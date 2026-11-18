package admin_test

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestSubjectDeletionDeletesRules is T-714, the exit condition of the sprint: revoking a key,
// deleting a mapping, a trusted issuer or a platform issuer deletes the rules of the subject in the
// transaction of the operation, each with its own limit.delete row, which names the actor, the
// request, the cause and every field of the rule.
func TestSubjectDeletionDeletesRules(t *testing.T) {
	w := newLW(t)
	api := w.api
	platID := api.PlatformKey()[4:21]
	actor := *me(t, api, w.ka)
	seen := seenIDs(t, api)
	rid := func(r *apitest.Response) string { return r.Header.Get("X-Pandokos-Request-Id") }
	super := api.Super(t)
	exists := func(id string) bool {
		t.Helper()
		var n int
		if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.limit_rule WHERE id = $1::uuid`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	rule := func(base string, key string, body map[string]any) adminapi.LimitRule {
		t.Helper()
		return decodeInto[adminapi.LimitRule](t, w.do(t, key, "POST", base, body), 201)
	}
	deletes := func(rows []auditRow, id string) []auditRow {
		var out []auditRow
		for _, r := range rows {
			if r.Action == "limit.delete" && r.ResourceID == id {
				out = append(out, r)
			}
		}
		return out
	}
	opRow := func(t *testing.T, rows []auditRow, action string) auditRow {
		t.Helper()
		var found []auditRow
		for _, r := range rows {
			if r.Action == action {
				found = append(found, r)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%d rows %s in %+v", len(found), action, rows)
		}
		return found[0]
	}
	tenantActor := func(t *testing.T, step string, r auditRow, tenantID, request string) {
		t.Helper()
		if r.Log != "tenant" || r.TenantID != tenantID || r.ActorType != "service_account" || r.ActorID != actor.ID || r.ActorName != actor.Name || r.KeyID != actor.KeyID ||
			r.RequestID != request || r.Outcome != "success" || r.ResourceType != "limit_rule" {
			t.Errorf("%s: row %+v, want the actor of the operation, the tenant log and the request %s", step, r, request)
		}
	}
	fields := func(t *testing.T, step string, r auditRow, want map[string]any) {
		t.Helper()
		for k, v := range want {
			if !reflect.DeepEqual(r.Detail[k], v) {
				t.Errorf("%s: detail[%s] = %#v, want %#v (%v)", step, k, r.Detail[k], v, r.Detail)
			}
		}
	}
	scoped := func(subjectKind, subjectID, metric string, limit int) map[string]any {
		return map[string]any{"metric": metric, "limit": limit, "subject_kind": subjectKind, "subject_id": subjectID,
			"provider": w.cat.Provider.Name, "model": w.cat.All.Name}
	}
	providerID := func() string {
		var s string
		if err := super.QueryRow(ctx(t), `SELECT id::text FROM pandokos.provider WHERE name = $1`, w.cat.Provider.Name).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}()
	newKey := func(t *testing.T) string {
		t.Helper()
		sa := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"inference"}}), 201)
		return decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys", map[string]any{"service_account_id": sa.ID}), 201).Key.ID
	}
	revoke := func(key string, body any) *apitest.Response {
		return w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys/"+key+"/revoke", body)
	}
	newRows(t, api, seen)

	t.Run("revoking a key", func(t *testing.T) {
		key := newKey(t)
		r1 := rule(w.baseA, w.ka, scoped("key", key, "tokens_per_day", 10))
		r2 := rule(w.baseA, w.ka, map[string]any{"metric": "requests_per_month", "limit": 3, "subject_kind": "key", "subject_id": key})
		keep := rule(w.baseA, w.ka, map[string]any{"metric": "requests_per_month", "limit": 4, "subject_kind": "key", "subject_id": w.keyA})
		newRows(t, api, seen)
		resp := revoke(key, map[string]any{"reason": "leaked"})
		if resp.Status != 200 {
			t.Fatalf("revoke: %d\n%s", resp.Status, resp.Body)
		}
		rows := newRows(t, api, seen)
		for _, r := range []adminapi.LimitRule{r1, r2} {
			if exists(r.ID) {
				t.Errorf("the rule %s of the revoked key is still there", r.ID)
			}
			d := deletes(rows, r.ID)
			if len(d) != 1 {
				t.Fatalf("%d limit.delete rows for %s, want 1 (%+v)", len(d), r.ID, rows)
			}
			tenantActor(t, "revokeVirtualKey", d[0], w.a.ID, rid(resp))
			if d[0].Reason != "leaked" {
				t.Errorf("reason %q, want the reason of the request", d[0].Reason)
			}
			fields(t, "revokeVirtualKey", d[0], map[string]any{"cause": "key_revoked", "cause_id": key, "subject_kind": "key", "subject_id": key,
				"metric": string(r.Metric), "limit": float64(r.Limit), "enforce": true})
		}
		d1 := deletes(rows, r1.ID)[0]
		fields(t, "the scoped rule", d1, map[string]any{"provider": w.cat.Provider.Name, "model": w.cat.All.Name, "provider_id": providerID, "model_id": w.cat.All.ID})
		if !exists(keep.ID) {
			t.Error("the rule of another key was deleted")
		}
		op := opRow(t, rows, "virtual_key.revoke")
		if op.Detail["limit_rules_deleted"] != float64(2) {
			t.Errorf("limit_rules_deleted %v, want 2 (%v)", op.Detail["limit_rules_deleted"], op.Detail)
		}
		if len(rows) != 3 {
			t.Errorf("%d audit rows, want 3: the two rules and the revocation", len(rows))
		}
		// A revoked key that is revoked again changes and audits nothing.
		if r := revoke(key, map[string]any{}); r.Status != 200 {
			t.Errorf("a second revocation: %d", r.Status)
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("a second revocation wrote %+v", rows)
		}
		// A key without rules: the count is 0.
		k2 := newKey(t)
		newRows(t, api, seen)
		revoke(k2, map[string]any{})
		rows = newRows(t, api, seen)
		if op := opRow(t, rows, "virtual_key.revoke"); op.Detail["limit_rules_deleted"] != float64(0) {
			t.Errorf("a key without rules: limit_rules_deleted %v", op.Detail["limit_rules_deleted"])
		}
	})

	t.Run("deleting a subject mapping", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": "ci", "service_account_id": w.saA.ID}), 201)
		r1 := rule(w.baseA, w.ka, scoped("key", m.ID, "tokens_per_day", 10))
		newRows(t, api, seen)
		resp := w.do(t, w.ka, "DELETE", "/v1/tenants/"+w.a.ID+"/subject-mappings/"+m.ID, nil)
		if resp.Status != 200 {
			t.Fatalf("delete: %d\n%s", resp.Status, resp.Body)
		}
		rows := newRows(t, api, seen)
		d := deletes(rows, r1.ID)
		if exists(r1.ID) || len(d) != 1 {
			t.Fatalf("the rule exists %v, %d rows", exists(r1.ID), len(d))
		}
		tenantActor(t, "deleteSubjectMapping", d[0], w.a.ID, rid(resp))
		fields(t, "deleteSubjectMapping", d[0], map[string]any{"cause": "subject_mapping_deleted", "cause_id": m.ID, "subject_id": m.ID, "subject_kind": "key"})
		if op := opRow(t, rows, "subject_mapping.delete"); op.Detail["limit_rules_deleted"] != float64(1) {
			t.Errorf("limit_rules_deleted %v", op.Detail["limit_rules_deleted"])
		}
		// A mapping that does not exist answers 404 and touches no rule.
		keep := rule(w.baseA, w.ka, map[string]any{"metric": "requests_per_day", "limit": 3, "subject_kind": "key", "subject_id": w.mappingA})
		expect(t, w.do(t, w.ka, "DELETE", "/v1/tenants/"+w.a.ID+"/subject-mappings/0192f0f0-0000-7000-8000-0000000000ee", nil), 404, adminapi.ProblemCodeNotFound)
		if !exists(keep.ID) {
			t.Error("a 404 deleted a rule")
		}
		newRows(t, api, seen)
	})

	t.Run("deleting a trusted issuer", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		var rules []adminapi.LimitRule
		for i := 0; i < 2; i++ {
			m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
				map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": fmt.Sprintf("u%d", i), "service_account_id": w.saA.ID}), 201)
			rules = append(rules, rule(w.baseA, w.ka, scoped("key", m.ID, "tokens_per_day", 10+i)))
		}
		other := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		om := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "tenant", "id": other.ID}, "subject": "other", "service_account_id": w.saA.ID}), 201)
		keep := rule(w.baseA, w.ka, scoped("key", om.ID, "tokens_per_day", 99))
		newRows(t, api, seen)
		resp := w.do(t, w.ka, "DELETE", "/v1/tenants/"+w.a.ID+"/token-issuers/"+ti.ID, nil)
		if resp.Status != 200 {
			t.Fatalf("delete: %d\n%s", resp.Status, resp.Body)
		}
		rows := newRows(t, api, seen)
		for _, r := range rules {
			d := deletes(rows, r.ID)
			if exists(r.ID) || len(d) != 1 {
				t.Fatalf("rule %s exists %v, %d rows", r.ID, exists(r.ID), len(d))
			}
			tenantActor(t, "deleteTokenIssuer", d[0], w.a.ID, rid(resp))
			fields(t, "deleteTokenIssuer", d[0], map[string]any{"cause": "token_issuer_deleted", "cause_id": ti.ID})
		}
		if !exists(keep.ID) {
			t.Error("the rule of the mapping of another issuer was deleted")
		}
		if op := opRow(t, rows, "token_issuer.delete"); op.Detail["limit_rules_deleted"] != float64(2) {
			t.Errorf("limit_rules_deleted %v, want 2", op.Detail["limit_rules_deleted"])
		}
	})

	t.Run("deleting a platform issuer deletes the rules of its mappings in every tenant", func(t *testing.T) {
		pi := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		for _, tn := range []string{w.a.ID, w.b.ID} {
			decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+pi.ID+"/tenants/"+tn, map[string]any{}), 200)
		}
		mk := func(tn, key, sa, subject string) (adminapi.SubjectMapping, adminapi.LimitRule) {
			m := decodeInto[adminapi.SubjectMapping](t, w.do(t, key, "POST", "/v1/tenants/"+tn+"/subject-mappings",
				map[string]any{"issuer": map[string]any{"scope": "platform", "id": pi.ID}, "subject": subject, "service_account_id": sa}), 201)
			return m, rule("/v1/tenants/"+tn+"/limits", key, scoped("key", m.ID, "requests_per_month", 77))
		}
		mA, rA := mk(w.a.ID, w.ka, w.saA.ID, "plat-a")
		mB, rB := mk(w.b.ID, w.kb, w.saB.ID, "plat-b")
		// The same rule on a mapping of a tenant-bound issuer, deleted by its own operation: the details must agree.
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		mT := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": "tenant-bound", "service_account_id": w.saA.ID}), 201)
		rT := rule(w.baseA, w.ka, scoped("key", mT.ID, "requests_per_month", 77))
		newRows(t, api, seen)

		respT := w.do(t, w.ka, "DELETE", "/v1/tenants/"+w.a.ID+"/subject-mappings/"+mT.ID, nil)
		if respT.Status != 200 {
			t.Fatalf("delete of the mapping: %d", respT.Status)
		}
		viaHandler := deletes(newRows(t, api, seen), rT.ID)
		if len(viaHandler) != 1 {
			t.Fatalf("%d rows by the handler", len(viaHandler))
		}

		resp := w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+pi.ID, nil)
		if resp.Status != 200 {
			t.Fatalf("delete: %d\n%s", resp.Status, resp.Body)
		}
		rows := newRows(t, api, seen)
		for _, c := range []struct {
			tenant string
			rule   adminapi.LimitRule
			m      adminapi.SubjectMapping
		}{{w.a.ID, rA, mA}, {w.b.ID, rB, mB}} {
			d := deletes(rows, c.rule.ID)
			if exists(c.rule.ID) || len(d) != 1 {
				t.Fatalf("tenant %s: the rule exists %v, %d rows", c.tenant, exists(c.rule.ID), len(d))
			}
			r := d[0]
			if r.Log != "tenant" || r.TenantID != c.tenant || r.ActorType != "platform" || r.ActorID != platID || r.ActorName != "apitest" || r.KeyID != platID || r.RequestID != rid(resp) || r.Outcome != "success" {
				t.Errorf("tenant %s: row %+v, want the platform actor, the key and the request", c.tenant, r)
			}
			fields(t, "deletePlatformIssuer", r, map[string]any{"cause": "platform_issuer_deleted", "cause_id": pi.ID, "subject_kind": "key", "subject_id": c.m.ID,
				"metric": "requests_per_month", "limit": float64(77), "enforce": true, "provider": w.cat.Provider.Name, "model": w.cat.All.Name,
				"provider_id": providerID, "model_id": w.cat.All.ID})
			// Member by member, the detail is that of the handler's.
			strip := func(m map[string]any) map[string]any {
				out := map[string]any{}
				for k, v := range m {
					if k != "cause" && k != "cause_id" && k != "subject_id" {
						out[k] = v
					}
				}
				return out
			}
			if !reflect.DeepEqual(strip(r.Detail), strip(viaHandler[0].Detail)) {
				t.Errorf("the detail written by the function differs from the handler's:\n function %v\n handler  %v", strip(r.Detail), strip(viaHandler[0].Detail))
			}
		}
		op := opRow(t, rows, "platform_issuer.delete")
		if op.Log != "platform" || op.Detail["limit_rules_deleted"] != float64(2) {
			t.Errorf("the operation's own row: %+v", op)
		}
	})

	t.Run("a disabled service account keeps its rules", func(t *testing.T) {
		sa := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": apitest.UniqueName("dis"), "roles": []string{"inference"}}), 201)
		r := rule(w.baseA, w.ka, map[string]any{"metric": "requests_per_day", "limit": 3, "subject_kind": "service_account", "subject_id": sa.ID})
		newRows(t, api, seen)
		if resp := w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts/"+sa.ID+"/disable", map[string]any{}); resp.Status != 200 {
			t.Fatalf("disable: %d", resp.Status)
		}
		if !exists(r.ID) {
			t.Error("disabling a service account deleted its rule")
		}
		for _, row := range newRows(t, api, seen) {
			if row.Action == "limit.delete" {
				t.Errorf("a limit.delete row for a disabled account: %+v", row)
			}
		}
		w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts/"+sa.ID+"/enable", map[string]any{})
		if !exists(r.ID) {
			t.Error("enabling a service account removed its rule")
		}
	})

	t.Run("a failure after the rules are deleted leaves the subject and its rules", func(t *testing.T) {
		defer admin.SetAfterRuleDeletion(func() error { return errors.New("injected after the deletion of the rules") })()
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": "rollback", "service_account_id": w.saA.ID}), 201)
		pi := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+pi.ID+"/tenants/"+w.a.ID, map[string]any{}), 200)
		pm := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "platform", "id": pi.ID}, "subject": "rollback-p", "service_account_id": w.saA.ID}), 201)
		key := newKey(t)
		keyRule := rule(w.baseA, w.ka, scoped("key", key, "tokens_per_day", 10))
		mRule := rule(w.baseA, w.ka, scoped("key", m.ID, "tokens_per_day", 10))
		pRule := rule(w.baseA, w.ka, scoped("key", pm.ID, "tokens_per_day", 10))
		newRows(t, api, seen)
		before := api.SnapshotTenant(t, w.a.ID)

		for name, resp := range map[string]*apitest.Response{
			"revokeVirtualKey":     revoke(key, map[string]any{}),
			"deleteSubjectMapping": w.do(t, w.ka, "DELETE", "/v1/tenants/"+w.a.ID+"/subject-mappings/"+m.ID, nil),
			"deleteTokenIssuer":    w.do(t, w.ka, "DELETE", "/v1/tenants/"+w.a.ID+"/token-issuers/"+ti.ID, nil),
			"deletePlatformIssuer": w.do(t, w.plat, "DELETE", "/v1/platform-issuers/"+pi.ID, nil),
		} {
			if resp.Status != 500 {
				t.Errorf("%s: status %d, want 500 from the injected failure\n%s", name, resp.Status, resp.Body)
			}
		}
		for name, id := range map[string]string{"the key's rule": keyRule.ID, "the mapping's rule": mRule.ID, "the platform mapping's rule": pRule.ID} {
			if !exists(id) {
				t.Errorf("%s was deleted although the transaction failed", name)
			}
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("a failed operation wrote audit rows: %+v", rows)
		}
		if got := api.SnapshotTenant(t, w.a.ID); got != before {
			t.Error("a failed operation changed the tenant")
		}
		if r := w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/subject-mappings/"+m.ID, nil); r.Status != 200 {
			t.Errorf("the mapping is gone: %d", r.Status)
		}
		if r := w.do(t, w.plat, "GET", "/v1/platform-issuers/"+pi.ID, nil); r.Status != 200 {
			t.Errorf("the platform issuer is gone: %d", r.Status)
		}
	})

	t.Run("createLimit racing a revocation leaves no rule", func(t *testing.T) {
		const rounds = 8
		for i := 0; i < rounds; i++ {
			key := newKey(t)
			var wg sync.WaitGroup
			var created, revoked int
			wg.Add(2)
			go func() {
				defer wg.Done()
				r := w.do(t, w.ka, "POST", w.baseA, map[string]any{"metric": "requests_per_day", "limit": 3, "subject_kind": "key", "subject_id": key})
				if r.Status == 201 {
					created = 1
				} else if r.Status != 400 {
					t.Errorf("round %d: createLimit answered %d\n%s", i, r.Status, r.Body)
				}
			}()
			go func() {
				defer wg.Done()
				r := revoke(key, map[string]any{})
				if r.Status != 200 {
					t.Errorf("round %d: revoke answered %d\n%s", i, r.Status, r.Body)
				}
				revoked = 1
			}()
			wg.Wait()
			var n int
			if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.limit_rule r JOIN pandokos.virtual_key k ON k.id = r.virtual_key_id WHERE k.key_id = $1`, key).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 || revoked != 1 {
				t.Fatalf("round %d: %d rules of a revoked key (created %d)", i, n, created)
			}
			var del int
			rows := newRows(t, api, seen)
			for _, r := range rows {
				if r.Action == "limit.delete" && r.Detail["subject_id"] == key {
					del++
				}
			}
			if del != created {
				t.Errorf("round %d: %d limit.delete rows for %d rules created", i, del, created)
			}
		}
	})
}
