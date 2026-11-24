// Package transit holds the conformance suite of the transit key provider (sprint 12.1): the
// functions that exercise vault.TransitProvider against a service through a Target. The suite runs
// in the ordinary tests against the double (fake_test.go) and, with the build tag transit, against
// OpenBao and HashiCorp Vault (real_test.go), so that the double is held to the behaviour of the
// real services.
package transit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/testutil"
)

// Target is a key service the suite can reach and administer.
type Target interface {
	// Address is the base URL of the service, for example http://127.0.0.1:8200.
	Address() string
	// Vendor is "openbao", "vault" or "double".
	Vendor() string
	// Namespaces reports whether the service implements namespaces.
	Namespaces() bool
	// Admin makes a request as an administrator: the path is relative to /v1/ and may start with a
	// namespace. It returns the status and the whole decoded answer (the fields data and auth of the
	// service); data is nil when the answer has no JSON object.
	Admin(tb testing.TB, method, path string, body any) (status int, data map[string]any)
}

func randomName(prefix string) string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// admin calls Admin and fails the test unless the status is a success.
func admin(tb testing.TB, t Target, method, path string, body any) map[string]any {
	tb.Helper()
	status, data := t.Admin(tb, method, path, body)
	if status < 200 || status > 299 {
		tb.Fatalf("%s %s as the administrator: status %d: %v", method, path, status, data)
	}
	return data
}

// NewKey enables a transit engine at a new mount and creates an aes256-gcm96 key in it. A fresh
// mount per test keeps the tests apart.
func NewKey(tb testing.TB, t Target) (mount, key string) {
	tb.Helper()
	mount, key = randomName("pdk"), randomName("key")
	admin(tb, t, http.MethodPost, "sys/mounts/"+mount, map[string]any{"type": "transit"})
	admin(tb, t, http.MethodPost, mount+"/keys/"+key, map[string]any{"type": "aes256-gcm96"})
	return mount, key
}

// policyFile is the least-privilege policy of the repository, the file the operator loads.
func policyFile(tb testing.TB) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(tb), "deploy", "transit", "pandokos-policy.hcl"))
	if err != nil {
		tb.Fatalf("the policy file of the repository: %v", err)
	}
	return string(b)
}

// PolicyText returns the policy of the repository for a mount and a key: the file as it is, with
// the mount transit and the key pandokos replaced.
func PolicyText(tb testing.TB, mount, key string) string {
	tb.Helper()
	text := strings.ReplaceAll(policyFile(tb), "pandokos", key)
	return strings.ReplaceAll(text, "transit/", mount+"/")
}

// writePolicy loads the policy in the service under a new name and returns the name.
func writePolicy(tb testing.TB, t Target, mount, key string) string {
	tb.Helper()
	name := randomName("pdk-policy")
	admin(tb, t, http.MethodPut, "sys/policies/acl/"+name, map[string]any{"policy": PolicyText(tb, mount, key)})
	return name
}

// PolicyToken loads the policy file of the repository for the mount and the key and returns a token
// that has that policy and no other (not the default policy), valid for ttl ("" is the service's
// default) and renewable.
func PolicyToken(tb testing.TB, t Target, mount, key string, ttl string) string {
	tb.Helper()
	name := writePolicy(tb, t, mount, key)
	body := map[string]any{"policies": []string{name}, "no_default_policy": true, "renewable": true}
	if ttl != "" {
		body["ttl"] = ttl
	}
	data := admin(tb, t, http.MethodPost, "auth/token/create", body)
	auth, _ := data["auth"].(map[string]any)
	tok, _ := auth["client_token"].(string)
	if tok == "" {
		tb.Fatalf("no token in the answer: %v", data)
	}
	return tok
}

// AppRole enables AppRole at a new mount, makes a role with the policy file's policy for the mount
// and the key, and returns the mount, the role ID and a secret ID. The tokens of the role live ttl
// and at most maxTTL ("" is the service's default for either).
func AppRole(tb testing.TB, t Target, mount, key, ttl, maxTTL string) (approleMount, roleID, secretID string) {
	tb.Helper()
	approleMount = randomName("approle")
	policy := writePolicy(tb, t, mount, key)
	admin(tb, t, http.MethodPost, "sys/auth/"+approleMount, map[string]any{"type": "approle"})
	role := map[string]any{"token_policies": []string{policy}, "token_no_default_policy": true}
	if ttl != "" {
		role["token_ttl"] = ttl
	}
	if maxTTL != "" {
		role["token_max_ttl"] = maxTTL
	}
	admin(tb, t, http.MethodPost, "auth/"+approleMount+"/role/pandokos", role)
	id := admin(tb, t, http.MethodGet, "auth/"+approleMount+"/role/pandokos/role-id", nil)
	sec := admin(tb, t, http.MethodPost, "auth/"+approleMount+"/role/pandokos/secret-id", map[string]any{})
	idData, _ := id["data"].(map[string]any)
	secData, _ := sec["data"].(map[string]any)
	roleID, _ = idData["role_id"].(string)
	secretID, _ = secData["secret_id"].(string)
	if roleID == "" || secretID == "" {
		tb.Fatalf("no role ID or secret ID in %v %v", id, sec)
	}
	return approleMount, roleID, secretID
}

// Raw sends a request with a token and returns the status and the decoded answer.
func Raw(tb testing.TB, t Target, token, method, path string, body any) (int, map[string]any) {
	tb.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			tb.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(t.Address(), "/")+"/v1/"+strings.TrimLeft(path, "/"), rd)
	if err != nil {
		tb.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// Options are what Provider is given besides the service.
type Options struct {
	Mount, Key   string
	Namespace    string
	Method       string // "token" or "approle"
	Token        string
	AppRoleMount string
	RoleID       string
	SecretID     string
	Timeout      time.Duration
	Adjust       func(*vault.TransitOptions)
	TokenFile    string // set by Provider for a token
	RoleIDFile   string
	SecretIDFile string
}

func writeSecret(tb testing.TB, dir, name, value string) string {
	tb.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	return p
}

// Provider builds a vault.TransitProvider on the service through the transport of an egress guard,
// without starting it. The files of the secrets are written in the temporary directory of the test.
func Provider(tb testing.TB, t Target, o *Options) *vault.TransitProvider {
	tb.Helper()
	g, err := egress.New(egress.Options{ConnectTimeout: 3 * time.Second})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(g.CloseIdleConnections)
	u, err := url.Parse(t.Address())
	if err != nil {
		tb.Fatal(err)
	}
	dir := tb.TempDir()
	if o.Method == "" {
		o.Method = "token"
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	to := vault.TransitOptions{
		Transport: g.KeyService(nil), Address: u, Namespace: o.Namespace, Mount: o.Mount, KeyName: o.Key, AuthMethod: o.Method,
		RequestTimeout: o.Timeout, MaxConcurrent: 32, UserAgent: "pandokos/conformance",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	switch o.Method {
	case "token":
		o.TokenFile = writeSecret(tb, dir, "token", o.Token)
		to.TokenFile = o.TokenFile
	case "approle":
		o.RoleIDFile = writeSecret(tb, dir, "role_id", o.RoleID)
		o.SecretIDFile = writeSecret(tb, dir, "secret_id", o.SecretID)
		to.AppRoleMount, to.RoleIDFile, to.SecretIDFile = o.AppRoleMount, o.RoleIDFile, o.SecretIDFile
	}
	if o.Adjust != nil {
		o.Adjust(&to)
	}
	p, err := vault.NewTransitProvider(to)
	if err != nil {
		tb.Fatalf("NewTransitProvider: %v", err)
	}
	return p
}

// Started is Provider and Start.
func Started(tb testing.TB, t Target, o *Options) *vault.TransitProvider {
	tb.Helper()
	p := Provider(tb, t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		tb.Fatalf("Start: %v", err)
	}
	return p
}

func ctx(tb testing.TB) context.Context {
	tb.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	tb.Cleanup(cancel)
	return c
}

func dataKey(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)*5
	}
	return b
}

func class(err error) string {
	var te *vault.TransitError
	if errors.As(err, &te) {
		return te.Class
	}
	return ""
}

// RunConformance runs the suite of T-1090 and T-1091 against a target.
func RunConformance(t *testing.T, target Target) {
	aad := []byte("pandokos/dek/v1 conformance")
	newProvider := func(t *testing.T) (*vault.TransitProvider, string, string, string) {
		t.Helper()
		mount, key := NewKey(t, target)
		token := PolicyToken(t, target, mount, key, "")
		return Started(t, target, &Options{Mount: mount, Key: key, Token: token}), mount, key, token
	}

	t.Run("wrap and unwrap with the policy of the repository", func(t *testing.T) {
		p, _, _, _ := newProvider(t)
		dek := dataKey(1)
		version, wrapped, err := p.Wrap(ctx(t), dek, aad)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		if !strings.HasPrefix(version, "transit-") || len(wrapped) != 61 || wrapped[0] != 0x02 {
			t.Errorf("version %q, %d bytes starting with %#x", version, len(wrapped), wrapped[0])
		}
		back, err := p.Unwrap(ctx(t), version, wrapped, aad)
		if err != nil || !bytes.Equal(back, dek) {
			t.Fatalf("Unwrap: %v", err)
		}
	})

	t.Run("other associated data, none and a flipped bit are ErrUnwrap", func(t *testing.T) {
		p, _, _, _ := newProvider(t)
		version, wrapped, err := p.Wrap(ctx(t), dataKey(2), aad)
		if err != nil {
			t.Fatal(err)
		}
		flipped := append([]byte(nil), wrapped...)
		flipped[30] ^= 1
		for name, c := range map[string]struct {
			w []byte
			a []byte
		}{"other data": {wrapped, []byte("other")}, "none": {wrapped, nil}, "a flipped bit": {flipped, aad}} {
			if got, err := p.Unwrap(ctx(t), version, c.w, c.a); !errors.Is(err, vault.ErrUnwrap) || got != nil {
				t.Errorf("%s: %x, %v; want ErrUnwrap", name, got, err)
			}
		}
	})

	t.Run("the start check passes with its negative probe", func(t *testing.T) {
		p, _, _, _ := newProvider(t)
		version, err := vault.Probe(ctx(t), p)
		if err != nil || version != p.Info().Version {
			t.Errorf("Probe = %q, %v", version, err)
		}
	})

	t.Run("the version stays pinned after a rotation", func(t *testing.T) {
		p, mount, key, _ := newProvider(t)
		before := p.Info().Version
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/rotate", nil)
		version, wrapped, err := p.Wrap(ctx(t), dataKey(3), aad)
		if err != nil || version != before {
			t.Fatalf("Wrap after a rotation: %q, %v; want %q", version, err, before)
		}
		if _, err := p.Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Errorf("Unwrap: %v", err)
		}
	})

	t.Run("identifiers are stable across restarts and distinct per key and version", func(t *testing.T) {
		p, mount, key, token := newProvider(t)
		v1 := p.Info().Version
		again := Started(t, target, &Options{Mount: mount, Key: key, Token: token})
		if got := again.Info().Version; got != v1 {
			t.Errorf("after a restart the identifier is %s, was %s", got, v1)
		}
		other, _, _, _ := newProvider(t)
		if other.Info().Version == v1 {
			t.Errorf("two keys have the identifier %s", v1)
		}
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/rotate", nil)
		next := Started(t, target, &Options{Mount: mount, Key: key, Token: token})
		if v2 := next.Info().Version; v2 == v1 || !strings.HasSuffix(v2, "-v2") {
			t.Errorf("version 2 has the identifier %s, version 1 has %s", v2, v1)
		}
		if err := next.Available(ctx(t), v1); err != nil {
			t.Errorf("Available of the first version: %v", err)
		}
	})

	t.Run("min_decryption_version and trim make a version unknown", func(t *testing.T) {
		p, mount, key, token := newProvider(t)
		v1 := p.Info().Version
		version, wrapped, err := p.Wrap(ctx(t), dataKey(4), aad)
		if err != nil {
			t.Fatal(err)
		}
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/rotate", nil)
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/rotate", nil)
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/config", map[string]any{"min_decryption_version": 2})
		raised := Started(t, target, &Options{Mount: mount, Key: key, Token: token})
		if err := raised.Available(ctx(t), v1); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Available below min_decryption_version: %v", err)
		}
		if _, err := raised.Unwrap(ctx(t), version, wrapped, aad); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Unwrap below min_decryption_version: %v", err)
		}
		// Lowering it again repairs it, until the version is trimmed.
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/config", map[string]any{"min_decryption_version": 1})
		if _, err := Started(t, target, &Options{Mount: mount, Key: key, Token: token}).Unwrap(ctx(t), version, wrapped, aad); err != nil {
			t.Errorf("after lowering min_decryption_version: %v", err)
		}
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/config", map[string]any{"min_decryption_version": 2, "min_encryption_version": 2})
		admin(t, target, http.MethodPost, mount+"/keys/"+key+"/trim", map[string]any{"min_available_version": 2})
		trimmed := Started(t, target, &Options{Mount: mount, Key: key, Token: token})
		if err := trimmed.Available(ctx(t), v1); !errors.Is(err, vault.ErrKeyVersionUnknown) {
			t.Errorf("Available after a trim: %v", err)
		}
	})

	t.Run("a wrong key name is not found or denied and creates no key", func(t *testing.T) {
		mount, key := NewKey(t, target)
		token := PolicyToken(t, target, mount, key, "")
		p := Provider(t, target, &Options{Mount: mount, Key: "no-such-key", Token: token})
		err := p.Start(ctx(t))
		if c := class(err); c != "denied" && c != "not_found" {
			t.Errorf("Start with the name of a key that does not exist: %v, want denied or not_found", err)
		}
		// A token whose policy names the missing key: the service answers not found, and an encrypt
		// with update only is refused instead of creating the key.
		ghost := PolicyToken(t, target, mount, "ghost", "")
		if c := class(Provider(t, target, &Options{Mount: mount, Key: "ghost", Token: ghost}).Start(ctx(t))); c != "not_found" && c != "denied" {
			t.Errorf("Start with a policy for a missing key: class %q", c)
		}
		status, _ := Raw(t, target, ghost, http.MethodPost, mount+"/encrypt/ghost", map[string]any{"plaintext": "AAAA"})
		if status != http.StatusForbidden {
			t.Errorf("an encrypt on a missing key with the update capability only: status %d, want 403", status)
		}
		if status, _ := target.Admin(t, http.MethodGet, mount+"/keys/ghost", nil); status != http.StatusNotFound {
			t.Errorf("the key ghost exists after the refused encrypt: status %d", status)
		}
		if status, _ := target.Admin(t, http.MethodGet, mount+"/keys/no-such-key", nil); status != http.StatusNotFound {
			t.Errorf("the key no-such-key exists: status %d", status)
		}
	})

	t.Run("the token of the policy cannot rotate, configure, trim, delete, export, read or use another key", func(t *testing.T) {
		_, mount, key, token := newProvider(t)
		otherKey := randomName("other")
		admin(t, target, http.MethodPost, mount+"/keys/"+otherKey, map[string]any{"type": "aes256-gcm96"})
		for _, c := range []struct{ name, method, path string }{
			{"rotate", http.MethodPost, mount + "/keys/" + key + "/rotate"},
			{"configure", http.MethodPost, mount + "/keys/" + key + "/config"},
			{"trim", http.MethodPost, mount + "/keys/" + key + "/trim"},
			{"delete", http.MethodDelete, mount + "/keys/" + key},
			{"export", http.MethodGet, mount + "/export/encryption-key/" + key},
			{"backup", http.MethodGet, mount + "/backup/" + key},
			{"read another key", http.MethodGet, mount + "/keys/" + otherKey},
			{"encrypt with another key", http.MethodPost, mount + "/encrypt/" + otherKey},
			{"decrypt with another key", http.MethodPost, mount + "/decrypt/" + otherKey},
			{"list the keys", http.MethodGet, mount + "/keys"},
			{"create a key", http.MethodPost, mount + "/keys/" + randomName("new")},
		} {
			body := any(nil)
			if c.method == http.MethodPost {
				body = map[string]any{"plaintext": "AAAA", "ciphertext": "vault:v1:AAAA", "min_decryption_version": 1, "min_available_version": 1, "type": "aes256-gcm96"}
			}
			if status, out := Raw(t, target, token, c.method, c.path, body); status != http.StatusForbidden {
				t.Errorf("%s with the token of the policy: status %d (%v), want 403", c.name, status, out)
			}
		}
		// And the key is intact: nothing above changed it.
		if status, data := target.Admin(t, http.MethodGet, mount+"/keys/"+key, nil); status != 200 || data == nil {
			t.Errorf("the key after the refused requests: %d", status)
		}
	})
}
