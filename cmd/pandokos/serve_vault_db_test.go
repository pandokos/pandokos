package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/internal/vault/transittest"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// stepper is the clock of the start sequence: Sleep returns at once and moves the time, after
// calling the hook of the test.
type stepper struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	hook   func(n int) // called with the number of sleeps so far, before Sleep returns
}

func newStepper() *stepper { return &stepper{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)} }

func (c *stepper) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepper) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	n, hook := len(c.sleeps), c.hook
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return ctx.Err()
}

func (c *stepper) waited() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func (c *stepper) elapsed(start time.Time) time.Duration { return c.Now().Sub(start) }

// startRig is a database with no key version, a double with a key and a transit provider that is
// built and not started.
type startRig struct {
	d     *dbtest.Database
	admin *store.DB
	work  *store.DB
	s     *transittest.Server
	f     vaulttest.TransitFiles
	tp    *vault.TransitProvider
	logs  *testutil.LogBuffer
	clk   *stepper
}

func newStartRig(t *testing.T, setup vaulttest.TransitSetup) *startRig {
	t.Helper()
	d := dbtest.NewMigrated(t)
	if _, err := d.Super(t).Exec(context.Background(), `DELETE FROM pandokos.kek_version`); err != nil {
		t.Fatal(err)
	}
	r := &startRig{d: d, admin: d.Open(t, store.RoleAdmin), work: d.Open(t, store.RoleWorker), s: transittest.New(t), logs: &testutil.LogBuffer{}, clk: newStepper()}
	r.f = vaulttest.Transit(t, r.s, setup)
	r.tp = r.provider(t)
	return r
}

func (r *startRig) provider(t *testing.T) *vault.TransitProvider {
	t.Helper()
	o, g := vaulttest.TransitOptions(t, r.s, r.f, func(o *vault.TransitOptions) {
		o.Logger = telemetry.NewLogger(r.logs, "debug")
		o.RequestTimeout = 300 * time.Millisecond
	})
	t.Cleanup(g.CloseIdleConnections)
	p, err := vault.NewTransitProvider(o)
	if err != nil {
		t.Fatalf("NewTransitProvider: %v", err)
	}
	return p
}

func (r *startRig) options(tp *vault.TransitProvider, p vault.KeyProvider, timeout time.Duration) startVaultOptions {
	return startVaultOptions{Transit: tp, Provider: p, Admin: r.admin, Worker: r.work, Logger: telemetry.NewLogger(r.logs, "debug"),
		StartTimeout: timeout, Now: r.clk.Now, Sleep: r.clk.Sleep}
}

// TestServeWaitsForKeyProvider is T-1081: the start sequence of the design with a test clock.
func TestServeWaitsForKeyProvider(t *testing.T) {
	bg := context.Background()
	d15 := 15 * time.Second

	t.Run("unavailable: it waits with the backoff and one warning per 30 s, then proceeds", func(t *testing.T) {
		r := newStartRig(t, vaulttest.TransitSetup{})
		r.s.Stop()
		start := r.clk.Now()
		r.clk.hook = func(n int) {
			if n == 8 {
				r.s.Start() // the service is back after eight waits
			}
		}
		version, activation, err := startVault(bg, r.options(r.tp, r.tp, 0))
		if err != nil || activation != "activated" || version != r.tp.Info().Version || !strings.HasPrefix(version, "transit-") {
			t.Fatalf("startVault = %q %q %v", version, activation, err)
		}
		want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, d15, d15, d15, d15}
		got := r.clk.waited()
		if len(got) != len(want) {
			t.Fatalf("the waits are %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("the waits are %v, want %v", got, want)
				break
			}
		}
		// Nine attempts over 75 s of the clock, a warning at 0 s, 30 s and 60 s.
		lines := r.logs.Find(t, "key provider unavailable")
		if len(lines) != 3 {
			t.Fatalf("%d lines key provider unavailable for 75 s of outage, want 3 (one per 30 s): %v", len(lines), lines)
		}
		prev := float64(0)
		for _, l := range lines {
			if l["level"] != "WARN" || l["phase"] != "start" || l["kek_provider"] != "transit" || l["operation"] == nil || l["error_class"] == nil || l["failures"] == nil {
				t.Errorf("the line is %v", l)
			}
			if f, ok := l["failures"].(float64); !ok || f <= prev {
				t.Errorf("failures do not grow: %v after %v", l["failures"], prev)
			} else {
				prev = f
			}
		}
		rec := r.logs.Find(t, "key provider recovered")
		if len(rec) != 1 || rec[0]["phase"] != "start" || rec[0]["kek_provider"] != "transit" {
			t.Fatalf("the lines key provider recovered: %v", rec)
		}
		if secs, ok := rec[0]["outage_seconds"].(float64); !ok || secs < 60 || secs > 90 {
			t.Errorf("outage_seconds = %v, want about the 75 s of the clock", rec[0]["outage_seconds"])
		}
		if e := r.clk.elapsed(start); e != 75*time.Second {
			t.Errorf("%v of the clock passed", e)
		}
		if got := r.d.Count(t, `SELECT count(*)::int FROM pandokos.kek_version WHERE id = $1 AND state = 'active'`, version); got != 1 {
			t.Errorf("the version is not active in the database")
		}
	})

	t.Run("the service answers at once: no wait, no line", func(t *testing.T) {
		r := newStartRig(t, vaulttest.TransitSetup{})
		if _, _, err := startVault(bg, r.options(r.tp, r.tp, 0)); err != nil {
			t.Fatal(err)
		}
		if w := r.clk.waited(); len(w) != 0 {
			t.Errorf("waits %v with the service up", w)
		}
		if n := len(r.logs.Find(t, "key provider unavailable")) + len(r.logs.Find(t, "key provider recovered")); n != 0 {
			t.Errorf("%d lines about an outage that did not happen", n)
		}
		if len(r.logs.Find(t, "key provider ready")) != 1 {
			t.Errorf("no line key provider ready")
		}
	})

	t.Run("start_timeout passed: the error", func(t *testing.T) {
		r := newStartRig(t, vaulttest.TransitSetup{})
		r.s.Stop()
		start := r.clk.Now()
		_, _, err := startVault(bg, r.options(r.tp, r.tp, 20*time.Second))
		if err == nil || !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Fatalf("after the start timeout: %v, want an error that is ErrProviderUnavailable", err)
		}
		if e := r.clk.elapsed(start); e < 20*time.Second || e > 20*time.Second+d15 {
			t.Errorf("it gave up after %v of the clock, want 20 s to 35 s", e)
		}
	})

	t.Run("a stop signal ends the wait", func(t *testing.T) {
		r := newStartRig(t, vaulttest.TransitSetup{})
		r.s.Stop()
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		r.clk.hook = func(n int) {
			if n == 3 {
				cancel()
			}
		}
		_, _, err := startVault(ctx, r.options(r.tp, r.tp, 0))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("after a stop signal: %v, want context.Canceled", err)
		}
		if n := len(r.clk.waited()); n != 3 {
			t.Errorf("%d waits, want it to end at the third", n)
		}
	})

	causes := []struct {
		name  string
		setup vaulttest.TransitSetup
		fault func(r *startRig)
	}{
		{"a refused token", vaulttest.TransitSetup{}, func(r *startRig) { r.s.Namespace("").RevokeToken(r.f.Token) }},
		{"a missing key", vaulttest.TransitSetup{}, func(r *startRig) { r.s.Namespace("").DeleteKey(r.f.Mount, r.f.Key) }},
		{"a key of another type", vaulttest.TransitSetup{KeyOptions: transittest.KeyOptions{Type: "rsa-2048"}}, func(*startRig) {}},
		{"associated data not bound", vaulttest.TransitSetup{}, func(r *startRig) { r.s.IgnoreAssociatedData() }},
		{"a login refused", vaulttest.TransitSetup{Method: "approle"}, func(r *startRig) {
			r.s.Namespace("").NewSecretID(r.f.AppRoleMount, r.f.RoleID)
		}},
	}
	for _, c := range causes {
		t.Run("a permanent cause ends at once: "+c.name, func(t *testing.T) {
			r := newStartRig(t, c.setup)
			c.fault(r)
			_, _, err := startVault(bg, r.options(r.tp, r.tp, 0))
			if err == nil {
				t.Fatal("startVault succeeded")
			}
			if errors.Is(err, vault.ErrProviderUnavailable) {
				t.Errorf("a permanent cause is reported as unavailable: %v", err)
			}
			if w := r.clk.waited(); len(w) != 0 {
				t.Errorf("it waited %v for a cause that only a person can repair", w)
			}
			if got := r.d.Count(t, `SELECT count(*)::int FROM pandokos.kek_version`); got != 0 {
				t.Errorf("%d key versions were written by a start that failed", got)
			}
			if n := len(r.logs.Find(t, "key provider unavailable")); n != 0 {
				t.Errorf("%d lines key provider unavailable for a permanent cause", n)
			}
		})
	}

	t.Run("a replaced version and a version nobody provides end at once", func(t *testing.T) {
		r := newStartRig(t, vaulttest.TransitSetup{})
		k := vaulttest.Keys(t, 1)
		old := vaulttest.Provider(t, k[0])
		if _, _, err := vault.Activate(bg, r.admin, r.work, old); err != nil {
			t.Fatal(err)
		}
		sealer, err := vault.NewSealer(old)
		if err != nil {
			t.Fatal(err)
		}
		tid := r.d.SeedTenant(t, "needs-the-old-key")
		if err := r.admin.WithTenant(bg, tid, func(c context.Context, tx store.Tx) error {
			_, _, err := sealer.EnsureDataKey(c, tx, tid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		both, err := vault.NewRouter(r.tp, old)
		if err != nil {
			t.Fatal(err)
		}
		if _, act, err := startVault(bg, r.options(r.tp, both, 0)); err != nil || act != "activated" {
			t.Fatalf("the move to the service: %q %v", act, err)
		}
		// The old key file alone: its version was replaced.
		_, _, err = startVault(bg, r.options(nil, old, 0))
		if !errors.Is(err, vault.ErrKeyReplaced) {
			t.Errorf("the replaced key file: %v, want ErrKeyReplaced", err)
		}
		// The service alone while a data key is still under the old key file: the version is not provided.
		tp2 := r.provider(t)
		_, _, err = startVault(bg, r.options(tp2, tp2, 0))
		if err == nil || errors.Is(err, vault.ErrProviderUnavailable) || !strings.Contains(err.Error(), k[0].Version) {
			t.Errorf("a needed version that is not provided: %v, want an error that names %s", err, k[0].Version)
		}
		if w := r.clk.waited(); len(w) != 0 {
			t.Errorf("it waited %v", w)
		}
	})
}
