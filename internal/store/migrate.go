package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store/migrate"
)

func connectOwner(ctx context.Context, o ConnOptions) (*pgx.Conn, error) {
	cfg, err := connConfig(o, "pandokos-migrate")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("cannot connect: %w", err)
	}
	return conn, nil
}

func closeConn(ctx context.Context, conn *pgx.Conn) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = conn.Close(c)
}

// Migrate connects as the owner user of o and applies the pending migrations of ms.
func Migrate(ctx context.Context, o ConnOptions, ms []migrate.Migration, pandokosVersion string) (migrate.Result, error) {
	conn, err := connectOwner(ctx, o)
	if err != nil {
		return migrate.Result{}, err
	}
	defer closeConn(ctx, conn)
	return migrate.Apply(ctx, conn, ms, pandokosVersion)
}

// MigrationStatus connects as the owner user of o and reports the state of the schema.
func MigrationStatus(ctx context.Context, o ConnOptions, ms []migrate.Migration) (migrate.Status, error) {
	conn, err := connectOwner(ctx, o)
	if err != nil {
		return migrate.Status{}, err
	}
	defer closeConn(ctx, conn)
	return migrate.ReadStatus(ctx, conn, ms)
}
