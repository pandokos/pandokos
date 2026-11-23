package cli

import (
	"strings"
	"testing"
)

// TestLifecycleCompositesAreOutsideCommands is the part of T-1010 that reads the tables: the commands that
// make several calls (tenant deletion verify, tenant-export, tenant-export schema) are composites, and none
// of them is a row of commands (T-219 counts commands against operations).
func TestLifecycleCompositesAreOutsideCommands(t *testing.T) {
	has := func(table []command, words string) bool {
		for _, c := range table {
			if strings.Join(c.words, " ") == words {
				return true
			}
		}
		return false
	}
	for _, w := range []string{"tenant deletion verify", "tenant-export", "tenant-export schema"} {
		if !has(composites, w) {
			t.Errorf("%q is not a composite", w)
		}
		if has(commands, w) {
			t.Errorf("%q is in commands: it is made of several operations or of none", w)
		}
	}
	for _, w := range []string{"tenant delete", "tenant deletion get", "tenant deletion cancel", "tenant deletion list", "tenant deletion audit-head", "tenant deletion audit-export", "kek-version list"} {
		if !has(commands, w) {
			t.Errorf("%q is not a command of one operation", w)
		}
	}
}
