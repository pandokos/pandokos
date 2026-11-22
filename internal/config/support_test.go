package config

import (
	"testing"
	"time"
)

// TestSupport is T-886: the configuration of support actions (sprint 4.3, section 5.10).
func TestSupport(t *testing.T) {
	envOf := map[string]Key{}
	for _, k := range Keys() {
		envOf[k.Name] = k
	}
	t.Run("the keys, their environment names and defaults", func(t *testing.T) {
		for name, env := range map[string]string{"support.enabled": "PANDOKOS_SUPPORT_ENABLED", "support.max_duration": "PANDOKOS_SUPPORT_MAX_DURATION"} {
			k, ok := envOf[name]
			if !ok || k.Env != env {
				t.Errorf("%s: %+v, want the environment name %s", name, k, env)
			}
		}
		if envOf["support.enabled"].Default != "true" || envOf["support.max_duration"].Default != "1h" {
			t.Errorf("defaults %q and %q, want true and 1h", envOf["support.enabled"].Default, envOf["support.max_duration"].Default)
		}
		c, ws := mustLoad(t, validSource(t))
		if !c.Support.Enabled || time.Duration(c.Support.MaxDuration) != time.Hour {
			t.Errorf("the loaded defaults: %+v", c.Support)
		}
		for _, w := range ws {
			if w.Key == "support.enabled" {
				t.Errorf("a warning with support actions on: %s", w.Message)
			}
		}
		if d := Defaults(); !d.Support.Enabled || time.Duration(d.Support.MaxDuration) != time.Hour {
			t.Errorf("Defaults(): %+v", d.Support)
		}
	})
	t.Run("from the file and from the environment, which wins", func(t *testing.T) {
		src := validSource(t)
		src.File = writeFile(t, "support:\n  enabled: false\n  max_duration: 2h\n")
		c, _ := mustLoad(t, src)
		if c.Support.Enabled || time.Duration(c.Support.MaxDuration) != 2*time.Hour {
			t.Errorf("from the file: %+v", c.Support)
		}
		src.Environ = append(src.Environ, "PANDOKOS_SUPPORT_ENABLED=true", "PANDOKOS_SUPPORT_MAX_DURATION=30m")
		c, _ = mustLoad(t, src)
		if !c.Support.Enabled || time.Duration(c.Support.MaxDuration) != 30*time.Minute {
			t.Errorf("from the environment: %+v", c.Support)
		}
	})
	t.Run("the bounds of the duration: 1 minute to 24 hours", func(t *testing.T) {
		for v, valid := range map[string]bool{"59s": false, "0s": false, "-1h": false, "24h1s": false, "48h": false, "1m": true, "90m": true, "24h": true} {
			src := validSource(t)
			src.Environ = append(src.Environ, "PANDOKOS_SUPPORT_MAX_DURATION="+v)
			_, _, err := Load(src)
			if valid && err != nil {
				t.Errorf("%s refused: %v", v, err)
			}
			if !valid && (err == nil || !hasProblem(err, "support.max_duration")) {
				t.Errorf("%s accepted or refused under another key: %v", v, err)
			}
		}
	})
	t.Run("a value that is not a duration or a boolean", func(t *testing.T) {
		for kv, key := range map[string]string{"PANDOKOS_SUPPORT_MAX_DURATION=soon": "support.max_duration", "PANDOKOS_SUPPORT_ENABLED=maybe": "support.enabled"} {
			src := validSource(t)
			src.Environ = append(src.Environ, kv)
			if _, _, err := Load(src); err == nil || !hasProblem(err, key) {
				t.Errorf("%s: %v, want a problem under %s", kv, err, key)
			}
		}
	})
	t.Run("the warning with the switch off", func(t *testing.T) {
		src := validSource(t)
		src.Environ = append(src.Environ, "PANDOKOS_SUPPORT_ENABLED=false")
		_, ws := mustLoad(t, src)
		found := false
		for _, w := range ws {
			if w.Key == "support.enabled" {
				found = true
				if w.Message != "support actions are off: a tenant that loses its administrators cannot be recovered through the API" {
					t.Errorf("the warning says %q", w.Message)
				}
			}
		}
		if !found {
			t.Error("no warning with support.enabled false")
		}
	})
}
