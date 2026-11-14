package isolation

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// outcome is what one statement is expected to do.
type outcome int

const (
	allowed      outcome = iota // no error
	denied                      // SQLSTATE 42501
	noRows                      // no error and no row
	deniedOrNone                // 42501, or no error and no row
	noColumn                    // SQLSTATE 42703
)

func (o outcome) String() string {
	return [...]string{"allowed", "denied (42501)", "no rows", "denied or no rows", "undefined column (42703)"}[o]
}

type try struct {
	role string
	sess string // "tenant-a", "none", "empty", "platform"
	sql  string
	args []any
	copy bool // sql is a COPY ... TO STDOUT
	want outcome
}

func sessionNamed(a tenant.ID, name string) session {
	for _, s := range sessions(a) {
		if s.name == name {
			return s
		}
	}
	panic("no session " + name)
}

// run executes the statement in a transaction of the session as the role and returns the number
// of rows it produced (queries) and the error.
func (p try) run(t *testing.T, _ *dbtest.Database, a tenant.ID, conns map[string]*pgx.Conn) (rows int, cols []string, err error) {
	t.Helper()
	conn := conns[p.role]
	err = inSession(t, conn, sessionNamed(a, p.sess), func(c context.Context, tx pgx.Tx) error {
		if p.copy {
			_, err := tx.Conn().PgConn().CopyTo(c, io.Discard, p.sql)
			return err
		}
		if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(p.sql)), "SELECT") || strings.Contains(strings.ToUpper(p.sql), "RETURNING") {
			r, err := tx.Query(c, p.sql, p.args...)
			if err != nil {
				return err
			}
			for _, f := range r.FieldDescriptions() {
				cols = append(cols, f.Name)
			}
			for r.Next() {
				rows++
			}
			r.Close()
			return r.Err()
		}
		_, err := tx.Exec(c, p.sql, p.args...)
		return err
	})
	return rows, cols, err
}

func (p try) check(t *testing.T, rows int, err error) {
	t.Helper()
	got := sqlstate(err)
	switch p.want {
	case allowed:
		if err != nil {
			t.Errorf("%s as %s in %s: %v, want success\n%s", short(p.sql), p.role, p.sess, err, p.sql)
		}
	case denied:
		if got != insufficientPrivilege {
			t.Errorf("%s as %s in %s: %v (rows %d), want SQLSTATE %s\n%s", short(p.sql), p.role, p.sess, err, rows, insufficientPrivilege, p.sql)
		}
	case noRows:
		if err != nil || rows != 0 {
			t.Errorf("%s as %s in %s: %v, %d rows, want no rows\n%s", short(p.sql), p.role, p.sess, err, rows, p.sql)
		}
	case deniedOrNone:
		if got != insufficientPrivilege && (err != nil || rows != 0) {
			t.Errorf("%s as %s in %s: %v, %d rows, want 42501 or no rows\n%s", short(p.sql), p.role, p.sess, err, rows, p.sql)
		}
	case noColumn:
		if got != "42703" {
			t.Errorf("%s as %s in %s: %v, want SQLSTATE 42703\n%s", short(p.sql), p.role, p.sess, err, p.sql)
		}
	}
}

func short(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}

// seedVault gives tenants a and b a data key, a credential each, and returns the IDs.
func seedVault(t *testing.T, d *dbtest.Database, a, b tenant.ID) (credA, credB string) {
	t.Helper()
	super := d.Super(t)
	fx := DefaultSpec().Fixtures
	insertRow(t, super, fx["tenant_data_key"], a)
	insertRow(t, super, fx["tenant_data_key"], b)
	return insertRow(t, super, fx["provider_credential"], a), insertRow(t, super, fx["provider_credential"], b)
}

// TestVaultColumnPrivileges is T-321: the admin role cannot read the sealed secret, in any
// session; only the gateway role can, in its tenant's session; platform scope and no scope see
// nothing of the vault tables; kek_version is for reading, and for the admin role to add to.
func TestVaultColumnPrivileges(t *testing.T) {
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
		credA, credB := seedVault(t, d, a, b)
		conns := map[string]*pgx.Conn{}
		for _, r := range runtimeRoles {
			conns[r] = d.Connect(t, r)
		}
		const pc = "pandokos.provider_credential"

		t.Run("the admin role cannot read the sealed secret", func(t *testing.T) {
			for _, p := range []try{
				{sql: `SELECT secret FROM ` + pc, want: denied},
				{sql: `SELECT id, secret FROM ` + pc, want: denied},
				{sql: `SELECT * FROM ` + pc, want: denied},
				{sql: `SELECT t.* FROM ` + pc + ` t`, want: denied},
				{sql: `SELECT row_to_json(t) FROM ` + pc + ` t`, want: denied},
				{sql: `SELECT t FROM ` + pc + ` t`, want: denied},
				{sql: `SELECT length(secret) FROM ` + pc, want: denied},
				{sql: `SELECT count(secret) FROM ` + pc, want: denied},
				{sql: `SELECT md5(secret::text) FROM ` + pc, want: denied},
				{sql: `SELECT id FROM ` + pc + ` WHERE secret IS NOT NULL`, want: denied},
				{sql: `SELECT id FROM ` + pc + ` WHERE secret = '\x00'::bytea`, want: denied},
				{sql: `SELECT id FROM ` + pc + ` ORDER BY secret`, want: denied},
				{sql: `SELECT id FROM ` + pc + ` GROUP BY id, secret`, want: denied},
				{sql: `SELECT id FROM ` + pc + ` WHERE id = $1::uuid AND secret IS NOT NULL FOR UPDATE`, args: []any{credA}, want: denied},
				{sql: `UPDATE ` + pc + ` SET description = 'x' WHERE id = $1::uuid RETURNING secret`, args: []any{credA}, want: denied},
				{sql: `UPDATE ` + pc + ` SET description = 'x' WHERE id = $1::uuid RETURNING *`, args: []any{credA}, want: denied},
				{sql: `UPDATE ` + pc + ` SET description = 'x' WHERE secret IS NOT NULL`, want: denied},
				{sql: `UPDATE ` + pc + ` SET secret = secret`, want: denied},
				{sql: `DELETE FROM ` + pc + ` WHERE secret IS NULL`, want: denied},
				{sql: `DELETE FROM ` + pc + ` WHERE id = $1::uuid RETURNING secret`, args: []any{credA}, want: denied},
				{sql: `COPY ` + pc + ` (secret) TO STDOUT`, copy: true, want: denied},
				{sql: `COPY ` + pc + ` TO STDOUT`, copy: true, want: denied},
				{sql: `COPY (SELECT secret FROM ` + pc + `) TO STDOUT`, copy: true, want: denied},
				{sql: `SELECT secret FROM pandokos.provider_credential_metadata`, want: noColumn},
				{sql: `SELECT m.secret FROM pandokos.provider_credential_metadata m`, want: noColumn},
				{sql: `SELECT id FROM pandokos.provider_credential_metadata WHERE secret IS NULL`, want: noColumn},
			} {
				p.role, p.sess = "admin", "tenant-a"
				rows, _, err := p.run(t, d, a, conns)
				p.check(t, rows, err)
			}
			// An insert that returns the secret.
			ins := try{role: "admin", sess: "tenant-a", want: denied,
				sql: `INSERT INTO ` + pc + ` (tenant_id, provider, label, key_version, secret, fingerprint)
VALUES ($1::uuid, 'openai', 'ret-' || substr(md5(random()::text), 1, 8), 1, decode(repeat('00', 61), 'hex'), md5(random()::text)) RETURNING secret`,
				args: []any{a.String()}}
			rows, _, err := ins.run(t, d, a, conns)
			ins.check(t, rows, err)

			// The positive controls: the same role reads, locks and changes everything else.
			for _, p := range []try{
				{sql: `SELECT id, tenant_id, provider, label, description, state, is_default, key_version, fingerprint, hint, expires_at, validated_at, rotated_at, state_changed_at, state_changed_by, state_reason, created_at, updated_at FROM ` + pc, want: allowed},
				{sql: `SELECT id FROM ` + pc + ` WHERE id = $1::uuid FOR UPDATE`, args: []any{credA}, want: allowed},
				{sql: `UPDATE ` + pc + ` SET description = 'changed' WHERE id = $1::uuid RETURNING id, state`, args: []any{credA}, want: allowed},
				{sql: `SELECT * FROM pandokos.provider_credential_metadata`, want: allowed},
				{sql: `SELECT wrapped_key FROM pandokos.tenant_data_key`, want: allowed},
			} {
				p.role, p.sess = "admin", "tenant-a"
				rows, _, err := p.run(t, d, a, conns)
				p.check(t, rows, err)
			}

			// The view shows the tenant's own rows, and its columns are the metadata.
			p := try{role: "admin", sess: "tenant-a", sql: `SELECT * FROM pandokos.provider_credential_metadata`}
			rows, cols, err := p.run(t, d, a, conns)
			if err != nil || rows != 1 {
				t.Errorf("the view in A's session: %d rows, %v; want exactly A's credential", rows, err)
			}
			for _, c := range cols {
				if c == "secret" {
					t.Error("the view has a column secret")
				}
			}
			if len(cols) != 18 {
				t.Errorf("the view has %d columns (%v), want the 18 of the metadata", len(cols), cols)
			}
		})

		t.Run("platform scope, no scope and an empty tenant see nothing of the vault", func(t *testing.T) {
			for _, role := range runtimeRoles {
				for _, sess := range []string{"none", "empty", "platform"} {
					for _, tbl := range []string{"pandokos.tenant_data_key", pc, "pandokos.provider_credential_metadata"} {
						want := noRows
						if role == "worker" || (role == "gateway" && strings.HasSuffix(tbl, "_metadata")) {
							want = denied // no privilege at all
						}
						p := try{role: role, sess: sess, sql: `SELECT tenant_id::text AS tenant_id FROM ` + tbl, want: want}
						rows, _, err := p.run(t, d, a, conns)
						p.check(t, rows, err)
					}
				}
			}
			// The two roles that may name the sealed column ask for it where no tenant is set.
			for _, role := range []string{"gateway", "admin"} {
				for _, sess := range []string{"none", "empty", "platform"} {
					want := noRows
					if role == "admin" {
						want = denied
					}
					p := try{role: role, sess: sess, sql: `SELECT secret FROM ` + pc, want: want}
					rows, _, err := p.run(t, d, a, conns)
					p.check(t, rows, err)
				}
			}
		})

		t.Run("the gateway role reads secrets in its tenant's session, and nothing else", func(t *testing.T) {
			p := try{role: "gateway", sess: "tenant-a", sql: `SELECT id::text, secret FROM ` + pc}
			var seen []string
			err := inSession(t, conns["gateway"], sessionNamed(a, "tenant-a"), func(c context.Context, tx pgx.Tx) error {
				r, err := tx.Query(c, p.sql)
				if err != nil {
					return err
				}
				for r.Next() {
					var id string
					var s []byte
					if err := r.Scan(&id, &s); err != nil {
						return err
					}
					if len(s) != 61 {
						t.Errorf("the sealed secret has %d bytes", len(s))
					}
					seen = append(seen, id)
				}
				return r.Err()
			})
			if err != nil || len(seen) != 1 || seen[0] != credA {
				t.Errorf("the gateway read %v, %v; want exactly %s", seen, err, credA)
			}
			for _, w := range []try{
				{sql: `SELECT wrapped_key FROM pandokos.tenant_data_key`, want: allowed},
				{sql: `SELECT secret FROM ` + pc + ` WHERE id = $1::uuid`, args: []any{credB}, want: noRows},
				{sql: `SELECT * FROM pandokos.provider_credential_metadata`, want: denied},
				{sql: `UPDATE ` + pc + ` SET description = 'x'`, want: denied},
				{sql: `UPDATE ` + pc + ` SET secret = secret`, want: denied},
				{sql: `DELETE FROM ` + pc, want: denied},
				{sql: `TRUNCATE ` + pc, want: denied},
				{sql: fixtureOf("provider_credential"), args: []any{a.String()}, want: denied},
				{sql: fixtureOf("tenant_data_key"), args: []any{a.String()}, want: denied},
				{sql: `UPDATE pandokos.tenant_data_key SET wrapped_key = wrapped_key`, want: denied},
				{sql: `DELETE FROM pandokos.tenant_data_key`, want: denied},
				{sql: `INSERT INTO pandokos.kek_version (id, provider) VALUES ('gateway-test', 'test')`, want: denied},
			} {
				w.role, w.sess = "gateway", "tenant-a"
				rows, _, err := w.run(t, d, a, conns)
				w.check(t, rows, err)
			}
		})

		t.Run("the worker role has no privilege on the vault", func(t *testing.T) {
			for _, sql := range []string{
				`SELECT tenant_id FROM ` + pc, `SELECT tenant_id FROM pandokos.tenant_data_key`, `SELECT tenant_id FROM pandokos.provider_credential_metadata`,
				`SELECT secret FROM ` + pc, `UPDATE ` + pc + ` SET description = 'x'`, `DELETE FROM ` + pc, `TRUNCATE ` + pc,
				`INSERT INTO pandokos.kek_version (id, provider) VALUES ('worker-test', 'test')`,
			} {
				p := try{role: "worker", sess: "tenant-a", sql: sql, want: denied}
				rows, _, err := p.run(t, d, a, conns)
				p.check(t, rows, err)
			}
		})

		t.Run("kek_version is readable by every role and insertable by the admin role only", func(t *testing.T) {
			id := "t321-" + strings.ToLower(strings.ReplaceAll(a.String()[:8], "-", ""))
			for _, role := range runtimeRoles {
				for _, sess := range []string{"tenant-a", "none", "empty", "platform"} {
					p := try{role: role, sess: sess, sql: `SELECT id, provider FROM pandokos.kek_version`, want: allowed}
					rows, _, err := p.run(t, d, a, conns)
					p.check(t, rows, err)
					_ = rows
				}
			}
			for _, sess := range []string{"tenant-a", "none", "platform"} {
				for _, role := range runtimeRoles {
					want := denied
					sql := `INSERT INTO pandokos.kek_version (id, provider) VALUES ('` + id + `-` + role + `-` + sess + `', 'test')`
					if role == "admin" {
						want = allowed
					}
					p := try{role: role, sess: sess, sql: sql, want: want}
					rows, _, err := p.run(t, d, a, conns)
					p.check(t, rows, err)
				}
			}
			// The admin role may name nothing but id and provider, and never changes a row.
			for _, p := range []try{
				{role: "admin", sess: "tenant-a", sql: `INSERT INTO pandokos.kek_version (id, provider, created_at) VALUES ('t321-created', 'test', now())`, want: denied},
				{role: "admin", sess: "tenant-a", sql: `UPDATE pandokos.kek_version SET provider = 'other'`, want: denied},
				{role: "admin", sess: "tenant-a", sql: `UPDATE pandokos.kek_version SET id = id`, want: denied},
				{role: "admin", sess: "tenant-a", sql: `DELETE FROM pandokos.kek_version`, want: denied},
				{role: "admin", sess: "none", sql: `TRUNCATE pandokos.kek_version`, want: denied},
				{role: "gateway", sess: "tenant-a", sql: `UPDATE pandokos.kek_version SET provider = 'other'`, want: denied},
				{role: "gateway", sess: "tenant-a", sql: `DELETE FROM pandokos.kek_version`, want: denied},
				{role: "worker", sess: "tenant-a", sql: `UPDATE pandokos.kek_version SET provider = 'other'`, want: denied},
				{role: "worker", sess: "tenant-a", sql: `DELETE FROM pandokos.kek_version`, want: denied},
			} {
				rows, _, err := p.run(t, d, a, conns)
				p.check(t, rows, err)
			}
			var n int
			if err := d.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.kek_version WHERE provider = 'other' OR id = 't321-created'`).Scan(&n); err != nil || n != 0 {
				t.Errorf("a refused statement changed kek_version: %d, %v", n, err)
			}
		})

		t.Run("the data key table: the admin role inserts for its tenant and changes nothing", func(t *testing.T) {
			for _, p := range []try{
				{role: "admin", sess: "tenant-a", sql: `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 2, 'test-fixture', decode(repeat('00', 61), 'hex'))`, args: []any{a.String()}, want: allowed},
				{role: "admin", sess: "tenant-a", sql: `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 2, 'test-fixture', decode(repeat('00', 61), 'hex'))`, args: []any{b.String()}, want: denied},
				{role: "admin", sess: "tenant-a", sql: `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key, id) VALUES ($1::uuid, 3, 'test-fixture', decode(repeat('00', 61), 'hex'), pandokos.uuid_v7())`, args: []any{a.String()}, want: denied},
				{role: "admin", sess: "tenant-a", sql: `UPDATE pandokos.tenant_data_key SET wrapped_key = wrapped_key`, want: denied},
				{role: "admin", sess: "tenant-a", sql: `UPDATE pandokos.tenant_data_key SET kek_version = kek_version`, want: denied},
				{role: "admin", sess: "tenant-a", sql: `DELETE FROM pandokos.tenant_data_key`, want: denied},
			} {
				rows, _, err := p.run(t, d, a, conns)
				p.check(t, rows, err)
			}
		})
	})
}

// fixtureOf returns the insert statement of the fixture of a table.
func fixtureOf(table string) string { return DefaultSpec().Fixtures[table].Insert }

// mkCred creates a credential of a tenant as superuser, in the state asked for (a credential
// is created active; the trigger does the rest).
func mkCred(t *testing.T, super *pgx.Conn, tid tenant.ID, provider, state string, isDefault bool) string {
	t.Helper()
	var id string
	label := fmt.Sprintf("c-%s", strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), "-", ""))
	if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint, is_default)
VALUES ($1::uuid, $2, $3, 1, decode(repeat('ab', 61), 'hex'), md5(random()::text), $4) RETURNING id::text`, tid.String(), provider, label, isDefault).Scan(&id); err != nil {
		t.Fatalf("create a credential: %v", err)
	}
	if state != "active" {
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.provider_credential SET state = $2 WHERE id = $1::uuid`, id, state); err != nil {
			t.Fatalf("set the state %s: %v", state, err)
		}
	}
	return id
}

type credState struct {
	state, by, reason string
	secretNull        bool
	isDefault         bool
	rotatedSet        bool
	validatedSet      bool
	changedAt         time.Time
}

func readCred(t *testing.T, super *pgx.Conn, id string) credState {
	t.Helper()
	var c credState
	if err := super.QueryRow(ctx(t), `SELECT state, state_changed_by, state_reason, secret IS NULL, is_default, rotated_at IS NOT NULL, validated_at IS NOT NULL, state_changed_at
FROM pandokos.provider_credential WHERE id = $1::uuid`, id).Scan(&c.state, &c.by, &c.reason, &c.secretNull, &c.isDefault, &c.rotatedSet, &c.validatedSet, &c.changedAt); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCredentialGuards is T-322: the lifecycle of section 5.3 as the database enforces it, for the
// admin role and for the owner, and the notifications the changes send.
func TestCredentialGuards(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "tenant-a"), d.SeedTenant(t, "tenant-b")
	super := d.Super(t)
	seedVault(t, d, a, b)
	admin, owner := d.Connect(t, "admin"), d.Connect(t, "owner")
	sess := sessionNamed(a, "tenant-a")
	as := map[string]*pgx.Conn{"admin": admin, "owner": owner}

	exec := func(who, sql string, args ...any) error {
		t.Helper()
		return inSession(t, as[who], sess, func(c context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(c, sql, args...)
			return err
		})
	}
	// expect compares the SQLSTATE of the statement run by each actor with the wanted one ("" for success).
	expect := func(name, sql string, args []any, want map[string]string) {
		t.Helper()
		for who, st := range want {
			err := exec(who, sql, args...)
			if sqlstate(err) != st || (st == "" && err != nil) {
				t.Errorf("%s as %s: %v, want SQLSTATE %q", name, who, err, st)
			}
		}
	}
	both := func(st string) map[string]string { return map[string]string{"admin": st, "owner": st} }

	t.Run("a credential is created active", func(*testing.T) {
		const ins = `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint, state)
VALUES ($1::uuid, 'openai', 'born-' || substr(md5(random()::text), 1, 8), 1, decode(repeat('00', 61), 'hex'), md5(random()::text), $2)`
		expect("insert disabled", ins, []any{a.String(), "disabled"}, map[string]string{"admin": insufficientPrivilege, "owner": checkViolation})
		expect("insert revoked", ins, []any{a.String(), "revoked"}, map[string]string{"admin": insufficientPrivilege, "owner": checkViolation})
		expect("insert active", ins, []any{a.String(), "active"}, map[string]string{"owner": ""})
	})

	t.Run("active and disabled alternate", func(t *testing.T) {
		id := mkCred(t, super, a, "openai", "active", false)
		before := readCred(t, super, id)
		time.Sleep(20 * time.Millisecond)
		expect("disable", `UPDATE pandokos.provider_credential SET state = 'disabled', state_changed_by = 'platform', state_reason = 'abuse' WHERE id = $1::uuid`, []any{id}, both(""))
		c := readCred(t, super, id)
		if c.state != "disabled" || c.by != "platform" || c.reason != "abuse" || c.secretNull || !c.changedAt.After(before.changedAt) {
			t.Errorf("after disable: %+v", c)
		}
		// A change that is not of the state leaves state_changed_at alone, and the database owns it.
		expect("describe", `UPDATE pandokos.provider_credential SET description = 'd' WHERE id = $1::uuid`, []any{id}, both(""))
		if again := readCred(t, super, id); !again.changedAt.Equal(c.changedAt) {
			t.Error("state_changed_at moved without a change of state")
		}
		expect("enable", `UPDATE pandokos.provider_credential SET state = 'active', state_changed_by = 'tenant', state_reason = '' WHERE id = $1::uuid`, []any{id}, both(""))
		if c := readCred(t, super, id); c.state != "active" || c.by != "tenant" {
			t.Errorf("after enable: %+v", c)
		}
		// state_changed_at and rotated_at cannot be set from outside: the owner tries.
		before = readCred(t, super, id)
		expect("set the clocks", `UPDATE pandokos.provider_credential SET state_changed_at = '2001-01-01', rotated_at = '2001-01-01', created_at = created_at WHERE id = $1::uuid`, []any{id},
			map[string]string{"owner": "", "admin": insufficientPrivilege})
		if c := readCred(t, super, id); !c.changedAt.Equal(before.changedAt) || c.rotatedSet {
			t.Errorf("the owner set a clock that is the database's: %+v", c)
		}
		expect("an unknown state", `UPDATE pandokos.provider_credential SET state = 'paused' WHERE id = $1::uuid`, []any{id}, both(checkViolation))
	})

	t.Run("revocation is final and destroys the secret", func(t *testing.T) {
		id := mkCred(t, super, a, "openai", "active", false)
		expect("revoke", `UPDATE pandokos.provider_credential SET state = 'revoked', state_changed_by = 'platform', state_reason = 'leaked' WHERE id = $1::uuid`, []any{id}, map[string]string{"admin": ""})
		c := readCred(t, super, id)
		if c.state != "revoked" || !c.secretNull || c.reason != "leaked" || c.by != "platform" {
			t.Fatalf("after revoke: %+v", c)
		}
		for name, sql := range map[string]string{
			"enable":                   `UPDATE pandokos.provider_credential SET state = 'active' WHERE id = $1::uuid`,
			"disable":                  `UPDATE pandokos.provider_credential SET state = 'disabled' WHERE id = $1::uuid`,
			"put a secret back":        `UPDATE pandokos.provider_credential SET secret = decode(repeat('cd', 61), 'hex') WHERE id = $1::uuid`,
			"put a secret back, state": `UPDATE pandokos.provider_credential SET state = 'active', secret = decode(repeat('cd', 61), 'hex') WHERE id = $1::uuid`,
			"become the default":       `UPDATE pandokos.provider_credential SET is_default = true WHERE id = $1::uuid`,
		} {
			expect(name, sql, []any{id}, both(checkViolation))
		}
		expect("describe a revoked credential", `UPDATE pandokos.provider_credential SET description = 'still here' WHERE id = $1::uuid`, []any{id}, both(""))
		expect("revoke again", `UPDATE pandokos.provider_credential SET state = 'revoked', state_reason = 'again' WHERE id = $1::uuid`, []any{id}, both(""))
		if c := readCred(t, super, id); c.state != "revoked" || !c.secretNull {
			t.Errorf("after the refused changes: %+v", c)
		}
		// Revoking from disabled, and with a secret in the same statement: the secret goes anyway.
		id2 := mkCred(t, super, a, "openai", "disabled", false)
		expect("revoke a disabled credential", `UPDATE pandokos.provider_credential SET state = 'revoked', secret = decode(repeat('cd', 61), 'hex') WHERE id = $1::uuid`, []any{id2}, map[string]string{"admin": ""})
		if c := readCred(t, super, id2); c.state != "revoked" || !c.secretNull {
			t.Errorf("a revocation that names a secret: %+v", c)
		}
		// Only a revocation removes the secret.
		id3 := mkCred(t, super, a, "openai", "active", false)
		expect("remove the secret", `UPDATE pandokos.provider_credential SET secret = NULL WHERE id = $1::uuid`, []any{id3}, both(checkViolation))
		if c := readCred(t, super, id3); c.secretNull {
			t.Error("a secret was removed without a revocation")
		}
	})

	t.Run("the provider never changes", func(t *testing.T) {
		id := mkCred(t, super, a, "openai", "active", false)
		expect("change the provider", `UPDATE pandokos.provider_credential SET provider = 'anthropic' WHERE id = $1::uuid`, []any{id}, map[string]string{"admin": insufficientPrivilege, "owner": checkViolation})
	})

	t.Run("replacing the secret", func(t *testing.T) {
		id := mkCred(t, super, a, "openai", "active", false)
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.provider_credential SET validated_at = now() WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		if c := readCred(t, super, id); !c.validatedSet || c.rotatedSet {
			t.Fatalf("the premise: %+v", c)
		}
		expect("replace", `UPDATE pandokos.provider_credential SET secret = decode(repeat('cd', 61), 'hex'), key_version = 1, fingerprint = md5('x') WHERE id = $1::uuid`, []any{id}, both(""))
		if c := readCred(t, super, id); !c.rotatedSet || c.validatedSet {
			t.Errorf("a replaced secret keeps rotated_at unset or validated_at set: %+v", c)
		}
		expect("replace and validate", `UPDATE pandokos.provider_credential SET secret = decode(repeat('ef', 61), 'hex'), validated_at = now() WHERE id = $1::uuid`, []any{id}, both(""))
		if c := readCred(t, super, id); !c.rotatedSet || !c.validatedSet {
			t.Errorf("a replacement that is validated: %+v", c)
		}
		// The same bytes are not a replacement.
		id2 := mkCred(t, super, a, "openai", "active", false)
		expect("rewrite the same secret", `UPDATE pandokos.provider_credential SET secret = secret WHERE id = $1::uuid`, []any{id2}, map[string]string{"owner": ""})
		if c := readCred(t, super, id2); c.rotatedSet {
			t.Error("rotated_at was set by a statement that changed nothing")
		}
	})

	t.Run("one default per tenant and provider, one label per provider", func(t *testing.T) {
		const ins = `INSERT INTO pandokos.provider_credential (tenant_id, provider, label, key_version, secret, fingerprint, is_default)
VALUES ($1::uuid, $2, $3, 1, decode(repeat('00', 61), 'hex'), md5(random()::text), $4)`
		expect("the first default", ins, []any{a.String(), "dflt-provider", "one", true}, map[string]string{"admin": ""})
		for who := range as {
			err := exec(who, ins, a.String(), "dflt-provider", "two-"+who, true)
			if sqlstate(err) != uniqueViolation || !strings.Contains(fmt.Sprint(err), "provider_credential_default_key") {
				t.Errorf("a second default as %s: %v, want a violation of provider_credential_default_key", who, err)
			}
			if err := exec(who, ins, a.String(), "dflt-provider", "three-"+who, false); err != nil {
				t.Errorf("a non-default of the same provider as %s: %v", who, err)
			}
			if err := exec(who, ins, a.String(), "default-of-"+who, "one", true); err != nil {
				t.Errorf("a default of another provider as %s: %v", who, err)
			}
			err = exec(who, ins, a.String(), "dflt-provider", "one", false)
			if sqlstate(err) != uniqueViolation || !strings.Contains(fmt.Sprint(err), "provider_credential_label_key") {
				t.Errorf("a label taken, as %s: %v", who, err)
			}
			if err := exec(who, ins, a.String(), "label-of-"+who, "one", false); err != nil {
				t.Errorf("the label of another provider as %s: %v", who, err)
			}
		}
		// The same label and default in another tenant is free.
		if _, err := super.Exec(ctx(t), ins, b.String(), "dflt-provider", "one", true); err != nil {
			t.Errorf("the same label and default in another tenant: %v", err)
		}
		// A second default by update.
		other := mkCred(t, super, a, "dflt-provider", "active", false)
		for who := range as {
			err := exec(who, `UPDATE pandokos.provider_credential SET is_default = true WHERE id = $1::uuid`, other)
			if sqlstate(err) != uniqueViolation {
				t.Errorf("a second default by update, as %s: %v", who, err)
			}
		}
	})

	t.Run("the pin of a key", func(t *testing.T) {
		var sa string
		if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'pinner') RETURNING id::text`, a.String()).Scan(&sa); err != nil {
			t.Fatal(err)
		}
		credA := mkCred(t, super, a, "openai", "active", false)
		credA2 := mkCred(t, super, a, "openai", "active", false)
		credB := mkCred(t, super, b, "openai", "active", false)
		newKey := func(pin any) (string, error) {
			var id string
			err := super.QueryRow(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, credential_id)
VALUES ($1::uuid, $2::uuid, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'), sha256(random()::text::bytea), $3::uuid) RETURNING id::text`, a.String(), sa, pin).Scan(&id)
			return id, err
		}
		pinOf := func(id string) *string {
			var p *string
			if err := super.QueryRow(ctx(t), `SELECT credential_id::text FROM pandokos.virtual_key WHERE id = $1::uuid`, id).Scan(&p); err != nil {
				t.Fatal(err)
			}
			return p
		}
		// A pin to a credential of another tenant violates the foreign key.
		if _, err := newKey(credB); sqlstate(err) != foreignKey {
			t.Errorf("a pin to another tenant's credential: %v, want SQLSTATE %s", err, foreignKey)
		}
		if _, err := newKey("0192f0f0-0000-7000-8000-00000000dead"); sqlstate(err) != foreignKey {
			t.Errorf("a pin to a credential that does not exist: %v", err)
		}
		pinned, err := newKey(credA)
		if err != nil {
			t.Fatal(err)
		}
		if p := pinOf(pinned); p == nil || *p != credA {
			t.Fatalf("the pin: %v", p)
		}
		// It is set at creation and never changes, but to NULL: the owner too.
		expect("repin", `UPDATE pandokos.virtual_key SET credential_id = $2::uuid WHERE id = $1::uuid`, []any{pinned, credA2}, map[string]string{"owner": checkViolation, "admin": insufficientPrivilege})
		unpinned, err := newKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		expect("pin later", `UPDATE pandokos.virtual_key SET credential_id = $2::uuid WHERE id = $1::uuid`, []any{unpinned, credA}, map[string]string{"owner": checkViolation, "admin": insufficientPrivilege})
		if p := pinOf(unpinned); p != nil {
			t.Error("a key was pinned after its creation")
		}
		// The admin role may name the column when it creates a key; nothing else of the pin.
		expect("create a pinned key", `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, credential_id)
VALUES ($1::uuid, $2::uuid, 't' || translate(substr(md5(random()::text), 1, 16), '0189', 'ghij'), sha256(random()::text::bytea), $3::uuid)`, []any{a.String(), sa, credA2}, map[string]string{"admin": "", "owner": ""})
		// The owner may clear a pin (that is what the foreign key does).
		expect("clear", `UPDATE pandokos.virtual_key SET credential_id = NULL WHERE id = $1::uuid`, []any{pinned}, map[string]string{"owner": ""})
		if p := pinOf(pinned); p != nil {
			t.Error("the pin was not cleared")
		}
		// Deleting the credential clears the pins that name it, and sends the notifications.
		again, err := newKey(credA)
		if err != nil {
			t.Fatal(err)
		}
		listener := d.Super(t)
		if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
			t.Fatal(err)
		}
		drain(t, listener, 200*time.Millisecond)
		if err := exec("admin", `DELETE FROM pandokos.provider_credential WHERE id = $1::uuid`, credA); err != nil {
			t.Fatalf("delete the credential: %v", err)
		}
		if p := pinOf(again); p != nil {
			t.Errorf("the pin survived the deletion of its credential: %s", *p)
		}
		got := drain(t, listener, 500*time.Millisecond)
		if !got["vault:"+a.String()] || !got["tenant:"+a.String()] {
			t.Errorf("the deletion sent %v, want vault:<tenant> and tenant:<tenant>", keys(got))
		}
	})

	t.Run("every change sends vault:<tenant> and nothing else of the row", func(t *testing.T) {
		listener := d.Super(t)
		if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
			t.Fatal(err)
		}
		drain(t, listener, 200*time.Millisecond)
		rePayload := regexp.MustCompile(`^((vault|tenant):[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|platform)$`)
		check := func(what string, wantVault map[tenant.ID]bool) {
			t.Helper()
			got := drain(t, listener, 500*time.Millisecond)
			for p := range got {
				if !rePayload.MatchString(p) {
					t.Errorf("%s: the payload %q is not a kind and a tenant", what, p)
				}
				if strings.HasPrefix(p, "vault:") && len(p) != len("vault:")+36 {
					t.Errorf("%s: the payload %q holds more than a tenant", what, p)
				}
			}
			for _, tid := range []tenant.ID{a, b} {
				if got["vault:"+tid.String()] != wantVault[tid] {
					t.Errorf("%s: vault:%s sent=%v, want %v (all: %v)", what, tid, got["vault:"+tid.String()], wantVault[tid], keys(got))
				}
			}
		}
		id := mkCred(t, super, a, "notify-provider", "active", false)
		check("insert of a credential", map[tenant.ID]bool{a: true})
		if err := exec("admin", `UPDATE pandokos.provider_credential SET description = 'n' WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		check("update of a credential", map[tenant.ID]bool{a: true})
		if err := exec("admin", `UPDATE pandokos.provider_credential SET state = 'revoked' WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		check("revocation", map[tenant.ID]bool{a: true})
		if err := exec("admin", `DELETE FROM pandokos.provider_credential WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		check("deletion of a credential", map[tenant.ID]bool{a: true})
		mkCred(t, super, b, "notify-provider", "active", false)
		check("insert for another tenant", map[tenant.ID]bool{b: true})
		// A statement that changes nothing sends nothing.
		if err := exec("admin", `UPDATE pandokos.provider_credential SET description = 'x' WHERE false`); err != nil {
			t.Fatal(err)
		}
		check("an update of no row", map[tenant.ID]bool{})
		// Data keys: an update and a removal notify, an insert does not.
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 7, 'test-fixture', decode(repeat('00', 61), 'hex'))`, a.String()); err != nil {
			t.Fatal(err)
		}
		check("insert of a data key", map[tenant.ID]bool{})
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.tenant_data_key SET wrapped_key = decode(repeat('11', 61), 'hex') WHERE tenant_id = $1::uuid AND version = 7`, a.String()); err != nil {
			t.Fatal(err)
		}
		check("update of a data key", map[tenant.ID]bool{a: true})
		if _, err := super.Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid AND version = 7`, a.String()); err != nil {
			t.Fatal(err)
		}
		check("removal of a data key", map[tenant.ID]bool{a: true})
	})

	t.Run("the data key", func(t *testing.T) {
		// The version of a data key never changes.
		var id string
		if err := super.QueryRow(ctx(t), `SELECT id::text FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, a.String()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.tenant_data_key SET version = 9 WHERE id = $1::uuid`, id); sqlstate(err) != checkViolation {
			t.Errorf("a data key changed its version: %v", err)
		}
		// It names a key-encryption key version that is registered.
		_, err := super.Exec(ctx(t), `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 8, 'not-registered', decode(repeat('00', 61), 'hex'))`, a.String())
		if sqlstate(err) != foreignKey {
			t.Errorf("a data key of an unregistered version: %v", err)
		}
		// And is a wrapped key, not an empty value.
		_, err = super.Exec(ctx(t), `INSERT INTO pandokos.tenant_data_key (tenant_id, version, kek_version, wrapped_key) VALUES ($1::uuid, 8, 'test-fixture', decode('00', 'hex'))`, a.String())
		if sqlstate(err) != checkViolation {
			t.Errorf("a wrapped key of one byte: %v", err)
		}
	})
}

// drain collects the payloads of the notifications that arrive on the connection until nothing
// arrives for quiet.
func drain(t *testing.T, conn *pgx.Conn, quiet time.Duration) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for {
		c, cancel := context.WithTimeout(context.Background(), quiet)
		n, err := conn.WaitForNotification(c)
		cancel()
		if err != nil {
			return got
		}
		got[n.Payload] = true
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
