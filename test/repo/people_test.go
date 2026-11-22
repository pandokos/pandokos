package repo

import (
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

	"github.com/pandokos/pandokos/internal/config"
)

// importersOf returns, for every non-test Go file of the repository (the nested modules and the
// planning and docs trees aside), the packages of the module that it imports.
func productFiles(t *testing.T) map[string][]string {
	t.Helper()
	files := map[string][]string{}
	err := filepath.WalkDir(root(t), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", "planning", "docs", "local", "bin", "test/sdk":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		var imports []string
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			imports = append(imports, p)
		}
		files[rel] = imports
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 150 {
		t.Fatalf("only %d Go files were read", len(files))
	}
	return files
}

// TestArgon2InOnePlace is T-R53: Argon2id lives in internal/password and nowhere else, and the
// linter says the same. (The count of direct requirements is TestDependencies.)
func TestArgon2InOnePlace(t *testing.T) {
	const crypto = "golang.org/x/crypto"
	found := false
	for rel, imports := range productFiles(t) {
		for _, p := range imports {
			if p != crypto && !strings.HasPrefix(p, crypto+"/") {
				continue
			}
			if !strings.HasPrefix(rel, "internal/password/") {
				t.Errorf("%s imports %s: only internal/password may", rel, p)
				continue
			}
			if p != crypto+"/argon2" {
				t.Errorf("%s imports %s: only the sub-package argon2 is used", rel, p)
			}
			found = found || p == crypto+"/argon2"
		}
	}
	if !found {
		t.Error("internal/password does not import golang.org/x/crypto/argon2")
	}
	// Test files of other packages do not use it either: hashes are made through internal/password.
	err := filepath.WalkDir(root(t), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", "planning", "docs", "local", "bin", "test/sdk":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "internal/password/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); strings.HasPrefix(p, crypto) {
				t.Errorf("%s imports %s", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lint := read(t, ".golangci.yml")
	if !strings.Contains(lint, "argon2-only-in-password") || !strings.Contains(lint, crypto) || !strings.Contains(lint, "internal/password") {
		t.Error(".golangci.yml does not carry the depguard rule argon2-only-in-password for golang.org/x/crypto and internal/password")
	}
	gomod := read(t, "go.mod")
	if n := strings.Count(gomod, crypto+" "); n != 1 || regexp.MustCompile(`(?m)^\s*`+regexp.QuoteMeta(crypto)+` v\S+ // indirect`).MatchString(gomod) {
		t.Errorf("go.mod requires %s %d times, want once and directly", crypto, n)
	}
}

// TestPeopleImports is T-R54: the import rules of section 5.16. The five packages of the people are
// imported by internal/admin and cmd/pandokos (and by each other as their signatures show, and by
// the test support packages), never by the planes of the gateway.
func TestPeopleImports(t *testing.T) {
	const mod = modulePath + "/internal/"
	allowed := map[string][]string{
		"password": {"internal/admin", "internal/account", "cmd/pandokos"},
		"session":  {"internal/admin", "cmd/pandokos"},
		"account":  {"internal/admin", "internal/session", "cmd/pandokos"},
		"oidc":     {"internal/admin", "cmd/pandokos"},
		"idem":     {"internal/admin", "internal/audit", "cmd/pandokos"},
	}
	forbidden := []string{"internal/gateway", "internal/resolver", "internal/family", "internal/authn", "internal/jwks", "internal/limits", "internal/ledger", "internal/vault", "internal/cli"}
	used := map[string]bool{}
	for rel, imports := range productFiles(t) {
		dir := filepath.ToSlash(filepath.Dir(rel))
		for _, p := range imports {
			name, ok := strings.CutPrefix(p, mod)
			if !ok {
				continue
			}
			name, _, _ = strings.Cut(name, "/")
			list, people := allowed[name]
			if !people {
				continue
			}
			used[name] = true
			for _, f := range forbidden {
				if dir == f || strings.HasPrefix(dir, f+"/") {
					t.Errorf("%s imports internal/%s: the gateway plane and the libraries it shares know no people", rel, name)
				}
			}
			ok = strings.HasPrefix(dir, "test/") || dir == "internal/"+name
			for _, a := range list {
				ok = ok || dir == a || strings.HasPrefix(dir, a+"/")
			}
			if !ok {
				t.Errorf("%s imports internal/%s: only %v (and the test support packages) may", rel, name, list)
			}
		}
	}
	for name := range allowed {
		if !used[name] {
			t.Errorf("nothing imports internal/%s", name)
		}
	}
	// go-jose stays where it is: the relying party verifies through internal/authn and imports none.
	for rel, imports := range productFiles(t) {
		if !strings.HasPrefix(rel, "internal/oidc/") {
			continue
		}
		for _, p := range imports {
			if strings.HasPrefix(p, "github.com/go-jose/") {
				t.Errorf("%s imports %s: internal/oidc verifies the ID token through internal/authn", rel, p)
			}
		}
	}
}

// TestGatewayReadsNoCookie is T-R55: no file of the gateway plane names a cookie.
func TestGatewayReadsNoCookie(t *testing.T) {
	re := regexp.MustCompile(`\bCookie\b|\bSetCookie\b|Set-Cookie|\bCookies\b|\bAddCookie\b`)
	files := nonTestGoFiles(t, []string{"internal/gateway", "internal/resolver", "internal/family", "internal/authn"})
	if len(files) < 30 {
		t.Fatalf("%d files of the gateway plane", len(files))
	}
	for _, rel := range files {
		if loc := re.FindStringIndex(read(t, rel)); loc != nil {
			t.Errorf("%s names a cookie (%q): the inference listener reads none", rel, read(t, rel)[loc[0]:loc[1]])
		}
	}
}

// TestSignInGuide is T-R56: the guide of sign-in names what an operator and a client author need, and
// the four records exist and are indexed.
func TestSignInGuide(t *testing.T) {
	doc := read(t, "docs/operations/sign-in.md")
	var keys []string
	for _, k := range config.Keys() {
		if strings.HasPrefix(k.Name, "auth.") {
			keys = append(keys, k.Name)
		}
	}
	keys = append(keys, "listeners.admin.trusted_proxies", "tokens.admin_listener", "idempotency.retention")
	if len(keys) < 27 {
		t.Fatalf("%d keys of section 5.15", len(keys))
	}
	for _, k := range keys {
		if !strings.Contains(doc, k) {
			t.Errorf("sign-in.md does not name %s", k)
		}
	}
	for _, code := range []string{"too_many_attempts", "password_change_required", "csrf_rejected", "sign_in_disabled", "idempotency_key_reused", "idempotency_in_progress", "idempotency_response_lost"} {
		if !strings.Contains(doc, code) {
			t.Errorf("sign-in.md does not name the code %s", code)
		}
	}
	for _, want := range []string{"__Host-pandokos_session", "X-Pandokos-Csrf", "Origin", "IAM-11", "Idempotency-Key", "init --admin-username", "user reset-password", "user unlock",
		"SameSite", "trusted_proxies", "auth.public_origin"} {
		if !strings.Contains(doc, want) {
			t.Errorf("sign-in.md does not mention %s", want)
		}
	}
	index := read(t, "docs/adr/README.md")
	for _, n := range []string{"0056", "0057", "0058", "0059"} {
		matches, _ := filepath.Glob(filepath.Join(root(t), "docs", "adr", n+"-*.md"))
		if len(matches) != 1 {
			t.Errorf("record %s: %d files", n, len(matches))
			continue
		}
		base := filepath.Base(matches[0])
		if !strings.Contains(index, "]("+base+")") {
			t.Errorf("docs/adr/README.md does not index %s", base)
		}
		body, _ := os.ReadFile(matches[0])
		if !regexp.MustCompile(`(?m)^- Status: proposed\s*$`).Match(body) {
			t.Errorf("%s is not proposed", base)
		}
	}
	if !strings.Contains(read(t, "docs/reference/admin-api.md"), "sign-in.md") && !strings.Contains(read(t, "README.md"), "sign-in.md") {
		t.Error("neither the reference nor the README links the sign-in guide")
	}
}

// TestBrowserJobInCI is T-R57: the browser job runs make test-browser, which fails, and does not skip,
// without Chrome.
func TestBrowserJobInCI(t *testing.T) {
	text := read(t, ".github/workflows/ci.yml")
	jobs := map[string]string{}
	header := regexp.MustCompile(`^  ([a-z-]+):$`)
	current := ""
	for _, line := range strings.Split(text, "\n") {
		if m := header.FindStringSubmatch(line); m != nil {
			current = m[1]
		}
		jobs[current] += line + "\n"
	}
	job, ok := jobs["browser"]
	if !ok {
		t.Fatal("ci.yml has no job browser")
	}
	for _, want := range []string{"test/db/up.sh", "make test-browser", "google-chrome --version"} {
		if !strings.Contains(job, want) {
			t.Errorf("job browser does not run %s", want)
		}
	}
	mk := read(t, "Makefile")
	if !regexp.MustCompile(`(?m)^test-browser:`).MatchString(mk) || !strings.Contains(mk, "-tags browser") || !regexp.MustCompile(`(?m)^\.PHONY:.*\btest-browser\b`).MatchString(mk) {
		t.Error("the Makefile has no phony target test-browser that runs the tests with -tags browser")
	}
	files, err := filepath.Glob(filepath.Join(root(t), "test", "browser", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no test in test/browser: %v", err)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		src := string(b)
		if !strings.Contains(src, "//go:build browser") {
			t.Errorf("%s does not carry the build tag browser", f)
		}
		if strings.Contains(src, "t.Skip") || strings.Contains(src, "tb.Skip") || strings.Contains(src, "SkipNow") {
			t.Errorf("%s skips: the tests behind the tag browser fail without Chrome", f)
		}
	}
	for _, f := range []string{"test/browser/page/index.html", "test/browser/page/attacker.html"} {
		if !strings.Contains(read(t, f), "fetch(") {
			t.Errorf("%s does not use fetch", f)
		}
	}
	if !strings.Contains(read(t, "test/browser/page/index.html"), "result") {
		t.Error("the minimal page does not write its result")
	}
}

// TestNoPeopleInTelemetry is T-R58 (ADR 0055): no log call and no span attribute of the product names a
// username, a user ID, a subject, an e-mail address, a group or a source address.
func TestNoPeopleInTelemetry(t *testing.T) {
	forbidden := map[string]bool{
		"username": true, "user_id": true, "user": true, "subject": true, "email": true, "group": true, "groups": true,
		"source_address": true, "source_ip": true, "remote_addr": true, "remote_address": true, "client_ip": true, "peer_address": true, "x_forwarded_for": true,
	}
	logMethods := map[string]bool{"Debug": true, "Info": true, "Warn": true, "Error": true, "DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true, "Log": true, "LogAttrs": true}
	attrFuncs := map[string]bool{"String": true, "Int": true, "Int64": true, "Bool": true, "Any": true, "Float64": true, "Duration": true, "Time": true, "Key": true, "StringSlice": true}
	n := 0
	for rel := range productFiles(t) {
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			continue
		}
		if strings.HasPrefix(rel, "internal/adminapi/") || strings.HasPrefix(rel, "internal/config/") || strings.HasPrefix(rel, "internal/cli/") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root(t), rel), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv := ""
			if id, ok := sel.X.(*ast.Ident); ok {
				recv = id.Name
			}
			isLog := logMethods[sel.Sel.Name] && len(call.Args) > 0
			isAttr := (recv == "slog" || recv == "attribute" || recv == "attr") && attrFuncs[sel.Sel.Name]
			if !isLog && !isAttr {
				return true
			}
			n++
			for _, a := range call.Args {
				lit, ok := a.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if key, err := strconv.Unquote(lit.Value); err == nil && forbidden[strings.ToLower(key)] {
					t.Errorf("%s:%d: %s(…%q…) names %s: operational telemetry holds no data about people (ADR 0055)", rel, fset.Position(call.Pos()).Line, sel.Sel.Name, key, key)
				}
			}
			return true
		})
	}
	if n < 100 {
		t.Errorf("only %d log calls and span attributes were examined", n)
	}
	// The admin request line names a user only on the platform: the line is written in one place.
	src := read(t, "internal/admin/admin.go")
	if !strings.Contains(src, `"admin request"`) {
		t.Error("the admin request line is not in internal/admin/admin.go")
	}
	keys := regexp.MustCompile(`"principal_id"`).FindAllString(src, -1)
	sort.Strings(keys)
	if len(keys) != 1 {
		t.Errorf("the admin request line names principal_id %d times, want once, in the platform branch", len(keys))
	}
}
