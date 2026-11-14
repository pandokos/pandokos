package anthropic

import (
	"bytes"
	"encoding/json"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/sse"
)

const maxEventBytes = 1 << 20

// event is the part of a stream event the reader looks at.
type event struct {
	Type    string `json:"type"`
	Message struct {
		Usage json.RawMessage `json:"usage"`
	} `json:"message"`
	Usage json.RawMessage `json:"usage"`
}

// StreamReader reads usage from a Messages event stream, incrementally. It only observes: every
// byte is forwarded as received, and it never breaks the stream (after an event passes the size
// bound it stops reading).
type StreamReader struct {
	sse.Framing
	cur    map[string]json.RawMessage // the current usage object
	usage  family.Usage               // cur, normalized
	parts  family.Parts
	deltas int64
}

// NewStreamReader returns a reader.
func NewStreamReader() *StreamReader {
	return &StreamReader{Framing: sse.Framing{Max: maxEventBytes}}
}

// Filter takes the next bytes of the stream and returns them unchanged.
func (r *StreamReader) Filter(p []byte) []byte {
	if r.Framing.Failed() {
		return p
	}
	r.Split(p, r.inspect)
	return p
}

// Flush returns the bytes still held at the end of the stream: none, as nothing is withheld. An
// event the stream ended in the middle of is not read.
func (r *StreamReader) Flush() []byte {
	r.Take()
	return nil
}

var (
	usageKey = []byte(`"usage"`)
	deltaKey = []byte(`"content_block_delta"`)
)

// inspect reads one complete event.
func (r *StreamReader) inspect(ev []byte) {
	data := sse.Data(ev)
	if len(data) == 0 || data[0] != '{' || (!bytes.Contains(data, usageKey) && !bytes.Contains(data, deltaKey)) {
		return
	}
	var e event
	if err := json.Unmarshal(data, &e); err != nil {
		return
	}
	switch e.Type {
	case "content_block_delta":
		r.deltas++
	case "message_start":
		if isAbsent(e.Message.Usage) {
			r.cur, r.usage, r.parts = nil, family.Usage{}, 0
			return
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(e.Message.Usage, &obj) != nil || obj == nil {
			return
		}
		r.set(obj, family.PartInput)
	case "message_delta":
		var upd map[string]json.RawMessage
		if isAbsent(e.Usage) || json.Unmarshal(e.Usage, &upd) != nil || upd == nil {
			return
		}
		merged := make(map[string]json.RawMessage, len(r.cur)+len(upd))
		for k, v := range r.cur {
			merged[k] = v
		}
		for k, v := range upd {
			merged[k] = v
		}
		parts := r.parts
		if !isAbsent(upd["output_tokens"]) {
			parts |= family.PartOutput
		}
		r.set(merged, parts)
	}
}

// set makes obj the current usage object if it can be used, and parts the parts reported.
func (r *StreamReader) set(obj map[string]json.RawMessage, parts family.Parts) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return
	}
	u, err := normalizeUsage(raw)
	if err != nil {
		return
	}
	r.cur, r.usage, r.parts = obj, u, parts
}

// Usage returns the usage read so far and the parts the provider reported: none before
// message_start, the input part after it, both once a message_delta reported output_tokens.
func (r *StreamReader) Usage() (family.Usage, family.Parts) { return r.usage, r.parts }

// Failed reports that the reader gave up because an event exceeded the size bound.
func (r *StreamReader) Failed() bool { return r.Framing.Failed() }

// Deltas counts the content_block_delta events seen so far.
func (r *StreamReader) Deltas() int64 { return r.deltas }
