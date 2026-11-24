package outbox

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// stepClock is a clock that a test moves.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestRelayUnwrapCache is T-1076: with a transit provider the relay opens the signing secrets
// through vault.UnwrapCache, so the key service is asked once per lifetime and not once per
// round; with the service away a round fails without consuming an attempt.
func TestRelayUnwrapCache(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "relay-transit")
	recv := newSink(t, nil)
	dest, _ := d.SeedEventDestination(t, tid, recv.url("/t"))
	rotateWith(t, d, tid, dest, 3600) // a current and a previous secret, both under the tenant's data key

	// Move the installation from the key file to the service, as the worker does.
	local := vaulttest.ProviderFor(t, d)
	s := transittest.New(t)
	files := vaulttest.Transit(t, s, vaulttest.TransitSetup{})
	clk := &stepClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	tp := vaulttest.TransitProvider(t, s, files, func(o *vault.TransitOptions) { o.Now = clk.now; o.RequestTimeout = 500 * time.Millisecond })
	both, err := vault.NewRouter(tp, local)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	admin, worker := d.Open(t, store.RoleAdmin), d.Open(t, store.RoleWorker)
	if _, res, err := vault.Activate(t.Context(), admin, worker, both); err != nil || res != "activated" {
		t.Fatalf("Activate the service: %q %v", res, err)
	}
	if err := (&vault.Rewrapper{DB: worker, Provider: both}).Pass(t.Context()); err != nil {
		t.Fatalf("the re-wrap to the service: %v", err)
	}
	if n := d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE kek_version LIKE 'transit-%'`); n != 1 {
		t.Fatalf("%d data keys under the service, want 1", n)
	}

	cache, err := vault.NewUnwrapCache(tp, 5*time.Minute, clk.now)
	if err != nil {
		t.Fatalf("NewUnwrapCache: %v", err)
	}
	r := newRig(t, d, func(rl *Relay) { rl.Provider = cache })
	s.Reset()

	t.Run("ten rounds make one unwrap and one more after the lifetime", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			d.SeedUsageRecords(t, tid, 1)
			if _, err := r.pass(); err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		}
		if got, _ := recv.ids("/t"); len(got) != 10 {
			t.Fatalf("%d events delivered, want 10", len(got))
		}
		if n := s.Count(transittest.OpUnwrap); n != 1 {
			t.Errorf("%d unwraps for ten rounds, want 1", n)
		}
		clk.advance(5*time.Minute + time.Second)
		d.SeedUsageRecords(t, tid, 1)
		if _, err := r.pass(); err != nil {
			t.Fatal(err)
		}
		if n := s.Count(transittest.OpUnwrap); n != 2 {
			t.Errorf("%d unwraps after the lifetime, want 2", n)
		}
	})

	t.Run("with the service away a round fails without consuming an attempt", func(t *testing.T) {
		clk.advance(5*time.Minute + time.Second) // nothing is cached
		before, _ := recv.ids("/t")
		s.Stop()
		d.SeedUsageRecords(t, tid, 2)
		for i := 0; i < 2; i++ {
			_, _ = r.pass()
		}
		after, _ := recv.ids("/t")
		if len(after) != len(before) {
			t.Errorf("%d requests were made without the key", len(after)-len(before))
		}
		row := readDest(t, d, dest)
		if row.Failures != 0 || row.FailingSince != nil || row.LastError != "" || row.LastFailure != nil {
			t.Errorf("an attempt was counted against the destination: %+v", row)
		}
		lines := r.logs.Find(t, "event relay failing")
		if len(lines) == 0 || lines[0]["cause"] == nil || lines[0]["cause"] == "" {
			t.Errorf("the lines %v, want a line that names the cause", lines)
		}
		for _, l := range lines {
			if strings.Contains(strings.ToLower(fmt.Sprint(l["cause"])), "token") {
				t.Errorf("the cause is %v", l["cause"])
			}
		}
		// The service is back: the events are delivered, in order.
		s.Start()
		clk.advance(6 * time.Second) // the refusal of the provider ends
		expireLease(t, d, dest)
		r.drain()
		got, _ := recv.ids("/t")
		want := eventIDs(t, d, tid)
		if len(got) != len(want) {
			t.Fatalf("%d events delivered, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("event %d was delivered as %s, want %s: the order was not kept", i, got[i], want[i])
				break
			}
		}
	})
}
