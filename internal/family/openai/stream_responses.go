package openai

import "github.com/pandokos/pandokos/internal/family"

// NewStreamReaderFor returns the stream reader of an operation of the family; strip says
// whether usage-only events are removed (chat completions only).
func NewStreamReaderFor(operation string, strip bool) family.StreamReader {
	_ = operation
	return NewStreamReader(strip)
}
