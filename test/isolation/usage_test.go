package isolation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/test/dbtest"
)

// usageJSONFor is one record in the JSON form of InsertUsageRecords; it names the given tenant,
// which the statement must ignore.
func usageJSONFor(id, tenantInJSON string, started time.Time) string {
	return `[{"id":"` + id + `","tenant_id":"` + tenantInJSON + `","started_at":"` + started.UTC().Format(time.RFC3339Nano) + `",` +
		`"request_id":"0123456789abcdef0123456789abcdef","trace_id":"","principal_kind":"service_account","principal_id":"0192f0f0-0000-7000-8000-0000000000a2","key_id":"",` +
		`"funding":"byok","provider":"openai","api_family":"openai","operation":"chat.completions","endpoint":"","model":"","upstream_model":"",` +
		`"outcome":"ok","error_code":"","duration_ms":1,"input_source":"none","output_source":"none","cost_status":"none","cost_reason":"",` +
		`"other_units":{},"provider_usage":{},"metadata":{},"metadata_dropped":0}]`
}

func countUsage(t *testing.T, super *pgx.Conn, where string, args ...any) int {
	t.Helper()
	var n int
	if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.usage_record `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestUsageRecord is T-480: the grants and the policy of usage_record, for every runtime role.
func TestUsageRecord(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "usage-a"), d.SeedTenant(t, "usage-b")
	super := d.Super(t)
	gateway, admin, worker := d.Connect(t, "gateway"), d.Connect(t, "admin"), d.Connect(t, "worker")
	now := time.Now().UTC().Truncate(time.Microsecond)
	const id1, id2 = "0192f0f0-0000-7000-8000-0000000000d1", "0192f0f0-0000-7000-8000-0000000000d2"

	insert := func(conn *pgx.Conn, s session, records string) (int64, error) {
		var n int64
		err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			var err error
			n, err = queries.New(tx).InsertUsageRecords(c, records)
			return err
		})
		return n, err
	}
	tenantA, tenantB := sessions(a)[0], sessions(b)[0]

	t.Run("the gateway role inserts for the tenant of its session, whatever the JSON says", func(t *testing.T) {
		n, err := insert(gateway, tenantA, usageJSONFor(id1, b.String(), now))
		if err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
		if got := countUsage(t, super, `WHERE tenant_id = $1::uuid`, a.String()); got != 1 {
			t.Errorf("tenant A has %d rows", got)
		}
		if got := countUsage(t, super, `WHERE tenant_id = $1::uuid`, b.String()); got != 0 {
			t.Errorf("tenant B has %d rows: the JSON chose the tenant", got)
		}
	})

	t.Run("no insert without a tenant", func(t *testing.T) {
		for _, s := range sessions(a)[1:] {
			if _, err := insert(gateway, s, usageJSONFor(id2, a.String(), now)); sqlstate(err) != insufficientPrivilege {
				t.Errorf("session %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
			}
		}
	})

	t.Run("an insert for another tenant is refused", func(t *testing.T) {
		err := inSession(t, gateway, tenantA, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
				provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status)
				VALUES ($1::uuid, pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok',
				'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`, b.String())
			return err
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("%v, want SQLSTATE %s", err, insufficientPrivilege)
		}
	})

	t.Run("the gateway role cannot read, change or remove a row", func(t *testing.T) {
		for name, sql := range map[string]string{
			"SELECT":   `SELECT id FROM pandokos.usage_record`,
			"COUNT":    `SELECT count(*) FROM pandokos.usage_record`,
			"UPDATE":   `UPDATE pandokos.usage_record SET model = 'changed'`,
			"DELETE":   `DELETE FROM pandokos.usage_record`,
			"TRUNCATE": `TRUNCATE pandokos.usage_record`,
		} {
			err := inSession(t, gateway, tenantA, func(c context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(c, sql)
				return err
			})
			if sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s: %v, want SQLSTATE %s", name, err, insufficientPrivilege)
			}
		}
		// ON CONFLICT with a target would need SELECT: the statement does not use one.
		err := inSession(t, gateway, tenantA, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
				provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status)
				SELECT tenant_id, id, started_at, request_id, principal_kind, principal_id, funding, provider, api_family, operation, outcome, duration_ms,
				input_source, output_source, cost_status FROM pandokos.usage_record ON CONFLICT (tenant_id, started_at, id) DO NOTHING`)
			return err
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("a statement that reads the table: %v", err)
		}
	})

	t.Run("the same ID in two tenants gives two rows and tells neither about the other", func(t *testing.T) {
		nB, err := insert(gateway, tenantB, usageJSONFor(id1, a.String(), now))
		if err != nil || nB != 1 {
			t.Fatalf("tenant B: %d %v", nB, err)
		}
		if got := countUsage(t, super, `WHERE id = $1::uuid`, id1); got != 2 {
			t.Errorf("%d rows with the ID of the first record, want 2", got)
		}
		if again, err := insert(gateway, tenantA, usageJSONFor(id1, a.String(), now)); err != nil || again != 0 {
			t.Errorf("a repeated batch of tenant A: %d %v, want 0 rows", again, err)
		}
		if got := countUsage(t, super, `WHERE id = $1::uuid`, id1); got != 2 {
			t.Errorf("%d rows after the repeat", got)
		}
	})

	for name, conn := range map[string]*pgx.Conn{"admin": admin, "worker": worker} {
		t.Run(name+" reads the rows of the session's tenant only, and writes nothing", func(t *testing.T) {
			read := func(s session) (own, foreign int) {
				err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
					rows, err := tx.Query(c, `SELECT tenant_id::text FROM pandokos.usage_record`)
					if err != nil {
						return err
					}
					ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
					for _, id := range ids {
						if id == a.String() {
							own++
						} else {
							foreign++
						}
					}
					return err
				})
				if err != nil {
					t.Fatalf("%s %s: %v", name, s.name, err)
				}
				return own, foreign
			}
			if own, foreign := read(tenantA); own != 1 || foreign != 0 {
				t.Errorf("tenant A's session: %d own rows and %d foreign rows", own, foreign)
			}
			for _, s := range sessions(a)[1:] {
				if own, foreign := read(s); own+foreign != 0 {
					t.Errorf("session %s: %d rows without a tenant", s.name, own+foreign)
				}
			}
			for op, sql := range map[string]string{
				"INSERT": `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
					provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status)
					VALUES (pandokos.current_tenant_id(), pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok',
					'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`,
				"UPDATE":   `UPDATE pandokos.usage_record SET model = 'changed'`,
				"DELETE":   `DELETE FROM pandokos.usage_record`,
				"TRUNCATE": `TRUNCATE pandokos.usage_record`,
			} {
				err := inSession(t, conn, tenantA, func(c context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(c, sql)
					return err
				})
				if sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s %s: %v, want SQLSTATE %s", name, op, err, insufficientPrivilege)
				}
			}
		})
	}
}

// monthName is the name of the partition of the month that starts at m.
func monthName(m time.Time) string {
	return fmt.Sprintf("usage_record_y%04dm%02d", m.Year(), int(m.Month()))
}

func firstOfMonth(n int) time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
}

const insertAt = `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
	provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status)
	VALUES ($1::uuid, pandokos.uuid_v7(), $2, md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok',
	'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`

// TestUsagePartitions is T-481: the function that creates partitions, the protection of a
// partition and rule L-20.
func TestUsagePartitions(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "partitions")
	super := d.Super(t)
	gateway, admin, worker := d.Connect(t, "gateway"), d.Connect(t, "admin"), d.Connect(t, "worker")

	ensure := func(conn *pgx.Conn, ahead int) (created int, inUse bool, err error) {
		err = inSession(t, conn, session{name: "none"}, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT created, default_in_use FROM pandokos.ensure_usage_partitions($1)`, ahead).Scan(&created, &inUse)
		})
		return created, inUse, err
	}
	exists := func(name string) bool {
		var ok bool
		if err := super.QueryRow(ctx(t), `SELECT to_regclass('pandokos.' || $1) IS NOT NULL`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	t.Run("only the worker executes ensure_usage_partitions", func(t *testing.T) {
		for name, conn := range map[string]*pgx.Conn{"gateway": gateway, "admin": admin} {
			if _, _, err := ensure(conn, 3); sqlstate(err) != insufficientPrivilege {
				t.Errorf("%s: %v, want SQLSTATE %s", name, err, insufficientPrivilege)
			}
		}
		if created, inUse, err := ensure(worker, 3); err != nil || created != 0 || inUse {
			t.Errorf("worker: %d %v %v", created, inUse, err)
		}
	})

	t.Run("an argument out of 1 to 12 is refused", func(t *testing.T) {
		for _, n := range []int{0, 13, -1} {
			if _, _, err := ensure(worker, n); sqlstate(err) != checkViolation {
				t.Errorf("ensure(%d): %v, want SQLSTATE %s", n, err, checkViolation)
			}
		}
	})

	t.Run("rows at the edge of a month land in their partitions", func(t *testing.T) {
		next := firstOfMonth(1)
		for _, at := range []time.Time{next.Add(-time.Microsecond), next} {
			if _, err := super.Exec(ctx(t), insertAt, tid.String(), at); err != nil {
				t.Fatal(err)
			}
			var table string
			if err := super.QueryRow(ctx(t), `SELECT tableoid::regclass::text FROM pandokos.usage_record WHERE started_at = $1`, at).Scan(&table); err != nil {
				t.Fatal(err)
			}
			want := monthName(time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC))
			if !strings.HasSuffix(table, want) {
				t.Errorf("a row at %s is in %s, want %s", at.Format(time.RFC3339Nano), table, want)
			}
		}
	})

	t.Run("a month whose rows are in the default partition is skipped, and a row five years ahead is reported", func(t *testing.T) {
		skipped := firstOfMonth(5)
		for _, at := range []time.Time{skipped.Add(time.Hour), time.Now().AddDate(5, 0, 0)} {
			if _, err := super.Exec(ctx(t), insertAt, tid.String(), at); err != nil {
				t.Fatal(err)
			}
			var table string
			if err := super.QueryRow(ctx(t), `SELECT tableoid::regclass::text FROM pandokos.usage_record WHERE started_at = $1`, at).Scan(&table); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(table, "usage_record_default") {
				t.Errorf("a row at %s is in %s, want the default partition", at, table)
			}
		}
		created, inUse, err := ensure(worker, 6)
		if err != nil {
			t.Fatalf("a month with rows in the default partition made the function fail: %v", err)
		}
		if created != 2 || !inUse {
			t.Errorf("created %d, default_in_use %v, want 2 and true", created, inUse)
		}
		if exists(monthName(skipped)) {
			t.Error("the month whose rows are in the default partition got a partition")
		}
		for _, n := range []int{4, 6} {
			if !exists(monthName(firstOfMonth(n))) {
				t.Errorf("the partition of month +%d was not created", n)
			}
		}
	})

	t.Run("two concurrent calls succeed", func(t *testing.T) {
		name := monthName(firstOfMonth(2))
		owned(t, d, `DROP TABLE pandokos.`+name)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			conn := d.Connect(t, "worker")
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, errs[i] = ensure(conn, 3)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}
		if !exists(name) {
			t.Error("the partition was not created again")
		}
	})

	t.Run("no runtime role reaches a partition by name, nor protect_usage_partition", func(t *testing.T) {
		name := pgx.Identifier{"pandokos", monthName(firstOfMonth(0))}.Sanitize()
		for role, conn := range map[string]*pgx.Conn{"gateway": gateway, "admin": admin, "worker": worker} {
			tenantSession := sessions(tid)[0]
			for what, sql := range map[string]string{
				"SELECT": `SELECT count(*) FROM ` + name,
				"INSERT": `INSERT INTO ` + name + ` (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding, provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status)
					VALUES (pandokos.current_tenant_id(), pandokos.uuid_v7(), now(), md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok', 'openai', 'openai', 'x', 'ok', 1, 'none', 'none', 'none')`,
				"UPDATE":   `UPDATE ` + name + ` SET model = 'x'`,
				"DELETE":   `DELETE FROM ` + name,
				"TRUNCATE": `TRUNCATE ` + name,
				"protect":  `SELECT pandokos.protect_usage_partition('usage_record_default')`,
			} {
				err := inSession(t, conn, tenantSession, func(c context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(c, sql)
					return err
				})
				if sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s %s: %v, want SQLSTATE %s", role, what, err, insufficientPrivilege)
				}
			}
		}
	})

	t.Run("the lint has no finding for the partitions the function made", func(t *testing.T) {
		findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range findings {
			t.Errorf("%s %s: %s", f.Rule, f.Object, f.Detail)
		}
	})
}

// TestUsagePartitionLint is T-481 for rule L-20: a partition made by hand without protection
// gives one finding per sub-rule.
func TestUsagePartitionLint(t *testing.T) {
	const name = "usage_record_y2099m01"
	const create = `CREATE TABLE pandokos.` + name + ` PARTITION OF pandokos.usage_record FOR VALUES FROM ('2099-01-01 00:00:00+00') TO ('2099-02-01 00:00:00+00');`
	cases := []struct {
		sub  string
		ddl  string
		post string
	}{
		{sub: "(a) owner", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `');`, post: `ALTER TABLE pandokos.` + name + ` OWNER TO pandokos_admin`},
		{sub: "(b) a privilege of a runtime role", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); GRANT SELECT ON pandokos.` + name + ` TO pandokos_worker;`},
		{sub: "(b) a column privilege", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); GRANT INSERT (model) ON pandokos.` + name + ` TO pandokos_gateway;`},
		{sub: "(b) PUBLIC", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); GRANT SELECT ON pandokos.` + name + ` TO PUBLIC;`},
		{sub: "(c) row-level security not enabled", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); ALTER TABLE pandokos.` + name + ` DISABLE ROW LEVEL SECURITY;`},
		{sub: "(c) row-level security not forced", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); ALTER TABLE pandokos.` + name + ` NO FORCE ROW LEVEL SECURITY;`},
		{sub: "(c) no policy", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); DROP POLICY tenant_isolation ON pandokos.` + name + `;`},
		{sub: "(c) a wide policy", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); DROP POLICY tenant_isolation ON pandokos.` + name + `;
			CREATE POLICY tenant_isolation ON pandokos.` + name + ` USING (true) WITH CHECK (true);`},
		{sub: "(c) a second policy", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); CREATE POLICY wide ON pandokos.` + name + ` FOR SELECT USING (true);`},
		{sub: "(d) no truncate trigger", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); DROP TRIGGER ` + name + `_no_truncate ON pandokos.` + name + `;`},
		{sub: "(d) a disabled truncate trigger", ddl: create + `SELECT pandokos.protect_usage_partition('` + name + `'); ALTER TABLE pandokos.` + name + ` DISABLE TRIGGER ` + name + `_no_truncate;`},
	}
	for _, c := range cases {
		t.Run(c.sub, func(t *testing.T) {
			d := dbtest.NewMigrated(t)
			owned(t, d, c.ddl)
			if c.post != "" {
				if _, err := d.Super(t).Exec(ctx(t), c.post); err != nil {
					t.Fatal(err)
				}
			}
			findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, f := range findings {
				if f.Rule == "L-20" {
					n++
					if f.Object != "pandokos."+name {
						t.Errorf("an L-20 finding on %s", f.Object)
					}
				}
			}
			if n != 1 {
				t.Errorf("%d L-20 findings, want exactly one; findings: %+v", n, findings)
			}
		})
	}

	t.Run("a partition made by hand and left unprotected", func(t *testing.T) {
		d := dbtest.NewMigrated(t)
		owned(t, d, create)
		if _, err := d.Super(t).Exec(ctx(t), `ALTER TABLE pandokos.`+name+` OWNER TO pandokos_admin; GRANT SELECT ON pandokos.`+name+` TO pandokos_worker`); err != nil {
			t.Fatal(err)
		}
		findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
		if err != nil {
			t.Fatal(err)
		}
		var l20 []Finding
		for _, f := range findings {
			if f.Rule == "L-20" {
				l20 = append(l20, f)
			}
		}
		// The owner (a), a privilege (b), no row-level security or policy (c) and no trigger (d).
		if len(l20) < 4 {
			t.Errorf("%d L-20 findings for a partition that breaks every sub-rule, want at least 4: %+v", len(l20), l20)
		}
		details := map[string]bool{}
		for _, f := range l20 {
			details[f.Detail] = true
		}
		if len(details) != len(l20) {
			t.Errorf("findings repeat: %+v", l20)
		}
	})
}

// TestUsageAppendOnly is T-482: the owner cannot update or delete a row through the parent or a
// partition, nor truncate either; rule L-17 passes.
func TestUsageAppendOnly(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "append-only")
	super := d.Super(t)
	if _, err := super.Exec(ctx(t), insertAt, tid.String(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var part string
	if err := super.QueryRow(ctx(t), `SELECT tableoid::regclass::text FROM pandokos.usage_record LIMIT 1`).Scan(&part); err != nil {
		t.Fatal(err)
	}
	owner := d.Connect(t, "owner")
	tenantSession := sessions(tid)[0]
	for _, target := range []string{"pandokos.usage_record", part} {
		for op, sql := range map[string]string{
			"UPDATE":   `UPDATE ` + target + ` SET model = 'changed'`,
			"DELETE":   `DELETE FROM ` + target,
			"TRUNCATE": `TRUNCATE ` + target,
		} {
			for who, conn := range map[string]*pgx.Conn{"owner": owner, "superuser": super} {
				var err error
				if who == "owner" {
					err = inSession(t, conn, tenantSession, func(c context.Context, tx pgx.Tx) error {
						_, err := tx.Exec(c, sql)
						return err
					})
				} else {
					_, err = conn.Exec(ctx(t), sql)
				}
				if sqlstate(err) != restrictViolation {
					t.Errorf("%s %s on %s: %v, want SQLSTATE %s", who, op, target, err, restrictViolation)
				}
			}
		}
	}
	if got := countUsage(t, super, ``); got != 1 {
		t.Errorf("%d rows survive, want 1", got)
	}
	findings, err := Lint(ctx(t), d.Super(t), DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Rule == "L-17" {
			t.Errorf("%s %s: %s", f.Rule, f.Object, f.Detail)
		}
	}
}
