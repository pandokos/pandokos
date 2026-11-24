package egress_test

import (
	"net/http"
	"testing"

	"github.com/pandokos/pandokos/internal/egress"
)

// TestStats is T-917: each reason of the guard, provoked, is counted once by reason, and nothing
// else is counted. Stats() returns a copy whose Blocked map is keyed by the closed values of
// egress.BlockReasons.
func TestStats(t *testing.T) {
	g := newGuard(t, egress.Options{})

	if got := g.Stats().Blocked; sumOf(got) != 0 {
		t.Fatalf("a new guard has counted %v", got)
	}
	// Accepted connections and requests are not counted.
	if err := g.PublicControl()("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Fatal(err)
	}
	if got := sumOf(g.Stats().Blocked); got != 0 {
		t.Fatalf("an accepted connection was counted: %d", got)
	}

	provoke := map[string]func(){
		egress.BlockNotTCP:           func() { _ = g.PublicControl()("udp4", "8.8.8.8:53", nil) },
		egress.BlockUnparsable:       func() { _ = g.PublicControl()("tcp4", "not-an-address", nil) },
		egress.BlockForbiddenAddress: func() { _ = g.PublicControl()("tcp4", "169.254.169.254:80", nil) },
		egress.BlockPrivateAddress:   func() { _ = g.PublicControl()("tcp4", "10.1.2.3:443", nil) },
		egress.BlockScheme: func() {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.test/v1", nil)
			_, _ = g.Transport(false).RoundTrip(req)
		},
		egress.BlockUserInfo: func() {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://user:synthetic-pw@example.test/v1", nil)
			_, _ = g.Transport(false).RoundTrip(req)
		},
	}
	if len(provoke) != len(egress.BlockReasons) {
		t.Fatalf("the test provokes %d reasons, egress.BlockReasons lists %d", len(provoke), len(egress.BlockReasons))
	}
	for i, reason := range egress.BlockReasons {
		run, ok := provoke[reason]
		if !ok {
			t.Fatalf("no way to provoke the reason %q", reason)
		}
		run()
		got := g.Stats().Blocked
		if got[reason] != 1 {
			t.Errorf("after one %s refusal the count is %d, want 1 (all: %v)", reason, got[reason], got)
		}
		if sumOf(got) != int64(i+1) {
			t.Errorf("after %d refusals the total is %d: %v", i+1, sumOf(got), got)
		}
	}
	// A second refusal of the same reason adds one; the snapshot is a copy.
	provoke[egress.BlockPrivateAddress]()
	snap := g.Stats().Blocked
	if snap[egress.BlockPrivateAddress] != 2 {
		t.Errorf("private_address = %d after two refusals", snap[egress.BlockPrivateAddress])
	}
	snap[egress.BlockPrivateAddress] = 99
	if g.Stats().Blocked[egress.BlockPrivateAddress] != 2 {
		t.Error("Stats returns the live map, not a copy")
	}
	// The keys are the closed list and nothing else.
	closed := map[string]bool{}
	for _, r := range egress.BlockReasons {
		closed[r] = true
	}
	for k := range g.Stats().Blocked {
		if !closed[k] {
			t.Errorf("the snapshot has the key %q, which is not in BlockReasons", k)
		}
	}

	// The third transport, the one of the key service, counts its refusals in the same snapshot.
	ks := newGuard(t, egress.Options{})
	for _, raw := range []string{"https://169.254.169.254:8200/v1/sys/health", "https://user:synthetic-pw@example.test/v1"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
		_, _ = ks.KeyService(nil).RoundTrip(req)
	}
	got := ks.Stats().Blocked
	if got[egress.BlockForbiddenAddress] != 1 || got[egress.BlockUserInfo] != 1 || sumOf(got) != 2 {
		t.Errorf("the refusals of the key service transport: %v, want one forbidden_address and one user_info", got)
	}
}

func sumOf(m map[string]int64) (n int64) {
	for _, v := range m {
		n += v
	}
	return n
}
