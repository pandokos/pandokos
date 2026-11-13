package store

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store/migrate"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/testcert"
)

// envSummary lists every field of the driver configuration that a libpq environment variable can
// change, as text, so that two configurations can be compared.
func envSummary(pc *pgxpool.Config) string {
	cc := pc.ConnConfig
	tlsSummary := "none"
	if cc.TLSConfig != nil {
		tlsSummary = fmt.Sprintf("min=%x server=%s insecure=%v roots=%v certs=%d", cc.TLSConfig.MinVersion, cc.TLSConfig.ServerName,
			cc.TLSConfig.InsecureSkipVerify, cc.TLSConfig.RootCAs != nil, len(cc.TLSConfig.Certificates))
	}
	return fmt.Sprintf("host=%s port=%d db=%s user=%s pw=%s tls=%s fallbacks=%d params=%v timeout=%v validate=%v after=%v "+
		"sslneg=%q sslcertmode=%q minproto=%q maxproto=%q binding=%q requireauth=%q krb=%q/%q mode=%v stmt=%d desc=%d",
		cc.Host, cc.Port, cc.Database, cc.User, cc.Password, tlsSummary, len(cc.Fallbacks), cc.RuntimeParams, cc.ConnectTimeout,
		cc.ValidateConnect != nil, cc.AfterConnect != nil, cc.SSLNegotiation, cc.SSLCertMode, cc.MinProtocolVersion,
		cc.MaxProtocolVersion, cc.ChannelBinding, cc.RequireAuth, cc.KerberosSrvName, cc.KerberosSpn,
		cc.DefaultQueryExecMode, cc.StatementCacheCapacity, cc.DescriptionCacheCapacity)
}

// hostileEnvironment is every variable that pgx's parseEnvSettings reads (pgconn v5.12.0), with
// a value that would change or break the connection. PGSERVICE and PGSERVICEFILE are separate:
// they are refused, not neutralised.
var hostileEnvironment = map[string]string{
	"PGHOST": "nowhere.invalid", "PGPORT": "1", "PGDATABASE": "nodb", "PGUSER": "nobody", "PGPASSWORD": "wrong",
	"PGPASSFILE": "/nonexistent/pgpass", "PGAPPNAME": "x", "PGCONNECT_TIMEOUT": "1", "PGSSLMODE": "require",
	"PGSSLKEY": "/nonexistent/key", "PGSSLCERT": "/nonexistent/cert", "PGSSLCERTMODE": "require", "PGSSLSNI": "0",
	"PGSSLROOTCERT": "/nonexistent/root", "PGSSLPASSWORD": "wrong", "PGSSLNEGOTIATION": "direct",
	"PGTARGETSESSIONATTRS": "read-only", "PGTZ": "Mars/Base", "PGOPTIONS": "-c statement_timeout=1",
	"PGMINPROTOCOLVERSION": "3.2", "PGMAXPROTOCOLVERSION": "3.0", "PGCHANNELBINDING": "require", "PGREQUIREAUTH": "none",
}

func dummyMigrations() []migrate.Migration {
	return []migrate.Migration{{Version: 1, Name: "init", SQL: "SELECT 1", Checksum: "0000000000000000000000000000000000000000000000000000000000000000"}}
}

func validOptions() Options {
	return Options{
		Conn: ConnOptions{
			Host: "db.example.test", Port: 5432, Database: "pandokos", User: "pdk_gateway",
			Password: secret.New("pw-for-the-test"), TLSMode: "disable", ConnectTimeout: 5 * time.Second,
		},
		Role:       RoleGateway,
		MaxConns:   7,
		Migrations: dummyMigrations(),
	}
}

// TestConnConfig is T-107.
func TestConnConfig(t *testing.T) {
	t.Run("every libpq variable is neutralised", func(t *testing.T) {
		for k := range hostileEnvironment {
			t.Setenv(k, "") // registers the restore of the original value
			os.Unsetenv(k)
		}
		clean, err := poolConfig(validOptions())
		if err != nil {
			t.Fatal(err)
		}
		cleanVerify := validOptions()
		cleanVerify.Conn.TLSMode = "verify-full"
		cleanV, err := poolConfig(cleanVerify)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hostileEnvironment {
			t.Setenv(k, v)
		}
		got, err := poolConfig(validOptions())
		if err != nil {
			t.Fatalf("a libpq variable broke the configuration: %v", err)
		}
		if envSummary(got) != envSummary(clean) {
			t.Errorf("the environment changed the configuration:\n got %s\nwant %s", envSummary(got), envSummary(clean))
		}
		gotV, err := poolConfig(cleanVerify)
		if err != nil {
			t.Fatalf("a libpq variable broke the verify-full configuration: %v", err)
		}
		if envSummary(gotV) != envSummary(cleanV) {
			t.Errorf("the environment changed the verify-full configuration:\n got %s\nwant %s", envSummary(gotV), envSummary(cleanV))
		}
	})
	t.Run("service variables are refused", func(t *testing.T) {
		for _, name := range []string{"PGSERVICE", "PGSERVICEFILE"} {
			t.Run(name, func(t *testing.T) {
				t.Setenv(name, "x")
				db, err := Open(validOptions())
				if err == nil {
					db.Close()
					t.Fatal("Open must fail")
				}
				want := "store: environment variable " + name + " is set; Pandokos does not use libpq service files, unset it"
				if err.Error() != want {
					t.Errorf("error %q, want %q", err.Error(), want)
				}
				if _, err := Migrate(context.Background(), validOptions().Conn, dummyMigrations(), "x"); err == nil || err.Error() != want {
					t.Errorf("Migrate error %v, want %q", err, want)
				}
			})
		}
	})
	t.Run("environment", func(t *testing.T) {
		for k, v := range map[string]string{
			"PGHOST": "nowhere.invalid", "PGPORT": "1", "PGUSER": "nobody", "PGPASSWORD": "wrong",
			"PGDATABASE": "nodb", "PGSSLMODE": "require", "PGAPPNAME": "x",
			"PGOPTIONS": "-c statement_timeout=1", "PGTARGETSESSIONATTRS": "read-only",
		} {
			t.Setenv(k, v)
		}
		o := validOptions()
		pc, err := poolConfig(o)
		if err != nil {
			t.Fatal(err)
		}
		cc := pc.ConnConfig
		if cc.Host != "db.example.test" || cc.Port != 5432 || cc.Database != "pandokos" || cc.User != "pdk_gateway" || cc.Password != "pw-for-the-test" {
			t.Errorf("connection identity: %s %d %s %s", cc.Host, cc.Port, cc.Database, cc.User)
		}
		if want := map[string]string{"application_name": "pandokos-gateway"}; !reflect.DeepEqual(cc.RuntimeParams, want) {
			t.Errorf("RuntimeParams = %v, want %v", cc.RuntimeParams, want)
		}
		if len(cc.Fallbacks) != 0 {
			t.Errorf("fallbacks: %d", len(cc.Fallbacks))
		}
		if cc.ValidateConnect != nil {
			t.Error("a connection validator is set")
		}
		if cc.TLSConfig != nil {
			t.Error("TLS must be off for mode disable")
		}
	})
	t.Run("verify-full", func(t *testing.T) {
		o := validOptions()
		o.Conn.TLSMode = "verify-full"
		o.Conn.TLSCAFile, _, _ = testcert.Files(t, []string{"localhost"}, nil)
		pc, err := poolConfig(o)
		if err != nil {
			t.Fatal(err)
		}
		tc := pc.ConnConfig.TLSConfig
		if tc == nil {
			t.Fatal("no TLS configuration")
		}
		if tc.MinVersion != tls.VersionTLS12 || tc.ServerName != "db.example.test" || tc.RootCAs == nil || tc.InsecureSkipVerify {
			t.Errorf("TLS configuration: min %x, server name %q, roots %v, insecure %v", tc.MinVersion, tc.ServerName, tc.RootCAs != nil, tc.InsecureSkipVerify)
		}
		o.Conn.TLSCAFile = ""
		pc, err = poolConfig(o)
		if err != nil {
			t.Fatal(err)
		}
		if pc.ConnConfig.TLSConfig == nil || pc.ConnConfig.TLSConfig.RootCAs != nil {
			t.Error("without a CA file the system roots must be used (RootCAs nil)")
		}
		o.Conn.TLSCAFile = "/nonexistent/ca.pem"
		if _, err := poolConfig(o); err == nil {
			t.Error("a missing CA file must be an error")
		}
	})
	t.Run("pool", func(t *testing.T) {
		pc, err := poolConfig(validOptions())
		if err != nil {
			t.Fatal(err)
		}
		if pc.MaxConns != 7 || pc.MinConns != 0 || pc.MaxConnLifetime != 30*time.Minute ||
			pc.MaxConnIdleTime != 5*time.Minute || pc.HealthCheckPeriod != 30*time.Second {
			t.Errorf("pool settings: %d %d %v %v %v", pc.MaxConns, pc.MinConns, pc.MaxConnLifetime, pc.MaxConnIdleTime, pc.HealthCheckPeriod)
		}
		cc := pc.ConnConfig
		if cc.DefaultQueryExecMode != pgx.QueryExecModeExec || cc.StatementCacheCapacity != 0 || cc.DescriptionCacheCapacity != 0 {
			t.Errorf("query mode %v, caches %d %d", cc.DefaultQueryExecMode, cc.StatementCacheCapacity, cc.DescriptionCacheCapacity)
		}
	})
	t.Run("application names", func(t *testing.T) {
		for role, want := range map[Role]string{RoleGateway: "pandokos-gateway", RoleAdmin: "pandokos-admin", RoleWorker: "pandokos-worker"} {
			o := validOptions()
			o.Role = role
			pc, err := poolConfig(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := pc.ConnConfig.RuntimeParams["application_name"]; got != want {
				t.Errorf("%s: application_name %q, want %q", role, got, want)
			}
		}
	})
	t.Run("password never in an error", func(t *testing.T) {
		o := validOptions()
		o.Conn.TLSMode = "bogus"
		_, err := poolConfig(o)
		if err == nil {
			t.Fatal("expected an error")
		}
		assertNoPassword(t, err.Error())
	})
}

// TestOpenValidatesOptions is T-108.
func TestOpenValidatesOptions(t *testing.T) {
	rows := []struct {
		name string
		edit func(o *Options)
	}{
		{"unknown role", func(o *Options) { o.Role = "owner" }},
		{"empty role", func(o *Options) { o.Role = "" }},
		{"empty host", func(o *Options) { o.Conn.Host = "" }},
		{"port zero", func(o *Options) { o.Conn.Port = 0 }},
		{"port too large", func(o *Options) { o.Conn.Port = 70000 }},
		{"empty database", func(o *Options) { o.Conn.Database = "" }},
		{"empty user", func(o *Options) { o.Conn.User = "" }},
		{"zero password", func(o *Options) { o.Conn.Password = secret.Secret{} }},
		{"unknown TLS mode", func(o *Options) { o.Conn.TLSMode = "require" }},
		{"zero connect timeout", func(o *Options) { o.Conn.ConnectTimeout = 0 }},
		{"MaxConns zero", func(o *Options) { o.MaxConns = 0 }},
		{"MaxConns too large", func(o *Options) { o.MaxConns = 1001 }},
		{"no migrations", func(o *Options) { o.Migrations = nil }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			o := validOptions()
			r.edit(&o)
			start := time.Now()
			db, err := Open(o)
			if err == nil {
				db.Close()
				t.Fatal("expected an error")
			}
			if time.Since(start) > time.Second {
				t.Error("Open tried to connect")
			}
			assertNoPassword(t, err.Error())
		})
	}
	t.Run("valid options do not connect", func(t *testing.T) {
		o := validOptions()
		o.Conn.Host, o.Conn.Port = "127.0.0.1", 1
		start := time.Now()
		db, err := Open(o)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if db.Role() != RoleGateway || time.Since(start) > time.Second {
			t.Errorf("role %q after %v", db.Role(), time.Since(start))
		}
	})
}

func unreachable(t *testing.T, role Role) *DB {
	t.Helper()
	o := validOptions()
	o.Role = role
	o.Conn.Host, o.Conn.Port = "127.0.0.1", 1
	db, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// TestWithTenantRequiresTenant is T-109.
func TestWithTenantRequiresTenant(t *testing.T) {
	for _, role := range []Role{RoleGateway, RoleAdmin, RoleWorker} {
		db := unreachable(t, role)
		called := false
		start := time.Now()
		err := db.WithTenant(context.Background(), tenant.ID{}, func(context.Context, Tx) error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrNoTenant) || called {
			t.Errorf("%s: error %v, called %v", role, err, called)
		}
		if time.Since(start) > 500*time.Millisecond {
			t.Errorf("%s: WithTenant took %v: it needed a connection", role, time.Since(start))
		}
	}
}

// TestPlatformRefusedForGateway is T-110.
func TestPlatformRefusedForGateway(t *testing.T) {
	db := unreachable(t, RoleGateway)
	called := false
	start := time.Now()
	err := db.Platform(context.Background(), func(context.Context, Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNoPlatformScope) || called {
		t.Fatalf("error %v, called %v", err, called)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("Platform took %v: it needed a connection", time.Since(start))
	}
}

// TestRoleNames is T-111.
func TestRoleNames(t *testing.T) {
	var got []string
	for _, r := range []Role{RoleGateway, RoleAdmin, RoleWorker} {
		got = append(got, r.DatabaseRole())
	}
	if !reflect.DeepEqual(got, migrate.RuntimeRoles) {
		t.Fatalf("database roles %v, want %v", got, migrate.RuntimeRoles)
	}
}

// assertNoPassword fails the test if text contains the password of the tests. It is a local
// helper: this package cannot import test/testutil, which imports internal/authn and so, through
// it, this package.
func assertNoPassword(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "pw-for-the-test") {
		t.Error("the password was found in the error text")
	}
}
