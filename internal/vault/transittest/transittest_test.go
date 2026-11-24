package transittest

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var b64 = base64.StdEncoding.EncodeToString

// call sends a request to the double and returns the status and the decoded body.
func call(t *testing.T, c *http.Client, s *Server, method, path, token, ns string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.URL()+"/v1/"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if ns != "" {
		req.Header.Set("X-Vault-Namespace", ns)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func data(m map[string]any) map[string]any {
	d, _ := m["data"].(map[string]any)
	return d
}

func errorsOf(m map[string]any) []any {
	e, _ := m["errors"].([]any)
	return e
}

type rig struct {
	t     *testing.T
	s     *Server
	c     *http.Client
	root  string
	token string // least privilege for transit/pandokos
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s := New(t)
	s.Namespace("").CreateKey("transit", "pandokos", KeyOptions{})
	r := &rig{t: t, s: s, c: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	r.root = s.Namespace("").Token(TokenOptions{Root: true})
	r.token = s.Namespace("").Token(TokenOptions{})
	return r
}

func (r *rig) post(path, token string, body any) (int, map[string]any) {
	r.t.Helper()
	return call(r.t, r.c, r.s, http.MethodPost, path, token, "", body)
}

func (r *rig) get(path, token string) (int, map[string]any) {
	r.t.Helper()
	return call(r.t, r.c, r.s, http.MethodGet, path, token, "", nil)
}

func (r *rig) encrypt(pt []byte, aad string) string {
	r.t.Helper()
	b := map[string]any{"plaintext": b64(pt)}
	if aad != "" {
		b["associated_data"] = b64([]byte(aad))
	}
	code, m := r.post("transit/encrypt/pandokos", r.token, b)
	if code != 200 {
		r.t.Fatalf("encrypt: %d %v", code, m)
	}
	return data(m)["ciphertext"].(string)
}

func (r *rig) decrypt(ct, aad string) (int, map[string]any) {
	r.t.Helper()
	b := map[string]any{"ciphertext": ct}
	if aad != "" {
		b["associated_data"] = b64([]byte(aad))
	}
	return r.post("transit/decrypt/pandokos", r.token, b)
}

func TestAssociatedDataIsBound(t *testing.T) {
	r := newRig(t)
	pt := bytes.Repeat([]byte{7}, 32)
	ct := r.encrypt(pt, "tenant-a")
	if len(ct) != 89 || !strings.HasPrefix(ct, "vault:v1:") {
		t.Fatalf("ciphertext of 32 bytes is %d characters, %q", len(ct), ct[:9])
	}
	code, m := r.decrypt(ct, "tenant-a")
	if code != 200 || data(m)["plaintext"] != b64(pt) {
		t.Fatalf("decrypt = %d %v", code, m)
	}
	for name, aad := range map[string]string{"other associated data": "tenant-b", "none": ""} {
		if code, _ := r.decrypt(ct, aad); code != 400 {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	raw, _ := base64.StdEncoding.DecodeString(ct[9:])
	raw[20] ^= 1
	if code, _ := r.decrypt("vault:v1:"+b64(raw), "tenant-a"); code != 400 {
		t.Errorf("changed ciphertext: status %d, want 400", code)
	}
	plain := r.encrypt(pt, "")
	if code, _ := r.decrypt(plain, "tenant-a"); code != 400 {
		t.Errorf("ciphertext encrypted without the field: status %d, want 400", code)
	}
	r.s.Namespace("").Rotate("transit", "pandokos")
	if code, _ := r.decrypt("vault:v2:"+ct[9:], "tenant-a"); code != 400 {
		t.Errorf("version 1 relabelled v2: status %d, want 400", code)
	}
	t.Run("ignored by the fault", func(t *testing.T) {
		r.s.IgnoreAssociatedData()
		defer r.s.Clear()
		ct := r.encrypt(pt, "tenant-a")
		if code, _ := r.decrypt(ct, "tenant-b"); code != 200 {
			t.Errorf("status %d, want 200 with the field ignored", code)
		}
	})
}

func TestKeyTypes(t *testing.T) {
	r := newRig(t)
	sc := r.s.Namespace("")
	sc.CreateKey("transit", "rsa", KeyOptions{Type: "rsa-2048"})
	sc.CreateKey("transit", "chacha", KeyOptions{Type: "chacha20-poly1305"})
	sc.CreateKey("transit", "derived", KeyOptions{Derived: true})
	aad := map[string]any{"plaintext": b64([]byte("x")), "associated_data": b64([]byte("a"))}
	if code, _ := r.post("transit/encrypt/rsa", r.root, aad); code != 400 {
		t.Errorf("rsa-2048 with associated_data: %d, want 400", code)
	}
	if code, _ := r.post("transit/encrypt/chacha", r.root, aad); code != 200 {
		t.Errorf("chacha20-poly1305 with associated_data: %d, want 200", code)
	}
	if code, _ := r.post("transit/encrypt/derived", r.root, aad); code != 400 {
		t.Errorf("derived without context: %d, want 400", code)
	}
	aad["context"] = b64([]byte("c"))
	code, m := r.post("transit/encrypt/derived", r.root, aad)
	if code != 200 {
		t.Fatalf("derived with context: %d", code)
	}
	if code, _ := r.post("transit/decrypt/derived", r.root, map[string]any{"ciphertext": data(m)["ciphertext"], "associated_data": aad["associated_data"], "context": aad["context"]}); code != 200 {
		t.Errorf("derived decrypt: %d", code)
	}
}

func TestKeyVersions(t *testing.T) {
	r := newRig(t)
	sc := r.s.Namespace("")
	pt := b64([]byte("p"))
	sc.Rotate("transit", "pandokos")
	sc.Rotate("transit", "pandokos")
	enc := func(v int) (int, map[string]any) {
		return r.post("transit/encrypt/pandokos", r.token, map[string]any{"plaintext": pt, "key_version": v})
	}
	code, m := enc(2)
	if code != 200 || data(m)["key_version"].(float64) != 2 || !strings.HasPrefix(data(m)["ciphertext"].(string), "vault:v2:") {
		t.Fatalf("key_version 2: %d %v", code, m)
	}
	if code, _ := enc(4); code != 400 {
		t.Errorf("above the latest: %d, want 400", code)
	}
	code, m = r.post("transit/encrypt/pandokos", r.token, map[string]any{"plaintext": pt, "key_version": 1})
	ct1 := data(mustOK(t, code, m))["ciphertext"].(string)
	sc.Configure("transit", "pandokos", 2, 2)
	if code, _ := enc(1); code != 400 {
		t.Errorf("below min_decryption_version: %d, want 400", code)
	}
	if code, m := r.decrypt(ct1, ""); code != 400 {
		t.Errorf("decrypt of a version below min_decryption_version: %d %v", code, m)
	}
	sc.Configure("transit", "pandokos", 1, 0)
	if code, _ := r.decrypt(ct1, ""); code != 200 {
		t.Errorf("min_decryption_version lowered again: %d, want 200", code)
	}
	if code, _ := r.decrypt("vault:v9:"+ct1[9:], ""); code != 400 {
		t.Errorf("a version above the latest: %d, want 400", code)
	}
	t.Run("trim", func(t *testing.T) {
		sc.Configure("transit", "pandokos", 1, 2)
		sc.Trim("transit", "pandokos", 2)
		if code, _ := r.decrypt(ct1, ""); code != 400 {
			t.Errorf("a trimmed version: %d, want 400", code)
		}
		_, m := r.get("transit/keys/pandokos", r.token)
		if _, has := data(m)["keys"].(map[string]any)["1"]; has {
			t.Errorf("trimmed version 1 is still described")
		}
	})
}

func mustOK(t *testing.T, code int, m map[string]any) map[string]any {
	t.Helper()
	if code != 200 {
		t.Fatalf("status %d %v", code, m)
	}
	return m
}

func TestIdentify(t *testing.T) {
	r := newRig(t)
	sc := r.s.Namespace("")
	sc.CreateKey("transit", "other", KeyOptions{})
	in := func(v int) map[string]any {
		b := map[string]any{"input": b64([]byte("pandokos kek id v1"))}
		if v != 0 {
			b["key_version"] = v
		}
		return b
	}
	h := func(path, tok string, b map[string]any) (int, string) {
		code, m := r.post(path, tok, b)
		s, _ := data(m)["hmac"].(string)
		return code, s
	}
	_, a := h("transit/hmac/pandokos/sha2-256", r.token, in(0))
	_, a2 := h("transit/hmac/pandokos/sha2-256", r.token, in(0))
	if a == "" || a != a2 {
		t.Fatalf("not stable: %q %q", a, a2)
	}
	_, o := h("transit/hmac/other/sha2-256", r.root, in(0))
	if o == a {
		t.Errorf("two keys give the same value")
	}
	sc.Rotate("transit", "pandokos")
	_, v2 := h("transit/hmac/pandokos/sha2-256", r.token, in(2))
	_, v1 := h("transit/hmac/pandokos/sha2-256", r.token, in(1))
	if v1 != a || v2 == v1 {
		t.Errorf("versions: v1 %q (first %q), v2 %q", v1, a, v2)
	}
	if code, _ := h("transit/hmac/pandokos/sha2-256", r.token, in(3)); code != 400 {
		t.Errorf("above the latest: %d, want 400", code)
	}
	sc.Configure("transit", "pandokos", 2, 2)
	if code, _ := h("transit/hmac/pandokos/sha2-256", r.token, in(1)); code != 400 {
		t.Errorf("below min_decryption_version: %d, want 400", code)
	}
	sc.Configure("transit", "pandokos", 1, 2)
	if code, _ := h("transit/hmac/pandokos/sha2-256", r.token, in(1)); code != 400 {
		t.Errorf("below min_encryption_version: %d, want 400", code)
	}
}

func TestDescribe(t *testing.T) {
	r := newRig(t)
	sc := r.s.Namespace("")
	sc.CreateKey("transit", "full", KeyOptions{Exportable: true, DeletionAllowed: true, PlaintextBackup: true, AutoRotate: time.Hour})
	sc.Rotate("transit", "full")
	sc.Rotate("transit", "full")
	_, m := r.get("transit/keys/full", r.root)
	d := data(m)
	if d["latest_version"].(float64) != 3 || d["min_decryption_version"].(float64) != 1 || d["exportable"] != true ||
		d["deletion_allowed"] != true || d["allow_plaintext_backup"] != true || d["auto_rotate_period"].(float64) != 3600 {
		t.Errorf("description = %v", d)
	}
	keys := d["keys"].(map[string]any)
	if keys["1"] != keys["2"] {
		t.Errorf("versions rotated within a second differ: %v", keys)
	}
	sc.Configure("transit", "full", 3, 0)
	_, m = r.get("transit/keys/full", r.root)
	if keys := data(m)["keys"].(map[string]any); len(keys) != 1 {
		t.Errorf("keys below min_decryption_version are listed: %v", keys)
	}
	if code, m := r.get("transit/keys/missing", r.root); code != 404 || len(errorsOf(m)) != 0 {
		t.Errorf("a missing key: %d %v, want 404 with an empty list", code, m)
	}
	if code, _ := r.get("nowhere/keys/pandokos", r.root); code != 404 {
		t.Errorf("a wrong mount: %d, want 404", code)
	}
	sc.DeleteKey("transit", "full")
	if code, _ := r.get("transit/keys/full", r.root); code != 404 {
		t.Errorf("a deleted key: %d, want 404", code)
	}
	sc.CreateKey("transit", "full", KeyOptions{})
	if code, _ := r.get("transit/keys/full", r.root); code != 200 {
		t.Errorf("created again: %d, want 200", code)
	}
}

func TestDecryptFailuresAreBadRequests(t *testing.T) {
	r := newRig(t)
	if code, _ := r.post("transit/decrypt/missing", r.root, map[string]any{"ciphertext": "vault:v1:AAAA"}); code != 400 {
		t.Errorf("a missing key: %d, want 400", code)
	}
	for _, ct := range []string{"", "garbage", "vault:v1:!!!", "vault:v1:AA"} {
		if code, _ := r.decrypt(ct, ""); code != 400 {
			t.Errorf("ciphertext %q: %d, want 400", ct, code)
		}
	}
}

func TestPolicies(t *testing.T) {
	r := newRig(t)
	pt := map[string]any{"plaintext": b64([]byte("x"))}
	if code, _ := r.post("transit/encrypt/created", r.root, pt); code != 200 {
		t.Errorf("a root token encrypting on a missing key: %d, want 200", code)
	}
	if code, _ := r.get("transit/keys/created", r.root); code != 200 {
		t.Errorf("the key was not created")
	}
	t.Run("update only is 403 on a missing key", func(t *testing.T) {
		tok := r.s.Namespace("").Token(TokenOptions{Key: "absent"})
		if code, m := r.post("transit/encrypt/absent", tok, pt); code != 403 || errorsOf(m)[0] != "permission denied" {
			t.Errorf("%d %v, want 403 permission denied", code, m)
		}
	})
	for name, c := range map[string][2]string{
		"another key": {http.MethodPost, "transit/encrypt/created"},
		"rotate":      {http.MethodPost, "transit/keys/pandokos/rotate"},
		"config":      {http.MethodPost, "transit/keys/pandokos/config"},
		"export":      {http.MethodGet, "transit/export/encryption-key/pandokos"},
		"sys":         {http.MethodGet, "sys/mounts"},
	} {
		code, _ := call(t, r.c, r.s, c[0], c[1], r.token, "", pt)
		if code != 403 {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	if code, _ := r.get("auth/token/lookup-self", r.token); code != 200 {
		t.Errorf("lookup-self of the default token: %d", code)
	}
	nl := r.s.Namespace("").Token(TokenOptions{NoLookup: true})
	if code, _ := r.get("auth/token/lookup-self", nl); code != 403 {
		t.Errorf("lookup-self without the default policy: %d, want 403", code)
	}
	if code, _ := r.post("auth/token/renew-self", nl, nil); code != 403 {
		t.Errorf("renew-self without the default policy: %d, want 403", code)
	}
	custom := r.s.Namespace("").Token(TokenOptions{Paths: []Path{{"auth/token/lookup-self", "read"}}})
	if code, _ := r.get("auth/token/lookup-self", custom); code != 200 {
		t.Errorf("a token with Paths: %d", code)
	}
	if code, _ := r.post("transit/encrypt/pandokos", custom, pt); code != 403 {
		t.Errorf("a token with Paths does not replace the defaults: %d", code)
	}
	if _, m := r.get("auth/token/lookup-self", r.root); data(m)["policies"].([]any)[0] != "root" {
		t.Errorf("a root token does not show the root policy: %v", m)
	}
}

func TestTokens(t *testing.T) {
	r := newRig(t)
	sc := r.s.Namespace("")
	for name, tok := range map[string]string{"none": "", "unknown": "hvs.nope"} {
		if code, m := r.get("transit/keys/pandokos", tok); code != 403 || errorsOf(m)[0] != "permission denied" {
			t.Errorf("%s: %d %v, want 403", name, code, m)
		}
	}
	short := sc.Token(TokenOptions{TTL: time.Minute})
	if code, _ := r.get("transit/keys/pandokos", short); code != 200 {
		t.Fatalf("a live token: %d", code)
	}
	r.s.Advance(2 * time.Minute)
	if code, _ := r.get("transit/keys/pandokos", short); code != 403 {
		t.Errorf("an expired token: %d, want 403", code)
	}
	rev := sc.Token(TokenOptions{})
	sc.RevokeToken(rev)
	if code, _ := r.get("transit/keys/pandokos", rev); code != 403 {
		t.Errorf("a revoked token: %d, want 403", code)
	}
	t.Run("lookup and renewal", func(t *testing.T) {
		tok := sc.Token(TokenOptions{TTL: 60 * time.Second, MaxTTL: 100 * time.Second, Renewable: true})
		_, m := r.get("auth/token/lookup-self", tok)
		if d := data(m); d["ttl"].(float64) < 59 || d["renewable"] != true {
			t.Errorf("lookup = %v", d)
		}
		r.s.Advance(50 * time.Second)
		code, m := r.post("auth/token/renew-self", tok, map[string]any{})
		a, _ := m["auth"].(map[string]any)
		if code != 200 || a["lease_duration"].(float64) > 51 || a["lease_duration"].(float64) < 49 || len(m["warnings"].([]any)) != 1 {
			t.Errorf("a renewal capped by max_ttl: %d %v", code, m)
		}
		r.s.Advance(60 * time.Second)
		if code, _ := r.get("transit/keys/pandokos", tok); code != 403 {
			t.Errorf("past max_ttl: %d, want 403", code)
		}
	})
	t.Run("renewal beyond one period", func(t *testing.T) {
		tok := sc.Token(TokenOptions{TTL: 60 * time.Second, Renewable: true})
		r.s.Advance(40 * time.Second)
		code, m := r.post("auth/token/renew-self", tok, nil)
		if a, _ := m["auth"].(map[string]any); code != 200 || a["lease_duration"].(float64) < 59 {
			t.Errorf("renew: %d %v", code, m)
		}
	})
	t.Run("not renewable", func(t *testing.T) {
		tok := sc.Token(TokenOptions{TTL: time.Hour})
		if code, _ := r.post("auth/token/renew-self", tok, nil); code != 400 {
			t.Errorf("%d, want 400", code)
		}
		if code, _ := r.post("auth/token/renew-self", sc.Token(TokenOptions{Renewable: true}), nil); code != 400 {
			t.Errorf("a token that never expires: %d, want 400", code)
		}
	})
}

func TestAppRole(t *testing.T) {
	r := newRig(t)
	sc := r.s.Namespace("")
	roleID, secretID := sc.AppRole("approle", RoleOptions{TokenTTL: time.Minute, TokenMaxTTL: time.Hour})
	login := func(mount, role, sec string) (int, map[string]any) {
		return r.post("auth/"+mount+"/login", "", map[string]any{"role_id": role, "secret_id": sec})
	}
	code, m := login("approle", roleID, secretID)
	a, _ := m["auth"].(map[string]any)
	if code != 200 || a["renewable"] != true || a["lease_duration"].(float64) != 60 {
		t.Fatalf("login = %d %v", code, m)
	}
	tok := a["client_token"].(string)
	if code, _ := r.get("transit/keys/pandokos", tok); code != 200 {
		t.Errorf("the login's token cannot describe the key: %d", code)
	}
	if code, m := login("approle", roleID, "wrong"); code != 400 || errorsOf(m)[0] != "invalid role or secret ID" {
		t.Errorf("a wrong secret ID: %d %v", code, m)
	}
	if code, _ := login("approle", "wrong", secretID); code != 400 {
		t.Errorf("a wrong role ID: %d, want 400", code)
	}
	if code, _ := login("nowhere", roleID, secretID); code != 403 {
		t.Errorf("a missing auth mount: %d, want 403", code)
	}
	newID := sc.NewSecretID("approle", roleID)
	if code, _ := login("approle", roleID, secretID); code != 400 {
		t.Errorf("the old secret ID: %d, want 400", code)
	}
	if code, _ := login("approle", roleID, newID); code != 200 {
		t.Errorf("the new secret ID: %d, want 200", code)
	}
}

func TestNamespaces(t *testing.T) {
	r := newRig(t)
	ns := r.s.Namespace("team/a")
	ns.CreateKey("transit", "inner", KeyOptions{})
	tok := ns.Token(TokenOptions{Key: "inner"})
	pt := map[string]any{"plaintext": b64([]byte("x"))}
	if code, _ := call(t, r.c, r.s, http.MethodPost, "transit/encrypt/inner", tok, "team/a", pt); code != 200 {
		t.Errorf("with the header: %d, want 200", code)
	}
	if code, _ := call(t, r.c, r.s, http.MethodPost, "transit/encrypt/inner", tok, "", pt); code != 403 {
		t.Errorf("a namespace token without the header: %d, want 403", code)
	}
	if code, _ := call(t, r.c, r.s, http.MethodGet, "transit/keys/inner", r.root, "", nil); code != 404 {
		t.Errorf("the key without the header: %d, want 404", code)
	}
	if code, _ := call(t, r.c, r.s, http.MethodGet, "transit/keys/inner", r.root, "team/a/", nil); code != 200 {
		t.Errorf("a root token with the header: %d, want 200", code)
	}
	if code, _ := call(t, r.c, r.s, http.MethodGet, "transit/keys/inner", r.root, "other", nil); code != 404 {
		t.Errorf("a namespace that does not exist: %d, want 404", code)
	}
	id, sec := ns.AppRole("approle", RoleOptions{Key: "inner"})
	body := map[string]any{"role_id": id, "secret_id": sec}
	if code, _ := call(t, r.c, r.s, http.MethodPost, "auth/approle/login", "", "team/a", body); code != 200 {
		t.Errorf("a login in the namespace: %d", code)
	}
	if code, _ := call(t, r.c, r.s, http.MethodPost, "auth/approle/login", "", "", body); code != 403 {
		t.Errorf("a login at the root: %d, want 403", code)
	}
}

func TestFaults(t *testing.T) {
	r := newRig(t)
	desc := func() (int, map[string]any) { return r.get("transit/keys/pandokos", r.token) }
	t.Run("sealed", func(t *testing.T) {
		r.s.Sealed()
		defer r.s.Clear()
		for _, c := range [][2]string{{"GET", "transit/keys/pandokos"}, {"POST", "auth/approle/login"}, {"POST", "transit/encrypt/pandokos"}, {"GET", "sys/health"}} {
			code, m := call(t, r.c, r.s, c[0], c[1], r.token, "", nil)
			if code != 503 || errorsOf(m)[0] != "Vault is sealed" {
				t.Errorf("%v: %d %v", c, code, m)
			}
		}
	})
	t.Run("status", func(t *testing.T) {
		r.s.Status(429)
		defer r.s.Clear()
		if code, m := desc(); code != 429 || len(errorsOf(m)) != 1 {
			t.Errorf("%d %v", code, m)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		r.s.Redirect("http://127.0.0.1:1/")
		defer r.s.Clear()
		req, _ := http.NewRequest("GET", r.s.URL()+"/v1/x", nil)
		resp, err := r.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 307 || resp.Header.Get("Location") != "http://127.0.0.1:1/" {
			t.Errorf("%d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
	})
	t.Run("delay", func(t *testing.T) {
		r.s.Delay(150 * time.Millisecond)
		defer r.s.Clear()
		start := time.Now()
		desc()
		if time.Since(start) < 140*time.Millisecond {
			t.Errorf("answered after %v", time.Since(start))
		}
	})
	t.Run("drop", func(t *testing.T) {
		r.s.Drop()
		defer r.s.Clear()
		req, _ := http.NewRequest("GET", r.s.URL()+"/v1/x", nil)
		if resp, err := r.c.Do(req); err == nil {
			resp.Body.Close()
			t.Errorf("a dropped connection answered %d", resp.StatusCode)
		}
		if n := len(r.s.Requests()); n == 0 {
			t.Errorf("a dropped request was not recorded")
		}
	})
	t.Run("hang", func(t *testing.T) {
		r.s.Hang()
		cl := &http.Client{Timeout: 200 * time.Millisecond}
		req, _ := http.NewRequest("GET", r.s.URL()+"/v1/x", nil)
		if resp, err := cl.Do(req); err == nil {
			resp.Body.Close()
			t.Errorf("a hanging double answered %d", resp.StatusCode)
		}
		r.s.Clear()
		if code, _ := desc(); code != 200 {
			t.Errorf("after Clear: %d", code)
		}
	})
	t.Run("echo", func(t *testing.T) {
		r.s.EchoInErrors("canary-extra")
		defer r.s.Clear()
		_, m := r.post("transit/decrypt/pandokos", r.token, map[string]any{"ciphertext": "canary-body"})
		raw, _ := json.Marshal(m)
		for _, w := range []string{r.token, "canary-body", "canary-extra"} {
			if !strings.Contains(string(raw), w) {
				t.Errorf("the error body does not repeat %q: %s", w, raw)
			}
		}
	})
	t.Run("oversize", func(t *testing.T) {
		r.s.OversizeAnswers()
		defer r.s.Clear()
		req, _ := http.NewRequest("GET", r.s.URL()+"/v1/transit/keys/pandokos", nil)
		resp, err := r.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		n, _ := io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 || n <= 64<<10 {
			t.Errorf("%d, %d bytes", resp.StatusCode, n)
		}
	})
}

func TestStopAndStart(t *testing.T) {
	r := newRig(t)
	ct := r.encrypt([]byte("kept"), "a")
	addr := r.s.URL()
	r.s.Stop()
	r.s.Stop() // idempotent
	req, _ := http.NewRequest("GET", addr+"/v1/transit/keys/pandokos", nil)
	if resp, err := r.c.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("a stopped double answered %d", resp.StatusCode)
	}
	r.s.Start()
	if r.s.URL() != addr {
		t.Errorf("address %s, want %s", r.s.URL(), addr)
	}
	if code, m := r.decrypt(ct, "a"); code != 200 || data(m)["plaintext"] != b64([]byte("kept")) {
		t.Errorf("the state was lost: %d %v", code, m)
	}
}

func TestWhatItSaw(t *testing.T) {
	r := newRig(t)
	ct := r.encrypt([]byte("secret-value"), "a")
	r.decrypt(ct, "a")
	r.get("transit/keys/pandokos", r.token)
	r.post("transit/hmac/pandokos/sha2-256", r.token, map[string]any{"input": b64([]byte("i"))})
	r.get("auth/token/lookup-self", r.token)
	r.post("auth/token/renew-self", r.token, nil)
	for op, want := range map[string]int{OpWrap: 1, OpUnwrap: 1, OpDescribe: 1, OpIdentify: 1, OpLookup: 1, OpRenew: 1, OpLogin: 0} {
		if got := r.s.Count(op); got != want {
			t.Errorf("Count(%s) = %d, want %d", op, got, want)
		}
	}
	reqs := r.s.Requests()
	if len(reqs) != 6 || reqs[0].Method != "POST" || reqs[0].Path != "/v1/transit/encrypt/pandokos" ||
		reqs[0].Header.Get("X-Vault-Token") != r.token || !bytes.Contains(reqs[0].Body, []byte("plaintext")) {
		t.Errorf("requests = %+v", reqs)
	}
	pts := r.s.Plaintexts()
	if len(pts) < 2 || string(pts[0]) != "secret-value" || string(pts[1]) != "secret-value" {
		t.Errorf("plaintexts = %q", pts)
	}
	if pl := r.s.Payloads(); len(pl) != 2 || !bytes.Equal(pl[0], pl[1]) || len(pl[0]) != 12+12+16 {
		t.Errorf("payloads = %d", len(pl))
	}
	r.s.Reset()
	if len(r.s.Requests()) != 0 || len(r.s.Plaintexts()) != 0 || len(r.s.Payloads()) != 0 {
		t.Errorf("Reset kept what it saw")
	}
	if code, _ := r.get("transit/keys/pandokos", r.token); code != 200 {
		t.Errorf("Reset lost the state")
	}
}

func TestTLS(t *testing.T) {
	s, caFile := NewTLS(t)
	if !strings.HasPrefix(s.URL(), "https://") {
		t.Fatalf("URL %s", s.URL())
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the authority file holds no certificate")
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 5 * time.Second}
	root := s.Namespace("").Token(TokenOptions{Root: true})
	if code, _ := call(t, c, s, http.MethodGet, "transit/keys/x", root, "", nil); code != 404 {
		t.Errorf("status %d, want 404 over TLS", code)
	}
	plain := &http.Client{Timeout: 5 * time.Second}
	if resp, err := plain.Get(s.URL() + "/v1/x"); err == nil {
		resp.Body.Close()
		t.Errorf("a client without the authority was served")
	}
}

func TestConcurrentUse(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ct := r.encrypt([]byte("x"), "a")
			if code, _ := r.decrypt(ct, "a"); code != 200 {
				t.Errorf("decrypt: %d", code)
			}
		}()
	}
	wg.Wait()
}
