package admin_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestLimitAudit is T-677: each of the six actions writes one row in the right log, in the
// transaction of the change, with the actor, the resource and the detail of section 5.12; a change
// that is refused writes none, and one whose audit row cannot be written changes nothing.
func TestLimitAudit(t *testing.T) {
	t.Parallel()
	w := newLW(t)
	api := w.api
	platID := api.PlatformKey()[4:21]
	admin := *me(t, api, w.ka)
	seen := seenIDs(t, api)
	tid := w.a.ID
	rid := func(r *apitest.Response) string { return r.Header.Get("X-Pandokos-Request-Id") }

	one := func(t *testing.T, step string, r *apitest.Response, status int, log, action, resourceType, resourceID string) auditRow {
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
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: row\n got %+v\nwant %+v", step, got, want)
		}
		return row
	}
	has := func(t *testing.T, step string, d map[string]any, want map[string]any) {
		t.Helper()
		for k, v := range want {
			if !reflect.DeepEqual(d[k], v) {
				t.Errorf("%s: detail[%s] = %#v, want %#v (%v)", step, k, d[k], v, d)
			}
		}
	}
	nothing := func(t *testing.T, step string, d map[string]any, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := d[k]; ok {
				t.Errorf("%s: detail names %s, which did not change: %v", step, k, d)
			}
		}
	}

	var rule adminapi.LimitRule
	t.Run("limit.create", func(t *testing.T) {
		r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "cost_per_month", "amount": "250.50", "currency": "EUR", "enforce": false, "thresholds": []int{100, 80},
			"subject_kind": "service_account", "subject_id": w.saA.ID, "provider": w.cat.Provider.Name, "model": w.cat.All.Name})
		rule = decodeInto[adminapi.LimitRule](t, r, 201)
		row := one(t, "createLimit", r, 201, "tenant", "limit.create", "limit_rule", rule.ID)
		has(t, "createLimit", row.Detail, map[string]any{"subject_kind": "service_account", "subject_id": w.saA.ID, "provider": w.cat.Provider.Name, "model": w.cat.All.Name,
			"metric": "cost_per_month", "amount": "250.5", "currency": "EUR", "enforce": false, "thresholds": []any{float64(80), float64(100)}})
	})
	t.Run("limit.update", func(t *testing.T) {
		path := w.baseA + "/" + rule.ID
		r := w.do(t, w.ka, "PATCH", path, map[string]any{"amount": "300", "enforce": true, "thresholds": []int{}})
		row := one(t, "updateLimit", r, 200, "tenant", "limit.update", "limit_rule", rule.ID)
		has(t, "updateLimit", row.Detail, map[string]any{
			"subject_kind": "service_account", "metric": "cost_per_month",
			"amount": map[string]any{"from": "250.5", "to": "300"}, "enforce": map[string]any{"from": false, "to": true},
			"thresholds": map[string]any{"from": []any{float64(80), float64(100)}, "to": []any{}},
		})
		// Only what changed is named.
		r = w.do(t, w.ka, "PATCH", path, map[string]any{"amount": "300", "enforce": false, "thresholds": []int{70}})
		row = one(t, "updateLimit (two members change)", r, 200, "tenant", "limit.update", "limit_rule", rule.ID)
		nothing(t, "updateLimit (two members change)", row.Detail, "amount", "limit")
		has(t, "updateLimit (two members change)", row.Detail, map[string]any{"enforce": map[string]any{"from": true, "to": false}})
		// An update that changes nothing writes nothing.
		r = w.do(t, w.ka, "PATCH", path, map[string]any{"amount": "300", "enforce": false, "thresholds": []int{70}})
		if r.Status != 200 || len(newRows(t, api, seen)) != 0 {
			t.Errorf("an update that changes nothing: %d, wrote rows", r.Status)
		}
	})
	t.Run("limit.delete", func(t *testing.T) {
		r := w.do(t, w.ka, "DELETE", w.baseA+"/"+rule.ID, nil)
		row := one(t, "deleteLimit", r, 200, "tenant", "limit.delete", "limit_rule", rule.ID)
		has(t, "deleteLimit", row.Detail, map[string]any{"metric": "cost_per_month", "amount": "300", "currency": "EUR", "enforce": false, "subject_id": w.saA.ID})
	})

	var plat adminapi.PlatformLimitRule
	t.Run("platform_limit.create, update and delete", func(t *testing.T) {
		r := w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"tenant": tid, "metric": "tokens_per_day", "limit": 1000, "provider": w.cat.Provider.Name})
		plat = decodeInto[adminapi.PlatformLimitRule](t, r, 201)
		row := one(t, "createPlatformLimit", r, 201, "platform", "platform_limit.create", "platform_limit_rule", plat.ID)
		has(t, "createPlatformLimit", row.Detail, map[string]any{"tenant": tid, "metric": "tokens_per_day", "provider": w.cat.Provider.Name, "enforce": true})
		if !containsValue(row.Detail, "1000") && row.Detail["limit"] != float64(1000) {
			t.Errorf("createPlatformLimit: the detail does not hold the limit: %v", row.Detail)
		}
		// A default names no tenant.
		r = w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "requests_per_minute", "limit": 900})
		def := decodeInto[adminapi.PlatformLimitRule](t, r, 201)
		row = one(t, "createPlatformLimit (default)", r, 201, "platform", "platform_limit.create", "platform_limit_rule", def.ID)
		if v, ok := row.Detail["tenant"]; ok && v != "" && v != nil {
			t.Errorf("a default names the tenant %v", v)
		}

		r = w.do(t, w.plat, "PATCH", "/v1/platform-limits/"+plat.ID, map[string]any{"limit": 2000})
		row = one(t, "updatePlatformLimit", r, 200, "platform", "platform_limit.update", "platform_limit_rule", plat.ID)
		has(t, "updatePlatformLimit", row.Detail, map[string]any{"tenant": tid, "metric": "tokens_per_day", "limit": map[string]any{"from": float64(1000), "to": float64(2000)}})
		nothing(t, "updatePlatformLimit", row.Detail, "enforce", "thresholds")
		r = w.do(t, w.plat, "PATCH", "/v1/platform-limits/"+plat.ID, map[string]any{"limit": 2000})
		if r.Status != 200 || len(newRows(t, api, seen)) != 0 {
			t.Errorf("a platform update that changes nothing: %d, wrote rows", r.Status)
		}

		r = w.do(t, w.plat, "DELETE", "/v1/platform-limits/"+plat.ID, nil)
		row = one(t, "deletePlatformLimit", r, 200, "platform", "platform_limit.delete", "platform_limit_rule", plat.ID)
		has(t, "deletePlatformLimit", row.Detail, map[string]any{"tenant": tid, "metric": "tokens_per_day"})
		w.do(t, w.plat, "DELETE", "/v1/platform-limits/"+def.ID, nil)
		newRows(t, api, seen)
	})

	t.Run("a change that is refused writes no row", func(t *testing.T) {
		first := w.create(t, map[string]any{"metric": "tokens_per_month", "limit": 5})
		pl := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_month", "limit": 5}), 201)
		newRows(t, api, seen)
		missing := "0192f0f0-0000-7000-8000-0000000000ee"
		for name, r := range map[string]*apitest.Response{
			"a conflict":                  w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "tokens_per_month", "limit": 6}),
			"an invalid body":             w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "tokens_per_month", "limit": 0, "provider": w.cat.Provider.Name}),
			"a subject that is not there": w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_day", "limit": 1, "subject_kind": "key", "subject_id": "tnosuchkeyid000000"}),
			"an update of a missing one":  w.do(t, w.ka, "PATCH", w.baseA+"/"+missing, map[string]any{"limit": 1}),
			"an invalid update":           w.do(t, w.ka, "PATCH", w.baseA+"/"+first.ID, map[string]any{"amount": "5"}),
			"a delete of a missing one":   w.do(t, w.ka, "DELETE", w.baseA+"/"+missing, nil),
			"another tenant's key":        w.do(t, w.kb, "DELETE", w.baseA+"/"+first.ID, nil),
			"a platform conflict":         w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_month", "limit": 9}),
			"a platform missing tenant":   w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"tenant": missing, "metric": "tokens_per_month", "limit": 9}),
			"a platform update of none":   w.do(t, w.plat, "PATCH", "/v1/platform-limits/"+missing, map[string]any{"limit": 1}),
			"a tenant key on a platform":  w.do(t, w.ka, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_day", "limit": 1}),
			"a platform key on a tenant":  w.do(t, w.plat, "POST", w.baseA, map[string]any{"metric": "tokens_per_day", "limit": 1}),
		} {
			if r.Status < 400 {
				t.Errorf("%s: status %d", name, r.Status)
			}
			if rows := newRows(t, api, seen); len(rows) != 0 {
				t.Errorf("%s wrote audit rows: %+v", name, rows)
			}
		}
		for _, p := range []string{w.baseA, w.baseA + "/" + first.ID, "/v1/tenants/" + tid + "/platform-limits", "/v1/tenants/" + tid + "/limit-events"} {
			if r := w.do(t, w.ka, "GET", p, nil); r.Status != 200 {
				t.Errorf("GET %s: %d", p, r.Status)
			}
		}
		for _, p := range []string{"/v1/platform-limits", "/v1/platform-limits/" + pl.ID} {
			if r := w.do(t, w.plat, "GET", p, nil); r.Status != 200 {
				t.Errorf("GET %s: %d", p, r.Status)
			}
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("a read wrote audit rows: %+v", rows)
		}
	})

	t.Run("an audit record that cannot be written leaves the tables as they were", func(t *testing.T) {
		keep := w.create(t, map[string]any{"metric": "requests_per_month", "limit": 8})
		pk := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "requests_per_month", "limit": 8}), 201)
		newRows(t, api, seen)
		super := api.Super(t)
		for _, q := range []string{
			`CREATE FUNCTION pandokos.zz_fail_limits() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail_limits()`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail_limits()`,
		} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		for op, call := range map[string]func() *apitest.Response{
			"createLimit": func() *apitest.Response {
				return w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "tokens_per_day", "limit": 3})
			},
			"updateLimit": func() *apitest.Response {
				return w.do(t, w.ka, "PATCH", w.baseA+"/"+keep.ID, map[string]any{"limit": 99})
			},
			"deleteLimit": func() *apitest.Response { return w.do(t, w.ka, "DELETE", w.baseA+"/"+keep.ID, nil) },
			"createPlatformLimit": func() *apitest.Response {
				return w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_day", "limit": 3})
			},
			"updatePlatformLimit": func() *apitest.Response {
				return w.do(t, w.plat, "PATCH", "/v1/platform-limits/"+pk.ID, map[string]any{"limit": 99})
			},
			"deletePlatformLimit": func() *apitest.Response { return w.do(t, w.plat, "DELETE", "/v1/platform-limits/"+pk.ID, nil) },
		} {
			before := api.Snapshot(t)
			resp := call()
			p := expect(t, resp, 500, adminapi.ProblemCodeInternal)
			if strings.Contains(string(resp.Body), "audit is down") || p.Detail != "Internal error." {
				t.Errorf("%s: the failure leaks: %s", op, resp.Body)
			}
			if after := api.Snapshot(t); after != before {
				t.Errorf("%s: a table changed although the audit record could not be written", op)
			}
		}
		for _, q := range []string{`DROP TRIGGER a_fail ON pandokos.audit_event`, `DROP TRIGGER a_fail ON pandokos.platform_audit_event`} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatal(err)
			}
		}
		if r := w.do(t, w.ka, "PATCH", w.baseA+"/"+keep.ID, map[string]any{"limit": 99}); r.Status != 200 {
			t.Errorf("the same update once the trigger is gone: %d %s", r.Status, r.Body)
		}
	})
}

// TestLimitPermissions is T-678: each of the twelve operations needs its permission: it is refused to
// the role inference, to a key scoped without it, to a platform key on a tenant route and to a tenant
// key on a platform route.
func TestLimitPermissions(t *testing.T) {
	t.Parallel()
	w := newLW(t)
	rule := w.create(t, map[string]any{"metric": "requests_per_day", "limit": 5})
	pl := decodeInto[adminapi.PlatformLimitRule](t, w.do(t, w.plat, "POST", "/v1/platform-limits", map[string]any{"tenant": w.a.ID, "metric": "requests_per_day", "limit": 5}), 201)

	type op struct {
		id, method, path string
		body             map[string]any
		perm             string
		platform         bool
	}
	base := "/v1/tenants/" + w.a.ID
	ops := []op{
		{"listLimits", "GET", base + "/limits", nil, "limit:read", false},
		{"createLimit", "POST", base + "/limits", map[string]any{"metric": "tokens_per_day", "limit": 1}, "limit:write", false},
		{"getLimit", "GET", base + "/limits/" + rule.ID, nil, "limit:read", false},
		{"updateLimit", "PATCH", base + "/limits/" + rule.ID, map[string]any{"limit": 7}, "limit:write", false},
		{"deleteLimit", "DELETE", base + "/limits/" + rule.ID, nil, "limit:delete", false},
		{"listTenantPlatformLimits", "GET", base + "/platform-limits", nil, "limit:read", false},
		{"getLimitUsage", "GET", base + "/limits/" + rule.ID + "/usage", nil, "usage:read", false}, // sprint 6.2
		{"listLimitEvents", "GET", base + "/limit-events", nil, "limit:read", false},
		{"listPlatformLimits", "GET", "/v1/platform-limits", nil, "platform_limit:read", true},
		{"createPlatformLimit", "POST", "/v1/platform-limits", map[string]any{"metric": "tokens_per_day", "limit": 1}, "platform_limit:write", true},
		{"getPlatformLimit", "GET", "/v1/platform-limits/" + pl.ID, nil, "platform_limit:read", true},
		{"updatePlatformLimit", "PATCH", "/v1/platform-limits/" + pl.ID, map[string]any{"limit": 7}, "platform_limit:write", true},
		{"deletePlatformLimit", "DELETE", "/v1/platform-limits/" + pl.ID, nil, "platform_limit:delete", true},
	}
	// Every operation of the document is here.
	byID := map[string]bool{}
	for _, o := range ops {
		byID[o.id] = true
	}
	for _, o := range adminapi.Operations {
		if (strings.Contains(o.Path, "limit")) && !byID[o.ID] {
			t.Errorf("the operation %s is not in the table of this test", o.ID)
		}
	}
	for _, o := range ops {
		spec := ""
		for _, s := range adminapi.Operations {
			if s.ID == o.id {
				spec = s.Permission
			}
		}
		if spec != o.perm {
			t.Errorf("%s declares %q, the design says %q", o.id, spec, o.perm)
		}
	}

	// A key of a service account with the role inference, and keys scoped to everything but the permission.
	inference := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", base+"/service-accounts", map[string]any{"name": "inference-only", "roles": []string{"inference"}}), 201)
	infKey := decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", base+"/virtual-keys", map[string]any{"service_account_id": inference.ID}), 201)
	admin2 := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", base+"/service-accounts", map[string]any{"name": "scoped-admin", "roles": []string{"tenant-admin"}}), 201)
	scopedKey := func(perms ...string) string {
		k := decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", base+"/virtual-keys", map[string]any{"service_account_id": admin2.ID, "scopes": perms}), 201)
		return k.Secret
	}
	allTenant := []string{"limit:read", "limit:write", "limit:delete", "usage:read", "service_account:read", "virtual_key:read"}
	for _, o := range ops {
		do := func(key string) *apitest.Response { return w.do(t, key, o.method, o.path, o.body) }
		if o.platform {
			expect(t, do(w.ka), 403, adminapi.ProblemCodeForbidden) // a tenant key on a platform route
			expect(t, do(infKey.Secret), 403, adminapi.ProblemCodeForbidden)
			continue
		}
		expect(t, do(w.plat), 403, adminapi.ProblemCodeForbidden) // a platform key on a tenant route
		expect(t, do(infKey.Secret), 403, adminapi.ProblemCodeForbidden)
		var without []string
		for _, p := range allTenant {
			if p != o.perm {
				without = append(without, p)
			}
		}
		expect(t, do(scopedKey(without...)), 403, adminapi.ProblemCodeForbidden)
	}
	// The permission itself is enough, and no more: a key scoped to it alone does the operation.
	for _, o := range ops {
		if o.platform {
			continue
		}
		key := scopedKey(o.perm)
		r := w.do(t, key, o.method, o.path, o.body)
		if r.Status == 403 {
			t.Errorf("%s: refused to a key scoped to %s: %s", o.id, o.perm, r.Body)
		}
	}
	// A suspended tenant cannot be administered.
	if _, err := w.api.SuspendTenant(t, w.plat, w.b.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/limits", nil), 403, adminapi.ProblemCodeTenantSuspended)
	expect(t, w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/limits", map[string]any{"metric": "tokens_per_day", "limit": 1}), 403, adminapi.ProblemCodeTenantSuspended)
}
