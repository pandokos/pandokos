package migrate

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// readLedger reports whether the ledger table exists and returns its rows by version.
func readLedger(ctx context.Context, q Querier) (exists bool, rows []Applied, err error) {
	// A catalogue query and not to_regclass: to_regclass can answer from a stale negative cache
	// entry in a transaction that waited for the lock while another migrator created the table.
	const existsQuery = `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	 WHERE n.nspname = 'pandokos' AND c.relname = 'schema_migration' AND c.relkind = 'r')`
	if err := q.QueryRow(ctx, existsQuery).Scan(&exists); err != nil {
		return false, nil, fmt.Errorf("read the migration ledger: %w", err)
	}
	if !exists {
		return false, nil, nil
	}
	r, err := q.Query(ctx, "SELECT version, name, checksum, applied_at FROM pandokos.schema_migration ORDER BY version")
	if err != nil {
		return false, nil, fmt.Errorf("read the migration ledger: %w", err)
	}
	rows, err = pgx.CollectRows(r, func(row pgx.CollectableRow) (Applied, error) {
		var a Applied
		err := row.Scan(&a.Version, &a.Name, &a.Checksum, &a.AppliedAt)
		return a, err
	})
	if err != nil {
		return false, nil, fmt.Errorf("read the migration ledger: %w", err)
	}
	return true, rows, nil
}

// ReadStatus reads the ledger and compares it with ms. It fails with ErrSchemaNewer,
// ErrChecksum or ErrLedger when the ledger is not a prefix of ms.
func ReadStatus(ctx context.Context, q Querier, ms []Migration) (Status, error) {
	_, applied, err := readLedger(ctx, q)
	if err != nil {
		return Status{}, err
	}
	pending, err := compare(applied, ms)
	if err != nil {
		return Status{}, err
	}
	return Status{Applied: applied, Pending: pending, Version: len(applied), Want: len(ms)}, nil
}

// Check returns nil only when the ledger equals ms exactly: same versions, names, checksums.
// Otherwise ErrNoSchema, ErrSchemaOlder, ErrSchemaNewer, ErrChecksum or ErrLedger, wrapped with
// the detail of sprint 1.2, section 5.8.
func Check(ctx context.Context, q Querier, ms []Migration) error {
	exists, applied, err := readLedger(ctx, q)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w; run \"pandokos migrate\"", ErrNoSchema)
	}
	pending, err := compare(applied, ms)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("%w: schema version %d, this binary needs version %d; run \"pandokos migrate\"", ErrSchemaOlder, len(applied), len(ms))
	}
	return nil
}

// preflight checks the server and the roles before any migration runs.
func preflight(ctx context.Context, q Querier, owner string, runtime []string) error {
	var num int
	var version string
	if err := q.QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version_num')::integer, pg_catalog.current_setting('server_version')").Scan(&num, &version); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	if num < 160000 {
		return fmt.Errorf("%w: the server reports %s", ErrServerVersion, version)
	}

	names := append([]string{owner}, runtime...)
	rows, err := q.Query(ctx, "SELECT r.rolname, r.rolsuper OR r.rolbypassrls FROM pg_catalog.pg_roles r WHERE r.rolname = ANY($1)", names)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	privileged := map[string]bool{}
	for rows.Next() {
		var name string
		var p bool
		if err := rows.Scan(&name, &p); err != nil {
			return fmt.Errorf("preflight: %w", err)
		}
		privileged[name] = p
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	for _, n := range names {
		if _, ok := privileged[n]; !ok {
			return fmt.Errorf("%w: %s; create the roles first (docs/operations/database.md)", ErrRoleMissing, n)
		}
	}
	for _, n := range names {
		if privileged[n] {
			return fmt.Errorf("%w: %s is a superuser or has BYPASSRLS", ErrRolePrivileged, n)
		}
	}

	var user string
	var canAct bool
	if err := q.QueryRow(ctx, "SELECT session_user::text, COALESCE((SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = session_user), false) OR pg_catalog.pg_has_role(session_user, $1::text, 'SET')", owner).Scan(&user, &canAct); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	if !canAct {
		return fmt.Errorf("%w: grant %s to %q", ErrNotOwner, owner, user)
	}
	return nil
}

// Apply applies the pending migrations of ms. pandokosVersion is recorded in the ledger.
func Apply(ctx context.Context, conn *pgx.Conn, ms []Migration, pandokosVersion string) (Result, error) {
	if err := preflight(ctx, conn, OwnerRole, RuntimeRoles); err != nil {
		return Result{}, err
	}
	_, applied, err := readLedger(ctx, conn)
	if err != nil {
		return Result{}, err
	}
	pending, err := compare(applied, ms)
	if err != nil {
		return Result{}, err
	}
	res := Result{Version: len(applied)}
	for _, m := range pending {
		did, err := applyOne(ctx, conn, m, pandokosVersion)
		if err != nil {
			return res, fmt.Errorf("migration %s: %w", m.ID(), err)
		}
		if did {
			res.Applied = append(res.Applied, m)
		}
		res.Version = m.Version
	}
	return res, nil
}

// applyOne applies one migration in one transaction. Nothing in it is session state: the role and
// the timeout are transaction-local and the lock is a transaction-level lock, so that it works
// through a transaction-mode pooler. It reports false when a concurrent migrator already did it.
func applyOne(ctx context.Context, conn *pgx.Conn, m Migration, pandokosVersion string) (bool, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rctx) // an error after Commit is expected and irrelevant
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{OwnerRole}.Sanitize()); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_catalog.pg_advisory_xact_lock($1)", LockKey); err != nil {
		return false, err
	}
	_, rows, err := readLedger(ctx, tx)
	if err != nil {
		return false, err
	}
	if len(rows) >= m.Version {
		if r := rows[m.Version-1]; r.Name != m.Name || r.Checksum != m.Checksum {
			return false, fmt.Errorf("%w: %s", ErrChecksum, m.ID())
		}
		return false, tx.Commit(ctx)
	}
	if len(rows) != m.Version-1 {
		return false, fmt.Errorf("%w: expected version %d, found %d", ErrLedger, m.Version-1, len(rows))
	}
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '10s'"); err != nil {
		return false, err
	}
	// No arguments: the driver uses the simple protocol and the file may hold many statements.
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO pandokos.schema_migration (version, name, checksum, pandokos_version) VALUES ($1, $2, $3, $4)",
		m.Version, m.Name, m.Checksum, pandokosVersion); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
