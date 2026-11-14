package apitest

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

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
}

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
	path = strings.ReplaceAll(path, "{price_import}", p.PriceImport)
	if strings.HasPrefix(op.Path, "/v1/platform-keys/") {
		path = strings.ReplaceAll(path, "{key}", p.PlatformKey)
	} else {
		path = strings.ReplaceAll(path, "{key}", p.VirtualKey)
	}
	return Call{Method: op.Method, Path: path + query, Body: body}
}
