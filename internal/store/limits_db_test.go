package store_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pandokos/pandokos/test/dbtest"
)

// drainPayloads returns the payloads received until nothing arrives for quiet.
func drainPayloads(t *testing.T, conn *pgx.Conn, quiet time.Duration) []string {
	t.Helper()
	var got []string
	for {
		c, cancel := context.WithTimeout(context.Background(), quiet)
		n, err := conn.WaitForNotification(c)
		cancel()
		if err != nil {
			sort.Strings(got)
			return got
		}
		got = append(got, n.Payload)
	}
}

// TestLimitNotifications is T-625: a change of a limit rule tells the instances which tenant's
// rules changed, or that a default did, and nothing else.
func TestLimitNotifications(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a, b := d.SeedTenant(t, "notify-a"), d.SeedTenant(t, "notify-b")
	super := d.Super(t)
	// A rule of a key names the key by foreign key; the subject is written before the listening starts.
	saB := d.SeedServiceAccount(t, b, "notify-sa")
	_, keyB := d.SeedVirtualKey(t, b, saB)
	listener := d.Super(t)
	if _, err := listener.Exec(ctx(t), "LISTEN pandokos_cache"); err != nil {
		t.Fatal(err)
	}
	drainPayloads(t, listener, 200*time.Millisecond)

	check := func(what string, want ...string) {
		t.Helper()
		got := drainPayloads(t, listener, 400*time.Millisecond)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Errorf("%s: payloads %v, want %v", what, got, want)
			return
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: payloads %v, want %v", what, got, want)
				return
			}
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := super.Exec(ctx(t), sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}

	exec(`INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, metric, limit_value) VALUES ($1::uuid, 'tenant', 'requests_per_minute', 10)`, a.String())
	check("a rule of A is inserted", "limits:"+a.String())
	exec(`UPDATE pandokos.limit_rule SET limit_value = 20 WHERE tenant_id = $1::uuid`, a.String())
	check("a rule of A is updated", "limits:"+a.String())
	exec(`INSERT INTO pandokos.limit_rule (tenant_id, subject_kind, virtual_key_id, metric, limit_value) VALUES ($1::uuid, 'key', $2::uuid, 'tokens_per_minute', 10)`, b.String(), keyB)
	check("a rule of B is inserted", "limits:"+b.String())
	exec(`DELETE FROM pandokos.limit_rule WHERE tenant_id = $1::uuid`, a.String())
	check("a rule of A is deleted", "limits:"+a.String())

	exec(`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES (NULL, 'requests_per_minute', 100)`)
	check("a default is inserted", "limits")
	exec(`UPDATE pandokos.platform_limit_rule SET limit_value = 200 WHERE tenant_id IS NULL`)
	check("a default is updated", "limits")
	exec(`INSERT INTO pandokos.platform_limit_rule (tenant_id, metric, limit_value) VALUES ($1::uuid, 'requests_per_minute', 300)`, a.String())
	check("a platform row of A is inserted", "limits:"+a.String())
	exec(`UPDATE pandokos.platform_limit_rule SET limit_value = 400 WHERE tenant_id = $1::uuid`, a.String())
	check("a platform row of A is updated", "limits:"+a.String())
	exec(`DELETE FROM pandokos.platform_limit_rule WHERE tenant_id = $1::uuid`, a.String())
	check("a platform row of A is deleted", "limits:"+a.String())
	exec(`DELETE FROM pandokos.platform_limit_rule WHERE tenant_id IS NULL`)
	check("a default is deleted", "limits")

	// Nothing else: a statement that changes no row, and the rows of other tables, send nothing.
	exec(`UPDATE pandokos.limit_rule SET limit_value = 1 WHERE false`)
	exec(`INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, 'bot')`, a.String())
	check("nothing that is not a limit rule")
}
