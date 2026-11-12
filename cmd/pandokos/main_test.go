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
		if code != 0 || !strings.Contains(out, "serve") || !strings.Contains(out, "config check") || errs != "" {
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

func secretEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PANDOKOS_SKELETON_ACCESS_TOKEN_FILE=" + testutil.WriteSecret(t, "token", fixtures.CanaryAccessToken),
		"PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=" + testutil.WriteSecret(t, "key", fixtures.CanaryProviderKey),
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
			!strings.Contains(errs, "warning: listeners.inference.address: the listener serves plain HTTP; terminate TLS in front of it") {
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
