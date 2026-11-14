package cli_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/cli"
	"github.com/pandokos/pandokos/test/testcert"
	"github.com/pandokos/pandokos/test/testutil"
)

const (
	tid  = "0192f0f0-0000-7000-8000-000000000001"
	sid  = "0192f0f0-0000-7000-8000-000000000002"
	sid2 = "0192f0f0-0000-7000-8000-000000000003"
	vk   = "tabcdefghijklmnop"
	pk   = "pabcdefghijklmnop"
	cid  = "0192f0f0-0000-7000-8000-000000000004"
)

// fakeAPI is an adminapi.Server that records the inputs, returns canned outputs and can fail.
type fakeAPI struct {
	adminapi.Server // the catalogue operations are exercised from sprint 2.2 on; calling one here panics
	mu              sync.Mutex
	got             map[string][]any
	out             map[string]any
	fail            map[string]error
	// pages answers listTenants by cursor.
	pages map[string]adminapi.TenantList
	// patchBody is the raw body of the last PATCH: the generated types cannot tell an absent
	// member from an empty one, the wire can.
	patchBody string
}

func newFake() *fakeAPI {
	return &fakeAPI{got: map[string][]any{}, out: map[string]any{}, fail: map[string]error{}}
}

func (f *fakeAPI) rec(id string, in any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got[id] = append(f.got[id], in)
	return f.fail[id]
}

func (f *fakeAPI) calls(id string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.got[id]...)
}

func (f *fakeAPI) only(t *testing.T, id string) any {
	t.Helper()
	c := f.calls(id)
	if len(c) != 1 {
		t.Fatalf("operation %s was called %d times, want once", id, len(c))
	}
	return c[0]
}

func pick[T any](f *fakeAPI, id string) *T {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.out[id].(*T); ok {
		return v
	}
	return new(T)
}

func (f *fakeAPI) GetMe(_ context.Context, in adminapi.GetMeInput) (*adminapi.Me, error) {
	return pick[adminapi.Me](f, "getMe"), f.rec("getMe", in)
}
func (f *fakeAPI) ListTenants(_ context.Context, in adminapi.ListTenantsInput) (*adminapi.TenantList, error) {
	err := f.rec("listTenants", in)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pages != nil {
		p := f.pages[in.Cursor]
		return &p, err
	}
	if v, ok := f.out["listTenants"].(*adminapi.TenantList); ok {
		return v, err
	}
	return &adminapi.TenantList{}, err
}
func (f *fakeAPI) CreateTenant(_ context.Context, in adminapi.CreateTenantInput) (*adminapi.TenantCreated, error) {
	return pick[adminapi.TenantCreated](f, "createTenant"), f.rec("createTenant", in)
}
func (f *fakeAPI) GetTenant(_ context.Context, in adminapi.GetTenantInput) (*adminapi.Tenant, error) {
	return pick[adminapi.Tenant](f, "getTenant"), f.rec("getTenant", in)
}
func (f *fakeAPI) UpdateTenant(_ context.Context, in adminapi.UpdateTenantInput) (*adminapi.Tenant, error) {
	return pick[adminapi.Tenant](f, "updateTenant"), f.rec("updateTenant", in)
}
func (f *fakeAPI) SuspendTenant(_ context.Context, in adminapi.SuspendTenantInput) (*adminapi.Tenant, error) {
	return pick[adminapi.Tenant](f, "suspendTenant"), f.rec("suspendTenant", in)
}
func (f *fakeAPI) ReactivateTenant(_ context.Context, in adminapi.ReactivateTenantInput) (*adminapi.Tenant, error) {
	return pick[adminapi.Tenant](f, "reactivateTenant"), f.rec("reactivateTenant", in)
}
func (f *fakeAPI) ListServiceAccounts(_ context.Context, in adminapi.ListServiceAccountsInput) (*adminapi.ServiceAccountList, error) {
	return pick[adminapi.ServiceAccountList](f, "listServiceAccounts"), f.rec("listServiceAccounts", in)
}
func (f *fakeAPI) CreateServiceAccount(_ context.Context, in adminapi.CreateServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return pick[adminapi.ServiceAccount](f, "createServiceAccount"), f.rec("createServiceAccount", in)
}
func (f *fakeAPI) GetServiceAccount(_ context.Context, in adminapi.GetServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return pick[adminapi.ServiceAccount](f, "getServiceAccount"), f.rec("getServiceAccount", in)
}
func (f *fakeAPI) UpdateServiceAccount(_ context.Context, in adminapi.UpdateServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return pick[adminapi.ServiceAccount](f, "updateServiceAccount"), f.rec("updateServiceAccount", in)
}
func (f *fakeAPI) DisableServiceAccount(_ context.Context, in adminapi.DisableServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return pick[adminapi.ServiceAccount](f, "disableServiceAccount"), f.rec("disableServiceAccount", in)
}
func (f *fakeAPI) EnableServiceAccount(_ context.Context, in adminapi.EnableServiceAccountInput) (*adminapi.ServiceAccount, error) {
	return pick[adminapi.ServiceAccount](f, "enableServiceAccount"), f.rec("enableServiceAccount", in)
}
func (f *fakeAPI) ListVirtualKeys(_ context.Context, in adminapi.ListVirtualKeysInput) (*adminapi.VirtualKeyList, error) {
	return pick[adminapi.VirtualKeyList](f, "listVirtualKeys"), f.rec("listVirtualKeys", in)
}
func (f *fakeAPI) CreateVirtualKey(_ context.Context, in adminapi.CreateVirtualKeyInput) (*adminapi.VirtualKeyCreated, error) {
	return pick[adminapi.VirtualKeyCreated](f, "createVirtualKey"), f.rec("createVirtualKey", in)
}
func (f *fakeAPI) GetVirtualKey(_ context.Context, in adminapi.GetVirtualKeyInput) (*adminapi.VirtualKey, error) {
	return pick[adminapi.VirtualKey](f, "getVirtualKey"), f.rec("getVirtualKey", in)
}
func (f *fakeAPI) RevokeVirtualKey(_ context.Context, in adminapi.RevokeVirtualKeyInput) (*adminapi.VirtualKey, error) {
	return pick[adminapi.VirtualKey](f, "revokeVirtualKey"), f.rec("revokeVirtualKey", in)
}
func (f *fakeAPI) ListPlatformKeys(_ context.Context, in adminapi.ListPlatformKeysInput) (*adminapi.PlatformKeyList, error) {
	return pick[adminapi.PlatformKeyList](f, "listPlatformKeys"), f.rec("listPlatformKeys", in)
}
func (f *fakeAPI) CreatePlatformKey(_ context.Context, in adminapi.CreatePlatformKeyInput) (*adminapi.PlatformKeyCreated, error) {
	return pick[adminapi.PlatformKeyCreated](f, "createPlatformKey"), f.rec("createPlatformKey", in)
}
func (f *fakeAPI) GetPlatformKey(_ context.Context, in adminapi.GetPlatformKeyInput) (*adminapi.PlatformKey, error) {
	return pick[adminapi.PlatformKey](f, "getPlatformKey"), f.rec("getPlatformKey", in)
}
func (f *fakeAPI) RevokePlatformKey(_ context.Context, in adminapi.RevokePlatformKeyInput) (*adminapi.PlatformKey, error) {
	return pick[adminapi.PlatformKey](f, "revokePlatformKey"), f.rec("revokePlatformKey", in)
}
func (f *fakeAPI) ListTenantCredentials(_ context.Context, in adminapi.ListTenantCredentialsInput) (*adminapi.CredentialList, error) {
	return pick[adminapi.CredentialList](f, "listTenantCredentials"), f.rec("listTenantCredentials", in)
}
func (f *fakeAPI) GetTenantCredential(_ context.Context, in adminapi.GetTenantCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "getTenantCredential"), f.rec("getTenantCredential", in)
}
func (f *fakeAPI) DisableTenantCredential(_ context.Context, in adminapi.DisableTenantCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "disableTenantCredential"), f.rec("disableTenantCredential", in)
}
func (f *fakeAPI) RevokeTenantCredential(_ context.Context, in adminapi.RevokeTenantCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "revokeTenantCredential"), f.rec("revokeTenantCredential", in)
}
func (f *fakeAPI) ListCredentials(_ context.Context, in adminapi.ListCredentialsInput) (*adminapi.CredentialList, error) {
	return pick[adminapi.CredentialList](f, "listCredentials"), f.rec("listCredentials", in)
}
func (f *fakeAPI) CreateCredential(_ context.Context, in adminapi.CreateCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "createCredential"), f.rec("createCredential", in)
}
func (f *fakeAPI) GetCredential(_ context.Context, in adminapi.GetCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "getCredential"), f.rec("getCredential", in)
}
func (f *fakeAPI) UpdateCredential(_ context.Context, in adminapi.UpdateCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "updateCredential"), f.rec("updateCredential", in)
}
func (f *fakeAPI) DeleteCredential(_ context.Context, in adminapi.DeleteCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "deleteCredential"), f.rec("deleteCredential", in)
}
func (f *fakeAPI) DisableCredential(_ context.Context, in adminapi.DisableCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "disableCredential"), f.rec("disableCredential", in)
}
func (f *fakeAPI) EnableCredential(_ context.Context, in adminapi.EnableCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "enableCredential"), f.rec("enableCredential", in)
}
func (f *fakeAPI) RevokeCredential(_ context.Context, in adminapi.RevokeCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "revokeCredential"), f.rec("revokeCredential", in)
}
func (f *fakeAPI) ReplaceCredentialSecret(_ context.Context, in adminapi.ReplaceCredentialSecretInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "replaceCredentialSecret"), f.rec("replaceCredentialSecret", in)
}
func (f *fakeAPI) SetDefaultCredential(_ context.Context, in adminapi.SetDefaultCredentialInput) (*adminapi.Credential, error) {
	return pick[adminapi.Credential](f, "setDefaultCredential"), f.rec("setDefaultCredential", in)
}

var _ adminapi.Server = (*fakeAPI)(nil)

type problemErr struct{ p adminapi.Problem }

func (e *problemErr) Error() string { return string(e.p.Code) }

// handler answers with the fake. header, when set, is added to every response; auth records the
// Authorization header of every request.
func handler(f *fakeAPI, header http.Header, auth *[]string) http.Handler {
	h := adminapi.NewHandler(f, adminapi.Hooks{
		Before: func(ctx context.Context, r *http.Request, _ *adminapi.Operation, _ map[string]string) (context.Context, error) {
			if auth != nil {
				*auth = append(*auth, r.Header.Get("Authorization"))
			}
			return ctx, nil
		},
		Error: func(w http.ResponseWriter, _ *http.Request, _ *adminapi.Operation, err error) {
			p := adminapi.Problem{Type: "urn:pandokos:problem:internal", Title: "Internal error", Status: 500, Code: adminapi.ProblemCodeInternal, Detail: "Internal error.", RequestID: "req500"}
			var pe *problemErr
			var re *adminapi.RequestError
			switch {
			case errorsAs(err, &pe):
				p = pe.p
			case errorsAs(err, &re):
				p = adminapi.Problem{Type: "urn:pandokos:problem:" + string(re.Code), Title: "Invalid request", Status: re.Status, Code: re.Code, Detail: "The request does not match the API description.", RequestID: "req400", Errors: re.Fields}
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(p.Status)
			_ = json.NewEncoder(w).Encode(p)
		},
		MaxBodyBytes: 1 << 20,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.patchBody = string(b)
			f.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		h.ServeHTTP(w, r)
	})
}

func serve(t *testing.T, f *fakeAPI, header http.Header, auth *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler(f, header, auth))
	t.Cleanup(srv.Close)
	return srv
}

func errorsAs[T any](err error, target *T) bool {
	for err != nil {
		if v, ok := err.(T); ok { //nolint:errorlint // a hand-rolled As for the generic target
			*target = v
			return true
		}
		u, ok := err.(interface{ Unwrap() error }) //nolint:errorlint // see above
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

type rig struct {
	f       *fakeAPI
	srv     *httptest.Server
	key     string
	keyFile string
	auth    []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{f: newFake(), key: testutil.NewKey(t)}
	r.srv = serve(t, r.f, nil, &r.auth)
	r.keyFile = testutil.WriteSecret(t, "key", r.key)
	return r
}

func (r *rig) environ() []string {
	return []string{"PANDOKOS_CLI_SERVER=" + r.srv.URL, "PANDOKOS_CLI_KEY_FILE=" + r.keyFile}
}

func (r *rig) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runWith(t, r.environ(), args...)
}

func runWith(t *testing.T, environ []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code = cli.Run(ctx, args, &out, &errb, environ)
	return code, out.String(), errb.String()
}

func mustRun(t *testing.T, r *rig, args ...string) (stdout, stderr string) {
	t.Helper()
	code, out, errs := r.run(t, args...)
	if code != 0 {
		t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errs)
	}
	return out, errs
}

var (
	t0     = time.Date(2026, 11, 13, 9, 4, 5, 0, time.UTC)
	t1     = time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	tenant = adminapi.Tenant{ID: tid, Name: "acme", DisplayName: "Acme Inc", State: adminapi.TenantStateActive, CreatedAt: t0, UpdatedAt: t0, StateChangedAt: t0}
	acct   = adminapi.ServiceAccount{ID: sid, TenantID: tid, Name: "bot", Description: "a bot", State: adminapi.ServiceAccountStateActive, Roles: []string{"inference"}, CreatedAt: t0, UpdatedAt: t0}
	vkey   = adminapi.VirtualKey{ID: vk, TenantID: tid, ServiceAccountID: sid, Description: "ci", State: adminapi.KeyStateActive, CreatedAt: t0}
	pkey   = adminapi.PlatformKey{ID: pk, Description: "ops", Role: "platform-admin", State: adminapi.KeyStateActive, CreatedAt: t0}
	cred   = adminapi.Credential{ID: cid, TenantID: tid, Provider: "openai", Label: "main", Description: "the main one", Owner: adminapi.CredentialOwnerTenant,
		State: adminapi.CredentialStateActive, IsDefault: true, Fingerprint: "0123456789abcdef0123456789abcdef", Hint: "wxyz",
		StateChangedAt: t0, StateChangedBy: adminapi.CredentialActorTenant, CreatedAt: t0}
)

func lineWith(text, prefix string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// TestCommands is T-217.
func TestCommands(t *testing.T) {
	t.Run("whoami", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getMe"] = &adminapi.Me{PrincipalType: adminapi.PrincipalTypePlatform, ID: pk, Name: "ops", KeyID: pk, Roles: []string{"platform-admin"}, Permissions: []string{"tenant:read"}}
		out, _ := mustRun(t, r, "whoami")
		if !reflect.DeepEqual(r.f.only(t, "getMe"), adminapi.GetMeInput{}) {
			t.Error("getMe input")
		}
		for _, want := range []string{"principal_type: platform", "name: ops", "key_id: " + pk} {
			if !lineWith(out, want) {
				t.Errorf("stdout lacks the line %q:\n%s", want, out)
			}
		}
	})

	t.Run("tenant commands", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getTenant"] = &tenant
		r.f.out["updateTenant"] = &tenant
		r.f.out["suspendTenant"] = &tenant
		r.f.out["reactivateTenant"] = &tenant
		mustRun(t, r, "tenant", "get", tid)
		if got := r.f.only(t, "getTenant"); !reflect.DeepEqual(got, adminapi.GetTenantInput{Tenant: tid}) {
			t.Errorf("get: %+v", got)
		}
		out, _ := mustRun(t, r, "tenant", "update", "--name", "acme2", "--display-name", "Acme Two", tid)
		if got := r.f.only(t, "updateTenant"); !reflect.DeepEqual(got, adminapi.UpdateTenantInput{Tenant: tid, Body: adminapi.TenantUpdate{Name: adminapi.Some("acme2"), DisplayName: adminapi.Some("Acme Two")}}) {
			t.Errorf("update: %+v", got)
		}
		for _, want := range []string{"id: " + tid, "name: acme", "display_name: Acme Inc", "state: active"} {
			if !lineWith(out, want) {
				t.Errorf("a resource is printed as name: value lines; %q is missing:\n%s", want, out)
			}
		}
		mustRun(t, r, "tenant", "suspend", "--reason", "invoice unpaid", tid)
		if got := r.f.only(t, "suspendTenant"); !reflect.DeepEqual(got, adminapi.SuspendTenantInput{Tenant: tid, Body: adminapi.Reason{Reason: "invoice unpaid"}}) {
			t.Errorf("suspend: %+v", got)
		}
		mustRun(t, r, "tenant", "reactivate", tid)
		if got := r.f.only(t, "reactivateTenant"); !reflect.DeepEqual(got, adminapi.ReactivateTenantInput{Tenant: tid}) {
			t.Errorf("reactivate: %+v", got)
		}
		// Flags may also follow the argument.
		r.f.got = map[string][]any{}
		mustRun(t, r, "tenant", "suspend", tid, "--reason", "after")
		if got := r.f.only(t, "suspendTenant"); !reflect.DeepEqual(got, adminapi.SuspendTenantInput{Tenant: tid, Body: adminapi.Reason{Reason: "after"}}) {
			t.Errorf("suspend with a trailing flag: %+v", got)
		}
	})

	t.Run("a tenant name is resolved", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		r.f.out["getTenant"] = &tenant
		mustRun(t, r, "tenant", "get", "acme")
		if got := r.f.only(t, "listTenants"); !reflect.DeepEqual(got, adminapi.ListTenantsInput{Name: "acme", Limit: 50, Sort: adminapi.NamedSortCreatedAt}) {
			t.Errorf("resolution: %+v", got)
		}
		if got := r.f.only(t, "getTenant"); !reflect.DeepEqual(got, adminapi.GetTenantInput{Tenant: tid}) {
			t.Errorf("get: %+v", got)
		}
		// An ID is not looked up.
		r2 := newRig(t)
		mustRun(t, r2, "tenant", "get", tid)
		if len(r2.f.calls("listTenants")) != 0 {
			t.Error("an ID was resolved as a name")
		}
		// A name that matches nothing.
		r3 := newRig(t)
		code, _, errs := r3.run(t, "tenant", "get", "nobody")
		if code != 1 || !strings.Contains(errs, "error: ") || !strings.Contains(errs, "nobody") || len(r3.f.calls("getTenant")) != 0 {
			t.Errorf("unknown name: exit %d, stderr %q", code, errs)
		}
	})

	t.Run("tenant list and create", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		out, _ := mustRun(t, r, "tenant", "list", "--state", "active", "--name", "acme", "--sort", "-name", "--limit", "5")
		if got := r.f.only(t, "listTenants"); !reflect.DeepEqual(got, adminapi.ListTenantsInput{State: adminapi.TenantStateActive, Name: "acme", Sort: adminapi.NamedSortMinusName, Limit: 5}) {
			t.Errorf("list: %+v", got)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 || !strings.Contains(strings.ToUpper(lines[0]), "NAME") || !strings.Contains(lines[1], "acme") || !strings.Contains(lines[1], tid) {
			t.Errorf("a list is a table with a header:\n%s", out)
		}

		r = newRig(t)
		secret := testutil.NewKey(t)
		r.f.out["createTenant"] = &adminapi.TenantCreated{
			Tenant: tenant, AdminServiceAccount: acct,
			AdminKey: adminapi.VirtualKeyCreated{Key: vkey, Secret: secret},
		}
		out, _ = mustRun(t, r, "tenant", "create", "--display-name", "Acme Inc", "--admin-name", "root", "--admin-key-expires-at", t1.Format(time.RFC3339), "acme")
		want := adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "acme", DisplayName: "Acme Inc", AdminServiceAccountName: "root", AdminKeyExpiresAt: &t1}}
		if got := r.f.only(t, "createTenant"); !reflect.DeepEqual(got, want) {
			t.Errorf("create: %+v", got)
		}
		if strings.Count(out, secret) != 1 || !lineWith(out, "key:     "+secret) {
			t.Errorf("the key is printed once, as the line %q:\n%s", "key:     <key>", out)
		}
		if !lineWith(out, "name: acme") {
			t.Errorf("the tenant is printed before the key:\n%s", out)
		}
	})

	t.Run("--all follows the cursors and a page ends with a hint", func(t *testing.T) {
		r := newRig(t)
		t2, t3 := tenant, tenant
		t2.Name, t3.Name = "second", "third"
		r.f.pages = map[string]adminapi.TenantList{
			"":   {Items: []adminapi.Tenant{tenant}, NextCursor: "c1"},
			"c1": {Items: []adminapi.Tenant{t2}, NextCursor: "c2"},
			"c2": {Items: []adminapi.Tenant{t3}},
		}
		out, errs := mustRun(t, r, "tenant", "list", "--all")
		if n := len(r.f.calls("listTenants")); n != 3 {
			t.Fatalf("%d pages were requested, want 3", n)
		}
		if got := r.f.calls("listTenants")[1].(adminapi.ListTenantsInput).Cursor; got != "c1" {
			t.Errorf("second page cursor %q", got)
		}
		for _, n := range []string{"acme", "second", "third"} {
			if !strings.Contains(out, n) {
				t.Errorf("%s is missing:\n%s", n, out)
			}
		}
		if strings.Count(out, "NAME")+strings.Count(out, "Name")+strings.Count(out, "name") < 1 || len(strings.Split(strings.TrimSpace(out), "\n")) != 4 {
			t.Errorf("one header and three rows expected:\n%s", out)
		}
		if strings.Contains(errs, "more:") {
			t.Errorf("--all printed a hint: %q", errs)
		}

		r = newRig(t)
		r.f.pages = map[string]adminapi.TenantList{"": {Items: []adminapi.Tenant{tenant}, NextCursor: "c1"}, "c1": {Items: []adminapi.Tenant{t2}}}
		_, errs = mustRun(t, r, "tenant", "list")
		if n := len(r.f.calls("listTenants")); n != 1 || !strings.Contains(errs, "more: --cursor c1") {
			t.Errorf("%d pages, stderr %q", n, errs)
		}
		mustRun(t, r, "tenant", "list", "--cursor", "c1")
		if got := r.f.calls("listTenants")[1].(adminapi.ListTenantsInput).Cursor; got != "c1" {
			t.Errorf("--cursor was not passed: %q", got)
		}
	})

	t.Run("service accounts", func(t *testing.T) {
		r := newRig(t)
		r.f.out["listServiceAccounts"] = &adminapi.ServiceAccountList{Items: []adminapi.ServiceAccount{acct}}
		r.f.out["createServiceAccount"] = &acct
		r.f.out["getServiceAccount"] = &acct
		r.f.out["updateServiceAccount"] = &acct
		r.f.out["disableServiceAccount"] = &acct
		r.f.out["enableServiceAccount"] = &acct
		mustRun(t, r, "service-account", "list", "--tenant", tid, "--state", "disabled", "--name", "bot", "--sort", "name", "--limit", "3")
		if got := r.f.only(t, "listServiceAccounts"); !reflect.DeepEqual(got, adminapi.ListServiceAccountsInput{Tenant: tid, State: adminapi.ServiceAccountStateDisabled, Name: "bot", Sort: adminapi.NamedSortName, Limit: 3}) {
			t.Errorf("list: %+v", got)
		}
		mustRun(t, r, "service-account", "create", "--tenant", tid, "--description", "a bot", "--role", "inference", "--role", "tenant-admin",
			"--valid-from", t0.Format(time.RFC3339), "--valid-until", t1.Format(time.RFC3339), "bot")
		want := adminapi.CreateServiceAccountInput{Tenant: tid, Body: adminapi.ServiceAccountCreate{Name: "bot", Description: "a bot", ValidFrom: &t0, ValidUntil: &t1, Roles: []string{"inference", "tenant-admin"}}}
		if got := r.f.only(t, "createServiceAccount"); !reflect.DeepEqual(got, want) {
			t.Errorf("create: %+v", got)
		}
		mustRun(t, r, "service-account", "get", "--tenant", tid, sid)
		if got := r.f.only(t, "getServiceAccount"); !reflect.DeepEqual(got, adminapi.GetServiceAccountInput{Tenant: tid, ServiceAccount: sid}) {
			t.Errorf("get: %+v", got)
		}
		mustRun(t, r, "service-account", "update", "--tenant", tid, "--name", "bot2", "--description", "d2", "--role", "inference", "--valid-from", t0.Format(time.RFC3339), "--no-valid-until", sid)
		wantU := adminapi.UpdateServiceAccountInput{Tenant: tid, ServiceAccount: sid, Body: adminapi.ServiceAccountUpdate{
			Name: adminapi.Some("bot2"), Description: adminapi.Some("d2"), Roles: adminapi.Some([]string{"inference"}), ValidFrom: adminapi.Value(t0), ValidUntil: adminapi.Null[time.Time]()}}
		if got := r.f.only(t, "updateServiceAccount"); !reflect.DeepEqual(got, wantU) {
			t.Errorf("update: %+v", got)
		}
		r.f.got = map[string][]any{}
		mustRun(t, r, "service-account", "update", "--tenant", tid, "--no-roles", "--no-valid-from", sid)
		got := r.f.only(t, "updateServiceAccount").(adminapi.UpdateServiceAccountInput)
		if !got.Body.Roles.Set || len(got.Body.Roles.Value) != 0 {
			t.Errorf("--no-roles must send an empty array, not leave the member out: %+v", got.Body.Roles)
		}
		if !got.Body.ValidFrom.Set || !got.Body.ValidFrom.Null || got.Body.ValidUntil.Set {
			t.Errorf("--no-valid-from: %+v", got.Body)
		}
		raw, _ := json.Marshal(got.Body)
		if !strings.Contains(string(raw), `"roles":[]`) {
			// The JSON of the request is what matters to the server.
			t.Logf("body: %s", raw)
		}
		mustRun(t, r, "service-account", "disable", "--tenant", tid, "--reason", "left", sid)
		if got := r.f.only(t, "disableServiceAccount"); !reflect.DeepEqual(got, adminapi.DisableServiceAccountInput{Tenant: tid, ServiceAccount: sid, Body: adminapi.Reason{Reason: "left"}}) {
			t.Errorf("disable: %+v", got)
		}
		mustRun(t, r, "service-account", "enable", "--tenant", tid, sid)
		if got := r.f.only(t, "enableServiceAccount"); !reflect.DeepEqual(got, adminapi.EnableServiceAccountInput{Tenant: tid, ServiceAccount: sid}) {
			t.Errorf("enable: %+v", got)
		}
		for _, conflict := range [][]string{
			{"--no-roles", "--role", "inference"}, {"--no-valid-from", "--valid-from", t0.Format(time.RFC3339)}, {"--no-valid-until", "--valid-until", t1.Format(time.RFC3339)},
		} {
			n := len(r.f.calls("updateServiceAccount"))
			code, _, errs := r.run(t, append([]string{"service-account", "update", "--tenant", tid}, append(conflict, sid)...)...)
			if code != 2 || errs == "" || len(r.f.calls("updateServiceAccount")) != n {
				t.Errorf("%v: exit %d, stderr %q", conflict, code, errs)
			}
		}
		code, _, errs := r.run(t, "service-account", "update", "--tenant", tid, sid)
		if code != 2 || errs == "" {
			t.Errorf("an update that changes nothing is a usage error: %d %q", code, errs)
		}
	})

	t.Run("a service account name is resolved and the tenant defaults to the caller's", func(t *testing.T) {
		r := newRig(t)
		r.f.out["getMe"] = &adminapi.Me{PrincipalType: adminapi.PrincipalTypeServiceAccount, ID: sid, Name: "admin", KeyID: vk, Tenant: &adminapi.TenantRef{ID: tid, Name: "acme", State: adminapi.TenantStateActive}}
		r.f.out["listServiceAccounts"] = &adminapi.ServiceAccountList{Items: []adminapi.ServiceAccount{acct}}
		r.f.out["getServiceAccount"] = &acct
		mustRun(t, r, "service-account", "get", "bot")
		if got := r.f.only(t, "getMe"); !reflect.DeepEqual(got, adminapi.GetMeInput{}) {
			t.Errorf("getMe: %+v", got)
		}
		if got := r.f.only(t, "listServiceAccounts"); !reflect.DeepEqual(got, adminapi.ListServiceAccountsInput{Tenant: tid, Name: "bot", Limit: 50, Sort: adminapi.NamedSortCreatedAt}) {
			t.Errorf("resolution: %+v", got)
		}
		if got := r.f.only(t, "getServiceAccount"); !reflect.DeepEqual(got, adminapi.GetServiceAccountInput{Tenant: tid, ServiceAccount: sid}) {
			t.Errorf("get: %+v", got)
		}
		// A tenant given by name is resolved with a platform key.
		r = newRig(t)
		r.f.out["listTenants"] = &adminapi.TenantList{Items: []adminapi.Tenant{tenant}}
		mustRun(t, r, "service-account", "list", "--tenant", "acme")
		if got := r.f.only(t, "listServiceAccounts"); !reflect.DeepEqual(got, adminapi.ListServiceAccountsInput{Tenant: tid, Limit: 50, Sort: adminapi.NamedSortCreatedAt}) {
			t.Errorf("list: %+v", got)
		}
	})

	t.Run("keys", func(t *testing.T) {
		r := newRig(t)
		secret := testutil.NewKey(t)
		r.f.out["listVirtualKeys"] = &adminapi.VirtualKeyList{Items: []adminapi.VirtualKey{vkey}}
		r.f.out["createVirtualKey"] = &adminapi.VirtualKeyCreated{Key: vkey, Secret: secret}
		r.f.out["getVirtualKey"] = &vkey
		r.f.out["revokeVirtualKey"] = &vkey
		mustRun(t, r, "key", "list", "--tenant", tid, "--service-account", sid, "--state", "revoked", "--sort", "-created_at", "--limit", "9")
		if got := r.f.only(t, "listVirtualKeys"); !reflect.DeepEqual(got, adminapi.ListVirtualKeysInput{Tenant: tid, ServiceAccountID: sid, State: adminapi.KeyStateRevoked, Sort: adminapi.SortMinusCreatedAt, Limit: 9}) {
			t.Errorf("list: %+v", got)
		}
		out, _ := mustRun(t, r, "key", "create", "--tenant", tid, "--service-account", sid, "--description", "ci", "--scope", "model:invoke", "--scope", "virtual_key:read", "--expires-at", t1.Format(time.RFC3339))
		want := adminapi.CreateVirtualKeyInput{Tenant: tid, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sid, Description: "ci", Scopes: []string{"model:invoke", "virtual_key:read"}, ExpiresAt: &t1}}
		if got := r.f.only(t, "createVirtualKey"); !reflect.DeepEqual(got, want) {
			t.Errorf("create: %+v", got)
		}
		if strings.Count(out, secret) != 1 || !lineWith(out, "key:     "+secret) {
			t.Errorf("the key is printed once:\n%s", out)
		}
		mustRun(t, r, "key", "get", "--tenant", tid, vk)
		if got := r.f.only(t, "getVirtualKey"); !reflect.DeepEqual(got, adminapi.GetVirtualKeyInput{Tenant: tid, Key: vk}) {
			t.Errorf("get: %+v", got)
		}
		mustRun(t, r, "key", "revoke", "--tenant", tid, "--reason", "leaked", vk)
		if got := r.f.only(t, "revokeVirtualKey"); !reflect.DeepEqual(got, adminapi.RevokeVirtualKeyInput{Tenant: tid, Key: vk, Body: adminapi.Reason{Reason: "leaked"}}) {
			t.Errorf("revoke: %+v", got)
		}
		if code, _, errs := r.run(t, "key", "create", "--tenant", tid); code != 2 || !strings.Contains(errs, "service-account") {
			t.Errorf("--service-account is required: %d %q", code, errs)
		}
		// T-311: key create --credential pins the key to a credential.
		r.f.got = map[string][]any{}
		mustRun(t, r, "key", "create", "--tenant", tid, "--service-account", sid, "--credential", cid)
		wantPin := adminapi.CreateVirtualKeyInput{Tenant: tid, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sid, CredentialID: cid}}
		if got := r.f.only(t, "createVirtualKey"); !reflect.DeepEqual(got, wantPin) {
			t.Errorf("create with --credential: %+v", got)
		}
	})

	t.Run("platform keys", func(t *testing.T) {
		r := newRig(t)
		secret := testutil.NewKey(t)
		r.f.out["listPlatformKeys"] = &adminapi.PlatformKeyList{Items: []adminapi.PlatformKey{pkey}}
		r.f.out["createPlatformKey"] = &adminapi.PlatformKeyCreated{Key: pkey, Secret: secret}
		r.f.out["getPlatformKey"] = &pkey
		r.f.out["revokePlatformKey"] = &pkey
		mustRun(t, r, "platform-key", "list", "--state", "active", "--sort", "created_at", "--limit", "2")
		if got := r.f.only(t, "listPlatformKeys"); !reflect.DeepEqual(got, adminapi.ListPlatformKeysInput{State: adminapi.KeyStateActive, Sort: adminapi.SortCreatedAt, Limit: 2}) {
			t.Errorf("list: %+v", got)
		}
		out, _ := mustRun(t, r, "platform-key", "create", "--description", "ops", "--expires-at", t1.Format(time.RFC3339))
		if got := r.f.only(t, "createPlatformKey"); !reflect.DeepEqual(got, adminapi.CreatePlatformKeyInput{Body: adminapi.PlatformKeyCreate{Description: "ops", ExpiresAt: &t1}}) {
			t.Errorf("create: %+v", got)
		}
		if strings.Count(out, secret) != 1 {
			t.Errorf("the key is printed once:\n%s", out)
		}
		mustRun(t, r, "platform-key", "get", pk)
		if got := r.f.only(t, "getPlatformKey"); !reflect.DeepEqual(got, adminapi.GetPlatformKeyInput{Key: pk}) {
			t.Errorf("get: %+v", got)
		}
		mustRun(t, r, "platform-key", "revoke", "--reason", "rotated", pk)
		if got := r.f.only(t, "revokePlatformKey"); !reflect.DeepEqual(got, adminapi.RevokePlatformKeyInput{Key: pk, Body: adminapi.Reason{Reason: "rotated"}}) {
			t.Errorf("revoke: %+v", got)
		}
	})

	t.Run("json output is the body of the API, indented", func(t *testing.T) {
		r := newRig(t)
		secret := testutil.NewKey(t)
		created := &adminapi.PlatformKeyCreated{Key: pkey, Secret: secret}
		r.f.out["createPlatformKey"] = created
		r.f.out["getTenant"] = &tenant
		out, _ := mustRun(t, r, "tenant", "get", "-o", "json", tid)
		var got adminapi.Tenant
		if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, tenant) {
			t.Errorf("%v\n%s", err, out)
		}
		if !strings.Contains(out, "\n  \"id\"") {
			t.Errorf("the output is not indented:\n%s", out)
		}
		out, _ = mustRun(t, r, "platform-key", "create", "-o", "json")
		var c adminapi.PlatformKeyCreated
		if err := json.Unmarshal([]byte(out), &c); err != nil || c.Secret != secret || strings.Count(out, secret) != 1 {
			t.Errorf("json create: %v\n%s", err, out)
		}
		// With an out-file the secret is replaced by the empty string.
		file := filepath.Join(t.TempDir(), "new.key")
		out, _ = mustRun(t, r, "platform-key", "create", "-o", "json", "--key-out", file)
		if err := json.Unmarshal([]byte(out), &c); err != nil || c.Secret != "" || strings.Contains(out, secret) {
			t.Errorf("json create with --key-out: %v\n%s", err, out)
		}
		b, err := os.ReadFile(file)
		if err != nil || strings.TrimSpace(string(b)) != secret {
			t.Errorf("the out-file holds %q, %v", b, err)
		}
	})

	t.Run("a problem is printed on stderr and exits 1", func(t *testing.T) {
		r := newRig(t)
		r.f.fail["createTenant"] = &problemErr{adminapi.Problem{
			Type: "urn:pandokos:problem:conflict", Title: "Conflict", Status: 409, Code: adminapi.ProblemCodeConflict,
			Detail: "The name is already in use.", RequestID: "req409abc",
		}}
		code, out, errs := r.run(t, "tenant", "create", "acme")
		if code != 1 || out != "" || errs != "error: conflict: The name is already in use. (request req409abc)\n" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		// Field errors follow, one line each.
		code, _, errs = r.run(t, "tenant", "create", "Not A Name")
		if code != 1 || !strings.HasPrefix(errs, "error: invalid_request: The request does not match the API description. (request req400)\n") || !strings.Contains(errs, "\n  name: must match ") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
		r.f.fail["getTenant"] = &problemErr{adminapi.Problem{Type: "urn:pandokos:problem:not_found", Title: "Not found", Status: 404, Code: adminapi.ProblemCodeNotFound, Detail: "The resource does not exist.", RequestID: "req404"}}
		if code, _, errs := r.run(t, "tenant", "get", tid); code != 1 || !strings.Contains(errs, "error: not_found: The resource does not exist. (request req404)") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
	})

	t.Run("an unreachable server exits 1 and the key is not in the message", func(t *testing.T) {
		r := newRig(t)
		url := r.srv.URL
		r.srv.Close()
		code, out, errs := runWith(t, []string{"PANDOKOS_CLI_SERVER=" + url, "PANDOKOS_CLI_KEY_FILE=" + r.keyFile}, "whoami")
		if code != 1 || out != "" || !strings.HasPrefix(errs, "error: the server could not be reached: ") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		testutil.AssertAbsent(t, "stderr", errs, testutil.KeyForms(r.key)...)
		if strings.Contains(errs, url) {
			t.Errorf("the message repeats the URL: %q", errs)
		}
	})
}

// TestConnectionAndSecrets is T-218.
func TestConnectionAndSecrets(t *testing.T) {
	t.Run("a flag beats its variable", func(t *testing.T) {
		a, b := newRig(t), newRig(t)
		a.f.out["getMe"] = &adminapi.Me{Name: "from-a"}
		b.f.out["getMe"] = &adminapi.Me{Name: "from-b"}
		env := []string{"PANDOKOS_CLI_SERVER=" + a.srv.URL, "PANDOKOS_CLI_KEY_FILE=" + a.keyFile}
		code, out, errs := runWith(t, env, "whoami", "--server", b.srv.URL, "--key-file", b.keyFile)
		if code != 0 || !strings.Contains(out, "from-b") || len(a.f.calls("getMe")) != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if len(b.auth) != 1 || b.auth[0] != "Bearer "+b.key || len(a.auth) != 0 {
			t.Errorf("the key file of the flag was not used: %v", b.auth)
		}
		// Without the flags the variables apply.
		if code, out, _ := runWith(t, env, "whoami"); code != 0 || !strings.Contains(out, "from-a") || a.auth[0] != "Bearer "+a.key {
			t.Errorf("variables: exit %d, stdout %q", code, out)
		}
	})

	t.Run("the default server is the loopback admin listener", func(t *testing.T) {
		// Nothing listens there in a test; the point is that the command gets as far as connecting.
		keyFile := testutil.WriteSecret(t, "key", testutil.NewKey(t))
		code, _, errs := runWith(t, []string{"PANDOKOS_CLI_KEY_FILE=" + keyFile}, "whoami", "--server", "http://127.0.0.1:1")
		if code != 1 || !strings.Contains(errs, "could not be reached") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
	})

	t.Run("the key file is required", func(t *testing.T) {
		r := newRig(t)
		code, out, errs := runWith(t, []string{"PANDOKOS_CLI_SERVER=" + r.srv.URL}, "whoami")
		if code != 2 || out != "" || !strings.Contains(errs, "--key-file") || !strings.Contains(errs, "PANDOKOS_CLI_KEY_FILE") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if len(r.f.calls("getMe")) != 0 {
			t.Error("the server was called without a key")
		}
	})

	t.Run("there is no way to pass the key itself or to skip verification", func(t *testing.T) {
		r := newRig(t)
		for _, args := range [][]string{
			{"whoami", "--key", r.key}, {"whoami", "--insecure"}, {"whoami", "--insecure-skip-verify"}, {"whoami", "--skip-verify"},
		} {
			code, _, errs := r.run(t, args...)
			if code != 2 || errs == "" {
				t.Errorf("%v: exit %d, stderr %q", args, code, errs)
			}
			testutil.AssertAbsent(t, "stderr", errs, testutil.KeyForms(r.key)...)
		}
		code, _, _ := runWith(t, []string{"PANDOKOS_CLI_SERVER=" + r.srv.URL, "PANDOKOS_CLI_KEY=" + r.key}, "whoami")
		if code != 2 {
			t.Errorf("a variable that holds the key was accepted: %d", code)
		}
	})

	t.Run("the server URL is checked", func(t *testing.T) {
		r := newRig(t)
		for _, u := range []string{"ftp://127.0.0.1:8081", "127.0.0.1:8081", "http://user:pw@127.0.0.1:8081", "http://127.0.0.1:8081/v1", "http://127.0.0.1:8081?x=1", "http://", ""} {
			code, _, errs := runWith(t, []string{"PANDOKOS_CLI_KEY_FILE=" + r.keyFile}, "whoami", "--server", u)
			if u == "" {
				continue // empty means the default
			}
			if code != 2 || errs == "" {
				t.Errorf("%q: exit %d, stderr %q", u, code, errs)
			}
			if strings.Contains(errs, "pw") && strings.Contains(u, "pw") {
				t.Errorf("the password of the URL is repeated: %q", errs)
			}
		}
	})

	t.Run("--ca-file verifies a TLS server", func(t *testing.T) {
		caFile, certFile, keyFile := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
		otherCA, _, _ := testcert.Files(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
		f := newFake()
		f.out["getMe"] = &adminapi.Me{Name: "tls-ok"}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(handler(f, nil, nil))
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		srv.StartTLS()
		t.Cleanup(srv.Close)
		keyF := testutil.WriteSecret(t, "key", testutil.NewKey(t))
		env := []string{"PANDOKOS_CLI_KEY_FILE=" + keyF}
		if code, out, errs := runWith(t, env, "whoami", "--server", srv.URL, "--ca-file", caFile); code != 0 || !strings.Contains(out, "tls-ok") {
			t.Errorf("trusted authority: exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if code, out, errs := runWith(t, append(env, "PANDOKOS_CLI_CA_FILE="+caFile), "whoami", "--server", srv.URL); code != 0 || !strings.Contains(out, "tls-ok") {
			t.Errorf("trusted authority by variable: exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if code, out, errs := runWith(t, env, "whoami", "--server", srv.URL, "--ca-file", otherCA); code != 1 || out != "" || !strings.HasPrefix(errs, "error: the server could not be reached: ") {
			t.Errorf("another authority: exit %d, stdout %q, stderr %q", code, out, errs)
		}
		if code, _, errs := runWith(t, env, "whoami", "--server", srv.URL); code != 1 || !strings.Contains(errs, "could not be reached") {
			t.Errorf("system roots: exit %d, stderr %q", code, errs)
		}
		if code, _, errs := runWith(t, env, "whoami", "--server", srv.URL, "--ca-file", filepath.Join(t.TempDir(), "missing.pem")); code == 0 || errs == "" {
			t.Errorf("a missing CA file: exit %d", code)
		}
		bad := testutil.WriteSecret(t, "bad.pem", "not a certificate")
		if code, _, _ := runWith(t, env, "whoami", "--server", srv.URL, "--ca-file", bad); code == 0 {
			t.Error("a CA file without a certificate was accepted")
		}
	})

	t.Run("plain HTTP to a host that is not loopback warns", func(t *testing.T) {
		keyF := testutil.WriteSecret(t, "key", testutil.NewKey(t))
		env := []string{"PANDOKOS_CLI_KEY_FILE=" + keyF}
		code, _, errs := runWith(t, env, "whoami", "--server", "http://admin.invalid:8081")
		if code != 1 || !strings.Contains(errs, "warning: the key is sent without TLS") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
		r := newRig(t)
		if _, errs := mustRun(t, r, "whoami"); strings.Contains(errs, "without TLS") {
			t.Errorf("a loopback server warned: %q", errs)
		}
	})

	t.Run("a created key is printed once, or written to a file", func(t *testing.T) {
		r := newRig(t)
		secret := testutil.NewKey(t)
		r.f.out["createVirtualKey"] = &adminapi.VirtualKeyCreated{Key: vkey, Secret: secret}
		file := filepath.Join(t.TempDir(), "out.key")
		out, errs := mustRun(t, r, "key", "create", "--tenant", tid, "--service-account", sid, "--key-out", file)
		if strings.Contains(out, secret) || strings.Contains(errs, secret) || !lineWith(out, "key written to "+file) {
			t.Errorf("stdout %q, stderr %q", out, errs)
		}
		st, err := os.Stat(file)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("out-file: %v %v", st, err)
		}
		if b, _ := os.ReadFile(file); string(b) != secret+"\n" {
			t.Errorf("the out-file holds %q", b)
		}
		// An existing out-file is an error, and the key is not issued to nowhere.
		r.f.got = map[string][]any{}
		code, out, errs := r.run(t, "key", "create", "--tenant", tid, "--service-account", sid, "--key-out", file)
		if code == 0 || strings.Contains(out+errs, secret) || len(r.f.calls("createVirtualKey")) != 0 {
			t.Errorf("exit %d, stdout %q, stderr %q, calls %d", code, out, errs, len(r.f.calls("createVirtualKey")))
		}
		// Nor does the tenant create command print the key when it has an out-file.
		r.f.out["createTenant"] = &adminapi.TenantCreated{Tenant: tenant, AdminServiceAccount: acct, AdminKey: adminapi.VirtualKeyCreated{Key: vkey, Secret: secret}}
		file2 := filepath.Join(t.TempDir(), "admin.key")
		out, _ = mustRun(t, r, "tenant", "create", "--admin-key-out", file2, "acme")
		if strings.Contains(out, secret) || !lineWith(out, "key written to "+file2) {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("the content of the key file is in no output", func(t *testing.T) {
		r := newRig(t)
		r.f.fail["getMe"] = &problemErr{adminapi.Problem{Type: "urn:pandokos:problem:unauthenticated", Title: "Unauthenticated", Status: 401, Code: adminapi.ProblemCodeUnauthenticated, Detail: "Missing or invalid credential.", RequestID: "r401"}}
		for _, args := range [][]string{{"whoami"}, {"whoami", "-o", "json"}, {"tenant", "list"}, {"tenant", "get", "nobody"}} {
			_, out, errs := r.run(t, args...)
			testutil.AssertAbsent(t, "stdout of "+strings.Join(args, " "), out, testutil.KeyForms(r.key)...)
			testutil.AssertAbsent(t, "stderr of "+strings.Join(args, " "), errs, testutil.KeyForms(r.key)...)
		}
		if len(r.auth) == 0 || r.auth[0] != "Bearer "+r.key {
			t.Errorf("the server did not receive the key: %v", r.auth)
		}
	})

	t.Run("the bootstrap warning", func(t *testing.T) {
		f := newFake()
		srv := serve(t, f, http.Header{"X-Pandokos-Warning": {"bootstrap-key-active"}}, nil)
		keyF := testutil.WriteSecret(t, "key", testutil.NewKey(t))
		code, _, errs := runWith(t, []string{"PANDOKOS_CLI_SERVER=" + srv.URL, "PANDOKOS_CLI_KEY_FILE=" + keyF}, "whoami")
		if code != 0 || !strings.Contains(errs, "warning: you are using the bootstrap key; create a platform key and revoke this one") {
			t.Errorf("exit %d, stderr %q", code, errs)
		}
		r := newRig(t)
		if _, errs := mustRun(t, r, "whoami"); strings.Contains(errs, "bootstrap") {
			t.Errorf("a warning without the header: %q", errs)
		}
	})
}
