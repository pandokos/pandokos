package admin_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// TestSubjectMappings is T-589 (the mappings): creation for both kinds of registration, each rule
// with its field, what a mapping may name, the conflicts, pagination, filters, delete.
func TestSubjectMappings(t *testing.T) {
	w := newIW(t)
	base := "/v1/tenants/" + w.a.ID + "/subject-mappings"
	ti := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
	pi := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
	decodeInto[adminapi.PlatformIssuerTenant](t, w.do(t, w.plat, "PUT", "/v1/platform-issuers/"+pi.ID+"/tenants/"+w.a.ID, map[string]any{}), 200)
	mapping := func(scope, issuer, subject, sa string, extra map[string]any) map[string]any {
		b := map[string]any{"issuer": map[string]any{"scope": scope, "id": issuer}, "subject": subject, "service_account_id": sa}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	tenantMapping := func(subject string) map[string]any { return mapping("tenant", ti.ID, subject, w.saA.ID, nil) }

	t.Run("creation for a tenant-bound registration", func(t *testing.T) {
		m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", base, mapping("tenant", ti.ID, "spiffe://acme/ns/ci", w.saA.ID, map[string]any{"description": "ci"})), 201)
		if m.TenantID != w.a.ID || m.Issuer.Scope != adminapi.IssuerScopeTenant || m.Issuer.ID != ti.ID || m.Issuer.Name != ti.Name || m.Subject != "spiffe://acme/ns/ci" ||
			m.ServiceAccountID != w.saA.ID || m.ServiceAccountName != w.saA.Name || m.Description != "ci" || len(m.ID) != 36 || m.CreatedAt.IsZero() {
			t.Errorf("created: %+v", m)
		}
		if got := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "GET", base+"/"+m.ID, nil), 200); !reflect.DeepEqual(got, m) {
			t.Errorf("get after create:\n%+v\n%+v", got, m)
		}
		// The name of the registration in a request is ignored, even when it is wrong.
		named := mapping("tenant", ti.ID, "named", w.saA.ID, nil)
		named["issuer"].(map[string]any)["name"] = "some-other-name"
		if m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", base, named), 201); m.Issuer.Name != ti.Name {
			t.Errorf("the name of the issuer in the response: %q", m.Issuer.Name)
		}
		// A subject is any string of 1 to 255 characters: spaces, unicode, URLs, case.
		for _, s := range []string{"a", strings.Repeat("s", 255), "système de façade / équipe 1", "arn:aws:iam::123456789012:role/ci", "CN=ci,O=Acme", "Same", "same"} {
			if r := w.do(t, w.ka, "POST", base, tenantMapping(s)); r.Status != 201 {
				t.Errorf("subject %q: %d\n%s", clip(s), r.Status, r.Body)
			}
		}
	})

	t.Run("creation for a platform-level registration that lists the tenant", func(t *testing.T) {
		m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", base, mapping("platform", pi.ID, "workload", w.saA.ID, nil)), 201)
		if m.Issuer.Scope != adminapi.IssuerScopePlatform || m.Issuer.ID != pi.ID || m.Issuer.Name != pi.Name || m.Subject != "workload" {
			t.Errorf("created: %+v", m)
		}
		// The same subject of a tenant-bound and a platform-level registration are two mappings.
		if r := w.do(t, w.ka, "POST", base, mapping("tenant", ti.ID, "workload", w.saA.ID, nil)); r.Status != 201 {
			t.Errorf("the same subject under another registration: %d\n%s", r.Status, r.Body)
		}
	})

	t.Run("each rule, with its field", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.a.ID)
		type row struct {
			what  string
			body  any
			field string
		}
		rows := []row{
			{"an empty subject", tenantMapping(""), "subject"},
			{"a subject of 256 characters", tenantMapping(strings.Repeat("s", 256)), "subject"},
			{"a subject with a control character", tenantMapping("a\nb"), "subject"},
			{"a subject with NUL", tenantMapping("a\x00b"), "subject"},
			{"a description with NUL", mapping("tenant", ti.ID, "d", w.saA.ID, map[string]any{"description": "a\x00b"}), "description"},
			{"a description with a control character", mapping("tenant", ti.ID, "d", w.saA.ID, map[string]any{"description": "a\x1fb"}), "description"},
			{"a description of 201 characters", mapping("tenant", ti.ID, "d", w.saA.ID, map[string]any{"description": strings.Repeat("d", 201)}), "description"},
			{"no subject", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "service_account_id": w.saA.ID}, "subject"},
			{"no issuer", map[string]any{"subject": "x", "service_account_id": w.saA.ID}, "issuer"},
			{"no service account", map[string]any{"issuer": map[string]any{"scope": "tenant", "id": ti.ID}, "subject": "x"}, "service_account_id"},
			{"another scope", mapping("global", ti.ID, "x", w.saA.ID, nil), "issuer"},
			{"an issuer without a scope", map[string]any{"issuer": map[string]any{"id": ti.ID}, "subject": "x", "service_account_id": w.saA.ID}, "issuer"},
			{"an issuer ID that is not an ID", mapping("tenant", "not-an-id", "x", w.saA.ID, nil), "issuer"},
			{"a service account ID that is not an ID", mapping("tenant", ti.ID, "x", "not-an-id", nil), "service_account_id"},
			{"an unknown member", tenantMapping("x"), ""},
		}
		rows[len(rows)-1].body = func() map[string]any { b := tenantMapping("x"); b["tenant_id"] = w.b.ID; return b }()
		for _, c := range rows {
			r := w.do(t, w.ka, "POST", base, c.body)
			if r.Status != 400 {
				t.Errorf("%s: status %d, want 400\n%s", c.what, r.Status, r.Body)
				continue
			}
			if p := problem(t, r); p.Code != adminapi.ProblemCodeInvalidRequest || (c.field != "" && !namesMember(p, c.field)) {
				t.Errorf("%s: %+v does not name %q", c.what, p, c.field)
			}
		}
		if w.api.SnapshotTenant(t, w.a.ID) != before {
			t.Error("a refused creation changed a row")
		}
	})

	t.Run("a registration or a service account that the tenant cannot name", func(t *testing.T) {
		otherIssuer := decodeInto[adminapi.TokenIssuer](t, w.do(t, w.kb, "POST", "/v1/tenants/"+w.b.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		unlisted := decodeInto[adminapi.PlatformIssuer](t, w.do(t, w.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		missingID := "0192f0f0-0000-7000-8000-0000000000ee"
		for what, c := range map[string]struct{ real, missing map[string]any }{
			"a registration of another tenant":                   {mapping("tenant", otherIssuer.ID, "x", w.saA.ID, nil), mapping("tenant", missingID, "x", w.saA.ID, nil)},
			"a platform-level registration that lists no tenant": {mapping("platform", unlisted.ID, "x", w.saA.ID, nil), mapping("platform", missingID, "x", w.saA.ID, nil)},
			"a registration under the wrong scope":               {mapping("platform", ti.ID, "x", w.saA.ID, nil), mapping("platform", missingID, "x", w.saA.ID, nil)},
			"a service account of another tenant":                {mapping("tenant", ti.ID, "x", w.saB.ID, nil), mapping("tenant", ti.ID, "x", missingID, nil)},
		} {
			r1, r2 := w.do(t, w.ka, "POST", base, c.real), w.do(t, w.ka, "POST", base, c.missing)
			if (r1.Status != 404 && r1.Status != 400) || r1.Status != r2.Status {
				t.Errorf("%s: status %d, a missing one %d, want the same 404 or 400\n%s", what, r1.Status, r2.Status, r1.Body)
				continue
			}
			sameProblem(t, what, r1, r2)
			if r1.Status == 400 {
				if p := problem(t, r1); !namesMember(p, "issuer") && !namesMember(p, "service_account_id") {
					t.Errorf("%s: %+v names neither member", what, p)
				}
			}
			for _, secret := range []string{otherIssuer.ID, w.saB.ID, unlisted.ID, w.b.ID} {
				if strings.Contains(string(r1.Body), secret) {
					t.Errorf("%s: the answer holds %s", what, secret)
				}
			}
		}
	})

	t.Run("a subject is mapped once per registration", func(t *testing.T) {
		decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", base, tenantMapping("once")), 201)
		r1 := w.do(t, w.ka, "POST", base, tenantMapping("once"))
		expect(t, r1, 409, adminapi.ProblemCodeConflict)
		other := decodeInto[adminapi.ServiceAccount](t, w.do(t, w.ka, "POST", "/v1/tenants/"+w.a.ID+"/service-accounts", map[string]any{"name": "other-account", "roles": []string{"inference"}}), 201)
		r2 := w.do(t, w.ka, "POST", base, mapping("tenant", ti.ID, "once", other.ID, nil))
		expect(t, r2, 409, adminapi.ProblemCodeConflict)
		sameProblem(t, "the same subject for another service account", r1, r2)
		if strings.Contains(string(r1.Body), "once") || strings.Contains(string(r1.Body), "subject_mapping") {
			t.Errorf("the conflict repeats the subject or the constraint: %s", r1.Body)
		}
		decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", base, mapping("platform", pi.ID, "once", w.saA.ID, nil)), 201)
		expect(t, w.do(t, w.ka, "POST", base, mapping("platform", pi.ID, "once", w.saA.ID, nil)), 409, adminapi.ProblemCodeConflict)
	})

	t.Run("pagination, sorting and filters", func(t *testing.T) {
		pw := newIW(t)
		pbase := "/v1/tenants/" + pw.a.ID + "/subject-mappings"
		iss := decodeInto[adminapi.TokenIssuer](t, pw.do(t, pw.ka, "POST", "/v1/tenants/"+pw.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		iss2 := decodeInto[adminapi.TokenIssuer](t, pw.do(t, pw.ka, "POST", "/v1/tenants/"+pw.a.ID+"/token-issuers", uniqueIssuer(nil)), 201)
		sa2 := decodeInto[adminapi.ServiceAccount](t, pw.do(t, pw.ka, "POST", "/v1/tenants/"+pw.a.ID+"/service-accounts", map[string]any{"name": "second", "roles": []string{"inference"}}), 201)
		for i := 0; i < 5; i++ {
			issuer, sa := iss.ID, pw.saA.ID
			if i%2 == 1 {
				issuer, sa = iss2.ID, sa2.ID
			}
			decodeInto[adminapi.SubjectMapping](t, pw.do(t, pw.ka, "POST", pbase, mapping("tenant", issuer, fmt.Sprintf("s%d", i), sa, nil)), 201)
		}
		list := func(q string) adminapi.SubjectMappingList {
			return decodeInto[adminapi.SubjectMappingList](t, pw.do(t, pw.ka, "GET", pbase+q, nil), 200)
		}
		subjects := func(l adminapi.SubjectMappingList) string {
			var out []string
			for _, m := range l.Items {
				out = append(out, m.Subject)
			}
			return strings.Join(out, ",")
		}
		if got := subjects(list("")); got != "s0,s1,s2,s3,s4" {
			t.Errorf("default: %s", got)
		}
		if got := subjects(list("?sort=-created_at")); got != "s4,s3,s2,s1,s0" {
			t.Errorf("-created_at: %s", got)
		}
		for _, sort := range []string{"created_at", "-created_at"} {
			var all []string
			cursor := ""
			for pages := 0; ; pages++ {
				l := list("?sort=" + sort + "&limit=2" + cursor)
				if len(l.Items) > 2 || pages > 4 {
					t.Fatalf("a page of %d items after %d pages", len(l.Items), pages)
				}
				all = append(all, subjects(l))
				if l.NextCursor == "" {
					break
				}
				cursor = "&cursor=" + l.NextCursor
			}
			if got, want := strings.Join(all, ","), subjects(list("?sort="+sort)); got != want {
				t.Errorf("sort %s: pages %s, whole %s", sort, got, want)
			}
		}
		if got := subjects(list("?issuer=" + iss2.ID)); got != "s1,s3" {
			t.Errorf("filter by issuer: %s", got)
		}
		if got := subjects(list("?service_account=" + sa2.ID)); got != "s1,s3" {
			t.Errorf("filter by service account: %s", got)
		}
		if got := subjects(list("?subject=s2")); got != "s2" {
			t.Errorf("filter by subject: %s", got)
		}
		if got := subjects(list("?subject=S2")); got != "" {
			t.Errorf("a subject filter is exact: %s", got)
		}
		if got := subjects(list("?issuer=" + iss.ID + "&service_account=" + sa2.ID)); got != "" {
			t.Errorf("filters combine with and: %s", got)
		}
		for _, q := range []string{"?issuer=not-an-id", "?service_account=x", "?sort=subject", "?limit=0", "?cursor=garbage"} {
			if r := pw.do(t, pw.ka, "GET", pbase+q, nil); r.Status != 400 {
				t.Errorf("%s: %d\n%s", q, r.Status, r.Body)
			}
		}
		if l := decodeInto[adminapi.SubjectMappingList](t, pw.do(t, pw.kb, "GET", "/v1/tenants/"+pw.b.ID+"/subject-mappings", nil), 200); len(l.Items) != 0 {
			t.Errorf("tenant B lists %d mappings of A", len(l.Items))
		}
	})

	t.Run("delete returns the mapping as it was", func(t *testing.T) {
		m := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "POST", base, tenantMapping("to-delete")), 201)
		expect(t, w.do(t, w.kb, "DELETE", base+"/"+m.ID, nil), 404, adminapi.ProblemCodeNotFound)
		gone := decodeInto[adminapi.SubjectMapping](t, w.do(t, w.ka, "DELETE", base+"/"+m.ID, nil), 200)
		if !reflect.DeepEqual(gone, m) {
			t.Errorf("delete returned\n%+v\nwant\n%+v", gone, m)
		}
		expect(t, w.do(t, w.ka, "GET", base+"/"+m.ID, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, w.ka, "DELETE", base+"/"+m.ID, nil), 404, adminapi.ProblemCodeNotFound)
		// The subject can be mapped again, to another service account.
		if r := w.do(t, w.ka, "POST", base, mapping("tenant", ti.ID, "to-delete", w.saA.ID, nil)); r.Status != 201 {
			t.Errorf("mapping a deleted subject again: %d\n%s", r.Status, r.Body)
		}
		// A mapping is immutable: there is no PATCH.
		if r := w.do(t, w.ka, "PATCH", base+"/"+m.ID, map[string]any{"subject": "x"}); r.Status != 404 && r.Status != 405 {
			t.Errorf("PATCH on a mapping: %d", r.Status)
		}
	})

	t.Run("a tenant that leaves a platform-level list keeps its mapping, which is inert", func(t *testing.T) {
		pw := newIW(t)
		p := decodeInto[adminapi.PlatformIssuer](t, pw.do(t, pw.plat, "POST", "/v1/platform-issuers", uniqueIssuer(map[string]any{"tenant_claim": "org"})), 201)
		decodeInto[adminapi.PlatformIssuerTenant](t, pw.do(t, pw.plat, "PUT", "/v1/platform-issuers/"+p.ID+"/tenants/"+pw.a.ID, nil), 200)
		m := decodeInto[adminapi.SubjectMapping](t, pw.do(t, pw.ka, "POST", "/v1/tenants/"+pw.a.ID+"/subject-mappings", mapping("platform", p.ID, "w", pw.saA.ID, nil)), 201)
		decodeInto[adminapi.PlatformIssuerTenant](t, pw.do(t, pw.plat, "DELETE", "/v1/platform-issuers/"+p.ID+"/tenants/"+pw.a.ID, nil), 200)
		if got := decodeInto[adminapi.SubjectMapping](t, pw.do(t, pw.ka, "GET", "/v1/tenants/"+pw.a.ID+"/subject-mappings/"+m.ID, nil), 200); got.ID != m.ID {
			t.Errorf("the mapping after the tenant left the list: %+v", got)
		}
		if l := decodeInto[adminapi.SubjectMappingList](t, pw.do(t, pw.ka, "GET", "/v1/tenants/"+pw.a.ID+"/subject-mappings", nil), 200); len(l.Items) != 1 {
			t.Errorf("%d mappings listed", len(l.Items))
		}
		// New mappings to it are refused now, as for any registration that does not list the tenant.
		if r := pw.do(t, pw.ka, "POST", "/v1/tenants/"+pw.a.ID+"/subject-mappings", mapping("platform", p.ID, "w2", pw.saA.ID, nil)); r.Status != 404 && r.Status != 400 {
			t.Errorf("a new mapping to a registration that no longer lists the tenant: %d", r.Status)
		}
		// Deleting the registration deletes the mapping too.
		decodeInto[adminapi.PlatformIssuer](t, pw.do(t, pw.plat, "DELETE", "/v1/platform-issuers/"+p.ID, nil), 200)
		expect(t, pw.do(t, pw.ka, "GET", "/v1/tenants/"+pw.a.ID+"/subject-mappings/"+m.ID, nil), 404, adminapi.ProblemCodeNotFound)
	})
}
