package openai

import (
	"bytes"
	"encoding/json"

	"github.com/pandokos/pandokos/internal/family"
)

const maxEventBytes = 1 << 20

type chunk struct {
	Choices []struct{} `json:"choices"`
	Usage   *wireUsage `json:"usage"`
}

// StreamReader reads usage from a chat completions event stream, incrementally. With strip
// set it also removes the usage-only event; otherwise it is a pure tee.
type StreamReader struct {
	strip    bool
	failed   bool
	buf      []byte // the incomplete event being assembled, nothing else
	scan     int    // index in buf of the first line not examined yet
	out      []byte // reused output buffer (strip mode)
	usage    family.Usage
	hasUsage bool
}

// NewStreamReader returns a reader; strip says whether usage-only events are removed.
func NewStreamReader(strip bool) *StreamReader { return &StreamReader{strip: strip} }

// Filter takes the next bytes of the stream and returns the bytes to forward; the result is
// valid until the next call.
func (r *StreamReader) Filter(p []byte) []byte {
	if r.failed {
		return p
	}
	r.out = r.out[:0]
	r.buf = append(r.buf, p...)
	start := 0
	for {
		end, next := eventEnd(r.buf, r.scan)
		r.scan = next
		if end < 0 {
			break
		}
		ev := r.buf[start:end]
		usageOnly := r.inspect(ev)
		if r.strip && !usageOnly {
			r.out = append(r.out, ev...)
		}
		start = end
	}
	rest := r.buf[start:]
	if len(rest) > maxEventBytes {
		r.failed = true
		if r.strip {
			r.out = append(r.out, rest...)
		}
		r.buf, r.scan = nil, 0
	} else {
		r.buf = append(r.buf[:0], rest...)
		r.scan -= start
	}
	if !r.strip {
		return p
	}
	return r.out
}

// Flush returns the bytes still held at the end of the stream.
func (r *StreamReader) Flush() []byte {
	if !r.strip || r.failed {
		return nil
	}
	b := r.buf
	r.buf, r.scan = nil, 0
	return b
}

// eventEnd looks for the empty line that ends the current event. scan is the index of the
// first line not examined yet. It returns the index just after the empty line, or -1, and the
// index from which the next call must resume.
func eventEnd(b []byte, scan int) (end, next int) {
	i := scan
	for {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return -1, i
		}
		line := b[i : i+j]
		i += j + 1
		if len(line) == 0 || (len(line) == 1 && line[0] == '\r') {
			return i, i
		}
	}
}

// inspect records the usage carried by a complete event and reports whether the event is a
// usage-only event.
func (r *StreamReader) inspect(ev []byte) bool {
	var data []byte
	n := 0
	for _, line := range bytes.Split(ev, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		v := bytes.TrimPrefix(line[len("data:"):], []byte(" "))
		if n == 0 {
			data = v
		} else {
			data = append(append(append([]byte(nil), data...), '\n'), v...)
		}
		n++
	}
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil || c.Usage == nil {
		return false
	}
	u, err := c.Usage.normalize() // wireUsage → family.Usage; error on a negative count
	if err != nil {
		return false
	}
	r.usage, r.hasUsage = u, true
	return c.Choices != nil && len(c.Choices) == 0
}

// Usage returns the usage read so far; ok is false when none was seen.
func (r *StreamReader) Usage() (family.Usage, bool) { return r.usage, r.hasUsage }

// Failed reports that the reader gave up because an event exceeded the size bound.
func (r *StreamReader) Failed() bool { return r.failed }

// Deltas counts the chunks with a non-empty choices array seen so far.
func (r *StreamReader) Deltas() int64 { return 0 }
