package isolation

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// What a statement is expected to do in the matrix of T-391.
type effect int

const (
	effOK     effect = iota // it runs (and a query sees the rows the design says)
	effNone                 // it runs and changes or finds no row
	effDenied               // SQLSTATE 42501: no privilege, or a row-level security violation
)

func (e effect) String() string { return [...]string{"ok", "no row", "denied (42501)"}[e] }

// arrangeCatalogue creates, as superuser, the rows the matrix works on; it is safe to call again
// and puts back what a successful delete of an earlier case removed.
func arrangeCatalogue(t *testing.T, super *pgx.Conn, a, b tenant.ID) {
	t.Helper()
	for _, sql := range []string{
		`INSERT INTO pandokos.provider (name, api_family) VALUES ('synthetic-pol', 'openai'), ('synthetic-del', 'openai') ON CONFLICT DO NOTHING`,
		`INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url)
		 SELECT p.id, v.name, 'https://pol.example.com' FROM pandokos.provider p, (VALUES ('ep1'), ('ep-del')) AS v (name)
		  WHERE p.name = 'synthetic-pol' ON CONFLICT DO NOTHING`,
		`INSERT INTO pandokos.model (provider_id, name, upstream_id)
		 SELECT p.id, v.name, v.name FROM pandokos.provider p, (VALUES ('pol-model'), ('pol-del'), ('pol-grant'), ('pol-pv-model')) AS v (name)
		  WHERE p.name = 'synthetic-pol' ON CONFLICT DO NOTHING`,
		`DELETE FROM pandokos.model_tenant_grant WHERE model_id IN (SELECT id FROM pandokos.model WHERE name IN ('pol-model', 'pol-grant'))`,
	} {
		if _, err := super.Exec(ctx(t), sql); err != nil {
			t.Fatalf("arrange: %v\n%s", err, sql)
		}
	}
	for _, id := range []tenant.ID{a, b} {
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id) SELECT id, $1::uuid FROM pandokos.model WHERE name = 'pol-model'`, id.String()); err != nil {
			t.Fatalf("arrange the grants: %v", err)
		}
	}
	for _, sql := range []string{
		`INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type)
		 SELECT now(), 'embedded', 'genai-prices-v2', 'success', 'host' WHERE NOT EXISTS (SELECT 1 FROM pandokos.price_import)`,
		`INSERT INTO pandokos.price_version (model_id, currency, source, note)
		 SELECT m.id, 'USD', 'override', 'pol-keep' FROM pandokos.model m WHERE m.name = 'pol-pv-model'
		   AND NOT EXISTS (SELECT 1 FROM pandokos.price_version v WHERE v.note = 'pol-keep')`,
		`INSERT INTO pandokos.price_version (model_id, valid_from, currency, source, note)
		 SELECT m.id, now() + interval '1 day' + (random() * interval '1 hour'), 'USD', 'override', 'pol-del' FROM pandokos.model m WHERE m.name = 'pol-pv-model'
		   AND NOT EXISTS (SELECT 1 FROM pandokos.price_version v WHERE v.note = 'pol-del')`,
	} {
		if _, err := super.Exec(ctx(t), sql); err != nil {
			t.Fatalf("arrange: %v\n%s", err, sql)
		}
	}
}

// TestCatalogPolicies is T-391: for every restricted global table of the catalogue, every
// runtime role and every session (tenant A, tenant B, platform, none, empty), what is read and what
// is written. The expectations are the matrix of section 5.1 and the verified matrix of section
// 12, written here; none is derived from the catalogue of the database.
func TestCatalogPolicies(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	arrangeCatalogue(t, super, a, b)

	sess := []session{
		{"tenant-a", "app.tenant_id", a.String()},
		{"tenant-b", "app.tenant_id", b.String()},
		{"platform", "app.scope", "platform"},
		{"none", "", ""},
		{"empty", "app.tenant_id", ""},
	}
	type tbl struct {
		name      string
		selectBy  []string // roles with SELECT
		insertBy  []string // roles with INSERT, in platform scope only
		updateBy  []string
		deleteBy  []string
		insertSQL string
		updateSQL string
		deleteSQL string
		args      func(s session) []any
	}
	in := func(list []string, role string) bool {
		for _, r := range list {
			if r == role {
				return true
			}
		}
		return false
	}
	rnd := `substr(md5(random()::text), 1, 10)`
	tables := []tbl{
		{name: "provider", selectBy: []string{"gateway", "admin", "worker"}, insertBy: []string{"admin"}, updateBy: []string{"admin"}, deleteBy: []string{"admin"},
			insertSQL: `INSERT INTO pandokos.provider (name, api_family) VALUES ('synthetic-' || ` + rnd + `, 'openai')`,
			updateSQL: `UPDATE pandokos.provider SET description = 'changed' WHERE name = 'synthetic-pol'`,
			deleteSQL: `DELETE FROM pandokos.provider WHERE name = 'synthetic-del'`},
		{name: "provider_endpoint", selectBy: []string{"gateway", "admin"}, insertBy: []string{"admin"}, updateBy: []string{"admin"}, deleteBy: []string{"admin"},
			insertSQL: `INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url) SELECT id, 'ep-' || ` + rnd + `, 'https://x.example.com' FROM pandokos.provider WHERE name = 'synthetic-pol'`,
			updateSQL: `UPDATE pandokos.provider_endpoint SET description = 'changed' WHERE name = 'ep1'`,
			deleteSQL: `DELETE FROM pandokos.provider_endpoint WHERE name = 'ep-del'`},
		{name: "model", selectBy: []string{"gateway", "admin", "worker"}, insertBy: []string{"admin"}, updateBy: []string{"admin"}, deleteBy: []string{"admin"},
			insertSQL: `INSERT INTO pandokos.model (provider_id, name, upstream_id) SELECT id, 'm-' || ` + rnd + `, 'u-' || ` + rnd + ` FROM pandokos.provider WHERE name = 'synthetic-pol'`,
			updateSQL: `UPDATE pandokos.model SET description = 'changed' WHERE name = 'pol-model'`,
			deleteSQL: `DELETE FROM pandokos.model WHERE name = 'pol-del'`},
		{name: "model_tenant_grant", selectBy: []string{"gateway", "admin"}, insertBy: []string{"admin"}, deleteBy: []string{"admin"},
			insertSQL: `INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id) SELECT id, $1::uuid FROM pandokos.model WHERE name = 'pol-grant'`,
			updateSQL: `UPDATE pandokos.model_tenant_grant SET created_at = created_at`,
			deleteSQL: `DELETE FROM pandokos.model_tenant_grant WHERE model_id = (SELECT id FROM pandokos.model WHERE name = 'pol-model')`,
			args:      func(session) []any { return []any{a.String()} }},
		{name: "price_import", selectBy: []string{"admin", "worker"}, insertBy: []string{"admin", "worker"},
			insertSQL: `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type) VALUES (now(), 'file', 'genai-prices-v2', 'success', 'host')`,
			updateSQL: `UPDATE pandokos.price_import SET report = report`,
			deleteSQL: `DELETE FROM pandokos.price_import`},
		{name: "price_version", selectBy: []string{"gateway", "admin", "worker"}, insertBy: []string{"admin", "worker"}, deleteBy: []string{"admin"},
			insertSQL: `INSERT INTO pandokos.price_version (model_id, currency, source) SELECT id, 'USD', 'override' FROM pandokos.model WHERE name = 'pol-pv-model'`,
			updateSQL: `UPDATE pandokos.price_version SET note = note`,
			deleteSQL: `DELETE FROM pandokos.price_version WHERE note = 'pol-del'`},
		{name: "price_rate", selectBy: []string{"gateway", "admin", "worker"}, insertBy: []string{"admin", "worker"},
			insertSQL: `INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate) SELECT id, 'input', (random() * 1000000)::bigint, 1 FROM pandokos.price_version WHERE note = 'pol-keep'`,
			updateSQL: `UPDATE pandokos.price_rate SET rate = rate`,
			deleteSQL: `DELETE FROM pandokos.price_rate`},
	}

	count := func(t *testing.T, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := super.QueryRow(ctx(t), sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// run executes sql in the session as the role. A query returns the number of its rows.
	run := func(t *testing.T, conn *pgx.Conn, s session, sql string, args []any, query bool) (n int64, err error) {
		t.Helper()
		err = inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			if query {
				return tx.QueryRow(c, sql, args...).Scan(&n)
			}
			tag, err := tx.Exec(c, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}

	for _, tb := range tables {
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, s := range sess {
				name := tb.name + "/" + role + "/" + s.name
				argsFor := func() []any {
					if tb.args != nil {
						return tb.args(s)
					}
					return nil
				}
				platform := s.name == "platform"

				t.Run(name+"/select", func(t *testing.T) {
					arrangeCatalogue(t, super, a, b)
					n, err := run(t, conn, s, `SELECT count(*) FROM pandokos.`+tb.name, nil, true)
					if !in(tb.selectBy, role) {
						if sqlstate(err) != insufficientPrivilege {
							t.Fatalf("want %s, got %v", effDenied, err)
						}
						return
					}
					if err != nil {
						t.Fatalf("a read failed: %v", err)
					}
					total := count(t, `SELECT count(*) FROM pandokos.`+tb.name)
					want := total
					switch tb.name {
					case "model_tenant_grant":
						// A tenant's session sees its own rows; the platform scope sees all of them, for the admin role only.
						switch {
						case s.name == "tenant-a":
							want = count(t, `SELECT count(*) FROM pandokos.model_tenant_grant WHERE tenant_id = $1::uuid`, a.String())
						case s.name == "tenant-b":
							want = count(t, `SELECT count(*) FROM pandokos.model_tenant_grant WHERE tenant_id = $1::uuid`, b.String())
						case platform && role == "admin":
							want = total
						default:
							want = 0
						}
					case "price_import":
						if !platform {
							want = 0
						}
					}
					if int(n) != want {
						t.Errorf("sees %d rows, want %d (of %d)", n, want, total)
					}
					if tb.name == "model_tenant_grant" && (s.name == "tenant-a" || s.name == "tenant-b") && want == 0 {
						t.Error("the test proves nothing: the tenant has no grant")
					}
				})

				for _, op := range []struct {
					name  string
					by    []string
					sql   string
					query bool
				}{
					{"insert", tb.insertBy, tb.insertSQL, false},
					{"update", tb.updateBy, tb.updateSQL, false},
					{"delete", tb.deleteBy, tb.deleteSQL, false},
				} {
					t.Run(name+"/"+op.name, func(t *testing.T) {
						arrangeCatalogue(t, super, a, b)
						var args []any
						if op.name == "insert" {
							args = argsFor()
						}
						n, err := run(t, conn, s, op.sql, args, false)
						var want effect
						switch {
						case !in(op.by, role):
							want = effDenied
						case op.name == "insert" && platform:
							want = effOK
						case op.name == "insert":
							want = effDenied // a row-level security violation
						case platform:
							want = effOK
						default:
							want = effNone // the policy hides the rows from the statement
						}
						switch want {
						case effDenied:
							if sqlstate(err) != insufficientPrivilege {
								t.Errorf("want %s, got %v (%d rows)", want, err, n)
							}
						case effNone:
							if err != nil || n != 0 {
								t.Errorf("want %s, got %v (%d rows)", want, err, n)
							}
						case effOK:
							if err != nil || n < 1 {
								t.Errorf("want %s, got %v (%d rows)", want, err, n)
							}
						}
					})
				}
			}
		}
	}

	t.Run("a model of a grant cascades with the model under forced row-level security", func(t *testing.T) {
		arrangeCatalogue(t, super, a, b)
		before := count(t, `SELECT count(*) FROM pandokos.model_tenant_grant`)
		admin := d.Connect(t, "admin")
		_, err := run(t, admin, sess[2], `DELETE FROM pandokos.model WHERE name = 'pol-model'`, nil, false)
		if err != nil {
			t.Fatalf("delete of a granted model: %v", err)
		}
		if after := count(t, `SELECT count(*) FROM pandokos.model_tenant_grant`); after != before-2 {
			t.Errorf("%d grants left, want %d", after, before-2)
		}
	})

	t.Run("the names a provider may not take, and the families", func(t *testing.T) {
		admin := d.Connect(t, "admin")
		insert := func(name, family string) error {
			_, err := run(t, admin, sess[2], `INSERT INTO pandokos.provider (name, api_family) VALUES ($1, $2)`, []any{name, family}, false)
			return err
		}
		for _, bad := range catalog.ReservedProviderNames {
			if got := sqlstate(insert(bad, "openai")); got != checkViolation {
				t.Errorf("the reserved name %q: SQLSTATE %q, want %s", bad, got, checkViolation)
			}
		}
		for _, bad := range []string{"Upper", "1leading", "has_underscore", "has space", strings.Repeat("a", 64), ""} {
			if got := sqlstate(insert(bad, "openai")); got != checkViolation {
				t.Errorf("the name %q: SQLSTATE %q, want %s", bad, got, checkViolation)
			}
		}
		for _, good := range []string{"v2", "v", "azure-openai", "healthz2"} {
			if err := insert("syn-"+good, "openai"); err != nil {
				t.Errorf("the name %q: %v", good, err)
			}
		}
		if got := sqlstate(insert("synthetic-family", "gemini")); got != checkViolation {
			t.Errorf("a family nobody implements: SQLSTATE %q", got)
		}
		if err := insert("synthetic-anthropic", "anthropic"); err != nil {
			t.Errorf("the family anthropic: %v", err)
		}
		// The name and the family never change, not even for the owner.
		for _, sql := range []string{
			`UPDATE pandokos.provider SET name = 'synthetic-renamed' WHERE name = 'synthetic-anthropic'`,
			`UPDATE pandokos.provider SET api_family = 'openai' WHERE name = 'synthetic-anthropic'`,
		} {
			if _, err := super.Exec(ctx(t), sql); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v", sql, err)
			}
		}
		// An endpoint: http only when it is private; no trailing slash; one default per provider.
		ep := func(provider, name, url string, private, def bool) error {
			_, err := run(t, admin, sess[2], `INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url, private, is_default)
				SELECT id, $2, $3, $4, $5 FROM pandokos.provider WHERE name = $1`, []any{provider, name, url, private, def}, false)
			return err
		}
		if got := sqlstate(ep("synthetic-anthropic", "plain", "http://x.example.com", false, false)); got != checkViolation {
			t.Errorf("http for a public endpoint: SQLSTATE %q", got)
		}
		if err := ep("synthetic-anthropic", "plain", "http://x.example.com", true, false); err != nil {
			t.Errorf("http for a private endpoint: %v", err)
		}
		if got := sqlstate(ep("synthetic-anthropic", "slash", "https://x.example.com/", false, false)); got != checkViolation {
			t.Errorf("a trailing slash: SQLSTATE %q", got)
		}
		if err := ep("synthetic-anthropic", "first", "https://x.example.com", false, true); err != nil {
			t.Errorf("a first default: %v", err)
		}
		if got := sqlstate(ep("synthetic-anthropic", "second", "https://y.example.com", false, true)); got != uniqueViolation {
			t.Errorf("a second default: SQLSTATE %q", got)
		}
	})

	t.Run("a change sends catalog or catalog:<tenant> and nothing of the row", func(t *testing.T) {
		arrangeCatalogue(t, super, a, b)
		listener := d.Super(t)
		if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
			t.Fatal(err)
		}
		drain(t, listener, 200*time.Millisecond)
		re := regexp.MustCompile(`^catalog(:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?$`)
		check := func(what string, want ...string) {
			t.Helper()
			got := drain(t, listener, 500*time.Millisecond)
			for p := range got {
				if !re.MatchString(p) {
					t.Errorf("%s: the payload %q is not catalog or catalog:<tenant>", what, p)
				}
			}
			for _, w := range want {
				if !got[w] {
					t.Errorf("%s: %q was not sent (got %v)", what, w, keys(got))
				}
			}
			if len(want) == 0 && len(got) != 0 {
				t.Errorf("%s: sent %v, want nothing", what, keys(got))
			}
		}
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := super.Exec(ctx(t), sql, args...); err != nil {
				t.Fatalf("%v\n%s", err, sql)
			}
		}
		exec(`INSERT INTO pandokos.provider (name, api_family) VALUES ('synthetic-notify', 'openai')`)
		check("a provider", "catalog")
		exec(`UPDATE pandokos.provider SET description = 'n' WHERE name = 'synthetic-notify'`)
		check("an update of a provider", "catalog")
		exec(`INSERT INTO pandokos.provider_endpoint (provider_id, name, base_url) SELECT id, 'ep', 'https://n.example.com' FROM pandokos.provider WHERE name = 'synthetic-notify'`)
		check("an endpoint", "catalog")
		exec(`INSERT INTO pandokos.model (provider_id, name, upstream_id) SELECT id, 'notify-model', 'notify-model' FROM pandokos.provider WHERE name = 'synthetic-notify'`)
		check("a model", "catalog")
		exec(`INSERT INTO pandokos.price_version (model_id, currency, source) SELECT id, 'USD', 'override' FROM pandokos.model WHERE name = 'notify-model'`)
		check("a price version", "catalog")
		exec(`INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id) SELECT id, $1::uuid FROM pandokos.model WHERE name = 'notify-model'`, a.String())
		check("a grant", "catalog:"+a.String())
		exec(`INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id) SELECT id, $1::uuid FROM pandokos.model WHERE name = 'notify-model'`, b.String())
		check("a grant of another tenant", "catalog:"+b.String())
		exec(`DELETE FROM pandokos.model_tenant_grant WHERE tenant_id = $1::uuid`, a.String())
		check("a revocation", "catalog:"+a.String())
		exec(`INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['synthetic-notify/notify-model'])`, a.String())
		check("a restriction", "catalog:"+a.String())
		exec(`UPDATE pandokos.tenant_model_restriction SET models = ARRAY[]::text[] WHERE tenant_id = $1::uuid`, a.String())
		check("a change of a restriction", "catalog:"+a.String())
		exec(`DELETE FROM pandokos.tenant_model_restriction WHERE tenant_id = $1::uuid`, a.String())
		check("the removal of a restriction", "catalog:"+a.String())
		// An import and rates change no cache: the version of a model is what the cache reads.
		exec(`INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type) VALUES (now(), 'file', 'genai-prices-v2', 'success', 'host')`)
		check("an import row")
	})
}

// TestPriceGuards is T-392: the guards of the migration, as seen by the roles that can reach
// them and by the owner of the data (a superuser, who bypasses privileges and row-level security).
func TestPriceGuards(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	super := d.Super(t)
	admin := d.Connect(t, "admin")
	worker := d.Connect(t, "worker")
	platform := session{"platform", "app.scope", "platform"}

	exec := func(conn *pgx.Conn, sql string, args ...any) (int64, error) {
		t.Helper()
		var n int64
		err := inSession(t, conn, platform, func(c context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(c, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	mustSuper := func(sql string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx(t), sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
		return s
	}
	mustSuper(`INSERT INTO pandokos.provider (name, api_family) VALUES ('synthetic-guards', 'openai')`)
	mustSuper(`INSERT INTO pandokos.model (provider_id, name, upstream_id) SELECT id, v, v FROM pandokos.provider, (VALUES ('guard-a'), ('guard-b')) AS m (v) WHERE name = 'synthetic-guards'`)
	modelA := scalar(`SELECT id::text FROM pandokos.model WHERE name = 'guard-a'`)
	modelB := scalar(`SELECT id::text FROM pandokos.model WHERE name = 'guard-b'`)

	version := func(conn *pgx.Conn, model, from, note string) error {
		_, err := exec(conn, `INSERT INTO pandokos.price_version (model_id, valid_from, currency, source, note) VALUES ($1::uuid, `+from+`, 'USD', 'override', $2)`, model, note)
		return err
	}

	t.Run("a version cannot start in the past", func(t *testing.T) {
		for _, c := range []struct {
			role string
			conn *pgx.Conn
		}{{"admin", admin}, {"worker", worker}} {
			if got := sqlstate(version(c.conn, modelA, `now() - interval '1 second'`, "past")); got != checkViolation {
				t.Errorf("%s: a version in the past: SQLSTATE %q, want %s", c.role, got, checkViolation)
			}
		}
		if got := sqlstate(version(admin, modelA, `now() - interval '1 year'`, "past")); got != checkViolation {
			t.Errorf("a version a year ago: SQLSTATE %q", got)
		}
		// Now and later are fine.
		if err := version(admin, modelA, `now()`, "now"); err != nil {
			t.Errorf("a version that starts now: %v", err)
		}
		if err := version(worker, modelA, `now() + interval '1 hour'`, "later"); err != nil {
			t.Errorf("the worker's version that starts later: %v", err)
		}
		// The default of valid_from is now.
		if _, err := exec(admin, `INSERT INTO pandokos.price_version (model_id, currency, source, note) VALUES ($1::uuid, 'EUR', 'override', 'default')`, modelB); err != nil {
			t.Errorf("a version without valid_from: %v", err)
		}
	})

	t.Run("a version never changes", func(t *testing.T) {
		if _, err := exec(admin, `UPDATE pandokos.price_version SET note = 'changed'`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role updates a version: %v", err)
		}
		if _, err := exec(worker, `UPDATE pandokos.price_version SET currency = 'EUR'`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker role updates a version: %v", err)
		}
		// The privilege is not what protects it: a superuser is refused by the guard.
		for _, sql := range []string{
			`UPDATE pandokos.price_version SET note = 'changed'`,
			`UPDATE pandokos.price_version SET valid_from = valid_from + interval '1 day'`,
			`UPDATE pandokos.price_version SET currency = 'GBP'`,
		} {
			if _, err := super.Exec(ctx(t), sql); sqlstate(err) != restrictViolation {
				t.Errorf("%s as a superuser: %v, want SQLSTATE %s", sql, err, restrictViolation)
			}
		}
		if _, err := super.Exec(ctx(t), `TRUNCATE pandokos.price_version`); err == nil {
			t.Error("truncate of the price versions was accepted")
		}
	})

	t.Run("a version that has started cannot be deleted, one that has not can, with its rates", func(t *testing.T) {
		mustSuper(`INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate)
			SELECT id, 'input', 0, 1.5 FROM pandokos.price_version WHERE note IN ('now', 'later')`)
		if _, err := exec(admin, `DELETE FROM pandokos.price_version WHERE note = 'now'`); sqlstate(err) != restrictViolation {
			t.Errorf("deleting a version that has started: %v, want SQLSTATE %s", err, restrictViolation)
		}
		if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.price_version WHERE note = 'now'`); sqlstate(err) != restrictViolation {
			t.Errorf("deleting a version that has started, as a superuser: %v", err)
		}
		n, err := exec(admin, `DELETE FROM pandokos.price_version WHERE note = 'later'`)
		if err != nil || n != 1 {
			t.Fatalf("deleting a version that has not started: %d %v", n, err)
		}
		if got := scalar(`SELECT count(*)::text FROM pandokos.price_rate r WHERE NOT EXISTS (SELECT 1 FROM pandokos.price_version v WHERE v.id = r.price_version_id)`); got != "0" {
			t.Errorf("%s rates without a version", got)
		}
		if got := scalar(`SELECT count(*)::text FROM pandokos.price_rate`); got != "1" {
			t.Errorf("%s rates, want the one of the version that started", got)
		}
		// The worker cannot delete at all.
		if _, err := exec(worker, `DELETE FROM pandokos.price_version`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker role deletes a version: %v", err)
		}
	})

	t.Run("rates never change", func(t *testing.T) {
		for _, c := range []struct {
			role string
			conn *pgx.Conn
		}{{"admin", admin}, {"worker", worker}} {
			if _, err := exec(c.conn, `UPDATE pandokos.price_rate SET rate = 2`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s updates a rate: %v", c.role, err)
			}
			if _, err := exec(c.conn, `DELETE FROM pandokos.price_rate`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s deletes a rate: %v", c.role, err)
			}
		}
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.price_rate SET rate = 2`); sqlstate(err) != restrictViolation {
			t.Errorf("a superuser updates a rate: %v, want SQLSTATE %s", err, restrictViolation)
		}
		if _, err := super.Exec(ctx(t), `TRUNCATE pandokos.price_rate`); sqlstate(err) != restrictViolation {
			t.Errorf("truncate: %v", err)
		}
		// The bounds of a rate and of a threshold, and the categories.
		ins := func(cat string, threshold int64, rate string) error {
			_, err := exec(admin, `INSERT INTO pandokos.price_rate (price_version_id, category, threshold, rate)
				SELECT id, $1, $2, $3::numeric FROM pandokos.price_version WHERE note = 'default'`, cat, threshold, rate)
			return err
		}
		for name, e := range map[string]error{
			"a negative rate":          ins("input", 0, "-0.000000000001"),
			"a rate above a million":   ins("input", 0, "1000000.000000000001"),
			"a negative threshold":     ins("output", -1, "1"),
			"a threshold above 1e8":    ins("output", 100000001, "1"),
			"a category nobody prices": ins("web_search", 0, "1"),
		} {
			if got := sqlstate(e); got != checkViolation {
				t.Errorf("%s: SQLSTATE %q, want %s", name, got, checkViolation)
			}
		}
		for name, e := range map[string]error{
			"a rate of zero":         ins("input", 0, "0"),
			"a million":              ins("output", 0, "1000000"),
			"twelve fraction digits": ins("cache_read", 0, "0.000000000001"),
			"a tier at 1e8":          ins("input", 100000000, "5"),
		} {
			if e != nil {
				t.Errorf("%s: %v", name, e)
			}
		}
		if got := sqlstate(ins("input", 0, "7")); got != uniqueViolation {
			t.Errorf("the same rate twice: SQLSTATE %q", got)
		}
	})

	t.Run("two versions of a model never share an instant", func(t *testing.T) {
		at := `timestamptz '2999-01-01 00:00:00+00'`
		if err := version(admin, modelA, at, "twin"); err != nil {
			t.Fatal(err)
		}
		_, err := exec(admin, `INSERT INTO pandokos.price_version (model_id, valid_from, currency, source) VALUES ($1::uuid, `+at+`, 'EUR', 'override')`, modelA)
		if sqlstate(err) != uniqueViolation {
			t.Errorf("a second version at the same instant: %v", err)
		}
		// Another model may start at that instant.
		if err := version(admin, modelB, at, "twin"); err != nil {
			t.Errorf("another model at the same instant: %v", err)
		}
	})

	t.Run("an import version names its import and an override does not", func(t *testing.T) {
		_, err := exec(admin, `INSERT INTO pandokos.price_version (model_id, currency, source) VALUES ($1::uuid, 'USD', 'import')`, modelA)
		if sqlstate(err) != checkViolation {
			t.Errorf("an import version without an import: %v", err)
		}
		for _, cur := range []string{"usd", "US", "USDD", "U1D"} {
			if _, err := exec(admin, `INSERT INTO pandokos.price_version (model_id, valid_from, currency, source) VALUES ($1::uuid, now() + interval '2 days', $2, 'override')`, modelA, cur); sqlstate(err) != checkViolation {
				t.Errorf("the currency %q: %v", cur, err)
			}
		}
	})

	t.Run("an import row is append-only", func(t *testing.T) {
		ins := `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type, report) VALUES (now(), 'file', 'genai-prices-v2', 'success', 'host', '{"items":[]}')`
		for role, conn := range map[string]*pgx.Conn{"admin": admin, "worker": worker} {
			if _, err := exec(conn, ins); err != nil {
				t.Errorf("%s inserts an import: %v", role, err)
			}
			if _, err := exec(conn, `UPDATE pandokos.price_import SET outcome = 'failure'`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s updates an import: %v", role, err)
			}
			if _, err := exec(conn, `DELETE FROM pandokos.price_import`); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s deletes an import: %v", role, err)
			}
		}
		for _, sql := range []string{`UPDATE pandokos.price_import SET outcome = 'failure'`, `DELETE FROM pandokos.price_import`} {
			if _, err := super.Exec(ctx(t), sql); sqlstate(err) != restrictViolation {
				t.Errorf("%s as a superuser: %v, want SQLSTATE %s", sql, err, restrictViolation)
			}
		}
		if _, err := super.Exec(ctx(t), `TRUNCATE pandokos.price_import`); err == nil {
			t.Error("truncate of the imports was accepted")
		}
		// What the report may hold, and what an import may not claim.
		bad := map[string]string{
			"a report that is not an object": `'[]'`,
			"a report over 64 KiB":           `(SELECT jsonb_build_object('x', string_agg(md5(i::text), '')) FROM generate_series(1, 3000) i)`,
		}
		for name, report := range bad {
			if _, err := exec(admin, `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type, report) VALUES (now(), 'file', 'genai-prices-v2', 'success', 'host', `+report+`::jsonb)`); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := exec(admin, `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type, digest) VALUES (now(), 'file', 'genai-prices-v2', 'success', 'host', 'NOT-A-DIGEST')`); sqlstate(err) != checkViolation {
			t.Errorf("a digest that is not 64 hexadecimal characters: %v", err)
		}
	})

	t.Run("the models of a key are set when it is created and never change", func(t *testing.T) {
		mustSuper(`INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'guards-sa')`, a.String())
		insert := func(models string) string {
			t.Helper()
			var id string
			err := super.QueryRow(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, models)
				SELECT $1::uuid, id, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'), sha256(random()::text::bytea), `+models+`
				  FROM pandokos.service_account WHERE name = 'guards-sa' RETURNING id::text`, a.String()).Scan(&id)
			if err != nil {
				t.Fatalf("%v (models %s)", err, models)
			}
			return id
		}
		restricted := insert(`ARRAY['openai/gpt-4o']`)
		free := insert(`NULL`)
		for name, sql := range map[string]string{
			"another list":  `UPDATE pandokos.virtual_key SET models = ARRAY['openai/gpt-4o-mini'] WHERE id = $1::uuid`,
			"no list":       `UPDATE pandokos.virtual_key SET models = NULL WHERE id = $1::uuid`,
			"an empty list": `UPDATE pandokos.virtual_key SET models = ARRAY[]::text[] WHERE id = $1::uuid`,
		} {
			if _, err := super.Exec(ctx(t), sql, restricted); sqlstate(err) != checkViolation {
				t.Errorf("%s on a restricted key: %v, want SQLSTATE %s", name, err, checkViolation)
			}
		}
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.virtual_key SET models = ARRAY['openai/gpt-4o'] WHERE id = $1::uuid`, free); sqlstate(err) != checkViolation {
			t.Errorf("a list added to a key that had none: %v", err)
		}
		// The admin role cannot touch the column at all, and may still revoke.
		if _, err := exec(admin, `UPDATE pandokos.virtual_key SET models = NULL WHERE id = $1::uuid`, restricted); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role changes the models of a key: %v", err)
		}
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.virtual_key SET revoked_at = now() WHERE id = $1::uuid`, restricted); err != nil {
			t.Errorf("revoking a key with a list: %v", err)
		}
		// Sixty-five references, and a null among them, are refused by the table.
		for name, models := range map[string]string{
			"65 references":    `(SELECT array_agg('p/m' || n) FROM generate_series(1, 65) n)`,
			"a null reference": `ARRAY['p/m', NULL]`,
		} {
			_, err := super.Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, models)
				SELECT $1::uuid, id, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'), sha256(random()::text::bytea), `+models+`
				  FROM pandokos.service_account WHERE name = 'guards-sa'`, a.String())
			if sqlstate(err) != checkViolation {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("a credential names a provider of the catalogue", func(t *testing.T) {
		cred := func(provider string) error {
			_, err := super.Exec(ctx(t), `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint)
				VALUES ($1::uuid, $2, 'guards-' || substr(md5(random()::text), 1, 8), 1, decode(repeat('00', 61), 'hex'), md5(random()::text))`, a.String(), provider)
			return err
		}
		if got := sqlstate(cred("no-such-provider")); got != foreignKey {
			t.Errorf("a credential for a provider that is not in the catalogue: SQLSTATE %q, want %s", got, foreignKey)
		}
		if err := cred("synthetic-guards"); err != nil {
			t.Fatalf("a credential for a provider of the catalogue: %v", err)
		}
		// A provider with credentials is not deleted, by the admin role in platform scope either.
		if _, err := exec(admin, `DELETE FROM pandokos.provider WHERE name = 'synthetic-guards'`); sqlstate(err) != foreignKey {
			t.Errorf("deleting a provider that has credentials: %v", err)
		}
		// A disabled provider still accepts one.
		mustSuper(`UPDATE pandokos.provider SET enabled = false WHERE name = 'synthetic-guards'`)
		if err := cred("synthetic-guards"); err != nil {
			t.Errorf("a credential for a disabled provider: %v", err)
		}
	})

	t.Run("a priced model, and a model with an endpoint under its provider, are not deleted", func(t *testing.T) {
		if _, err := exec(admin, `DELETE FROM pandokos.model WHERE name = 'guard-a'`); sqlstate(err) != foreignKey {
			t.Errorf("deleting a model that has a price version: %v", err)
		}
		if _, err := exec(admin, `DELETE FROM pandokos.provider WHERE name = 'openai'`); sqlstate(err) != foreignKey {
			t.Errorf("deleting a provider that has models: %v", err)
		}
	})

	t.Run("the seed", func(t *testing.T) {
		if got := scalar(`SELECT string_agg(name, ',' ORDER BY name) FROM pandokos.provider WHERE name IN ('openai', 'anthropic')`); got != "anthropic,openai" {
			t.Errorf("seeded providers: %s", got)
		}
		if got := scalar(`SELECT count(*)::text FROM pandokos.model m JOIN pandokos.provider p ON p.id = m.provider_id WHERE p.name IN ('openai', 'anthropic') AND m.availability = 'all' AND m.upstream_id = m.name`); got != "17" {
			t.Errorf("%s seeded models available to all, want 17", got)
		}
		if got := scalar(`SELECT count(*)::text FROM pandokos.provider_endpoint e JOIN pandokos.provider p ON p.id = e.provider_id WHERE p.name IN ('openai', 'anthropic') AND e.is_default AND e.base_url IN ('https://api.openai.com', 'https://api.anthropic.com')`); got != "2" {
			t.Errorf("%s seeded default endpoints, want 2", got)
		}
		if got := scalar(`SELECT count(*)::text FROM pandokos.price_version v JOIN pandokos.model m ON m.id = v.model_id JOIN pandokos.provider p ON p.id = m.provider_id WHERE p.name IN ('openai', 'anthropic')`); got != "0" {
			t.Errorf("%s seeded versions: the migration seeds no price", got)
		}
	})
}
