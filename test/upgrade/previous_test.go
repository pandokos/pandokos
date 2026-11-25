//go:build upgrade

package upgrade

import (
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/instance"
)

// TestPreviousBinary is T-1129: the binary built from test/upgrade/PREVIOUS reports that version and
// that commit, its migrate status on an empty database needs schema version 16, and this tree needs
// 17 (the one migration an upgrade from the previous release crosses: 0017_events).
func TestPreviousBinary(t *testing.T) {
	t.Parallel()
	pin := ReadPin(t)
	if pin.Version == "" || pin.Commit == "" || pin.Image == "" {
		t.Fatalf("test/upgrade/PREVIOUS is not three lines: %+v", pin)
	}
	old := instance.Previous(t)
	code, out, errs := old.Run(t, nil, "version")
	if code != 0 {
		t.Fatalf("version: exit %d\n%s%s", code, out, errs)
	}
	if want := "pandokos " + strings.TrimPrefix(pin.Version, "v") + " (commit " + pin.Commit + ","; !strings.HasPrefix(out, want) {
		t.Errorf("the previous binary says %q, want it to start with %q", strings.TrimSpace(out), want)
	}

	d := dbtest.New(t) // empty
	for _, c := range []struct {
		name string
		bin  instance.Binary
		want string
	}{{"the previous binary", old, "version 16"}, {"this tree", instance.Build(t), "version 17"}} {
		code, out, errs := c.bin.Run(t, d.Env(t), "migrate", "status")
		text := out + errs
		if code == 0 || !strings.Contains(text, c.want) {
			t.Errorf("%s: migrate status on an empty database exits %d and says %q, want it to say it needs %s", c.name, code, strings.TrimSpace(text), c.want)
		}
	}
}
