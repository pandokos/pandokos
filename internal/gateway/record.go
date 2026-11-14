package gateway

import (
	"fmt"
	"net/http"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/usage"
)

// outcomeOf applies the table of sprint.md section 5.5, first match wins. logOutcome is the
// outcome of the request log line.
func outcomeOf(st *reqState, logOutcome string) usage.Outcome {
	switch st.errCode {
	case "":
	case family.CodeTenantSuspended, family.CodeForbidden, family.CodeModelNotAllowed, family.CodeEndpointNotSupported,
		family.CodeNoCredential, family.CodeInvalidRequest, family.CodeRequestTooLarge:
		return usage.OutcomeRejectedPolicy
	case family.CodeRequestTimeout, family.CodeUpstreamTimeout:
		return usage.OutcomeTimeout
	case family.CodeUpstreamUnreachable:
		return usage.OutcomeUpstreamError
	default: // unavailable, internal
		return usage.OutcomeInternalError
	}
	switch logOutcome {
	case "client_error", "client_cancelled", "shutdown_aborted":
		return usage.OutcomeCancelled
	case "timeout":
		return usage.OutcomeTimeout
	case "upstream_aborted":
		return usage.OutcomeUpstreamError
	case "pandokos_error":
		return usage.OutcomeInternalError
	}
	if st.upstreamStatus >= 400 {
		return usage.OutcomeUpstreamError
	}
	return usage.OutcomeOK
}

// providerUsage reads the usage of the answer through the family's readers, each call guarded
// against a panic. gaveUp says that a reader could not make sense of the answer.
func (h *Handler) providerUsage(st *reqState) (u family.Usage, ok, gaveUp bool) {
	switch {
	case st.streamBody != nil:
		r := st.streamBody.reader
		h.soft(st, func() { st.deltas = r.Deltas() })
		h.soft(st, func() { u, ok = r.Usage() })
		h.soft(st, func() { gaveUp = r.Failed() })
		// A reader that gave up cannot vouch for what it saw afterwards.
		gaveUp = gaveUp || st.readerAbandoned
		if !ok || gaveUp {
			u, ok = family.Usage{}, false
		}
		return u, ok, gaveUp
	case st.teeBody != nil:
		h.soft(st, func() { u, ok, gaveUp = st.teeBody.usage() })
		if !ok {
			u = family.Usage{}
		}
		return u, ok, gaveUp || st.readerAbandoned
	}
	return family.Usage{}, false, false
}

// recordOf builds the usage record of an admitted request: what was measured, never content.
func (h *Handler) recordOf(st *reqState, logOutcome string) usage.Record {
	p := st.principal
	r := usage.Record{
		ID:            st.recordID,
		TenantID:      p.TenantID,
		StartedAt:     st.start.UTC(),
		RequestID:     st.id,
		TraceID:       st.traceID,
		PrincipalKind: "service_account",
		PrincipalID:   p.ID,
		KeyID:         p.KeyID,
		Funding:       "byok",
		Provider:      st.provider,
		APIFamily:     st.family,
		Operation:     st.op.Name,
		Outcome:       outcomeOf(st, logOutcome),
		ErrorCode:     string(st.errCode),
		InputSource:   usage.SourceNone,
		OutputSource:  usage.SourceNone,
		CostStatus:    "none",
		Metadata:      st.metadata,

		MetadataDropped: st.metadataDropped,
	}
	if st.opened {
		r.CredentialID = st.credentialID
	}
	if st.resolved {
		r.Endpoint, r.Model, r.ModelID, r.UpstreamModel = st.target.Endpoint, st.target.Model, st.target.ModelID, st.target.UpstreamModel
	}
	if st.streamKnown {
		stream := st.stream
		r.Stream = &stream
	}
	if st.bodyRead {
		n := int64(st.requestBytes)
		r.RequestBytes = &n
	}
	r.UpstreamStatus = st.upstreamStatus
	if d, ok := st.ttfb(); ok {
		r.TTFB = &d
	}
	if st.answered {
		n := st.responseBytes()
		r.ResponseBytes = &n
	}

	// Usage and its sources (section 5.4): only a 2xx answer carries any.
	if st.answered && st.upstreamStatus >= 200 && st.upstreamStatus < 300 {
		u, ok, gaveUp := h.providerUsage(st)
		st.readerFailed = st.readerFailed || gaveUp
		if ok {
			r.InputSource, r.OutputSource, r.Usage = usage.SourceProvider, usage.SourceProvider, u
			st.costUsage = u
		} else {
			evidence := family.Evidence{
				RequestBytes: int64(st.requestBytes), ResponseBytes: st.responseBytes(),
				Deltas: st.deltas, Stream: st.streamBody != nil,
			}
			e := st.adapter.EstimateUsage(st.op, evidence)
			e = family.Usage{InputTokens: e.InputTokens, OutputTokens: e.OutputTokens}
			r.InputSource, r.OutputSource, r.Estimated = usage.SourceEstimated, usage.SourceEstimated, e
			st.costUsage = e
		}
	}

	// Cost: the price in force when the request started.
	if r.InputSource != usage.SourceNone {
		v, found := pricing.VersionAt(st.target.Prices, st.start)
		r.Cost = pricing.CostOf(st.costUsage, v, found)
		r.CostStatus = "unknown"
		if r.Cost.Known {
			r.CostStatus = "known"
		}
	}
	r.Duration = time.Since(st.start)
	return r
}

// minimalRecord is what is recorded when building the record failed: attribution and an
// internal error, nothing measured.
func (st *reqState) minimalRecord() usage.Record {
	p := st.principal
	id := st.recordID
	if id == "" {
		id = usage.NewID(st.start)
	}
	return usage.Record{
		ID: id, TenantID: p.TenantID, StartedAt: st.start.UTC(), RequestID: st.id, TraceID: st.traceID,
		PrincipalKind: "service_account", PrincipalID: p.ID, KeyID: p.KeyID, Funding: "byok",
		Provider: st.provider, APIFamily: st.family, Operation: st.op.Name,
		Outcome: usage.OutcomeInternalError, ErrorCode: string(family.CodeInternal),
		InputSource: usage.SourceNone, OutputSource: usage.SourceNone, CostStatus: "none",
		Duration: time.Since(st.start),
	}
}

// safeRecordOf builds the record and falls back to the minimal one when that panics.
func (h *Handler) safeRecordOf(st *reqState, logOutcome string) (r usage.Record) {
	defer func() {
		if v := recover(); v != nil {
			h.logger.Error("usage record failed", "request_id", st.id, "panic_type", fmt.Sprintf("%T", v))
			r = st.minimalRecord()
		}
	}()
	return h.recordOf(st, logOutcome)
}

// finishRecord runs when an admitted request ends: it builds the record, ends the client span,
// hands the record over and ends the server span, in that order.
func (h *Handler) finishRecord(st *reqState, logOutcome string) {
	r := h.safeRecordOf(st, logOutcome)
	st.rec = &r
	st.endClient(&r)
	h.recorder.Record(r)
	st.endServer(&r)
}

// metadataHeader sets the count of dropped metadata entries on the response.
func metadataHeader(w http.Header, dropped int) {
	if dropped > 0 {
		w.Set(family.HeaderMetadataDropped, fmt.Sprint(dropped))
	}
}
