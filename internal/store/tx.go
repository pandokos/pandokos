package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	settingTenant = "app.tenant_id"
	settingScope  = "app.scope"
	scopePlatform = "platform"
)

// Tx is what code running inside a transaction may do. It cannot commit, roll back, or reach
// the connection.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// The errors of the transaction helpers.
var (
	ErrNoTenant        = errors.New("store: tenant context required")
	ErrNoPlatformScope = errors.New("store: this role has no platform scope")
	ErrNotAdmin        = errors.New("store: only the admin role provisions tenants")
	// ErrNoRows is what a :one query returns when there is no row.
	ErrNoRows = pgx.ErrNoRows
)

type setting struct{ name, value string }

type tx struct{ t pgx.Tx }

func (x tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return x.t.Exec(ctx, sql, args...)
}

func (x tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return x.t.Query(ctx, sql, args...)
}

func (x tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return x.t.QueryRow(ctx, sql, args...)
}

// WithTenant runs fn in one transaction whose tenant is id. It commits when fn returns nil and
// rolls back when fn returns an error or panics. A zero id is ErrNoTenant.
func (db *DB) WithTenant(ctx context.Context, id tenant.ID, fn func(ctx context.Context, tx Tx) error) error {
	if id.IsZero() {
		return ErrNoTenant
	}
	return db.inTx(ctx, []setting{{settingTenant, id.String()}}, fn)
}

// Platform runs fn in one transaction in platform scope, with no tenant: tenant-scoped tables
// show no rows. The gateway role has no platform scope: ErrNoPlatformScope.
func (db *DB) Platform(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	if db.role == RoleGateway {
		return ErrNoPlatformScope
	}
	return db.inTx(ctx, []setting{{settingScope, scopePlatform}}, fn)
}

func (db *DB) inTx(ctx context.Context, settings []setting, fn func(ctx context.Context, tx Tx) error) (err error) {
	return db.run(ctx, settings, func(ctx context.Context, t pgx.Tx) error { return fn(ctx, tx{t}) })
}

func (db *DB) run(ctx context.Context, settings []setting, fn func(ctx context.Context, t pgx.Tx) error) (err error) {
	t, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	rollback := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = t.Rollback(rctx)
	}
	defer func() {
		if p := recover(); p != nil {
			rollback()
			panic(p)
		}
		if err != nil {
			rollback()
		}
	}()
	for _, st := range settings {
		if _, err = t.Exec(ctx, "SELECT pg_catalog.set_config($1, $2, true)", st.name, st.value); err != nil {
			return fmt.Errorf("store: set the transaction context: %w", err)
		}
	}
	if err = fn(ctx, t); err != nil {
		return err
	}
	if err = t.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// Unscoped runs fn in one transaction with neither a tenant nor the platform scope: every
// protected table shows no row. It exists for the key lookup function.
func (db *DB) Unscoped(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	return db.inTx(ctx, nil, fn)
}

// Provision runs fn in one transaction in platform scope. fn may call bind once, with the ID of
// the tenant it has just created; from then on the transaction is also that tenant's session.
// Admin role only (ErrNotAdmin). It exists for creating a tenant with its first rows.
func (db *DB) Provision(ctx context.Context, fn func(ctx context.Context, tx Tx, bind func(ctx context.Context, id tenant.ID) error) error) error {
	if db.role != RoleAdmin {
		return ErrNotAdmin
	}
	return db.run(ctx, []setting{{settingScope, scopePlatform}}, func(ctx context.Context, t pgx.Tx) error {
		bound := false
		bind := func(ctx context.Context, id tenant.ID) error {
			if id.IsZero() {
				return ErrNoTenant
			}
			if bound {
				return errors.New("store: bind was already called")
			}
			bound = true
			if _, err := t.Exec(ctx, "SELECT pg_catalog.set_config($1, $2, true)", settingTenant, id.String()); err != nil {
				return fmt.Errorf("store: set the transaction context: %w", err)
			}
			return nil
		}
		return fn(ctx, tx{t}, bind)
	})
}
