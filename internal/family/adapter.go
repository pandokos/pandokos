package family

import (
	"errors"
	"net/http"
)

// Operation is one metered endpoint of a family.
type Operation struct {
	Name   string // "chat.completions"
	Method string // "POST"
	Path   string // native path appended to the endpoint base URL: "/v1/chat/completions"
}

// Request is what a family read from a request body.
type Request interface {
	Model() string // as the caller wrote it, JSON-decoded
	Stream() bool
	// Prepare returns the body to forward: the caller's bytes, with the usage option added
	// when the family needs it (strip reports that) and the model replaced by upstreamModel
	// when that is not empty and differs from Model().
	Prepare(upstreamModel string) (body []byte, strip bool)
}

// StreamReader reads usage from a response stream and says what to forward.
type StreamReader interface {
	Filter(p []byte) []byte
	Flush() []byte
	Usage() (Usage, bool)
	Failed() bool
	// Deltas counts the content events seen so far, the evidence of an estimate.
	Deltas() int64
}

// Adapter is everything the gateway needs to know about one wire protocol (ADR 0009).
type Adapter interface {
	Name() string
	Operations() []Operation
	Match(method, path string) (Operation, bool)
	InboundCredential(h http.Header) (string, bool)
	AttachCredential(h http.Header, secret string)
	RequestHeaders() []string
	AllowResponseHeader(canonicalName string) bool
	ParseRequest(op Operation, body []byte) (Request, error) // wraps ErrInvalidRequest
	ReadUsage(op Operation, body []byte) (Usage, bool, error)
	NewStreamReader(op Operation, strip bool) StreamReader
	// EstimateUsage estimates the usage of a request whose provider reported none.
	EstimateUsage(op Operation, e Evidence) Usage
	WriteError(w http.ResponseWriter, code ErrorCode)
}

// ErrInvalidRequest is wrapped by every refusal of ParseRequest.
var ErrInvalidRequest = errors.New("family: invalid request")

// invalid is a refusal of ParseRequest with the reason that names the row of the contract.
type invalid struct{ reason string }

func (e invalid) Error() string { return "family: invalid request" }

// Unwrap makes errors.Is(err, ErrInvalidRequest) true.
func (e invalid) Unwrap() error { return ErrInvalidRequest }

// Invalid returns a refusal of ParseRequest with a reason such as "model_missing".
func Invalid(reason string) error { return invalid{reason: reason} }

// InvalidReason returns the reason of a refusal of ParseRequest ("" when err is not one).
func InvalidReason(err error) string {
	var e invalid
	if errors.As(err, &e) {
		return e.reason
	}
	return ""
}
