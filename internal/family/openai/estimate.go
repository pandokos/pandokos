package openai

import "github.com/pandokos/pandokos/internal/family"

// Estimate estimates the usage of a request whose provider reported none, from what the
// gateway measured (sprint 3.2 section 5.4): the input is the request bytes over four, rounded up;
// the output is one token per content event for a stream and the response bytes over four,
// rounded up, otherwise; embeddings have no output. There is no tokenizer.
func Estimate(operation string, e family.Evidence) family.Usage {
	u := family.Usage{InputTokens: ceil4(e.RequestBytes)}
	switch {
	case operation == OperationEmbeddings:
	case e.Stream:
		u.OutputTokens = max(e.Deltas, 0)
	default:
		u.OutputTokens = ceil4(e.ResponseBytes)
	}
	return u
}

func ceil4(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return n/4 + min(n%4, 1)
}
