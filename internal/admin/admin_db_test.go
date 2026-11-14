package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/testutil"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

var reRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// problem decodes a problem response and checks the parts every problem has.
func problem(t testing.TB, r *apitest.Response) adminapi.Problem {
	t.Helper()
	var p adminapi.Problem
	if err := json.Unmarshal(r.Body, &p); err != nil {
		t.Fatalf("status %d, the body is not a problem: %v\n%s", r.Status, err, r.Body)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type %q on a problem", ct)
	}
	if p.Status != r.Status || p.Type != "urn:pandokos:problem:"+string(p.Code) {
		t.Errorf("problem %+v does not match status %d", p, r.Status)
	}
	if id := r.Header.Get("X-Pandokos-Request-Id"); !reRequestID.MatchString(id) || p.RequestID != id {
		t.Errorf("request ID %q in the body, %q in the header", p.RequestID, id)
	}
	if cc := r.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	return p
}

func expect(t testing.TB, r *apitest.Response, status int, code adminapi.ProblemCode) adminapi.Problem {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d (%s)\n%s", r.Status, status, code, r.Body)
	}
	p := problem(t, r)
	if p.Code != code {
		t.Fatalf("code %q, want %q\n%s", p.Code, code, r.Body)
	}
	return p
}

func ok(t testing.TB, r *apitest.Response, status int, into any) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d\n%s", r.Status, status, r.Body)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	if id := r.Header.Get("X-Pandokos-Request-Id"); !reRequestID.MatchString(id) {
		t.Errorf("request ID %q", id)
	}
	if into != nil {
		if err := json.Unmarshal(r.Body, into); err != nil {
			t.Fatalf("%v\n%s", err, r.Body)
		}
	}
}

// ids creates a tenant, a service account and a virtual key through the API and returns what
// the tests need to address them.
type world struct {
	api       *apitest.API
	tenant    adminapi.Tenant
	adminKey  string
	sa        adminapi.ServiceAccount
	key       adminapi.VirtualKeyCreated
	platform  string
	platformK adminapi.PlatformKeyCreated
	cred      adminapi.Credential // the default openai credential of the tenant

	// The catalogue of sprint 2.2, for the tests that call every operation: a provider with an
	// endpoint and two models, a price version of one of them, and a price import row.
	cat apitest.Catalogue
	pv  adminapi.PriceVersion
	imp string
}

func newWorld(t *testing.T, name string) *world {
	t.Helper()
	w := &world{api: apitest.New(t)}
	w.fill(t, name)
	return w
}

func (w *world) fill(t *testing.T, name string) {
	t.Helper()
	w.platform = w.api.PlatformKey()
	w.tenant, w.adminKey = w.api.Tenant(t, name)
	c := w.api.Client(w.adminKey)
	sa, err := c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "worker", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	w.sa = *sa
	k, err := c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, Description: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	w.key = *k
	pk, err := w.api.Client(w.platform).CreatePlatformKey(ctx(t), adminapi.CreatePlatformKeyInput{Body: adminapi.PlatformKeyCreate{Description: "second platform key"}})
	if err != nil {
		t.Fatal(err)
	}
	w.platformK = *pk
	w.cred = w.api.Credential(t, w.tenant.ID, w.adminKey, "openai", "world")

	w.cat = w.api.Catalog(t)
	pv, err := w.api.Client(w.platform).CreatePriceOverride(ctx(t), adminapi.CreatePriceOverrideInput{Model: w.cat.All.ID, Body: adminapi.PriceOverride{
		Currency: "USD", Rates: []adminapi.PriceRate{{Category: adminapi.PriceCategoryInput, Rate: "1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	w.pv = *pv
	// An import row, made as superuser: running an import would write an audit row for every priced model.
	if err := w.api.Super(t).QueryRow(ctx(t), `INSERT INTO pandokos.price_import (started_at, source, dataset_format, outcome, actor_type)
		VALUES (now(), 'file', 'genai-prices-v2', 'success', 'host') RETURNING id::text`).Scan(&w.imp); err != nil {
		t.Fatal(err)
	}
}

func (w *world) params() apitest.Params {
	return apitest.Params{Tenant: w.tenant.ID, ServiceAccount: w.sa.ID, VirtualKey: w.key.Key.ID, PlatformKey: w.platformK.Key.ID, Credential: w.cred.ID,
		Provider: w.cat.Provider.Name, Endpoint: w.cat.Endpoint.Name, Model: w.cat.All.ID, PriceVersion: w.pv.ID, PriceImport: w.imp}
}

// call makes the valid call of op with the key.
func (w *world) call(t testing.TB, op adminapi.Operation, key string) *apitest.Response {
	t.Helper()
	c := apitest.CallFor(t, op, w.params())
	return w.api.Do(t, c.Method, c.Path, key, c.Body, nil)
}

// TestEveryOperationNeedsACredential is T-232.
func TestEveryOperationNeedsACredential(t *testing.T) {
	w := newWorld(t, "credentials")
	d := w.api.DB
	super := d.Super(t)

	// Keys that are refused for different reasons.
	insert := func(hashOfOther bool, expires string) string {
		t.Helper()
		key, keyID, hash, err := authn.Generate(authn.KindTenant)
		if err != nil {
			t.Fatal(err)
		}
		if hashOfOther {
			_, _, hash, _ = authn.Generate(authn.KindTenant)
		}
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash, expires_at) VALUES ($1::uuid, $2::uuid, $3, $4, `+expires+`)`,
			w.tenant.ID, w.sa.ID, keyID, hash[:]); err != nil {
			t.Fatal(err)
		}
		return key.Reveal()
	}
	wrongSecret := insert(true, "NULL")
	revoked := insert(false, "NULL")
	expired := insert(false, "now() - interval '1 second'")
	if _, err := super.Exec(ctx(t), `UPDATE pandokos.virtual_key SET revoked_at = now() WHERE key_id = $1`, revoked[4:21]); err != nil {
		t.Fatal(err)
	}
	// A disabled service account, and one that is not valid yet, each with a key.
	otherTenant, _ := w.api.Tenant(t, "credentials-two")
	var disabledSA, earlySA string
	for name, sql := range map[string]string{"disabled": "state = 'disabled'", "early": "valid_from = now() + interval '1 hour'"} {
		var id string
		if err := super.QueryRow(ctx(t), `INSERT INTO pandokos.service_account (tenant_id, name) VALUES ($1::uuid, $2) RETURNING id::text`, otherTenant.ID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := super.Exec(ctx(t), `UPDATE pandokos.service_account SET `+sql+` WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		if name == "disabled" {
			disabledSA = id
		} else {
			earlySA = id
		}
	}
	keyFor := func(sa string) string {
		key, keyID, hash, _ := authn.Generate(authn.KindTenant)
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, otherTenant.ID, sa, keyID, hash[:]); err != nil {
			t.Fatal(err)
		}
		return key.Reveal()
	}
	unknown, _, _, _ := authn.Generate(authn.KindTenant)
	unknownPlatform, _, _, _ := authn.Generate(authn.KindPlatform)

	type cred struct {
		name string
		key  string
		hdr  http.Header
	}
	creds := []cred{
		{"no header", "", nil},
		{"basic", "", http.Header{"Authorization": {"Basic dXNlcjpwYXNz"}}},
		{"basic with a real key", "", http.Header{"Authorization": {"Basic " + w.adminKey}}},
		{"empty bearer", "", http.Header{"Authorization": {"Bearer "}}},
		{"bearer without a space", "", http.Header{"Authorization": {"Bearer" + w.adminKey}}},
		{"two spaces", "", http.Header{"Authorization": {"Bearer  " + w.adminKey}}},
		{"malformed key", "pdk_nonsense", nil},
		{"unknown key", unknown.Reveal(), nil},
		{"unknown platform key", unknownPlatform.Reveal(), nil},
		{"wrong secret", wrongSecret, nil},
		{"revoked key", revoked, nil},
		{"expired key", expired, nil},
		{"disabled service account", keyFor(disabledSA), nil},
		{"service account not valid yet", keyFor(earlySA), nil},
	}

	// A baseline that must not move: the audit tables only gain authentication failures.
	before := w.api.Snapshot(t)
	seenAudit := seenIDs(t, w.api)
	var want *apitest.Response
	for _, op := range adminapi.Operations {
		for _, c := range creds {
			call := apitest.CallFor(t, op, w.params())
			r := w.api.Do(t, call.Method, call.Path, c.key, call.Body, c.hdr)
			p := expect(t, r, 401, adminapi.ProblemCodeUnauthenticated)
			if p.Title != "Unauthenticated" || p.Detail != "Missing or invalid credential." || len(p.Errors) != 0 {
				t.Errorf("%s with %s: %+v", op.ID, c.name, p)
			}
			if got := r.Header.Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("%s with %s: WWW-Authenticate %q", op.ID, c.name, got)
			}
			// The same body and the same headers in every case, the request ID aside.
			norm := strings.ReplaceAll(string(r.Body), p.RequestID, "ID")
			if want == nil {
				want = &apitest.Response{Body: []byte(norm), Header: r.Header.Clone()}
				continue
			}
			if norm != string(want.Body) {
				t.Errorf("%s with %s: the 401 body differs:\n%s\nwant\n%s", op.ID, c.name, norm, want.Body)
			}
			for _, h := range []string{"Content-Type", "Cache-Control", "Www-Authenticate"} {
				if r.Header.Get(h) != want.Header.Get(h) {
					t.Errorf("%s with %s: header %s is %q, want %q", op.ID, c.name, h, r.Header.Get(h), want.Header.Get(h))
				}
			}
		}
	}
	// Only the audit tables may change, and only by authentication failures.
	added := newRows(t, w.api, seenAudit)
	for _, r := range added {
		if r.Action != "auth.failure" || r.Outcome != "failure" || r.ActorType != "anonymous" {
			t.Errorf("a refused request wrote %+v", r)
		}
	}
	if len(added) == 0 {
		t.Error("no authentication failure was audited")
	}
	if after := w.api.Snapshot(t, "audit_event", "platform_audit_event"); after != strings.Join(nonAudit(before), "") {
		t.Error("a request without a valid credential changed a row")
	}
	// A request that is not a route is 404 whatever it carries, and the health paths are not
	// served here at all (they belong to the server, not to the API handler).
	r := w.api.Do(t, "GET", "/v1/nope", "", nil, nil)
	expect(t, r, 404, adminapi.ProblemCodeNotFound)
}

// nonAudit drops the two audit tables from a full snapshot.
func nonAudit(snapshot string) []string {
	var out []string
	for _, l := range strings.SplitAfter(snapshot, "\n") {
		if !strings.HasPrefix(l, "audit_event: ") && !strings.HasPrefix(l, "platform_audit_event: ") {
			out = append(out, l)
		}
	}
	return out
}

// TestPagination is T-233.
func TestPagination(t *testing.T) {
	api := apitest.New(t)
	for i := 0; i < 25; i++ {
		api.Tenant(t, fmt.Sprintf("page-%02d", i))
	}
	c := api.Client(api.PlatformKey())
	for _, sortBy := range []adminapi.NamedSort{adminapi.NamedSortCreatedAt, adminapi.NamedSortMinusCreatedAt, adminapi.NamedSortName, adminapi.NamedSortMinusName} {
		var all []adminapi.Tenant
		cursor, pages := "", 0
		for {
			out, err := c.ListTenants(ctx(t), adminapi.ListTenantsInput{Limit: 10, Sort: sortBy, Cursor: cursor})
			if err != nil {
				t.Fatalf("%s page %d: %v", sortBy, pages, err)
			}
			pages++
			all = append(all, out.Items...)
			if out.NextCursor == "" {
				if len(out.Items) == 0 || pages != 3 {
					t.Errorf("%s: the last page is page %d with %d items", sortBy, pages, len(out.Items))
				}
				break
			}
			if len(out.Items) != 10 {
				t.Errorf("%s: page %d has %d items", sortBy, pages, len(out.Items))
			}
			cursor = out.NextCursor
		}
		if len(all) != 25 {
			t.Fatalf("%s: %d items in %d pages, want 25", sortBy, len(all), pages)
		}
		seen := map[string]bool{}
		for i, tn := range all {
			if seen[tn.ID] {
				t.Errorf("%s: %s appears twice", sortBy, tn.ID)
			}
			seen[tn.ID] = true
			if i == 0 {
				continue
			}
			prev := all[i-1]
			var inOrder bool
			switch sortBy {
			case adminapi.NamedSortCreatedAt:
				inOrder = prev.ID < tn.ID
			case adminapi.NamedSortMinusCreatedAt:
				inOrder = prev.ID > tn.ID
			case adminapi.NamedSortName:
				inOrder = prev.Name < tn.Name
			default:
				inOrder = prev.Name > tn.Name
			}
			if !inOrder {
				t.Errorf("%s: %s (%s) follows %s (%s)", sortBy, tn.Name, tn.ID, prev.Name, prev.ID)
			}
		}
	}

	// The cursor is tied to its sort; broken cursors and limits are refused.
	first, err := c.ListTenants(ctx(t), adminapi.ListTenantsInput{Limit: 10, Sort: adminapi.NamedSortName})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("%v %+v", err, first)
	}
	get := func(q string) *apitest.Response {
		return api.Do(t, "GET", "/v1/tenants?"+q, api.PlatformKey(), nil, nil)
	}
	bad := map[string]string{
		"another sort":     "sort=-name&cursor=" + first.NextCursor,
		"default sort":     "cursor=" + first.NextCursor,
		"truncated cursor": "sort=name&cursor=" + first.NextCursor[:len(first.NextCursor)/2],
		"garbage cursor":   "sort=name&cursor=!!!",
		"padded cursor":    "sort=name&cursor=" + first.NextCursor + "==",
		"limit zero":       "limit=0",
		"limit 201":        "limit=201",
		"limit negative":   "limit=-1",
	}
	for name, q := range bad {
		r := get(q)
		p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		if len(p.Errors) == 0 {
			t.Errorf("%s: no member is named", name)
		}
		if strings.Contains(name, "cursor") || name == "another sort" || name == "default sort" {
			named := false
			for _, e := range p.Errors {
				named = named || e.Field == "cursor"
			}
			if !named {
				t.Errorf("%s: the member cursor is not named: %+v", name, p.Errors)
			}
		}
	}
	if r := get("limit=200"); r.Status != 200 {
		t.Errorf("limit 200: %d", r.Status)
	}
	// An empty collection is an empty array, never null, and has no cursor.
	w := newWorld(t, "empty-list")
	r := w.api.Do(t, "GET", "/v1/tenants/"+w.tenant.ID+"/virtual-keys?state=revoked", w.adminKey, nil, nil)
	ok(t, r, 200, nil)
	if strings.TrimSpace(string(r.Body)) != `{"items":[]}` {
		t.Errorf("an empty page is %s", r.Body)
	}
	// A page that is the last has no next_cursor member at all.
	r = get("limit=200")
	if strings.Contains(string(r.Body), "next_cursor") {
		t.Errorf("the last page carries next_cursor: %s", r.Body)
	}
}

// TestFilters is T-234.
func TestFilters(t *testing.T) {
	w := newWorld(t, "filters")
	plat := w.api.Client(w.platform)
	adm := w.api.Client(w.adminKey)
	other, _ := w.api.Tenant(t, "filters-other")
	if _, err := plat.SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: other.ID}); err != nil {
		t.Fatal(err)
	}

	ids := func(l *adminapi.TenantList) []string {
		var out []string
		for _, i := range l.Items {
			out = append(out, i.Name)
		}
		return out
	}
	byState, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{State: adminapi.TenantStateSuspended})
	if err != nil || strings.Join(ids(byState), ",") != "filters-other" {
		t.Errorf("by state: %v %v", ids(byState), err)
	}
	byName, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{Name: "filters"})
	if err != nil || strings.Join(ids(byName), ",") != "filters" {
		t.Errorf("by name: %v %v", ids(byName), err)
	}
	if none, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{Name: "fil"}); err != nil || len(none.Items) != 0 {
		t.Errorf("a name is exact: %v %v", ids(none), err)
	}
	if both, err := plat.ListTenants(ctx(t), adminapi.ListTenantsInput{Name: "filters", State: adminapi.TenantStateSuspended}); err != nil || len(both.Items) != 0 {
		t.Errorf("name and state: %v %v", ids(both), err)
	}

	// Service accounts.
	sa2, err := adm.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adm.DisableServiceAccount(ctx(t), adminapi.DisableServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: sa2.ID}); err != nil {
		t.Fatal(err)
	}
	disabled, err := adm.ListServiceAccounts(ctx(t), adminapi.ListServiceAccountsInput{Tenant: w.tenant.ID, State: adminapi.ServiceAccountStateDisabled})
	if err != nil || len(disabled.Items) != 1 || disabled.Items[0].ID != sa2.ID {
		t.Errorf("disabled: %+v %v", disabled, err)
	}
	active, err := adm.ListServiceAccounts(ctx(t), adminapi.ListServiceAccountsInput{Tenant: w.tenant.ID, State: adminapi.ServiceAccountStateActive})
	if err != nil || len(active.Items) != 2 { // the administrator, "worker"
		t.Errorf("active: %+v %v", active, err)
	}
	named, err := adm.ListServiceAccounts(ctx(t), adminapi.ListServiceAccountsInput{Tenant: w.tenant.ID, Name: "worker"})
	if err != nil || len(named.Items) != 1 || named.Items[0].ID != w.sa.ID {
		t.Errorf("by name: %+v %v", named, err)
	}

	// Keys: one active, one expired, one revoked.
	soon := time.Now().Add(1500 * time.Millisecond)
	expiring, err := adm.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: w.sa.ID, ExpiresAt: &soon}})
	if err != nil {
		t.Fatal(err)
	}
	toRevoke, err := adm.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa2.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adm.RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: w.tenant.ID, Key: toRevoke.Key.ID}); err != nil {
		t.Fatal(err)
	}
	keys := func(in adminapi.ListVirtualKeysInput) map[string]adminapi.KeyState {
		in.Tenant = w.tenant.ID
		out, err := adm.ListVirtualKeys(ctx(t), in)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]adminapi.KeyState{}
		for _, k := range out.Items {
			m[k.ID] = k.State
		}
		return m
	}
	testutil.Eventually(t, 5*time.Second, "the key to expire", func() bool {
		return keys(adminapi.ListVirtualKeysInput{State: adminapi.KeyStateExpired})[expiring.Key.ID] == adminapi.KeyStateExpired
	})
	if got := keys(adminapi.ListVirtualKeysInput{State: adminapi.KeyStateExpired}); len(got) != 1 {
		t.Errorf("expired: %v", got)
	}
	if got := keys(adminapi.ListVirtualKeysInput{State: adminapi.KeyStateRevoked}); len(got) != 1 || got[toRevoke.Key.ID] != adminapi.KeyStateRevoked {
		t.Errorf("revoked: %v", got)
	}
	if got := keys(adminapi.ListVirtualKeysInput{State: adminapi.KeyStateActive}); len(got) != 2 || got[w.key.Key.ID] != adminapi.KeyStateActive { // and the key of the administrator
		t.Errorf("active: %v", got)
	}
	if got := keys(adminapi.ListVirtualKeysInput{ServiceAccountID: sa2.ID}); len(got) != 1 || got[toRevoke.Key.ID] == "" {
		t.Errorf("by service account: %v", got)
	}
	if got := keys(adminapi.ListVirtualKeysInput{}); len(got) != 4 {
		t.Errorf("all: %v", got)
	}
	// A service account of another tenant names nothing visible: an empty page.
	other2, _ := w.api.Tenant(t, "filters-third")
	if out, err := adm.ListVirtualKeys(ctx(t), adminapi.ListVirtualKeysInput{Tenant: w.tenant.ID, ServiceAccountID: other2.ID}); err != nil || len(out.Items) != 0 {
		t.Errorf("by a foreign ID: %+v %v", out, err)
	}

	// Platform keys.
	pk, err := plat.ListPlatformKeys(ctx(t), adminapi.ListPlatformKeysInput{State: adminapi.KeyStateRevoked})
	if err != nil || len(pk.Items) != 0 {
		t.Errorf("revoked platform keys: %+v %v", pk, err)
	}
	if _, err := plat.RevokePlatformKey(ctx(t), adminapi.RevokePlatformKeyInput{Key: w.platformK.Key.ID}); err != nil {
		t.Fatal(err)
	}
	if pk, err = plat.ListPlatformKeys(ctx(t), adminapi.ListPlatformKeysInput{State: adminapi.KeyStateRevoked}); err != nil || len(pk.Items) != 1 {
		t.Errorf("revoked platform keys after a revocation: %+v %v", pk, err)
	}
}

// TestProblems is T-235.
func TestProblems(t *testing.T) {
	w := newWorld(t, "problems")
	d := w.api.DB
	type row struct {
		code          adminapi.ProblemCode
		status        int
		title, detail string
		get           func() *apitest.Response
	}
	suspendedName := "problems-suspended"
	suspended, suspendedKey := w.api.Tenant(t, suspendedName)
	if _, err := w.api.Client(w.platform).SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: suspended.ID}); err != nil {
		t.Fatal(err)
	}
	deleting, _ := w.api.Tenant(t, "problems-deleting")
	if _, err := d.Super(t).Exec(ctx(t), "UPDATE pandokos.tenant SET state = 'deleting' WHERE id = $1::uuid", deleting.ID); err != nil {
		t.Fatal(err)
	}
	// The two codes of sprint 2.2: a remote price import whose download fails, and whose
	// document cannot be read. The handler gets its fetch function from the options.
	remote := func(fetch func(context.Context, string) ([]byte, error)) *apitest.API {
		return apitest.New(t, func(o *admin.Options) {
			o.Refresh.Enabled, o.Refresh.URL, o.Refresh.Fetch = true, "https://prices.example.com/data.json", fetch
		})
	}
	failing := remote(func(context.Context, string) ([]byte, error) { return nil, &catalog.FetchError{Class: "status"} })
	garbled := remote(func(context.Context, string) ([]byte, error) { return []byte(`{"not": "a dataset"}`), nil })
	rows := []row{
		{adminapi.ProblemCodeInvalidRequest, 400, "Invalid request", "The request does not match the API description.",
			func() *apitest.Response { return w.api.Do(t, "POST", "/v1/tenants", w.platform, map[string]any{}, nil) }},
		{adminapi.ProblemCodeUnauthenticated, 401, "Unauthenticated", "Missing or invalid credential.",
			func() *apitest.Response { return w.api.Do(t, "GET", "/v1/me", "", nil, nil) }},
		{adminapi.ProblemCodeForbidden, 403, "Forbidden", "The credential does not allow this operation.",
			func() *apitest.Response { return w.api.Do(t, "GET", "/v1/tenants", w.adminKey, nil, nil) }},
		{adminapi.ProblemCodeTenantSuspended, 403, "Tenant not active", "The tenant is not active.",
			func() *apitest.Response {
				return w.api.Do(t, "GET", "/v1/tenants/"+suspended.ID+"/service-accounts", suspendedKey, nil, nil)
			}},
		{adminapi.ProblemCodeNotFound, 404, "Not found", "The resource does not exist.",
			func() *apitest.Response { return w.api.Do(t, "GET", "/v1/nope", w.platform, nil, nil) }},
		{adminapi.ProblemCodeConflict, 409, "Conflict", "The name is already in use.",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants", w.platform, map[string]any{"name": "problems"}, nil)
			}},
		{adminapi.ProblemCodeInvalidState, 409, "Invalid state", "The resource is in a state that does not allow this operation.",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+deleting.ID+"/suspend", w.platform, map[string]any{}, nil)
			}},
		{adminapi.ProblemCodeRequestTooLarge, 413, "Request too large", "The request body exceeds 1048576 bytes.",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants", w.platform, `{"name":"x","display_name":"`+strings.Repeat("y", 1<<20)+`"}`, nil)
			}},
		{adminapi.ProblemCodeUnsupportedMediaType, 415, "Unsupported media type", "The request body must be application/json.",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants", w.platform, `{"name":"x"}`, http.Header{"Content-Type": {"text/plain"}})
			}},
	}
	rows = append(rows,
		row{adminapi.ProblemCodePriceSourceUnavailable, 502, "Price source unavailable", "The price dataset could not be downloaded. Nothing was imported.",
			func() *apitest.Response {
				return failing.Do(t, "POST", "/v1/price-imports", failing.PlatformKey(), map[string]any{"source": "remote"}, nil)
			}},
		row{adminapi.ProblemCodePriceDatasetInvalid, 422, "Price dataset invalid", "The price dataset could not be read. Nothing was imported.",
			func() *apitest.Response {
				return garbled.Do(t, "POST", "/v1/price-imports", garbled.PlatformKey(), map[string]any{"source": "remote"}, nil)
			}})
	for _, r := range rows {
		resp := r.get()
		p := expect(t, resp, r.status, r.code)
		if p.Title != r.title || p.Detail != r.detail {
			t.Errorf("%s: title %q, detail %q", r.code, p.Title, p.Detail)
		}
		if r.code != adminapi.ProblemCodeInvalidRequest && len(p.Errors) != 0 {
			t.Errorf("%s carries errors: %+v", r.code, p.Errors)
		}
	}
	// invalid_request names the member and the rule, and never repeats the value.
	const canary = "Canary-Value-4711"
	resp := w.api.Do(t, "POST", "/v1/tenants", w.platform, map[string]any{"name": canary}, nil)
	p := expect(t, resp, 400, adminapi.ProblemCodeInvalidRequest)
	if len(p.Errors) != 1 || p.Errors[0].Field != "name" || !strings.Contains(p.Errors[0].Message, "^[a-z0-9]") || strings.Contains(string(resp.Body), canary) {
		t.Errorf("errors: %+v", p.Errors)
	}

	// A forced database error: the admin user loses a privilege.
	if _, err := d.Super(t).Exec(ctx(t), "REVOKE SELECT ON pandokos.tenant FROM pandokos_admin"); err != nil {
		t.Fatal(err)
	}
	resp = w.api.Do(t, "GET", "/v1/tenants", w.platform, nil, nil)
	p = expect(t, resp, 500, adminapi.ProblemCodeInternal)
	if p.Title != "Internal error" || p.Detail != "Internal error." {
		t.Errorf("internal: %+v", p)
	}
	var generic map[string]any
	if err := json.Unmarshal(resp.Body, &generic); err != nil {
		t.Fatal(err)
	}
	delete(generic, "type") // the type URN names the product and the code; the rest is what could leak
	rest, _ := json.Marshal(generic)
	for _, leak := range []string{"permission", "tenant", "pandokos", "SQLSTATE", "42501", "SELECT", "denied"} {
		if strings.Contains(string(rest), leak) {
			t.Errorf("the 500 body contains %q: %s", leak, resp.Body)
		}
	}
	line := lastLine(t, w.api.Logs, "internal error")
	if line["operation"] != "listTenants" || line["sqlstate"] != "42501" || line["request_id"] != resp.Header.Get("X-Pandokos-Request-Id") {
		t.Errorf("the log line of the failure: %v", line)
	}
	if strings.Contains(w.api.Logs.String(), "permission denied") {
		t.Error("the database's message was logged")
	}
	if _, err := d.Super(t).Exec(ctx(t), "GRANT SELECT ON pandokos.tenant TO pandokos_admin"); err != nil {
		t.Fatal(err)
	}

	// A database that refuses connections is 503.
	d.SetAcceptingConnections(t, false)
	resp = w.api.Do(t, "GET", "/v1/tenants", w.platform, nil, nil)
	p = expect(t, resp, 503, adminapi.ProblemCodeUnavailable)
	if p.Title != "Unavailable" || p.Detail != "The request cannot be verified or recorded right now." {
		t.Errorf("unavailable: %+v", p)
	}
	d.SetAcceptingConnections(t, true)
	// The key shows up in no problem body.
	testutil.AssertAbsent(t, "the log", w.api.Logs.String(), w.api.Secrets()...)
}

func lastLine(t testing.TB, logs *testutil.LogBuffer, msg string) map[string]any {
	t.Helper()
	l := logs.Find(t, msg)
	if len(l) == 0 {
		t.Fatalf("no %q log line in:\n%s", msg, logs.String())
	}
	return l[len(l)-1]
}
