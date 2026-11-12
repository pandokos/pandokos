package gateway

import (
	"io"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
)

const (
	streamBufferBytes = 32 << 10
	teeLimitBytes     = 4 << 20
)

// streamBody wraps a provider event stream: it feeds every chunk to the stream reader and
// returns what the reader says to forward. It never waits for more input while it holds output.
type streamBody struct {
	src     io.ReadCloser
	reader  *openai.StreamReader
	tmp     []byte
	pending []byte
	err     error
	bytes   int64
}

func newStreamBody(src io.ReadCloser, r *openai.StreamReader) *streamBody {
	return &streamBody{src: src, reader: r, tmp: make([]byte, streamBufferBytes)}
}

func (b *streamBody) Read(p []byte) (int, error) {
	for len(b.pending) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		n, err := b.src.Read(b.tmp)
		if n > 0 {
			b.pending = b.reader.Filter(b.tmp[:n])
		}
		if err != nil {
			b.err = err
			if rest := b.reader.Flush(); len(rest) > 0 {
				b.pending = append(b.pending[:len(b.pending):len(b.pending)], rest...)
			}
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	b.bytes += int64(n)
	return n, nil
}

func (b *streamBody) Close() error { return b.src.Close() }

// teeBody copies a complete JSON response into a bounded buffer while forwarding it.
type teeBody struct {
	src        io.ReadCloser
	buf        []byte
	overflowed bool
	bytes      int64
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
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

// usage reads the usage from the collected body; ok is false when there is none or it is unusable.
func (b *teeBody) usage() (family.Usage, bool) {
	if b.overflowed {
		return family.Usage{}, false
	}
	u, ok, err := openai.ReadUsage(b.buf)
	if err != nil || !ok {
		return family.Usage{}, false
	}
	return u, true
}

// countBody only counts the bytes forwarded.
type countBody struct {
	src   io.ReadCloser
	bytes int64
}

func (b *countBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	b.bytes += int64(n)
	return n, err
}

func (b *countBody) Close() error { return b.src.Close() }
