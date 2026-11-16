package admin_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

func namesMember(p adminapi.Problem, member string) bool {
	for _, f := range fieldsOf(p) {
		if f == member || strings.HasPrefix(f, member+".") || strings.HasPrefix(f, member+"[") || strings.HasPrefix(f, member+"/") {
			return true
		}
	}
	return false
}

var defaultAlgorithms = []string{"RS256", "ES256"}

// TestTokenIssuers is T-589 (the tenant-bound registrations): creation with defaults, each rule of
// section 5.3 with its field, pagination and sorting, the filters, the conflicts, update and delete.
func TestTokenIssuers(t *testing.T) {
	w := newIW(t)
	base := "/v1/tenants/" + w.a.ID + "/token-issuers"

	t.Run("creation with the defaults", func(t *testing.T) {
		body := uniqueIssuer(nil)
		before := w.api.SnapshotTenant(t, w.b.ID)
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, body), 201)
		if ti.TenantID != w.a.ID || ti.Name != body["name"] || ti.Issuer != body["issuer"] || ti.Audience != body["audience"] || ti.Description != "" || ti.JwksUri != "" ||
			!reflect.DeepEqual(ti.Algorithms, defaultAlgorithms) || ti.SubjectClaim != "sub" || len(ti.RequiredClaims) != 0 || ti.RequiredClaims == nil || !ti.Enabled ||
			len(ti.ID) != 36 || ti.CreatedAt.IsZero() || !ti.UpdatedAt.Equal(ti.CreatedAt) && ti.UpdatedAt.Before(ti.CreatedAt) {
			t.Errorf("created: %+v", ti)
		}
		raw := w.do(t, w.ka, "GET", base+"/"+ti.ID, nil)
		if !strings.Contains(string(raw.Body), `"required_claims":{}`) || !strings.Contains(string(raw.Body), `"algorithms":["RS256","ES256"]`) {
			t.Errorf("the wire form of the defaults: %s", raw.Body)
		}
		if got := decodeInto[adminapi.TokenIssuer](t, raw, 200); !reflect.DeepEqual(got, ti) {
			t.Errorf("get after create:\n%+v\n%+v", got, ti)
		}
		if w.api.SnapshotTenant(t, w.b.ID) != before {
			t.Error("a creation in A changed tenant B")
		}
	})

	t.Run("creation with every member", func(t *testing.T) {
		body := uniqueIssuer(map[string]any{"description": "the company realm", "jwks_uri": "https://keys.example.test/jwks?v=1", "algorithms": []string{"ES256", "PS256", "EdDSA"},
			"subject_claim": "azp", "required_claims": map[string]string{"tier": "gold", "env": "prod"}, "enabled": false})
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, body), 201)
		if ti.Description != "the company realm" || ti.JwksUri != "https://keys.example.test/jwks?v=1" || !reflect.DeepEqual(ti.Algorithms, []string{"ES256", "PS256", "EdDSA"}) ||
			ti.SubjectClaim != "azp" || !reflect.DeepEqual(ti.RequiredClaims, map[string]string{"tier": "gold", "env": "prod"}) || ti.Enabled {
			t.Errorf("created: %+v", ti)
		}
		// The limits that are allowed: 16 required claims, 10 algorithms, 255 characters.
		claims := map[string]string{}
		for i := 0; i < 16; i++ {
			claims[fmt.Sprintf("claim%d", i)] = strings.Repeat("v", 255)
		}
		edge := uniqueIssuer(map[string]any{"algorithms": []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"},
			"required_claims": claims, "audience": strings.Repeat("a", 255), "subject_claim": strings.Repeat("c", 64), "description": strings.Repeat("d", 200),
			"issuer": "https://" + strings.Repeat("i", 247)})
		if r := w.do(t, w.ka, "POST", base, edge); r.Status != 201 {
			t.Errorf("a registration at every limit: %d\n%s", r.Status, r.Body)
		}
		// A namespaced claim name is a claim name.
		ns := uniqueIssuer(map[string]any{"subject_claim": "https://claims.example.test/sub", "required_claims": map[string]string{"kubernetes.io:ns": "ci"}})
		if r := w.do(t, w.ka, "POST", base, ns); r.Status != 201 {
			t.Errorf("namespaced claim names: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("each rule of the registration, with its field", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.a.ID)
		for _, c := range registrationValidation() {
			body := uniqueIssuer(map[string]any{c.member: c.value})
			r := w.do(t, w.ka, "POST", base, body)
			if r.Status != 400 {
				t.Errorf("%s: status %d, want 400 on %s\n%s", c.what, r.Status, c.member, r.Body)
				continue
			}
			p := problem(t, r)
			if p.Code != adminapi.ProblemCodeInvalidRequest || !namesMember(p, c.field) {
				t.Errorf("%s: %+v does not name %s", c.what, p, c.field)
			}
			if s, _ := c.value.(string); len(s) > 8 && strings.Contains(string(r.Body), s) {
				t.Errorf("%s: the answer repeats the value", c.what)
			}
		}
		if w.api.SnapshotTenant(t, w.a.ID) != before {
			t.Error("a refused creation changed a row")
		}
		// Required members, an unknown member, a body that is not an object.
		for what, body := range map[string]any{
			"no name": map[string]any{"issuer": "https://x.example.test", "audience": "a"}, "no issuer": map[string]any{"name": "x", "audience": "a"},
			"no audience": map[string]any{"name": "x", "issuer": "https://x.example.test"}, "an unknown member": uniqueIssuer(map[string]any{"tenant_id": w.b.ID}),
			"a platform-only member": uniqueIssuer(map[string]any{"tenant_claim": "org"}), "an array": []any{}, "null": nil,
		} {
			if r := w.do(t, w.ka, "POST", base, body); r.Status != 400 && r.Status != 415 {
				t.Errorf("%s: status %d\n%s", what, r.Status, r.Body)
			}
		}
	})

	t.Run("a name or a pair that is taken is a 409 with the same body", func(t *testing.T) {
		first := uniqueIssuer(nil)
		decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, first), 201)
		byName := w.do(t, w.ka, "POST", base, uniqueIssuer(map[string]any{"name": first["name"]}))
		samePair := w.do(t, w.ka, "POST", base, uniqueIssuer(map[string]any{"issuer": first["issuer"], "audience": first["audience"]}))
		otherTenant := w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/token-issuers", uniqueIssuer(map[string]any{"issuer": first["issuer"], "audience": first["audience"]}))
		expect(t, byName, 409, adminapi.ProblemCodeConflict)
		expect(t, samePair, 409, adminapi.ProblemCodeConflict)
		expect(t, otherTenant, 409, adminapi.ProblemCodeConflict)
		sameProblem(t, "a taken pair and a taken name", byName, samePair)
		sameProblem(t, "a pair taken by another tenant and a taken name", byName, otherTenant)
		if strings.Contains(string(otherTenant.Body), w.a.ID) || strings.Contains(string(otherTenant.Body), first["issuer"].(string)) {
			t.Errorf("the conflict tells who holds the pair: %s", otherTenant.Body)
		}
		// Another audience of the same issuer is another pair; the same name in another tenant is fine.
		if r := w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/token-issuers", uniqueIssuer(map[string]any{"issuer": first["issuer"], "name": first["name"]})); r.Status != 201 {
			t.Errorf("the same issuer, another audience, and the same name in another tenant: %d\n%s", r.Status, r.Body)
		}
		// Byte for byte: another case is another pair.
		if r := w.do(t, w.ka, "POST", base, uniqueIssuer(map[string]any{"issuer": strings.ToUpper(first["issuer"].(string)[:12]) + first["issuer"].(string)[12:], "audience": first["audience"]})); r.Status != 201 {
			t.Errorf("an issuer in another case: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("pagination, sorting and filters", func(t *testing.T) {
		pw := newIW(t)
		pbase := "/v1/tenants/" + pw.a.ID + "/token-issuers"
		for _, n := range []string{"delta", "alpha", "echo", "bravo", "charlie"} {
			decodeInto[adminapi.TokenIssuer](t, pw.do(t, pw.ka, "POST", pbase, uniqueIssuer(map[string]any{"name": n, "enabled": n != "echo"})), 201)
		}
		list := func(query string) adminapi.TokenIssuerList {
			return decodeInto[adminapi.TokenIssuerList](t, pw.do(t, pw.ka, "GET", pbase+query, nil), 200)
		}
		names := func(l adminapi.TokenIssuerList) string {
			var out []string
			for _, i := range l.Items {
				out = append(out, i.Name)
			}
			return strings.Join(out, ",")
		}
		if got := names(list("")); got != "delta,alpha,echo,bravo,charlie" {
			t.Errorf("the default order is the creation order: %s", got)
		}
		if got := names(list("?sort=-created_at")); got != "charlie,bravo,echo,alpha,delta" {
			t.Errorf("-created_at: %s", got)
		}
		if got := names(list("?sort=name")); got != "alpha,bravo,charlie,delta,echo" {
			t.Errorf("name: %s", got)
		}
		if got := names(list("?sort=-name")); got != "echo,delta,charlie,bravo,alpha" {
			t.Errorf("-name: %s", got)
		}
		for _, sort := range []string{"created_at", "-created_at", "name", "-name"} {
			var all []string
			cursor := ""
			for pages := 0; ; pages++ {
				l := list("?sort=" + sort + "&limit=2" + cursor)
				if len(l.Items) > 2 || pages > 5 {
					t.Fatalf("sort %s: a page of %d items, %d pages", sort, len(l.Items), pages)
				}
				all = append(all, names(l))
				if l.NextCursor == "" {
					break
				}
				cursor = "&cursor=" + l.NextCursor
			}
			full := names(list("?sort=" + sort))
			if got := strings.Join(all, ","); got != full {
				t.Errorf("sort %s: the pages give %s, the whole list %s", sort, got, full)
			}
		}
		if got := names(list("?name=bravo")); got != "bravo" {
			t.Errorf("name filter: %s", got)
		}
		if got := names(list("?name=nobody")); got != "" {
			t.Errorf("name filter, none: %s", got)
		}
		if got := names(list("?enabled=false")); got != "echo" {
			t.Errorf("enabled=false: %s", got)
		}
		if got := names(list("?enabled=true&sort=name")); got != "alpha,bravo,charlie,delta" {
			t.Errorf("enabled=true: %s", got)
		}
		for _, q := range []string{"?enabled=yes", "?sort=issuer", "?limit=0", "?limit=1000", "?cursor=garbage", "?limit=x"} {
			if r := pw.do(t, pw.ka, "GET", pbase+q, nil); r.Status != 400 {
				t.Errorf("%s: status %d, want 400\n%s", q, r.Status, r.Body)
			}
		}
		// Another tenant's list is its own.
		if l := decodeInto[adminapi.TokenIssuerList](t, pw.do(t, pw.kb, "GET", "/v1/tenants/"+pw.b.ID+"/token-issuers", nil), 200); len(l.Items) != 0 {
			t.Errorf("tenant B lists %d registrations of A", len(l.Items))
		}
	})

	t.Run("get, and the ID of a registration that is not there", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, uniqueIssuer(nil)), 201)
		if r := w.do(t, w.ka, "GET", base+"/"+ti.ID, nil); r.Status != 200 {
			t.Errorf("get: %d", r.Status)
		}
		expect(t, w.do(t, w.ka, "GET", base+"/0192f0f0-0000-7000-8000-0000000000ee", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.kb, "GET", base+"/"+ti.ID, nil), 404, adminapi.ProblemCodeNotFound)
		if r := w.do(t, w.ka, "GET", base+"/not-an-id", nil); r.Status != 404 {
			t.Errorf("a malformed ID: %d", r.Status)
		}
	})

	t.Run("update", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, uniqueIssuer(map[string]any{"description": "first", "required_claims": map[string]string{"a": "b"},
			"algorithms": []string{"ES256", "EdDSA"}, "jwks_uri": "https://keys.example.test/k"})), 201)
		path := base + "/" + ti.ID
		// Members that are not sent are left as they are.
		up := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "PATCH", path, map[string]any{"audience": "aud-changed", "enabled": false}), 200)
		want := ti
		want.Audience, want.Enabled = "aud-changed", false
		want.UpdatedAt = up.UpdatedAt
		if !reflect.DeepEqual(up, want) || up.UpdatedAt.Before(ti.UpdatedAt) {
			t.Errorf("updated:\n%+v\nwant\n%+v", up, want)
		}
		// Arrays and objects replace the whole value; an empty string clears; a boolean is set to false and back.
		up = decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "PATCH", path, map[string]any{"algorithms": []string{"RS256"}, "required_claims": map[string]string{}, "jwks_uri": "", "description": "",
			"subject_claim": "client_id", "name": ti.Name + "-2", "issuer": "https://changed-" + ti.Name + ".example.test", "enabled": true}), 200)
		if !reflect.DeepEqual(up.Algorithms, []string{"RS256"}) || len(up.RequiredClaims) != 0 || up.JwksUri != "" || up.Description != "" || up.SubjectClaim != "client_id" ||
			up.Name != ti.Name+"-2" || up.Issuer != "https://changed-"+ti.Name+".example.test" || !up.Enabled {
			t.Errorf("updated: %+v", up)
		}
		// A PATCH that changes nothing is a 200 and the registration is the same.
		same := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "PATCH", path, map[string]any{}), 200)
		if !reflect.DeepEqual(same, up) {
			t.Errorf("an empty PATCH changed the registration:\n%+v\n%+v", same, up)
		}
		// Each rule applies to an update too.
		for _, c := range registrationValidation() {
			if c.member == "name" && c.value == "" {
				continue
			}
			r := w.do(t, w.ka, "PATCH", path, map[string]any{c.member: c.value})
			if r.Status != 400 {
				t.Errorf("update, %s: status %d, want 400\n%s", c.what, r.Status, r.Body)
				continue
			}
			if p := problem(t, r); !namesMember(p, c.field) {
				t.Errorf("update, %s: %+v does not name %s", c.what, p, c.field)
			}
		}
		if r := w.do(t, w.ka, "PATCH", path, map[string]any{"tenant_id": w.b.ID}); r.Status != 400 {
			t.Errorf("an update of tenant_id: %d", r.Status)
		}
		// A taken pair or name is a conflict, and nothing changed.
		other := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, uniqueIssuer(nil)), 201)
		expect(t, w.do(t, w.ka, "PATCH", path, map[string]any{"issuer": other.Issuer, "audience": other.Audience}), 409, adminapi.ProblemCodeConflict)
		expect(t, w.do(t, w.ka, "PATCH", path, map[string]any{"name": other.Name}), 409, adminapi.ProblemCodeConflict)
		if got := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "GET", path, nil), 200); !reflect.DeepEqual(got, up) {
			t.Errorf("a refused update changed the registration: %+v", got)
		}
		// Another tenant's key, a registration that is not there.
		expect(t, w.do(t, w.kb, "PATCH", path, map[string]any{"description": "taken"}), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "PATCH", base+"/0192f0f0-0000-7000-8000-0000000000ee", map[string]any{"description": "x"}), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("delete returns the registration as it was, and takes its mappings", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, uniqueIssuer(nil)), 201)
		var mappings []string
		for i := 0; i < 3; i++ {
			m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
				map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": fmt.Sprintf("s%d", i), "service_account_id": w.saA.ID}), 201)
			mappings = append(mappings, m.ID)
		}
		gone := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "DELETE", base+"/"+ti.ID, nil), 200)
		if !reflect.DeepEqual(gone, ti) {
			t.Errorf("delete returned\n%+v\nwant\n%+v", gone, ti)
		}
		expect(t, w.do(t, w.ka, "GET", base+"/"+ti.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "DELETE", base+"/"+ti.ID, nil), 404, adminapi.ProblemCodeNotFound)
		for _, id := range mappings {
			expect(t, w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/subject-mappings/"+id, nil), 404, adminapi.ProblemCodeNotFound)
		}
		// The pair is free again, and the service account is untouched.
		if r := w.do(t, w.ka, "POST", base, map[string]any{"name": ti.Name, "issuer": ti.Issuer, "audience": ti.Audience}); r.Status != 201 {
			t.Errorf("the pair of a deleted registration cannot be registered again: %d\n%s", r.Status, r.Body)
		}
		if r := w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/service-accounts/"+w.saA.ID, nil); r.Status != 200 {
			t.Errorf("the service account after the deletion of a registration: %d", r.Status)
		}
		// Another tenant cannot delete it.
		again := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, uniqueIssuer(nil)), 201)
		expect(t, w.do(t, w.kb, "DELETE", base+"/"+again.ID, nil), 404, adminapi.ProblemCodeNotFound)
		if r := w.do(t, w.ka, "GET", base+"/"+again.ID, nil); r.Status != 200 {
			t.Errorf("a refused delete removed the registration: %d", r.Status)
		}
	})

	t.Run("the permissions", func(t *testing.T) {
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", base, uniqueIssuer(nil)), 201)
		// A key with the scope token_issuer:read only: reads pass, writes and deletes are refused.
		reader := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": "reader-2", "roles": []string{"tenant-admin"}}), 201)
		key := decodeInto[adminapi.VirtualKeyCreated](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/virtual-keys", map[string]any{"service_account_id": reader.ID, "scopes": []string{"token_issuer:read"}}), 201)
		if r := w.do(t, key.Secret, "GET", base+"/"+ti.ID, nil); r.Status != 200 {
			t.Errorf("read with the read scope: %d\n%s", r.Status, r.Body)
		}
		if r := w.do(t, key.Secret, "GET", base, nil); r.Status != 200 {
			t.Errorf("list with the read scope: %d", r.Status)
		}
		expect(t, w.do(t, key.Secret, "PATCH", base+"/"+ti.ID, map[string]any{"description": "x"}), 403, adminapi.ProblemCodeForbidden)
		expect(t, w.do(t, key.Secret, "POST", base, uniqueIssuer(nil)), 403, adminapi.ProblemCodeForbidden)
		expect(t, w.do(t, key.Secret, "DELETE", base+"/"+ti.ID, nil), 403, adminapi.ProblemCodeForbidden)
		expect(t, w.do(t, key.Secret, "GET", "/v1/tenants/"+w.a.ID+"/subject-mappings", nil), 403, adminapi.ProblemCodeForbidden)
		// The platform key is not a tenant principal.
		expect(t, w.do(t, w.plat, "GET", base, nil), 403, adminapi.ProblemCodeForbidden)
		// A suspended tenant cannot be administered.
		if _, err := w.api.Client(w.plat).SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: w.b.ID}); err != nil {
			t.Fatal(err)
		}
		expect(t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/token-issuers", nil), 403, adminapi.ProblemCodeTenantSuspended)
	})
}
