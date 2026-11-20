package ledger

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestLedgerLogsNoRow is the part of T-739 that belongs to the ledger writer, as ADR 0055 redefines
// it: the lines `usage record rejected` (by Validate and by the database) and `usage record not written`
// name at most the request and the failure. No row, no tenant, no record ID, no usage, no cost.
func TestLedgerLogsNoRow(t *testing.T) {
	allowed := []string{"time", "level", "msg", "request_id", "error", "sqlstate", "constraint"}

	type forced struct {
		name, line string
		force      func(t *testing.T, r *rig, tid tenant.ID) // makes the writer log the line
	}
	cases := []forced{
		{"a record that fails Validate", "usage record rejected", func(t *testing.T, r *rig, tid tenant.ID) {
			r.admitRecord(t, tid, func(rec *usage.Record) { rec.RequestID = "" })
			r.wait(t, "the record to be rejected", func() bool { return r.w.Stats().Rejected == 1 })
		}},
		{"a record the database refuses", "usage record rejected", func(t *testing.T, r *rig, tid tenant.ID) {
			r.fs.setFail(func(_ tenant.ID, _ []usage.Record) error { return errRefused })
			r.admitRecord(t, tid)
			r.wait(t, "the record to be rejected", func() bool { return r.w.Stats().Rejected == 1 })
		}},
		{"a record written off at Close", "usage record not written", func(t *testing.T, r *rig, tid tenant.ID) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r.w.Close(ctx)
			r.w.Record(testutil.ValidRecord(tid))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, logs := opts(5, 10, 5*time.Millisecond)
			fs := &fakeStore{}
			w, err := newWriter(o, fs)
			if err != nil {
				t.Fatal(err)
			}
			r := &rig{w: w, fs: fs, logs: logs, done: make(chan struct{})}
			go func() { w.Run(context.Background()); close(r.done) }()
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				w.Close(ctx)
			})
			tid := tenantID(t)
			c.force(t, r, tid)
			lines := logs.Find(t, c.line)
			if len(lines) != 1 {
				t.Fatalf("%d `%s` lines: %s", len(lines), c.line, logs.String())
			}
			for k := range lines[0] {
				if !slices.Contains(allowed, k) {
					t.Errorf("the line has %s: %v", k, lines[0])
				}
			}
			if strings.Contains(logs.String(), tid.String()) {
				t.Errorf("the log holds the tenant ID: %s", logs.String())
			}
		})
	}
}
