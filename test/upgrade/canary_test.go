//go:build upgrade

package upgrade

import (
	"testing"
)

// TestUpgradeCanary is T-1133: over the scenario of T-1130, no provider credential, key-encryption
// key, database password, session token, inference key or password of the local account is in any
// output of either binary: the logs of the previous release and of this one, the output of migrate,
// of config check, of init and of every command of the line. The harness passes every output it
// collects to the canaries as it comes (and the logs at the stop); this test also scans the complete
// logs and the output of migrate by name, and proves that outputs were scanned and secrets registered.
func TestUpgradeCanary(t *testing.T) {
	t.Parallel()
	u := runUpgrade(t)
	w := u.w
	c := w.inst.Canaries
	c.Scan(t, "the complete log of the previous release", []byte(w.proc.Stdout.String()), []byte(w.proc.Stderr.String()))
	c.Scan(t, "the complete log of this release", []byte(u.newProc.Stdout.String()), []byte(u.newProc.Stderr.String()))
	c.Scan(t, "the output of migrate", []byte(u.migrate))
	// The harness scanned init, migrate, config check, every command of the line and both logs.
	if n := c.Scanned(); n < 30 {
		t.Errorf("only %d outputs were scanned over the whole scenario: the commands of the world did not pass through the harness", n)
	}
	for _, name := range []string{"a", "b", "c"} {
		if w.tenants[name].Credential == "" || w.tenants[name].InferenceKey == "" {
			t.Errorf("tenant %s has no secret to look for", name)
		}
	}
}
