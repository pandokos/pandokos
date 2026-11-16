package queries_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// issuerStatements is the part of T-230 (and T-393) that covers the 28 statements of sprint 4.1:
// the tenant-bound and platform-level registrations, the tenant list, the mappings, and the
// authentication statements the gateway role runs. Each is called through a store helper in the
// session it is written for, directly and through the pooler; values that go in come back unchanged.
func issuerStatements(c context.Context, t *testing.T, admin, gateway *store.DB, tid tenant.ID, tenantID, serviceAccountID string, called map[string]bool) {
	t.Helper()
	mark := func(name string) { called[name] = true }
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	required := `{"tier": "gold"}`
	sameJSON := func(what, got, want string) {
		t.Helper()
		if strings.ReplaceAll(got, " ", "") != strings.ReplaceAll(want, " ", "") {
			t.Errorf("%s: required_claims came back as %s, want %s", what, got, want)
		}
	}

	// A tenant-bound registration, in the tenant's session.
	var issuer queries.CreateTrustedIssuerRow
	var mappingTenant queries.CreateSubjectMappingRow
	must("tenant-bound registrations", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CreateTrustedIssuer")
		var err error
		issuer, err = q.CreateTrustedIssuer(c, queries.CreateTrustedIssuerParams{TenantID: tenantID, Name: "idp-uni", Description: "d, é", Issuer: "https://idp-4-1.example.test/realms/é",
			Audience: "aud ünï", JwksUri: "https://idp-4-1.example.test/keys?v=1", Algorithms: []string{"ES256", "EdDSA"}, SubjectClaim: "azp", RequiredClaims: required, Enabled: false})
		if err != nil {
			return err
		}
		if issuer.Name != "idp-uni" || issuer.TenantID != tenantID || issuer.Audience != "aud ünï" || issuer.Enabled || !reflect.DeepEqual(issuer.Algorithms, []string{"ES256", "EdDSA"}) ||
			issuer.SubjectClaim != "azp" || issuer.JwksUri != "https://idp-4-1.example.test/keys?v=1" || len(issuer.ID) != 36 || issuer.CreatedAt.IsZero() {
			t.Errorf("CreateTrustedIssuer: %+v", issuer)
		}
		sameJSON("CreateTrustedIssuer", issuer.RequiredClaims, required)

		mark("GetTrustedIssuer")
		got, err := q.GetTrustedIssuer(c, issuer.ID)
		if err != nil || got.ID != issuer.ID || got.Name != issuer.Name || got.Issuer != issuer.Issuer || !reflect.DeepEqual(got.Algorithms, issuer.Algorithms) {
			t.Errorf("GetTrustedIssuer: %+v %v", got, err)
		}
		if _, err := q.GetTrustedIssuer(c, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetTrustedIssuer of an ID that does not exist: %v", err)
		}
		mark("LockTrustedIssuer")
		if id, err := q.LockTrustedIssuer(c, issuer.ID); err != nil || id != issuer.ID {
			t.Errorf("LockTrustedIssuer: %q %v", id, err)
		}
		mark("UpdateTrustedIssuer")
		desc, aud, enabled := "changed", "aud-2", true
		upd, err := q.UpdateTrustedIssuer(c, queries.UpdateTrustedIssuerParams{ID: issuer.ID, Description: &desc, Audience: &aud, Enabled: &enabled,
			Algorithms: []string{"RS256"}})
		if err != nil {
			return err
		}
		if upd.Description != "changed" || upd.Audience != "aud-2" || !upd.Enabled || !reflect.DeepEqual(upd.Algorithms, []string{"RS256"}) || upd.Name != issuer.Name || upd.Issuer != issuer.Issuer ||
			upd.SubjectClaim != "azp" || upd.JwksUri != issuer.JwksUri {
			t.Errorf("UpdateTrustedIssuer: %+v (members that were not given must stay)", upd)
		}
		sameJSON("UpdateTrustedIssuer", upd.RequiredClaims, required)
		upd, err = q.UpdateTrustedIssuer(c, queries.UpdateTrustedIssuerParams{ID: issuer.ID, Algorithms: []string{"ES256", "EdDSA"}})
		if err != nil || upd.Description != "changed" || upd.Audience != "aud-2" {
			t.Errorf("UpdateTrustedIssuer with nothing but arrays: %+v %v", upd, err)
		}

		mark("ListTrustedIssuers")
		second, err := q.CreateTrustedIssuer(c, queries.CreateTrustedIssuerParams{TenantID: tenantID, Name: "other-idp", Issuer: "https://idp-4-1b.example.test", Audience: "aud",
			Algorithms: []string{"RS256", "ES256"}, SubjectClaim: "sub", RequiredClaims: `{}`, Enabled: true})
		if err != nil {
			return err
		}
		rows, err := q.ListTrustedIssuers(c, queries.ListTrustedIssuersParams{Sort: "created_at", AfterID: zeroID, PageSize: 10})
		if err != nil || len(rows) != 2 || rows[0].ID != issuer.ID || rows[1].ID != second.ID {
			t.Errorf("ListTrustedIssuers: %+v %v", rows, err)
		}
		rows, err = q.ListTrustedIssuers(c, queries.ListTrustedIssuersParams{Sort: "-name", AfterID: zeroID, PageSize: 10})
		if err != nil || len(rows) != 2 || rows[0].Name != "other-idp" {
			t.Errorf("ListTrustedIssuers by -name: %+v %v", rows, err)
		}
		rows, err = q.ListTrustedIssuers(c, queries.ListTrustedIssuersParams{Sort: "name", After: true, AfterName: "idp-uni", AfterID: issuer.ID, PageSize: 10})
		if err != nil || len(rows) != 1 || rows[0].ID != second.ID {
			t.Errorf("ListTrustedIssuers after a cursor: %+v %v", rows, err)
		}
		rows, err = q.ListTrustedIssuers(c, queries.ListTrustedIssuersParams{Sort: "created_at", Name: "other-idp", Enabled: "true", AfterID: zeroID, PageSize: 10})
		if err != nil || len(rows) != 1 || rows[0].ID != second.ID {
			t.Errorf("ListTrustedIssuers with filters: %+v %v", rows, err)
		}
		rows, err = q.ListTrustedIssuers(c, queries.ListTrustedIssuersParams{Sort: "created_at", Enabled: "false", AfterID: zeroID, PageSize: 10})
		if err != nil || len(rows) != 0 {
			t.Errorf("ListTrustedIssuers enabled=false: %+v %v", rows, err)
		}
		// The delete, and the count it reports, are called below with a mapping in place; here the
		// second registration, which has none.
		mark("CountSubjectMappingsOfIssuer")
		if n, err := q.CountSubjectMappingsOfIssuer(c, second.ID); err != nil || n != 0 {
			t.Errorf("CountSubjectMappingsOfIssuer: %d %v", n, err)
		}
		mark("DeleteTrustedIssuer")
		del, err := q.DeleteTrustedIssuer(c, second.ID)
		if err != nil || del.ID != second.ID || del.Name != "other-idp" {
			t.Errorf("DeleteTrustedIssuer: %+v %v", del, err)
		}
		if _, err := q.DeleteTrustedIssuer(c, second.ID); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("a second DeleteTrustedIssuer: %v", err)
		}

		// A mapping to the registration.
		mark("CreateSubjectMapping")
		issuerID := issuer.ID
		mappingTenant, err = q.CreateSubjectMapping(c, queries.CreateSubjectMappingParams{TenantID: tenantID, TrustedIssuerID: &issuerID, Subject: "spiffe://ünï/ns/ci, 1",
			ServiceAccountID: serviceAccountID, Description: "d"})
		if err != nil {
			return err
		}
		if mappingTenant.IssuerScope != "tenant" || mappingTenant.IssuerID != issuer.ID || mappingTenant.IssuerName != "idp-uni" || mappingTenant.Subject != "spiffe://ünï/ns/ci, 1" ||
			mappingTenant.ServiceAccountID != serviceAccountID || mappingTenant.ServiceAccountName == "" || mappingTenant.TenantID != tenantID || mappingTenant.CreatedAt.IsZero() {
			t.Errorf("CreateSubjectMapping: %+v", mappingTenant)
		}
		mark("CountSubjectMappingsOfIssuer")
		if n, err := q.CountSubjectMappingsOfIssuer(c, issuer.ID); err != nil || n != 1 {
			t.Errorf("CountSubjectMappingsOfIssuer: %d %v", n, err)
		}
		mark("GetSubjectMapping")
		gm, err := q.GetSubjectMapping(c, mappingTenant.ID)
		if err != nil || gm.ID != mappingTenant.ID || gm.IssuerName != "idp-uni" || gm.Subject != mappingTenant.Subject {
			t.Errorf("GetSubjectMapping: %+v %v", gm, err)
		}
		mark("ListSubjectMappings")
		ml, err := q.ListSubjectMappings(c, queries.ListSubjectMappingsParams{Sort: "created_at", AfterID: zeroID, PageSize: 10})
		if err != nil || len(ml) != 1 || ml[0].ID != mappingTenant.ID {
			t.Errorf("ListSubjectMappings: %+v %v", ml, err)
		}
		for name, p := range map[string]queries.ListSubjectMappingsParams{
			"by issuer":          {IssuerID: issuer.ID, Sort: "-created_at", AfterID: zeroID, PageSize: 10},
			"by service account": {ServiceAccountID: serviceAccountID, Sort: "created_at", AfterID: zeroID, PageSize: 10},
			"by subject":         {Subject: mappingTenant.Subject, Sort: "created_at", AfterID: zeroID, PageSize: 10},
		} {
			if ml, err := q.ListSubjectMappings(c, p); err != nil || len(ml) != 1 {
				t.Errorf("ListSubjectMappings %s: %+v %v", name, ml, err)
			}
		}
		if ml, err := q.ListSubjectMappings(c, queries.ListSubjectMappingsParams{Sort: "created_at", Subject: "another", AfterID: zeroID, PageSize: 10}); err != nil || len(ml) != 0 {
			t.Errorf("ListSubjectMappings for a subject that does not exist: %+v %v", ml, err)
		}
		if ml, err := q.ListSubjectMappings(c, queries.ListSubjectMappingsParams{Sort: "created_at", After: true, AfterID: mappingTenant.ID, PageSize: 10}); err != nil || len(ml) != 0 {
			t.Errorf("ListSubjectMappings after the last: %+v %v", ml, err)
		}
		return nil
	}))

	// The members that are JSON objects are replaced as a whole. The parameter of an update is a
	// nullable jsonb, sent through the extended protocol without a prepared statement, which is
	// how every statement of the product runs: it must reach the server as JSON text.
	replaced := `{"env": "prod", "tier": "silver"}`
	err := admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		p := queries.UpdateTrustedIssuerParams{ID: issuer.ID}
		setJSON(t, &p.RequiredClaims, replaced)
		upd, err := queries.New(tx).UpdateTrustedIssuer(c, p)
		if err != nil {
			return err
		}
		sameJSON("UpdateTrustedIssuer with required_claims", upd.RequiredClaims, `{"env":"prod","tier":"silver"}`)
		p.RequiredClaims = nil
		setJSON(t, &p.RequiredClaims, `{}`)
		if upd, err = queries.New(tx).UpdateTrustedIssuer(c, p); err != nil {
			return err
		}
		sameJSON("UpdateTrustedIssuer clearing required_claims", upd.RequiredClaims, `{}`)
		return nil
	})
	claimsNow := `{}` // what the registration holds after the update above, which a failure leaves as it was
	if err != nil {
		t.Errorf("UpdateTrustedIssuer with required_claims: %v", err)
		claimsNow = required
	}

	// A platform-level registration with this tenant in its list, in platform scope.
	var platform queries.CreatePlatformIssuerRow
	var tenantList queries.PutPlatformIssuerTenantRow
	must("platform-level registrations", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("CreatePlatformIssuer")
		var err error
		platform, err = q.CreatePlatformIssuer(c, queries.CreatePlatformIssuerParams{Name: "shared-uni", Description: "d", Issuer: "https://shared-4-1.example.test", Audience: "shared-aud",
			Algorithms: []string{"PS256", "ES256"}, SubjectClaim: "sub", TenantClaim: "org", RequiredClaims: required, Enabled: true})
		if err != nil {
			return err
		}
		if platform.Name != "shared-uni" || platform.TenantClaim != "org" || platform.TenantCount != 0 || !reflect.DeepEqual(platform.Algorithms, []string{"PS256", "ES256"}) || len(platform.ID) != 36 {
			t.Errorf("CreatePlatformIssuer: %+v", platform)
		}
		sameJSON("CreatePlatformIssuer", platform.RequiredClaims, required)
		mark("LockPlatformIssuer")
		if id, err := q.LockPlatformIssuer(c, platform.ID); err != nil || id != platform.ID {
			t.Errorf("LockPlatformIssuer: %q %v", id, err)
		}
		mark("PutPlatformIssuerTenant")
		tenantList, err = q.PutPlatformIssuerTenant(c, queries.PutPlatformIssuerTenantParams{IssuerID: platform.ID, TenantID: tenantID, ClaimValue: "org-ünï-1"})
		if err != nil || tenantList.TenantName == "" || tenantList.ClaimValue != "org-ünï-1" || tenantList.IssuerID != platform.ID || tenantList.TenantID != tenantID {
			t.Errorf("PutPlatformIssuerTenant: %+v %v", tenantList, err)
		}
		again, err := q.PutPlatformIssuerTenant(c, queries.PutPlatformIssuerTenantParams{IssuerID: platform.ID, TenantID: tenantID, ClaimValue: "org-ünï-2"})
		if err != nil || again.ClaimValue != "org-ünï-2" || !again.CreatedAt.Equal(tenantList.CreatedAt) {
			t.Errorf("PutPlatformIssuerTenant again (idempotent, the claim value is replaced, the row stays): %+v %v", again, err)
		}
		mark("GetPlatformIssuer")
		got, err := q.GetPlatformIssuer(c, platform.ID)
		if err != nil || got.ID != platform.ID || got.TenantCount != 1 || got.TenantClaim != "org" {
			t.Errorf("GetPlatformIssuer: %+v %v", got, err)
		}
		mark("ListPlatformIssuerTenants")
		tl, err := q.ListPlatformIssuerTenants(c, queries.ListPlatformIssuerTenantsParams{IssuerID: platform.ID, Sort: "created_at", AfterID: zeroID, PageSize: 10})
		if err != nil || len(tl) != 1 || tl[0].ClaimValue != "org-ünï-2" || tl[0].TenantName == "" {
			t.Errorf("ListPlatformIssuerTenants: %+v %v", tl, err)
		}
		if tl, err := q.ListPlatformIssuerTenants(c, queries.ListPlatformIssuerTenantsParams{IssuerID: platform.ID, Sort: "-created_at", After: true,
			AfterCreatedAt: time.Now().Add(time.Hour), AfterID: tenantID, PageSize: 10}); err != nil || len(tl) != 1 {
			t.Errorf("ListPlatformIssuerTenants after a cursor in the future: %+v %v", tl, err)
		}
		mark("ListPlatformIssuers")
		pl, err := q.ListPlatformIssuers(c, queries.ListPlatformIssuersParams{Sort: "name", AfterID: zeroID, PageSize: 10})
		if err != nil || len(pl) != 1 || pl[0].ID != platform.ID || pl[0].TenantCount != 1 {
			t.Errorf("ListPlatformIssuers: %+v %v", pl, err)
		}
		if pl, err := q.ListPlatformIssuers(c, queries.ListPlatformIssuersParams{Sort: "-created_at", Name: "shared-uni", Enabled: "false", AfterID: zeroID, PageSize: 10}); err != nil || len(pl) != 0 {
			t.Errorf("ListPlatformIssuers with filters: %+v %v", pl, err)
		}
		mark("UpdatePlatformIssuer")
		claim, enabled := "org_id", false
		upd, err := q.UpdatePlatformIssuer(c, queries.UpdatePlatformIssuerParams{ID: platform.ID, TenantClaim: &claim, Enabled: &enabled})
		if err != nil || upd.TenantClaim != "org_id" || upd.Enabled || upd.Name != "shared-uni" || upd.TenantCount != 1 || !reflect.DeepEqual(upd.Algorithms, []string{"PS256", "ES256"}) {
			t.Errorf("UpdatePlatformIssuer: %+v %v", upd, err)
		}
		on := true
		if _, err := q.UpdatePlatformIssuer(c, queries.UpdatePlatformIssuerParams{ID: platform.ID, Enabled: &on}); err != nil {
			t.Errorf("UpdatePlatformIssuer enable: %v", err)
		}
		return nil
	}))
	err = admin.Platform(c, func(c context.Context, tx store.Tx) error {
		p := queries.UpdatePlatformIssuerParams{ID: platform.ID}
		setJSON(t, &p.RequiredClaims, replaced)
		upd, err := queries.New(tx).UpdatePlatformIssuer(c, p)
		if err != nil {
			return err
		}
		sameJSON("UpdatePlatformIssuer with required_claims", upd.RequiredClaims, `{"env":"prod","tier":"silver"}`)
		return nil
	})
	if err != nil {
		t.Errorf("UpdatePlatformIssuer with required_claims: %v", err)
	}

	// The tenant's view of the platform-level registration, and a mapping to it.
	must("a tenant's view of platform-level registrations", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("ListTenantPlatformIssuers")
		l, err := q.ListTenantPlatformIssuers(c, queries.ListTenantPlatformIssuersParams{Sort: "created_at", AfterID: zeroID, PageSize: 10})
		if err != nil || len(l) != 1 || l[0].ID != platform.ID || l[0].ClaimValue != "org-ünï-2" || l[0].TenantClaim != "org_id" || l[0].Name != "shared-uni" {
			t.Errorf("ListTenantPlatformIssuers: %+v %v", l, err)
		}
		if l, err := q.ListTenantPlatformIssuers(c, queries.ListTenantPlatformIssuersParams{Sort: "-created_at", After: true, AfterID: platform.ID, PageSize: 10}); err != nil || len(l) != 0 {
			t.Errorf("ListTenantPlatformIssuers after the last: %+v %v", l, err)
		}
		mark("GetTenantPlatformIssuer")
		g, err := q.GetTenantPlatformIssuer(c, platform.ID)
		if err != nil || g.ID != platform.ID || g.ClaimValue != "org-ünï-2" {
			t.Errorf("GetTenantPlatformIssuer: %+v %v", g, err)
		}
		pid := platform.ID
		m, err := q.CreateSubjectMapping(c, queries.CreateSubjectMappingParams{TenantID: tenantID, PlatformIssuerID: &pid, Subject: "workload-p", ServiceAccountID: serviceAccountID})
		if err != nil || m.IssuerScope != "platform" || m.IssuerID != platform.ID || m.IssuerName != "shared-uni" {
			t.Errorf("CreateSubjectMapping to a platform-level registration: %+v %v", m, err)
		}
		return nil
	}))

	// The statements of authentication, as the gateway role runs them.
	var principalRow queries.GetTokenPrincipalRow
	must("the gateway's statements", gateway.Unscoped(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("LookupIssuers")
		rows, err := q.LookupIssuers(c, queries.LookupIssuersParams{Issuer: "https://idp-4-1.example.test/realms/é", Audiences: []string{"nope", "aud-2"}})
		if err != nil || len(rows) != 1 || rows[0].Scope != "tenant" || rows[0].TenantID != tenantID || rows[0].IssuerID != issuer.ID {
			t.Errorf("LookupIssuers for a tenant-bound registration: %+v %v", rows, err)
		}
		rows, err = q.LookupIssuers(c, queries.LookupIssuersParams{Issuer: "https://shared-4-1.example.test", Audiences: []string{"shared-aud"}})
		if err != nil || len(rows) != 1 || rows[0].Scope != "platform" || rows[0].TenantID != "" || rows[0].IssuerID != platform.ID {
			t.Errorf("LookupIssuers for a platform-level registration: %+v %v", rows, err)
		}
		if rows, err := q.LookupIssuers(c, queries.LookupIssuersParams{Issuer: "https://idp-4-1.example.test/realms/é", Audiences: []string{"other"}}); err != nil || len(rows) != 0 {
			t.Errorf("LookupIssuers for an audience that is not registered: %+v %v", rows, err)
		}
		mark("GetPlatformIssuerForAuth")
		pa, err := q.GetPlatformIssuerForAuth(c, platform.ID)
		if err != nil || pa.ID != platform.ID || pa.TenantClaim != "org_id" || !pa.Enabled || pa.SubjectClaim != "sub" || !reflect.DeepEqual(pa.Algorithms, []string{"PS256", "ES256"}) {
			t.Errorf("GetPlatformIssuerForAuth: %+v %v", pa, err)
		}
		mark("LookupPlatformIssuerTenant")
		if got, err := q.LookupPlatformIssuerTenant(c, queries.LookupPlatformIssuerTenantParams{IssuerID: platform.ID, ClaimValue: "org-ünï-2"}); err != nil || got != tenantID {
			t.Errorf("LookupPlatformIssuerTenant: %q %v", got, err)
		}
		if got, err := q.LookupPlatformIssuerTenant(c, queries.LookupPlatformIssuerTenantParams{IssuerID: platform.ID, ClaimValue: "org-ünï-1"}); err != nil || got != "" {
			t.Errorf("LookupPlatformIssuerTenant for a claim value that is not listed any more: %q %v", got, err)
		}
		return nil
	}))
	must("the gateway's statements in the tenant's session", gateway.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("GetTrustedIssuerForAuth")
		ta, err := q.GetTrustedIssuerForAuth(c, issuer.ID)
		if err != nil || ta.ID != issuer.ID || ta.TenantID != tenantID || ta.Audience != "aud-2" || !ta.Enabled || ta.SubjectClaim != "azp" {
			t.Errorf("GetTrustedIssuerForAuth: %+v %v", ta, err)
		}
		sameJSON("GetTrustedIssuerForAuth", ta.RequiredClaims, claimsNow)
		mark("GetTokenPrincipal")
		principalRow, err = q.GetTokenPrincipal(c, queries.GetTokenPrincipalParams{Subject: "spiffe://ünï/ns/ci, 1", Scope: "tenant", IssuerID: issuer.ID})
		if err != nil {
			t.Fatalf("GetTokenPrincipal: %v", err)
		}
		if principalRow.MappingID != mappingTenant.ID || principalRow.ServiceAccountID != serviceAccountID || principalRow.TenantID != tenantID || principalRow.ServiceAccountState != "active" ||
			principalRow.TenantName == "" || principalRow.TenantState == "" || len(principalRow.Roles) == 0 {
			t.Errorf("GetTokenPrincipal: %+v", principalRow)
		}
		if _, err := q.GetTokenPrincipal(c, queries.GetTokenPrincipalParams{Subject: "another", Scope: "tenant", IssuerID: issuer.ID}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetTokenPrincipal for a subject that is not mapped: %v", err)
		}
		if _, err := q.GetTokenPrincipal(c, queries.GetTokenPrincipalParams{Subject: "spiffe://ünï/ns/ci, 1", Scope: "platform", IssuerID: issuer.ID}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetTokenPrincipal with the scope of another kind: %v", err)
		}
		if _, err := q.GetTokenPrincipal(c, queries.GetTokenPrincipalParams{Subject: "workload-p", Scope: "platform", IssuerID: platform.ID, ClaimValue: "org-ünï-2"}); err != nil {
			t.Errorf("GetTokenPrincipal for a platform-level registration: %v", err)
		}
		if _, err := q.GetTokenPrincipal(c, queries.GetTokenPrincipalParams{Subject: "workload-p", Scope: "platform", IssuerID: platform.ID, ClaimValue: "org-ünï-1"}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("GetTokenPrincipal with a claim value that does not designate the tenant: %v", err)
		}
		mark("RecordTokenAuthFailure")
		return q.RecordTokenAuthFailure(c, queries.RecordTokenAuthFailureParams{Scope: "tenant", IssuerID: issuer.ID, Reason: "bad_signature", Subject: "", RequestID: "0123456789abcdef0123456789abcdef"})
	}))

	// The deletes, last: the tenant list entry, the platform-level registration, a mapping and its registration.
	must("the deletes", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeletePlatformIssuerTenant")
		del, err := q.DeletePlatformIssuerTenant(c, queries.DeletePlatformIssuerTenantParams{IssuerID: platform.ID, TenantID: tenantID})
		if err != nil || del.ClaimValue != "org-ünï-2" || del.TenantName == "" {
			t.Errorf("DeletePlatformIssuerTenant: %+v %v", del, err)
		}
		if _, err := q.DeletePlatformIssuerTenant(c, queries.DeletePlatformIssuerTenantParams{IssuerID: platform.ID, TenantID: tenantID}); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("a second DeletePlatformIssuerTenant: %v", err)
		}
		return nil
	}))
	must("delete a mapping", admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeleteSubjectMapping")
		del, err := q.DeleteSubjectMapping(c, mappingTenant.ID)
		if err != nil || del.ID != mappingTenant.ID || del.Subject != mappingTenant.Subject || del.IssuerName != "idp-uni" {
			t.Errorf("DeleteSubjectMapping: %+v %v", del, err)
		}
		if _, err := q.DeleteSubjectMapping(c, mappingTenant.ID); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("a second DeleteSubjectMapping: %v", err)
		}
		return nil
	}))
	must("delete the platform-level registration", admin.Platform(c, func(c context.Context, tx store.Tx) error {
		q := queries.New(tx)
		mark("DeletePlatformIssuer")
		del, err := q.DeletePlatformIssuer(c, platform.ID)
		if err != nil || del.ID != platform.ID || del.TenantCount != 0 {
			t.Errorf("DeletePlatformIssuer: %+v %v", del, err)
		}
		if _, err := q.DeletePlatformIssuer(c, platform.ID); !errors.Is(err, store.ErrNoRows) {
			t.Errorf("a second DeletePlatformIssuer: %v", err)
		}
		return nil
	}))
}

// setJSON sets the JSON member of an update's parameters, whatever Go type the generator gave it
// ([]byte for a nullable jsonb, *string when the statement casts through text).
func setJSON(t *testing.T, field any, text string) {
	t.Helper()
	switch f := field.(type) {
	case *[]byte:
		*f = []byte(text)
	case **string:
		*f = &text
	default:
		t.Fatalf("the required_claims parameter is a %T", field)
	}
}
