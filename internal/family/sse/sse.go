// Package sse frames server-sent events. It is a leaf: it imports the standard library only, so
// every API family can use it.
package sse

import "bytes"

// Framing assembles the events of an event stream from chunks cut anywhere. It holds the one
// incomplete event, nothing else, and gives up when that event passes max bytes.
type Framing struct {
	// Max is the bound, in bytes, on the one incomplete event held.
	Max    int
	failed bool   // read through Failed
	buf    []byte // the incomplete event being assembled
	scan   int    // index in buf of the first line not examined yet
}

// Split appends p and calls each for every complete event (valid during the call only). It
// returns the bytes held when the bound was passed, which the caller must forward: after that the
// framing holds nothing and has failed.
func (f *Framing) Split(p []byte, each func(ev []byte)) (overflow []byte) {
	f.buf = append(f.buf, p...)
	start := 0
	for {
		end, next := eventEnd(f.buf, f.scan)
		f.scan = next
		if end < 0 {
			break
		}
		if end-start > f.Max { // a complete event over the bound: give up like for an incomplete one
			f.failed = true
			overflow = append([]byte(nil), f.buf[start:]...)
			f.buf, f.scan = nil, 0
			return overflow
		}
		each(f.buf[start:end])
		start = end
	}
	if rest := f.buf[start:]; len(rest) > f.Max {
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

// Take returns the bytes held and forgets them.
func (f *Framing) Take() []byte {
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

// Data returns the data of an event: its data: lines without the field name and one leading
// space, joined with newlines. It returns the event's own bytes when there is a single line.
func Data(ev []byte) []byte {
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

// Failed reports that the framing gave up because an event passed Max.
func (f *Framing) Failed() bool { return f.failed }
