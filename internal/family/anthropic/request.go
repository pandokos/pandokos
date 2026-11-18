package anthropic

import (
	"github.com/pandokos/pandokos/internal/family"
)

// Request is a Messages API request body that was read.
type Request struct {
	body   []byte
	model  string
	stream bool
	modelV family.Member // byte range of the value of the model member
	bound  family.OutputBound
}

// ParseOperation reads the request body of an operation of the family. Every error wraps
// family.ErrInvalidRequest and carries a reason (family.InvalidReason): not_object,
// model_missing, model_duplicate or model_not_string. The body is not modified and is kept by the
// Request. Token counting never streams.
func ParseOperation(operation string, body []byte) (*Request, error) {
	members, _, err := family.ScanObject(body)
	if err != nil {
		return nil, family.Invalid("not_object")
	}
	model, mm, err := family.ParseModel(body, members)
	if err != nil {
		return nil, err
	}
	var stream bool
	bound := family.OutputBound{Choices: 1}
	if operation == OperationCountTokens {
		bound.None = true
	} else {
		bound.Tokens, _ = family.PositiveInt(body, members, "max_tokens", 12)
	}
	if operation == OperationMessages {
		if sm, ok := family.LastMember(members, "stream"); ok {
			stream = family.IsLiteral(body, sm, "true")
		}
	}
	return &Request{body: body, model: model, stream: stream, modelV: mm, bound: bound}, nil
}

// Model returns the model as the caller wrote it, JSON-decoded.
func (r *Request) Model() string { return r.model }

// Stream reports whether the caller asked for a stream.
func (r *Request) Stream() bool { return r.stream }

// OutputBound returns what the request says about the output it may produce.
func (r *Request) OutputBound() family.OutputBound { return r.bound }

// Prepare returns the body to forward: the value of the model member replaced by p.UpstreamModel
// when that is not empty and differs from Model(); every other byte as received, and the input
// slice itself when nothing changes. Anthropic streams always report usage, so strip is false.
func (r *Request) Prepare(p family.Preparation) (body []byte, strip bool) {
	if p.UpstreamModel == "" || p.UpstreamModel == r.model {
		return r.body, false
	}
	return family.Splice(r.body, r.modelV.Start, r.modelV.End, family.EncodeString(p.UpstreamModel)), false
}
