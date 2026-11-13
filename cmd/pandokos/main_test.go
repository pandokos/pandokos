package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func runCmd(t *testing.T, environ []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code = run(ctx, args, &out, &errb, environ)
	return code, out.String(), errb.String()
}

func TestVersion(t *testing.T) {
	code, out, errs := runCmd(t, nil, "version")
	re := regexp.MustCompile(`^pandokos \S+ \(commit \S+, go\S+, [a-z0-9]+/[a-z0-9]+\)\n$`)
	if code != 0 || !re.MatchString(out) || errs != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, out, errs := runCmd(t, nil, args...)
		if code != 0 || !strings.Contains(out, "serve") || !strings.Contains(out, "config check") || errs != "" ||
			!strings.Contains(out, "migrate [--config FILE]") || !strings.Contains(out, "migrate status [--config FILE]") ||
			!strings.Contains(out, "[--for serve|migrate|init]") {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
	code, out, errs := runCmd(t, nil)
	if code != 2 || out != "" || !strings.Contains(errs, "serve") {
		t.Errorf("no arguments: code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, out, errs := runCmd(t, nil, "frobnicate")
	if code != 2 || out != "" || !strings.Contains(errs, `pandokos: unknown command "frobnicate"`) || !strings.Contains(errs, "serve") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
	}
	if code, _, errs := runCmd(t, nil, "serve", "--bogus"); code != 2 || errs == "" {
		t.Errorf("unknown flag: code %d, stderr %q", code, errs)
	}
	if code, _, _ := runCmd(t, nil, "config"); code != 2 {
		t.Errorf("config without check: code %d", code)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// secretEnv is a valid environment for the serve purpose: the provider key and the database
// settings, with the three runtime password files.
func secretEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=" + testutil.WriteSecret(t, "key", fixtures.CanaryProviderKey),
		"PANDOKOS_DATABASE_ADDRESS=127.0.0.1:5432",
		"PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_gateway", "pw-gateway-test"),
		"PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_admin", "pw-admin-test"),
		"PANDOKOS_DATABASE_WORKER_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_worker", "pw-worker-test"),
	}
}

// initEnv is a valid environment for the init purpose: the address and the admin password file
// only.
func initEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PANDOKOS_DATABASE_ADDRESS=127.0.0.1:5432",
		"PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_admin", "pw-admin-test"),
	}
}

// migrateEnv is a valid environment for the migrate purpose: the address and the owner password
// file only.
func migrateEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PANDOKOS_DATABASE_ADDRESS=127.0.0.1:5432",
		"PANDOKOS_DATABASE_TLS_MODE=disable",
		"PANDOKOS_DATABASE_OWNER_PASSWORD_FILE=" + testutil.WriteSecret(t, "db_owner", "pw-owner-test"),
	}
}

func TestConfigCheck(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		file := writeConfig(t, "log:\n  level: debug\nlisteners:\n  inference:\n    address: 0.0.0.0:8080\nskeleton:\n  openai:\n    base_url: http://example.com/v1\n")
		code, out, errs := runCmd(t, secretEnv(t), "config", "check", "--config", file)
		if code != 0 || strings.TrimSpace(out) != "configuration OK" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		if !strings.Contains(errs, "warning: skeleton.openai.base_url: the provider key is sent without TLS") ||
			!strings.Contains(errs, "warning: listeners.inference.address: the listener serves plain HTTP; set listeners.inference.tls.cert_file and key_file, or terminate TLS in front of it") {
			t.Errorf("warnings: %q", errs)
		}
		testutil.AssertNoCanary(t, "stdout", out)
		testutil.AssertNoCanary(t, "stderr", errs)
	})
	t.Run("invalid", func(t *testing.T) {
		env := append(secretEnv(t), "PANDOKOS_LOG_LEVEL=verbose", "PANDOKOS_LISTENERS_INFERENCE_MAX_HEADER_BYTES=1")
		file := writeConfig(t, "skeleton:\n  openai:\n    api_key: "+fixtures.CanaryProviderKey+"\n")
		code, out, errs := runCmd(t, env, "config", "check", "--config", file)
		if code != 1 || out != "" {
			t.Fatalf("code %d, stdout %q", code, out)
		}
		for _, want := range []string{"error: log.level: ", "error: listeners.inference.max_header_bytes: ", "error: file: "} {
			if !strings.Contains(errs, want) {
				t.Errorf("stderr lacks %q: %q", want, errs)
			}
		}
		testutil.AssertNoCanary(t, "stderr", errs)
	})
	t.Run("no file uses defaults and environment", func(t *testing.T) {
		code, out, errs := runCmd(t, secretEnv(t), "config", "check")
		if code != 0 || !strings.Contains(out, "configuration OK") {
			t.Fatalf("code %d %q %q", code, out, errs)
		}
	})
	t.Run("flag beats PANDOKOS_CONFIG", func(t *testing.T) {
		bad := writeConfig(t, "nope: 1\n")
		good := writeConfig(t, "log:\n  level: info\n")
		env := append(secretEnv(t), "PANDOKOS_CONFIG="+bad)
		if code, _, errs := runCmd(t, env, "config", "check", "--config", good); code != 0 {
			t.Fatalf("flag did not win: %d %q", code, errs)
		}
		if code, _, _ := runCmd(t, env, "config", "check"); code != 1 {
			t.Fatal("PANDOKOS_CONFIG was not used")
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		code, _, errs := runCmd(t, secretEnv(t), "config", "check", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
		if code != 1 || !strings.Contains(errs, "error: file: ") {
			t.Fatalf("code %d %q", code, errs)
		}
	})
}

func TestServeRejectsInvalidConfiguration(t *testing.T) {
	env := append(secretEnv(t), "PANDOKOS_LOG_LEVEL=verbose", "PANDOKOS_LISTENERS_INFERENCE_ADDRESS=nohost")
	code, out, errs := runCmd(t, env, "serve")
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	logs := &testutil.LogBuffer{}
	_, _ = logs.Write([]byte(out))
	lines := logs.Find(t, "configuration error")
	if len(lines) != 2 {
		t.Fatalf("got %d configuration error lines in %q", len(lines), out)
	}
	for _, l := range lines {
		if l["level"] != "ERROR" || l["key"] == nil || l["problem"] == nil {
			t.Errorf("line: %v", l)
		}
	}
	testutil.AssertNoCanary(t, "stdout", out)
	testutil.AssertNoCanary(t, "stderr", errs)
}

// TestMigrateUsage is T-117.
func TestMigrateUsage(t *testing.T) {
	const message = "pandokos: migrate takes no argument other than status"
	for _, args := range [][]string{{"migrate", "extra"}, {"migrate", "status", "extra"}, {"migrate", "--config", "x.yaml", "extra"}} {
		code, out, errs := runCmd(t, migrateEnv(t), args...)
		if code != 2 || out != "" || !strings.Contains(errs, message) || !strings.Contains(errs, "migrate status [--config FILE]") {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
	for _, args := range [][]string{{"migrate", "--bogus"}, {"migrate", "status", "--bogus"}} {
		if code, out, errs := runCmd(t, migrateEnv(t), args...); code != 2 || out != "" || errs == "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
	t.Run("invalid configuration", func(t *testing.T) {
		for _, args := range [][]string{{"migrate"}, {"migrate", "status"}} {
			code, out, errs := runCmd(t, nil, args...)
			if code != 1 || out != "" {
				t.Fatalf("%v: code %d, stdout %q", args, code, out)
			}
			for _, want := range []string{"error: database.address: must be set", "error: database.owner.password_file: must be set"} {
				if !strings.Contains(errs, want) {
					t.Errorf("%v: stderr lacks %q: %q", args, want, errs)
				}
			}
			if strings.Contains(errs, "skeleton.") || strings.Contains(errs, "database.gateway") {
				t.Errorf("%v: migrate asked for a secret it does not need: %q", args, errs)
			}
		}
	})
	t.Run("help lists the commands", func(t *testing.T) {
		code, out, _ := runCmd(t, nil, "help")
		if code != 0 || !strings.Contains(out, "migrate [--config FILE]          Apply pending schema migrations, then exit") ||
			!strings.Contains(out, "migrate status [--config FILE]   Show applied and pending migrations, then exit") {
			t.Errorf("code %d, stdout %q", code, out)
		}
	})
}

// TestConfigCheckFor is T-118.
func TestConfigCheckFor(t *testing.T) {
	t.Run("migrate needs only the owner password", func(t *testing.T) {
		code, out, errs := runCmd(t, migrateEnv(t), "config", "check", "--for", "migrate")
		if code != 0 || strings.TrimSpace(out) != "configuration OK" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("serve needs the runtime passwords", func(t *testing.T) {
		for _, args := range [][]string{{"config", "check", "--for", "serve"}, {"config", "check"}} {
			code, _, errs := runCmd(t, migrateEnv(t), args...)
			if code != 1 || !strings.Contains(errs, "error: database.gateway.password_file: must be set") {
				t.Errorf("%v: code %d, stderr %q", args, code, errs)
			}
		}
		for _, args := range [][]string{{"config", "check", "--for", "serve"}, {"config", "check"}} {
			if code, out, errs := runCmd(t, secretEnv(t), args...); code != 0 || !strings.Contains(out, "configuration OK") {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
			}
		}
	})
	t.Run("init needs only the admin password", func(t *testing.T) {
		code, out, errs := runCmd(t, initEnv(t), "config", "check", "--for", "init")
		if code != 0 || strings.TrimSpace(out) != "configuration OK" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		code, _, errs = runCmd(t, migrateEnv(t), "config", "check", "--for", "init")
		if code != 1 || !strings.Contains(errs, "error: database.admin.password_file: must be set") ||
			strings.Contains(errs, "database.gateway") || strings.Contains(errs, "skeleton.") {
			t.Errorf("code %d, stderr %q", code, errs)
		}
	})
	t.Run("unknown purpose", func(t *testing.T) {
		code, out, errs := runCmd(t, secretEnv(t), "config", "check", "--for", "nope")
		if code != 2 || out != "" || !strings.Contains(errs, "pandokos: --for must be serve, migrate or init") {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
	})
}

// TestInitAndClientUsage is T-216.
func TestInitAndClientUsage(t *testing.T) {
	t.Run("init flags", func(t *testing.T) {
		for _, args := range [][]string{
			{"init", "--ttl", "1m"}, {"init", "--ttl", "4m59s"}, {"init", "--ttl", "721h"}, {"init", "--ttl", "soon"}, {"init", "--ttl", "-1h"},
			{"init", "extra"}, {"init", "--bogus"}, {"init", "--key-out"},
		} {
			code, out, errs := runCmd(t, initEnv(t), args...)
			if code != 2 || out != "" || errs == "" {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
			}
		}
		for _, args := range [][]string{{"init", "--ttl", "5m"}, {"init", "--ttl", "720h"}} {
			// Within the range the command goes on to the database, which is not there: 1, not 2.
			env := append(initEnv(t), "PANDOKOS_DATABASE_ADDRESS=127.0.0.1:1")
			if code, _, errs := runCmd(t, env, args...); code != 1 {
				t.Errorf("%v: code %d, stderr %q", args, code, errs)
			}
		}
	})
	t.Run("init with an invalid configuration", func(t *testing.T) {
		code, out, errs := runCmd(t, nil, "init")
		if code != 1 || out != "" || !strings.Contains(errs, "error: database.address: must be set") || !strings.Contains(errs, "error: database.admin.password_file: must be set") {
			t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
		}
		if strings.Contains(errs, "database.gateway") || strings.Contains(errs, "database.owner") || strings.Contains(errs, "skeleton.") {
			t.Errorf("init asked for a secret it does not need: %q", errs)
		}
	})
	t.Run("help lists the new commands", func(t *testing.T) {
		code, out, _ := runCmd(t, nil, "help")
		if code != 0 {
			t.Fatalf("code %d", code)
		}
		for _, want := range []string{
			"init [--config FILE] [--ttl DURATION] [--description TEXT] [--key-out FILE] [--recover]",
			"Issue the bootstrap platform key, then exit",
			"whoami",
			"tenant list|create|get|update|suspend|reactivate",
			"service-account list|create|get|update|disable|enable",
			"key list|create|get|revoke",
			"platform-key list|create|get|revoke",
			"Administer through the administration API;",
			`"pandokos <command> --help" lists the flags`,
			"config check [--config FILE] [--for serve|migrate|init]",
			"API commands read PANDOKOS_CLI_SERVER, PANDOKOS_CLI_KEY_FILE and PANDOKOS_CLI_CA_FILE.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the usage text lacks %q", want)
			}
		}
		if i, j := strings.Index(out, "migrate status"), strings.Index(out, "config check"); i < 0 || j < i || !(strings.Index(out, "init [--config") > i && strings.Index(out, "init [--config") < j) {
			t.Error("the new lines are not between migrate status and config check")
		}
	})
	t.Run("a command group needs a sub-command", func(t *testing.T) {
		for _, args := range [][]string{
			{"tenant"}, {"service-account"}, {"key"}, {"platform-key"},
			{"tenant", "frobnicate"}, {"key", "delete"}, {"platform-key", "list", "extra"},
		} {
			code, out, errs := runCmd(t, nil, args...)
			if code != 2 || out != "" || errs == "" {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
			}
		}
	})
	t.Run("a command without a key file is a usage error", func(t *testing.T) {
		for _, args := range [][]string{{"whoami"}, {"tenant", "list"}} {
			code, out, errs := runCmd(t, nil, args...)
			if code != 2 || out != "" || !strings.Contains(errs, "key") {
				t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
			}
		}
	})
}
