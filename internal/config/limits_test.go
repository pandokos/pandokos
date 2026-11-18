package config

import (
	"testing"
	"time"
)

// TestLimitsKeys is T-641: the two keys of sprint 5.1, section 5.11: defaults, environment names
// and bounds; the error names the key.
func TestLimitsKeys(t *testing.T) {
	envOf := envOfKeys()

	t.Run("the keys, their environment names, types and defaults", func(t *testing.T) {
		want := map[string]struct{ env, typ, def string }{
			"limits.default_output_tokens": {"PANDOKOS_LIMITS_DEFAULT_OUTPUT_TOKENS", "integer", "4096"},
			"cache.limits_ttl":             {"PANDOKOS_CACHE_LIMITS_TTL", "duration", "30s"},
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
			t.Errorf("%d of the two keys are in Keys()", found)
		}
		d := Defaults()
		if d.Limits.DefaultOutputTokens != 4096 || time.Duration(d.Cache.LimitsTTL) != 30*time.Second {
			t.Errorf("defaults: %+v %+v", d.Limits, d.Cache)
		}
		c, _ := mustLoad(t, validSource(t))
		if c.Limits.DefaultOutputTokens != 4096 || time.Duration(c.Cache.LimitsTTL) != 30*time.Second {
			t.Errorf("a configuration that sets neither key: %+v %+v", c.Limits, c.Cache)
		}
	})

	t.Run("limits.default_output_tokens is 1 to 1,000,000", func(t *testing.T) {
		for _, bad := range []string{"0", "-1", "1000001", "10000000", "4096.5", "lots"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["limits.default_output_tokens"]+"="+bad)
			_, _, err := Load(src)
			if !hasProblem(err, "limits.default_output_tokens") {
				t.Errorf("%q: %v", bad, err)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["limits.default_output_tokens"]+"=0")
		_, _, err := Load(src)
		if msg, ok := problemMessage(err, "limits.default_output_tokens"); !ok || msg != "must be between 1 and 1000000" {
			t.Errorf("0: %q %v", msg, err)
		}
		for good, want := range map[string]int64{"1": 1, "1000000": 1000000, "4096": 4096, "8192": 8192} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["limits.default_output_tokens"]+"="+good)
			c, _ := mustLoad(t, src)
			if c.Limits.DefaultOutputTokens != want {
				t.Errorf("%s: %d", good, c.Limits.DefaultOutputTokens)
			}
		}
	})

	t.Run("cache.limits_ttl is 1s to 5m", func(t *testing.T) {
		for _, bad := range []string{"0s", "999ms", "5m1s", "301s", "1h", "-1s", "soon"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.limits_ttl"]+"="+bad)
			_, _, err := Load(src)
			if !hasProblem(err, "cache.limits_ttl") {
				t.Errorf("%s: %v", bad, err)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["cache.limits_ttl"]+"=5m1s")
		_, _, err := Load(src)
		if msg, ok := problemMessage(err, "cache.limits_ttl"); !ok || msg != "must be between 1s and 5m" {
			t.Errorf("5m1s: %q %v", msg, err)
		}
		for good, want := range map[string]time.Duration{"1s": time.Second, "5m": 5 * time.Minute, "300s": 5 * time.Minute, "30s": 30 * time.Second} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.limits_ttl"]+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.Cache.LimitsTTL) != want {
				t.Errorf("%s: %v", good, c.Cache.LimitsTTL)
			}
		}
	})
}
