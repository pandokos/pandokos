package isolation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/test/dbtest"
)

// TestKeyTables is T-286.
func TestKeyTables(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
		super := d.Super(t)
		var saA, saB string
		for id, dst := range map[string]*string{a.String(): &saA, b.String(): &saB} {
			if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot') RETURNING id::text`, id).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		const keyA, keyB = "tabcdefghijklmnop", "tabcdefghijklmnoq"
		for _, k := range []struct{ tenant, sa, key string }{{a.String(), saA, keyA}, {b.String(), saB, keyB}} {
			if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, sha256('x'::bytea))`, k.tenant, k.sa, k.key); err != nil {
				t.Fatal(err)
			}
		}

		// No runtime role reads or writes the directory, whatever session it is in.
		for _, role := range runtimeRoles {
			conn := d.Connect(t, role)
			for _, sess := range sessions(a) {
				for _, sql := range []string{
					`SELECT count(*) FROM pandokos.virtual_key_directory`,
					`INSERT INTO pandokos.virtual_key_directory (key_id, tenant_id) VALUES ('tzzzzzzzzzzzzzzzz', pandokos.uuid_v7())`,
					`UPDATE pandokos.virtual_key_directory SET tenant_id = tenant_id`,
					`DELETE FROM pandokos.virtual_key_directory`,
					`TRUNCATE pandokos.virtual_key_directory`,
				} {
					err := inSession(t, conn, sess, func(c context.Context, tx pgx.Tx) error {
						_, err := tx.Exec(c, sql)
						return err
					})
					if sqlstate(err) != insufficientPrivilege {
						t.Errorf("%s in session %s: %q gave %v, want SQLSTATE %s", role, sess.name, sql, err, insufficientPrivilege)
					}
				}
			}
		}

		// The lookup returns the tenant for a key ID and NULL otherwise, for gateway and admin;
		// the worker cannot call it.
		for _, role := range []string{"gateway", "admin"} {
			conn := d.Connect(t, role)
			for _, sess := range sessions(a) {
				for key, want := range map[string]*string{keyA: strp(a.String()), keyB: strp(b.String()), "tzzzzzzzzzzzzzzzz": nil, "": nil} {
					var got *string
					if err := inSession(t, conn, sess, func(c context.Context, tx pgx.Tx) error {
						return tx.QueryRow(c, `SELECT pandokos.virtual_key_lookup($1::text)::text`, key).Scan(&got)
					}); err != nil {
						t.Fatalf("%s/%s: %v", role, sess.name, err)
					}
					if (got == nil) != (want == nil) || (got != nil && *got != *want) {
						t.Errorf("%s in session %s: lookup(%q) = %v, want %v", role, sess.name, key, got, want)
					}
				}
			}
		}
		err := inSession(t, d.Connect(t, "worker"), sessions(a)[0], func(c context.Context, tx pgx.Tx) error {
			var s *string
			return tx.QueryRow(c, `SELECT pandokos.virtual_key_lookup($1::text)::text`, keyA).Scan(&s)
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("the worker called the lookup: %v", err)
		}
		// The directory is filled by the key row and by nothing else.
		var n int
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.virtual_key_directory`).Scan(&n); err != nil || n != 2 {
			t.Errorf("directory rows: %d %v", n, err)
		}

		// A key row changes only by revocation, once, at the database's time, even for the superuser.
		var revoked time.Time
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.virtual_key SET revoked_at = '2001-01-01T00:00:00Z' WHERE key_id = $1`, keyA); err != nil {
			t.Fatalf("revocation: %v", err)
		}
		if err := super.QueryRow(ctx(t), `SELECT revoked_at FROM pandokos.virtual_key WHERE key_id = $1`, keyA).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if time.Since(revoked) > time.Minute || time.Since(revoked) < -time.Minute {
			t.Errorf("revoked_at is %v: the value sent was used instead of the time of the database", revoked)
		}
		for name, sql := range map[string]string{
			"revoke again":      `UPDATE pandokos.virtual_key SET revoked_at = now() + interval '1 day' WHERE key_id = 'tabcdefghijklmnop'`,
			"un-revoke":         `UPDATE pandokos.virtual_key SET revoked_at = NULL WHERE key_id = 'tabcdefghijklmnop'`,
			"change the hash":   `UPDATE pandokos.virtual_key SET secret_hash = sha256('y'::bytea) WHERE key_id = 'tabcdefghijklmnoq'`,
			"change the key ID": `UPDATE pandokos.virtual_key SET key_id = 'tabcdefghijklmnor' WHERE key_id = 'tabcdefghijklmnoq'`,
			"change the expiry": `UPDATE pandokos.virtual_key SET expires_at = now() + interval '1 year' WHERE key_id = 'tabcdefghijklmnoq'`,
			"change the tenant": `UPDATE pandokos.virtual_key SET tenant_id = '` + a.String() + `' WHERE key_id = 'tabcdefghijklmnoq'`,
		} {
			if _, err := super.Exec(ctx(t), sql); sqlstate(err) != checkViolation {
				t.Errorf("%s: %v, want SQLSTATE %s", name, err, checkViolation)
			}
		}
		// The admin role may revoke, once, and nothing else.
		admin := d.Connect(t, "admin")
		sessB := sessions(b)[0]
		if err := inSession(t, admin, sessB, func(c context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(c, `UPDATE pandokos.virtual_key SET revoked_at = now() WHERE key_id = $1`, keyB)
			if err == nil && tag.RowsAffected() != 1 {
				t.Errorf("%d rows revoked", tag.RowsAffected())
			}
			return err
		}); err != nil {
			t.Errorf("the admin role cannot revoke: %v", err)
		}
		err = inSession(t, admin, sessB, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `UPDATE pandokos.virtual_key SET secret_hash = sha256('z'::bytea)`)
			return err
		})
		if sqlstate(err) != insufficientPrivilege {
			t.Errorf("the admin role changed a hash: %v", err)
		}

		// Platform keys: the admin role sees nothing without platform scope; gateway and worker nothing at all.
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.platform_key (key_id, secret_hash) VALUES ('pabcdefghijklmnop', sha256('x'::bytea))`); err != nil {
			t.Fatal(err)
		}
		count := func(role string, s session) (int, error) {
			var n int
			err := inSession(t, d.Connect(t, role), s, func(c context.Context, tx pgx.Tx) error {
				return tx.QueryRow(c, `SELECT count(*) FROM pandokos.platform_key`).Scan(&n)
			})
			return n, err
		}
		for _, s := range sessions(a) {
			n, err := count("admin", s)
			want := 0
			if s.name == "platform" {
				want = 1
			}
			if err != nil || n != want {
				t.Errorf("admin in session %s sees %d platform keys (%v), want %d", s.name, n, err, want)
			}
			for _, role := range []string{"gateway", "worker"} {
				if _, err := count(role, s); sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s in session %s read platform keys: %v", role, s.name, err)
				}
			}
		}
		// A bootstrap key without an expiry is refused.
		err = inSession(t, admin, sessions(a)[3], func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `INSERT INTO pandokos.platform_key (key_id, secret_hash, bootstrap) VALUES ('pabcdefghijklmnoq', sha256('x'::bytea), true)`)
			return err
		})
		if sqlstate(err) != checkViolation {
			t.Errorf("a bootstrap key without expiry: %v, want SQLSTATE %s", err, checkViolation)
		}
		// With an expiry it is accepted.
		err = inSession(t, admin, sessions(a)[3], func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, `INSERT INTO pandokos.platform_key (key_id, secret_hash, bootstrap, expires_at) VALUES ('pabcdefghijklmnoq', sha256('x'::bytea), true, now() + interval '1 hour')`)
			return err
		})
		if err != nil {
			t.Errorf("a bootstrap key with an expiry: %v", err)
		}
	})
}

func strp(s string) *string { return &s }
