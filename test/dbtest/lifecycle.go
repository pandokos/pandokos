package dbtest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/testutil"
)

// The helpers of sprint 6.3: the key version every migrated test database has active (as the
// database of a running `serve` has), and the means to put a tenant through a deletion.

// KEKFile returns the path of the key-encryption key file of the database: made on the first call,
// the same on every later call, so that a process or a provider built on it finds the key that
// sealed the data. Migrate activates the version of this key.
func (d *Database) KEKFile(tb testing.TB) string {
	tb.Helper()
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if d.s.kekFile == "" {
		d.s.kekFile = testutil.WriteKEK(d.s.root)
	}
	return d.s.kekFile
}

// KEKVersion returns the version of the database's key (ADR 0033: "local-" and the first 8 bytes,
// in hexadecimal, of SHA-256("pandokos kek id v1" 0x00 key)). vaulttest checks that the vault
// derives the same.
func (d *Database) KEKVersion(tb testing.TB) string {
	tb.Helper()
	b, err := os.ReadFile(d.KEKFile(tb))
	if err != nil {
		tb.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != 32 {
		tb.Fatal("the test key file is not a key")
	}
	h := sha256.New()
	h.Write([]byte("pandokos kek id v1"))
	h.Write([]byte{0})
	h.Write(raw)
	return "local-" + hex.EncodeToString(h.Sum(nil)[:8])
}

// activateKEK makes the version of the database's key the active one when none is, as the admin
// role in platform scope (the statement serve calls). A database that already has an active
// version (an upgrade test migrated it) is left as it is.
func (d *Database) activateKEK(tb testing.TB) {
	tb.Helper()
	if d.Count(tb, `SELECT count(*)::int FROM pandokos.kek_version WHERE state = 'active'`) > 0 {
		return
	}
	version := d.KEKVersion(tb)
	ctx, cancel := ctx30()
	defer cancel()
	err := d.admin(tb).Platform(ctx, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).ActivateKEKVersion(c, queries.ActivateKEKVersionParams{ID: version, Provider: "local"})
		return err
	})
	if err != nil {
		tb.Fatalf("activate the test key version: %v", err)
	}
}

// ActivateKEK activates a version (provider "local") as the admin role in platform scope and
// returns what the database answered ("activated" or "unchanged").
func (d *Database) ActivateKEK(tb testing.TB, version string) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var res string
	err := d.admin(tb).Platform(ctx, func(c context.Context, tx store.Tx) error {
		var err error
		res, err = queries.New(tx).ActivateKEKVersion(c, queries.ActivateKEKVersionParams{ID: version, Provider: "local"})
		return err
	})
	if err != nil {
		tb.Fatalf("activate %s: %v", version, err)
	}
	return res
}

func (d *Database) superExec(tb testing.TB, what, sql string, args ...any) {
	tb.Helper()
	d.seedExec(tb, what, sql, args...)
}

// PopulatedTenant inserts an active tenant with a row in every table of section 5.3 of the design
// of sprint 6.3 (the foreign-key cases included: a rule scoped to a key and one scoped to a
// mapping, a started price version, an entry of a platform issuer, a user the tenant manages with
// account, membership and session, and a member the platform manages, with its identity, and, of
// sprint 6.4, an event destination with a dead letter and the events of its ledger records and
// limit event), and returns its ID. It can be
// called again on the same database: every name is new. The tenant's ledger holds two records,
// its audit log two sealed rows.
func (d *Database) PopulatedTenant(tb testing.TB) tenant.ID {
	tb.Helper()
	sfx := seedSuffix(tb)
	tid := d.SeedTenant(tb, "pop-"+sfx)
	id := tid.String()

	// Catalogue entries (global, shared by every populated tenant of the database).
	d.superExec(tb, "populate: provider", `INSERT INTO pandokos.provider (name, api_family) VALUES ('pop-provider', 'openai') ON CONFLICT (name) DO NOTHING`)
	d.superExec(tb, "populate: model", `INSERT INTO pandokos.model (provider_id, name, upstream_id, availability)
		SELECT id, 'pop-model', 'pop-model', 'all' FROM pandokos.provider WHERE name = 'pop-provider' ON CONFLICT DO NOTHING`)

	// Access (group A).
	sa := d.SeedServiceAccount(tb, tid, "sa-"+sfx)
	keyID, keyRow := d.SeedVirtualKey(tb, tid, sa)
	_ = keyRow
	d.superExec(tb, "populate: role assignment", `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, 'inference')`, id, sa)
	ti := d.SeedTrustedIssuer(tb, tid, "ti-"+sfx)
	sm := d.SeedSubjectMapping(tb, tid, ti, false, sa, "sub-"+sfx)
	d.SeedPlatformIssuer(tb, "pi-"+sfx, tid)
	d.SeedLimitRule(tb, tid, "tenant", "", "requests_per_minute", 100)
	d.SeedLimitRule(tb, tid, "key", keyID, "requests_per_minute", 10)
	d.SeedLimitRule(tb, tid, "key", sm, "requests_per_minute", 11)
	d.superExec(tb, "populate: idempotency key", `INSERT INTO pandokos.idempotency_key (tenant_id, principal_kind, principal_id, key, operation, salt, request_hash, locked_until, expires_at)
		VALUES ($1::uuid, 'service_account', $2, 'idem-' || $3, 'createServiceAccount', decode(repeat('00', 16), 'hex'), decode(repeat('00', 32), 'hex'), now(), now() + interval '1 day')`, id, sa, sfx)
	d.SupportAction(tb, tid, "platform", "pkey"+sfx, "populate", time.Hour, "member:read")
	managed := d.TenantUser(tb, tid, "managed-"+sfx, []string{"tenant-admin"}).ID
	tokenHash := sha256.Sum256([]byte("session-" + sfx))
	d.Session(tb, managed, "local", tokenHash[:], time.Hour)
	// A member the platform manages: a user with no tenant of its own, with a membership here.
	pm := d.FederatedUser(tb, "https://idp-"+sfx+".example.test", "subject-"+sfx, nil)
	d.Member(tb, tid, pm.ID, []string{"tenant-viewer"})
	d.CustomRole(tb, tid, "role-"+sfx, "member:read")
	// Secrets (group B).
	d.superExec(tb, "populate: data key", `INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key)
		SELECT $1::uuid, v.id, decode(repeat('00', 61), 'hex') FROM pandokos.kek_version v WHERE v.state = 'active'`, id)
	d.superExec(tb, "populate: credential", `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint)
		VALUES ($1::uuid, 'openai', 'c-' || $2, 1, decode(repeat('00', 61), 'hex'), md5($2))`, id, sfx)

	// Configuration (group C).
	d.SeedPlatformLimit(tb, &tid, "requests_per_minute", 500)
	d.superExec(tb, "populate: model grant", `INSERT INTO pandokos.model_tenant_grant (model_id, tenant_id) SELECT m.id, $1::uuid FROM pandokos.model m WHERE m.name = 'pop-model'`, id)
	d.superExec(tb, "populate: model restriction", `INSERT INTO pandokos.tenant_model_restriction (tenant_id, models) VALUES ($1::uuid, ARRAY['pop-provider/pop-model'])`, id)
	d.superExec(tb, "populate: metadata policy", `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, ARRAY['team'])`, id)
	d.SeedTenantPriceStarted(tb, tid, "EUR", "pop-provider", "pop-model", 24*time.Hour)
	d.superExec(tb, "populate: limit event", `INSERT INTO pandokos.limit_event (tenant_id, kind, rule_id, rule_level, subject_kind, metric, period_start, period_end, percent, limit_value, observed)
		VALUES ($1::uuid, 'threshold_reached', pandokos.uuid_v7(), 'tenant', 'tenant', 'tokens_per_day', date_trunc('day', now()), date_trunc('day', now()) + interval '1 day', 80, 10, 8)`, id)
	d.superExec(tb, "populate: rollup", `INSERT INTO pandokos.usage_rollup (tenant_id, hour, principal_kind, principal_id, key_id, provider, model, model_id,
			currency, price_source, outcome, credential, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			reasoning_tokens, estimated_input_tokens, estimated_output_tokens, cost, cost_known, cost_unknown)
		VALUES ($1::uuid, date_bin('1 hour', now(), '2000-01-01 00:00:00+00'), '', '', '', '', '', '', '', '', '', true, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`, id)
	d.superExec(tb, "populate: rollup state", `INSERT INTO pandokos.usage_rollup_state (tenant_id, watermark) VALUES ($1::uuid, now())`, id)

	// Kept records: the ledger and the log.
	now := time.Now().UTC()
	d.SeedUsage(tb, tid,
		UsageRow{StartedAt: now.Add(-2 * time.Hour), PrincipalID: sa, KeyID: keyID, Provider: "openai", InputTokens: 10, OutputTokens: 5},
		UsageRow{StartedAt: now.Add(-time.Hour), PrincipalID: sa, KeyID: keyID, Provider: "openai", InputTokens: 7, OutputTokens: 3})
	// An event destination, made after the events of the ledger and of the limit event: it starts at
	// the head of the log and has no work (the claim of the relay leaves it alone). Its sealed secret
	// is a placeholder, as the credential's: no test of a deletion opens it. A dead letter of it; the
	// directory row is the trigger's.
	d.superExec(tb, "populate: event destination", `INSERT INTO pandokos.event_destination (tenant_id, name, url, secret, secret_key_version)
		VALUES ($1::uuid, 'hook-' || $2, 'https://hooks.example.test/in', decode(repeat('00', 61), 'hex'), 1)`, id, sfx)
	d.superExec(tb, "populate: dead letter", `INSERT INTO pandokos.outbox_dead_letter (tenant_id, destination_id, seq, event_id, type, attempts, reason, status)
		SELECT $1::uuid, d.id, 1, pandokos.uuid_v7(), 'io.pandokos.usage.recorded.v1', 10, 'status', 500
		  FROM pandokos.event_destination d WHERE d.tenant_id = $1::uuid`, id)
	d.SealedRow(tb, tid, now.Add(-2*time.Hour), "populate.first")
	d.SealedRow(tb, tid, now.Add(-time.Hour), "populate.second")
	return tid
}

// TableDigests returns, for every table of the schema pandokos (partitions excluded), the md5 of
// the rows in a fixed order, each as row_to_json: the rows of the tenant for a table with a tenant
// column, the whole table for the others. It is read as the superuser. A test takes it before and
// after an operation and compares the tables that must not change.
func (d *Database) TableDigests(tb testing.TB, id tenant.ID) map[string]string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	super := d.Super(tb)
	rows, err := super.Query(ctx, `SELECT c.relname, EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a
			WHERE a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped AND a.attnum > 0)
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY 1`)
	if err != nil {
		tb.Fatal(err)
	}
	type tab struct {
		name   string
		scoped bool
	}
	var tabs []tab
	for rows.Next() {
		var t tab
		if err := rows.Scan(&t.name, &t.scoped); err != nil {
			tb.Fatal(err)
		}
		tabs = append(tabs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	out := map[string]string{}
	for _, t := range tabs {
		q := fmt.Sprintf(`SELECT COALESCE(md5(string_agg(row_to_json(x)::text, E'\n' ORDER BY row_to_json(x)::text)), '') FROM pandokos.%s x`, t.name)
		var args []any
		if t.scoped {
			q += ` WHERE x.tenant_id = $1::uuid`
			args = append(args, id.String())
		}
		var dg string
		if err := super.QueryRow(ctx, q, args...).Scan(&dg); err != nil {
			tb.Fatalf("digest of %s: %v", t.name, err)
		}
		out[t.name] = dg
	}
	return out
}

// RequestDeletion records a request for the deletion of the tenant, as the product does: the
// tenant is suspended when it is active, and the row of tenant_deletion is inserted with
// not_before as given (the owner writes both; the insert goes through the guard trigger).
func (d *Database) RequestDeletion(tb testing.TB, id tenant.ID, notBefore time.Time) {
	tb.Helper()
	d.superExec(tb, "suspend for the deletion", `UPDATE pandokos.tenant SET state = 'suspended' WHERE id = $1::uuid AND state = 'active'`, id.String())
	d.superExec(tb, "request a deletion", `INSERT INTO pandokos.tenant_deletion (tenant_id, reason, requested_by_type, requested_by_id, requested_by_name, request_id, not_before)
		VALUES ($1::uuid, 'test', 'platform', 'pkeytest', 'test', 'req-del-' || $2, $3)`, id.String(), seedSuffix(tb), notBefore.UTC())
}

// AgeDeletion moves not_before, and settle_at when it is set, to now, so that the next step runs
// (no test waits two minutes). Called before the first step it moves not_before only; the step
// "begin" sets settle_at, and a second call after it moves that.
func (d *Database) AgeDeletion(tb testing.TB, id tenant.ID) {
	tb.Helper()
	// The tests poll with this helper: it uses the one connection the database keeps, not a new one per call.
	ctx, cancel := ctx30()
	defer cancel()
	if _, err := d.SharedSuper(tb).Exec(ctx, `UPDATE pandokos.tenant_deletion SET not_before = least(not_before, now()),
		settle_at = CASE WHEN settle_at IS NULL THEN NULL ELSE least(settle_at, now()) END WHERE tenant_id = $1::uuid`, id.String()); err != nil {
		tb.Fatalf("age the deletion: %v", err)
	}
}

// UseKEK makes the database look like one that was started with a key of that version: the versions
// the harness registered are removed (no data key may refer to one yet) and the version is activated
// as the admin role does. For a test that starts the binary with a key of its own.
func (d *Database) UseKEK(tb testing.TB, version string) {
	tb.Helper()
	d.superExec(tb, "remove the key versions", `DELETE FROM pandokos.kek_version`)
	if got := d.ActivateKEK(tb, version); got != "activated" {
		tb.Fatalf("UseKEK: the database answered %q", got)
	}
}

// Copy makes a second database with the content of this one (CREATE DATABASE ... TEMPLATE), served
// by the same server to the same four logins, removed at cleanup: the backup of a test of erasure.
// The connections to the source are closed first (a template cannot be copied while it is in use), so
// no process should use the source at that moment, and the connections the helpers keep are not
// usable afterwards.
func (d *Database) Copy(tb testing.TB) *Database {
	tb.Helper()
	name := "pdk_t_" + randHex(tb, 4)
	ctx, cancel := ctx30()
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, d.s.admin)
	if err != nil {
		tb.Fatalf("cannot reach the test database: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	d.s.mu.Lock()
	d.s.helper = nil // closed by the termination below
	kek := d.s.kekFile
	d.s.mu.Unlock()
	for _, q := range []string{
		"SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = " + quote(d.Name) + " AND pid <> pg_catalog.pg_backend_pid()",
		"CREATE DATABASE " + ident(name) + " TEMPLATE " + ident(d.Name) + " OWNER pandokos_owner",
		"REVOKE ALL ON DATABASE " + ident(name) + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + ident(name) + " TO pandokos_gateway, pandokos_admin, pandokos_worker",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			tb.Fatalf("copy the database: %s: %v", q, err)
		}
	}
	tb.Cleanup(func() {
		c, cancel := ctx30()
		defer cancel()
		a, err := pgx.ConnectConfig(c, d.s.admin)
		if err != nil {
			return
		}
		defer func() { _ = a.Close(c) }()
		if _, err := a.Exec(c, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)"); err != nil {
			tb.Logf("cleanup: drop the copy: %v", err)
		}
	})
	// The copy has its own helpers (their connections are to its database) and the logins and the key of the source.
	return &Database{Name: name, Host: d.Host, Port: d.Port, s: &shared{admin: d.s.admin, users: d.s.users, passwords: d.s.passwords, root: d.s.root, kekFile: kek}}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (d *Database) worker(tb testing.TB) *store.DB {
	tb.Helper()
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if d.s.workerDB == nil {
		d.s.workerDB = d.Open(d.s.root, store.RoleWorker)
	}
	return d.s.workerDB
}

// StepDeletion makes one call of tenant_erasure_step(batch) as the worker role in the session of the
// tenant, as the deletion job does, and returns the answer.
func (d *Database) StepDeletion(tb testing.TB, id tenant.ID, batch int) string {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	var next string
	err := d.worker(tb).WithTenant(ctx, id, func(c context.Context, tx store.Tx) error {
		var err error
		next, err = queries.New(tx).TenantErasureStep(c, int32(batch)) // #nosec G115 -- a small number of a test
		return err
	})
	if err != nil {
		tb.Fatalf("tenant_erasure_step: %v", err)
	}
	return next
}

// BeginDeletion requests the deletion of the tenant (RequestDeletion, due at once) and runs the first
// step: the tenant is deleting, its access, its secrets and its configuration are still there.
func (d *Database) BeginDeletion(tb testing.TB, id tenant.ID) {
	tb.Helper()
	d.RequestDeletion(tb, id, time.Now().Add(-time.Minute))
	if got := d.StepDeletion(tb, id, 100); got != "access" {
		tb.Fatalf("the first step answered %q, want access", got)
	}
}

// CompleteDeletion requests the deletion of the tenant (unless one is requested) and runs every step to
// the end, passing the settling time by ageing it (AgeDeletion).
func (d *Database) CompleteDeletion(tb testing.TB, id tenant.ID) {
	tb.Helper()
	if d.Count(tb, `SELECT count(*)::int FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, id.String()) == 0 {
		d.RequestDeletion(tb, id, time.Now().Add(-time.Minute))
	}
	d.AgeDeletion(tb, id)
	for i := 0; i < 200; i++ {
		switch d.StepDeletion(tb, id, 100) {
		case "done":
			return
		case "settling", "scheduled":
			d.AgeDeletion(tb, id)
		}
	}
	tb.Fatal("the deletion did not end")
}

// SuperExecReplica runs one statement as the superuser with triggers and rules off
// (session_replication_role = replica, for that transaction only): the state that a transaction that
// began before a deletion, or a fault, can leave and that no role of the product could write.
func (d *Database) SuperExecReplica(tb testing.TB, what, sql string, args ...any) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	err := pgx.BeginFunc(ctx, d.Super(tb), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
	if err != nil {
		tb.Fatalf("%s: %v", what, err)
	}
}
