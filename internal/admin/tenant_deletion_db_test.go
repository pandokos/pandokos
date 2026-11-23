package admin_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/auditchain"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestTenantDeletionRequest is T-996: the request, its refusals, the repeat, the hold on
// reactivation, the cancellation and the two reads.
func TestTenantDeletionRequest(t *testing.T) {
	api := apitest.New(t)
	plat := api.PlatformCaller()
	reason := apitest.Reason("close")
	deletionPath := func(id string) string { return "/v1/tenants/" + id + "/deletion" }

	t.Run("an active tenant becomes suspended and scheduled", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-active"))
		before := time.Now().Add(-2 * time.Second)
		r := plat.Do(t, "POST", deletionPath(x.ID), map[string]any{"reason": reason, "confirm_name": x.Name}, nil)
		d := decode[adminapi.TenantDeletion](t, r, 200)
		if d.TenantID != x.ID || d.State != adminapi.TenantDeletionStateScheduled || d.Step != adminapi.TenantDeletionStepBegin || d.Reason != reason ||
			d.RequestedBy.Type != "platform" || d.RequestedBy.ID == "" || d.RequestID == "" || d.Passes != 0 || len(d.KekVersions) != 0 ||
			d.BackupErasure.State != adminapi.BackupErasureStateNotApplicable || len(d.BackupErasure.PendingKekVersions) != 0 ||
			d.StartedAt != nil || d.CompletedAt != nil || d.Audit != nil || d.CompletionSeq != 0 {
			t.Errorf("the request answered %+v", d)
		}
		if d.RequestedAt.Before(before) || d.NotBefore.Before(d.RequestedAt) {
			t.Errorf("requested_at %v, not_before %v: not_before is requested_at plus the grace period", d.RequestedAt, d.NotBefore)
		}
		if got := decode[adminapi.Tenant](t, plat.Do(t, "GET", "/v1/tenants/"+x.ID, nil, nil), 200); got.State != adminapi.TenantStateSuspended {
			t.Errorf("the tenant is %s after the request, want suspended", got.State)
		}
		// The platform log has the request, with the reason, the state it came from and the time.
		var found bool
		for _, row := range api.DB.PlatformAuditRows(t) {
			if row.Action == "tenant.delete_request" && row.ResourceID == x.ID {
				found = true
				if row.Reason != reason || row.ActorType != "platform" || row.Detail["from"] != "active" || row.Detail["not_before"] == nil {
					t.Errorf("the audit row of the request: %+v", row)
				}
			}
		}
		if !found {
			t.Error("no tenant.delete_request row in the platform log")
		}
		// No answer holds the salt of the pseudonyms: the body is the deletion and nothing else.
		if strings.Contains(string(r.Body), "salt") {
			t.Errorf("the answer names a salt: %s", r.Body)
		}
	})

	t.Run("a suspended tenant is scheduled too", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-suspended"))
		if _, err := api.SuspendTenant(t, plat.Key, x.ID); err != nil {
			t.Fatal(err)
		}
		d := decode[adminapi.TenantDeletion](t, plat.Do(t, "POST", deletionPath(x.ID), map[string]any{"reason": reason, "confirm_name": x.Name}, nil), 200)
		if d.State != adminapi.TenantDeletionStateScheduled {
			t.Errorf("%+v", d)
		}
		for _, row := range api.DB.PlatformAuditRows(t) {
			if row.Action == "tenant.delete_request" && row.ResourceID == x.ID && row.Detail["from"] != "suspended" {
				t.Errorf("the request of a suspended tenant records from = %v", row.Detail["from"])
			}
		}
	})

	t.Run("a tenant that is provisioning, deleting or deleted answers 409", func(t *testing.T) {
		provisioning, deleting, deleted := apitest.UniqueName("del-prov"), apitest.UniqueName("del-ing"), apitest.UniqueName("del-ed")
		if _, err := api.DB.Super(t).Exec(ctx(t), `INSERT INTO pandokos.tenant (name) VALUES ($1)`, provisioning); err != nil {
			t.Fatal(err)
		}
		var provisioningID string
		if err := api.DB.Super(t).QueryRow(ctx(t), `SELECT id::text FROM pandokos.tenant WHERE name = $1`, provisioning).Scan(&provisioningID); err != nil {
			t.Fatal(err)
		}
		d2, _ := api.Tenant(t, deleting)
		d3, _ := api.Tenant(t, deleted)
		api.DB.BeginDeletion(t, parseTenant(t, d2.ID))
		api.DB.CompleteDeletion(t, parseTenant(t, d3.ID))
		// A tenant that is deleting has a running deletion: a repeated request is refused, not answered 200.
		for name, c := range map[string]struct{ id, name string }{"provisioning": {provisioningID, provisioning}, "deleting": {d2.ID, d2.Name}, "deleted": {d3.ID, d3.Name}} {
			rows := len(api.DB.PlatformAuditRows(t))
			r := plat.Do(t, "POST", deletionPath(c.id), map[string]any{"reason": reason, "confirm_name": c.name}, nil)
			if r.Status != 409 || r.Problem() != "invalid_state" {
				t.Errorf("a tenant that is %s: %d %s, want 409 invalid_state", name, r.Status, r.Body)
			}
			if got := len(api.DB.PlatformAuditRows(t)); got != rows {
				t.Errorf("a refused request for a tenant that is %s wrote %d audit rows", name, got-rows)
			}
		}
	})

	t.Run("a missing reason or a wrong name answers 400 on that field and writes nothing", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-bad"))
		rowsBefore := len(api.DB.PlatformAuditRows(t))
		for what, c := range map[string]struct {
			body  map[string]any
			field string
		}{
			"no reason":          {map[string]any{"confirm_name": x.Name}, "reason"},
			"an empty reason":    {map[string]any{"reason": "", "confirm_name": x.Name}, "reason"},
			"a blank reason":     {map[string]any{"reason": "   ", "confirm_name": x.Name}, "reason"},
			"no name":            {map[string]any{"reason": reason}, "confirm_name"},
			"a wrong name":       {map[string]any{"reason": reason, "confirm_name": x.Name + "-x"}, "confirm_name"},
			"the ID as the name": {map[string]any{"reason": reason, "confirm_name": x.ID}, "confirm_name"},
		} {
			r := plat.Do(t, "POST", deletionPath(x.ID), c.body, nil)
			if r.Status != 400 || r.Problem() != "invalid_request" || !hasField(problemOf(t, r), c.field) {
				t.Errorf("%s: %d %s, want 400 invalid_request on %s", what, r.Status, r.Body, c.field)
			}
		}
		if got := decode[adminapi.Tenant](t, plat.Do(t, "GET", "/v1/tenants/"+x.ID, nil, nil), 200); got.State != adminapi.TenantStateActive {
			t.Errorf("a refused request changed the tenant to %s", got.State)
		}
		if got := len(api.DB.PlatformAuditRows(t)); got != rowsBefore {
			t.Errorf("a refused request wrote %d audit rows", got-rowsBefore)
		}
		if r := plat.Do(t, "GET", deletionPath(x.ID), nil, nil); r.Status != 404 {
			t.Errorf("a refused request left a deletion: %d %s", r.Status, r.Body)
		}
	})

	t.Run("a repeated request answers the existing deletion and writes nothing", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-repeat"))
		body := map[string]any{"reason": reason, "confirm_name": x.Name}
		first := decode[adminapi.TenantDeletion](t, plat.Do(t, "POST", deletionPath(x.ID), body, nil), 200)
		rows := len(api.DB.PlatformAuditRows(t))
		second := decode[adminapi.TenantDeletion](t, plat.Do(t, "POST", deletionPath(x.ID), map[string]any{"reason": "another reason", "confirm_name": x.Name}, nil), 200)
		if first.RequestID != second.RequestID || !first.NotBefore.Equal(second.NotBefore) || second.Reason != reason {
			t.Errorf("the repeat answered %+v, the first %+v", second, first)
		}
		if got := len(api.DB.PlatformAuditRows(t)); got != rows {
			t.Errorf("the repeat wrote %d audit rows", got-rows)
		}
		n := 0
		for _, row := range api.DB.PlatformAuditRows(t) {
			if row.Action == "tenant.delete_request" && row.ResourceID == x.ID {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d tenant.delete_request rows for the tenant, want 1", n)
		}
	})

	t.Run("reactivation is held, the cancel removes the request, and then it works", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-cancel"))
		plat.Do(t, "POST", deletionPath(x.ID), map[string]any{"reason": reason, "confirm_name": x.Name}, nil)
		r := plat.Do(t, "POST", "/v1/tenants/"+x.ID+"/reactivate", map[string]any{"reason": apitest.Reason("reactivate")}, nil)
		p := problemOf(t, r)
		if r.Status != 409 || p.Code != "invalid_state" || !hasField(p, "state") || !strings.Contains(string(r.Body), "cancel it first") {
			t.Errorf("reactivation while a deletion is requested: %d %s, want 409 invalid_state with an error on state", r.Status, r.Body)
		}
		cancelReason := apitest.Reason("cancel")
		got := decode[adminapi.Tenant](t, plat.Do(t, "POST", deletionPath(x.ID)+"/cancel", map[string]any{"reason": cancelReason}, nil), 200)
		if got.State != adminapi.TenantStateSuspended {
			t.Errorf("the tenant is %s after the cancel, want suspended", got.State)
		}
		if r := plat.Do(t, "GET", deletionPath(x.ID), nil, nil); r.Status != 404 {
			t.Errorf("the deletion after the cancel: %d, want 404", r.Status)
		}
		found := false
		for _, row := range api.DB.PlatformAuditRows(t) {
			if row.Action == "tenant.delete_cancel" && row.ResourceID == x.ID && row.Reason == cancelReason && row.ActorType == "platform" {
				found = true
			}
		}
		if !found {
			t.Error("no tenant.delete_cancel row with the reason in the platform log")
		}
		if _, err := api.ReactivateTenant(t, plat.Key, x.ID); err != nil {
			t.Errorf("reactivation after the cancel: %v", err)
		}
		// Nothing to cancel: 404; a reason is still required.
		if r := plat.Do(t, "POST", deletionPath(x.ID)+"/cancel", map[string]any{"reason": cancelReason}, nil); r.Status != 404 {
			t.Errorf("a cancel with no request: %d %s, want 404", r.Status, r.Body)
		}
		if r := plat.Do(t, "POST", deletionPath(x.ID)+"/cancel", map[string]any{}, nil); r.Status != 400 {
			t.Errorf("a cancel without a reason: %d, want 400", r.Status)
		}
	})

	t.Run("the cancel locks the deletion before the tenant, as the step does", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-lock"))
		plat.Do(t, "POST", deletionPath(x.ID), map[string]any{"reason": reason, "confirm_name": x.Name}, nil)
		// The step's order: the row of the deletion, then the tenant. While a transaction holds the
		// first, a cancel waits for it holding nothing it needs next; with the reverse order the two
		// would deadlock.
		step, err := api.DB.Super(t).Begin(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = step.Rollback(ctx(t)) }()
		if _, err := step.Exec(ctx(t), `SELECT 1 FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid FOR UPDATE`, x.ID); err != nil {
			t.Fatal(err)
		}
		answered := make(chan *apitest.Response, 1)
		go func() {
			answered <- plat.Do(t, "POST", deletionPath(x.ID)+"/cancel", map[string]any{"reason": reason}, nil)
		}()
		waiting := false
		for i := 0; i < 200 && !waiting; i++ {
			time.Sleep(25 * time.Millisecond)
			waiting = api.DB.Count(t, `SELECT count(*)::int FROM pg_catalog.pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) > 0
		}
		if !waiting {
			t.Fatal("the cancel never waited for the row of the deletion")
		}
		if _, err := step.Exec(ctx(t), `SET LOCAL lock_timeout = '3s'`); err != nil {
			t.Fatal(err)
		}
		if _, err := step.Exec(ctx(t), `SELECT 1 FROM pandokos.tenant WHERE id = $1::uuid FOR NO KEY UPDATE`, x.ID); err != nil {
			t.Errorf("the step's second lock, the tenant, while the cancel waits: %v (the cancel holds the tenant)", err)
		}
		if err := step.Commit(ctx(t)); err != nil {
			t.Logf("the step's transaction: %v", err)
		}
		select {
		case r := <-answered:
			if r.Status != 200 {
				t.Errorf("the cancel after the step's transaction: %d %s, want 200", r.Status, r.Body)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the cancel did not answer")
		}
	})

	t.Run("a deletion that started cannot be cancelled", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-running"))
		api.DB.BeginDeletion(t, parseTenant(t, x.ID))
		r := plat.Do(t, "POST", deletionPath(x.ID)+"/cancel", map[string]any{"reason": reason}, nil)
		if r.Status != 409 || r.Problem() != "invalid_state" {
			t.Errorf("the cancel of a running deletion: %d %s, want 409 invalid_state", r.Status, r.Body)
		}
		if got := api.DB.Count(t, `SELECT count(*)::int FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid AND state = 'running'`, x.ID); got != 1 {
			t.Error("the running deletion was removed")
		}
	})

	t.Run("audit and backup_erasure follow the steps that ran, not the end", func(t *testing.T) {
		x, _ := api.Tenant(t, apitest.UniqueName("del-steps"))
		id := parseTenant(t, x.ID)
		get := func() adminapi.TenantDeletion {
			return decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", deletionPath(x.ID), nil, nil), 200)
		}
		api.DB.BeginDeletion(t, id) // the next step is access: neither has run
		if d := get(); d.Audit != nil || d.BackupErasure.State != adminapi.BackupErasureStateNotApplicable {
			t.Errorf("before the step secrets: audit %+v, backup_erasure %+v; want no audit and not_applicable", d.Audit, d.BackupErasure)
		}
		for _, want := range []string{"secrets", "configuration"} {
			if got := api.DB.StepDeletion(t, id, 100); got != want {
				t.Fatalf("the step answered %q, want %q", got, want)
			}
		}
		d := get()
		if d.Audit != nil || d.BackupErasure.State == adminapi.BackupErasureStateNotApplicable {
			t.Errorf("after the step secrets: audit %+v, backup_erasure %+v; want no audit and a state of the erasure", d.Audit, d.BackupErasure)
		}
		for i := 0; i < 50 && d.Step != adminapi.TenantDeletionStepComplete; i++ {
			api.DB.StepDeletion(t, id, 100)
			d = get()
		}
		if d.State != adminapi.TenantDeletionStateRunning || d.Step != adminapi.TenantDeletionStepComplete {
			t.Fatalf("the deletion is %s at %s, want running at complete", d.State, d.Step)
		}
		if d.Audit == nil || d.Audit.ResealedHead == "" || d.Audit.LastSeq < 1 || d.BackupErasure.State == adminapi.BackupErasureStateNotApplicable {
			t.Errorf("after the step audit: audit %+v, backup_erasure %+v; want the audit of the re-seal", d.Audit, d.BackupErasure)
		}
	})

	t.Run("get and list", func(t *testing.T) {
		scheduled, _ := api.Tenant(t, apitest.UniqueName("del-list-a"))
		running, _ := api.Tenant(t, apitest.UniqueName("del-list-b"))
		completed, _ := api.Tenant(t, apitest.UniqueName("del-list-c"))
		none, _ := api.Tenant(t, apitest.UniqueName("del-list-none"))
		plat.Do(t, "POST", deletionPath(scheduled.ID), map[string]any{"reason": reason, "confirm_name": scheduled.Name}, nil)
		api.DB.BeginDeletion(t, parseTenant(t, running.ID))
		api.DB.CompleteDeletion(t, parseTenant(t, completed.ID))

		if r := plat.Do(t, "GET", deletionPath(none.ID), nil, nil); r.Status != 404 || r.Problem() != "not_found" {
			t.Errorf("the deletion of a tenant with none: %d %s", r.Status, r.Body)
		}
		g := decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", deletionPath(running.ID), nil, nil), 200)
		if g.State != adminapi.TenantDeletionStateRunning || g.Step != adminapi.TenantDeletionStepAccess || g.StartedAt == nil || g.RequestedBy.Type == "" {
			t.Errorf("a running deletion: %+v", g)
		}
		done := decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", deletionPath(completed.ID), nil, nil), 200)
		if done.State != adminapi.TenantDeletionStateCompleted || done.Step != adminapi.TenantDeletionStepDone || done.CompletedAt == nil || done.Audit == nil ||
			done.CompletionSeq < 1 || len(done.KekVersions) != 1 || done.Passes < 1 || len(done.Removed) < 3 {
			t.Errorf("a completed deletion: %+v", done)
		}
		if strings.Contains(string(done.Removed), "usage_record") || strings.Contains(string(done.Removed), "limit_event") {
			t.Errorf("removed holds a figure of usage: %s", done.Removed)
		}

		list := decode[adminapi.TenantDeletionList](t, plat.Do(t, "GET", "/v1/tenant-deletions", nil, nil), 200)
		var order []string
		for _, it := range list.Items {
			order = append(order, it.TenantID)
		}
		pos := map[string]int{}
		for i, id := range order {
			pos[id] = i
		}
		if len(order) < 3 || pos[completed.ID] > pos[running.ID] || pos[running.ID] > pos[scheduled.ID] {
			t.Errorf("the list is %v, want the newest request first (the completed one was requested last)", order)
		}
		for state, want := range map[adminapi.TenantDeletionState]string{
			adminapi.TenantDeletionStateScheduled: scheduled.ID, adminapi.TenantDeletionStateRunning: running.ID, adminapi.TenantDeletionStateCompleted: completed.ID,
		} {
			l := decode[adminapi.TenantDeletionList](t, plat.Do(t, "GET", "/v1/tenant-deletions?state="+string(state), nil, nil), 200)
			found := false
			for _, it := range l.Items {
				found = found || it.TenantID == want
				if it.State != state {
					t.Errorf("the filter %s returned a deletion that is %s", state, it.State)
				}
			}
			if !found {
				t.Errorf("the filter %s lacks the deletion of %s", state, want)
			}
		}
		// Paging: one at a time, no deletion twice, ending without a cursor.
		seen := map[string]bool{}
		path := "/v1/tenant-deletions?limit=1"
		for i := 0; i < 20; i++ {
			l := decode[adminapi.TenantDeletionList](t, plat.Do(t, "GET", path, nil, nil), 200)
			if len(l.Items) > 1 {
				t.Fatalf("a page of %d with limit 1", len(l.Items))
			}
			for _, it := range l.Items {
				if seen[it.TenantID] {
					t.Errorf("the deletion of %s is on two pages", it.TenantID)
				}
				seen[it.TenantID] = true
			}
			if l.NextCursor == "" {
				break
			}
			path = "/v1/tenant-deletions?limit=1&cursor=" + l.NextCursor
		}
		for _, id := range []string{scheduled.ID, running.ID, completed.ID} {
			if !seen[id] {
				t.Errorf("paging never showed the deletion of %s", id)
			}
		}
		// A cursor taken with one filter does not serve another.
		first := decode[adminapi.TenantDeletionList](t, plat.Do(t, "GET", "/v1/tenant-deletions?limit=1&state=completed", nil, nil), 200)
		if first.NextCursor != "" {
			if r := plat.Do(t, "GET", "/v1/tenant-deletions?limit=1&cursor="+first.NextCursor, nil, nil); r.Status != 400 {
				t.Errorf("a cursor of the filter completed used without it: %d, want 400", r.Status)
			}
		}

		// No answer holds the salt: not by name, and not its bytes in any common form.
		var salt []byte
		if err := api.DB.Super(t).QueryRow(ctx(t), `SELECT salt FROM pandokos.tenant_deletion WHERE tenant_id = $1::uuid`, running.ID).Scan(&salt); err != nil || len(salt) != 32 {
			t.Fatalf("the salt of the running deletion: %d bytes, %v", len(salt), err)
		}
		forms := []string{hex.EncodeToString(salt), base64.StdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(salt), base64.URLEncoding.EncodeToString(salt), "salt"}
		for _, path := range []string{deletionPath(running.ID), "/v1/tenant-deletions", "/v1/tenant-deletions?state=running"} {
			body := string(plat.Do(t, "GET", path, nil, nil).Body)
			for _, f := range forms {
				if strings.Contains(body, f) {
					t.Errorf("%s holds %q", path, f)
				}
			}
		}
	})
}

// TestLifecycleRoutes is T-997: who may call the seven operations of sprint 6.3, and what every
// tenant role gets.
func TestLifecycleRoutes(t *testing.T) {
	api := apitest.NewPeople(t, apitest.PeopleConfig{}) // platform users and tenant members sign in
	a, _ := api.Tenant(t, apitest.UniqueName("route-a"))
	b, _ := api.Tenant(t, apitest.UniqueName("route-b"))
	ops := map[string]bool{"requestTenantDeletion": true, "cancelTenantDeletion": true, "getTenantDeletion": true, "listTenantDeletions": true,
		"getDeletedTenantAuditHead": true, "exportDeletedTenantAuditEvents": true, "listKEKVersions": true}
	var chosen []adminapi.Operation
	for _, op := range adminapi.Operations {
		if ops[op.ID] {
			chosen = append(chosen, op)
		}
	}
	if len(chosen) != 7 {
		t.Fatalf("%d of the seven operations are in the specification", len(chosen))
	}
	params := func(id string) apitest.Params {
		p := apitest.Params{}
		p.Tenant = id
		return p
	}

	// Platform callers: allowed exactly to the roles that hold the permission.
	admin, _ := api.PlatformAs(t, authz.RolePlatformAdmin)
	auditor, _ := api.PlatformAs(t, authz.RolePlatformAuditor)
	for _, op := range chosen {
		c := apitest.CallFor(t, op, params(a.ID))
		for name, who := range map[string]*apitest.Caller{"platform-admin": admin, "platform-auditor": auditor} {
			r := who.Do(t, c.Method, c.Path, c.Body, nil)
			holds := false
			for _, p := range authz.Roles() {
				if p.Name == name {
					for _, perm := range p.Permissions {
						holds = holds || perm == op.Permission
					}
				}
			}
			if refused := apitest.Refused(r); refused == holds {
				t.Errorf("%s as %s: status %d %s; the role holds %s: %v", op.ID, name, r.Status, r.Problem(), op.Permission, holds)
			}
		}
	}
	// tenant:delete belongs to platform-admin only.
	for _, role := range authz.Roles() {
		if role.Scope == authz.ScopePlatform {
			holds := false
			for _, perm := range role.Permissions {
				holds = holds || perm == authz.TenantDelete
			}
			if holds != (role.Name == authz.RolePlatformAdmin) {
				t.Errorf("the role %s holds tenant:delete: %v", role.Name, holds)
			}
		}
	}

	// Every built-in tenant role gets the same 403 for its own tenant, another one and an unknown one,
	// and the same again while a support action is open.
	unknown := "0192f0f0-0000-7000-8000-0000000000ff"
	for _, support := range []bool{false, true} {
		if support {
			apitest.OpenSupport(t, api.PlatformCaller(), a.ID, authz.MemberRead)
		}
		for _, role := range authz.Roles() {
			if role.Scope != authz.ScopeTenant {
				continue
			}
			caller := api.AsRole(t, a.ID, role.Name)
			for _, op := range chosen {
				var bodies []string
				for _, id := range []string{a.ID, b.ID, unknown} {
					c := apitest.CallFor(t, op, params(id))
					r := caller.Do(t, c.Method, c.Path, c.Body, nil)
					if !apitest.Refused(r) {
						t.Errorf("%s as %s on %s (support open: %v): %d %s, want 403 forbidden", op.ID, role.Name, id, support, r.Status, r.Body)
					}
					bodies = append(bodies, strings.ReplaceAll(strings.ReplaceAll(string(r.Body), a.ID, "T"), b.ID, "T"))
				}
				for _, other := range bodies[1:] {
					if !sameWithoutRequestID(bodies[0], other) {
						t.Errorf("%s as %s: the refusals differ between tenants:\n%s\n%s", op.ID, role.Name, bodies[0], other)
					}
				}
			}
		}
	}
	// No credential at all, and a bad one.
	for _, op := range chosen {
		c := apitest.CallFor(t, op, params(a.ID))
		for name, key := range map[string]string{"none": "", "garbage": "pnotakeynotakeynotake"} {
			if r := api.Do(t, c.Method, c.Path, key, c.Body, nil); r.Status != http.StatusUnauthorized {
				t.Errorf("%s with %s: %d, want 401", op.ID, name, r.Status)
			}
		}
	}
}

// sameWithoutRequestID compares two problem bodies but for the request ID.
func sameWithoutRequestID(a, b string) bool {
	strip := func(s string) string {
		i := strings.Index(s, `"request_id":"`)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i+len(`"request_id":"`):], `"`)
		return s[:i] + s[i+len(`"request_id":"`)+j+1:]
	}
	return strip(a) == strip(b)
}

// TestDeletedTenantAudit is T-998: the log kept for a deleted tenant is read through two platform
// operations, for a deleted tenant only, and verifies as an ordinary chain.
func TestDeletedTenantAudit(t *testing.T) {
	api := apitest.New(t)
	plat := api.PlatformCaller()
	head := func(id string) *apitest.Response {
		return plat.Do(t, "GET", "/v1/tenants/"+id+"/deletion/audit-head", nil, nil)
	}
	export := func(id string) *apitest.Response {
		return plat.Do(t, "GET", "/v1/tenants/"+id+"/deletion/audit-events/export", nil, nil)
	}

	x, key := api.Tenant(t, apitest.UniqueName("kept-log"))
	// Activity that writes the tenant's log: a service account and a key, then the deletion.
	if r := api.Do(t, "POST", "/v1/tenants/"+x.ID+"/service-accounts", key, map[string]any{"name": "workload", "roles": []string{"inference"}}, nil); r.Status != 201 {
		t.Fatalf("create a service account: %d %s", r.Status, r.Body)
	}
	other, _ := api.Tenant(t, apitest.UniqueName("kept-log-other"))

	// 409 until the tenant is deleted: active, requested, being deleted; 404 for a tenant that does not exist.
	for _, c := range []struct {
		what    string
		prepare func()
	}{
		{"active", func() {}},
		{"requested", func() { api.DB.RequestDeletion(t, parseTenant(t, x.ID), time.Now().Add(time.Hour)) }},
		{"deleting", func() {
			api.DB.AgeDeletion(t, parseTenant(t, x.ID))
			api.DB.StepDeletion(t, parseTenant(t, x.ID), 100)
		}},
	} {
		c.prepare()
		for name, r := range map[string]*apitest.Response{"head": head(x.ID), "export": export(x.ID)} {
			if r.Status != 409 || r.Problem() != "invalid_state" {
				t.Errorf("the %s of a tenant that is %s: %d %s, want 409 invalid_state", name, c.what, r.Status, r.Body)
			}
		}
	}
	for name, r := range map[string]*apitest.Response{"head": head("0192f0f0-0000-7000-8000-0000000000fe"), "export": export("0192f0f0-0000-7000-8000-0000000000fe")} {
		if r.Status != 404 || r.Problem() != "not_found" {
			t.Errorf("the %s of an unknown tenant: %d %s, want 404", name, r.Status, r.Body)
		}
	}
	before := api.AdminStats(t).ExportsComplete["deleted_tenant_audit"]
	api.DB.CompleteDeletion(t, parseTenant(t, x.ID))

	// The head is the resealed one, and the export is the chain from the anchor to it.
	h := decode[adminapi.AuditHead](t, head(x.ID), 200)
	deletion := decode[adminapi.TenantDeletion](t, plat.Do(t, "GET", "/v1/tenants/"+x.ID+"/deletion", nil, nil), 200)
	if deletion.Audit == nil || h.Hash != deletion.Audit.ResealedHead || h.Seq != deletion.Audit.LastSeq || h.AnchorSeq != deletion.Audit.AnchorSeq {
		t.Errorf("the head %+v is not the resealed head of the deletion %+v", h, deletion.Audit)
	}
	r := export(x.ID)
	if r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("the export: %d %s", r.Status, r.Header.Get("Content-Type"))
	}
	var rows []auditchain.Row
	var last adminapi.AuditEvent
	for _, line := range strings.Split(strings.TrimSpace(string(r.Body)), "\n") {
		var e adminapi.AuditEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("a line of the export: %v\n%s", err, line)
		}
		if e.ActorName != "" || e.Reason != "" || string(e.Detail) != "{}" || e.DetailWithheld {
			t.Errorf("seq %d keeps a name, a reason or a detail (details are empty, not withheld): %+v", e.Seq, e)
		}
		prev, _ := hex.DecodeString(e.PrevHash)
		hash, _ := hex.DecodeString(e.Hash)
		rows = append(rows, auditchain.Row{Scope: "tenant:" + x.ID, Seq: int64(e.Seq), ID: e.ID, OccurredAt: e.OccurredAt, ActorType: e.ActorType, ActorID: e.ActorID, ActorName: e.ActorName,
			KeyID: e.KeyID, Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, Outcome: e.Outcome, RequestID: e.RequestID, Reason: e.Reason,
			SupportActionID: e.SupportActionID, Detail: e.Detail, PrevHash: prev, Hash: hash})
		last = e
	}
	if len(rows) != deletion.Audit.Rows || last.Action != "tenant.delete" {
		t.Errorf("the export has %d rows ending with %s, want %d rows ending with tenant.delete", len(rows), last.Action, deletion.Audit.Rows)
	}
	hh, _ := hex.DecodeString(h.Hash)
	ah, _ := hex.DecodeString(h.AnchorHash)
	res := auditchain.Verify(func(yield func(auditchain.Row, error) bool) {
		for _, row := range rows {
			if !yield(row, nil) {
				return
			}
		}
	}, &auditchain.Head{Seq: int64(h.Seq), Hash: hh, AnchorSeq: int64(h.AnchorSeq), AnchorHash: ah}, nil)
	if len(res.Breaks) != 0 || res.Rows != int64(len(rows)) {
		t.Errorf("Verify: %+v", res)
	}
	if got := api.AdminStats(t).ExportsComplete["deleted_tenant_audit"]; got != before+1 {
		t.Errorf("M46 counts %d exports of the kind deleted_tenant_audit, want %d", got, before+1)
	}
	// Another tenant's log is not in it; the other tenant, still active, answers 409.
	if strings.Contains(string(r.Body), other.ID) {
		t.Error("the export names another tenant")
	}
	if r := head(other.ID); r.Status != 409 {
		t.Errorf("the head of a tenant that is not deleted: %d", r.Status)
	}
	// A tenant principal reads none of it, and the auditor reads it.
	if r := api.Do(t, "GET", "/v1/tenants/"+x.ID+"/deletion/audit-head", key, nil, nil); r.Status == 200 {
		t.Error("a key of the deleted tenant read its kept log")
	}
	if r := api.Do(t, "GET", "/v1/tenants/"+other.ID+"/deletion/audit-head", api.OwnerKey(t, other.ID), nil, nil); !apitest.Refused(r) {
		t.Errorf("the owner of another tenant: %d, want 403", r.Status)
	}
}
