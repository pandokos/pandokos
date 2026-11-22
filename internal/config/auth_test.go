package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/testutil"
)

// TestAuth is T-830: the defaults, the bounds and the cross rules of section 5.15, the syntax of the
// origin and of the mappings, the warnings, and the client secret.
func TestAuth(t *testing.T) {
	load := func(kv ...string) (*Config, map[string]string, error) {
		t.Helper()
		src := validSource(t)
		src.Environ = append(src.Environ, kv...)
		c, ws, err := Load(src)
		out := map[string]string{}
		for _, w := range ws {
			out[w.Key] = w.Message
		}
		return c, out, err
	}
	problem := func(err error, key string) bool {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			return false
		}
		for _, p := range ve.Problems {
			if p.Key == key {
				return true
			}
		}
		return false
	}
	// refused and accepted check a value of one key, with the extra variables it needs.
	refused := func(key, env string, extra []string, values ...string) {
		t.Helper()
		for _, v := range values {
			if _, _, err := load(append([]string{env + "=" + v}, extra...)...); !problem(err, key) {
				t.Errorf("%s=%q was accepted (err %v)", key, v, err)
			}
		}
	}
	accepted := func(key, env string, extra []string, values ...string) {
		t.Helper()
		for _, v := range values {
			if _, _, err := load(append([]string{env + "=" + v}, extra...)...); err != nil {
				t.Errorf("%s=%q was refused: %v", key, v, err)
			}
		}
	}

	t.Run("defaults", func(t *testing.T) {
		c, ws, err := load()
		if err != nil {
			t.Fatal(err)
		}
		a := c.Auth
		if a.PublicOrigin != "" || !a.Local.Enabled || a.Local.PasswordMinLength != 12 || time.Duration(a.Local.GeneratedPasswordTTL) != 72*time.Hour ||
			a.Local.MaxFailures != 10 || time.Duration(a.Local.FailureWindow) != 15*time.Minute || time.Duration(a.Local.LockDuration) != 15*time.Minute ||
			a.Local.SourceMaxFailures != 50 || a.Local.Argon2 != (AuthArgon2{MemoryKiB: 65536, Iterations: 3, Parallelism: 1}) || a.Local.MaxConcurrentHashes != 4 ||
			time.Duration(a.Session.IdleTimeout) != 30*time.Minute || time.Duration(a.Session.AbsoluteLifetime) != 12*time.Hour || a.Session.MaxPerUser != 10 ||
			a.OIDC.Enabled || a.OIDC.ClientAuth != "basic" || strings.Join(a.OIDC.Scopes, " ") != "openid profile email" || strings.Join(a.OIDC.Algorithms, " ") != "RS256 ES256" ||
			a.OIDC.DisplayName != "Single sign-on" || a.OIDC.NameClaim != "name" || a.OIDC.EmailClaim != "email" || a.OIDC.GroupsClaim != "groups" ||
			len(a.OIDC.GroupMappings) != 0 || a.OIDC.AutoProvision || len(a.OIDC.PrivateHosts) != 0 || a.OIDC.PostSignInPath != "/" ||
			len(c.Listeners.Admin.TrustedProxies) != 0 || c.Tokens.AdminListener || time.Duration(c.Idempotency.Retention) != 24*time.Hour {
			t.Errorf("defaults %+v", c.Auth)
		}
		for key, msg := range ws {
			if strings.HasPrefix(key, "auth.") {
				t.Errorf("a warning about %s with the defaults: %s", key, msg)
			}
		}
	})

	t.Run("bounds of the local accounts", func(t *testing.T) {
		const p = "PANDOKOS_AUTH_LOCAL_"
		accepted("auth.local.password_min_length", p+"PASSWORD_MIN_LENGTH", nil, "8", "12", "128")
		refused("auth.local.password_min_length", p+"PASSWORD_MIN_LENGTH", nil, "7", "129", "0", "-1", "twelve")
		accepted("auth.local.generated_password_ttl", p+"GENERATED_PASSWORD_TTL", nil, "1h", "72h", "720h")
		refused("auth.local.generated_password_ttl", p+"GENERATED_PASSWORD_TTL", nil, "59m", "721h", "0s", "soon")
		accepted("auth.local.max_failures", p+"MAX_FAILURES", []string{p + "SOURCE_MAX_FAILURES=100"}, "3", "10", "100")
		refused("auth.local.max_failures", p+"MAX_FAILURES", nil, "2", "101", "0")
		accepted("auth.local.failure_window", p+"FAILURE_WINDOW", nil, "1m", "15m", "24h")
		refused("auth.local.failure_window", p+"FAILURE_WINDOW", nil, "59s", "25h", "0s")
		accepted("auth.local.lock_duration", p+"LOCK_DURATION", nil, "1m", "15m", "24h")
		refused("auth.local.lock_duration", p+"LOCK_DURATION", nil, "59s", "25h", "0s")
		accepted("auth.local.source_max_failures", p+"SOURCE_MAX_FAILURES", nil, "10", "50", "1000")
		refused("auth.local.source_max_failures", p+"SOURCE_MAX_FAILURES", nil, "9", "0")
		if _, _, err := load(p+"MAX_FAILURES=20", p+"SOURCE_MAX_FAILURES=19"); !problem(err, "auth.local.source_max_failures") {
			t.Errorf("a source limit below the account limit: %v", err)
		}
		accepted("auth.local.argon2.memory_kib", p+"ARGON2_MEMORY_KIB", nil, "19456", "65536", "1048576")
		refused("auth.local.argon2.memory_kib", p+"ARGON2_MEMORY_KIB", nil, "19455", "0", "1048577")
		accepted("auth.local.argon2.iterations", p+"ARGON2_ITERATIONS", nil, "2", "3", "10")
		refused("auth.local.argon2.iterations", p+"ARGON2_ITERATIONS", nil, "1", "0", "11")
		accepted("auth.local.argon2.parallelism", p+"ARGON2_PARALLELISM", nil, "1", "4")
		refused("auth.local.argon2.parallelism", p+"ARGON2_PARALLELISM", nil, "0", "5")
		accepted("auth.local.max_concurrent_hashes", p+"MAX_CONCURRENT_HASHES", nil, "1", "4", "64")
		refused("auth.local.max_concurrent_hashes", p+"MAX_CONCURRENT_HASHES", nil, "0", "65")
		if _, _, err := load(p + "ENABLED=false"); !problem(err, "auth.local.enabled") {
			t.Errorf("local accounts off without the identity provider: %v", err)
		}
	})

	t.Run("sessions", func(t *testing.T) {
		const p = "PANDOKOS_AUTH_SESSION_"
		accepted("auth.session.idle_timeout", p+"IDLE_TIMEOUT", nil, "1m", "30m", "12h")
		refused("auth.session.idle_timeout", p+"IDLE_TIMEOUT", nil, "59s", "25h", "0s")
		accepted("auth.session.absolute_lifetime", p+"ABSOLUTE_LIFETIME", nil, "30m", "12h", "168h")
		refused("auth.session.absolute_lifetime", p+"ABSOLUTE_LIFETIME", nil, "169h", "10m", "0s")
		if _, _, err := load(p+"IDLE_TIMEOUT=13h", p+"ABSOLUTE_LIFETIME=12h"); !problem(err, "auth.session.idle_timeout") && !problem(err, "auth.session.absolute_lifetime") {
			t.Errorf("an idle timeout above the absolute lifetime: %v", err)
		}
		accepted("auth.session.max_per_user", p+"MAX_PER_USER", nil, "1", "10", "100")
		refused("auth.session.max_per_user", p+"MAX_PER_USER", nil, "0", "101")
	})

	t.Run("the origin", func(_ *testing.T) {
		accepted("auth.public_origin", "PANDOKOS_AUTH_PUBLIC_ORIGIN", nil,
			"https://admin.example.test", "https://admin.example.test:8443", "http://127.0.0.1:8081", "http://localhost:8081", "http://[::1]:8081", "https://[2001:db8::1]:8443")
		refused("auth.public_origin", "PANDOKOS_AUTH_PUBLIC_ORIGIN", nil,
			"http://admin.example.test", "admin.example.test", "https://admin.example.test/", "https://admin.example.test/path", "https://admin.example.test?x=1",
			"https://admin.example.test#f", "https://user@admin.example.test", "ftp://admin.example.test", "https://", "https://a b.example.test", "HTTPS://ADMIN.EXAMPLE.TEST",
			"http://10.1.2.3:8081", "https://admin.example.test:0", "https://admin.example.test:99999")
	})

	t.Run("trusted proxies", func(t *testing.T) {
		accepted("listeners.admin.trusted_proxies", "PANDOKOS_LISTENERS_ADMIN_TRUSTED_PROXIES", nil, "10.0.0.0/8", "10.0.0.0/8,2001:db8::/32", "192.0.2.1/32")
		refused("listeners.admin.trusted_proxies", "PANDOKOS_LISTENERS_ADMIN_TRUSTED_PROXIES", nil, "10.0.0.1", "garbage", "10.0.0.0/33", "10.0.0.0/8,", "10.0.0.0/8,garbage")
		c, _, err := load("PANDOKOS_LISTENERS_ADMIN_TRUSTED_PROXIES=10.0.0.0/8,2001:db8::/32")
		if err != nil || len(c.Listeners.Admin.TrustedProxies) != 2 {
			t.Errorf("%v %v", err, c)
		}
	})

	t.Run("tokens and idempotency", func(t *testing.T) {
		if _, _, err := load("PANDOKOS_TOKENS_ADMIN_LISTENER=true"); !problem(err, "tokens.admin_listener") {
			t.Errorf("tokens on the administration listener without tokens: %v", err)
		}
		if _, _, err := load("PANDOKOS_TOKENS_ENABLED=true", "PANDOKOS_TOKENS_ADMIN_LISTENER=true"); err != nil {
			t.Errorf("with tokens.enabled: %v", err)
		}
		accepted("idempotency.retention", "PANDOKOS_IDEMPOTENCY_RETENTION", nil, "1h", "24h", "168h")
		refused("idempotency.retention", "PANDOKOS_IDEMPOTENCY_RETENTION", nil, "59m", "169h", "0s")
	})

	t.Run("the identity provider", func(t *testing.T) {
		secretFile := testutil.WriteSecret(t, "oidc", "a-client-secret-of-enough-bytes")
		on := []string{
			"PANDOKOS_AUTH_OIDC_ENABLED=true", "PANDOKOS_AUTH_OIDC_ISSUER=https://idp.example.test/realms/acme", "PANDOKOS_AUTH_OIDC_CLIENT_ID=pandokos",
			"PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + secretFile, "PANDOKOS_AUTH_PUBLIC_ORIGIN=https://admin.example.test",
		}
		without := func(name string) []string {
			var out []string
			for _, kv := range on {
				if !strings.HasPrefix(kv, name+"=") {
					out = append(out, kv)
				}
			}
			return out
		}
		c, _, err := load(on...)
		if err != nil {
			t.Fatalf("a complete configuration: %v", err)
		}
		if c.Auth.OIDC.ClientSecret.Reveal() != "a-client-secret-of-enough-bytes" {
			t.Error("the client secret was not read from its file")
		}
		for name, key := range map[string]string{
			"PANDOKOS_AUTH_OIDC_ISSUER": "auth.oidc.issuer", "PANDOKOS_AUTH_OIDC_CLIENT_ID": "auth.oidc.client_id",
			"PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE": "auth.oidc.client_secret_file", "PANDOKOS_AUTH_PUBLIC_ORIGIN": "auth.public_origin",
		} {
			if _, _, err := load(without(name)...); !problem(err, key) {
				t.Errorf("OIDC enabled without %s: %v", key, err)
			}
		}
		with := func(kv ...string) []string { return append(append([]string{}, on...), kv...) }
		for _, bad := range []string{"http://idp.example.test", "idp.example.test", "https://idp.example.test/?x=1", "https://"} {
			if _, _, err := load(with("PANDOKOS_AUTH_OIDC_ISSUER=" + bad)...); !problem(err, "auth.oidc.issuer") {
				t.Errorf("issuer %q: %v", bad, err)
			}
		}
		short := testutil.WriteSecret(t, "short", "too-short-1234")
		if _, _, err := load(with("PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + short)...); !problem(err, "auth.oidc.client_secret_file") {
			t.Errorf("a client secret of 14 bytes: %v", err)
		} else if strings.Contains(err.Error(), "too-short-1234") {
			t.Error("the problem quotes the secret")
		}
		if _, _, err := load(with("PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + t.TempDir() + "/absent")...); !problem(err, "auth.oidc.client_secret_file") {
			t.Errorf("a client secret file that does not exist: %v", err)
		}
		accepted("auth.oidc.client_auth", "PANDOKOS_AUTH_OIDC_CLIENT_AUTH", on, "basic", "post")
		refused("auth.oidc.client_auth", "PANDOKOS_AUTH_OIDC_CLIENT_AUTH", on, "none", "private_key_jwt", "")
		accepted("auth.oidc.scopes", "PANDOKOS_AUTH_OIDC_SCOPES", on, "openid", "openid profile", "profile,openid,email")
		refused("auth.oidc.scopes", "PANDOKOS_AUTH_OIDC_SCOPES", on, "profile email", "")
		accepted("auth.oidc.algorithms", "PANDOKOS_AUTH_OIDC_ALGORITHMS", on, "RS256", "ES256 PS256", "EdDSA,RS256")
		refused("auth.oidc.algorithms", "PANDOKOS_AUTH_OIDC_ALGORITHMS", on, "HS256", "none", "RS256 HS512", "rs256", "")
		accepted("auth.oidc.post_sign_in_path", "PANDOKOS_AUTH_OIDC_POST_SIGN_IN_PATH", on, "/", "/console", "/a/b?c=d")
		refused("auth.oidc.post_sign_in_path", "PANDOKOS_AUTH_OIDC_POST_SIGN_IN_PATH", on, "console", "//evil.example.test", "/a//b", `/a\b`, "https://evil.example.test/", "/a\nb", "", "/a\tb")
		accepted("auth.oidc.group_mappings", "PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS", on,
			"platform:platform-admin=admins", "platform:platform-admin=cn=admins,ou=x", "tenant:acme:tenant-admin=eng=ops:team")
		refused("auth.oidc.group_mappings", "PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS", on,
			"admins", "platform:platform-admin", "platform:platform-admin=", "platform:inference=x", "platform:tenant-admin=x", "platform:ghost=x",
			"tenant:acme:platform-admin=x", "tenant:acme:inference=x", "tenant:acme=x", "other:platform-admin=x", "tenant::tenant-admin=x")
		if _, _, err := load(with("PANDOKOS_AUTH_OIDC_AUTO_PROVISION=true")...); !problem(err, "auth.oidc.auto_provision") {
			t.Errorf("auto_provision without mappings: %v", err)
		}
		if _, _, err := load(with("PANDOKOS_AUTH_OIDC_AUTO_PROVISION=true", "PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS=platform:platform-admin=admins")...); err != nil {
			t.Errorf("auto_provision with mappings: %v", err)
		}
		if c, _, err := load(with("PANDOKOS_AUTH_OIDC_PRIVATE_HOSTS=idp.internal.test,keycloak")...); err != nil || len(c.Auth.OIDC.PrivateHosts) != 2 {
			t.Errorf("private hosts: %v", err)
		}
		// With the provider on, local accounts may be switched off.
		if _, _, err := load(with("PANDOKOS_AUTH_LOCAL_ENABLED=false")...); err != nil {
			t.Errorf("local accounts off with the provider on: %v", err)
		}
		// The provider's keys are not read when it is off, nor by commands that do not serve.
		if _, _, err := load("PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + t.TempDir() + "/absent"); err != nil {
			t.Errorf("a secret file named while the provider is off: %v", err)
		}
	})

	t.Run("the three warnings", func(t *testing.T) {
		secretFile := testutil.WriteSecret(t, "oidc", "a-client-secret-of-enough-bytes")
		oidc := []string{
			"PANDOKOS_AUTH_OIDC_ENABLED=true", "PANDOKOS_AUTH_OIDC_ISSUER=https://idp.example.test/", "PANDOKOS_AUTH_OIDC_CLIENT_ID=pandokos",
			"PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE=" + secretFile, "PANDOKOS_AUTH_PUBLIC_ORIGIN=https://admin.example.test",
		}
		if _, ws, err := load("PANDOKOS_AUTH_LOCAL_PASSWORD_MIN_LENGTH=10"); err != nil || ws["auth.local.password_min_length"] == "" {
			t.Errorf("a minimum length below 12: %v %v", err, ws)
		}
		if _, ws, err := load("PANDOKOS_AUTH_LOCAL_PASSWORD_MIN_LENGTH=12"); err != nil || ws["auth.local.password_min_length"] != "" {
			t.Errorf("a minimum length of 12 warned: %v %v", err, ws)
		}
		_, ws, err := load("PANDOKOS_LISTENERS_ADMIN_ADDRESS=0.0.0.0:8081", "PANDOKOS_LISTENERS_ADMIN_ALLOW_PLAINTEXT=true")
		if err != nil || ws["auth.public_origin"] == "" || !strings.Contains(ws["auth.public_origin"], "auth.public_origin") && !strings.Contains(ws["auth.public_origin"], "origin") {
			t.Errorf("allow_plaintext without auth.public_origin: %v %v", err, ws)
		}
		_, ws, err = load("PANDOKOS_LISTENERS_ADMIN_ADDRESS=0.0.0.0:8081", "PANDOKOS_LISTENERS_ADMIN_ALLOW_PLAINTEXT=true", "PANDOKOS_AUTH_PUBLIC_ORIGIN=https://admin.example.test")
		if err != nil || ws["auth.public_origin"] != "" {
			t.Errorf("allow_plaintext with the origin named warned: %v %v", err, ws)
		}
		_, ws, err = load(oidc...)
		if err != nil || ws["auth.oidc.group_mappings"] == "" {
			t.Errorf("the provider without group mappings and without auto_provision: %v %v", err, ws)
		}
		_, ws, err = load(append(oidc, "PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS=platform:platform-admin=admins")...)
		if err != nil || ws["auth.oidc.group_mappings"] != "" {
			t.Errorf("the provider with group mappings warned: %v %v", err, ws)
		}
	})

	t.Run("the client secret is a secret", func(t *testing.T) {
		const value = "canary-client-secret-0d6f3a1b9c2e47aa"
		secretFile := testutil.WriteSecret(t, "oidc", value)
		src := validSource(t)
		src.Environ = append(src.Environ,
			"PANDOKOS_AUTH_OIDC_ENABLED=true", "PANDOKOS_AUTH_OIDC_ISSUER=https://idp.example.test/", "PANDOKOS_AUTH_OIDC_CLIENT_ID=pandokos",
			"PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE="+secretFile, "PANDOKOS_AUTH_PUBLIC_ORIGIN=https://admin.example.test")
		c, ws, err := Load(src)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmtAll(c), value) {
			t.Error("the configuration prints its client secret")
		}
		for _, w := range ws {
			if strings.Contains(w.Message, value) {
				t.Errorf("a warning prints the client secret: %s", w.Message)
			}
		}
		b, _ := os.ReadFile(secretFile)
		if strings.TrimSpace(string(b)) != value { // WriteSecret ends the file with a newline
			t.Fatal("the test file")
		}
	})
}

func fmtAll(c *Config) string {
	return strings.Join([]string{c.Auth.OIDC.ClientSecret.String(), fmtV(c.Auth)}, " ")
}

func fmtV(v any) string { return strings.TrimSpace(fmt.Sprintf("%+v %#v", v, v)) }
