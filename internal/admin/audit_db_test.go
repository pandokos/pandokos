package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/testutil"
)

type auditRow struct {
	Log          string // "tenant" or "platform"
	TenantID     string
	ID           string
	ActorType    string
	ActorID      string
	ActorName    string
	KeyID        string
	Action       string
	ResourceType string
	ResourceID   string
	Outcome      string
	RequestID    string
	Reason       string
	Detail       map[string]any
}

// auditRows reads both audit logs as superuser.
func auditRows(t testing.TB, api *apitest.API) []auditRow {
	t.Helper()
	rows, err := api.Super(t).Query(ctx(t), `
SELECT 'tenant', tenant_id::text, id::text, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail::text FROM pandokos.audit_event
UNION ALL
SELECT 'platform', '', id::text, actor_type, actor_id, actor_name, key_id, action, resource_type, resource_id, outcome, request_id, reason, detail::text FROM pandokos.platform_audit_event
ORDER BY 3`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var detail string
		if err := rows.Scan(&r.Log, &r.TenantID, &r.ID, &r.ActorType, &r.ActorID, &r.ActorName, &r.KeyID, &r.Action, &r.ResourceType, &r.ResourceID, &r.Outcome, &r.RequestID, &r.Reason, &detail); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(detail), &r.Detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seenIDs marks the rows that exist now.
func seenIDs(t testing.TB, api *apitest.API) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	for _, r := range auditRows(t, api) {
		m[r.ID] = true
	}
	return m
}

// newRows returns the rows that are not in seen, sorted by log and action, and marks them seen.
func newRows(t testing.TB, api *apitest.API, seen map[string]bool) []auditRow {
	t.Helper()
	var out []auditRow
	for _, r := range auditRows(t, api) {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Log != out[j].Log {
			return out[i].Log < out[j].Log
		}
		return out[i].Action < out[j].Action
	})
	return out
}

func rfc(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }

// step is one mutating operation with the audit rows it must write.
type step struct {
	op   string
	do   func() *apitest.Response
	want func(resp *apitest.Response) []auditRow
}

// fixtures is what the steps act on.
type fixtures struct {
	w        *world
	platID   string // key ID of the platform key that acts
	platName string
	admin    adminapi.Me
	frozen   adminapi.Tenant // suspended, with an administrator key
	dormant  adminapi.ServiceAccount
}

func newFixtures(t *testing.T, opts ...func(*admin.Options)) *fixtures {
	t.Helper()
	w := &world{api: apitest.New(t, opts...)}
	w.fill(t, "audited")
	f := &fixtures{w: w, platID: w.api.PlatformKey()[4:21], platName: "apitest"}
	f.admin = *me(t, w.api, w.adminKey)
	frozen, _ := w.api.Tenant(t, "frozen")
	if _, err := w.api.Client(w.platform).SuspendTenant(ctx(t), adminapi.SuspendTenantInput{Tenant: frozen.ID}); err != nil {
		t.Fatal(err)
	}
	f.frozen = frozen
	dormant, err := w.api.Client(w.adminKey).CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "dormant"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.api.Client(w.adminKey).DisableServiceAccount(ctx(t), adminapi.DisableServiceAccountInput{Tenant: w.tenant.ID, ServiceAccount: dormant.ID}); err != nil {
		t.Fatal(err)
	}
	f.dormant = *dormant
	return f
}

func (f *fixtures) platActor(r auditRow) auditRow {
	r.Log, r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Outcome = "platform", "platform", f.platID, f.platName, f.platID, "success"
	return r
}

func (f *fixtures) tenantActor(r auditRow, tenantID string) auditRow {
	r.Log, r.TenantID, r.ActorType, r.ActorID, r.ActorName, r.KeyID, r.Outcome = "tenant", tenantID, "service_account", f.admin.ID, f.admin.Name, f.admin.KeyID, "success"
	return r
}

// steps lists the twelve mutating operations, in an order in which each step finds what it needs.
func (f *fixtures) steps(t *testing.T) []step {
	t.Helper()
	w := f.w
	tid := w.tenant.ID
	exp := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	from := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	until := from.Add(24 * time.Hour)
	var newSA adminapi.ServiceAccount
	var newKey adminapi.VirtualKeyCreated
	var newPlat adminapi.PlatformKeyCreated
	var newTenant adminapi.TenantCreated
	decode := func(resp *apitest.Response, into any) {
		t.Helper()
		if err := json.Unmarshal(resp.Body, into); err != nil {
			t.Fatalf("%v\n%s", err, resp.Body)
		}
	}
	rid := func(r *apitest.Response) string { return r.Header.Get("X-Pandokos-Request-Id") }
	return []step{
		{"createServiceAccount",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/service-accounts", w.adminKey, map[string]any{
					"name": "audited-sa", "roles": []string{"inference"}, "valid_from": rfc(from), "valid_until": rfc(until)}, nil)
			},
			func(r *apitest.Response) []auditRow {
				decode(r, &newSA)
				return []auditRow{f.tenantActor(auditRow{Action: "service_account.create", ResourceType: "service_account", ResourceID: newSA.ID, RequestID: rid(r),
					Detail: map[string]any{"name": "audited-sa", "roles": []any{"inference"}, "valid_from": rfc(from), "valid_until": rfc(until)}}, tid)}
			}},
		{"updateServiceAccount",
			func() *apitest.Response {
				return w.api.Do(t, "PATCH", "/v1/tenants/"+tid+"/service-accounts/"+newSA.ID, w.adminKey, map[string]any{
					"description": "new text", "roles": []string{"inference", "tenant-admin"}, "valid_until": nil}, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.tenantActor(auditRow{Action: "service_account.update", ResourceType: "service_account", ResourceID: newSA.ID, RequestID: rid(r),
					Detail: map[string]any{
						"description": map[string]any{"from": "", "to": "new text"},
						"roles":       map[string]any{"from": []any{"inference"}, "to": []any{"inference", "tenant-admin"}},
						"valid_until": map[string]any{"from": rfc(until), "to": nil},
					}}, tid)}
			}},
		{"disableServiceAccount",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/service-accounts/"+newSA.ID+"/disable", w.adminKey, map[string]any{"reason": "left the team"}, nil)
			},
			func(r *apitest.Response) []auditRow {
				row := f.tenantActor(auditRow{Action: "service_account.disable", ResourceType: "service_account", ResourceID: newSA.ID, RequestID: rid(r), Reason: "left the team", Detail: map[string]any{}}, tid)
				return []auditRow{row}
			}},
		{"enableServiceAccount",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/service-accounts/"+newSA.ID+"/enable", w.adminKey, nil, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.tenantActor(auditRow{Action: "service_account.enable", ResourceType: "service_account", ResourceID: newSA.ID, RequestID: rid(r), Detail: map[string]any{}}, tid)}
			}},
		{"createVirtualKey",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/virtual-keys", w.adminKey, map[string]any{
					"service_account_id": newSA.ID, "scopes": []string{"model:invoke"}, "expires_at": rfc(exp)}, nil)
			},
			func(r *apitest.Response) []auditRow {
				decode(r, &newKey)
				return []auditRow{f.tenantActor(auditRow{Action: "virtual_key.create", ResourceType: "virtual_key", ResourceID: newKey.Key.ID, RequestID: rid(r),
					Detail: map[string]any{"service_account_id": newSA.ID, "scopes": []any{"model:invoke"}, "expires_at": rfc(exp)}}, tid)}
			}},
		{"revokeVirtualKey",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/virtual-keys/"+newKey.Key.ID+"/revoke", w.adminKey, map[string]any{"reason": "leaked"}, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.tenantActor(auditRow{Action: "virtual_key.revoke", ResourceType: "virtual_key", ResourceID: newKey.Key.ID, RequestID: rid(r), Reason: "leaked",
					Detail: map[string]any{"service_account_id": newSA.ID}}, tid)}
			}},
		{"createPlatformKey",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/platform-keys", w.platform, map[string]any{"description": "ops", "expires_at": rfc(exp)}, nil)
			},
			func(r *apitest.Response) []auditRow {
				decode(r, &newPlat)
				return []auditRow{f.platActor(auditRow{Action: "platform_key.create", ResourceType: "platform_key", ResourceID: newPlat.Key.ID, RequestID: rid(r),
					Detail: map[string]any{"description": "ops", "expires_at": rfc(exp)}})}
			}},
		{"revokePlatformKey",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/platform-keys/"+newPlat.Key.ID+"/revoke", w.platform, map[string]any{"reason": "rotated"}, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.platActor(auditRow{Action: "platform_key.revoke", ResourceType: "platform_key", ResourceID: newPlat.Key.ID, RequestID: rid(r), Reason: "rotated",
					Detail: map[string]any{"bootstrap": false}})}
			}},
		{"createTenant",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants", w.platform, map[string]any{"name": "audited-new", "display_name": "New Inc", "admin_service_account_name": "boss"}, nil)
			},
			func(r *apitest.Response) []auditRow {
				decode(r, &newTenant)
				nt := newTenant.Tenant.ID
				return []auditRow{
					f.platActor(auditRow{Action: "tenant.create", ResourceType: "tenant", ResourceID: nt, RequestID: rid(r),
						Detail: map[string]any{"name": "audited-new", "display_name": "New Inc", "admin_service_account_id": newTenant.AdminServiceAccount.ID, "admin_key_id": newTenant.AdminKey.Key.ID}}),
					// The actor of both rows in the tenant's log is the platform principal.
					func() auditRow {
						row := f.platActor(auditRow{Action: "service_account.create", ResourceType: "service_account", ResourceID: newTenant.AdminServiceAccount.ID, RequestID: rid(r),
							Detail: map[string]any{"name": "boss", "roles": []any{"tenant-admin"}}})
						row.Log, row.TenantID = "tenant", nt
						return row
					}(),
					func() auditRow {
						row := f.platActor(auditRow{Action: "virtual_key.create", ResourceType: "virtual_key", ResourceID: newTenant.AdminKey.Key.ID, RequestID: rid(r),
							Detail: map[string]any{"service_account_id": newTenant.AdminServiceAccount.ID}})
						row.Log, row.TenantID = "tenant", nt
						return row
					}(),
				}
			}},
		{"updateTenant",
			func() *apitest.Response {
				return w.api.Do(t, "PATCH", "/v1/tenants/"+tid, w.platform, map[string]any{"name": "audited-renamed", "display_name": "Audited"}, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.platActor(auditRow{Action: "tenant.update", ResourceType: "tenant", ResourceID: tid, RequestID: rid(r),
					Detail: map[string]any{"name": map[string]any{"from": "audited", "to": "audited-renamed"}, "display_name": map[string]any{"from": "", "to": "Audited"}}})}
			}},
		{"suspendTenant",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/suspend", w.platform, map[string]any{"reason": "unpaid"}, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.platActor(auditRow{Action: "tenant.suspend", ResourceType: "tenant", ResourceID: tid, RequestID: rid(r), Reason: "unpaid",
					Detail: map[string]any{"from": "active", "to": "suspended"}})}
			}},
		{"reactivateTenant",
			func() *apitest.Response {
				return w.api.Do(t, "POST", "/v1/tenants/"+tid+"/reactivate", w.platform, nil, nil)
			},
			func(r *apitest.Response) []auditRow {
				return []auditRow{f.platActor(auditRow{Action: "tenant.reactivate", ResourceType: "tenant", ResourceID: tid, RequestID: rid(r),
					Detail: map[string]any{"from": "suspended", "to": "active"}})}
			}},
	}
}

// TestEveryChangeIsAudited is T-281.
func TestEveryChangeIsAudited(t *testing.T) {
	f := newFixtures(t)
	api := f.w.api
	seen := seenIDs(t, api)
	steps := f.steps(t)

	byOp := map[string]bool{}
	for _, s := range steps {
		byOp[s.op] = true
	}
	for _, op := range adminapi.Operations {
		if op.Method != "GET" && !byOp[op.ID] {
			t.Errorf("the mutating operation %s has no audit step: add it", op.ID)
		}
	}

	for _, s := range steps {
		resp := s.do()
		if resp.Status != 200 && resp.Status != 201 {
			t.Fatalf("%s: status %d\n%s", s.op, resp.Status, resp.Body)
		}
		want := s.want(resp)
		sort.Slice(want, func(i, j int) bool {
			if want[i].Log != want[j].Log {
				return want[i].Log < want[j].Log
			}
			return want[i].Action < want[j].Action
		})
		got := newRows(t, api, seen)
		if len(got) != len(want) {
			t.Errorf("%s: %d audit rows, want %d: %+v", s.op, len(got), len(want), got)
			continue
		}
		for i := range want {
			g, w := got[i], want[i]
			g.ID = ""
			if !reflect.DeepEqual(g, w) {
				t.Errorf("%s: row %d\n got %+v\nwant %+v", s.op, i, g, w)
			}
		}
	}

	// A repeat that changes nothing writes nothing.
	repeats := []struct {
		name string
		do   func() *apitest.Response
	}{
		{"suspend", func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+f.frozen.ID+"/suspend", api.PlatformKey(), nil, nil)
		}},
		{"reactivate an active tenant", func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+f.w.tenant.ID+"/reactivate", api.PlatformKey(), nil, nil)
		}},
		{"enable an enabled account", func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+f.w.tenant.ID+"/service-accounts/"+f.w.sa.ID+"/enable", f.w.adminKey, nil, nil)
		}},
		{"disable a disabled account", func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+f.w.tenant.ID+"/service-accounts/"+f.dormant.ID+"/disable", f.w.adminKey, nil, nil)
		}},
		{"revoke a revoked key", func() *apitest.Response {
			return api.Do(t, "POST", "/v1/platform-keys/"+f.w.platformK.Key.ID+"/revoke", api.PlatformKey(), nil, nil)
		}},
		{"update to the same values", func() *apitest.Response {
			return api.Do(t, "PATCH", "/v1/tenants/"+f.w.tenant.ID, api.PlatformKey(), map[string]any{"name": "audited-renamed"}, nil)
		}},
		{"update an account to the same values", func() *apitest.Response {
			return api.Do(t, "PATCH", "/v1/tenants/"+f.w.tenant.ID+"/service-accounts/"+f.w.sa.ID, f.w.adminKey, map[string]any{"name": "worker"}, nil)
		}},
	}
	// The key that the "revoke a revoked key" repeat needs is revoked first, and that revocation
	// is itself audited: take it out of the picture.
	if resp := api.Do(t, "POST", "/v1/platform-keys/"+f.w.platformK.Key.ID+"/revoke", api.PlatformKey(), nil, nil); resp.Status != 200 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	newRows(t, api, seen)
	for _, r := range repeats {
		resp := r.do()
		if resp.Status != 200 {
			t.Errorf("%s: status %d\n%s", r.name, resp.Status, resp.Body)
		}
		if rows := newRows(t, api, seen); len(rows) != 0 {
			t.Errorf("%s wrote audit rows: %+v", r.name, rows)
		}
	}
	// Revoking a virtual key twice.
	k, err := api.Client(f.w.adminKey).CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: f.w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: f.w.sa.ID}})
	if err != nil {
		t.Fatal(err)
	}
	newRows(t, api, seen)
	for i := 0; i < 2; i++ {
		resp := api.Do(t, "POST", "/v1/tenants/"+f.w.tenant.ID+"/virtual-keys/"+k.Key.ID+"/revoke", f.w.adminKey, nil, nil)
		if resp.Status != 200 {
			t.Fatalf("%d %s", resp.Status, resp.Body)
		}
		rows := newRows(t, api, seen)
		if (i == 0) != (len(rows) == 1) || len(rows) > 1 {
			t.Errorf("revocation %d wrote %d rows", i, len(rows))
		}
	}

	// Reads write nothing: every GET operation, with a key that is allowed to make it.
	for _, op := range adminapi.Operations {
		if op.Method != "GET" {
			continue
		}
		key := f.w.adminKey
		if perm, _ := authz.Lookup(op.Permission); perm.Scope == authz.ScopePlatform {
			key = api.PlatformKey()
		}
		if resp := f.w.call(t, op, key); resp.Status != 200 {
			t.Errorf("%s: status %d\n%s", op.ID, resp.Status, resp.Body)
		}
	}
	if rows := newRows(t, api, seen); len(rows) != 0 {
		t.Errorf("a read wrote audit rows: %+v", rows)
	}
	// And nothing in the logs of the rows holds a key.
	for _, r := range auditRows(t, api) {
		testutil.AssertAbsent(t, "audit row "+r.Action, fmt.Sprintf("%+v", r), api.Secrets()...)
	}
}

// TestAuditIsAtomic is T-282: with an audit insert that fails, every mutating operation answers
// 500 and leaves every table as it was.
func TestAuditIsAtomic(t *testing.T) {
	f := newFixtures(t)
	api := f.w.api
	tid := f.w.tenant.ID
	exp := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)

	calls := map[string]func() *apitest.Response{
		"createTenant": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants", api.PlatformKey(), map[string]any{"name": "atomic-new"}, nil)
		},
		"updateTenant": func() *apitest.Response {
			return api.Do(t, "PATCH", "/v1/tenants/"+tid, api.PlatformKey(), map[string]any{"name": "atomic-renamed"}, nil)
		},
		"suspendTenant": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+tid+"/suspend", api.PlatformKey(), nil, nil)
		},
		"reactivateTenant": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+f.frozen.ID+"/reactivate", api.PlatformKey(), nil, nil)
		},
		"createServiceAccount": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+tid+"/service-accounts", f.w.adminKey, map[string]any{"name": "atomic-sa", "roles": []string{"inference"}}, nil)
		},
		"updateServiceAccount": func() *apitest.Response {
			return api.Do(t, "PATCH", "/v1/tenants/"+tid+"/service-accounts/"+f.w.sa.ID, f.w.adminKey, map[string]any{"description": "changed", "roles": []string{}}, nil)
		},
		"disableServiceAccount": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+tid+"/service-accounts/"+f.w.sa.ID+"/disable", f.w.adminKey, nil, nil)
		},
		"enableServiceAccount": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+tid+"/service-accounts/"+f.dormant.ID+"/enable", f.w.adminKey, nil, nil)
		},
		"createVirtualKey": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+tid+"/virtual-keys", f.w.adminKey, map[string]any{"service_account_id": f.w.sa.ID, "expires_at": rfc(exp)}, nil)
		},
		"revokeVirtualKey": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/tenants/"+tid+"/virtual-keys/"+f.w.key.Key.ID+"/revoke", f.w.adminKey, nil, nil)
		},
		"createPlatformKey": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/platform-keys", api.PlatformKey(), map[string]any{"description": "atomic"}, nil)
		},
		"revokePlatformKey": func() *apitest.Response {
			return api.Do(t, "POST", "/v1/platform-keys/"+f.w.platformK.Key.ID+"/revoke", api.PlatformKey(), nil, nil)
		},
	}
	for _, op := range adminapi.Operations {
		if op.Method != "GET" && calls[op.ID] == nil {
			t.Errorf("the mutating operation %s has no call here: add it", op.ID)
		}
	}

	// The failing trigger is created after the fixtures exist. Its name sorts before the
	// stamp trigger, so the failure comes first.
	super := api.Super(t)
	for _, q := range []string{
		`CREATE FUNCTION pandokos.zz_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`,
		`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
		`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.platform_audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
	} {
		if _, err := super.Exec(ctx(t), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for op, call := range calls {
		before := api.Snapshot(t)
		resp := call()
		p := expect(t, resp, 500, adminapi.ProblemCodeInternal)
		if strings.Contains(string(resp.Body), "audit is down") || p.Detail != "Internal error." {
			t.Errorf("%s: the failure leaks: %s", op, resp.Body)
		}
		if after := api.Snapshot(t); after != before {
			t.Errorf("%s: a table changed although the audit record could not be written", op)
		}
	}
	// Nothing was left half done: the same calls succeed once the trigger is gone.
	for _, q := range []string{
		`DROP TRIGGER a_fail ON pandokos.audit_event`,
		`DROP TRIGGER a_fail ON pandokos.platform_audit_event`,
	} {
		if _, err := super.Exec(ctx(t), q); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range []string{"createServiceAccount", "enableServiceAccount", "revokeVirtualKey", "createTenant"} {
		if resp := calls[op](); resp.Status != 200 && resp.Status != 201 {
			t.Errorf("%s after the trigger is gone: %d %s", op, resp.Status, resp.Body)
		}
	}
}

// TestCorrelation is T-283: the response header, the log line and the audit row of one change
// hold the same request ID.
func TestCorrelation(t *testing.T) {
	f := newFixtures(t)
	api := f.w.api
	seen := seenIDs(t, api)
	resp := api.Do(t, "POST", "/v1/tenants/"+f.w.tenant.ID+"/service-accounts", f.w.adminKey, map[string]any{"name": "correlated"}, nil)
	if resp.Status != 201 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	id := resp.Header.Get("X-Pandokos-Request-Id")
	if !reRequestID.MatchString(id) {
		t.Fatalf("request ID %q", id)
	}
	rows := newRows(t, api, seen)
	if len(rows) != 1 || rows[0].RequestID != id {
		t.Errorf("audit rows: %+v, want one with request ID %s", rows, id)
	}
	var line map[string]any
	for _, l := range api.Logs.Find(t, "admin request") {
		if l["request_id"] == id {
			line = l
		}
	}
	if line == nil {
		t.Fatalf("no admin request line with request ID %s", id)
	}
	if line["operation"] != "createServiceAccount" || line["method"] != "POST" || line["status"] != float64(201) || line["tenant_id"] != f.w.tenant.ID ||
		line["principal_type"] != "service_account" || line["principal_id"] != f.admin.ID || line["key_id"] != f.admin.KeyID {
		t.Errorf("the log line: %v", line)
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Error("the log line has no duration_ms")
	}
	if _, ok := line["error_code"]; ok {
		t.Error("a success carries error_code")
	}
	for k, v := range line {
		if s, isString := v.(string); isString && (strings.Contains(s, "/v1/") || strings.Contains(s, f.w.adminKey)) {
			t.Errorf("the log line %s holds the path or a key: %v", k, v)
		}
	}
	// A problem carries error_code in the same line.
	bad := api.Do(t, "GET", "/v1/tenants", f.w.adminKey, nil, nil)
	expect(t, bad, 403, adminapi.ProblemCodeForbidden)
	for _, l := range api.Logs.Find(t, "admin request") {
		if l["request_id"] == bad.Header.Get("X-Pandokos-Request-Id") {
			if l["error_code"] != "forbidden" || l["status"] != float64(403) || l["operation"] != "listTenants" {
				t.Errorf("the line of a refused request: %v", l)
			}
		}
	}
}

// TestAuthenticationFailuresAreAudited is T-284.
func TestAuthenticationFailuresAreAudited(t *testing.T) {
	now := time.Now()
	freeze := func(o *admin.Options) { o.Now = func() time.Time { return now } }
	t.Run("one row per kind of failure", func(t *testing.T) {
		f := newFixtures(t, freeze)
		api := f.w.api
		super := api.Super(t)
		seen := seenIDs(t, api)

		// A wrong secret for a tenant key.
		key, keyID, _, _ := authn.Generate(authn.KindTenant)
		_, _, other, _ := authn.Generate(authn.KindTenant)
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.virtual_key (tenant_id, service_account_id, key_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3, $4)`, f.w.tenant.ID, f.w.sa.ID, keyID, other[:]); err != nil {
			t.Fatal(err)
		}
		resp := api.Do(t, "GET", "/v1/me", key.Reveal(), nil, nil)
		expect(t, resp, 401, adminapi.ProblemCodeUnauthenticated)
		rows := newRows(t, api, seen)
		if len(rows) != 1 {
			t.Fatalf("%d rows for a wrong secret: %+v", len(rows), rows)
		}
		want := auditRow{Log: "tenant", TenantID: f.w.tenant.ID, ActorType: "anonymous", KeyID: keyID, Action: "auth.failure", ResourceType: "virtual_key", ResourceID: keyID,
			Outcome: "failure", RequestID: resp.Header.Get("X-Pandokos-Request-Id"), Detail: map[string]any{"reason": "wrong_secret", "listener": "admin"}}
		got := rows[0]
		got.ID = ""
		if !reflect.DeepEqual(got, want) {
			t.Errorf("tenant key:\n got %+v\nwant %+v", got, want)
		}

		// A wrong secret for a platform key, an unknown key, a malformed credential: platform log.
		pkey, pkeyID, _, _ := authn.Generate(authn.KindPlatform)
		_, _, pother, _ := authn.Generate(authn.KindPlatform)
		if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.platform_key (key_id, secret_hash) VALUES ($1, $2)`, pkeyID, pother[:]); err != nil {
			t.Fatal(err)
		}
		unknown, unknownID, _, _ := authn.Generate(authn.KindTenant)
		cases := []struct {
			name, key, resource, id, reason string
		}{
			{"platform key", pkey.Reveal(), "platform_key", pkeyID, "wrong_secret"},
			{"unknown key", unknown.Reveal(), "virtual_key", unknownID, "unknown_key"},
			{"malformed credential", "pdk_nonsense-4711", "credential", "", "malformed"},
		}
		for _, c := range cases {
			resp := api.Do(t, "GET", "/v1/me", c.key, nil, nil)
			expect(t, resp, 401, adminapi.ProblemCodeUnauthenticated)
			rows := newRows(t, api, seen)
			if len(rows) != 1 {
				t.Fatalf("%s: %d rows: %+v", c.name, len(rows), rows)
			}
			r := rows[0]
			if r.Log != "platform" || r.Action != "auth.failure" || r.ActorType != "anonymous" || r.ResourceType != c.resource || r.ResourceID != c.id || r.Outcome != "failure" ||
				r.RequestID != resp.Header.Get("X-Pandokos-Request-Id") || r.Detail["reason"] != c.reason || r.Detail["listener"] != "admin" || r.KeyID != c.id {
				t.Errorf("%s: %+v", c.name, r)
			}
		}
		// No row holds what was presented.
		var all strings.Builder
		for _, r := range auditRows(t, api) {
			all.WriteString(fmt.Sprintf("%+v\n", r))
		}
		for _, k := range []string{key.Reveal(), pkey.Reveal(), unknown.Reveal(), "pdk_nonsense-4711"} {
			testutil.AssertAbsent(t, "the audit log", all.String(), testutil.KeyForms(k)...)
		}
		if strings.Contains(all.String(), "nonsense") {
			t.Error("a malformed credential is in the audit log")
		}
		for _, l := range api.Logs.Find(t, "authentication failed") {
			if l["listener"] != "admin" || l["reason"] == nil || l["request_id"] == nil || l["audited"] != true {
				t.Errorf("the log line: %v", l)
			}
		}
		testutil.AssertAbsent(t, "the log", api.Logs.String(), testutil.KeyForms(key.Reveal())...)
	})

	t.Run("a burst is capped at 60 rows", func(t *testing.T) {
		api := apitest.New(t, freeze)
		unknown, _, _, _ := authn.Generate(authn.KindTenant)
		for i := 0; i < 100; i++ {
			r := api.Do(t, "GET", "/v1/me", unknown.Reveal(), nil, http.Header{})
			if r.Status != 401 {
				t.Fatalf("request %d: %d", i, r.Status)
			}
		}
		var n int
		if err := api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.platform_audit_event WHERE action = 'auth.failure'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 60 {
			t.Errorf("%d audit rows, want 60", n)
		}
		audited, notAudited := 0, 0
		for _, l := range api.Logs.Find(t, "authentication failed") {
			switch l["audited"] {
			case true:
				audited++
			case false:
				notAudited++
			default:
				t.Errorf("the line has no audited field: %v", l)
			}
		}
		if audited != 60 || notAudited != 40 {
			t.Errorf("log lines: %d audited, %d not audited; want 60 and 40", audited, notAudited)
		}
		// The response is the same whether the failure was recorded or not.
		r := api.Do(t, "GET", "/v1/me", unknown.Reveal(), nil, nil)
		expect(t, r, 401, adminapi.ProblemCodeUnauthenticated)
	})
}
