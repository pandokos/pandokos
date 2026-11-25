//go:build drill

package drill

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Report is the one JSON document the suite writes with PANDOKOS_DRILL_REPORT=<file>: for each case
// its identifier, outcome, the steps with their durations, the versions and the sizes. It has no
// free-form field: no name of a tenant, no secret and no address can be in it.
type Report struct {
	Cases []CaseReport `json:"cases"`
}

// CaseReport is one case of the drill.
type CaseReport struct {
	ID       string       `json:"id"`      // L1, P1, P2, K1 to K4, S1 to S4, N1, F1
	Outcome  string       `json:"outcome"` // Pass or Fail
	NotRun   string       `json:"not_run,omitempty"`
	Steps    []StepReport `json:"steps"`
	Versions Versions     `json:"versions"`
	Sizes    Sizes        `json:"sizes"`
}

// The outcomes of a case.
const (
	Pass = "pass"
	Fail = "fail"
)

// StepReport is a step of the guide with the time it took.
type StepReport struct {
	Step       Step  `json:"step"`
	DurationMS int64 `json:"duration_ms"`
}

// Versions are the versions the case ran on.
type Versions struct {
	Binary     string `json:"binary"`
	PostgreSQL string `json:"postgresql"`
	KeyService string `json:"key_service,omitempty"`
}

// Sizes are what the case held: rows of the ledger and of the audit logs, bytes of the backup.
type Sizes struct {
	LedgerRows  int64 `json:"ledger_rows"`
	AuditRows   int64 `json:"audit_rows"`
	BackupBytes int64 `json:"backup_bytes"`
}

// StepTimer records the steps of a case.
type StepTimer struct {
	mu    sync.Mutex
	steps []StepReport
}

// Time runs fn as step s and records how long it took.
func (st *StepTimer) Time(s Step, fn func()) {
	start := time.Now()
	fn()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.steps = append(st.steps, StepReport{Step: s, DurationMS: time.Since(start).Milliseconds()})
}

// Steps returns what was recorded.
func (st *StepTimer) Steps() []StepReport {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]StepReport(nil), st.steps...)
}

// collected holds the reports of the cases that ended, for TestMain to write.
var collected struct {
	mu    sync.Mutex
	cases []CaseReport
}

// Collect adds a case to the report of the run.
func Collect(c CaseReport) {
	collected.mu.Lock()
	defer collected.mu.Unlock()
	collected.cases = append(collected.cases, c)
}

// Collected returns the report of the run so far.
func Collected() Report {
	collected.mu.Lock()
	defer collected.mu.Unlock()
	return Report{Cases: append([]CaseReport(nil), collected.cases...)}
}

// WriteReport writes the report of the run to path, with no indentation when oneLine is set (the
// line the CI job prints).
func WriteReport(path string, oneLine bool) ([]byte, error) {
	r := Collected()
	var b []byte
	var err error
	if oneLine {
		b, err = json.Marshal(r)
	} else {
		b, err = json.MarshalIndent(r, "", "  ")
	}
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
	}
	return b, nil
}
