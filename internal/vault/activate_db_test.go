package vault_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// activationWorld is a migrated database with no key version at all (an installation that never
// started), its admin and worker pools, and a sealer on whichever provider a step uses.
type activationWorld struct {
	d             *dbtest.Database
	admin, worker *store.DB
}

func newActivationWorld(t *testing.T) *activationWorld {
	t.Helper()
	d := dbtest.NewMigrated(t)
	if _, err := d.Super(t).Exec(ctx(t), `DELETE FROM pandokos.kek_version`); err != nil {
		t.Fatal(err)
	}
	return &activationWorld{d: d, admin: d.Open(t, store.RoleAdmin), worker: d.Open(t, store.RoleWorker)}
}

func (w *activationWorld) states(t *testing.T) string {
	t.Helper()
	var s string
	if err := w.d.Super(t).QueryRow(ctx(t), `SELECT COALESCE(string_agg(id || '=' || state, ' ' ORDER BY activated_at, id), '') FROM pandokos.kek_version`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (w *activationWorld) auditRows(t *testing.T) int {
	t.Helper()
	return w.d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action IN ('kek.activate', 'kek.retire')`)
}

// dataKeyFor creates the data key of a new tenant under the active version of the provider, as the
// sealer does when the first credential is written.
func (w *activationWorld) dataKeyFor(t *testing.T, p vault.KeyProvider, name string) {
	t.Helper()
	s, err := vault.NewSealer(p)
	if err != nil {
		t.Fatal(err)
	}
	tid := w.d.SeedTenant(t, name)
	err = w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
		_, _, err := s.EnsureDataKey(c, tx, tid)
		return err
	})
	if err != nil {
		t.Fatalf("a data key under %v: %v", name, err)
	}
}

// TestActivateAtStart is T-988: the start-up of a process with one key, with a new key and the old
// as an unwrap key, with an old key, and with a key that a version in use needs.
func TestActivateAtStart(t *testing.T) {
	k := vaulttest.Keys(t, 3)

	t.Run("an empty database, the same key again, a new key with the old one to unwrap", func(t *testing.T) {
		w := newActivationWorld(t)
		p1 := vaulttest.Provider(t, k[0])
		if v, res, err := vault.Activate(ctx(t), w.admin, w.worker, p1); err != nil || v != k[0].Version || res != "activated" {
			t.Fatalf("an empty database: %q %q %v; want %s activated", v, res, err, k[0].Version)
		}
		if got := w.states(t); got != k[0].Version+"=active" {
			t.Errorf("the states: %s", got)
		}
		rows := w.auditRows(t)
		if v, res, err := vault.Activate(ctx(t), w.admin, w.worker, p1); err != nil || v != k[0].Version || res != "unchanged" {
			t.Errorf("the same key again: %q %q %v; want unchanged", v, res, err)
		}
		if got := w.auditRows(t); got != rows {
			t.Errorf("an unchanged activation wrote an audit row (%d, was %d)", got, rows)
		}
		w.dataKeyFor(t, p1, "tenant-one")

		p2 := vaulttest.Provider(t, k[1], k[0])
		if v, res, err := vault.Activate(ctx(t), w.admin, w.worker, p2); err != nil || v != k[1].Version || res != "activated" {
			t.Fatalf("a new key with the old one to unwrap: %q %q %v", v, res, err)
		}
		if got := w.states(t); !strings.Contains(got, k[0].Version+"=retiring") || !strings.Contains(got, k[1].Version+"=active") {
			t.Errorf("the states after the rotation: %s, want the old version retiring and the new one active", got)
		}
	})

	t.Run("the old key as the key file afterwards is refused and nothing is written", func(t *testing.T) {
		w := newActivationWorld(t)
		old := vaulttest.Provider(t, k[0])
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, old); err != nil {
			t.Fatal(err)
		}
		w.dataKeyFor(t, old, "tenant-one") // the old version is in use: it stays retiring
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[1], k[0])); err != nil {
			t.Fatal(err)
		}
		before, rows := w.states(t), w.auditRows(t)
		// The old key wraps again, the new one only unwraps: the version configured was replaced.
		_, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[0], k[1]))
		if !errors.Is(err, vault.ErrKeyReplaced) {
			t.Fatalf("the old key as the key file: %v, want ErrKeyReplaced", err)
		}
		for _, v := range []string{k[0].Version, k[1].Version} {
			if !strings.Contains(err.Error(), v) {
				t.Errorf("the error does not name the version %s: %v", v, err)
			}
		}
		if got := w.states(t); got != before {
			t.Errorf("a refused activation changed the states: %s, was %s", got, before)
		}
		if got := w.auditRows(t); got != rows {
			t.Errorf("a refused activation wrote an audit row")
		}
		// With the old key alone the active version is not provided: the check refuses, as before.
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, old); err == nil || !strings.Contains(err.Error(), k[1].Version) {
			t.Errorf("the old key alone: %v, want a refusal that names the active version %s", err, k[1].Version)
		}
	})

	t.Run("a retiring version with data keys and no key file fails the check", func(t *testing.T) {
		w := newActivationWorld(t)
		old := vaulttest.Provider(t, k[0])
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, old); err != nil {
			t.Fatal(err)
		}
		w.dataKeyFor(t, old, "tenant-one")
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[1], k[0])); err != nil {
			t.Fatal(err)
		}
		_, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[1]))
		if err == nil || !strings.Contains(err.Error(), k[0].Version) || !strings.Contains(err.Error(), "which this configuration does not provide") {
			t.Errorf("a retiring version in use with no key: %v, want the check's refusal naming %s", err, k[0].Version)
		}
		if got := w.states(t); !strings.Contains(got, k[0].Version+"=retiring") {
			t.Errorf("the states: %s: the version in use was retired", got)
		}
	})

	t.Run("a retiring version without data keys and no key file is retired first, and a retired one is not needed", func(t *testing.T) {
		w := newActivationWorld(t)
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[0])); err != nil {
			t.Fatal(err)
		}
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[1], k[0])); err != nil {
			t.Fatal(err)
		}
		if got := w.states(t); !strings.Contains(got, k[0].Version+"=retiring") {
			t.Fatalf("the states before: %s", got)
		}
		v, res, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[1]))
		if err != nil || v != k[1].Version || res != "unchanged" {
			t.Fatalf("the start without the old key: %q %q %v", v, res, err)
		}
		if got := w.states(t); got != k[0].Version+"=retired "+k[1].Version+"=active" {
			t.Errorf("the states: %s, want the old version retired", got)
		}
		if got := w.d.Count(t, `SELECT count(*)::int FROM pandokos.platform_audit_event WHERE action = 'kek.retire' AND resource_id = $1`, k[0].Version); got != 1 {
			t.Errorf("%d kek.retire rows, want 1", got)
		}
		// A retired version is not needed again: another start without it passes.
		if _, res, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[1])); err != nil || res != "unchanged" {
			t.Errorf("a second start: %q %v", res, err)
		}
		// And a retired version never comes back as the key file.
		if _, _, err := vault.Activate(ctx(t), w.admin, w.worker, vaulttest.Provider(t, k[0], k[1])); !errors.Is(err, vault.ErrKeyReplaced) {
			t.Errorf("a retired key as the key file: %v, want ErrKeyReplaced", err)
		}
	})
}
