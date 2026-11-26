package repo

import (
	"go/ast"
	"go/token"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/telemetry"
)

// TestTransitClientDependencies is T-R85: the transit client owns no module of the two vendors,
// imports the standard library and internal/ only, builds no transport, dialer or TLS configuration
// of its own, and nothing outside tests switches certificate verification off.
func TestTransitClientDependencies(t *testing.T) {
	for _, f := range []string{"go.mod", "go.sum"} {
		text := strings.ToLower(read(t, f))
		for _, vendor := range []string{"github.com/hashicorp/vault", "github.com/openbao/", "github.com/hashicorp/go-secure-stdlib", "hashicorp/vault-client-go"} {
			if strings.Contains(text, vendor) {
				t.Errorf("%s names %s: the key service is spoken to with net/http (ADR 0002)", f, vendor)
			}
		}
	}
	var files []string
	for _, f := range goFiles(t, "internal/vault") {
		rel := relPath(t, f)
		if strings.HasPrefix(rel, "internal/vault/transittest/") {
			continue
		}
		base := filepath.Base(rel)
		if strings.HasPrefix(base, "transit") || base == "router.go" || base == "unwrapcache.go" {
			files = append(files, f)
		}
	}
	if len(files) < 3 {
		t.Fatalf("the files of the transit client are %v", files)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		rel := relPath(t, file)
		f := parseFile(t, fset, file)
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") && !strings.HasPrefix(path, "github.com/pandokos/pandokos/internal/") {
				t.Errorf("%s imports %s: the standard library and internal/ only", rel, path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				sel, ok := x.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				if pkg == nil {
					return true
				}
				switch name := pkg.Name + "." + sel.Sel.Name; name {
				case "http.Transport", "net.Dialer", "tls.Config":
					t.Errorf("%s: %s constructs a %s; the transport comes from egress.Guard.KeyService", fset.Position(x.Pos()), rel, name)
				case "http.Client":
					hasTransport := false
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Transport" {
								hasTransport = true
							}
						}
					}
					if !hasTransport {
						t.Errorf("%s: %s constructs an http.Client with the default transport", fset.Position(x.Pos()), rel)
					}
				}
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "http" && (x.Sel.Name == "DefaultClient" || x.Sel.Name == "DefaultTransport" || x.Sel.Name == "Get" || x.Sel.Name == "Post") {
					t.Errorf("%s: %s uses http.%s", fset.Position(x.Pos()), rel, x.Sel.Name)
				}
			}
			return true
		})
	}
	for _, file := range goFiles(t, "cmd", "internal", "test") {
		if strings.Contains(read(t, relPath(t, file)), "InsecureSkip"+"Verify") {
			t.Errorf("%s names InsecureSkip%s: only a test of a refusal may", relPath(t, file), "Verify")
		}
	}
}

// TestTransitDocuments is T-R86: the documents of sprint 12.1 say what the design says.
func TestTransitDocuments(t *testing.T) {
	has := func(t *testing.T, name, doc string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(strings.ToLower(doc), strings.ToLower(w)) {
				t.Errorf("%s does not mention %q", name, w)
			}
		}
	}
	policy := strings.TrimSpace(read(t, "deploy/transit/pandokos-policy.hcl"))

	t.Run("the guide", func(t *testing.T) {
		doc := read(t, "docs/operations/external-key.md")
		for _, h := range []string{"what changes", "requirements of the service", "creating the key", "the keys", "kek check", "moving to the key service", "moving back",
			"rotating", "erasure", "slow or away", "backups", "loss and compromise", "limits"} {
			if !regexp.MustCompile(`(?mi)^#+ .*` + regexp.QuoteMeta(h)).MatchString(doc) {
				t.Errorf("external-key.md has no section about %q", h)
			}
		}
		// The constants of the design.
		has(t, "external-key.md", doc, "request_timeout", "max_concurrent_requests", "start_timeout", "64 KiB", "five consecutive", "5 s", "2 s", "32", "30 s", "15 s",
			"pandokos_vault_provider_up", "pandokos_vault_provider_token_ttl_seconds", "kek_provider_unavailable", "min_decryption_version", "trim", "auto_rotate_period",
			"X-Vault-Namespace", "unwrap_key_files")
		// Sprint 12.2: an exportable key is refused unless the option allows it, and the section on
		// the key says so.
		has(t, "external-key.md", doc, "vault.kek.transit.allow_exportable_key", "refus")
		if i := regexp.MustCompile(`(?mi)^#+ .*creating the key`).FindStringIndex(doc); i == nil ||
			!strings.Contains(doc[i[0]:min(len(doc), i[0]+4000)], "allow_exportable_key") {
			t.Errorf("the section of external-key.md on creating the key does not name the refusal of an exportable key (vault.kek.transit.allow_exportable_key)")
		}
		if !strings.Contains(doc, policy) {
			t.Errorf("the policy of the guide is not deploy/transit/pandokos-policy.hcl byte for byte")
		}
		if !strings.Contains(read(t, "test/transit/conformance.go"), "pandokos-policy.hcl") {
			t.Errorf("the conformance suite does not load deploy/transit/pandokos-policy.hcl")
		}
	})

	t.Run("the backup section names the snapshot rule", func(t *testing.T) {
		// Sprint 12.3 (the test is changed): a key service is backed up by its own snapshot, taken after every
		// rotation and before Pandokos is restarted; a version a kept backup needs is never trimmed; the
		// unseal shares and the means to authenticate are kept with the snapshot; and the transit engine's own
		// backup endpoint is not the procedure, because it needs an exportable key.
		doc := read(t, "docs/operations/external-key.md")
		i := regexp.MustCompile(`(?mi)^#+ .*backups`).FindStringIndex(doc)
		if i == nil {
			t.Fatal("external-key.md has no section about backups")
		}
		section := doc[i[0]:min(len(doc), i[0]+6000)]
		for _, want := range []string{"snapshot", "after every rotation", "before pandokos is restarted", "min_decryption_version", "unseal", "exportable"} {
			if !strings.Contains(strings.ToLower(section), want) {
				t.Errorf("the backup section of external-key.md does not mention %q", want)
			}
		}
	})

	t.Run("the failure reference", func(t *testing.T) {
		doc := read(t, "docs/reference/failure-behaviour.md")
		start := regexp.MustCompile(`(?m)^## 2a\. The key service`).FindStringIndex(doc)
		if start == nil {
			t.Fatal("the failure reference has no section \"2a. The key service\"")
		}
		section := doc[start[0]:]
		if next := regexp.MustCompile(`(?m)^## `).FindStringIndex(section[3:]); next != nil {
			section = section[:next[0]+3]
		}
		var rows []string
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, "|") && !strings.HasPrefix(strings.ReplaceAll(line, " ", ""), "|-") && !strings.HasPrefix(strings.ToLower(strings.Trim(line, "| ")), "during an outage") &&
				!strings.HasPrefix(strings.ToLower(strings.Trim(line, "| ")), "situation") && !strings.HasPrefix(strings.ToLower(strings.Trim(line, "| ")), "what fails") {
				rows = append(rows, line)
			}
		}
		if len(rows) < 8 {
			t.Fatalf("section 2a has %d rows, want one per line of the table of section 5.10 (8)", len(rows))
		}
		for _, subject := range []string{"cached", "nothing cached", "writing", "administration", "event relay", "re-wrap", "restart", "token expires", "no longer decryptable"} {
			if !strings.Contains(strings.ToLower(strings.Join(rows, "\n")), subject) {
				t.Errorf("section 2a has no row for %q", subject)
			}
		}
		// What a row cites exists (the rule of T-R80).
		tests := map[string]bool{}
		for _, f := range testFiles(t) {
			for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(read(t, relPath(t, f)), -1) {
				tests[m[1]] = true
			}
		}
		logRows := read(t, "docs/reference/logs.md")
		keys := map[string]bool{}
		for _, k := range config.Keys() {
			keys[k.Name] = true
		}
		instruments := map[string]bool{}
		for _, s := range telemetry.Catalogue() {
			instruments[s.Name] = true
		}
		cited := map[string]bool{}
		for _, row := range rows {
			for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(row, -1) {
				tok := m[1]
				switch {
				case strings.HasPrefix(tok, "Test"):
					cited[tok] = true
					if !tests[tok] {
						t.Errorf("section 2a cites the test %s, which does not exist", tok)
					}
				case strings.HasPrefix(tok, "pandokos."):
					if !instruments[tok] {
						t.Errorf("section 2a cites the instrument %s, which does not exist", tok)
					}
				case strings.HasPrefix(tok, "vault."):
					if !keys[tok] {
						t.Errorf("section 2a cites the key %s, which does not exist", tok)
					}
				case strings.HasPrefix(tok, "key provider ") || strings.HasPrefix(tok, "credential cannot be") || strings.HasPrefix(tok, "key rewrap") || strings.HasPrefix(tok, "event relay"):
					if !strings.Contains(logRows, "`"+tok+"`") {
						t.Errorf("section 2a cites the log line %q, which is not in docs/reference/logs.md", tok)
					}
				}
			}
		}
		for _, name := range []string{"TestTransitOutage", "TestOpenerWithTransit", "TestRelayUnwrapCache", "TestRewrapAcrossProviders", "TestTransitTokenLifetime", "TestTransitStartRefusals"} {
			if !cited[name] {
				t.Errorf("section 2a does not cite %s", name)
			}
		}
	})

	t.Run("the configuration, log and metrics references", func(t *testing.T) {
		cfg := read(t, "docs/operations/configuration.md")
		n := 0
		for _, k := range config.Keys() {
			if strings.HasPrefix(k.Name, "vault.kek.transit.") {
				n++
				if !strings.Contains(cfg, k.Name) || !strings.Contains(cfg, k.Env) {
					t.Errorf("configuration.md does not list %s and %s", k.Name, k.Env)
				}
			}
		}
		if n != 14 {
			t.Errorf("%d keys of the transit provider, want 14", n)
		}
		logs := read(t, "docs/reference/logs.md")
		for _, line := range []string{"key provider ready", "key provider configuration weak", "key provider unavailable", "key provider recovered", "key provider token expiring",
			"key provider authentication failing", "key provider no longer needed"} {
			if !strings.Contains(logs, "`"+line+"`") {
				t.Errorf("logs.md has no row for %q", line)
			}
		}
		has(t, "logs.md", logs, "kek_unwrap_provider", "kek_provider_unavailable", "auth_method", "expires_in_seconds", "outage_seconds", "oldest_version", "newest_version")
		metrics := read(t, "docs/reference/metrics.md")
		for _, name := range []string{"pandokos.vault.provider.requests", "pandokos.vault.provider.request.duration", "pandokos.vault.provider.up", "pandokos.vault.provider.token.ttl",
			"pandokos.kek.operation", "pandokos.kek.outcome"} {
			if !strings.Contains(metrics, name) {
				t.Errorf("metrics.md does not name %s", name)
			}
		}
	})

	t.Run("the decision record and its history lines", func(t *testing.T) {
		index := read(t, "docs/adr/README.md")
		if !strings.Contains(index, "0073-the-transit-key-provider") {
			t.Error("docs/adr/README.md does not index ADR 0073")
		}
		adr := read(t, "docs/adr/0073-the-transit-key-provider.md")
		has(t, "ADR 0073", adr, "associated data", "vault.Probe", "amends", "0033", "0038", "0068")
		for _, name := range []string{"0033", "0038"} {
			matches, err := filepath.Glob(filepath.Join(root(t), "docs", "adr", name+"-*.md"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("ADR %s: %v %v", name, matches, err)
			}
			if !strings.Contains(read(t, relPath(t, matches[0])), "0073") {
				t.Errorf("ADR %s carries no history line naming 0073", name)
			}
		}
	})

	t.Run("the release notes name the provider", func(t *testing.T) {
		has(t, "the release notes", read(t, "docs/releases/v0.3.0.md"), "external key-encryption key", "transit", "OpenBao", "HashiCorp Vault", "kek check", "no migration")
	})
}

// TestTransitSuitePinned is T-R87: the real services of the suite are pinned and the CI job runs both.
func TestTransitSuitePinned(t *testing.T) {
	compose := read(t, "test/transit/compose.yaml")
	images := regexp.MustCompile(`(?m)^\s+image:\s*(\S+)\s*$`).FindAllStringSubmatch(compose, -1)
	if len(images) != 2 {
		t.Fatalf("test/transit/compose.yaml has %d images, want 2 (OpenBao and HashiCorp Vault)", len(images))
	}
	for _, m := range images {
		if !regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`).MatchString(m[1]) {
			t.Errorf("the image %s has no tag and sha256 digest", m[1])
		}
	}

	ci := read(t, ".github/workflows/ci.yml")
	i := regexp.MustCompile(`(?m)^  transit:\s*$`).FindStringIndex(ci)
	if i == nil {
		t.Fatal("the workflow has no job transit")
	}
	job := ci[i[0]:]
	if next := regexp.MustCompile(`(?m)^  [a-z0-9_-]+:\s*$`).FindStringIndex(job[len("  transit:"):]); next != nil {
		job = job[:next[0]+len("  transit:")]
	}
	if !regexp.MustCompile(`vendor:\s*\[\s*openbao\s*,\s*vault\s*\]`).MatchString(job) {
		t.Errorf("the job transit does not have the matrix vendor: [openbao, vault]")
	}
	if strings.Contains(job, "continue-on-error") {
		t.Errorf("the job transit has continue-on-error: a failing leg must fail the workflow")
	}
	for _, want := range []string{"make transit-up VENDOR=", "make test-transit", "make transit-down"} {
		if !strings.Contains(job, want) {
			t.Errorf("the job transit does not run %q", want)
		}
	}

	makefile := read(t, "Makefile")
	for _, target := range []string{"transit-up", "transit-down", "test-transit"} {
		if !regexp.MustCompile(`(?m)^` + target + `:`).MatchString(makefile) {
			t.Errorf("the Makefile has no target %s", target)
		}
	}
	for _, f := range []string{"test/transit/up.sh", "test/transit/down.sh"} {
		read(t, f)
	}

	cmd := exec.Command("git", "check-ignore", "-q", "local/transit/root_token")
	cmd.Dir = root(t)
	if err := cmd.Run(); err != nil {
		t.Errorf("local/transit is not ignored by git: %v", err)
	}
}

// TestKeyServiceOneClient is T-R88: the key service is spoken to by one file set.
func TestKeyServiceOneClient(t *testing.T) {
	// The header and the paths of the transit API appear in internal/vault only.
	secrets := []string{"X-Vault-Token", "X-Vault-Request", "/encrypt/", "/decrypt/", "lookup-self", "renew-self", "sha2-256"}
	fset := token.NewFileSet()
	for _, file := range goFiles(t, "cmd", "internal") {
		rel := relPath(t, file)
		if strings.HasPrefix(rel, "internal/vault/") {
			continue
		}
		src := read(t, rel)
		for _, s := range secrets {
			if strings.Contains(src, s) {
				t.Errorf("%s holds %q: only internal/vault speaks the transit API", rel, s)
			}
		}
		f := parseFile(t, fset, file)
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch name := calledName(c); {
			case name == "NewTransitProvider":
				if !strings.HasPrefix(rel, "cmd/pandokos/") {
					t.Errorf("%s: %s builds a transit provider: cmd/pandokos and tests only", fset.Position(c.Pos()), rel)
				}
			case name == "Wrap" && len(c.Args) == 3, name == "Unwrap" && len(c.Args) == 4:
				if rel != "cmd/pandokos/kek.go" {
					if _, ok := c.Fun.(*ast.SelectorExpr); ok {
						t.Errorf("%s: %s wraps or unwraps with a key provider: internal/vault only, with cmd/pandokos/kek.go as the one exception", fset.Position(c.Pos()), rel)
					}
				}
			}
			return true
		})
	}
	// The exception is stated where T-R78 lists it.
	if src := read(t, "test/repo/lifecycle_test.go"); !strings.Contains(src, "kek check") {
		t.Errorf("T-R78 does not list `kek check` with `kek generate` as the exception of cmd/pandokos/kek.go")
	}
}
