package transit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/vault/transittest"
)

// doubleTarget is a Target on the double of the key service: the administrative paths of the
// suite are mapped onto the administration functions of the double.
type doubleTarget struct {
	s *transittest.Server

	mu         sync.Mutex
	namespaces map[string]bool
	policies   map[string][]transittest.Path
	roles      map[string][2]string // approle mount and role: role ID and secret ID
	roots      map[string]string    // a root token per namespace, for the requests that are forwarded
}

func newDoubleTarget(t *testing.T) *doubleTarget {
	t.Helper()
	return &doubleTarget{s: transittest.New(t), namespaces: map[string]bool{}, policies: map[string][]transittest.Path{},
		roles: map[string][2]string{}, roots: map[string]string{}}
}

func (d *doubleTarget) Address() string  { return d.s.URL() }
func (d *doubleTarget) Vendor() string   { return "double" }
func (d *doubleTarget) Namespaces() bool { return true }
func (d *doubleTarget) scopeOf(path string) (ns, rest string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for n := range d.namespaces {
		if strings.HasPrefix(path, n+"/") && len(n) > len(ns) {
			ns, rest = n, strings.TrimPrefix(path, n+"/")
		}
	}
	if ns == "" {
		return "", path
	}
	return ns, rest
}

var rePolicyPath = regexp.MustCompile(`path\s+"([^"]+)"\s*\{\s*capabilities\s*=\s*\[\s*"([a-z]+)"`)

// parsePolicy reads the stanzas of a policy of the form of the repository's file.
func parsePolicy(text string) []transittest.Path {
	var out []transittest.Path
	for _, m := range rePolicyPath.FindAllStringSubmatch(text, -1) {
		out = append(out, transittest.Path{Path: m[1], Capability: m[2]})
	}
	return out
}

func asDuration(v any) time.Duration {
	s, _ := v.(string)
	d, _ := time.ParseDuration(s)
	return d
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func (d *doubleTarget) Admin(tb testing.TB, method, path string, body any) (int, map[string]any) {
	tb.Helper()
	in, _ := body.(map[string]any)
	ns, p := d.scopeOf(strings.Trim(path, "/"))
	sc := d.s.Namespace(ns)
	ok := func(data map[string]any) (int, map[string]any) { return 200, data }
	switch {
	case method == http.MethodPost && strings.HasPrefix(p, "sys/namespaces/"):
		name := strings.TrimPrefix(p, "sys/namespaces/")
		if ns != "" {
			name = ns + "/" + name
		}
		d.mu.Lock()
		d.namespaces[name] = true
		d.mu.Unlock()
		d.s.Namespace(name)
		return ok(nil)
	case method == http.MethodPost && strings.HasPrefix(p, "sys/mounts/"):
		sc.Mount(strings.TrimPrefix(p, "sys/mounts/"))
		return ok(nil)
	case method == http.MethodPost && strings.HasPrefix(p, "sys/auth/"):
		return ok(nil)
	case method == http.MethodPut && strings.HasPrefix(p, "sys/policies/acl/"):
		text, _ := in["policy"].(string)
		d.mu.Lock()
		d.policies[ns+"/"+strings.TrimPrefix(p, "sys/policies/acl/")] = parsePolicy(text)
		d.mu.Unlock()
		return ok(nil)
	case method == http.MethodPost && p == "auth/token/create":
		var paths []transittest.Path
		names, _ := in["policies"].([]string)
		d.mu.Lock()
		for _, n := range names {
			paths = append(paths, d.policies[ns+"/"+n]...)
		}
		d.mu.Unlock()
		renewable, _ := in["renewable"].(bool)
		tok := sc.Token(transittest.TokenOptions{TTL: asDuration(in["ttl"]), Renewable: renewable, Paths: paths})
		return ok(map[string]any{"auth": map[string]any{"client_token": tok}})
	case method == http.MethodPost && strings.HasPrefix(p, "auth/") && strings.Contains(p, "/role/") && !strings.HasSuffix(p, "/secret-id"):
		mount, role, _ := strings.Cut(strings.TrimPrefix(p, "auth/"), "/role/")
		var paths []transittest.Path
		names, _ := in["token_policies"].([]string)
		d.mu.Lock()
		for _, n := range names {
			paths = append(paths, d.policies[ns+"/"+n]...)
		}
		d.mu.Unlock()
		id, secret := sc.AppRole(mount, transittest.RoleOptions{TokenTTL: asDuration(in["token_ttl"]), TokenMaxTTL: asDuration(in["token_max_ttl"]), Paths: paths})
		d.mu.Lock()
		d.roles[ns+"/"+mount+"/"+role] = [2]string{id, secret}
		d.mu.Unlock()
		return ok(nil)
	case method == http.MethodGet && strings.HasSuffix(p, "/role-id"):
		mount, role, _ := strings.Cut(strings.TrimPrefix(strings.TrimSuffix(p, "/role-id"), "auth/"), "/role/")
		d.mu.Lock()
		r := d.roles[ns+"/"+mount+"/"+role]
		d.mu.Unlock()
		return ok(map[string]any{"data": map[string]any{"role_id": r[0]}})
	case method == http.MethodPost && strings.HasSuffix(p, "/secret-id"):
		mount, role, _ := strings.Cut(strings.TrimPrefix(strings.TrimSuffix(p, "/secret-id"), "auth/"), "/role/")
		d.mu.Lock()
		r := d.roles[ns+"/"+mount+"/"+role]
		d.mu.Unlock()
		return ok(map[string]any{"data": map[string]any{"secret_id": r[1]}})
	}
	if mount, rest, found := strings.Cut(p, "/keys/"); found && method == http.MethodPost {
		name, op, _ := strings.Cut(rest, "/")
		switch op {
		case "":
			typ, _ := in["type"].(string)
			sc.CreateKey(mount, name, transittest.KeyOptions{Type: typ})
			return ok(nil)
		case "rotate":
			sc.Rotate(mount, name)
			return ok(nil)
		case "config":
			sc.Configure(mount, name, asInt(in["min_decryption_version"]), asInt(in["min_encryption_version"]))
			return ok(nil)
		case "trim":
			sc.Trim(mount, name, asInt(in["min_available_version"]))
			return ok(nil)
		}
	}
	// Anything else is a request to the double with the root token of the namespace.
	d.mu.Lock()
	root, have := d.roots[ns]
	d.mu.Unlock()
	if !have {
		root = sc.Token(transittest.TokenOptions{Root: true})
		d.mu.Lock()
		d.roots[ns] = root
		d.mu.Unlock()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, d.s.URL()+"/v1/"+p, rd)
	if err != nil {
		tb.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", root)
	req.Header.Set("X-Vault-Namespace", ns)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tb.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestConformanceDouble is T-1091: the suite of T-1090 against the double, in the ordinary suite,
// so that the double is held to what the real services do.
func TestConformanceDouble(t *testing.T) {
	RunConformance(t, newDoubleTarget(t))
}
