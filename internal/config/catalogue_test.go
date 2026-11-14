package config

import (
	"strings"
	"testing"
	"time"
)

// TestCatalogueConfiguration is T-383: the five keys of sprint 2.2, section 5.10: defaults,
// bounds, environment names, and the rule that the download URL is https only.
func TestCatalogueConfiguration(t *testing.T) {
	envOf := envOfKeys()

	t.Run("the keys, their environment names, types and defaults", func(t *testing.T) {
		const defaultURL = "https://raw.githubusercontent.com/pydantic/genai-prices/main/prices/new_data/v2/data.json"
		want := map[string]struct{ env, typ, def string }{
			"cache.catalog_ttl":        {"PANDOKOS_CACHE_CATALOG_TTL", "duration", "30s"},
			"pricing.import_at_start":  {"PANDOKOS_PRICING_IMPORT_AT_START", "boolean", "true"},
			"pricing.refresh.enabled":  {"PANDOKOS_PRICING_REFRESH_ENABLED", "boolean", "false"},
			"pricing.refresh.url":      {"PANDOKOS_PRICING_REFRESH_URL", "URL", defaultURL},
			"pricing.refresh.interval": {"PANDOKOS_PRICING_REFRESH_INTERVAL", "duration", "24h"},
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
			t.Errorf("%d of the five keys are in Keys()", found)
		}
		d := Defaults()
		if time.Duration(d.Cache.CatalogTTL) != 30*time.Second || !d.Pricing.ImportAtStart || d.Pricing.Refresh.Enabled ||
			d.Pricing.Refresh.URL != defaultURL || time.Duration(d.Pricing.Refresh.Interval) != 24*time.Hour {
			t.Errorf("defaults: %+v %+v", d.Cache, d.Pricing)
		}
		// The defaults are valid: a serve configuration that sets none of the keys loads.
		c, _ := mustLoad(t, validSource(t))
		if c.Pricing.Refresh.ParsedURL == nil || c.Pricing.Refresh.ParsedURL.String() != defaultURL {
			t.Errorf("ParsedURL = %v, want the default URL parsed", c.Pricing.Refresh.ParsedURL)
		}
	})

	t.Run("cache.catalog_ttl", func(t *testing.T) {
		for _, bad := range []string{"0s", "999ms", "5m1s", "301s", "1h", "-1s"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.catalog_ttl"]+"="+bad)
			_, _, err := Load(src)
			if msg, ok := problemMessage(err, "cache.catalog_ttl"); !ok || msg != "must be between 1s and 5m" {
				t.Errorf("%s: %q %v", bad, msg, err)
			}
		}
		for good, want := range map[string]time.Duration{"1s": time.Second, "5m": 5 * time.Minute, "300s": 5 * time.Minute, "30s": 30 * time.Second} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["cache.catalog_ttl"]+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.Cache.CatalogTTL) != want {
				t.Errorf("%s: %v", good, c.Cache.CatalogTTL)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["cache.catalog_ttl"]+"=soon")
		if _, _, err := Load(src); !hasProblem(err, "cache.catalog_ttl") {
			t.Errorf("a value that is not a duration: %v", err)
		}
	})

	t.Run("pricing.refresh.interval", func(t *testing.T) {
		for _, bad := range []string{"59m", "0s", "30m", "721h", "30d", "-1h", "soon"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["pricing.refresh.interval"]+"="+bad)
			if _, _, err := Load(src); !hasProblem(err, "pricing.refresh.interval") {
				t.Errorf("%s: %v", bad, err)
			}
		}
		for good, want := range map[string]time.Duration{"1h": time.Hour, "720h": 720 * time.Hour, "24h": 24 * time.Hour, "90m": 90 * time.Minute, "3600s": time.Hour} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["pricing.refresh.interval"]+"="+good)
			c, _ := mustLoad(t, src)
			if time.Duration(c.Pricing.Refresh.Interval) != want {
				t.Errorf("%s: %v", good, c.Pricing.Refresh.Interval)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["pricing.refresh.interval"]+"=59m")
		_, _, err := Load(src)
		if msg, ok := problemMessage(err, "pricing.refresh.interval"); !ok || msg != "must be between 1h and 720h" {
			t.Errorf("59m: %q %v", msg, err)
		}
	})

	t.Run("pricing.refresh.url is https, without user information", func(t *testing.T) {
		for name, bad := range map[string]string{
			"http":                  "http://prices.example.com/data.json",
			"a file URL":            "file:///etc/passwd",
			"ftp":                   "ftp://prices.example.com/data.json",
			"user information":      "https://user:synthetic-password@prices.example.com/data.json",
			"a user without a pass": "https://user@prices.example.com/data.json",
			"no host":               "https:///data.json",
			"no scheme":             "prices.example.com/data.json",
			"empty":                 "",
			"a space":               "https://prices.example.com/da ta.json",
		} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf["pricing.refresh.enabled"]+"=true", envOf["pricing.refresh.url"]+"="+bad)
			_, _, err := Load(src)
			if !hasProblem(err, "pricing.refresh.url") {
				t.Errorf("%s: %q accepted (%v)", name, bad, err)
				continue
			}
			// The problem names the rule and never repeats the value.
			if msg, _ := problemMessage(err, "pricing.refresh.url"); strings.Contains(msg, "synthetic-password") || (bad != "" && strings.Contains(err.Error(), bad)) {
				t.Errorf("%s: the problem quotes the URL: %v", name, err)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["pricing.refresh.enabled"]+"=true", envOf["pricing.refresh.url"]+"=https://prices.example.com:8443/dir/data.json")
		c, _ := mustLoad(t, src)
		if !c.Pricing.Refresh.Enabled || c.Pricing.Refresh.ParsedURL == nil || c.Pricing.Refresh.ParsedURL.Host != "prices.example.com:8443" || c.Pricing.Refresh.ParsedURL.Path != "/dir/data.json" {
			t.Errorf("%+v", c.Pricing.Refresh)
		}
	})

	t.Run("the booleans", func(t *testing.T) {
		for _, key := range []string{"pricing.import_at_start", "pricing.refresh.enabled"} {
			src := validSource(t)
			src.Environ = append(src.Environ, envOf[key]+"=maybe")
			if _, _, err := Load(src); !hasProblem(err, key) {
				t.Errorf("%s: a value that is not a boolean: %v", key, err)
			}
		}
		src := validSource(t)
		src.Environ = append(src.Environ, envOf["pricing.import_at_start"]+"=false", envOf["pricing.refresh.enabled"]+"=true")
		c, _ := mustLoad(t, src)
		if c.Pricing.ImportAtStart || !c.Pricing.Refresh.Enabled {
			t.Errorf("%+v", c.Pricing)
		}
	})

	t.Run("the configuration file", func(t *testing.T) {
		src := validSource(t)
		src.File = writeFile(t, "cache:\n  catalog_ttl: 2m\npricing:\n  import_at_start: false\n  refresh:\n    enabled: true\n    url: https://prices.example.com/data.json\n    interval: 12h\n")
		c, _ := mustLoad(t, src)
		if time.Duration(c.Cache.CatalogTTL) != 2*time.Minute || c.Pricing.ImportAtStart || !c.Pricing.Refresh.Enabled ||
			c.Pricing.Refresh.URL != "https://prices.example.com/data.json" || time.Duration(c.Pricing.Refresh.Interval) != 12*time.Hour {
			t.Errorf("%+v %+v", c.Cache, c.Pricing)
		}
		// The environment wins over the file, as for every key.
		src.Environ = append(src.Environ, envOf["cache.catalog_ttl"]+"=45s")
		if c, _ := mustLoad(t, src); time.Duration(c.Cache.CatalogTTL) != 45*time.Second {
			t.Errorf("the environment did not win: %v", c.Cache.CatalogTTL)
		}
		// An unknown member under pricing is refused like any unknown key.
		src = validSource(t)
		src.File = writeFile(t, "pricing:\n  refresh:\n    proxy: http://proxy.example.com\n")
		if _, _, err := Load(src); err == nil {
			t.Error("an unknown key under pricing.refresh was accepted")
		}
	})

	t.Run("migrate and init (and so import-prices) do not validate the download settings", func(t *testing.T) {
		for name, src := range map[string]Source{"migrate": validMigrateSource(t), "init": validInitSource(t)} {
			src.Environ = append(src.Environ, envOf["pricing.refresh.url"]+"=http://invalid.example.com", envOf["pricing.refresh.interval"]+"=1s")
			if _, _, err := Load(src); err != nil {
				t.Errorf("%s: a download setting, which only serve reads, made the load fail: %v", name, err)
			}
		}
	})
}
