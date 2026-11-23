package telemetry

import (
	"errors"

	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/semconv/v1.41.0/httpconv"
)

// Bucket boundaries (section 5.4). The duration ones are the advice of the pinned GenAI
// conventions, used for the HTTP server duration too: an inference call often lasts longer than the
// 10 s the HTTP advice stops at.
var (
	durationBounds = []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
	tokenBounds    = []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}
	ratioBounds    = []float64{0.1, 0.25, 0.5, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 2, 4}
)

// The names of the pushed instruments.
const (
	nameHTTPDuration = "http.server.request.duration"
	nameRequests     = "pandokos.gateway.requests"
	nameOpDuration   = "gen_ai.client.operation.duration"
	nameFirstChunk   = "gen_ai.client.operation.time_to_first_chunk"
	nameTokenUsage   = "gen_ai.client.token.usage" //nolint:gosec // an instrument name, not a credential
	nameEstimated    = "pandokos.gen_ai.client.estimated_tokens"
	nameCost         = "pandokos.gen_ai.client.cost"
	nameCostUnknown  = "pandokos.gen_ai.client.cost_unknown"
	nameRateLimited  = "pandokos.provider.rate_limited"
	nameRefusals     = "pandokos.limits.refusals"
	nameEstimate     = "pandokos.limits.estimate_ratio"
)

// Attribute lists shared by several instruments.
var (
	genaiAttrs  = []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model"}
	genaiTokens = []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.token.type", "gen_ai.request.model"}
	resultAttr  = []string{"pandokos.result"}
)

func spec(name, kind, unit, desc string, attrs ...string) InstrumentSpec {
	s := InstrumentSpec{Name: name, Kind: kind, Unit: unit, Description: desc, Attributes: attrs}
	s.PrometheusName = promName(s)
	return s
}

// catalogue is the table of section 5.3, in order. The descriptions of the instruments of the
// pinned conventions are the pinned package's.
var catalogue = []InstrumentSpec{
	spec(nameHTTPDuration, "H", httpconv.ServerRequestDuration{}.Unit(), httpconv.ServerRequestDuration{}.Description(),
		"http.request.method", "url.scheme", "http.response.status_code", "http.route", "error.type"),
	spec(nameRequests, "C", "{request}", "Requests of the inference listener, by outcome, error code and limit state.",
		"pandokos.outcome", "pandokos.error_code", "pandokos.limit_state", "gen_ai.provider.name", "pandokos.api_family", "gen_ai.operation.name", "gen_ai.request.model"),
	spec(nameOpDuration, "H", genaiconv.ClientOperationDuration{}.Unit(), genaiconv.ClientOperationDuration{}.Description(),
		"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "error.type"),
	spec(nameFirstChunk, "H", genaiconv.ClientOperationTimeToFirstChunk{}.Unit(), genaiconv.ClientOperationTimeToFirstChunk{}.Description(), genaiAttrs...),
	spec(nameTokenUsage, "H", genaiconv.ClientTokenUsage{}.Unit(), genaiconv.ClientTokenUsage{}.Description(), genaiTokens...),
	spec(nameEstimated, "C", "{token}", "Tokens counted by Pandokos when the provider gave no usage, by token type.", genaiTokens...),
	spec(nameCost, "C", "{cost}", "Cost of the requests that have a known price, by currency.",
		"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "pandokos.currency"),
	spec(nameCostUnknown, "C", "{request}", "Requests with usage whose cost could not be computed, by reason.",
		"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "pandokos.cost.reason"),
	spec(nameRateLimited, "C", "{response}", "Responses of a provider that said 429, by provider and credential.",
		"gen_ai.provider.name", "pandokos.credential.id"),
	spec(nameRefusals, "C", "{request}", "Requests refused by a limit, by code, metric, level, subject kind and scope.",
		"pandokos.limit.code", "pandokos.limit.metric", "pandokos.limit.level", "pandokos.limit.subject", "pandokos.limit.scope"),
	spec(nameEstimate, "H", "1", "Settled over reserved quantity of a request, per quantity.", "pandokos.limit.quantity"),

	spec("pandokos.ledger.records", "OC", "{record}", "Usage records by result: written, rejected by the database, lost at shutdown.", resultAttr...),
	spec("pandokos.ledger.admission_refused", "OC", "{request}", "Requests the ledger refused to admit."),
	spec("pandokos.ledger.buffer.usage", "G", "{record}", "Slots of the ledger buffer by state: in use, queued.", "pandokos.ledger.state"),
	spec("pandokos.ledger.buffer.limit", "G", "{record}", "Size of the ledger buffer."),
	spec("pandokos.egress.blocked", "OC", "{connection}", "Connections and URLs the egress guard refused, by reason.", "pandokos.egress.reason"),
	spec("pandokos.authn.attempts", "OC", "{attempt}", "Authentication attempts, by listener, kind, result and failure reason.",
		"pandokos.listener", "pandokos.authn.kind", "pandokos.authn.result", "pandokos.authn.reason"),
	spec("pandokos.jwks.fetches", "OC", "{fetch}", "Fetches of token-issuer documents, by document and result.", "pandokos.jwks.document", "pandokos.result"),
	spec("pandokos.jwks.sources", "G", "{source}", "Token-issuer key sources by state.", "pandokos.jwks.state"),
	spec("pandokos.jwks.staleness", "G", "s", "Age of the oldest last success among the stale key sources; 0 when none is stale."),
	spec("pandokos.signin.attempts", "OC", "{attempt}", "Sign-in attempts, by method and result.", "pandokos.signin.method", "pandokos.result"),
	spec("pandokos.account.locks", "OC", "{lock}", "Accounts locked after failed sign-ins."),
	spec("pandokos.sessions.open", "G", "{session}", "Open sessions, as counted by the last purge."),
	spec("pandokos.password.hashes", "G", "{hash}", "Password hash computations by state: running, waiting for a slot.", "pandokos.password.state"),
	spec("pandokos.idempotency.replays", "OC", "{request}", "Requests answered from a stored idempotent response."),
	spec("pandokos.authz.grants_refused", "OC", "{request}", "Grants the authorization refused."),
	spec("pandokos.support_actions.opened", "OC", "{action}", "Support actions opened."),
	spec("pandokos.support_actions.ended", "OC", "{action}", "Support actions ended, by cause.", "pandokos.support.cause"),
	spec("pandokos.limits.reservations", "G", "{reservation}", "Reservations outstanding in memory."),
	spec("pandokos.limits.counters", "G", "{counter}", "Counters held in memory."),
	spec("pandokos.limits.reservations_expired", "OC", "{reservation}", "Reservations expired by the sweep; it must stay at zero."),
	spec("pandokos.limits.reconciliations", "OC", "{run}", "Reconciliation runs per tenant, by result.", resultAttr...),
	spec("pandokos.limits.reconciliation.last_duration", "G", "s", "Duration of the last reconciliation tick."),
	spec("pandokos.limits.cut_lag", "G", "s", "Time since the last cut of the reconciler; a value that grows is an alert."),
	spec("pandokos.limits.events", "OC", "{event}", "Limit events by result: recorded, dropped, failed.", resultAttr...),
	spec("pandokos.limits.rule_loads", "OC", "{load}", "Loads of the limit rules, by result.", resultAttr...),
	spec("pandokos.limits.admitted_unpriced", "OC", "{request}", "Requests admitted with an unknown estimated cost under limits.on_unknown_price: allow."),
	spec("pandokos.limits.unlimited_traffic", "OC", "{row}", "Rows of unlimited traffic by result: written, skipped, failed.", resultAttr...),
	spec("pandokos.telemetry.spans", "OC", "{span}", "Spans of the trace provider by state: ended, exported, failed. ended minus exported minus failed is what the queue dropped.", "pandokos.telemetry.state"),
	spec("pandokos.telemetry.export_failures", "OC", "{export}", "Exports of telemetry that failed, by signal and class.", "pandokos.telemetry.signal", "error.type"),
	spec("pandokos.log.redactions", "OC", "{value}", "Log values rewritten because they had the shape of a secret; any non-zero value is a bug to report."),
	spec("pandokos.reports.rollups", "OC", "{run}", "Passes of the usage rollup, one per tenant, by result.", resultAttr...),
	spec("pandokos.reports.rollup.lag", "G", "s", "The largest time since the watermark among the tenants of the last rollup pass; 0 before the first."),
	spec("pandokos.retention.removed", "OC", "{item}", "Items removed by retention, by kind.", AttrRetentionKind),
	spec("pandokos.retention.runs", "OC", "{run}", "Runs of the retention job, by result.", resultAttr...),
	spec("pandokos.exports", "OC", "{export}", "Usage and audit exports, by kind and result.", AttrExportKind, "pandokos.result"),

	spec("pandokos.tenant_deletion.steps", "OC", "{step}", "Steps of tenant deletions that ran, by step and result.", AttrDeletionStep, "pandokos.result"),
	spec("pandokos.tenant_deletion.open", "G", "{deletion}", "Tenant deletions not completed, by state: scheduled, running.", AttrDeletionState),
	spec("pandokos.vault.rewraps", "OC", "{key}", "Data keys re-wrapped under the active key-encryption key version, by result.", resultAttr...),
	spec("pandokos.vault.kek.versions", "G", "{version}", "Key-encryption key versions by state.", AttrKEKState),
	spec("pandokos.vault.erasures.pending", "G", "{deletion}", "Completed tenant deletions whose backups stay readable until a key version is retired."),
}

// Catalogue returns the instrument catalogue: the table of the metrics reference.
func Catalogue() []InstrumentSpec {
	out := make([]InstrumentSpec, len(catalogue))
	for i, s := range catalogue {
		s.Attributes = append([]string(nil), s.Attributes...)
		out[i] = s
	}
	return out
}

// specOf returns the catalogue entry of an instrument.
func specOf(name string) InstrumentSpec {
	for _, s := range catalogue {
		if s.Name == name {
			return s
		}
	}
	panic("telemetry: " + name + " is not in the catalogue")
}

// maker creates the instruments that no convention package creates, with the unit and the
// description of the catalogue; the first error is kept.
type maker struct {
	meter metric.Meter
	err   error
}

func (k *maker) fail(err error) { k.err = errors.Join(k.err, err) }

func (k *maker) int64Counter(name string) metric.Int64Counter {
	s := specOf(name)
	c, err := k.meter.Int64Counter(name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description))
	k.fail(err)
	return c
}

func (k *maker) float64Counter(name string) metric.Float64Counter {
	s := specOf(name)
	c, err := k.meter.Float64Counter(name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description))
	k.fail(err)
	return c
}

func (k *maker) float64Histogram(name string) metric.Float64Histogram {
	s := specOf(name)
	c, err := k.meter.Float64Histogram(name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description))
	k.fail(err)
	return c
}

func (k *maker) int64ObservableCounter(name string) metric.Int64ObservableCounter {
	s := specOf(name)
	c, err := k.meter.Int64ObservableCounter(name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description))
	k.fail(err)
	return c
}

func (k *maker) int64ObservableGauge(name string) metric.Int64ObservableGauge {
	s := specOf(name)
	c, err := k.meter.Int64ObservableGauge(name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description))
	k.fail(err)
	return c
}

func (k *maker) float64ObservableGauge(name string) metric.Float64ObservableGauge {
	s := specOf(name)
	c, err := k.meter.Float64ObservableGauge(name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description))
	k.fail(err)
	return c
}

// views are the explicit bucket boundaries of the histograms.
func views() []sdkmetric.View {
	hist := func(name string, bounds []float64) sdkmetric.View {
		return sdkmetric.NewView(sdkmetric.Instrument{Name: name},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: bounds}})
	}
	return []sdkmetric.View{
		hist(nameHTTPDuration, durationBounds),
		hist(nameOpDuration, durationBounds),
		hist(nameFirstChunk, durationBounds),
		hist(nameTokenUsage, tokenBounds),
		hist(nameEstimate, ratioBounds),
	}
}
