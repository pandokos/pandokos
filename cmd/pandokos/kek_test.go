package main

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

var reKEKGenerated = regexp.MustCompile(`^key-encryption key written to (.+); version (local-[0-9a-f]{16})\n$`)

// versionOfFile is the version identifier of the key in the file, by the provider.
func versionOfFile(t *testing.T, path string) string {
	t.Helper()
	k, err := secret.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := vault.NewLocalProvider(k)
	if err != nil {
		t.Fatalf("the key file is not accepted by the local provider: %v", err)
	}
	v, _, err := p.Wrap(t.Context(), make([]byte, 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestKEKGenerate is T-312.
func TestKEKGenerate(t *testing.T) {
	t.Run("a new file of mode 0600 with a key of 32 bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kek")
		code, out, errs := runCmd(t, nil, "kek", "generate", "--out", path)
		if code != 0 || errs != "" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
			t.Fatalf("the file: %v, %v", st, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSuffix(string(raw), "\n")
		if strings.ContainsAny(line, "\r\n \t") || len(line) != 44 {
			t.Fatalf("the file is not one line of 44 characters")
		}
		key, err := base64.StdEncoding.Strict().DecodeString(line)
		if err != nil || len(key) != 32 {
			t.Fatalf("the content decodes to %d bytes: %v", len(key), err)
		}
		// The output names the version and not the key.
		m := reKEKGenerated.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("stdout %q is not `key-encryption key written to FILE; version local-…`", out)
		}
		if m[1] != path || m[2] != versionOfFile(t, path) {
			t.Errorf("the output names %q and %q, want the file %q and the version of its key %q", m[1], m[2], path, versionOfFile(t, path))
		}
		testutil.AssertAbsent(t, "stdout", out, testutil.SecretForms(line)...)
		testutil.AssertAbsent(t, "stdout", out, testutil.SecretForms(string(key))...)
	})

	t.Run("an existing file is refused and left alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "kek")
		const before = "this file was here first\n"
		if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runCmd(t, nil, "kek", "generate", "--out", path)
		if code != 1 || out != "" || !strings.HasPrefix(errs, "error: ") {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		if got, _ := os.ReadFile(path); string(got) != before {
			t.Error("an existing file was changed")
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
			t.Errorf("the mode of an existing file was changed to %v", st.Mode().Perm())
		}
		// A link that points nowhere is not a free name either: nothing is created behind it.
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere")
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		code, _, errs = runCmd(t, nil, "kek", "generate", "--out", link)
		if code != 1 || errs == "" {
			t.Errorf("a dangling link: code %d, stderr %q", code, errs)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Error("the key was written through a link")
		}
	})

	t.Run("two runs differ", func(t *testing.T) {
		dir := t.TempDir()
		a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
		_, outA, _ := runCmd(t, nil, "kek", "generate", "--out", a)
		_, outB, _ := runCmd(t, nil, "kek", "generate", "--out", b)
		ra, _ := os.ReadFile(a)
		rb, _ := os.ReadFile(b)
		if len(ra) == 0 || string(ra) == string(rb) {
			t.Error("two generated keys are equal")
		}
		va, vb := reKEKGenerated.FindStringSubmatch(outA), reKEKGenerated.FindStringSubmatch(outB)
		if va == nil || vb == nil || va[2] == vb[2] {
			t.Errorf("two generated keys have one version: %q %q", outA, outB)
		}
	})

	t.Run("usage", func(t *testing.T) {
		dir := t.TempDir()
		for _, args := range [][]string{
			{"kek", "generate"}, {"kek", "generate", "--out"}, {"kek", "generate", "--out", ""},
			{"kek", "generate", "--bogus"}, {"kek", "generate", "--out", filepath.Join(dir, "x"), "extra"},
			{"kek"}, {"kek", "rotate"},
		} {
			code, out, errs := runCmd(t, nil, args...)
			if code != 2 || out != "" || errs == "" {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
			}
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("a refused command created %d files", len(entries))
		}
		// There is no flag that prints the key.
		for _, flag := range []string{"--print", "--stdout", "-v", "--show"} {
			if code, out, _ := runCmd(t, nil, "kek", "generate", "--out", filepath.Join(dir, "y"), flag); code != 2 || out != "" {
				t.Errorf("%s: code %d, stdout %q", flag, code, out)
			}
		}
	})

	t.Run("a directory that does not exist", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "kek")
		code, out, errs := runCmd(t, nil, "kek", "generate", "--out", path)
		if code != 1 || out != "" || errs == "" {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Error("the command created a directory")
		}
	})
}

// checkEnv is the environment of `pandokos kek check` against a double: the settings of serve with
// provider transit, a database address that counts the connections made to it (the command must
// make none) and no key file.
type checkEnv struct {
	env   []string
	conns *atomic.Int64
}

func newCheckEnv(t *testing.T, s *transittest.Server, f vaulttest.TransitFiles, extra ...string) *checkEnv {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conns := &atomic.Int64{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	env := []string{
		"PATH=x",
		"PANDOKOS_DATABASE_ADDRESS=" + l.Addr().String(),
		"PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_gateway", "synthetic-gateway-password"),
		"PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_admin", "synthetic-admin-password"),
		"PANDOKOS_DATABASE_WORKER_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_worker", "synthetic-worker-password"),
		"PANDOKOS_VAULT_KEK_PROVIDER=transit",
		"PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS=" + s.URL(),
		"PANDOKOS_VAULT_KEK_TRANSIT_MOUNT=" + f.Mount,
		"PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=" + f.Key,
		"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD=" + f.Method,
		"PANDOKOS_VAULT_KEK_TRANSIT_REQUEST_TIMEOUT=500ms",
	}
	if f.Namespace != "" {
		env = append(env, "PANDOKOS_VAULT_KEK_TRANSIT_NAMESPACE="+f.Namespace)
	}
	switch f.Method {
	case "token":
		env = append(env, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE="+f.TokenFile)
	case "approle":
		env = append(env, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_MOUNT="+f.AppRoleMount,
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_ROLE_ID_FILE="+f.RoleIDFile,
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_SECRET_ID_FILE="+f.SecretIDFile)
	}
	return &checkEnv{env: append(env, extra...), conns: conns}
}

// TestKEKCheck is T-1080: `pandokos kek check` against the double of the key service.
func TestKEKCheck(t *testing.T) {
	reVersion := regexp.MustCompile(`(?m)^version: (transit-[0-9a-f]{16}-v[0-9]+)$`)

	t.Run("a sound key: the provider, the identifier, the range and the weak settings, and 0", func(t *testing.T) {
		for _, method := range []string{"token", "approle"} {
			s := transittest.New(t)
			f := vaulttest.Transit(t, s, vaulttest.TransitSetup{Method: method, KeyOptions: transittest.KeyOptions{Exportable: true}})
			s.Namespace("").Rotate(f.Mount, f.Key)
			ce := newCheckEnv(t, s, f)
			code, out, errs := runCmd(t, ce.env, "kek", "check")
			if code != 0 {
				t.Fatalf("%s: code %d, stdout %q, stderr %q", method, code, out, errs)
			}
			if !strings.Contains(out, "provider: transit\n") {
				t.Errorf("%s: no line `provider: transit`: %q", method, out)
			}
			m := reVersion.FindStringSubmatch(out)
			if m == nil || !strings.HasSuffix(m[1], "-v2") {
				t.Errorf("%s: no line `version: transit-…-v2`: %q", method, out)
			}
			if !strings.Contains(out, "unwraps: v1 to v2\n") {
				t.Errorf("%s: no line `unwraps: v1 to v2`: %q", method, out)
			}
			if !strings.Contains(out, "weak: ") || !strings.Contains(out, "exportable") {
				t.Errorf("%s: the weak setting is not reported: %q", method, out)
			}
			if got := ce.conns.Load(); got != 0 {
				t.Errorf("%s: the command opened %d database connections", method, got)
			}
			secrets := transittest.Secrets(s, f.Token, f.RoleID, f.SecretID)
			testutil.AssertAbsent(t, "stdout", out, secrets...)
			testutil.AssertAbsent(t, "stderr", errs, secrets...)
			// It only probes: the double saw the requests of the check and nothing else.
			for _, q := range s.Requests() {
				if strings.Contains(q.Path, "/rotate") || strings.Contains(q.Path, "/config") || strings.Contains(q.Path, "/trim") {
					t.Errorf("%s: the command sent %s", method, q.Path)
				}
			}
		}
	})

	t.Run("no weak line for a sound key", func(t *testing.T) {
		s := transittest.New(t)
		f := vaulttest.Transit(t, s, vaulttest.TransitSetup{})
		code, out, errs := runCmd(t, newCheckEnv(t, s, f).env, "kek", "check")
		if code != 0 || strings.Contains(out, "weak: ") && !strings.Contains(out, "weak: none") {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
	})

	failures := []struct {
		name  string
		fault func(s *transittest.Server, f vaulttest.TransitFiles)
		want  []string // any of these in the output
	}{
		{"a refused token", func(s *transittest.Server, f vaulttest.TransitFiles) { s.Namespace("").RevokeToken(f.Token) }, []string{"denied"}},
		{"a missing key", func(s *transittest.Server, f vaulttest.TransitFiles) { s.Namespace("").DeleteKey(f.Mount, f.Key) }, []string{"does not exist", "not_found"}},
		{"associated data not bound", func(s *transittest.Server, _ vaulttest.TransitFiles) { s.IgnoreAssociatedData() }, []string{"associated data", "aad_not_bound"}},
		{"a service away", func(s *transittest.Server, _ vaulttest.TransitFiles) { s.Stop() }, []string{"unavailable"}},
		{"a service that hangs", func(s *transittest.Server, _ vaulttest.TransitFiles) { s.Hang() }, []string{"unavailable"}},
	}
	for _, c := range failures {
		t.Run(c.name+": the class and 1 within the request timeout", func(t *testing.T) {
			s := transittest.New(t)
			f := vaulttest.Transit(t, s, vaulttest.TransitSetup{})
			ce := newCheckEnv(t, s, f)
			c.fault(s, f)
			s.EchoInErrors("synthetic-echoed-canary", f.Token)
			start := time.Now()
			code, out, errs := runCmd(t, ce.env, "kek", "check")
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("the command took %v with a request timeout of 500 ms", d)
			}
			if code != 1 {
				t.Errorf("code %d, stdout %q, stderr %q, want 1", code, out, errs)
			}
			all := out + errs
			found := false
			for _, w := range c.want {
				found = found || strings.Contains(all, w)
			}
			if !found {
				t.Errorf("the output %q does not print the class (%v)", all, c.want)
			}
			if got := ce.conns.Load(); got != 0 {
				t.Errorf("the command opened %d database connections", got)
			}
			testutil.AssertAbsent(t, "the output", all, append(transittest.Secrets(s, f.Token), "synthetic-echoed-canary")...)
		})
	}

	t.Run("a configuration that is not valid is refused as for serve", func(t *testing.T) {
		s := transittest.New(t)
		f := vaulttest.Transit(t, s, vaulttest.TransitSetup{})
		ce := newCheckEnv(t, s, f, "PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=-bad")
		code, out, errs := runCmd(t, ce.env, "kek", "check")
		if code == 0 || ce.conns.Load() != 0 || len(s.Requests()) != 0 {
			t.Errorf("code %d, stdout %q, stderr %q, %d requests", code, out, errs, len(s.Requests()))
		}
	})
}
