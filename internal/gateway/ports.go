package gateway

import (
	"context"
	"errors"
	"net/url"
	"sort"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/usage"
)

// Target is where an authorized call goes. Every field comes from the catalogue.
type Target struct {
	Provider, Endpoint string // names
	ModelID, Model     string // catalogue ID and public name
	UpstreamModel      string // what the provider receives as model
	BaseURL            *url.URL
	Private            bool
	Prices             []pricing.Version // the model's price versions, from the snapshot that resolved it; read-only
}

// Recorder receives the usage record of every admitted request.
type Recorder interface {
	// Admit reserves room for the record of one request. When it returns false the request
	// is refused and Record is not called.
	Admit() bool
	// Record takes the record of an admitted request. It does not block.
	Record(r usage.Record)
}

// SpanAttributes lists every attribute key the gateway sets on a span.
var SpanAttributes = []string{
	// server span
	"http.request.method", "http.route", "http.response.status_code", "pandokos.request.id",
	"pandokos.record.id", "pandokos.tenant.id", "pandokos.principal.id", "pandokos.key.id",
	"pandokos.provider", "pandokos.operation", "pandokos.outcome", "pandokos.error.code",
	// provider span (the keys it shares with the server span are listed once)
	"pandokos.endpoint", "pandokos.model", "pandokos.stream", "pandokos.credential.id",
	"pandokos.funding", "pandokos.upstream.error", "pandokos.ttfb_ms", "pandokos.usage.source",
	"pandokos.cost.status", "pandokos.cost.amount", "pandokos.cost.currency",
}

// Catalogue is the gateway's view of the catalogue.
type Catalogue interface {
	// Provider returns the API family of an enabled provider; ok is false for a name that is
	// unknown or disabled. An error means the catalogue could not be read.
	Provider(ctx context.Context, name string) (family string, ok bool, err error)
	// Resolve returns the target for a model. ErrModelNotAllowed and ErrProviderUnavailable are
	// refusals; any other error means the decision could not be taken.
	Resolve(ctx context.Context, p authz.Principal, provider, model string) (Target, error)
}

// Credential is an opened provider credential.
type Credential struct {
	ID     string
	Pinned bool
	Secret secret.Secret
}

// Credentials opens the credential a call is made with.
type Credentials interface {
	// Open returns ErrNoCredential, ErrCredentialDenied, or another error when the credential
	// cannot be verified now.
	Open(ctx context.Context, p authz.Principal, requestID, provider string) (Credential, error)
}

// AuthFailure is a refused credential that names a key of a tenant.
type AuthFailure struct{ RequestID, Reason, KeyID, TenantID string }

// FailureRecorder records authentication failures; it reports whether a record was written.
type FailureRecorder interface {
	Record(ctx context.Context, f AuthFailure) bool
}

// The refusals the gateway distinguishes.
var (
	ErrModelNotAllowed     = errors.New("gateway: model not allowed")
	ErrProviderUnavailable = errors.New("gateway: provider unavailable")
	ErrNoCredential        = errors.New("gateway: no usable credential")
	ErrCredentialDenied    = errors.New("gateway: the principal may not use a credential")
)

// Route is one route of the gateway and the permissions it requires.
type Route struct {
	Method      string
	Path        string // "/{provider}" + native path
	Permissions []string
}

// Routes lists, for every operation of every family, the route and its permissions.
func Routes() []Route {
	var routes []Route
	names := make([]string, 0, len(knownAdapters))
	for name := range knownAdapters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, op := range knownAdapters[name].Operations() {
			routes = append(routes, Route{Method: op.Method, Path: "/{provider}" + op.Path, Permissions: []string{authz.ModelInvoke, authz.CredentialUse}})
		}
	}
	return routes
}

// knownAdapters are the adapters of the families this build serves.
var knownAdapters = map[string]family.Adapter{openai.Name: openai.Adapter()}
