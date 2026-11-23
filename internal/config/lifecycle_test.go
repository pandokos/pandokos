package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

const (
	envGrace  = "PANDOKOS_TENANT_DELETION_GRACE_PERIOD"
	envTick   = "PANDOKOS_TENANT_DELETION_INTERVAL"
	envUnwrap = "PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES"
)

// TestLifecycleKeys is T-1008: the three keys of sprint 6.3, section 5.15.
func TestLifecycleKeys(t *testing.T) {
	t.Run("types, defaults and environment names", func(t *testing.T) {
		want := map[string]struct{ env, typ, def string }{
			"tenant_deletion.grace_period":     {envGrace, "duration", "168h"},
			"tenant_deletion.interval":         {envTick, "duration", "1m"},
			"vault.kek.local.unwrap_key_files": {envUnwrap, "list", ""},
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
			t.Errorf("%d of the three keys are in Keys()", found)
		}
		d := Defaults()
		if time.Duration(d.TenantDeletion.GracePeriod) != 168*time.Hour || time.Duration(d.TenantDeletion.Interval) != time.Minute ||
			len(d.Vault.KEK.Local.UnwrapKeyFiles) != 0 || len(d.Vault.KEK.Local.UnwrapKeys) != 0 {
			t.Errorf("defaults: %+v %+v", d.TenantDeletion, d.Vault.KEK.Local)
		}
	})

	t.Run("tenant_deletion.grace_period", func(t *testing.T) {
		for _, bad := range []string{"-1s", "2161h", "91d", "later"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envGrace+"="+bad)
			_, _, err := Load(src)
			if !hasProblem(err, "tenant_deletion.grace_period") {
				t.Errorf("%s: %v", bad, err)
			}
		}
		if msg, ok := func() (string, bool) {
			src := validSource(t)
			src.Environ = append(src.Environ, envGrace+"=2161h")
			_, _, err := Load(src)
			return problemMessage(err, "tenant_deletion.grace_period")
		}(); !ok || msg != "must be between 0 and 2160h" {
			t.Errorf("the message is %q", msg)
		}
		for good, want := range map[string]time.Duration{"0s": 0, "1h": time.Hour, "168h": 168 * time.Hour, "2160h": 2160 * time.Hour, "90h30m": 90*time.Hour + 30*time.Minute} {
			src := validSource(t)
			src.Environ = append(src.Environ, envGrace+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.TenantDeletion.GracePeriod) != want {
				t.Errorf("%s: %v", good, c.TenantDeletion.GracePeriod)
			}
		}
	})

	t.Run("tenant_deletion.interval", func(t *testing.T) {
		for _, bad := range []string{"0s", "9s", "9999ms", "61m", "1h1s", "-1m", "often"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envTick+"="+bad)
			_, _, err := Load(src)
			if msg, ok := problemMessage(err, "tenant_deletion.interval"); !ok || (bad != "often" && msg != "must be between 10s and 1h") {
				t.Errorf("%s: %q %v", bad, msg, err)
			}
		}
		for good, want := range map[string]time.Duration{"10s": 10 * time.Second, "1m": time.Minute, "1h": time.Hour, "90s": 90 * time.Second} {
			src := validSource(t)
			src.Environ = append(src.Environ, envTick+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.TenantDeletion.Interval) != want {
				t.Errorf("%s: %v", good, c.TenantDeletion.Interval)
			}
		}
		// The file of the configuration too.
		src := validSource(t)
		src.File = writeFile(t, "tenant_deletion:\n  grace_period: 24h\n  interval: 30s\n")
		c, _ := mustLoad(t, src)
		if time.Duration(c.TenantDeletion.GracePeriod) != 24*time.Hour || time.Duration(c.TenantDeletion.Interval) != 30*time.Second {
			t.Errorf("from the file: %+v", c.TenantDeletion)
		}
	})

	t.Run("they are keys of serve only", func(t *testing.T) {
		for name, src := range map[string]Source{"migrate": validMigrateSource(t), "init": validInitSource(t)} {
			src.Environ = append(src.Environ, envGrace+"=-1s", envTick+"=1s", envUnwrap+"=/nonexistent/old-kek")
			c, ws := mustLoad(t, src)
			if len(c.Vault.KEK.Local.UnwrapKeys) != 0 {
				t.Errorf("%s read an unwrap key", name)
			}
			for _, w := range ws {
				if w.Key == "vault.kek.local.unwrap_key_files" {
					t.Errorf("%s warned about the unwrap key files: %s", name, w.Message)
				}
			}
		}
	})

	t.Run("unwrap key files are read as secrets in the order given", func(t *testing.T) {
		l1, l2 := keyLine(t, 0x40), keyLine(t, 0x50)
		f1, f2 := testutil.WriteSecret(t, "old1", l1), writeFile(t, "\t"+l2+"\r\n\r\n")
		src := validSource(t)
		src.Environ = append(src.Environ, envUnwrap+"="+f1+","+f2)
		c, _ := mustLoad(t, src)
		if got := c.Vault.KEK.Local.UnwrapKeyFiles; len(got) != 2 || got[0] != f1 || got[1] != f2 {
			t.Errorf("UnwrapKeyFiles = %v", got)
		}
		if got := c.Vault.KEK.Local.UnwrapKeys; len(got) != 2 || got[0].Reveal() != l1 || got[1].Reveal() != l2 {
			t.Errorf("the unwrap keys were not read in the order of the files (or not trimmed)")
		}
		// The key file of the configuration, as a list.
		src = validSource(t)
		src.File = writeFile(t, "vault:\n  kek:\n    local:\n      unwrap_key_files:\n        - "+f1+"\n        - "+f2+"\n")
		if c, _ := mustLoad(t, src); len(c.Vault.KEK.Local.UnwrapKeys) != 2 {
			t.Error("the list in the file of the configuration was not read")
		}
		// None: the default.
		if c, _ := mustLoad(t, validSource(t)); len(c.Vault.KEK.Local.UnwrapKeys) != 0 {
			t.Error("an unwrap key without a file")
		}
	})

	t.Run("a file readable by others warns for that file", func(t *testing.T) {
		line := keyLine(t, 0x60)
		closed, open := testutil.WriteSecret(t, "closed", line), filepath.Join(t.TempDir(), "open")
		if err := os.WriteFile(open, []byte(keyLine(t, 0x70)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(open, 0o644); err != nil {
			t.Fatal(err)
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envUnwrap+"="+closed+","+open)
		_, ws := mustLoad(t, src)
		var n int
		for _, w := range ws {
			if w.Key == "vault.kek.local.unwrap_key_files" {
				n++
				if !strings.Contains(strings.ToLower(w.Message), "other") {
					t.Errorf("the warning %q does not say that other users can access the file", w.Message)
				}
				testutil.AssertAbsent(t, "the warning", w.Message, testutil.SecretForms(line)...)
			}
		}
		if n != 1 {
			t.Errorf("%d warnings about the unwrap key files, want one for the file readable by others", n)
		}
	})

	t.Run("what is refused and what the errors quote", func(t *testing.T) {
		line := keyLine(t, 0x80)
		good := testutil.WriteSecret(t, "good", line)
		sameKeyElsewhere := testutil.WriteSecret(t, "same", line)
		mine := testutil.WriteKEK(t)
		mineLine, _ := os.ReadFile(mine)
		nine := make([]string, 9)
		for i := range nine {
			nine[i] = testutil.WriteSecret(t, "nine", keyLine(t, byte(0x90+i)))
		}
		garbage := writeFile(t, "this-is-not-a-key-recognisable-canary-text\n")
		cases := map[string][]string{
			"a path twice":               {good, good},
			"the same key in two files":  {good, sameKeyElsewhere},
			"the path of the key file":   {mine},
			"the key of the key file":    {testutil.WriteSecret(t, "copy-of-mine", strings.TrimSpace(string(mineLine)))},
			"nine files":                 nine,
			"a file that is not there":   {"/nonexistent/old-kek"},
			"a file that is not a key":   {garbage},
			"a key among the valid ones": {good, garbage},
			"an empty file in the list":  {good, writeFile(t, "")},
			"a directory in the list":    {t.TempDir()},
		}
		for name, files := range cases {
			src := validSource(t)
			src.Environ = append(without(src.Environ, envKEKFile), envKEKFile+"="+mine, envUnwrap+"="+strings.Join(files, ","))
			_, _, err := Load(src)
			if err == nil || !hasProblem(err, "vault.kek.local.unwrap_key_files") {
				t.Errorf("%s: %v, want a problem on vault.kek.local.unwrap_key_files", name, err)
				continue
			}
			testutil.AssertAbsent(t, "the error of "+name, err.Error(), line, strings.TrimSpace(string(mineLine)), "recognisable", "canary")
		}
		// Nine is refused for its number, with the rule in the message.
		src := validSource(t)
		src.Environ = append(src.Environ, envUnwrap+"="+strings.Join(nine, ","))
		_, _, err := Load(src)
		if msg, ok := problemMessage(err, "vault.kek.local.unwrap_key_files"); !ok || !strings.Contains(msg, "at most 8") {
			t.Errorf("nine files: %q %v", msg, err)
		}
		// Eight are accepted.
		src = validSource(t)
		src.Environ = append(src.Environ, envUnwrap+"="+strings.Join(nine[:8], ","))
		if c, _ := mustLoad(t, src); len(c.Vault.KEK.Local.UnwrapKeys) != 8 {
			t.Error("eight files were not accepted")
		}
	})
}
