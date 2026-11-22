package admin_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/oidc"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestGroupRulesWithRoles is T-882: a rule that names a built-in role of sprint 4.3 and one that names a
// custom role give them at sign-in; an inactive or missing custom role is skipped with a line that
// names no tenant, role or group; a rule that names inference, or an unknown role, stops the start.
func TestGroupRulesWithRoles(t *testing.T) {
	var captured admin.Options
	mappings := []string{
		"tenant:acme:usage-viewer=eng", "tenant:acme:team-lead=eng", "tenant:acme:gone-role=eng", "tenant:acme:sleeping=eng",
		"platform:platform-auditor=admins",
	}
	// The tenant does not exist when the handler is built: its rules are checked at sign-in.
	api, o := apitest.NewPeopleOIDC(t,
		apitest.PeopleConfig{Admin: func(a *admin.Options) { captured = *a }},
		apitest.OIDCConfig{Users: idpPeople, Mappings: mappings})
	p := peopleOn(t, api)
	d := p.d
	owner := api.Owner(t, p.tenant.ID)
	for _, name := range []string{"team-lead", "sleeping"} {
		if r := owner.Do(t, "POST", "/v1/tenants/"+p.tenant.ID+"/roles", map[string]any{"name": name, "permissions": []string{"member:read"}}, nil); r.Status != 201 {
			t.Fatalf("creating the custom role %s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := owner.Do(t, "POST", "/v1/tenants/"+p.tenant.ID+"/roles/sleeping/deactivate", map[string]any{"reason": "kept for later"}, nil); r.Status != 200 {
		t.Fatalf("deactivate: %d %s", r.Status, r.Body)
	}
	ada := d.FederatedUser(t, o.IDP.Issuer(), "ada", nil)
	o.IDP.SignInAs("ada") // groups: admins, eng, other
	if end := last(o.SignIn(t, api.NewBrowser())); end.Location != "/console" {
		t.Fatalf("the sign-in ends with %d %q", end.Status, end.Location)
	}
	var got string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT COALESCE(string_agg(r.role || ':' || r.source, ',' ORDER BY r.role), '') FROM pandokos.membership_role r WHERE r.tenant_id = $1::uuid AND r.user_id = $2::uuid`,
		p.tenant.ID, ada.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "team-lead:idp,usage-viewer:idp" {
		t.Errorf("the roles given in acme: %q, want team-lead:idp,usage-viewer:idp (a built-in role of sprint 4.3 and an active custom role; not the inactive one, not the missing one)", got)
	}
	var platformRoles string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT COALESCE(string_agg(role || ':' || source, ','), '') FROM pandokos.platform_role_assignment WHERE user_id = $1::uuid`, ada.ID).Scan(&platformRoles); err != nil {
		t.Fatal(err)
	}
	if platformRoles != "platform-auditor:idp" {
		t.Errorf("the platform roles given: %q, want platform-auditor:idp", platformRoles)
	}

	// The two rules that were skipped left one line each, with the position of the rule and no name.
	skipped := 0
	for _, line := range strings.Split(strings.TrimSpace(api.Logs.String()), "\n") {
		if !strings.Contains(line, "group rule skipped") {
			continue
		}
		skipped++
		for _, name := range []string{"acme", "gone-role", "sleeping", "team-lead", "usage-viewer", "eng", "admins"} {
			if strings.Contains(line, name) {
				t.Errorf("the line of a skipped rule names %q: %s", name, line)
			}
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		position := false
		for k, v := range rec {
			if n, ok := v.(float64); ok && (k == "rule" || k == "index" || k == "position") && n >= 0 && n <= float64(len(mappings)) {
				position = true
			}
		}
		if !position {
			t.Errorf("the line of a skipped rule does not carry the position of the rule (rule, index or position): %s", line)
		}
	}
	if skipped != 2 {
		t.Errorf("%d lines say group rule skipped, want 2 (the inactive role and the missing one)", skipped)
	}

	// A rule that stops the start: inference, an unknown role, a role of the other scope, a custom
	// role that the tenant does not have, a malformed name.
	for what, rule := range map[string]string{
		"inference":                              "tenant:acme:inference=eng",
		"an unknown built-in-looking role":       "tenant:acme:Not_A_Role=eng",
		"a platform role in a tenant":            "tenant:acme:platform-admin=eng",
		"a tenant role for the platform":         "platform:usage-viewer=eng",
		"owner for the platform":                 "platform:owner=eng",
		"a custom role the tenant does not have": "tenant:acme:no-such-role=eng",
	} {
		rules, err := oidc.ParseRules([]string{rule})
		if err != nil {
			continue // refused as syntax, which also stops the start
		}
		bad := captured
		bad.Auth.GroupRules = rules
		if _, err := admin.New(bad); err == nil {
			t.Errorf("admin.New accepted the rule that names %s (%s)", what, rule)
		}
	}
	// The rules of the test are accepted now that the tenant and its custom roles exist.
	good := captured
	good.Auth.GroupRules = o.Rules
	if _, err := admin.New(good); err != nil {
		t.Errorf("admin.New with the rules of the test: %v", err)
	}
}
