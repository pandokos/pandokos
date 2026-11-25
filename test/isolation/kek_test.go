package isolation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestKEKVersionLifecycle is T-985: the states of the versions of the key-encryption key, their
// activation and retirement, and who may do what to them.
func TestKEKVersionLifecycle(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	// An installation that never started: no version at all (dbtest activated its own at migration).
	if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.kek_version`); err != nil {
		t.Fatal(err)
	}
	admin, worker, gateway := d.Connect(t, "admin"), d.Connect(t, "worker"), d.Connect(t, "gateway")
	const v1, v2, v3 = "local-1111111111111111", "local-2222222222222222", "local-3333333333333333"
	activate := func(conn *pgx.Conn, s session, id, provider string) (string, error) {
		var res string
		err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT pandokos.kek_activate($1, $2)`, id, provider).Scan(&res)
		})
		return res, err
	}
	states := func() string {
		return dbText(t, d, `SELECT COALESCE(string_agg(id || '=' || state, ' ' ORDER BY id), '') FROM pandokos.kek_version`)
	}
	audit := func(action string) int64 {
		return dbNumber(t, d, `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = $1`, action)
	}

	// The first version, the same again, a new one.
	activations := audit("kek.activate") // the one dbtest wrote before the versions were removed
	if got, err := activate(admin, platformSession, v1, "local"); err != nil || got != "activated" {
		t.Fatalf("the first version: %q, %v", got, err)
	}
	if got := audit("kek.activate"); got != activations+1 {
		t.Errorf("%d kek.activate rows after the first activation, want %d", got, activations+1)
	}
	if got, err := activate(admin, platformSession, v1, "local"); err != nil || got != "unchanged" {
		t.Errorf("the same version again: %q, %v, want unchanged", got, err)
	}
	if got := audit("kek.activate"); got != activations+1 {
		t.Errorf("the repeated activation wrote an audit row (%d, was %d)", got, activations+1)
	}
	// Data keys of two tenants under v1.
	a, b := d.SeedTenant(t, "kek-a"), d.SeedTenant(t, "kek-b")
	for _, tid := range []string{a.String(), b.String()} {
		if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key) VALUES ($1::uuid, $2, decode(repeat('00', 61), 'hex'))`, tid, v1); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := activate(admin, platformSession, v2, "local"); err != nil || got != "activated" {
		t.Fatalf("a new version: %q, %v", got, err)
	}
	if got := states(); got != v1+"=retiring "+v2+"=active" {
		t.Errorf("the states after the second activation: %s", got)
	}
	if got := dbText(t, d, `SELECT detail ->> 'previous' || '/' || actor_type || '/' || actor_name FROM pandokos.platform_audit_event WHERE action = 'kek.activate' ORDER BY seq DESC LIMIT 1`); got != v1+"/host/serve" {
		t.Errorf("the audit row of the activation says %s, want %s/host/serve", got, v1)
	}

	// What a replaced version, another provider, the wrong scope and the wrong role get.
	if _, err := activate(admin, platformSession, v1, "local"); sqlstate(err) != "55000" || constraintOf(err) != "kek_version_replaced" {
		t.Errorf("a retiring version: %v, want kek_version_replaced", err)
	}
	if _, err := activate(admin, platformSession, v2, "vault-transit"); constraintOf(err) != "kek_version_provider" {
		t.Errorf("another provider's name: %v, want kek_version_provider", err)
	}
	for _, s := range []session{tenantSession(a), {name: "none"}, {"empty", "app.tenant_id", ""}} {
		if _, err := activate(admin, s, v3, "local"); sqlstate(err) != insufficientPrivilege {
			t.Errorf("activation in session %s: %v, want SQLSTATE %s", s.name, err, insufficientPrivilege)
		}
	}
	for role, conn := range map[string]*pgx.Conn{"worker": worker, "gateway": gateway} {
		if _, err := activate(conn, platformSession, v3, "local"); sqlstate(err) != insufficientPrivilege {
			t.Errorf("activation by the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
		}
	}
	if got := states(); got != v1+"=retiring "+v2+"=active" {
		t.Errorf("a refused activation changed the states: %s", got)
	}

	// At most one version is active; the admin role cannot insert a version.
	if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.kek_version (id, provider, state) VALUES ($1, 'local', 'active')`, v3); sqlstate(err) != uniqueViolation {
		t.Errorf("a second active version: %v, want SQLSTATE %s", err, uniqueViolation)
	}
	if err := attemptInsert(t, admin, platformSession, nil, `INSERT INTO pandokos.kek_version (id, provider) VALUES ('local-4444444444444444', 'local')`); sqlstate(err) != insufficientPrivilege {
		t.Errorf("the admin role inserts a version: %v, want SQLSTATE %s", err, insufficientPrivilege)
	}

	// A data key is inserted, or re-wrapped, under the active version only.
	insertKey := func(version string) error {
		_, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 2, $2, decode(repeat('00', 61), 'hex'))`, a.String(), version)
		return err
	}
	if err := insertKey(v1); sqlstate(err) != restrictViolation || constraintOf(err) != "tenant_data_key_kek_active" {
		t.Errorf("a data key under a retiring version: %v, want tenant_data_key_kek_active", err)
	}
	if err := insertKey(v2); err != nil {
		t.Fatalf("a data key under the active version: %v", err)
	}
	if err := insertKey("local-9999999999999999"); sqlstate(err) != restrictViolation {
		t.Errorf("a data key under an unknown version: %v, want SQLSTATE %s", err, restrictViolation)
	}

	// The worker re-wraps in the session of one tenant: two columns, that tenant's rows, nothing else.
	bBefore := d.TableDigests(t, b)["tenant_data_key"]
	err := inSession(t, worker, tenantSession(a), func(c context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(c, `UPDATE pandokos.tenant_data_key SET kek_version = $1, wrapped_key = decode(repeat('11', 61), 'hex') WHERE kek_version = $2`, v2, v1)
		if err != nil || tag.RowsAffected() != 1 {
			t.Errorf("the re-wrap in the tenant's session: %d rows, %v, want 1", tag.RowsAffected(), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.TableDigests(t, b)["tenant_data_key"]; got != bBefore {
		t.Error("a re-wrap in one tenant's session changed the other tenant's data key")
	}
	for _, c := range []struct {
		sql  string
		want string
	}{
		{`UPDATE pandokos.tenant_data_key SET updated_at = now()`, insufficientPrivilege},
		{`UPDATE pandokos.tenant_data_key SET version = 9`, insufficientPrivilege},
		{`UPDATE pandokos.tenant_data_key SET tenant_id = '` + b.String() + `'`, insufficientPrivilege},
		{`INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key) VALUES ('` + a.String() + `', '` + v2 + `', decode(repeat('00', 61), 'hex'))`, insufficientPrivilege},
		{`DELETE FROM pandokos.tenant_data_key`, insufficientPrivilege},
		{`TRUNCATE pandokos.tenant_data_key`, insufficientPrivilege},
	} {
		if err := attemptInsert(t, worker, tenantSession(a), nil, c.sql); sqlstate(err) != c.want {
			t.Errorf("the worker runs %q: %v, want SQLSTATE %s", c.sql, err, c.want)
		}
	}
	// Re-wrapping to a version that is not active is refused by the guard, whoever asks.
	if err := attemptInsert(t, worker, tenantSession(b), nil, `UPDATE pandokos.tenant_data_key SET kek_version = '`+v1+`', wrapped_key = decode(repeat('22', 61), 'hex')`); constraintOf(err) != "tenant_data_key_kek_active" {
		t.Errorf("a re-wrap to a retiring version: %v, want tenant_data_key_kek_active", err)
	}
	// From a platform session the worker sees no data key, and updates none.
	if err := inSession(t, worker, platformSession, func(c context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(c, `UPDATE pandokos.tenant_data_key SET kek_version = $1`, v2)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("the worker's re-wrap in platform scope: %d rows, %v, want none", tag.RowsAffected(), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Retirement: nothing while a data key of any tenant has the version, asked from a session that sees none.
	retire := func(conn *pgx.Conn, s session) (int, error) {
		var n int
		err := inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT pandokos.kek_retire()`).Scan(&n)
		})
		return n, err
	}
	if n, err := retire(worker, platformSession); err != nil || n != 0 {
		t.Fatalf("retire while tenant b has a key under v1: %d, %v", n, err)
	}
	if got := states(); got != v1+"=retiring "+v2+"=active" {
		t.Errorf("the states after a retirement that retired nothing: %s", got)
	}
	for role, conn := range map[string]*pgx.Conn{"admin": admin, "gateway": gateway} {
		if _, err := retire(conn, platformSession); sqlstate(err) != insufficientPrivilege {
			t.Errorf("retirement by the %s role: %v, want SQLSTATE %s", role, err, insufficientPrivilege)
		}
	}
	if _, err := retire(worker, tenantSession(a)); sqlstate(err) != insufficientPrivilege {
		t.Errorf("retirement in a tenant's session: %v, want SQLSTATE %s", err, insufficientPrivilege)
	}

	// An insert in flight under the old version makes the retirement wait, and then keep the version.
	v4 := "local-4444444444444444"
	flying := d.SeedTenant(t, "kek-flying")
	if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid OR tenant_id = $2::uuid`, a.String(), b.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Super(t).Exec(ctx(t), `UPDATE pandokos.kek_version SET state = 'retired', retired_at = now() WHERE id = $1`, v1); err != nil {
		t.Fatal(err)
	}
	inFlight, err := d.Connect(t, "admin").Begin(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inFlight.Exec(ctx(t), `SELECT pg_catalog.set_config('app.tenant_id', $1, true)`, flying.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := inFlight.Exec(ctx(t), `INSERT INTO pandokos.tenant_data_key (tenant_id, kek_version, wrapped_key) VALUES ($1::uuid, $2, decode(repeat('00', 61), 'hex'))`, flying.String(), v2); err != nil {
		t.Fatalf("the insert in flight: %v", err)
	}
	if got, err := activate(admin, platformSession, v4, "local"); err != nil || got != "activated" {
		t.Fatalf("the activation beside the insert in flight: %q, %v", got, err)
	}
	type answer struct {
		n   int
		err error
	}
	done := make(chan answer, 1)
	go func() {
		n, err := retire(d.Connect(t, "worker"), platformSession)
		done <- answer{n, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the retirement did not wait for the insert in flight: %+v", r)
	case <-time.After(500 * time.Millisecond):
	}
	if err := inFlight.Commit(ctx(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.n != 0 {
			t.Errorf("the retirement after the insert committed: %+v, want 0 versions retired", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the retirement did not finish after the insert committed")
	}
	if got := dbText(t, d, `SELECT state FROM pandokos.kek_version WHERE id = $1`, v2); got != "retiring" {
		t.Errorf("v2 is %s after the insert under it committed, want retiring", got)
	}

	// When no data key has the version, it is retired, with its dates kept, and the platform log says so.
	if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, flying.String()); err != nil {
		t.Fatal(err)
	}
	dates := dbText(t, d, `SELECT created_at::text || '|' || activated_at::text FROM pandokos.kek_version WHERE id = $1`, v2)
	if n, err := retire(worker, platformSession); err != nil || n != 1 {
		t.Fatalf("the retirement with no data key: %d, %v, want 1", n, err)
	}
	if got := dbText(t, d, `SELECT state FROM pandokos.kek_version WHERE id = $1`, v2); got != "retired" {
		t.Errorf("v2 is %s, want retired", got)
	}
	if got := dbText(t, d, `SELECT created_at::text || '|' || activated_at::text FROM pandokos.kek_version WHERE id = $1`, v2); got != dates {
		t.Errorf("created_at and activated_at of the retired version are %s, were %s", got, dates)
	}
	if got := dbNumber(t, d, `SELECT count(*) FROM pandokos.kek_version WHERE id = $1 AND retired_at IS NOT NULL`, v2); got != 1 {
		t.Error("the retired version has no retired_at")
	}
	if got := audit("kek.retire"); got != 1 {
		t.Errorf("%d kek.retire rows, want 1", got)
	}
	if got := dbText(t, d, `SELECT actor_type || '/' || resource_id FROM pandokos.platform_audit_event WHERE action = 'kek.retire'`); got != "platform/"+v2 {
		t.Errorf("the retirement row is %s", got)
	}
	if got := states(); got != v1+"=retired "+v2+"=retired "+v4+"=active" {
		t.Errorf("the final states: %s", got)
	}
	// A retired version never comes back.
	if _, err := activate(admin, platformSession, v2, "local"); constraintOf(err) != "kek_version_replaced" {
		t.Errorf("a retired version: %v, want kek_version_replaced", err)
	}
}
