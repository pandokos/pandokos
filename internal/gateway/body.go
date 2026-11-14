package gateway

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pandokos/pandokos/internal/family"
)

const (
	streamBufferBytes = 32 << 10
	teeLimitBytes     = 4 << 20
)

// trailerGuard drops the trailers the HTTP/1 transport stores in the response when the body
// ends: trailers are provider headers outside the allow-list.
type trailerGuard struct{ resp *http.Response }

func (g trailerGuard) end(err error) {
	if err != nil && g.resp != nil {
		g.resp.Trailer = nil
	}
}

// noteFirstByte takes the time of the first body byte read from the provider.
func (st *reqState) noteFirstByte(n int) {
	if n > 0 && st.firstByte.IsZero() {
		st.firstByte = time.Now()
	}
}

// ttfb returns the time from step 1 to the first body byte, false when no byte arrived.
func (st *reqState) ttfb() (time.Duration, bool) {
	if st.firstByte.IsZero() {
		return 0, false
	}
	return st.firstByte.Sub(st.start), true
}

// soft runs a call into a family reader or adapter so that a panic cannot reach the stream or
// the record: the reader is abandoned, one error line is logged with the type of the panic and
// nothing else, and soft returns false, now and for every later call.
func (h *Handler) soft(st *reqState, call func()) (ok bool) {
	if st.readerAbandoned {
		return false
	}
	defer func() {
		if v := recover(); v != nil {
			st.readerAbandoned, st.readerFailed = true, true
			h.logger.Error("usage reader failed", "request_id", st.id, "panic_type", fmt.Sprintf("%T", v))
			ok = false
		}
	}()
	call()
	return true
}

// streamBody wraps a provider event stream and feeds every chunk to the stream reader. It never
// waits for more input while it holds output.
//
// In observe mode the caller gets the bytes the provider sent, read into the caller's own
// buffer; the reader sees them and whatever it does cannot change the stream. In strip mode the
// caller gets what the reader says to forward. When the reader panics it is abandoned: the
// current chunk and everything after it are forwarded unchanged.
type streamBody struct {
	h        *Handler
	st       *reqState
	src      io.ReadCloser
	trailers trailerGuard
	reader   family.StreamReader
	strip    bool
	tmp      []byte
	pending  []byte
	err      error
	bytes    int64
}

func newStreamBody(h *Handler, st *reqState, resp *http.Response, r family.StreamReader, strip bool) *streamBody {
	b := &streamBody{h: h, st: st, src: resp.Body, trailers: trailerGuard{resp}, reader: r, strip: strip}
	if strip {
		b.tmp = make([]byte, streamBufferBytes)
	}
	return b
}

func (b *streamBody) Read(p []byte) (int, error) {
	if !b.strip {
		return b.observe(p)
	}
	for len(b.pending) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		n, err := b.src.Read(b.tmp)
		if n > 0 {
			b.st.noteFirstByte(n)
			chunk := b.tmp[:n]
			var out []byte
			if b.h.soft(b.st, func() { out = b.reader.Filter(chunk) }) {
				b.pending = out
			} else {
				b.pending = chunk
			}
		}
		if err != nil {
			b.err = err
			b.trailers.end(err)
			var rest []byte
			if b.h.soft(b.st, func() { rest = b.reader.Flush() }) && len(rest) > 0 {
				b.pending = append(b.pending[:len(b.pending):len(b.pending)], rest...)
			}
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	b.bytes += int64(n)
	return n, nil
}

// observe forwards what the provider sent: one read into the caller's buffer, the same slice
// handed to the reader, nothing between the read and the return.
func (b *streamBody) observe(p []byte) (int, error) {
	n, err := b.src.Read(p)
	if n > 0 {
		b.st.noteFirstByte(n)
		b.bytes += int64(n)
		chunk := p[:n]
		b.h.soft(b.st, func() { b.reader.Filter(chunk) })
	}
	if err != nil {
		b.trailers.end(err)
		b.h.soft(b.st, func() { b.reader.Flush() })
	}
	return n, err
}

func (b *streamBody) Close() error { return b.src.Close() }

// teeBody copies a complete JSON response into a bounded buffer while forwarding it.
type teeBody struct {
	st         *reqState
	src        io.ReadCloser
	trailers   trailerGuard
	adapter    family.Adapter
	op         family.Operation
	buf        []byte
	overflowed bool
	bytes      int64
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	b.st.noteFirstByte(n)
	b.trailers.end(err)
	b.bytes += int64(n)
	if n > 0 && !b.overflowed {
		if len(b.buf)+n > teeLimitBytes {
			b.buf, b.overflowed = nil, true
		} else {
			b.buf = append(b.buf, p[:n]...)
		}
	}
	return n, err
}

func (b *teeBody) Close() error { return b.src.Close() }

// usage reads the usage from the collected body. ok is false when there is none or it is
// unusable; gaveUp says that the reader could not read the answer (too large, not valid).
func (b *teeBody) usage() (u family.Usage, ok, gaveUp bool) {
	if b.overflowed {
		return family.Usage{}, false, true
	}
	u, ok, err := b.adapter.ReadUsage(b.op, b.buf)
	if err != nil {
		return family.Usage{}, false, true
	}
	return u, ok, false
}

// countBody only counts the bytes forwarded.
type countBody struct {
	st       *reqState
	src      io.ReadCloser
	trailers trailerGuard
	bytes    int64
}

func (b *countBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	b.st.noteFirstByte(n)
	b.trailers.end(err)
	b.bytes += int64(n)
	return n, err
}

func (b *countBody) Close() error { return b.src.Close() }
