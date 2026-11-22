package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/audit"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/dbtest"
)

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestRecordAuthFailure is T-449: the query writes the row through the function of migration
// 0005, and the function's format check refuses what does not look like its arguments.
func TestRecordAuthFailure(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	gw := d.Open(t, store.RoleGateway)
	super := d.Super(t)
	const (
		keyID     = "tabcdefghijklmnop"
		requestID = "0123456789abcdef0123456789abcdef"
	)
	write := func(key, reason, request string) error {
		return gw.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			return audit.RecordAuthFailure(c, tx, key, reason, request)
		})
	}

	if err := write(keyID, "wrong_secret", requestID); err != nil {
		t.Fatal(err)
	}
	var action, actor, resType, resID, outcome, request, detail string
	if err := super.QueryRow(ctx(t), `SELECT action, actor_type, resource_type, resource_id, outcome, request_id, detail::text FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, a.String()).
		Scan(&action, &actor, &resType, &resID, &outcome, &request, &detail); err != nil {
		t.Fatal(err)
	}
	if action != "auth.failure" || actor != "anonymous" || resType != "virtual_key" || resID != keyID || outcome != "failure" || request != requestID {
		t.Errorf("the row: %s %s %s %s %s %s", action, actor, resType, resID, outcome, request)
	}
	if !strings.Contains(detail, `"listener": "inference"`) || !strings.Contains(detail, `"reason": "wrong_secret"`) {
		t.Errorf("detail %s", detail)
	}

	// Values that look like what must never be written are refused by the function's format check.
	for name, c := range map[string]struct{ key, reason, request string }{
		"a whole key as the key ID":   {"pdk_tabcdefghijklmnop_" + strings.Repeat("a", 52), "wrong_secret", requestID},
		"a secret as the reason":      {keyID, "pdk_" + strings.Repeat("a", 52), requestID},
		"a reason with a bearer":      {keyID, "bearer pdk_abc", requestID},
		"a prompt as the request ID":  {keyID, "wrong_secret", "synthetic prompt text of a caller"},
		"a request ID with a newline": {keyID, "wrong_secret", requestID[:31] + "\n"},
	} {
		err := write(c.key, c.reason, c.request)
		if store.SQLState(err) != "23514" {
			t.Errorf("%s: %v, want SQLSTATE 23514", name, err)
		}
	}
	var n int
	if err := super.QueryRow(ctx(t), `SELECT count(*) FROM pandokos.audit_event WHERE tenant_id = $1::uuid`, a.String()).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d rows after the refused calls, want the one of the first: %v", n, err)
	}
}

// TestSupportAction is T-884: RecordTenant writes the action of a support principal and none
// otherwise; RecordIntervention writes the same event to both logs, the platform's with the tenant
// in its detail.
func TestSupportAction(t *testing.T) {
	d := dbtest.NewMigrated(t)
	a := d.SeedTenant(t, "tenant-a")
	admin := d.Open(t, store.RoleAdmin)
	action := d.SupportAction(t, a, "platform", "pabcdefghijklmnop", "recover the tenant", time.Hour, "member:read")
	support := authz.Principal{Kind: authz.KindPlatform, ID: "pabcdefghijklmnop", Name: "ops", KeyID: "pabcdefghijklmnop", TenantID: a, TenantState: tenant.StateActive,
		SupportActionID: action, Granted: []string{"member:read"}}
	own := authz.Principal{Kind: authz.KindServiceAccount, ID: "0192f0f0-0000-7000-8000-0000000000aa", Name: "bot", KeyID: "tabcdefghijklmnop", TenantID: a, TenantState: tenant.StateActive,
		Roles: []string{authz.RoleOwner}}

	record := func(p authz.Principal, e audit.Event) {
		t.Helper()
		err := admin.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			return audit.RecordTenant(authz.WithPrincipal(c, p), tx, a, e)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	event := func(action string) audit.Event {
		return audit.Event{Action: action, ResourceType: "member", ResourceID: "m1", Outcome: "success", RequestID: "0123456789abcdef0123456789abcdef"}
	}
	byAction := func(name string) dbtest.AuditRow {
		t.Helper()
		for _, r := range d.AuditRows(t, a) {
			if r.Action == name {
				return r
			}
		}
		t.Fatalf("no audit row %s", name)
		return dbtest.AuditRow{}
	}

	t.Run("RecordTenant writes the action of a support principal", func(t *testing.T) {
		e := event("member.add")
		e.Actor = audit.ActorOf(support)
		record(support, e)
		if r := byAction("member.add"); r.SupportActionID != action || r.ActorType != "platform" || r.ActorID != "pabcdefghijklmnop" {
			t.Errorf("the row of a support principal: %+v", r)
		}
	})
	t.Run("and none for the tenant's own principals", func(t *testing.T) {
		e := event("member.update")
		e.Actor = audit.ActorOf(own)
		record(own, e)
		if r := byAction("member.update"); r.SupportActionID != "" {
			t.Errorf("the row of the tenant's own principal names the action %q", r.SupportActionID)
		}
	})
	t.Run("and none without a principal in the context", func(t *testing.T) {
		e := event("member.remove")
		e.Actor = audit.Actor{Type: audit.ActorHost, Name: "pandokos init"}
		err := admin.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error { return audit.RecordTenant(c, tx, a, e) })
		if err != nil {
			t.Fatal(err)
		}
		if r := byAction("member.remove"); r.SupportActionID != "" {
			t.Errorf("a row written without a principal names the action %q", r.SupportActionID)
		}
	})
	t.Run("an event that names the action keeps it", func(t *testing.T) {
		e := event("role.create")
		e.Actor = audit.ActorOf(own)
		e.SupportActionID = action
		record(own, e)
		if r := byAction("role.create"); r.SupportActionID != action {
			t.Errorf("an explicit action ID: %q", r.SupportActionID)
		}
	})

	t.Run("RecordIntervention writes both logs", func(t *testing.T) {
		e := audit.Event{Actor: audit.ActorOf(support), Action: "support_action.open", ResourceType: "support_action", ResourceID: action, Outcome: "success",
			RequestID: "fedcba9876543210fedcba9876543210", Reason: "recover the tenant", SupportActionID: action,
			Detail: map[string]any{"permissions": []string{"member:read"}, "replaces": ""}}
		err := admin.ActInTenant(authz.WithPrincipal(ctx(t), support), a, func(c context.Context, tx store.Tx) error {
			return audit.RecordIntervention(c, tx, a, e)
		})
		if err != nil {
			t.Fatal(err)
		}
		tenantRow := byAction("support_action.open")
		var platformRow *dbtest.AuditRow
		for _, r := range d.PlatformAuditRows(t) {
			if r.Action == "support_action.open" {
				r := r
				platformRow = &r
			}
		}
		if platformRow == nil {
			t.Fatal("the platform's log has no support_action.open")
		}
		for name, r := range map[string]dbtest.AuditRow{"tenant": tenantRow, "platform": *platformRow} {
			if r.ActorType != "platform" || r.ActorID != "pabcdefghijklmnop" || r.ResourceType != "support_action" || r.ResourceID != action ||
				r.Outcome != "success" || r.Reason != "recover the tenant" || r.RequestID != e.RequestID {
				t.Errorf("the %s row: %+v", name, r)
			}
		}
		if tenantRow.SupportActionID != action {
			t.Errorf("the tenant's row names the action %q", tenantRow.SupportActionID)
		}
		if platformRow.Detail["tenant_id"] != a.String() {
			t.Errorf("the platform's detail %v lacks the tenant ID", platformRow.Detail)
		}
		if _, ok := tenantRow.Detail["tenant_id"]; ok {
			t.Errorf("the tenant's own detail names its tenant: %v", tenantRow.Detail)
		}
		if tenantRow.Detail["permissions"] == nil || platformRow.Detail["permissions"] == nil {
			t.Errorf("the detail of the event is missing from a row: %v / %v", tenantRow.Detail, platformRow.Detail)
		}
	})
	t.Run("RecordIntervention outside ActInTenant writes nothing", func(t *testing.T) {
		before := len(d.PlatformAuditRows(t))
		err := admin.WithTenant(ctx(t), a, func(c context.Context, tx store.Tx) error {
			return audit.RecordIntervention(c, tx, a, event("support_action.close"))
		})
		if err == nil {
			t.Error("RecordIntervention succeeded in a tenant's session: the platform's row cannot be written there")
		}
		if n := len(d.PlatformAuditRows(t)); n != before {
			t.Errorf("%d platform rows, want %d", n, before)
		}
	})
}
