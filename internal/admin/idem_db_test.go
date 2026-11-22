package admin_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/fixtures"
)

// oneTime returns every string held by a member that is shown once, at any depth.
func oneTime(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && (k == "secret" || k == "initial_password" || k == "password" || k == "key") && len(s) >= 12 {
				*out = append(*out, s)
			}
			oneTime(e, out)
		}
	case []any:
		for _, e := range x {
			oneTime(e, out)
		}
	}
}

// subset reports whether every member of replay is in first with the same value.
func subset(replay, first any) bool {
	switch r := replay.(type) {
	case map[string]any:
		f, ok := first.(map[string]any)
		if !ok {
			return false
		}
		for k, e := range r {
			fe, ok := f[k]
			if !ok || !subset(e, fe) {
				return false
			}
		}
		return true
	case []any:
		f, ok := first.([]any)
		if !ok || len(f) != len(r) {
			return false
		}
		for i := range r {
			if !subset(r[i], f[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(replay, first)
}

func auditCount(w *world) int {
	tid, _ := tenant.ParseID(w.tenant.ID)
	return len(w.api.DB.PlatformAuditRows(w.api.Root())) + len(w.api.DB.AuditRows(w.api.Root(), tid))
}

// TestIdempotencyEveryCreate is T-819.
func TestIdempotencyEveryCreate(t *testing.T) {
	w := &world{api: apitest.NewPeople(t, apitest.PeopleConfig{})}
	w.fill(t, "idem")
	n := 0
	for _, op := range adminapi.Operations {
		if op.Status != 201 {
			continue
		}
		n++
		if !op.Idempotent {
			t.Errorf("%s answers 201 and does not declare the Idempotency-Key header", op.ID)
		}
		key := w.platform
		if perm, _ := authz.Lookup(op.Permission); perm.Scope == authz.ScopeTenant {
			key = w.adminKey
		}
		call := apitest.CallFor(t, op, w.params())
		hdr := http.Header{"Idempotency-Key": {"key-" + op.ID}}
		first := w.api.Do(t, call.Method, call.Path, key, call.Body, hdr)
		if first.Status != 201 {
			t.Errorf("%s: the first request answered %d %s", op.ID, first.Status, first.Body)
			continue
		}
		if first.Header.Get("Idempotency-Replayed") != "" {
			t.Errorf("%s: the first response says it is a replay", op.ID)
		}
		rowsAfterFirst := auditCount(w)
		second := w.api.Do(t, call.Method, call.Path, key, call.Body, hdr)
		if second.Status != 201 || second.Header.Get("Idempotency-Replayed") != "true" {
			t.Errorf("%s: the second request answered %d, replayed %q\n%s", op.ID, second.Status, second.Header.Get("Idempotency-Replayed"), second.Body)
			continue
		}
		var f, s any
		if json.Unmarshal(first.Body, &f) != nil || json.Unmarshal(second.Body, &s) != nil {
			t.Errorf("%s: the bodies are not JSON", op.ID)
			continue
		}
		if !subset(s, f) {
			t.Errorf("%s: the replay is not the first response without its one-time members:\n%s\n%s", op.ID, second.Body, first.Body)
		}
		var once []string
		oneTime(f, &once)
		for _, v := range once {
			if strings.Contains(string(second.Body), v) {
				t.Errorf("%s: the replay shows a member that is shown once", op.ID)
			}
		}
		if m, ok := s.(map[string]any); !ok || len(m) == 0 {
			t.Errorf("%s: an empty replay %s", op.ID, second.Body)
		}
		if got := auditCount(w); got != rowsAfterFirst {
			t.Errorf("%s: the replay wrote %d audit rows", op.ID, got-rowsAfterFirst)
		}
		// Another body with the same key is refused.
		other := w.api.Do(t, call.Method, call.Path, key, map[string]any{"different": "body"}, hdr)
		if other.Status != 422 || problem(t, other).Code != "idempotency_key_reused" {
			t.Errorf("%s: another body with the same key: %d %s", op.ID, other.Status, other.Body)
		}
	}
	if n < 20 {
		t.Errorf("only %d operations answer 201", n)
	}
	// An operation that does not answer 201 does not declare the header.
	for _, op := range adminapi.Operations {
		if op.Status != 201 && op.Idempotent {
			t.Errorf("%s declares the Idempotency-Key header and answers %d", op.ID, op.Status)
		}
	}
}

// TestIdempotencyFailures is T-820.
func TestIdempotencyFailures(t *testing.T) {
	p := newPeople(t, apitest.PeopleConfig{})
	d := p.d
	rows := func() int { return d.Count(t, "SELECT count(*) FROM pandokos.idempotency_key") }
	path := "/v1/tenants/" + p.tenant.ID + "/service-accounts"
	with := func(key string) http.Header { return http.Header{"Idempotency-Key": {key}} }
	create := func(key string, body any) *apitest.Response {
		return p.api.Do(t, "POST", path, p.adminKey, body, with(key))
	}

	// 400: nothing is kept, and the key can be used again.
	if r := create("fix-me", map[string]any{"name": "Not Valid", "roles": []string{"inference"}}); r.Status != 400 || rows() != 0 {
		t.Fatalf("a 400: %d, %d rows", r.Status, rows())
	}
	if r := create("fix-me", map[string]any{"name": "valid-name", "roles": []string{"inference"}}); r.Status != 201 || r.Header.Get("Idempotency-Replayed") != "" {
		t.Errorf("the key used again after a 400: %d %s", r.Status, r.Body)
	}
	// 404: another tenant's administrator.
	_, otherKey := p.api.Tenant(t, "globex")
	before := rows()
	if r := p.api.Do(t, "POST", path, otherKey, map[string]any{"name": "x-name", "roles": []string{"inference"}}, with("not-found")); r.Status != 404 || rows() != before {
		t.Errorf("a 404: %d, %d rows", r.Status, rows()-before)
	}
	// 409: a name that exists.
	if r := create("conflict", map[string]any{"name": "valid-name", "roles": []string{"inference"}}); r.Status != 409 || rows() != before {
		t.Errorf("a 409: %d, %d rows", r.Status, rows()-before)
	}
	if r := create("conflict", map[string]any{"name": "another-name", "roles": []string{"inference"}}); r.Status != 201 {
		t.Errorf("the key used again after a 409: %d %s", r.Status, r.Body)
	}

	// A handler that fails after its transaction committed: the commit mark stays.
	super := p.api.Super(t)
	for _, q := range []string{
		`CREATE FUNCTION pandokos.zz_no_response() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'the response cannot be stored'; END $$`,
		`CREATE TRIGGER a_no_response BEFORE UPDATE ON pandokos.idempotency_key FOR EACH ROW WHEN (NEW.completed_at IS NOT NULL AND OLD.completed_at IS NULL) EXECUTE FUNCTION pandokos.zz_no_response()`,
	} {
		if _, err := super.Exec(ctx(t), q); err != nil {
			t.Fatal(err)
		}
	}
	keys := p.d.Count(t, "SELECT count(*) FROM pandokos.platform_key")
	first := p.api.Do(t, "POST", "/v1/platform-keys", p.api.PlatformKey(), map[string]any{"description": "lost response"}, with("lost"))
	if first.Status != 201 {
		t.Fatalf("the first request: %d %s", first.Status, first.Body)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.idempotency_key WHERE key = 'lost' AND committed_at IS NOT NULL AND response_status IS NULL"); n != 1 {
		t.Error("the claim does not carry the commit mark and no response")
	}
	retry := p.api.Do(t, "POST", "/v1/platform-keys", p.api.PlatformKey(), map[string]any{"description": "lost response"}, with("lost"))
	if retry.Status != 409 || problem(t, retry).Code != "idempotency_response_lost" {
		t.Errorf("the retry: %d %s", retry.Status, retry.Body)
	}
	if n := d.Count(t, "SELECT count(*) FROM pandokos.platform_key"); n != keys+1 {
		t.Errorf("%d platform keys were created, want 1", n-keys)
	}

	// A key that is not 1 to 128 printable ASCII characters.
	for name, k := range map[string]string{
		"empty": "", "a space": "has space", "a tab": "has\ttab", "non-ASCII": "clé", "129 characters": strings.Repeat("k", 129),
	} {
		r := p.api.Do(t, "POST", "/v1/platform-keys", p.api.PlatformKey(), map[string]any{"description": "bad key"}, http.Header{"Idempotency-Key": {k}})
		if r.Status != 400 || problem(t, r).Code != "invalid_request" {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := p.api.Do(t, "POST", "/v1/platform-keys", p.api.PlatformKey(), map[string]any{"description": "longest"}, with(strings.Repeat("k", 128))); r.Status != 201 {
		t.Errorf("a key of 128 characters: %d", r.Status)
	}
	// Without the header nothing changes.
	if r := p.api.Do(t, "POST", "/v1/platform-keys", p.api.PlatformKey(), map[string]any{"description": "no header"}, nil); r.Status != 201 {
		t.Errorf("without the header: %d", r.Status)
	}
}

// TestIdempotencyStoresNoSecret is T-821.
func TestIdempotencyStoresNoSecret(t *testing.T) {
	w := &world{api: apitest.NewPeople(t, apitest.PeopleConfig{})}
	w.fill(t, "nosecret")
	canary := apitest.Canary("body")
	hdr := func(k string) http.Header { return http.Header{"Idempotency-Key": {k}} }
	var shown []string
	do := func(name, method, path, key string, body map[string]any) {
		t.Helper()
		r := w.api.Do(t, method, path, key, body, hdr("canary-"+name))
		if r.Status != 201 {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
		var v any
		_ = json.Unmarshal(r.Body, &v)
		oneTime(v, &shown)
	}
	do("virtual", "POST", "/v1/tenants/"+w.tenant.ID+"/virtual-keys", w.adminKey, map[string]any{"service_account_id": w.sa.ID, "description": canary + "-vk"})
	do("platform", "POST", "/v1/platform-keys", w.platform, map[string]any{"description": canary + "-pk"})
	do("tenant", "POST", "/v1/tenants", w.platform, map[string]any{"name": apitest.UniqueName("canary"), "display_name": canary + "-tenant"})
	do("credential", "POST", "/v1/tenants/"+w.tenant.ID+"/credentials", w.adminKey, map[string]any{"provider": "openai", "label": apitest.UniqueName("canary"), "description": canary + "-cred", "secret": fixtures.CanaryCredential})
	do("user", "POST", "/v1/users", w.platform, map[string]any{"sign_in": "local", "username": apitest.UniqueName("canary-user"), "display_name": canary + "-user"})
	do("member", "POST", "/v1/tenants/"+w.tenant.ID+"/members", w.adminKey, map[string]any{"account": "new", "username": apitest.UniqueName("canary-member"), "display_name": canary + "-member", "roles": []string{"tenant-admin"}})

	var text string
	if err := w.api.Super(t).QueryRow(ctx(t), "SELECT string_agg(to_jsonb(i)::text, ' ') FROM pandokos.idempotency_key i").Scan(&text); err != nil {
		t.Fatal(err)
	}
	if n := w.api.DB.Count(t, "SELECT count(*) FROM pandokos.idempotency_key"); n != 6 {
		t.Fatalf("%d rows, want 6", n)
	}
	if len(shown) < 5 {
		t.Fatalf("only %d one-time values were returned", len(shown))
	}
	for _, v := range append(shown, fixtures.CanaryCredential, canary) {
		if strings.Contains(text, v) {
			t.Errorf("idempotency_key holds %q", v[:min(len(v), 16)])
		}
	}
}
