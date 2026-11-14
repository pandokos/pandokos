package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/pandokos/pandokos/internal/family"
)

// maxResponsesEventBytes is the size bound of a buffered JSON answer: the terminal event repeats
// the whole response.
const maxResponsesEventBytes = 4 << 20

// NewStreamReaderFor returns the stream reader of an operation of the family; strip says
// whether usage-only events are removed (chat completions only).
func NewStreamReaderFor(operation string, strip bool) family.StreamReader {
	if operation == OperationResponses {
		return &ResponsesStreamReader{framing: framing{max: maxResponsesEventBytes}}
	}
	return NewStreamReader(strip)
}

// ResponsesStreamReader reads usage from a responses event stream. It never strips: it forwards
// what it is given.
type ResponsesStreamReader struct {
	framing
	usage    family.Usage
	hasUsage bool
	deltas   int64
}

type responsesEvent struct {
	Type     string `json:"type"`
	Response *struct {
		Usage json.RawMessage `json:"usage"`
	} `json:"response"`
}

// Filter reads the stream and returns p.
func (r *ResponsesStreamReader) Filter(p []byte) []byte {
	if r.failed {
		return p
	}
	r.split(p, r.inspect)
	return p
}

// Flush reads an event the stream ended in the middle of and returns nil.
func (r *ResponsesStreamReader) Flush() []byte {
	if r.failed {
		return nil
	}
	if b := r.take(); len(b) > 0 {
		r.inspect(b)
	}
	return nil
}

var (
	usageMark = []byte(`"usage"`)
	deltaMark = []byte(`.delta"`)
)

func (r *ResponsesStreamReader) inspect(ev []byte) {
	data := eventData(ev)
	if len(data) == 0 || data[0] != '{' || (!bytes.Contains(data, usageMark) && !bytes.Contains(data, deltaMark)) {
		return
	}
	var e responsesEvent
	if json.Unmarshal(data, &e) != nil {
		return
	}
	if strings.HasSuffix(e.Type, ".delta") {
		r.deltas++
	}
	switch e.Type {
	case "response.completed", "response.incomplete", "response.failed":
	default:
		return
	}
	if e.Response == nil || isAbsent(e.Response.Usage) {
		return
	}
	if u, err := normalizeUsage(OperationResponses, e.Response.Usage); err == nil {
		r.usage, r.hasUsage = u, true
	}
}

// Usage returns the usage read so far; ok is false when none was seen.
func (r *ResponsesStreamReader) Usage() (family.Usage, bool) { return r.usage, r.hasUsage }

// Failed reports that the reader gave up because an event exceeded the size bound.
func (r *ResponsesStreamReader) Failed() bool { return r.failed }

// Deltas counts the events whose type ends in .delta.
func (r *ResponsesStreamReader) Deltas() int64 { return r.deltas }
