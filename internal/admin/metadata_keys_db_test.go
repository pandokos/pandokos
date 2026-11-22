package admin_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/test/apitest"
)

// TestMetadataKeys is T-965: get, set and clear the keys a tenant accepts, each change with its audit
// row and atomic with it; refusals of the shape of the list; the permissions; a platform principal.
func TestMetadataKeys(t *testing.T) {
	w := newPW(t)
	path := "/v1/tenants/" + w.a.ID + "/metadata-keys"
	seen := seenIDs(t, w.api)

	got := decodeInto[adminapi.MetadataKeys](t, w.do(t, w.ka, "GET", path, nil), 200)
	if got.Restricted || len(got.Keys) != 0 || got.UpdatedAt != nil {
		t.Errorf("no policy: %+v", got)
	}

	set := decodeInto[adminapi.MetadataKeys](t, w.do(t, w.ka, "PUT", path, map[string]any{"keys": []string{"team", "project"}}), 200)
	sorted := func(in []string) []string { out := append([]string(nil), in...); sort.Strings(out); return out }
	if !set.Restricted || !reflect.DeepEqual(sorted(set.Keys), []string{"project", "team"}) || set.UpdatedAt == nil {
		t.Errorf("after set: %+v", set)
	}
	rows := newRows(t, w.api, seen)
	if len(rows) != 1 || rows[0].Action != "tenant.metadata_keys_set" || rows[0].Log != "tenant" || rows[0].TenantID != w.a.ID || rows[0].ResourceType != "tenant" ||
		rows[0].Outcome != "success" || rows[0].ActorType != "service_account" {
		t.Fatalf("the audit rows of a set: %+v", rows)
	}
	if keys, _ := rows[0].Detail["keys"].([]any); len(keys) != 2 {
		t.Errorf("the detail: %v, want the keys", rows[0].Detail)
	}
	if got := decodeInto[adminapi.MetadataKeys](t, w.do(t, w.ka, "GET", path, nil), 200); !got.Restricted || len(got.Keys) != 2 {
		t.Errorf("get after set: %+v", got)
	}
	// An empty list is a policy that keeps nothing, not the absence of a policy.
	empty := decodeInto[adminapi.MetadataKeys](t, w.do(t, w.ka, "PUT", path, map[string]any{"keys": []string{}}), 200)
	if !empty.Restricted || len(empty.Keys) != 0 {
		t.Errorf("an empty list: %+v", empty)
	}
	newRows(t, w.api, seen)

	cleared := decodeInto[adminapi.MetadataKeys](t, w.do(t, w.ka, "DELETE", path, nil), 200)
	if cleared.Restricted || len(cleared.Keys) != 0 {
		t.Errorf("after clear: %+v", cleared)
	}
	rows = newRows(t, w.api, seen)
	if len(rows) != 1 || rows[0].Action != "tenant.metadata_keys_clear" || rows[0].Log != "tenant" {
		t.Errorf("the audit rows of a clear: %+v", rows)
	}
	// Clearing again changes nothing and writes nothing.
	if r := w.do(t, w.ka, "DELETE", path, nil); r.Status != 200 || len(newRows(t, w.api, seen)) != 0 {
		t.Errorf("a second clear: %d, rows written", r.Status)
	}

	t.Run("refusals", func(t *testing.T) {
		var sixtyFive []string
		for i := 0; i < 65; i++ {
			sixtyFive = append(sixtyFive, "k"+strings.Repeat("a", 1)+string(rune('a'+i%26))+string(rune('a'+i/26)))
		}
		for name, keys := range map[string][]string{
			"65 keys":          sixtyFive,
			"a duplicate":      {"team", "team"},
			"an upper-case":    {"Team"},
			"33 characters":    {strings.Repeat("a", 33)},
			"an empty key":     {""},
			"a space":          {"two words"},
			"a leading hyphen": {"-team"},
		} {
			r := w.do(t, w.ka, "PUT", path, map[string]any{"keys": keys})
			p := expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
			if len(p.Errors) == 0 || p.Errors[0].Field != "keys" {
				t.Errorf("%s: %+v, want an error on keys", name, p.Errors)
			}
		}
		if r := w.do(t, w.ka, "PUT", path, map[string]any{}); r.Status != 400 {
			t.Errorf("no keys member: %d, want 400", r.Status)
		}
		var sixtyFour []string
		for i := 0; i < 64; i++ {
			sixtyFour = append(sixtyFour, "k"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		}
		if r := w.do(t, w.ka, "PUT", path, map[string]any{"keys": sixtyFour}); r.Status != 200 {
			t.Errorf("64 keys: %d %s", r.Status, r.Body)
		}
		w.do(t, w.ka, "DELETE", path, nil)
		if rows := newRows(t, w.api, seen); len(rows) != 2 {
			t.Errorf("%d audit rows after the refusals, the 64 keys and the clear, want 2: a refused request writes none", len(rows))
		}
	})

	t.Run("who reads and who writes", func(t *testing.T) {
		viewer := w.api.AsRole(t, w.a.ID, "usage-viewer")
		writer := w.api.AsRole(t, w.a.ID, w.api.CustomRole(t, w.a.ID, authz.TenantSettingWrite))
		both := w.api.AsRole(t, w.a.ID, w.api.CustomRole(t, w.a.ID, authz.TenantSettingWrite, authz.UsageRead))
		inference := w.api.AsRole(t, w.a.ID, "inference")
		body := map[string]any{"keys": []string{"team"}}
		for _, c := range []struct {
			name                string
			who                 *apitest.Caller
			read, write, remove bool
		}{
			{"usage-viewer", viewer, true, false, false},
			{"tenant_setting:write only", writer, false, true, true},
			{"both", both, true, true, true},
			{"inference", inference, false, false, false},
		} {
			for op, call := range map[string]struct {
				method string
				body   any
				want   bool
			}{"get": {"GET", nil, c.read}, "set": {"PUT", body, c.write}, "clear": {"DELETE", nil, c.remove}} {
				r := c.who.Do(t, call.method, path, call.body, nil)
				if call.want && r.Status != 200 || !call.want && !apitest.Refused(r) {
					t.Errorf("%s %s: %d %s, want allowed %v", c.name, op, r.Status, r.Body, call.want)
				}
			}
		}
	})

	t.Run("a platform principal is refused", func(t *testing.T) {
		for _, key := range []string{w.plat} {
			for _, c := range []struct {
				method string
				body   any
			}{{"GET", nil}, {"PUT", map[string]any{"keys": []string{"team"}}}, {"DELETE", nil}} {
				if r := w.do(t, key, c.method, path, c.body); !apitest.Refused(r) {
					t.Errorf("a platform key: %s %d, want 403", c.method, r.Status)
				}
			}
		}
		auditor := platformKeyOf(t, w.api, "platform-auditor")
		if r := auditor.Do(t, "GET", path, nil, nil); !apitest.Refused(r) {
			t.Errorf("a platform auditor: %d, want 403", r.Status)
		}
		// Not another tenant's.
		if r := w.do(t, w.kb, "PUT", path, map[string]any{"keys": []string{"team"}}); r.Status != 404 {
			t.Errorf("B's key on A's keys: %d, want 404", r.Status)
		}
	})

	t.Run("a change is atomic with its audit row", func(t *testing.T) {
		super := w.api.Super(t)
		for _, q := range []string{
			`CREATE FUNCTION pandokos.zz_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit is down'; END $$`,
			`CREATE TRIGGER a_fail BEFORE INSERT ON pandokos.audit_event FOR EACH ROW EXECUTE FUNCTION pandokos.zz_fail()`,
		} {
			if _, err := super.Exec(ctx(t), q); err != nil {
				t.Fatal(err)
			}
		}
		before := w.api.Snapshot(t)
		for op, c := range map[string]struct {
			method string
			body   any
		}{"setMetadataKeys": {"PUT", map[string]any{"keys": []string{"team"}}}, "clearMetadataKeys": {"DELETE", nil}} {
			if op == "clearMetadataKeys" {
				// There must be a policy to clear for the change to be attempted.
				if _, err := super.Exec(ctx(t), `INSERT INTO pandokos.metadata_key_policy (tenant_id, keys) VALUES ($1::uuid, ARRAY['team'])`, w.a.ID); err != nil {
					t.Fatal(err)
				}
				before = w.api.Snapshot(t)
			}
			r := w.do(t, w.ka, c.method, path, c.body)
			expect(t, r, 500, adminapi.ProblemCodeInternal)
			if w.api.Snapshot(t) != before {
				t.Errorf("%s: a table changed although the audit row could not be written", op)
			}
		}
	})
}
