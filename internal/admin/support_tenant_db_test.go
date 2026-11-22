package admin_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestSupportGrants is T-875: under an action the platform grants with the tenant's authority, and a
// user cannot make itself a member.
func TestSupportGrants(t *testing.T) {
	w := newSW(t)
	person := w.d.PlatformUser(t, apitest.UniqueName("recovered"), nil)
	w.open(t, w.key, apitest.Reason("grants"), []string{authz.MemberWrite, authz.MemberRead}, nil)
	w.open(t, w.user, apitest.Reason("grants"), []string{authz.MemberWrite, authz.MemberRead}, nil)

	for name, who := range map[string]apitest.Doer{"a platform key": w.key, "a platform user": w.user} {
		created := decode[adminapi.MemberCreated](t, who.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("owner"), "roles": []string{"owner"}}, nil), 201)
		if !reflect.DeepEqual(created.Member.Roles, []string{"owner"}) {
			t.Errorf("%s: the member created under the action has the roles %v, want [owner]", name, created.Member.Roles)
		}
	}
	// A key may add an existing platform user; the user it adds is not itself.
	if r := w.key.Do(t, "POST", w.tp("/members"), map[string]any{"account": "existing", "user_id": person.ID, "roles": []string{"tenant-admin"}}, nil); r.Status != 201 {
		t.Errorf("a key adding another platform user: %d %s", r.Status, r.Body)
	}
	// A user cannot add itself: by ID, and by username.
	self := w.d.PlatformUser(t, apitest.UniqueName("self"), []string{authz.RolePlatformAdmin})
	b := w.api.NewBrowser()
	b.MustSignIn(t, self.Username, self.Password)
	apitest.OpenSupport(t, b, w.tenant.ID, authz.MemberWrite, authz.MemberRead)
	for what, body := range map[string]map[string]any{
		"its own ID":       {"account": "existing", "user_id": self.ID, "roles": []string{"owner"}},
		"its own username": {"account": "existing", "username": self.Username, "roles": []string{"owner"}},
	} {
		r := b.Do(t, "POST", w.tp("/members"), body, nil)
		if r.Status != 409 || r.Problem() != "invalid_state" {
			t.Errorf("adding %s under its own action: %d %s, want 409 invalid_state", what, r.Status, r.Body)
		}
	}
	var n int
	if err := w.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.membership WHERE user_id = $1::uuid`, self.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("the user became a member (%d, %v)", n, err)
	}
	// Without member:write in the action the same call is 403 forbidden.
	read := w.d.PlatformUser(t, apitest.UniqueName("reader"), []string{authz.RolePlatformAdmin})
	rb := w.api.NewBrowser()
	rb.MustSignIn(t, read.Username, read.Password)
	apitest.OpenSupport(t, rb, w.tenant.ID, authz.MemberRead)
	if r := rb.Do(t, "POST", w.tp("/members"), map[string]any{"account": "existing", "user_id": read.ID, "roles": []string{"owner"}}, nil); !apitest.Refused(r) {
		t.Errorf("adding itself without member:write in the action: %d %s, want 403 forbidden", r.Status, r.Body)
	}
	// Under an action a platform permission is never grantable: a platform role cannot be handed out through a member.
	if r := w.key.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("m"), "roles": []string{"platform-admin"}}, nil); r.Status != 400 {
		t.Errorf("a platform role through a member: %d %s, want 400", r.Status, r.Body)
	}
}

// TestTenantSeesAndEndsSupport is T-876.
func TestTenantSeesAndEndsSupport(t *testing.T) {
	w := newSW(t)
	other, _ := w.api.Tenant(t, "support-seen-other")
	action := w.open(t, w.key, apitest.Reason("seen"), []string{authz.MemberRead, authz.RoleRead}, nil)
	otherAction := apitest.OpenSupport(t, w.key, other.ID, authz.MemberRead)

	members := map[string]apitest.Doer{}
	for _, role := range []string{authz.RoleAuditor, authz.RoleReadOnly, authz.RoleUsageViewer, authz.RoleTenantAdmin, authz.RoleOwner} {
		members[role] = w.api.AsRole(t, w.tenant.ID, role)
		members[role+" (session)"] = w.api.MemberAs(t, w.tenant.ID, role)
	}
	for name, who := range members {
		role := strings.TrimSuffix(name, " (session)")
		canRead := role == authz.RoleAuditor || role == authz.RoleReadOnly || role == authz.RoleTenantAdmin || role == authz.RoleOwner
		list := who.Do(t, "GET", w.tp("/support-actions"), nil, nil)
		get := who.Do(t, "GET", w.tp("/support-actions/"+action.ID), nil, nil)
		if canRead {
			l := decode[adminapi.SupportActionList](t, list, 200)
			if len(l.Items) != 1 {
				t.Errorf("%s: %d actions listed, want its tenant's one", name, len(l.Items))
				continue
			}
			g := decode[adminapi.SupportAction](t, get, 200)
			if g.ID != action.ID || g.Reason != action.Reason || g.Actor.Type != adminapi.SupportActionActorTypePlatform || g.Actor.ID == "" ||
				!reflect.DeepEqual(g.Permissions, []string{authz.MemberRead, authz.RoleRead}) || g.State != "open" || g.OpenedAt.IsZero() || g.ExpiresAt.IsZero() {
				t.Errorf("%s: the action as the tenant sees it: %+v", name, g)
			}
		} else if !apitest.Refused(list) || !apitest.Refused(get) {
			t.Errorf("%s: list %d, get %d, want 403 forbidden", name, list.Status, get.Status)
		}
		// Another tenant's action is not there, whoever asks.
		if canRead {
			if r := who.Do(t, "GET", w.tp("/support-actions/"+otherAction), nil, nil); r.Status != 404 {
				t.Errorf("%s: another tenant's action: %d, want 404", name, r.Status)
			}
		}
	}
	// Only an owner ends one.
	for _, role := range []string{authz.RoleTenantAdmin, authz.RoleAuditor, authz.RoleReadOnly} {
		for _, name := range []string{role, role + " (session)"} {
			if r := members[name].Do(t, "POST", w.tp("/support-actions/"+action.ID+"/end"), map[string]any{}, nil); !apitest.Refused(r) {
				t.Errorf("%s ending the action: %d %s, want 403 forbidden", name, r.Status, r.Body)
			}
		}
	}
	if r := w.key.Do(t, "GET", w.tp("/members"), nil, nil); r.Status != 200 {
		t.Fatalf("the action is still open: %d", r.Status)
	}
	if r := members["owner"].Do(t, "POST", w.tp("/support-actions/"+otherAction+"/end"), map[string]any{}, nil); r.Status != 404 {
		t.Errorf("the owner ending another tenant's action: %d, want 404", r.Status)
	}
	reason := apitest.Reason("tenant ends")
	ended := decode[adminapi.SupportAction](t, members["owner (session)"].Do(t, "POST", w.tp("/support-actions/"+action.ID+"/end"), map[string]any{"reason": reason}, nil), 200)
	if ended.State != "ended" || ended.ClosedBy != adminapi.SupportActionClosedByTenant || ended.ClosedAt == nil || ended.CloseReason != reason {
		t.Errorf("the ended action: %+v", ended)
	}
	// It stops at the next request, and ending it again changes nothing.
	if r := w.key.Do(t, "GET", w.tp("/members"), nil, nil); !apitest.Refused(r) {
		t.Errorf("the platform key after the end: %d %s, want 403 forbidden", r.Status, r.Body)
	}
	again := decode[adminapi.SupportAction](t, members["owner"].Do(t, "POST", w.tp("/support-actions/"+action.ID+"/end"), map[string]any{}, nil), 200)
	if again.State != "ended" || again.ClosedAt == nil || !again.ClosedAt.Equal(*ended.ClosedAt) {
		t.Errorf("ending an ended action changed it: %+v", again)
	}
	// The tenant reads how it ended, and the platform reads the same.
	for _, who := range []apitest.Doer{members["auditor"], w.key} {
		var got adminapi.SupportAction
		path := w.tp("/support-actions/" + action.ID)
		if _, isKey := who.(*apitest.Caller); isKey && who == apitest.Doer(w.key) {
			path = w.sp("/" + action.ID)
		}
		got = decode[adminapi.SupportAction](t, who.Do(t, "GET", path, nil, nil), 200)
		if got.State != "ended" || got.ClosedBy != adminapi.SupportActionClosedByTenant {
			t.Errorf("how it ended: %+v", got)
		}
	}
	// The three other states.
	a2 := w.open(t, w.key, apitest.Reason("closed"), []string{authz.MemberRead}, nil)
	if r := w.key.Do(t, "POST", w.sp("/"+a2.ID+"/close"), map[string]any{"reason": "done"}, nil); r.Status != 200 {
		t.Fatal(r.Status)
	}
	if g := decode[adminapi.SupportAction](t, members["owner"].Do(t, "GET", w.tp("/support-actions/"+a2.ID), nil, nil), 200); g.State != "closed" || g.ClosedBy != adminapi.SupportActionClosedByPlatform {
		t.Errorf("an action closed by the platform: %+v", g)
	}
	a3 := w.open(t, w.key, apitest.Reason("expired"), []string{authz.MemberRead}, nil)
	w.d.ExpireSupportAction(t, a3.ID)
	if g := decode[adminapi.SupportAction](t, members["owner"].Do(t, "GET", w.tp("/support-actions/"+a3.ID), nil, nil), 200); g.State != "expired" {
		t.Errorf("an expired action: %+v", g)
	}
	// The list filters by state and sorts and pages.
	open := decode[adminapi.SupportActionList](t, members["owner"].Do(t, "GET", w.tp("/support-actions?state=open"), nil, nil), 200)
	closed := decode[adminapi.SupportActionList](t, members["owner"].Do(t, "GET", w.tp("/support-actions?state=closed&sort=opened_at&limit=2"), nil, nil), 200)
	if len(open.Items) != 0 || len(closed.Items) != 2 || closed.NextCursor == "" || !closed.Items[0].OpenedAt.Before(closed.Items[1].OpenedAt.Add(time.Second)) {
		t.Errorf("open %d, closed %d (next %q)", len(open.Items), len(closed.Items), closed.NextCursor)
	}
}

// TestRolesTelemetry is T-889: at log level debug, over the calls of the roles and support tests,
// no line holds a tenant ID or name, a role name, a custom role's permissions, a reason or an
// action ID, and the line of a support principal says support: true. The administration plane
// records no span.
func TestRolesTelemetry(t *testing.T) {
	w := newSW(t)
	var reasons, actions, roleNames []string
	custom := w.role(t, apitest.UniqueName("telemetry-role"), authz.MemberRead, authz.RoleRead, authz.TenantSettingWrite)
	roleNames = append(roleNames, custom.Name)
	// The calls of T-859.
	reason := apitest.Reason("telemetry")
	reasons = append(reasons, reason)
	w.owner.Do(t, "PATCH", w.tp("/roles/"+custom.Name), map[string]any{"description": "d", "permissions": []string{authz.MemberRead, authz.RoleRead, authz.UsageRead}, "reason": reason}, nil)
	w.owner.Do(t, "GET", w.tp("/roles"), nil, nil)
	w.owner.Do(t, "GET", w.tp("/roles/"+custom.Name), nil, nil)
	deact := apitest.Reason("deactivate")
	reasons = append(reasons, deact)
	w.owner.Do(t, "POST", w.tp("/roles/"+custom.Name+"/deactivate"), map[string]any{"reason": deact}, nil)
	w.owner.Do(t, "POST", w.tp("/roles/"+custom.Name+"/activate"), map[string]any{"reason": deact}, nil)
	w.owner.Do(t, "POST", w.tp("/roles"), map[string]any{"name": "owner", "permissions": []string{authz.MemberRead}}, nil) // refused: a built-in name
	w.owner.Do(t, "POST", w.tp("/roles"), map[string]any{"name": apitest.UniqueName("grab"), "permissions": []string{"tenant:read"}}, nil)
	admin := w.api.AsRole(t, w.tenant.ID, authz.RoleTenantAdmin)
	admin.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"owner"}}, nil)             // escalation_refused
	admin.Do(t, "POST", w.tp("/roles"), map[string]any{"name": apitest.UniqueName("grab"), "permissions": []string{authz.SupportActionEnd}}, nil) // escalation_refused
	// The calls of T-870.
	open := apitest.Reason("telemetry open")
	reasons = append(reasons, open)
	a := w.open(t, w.key, open, []string{authz.MemberRead, authz.ServiceAccountRead, authz.RoleRead}, nil)
	actions = append(actions, a.ID)
	// The calls of T-873: reads and writes under the action, by a key and by a user.
	w.key.Do(t, "GET", w.tp("/members"), nil, nil)
	w.key.Do(t, "GET", w.tp("/service-accounts"), nil, nil)
	w.key.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"inference"}}, nil)
	closeReason := apitest.Reason("telemetry close")
	reasons = append(reasons, closeReason)
	w.key.Do(t, "POST", w.sp("/"+a.ID+"/close"), map[string]any{"reason": closeReason}, nil)
	b := w.open(t, w.user, apitest.Reason("telemetry user"), []string{authz.MemberRead}, nil)
	actions = append(actions, b.ID)
	w.user.Do(t, "GET", w.tp("/members"), nil, nil)
	endReason := apitest.Reason("telemetry end")
	reasons = append(reasons, endReason)
	w.owner.Do(t, "POST", w.tp("/support-actions/"+b.ID+"/end"), map[string]any{"reason": endReason}, nil)
	w.owner.Do(t, "GET", w.tp("/support-actions"), nil, nil)

	logs := w.api.Logs.String()
	if len(logs) == 0 {
		t.Fatal("no log at all")
	}
	forbidden := map[string][]string{
		"the tenant's ID":   {w.tenant.ID},
		"the tenant's name": {`"` + w.tenant.Name + `"`},
		"a reason":          reasons,
		"an action ID":      actions,
		"a custom role":     roleNames,
		"a built-in role":   {"usage-viewer", "tenant-admin", "credential-manager", "read-only", "platform-admin", "platform-auditor"},
		"the permissions of a custom role": {
			`"member:read","role:read","tenant_setting:write"`, "member:read role:read tenant_setting:write", "member:read,role:read,tenant_setting:write",
			`"member:read","role:read","usage:read"`, "member:read role:read usage:read", "member:read,role:read,usage:read",
		},
	}
	for what, needles := range forbidden {
		for _, n := range needles {
			if n != "" && strings.Contains(logs, n) {
				t.Errorf("the log holds %s (%q)", what, n)
			}
		}
	}
	// The lines of a support principal say so and name the platform principal as on the platform.
	supportLines, plainLines := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "admin request" {
			continue
		}
		op, _ := rec["operation"].(string)
		// A platform principal succeeds at an operation of a tenant only under a support action.
		status, _ := rec["status"].(float64)
		underAction := (op == "listMembers" || op == "listServiceAccounts" || op == "createServiceAccount") && rec["principal_type"] == "platform" && status < 300
		if rec["support"] == true {
			supportLines++
			if rec["principal_type"] != "platform" && rec["principal_type"] != "user" {
				t.Errorf("a support line names the principal type %v", rec["principal_type"])
			}
			if _, ok := rec["principal_id"]; !ok {
				t.Errorf("a support line does not name the platform principal: %s", line)
			}
		} else if underAction {
			plainLines++
		}
	}
	if supportLines < 3 {
		t.Errorf("%d request lines say support: true, want at least the reads and the write of the key and the user's read", supportLines)
	}
	if plainLines != 0 {
		t.Errorf("%d request lines of requests made under an action do not say support: true", plainLines)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, `"support":true`) && (strings.Contains(line, "tenant_id") || strings.Contains(line, "action_id")) {
			t.Errorf("a support line names the tenant or the action: %s", line)
		}
	}
	// A refused grant logs the request ID and the operation, nothing more.
	refusedLines := 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, "grant refused") {
			refusedLines++
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatal(err)
			}
			for k := range rec {
				switch k {
				case "time", "level", "msg", "request_id", "operation":
				default:
					t.Errorf("the line of a refused grant carries %q: %s", k, line)
				}
			}
		}
	}
	if refusedLines < 2 {
		t.Errorf("%d lines say grant refused, want one for each of the two refused grants", refusedLines)
	}
}
