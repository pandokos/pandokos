package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestEveryOperationHasACommand is T-219: the table of commands covers every operation of the
// document and names none that does not exist.
func TestEveryOperationHasACommand(t *testing.T) {
	ops := map[string]bool{}
	for _, op := range adminapi.Operations {
		ops[op.ID] = true
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
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("operations without a command: %v", missing)
	}
	if len(commands) != len(adminapi.Operations) {
		t.Errorf("%d commands for %d operations", len(commands), len(adminapi.Operations))
	}
}
