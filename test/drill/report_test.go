//go:build drill

package drill

import (
	"encoding/json"
	"flag"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDrillCanary is T-1145: over the cases, no credential, signing secret, key file content, database
// password, unseal share or key-service token is in any output of the binary, of the backup tools as
// the suite captured them, or in the report. Every case scans its outputs as a final check, before
// its containers go (World registers the scan with t.Cleanup); this test runs the logical case and
// fails if the case did not register its outputs, or if the report holds anything but what its
// schema allows.
func TestDrillCanary(t *testing.T) {
	t.Parallel()
	w, _, b2 := logicalWorld(t, Options{})
	r := w.Restore(RestoreSpec{Backup: b2, Keys: K1})
	assertRestored(t, r, b2, "unchanged", worldTenants...)

	for _, s := range w.Secrets() {
		if s == "" {
			t.Fatal("the world registered an empty secret")
		}
	}
	if n := w.Canaries.Scanned(); n < 20 {
		t.Errorf("the case scanned %d outputs: its commands did not pass through the harness", n)
	}
	w.Finish("CA")
	b, err := WriteReport("", false)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range append(w.Secrets(), w.KeyMaterial()...) {
		if s != "" && strings.Contains(string(b), s) {
			t.Errorf("secret %d is in the report", i)
		}
	}
	w.Canaries.Scan(t, "the report", b)
}

// TestFiguresAndReport is T-1146: the figure case, with the dataset the record states (10^5 ledger
// records and 10^4 audit rows, four tenants; the bulk of the ledger written by SQL in this case only,
// the audit rows only through the product, so that the chain is real), and the report of the run: it
// has every case of 5.5 and 5.3 with an outcome, every step with a duration, the versions, and
// nothing else. Sizes come from PANDOKOS_DRILL_LEDGER_ROWS (default 100000) and
// PANDOKOS_DRILL_AUDIT_ROWS (default 10000; CI may set 1000).
func TestFiguresAndReport(t *testing.T) {
	t.Parallel()
	ledger, audit := envInt("PANDOKOS_DRILL_LEDGER_ROWS", 100000), envInt("PANDOKOS_DRILL_AUDIT_ROWS", 10000)
	w := NewWorld(t, Options{LedgerRows: ledger, AuditRows: audit})
	w.Step(Step1a, func() { w.BackupLogical(B1) })
	w.Rotate()
	b2 := w.BackupLogical(B2)
	w.Disaster()
	r := w.Restore(RestoreSpec{Backup: b2, Keys: K1})
	assertRestored(t, r, b2, "unchanged", worldTenants...)
	w.Finish("F1")

	// In a whole run the other cases are awaited; with -run only the ones that ran are checked.
	want := []string{"L1", "P1", "P2", "K1", "K2", "K3", "K4", "S1", "S2", "S3", "S4a", "S4b", "S4c", "N1", "F1"}
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		deadline := time.Now().Add(25 * time.Minute)
		for time.Now().Before(deadline) && !hasCases(Collected(), want) {
			time.Sleep(2 * time.Second)
		}
		if !hasCases(Collected(), want) {
			t.Errorf("the report lacks a case of 5.5 or 5.3: it has %v, want %v", caseIDs(Collected()), want)
		}
	}

	b, err := WriteReport("", false)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing but the schema: decoding with unknown members refused proves it.
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var rep Report
	if err := dec.Decode(&rep); err != nil {
		t.Fatalf("the report holds something its schema does not: %v", err)
	}
	for _, c := range rep.Cases {
		if c.Outcome != Pass && c.Outcome != Fail {
			t.Errorf("case %s has the outcome %q", c.ID, c.Outcome)
		}
		if c.Versions.Binary == "" || c.Versions.PostgreSQL == "" {
			t.Errorf("case %s has no versions: %+v", c.ID, c.Versions)
		}
		if len(c.Steps) == 0 && c.NotRun == "" {
			t.Errorf("case %s has no steps and no reason for not having run", c.ID)
		}
		for _, s := range c.Steps {
			if s.DurationMS < 0 || s.Step == "" {
				t.Errorf("case %s has the step %+v", c.ID, s)
			}
		}
	}
	var fig *CaseReport
	for i := range rep.Cases {
		if rep.Cases[i].ID == "F1" {
			fig = &rep.Cases[i]
		}
	}
	if fig == nil {
		t.Fatal("the figure case F1 is not in the report")
	}
	if fig.Sizes.LedgerRows < int64(ledger) || fig.Sizes.AuditRows < int64(audit) || fig.Sizes.BackupBytes == 0 {
		t.Errorf("the figure case holds %+v, want at least %d ledger rows and %d audit rows", fig.Sizes, ledger, audit)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func caseIDs(r Report) []string {
	var out []string
	for _, c := range r.Cases {
		out = append(out, c.ID)
	}
	return out
}

func hasCases(r Report, want []string) bool {
	have := map[string]bool{}
	for _, id := range caseIDs(r) {
		have[id] = true
	}
	for _, id := range want {
		if !have[id] {
			return false
		}
	}
	return true
}
