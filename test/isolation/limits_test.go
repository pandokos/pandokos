package isolation

import (
	"sort"
	"testing"

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
