package ledger

import (
	"github.com/pandokos/pandokos/internal/usage"
)

// row is one record as the insert statement reads it (jsonb_to_recordset): one field per column
// of the table that the gateway fills. The tenant is not here: the statement takes it from the
// session.
type row struct {
	ID                    string            `json:"id"`
	StartedAt             string            `json:"started_at"`
	RequestID             string            `json:"request_id"`
	TraceID               string            `json:"trace_id"`
	PrincipalKind         string            `json:"principal_kind"`
	PrincipalID           string            `json:"principal_id"`
	KeyID                 string            `json:"key_id"`
	CredentialID          *string           `json:"credential_id"`
	Funding               string            `json:"funding"`
	Provider              string            `json:"provider"`
	APIFamily             string            `json:"api_family"`
	Operation             string            `json:"operation"`
	Endpoint              string            `json:"endpoint"`
	Model                 string            `json:"model"`
	ModelID               *string           `json:"model_id"`
	UpstreamModel         string            `json:"upstream_model"`
	Stream                *bool             `json:"stream"`
	Outcome               string            `json:"outcome"`
	ErrorCode             string            `json:"error_code"`
	UpstreamStatus        *int              `json:"upstream_status"`
	TTFBMs                *int64            `json:"ttfb_ms"`
	DurationMs            int64             `json:"duration_ms"`
	RequestBytes          *int64            `json:"request_bytes"`
	ResponseBytes         *int64            `json:"response_bytes"`
	InputSource           string            `json:"input_source"`
	OutputSource          string            `json:"output_source"`
	InputTokens           *int64            `json:"input_tokens"`
	OutputTokens          *int64            `json:"output_tokens"`
	CacheReadTokens       *int64            `json:"cache_read_tokens"`
	CacheWriteTokens      *int64            `json:"cache_write_tokens"`
	ReasoningTokens       *int64            `json:"reasoning_tokens"`
	OtherUnits            map[string]int64  `json:"other_units"`
	ProviderUsage         map[string]int64  `json:"provider_usage"`
	EstimatedInputTokens  *int64            `json:"estimated_input_tokens"`
	EstimatedOutputTokens *int64            `json:"estimated_output_tokens"`
	CostStatus            string            `json:"cost_status"`
	CostReason            string            `json:"cost_reason"`
	Cost                  *string           `json:"cost"`
	Currency              *string           `json:"currency"`
	PriceVersionID        *string           `json:"price_version_id"`
	CostFallbacks         []string          `json:"cost_fallbacks"`
	Metadata              map[string]string `json:"metadata"`
	MetadataDropped       int               `json:"metadata_dropped"`
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func numOrNil(ok bool, n int64) *int64 {
	if !ok {
		return nil
	}
	return &n
}

// rowOf converts a record. Provider columns are filled only when the source is provider and
// estimate columns only when it is estimated; cache_* follow the input source and
// reasoning_tokens the output source (the table's constraints demand it).
func rowOf(r *usage.Record) row {
	provIn, provOut := r.InputSource == usage.SourceProvider, r.OutputSource == usage.SourceProvider
	estIn, estOut := r.InputSource == usage.SourceEstimated, r.OutputSource == usage.SourceEstimated
	x := row{
		ID:        r.ID,
		StartedAt: r.StartedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		RequestID: r.RequestID, TraceID: r.TraceID,
		PrincipalKind: r.PrincipalKind, PrincipalID: r.PrincipalID, KeyID: r.KeyID,
		CredentialID: strOrNil(r.CredentialID), Funding: r.Funding,
		Provider: r.Provider, APIFamily: r.APIFamily, Operation: r.Operation,
		Endpoint: r.Endpoint, Model: r.Model, ModelID: strOrNil(r.ModelID), UpstreamModel: r.UpstreamModel,
		Stream:  r.Stream,
		Outcome: string(r.Outcome), ErrorCode: r.ErrorCode,
		DurationMs:   r.Duration.Milliseconds(),
		RequestBytes: r.RequestBytes, ResponseBytes: r.ResponseBytes,
		InputSource: string(r.InputSource), OutputSource: string(r.OutputSource),
		InputTokens:      numOrNil(provIn, r.Usage.InputTokens),
		OutputTokens:     numOrNil(provOut, r.Usage.OutputTokens),
		CacheReadTokens:  numOrNil(provIn, r.Usage.CacheReadTokens),
		CacheWriteTokens: numOrNil(provIn, r.Usage.CacheWriteTokens),
		ReasoningTokens:  numOrNil(provOut, r.Usage.ReasoningTokens),
		OtherUnits:       map[string]int64{}, ProviderUsage: map[string]int64{},
		EstimatedInputTokens:  numOrNil(estIn, r.Estimated.InputTokens),
		EstimatedOutputTokens: numOrNil(estOut, r.Estimated.OutputTokens),
		CostStatus:            r.CostStatus,
		Currency:              strOrNil(r.Cost.Currency), PriceVersionID: strOrNil(r.Cost.VersionID),
		CostFallbacks:   []string{},
		Metadata:        map[string]string{},
		MetadataDropped: r.MetadataDropped,
	}
	if r.UpstreamStatus != 0 {
		s := r.UpstreamStatus
		x.UpstreamStatus = &s
	}
	if r.TTFB != nil {
		ms := r.TTFB.Milliseconds()
		x.TTFBMs = &ms
	}
	if provIn || provOut {
		for k, v := range r.Usage.Extra {
			x.OtherUnits[k] = v
		}
		for k, v := range r.Usage.Reported {
			x.ProviderUsage[k] = v
		}
	}
	if r.CostStatus == "unknown" {
		x.CostReason = r.Cost.Reason
	}
	if r.Cost.Known {
		s := r.Cost.Amount.String()
		x.Cost = &s
	}
	for _, c := range r.Cost.Fallbacks {
		x.CostFallbacks = append(x.CostFallbacks, string(c))
	}
	for k, v := range r.Metadata {
		x.Metadata[k] = v
	}
	return x
}
