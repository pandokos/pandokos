package dbtest

import (
	"strings"
	"testing"
)

// TestGate is T-119: the decision table of sprint.md section 7.1 as a pure function.
func TestGate(t *testing.T) {
	for _, hasValue := range []bool{false, true} {
		for _, hasOptOut := range []bool{false, true} {
			for _, ci := range []string{"", "false", "true", "1"} {
				value, optOut := "", ""
				if hasValue {
					value = "x"
				}
				if hasOptOut {
					optOut = "1"
				}
				inCI := ci != "" && ci != "false"
				var want action
				switch {
				case hasOptOut && inCI:
					want = fail
				case hasValue:
					want = run
				case hasOptOut:
					want = skip
				default:
					want = fail
				}
				got, msg := gate("PANDOKOS_TEST_X", "PANDOKOS_TEST_NO_X", value, optOut, ci)
				if got != want {
					t.Errorf("value=%v optOut=%v ci=%q: action %d, want %d", hasValue, hasOptOut, ci, got, want)
				}
				switch got {
				case run:
					if msg != "" {
						t.Errorf("run has a message: %q", msg)
					}
				case fail, skip:
					if !strings.Contains(msg, "PANDOKOS_TEST_") {
						t.Errorf("value=%v optOut=%v ci=%q: message %q names no variable", hasValue, hasOptOut, ci, msg)
					}
				}
			}
		}
	}
	if _, msg := gate("PANDOKOS_TEST_X", "PANDOKOS_TEST_NO_X", "", "", ""); !strings.Contains(msg, "make db-up") {
		t.Errorf("the failure message does not say how to start the database: %q", msg)
	}
	if _, msg := gate("PANDOKOS_TEST_X", "PANDOKOS_TEST_NO_X", "x", "1", "true"); !strings.Contains(msg, "cannot be skipped in CI") {
		t.Errorf("the CI message is %q", msg)
	}
}
