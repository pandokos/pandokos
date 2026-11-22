package admin_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

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

	// A rule that stops the start: inference, an unknown role, a role of the other scope, a malformed
	// name.
	for what, rule := range map[string]string{
		"inference":                        "tenant:acme:inference=eng",
		"an unknown built-in-looking role": "tenant:acme:Not_A_Role=eng",
		"a platform role in a tenant":      "tenant:acme:platform-admin=eng",
		"a tenant role for the platform":   "platform:usage-viewer=eng",
		"owner for the platform":           "platform:owner=eng",
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
	// The rules of the test are accepted now that the tenant and its custom roles exist. The rule of
	// the missing custom role does not stop the start either: a tenant could otherwise stop the
	// installation by deleting a role. It is skipped with a warning that names its position only.
	var start strings.Builder
	good := captured
	good.Logger = slog.New(slog.NewJSONHandler(&start, nil))
	good.Auth.GroupRules = o.Rules
	if _, err := admin.New(good); err != nil {
		t.Errorf("admin.New with the rules of the test, one of which names a custom role the tenant does not have: %v", err)
	}
	gone := -1
	for i, r := range o.Rules {
		if r.Role == "gone-role" {
			gone = i + 1
		}
	}
	warned := 0
	for _, line := range strings.Split(strings.TrimSpace(start.String()), "\n") {
		if !strings.Contains(line, "group rule skipped") {
			continue
		}
		warned++
		for _, name := range []string{"acme", "gone-role", "eng", p.tenant.ID} {
			if strings.Contains(line, name) {
				t.Errorf("the start warning names %q: %s", name, line)
			}
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec["level"] != "WARN" || rec["rule"] != float64(gone) {
			t.Errorf("the start warning: %s, want level WARN and rule %d", line, gone)
		}
	}
	if warned != 1 {
		t.Errorf("%d start lines say group rule skipped, want 1 (the missing role; the inactive one exists): %s", warned, start.String())
	}
}

// TestGroupRulesResolveInTheTransaction is part of T-882: the custom roles that group rules give are
// read in the transaction that assigns them, FOR SHARE, so that a role deleted meanwhile is not
// assigned, and all of them are found, whatever the number of roles of the tenant.
func TestGroupRulesResolveInTheTransaction(t *testing.T) {
	api, o := apitest.NewPeopleOIDC(t, apitest.PeopleConfig{},
		apitest.OIDCConfig{Users: idpPeople, Mappings: []string{"tenant:acme:aa-raced=eng", "tenant:acme:zzz-last=eng"}})
	p := peopleOn(t, api)
	d := p.d
	// One more than a page: zzz-last sorts after the first hundred roles.
	d.CustomRole(t, p.tid, "aa-raced", "member:read")
	for i := 0; i < 100; i++ {
		d.CustomRole(t, p.tid, fmt.Sprintf("m-%03d", i), "member:read")
	}
	d.CustomRole(t, p.tid, "zzz-last", "member:read")
	ada := d.FederatedUser(t, o.IDP.Issuer(), "ada", nil)
	o.IDP.SignInAs("ada")

	// A deletion of aa-raced holds the row while the sign-in runs, and commits once the sign-in
	// waits for it (or has finished).
	c := ctx(t)
	tx, err := d.Super(t).Begin(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(c) }()
	if _, err := tx.Exec(c, "SELECT 1 FROM pandokos.tenant_role WHERE tenant_id = $1::uuid AND name = 'aa-raced' FOR UPDATE", p.tenant.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan apitest.Hop, 1)
	go func() { done <- last(o.SignIn(t, api.NewBrowser())) }()
	var end apitest.Hop
	finished := false
	for deadline := time.Now().Add(15 * time.Second); !finished && time.Now().Before(deadline); {
		var waiting int
		if err := tx.QueryRow(c, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case end = <-done:
			finished = true
		case <-time.After(50 * time.Millisecond):
		}
	}
	if _, err := tx.Exec(c, "DELETE FROM pandokos.tenant_role WHERE tenant_id = $1::uuid AND name = 'aa-raced'", p.tenant.ID); err != nil {
		t.Errorf("the role could not be deleted: the sign-in assigned it while its deletion held the row: %v", err)
	}
	if err := tx.Commit(c); err != nil {
		t.Errorf("commit of the deletion: %v", err)
	}
	if !finished {
		select {
		case end = <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("the sign-in did not finish after the deletion")
		}
	}
	if end.Location != "/console" {
		t.Fatalf("the sign-in ends with %d %q", end.Status, end.Location)
	}
	var got string
	if err := d.Super(t).QueryRow(ctx(t), `SELECT COALESCE(string_agg(r.role || ':' || r.source, ',' ORDER BY r.role), '') FROM pandokos.membership_role r WHERE r.tenant_id = $1::uuid AND r.user_id = $2::uuid`,
		p.tenant.ID, ada.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "zzz-last:idp" {
		t.Errorf("the roles given in acme: %q, want zzz-last:idp (the role beyond the first page; not the deleted one)", got)
	}
}
