package isolation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestRollupAccessMatrix is T-952, the part that is specific to sprint 6.2 (the generic negative
// tests over every tenant-scoped table, usage_rollup, usage_rollup_state, audit_chain and
// metadata_key_policy among them, run from their fixtures in Spec): the worker in A's session cannot
// read or change B's rollup; the gateway has no privilege on the rollup; the admin role reads it and
// cannot write it.
func TestRollupAccessMatrix(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "rollup-a"), d.SeedTenant(t, "rollup-b")
	super := d.Super(t)
	spec := DefaultSpec()
	for _, tid := range []tenant.ID{a, b} {
		for _, tbl := range []string{"usage_rollup", "usage_rollup_state"} {
			if _, err := super.Exec(ctx(t), spec.Fixtures[tbl].Insert, tid.String()); err != nil {
				t.Fatalf("%s: %v", tbl, err)
			}
		}
	}
	sessA := sessions(a)[0]
	worker, admin, gateway := d.Connect(t, "worker"), d.Connect(t, "admin"), d.Connect(t, "gateway")

	for _, tbl := range []string{"usage_rollup", "usage_rollup_state"} {
		// The worker and the admin role read their own tenant's rows and no other's.
		for name, conn := range map[string]*pgx.Conn{"worker": worker, "admin": admin} {
			var own, foreign int
			err := inSession(t, conn, sessA, func(c context.Context, tx pgx.Tx) error {
				if err := tx.QueryRow(c, `SELECT count(*) FROM pandokos.`+tbl+` WHERE tenant_id = $1::uuid`, a.String()).Scan(&own); err != nil {
					return err
				}
				return tx.QueryRow(c, `SELECT count(*) FROM pandokos.`+tbl+` WHERE tenant_id = $1::uuid`, b.String()).Scan(&foreign)
			})
			if err != nil || own != 1 || foreign != 0 {
				t.Errorf("%s reads %d own and %d foreign rows of %s (%v)", name, own, foreign, tbl, err)
			}
			for _, sess := range []session{sessions(a)[1], sessions(a)[2], sessions(a)[3]} {
				n, err := count2(t, conn, sess, `SELECT count(*) FROM pandokos.`+tbl)
				if err != nil || n != 0 {
					t.Errorf("%s reads %d rows of %s in session %s (%v): a session without a tenant sees none", name, n, tbl, sess.name, err)
				}
			}
		}
		// The gateway has no privilege on either table, in any session.
		for _, sess := range sessions(a) {
			for _, sql := range []string{`SELECT count(*) FROM pandokos.` + tbl, `DELETE FROM pandokos.` + tbl} {
				if _, err := execIn(t, gateway, sess, sql); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the gateway role: %s in session %s: %v, want SQLSTATE %s", sql, sess.name, err, insufficientPrivilege)
				}
			}
		}
		// The admin role never writes them.
		for _, sql := range []string{`DELETE FROM pandokos.` + tbl, `UPDATE pandokos.` + tbl + ` SET ` + spec.Fixtures[tbl].Update, `TRUNCATE pandokos.` + tbl} {
			if _, err := execIn(t, admin, sessA, sql); sqlstate(err) != insufficientPrivilege {
				t.Errorf("the admin role: %s: %v, want SQLSTATE %s", sql, err, insufficientPrivilege)
			}
		}
		// The worker writes only its own tenant's rows.
		if _, err := execIn(t, worker, sessA, spec.Fixtures[tbl].Insert, b.String()); sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker role inserted a row of %s for B in A's session: %v", tbl, err)
		}
		if n, err := execIn(t, worker, sessA, `UPDATE pandokos.`+tbl+` SET `+spec.Fixtures[tbl].Update+` WHERE tenant_id = $1::uuid`, b.String()); err == nil && n != 0 {
			t.Errorf("the worker role changed %d rows of %s of tenant B", n, tbl)
		}
		if n, err := execIn(t, worker, sessA, `DELETE FROM pandokos.`+tbl+` WHERE tenant_id = $1::uuid`, b.String()); err == nil && n != 0 {
			t.Errorf("the worker role deleted %d rows of %s of tenant B", n, tbl)
		}
	}
	// The worker moves the watermark and nothing else of the state.
	if _, err := execIn(t, worker, sessA, `UPDATE pandokos.usage_rollup_state SET watermark = now(), updated_at = now()`); err != nil {
		t.Errorf("the worker role cannot move its watermark: %v", err)
	}
	if _, err := execIn(t, worker, sessA, `UPDATE pandokos.usage_rollup_state SET tenant_id = $1::uuid`, b.String()); sqlstate(err) != insufficientPrivilege {
		t.Errorf("the worker role changed the tenant of its state: %v", err)
	}
}

// partitionNames lists the monthly partitions of the usage ledger.
func partitionNames(t *testing.T, super *pgx.Conn) []string {
	t.Helper()
	rows, err := super.Query(ctx(t), `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'pandokos.usage_record'::regclass AND c.relname ~ '^usage_record_y[0-9]{4}m[0-9]{2}$' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestUsageRetention is T-960: platform_retention drops the partitions that end on or before the
// cut and writes one usage.retention row in the platform log; tenant_retention removes the tenant's
// default-partition rows and rollup rows before the cut, in that tenant's session, and writes
// usage.retention in that tenant's log.
func TestUsageRetention(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "usage-a"), d.SeedTenant(t, "usage-b")
	super := d.Super(t)
	now := time.Now().UTC()
	first := func(back int) time.Time {
		return time.Date(now.Year(), now.Month()-time.Month(back), 1, 0, 0, 0, 0, time.UTC)
	}
	name := func(back int) string {
		m := first(back)
		return fmt.Sprintf("usage_record_y%04dm%02d", m.Year(), int(m.Month()))
	}
	// The cut of a retention of two months is the start of the month two months back: the partitions
	// of the months up to three months back end on or before it.
	for _, back := range []int{5, 4, 3, 2} {
		d.OldPartition(t, first(back))
	}
	// Rows in the default partition, which no partition holds: ten months back.
	old := first(10).Add(36 * time.Hour)
	d.SeedUsage(t, a, dbtest.UsageRow{StartedAt: old}, dbtest.UsageRow{StartedAt: old.Add(time.Hour)}, dbtest.UsageRow{StartedAt: now})
	d.SeedUsage(t, b, dbtest.UsageRow{StartedAt: old})
	for _, tid := range []tenant.ID{a, b} {
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.usage_rollup (tenant_id, hour, principal_kind, principal_id, key_id, provider, model, model_id, currency, price_source, outcome, credential,
				requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, estimated_input_tokens, estimated_output_tokens, cost, cost_known, cost_unknown)
			SELECT $1::uuid, h, '', '', '', '', '', '', '', '', 'ok', true, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0 FROM unnest($2::timestamptz[]) h`,
			tid.String(), []time.Time{old, old.Add(time.Hour), now.Truncate(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	worker := d.Connect(t, "worker")
	platform, sessA := sessions(a)[3], sessions(a)[0]

	t.Run("platform_retention drops the partitions before the cut and says so", func(t *testing.T) {
		var dropped int
		var auditRows int64
		err := inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT partitions_dropped, audit_rows FROM pandokos.platform_retention(2, 0)`).Scan(&dropped, &auditRows)
		})
		if err != nil || dropped != 3 || auditRows != 0 {
			t.Fatalf("platform_retention(2, 0): %d dropped, %d audit rows, %v, want 3 and 0", dropped, auditRows, err)
		}
		have := strings.Join(partitionNames(t, super), ",")
		for _, back := range []int{5, 4, 3} {
			if strings.Contains(have, name(back)) {
				t.Errorf("the partition %s is still there", name(back))
			}
		}
		for _, back := range []int{2, 1, 0} {
			if !strings.Contains(have, name(back)) {
				t.Errorf("the partition %s was dropped: it ends after the cut", name(back))
			}
		}
		var n int
		var detail map[string]any
		if err := super.QueryRow(ctx(t), `SELECT count(*), (array_agg(detail))[1] FROM pandokos.platform_audit_event WHERE action = 'usage.retention'`).Scan(&n, &detail); err != nil || n != 1 {
			t.Fatalf("%d usage.retention rows (%v), want 1", n, err)
		}
		if parts, _ := json.Marshal(detail["partitions"]); string(parts) != fmt.Sprintf(`["%s","%s","%s"]`, name(5), name(4), name(3)) {
			t.Errorf("the row names %s, want the three partitions", parts)
		}
		rows, head := chainOf(t, super, "platform")
		requireChain(t, "platform", rows, head)
	})
	t.Run("a second run is idempotent", func(t *testing.T) {
		var dropped int
		err := inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT partitions_dropped FROM pandokos.platform_retention(2, 0)`).Scan(&dropped)
		})
		var n int
		_ = super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'usage.retention'`).Scan(&n)
		if err != nil || dropped != 0 || n != 1 {
			t.Errorf("a second run: %d dropped (%v), %d usage.retention rows, want 0 and 1", dropped, err, n)
		}
	})
	t.Run("a value out of range, a tenant session and the admin role are refused", func(t *testing.T) {
		for _, months := range []int{1, 121} {
			err := inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(c, `SELECT * FROM pandokos.platform_retention($1, 0)`, months)
				return err
			})
			if sqlstate(err) != checkViolation {
				t.Errorf("platform_retention(%d, 0): %v, want SQLSTATE %s", months, err, checkViolation)
			}
			err = inSession(t, worker, sessA, func(c context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(c, `SELECT * FROM pandokos.tenant_retention($1, 0)`, months)
				return err
			})
			if sqlstate(err) != checkViolation {
				t.Errorf("tenant_retention(%d, 0): %v, want SQLSTATE %s", months, err, checkViolation)
			}
		}
		if _, err := execIn(t, worker, sessA, `SELECT * FROM pandokos.platform_retention(2, 0)`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("platform_retention in a tenant session: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
		if _, err := execIn(t, worker, sessions(a)[1], `SELECT * FROM pandokos.tenant_retention(2, 0)`); sqlstate(err) != insufficientPrivilege {
			t.Errorf("tenant_retention without a tenant: %v, want SQLSTATE %s", err, insufficientPrivilege)
		}
		for _, role := range []string{"admin", "gateway"} {
			conn := d.Connect(t, role)
			for _, sql := range []string{`SELECT * FROM pandokos.platform_retention(2, 0)`, `SELECT * FROM pandokos.tenant_retention(2, 0)`} {
				if _, err := execIn(t, conn, platform, sql); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the %s role: %s: %v, want SQLSTATE %s", role, sql, err, insufficientPrivilege)
				}
			}
		}
	})
	t.Run("tenant_retention removes the tenant's old rows, not another tenant's", func(t *testing.T) {
		var usage, rollup, audit int64
		err := inSession(t, worker, sessA, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT usage_rows, rollup_rows, audit_rows FROM pandokos.tenant_retention(2, 0)`).Scan(&usage, &rollup, &audit)
		})
		if err != nil || usage != 2 || rollup != 2 || audit != 0 {
			t.Fatalf("tenant_retention(2, 0): %d usage, %d rollup, %d audit rows, %v, want 2, 2 and 0", usage, rollup, audit, err)
		}
		count := func(tbl string, tid tenant.ID) int {
			var n int
			if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.`+tbl+` WHERE tenant_id = $1::uuid`, tid.String()).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := count("usage_record", a); n != 1 {
			t.Errorf("tenant A keeps %d ledger rows, want the one of this month", n)
		}
		if n := count("usage_rollup", a); n != 1 {
			t.Errorf("tenant A keeps %d rollup rows, want the one of this hour", n)
		}
		if count("usage_record", b) != 1 || count("usage_rollup", b) != 3 {
			t.Errorf("tenant B's rows changed: %d ledger, %d rollup", count("usage_record", b), count("usage_rollup", b))
		}
		var n int
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'usage.retention'`, a.String()).Scan(&n); err != nil || n != 1 {
			t.Errorf("%d usage.retention rows in A's log (%v), want 1", n, err)
		}
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'usage.retention'`, b.String()).Scan(&n); err != nil || n != 0 {
			t.Errorf("%d usage.retention rows in B's log (%v), want none", n, err)
		}
		rows, head := chainOf(t, super, tenantScope(a))
		requireChain(t, tenantScope(a), rows, head)
		// Nothing left to remove: no new row.
		err = inSession(t, worker, sessA, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT usage_rows, rollup_rows, audit_rows FROM pandokos.tenant_retention(2, 0)`).Scan(&usage, &rollup, &audit)
		})
		if err != nil || usage+rollup+audit != 0 {
			t.Errorf("a second run removed %d %d %d (%v)", usage, rollup, audit, err)
		}
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'usage.retention'`, a.String()).Scan(&n); err != nil || n != 1 {
			t.Errorf("%d usage.retention rows after the second run, want 1", n)
		}
	})
}

// TestMetadataKeyPolicyTable is T-975, the part specific to sprint 6.2 (the generic negative tests
// run from the fixture in Spec): the check refuses 65 keys and a key of the wrong format; one row per
// tenant; a change notifies catalog:<tenant>.
func TestMetadataKeyPolicyTable(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "keys-a"), d.SeedTenant(t, "keys-b")
	admin := d.Connect(t, "admin")
	sessA := sessions(a)[0]
	insert := func(keys ...string) error {
		_, err := execIn(t, admin, sessA, `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, $2::text[])`, a.String(), keys)
		return err
	}
	var sixtyFive []string
	for i := 0; i < 65; i++ {
		sixtyFive = append(sixtyFive, fmt.Sprintf("k%d", i))
	}
	clean := func() {
		t.Helper()
		if _, err := execIn(t, admin, sessA, `DELETE FROM pandokos.metadata_key_policy`); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name string
		keys []string
	}{
		{"65 keys", sixtyFive},
		{"an upper-case key", []string{"Team"}},
		{"a key of 33 chars", []string{strings.Repeat("a", 33)}},
		{"an empty key", []string{""}},
		{"a key with a space", []string{"two words"}},
		{"a key with a dot", []string{"a.b"}},
		{"a key starting with -", []string{"-a"}},
	} {
		err := insert(c.keys...)
		if sqlstate(err) != checkViolation {
			t.Errorf("%s: %v, want SQLSTATE %s", c.name, err, checkViolation)
		}
		if err == nil {
			clean()
		}
	}
	if _, err := execIn(t, admin, sessA, `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, ARRAY['a', NULL])`, a.String()); sqlstate(err) != checkViolation {
		t.Errorf("a null key: %v, want SQLSTATE %s", err, checkViolation)
	}
	clean()
	listener := d.Super(t)
	if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
		t.Fatal(err)
	}
	var sixtyFour []string
	for i := 0; i < 64; i++ {
		sixtyFour = append(sixtyFour, fmt.Sprintf("k%d", i))
	}
	if err := insert(sixtyFour...); err != nil {
		t.Errorf("64 keys: %v", err)
	}
	if err := insert("team"); sqlstate(err) != uniqueViolation {
		t.Errorf("a second row for the tenant: %v, want SQLSTATE %s", err, uniqueViolation)
	}
	if got := notifications(listener, 500*time.Millisecond); !containsAll(got, "catalog:"+a.String()) {
		t.Errorf("an insert notified %v, want catalog:%s", got, a)
	}
	if _, err := execIn(t, admin, sessA, `UPDATE pandokos.metadata_key_policy SET keys = ARRAY['team']`); err != nil {
		t.Fatal(err)
	}
	if got := notifications(listener, 500*time.Millisecond); !containsAll(got, "catalog:"+a.String()) {
		t.Errorf("an update notified %v, want catalog:%s", got, a)
	}
	if _, err := execIn(t, admin, sessA, `DELETE FROM pandokos.metadata_key_policy`); err != nil {
		t.Fatal(err)
	}
	if got := notifications(listener, 500*time.Millisecond); !containsAll(got, "catalog:"+a.String()) {
		t.Errorf("a delete notified %v, want catalog:%s", got, a)
	}
	// B's rows are not A's: a policy for B cannot be written in A's session.
	if _, err := execIn(t, admin, sessA, `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, ARRAY['team'])`, b.String()); sqlstate(err) != insufficientPrivilege {
		t.Errorf("a row for B in A's session: %v, want SQLSTATE %s", err, insufficientPrivilege)
	}
	// The gateway reads it; the worker does not.
	if n, err := count2(t, d.Connect(t, "gateway"), sessA, `SELECT count(*) FROM pandokos.metadata_key_policy`); err != nil || n != 0 {
		t.Errorf("the gateway role: %d %v", n, err)
	}
	if _, err := count2(t, d.Connect(t, "worker"), sessA, `SELECT count(*) FROM pandokos.metadata_key_policy`); sqlstate(err) != insufficientPrivilege {
		t.Errorf("the worker role reads the policy: %v", err)
	}
}

func containsAll(got []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			return false
		}
	}
	return true
}
