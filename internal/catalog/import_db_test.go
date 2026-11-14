package catalog_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/pricing/genaiprices"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func fixtureFile(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "prices", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var cliActor = audit.Actor{Type: audit.ActorHost, Name: "import-prices"}

// env is a migrated database with the catalogue of the import tests: the seed, and a provider
// `synthetic` (dataset provider synthetic) with models that the fixtures price or do not. The
// superuser connection is reached through two functions, because only internal/store may name the
// database driver (depguard).
type env struct {
	t      *testing.T
	d      *dbtest.Database
	admin  *store.DB
	worker *store.DB
	exec   func(q string, args ...any) error
	scan   func(q string, args ...any) func(dest ...any) error
}

func newEnv(t *testing.T, models ...string) *env {
	t.Helper()
	d := dbtest.NewMigrated(t)
	return newEnvOn(t, d, d, models...)
}

// newEnvOn is newEnv on a migrated database d, with the roles opened through the view p (the
// database itself, or its pooled view).
func newEnvOn(t *testing.T, d, p *dbtest.Database, models ...string) *env {
	t.Helper()
	conn := d.Super(t)
	e := &env{t: t, d: p, admin: p.Open(t, store.RoleAdmin), worker: p.Open(t, store.RoleWorker)}
	e.exec = func(q string, args ...any) error {
		_, err := conn.Exec(ctx(t), q, args...)
		return err
	}
	e.scan = func(q string, args ...any) func(dest ...any) error { return conn.QueryRow(ctx(t), q, args...).Scan }
	e.sql(`INSERT INTO pandokos.provider (name, api_family, dataset_provider) VALUES ('synthetic', 'openai', 'synthetic')`)
	if len(models) == 0 {
		models = []string{"synthetic-chat", "synthetic-tiered", "synthetic-nomatch"}
	}
	for _, m := range models {
		e.sql(`INSERT INTO pandokos.model (provider_id, name, upstream_id) SELECT id, $1, $1 FROM pandokos.provider WHERE name = 'synthetic'`, m)
	}
	return e
}

func (e *env) sql(q string, args ...any) {
	e.t.Helper()
	if err := e.exec(q, args...); err != nil {
		e.t.Fatalf("%v\n%s", err, q)
	}
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.scan(q, args...)(&n); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return n
}

// versionRow is a version of a model with its rates as "category@threshold=rate".
type versionRow struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Currency  string    `json:"currency"`
	ImportID  string    `json:"import_id"`
	ValidFrom time.Time `json:"valid_from"`
	Rates     []string  `json:"rates"`
}

// versions returns the versions of a model, oldest first.
func (e *env) versions(t *testing.T, provider, model string) []versionRow {
	t.Helper()
	var doc string
	if err := e.scan(`SELECT COALESCE(jsonb_agg(jsonb_build_object('id', v.id, 'source', v.source, 'currency', v.currency, 'import_id', COALESCE(v.import_id::text, ''), 'valid_from', v.valid_from,
		'rates', COALESCE((SELECT jsonb_agg(r.category || '@' || r.threshold || '=' || trim(trailing '.' FROM trim(trailing '0' FROM r.rate::text)) ORDER BY r.category, r.threshold)
		                     FROM pandokos.price_rate r WHERE r.price_version_id = v.id), '[]'::jsonb)) ORDER BY v.valid_from), '[]'::jsonb)::text
		FROM pandokos.price_version v JOIN pandokos.model m ON m.id = v.model_id JOIN pandokos.provider p ON p.id = m.provider_id
		WHERE p.name = $1 AND m.name = $2`, provider, model)(&doc); err != nil {
		t.Fatal(err)
	}
	var out []versionRow
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type auditRow struct {
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	ActorType    string         `json:"actor_type"`
	ActorName    string         `json:"actor_name"`
	RequestID    string         `json:"request_id"`
	Detail       map[string]any `json:"detail"`
}

// audit returns the platform audit rows about prices, oldest first.
func (e *env) audit(t *testing.T) []auditRow {
	t.Helper()
	var doc string
	if err := e.scan(`SELECT COALESCE(jsonb_agg(jsonb_build_object('action', action, 'resource_type', resource_type, 'resource_id', resource_id, 'actor_type', actor_type,
		'actor_name', actor_name, 'request_id', request_id, 'detail', detail) ORDER BY id), '[]'::jsonb)::text FROM pandokos.platform_audit_event WHERE action LIKE 'price%'`)(&doc); err != nil {
		t.Fatal(err)
	}
	var out []auditRow
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func actions(rows []auditRow, action string) []auditRow {
	var out []auditRow
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func item(r catalog.Report, provider, model string) (catalog.ReportItem, bool) {
	for _, it := range r.Items {
		if it.Provider == provider && it.Model == model {
			return it, true
		}
	}
	return catalog.ReportItem{}, false
}

// TestImportDB is T-394 (exit condition 3): an import that changes a price adds a version and an
// audit record, and nothing else.
func TestImportDB(t *testing.T) {
	base, changed := fixtureFile(t, "base.json"), fixtureFile(t, "changed.json")

	t.Run("base, again, changed", func(t *testing.T) {
		e := newEnv(t)
		sum := sha256.Sum256(base)

		rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "/data/base.json", Data: base}, catalog.ImportOptions{Actor: cliActor, RequestID: "req-394-a"})
		if err != nil {
			t.Fatal(err)
		}
		// 17 seeded models and three of the test; four are priced by the document.
		if rep.ImportID == "" || rep.Digest != hex.EncodeToString(sum[:]) || rep.ModelsSeen != 20 || rep.Created != 4 || rep.Unchanged != 0 || rep.Overridden != 0 || rep.Unmatched != 16 || rep.Skipped != 0 {
			t.Errorf("report: %+v", rep)
		}
		for _, c := range []struct{ provider, model, outcome string }{
			{"openai", "gpt-4o", "created"}, {"openai", "o4-mini", "created"}, {"synthetic", "synthetic-chat", "created"}, {"synthetic", "synthetic-tiered", "created"},
			{"synthetic", "synthetic-nomatch", "unmatched"}, {"anthropic", "claude-opus-4-5", "unmatched"}, {"openai", "gpt-5", "unmatched"},
		} {
			if it, ok := item(rep, c.provider, c.model); !ok || it.Outcome != c.outcome {
				t.Errorf("%s/%s: %+v (found %v), want %s", c.provider, c.model, it, ok, c.outcome)
			}
		}
		if it, _ := item(rep, "openai", "gpt-4o"); it.DatasetModel != "gpt-4o" {
			t.Errorf("the dataset model of gpt-4o: %q", it.DatasetModel)
		}
		wantRates := map[string][]string{
			"openai/gpt-4o":              {"cache_read@0=1.225", "input@0=2.45", "output@0=9.8"},
			"openai/o4-mini":             {"cache_read@0=0.29", "input@0=1.15", "output@0=4.6"},
			"synthetic/synthetic-chat":   {"input@0=0.5", "output@0=1.5"},
			"synthetic/synthetic-tiered": {"input@0=1", "input@100000=2", "output@0=3", "output@100000=6"},
		}
		for key, want := range wantRates {
			provider, model, _ := strings.Cut(key, "/")
			vs := e.versions(t, provider, model)
			if len(vs) != 1 || vs[0].Source != "import" || vs[0].Currency != "USD" || vs[0].ImportID != rep.ImportID || strings.Join(vs[0].Rates, ",") != strings.Join(want, ",") {
				t.Errorf("%s: %+v, want rates %v", key, vs, want)
			}
			if age := time.Since(vs[0].ValidFrom); age < -time.Minute || age > time.Minute {
				t.Errorf("%s: valid_from %v is not the time of the import", key, vs[0].ValidFrom)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 4 {
			t.Errorf("%d versions, want 4 (an import prices catalogue models only and creates no model)", n)
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.model`); n != 20 {
			t.Errorf("%d models: an import created or removed a model", n)
		}
		rows := e.audit(t)
		if len(actions(rows, "price.create")) != 4 || len(actions(rows, "price_import.run")) != 1 || len(rows) != 5 {
			t.Fatalf("audit rows: %+v", rows)
		}
		for _, r := range actions(rows, "price.create") {
			if r.ResourceType != "price_version" || r.ActorType != "host" || r.ActorName != "import-prices" || r.RequestID != "req-394-a" || r.Detail["import_id"] != rep.ImportID || r.Detail["source"] != "import" || r.Detail["currency"] != "USD" {
				t.Errorf("price.create: %+v", r)
			}
		}
		run := actions(rows, "price_import.run")[0]
		if run.ResourceType != "price_import" || run.ResourceID != rep.ImportID || run.Detail["source"] != "file" || run.Detail["source_ref"] != "base.json" || run.Detail["digest"] != rep.Digest || run.Detail["dry_run"] != false {
			t.Errorf("price_import.run: %+v (the file is named by its base name)", run)
		}
		var outcome, source, format, ref string
		var created int
		if err := e.scan(`SELECT outcome, source, dataset_format, versions_created, source_ref FROM pandokos.price_import WHERE id = $1::uuid`, rep.ImportID)(&outcome, &source, &format, &created, &ref); err != nil ||
			outcome != "success" || source != "file" || format != "genai-prices-v2" || created != 4 {
			t.Errorf("the import row: %s %s %s %d %v", outcome, source, format, created, err)
		}

		// Again: nothing new, the same data.
		time.Sleep(5 * time.Millisecond)
		rep2, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: base}, catalog.ImportOptions{Actor: cliActor})
		if err != nil || rep2.Created != 0 || rep2.Unchanged != 4 || rep2.ImportID == rep.ImportID || rep2.Unmatched != 16 {
			t.Errorf("second import: %+v %v", rep2, err)
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 4 {
			t.Errorf("the second import created versions: %d", n)
		}
		if got := actions(e.audit(t), "price.create"); len(got) != 4 {
			t.Errorf("the second import wrote price.create rows: %d", len(got))
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_import`); n != 2 {
			t.Errorf("%d import rows: every run is recorded", n)
		}

		// changed.json changes the output price of gpt-4o and nothing else.
		time.Sleep(5 * time.Millisecond)
		before := e.versions(t, "openai", "gpt-4o")
		rep3, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "changed.json", Data: changed}, catalog.ImportOptions{Actor: cliActor, RequestID: "req-394-c"})
		if err != nil || rep3.Created != 1 || rep3.Unchanged != 3 || rep3.Unmatched != 16 {
			t.Fatalf("third import: %+v %v", rep3, err)
		}
		if it, _ := item(rep3, "openai", "gpt-4o"); it.Outcome != "created" {
			t.Errorf("gpt-4o: %+v", it)
		}
		vs := e.versions(t, "openai", "gpt-4o")
		if len(vs) != 2 || vs[0].ID != before[0].ID || strings.Join(vs[0].Rates, ",") != "cache_read@0=1.225,input@0=2.45,output@0=9.8" || strings.Join(vs[1].Rates, ",") != "cache_read@0=1.225,input@0=2.45,output@0=10.4" || !vs[1].ValidFrom.After(vs[0].ValidFrom) {
			t.Errorf("gpt-4o versions: %+v (the old one is unchanged, the new one is newer)", vs)
		}
		if len(e.versions(t, "openai", "o4-mini")) != 1 || len(e.versions(t, "synthetic", "synthetic-chat")) != 1 {
			t.Error("a model that did not change got a version")
		}
		creates := actions(e.audit(t), "price.create")
		if len(creates) != 5 {
			t.Fatalf("%d price.create rows, want 5", len(creates))
		}
		last := creates[4]
		if last.ResourceID != vs[1].ID || last.Detail["import_id"] != rep3.ImportID || !strings.Contains(mustJSON(last.Detail), vs[0].ID) {
			t.Errorf("the row of the new version of gpt-4o: %+v (it names the previous version %s)", last, vs[0].ID)
		}
		if got := actions(e.audit(t), "price_import.run"); len(got) != 3 {
			t.Errorf("%d run rows: one per run that came from the CLI", len(got))
		}
	})

	t.Run("an override is never changed by an import", func(t *testing.T) {
		e := newEnv(t)
		if _, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: base}, catalog.ImportOptions{Actor: cliActor}); err != nil {
			t.Fatal(err)
		}
		// The operator overrides synthetic-chat: a version and the flag, as the handler does.
		e.sql(`INSERT INTO pandokos.price_version (model_id, valid_from, currency, source, note) SELECT id, now(), 'EUR', 'override', 'negotiated' FROM pandokos.model WHERE name = 'synthetic-chat'`)
		e.sql(`INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate) SELECT v.id, 'input', 0, 0.1 FROM pandokos.price_version v WHERE v.source = 'override'`)
		e.sql(`UPDATE pandokos.model SET price_override = true WHERE name = 'synthetic-chat'`)
		moved := strings.Replace(string(base), `"input_mtok": 0.5`, `"input_mtok": 0.6`, 1)
		time.Sleep(5 * time.Millisecond)
		rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "moved.json", Data: []byte(moved)}, catalog.ImportOptions{Actor: cliActor})
		if err != nil || rep.Overridden != 1 || rep.Created != 0 || rep.Unchanged != 3 {
			t.Fatalf("report: %+v %v", rep, err)
		}
		if it, _ := item(rep, "synthetic", "synthetic-chat"); it.Outcome != "overridden" {
			t.Errorf("synthetic-chat: %+v", it)
		}
		if vs := e.versions(t, "synthetic", "synthetic-chat"); len(vs) != 2 || vs[1].Source != "override" || vs[1].Currency != "EUR" {
			t.Errorf("an import touched an overridden model: %+v", vs)
		}
		// Cleared, the next import prices it again, with a new version.
		e.sql(`UPDATE pandokos.model SET price_override = false WHERE name = 'synthetic-chat'`)
		rep, err = catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "moved.json", Data: []byte(moved)}, catalog.ImportOptions{Actor: cliActor})
		if err != nil || rep.Created != 1 || rep.Overridden != 0 {
			t.Fatalf("after the clearing: %+v %v", rep, err)
		}
		if vs := e.versions(t, "synthetic", "synthetic-chat"); len(vs) != 3 || vs[2].Source != "import" || vs[2].Currency != "USD" {
			t.Errorf("versions after the clearing: %+v", vs)
		}
	})

	t.Run("what cannot be represented is reported, not fatal", func(t *testing.T) {
		e := newEnv(t, "synthetic-plain", "synthetic-all-categories", "synthetic-variants", "synthetic-other-category", "synthetic-negative", "synthetic-no-supported-price",
			"synthetic-time-of-day", "synthetic-scheduled", "synthetic-conditional", "synthetic-sw-1", "synthetic-rx-20250101", "synthetic-lb")
		rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "shapes.json", Data: fixtureFile(t, "shapes.json")}, catalog.ImportOptions{Actor: cliActor})
		if err != nil {
			t.Fatal(err)
		}
		reasons := func(model string) string {
			it, ok := item(rep, "synthetic", model)
			if !ok {
				return "(absent)"
			}
			var rs []string
			for _, is := range it.Issues {
				rs = append(rs, is.Reason)
			}
			return it.Outcome + ":" + strings.Join(dedupe(rs), ",")
		}
		for model, want := range map[string]string{
			"synthetic-variants":           "created:price_variants_ignored",
			"synthetic-other-category":     "created:category_ignored",
			"synthetic-negative":           "skipped:value_not_representable",
			"synthetic-no-supported-price": "skipped:no_supported_price",
			"synthetic-time-of-day":        "created:time_of_day_price_ignored",
			"synthetic-scheduled":          "created:scheduled_price_not_imported",
		} {
			if got := reasons(model); got != want {
				t.Errorf("%s: %s, want %s", model, got, want)
			}
		}
		if got := reasons("synthetic-lb"); !strings.HasPrefix(got, "unmatched:") {
			t.Errorf("synthetic-lb (a pattern RE2 refuses matches nothing): %s", got)
		}
		if got := reasons("synthetic-plain"); got != "created:" {
			t.Errorf("synthetic-plain: %s", got)
		}
		if rep.Skipped != 2 || rep.Created < 8 {
			t.Errorf("report counts: %+v", rep)
		}
		// A skipped model has no version; the others do, and every version validates.
		if len(e.versions(t, "synthetic", "synthetic-negative")) != 0 || len(e.versions(t, "synthetic", "synthetic-no-supported-price")) != 0 {
			t.Error("a skipped model got a version")
		}
		if vs := e.versions(t, "synthetic", "synthetic-all-categories"); len(vs) != 1 || len(vs[0].Rates) != 7 {
			t.Errorf("synthetic-all-categories: %+v", vs)
		}
		// The report as stored holds identifiers only.
		var report string
		if err := e.scan(`SELECT report::text FROM pandokos.price_import WHERE id = $1::uuid`, rep.ImportID)(&report); err != nil || !strings.Contains(report, "synthetic-negative") {
			t.Errorf("the stored report: %v %s", err, report)
		}
	})

	t.Run("a dry run writes nothing", func(t *testing.T) {
		e := newEnv(t)
		rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: base}, catalog.ImportOptions{Actor: cliActor, DryRun: true})
		if err != nil || rep.Created != 4 || rep.Unmatched != 16 || rep.ImportID != "" || len(rep.Items) == 0 {
			t.Fatalf("dry run: %+v %v", rep, err)
		}
		for _, tbl := range []string{"price_version", "price_rate", "price_import", "platform_audit_event"} {
			want := 0
			if tbl == "platform_audit_event" {
				want = e.count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action LIKE 'price%'`)
				if want != 0 {
					t.Errorf("audit rows of prices after a dry run: %d", want)
				}
				continue
			}
			if n := e.count(t, `SELECT count(*) FROM pandokos.`+tbl); n != want {
				t.Errorf("%s has %d rows after a dry run", tbl, n)
			}
		}
		// The same run for real, then a dry run: it reports the difference to the database.
		if _, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: base}, catalog.ImportOptions{Actor: cliActor}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		rep, err = catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "changed.json", Data: changed}, catalog.ImportOptions{Actor: cliActor, DryRun: true})
		if err != nil || rep.Created != 1 || rep.Unchanged != 3 {
			t.Errorf("dry run of a change: %+v %v", rep, err)
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 4 {
			t.Errorf("the dry run created a version: %d", n)
		}
	})

	t.Run("two imports at once serialize and the second creates nothing", func(t *testing.T) {
		e := newEnv(t)
		var wg sync.WaitGroup
		reps := make([]catalog.Report, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range reps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				reps[i], errs[i] = catalog.Import(context.Background(), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: base}, catalog.ImportOptions{Actor: cliActor})
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("import %d: %v", i, err)
			}
		}
		if reps[0].Created+reps[1].Created != 4 || reps[0].Unchanged+reps[1].Unchanged != 4 || (reps[0].Created != 0 && reps[1].Created != 0) {
			t.Errorf("the two reports: %+v / %+v (exactly one run creates the versions)", reps[0], reps[1])
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 4 {
			t.Errorf("%d versions after two concurrent imports", n)
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'price.create'`); n != 4 {
			t.Errorf("%d price.create rows", n)
		}
	})

	t.Run("the worker role imports in platform scope, with its own identity", func(t *testing.T) {
		e := newEnv(t)
		actor := audit.Actor{Type: audit.ActorHost, Name: "price-import-at-start"}
		rep, err := catalog.Import(ctx(t), e.worker, catalog.Source{Kind: "embedded", Data: genaiprices.Snapshot()}, catalog.ImportOptions{Actor: actor})
		if err != nil {
			t.Fatalf("the worker role: %v", err)
		}
		// The embedded snapshot prices every seeded model, with an input rate.
		seeded := 0
		for _, it := range rep.Items {
			if it.Provider == "synthetic" {
				continue
			}
			if it.Outcome != "created" {
				t.Errorf("%s/%s: %s", it.Provider, it.Model, it.Outcome)
			}
			seeded++
		}
		if seeded != 17 || rep.Created != 17 {
			t.Errorf("%d seeded models in the report, %d created", seeded, rep.Created)
		}
		var withInput int
		if err := e.scan(`SELECT count(DISTINCT m.id) FROM pandokos.model m JOIN pandokos.price_version v ON v.model_id = m.id JOIN pandokos.price_rate r ON r.price_version_id = v.id
			WHERE r.category = 'input' AND r.threshold = 0 AND m.name <> ALL (ARRAY['synthetic-chat', 'synthetic-tiered', 'synthetic-nomatch'])`)(&withInput); err != nil || withInput != 17 {
			t.Errorf("%d seeded models with an input rate: %v", withInput, err)
		}
		rows := e.audit(t)
		if len(actions(rows, "price.create")) != 17 {
			t.Errorf("%d price.create rows", len(actions(rows, "price.create")))
		}
		// The worker's rows carry its identity; a run that created versions is audited, one that did not is not.
		for _, r := range rows {
			if r.ActorType != "host" || r.ActorName != "price-import-at-start" {
				t.Errorf("the actor of %s: %s %s", r.Action, r.ActorType, r.ActorName)
			}
		}
		if len(actions(rows, "price_import.run")) != 1 {
			t.Errorf("run rows after a run that created versions: %d", len(actions(rows, "price_import.run")))
		}
		time.Sleep(5 * time.Millisecond)
		if rep, err := catalog.Import(ctx(t), e.worker, catalog.Source{Kind: "embedded", Data: genaiprices.Snapshot()}, catalog.ImportOptions{Actor: actor}); err != nil || rep.Created != 0 || rep.Unchanged != 17 {
			t.Errorf("the same snapshot again: %+v %v", rep, err)
		}
		if got := actions(e.audit(t), "price_import.run"); len(got) != 1 {
			t.Errorf("a start-up run that changed nothing wrote a run row: %d", len(got))
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_import WHERE source = 'embedded'`); n != 2 {
			t.Errorf("%d embedded import rows: every run is recorded", n)
		}
		// And the admin role of the platform (the API) always writes the run row.
		time.Sleep(5 * time.Millisecond)
		api := audit.Actor{Type: audit.ActorPlatform, ID: "pabcdefghijklmnop", Name: "ops", KeyID: "pabcdefghijklmnop"}
		if _, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "embedded", Data: genaiprices.Snapshot()}, catalog.ImportOptions{Actor: api, RequestID: "req-api"}); err != nil {
			t.Fatal(err)
		}
		runs := actions(e.audit(t), "price_import.run")
		if len(runs) != 2 || runs[1].ActorType != "platform" || runs[1].RequestID != "req-api" {
			t.Errorf("run rows: %+v", runs)
		}
	})

	t.Run("a dataset that cannot be read is a failed run and no version", func(t *testing.T) {
		e := newEnv(t)
		for name, doc := range map[string][]byte{"invalid.json": fixtureFile(t, "invalid.json"), "not json": []byte("synthetic-marker-not-json")} {
			rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: name, Data: doc}, catalog.ImportOptions{Actor: cliActor})
			if !errors.Is(err, catalog.ErrDatasetInvalid) {
				t.Errorf("%s: error %v, want ErrDatasetInvalid", name, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-marker-not-json") {
				t.Errorf("the error quotes the document: %v", err)
			}
			_ = rep
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_version`); n != 0 {
			t.Errorf("%d versions after failed runs", n)
		}
		var outcome, class, digest string
		if err := e.scan(`SELECT outcome, error_class, digest FROM pandokos.price_import ORDER BY id LIMIT 1`)(&outcome, &class, &digest); err != nil || outcome != "failure" || class != "dataset_invalid" || len(digest) != 64 {
			t.Errorf("the failed run: %s %s %s %v", outcome, class, digest, err)
		}
		if n := e.count(t, `SELECT count(*) FROM pandokos.price_import WHERE outcome = 'failure'`); n != 2 {
			t.Errorf("%d failed runs recorded, want 2", n)
		}
	})

	t.Run("a model with a dataset provider that is empty is not imported", func(t *testing.T) {
		e := newEnv(t)
		e.sql(`UPDATE pandokos.provider SET dataset_provider = '' WHERE name = 'synthetic'`)
		rep, err := catalog.Import(ctx(t), e.admin, catalog.Source{Kind: "file", Ref: "base.json", Data: base}, catalog.ImportOptions{Actor: cliActor})
		if err != nil || rep.ModelsSeen != 17 || rep.Created != 2 {
			t.Errorf("%+v %v", rep, err)
		}
		if _, ok := item(rep, "synthetic", "synthetic-chat"); ok {
			t.Error("a model of a provider without a dataset provider is in the report")
		}
	})
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
