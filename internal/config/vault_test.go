package config

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// keyLine is a key file content that is valid and recognisable in an output.
func keyLine(t *testing.T, fill byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func envOfKeys() map[string]string {
	m := map[string]string{}
	for _, k := range Keys() {
		m[k.Name] = k.Env
	}
	return m
}

// TestVaultConfiguration is T-308: the four keys of sprint 2.1, section 5.9.
func TestVaultConfiguration(t *testing.T) {
	envOf := envOfKeys()

	t.Run("the keys, their environment names and their defaults", func(t *testing.T) {
		want := map[string]struct{ env, typ, def string }{
			"cache.credential_ttl":     {"PANDOKOS_CACHE_CREDENTIAL_TTL", "duration", "30s"},
			"cache.data_key_ttl":       {"PANDOKOS_CACHE_DATA_KEY_TTL", "duration", "5m"},
			"vault.kek.provider":       {"PANDOKOS_VAULT_KEK_PROVIDER", "local, transit", "local"},
			"vault.kek.local.key_file": {"PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE", "path", ""},
		}
		found := 0
		for _, k := range Keys() {
			w, ok := want[k.Name]
			if !ok {
				continue
			}
			found++
			if k.Env != w.env || k.Default != w.def || k.Type != w.typ || k.Description == "" {
				t.Errorf("%s: %+v, want env %s, type %s, default %q", k.Name, k, w.env, w.typ, w.def)
			}
		}
		if found != len(want) {
			t.Errorf("%d of the four keys are in Keys()", found)
		}
		d := Defaults()
		if time.Duration(d.Cache.CredentialTTL) != 30*time.Second || time.Duration(d.Cache.DataKeyTTL) != 5*time.Minute ||
			d.Vault.KEK.Provider != "local" || d.Vault.KEK.Local.KeyFile != "" || !d.Vault.KEK.Local.Key.IsZero() {
			t.Errorf("defaults: %+v %+v", d.Cache, d.Vault)
		}
	})

	t.Run("cache.credential_ttl", func(t *testing.T) {
		for _, bad := range []string{"0s", "999ms", "61s", "2m", "-1s"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.credential_ttl"]+"="+bad)
			_, _, err := Load(src)
			if msg, ok := problemMessage(err, "cache.credential_ttl"); !ok || msg != "must be between 1s and 60s" {
				t.Errorf("%s: %q %v", bad, msg, err)
			}
		}
		for good, want := range map[string]time.Duration{"1s": time.Second, "60s": time.Minute, "45s": 45 * time.Second, "1m": time.Minute} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.credential_ttl"]+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.Cache.CredentialTTL) != want {
				t.Errorf("%s: %v", good, c.Cache.CredentialTTL)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["cache.credential_ttl"]+"=soon")
		if _, _, err := Load(src); !hasProblem(err, "cache.credential_ttl") {
			t.Errorf("a value that is not a duration: %v", err)
		}
	})

	t.Run("cache.data_key_ttl", func(t *testing.T) {
		for _, bad := range []string{"0s", "999ms", "16m", "901s", "1h", "-1m"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.data_key_ttl"]+"="+bad)
			_, _, err := Load(src)
			if msg, ok := problemMessage(err, "cache.data_key_ttl"); !ok || msg != "must be between 1s and 15m" {
				t.Errorf("%s: %q %v", bad, msg, err)
			}
		}
		for good, want := range map[string]time.Duration{"1s": time.Second, "15m": 15 * time.Minute, "900s": 15 * time.Minute, "5m": 5 * time.Minute} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.data_key_ttl"]+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.Cache.DataKeyTTL) != want {
				t.Errorf("%s: %v", good, c.Cache.DataKeyTTL)
			}
		}
	})

	t.Run("vault.kek.provider", func(t *testing.T) {
		for _, bad := range []string{"kms", "Local", "aws", "local "} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["vault.kek.provider"]+"="+bad)
			_, _, err := Load(src)
			if msg, ok := problemMessage(err, "vault.kek.provider"); !ok || msg != "must be local or transit" {
				t.Errorf("%q: %q %v", bad, msg, err)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["vault.kek.provider"]+"=local")
		if c, _ := mustLoad(t, src); c.Vault.KEK.Provider != "local" {
			t.Errorf("provider %q", c.Vault.KEK.Provider)
		}
		// transit is accepted, with its settings and without a key file (TestTransitKeys has the rest).
		tsrc := transitSource(t, "token")
		if c, _ := mustLoad(t, tsrc); c.Vault.KEK.Provider != "transit" || !c.Vault.KEK.Transit.Configured() {
			t.Errorf("provider %q, transit configured %v", c.Vault.KEK.Provider, c.Vault.KEK.Transit.Configured())
		}
	})

	t.Run("serve needs the key file, and reads it with the other secrets", func(t *testing.T) {
		line := keyLine(t, 0x10)
		src := validSource(t)
		src.Environ = append(without(src.Environ, envKEKFile), envKEKFile+"="+testutil.WriteSecret(t, "kek", line))
		c, _ := mustLoad(t, src)
		if c.Vault.KEK.Local.Key.Reveal() != line {
			t.Error("the key was not read from its file")
		}
		if c.Vault.KEK.Local.KeyFile == "" || strings.Contains(c.Vault.KEK.Local.KeyFile, line) {
			t.Errorf("KeyFile = %q", c.Vault.KEK.Local.KeyFile)
		}
		// Trailing white space is trimmed like every secret file.
		src = validSource(t)
		src.Environ = append(without(src.Environ, envKEKFile), envKEKFile+"="+writeFile(t, "\t"+line+"\r\n\r\n"))
		if c, _ := mustLoad(t, src); c.Vault.KEK.Local.Key.Reveal() != line {
			t.Error("the key was not trimmed")
		}
		// Not set, or a file that is not there.
		src = validSource(t)
		src.Environ = without(src.Environ, envKEKFile)
		_, _, err := Load(src)
		if msg, ok := problemMessage(err, "vault.kek.local.key_file"); !ok || msg != "must be set" {
			t.Errorf("key file not set: %q %v", msg, err)
		}
		src.Environ = append(src.Environ, envKEKFile+"=/nonexistent/kek")
		_, _, err = Load(src)
		if msg, ok := problemMessage(err, "vault.kek.local.key_file"); !ok || !strings.Contains(msg, "cannot read secret file") {
			t.Errorf("key file missing: %q %v", msg, err)
		}
		// Through the file of the configuration too.
		src = validSource(t)
		src.Environ = without(src.Environ, envKEKFile)
		src.File = writeFile(t, "vault:\n  kek:\n    local:\n      key_file: "+testutil.WriteSecret(t, "kek2", line)+"\n")
		if c, _ := mustLoad(t, src); c.Vault.KEK.Local.Key.Reveal() != line {
			t.Error("the key file named in the configuration file was not read")
		}
		// The key itself is not a configuration key: there is no way to put it in the file.
		src = validSource(t)
		src.File = writeFile(t, "vault:\n  kek:\n    local:\n      key: "+line+"\n")
		_, _, err = Load(src)
		if err == nil || !hasProblem(err, "file") {
			t.Errorf("a key in the configuration file: %v", err)
		}
		if err != nil {
			testutil.AssertAbsent(t, "error text", err.Error(), line)
		}
	})

	t.Run("migrate and init do not read the key file", func(t *testing.T) {
		for name, src := range map[string]Source{"migrate": validMigrateSource(t), "init": validInitSource(t)} {
			src.Environ = append(src.Environ, envKEKFile+"=/nonexistent/kek")
			c, ws := mustLoad(t, src)
			if !c.Vault.KEK.Local.Key.IsZero() {
				t.Errorf("%s read the key", name)
			}
			for _, w := range ws {
				if w.Key == "vault.kek.local.key_file" {
					t.Errorf("%s warned about the key file: %s", name, w.Message)
				}
			}
			mustLoad(t, func() Source { s := src; s.Environ = without(s.Environ, envKEKFile); return s }())
		}
	})

	t.Run("what is not a key", func(t *testing.T) {
		good := keyLine(t, 0x20)
		raw31 := make([]byte, 31)
		url := base64.URLEncoding.EncodeToString([]byte(strings.Repeat("\xff", 32)))
		bad := map[string]string{
			"31 bytes":             base64.StdEncoding.EncodeToString(raw31),
			"64 bytes":             base64.StdEncoding.EncodeToString(make([]byte, 64)),
			"the URL alphabet":     url,
			"hexadecimal":          hex.EncodeToString([]byte(strings.Repeat("k", 32))),
			"white space inside":   good[:20] + " " + good[20:],
			"without padding":      strings.TrimRight(good, "="),
			"a control character":  good[:10] + "\x01" + good[10:],
			"two lines":            good + "\n" + good,
			"text":                 "this-is-not-a-key-pandokos-canary-key-material",
			"empty":                "",
			"a long line of noise": strings.Repeat("A", 9000),
		}
		for name, content := range bad {
			p := filepath.Join(t.TempDir(), "kek")
			if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			src := validSource(t)
			src.Environ = append(without(src.Environ, envKEKFile), envKEKFile+"="+p)
			_, _, err := Load(src)
			if err == nil || !hasProblem(err, "vault.kek.local.key_file") {
				t.Errorf("%s: %v", name, err)
				continue
			}
			// The problem names the key and the rule, and quotes nothing of the file.
			if len(content) >= 8 && (strings.Contains(err.Error(), content) || strings.Contains(err.Error(), content[:8])) {
				t.Errorf("%s: the error quotes the content: %q", name, err.Error())
			}
			testutil.AssertAbsent(t, "error text of "+name, err.Error(), good, strings.TrimRight(good, "="))
		}
		// A directory, and a link to a missing file.
		dir := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
			t.Fatal(err)
		}
		for name, p := range map[string]string{"a directory": dir, "a dangling link": link} {
			src := validSource(t)
			src.Environ = append(without(src.Environ, envKEKFile), envKEKFile+"="+p)
			_, _, err := Load(src)
			if err == nil || !hasProblem(err, "vault.kek.local.key_file") {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("a file readable by others is a warning, not a refusal", func(t *testing.T) {
		line := keyLine(t, 0x30)
		dir := t.TempDir()
		mk := func(name string, mode os.FileMode) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			return p
		}
		warn := func(p string) (string, bool) {
			src := validSource(t)
			src.Environ = append(without(src.Environ, envKEKFile), envKEKFile+"="+p)
			c, ws := mustLoad(t, src)
			if c.Vault.KEK.Local.Key.Reveal() != line {
				t.Errorf("the key of %s was not loaded", filepath.Base(p))
			}
			for _, w := range ws {
				if w.Key == "vault.kek.local.key_file" {
					return w.Message, true
				}
			}
			return "", false
		}
		for _, m := range []os.FileMode{0o600, 0o400, 0o440, 0o640, 0o660} {
			if msg, ok := warn(mk(m.String(), m)); ok {
				t.Errorf("mode %v warned: %s", m, msg)
			}
		}
		for _, m := range []os.FileMode{0o644, 0o604, 0o606, 0o664, 0o666, 0o601, 0o602, 0o404, 0o444} {
			msg, ok := warn(mk(m.String(), m))
			if !ok || msg == "" {
				t.Errorf("mode %v did not warn", m)
				continue
			}
			testutil.AssertAbsent(t, "the warning", msg, testutil.SecretForms(line)...)
			if !strings.Contains(strings.ToLower(msg), "other") {
				t.Errorf("mode %v: the warning %q does not say that other users can access the file", m, msg)
			}
		}
		// A link is followed: the mode of the target counts (secret volumes are links).
		open, secretFile := mk("target-open", 0o644), mk("target-closed", 0o600)
		for name, target := range map[string]string{"open": open, "closed": secretFile} {
			link := filepath.Join(dir, "link-to-"+name)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			_, ok := warn(link)
			if ok != (name == "open") {
				t.Errorf("a link to a file that is %s: warning %v", name, ok)
			}
		}
	})

	t.Run("Keys and the example in the reference stay in step", func(t *testing.T) {
		// The documentation table is generated from Keys(); here only that every key has a
		// complete row, so the generator has what it needs.
		for _, k := range Keys() {
			if k.Name == "" || k.Env == "" || k.Type == "" || k.Description == "" {
				t.Errorf("incomplete key %+v", k)
			}
		}
	})
}

// transitSource is a source for serve with provider transit, no key file and the settings of the
// method: the base of the cases of TestTransitKeys.
func transitSource(t *testing.T, method string) Source {
	t.Helper()
	env := without(validSource(t).Environ, envKEKFile)
	env = append(env,
		"PANDOKOS_VAULT_KEK_PROVIDER=transit",
		"PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS=https://key-service.example.test:8200",
		"PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=pandokos",
		"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD="+method,
	)
	switch method {
	case "token":
		env = append(env, "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE="+testutil.WriteSecret(t, "transit_token", "hvs.synthetic-token-0001"))
	case "approle":
		env = append(env,
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_ROLE_ID_FILE="+testutil.WriteSecret(t, "transit_role", "synthetic-role-0001"),
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_SECRET_ID_FILE="+testutil.WriteSecret(t, "transit_secret", "synthetic-secret-0001"))
	}
	return Source{Environ: env}
}

// TestTransitKeys is T-1078: the keys of the transit provider, with their defaults, environment
// names, bounds, problems and warnings (sprint 12.1, section 5.11).
func TestTransitKeys(t *testing.T) {
	const prefix = "vault.kek.transit."
	envName := func(key string) string {
		return "PANDOKOS_" + strings.ToUpper(strings.NewReplacer(".", "_").Replace(key))
	}
	with := func(src Source, kv ...string) Source {
		src.Environ = append(append([]string(nil), src.Environ...), kv...)
		return src
	}
	override := func(base Source, key, value string) Source {
		base.Environ = append(without(base.Environ, envName(key)), envName(key)+"="+value)
		return base
	}
	drop := func(base Source, keys ...string) Source {
		var names []string
		for _, k := range keys {
			names = append(names, envName(k))
		}
		base.Environ = without(base.Environ, names...)
		return base
	}

	t.Run("Keys lists the thirteen keys with environment names that follow the rule", func(t *testing.T) {
		n := 0
		for _, k := range Keys() {
			if strings.HasPrefix(k.Name, prefix) {
				n++
				if k.Env != envName(k.Name) {
					t.Errorf("%s: environment name %s, want %s", k.Name, k.Env, envName(k.Name))
				}
				if k.Description == "" || k.Type == "" {
					t.Errorf("%s: incomplete row %+v", k.Name, k)
				}
			}
		}
		if n != 13 {
			t.Errorf("%d keys under %s, want 13", n, prefix)
		}
	})

	t.Run("the defaults", func(t *testing.T) {
		d := Defaults().Vault.KEK
		tr := d.Transit
		if d.Provider != "local" || tr.Address != "" || tr.Namespace != "" || tr.Mount != "transit" || tr.KeyName != "" ||
			tr.TLS.CAFile != "" || tr.Auth.Method != "" || tr.Auth.TokenFile != "" || tr.Auth.AppRole.Mount != "approle" ||
			tr.Auth.AppRole.RoleIDFile != "" || tr.Auth.AppRole.SecretIDFile != "" || time.Duration(tr.RequestTimeout) != 2*time.Second ||
			tr.MaxConcurrentRequests != 32 || time.Duration(tr.StartTimeout) != 0 || tr.Configured() {
			t.Errorf("defaults: %+v", d)
		}
	})

	t.Run("a valid configuration with each method", func(t *testing.T) {
		for _, method := range []string{"token", "approle"} {
			c, ws := mustLoad(t, transitSource(t, method))
			tr := c.Vault.KEK.Transit
			if c.Vault.KEK.Provider != "transit" || !tr.Configured() || tr.URL == nil || tr.URL.Host != "key-service.example.test:8200" ||
				tr.URL.Scheme != "https" || tr.KeyName != "pandokos" || tr.Auth.Method != method {
				t.Errorf("%s: %+v", method, tr)
			}
			if tr.TLS.RootCAs != nil {
				t.Errorf("%s: RootCAs is set without a ca_file", method)
			}
			for _, w := range ws {
				if strings.HasPrefix(w.Key, prefix) {
					t.Errorf("%s: a warning for a valid configuration: %s %s", method, w.Key, w.Message)
				}
			}
		}
		// The service as an unwrap-only provider: provider local with a key file and an address.
		src := with(validSource(t),
			"PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS=https://key-service.example.test:8200", "PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=pandokos",
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD=token", "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE="+testutil.WriteSecret(t, "tok", "hvs.synthetic-token-0002"))
		if c, _ := mustLoad(t, src); c.Vault.KEK.Provider != "local" || !c.Vault.KEK.Transit.Configured() {
			t.Errorf("an unwrap-only service: %+v", c.Vault.KEK)
		}
		// Through the file of the configuration.
		src = transitSource(t, "token")
		src.Environ = without(src.Environ, envName("vault.kek.transit.address"), envName("vault.kek.transit.key_name"))
		src.File = writeFile(t, "vault:\n  kek:\n    transit:\n      address: https://from-file.example.test:8200\n      key_name: from-file\n      mount: team/transit\n      namespace: team/a\n")
		c, _ := mustLoad(t, src)
		if tr := c.Vault.KEK.Transit; tr.URL == nil || tr.URL.Host != "from-file.example.test:8200" || tr.KeyName != "from-file" || tr.Mount != "team/transit" || tr.Namespace != "team/a" {
			t.Errorf("from the configuration file: %+v", tr)
		}
	})

	t.Run("provider transit with a key file", func(t *testing.T) {
		src := with(transitSource(t, "token"), envKEKFile+"="+testutil.WriteKEK(t))
		_, _, err := Load(src)
		msg, ok := problemMessage(err, "vault.kek.local.key_file")
		want := "must be empty when vault.kek.provider is transit; list the file in vault.kek.local.unwrap_key_files while data keys are re-wrapped"
		if !ok || msg != want {
			t.Errorf("provider transit with a key file: %q %v", msg, err)
		}
		// The key file moved to the unwrap files is accepted.
		src = with(transitSource(t, "token"), "PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES="+testutil.WriteKEK(t))
		if _, _, err := Load(src); err != nil {
			t.Errorf("an unwrap key file with provider transit: %v", err)
		}
		// Provider transit needs the service.
		src = drop(transitSource(t, "token"), "vault.kek.transit.address", "vault.kek.transit.key_name", "vault.kek.transit.auth.method", "vault.kek.transit.auth.token_file")
		if _, _, err := Load(src); !hasProblem(err, "vault.kek.transit.address") {
			t.Errorf("provider transit without an address: %v", err)
		}
		// Provider local still needs its key file.
		src = drop(validSource(t), "vault.kek.local.key_file")
		if _, _, err := Load(src); !hasProblem(err, "vault.kek.local.key_file") {
			t.Errorf("provider local without a key file: %v", err)
		}
	})

	t.Run("a key set without an address", func(t *testing.T) {
		values := map[string]string{
			"namespace": "team", "mount": "other", "key_name": "pandokos", "tls.ca_file": "/nonexistent/ca.pem", "auth.method": "token",
			"auth.token_file": "/nonexistent/token", "auth.approle.mount": "other", "auth.approle.role_id_file": "/nonexistent/role",
			"auth.approle.secret_id_file": "/nonexistent/secret", "request_timeout": "3s", "max_concurrent_requests": "16", "start_timeout": "1m",
		}
		for key, value := range values {
			src := override(validSource(t), prefix+key, value)
			_, _, err := Load(src)
			if !hasProblem(err, prefix+key) {
				t.Errorf("%s set without an address: %v", prefix+key, err)
			}
		}
	})

	t.Run("the address", func(t *testing.T) {
		bad := map[string]string{
			"http to a host that is not loopback": "http://key-service.example.test:8200",
			"http to a private address":           "http://10.1.2.3:8200",
			"user information":                    "https://user:pw@key-service.example.test:8200",
			"a user name":                         "https://user@key-service.example.test:8200",
			"a path":                              "https://key-service.example.test:8200/v1",
			"a query":                             "https://key-service.example.test:8200?x=1",
			"a fragment":                          "https://key-service.example.test:8200#x",
			"no scheme":                           "key-service.example.test:8200",
			"another scheme":                      "ftp://key-service.example.test",
			"no host":                             "https://",
			"a port out of range":                 "https://key-service.example.test:99999",
			"white space":                         "https://key service.example.test",
		}
		for name, addr := range bad {
			_, _, err := Load(override(transitSource(t, "token"), prefix+"address", addr))
			if msg, ok := problemMessage(err, prefix+"address"); !ok || msg == "" {
				t.Errorf("%s: %v", name, err)
			} else if strings.Contains(msg, "pw") && strings.Contains(addr, "pw") {
				t.Errorf("%s: the message quotes the password: %q", name, msg)
			}
		}
		for _, addr := range []string{"https://key-service.example.test", "https://key-service.example.test:8200", "https://10.1.2.3:8200", "https://[::1]:8200"} {
			if _, _, err := Load(override(transitSource(t, "token"), prefix+"address", addr)); err != nil {
				t.Errorf("%s: %v", addr, err)
			}
		}
		// http for loopback is accepted with a warning.
		for _, addr := range []string{"http://127.0.0.1:8200", "http://localhost:8200", "http://[::1]:8200"} {
			c, ws := mustLoad(t, override(transitSource(t, "token"), prefix+"address", addr))
			found := false
			for _, w := range ws {
				if w.Key == prefix+"address" && w.Message == "the connection to the key service is not encrypted" {
					found = true
				}
			}
			if !found || c.Vault.KEK.Transit.URL == nil || c.Vault.KEK.Transit.URL.Scheme != "http" {
				t.Errorf("%s: warnings %v", addr, ws)
			}
		}
	})

	t.Run("the grammar of the namespace, the mounts and the key name", func(t *testing.T) {
		long := func(n int) string { return strings.Repeat("a", n) }
		cases := []struct {
			key       string
			good, bad []string
		}{
			{"namespace", []string{"team", "team/a-b_c", "A1/B2", long(255)}, []string{"a b", "a//b", "/a", "a/", "a.b", "é", "a\\b", "a:b", long(256)}},
			{"mount", []string{"transit", "team/transit_2", long(128)}, []string{"a b", "a//b", "/a", "a/", "..", "a.b", long(129)}},
			{"auth.approle.mount", []string{"approle", "team/approle-2", long(128)}, []string{"a b", "a//b", "/a", "a/", "..", long(129)}},
			{"key_name", []string{"a", "pandokos", "k.e-y_1", "A9", long(128)}, []string{"-x", ".x", "_x", "a b", "a/b", "a:b", "é", long(129)}},
		}
		for _, c := range cases {
			base := transitSource(t, "token")
			if c.key == "auth.approle.mount" {
				base = transitSource(t, "approle")
			}
			for _, v := range c.good {
				if _, _, err := Load(override(base, prefix+c.key, v)); err != nil {
					t.Errorf("%s=%.20q: %v", c.key, v, err)
				}
			}
			for _, v := range c.bad {
				_, _, err := Load(override(base, prefix+c.key, v))
				if !hasProblem(err, prefix+c.key) {
					t.Errorf("%s=%.20q: %v, want a problem naming the key", c.key, v, err)
				}
			}
		}
		// The key name is required with an address.
		if _, _, err := Load(drop(transitSource(t, "token"), prefix+"key_name")); !hasProblem(err, prefix+"key_name") {
			t.Errorf("no key name: %v", err)
		}
	})

	t.Run("the methods and their files", func(t *testing.T) {
		// A method is required, from the closed list.
		for _, m := range []string{"", "kubernetes", "Token", "AppRole", "ldap"} {
			src := transitSource(t, "token")
			if m == "" {
				src = drop(src, prefix+"auth.method")
			} else {
				src = override(src, prefix+"auth.method", m)
			}
			if _, _, err := Load(src); !hasProblem(err, prefix+"auth.method") {
				t.Errorf("method %q: %v", m, err)
			}
		}
		// Each method needs its files and refuses the other's.
		if _, _, err := Load(drop(transitSource(t, "token"), prefix+"auth.token_file")); !hasProblem(err, prefix+"auth.token_file") {
			t.Errorf("token without a file: %v", err)
		}
		for _, k := range []string{"auth.approle.role_id_file", "auth.approle.secret_id_file"} {
			if _, _, err := Load(drop(transitSource(t, "approle"), prefix+k)); !hasProblem(err, prefix+k) {
				t.Errorf("approle without %s: %v", k, err)
			}
			src := override(transitSource(t, "token"), prefix+k, testutil.WriteSecret(t, "x", "synthetic-0003"))
			if _, _, err := Load(src); !hasProblem(err, prefix+k) {
				t.Errorf("token with %s: %v", k, err)
			}
		}
		src := override(transitSource(t, "approle"), prefix+"auth.token_file", testutil.WriteSecret(t, "x", "hvs.synthetic-0004"))
		if _, _, err := Load(src); !hasProblem(err, prefix+"auth.token_file") {
			t.Errorf("approle with a token file: %v", err)
		}
	})

	t.Run("the durations and the integer at and beyond their bounds", func(t *testing.T) {
		type bound struct {
			key       string
			good, bad []string
		}
		for _, b := range []bound{
			{"request_timeout", []string{"100ms", "2s", "30s"}, []string{"0s", "99ms", "30001ms", "31s", "-1s", "soon", "1h"}},
			{"start_timeout", []string{"0s", "0", "1s", "1h"}, []string{"-1s", "3601s", "1h1s", "2h", "soon"}},
			{"max_concurrent_requests", []string{"1", "32", "1024"}, []string{"0", "-1", "1025", "x", "1.5", "99999"}},
		} {
			for _, v := range b.good {
				if _, _, err := Load(override(transitSource(t, "token"), prefix+b.key, v)); err != nil {
					t.Errorf("%s=%s: %v", b.key, v, err)
				}
			}
			for _, v := range b.bad {
				if _, _, err := Load(override(transitSource(t, "token"), prefix+b.key, v)); !hasProblem(err, prefix+b.key) {
					t.Errorf("%s=%s: want a problem naming the key", b.key, v)
				}
			}
		}
		c, _ := mustLoad(t, override(override(transitSource(t, "token"), prefix+"request_timeout", "250ms"), prefix+"max_concurrent_requests", "7"))
		if tr := c.Vault.KEK.Transit; time.Duration(tr.RequestTimeout) != 250*time.Millisecond || tr.MaxConcurrentRequests != 7 {
			t.Errorf("the values read: %+v", tr)
		}
	})

	t.Run("the secret files and the CA file", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, content string, mode os.FileMode) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(content), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			return p
		}
		const canary = "synthetic-canary-secret-text"
		badFiles := map[string]string{
			"a missing file":      filepath.Join(dir, "gone"),
			"an empty file":       write("empty", "", 0o600),
			"only white space":    write("blank", " \n\t\n", 0o600),
			"a control character": write("ctrl", canary+"\x01more\n", 0o600),
			"too large":           write("large", strings.Repeat("a", 70000), 0o600),
			"a directory":         dir,
		}
		for name, p := range badFiles {
			for _, key := range []string{"auth.token_file"} {
				_, _, err := Load(override(transitSource(t, "token"), prefix+key, p))
				if !hasProblem(err, prefix+key) {
					t.Errorf("%s: %v", name, err)
					continue
				}
				testutil.AssertAbsent(t, "the error for "+name, err.Error(), canary, "more")
			}
			for _, key := range []string{"auth.approle.role_id_file", "auth.approle.secret_id_file"} {
				_, _, err := Load(override(transitSource(t, "approle"), prefix+key, p))
				if !hasProblem(err, prefix+key) {
					t.Errorf("%s (%s): %v", name, key, err)
				}
			}
		}
		// A file that other users can access is a warning, not a refusal, and the warning quotes nothing.
		open := write("open", "hvs.synthetic-open-token\n", 0o644)
		_, ws := mustLoad(t, override(transitSource(t, "token"), prefix+"auth.token_file", open))
		found := false
		for _, w := range ws {
			if w.Key == prefix+"auth.token_file" {
				found = strings.Contains(strings.ToLower(w.Message), "other")
				testutil.AssertAbsent(t, "the warning", w.Message, "hvs.synthetic-open-token")
			}
		}
		if !found {
			t.Errorf("a token file readable by others did not warn: %v", ws)
		}
		// The CA file: PEM with a certificate, or nothing.
		ca := testutil.WriteCA(t)
		c, _ := mustLoad(t, override(transitSource(t, "token"), prefix+"tls.ca_file", ca))
		if c.Vault.KEK.Transit.TLS.RootCAs == nil || c.Vault.KEK.Transit.TLS.CAFile != ca {
			t.Errorf("the CA file was not read: %+v", c.Vault.KEK.Transit.TLS)
		}
		for name, p := range map[string]string{
			"a missing CA file": filepath.Join(dir, "no-ca"), "a CA file that is not PEM": write("notpem", "this is not a certificate "+canary+"\n", 0o644),
			"an empty CA file": write("emptyca", "", 0o644),
		} {
			_, _, err := Load(override(transitSource(t, "token"), prefix+"tls.ca_file", p))
			if !hasProblem(err, prefix+"tls.ca_file") {
				t.Errorf("%s: %v", name, err)
			} else {
				testutil.AssertAbsent(t, "the error for "+name, err.Error(), canary)
			}
		}
	})

	t.Run("migrate and init read none of the keys", func(t *testing.T) {
		junk := []string{
			"PANDOKOS_VAULT_KEK_PROVIDER=transit", "PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS=ftp://nowhere", "PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME=-bad",
			"PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD=ldap", "PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE=/nonexistent/token",
			"PANDOKOS_VAULT_KEK_TRANSIT_REQUEST_TIMEOUT=soon", "PANDOKOS_VAULT_KEK_TRANSIT_MAX_CONCURRENT_REQUESTS=0",
			"PANDOKOS_VAULT_KEK_TRANSIT_TLS_CA_FILE=/nonexistent/ca.pem",
		}
		for name, src := range map[string]Source{"migrate": validMigrateSource(t), "init": validInitSource(t)} {
			src.Environ = append(src.Environ, junk...)
			c, ws := mustLoad(t, src)
			if c.Vault.KEK.Transit.URL != nil || c.Vault.KEK.Transit.TLS.RootCAs != nil {
				t.Errorf("%s read the transit keys: %+v", name, c.Vault.KEK.Transit)
			}
			for _, w := range ws {
				if strings.HasPrefix(w.Key, prefix) || w.Key == "vault.kek.provider" {
					t.Errorf("%s warned about %s: %s", name, w.Key, w.Message)
				}
			}
		}
	})

	t.Run("no message quotes the content of a file", func(t *testing.T) {
		const canary = "synthetic-canary-in-a-file-xyz"
		for _, key := range []string{"auth.token_file", "tls.ca_file"} {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, []byte(canary+"\x02\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, ws, err := Load(override(transitSource(t, "token"), prefix+key, p))
			if err == nil {
				t.Errorf("%s: a file with a control character was accepted", key)
				continue
			}
			testutil.AssertAbsent(t, "the error for "+key, err.Error(), canary)
			for _, w := range ws {
				testutil.AssertAbsent(t, "a warning for "+key, w.Message, canary)
			}
		}
	})
}
