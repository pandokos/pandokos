package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// tokenEnv returns the environment variable of a key.
func tokenEnv(t *testing.T, key string) string {
	t.Helper()
	for _, k := range Keys() {
		if k.Name == key {
			return k.Env
		}
	}
	t.Fatalf("no key %s", key)
	return ""
}

func loadTok(t *testing.T, kv ...string) (*Config, []Warning, error) {
	t.Helper()
	src := validSource(t)
	src.Environ = append(src.Environ, kv...)
	return Load(src)
}

func problemText(err error, key string) string {
	var ve *ValidationError
	if errors.As(err, &ve) {
		for _, p := range ve.Problems {
			if p.Key == key {
				return p.Message
			}
		}
	}
	return ""
}

// TestTokens is T-601: the defaults of the tokens section, every bound, the rule that ties the
// size of a token to the header limit, the syntax of the private hosts, and the two warnings.
func TestTokens(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, ws := mustLoad(t, validSource(t))
		tk := c.Tokens
		if !tk.Enabled || tk.MaxTokenBytes != 8192 || time.Duration(tk.ClockSkew) != 60*time.Second || time.Duration(tk.MaxLifetime) != 24*time.Hour ||
			len(tk.PrivateHosts) != 0 || time.Duration(tk.JWKS.FetchTimeout) != 5*time.Second || time.Duration(tk.JWKS.RefreshInterval) != 15*time.Minute ||
			time.Duration(tk.JWKS.MinRefetchInterval) != 10*time.Second || time.Duration(tk.JWKS.MaxStale) != 6*time.Hour {
			t.Errorf("defaults: %+v", tk)
		}
		for _, w := range ws {
			if strings.HasPrefix(w.Key, "tokens.") {
				t.Errorf("the defaults warn: %+v", w)
			}
		}
		if Defaults().Tokens.MaxTokenBytes != 8192 {
			t.Error("Defaults() does not carry the tokens section")
		}
	})

	t.Run("the keys exist with their environment names", func(t *testing.T) {
		for key, env := range map[string]string{
			"tokens.enabled":                   "PANDOKOS_TOKENS_ENABLED",
			"tokens.max_token_bytes":           "PANDOKOS_TOKENS_MAX_TOKEN_BYTES",
			"tokens.clock_skew":                "PANDOKOS_TOKENS_CLOCK_SKEW",
			"tokens.max_lifetime":              "PANDOKOS_TOKENS_MAX_LIFETIME",
			"tokens.private_hosts":             "PANDOKOS_TOKENS_PRIVATE_HOSTS",
			"tokens.jwks.fetch_timeout":        "PANDOKOS_TOKENS_JWKS_FETCH_TIMEOUT",
			"tokens.jwks.refresh_interval":     "PANDOKOS_TOKENS_JWKS_REFRESH_INTERVAL",
			"tokens.jwks.min_refetch_interval": "PANDOKOS_TOKENS_JWKS_MIN_REFETCH_INTERVAL",
			"tokens.jwks.max_stale":            "PANDOKOS_TOKENS_JWKS_MAX_STALE",
		} {
			if got := tokenEnv(t, key); got != env {
				t.Errorf("%s: environment name %q, want %q", key, got, env)
			}
		}
	})

	t.Run("every bound", func(t *testing.T) {
		rows := []struct {
			key, value string
			ok         bool
		}{
			{"tokens.enabled", "false", true},
			{"tokens.enabled", "maybe", false},
			{"tokens.max_token_bytes", "1023", false},
			{"tokens.max_token_bytes", "1024", true},
			{"tokens.max_token_bytes", "4096", true},  // half of the default header limit of 8192? see the header rule below
			{"tokens.max_token_bytes", "16384", true}, // the default header limit is large enough for it
			{"tokens.max_token_bytes", "16385", false},
			{"tokens.max_token_bytes", "0", false},
			{"tokens.clock_skew", "0s", true},
			{"tokens.clock_skew", "5m", true},
			{"tokens.clock_skew", "5m1s", false},
			{"tokens.clock_skew", "-1s", false},
			{"tokens.max_lifetime", "59s", false},
			{"tokens.max_lifetime", "1m", true},
			{"tokens.max_lifetime", "720h", true},
			{"tokens.max_lifetime", "721h", false},
			{"tokens.jwks.fetch_timeout", "999ms", false},
			{"tokens.jwks.fetch_timeout", "1s", true},
			{"tokens.jwks.fetch_timeout", "30s", true},
			{"tokens.jwks.fetch_timeout", "31s", false},
			{"tokens.jwks.refresh_interval", "59s", false},
			{"tokens.jwks.refresh_interval", "1m", true},
			{"tokens.jwks.refresh_interval", "1h", true},
			{"tokens.jwks.refresh_interval", "61m", false},
			{"tokens.jwks.min_refetch_interval", "999ms", false},
			{"tokens.jwks.min_refetch_interval", "1s", true},
			{"tokens.jwks.min_refetch_interval", "5m", true},
			{"tokens.jwks.min_refetch_interval", "301s", false},
			{"tokens.jwks.max_stale", "72h", true},
			{"tokens.jwks.max_stale", "72h1s", false},
			{"tokens.jwks.max_stale", "30m", true}, // twice the default refresh interval
			{"tokens.jwks.max_stale", "29m59s", false},
			{"tokens.jwks.max_stale", "0s", false},
		}
		for _, r := range rows {
			_, _, err := loadTok(t, tokenEnv(t, r.key)+"="+r.value)
			if got := err == nil || !hasProblem(err, r.key); got != r.ok {
				t.Errorf("%s=%s: accepted=%v, want %v (%v)", r.key, r.value, got, r.ok, err)
			}
		}
		// max_stale follows refresh_interval: raising one makes the other's value wrong.
		_, _, err := loadTok(t, "PANDOKOS_TOKENS_JWKS_REFRESH_INTERVAL=1h", "PANDOKOS_TOKENS_JWKS_MAX_STALE=119m")
		if !hasProblem(err, "tokens.jwks.max_stale") {
			t.Errorf("max_stale below twice a refresh interval of 1h: %v", err)
		}
		if _, _, err := loadTok(t, "PANDOKOS_TOKENS_JWKS_REFRESH_INTERVAL=1h", "PANDOKOS_TOKENS_JWKS_MAX_STALE=2h"); err != nil {
			t.Errorf("max_stale of exactly twice the refresh interval: %v", err)
		}
	})

	t.Run("a token is at most half of the header limit", func(t *testing.T) {
		const mtb, mhb = "PANDOKOS_TOKENS_MAX_TOKEN_BYTES", "PANDOKOS_LISTENERS_INFERENCE_MAX_HEADER_BYTES"
		for _, c := range []struct {
			tokenBytes, headerBytes string
			ok                      bool
		}{
			{"8192", "16384", true},
			{"8193", "16384", false},
			{"1024", "4096", true},
			{"2049", "4096", false},
			{"16384", "32768", true},
			{"16384", "32767", false},
			{"8192", "8192", false}, // half of 8192 is 4096
		} {
			_, _, err := loadTok(t, mtb+"="+c.tokenBytes, mhb+"="+c.headerBytes)
			got := err == nil || !hasProblem(err, "tokens.max_token_bytes")
			if got != c.ok {
				t.Errorf("max_token_bytes %s with max_header_bytes %s: accepted=%v, want %v (%v)", c.tokenBytes, c.headerBytes, got, c.ok, err)
			}
		}
		_, _, err := loadTok(t, mtb+"=8193", mhb+"=16384")
		if msg := problemText(err, "tokens.max_token_bytes"); !strings.Contains(msg, "listeners.inference.max_header_bytes") {
			t.Errorf("the message does not name the header limit: %q", msg)
		}
	})

	t.Run("private hosts", func(t *testing.T) {
		ok := []string{"idp.internal.test", "localhost", "a", "a-b.example.com", "idp1.corp", strings.Repeat("a", 63) + ".example", "xn--bcher-kva.example"}
		bad := []string{"", "10.0.0.1", "127.0.0.1", "::1", "[::1]", "*.example.com", "*", "idp.example.com:443", "https://idp.example.com", "idp.example.com/", "-a.example", "a-.example",
			"a..b", ".a", "a.", "a b", "idp_x.example", "idp.example.com,", strings.Repeat("a", 64) + ".example", "1.2.3.4", "123", "1.2.3.4.5", "idp@example.com", "é.example", strings.Repeat("a.", 127) + "a"}
		for _, h := range ok {
			c, _, err := loadTok(t, "PANDOKOS_TOKENS_PRIVATE_HOSTS="+h)
			if err != nil {
				t.Errorf("%q refused: %v", h, err)
				continue
			}
			if len(c.Tokens.PrivateHosts) != 1 || c.Tokens.PrivateHosts[0] != h {
				t.Errorf("%q loaded as %v", h, c.Tokens.PrivateHosts)
			}
		}
		for _, h := range bad {
			if h == "" || h == "idp.example.com," {
				continue // an empty list and a trailing comma are handled below
			}
			_, _, err := loadTok(t, "PANDOKOS_TOKENS_PRIVATE_HOSTS=idp.example.test,"+h)
			if !hasProblem(err, "tokens.private_hosts") {
				t.Errorf("%q accepted: %v", h, err)
			}
			if msg := problemText(err, "tokens.private_hosts"); h != "" && strings.Contains(msg, h) {
				t.Errorf("the message quotes the host %q: %q", h, msg)
			}
		}
		// Lower-cased by Load, in the environment and in a file.
		c, _, err := loadTok(t, "PANDOKOS_TOKENS_PRIVATE_HOSTS=IDP.Internal.TEST, Other.Example")
		if err != nil || strings.Join(c.Tokens.PrivateHosts, ",") != "idp.internal.test,other.example" {
			t.Errorf("lower-casing: %v %v", c.Tokens.PrivateHosts, err)
		}
		src := validSource(t)
		src.File = writeFile(t, "tokens:\n  private_hosts: [Idp.Example.Test]\n")
		c, _ = mustLoad(t, src)
		if len(c.Tokens.PrivateHosts) != 1 || c.Tokens.PrivateHosts[0] != "idp.example.test" {
			t.Errorf("file: %v", c.Tokens.PrivateHosts)
		}
		// At most 32.
		var many []string
		for i := 0; i < 32; i++ {
			many = append(many, "h"+strings.Repeat("a", i%5)+string(rune('a'+i%26))+".example")
		}
		many = uniq(many)
		for len(many) < 33 {
			many = append(many, "extra"+string(rune('a'+len(many)%26))+string(rune('a'+len(many)/26))+".example")
		}
		if _, _, err := loadTok(t, "PANDOKOS_TOKENS_PRIVATE_HOSTS="+strings.Join(many[:32], ",")); err != nil {
			t.Errorf("32 hosts: %v", err)
		}
		if _, _, err := loadTok(t, "PANDOKOS_TOKENS_PRIVATE_HOSTS="+strings.Join(many[:33], ",")); !hasProblem(err, "tokens.private_hosts") {
			t.Errorf("33 hosts: %v", err)
		}
	})

	t.Run("the two warnings", func(t *testing.T) {
		warnings := func(kv ...string) map[string]string {
			_, ws, err := loadTok(t, kv...)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]string{}
			for _, w := range ws {
				if strings.HasPrefix(w.Key, "tokens.") {
					out[w.Key] = w.Message
				}
			}
			return out
		}
		if w := warnings(); len(w) != 0 {
			t.Errorf("defaults warn: %v", w)
		}
		w := warnings("PANDOKOS_TOKENS_PRIVATE_HOSTS=idp.internal.test,other.internal.test")
		msg := w["tokens.private_hosts"]
		if len(w) != 1 || !strings.Contains(msg, "idp.internal.test") || !strings.Contains(msg, "other.internal.test") {
			t.Errorf("private hosts warning: %v", w)
		}
		if w := warnings("PANDOKOS_TOKENS_JWKS_MAX_STALE=24h"); len(w) != 0 {
			t.Errorf("max_stale of exactly 24h warns: %v", w)
		}
		if w := warnings("PANDOKOS_TOKENS_JWKS_MAX_STALE=24h1s"); len(w) != 1 || w["tokens.jwks.max_stale"] == "" {
			t.Errorf("max_stale above 24h: %v", w)
		}
		if w := warnings("PANDOKOS_TOKENS_JWKS_MAX_STALE=48h", "PANDOKOS_TOKENS_PRIVATE_HOSTS=a.example"); len(w) != 2 {
			t.Errorf("both: %v", w)
		}
	})

	t.Run("a file and the environment", func(t *testing.T) {
		src := validSource(t)
		src.File = writeFile(t, "tokens:\n  enabled: false\n  max_token_bytes: 2048\n  clock_skew: 30s\n  jwks:\n    max_stale: 2h\n")
		src.Environ = append(src.Environ, "PANDOKOS_TOKENS_CLOCK_SKEW=10s")
		c, _ := mustLoad(t, src)
		if c.Tokens.Enabled || c.Tokens.MaxTokenBytes != 2048 || time.Duration(c.Tokens.ClockSkew) != 10*time.Second || time.Duration(c.Tokens.JWKS.MaxStale) != 2*time.Hour {
			t.Errorf("%+v", c.Tokens)
		}
		// Unknown keys of the section are refused like any other.
		src.File = writeFile(t, "tokens:\n  audience: x\n")
		if _, _, err := Load(src); err == nil {
			t.Error("an unknown key under tokens was accepted")
		}
	})

	t.Run("not read when the purpose is not serve", func(t *testing.T) {
		src := validInitSource(t)
		src.Environ = append(src.Environ, "PANDOKOS_TOKENS_MAX_TOKEN_BYTES=1")
		if _, _, err := Load(src); err != nil {
			t.Errorf("init refused a tokens setting it does not read: %v", err)
		}
	})
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
