package fakeidp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The authorization code flow of sprint 4.2: /authorize answers at once with a code for the person
// the test chose, /token checks everything a relying party must get right (client authentication,
// redirect URI, PKCE verifier, one use of a code) and returns an ID token whose claims the test
// shapes. The client ID, the client secret and every token are generated when Start runs.

const codeLifetime = 60 * time.Second

// TokenRequest is what the token endpoint received.
type TokenRequest struct {
	Form         url.Values
	BasicUser    string // from the Authorization header, when the request used client_secret_basic
	BasicSecret  string
	HasBasic     bool
	Header       http.Header
	ContentType  string
	RequestedAt  time.Time
	Authenticate string // "basic", "post" or "none"
}

type issuedCode struct {
	subject, redirectURI, challenge, nonce string
	at                                     time.Time
	used                                   bool
}

type flowState struct {
	mu                         sync.Mutex
	clientID, clientSecret     string
	accessToken, refreshToken  string
	codes                      map[string]*issuedCode
	signInAs                   string
	idTokenClaims              func(Claims) Claims
	idTokenAlg                 string
	signWith                   any
	tokenStatus                int
	tokenBody                  []byte
	tokenDelay                 time.Duration
	tokenRedirect              string
	callbackError, callbackIss *string
	redirectURIs               []string
	lastCode, lastIDToken      string
	requests                   []TokenRequest
	tokenRequests              atomic.Int64
	authorizeRequests          atomic.Int64
	tb                         testing.TB
}

func randomToken(tb testing.TB, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (s *Server) initFlow() {
	f := &s.flow
	f.tb = s.tb
	f.clientID = "pandokos-" + randomToken(s.tb, 6)
	f.clientSecret = randomToken(s.tb, 24)
	f.accessToken = "at-" + randomToken(s.tb, 20)
	f.refreshToken = "rt-" + randomToken(s.tb, 20)
	f.codes = map[string]*issuedCode{}
	f.idTokenAlg = "RS256"
	for sub := range s.opt.Users {
		f.signInAs = sub
		break
	}
}

// RegisterRedirectURI adds uri to the redirect URIs of the client, as at a real provider: the
// authorization endpoint refuses a redirect_uri that is not one of them, compared exactly, and
// redirects only to the registered value.
func (s *Server) RegisterRedirectURI(uri string) {
	s.flow.mu.Lock()
	defer s.flow.mu.Unlock()
	s.flow.redirectURIs = append(s.flow.redirectURIs, uri)
}

// registered returns the registered redirect URI equal to uri, or "" when there is none.
func (f *flowState) registered(uri string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.redirectURIs {
		if r == uri {
			return r
		}
	}
	return ""
}

// ClientID returns the client ID that the token endpoint accepts.
func (s *Server) ClientID() string { return s.flow.clientID }

// ClientSecret returns the client secret that the token endpoint accepts.
func (s *Server) ClientSecret() string { return s.flow.clientSecret }

// AccessToken returns the access token that every token response carries; RefreshToken the
// refresh token. The canary tests search every output for them.
func (s *Server) AccessToken() string { return s.flow.accessToken }

// RefreshToken returns the refresh token of every token response.
func (s *Server) RefreshToken() string { return s.flow.refreshToken }

// LastCode returns the last authorization code issued; LastIDToken the last ID token returned.
func (s *Server) LastCode() string {
	s.flow.mu.Lock()
	defer s.flow.mu.Unlock()
	return s.flow.lastCode
}

// LastIDToken returns the last ID token the token endpoint returned.
func (s *Server) LastIDToken() string {
	s.flow.mu.Lock()
	defer s.flow.mu.Unlock()
	return s.flow.lastIDToken
}

// AuthorizeURL returns the address of the authorization endpoint.
func (s *Server) AuthorizeURL() string { return "https://" + s.Host() + s.base + "/authorize" }

// TokenURL returns the address of the token endpoint.
func (s *Server) TokenURL() string { return "https://" + s.Host() + s.base + "/token" }

// SignInAs chooses the person that the next authorization requests sign in: a subject of
// Options.Users, or any subject (then the ID token carries the standard claims only).
func (s *Server) SignInAs(subject string) {
	s.flow.mu.Lock()
	s.flow.signInAs = subject
	s.flow.mu.Unlock()
}

// TokenRequests returns the number of requests the token endpoint received.
func (s *Server) TokenRequests() int { return int(s.flow.tokenRequests.Load()) }

// AuthorizeRequests returns the number of requests the authorization endpoint received.
func (s *Server) AuthorizeRequests() int { return int(s.flow.authorizeRequests.Load()) }

// TokenRequestLog returns the requests the token endpoint received, oldest first.
func (s *Server) TokenRequestLog() []TokenRequest {
	s.flow.mu.Lock()
	defer s.flow.mu.Unlock()
	return append([]TokenRequest(nil), s.flow.requests...)
}

// IDTokenClaims changes the claims of the next ID tokens: f receives the claims the fake would
// send and returns the ones it sends. nil restores them.
func (s *Server) IDTokenClaims(f func(Claims) Claims) {
	s.flow.mu.Lock()
	s.flow.idTokenClaims = f
	s.flow.mu.Unlock()
}

// IDTokenAlg sets the algorithm the ID token is signed with (RS256 by default).
func (s *Server) IDTokenAlg(alg string) {
	s.flow.mu.Lock()
	s.flow.idTokenAlg = alg
	s.flow.mu.Unlock()
}

// SignIDTokenWith signs the ID token with a key that is not the issuer's (a *rsa.PrivateKey, an
// *ecdsa.PrivateKey, ed25519.PrivateKey), carrying the key ID of the issuer's own key. nil
// restores it.
func (s *Server) SignIDTokenWith(key any) {
	s.flow.mu.Lock()
	s.flow.signWith = key
	s.flow.mu.Unlock()
}

// TokenStatus makes the token endpoint answer with the status and an empty body; 0 restores it.
func (s *Server) TokenStatus(code int) {
	s.flow.mu.Lock()
	s.flow.tokenStatus = code
	s.flow.mu.Unlock()
}

// TokenBody makes the token endpoint answer 200 with the body as it is; nil restores it.
func (s *Server) TokenBody(b []byte) {
	s.flow.mu.Lock()
	s.flow.tokenBody = b
	s.flow.mu.Unlock()
}

// TokenDelay makes the token endpoint wait before it answers.
func (s *Server) TokenDelay(d time.Duration) {
	s.flow.mu.Lock()
	s.flow.tokenDelay = d
	s.flow.mu.Unlock()
}

// TokenRedirect makes the token endpoint answer 302 to the address; "" restores it.
func (s *Server) TokenRedirect(to string) {
	s.flow.mu.Lock()
	s.flow.tokenRedirect = to
	s.flow.mu.Unlock()
}

// CallbackError makes the authorization endpoint redirect with error=<code> and no code; nil
// restores it.
func (s *Server) CallbackError(code *string) {
	s.flow.mu.Lock()
	s.flow.callbackError = code
	s.flow.mu.Unlock()
}

// CallbackIss makes the authorization endpoint add iss=<value> to the redirect (RFC 9207); nil
// restores it.
func (s *Server) CallbackIss(iss *string) {
	s.flow.mu.Lock()
	s.flow.callbackIss = iss
	s.flow.mu.Unlock()
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	s.flow.authorizeRequests.Add(1)
	q := r.URL.Query()
	bad := func(msg string) { http.Error(w, "invalid_request: "+msg, http.StatusBadRequest) }
	switch {
	case q.Get("client_id") != s.flow.clientID:
		bad("client_id")
		return
	case q.Get("response_type") != "code":
		bad("response_type")
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		bad("code_challenge")
		return
	case q.Get("state") == "" || q.Get("nonce") == "":
		bad("state and nonce")
		return
	}
	// Only a registered redirect URI, and the address redirected to is built from the registered
	// value, never from the request.
	target := s.flow.registered(q.Get("redirect_uri"))
	redirect, err := url.Parse(target)
	if target == "" || err != nil || redirect.Host == "" || redirect.Fragment != "" || (redirect.Scheme != "https" && redirect.Scheme != "http") {
		bad("redirect_uri")
		return
	}
	s.flow.mu.Lock()
	cbErr, cbIss, subject := s.flow.callbackError, s.flow.callbackIss, s.flow.signInAs
	code := "code-" + randomToken(s.tb, 20)
	if cbErr == nil {
		s.flow.codes[code] = &issuedCode{subject: subject, redirectURI: target, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), at: time.Now()}
		s.flow.lastCode = code
	}
	s.flow.mu.Unlock()
	out := url.Values{"state": {q.Get("state")}}
	if cbErr != nil {
		out.Set("error", *cbErr)
	} else {
		out.Set("code", code)
	}
	if cbIss != nil {
		out.Set("iss", *cbIss)
	}
	query := redirect.Query()
	for k, v := range out {
		query[k] = v
	}
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	s.flow.tokenRequests.Add(1)
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	form, _ := url.ParseQuery(string(body))
	req := TokenRequest{Form: form, Header: r.Header.Clone(), ContentType: r.Header.Get("Content-Type"), RequestedAt: time.Now(), Authenticate: "none"}
	if u, p, ok := r.BasicAuth(); ok {
		req.BasicUser, req.BasicSecret, req.HasBasic, req.Authenticate = u, p, true, "basic"
	} else if form.Get("client_secret") != "" {
		req.Authenticate = "post"
	}
	s.flow.mu.Lock()
	s.flow.requests = append(s.flow.requests, req)
	status, fixed, delay, redirect := s.flow.tokenStatus, s.flow.tokenBody, s.flow.tokenDelay, s.flow.tokenRedirect
	s.flow.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	switch {
	case redirect != "":
		http.Redirect(w, r, redirect, http.StatusFound)
		return
	case status != 0:
		w.WriteHeader(status)
		return
	case fixed != nil:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixed)
		return
	}
	reject := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}

	// Client authentication: basic or post, never both.
	id, secret := form.Get("client_id"), form.Get("client_secret")
	if req.HasBasic {
		id, secret = req.BasicUser, req.BasicSecret
		if form.Get("client_secret") != "" {
			reject("invalid_request")
			return
		}
	}
	if subtle.ConstantTimeCompare([]byte(id), []byte(s.flow.clientID)) != 1 || subtle.ConstantTimeCompare([]byte(secret), []byte(s.flow.clientSecret)) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	if form.Get("grant_type") != "authorization_code" {
		reject("unsupported_grant_type")
		return
	}
	s.flow.mu.Lock()
	c := s.flow.codes[form.Get("code")]
	var ok bool
	if c != nil && !c.used && time.Since(c.at) <= codeLifetime {
		c.used, ok = true, true
	}
	s.flow.mu.Unlock()
	if !ok {
		reject("invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if form.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge || form.Get("redirect_uri") != c.redirectURI {
		reject("invalid_grant")
		return
	}

	s.flow.mu.Lock()
	claims := s.StandardAt(time.Now(), c.subject, s.flow.clientID, 10*time.Minute)
	for k, v := range s.opt.Users[c.subject] {
		claims[k] = v
	}
	claims["nonce"] = c.nonce
	if s.flow.idTokenClaims != nil {
		claims = s.flow.idTokenClaims(claims)
	}
	alg, key := s.flow.idTokenAlg, s.flow.signWith
	s.flow.mu.Unlock()
	var idToken string
	switch {
	case alg == "none":
		idToken = s.Raw(map[string]any{"alg": "none", "typ": "JWT"}, claims, nil)
	case alg == "HS256": // keyed with the public key of the issuer: the classic confusion
		idToken = s.Raw(map[string]any{"alg": "HS256", "typ": "JWT", "kid": s.KeyID("RS256")}, claims, HS256(s.PublicKeyDER("RS256")))
	case key != nil:
		idToken = s.Raw(map[string]any{"alg": alg, "typ": "JWT", "kid": s.KeyID(alg)}, claims, SignWithKey(s.tb, alg, key))
	default:
		idToken = s.Sign(alg, claims)
	}
	s.flow.mu.Lock()
	s.flow.lastIDToken = idToken
	s.flow.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id_token": idToken, "access_token": s.flow.accessToken, "refresh_token": s.flow.refreshToken,
		"token_type": "Bearer", "expires_in": 600,
	})
}

// Authorize plays the browser at the authorization endpoint: it requests authURL (the redirect that
// a relying party gave) without following the answer and returns the query of the address the
// provider redirected to, and that address. A provider that refuses the request is a test failure.
func (s *Server) Authorize(authURL string) (query url.Values, location string) {
	s.tb.Helper()
	client := &http.Client{Transport: s.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	if err != nil {
		s.tb.Fatalf("fakeidp: authorize: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		s.tb.Fatalf("fakeidp: the authorization endpoint answered %d: %s", resp.StatusCode, b)
	}
	location = resp.Header.Get("Location")
	u, err := url.Parse(location)
	if err != nil {
		s.tb.Fatalf("fakeidp: Location %q: %v", location, err)
	}
	return u.Query(), location
}
