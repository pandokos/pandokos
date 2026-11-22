package admin_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// people is an administration API with sign-in (sprint 4.2), a platform administrator and a tenant.
type people struct {
	api      *apitest.API
	d        *dbtest.Database
	root     dbtest.Person // a platform administrator, signed in nowhere yet
	tenant   adminapi.Tenant
	tid      tenant.ID
	adminKey string // the key of the first administrator of the tenant
}

func newPeople(t *testing.T, cfg apitest.PeopleConfig) *people {
	t.Helper()
	return peopleOn(t, apitest.NewPeople(t, cfg))
}

func peopleOn(t *testing.T, api *apitest.API) *people {
	t.Helper()
	p := &people{api: api, d: api.DB}
	p.root = p.d.PlatformUser(t, "root.admin", []string{"platform-admin"})
	p.tenant, p.adminKey = api.Tenant(t, "acme")
	var err error
	if p.tid, err = tenant.ParseID(p.tenant.ID); err != nil {
		t.Fatal(err)
	}
	return p
}

// signIn returns a browser signed in as the person.
func (p *people) signIn(t *testing.T, who dbtest.Person) *apitest.Browser {
	t.Helper()
	b := p.api.NewBrowser()
	b.MustSignIn(t, who.Username, who.Password)
	return b
}

// sessionOf returns a browser holding a session that is made without a sign-in (a seeded session).
func (p *people) sessionOf(t *testing.T, userID string) *apitest.Browser {
	t.Helper()
	b := p.api.NewBrowser()
	var tok string
	err := p.api.Admin.Platform(context.Background(), func(ctx context.Context, tx store.Tx) error {
		secretTok, _, err := p.api.People.Sessions.Create(ctx, tx, userID, "local")
		tok = secretTok.Reveal()
		return err
	})
	if err != nil {
		t.Fatalf("seed a session: %v", err)
	}
	b.SetCookie(session.CookieName, tok)
	return b
}

// platformCall is a call with the platform key.
func (p *people) platform(t *testing.T, method, path string, body any) *apitest.Response {
	t.Helper()
	return p.api.Do(t, method, path, p.api.PlatformKey(), body, nil)
}

// tenantCall is a call with the key of the first administrator of the tenant.
func (p *people) tenantKey(t *testing.T, method, path string, body any) *apitest.Response {
	t.Helper()
	return p.api.Do(t, method, path, p.adminKey, body, nil)
}

func (p *people) membersPath(suffix string) string {
	return "/v1/tenants/" + p.tenant.ID + "/members" + suffix
}

var reIDs = regexp.MustCompile(`[0-9a-f]{32}`)

// normalize removes what differs between two answers of one kind: the request ID.
func normalize(r *apitest.Response) string {
	return reIDs.ReplaceAllString(string(r.Body), "ID")
}

func code(t testing.TB, r *apitest.Response, status int) adminapi.ProblemCode {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d\n%s", r.Status, status, r.Body)
	}
	return problem(t, r).Code
}

// setCookies returns the Set-Cookie headers of a response.
func setCookies(r *apitest.Response) []string { return r.Header.Values("Set-Cookie") }

// clearsSession reports whether the response tells the browser to forget the session cookie.
func clearsSession(r *apitest.Response) bool {
	for _, c := range setCookies(r) {
		if strings.HasPrefix(c, session.CookieName+"=;") && strings.Contains(c, "Max-Age=0") {
			return true
		}
	}
	return false
}

func since(t time.Time) time.Duration { return time.Since(t) }

// queryOf returns the query of a Location.
func queryOf(t testing.TB, loc string) url.Values {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

var _ = http.StatusOK
