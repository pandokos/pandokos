package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/testutil"
)

// tenantDataKeys are the columns of a row that describe what a tenant used or paid.
var tenantDataKeys = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens",
	"estimated_input_tokens", "estimated_output_tokens", "cost", "cost_status", "currency", "price_version_id", "price_source",
	"provider_usage", "other_units", "metadata", "model", "provider", "limit_state"}

// TestLedgerLogsUsageDetails is the part of T-739 that belongs to the ledger writer: with
// telemetry.tenant_usage_details off, the lines `usage record rejected` (by Validate and by the database)
// and `usage record not written` carry no token, estimate, cost, currency, price version or price source;
// with it on they carry the whole row.
func TestLedgerLogsUsageDetails(t *testing.T) {
	// Amounts and counts of the fixture that must stay out of the log when the key is off.
	canaries := []string{"0.000099", `"input_tokens":9`, `"output_tokens":12`, "USD", "known"}

	type forced struct {
		name, line string
		force      func(t *testing.T, r *rig) // makes the writer log the line
	}
	cases := []forced{
		{"a record that fails Validate", "usage record rejected", func(t *testing.T, r *rig) {
			r.admitRecord(t, tenantID(t), func(rec *usage.Record) { rec.RequestID = "" })
			r.wait(t, "the record to be rejected", func() bool { return r.w.Stats().Rejected == 1 })
		}},
		{"a record the database refuses", "usage record rejected", func(t *testing.T, r *rig) {
			r.fs.setFail(func(_ tenant.ID, _ []usage.Record) error { return errRefused })
			r.admitRecord(t, tenantID(t))
			r.wait(t, "the record to be rejected", func() bool { return r.w.Stats().Rejected == 1 })
		}},
		{"a record written off at Close", "usage record not written", func(t *testing.T, r *rig) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r.w.Close(ctx)
			r.w.Record(testutil.ValidRecord(tenantID(t)))
		}},
	}
	for _, c := range cases {
		for _, on := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, key %v", c.name, on), func(t *testing.T) {
				o, logs := opts(5, 10, 5*time.Millisecond)
				o.UsageDetails = on
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
				c.force(t, r)
				lines := logs.Find(t, c.line)
				if len(lines) != 1 {
					t.Fatalf("%d `%s` lines: %s", len(lines), c.line, logs.String())
				}
				line := lines[0]
				text := logs.String()
				for _, k := range []string{"record_id", "tenant_id"} {
					if _, ok := line[k]; !ok {
						t.Errorf("the line has no %s: %v", k, line)
					}
				}
				if on {
					var row map[string]any
					if err := json.Unmarshal([]byte(fmt.Sprint(line["row"])), &row); err != nil {
						t.Fatalf("the line has no row: %v", line)
					}
					if c.name != "a record that fails Validate" && (row["cost"] == nil || row["currency"] != "USD") {
						t.Errorf("with the key on the row is whole: %v", row)
					}
					return
				}
				if _, ok := line["row"]; ok {
					t.Errorf("with the key off the line has a row: %v", line)
				}
				for _, k := range tenantDataKeys {
					if strings.Contains(text, `"`+k+`\"`) || strings.Contains(text, `\"`+k+`\"`) {
						t.Errorf("with the key off the log holds %s: %s", k, text)
					}
				}
				for _, canary := range canaries {
					if strings.Contains(text, canary) {
						t.Errorf("with the key off the log holds %s: %s", canary, text)
					}
				}
			})
		}
	}
}
