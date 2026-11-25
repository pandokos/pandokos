package isolation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

// commitIn is inSession that returns the error of the commit too: the shape of a price version is checked
// at commit, by constraint triggers that run deferred.
func commitIn(t *testing.T, conn *pgx.Conn, s session, fn func(ctx context.Context, tx pgx.Tx) error) error {
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
	return tx.Commit(c)
}

// seedModel inserts a model of the provider openai for the price tests and returns its ID.
func seedModel(t *testing.T, d *dbtest.Database, name string) string {
	t.Helper()
	return d.SeedModel(t, "openai", name)
}

// seedStarted inserts a version that has started (an hour ago) with the triggers off: the state the
// passing of time leaves, which the guard of the table refuses for every role.
func seedStarted(t *testing.T, super *pgx.Conn, id tenant.ID, model, currency string) string {
	t.Helper()
	var vid string
	err := pgx.BeginFunc(ctx(t), super, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx(t), `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx(t), `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency)
			VALUES ($1::uuid, $2::uuid, now() - interval '1 hour', $3) RETURNING id::text`, id.String(), model, currency).Scan(&vid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx(t), `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate) VALUES ($1::uuid, $2::uuid, 'input', 1)`, id.String(), vid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return vid
}

// insertVersion inserts a version of the model, scheduled `in` from now, with the rates (category,
// threshold, rate) of the same transaction; the commit checks its shape.
func insertVersion(ctx context.Context, tx pgx.Tx, id tenant.ID, model, currency, kind string, in time.Duration, rates ...[3]any) (string, error) {
	var vid string
	if err := tx.QueryRow(ctx, `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, kind, currency)
		VALUES ($1::uuid, $2::uuid, now() + $3::interval, $4, $5) RETURNING id::text`, id.String(), model, fmt.Sprintf("%d microseconds", in.Microseconds()), kind, currency).Scan(&vid); err != nil {
		return "", err
	}
	for _, r := range rates {
		if _, err := tx.Exec(ctx, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, threshold, rate) VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
			id.String(), vid, r[0], r[1], r[2]); err != nil {
			return vid, err
		}
	}
	return vid, nil
}

var baseRate = [3]any{"input", 0, 1}

// TestTenantPricePolicies is T-741: both price tables, direct and pooled: the admin role writes and reads
// its tenant's rows only; the gateway reads its tenant's only and writes nothing; the worker holds
// nothing; the platform scope and a session without scope see no row and write none.
func TestTenantPricePolicies(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "price-a"), d.SeedTenant(t, "price-b")
		model := seedModel(t, d, "isolation-price-model")
		super := d.Super(t)
		va := d.SeedTenantPrice(t, a, "EUR", "openai", "isolation-price-model", time.Now().Add(2*time.Hour))
		vb := d.SeedTenantPrice(t, b, "EUR", "openai", "isolation-price-model", time.Now().Add(2*time.Hour))
		admin, gateway, worker := d.Connect(t, "admin"), d.Connect(t, "gateway"), d.Connect(t, "worker")
		sessA, sessB := sessions(a)[0], session{name: "tenant-b", setting: "app.tenant_id", value: b.String()}
		blind := []session{noSession, {name: "empty", setting: "app.tenant_id", value: ""}, {name: "platform", setting: "app.scope", value: "platform"}}

		ids := func(rows []map[string]any, col string) []string {
			var out []string
			for _, r := range rows {
				out = append(out, r[col].(string))
			}
			return out
		}
		read := func(conn *pgx.Conn, s session, tbl, col string) ([]string, error) {
			rows, err := query(t, conn, s, fmt.Sprintf(`SELECT DISTINCT %s::text AS k FROM pandokos.%s ORDER BY 1`, col, tbl))
			return ids(rows, "k"), err
		}

		for _, role := range []struct {
			name string
			conn *pgx.Conn
		}{{"admin", admin}, {"gateway", gateway}} {
			for _, tbl := range []struct{ name, col string }{{"tenant_price_version", "id"}, {"tenant_price_rate", "price_version_id"}} {
				if got, err := read(role.conn, sessA, tbl.name, tbl.col); err != nil || len(got) != 1 || got[0] != va {
					t.Errorf("%s reads %s in A's session: %v %v, want only %s", role.name, tbl.name, got, err, va)
				}
				if got, err := read(role.conn, sessB, tbl.name, tbl.col); err != nil || len(got) != 1 || got[0] != vb {
					t.Errorf("%s reads %s in B's session: %v %v, want only %s", role.name, tbl.name, got, err, vb)
				}
				for _, s := range blind {
					if got, err := read(role.conn, s, tbl.name, tbl.col); err != nil || len(got) != 0 {
						t.Errorf("%s reads %s in the session %s: %v %v, want no row", role.name, tbl.name, s.name, got, err)
					}
				}
			}
		}

		t.Run("the worker holds nothing", func(t *testing.T) {
			for _, tbl := range []string{"tenant_price_version", "tenant_price_rate"} {
				for _, s := range append([]session{sessA}, blind...) {
					_, err := read(worker, s, tbl, "tenant_id")
					if sqlstate(err) != insufficientPrivilege {
						t.Errorf("the worker reads %s in %s: %v, want 42501", tbl, s.name, err)
					}
				}
			}
		})

		t.Run("the gateway writes nothing", func(t *testing.T) {
			for _, s := range append([]session{sessA}, blind...) {
				for what, sql := range map[string]string{
					"insert a version": `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency) VALUES (current_setting('app.tenant_id', true)::uuid, '` + model + `', now() + interval '1 day', 'GBP')`,
					"insert a rate":    `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate) VALUES ('` + a.String() + `', '` + va + `', 'output', 1)`,
					"delete a version": `DELETE FROM pandokos.tenant_price_version`,
					"update a version": `UPDATE pandokos.tenant_price_version SET note = 'x'`,
				} {
					if _, err := execIn(t, gateway, s, sql); sqlstate(err) != insufficientPrivilege {
						t.Errorf("the gateway, %s in %s: %v, want 42501", what, s.name, err)
					}
				}
			}
		})

		t.Run("the admin role writes its tenant's rows only", func(t *testing.T) {
			var mine string
			err := commitIn(t, admin, sessA, func(c context.Context, tx pgx.Tx) error {
				var err error
				mine, err = insertVersion(c, tx, a, model, "GBP", "price", time.Hour, baseRate, [3]any{"output", 0, 2})
				return err
			})
			if err != nil {
				t.Fatalf("A's version in A's session: %v", err)
			}
			// A version for B from A's session, and any from the sessions that name no tenant: refused by the policy.
			for _, c := range []struct {
				who  string
				s    session
				for_ tenant.ID
			}{{"A's session for B", sessA, b}, {"B's session for A", sessB, a}} {
				err := commitIn(t, admin, c.s, func(cx context.Context, tx pgx.Tx) error {
					_, err := insertVersion(cx, tx, c.for_, model, "CHF", "price", time.Hour, baseRate)
					return err
				})
				if sqlstate(err) != insufficientPrivilege {
					t.Errorf("%s: %v, want 42501", c.who, err)
				}
			}
			for _, s := range blind {
				err := commitIn(t, admin, s, func(cx context.Context, tx pgx.Tx) error {
					_, err := insertVersion(cx, tx, a, model, "CHF", "price", time.Hour, baseRate)
					return err
				})
				if sqlstate(err) != insufficientPrivilege {
					t.Errorf("session %s: %v, want 42501", s.name, err)
				}
			}
			// A rate of A's version from B's session names a version B does not have: the foreign key.
			if _, err := execIn(t, admin, sessB, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate) VALUES ($1::uuid, $2::uuid, 'output', 1)`, b.String(), va); sqlstate(err) != foreignKey {
				t.Errorf("a rate of A's version in B's session: %v, want 23503", err)
			}
			// Deletion: only the tenant's own scheduled versions.
			for _, s := range append([]session{sessB}, blind...) {
				if n, err := execIn(t, admin, s, `DELETE FROM pandokos.tenant_price_version WHERE id = $1::uuid`, mine); err != nil || n != 0 {
					t.Errorf("session %s deleted %d of A's versions (%v)", s.name, n, err)
				}
			}
			if n, err := execIn(t, admin, sessA, `DELETE FROM pandokos.tenant_price_version WHERE id = $1::uuid`, mine); err != nil || n != 1 {
				t.Errorf("A deleted %d of its versions (%v)", n, err)
			}
			if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.tenant_price_rate WHERE price_version_id = $1::uuid`, mine); n != "0" {
				t.Errorf("%s rates of a deleted version remain", n)
			}
			// B's rows were never touched.
			if got := scalar(t, super, `SELECT count(*)::text FROM pandokos.tenant_price_version WHERE tenant_id = $1::uuid`, b.String()); got != "1" {
				t.Errorf("tenant B has %s versions", got)
			}
		})
	})
}

// TestTenantPriceRules is T-742: for the admin role and for a superuser, a start in the past, an update,
// the deletion of a started version, the deletion of a scheduled one (which takes its rates), truncate,
// the unique key per currency, the format of the currency and the rate checks; at commit a price without a
// rate, without a base rate, an end with a rate, a rate added to an existing version, a rate for another
// tenant's version; a model with a version cannot be deleted, also from the platform scope.
func TestTenantPriceRules(t *testing.T) {
	t.Parallel()
	dbtest.EachMode(t, func(t *testing.T, d *dbtest.Database) {
		a, b := d.SeedTenant(t, "rule-a"), d.SeedTenant(t, "rule-b")
		model := seedModel(t, d, "isolation-rule-model")
		super := d.Super(t)
		admin := d.Connect(t, "admin")
		sessA := sessions(a)[0]
		platform := session{name: "platform", setting: "app.scope", value: "platform"}

		type actor struct {
			name string
			conn *pgx.Conn
			s    session
		}
		actors := []actor{{"admin", admin, sessA}, {"superuser", super, session{name: "tenant-a", setting: "app.tenant_id", value: a.String()}}}
		// The error each statement gets: the admin role has no privilege where the table gives none, a superuser meets the trigger.
		type want struct{ admin, super string }
		check := func(what string, w want, fn func(a actor) error) {
			t.Helper()
			for _, ac := range actors {
				got, exp := sqlstate(fn(ac)), w.admin
				if ac.name == "superuser" {
					exp = w.super
				}
				if got != exp {
					t.Errorf("%s as %s: SQLSTATE %q, want %q", what, ac.name, got, exp)
				}
			}
		}
		started := seedStarted(t, super, a, model, "EUR")
		scheduled := d.SeedTenantPrice(t, a, "EUR", "openai", "isolation-rule-model", time.Now().Add(5*time.Hour))

		t.Run("a start in the past", func(t *testing.T) {
			check("a version that starts in the past", want{checkViolation, checkViolation}, func(ac actor) error {
				return commitIn(t, ac.conn, ac.s, func(c context.Context, tx pgx.Tx) error {
					_, err := insertVersion(c, tx, a, model, "USD", "price", -time.Minute, baseRate)
					return err
				})
			})
		})
		t.Run("an update, the deletion of a started version and truncate", func(t *testing.T) {
			check("an update", want{insufficientPrivilege, restrictViolation}, func(ac actor) error {
				_, err := execIn(t, ac.conn, ac.s, `UPDATE pandokos.tenant_price_version SET note = 'x' WHERE id = $1::uuid`, scheduled)
				return err
			})
			check("the deletion of a started version", want{restrictViolation, restrictViolation}, func(ac actor) error {
				_, err := execIn(t, ac.conn, ac.s, `DELETE FROM pandokos.tenant_price_version WHERE id = $1::uuid`, started)
				return err
			})
			check("truncate of the versions", want{insufficientPrivilege, restrictViolation}, func(ac actor) error {
				_, err := execIn(t, ac.conn, ac.s, `TRUNCATE pandokos.tenant_price_version CASCADE`)
				return err
			})
			check("truncate of the rates", want{insufficientPrivilege, restrictViolation}, func(ac actor) error {
				_, err := execIn(t, ac.conn, ac.s, `TRUNCATE pandokos.tenant_price_rate`)
				return err
			})
			check("an update of a rate", want{insufficientPrivilege, restrictViolation}, func(ac actor) error {
				_, err := execIn(t, ac.conn, ac.s, `UPDATE pandokos.tenant_price_rate SET rate = 9 WHERE price_version_id = $1::uuid`, scheduled)
				return err
			})
			if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.tenant_price_version WHERE id IN ($1::uuid, $2::uuid)`, started, scheduled); n != "2" {
				t.Errorf("%s of the two versions remain after the refusals", n)
			}
		})
		t.Run("the deletion of a scheduled version takes its rates", func(t *testing.T) {
			for _, ac := range actors {
				var vid string
				err := commitIn(t, ac.conn, ac.s, func(c context.Context, tx pgx.Tx) error {
					var err error
					vid, err = insertVersion(c, tx, a, model, "JPY", "price", 7*24*time.Hour, baseRate, [3]any{"output", 0, 3}, [3]any{"output", 1000, 4})
					return err
				})
				if err != nil {
					t.Fatalf("%s: %v", ac.name, err)
				}
				if n, err := execIn(t, ac.conn, ac.s, `DELETE FROM pandokos.tenant_price_version WHERE id = $1::uuid`, vid); err != nil || n != 1 {
					t.Fatalf("%s: deleted %d (%v)", ac.name, n, err)
				}
				if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.tenant_price_rate WHERE price_version_id = $1::uuid`, vid); n != "0" {
					t.Errorf("%s: %s rates remain", ac.name, n)
				}
			}
		})
		t.Run("the unique key per currency, the format and the rate checks", func(t *testing.T) {
			at := time.Now().Add(9 * time.Hour).UTC().Truncate(time.Microsecond)
			insertAt := func(ac actor, currency string, rates ...[3]any) error {
				return commitIn(t, ac.conn, ac.s, func(c context.Context, tx pgx.Tx) error {
					var vid string
					if err := tx.QueryRow(c, `INSERT INTO pandokos.tenant_price_version (tenant_id, model_id, valid_from, currency) VALUES ($1::uuid, $2::uuid, $3, $4) RETURNING id::text`,
						a.String(), model, at, currency).Scan(&vid); err != nil {
						return err
					}
					for _, r := range rates {
						if _, err := tx.Exec(c, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, threshold, rate) VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, a.String(), vid, r[0], r[1], r[2]); err != nil {
							return err
						}
					}
					return nil
				})
			}
			// The first insert is accepted; the same list, model and start again is refused; another currency is another list.
			if err := insertAt(actors[0], "CAD", baseRate); err != nil {
				t.Fatalf("the first version: %v", err)
			}
			check("the same list, model and start twice", want{uniqueViolation, uniqueViolation}, func(ac actor) error { return insertAt(ac, "CAD", baseRate) })
			if err := insertAt(actors[0], "AUD", baseRate); err != nil {
				t.Errorf("the same start in another currency: %v", err)
			}
			for _, cur := range []string{"eur", "EU", "EURO", "E1R", ""} {
				check("the currency "+cur, want{checkViolation, checkViolation}, func(ac actor) error { return insertAt(ac, cur, baseRate) })
			}
			for what, rate := range map[string][3]any{
				"an unknown category":    {"images", 0, 1},
				"a negative rate":        {"input", 0, -1},
				"a rate above a million": {"input", 0, 1000001},
				"a negative threshold":   {"input", -1, 1},
				"a threshold above 1e8":  {"input", 100000001, 1},
			} {
				currency := "NZD"
				check(what, want{checkViolation, checkViolation}, func(ac actor) error { return insertAt(ac, currency, rate) })
			}
		})
		t.Run("at commit: the shape of a version and of its rates", func(t *testing.T) {
			const shape = "23000"
			cases := map[string]func(c context.Context, tx pgx.Tx) error{
				"a price without a rate": func(c context.Context, tx pgx.Tx) error {
					_, err := insertVersion(c, tx, a, model, "SEK", "price", 11*time.Hour)
					return err
				},
				"a price without a base rate": func(c context.Context, tx pgx.Tx) error {
					_, err := insertVersion(c, tx, a, model, "SEK", "price", 11*time.Hour, [3]any{"input", 1000, 1})
					return err
				},
				"a price with a base rate for one category only": func(c context.Context, tx pgx.Tx) error {
					_, err := insertVersion(c, tx, a, model, "SEK", "price", 11*time.Hour, baseRate, [3]any{"output", 500, 2})
					return err
				},
				"an end with a rate": func(c context.Context, tx pgx.Tx) error {
					_, err := insertVersion(c, tx, a, model, "SEK", "end", 11*time.Hour, baseRate)
					return err
				},
				"a rate added to an existing version": func(c context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(c, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate) VALUES ($1::uuid, $2::uuid, 'cache_read', 5)`, a.String(), scheduled)
					return err
				},
			}
			for what, fn := range cases {
				check(what, want{shape, shape}, func(ac actor) error { return commitIn(t, ac.conn, ac.s, fn) })
			}
			// An end with no rate, and a price with a base rate for each category, are accepted.
			if err := commitIn(t, admin, sessA, func(c context.Context, tx pgx.Tx) error {
				if _, err := insertVersion(c, tx, a, model, "SEK", "end", 12*time.Hour); err != nil {
					return err
				}
				_, err := insertVersion(c, tx, a, model, "NOK", "price", 12*time.Hour, baseRate, [3]any{"input", 100000, 2}, [3]any{"output", 0, 4})
				return err
			}); err != nil {
				t.Errorf("a valid end and a valid tiered price: %v", err)
			}
		})
		t.Run("a rate for another tenant's version", func(t *testing.T) {
			vb := d.SeedTenantPrice(t, b, "EUR", "openai", "isolation-rule-model", time.Now().Add(6*time.Hour))
			check("a rate of B's version in a row of A", want{foreignKey, foreignKey}, func(ac actor) error {
				_, err := execIn(t, ac.conn, ac.s, `INSERT INTO pandokos.tenant_price_rate (tenant_id, price_version_id, category, rate) VALUES ($1::uuid, $2::uuid, 'output', 1)`, a.String(), vb)
				return err
			})
		})
		t.Run("a model with a version cannot be deleted, also from the platform scope", func(t *testing.T) {
			for _, ac := range []actor{{"admin, platform scope", admin, platform}, {"superuser", super, platform}} {
				_, err := execIn(t, ac.conn, ac.s, `DELETE FROM pandokos.model WHERE id = $1::uuid`, model)
				if got := sqlstate(err); !deleteRefused(got) && (ac.name == "superuser" || got != insufficientPrivilege) {
					t.Errorf("%s: SQLSTATE %q (%v), want 23503 or 23001 (the foreign key of the versions)", ac.name, got, err)
				}
			}
			if n := scalar(t, super, `SELECT count(*)::text FROM pandokos.model WHERE id = $1::uuid`, model); n != "1" {
				t.Error("the model was deleted")
			}
		})
	})
}
