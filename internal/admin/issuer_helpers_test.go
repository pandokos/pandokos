package admin_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/apitest"
)

// issuerMutations are the mutating operations of sprint 4.1 that TestIssuerAudit (T-590) covers;
// the enumerating tests of earlier sprints (T-281, T-282) leave them to it.
var issuerMutations = map[string]bool{
	"createTokenIssuer": true, "updateTokenIssuer": true, "deleteTokenIssuer": true,
	"createSubjectMapping": true, "deleteSubjectMapping": true,
	"createPlatformIssuer": true, "updatePlatformIssuer": true, "deletePlatformIssuer": true,
	"putPlatformIssuerTenant": true, "deletePlatformIssuerTenant": true,
}

// iw is two tenants, each with an administrator and a service account, and the platform key.
type iw struct {
	api    *apitest.API
	plat   string
	a, b   adminapi.Tenant
	ka, kb string // administrator keys
	saA    adminapi.ServiceAccount
	saB    adminapi.ServiceAccount
}

func newIW(t *testing.T) *iw {
	t.Helper()
	w := &iw{api: apitest.New(t)}
	w.plat = w.api.PlatformKey()
	w.a, w.ka = w.api.Tenant(t, "issuers-a")
	w.b, w.kb = w.api.Tenant(t, "issuers-b")
	mk := func(ten adminapi.Tenant, key string) adminapi.ServiceAccount {
		sa, err := w.api.Client(key).CreateServiceAccount(ctx(t), adminapi.CreateServiceAccountInput{Tenant: ten.ID, Body: adminapi.ServiceAccountCreate{Name: "workload", Roles: []string{"inference"}}})
		if err != nil {
			t.Fatal(err)
		}
		return *sa
	}
	w.saA, w.saB = mk(w.a, w.ka), mk(w.b, w.kb)
	return w
}

func (w *iw) do(t *testing.T, key, method, path string, body any) *apitest.Response {
	t.Helper()
	return w.api.Do(t, method, path, key, body, nil)
}

// uniqueIssuer returns a body for a tenant-bound or platform-level registration with an issuer and
// an audience that are unique in the process.
func uniqueIssuer(extra map[string]any) map[string]any {
	n := apitest.UniqueName("idp")
	body := map[string]any{"name": n, "issuer": "https://" + n + ".example.test", "audience": "aud-" + n}
	for k, v := range extra {
		if v == nil {
			delete(body, k)
		} else {
			body[k] = v
		}
	}
	return body
}

func decodeInto[T any](t *testing.T, r *apitest.Response, status int) T {
	t.Helper()
	var v T
	ok(t, r, status, &v)
	return v
}

// sameProblem checks that two problem responses are the same but for the request ID.
func sameProblem(t *testing.T, what string, r1, r2 *apitest.Response) {
	t.Helper()
	norm := func(r *apitest.Response) string {
		p := problem(t, r)
		return strings.ReplaceAll(string(r.Body), p.RequestID, "ID")
	}
	if r1.Status != r2.Status || norm(r1) != norm(r2) {
		t.Errorf("%s: the answers differ:\n%d %s\n%d %s", what, r1.Status, norm(r1), r2.Status, norm(r2))
	}
}

// validationCases are the rules of section 5.3 that a registration of either kind follows: a body
// member, its value, and the field the 400 must name. base is a valid body; the case replaces a member.
type validationCase struct {
	what   string
	member string
	value  any
	field  string
}

func registrationValidation() []validationCase {
	long := func(n int, c string) string { return strings.Repeat(c, n) }
	var cases []validationCase
	add := func(what, member string, value any) {
		cases = append(cases, validationCase{what, member, value, member})
	}
	for _, v := range []string{"", "Idp", "-idp", "idp-", "has space", "idp_x", long(64, "a"), "idp/x", "é"} {
		add(fmt.Sprintf("name %q", v), "name", v)
	}
	for _, v := range []string{"", "http://idp.example.test", "ftp://idp.example.test", "idp.example.test", "https://", "https://idp.example.test/?a=b", "https://idp.example.test/#frag",
		"https://user@idp.example.test", "https://user:pw@idp.example.test", "https://idp.example.test/a b", "https://" + long(248, "a"), "//idp.example.test", "https:idp.example.test"} {
		add(fmt.Sprintf("issuer %q", clip(v)), "issuer", v)
	}
	for _, v := range []string{"", long(256, "a"), "a\nb", "a\x00b", "a\tb"} {
		add(fmt.Sprintf("audience %q", clip(v)), "audience", v)
	}
	for _, v := range []string{"http://idp.example.test/keys", "https://user:pw@idp.example.test/keys", "https://idp.example.test/keys#f", "/keys", "keys.json", "https://", "https://idp.example.test/" + long(2049, "a")} {
		add(fmt.Sprintf("jwks_uri %q", clip(v)), "jwks_uri", v)
	}
	for what, v := range map[string]any{
		"no algorithm": []string{}, "an unknown algorithm": []string{"RS1"}, "HS256": []string{"HS256"}, "none": []string{"none"}, "lower case": []string{"rs256"},
		"a duplicate": []string{"RS256", "RS256"}, "eleven": []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA", "RS256"}, "a number": []int{1},
		"a string": "RS256",
	} {
		add("algorithms: "+what, "algorithms", v)
	}
	for _, v := range []string{"a\x00b", "a\nb", "a\x1fb", "a\x7fb"} {
		add(fmt.Sprintf("description %q", v), "description", v)
		add(fmt.Sprintf("required_claims value %q", v), "required_claims", map[string]string{"x": v})
	}
	for _, v := range []string{"", "a b", long(65, "a"), "a@b", "a?b", "iss", "aud", "exp", "nbf", "iat"} {
		add(fmt.Sprintf("subject_claim %q", clip(v)), "subject_claim", v)
	}
	tooMany := map[string]string{}
	for i := 0; i < 17; i++ {
		tooMany[fmt.Sprintf("c%d", i)] = "v"
	}
	for what, v := range map[string]any{
		"17 members": tooMany, "an invalid name": map[string]string{"a b": "v"}, "a reserved name iss": map[string]string{"iss": "x"}, "a reserved name aud": map[string]string{"aud": "x"},
		"a reserved name exp": map[string]string{"exp": "x"}, "a reserved name nbf": map[string]string{"nbf": "x"}, "a reserved name iat": map[string]string{"iat": "x"},
		"an empty value": map[string]string{"tier": ""}, "a value of 256 characters": map[string]string{"tier": long(256, "v")}, "a number as value": map[string]any{"tier": 1},
		"an array as value": map[string]any{"tier": []string{"a"}}, "an array": []string{"a"}, "a name of 65 characters": map[string]string{long(65, "c"): "v"},
	} {
		add("required_claims: "+what, "required_claims", v)
	}
	add("description of 201 characters", "description", long(201, "d"))
	add("enabled as a string", "enabled", "yes")
	return cases
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// limitMutations are the mutating operations of sprint 5.2 that TestLimitAudit (T-677) covers, with
// their atomicity; the enumerating tests of earlier sprints (T-281, T-282) leave them to it, as they
// leave the token issuer operations to TestIssuerAudit.
var limitMutations = map[string]bool{
	"createLimit": true, "updateLimit": true, "deleteLimit": true,
	"createPlatformLimit": true, "updatePlatformLimit": true, "deletePlatformLimit": true,
}

// peopleMutations are the mutating operations of sprint 4.2 (sign-in, own sessions, users and
// members) that the audit tests of that sprint cover (T-796, T-826), with their atomicity; the
// enumerating tests of earlier sprints (T-281, T-282) leave them to those, as they leave the token
// issuer and limit operations to theirs.
var peopleMutations = map[string]bool{
	"signIn": true, "signOut": true, "changePassword": true, "deleteOwnSession": true,
	"createUser": true, "updateUser": true, "disableUser": true, "enableUser": true, "unlockUser": true, "resetUserPassword": true, "revokeUserSessions": true,
	"createMember": true, "updateMember": true, "deleteMember": true, "disableMember": true, "enableMember": true, "unlockMember": true, "resetMemberPassword": true, "revokeMemberSessions": true,
}
