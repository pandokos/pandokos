//go:build idp

package idp

import (
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/pandokos/pandokos/test/apitest"
)

// TestKeycloakLimitedRole is T-893: a Keycloak user whose group maps to usage-viewer signs in; getMe
// reports the permissions of the role for the tenant and nothing else; a write is 403 (UC-08).
func TestKeycloakLimitedRole(t *testing.T) {
	p := startPeople(t, nil, "PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS=platform:platform-admin="+groupAdmins+",tenant:tenant-a:usage-viewer="+groupTenant)
	tenantOut := p.cli(t, "tenant", "create", "tenant-a")
	tenantID := regexp.MustCompile(`(?m)^id: ([0-9a-f-]{36})$`).FindStringSubmatch(tenantOut)[1]
	who := person(t, "bob")
	kc := newKeycloakAdmin(t)
	if err := kc.setGroup(who.username, groupTenant, true); err != nil {
		t.Fatalf("put %s in %s: %v", who.username, groupTenant, err)
	}
	t.Cleanup(func() { _ = kc.setGroup(who.username, groupTenant, false) })

	b := p.browser()
	if end := p.signIn(t, b, who); end.Status != 303 {
		t.Fatalf("the sign-in ended with %d %q", end.Status, end.Location)
	}
	m := me(t, b)
	if len(m.Memberships) != 1 || m.Memberships[0].Tenant.ID != tenantID || !reflect.DeepEqual(m.Memberships[0].Roles, []string{"usage-viewer"}) {
		t.Fatalf("memberships %+v, want usage-viewer in tenant-a", m.Memberships)
	}
	want := append([]string(nil), apitest.Golden(t)["usage-viewer"]...)
	want = append(want, "session:self")
	sort.Strings(want)
	if !reflect.DeepEqual(m.Memberships[0].Permissions, want) {
		t.Errorf("the permissions in tenant-a: %v, want exactly those of usage-viewer and the person's own %v", m.Memberships[0].Permissions, want)
	}
	if !reflect.DeepEqual(m.Permissions, []string{"identity:read", "session:self"}) {
		t.Errorf("the permissions outside any tenant: %v", m.Permissions)
	}
	base := "/v1/tenants/" + tenantID
	if r := b.Do(t, "GET", base+"/usage-records", nil, nil); r.Status != 200 {
		t.Errorf("the usage viewer reads usage: %d %s", r.Status, r.Body)
	}
	for name, r := range map[string]*apitest.Response{
		"a service account": b.Do(t, "POST", base+"/service-accounts", map[string]any{"name": "by-viewer", "roles": []string{"inference"}}, nil),
		"a limit rule":      b.Do(t, "POST", base+"/limits", map[string]any{"metric": "requests_per_day", "limit": 10}, nil),
		"a member":          b.Do(t, "POST", base+"/members", map[string]any{"account": "new", "username": "by.viewer", "roles": []string{"usage-viewer"}}, nil),
	} {
		if r.Status != 403 || r.Problem() != "forbidden" {
			t.Errorf("a usage viewer creates %s: %d %s, want 403 forbidden", name, r.Status, r.Body)
		}
	}
}
