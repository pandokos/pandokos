package openai

import (
	"encoding/json"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/sse"
)

const maxEventBytes = 1 << 20

type chunk struct {
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
}

// StreamReader reads usage from a chat completions event stream, incrementally. With strip
// set it also removes the usage-only event; otherwise it is a pure tee.
type StreamReader struct {
	sse.Framing
	strip    bool
	out      []byte // reused output buffer (strip mode)
	usage    family.Usage
	hasUsage bool
	deltas   int64
}

// NewStreamReader returns a reader; strip says whether usage-only events are removed.
func NewStreamReader(strip bool) *StreamReader {
	return &StreamReader{Framing: sse.Framing{Max: maxEventBytes}, strip: strip}
}

// Filter takes the next bytes of the stream and returns the bytes to forward; the result is
// valid until the next call.
func (r *StreamReader) Filter(p []byte) []byte {
	if r.Failed() {
		return p
	}
	r.out = r.out[:0]
	overflow := r.Split(p, func(ev []byte) {
		usageOnly := r.inspect(ev)
		if r.strip && !usageOnly {
			r.out = append(r.out, ev...)
		}
	})
	if !r.strip {
		return p
	}
	return append(r.out, overflow...)
}

// Flush returns the bytes still held at the end of the stream. An event the stream ended in the
// middle of is still read for its usage, and forwarded, unless it is the usage-only event that
// Pandokos asked for: that one is removed in strip mode, terminated or not.
func (r *StreamReader) Flush() []byte {
	if r.Failed() {
		return nil
	}
	b := r.Take()
	usageOnly := len(b) > 0 && r.inspect(b)
	if !r.strip || usageOnly {
		return nil
	}
	return b
}

// inspect records the usage carried by an event and counts it among the content events, and
// reports whether the event is a usage-only event.
func (r *StreamReader) inspect(ev []byte) bool {
	data := sse.Data(ev)
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil {
		return false
	}
	if len(c.Choices) > 0 {
		r.deltas++
	}
	if isAbsent(c.Usage) {
		return false
	}
	u, err := normalizeUsage(OperationChatCompletions, c.Usage) // error on a negative count
	if err != nil {
		return false
	}
	r.usage, r.hasUsage = u, true
	return c.Choices != nil && len(c.Choices) == 0
}

// Usage returns the usage read so far; the parts are PartsAll, or 0 when none was seen.
func (r *StreamReader) Usage() (family.Usage, family.Parts) { return r.usage, partsOf(r.hasUsage) }

// Failed reports that the reader gave up because an event exceeded the size bound.
func (r *StreamReader) Failed() bool { return r.Framing.Failed() }

// Deltas counts the chunks with a non-empty choices array seen so far.
func (r *StreamReader) Deltas() int64 { return r.deltas }
