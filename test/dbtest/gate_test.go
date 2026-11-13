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

// TestGateCAFile extends T-119 with the pair of variables of the TLS tests.
func TestGateCAFile(t *testing.T) {
	if EnvDatabaseCAFile != "PANDOKOS_TEST_DATABASE_CA_FILE" || EnvNoDatabaseTLS != "PANDOKOS_TEST_NO_DATABASE_TLS" {
		t.Fatal("the names of the variables changed")
	}
	if got, msg := gate(EnvDatabaseCAFile, EnvNoDatabaseTLS, "", "", ""); got != fail || !strings.Contains(msg, EnvNoDatabaseTLS) {
		t.Errorf("missing variable: %d %q", got, msg)
	}
	if got, _ := gate(EnvDatabaseCAFile, EnvNoDatabaseTLS, "", "1", ""); got != skip {
		t.Errorf("opt-out outside CI: %d", got)
	}
	if got, _ := gate(EnvDatabaseCAFile, EnvNoDatabaseTLS, "", "1", "true"); got != fail {
		t.Errorf("opt-out in CI: %d", got)
	}
	if got, _ := gate(EnvDatabaseCAFile, EnvNoDatabaseTLS, "/ca.pem", "", "true"); got != run {
		t.Errorf("present: %d", got)
	}
}
