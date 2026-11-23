package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestWriterRejectsAClosedTenant is the part of T-1000 that concerns the ledger: a record offered
// after the tenant was closed is rejected with the constraint tenant_open and frees its slot, and
// another tenant's records go on.
func TestWriterRejectsAClosedTenant(t *testing.T) {
	d := dbtest.NewMigrated(t)
	closed, open := d.SeedTenant(t, "ledger-closed"), d.SeedTenant(t, "ledger-open")
	d.BeginDeletion(t, closed)
	logs := &testutil.LogBuffer{}
	w, err := New(Options{DB: d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(logs, "debug"), BufferSize: 10, BatchSize: 5, FlushInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run(context.Background())
	for i := 0; i < 4; i++ { // two records of each tenant, interleaved
		if !w.Admit() {
			t.Fatal("Admit refused")
		}
		if i%2 == 0 {
			w.Record(testutil.ValidRecord(closed))
		} else {
			w.Record(testutil.ValidRecord(open))
		}
	}
	testutil.Eventually(t, 10*time.Second, "every slot is settled", func() bool { return w.Stats().InUse == 0 })
	if s := w.Stats(); s.Rejected != 2 || s.Written != 2 {
		t.Errorf("stats %+v, want the two records of the closed tenant rejected and the two of the other written", s)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, closed.String()); n != 0 {
		t.Errorf("%d records written for a closed tenant", n)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.usage_record WHERE tenant_id = $1::uuid`, open.String()); n != 2 {
		t.Errorf("%d records for the other tenant, want 2", n)
	}
	lines := logs.Find(t, "usage record rejected")
	if len(lines) == 0 {
		t.Fatal("no `usage record rejected` line")
	}
	for _, l := range lines {
		if l["constraint"] != "tenant_open" || l["sqlstate"] != "23001" || l["level"] != "ERROR" {
			t.Errorf("the line: %v, want constraint tenant_open and SQLSTATE 23001", l)
		}
		if _, ok := l["tenant_id"]; ok {
			t.Errorf("the line names a tenant: %v", l)
		}
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Close(cctx)
}
