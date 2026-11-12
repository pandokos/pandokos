package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store/migrate"
)

// Role is a process role. Each has its own database user and privileges.
type Role string

// The three process roles that connect to the database.
const (
	RoleGateway Role = "gateway"
	RoleAdmin   Role = "admin"
	RoleWorker  Role = "worker"
)

// DatabaseRole returns the privilege role of r: "pandokos_gateway", "pandokos_admin", ...
func (r Role) DatabaseRole() string { return "pandokos_" + string(r) }

// ConnOptions says how to reach the database as one user.
type ConnOptions struct {
	Host           string
	Port           int
	Database       string
	User           string
	Password       secret.Secret
	TLSMode        string // "verify-full" or "disable"
	TLSCAFile      string // optional, PEM
	ConnectTimeout time.Duration
}

// Options configure the pool of one process role.
type Options struct {
	Conn       ConnOptions
	Role       Role
	MaxConns   int
	Migrations []migrate.Migration // what the schema must match; required
}

// DB is the database as seen by one process role. It does not expose its pool: WithTenant,
// Platform and the checks are the only way to run SQL (ADR 0004).
type DB struct {
	pool       *pgxpool.Pool
	role       Role
	migrations []migrate.Migration
}

func validate(o Options) error {
	switch o.Role {
	case RoleGateway, RoleAdmin, RoleWorker:
	default:
		return errors.New("store: unknown role")
	}
	switch {
	case o.Conn.Host == "":
		return errors.New("store: the host must not be empty")
	case o.Conn.Port < 1 || o.Conn.Port > 65535:
		return errors.New("store: the port must be between 1 and 65535")
	case o.Conn.Database == "":
		return errors.New("store: the database name must not be empty")
	case o.Conn.User == "":
		return errors.New("store: the user must not be empty")
	case o.Conn.Password.IsZero():
		return errors.New("store: the password must not be empty")
	case o.Conn.TLSMode != "verify-full" && o.Conn.TLSMode != "disable":
		return errors.New("store: the TLS mode must be verify-full or disable")
	case o.Conn.ConnectTimeout <= 0:
		return errors.New("store: the connect timeout must be greater than zero")
	case o.MaxConns < 1 || o.MaxConns > 1000:
		return errors.New("store: MaxConns must be between 1 and 1000")
	case len(o.Migrations) == 0:
		return errors.New("store: the migrations of the binary are required")
	}
	return nil
}

// Open validates o and creates the pool. It does not connect.
func Open(o Options) (*DB, error) {
	if err := validate(o); err != nil {
		return nil, err
	}
	pc, err := poolConfig(o)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, errors.New("store: cannot create the pool")
	}
	return &DB{pool: pool, role: o.Role, migrations: o.Migrations}, nil
}

// Role returns the process role of the pool.
func (db *DB) Role() Role { return db.role }

// Close closes the pool and waits for connections in use to be released.
func (db *DB) Close() { db.pool.Close() }
