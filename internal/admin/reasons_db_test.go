package admin_test

// The tests of reasons and of the smaller changes of the contract (sprint 4.3, sections 5.8 and
// 5.1): T-877 to T-880.

import (
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/dbtest"
)

// reasonCall is a call of one of the operations that take a reason, on resources made for it.
type reasonCall struct {
	who    apitest.Doer
	method string
	path   string
	body   func(reason any) map[string]any // reason nil leaves the member out
}

// reasonDrivers returns, for each operation that takes a required reason, a function that makes a
// call on resources of its own. The tests over the nine, and over the operations under an InTenant
// permission, use the same table.
func reasonDrivers(t *testing.T, w *sw) map[string]func() reasonCall {
	t.Helper()
	plat := w.api.PlatformCaller()
	plain := func(reason any) map[string]any {
		if reason == nil {
			return map[string]any{}
		}
		return map[string]any{"reason": reason}
	}
	return map[string]func() reasonCall{
		"suspendTenant": func() reasonCall {
			x, _ := w.api.Tenant(t, apitest.UniqueName("reason-suspend"))
			return reasonCall{plat, "POST", "/v1/tenants/" + x.ID + "/suspend", plain}
		},
		"reactivateTenant": func() reasonCall {
			x, _ := w.api.Tenant(t, apitest.UniqueName("reason-reactivate"))
			if _, err := w.api.SuspendTenant(t, w.api.PlatformKey(), x.ID); err != nil {
				t.Fatal(err)
			}
			return reasonCall{plat, "POST", "/v1/tenants/" + x.ID + "/reactivate", plain}
		},
		"requestTenantDeletion": func() reasonCall {
			x, _ := w.api.Tenant(t, apitest.UniqueName("reason-delete"))
			return reasonCall{plat, "POST", "/v1/tenants/" + x.ID + "/deletion", func(reason any) map[string]any {
				b := plain(reason)
				b["confirm_name"] = x.Name
				return b
			}}
		},
		"cancelTenantDeletion": func() reasonCall {
			x, _ := w.api.Tenant(t, apitest.UniqueName("reason-cancel"))
			id, err := tenant.ParseID(x.ID)
			if err != nil {
				t.Fatal(err)
			}
			w.d.RequestDeletion(t, id, time.Now().Add(24*time.Hour))
			return reasonCall{plat, "POST", "/v1/tenants/" + x.ID + "/deletion/cancel", plain}
		},
		"disableTenantCredential": func() reasonCall {
			c := w.api.Credential(t, w.tenant.ID, w.adminKey, "openai", apitest.UniqueName("reason-disable"))
			return reasonCall{plat, "POST", "/v1/tenant-credentials/" + w.tenant.ID + "/" + c.ID + "/disable", plain}
		},
		"revokeTenantCredential": func() reasonCall {
			c := w.api.Credential(t, w.tenant.ID, w.adminKey, "openai", apitest.UniqueName("reason-revoke"))
			return reasonCall{plat, "POST", "/v1/tenant-credentials/" + w.tenant.ID + "/" + c.ID + "/revoke", plain}
		},
		"openSupportAction": func() reasonCall {
			return reasonCall{plat, "POST", w.sp(""), func(reason any) map[string]any {
				b := plain(reason)
				b["permissions"] = []string{authz.MemberRead}
				return b
			}}
		},
		"resetUserPassword": func() reasonCall {
			u := w.d.PlatformUser(t, apitest.UniqueName("reason-user"), []string{authz.RolePlatformAuditor})
			return reasonCall{plat, "POST", "/v1/users/" + u.ID + "/reset-password", plain}
		},
		"resetMemberPassword": func() reasonCall {
			id, _ := w.member(t, authz.RoleReadOnly)
			return reasonCall{w.owner, "POST", w.tp("/members/" + id + "/reset-password"), plain}
		},
		"deactivateRole": func() reasonCall {
			r := w.role(t, apitest.UniqueName("reason-deact"), authz.MemberRead)
			return reasonCall{w.owner, "POST", w.tp("/roles/" + r.Name + "/deactivate"), plain}
		},
		"activateRole": func() reasonCall {
			r := w.role(t, apitest.UniqueName("reason-act"), authz.MemberRead)
			if resp := w.owner.Do(t, "POST", w.tp("/roles/"+r.Name+"/deactivate"), map[string]any{"reason": "setup"}, nil); resp.Status != 200 {
				t.Fatalf("deactivate: %d", resp.Status)
			}
			return reasonCall{w.owner, "POST", w.tp("/roles/" + r.Name + "/activate"), plain}
		},
		"closeSupportAction": func() reasonCall {
			a := w.open(t, plat, apitest.Reason("to close"), []string{authz.MemberRead}, nil)
			return reasonCall{plat, "POST", w.sp("/" + a.ID + "/close"), plain}
		},
	}
}

func allAuditRows(w *sw, t *testing.T) []dbtest.AuditRow {
	t.Helper()
	return append(w.d.AuditRows(t, w.tid), w.d.PlatformAuditRows(t)...)
}

// TestReasonRequired is T-877.
func TestReasonRequired(t *testing.T) {
	t.Parallel()
	w := newSW(t)
	nine := []string{"activateRole", "cancelTenantDeletion", "deactivateRole", "disableTenantCredential", "openSupportAction", "reactivateTenant",
		"requestTenantDeletion", "resetMemberPassword", "resetUserPassword", "revokeTenantCredential", "suspendTenant"}
	var got []string
	for _, op := range adminapi.Operations {
		if op.ReasonRequired {
			got = append(got, op.ID)
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, nine) {
		t.Fatalf("the operations with ReasonRequired are %v, want the eleven %v", got, nine)
	}
	drivers := reasonDrivers(t, w)
	for _, id := range nine {
		t.Run(id, func(t *testing.T) {
			c := drivers[id]()
			before := len(allAuditRows(w, t))
			for what, body := range map[string]any{
				"no body":         nil,
				"no member":       c.body(nil),
				"an empty reason": c.body(""),
				"a blank reason":  c.body("  \t\n "),
			} {
				r := c.who.Do(t, c.method, c.path, body, nil)
				if r.Status != 400 || !hasField(problemOf(t, r), "reason") && what != "no body" {
					t.Errorf("%s: %d %s, want 400 on reason", what, r.Status, short(r))
				}
				if what == "no body" && r.Status != 400 {
					t.Errorf("no body: %d, want 400", r.Status)
				}
			}
			if n := len(allAuditRows(w, t)); n != before {
				t.Errorf("a refused call wrote %d audit rows", n-before)
			}
			reason := apitest.Reason("required")
			if r := c.who.Do(t, c.method, c.path, c.body(reason), nil); r.Status/100 != 2 {
				t.Fatalf("with a reason: %d %s", r.Status, short(r))
			}
			found := false
			for _, row := range allAuditRows(w, t) {
				found = found || row.Reason == reason
			}
			if !found {
				t.Errorf("no audit row written by the call carries the reason")
			}
		})
	}

	t.Run("the operations with an optional reason still accept none", func(t *testing.T) {
		sa, _ := w.sa(t, authz.RoleReadOnly)
		keyOf := decode[adminapi.VirtualKeyCreated](t, w.owner.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": sa}, nil), 201)
		member, _ := w.member(t, authz.RoleReadOnly)
		person := w.d.PlatformUser(t, apitest.UniqueName("optional"), []string{authz.RolePlatformAuditor})
		a := w.open(t, w.key, apitest.Reason("optional"), []string{authz.MemberRead}, nil)
		b := w.open(t, w.user, apitest.Reason("optional"), []string{authz.MemberRead}, nil)
		for what, c := range map[string]struct {
			who          apitest.Doer
			method, path string
		}{
			"disableServiceAccount": {w.owner, "POST", w.tp("/service-accounts/" + sa + "/disable")},
			"enableServiceAccount":  {w.owner, "POST", w.tp("/service-accounts/" + sa + "/enable")},
			"revokeVirtualKey":      {w.owner, "POST", w.tp("/virtual-keys/" + keyOf.Key.ID + "/revoke")},
			"disableMember":         {w.owner, "POST", w.tp("/members/" + member + "/disable")},
			"enableMember":          {w.owner, "POST", w.tp("/members/" + member + "/enable")},
			"disableUser":           {w.key, "POST", "/v1/users/" + person.ID + "/disable"},
			"enableUser":            {w.key, "POST", "/v1/users/" + person.ID + "/enable"},
			"closeSupportAction":    {w.key, "POST", w.sp("/" + a.ID + "/close")},
			"endSupportAction":      {w.owner, "POST", w.tp("/support-actions/" + b.ID + "/end")},
		} {
			for _, body := range []any{map[string]any{}} {
				if r := c.who.Do(t, c.method, c.path, body, nil); r.Status != 200 {
					t.Errorf("%s with no reason: %d %s, want 200", what, r.Status, short(r))
				}
			}
		}
		// A close and an end need no body at all.
		a2 := w.open(t, w.key, apitest.Reason("optional2"), []string{authz.MemberRead}, nil)
		if r := w.key.Do(t, "POST", w.sp("/"+a2.ID+"/close"), nil, nil); r.Status != 200 {
			t.Errorf("closeSupportAction without a body: %d %s", r.Status, short(r))
		}
		b2 := w.open(t, w.user, apitest.Reason("optional3"), []string{authz.MemberRead}, nil)
		if r := w.owner.Do(t, "POST", w.tp("/support-actions/"+b2.ID+"/end"), nil, nil); r.Status != 200 {
			t.Errorf("endSupportAction without a body: %d %s", r.Status, short(r))
		}
	})

	t.Run("the four limit-rule operations record a reason when one is given", func(t *testing.T) {
		reason := apitest.Reason("limit create")
		rule := decode[adminapi.LimitRule](t, w.owner.Do(t, "POST", w.tp("/limits"), map[string]any{"metric": "requests_per_day", "limit": 1000, "reason": reason}, nil), 201)
		reasonUpdate := apitest.Reason("limit update")
		if r := w.owner.Do(t, "PATCH", w.tp("/limits/"+rule.ID), map[string]any{"limit": 2000, "reason": reasonUpdate}, nil); r.Status != 200 {
			t.Fatalf("updateLimit: %d %s", r.Status, short(r))
		}
		var tenantReasons []string
		for _, row := range w.d.AuditRows(t, w.tid) {
			tenantReasons = append(tenantReasons, row.Reason)
		}
		if !inList(tenantReasons, reason) || !inList(tenantReasons, reasonUpdate) {
			t.Errorf("the tenant's log lacks the reasons of createLimit and updateLimit: %v", tenantReasons)
		}
		if r := w.owner.Do(t, "PATCH", w.tp("/limits/"+rule.ID), map[string]any{"reason": "alone"}, nil); r.Status != 400 {
			t.Errorf("updateLimit with a reason alone: %d, want 400 as an empty update", r.Status)
		}
		// Without a reason they still work.
		if r := w.owner.Do(t, "PATCH", w.tp("/limits/"+rule.ID), map[string]any{"limit": 3000}, nil); r.Status != 200 {
			t.Errorf("updateLimit without a reason: %d", r.Status)
		}
		pReason := apitest.Reason("platform limit create")
		pRule := decode[adminapi.PlatformLimitRule](t, w.key.Do(t, "POST", "/v1/platform-limits", map[string]any{"metric": "requests_per_day", "limit": 50000, "reason": pReason}, nil), 201)
		pUpdate := apitest.Reason("platform limit update")
		if r := w.key.Do(t, "PATCH", "/v1/platform-limits/"+pRule.ID, map[string]any{"limit": 60000, "reason": pUpdate}, nil); r.Status != 200 {
			t.Fatalf("updatePlatformLimit: %d %s", r.Status, short(r))
		}
		var platformReasons []string
		for _, row := range w.d.PlatformAuditRows(t) {
			platformReasons = append(platformReasons, row.Reason)
		}
		if !inList(platformReasons, pReason) || !inList(platformReasons, pUpdate) {
			t.Errorf("the platform's log lacks the reasons of createPlatformLimit and updatePlatformLimit: %v", platformReasons)
		}
		if r := w.key.Do(t, "PATCH", "/v1/platform-limits/"+pRule.ID, map[string]any{"reason": "alone"}, nil); r.Status != 400 {
			t.Errorf("updatePlatformLimit with a reason alone: %d, want 400", r.Status)
		}
	})
}

// TestInTenantRule is T-878: every operation that is not a GET and whose permission has InTenant
// requires a reason, and writes its audit row to the tenant's log.
func TestInTenantRule(t *testing.T) {
	t.Parallel()
	w := newSW(t)
	exempt := map[string]string{
		// section 5.8: closing is tidying; its reason stays optional so that an action can be closed in a hurry.
		"closeSupportAction": "the optional reason of a close (5.8)",
	}
	drivers := reasonDrivers(t, w)
	checked := 0
	for _, op := range adminapi.Operations {
		perm, ok := authz.Lookup(op.Permission)
		if !ok || !perm.InTenant || op.Method == http.MethodGet {
			continue
		}
		checked++
		t.Run(op.ID, func(t *testing.T) {
			if _, ok := exempt[op.ID]; !ok && !op.ReasonRequired {
				t.Errorf("%s changes something in one tenant under %s and does not require a reason", op.ID, op.Permission)
			}
			drive, ok := drivers[op.ID]
			if !ok {
				t.Fatalf("%s has no driver in reasonDrivers: add one", op.ID)
			}
			c := drive()
			before := len(w.d.AuditRows(t, w.tid))
			reason := apitest.Reason("in tenant")
			if r := c.who.Do(t, c.method, c.path, c.body(reason), nil); r.Status/100 != 2 {
				t.Fatalf("%d %s", r.Status, short(r))
			}
			rows := w.d.AuditRows(t, w.tid)[before:]
			found := false
			for _, row := range rows {
				found = found || row.Reason == reason
			}
			if len(rows) == 0 || !found {
				t.Errorf("the audit row of %s is not in the tenant's log with its reason: %+v", op.ID, rows)
			}
		})
	}
	if checked < 4 {
		t.Errorf("%d operations under an InTenant permission were checked", checked)
	}
	for id := range exempt {
		found := false
		for _, op := range adminapi.Operations {
			found = found || op.ID == id
		}
		if !found {
			t.Errorf("%s is exempt and is not an operation", id)
		}
	}
}

// TestSettingSplit is T-879: the time zone and the active currency are the tenant's settings.
func TestSettingSplit(t *testing.T) {
	t.Parallel()
	w := newRW(t)
	zone := func(who apitest.Doer, method string) *apitest.Response {
		if method == "GET" {
			return who.Do(t, "GET", w.tp("/time-zone"), nil, nil)
		}
		return who.Do(t, "PUT", w.tp("/time-zone"), map[string]any{"time_zone": ""}, nil)
	}
	currency := func(who apitest.Doer, method string) *apitest.Response {
		if method == "GET" {
			return who.Do(t, "GET", w.tp("/active-currency"), nil, nil)
		}
		return who.Do(t, "PUT", w.tp("/active-currency"), map[string]any{"currency": ""}, nil)
	}
	for _, perms := range [][]string{{authz.LimitWrite}, {authz.PriceListWrite}, {authz.LimitWrite, authz.PriceListWrite, authz.LimitRead, authz.PriceListRead}} {
		who := w.api.AsRole(t, w.tenant.ID, w.api.CustomRole(t, w.tenant.ID, perms...))
		if r := zone(who, "PUT"); !apitest.Refused(r) {
			t.Errorf("setTenantTimeZone with %v: %d %s, want 403 forbidden", perms, r.Status, short(r))
		}
		if r := currency(who, "PUT"); !apitest.Refused(r) {
			t.Errorf("setActiveCurrency with %v: %d %s, want 403 forbidden", perms, r.Status, short(r))
		}
	}
	setter := w.api.AsRole(t, w.tenant.ID, w.api.CustomRole(t, w.tenant.ID, authz.TenantSettingWrite))
	if r := zone(setter, "PUT"); r.Status != 200 {
		t.Errorf("setTenantTimeZone with tenant_setting:write: %d %s", r.Status, short(r))
	}
	if r := currency(setter, "PUT"); r.Status != 200 {
		t.Errorf("setActiveCurrency with tenant_setting:write: %d %s", r.Status, short(r))
	}
	// Reading is unchanged: limit:read and price_list:read, and tenant_setting:write reads neither.
	if r := zone(setter, "GET"); !apitest.Refused(r) {
		t.Errorf("getTenantTimeZone with tenant_setting:write alone: %d", r.Status)
	}
	if r := currency(setter, "GET"); !apitest.Refused(r) {
		t.Errorf("getActiveCurrency with tenant_setting:write alone: %d", r.Status)
	}
	reader := w.api.AsRole(t, w.tenant.ID, w.api.CustomRole(t, w.tenant.ID, authz.LimitRead, authz.PriceListRead))
	if r := zone(reader, "GET"); r.Status != 200 {
		t.Errorf("getTenantTimeZone with limit:read: %d %s", r.Status, short(r))
	}
	if r := currency(reader, "GET"); r.Status != 200 {
		t.Errorf("getActiveCurrency with price_list:read: %d %s", r.Status, short(r))
	}
	// The roles that held both permissions hold the new one.
	for _, role := range []string{authz.RoleOwner, authz.RoleTenantAdmin} {
		who := w.api.AsRole(t, w.tenant.ID, role)
		if r := zone(who, "PUT"); r.Status != 200 {
			t.Errorf("setTenantTimeZone as %s: %d", role, r.Status)
		}
		if r := currency(who, "PUT"); r.Status != 200 {
			t.Errorf("setActiveCurrency as %s: %d", role, r.Status)
		}
	}
	// A key whose scope list names limit:write or price_list:write and not the new permission can no longer set them.
	sa := decode[adminapi.ServiceAccount](t, w.owner.Do(t, "POST", w.tp("/service-accounts"), map[string]any{"name": "scoped-setter", "roles": []string{"owner"}}, nil), 201)
	scoped := func(scopes ...string) *apitest.Caller {
		k := decode[adminapi.VirtualKeyCreated](t, w.owner.Do(t, "POST", w.tp("/virtual-keys"), map[string]any{"service_account_id": sa.ID, "scopes": scopes}, nil), 201)
		return &apitest.Caller{API: w.api, Key: k.Secret}
	}
	old := scoped(authz.LimitWrite, authz.PriceListWrite)
	if r := zone(old, "PUT"); !apitest.Refused(r) {
		t.Errorf("a key scoped to limit:write sets the time zone: %d", r.Status)
	}
	if r := currency(old, "PUT"); !apitest.Refused(r) {
		t.Errorf("a key scoped to price_list:write sets the active currency: %d", r.Status)
	}
	if r := zone(scoped(authz.TenantSettingWrite), "PUT"); r.Status != 200 {
		t.Errorf("a key scoped to tenant_setting:write sets the time zone: %d", r.Status)
	}
}

// TestPlatformAuditor is T-880.
func TestPlatformAuditor(t *testing.T) {
	t.Parallel()
	api := apitest.NewPeople(t, apitest.PeopleConfig{})
	acme, _ := api.Tenant(t, "auditor-tenant")
	key, user := api.PlatformAs(t, authz.RolePlatformAuditor)
	line := apitest.Golden(t)[authz.RolePlatformAuditor]
	held := setOf(line)
	p := probeParams(t, api, acme.ID)
	for name, who := range map[string]apitest.Doer{"a platform-auditor key": key, "a platform-auditor user": user} {
		reads, writes := 0, 0
		for _, op := range probeOps() {
			perm, ok := authz.Lookup(op.Permission)
			if !ok || perm.Scope != authz.ScopePlatform {
				continue
			}
			r := apitest.Probe(t, who, op, p)
			switch {
			case held[op.Permission]:
				reads++
				if op.Method != "GET" {
					t.Errorf("%s: the auditor holds %s, which %s (%s) needs, and it is not a read", name, op.Permission, op.ID, op.Method)
				}
				if apitest.Refused(r) {
					t.Errorf("%s: %s is refused: %d %s", name, op.ID, r.Status, short(r))
				}
				// A list needs no identifier: it works.
				if op.Method == "GET" && len(op.PathParams) == 0 && r.Status != 200 {
					t.Errorf("%s: %s answered %d %s, want 200", name, op.ID, r.Status, short(r))
				}
			default:
				writes++
				if !apitest.Refused(r) {
					t.Errorf("%s: %s (%s) was answered %d %s, want 403 forbidden", name, op.ID, op.Permission, r.Status, short(r))
				}
			}
		}
		if reads < 8 || writes < 25 {
			t.Errorf("%s: %d reads and %d refused operations were checked", name, reads, writes)
		}
		for _, id := range []string{"listTenantCredentials", "getTenantCredential"} {
			if r := apitest.Probe(t, who, mustOp(t, id), p); !apitest.Refused(r) {
				t.Errorf("%s: %s: %d, want 403 forbidden (the credentials of tenants are the administrator's, AZ-08)", name, id, r.Status)
			}
		}
	}
	// createPlatformKey takes the role.
	admin := api.PlatformCaller()
	created := decode[adminapi.PlatformKeyCreated](t, admin.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "inventory", "role": authz.RolePlatformAuditor}, nil), 201)
	if created.Key.Role != authz.RolePlatformAuditor {
		t.Errorf("the key made with the role: %+v", created.Key)
	}
	inventory := &apitest.Caller{API: api, Key: created.Secret}
	if me := decode[adminapi.Me](t, inventory.Do(t, "GET", "/v1/me", nil, nil), 200); !reflect.DeepEqual(me.Roles, []string{authz.RolePlatformAuditor}) || !reflect.DeepEqual(me.Permissions, line) {
		t.Errorf("the key reports %v and %v", me.Roles, me.Permissions)
	}
	if r := inventory.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "more"}, nil); !apitest.Refused(r) {
		t.Errorf("an auditor key issuing a key: %d", r.Status)
	}
	// The default is the administrator's role.
	def := decode[adminapi.PlatformKeyCreated](t, admin.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "default"}, nil), 201)
	if def.Key.Role != authz.RolePlatformAdmin {
		t.Errorf("the default role is %q, want platform-admin", def.Key.Role)
	}
	for what, role := range map[string]string{"an unknown role": "ghost", "a tenant role": authz.RoleOwner, "a tenant role that a person holds": authz.RoleTenantAdmin, "inference": authz.RoleInference} {
		invalidOn(t, what, admin.Do(t, "POST", "/v1/platform-keys", map[string]any{"description": "x", "role": role}, nil), "role")
	}
}

func inList(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
