package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
)

// TestEveryOperationHasACommand is T-219: the table of commands covers every operation of the
// document and names none that does not exist.
func TestEveryOperationHasACommand(t *testing.T) {
	ops := map[string]bool{}
	exempt := map[string]bool{}
	for _, op := range adminapi.Operations {
		ops[op.ID] = true
		// Sprint 4.2 (section 5.12): the CLI authenticates with a key, so the public operations
		// and the ones of the caller's own session have no command. The user and member
		// commands are written in step C5; until then they are exempt too.
		switch op.Permission {
		case "", authz.SessionSelf, authz.UserRead, authz.UserWrite, authz.MemberRead, authz.MemberWrite:
			exempt[op.ID] = true
		}
	}
	covered := map[string]bool{}
	words := map[string]bool{}
	for _, c := range commands {
		if c.operation == "" || len(c.words) == 0 || c.run == nil {
			t.Errorf("incomplete command %+v", c)
			continue
		}
		if !ops[c.operation] {
			t.Errorf("the command %q names %q, which is not an operation", strings.Join(c.words, " "), c.operation)
		}
		if covered[c.operation] {
			t.Errorf("operation %s has two commands", c.operation)
		}
		covered[c.operation] = true
		w := strings.Join(c.words, " ")
		if words[w] {
			t.Errorf("command %q is listed twice", w)
		}
		words[w] = true
	}
	var missing []string
	for id := range ops {
		if !covered[id] && !exempt[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("operations without a command: %v", missing)
	}
	if len(commands) != len(adminapi.Operations)-len(exempt) {
		t.Errorf("%d commands for %d operations, %d of them exempt", len(commands), len(adminapi.Operations), len(exempt))
	}
}
