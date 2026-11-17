package gateway

import (
	"time"

	"github.com/pandokos/pandokos/internal/family"
)

// estimateOf computes the request-side estimate of sprint 5.1 section 5.3: the input from the
// size of the body through the family's estimator, capped by the model's context window; the
// output from the caller's limit per choice (else defaultOutput, capped by the model's maximum)
// times the number of choices; the cost at the price version in force at start. Not implemented
// yet.
func estimateOf(_ family.Adapter, _ family.Operation, _ family.OutputBound, _ int64, _ Target, _ int64, _ time.Time) LimitEstimate {
	return LimitEstimate{}
}
