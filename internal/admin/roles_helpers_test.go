package admin_test

import (
	"testing"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
)

func parseTenant(t testing.TB, id string) tenant.ID {
	t.Helper()
	tid, err := tenant.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	return tid
}

// probeParams names the resources of the calls that probe every operation of the document: the
// tenant is real, every other identifier is well formed and names nothing, so that a request is
// refused or answered by its handler with a 404 and the router never rejects the path. A request
// whose permission the caller holds reaches its handler; that is all a probe needs.
func probeParams(t testing.TB, _ *apitest.API, tenantID string) apitest.Params {
	t.Helper()
	_, keyID, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	return apitest.Params{Tenant: tenantID, ServiceAccount: apitest.NoUser, VirtualKey: keyID, PlatformKey: "pabcdefghijklmnop", Credential: apitest.NoUser,
		Provider: "synthetic-missing", Endpoint: "ep-missing", Model: apitest.NoUser, PriceVersion: apitest.NoUser, PriceImport: apitest.NoUser,
		TokenIssuer: apitest.NoUser, SubjectMapping: apitest.NoUser, PlatformIssuer: apitest.NoUser, LimitRule: apitest.NoLimitRule, PlatformLimit: apitest.NoLimitRule,
		TenantPrice: apitest.NoUser, User: apitest.NoUser, Session: apitest.NoUser, Role: apitest.NoRole, SupportAction: apitest.NoUser, Destination: apitest.NoUser}
}
