package apitest

import (
	"crypto/rand"
	"encoding/hex"
	mrand "math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/test/fixtures"
)

// Params names the resources a call is made on.
type Params struct {
	Tenant         string // {tenant}
	ServiceAccount string // {service_account}, and the service_account_id of a new key
	VirtualKey     string // {key} of a virtual key
	PlatformKey    string // {key} of a platform key
	Credential     string // {credential}
	Provider       string // {provider} (a name)
	Endpoint       string // {endpoint} (a name), and the endpoint of {provider}
	Model          string // {model} (an identifier), and the model of a new price override
	PriceVersion   string // {price_version}
	PriceImport    string // {price_import}
	TokenIssuer    string // {issuer} of a tenant-bound trusted issuer, and the issuer of a new mapping
	SubjectMapping string // {mapping}
	PlatformIssuer string // {issuer} of a platform-level issuer
	LimitRule      string // {rule} of a limit rule of the tenant
	PlatformLimit  string // {rule} of a platform limit rule
	TenantPrice    string // {price} of a scheduled price version of the tenant's price lists
	User           string // {user}; NoUser when empty
	Session        string // {session}; NoUser when empty
}

// NoUser is the {user} and {session} of a call whose Params name none: a well-formed identifier
// that no row has.
const NoUser = "0192f0f0-0000-7000-8000-00000000000b"

// randomPassword returns a password made when the test runs; no test password is written down.
func randomPassword(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// NoLimitRule is the {rule} of a call whose Params name no limit rule: a well-formed identifier
// that no row has.
const NoLimitRule = "0192f0f0-0000-7000-8000-00000000000a"

// Call is how to make a valid call of one operation.
type Call struct {
	Method string
	Path   string
	Body   any // nil for no body
}

var unique atomic.Int64

// UniqueName returns a valid resource name that has not been returned before in this process.
func UniqueName(prefix string) string {
	n := unique.Add(1)
	s := strings.ToLower(strings.TrimSpace(prefix))
	return s + "-" + strconv.FormatInt(n, 10)
}

// CallFor returns a valid call of op on the resources in p, with a valid body where the
// operation takes one. An operation that this table does not know fails the test: the tests
// over every operation must be extended when an operation is added, never skipped.
func CallFor(tb testing.TB, op adminapi.Operation, p Params) Call {
	tb.Helper()
	var body any
	var query string
	switch op.ID {
	case "getMe", "listTenants", "getTenant", "listServiceAccounts", "getServiceAccount",
		"listVirtualKeys", "getVirtualKey", "listPlatformKeys", "getPlatformKey":
	case "createTenant":
		body = map[string]any{"name": UniqueName("created")}
	case "updateTenant":
		body = map[string]any{"display_name": "changed by a test"}
	case "suspendTenant", "reactivateTenant", "disableServiceAccount", "enableServiceAccount",
		"revokeVirtualKey", "revokePlatformKey":
		body = map[string]any{}
	case "createServiceAccount":
		body = map[string]any{"name": UniqueName("sa"), "roles": []string{"inference"}}
	case "updateServiceAccount":
		body = map[string]any{"description": "changed by a test"}
	case "createVirtualKey":
		body = map[string]any{"service_account_id": p.ServiceAccount}
	case "createPlatformKey":
		body = map[string]any{"description": "created by a test"}
	case "listCredentials", "getCredential", "deleteCredential", "listTenantCredentials", "getTenantCredential":
	case "createCredential":
		body = map[string]any{"provider": "openai", "label": UniqueName("cred"), "secret": fixtures.CanaryCredential}
	case "replaceCredentialSecret":
		body = map[string]any{"secret": fixtures.CanaryCredential}
	case "updateCredential":
		body = map[string]any{"description": "changed by a test"}
	case "disableCredential", "enableCredential", "setDefaultCredential", "revokeCredential":
		body = map[string]any{}
	case "disableTenantCredential", "revokeTenantCredential":
		body = map[string]any{"reason": "test"}
	case "listProviders", "getProvider", "deleteProvider", "listEndpoints", "getEndpoint", "deleteEndpoint",
		"listModels", "getModel", "deleteModel", "listModelGrants", "grantModel", "revokeModelGrant",
		"listPriceVersions", "deletePriceVersion", "listPriceImports", "getPriceImport",
		"listTenantModels", "getModelRestriction", "clearModelRestriction", "listUsageRecords":
	case "getCapability":
		query = "?provider=openai&model=gpt-4o-mini"
	case "createProvider":
		body = map[string]any{"name": UniqueName("synthetic"), "api_family": "openai"}
	case "createEndpoint":
		body = map[string]any{"name": UniqueName("ep"), "base_url": "https://synthetic.invalid"}
	case "createModel":
		body = map[string]any{"provider": p.Provider, "name": UniqueName("synthetic-model")}
	case "updateProvider", "updateEndpoint", "updateModel":
		body = map[string]any{"description": "changed by a test"}
	case "createPriceOverride":
		body = map[string]any{"currency": "USD", "rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "1"}}}
	case "clearPriceOverride":
		body = map[string]any{}
	case "runPriceImport":
		body = map[string]any{"source": "embedded", "dry_run": true}
	case "setModelRestriction":
		body = map[string]any{"models": []string{}}
	case "listTokenIssuers", "getTokenIssuer", "deleteTokenIssuer", "listTenantPlatformIssuers", "listSubjectMappings",
		"getSubjectMapping", "deleteSubjectMapping", "listPlatformIssuers", "getPlatformIssuer", "deletePlatformIssuer",
		"listPlatformIssuerTenants", "deletePlatformIssuerTenant":
	case "createTokenIssuer":
		n := unique.Add(1)
		body = map[string]any{"name": UniqueName("issuer"), "issuer": "https://created-" + strconv.FormatInt(n, 10) + ".example.test", "audience": "created-" + strconv.FormatInt(n, 10)}
	case "updateTokenIssuer", "updatePlatformIssuer":
		body = map[string]any{"description": "changed by a test"}
	case "createPlatformIssuer":
		n := unique.Add(1)
		body = map[string]any{"name": UniqueName("platform"), "issuer": "https://created-platform-" + strconv.FormatInt(n, 10) + ".example.test",
			"audience": "created-platform-" + strconv.FormatInt(n, 10), "tenant_claim": "org"}
	case "createSubjectMapping":
		body = map[string]any{"issuer": map[string]any{"scope": "tenant", "id": p.TokenIssuer}, "subject": UniqueName("subject"), "service_account_id": p.ServiceAccount}
	case "putPlatformIssuerTenant":
		body = map[string]any{}
	case "listLimits", "getLimit", "deleteLimit", "listTenantPlatformLimits", "listLimitEvents",
		"listPlatformLimits", "getPlatformLimit", "deletePlatformLimit":
	case "createLimit", "createPlatformLimit":
		body = map[string]any{"metric": "requests_per_day", "limit": 1000}
	case "updateLimit", "updatePlatformLimit":
		body = map[string]any{"limit": 2000}
	case "getTenantTimeZone":
	case "setTenantTimeZone":
		body = map[string]any{"time_zone": ""}
	case "listPriceLists", "listPriceListVersions", "getPriceListVersion", "deletePriceListVersion", "getActiveCurrency":
	case "createPriceListVersion":
		// A random start: two calls with the same start in one list and model collide on the unique key.
		start := time.Now().UTC().Add(time.Hour + time.Duration(mrand.Int63n(int64(24*time.Hour*365)))).Truncate(time.Second)
		body = map[string]any{"provider": "openai", "model": "gpt-4o-mini", "currency": "EUR", "valid_from": start.Format(time.RFC3339),
			"rates": []map[string]any{{"category": "input", "threshold": 0, "rate": "1"}}}
	case "setActiveCurrency":
		// The default of the installation: always accepted, and it changes nothing.
		body = map[string]any{"currency": ""}
	case "getAuthMethods", "startOidcSignIn", "completeOidcSignIn", "signOut", "listOwnSessions", "deleteOwnSession",
		"listUsers", "getUser", "listMembers", "getMember", "deleteMember":
	case "signIn":
		body = map[string]any{"username": UniqueName("nobody"), "password": randomPassword(tb)}
	case "changePassword":
		body = map[string]any{"current_password": randomPassword(tb), "new_password": randomPassword(tb)}
	case "createUser":
		body = map[string]any{"sign_in": "local", "username": UniqueName("person")}
	case "createMember":
		body = map[string]any{"account": "new", "username": UniqueName("member"), "roles": []string{"tenant-admin"}}
	case "updateUser", "updateMember":
		body = map[string]any{"display_name": "changed by a test"}
	case "disableUser", "enableUser", "unlockUser", "resetUserPassword", "revokeUserSessions",
		"disableMember", "enableMember", "unlockMember", "resetMemberPassword", "revokeMemberSessions":
		body = map[string]any{}
	default:
		tb.Fatalf("apitest.CallFor does not know the operation %s: add it", op.ID)
	}
	path := strings.ReplaceAll(op.Path, "{tenant}", p.Tenant)
	path = strings.ReplaceAll(path, "{credential}", p.Credential)
	path = strings.ReplaceAll(path, "{service_account}", p.ServiceAccount)
	path = strings.ReplaceAll(path, "{provider}", p.Provider)
	path = strings.ReplaceAll(path, "{endpoint}", p.Endpoint)
	path = strings.ReplaceAll(path, "{model}", p.Model)
	path = strings.ReplaceAll(path, "{price_version}", p.PriceVersion)
	path = strings.ReplaceAll(path, "{price}", p.TenantPrice)
	path = strings.ReplaceAll(path, "{price_import}", p.PriceImport)
	path = strings.ReplaceAll(path, "{mapping}", p.SubjectMapping)
	path = strings.ReplaceAll(path, "{user}", orDefault(p.User, NoUser))
	path = strings.ReplaceAll(path, "{session}", orDefault(p.Session, NoUser))
	rule := p.LimitRule
	if strings.HasPrefix(op.Path, "/v1/platform-limits/") {
		rule = p.PlatformLimit
	}
	if rule == "" {
		rule = NoLimitRule
	}
	path = strings.ReplaceAll(path, "{rule}", rule)
	if strings.Contains(op.Path, "/platform-issuers/{issuer}") {
		path = strings.ReplaceAll(path, "{issuer}", p.PlatformIssuer)
	} else {
		path = strings.ReplaceAll(path, "{issuer}", p.TokenIssuer)
	}
	if strings.HasPrefix(op.Path, "/v1/platform-keys/") {
		path = strings.ReplaceAll(path, "{key}", p.PlatformKey)
	} else {
		path = strings.ReplaceAll(path, "{key}", p.VirtualKey)
	}
	return Call{Method: op.Method, Path: path + query, Body: body}
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
