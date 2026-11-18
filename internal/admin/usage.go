package admin

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/pandokos/pandokos/internal/adminapi"
	"github.com/pandokos/pandokos/internal/catalog"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
)

const (
	defaultUsageWindow = 24 * time.Hour
	maxUsageWindow     = 31 * 24 * time.Hour
)

// ListUsageRecords lists the usage records of a tenant, newest first, in a window of at most 31
// days. The query runs in the tenant's session, so the row policy returns only its rows.
func (s *server) ListUsageRecords(ctx context.Context, in adminapi.ListUsageRecordsInput) (*adminapi.UsageRecordList, error) {
	tid, err := tenantID(in.Tenant)
	if err != nil {
		return nil, err
	}
	const layout = time.RFC3339Nano
	const sortKey = "usage"
	var cur cursor
	if in.Cursor != "" {
		if cur, err = decodeFullCursor(in.Cursor, sortKey); err != nil {
			return nil, err
		}
	}
	// A cursor carries the window and the provider it was made for (the default window moves with
	// the clock): they are taken from it, and an explicit value must be the same instant.
	to := time.Now().UTC()
	var toErr, fromErr, provErr *adminapi.FieldError
	if in.To != "" {
		t, perr := time.Parse(time.RFC3339Nano, in.To)
		if perr != nil {
			toErr = &adminapi.FieldError{Field: "to", Message: "must be an RFC 3339 instant with a zone"}
		} else {
			to = t.UTC()
		}
	}
	from := to.Add(-defaultUsageWindow)
	if in.From != "" {
		t, perr := time.Parse(time.RFC3339Nano, in.From)
		if perr != nil {
			fromErr = &adminapi.FieldError{Field: "from", Message: "must be an RFC 3339 instant with a zone"}
		} else {
			from = t.UTC()
		}
	}
	if in.Provider != "" && catalog.ValidateProviderName(in.Provider) != nil {
		provErr = &adminapi.FieldError{Field: "provider", Message: "is not a provider name"}
	}
	if err := fieldsError(fromErr, toErr, provErr); err != nil {
		return nil, err
	}
	provider, limitState := in.Provider, string(in.LimitState)
	if in.Cursor != "" {
		cf, ferr := time.Parse(layout, cur.F)
		ct, terr := time.Parse(layout, cur.T)
		if ferr != nil || terr != nil ||
			(in.From != "" && !from.Equal(cf)) || (in.To != "" && !to.Equal(ct)) || (in.Provider != "" && in.Provider != cur.P) ||
			(in.LimitState != "" && string(in.LimitState) != cur.L) {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
		from, to, provider, limitState = cf.UTC(), ct.UTC(), cur.P, cur.L
	}
	switch {
	case !from.Before(to):
		fromErr = &adminapi.FieldError{Field: "from", Message: "must be before to"}
	case to.Sub(from) > maxUsageWindow:
		fromErr = &adminapi.FieldError{Field: "from", Message: "the window is at most 31 days"}
	}
	if in.Cursor != "" && fromErr != nil {
		return nil, invalid("cursor", "is not a cursor of this collection and sort")
	}
	if err := fieldsError(fromErr); err != nil {
		return nil, err
	}

	after := in.Cursor != ""
	afterAt := time.Time{}
	if after {
		if afterAt, err = time.Parse(layout, cur.N); err != nil {
			return nil, invalid("cursor", "is not a cursor of this collection and sort")
		}
	}
	limit, ask := limitOf(in.Limit)
	par := queries.ListUsageRecordsParams{
		FromAt: from, ToAt: to, Provider: provider, LimitState: limitState, After: after, AfterStartedAt: afterAt, AfterID: afterID(after, cur.I), PageSize: ask,
	}
	var rows []queries.ListUsageRecordsRow
	err = s.db.WithTenant(ctx, tid, func(ctx context.Context, tx store.Tx) error {
		var err error
		rows, err = queries.New(tx).ListUsageRecords(ctx, par)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := &adminapi.UsageRecordList{Items: []adminapi.UsageRecord{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			out.NextCursor = encodeFullCursor(cursor{S: sortKey, N: last.StartedAt.UTC().Format(layout), I: last.ID,
				F: from.Format(layout), T: to.Format(layout), P: provider, L: limitState})
			break
		}
		out.Items = append(out.Items, usageRecordOut(r))
	}
	return out, nil
}

// usageRecordOut maps a ledger row to the representation of the API: absent values are zero, a
// cost is null unless known, and the JSON members are arrays sorted by name.
func usageRecordOut(r queries.ListUsageRecordsRow) adminapi.UsageRecord {
	i64 := func(p *int64) int {
		if p == nil {
			return 0
		}
		return int(*p)
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	provIn, provOut := r.InputSource == "provider", r.OutputSource == "provider"
	estIn, estOut := r.InputSource == "estimated", r.OutputSource == "estimated"
	side := func(ok bool, p *int64) int {
		if !ok {
			return 0
		}
		return i64(p)
	}
	o := adminapi.UsageRecord{
		ID: r.ID, RequestID: r.RequestID, TraceID: r.TraceID, StartedAt: r.StartedAt.UTC(), DurationMs: int(r.DurationMs),
		PrincipalKind: r.PrincipalKind, PrincipalID: r.PrincipalID, KeyID: r.KeyID, CredentialID: str(r.CredentialID),
		Provider: r.Provider, ApiFamily: r.ApiFamily, Operation: r.Operation, Endpoint: r.Endpoint, Model: r.Model, ModelID: str(r.ModelID),
		UpstreamModel: r.UpstreamModel, Stream: r.Stream != nil && *r.Stream, Outcome: r.Outcome, ErrorCode: r.ErrorCode,
		TtfbMs: i64(r.TtfbMs), RequestBytes: i64(r.RequestBytes), ResponseBytes: i64(r.ResponseBytes),
		InputSource: r.InputSource, OutputSource: r.OutputSource,
		InputTokens: side(provIn, r.InputTokens), OutputTokens: side(provOut, r.OutputTokens),
		CacheReadTokens: side(provIn, r.CacheReadTokens), CacheWriteTokens: side(provIn, r.CacheWriteTokens),
		ReasoningTokens:      side(provOut, r.ReasoningTokens),
		EstimatedInputTokens: side(estIn, r.EstimatedInputTokens), EstimatedOutputTokens: side(estOut, r.EstimatedOutputTokens),
		OtherUnits: counters(r.OtherUnits), ProviderUsage: counters(r.ProviderUsage),
		CostStatus: r.CostStatus, CostReason: r.CostReason, Cost: adminapi.Null[string](), Currency: str(r.Currency),
		PriceVersionID: str(r.PriceVersionID), CostFallbacks: append([]string{}, r.CostFallbacks...),
		Metadata: metadataEntries(r.Metadata), MetadataDropped: int(r.MetadataDropped), LimitState: r.LimitState,
	}
	if r.UpstreamStatus != nil {
		o.UpstreamStatus = int(*r.UpstreamStatus)
	}
	if r.CostStatus == "known" && r.Cost != nil {
		o.Cost = adminapi.Value(*r.Cost)
	}
	return o
}

// counters decodes a JSON object of integers into an array sorted by name; malformed text (which
// the table's checks do not allow) reads as empty.
func counters(text string) []adminapi.UsageCounter {
	m := map[string]int64{}
	_ = json.Unmarshal([]byte(text), &m)
	out := make([]adminapi.UsageCounter, 0, len(m))
	for k, v := range m {
		out = append(out, adminapi.UsageCounter{Name: k, Value: int(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// metadataEntries decodes a JSON object of strings into an array sorted by key.
func metadataEntries(text string) []adminapi.MetadataEntry {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(text), &m)
	out := make([]adminapi.MetadataEntry, 0, len(m))
	for k, v := range m {
		out = append(out, adminapi.MetadataEntry{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
