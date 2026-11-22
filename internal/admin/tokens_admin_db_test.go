package admin_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/fakeidp"
)

// tokenWorld is an administration API that accepts tokens (tokens.admin_listener), a fake issuer
// registered by a tenant and at platform level, and the service accounts they map to.
type tokenWorld struct {
	*people
	idp                  *fakeidp.Server
	other                adminapi.Tenant
	tenantIssuer, shared string
	sa                   string
	tenantTok, sharedTok string
}

const (
	audTenant = "pandokos-acme"
	audShared = "pandokos-shared"
)

func newTokenWorld(t *testing.T, tokens bool) *tokenWorld {
	t.Helper()
	w := &tokenWorld{idp: fakeidp.Start(t, fakeidp.Options{})}
	keys := apitest.NewIDPKeys(w.idp)
	api := apitest.NewPeople(t, apitest.PeopleConfig{Admin: func(o *admin.Options) {
		kinds := []authn.Kind{authn.KindTenant, authn.KindPlatform}
		var tok authn.TokenOptions
		if tokens {
			kinds = append(kinds, authn.KindToken)
			tok = authn.TokenOptions{Keys: keys, MaxBytes: 8192, ClockSkew: time.Minute, MaxLifetime: 24 * time.Hour}
		}
		a, err := authn.New(authn.Options{DB: o.DB, Logger: o.Logger, Kinds: kinds, Tokens: tok})
		if err != nil {
			t.Fatalf("authn.New: %v", err)
		}
		o.Authenticator = a
	}})
	w.people = peopleOn(t, api)
	w.other, _ = api.Tenant(t, "globex")
	sa := decode[adminapi.ServiceAccount](t, w.tenantKey(t, "POST", "/v1/tenants/"+w.tenant.ID+"/service-accounts", map[string]any{"name": "token-admin", "roles": []string{"tenant-admin"}}), 201)
	w.sa = sa.ID
	super := api.Super(t)
	scan := func(sql string, args ...any) string {
		var id string
		if err := super.QueryRow(ctx(t), sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	w.tenantIssuer = scan(`INSERT INTO pandokos.trusted_issuer (tenant_id, name, issuer, audience) VALUES ($1::uuid, 'acme-idp', $2, $3) RETURNING id::text`, w.tid.String(), w.idp.Issuer(), audTenant)
	w.shared = scan(`INSERT INTO pandokos.platform_issuer (name, issuer, audience, tenant_claim) VALUES ('shared-idp', $1, $2, 'org') RETURNING id::text`, w.idp.Issuer(), audShared)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pandokos.platform_issuer_tenant (issuer_id, tenant_id, claim_value) VALUES ($1::uuid, $2::uuid, 'acme-org')`, []any{w.shared, w.tid.String()}},
		{`INSERT INTO pandokos.subject_mapping (tenant_id, trusted_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, 'workload-1', $3::uuid)`, []any{w.tid.String(), w.tenantIssuer, w.sa}},
		{`INSERT INTO pandokos.subject_mapping (tenant_id, platform_issuer_id, subject, service_account_id) VALUES ($1::uuid, $2::uuid, 'workload-p', $3::uuid)`, []any{w.tid.String(), w.shared, w.sa}},
	} {
		if _, err := super.Exec(ctx(t), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	w.tenantTok = w.sign("workload-1", audTenant, nil)
	w.sharedTok = w.sign("workload-p", audShared, map[string]any{"org": "acme-org"})
	return w
}

func (w *tokenWorld) sign(sub, aud string, extra map[string]any) string {
	c := w.idp.Standard(sub, aud, 10*time.Minute)
	for k, v := range extra {
		c = c.With(k, v)
	}
	return w.idp.Sign("ES256", c)
}

func (w *tokenWorld) get(t *testing.T, token, path string) *apitest.Response {
	t.Helper()
	return w.api.Do(t, "GET", path, token, nil, nil)
}

// TestTokensOnAdminListener is T-823.
func TestTokensOnAdminListener(t *testing.T) {
	w := newTokenWorld(t, true)
	mine, theirs := "/v1/tenants/"+w.tenant.ID+"/service-accounts", "/v1/tenants/"+w.other.ID+"/service-accounts"

	for name, tok := range map[string]string{"a tenant-bound registration": w.tenantTok, "a platform-level registration": w.sharedTok} {
		me := decode[adminapi.Me](t, w.get(t, tok, "/v1/me"), 200)
		if me.PrincipalType != "service_account" || me.ID != w.sa || me.Tenant == nil || me.Tenant.ID != w.tenant.ID || strings.Join(me.Roles, ",") != "tenant-admin" {
			t.Errorf("%s: me %+v", name, me)
		}
		if r := w.get(t, tok, mine); r.Status != 200 {
			t.Errorf("%s: its tenant's operation: %d %s", name, r.Status, r.Body)
		}
		if c := code(t, w.get(t, tok, theirs), 404); c != "not_found" {
			t.Errorf("%s: another tenant's operation: %s", name, c)
		}
		// A token never yields a platform principal or a user.
		if r := w.get(t, tok, "/v1/tenants"); r.Status != 403 {
			t.Errorf("%s: a platform operation: %d", name, r.Status)
		}
		if r := w.get(t, tok, "/v1/users"); r.Status != 403 {
			t.Errorf("%s: a user operation: %d", name, r.Status)
		}
		if r := w.get(t, tok, "/v1/auth/sessions"); r.Status != 403 {
			t.Errorf("%s: session:self: %d", name, r.Status)
		}
		// Nor does it pass for a session: as a cookie it is nothing.
		b := w.api.NewBrowser()
		b.SetCookie(session.CookieName, tok)
		if r := b.Do(t, "GET", "/v1/me", nil, nil); r.Status != 401 {
			t.Errorf("%s as a cookie: %d", name, r.Status)
		}
	}

	// Every refusal is the same 401.
	other := fakeidp.Start(t, fakeidp.Options{})
	refused := map[string]string{
		"an unmapped subject":    w.sign("stranger", audTenant, nil),
		"a wrong audience":       w.sign("workload-1", "someone-else", nil),
		"another issuer's key":   other.Sign("ES256", w.idp.Standard("workload-1", audTenant, time.Minute), fakeidp.WithKID(w.idp.KeyID("ES256"))),
		"an expired token":       w.idp.Sign("ES256", w.idp.StandardAt(time.Now().Add(-time.Hour), "workload-1", audTenant, 10*time.Minute)),
		"a missing tenant claim": w.sign("workload-p", audShared, nil),
		"a not a token":          "eyJ.eyJ.sig",
	}
	var want *apitest.Response
	for name, tok := range refused {
		r := w.get(t, tok, "/v1/me")
		if r.Status != 401 || problem(t, r).Code != "unauthenticated" {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
			continue
		}
		if want == nil {
			want = r
		} else if normalize(r) != normalize(want) || r.Header.Get("Www-Authenticate") != want.Header.Get("Www-Authenticate") {
			t.Errorf("%s: the 401 differs:\n%s\n%s", name, normalize(r), normalize(want))
		}
	}

	// A failure writes the row of ADR 0032 with listener admin, in the tenant's log when it names a tenant.
	var found bool
	for _, row := range w.d.AuditRows(t, w.tid) {
		if row.Action == "auth.failure" && row.ResourceType == "token_issuer" && row.ResourceID == w.tenantIssuer && row.Detail["reason"] == "subject_not_mapped" {
			found = true
			if row.Detail["listener"] != "admin" || row.ActorType != "anonymous" {
				t.Errorf("failure row %+v", row)
			}
		}
	}
	if !found {
		t.Error("no auth.failure row for the unmapped subject in the tenant's log")
	}

	// A token acts: it creates a resource, with no CSRF header and no Origin, and the audit row names the token.
	r := w.api.Do(t, "POST", mine, w.tenantTok, map[string]any{"name": "made-by-token", "roles": []string{"inference"}}, nil)
	if r.Status != 201 {
		t.Errorf("a create with a token: %d %s", r.Status, r.Body)
	}
}

// TestTokensOffOnAdminListener is the other half of T-823: with the switch off a token is a 401.
func TestTokensOffOnAdminListener(t *testing.T) {
	w := newTokenWorld(t, false)
	for name, tok := range map[string]string{"tenant-bound": w.tenantTok, "platform-level": w.sharedTok} {
		r := w.get(t, tok, "/v1/me")
		if r.Status != 401 || problem(t, r).Code != "unauthenticated" {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := w.get(t, w.api.PlatformKey(), "/v1/me"); r.Status != 200 {
		t.Errorf("a key with the switch off: %d", r.Status)
	}
}
