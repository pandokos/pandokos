package isolation

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// side is one tenant of the API tests: its administrator, one more service account and one more
// key, and an inference service account with a key.
type side struct {
	tenant    adminapi.Tenant
	adminKey  string
	sa        adminapi.ServiceAccount // inference role
	key       adminapi.VirtualKeyCreated
	inference adminapi.VirtualKeyCreated // a key of a service account of its own, for the inference tests
	cred      adminapi.Credential        // the default openai credential of the tenant

	// The catalogue resources every side's params name (platform operations are called with a
	// tenant key in these tests; the identifiers only have to exist).
	provider, endpoint, model, priceVersion, priceImport string

	// The token issuers of sprint 4.1, seeded as superuser: a tenant-bound issuer of the side with
	// one mapping, and a platform-level issuer (the same for both sides) that lists the side.
	tokenIssuer, mapping, platformIssuer string

	// The limit rules of sprint 5.2, seeded as superuser: a rule of the side and a platform rule for it.
	limitRule, platformLimit string

	// The price of sprint 5.4: a scheduled version of the side's own price list, seeded as superuser.
	tenantPrice string

	// The event destination of sprint 6.4, seeded as superuser with a placeholder secret.
	destination string
}

type apiWorld struct {
	api  *apitest.API
	a, b side

	cat    apitest.Catalogue
	onlyB  adminapi.Model // a `selected` model granted to tenant B only
	allRef string         // the reference of a model that every tenant may use
}

func newSide(t *testing.T, api *apitest.API, name string) side {
	t.Helper()
	var s side
	s.tenant, s.adminKey = api.Tenant(t, name)
	c := api.Client(s.adminKey)
	sa, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: s.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "worker", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.sa = *sa
	k, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: s.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, Description: "extra"}})
	if err != nil {
		t.Fatal(err)
	}
	s.key = *k
	inf, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: s.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "inference", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	ik, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: s.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: inf.ID}})
	if err != nil {
		t.Fatal(err)
	}
	s.inference = *ik
	s.cred = api.Credential(t, s.tenant.ID, s.adminKey, "openai", "main")
	s.tokenIssuer = api.SeedTokenIssuer(t, s.tenant.ID)
	s.mapping = api.SeedSubjectMapping(t, s.tenant.ID, s.tokenIssuer, s.sa.ID)
	s.limitRule = api.SeedLimitRule(t, s.tenant.ID)
	s.platformLimit = api.SeedPlatformLimit(t, s.tenant.ID)
	s.destination = api.SeedEventDestination(t, s.tenant.ID)
	return s
}

func newAPIWorld(t *testing.T) *apiWorld {
	t.Helper()
	w := &apiWorld{api: apitest.NewPeople(t, apitest.PeopleConfig{})} // the member operations need local accounts
	w.a, w.b = newSide(t, w.api, "tenant-a"), newSide(t, w.api, "tenant-b")

	// The catalogue of sprint 2.2: a provider with a model for everybody and a model granted to
	// tenant B only, a price version and a price import.
	platform := w.api.Client(w.api.PlatformKey())
	w.cat = w.api.Catalog(t)
	w.allRef = w.cat.All.Ref
	only, err := platform.CreateModel(ctx(t), adminapi.CreateModelInput{Body: adminapi.ModelCreate{
		Provider: w.cat.Provider.Name, Name: apitest.UniqueName("synthetic-only-b"), Availability: adminapi.ModelAvailabilitySelected}})
	if err != nil {
		t.Fatal(err)
	}
	w.onlyB = *only
	if _, err := platform.GrantModel(ctx(t), adminapi.GrantModelInput{Model: only.ID, Tenant: w.b.tenant.ID}); err != nil {
		t.Fatal(err)
	}
	pv, err := platform.CreatePriceOverride(ctx(t), adminapi.CreatePriceOverrideInput{Model: w.cat.All.ID, Body: adminapi.PriceOverride{
		Currency: "USD", Rates: []adminapi.PriceRate{{Category: adminapi.PriceCategoryInput, Rate: "1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	imp, err := platform.RunPriceImport(ctx(t), adminapi.RunPriceImportInput{Body: adminapi.PriceImportRun{Source: adminapi.PriceImportRunSourceEmbedded}})
	if err != nil {
		t.Fatal(err)
	}
	platformIssuer := w.api.SeedPlatformIssuer(t)
	for _, s := range []*side{&w.a, &w.b} {
		w.api.SeedPlatformIssuerTenant(t, platformIssuer, s.tenant.ID)
		s.tenantPrice = w.seedTenantPrice(t, *s)
		s.platformIssuer = platformIssuer
		s.provider, s.endpoint, s.model, s.priceVersion, s.priceImport = w.cat.Provider.Name, w.cat.Endpoint.Name, w.cat.All.ID, pv.ID, imp.ID
	}
	return w
}

func (s side) params() apitest.Params {
	return apitest.Params{Tenant: s.tenant.ID, ServiceAccount: s.sa.ID, VirtualKey: s.key.Key.ID, PlatformKey: "pabcdefghijklmnop", Credential: s.cred.ID,
		Provider: s.provider, Endpoint: s.endpoint, Model: s.model, PriceVersion: s.priceVersion, PriceImport: s.priceImport,
		TokenIssuer: s.tokenIssuer, SubjectMapping: s.mapping, PlatformIssuer: s.platformIssuer,
		LimitRule: s.limitRule, PlatformLimit: s.platformLimit, TenantPrice: s.tenantPrice, Destination: s.destination}
}

// seedTenantPrice seeds a scheduled version of the side's price list for the model of everybody, with a
// start of its own (a start is unique per list and model), and returns its ID.
func (w *apiWorld) seedTenantPrice(t *testing.T, s side) string {
	t.Helper()
	id, err := tenant.ParseID(s.tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(time.Hour + time.Duration(rand.Int63n(int64(24*time.Hour*365))))
	return w.api.DB.SeedTenantPrice(t, id, "EUR", w.cat.Provider.Name, w.cat.All.Name, start)
}

// fresh is params with a credential, and a model, of its own when the operation names one: the
// tests that call every operation in turn would otherwise find the credential deleted, revoked or
// replaced, or the model deleted or changed, by an earlier operation. The new credential is not
// the default of its provider; the new model belongs to the provider of the world's catalogue.
func (w *apiWorld) fresh(t *testing.T, s side, op adminapi.Operation) apitest.Params {
	t.Helper()
	p := s.params()
	if strings.Contains(op.Path, "{credential}") {
		p.Credential = w.api.Credential(t, s.tenant.ID, s.adminKey, "openai", apitest.UniqueName("extra")).ID
	}
	if strings.Contains(op.Path, "{model}") {
		m, err := w.api.Client(w.api.PlatformKey()).CreateModel(ctx(t), adminapi.CreateModelInput{Body: adminapi.ModelCreate{
			Provider: w.cat.Provider.Name, Name: apitest.UniqueName("synthetic-fresh"), Availability: adminapi.ModelAvailabilityAll}})
		if err != nil {
			t.Fatal(err)
		}
		p.Model = m.ID
	}
	// The operations on a member get a member of their own that the tenant manages, so that the
	// ones that remove or disable it leave the others.
	if strings.Contains(op.Path, "/members/{user}") {
		id, err := tenant.ParseID(s.tenant.ID)
		if err != nil {
			t.Fatal(err)
		}
		p.User = w.api.DB.TenantUser(t, id, apitest.UniqueName("member"), []string{"tenant-admin"}).ID
	}
	// The operations of sprint 4.1 that delete a registration or a mapping get one of their own, so
	// that the ones the other operations name are still there.
	if !strings.HasPrefix(op.Path, "/v1/platform-issuers") && (strings.Contains(op.Path, "{issuer}") || strings.Contains(op.Path, "{mapping}")) {
		p.TokenIssuer = w.api.SeedTokenIssuer(t, s.tenant.ID)
		if strings.Contains(op.Path, "{mapping}") {
			p.SubjectMapping = w.api.SeedSubjectMapping(t, s.tenant.ID, p.TokenIssuer, s.sa.ID)
		}
	}
	if strings.HasPrefix(op.Path, "/v1/platform-issuers") && strings.Contains(op.Path, "{issuer}") {
		p.PlatformIssuer = w.api.SeedPlatformIssuer(t)
		w.api.SeedPlatformIssuerTenant(t, p.PlatformIssuer, s.tenant.ID)
	}
	// The operations of sprint 5.4 that name a price version get a scheduled one of their own (a
	// delete removes it).
	if strings.Contains(op.Path, "{price}") {
		p.TenantPrice = w.seedTenantPrice(t, s)
	}
	// The operation that deletes a limit rule gets one of its own, so that the operations after it
	// (the usage of a rule, of sprint 6.2) still find the rule of the tenant.
	if op.ID == "deleteLimit" {
		p.LimitRule = w.api.SeedExtraLimitRule(t, s.tenant.ID)
	}
	// The operation that deletes an event destination gets one of its own, so that the operations after
	// it (rotate, test, rewind, dead letters) still find the destination of the tenant (sprint 6.4).
	if op.ID == "deleteEventDestination" {
		p.Destination = w.api.SeedEventDestination(t, s.tenant.ID)
	}
	// The operations of sprint 4.3 on a custom role or on a support action get one of their own.
	if strings.Contains(op.Path, "{role}") {
		p.Role = w.api.CustomRole(t, s.tenant.ID, "member:read")
	}
	if strings.Contains(op.Path, "/support-actions/{action}") {
		p.SupportAction = apitest.OpenSupport(t, &apitest.Caller{API: w.api, Key: w.api.PlatformKey(), Name: "platform"}, s.tenant.ID, "member:read")
	}
	return p
}

// normalize reduces a response to what must not depend on the resource it names: the status,
// the headers but the request ID and the date, and the body but its request ID member.
func normalize(t *testing.T, r *apitest.Response) string {
	t.Helper()
	var hdr []string
	for k, v := range r.Header {
		if k == "X-Pandokos-Request-Id" || k == "Date" {
			continue
		}
		hdr = append(hdr, k+": "+strings.Join(v, ","))
	}
	sort.Strings(hdr)
	body := string(r.Body)
	var m map[string]any
	if json.Unmarshal(r.Body, &m) == nil {
		delete(m, "request_id")
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		body = string(b)
	}
	return http.StatusText(r.Status) + "\n" + strings.Join(hdr, "\n") + "\n\n" + body
}

func randomUUID(t *testing.T, w *apiWorld) string {
	t.Helper()
	var id string
	if err := w.api.Super(t).QueryRow(ctx(t), "SELECT pandokos.uuid_v7()::text").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func randomKeyID(t *testing.T) string {
	t.Helper()
	_, id, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// platformAndSelf classifies the operations that no tenant permission governs; an operation
// that is not here and has no tenant permission fails the test until somebody classifies it.
var platformAndSelf = map[string]string{
	"getMe":             "self",
	"listTenants":       "platform",
	"createTenant":      "platform",
	"getTenant":         "platform",
	"updateTenant":      "platform",
	"suspendTenant":     "platform",
	"reactivateTenant":  "platform",
	"listPlatformKeys":  "platform",
	"createPlatformKey": "platform",
	"getPlatformKey":    "platform",
	"revokePlatformKey": "platform",

	// Sprint 6.3: the deletion of tenants, the log kept for a deleted one and the key versions.
	"requestTenantDeletion":          "platform",
	"cancelTenantDeletion":           "platform",
	"getTenantDeletion":              "platform",
	"listTenantDeletions":            "platform",
	"getDeletedTenantAuditHead":      "platform",
	"exportDeletedTenantAuditEvents": "platform",
	"listKEKVersions":                "platform",

	"listPlatformAuditEvents":   "platform",
	"getPlatformAuditHead":      "platform",
	"exportPlatformAuditEvents": "platform",

	"listTenantCredentials":   "platform",
	"getTenantCredential":     "platform",
	"disableTenantCredential": "platform",
	"revokeTenantCredential":  "platform",

	// Sprint 2.2: the 25 platform operations of the catalogue.
	"listProviders":       "platform",
	"createProvider":      "platform",
	"getProvider":         "platform",
	"updateProvider":      "platform",
	"deleteProvider":      "platform",
	"listEndpoints":       "platform",
	"createEndpoint":      "platform",
	"getEndpoint":         "platform",
	"updateEndpoint":      "platform",
	"deleteEndpoint":      "platform",
	"listModels":          "platform",
	"createModel":         "platform",
	"getModel":            "platform",
	"updateModel":         "platform",
	"deleteModel":         "platform",
	"listModelGrants":     "platform",
	"grantModel":          "platform",
	"revokeModelGrant":    "platform",
	"listPriceVersions":   "platform",
	"createPriceOverride": "platform",
	"deletePriceVersion":  "platform",
	"clearPriceOverride":  "platform",
	"listPriceImports":    "platform",
	"getPriceImport":      "platform",
	"runPriceImport":      "platform",

	// Sprint 4.1: the eight platform operations of the platform-level token issuers.
	"listPlatformIssuers":        "platform",
	"createPlatformIssuer":       "platform",
	"getPlatformIssuer":          "platform",
	"updatePlatformIssuer":       "platform",
	"deletePlatformIssuer":       "platform",
	"listPlatformIssuerTenants":  "platform",
	"putPlatformIssuerTenant":    "platform",
	"deletePlatformIssuerTenant": "platform",

	// Sprint 5.2: the five platform operations of the platform limits.
	"listPlatformLimits":  "platform",
	"createPlatformLimit": "platform",
	"getPlatformLimit":    "platform",
	"updatePlatformLimit": "platform",
	"deletePlatformLimit": "platform",

	// Sprint 4.2: the nine platform operations on users, and the four of the caller's own session.
	"listUsers": "platform", "createUser": "platform", "getUser": "platform", "updateUser": "platform", "disableUser": "platform",
	"enableUser": "platform", "unlockUser": "platform", "resetUserPassword": "platform", "revokeUserSessions": "platform",
	"signOut": "self", "changePassword": "self", "listOwnSessions": "self", "deleteOwnSession": "self",

	// Sprint 4.3: the catalogue and the built-in roles, and the four platform operations on the support actions of a tenant.
	"listPermissions": "self", "listBuiltinRoles": "self",
	"listTenantSupportActions": "platform", "openSupportAction": "platform", "getTenantSupportAction": "platform", "closeSupportAction": "platform",

	// Sprint 6.4: the export of the platform's events.
	"exportPlatformEvents": "platform",
}

func scopeOf(t *testing.T, op adminapi.Operation) authz.Scope {
	t.Helper()
	p, ok := authz.Lookup(op.Permission)
	if !ok {
		t.Fatalf("%s names %q, which is not in the catalogue", op.ID, op.Permission)
	}
	return p.Scope
}

// TestCrossTenantIndistinguishable is T-270: a caller of tenant A that names a resource of
// tenant B gets the same response as for a resource that does not exist.
func TestCrossTenantIndistinguishable(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	rows := w.api.SnapshotTenant(t, w.b.tenant.ID)
	checked := 0
	for _, op := range apiOperations() {
		scope := scopeOf(t, op)
		if scope != authz.ScopeTenant {
			kind, ok := platformAndSelf[op.ID]
			if !ok {
				t.Errorf("the operation %s has no tenant permission and is not classified: add it to platformAndSelf", op.ID)
				continue
			}
			if (kind == "platform") != (scope == authz.ScopePlatform) {
				t.Errorf("%s is classified %s but its permission has scope %s", op.ID, kind, scope)
			}
			// A's key gets 403 on a platform operation, or 200 on getMe, the same for an
			// existing and a missing identifier.
			real := w.b.params()
			real.PlatformKey = "pabcdefghijklmnop"
			missing := apitest.Params{Tenant: randomUUID(t, w), ServiceAccount: randomUUID(t, w), VirtualKey: randomKeyID(t), PlatformKey: "pabcdefghijklmnoq", Credential: randomUUID(t, w),
				Provider: "synthetic-missing", Endpoint: "ep-missing", Model: randomUUID(t, w), PriceVersion: randomUUID(t, w), PriceImport: randomUUID(t, w),
				TokenIssuer: randomUUID(t, w), SubjectMapping: randomUUID(t, w), PlatformIssuer: randomUUID(t, w),
				LimitRule: randomUUID(t, w), PlatformLimit: randomUUID(t, w), TenantPrice: randomUUID(t, w), Destination: randomUUID(t, w)}
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			if op.ID == "createTenant" {
				cm.Body = cr.Body
			}
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if op.Permission == authz.SessionSelf {
				// Sprint 4.2: a key has no session, so the operations on one's own session refuse it.
				expect403(t, op, r1)
				expect403(t, op, r2)
			} else if scope == authz.ScopeSelf {
				if r1.Status != 200 || r2.Status != 200 {
					t.Errorf("%s: statuses %d %d", op.ID, r1.Status, r2.Status)
				}
			} else {
				expect403(t, op, r1)
				expect403(t, op, r2)
				if normalize(t, r1) != normalize(t, r2) {
					t.Errorf("%s: a platform operation answers differently for an existing and a missing identifier:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
				}
			}
			checked++
			continue
		}

		if strings.Contains(op.Path, "{tenant}") {
			real, missing := w.a.params(), w.a.params()
			real.Tenant, missing.Tenant = w.b.tenant.ID, randomUUID(t, w)
			real.ServiceAccount, real.VirtualKey, real.Credential = w.b.sa.ID, w.b.key.Key.ID, w.b.cred.ID
			real.TokenIssuer, real.SubjectMapping, real.LimitRule, real.TenantPrice, real.Destination = w.b.tokenIssuer, w.b.mapping, w.b.limitRule, w.b.tenantPrice, w.b.destination
			missing.ServiceAccount, missing.VirtualKey, missing.Credential = randomUUID(t, w), randomKeyID(t), randomUUID(t, w)
			missing.TokenIssuer, missing.SubjectMapping, missing.LimitRule, missing.TenantPrice, missing.Destination = randomUUID(t, w), randomUUID(t, w), randomUUID(t, w), randomUUID(t, w), randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if r1.Status != 404 || r2.Status != 404 || !strings.Contains(string(r1.Body), `"code":"not_found"`) {
				t.Errorf("%s: another tenant gives %d, a missing tenant %d, want 404 not_found\n%s", op.ID, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: another tenant and a missing tenant differ:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.b.tenant.ID); after != rows {
				t.Fatalf("%s changed a row of tenant B", op.ID)
			}
			checked++
		}

		// A second parameter: in A's own tenant, B's resource against a missing one.
		if strings.Contains(op.Path, "{service_account}") || strings.Contains(op.Path, "{credential}") || (strings.Contains(op.Path, "{key}") && !strings.HasPrefix(op.Path, "/v1/platform-keys")) ||
			strings.Contains(op.Path, "{mapping}") || (strings.Contains(op.Path, "{issuer}") && strings.HasPrefix(op.Path, "/v1/tenants/")) ||
			(strings.Contains(op.Path, "{rule}") && strings.HasPrefix(op.Path, "/v1/tenants/")) || strings.Contains(op.Path, "{price}") || strings.Contains(op.Path, "{destination}") {
			real, missing := w.a.params(), w.a.params()
			real.ServiceAccount, real.VirtualKey, real.Credential = w.b.sa.ID, w.b.key.Key.ID, w.b.cred.ID
			real.TokenIssuer, real.SubjectMapping, real.LimitRule, real.TenantPrice, real.Destination = w.b.tokenIssuer, w.b.mapping, w.b.limitRule, w.b.tenantPrice, w.b.destination
			missing.ServiceAccount, missing.VirtualKey, missing.Credential = randomUUID(t, w), randomKeyID(t), randomUUID(t, w)
			missing.TokenIssuer, missing.SubjectMapping, missing.LimitRule, missing.TenantPrice, missing.Destination = randomUUID(t, w), randomUUID(t, w), randomUUID(t, w), randomUUID(t, w), randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if r1.Status != 404 || r2.Status != 404 {
				t.Errorf("%s: B's resource gives %d, a missing one %d, want 404\n%s", op.ID, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: B's resource and a missing one differ:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.b.tenant.ID); after != rows {
				t.Fatalf("%s changed a row of tenant B", op.ID)
			}
			checked++
		}

		// A body member that names a resource: the credential a new key is pinned to.
		if op.ID == "createVirtualKey" {
			real, missing := w.a.params(), w.a.params()
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			cr.Body.(map[string]any)["credential_id"] = w.b.cred.ID
			cm.Body.(map[string]any)["credential_id"] = randomUUID(t, w)
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if r1.Status != 400 || r2.Status != 400 {
				t.Errorf("%s: B's credential gives %d, a missing one %d, want 400\n%s", op.ID, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: B's credential and a missing one differ:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.b.tenant.ID); after != rows {
				t.Fatalf("%s changed a row of tenant B", op.ID)
			}
			checked++
		}
		if op.ID == "createVirtualKey" {
			real, missing := w.a.params(), w.a.params()
			real.ServiceAccount, missing.ServiceAccount = w.b.sa.ID, randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if r1.Status != 400 || r2.Status != 400 {
				t.Errorf("%s: B's service account gives %d, a missing one %d, want 400\n%s", op.ID, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: B's service account and a missing one differ:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.b.tenant.ID); after != rows {
				t.Fatalf("%s changed a row of tenant B", op.ID)
			}
			checked++
		}

		// A body member that names models: a model granted only to B, named by A, is refused
		// exactly as a model that does not exist (a tenant cannot probe the grants of others).
		if op.ID == "createVirtualKey" || op.ID == "setModelRestriction" {
			cr, cm := apitest.CallFor(t, op, w.a.params()), apitest.CallFor(t, op, w.a.params())
			set := func(c apitest.Call, refs ...string) apitest.Call {
				body := c.Body.(map[string]any)
				body["models"] = refs
				return c
			}
			cr, cm = set(cr, w.onlyB.Ref), set(cm, w.cat.Provider.Name+"/synthetic-missing-model")
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if r1.Status != 400 || r2.Status != 400 || !strings.Contains(string(r1.Body), `"code":"invalid_request"`) {
				t.Errorf("%s: B's model gives %d, a missing one %d, want 400\n%s", op.ID, r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("%s: B's model and a missing one differ:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.b.tenant.ID); after != rows {
				t.Fatalf("%s changed a row of tenant B", op.ID)
			}
			// The control: a model that every tenant may use is accepted.
			ok := set(apitest.CallFor(t, op, w.a.params()), w.allRef)
			if r := w.api.Do(t, ok.Method, ok.Path, w.a.adminKey, ok.Body, nil); r.Status != 200 && r.Status != 201 {
				t.Errorf("%s: a model that every tenant may use gives %d\n%s", op.ID, r.Status, r.Body)
			}
			// And B itself may name the model granted to it.
			own := set(apitest.CallFor(t, op, w.b.params()), w.onlyB.Ref)
			if r := w.api.Do(t, own.Method, own.Path, w.b.adminKey, own.Body, nil); r.Status != 200 && r.Status != 201 {
				t.Errorf("%s: B naming its own model gives %d\n%s", op.ID, r.Status, r.Body)
			}
			// B's own call changed B's rows on purpose: the baseline of the following operations moves with it.
			rows = w.api.SnapshotTenant(t, w.b.tenant.ID)
			checked += 3
		}
	}
	if checked < 20 {
		t.Errorf("only %d checks were made", checked)
	}
	// A malformed identifier does not match a route at all: the same 404 as a missing one.
	for _, p := range []string{"/v1/tenants/" + w.b.tenant.ID + "x/service-accounts", "/v1/tenants/not-an-id/service-accounts", "/v1/tenants/" + strings.ToUpper(w.b.tenant.ID) + "/service-accounts"} {
		r := w.api.Do(t, "GET", p, w.a.adminKey, nil, nil)
		if r.Status != 404 {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
}

func expect403(t *testing.T, op adminapi.Operation, r *apitest.Response) {
	t.Helper()
	if r.Status != 403 || !strings.Contains(string(r.Body), `"code":"forbidden"`) {
		t.Errorf("%s: status %d, want 403 forbidden\n%s", op.ID, r.Status, r.Body)
	}
}

// inferenceHandler returns an in-process inference handler authenticating with the virtual
// keys of the world, and the log buffer of its request lines.
// inferenceHandler returns the server and the recorder of its usage records: the request line names
// no tenant (ADR 0055), the record does.
func inferenceHandler(t *testing.T, w *apiWorld) (*httptest.Server, *testutil.MemoryRecorder) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	logger := telemetry.NewLogger(logs, "debug")
	gw := w.api.DB.Open(t, store.RoleGateway)
	auth, err := authn.New(authn.Options{DB: gw, Logger: logger, Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: 0})
	if err != nil {
		t.Fatal(err)
	}
	up := fakeprovider.New(t)
	guard := testutil.TestGuard(t)
	rec := &testutil.MemoryRecorder{}
	h, err := gateway.New(gateway.Options{
		DefaultOutputTokens: 4096,
		Logger:              logger, Authenticator: auth,
		Catalogue:     testutil.StaticCatalogue(map[string]string{"openai": "openai"}, map[string]gateway.Target{"openai/fake-basic": testutil.TargetFor(up, "openai", "fake-basic")}),
		Credentials:   testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryProviderKey}),
		Families:      map[string]family.Adapter{"openai": openai.Adapter()},
		DefaultFamily: "openai",
		Public:        guard.Transport(false), Private: guard.Transport(true),
		MaxRequestBodyBytes: 1 << 20, RequestBodyReadTimeout: 2 * time.Second, MaxRequestDuration: 10 * time.Second,
		Recorder: rec, Tracer: tracenoop.NewTracerProvider(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, rec
}

func chat(t *testing.T, srv *httptest.Server, key string, hdr http.Header, query string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx(t), "POST", srv.URL+"/openai/v1/chat/completions"+query,
		strings.NewReader(`{"model":"fake-basic","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestKeyOfOneTenantCannotAddressAnother is T-271.
func TestKeyOfOneTenantCannotAddressAnother(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	c := w.api.Client(w.a.adminKey)
	n := 0
	for _, op := range apiOperations() {
		if scopeOf(t, op) != authz.ScopeTenant {
			continue
		}
		// The positive control: A's key does it in A.
		own := apitest.CallFor(t, op, w.fresh(t, w.a, op))
		r := w.api.Do(t, own.Method, own.Path, w.a.adminKey, own.Body, nil)
		if r.Status != 200 && r.Status != 201 && r.Status != 202 && r.Status != 204 { // deleteRole answers 204, testEventDestination 202
			t.Errorf("%s in A: status %d\n%s", op.ID, r.Status, r.Body)
		}
		// And none in B.
		p := w.a.params()
		p.Tenant, p.ServiceAccount, p.VirtualKey, p.Credential = w.b.tenant.ID, w.b.sa.ID, w.b.key.Key.ID, w.b.cred.ID
		p.TokenIssuer, p.SubjectMapping, p.LimitRule, p.TenantPrice = w.b.tokenIssuer, w.b.mapping, w.b.limitRule, w.b.tenantPrice
		other := apitest.CallFor(t, op, p)
		if r := w.api.Do(t, other.Method, other.Path, w.a.adminKey, other.Body, nil); r.Status != 404 {
			t.Errorf("%s in B: status %d\n%s", op.ID, r.Status, r.Body)
		}
		n++
	}
	if n != 92 {
		t.Errorf("%d tenant operations, want 92 (24, the two of sprint 3.3, the ten of sprint 4.1, the seven of sprint 5.2, the two of sprint 5.3, the seven of sprint 5.4, the ten of sprint 4.2, the ten of sprint 4.3, the nine of sprint 6.2 and the eleven of sprint 6.4)", n)
	}
	if _, err := c.GetTenant(ctx(t), adminapi.GetTenantInput{Tenant: w.b.tenant.ID}); err == nil {
		t.Error("a tenant key read a tenant")
	}
	// The inference handler attributes each key to its own tenant.
	srv, rec := inferenceHandler(t, w)
	for _, s := range []side{w.a, w.b} {
		resp := chat(t, srv, s.inference.Secret, nil, "")
		if resp.StatusCode != 200 {
			t.Fatalf("inference with the key of %s: %d", s.tenant.Name, resp.StatusCode)
		}
	}
	testutil.Eventually(t, 5*time.Second, "two usage records", func() bool { return len(rec.Records()) >= 2 })
	got := map[string]bool{}
	for _, r := range rec.Records() {
		got[r.TenantID.String()] = true
	}
	if !got[w.a.tenant.ID] || !got[w.b.tenant.ID] || len(got) != 2 {
		t.Errorf("tenants in the usage records: %v", got)
	}
}

// TestPlatformKeyHasNoTenantPermission is T-272.
func TestPlatformKeyHasNoTenantPermission(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	for _, op := range apiOperations() {
		if scopeOf(t, op) != authz.ScopeTenant {
			continue
		}
		real, missing := w.a.params(), w.a.params()
		missing.Tenant, missing.ServiceAccount, missing.VirtualKey, missing.Credential = randomUUID(t, w), randomUUID(t, w), randomKeyID(t), randomUUID(t, w)
		missing.TokenIssuer, missing.SubjectMapping, missing.TenantPrice = randomUUID(t, w), randomUUID(t, w), randomUUID(t, w)
		cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
		r1 := w.api.Do(t, cr.Method, cr.Path, w.api.PlatformKey(), cr.Body, nil)
		r2 := w.api.Do(t, cm.Method, cm.Path, w.api.PlatformKey(), cm.Body, nil)
		expect403(t, op, r1)
		expect403(t, op, r2)
		if normalize(t, r1) != normalize(t, r2) {
			t.Errorf("%s: an existing and a missing tenant differ for a platform key:\n%s\n%s", op.ID, normalize(t, r1), normalize(t, r2))
		}
	}
	// And the inference handler refuses it.
	srv, _ := inferenceHandler(t, w)
	if resp := chat(t, srv, w.api.PlatformKey(), nil, ""); resp.StatusCode != 401 {
		t.Errorf("a platform key on the inference handler: %d", resp.StatusCode)
	}
}

// TestTenantAdminIsEnoughAndNoMore is T-273 (changed in sprint 4.3: it runs for the owner, and the
// tenant-admin is enough for everything but endSupportAction, which is kept for owners).
func TestTenantAdminIsEnoughAndNoMore(t *testing.T) {
	t.Parallel()
	for _, who := range []struct {
		name string
		key  func(w *apiWorld) string
	}{
		{"owner", func(w *apiWorld) string { return w.a.adminKey }},
		{"tenant-admin", func(w *apiWorld) string { return w.api.AsRole(t, w.a.tenant.ID, "tenant-admin").Key }},
	} {
		t.Run(who.name, func(t *testing.T) {
			// A world of its own: the operations that delete or create leave the next run a different tenant.
			w := newAPIWorld(t)
			plat := &apitest.Caller{API: w.api, Key: w.api.PlatformKey(), Name: "platform"}
			who := struct {
				name string
				key  string
			}{who.name, who.key(w)}
			for _, op := range apiOperations() {
				p := w.fresh(t, w.a, op)
				// The operations on a custom role and on a support action get ones of their own.
				if strings.Contains(op.Path, "{role}") {
					p.Role = w.api.CustomRole(t, w.a.tenant.ID, "member:read")
				}
				if strings.Contains(op.Path, "/support-actions/{action}") {
					p.SupportAction = apitest.OpenSupport(t, plat, w.a.tenant.ID, "member:read")
				}
				call := apitest.CallFor(t, op, p)
				r := w.api.Do(t, call.Method, call.Path, who.key, call.Body, nil)
				switch scopeOf(t, op) {
				case authz.ScopeTenant, authz.ScopeSelf:
					if op.Permission == authz.SessionSelf {
						expect403(t, op, r) // sprint 4.2: a key has no session
						continue
					}
					if who.name == "tenant-admin" && op.ID == "endSupportAction" {
						expect403(t, op, r) // sprint 4.3: kept for owners
						continue
					}
					if r.Status != 200 && r.Status != 201 && r.Status != 202 && r.Status != 204 {
						t.Errorf("%s: a %s key got %d\n%s", op.ID, who.name, r.Status, r.Body)
					}
				default:
					expect403(t, op, r)
				}
			}
		})
	}
}

// TestRoutesArePublicOrPermitted is T-825 (T-274, TestEveryRouteDeclaresAPermission, in sprint 4.2): every
// operation declares a permission or is one of exactly four public operations; without a credential
// every operation that is not public is 401.
func TestRoutesArePublicOrPermitted(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	catalogue := map[string]bool{}
	for _, p := range authz.Catalogue() {
		catalogue[p.Name] = true
	}
	public := map[string]bool{}
	for _, op := range adminapi.Operations {
		if op.Public {
			public[op.ID] = true
			if op.Permission != "" {
				t.Errorf("the public operation %s names the permission %q", op.ID, op.Permission)
			}
		} else if !catalogue[op.Permission] {
			t.Errorf("%s is not public and names %q, which is not in the catalogue", op.ID, op.Permission)
		}
	}
	if len(public) != 4 || !public["getAuthMethods"] || !public["signIn"] || !public["startOidcSignIn"] || !public["completeOidcSignIn"] {
		t.Errorf("the public operations are %v, want exactly getAuthMethods, signIn, startOidcSignIn and completeOidcSignIn", public)
	}
	// A service account with no role at all.
	c := w.api.Client(w.a.adminKey)
	sa, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.a.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "no-roles"}})
	if err != nil {
		t.Fatal(err)
	}
	k, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.a.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range apiOperations() {
		if !catalogue[op.Permission] {
			t.Errorf("%s names %q, which is not in the catalogue", op.ID, op.Permission)
		}
		call := apitest.CallFor(t, op, w.a.params())
		r := w.api.Do(t, call.Method, call.Path, k.Secret, call.Body, nil)
		expect403(t, op, r)
	}
	// The inference route refuses it too.
	srv, _ := inferenceHandler(t, w)
	resp := chat(t, srv, k.Secret, nil, "")
	if resp.StatusCode != 403 {
		t.Errorf("inference with no role: %d", resp.StatusCode)
	}
	// Nothing answers without a credential.
	if resp := chat(t, srv, "", nil, ""); resp.StatusCode != 401 {
		t.Errorf("inference without a credential: %d", resp.StatusCode)
	}
	for _, op := range apiOperations() {
		call := apitest.CallFor(t, op, w.a.params())
		if r := w.api.Do(t, call.Method, call.Path, "", call.Body, nil); r.Status != 401 {
			t.Errorf("%s without a credential: %d", op.ID, r.Status)
		}
	}
}

// TestCallerCannotNameTheTenant is T-275.
func TestCallerCannotNameTheTenant(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	hdr := http.Header{"X-Tenant-Id": {w.b.tenant.ID}, "X-Pandokos-Tenant": {w.b.tenant.ID}, "X-Pandokos-Tenant-Id": {w.b.tenant.ID}}
	path := "/v1/tenants/" + w.a.tenant.ID + "/service-accounts"
	plain := w.api.Do(t, "GET", path, w.a.adminKey, nil, nil)
	named := w.api.Do(t, "GET", path, w.a.adminKey, nil, hdr)
	if plain.Status != 200 || normalize(t, plain) != normalize(t, named) {
		t.Errorf("headers that name tenant B changed the response:\n%s\n%s", normalize(t, plain), normalize(t, named))
	}
	if strings.Contains(string(named.Body), w.b.tenant.ID) {
		t.Error("a row of tenant B in the response")
	}
	me := w.api.Do(t, "GET", "/v1/me", w.a.adminKey, nil, hdr)
	if !strings.Contains(string(me.Body), w.a.tenant.ID) || strings.Contains(string(me.Body), w.b.tenant.ID) {
		t.Errorf("getMe with headers: %s", me.Body)
	}
	for _, q := range []string{"?tenant_id=" + w.b.tenant.ID, "?tenant=" + w.b.tenant.ID} {
		r := w.api.Do(t, "GET", path+q, w.a.adminKey, nil, nil)
		if r.Status != 400 || !strings.Contains(string(r.Body), `"code":"invalid_request"`) {
			t.Errorf("%s: %d %s", q, r.Status, r.Body)
		}
	}
	r := w.api.Do(t, "POST", path, w.a.adminKey, map[string]any{"name": "sneaky", "tenant_id": w.b.tenant.ID}, nil)
	if r.Status != 400 || !strings.Contains(string(r.Body), `"code":"invalid_request"`) {
		t.Errorf("a body member tenant_id: %d %s", r.Status, r.Body)
	}
	// Nothing was created in either tenant.
	if l, err := w.api.Client(w.a.adminKey).ListServiceAccounts(ctx(t), adminapi.ListServiceAccountsInput{Tenant: w.a.tenant.ID, Name: "sneaky"}); err != nil || len(l.Items) != 0 {
		t.Errorf("%+v %v", l, err)
	}

	// On the inference handler the same headers and query change nothing.
	srv, rec := inferenceHandler(t, w)
	for _, q := range []string{"", "?tenant_id=" + w.b.tenant.ID} {
		for _, h := range []http.Header{nil, hdr} {
			if resp := chat(t, srv, w.a.inference.Secret, h, q); resp.StatusCode != 200 {
				t.Errorf("inference with %q and %v: %d", q, h, resp.StatusCode)
			}
		}
	}
	testutil.Eventually(t, 5*time.Second, "four usage records", func() bool { return len(rec.Records()) >= 4 })
	for _, r := range rec.Records() {
		if r.TenantID.String() != w.a.tenant.ID {
			t.Errorf("a usage record names tenant %v", r.TenantID)
		}
	}
}

// TestTimeZoneAPI is T-719: a key of tenant B asking for the time zone of tenant A, on both methods,
// gets the 404 of a tenant that does not exist, and changes nothing of A; its own zone it reads and
// sets.
func TestTimeZoneAPI(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	before := w.api.SnapshotTenant(t, w.a.tenant.ID)
	missing := randomUUID(t, w)
	for _, m := range []struct {
		method string
		body   any
	}{{"GET", nil}, {"PUT", map[string]any{"time_zone": "Europe/Rome"}}} {
		other := w.api.Do(t, m.method, "/v1/tenants/"+w.a.tenant.ID+"/time-zone", w.b.adminKey, m.body, nil)
		none := w.api.Do(t, m.method, "/v1/tenants/"+missing+"/time-zone", w.b.adminKey, m.body, nil)
		if other.Status != 404 || none.Status != 404 || normalize(t, other) != normalize(t, none) {
			t.Errorf("%s: another tenant's zone %d, a missing tenant's %d\n%s\n%s", m.method, other.Status, none.Status, normalize(t, other), normalize(t, none))
		}
		if strings.Contains(string(other.Body), w.a.tenant.ID) {
			t.Errorf("%s: the answer names the tenant: %s", m.method, other.Body)
		}
	}
	if w.api.SnapshotTenant(t, w.a.tenant.ID) != before {
		t.Error("a key of B changed the rows of A")
	}
	if got := scalarOf(t, w.api, `SELECT time_zone FROM pandokos.tenant WHERE id = $1::uuid`, w.a.tenant.ID); got != "" {
		t.Errorf("A's zone is %q after B's attempts", got)
	}
	// The positive control: B reads and sets its own.
	if r := w.api.Do(t, "GET", "/v1/tenants/"+w.b.tenant.ID+"/time-zone", w.b.adminKey, nil, nil); r.Status != 200 {
		t.Errorf("B reads its own zone: %d\n%s", r.Status, r.Body)
	}
	if r := w.api.Do(t, "PUT", "/v1/tenants/"+w.b.tenant.ID+"/time-zone", w.b.adminKey, map[string]any{"time_zone": "Europe/Rome"}, nil); r.Status != 200 {
		t.Errorf("B sets its own zone: %d\n%s", r.Status, r.Body)
	}
}

func scalarOf(t *testing.T, api *apitest.API, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := api.Super(t).QueryRow(ctx(t), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// priceListOperations are the seven operations of sprint 5.4.
var priceListOperations = []string{"listPriceLists", "listPriceListVersions", "createPriceListVersion", "getPriceListVersion", "deletePriceListVersion", "getActiveCurrency", "setActiveCurrency"}

// TestTenantPriceAPI is T-748: a key of tenant B on the seven operations of A gets the 404 of a tenant
// that does not exist; a price ID of B in A's path is the 404 of an unknown ID.
func TestTenantPriceAPI(t *testing.T) {
	t.Parallel()
	w := newAPIWorld(t)
	byID := map[string]adminapi.Operation{}
	for _, op := range apiOperations() {
		byID[op.ID] = op
	}
	for _, id := range priceListOperations {
		op, ok := byID[id]
		if !ok {
			t.Errorf("the operation %s is not in the specification", id)
			continue
		}
		t.Run(id+" on another tenant", func(t *testing.T) {
			real, missing := w.fresh(t, w.b, op), w.fresh(t, w.b, op)
			real.Tenant, missing.Tenant = w.a.tenant.ID, randomUUID(t, w)
			real.TenantPrice, missing.TenantPrice = w.a.tenantPrice, randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			rowsOfA := w.api.SnapshotTenant(t, w.a.tenant.ID) // taken here: a call with A's own key (below) records its use
			r1 := w.api.Do(t, cr.Method, cr.Path, w.b.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.b.adminKey, cm.Body, nil)
			if r1.Status != 404 || r2.Status != 404 || !strings.Contains(string(r1.Body), `"code":"not_found"`) {
				t.Errorf("B's key on A: %d, on a missing tenant: %d, want 404 not_found\n%s", r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("another tenant and a missing tenant differ:\n%s\n%s", normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.a.tenant.ID); after != rowsOfA {
				t.Fatalf("%s changed a row of tenant A", id)
			}
		})
		if !strings.Contains(op.Path, "{price}") {
			continue
		}
		t.Run(id+" with a price of another tenant", func(t *testing.T) {
			real, missing := w.fresh(t, w.a, op), w.fresh(t, w.a, op)
			real.TenantPrice, missing.TenantPrice = w.b.tenantPrice, randomUUID(t, w)
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			rowsOfB := w.api.SnapshotTenant(t, w.b.tenant.ID)
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if r1.Status != 404 || r2.Status != 404 {
				t.Errorf("B's price in A's path: %d, an unknown one: %d, want 404\n%s", r1.Status, r2.Status, r1.Body)
			}
			if normalize(t, r1) != normalize(t, r2) {
				t.Errorf("B's price and an unknown one differ:\n%s\n%s", normalize(t, r1), normalize(t, r2))
			}
			if after := w.api.SnapshotTenant(t, w.b.tenant.ID); after != rowsOfB {
				t.Fatalf("%s changed a row of tenant B", id)
			}
		})
	}
}

// apiOperations are the operations that need a credential: every one but the four public ones of
// sprint 4.2 (sign-in), which name no permission. T-825 covers those.
func apiOperations() []adminapi.Operation {
	var ops []adminapi.Operation
	for _, op := range adminapi.Operations {
		if !op.Public {
			ops = append(ops, op)
		}
	}
	return ops
}
