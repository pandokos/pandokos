package admin_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// detailValues returns every scalar value of a detail, to find a value without knowing its key.
func detailValues(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			detailValues(e, out)
		}
	case []any:
		for _, e := range x {
			detailValues(e, out)
		}
	case string:
		*out = append(*out, x)
	}
}

func containsValue(detail map[string]any, want string) bool {
	var vals []string
	detailValues(detail, &vals)
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

// TestIssuerAudit is T-590: each of the ten actions writes one row in the right log, in the
// transaction of the change, with the actor, the resource and the detail of section 5.12; a change
// that fails writes none, and one whose audit row cannot be written changes nothing.
func TestIssuerAudit(t *testing.T) {
	t.Parallel()
	w := newIW(t)
	api := w.api
	platID := api.PlatformKey()[4:21]
	admin := *me(t, api, w.ka)
	seen := seenIDs(t, api)
	tid := w.a.ID
	rid := func(r *apitest.Response) string { return r.Header.Get("X-Pandokos-Request-Id") }

	// expectOne checks that a step wrote exactly one row, in the log, with the actor of the key, and
	// returns it for the checks of its detail.
	expectOne := func(t *testing.T, step string, r *apitest.Response, status int, log, action, resourceType, resourceID string) auditRow {
		t.Helper()
		if r.Status != status {
			t.Fatalf("%s: status %d, want %d\n%s", step, r.Status, status, r.Body)
		}
		rows := newRows(t, api, seen)
		if len(rows) != 1 {
			t.Fatalf("%s: %d audit rows, want 1: %+v", step, len(rows), rows)
		}
		row := rows[0]
		want := auditRow{Log: log, Action: action, ResourceType: resourceType, ResourceID: resourceID, Outcome: "success", RequestID: rid(r)}
		if log == "platform" {
			want.ActorType, want.ActorID, want.ActorName, want.KeyID = "platform", platID, "apitest", platID
		} else {
			want.TenantID, want.ActorType, want.ActorID, want.ActorName, want.KeyID = tid, "service_account", admin.ID, admin.Name, admin.KeyID
		}
		got := row
		got.ID, got.Detail, got.Reason = "", nil, ""
		want.Detail = nil
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: row\n got %+v\nwant %+v", step, got, want)
		}
		if row.Reason != "" {
			t.Errorf("%s: a reason %q that nobody gave", step, row.Reason)
		}
		return row
	}
	// onlyKeys checks that the detail has these keys and no others.
	onlyKeys := func(t *testing.T, step string, d map[string]any, keys ...string) {
		t.Helper()
		var got []string
		for k := range d {
			got = append(got, k)
		}
		sort.Strings(got)
		sort.Strings(keys)
		if !reflect.DeepEqual(got, keys) {
			t.Errorf("%s: the detail has the keys %v, want %v: %v", step, got, keys, d)
		}
	}
	has := func(t *testing.T, step string, d map[string]any, key string, want any) {
		t.Helper()
		if !reflect.DeepEqual(d[key], want) {
			t.Errorf("%s: detail[%s] = %#v, want %#v (%v)", step, key, d[key], want, d)
		}
	}

	// The tenant-bound registration: create, update, a mapping, its deletion, the deletion of the registration.
	ti := uniqueIssuer(map[string]any{"description": "the realm", "algorithms": []string{"ES256", "PS256"}, "subject_claim": "azp", "required_claims": map[string]string{"tier": "gold"}})
	var created adminapi.TokenIssuer
	t.Run("token_issuer.create", func(t *testing.T) {
		r := w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/token-issuers", ti)
		_ = json.Unmarshal(r.Body, &created)
		row := expectOne(t, "createTokenIssuer", r, 201, "tenant", "token_issuer.create", "token_issuer", created.ID)
		for k, v := range map[string]any{"name": ti["name"], "issuer": ti["issuer"], "audience": ti["audience"], "enabled": true, "algorithms": []any{"ES256", "PS256"}} {
			has(t, "createTokenIssuer", row.Detail, k, v)
		}
		if row.Detail["description"] != nil {
			has(t, "createTokenIssuer", row.Detail, "description", "the realm")
		}
	})
	t.Run("token_issuer.update", func(t *testing.T) {
		r := w.do(t, w.ka, "PATCH", "/v1/tenants/"+tid+"/token-issuers/"+created.ID, map[string]any{"audience": "aud-changed", "enabled": false, "algorithms": []string{"RS256"}})
		row := expectOne(t, "updateTokenIssuer", r, 200, "tenant", "token_issuer.update", "token_issuer", created.ID)
		onlyKeys(t, "updateTokenIssuer", row.Detail, "changed", "audience", "enabled", "algorithms")
		has(t, "updateTokenIssuer", row.Detail, "changed", []any{"algorithms", "audience", "enabled"})
		has(t, "updateTokenIssuer", row.Detail, "audience", "aud-changed")
		has(t, "updateTokenIssuer", row.Detail, "enabled", false)
		has(t, "updateTokenIssuer", row.Detail, "algorithms", []any{"RS256"})
		// Enabling is a PATCH and says so.
		r = w.do(t, w.ka, "PATCH", "/v1/tenants/"+tid+"/token-issuers/"+created.ID, map[string]any{"enabled": true})
		row = expectOne(t, "enable", r, 200, "tenant", "token_issuer.update", "token_issuer", created.ID)
		onlyKeys(t, "enable", row.Detail, "changed", "enabled")
		has(t, "enable", row.Detail, "enabled", true)
		// A change of the claims the registration requires is named, whatever its detail.
		r = w.do(t, w.ka, "PATCH", "/v1/tenants/"+tid+"/token-issuers/"+created.ID, map[string]any{"required_claims": map[string]string{"env": "prod"}, "name": ti["name"].(string) + "-2"})
		row = expectOne(t, "claims", r, 200, "tenant", "token_issuer.update", "token_issuer", created.ID)
		has(t, "claims", row.Detail, "changed", []any{"name", "required_claims"})
		has(t, "claims", row.Detail, "name", ti["name"].(string)+"-2")
		// An update that changes nothing writes nothing.
		r = w.do(t, w.ka, "PATCH", "/v1/tenants/"+tid+"/token-issuers/"+created.ID, map[string]any{"enabled": true, "audience": "aud-changed"})
		if r.Status != 200 || len(newRows(t, api, seen)) != 0 {
			t.Errorf("an update that changes nothing: %d, wrote rows", r.Status)
		}
	})
	var mapping adminapi.SubjectMapping
	t.Run("subject_mapping.create and delete", func(t *testing.T) {
		r := w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/subject-mappings", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": created.ID}, "subject": "spiffe://acme/ns/ci", "service_account_id": w.saA.ID, "description": "ci"})
		_ = json.Unmarshal(r.Body, &mapping)
		row := expectOne(t, "createSubjectMapping", r, 201, "tenant", "subject_mapping.create", "subject_mapping", mapping.ID)
		if !containsValue(row.Detail, "spiffe://acme/ns/ci") || !containsValue(row.Detail, w.saA.ID) || !containsValue(row.Detail, created.ID) {
			t.Errorf("createSubjectMapping: the detail names neither the subject, the service account nor the registration: %v", row.Detail)
		}
		r = w.do(t, w.ka, "DELETE", "/v1/tenants/"+tid+"/subject-mappings/"+mapping.ID, nil)
		row = expectOne(t, "deleteSubjectMapping", r, 200, "tenant", "subject_mapping.delete", "subject_mapping", mapping.ID)
		if !containsValue(row.Detail, "spiffe://acme/ns/ci") || !containsValue(row.Detail, w.saA.ID) {
			t.Errorf("deleteSubjectMapping: the detail names neither the subject nor the service account: %v", row.Detail)
		}
		// A mapping to a platform-level registration is audited in the tenant's log, naming the registration.
		p := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+p.ID+"/tenants/"+tid, nil), 200)
		newRows(t, api, seen)
		r = w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/subject-mappings", map[string]any{"issuer": map[string]any{"scope": "platform", "id": p.ID}, "subject": "workload", "service_account_id": w.saA.ID})
		var pm adminapi.SubjectMapping
		_ = json.Unmarshal(r.Body, &pm)
		row = expectOne(t, "createSubjectMapping (platform-level)", r, 201, "tenant", "subject_mapping.create", "subject_mapping", pm.ID)
		if !containsValue(row.Detail, p.ID) {
			t.Errorf("the detail does not name the platform-level registration: %v", row.Detail)
		}
		w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+p.ID, nil)
		newRows(t, api, seen)
	})
	t.Run("token_issuer.delete", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/subject-mappings", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": created.ID}, "subject": fmt.Sprintf("s%d", i), "service_account_id": w.saA.ID}), 201)
		}
		newRows(t, api, seen)
		r := w.do(t, w.ka, "DELETE", "/v1/tenants/"+tid+"/token-issuers/"+created.ID, nil)
		row := expectOne(t, "deleteTokenIssuer", r, 200, "tenant", "token_issuer.delete", "token_issuer", created.ID)
		has(t, "deleteTokenIssuer", row.Detail, "name", ti["name"].(string)+"-2")
		has(t, "deleteTokenIssuer", row.Detail, "mappings_deleted", float64(2))
	})

	// The platform-level registration and its list.
	pbody := uniqueIssuer(map[string]any{"tenant_claim": "org", "algorithms": []string{"EdDSA"}, "required_claims": map[string]string{"tier": "gold"}})
	var plat adminapi.PlatformIssuer
	t.Run("platform_issuer.create, update, tenant_add, tenant_remove and delete", func(t *testing.T) {
		r := w.do(t, w.plat, "POST", "/v1/platform-issuers", pbody)
		_ = json.Unmarshal(r.Body, &plat)
		row := expectOne(t, "createPlatformIssuer", r, 201, "platform", "platform_issuer.create", "platform_issuer", plat.ID)
		for k, v := range map[string]any{"name": pbody["name"], "issuer": pbody["issuer"], "audience": pbody["audience"], "tenant_claim": "org", "enabled": true, "algorithms": []any{"EdDSA"}} {
			has(t, "createPlatformIssuer", row.Detail, k, v)
		}

		r = w.do(t, w.plat, "PATCH", "/v1/platform-issuers/"+plat.ID, map[string]any{"tenant_claim": "org_id", "enabled": false})
		row = expectOne(t, "updatePlatformIssuer", r, 200, "platform", "platform_issuer.update", "platform_issuer", plat.ID)
		onlyKeys(t, "updatePlatformIssuer", row.Detail, "changed", "enabled", "tenant_claim")
		has(t, "updatePlatformIssuer", row.Detail, "changed", []any{"enabled", "tenant_claim"})
		has(t, "updatePlatformIssuer", row.Detail, "tenant_claim", "org_id")
		has(t, "updatePlatformIssuer", row.Detail, "enabled", false)

		r = w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+plat.ID+"/tenants/"+tid, map[string]any{"claim_value": "org-a"})
		row = expectOne(t, "putPlatformIssuerTenant", r, 200, "platform", "platform_issuer.tenant_add", "platform_issuer", plat.ID)
		onlyKeys(t, "putPlatformIssuerTenant", row.Detail, "tenant_id", "claim_value")
		has(t, "putPlatformIssuerTenant", row.Detail, "tenant_id", tid)
		has(t, "putPlatformIssuerTenant", row.Detail, "claim_value", "org-a")
		// Adding the same tenant with the same value again changes nothing and writes nothing; a new value does.
		r = w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+plat.ID+"/tenants/"+tid, map[string]any{"claim_value": "org-a"})
		if r.Status != 200 || len(newRows(t, api, seen)) != 0 {
			t.Errorf("an idempotent PUT: %d, wrote rows", r.Status)
		}
		r = w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+plat.ID+"/tenants/"+tid, map[string]any{"claim_value": "org-a2"})
		row = expectOne(t, "putPlatformIssuerTenant (a new value)", r, 200, "platform", "platform_issuer.tenant_add", "platform_issuer", plat.ID)
		has(t, "putPlatformIssuerTenant (a new value)", row.Detail, "claim_value", "org-a2")

		r = w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+plat.ID+"/tenants/"+tid, nil)
		row = expectOne(t, "deletePlatformIssuerTenant", r, 200, "platform", "platform_issuer.tenant_remove", "platform_issuer", plat.ID)
		onlyKeys(t, "deletePlatformIssuerTenant", row.Detail, "tenant_id", "claim_value")
		has(t, "deletePlatformIssuerTenant", row.Detail, "tenant_id", tid)
		has(t, "deletePlatformIssuerTenant", row.Detail, "claim_value", "org-a2")

		// Mappings in two tenants: the platform session sees none of them, the row counts all three.
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+plat.ID+"/tenants/"+tid, nil), 200)
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+plat.ID+"/tenants/"+w.b.ID, nil), 200)
		for _, m := range []struct {
			tenant, key, sa, subject string
		}{{tid, w.ka, w.saA.ID, "p1"}, {tid, w.ka, w.saA.ID, "p2"}, {w.b.ID, w.kb, w.saB.ID, "p3"}} {
			decodeInto[adminapi.SubjectMapping](t, w.do(t, m.key, "POST", "/v1/tenants/"+m.tenant+"/subject-mappings",
				map[string]any{"issuer": map[string]any{"scope": "platform", "id": plat.ID}, "subject": m.subject, "service_account_id": m.sa}), 201)
		}
		newRows(t, api, seen)
		r = w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+plat.ID, nil)
		row = expectOne(t, "deletePlatformIssuer", r, 200, "platform", "platform_issuer.delete", "platform_issuer", plat.ID)
		has(t, "deletePlatformIssuer", row.Detail, "name", pbody["name"])
		has(t, "deletePlatformIssuer", row.Detail, "mappings_deleted", float64(3))
	})

	t.Run("a change that fails writes no row", func(t *testing.T) {
		first := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/token-issuers", uniqueIssuer(nil)), 201)
		p := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		newRows(t, api, seen)
		missing := "0192f0f0-0000-7000-8000-0000000000ee"
		for name, r := range map[string]*apitest.Response{
			"a conflict":                           w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/token-issuers", uniqueIssuer(map[string]any{"name": first.Name})),
			"an invalid body":                      w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/token-issuers", uniqueIssuer(map[string]any{"issuer": "http://x.example.test"})),
			"an update of a missing one":           w.do(t, w.ka, "PATCH", "/v1/tenants/"+tid+"/token-issuers/"+missing, map[string]any{"description": "x"}),
			"a delete of a missing one":            w.do(t, w.ka, "DELETE", "/v1/tenants/"+tid+"/token-issuers/"+missing, nil),
			"another tenant's key":                 w.do(t, w.kb, "DELETE", "/v1/tenants/"+tid+"/token-issuers/"+first.ID, nil),
			"a mapping to a missing one":           w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/subject-mappings", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": missing}, "subject": "x", "service_account_id": w.saA.ID}),
			"a mapping that is deleted":            w.do(t, w.ka, "DELETE", "/v1/tenants/"+tid+"/subject-mappings/"+missing, nil),
			"a platform conflict":                  w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org", "name": p.Name})),
			"a tenant of a missing list":           w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+missing+"/tenants/"+tid, nil),
			"a missing tenant":                     w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+p.ID+"/tenants/"+missing, nil),
			"a removal that is not there":          w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+p.ID+"/tenants/"+tid, nil),
			"a tenant key on a platform operation": w.do(t, w.ka, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})),
		} {
			if r.Status < 400 {
				t.Errorf("%s: status %d", name, r.Status)
			}
			if rows := newRows(t, api, seen); len(rows) != 0 {
				t.Errorf("%s wrote audit rows: %+v", name, rows)
			}
		}
		// Reads write nothing.
		for _, p := range []string{"/v1/tenants/" + tid + "/token-issuers", "/v1/tenants/" + tid + "/token-issuers/" + first.ID, "/v1/tenants/" + tid + "/subject-mappings", "/v1/tenants/" + tid + "/platform-issuers"} {
			if r := w.do(t, w.ka, "GET", p, nil); r.Status != 200 {
				t.Errorf("GET %s: %d", p, r.Status)
			}
		}
		for _, p := range []string{"/v1/platform-issuers", "/v1/platform-issuers/" + p.ID, "/v1/platform-issuers/" + p.ID + "/tenants"} {
			if r := w.do(t, w.plat, "GET", p, nil); r.Status != 200 {
				t.Errorf("GET %s: %d", p, r.Status)
			}
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("a read wrote audit rows: %+v", rows)
		}
	})

	t.Run("an audit record that cannot be written leaves the tables as they were", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/token-issuers", uniqueIssuer(nil)), 201)
		m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/subject-mappings", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": "x", "service_account_id": w.saA.ID}), 201)
		p := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+p.ID+"/tenants/"+w.b.ID, nil), 200)
		calls := map[string]func() *apitest.Response{
			"createTokenIssuer": func() *apitest.Response {
				return w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/token-issuers", uniqueIssuer(nil))
			},
			"updateTokenIssuer": func() *apitest.Response {
				return w.do(t, w.ka, "PATCH", "/v1/tenants/"+tid+"/token-issuers/"+ti.ID, map[string]any{"description": "changed"})
			},
			"deleteTokenIssuer": func() *apitest.Response {
				return w.do(t, w.ka, "DELETE", "/v1/tenants/"+tid+"/token-issuers/"+ti.ID, nil)
			},
			"createSubjectMapping": func() *apitest.Response {
				return w.do(t, w.ka, "POST", "/v1/tenants/"+tid+"/subject-mappings", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": "y", "service_account_id": w.saA.ID})
			},
			"deleteSubjectMapping": func() *apitest.Response {
				return w.do(t, w.ka, "DELETE", "/v1/tenants/"+tid+"/subject-mappings/"+m.ID, nil)
			},
			"createPlatformIssuer": func() *apitest.Response {
				return w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"}))
			},
			"updatePlatformIssuer": func() *apitest.Response {
				return w.do(t, w.plat, "PATCH", "/v1/platform-issuers/"+p.ID, map[string]any{"description": "changed"})
			},
			"deletePlatformIssuer": func() *apitest.Response { return w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+p.ID, nil) },
			"putPlatformIssuerTenant": func() *apitest.Response {
				return w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+p.ID+"/tenants/"+tid, nil)
			},
			"deletePlatformIssuerTenant": func() *apitest.Response {
				return w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+p.ID+"/tenants/"+w.b.ID, nil)
			},
		}
		for _, op := range adminapi.Operations {
			if issuerMutations[op.ID] && calls[op.ID] == nil {
				t.Errorf("the operation %s has no call here", op.ID)
			}
		}
		super := api.Super(t)
		for _, q := range []string{
			`CREATE FUNCTION pandokos.zz_fail_issuers() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail_issuers()`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail_issuers()`,
		} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatal(err)
			}
		}
		for op, call := range calls {
			before := api.Snapshot(t)
			r := call()
			p := expect(t, r, 500, adminapi.ProblemCodeInternal)
			if strings.Contains(string(r.Body), "audit is down") || p.Detail != "Internal error." {
				t.Errorf("%s: the failure leaks: %s", op, r.Body)
			}
			if api.Snapshot(t) != before {
				t.Errorf("%s: a table changed although the audit record could not be written", op)
			}
		}
		for _, q := range []string{`DROP TRIGGER a_fail ON pandokos.audit_event`, `DROP TRIGGER a_fail ON pandokos.platform_audit_event`} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatal(err)
			}
		}
		for _, op := range []string{"createTokenIssuer", "deleteSubjectMapping", "putPlatformIssuerTenant"} {
			if r := calls[op](); r.Status != 200 && r.Status != 201 {
				t.Errorf("%s after the trigger is gone: %d %s", op, r.Status, r.Body)
			}
		}
	})

	t.Run("no row holds anything a registration could not publish", func(t *testing.T) {
		for _, r := range auditRows(t, api) {
			if !strings.HasPrefix(r.Action, "token_issuer.") && !strings.HasPrefix(r.Action, "subject_mapping.") && !strings.HasPrefix(r.Action, "platform_issuer.") {
				continue
			}
			text := fmt.Sprintf("%+v", r)
			for _, secret := range api.Secrets() {
				if strings.Contains(text, secret) {
					t.Errorf("the row of %s holds a key", r.Action)
				}
			}
		}
	})
}
