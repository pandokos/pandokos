package isolation

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// chainOf reads a chain as the owner: the rows of a tenant's log ("tenant:<id>") or of the
// platform's ("platform"), oldest first, and its head.
func chainOf(t *testing.T, super *pgx.Conn, scope string) ([]auditchain.Row, auditchain.Head) {
	t.Helper()
	var sql string
	var args []any
	var headSQL string
	if scope == "platform" {
		sql = `SELECT seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, '', detail::text, prev_hash, hash
			FROM pandokos.platform_audit_event ORDER BY seq`
		headSQL = `SELECT seq, hash, anchor_seq, anchor_hash FROM pandokos.platform_audit_chain WHERE id = 1`
	} else {
		tid := strings.TrimPrefix(scope, "tenant:")
		sql = `SELECT seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, COALESCE(support_action_id::text, ''), detail::text, prev_hash, hash
			FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY seq`
		args = []any{tid}
		headSQL = `SELECT seq, hash, anchor_seq, anchor_hash FROM pandokos.audit_chain WHERE tenant_id = $1::uuid`
	}
	rows, err := super.Query(ctx(t), sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditchain.Row
	for rows.Next() {
		r := auditchain.Row{Scope: scope}
		var detail string
		if err := rows.Scan(&r.Seq, &r.ID, &r.OccurredAt, &r.ActorType, &r.ActorID, &r.ActorName, &r.KeyID, &r.Action, &r.ResourceType, &r.ResourceID,
			&r.Outcome, &r.RequestID, &r.Reason, &r.SupportActionID, &detail, &r.PrevHash, &r.Hash); err != nil {
			t.Fatal(err)
		}
		r.Detail = []byte(detail)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var head auditchain.Head
	if err := super.QueryRow(ctx(t), headSQL, args...).Scan(&head.Seq, &head.Hash, &head.AnchorSeq, &head.AnchorHash); errors.Is(err, pgx.ErrNoRows) {
		// the head of a chain is made by its first row
		head = auditchain.Head{Hash: make([]byte, 32), AnchorSeq: 1, AnchorHash: make([]byte, 32)}
	} else if err != nil {
		t.Fatalf("the head of %s: %v", scope, err)
	}
	return out, head
}

// requireChain asserts the seal of section 5.6 on a chain read by chainOf: numbers from the anchor
// without gap, each prev_hash the hash before it, each hash the one auditchain.Hash computes, the
// head the last row, and the chain as a whole verified by auditchain.Verify.
func requireChain(t *testing.T, scope string, rows []auditchain.Row, head auditchain.Head) {
	t.Helper()
	if len(rows) == 0 {
		if head.Seq != head.AnchorSeq-1 {
			t.Errorf("%s: no row, and the head is %d with the anchor at %d", scope, head.Seq, head.AnchorSeq)
		}
		return
	}
	prev := head.AnchorHash
	for i, r := range rows {
		if want := head.AnchorSeq + int64(i); r.Seq != want {
			t.Errorf("%s: row %d has seq %d, want %d", scope, i, r.Seq, want)
			return
		}
		if !bytes.Equal(r.PrevHash, prev) {
			t.Errorf("%s: seq %d has prev_hash %x, want %x", scope, r.Seq, r.PrevHash, prev)
		}
		h, err := auditchain.Hash(r)
		if err != nil {
			t.Fatalf("%s: seq %d: %v", scope, r.Seq, err)
		}
		if !bytes.Equal(r.Hash, h[:]) {
			t.Errorf("%s: seq %d has hash %x, auditchain.Hash gives %x", scope, r.Seq, r.Hash, h)
		}
		prev = r.Hash
	}
	last := rows[len(rows)-1]
	if head.Seq != last.Seq || !bytes.Equal(head.Hash, last.Hash) {
		t.Errorf("%s: the head is %d %x, the last row %d %x", scope, head.Seq, head.Hash, last.Seq, last.Hash)
	}
	res := auditchain.Verify(func(yield func(auditchain.Row, error) bool) {
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}, &head, nil)
	if len(res.Breaks) != 0 || res.Rows != int64(len(rows)) {
		t.Errorf("%s: Verify: %+v", scope, res)
	}
}

func tenantScope(id tenant.ID) string { return "tenant:" + id.String() }

// TestAuditChainSeal is T-933: every writer of each log gives a row that the chain numbers and
// seals; a runtime role cannot write the chain columns or touch a head.
func TestAuditChainSeal(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "seal-a"), d.SeedTenant(t, "seal-b")
	super := d.Super(t)
	admin, worker, gateway := d.Open(t, store.RoleAdmin), d.Open(t, store.RoleWorker), d.Open(t, store.RoleGateway)
	c := ctx(t)
	ev := func(tid tenant.ID, action string) queries.InsertAuditEventParams {
		return queries.InsertAuditEventParams{TenantID: tid.String(), ActorType: "platform", ActorName: "seal", Action: action, ResourceType: "test",
			ResourceID: "r", Outcome: "success", RequestID: strings.Repeat("a", 32), Reason: "ünï", Detail: `{"n": 1.50, "k": ["é"]}`}
	}
	pev := func(action string) queries.InsertPlatformAuditEventParams {
		return queries.InsertPlatformAuditEventParams{ActorType: "platform", ActorName: "seal", Action: action, ResourceType: "test",
			ResourceID: "r", Outcome: "success", RequestID: strings.Repeat("b", 32), Detail: `{"n": 2.50}`}
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	// The writers of the tenant's log.
	must("InsertAuditEvent in a tenant session", admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
		return queries.New(tx).InsertAuditEvent(c, ev(a, "seal.tenant_session"))
	}))
	must("InsertAuditEvent under ActInTenant", admin.ActInTenant(c, a, func(c context.Context, tx store.Tx) error {
		return queries.New(tx).InsertAuditEvent(c, ev(a, "seal.act_in_tenant"))
	}))
	must("InsertAuditEvent inside Provision", admin.Provision(c, func(c context.Context, tx store.Tx, bind func(context.Context, tenant.ID) error) error {
		if err := bind(c, a); err != nil {
			return err
		}
		return queries.New(tx).InsertAuditEvent(c, ev(a, "seal.provision"))
	}))
	must("InsertAuditEvent for the other tenant", admin.WithTenant(c, b, func(c context.Context, tx store.Tx) error {
		return queries.New(tx).InsertAuditEvent(c, ev(b, "seal.other_tenant"))
	}))
	must("record_auth_failure", gateway.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
		return queries.New(tx).RecordAuthFailure(c, queries.RecordAuthFailureParams{KeyID: "tabcdefghijklmnop", Reason: "revoked", RequestID: strings.Repeat("c", 32)})
	}))
	must("record_token_auth_failure", gateway.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
		_, err := tx.Exec(c, `SELECT pandokos.record_token_auth_failure('tenant', pandokos.uuid_v7(), 'bad_signature', 'subject-1', $1)`, strings.Repeat("d", 32))
		return err
	}))
	var first, second bool
	must("record_unlimited_traffic", gateway.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
		day := time.Now().UTC().Format("2006-01-02")
		if err := tx.QueryRow(c, `SELECT pandokos.record_unlimited_traffic($1::date, 'UTC', 'openai', 'gpt-4o', $2)`, day, strings.Repeat("e", 32)).Scan(&first); err != nil {
			return err
		}
		return tx.QueryRow(c, `SELECT pandokos.record_unlimited_traffic($1::date, 'UTC', 'openai', 'gpt-4o', $2)`, day, strings.Repeat("e", 32)).Scan(&second)
	}))
	if !first || second {
		t.Errorf("record_unlimited_traffic answered %v then %v, want true then false", first, second)
	}
	if _, err := super.Exec(c, `INSERT INTO pandokos.audit_event (tenant_id, actor_type, actor_name, action, resource_type, outcome) VALUES ($1::uuid, 'host', 'owner', 'seal.owner', 'test', 'success')`, a.String()); err != nil {
		t.Fatalf("the owner without a session: %v", err)
	}

	// The writers of the platform's log.
	must("InsertPlatformAuditEvent as admin", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		return queries.New(tx).InsertPlatformAuditEvent(c, pev("seal.platform_admin"))
	}))
	must("InsertPlatformAuditEvent as worker", worker.Platform(c, func(c context.Context, tx store.Tx) error {
		return queries.New(tx).InsertPlatformAuditEvent(c, pev("seal.platform_worker"))
	}))
	if _, err := super.Exec(c, `INSERT INTO pandokos.platform_audit_event (actor_type, actor_name, action, resource_type, outcome) VALUES ('host', 'owner', 'seal.owner', 'test', 'success')`); err != nil {
		t.Fatalf("the owner, platform log: %v", err)
	}

	for _, scope := range []string{tenantScope(a), tenantScope(b), "platform"} {
		rows, head := chainOf(t, super, scope)
		requireChain(t, scope, rows, head)
		if head.AnchorSeq != 1 || !bytes.Equal(head.AnchorHash, make([]byte, 32)) {
			t.Errorf("%s: the anchor is %d %x before any retention", scope, head.AnchorSeq, head.AnchorHash)
		}
		for _, r := range rows {
			if skew := time.Since(r.OccurredAt); skew < 0 || skew > time.Minute || r.ID == "" {
				t.Errorf("%s: seq %d has id %q and occurred_at %v: both are the database's", scope, r.Seq, r.ID, r.OccurredAt)
			}
		}
	}
	rows, _ := chainOf(t, super, tenantScope(a))
	var got []string
	for _, r := range rows {
		got = append(got, r.Action)
	}
	want := []string{"seal.tenant_session", "seal.act_in_tenant", "seal.provision", "auth.failure", "auth.failure", "limit.unlimited_traffic", "seal.owner"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the rows of tenant A: %v, want %v", got, want)
	}
	if rows, head := chainOf(t, super, tenantScope(b)); len(rows) != 1 || head.Seq != 1 {
		t.Errorf("tenant B holds %d rows, head %d: another tenant's writes leaked in", len(rows), head.Seq)
	}
	prows, _ := chainOf(t, super, "platform")
	n := 0
	for _, r := range prows {
		if r.Action == "limit.unlimited_traffic" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("record_unlimited_traffic left %d rows in the platform log, want 1", n)
	}

	t.Run("a runtime role cannot write the chain columns nor touch a head", func(t *testing.T) {
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, c := range []struct {
				name string
				sess session
				sql  string
			}{
				{"seq", sessions(a)[0], `INSERT INTO pandokos.audit_event (tenant_id, seq, actor_type, action, resource_type, outcome) VALUES ($1::uuid, 99, 'platform', 'x.y', 'test', 'success')`},
				{"prev_hash", sessions(a)[0], `INSERT INTO pandokos.audit_event (tenant_id, prev_hash, actor_type, action, resource_type, outcome) VALUES ($1::uuid, decode(repeat('00', 32), 'hex'), 'platform', 'x.y', 'test', 'success')`},
				{"hash", sessions(a)[0], `INSERT INTO pandokos.audit_event (tenant_id, hash, actor_type, action, resource_type, outcome) VALUES ($1::uuid, decode(repeat('00', 32), 'hex'), 'platform', 'x.y', 'test', 'success')`},
				{"platform seq", sessions(a)[3], `INSERT INTO pandokos.platform_audit_event (seq, actor_type, action, resource_type, outcome) VALUES (99, 'platform', 'x.y', 'test', 'success')`},
				{"head update", sessions(a)[0], `UPDATE pandokos.audit_chain SET seq = 0 WHERE tenant_id = $1::uuid`},
				{"head delete", sessions(a)[0], `DELETE FROM pandokos.audit_chain WHERE tenant_id = $1::uuid`},
				{"head insert", sessions(a)[0], `INSERT INTO pandokos.audit_chain (tenant_id) VALUES ($1::uuid)`},
				{"platform head update", sessions(a)[3], `UPDATE pandokos.platform_audit_chain SET seq = 0`},
				{"platform head delete", sessions(a)[3], `DELETE FROM pandokos.platform_audit_chain`},
				{"platform head truncate", sessions(a)[3], `TRUNCATE pandokos.platform_audit_chain`},
				{"head truncate", sessions(a)[0], `TRUNCATE pandokos.audit_chain`},
			} {
				args := []any{}
				if strings.Contains(c.sql, "$1") {
					args = append(args, a.String())
				}
				_, err := execIn(t, conn, c.sess, c.sql, args...)
				if sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s: %s: %v, want SQLSTATE %s", role, c.name, err, insufficientPrivilege)
				}
			}
		}
	})
}

// TestAuditChainConcurrent is T-934: many writers on one chain commit without a gap or a duplicate,
// with interventions (the tenant's row, then the platform's) and platform rows interleaved.
func TestAuditChainConcurrent(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "concurrent")
	super := d.Super(t)
	admin := d.OpenSized(t, store.RoleAdmin, 16)
	c := ctx(t)
	row := func(i int) queries.InsertAuditEventParams {
		return queries.InsertAuditEventParams{TenantID: a.String(), ActorType: "platform", ActorName: "w", Action: "load.tenant", ResourceType: "test",
			ResourceID: fmt.Sprint(i), Outcome: "success", RequestID: strings.Repeat("a", 32), Detail: "{}"}
	}
	prow := func(i int) queries.InsertPlatformAuditEventParams {
		return queries.InsertPlatformAuditEventParams{ActorType: "platform", ActorName: "w", Action: "load.platform", ResourceType: "test",
			ResourceID: fmt.Sprint(i), Outcome: "success", RequestID: strings.Repeat("b", 32), Detail: "{}"}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				errs <- err
			}
		}()
	}
	for w := 0; w < 8; w++ {
		run(func() error {
			for i := 0; i < 50; i++ {
				if err := admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error { return queries.New(tx).InsertAuditEvent(c, row(w*100+i)) }); err != nil {
					return fmt.Errorf("tenant writer %d: %w", w, err)
				}
			}
			return nil
		})
	}
	for i := 0; i < 20; i++ {
		run(func() error { // an intervention: the tenant's row first, then the platform's, in one transaction
			return admin.ActInTenant(c, a, func(c context.Context, tx store.Tx) error {
				q := queries.New(tx)
				if err := q.InsertAuditEvent(c, row(1000+i)); err != nil {
					return err
				}
				return q.InsertPlatformAuditEvent(c, prow(1000+i))
			})
		})
		run(func() error {
			return admin.Platform(c, func(c context.Context, tx store.Tx) error {
				return queries.New(tx).InsertPlatformAuditEvent(c, prow(2000+i))
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a transaction failed: %v", err)
	}
	for scope, want := range map[string]int{tenantScope(a): 8*50 + 20, "platform": 40} {
		rows, head := chainOf(t, super, scope)
		if len(rows) != want {
			t.Errorf("%s: %d rows, want %d", scope, len(rows), want)
		}
		requireChain(t, scope, rows, head)
	}
}

// TestAuditChainNotInserted is T-935: a row that is not inserted leaves its head where it was.
func TestAuditChainNotInserted(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "not-a"), d.SeedTenant(t, "not-b")
	super := d.Super(t)
	admin, gateway := d.OpenSized(t, store.RoleAdmin, 4), d.OpenSized(t, store.RoleGateway, 4)
	c := ctx(t)
	ev := func(tid tenant.ID, detail string) queries.InsertAuditEventParams {
		return queries.InsertAuditEventParams{TenantID: tid.String(), ActorType: "platform", ActorName: "x", Action: "head.test", ResourceType: "test",
			Outcome: "success", RequestID: strings.Repeat("a", 32), Detail: detail}
	}
	heads := func() string {
		t.Helper()
		_, ha := chainOf(t, super, tenantScope(a))
		_, hb := chainOf(t, super, tenantScope(b))
		_, hp := chainOf(t, super, "platform")
		return fmt.Sprintf("%d %x|%d %x|%d %x", ha.Seq, ha.Hash, hb.Seq, hb.Hash, hp.Seq, hp.Hash)
	}
	if err := admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error { return queries.New(tx).InsertAuditEvent(c, ev(a, "{}")) }); err != nil {
		t.Fatal(err)
	}
	before := heads()

	t.Run("a rolled-back transaction", func(t *testing.T) {
		boom := fmt.Errorf("boom")
		err := admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
			if err := queries.New(tx).InsertAuditEvent(c, ev(a, "{}")); err != nil {
				return err
			}
			return boom
		})
		if err == nil || heads() != before {
			t.Errorf("rollback: %v, heads changed: %v", err, heads() != before)
		}
	})
	t.Run("a row refused by a check", func(t *testing.T) {
		big := `{"x": "` + strings.Repeat("a", 8200) + `"}`
		err := admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error { return queries.New(tx).InsertAuditEvent(c, ev(a, big)) })
		if sqlstate(err) != checkViolation || heads() != before {
			t.Errorf("a detail over 8192 bytes: %v (%s), heads changed: %v", err, sqlstate(err), heads() != before)
		}
	})
	t.Run("a row refused by the policy", func(t *testing.T) {
		err := admin.WithTenant(c, a, func(c context.Context, tx store.Tx) error { return queries.New(tx).InsertAuditEvent(c, ev(b, "{}")) })
		if sqlstate(err) != insufficientPrivilege || heads() != before {
			t.Errorf("another tenant's ID: %v (%s), heads changed: %v", err, sqlstate(err), heads() != before)
		}
	})
	t.Run("a second record_unlimited_traffic of the day", func(t *testing.T) {
		day := time.Now().UTC().Format("2006-01-02")
		call := func(req string) (bool, error) {
			var written bool
			err := gateway.WithTenant(c, a, func(c context.Context, tx store.Tx) error {
				return tx.QueryRow(c, `SELECT pandokos.record_unlimited_traffic($1::date, 'UTC', 'openai', 'gpt-4o', $2)`, day, req).Scan(&written)
			})
			return written, err
		}
		if w, err := call(strings.Repeat("1", 32)); err != nil || !w {
			t.Fatalf("the first call: %v %v", w, err)
		}
		after := heads()
		if w, err := call(strings.Repeat("2", 32)); err != nil || w {
			t.Errorf("the second call: %v %v, want false", w, err)
		}
		if heads() != after {
			t.Error("the second record_unlimited_traffic moved a head")
		}
	})
	t.Run("two concurrent first calls of a day", func(t *testing.T) {
		other := d.SeedTenant(t, "not-c")
		day := time.Now().UTC().Format("2006-01-02")
		results := make(chan bool, 2)
		errs := make(chan error, 2)
		start := make(chan struct{})
		for i := 0; i < 2; i++ {
			go func(i int) {
				<-start
				var written bool
				err := gateway.WithTenant(c, other, func(c context.Context, tx store.Tx) error {
					return tx.QueryRow(c, `SELECT pandokos.record_unlimited_traffic($1::date, 'UTC', 'openai', 'gpt-4o', $2)`, day, strings.Repeat(fmt.Sprint(i+3), 32)).Scan(&written)
				})
				results <- written
				errs <- err
			}(i)
		}
		close(start)
		wrote := 0
		for i := 0; i < 2; i++ {
			if <-results {
				wrote++
			}
			if err := <-errs; err != nil {
				t.Errorf("a concurrent call failed: %v", err)
			}
		}
		if wrote != 1 {
			t.Errorf("%d calls say they wrote the row, want 1", wrote)
		}
		var n int
		if err := super.QueryRow(c, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'limit.unlimited_traffic'`, other.String()).Scan(&n); err != nil || n != 1 {
			t.Errorf("%d rows in the tenant's log (%v), want 1", n, err)
		}
		rows, head := chainOf(t, super, tenantScope(other))
		requireChain(t, tenantScope(other), rows, head)
		prows, phead := chainOf(t, super, "platform")
		requireChain(t, "platform", prows, phead)
		m := 0
		for _, r := range prows {
			if r.Action == "limit.unlimited_traffic" && r.ResourceID == other.String() {
				m++
			}
		}
		if m != 1 {
			t.Errorf("%d rows of the tenant in the platform log, want 1", m)
		}
	})
}

// TestAuditReadIsolation is T-942: who reads the logs and the heads, in which session.
func TestAuditReadIsolation(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "read-a"), d.SeedTenant(t, "read-b")
	for _, tid := range []tenant.ID{a, a, b} {
		d.SealedRow(t, tid, time.Now(), "read.test")
	}
	d.SealedPlatformRow(t, time.Now(), "read.test")
	admin := d.Connect(t, "admin")
	count := func(sess session, sql string, args ...any) (int, error) {
		var n int
		err := inSession(t, admin, sess, func(c context.Context, tx pgx.Tx) error { return tx.QueryRow(c, sql, args...).Scan(&n) })
		return n, err
	}
	sessA := sessions(a)[0]
	if n, err := count(sessA, `SELECT count(*) FROM pandokos.audit_event`); err != nil || n != 2 {
		t.Errorf("tenant A reads %d rows (%v), want its 2", n, err)
	}
	if n, err := count(sessA, `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id <> $1::uuid`, a.String()); err != nil || n != 0 {
		t.Errorf("tenant A reads %d rows of another tenant (%v)", n, err)
	}
	if n, err := count(sessA, `SELECT count(*) FROM pandokos.audit_chain`); err != nil || n != 1 {
		t.Errorf("tenant A reads %d heads (%v), want its own", n, err)
	}
	if n, err := count(sessA, `SELECT count(*) FROM pandokos.audit_chain WHERE tenant_id = $1::uuid`, b.String()); err != nil || n != 0 {
		t.Errorf("tenant A reads B's head (%d, %v)", n, err)
	}
	for _, sess := range []session{sessions(a)[1], sessions(a)[2], sessions(a)[3]} {
		for _, tbl := range []string{"audit_event", "audit_chain"} {
			if n, err := count(sess, `SELECT count(*) FROM pandokos.`+tbl); err != nil || n != 0 {
				t.Errorf("session %s reads %d rows of %s (%v): no session without a tenant sees them", sess.name, n, tbl, err)
			}
		}
	}
	for _, tbl := range []string{"platform_audit_event", "platform_audit_chain"} {
		if n, err := count(sessions(a)[3], `SELECT count(*) FROM pandokos.`+tbl); err != nil || n < 1 {
			t.Errorf("platform scope reads %d rows of %s (%v)", n, tbl, err)
		}
		for _, sess := range sessions(a)[:3] {
			if n, err := count(sess, `SELECT count(*) FROM pandokos.`+tbl); err != nil || n != 0 {
				t.Errorf("session %s reads %d rows of %s (%v): only the platform scope does", sess.name, n, tbl, err)
			}
		}
	}
	for _, role := range []string{"gateway", "worker"} {
		conn := d.Connect(t, role)
		for _, tbl := range []string{"audit_event", "platform_audit_event", "audit_chain", "platform_audit_chain"} {
			for _, sess := range sessions(a) {
				if _, err := count2(t, conn, sess, `SELECT count(*) FROM pandokos.`+tbl); sqlstate(err) != insufficientPrivilege {
					t.Errorf("the %s role reads %s in session %s: %v, want SQLSTATE %s", role, tbl, sess.name, err, insufficientPrivilege)
				}
			}
		}
	}
	// The canonical functions are not for the runtime roles.
	super := d.Super(t)
	for _, role := range runtimeRoles {
		for _, fn := range []string{"audit_field(text)", "audit_canonical(jsonb)", "audit_seal_tenant()", "audit_seal_platform()"} {
			var ok bool
			if err := super.QueryRow(ctx(t), `SELECT has_function_privilege($1, $2, 'EXECUTE')`, d.User(role), "pandokos."+fn).Scan(&ok); err != nil || ok {
				t.Errorf("the %s role may execute %s (%v)", role, fn, err)
			}
		}
		var ok bool
		if err := super.QueryRow(ctx(t), `SELECT has_function_privilege($1, 'pandokos.audit_row_hash(text, bigint, uuid, timestamptz, text, text, text, text, text, text, text, text, text, text, uuid, jsonb, bytea)', 'EXECUTE')`, d.User(role)).Scan(&ok); err != nil || ok {
			t.Errorf("the %s role may execute audit_row_hash (%v)", role, err)
		}
	}
}

func count2(t *testing.T, conn *pgx.Conn, s session, sql string) (int, error) {
	t.Helper()
	var n int
	err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error { return tx.QueryRow(c, sql).Scan(&n) })
	return n, err
}

// TestAppendOnlyUnderRetention is T-961: the setting that lets the retention functions delete gives
// a runtime role nothing, and the owner is refused without it.
func TestAppendOnlyUnderRetention(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "append-only")
	super := d.Super(t)
	d.SealedRow(t, a, time.Now(), "ao.test")
	d.SealedPlatformRow(t, time.Now(), "ao.test")
	d.SeedUsage(t, a, dbtest.UsageRow{StartedAt: time.Now()})
	// A row in each of the other two tables: a row trigger has nothing to stop in an empty one.
	if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type) VALUES (now(), 'embedded', 'genai-prices-v2', 'success', 'host')`); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx(t), DefaultSpec().Fixtures["limit_event"].Insert, a.String()); err != nil {
		t.Fatal(err)
	}
	tables := []string{"audit_event", "platform_audit_event", "price_import", "usage_record", "limit_event"}
	for _, role := range runtimeRoles {
		conn := d.Connect(t, role)
		for _, sess := range sessions(a) {
			for _, tbl := range tables {
				for op, sql := range map[string]string{
					"DELETE":   `DELETE FROM pandokos.` + tbl,
					"UPDATE":   `UPDATE pandokos.` + tbl + ` SET request_id = 'x'`,
					"TRUNCATE": `TRUNCATE pandokos.` + tbl,
				} {
					if op == "UPDATE" && tbl == "price_import" {
						sql = `UPDATE pandokos.price_import SET source = 'x'`
					}
					if op == "UPDATE" && tbl == "limit_event" {
						sql = `UPDATE pandokos.limit_event SET percent = 1`
					}
					err := inSession(t, conn, sess, func(c context.Context, tx pgx.Tx) error {
						if _, err := tx.Exec(c, `SELECT set_config('pandokos.retention', 'on', true)`); err != nil {
							return err
						}
						_, err := tx.Exec(c, sql)
						return err
					})
					if sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s %s on %s as %s in session %s with the retention setting: %v, want SQLSTATE %s", op, "", tbl, role, sess.name, err, insufficientPrivilege)
					}
				}
			}
		}
	}
	// The owner without the setting is stopped by the trigger of each table.
	for _, tbl := range tables {
		for op, sql := range map[string]string{"DELETE": `DELETE FROM pandokos.` + tbl, "TRUNCATE": `TRUNCATE pandokos.` + tbl} {
			_, err := super.Exec(ctx(t), sql)
			// price_import is referenced by a foreign key: TRUNCATE is refused before its trigger runs.
			if st := sqlstate(err); st != restrictViolation && (op != "TRUNCATE" || tbl != "price_import" || st != "0A000") {
				t.Errorf("%s on %s without the setting: %v, want SQLSTATE %s", op, tbl, err, restrictViolation)
			}
		}
	}
	// TRUNCATE stays refused for the owner even with the setting; UPDATE too.
	for _, tbl := range tables {
		tx, err := super.Begin(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx(t), `SELECT set_config('pandokos.retention', 'on', true)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx(t), `TRUNCATE pandokos.`+tbl); err == nil || (sqlstate(err) != restrictViolation && sqlstate(err) != "0A000") {
			t.Errorf("TRUNCATE on %s with the setting: %v, want SQLSTATE %s", tbl, err, restrictViolation)
		}
		_ = tx.Rollback(ctx(t))
	}
	findings, err := Lint(ctx(t), super, DefaultSpec())
	if err != nil || len(findings) != 0 {
		t.Errorf("the lint (L-17 among its rules): %v %v", findings, err)
	}
}

// TestAuditRetention is T-962: tenant_retention and platform_retention remove the prefix of a chain
// before the cut, move the anchor, say so in the log, and leave a chain that verifies.
func TestAuditRetention(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "retain-a"), d.SeedTenant(t, "retain-b")
	super := d.Super(t)
	now := time.Now().UTC()
	month := func(back int) time.Time { // the middle of a month, back months before this one
		return time.Date(now.Year(), now.Month()-time.Month(back), 15, 12, 0, 0, 0, time.UTC)
	}
	worker := d.Connect(t, "worker")
	platform := sessions(a)[3]

	seal := func(platformLog bool) {
		for _, back := range []int{24, 18, 14, 20, 1} { // seq 4 is later than seq 3 and earlier than the cut: it goes with them
			if platformLog {
				d.SealedPlatformRow(t, month(back), "old.row")
			} else {
				d.SealedRow(t, a, month(back), "old.row")
			}
		}
	}
	seal(false)
	seal(true)
	d.SealedRow(t, b, month(30), "other.tenant")
	rowsBefore, _ := chainOf(t, super, tenantScope(a))
	if len(rowsBefore) != 5 {
		t.Fatalf("the helper wrote %d rows, want 5", len(rowsBefore))
	}
	prows, _ := chainOf(t, super, "platform")
	firstPlatform := int64(len(prows)) - 5 // rows the platform log held before the five of this test

	type result struct{ usage, rollup, audit int64 }
	runTenant := func(months int) (result, error) {
		var r result
		err := inSession(t, worker, sessions(a)[0], func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT usage_rows, rollup_rows, audit_rows FROM pandokos.tenant_retention(0, $1)`, months).Scan(&r.usage, &r.rollup, &r.audit)
		})
		return r, err
	}
	if r, err := runTenant(12); err != nil || r.audit != 4 {
		t.Fatalf("tenant_retention(0, 12): %+v %v, want 4 rows removed (the prefix up to seq 4)", r, err)
	}
	rows, head := chainOf(t, super, tenantScope(a))
	if head.AnchorSeq != 5 || !bytes.Equal(head.AnchorHash, rowsBefore[3].Hash) {
		t.Errorf("the anchor is %d %x, want 5 and the hash of seq 4 (%x)", head.AnchorSeq, head.AnchorHash, rowsBefore[3].Hash)
	}
	if len(rows) != 2 || rows[0].Seq != 5 || rows[1].Action != "audit.retention" {
		t.Fatalf("the log holds %d rows: %+v", len(rows), rows)
	}
	var detail map[string]any
	if err := super.QueryRow(ctx(t), `SELECT detail FROM pandokos.audit_event WHERE tenant_id = $1::uuid AND action = 'audit.retention'`, a.String()).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if detail["deleted"] != float64(4) || detail["first_seq"] != float64(1) || detail["last_seq"] != float64(4) || detail["last_hash"] != hex.EncodeToString(rowsBefore[3].Hash) {
		t.Errorf("audit.retention detail: %v", detail)
	}
	requireChain(t, tenantScope(a), rows, head)
	if rows, _ := chainOf(t, super, tenantScope(b)); len(rows) != 1 {
		t.Errorf("tenant B holds %d rows, want its own, untouched", len(rows))
	}
	again, err := runTenant(12)
	if err != nil || again.audit != 0 {
		t.Errorf("a second run: %+v %v, want nothing removed", again, err)
	}
	if rows2, _ := chainOf(t, super, tenantScope(a)); len(rows2) != 2 {
		t.Errorf("a second run changed the log: %d rows", len(rows2))
	}
	if _, err := runTenant(1201); sqlstate(err) != checkViolation {
		t.Errorf("tenant_retention(0, 1201): %v, want SQLSTATE %s", err, checkViolation)
	}

	// The platform's log, the same through platform_retention.
	var removed int64
	err = inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT audit_rows FROM pandokos.platform_retention(0, 12)`).Scan(&removed)
	})
	if err != nil || removed != firstPlatform+4 {
		t.Fatalf("platform_retention(0, 12): removed %d (%v), want %d: the four rows of the test and the older ones", removed, err, firstPlatform+4)
	}
	prows2, phead := chainOf(t, super, "platform")
	requireChain(t, "platform", prows2, phead)
	if prows2[len(prows2)-1].Action != "audit.retention" || phead.AnchorSeq != firstPlatform+5 {
		t.Errorf("the platform log ends with %s and the anchor is %d", prows2[len(prows2)-1].Action, phead.AnchorSeq)
	}
	n := len(prows2)
	err = inSession(t, worker, platform, func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `SELECT audit_rows FROM pandokos.platform_retention(0, 12)`).Scan(&removed)
	})
	if prows3, _ := chainOf(t, super, "platform"); err != nil || removed != 0 || len(prows3) != n {
		t.Errorf("a second run of platform_retention: removed %d, %d rows (was %d), %v", removed, len(prows3), n, err)
	}
}
