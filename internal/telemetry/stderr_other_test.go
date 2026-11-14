//go:build !linux

package telemetry_test

import "testing"

// captureStderr cannot redirect descriptor 2 here: the check of T-503 on standard error is made
// on Linux, where CI runs.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	return func() string { return "" }
}
