package anthropic

import "github.com/pandokos/pandokos/internal/family"

// Request is a Messages API request body that was read.
type Request struct {
	body   []byte
	model  string
	stream bool
}

// ParseOperation reads the request body of an operation of the family. The stub refuses every
// body with the reason not_object.
func ParseOperation(_ string, _ []byte) (*Request, error) {
	return nil, family.Invalid("not_object")
}

// Model returns the model as the caller wrote it, JSON-decoded.
func (r *Request) Model() string { return r.model }

// Stream reports whether the caller asked for a stream.
func (r *Request) Stream() bool { return r.stream }

// Prepare returns the body to forward. The stub returns it unchanged and never strips.
func (r *Request) Prepare(_ family.Preparation) (body []byte, strip bool) { return r.body, false }
