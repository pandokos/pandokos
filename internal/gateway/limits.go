package gateway

import (
	"fmt"
	"strconv"
	"strings"
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
	res, ref, state, err := h.limits.Reserve(st.callerCx, *st.principal, e)
	if state != "" {
		st.limitState = state
	}
	switch {
	case err != nil:
		if st.callerCx.Err() == nil {
			st.sw.Header().Set("Retry-After", unavailableRetryAfter)
		}
		h.unavailable(st)
		return false
	case ref != nil:
		st.refusal = ref
		code := family.ErrorCode(ref.Code)
		if code == "" {
			code = family.CodeRateLimited
		}
		if code != family.CodeNoPrice {
			hd := st.sw.Header()
			hd.Set("Retry-After", strconv.FormatInt(seconds(ref.RetryAfter), 10))
			hd.Set(family.HeaderLimitSubject, ref.Subject)
			hd.Set(family.HeaderLimitMetric, ref.Metric)
			if ref.Currency != "" {
				hd.Set(family.HeaderLimit, millionths(ref.Limit))
				hd.Set(family.HeaderLimitRemaining, millionths(ref.Remaining))
				hd.Set(family.HeaderLimitCurrency, ref.Currency)
			} else {
				hd.Set(family.HeaderLimit, strconv.FormatInt(ref.Limit, 10))
				hd.Set(family.HeaderLimitRemaining, strconv.FormatInt(ref.Remaining, 10))
			}
			if ref.Scope != "" {
				hd.Set(family.HeaderLimitScope, ref.Scope)
			}
			if s := seconds(ref.Reset); s > 0 {
				hd.Set(family.HeaderLimitReset, strconv.FormatInt(s, 10))
			}
		}
		h.fail(st, code)
		return false
	}
	st.reservation, st.reservedTokens = res, e.InputTokens+e.OutputTokens
	return true
}

// millionths formats an amount of millionths of a currency as a decimal with at most six fraction
// digits and no trailing zeros: 250500000 is "250.5", 0 is "0".
func millionths(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v/1_000_000, 10)
	if f := v % 1_000_000; f != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%06d", f), "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}

// settleLimit ends the reservation, if one is held: settle with the figures of the usage record
// when the request counts (its credential was opened), release otherwise. It never panics.
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
	if !st.counted {
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

// limitAttrs are the log fields of the limits: whether they were evaluated and whether the counters
// could be verified. No rule, subject, metric or scope of the tenant (ADR 0055).
func (st *reqState) limitAttrs() []any {
	attrs := []any{"limit_state", string(st.limitState)}
	if m, ok := st.reservation.(interface{ Unverified() bool }); ok && m.Unverified() {
		attrs = append(attrs, "limits_unverified", true)
	}
	return attrs
}
