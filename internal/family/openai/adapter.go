package openai

import (
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

// operations lists the metered endpoints of the family.
var operations = []family.Operation{
	{Name: OperationChatCompletions, Method: http.MethodPost, Path: "/v1" + PathChatCompletions},
}

// adapter implements family.Adapter by delegating to the functions of the package.
type adapter struct{}

// Adapter returns the OpenAI family as a family.Adapter.
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

func (adapter) ParseRequest(_ family.Operation, body []byte) (family.Request, error) {
	r, err := Parse(body)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (adapter) ReadUsage(_ family.Operation, body []byte) (family.Usage, bool, error) {
	return ReadUsage(body)
}

func (adapter) NewStreamReader(_ family.Operation, strip bool) family.StreamReader {
	return NewStreamReader(strip)
}

func (adapter) WriteError(w http.ResponseWriter, code family.ErrorCode) { WriteError(w, code) }
