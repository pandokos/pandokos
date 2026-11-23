package repo

import (
	"encoding/json"
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

	"go.yaml.in/yaml/v3"

	"github.com/pandokos/pandokos/internal/telemetry"
)

// TestMetricsImports is T-R64 (sprint 6.1): the metric SDK, the metric exporter and the semantic
// conventions are imported by internal/telemetry only, and so is the metric API; outside its tests
// internal/telemetry imports no package of internal/ but secret (its tests that need authn or
// session are in the external package telemetry_test, since session reaches telemetry through
// audit); the gateway still imports only the four API packages of OpenTelemetry.
func TestMetricsImports(t *testing.T) {
	imports := importsOf(t)
	only := []string{
		"go.opentelemetry.io/otel/sdk/metric", "go.opentelemetry.io/otel/exporters/otlp/otlpmetric", "go.opentelemetry.io/otel/semconv", "go.opentelemetry.io/otel/metric",
	}
	for pkg, imps := range imports {
		for _, imp := range imps {
			for _, f := range only {
				if strings.HasPrefix(imp, f) && pkg != "internal/telemetry" {
					t.Errorf("%s imports %s: only internal/telemetry may", pkg, imp)
				}
			}
		}
	}
	var sdk, exporter, metricAPI, semconv bool
	for _, imp := range imports["internal/telemetry"] {
		sdk = sdk || strings.HasPrefix(imp, "go.opentelemetry.io/otel/sdk/metric")
		exporter = exporter || strings.HasPrefix(imp, "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp")
		metricAPI = metricAPI || strings.HasPrefix(imp, "go.opentelemetry.io/otel/metric")
		semconv = semconv || strings.HasPrefix(imp, "go.opentelemetry.io/otel/semconv/v1.41.0")
	}
	if !sdk || !exporter || !metricAPI || !semconv {
		t.Errorf("internal/telemetry imports the metric SDK %v, the metric exporter %v, the metric API %v and the conventions v1.41.0 %v", sdk, exporter, metricAPI, semconv)
	}

	for _, imp := range imports["internal/telemetry"] {
		if strings.HasPrefix(imp, productModule) && imp != productModule+"internal/secret" {
			t.Errorf("internal/telemetry imports %s: only internal/secret", imp)
		}
	}

	allowed := map[string]bool{}
	for _, a := range []string{"trace", "attribute", "codes", "propagation"} {
		allowed["go.opentelemetry.io/otel/"+a] = true
	}
	for _, imp := range imports["internal/gateway"] {
		if strings.HasPrefix(imp, "go.opentelemetry.io/") && !allowed[imp] {
			t.Errorf("internal/gateway imports %s", imp)
		}
	}
	for _, d := range internalDeps(t, "gateway") {
		if d == productModule+"internal/telemetry" || strings.HasPrefix(d, "go.opentelemetry.io/otel/sdk/metric") || strings.HasPrefix(d, "go.opentelemetry.io/otel/metric") {
			t.Errorf("internal/gateway reaches %s", d)
		}
	}
}

// metricsRows reads the instrument table of docs/reference/metrics.md: the cells without backticks,
// keyed by the first cell of each row.
func metricsRows(t *testing.T) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	for _, cells := range tableRows(read(t, "docs/reference/metrics.md")) {
		for i := range cells {
			cells[i] = strings.Trim(cells[i], "` ")
		}
		rows[cells[0]] = cells
	}
	return rows
}

// TestMetricsReference is T-R65: docs/reference/metrics.md lists exactly the instruments of
// telemetry.Catalogue() (name, kind, unit, attributes, Prometheus name) and states the pinned version.
func TestMetricsReference(t *testing.T) {
	doc := read(t, "docs/reference/metrics.md")
	if !strings.Contains(doc, "1.41.0") {
		t.Error("docs/reference/metrics.md does not state the pinned version 1.41.0")
	}
	catalogue := telemetry.Catalogue()
	if len(catalogue) != 51 {
		t.Fatalf("telemetry.Catalogue() has %d instruments, want the 51 of the design", len(catalogue))
	}
	rows := metricsRows(t)
	names := map[string]bool{}
	attributes := map[string]bool{}
	for _, spec := range catalogue {
		names[spec.Name] = true
		for _, a := range spec.Attributes {
			attributes[a] = true
		}
	}
	for _, spec := range catalogue {
		row, ok := rows[spec.Name]
		if !ok {
			t.Errorf("docs/reference/metrics.md has no row for %s", spec.Name)
			continue
		}
		text := strings.Join(row, " | ")
		for _, want := range []string{spec.Kind, spec.Unit, spec.PrometheusName} {
			found := false
			for _, c := range row[1:] {
				found = found || c == want
			}
			if want != "" && !found {
				t.Errorf("%s: the row has no cell %q: %s", spec.Name, want, text)
			}
		}
		for _, a := range spec.Attributes {
			if !strings.Contains(text, a) {
				t.Errorf("%s: the row does not name the attribute %s: %s", spec.Name, a, text)
			}
		}
	}
	// No row of the instrument table is an instrument that the catalogue lacks: a first cell that
	// looks like an instrument name (a dotted name of the product's or the conventions' namespaces)
	// and is not an attribute of the catalogue either.
	reName := regexp.MustCompile(`^(pandokos|gen_ai|http\.server)[a-z_.]*$`)
	for first := range rows {
		if reName.MatchString(first) && !names[first] && !attributes[first] && !strings.Contains(first, "{") {
			// A closed-value table lists attribute keys and values; only a name with the shape of an
			// instrument whose Prometheus name is in the row is an instrument.
			for _, c := range rows[first][1:] {
				if strings.HasPrefix(c, "pandokos_") || strings.HasPrefix(c, "gen_ai_") || strings.HasPrefix(c, "http_server_") {
					t.Errorf("docs/reference/metrics.md lists %s, which is not in telemetry.Catalogue()", first)
				}
			}
		}
	}
}

// logCall is a call of a logger of the product: its message and the keys passed to it.
type logCall struct {
	file    string
	line    int
	message string
	keys    []string
}

var logLevels = map[string]bool{"Debug": true, "Info": true, "Warn": true, "Error": true, "DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true, "Log": true}

func stringLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

// logKeys returns the keys of an alternating key and value list: literals, slog.String("k", …) and
// the like, and the keys of slog.Group.
func logKeys(args []ast.Expr) []string {
	var keys []string
	for i := 0; i < len(args); i++ {
		if k, ok := stringLit(args[i]); ok {
			keys = append(keys, k)
			i++
			continue
		}
		if c, ok := args[i].(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "slog" && len(c.Args) > 0 {
					if k, ok := stringLit(c.Args[0]); ok {
						keys = append(keys, k)
						if sel.Sel.Name == "Group" {
							keys = append(keys, logKeys(c.Args[1:])...)
						}
					}
				}
			}
		}
	}
	return keys
}

// scanLogs parses the non-test files of cmd and internal and returns the calls of the loggers and
// the keys placed in slices named attrs (which are passed to a logger further on).
func scanLogs(t *testing.T) (calls []logCall, attrsKeys map[string][]string) {
	t.Helper()
	attrsKeys = map[string][]string{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root(t), dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root(t), path)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || !logLevels[sel.Sel.Name] {
						return true
					}
					args := x.Args
					if strings.HasSuffix(sel.Sel.Name, "Context") && len(args) > 0 {
						args = args[1:]
					}
					if sel.Sel.Name == "Log" && len(x.Args) >= 3 { // logger.Log(ctx, level, msg, …)
						args = x.Args[2:]
					}
					if len(args) == 0 {
						return true
					}
					msg, ok := stringLit(args[0])
					if !ok {
						return true
					}
					calls = append(calls, logCall{file: filepath.ToSlash(rel), line: fset.Position(x.Pos()).Line, message: msg, keys: logKeys(args[1:])})
				case *ast.AssignStmt:
					for i, l := range x.Lhs {
						id, ok := l.(*ast.Ident)
						if !ok || id.Name != "attrs" || i >= len(x.Rhs) {
							continue
						}
						var keys []string
						switch r := x.Rhs[i].(type) {
						case *ast.CompositeLit:
							keys = logKeys(r.Elts)
						case *ast.CallExpr:
							if fn, ok := r.Fun.(*ast.Ident); ok && fn.Name == "append" && len(r.Args) > 1 && !r.Ellipsis.IsValid() {
								keys = logKeys(r.Args[1:])
							}
						}
						attrsKeys[filepath.ToSlash(rel)] = append(attrsKeys[filepath.ToSlash(rel)], keys...)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return calls, attrsKeys
}

// logReference parses docs/reference/logs.md: the rows by message with their fields and platform
// identities, and the glossary of the table under "## Fields".
func logReference(t *testing.T) (rows map[string]struct{ fields, identities map[string]bool }, glossary map[string]bool) {
	t.Helper()
	rows = map[string]struct{ fields, identities map[string]bool }{}
	glossary = map[string]bool{}
	reHeading := regexp.MustCompile("^### `(.+)`$")
	reCode := regexp.MustCompile("`([^`]+)`")
	var cur string
	inGlossary := false
	for _, line := range strings.Split(read(t, "docs/reference/logs.md"), "\n") {
		line = strings.TrimRight(line, " \r")
		switch {
		case strings.HasPrefix(line, "## "):
			inGlossary, cur = strings.TrimSpace(line) == "## Fields", ""
		case reHeading.MatchString(line):
			cur = reHeading.FindStringSubmatch(line)[1]
			rows[cur] = struct{ fields, identities map[string]bool }{map[string]bool{}, map[string]bool{}}
			inGlossary = false
		case inGlossary && strings.HasPrefix(line, "| `"):
			glossary[reCode.FindStringSubmatch(line)[1]] = true
		case cur != "" && strings.HasPrefix(line, "Fields:"):
			for _, m := range reCode.FindAllStringSubmatch(line, -1) {
				rows[cur].fields[m[1]] = true
			}
		case cur != "" && strings.HasPrefix(line, "Platform identities:"):
			for _, m := range reCode.FindAllStringSubmatch(line, -1) {
				rows[cur].identities[m[1]] = true
			}
		}
	}
	return rows, glossary
}

// TestLogReference is T-R66: every message literal of a log call in cmd/ and internal/ is a row of
// docs/reference/logs.md; every key literal passed to a log call, or placed in an attrs slice that is
// passed to one, is in the glossary; the keys that name a tenant or its usage appear only in the rows
// the reference marks as platform identities; and no slog handler is built outside internal/telemetry
// but the discard handler.
func TestLogReference(t *testing.T) {
	calls, attrsKeys := scanLogs(t)
	rows, glossary := logReference(t)
	if len(calls) < 50 || len(rows) < 50 || len(glossary) < 30 {
		t.Fatalf("%d log calls, %d rows, %d fields of the glossary: a reference is missing or empty", len(calls), len(rows), len(glossary))
	}

	forbidden := map[string]bool{}
	for _, k := range []string{"tenant_id", "tenant", "key_id", "principal_id", "service_account_id", "credential_id", "issuer_id", "subject", "model", "upstream_model", "cost", "currency", "input_tokens", "output_tokens"} {
		forbidden[k] = true
	}
	reported := map[string]bool{}
	report := func(key, format string, args ...any) {
		if !reported[key] {
			reported[key] = true
			t.Errorf(format, args...)
		}
	}
	for _, c := range calls {
		row, ok := rows[c.message]
		if !ok {
			report("msg:"+c.message, "%s:%d: the message %q is not a row of docs/reference/logs.md", c.file, c.line, c.message)
			continue
		}
		for _, k := range c.keys {
			if !glossary[k] {
				report("glossary:"+k, "%s:%d: the key %q (of %q) is not in the glossary of docs/reference/logs.md", c.file, c.line, k, c.message)
			}
			if !row.fields[k] && !strings.HasPrefix(k, "!") {
				report("row:"+c.message+k, "%s:%d: the row of %q does not list the field %q", c.file, c.line, c.message, k)
			}
			if forbidden[k] && !row.identities[k] {
				report("forbidden:"+c.message+k, "%s:%d: the key %q is a tenant's identity or usage (ADR 0055) and %q is not marked as a platform identity for it", c.file, c.line, k, c.message)
			}
		}
	}
	logging := map[string]bool{}
	for _, c := range calls {
		logging[c.file] = true
	}
	for file, keys := range attrsKeys {
		if !logging[file] {
			continue // a slice named attrs in a file that writes no log line is not a log's
		}
		for _, k := range keys {
			if !glossary[k] {
				report("glossary:"+k, "%s: the key %q of an attrs slice is not in the glossary of docs/reference/logs.md", file, k)
			}
			if forbidden[k] {
				// The slice is passed to a log call of the same file: the row of that call lists it.
				ok := false
				for _, c := range calls {
					if c.file == file && rows[c.message].identities[k] {
						ok = true
					}
				}
				if !ok {
					report("forbidden-attrs:"+file+k, "%s: the key %q of an attrs slice is a tenant's identity or usage and no log call of the file is marked as a platform identity for it", file, k)
				}
			}
		}
	}
	// The messages the sprint adds or renames are rows (the page committed with the tests has the lines that
	// existed before it).
	for _, msg := range []string{"telemetry error", "metric export failing", "trace export failing", "metrics observation failed", "session count failed"} {
		if _, ok := rows[msg]; !ok {
			t.Errorf("docs/reference/logs.md has no row for %q", msg)
		}
	}

	// One handler: the redacting one of internal/telemetry; discard handlers are allowed.
	err := filepath.WalkDir(filepath.Join(root(t), "."), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", "docs", "planning", "internal/telemetry", "test", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "slog" {
				return true
			}
			switch sel.Sel.Name {
			case "NewJSONHandler", "NewTextHandler":
				t.Errorf("%s:%d: slog.%s builds a handler outside internal/telemetry", rel, fset.Position(c.Pos()).Line, sel.Sel.Name)
			case "New":
				if len(c.Args) == 1 {
					if inner, ok := c.Args[0].(*ast.SelectorExpr); ok && inner.Sel.Name == "DiscardHandler" {
						return true
					}
				}
				t.Errorf("%s:%d: slog.New with a handler other than the discard handler, outside internal/telemetry", rel, fset.Position(c.Pos()).Line)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// panel is a panel of a Grafana dashboard that has queries.
type panel struct {
	Title      string
	Queries    []string
	Datasource string
}

type dashboard struct {
	file      string
	title     string
	panels    []panel
	variables map[string]string // name -> query
	hasSource bool              // a templating variable named datasource
}

func readDashboard(t *testing.T, rel string) dashboard {
	t.Helper()
	var doc struct {
		Title      string            `json:"title"`
		Panels     []json.RawMessage `json:"panels"`
		Templating struct {
			List []struct {
				Name  string          `json:"name"`
				Type  string          `json:"type"`
				Query json.RawMessage `json:"query"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal([]byte(read(t, rel)), &doc); err != nil {
		t.Fatalf("%s is not JSON: %v", rel, err)
	}
	d := dashboard{file: rel, title: doc.Title, variables: map[string]string{}}
	for _, v := range doc.Templating.List {
		q := string(v.Query)
		var s string
		if json.Unmarshal(v.Query, &s) == nil {
			q = s
		} else {
			var o struct {
				Query string `json:"query"`
			}
			if json.Unmarshal(v.Query, &o) == nil {
				q = o.Query
			}
		}
		d.variables[v.Name] = q
		if v.Name == "datasource" && v.Type == "datasource" {
			d.hasSource = true
		}
	}
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var p struct {
			Title      string          `json:"title"`
			Datasource json.RawMessage `json:"datasource"`
			Targets    []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
			Panels []json.RawMessage `json:"panels"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		pn := panel{Title: p.Title, Datasource: string(p.Datasource)}
		for _, tg := range p.Targets {
			pn.Queries = append(pn.Queries, tg.Expr)
		}
		if len(p.Targets) > 0 {
			d.panels = append(d.panels, pn)
		}
		for _, c := range p.Panels {
			walk(c)
		}
	}
	for _, p := range doc.Panels {
		walk(p)
	}
	return d
}

// TestDashboards is T-R67: both dashboards parse; each panel has a query; every metric name in a query
// is a Prometheus name of the catalogue; every label used in by (…), a matcher or a variable is an
// attribute of that metric (after rule R) or job, instance or le; no query adds costs across
// pandokos_currency; the data source is the variable.
func TestDashboards(t *testing.T) {
	catalogue := telemetry.Catalogue()
	if len(catalogue) == 0 {
		t.Fatal("telemetry.Catalogue() is empty")
	}
	byProm := map[string]telemetry.InstrumentSpec{}
	for _, s := range catalogue {
		byProm[s.PrometheusName] = s
	}
	label := func(a string) string { return strings.NewReplacer(".", "_", "-", "_").Replace(a) }
	reName := regexp.MustCompile(`\b((?:gen_ai|http_server|pandokos)_[a-z0-9_:]*)`)
	reBy := regexp.MustCompile(`(?:by|without)\s*\(([^)]*)\)`)
	reMatcher := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:=~|!~|!=|=)\s*"`)
	reLabelValues := regexp.MustCompile(`label_values\(\s*([a-zA-Z_:][a-zA-Z0-9_:]*)\s*,\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\)`)
	strip := func(n string) string {
		for _, suf := range []string{"_bucket", "_sum", "_count"} {
			if _, ok := byProm[n]; ok {
				return n
			}
			n = strings.TrimSuffix(n, suf)
		}
		return n
	}
	always := map[string]bool{"job": true, "instance": true, "le": true, "service_instance_id": true, "__name__": true}

	files := []string{"deploy/compose/observability/grafana/dashboards/pandokos-inference.json", "deploy/compose/observability/grafana/dashboards/pandokos-internals.json"}
	for _, rel := range files {
		d := readDashboard(t, rel)
		if len(d.panels) == 0 {
			t.Errorf("%s has no panel with a query", rel)
		}
		if !d.hasSource {
			t.Errorf("%s has no datasource variable", rel)
		}
		check := func(where, q string) {
			metrics := map[string]telemetry.InstrumentSpec{}
			// Label names in by (...) and in matchers share the prefixes of the metric namespaces
			// (gen_ai_request_model, pandokos_currency): they are checked below and are not metric names.
			names := reMatcher.ReplaceAllString(reBy.ReplaceAllString(q, "by ()"), `"`)
			for _, m := range reName.FindAllString(names, -1) {
				n := strip(m)
				spec, ok := byProm[n]
				if !ok {
					t.Errorf("%s, %s: the query names %s, which is not a Prometheus name of the catalogue", rel, where, m)
					continue
				}
				metrics[n] = spec
			}
			allowed := map[string]bool{}
			for _, spec := range metrics {
				for _, a := range spec.Attributes {
					allowed[label(a)] = true
				}
			}
			var used []string
			for _, m := range reBy.FindAllStringSubmatch(q, -1) {
				for _, l := range strings.Split(m[1], ",") {
					if l = strings.TrimSpace(l); l != "" {
						used = append(used, l)
					}
				}
			}
			for _, m := range reMatcher.FindAllStringSubmatch(q, -1) {
				used = append(used, m[1])
			}
			for _, l := range used {
				if !allowed[l] && !always[l] && len(metrics) > 0 {
					t.Errorf("%s, %s: the label %s is not an attribute of %v", rel, where, l, keysOf(metrics))
				}
			}
			// Costs are never added across currencies.
			if _, ok := metrics["pandokos_gen_ai_client_cost_total"]; ok && regexp.MustCompile(`\b(sum|avg|max|min|count)\b`).MatchString(q) {
				by := reBy.FindAllStringSubmatch(q, -1)
				found := false
				for _, m := range by {
					found = found || strings.Contains(m[1], "pandokos_currency")
				}
				if !found {
					t.Errorf("%s, %s: an aggregation of the cost does not group by pandokos_currency: %s", rel, where, q)
				}
			}
		}
		for _, p := range d.panels {
			if len(p.Queries) == 0 {
				t.Errorf("%s: the panel %q has no query", rel, p.Title)
			}
			if !strings.Contains(p.Datasource, "datasource") {
				t.Errorf("%s: the panel %q does not use the data source variable: %s", rel, p.Title, p.Datasource)
			}
			for _, q := range p.Queries {
				if strings.TrimSpace(q) == "" {
					t.Errorf("%s: the panel %q has an empty query", rel, p.Title)
					continue
				}
				check("panel "+p.Title, q)
			}
		}
		for name, q := range d.variables {
			if m := reLabelValues.FindStringSubmatch(q); m != nil {
				spec, ok := byProm[strip(m[1])]
				if !ok {
					t.Errorf("%s: the variable %s queries %s, which is not a Prometheus name of the catalogue", rel, name, m[1])
					continue
				}
				allowed := false
				for _, a := range spec.Attributes {
					allowed = allowed || label(a) == m[2]
				}
				if !allowed && !always[m[2]] {
					t.Errorf("%s: the variable %s takes the label %s, which is not an attribute of %s", rel, name, m[2], m[1])
				}
			}
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestObservabilityOverlay is T-R68: the overlay's images are pinned by digest; every published port
// is on 127.0.0.1; the collector configuration has no logs pipeline and no exporter but
// otlphttp/prometheus and debug; Prometheus's translation strategy is UnderscoreEscapingWithSuffixes;
// the new containers and the Pandokos service of the overlay keep the hardening of the reference.
func TestObservabilityOverlay(t *testing.T) {
	var doc composeDoc
	if err := yaml.Unmarshal([]byte(read(t, "deploy/compose/observability/compose.yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	digest := regexp.MustCompile(`^[\w./-]+:[\w.-]+@sha256:[0-9a-f]{64}$`)
	for name, repo := range map[string]string{"otel-collector": "otel/opentelemetry-collector", "prometheus": "prom/prometheus", "grafana": "grafana/grafana"} {
		s, ok := doc.Services[name]
		if !ok {
			t.Errorf("the overlay has no service %s", name)
			continue
		}
		if !digest.MatchString(s.Image) || !strings.HasPrefix(s.Image, repo+":") {
			t.Errorf("%s: the image %q is not %s:<tag>@sha256:<digest>", name, s.Image, repo)
		}
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
	for name, s := range doc.Services {
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("%s publishes %q: every published port is on the host loopback", name, p)
			}
		}
	}
	if len(doc.Services["otel-collector"].Ports) != 0 {
		t.Error("the collector publishes a host port")
	}
	if !doc.Networks["observability"].Internal {
		t.Error("the observability network is not internal")
	}

	pandokos, ok := doc.Services["pandokos"]
	if !ok {
		t.Fatal("the overlay does not extend the pandokos service")
	}
	for k, want := range map[string]string{
		"PANDOKOS_TELEMETRY_OTLP_ENDPOINT":           "http://otel-collector:4318",
		"PANDOKOS_TELEMETRY_METRICS_EXPORT_INTERVAL": "10s",
		"PANDOKOS_TELEMETRY_METRICS_PROMETHEUS":      "true",
	} {
		if pandokos.Environment[k] != want {
			t.Errorf("the pandokos service of the overlay has %s = %q, want %q", k, pandokos.Environment[k], want)
		}
	}
	if len(pandokos.Ports) != 0 {
		t.Errorf("the overlay publishes ports of pandokos: %v", pandokos.Ports)
	}
	if pandokos.Image != "" && !strings.Contains(pandokos.Image, "$") && pandokos.Image != doc.Services["pandokos"].Image {
		t.Errorf("the overlay changes the image of pandokos: %s", pandokos.Image)
	}
	overlay := read(t, "deploy/compose/observability/compose.yaml")
	for _, bad := range []string{"privileged", "cap_add", "read_only: false", "no-new-privileges:false"} {
		if strings.Contains(overlay, bad) {
			t.Errorf("the overlay contains %q", bad)
		}
	}

	var collector struct {
		Exporters map[string]any `yaml:"exporters"`
		Service   struct {
			Pipelines map[string]struct {
				Exporters []string `yaml:"exporters"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	if err := yaml.Unmarshal([]byte(read(t, "deploy/compose/observability/otel-collector.yaml")), &collector); err != nil {
		t.Fatal(err)
	}
	if _, ok := collector.Service.Pipelines["logs"]; ok {
		t.Error("the collector has a logs pipeline")
	}
	var exporters []string
	for k := range collector.Exporters {
		exporters = append(exporters, k)
	}
	sort.Strings(exporters)
	if strings.Join(exporters, ",") != "debug,otlphttp/prometheus" {
		t.Errorf("the collector's exporters are %v, want debug and otlphttp/prometheus only", exporters)
	}
	for name, p := range collector.Service.Pipelines {
		for _, e := range p.Exporters {
			if e != "debug" && e != "otlphttp/prometheus" {
				t.Errorf("the pipeline %s exports to %s", name, e)
			}
		}
	}

	var prom struct {
		OTLP struct {
			Strategy string `yaml:"translation_strategy"`
		} `yaml:"otlp"`
	}
	if err := yaml.Unmarshal([]byte(read(t, "deploy/compose/observability/prometheus.yaml")), &prom); err != nil {
		t.Fatal(err)
	}
	if prom.OTLP.Strategy != "UnderscoreEscapingWithSuffixes" {
		t.Errorf("Prometheus's translation strategy is %q", prom.OTLP.Strategy)
	}
	for _, rel := range []string{"grafana/provisioning/datasources/prometheus.yaml", "grafana/provisioning/dashboards/pandokos.yaml"} {
		if _, err := os.Stat(filepath.Join(root(t), "deploy/compose/observability", rel)); err != nil {
			t.Errorf("the overlay lacks %s: %v", rel, err)
		}
	}
}

// historyOf returns the text of the History section of an ADR file by number.
func adrFile(t *testing.T, number string) (rel, text string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root(t), "docs", "adr", number+"-*.md"))
	if err != nil || len(files) != 1 {
		t.Errorf("docs/adr/%s-*.md: %v %v", number, files, err)
		return "", ""
	}
	rel, _ = filepath.Rel(root(t), files[0])
	return filepath.ToSlash(rel), read(t, filepath.ToSlash(rel))
}

// TestMetricsRecords is T-R69: ADR 0062 and 0063 exist and are in the index; ADR 0062 names the
// pinned version, the permission, the credential attribute and the two issues; the ADRs the sprint
// refines have a history line naming 0062 or 0063.
func TestMetricsRecords(t *testing.T) {
	index := read(t, "docs/adr/README.md")
	for number, wants := range map[string][]string{
		"0062": {"1.41.0", "metrics:read", "pandokos.credential.id", "#52", "#32"},
		"0063": {"redact", "docs/reference/logs.md"},
	} {
		rel, text := adrFile(t, number)
		if text == "" {
			continue
		}
		if !strings.Contains(index, "]("+filepath.Base(rel)+")") {
			t.Errorf("docs/adr/README.md does not list ADR %s", number)
		}
		for _, w := range wants {
			if !strings.Contains(text, w) {
				t.Errorf("%s does not mention %q", rel, w)
			}
		}
		if !strings.Contains(text, "- Status: proposed") && !strings.Contains(text, "- Status: accepted") {
			t.Errorf("%s has no status", rel)
		}
	}
	for _, number := range []string{"0015", "0018", "0019", "0040", "0054", "0055", "0060"} {
		rel, text := adrFile(t, number)
		if text == "" {
			continue
		}
		_, history, ok := strings.Cut(text, "## History")
		if !ok {
			t.Errorf("%s has no History section", rel)
			continue
		}
		found := false
		for _, line := range strings.Split(history, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "-") && strings.Contains(line, "sprint 6.1") && (strings.Contains(line, "0062") || strings.Contains(line, "0063")) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no history line of sprint 6.1 that names ADR 0062 or 0063", rel)
		}
	}
}

// TestObservabilityDocuments is T-R70: OBS-07 has its new wording; the data-flows reference has a row
// for the metrics export and one for the scrape endpoint; the telemetry guide names the three keys,
// the version, the endpoint, the permission and the dashboards; the draft release notes of v0.2.0
// announce the version, the keys, the permission and the renamed line.
func TestObservabilityDocuments(t *testing.T) {
	requirements := read(t, "docs/requirements/README.md")
	var obs07 string
	for _, line := range strings.Split(requirements, "\n") {
		if strings.HasPrefix(line, "| OBS-07 |") {
			obs07 = line
		}
	}
	for _, w := range []string{"Tenant, caller and request identifiers are never metric labels", "no setting adds them", "#52"} {
		if !strings.Contains(obs07, w) {
			t.Errorf("OBS-07 does not say %q: %s", w, obs07)
		}
	}
	if strings.Contains(obs07, "opt-in") {
		t.Errorf("OBS-07 still speaks of an opt-in: %s", obs07)
	}

	flows := read(t, "docs/reference/data-flows.md")
	var metricsRow, scrapeRow bool
	for _, cells := range tableRows(flows) {
		first := strings.ToLower(cells[0])
		if strings.Contains(first, "metrics") && strings.Contains(first, "otlp") {
			metricsRow = true
		}
		if strings.Contains(cells[0], "GET /metrics") || strings.Contains(first, "scrape") {
			scrapeRow = true
		}
	}
	if !metricsRow {
		t.Error("docs/reference/data-flows.md has no row for the OTLP metrics export")
	}
	if !scrapeRow {
		t.Error("docs/reference/data-flows.md has no row for the scraper of GET /metrics")
	}

	guide := read(t, "docs/operations/telemetry.md")
	for _, w := range []string{"telemetry.metrics.otlp", "telemetry.metrics.export_interval", "telemetry.metrics.prometheus", "1.41.0", "GET /metrics", "metrics:read",
		"docs/reference/metrics.md", "dashboard", "Grafana", "platform-key create"} {
		if !strings.Contains(guide, w) {
			t.Errorf("docs/operations/telemetry.md does not mention %q", w)
		}
	}
	if strings.Contains(guide, "Metrics come with a later release") {
		t.Error("docs/operations/telemetry.md still says that metrics come with a later release")
	}

	notes := read(t, "docs/releases/v0.2.0-draft.md")
	for _, w := range []string{"1.41.0", "telemetry.metrics.otlp", "telemetry.metrics.export_interval", "telemetry.metrics.prometheus", "metrics:read", "telemetry error", "trace export failing"} {
		if !strings.Contains(notes, w) {
			t.Errorf("docs/releases/v0.2.0-draft.md does not announce %q", w)
		}
	}
}
