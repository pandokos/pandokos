package apitest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/test/dbtest"
)

// Clock is the real time plus an offset that a test moves: SQL stamps rows with now(), so a clock that
// started elsewhere would disagree with them. Where a test needs an instant far from now it ages the
// rows (dbtest.Age) instead.
type Clock struct{ offset atomic.Int64 }

// Now returns the time of the clock.
func (c *Clock) Now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) { c.offset.Add(int64(d)) }

// PeopleConfig builds the administration API with sign-in, sessions and idempotency keys (sprint
// 4.2). Every function is optional and runs after the defaults are set: the defaults of section
// 5.15, the floor parameters of Argon2id and a listener on a loopback address without TLS.
type PeopleConfig struct {
	Account func(*account.Options)
	Session func(*session.Options)
	Idem    func(*idem.Options)
	Admin   func(*admin.Options)
	Params  *password.Params // Argon2id parameters of the service; the floor when nil
	// MaxHashes is auth.local.max_concurrent_hashes; 4 when zero.
	MaxHashes int
}

// People are the services behind the handler of an API made by NewPeople.
type People struct {
	Clock    *Clock
	Hasher   *password.Hasher
	Sessions *session.Store
	Accounts *account.Service
	Idem     *idem.Store
}

// NewPeople creates a migrated database and an API on it with the services of sprint 4.2.
func NewPeople(tb testing.TB, cfg PeopleConfig) *API {
	tb.Helper()
	return NewPeopleOn(tb, dbtest.NewMigrated(tb), cfg)
}

// NewPeopleOn is NewPeople on a given, migrated database.
func NewPeopleOn(tb testing.TB, d *dbtest.Database, cfg PeopleConfig) *API {
	tb.Helper()
	p := &People{Clock: &Clock{}}
	a := NewOn(tb, d, func(o *admin.Options) {
		params := dbtest.FloorParams
		if cfg.Params != nil {
			params = *cfg.Params
		}
		var err error
		maxHashes := cfg.MaxHashes
		if maxHashes == 0 {
			maxHashes = 4
		}
		if p.Hasher, err = password.New(params, maxHashes); err != nil {
			tb.Fatalf("password.New: %v", err)
		}
		so := session.Options{DB: o.DB, Now: p.Clock.Now, Idle: 30 * time.Minute, Absolute: 12 * time.Hour, MaxPerUser: 10}
		if cfg.Session != nil {
			cfg.Session(&so)
		}
		p.Sessions = session.NewStore(so)
		ao := account.Options{
			DB: o.DB, Hasher: p.Hasher, Sessions: p.Sessions, Now: p.Clock.Now, MinLength: 12, GeneratedPasswordTTL: 72 * time.Hour,
			MaxFailures: 10, SourceMaxFailures: 50, FailureWindow: 15 * time.Minute, LockDuration: 15 * time.Minute,
		}
		if cfg.Account != nil {
			cfg.Account(&ao)
		}
		p.Accounts = account.NewService(ao)
		io := idem.Options{DB: o.DB, Now: p.Clock.Now, Retention: 24 * time.Hour}
		if cfg.Idem != nil {
			cfg.Idem(&io)
		}
		p.Idem = idem.NewStore(io)
		o.Now = p.Clock.Now
		o.Sessions, o.Accounts, o.Idempotency = p.Sessions, p.Accounts, p.Idem
		o.Origin = admin.OriginOptions{Loopback: true}
		o.Auth = admin.AuthOptions{LocalEnabled: true, PostSignInPath: "/"}
		if cfg.Admin != nil {
			cfg.Admin(o)
		}
	})
	a.People = p
	return a
}

// Canary returns a value made when the test runs, of the kind given ("password", "token", "username"):
// no value is written down, and two runs never share one.
func Canary(kind string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "canary-" + kind + "-" + hex.EncodeToString(b)
}

// NewPassword returns a random password of 20 characters that satisfies the policy.
func NewPassword(tb testing.TB) string { return dbtest.NewPassword(tb) }

// Hop is one response of a navigation.
type Hop struct {
	URL      string
	Status   int
	Location string
	Header   http.Header
	Body     []byte
}

// Browser is a client that behaves as a browser does for the administration API: it keeps the
// cookies of the origin, `Secure` ones included, sends the CSRF header and the Origin that the page
// would, and never follows a redirect by itself.
type Browser struct {
	API *API
	// Origin is what the Origin header says; the origin of the API when empty.
	Origin string
	// NoCSRF, NoOrigin leave the header out; SecFetchSite, when set, is sent.
	NoCSRF, NoOrigin bool
	SecFetchSite     string
	// Header is added to every request.
	Header http.Header

	cookies map[string]string
	client  *http.Client
	baseURL string
}

// NewBrowser returns a browser for the API.
func (a *API) NewBrowser() *Browser {
	b := NewBrowserAt(a.URL, a.srv.Client().Transport)
	b.API = a
	b.Origin = a.PublicOrigin
	return b
}

// NewBrowserAt returns a browser for an administration listener at baseURL (a process under test):
// transport is the one that trusts the listener's certificate, or nil.
func NewBrowserAt(baseURL string, transport http.RoundTripper) *Browser {
	return &Browser{
		baseURL: baseURL, cookies: map[string]string{}, Header: http.Header{},
		client: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       30 * time.Second,
		},
	}
}

// Cookie returns the value of a cookie the browser holds, or "".
func (b *Browser) Cookie(name string) string { return b.cookies[name] }

// SetCookie plants a cookie, as a page of the origin (or the user) could.
func (b *Browser) SetCookie(name, value string) { b.cookies[name] = value }

// Token returns the value of the session cookie.
func (b *Browser) Token() string { return b.cookies[session.CookieName] }

// Cookies returns the cookies as a Cookie header would carry them.
func (b *Browser) cookieHeader() string {
	var parts []string
	for n, v := range b.cookies {
		parts = append(parts, n+"="+v)
	}
	return strings.Join(parts, "; ")
}

func (b *Browser) origin() string {
	if b.Origin != "" {
		return b.Origin
	}
	return b.baseURL
}

// Do sends a request as the page would: with the cookies, the CSRF header and the Origin unless the
// browser was told otherwise. A body is sent as JSON (strings and byte slices as they are).
func (b *Browser) Do(tb testing.TB, method, path string, body any, hdr http.Header) *Response {
	tb.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	case []byte:
		rd = bytes.NewReader(v)
	default:
		enc, err := json.Marshal(v)
		if err != nil {
			tb.Fatal(err)
		}
		rd = bytes.NewReader(enc)
	}
	req, err := http.NewRequest(method, b.baseURL+path, rd)
	if err != nil {
		tb.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !b.NoCSRF {
		req.Header.Set("X-Pandokos-Csrf", "1")
	}
	if !b.NoOrigin {
		req.Header.Set("Origin", b.origin())
	}
	if b.SecFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", b.SecFetchSite)
	}
	if c := b.cookieHeader(); c != "" {
		req.Header.Set("Cookie", c)
	}
	for k, v := range b.Header {
		req.Header[k] = v
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := b.client.Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatal(err)
	}
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: out}
}

// SignIn posts the credentials to the sign-in operation as the page would.
func (b *Browser) SignIn(tb testing.TB, username, pw string) *Response {
	tb.Helper()
	return b.Do(tb, http.MethodPost, "/v1/auth/sign-in", map[string]any{"username": username, "password": pw}, nil)
}

// MustSignIn signs in and fails the test unless the answer is 200.
func (b *Browser) MustSignIn(tb testing.TB, username, pw string) *Response {
	tb.Helper()
	r := b.SignIn(tb, username, pw)
	if r.Status != http.StatusOK {
		tb.Fatalf("sign-in as %s: %d %s", username, r.Status, r.Body)
	}
	return r
}

// Navigate does what a navigation does: a GET, and each 303 followed by hand on the same origin,
// without the CSRF header, with the Origin the browser sends to its own origin. It returns every hop;
// a redirect to another origin, or to a page that is not of the API (/v1/), is the last hop.
func (b *Browser) Navigate(tb testing.TB, path string) []Hop {
	tb.Helper()
	var hops []Hop
	for i := 0; i < 8; i++ {
		saveCSRF, saveOrigin := b.NoCSRF, b.NoOrigin
		b.NoCSRF = true
		r := b.Do(tb, http.MethodGet, path, nil, nil)
		b.NoCSRF, b.NoOrigin = saveCSRF, saveOrigin
		h := Hop{URL: path, Status: r.Status, Location: r.Header.Get("Location"), Header: r.Header, Body: r.Body}
		hops = append(hops, h)
		if r.Status != http.StatusSeeOther || h.Location == "" {
			return hops
		}
		u, err := url.Parse(h.Location)
		if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/v1/") {
			return hops // another origin, or a page of the console: the API does not serve it
		}
		path = h.Location
	}
	tb.Fatal("too many redirects")
	return hops
}

// JSON decodes the body of a response into v.
func (r *Response) JSON(tb testing.TB, v any) {
	tb.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		tb.Fatalf("response %d: %v\n%s", r.Status, err, r.Body)
	}
}

// Problem returns the code of a problem response, or "".
func (r *Response) Problem() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}
