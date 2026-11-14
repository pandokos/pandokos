package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

var reFP = regexp.MustCompile(`^[0-9a-f]{32}$`)

// cw is a tenant with an administrator, on an API.
type cw struct {
	api    *apitest.API
	tenant adminapi.Tenant
	admin  string
	c      *adminapi.Client
	plat   *adminapi.Client
}

func newCW(t *testing.T, name string, opts ...func(*admin.Options)) *cw {
	t.Helper()
	api := apitest.New(t, opts...)
	return newCWOn(t, api, name)
}

func newCWOn(t *testing.T, api *apitest.API, name string) *cw {
	t.Helper()
	w := &cw{api: api}
	w.tenant, w.admin = api.Tenant(t, name)
	w.c, w.plat = api.Client(w.admin), api.Client(api.PlatformKey())
	return w
}

func (w *cw) path(parts ...string) string {
	return "/v1/tenants/" + w.tenant.ID + "/credentials" + strings.Join(append([]string{""}, parts...), "/")
}

func (w *cw) do(t *testing.T, method, path, key string, body any) *apitest.Response {
	t.Helper()
	return w.api.Do(t, method, path, key, body, nil)
}

func (w *cw) create(t *testing.T, provider, label, secretText string) adminapi.Credential {
	t.Helper()
	out, err := w.c.CreateCredential(ctx(t), adminapi.CreateCredentialInput{Tenant: w.tenant.ID, Body: adminapi.CredentialCreate{Provider: provider, Label: label, Secret: secretText}})
	if err != nil {
		t.Fatalf("create %s: %v", label, err)
	}
	return *out
}

func decodeCred(t *testing.T, r *apitest.Response) adminapi.Credential {
	t.Helper()
	var c adminapi.Credential
	ok(t, r, 200, &c)
	return c
}

// credentialMembers are the members of the schema Credential: nothing else is ever returned.
var credentialMembers = map[string]bool{
	"id": true, "tenant_id": true, "provider": true, "label": true, "description": true, "owner": true, "state": true, "is_default": true,
	"fingerprint": true, "hint": true, "expires_at": true, "validated_at": true, "rotated_at": true, "state_changed_at": true,
	"state_changed_by": true, "state_reason": true, "created_at": true,
}

func assertMembers(t *testing.T, what string, body []byte) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%s: %v\n%s", what, err, body)
	}
	for k := range m {
		if !credentialMembers[k] {
			t.Errorf("%s: the response has the member %q, which is not part of the metadata", what, k)
		}
	}
}

func problemWith(t *testing.T, r *apitest.Response, status int, code adminapi.ProblemCode, field string) adminapi.Problem {
	t.Helper()
	p := expect(t, r, status, code)
	if field != "" {
		found := false
		for _, e := range p.Errors {
			found = found || e.Field == field
		}
		if !found {
			t.Errorf("the problem does not name the member %q: %+v", field, p.Errors)
		}
	}
	return p
}

// TestCredentialLifecycle is T-340.
func TestCredentialLifecycle(t *testing.T) {
	w := newCW(t, "lifecycle")
	other := newCWOn(t, w.api, "lifecycle-other")
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	const secretOne, secretTwo = fixtures.CanaryCredential, "synthetic-secret-for-the-second-credential-0002"

	t.Run("create: the first credential of a provider is its default", func(t *testing.T) {
		r := w.do(t, "POST", w.path(), w.admin, map[string]any{
			"provider": "openai", "label": "main", "secret": secretOne, "description": "the main one", "expires_at": rfc(exp)})
		var c adminapi.Credential
		ok(t, r, 201, &c)
		assertMembers(t, "create", r.Body)
		if c.Owner != adminapi.CredentialOwnerTenant || c.State != adminapi.CredentialStateActive || !c.IsDefault || c.Provider != "openai" || c.Label != "main" ||
			c.Description != "the main one" || c.TenantID != w.tenant.ID || c.StateChangedBy != adminapi.CredentialActorTenant || c.StateReason != "" {
			t.Errorf("created: %+v", c)
		}
		if !reFP.MatchString(c.Fingerprint) || c.Hint != "f365" || c.ExpiresAt == nil || !c.ExpiresAt.Equal(exp) || c.ValidatedAt != nil || c.RotatedAt != nil || c.ID == "" || c.CreatedAt.IsZero() {
			t.Errorf("created: %+v", c)
		}
		testutil.AssertAbsent(t, "the response of create", string(r.Body), testutil.SecretForms(secretOne)...)

		second := w.create(t, "openai", "second", secretTwo)
		if second.IsDefault || second.State != adminapi.CredentialStateActive || second.Hint != "0002" || second.ExpiresAt != nil {
			t.Errorf("a second credential of the provider: %+v", second)
		}
		if second.Fingerprint == c.Fingerprint {
			t.Error("two secrets have one fingerprint")
		}
		claude := w.create(t, "anthropic", "claude", "synthetic-anthropic-key-0003")
		if !claude.IsDefault {
			t.Errorf("the first credential of another provider is not its default: %+v", claude)
		}
		// A short secret has no hint.
		short := w.create(t, "anthropic", "short", "tiny-key-1234")
		if short.Hint != "" {
			t.Errorf("a secret of fewer than 16 characters has the hint %q", short.Hint)
		}
		// The same secret in another tenant has another fingerprint.
		same := other.create(t, "openai", "main", secretOne)
		if same.Fingerprint == c.Fingerprint {
			t.Error("equal secrets of two tenants have equal fingerprints: the platform could correlate them")
		}
		// The same secret twice in one tenant has one fingerprint.
		again := w.create(t, "anthropic", "again", "tiny-key-1234")
		if again.Fingerprint != short.Fingerprint {
			t.Error("equal secrets of one tenant have different fingerprints")
		}
		// The default of one tenant does not touch another's.
		if !same.IsDefault {
			t.Error("the first credential of another tenant is not its default")
		}
	})

	var main, second, claude adminapi.Credential
	t.Run("get and list", func(t *testing.T) {
		l, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Sort: adminapi.SortCreatedAt})
		if err != nil || len(l.Items) != 5 {
			t.Fatalf("list: %+v %v", l, err)
		}
		main, second, claude = l.Items[0], l.Items[1], l.Items[2]
		if main.Label != "main" || second.Label != "second" || claude.Label != "claude" {
			t.Fatalf("order: %s %s %s", main.Label, second.Label, claude.Label)
		}
		g := decodeCred(t, w.do(t, "GET", w.path(main.ID), w.admin, nil))
		if g.ID != main.ID || g.Fingerprint != main.Fingerprint || g.Hint != main.Hint {
			t.Errorf("get: %+v", g)
		}
		assertMembers(t, "get", w.do(t, "GET", w.path(main.ID), w.admin, nil).Body)
		desc, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Sort: adminapi.SortMinusCreatedAt})
		if err != nil || len(desc.Items) != 5 || desc.Items[0].Label != "again" {
			t.Errorf("descending: %+v %v", desc, err)
		}
		// Filters.
		for _, c := range []struct {
			in   adminapi.ListCredentialsInput
			want int
		}{
			{adminapi.ListCredentialsInput{Provider: "openai"}, 2},
			{adminapi.ListCredentialsInput{Provider: "anthropic"}, 3},
			{adminapi.ListCredentialsInput{State: adminapi.CredentialStateActive}, 5},
			{adminapi.ListCredentialsInput{State: adminapi.CredentialStateDisabled}, 0},
			{adminapi.ListCredentialsInput{State: adminapi.CredentialStateRevoked}, 0},
			{adminapi.ListCredentialsInput{Provider: "openai", State: adminapi.CredentialStateActive}, 2},
			{adminapi.ListCredentialsInput{Provider: "gemini"}, 0},
		} {
			c.in.Tenant = w.tenant.ID
			got, err := w.c.ListCredentials(ctx(t), c.in)
			if err != nil || len(got.Items) != c.want {
				t.Errorf("list %+v: %d items, %v; want %d", c.in, len(got.Items), err, c.want)
			}
		}
		// Pagination, both ways.
		for _, sortBy := range []adminapi.Sort{adminapi.SortCreatedAt, adminapi.SortMinusCreatedAt} {
			var ids []string
			cursor := ""
			for pages := 0; pages < 10; pages++ {
				page, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Sort: sortBy, Limit: 2, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				for _, it := range page.Items {
					ids = append(ids, it.ID)
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			sorted := append([]string(nil), ids...)
			sort.Strings(sorted)
			if sortBy == adminapi.SortMinusCreatedAt {
				for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
			if len(ids) != 5 || strings.Join(ids, ",") != strings.Join(sorted, ",") {
				t.Errorf("pagination %s: %v", sortBy, ids)
			}
		}
		// A bad filter is a 400 on the parameter.
		for _, q := range []string{"?state=paused", "?provider=NOT%20A%20PROVIDER", "?sort=label", "?limit=0", "?limit=1000", "?cursor=!!!"} {
			expect(t, w.do(t, "GET", w.path()+q, w.admin, nil), 400, adminapi.ProblemCodeInvalidRequest)
		}
		// The list of another tenant is its own.
		if l, err := other.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: other.tenant.ID}); err != nil || len(l.Items) != 1 {
			t.Errorf("the list of another tenant: %+v %v", l, err)
		}
	})

	t.Run("invalid requests", func(t *testing.T) {
		base := func() map[string]any {
			return map[string]any{"provider": "openai", "label": "fresh", "secret": "synthetic-valid-secret-0009"}
		}
		with := func(k string, v any) map[string]any { m := base(); m[k] = v; return m }
		without := func(k string) map[string]any { m := base(); delete(m, k); return m }
		for _, c := range []struct {
			name  string
			body  any
			field string
		}{
			{"an unknown provider", with("provider", "gemini"), "provider"},
			{"a provider of the wrong shape", with("provider", "Open AI"), "provider"},
			{"no provider", without("provider"), "provider"},
			{"a secret that is too short", with("secret", "short"), "secret"},
			{"no secret", without("secret"), "secret"},
			{"a secret with a space at the start", with("secret", " synthetic-valid-secret-0009"), "secret"},
			{"a secret with a space at the end", with("secret", "synthetic-valid-secret-0009 "), "secret"},
			{"a secret with a newline at the end", with("secret", "synthetic-valid-secret-0009\n"), "secret"},
			{"a secret with a control character", with("secret", "synthetic-valid\x07-secret-0009"), "secret"},
			{"a secret that is too long", with("secret", strings.Repeat("k", 8193)), "secret"},
			{"a label of the wrong shape", with("label", "Not A Label"), "label"},
			{"a label that is too long", with("label", strings.Repeat("a", 64)), "label"},
			{"no label", without("label"), "label"},
			{"a description that is too long", with("description", strings.Repeat("d", 501)), "description"},
			{"an expiry that is not a time", with("expires_at", "tomorrow"), "expires_at"},
			{"validate for a provider with no validator", with("validate", true), "validate"},
			{"validate for anthropic", func() map[string]any { m := with("validate", true); m["provider"] = "anthropic"; return m }(), "validate"},
			{"an unknown member", with("sealed", "x"), "sealed"},
			{"the tenant in the body", with("tenant_id", w.tenant.ID), "tenant_id"},
		} {
			r := w.do(t, "POST", w.path(), w.admin, c.body)
			p := problemWith(t, r, 400, adminapi.ProblemCodeInvalidRequest, "")
			names := false
			for _, e := range p.Errors {
				names = names || e.Field == c.field || strings.HasPrefix(c.field, e.Field)
			}
			if !names && c.field != "sealed" && c.field != "tenant_id" {
				t.Errorf("%s: the problem names %+v, want the member %s", c.name, p.Errors, c.field)
			}
			// A message names the member and the rule, never the value.
			testutil.AssertAbsent(t, "the problem of "+c.name, string(r.Body), "synthetic-valid-secret-0009", "synthetic-valid")
			if l, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Provider: "openai"}); err != nil || len(l.Items) != 2 {
				t.Fatalf("%s: a refused request left %d credentials of openai: %v", c.name, len(l.Items), err)
			}
		}
		// A body that is not JSON, and one with the wrong content type.
		expect(t, w.do(t, "POST", w.path(), w.admin, `{"provider":"openai","label":"x","secret":"junk`), 400, adminapi.ProblemCodeInvalidRequest)
		r := w.api.Do(t, "POST", w.path(), w.admin, `{"provider":"openai"}`, http.Header{"Content-Type": {"text/plain"}})
		expect(t, r, 415, adminapi.ProblemCodeUnsupportedMediaType)
		// The label of a credential of the provider is taken.
		p := expect(t, w.do(t, "POST", w.path(), w.admin, with("label", "main")), 409, adminapi.ProblemCodeConflict)
		testutil.AssertAbsent(t, "the conflict", fmt.Sprint(p), "synthetic-valid-secret-0009")
		// The same label of another provider is not taken.
		w.create(t, "anthropic", "main", "synthetic-valid-secret-0010")
	})

	t.Run("not found", func(t *testing.T) {
		r := w.do(t, "GET", w.path("0192f0f0-0000-7000-8000-00000000beef"), w.admin, nil)
		missing := expect(t, r, 404, adminapi.ProblemCodeNotFound)
		foreign := other.create(t, "anthropic", "foreign", "synthetic-valid-secret-0011")
		for _, c := range []struct{ method, suffix string }{
			{"GET", ""}, {"PATCH", ""}, {"DELETE", ""}, {"PUT", "/secret"}, {"POST", "/disable"}, {"POST", "/enable"}, {"POST", "/set-default"}, {"POST", "/revoke"},
		} {
			var body any
			switch c.method {
			case "PATCH":
				body = map[string]any{"description": "x"}
			case "PUT":
				body = map[string]any{"secret": "synthetic-valid-secret-0012"}
			case "POST":
				body = map[string]any{}
			}
			rm := w.do(t, c.method, w.path("0192f0f0-0000-7000-8000-00000000beef")+c.suffix, w.admin, body)
			rf := w.do(t, c.method, w.path(foreign.ID)+c.suffix, w.admin, body)
			a, b := expect(t, rm, 404, adminapi.ProblemCodeNotFound), expect(t, rf, 404, adminapi.ProblemCodeNotFound)
			a.RequestID, b.RequestID = "", ""
			if fmt.Sprint(a) != fmt.Sprint(b) {
				t.Errorf("%s %s: a credential of another tenant and a missing one differ:\n%+v\n%+v", c.method, c.suffix, a, b)
			}
		}
		_ = missing
		// Nothing of the other tenant changed.
		if g, err := other.c.GetCredential(ctx(t), adminapi.GetCredentialInput{Tenant: other.tenant.ID, Credential: foreign.ID}); err != nil || g.State != adminapi.CredentialStateActive || g.Fingerprint != foreign.Fingerprint {
			t.Errorf("the credential of the other tenant: %+v %v", g, err)
		}
	})

	t.Run("update", func(t *testing.T) {
		exp2 := exp.Add(time.Hour)
		r := w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"label": "renamed", "description": "d2", "expires_at": rfc(exp2)})
		c := decodeCred(t, r)
		if c.Label != "renamed" || c.Description != "d2" || c.ExpiresAt == nil || !c.ExpiresAt.Equal(exp2) || c.ID != second.ID || c.Fingerprint != second.Fingerprint || c.IsDefault != second.IsDefault {
			t.Errorf("update: %+v", c)
		}
		// null clears the expiry; a member that is absent is left alone.
		c = decodeCred(t, w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"expires_at": nil}))
		if c.ExpiresAt != nil || c.Label != "renamed" || c.Description != "d2" {
			t.Errorf("update with a null expiry: %+v", c)
		}
		c = decodeCred(t, w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"description": ""}))
		if c.Description != "" || c.Label != "renamed" {
			t.Errorf("update with an empty description: %+v", c)
		}
		// A label that is taken is a conflict, in the same provider only.
		expect(t, w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"label": "main"}), 409, adminapi.ProblemCodeConflict)
		decodeCred(t, w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"label": "claude"})) // anthropic has "claude"; openai does not
		decodeCred(t, w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"label": "second"}))
		// What cannot be updated is refused, the secret and the provider first of all.
		for _, body := range []map[string]any{{"secret": "synthetic-valid-secret-0013"}, {"provider": "anthropic"}, {"state": "revoked"}, {"is_default": true}, {"fingerprint": "x"}} {
			r := w.do(t, "PATCH", w.path(second.ID), w.admin, body)
			expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
		}
		expect(t, w.do(t, "PATCH", w.path(second.ID), w.admin, map[string]any{"label": "Bad Label"}), 400, adminapi.ProblemCodeInvalidRequest)
	})

	t.Run("replace the secret", func(t *testing.T) {
		const replaced = "synthetic-replacement-secret-0004"
		before := decodeCred(t, w.do(t, "GET", w.path(main.ID), w.admin, nil))
		exp3 := exp.Add(2 * time.Hour)
		r := w.do(t, "PUT", w.path(main.ID, "secret"), w.admin, map[string]any{"secret": replaced, "expires_at": rfc(exp3)})
		c := decodeCred(t, r)
		assertMembers(t, "replace", r.Body)
		if c.ID != before.ID || c.Label != before.Label || !c.IsDefault || c.Provider != before.Provider || c.State != adminapi.CredentialStateActive {
			t.Errorf("replace changed what it must keep: %+v", c)
		}
		if c.Fingerprint == before.Fingerprint || c.Hint != "0004" || c.RotatedAt == nil || c.ValidatedAt != nil || c.ExpiresAt == nil || !c.ExpiresAt.Equal(exp3) {
			t.Errorf("replace: %+v", c)
		}
		testutil.AssertAbsent(t, "the response of replace", string(r.Body), testutil.SecretForms(replaced)...)
		// The expiry is replaced by the one given, or cleared.
		c = decodeCred(t, w.do(t, "PUT", w.path(main.ID, "secret"), w.admin, map[string]any{"secret": replaced}))
		if c.ExpiresAt != nil {
			t.Errorf("a replacement without an expiry kept %v", c.ExpiresAt)
		}
		// Back to the first secret: the fingerprint is the one of the first secret again.
		c = decodeCred(t, w.do(t, "PUT", w.path(main.ID, "secret"), w.admin, map[string]any{"secret": secretOne}))
		if c.Fingerprint != before.Fingerprint || c.Hint != "f365" {
			t.Errorf("the same secret again: %+v", c)
		}
		for _, body := range []map[string]any{{}, {"secret": "short"}, {"secret": " " + replaced}, {"secret": replaced, "label": "x"}, {"secret": replaced, "validate": true}} {
			r := w.do(t, "PUT", w.path(main.ID, "secret"), w.admin, body)
			problemWith(t, r, 400, adminapi.ProblemCodeInvalidRequest, "")
			testutil.AssertAbsent(t, "the problem", string(r.Body), testutil.SecretForms(replaced)...)
		}
	})

	t.Run("replacing keeps the pins of keys", func(t *testing.T) {
		sa, err := w.c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "pinned", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, CredentialID: second.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if k.Key.CredentialID != second.ID {
			t.Fatalf("the pin of the new key: %q", k.Key.CredentialID)
		}
		decodeCred(t, w.do(t, "PUT", w.path(second.ID, "secret"), w.admin, map[string]any{"secret": "synthetic-second-secret-0005"}))
		got, err := w.c.GetVirtualKey(ctx(t), adminapi.GetVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID})
		if err != nil || got.CredentialID != second.ID {
			t.Errorf("after the replacement the key is pinned to %q, %v; want %s", got.CredentialID, err, second.ID)
		}
		// The key is usable, so the credential cannot be deleted; and it is not the default.
		p := expect(t, w.do(t, "DELETE", w.path(second.ID), w.admin, nil), 409, adminapi.ProblemCodeInvalidState)
		_ = p
		if _, err := w.c.RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disable, enable, revoke", func(t *testing.T) {
		c := decodeCred(t, w.do(t, "POST", w.path(claude.ID, "disable"), w.admin, map[string]any{"reason": "rotating"}))
		if c.State != adminapi.CredentialStateDisabled || c.StateChangedBy != adminapi.CredentialActorTenant || c.StateReason != "rotating" || !c.IsDefault {
			t.Errorf("disable: %+v (a disabled default stays the default)", c)
		}
		changed := c.StateChangedAt
		c = decodeCred(t, w.do(t, "POST", w.path(claude.ID, "disable"), w.admin, map[string]any{"reason": "again"}))
		if c.State != adminapi.CredentialStateDisabled || !c.StateChangedAt.Equal(changed) || c.StateReason != "rotating" {
			t.Errorf("disabling a disabled credential changed something: %+v", c)
		}
		c = decodeCred(t, w.do(t, "POST", w.path(claude.ID, "enable"), w.admin, nil))
		if c.State != adminapi.CredentialStateActive || c.StateReason != "" || !c.StateChangedAt.After(changed) {
			t.Errorf("enable: %+v", c)
		}
		c = decodeCred(t, w.do(t, "POST", w.path(claude.ID, "enable"), w.admin, map[string]any{}))
		if c.State != adminapi.CredentialStateActive {
			t.Errorf("enabling an active credential: %+v", c)
		}
		// Revocation destroys the secret and is final; from disabled too.
		c = decodeCred(t, w.do(t, "POST", w.path(claude.ID, "disable"), w.admin, nil))
		c = decodeCred(t, w.do(t, "POST", w.path(claude.ID, "revoke"), w.admin, map[string]any{"reason": "leaked"}))
		if c.State != adminapi.CredentialStateRevoked || c.StateReason != "leaked" || c.Fingerprint != claude.Fingerprint || !c.IsDefault {
			t.Errorf("revoke: %+v (the fingerprint stays, to tell what was revoked)", c)
		}
		var secretIsNull bool
		if err := w.api.Super(t).QueryRow(ctx(t), `SELECT secret IS NULL FROM pandokos.provider_credential WHERE id = $1::uuid`, claude.ID).Scan(&secretIsNull); err != nil || !secretIsNull {
			t.Errorf("the sealed secret of a revoked credential is still in the database: %v %v", secretIsNull, err)
		}
		again := decodeCred(t, w.do(t, "POST", w.path(claude.ID, "revoke"), w.admin, map[string]any{"reason": "again"}))
		if again.State != adminapi.CredentialStateRevoked || again.StateReason != "leaked" || !again.StateChangedAt.Equal(c.StateChangedAt) {
			t.Errorf("revoking a revoked credential changed something: %+v", again)
		}
		// Everything else is invalid_state on a revoked credential.
		for _, c := range []struct {
			method, suffix string
			body           any
		}{
			{"POST", "/disable", map[string]any{}}, {"POST", "/enable", map[string]any{}}, {"POST", "/set-default", map[string]any{}},
			{"PUT", "/secret", map[string]any{"secret": "synthetic-valid-secret-0014"}},
		} {
			p := expect(t, w.do(t, c.method, w.path(claude.ID)+c.suffix, w.admin, c.body), 409, adminapi.ProblemCodeInvalidState)
			testutil.AssertAbsent(t, "the problem", fmt.Sprint(p), "synthetic-valid-secret-0014")
		}
		if g := decodeCred(t, w.do(t, "GET", w.path(claude.ID), w.admin, nil)); g.State != adminapi.CredentialStateRevoked {
			t.Errorf("after the refused changes: %+v", g)
		}
	})

	t.Run("the default moves only when asked", func(t *testing.T) {
		fresh := w.create(t, "openai", "third", "synthetic-third-secret-0006")
		if fresh.IsDefault {
			t.Fatal("a new credential took the default")
		}
		c := decodeCred(t, w.do(t, "POST", w.path(fresh.ID, "set-default"), w.admin, nil))
		if !c.IsDefault || c.ID != fresh.ID {
			t.Errorf("set-default: %+v", c)
		}
		l, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Provider: "openai"})
		if err != nil {
			t.Fatal(err)
		}
		defaults := 0
		for _, it := range l.Items {
			if it.IsDefault {
				defaults++
				if it.ID != fresh.ID {
					t.Errorf("the default is %s, want %s", it.ID, fresh.ID)
				}
			}
		}
		if defaults != 1 {
			t.Errorf("%d defaults for openai", defaults)
		}
		// Naming the current default changes nothing.
		again := decodeCred(t, w.do(t, "POST", w.path(fresh.ID, "set-default"), w.admin, nil))
		if !again.IsDefault || again.StateChangedAt != c.StateChangedAt {
			t.Errorf("set-default of the default: %+v", again)
		}
		// Disabling the default does not move it, and a disabled credential can be made the default.
		decodeCred(t, w.do(t, "POST", w.path(fresh.ID, "disable"), w.admin, nil))
		if g := decodeCred(t, w.do(t, "GET", w.path(fresh.ID), w.admin, nil)); !g.IsDefault || g.State != adminapi.CredentialStateDisabled {
			t.Errorf("a disabled default: %+v", g)
		}
		back := decodeCred(t, w.do(t, "POST", w.path(main.ID, "set-default"), w.admin, nil))
		if !back.IsDefault {
			t.Errorf("set-default of the first: %+v", back)
		}
		if g := decodeCred(t, w.do(t, "GET", w.path(fresh.ID), w.admin, nil)); g.IsDefault {
			t.Error("two defaults after set-default")
		}
		decodeCred(t, w.do(t, "POST", w.path(fresh.ID, "enable"), w.admin, nil))
	})

	t.Run("delete", func(t *testing.T) {
		// The default is kept while another credential of the provider can be used.
		expect(t, w.do(t, "DELETE", w.path(main.ID), w.admin, nil), 409, adminapi.ProblemCodeInvalidState)
		before := decodeCred(t, w.do(t, "GET", w.path(second.ID), w.admin, nil))
		got := decodeCred(t, w.do(t, "DELETE", w.path(second.ID), w.admin, nil))
		if got.ID != before.ID || got.Label != before.Label || got.Fingerprint != before.Fingerprint || got.State != before.State {
			t.Errorf("delete returned %+v, want the metadata as it was: %+v", got, before)
		}
		expect(t, w.do(t, "GET", w.path(second.ID), w.admin, nil), 404, adminapi.ProblemCodeNotFound)
		expect(t, w.do(t, "DELETE", w.path(second.ID), w.admin, nil), 404, adminapi.ProblemCodeNotFound)
		var n int
		if err := w.api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.provider_credential WHERE id = $1::uuid`, second.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("the row of a deleted credential remains: %d %v", n, err)
		}
		// While a key that can be used is pinned to a credential, it is not deleted; once the key
		// is revoked it is, and the pin of the revoked key goes with it.
		pinme := w.create(t, "anthropic", "pinme", "synthetic-pinme-secret-0008")
		sa, err := w.c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "deleter", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, CredentialID: pinme.ID}})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, w.do(t, "DELETE", w.path(pinme.ID), w.admin, nil), 409, adminapi.ProblemCodeInvalidState)
		if _, err := w.c.RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID}); err != nil {
			t.Fatal(err)
		}
		if g := decodeCred(t, w.do(t, "DELETE", w.path(pinme.ID), w.admin, nil)); g.ID != pinme.ID {
			t.Errorf("delete of a credential whose key was revoked: %+v", g)
		}
		if key, err := w.c.GetVirtualKey(ctx(t), adminapi.GetVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID}); err != nil || key.CredentialID != "" {
			t.Errorf("the pin of a revoked key after the deletion of its credential: %q %v", key.CredentialID, err)
		}
		// A revoked default is kept too while another credential of the provider is not revoked:
		// the default has to move first.
		expect(t, w.do(t, "DELETE", w.path(claude.ID), w.admin, nil), 409, adminapi.ProblemCodeInvalidState)
		l, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Provider: "anthropic"})
		if err != nil {
			t.Fatal(err)
		}
		var short adminapi.Credential
		for _, it := range l.Items {
			if it.Label == "short" {
				short = it
			}
		}
		decodeCred(t, w.do(t, "POST", w.path(short.ID, "set-default"), w.admin, nil))
		if g := decodeCred(t, w.do(t, "DELETE", w.path(claude.ID), w.admin, nil)); g.State != adminapi.CredentialStateRevoked || g.IsDefault {
			t.Errorf("delete of a revoked credential that is no longer the default: %+v", g)
		}
	})

	t.Run("an operation that changes nothing writes no audit row", func(t *testing.T) {
		c := w.create(t, "openai", "audited-repeat", "synthetic-repeat-secret-0007")
		seen := seenIDs(t, w.api)
		for i := 0; i < 3; i++ {
			decodeCred(t, w.do(t, "POST", w.path(c.ID, "disable"), w.admin, nil))
		}
		if rows := newRows(t, w.api, seen); len(rows) != 1 || rows[0].Action != "credential.disable" {
			t.Errorf("three disables wrote %+v, want exactly one credential.disable", rows)
		}
		decodeCred(t, w.do(t, "POST", w.path(c.ID, "enable"), w.admin, nil))
		decodeCred(t, w.do(t, "POST", w.path(c.ID, "enable"), w.admin, nil))
		decodeCred(t, w.do(t, "POST", w.path(c.ID, "set-default"), w.admin, nil))
		newRows(t, w.api, seen)
		decodeCred(t, w.do(t, "POST", w.path(c.ID, "set-default"), w.admin, nil)) // it is the default now
		decodeCred(t, w.do(t, "PATCH", w.path(c.ID), w.admin, map[string]any{"label": "audited-repeat"}))
		decodeCred(t, w.do(t, "POST", w.path(c.ID, "revoke"), w.admin, nil))
		newRows(t, w.api, seen)
		decodeCred(t, w.do(t, "POST", w.path(c.ID, "revoke"), w.admin, nil))
		if rows := newRows(t, w.api, seen); len(rows) != 0 {
			t.Errorf("repeats that change nothing wrote %+v", rows)
		}
	})
}

func parseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestNoResponseHoldsASecret is T-341: every response of every credential operation, success and
// each error, holds neither the canary nor the sealed bytes of the database.
func TestNoResponseHoldsASecret(t *testing.T) {
	fake := fakeprovider.New(t)
	fake.Accept() // nothing is accepted: the provider answers 401 and echoes the key
	w := newCW(t, "no-secret", withValidator(t, 2*time.Second))
	w.pointAt(t, "openai", "default", fake)
	other := newCWOn(t, w.api, "no-secret-other")
	secretOne := fixtures.CanaryCredential
	cred := w.create(t, "openai", "main", secretOne)
	foreign := other.create(t, "openai", "main", secretOne)
	revoked := w.create(t, "anthropic", "revoked", secretOne+"-revoked")
	if _, err := w.c.RevokeCredential(ctx(t), adminapi.RevokeCredentialInput{Tenant: w.tenant.ID, Credential: revoked.ID}); err != nil {
		t.Fatal(err)
	}
	inference := func() string {
		sa, err := w.c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "inference", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
		if err != nil {
			t.Fatal(err)
		}
		return k.Secret
	}()

	var responses []string
	record := func(what string, r *apitest.Response) *apitest.Response {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d\n", r.Status)
		keys := make([]string, 0, len(r.Header))
		for k := range r.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s: %s\n", k, strings.Join(r.Header[k], ","))
		}
		sb.Write(r.Body)
		responses = append(responses, what+"\n"+sb.String())
		return r
	}

	const oversize = 2 << 20
	canaryBlob := `{"provider":"openai","label":"big","secret":"` + secretOne + `","description":"` + strings.Repeat("x", oversize) + `"}`

	n := 0
	for _, op := range adminapi.Operations {
		if !strings.Contains(op.Path, "credential") {
			continue
		}
		n++
		p := apitest.Params{Tenant: w.tenant.ID, Credential: cred.ID}
		call := apitest.CallFor(t, op, p)
		tenantKey := w.admin
		if strings.HasPrefix(op.Path, "/v1/tenant-credentials/") {
			tenantKey = w.api.PlatformKey()
		}
		// The valid call, with a credential of its own where the call changes it.
		vp := p
		if strings.Contains(op.Path, "{credential}") && op.Method != "GET" && op.ID != "createCredential" {
			vp.Credential = w.create(t, "openai", apitest.UniqueName("extra"), secretOne).ID
		}
		vc := apitest.CallFor(t, op, vp)
		record(op.ID+" valid", w.do(t, vc.Method, vc.Path, tenantKey, vc.Body))
		// Errors: the variants of section 5.4.
		record(op.ID+" without a key", w.do(t, call.Method, call.Path, "", call.Body))
		record(op.ID+" with a malformed key", w.do(t, call.Method, call.Path, "pdk_nonsense", call.Body))
		record(op.ID+" with a key that has no permission", w.do(t, call.Method, call.Path, inference, call.Body))
		mp := p
		mp.Credential = "0192f0f0-0000-7000-8000-00000000beef"
		mc := apitest.CallFor(t, op, mp)
		record(op.ID+" of a credential that is not there", w.do(t, mc.Method, mc.Path, tenantKey, mc.Body))
		fp := p
		fp.Credential = foreign.ID
		fc := apitest.CallFor(t, op, fp)
		record(op.ID+" of a credential of another tenant", w.do(t, fc.Method, fc.Path, tenantKey, fc.Body))
		rp := p
		rp.Credential = revoked.ID
		rc := apitest.CallFor(t, op, rp)
		record(op.ID+" of a revoked credential", w.do(t, rc.Method, rc.Path, tenantKey, rc.Body))
		if call.Body != nil {
			record(op.ID+" with a body that is not JSON", w.do(t, call.Method, call.Path, tenantKey, `{"secret":"`+secretOne+`"`))
			record(op.ID+" with another content type", w.api.Do(t, call.Method, call.Path, tenantKey, `{"secret":"`+secretOne+`"}`, http.Header{"Content-Type": {"text/plain"}}))
			record(op.ID+" with a body that is too large", w.do(t, call.Method, call.Path, tenantKey, canaryBlob))
			record(op.ID+" with an unknown member", w.do(t, call.Method, call.Path, tenantKey, map[string]any{"secret": secretOne, "sealed": secretOne, "reason": "x"}))
		}
	}
	// The variants that are about the content.
	for _, c := range []struct {
		name, method, path string
		body               any
	}{
		{"create with a taken label", "POST", w.path(), map[string]any{"provider": "openai", "label": "main", "secret": secretOne}},
		{"create with a short secret", "POST", w.path(), map[string]any{"provider": "openai", "label": "short", "secret": secretOne[:6]}},
		{"create with an unknown provider", "POST", w.path(), map[string]any{"provider": "gemini", "label": "g", "secret": secretOne}},
		{"create with a secret that has a space at the end", "POST", w.path(), map[string]any{"provider": "openai", "label": "sp", "secret": secretOne + " "}},
		{"create with a secret with a control character", "POST", w.path(), map[string]any{"provider": "openai", "label": "cc", "secret": secretOne + "\x07"}},
		{"create, rejected by the provider", "POST", w.path(), map[string]any{"provider": "openai", "label": "rej", "secret": secretOne, "validate": true}},
		{"replace, rejected by the provider", "PUT", w.path(cred.ID, "secret"), map[string]any{"secret": secretOne, "validate": true}},
		{"validate for anthropic", "POST", w.path(), map[string]any{"provider": "anthropic", "label": "va", "secret": secretOne, "validate": true}},
		{"replace a revoked credential", "PUT", w.path(revoked.ID, "secret"), map[string]any{"secret": secretOne}},
		{"the secret in the query", "GET", w.path(cred.ID) + "?secret=" + secretOne, nil},
		{"the secret in the path", "GET", w.path(secretOne), nil},
	} {
		record(c.name, w.do(t, c.method, c.path, w.admin, c.body))
	}
	fake.ModelsBehaviour("500")
	record("create, the provider fails", w.do(t, "POST", w.path(), w.admin, map[string]any{"provider": "openai", "label": "fail", "secret": secretOne, "validate": true}))
	fake.ModelsBehaviour("redirect")
	record("create, the provider redirects", w.do(t, "POST", w.path(), w.admin, map[string]any{"provider": "openai", "label": "redir", "secret": secretOne, "validate": true}))
	fake.ModelsBehaviour("")
	fake.Accept(secretOne)
	record("create, accepted by the provider", w.do(t, "POST", w.path(), w.admin, map[string]any{"provider": "openai", "label": "accepted", "secret": secretOne, "validate": true}))
	if n != 14 {
		t.Fatalf("%d credential operations were exercised, want 14", n)
	}

	// What must not be in any of them: the canary in four forms, and the sealed bytes of the
	// database, in every form and in every run of twelve bytes.
	forbidden := testutil.SecretForms(secretOne)
	forbidden = append(forbidden, testutil.SecretForms(secretOne+"-revoked")...)
	rows, err := w.api.Super(t).Query(ctx(t), `SELECT secret FROM pandokos.provider_credential WHERE secret IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	sealedCount := 0
	for rows.Next() {
		var s []byte
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		sealedCount++
		forbidden = append(forbidden, sealedRuns(s)...)
	}
	rows.Close()
	if sealedCount < 4 {
		t.Fatalf("only %d sealed secrets were found: the scan would prove little", sealedCount)
	}
	for _, r := range responses {
		name, text, _ := strings.Cut(r, "\n")
		for i, f := range forbidden {
			if len(f) >= 8 && strings.Contains(text, f) {
				t.Errorf("%s: form %d of the secret or of the sealed bytes is in the response", name, i)
			}
		}
	}
	if len(responses) < 14*8 {
		t.Errorf("only %d responses were scanned", len(responses))
	}
	// And the logs, at debug level, although the provider echoed the key.
	logs := w.api.Logs.String()
	for i, f := range forbidden {
		if len(f) >= 8 && strings.Contains(logs, f) {
			t.Errorf("form %d of the secret or of the sealed bytes is in the log", i)
		}
	}
	// The response to a success has the members of the metadata and no other.
	r := w.do(t, "GET", w.path(cred.ID), w.admin, nil)
	assertMembers(t, "get", r.Body)
	l := w.do(t, "GET", w.path(), w.admin, nil)
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	ok(t, l, 200, &list)
	for _, it := range list.Items {
		assertMembers(t, "list item", it)
	}
	// A Location-like header is never set by these operations.
	for _, h := range []string{"Location", "Content-Location", "Link", "Set-Cookie"} {
		if r.Header.Get(h) != "" {
			t.Errorf("the response has the header %s", h)
		}
	}
}

// sealedRuns returns the forms in which a run of the sealed bytes could appear in an output:
// the whole value in hexadecimal and base64, and every window of twelve bytes in hexadecimal
// (and in base64 where the window is aligned).
func sealedRuns(s []byte) []string {
	out := append([]string(nil), testutil.SecretForms(string(s))[1:]...)
	out = append(out, `\x`+fmt.Sprintf("%x", s))
	for i := 0; i+12 <= len(s); i++ {
		out = append(out, fmt.Sprintf("%x", s[i:i+12]))
		if i%3 == 0 {
			out = append(out, testutil.SecretForms(string(s[i : i+12]))[1], testutil.SecretForms(string(s[i : i+12]))[2])
		}
	}
	return out
}
