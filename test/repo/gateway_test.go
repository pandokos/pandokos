package repo

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/test/familycodes"
)

// nonTestGoFiles lists the Go files of the given directories that are not tests, with the
// directories of packages that are exempt skipped.
func nonTestGoFiles(t *testing.T, dirs []string, skip ...string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(root(t), d), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			rel = filepath.ToSlash(rel)
			if e.IsDir() {
				for _, s := range skip {
					if rel == s {
						return fs.SkipDir
					}
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// TestOutboundHTTPInOnePlace is the second half of T-R25 (sprint 3.1, section 4): outbound HTTP
// exists in internal/egress only, and in internal/cli and internal/adminapi, the operator's
// client of the administration API. Nowhere in cmd/ or internal/ is a transport or a dialer built, the default
// client or transport used, or verification switched off or a proxy read from the environment.
func TestOutboundHTTPInOnePlace(t *testing.T) {
	// internal/adminapi is the runtime of the generated client the CLI is built on ("the CLI's
	// client, untouched" in the plan): it is exempt with internal/cli.
	files := nonTestGoFiles(t, []string{"cmd", "internal"}, "internal/egress", "internal/cli", "internal/adminapi")
	if len(files) < 50 {
		t.Fatalf("only %d files were scanned", len(files))
	}
	bannedSelectors := map[string]map[string]bool{
		"net/http": {"DefaultClient": true, "DefaultTransport": true, "Get": true, "Post": true, "Head": true, "PostForm": true},
	}
	bannedLiterals := map[string]map[string]bool{
		"net/http": {"Transport": true},
		"net":      {"Dialer": true},
	}
	never := map[string]bool{"InsecureSkipVerify": true, "ProxyFromEnvironment": true}
	scanned := 0
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(src, []byte("Code generated")) && bytes.Contains(src, []byte("DO NOT EDIT")) {
			continue // generated: the generator's runtime is not an outbound path of the server
		}
		f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		scanned++
		local := map[string]string{} // the name an import is known by -> its path
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			name := filepath.Base(path)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			local[name] = path
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok {
					if banned := bannedSelectors[local[id.Name]]; banned[x.Sel.Name] {
						t.Errorf("%s names %s.%s: outbound HTTP belongs to internal/egress", rel, id.Name, x.Sel.Name)
					}
				}
				if never[x.Sel.Name] {
					t.Errorf("%s names %s: it is never written", rel, x.Sel.Name)
				}
			case *ast.CompositeLit:
				if sel, ok := x.Type.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && bannedLiterals[local[id.Name]][sel.Sel.Name] {
						t.Errorf("%s builds a %s.%s: outbound HTTP belongs to internal/egress", rel, id.Name, sel.Sel.Name)
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && never[id.Name] {
					t.Errorf("%s sets %s: it is never written", rel, id.Name)
				}
			}
			return true
		})
	}
	if scanned < 50 {
		t.Errorf("only %d non-generated files were scanned", scanned)
	}
}

// TestGatewayGuide is T-R26: the gateway guide names what an operator and a caller need.
func TestGatewayGuide(t *testing.T) {
	doc := read(t, "docs/operations/gateway.md")
	lower := strings.ToLower(doc)

	t.Run("every error code with its status", func(t *testing.T) {
		lines := strings.Split(doc, "\n")
		codes := familycodes.All(t)
		for _, code := range codes {
			status := statusOf(code)
			found := false
			for _, l := range lines {
				found = found || (strings.Contains(l, "`"+code+"`") && strings.Contains(l, strconv.Itoa(status)))
			}
			if !found {
				t.Errorf("gateway.md has no line with `%s` and its status %d", code, status)
			}
		}
		for _, h := range []string{"X-Pandokos-Error", "X-Pandokos-Request-Id"} {
			if !strings.Contains(doc, h) {
				t.Errorf("gateway.md does not name %s", h)
			}
		}
	})

	t.Run("every header of the adapter's lists", func(t *testing.T) {
		for _, h := range openai.Adapter().RequestHeaders() {
			if !strings.Contains(lower, strings.ToLower(h)) {
				t.Errorf("gateway.md does not name the forwarded header %s", h)
			}
		}
		for _, h := range []string{"Authorization", "Cookie", "X-Api-Key", "Idempotency-Key", "Accept-Encoding", "Traceparent", "Forwarded"} {
			if !strings.Contains(lower, strings.ToLower(h)) {
				t.Errorf("gateway.md does not name the dropped header %s", h)
			}
		}
	})

	t.Run("the keys", func(t *testing.T) {
		for _, k := range []string{"upstream.tls.ca_file", "upstream.proxy.url", "PANDOKOS_UPSTREAM_TLS_CA_FILE", "PANDOKOS_UPSTREAM_PROXY_URL"} {
			if !strings.Contains(doc, k) {
				t.Errorf("gateway.md does not name %s", k)
			}
		}
	})

	t.Run("every range of the classification table", func(t *testing.T) {
		for _, r := range []string{
			"0.0.0.0/8", "224.0.0.0/4", "ff00::/8", "240.0.0.0/4", "169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254",
			"127.0.0.0/8", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "fe80::/10",
			"fc00::/7", "fec0::/10", "192.0.0.0/24", "198.18.0.0/15", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24",
			"2001:db8::/32", "2001::/32", "64:ff9b:1::/48", "64:ff9b::/96", "2002::/16",
		} {
			if !strings.Contains(doc, r) {
				t.Errorf("gateway.md does not list the range %s", r)
			}
		}
	})

	t.Run("sections the guide must have", func(t *testing.T) {
		for _, w := range []string{
			"base urls", "what a call needs", "outbound connections", "authentication failures",
			"model:invoke", "credential:use", "private", "redirect", "egress blocked", "x-pandokos-error",
		} {
			if !strings.Contains(lower, w) {
				t.Errorf("gateway.md lacks %q", w)
			}
		}
		if !regexp.MustCompile(`(?mi)^#+ .*upgrading from the .?skeleton.? configuration`).MatchString(doc) {
			t.Error("gateway.md has no section about upgrading from the skeleton configuration")
		}
		for _, w := range []string{"pandokos endpoint update", "pandokos credential create", "without"} {
			if !strings.Contains(lower, w) {
				t.Errorf("the upgrade note lacks %q", w)
			}
		}
	})

	t.Run("the pair of permissions", func(t *testing.T) {
		perms := read(t, "docs/reference/permissions.md")
		ok := false
		for _, l := range strings.Split(perms, "\n") {
			ok = ok || (strings.Contains(l, "`model:invoke`") && strings.Contains(l, "`credential:use`") && strings.Contains(strings.ToLower(l), "both"))
		}
		if !ok {
			t.Error("permissions.md has no line that says a call needs both model:invoke and credential:use")
		}
	})

	t.Run("replay exposure", func(t *testing.T) {
		sec := read(t, "docs/architecture/security-model.md")
		i := strings.Index(sec, "Replay exposure")
		if i < 0 {
			t.Fatal("security-model.md has no section Replay exposure")
		}
		for _, w := range []string{"bearer", "TLS", "ADR 0030", "IAM-15", "IAM-16", "SEC-02"} {
			if !strings.Contains(sec[i:], w) {
				t.Errorf("the replay section lacks %q", w)
			}
		}
	})

	t.Run("the decision records", func(t *testing.T) {
		index := read(t, "docs/adr/README.md")
		for file, title := range map[string]string{
			"docs/adr/0037-request-pipeline-and-resolution.md":     "# 0037. Request pipeline and resolution",
			"docs/adr/0038-egress-guard-upstream-tls-and-proxy.md": "# 0038. Egress guard, upstream TLS and proxy",
		} {
			if !strings.HasPrefix(read(t, file), title+"\n") {
				t.Errorf("%s does not start with %q", file, title)
			}
			if !strings.Contains(index, strings.TrimSuffix(filepath.Base(file), ".md")) {
				t.Errorf("docs/adr/README.md does not list %s", filepath.Base(file))
			}
		}
		adr := read(t, "docs/adr/0020-error-contract.md")
		for _, w := range []string{"`invalid_request`", "`request_timeout`", "## History", "sprint 3.1"} {
			if !strings.Contains(adr, w) {
				t.Errorf("ADR 0020 lacks %q", w)
			}
		}
	})
}

// statusOf is the status the contract gives a code (sprint 3.1, section 5.8). A code that is not
// here fails TestGatewayGuide: the sweep ranges over the product's own list.
func statusOf(code string) int {
	switch code {
	case "unauthenticated":
		return 401
	case "tenant_suspended", "forbidden", "model_not_allowed":
		return 403
	case "endpoint_not_supported":
		return 404
	case "no_credential":
		return 409
	case "invalid_request":
		return 400
	case "request_timeout":
		return 408
	case "request_too_large":
		return 413
	case "upstream_unreachable":
		return 502
	case "upstream_timeout":
		return 504
	case "unavailable":
		return 503
	case "internal":
		return 500
	}
	return -1 // an unknown code: no line can hold it
}

// TestNoSkeleton is T-R27: the word that named the walking skeleton of sprint 1.1 is in no file of
// the product, its tests, its deployment files or its operations guides, except where the removal
// is proven or explained: the upgrade note of the gateway guide, the test of the configuration
// that proves the section is refused, and this file.
func TestNoSkeleton(t *testing.T) {
	allowed := map[string]bool{
		"docs/operations/gateway.md":       true,
		"internal/config/upstream_test.go": true,
		"test/repo/gateway_test.go":        true,
	}
	var hits []string
	scanned := 0
	for _, top := range []string{"cmd", "internal", "test", "deploy", "Dockerfile", "docs/operations"} {
		base := filepath.Join(root(t), top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			rel = filepath.ToSlash(rel)
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.IndexByte(b, 0) >= 0 { // binary
				return nil
			}
			scanned++
			if bytes.Contains(bytes.ToLower(b), []byte("skeleton")) && !allowed[rel] {
				hits = append(hits, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("only %d files were scanned", scanned)
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Errorf("%s still says skeleton", h)
	}
	// The allowed files do say it: an exception nobody needs is removed.
	for rel := range allowed {
		if rel == "test/repo/gateway_test.go" {
			continue
		}
		if !strings.Contains(strings.ToLower(read(t, rel)), "skeleton") {
			t.Errorf("%s is allowed to say skeleton and does not: remove the exception", rel)
		}
	}
}
