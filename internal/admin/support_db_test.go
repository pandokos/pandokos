package admin_test

// The tests of support actions (sprint 4.3, section 5.6): T-870 to T-876, and the telemetry of
// roles and support actions, T-889.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/session"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// sw is a tenant, its owner, and two platform administrators: a key and a user.
type sw struct {
	*rw
	key    *apitest.Caller
	keyID  string
	user   *apitest.Browser
	userID string
}

func newSW(t *testing.T, mutate ...func(*admin.Options)) *sw {
	t.Helper()
	cfg := apitest.PeopleConfig{}
	if len(mutate) > 0 {
		cfg.Admin = func(o *admin.Options) {
			for _, m := range mutate {
				m(o)
			}
		}
	}
	p := newPeople(t, cfg)
	w := &sw{rw: &rw{people: p, owner: p.api.Owner(t, p.tenant.ID)}}
	w.key, w.user = w.api.PlatformAs(t, authz.RolePlatformAdmin)
	w.keyID = decode[adminapi.Me](t, w.key.Do(t, "GET", "/v1/me", nil, nil), 200).KeyID
	w.userID = decode[adminapi.Me](t, w.user.Do(t, "GET", "/v1/me", nil, nil), 200).ID
	return w
}

func (w *sw) sp(rest string) string { return "/v1/tenant-support/" + w.tenant.ID + "/actions" + rest }

// open opens an action as who and returns it.
func (w *sw) open(t *testing.T, who apitest.Doer, reason string, perms []string, extra map[string]any) adminapi.SupportAction {
	t.Helper()
	body := map[string]any{"reason": reason, "permissions": perms}
	for k, v := range extra {
		body[k] = v
	}
	return decode[adminapi.SupportAction](t, who.Do(t, "POST", w.sp(""), body, nil), 201)
}

func tenantOps() []adminapi.Operation {
	var out []adminapi.Operation
	for _, op := range probeOps() {
		if perm, ok := authz.Lookup(op.Permission); ok && perm.Scope == authz.ScopeTenant {
			out = append(out, op)
		}
	}
	return out
}

// TestSupportOpen is T-870.
func TestSupportOpen(t *testing.T) {
	w := newSW(t)
	reasonOf := func() string { return apitest.Reason("recover") }
	valid := []string{authz.MemberRead, authz.ServiceAccountRead}

	t.Run("the request", func(t *testing.T) {
		for what, body := range map[string]map[string]any{
			"no reason":      {"permissions": valid},
			"an empty one":   {"reason": "", "permissions": valid},
			"a blank one":    {"reason": " \t\n", "permissions": valid},
			"a long one":     {"reason": strings.Repeat("r", 501), "permissions": valid},
			"no permissions": {"reason": reasonOf()},
			"an empty list":  {"reason": reasonOf(), "permissions": []string{}},
			"twelve":         {"reason": reasonOf(), "permissions": append(authz.SupportPermissions(), authz.MemberRead)},
			"a repeated one": {"reason": reasonOf(), "permissions": []string{authz.MemberRead, authz.MemberRead}},
		} {
			if r := w.key.Do(t, "POST", w.sp(""), body, nil); r.Status != 400 || r.Problem() != "invalid_request" {
				t.Errorf("%s: %d %s, want 400 invalid_request", what, r.Status, r.Body)
			}
		}
		for _, perm := range authz.TenantPermissions() {
			if p, _ := authz.Lookup(perm); p.Support {
				continue
			}
			r := w.key.Do(t, "POST", w.sp(""), map[string]any{"reason": reasonOf(), "permissions": []string{authz.MemberRead, perm}}, nil)
			invalidOn(t, "a permission without the attribute, "+perm, r, "permissions")
		}
		for _, perm := range []string{"tenant:read", "tenant_support:open", "identity:read", "session:self", "ghost:thing"} {
			invalidOn(t, "the permission "+perm, w.key.Do(t, "POST", w.sp(""), map[string]any{"reason": reasonOf(), "permissions": []string{perm}}, nil), "permissions")
		}
		for _, r := range w.d.PlatformAuditRows(t) {
			if r.Action == "support_action.open" {
				t.Errorf("a refused opening wrote a platform audit row: %+v", r)
			}
		}
		if n := len(w.audit(t, "support_action.open")); n != 0 {
			t.Errorf("a refused opening wrote %d rows in the tenant's log", n)
		}
	})
	t.Run("the duration", func(t *testing.T) {
		// 0, like an absent member, is the default; 1 to 59 are below the bound.
		for what, secs := range map[string]int{"1 second": 1, "59 seconds": 59, "negative": -5, "above the bound of the schema": 86401, "above support.max_duration": 3601} {
			body := map[string]any{"reason": reasonOf(), "permissions": valid, "duration_seconds": secs}
			r := w.key.Do(t, "POST", w.sp(""), body, nil)
			if r.Status != 400 || !hasField(problemOf(t, r), "duration_seconds") {
				t.Errorf("%s: %d %s, want 400 on duration_seconds", what, r.Status, r.Body)
			}
		}
		a := w.open(t, w.key, reasonOf(), valid, map[string]any{"duration_seconds": 60})
		if d := a.ExpiresAt.Sub(a.OpenedAt); d < 59*time.Second || d > 61*time.Second {
			t.Errorf("60 seconds gave a lifetime of %v", d)
		}
		a = w.open(t, w.key, reasonOf(), valid, nil)
		if d := a.ExpiresAt.Sub(a.OpenedAt); d < time.Hour-2*time.Second || d > time.Hour+2*time.Second {
			t.Errorf("the default duration is %v, want support.max_duration (1h)", d)
		}
		a = w.open(t, w.key, reasonOf(), valid, map[string]any{"duration_seconds": 0})
		if d := a.ExpiresAt.Sub(a.OpenedAt); d < time.Hour-2*time.Second || d > time.Hour+2*time.Second {
			t.Errorf("duration_seconds 0 gave %v, want the default, support.max_duration (1h)", d)
		}
		// The bound is the configuration's: with two hours, two hours is the default and the bound.
		long := newSW(t, func(o *admin.Options) { o.Support.MaxDuration = 2 * time.Hour })
		a = long.open(t, long.key, reasonOf(), valid, nil)
		if d := a.ExpiresAt.Sub(a.OpenedAt); d < 2*time.Hour-2*time.Second || d > 2*time.Hour+2*time.Second {
			t.Errorf("the default under support.max_duration 2h is %v", d)
		}
		if r := long.key.Do(t, "POST", long.sp(""), map[string]any{"reason": reasonOf(), "permissions": valid, "duration_seconds": 7201}, nil); r.Status != 400 {
			t.Errorf("7201 seconds under support.max_duration 2h: %d", r.Status)
		}
	})
	t.Run("the response, the row, the two audit events", func(t *testing.T) {
		v := newSW(t)
		reason := reasonOf()
		a := v.open(t, v.key, reason, []string{authz.ServiceAccountRead, authz.MemberRead}, map[string]any{"duration_seconds": 900})
		if a.TenantID != v.tenant.ID || a.Reason != reason || a.State != "open" || !reflect.DeepEqual(a.Permissions, []string{authz.MemberRead, authz.ServiceAccountRead}) ||
			a.Actor.Type != adminapi.SupportActionActorTypePlatform || a.Actor.ID != v.keyID || a.ClosedAt != nil || a.ID == "" {
			t.Errorf("the response: %+v", a)
		}
		if d := a.ExpiresAt.Sub(a.OpenedAt); d < 899*time.Second || d > 901*time.Second {
			t.Errorf("the lifetime is %v, want the 900 s asked", d)
		}
		var actor, actorID, reasonRow string
		var perms []string
		if err := v.api.Super(t).QueryRow(ctx(t), `SELECT actor_type, actor_id, reason, permissions FROM pandokos.support_action WHERE id = $1::uuid AND tenant_id = $2::uuid`, a.ID, v.tenant.ID).
			Scan(&actor, &actorID, &reasonRow, &perms); err != nil {
			t.Fatal(err)
		}
		if actor != "platform" || actorID != v.keyID || reasonRow != reason || !reflect.DeepEqual(perms, []string{authz.MemberRead, authz.ServiceAccountRead}) {
			t.Errorf("the row: %s %s %q %v", actor, actorID, reasonRow, perms)
		}
		rows := v.audit(t, "support_action.open")
		if len(rows) != 1 || rows[0].Reason != reason || rows[0].SupportActionID != a.ID || rows[0].ActorType != "platform" || rows[0].ActorID != v.keyID || rows[0].ResourceType != "support_action" {
			t.Fatalf("the tenant's audit rows: %+v", rows)
		}
		if !reflect.DeepEqual(rows[0].Detail["permissions"], []any{authz.MemberRead, authz.ServiceAccountRead}) || rows[0].Detail["expires_at"] == nil {
			t.Errorf("the tenant's detail: %v", rows[0].Detail)
		}
		var platformRow *dbtest.AuditRow
		for _, r := range v.d.PlatformAuditRows(t) {
			if r.Action == "support_action.open" && r.Detail["tenant_id"] == v.tenant.ID {
				r := r
				platformRow = &r
			}
		}
		if platformRow == nil || platformRow.Reason != reason || platformRow.ResourceID != a.ID {
			t.Fatalf("the platform's audit row: %+v", platformRow)
		}
		// A second opening by the same actor closes the first.
		b := v.open(t, v.key, reasonOf(), []string{authz.RoleRead}, nil)
		first := decode[adminapi.SupportAction](t, v.key.Do(t, "GET", v.sp("/"+a.ID), nil, nil), 200)
		if first.State != "closed" || first.ClosedBy != adminapi.SupportActionClosedByPlatform || first.ClosedAt == nil {
			t.Errorf("the first action after a second opening: %+v", first)
		}
		if b.State != "open" || b.ID == a.ID {
			t.Errorf("the second: %+v", b)
		}
		open := decode[adminapi.SupportActionList](t, v.key.Do(t, "GET", v.sp("?state=open"), nil, nil), 200)
		if len(open.Items) != 1 || open.Items[0].ID != b.ID {
			t.Errorf("the open actions: %+v", open.Items)
		}
		if rows := v.audit(t, "support_action.open"); len(rows) != 2 || rows[1].Detail["replaces"] == nil {
			t.Errorf("the second opening names what it replaced: %+v", rows)
		}
		// Another actor in the same tenant has an action of its own.
		c := v.open(t, v.user, reasonOf(), []string{authz.MemberRead}, nil)
		if list := decode[adminapi.SupportActionList](t, v.key.Do(t, "GET", v.sp("?state=open"), nil, nil), 200); len(list.Items) != 2 {
			t.Errorf("two actors, two open actions: %d (%s)", len(list.Items), c.ID)
		}
	})
	t.Run("who may open one", func(t *testing.T) {
		auditor, _ := w.api.PlatformAs(t, authz.RolePlatformAuditor)
		body := map[string]any{"reason": reasonOf(), "permissions": valid}
		if r := auditor.Do(t, "POST", w.sp(""), body, nil); !apitest.Refused(r) {
			t.Errorf("a platform auditor: %d %s, want 403 forbidden", r.Status, r.Body)
		}
		if r := w.owner.Do(t, "POST", w.sp(""), body, nil); !apitest.Refused(r) {
			t.Errorf("a tenant's owner: %d %s, want 403 forbidden", r.Status, r.Body)
		}
		if r := w.key.Do(t, "POST", "/v1/tenant-support/"+apitest.NoUser+"/actions", body, nil); r.Status != 404 {
			t.Errorf("an unknown tenant: %d %s, want 404", r.Status, r.Body)
		}
		// A platform auditor reads what the platform did.
		if r := auditor.Do(t, "GET", w.sp(""), nil, nil); r.Status != 200 {
			t.Errorf("a platform auditor listing: %d %s", r.Status, r.Body)
		}
		a := w.open(t, w.key, reasonOf(), valid, nil)
		if r := auditor.Do(t, "GET", w.sp("/"+a.ID), nil, nil); r.Status != 200 {
			t.Errorf("a platform auditor reading: %d %s", r.Status, r.Body)
		}
		if r := auditor.Do(t, "POST", w.sp("/"+a.ID+"/close"), map[string]any{}, nil); !apitest.Refused(r) {
			t.Errorf("a platform auditor closing: %d %s", r.Status, r.Body)
		}
		for _, path := range []string{w.sp("/" + apitest.NoUser), "/v1/tenant-support/" + apitest.NoUser + "/actions"} {
			if r := w.key.Do(t, "GET", path, nil, nil); r.Status != 404 {
				t.Errorf("GET %s: %d, want 404", path, r.Status)
			}
		}
	})
	t.Run("support.enabled is false", func(t *testing.T) {
		off := newSW(t, func(o *admin.Options) { o.Support.Enabled = false })
		r := off.key.Do(t, "POST", off.sp(""), map[string]any{"reason": reasonOf(), "permissions": valid}, nil)
		if r.Status != 403 || r.Problem() != "support_disabled" {
			t.Errorf("an opening with the switch off: %d %s, want 403 support_disabled", r.Status, r.Body)
		}
		var n int
		if err := off.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.support_action`).Scan(&n); err != nil || n != 0 {
			t.Errorf("%d support actions after a refused opening (%v)", n, err)
		}
		if n := len(off.audit(t, "support_action.open")); n != 0 {
			t.Errorf("%d audit rows after a refused opening", n)
		}
		// Reading and closing work with the switch off.
		if r := off.key.Do(t, "GET", off.sp(""), nil, nil); r.Status != 200 {
			t.Errorf("listing with the switch off: %d", r.Status)
		}
	})
}

// TestSupportPrincipal is T-871.
func TestSupportPrincipal(t *testing.T) {
	w := newSW(t)
	otherTenant, _ := w.api.Tenant(t, "support-other")
	p := probeParams(t, w.api, w.tenant.ID)
	pOther := probeParams(t, w.api, otherTenant.ID)
	ops := tenantOps()
	listed := []string{authz.MemberRead, authz.ServiceAccountRead, authz.RoleRead, authz.VirtualKeyWrite}
	allowedFor := func(op adminapi.Operation, person bool) bool {
		return setOf(listed)[op.Permission] || (person && (op.Permission == authz.IdentityRead || op.Permission == authz.SessionSelf))
	}
	// A user that is a platform user with no platform role at all: the non-member of 4.2.
	nobody := w.d.PlatformUser(t, apitest.UniqueName("nobody"), nil)
	nobodyBrowser := w.api.NewBrowser()
	nobodyBrowser.MustSignIn(t, nobody.Username, nobody.Password)

	record := func(who apitest.Doer, pp apitest.Params) map[string]string {
		out := map[string]string{}
		for _, op := range ops {
			out[op.ID] = normalize(apitest.Probe(t, who, op, pp))
		}
		return out
	}
	// Recorded before any action exists: the answers of 4.2, which are the same for every operation.
	keyBase, userBase := record(w.key, p), record(w.user, p)
	nobodyBase := record(nobodyBrowser, p)
	keyRefusal, userRefusal := keyBase[ops[0].ID], userBase[ops[0].ID]
	for _, op := range ops {
		if keyBase[op.ID] != keyRefusal || userBase[op.ID] != userRefusal {
			t.Errorf("%s: the refusal of a platform key or user differs from that of another operation:\n%s\n%s", op.ID, keyBase[op.ID], keyRefusal)
		}
	}
	for _, op := range ops {
		if r := apitest.Probe(t, w.key, op, p); !apitest.Refused(r) {
			t.Errorf("%s with a platform key and no action: %d %s, want 403 forbidden", op.ID, r.Status, short(r))
		}
		if r := apitest.Probe(t, w.user, op, p); r.Status != 404 || r.Problem() != "not_found" {
			t.Errorf("%s with a platform user and no action: %d %s, want 404 not_found", op.ID, r.Status, short(r))
		}
		if userBase[op.ID] != nobodyBase[op.ID] {
			t.Errorf("%s: the answer to a platform administrator and to a user with no role differ without an action:\n%s\n%s", op.ID, userBase[op.ID], nobodyBase[op.ID])
		}
	}
	// atBaseline reports whether the answer is the one of 4.2, byte for byte but for the request ID.
	atBaseline := func(t *testing.T, what string, r *apitest.Response, want string) {
		t.Helper()
		if got := normalize(r); got != want {
			t.Errorf("%s: the answer is not the one of 4.2:\n%s\n%s", what, got, want)
		}
	}
	honoured := func(what string, who apitest.Doer, person bool) {
		t.Helper()
		allowed, refused := 0, 0
		for _, op := range ops {
			r := apitest.Probe(t, who, op, p)
			if allowedFor(op, person) {
				allowed++
				if apitest.Refused(r) || r.Status == 401 {
					t.Errorf("%s: %s (%s) is listed in the action and was refused: %d %s", what, op.ID, op.Permission, r.Status, short(r))
				}
			} else {
				refused++
				if !apitest.Refused(r) {
					t.Errorf("%s: %s (%s) is not in the action and was answered %d %s, want 403 forbidden", what, op.ID, op.Permission, r.Status, short(r))
				}
			}
		}
		if allowed < 4 || refused < 20 {
			t.Errorf("%s: %d allowed and %d refused operations", what, allowed, refused)
		}
	}

	t.Run("exactly the listed permissions are allowed, to a key and to a user", func(t *testing.T) {
		w.open(t, w.key, apitest.Reason("key"), listed, nil)
		honoured("a platform key", w.key, false)
		w.open(t, w.user, apitest.Reason("user"), listed, nil)
		honoured("a platform user", w.user, true)
		// Another tenant is unaffected.
		for _, op := range tenantOps() {
			if r := apitest.Probe(t, w.key, op, pOther); !apitest.Refused(r) {
				t.Errorf("%s in another tenant under an action of this one: %d %s", op.ID, r.Status, short(r))
				break
			}
		}
		// The two actions have their own rows: the user's action does not make the key's wider.
		k := decode[adminapi.SupportActionList](t, w.key.Do(t, "GET", w.sp("?state=open"), nil, nil), 200)
		if len(k.Items) != 2 {
			t.Errorf("%d open actions", len(k.Items))
		}
	})
	t.Run("a platform principal is a member's equal only if it is one: the membership wins", func(t *testing.T) {
		both := w.d.PlatformUser(t, apitest.UniqueName("both"), []string{authz.RolePlatformAdmin})
		w.d.Member(t, w.tid, both.ID, []string{authz.RoleReadOnly})
		b := w.api.NewBrowser()
		b.MustSignIn(t, both.Username, both.Password)
		w.open(t, b, apitest.Reason("member"), []string{authz.ServiceAccountWrite, authz.ServiceAccountRead}, nil)
		if r := b.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": apitest.UniqueName("sa"), "roles": []string{"read-only"}}, nil); !apitest.Refused(r) {
			t.Errorf("a member that is also a platform user, with an action that lists service_account:write: %d %s, want 403: the member's roles win", r.Status, short(r))
		}
		if r := b.Do(t, "GET", w.tp("/members"), nil, nil); r.Status != 200 {
			t.Errorf("the member's own read-only role: %d %s", r.Status, short(r))
		}
		if r := b.Do(t, "GET", w.tp("/roles"), nil, nil); r.Status != 200 {
			t.Errorf("read-only reads roles: %d", r.Status)
		}
		// virtual_key:write is in neither the member's roles... and read-only reads keys but not writes.
		if r := b.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": apitest.NoUser}, nil); !apitest.Refused(r) {
			t.Errorf("read-only issuing a key: %d", r.Status)
		}
	})
	t.Run("a suspended tenant is 403 tenant_suspended", func(t *testing.T) {
		v := newSW(t)
		vp := probeParams(t, v.api, v.tenant.ID)
		v.open(t, v.key, apitest.Reason("susp"), listed, nil)
		if _, err := v.api.SuspendTenant(t, v.api.PlatformKey(), v.tenant.ID); err != nil {
			t.Fatal(err)
		}
		for _, op := range ops {
			if !setOf(listed)[op.Permission] {
				continue
			}
			if r := apitest.Probe(t, v.key, op, vp); r.Status != 403 || r.Problem() != "tenant_suspended" {
				t.Errorf("%s under an action in a suspended tenant: %d %s, want 403 tenant_suspended", op.ID, r.Status, short(r))
			}
		}
		if _, err := v.api.ReactivateTenant(t, v.api.PlatformKey(), v.tenant.ID); err != nil {
			t.Fatal(err)
		}
		if r := apitest.Probe(t, v.key, ops[0], vp); r.Status == 403 && r.Problem() == "tenant_suspended" {
			t.Error("the tenant is active again and the action is still refused")
		}
	})

	// The refusal is back at the next request.
	t.Run("expiry", func(t *testing.T) {
		v := newSW(t)
		vp := probeParams(t, v.api, v.tenant.ID)
		a := v.open(t, v.key, apitest.Reason("exp"), listed, nil)
		if r := apitest.Probe(t, v.key, mustOp(t, "listMembers"), vp); r.Status != 200 {
			t.Fatalf("under the action: %d", r.Status)
		}
		v.d.ExpireSupportAction(t, a.ID)
		atBaseline(t, "after the expiry", apitest.Probe(t, v.key, mustOp(t, "listMembers"), vp), keyRefusal)
		got := decode[adminapi.SupportAction](t, v.key.Do(t, "GET", v.sp("/"+a.ID), nil, nil), 200)
		if got.State != "expired" {
			t.Errorf("an expired action reads as %q", got.State)
		}
	})
	t.Run("closed by the platform, ended by the tenant", func(t *testing.T) {
		v := newSW(t)
		vp := probeParams(t, v.api, v.tenant.ID)
		a := v.open(t, v.key, apitest.Reason("close"), listed, nil)
		if r := v.key.Do(t, "POST", v.sp("/"+a.ID+"/close"), map[string]any{"reason": "done"}, nil); r.Status != 200 {
			t.Fatalf("close: %d %s", r.Status, r.Body)
		}
		atBaseline(t, "after the platform closed it", apitest.Probe(t, v.key, mustOp(t, "listMembers"), vp), keyRefusal)
		b := v.open(t, v.key, apitest.Reason("end"), listed, nil)
		if r := v.owner.Do(t, "POST", v.tp("/support-actions/"+b.ID+"/end"), map[string]any{"reason": "no longer needed"}, nil); r.Status != 200 {
			t.Fatalf("end: %d %s", r.Status, r.Body)
		}
		atBaseline(t, "after the tenant ended it", apitest.Probe(t, v.key, mustOp(t, "listMembers"), vp), keyRefusal)
	})
	t.Run("the switch off, the loss of the platform role, a disabled user", func(t *testing.T) {
		v := newSW(t)
		vp := probeParams(t, v.api, v.tenant.ID)
		v.open(t, v.key, apitest.Reason("switch"), listed, nil)
		v.open(t, v.user, apitest.Reason("switch"), listed, nil)
		off := apitest.NewPeopleOn(t, v.api.DB, apitest.PeopleConfig{Admin: func(o *admin.Options) { o.Support.Enabled = false }})
		offKey := &apitest.Caller{API: off, Key: v.key.Key}
		offUser := off.NewBrowser()
		offUser.SetCookie(session.CookieName, v.user.Token())
		for _, op := range []adminapi.Operation{mustOp(t, "listMembers"), mustOp(t, "listServiceAccounts")} {
			atBaseline(t, "a key with support.enabled false, "+op.ID, apitest.Probe(t, offKey, op, vp), keyRefusal)
			atBaseline(t, "a user with support.enabled false, "+op.ID, apitest.Probe(t, offUser, op, vp), userRefusal)
		}
		// With the switch on again the action is honoured (nothing was closed).
		if r := apitest.Probe(t, v.key, mustOp(t, "listMembers"), vp); r.Status != 200 {
			t.Errorf("the action with the switch on: %d", r.Status)
		}
		// The user loses its platform role: its action is not honoured at the next request.
		if r := v.platform(t, "PATCH", "/v1/users/"+v.userID, map[string]any{"platform_roles": []string{authz.RolePlatformAuditor}}); r.Status != 200 {
			t.Fatalf("changing the roles of the user: %d %s", r.Status, r.Body)
		}
		atBaseline(t, "a user that lost tenant_support:open", apitest.Probe(t, v.user, mustOp(t, "listMembers"), vp), userRefusal)
		// The platform role is back, the action is honoured again, and a disabled user is refused.
		if r := v.platform(t, "PATCH", "/v1/users/"+v.userID, map[string]any{"platform_roles": []string{authz.RolePlatformAdmin}}); r.Status != 200 {
			t.Fatalf("restoring: %d", r.Status)
		}
		if r := apitest.Probe(t, v.user, mustOp(t, "listMembers"), vp); r.Status != 200 {
			t.Errorf("the role back: %d %s", r.Status, short(r))
		}
		if r := v.platform(t, "POST", "/v1/users/"+v.userID+"/disable", map[string]any{}); r.Status != 200 {
			t.Fatalf("disabling: %d %s", r.Status, r.Body)
		}
		if r := apitest.Probe(t, v.user, mustOp(t, "listMembers"), vp); r.Status != 401 {
			t.Errorf("a disabled user: %d %s, want 401", r.Status, short(r))
		}
	})
}

func mustOp(t testing.TB, id string) adminapi.Operation {
	t.Helper()
	for _, op := range adminapi.Operations {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("no operation %s", id)
	return adminapi.Operation{}
}

// TestSupportNeverReadsUsage is T-872, the exit condition: no support action reaches usage, limits,
// prices, credentials, models, issuers, mappings or the settings of a tenant.
func TestSupportNeverReadsUsage(t *testing.T) {
	w := newSW(t)
	p := probeParams(t, w.api, w.tenant.ID)
	forbidden := map[string]bool{}
	for _, perm := range authz.TenantPermissions() {
		switch resourceOf(perm) {
		case "usage", "limit", "price_list", "credential", "model", "token_issuer", "subject_mapping", "tenant_setting":
			forbidden[perm] = true
		}
	}
	if len(forbidden) < 20 {
		t.Fatalf("%d forbidden permissions", len(forbidden))
	}
	var ops []adminapi.Operation
	for _, op := range tenantOps() {
		if forbidden[op.Permission] {
			ops = append(ops, op)
		}
	}
	if len(ops) < 40 {
		t.Fatalf("%d operations under the forbidden permissions", len(ops))
	}
	for name, who := range map[string]apitest.Doer{"a platform key": w.key, "a platform user": w.user} {
		refused := func(what string, r *apitest.Response, op adminapi.Operation, session bool) {
			t.Helper()
			if session && what == "without an action" {
				if r.Status != 404 {
					t.Errorf("%s %s, %s: %d %s, want 404", name, what, op.ID, r.Status, short(r))
				}
				return
			}
			if !apitest.Refused(r) {
				t.Errorf("%s %s, %s (%s): %d %s, want 403 forbidden", name, what, op.ID, op.Permission, r.Status, short(r))
			}
		}
		_, isSession := who.(*apitest.Browser)
		for _, op := range ops {
			refused("without an action", apitest.Probe(t, who, op, p), op, isSession)
		}
		w.open(t, who, apitest.Reason("all eleven"), authz.SupportPermissions(), nil)
		for _, op := range ops {
			refused("with an action that holds all eleven permissions", apitest.Probe(t, who, op, p), op, false)
		}
	}
	// No other path: a new password for a platform user that is a member of the tenant would be a
	// session of the member, with its permissions; it is refused with or without an action.
	member := w.d.PlatformUser(t, apitest.UniqueName("member"), nil)
	w.d.Member(t, w.tid, member.ID, []string{"owner"})
	for name, who := range map[string]apitest.Doer{"a platform key": w.key, "a platform user": w.user} {
		r := who.Do(t, "POST", "/v1/users/"+member.ID+"/reset-password", map[string]any{"reason": apitest.Reason("reset")}, nil)
		if r.Status != 403 || r.Problem() != "escalation_refused" {
			t.Errorf("%s resetting the password of a member of the tenant: %d %s, want 403 escalation_refused", name, r.Status, short(r))
		}
	}
	// Opening an action that names one of them is 400.
	for perm := range forbidden {
		invalidOn(t, "an action that names "+perm, w.key.Do(t, "POST", w.sp(""), map[string]any{"reason": apitest.Reason("x"), "permissions": []string{perm}}, nil), "permissions")
	}

	// Nothing of the platform's own operations returns a usage record, a limit event or a price of the tenant.
	const (
		canaryPrincipal = "0192f0f0-1111-7000-8000-00000000c0de"
		canaryKey       = "tcanarykey0000000"
		canaryCost      = "9876.543210"
		canaryRate      = "4321.5678"
	)
	w.d.SeedModel(t, "openai", "usage-canary")
	w.d.SeedUsage(t, w.tid, dbtest.UsageRow{StartedAt: time.Now().Add(-time.Minute), PrincipalID: canaryPrincipal, KeyID: canaryKey, Provider: "openai", Model: "usage-canary",
		InputTokens: 7654321, Cost: canaryCost, Currency: "EUR"})
	w.d.SeedTenantPrice(t, w.tid, "EUR", "openai", "usage-canary", time.Time{}, dbtest.Rate{Category: "input", Rate: canaryRate})
	w.d.SeedLimitRule(t, w.tid, "tenant", "", "requests_per_day", 7777777)
	canaries := []string{canaryPrincipal, canaryKey, canaryCost, canaryRate, "7654321", "7777777", "usage-canary"}
	checked := 0
	for _, op := range adminapi.Operations {
		perm, ok := authz.Lookup(op.Permission)
		if !ok || perm.Scope != authz.ScopePlatform || op.Method != "GET" {
			continue
		}
		pp := p
		pp.Model = apitest.NoUser
		for name, who := range map[string]apitest.Doer{"key": w.key, "user": w.user} {
			r := apitest.Probe(t, who, op, pp)
			checked++
			for _, c := range canaries {
				if c == "usage-canary" && strings.HasPrefix(op.Permission, "catalog:") {
					continue // the model is seeded in the platform's own catalogue, which the platform lists
				}
				if strings.Contains(string(r.Body), c) {
					t.Errorf("%s as a platform %s returns %q, which is the tenant's", op.ID, name, c)
				}
			}
		}
	}
	if checked < 40 {
		t.Errorf("%d platform reads were checked", checked)
	}
	// The listing of the models of the catalogue is the platform's, and names the model the tenant uses, but never its price or its use.
	for _, path := range []string{"/v1/models?limit=200", "/v1/tenants"} {
		if r := w.key.Do(t, "GET", path, nil, nil); r.Status == 200 {
			for _, c := range []string{canaryCost, canaryRate, canaryPrincipal} {
				if strings.Contains(string(r.Body), c) {
					t.Errorf("GET %s returns %q", path, c)
				}
			}
		}
	}
}

func resourceOf(permission string) string { return permission[:strings.IndexByte(permission, ':')] }

// TestSupportAudit is T-873.
func TestSupportAudit(t *testing.T) {
	w := newSW(t)
	action := w.open(t, w.key, apitest.Reason("audit"), []string{authz.ServiceAccountRead, authz.ServiceAccountWrite, authz.MemberRead, authz.MemberWrite, authz.VirtualKeyWrite, authz.RoleWrite, authz.RoleRead}, nil)
	// A write of the tenant's own principal has no action ID.
	own := decode[adminapi.ServiceAccount](t, w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": "by-the-tenant", "roles": []string{"inference"}}, nil), 201)
	for _, r := range w.audit(t, "service_account.create") {
		if r.ResourceID == own.ID && r.SupportActionID != "" {
			t.Errorf("a write of the tenant's own principal names the action %q", r.SupportActionID)
		}
	}
	rowsNow := func() int { return len(w.d.AuditRows(t, w.tid)) }
	type write struct {
		name string
		do   func() *apitest.Response
		want string // the audit action
	}
	var madeSA string
	writes := []write{
		{"createServiceAccount", func() *apitest.Response {
			r := w.key.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": "by-support", "roles": []string{"owner"}}, nil)
			if r.Status == 201 {
				var sa adminapi.ServiceAccount
				r.JSON(t, &sa)
				madeSA = sa.ID
			}
			return r
		}, "service_account.create"},
		{"updateServiceAccount", func() *apitest.Response {
			return w.key.Do(t, "PATCH", w.tp("/service-accounts/"+madeSA), map[string]any{"description": "by support"}, nil)
		}, "service_account.update"},
		{"createVirtualKey", func() *apitest.Response {
			return w.key.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": madeSA}, nil)
		}, "virtual_key.create"},
		{"createRole", func() *apitest.Response {
			return w.key.Do(t, "POST", w.tp("/roles"), map[string]any{"name": "by-support-role", "permissions": []string{authz.UsageRead}}, nil)
		}, "role.create"},
		{"createMember", func() *apitest.Response {
			return w.key.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": apitest.UniqueName("recovered"), "roles": []string{"owner"}}, nil)
		}, "member.add"},
	}
	for _, c := range writes {
		before := rowsNow()
		if r := c.do(); r.Status/100 != 2 {
			t.Fatalf("%s under the action: %d %s", c.name, r.Status, short(r))
		}
		var found []dbtest.AuditRow
		for _, r := range w.d.AuditRows(t, w.tid)[before:] {
			if r.SupportActionID != "" || r.Action == c.want {
				found = append(found, r)
			}
		}
		matched := false
		for _, r := range found {
			if r.Action == c.want {
				matched = true
				if r.ActorType != "platform" || r.ActorID != w.keyID || r.SupportActionID != action.ID {
					t.Errorf("%s: the row names %s %s and the action %q, want the platform key %s and %s", c.name, r.ActorType, r.ActorID, r.SupportActionID, w.keyID, action.ID)
				}
			}
		}
		if !matched {
			t.Errorf("%s wrote no row %s", c.name, c.want)
		}
		for _, r := range w.d.AuditRows(t, w.tid)[before:] {
			if r.SupportActionID != action.ID {
				t.Errorf("%s: the row %s of a write under the action does not name it", c.name, r.Action)
			}
		}
	}
	// Reads write nothing.
	before := rowsNow()
	for _, path := range []string{w.tp("/service-accounts"), w.tp("/members"), w.tp("/roles"), w.tp("/virtual-keys"), w.tp("/support-actions")} {
		if r := w.key.Do(t, "GET", path, nil, nil); r.Status/100 != 2 && !apitest.Refused(r) {
			t.Errorf("GET %s: %d", path, r.Status)
		}
	}
	if n := rowsNow(); n != before {
		t.Errorf("reads wrote %d rows", n-before)
	}
	// Close: both logs, with the reason. End: the tenant's log only.
	closeReason := apitest.Reason("close")
	if r := w.key.Do(t, "POST", w.sp("/"+action.ID+"/close"), map[string]any{"reason": closeReason}, nil); r.Status != 200 {
		t.Fatalf("close: %d", r.Status)
	}
	other := w.open(t, w.key, apitest.Reason("to end"), []string{authz.MemberRead}, nil)
	endReason := apitest.Reason("end")
	if r := w.owner.Do(t, "POST", w.tp("/support-actions/"+other.ID+"/end"), map[string]any{"reason": endReason}, nil); r.Status != 200 {
		t.Fatalf("end: %d", r.Status)
	}
	count := func(rows []dbtest.AuditRow, action string) []dbtest.AuditRow {
		var out []dbtest.AuditRow
		for _, r := range rows {
			if r.Action == action {
				out = append(out, r)
			}
		}
		return out
	}
	tenantRows, platformRows := w.d.AuditRows(t, w.tid), w.d.PlatformAuditRows(t)
	if len(count(tenantRows, "support_action.open")) != 2 || len(count(platformRows, "support_action.open")) < 2 {
		t.Errorf("open: %d rows in the tenant's log and %d in the platform's", len(count(tenantRows, "support_action.open")), len(count(platformRows, "support_action.open")))
	}
	tc, pc := count(tenantRows, "support_action.close"), count(platformRows, "support_action.close")
	if len(tc) != 1 || len(pc) != 1 || tc[0].Reason != closeReason || pc[0].Reason != closeReason || tc[0].SupportActionID != action.ID {
		t.Errorf("close: %+v in the tenant's log, %+v in the platform's", tc, pc)
	}
	te := count(tenantRows, "support_action.end")
	if len(te) != 1 || te[0].Reason != endReason || te[0].ActorType != "service_account" || len(count(platformRows, "support_action.end")) != 0 {
		t.Errorf("end: %+v in the tenant's log, %d in the platform's", te, len(count(platformRows, "support_action.end")))
	}
}

// TestSupportRecovery is T-874: a tenant that lost its administrators is recovered by the platform
// under a support action, and its own log tells how.
func TestSupportRecovery(t *testing.T) {
	for _, route := range []string{"a key for the owner's account", "a member that is an owner"} {
		t.Run(route, func(t *testing.T) {
			w := newSW(t)
			// The tenant loses every key and has no member.
			keys := decode[adminapi.VirtualKeyList](t, w.owner.Do(t, "GET", w.tp("/virtual-keys?limit=200"), nil, nil), 200)
			sas := decode[adminapi.ServiceAccountList](t, w.owner.Do(t, "GET", w.tp("/service-accounts?limit=200"), nil, nil), 200)
			var ownerSA string
			for _, sa := range sas.Items {
				if reflect.DeepEqual(sa.Roles, []string{"owner"}) {
					ownerSA = sa.ID
				}
			}
			if ownerSA == "" || len(keys.Items) == 0 {
				t.Fatalf("the tenant has %d keys and the owner account %q", len(keys.Items), ownerSA)
			}
			for _, k := range keys.Items {
				if r := w.owner.Do(t, "POST", w.tp("/virtual-keys/"+k.ID+"/revoke"), map[string]any{}, nil); r.Status != 200 && r.Status != 401 {
					t.Fatalf("revoking %s: %d %s", k.ID, r.Status, r.Body)
				}
			}
			// Nobody of the tenant can administer it any more.
			if r := w.owner.Do(t, "GET", w.tp("/service-accounts"), nil, nil); r.Status != 401 {
				t.Fatalf("the revoked key of the owner: %d %s, want 401", r.Status, r.Body)
			}
			var members int
			if err := w.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.membership WHERE tenant_id = $1::uuid`, w.tenant.ID).Scan(&members); err != nil || members != 0 {
				t.Fatalf("the tenant has %d members (%v)", members, err)
			}

			// The platform acts under an action, and closes it.
			perms := []string{authz.ServiceAccountRead, authz.VirtualKeyWrite}
			if route == "a member that is an owner" {
				perms = []string{authz.MemberWrite}
			}
			action := w.open(t, w.key, apitest.Reason("the administrators left"), perms, nil)
			var newKey string
			if route == "a key for the owner's account" {
				list := decode[adminapi.ServiceAccountList](t, w.key.Do(t, "GET", w.tp("/service-accounts?limit=200"), nil, nil), 200)
				found := false
				for _, sa := range list.Items {
					found = found || sa.ID == ownerSA
				}
				if !found {
					t.Fatal("the owner's account is not listed under the action")
				}
				created := decode[adminapi.VirtualKeyCreated](t, w.key.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": ownerSA, "description": "recovery"}, nil), 201)
				newKey = created.Secret
			}
			var person, password string
			if route == "a member that is an owner" {
				person = apitest.UniqueName("recovered.owner")
				created := decode[adminapi.MemberCreated](t, w.key.Do(t, "POST", w.tp("/members"), map[string]any{"account": "new", "username": person, "roles": []string{"owner"}}, nil), 201)
				password = created.InitialPassword
			}
			if r := w.key.Do(t, "POST", w.sp("/"+action.ID+"/close"), map[string]any{"reason": "the owner has the keys again"}, nil); r.Status != 200 {
				t.Fatalf("closing: %d %s", r.Status, r.Body)
			}
			// The action is over: the platform key can no longer act in the tenant.
			if r := w.key.Do(t, "GET", w.tp("/service-accounts"), nil, nil); !apitest.Refused(r) {
				t.Errorf("the platform key after the close: %d", r.Status)
			}
			// The tenant administers itself again.
			var who apitest.Doer
			if newKey != "" {
				who = &apitest.Caller{API: w.api, Key: newKey}
			} else {
				b := w.api.NewBrowser()
				b.MustSignIn(t, person, password)
				if r := b.Do(t, "POST", "/v1/auth/password", map[string]any{"current_password": password, "new_password": dbtest.NewPassword(t)}, nil); r.Status != 200 {
					t.Fatalf("the first password change: %d %s", r.Status, r.Body)
				}
				who = b
			}
			if r := who.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": "administered-again", "roles": []string{"inference"}}, nil); r.Status != 201 {
				t.Errorf("the tenant administers itself again: %d %s", r.Status, r.Body)
			}
			// The tenant's log holds the three rows, in order.
			var seq []string
			for _, r := range w.d.AuditRows(t, w.tid) {
				if r.SupportActionID == action.ID || strings.HasPrefix(r.Action, "support_action.") {
					seq = append(seq, r.Action)
				}
			}
			want := []string{"support_action.open", "virtual_key.create", "support_action.close"}
			if route != "a key for the owner's account" {
				want = []string{"support_action.open", "user.create", "member.add", "support_action.close"}
			}
			filtered := seq[:0:0]
			for _, a := range seq {
				if a != "member.role.add" && a != "membership_role.add" {
					filtered = append(filtered, a)
				}
			}
			if !containsInOrder(filtered, want) {
				t.Errorf("the tenant's log holds %v under the action, want %v in this order", filtered, want)
			}
			opened := false
			for _, r := range w.d.AuditRows(t, w.tid) {
				// The platform created the tenant, with its first key, before the action: that row is not under it.
				opened = opened || (r.Action == "support_action.open" && r.ResourceID == action.ID)
				if opened && (r.Action == "virtual_key.create" || r.Action == "member.add") && r.ActorType == "platform" && r.SupportActionID != action.ID {
					t.Errorf("%s by the platform does not name the action", r.Action)
				}
			}
		})
	}
}

func containsInOrder(have, want []string) bool {
	i := 0
	for _, h := range have {
		if i < len(want) && h == want[i] {
			i++
		}
	}
	return i == len(want)
}
