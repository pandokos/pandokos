// Package migrate is the schema migrator of Pandokos: it loads numbered SQL files, applies the
// pending ones each in its own transaction as the schema owner, and compares the ledger of the
// database with the migrations of the binary (ADR 0026).
package migrate

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// Schema is the PostgreSQL schema that holds every Pandokos object.
	Schema = "pandokos"
	// OwnerRole is the privilege role that owns every object.
	OwnerRole = "pandokos_owner"
	// LockKey is the advisory lock that serializes migrators: the bytes of "pandokos" as a
	// big-endian integer.
	LockKey int64 = 8097874982633959283
)

// RuntimeRoles are the privilege roles of the three process roles.
var RuntimeRoles = []string{"pandokos_gateway", "pandokos_admin", "pandokos_worker"}

// Migration is one embedded SQL file.
type Migration struct {
	Version  int    // 1, 2, 3, ... without gaps
	Name     string // "init" for 0001_init.sql
	SQL      string // the file content
	Checksum string // lower-case hexadecimal SHA-256 of the file bytes
}

// ID returns the file name without extension: "0001_init".
func (m Migration) ID() string { return pad4(m.Version) + "_" + m.Name }

func pad4(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// Querier is the part of *pgx.Conn, pgx.Tx and *pgxpool.Pool that the migrator needs.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Applied is one row of the migration ledger.
type Applied struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// Status compares the ledger with the migrations of the binary.
type Status struct {
	Applied []Applied   // ledger rows, by version; empty when the schema does not exist
	Pending []Migration // migrations of the binary that are not in the ledger
	Version int         // highest applied version, 0 when none
	Want    int         // highest version of the binary
}

// Result is what one call of Apply did.
type Result struct {
	Applied []Migration // what this call applied, in order
	Version int         // schema version after the call
}

// The sentinel errors of the migrator. They are wrapped with the detail of sprint 1.2, section 5.4.
var (
	ErrNoSchema       = errors.New("the database has no Pandokos schema")
	ErrSchemaOlder    = errors.New("the database schema is older than this binary")
	ErrSchemaNewer    = errors.New("the database schema is newer than this binary")
	ErrChecksum       = errors.New("a migration recorded in the database differs from this binary")
	ErrLedger         = errors.New("the migration ledger is inconsistent")
	ErrRoleMissing    = errors.New("a Pandokos database role does not exist")
	ErrRolePrivileged = errors.New("a Pandokos database role has privileges it must not have")
	ErrNotOwner       = errors.New("the database user cannot act as pandokos_owner")
	ErrServerVersion  = errors.New("PostgreSQL 16 or later is required")
)
