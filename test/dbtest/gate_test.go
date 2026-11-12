package dbtest

import (
	"strings"
	"testing"
)

// TestGate is T-119: the decision table of sprint.md section 7.1, written out literally.
func TestGate(t *testing.T) {
	rows := []struct {
		hasValue, hasOptOut bool
		ci                  string
		want                action
	}{
		{false, false, "", fail},
		{false, false, "false", fail},
		{false, false, "true", fail},
		{false, false, "1", fail},
		{false, true, "", skip},
		{false, true, "false", skip},
		{false, true, "true", fail},
		{false, true, "1", fail},
		{true, false, "", run},
		{true, false, "false", run},
		{true, false, "true", run},
		{true, false, "1", run},
		{true, true, "", run},
		{true, true, "false", run},
		{true, true, "true", fail},
		{true, true, "1", fail},
	}
	for _, r := range rows {
		value, optOut := "", ""
		if r.hasValue {
			value = "x"
		}
		if r.hasOptOut {
			optOut = "1"
		}
		got, msg := gate("PANDOKOS_TEST_X", "PANDOKOS_TEST_NO_X", value, optOut, r.ci)
		if got != r.want {
			t.Errorf("value=%v optOut=%v ci=%q: action %d, want %d", r.hasValue, r.hasOptOut, r.ci, got, r.want)
		}
		if got == run {
			if msg != "" {
				t.Errorf("run has a message: %q", msg)
			}
		} else if !strings.Contains(msg, "PANDOKOS_TEST_") {
			t.Errorf("value=%v optOut=%v ci=%q: message %q names no variable", r.hasValue, r.hasOptOut, r.ci, msg)
		}
	}
	if _, msg := gate("PANDOKOS_TEST_X", "PANDOKOS_TEST_NO_X", "", "", ""); !strings.Contains(msg, "make db-up") {
		t.Errorf("the failure message does not say how to start the database: %q", msg)
	}
	if _, msg := gate("PANDOKOS_TEST_X", "PANDOKOS_TEST_NO_X", "x", "1", "true"); !strings.Contains(msg, "cannot be skipped in CI") {
		t.Errorf("the CI message is %q", msg)
	}
}
