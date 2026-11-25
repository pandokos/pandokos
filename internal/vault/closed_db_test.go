package vault_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/vaulttest"
)

// TestNoDataKeyForAClosedTenant is T-992: a tenant that is being deleted or is deleted takes no
// data key (2.1 R-9), and an instance whose key-encryption key was replaced cannot create one.
func TestNoDataKeyForAClosedTenant(t *testing.T) {
	t.Parallel()
	w := newVWorld(t)
	worker := w.d.Open(t, store.RoleWorker)

	step := func(tid tenant.ID) string {
		t.Helper()
		var next string
		err := worker.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			var err error
			next, err = queries.New(tx).TenantErasureStep(c, 100)
			return err
		})
		if err != nil {
			t.Fatalf("the erasure step: %v", err)
		}
		return next
	}
	deleting, deleted := w.d.SeedTenant(t, "closing"), w.d.SeedTenant(t, "closed")
	w.d.RequestDeletion(t, deleting, time.Now().Add(-time.Minute))
	if got := step(deleting); got != "access" {
		t.Fatalf("the first step: %q", got)
	}
	w.d.RequestDeletion(t, deleted, time.Now().Add(-time.Minute))
	for i := 0; i < 40; i++ {
		got := step(deleted)
		if got == "settling" {
			w.d.AgeDeletion(t, deleted)
		}
		if got == "done" {
			break
		}
	}
	for name, tid := range map[string]tenant.ID{"being deleted": deleting, "deleted": deleted} {
		err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			_, _, err := w.sealer.EnsureDataKey(c, tx, tid)
			return err
		})
		if _, constraint, ok := store.ConstraintViolation(err); !ok || constraint != "tenant_open" {
			t.Errorf("EnsureDataKey for a tenant %s: %v, want the refusal tenant_open as it is", name, err)
		}
		err = w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
			_, err := w.sealer.Seal(c, tx, tid, "0192f0f0-1111-7000-8000-000000000001", secret.New("synthetic-secret-closed"))
			return err
		})
		if _, constraint, ok := store.ConstraintViolation(err); !ok || constraint != "tenant_open" {
			t.Errorf("Seal for a tenant %s: %v, want the refusal tenant_open", name, err)
		}
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tid.String()); n != 0 {
			t.Errorf("a data key exists for a tenant %s: %d rows", name, n)
		}
	}

	t.Run("a replaced key cannot create a data key", func(t *testing.T) {
		w := newVWorld(t)
		fresh := w.d.SeedTenant(t, "fresh")
		other := vaulttest.Keys(t, 1)[0]
		w.d.ActivateKEK(t, other.Version) // another instance rotated: this sealer's key is no longer the active one
		err := w.admin.WithTenant(ctx(t), fresh, func(c context.Context, tx store.Tx) error {
			_, err := w.sealer.Seal(c, tx, fresh, "0192f0f0-1111-7000-8000-000000000002", secret.New("synthetic-secret-replaced"))
			return err
		})
		var se *vault.SealError
		if !errors.As(err, &se) || se.Cause != "kek_replaced" || !errors.Is(err, vault.ErrUnavailable) {
			t.Fatalf("Seal with a replaced key: %v, want a SealError with the cause kek_replaced, unavailable", err)
		}
		if n := w.d.Count(t, `SELECT count(*)::int FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, fresh.String()); n != 0 {
			t.Errorf("the refused seal left %d data keys", n)
		}
	})

	t.Run("the API answers 503 and names no key", func(t *testing.T) {
		api := apitest.New(t)
		tn, adminKey := api.Tenant(t, apitest.UniqueName("replaced"))
		// The tenant has no data key yet (as a tenant that never held a credential); the key of the
		// instance is then replaced by another instance's rotation.
		if _, err := api.DB.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, tn.ID); err != nil {
			t.Fatal(err)
		}
		other := vaulttest.Keys(t, 1)[0]
		api.DB.ActivateKEK(t, other.Version)
		r := api.Do(t, "POST", "/v1/tenants/"+tn.ID+"/credentials", adminKey,
			map[string]any{"provider": "openai", "label": "main", "secret": "synthetic-secret-api"}, nil)
		if r.Status != 503 || !strings.Contains(string(r.Body), `"code":"unavailable"`) {
			t.Fatalf("a credential with a replaced key: %d %s, want 503 unavailable", r.Status, r.Body)
		}
		if lines := api.Logs.Find(t, "credential cannot be sealed"); len(lines) == 0 || lines[len(lines)-1]["cause"] != "kek_replaced" {
			t.Errorf("the log lines credential cannot be sealed: %v, want the cause kek_replaced", lines)
		}
		for _, text := range []string{string(r.Body), api.Logs.String()} {
			if strings.Contains(text, other.Version) || strings.Contains(text, other.Secret.Reveal()) {
				t.Error("an output names the key or its version")
			}
		}
	})
}
