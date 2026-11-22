package apitest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/dbtest"
)

// Doer is what makes a call as one identity: a Caller (a key) or a Browser (a session).
type Doer interface {
	Do(tb testing.TB, method, path string, body any, hdr http.Header) *Response
}

// Caller makes calls with one key.
type Caller struct {
	API  *API
	Key  string
	Name string // what the identity is, for messages: the role it was made with
}

// Do sends the request with the caller's key.
func (c *Caller) Do(tb testing.TB, method, path string, body any, hdr http.Header) *Response {
	tb.Helper()
	return c.API.Do(tb, method, path, c.Key, body, hdr)
}

var (
	_ Doer = (*Caller)(nil)
	_ Doer = (*Browser)(nil)
)

// OwnerKey returns the key of the first service account of a tenant made by Tenant: the owner.
func (a *API) OwnerKey(tb testing.TB, tenantID string) string {
	tb.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	k, ok := a.owners[tenantID]
	if !ok {
		tb.Fatalf("apitest: tenant %s was not made by API.Tenant: its owner key is not known", tenantID)
	}
	return k
}

// Owner returns a caller that holds the key of the tenant's owner.
func (a *API) Owner(tb testing.TB, tenantID string) *Caller {
	tb.Helper()
	return &Caller{API: a, Key: a.OwnerKey(tb, tenantID), Name: "owner"}
}

// created decodes the JSON of a response that must have the status.
func created(tb testing.TB, r *Response, status int, into any) {
	tb.Helper()
	if r.Status != status {
		tb.Fatalf("status %d, want %d\n%s", r.Status, status, r.Body)
	}
	r.JSON(tb, into)
}

// AsRole creates, through the API as the tenant's owner, a service account that holds the role (a
// built-in tenant role or a custom role of the tenant) and one key for it, and returns a caller
// with the key.
func (a *API) AsRole(tb testing.TB, tenantID, role string) *Caller {
	tb.Helper()
	owner := a.Owner(tb, tenantID)
	var sa struct {
		ID string `json:"id"`
	}
	created(tb, owner.Do(tb, http.MethodPost, "/v1/tenants/"+tenantID+"/service-accounts", map[string]any{"name": UniqueName("as-" + role), "roles": []string{role}}, nil), 201, &sa)
	var key struct {
		Secret string `json:"secret"`
	}
	created(tb, owner.Do(tb, http.MethodPost, "/v1/tenants/"+tenantID+"/virtual-keys", map[string]any{"service_account_id": sa.ID}, nil), 201, &key)
	a.track(key.Secret)
	return &Caller{API: a, Key: key.Secret, Name: role}
}

// MemberAs creates, through the API as the tenant's owner, a member with a new local account that
// holds the role, signs it in and changes the generated password, and returns the browser of the
// session. The API must have been made by NewPeople.
func (a *API) MemberAs(tb testing.TB, tenantID, role string) *Browser {
	tb.Helper()
	return a.MemberWith(tb, tenantID, role)
}

// MemberWith is MemberAs for any number of roles.
func (a *API) MemberWith(tb testing.TB, tenantID string, roles ...string) *Browser {
	tb.Helper()
	if a.People == nil {
		tb.Fatal("apitest: MemberAs needs an API made by NewPeople")
	}
	username := UniqueName("member")
	var m struct {
		Member struct {
			UserID string `json:"user_id"`
		} `json:"member"`
		InitialPassword string `json:"initial_password"`
	}
	created(tb, a.Owner(tb, tenantID).Do(tb, http.MethodPost, "/v1/tenants/"+tenantID+"/members", map[string]any{"account": "new", "username": username, "roles": roles}, nil), 201, &m)
	b := a.NewBrowser()
	b.MustSignIn(tb, username, m.InitialPassword)
	if r := b.Do(tb, http.MethodPost, "/v1/auth/password", map[string]any{"current_password": m.InitialPassword, "new_password": dbtest.NewPassword(tb)}, nil); r.Status != 200 {
		tb.Fatalf("the first password change of %s: %d %s", username, r.Status, r.Body)
	}
	return b
}

// MemberID returns the user ID of the member that MemberAs or MemberWith made, from its session.
func (a *API) MemberID(tb testing.TB, b *Browser) string {
	tb.Helper()
	var me struct {
		ID string `json:"id"`
	}
	created(tb, b.Do(tb, http.MethodGet, "/v1/me", nil, nil), 200, &me)
	return me.ID
}

// PlatformAs makes a platform key and a platform user that hold the platform role, and returns the
// caller of the key and the browser of the user (signed in). The key is issued with the bootstrap
// key; the user is seeded.
func (a *API) PlatformAs(tb testing.TB, role string) (*Caller, *Browser) {
	tb.Helper()
	var key struct {
		Secret string `json:"secret"`
	}
	created(tb, a.Do(tb, http.MethodPost, "/v1/platform-keys", a.platformKey, map[string]any{"description": UniqueName("as-" + role), "role": role}, nil), 201, &key)
	a.track(key.Secret)
	p := a.DB.PlatformUser(tb, UniqueName("platform-user"), []string{role})
	b := a.NewBrowser()
	b.MustSignIn(tb, p.Username, p.Password)
	return &Caller{API: a, Key: key.Secret, Name: role}, b
}

// CustomRole creates a custom role of the tenant with the permissions, through the API as the
// tenant's owner, and returns its name.
func (a *API) CustomRole(tb testing.TB, tenantID string, permissions ...string) string {
	tb.Helper()
	name := UniqueName("custom")
	r := a.Owner(tb, tenantID).Do(tb, http.MethodPost, "/v1/tenants/"+tenantID+"/roles", map[string]any{"name": name, "permissions": permissions}, nil)
	if r.Status != 201 {
		tb.Fatalf("create the role %s with %v: %d %s", name, permissions, r.Status, r.Body)
	}
	return name
}

// OpenSupport opens a support action in the tenant with the permissions, as the caller (a platform
// key or a platform user), with a reason made for this call, and returns the ID of the action.
func OpenSupport(tb testing.TB, c Doer, tenantID string, permissions ...string) string {
	tb.Helper()
	var sa struct {
		ID string `json:"id"`
	}
	r := c.Do(tb, http.MethodPost, "/v1/tenant-support/"+tenantID+"/actions", map[string]any{"reason": Reason("open"), "permissions": permissions}, nil)
	created(tb, r, 201, &sa)
	return sa.ID
}

// Reason returns a reason made for this call: random, so that a test that searches a log for it
// finds nothing else.
func Reason(kind string) string {
	return fmt.Sprintf("%s %s", strings.ReplaceAll(kind, " ", "-"), Canary("reason"))
}

// SuspendTenant suspends the tenant with the key and a reason (required since sprint 4.3).
func (a *API) SuspendTenant(tb testing.TB, key, tenantID string) (*adminapi.Tenant, error) {
	tb.Helper()
	return a.setTenantState(tb, key, tenantID, "suspend")
}

// ReactivateTenant reactivates the tenant with the key and a reason (required since sprint 4.3).
func (a *API) ReactivateTenant(tb testing.TB, key, tenantID string) (*adminapi.Tenant, error) {
	tb.Helper()
	return a.setTenantState(tb, key, tenantID, "reactivate")
}

func (a *API) setTenantState(tb testing.TB, key, tenantID, verb string) (*adminapi.Tenant, error) {
	tb.Helper()
	r := a.Do(tb, http.MethodPost, "/v1/tenants/"+tenantID+"/"+verb, key, map[string]any{"reason": Reason(verb)}, nil)
	if r.Status != 200 {
		return nil, fmt.Errorf("%s: %d %s", verb, r.Status, r.Body)
	}
	var t adminapi.Tenant
	r.JSON(tb, &t)
	return &t, nil
}

// PlatformCaller returns a caller that holds the bootstrap platform key.
func (a *API) PlatformCaller() *Caller {
	return &Caller{API: a, Key: a.platformKey, Name: "platform-admin"}
}
