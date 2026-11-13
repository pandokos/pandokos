package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/migrate"
	"github.com/pandokos/pandokos/internal/store/migrations"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/testutil"
)

var roleNames = []string{"owner", "gateway", "admin", "worker"}

// shared is what a Database and its pooled view have in common.
type shared struct {
	admin     *pgx.ConnConfig // superuser, direct, to the maintenance database
	users     map[string]string
	passwords map[string]string
	extra     []string // logins created by CreateLogin
	mu        sync.Mutex
}

// Database is one test database with its four login users.
type Database struct {
	Name string
	Host string // where the product connects: PostgreSQL, or PgBouncer for a pooled view
	Port int

	s *shared
}

func randHex(tb testing.TB, n int) string {
	tb.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func ctx30() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func ident(parts ...string) string { return pgx.Identifier(parts).Sanitize() }

// New creates an empty database and four login users, and registers their removal.
// It fails or skips the test according to the gate.
func New(tb testing.TB) *Database {
	tb.Helper()
	switch act, msg := gate(EnvDatabaseURL, EnvNoDatabase, os.Getenv(EnvDatabaseURL), os.Getenv(EnvNoDatabase), os.Getenv("CI")); act {
	case skip:
		tb.Skip(msg)
	case fail:
		tb.Fatal(msg)
	}
	cfg, err := pgx.ParseConfig(os.Getenv(EnvDatabaseURL))
	if err != nil {
		tb.Fatal("PANDOKOS_TEST_DATABASE_URL is not a valid PostgreSQL URL")
	}
	ctx, cancel := ctx30()
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		tb.Fatalf("cannot reach the test database: %v", err)
	}
	tb.Cleanup(func() {
		c, cancel := ctx30()
		defer cancel()
		_ = admin.Close(c)
	})

	rolesSQL, err := os.ReadFile(filepath.Join(testutil.RepoRoot(tb), "deploy", "postgres", "roles.sql"))
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := admin.Exec(ctx, string(rolesSQL)); err != nil {
		tb.Fatalf("roles.sql: %v", err)
	}

	name := "pdk_t_" + randHex(tb, 4)
	for _, q := range []string{
		"CREATE DATABASE " + ident(name) + " OWNER pandokos_owner",
		"REVOKE ALL ON DATABASE " + ident(name) + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + ident(name) + " TO pandokos_gateway, pandokos_admin, pandokos_worker",
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			tb.Fatalf("%s: %v", q, err)
		}
	}

	s := &shared{admin: cfg, users: map[string]string{}, passwords: map[string]string{}}
	d := &Database{Name: name, Host: cfg.Host, Port: int(cfg.Port), s: s}
	tb.Cleanup(func() {
		c, cancel := ctx30()
		defer cancel()
		if _, err := admin.Exec(c, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)"); err != nil {
			tb.Logf("cleanup: drop database: %v", err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, u := range s.users {
			if _, err := admin.Exec(c, "DROP ROLE IF EXISTS "+ident(u)); err != nil {
				tb.Logf("cleanup: drop role: %v", err)
			}
		}
		for _, u := range s.extra {
			if _, err := admin.Exec(c, "DROP ROLE IF EXISTS "+ident(u)); err != nil {
				tb.Logf("cleanup: drop role: %v", err)
			}
		}
	})

	for _, r := range roleNames {
		pw := randHex(tb, 16)
		user := name + "_" + r
		var stmt string
		if err := admin.QueryRow(ctx,
			`SELECT format('CREATE ROLE %I LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L IN ROLE %I', $1::text, $2::text, $3::text)`,
			user, pw, "pandokos_"+r).Scan(&stmt); err != nil {
			tb.Fatalf("building the CREATE ROLE statement: %v", err)
		}
		if _, err := admin.Exec(ctx, stmt); err != nil {
			tb.Fatalf("creating the login for %s: %v", r, err)
		}
		s.users[r], s.passwords[r] = user, pw
	}
	return d
}

// NewMigrated is New followed by Migrate.
func NewMigrated(tb testing.TB) *Database {
	tb.Helper()
	d := New(tb)
	d.Migrate(tb)
	return d
}

func embedded(tb testing.TB) []migrate.Migration {
	tb.Helper()
	ms, err := migrations.Load()
	if err != nil {
		tb.Fatal(err)
	}
	return ms
}

// Migrate applies the embedded migrations with the owner user (store.Migrate).
func (d *Database) Migrate(tb testing.TB) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	if _, err := store.Migrate(ctx, d.ConnOptions("owner"), embedded(tb), "test"); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
}

// Pooled returns a view of the same database reached through PgBouncer. It fails or skips the
// test according to the pooler gate.
func (d *Database) Pooled(tb testing.TB) *Database {
	tb.Helper()
	addr := os.Getenv(EnvPoolerAddr)
	switch act, msg := gate(EnvPoolerAddr, EnvNoPooler, addr, os.Getenv(EnvNoPooler), os.Getenv("CI")); act {
	case skip:
		tb.Skip(msg)
	case fail:
		tb.Fatal(msg)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		tb.Fatalf("%s is not host:port", EnvPoolerAddr)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		tb.Fatalf("%s has no valid port", EnvPoolerAddr)
	}
	cp := *d
	cp.Host, cp.Port = host, n
	return &cp
}

// CAFile returns the file with the certificate authority of the test PostgreSQL. It fails or
// skips the test according to the gate (missing variable: fail; opt-out variable outside CI: skip).
func CAFile(tb testing.TB) string {
	tb.Helper()
	f := os.Getenv(EnvDatabaseCAFile)
	switch act, msg := gate(EnvDatabaseCAFile, EnvNoDatabaseTLS, f, os.Getenv(EnvNoDatabaseTLS), os.Getenv("CI")); act {
	case skip:
		tb.Skip(msg)
	case fail:
		tb.Fatal(msg)
	}
	return f
}

// EachMode runs fn in a sub-test "direct" and in a sub-test "pooler", each with its own
// migrated database; the pooler one is reached through PgBouncer.
func EachMode(t *testing.T, fn func(t *testing.T, d *Database)) {
	t.Helper()
	t.Run("direct", func(t *testing.T) {
		fn(t, NewMigrated(t))
	})
	t.Run("pooler", func(t *testing.T) {
		d := New(t)
		p := d.Pooled(t)
		d.Migrate(t)
		fn(t, p)
	})
}

// User returns the login name for "owner", "gateway", "admin" or "worker".
func (d *Database) User(role string) string {
	u, ok := d.s.users[role]
	if !ok {
		panic("dbtest: unknown role " + role)
	}
	return u
}

// Passwords returns the four passwords, for assertions that they never appear in an output.
func (d *Database) Passwords() []string {
	var out []string
	for _, r := range roleNames {
		out = append(out, d.s.passwords[r])
	}
	return out
}

func (d *Database) options(user, password string) store.ConnOptions {
	return store.ConnOptions{
		Host: d.Host, Port: d.Port, Database: d.Name, User: user,
		Password: secret.New(password), TLSMode: "disable", ConnectTimeout: 5 * time.Second,
	}
}

// ConnOptions returns the product's connection options for that user (TLS mode "disable",
// connect timeout 5 s), towards d.Host and d.Port.
func (d *Database) ConnOptions(role string) store.ConnOptions {
	return d.options(d.User(role), d.s.passwords[role])
}

// Open returns a checked store.DB for the role (store.Open, then Check), closed at cleanup.
func (d *Database) Open(tb testing.TB, role store.Role) *store.DB {
	tb.Helper()
	db, err := store.Open(store.Options{
		Conn: d.ConnOptions(string(role)), Role: role, MaxConns: 4, Migrations: embedded(tb),
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(db.Close)
	ctx, cancel := ctx30()
	defer cancel()
	if err := db.Check(ctx); err != nil {
		tb.Fatalf("check: %v", err)
	}
	return db
}

func (d *Database) rawConfig(tb testing.TB, user, password, host string, port int) *pgx.ConnConfig {
	tb.Helper()
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=disable", host, port, d.Name, user))
	if err != nil {
		tb.Fatal(err)
	}
	cfg.Password = password
	cfg.RuntimeParams = map[string]string{"application_name": "pandokos-test"}
	return cfg
}

func (d *Database) connect(tb testing.TB, cfg *pgx.ConnConfig) *pgx.Conn {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		tb.Fatalf("connect as %s: %v", cfg.User, err)
	}
	tb.Cleanup(func() {
		c, cancel := ctx30()
		defer cancel()
		_ = conn.Close(c)
	})
	return conn
}

// Connect returns a raw connection logged in as that user, towards d.Host and d.Port, in
// QueryExecModeExec, closed at cleanup. Tests use it to play code that bypasses the helper.
func (d *Database) Connect(tb testing.TB, role string) *pgx.Conn {
	tb.Helper()
	cfg := d.rawConfig(tb, d.User(role), d.s.passwords[role], d.Host, d.Port)
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.StatementCacheCapacity = 0
	cfg.DescriptionCacheCapacity = 0
	return d.connect(tb, cfg)
}

// Super returns a superuser connection to this database (always direct). It bypasses
// row-level security: tests use it to arrange data and to verify.
func (d *Database) Super(tb testing.TB) *pgx.Conn {
	tb.Helper()
	cfg := d.s.admin.Copy()
	cfg.Database = d.Name
	return d.connect(tb, cfg)
}

// CreateLogin creates one more login user "<database>_<suffix>" with the given attribute
// clause (for example "SUPERUSER" or "") and memberships, removed at cleanup, and returns
// connection options for it.
func (d *Database) CreateLogin(tb testing.TB, suffix, attributes string, memberOf ...string) store.ConnOptions {
	tb.Helper()
	user := d.Name + "_" + suffix
	pw := randHex(tb, 16)
	ctx, cancel := ctx30()
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, d.s.admin)
	if err != nil {
		tb.Fatalf("cannot reach the test database: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var stmt string
	if err := admin.QueryRow(ctx, `SELECT format('CREATE ROLE %I LOGIN '||$3::text||' PASSWORD %L', $1::text, $2::text)`, user, pw, attributes).Scan(&stmt); err != nil {
		tb.Fatalf("building the CREATE ROLE statement: %v", err)
	}
	if _, err := admin.Exec(ctx, stmt); err != nil {
		tb.Fatalf("creating the login %s: %v", suffix, err)
	}
	d.s.mu.Lock()
	d.s.extra = append(d.s.extra, user)
	d.s.mu.Unlock()
	for _, m := range memberOf {
		if _, err := admin.Exec(ctx, "GRANT "+ident(m)+" TO "+ident(user)); err != nil {
			tb.Fatalf("granting %s: %v", m, err)
		}
	}
	d.s.mu.Lock()
	d.s.passwords["extra:"+suffix] = pw
	d.s.mu.Unlock()
	return d.options(user, pw)
}

// SeedTenant inserts a tenant through Super and makes it active.
func (d *Database) SeedTenant(tb testing.TB, name string) tenant.ID {
	tb.Helper()
	conn := d.Super(tb)
	ctx, cancel := ctx30()
	defer cancel()
	var id string
	if err := conn.QueryRow(ctx, "INSERT INTO pandokos.tenant (name) VALUES ($1) RETURNING id::text", name).Scan(&id); err != nil {
		tb.Fatalf("seed tenant: %v", err)
	}
	if _, err := conn.Exec(ctx, "UPDATE pandokos.tenant SET state = 'active' WHERE id = $1::uuid", id); err != nil {
		tb.Fatalf("activate tenant: %v", err)
	}
	tid, err := tenant.ParseID(id)
	if err != nil {
		tb.Fatal(err)
	}
	return tid
}

// SetAcceptingConnections makes the database refuse (and drop) or accept connections.
func (d *Database) SetAcceptingConnections(tb testing.TB, accept bool) {
	tb.Helper()
	ctx, cancel := ctx30()
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, d.s.admin)
	if err != nil {
		tb.Fatalf("cannot reach the test database: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	if accept {
		if _, err := admin.Exec(ctx, "ALTER DATABASE "+ident(d.Name)+" WITH ALLOW_CONNECTIONS true"); err != nil {
			tb.Fatal(err)
		}
		return
	}
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+ident(d.Name)+" WITH ALLOW_CONNECTIONS false"); err != nil {
		tb.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND pid <> pg_catalog.pg_backend_pid()", d.Name); err != nil {
		tb.Fatal(err)
	}
}

// Env returns the PANDOKOS_DATABASE_* variables for a process: address, name, TLS mode
// "disable", the three runtime users with password files, and the owner user with its
// password file. Password files are created with testutil.WriteSecret.
func (d *Database) Env(tb testing.TB) []string {
	tb.Helper()
	env := []string{
		"PANDOKOS_DATABASE_ADDRESS=" + net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		"PANDOKOS_DATABASE_NAME=" + d.Name,
		"PANDOKOS_DATABASE_TLS_MODE=disable",
	}
	for _, r := range []string{"gateway", "admin", "worker", "owner"} {
		up := strings.ToUpper(r)
		env = append(env,
			"PANDOKOS_DATABASE_"+up+"_USER="+d.User(r),
			"PANDOKOS_DATABASE_"+up+"_PASSWORD_FILE="+testutil.WriteSecret(tb, "db_"+r, d.s.passwords[r]),
		)
	}
	return env
}
