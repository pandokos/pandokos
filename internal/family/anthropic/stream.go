package anthropic

import "github.com/pandokos/pandokos/internal/family"

// StreamReader reads usage from a Messages event stream. The stub is a pure tee that reads
// nothing.
type StreamReader struct{}

// NewStreamReader returns a reader.
func NewStreamReader() *StreamReader { return &StreamReader{} }

// Filter returns the bytes to forward: all of them.
func (r *StreamReader) Filter(p []byte) []byte { return p }

// Flush returns the bytes still held at the end of the stream.
func (r *StreamReader) Flush() []byte { return nil }

// Usage returns the usage read so far and the parts the provider reported.
func (r *StreamReader) Usage() (family.Usage, family.Parts) { return family.Usage{}, 0 }

// Failed reports that the reader gave up.
func (r *StreamReader) Failed() bool { return false }

// Deltas counts the content events seen so far.
func (r *StreamReader) Deltas() int64 { return 0 }
