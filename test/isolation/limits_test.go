package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
			if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value) VALUES ($1::uuid, 'tenant', 'requests_per_minute', 10)`, id); err != nil {
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
						"INSERT limit_rule":          `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value) VALUES ('` + a.String() + `', 'tenant', 'tokens_per_minute', 5)`,
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

			// A rule names its subject by identifier, in typed columns with foreign keys (migration 0011);
			// T-694 covers every case of the constraints, this test the grants and the checks of 5.1.
			sa := d.SeedServiceAccount(t, a, "checks-sa")
			_, vk := d.SeedVirtualKey(t, a, sa)
			rule := func(kind string, saID, vkID any, metric string, value int64) error {
				return ins(sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, service_account_id, virtual_key_id, metric, limit_value)
					VALUES ($1::uuid, $2, $3::uuid, $4::uuid, $5, $6)`, a.String(), kind, saID, vkID, metric, value)
			}
			if got := sqlstate(rule("tenant", nil, nil, "requests_per_minute", 5)); got != uniqueViolation {
				t.Errorf("a second tenant rule of a metric: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("group", sa, nil, "requests_per_minute", 5)); got != checkViolation {
				t.Errorf("an unknown subject kind: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("tenant", sa, nil, "tokens_per_minute", 5)); got != checkViolation {
				t.Errorf("a tenant subject with a subject: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("service_account", nil, nil, "tokens_per_minute", 5)); got != checkViolation {
				t.Errorf("service_account without a subject: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("key", nil, nil, "tokens_per_minute", 5)); got != checkViolation {
				t.Errorf("key without a subject: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("service_account", "0192f0f0-0000-7000-8000-000000000001", nil, "tokens_per_minute", 5)); got != foreignKey {
				t.Errorf("a service account that does not exist: SQLSTATE %q", got)
			}
			if err := rule("service_account", sa, nil, "tokens_per_minute", 5); err != nil {
				t.Errorf("service_account: %v", err)
			}
			if err := rule("key", nil, vk, "tokens_per_minute", 5); err != nil {
				t.Errorf("key: %v", err)
			}
			if got := sqlstate(rule("key", nil, vk, "requests_per_hour", 5)); got != checkViolation {
				t.Errorf("an unknown metric: SQLSTATE %q", got)
			}
			if got := sqlstate(rule("key", nil, vk, "requests_per_minute", 0)); got != checkViolation {
				t.Errorf("value 0: SQLSTATE %q", got)
			}
			// A tenant cannot write a row of another tenant.
			if err := ins(sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value) VALUES ($1::uuid, 'tenant', 'tokens_per_minute', 5)`, b.String()); sqlstate(err) != insufficientPrivilege {
				t.Errorf("a rule for another tenant: %v", err)
			}
		})

		t.Run("only limit_value can be updated", func(t *testing.T) {
			for what, sql := range map[string]string{
				"limit_rule.metric":                  `UPDATE pandokos.limit_rule SET metric = 'tokens_per_minute'`,
				"limit_rule.service_account_id":      `UPDATE pandokos.limit_rule SET service_account_id = gen_random_uuid()`,
				"limit_rule.virtual_key_id":          `UPDATE pandokos.limit_rule SET virtual_key_id = NULL`,
				"limit_rule.subject_mapping_id":      `UPDATE pandokos.limit_rule SET subject_mapping_id = NULL`,
				"limit_rule.provider_id":             `UPDATE pandokos.limit_rule SET provider_id = NULL`,
				"limit_rule.model_id":                `UPDATE pandokos.limit_rule SET model_id = NULL`,
				"platform_limit_rule.provider_id":    `UPDATE pandokos.platform_limit_rule SET provider_id = NULL`,
				"platform_limit_rule.model_id":       `UPDATE pandokos.platform_limit_rule SET model_id = NULL`,
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
// thresholds of both rule tables, and the unique keys that include the scope. Since sprint 5.3 the
// scope is a provider ID and a model ID with foreign keys: the cases of the format of a name moved
// to T-694 (TestLimitRuleIntegrity), where the names are not in the table any more.
func TestLimitRuleChecks(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a := d.SeedTenant(t, "checks-a")
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		ad := d.Connect(t, "admin")
		openai, anthropic, gpt4o := d.ProviderID(t, "openai"), d.ProviderID(t, "anthropic"), d.ModelID(t, "openai", "gpt-4o")

		type row struct {
			tenant     any
			providerID any // nil: every provider
			modelID    any
			metric     string
			value      int64
			currency   string
			enforce    bool
			thresholds []int16
		}
		def := func(o func(*row)) row {
			r := row{metric: "tokens_per_day", value: 10, currency: "", enforce: true, thresholds: []int16{}}
			if o != nil {
				o(&r)
			}
			return r
		}
		tenantRule := func(r row) error {
			_, err := execIn(t, ad, sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, provider_id, model_id, metric, limit_value, currency, enforce, thresholds)
				VALUES ($1::uuid, 'tenant', $2::uuid, $3::uuid, $4, $5, $6, $7, $8::smallint[])`, a.String(), r.providerID, r.modelID, r.metric, r.value, r.currency, r.enforce, r.thresholds)
			return err
		}
		platformRule := func(r row) error {
			_, err := execIn(t, ad, platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, provider_id, model_id, metric, limit_value, currency, enforce, thresholds)
				VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::smallint[])`, r.tenant, r.providerID, r.modelID, r.metric, r.value, r.currency, r.enforce, r.thresholds)
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
				bad("a model without a provider", func(r *row) { r.modelID = gpt4o })
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
				good("a scope of provider and model", func(r *row) { r.providerID, r.modelID = openai, gpt4o })
				good("a scope of a provider", func(r *row) { r.providerID, r.metric = anthropic, "tokens_per_month" })
				// The unique key includes the scope: the same metric in the same scope is refused, in another is not.
				if got := sqlstate(insert(def(func(r *row) { r.providerID, r.modelID = openai, gpt4o }))); got != uniqueViolation {
					t.Errorf("the same rule in the same scope: SQLSTATE %q, want %s", got, uniqueViolation)
				}
				if got := sqlstate(insert(def(func(r *row) { r.providerID = anthropic }))); got != "" {
					t.Errorf("the same metric in another scope: SQLSTATE %q", got)
				}
			})
		}
	})
}

// TestRuleForeignKeys is T-693, the schema test that replaces a job that looks for orphans: every
// uuid column of the two rule tables other than id is the referencing column of a foreign key, the
// subject and scope keys are ON DELETE RESTRICT, and the subject keys are composite with tenant_id.
func TestRuleForeignKeys(t *testing.T) {
	d := dbtest.NewMigrated(t)
	super := d.Super(t)

	type fk struct {
		name, refTable, onDelete string
		cols, refCols            []string
	}
	load := func(tbl string) (uuidCols []string, fks []fk) {
		t.Helper()
		rows, err := super.Query(ctx(t), `SELECT a.attname FROM pg_catalog.pg_attribute a
			WHERE a.attrelid = ('pandokos.' || $1)::regclass AND a.attnum > 0 AND NOT a.attisdropped AND a.atttypid = 'uuid'::regtype
			ORDER BY a.attname`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		if uuidCols, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			t.Fatal(err)
		}
		rows, err = super.Query(ctx(t), `SELECT c.conname, c.confrelid::regclass::text, c.confdeltype::text,
				ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(n, o) JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.n ORDER BY k.o),
				ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(n, o) JOIN pg_catalog.pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.n ORDER BY k.o)
			FROM pg_catalog.pg_constraint c WHERE c.conrelid = ('pandokos.' || $1)::regclass AND c.contype = 'f' ORDER BY c.conname`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var f fk
			if err := rows.Scan(&f.name, &f.refTable, &f.onDelete, &f.cols, &f.refCols); err != nil {
				t.Fatal(err)
			}
			fks = append(fks, f)
		}
		rows.Close()
		return uuidCols, fks
	}

	for _, tbl := range []string{"limit_rule", "platform_limit_rule"} {
		cols, fks := load(tbl)
		referencing := map[string]bool{}
		for _, f := range fks {
			for _, c := range f.cols {
				referencing[c] = true
			}
		}
		for _, c := range cols {
			if c != "id" && !referencing[c] {
				t.Errorf("%s.%s is a uuid column that no foreign key references: a reference nobody keeps becomes an orphan", tbl, c)
			}
		}
		find := func(name string) (fk, bool) {
			for _, f := range fks {
				if f.name == name {
					return f, true
				}
			}
			return fk{}, false
		}
		type want struct {
			name, refTable string
			cols, refCols  []string
		}
		wants := []want{
			{tbl + "_provider_fkey", "pandokos.provider", []string{"provider_id"}, []string{"id"}},
			{tbl + "_model_fkey", "pandokos.model", []string{"provider_id", "model_id"}, []string{"provider_id", "id"}},
		}
		if tbl == "limit_rule" {
			wants = append(wants,
				want{"limit_rule_service_account_fkey", "pandokos.service_account", []string{"tenant_id", "service_account_id"}, []string{"tenant_id", "id"}},
				want{"limit_rule_virtual_key_fkey", "pandokos.virtual_key", []string{"tenant_id", "virtual_key_id"}, []string{"tenant_id", "id"}},
				want{"limit_rule_subject_mapping_fkey", "pandokos.subject_mapping", []string{"tenant_id", "subject_mapping_id"}, []string{"tenant_id", "id"}})
		}
		for _, w := range wants {
			f, ok := find(w.name)
			switch {
			case !ok:
				t.Errorf("%s has no foreign key %s", tbl, w.name)
			case f.refTable != w.refTable || !reflect.DeepEqual(f.cols, w.cols) || !reflect.DeepEqual(f.refCols, w.refCols):
				t.Errorf("%s: %s -> %s %v, want %s %v -> %v", tbl, w.name, f.refTable, f.cols, w.refTable, w.cols, w.refCols)
			case f.onDelete != "r":
				t.Errorf("%s: %s is ON DELETE %q, want RESTRICT (r): no cascade deletes a rule without its audit row", tbl, w.name, f.onDelete)
			}
		}
	}
}

// TestLimitRuleIntegrity is T-694: what the constraints, the foreign keys and the two key guards
// of migration 0011 refuse, as each role that can write or read a rule.
func TestLimitRuleIntegrity(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "integrity-a"), d.SeedTenant(t, "integrity-b")
		super := d.Super(t)
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}
		ad := d.Connect(t, "admin")

		saA, saB := d.SeedServiceAccount(t, a, "sa-a"), d.SeedServiceAccount(t, b, "sa-b")
		_, vkA := d.SeedVirtualKey(t, a, saA)
		_, vkB := d.SeedVirtualKey(t, b, saB)
		_, vkRevoked := d.SeedVirtualKey(t, a, saA, dbtest.KeyRevoked())
		issuerA := d.SeedTrustedIssuer(t, a, "issuer-a")
		smA := d.SeedSubjectMapping(t, a, issuerA, false, saA, "subject-a")
		d.SeedModel(t, "openai", "integrity-model")
		openai, anthropic := d.ProviderID(t, "openai"), d.ProviderID(t, "anthropic")
		modelOpenAI := d.ModelID(t, "openai", "integrity-model")

		// rule inserts a rule of tenant A through the admin role in A's session.
		rule := func(kind string, sa, vk, sm, prov, mod any, metric string) error {
			_, err := execIn(t, ad, sessA, `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, service_account_id, virtual_key_id, subject_mapping_id,
					provider_id, model_id, metric, limit_value)
				VALUES ($1::uuid, $2, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, 10)`, a.String(), kind, sa, vk, sm, prov, mod, metric)
			return err
		}
		expect := func(what, want string, err error) {
			t.Helper()
			if got := sqlstate(err); got != want {
				t.Errorf("%s: SQLSTATE %q (%v), want %q", what, got, err, want)
			}
		}

		t.Run("every case of subject_by_kind", func(_ *testing.T) {
			expect("a tenant rule with a service account", checkViolation, rule("tenant", saA, nil, nil, nil, nil, "requests_per_day"))
			expect("a tenant rule with a key", checkViolation, rule("tenant", nil, vkA, nil, nil, nil, "requests_per_day"))
			expect("a tenant rule with a mapping", checkViolation, rule("tenant", nil, nil, smA, nil, nil, "requests_per_day"))
			expect("a service account rule without a subject", checkViolation, rule("service_account", nil, nil, nil, nil, nil, "requests_per_day"))
			expect("a service account rule with a key too", checkViolation, rule("service_account", saA, vkA, nil, nil, nil, "requests_per_day"))
			expect("a service account rule with a mapping too", checkViolation, rule("service_account", saA, nil, smA, nil, nil, "requests_per_day"))
			expect("a key rule without a subject", checkViolation, rule("key", nil, nil, nil, nil, nil, "requests_per_day"))
			expect("a key rule with a service account", checkViolation, rule("key", saA, nil, nil, nil, nil, "requests_per_day"))
			expect("a key rule with a key and a mapping", checkViolation, rule("key", nil, vkA, smA, nil, nil, "requests_per_day"))
			expect("an unknown kind", checkViolation, rule("team", saA, nil, nil, nil, nil, "requests_per_day"))
			expect("a model without a provider", checkViolation, rule("tenant", nil, nil, nil, nil, modelOpenAI, "requests_per_day"))
			expect("a tenant rule", "", rule("tenant", nil, nil, nil, nil, nil, "requests_per_day"))
			expect("a service account rule", "", rule("service_account", saA, nil, nil, nil, nil, "requests_per_day"))
			expect("a key rule on a virtual key", "", rule("key", nil, vkA, nil, nil, nil, "requests_per_day"))
			expect("a key rule on a mapping", "", rule("key", nil, nil, smA, nil, nil, "requests_per_day"))
		})

		t.Run("the unique key counts nulls as equal", func(_ *testing.T) {
			expect("a second tenant rule of a metric", uniqueViolation, rule("tenant", nil, nil, nil, nil, nil, "requests_per_day"))
			expect("a second rule of a key", uniqueViolation, rule("key", nil, vkA, nil, nil, nil, "requests_per_day"))
			expect("a second rule of a mapping", uniqueViolation, rule("key", nil, nil, smA, nil, nil, "requests_per_day"))
			expect("another metric", "", rule("tenant", nil, nil, nil, nil, nil, "requests_per_month"))
			expect("a scope", "", rule("tenant", nil, nil, nil, openai, nil, "requests_per_day"))
			expect("a second rule of a scope", uniqueViolation, rule("tenant", nil, nil, nil, openai, nil, "requests_per_day"))
			expect("a model scope", "", rule("tenant", nil, nil, nil, openai, modelOpenAI, "requests_per_day"))
			expect("a second rule of a model scope", uniqueViolation, rule("tenant", nil, nil, nil, openai, modelOpenAI, "requests_per_day"))
		})

		t.Run("a subject of another tenant and a model of another provider are refused", func(t *testing.T) {
			expect("a service account of another tenant", foreignKey, rule("service_account", saB, nil, nil, nil, nil, "tokens_per_day"))
			expect("a key of another tenant", foreignKey, rule("key", nil, vkB, nil, nil, nil, "tokens_per_day"))
			expect("a subject that does not exist", foreignKey, rule("service_account", "0192f0f0-0000-7000-8000-000000000001", nil, nil, nil, nil, "tokens_per_day"))
			expect("a provider that does not exist", foreignKey, rule("tenant", nil, nil, nil, "0192f0f0-0000-7000-8000-000000000002", nil, "tokens_per_day"))
			expect("a model of another provider", foreignKey, rule("tenant", nil, nil, nil, anthropic, modelOpenAI, "tokens_per_day"))
			expect("a model that does not exist", foreignKey, rule("tenant", nil, nil, nil, openai, "0192f0f0-0000-7000-8000-000000000003", "tokens_per_day"))
			// The same for a ceiling of the platform.
			ceiling := func(prov, mod any) error {
				_, err := execIn(t, ad, platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, provider_id, model_id, metric, limit_value)
					VALUES (NULL, $1::uuid, $2::uuid, 'tokens_per_day', 10)`, prov, mod)
				return err
			}
			expect("a ceiling with a model of another provider", foreignKey, ceiling(anthropic, modelOpenAI))
			expect("a ceiling with a model and no provider", checkViolation, ceiling(nil, modelOpenAI))
			expect("a ceiling of a model", "", ceiling(openai, modelOpenAI))
			expect("a second ceiling of the model", uniqueViolation, ceiling(openai, modelOpenAI))
		})

		t.Run("a rule for a revoked key is refused", func(_ *testing.T) {
			expect("insert", checkViolation, rule("key", nil, vkRevoked, nil, nil, nil, "tokens_per_month"))
		})

		t.Run("revoking a key that has a rule fails at commit", func(t *testing.T) {
			saX := d.SeedServiceAccount(t, a, "sa-revoke")
			_, vkX := d.SeedVirtualKey(t, a, saX)
			if err := rule("key", nil, vkX, nil, nil, nil, "tokens_per_month"); err != nil {
				t.Fatal(err)
			}
			revoke := func(tx pgx.Tx, c context.Context) error {
				_, err := tx.Exec(c, `UPDATE pandokos.virtual_key SET revoked_at = now() WHERE id = $1::uuid`, vkX)
				return err
			}
			drop := func(tx pgx.Tx, c context.Context) error {
				_, err := tx.Exec(c, `DELETE FROM pandokos.limit_rule WHERE virtual_key_id = $1::uuid`, vkX)
				return err
			}
			run := func(steps ...func(pgx.Tx, context.Context) error) error {
				c := ctx(t)
				tx, err := ad.Begin(c)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(c, "SELECT pg_catalog.set_config('app.tenant_id', $1, true)", a.String()); err != nil {
					t.Fatal(err)
				}
				for _, s := range steps {
					if err := s(tx, c); err != nil {
						_ = tx.Rollback(c)
						return err
					}
				}
				return tx.Commit(c)
			}
			expect("a revocation that leaves the rule", restrictViolation, run(revoke))
			if got := scalar(t, super, `SELECT revoked_at::text FROM pandokos.virtual_key WHERE id = $1::uuid`, vkX); got != "<null>" {
				t.Errorf("the key is revoked (%s) although the commit failed", got)
			}
			expect("rule deleted, then key revoked", "", run(drop, revoke))
			if got := scalar(t, super, `SELECT (revoked_at IS NOT NULL)::text FROM pandokos.virtual_key WHERE id = $1::uuid`, vkX); got != "true" {
				t.Errorf("the key is not revoked: %s", got)
			}
			// The other order is allowed too: the check is deferred to the commit.
			saY := d.SeedServiceAccount(t, a, "sa-revoke-2")
			_, vkY := d.SeedVirtualKey(t, a, saY)
			if err := rule("key", nil, vkY, nil, nil, nil, "tokens_per_month"); err != nil {
				t.Fatal(err)
			}
			vkX = vkY
			expect("key revoked, then rule deleted", "", run(revoke, drop))
		})

		t.Run("a rule and a revocation in two sessions never both commit", func(t *testing.T) {
			// Without the advisory lock of the handlers (any role, any caller): the rule insert takes a
			// share lock of the key row, so the revocation waits for it, or the insert sees the revocation.
			begin := func(conn *pgx.Conn) pgx.Tx {
				c := ctx(t)
				tx, err := conn.Begin(c)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(c, "SELECT pg_catalog.set_config('app.tenant_id', $1, true)", a.String()); err != nil {
					t.Fatal(err)
				}
				return tx
			}
			insert := func(tx pgx.Tx, vk string) error {
				_, err := tx.Exec(ctx(t), `INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, virtual_key_id, metric, limit_value)
					VALUES ($1::uuid, 'key', $2::uuid, 'tokens_per_month', 10)`, a.String(), vk)
				return err
			}
			revoke := func(tx pgx.Tx, vk string) error {
				_, err := tx.Exec(ctx(t), `UPDATE pandokos.virtual_key SET revoked_at = now() WHERE id = $1::uuid`, vk)
				return err
			}
			// blocked reports whether fn is still running after a while (waiting for a lock).
			blocked := func(fn func() error) (wait func() error, isBlocked bool) {
				done := make(chan error, 1)
				go func() { done <- fn() }()
				select {
				case err := <-done:
					return func() error { return err }, false
				case <-time.After(500 * time.Millisecond):
					return func() error { return <-done }, true
				}
			}
			other := d.Connect(t, "admin")
			final := func(vk string) (revoked bool, rules string) {
				return scalar(t, super, `SELECT (revoked_at IS NOT NULL)::text FROM pandokos.virtual_key WHERE id = $1::uuid`, vk) == "true",
					scalar(t, super, `SELECT count(*)::text FROM pandokos.limit_rule WHERE virtual_key_id = $1::uuid`, vk)
			}

			t.Run("the rule first", func(t *testing.T) {
				sa := d.SeedServiceAccount(t, a, "sa-race-1")
				_, vk := d.SeedVirtualKey(t, a, sa)
				tx1, tx2 := begin(ad), begin(other)
				if err := insert(tx1, vk); err != nil {
					t.Fatal(err)
				}
				wait, isBlocked := blocked(func() error { return revoke(tx2, vk) })
				if !isBlocked {
					t.Error("the revocation did not wait for the transaction that inserts a rule of the key")
				}
				err1 := tx1.Commit(ctx(t))
				errRevoke := wait()
				var err2 error
				if errRevoke == nil {
					err2 = tx2.Commit(ctx(t))
				} else {
					_ = tx2.Rollback(ctx(t))
				}
				revoked, rules := final(vk)
				if err1 != nil || (errRevoke == nil && err2 == nil) || (revoked && rules != "0") {
					t.Errorf("rule commit %v, revoke %v, revoke commit %v: revoked %v with %s rules", err1, errRevoke, err2, revoked, rules)
				}
				if sqlstate(err2) != restrictViolation {
					t.Errorf("the revoking transaction: %v, want SQLSTATE %s at the commit", err2, restrictViolation)
				}
			})

			t.Run("the revocation first", func(t *testing.T) {
				sa := d.SeedServiceAccount(t, a, "sa-race-2")
				_, vk := d.SeedVirtualKey(t, a, sa)
				tx1, tx2 := begin(ad), begin(other)
				if err := revoke(tx2, vk); err != nil {
					t.Fatal(err)
				}
				wait, isBlocked := blocked(func() error { return insert(tx1, vk) })
				if !isBlocked {
					t.Error("the insert did not wait for the transaction that revokes the key")
				}
				if err := tx2.Commit(ctx(t)); err != nil {
					t.Fatal(err)
				}
				errInsert := wait()
				_ = tx1.Rollback(ctx(t))
				if sqlstate(errInsert) != checkViolation {
					t.Errorf("the insert after the revocation: %v, want SQLSTATE %s", errInsert, checkViolation)
				}
				if revoked, rules := final(vk); !revoked || rules != "0" {
					t.Errorf("revoked %v with %s rules, want revoked with none", revoked, rules)
				}
			})
		})

		t.Run("a subject, a provider or a model that a rule names cannot be deleted", func(t *testing.T) {
			// Referential checks ignore row-level security: the deletes also run in sessions that cannot see
			// the rule. (PostgreSQL reports ON DELETE RESTRICT as foreign_key_violation, 23503; 23001 is what
			// the deferred trigger of a revoked key raises.)
			refused := func(what, constraint string, err error) {
				t.Helper()
				var pe *pgconn.PgError
				if !errors.As(err, &pe) || pe.Code != foreignKey || pe.ConstraintName != constraint {
					t.Errorf("%s: %v, want SQLSTATE %s on %s", what, err, foreignKey, constraint)
				}
			}
			refused("delete the mapping", "limit_rule_subject_mapping_fkey", errOf(execIn(t, ad, sessA, `DELETE FROM pandokos.subject_mapping WHERE id = $1::uuid`, smA)))
			refused("delete the trusted issuer (cascades to the mapping)", "limit_rule_subject_mapping_fkey", errOf(execIn(t, ad, sessA, `DELETE FROM pandokos.trusted_issuer WHERE id = $1::uuid`, issuerA)))

			pi := d.SeedPlatformIssuer(t, "integrity-platform", a)
			smP := d.SeedSubjectMapping(t, a, pi, true, saA, "subject-platform")
			if err := rule("key", nil, nil, smP, nil, nil, "tokens_per_day"); err != nil {
				t.Fatal(err)
			}
			refused("delete the platform issuer from the platform scope, which cannot see the rule", "limit_rule_subject_mapping_fkey",
				errOf(execIn(t, ad, platform, `DELETE FROM pandokos.platform_issuer WHERE id = $1::uuid`, pi)))
			refused("delete the model, from the platform scope", "limit_rule_model_fkey",
				errOf(execIn(t, ad, platform, `DELETE FROM pandokos.model WHERE id = $1::uuid`, modelOpenAI)))

			// A provider with nothing but a rule of a tenant, then a ceiling of the platform.
			bare := d.SeedProvider(t, "integrity-bare")
			if err := rule("tenant", nil, nil, nil, bare, nil, "tokens_per_month"); err != nil {
				t.Fatal(err)
			}
			refused("delete the provider, from the platform scope", "limit_rule_provider_fkey",
				errOf(execIn(t, ad, platform, `DELETE FROM pandokos.provider WHERE id = $1::uuid`, bare)))
			if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.limit_rule WHERE provider_id = $1::uuid`, bare); err != nil {
				t.Fatal(err)
			}
			if _, err := execIn(t, ad, platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, provider_id, metric, limit_value) VALUES (NULL, $1::uuid, 'tokens_per_month', 5)`, bare); err != nil {
				t.Fatal(err)
			}
			refused("delete the provider while a ceiling names it", "platform_limit_rule_provider_fkey",
				errOf(execIn(t, ad, platform, `DELETE FROM pandokos.provider WHERE id = $1::uuid`, bare)))
			if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.platform_limit_rule WHERE provider_id = $1::uuid`, bare); err != nil {
				t.Fatal(err)
			}
			expect("delete the provider when nothing names it", "", errOf(execIn(t, ad, platform, `DELETE FROM pandokos.provider WHERE id = $1::uuid`, bare)))

			// The model: refused while the platform's ceiling names it, free once nothing does.
			if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.limit_rule WHERE model_id = $1::uuid`, modelOpenAI); err != nil {
				t.Fatal(err)
			}
			if _, err := execIn(t, ad, platform, `INSERT INTO pandokos.platform_limit_rule (tenant_id, provider_id, model_id, metric, limit_value) VALUES (NULL, $1::uuid, $2::uuid, 'tokens_per_month', 5)`, openai, modelOpenAI); err != nil {
				t.Fatal(err)
			}
			refused("delete the model while a ceiling names it", "platform_limit_rule_model_fkey",
				errOf(execIn(t, ad, platform, `DELETE FROM pandokos.model WHERE id = $1::uuid`, modelOpenAI)))
			if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.platform_limit_rule WHERE model_id = $1::uuid`, modelOpenAI); err != nil {
				t.Fatal(err)
			}
			expect("delete the model when nothing names it", "", errOf(execIn(t, ad, platform, `DELETE FROM pandokos.model WHERE id = $1::uuid`, modelOpenAI)))
		})

		t.Run("the gateway reads the rules with their joins and the worker none", func(t *testing.T) {
			rows, err := query(t, d.Connect(t, "gateway"), sessA, `SELECT r.subject_kind, COALESCE(r.service_account_id::text, k.key_id, r.subject_mapping_id::text, '') AS subject_id
				FROM pandokos.limit_rule r LEFT JOIN pandokos.virtual_key k ON k.tenant_id = r.tenant_id AND k.id = r.virtual_key_id
				WHERE r.virtual_key_id IS NOT NULL`)
			if err != nil || len(rows) == 0 {
				t.Fatalf("the gateway cannot read rules with the key join: %v %v", rows, err)
			}
			for _, r := range rows {
				if r["subject_id"] == "" || len(r["subject_id"].(string)) != 17 {
					t.Errorf("the join gave %v, want a key ID", r["subject_id"])
				}
			}
			rows, err = query(t, d.Connect(t, "gateway"), sessions(b)[0], `SELECT count(*) AS n FROM pandokos.limit_rule WHERE tenant_id = $1::uuid`, a.String())
			if err != nil || rows[0]["n"] != int64(0) {
				t.Errorf("tenant B's session reads A's rules: %v %v", rows, err)
			}
			for _, tbl := range []string{"limit_rule", "platform_limit_rule"} {
				if _, err := query(t, d.Connect(t, "worker"), sessA, `SELECT count(*) FROM pandokos.`+tbl); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the worker role reads %s: %v", tbl, err)
				}
			}
		})
	})
}

func errOf(_ int64, err error) error { return err }
