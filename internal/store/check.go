package store

import (
	"context"
	"fmt"

	"github.com/pandokos/pandokos/internal/store/migrate"
)

const userCheck = `SELECT current_user::text,
       r.rolsuper,
       r.rolbypassrls,
       pg_catalog.pg_has_role(current_user, $1::text, 'USAGE'),
       pg_catalog.pg_has_role(current_user, 'pandokos_owner', 'MEMBER'),
       pg_catalog.pg_has_role(current_user, $2::text, 'MEMBER'),
       pg_catalog.pg_has_role(current_user, $3::text, 'MEMBER'),
       (SELECT count(*) FROM pg_catalog.pg_class c
          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'pandokos' AND c.relowner = r.oid)
  FROM pg_catalog.pg_roles r
 WHERE r.rolname = current_user`

// Check connects and verifies the database user and the schema (sprint 1.2, section 5.8). Every
// error is prefixed "database (<role>): ".
func (db *DB) Check(ctx context.Context) error {
	if err := db.check(ctx); err != nil {
		return fmt.Errorf("database (%s): %w", db.role, err)
	}
	return nil
}

func (db *DB) check(ctx context.Context) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("cannot connect: %w", err)
	}
	defer conn.Release()

	var others []string
	for _, r := range migrate.RuntimeRoles {
		if r != db.role.DatabaseRole() {
			others = append(others, r)
		}
	}
	var (
		user                           string
		super, bypass, member, isOwner bool
		inOther1, inOther2             bool
		owned                          int64
	)
	if err := conn.QueryRow(ctx, userCheck, db.role.DatabaseRole(), others[0], others[1]).
		Scan(&user, &super, &bypass, &member, &isOwner, &inOther1, &inOther2, &owned); err != nil {
		return fmt.Errorf("cannot check the database user: %w", err)
	}
	// A superuser is a member of everything, so it is tested first; the first failure wins.
	switch {
	case super:
		return fmt.Errorf("user %q is a superuser; a runtime role must not be", user)
	case bypass:
		return fmt.Errorf("user %q has BYPASSRLS; a runtime role must not", user)
	case !member:
		return fmt.Errorf("user %q is not a member of %s", user, db.role.DatabaseRole())
	case isOwner:
		return fmt.Errorf("user %q is a member of pandokos_owner; a runtime role must not own the schema", user)
	case inOther1:
		return fmt.Errorf("user %q is also a member of %s; each role needs its own database user", user, others[0])
	case inOther2:
		return fmt.Errorf("user %q is also a member of %s; each role needs its own database user", user, others[1])
	case owned > 0:
		return fmt.Errorf("user %q owns objects in schema pandokos; a runtime role must own nothing", user)
	}
	return migrate.Check(ctx, conn, db.migrations)
}

// Ready is the readiness check: the database answers and the schema still matches the binary.
func (db *DB) Ready(ctx context.Context) error {
	var n, highest int64
	if err := db.pool.QueryRow(ctx, "SELECT count(*), COALESCE(max(version), 0) FROM pandokos.schema_migration").Scan(&n, &highest); err != nil {
		return fmt.Errorf("database (%s): %w", db.role, err)
	}
	want := int64(len(db.migrations))
	if n != want || highest != want {
		return fmt.Errorf("database (%s): the schema changed: version %d, this binary needs version %d", db.role, highest, want)
	}
	return nil
}
