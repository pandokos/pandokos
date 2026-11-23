package dbtest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/tenant"
)

// SealedRow writes one row of a tenant's audit log with the time given, in the place the chain
// gives it, with the hash the chain gives it: the way a row written in the past would stand. It runs
// as the owner (the superuser of the test database): it disables the seal for its transaction,
// numbers the row from the head, computes its hash with pandokos.audit_row_hash, moves the head, and
// enables the seal again. It returns the number of the row. For tests of retention and of the
// verifier; never for a test of the seal itself.
func (d *Database) SealedRow(tb testing.TB, id tenant.ID, occurredAt time.Time, action string) int64 {
	tb.Helper()
	return d.sealed(tb, id.String(), occurredAt, action)
}

// SealedPlatformRow is SealedRow for the platform's log.
func (d *Database) SealedPlatformRow(tb testing.TB, occurredAt time.Time, action string) int64 {
	tb.Helper()
	return d.sealed(tb, "", occurredAt, action)
}

func (d *Database) sealed(tb testing.TB, tid string, occurredAt time.Time, action string) int64 {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	super := d.Super(tb)
	tx, err := super.Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		tb.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			tb.Fatalf("sealed row: %v\n%s", err, sql)
		}
	}
	var seq int64
	if tid != "" {
		exec(`ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_seal`)
		exec(`INSERT INTO pandokos.audit_chain (tenant_id) VALUES ($1::uuid) ON CONFLICT (tenant_id) DO NOTHING`, tid)
		var head int64
		var prev []byte
		if err := tx.QueryRow(ctx, `SELECT seq, hash FROM pandokos.audit_chain WHERE tenant_id = $1::uuid FOR UPDATE`, tid).Scan(&head, &prev); err != nil {
			tb.Fatal(err)
		}
		seq = head + 1
		exec(`INSERT INTO pandokos.audit_event (tenant_id, id, occurred_at, seq, prev_hash, hash, actor_type, actor_id, actor_name, key_id, action,
				resource_type, resource_id, outcome, request_id, reason, detail)
			SELECT $1::uuid, i.id, $2, $3, $4, pandokos.audit_row_hash('tenant:' || $1::text, $3, i.id, $2, 'platform', '', 'sealed-row', '', $5,
				'test', '', 'success', '', '', NULL, '{}'::jsonb, $4), 'platform', '', 'sealed-row', '', $5, 'test', '', 'success', '', '', '{}'::jsonb
			FROM (SELECT pandokos.uuid_v7() AS id) i`, tid, occurredAt.UTC(), seq, prev, action)
		exec(`UPDATE pandokos.audit_chain c SET seq = e.seq, hash = e.hash FROM pandokos.audit_event e WHERE c.tenant_id = $1::uuid AND e.tenant_id = c.tenant_id AND e.seq = $2`, tid, seq)
		exec(`ALTER TABLE pandokos.audit_event ENABLE TRIGGER audit_event_seal`)
	} else {
		exec(`ALTER TABLE pandokos.platform_audit_event DISABLE TRIGGER platform_audit_event_seal`)
		var head int64
		var prev []byte
		if err := tx.QueryRow(ctx, `SELECT seq, hash FROM pandokos.platform_audit_chain WHERE id = 1 FOR UPDATE`).Scan(&head, &prev); err != nil {
			tb.Fatal(err)
		}
		seq = head + 1
		exec(`INSERT INTO pandokos.platform_audit_event (id, occurred_at, seq, prev_hash, hash, actor_type, actor_id, actor_name, key_id, action,
				resource_type, resource_id, outcome, request_id, reason, detail)
			SELECT i.id, $1, $2, $3, pandokos.audit_row_hash('platform', $2, i.id, $1, 'platform', '', 'sealed-row', '', $4,
				'test', '', 'success', '', '', NULL, '{}'::jsonb, $3), 'platform', '', 'sealed-row', '', $4, 'test', '', 'success', '', '', '{}'::jsonb
			FROM (SELECT pandokos.uuid_v7() AS id) i`, occurredAt.UTC(), seq, prev, action)
		exec(`UPDATE pandokos.platform_audit_chain c SET seq = e.seq, hash = e.hash FROM pandokos.platform_audit_event e WHERE c.id = 1 AND e.seq = $1`, seq)
		exec(`ALTER TABLE pandokos.platform_audit_event ENABLE TRIGGER platform_audit_event_seal`)
	}
	if err := tx.Commit(ctx); err != nil {
		tb.Fatal(err)
	}
	return seq
}

// Tamper runs one statement as the owner with the append-only triggers of the two audit logs
// disabled, the way someone with write access to the tables would change a row: the chain must
// notice. It enables the triggers again before it returns.
func (d *Database) Tamper(tb testing.TB, sql string, args ...any) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	tx, err := d.Super(tb).Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	steps := []struct {
		sql  string
		args []any
	}{
		{`ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_append_only`, nil},
		{`ALTER TABLE pandokos.platform_audit_event DISABLE TRIGGER platform_audit_event_append_only`, nil},
		{sql, args},
		{`ALTER TABLE pandokos.audit_event ENABLE TRIGGER audit_event_append_only`, nil},
		{`ALTER TABLE pandokos.platform_audit_event ENABLE TRIGGER platform_audit_event_append_only`, nil},
	}
	for _, s := range steps {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			tb.Fatalf("tamper: %v\n%s", err, s.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		tb.Fatal(err)
	}
}

// OldPartition makes the monthly partition of the usage ledger that starts at the first day of the
// UTC month of month, and protects it as ensure_usage_partitions does. The partition of a month in
// the past is what retention drops. It returns the name of the partition.
func (d *Database) OldPartition(tb testing.TB, month time.Time) string {
	tb.Helper()
	m := time.Date(month.UTC().Year(), month.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	name := fmt.Sprintf("usage_record_y%04dm%02d", m.Year(), int(m.Month()))
	ctx, cancel := ctx30()
	defer cancel()
	tx, err := d.Super(tb).Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The name is built from two integers: it needs no quoting.
	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE pandokos.%s PARTITION OF pandokos.usage_record FOR VALUES FROM ('%s 00:00:00+00') TO ('%s 00:00:00+00')`,
		name, m.Format("2006-01-02"), m.AddDate(0, 1, 0).Format("2006-01-02"))); err != nil {
		tb.Fatalf("old partition %s: %v", name, err)
	}
	if _, err := tx.Exec(ctx, `SELECT pandokos.protect_usage_partition($1)`, name); err != nil {
		tb.Fatalf("protect %s: %v", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		tb.Fatal(err)
	}
	return name
}

// AuditSnapshot is the two audit logs and their heads at one moment, to be restored by RestoreAudit
// after a test has tampered with them.
type AuditSnapshot struct{ tenant, platform, heads, platformHead string }

// SnapshotAudit reads both logs and both heads as the owner.
func (d *Database) SnapshotAudit(tb testing.TB) AuditSnapshot {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var s AuditSnapshot
	for sql, dst := range map[string]*string{
		`SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.tenant_id, e.seq), '[]')::text FROM pandokos.audit_event e`: &s.tenant,
		`SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.seq), '[]')::text FROM pandokos.platform_audit_event e`:     &s.platform,
		`SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.tenant_id), '[]')::text FROM pandokos.audit_chain c`:        &s.heads,
		`SELECT COALESCE(jsonb_agg(to_jsonb(c)), '[]')::text FROM pandokos.platform_audit_chain c`:                    &s.platformHead,
	} {
		if err := d.Super(tb).QueryRow(ctx, sql).Scan(dst); err != nil {
			tb.Fatal(err)
		}
	}
	return s
}

// RestoreAudit puts the logs and the heads back as the snapshot has them: the owner, with the
// append-only and the seal triggers off for the transaction.
func (d *Database) RestoreAudit(tb testing.TB, s AuditSnapshot) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	tx, err := d.Super(tb).Begin(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_append_only`,
		`ALTER TABLE pandokos.audit_event DISABLE TRIGGER audit_event_seal`,
		`ALTER TABLE pandokos.platform_audit_event DISABLE TRIGGER platform_audit_event_append_only`,
		`ALTER TABLE pandokos.platform_audit_event DISABLE TRIGGER platform_audit_event_seal`,
		`DELETE FROM pandokos.audit_event`,
		`DELETE FROM pandokos.platform_audit_event`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			tb.Fatalf("restore audit: %v\n%s", err, q)
		}
	}
	for _, q := range []struct{ sql, arg string }{
		{`INSERT INTO pandokos.audit_event SELECT * FROM jsonb_populate_recordset(NULL::pandokos.audit_event, $1::jsonb)`, s.tenant},
		{`INSERT INTO pandokos.platform_audit_event SELECT * FROM jsonb_populate_recordset(NULL::pandokos.platform_audit_event, $1::jsonb)`, s.platform},
		{`UPDATE pandokos.audit_chain c SET seq = h.seq, hash = h.hash, anchor_seq = h.anchor_seq, anchor_hash = h.anchor_hash FROM jsonb_populate_recordset(NULL::pandokos.audit_chain, $1::jsonb) h WHERE c.tenant_id = h.tenant_id`, s.heads},
		{`UPDATE pandokos.platform_audit_chain c SET seq = h.seq, hash = h.hash, anchor_seq = h.anchor_seq, anchor_hash = h.anchor_hash FROM jsonb_populate_recordset(NULL::pandokos.platform_audit_chain, $1::jsonb) h`, s.platformHead},
		{`ALTER TABLE pandokos.audit_event ENABLE TRIGGER audit_event_append_only`, ""},
		{`ALTER TABLE pandokos.audit_event ENABLE TRIGGER audit_event_seal`, ""},
		{`ALTER TABLE pandokos.platform_audit_event ENABLE TRIGGER platform_audit_event_append_only`, ""},
		{`ALTER TABLE pandokos.platform_audit_event ENABLE TRIGGER platform_audit_event_seal`, ""},
	} {
		var err error
		if q.arg == "" {
			_, err = tx.Exec(ctx, q.sql)
		} else {
			_, err = tx.Exec(ctx, q.sql, q.arg)
		}
		if err != nil {
			tb.Fatalf("restore audit: %v\n%s", err, q.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		tb.Fatal(err)
	}
}

// VerifyAuditChain reads the audit log of a tenant (scope "tenant:<id>") or of the platform (scope
// "platform") as the owner and verifies it against its head with auditchain.Verify, as the product's
// verifier does. It returns the result and the head.
func (d *Database) VerifyAuditChain(tb testing.TB, scope string) (auditchain.Result, auditchain.Head) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	super := d.Super(tb)
	var rowsSQL, headSQL string
	var args []any
	if scope == "platform" {
		rowsSQL = `SELECT seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, '', detail::text, prev_hash, hash
			FROM pandokos.platform_audit_event ORDER BY seq`
		headSQL = `SELECT seq, hash, anchor_seq, anchor_hash FROM pandokos.platform_audit_chain WHERE id = 1`
	} else {
		args = []any{strings.TrimPrefix(scope, "tenant:")}
		rowsSQL = `SELECT seq, id::text, occurred_at, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, COALESCE(support_action_id::text, ''), detail::text, prev_hash, hash
			FROM pandokos.audit_event WHERE tenant_id = $1::uuid ORDER BY seq`
		headSQL = `SELECT seq, hash, anchor_seq, anchor_hash FROM pandokos.audit_chain WHERE tenant_id = $1::uuid`
	}
	rows, err := super.Query(ctx, rowsSQL, args...)
	if err != nil {
		tb.Fatal(err)
	}
	var chain []auditchain.Row
	for rows.Next() {
		r := auditchain.Row{Scope: scope}
		var detail string
		if err := rows.Scan(&r.Seq, &r.ID, &r.OccurredAt, &r.ActorType, &r.ActorID, &r.ActorName, &r.KeyID, &r.Action, &r.ResourceType, &r.ResourceID,
			&r.Outcome, &r.RequestID, &r.Reason, &r.SupportActionID, &detail, &r.PrevHash, &r.Hash); err != nil {
			tb.Fatal(err)
		}
		r.Detail = []byte(detail)
		chain = append(chain, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	var head auditchain.Head
	if err := super.QueryRow(ctx, headSQL, args...).Scan(&head.Seq, &head.Hash, &head.AnchorSeq, &head.AnchorHash); err != nil {
		tb.Fatalf("the head of %s: %v", scope, err)
	}
	return auditchain.Verify(func(yield func(auditchain.Row, error) bool) {
		for _, r := range chain {
			if !yield(r, nil) {
				return
			}
		}
	}, &head, nil), head
}
