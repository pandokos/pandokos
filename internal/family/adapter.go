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
	// NoUsage marks an operation that consumes no tokens (token counting): its records carry no
	// usage, no estimate and no cost.
	NoUsage bool
}

// Parts says which totals of a Usage the provider reported.
type Parts uint8

// The parts of a Usage a provider can report.
const (
	PartInput  Parts = 1 << iota // InputTokens, CacheReadTokens, CacheWriteTokens
	PartOutput                   // OutputTokens, ReasoningTokens
	PartsAll   = PartInput | PartOutput
)

// Preparation is what Request.Prepare needs from the catalogue.
type Preparation struct {
	UpstreamModel string // replaces the model when not empty and different from Model()
	StreamUsage   bool   // the provider accepts the stream usage option (openai family only)
}

// Request is what a family read from a request body.
type Request interface {
	Model() string // as the caller wrote it, JSON-decoded
	Stream() bool
	// Prepare returns the body to forward: the caller's bytes, with the usage option added
	// when the family needs it and p.StreamUsage allows it (strip reports that) and the model
	// replaced by p.UpstreamModel when that is not empty and differs from Model().
	Prepare(p Preparation) (body []byte, strip bool)
}

// StreamReader reads usage from a response stream and says what to forward.
type StreamReader interface {
	Filter(p []byte) []byte
	Flush() []byte
	// Usage returns the usage read so far and which parts of it the provider reported.
	Usage() (Usage, Parts)
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
	// Recognizes reports whether a request that reached no provider looks like a call of this
	// family. It reads the method, the native path and the header only.
	Recognizes(method, nativePath string, h http.Header) bool
	// UpstreamRequestID returns the provider's request ID header value, unchecked; "" if none.
	UpstreamRequestID(h http.Header) string
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
