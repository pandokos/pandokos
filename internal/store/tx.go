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
)

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
	return db.inTx(ctx, settingTenant, id.String(), fn)
}

// Platform runs fn in one transaction in platform scope, with no tenant: tenant-scoped tables
// show no rows. The gateway role has no platform scope: ErrNoPlatformScope.
func (db *DB) Platform(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	if db.role == RoleGateway {
		return ErrNoPlatformScope
	}
	return db.inTx(ctx, settingScope, scopePlatform, fn)
}

func (db *DB) inTx(ctx context.Context, setting, value string, fn func(ctx context.Context, tx Tx) error) (err error) {
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
	if _, err = t.Exec(ctx, "SELECT pg_catalog.set_config($1, $2, true)", setting, value); err != nil {
		return fmt.Errorf("store: set the transaction context: %w", err)
	}
	if err = fn(ctx, tx{t}); err != nil {
		return err
	}
	if err = t.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
