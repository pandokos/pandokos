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

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/internal/gateway"
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
		for name, a := range gateway.Families() {
			for _, h := range a.RequestHeaders() {
				if !strings.Contains(lower, strings.ToLower(h)) {
					t.Errorf("gateway.md does not name the forwarded header %s of the %s family", h, name)
				}
			}
		}
		for _, h := range []string{"Authorization", "Cookie", "X-Api-Key", "Idempotency-Key", "Accept-Encoding", "Traceparent", "Forwarded"} {
			if !strings.Contains(lower, strings.ToLower(h)) {
				t.Errorf("gateway.md does not name the dropped header %s", h)
			}
		}
	})

	// Sprint 3.2 (section 5.2, 5.3 and 5.5): the three operations, background processing, the
	// metadata headers, the first-byte time and the fields the ledger adds to the log line.
	t.Run("operations, metadata and log fields", func(t *testing.T) {
		for name, a := range gateway.Families() {
			for _, op := range a.Operations() {
				for _, w := range []string{op.Name, op.Path} {
					if !strings.Contains(doc, w) {
						t.Errorf("gateway.md does not name %s of the %s family", w, name)
					}
				}
			}
		}
		for _, w := range []string{"background", "X-Pandokos-Metadata-", "X-Pandokos-Metadata-Dropped", "ttfb_ms", "record_id", "trace_id", "cost_status", "usage_source", "ledger: full", "0024"} {
			if !strings.Contains(doc, w) {
				t.Errorf("gateway.md lacks %q", w)
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
	case "rate_limited":
		return 429
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

// markdownTable returns the header cells and the rows of the first table of doc whose header holds
// every one of the wanted cells, with the markup of a cell (backticks) removed.
func markdownTable(t *testing.T, doc string, want ...string) (header []string, rows [][]string) {
	t.Helper()
	cells := func(line string) []string {
		var out []string
		for _, c := range strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|") {
			out = append(out, strings.TrimSpace(strings.ReplaceAll(c, "`", "")))
		}
		return out
	}
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		head := cells(line)
		has := map[string]bool{}
		for _, c := range head {
			has[c] = true
		}
		ok := true
		for _, w := range want {
			ok = ok && has[w]
		}
		if !ok {
			continue
		}
		for _, row := range lines[i+2:] { // the line after the header is the separator
			if !strings.HasPrefix(strings.TrimSpace(row), "|") {
				break
			}
			rows = append(rows, cells(row))
		}
		return head, rows
	}
	t.Fatalf("no table with the columns %v", want)
	return nil, nil
}

// TestGatewayGuideFamilies is T-R31: the gateway guide lists every route of gateway.Routes(), the
// request and response header lists of both adapters, the error type table, the base URL table
// (one row per SDK and family) and the limit of 500 header values.
func TestGatewayGuideFamilies(t *testing.T) {
	doc := read(t, "docs/operations/gateway.md")
	lower := strings.ToLower(doc)

	t.Run("every route", func(t *testing.T) {
		routes := gateway.Routes()
		if len(routes) != 5 {
			t.Fatalf("%d routes", len(routes))
		}
		for _, r := range routes {
			if !strings.Contains(doc, r.Path) {
				t.Errorf("gateway.md does not list the route %s %s", r.Method, r.Path)
			}
		}
		for name, a := range gateway.Families() {
			for _, op := range a.Operations() {
				if !strings.Contains(doc, op.Name) {
					t.Errorf("gateway.md does not name the operation %s of %s", op.Name, name)
				}
			}
		}
	})

	t.Run("the header lists of both adapters", func(t *testing.T) {
		for name, a := range gateway.Families() {
			for _, h := range a.RequestHeaders() {
				if !strings.Contains(lower, strings.ToLower(h)) {
					t.Errorf("gateway.md does not name the request header %s of %s", h, name)
				}
			}
		}
		// The response headers each adapter passes, by name or by prefix.
		for name, probes := range map[string][]string{
			"openai":    {"Content-Type", "Content-Length", "Retry-After", "X-Request-Id", "X-Ratelimit-", "Openai-"},
			"anthropic": {"Content-Type", "Content-Length", "Retry-After", "Request-Id", "X-Should-Retry", "Anthropic-"},
		} {
			a := gateway.Families()[name]
			if a == nil {
				t.Fatalf("no adapter %s", name)
			}
			for _, h := range probes {
				if !a.AllowResponseHeader(h) && !a.AllowResponseHeader(h+"Probe") {
					t.Errorf("the %s adapter does not pass %s: this test's list is stale", name, h)
				}
				if !strings.Contains(lower, strings.ToLower(h)) {
					t.Errorf("gateway.md does not name the response header %s of %s", h, name)
				}
			}
		}
		for _, h := range []string{"X-Api-Key", "Anthropic-Version", "Anthropic-Beta"} {
			if !strings.Contains(doc, h) {
				t.Errorf("gateway.md does not name %s", h)
			}
		}
		if !strings.Contains(lower, "request-id") || !strings.Contains(doc, "upstream_request_id") || !strings.Contains(doc, "usage_output_source") {
			t.Error("gateway.md does not name the upstream request ID header and the log fields of sprint 3.3")
		}
	})

	t.Run("the error type table", func(t *testing.T) {
		// The table of section 5.2 of the design, owned by this test; the adapter must agree with it.
		types := map[string]string{
			"invalid_request": "invalid_request_error", "no_credential": "invalid_request_error", "unauthenticated": "authentication_error",
			"forbidden": "permission_error", "tenant_suspended": "permission_error", "model_not_allowed": "permission_error",
			"endpoint_not_supported": "not_found_error", "request_timeout": "timeout_error", "upstream_timeout": "timeout_error",
			"request_too_large": "request_too_large", "internal": "api_error", "upstream_unreachable": "api_error", "unavailable": "api_error", "rate_limited": "rate_limit_error",
		}
		lines := strings.Split(doc, "\n")
		for _, code := range familycodes.All(t) {
			typ, known := types[code]
			if !known {
				t.Errorf("the code %q has no error type in this test's table", code)
				continue
			}
			if got := anthropic.ErrorType(family.ErrorCode(code)); got != typ {
				t.Errorf("anthropic.ErrorType(%s) = %q, want %q", code, got, typ)
			}
			found := false
			for _, l := range lines {
				found = found || (strings.Contains(l, "`"+code+"`") && strings.Contains(l, "`"+typ+"`"))
			}
			if !found {
				t.Errorf("gateway.md has no line with `%s` and its Anthropic error type `%s`", code, typ)
			}
		}
	})

	t.Run("the base URL table has a row per SDK and family", func(t *testing.T) {
		head, rows := markdownTable(t, doc, "SDK", "Family", "Base URL")
		col := map[string]int{}
		for i, h := range head {
			col[h] = i
		}
		if len(rows) < 3 {
			t.Fatalf("%d rows, want one per SDK and family (OpenAI, Anthropic, a compatible provider)", len(rows))
		}
		pairs, paths, families := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, r := range rows {
			if len(r) != len(head) || r[col["SDK"]] == "" || r[col["Family"]] == "" || r[col["Base URL"]] == "" {
				t.Errorf("an incomplete row: %v", r)
				continue
			}
			pair := r[col["SDK"]] + "|" + r[col["Family"]]
			if pairs[pair] {
				t.Errorf("the pair %q is listed twice", pair)
			}
			pairs[pair] = true
			families[strings.ToLower(r[col["Family"]])] = true
			u := r[col["Base URL"]]
			if i := strings.Index(u, "://"); i >= 0 {
				u = u[i+3:]
			}
			if i := strings.Index(u, "/"); i >= 0 {
				first, rest, _ := strings.Cut(strings.TrimPrefix(u[i:], "/"), "/")
				if first != "openai" && first != "anthropic" { // the example provider of the guide stands for the suite's "compat"
					first = "compat"
				}
				paths["/"+first+"/"+rest] = true
				if rest == "" {
					paths["/"+first] = true
				}
			}
		}
		for _, p := range []string{"/openai/v1", "/anthropic", "/compat/v1"} {
			if !paths[p] {
				t.Errorf("no row with the base URL path %s (the SDK suites build their clients from it): %v", p, paths)
			}
		}
		for _, f := range []string{"openai", "anthropic"} {
			if !families[f] {
				t.Errorf("no row for the family %s", f)
			}
		}
		var sdks []string
		for _, r := range rows {
			sdks = append(sdks, r[col["SDK"]])
		}
		all := strings.ToLower(strings.Join(sdks, " "))
		for _, w := range []string{"openai", "anthropic"} {
			if !strings.Contains(all, w) {
				t.Errorf("the SDK column does not name %s: %v", w, sdks)
			}
		}
	})

	t.Run("the limit of 500 header values", func(t *testing.T) {
		re := regexp.MustCompile(`(?is)500[^.\n|]*header values?`)
		if !re.MatchString(doc) || !strings.Contains(doc, "431") {
			t.Error("gateway.md does not say that a request with more than 500 header values is answered 431 by net/http before Pandokos sees it")
		}
	})
}
