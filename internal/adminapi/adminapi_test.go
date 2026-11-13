package adminapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
)

const (
	tid = "0192f0f0-0000-7000-8000-000000000001"
	sid = "0192f0f0-0000-7000-8000-000000000002"
	vk  = "tabcdefghijklmnop"
	pk  = "pabcdefghijklmnop"
	cid = "0192f0f0-0000-7000-8000-000000000003"
)

// fake is an adminapi.Server that records the input of every call and answers with zero values.
type fake struct {
	mu  sync.Mutex
	got map[string]any
}

func (f *fake) rec(id string, in any) { f.mu.Lock(); f.got[id] = in; f.mu.Unlock() }
func (f *fake) last(id string) any    { f.mu.Lock(); defer f.mu.Unlock(); return f.got[id] }
func (f *fake) reset()                { f.mu.Lock(); f.got = map[string]any{}; f.mu.Unlock() }

func (f *fake) GetMe(_ context.Context, in adminapi.GetMeInput) (*adminapi.Me, error) {
	f.rec("getMe", in)
	return &adminapi.Me{}, nil
}
func (f *fake) ListTenants(_ context.Context, in adminapi.ListTenantsInput) (*adminapi.TenantList, error) {
	f.rec("listTenants", in)
	return &adminapi.TenantList{}, nil
}
func (f *fake) CreateTenant(_ context.Context, in adminapi.CreateTenantInput) (*adminapi.TenantCreated, error) {
	f.rec("createTenant", in)
	return &adminapi.TenantCreated{}, nil
}
func (f *fake) GetTenant(_ context.Context, in adminapi.GetTenantInput) (*adminapi.Tenant, error) {
	f.rec("getTenant", in)
	return &adminapi.Tenant{}, nil
}
func (f *fake) UpdateTenant(_ context.Context, in adminapi.UpdateTenantInput) (*adminapi.Tenant, error) {
	f.rec("updateTenant", in)
	return &adminapi.Tenant{}, nil
}
func (f *fake) SuspendTenant(_ context.Context, in adminapi.SuspendTenantInput) (*adminapi.Tenant, error) {
	f.rec("suspendTenant", in)
	return &adminapi.Tenant{}, nil
}
func (f *fake) ReactivateTenant(_ context.Context, in adminapi.ReactivateTenantInput) (*adminapi.Tenant, error) {
	f.rec("reactivateTenant", in)
	return &adminapi.Tenant{}, nil
}
func (f *fake) ListServiceAccounts(_ context.Context, in adminapi.ListServiceAccountsInput) (*adminapi.ServiceAccountList, error) {
	f.rec("listServiceAccounts", in)
	return &adminapi.ServiceAccountList{}, nil
}
func (f *fake) CreateServiceAccount(_ context.Context, in adminapi.CreateServiceAccountInput) (*adminapi.ServiceAccount, error) {
	f.rec("createServiceAccount", in)
	return &adminapi.ServiceAccount{}, nil
}
func (f *fake) GetServiceAccount(_ context.Context, in adminapi.GetServiceAccountInput) (*adminapi.ServiceAccount, error) {
	f.rec("getServiceAccount", in)
	return &adminapi.ServiceAccount{}, nil
}
func (f *fake) UpdateServiceAccount(_ context.Context, in adminapi.UpdateServiceAccountInput) (*adminapi.ServiceAccount, error) {
	f.rec("updateServiceAccount", in)
	return &adminapi.ServiceAccount{}, nil
}
func (f *fake) DisableServiceAccount(_ context.Context, in adminapi.DisableServiceAccountInput) (*adminapi.ServiceAccount, error) {
	f.rec("disableServiceAccount", in)
	return &adminapi.ServiceAccount{}, nil
}
func (f *fake) EnableServiceAccount(_ context.Context, in adminapi.EnableServiceAccountInput) (*adminapi.ServiceAccount, error) {
	f.rec("enableServiceAccount", in)
	return &adminapi.ServiceAccount{}, nil
}
func (f *fake) ListVirtualKeys(_ context.Context, in adminapi.ListVirtualKeysInput) (*adminapi.VirtualKeyList, error) {
	f.rec("listVirtualKeys", in)
	return &adminapi.VirtualKeyList{}, nil
}
func (f *fake) CreateVirtualKey(_ context.Context, in adminapi.CreateVirtualKeyInput) (*adminapi.VirtualKeyCreated, error) {
	f.rec("createVirtualKey", in)
	return &adminapi.VirtualKeyCreated{}, nil
}
func (f *fake) GetVirtualKey(_ context.Context, in adminapi.GetVirtualKeyInput) (*adminapi.VirtualKey, error) {
	f.rec("getVirtualKey", in)
	return &adminapi.VirtualKey{}, nil
}
func (f *fake) RevokeVirtualKey(_ context.Context, in adminapi.RevokeVirtualKeyInput) (*adminapi.VirtualKey, error) {
	f.rec("revokeVirtualKey", in)
	return &adminapi.VirtualKey{}, nil
}
func (f *fake) ListPlatformKeys(_ context.Context, in adminapi.ListPlatformKeysInput) (*adminapi.PlatformKeyList, error) {
	f.rec("listPlatformKeys", in)
	return &adminapi.PlatformKeyList{}, nil
}
func (f *fake) CreatePlatformKey(_ context.Context, in adminapi.CreatePlatformKeyInput) (*adminapi.PlatformKeyCreated, error) {
	f.rec("createPlatformKey", in)
	return &adminapi.PlatformKeyCreated{}, nil
}
func (f *fake) GetPlatformKey(_ context.Context, in adminapi.GetPlatformKeyInput) (*adminapi.PlatformKey, error) {
	f.rec("getPlatformKey", in)
	return &adminapi.PlatformKey{}, nil
}
func (f *fake) RevokePlatformKey(_ context.Context, in adminapi.RevokePlatformKeyInput) (*adminapi.PlatformKey, error) {
	f.rec("revokePlatformKey", in)
	return &adminapi.PlatformKey{}, nil
}
func (f *fake) ListTenantCredentials(_ context.Context, in adminapi.ListTenantCredentialsInput) (*adminapi.CredentialList, error) {
	f.rec("listTenantCredentials", in)
	return &adminapi.CredentialList{}, nil
}
func (f *fake) GetTenantCredential(_ context.Context, in adminapi.GetTenantCredentialInput) (*adminapi.Credential, error) {
	f.rec("getTenantCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) DisableTenantCredential(_ context.Context, in adminapi.DisableTenantCredentialInput) (*adminapi.Credential, error) {
	f.rec("disableTenantCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) RevokeTenantCredential(_ context.Context, in adminapi.RevokeTenantCredentialInput) (*adminapi.Credential, error) {
	f.rec("revokeTenantCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) ListCredentials(_ context.Context, in adminapi.ListCredentialsInput) (*adminapi.CredentialList, error) {
	f.rec("listCredentials", in)
	return &adminapi.CredentialList{}, nil
}
func (f *fake) CreateCredential(_ context.Context, in adminapi.CreateCredentialInput) (*adminapi.Credential, error) {
	f.rec("createCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) GetCredential(_ context.Context, in adminapi.GetCredentialInput) (*adminapi.Credential, error) {
	f.rec("getCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) UpdateCredential(_ context.Context, in adminapi.UpdateCredentialInput) (*adminapi.Credential, error) {
	f.rec("updateCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) DeleteCredential(_ context.Context, in adminapi.DeleteCredentialInput) (*adminapi.Credential, error) {
	f.rec("deleteCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) DisableCredential(_ context.Context, in adminapi.DisableCredentialInput) (*adminapi.Credential, error) {
	f.rec("disableCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) EnableCredential(_ context.Context, in adminapi.EnableCredentialInput) (*adminapi.Credential, error) {
	f.rec("enableCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) RevokeCredential(_ context.Context, in adminapi.RevokeCredentialInput) (*adminapi.Credential, error) {
	f.rec("revokeCredential", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) ReplaceCredentialSecret(_ context.Context, in adminapi.ReplaceCredentialSecretInput) (*adminapi.Credential, error) {
	f.rec("replaceCredentialSecret", in)
	return &adminapi.Credential{}, nil
}
func (f *fake) SetDefaultCredential(_ context.Context, in adminapi.SetDefaultCredentialInput) (*adminapi.Credential, error) {
	f.rec("setDefaultCredential", in)
	return &adminapi.Credential{}, nil
}

var _ adminapi.Server = (*fake)(nil)

// problemBody is what the test's Error hook writes: enough to see the code and the members.
type problemBody struct {
	Status int                   `json:"status"`
	Code   string                `json:"code"`
	Op     string                `json:"op"`
	Errors []adminapi.FieldError `json:"errors"`
}

type rig struct {
	f      *fake
	srv    *httptest.Server
	before func(ctx context.Context, r *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error)
}

func errorHook(w http.ResponseWriter, _ *http.Request, op *adminapi.Operation, err error) {
	pb := problemBody{Status: 500}
	if op != nil {
		pb.Op = op.ID
	}
	var re *adminapi.RequestError
	if errors.As(err, &re) {
		pb.Status, pb.Code, pb.Errors = re.Status, string(re.Code), re.Fields
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(pb.Status)
	_ = json.NewEncoder(w).Encode(pb)
}

func newRig(t *testing.T, maxBody int64) *rig {
	t.Helper()
	r := &rig{f: &fake{got: map[string]any{}}}
	h := adminapi.NewHandler(r.f, adminapi.Hooks{
		Before: func(ctx context.Context, req *http.Request, op *adminapi.Operation, params map[string]string) (context.Context, error) {
			if r.before != nil {
				return r.before(ctx, req, op, params)
			}
			return ctx, nil
		},
		Error:        errorHook,
		MaxBodyBytes: maxBody,
	})
	r.srv = httptest.NewServer(h)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) client() *adminapi.Client {
	return &adminapi.Client{BaseURL: r.srv.URL, HTTP: r.srv.Client(), Key: func() string { return "k" }}
}

func (r *rig) raw(t *testing.T, method, path, contentType, body string) (int, problemBody, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, r.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var pb problemBody
	_ = json.Unmarshal(b, &pb)
	return resp.StatusCode, pb, string(b)
}

func at(h int) time.Time { return time.Date(2026, 11, 13, h, 4, 5, 0, time.UTC) }

type call struct {
	in any
	do func() error
}

// TestRouterAndClient is T-212.
func TestRouterAndClient(t *testing.T) {
	t.Run("every operation round-trips", func(t *testing.T) {
		r := newRig(t, 1<<20)
		c := r.client()
		ctx := context.Background()
		future := at(9)
		calls := map[string]call{}
		add := func(id string, in any, do func() error) { calls[id] = call{in, do} }
		{
			in := adminapi.GetMeInput{}
			add("getMe", in, func() error { _, err := c.GetMe(ctx, in); return err })
		}
		{
			in := adminapi.ListTenantsInput{Limit: 7, Cursor: "abc", Sort: adminapi.NamedSortMinusCreatedAt, State: adminapi.TenantStateActive, Name: "acme"}
			add("listTenants", in, func() error { _, err := c.ListTenants(ctx, in); return err })
		}
		{
			in := adminapi.CreateTenantInput{Body: adminapi.TenantCreate{Name: "acme", DisplayName: "Acme", AdminServiceAccountName: "root", AdminKeyExpiresAt: &future}}
			add("createTenant", in, func() error { _, err := c.CreateTenant(ctx, in); return err })
		}
		{
			in := adminapi.GetTenantInput{Tenant: tid}
			add("getTenant", in, func() error { _, err := c.GetTenant(ctx, in); return err })
		}
		{
			in := adminapi.UpdateTenantInput{Tenant: tid, Body: adminapi.TenantUpdate{Name: "acme2", DisplayName: "Acme 2"}}
			add("updateTenant", in, func() error { _, err := c.UpdateTenant(ctx, in); return err })
		}
		{
			in := adminapi.SuspendTenantInput{Tenant: tid, Body: adminapi.Reason{Reason: "why"}}
			add("suspendTenant", in, func() error { _, err := c.SuspendTenant(ctx, in); return err })
		}
		{
			in := adminapi.ReactivateTenantInput{Tenant: tid, Body: adminapi.Reason{Reason: "why"}}
			add("reactivateTenant", in, func() error { _, err := c.ReactivateTenant(ctx, in); return err })
		}
		{
			in := adminapi.ListServiceAccountsInput{Tenant: tid, Limit: 3, Sort: adminapi.NamedSortName, State: adminapi.ServiceAccountStateDisabled, Name: "bot"}
			add("listServiceAccounts", in, func() error { _, err := c.ListServiceAccounts(ctx, in); return err })
		}
		{
			in := adminapi.CreateServiceAccountInput{Tenant: tid, Body: adminapi.ServiceAccountCreate{Name: "bot", Description: "d", ValidFrom: &future, Roles: []string{"inference"}}}
			add("createServiceAccount", in, func() error { _, err := c.CreateServiceAccount(ctx, in); return err })
		}
		{
			in := adminapi.GetServiceAccountInput{Tenant: tid, ServiceAccount: sid}
			add("getServiceAccount", in, func() error { _, err := c.GetServiceAccount(ctx, in); return err })
		}
		{
			in := adminapi.UpdateServiceAccountInput{Tenant: tid, ServiceAccount: sid, Body: adminapi.ServiceAccountUpdate{
				Name: "bot2", Description: "d2", ValidFrom: adminapi.Null[time.Time](), ValidUntil: adminapi.Value(future), Roles: []string{"inference"}}}
			add("updateServiceAccount", in, func() error { _, err := c.UpdateServiceAccount(ctx, in); return err })
		}
		{
			in := adminapi.DisableServiceAccountInput{Tenant: tid, ServiceAccount: sid, Body: adminapi.Reason{Reason: "why"}}
			add("disableServiceAccount", in, func() error { _, err := c.DisableServiceAccount(ctx, in); return err })
		}
		{
			in := adminapi.EnableServiceAccountInput{Tenant: tid, ServiceAccount: sid}
			add("enableServiceAccount", in, func() error { _, err := c.EnableServiceAccount(ctx, in); return err })
		}
		{
			in := adminapi.ListVirtualKeysInput{Tenant: tid, Limit: 9, Sort: adminapi.SortMinusCreatedAt, State: adminapi.KeyStateRevoked, ServiceAccountID: sid}
			add("listVirtualKeys", in, func() error { _, err := c.ListVirtualKeys(ctx, in); return err })
		}
		{
			in := adminapi.CreateVirtualKeyInput{Tenant: tid, Body: adminapi.VirtualKeyCreate{ServiceAccountID: sid, Description: "d", CredentialID: cid, Scopes: []string{"model:invoke"}, ExpiresAt: &future}}
			add("createVirtualKey", in, func() error { _, err := c.CreateVirtualKey(ctx, in); return err })
		}
		{
			in := adminapi.GetVirtualKeyInput{Tenant: tid, Key: vk}
			add("getVirtualKey", in, func() error { _, err := c.GetVirtualKey(ctx, in); return err })
		}
		{
			in := adminapi.RevokeVirtualKeyInput{Tenant: tid, Key: vk, Body: adminapi.Reason{Reason: "why"}}
			add("revokeVirtualKey", in, func() error { _, err := c.RevokeVirtualKey(ctx, in); return err })
		}
		{
			in := adminapi.ListPlatformKeysInput{Limit: 2, Sort: adminapi.SortCreatedAt, State: adminapi.KeyStateActive}
			add("listPlatformKeys", in, func() error { _, err := c.ListPlatformKeys(ctx, in); return err })
		}
		{
			in := adminapi.CreatePlatformKeyInput{Body: adminapi.PlatformKeyCreate{Description: "d", ExpiresAt: &future}}
			add("createPlatformKey", in, func() error { _, err := c.CreatePlatformKey(ctx, in); return err })
		}
		{
			in := adminapi.GetPlatformKeyInput{Key: pk}
			add("getPlatformKey", in, func() error { _, err := c.GetPlatformKey(ctx, in); return err })
		}
		{
			in := adminapi.RevokePlatformKeyInput{Key: pk, Body: adminapi.Reason{Reason: "why"}}
			add("revokePlatformKey", in, func() error { _, err := c.RevokePlatformKey(ctx, in); return err })
		}
		{
			in := adminapi.ListCredentialsInput{Tenant: tid, Limit: 4, Cursor: "abc", Sort: adminapi.SortMinusCreatedAt, State: adminapi.CredentialStateDisabled, Provider: "openai"}
			add("listCredentials", in, func() error { _, err := c.ListCredentials(ctx, in); return err })
		}
		{
			in := adminapi.CreateCredentialInput{Tenant: tid, Body: adminapi.CredentialCreate{Provider: "openai", Label: "main", Secret: "synthetic-secret-1", Description: "d", ExpiresAt: &future, ValidateAtWrite: true}}
			add("createCredential", in, func() error { _, err := c.CreateCredential(ctx, in); return err })
		}
		{
			in := adminapi.GetCredentialInput{Tenant: tid, Credential: cid}
			add("getCredential", in, func() error { _, err := c.GetCredential(ctx, in); return err })
		}
		{
			in := adminapi.UpdateCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.CredentialUpdate{Label: "other", Description: "d2", ExpiresAt: adminapi.Null[time.Time]()}}
			add("updateCredential", in, func() error { _, err := c.UpdateCredential(ctx, in); return err })
		}
		{
			in := adminapi.DeleteCredentialInput{Tenant: tid, Credential: cid}
			add("deleteCredential", in, func() error { _, err := c.DeleteCredential(ctx, in); return err })
		}
		{
			in := adminapi.ReplaceCredentialSecretInput{Tenant: tid, Credential: cid, Body: adminapi.CredentialSecret{Secret: "synthetic-secret-2", ExpiresAt: &future, ValidateAtWrite: true}}
			add("replaceCredentialSecret", in, func() error { _, err := c.ReplaceCredentialSecret(ctx, in); return err })
		}
		{
			in := adminapi.DisableCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "why"}}
			add("disableCredential", in, func() error { _, err := c.DisableCredential(ctx, in); return err })
		}
		{
			in := adminapi.EnableCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "why"}}
			add("enableCredential", in, func() error { _, err := c.EnableCredential(ctx, in); return err })
		}
		{
			in := adminapi.SetDefaultCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "why"}}
			add("setDefaultCredential", in, func() error { _, err := c.SetDefaultCredential(ctx, in); return err })
		}
		{
			in := adminapi.RevokeCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.Reason{Reason: "why"}}
			add("revokeCredential", in, func() error { _, err := c.RevokeCredential(ctx, in); return err })
		}
		{
			in := adminapi.ListTenantCredentialsInput{Tenant: tid, Limit: 4, Sort: adminapi.SortCreatedAt, State: adminapi.CredentialStateRevoked, Provider: "anthropic"}
			add("listTenantCredentials", in, func() error { _, err := c.ListTenantCredentials(ctx, in); return err })
		}
		{
			in := adminapi.GetTenantCredentialInput{Tenant: tid, Credential: cid}
			add("getTenantCredential", in, func() error { _, err := c.GetTenantCredential(ctx, in); return err })
		}
		{
			in := adminapi.DisableTenantCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.RequiredReason{Reason: "why"}}
			add("disableTenantCredential", in, func() error { _, err := c.DisableTenantCredential(ctx, in); return err })
		}
		{
			in := adminapi.RevokeTenantCredentialInput{Tenant: tid, Credential: cid, Body: adminapi.RequiredReason{Reason: "why"}}
			add("revokeTenantCredential", in, func() error { _, err := c.RevokeTenantCredential(ctx, in); return err })
		}
		if len(adminapi.Operations) != 35 {
			t.Fatalf("%d operations, want 35", len(adminapi.Operations))
		}
		for _, op := range adminapi.Operations {
			tc, ok := calls[op.ID]
			if !ok {
				t.Errorf("the test has no call for operation %s", op.ID)
				continue
			}
			r.f.reset()
			if err := tc.do(); err != nil {
				t.Errorf("%s: %v", op.ID, err)
				continue
			}
			if got := r.f.last(op.ID); !reflect.DeepEqual(got, tc.in) {
				t.Errorf("%s: the server received %+v, the client sent %+v", op.ID, got, tc.in)
			}
		}
		for id := range calls {
			found := false
			for _, op := range adminapi.Operations {
				found = found || op.ID == id
			}
			if !found {
				t.Errorf("the test calls %s, which is not an operation", id)
			}
		}
	})

	t.Run("operations carry a permission and a status", func(t *testing.T) {
		for _, op := range adminapi.Operations {
			if op.Permission == "" || (op.Status != 200 && op.Status != 201) || !strings.HasPrefix(op.Path, "/v1/") {
				t.Errorf("%+v", op)
			}
			if (op.Status == 201) != (op.Method == "POST" && strings.HasPrefix(op.ID, "create")) {
				t.Errorf("%s: status %d", op.ID, op.Status)
			}
		}
	})

	t.Run("bad bodies are 400 with the member named and no value quoted", func(t *testing.T) {
		r := newRig(t, 1<<20)
		const canary = "canary-value-xyz"
		cases := []struct {
			name, body, field string
		}{
			{"unknown member", `{"name":"acme","` + canary + `":1}`, canary},
			{"missing required member", `{}`, "name"},
			{"null required member", `{"name":null}`, "name"},
			{"wrong type", `{"name":5}`, "name"},
			{"rule of the document", `{"name":"` + canary + `!"}`, "name"},
			{"too long", `{"name":"acme","display_name":"` + strings.Repeat("x", 201) + `"}`, "display_name"},
			{"second JSON value", `{"name":"acme"}{"name":"` + canary + `"}`, "body"},
			{"array", `["` + canary + `"]`, "body"},
			{"string", `"` + canary + `"`, "body"},
			{"null", `null`, "body"},
			{"not JSON", `{"name":` + canary, "body"},
		}
		for _, c := range cases {
			status, pb, text := r.raw(t, "POST", "/v1/tenants", "application/json", c.body)
			if status != 400 || pb.Code != "invalid_request" {
				t.Errorf("%s: status %d, code %q: %s", c.name, status, pb.Code, text)
				continue
			}
			named := false
			for _, e := range pb.Errors {
				named = named || e.Field == c.field
			}
			if !named {
				t.Errorf("%s: the member %q is not named: %s", c.name, c.field, text)
			}
			if c.name != "unknown member" && strings.Contains(text, canary) {
				t.Errorf("%s: the response quotes the value: %s", c.name, text)
			}
		}
		if got := r.f.last("createTenant"); got != nil {
			t.Errorf("the server was called with %+v", got)
		}
	})

	t.Run("bad queries are 400", func(t *testing.T) {
		r := newRig(t, 1<<20)
		const canary = "canary-value-xyz"
		for name, q := range map[string]string{
			"unknown parameter":  "?" + canary + "=1",
			"repeated parameter": "?limit=1&limit=2",
			"not an integer":     "?limit=" + canary,
			"limit zero":         "?limit=0",
			"limit too large":    "?limit=201",
			"bad sort":           "?sort=" + canary,
			"bad state":          "?state=" + canary,
			"empty name":         "?name=",
			"bad name":           "?name=" + url.QueryEscape("Not A Name!"),
			"bad query string":   "?%zz",
		} {
			status, pb, text := r.raw(t, "GET", "/v1/tenants"+q, "", "")
			if status != 400 || pb.Code != "invalid_request" || len(pb.Errors) == 0 {
				t.Errorf("%s: status %d: %s", name, status, text)
			}
			if name != "unknown parameter" && strings.Contains(text, canary) {
				t.Errorf("%s: the response quotes the value: %s", name, text)
			}
		}
		if got := r.f.last("listTenants"); got != nil {
			t.Errorf("the server was called with %+v", got)
		}
	})

	t.Run("media type and size", func(t *testing.T) {
		r := newRig(t, 64)
		if status, pb, text := r.raw(t, "POST", "/v1/tenants", "text/plain", `{"name":"acme"}`); status != 415 || pb.Code != "unsupported_media_type" {
			t.Errorf("text/plain: %d %s", status, text)
		}
		if status, pb, text := r.raw(t, "POST", "/v1/tenants", "", `{"name":"acme"}`); status != 415 || pb.Code != "unsupported_media_type" {
			t.Errorf("no content type: %d %s", status, text)
		}
		big := `{"name":"acme","display_name":"` + strings.Repeat("x", 100) + `"}`
		if status, pb, text := r.raw(t, "POST", "/v1/tenants", "application/json", big); status != 413 || pb.Code != "request_too_large" {
			t.Errorf("over the limit: %d %s", status, text)
		}
		if status, _, text := r.raw(t, "POST", "/v1/tenants", "application/json; charset=utf-8", `{"name":"acme"}`); status != 201 {
			t.Errorf("a charset parameter must be accepted: %d %s", status, text)
		}
	})

	t.Run("paths that are no route", func(t *testing.T) {
		r := newRig(t, 1<<20)
		for _, p := range []string{
			"/v1/tenants/", "/v1/tenants//suspend", "/v1/tenants/not-a-uuid", "/v1/tenants/" + strings.ToUpper(tid),
			"/v1/tenants/" + tid + "/", "/v1/tenants/" + tid + "/service-accounts/not-a-uuid",
			"/v1/tenants/" + tid + "/virtual-keys/" + pk, "/v1/platform-keys/" + vk, "/v1/platform-keys/" + pk + "/", "/v1", "/", "/v2/tenants", "v1/tenants",
		} {
			if op, _ := adminapi.Match("GET", p); op != nil {
				t.Errorf("Match(GET, %s) = %s", p, op.ID)
			}
			if strings.HasPrefix(p, "/") {
				status, pb, text := r.raw(t, "GET", p, "", "")
				if status != 404 || pb.Code != "not_found" || pb.Op != "" {
					t.Errorf("GET %s: %d %s", p, status, text)
				}
			}
		}
		if status, _, text := r.raw(t, "DELETE", "/v1/tenants/"+tid, "", ""); status != 404 {
			t.Errorf("DELETE: %d %s", status, text)
		}
		op, params := adminapi.Match("GET", "/v1/tenants/"+tid+"/virtual-keys/"+vk)
		if op == nil || op.ID != "getVirtualKey" || params["tenant"] != tid || params["key"] != vk {
			t.Errorf("Match = %v %v", op, params)
		}
	})

	t.Run("an optional body may be empty", func(t *testing.T) {
		r := newRig(t, 1<<20)
		status, _, text := r.raw(t, "POST", "/v1/tenants/"+tid+"/suspend", "", "")
		if status != 200 {
			t.Fatalf("%d %s", status, text)
		}
		if got := r.f.last("suspendTenant"); !reflect.DeepEqual(got, adminapi.SuspendTenantInput{Tenant: tid}) {
			t.Errorf("got %+v", got)
		}
		if status, _, text := r.raw(t, "POST", "/v1/tenants", "", ""); status != 400 {
			t.Errorf("a required body may not be empty: %d %s", status, text)
		}
	})

	t.Run("nullable distinguishes absent, null and a value", func(t *testing.T) {
		r := newRig(t, 1<<20)
		path := "/v1/tenants/" + tid + "/service-accounts/" + sid
		get := func(body string) adminapi.ServiceAccountUpdate {
			t.Helper()
			if status, _, text := r.raw(t, "PATCH", path, "application/json", body); status != 200 {
				t.Fatalf("%s: %d %s", body, status, text)
			}
			return r.f.last("updateServiceAccount").(adminapi.UpdateServiceAccountInput).Body
		}
		if b := get(`{"name":"bot"}`); b.ValidFrom.Set || b.ValidUntil.Set {
			t.Errorf("absent: %+v", b)
		}
		if b := get(`{"valid_until":null}`); !b.ValidUntil.Set || !b.ValidUntil.Null || b.ValidFrom.Set {
			t.Errorf("null: %+v", b)
		}
		if b := get(`{"valid_from":"2026-11-13T09:04:05Z"}`); !b.ValidFrom.Set || b.ValidFrom.Null || !b.ValidFrom.Value.Equal(at(9)) {
			t.Errorf("value: %+v", b)
		}
		if status, _, _ := r.raw(t, "PATCH", path, "application/json", `{"valid_from":"tomorrow"}`); status != 400 {
			t.Errorf("a bad date: %d", status)
		}
		out, err := json.Marshal(adminapi.ServiceAccountUpdate{ValidUntil: adminapi.Null[time.Time]()})
		if err != nil || string(out) != `{"valid_until":null}` {
			t.Errorf("Marshal = %s, %v", out, err)
		}
		out, err = json.Marshal(adminapi.ServiceAccountUpdate{})
		if err != nil || string(out) != `{}` {
			t.Errorf("Marshal of an empty update = %s, %v", out, err)
		}
	})

	t.Run("defaults of limit and sort", func(t *testing.T) {
		r := newRig(t, 1<<20)
		if status, _, text := r.raw(t, "GET", "/v1/tenants", "", ""); status != 200 {
			t.Fatalf("%d %s", status, text)
		}
		want := adminapi.ListTenantsInput{Limit: 50, Sort: adminapi.NamedSortCreatedAt}
		if got := r.f.last("listTenants"); !reflect.DeepEqual(got, want) {
			t.Errorf("tenants: got %+v, want %+v", got, want)
		}
		if status, _, _ := r.raw(t, "GET", "/v1/platform-keys", "", ""); status != 200 {
			t.Fatal(status)
		}
		if got := r.f.last("listPlatformKeys"); !reflect.DeepEqual(got, adminapi.ListPlatformKeysInput{Limit: 50, Sort: adminapi.SortCreatedAt}) {
			t.Errorf("platform keys: %+v", got)
		}
	})

	t.Run("Before runs before the body is read", func(t *testing.T) {
		f := &fake{got: map[string]any{}}
		var seen *adminapi.Operation
		var params map[string]string
		denied := errors.New("denied")
		h := adminapi.NewHandler(f, adminapi.Hooks{
			Before: func(ctx context.Context, _ *http.Request, op *adminapi.Operation, p map[string]string) (context.Context, error) {
				seen, params = op, p
				return ctx, denied
			},
			Error: func(w http.ResponseWriter, _ *http.Request, _ *adminapi.Operation, err error) {
				if !errors.Is(err, denied) {
					t.Errorf("the Error hook got %v", err)
				}
				w.WriteHeader(http.StatusForbidden)
			},
			MaxBodyBytes: 1 << 20,
		})
		body := &countingReader{r: strings.NewReader(`{"name":"acme"}`)}
		req := httptest.NewRequest("POST", "/v1/tenants", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || body.reads != 0 {
			t.Errorf("status %d, body read %d times", rec.Code, body.reads)
		}
		if seen == nil || seen.ID != "createTenant" || len(params) != 0 {
			t.Errorf("Before saw %v %v", seen, params)
		}
		if got := f.last("createTenant"); got != nil {
			t.Error("the server was called")
		}
		// A bad query does not preempt authentication either.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/tenants?bogus=1", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("a bad query with a denied caller: %d", rec.Code)
		}
	})

	t.Run("the context of Before reaches the server", func(t *testing.T) {
		type key struct{}
		var got any
		srv := &ctxServer{fake: &fake{got: map[string]any{}}, seen: &got, key: key{}}
		h := adminapi.NewHandler(srv, adminapi.Hooks{
			Before: func(ctx context.Context, _ *http.Request, _ *adminapi.Operation, _ map[string]string) (context.Context, error) {
				return context.WithValue(ctx, key{}, "principal"), nil
			},
			Error:        func(w http.ResponseWriter, _ *http.Request, _ *adminapi.Operation, _ error) { w.WriteHeader(500) },
			MaxBodyBytes: 1024,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/me", nil))
		if rec.Code != 200 || got != "principal" {
			t.Errorf("status %d, context value %v", rec.Code, got)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type %q", ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control %q", cc)
		}
	})

	t.Run("NewHandler needs every hook", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewHandler accepted missing hooks")
			}
		}()
		adminapi.NewHandler(&fake{}, adminapi.Hooks{})
	})

	t.Run("the client returns problems", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"type":"urn:pandokos:problem:not_found","title":"Not found","status":404,"code":"not_found","detail":"The resource does not exist.","request_id":"abc"}`))
		}))
		defer srv.Close()
		c := &adminapi.Client{BaseURL: srv.URL, Key: func() string { return "k" }}
		_, err := c.GetTenant(context.Background(), adminapi.GetTenantInput{Tenant: tid})
		var pe *adminapi.ProblemError
		if !errors.As(err, &pe) || pe.Problem.Code != adminapi.ProblemCodeNotFound || pe.Problem.RequestID != "abc" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "not_found") || !strings.Contains(err.Error(), "abc") {
			t.Errorf("Error() = %q", err.Error())
		}
		// A server that is not there: the URL (which may carry a host name) is not repeated.
		srv.Close()
		_, err = c.GetTenant(context.Background(), adminapi.GetTenantInput{Tenant: tid})
		if err == nil || strings.Contains(err.Error(), srv.URL) {
			t.Errorf("unreachable: %v", err)
		}
	})
}

type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) { c.reads++; return c.r.Read(p) }

type ctxServer struct {
	*fake
	seen *any
	key  any
}

func (s *ctxServer) GetMe(ctx context.Context, in adminapi.GetMeInput) (*adminapi.Me, error) {
	*s.seen = ctx.Value(s.key)
	return s.fake.GetMe(ctx, in)
}
