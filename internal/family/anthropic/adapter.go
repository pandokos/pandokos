package anthropic

import (
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

// operations lists the metered endpoints of the family.
var operations = []family.Operation{
	{Name: OperationMessages, Method: http.MethodPost, Path: PathMessages},
	{Name: OperationCountTokens, Method: http.MethodPost, Path: PathCountTokens, NoUsage: true},
}

// adapter implements family.Adapter by delegating to the functions of the package.
type adapter struct{}

// Adapter returns the Anthropic family as a family.Adapter.
func Adapter() family.Adapter { return adapter{} }

func (adapter) Name() string                   { return Name }
func (adapter) Operations() []family.Operation { return append([]family.Operation(nil), operations...) }

func (adapter) Match(method, path string) (family.Operation, bool) {
	name, ok := Match(method, path)
	if !ok {
		return family.Operation{}, false
	}
	for _, op := range operations {
		if op.Name == name {
			return op, true
		}
	}
	return family.Operation{}, false
}

func (adapter) InboundCredential(h http.Header) (string, bool) { return InboundCredential(h) }
func (adapter) AttachCredential(h http.Header, secret string)  { AttachCredential(h, secret) }
func (adapter) RequestHeaders() []string                       { return RequestHeaders }
func (adapter) AllowResponseHeader(canonicalName string) bool {
	return AllowResponseHeader(canonicalName)
}

func (adapter) ParseRequest(op family.Operation, body []byte) (family.Request, error) {
	r, err := ParseOperation(op.Name, body)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (adapter) ReadUsage(op family.Operation, body []byte) (family.Usage, bool, error) {
	return ReadUsageOf(op.Name, body)
}

// NewStreamReader ignores strip: Anthropic streams always report usage and nothing is added.
func (adapter) NewStreamReader(_ family.Operation, _ bool) family.StreamReader {
	return NewStreamReader()
}

func (adapter) EstimateUsage(op family.Operation, e family.Evidence) family.Usage {
	return Estimate(op.Name, e)
}

func (adapter) WriteError(w http.ResponseWriter, code family.ErrorCode) { WriteError(w, code) }

// Recognizes reports whether a request looks like a call of this family. The stub recognizes
// nothing.
func (adapter) Recognizes(_, _ string, _ http.Header) bool { return false }

// UpstreamRequestID returns the provider's Request-Id header value, unchecked.
func (adapter) UpstreamRequestID(h http.Header) string { return h.Get("Request-Id") }
