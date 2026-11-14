package ledger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// partitionName is the name of the partition of the month that is n months after the current one.
func partitionName(n int) string {
	now := time.Now().UTC()
	m := time.Date(now.Year(), now.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	return fmt.Sprintf("usage_record_y%04dm%02d", m.Year(), int(m.Month()))
}

func countAttr(l map[string]any) any {
	if v, ok := l["count"]; ok {
		return v
	}
	return l["created"]
}

// TestPartitioner is T-477.
func TestPartitioner(t *testing.T) {
	d := dbtest.NewMigrated(t)
	logs := &testutil.LogBuffer{}
	p := &Partitioner{DB: d.Open(t, store.RoleWorker), Logger: telemetry.NewLogger(logs, "debug"), Interval: 30 * time.Millisecond, Ahead: 3}
	exists := func(name string) bool {
		var ok bool
		if err := d.Super(t).QueryRow(ctx30(t), `SELECT to_regclass('pandokos.' || $1) IS NOT NULL`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	t.Run("nothing is missing after the migration", func(t *testing.T) {
		if err := p.Ensure(ctx30(t)); err != nil {
			t.Fatal(err)
		}
		if n := len(logs.Find(t, "usage partitions created")); n != 0 {
			t.Errorf("`usage partitions created` logged %d times", n)
		}
	})

	t.Run("Ensure creates what is missing and logs the count; a second call creates nothing", func(t *testing.T) {
		name := partitionName(3)
		if _, err := d.Super(t).Exec(ctx30(t), "DROP TABLE pandokos."+name); err != nil {
			t.Fatal(err)
		}
		if err := p.Ensure(ctx30(t)); err != nil {
			t.Fatal(err)
		}
		if !exists(name) {
			t.Fatalf("%s was not created", name)
		}
		lines := logs.Find(t, "usage partitions created")
		if len(lines) != 1 || countAttr(lines[0]) != float64(1) {
			t.Fatalf("lines %v", lines)
		}
		if err := p.Ensure(ctx30(t)); err != nil {
			t.Fatal(err)
		}
		if n := len(logs.Find(t, "usage partitions created")); n != 1 {
			t.Errorf("a second call logged a creation (%d lines)", n)
		}
	})

	t.Run("a row in the default partition is warned about once", func(t *testing.T) {
		tid := d.SeedTenant(t, "ledger-partitions")
		_, err := d.Super(t).Exec(ctx30(t), `INSERT INTO pandokos.usage_record (tenant_id, id, started_at, request_id, principal_kind, principal_id, funding,
			provider, api_family, operation, outcome, duration_ms, input_source, output_source, cost_status)
			VALUES ($1::uuid, pandokos.uuid_v7(), now() + interval '5 years', md5(random()::text), 'service_account', gen_random_uuid()::text, 'byok',
			'openai', 'openai', 'chat.completions', 'ok', 1, 'none', 'none', 'none')`, tid.String())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if err := p.Ensure(ctx30(t)); err != nil {
				t.Fatal(err)
			}
		}
		lines := logs.Find(t, "usage records in the default partition")
		if len(lines) != 1 || lines[0]["level"] != "WARN" {
			t.Errorf("%d lines: %v", len(lines), lines)
		}
	})

	t.Run("with the database away Ensure fails within its limit and Run keeps going", func(t *testing.T) {
		d.SetAcceptingConnections(t, false)
		t.Cleanup(func() { d.SetAcceptingConnections(t, true) })
		began := time.Now()
		if err := p.Ensure(ctx30(t)); err == nil {
			t.Error("Ensure succeeded with the database away")
		}
		if took := time.Since(began); took > 11*time.Second {
			t.Errorf("Ensure took %s, over its limit of 10 s", took)
		}

		before := len(logs.Lines(t))
		runCtx, stop := context.WithCancel(context.Background())
		runDone := make(chan struct{})
		go func() { p.Run(runCtx); close(runDone) }()
		testutil.Eventually(t, 5*time.Second, "failures are logged at warn", func() bool {
			n := 0
			for _, l := range logs.Lines(t)[before:] {
				if l["level"] == "WARN" {
					n++
				}
			}
			return n >= 2
		})
		select {
		case <-runDone:
			t.Fatal("Run returned while the database was away")
		default:
		}

		d.SetAcceptingConnections(t, true)
		name := partitionName(2)
		if _, err := d.Super(t).Exec(ctx30(t), "DROP TABLE pandokos."+name); err != nil {
			t.Fatal(err)
		}
		testutil.Eventually(t, 10*time.Second, "Run creates the partition again", func() bool { return exists(name) })
		stop()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return when its context ended")
		}
	})
}
