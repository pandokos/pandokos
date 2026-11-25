package admin_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestPlatformIssuers is T-589 (the platform-level registrations and their tenant lists): the same
// rules as for a tenant-bound registration plus the tenant claim, the list with default and explicit
// claim values, the conflicts, delete with its cascades, and the tenant's view of what lists it.
func TestPlatformIssuers(t *testing.T) {
	t.Parallel()
	w := newIW(t)
	const base = "/v1/platform-issuers"
	create := func(extra map[string]any) adminapi.PlatformIssuer {
		t.Helper()
		body := uniqueIssuer(map[string]any{"tenant_claim": "org"})
		for k, v := range extra {
			body[k] = v
		}
		return decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", base, body), 201)
	}

	t.Run("creation with the defaults and with every member", func(t *testing.T) {
		body := uniqueIssuer(map[string]any{"tenant_claim": "org"})
		p := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", base, body), 201)
		if p.Name != body["name"] || p.Issuer != body["issuer"] || p.Audience != body["audience"] || p.TenantClaim != "org" || p.Description != "" || p.JwksUri != "" ||
			!reflect.DeepEqual(p.Algorithms, defaultAlgorithms) || p.SubjectClaim != "sub" || p.RequiredClaims == nil || len(p.RequiredClaims) != 0 || !p.Enabled || p.TenantCount != 0 ||
			len(p.ID) != 36 || p.CreatedAt.IsZero() {
			t.Errorf("created: %+v", p)
		}
		if got := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "GET", base+"/"+p.ID, nil), 200); !reflect.DeepEqual(got, p) {
			t.Errorf("get after create:\n%+v\n%+v", got, p)
		}
		full := create(map[string]any{"description": "shared", "jwks_uri": "https://keys.example.test/jwks", "algorithms": []string{"PS256"}, "subject_claim": "azp",
			"tenant_claim": "https://claims.example.test/org", "required_claims": map[string]string{"tier": "gold"}, "enabled": false})
		if full.TenantClaim != "https://claims.example.test/org" || full.Enabled || full.SubjectClaim != "azp" || !reflect.DeepEqual(full.Algorithms, []string{"PS256"}) || full.RequiredClaims["tier"] != "gold" {
			t.Errorf("created: %+v", full)
		}
	})

	t.Run("each rule, with its field", func(t *testing.T) {
		for _, c := range registrationValidation() {
			r := w.do(t, w.plat, "POST", base, uniqueIssuer(map[string]any{"tenant_claim": "org", c.member: c.value}))
			if r.Status != 400 {
				t.Errorf("%s: status %d, want 400 on %s\n%s", c.what, r.Status, c.member, r.Body)
				continue
			}
			if p := problem(t, r); !namesMember(p, c.field) {
				t.Errorf("%s: %+v does not name %s", c.what, p, c.field)
			}
		}
		long := strings.Repeat("c", 65)
		for what, v := range map[string]any{"missing": nil, "empty": "", "a space": "a b", "65 characters": long, "iss": "iss", "aud": "aud", "exp": "exp", "nbf": "nbf", "iat": "iat", "a number": 7} {
			body := uniqueIssuer(map[string]any{"tenant_claim": v})
			if v == nil {
				delete(body, "tenant_claim")
			}
			r := w.do(t, w.plat, "POST", base, body)
			if r.Status != 400 {
				t.Errorf("tenant_claim %s: status %d\n%s", what, r.Status, r.Body)
			} else if p := problem(t, r); !namesMember(p, "tenant_claim") {
				t.Errorf("tenant_claim %s: %+v", what, p)
			}
		}
	})

	t.Run("a name or a pair that is taken is a 409 with the same body, across both kinds", func(t *testing.T) {
		first := create(nil)
		byName := w.do(t, w.plat, "POST", base, uniqueIssuer(map[string]any{"tenant_claim": "org", "name": first.Name}))
		byPair := w.do(t, w.plat, "POST", base, uniqueIssuer(map[string]any{"tenant_claim": "org", "issuer": first.Issuer, "audience": first.Audience}))
		expect(t, byName, 409, adminapi.ProblemCodeConflict)
		expect(t, byPair, 409, adminapi.ProblemCodeConflict)
		sameProblem(t, "a taken pair and a taken name", byName, byPair)
		// A tenant-bound registration holds a pair: the platform cannot take it, nor the tenant a platform pair.
		ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		cross := w.do(t, w.plat, "POST", base, uniqueIssuer(map[string]any{"tenant_claim": "org", "issuer": ti.Issuer, "audience": ti.Audience}))
		expect(t, cross, 409, adminapi.ProblemCodeConflict)
		sameProblem(t, "a pair held by a tenant", byName, cross)
		if strings.Contains(string(cross.Body), w.a.ID) || strings.Contains(string(cross.Body), ti.ID) {
			t.Errorf("the conflict tells which tenant holds the pair: %s", cross.Body)
		}
		back := w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/token-issuers", uniqueIssuer(map[string]any{"issuer": first.Issuer, "audience": first.Audience}))
		expect(t, back, 409, adminapi.ProblemCodeConflict)
		sameProblem(t, "a pair held by the platform", byName, back)
		// The platform name space and the tenant's are separate.
		if r := w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(map[string]any{"name": first.Name})); r.Status != 201 {
			t.Errorf("a tenant-bound registration with the name of a platform-level one: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("pagination, sorting and filters", func(t *testing.T) {
		pw := newIW(t)
		for _, n := range []string{"delta", "alpha", "echo", "bravo", "charlie"} {
			decodeInto[adminapi.PlatformIssuer](t, pw.do(t, pw.plat, "POST", base, uniqueIssuer(map[string]any{"tenant_claim": "org", "name": "pg-" + n + "-" + pw.a.Name, "enabled": n != "echo"})), 201)
		}
		suffix := "-" + pw.a.Name
		list := func(q string) []string {
			var out []string
			cursor := ""
			for pages := 0; pages < 20; pages++ {
				sep := "?"
				if strings.Contains(q, "?") {
					sep = "&"
				}
				l := decodeInto[adminapi.PlatformIssuerList](t, pw.do(t, pw.plat, "GET", base+q+sep+"limit=2"+cursor, nil), 200)
				if len(l.Items) > 2 {
					t.Fatalf("a page of %d items", len(l.Items))
				}
				for _, i := range l.Items {
					if strings.HasSuffix(i.Name, suffix) {
						out = append(out, strings.TrimSuffix(strings.TrimPrefix(i.Name, "pg-"), suffix))
					}
				}
				if l.NextCursor == "" {
					return out
				}
				cursor = "&cursor=" + l.NextCursor
			}
			t.Fatal("too many pages")
			return nil
		}
		join := func(s []string) string { return strings.Join(s, ",") }
		if got := join(list("")); got != "delta,alpha,echo,bravo,charlie" {
			t.Errorf("default: %s", got)
		}
		if got := join(list("?sort=-created_at")); got != "charlie,bravo,echo,alpha,delta" {
			t.Errorf("-created_at: %s", got)
		}
		if got := join(list("?sort=name")); got != "alpha,bravo,charlie,delta,echo" {
			t.Errorf("name: %s", got)
		}
		if got := join(list("?sort=-name")); got != "echo,delta,charlie,bravo,alpha" {
			t.Errorf("-name: %s", got)
		}
		if got := join(list("?name=pg-bravo" + suffix)); got != "bravo" {
			t.Errorf("name filter: %s", got)
		}
		if got := join(list("?enabled=false")); !strings.Contains(got, "echo") || strings.Contains(got, "alpha") {
			t.Errorf("enabled=false: %s", got)
		}
		if got := join(list("?enabled=true&sort=name")); strings.Contains(got, "echo") || !strings.Contains(got, "alpha") {
			t.Errorf("enabled=true: %s", got)
		}
		for _, q := range []string{"?enabled=yes", "?sort=issuer", "?limit=0", "?cursor=garbage"} {
			if r := pw.do(t, pw.plat, "GET", base+q, nil); r.Status != 400 {
				t.Errorf("%s: %d", q, r.Status)
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		p := create(map[string]any{"description": "first", "required_claims": map[string]string{"a": "b"}})
		path := base + "/" + p.ID
		up := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "PATCH", path, map[string]any{"tenant_claim": "org_id", "enabled": false, "audience": "changed"}), 200)
		want := p
		want.TenantClaim, want.Enabled, want.Audience, want.UpdatedAt = "org_id", false, "changed", up.UpdatedAt
		if !reflect.DeepEqual(up, want) {
			t.Errorf("updated:\n%+v\nwant\n%+v", up, want)
		}
		up = decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "PATCH", path, map[string]any{"required_claims": map[string]string{}, "algorithms": []string{"EdDSA"}, "description": "", "enabled": true}), 200)
		if len(up.RequiredClaims) != 0 || !reflect.DeepEqual(up.Algorithms, []string{"EdDSA"}) || up.Description != "" || !up.Enabled {
			t.Errorf("updated: %+v", up)
		}
		for _, c := range registrationValidation() {
			if c.member == "name" && c.value == "" {
				continue
			}
			r := w.do(t, w.plat, "PATCH", path, map[string]any{c.member: c.value})
			if r.Status != 400 {
				t.Errorf("update, %s: status %d\n%s", c.what, r.Status, r.Body)
			}
		}
		if r := w.do(t, w.plat, "PATCH", path, map[string]any{"tenant_claim": "a b"}); r.Status != 400 {
			t.Errorf("update, a bad tenant_claim: %d", r.Status)
		}
		other := create(nil)
		expect(t, w.do(t, w.plat, "PATCH", path, map[string]any{"issuer": other.Issuer, "audience": other.Audience}), 409, adminapi.ProblemCodeConflict)
		expect(t, w.do(t, w.plat, "PATCH", path, map[string]any{"name": other.Name}), 409, adminapi.ProblemCodeConflict)
		expect(t, w.do(t, w.plat, "PATCH", base+"/0192f0f0-0000-7000-8000-0000000000ee", map[string]any{"description": "x"}), 404, adminapi.ProblemCodeNotFound)
	})

	t.Run("the tenant list", func(t *testing.T) {
		p := create(nil)
		path := base + "/" + p.ID + "/tenants"
		// A tenant is added with its ID as the claim value by default, and with an explicit value.
		a := decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", path+"/"+w.a.ID, nil), 200)
		if a.IssuerID != p.ID || a.TenantID != w.a.ID || a.TenantName != w.a.Name || a.ClaimValue != w.a.ID || a.CreatedAt.IsZero() {
			t.Errorf("added with the default value: %+v", a)
		}
		b := decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", path+"/"+w.b.ID, map[string]any{"claim_value": "org-b"}), 200)
		if b.ClaimValue != "org-b" || b.TenantName != w.b.Name {
			t.Errorf("added with a value: %+v", b)
		}
		if got := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "GET", base+"/"+p.ID, nil), 200); got.TenantCount != 2 {
			t.Errorf("tenant_count %d, want 2", got.TenantCount)
		}
		// PUT is idempotent: again with the same value, and with another value, which replaces it.
		again := decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", path+"/"+w.b.ID, map[string]any{"claim_value": "org-b"}), 200)
		if !reflect.DeepEqual(again, b) {
			t.Errorf("the same PUT twice:\n%+v\n%+v", again, b)
		}
		replaced := decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", path+"/"+w.b.ID, map[string]any{"claim_value": "org-b-2"}), 200)
		if replaced.ClaimValue != "org-b-2" || !replaced.CreatedAt.Equal(b.CreatedAt) {
			t.Errorf("a new value: %+v", replaced)
		}
		if got := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "GET", base+"/"+p.ID, nil), 200); got.TenantCount != 2 {
			t.Errorf("tenant_count %d after a replacement, want 2", got.TenantCount)
		}
		// The claim value is unique in the registration; the same value in another registration is fine.
		third, _ := w.api.Tenant(t, "issuers-c")
		expect(t, w.do(t, w.plat, "PUT", path+"/"+third.ID, map[string]any{"claim_value": "org-b-2"}), 409, adminapi.ProblemCodeConflict)
		expect(t, w.do(t, w.plat, "PUT", path+"/"+third.ID, map[string]any{"claim_value": w.a.ID}), 409, adminapi.ProblemCodeConflict)
		if r := w.do(t, w.plat, "PUT", base+"/"+create(nil).ID+"/tenants/"+third.ID, map[string]any{"claim_value": "org-b-2"}); r.Status != 200 {
			t.Errorf("the same claim value in another registration: %d\n%s", r.Status, r.Body)
		}
		// What is refused.
		for what, c := range map[string]struct {
			path string
			body any
		}{
			"an empty claim value":                   {path + "/" + third.ID, map[string]any{"claim_value": ""}},
			"a claim value of 256 characters":        {path + "/" + third.ID, map[string]any{"claim_value": strings.Repeat("v", 256)}},
			"a claim value with a control character": {path + "/" + third.ID, map[string]any{"claim_value": "a\nb"}},
			"an unknown member":                      {path + "/" + third.ID, map[string]any{"tenant": "x"}},
		} {
			if r := w.do(t, w.plat, "PUT", c.path, c.body); r.Status != 400 {
				t.Errorf("%s: status %d\n%s", what, r.Status, r.Body)
			} else if p := problem(t, r); what != "an unknown member" && !namesMember(p, "claim_value") {
				t.Errorf("%s: %+v", what, p)
			}
		}
		expect(t, w.do(t, w.plat, "PUT", path+"/0192f0f0-0000-7000-8000-0000000000ee", nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.plat, "PUT", base+"/0192f0f0-0000-7000-8000-0000000000ee/tenants/"+w.a.ID, nil), 404, adminapi.ProblemCodeNotFound)
		// The list, with the tenant names, paged and sorted.
		var seen []string
		cursor := ""
		for pages := 0; pages < 5; pages++ {
			l := decodeInto[adminapi.PlatformIssuerTenantList](t, w.do(t, w.plat, "GET", path+"?sort=created_at&limit=1"+cursor, nil), 200)
			for _, e := range l.Items {
				seen = append(seen, e.TenantName+"="+e.ClaimValue)
			}
			if l.NextCursor == "" {
				break
			}
			cursor = "&cursor=" + l.NextCursor
		}
		if strings.Join(seen, ",") != w.a.Name+"="+w.a.ID+","+w.b.Name+"=org-b-2" {
			t.Errorf("the tenant list: %v", seen)
		}
		l := decodeInto[adminapi.PlatformIssuerTenantList](t, w.do(t, w.plat, "GET", path+"?sort=-created_at", nil), 200)
		if len(l.Items) != 2 || l.Items[0].TenantID != w.b.ID {
			t.Errorf("-created_at: %+v", l.Items)
		}
		// A tenant sees the registration that lists it, with its own claim value.
		view := decodeInto[adminapi.TenantPlatformIssuerList](t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/platform-issuers", nil), 200)
		found := false
		for _, v := range view.Items {
			if v.ID == p.ID {
				found = v.ClaimValue == "org-b-2" && v.TenantClaim == "org" && v.Issuer == p.Issuer && v.Audience == p.Audience && v.Name == p.Name
			}
		}
		if !found {
			t.Errorf("tenant B's view: %+v", view.Items)
		}
		// Removing a tenant returns the entry as it was; a second removal is a 404.
		gone := decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "DELETE", path+"/"+w.b.ID, nil), 200)
		if gone.ClaimValue != "org-b-2" || gone.TenantID != w.b.ID {
			t.Errorf("removed: %+v", gone)
		}
		expect(t, w.do(t, w.plat, "DELETE", path+"/"+w.b.ID, nil), 404, adminapi.ProblemCodeNotFound)
		view = decodeInto[adminapi.TenantPlatformIssuerList](t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/platform-issuers", nil), 200)
		for _, v := range view.Items {
			if v.ID == p.ID {
				t.Error("tenant B still sees a registration that no longer lists it")
			}
		}
		// The claim value of a removed tenant is free again.
		if r := w.do(t, w.plat, "PUT", path+"/"+third.ID, map[string]any{"claim_value": "org-b-2"}); r.Status != 200 {
			t.Errorf("a claim value that was released: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("delete takes the tenant list and the mappings", func(t *testing.T) {
		p := create(nil)
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", base+"/"+p.ID+"/tenants/"+w.a.ID, nil), 200)
		decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", base+"/"+p.ID+"/tenants/"+w.b.ID, nil), 200)
		ma := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "platform", "id": p.ID}, "subject": "w", "service_account_id": w.saA.ID}), 201)
		mb := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/subject-mappings",
			map[string]any{"issuer": map[string]any{"scope": "platform", "id": p.ID}, "subject": "w", "service_account_id": w.saB.ID}), 201)
		gone := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "DELETE", base+"/"+p.ID, nil), 200)
		if gone.ID != p.ID || gone.Name != p.Name || gone.Issuer != p.Issuer {
			t.Errorf("delete returned %+v", gone)
		}
		expect(t, w.do(t, w.plat, "GET", base+"/"+p.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.plat, "DELETE", base+"/"+p.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "GET", "/v1/tenants/"+w.a.ID+"/subject-mappings/"+ma.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.kb, "GET", "/v1/tenants/"+w.b.ID+"/subject-mappings/"+mb.ID, nil), 404, adminapi.ProblemCodeNotFound)
		// The pair is free again.
		if r := w.do(t, w.plat, "POST", base, map[string]any{"name": p.Name, "issuer": p.Issuer, "audience": p.Audience, "tenant_claim": "org"}); r.Status != 201 {
			t.Errorf("the pair of a deleted registration: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("the permissions", func(t *testing.T) {
		p := create(nil)
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", base, nil}, {"POST", base, uniqueIssuer(map[string]any{"tenant_claim": "org"})}, {"GET", base + "/" + p.ID, nil}, {"PATCH", base + "/" + p.ID, map[string]any{"description": "x"}},
			{"DELETE", base + "/" + p.ID, nil}, {"GET", base + "/" + p.ID + "/tenants", nil}, {"PUT", base + "/" + p.ID + "/tenants/" + w.a.ID, nil}, {"DELETE", base + "/" + p.ID + "/tenants/" + w.a.ID, nil},
		} {
			expect(t, w.do(t, w.ka, c.method, c.path, c.body), 403, adminapi.ProblemCodeForbidden)
			if r := w.do(t, "", c.method, c.path, c.body); r.Status != 401 {
				t.Errorf("%s %s without a credential: %d", c.method, c.path, r.Status)
			}
		}
	})
}
