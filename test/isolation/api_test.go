package isolation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/telemetry"
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
}

type apiWorld struct {
	api  *apitest.API
	a, b side
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
	return s
}

func newAPIWorld(t *testing.T) *apiWorld {
	t.Helper()
	w := &apiWorld{api: apitest.New(t)}
	w.a, w.b = newSide(t, w.api, "tenant-a"), newSide(t, w.api, "tenant-b")
	return w
}

func (s side) params() apitest.Params {
	return apitest.Params{Tenant: s.tenant.ID, ServiceAccount: s.sa.ID, VirtualKey: s.key.Key.ID, PlatformKey: "pabcdefghijklmnop"}
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
	w := newAPIWorld(t)
	rows := w.api.SnapshotTenant(t, w.b.tenant.ID)
	checked := 0
	for _, op := range adminapi.Operations {
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
			real := apitest.Params{Tenant: w.b.tenant.ID, ServiceAccount: w.b.sa.ID, VirtualKey: w.b.key.Key.ID, PlatformKey: "pabcdefghijklmnop"}
			missing := apitest.Params{Tenant: randomUUID(t, w), ServiceAccount: randomUUID(t, w), VirtualKey: randomKeyID(t), PlatformKey: "pabcdefghijklmnoq"}
			cr, cm := apitest.CallFor(t, op, real), apitest.CallFor(t, op, missing)
			if op.ID == "createTenant" {
				cm.Body = cr.Body
			}
			r1 := w.api.Do(t, cr.Method, cr.Path, w.a.adminKey, cr.Body, nil)
			r2 := w.api.Do(t, cm.Method, cm.Path, w.a.adminKey, cm.Body, nil)
			if scope == authz.ScopeSelf {
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
			real.ServiceAccount, real.VirtualKey = w.b.sa.ID, w.b.key.Key.ID
			missing.ServiceAccount, missing.VirtualKey = randomUUID(t, w), randomKeyID(t)
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
		if strings.Contains(op.Path, "{service_account}") || (strings.Contains(op.Path, "{key}") && !strings.HasPrefix(op.Path, "/v1/platform-keys")) {
			real, missing := w.a.params(), w.a.params()
			real.ServiceAccount, real.VirtualKey = w.b.sa.ID, w.b.key.Key.ID
			missing.ServiceAccount, missing.VirtualKey = randomUUID(t, w), randomKeyID(t)
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

		// A body member that names a resource.
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
func inferenceHandler(t *testing.T, w *apiWorld) (*httptest.Server, *testutil.LogBuffer) {
	t.Helper()
	logs := &testutil.LogBuffer{}
	logger := telemetry.NewLogger(logs, "debug")
	gw := w.api.DB.Open(t, store.RoleGateway)
	auth, err := authn.New(authn.Options{DB: gw, Logger: logger, Kinds: []authn.Kind{authn.KindTenant}, CacheTTL: 0})
	if err != nil {
		t.Fatal(err)
	}
	up := fakeprovider.New(t)
	base, err := url.Parse(up.BaseURL())
	if err != nil {
		t.Fatal(err)
	}
	tr := gateway.NewTransport(2 * time.Second)
	t.Cleanup(tr.CloseIdleConnections)
	h, err := gateway.New(gateway.Options{
		Logger: logger, BaseURL: base, ProviderKey: secret.New(fixtures.CanaryProviderKey), Authenticator: auth,
		MaxRequestBodyBytes: 1 << 20, RequestBodyReadTimeout: 2 * time.Second, MaxRequestDuration: 10 * time.Second, Transport: tr,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, logs
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
	w := newAPIWorld(t)
	c := w.api.Client(w.a.adminKey)
	n := 0
	for _, op := range adminapi.Operations {
		if scopeOf(t, op) != authz.ScopeTenant {
			continue
		}
		// The positive control: A's key does it in A.
		own := apitest.CallFor(t, op, w.a.params())
		r := w.api.Do(t, own.Method, own.Path, w.a.adminKey, own.Body, nil)
		if r.Status != 200 && r.Status != 201 {
			t.Errorf("%s in A: status %d\n%s", op.ID, r.Status, r.Body)
		}
		// And none in B.
		p := w.a.params()
		p.Tenant, p.ServiceAccount, p.VirtualKey = w.b.tenant.ID, w.b.sa.ID, w.b.key.Key.ID
		other := apitest.CallFor(t, op, p)
		if r := w.api.Do(t, other.Method, other.Path, w.a.adminKey, other.Body, nil); r.Status != 404 {
			t.Errorf("%s in B: status %d\n%s", op.ID, r.Status, r.Body)
		}
		n++
	}
	if n != 10 {
		t.Errorf("%d tenant operations, want 10", n)
	}
	if _, err := c.GetTenant(ctx(t), adminapi.GetTenantInput{Tenant: w.b.tenant.ID}); err == nil {
		t.Error("a tenant key read a tenant")
	}
	// The inference handler attributes each key to its own tenant.
	srv, logs := inferenceHandler(t, w)
	for _, s := range []side{w.a, w.b} {
		resp := chat(t, srv, s.inference.Secret, nil, "")
		if resp.StatusCode != 200 {
			t.Fatalf("inference with the key of %s: %d", s.tenant.Name, resp.StatusCode)
		}
	}
	testutil.Eventually(t, 5*time.Second, "two request lines", func() bool { return len(logs.Find(t, "request")) >= 2 })
	lines := logs.Find(t, "request")
	got := map[string]bool{}
	for _, l := range lines {
		got[l["tenant_id"].(string)] = true
	}
	if !got[w.a.tenant.ID] || !got[w.b.tenant.ID] || len(got) != 2 {
		t.Errorf("tenants in the request lines: %v", got)
	}
}

// TestPlatformKeyHasNoTenantPermission is T-272.
func TestPlatformKeyHasNoTenantPermission(t *testing.T) {
	w := newAPIWorld(t)
	for _, op := range adminapi.Operations {
		if scopeOf(t, op) != authz.ScopeTenant {
			continue
		}
		real, missing := w.a.params(), w.a.params()
		missing.Tenant, missing.ServiceAccount, missing.VirtualKey = randomUUID(t, w), randomUUID(t, w), randomKeyID(t)
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

// TestTenantAdminIsEnoughAndNoMore is T-273.
func TestTenantAdminIsEnoughAndNoMore(t *testing.T) {
	w := newAPIWorld(t)
	for _, op := range adminapi.Operations {
		call := apitest.CallFor(t, op, w.a.params())
		r := w.api.Do(t, call.Method, call.Path, w.a.adminKey, call.Body, nil)
		switch scopeOf(t, op) {
		case authz.ScopeTenant, authz.ScopeSelf:
			if r.Status != 200 && r.Status != 201 {
				t.Errorf("%s: a tenant-admin key got %d\n%s", op.ID, r.Status, r.Body)
			}
		default:
			expect403(t, op, r)
		}
	}
}

// TestEveryRouteDeclaresAPermission is T-274.
func TestEveryRouteDeclaresAPermission(t *testing.T) {
	w := newAPIWorld(t)
	catalogue := map[string]bool{}
	for _, p := range authz.Catalogue() {
		catalogue[p.Name] = true
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
	for _, op := range adminapi.Operations {
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
	for _, op := range adminapi.Operations {
		call := apitest.CallFor(t, op, w.a.params())
		if r := w.api.Do(t, call.Method, call.Path, "", call.Body, nil); r.Status != 401 {
			t.Errorf("%s without a credential: %d", op.ID, r.Status)
		}
	}
}

// TestCallerCannotNameTheTenant is T-275.
func TestCallerCannotNameTheTenant(t *testing.T) {
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
	srv, logs := inferenceHandler(t, w)
	for _, q := range []string{"", "?tenant_id=" + w.b.tenant.ID} {
		for _, h := range []http.Header{nil, hdr} {
			if resp := chat(t, srv, w.a.inference.Secret, h, q); resp.StatusCode != 200 {
				t.Errorf("inference with %q and %v: %d", q, h, resp.StatusCode)
			}
		}
	}
	testutil.Eventually(t, 5*time.Second, "four request lines", func() bool { return len(logs.Find(t, "request")) >= 4 })
	for _, l := range logs.Find(t, "request") {
		if l["tenant_id"] != w.a.tenant.ID {
			t.Errorf("a request line names tenant %v", l["tenant_id"])
		}
	}
}
