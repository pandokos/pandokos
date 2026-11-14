//go:build linux

package telemetry_test

import (
	"io"
	"os"
	"syscall"
	"testing"
)

// captureStderr points file descriptor 2 at a file until the test ends, so that what any writer of
// the process puts on standard error is seen; the returned function reads it.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := syscall.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(f.Fd()), 2, 0); err != nil {
		t.Fatal(err)
	}
	restore := func() { _ = syscall.Dup3(saved, 2, 0) }
	t.Cleanup(func() { restore(); _ = syscall.Close(saved); f.Close() })
	return func() string {
		restore()
		defer func() { _ = syscall.Dup3(int(f.Fd()), 2, 0) }()
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		return string(b)
	}
}
