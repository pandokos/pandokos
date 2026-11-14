package openai

import (
	"bytes"
	"encoding/json"

	"github.com/pandokos/pandokos/internal/family"
)

const maxEventBytes = 1 << 20

// framing assembles the events of an event stream from chunks cut anywhere. It holds the one
// incomplete event, nothing else, and gives up when that event passes max bytes.
type framing struct {
	max    int
	failed bool
	buf    []byte // the incomplete event being assembled
	scan   int    // index in buf of the first line not examined yet
}

// split appends p and calls each for every complete event (valid during the call only). It
// returns the bytes held when the bound was passed, which the caller must forward: after that the
// framing holds nothing and has failed.
func (f *framing) split(p []byte, each func(ev []byte)) (overflow []byte) {
	f.buf = append(f.buf, p...)
	start := 0
	for {
		end, next := eventEnd(f.buf, f.scan)
		f.scan = next
		if end < 0 {
			break
		}
		each(f.buf[start:end])
		start = end
	}
	if rest := f.buf[start:]; len(rest) > f.max {
		f.failed = true
		overflow = append([]byte(nil), rest...)
		f.buf, f.scan = nil, 0
		return overflow
	}
	if start > 0 {
		n := copy(f.buf, f.buf[start:])
		f.buf = f.buf[:n]
		f.scan -= start
	}
	return nil
}

// take returns the bytes held and forgets them.
func (f *framing) take() []byte {
	b := f.buf
	f.buf, f.scan = nil, 0
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

// eventData returns the data of an event: its data: lines without the field name and one leading
// space, joined with newlines. It returns the event's own bytes when there is a single line.
func eventData(ev []byte) []byte {
	var data []byte
	n := 0
	for len(ev) > 0 {
		var line []byte
		if i := bytes.IndexByte(ev, '\n'); i >= 0 {
			line, ev = ev[:i], ev[i+1:]
		} else {
			line, ev = ev, nil
		}
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
	return data
}

type chunk struct {
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
}

// StreamReader reads usage from a chat completions event stream, incrementally. With strip
// set it also removes the usage-only event; otherwise it is a pure tee.
type StreamReader struct {
	framing
	strip    bool
	out      []byte // reused output buffer (strip mode)
	usage    family.Usage
	hasUsage bool
	deltas   int64
}

// NewStreamReader returns a reader; strip says whether usage-only events are removed.
func NewStreamReader(strip bool) *StreamReader {
	return &StreamReader{framing: framing{max: maxEventBytes}, strip: strip}
}

// Filter takes the next bytes of the stream and returns the bytes to forward; the result is
// valid until the next call.
func (r *StreamReader) Filter(p []byte) []byte {
	if r.failed {
		return p
	}
	r.out = r.out[:0]
	overflow := r.split(p, func(ev []byte) {
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
// middle of is still read for its usage, and forwarded.
func (r *StreamReader) Flush() []byte {
	if r.failed {
		return nil
	}
	b := r.take()
	if len(b) > 0 {
		r.inspect(b)
	}
	if !r.strip {
		return nil
	}
	return b
}

// inspect records the usage carried by an event and counts it among the content events, and
// reports whether the event is a usage-only event.
func (r *StreamReader) inspect(ev []byte) bool {
	data := eventData(ev)
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

// Usage returns the usage read so far; ok is false when none was seen.
func (r *StreamReader) Usage() (family.Usage, bool) { return r.usage, r.hasUsage }

// Failed reports that the reader gave up because an event exceeded the size bound.
func (r *StreamReader) Failed() bool { return r.failed }

// Deltas counts the chunks with a non-empty choices array seen so far.
func (r *StreamReader) Deltas() int64 { return r.deltas }
