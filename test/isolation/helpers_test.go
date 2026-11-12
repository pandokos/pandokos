package isolation

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// SQLSTATEs used by the tests.
const (
	insufficientPrivilege = "42501" // also "violates row-level security policy"
	foreignKey            = "23503"
	uniqueViolation       = "23505"
	checkViolation        = "23514"
	restrictViolation     = "23001"
	invalidText           = "22P02"
)

var runtimeRoles = []string{"gateway", "admin", "worker"}

// sqlstate returns the SQLSTATE of err, or "" when err is nil or not a database error.
func sqlstate(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// session is a way to start a transaction: which setting, if any, it makes.
type session struct {
	name    string
	setting string
	value   string
}

func sessions(a tenant.ID) []session {
	return []session{
		{"tenant-a", "app.tenant_id", a.String()},
		{"none", "", ""},
		{"empty", "app.tenant_id", ""},
		{"platform", "app.scope", "platform"},
	}
}

// inSession runs fn in one transaction of the session. It commits when fn returns nil and rolls
// back otherwise, and returns fn's error.
func inSession(t *testing.T, conn *pgx.Conn, s session, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	c := ctx(t)
	tx, err := conn.Begin(c)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if s.setting != "" {
		if _, err := tx.Exec(c, "SELECT pg_catalog.set_config($1, $2, true)", s.setting, s.value); err != nil {
			_ = tx.Rollback(c)
			t.Fatalf("set the session %s: %v", s.name, err)
		}
	}
	if err := fn(c, tx); err != nil {
		_ = tx.Rollback(c)
		return err
	}
	if err := tx.Commit(c); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return nil
}

func table(name string) string { return pgx.Identifier{"pandokos", name}.Sanitize() }

// tenantScopedTables lists the tables of schema pandokos from the catalogue, minus the global ones.
func tenantScopedTables(t *testing.T, super *pgx.Conn, spec Spec) []string {
	t.Helper()
	rows, err := super.Query(ctx(t), `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'pandokos' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range names {
		if _, global := spec.Global[n]; !global {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		t.Fatal("the catalogue shows no tenant-scoped table")
	}
	return out
}

func keyOf(fx Fixture) string {
	if fx.Key == "" {
		return "id"
	}
	return fx.Key
}

// snapshot returns the rows of tenant id in the table as one JSON text, read as superuser.
func snapshot(t *testing.T, super *pgx.Conn, tbl string, id tenant.ID) string {
	t.Helper()
	var s string
	err := super.QueryRow(ctx(t), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]') FROM `+table(tbl)+` t WHERE t.tenant_id = $1::uuid`, id.String()).Scan(&s)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
}

// insertRow creates one row of the fixture's table for the tenant, as superuser, and returns its key.
func insertRow(t *testing.T, super *pgx.Conn, fx Fixture, id tenant.ID) string {
	t.Helper()
	var key string
	if err := super.QueryRow(ctx(t), fx.Insert, id.String()).Scan(&key); err != nil {
		t.Fatalf("fixture insert: %v", err)
	}
	return key
}

// ensureRow makes sure the row whose key is *key still exists, else creates a new one and updates *key.
func ensureRow(t *testing.T, super *pgx.Conn, tbl string, fx Fixture, id tenant.ID, key *string) {
	t.Helper()
	var n int
	if err := super.QueryRow(ctx(t), `SELECT count(*) FROM `+table(tbl)+` WHERE `+pgx.Identifier{keyOf(fx)}.Sanitize()+`::text = $1`, *key).Scan(&n); err != nil {
		t.Fatalf("ensureRow: %v", err)
	}
	if n == 0 {
		*key = insertRow(t, super, fx, id)
	}
}

// result is what one statement returned.
type result struct {
	rows     []map[string]any
	affected int64
	err      error
}

// statement is one of the ten unfiltered statements of sprint.md section 6.3.
type statement struct {
	name  string
	sql   string
	args  []any
	query bool
	check func(t *testing.T, r result, b tenant.ID)
}

func statements(tbl string, fx Fixture, a, b tenant.ID, keyB string, sess session) []statement {
	tn := table(tbl)
	key := pgx.Identifier{keyOf(fx)}.Sanitize()
	bText := b.String()
	errOrNoForeignRows := func(t *testing.T, r result, b tenant.ID) {
		t.Helper()
		if r.err != nil {
			if sqlstate(r.err) != insufficientPrivilege {
				t.Errorf("error %v, want SQLSTATE %s", r.err, insufficientPrivilege)
			}
			return
		}
		for _, row := range r.rows {
			if row["tenant_id"] == b.String() {
				t.Errorf("a row of tenant B was returned: %v", row)
			}
		}
	}
	errOrNoRows := func(t *testing.T, r result, _ tenant.ID) {
		t.Helper()
		if r.err != nil {
			if sqlstate(r.err) != insufficientPrivilege {
				t.Errorf("error %v, want SQLSTATE %s", r.err, insufficientPrivilege)
			}
			return
		}
		if len(r.rows) != 0 {
			t.Errorf("%d rows returned, want none", len(r.rows))
		}
	}
	mustBeDenied := func(t *testing.T, r result, _ tenant.ID) {
		t.Helper()
		if sqlstate(r.err) != insufficientPrivilege {
			t.Errorf("error %v, want SQLSTATE %s", r.err, insufficientPrivilege)
		}
	}
	errOrSuccess := func(t *testing.T, r result, _ tenant.ID) {
		t.Helper()
		if r.err != nil && sqlstate(r.err) != insufficientPrivilege {
			t.Errorf("error %v, want success or SQLSTATE %s", r.err, insufficientPrivilege)
		}
	}
	errOrNoneAffected := func(t *testing.T, r result, _ tenant.ID) {
		t.Helper()
		if r.err != nil {
			if sqlstate(r.err) != insufficientPrivilege {
				t.Errorf("error %v, want SQLSTATE %s", r.err, insufficientPrivilege)
			}
			return
		}
		if r.affected != 0 {
			t.Errorf("%d rows affected, want none", r.affected)
		}
	}
	list := []statement{
		{name: "select-all", sql: `SELECT tenant_id::text AS tenant_id FROM ` + tn, query: true, check: errOrNoForeignRows},
		{name: "select-by-key", sql: `SELECT tenant_id::text AS tenant_id FROM ` + tn + ` WHERE ` + key + `::text = $1`, args: []any{keyB}, query: true, check: errOrNoRows},
		{name: "select-for-update", sql: `SELECT tenant_id::text AS tenant_id FROM ` + tn + ` WHERE ` + key + `::text = $1 FOR UPDATE`, args: []any{keyB}, query: true, check: errOrNoRows},
		{name: "insert-for-b", sql: fx.Insert, args: []any{bText}, check: mustBeDenied},
		{name: "update-all", sql: `UPDATE ` + tn + ` SET ` + fx.Update, check: errOrSuccess},
		{name: "update-by-key", sql: `UPDATE ` + tn + ` SET ` + fx.Update + ` WHERE ` + key + `::text = $1`, args: []any{keyB}, check: errOrNoneAffected},
	}
	if sess.name == "tenant-a" {
		list = append(list, statement{name: "move-to-b", sql: `UPDATE ` + tn + ` SET tenant_id = $1::uuid WHERE tenant_id = $2::uuid`, args: []any{bText, a.String()}, check: mustBeDenied})
	}
	return append(list,
		statement{name: "delete-all", sql: `DELETE FROM ` + tn, check: errOrSuccess},
		statement{name: "delete-by-key", sql: `DELETE FROM ` + tn + ` WHERE ` + key + `::text = $1`, args: []any{keyB}, check: errOrNoneAffected},
		statement{name: "truncate", sql: `TRUNCATE ` + tn, check: mustBeDenied},
	)
}

// runCommitted runs the statement in its own transaction of the session: BEGIN, the setting, the
// statement, then COMMIT if it succeeded and ROLLBACK if it failed.
func runCommitted(t *testing.T, conn *pgx.Conn, s session, st statement) result {
	t.Helper()
	var res result
	_ = inSession(t, conn, s, func(c context.Context, tx pgx.Tx) error {
		if st.query {
			rows, err := tx.Query(c, st.sql, st.args...)
			if err != nil {
				res.err = err
				return err
			}
			res.rows, res.err = pgx.CollectRows(rows, pgx.RowToMap)
			return res.err
		}
		tag, err := tx.Exec(c, st.sql, st.args...)
		res.affected, res.err = tag.RowsAffected(), err
		return err
	})
	return res
}

// owned runs SQL on a superuser connection acting as pandokos_owner, so that the objects it
// creates belong to the owner as in a real migration.
func owned(t *testing.T, d *dbtest.Database, sql string) {
	t.Helper()
	super := d.Super(t)
	if _, err := super.Exec(ctx(t), "SET ROLE pandokos_owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx(t), sql); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}
