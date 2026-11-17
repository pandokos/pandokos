package gateway

import (
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/usage"
)

// maxEstimateTokens is the most one request reserves: the sum of input and output saturates here,
// which is also the largest limit a rule can hold.
const maxEstimateTokens = 1_000_000_000_000

// estimateOf computes the request-side estimate of sprint 5.1 section 5.3: the input from the
// size of the body through the family's estimator, capped by the model's context window; the
// output from the caller's limit per choice (else defaultOutput, capped by the model's maximum)
// times the number of choices; the cost at the price version in force at start. The arithmetic
// saturates: the sum of input and output never exceeds 1e12. The RequestID of the result is
// left for the caller.
func estimateOf(a family.Adapter, op family.Operation, bound family.OutputBound, bodyLen int64, t Target, defaultOutput int64, start time.Time) LimitEstimate {
	input := a.EstimateUsage(op, family.Evidence{RequestBytes: bodyLen}).InputTokens
	if t.ContextWindow > 0 {
		input = min(input, t.ContextWindow)
	}
	input = min(max(input, 0), maxEstimateTokens)

	var output int64
	if !bound.None {
		per := defaultOutput
		if bound.Tokens > 0 {
			per = bound.Tokens
		}
		if t.MaxOutputTokens > 0 {
			per = min(per, t.MaxOutputTokens)
		}
		per = max(per, 0)
		choices := max(bound.Choices, 1)
		if per > maxEstimateTokens/choices {
			output = maxEstimateTokens
		} else {
			output = per * choices
		}
		output = min(output, maxEstimateTokens-input)
	}

	v, found := pricing.VersionAt(t.Prices, start)
	cost := pricing.CostOf(family.Usage{InputTokens: input, OutputTokens: output}, v, found)
	if cost.Known && !usage.CostFits(cost.Amount) {
		cost = pricing.Cost{Reason: pricing.ReasonInconsistentUsage, Currency: v.Currency, VersionID: v.ID}
	}
	return LimitEstimate{InputTokens: input, OutputTokens: output, Cost: cost}
}
