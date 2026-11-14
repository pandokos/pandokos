package vault_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/testutil"
	"github.com/pandokos/pandokos/test/vaulttest"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

// vworld is a migrated database with the admin and gateway pools and a sealer on one local
// provider.
type vworld struct {
	d      *dbtest.Database
	admin  *store.DB
	gw     *store.DB
	prov   vault.KeyProvider
	sealer *vault.Sealer
}

func newVWorld(t *testing.T) *vworld {
	t.Helper()
	return newVWorldOn(t, dbtest.NewMigrated(t))
}

func newVWorldOn(t *testing.T, d *dbtest.Database) *vworld {
	t.Helper()
	w := &vworld{d: d, admin: d.Open(t, store.RoleAdmin), gw: d.Open(t, store.RoleGateway), prov: vaulttest.NewKeyProvider(t)}
	var err error
	if w.sealer, err = vault.NewSealer(w.prov); err != nil {
		t.Fatal(err)
	}
	return w
}

// createCredential seals plaintext and inserts the credential as the admin role would, in the
// session of the tenant; the first credential of a provider becomes its default (the statement
// decides). It returns the ID.
func (w *vworld) createCredential(t *testing.T, sealer *vault.Sealer, tid tenant.ID, provider, label, plaintext string) string {
	t.Helper()
	var id string
	err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		var err error
		if id, err = q.NewCredentialID(c); err != nil {
			return err
		}
		sealed, err := sealer.Seal(c, tx, tid, id, secret.New(plaintext))
		if err != nil {
			return err
		}
		_, err = q.CreateCredential(c, queries.CreateCredentialParams{
			TenantID: tid.String(), ID: id, Provider: provider, Label: label, KeyVersion: sealed.KeyVersion,
			Secret: sealed.Secret, Fingerprint: sealed.Fingerprint, Hint: sealed.Hint,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create the credential %s: %v", label, err)
	}
	return id
}

// setState changes the state of a credential as the admin role does.
func (w *vworld) setState(t *testing.T, tid tenant.ID, id, state string) {
	t.Helper()
	err := w.admin.WithTenant(ctx(t), tid, func(c context.Context, tx store.Tx) error {
		_, err := queries.New(tx).SetCredentialState(c, queries.SetCredentialStateParams{State: state, ChangedBy: "tenant", ID: id})
		return err
	})
	if err != nil {
		t.Fatalf("set the state of %s to %s: %v", id, state, err)
	}
}

// opener returns an Opener on the gateway pool with a debug log in logs.
func (w *vworld) opener(t *testing.T, logs *testutil.LogBuffer, prov vault.KeyProvider, credTTL, dekTTL time.Duration, now func() time.Time) *vault.Opener {
	t.Helper()
	o, err := vault.NewOpener(vault.OpenerOptions{
		DB: w.gw, Provider: prov, Logger: newLogger(logs), Now: now, CredentialTTL: credTTL, DataKeyTTL: dekTTL,
	})
	if err != nil {
		t.Fatalf("NewOpener: %v", err)
	}
	return o
}

func newLogger(logs *testutil.LogBuffer) *slog.Logger { return telemetry.NewLogger(logs, "debug") }

// principalFor is an inference service account of the tenant, with its key pinned to pin when
// that is not empty.
func principalFor(tid tenant.ID, pin string) authz.Principal {
	return authz.Principal{Kind: authz.KindServiceAccount, ID: "0192f0f0-2222-7000-8000-000000000001", Name: "caller", KeyID: "tabcdefghijklmnop",
		TenantID: tid, TenantName: "t", TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}, CredentialID: pin}
}

func open(t *testing.T, o *vault.Opener, p authz.Principal, provider string) (vault.Opened, error) {
	t.Helper()
	return o.Open(ctx(t), vault.Use{Principal: p, RequestID: "req-db-0001", Provider: provider})
}
