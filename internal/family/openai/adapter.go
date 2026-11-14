package openai

import (
	"net/http"

	"github.com/pandokos/pandokos/internal/family"
)

// operations lists the metered endpoints of the family.
var operations = []family.Operation{
	{Name: OperationChatCompletions, Method: http.MethodPost, Path: PathChatCompletions},
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

func (adapter) NewStreamReader(op family.Operation, strip bool) family.StreamReader {
	return NewStreamReaderFor(op.Name, strip)
}

func (adapter) EstimateUsage(op family.Operation, e family.Evidence) family.Usage {
	return Estimate(op.Name, e)
}

func (adapter) WriteError(w http.ResponseWriter, code family.ErrorCode) { WriteError(w, code) }
