package oidc_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
)

var flowPeople = map[string]fakeidp.Claims{
	"ada": {"name": "Ada Lovelace", "email": "ada@example.test", "groups": []string{"admins"}},
}

type admRig struct {
	api *apitest.API
	o   *apitest.OIDC
	ada dbtest.Person
}

func newAdmRig(t *testing.T) *admRig {
	t.Helper()
	api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{}, apitest.OIDCConfig{Users: flowPeople})
	o.IDP.SignInAs("ada")
	return &admRig{api: api, o: o, ada: api.DB.FederatedUser(t, o.IDP.Issuer(), "ada", []string{"platform-admin"})}
}

func (r *admRig) sessions(t *testing.T) int {
	return r.api.DB.Count(t, "SELECT count(*) FROM pandokos.user_session")
}

// refusedAt checks that a callback ended as a refusal: the failure redirect, the seed cookie cleared,
// no session, and no code redeemed.
func (r *admRig) refused(t *testing.T, what string, hops []apitest.Hop, b *apitest.Browser, tokenRequests int) {
	t.Helper()
	end := hops[len(hops)-1]
	u, err := url.Parse(end.Location)
	if end.Status != 303 || err != nil || u.Path != "/console" || u.Query().Get("sign_in_error") != "sign_in_failed" {
		t.Errorf("%s: the callback ended with %d %q, want the failure redirect", what, end.Status, end.Location)
	}
	cleared := false
	for _, c := range end.Header.Values("Set-Cookie") {
		cleared = cleared || strings.HasPrefix(c, oidc.SeedCookieName+"=;")
		if strings.HasPrefix(c, session.CookieName+"=") {
			t.Errorf("%s: a session cookie was set", what)
		}
	}
	if !cleared {
		t.Errorf("%s: the seed cookie was not cleared: %q", what, end.Header.Values("Set-Cookie"))
	}
	if n := r.sessions(t); n != 0 {
		t.Errorf("%s: %d sessions", what, n)
	}
	if got := r.o.IDP.TokenRequests(); got != tokenRequests {
		t.Errorf("%s: the provider received %d token requests, want %d", what, got, tokenRequests)
	}
}

// TestCallbackState is T-808.
func TestCallbackState(t *testing.T) {
	t.Run("no cookie", func(t *testing.T) {
		r := newAdmRig(t)
		start := r.o.Start(t, r.api.NewBrowser())
		q := r.o.Authorize(t, start.Location)
		stranger := r.api.NewBrowser() // the provider's redirect reaches a browser that did not start the flow
		r.refused(t, "no cookie", r.o.Callback(t, stranger, q), stranger, 0)
	})
	t.Run("another flow's cookie", func(t *testing.T) {
		r := newAdmRig(t)
		a, b := r.api.NewBrowser(), r.api.NewBrowser()
		startA := r.o.Start(t, a)
		r.o.Start(t, b)
		q := r.o.Authorize(t, startA.Location)
		r.refused(t, "another flow's cookie", r.o.Callback(t, b, q), b, 0)
	})
	t.Run("a state used twice", func(t *testing.T) {
		r := newAdmRig(t)
		b := r.api.NewBrowser()
		start := r.o.Start(t, b)
		seed := b.Cookie(oidc.SeedCookieName)
		q := r.o.Authorize(t, start.Location)
		hops := r.o.Callback(t, b, q)
		if end := hops[len(hops)-1]; end.Location != "/console" || b.Token() == "" {
			t.Fatalf("the first callback: %d %q", end.Status, end.Location)
		}
		r.api.DB.Super(t).Exec(ctx(t), "DELETE FROM pandokos.user_session") //nolint:errcheck // the test counts sessions of the replay
		b.SetCookie(oidc.SeedCookieName, seed)
		r.refused(t, "a state used twice", r.o.Callback(t, b, q), b, 1)
	})
	t.Run("a state older than ten minutes", func(t *testing.T) {
		r := newAdmRig(t)
		b := r.api.NewBrowser()
		start := r.o.Start(t, b)
		q := r.o.Authorize(t, start.Location)
		r.api.DB.Age(t, "oidc_login", "", 11*time.Minute, "created_at", "expires_at")
		r.refused(t, "an old state", r.o.Callback(t, b, q), b, 0)
	})
	t.Run("an error parameter", func(t *testing.T) {
		r := newAdmRig(t)
		b := r.api.NewBrowser()
		start := r.o.Start(t, b)
		denied := "access_denied"
		r.o.IDP.CallbackError(&denied)
		q := r.o.Authorize(t, start.Location)
		r.refused(t, "an error parameter", r.o.Callback(t, b, q), b, 0)
	})
	t.Run("an iss parameter that differs", func(t *testing.T) {
		r := newAdmRig(t)
		b := r.api.NewBrowser()
		start := r.o.Start(t, b)
		iss := "https://other.example.test"
		r.o.IDP.CallbackIss(&iss)
		q := r.o.Authorize(t, start.Location)
		r.refused(t, "another iss", r.o.Callback(t, b, q), b, 0)
	})
	t.Run("the reasons are audited without a subject", func(t *testing.T) {
		r := newAdmRig(t)
		b := r.api.NewBrowser()
		start := r.o.Start(t, b)
		q := r.o.Authorize(t, start.Location)
		r.o.Callback(t, r.api.NewBrowser(), q) // a callback without the cookie
		var reasons []string
		for _, row := range r.api.DB.FailureRows(t, [16]byte{}) {
			reasons = append(reasons, fmt.Sprint(row.Detail["reason"]))
			if row.Detail["method"] != "oidc" || row.ResourceID != "" {
				t.Errorf("failure row %+v", row)
			}
		}
		if len(reasons) != 1 || reasons[0] != "state_mismatch" {
			t.Errorf("reasons %v, want [state_mismatch]", reasons)
		}
	})
}

// TestTokensAreDropped is T-812: after a sign-in none of the secrets of the exchange is kept anywhere.
func TestTokensAreDropped(t *testing.T) {
	r := newAdmRig(t)
	b := r.api.NewBrowser()
	hops := r.o.SignIn(t, b)
	if end := hops[len(hops)-1]; end.Location != "/console" {
		t.Fatalf("the sign-in ended with %d %q", end.Status, end.Location)
	}
	planted := map[string]string{
		"the code": r.o.IDP.LastCode(), "the client secret": r.o.IDP.ClientSecret(), "the ID token": r.o.IDP.LastIDToken(),
		"the access token": r.o.IDP.AccessToken(), "the refresh token": r.o.IDP.RefreshToken(),
	}
	for name, v := range planted {
		if v == "" {
			t.Fatalf("%s was not issued", name)
		}
	}
	var out strings.Builder
	out.WriteString(r.api.Logs.String())
	out.WriteString(r.api.Snapshot(t)) // every table of the database
	out.WriteString(r.api.DB.AllAuditText(t))
	for _, h := range hops {
		fmt.Fprintf(&out, "%s %v %s\n", h.Location, h.Header, h.Body)
	}
	// A request that uses the new session shows nothing of the provider either.
	me := b.Do(t, "GET", "/v1/me", nil, nil)
	fmt.Fprintf(&out, "%v %s", me.Header, me.Body)
	for name, v := range planted {
		// The code travels in the redirect of the provider to the callback, which is part of the query that
		// the test sent; only what the handler wrote is searched, so the hops' own Location is not.
		text := out.String()
		if name == "the code" {
			text = strings.ReplaceAll(text, "code="+v, "")
		}
		if strings.Contains(text, v) {
			t.Errorf("%s is in the logs, the database, an audit row or a response", name)
		}
	}
}
