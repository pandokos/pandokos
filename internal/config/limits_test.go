package config

import (
	"strings"
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

// TestQuotaKeys is T-685: the four keys of sprint 5.2, section 5.14: defaults, environment names,
// bounds; an unknown zone and an unknown value name the key; the warning for allow.
func TestQuotaKeys(t *testing.T) {
	envOf := envOfKeys()
	load := func(t *testing.T, kv ...string) (*Config, []Warning, error) {
		t.Helper()
		src := validSource(t)
		for i := 0; i < len(kv); i += 2 {
			src.Environ = append(src.Environ, envOf[kv[i]]+"="+kv[i+1])
		}
		return Load(src)
	}

	t.Run("the keys, their environment names, types and defaults", func(t *testing.T) {
		want := map[string]struct{ env, typ, def string }{
			"limits.time_zone":          {"PANDOKOS_LIMITS_TIME_ZONE", "IANA zone name", "UTC"},
			"limits.on_unknown_price":   {"PANDOKOS_LIMITS_ON_UNKNOWN_PRICE", "refuse, allow", "refuse"},
			"limits.on_store_failure":   {"PANDOKOS_LIMITS_ON_STORE_FAILURE", "refuse, allow", "refuse"},
			"limits.reconcile_interval": {"PANDOKOS_LIMITS_RECONCILE_INTERVAL", "duration", "5m"},
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
		c, ws := mustLoad(t, validSource(t))
		if c.Limits.TimeZone != "UTC" || c.Limits.OnUnknownPrice != "refuse" || c.Limits.OnStoreFailure != "refuse" ||
			time.Duration(c.Limits.ReconcileInterval) != 5*time.Minute || c.Limits.Location == nil || c.Limits.Location.String() != "UTC" {
			t.Errorf("a configuration that sets none of the keys: %+v", c.Limits)
		}
		for _, w := range ws {
			if strings.HasPrefix(w.Key, "limits.") {
				t.Errorf("a warning for the defaults: %+v", w)
			}
		}
	})

	t.Run("limits.time_zone is a zone of the embedded database with whole-minute offsets", func(t *testing.T) {
		for _, bad := range []string{"Mars/Olympus", "europe/rome ", "Local", "UTC+2", "../etc/localtime", "Europe/"} {
			_, _, err := load(t, "limits.time_zone", bad)
			if !hasProblem(err, "limits.time_zone") {
				t.Errorf("%q: %v", bad, err)
			}
		}
		for _, good := range []string{"UTC", "Europe/Rome", "America/St_Johns", "Asia/Kathmandu", "Australia/Lord_Howe", "Pacific/Kiritimati"} {
			c, _, err := load(t, "limits.time_zone", good)
			if err != nil {
				t.Errorf("%s: %v", good, err)
				continue
			}
			if c.Limits.Location == nil || c.Limits.Location.String() != good {
				t.Errorf("%s: parsed as %v", good, c.Limits.Location)
			}
		}
	})

	for _, key := range []string{"limits.on_unknown_price", "limits.on_store_failure"} {
		t.Run(key+" is refuse or allow", func(t *testing.T) {
			for _, bad := range []string{"deny", "Allow", "true", "refuse ", "yes"} {
				_, _, err := load(t, key, bad)
				if !hasProblem(err, key) {
					t.Errorf("%q: %v", bad, err)
				}
			}
			for _, good := range []string{"refuse", "allow"} {
				if _, _, err := load(t, key, good); err != nil {
					t.Errorf("%s: %v", good, err)
				}
			}
		})
	}

	t.Run("limits.reconcile_interval is 10 s to 1 h", func(t *testing.T) {
		for _, bad := range []string{"9s", "0s", "-1m", "61m", "1h1s", "soon", "5"} {
			_, _, err := load(t, "limits.reconcile_interval", bad)
			if !hasProblem(err, "limits.reconcile_interval") {
				t.Errorf("%q: %v", bad, err)
			}
		}
		for good, want := range map[string]time.Duration{"10s": 10 * time.Second, "5m": 5 * time.Minute, "1h": time.Hour, "90s": 90 * time.Second} {
			c, _, err := load(t, "limits.reconcile_interval", good)
			if err != nil || time.Duration(c.Limits.ReconcileInterval) != want {
				t.Errorf("%s: %v %v", good, c, err)
			}
		}
	})

	t.Run("allow on the store failure is a start-up warning, the other values are not", func(t *testing.T) {
		_, ws, err := load(t, "limits.on_store_failure", "allow")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range ws {
			if w.Key == "limits.on_store_failure" && w.Message != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings %+v: none names limits.on_store_failure", ws)
		}
		for _, kv := range [][]string{{"limits.on_store_failure", "refuse"}, {"limits.on_unknown_price", "allow"}} {
			_, ws, err := load(t, kv...)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range ws {
				if w.Key == "limits.on_store_failure" {
					t.Errorf("%v: a warning for %s", kv, w.Key)
				}
			}
		}
	})

	t.Run("every problem is reported with its key", func(t *testing.T) {
		_, _, err := load(t, "limits.time_zone", "Mars/Olympus", "limits.on_unknown_price", "no", "limits.on_store_failure", "no", "limits.reconcile_interval", "1s")
		for _, k := range []string{"limits.time_zone", "limits.on_unknown_price", "limits.on_store_failure", "limits.reconcile_interval"} {
			if !hasProblem(err, k) {
				t.Errorf("no problem for %s in %v", k, err)
			}
		}
	})
}
