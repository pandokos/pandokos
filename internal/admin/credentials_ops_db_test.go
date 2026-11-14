package admin_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/vault"
	"github.com/pandokos/pandokos/test/apitest"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// tenantPath is the platform path of a credential of a tenant.
func tenantPath(tenantID string, parts ...string) string {
	return "/v1/tenant-credentials/" + tenantID + strings.Join(append([]string{""}, parts...), "/")
}

// TestPlatformOversight is T-342: a platform administrator sees metadata and can disable or
// revoke with a reason, and can do nothing else (AZ-08).
func TestPlatformOversight(t *testing.T) {
	w := newCW(t, "oversight")
	other := newCWOn(t, w.api, "oversight-other")
	c := w.create(t, "openai", "main", fixtures.CanaryCredential)
	second := w.create(t, "openai", "second", "synthetic-second-secret-0002")
	foreign := other.create(t, "openai", "main", fixtures.CanaryCredential)
	plat := w.api.PlatformKey()

	t.Run("a platform key lists and reads the metadata of a tenant", func(t *testing.T) {
		l, err := w.plat.ListTenantCredentials(ctx(t), adminapi.ListTenantCredentialsInput{Tenant: w.tenant.ID, Sort: adminapi.SortCreatedAt})
		if err != nil || len(l.Items) != 2 || l.Items[0].ID != c.ID || l.Items[1].ID != second.ID {
			t.Fatalf("list: %+v %v", l, err)
		}
		for _, it := range l.Items {
			if it.TenantID != w.tenant.ID || it.Owner != adminapi.CredentialOwnerTenant {
				t.Errorf("item: %+v", it)
			}
		}
		g, err := w.plat.GetTenantCredential(ctx(t), adminapi.GetTenantCredentialInput{Tenant: w.tenant.ID, Credential: c.ID})
		if err != nil || g.ID != c.ID || g.Fingerprint != c.Fingerprint || g.Hint != c.Hint || g.Label != "main" {
			t.Errorf("get: %+v %v", g, err)
		}
		r := w.do(t, "GET", tenantPath(w.tenant.ID, c.ID), plat, nil)
		assertMembers(t, "platform get", r.Body)
		testutil.AssertAbsent(t, "the platform read", string(r.Body), testutil.SecretForms(fixtures.CanaryCredential)...)
		// Filters and pages work as for the tenant.
		f, err := w.plat.ListTenantCredentials(ctx(t), adminapi.ListTenantCredentialsInput{Tenant: w.tenant.ID, Provider: "anthropic"})
		if err != nil || len(f.Items) != 0 {
			t.Errorf("filter: %+v %v", f, err)
		}
		p1, err := w.plat.ListTenantCredentials(ctx(t), adminapi.ListTenantCredentialsInput{Tenant: w.tenant.ID, Limit: 1})
		if err != nil || len(p1.Items) != 1 || p1.NextCursor == "" {
			t.Errorf("page: %+v %v", p1, err)
		}
		// A credential of another tenant is not found under this one.
		expect(t, w.do(t, "GET", tenantPath(w.tenant.ID, foreign.ID), plat, nil), 404, adminapi.ProblemCodeNotFound)
		// Reads write nothing.
		seen := seenIDs(t, w.api)
		w.do(t, "GET", tenantPath(w.tenant.ID), plat, nil)
		w.do(t, "GET", tenantPath(w.tenant.ID, c.ID), plat, nil)
		if rows := newRows(t, w.api, seen); len(rows) != 0 {
			t.Errorf("a platform read wrote %+v", rows)
		}
	})

	t.Run("an unknown tenant and an unknown credential are not found", func(t *testing.T) {
		const nobody = "0192f0f0-0000-7000-8000-00000000beef"
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", tenantPath(nobody), nil},
			{"GET", tenantPath(nobody, second.ID), nil},
			{"POST", tenantPath(nobody, second.ID, "disable"), map[string]any{"reason": "x"}},
			{"POST", tenantPath(nobody, second.ID, "revoke"), map[string]any{"reason": "x"}},
			{"GET", tenantPath(w.tenant.ID, nobody), nil},
			{"POST", tenantPath(w.tenant.ID, nobody, "disable"), map[string]any{"reason": "x"}},
			{"POST", tenantPath(w.tenant.ID, nobody, "revoke"), map[string]any{"reason": "x"}},
		} {
			expect(t, w.do(t, c.method, c.path, plat, c.body), 404, adminapi.ProblemCodeNotFound)
		}
	})

	t.Run("disable and revoke need a reason", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.tenant.ID)
		for _, verb := range []string{"disable", "revoke"} {
			for _, body := range []any{nil, map[string]any{}, map[string]any{"reason": ""}, map[string]any{"reason": strings.Repeat("r", 501)}, map[string]any{"why": "x"}} {
				r := w.do(t, "POST", tenantPath(w.tenant.ID, second.ID, verb), plat, body)
				expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
			}
		}
		if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
			t.Error("a refused call changed a row of the tenant")
		}
	})

	t.Run("disable: the row, the tenant's view, the audit row", func(t *testing.T) {
		seen := seenIDs(t, w.api)
		r := w.do(t, "POST", tenantPath(w.tenant.ID, second.ID, "disable"), plat, map[string]any{"reason": "abuse report"})
		got := decodeCred(t, r)
		if got.State != adminapi.CredentialStateDisabled || got.StateChangedBy != adminapi.CredentialActorPlatform || got.StateReason != "abuse report" || got.ID != second.ID {
			t.Errorf("disable: %+v", got)
		}
		// The tenant reads the reason on the credential itself.
		mine := decodeCred(t, w.do(t, "GET", w.path(second.ID), w.admin, nil))
		if mine.State != adminapi.CredentialStateDisabled || mine.StateChangedBy != adminapi.CredentialActorPlatform || mine.StateReason != "abuse report" {
			t.Errorf("the tenant reads: %+v", mine)
		}
		rows := newRows(t, w.api, seen)
		if len(rows) != 1 {
			t.Fatalf("%d audit rows: %+v", len(rows), rows)
		}
		a := rows[0]
		if a.Log != "tenant" || a.TenantID != w.tenant.ID || a.ActorType != "platform" || a.Action != "credential.disable" || a.ResourceType != "credential" || a.ResourceID != second.ID ||
			a.Reason != "abuse report" || a.Outcome != "success" || a.RequestID != r.Header.Get("X-Pandokos-Request-Id") {
			t.Errorf("the audit row: %+v", a)
		}
		if a.Detail["provider"] != "openai" || a.Detail["label"] != "second" || a.Detail["fingerprint"] != second.Fingerprint {
			t.Errorf("the detail: %v", a.Detail)
		}
		// Again, with another reason: nothing changes, nothing is written.
		again := decodeCred(t, w.do(t, "POST", tenantPath(w.tenant.ID, second.ID, "disable"), plat, map[string]any{"reason": "another"}))
		if again.StateReason != "abuse report" || len(newRows(t, w.api, seen)) != 0 {
			t.Errorf("disabling a disabled credential: %+v", again)
		}
		// The tenant may enable it again (open point O-5).
		enabled := decodeCred(t, w.do(t, "POST", w.path(second.ID, "enable"), w.admin, nil))
		if enabled.State != adminapi.CredentialStateActive || enabled.StateChangedBy != adminapi.CredentialActorTenant {
			t.Errorf("the tenant enables what the platform disabled: %+v", enabled)
		}
	})

	t.Run("revoke destroys the secret", func(t *testing.T) {
		seen := seenIDs(t, w.api)
		r := w.do(t, "POST", tenantPath(w.tenant.ID, second.ID, "revoke"), plat, map[string]any{"reason": "confirmed abuse"})
		got := decodeCred(t, r)
		if got.State != adminapi.CredentialStateRevoked || got.StateChangedBy != adminapi.CredentialActorPlatform || got.StateReason != "confirmed abuse" || got.Fingerprint != second.Fingerprint {
			t.Errorf("revoke: %+v", got)
		}
		var null bool
		if err := w.api.Super(t).QueryRow(ctx(t), `SELECT secret IS NULL FROM pandokos.provider_credential WHERE id = $1::uuid`, second.ID).Scan(&null); err != nil || !null {
			t.Errorf("the sealed secret is still there: %v %v", null, err)
		}
		rows := newRows(t, w.api, seen)
		if len(rows) != 1 || rows[0].Action != "credential.revoke" || rows[0].ActorType != "platform" || rows[0].Reason != "confirmed abuse" || rows[0].TenantID != w.tenant.ID {
			t.Errorf("the audit rows: %+v", rows)
		}
		// Final: the tenant cannot undo it, and the platform's repeat changes nothing.
		expect(t, w.do(t, "POST", w.path(second.ID, "enable"), w.admin, nil), 409, adminapi.ProblemCodeInvalidState)
		decodeCred(t, w.do(t, "POST", tenantPath(w.tenant.ID, second.ID, "revoke"), plat, map[string]any{"reason": "again"}))
		if rows := newRows(t, w.api, seen); len(rows) != 0 {
			t.Errorf("a repeated revocation wrote %+v", rows)
		}
		// Disabling a revoked credential is invalid_state.
		expect(t, w.do(t, "POST", tenantPath(w.tenant.ID, second.ID, "disable"), plat, map[string]any{"reason": "x"}), 409, adminapi.ProblemCodeInvalidState)
	})

	t.Run("no operation with a platform permission accepts a secret", func(t *testing.T) {
		before := w.api.SnapshotTenant(t, w.tenant.ID)
		for _, verb := range []string{"disable", "revoke"} {
			for _, body := range []map[string]any{
				{"reason": "x", "secret": fixtures.CanaryCredential},
				{"reason": "x", "sealed": "00"},
				{"reason": "x", "label": "renamed"},
				{"reason": "x", "state": "active"},
			} {
				r := w.do(t, "POST", tenantPath(w.tenant.ID, c.ID, verb), plat, body)
				expect(t, r, 400, adminapi.ProblemCodeInvalidRequest)
				testutil.AssertAbsent(t, "the problem", string(r.Body), testutil.SecretForms(fixtures.CanaryCredential)...)
			}
		}
		for _, q := range []string{"?secret=x", "?reveal=true", "?include=secret"} {
			expect(t, w.do(t, "GET", tenantPath(w.tenant.ID, c.ID)+q, plat, nil), 400, adminapi.ProblemCodeInvalidRequest)
		}
		// There is no operation to create, replace, update, delete or enable by the platform.
		for _, c := range []struct{ method, path string }{
			{"POST", tenantPath(w.tenant.ID)}, {"PUT", tenantPath(w.tenant.ID, c.ID, "secret")}, {"PATCH", tenantPath(w.tenant.ID, c.ID)},
			{"DELETE", tenantPath(w.tenant.ID, c.ID)}, {"POST", tenantPath(w.tenant.ID, c.ID, "enable")}, {"POST", tenantPath(w.tenant.ID, c.ID, "set-default")},
		} {
			r := w.do(t, c.method, c.path, plat, map[string]any{"secret": fixtures.CanaryCredential})
			if r.Status != 404 && r.Status != 405 {
				t.Errorf("%s %s: status %d, want 404 or 405", c.method, c.path, r.Status)
			}
		}
		if after := w.api.SnapshotTenant(t, w.tenant.ID); after != before {
			t.Error("a refused call changed a row of the tenant")
		}
	})

	t.Run("a platform key is refused all ten tenant operations, and a tenant key the four platform ones", func(t *testing.T) {
		n, m := 0, 0
		for _, op := range adminapi.Operations {
			if !strings.Contains(op.Path, "credential") {
				continue
			}
			perm, _ := authz.Lookup(op.Permission)
			p := apitest.Params{Tenant: w.tenant.ID, Credential: c.ID}
			call := apitest.CallFor(t, op, p)
			switch perm.Scope {
			case authz.ScopeTenant:
				n++
				expect(t, w.do(t, call.Method, call.Path, plat, call.Body), 403, adminapi.ProblemCodeForbidden)
			case authz.ScopePlatform:
				m++
				expect(t, w.do(t, call.Method, call.Path, w.admin, call.Body), 403, adminapi.ProblemCodeForbidden)
			}
		}
		if n != 10 || m != 4 {
			t.Errorf("%d tenant and %d platform operations were exercised, want 10 and 4", n, m)
		}
		// The first credential was not touched by any of it.
		if g := decodeCred(t, w.do(t, "GET", w.path(c.ID), w.admin, nil)); g.State != adminapi.CredentialStateActive || g.Fingerprint != c.Fingerprint {
			t.Errorf("after the refused calls: %+v", g)
		}
	})
}

// TestCredentialAudit is T-343, with T-281 and T-282 of audit_db_test.go: the data key of a
// tenant that has none is created, and audited, at its first credential write.
func TestCredentialAudit(t *testing.T) {
	w := newCW(t, "audit-lazy")
	api := w.api
	kek := kekVersion(t, api)

	t.Run("the data key is created with the tenant and not again", func(t *testing.T) {
		var n int
		if err := api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid AND version = 1 AND kek_version = $2`, w.tenant.ID, kek).Scan(&n); err != nil || n != 1 {
			t.Fatalf("a new tenant has %d data keys of version 1: %v", n, err)
		}
		var creates int
		for _, r := range auditRows(t, api) {
			if r.TenantID == w.tenant.ID && r.Action == "data_key.create" {
				creates++
				if r.ResourceType != "data_key" || r.ResourceID != "1" || r.ActorType != "platform" || r.Detail["version"] != float64(1) || r.Detail["kek_version"] != kek {
					t.Errorf("the data key row: %+v", r)
				}
			}
		}
		if creates != 1 {
			t.Errorf("%d data_key.create rows for the new tenant, want 1", creates)
		}
		// A first credential does not create another.
		seen := seenIDs(t, api)
		w.create(t, "openai", "first", fixtures.CanaryCredential)
		for _, r := range newRows(t, api, seen) {
			if r.Action == "data_key.create" {
				t.Errorf("a data key was created again: %+v", r)
			}
		}
	})

	t.Run("a tenant that has no data key gets one at its first credential", func(t *testing.T) {
		old := newCWOn(t, api, "audit-old")
		// The tenant predates the vault: its data key row is not there.
		if _, err := api.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, old.tenant.ID); err != nil {
			t.Fatal(err)
		}
		seen := seenIDs(t, api)
		r := old.do(t, "POST", old.path(), old.admin, map[string]any{"provider": "openai", "label": "late", "secret": fixtures.CanaryCredential})
		var c adminapi.Credential
		ok(t, r, 201, &c)
		rows := newRows(t, api, seen)
		if len(rows) != 2 || rows[0].Action != "credential.create" || rows[1].Action != "data_key.create" {
			t.Fatalf("audit rows: %+v, want credential.create and data_key.create", rows)
		}
		id := r.Header.Get("X-Pandokos-Request-Id")
		dk := rows[1]
		if dk.RequestID != id || rows[0].RequestID != id || dk.Log != "tenant" || dk.TenantID != old.tenant.ID || dk.ResourceType != "data_key" || dk.ResourceID != "1" ||
			dk.ActorType != "service_account" || dk.Detail["version"] != float64(1) || dk.Detail["kek_version"] != kek {
			t.Errorf("the data key row: %+v (the request ID of the response is %s)", dk, id)
		}
		// The second credential finds the key.
		seen = seenIDs(t, api)
		old.create(t, "anthropic", "next", fixtures.CanaryCredential)
		if rows := newRows(t, api, seen); len(rows) != 1 || rows[0].Action != "credential.create" {
			t.Errorf("the second credential wrote %+v", rows)
		}
		// And the credential opens with it: the key of the tenant is the one that sealed it.
		var n int
		if err := api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, old.tenant.ID).Scan(&n); err != nil || n != 1 {
			t.Errorf("%d data keys", n)
		}
	})

	t.Run("two first writes at once make one data key", func(t *testing.T) {
		old := newCWOn(t, api, "audit-race")
		if _, err := api.Super(t).Exec(ctx(t), `DELETE FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, old.tenant.ID); err != nil {
			t.Fatal(err)
		}
		seenRows := seenIDs(t, api) // the tenant's creation audited its first data key; count what the writes add
		var wg sync.WaitGroup
		statuses := make([]int, 6)
		for i := range statuses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := old.do(t, "POST", old.path(), old.admin, map[string]any{"provider": "openai", "label": fmt.Sprintf("race-%d", i), "secret": fixtures.CanaryCredential})
				statuses[i] = r.Status
			}()
		}
		wg.Wait()
		for i, s := range statuses {
			if s != 201 {
				t.Errorf("write %d: status %d", i, s)
			}
		}
		var keys, creates int
		if err := api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.tenant_data_key WHERE tenant_id = $1::uuid`, old.tenant.ID).Scan(&keys); err != nil || keys != 1 {
			t.Errorf("%d data keys after six first writes", keys)
		}
		for _, r := range auditRows(t, api) {
			if !seenRows[r.ID] && r.TenantID == old.tenant.ID && r.Action == "data_key.create" {
				creates++
			}
		}
		if creates != 1 {
			t.Errorf("%d data_key.create rows, want 1", creates)
		}
		// Exactly one of the six is the default of its provider.
		l, err := old.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: old.tenant.ID, Provider: "openai"})
		if err != nil || len(l.Items) != 6 {
			t.Fatalf("%+v %v", l, err)
		}
		defaults := 0
		for _, it := range l.Items {
			if it.IsDefault {
				defaults++
			}
		}
		if defaults != 1 {
			t.Errorf("%d defaults among six concurrent first credentials", defaults)
		}
	})

	t.Run("no row holds a secret or a sealed byte", func(t *testing.T) {
		var forbidden []string
		forbidden = append(forbidden, testutil.SecretForms(fixtures.CanaryCredential)...)
		rows, err := api.Super(t).Query(ctx(t), `SELECT secret FROM pandokos.provider_credential WHERE secret IS NOT NULL`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s []byte
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			forbidden = append(forbidden, sealedRuns(s)...)
		}
		rows.Close()
		wrapped, err := api.Super(t).Query(ctx(t), `SELECT wrapped_key FROM pandokos.tenant_data_key`)
		if err != nil {
			t.Fatal(err)
		}
		for wrapped.Next() {
			var s []byte
			if err := wrapped.Scan(&s); err != nil {
				t.Fatal(err)
			}
			forbidden = append(forbidden, sealedRuns(s)...)
		}
		wrapped.Close()
		for _, r := range auditRows(t, api) {
			text := fmt.Sprintf("%+v", r)
			for i, f := range forbidden {
				if len(f) >= 8 && strings.Contains(text, f) {
					t.Errorf("form %d of a secret, a sealed value or a wrapped key is in the audit row %s %s", i, r.Action, r.ID)
				}
			}
		}
	})
}

// TestValidationAtWrite is T-344: with the fake provider as the validator's destination.
func TestValidationAtWrite(t *testing.T) {
	fake := fakeprovider.New(t)
	fake.Accept(fixtures.CanaryCredential)
	w := newCW(t, "validated", func(o *admin.Options) {
		o.Validators = map[string]vault.Validator{"openai": vault.NewOpenAIValidator(parseURL(t, fake.BaseURL()), 700*time.Millisecond)}
	})
	api := w.api
	count := func() int {
		var n int
		if err := api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pandokos.provider_credential WHERE tenant_id = $1::uuid`, w.tenant.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	create := func(label string, validate bool) *apitest.Response {
		return w.do(t, "POST", w.path(), w.admin, map[string]any{"provider": "openai", "label": label, "secret": fixtures.CanaryCredential, "validate": validate})
	}
	rejectedProblem := func(r *apitest.Response) {
		t.Helper()
		p := expect(t, r, 422, adminapi.ProblemCodeCredentialRejected)
		if p.Title != "Credential rejected" || p.Detail != "The provider did not accept the credential. Nothing was stored." || len(p.Errors) != 0 {
			t.Errorf("the problem: %+v", p)
		}
	}
	unavailableProblem := func(r *apitest.Response) {
		t.Helper()
		p := expect(t, r, 502, adminapi.ProblemCodeValidationUnavailable)
		if p.Title != "Validation unavailable" || p.Detail != "The provider could not confirm the credential. Nothing was stored." || len(p.Errors) != 0 {
			t.Errorf("the problem: %+v", p)
		}
	}

	t.Run("without validate the provider receives nothing", func(t *testing.T) {
		before := len(fake.Requests())
		var c adminapi.Credential
		ok(t, create("plain", false), 201, &c)
		if c.ValidatedAt != nil {
			t.Errorf("a credential that was not validated has validated_at %v", c.ValidatedAt)
		}
		if n := len(fake.Requests()); n != before {
			t.Errorf("the provider received %d requests for a write without validate", n-before)
		}
	})

	t.Run("accepted: stored, with validated_at", func(t *testing.T) {
		before := len(fake.Requests())
		start := time.Now().Add(-time.Second)
		var c adminapi.Credential
		ok(t, create("accepted", true), 201, &c)
		if c.ValidatedAt == nil || c.ValidatedAt.Before(start) || c.State != adminapi.CredentialStateActive {
			t.Errorf("created: %+v", c)
		}
		reqs := fake.Requests()[before:]
		if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].Path != "/v1/models" || reqs[0].Header.Get("Authorization") != "Bearer "+fixtures.CanaryCredential {
			t.Fatalf("the provider received %+v", reqs)
		}
		for name := range reqs[0].Header {
			if name != "Authorization" && name != "User-Agent" && name != "Accept-Encoding" && name != "Connection" { // Connection: close, as the validator opens no reused connection
				t.Errorf("the validation request carries the header %s", name)
			}
		}
		if len(reqs[0].Body) != 0 || reqs[0].RawQuery != "" {
			t.Errorf("the validation request has a body or a query: %+v", reqs[0])
		}
		// Replace with validation: validated_at is set again; without: it is cleared.
		var rep adminapi.Credential
		ok(t, w.do(t, "PUT", w.path(c.ID, "secret"), w.admin, map[string]any{"secret": fixtures.CanaryCredential, "validate": true}), 200, &rep)
		if rep.ValidatedAt == nil || rep.RotatedAt == nil {
			t.Errorf("replace with validation: %+v", rep)
		}
		rep = adminapi.Credential{} // a member that is absent leaves the old value in a reused struct
		ok(t, w.do(t, "PUT", w.path(c.ID, "secret"), w.admin, map[string]any{"secret": fixtures.CanaryCredential}), 200, &rep)
		if rep.ValidatedAt != nil {
			t.Errorf("replace without validation kept validated_at %v", rep.ValidatedAt)
		}
	})

	t.Run("rejected: 422, nothing stored, nothing audited", func(t *testing.T) {
		for name, setup := range map[string]func(){
			"the provider says 401": func() { fake.Accept(); fake.ModelsBehaviour("") },
			"the provider says 403": func() { fake.Accept(fixtures.CanaryCredential); fake.ModelsBehaviour("403") },
		} {
			setup()
			rows, seen := count(), seenIDs(t, api)
			r := create("rejected", true)
			rejectedProblem(r)
			testutil.AssertAbsent(t, "the problem of "+name, string(r.Body)+fmt.Sprint(r.Header), testutil.SecretForms(fixtures.CanaryCredential)...)
			if count() != rows {
				t.Errorf("%s: a rejected credential was stored", name)
			}
			if got := newRows(t, api, seen); len(got) != 0 {
				t.Errorf("%s: a rejected credential was audited: %+v", name, got)
			}
		}
		// A replacement that is rejected leaves the credential as it was.
		fake.Accept(fixtures.CanaryCredential)
		fake.ModelsBehaviour("")
		c := w.create(t, "openai", "keep", fixtures.CanaryCredential)
		fake.Accept()
		rejectedProblem(w.do(t, "PUT", w.path(c.ID, "secret"), w.admin, map[string]any{"secret": "synthetic-rejected-secret-0001", "validate": true}))
		if g := decodeCred(t, w.do(t, "GET", w.path(c.ID), w.admin, nil)); g.Fingerprint != c.Fingerprint || g.RotatedAt != nil || g.Hint != c.Hint {
			t.Errorf("a rejected replacement changed the credential: %+v", g)
		}
		fake.Accept(fixtures.CanaryCredential)
	})

	t.Run("indeterminate: 502, nothing stored, nothing audited", func(t *testing.T) {
		for _, behaviour := range []string{"500", "429", "redirect", "stall"} {
			fake.ModelsBehaviour(behaviour)
			rows, seen := count(), seenIDs(t, api)
			r := create("unavailable-"+behaviour, true)
			unavailableProblem(r)
			testutil.AssertAbsent(t, "the problem for "+behaviour, string(r.Body)+fmt.Sprint(r.Header), testutil.SecretForms(fixtures.CanaryCredential)...)
			if count() != rows {
				t.Errorf("%s: a credential that could not be confirmed was stored", behaviour)
			}
			if got := newRows(t, api, seen); len(got) != 0 {
				t.Errorf("%s: wrote audit rows %+v", behaviour, got)
			}
		}
		fake.ModelsBehaviour("")
	})

	t.Run("validate for a provider without a validator is a mistake of the request", func(t *testing.T) {
		before := len(fake.Requests())
		r := w.do(t, "POST", w.path(), w.admin, map[string]any{"provider": "anthropic", "label": "claude", "secret": fixtures.CanaryCredential, "validate": true})
		problemWith(t, r, 400, adminapi.ProblemCodeInvalidRequest, "validate")
		if n := len(fake.Requests()); n != before {
			t.Errorf("the provider received %d requests", n-before)
		}
		c := w.create(t, "anthropic", "claude", fixtures.CanaryCredential)
		r = w.do(t, "PUT", w.path(c.ID, "secret"), w.admin, map[string]any{"secret": fixtures.CanaryCredential, "validate": true})
		problemWith(t, r, 400, adminapi.ProblemCodeInvalidRequest, "validate")
		if n := len(fake.Requests()); n != before {
			t.Errorf("the provider received %d requests", n-before)
		}
	})

	t.Run("no transaction is open while the provider is asked", func(t *testing.T) {
		fake.ModelsBehaviour("stall")
		defer fake.ModelsBehaviour("")
		before := len(fake.Requests())
		done := make(chan *apitest.Response, 1)
		go func() { done <- create("slow", true) }()
		testutil.Eventually(t, 5*time.Second, "the validation request to reach the provider", func() bool { return len(fake.Requests()) > before })
		var idle int
		if err := api.Super(t).QueryRow(ctx(t), `SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname = $1 AND state LIKE 'idle in transaction%'`, api.DB.Name).Scan(&idle); err != nil {
			t.Fatal(err)
		}
		if idle != 0 {
			t.Errorf("%d transactions are open while the provider is asked: the call must come before the transaction", idle)
		}
		unavailableProblem(<-done)
	})

	t.Run("the log line", func(t *testing.T) {
		count := func() int { return len(api.Logs.Find(t, "credential validation")) }
		type want struct {
			setup   func()
			outcome string
			status  float64
		}
		for _, c := range []want{
			{func() { fake.Accept(fixtures.CanaryCredential); fake.ModelsBehaviour("") }, "accepted", 200},
			{func() { fake.Accept() }, "rejected", 401},
			{func() { fake.Accept(fixtures.CanaryCredential); fake.ModelsBehaviour("500") }, "unavailable", 500},
			{func() { fake.ModelsBehaviour("stall") }, "unavailable", 0},
		} {
			c.setup()
			n := count()
			r := create("logged-"+c.outcome, true)
			id := r.Header.Get("X-Pandokos-Request-Id")
			lines := api.Logs.Find(t, "credential validation")
			if len(lines) != n+1 {
				t.Fatalf("%s: %d lines, want %d", c.outcome, len(lines), n+1)
			}
			l := lines[len(lines)-1]
			if l["level"] != "INFO" || l["request_id"] != id || l["tenant_id"] != w.tenant.ID || l["provider"] != "openai" || l["outcome"] != c.outcome || l["upstream_status"] != c.status {
				t.Errorf("%s: the line %v", c.outcome, l)
			}
			if _, ok := l["duration_ms"]; !ok {
				t.Errorf("%s: the line has no duration_ms", c.outcome)
			}
			for k := range l {
				if strings.HasSuffix(k, "_key") || strings.HasSuffix(k, "_secret") || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "_password") {
					t.Errorf("a log field named %s would be redacted by accident", k)
				}
			}
		}
		fake.ModelsBehaviour("")
		fake.Accept(fixtures.CanaryCredential)
	})

	t.Run("logs at debug hold no canary although the provider echoed it", func(t *testing.T) {
		fake.Accept() // 401, with the presented key in the body
		create("echo", true)
		fake.ModelsBehaviour("500")
		create("echo-500", true)
		fake.ModelsBehaviour("")
		testutil.AssertAbsent(t, "the log", api.Logs.String(), testutil.SecretForms(fixtures.CanaryCredential)...)
		fake.Accept(fixtures.CanaryCredential)
	})
}

// TestPinnedKey is T-345, with TestPrincipalCarriesThePin of package authn.
func TestPinnedKey(t *testing.T) {
	w := newCW(t, "pinned")
	other := newCWOn(t, w.api, "pinned-other")
	c := w.create(t, "openai", "main", fixtures.CanaryCredential)
	disabled := w.create(t, "openai", "disabled", "synthetic-disabled-secret-0001")
	if _, err := w.c.DisableCredential(ctx(t), adminapi.DisableCredentialInput{Tenant: w.tenant.ID, Credential: disabled.ID}); err != nil {
		t.Fatal(err)
	}
	revoked := w.create(t, "openai", "revoked", "synthetic-revoked-secret-0002")
	if _, err := w.c.RevokeCredential(ctx(t), adminapi.RevokeCredentialInput{Tenant: w.tenant.ID, Credential: revoked.ID}); err != nil {
		t.Fatal(err)
	}
	foreign := other.create(t, "openai", "main", fixtures.CanaryCredential)
	sa, err := w.c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "caller", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := "/v1/tenants/" + w.tenant.ID + "/virtual-keys"

	t.Run("a key pinned to a credential of the tenant", func(t *testing.T) {
		r := w.do(t, "POST", keyPath, w.admin, map[string]any{"service_account_id": sa.ID, "credential_id": c.ID, "description": "pinned"})
		var k adminapi.VirtualKeyCreated
		ok(t, r, 201, &k)
		if k.Key.CredentialID != c.ID {
			t.Errorf("the key shows the pin %q, want %s", k.Key.CredentialID, c.ID)
		}
		got, err := w.c.GetVirtualKey(ctx(t), adminapi.GetVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID})
		if err != nil || got.CredentialID != c.ID {
			t.Errorf("get: %+v %v", got, err)
		}
		l, err := w.c.ListVirtualKeys(ctx(t), adminapi.ListVirtualKeysInput{Tenant: w.tenant.ID})
		pinned := 0
		for _, it := range l.Items {
			if it.CredentialID == c.ID {
				pinned++
			}
		}
		if err != nil || pinned != 1 {
			t.Errorf("list: %d pinned keys, %v", pinned, err)
		}
		// A pin to a disabled credential is allowed: disabling is not revoking.
		var k2 adminapi.VirtualKeyCreated
		ok(t, w.do(t, "POST", keyPath, w.admin, map[string]any{"service_account_id": sa.ID, "credential_id": disabled.ID}), 201, &k2)
		// A key without a pin shows none.
		plain, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID}})
		if err != nil || plain.Key.CredentialID != "" {
			t.Errorf("a key without a pin: %+v %v", plain, err)
		}
		testutil.AssertAbsent(t, "the response", string(r.Body), testutil.SecretForms(fixtures.CanaryCredential)...)
		// The key cannot be changed to another pin: there is no operation for it.
		for _, m := range []string{"PATCH", "PUT"} {
			if r := w.do(t, m, keyPath+"/"+k.Key.ID, w.admin, map[string]any{"credential_id": disabled.ID}); r.Status < 400 {
				t.Errorf("%s of a key: status %d", m, r.Status)
			}
		}
	})

	t.Run("another tenant's credential, a missing one and a revoked one give the same 400", func(t *testing.T) {
		var bodies []string
		for name, id := range map[string]string{
			"another tenant's": foreign.ID, "a missing one": "0192f0f0-0000-7000-8000-00000000beef", "a revoked one": revoked.ID,
		} {
			r := w.do(t, "POST", keyPath, w.admin, map[string]any{"service_account_id": sa.ID, "credential_id": id})
			p := problemWith(t, r, 400, adminapi.ProblemCodeInvalidRequest, "credential_id")
			if len(p.Errors) != 1 || p.Errors[0].Field != "credential_id" || p.Errors[0].Message != "must name a credential of the tenant that is not revoked" {
				t.Errorf("%s: %+v", name, p.Errors)
			}
			p.RequestID = ""
			bodies = append(bodies, fmt.Sprint(p))
		}
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Errorf("the three refusals differ:\n%s\n%s", bodies[0], b)
			}
		}
		// A malformed ID is the same kind of mistake.
		expect(t, w.do(t, "POST", keyPath, w.admin, map[string]any{"service_account_id": sa.ID, "credential_id": "not-an-id"}), 400, adminapi.ProblemCodeInvalidRequest)
		// No key was created by any of it.
		l, err := w.c.ListVirtualKeys(ctx(t), adminapi.ListVirtualKeysInput{Tenant: w.tenant.ID, ServiceAccountID: sa.ID})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range l.Items {
			if it.CredentialID == foreign.ID || it.CredentialID == revoked.ID {
				t.Errorf("a key is pinned to %s", it.CredentialID)
			}
		}
		// The revoked credential cannot be the pin of a new key, and an old pin to one that is
		// revoked later stays on the key (it is how the key stops working).
		k, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, CredentialID: c.ID, Description: "later revoked"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.c.DisableCredential(ctx(t), adminapi.DisableCredentialInput{Tenant: w.tenant.ID, Credential: c.ID}); err != nil {
			t.Fatal(err)
		}
		if got, err := w.c.GetVirtualKey(ctx(t), adminapi.GetVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID}); err != nil || got.CredentialID != c.ID {
			t.Errorf("after the credential is disabled: %+v %v", got, err)
		}
		if _, err := w.c.EnableCredential(ctx(t), adminapi.EnableCredentialInput{Tenant: w.tenant.ID, Credential: c.ID}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the credential cannot be deleted while a usable key is pinned to it", func(t *testing.T) {
		d := w.create(t, "anthropic", "deletable", "synthetic-deletable-secret-0003")
		k, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, CredentialID: d.ID}})
		if err != nil {
			t.Fatal(err)
		}
		p := expect(t, w.do(t, "DELETE", w.path(d.ID), w.admin, nil), 409, adminapi.ProblemCodeInvalidState)
		_ = p
		if g, err := w.c.GetCredential(ctx(t), adminapi.GetCredentialInput{Tenant: w.tenant.ID, Credential: d.ID}); err != nil || g.ID != d.ID {
			t.Errorf("the refused deletion removed it: %v", err)
		}
		if _, err := w.c.RevokeVirtualKey(ctx(t), adminapi.RevokeVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID}); err != nil {
			t.Fatal(err)
		}
		if got := decodeCred(t, w.do(t, "DELETE", w.path(d.ID), w.admin, nil)); got.ID != d.ID {
			t.Errorf("delete after the revocation of the key: %+v", got)
		}
		if got, err := w.c.GetVirtualKey(ctx(t), adminapi.GetVirtualKeyInput{Tenant: w.tenant.ID, Key: k.Key.ID}); err != nil || got.CredentialID != "" {
			t.Errorf("the revoked key still names the deleted credential: %q %v", got.CredentialID, err)
		}
	})
}

// TestCredentialPermissions is T-346: the five tenant permissions of credentials are distinct.
func TestCredentialPermissions(t *testing.T) {
	w := newCW(t, "permissions")
	sa, err := w.c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "scoped", Roles: []string{"tenant-admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	keyWith := func(scopes ...string) string {
		k, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sa.ID, Scopes: scopes}})
		if err != nil {
			t.Fatal(err)
		}
		return k.Secret
	}
	inf, err := w.c.CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: w.tenant.ID, Body: adminapi.ServiceAccountCreate{Name: "inference", Roles: []string{"inference"}}})
	if err != nil {
		t.Fatal(err)
	}
	ik, err := w.c.CreateVirtualKey(ctx(t), adminapi.CreateVirtualKeyInput{Tenant: w.tenant.ID, Body: adminapi.VirtualKeyCreate{ServiceAccountID: inf.ID}})
	if err != nil {
		t.Fatal(err)
	}
	base := w.create(t, "openai", "base", fixtures.CanaryCredential)

	// Every tenant operation on credentials, with the permission it needs.
	needs := map[string]string{
		"listCredentials": authz.CredentialRead, "getCredential": authz.CredentialRead,
		"createCredential": authz.CredentialWrite, "updateCredential": authz.CredentialWrite, "disableCredential": authz.CredentialWrite,
		"enableCredential": authz.CredentialWrite, "setDefaultCredential": authz.CredentialWrite,
		"replaceCredentialSecret": authz.CredentialRotate,
		"revokeCredential":        authz.CredentialDelete, "deleteCredential": authz.CredentialDelete,
	}
	call := func(op adminapi.Operation, key string) *apitest.Response {
		t.Helper()
		p := apitest.Params{Tenant: w.tenant.ID, Credential: base.ID}
		if op.Method != "GET" && op.ID != "createCredential" {
			// An operation that changes the credential gets one of its own; a deletion needs
			// one that is not the default.
			p.Credential = w.create(t, "anthropic", apitest.UniqueName("own"), fixtures.CanaryCredential).ID
			if op.ID == "setDefaultCredential" || op.ID == "deleteCredential" || op.ID == "revokeCredential" {
				p.Credential = w.create(t, "openai", apitest.UniqueName("own"), fixtures.CanaryCredential).ID
			}
		}
		c := apitest.CallFor(t, op, p)
		return w.do(t, c.Method, c.Path, key, c.Body)
	}
	for _, perm := range []string{authz.CredentialRead, authz.CredentialWrite, authz.CredentialRotate, authz.CredentialDelete, authz.CredentialUse} {
		key := keyWith(perm)
		allowed := 0
		for _, op := range adminapi.Operations {
			want, ours := needs[op.ID]
			if !ours {
				continue
			}
			r := call(op, key)
			if want == perm {
				allowed++
				if r.Status != 200 && r.Status != 201 {
					t.Errorf("a key scoped to %s on %s: status %d\n%s", perm, op.ID, r.Status, r.Body)
				}
				continue
			}
			expect(t, r, 403, adminapi.ProblemCodeForbidden)
		}
		wantAllowed := map[string]int{authz.CredentialRead: 2, authz.CredentialWrite: 5, authz.CredentialRotate: 1, authz.CredentialDelete: 2, authz.CredentialUse: 0}[perm]
		if allowed != wantAllowed {
			t.Errorf("a key scoped to %s was allowed %d operations, want %d", perm, allowed, wantAllowed)
		}
	}

	t.Run("a key scoped to credential:read lists and cannot create", func(t *testing.T) {
		key := keyWith(authz.CredentialRead)
		if r := w.do(t, "GET", w.path(), key, nil); r.Status != 200 {
			t.Errorf("list: %d", r.Status)
		}
		expect(t, w.do(t, "POST", w.path(), key, map[string]any{"provider": "openai", "label": "denied", "secret": fixtures.CanaryCredential}), 403, adminapi.ProblemCodeForbidden)
		if l, err := w.c.ListCredentials(ctx(t), adminapi.ListCredentialsInput{Tenant: w.tenant.ID, Provider: "openai"}); err != nil {
			t.Fatal(err)
		} else {
			for _, it := range l.Items {
				if it.Label == "denied" {
					t.Error("a forbidden create created")
				}
			}
		}
	})

	t.Run("credential:use reads nothing", func(t *testing.T) {
		role, ok := authz.RoleByName(authz.RoleInference)
		if !ok {
			t.Fatal("no role inference")
		}
		has := false
		for _, p := range role.Permissions {
			has = has || p == authz.CredentialUse
			if strings.HasPrefix(p, "credential:") && p != authz.CredentialUse {
				t.Errorf("the role inference holds %s", p)
			}
		}
		if !has {
			t.Errorf("the role inference does not hold %s: %v", authz.CredentialUse, role.Permissions)
		}
		for _, op := range adminapi.Operations {
			if _, ours := needs[op.ID]; !ours {
				continue
			}
			expect(t, call(op, ik.Secret), 403, adminapi.ProblemCodeForbidden)
		}
		// The role tenant-admin holds the five permissions of the tenant.
		admin, _ := authz.RoleByName(authz.RoleTenantAdmin)
		held := map[string]bool{}
		for _, p := range admin.Permissions {
			held[p] = true
		}
		for _, p := range []string{authz.CredentialRead, authz.CredentialWrite, authz.CredentialRotate, authz.CredentialDelete, authz.CredentialUse} {
			if !held[p] {
				t.Errorf("tenant-admin does not hold %s", p)
			}
		}
		// Scopes name catalogued permissions, so a key can be limited to the new ones.
		r := w.api.Do(t, "POST", keyPath(w.tenant.ID), w.admin, map[string]any{"service_account_id": sa.ID, "scopes": []string{"credential:use", "model:invoke"}}, http.Header{})
		if r.Status != 201 {
			t.Errorf("a key scoped to credential:use and model:invoke: %d %s", r.Status, r.Body)
		}
		r = w.api.Do(t, "POST", keyPath(w.tenant.ID), w.admin, map[string]any{"service_account_id": sa.ID, "scopes": []string{"credential:destroy"}}, http.Header{})
		if r.Status != 400 {
			t.Errorf("a key scoped to a permission that does not exist: %d %s", r.Status, r.Body)
		}
	})
}

func keyPath(tenantID string) string { return "/v1/tenants/" + tenantID + "/virtual-keys" }
