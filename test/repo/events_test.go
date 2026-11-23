package repo

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/outbox"
	"github.com/pandokos/pandokos/internal/telemetry"
)

// The nine types of event of sprint 6.4: the three of a tenant's log, the five of the platform's and
// the test event.
var (
	tenantEventTypes   = []string{"io.pandokos.limit.exhausted.v1", "io.pandokos.limit.threshold_reached.v1", "io.pandokos.usage.recorded.v1"}
	platformEventTypes = []string{"io.pandokos.kek.version_activated.v1", "io.pandokos.kek.version_retired.v1", "io.pandokos.tenant.deleted.v1", "io.pandokos.tenant.deletion_cancelled.v1", "io.pandokos.tenant.deletion_requested.v1"}
	testEventType      = "io.pandokos.destination.test.v1"
)

func allEventTypes() []string {
	all := append(append(append([]string{}, tenantEventTypes...), platformEventTypes...), testEventType)
	sort.Strings(all)
	return all
}

var eventTypeRe = regexp.MustCompile(`io\.pandokos\.[a-z_]+(?:\.[a-z_]+)+\.v[0-9]+`)

func setOf(text string) []string {
	seen := map[string]bool{}
	for _, m := range eventTypeRe.FindAllString(text, -1) {
		seen[m] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEventsOffTheRequestPath is T-R81: the packages of the request path do not import
// internal/outbox, and no statement of the product writes the log of events: the database does.
func TestEventsOffTheRequestPath(t *testing.T) {
	for _, dir := range []string{"internal/gateway", "internal/ledger", "internal/limits", "internal/resolver"} {
		files := nonTestGoFiles(t, []string{dir})
		if len(files) == 0 {
			t.Fatalf("no file in %s", dir)
		}
		for _, rel := range files {
			paths, _, _ := importsAndNames(t, rel)
			for _, p := range paths {
				if p == modulePath+"/internal/outbox" {
					t.Errorf("%s imports internal/outbox: the request path never touches events", rel)
				}
			}
		}
	}
	write := regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from|truncate(\s+table)?)\s+(only\s+)?(pandokos\.)?(outbox_event|outbox_stream|platform_outbox_event|platform_outbox_stream)\b`)
	sqlFiles, err := filepath.Glob(filepath.Join(root(t), "internal/store/queries/sql/*.sql"))
	if err != nil || len(sqlFiles) == 0 {
		t.Fatalf("the statements of the product: %v", err)
	}
	for _, f := range sqlFiles {
		if m := write.FindString(read(t, relPath(t, f))); m != "" {
			t.Errorf("%s holds a statement that writes the log of events (%q): the database writes events, Go does not", relPath(t, f), strings.Join(strings.Fields(m), " "))
		}
	}
	for _, dir := range []string{"cmd", "internal"} {
		for _, f := range goFiles(t, dir) {
			rel := relPath(t, f)
			if strings.HasPrefix(rel, "internal/store/migrations/") || strings.HasPrefix(rel, "internal/store/queries/") {
				continue
			}
			if m := write.FindString(read(t, rel)); m != "" {
				t.Errorf("%s holds SQL that writes the log of events (%q)", rel, strings.Join(strings.Fields(m), " "))
			}
		}
	}
	// Only migration 0017 creates the triggers and functions that write the logs.
	for _, f := range sqlFilesIn(t, "internal/store/migrations/sql") {
		base := filepath.Base(f)
		if strings.HasPrefix(base, "0017_") {
			continue
		}
		if m := write.FindString(read(t, relPath(t, f))); m != "" {
			t.Errorf("%s writes the log of events (%q): only migration 0017 does", base, strings.Join(strings.Fields(m), " "))
		}
	}
}

func sqlFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root(t), dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestEventReference is T-R82: the types of the table check of migration 0017, of the schema
// directory, of docs/reference/events.md and of the OpenAPI enumerations are the same set; the
// published schemas equal the embedded ones.
func TestEventReference(t *testing.T) {
	want := allEventTypes()

	if got := outbox.Types(); !sameStrings(got, tenantEventTypes) {
		t.Errorf("outbox.Types() is %v, want the three types of a tenant's log %v", got, tenantEventTypes)
	}
	// The table check of the destinations: the tenant types and no other.
	migration := read(t, "internal/store/migrations/sql/0017_events.sql")
	check := regexp.MustCompile(`(?s)event_destination_event_types_valid CHECK \((.*?)\),\n\s+CONSTRAINT`).FindStringSubmatch(migration)
	if check == nil {
		t.Fatal("migration 0017 has no check event_destination_event_types_valid")
	}
	if got := setOf(check[1]); !sameStrings(got, tenantEventTypes) {
		t.Errorf("the table check of the destinations lists %v, want %v", got, tenantEventTypes)
	}
	// The embedded schemas: one file per type, named by the type without its prefix.
	var files []string
	entries, err := os.ReadDir(filepath.Join(root(t), "internal/outbox/schema"))
	if err != nil {
		t.Fatalf("the schema directory: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "schema.go" { // the leaf package that embeds the files
			continue
		}
		if !strings.HasSuffix(name, ".schema.json") {
			t.Errorf("internal/outbox/schema/%s is not a schema file", name)
			continue
		}
		files = append(files, "io.pandokos."+strings.TrimSuffix(name, ".schema.json"))
	}
	sort.Strings(files)
	if !sameStrings(files, want) {
		t.Errorf("the schema directory holds %v, want %v", files, want)
	}
	// The published schemas are the embedded ones.
	for _, typ := range want {
		name := strings.TrimPrefix(typ, "io.pandokos.") + ".schema.json"
		embedded, ok := outbox.Schema(typ)
		if !ok {
			t.Errorf("outbox.Schema(%q) has no schema", typ)
			continue
		}
		if got := read(t, "docs/reference/events/"+name); got != string(embedded) {
			t.Errorf("docs/reference/events/%s differs from the embedded schema: make generate copies it", name)
		}
		if got := read(t, "internal/outbox/schema/"+name); got != string(embedded) {
			t.Errorf("internal/outbox/schema/%s differs from outbox.Schema", name)
		}
	}
	// The reference lists every type.
	if got := setOf(read(t, "docs/reference/events.md")); !sameStrings(got, want) {
		t.Errorf("docs/reference/events.md names the types %v, want %v", got, want)
	}
	// The OpenAPI enumerations: EventType (a tenant's) and PlatformEventType.
	spec := read(t, "api/openapi/admin.v1.yaml")
	block := func(name string) string {
		m := regexp.MustCompile(`(?ms)^    ` + name + `:\n(.*?)(?:^    [A-Z]|\z)`).FindStringSubmatch(spec)
		if m == nil {
			t.Fatalf("the OpenAPI document has no schema %s", name)
		}
		return m[1]
	}
	if got := setOf(block("EventType")); !sameStrings(got, tenantEventTypes) {
		t.Errorf("the enumeration EventType is %v, want %v", got, tenantEventTypes)
	}
	if got := setOf(block("PlatformEventType")); !sameStrings(got, platformEventTypes) {
		t.Errorf("the enumeration PlatformEventType is %v, want %v", got, platformEventTypes)
	}
}

// TestOutboxEgress is T-R83: internal/outbox constructs no http.Transport, net.Dialer or tls.Config,
// and its client refuses redirects: every request goes through the egress guard.
func TestOutboxEgress(t *testing.T) {
	files := nonTestGoFiles(t, []string{"internal/outbox"})
	if len(files) == 0 {
		t.Fatal("no file in internal/outbox")
	}
	fset := token.NewFileSet()
	banned := map[string]bool{"http.Transport": true, "net.Dialer": true, "tls.Config": true, "http.DefaultTransport": true, "net.ListenConfig": true}
	sawGuard := false
	for _, rel := range files {
		f := parseFile(t, fset, filepath.Join(root(t), rel))
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if sel, ok := x.Type.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && banned[id.Name+"."+sel.Sel.Name] {
						t.Errorf("%s: %s builds a %s.%s: take the transport of the egress guard", rel, fset.Position(x.Pos()), id.Name, sel.Sel.Name)
					}
				}
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && banned[id.Name+"."+x.Sel.Name] && id.Name+"."+x.Sel.Name == "http.DefaultTransport" {
					t.Errorf("%s: %s uses http.DefaultTransport", rel, fset.Position(x.Pos()))
				}
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" && x.Sel.Name == "ErrUseLastResponse" {
					sawGuard = true
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "net" && (sel.Sel.Name == "Dial" || sel.Sel.Name == "DialTimeout") {
						t.Errorf("%s: %s dials: the egress guard does", rel, fset.Position(x.Pos()))
					}
				}
			}
			return true
		})
	}
	if !sawGuard {
		t.Error("internal/outbox never returns http.ErrUseLastResponse: its client must refuse redirects")
	}
	text := ""
	for _, rel := range files {
		text += read(t, rel)
	}
	if !strings.Contains(text, "CheckRedirect") {
		t.Error("internal/outbox sets no CheckRedirect on its client: a redirect would be followed")
	}
}

// TestEventDocs is T-R84: the failure reference has the rows "event destination" and "worker crash in
// a delivery", citing tests that exist; the configuration, log and metrics references list the keys,
// lines and instruments of section 5.15.
func TestEventDocs(t *testing.T) {
	tests := map[string]bool{}
	for _, f := range testFiles(t) {
		for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(read(t, relPath(t, f)), -1) {
			tests[m[1]] = true
		}
	}
	doc := read(t, "docs/reference/failure-behaviour.md")
	for _, subject := range []string{"event destination", "worker crash in a delivery"} {
		found := false
		for _, line := range strings.Split(doc, "\n") {
			if !strings.HasPrefix(line, "|") || !strings.Contains(strings.ToLower(strings.SplitN(strings.Trim(line, "| "), "|", 2)[0]), subject) {
				continue
			}
			found = true
			cited := regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(line, -1)
			if len(cited) == 0 {
				t.Errorf("the row %q cites no test", subject)
			}
			for _, c := range cited {
				if !tests[c[1]] {
					t.Errorf("the row %q cites %s, which is not a test of the repository", subject, c[1])
				}
			}
		}
		if !found {
			t.Errorf("the failure reference has no row for %q", subject)
		}
	}
	// The keys, the lines and the instruments of section 5.15.
	cfg := read(t, "docs/operations/configuration.md")
	n := 0
	for _, k := range config.Keys() {
		if strings.HasPrefix(k.Name, "events.") {
			n++
			if !strings.Contains(cfg, k.Name) || !strings.Contains(cfg, k.Env) {
				t.Errorf("configuration.md does not list %s and %s", k.Name, k.Env)
			}
		}
	}
	if n != 9 {
		t.Errorf("%d events keys, want 9", n)
	}
	logs := read(t, "docs/reference/logs.md")
	for _, line := range []string{"event relay ready", "event relay failing", "event relay recovered", "event deliveries failing", "event retention applied", "event retention failing"} {
		if !strings.Contains(logs, "`"+line+"`") {
			t.Errorf("logs.md has no row for %q", line)
		}
	}
	metrics := read(t, "docs/reference/metrics.md")
	for _, spec := range telemetry.Catalogue() {
		if strings.HasPrefix(spec.Name, "pandokos.events.") && !strings.Contains(metrics, spec.Name) {
			t.Errorf("metrics.md has no row for %s", spec.Name)
		}
	}
	for _, name := range []string{"pandokos.events.deliveries", "pandokos.events.delivery.duration", "pandokos.events.dead_letters", "pandokos.events.expired", "pandokos.events.destinations", "pandokos.events.lag", "pandokos.events.relay.passes"} {
		if !strings.Contains(metrics, name) {
			t.Errorf("metrics.md does not name %s", name)
		}
	}
	for _, v := range append(append([]string{}, telemetry.DeliveryOutcomes...), "outbox_event", "limit_event", "platform_event") {
		if !strings.Contains(metrics, v) {
			t.Errorf("metrics.md does not name the value %s", v)
		}
	}
}
