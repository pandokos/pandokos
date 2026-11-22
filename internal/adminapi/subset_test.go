package adminapi_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestSubsetAdditions is the generated half of T-822: what the four constructs that sprint 4.2 adds
// to the subset generate (section 5.12).
func TestSubsetAdditions(t *testing.T) {
	public := map[string]bool{"getAuthMethods": true, "signIn": true, "startOidcSignIn": true, "completeOidcSignIn": true}
	created := 0
	for _, op := range adminapi.Operations {
		// x-pandokos-public stands in place of the permission, exactly one of them.
		if op.Public != public[op.ID] || op.Public == (op.Permission != "") {
			t.Errorf("%s: Public=%v Permission=%q", op.ID, op.Public, op.Permission)
		}
		// The header parameter is allowed only on the operations that answer 201, and they all declare it.
		if op.Idempotent != (op.Status == 201) {
			t.Errorf("%s: Idempotent=%v and status %d", op.ID, op.Idempotent, op.Status)
		}
		if op.Idempotent {
			created++
		}
		// A 303 is the only response of a public GET.
		if (op.Status == 303) != (op.ID == "startOidcSignIn" || op.ID == "completeOidcSignIn") {
			t.Errorf("%s: status %d", op.ID, op.Status)
		}
		if op.Status == 303 && (!op.Public || op.Method != "GET" || op.HasBody) {
			t.Errorf("%s: a redirect that is not a public GET without a body", op.ID)
		}
	}
	if created < 15 {
		t.Errorf("%d operations take the Idempotency-Key header", created)
	}

	r := newRig(t, 1<<20)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := noRedirect.Get(r.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	// A 303 with a Location and no body.
	resp, body := get("/v1/auth/oidc/start")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/somewhere" || body != "" {
		t.Errorf("start: %d %q %q", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	// The client returns the Location and does not follow it.
	loc, err := r.client().StartOidcSignIn(context.Background(), adminapi.StartOidcSignInInput{})
	if err != nil || loc.Location != "/somewhere" {
		t.Errorf("the client: %+v %v", loc, err)
	}

	// x-pandokos-lenient-query: unknown parameters are ignored on the callback, a repeated declared one is refused.
	resp, _ = get("/v1/auth/oidc/callback?code=c&state=s&session_state=x&unknown=1")
	if resp.StatusCode != 303 {
		t.Errorf("callback with parameters of the provider's own: %d", resp.StatusCode)
	}
	in, _ := r.f.last("completeOidcSignIn").(adminapi.CompleteOidcSignInInput)
	if in.Code != "c" || in.State != "s" {
		t.Errorf("input %+v", in)
	}
	if resp, _ = get("/v1/auth/oidc/callback?code=c&code=d"); resp.StatusCode != 400 {
		t.Errorf("a repeated declared parameter on the callback: %d", resp.StatusCode)
	}
	// Everywhere else an unknown parameter is still refused.
	for _, path := range []string{"/v1/auth/methods?unknown=1", "/v1/auth/oidc/start?unknown=1", "/v1/tenants?unknown=1", "/v1/users?unknown=1"} {
		if resp, _ = get(path); resp.StatusCode != 400 {
			t.Errorf("%s: %d, want 400", path, resp.StatusCode)
		}
	}

	// The header parameter reaches the input; the router does not validate it (the hook does).
	req, _ := http.NewRequest("POST", r.srv.URL+"/v1/users", strings.NewReader(`{"sign_in":"local","username":"ada.lovelace"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k-1")
	if resp, err := noRedirect.Do(req); err != nil || resp.StatusCode != 200 && resp.StatusCode != 201 {
		t.Errorf("a create with the header: %v %v", err, resp)
	}
	if got, _ := r.f.last("createUser").(adminapi.CreateUserInput); got.IdempotencyKey != "k-1" {
		t.Errorf("input %+v: the header did not reach the input", got)
	}
	if _, err := r.client().CreateUser(context.Background(), adminapi.CreateUserInput{IdempotencyKey: "from-the-client", Body: adminapi.UserCreate{SignIn: "local", Username: "grace.hopper"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.f.last("createUser").(adminapi.CreateUserInput); got.IdempotencyKey != "from-the-client" {
		t.Errorf("the client did not send the header: %+v", got)
	}
	// A key that is not 1 to 128 printable ASCII characters is not the router's business.
	req, _ = http.NewRequest("POST", r.srv.URL+"/v1/users", strings.NewReader(`{"sign_in":"local","username":"ada.lovelace"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "has space")
	if resp, err := noRedirect.Do(req); err != nil || resp.StatusCode == 500 {
		t.Errorf("a malformed key: %v %v", err, resp)
	}

	// A request member named password is a secret as secret is: no message of the router repeats it.
	for _, body := range []string{`{"username":"a","password":"` + canaryPW + `","extra":1}`, `{"username":"a","password":3}`, `{"username":"a","password":"` + canaryPW + ``} {
		req, _ := http.NewRequest("POST", r.srv.URL+"/v1/auth/sign-in", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), canaryPW) {
			t.Errorf("a problem repeats the password: %s", b)
		}
	}
	for _, body := range []string{`{"current_password":"` + canaryPW + `","new_password":"` + canaryPW + `2","x":1}`} {
		req, _ := http.NewRequest("POST", r.srv.URL+"/v1/auth/password", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), canaryPW) {
			t.Errorf("a problem repeats a password: %s", b)
		}
	}
}

const canaryPW = "canary-password-3a9f0c7d1b"
