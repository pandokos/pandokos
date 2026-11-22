package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

const callback = "https://pandokos.example.test/v1/auth/oidc/callback"

type rig struct {
	idp  *fakeidp.Server
	p    *oidc.Provider
	logs *testutil.LogBuffer
}

var people = map[string]fakeidp.Claims{
	"ada": {"name": "Ada Lovelace", "email": "ada@example.test", "groups": []string{"admins", "eng"}},
	"bob": {"name": "Bob", "email": "bob@example.test"},
}

func guardFor(t testing.TB, idp *fakeidp.Server) *egress.Guard {
	t.Helper()
	pem, err := os.ReadFile(idp.CAFile())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the CA of the fake issuer")
	}
	g, err := egress.New(egress.Options{ConnectTimeout: 2 * time.Second, RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// newRig builds a provider over the fake issuer with the transport of the egress guard, as serve
// builds it, the issuer's host being a private one that the configuration lists.
func newRig(t testing.TB, mutate ...func(*oidc.Options)) *rig {
	t.Helper()
	idp := fakeidp.Start(t, fakeidp.Options{Users: people})
	idp.SignInAs("ada")
	return newRigOn(t, idp, mutate...)
}

func newRigOn(t testing.TB, idp *fakeidp.Server, mutate ...func(*oidc.Options)) *rig {
	t.Helper()
	r := &rig{idp: idp, logs: &testutil.LogBuffer{}}
	logger := telemetry.NewLogger(r.logs, "debug")
	g := guardFor(t, idp)
	cache, err := jwks.New(jwks.Options{
		Transport: g.Transport, PrivateHosts: []string{"localhost"}, FetchTimeout: 2 * time.Second,
		RefreshInterval: 15 * time.Minute, MinRefetchInterval: time.Second, MaxStale: 6 * time.Hour, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	o := oidc.Options{
		Issuer: idp.Issuer(), ClientID: idp.ClientID(), ClientSecret: secret.New(idp.ClientSecret()), ClientAuth: "basic",
		RedirectURI: callback, Scopes: []string{"openid", "profile", "email"}, Algorithms: []string{"RS256", "ES256"},
		NameClaim: "name", EmailClaim: "email", GroupsClaim: "groups", Cache: cache, Logger: logger,
		HTTP: &http.Client{
			Transport:     g.Transport(true),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       10 * time.Second,
		},
	}
	for _, m := range mutate {
		m(&o)
	}
	if r.p, err = oidc.New(o); err != nil {
		t.Fatalf("oidc.New: %v", err)
	}
	return r
}

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return c
}

// flow starts a sign-in, plays the browser at the provider and returns what the callback carries.
func (r *rig) flow(t testing.TB) (seed secret.Secret, query url.Values, authURL *url.URL) {
	t.Helper()
	redirect, seed, _, err := r.p.Start(ctx(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	authURL, err = url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	query, _ = r.idp.Authorize(redirect)
	return seed, query, authURL
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mac(seed secret.Secret, label string) string {
	key, err := base64.RawURLEncoding.DecodeString(seed.Reveal())
	if err != nil {
		return "(the seed is not base64url)"
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(label))
	return b64(h.Sum(nil))
}

func wantFailure(t testing.TB, what string, err error, reasons ...string) {
	t.Helper()
	var f *oidc.Failure
	if !errors.As(err, &f) {
		t.Errorf("%s: err = %v, want a *oidc.Failure", what, err)
		return
	}
	for _, want := range reasons {
		if f.Reason == want {
			return
		}
	}
	t.Errorf("%s: reason %q, want one of %v", what, f.Reason, reasons)
}

// TestStart is T-807.
func TestStart(t *testing.T) {
	r := newRig(t)
	redirect, seed, stateHash, err := r.p.Start(ctx(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != r.idp.AuthorizeURL() {
		t.Errorf("redirect to %s, want the authorization endpoint %s", got, r.idp.AuthorizeURL())
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != r.idp.ClientID() || q.Get("redirect_uri") != callback ||
		q.Get("scope") != "openid profile email" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("parameters %v", q)
	}
	for _, name := range []string{"state", "nonce", "code_challenge"} {
		if len(q.Get(name)) != 43 || strings.Trim(q.Get(name), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Errorf("%s %q: want 32 bytes in unpadded base64url", name, q.Get(name))
		}
	}
	if len(q) != 8 {
		t.Errorf("%d parameters, want the eight of section 5.8: %v", len(q), q)
	}

	// State, nonce and challenge are functions of the seed (HMAC-SHA-256 with the labels of section 5.8).
	if raw, err := base64.RawURLEncoding.DecodeString(seed.Reveal()); err != nil || len(raw) != 32 {
		t.Fatalf("the seed is not 32 bytes in unpadded base64url")
	}
	if q.Get("state") != mac(seed, "state") || q.Get("nonce") != mac(seed, "nonce") {
		t.Error("state or nonce is not HMAC-SHA-256(seed, label)")
	}
	verifier := mac(seed, "pkce")
	if sum := sha256.Sum256([]byte(verifier)); q.Get("code_challenge") != b64(sum[:]) {
		t.Error("the challenge is not the SHA-256 of the verifier derived from the seed")
	}
	if want := sha256.Sum256([]byte(q.Get("state"))); stateHash != want {
		t.Error("the state hash is not the SHA-256 of the state")
	}

	// Two starts share nothing.
	redirect2, seed2, hash2, err := r.p.Start(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	q2, _ := url.ParseQuery(strings.SplitN(redirect2, "?", 2)[1])
	if seed2.Reveal() == seed.Reveal() || hash2 == stateHash {
		t.Error("two starts share a seed or a state hash")
	}
	for _, name := range []string{"state", "nonce", "code_challenge"} {
		if q2.Get(name) == q.Get(name) {
			t.Errorf("two starts share the %s", name)
		}
	}

	// The cookie.
	want := "__Host-pandokos_oidc=" + seed.Reveal() + "; Path=/; Max-Age=600; HttpOnly; Secure; SameSite=Lax"
	if got := oidc.SeedCookie(seed).String(); got != want {
		t.Errorf("cookie %q, want %q", got, want)
	}
	if got := oidc.ClearSeedCookie().String(); got != "__Host-pandokos_oidc=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax" {
		t.Errorf("clearing cookie %q", got)
	}

	// The provider cannot be reached: unavailable, and no seed worth keeping.
	r.idp.Stop()
	other := newRigOn(t, fakeidp.Start(t, fakeidp.Options{Users: people}))
	other.idp.Stop()
	if _, _, _, err := other.p.Start(ctx(t)); !errors.Is(err, authz.ErrUnavailable) {
		t.Errorf("Start with the provider stopped: %v, want authz.ErrUnavailable", err)
	}
}

// TestCompleteSignsIn is the success path that the other tests change one thing of.
func TestCompleteSignsIn(t *testing.T) {
	r := newRig(t)
	seed, q, _ := r.flow(t)
	id, err := r.p.Complete(ctx(t), seed, q)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if id.Subject != "ada" || id.Name != "Ada Lovelace" || id.Email != "ada@example.test" || !reflect.DeepEqual(id.Groups, []string{"admins", "eng"}) {
		t.Errorf("identity %+v", id)
	}
	if r.p.Issuer() != r.idp.Issuer() {
		t.Errorf("Issuer %q", r.p.Issuer())
	}
	// A person without groups, name or e-mail claims.
	r.idp.SignInAs("bob")
	seed, q, _ = r.flow(t)
	if id, err = r.p.Complete(ctx(t), seed, q); err != nil || id.Subject != "bob" || len(id.Groups) != 0 {
		t.Errorf("identity %+v %v", id, err)
	}
}

// TestGroupsClaimShapeLogged: a groups claim of another shape gives no group and a warning that says
// the shape and nothing of the claim (section 5.9).
func TestGroupsClaimShapeLogged(t *testing.T) {
	canary := "canary-" + b64([]byte(t.Name()))
	idp := fakeidp.Start(t, fakeidp.Options{Users: map[string]fakeidp.Claims{
		"ada":    {"groups": []string{"admins"}},
		"none":   {"name": "None"},
		"object": {"groups": map[string]any{"k": canary}},
		"number": {"groups": 3},
		"mixed":  {"groups": []any{"a", 1}},
	}})
	r := newRigOn(t, idp)
	signIn := func(sub string) oidc.Identity {
		t.Helper()
		idp.SignInAs(sub)
		seed, q, _ := r.flow(t)
		id, err := r.p.Complete(ctx(t), seed, q)
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		return id
	}
	for _, sub := range []string{"ada", "none"} {
		signIn(sub)
	}
	if strings.Contains(r.logs.String(), "groups claim") {
		t.Errorf("a warning for a usable or an absent claim: %s", r.logs.String())
	}
	for _, sub := range []string{"object", "number", "mixed"} {
		before := strings.Count(r.logs.String(), "groups claim has an unexpected shape")
		if id := signIn(sub); len(id.Groups) != 0 {
			t.Errorf("%s: groups %v", sub, id.Groups)
		}
		if got := strings.Count(r.logs.String(), "groups claim has an unexpected shape"); got != before+1 {
			t.Errorf("%s: %d warnings, want 1 more than %d", sub, got, before)
		}
	}
	if strings.Contains(r.logs.String(), canary) {
		t.Error("the log holds the content of the claim")
	}
}

// TestCodeExchange is T-809.
func TestCodeExchange(t *testing.T) {
	t.Run("the token request, in both client authentication modes", func(t *testing.T) {
		for _, mode := range []string{"basic", "post"} {
			r := newRig(t, func(o *oidc.Options) { o.ClientAuth = mode })
			seed, q, authURL := r.flow(t)
			if _, err := r.p.Complete(ctx(t), seed, q); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			log := r.idp.TokenRequestLog()
			if len(log) != 1 {
				t.Fatalf("%s: %d token requests", mode, len(log))
			}
			req := log[0]
			f := req.Form
			if f.Get("grant_type") != "authorization_code" || f.Get("code") != q.Get("code") || f.Get("redirect_uri") != callback {
				t.Errorf("%s: form %v", mode, f)
			}
			sum := sha256.Sum256([]byte(f.Get("code_verifier")))
			if f.Get("code_verifier") == "" || b64(sum[:]) != authURL.Query().Get("code_challenge") {
				t.Errorf("%s: the verifier does not match the challenge of the start", mode)
			}
			if !strings.HasPrefix(req.ContentType, "application/x-www-form-urlencoded") {
				t.Errorf("%s: content type %q", mode, req.ContentType)
			}
			switch mode {
			case "basic":
				if !req.HasBasic || req.BasicUser != r.idp.ClientID() || req.BasicSecret != r.idp.ClientSecret() || f.Get("client_secret") != "" {
					t.Errorf("basic: %+v", req)
				}
			case "post":
				if req.HasBasic || f.Get("client_id") != r.idp.ClientID() || f.Get("client_secret") != r.idp.ClientSecret() {
					t.Errorf("post: %+v", req)
				}
			}
		}
	})

	failing := map[string]struct {
		set     func(*fakeidp.Server)
		failure []string // a *Failure with one of these reasons; empty: unavailable
	}{
		"a code the provider refuses": {func(s *fakeidp.Server) { s.TokenStatus(http.StatusBadRequest) }, []string{"code_rejected"}},
		"an answer without id_token": {func(s *fakeidp.Server) {
			s.TokenBody([]byte(`{"access_token":"x","token_type":"Bearer"}`))
		}, []string{"code_rejected", "no_id_token"}},
		"an answer that is not JSON": {func(s *fakeidp.Server) { s.TokenBody([]byte("<html>")) }, []string{"code_rejected"}},
		"an answer over 64 KiB": {func(s *fakeidp.Server) {
			s.TokenBody([]byte(`{"id_token":"` + strings.Repeat("a", 70<<10) + `"}`))
		}, []string{"code_rejected"}},
		"a redirect":     {func(s *fakeidp.Server) { s.TokenRedirect("https://elsewhere.example.test/token") }, []string{"code_rejected"}},
		"a server error": {func(s *fakeidp.Server) { s.TokenStatus(http.StatusInternalServerError) }, nil},
	}
	for name, c := range failing {
		r := newRig(t)
		seed, q, _ := r.flow(t)
		c.set(r.idp)
		id, err := r.p.Complete(ctx(t), seed, q)
		if err == nil || id.Subject != "" {
			t.Errorf("%s: signed in (%+v)", name, id)
			continue
		}
		if len(c.failure) > 0 {
			wantFailure(t, name, err, c.failure...)
		} else if !errors.Is(err, authz.ErrUnavailable) {
			t.Errorf("%s: %v, want authz.ErrUnavailable", name, err)
		}
	}

	// A slow answer and a stopped provider are outages.
	r := newRig(t)
	seed, q, _ := r.flow(t)
	r.idp.TokenDelay(3 * time.Second)
	short, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := r.p.Complete(short, seed, q); err == nil || errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("a slow answer: %v, want an outage", err)
	}
	r = newRig(t)
	seed, q, _ = r.flow(t)
	r.idp.Stop()
	if _, err := r.p.Complete(ctx(t), seed, q); !errors.Is(err, authz.ErrUnavailable) {
		t.Errorf("a stopped provider: %v, want authz.ErrUnavailable", err)
	}

	// A code is redeemed once.
	r = newRig(t)
	seed, q, _ = r.flow(t)
	if _, err := r.p.Complete(ctx(t), seed, q); err != nil {
		t.Fatal(err)
	}
	if _, err := r.p.Complete(ctx(t), seed, q); err == nil {
		t.Error("the same callback signed in twice")
	}
}

// TestCompleteQuery checks what Complete refuses before it asks the provider anything.
func TestCompleteQuery(t *testing.T) {
	r := newRig(t)
	seed, q, _ := r.flow(t)
	clone := func(edit func(url.Values)) url.Values {
		c := url.Values{}
		for k, v := range q {
			c[k] = append([]string(nil), v...)
		}
		edit(c)
		return c
	}
	other, _ := secret.New(b64(make([]byte, 32))), 0
	cases := map[string]struct {
		seed    secret.Secret
		query   url.Values
		reasons []string
	}{
		"no seed":                      {secret.Secret{}, q, []string{"state_mismatch"}},
		"another seed":                 {other, q, []string{"state_mismatch"}},
		"a seed that is not base64url": {secret.New("not base64!"), q, []string{"state_mismatch"}},
		"no state":                     {seed, clone(func(v url.Values) { v.Del("state") }), []string{"state_mismatch"}},
		"another state":                {seed, clone(func(v url.Values) { v.Set("state", strings.Repeat("A", 43)) }), []string{"state_mismatch"}},
		"an error of the provider":     {seed, clone(func(v url.Values) { v.Del("code"); v.Set("error", "access_denied") }), []string{"provider_error"}},
		"another iss":                  {seed, clone(func(v url.Values) { v.Set("iss", "https://other.example.test") }), []string{"provider_error", "issuer_mismatch"}},
		"no code":                      {seed, clone(func(v url.Values) { v.Del("code") }), []string{"code_rejected", "provider_error"}},
	}
	for name, c := range cases {
		id, err := r.p.Complete(ctx(t), c.seed, c.query)
		if err == nil || id.Subject != "" {
			t.Errorf("%s: accepted", name)
			continue
		}
		wantFailure(t, name, err, c.reasons...)
	}
	if n := r.idp.TokenRequests(); n != 0 {
		t.Errorf("the provider received %d token requests for callbacks that must be refused before", n)
	}
	// The provider names itself as the issuer: accepted (RFC 9207).
	good := clone(func(v url.Values) { v.Set("iss", r.idp.Issuer()) })
	if _, err := r.p.Complete(ctx(t), seed, good); err != nil {
		t.Errorf("a callback with the right iss: %v", err)
	}
	// Unknown parameters are tolerated: providers add their own.
	r.idp.SignInAs("ada")
	seed, q, _ = r.flow(t)
	q.Set("session_state", "x")
	q.Set("scope", "openid")
	if _, err := r.p.Complete(ctx(t), seed, q); err != nil {
		t.Errorf("a callback with parameters of the provider's own: %v", err)
	}
}

// TestIDToken is T-810.
func TestIDToken(t *testing.T) {
	foreignRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	foreignEC, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = foreignEC
	type edit = func(*fakeidp.Server, fakeidp.Claims) fakeidp.Claims
	cases := map[string]struct {
		prepare func(*fakeidp.Server)
		claims  edit
		reasons []string
	}{
		"signed by another key":       {func(s *fakeidp.Server) { s.SignIDTokenWith(foreignRSA) }, nil, []string{"bad_signature", "unknown_signing_key"}},
		"alg none":                    {func(s *fakeidp.Server) { s.IDTokenAlg("none") }, nil, []string{"algorithm_not_allowed", "malformed"}},
		"HMAC with the public key":    {func(s *fakeidp.Server) { s.IDTokenAlg("HS256") }, nil, []string{"algorithm_not_allowed"}},
		"an algorithm not configured": {func(s *fakeidp.Server) { s.IDTokenAlg("PS256") }, nil, []string{"algorithm_not_allowed"}},
		"wrong issuer": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("iss", "https://other.example.test")
		}, []string{"issuer_mismatch"}},
		"audience without the client": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("aud", "someone-else") }, []string{"wrong_audience"}},
		"no audience":                 {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.Without("aud") }, []string{"wrong_audience"}},
		"expired": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("iat", time.Now().Add(-2*time.Hour).Unix()).With("exp", time.Now().Add(-time.Hour).Unix())
		}, []string{"expired"}},
		"no exp": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.Without("exp") }, []string{"expired"}},
		"lifetime over a day": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("exp", time.Now().Add(48*time.Hour).Unix())
		}, []string{"lifetime_too_long"}},
		"no nonce": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.Without("nonce") }, []string{"nonce_mismatch"}},
		"the nonce of another flow": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("nonce", strings.Repeat("B", 43))
		}, []string{"nonce_mismatch"}},
		"nonce of another type": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("nonce", 42) }, []string{"nonce_mismatch"}},
		"two audiences without azp": {nil, func(s *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("aud", []string{s.ClientID(), "another-client"})
		}, []string{"azp_mismatch", "azp_missing"}},
		"two audiences, a foreign azp": {nil, func(s *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("aud", []string{s.ClientID(), "another-client"}).With("azp", "another-client")
		}, []string{"azp_mismatch"}},
		"one audience, a foreign azp":      {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("azp", "another-client") }, []string{"azp_mismatch"}},
		"no subject":                       {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.Without("sub") }, []string{"subject_missing"}},
		"empty subject":                    {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("sub", "") }, []string{"subject_missing"}},
		"subject with a control character": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("sub", "ad\na") }, []string{"subject_missing"}},
		"subject over 255 characters": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("sub", strings.Repeat("a", 256))
		}, []string{"subject_missing"}},
		"subject that is a number": {nil, func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("sub", 42) }, []string{"subject_missing"}},
	}
	for name, c := range cases {
		r := newRig(t)
		if c.prepare != nil {
			c.prepare(r.idp)
		}
		if c.claims != nil {
			edit := c.claims
			r.idp.IDTokenClaims(func(cl fakeidp.Claims) fakeidp.Claims { return edit(r.idp, cl) })
		}
		seed, q, _ := r.flow(t)
		id, err := r.p.Complete(ctx(t), seed, q)
		if err == nil || id.Subject != "" {
			t.Errorf("%s: accepted (%+v)", name, id)
			continue
		}
		wantFailure(t, name, err, c.reasons...)
	}

	// Accepted: azp equal to the client with several audiences; a subject of 255 characters; the
	// other asymmetric algorithm of the default list; the audience as an array of one.
	good := map[string]func(*fakeidp.Server, fakeidp.Claims) fakeidp.Claims{
		"several audiences with the right azp": func(s *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("aud", []string{s.ClientID(), "another-client"}).With("azp", s.ClientID())
		},
		"a subject of 255 characters": func(_ *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims {
			return c.With("sub", strings.Repeat("é", 255))
		},
		"an audience array of one": func(s *fakeidp.Server, c fakeidp.Claims) fakeidp.Claims { return c.With("aud", []string{s.ClientID()}) },
	}
	for name, edit := range good {
		r := newRig(t)
		e := edit
		r.idp.IDTokenClaims(func(cl fakeidp.Claims) fakeidp.Claims { return e(r.idp, cl) })
		seed, q, _ := r.flow(t)
		if _, err := r.p.Complete(ctx(t), seed, q); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	r := newRig(t)
	r.idp.IDTokenAlg("ES256")
	seed, q, _ := r.flow(t)
	if _, err := r.p.Complete(ctx(t), seed, q); err != nil {
		t.Errorf("ES256: %v", err)
	}
}

// TestMetadata is the oidc half of T-811: what a provider whose discovery document is wrong, or
// whose host is private and unlisted, or that cannot be reached, makes of a sign-in.
func TestMetadata(t *testing.T) {
	r := newRig(t)
	r.idp.SetFault(fakeidp.WrongIssuer)
	if _, _, _, err := r.p.Start(ctx(t)); !errors.Is(err, authz.ErrUnavailable) {
		t.Errorf("a document that names another issuer: %v, want authz.ErrUnavailable", err)
	}

	// A host that is private and not listed in auth.oidc.private_hosts is not fetched.
	idp := fakeidp.Start(t, fakeidp.Options{Users: people})
	g := guardFor(t, idp)
	unlisted, err := jwks.New(jwks.Options{
		Transport: g.Transport, FetchTimeout: 2 * time.Second, RefreshInterval: 15 * time.Minute, MinRefetchInterval: time.Second, MaxStale: 6 * time.Hour,
		Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"),
	})
	if err != nil {
		t.Fatal(err)
	}
	r = newRigOn(t, idp, func(o *oidc.Options) { o.Cache = unlisted })
	if _, _, _, err := r.p.Start(ctx(t)); !errors.Is(err, authz.ErrUnavailable) {
		t.Errorf("a private host that is not listed: %v, want authz.ErrUnavailable", err)
	}
	if d, _ := idp.Requests(); d != 0 {
		t.Errorf("%d discovery requests reached a private host that is not listed", d)
	}

	// Cached as keys are: one discovery for many starts.
	r = newRig(t)
	for i := 0; i < 5; i++ {
		if _, _, _, err := r.p.Start(ctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := r.idp.Requests(); d != 1 {
		t.Errorf("%d discovery requests for five starts, want 1", d)
	}
	r.idp.Stop()
	if _, _, _, err := r.p.Start(ctx(t)); err != nil {
		t.Errorf("a start with the document in the cache and the provider stopped: %v", err)
	}
}

func raw(t testing.TB, js string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestGroups is the first half of T-813.
func TestGroups(t *testing.T) {
	many := make([]string, 300)
	for i := range many {
		many[i] = "g" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	manyJSON, _ := json.Marshal(many)
	cases := []struct {
		name, claims, path string
		want               []string
	}{
		{"an array", `{"groups":["a","b"]}`, "groups", []string{"a", "b"}},
		{"one string", `{"groups":"admins"}`, "groups", []string{"admins"}},
		{"a dotted path", `{"realm_access":{"roles":["r1","r2"]}}`, "realm_access.roles", []string{"r1", "r2"}},
		{"a dotted path of four members", `{"a":{"b":{"c":{"d":["x"]}}}}`, "a.b.c.d", []string{"x"}},
		{"a dotted path of five members", `{"a":{"b":{"c":{"d":{"e":["x"]}}}}}`, "a.b.c.d.e", nil},
		{"a path through a string", `{"a":"x"}`, "a.b", nil},
		{"missing", `{"name":"x"}`, "groups", nil},
		{"null", `{"groups":null}`, "groups", nil},
		{"a number", `{"groups":3}`, "groups", nil},
		{"an object", `{"groups":{"a":"b"}}`, "groups", nil},
		{"a boolean", `{"groups":true}`, "groups", nil},
		{"an array with a number", `{"groups":["a",1]}`, "groups", nil},
		{"an array with an object", `{"groups":[{"a":1}]}`, "groups", nil},
		{"an empty array", `{"groups":[]}`, "groups", nil},
		{"an empty string", `{"groups":""}`, "groups", nil},
		{"a name that contains a dot, not a path", `{"a.b":["x"]}`, "a.b", nil},
		{"the claim name case matters", `{"Groups":["x"]}`, "groups", nil},
		{"a group with = and :", `{"groups":["eng=ops:team"]}`, "groups", []string{"eng=ops:team"}},
		{"300 entries: 256 are read", `{"groups":` + string(manyJSON) + `}`, "groups", many[:256]},
		{"an empty path", `{"groups":["a"]}`, "", nil},
	}
	for _, c := range cases {
		got := oidc.Groups(raw(t, c.claims), c.path)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, abbreviate(got), abbreviate(c.want))
		}
	}
}

func abbreviate(s []string) any {
	if len(s) > 5 {
		return len(s)
	}
	return s
}

// FuzzGroups is the fuzz target of T-813: no panic, never more than 256 groups, and a group is a
// string of the claim.
func FuzzGroups(f *testing.F) {
	for _, s := range []string{`{"groups":["a"]}`, `{"groups":"a"}`, `{"a":{"b":["c"]}}`, `{"groups":[1]}`, `[]`, `{"groups":{"groups":{"groups":{"groups":["x"]}}}}`} {
		f.Add(s, "groups")
		f.Add(s, "a.b")
	}
	f.Fuzz(func(t *testing.T, doc, path string) {
		var claims map[string]json.RawMessage
		if json.Unmarshal([]byte(doc), &claims) != nil {
			return
		}
		got := oidc.Groups(claims, path)
		if len(got) > 256 {
			t.Fatalf("%d groups", len(got))
		}
		// The oracle for a claim of one member: an array of strings, or one string, read as it is.
		var list []string
		var one string
		switch {
		case strings.Contains(path, ".") || path == "":
		case json.Unmarshal(claims[path], &list) == nil && list != nil:
			if len(list) > 256 {
				list = list[:256]
			}
			if !reflect.DeepEqual(got, list) && len(list) > 0 {
				t.Fatalf("groups %q, want %q", got, list)
			}
		case json.Unmarshal(claims[path], &one) == nil && one != "":
			if !reflect.DeepEqual(got, []string{one}) {
				t.Fatalf("groups %q, want %q", got, one)
			}
		}
	})
}

// TestRules is the second half of T-813: the syntax of the mappings and what they give.
func TestRules(t *testing.T) {
	rules, err := oidc.ParseRules([]string{
		"platform:platform-admin=cn=admins,ou=x",
		"tenant:acme:tenant-admin=eng=ops:team",
		"tenant:acme:tenant-admin=other",
		"tenant:globex:tenant-admin=eng=ops:team",
	})
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	want := []oidc.Rule{
		{Platform: true, Role: "platform-admin", Group: "cn=admins,ou=x"},
		{Tenant: "acme", Role: "tenant-admin", Group: "eng=ops:team"},
		{Tenant: "acme", Role: "tenant-admin", Group: "other"},
		{Tenant: "globex", Role: "tenant-admin", Group: "eng=ops:team"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules %+v", rules)
	}
	for _, bad := range []string{
		"", "platform-admin", "platform:platform-admin", "platform:platform-admin=", "platform:=x", "platform:a:b=x",
		"tenant:acme=x", "tenant:acme:=x", "tenant::tenant-admin=x", "tenant:acme:a:b=x", "other:platform-admin=x", "=x", ":=x",
	} {
		if _, err := oidc.ParseRules([]string{bad}); err == nil {
			t.Errorf("ParseRules(%q): accepted", bad)
		}
	}

	platform, tenants := oidc.Grants(rules, []string{"cn=admins,ou=x", "eng=ops:team", "unrelated"})
	if !reflect.DeepEqual(platform, []string{"platform-admin"}) {
		t.Errorf("platform roles %v", platform)
	}
	wantTenants := map[string][]string{"acme": {"tenant-admin"}, "globex": {"tenant-admin"}}
	if !reflect.DeepEqual(tenants, wantTenants) {
		t.Errorf("tenant roles %v", tenants)
	}
	// A role given twice by two groups is given once; the groups are compared exactly.
	platform, tenants = oidc.Grants(rules, []string{"eng=ops:team", "other", "ENG=OPS:TEAM", "eng=ops:team "})
	sort.Strings(platform)
	if len(platform) != 0 || !reflect.DeepEqual(tenants["acme"], []string{"tenant-admin"}) || len(tenants["globex"]) != 1 {
		t.Errorf("platform %v tenants %v", platform, tenants)
	}
	if p, tn := oidc.Grants(rules, nil); len(p) != 0 || len(tn) != 0 {
		t.Errorf("no groups: %v %v", p, tn)
	}
	if p, tn := oidc.Grants(nil, []string{"a"}); len(p) != 0 || len(tn) != 0 {
		t.Errorf("no rules: %v %v", p, tn)
	}
}
