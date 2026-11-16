package authn_test

import (
	"context"
	"crypto"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
	"github.com/pandokos/pandokos/test/fakeidp"
	"github.com/pandokos/pandokos/test/testutil"
)

// idpKeys is a key source backed by the keys of fake issuers, without HTTP: the source is found
// by its issuer URL.
type idpKeys struct {
	mu   sync.Mutex
	idps map[string]*fakeidp.Server
}

func newIDPKeys(idps ...*fakeidp.Server) *idpKeys {
	k := &idpKeys{idps: map[string]*fakeidp.Server{}}
	for _, s := range idps {
		k.idps[s.Issuer()] = s
	}
	return k
}

func (k *idpKeys) Key(_ context.Context, src jwks.Source, kid, alg string) (crypto.PublicKey, error) {
	k.mu.Lock()
	idp := k.idps[src.Issuer]
	k.mu.Unlock()
	if idp == nil {
		return nil, jwks.ErrKeyNotFound
	}
	if kid == "" {
		return idp.PublicKey(alg), nil
	}
	if pub := idp.PublicKeyByID(kid); pub != nil {
		return pub, nil
	}
	return nil, jwks.ErrKeyNotFound
}

// stepClock is a clock the test moves; it starts at the real time of the test, so that tokens
// signed with real time claims are valid.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStepClock() *stepClock { return &stepClock{t: time.Now().UTC().Truncate(time.Second)} }
func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// tokenRig is a migrated database, a fake issuer, and an authenticator of the gateway role that
// accepts tokens.
type tokenRig struct {
	d    *dbtest.Database
	idp  *fakeidp.Server
	keys *idpKeys
	clk  *stepClock
	auth *authn.Authenticator
	logs *testutil.LogBuffer
}

// newTokenRig builds the authenticator with the given principal cache lifetime (0: none).
func newTokenRig(t *testing.T, d *dbtest.Database, ttl time.Duration, more ...*fakeidp.Server) *tokenRig {
	t.Helper()
	r := &tokenRig{d: d, idp: fakeidp.Start(t, fakeidp.Options{}), clk: newStepClock(), logs: &testutil.LogBuffer{}}
	r.keys = newIDPKeys(append([]*fakeidp.Server{r.idp}, more...)...)
	var err error
	r.auth, err = authn.New(authn.Options{
		DB: d.Open(t, store.RoleGateway), Logger: telemetry.NewLogger(r.logs, "debug"), Kinds: []authn.Kind{authn.KindTenant, authn.KindToken},
		CacheTTL: ttl, Now: r.clk.now,
		Tokens: authn.TokenOptions{Keys: r.keys, MaxBytes: 8192, ClockSkew: 60 * time.Second, MaxLifetime: 24 * time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// token signs a token of the fake issuer at the clock of the rig, for ten minutes.
func (r *tokenRig) token(t *testing.T, alg, sub string, aud any) string {
	t.Helper()
	return r.idp.Sign(alg, r.idp.StandardAt(r.clk.now(), sub, aud, 10*time.Minute))
}

// The helpers of the plan: they write as the owner of the database (the handlers of the
// administration API are not needed to build a world), which fires the same triggers.

// newTrustedIssuer registers the fake issuer for the tenant with the audience and returns the
// ID of the registration.
func newTrustedIssuer(t *testing.T, d *dbtest.Database, tid tenant.ID, srv *fakeidp.Server, audience string) string {
	t.Helper()
	var id string
	err := d.Super(t).QueryRow(ctx(t), `INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
		tid.String(), "idp-"+testutil.RandomUUID()[:8], srv.Issuer(), audience).Scan(&id)
	if err != nil {
		t.Fatalf("trusted issuer: %v", err)
	}
	return id
}

// newPlatformIssuer registers the fake issuer at platform level with the tenant claim and the
// list: tenant ID to the claim value that designates it.
func newPlatformIssuer(t *testing.T, d *dbtest.Database, srv *fakeidp.Server, audience, claim string, list map[tenant.ID]string) string {
	t.Helper()
	var id string
	err := d.Super(t).QueryRow(ctx(t), `INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		"shared-"+testutil.RandomUUID()[:8], srv.Issuer(), audience, claim).Scan(&id)
	if err != nil {
		t.Fatalf("platform issuer: %v", err)
	}
	for tid, value := range list {
		if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, $3)`, id, tid.String(), value); err != nil {
			t.Fatalf("platform issuer tenant: %v", err)
		}
	}
	return id
}

// mapSubject maps the subject of the registration to the service account and returns the ID of
// the mapping.
func mapSubject(t *testing.T, d *dbtest.Database, tid tenant.ID, issuerID string, platform bool, subject, serviceAccountID string) string {
	t.Helper()
	col := "trusted_issuer_id"
	if platform {
		col = "platform_issuer_id"
	}
	var id string
	err := d.Super(t).QueryRow(ctx(t), `INSERT INTO pandokos.subject_mapping (tenant_id, `+col+`, subject, service_account_id) VALUES ($1::uuid, $2::uuid, $3, $4::uuid) RETURNING id::text`,
		tid.String(), issuerID, subject, serviceAccountID).Scan(&id)
	if err != nil {
		t.Fatalf("subject mapping: %v", err)
	}
	return id
}

// newServiceAccount creates a service account with the roles and returns its ID.
func newServiceAccount(t *testing.T, d *dbtest.Database, tid tenant.ID, name string, roles ...string) string {
	t.Helper()
	var id string
	if err := d.Super(t).QueryRow(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, $2) RETURNING id::text`, tid.String(), name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if _, err := d.Super(t).Exec(ctx(t), `INSERT INTO pandokos.role_assignment (tenant_id, service_account_id, role) VALUES ($1::uuid, $2::uuid, $3)`, tid.String(), id, r); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func exec(t *testing.T, d *dbtest.Database, sql string, args ...any) {
	t.Helper()
	if _, err := d.Super(t).Exec(ctx(t), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func newLogger(w *testutil.LogBuffer) *slog.Logger { return telemetry.NewLogger(w, "debug") }
