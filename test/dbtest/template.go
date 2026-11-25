package dbtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrate"
)

// Running the embedded migrations takes about half a second; a test suite makes more than a thousand
// migrated databases. NewMigrated therefore copies a template database (CREATE DATABASE ... TEMPLATE)
// that holds the result of the migrations, and every test still gets a database of its own.
//
// The template is named after a digest of roles.sql and of the checksums of the embedded migrations,
// so a change of either makes a new one. It is built by the first process that needs it, under an
// advisory lock that the processes of the other packages wait for, and stays in the cluster until
// the cluster is discarded (make db-down). It holds the schema and the data of the migrations and
// nothing else: the key of the database is activated per database, by Migrate, as before.

// templateLockKey is the key of the advisory lock that serializes the building of the template.
const templateLockKey = 7240117

var template struct {
	mu   sync.Mutex
	name string // set once the template exists in the cluster
}

// migratedTemplate returns the name of the template database, building it when it does not exist.
func migratedTemplate(tb testing.TB, cfg *pgx.ConnConfig, rolesSQL string) string {
	tb.Helper()
	template.mu.Lock()
	defer template.mu.Unlock()
	if template.name != "" {
		return template.name
	}
	ms := embedded(tb)
	sum := sha256.New()
	sum.Write([]byte(rolesSQL))
	for _, m := range ms {
		sum.Write([]byte{0})
		sum.Write([]byte(m.ID() + " " + m.Checksum))
	}
	name := "pdk_tpl_" + hex.EncodeToString(sum.Sum(nil))[:16]

	// Building can wait for another process that builds it, then finds it done.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		tb.Fatalf("cannot reach the test database: %v", err)
	}
	defer func() {
		c, cancel := ctx30()
		defer cancel()
		_ = conn.Close(c) // releases the advisory lock
	}()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
		tb.Fatalf("template lock: %v", err)
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		tb.Fatalf("template lookup: %v", err)
	}
	if !exists {
		buildTemplate(ctx, tb, conn, cfg, name, ms)
	}
	template.name = name
	return name
}

// buildTemplate makes the template database under a temporary name and renames it when it is
// complete, so that a template that exists is always whole (a process killed while building leaves
// only the temporary database, which the next build drops).
func buildTemplate(ctx context.Context, tb testing.TB, conn *pgx.Conn, cfg *pgx.ConnConfig, name string, ms []migrate.Migration) {
	tb.Helper()
	build := name + "_build"
	login := build + "_owner"
	pw := randHex(tb, 16)
	for _, q := range []string{
		"DROP DATABASE IF EXISTS " + ident(build) + " WITH (FORCE)",
		"DROP ROLE IF EXISTS " + ident(login),
		"CREATE DATABASE " + ident(build) + " OWNER pandokos_owner",
		"REVOKE ALL ON DATABASE " + ident(build) + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + ident(build) + " TO pandokos_gateway, pandokos_admin, pandokos_worker",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			tb.Fatalf("%s: %v", q, err)
		}
	}
	var stmt string
	if err := conn.QueryRow(ctx,
		`SELECT format('CREATE ROLE %I LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L IN ROLE pandokos_owner', $1::text, $2::text)`,
		login, pw).Scan(&stmt); err != nil {
		tb.Fatalf("building the CREATE ROLE statement: %v", err)
	}
	if _, err := conn.Exec(ctx, stmt); err != nil {
		tb.Fatalf("creating the login of the template: %v", err)
	}
	b := &Database{Name: build, Host: cfg.Host, Port: int(cfg.Port), s: &shared{
		users: map[string]string{"owner": login}, passwords: map[string]string{"owner": pw}, root: tb,
	}}
	if _, err := store.Migrate(ctx, b.ConnOptions("owner"), ms, "test"); err != nil {
		tb.Fatalf("migrating the template: %v", err)
	}
	for _, q := range []string{
		"DROP ROLE " + ident(login),
		"ALTER DATABASE " + ident(build) + " RENAME TO " + ident(name),
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			tb.Fatalf("%s: %v", q, err)
		}
	}
}
