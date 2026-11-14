package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// composeDoc is the part of compose.yaml the tests read.
type composeDoc struct {
	Services map[string]struct {
		Image       string            `yaml:"image"`
		Ports       []string          `yaml:"ports"`
		ReadOnly    bool              `yaml:"read_only"`
		CapDrop     []string          `yaml:"cap_drop"`
		SecurityOpt []string          `yaml:"security_opt"`
		Environment map[string]string `yaml:"environment"`
		Secrets     []string          `yaml:"secrets"`
		Networks    []string          `yaml:"networks"`
	} `yaml:"services"`
	Secrets  map[string]map[string]any `yaml:"secrets"`
	Networks map[string]struct {
		Internal bool `yaml:"internal"`
	} `yaml:"networks"`
}

// TestComposeReference is T-R32: the Compose reference is safe as it is, and the guide quotes the
// commands the Compose test runs.
func TestComposeReference(t *testing.T) {
	text := read(t, "deploy/compose/compose.yaml")
	var doc composeDoc
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"postgres", "migrate", "pandokos"} {
		if _, ok := doc.Services[name]; !ok {
			t.Fatalf("compose.yaml has no service %s", name)
		}
	}

	t.Run("the PostgreSQL image has a digest", func(t *testing.T) {
		if !regexp.MustCompile(`^postgres:16[\w.-]*@sha256:[0-9a-f]{64}$`).MatchString(doc.Services["postgres"].Image) {
			t.Errorf("image %q", doc.Services["postgres"].Image)
		}
	})
	t.Run("every published port is on the host loopback and PostgreSQL publishes none", func(t *testing.T) {
		n := 0
		for name, s := range doc.Services {
			for _, p := range s.Ports {
				n++
				if !strings.HasPrefix(p, "127.0.0.1:") {
					t.Errorf("%s publishes %q", name, p)
				}
			}
		}
		if n < 2 {
			t.Errorf("%d published ports", n)
		}
		if len(doc.Services["postgres"].Ports) != 0 {
			t.Error("PostgreSQL is published")
		}
	})
	t.Run("the Pandokos containers are hardened", func(t *testing.T) {
		for _, name := range []string{"migrate", "pandokos"} {
			s := doc.Services[name]
			if !s.ReadOnly {
				t.Errorf("%s is not read_only", name)
			}
			if len(s.CapDrop) != 1 || s.CapDrop[0] != "ALL" {
				t.Errorf("%s cap_drop %v", name, s.CapDrop)
			}
			found := false
			for _, o := range s.SecurityOpt {
				found = found || strings.HasPrefix(o, "no-new-privileges")
			}
			if !found {
				t.Errorf("%s has no no-new-privileges", name)
			}
		}
	})
	t.Run("the database network is internal and the server also has a way out", func(t *testing.T) {
		if !doc.Networks["database"].Internal {
			t.Error("the database network is not internal")
		}
		onDatabase := func(name string) bool {
			for _, n := range doc.Services[name].Networks {
				if n == "database" {
					return true
				}
			}
			return false
		}
		if !onDatabase("postgres") || !onDatabase("pandokos") || !onDatabase("migrate") {
			t.Error("a service is not on the database network")
		}
		out := false
		for _, n := range doc.Services["pandokos"].Networks {
			if n != "database" && !doc.Networks[n].Internal {
				out = true
			}
		}
		if !out {
			t.Error("pandokos has no network to reach the providers")
		}
		for _, n := range doc.Services["postgres"].Networks {
			if n != "database" {
				t.Errorf("postgres is also on %s", n)
			}
		}
	})
	t.Run("both listeners have TLS files and nothing allows plain text", func(t *testing.T) {
		env := doc.Services["pandokos"].Environment
		for _, k := range []string{
			"PANDOKOS_LISTENERS_INFERENCE_TLS_CERT_FILE", "PANDOKOS_LISTENERS_INFERENCE_TLS_KEY_FILE",
			"PANDOKOS_LISTENERS_ADMIN_TLS_CERT_FILE", "PANDOKOS_LISTENERS_ADMIN_TLS_KEY_FILE",
		} {
			if !strings.HasPrefix(env[k], "/run/secrets/") {
				t.Errorf("%s = %q, want a file under /run/secrets", k, env[k])
			}
		}
		for _, rel := range []string{"deploy/compose/compose.yaml", "deploy/compose/setup.sh", "deploy/compose/initdb/10-pandokos.sh"} {
			if strings.Contains(strings.ToLower(read(t, rel)), "allow_plaintext") {
				t.Errorf("%s mentions allow_plaintext", rel)
			}
		}
		if got := doc.Services["pandokos"].Environment["PANDOKOS_DATABASE_TLS_MODE"]; got != "verify-full" {
			t.Errorf("database TLS mode %q", got)
		}
	})
	t.Run("secrets are files, never values", func(t *testing.T) {
		if len(doc.Secrets) < 8 {
			t.Errorf("%d secrets", len(doc.Secrets))
		}
		for name, s := range doc.Secrets {
			if f, ok := s["file"].(string); !ok || !strings.HasPrefix(f, "./secrets/") {
				t.Errorf("secret %s: %v", name, s)
			}
			if _, ok := s["environment"]; ok {
				t.Errorf("secret %s is taken from the environment", name)
			}
		}
		for name, s := range doc.Services {
			for k := range s.Environment {
				if strings.HasSuffix(k, "_PASSWORD") || strings.HasSuffix(k, "_SECRET") || strings.HasSuffix(k, "_KEY") && !strings.HasSuffix(k, "TLS_KEY") {
					t.Errorf("%s sets %s as a value: the reference passes secrets as files", name, k)
				}
			}
		}
	})
	t.Run("the secrets directory is not committed, the scripts are executable and valid", func(t *testing.T) {
		if !regexp.MustCompile(`(?m)^/?secrets/?\s*$`).MatchString(read(t, "deploy/compose/.gitignore")) {
			t.Error("deploy/compose/.gitignore does not hold secrets/")
		}
		for _, rel := range []string{"deploy/compose/setup.sh", "deploy/compose/initdb/10-pandokos.sh"} {
			info, err := os.Stat(filepath.Join(root(t), rel))
			if err != nil || info.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s: %v %v", rel, info, err)
			}
			if out, err := exec.Command("sh", "-n", filepath.Join(root(t), rel)).CombinedOutput(); err != nil {
				t.Errorf("sh -n %s: %v\n%s", rel, err, out)
			}
		}
	})
	t.Run("the guide quotes each command the Compose test runs", func(t *testing.T) {
		guide := read(t, "docs/operations/compose.md")
		for _, c := range []string{
			"./setup.sh", "docker compose up -d --wait", "secrets/tls_cert", "/pandokos init --key-out", "tenant create demo --admin-key-out",
			"credential create --tenant demo --provider openai --secret-file /dev/stdin", "credential create --tenant demo --provider anthropic --secret-file /dev/stdin",
			"service-account create --tenant demo --role inference", "key create --tenant demo --service-account app",
			"capability check --tenant demo --provider anthropic --model", "usage list --tenant demo", "docker compose stop",
			"make image", "PANDOKOS_IMAGE", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS",
		} {
			if !strings.Contains(guide, c) {
				t.Errorf("docs/operations/compose.md does not quote %q", c)
			}
		}
	})
	t.Run("Dependabot covers the Compose reference and both SDK suites", func(t *testing.T) {
		var d struct {
			Updates []struct {
				Ecosystem string `yaml:"package-ecosystem"`
				Directory string `yaml:"directory"`
			} `yaml:"updates"`
		}
		if err := yaml.Unmarshal([]byte(read(t, ".github/dependabot.yml")), &d); err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, u := range d.Updates {
			have[u.Ecosystem+" "+u.Directory] = true
		}
		for _, want := range []string{"docker-compose /deploy/compose", "pip /test/sdk/python", "npm /test/sdk/ts"} {
			if !have[want] {
				t.Errorf("dependabot.yml has no entry %q", want)
			}
		}
	})
	t.Run("the Compose test file exists and fails when its image is missing", func(t *testing.T) {
		src := read(t, "test/compose/compose_test.go")
		if !strings.Contains(src, "//go:build compose") || !strings.Contains(src, "PANDOKOS_TEST_IMAGE") || strings.Contains(src, "t.Skip") {
			t.Error("test/compose/compose_test.go must carry the compose tag, read PANDOKOS_TEST_IMAGE and never skip")
		}
	})
}
