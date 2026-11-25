package authn_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// TestTokenOnAdminPool is the change of the authn token tests in sprint 4.2: checkTokens accepts
// the pool of the admin role (tokens.admin_listener), the pool of the worker role is still refused,
// and a token yields the same service account principal as on the gateway.
func TestTokenOnAdminPool(t *testing.T) {
	t.Parallel()
	d := dbtest.NewMigrated(t)
	idp := fakeidp.Start(t, fakeidp.Options{})
	clk := newStepClock()
	opts := func(db *store.DB) authn.Options {
		return authn.Options{
			DB: db, Logger: telemetry.NewLogger(&testutil.LogBuffer{}, "debug"), Kinds: []authn.Kind{authn.KindTenant, authn.KindPlatform, authn.KindToken}, Now: clk.now,
			Tokens: authn.TokenOptions{Keys: newIDPKeys(idp), MaxBytes: 8192, ClockSkew: 60 * time.Second, MaxLifetime: 24 * time.Hour},
		}
	}
	if _, err := authn.New(opts(d.Open(t, store.RoleWorker))); err == nil {
		t.Error("tokens are accepted on the pool of the worker role")
	}
	auth, err := authn.New(opts(d.Open(t, store.RoleAdmin)))
	if err != nil {
		t.Fatalf("tokens on the pool of the admin role: %v", err)
	}

	f := newFixture(t, d, "adminbound")
	const aud = "pandokos-adminbound"
	issuerID := newTrustedIssuer(t, d, f.tenant, idp, aud)
	mappingID := mapSubject(t, d, f.tenant, issuerID, false, "workload-1", f.sa)
	tok := idp.Sign("ES256", idp.StandardAt(clk.now(), "workload-1", aud, 10*time.Minute))
	p, err := auth.Authenticate(ctx(t), tok)
	if err != nil {
		t.Fatalf("a mapped subject on the admin pool: %v", err)
	}
	if p.Kind != authz.KindServiceAccount || p.ID != f.sa || p.KeyID != mappingID || p.IssuerID != issuerID || p.Subject != "workload-1" ||
		p.TenantID != f.tenant || strings.Join(p.Roles, ",") != "inference,tenant-admin" {
		t.Errorf("principal %+v", p)
	}
	if _, err := auth.Authenticate(ctx(t), idp.Sign("ES256", idp.StandardAt(clk.now(), "stranger", aud, 10*time.Minute))); err == nil {
		t.Error("an unmapped subject was accepted")
	}
}
