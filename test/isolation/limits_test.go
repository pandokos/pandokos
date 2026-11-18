package isolation

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestLimitRulePolicies is T-626: platform_limit_rule for each runtime role and session, who
// writes, the checks and the columns that can change. limit_rule itself is covered by the
// generic negative tests through its fixture.
func TestLimitRulePolicies(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		super := d.Super(t)
		a, b := d.SeedTenant(t, "limits-a"), d.SeedTenant(t, "limits-b")
		sessA, sessB := sessions(a)[0], session{name: "tenant-b", setting: "app.tenant_id", value: b.String()}
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		empty := session{name: "empty", setting: "app.tenant_id", value: ""}

		for _, sql := range []string{
			`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'requests_per_minute', 1000)`,
			`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'tokens_per_minute', 5000)`,
		} {
			if _, err := super.Exec(ctx(t), sql); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{a.String(), b.String()} {
			if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES ($1::uuid, 'concurrent_requests', 7)`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value) VALUES ($1::uuid, 'tenant', '', 'requests_per_minute', 10)`, id); err != nil {
				t.Fatal(err)
			}
		}

		labels := func(rows []map[string]any) []string {
			var out []string
			for _, r := range rows {
				who := "default"
				if r["tenant_id"] != nil {
					switch r["tenant_id"] {
					case a.String():
						who = "a"
					case b.String():
						who = "b"
					default:
						who = "other"
					}
				}
				out = append(out, who+"/"+r["metric"].(string))
			}
			sort.Strings(out)
			return out
		}
		const read = `SELECT tenant_id::text AS tenant_id, metric FROM pandokos.platform_limit_rule`
		equal := func(got, want []string) bool {
			if len(got) != len(want) {
				return false
			}
			for i := range got {
				if got[i] != want[i] {
					return false
				}
			}
			return true
		}

		for _, role := range []string{"gateway", "admin"} {
			conn := d.Connect(t, role)
			for _, c := range []struct {
				sess session
				want []string
			}{
				{sessA, []string{"a/concurrent_requests", "default/requests_per_minute", "default/tokens_per_minute"}},
				{sessB, []string{"b/concurrent_requests", "default/requests_per_minute", "default/tokens_per_minute"}},
				{noSession, []string{"default/requests_per_minute", "default/tokens_per_minute"}},
				{empty, []string{"default/requests_per_minute", "default/tokens_per_minute"}},
			} {
				rows, err := query(t, conn, c.sess, read)
				if err != nil {
					t.Errorf("%s in session %s: %v", role, c.sess.name, err)
					continue
				}
				if got := labels(rows); !equal(got, c.want) {
					t.Errorf("%s in session %s sees %v, want %v", role, c.sess.name, got, c.want)
				}
			}
		}
		t.Run("the platform scope of the admin role sees every row", func(t *testing.T) {
			rows, err := query(t, d.Connect(t, "admin"), platform, read)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"a/concurrent_requests", "b/concurrent_requests", "default/requests_per_minute", "default/tokens_per_minute"}
			if got := labels(rows); !equal(got, want) {
				t.Errorf("sees %v, want %v", got, want)
			}
		})
		t.Run("the gateway role in the platform scope sees what a tenant-less session sees", func(t *testing.T) {
			rows, err := query(t, d.Connect(t, "gateway"), platform, read)
			if err != nil {
				t.Fatal(err)
			}
			if got := labels(rows); len(got) != 2 {
				t.Errorf("sees %v, want the two defaults", got)
			}
		})
		t.Run("limit_rule shows a tenant its own rows only", func(t *testing.T) {
			for _, role := range []string{"gateway", "admin"} {
				rows, err := query(t, d.Connect(t, role), sessA, `SELECT tenant_id::text AS tenant_id FROM pandokos.limit_rule`)
				if err != nil || len(rows) != 1 || rows[0]["tenant_id"] != a.String() {
					t.Errorf("%s: %v %v", role, rows, err)
				}
				rows, err = query(t, d.Connect(t, role), noSession, `SELECT 1 AS n FROM pandokos.limit_rule`)
				if err != nil || len(rows) != 0 {
					t.Errorf("%s without a session: %v %v", role, rows, err)
				}
			}
		})

		t.Run("the worker role reads nothing of either table", func(t *testing.T) {
			wk := d.Connect(t, "worker")
			for _, tbl := range []string{"limit_rule", "platform_limit_rule"} {
				for _, sess := range []session{sessA, platform, noSession} {
					if _, err := query(t, wk, sess, `SELECT count(*) FROM `+table(tbl)); sqlstate(err) != insufficientPrivilege {
						t.Errorf("worker, session %s, SELECT %s: %v, want SQLSTATE %s", sess.name, tbl, err, insufficientPrivilege)
					}
				}
			}
		})

		t.Run("the gateway and the worker write nothing", func(t *testing.T) {
			for _, role := range []string{"gateway", "worker"} {
				conn := d.Connect(t, role)
				for _, sess := range []session{sessA, platform, noSession} {
					for what, sql := range map[string]string{
						"INSERT limit_rule":          `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value) VALUES ('` + a.String() + `', 'tenant', '', 'tokens_per_minute', 5)`,
						"UPDATE limit_rule":          `UPDATE pandokos.limit_rule SET limit_value = 1`,
						"DELETE limit_rule":          `DELETE FROM pandokos.limit_rule`,
						"TRUNCATE limit_rule":        `TRUNCATE pandokos.limit_rule`,
						"INSERT platform_limit_rule": `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'concurrent_requests', 5)`,
						"UPDATE platform_limit_rule": `UPDATE pandokos.platform_limit_rule SET limit_value = 1`,
						"DELETE platform_limit_rule": `DELETE FROM pandokos.platform_limit_rule`,
						"TRUNCATE platform":          `TRUNCATE pandokos.platform_limit_rule`,
					} {
						if _, err := execIn(t, conn, sess, sql); sqlstate(err) != insufficientPrivilege {
							t.Errorf("%s, session %s, %s: %v, want SQLSTATE %s", role, sess.name, what, err, insufficientPrivilege)
						}
					}
				}
			}
		})

		ad := d.Connect(t, "admin")
		t.Run("the admin role writes platform_limit_rule in the platform scope only", func(t *testing.T) {
			for _, sess := range []session{sessA, noSession, empty} {
				if _, err := execIn(t, ad, sess, `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'concurrent_requests', 3)`); sqlstate(err) != insufficientPrivilege {
					t.Errorf("session %s: INSERT: %v, want SQLSTATE %s", sess.name, err, insufficientPrivilege)
				}
				for what, sql := range map[string]string{
					"UPDATE": `UPDATE pandokos.platform_limit_rule SET limit_value = 1`,
					"DELETE": `DELETE FROM pandokos.platform_limit_rule`,
				} {
					n, err := execIn(t, ad, sess, sql)
					if (err != nil && sqlstate(err) != insufficientPrivilege) || (err == nil && n != 0) {
						t.Errorf("session %s: %s changed %d rows (%v)", sess.name, what, n, err)
					}
				}
			}
			if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.platform_limit_rule`); n != "4" {
				t.Errorf("%s rows after the refused writes, want 4", n)
			}
			if _, err := execIn(t, ad, platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'concurrent_requests', 3)`); err != nil {
				t.Errorf("platform scope INSERT: %v", err)
			}
			if n, err := execIn(t, ad, platform, `UPDATE pandokos.platform_limit_rule SET limit_value = 9 WHERE tenant_id IS NULL AND metric = 'concurrent_requests'`); err != nil || n != 1 {
				t.Errorf("platform scope UPDATE: %d %v", n, err)
			}
			if n, err := execIn(t, ad, platform, `DELETE FROM pandokos.platform_limit_rule WHERE tenant_id IS NULL AND metric = 'concurrent_requests'`); err != nil || n != 1 {
				t.Errorf("platform scope DELETE: %d %v", n, err)
			}
		})

		t.Run("the checks", func(t *testing.T) {
			ins := func(sess session, sql string, args ...any) error {
				_, err := execIn(t, ad, sess, sql, args...)
				return err
			}
			plat := func(tenantID any, metric string, value int64) error {
				return ins(platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES ($1::uuid, $2, $3)`, tenantID, metric, value)
			}
			if got := sqlstate(plat(nil, "requests_per_second", 5)); got != checkViolation {
				t.Errorf("an unknown metric: SQLSTATE %q", got)
			}
			for _, v := range []int64{0, -1, 1_000_000_000_001} {
				if got := sqlstate(plat(nil, "concurrent_requests", v)); got != checkViolation {
					t.Errorf("value %d: SQLSTATE %q", v, got)
				}
			}
			for _, v := range []int64{1, 1_000_000_000_000} {
				if err := plat(b.String(), "tokens_per_minute", v); err != nil {
					t.Errorf("value %d: %v", v, err)
				}
				if _, err := execIn(t, ad, platform, `DELETE FROM pandokos.platform_limit_rule WHERE tenant_id = $1::uuid AND metric = 'tokens_per_minute'`, b.String()); err != nil {
					t.Fatal(err)
				}
			}
			if got := sqlstate(plat(nil, "requests_per_minute", 5)); got != uniqueViolation {
				t.Errorf("a second default of a metric: SQLSTATE %q (nulls are not distinct)", got)
			}
			if got := sqlstate(plat(a.String(), "concurrent_requests", 5)); got != uniqueViolation {
				t.Errorf("a second row of a tenant and metric: SQLSTATE %q", got)
			}
			if got := sqlstate(plat("00000000-0000-4000-8000-000000000000", "tokens_per_minute", 5)); got != foreignKey {
				t.Errorf("a tenant that does not exist: SQLSTATE %q", got)
			}

			rule := func(kind, subject, metric string, value int64) error {
				return ins(sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value) VALUES ($1::uuid, $2, $3, $4, $5)`,
					a.String(), kind, subject, metric, value)
			}
			if got := sqlstate(rule("tenant", "", "requests_per_minute", 5)); got != uniqueViolation {
				t.Errorf("a second tenant rule of a metric: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("group", "x", "requests_per_minute", 5)); got != checkViolation {
				t.Errorf("an unknown subject kind: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("tenant", "x", "tokens_per_minute", 5)); got != checkViolation {
				t.Errorf("a tenant subject with an ID: SQLSTATE %q", got)
			}
			for _, kind := range []string{"service_account", "key"} {
				if got := sqlstate(rule(kind, "", "tokens_per_minute", 5)); got != checkViolation {
					t.Errorf("%s without an ID: SQLSTATE %q", kind, got)
				}
				if got := sqlstate(rule(kind, "UPPER", "tokens_per_minute", 5)); got != checkViolation {
					t.Errorf("%s with an upper-case ID: SQLSTATE %q", kind, got)
				}
				if got := sqlstate(rule(kind, "a b", "tokens_per_minute", 5)); got != checkViolation {
					t.Errorf("%s with a space in the ID: SQLSTATE %q", kind, got)
				}
				if got := sqlstate(rule(kind, "0123456789012345678901234567890123456789012345678901234567890123x", "tokens_per_minute", 5)); got != checkViolation {
					t.Errorf("%s with a 65 character ID: SQLSTATE %q", kind, got)
				}
				if err := rule(kind, "subject-1", "tokens_per_minute", 5); err != nil {
					t.Errorf("%s: %v", kind, err)
				}
			}
			if got := sqlstate(rule("key", "k1", "requests_per_hour", 5)); got != checkViolation {
				t.Errorf("an unknown metric: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("key", "k1", "requests_per_minute", 0)); got != checkViolation {
				t.Errorf("value 0: SQLSTATE %q", got)
			}
			// A tenant cannot write a row of another tenant.
			if err := ins(sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, metric, limit_value) VALUES ($1::uuid, 'tenant', '', 'tokens_per_minute', 5)`, b.String()); sqlstate(err) != insufficientPrivilege {
				t.Errorf("a rule for another tenant: %v", err)
			}
		})

		t.Run("only limit_value can be updated", func(t *testing.T) {
			for what, sql := range map[string]string{
				"limit_rule.metric":                  `UPDATE pandokos.limit_rule SET metric = 'tokens_per_minute'`,
				"limit_rule.subject_id":              `UPDATE pandokos.limit_rule SET subject_id = 'x'`,
				"limit_rule.tenant_id":               `UPDATE pandokos.limit_rule SET tenant_id = '` + b.String() + `'`,
				"limit_rule.updated_at":              `UPDATE pandokos.limit_rule SET updated_at = now()`,
				"platform_limit_rule.metric":         `UPDATE pandokos.platform_limit_rule SET metric = 'tokens_per_minute'`,
				"platform_limit_rule.tenant_id":      `UPDATE pandokos.platform_limit_rule SET tenant_id = NULL`,
				"platform_limit_rule.limit_value+id": `UPDATE pandokos.platform_limit_rule SET limit_value = 1, id = gen_random_uuid()`,
			} {
				sess := sessA
				if what[:8] == "platform" {
					sess = platform
				}
				if _, err := execIn(t, ad, sess, sql); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s: %v, want SQLSTATE %s", what, err, insufficientPrivilege)
				}
			}
			if n, err := execIn(t, ad, sessA, `UPDATE pandokos.limit_rule SET limit_value = 12 WHERE subject_kind = 'tenant'`); err != nil || n != 1 {
				t.Errorf("limit_value of limit_rule: %d %v", n, err)
			}
			if n, err := execIn(t, ad, platform, `UPDATE pandokos.platform_limit_rule SET limit_value = 1234 WHERE tenant_id IS NULL AND metric = 'tokens_per_minute'`); err != nil || n != 1 {
				t.Errorf("limit_value of platform_limit_rule: %d %v", n, err)
			}
		})
	})
}

// TestLimitEventPolicies is T-661: limit_event for each role and session. The gateway inserts the
// rows of its own tenant and reads nothing; the admin role reads the rows of its own tenant; the
// worker has nothing; nobody updates, deletes or truncates, the owner included; every check
// constraint and the unique key.
func TestLimitEventPolicies(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		super := d.Super(t)
		a, b := d.SeedTenant(t, "events-a"), d.SeedTenant(t, "events-b")
		sessA := sessions(a)[0]
		sessB := session{name: "tenant-b", setting: "app.tenant_id", value: b.String()}
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		none, empty := session{name: "none"}, session{name: "empty", setting: "app.tenant_id", value: ""}

		const cols = `tenant_id, kind, rule_id, rule_level, subject_kind, subject_id, provider, model, metric, currency, period_start, period_end, percent, limit_value, observed`
		const vals = `$1::uuid, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11::timestamptz, $12::timestamptz, $13, $14, $15`
		insert := `INSERT INTO pandokos.limit_event (` + cols + `) VALUES (` + vals + `)`
		rule := "00000000-0000-4000-8000-0000000000a1"
		args := func(tenantID tenant.ID, o map[string]any) []any {
			base := map[string]any{"kind": "threshold_reached", "rule": rule, "level": "tenant", "subject_kind": "key", "subject_id": "k1", "provider": "openai", "model": "gpt-4",
				"metric": "tokens_per_day", "currency": "", "start": "2026-11-17T00:00:00Z", "end": "2026-11-18T00:00:00Z", "percent": 80, "value": 100, "observed": 81.5}
			for k, v := range o {
				base[k] = v
			}
			return []any{tenantID.String(), base["kind"], base["rule"], base["level"], base["subject_kind"], base["subject_id"], base["provider"], base["model"], base["metric"],
				base["currency"], base["start"], base["end"], base["percent"], base["value"], base["observed"]}
		}
		for _, id := range []tenant.ID{a, b} {
			if _, err := super.Exec(ctx(t), insert, args(id, nil)...); err != nil {
				t.Fatal(err)
			}
		}
		gw, ad, wk := d.Connect(t, "gateway"), d.Connect(t, "admin"), d.Connect(t, "worker")

		t.Run("the gateway inserts the rows of its own tenant", func(t *testing.T) {
			if n, err := execIn(t, gw, sessA, insert, args(a, map[string]any{"percent": 90})...); err != nil || n != 1 {
				t.Errorf("own tenant: %d %v", n, err)
			}
			for _, c := range []struct {
				sess   session
				tenant tenant.ID
			}{{sessA, b}, {sessB, a}, {none, a}, {empty, a}, {platform, a}} {
				if _, err := execIn(t, gw, c.sess, insert, args(c.tenant, map[string]any{"percent": 91})...); sqlstate(err) != insufficientPrivilege {
					t.Errorf("session %s, a row of another tenant or no tenant: %v, want SQLSTATE %s", c.sess.name, err, insufficientPrivilege)
				}
			}
			// Recording is idempotent: the same event twice inserts one row without an error.
			dup := args(a, map[string]any{"percent": 95})
			if n, err := execIn(t, gw, sessA, insert+` ON CONFLICT DO NOTHING`, dup...); err != nil || n != 1 {
				t.Errorf("first insert: %d %v", n, err)
			}
			if n, err := execIn(t, gw, sessA, insert+` ON CONFLICT DO NOTHING`, dup...); err != nil || n != 0 {
				t.Errorf("the same event again: %d %v, want no row and no error", n, err)
			}
			if _, err := execIn(t, gw, sessA, insert, dup...); sqlstate(err) != uniqueViolation {
				t.Errorf("a plain insert of the same event: %v, want SQLSTATE %s", err, uniqueViolation)
			}
		})

		t.Run("the gateway reads nothing", func(t *testing.T) {
			for _, sess := range []session{sessA, sessB, platform, none, empty} {
				if _, err := query(t, gw, sess, `SELECT count(*) FROM pandokos.limit_event`); sqlstate(err) != insufficientPrivilege {
					t.Errorf("session %s: %v, want SQLSTATE %s", sess.name, err, insufficientPrivilege)
				}
			}
		})

		t.Run("the admin role reads the rows of its own tenant", func(t *testing.T) {
			for _, c := range []struct {
				sess session
				want int
			}{{sessA, 3}, {sessB, 1}, {none, 0}, {empty, 0}, {platform, 0}} {
				rows, err := query(t, ad, c.sess, `SELECT tenant_id::text AS tenant_id FROM pandokos.limit_event`)
				if err != nil {
					t.Errorf("session %s: %v", c.sess.name, err)
					continue
				}
				if len(rows) != c.want {
					t.Errorf("session %s sees %d rows, want %d", c.sess.name, len(rows), c.want)
				}
				for _, r := range rows {
					if (c.sess.name == "tenant-b" && r["tenant_id"] != b.String()) || (c.sess.name == "tenant-a" && r["tenant_id"] != a.String()) {
						t.Errorf("session %s sees a row of tenant %v", c.sess.name, r["tenant_id"])
					}
				}
			}
			if _, err := execIn(t, ad, sessA, insert, args(a, map[string]any{"percent": 70})...); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role inserted: %v", err)
			}
		})

		t.Run("the worker has nothing", func(t *testing.T) {
			for _, sess := range []session{sessA, platform, none} {
				if _, err := query(t, wk, sess, `SELECT count(*) FROM pandokos.limit_event`); sqlstate(err) != insufficientPrivilege {
					t.Errorf("session %s SELECT: %v", sess.name, err)
				}
				if _, err := execIn(t, wk, sess, insert, args(a, map[string]any{"percent": 60})...); sqlstate(err) != insufficientPrivilege {
					t.Errorf("session %s INSERT: %v", sess.name, err)
				}
			}
		})

		t.Run("nobody updates, deletes or truncates, the owner included", func(t *testing.T) {
			before := scalar(t, super, `SELECT count(*)::text FROM pandokos.limit_event`)
			for what, sql := range map[string]string{
				"UPDATE":   `UPDATE pandokos.limit_event SET observed = 0`,
				"DELETE":   `DELETE FROM pandokos.limit_event`,
				"TRUNCATE": `TRUNCATE pandokos.limit_event`,
			} {
				for role, conn := range map[string]*pgx.Conn{"gateway": gw, "admin": ad, "worker": wk} {
					if _, err := execIn(t, conn, sessA, sql); sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s as %s: %v, want SQLSTATE %s", what, role, err, insufficientPrivilege)
					}
				}
				if _, err := execIn(t, d.Connect(t, "owner"), sessA, sql); err == nil {
					t.Errorf("%s as the owner succeeded", what)
				}
				if _, err := super.Exec(ctx(t), sql); err == nil {
					t.Errorf("%s as the superuser succeeded: the table is append-only", what)
				}
			}
			if after := scalar(t, super, `SELECT count(*)::text FROM pandokos.limit_event`); after != before {
				t.Errorf("%s rows after the refused changes, %s before", after, before)
			}
		})

		t.Run("every check constraint and the unique key", func(t *testing.T) {
			bad := func(what string, code string, o map[string]any) {
				t.Helper()
				if _, err := execIn(t, gw, sessA, insert, args(a, o)...); sqlstate(err) != code {
					t.Errorf("%s: %v, want SQLSTATE %s", what, err, code)
				}
			}
			bad("an unknown kind", checkViolation, map[string]any{"kind": "alarm", "percent": 41})
			bad("a threshold without a percentage", checkViolation, map[string]any{"percent": 0})
			bad("a percentage above 100", checkViolation, map[string]any{"percent": 101})
			bad("an exhausted event with a percentage", checkViolation, map[string]any{"kind": "exhausted", "percent": 50})
			bad("an unknown rule level", checkViolation, map[string]any{"level": "global", "percent": 42})
			bad("an unknown subject kind", checkViolation, map[string]any{"subject_kind": "team", "percent": 43})
			bad("a tenant subject with an ID", checkViolation, map[string]any{"subject_kind": "tenant", "percent": 44})
			bad("a key subject without an ID", checkViolation, map[string]any{"subject_id": "", "percent": 45})
			bad("an ID with a space", checkViolation, map[string]any{"subject_id": "a b", "percent": 46})
			bad("an upper-case provider", checkViolation, map[string]any{"provider": "OpenAI", "percent": 47})
			bad("a model without a provider", checkViolation, map[string]any{"provider": "", "percent": 48})
			bad("a rate metric", checkViolation, map[string]any{"metric": "requests_per_minute", "percent": 49})
			bad("a currency on a count metric", checkViolation, map[string]any{"currency": "EUR", "percent": 50})
			bad("a budget without a currency", checkViolation, map[string]any{"metric": "cost_per_day", "percent": 51})
			bad("a lower-case currency", checkViolation, map[string]any{"metric": "cost_per_day", "currency": "eur", "percent": 52})
			bad("an empty period", checkViolation, map[string]any{"end": "2026-11-17T00:00:00Z", "percent": 53})
			bad("a value of 0", checkViolation, map[string]any{"value": 0, "percent": 54})
			bad("an observed value below 0", checkViolation, map[string]any{"observed": -1, "percent": 55})
			bad("the same rule, period, kind and percentage", uniqueViolation, nil)
			// And the valid forms: a budget, an exhausted event, a tenant subject.
			for what, o := range map[string]map[string]any{
				"a budget":          {"metric": "cost_per_month", "currency": "EUR", "percent": 10, "value": 250500000},
				"an exhausted":      {"kind": "exhausted", "percent": 0},
				"a tenant subject":  {"subject_kind": "tenant", "subject_id": "", "provider": "", "model": "", "percent": 20},
				"a platform level":  {"level": "platform", "percent": 30},
				"a service account": {"subject_kind": "service_account", "subject_id": "0192f0f0-0000-7000-8000-000000000001", "percent": 40},
			} {
				if n, err := execIn(t, gw, sessA, insert, args(a, o)...); err != nil || n != 1 {
					t.Errorf("%s: %d %v", what, n, err)
				}
			}
		})
	})
}

// TestLimitAPI is T-679: for every route of the limit operations with a tenant in the path, a key of
// tenant B gets the same 404 for tenant A as for a tenant that does not exist, and a rule ID or an
// event of A is not found under B. The routes are those of the generated route table that name a
// limit, so that a route added later without a test fails this one.
func TestLimitAPI(t *testing.T) {
	w := newAPIWorld(t)
	var ops []adminapi.Operation
	for _, op := range adminapi.Operations {
		if strings.Contains(op.Path, "limit") && strings.Contains(op.Path, "{tenant}") {
			ops = append(ops, op)
		}
	}
	if len(ops) != 7 {
		t.Fatalf("%d tenant routes name a limit, want 7 (the route table changed: give the new routes their cases)", len(ops))
	}
	// An event of A.
	if _, err := w.api.Super(t).Exec(ctx(t), `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
		VALUES ($1::uuid, 'threshold_reached', $2::uuid, 'tenant', 'tenant', 'requests_per_day', now(), now() + interval '1 day', 80, 100, 81)`, w.a.tenant.ID, w.a.limitRule); err != nil {
		t.Fatal(err)
	}
	before := w.api.SnapshotTenant(t, w.a.tenant.ID)
	for _, op := range ops {
		onA, missing := w.b.params(), w.b.params()
		onA.Tenant, onA.LimitRule = w.a.tenant.ID, w.a.limitRule
		missing.Tenant, missing.LimitRule = randomUUID(t, w), randomUUID(t, w)
		ca, cm := apitest.CallFor(t, op, onA), apitest.CallFor(t, op, missing)
		r1 := w.api.Do(t, ca.Method, ca.Path, w.b.adminKey, ca.Body, nil)
		r2 := w.api.Do(t, cm.Method, cm.Path, w.b.adminKey, cm.Body, nil)
		if r1.Status != 404 || r2.Status != 404 || !strings.Contains(string(r1.Body), `"code":"not_found"`) {
			t.Errorf("%s: tenant A gives %d, a missing tenant %d, want 404 not_found\n%s", op.ID, r1.Status, r2.Status, r1.Body)
		}
		if normalize(t, r1) != normalize(t, r2) {
			t.Errorf("%s: tenant A and a missing tenant differ:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
		}
		if after := w.api.SnapshotTenant(t, w.a.tenant.ID); after != before {
			t.Fatalf("%s changed a row of tenant A", op.ID)
		}

		// A rule of A under B's own tenant path is not found, as a rule that does not exist is not.
		if strings.Contains(op.Path, "{rule}") {
			ownPath, missingRule := w.b.params(), w.b.params()
			ownPath.LimitRule, missingRule.LimitRule = w.a.limitRule, randomUUID(t, w)
			c1, c2 := apitest.CallFor(t, op, ownPath), apitest.CallFor(t, op, missingRule)
			r1 := w.api.Do(t, c1.Method, c1.Path, w.b.adminKey, c1.Body, nil)
			r2 := w.api.Do(t, c2.Method, c2.Path, w.b.adminKey, c2.Body, nil)
			if r1.Status != 404 || r2.Status != 404 || normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: A's rule under B gives %d, a missing rule %d (the bodies must be the same)\n%s\n%s", op.ID, r1.Status, r2.Status, r1.Body, r2.Body)
			}
			if after := w.api.SnapshotTenant(t, w.a.tenant.ID); after != before {
				t.Fatalf("%s under B changed a row of tenant A", op.ID)
			}
		}
	}

	t.Run("the events and the rules of A are not in the lists of B", func(t *testing.T) {
		var ev adminapi.LimitEventList
		r := w.api.Do(t, "GET", "/v1/tenants/"+w.b.tenant.ID+"/limit-events?limit=100", w.b.adminKey, nil, nil)
		if r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		if err := json.Unmarshal(r.Body, &ev); err != nil || len(ev.Items) != 0 {
			t.Errorf("B lists %d events: %v %s", len(ev.Items), err, r.Body)
		}
		var rules adminapi.LimitRuleList
		r = w.api.Do(t, "GET", "/v1/tenants/"+w.b.tenant.ID+"/limits?limit=100", w.b.adminKey, nil, nil)
		if err := json.Unmarshal(r.Body, &rules); err != nil {
			t.Fatal(err)
		}
		for _, it := range rules.Items {
			if it.ID == w.a.limitRule {
				t.Errorf("B lists the rule of A")
			}
		}
		var pl adminapi.PlatformLimitRuleList
		r = w.api.Do(t, "GET", "/v1/tenants/"+w.b.tenant.ID+"/platform-limits?limit=100", w.b.adminKey, nil, nil)
		if err := json.Unmarshal(r.Body, &pl); err != nil {
			t.Fatal(err)
		}
		for _, it := range pl.Items {
			if it.ID == w.a.platformLimit || it.Tenant == w.a.tenant.ID {
				t.Errorf("B sees the platform rule of A: %+v", it)
			}
		}
	})

	t.Run("the platform routes are refused to a tenant key", func(t *testing.T) {
		for _, op := range adminapi.Operations {
			if !strings.HasPrefix(op.Path, "/v1/platform-limits") {
				continue
			}
			c := apitest.CallFor(t, op, w.a.params())
			r := w.api.Do(t, c.Method, c.Path, w.a.adminKey, c.Body, nil)
			if r.Status != 403 {
				t.Errorf("%s: status %d, want 403\n%s", op.ID, r.Status, r.Body)
			}
		}
	})
}

// TestLimitRuleChecks is T-626 extended (sprint 5.2): the scope, the currency, the enforcement and the
// thresholds of both rule tables, and the unique keys that include the scope.
func TestLimitRuleChecks(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a := d.SeedTenant(t, "checks-a")
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		ad := d.Connect(t, "admin")

		type row struct {
			tenant     any
			provider   string
			model      string
			metric     string
			value      int64
			currency   string
			enforce    bool
			thresholds []int16
		}
		def := func(o func(*row)) row {
			r := row{provider: "", model: "", metric: "tokens_per_day", value: 10, currency: "", enforce: true, thresholds: []int16{}}
			if o != nil {
				o(&r)
			}
			return r
		}
		tenantRule := func(r row) error {
			_, err := execIn(t, ad, sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, subject_id, provider, model, metric, limit_value, currency, enforce, thresholds)
				VALUES ($1::uuid, 'tenant', '', $2, $3, $4, $5, $6, $7, $8::smallint[])`, a.String(), r.provider, r.model, r.metric, r.value, r.currency, r.enforce, r.thresholds)
			return err
		}
		platformRule := func(r row) error {
			_, err := execIn(t, ad, platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, provider, model, metric, limit_value, currency, enforce, thresholds)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::smallint[])`, r.tenant, r.provider, r.model, r.metric, r.value, r.currency, r.enforce, r.thresholds)
			return err
		}
		for table, insert := range map[string]func(row) error{"limit_rule": tenantRule, "platform_limit_rule": platformRule} {
			t.Run(table, func(t *testing.T) {
				bad := func(what string, o func(*row)) {
					t.Helper()
					if got := sqlstate(insert(def(o))); got != checkViolation {
						t.Errorf("%s: SQLSTATE %q, want %s", what, got, checkViolation)
					}
				}
				good := func(what string, o func(*row)) {
					t.Helper()
					if err := insert(def(o)); err != nil {
						t.Errorf("%s: %v", what, err)
					}
				}
				bad("an upper-case provider", func(r *row) { r.provider = "OpenAI" })
				bad("a provider that starts with a digit", func(r *row) { r.provider = "1openai" })
				bad("a model without a provider", func(r *row) { r.model = "gpt-4" })
				bad("a model with a space", func(r *row) { r.provider, r.model = "openai", "gpt 4" })
				bad("a currency on a count metric", func(r *row) { r.currency = "EUR" })
				bad("a budget without a currency", func(r *row) { r.metric = "cost_per_day" })
				bad("a lower-case currency", func(r *row) { r.metric, r.currency = "cost_per_day", "eur" })
				bad("a four-letter currency", func(r *row) { r.metric, r.currency = "cost_per_month", "EURO" })
				bad("a count above 10^12", func(r *row) { r.value = 1_000_000_000_001 })
				bad("a budget above 10^15 millionths", func(r *row) { r.metric, r.currency, r.value = "cost_per_day", "EUR", 1_000_000_000_000_001 })
				bad("five thresholds", func(r *row) { r.thresholds = []int16{10, 20, 30, 40, 50} })
				bad("a threshold of 0", func(r *row) { r.thresholds = []int16{0} })
				bad("a threshold of 101", func(r *row) { r.thresholds = []int16{101} })
				bad("a soft rule without thresholds", func(r *row) { r.enforce = false })
				bad("a soft rate metric", func(r *row) { r.metric, r.enforce, r.thresholds = "requests_per_minute", false, []int16{50} })
				bad("thresholds on a rate metric", func(r *row) { r.metric, r.thresholds = "concurrent_requests", []int16{50} })
				bad("an unknown metric", func(r *row) { r.metric = "tokens_per_hour" })
				good("a budget at the largest amount", func(r *row) { r.metric, r.currency, r.value = "cost_per_day", "EUR", 1_000_000_000_000_000 })
				good("a count at the largest value", func(r *row) { r.metric, r.value = "requests_per_month", 1_000_000_000_000 })
				good("a soft rule with four thresholds", func(r *row) { r.metric, r.enforce, r.thresholds = "requests_per_day", false, []int16{1, 50, 99, 100} })
				good("a scope of provider and model", func(r *row) { r.provider, r.model = "openai", "gpt-4o" })
				good("a scope of a provider", func(r *row) { r.provider, r.metric = "anthropic", "tokens_per_month" })
				// The unique key includes the scope: the same metric in the same scope is refused, in another is not.
				if got := sqlstate(insert(def(func(r *row) { r.provider, r.model = "openai", "gpt-4o" }))); got != uniqueViolation {
					t.Errorf("the same rule in the same scope: SQLSTATE %q, want %s", got, uniqueViolation)
				}
				if got := sqlstate(insert(def(func(r *row) { r.provider = "anthropic" }))); got != "" {
					t.Errorf("the same metric in another scope: SQLSTATE %q", got)
				}
			})
		}
	})
}
