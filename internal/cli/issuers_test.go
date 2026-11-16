package cli_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
)

// The operations of sprint 4.1 on the fake: each records its input and returns the canned output
// of r.f.out[<operation>] or a zero value.

func (f *fakeAPI) ListTokenIssuers(_ context.Context, in adminapi.ListTokenIssuersInput) (*adminapi.TokenIssuerList, error) {
	return pick[adminapi.TokenIssuerList](f, "listTokenIssuers"), f.rec("listTokenIssuers", in)
}
func (f *fakeAPI) CreateTokenIssuer(_ context.Context, in adminapi.CreateTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return pick[adminapi.TokenIssuer](f, "createTokenIssuer"), f.rec("createTokenIssuer", in)
}
func (f *fakeAPI) GetTokenIssuer(_ context.Context, in adminapi.GetTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return pick[adminapi.TokenIssuer](f, "getTokenIssuer"), f.rec("getTokenIssuer", in)
}
func (f *fakeAPI) UpdateTokenIssuer(_ context.Context, in adminapi.UpdateTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return pick[adminapi.TokenIssuer](f, "updateTokenIssuer"), f.rec("updateTokenIssuer", in)
}
func (f *fakeAPI) DeleteTokenIssuer(_ context.Context, in adminapi.DeleteTokenIssuerInput) (*adminapi.TokenIssuer, error) {
	return pick[adminapi.TokenIssuer](f, "deleteTokenIssuer"), f.rec("deleteTokenIssuer", in)
}
func (f *fakeAPI) ListTenantPlatformIssuers(_ context.Context, in adminapi.ListTenantPlatformIssuersInput) (*adminapi.TenantPlatformIssuerList, error) {
	return pick[adminapi.TenantPlatformIssuerList](f, "listTenantPlatformIssuers"), f.rec("listTenantPlatformIssuers", in)
}
func (f *fakeAPI) ListSubjectMappings(_ context.Context, in adminapi.ListSubjectMappingsInput) (*adminapi.SubjectMappingList, error) {
	return pick[adminapi.SubjectMappingList](f, "listSubjectMappings"), f.rec("listSubjectMappings", in)
}
func (f *fakeAPI) CreateSubjectMapping(_ context.Context, in adminapi.CreateSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	return pick[adminapi.SubjectMapping](f, "createSubjectMapping"), f.rec("createSubjectMapping", in)
}
func (f *fakeAPI) GetSubjectMapping(_ context.Context, in adminapi.GetSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	return pick[adminapi.SubjectMapping](f, "getSubjectMapping"), f.rec("getSubjectMapping", in)
}
func (f *fakeAPI) DeleteSubjectMapping(_ context.Context, in adminapi.DeleteSubjectMappingInput) (*adminapi.SubjectMapping, error) {
	return pick[adminapi.SubjectMapping](f, "deleteSubjectMapping"), f.rec("deleteSubjectMapping", in)
}
func (f *fakeAPI) ListPlatformIssuers(_ context.Context, in adminapi.ListPlatformIssuersInput) (*adminapi.PlatformIssuerList, error) {
	return pick[adminapi.PlatformIssuerList](f, "listPlatformIssuers"), f.rec("listPlatformIssuers", in)
}
func (f *fakeAPI) CreatePlatformIssuer(_ context.Context, in adminapi.CreatePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return pick[adminapi.PlatformIssuer](f, "createPlatformIssuer"), f.rec("createPlatformIssuer", in)
}
func (f *fakeAPI) GetPlatformIssuer(_ context.Context, in adminapi.GetPlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return pick[adminapi.PlatformIssuer](f, "getPlatformIssuer"), f.rec("getPlatformIssuer", in)
}
func (f *fakeAPI) UpdatePlatformIssuer(_ context.Context, in adminapi.UpdatePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return pick[adminapi.PlatformIssuer](f, "updatePlatformIssuer"), f.rec("updatePlatformIssuer", in)
}
func (f *fakeAPI) DeletePlatformIssuer(_ context.Context, in adminapi.DeletePlatformIssuerInput) (*adminapi.PlatformIssuer, error) {
	return pick[adminapi.PlatformIssuer](f, "deletePlatformIssuer"), f.rec("deletePlatformIssuer", in)
}
func (f *fakeAPI) ListPlatformIssuerTenants(_ context.Context, in adminapi.ListPlatformIssuerTenantsInput) (*adminapi.PlatformIssuerTenantList, error) {
	return pick[adminapi.PlatformIssuerTenantList](f, "listPlatformIssuerTenants"), f.rec("listPlatformIssuerTenants", in)
}
func (f *fakeAPI) PutPlatformIssuerTenant(_ context.Context, in adminapi.PutPlatformIssuerTenantInput) (*adminapi.PlatformIssuerTenant, error) {
	return pick[adminapi.PlatformIssuerTenant](f, "putPlatformIssuerTenant"), f.rec("putPlatformIssuerTenant", in)
}
func (f *fakeAPI) DeletePlatformIssuerTenant(_ context.Context, in adminapi.DeletePlatformIssuerTenantInput) (*adminapi.PlatformIssuerTenant, error) {
	return pick[adminapi.PlatformIssuerTenant](f, "deletePlatformIssuerTenant"), f.rec("deletePlatformIssuerTenant", in)
}

const (
	issuerID  = "0192f0f0-0000-7000-8000-000000000030"
	mappingID = "0192f0f0-0000-7000-8000-000000000031"
)

var (
	tokenIssuer = adminapi.TokenIssuer{ID: issuerID, TenantID: tid, Name: "idp", Description: "d", Issuer: "https://idp.example.com", Audience: "pandokos-acme",
		Algorithms: []string{"RS256", "ES256"}, SubjectClaim: "sub", RequiredClaims: map[string]string{"tier": "gold"}, Enabled: true, CreatedAt: t0, UpdatedAt: t0}
	platformIssuer = adminapi.PlatformIssuer{ID: issuerID, Name: "shared", Issuer: "https://idp.example.com", Audience: "pandokos", Algorithms: []string{"RS256", "ES256"},
		SubjectClaim: "sub", TenantClaim: "org", RequiredClaims: map[string]string{}, Enabled: true, TenantCount: 1, CreatedAt: t0, UpdatedAt: t0}
	mapping = adminapi.SubjectMapping{ID: mappingID, TenantID: tid, Issuer: adminapi.IssuerRef{Scope: adminapi.IssuerScopeTenant, ID: issuerID, Name: "idp"},
		Subject: "spiffe://acme/ns/ci", ServiceAccountID: sid, ServiceAccountName: "bot", Description: "ci", CreatedAt: t0}
)

// TestIssuerCommands is T-592: every operation of sprint 4.1 has a command (with
// TestEveryOperationHasACommand, T-219), its flags map to the members of the body, a member that
// is not given is not sent, and the output is the API's.
func TestIssuerCommands(t *testing.T) {
	call := func(t *testing.T, id string, args ...string) any {
		t.Helper()
		r := newRig(t)
		mustRun(t, r, args...)
		return r.f.only(t, id)
	}
	check := func(t *testing.T, what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the API received\n %+v\nwant\n %+v", what, got, want)
		}
	}

	t.Run("token-issuer", func(t *testing.T) {
		check(t, "list", call(t, "listTokenIssuers", "token-issuer", "list", "--tenant", tid, "--limit", "5", "--sort", "-name", "--name", "idp", "--enabled", "false"),
			adminapi.ListTokenIssuersInput{Tenant: tid, Limit: 5, Sort: adminapi.NamedSortMinusName, Name: "idp", Enabled: adminapi.EnabledFilterFalse})
		check(t, "create", call(t, "createTokenIssuer", "token-issuer", "create", "--tenant", tid, "--name", "idp", "--issuer", "https://idp.example.com", "--audience", "pandokos-acme",
			"--jwks-uri", "https://idp.example.com/keys", "--algorithm", "ES256", "--algorithm", "EdDSA", "--subject-claim", "azp", "--require", "tier=gold", "--require", "env=prod", "--description", "d"),
			adminapi.CreateTokenIssuerInput{Tenant: tid, Body: adminapi.TokenIssuerCreate{Name: "idp", Issuer: "https://idp.example.com", Audience: "pandokos-acme", JwksUri: "https://idp.example.com/keys",
				Algorithms: []string{"ES256", "EdDSA"}, SubjectClaim: "azp", RequiredClaims: map[string]string{"tier": "gold", "env": "prod"}, Description: "d", Enabled: true}})
		// The defaults are the server's: only what is given is sent. (Enabled is true by default.)
		r := newRig(t)
		mustRun(t, r, "token-issuer", "create", "--tenant", tid, "--name", "bare", "--issuer", "https://idp.example.com", "--audience", "a")
		var wire map[string]any
		if err := json.Unmarshal([]byte(r.f.postBody), &wire); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(wire, map[string]any{"name": "bare", "issuer": "https://idp.example.com", "audience": "a"}) && wire["enabled"] != true {
			t.Errorf("a creation with the required flags sends %s: nothing but the given members, or enabled true", r.f.postBody)
		}
		// --disabled puts enabled:false on the wire (the member defaults to true, so it must not be omitted).
		r = newRig(t)
		mustRun(t, r, "token-issuer", "create", "--tenant", tid, "--name", "off", "--issuer", "https://idp.example.com", "--audience", "a", "--disabled")
		if err := json.Unmarshal([]byte(r.f.postBody), &wire); err != nil || wire["enabled"] != false {
			t.Errorf("--disabled sends %s, want enabled:false on the wire (%v)", r.f.postBody, err)
		}
		check(t, "get", call(t, "getTokenIssuer", "token-issuer", "get", "--tenant", tid, issuerID), adminapi.GetTokenIssuerInput{Tenant: tid, Issuer: issuerID})
		check(t, "delete", call(t, "deleteTokenIssuer", "token-issuer", "delete", "--tenant", tid, issuerID), adminapi.DeleteTokenIssuerInput{Tenant: tid, Issuer: issuerID})
		check(t, "platform", call(t, "listTenantPlatformIssuers", "token-issuer", "platform", "--tenant", tid, "--limit", "3", "--sort", "-created_at"),
			adminapi.ListTenantPlatformIssuersInput{Tenant: tid, Limit: 3, Sort: adminapi.SortMinusCreatedAt})

		// update: only the flags given are sent; an empty value clears; --enable and --disable.
		check(t, "update one flag", call(t, "updateTokenIssuer", "token-issuer", "update", "--tenant", tid, "--audience", "other", issuerID),
			adminapi.UpdateTokenIssuerInput{Tenant: tid, Issuer: issuerID, Body: adminapi.TokenIssuerUpdate{Audience: adminapi.Some("other")}})
		check(t, "update everything", call(t, "updateTokenIssuer", "token-issuer", "update", "--tenant", tid, "--name", "idp2", "--issuer", "https://new.example.com", "--audience", "x",
			"--jwks-uri", "", "--algorithm", "RS256", "--subject-claim", "client_id", "--require", "a=b", "--description", "", "--disable", issuerID),
			adminapi.UpdateTokenIssuerInput{Tenant: tid, Issuer: issuerID, Body: adminapi.TokenIssuerUpdate{Name: adminapi.Some("idp2"), Issuer: adminapi.Some("https://new.example.com"),
				Audience: adminapi.Some("x"), JwksUri: adminapi.Some(""), Algorithms: adminapi.Some([]string{"RS256"}), SubjectClaim: adminapi.Some("client_id"),
				RequiredClaims: adminapi.Some(map[string]string{"a": "b"}), Description: adminapi.Some(""), Enabled: adminapi.Some(false)}})
		check(t, "enable", call(t, "updateTokenIssuer", "token-issuer", "update", "--tenant", tid, "--enable", issuerID),
			adminapi.UpdateTokenIssuerInput{Tenant: tid, Issuer: issuerID, Body: adminapi.TokenIssuerUpdate{Enabled: adminapi.Some(true)}})
		r = newRig(t)
		mustRun(t, r, "token-issuer", "update", "--tenant", tid, "--description", "", issuerID)
		if r.f.patchBody != `{"description":""}` {
			t.Errorf("update --description \"\" sends %s, want {\"description\":\"\"}", r.f.patchBody)
		}

		// A tenant by name is resolved like everywhere else.
		r = newRig(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		mustRun(t, r, "token-issuer", "list", "--tenant", "acme")
		check(t, "list by tenant name", r.f.only(t, "listTokenIssuers"), adminapi.ListTokenIssuersInput{Tenant: tid, Limit: 50, Sort: adminapi.NamedSortCreatedAt})

		// Usage errors reach no operation.
		r = newRig(t)
		for name, args := range map[string][]string{
			"create without a name":       {"token-issuer", "create", "--tenant", tid, "--issuer", "https://x.example.com", "--audience", "a"},
			"create without an issuer":    {"token-issuer", "create", "--tenant", tid, "--name", "x", "--audience", "a"},
			"create without an audience":  {"token-issuer", "create", "--tenant", tid, "--name", "x", "--issuer", "https://x.example.com"},
			"a require without an equals": {"token-issuer", "create", "--tenant", tid, "--name", "x", "--issuer", "https://x.example.com", "--audience", "a", "--require", "tier"},
			"update that changes nothing": {"token-issuer", "update", "--tenant", tid, issuerID},
			"both enable and disable":     {"token-issuer", "update", "--tenant", tid, "--enable", "--disable", issuerID},
			"get without an ID":           {"token-issuer", "get", "--tenant", tid},
			"delete with two IDs":         {"token-issuer", "delete", "--tenant", tid, issuerID, issuerID},
			"an unknown subcommand":       {"token-issuer", "frobnicate"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		for _, id := range []string{"createTokenIssuer", "updateTokenIssuer", "getTokenIssuer", "deleteTokenIssuer"} {
			if n := len(r.f.calls(id)); n != 0 {
				t.Errorf("a refused command reached %s %d times", id, n)
			}
		}
	})

	t.Run("subject-mapping", func(t *testing.T) {
		check(t, "list", call(t, "listSubjectMappings", "subject-mapping", "list", "--tenant", tid, "--issuer", issuerID, "--service-account", sid, "--subject", "spiffe://acme/ns/ci", "--limit", "2"),
			adminapi.ListSubjectMappingsInput{Tenant: tid, Issuer: issuerID, ServiceAccount: sid, Subject: "spiffe://acme/ns/ci", Limit: 2, Sort: adminapi.SortCreatedAt})
		check(t, "create", call(t, "createSubjectMapping", "subject-mapping", "create", "--tenant", tid, "--issuer", issuerID, "--subject", "spiffe://acme/ns/ci", "--service-account", sid, "--description", "ci"),
			adminapi.CreateSubjectMappingInput{Tenant: tid, Body: adminapi.SubjectMappingCreate{Issuer: adminapi.IssuerRef{Scope: adminapi.IssuerScopeTenant, ID: issuerID}, Subject: "spiffe://acme/ns/ci", ServiceAccountID: sid, Description: "ci"}})
		check(t, "create for a platform-level issuer", call(t, "createSubjectMapping", "subject-mapping", "create", "--tenant", tid, "--issuer", issuerID, "--platform", "--subject", "w", "--service-account", sid),
			adminapi.CreateSubjectMappingInput{Tenant: tid, Body: adminapi.SubjectMappingCreate{Issuer: adminapi.IssuerRef{Scope: adminapi.IssuerScopePlatform, ID: issuerID}, Subject: "w", ServiceAccountID: sid}})
		check(t, "get", call(t, "getSubjectMapping", "subject-mapping", "get", "--tenant", tid, mappingID), adminapi.GetSubjectMappingInput{Tenant: tid, Mapping: mappingID})
		check(t, "delete", call(t, "deleteSubjectMapping", "subject-mapping", "delete", "--tenant", tid, mappingID), adminapi.DeleteSubjectMappingInput{Tenant: tid, Mapping: mappingID})

		// A service account by name is resolved like the key commands do.
		r := newRig(t)
		r.f.out["listServiceAccounts"] = &adminapi.ServiceAccountList{Items: []adminapi.ServiceAccount{acct}}
		mustRun(t, r, "subject-mapping", "create", "--tenant", tid, "--issuer", issuerID, "--subject", "w", "--service-account", "bot")
		check(t, "create with a service account name", r.f.only(t, "createSubjectMapping"),
			adminapi.CreateSubjectMappingInput{Tenant: tid, Body: adminapi.SubjectMappingCreate{Issuer: adminapi.IssuerRef{Scope: adminapi.IssuerScopeTenant, ID: issuerID}, Subject: "w", ServiceAccountID: sid}})

		r = newRig(t)
		for name, args := range map[string][]string{
			"create without a subject":         {"subject-mapping", "create", "--tenant", tid, "--issuer", issuerID, "--service-account", sid},
			"create without an issuer":         {"subject-mapping", "create", "--tenant", tid, "--subject", "w", "--service-account", sid},
			"create without a service account": {"subject-mapping", "create", "--tenant", tid, "--issuer", issuerID, "--subject", "w"},
			"get without an ID":                {"subject-mapping", "get", "--tenant", tid},
			"an update does not exist":         {"subject-mapping", "update", "--tenant", tid, mappingID},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
		if n := len(r.f.calls("createSubjectMapping")); n != 0 {
			t.Errorf("a refused command reached createSubjectMapping %d times", n)
		}
	})

	t.Run("platform-issuer", func(t *testing.T) {
		check(t, "list", call(t, "listPlatformIssuers", "platform-issuer", "list", "--limit", "4", "--sort", "name", "--name", "shared", "--enabled", "true"),
			adminapi.ListPlatformIssuersInput{Limit: 4, Sort: adminapi.NamedSortName, Name: "shared", Enabled: adminapi.EnabledFilterTrue})
		check(t, "create", call(t, "createPlatformIssuer", "platform-issuer", "create", "--name", "shared", "--issuer", "https://idp.example.com", "--audience", "pandokos",
			"--tenant-claim", "org", "--algorithm", "PS256", "--subject-claim", "azp", "--require", "tier=gold", "--description", "d", "--jwks-uri", "https://idp.example.com/keys"),
			adminapi.CreatePlatformIssuerInput{Body: adminapi.PlatformIssuerCreate{Name: "shared", Issuer: "https://idp.example.com", Audience: "pandokos", TenantClaim: "org",
				Algorithms: []string{"PS256"}, SubjectClaim: "azp", RequiredClaims: map[string]string{"tier": "gold"}, Description: "d", JwksUri: "https://idp.example.com/keys", Enabled: true}})
		r := newRig(t)
		mustRun(t, r, "platform-issuer", "create", "--name", "off", "--issuer", "https://idp.example.com", "--audience", "a", "--tenant-claim", "org", "--disabled")
		var wire map[string]any
		if err := json.Unmarshal([]byte(r.f.postBody), &wire); err != nil || wire["enabled"] != false {
			t.Errorf("--disabled sends %s, want enabled:false on the wire (%v)", r.f.postBody, err)
		}
		check(t, "get", call(t, "getPlatformIssuer", "platform-issuer", "get", issuerID), adminapi.GetPlatformIssuerInput{Issuer: issuerID})
		check(t, "delete", call(t, "deletePlatformIssuer", "platform-issuer", "delete", issuerID), adminapi.DeletePlatformIssuerInput{Issuer: issuerID})
		check(t, "update", call(t, "updatePlatformIssuer", "platform-issuer", "update", "--tenant-claim", "org_id", "--disable", issuerID),
			adminapi.UpdatePlatformIssuerInput{Issuer: issuerID, Body: adminapi.PlatformIssuerUpdate{TenantClaim: adminapi.Some("org_id"), Enabled: adminapi.Some(false)}})
		check(t, "tenants", call(t, "listPlatformIssuerTenants", "platform-issuer", "tenants", "--limit", "2", "--sort", "created_at", issuerID),
			adminapi.ListPlatformIssuerTenantsInput{Issuer: issuerID, Limit: 2, Sort: adminapi.SortCreatedAt})
		check(t, "add-tenant", call(t, "putPlatformIssuerTenant", "platform-issuer", "add-tenant", issuerID, tid),
			adminapi.PutPlatformIssuerTenantInput{Issuer: issuerID, Tenant: tid})
		check(t, "add-tenant with a claim value", call(t, "putPlatformIssuerTenant", "platform-issuer", "add-tenant", "--claim-value", "org-7", issuerID, tid),
			adminapi.PutPlatformIssuerTenantInput{Issuer: issuerID, Tenant: tid, Body: adminapi.PlatformIssuerTenantPut{ClaimValue: "org-7"}})
		check(t, "remove-tenant", call(t, "deletePlatformIssuerTenant", "platform-issuer", "remove-tenant", issuerID, tid),
			adminapi.DeletePlatformIssuerTenantInput{Issuer: issuerID, Tenant: tid})
		// A tenant by name is resolved.
		r = newRig(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		mustRun(t, r, "platform-issuer", "add-tenant", issuerID, "acme")
		check(t, "add-tenant by name", r.f.only(t, "putPlatformIssuerTenant"), adminapi.PutPlatformIssuerTenantInput{Issuer: issuerID, Tenant: tid})

		r = newRig(t)
		for name, args := range map[string][]string{
			"create without a tenant claim": {"platform-issuer", "create", "--name", "x", "--issuer", "https://x.example.com", "--audience", "a"},
			"add-tenant without a tenant":   {"platform-issuer", "add-tenant", issuerID},
			"remove-tenant with one ID":     {"platform-issuer", "remove-tenant", issuerID},
			"update that changes nothing":   {"platform-issuer", "update", issuerID},
			"tenants without an issuer":     {"platform-issuer", "tenants"},
		} {
			if code, out, errs := r.run(t, args...); code != 2 || out != "" || errs == "" {
				t.Errorf("%s: exit %d, stdout %q, stderr %q, want 2", name, code, out, errs)
			}
		}
	})

	t.Run("the output is the API's", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getTokenIssuer"] = &tokenIssuer
		out, _ := mustRun(t, r, "token-issuer", "get", "-o", "json", "--tenant", tid, issuerID)
		var got adminapi.TokenIssuer
		if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, tokenIssuer) {
			t.Errorf("token-issuer get: %v\n%s", err, out)
		}
		r.f.out["getPlatformIssuer"] = &platformIssuer
		out, _ = mustRun(t, r, "platform-issuer", "get", "-o", "json", issuerID)
		var gotP adminapi.PlatformIssuer
		if err := json.Unmarshal([]byte(out), &gotP); err != nil || !reflect.DeepEqual(gotP, platformIssuer) {
			t.Errorf("platform-issuer get: %v\n%s", err, out)
		}
		r.f.out["getSubjectMapping"] = &mapping
		out, _ = mustRun(t, r, "subject-mapping", "get", "-o", "json", "--tenant", tid, mappingID)
		var gotM adminapi.SubjectMapping
		if err := json.Unmarshal([]byte(out), &gotM); err != nil || !reflect.DeepEqual(gotM, mapping) {
			t.Errorf("subject-mapping get: %v\n%s", err, out)
		}
		// A list in JSON is the items, as for every other list.
		r.f.out["listSubjectMappings"] = &adminapi.SubjectMappingList{Items: []adminapi.SubjectMapping{mapping}}
		out, _ = mustRun(t, r, "subject-mapping", "list", "-o", "json", "--tenant", tid)
		var list struct{ Items []adminapi.SubjectMapping }
		if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0], mapping) {
			t.Errorf("subject-mapping list: %v\n%s", err, out)
		}
		// In text, what identifies the resource is shown; there is nothing secret to hide.
		text, _ := mustRun(t, r, "token-issuer", "get", "--tenant", tid, issuerID)
		for _, want := range []string{issuerID, "idp", "https://idp.example.com", "pandokos-acme", "ES256"} {
			if !strings.Contains(text, want) {
				t.Errorf("the text output of token-issuer get lacks %q:\n%s", want, text)
			}
		}
		// A problem is reported as for every command.
		r = newRig(t)
		r.f.fail["createTokenIssuer"] = &problemErr{adminapi.Problem{Type: "urn:pandokos:problem:conflict", Title: "Conflict", Status: 409, Code: adminapi.ProblemCodeConflict,
			Detail: "The resource already exists.", RequestID: "req409xyz"}}
		code, out, errs := r.run(t, "token-issuer", "create", "--tenant", tid, "--name", "x", "--issuer", "https://x.example.com", "--audience", "a")
		if code != 1 || out != "" || errs != "error: conflict: The resource already exists. (request req409xyz)\n" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
}
