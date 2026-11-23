package migrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestUpgrade0014 is T-853: migration 0014 on a database with the data of 0013.
func TestUpgrade0014(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 13)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	scalar := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx, q, args...).Scan(&s); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return s
	}
	tid := scalar(`INSERT INTO pandokos.tenant (name) VALUES ('upgrade-roles') RETURNING id::text`)
	other := scalar(`INSERT INTO pandokos.tenant (name) VALUES ('upgrade-roles-other') RETURNING id::text`)
	sa := func(tenant, name, role string) string {
		id := scalar(`INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, $2) RETURNING id::text`, tenant, name)
		exec(`INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, $3)`, tenant, id, role)
		return id
	}
	adminSA := sa(tid, "first-admin", "tenant-admin")
	inferenceSA := sa(tid, "worker", "inference")
	otherSA := sa(other, "other-admin", "tenant-admin")
	exec(`INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'inference')`, tid, adminSA)

	user := func(tenant any, name string) string {
		return scalar(`INSERT INTO pandokos.app_user (sign_in, tenant_id, display_name) VALUES ('local', $1::uuid, $2) RETURNING id::text`, tenant, name)
	}
	member := func(tenant, userID, source, role string) {
		exec(`INSERT INTO pandokos.membership (tenant_id, user_id, source) VALUES ($1::uuid, $2::uuid, $3) ON CONFLICT DO NOTHING`, tenant, userID, source)
		exec(`INSERT INTO pandokos.membership_role (tenant_id, user_id, role, source) VALUES ($1::uuid, $2::uuid, $3, $4)`, tenant, userID, role, source)
	}
	byHand := user(tid, "by hand")
	member(tid, byHand, "manual", "tenant-admin")
	byRule := user(nil, "by rule")
	member(tid, byRule, "idp", "tenant-admin")
	reader := user(tid, "reader") // holds no tenant-admin
	member(tid, reader, "manual", "tenant-admin")
	exec(`DELETE FROM pandokos.membership_role WHERE user_id = $1::uuid`, reader)
	member(tid, reader, "manual", "inference")

	exec(`INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, 'tabcdefghijklmnop', sha256('k'::bytea))`, tid, adminSA)
	exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome) VALUES ($1::uuid, 'anonymous', 'auth.failure', 'virtual_key', 'failure')`, tid)

	// What the migration must not change: every row of these tables but the owner rows it adds.
	snapshot := func(tbl string) string {
		t.Helper()
		where := ""
		switch tbl {
		case "role_assignment", "membership_role":
			where = " WHERE t.role <> 'owner'"
		}
		return scalar(`SELECT COALESCE(jsonb_agg(to_jsonb(t) - 'support_action_id' ORDER BY to_jsonb(t)::text)::text, '[]') FROM pandokos.` + tbl + ` t` + where)
	}
	tables := []string{"tenant", "service_account", "role_assignment", "virtual_key", "app_user", "membership", "membership_role", "audit_event", "platform_audit_event", "platform_key"}
	before := map[string]string{}
	for _, tbl := range tables {
		before[tbl] = snapshot(tbl)
	}
	auditBefore := scalar(`SELECT count(*)::text FROM pandokos.audit_event`)

	d.MigrateTo(t, 14)

	for _, tbl := range tables {
		if got := snapshot(tbl); got != before[tbl] {
			t.Errorf("%s changed by the migration:\n%s\n%s", tbl, before[tbl], got)
		}
	}

	// Whoever held tenant-admin by an administrator's hand holds owner as well; nobody else does.
	owners := func(q string, args ...any) []string {
		rows, err := super.Query(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	gotSA := owners(`SELECT service_account_id::text FROM pandokos.role_assignment WHERE role = 'owner' ORDER BY 1`)
	wantSA := []string{adminSA, otherSA}
	if strings.Join(gotSA, ",") != strings.Join(sortedStrings(wantSA), ",") {
		t.Errorf("service accounts with owner: %v, want %v (the holders of tenant-admin, not the inference account %s)", gotSA, wantSA, inferenceSA)
	}
	if n := scalar(`SELECT count(*)::text FROM pandokos.role_assignment WHERE service_account_id = $1::uuid AND role IN ('tenant-admin', 'owner', 'inference')`, adminSA); n != "3" {
		t.Errorf("the first administrator holds %s of tenant-admin, owner and inference, want all three", n)
	}
	gotMembers := owners(`SELECT user_id::text FROM pandokos.membership_role WHERE role = 'owner' ORDER BY 1`)
	if len(gotMembers) != 1 || gotMembers[0] != byHand {
		t.Errorf("members with owner: %v, want only the member made by hand (%s)", gotMembers, byHand)
	}
	if src := scalar(`SELECT source FROM pandokos.membership_role WHERE role = 'owner' AND user_id = $1::uuid`, byHand); src != "manual" {
		t.Errorf("the owner row of a manual member has the source %s", src)
	}
	if n := scalar(`SELECT count(*)::text FROM pandokos.membership_role WHERE user_id = $1::uuid AND role = 'owner'`, byRule); n != "0" {
		t.Errorf("a member whose role is kept by the group rules got owner")
	}

	// Both tables are forced again; so are the new ones.
	for _, tbl := range []string{"role_assignment", "membership_role", "tenant_role", "support_action"} {
		if got := scalar(`SELECT (relrowsecurity AND relforcerowsecurity)::text FROM pg_class WHERE oid = ('pandokos.' || $1)::regclass`, tbl); got != "true" {
			t.Errorf("row level security on %s is not enabled and forced after the migration", tbl)
		}
	}

	// audit_event kept its rows and accepts the new column, for a support action and for none.
	if got := scalar(`SELECT count(*)::text FROM pandokos.audit_event`); got != auditBefore {
		t.Errorf("audit_event holds %s rows, held %s", got, auditBefore)
	}
	action := scalar(`INSERT INTO pandokos.support_action (tenant_id, actor_type, actor_id, reason, permissions, expires_at)
		VALUES ($1::uuid, 'platform', 'pabcdefghijklmnop', 'a reason', ARRAY['member:read'], now() + interval '1 hour') RETURNING id::text`, tid)
	exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome, support_action_id) VALUES ($1::uuid, 'platform', 'member.add', 'member', 'success', $2::uuid)`, tid, action)
	if got := scalar(`SELECT count(*)::text FROM pandokos.audit_event WHERE support_action_id IS NULL`); got != auditBefore {
		t.Errorf("%s audit rows without an action, want the %s of before", got, auditBefore)
	}

	// The admin role may insert the column, the gateway and worker roles have no new privilege on the new tables.
	var can bool
	if err := super.QueryRow(ctx, `SELECT has_column_privilege($1, 'pandokos.audit_event', 'support_action_id', 'INSERT')`, d.User("admin")).Scan(&can); err != nil || !can {
		t.Errorf("the admin role cannot insert audit_event.support_action_id (%v)", err)
	}
	for _, role := range []string{"gateway", "worker"} {
		var got bool
		if err := super.QueryRow(ctx, `SELECT has_table_privilege($1, 'pandokos.support_action', 'SELECT') OR has_table_privilege($1, 'pandokos.tenant_role', 'INSERT')
			OR has_table_privilege($1, 'pandokos.tenant_role', 'UPDATE') OR has_table_privilege($1, 'pandokos.tenant_role', 'DELETE')`, d.User(role)).Scan(&got); err != nil || got {
			t.Errorf("%s has a privilege it must not have on the new tables (%v)", role, err)
		}
	}
	var gatewayReads bool
	if err := super.QueryRow(ctx, `SELECT has_table_privilege($1, 'pandokos.tenant_role', 'SELECT')`, d.User("gateway")).Scan(&gatewayReads); err != nil || !gatewayReads {
		t.Errorf("the gateway role cannot read tenant_role (%v)", err)
	}

	// A second run is refused, and the ledger holds the migration once.
	if _, err := d.Connect(t, "owner").Exec(ctx, migrationSQL(t, 14)); err == nil {
		t.Error("migration 0014 ran twice")
	}
	if n := scalar(`SELECT count(*)::text FROM pandokos.schema_migration WHERE version = 14`); n != "1" {
		t.Errorf("the ledger holds migration 14 %s times", n)
	}
	if !strings.Contains(migrationSQL(t, 14), "0014_roles_support") {
		t.Error("migration 14 is not 0014_roles_support")
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
