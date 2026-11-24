//go:build transit

package transit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/vault"
)

// realTarget is a real OpenBao or HashiCorp Vault service in development mode, administered with
// the root token the test environment made. A test that has not got its environment fails.
type realTarget struct {
	address, vendor, root string
}

func newRealTarget(t *testing.T) *realTarget {
	t.Helper()
	r := &realTarget{address: os.Getenv("PANDOKOS_TEST_TRANSIT_ADDRESS"), vendor: os.Getenv("PANDOKOS_TEST_TRANSIT_VENDOR")}
	file := os.Getenv("PANDOKOS_TEST_TRANSIT_ROOT_TOKEN_FILE")
	if r.address == "" || r.vendor == "" || file == "" {
		t.Fatal("PANDOKOS_TEST_TRANSIT_ADDRESS, PANDOKOS_TEST_TRANSIT_VENDOR and PANDOKOS_TEST_TRANSIT_ROOT_TOKEN_FILE must be set (make transit-up VENDOR=openbao, then make test-transit)")
	}
	if r.vendor != "openbao" && r.vendor != "vault" {
		t.Fatalf("PANDOKOS_TEST_TRANSIT_VENDOR is %q, want openbao or vault", r.vendor)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the root token file: %v", err)
	}
	r.root = strings.TrimSpace(string(b))
	return r
}

func (r *realTarget) Address() string  { return r.address }
func (r *realTarget) Vendor() string   { return r.vendor }
func (r *realTarget) Namespaces() bool { return r.vendor == "openbao" }

func (r *realTarget) Admin(tb testing.TB, method, path string, body any) (int, map[string]any) {
	tb.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			tb.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(r.address, "/")+"/v1/"+strings.TrimLeft(path, "/"), rd)
	if err != nil {
		tb.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", r.root)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestConformance is T-1090: the suite against the real service, with the policy file of the
// repository loaded as it is.
func TestConformance(t *testing.T) {
	RunConformance(t, newRealTarget(t))
}

// soak makes wraps and unwraps for d and fails on the first error.
func soak(t *testing.T, p *vault.TransitProvider, d time.Duration) {
	t.Helper()
	aad := []byte("pandokos/dek/v1 soak")
	deadline := time.Now().Add(d)
	n := 0
	for time.Now().Before(deadline) {
		dek := dataKey(byte(n))
		version, wrapped, err := p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatalf("Wrap after %d rounds: %v", n, err)
		}
		back, err := p.Unwrap(ctx(t), version, wrapped, aad)
		if err != nil || !bytes.Equal(back, dek) {
			t.Fatalf("Unwrap after %d rounds: %v", n, err)
		}
		n++
		time.Sleep(100 * time.Millisecond)
	}
}

// runProvider runs the lifecycle of the token of p until the test ends.
func runProvider(t *testing.T, p *vault.TransitProvider) {
	t.Helper()
	c, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(c) }()
	t.Cleanup(func() { stop(); <-done })
}

// TestAuthMethods is T-1092: tokens that live seconds, on a real service.
func TestAuthMethods(t *testing.T) {
	target := newRealTarget(t)

	t.Run("a renewable token of 4 s: 20 s of wraps and unwraps", func(t *testing.T) {
		mount, key := NewKey(t, target)
		p := Started(t, target, &Options{Mount: mount, Key: key, Token: PolicyToken(t, target, mount, key, "4s")})
		runProvider(t, p)
		soak(t, p, 20*time.Second)
		if st := p.Stats(); !st.TokenExpires || st.Requests[vault.TransitCount{Operation: "renew", Outcome: "ok"}] < 2 {
			t.Errorf("the token was renewed %d times in 20 s of a token of 4 s", st.Requests[vault.TransitCount{Operation: "renew", Outcome: "ok"}])
		}
	})

	t.Run("AppRole tokens of 4 s with a maximum of 8 s: 20 s of wraps and unwraps", func(t *testing.T) {
		mount, key := NewKey(t, target)
		am, roleID, secretID := AppRole(t, target, mount, key, "4s", "8s")
		p := Started(t, target, &Options{Mount: mount, Key: key, Method: "approle", AppRoleMount: am, RoleID: roleID, SecretID: secretID})
		runProvider(t, p)
		soak(t, p, 20*time.Second)
		if st := p.Stats(); st.Requests[vault.TransitCount{Operation: "login", Outcome: "ok"}] < 2 {
			t.Errorf("%d logins in 20 s of tokens that live at most 8 s", st.Requests[vault.TransitCount{Operation: "login", Outcome: "ok"}])
		}
	})

	t.Run("a wrong secret ID is refused", func(t *testing.T) {
		mount, key := NewKey(t, target)
		am, roleID, _ := AppRole(t, target, mount, key, "", "")
		err := Provider(t, target, &Options{Mount: mount, Key: key, Method: "approle", AppRoleMount: am, RoleID: roleID, SecretID: "not-the-secret-id"}).Start(ctx(t))
		var te *vault.TransitError
		if !errors.As(err, &te) || te.Operation != "login" || (te.Class != "denied" && te.Class != "rejected") {
			t.Errorf("Start with a wrong secret ID: %v, want a refused login (denied; rejected when the service answers 400)", err)
		}
	})

	t.Run("a token that expired is denied and a replaced token file is adopted", func(t *testing.T) {
		mount, key := NewKey(t, target)
		policy := writePolicy(t, target, mount, key)
		data := admin(t, target, http.MethodPost, "auth/token/create", map[string]any{"policies": []string{policy}, "no_default_policy": true, "ttl": "2s", "renewable": false})
		first, _ := data["auth"].(map[string]any)["client_token"].(string)
		o := &Options{Mount: mount, Key: key, Token: first}
		p := Started(t, target, o)
		time.Sleep(3 * time.Second) // the token is gone and nobody renews it
		aad := []byte("pandokos/dek/v1 expired")
		if _, _, err := p.Wrap(ctx(t), dataKey(1), aad); class(err) != "denied" {
			t.Errorf("Wrap with a token that expired: %v, want denied", err)
		}
		if err := os.WriteFile(o.TokenFile, []byte(PolicyToken(t, target, mount, key, "")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.Wrap(ctx(t), dataKey(2), aad); err != nil {
			t.Errorf("Wrap after the token file was replaced: %v", err)
		}
	})
}

// inNamespace is a Target whose administration happens inside a namespace.
type inNamespace struct {
	Target
	ns string
}

func (n inNamespace) Admin(tb testing.TB, method, path string, body any) (int, map[string]any) {
	tb.Helper()
	return n.Target.Admin(tb, method, n.ns+"/"+strings.TrimLeft(path, "/"), body)
}

// TestNamespace is T-1093: a key in a namespace is used with the namespace key and not found without it.
func TestNamespace(t *testing.T) {
	target := newRealTarget(t)
	if !target.Namespaces() {
		t.Skip("namespaces are an Enterprise feature of HashiCorp Vault; the header is covered by T-1066")
	}
	ns := randomName("ns")
	admin(t, target, http.MethodPost, "sys/namespaces/"+ns, map[string]any{})
	scoped := inNamespace{Target: target, ns: ns}
	mount, key := NewKey(t, scoped)
	token := PolicyToken(t, scoped, mount, key, "")

	p := Started(t, target, &Options{Mount: mount, Key: key, Namespace: ns, Token: token})
	aad := []byte("pandokos/dek/v1 namespace")
	version, wrapped, err := p.Wrap(ctx(t), dataKey(1), aad)
	if err != nil {
		t.Fatalf("Wrap with the namespace: %v", err)
	}
	if _, err := p.Unwrap(ctx(t), version, wrapped, aad); err != nil {
		t.Errorf("Unwrap with the namespace: %v", err)
	}
	err = Provider(t, target, &Options{Mount: mount, Key: key, Token: token}).Start(ctx(t))
	if c := class(err); c != "not_found" && c != "denied" {
		t.Errorf("Start without the namespace: %v, want not_found or denied", err)
	}
}
