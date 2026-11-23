package migrations_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/test/dbtest"
)

// TestUpgrade0015 is T-937: migration 0015 on a database with the audit rows of 0014, in two
// tenants and the platform: each chain is numbered by (occurred_at, id), verifies, ends at its head,
// no other column changes, and row-level security and the append-only triggers are back.
func TestUpgrade0015(t *testing.T) {
	d := dbtest.New(t)
	d.MigrateTo(t, 14)
	super := d.Super(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	a, b := d.SeedTenant(t, "upgrade-chain-a"), d.SeedTenant(t, "upgrade-chain-b")

	// Rows written the way the earlier migrations and the product wrote them: several in one
	// transaction (one occurred_at), several in later transactions, some with detail, a support
	// action id on one, accents and a number with trailing zeros in the detail.
	for i, tid := range []string{a.String(), b.String()} {
		for round := 0; round < 3; round++ {
			tx, err := super.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 4; j++ {
				if _, err := tx.Exec(ctx, `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, resource_id, outcome, request_id, detail)
					VALUES ($1::uuid, 'platform', $2, 'member.create', 'member', $3, 'success', md5($3), $4::jsonb)`,
					tid, "Zoë", fmt.Sprintf("r-%d-%d-%d", i, round, j), fmt.Sprintf(`{"n": %d.50, "ünï": ["a", null, true]}`, j)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome, reason) VALUES ($1::uuid, 'host', 'user.unlock', 'user', 'success', 'ticket')`, a.String())
	for round := 0; round < 3; round++ {
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			if _, err := tx.Exec(ctx, `INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, resource_id, outcome, detail)
				VALUES ('platform', 'p', 'tenant.create', 'tenant', $1, 'success', '{"x": 1}')`, fmt.Sprintf("p-%d-%d", round, j)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var sameTime int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM (SELECT occurred_at FROM pandokos.audit_event GROUP BY tenant_id, occurred_at HAVING count(*) > 1) x`).Scan(&sameTime); err != nil || sameTime == 0 {
		t.Fatalf("the test needs rows with equal occurred_at: %d (%v)", sameTime, err)
	}

	// What each chain must look like: the ids in the order of (occurred_at, id), and every column.
	order := func(sql string, args ...any) []string {
		t.Helper()
		rows, err := super.Query(ctx, sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	snap := func(tbl, where string) string {
		t.Helper()
		var s string
		if err := super.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) - 'seq' - 'prev_hash' - 'hash' ORDER BY id)::text, '[]') FROM pandokos.`+tbl+` t `+where).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	wantA := order(`SELECT id::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY occurred_at, id`, a.String())
	wantB := order(`SELECT id::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY occurred_at, id`, b.String())
	wantP := order(`SELECT id::text FROM pandokos.platform_audit_event ORDER BY occurred_at, id`)
	beforeTenant, beforePlatform := snap("audit_event", ""), snap("platform_audit_event", "")

	d.Migrate(t)

	read := func(scope string, sql string, args ...any) ([]auditchain.Row, []string) {
		t.Helper()
		rows, err := super.Query(ctx, sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []auditchain.Row
		var ids []string
		for rows.Next() {
			r := auditchain.Row{Scope: scope}
			var detail string
			if err := rows.Scan(&r.Seq, &r.ID, &r.OccurredAt, &r.ActorType, &r.ActorID, &r.ActorName, &r.KeyID, &r.Action, &r.ResourceType, &r.ResourceID,
				&r.Outcome, &r.RequestID, &r.Reason, &r.SupportActionID, &detail, &r.PrevHash, &r.Hash); err != nil {
				t.Fatal(err)
			}
			r.Detail = []byte(detail)
			out = append(out, r)
			ids = append(ids, r.ID)
		}
		return out, ids
	}
	verify := func(scope string, rows []auditchain.Row, want []string, ids []string, headSQL string, args ...any) {
		t.Helper()
		if fmt.Sprint(ids) != fmt.Sprint(want) {
			t.Errorf("%s: numbered in the order %v, want (occurred_at, id): %v", scope, ids, want)
		}
		var head auditchain.Head
		if err := super.QueryRow(ctx, headSQL, args...).Scan(&head.Seq, &head.Hash, &head.AnchorSeq, &head.AnchorHash); err != nil {
			t.Fatalf("%s: the head: %v", scope, err)
		}
		last := rows[len(rows)-1]
		if head.Seq != last.Seq || !bytes.Equal(head.Hash, last.Hash) || head.AnchorSeq != 1 || !bytes.Equal(head.AnchorHash, make([]byte, 32)) {
			t.Errorf("%s: the head %+v does not equal the last row %d %x", scope, head, last.Seq, last.Hash)
		}
		res := auditchain.Verify(func(yield func(auditchain.Row, error) bool) {
			for _, r := range rows {
				if !yield(r, nil) {
					return
				}
			}
		}, &head, nil)
		if len(res.Breaks) != 0 || res.Rows != int64(len(want)) || res.LinkUnverified {
			t.Errorf("%s: Verify: %+v", scope, res)
		}
	}
	const tenantCols = `seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, COALESCE(support_action_id::text, ''), detail::text, prev_hash, hash`
	for scope, c := range map[string]struct {
		id   string
		want []string
	}{"tenant:" + a.String(): {a.String(), wantA}, "tenant:" + b.String(): {b.String(), wantB}} {
		rows, ids := read(scope, `SELECT `+tenantCols+` FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY seq`, c.id)
		verify(scope, rows, c.want, ids, `SELECT seq, hash, anchor_seq, anchor_hash FROM pandokos.audit_chain WHERE tenant_id = $1::uuid`, c.id)
	}
	rows, ids := read("platform", `SELECT seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, '', detail::text, prev_hash, hash FROM pandokos.platform_audit_event ORDER BY seq`)
	verify("platform", rows, wantP, ids, `SELECT seq, hash, anchor_seq, anchor_hash FROM pandokos.platform_audit_chain WHERE id = 1`)

	if got := snap("audit_event", ""); got != beforeTenant {
		t.Errorf("audit_event changed by the migration besides the chain columns:\n%s\n%s", beforeTenant, got)
	}
	if got := snap("platform_audit_event", ""); got != beforePlatform {
		t.Errorf("platform_audit_event changed by the migration besides the chain columns:\n%s\n%s", beforePlatform, got)
	}

	// The migration lifts row-level security and the append-only triggers while it numbers the rows
	// and puts them back.
	for _, tbl := range []string{"audit_event", "platform_audit_event", "audit_chain", "platform_audit_chain"} {
		var forced, enabled bool
		if err := super.QueryRow(ctx, `SELECT relforcerowsecurity, relrowsecurity FROM pg_class WHERE oid = ('pandokos.' || $1)::regclass`, tbl).Scan(&forced, &enabled); err != nil || !forced || !enabled {
			t.Errorf("%s: forced %v enabled %v (%v), want both", tbl, forced, enabled, err)
		}
	}
	for _, trg := range []string{"audit_event_append_only", "platform_audit_event_append_only", "audit_event_seal", "platform_audit_event_seal"} {
		var state string
		if err := super.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger WHERE tgname = $1 AND NOT tgisinternal`, trg).Scan(&state); err != nil || state != "O" {
			t.Errorf("trigger %s: state %q (%v), want O (enabled)", trg, state, err)
		}
	}
	// The next row continues the chain.
	exec(`INSERT INTO pandokos.audit_event (tenant_id, actor_type, action, resource_type, outcome) VALUES ($1::uuid, 'host', 'x.y', 'test', 'success')`, a.String())
	var seq int64
	if err := super.QueryRow(ctx, `SELECT max(seq) FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, a.String()).Scan(&seq); err != nil || seq != int64(len(wantA))+1 {
		t.Errorf("the first row after the migration has seq %d (%v), want %d", seq, err, len(wantA)+1)
	}
}
