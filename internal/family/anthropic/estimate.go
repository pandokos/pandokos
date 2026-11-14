package anthropic

import "github.com/pandokos/pandokos/internal/family"

// Estimate estimates the usage of a request whose provider reported none, as the openai family
// does for chat: the input is the request bytes over four, rounded up; the output is one token per
// content event for a stream and the response bytes over four, rounded up, otherwise. Token
// counting consumes nothing and estimates nothing.
func Estimate(operation string, e family.Evidence) family.Usage {
	if operation != OperationMessages {
		return family.Usage{}
	}
	u := family.Usage{InputTokens: ceil4(e.RequestBytes)}
	if e.Stream {
		u.OutputTokens = max(e.Deltas, 0)
	} else {
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
