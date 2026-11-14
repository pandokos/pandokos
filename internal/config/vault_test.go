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
			"vault.kek.provider":       {"PANDOKOS_VAULT_KEK_PROVIDER", "local", "local"},
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
			if msg, ok := problemMessage(err, "vault.kek.provider"); !ok || msg != "must be local" {
				t.Errorf("%q: %q %v", bad, msg, err)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["vault.kek.provider"]+"=local")
		if c, _ := mustLoad(t, src); c.Vault.KEK.Provider != "local" {
			t.Errorf("provider %q", c.Vault.KEK.Provider)
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
