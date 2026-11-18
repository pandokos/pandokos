package gateway

import (
	"fmt"
	"strconv"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/usage"
)

// seconds rounds a duration up to whole seconds.
func seconds(d time.Duration) int64 {
	return int64((d + time.Second - 1) / time.Second)
}

// reserve runs step 7b: the estimate is reserved against the limits that apply to the principal
// before the credential is opened. It answers and returns false when the request is refused.
func (h *Handler) reserve(st *reqState, bound family.OutputBound) bool {
	if h.limits == nil {
		return true
	}
	e := estimateOf(st.adapter, st.op, bound, int64(st.requestBytes), st.target, h.defaultOut, st.start)
	e.RequestID = st.id
	res, ref, err := h.limits.Reserve(st.callerCx, *st.principal, e)
	switch {
	case err != nil:
		if st.callerCx.Err() == nil {
			st.sw.Header().Set("Retry-After", unavailableRetryAfter)
		}
		h.unavailable(st)
		return false
	case ref != nil:
		st.refusal = ref
		hd := st.sw.Header()
		hd.Set("Retry-After", strconv.FormatInt(seconds(ref.RetryAfter), 10))
		hd.Set(family.HeaderLimitSubject, ref.Subject)
		hd.Set(family.HeaderLimitMetric, ref.Metric)
		hd.Set(family.HeaderLimit, strconv.FormatInt(ref.Limit, 10))
		hd.Set(family.HeaderLimitRemaining, strconv.FormatInt(ref.Remaining, 10))
		if s := seconds(ref.Reset); s > 0 {
			hd.Set(family.HeaderLimitReset, strconv.FormatInt(s, 10))
		}
		h.fail(st, family.CodeRateLimited)
		return false
	}
	st.reservation, st.reservedTokens = res, e.InputTokens+e.OutputTokens
	return true
}

// settleLimit ends the reservation, if one is held: settle with the figures of the usage record
// when the request was forwarded, release otherwise. It never panics.
func (h *Handler) settleLimit(st *reqState) {
	if st.reservation == nil {
		return
	}
	res := st.reservation
	defer func() {
		if v := recover(); v != nil {
			h.logger.Error("limit settle failed", "request_id", st.id, "panic_type", fmt.Sprintf("%T", v))
		}
	}()
	if !st.forwarded {
		res.Release()
		return
	}
	var u LimitUsage
	if st.rec != nil {
		if st.rec.InputSource != usage.SourceNone {
			u.Tokens = st.costUsage.InputTokens + st.costUsage.OutputTokens
		}
		u.Cost = st.rec.Cost
	}
	res.Settle(u)
}

// limitAttrs are the log fields of sprint.md section 5.9.
func (st *reqState) limitAttrs() []any {
	var attrs []any
	if st.reservation != nil || st.reservedTokens > 0 {
		attrs = append(attrs, "reserved_tokens", st.reservedTokens)
	}
	if r := st.refusal; r != nil {
		attrs = append(attrs, "limit_subject", r.Subject, "limit_metric", r.Metric, "limit_level", r.Level,
			"limit", r.Limit, "retry_after_s", seconds(r.RetryAfter))
		if r.OverLimit {
			attrs = append(attrs, "estimate_over_limit", true)
		}
	}
	return attrs
}
