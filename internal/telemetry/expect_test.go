package telemetry_test

import (
	"time"

	"github.com/pandokos/pandokos/internal/telemetry"
)

// want is one row of the table of section 5.3 of the sprint, written here independently of
// telemetry.Catalogue() so that the catalogue is checked against the design and not against itself.
// Kind is the letter of the table: H histogram, C counter, G gauge, OC observable counter. Attrs are
// the attribute keys of the instrument (the optional ones included, without the question mark).
type want struct {
	ID, Name, Kind, Unit string
	Attrs                []string
	Prom                 string // the Prometheus name of rule R
}

var wantCatalogue = []want{
	{"M1", "http.server.request.duration", "H", "s", []string{"http.request.method", "url.scheme", "http.response.status_code", "http.route", "error.type"}, "http_server_request_duration_seconds"},
	{"M2", "pandokos.gateway.requests", "C", "{request}", []string{"pandokos.outcome", "pandokos.error_code", "pandokos.limit_state", "gen_ai.provider.name", "pandokos.api_family", "gen_ai.operation.name", "gen_ai.request.model"}, "pandokos_gateway_requests_total"},
	{"M3", "gen_ai.client.operation.duration", "H", "s", []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "error.type"}, "gen_ai_client_operation_duration_seconds"},
	{"M4", "gen_ai.client.operation.time_to_first_chunk", "H", "s", []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model"}, "gen_ai_client_operation_time_to_first_chunk_seconds"},
	{"M5", "gen_ai.client.token.usage", "H", "{token}", []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.token.type", "gen_ai.request.model"}, "gen_ai_client_token_usage"},
	{"M6", "pandokos.gen_ai.client.estimated_tokens", "C", "{token}", []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.token.type", "gen_ai.request.model"}, "pandokos_gen_ai_client_estimated_tokens_total"},
	{"M7", "pandokos.gen_ai.client.cost", "C", "{cost}", []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "pandokos.currency"}, "pandokos_gen_ai_client_cost_total"},
	{"M8", "pandokos.gen_ai.client.cost_unknown", "C", "{request}", []string{"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.request.model", "pandokos.cost.reason"}, "pandokos_gen_ai_client_cost_unknown_total"},
	{"M9", "pandokos.provider.rate_limited", "C", "{response}", []string{"gen_ai.provider.name", "pandokos.credential.id"}, "pandokos_provider_rate_limited_total"},
	{"M10", "pandokos.limits.refusals", "C", "{request}", []string{"pandokos.limit.code", "pandokos.limit.metric", "pandokos.limit.level", "pandokos.limit.subject", "pandokos.limit.scope"}, "pandokos_limits_refusals_total"},
	{"M11", "pandokos.limits.estimate_ratio", "H", "1", []string{"pandokos.limit.quantity"}, "pandokos_limits_estimate_ratio"},
	{"M12", "pandokos.ledger.records", "OC", "{record}", []string{"pandokos.result"}, "pandokos_ledger_records_total"},
	{"M13", "pandokos.ledger.admission_refused", "OC", "{request}", nil, "pandokos_ledger_admission_refused_total"},
	{"M14", "pandokos.ledger.buffer.usage", "G", "{record}", []string{"pandokos.ledger.state"}, "pandokos_ledger_buffer_usage"},
	{"M15", "pandokos.ledger.buffer.limit", "G", "{record}", nil, "pandokos_ledger_buffer_limit"},
	{"M16", "pandokos.egress.blocked", "OC", "{connection}", []string{"pandokos.egress.reason"}, "pandokos_egress_blocked_total"},
	{"M17", "pandokos.authn.attempts", "OC", "{attempt}", []string{"pandokos.listener", "pandokos.authn.kind", "pandokos.authn.result", "pandokos.authn.reason"}, "pandokos_authn_attempts_total"},
	{"M18", "pandokos.jwks.fetches", "OC", "{fetch}", []string{"pandokos.jwks.document", "pandokos.result"}, "pandokos_jwks_fetches_total"},
	{"M19", "pandokos.jwks.sources", "G", "{source}", []string{"pandokos.jwks.state"}, "pandokos_jwks_sources"},
	{"M20", "pandokos.jwks.staleness", "G", "s", nil, "pandokos_jwks_staleness_seconds"},
	{"M21", "pandokos.signin.attempts", "OC", "{attempt}", []string{"pandokos.signin.method", "pandokos.result"}, "pandokos_signin_attempts_total"},
	{"M22", "pandokos.account.locks", "OC", "{lock}", nil, "pandokos_account_locks_total"},
	{"M23", "pandokos.sessions.open", "G", "{session}", nil, "pandokos_sessions_open"},
	{"M24", "pandokos.password.hashes", "G", "{hash}", []string{"pandokos.password.state"}, "pandokos_password_hashes"},
	{"M25", "pandokos.idempotency.replays", "OC", "{request}", nil, "pandokos_idempotency_replays_total"},
	{"M26", "pandokos.authz.grants_refused", "OC", "{request}", nil, "pandokos_authz_grants_refused_total"},
	{"M27", "pandokos.support_actions.opened", "OC", "{action}", nil, "pandokos_support_actions_opened_total"},
	{"M28", "pandokos.support_actions.ended", "OC", "{action}", []string{"pandokos.support.cause"}, "pandokos_support_actions_ended_total"},
	{"M29", "pandokos.limits.reservations", "G", "{reservation}", nil, "pandokos_limits_reservations"},
	{"M30", "pandokos.limits.counters", "G", "{counter}", nil, "pandokos_limits_counters"},
	{"M31", "pandokos.limits.reservations_expired", "OC", "{reservation}", nil, "pandokos_limits_reservations_expired_total"},
	{"M32", "pandokos.limits.reconciliations", "OC", "{run}", []string{"pandokos.result"}, "pandokos_limits_reconciliations_total"},
	{"M33", "pandokos.limits.reconciliation.last_duration", "G", "s", nil, "pandokos_limits_reconciliation_last_duration_seconds"},
	{"M34", "pandokos.limits.cut_lag", "G", "s", nil, "pandokos_limits_cut_lag_seconds"},
	{"M35", "pandokos.limits.events", "OC", "{event}", []string{"pandokos.result"}, "pandokos_limits_events_total"},
	{"M36", "pandokos.limits.rule_loads", "OC", "{load}", []string{"pandokos.result"}, "pandokos_limits_rule_loads_total"},
	{"M37", "pandokos.limits.admitted_unpriced", "OC", "{request}", nil, "pandokos_limits_admitted_unpriced_total"},
	{"M38", "pandokos.limits.unlimited_traffic", "OC", "{row}", []string{"pandokos.result"}, "pandokos_limits_unlimited_traffic_total"},
	{"M39", "pandokos.telemetry.spans", "OC", "{span}", []string{"pandokos.telemetry.state"}, "pandokos_telemetry_spans_total"},
	{"M40", "pandokos.telemetry.export_failures", "OC", "{export}", []string{"pandokos.telemetry.signal", "error.type"}, "pandokos_telemetry_export_failures_total"},
	{"M41", "pandokos.log.redactions", "OC", "{value}", nil, "pandokos_log_redactions_total"},
}

// wantByName indexes wantCatalogue.
func wantByName() map[string]want {
	m := make(map[string]want, len(wantCatalogue))
	for _, w := range wantCatalogue {
		m[w.Name] = w
	}
	return m
}

// dataKind is the shape a collected instrument of the kind letter must have.
func dataKind(kind string) (shape string, monotonic bool) {
	switch kind {
	case "H":
		return "histogram", false
	case "G":
		return "gauge", false
	default:
		return "sum", true
	}
}

// The closed values of section 5.3.1 that the tests of this package know without importing the
// product package that owns them.
var (
	outcomes    = []string{"ok", "pandokos_error", "client_error", "client_cancelled", "shutdown_aborted", "timeout", "upstream_aborted"}
	limitStates = []string{"not_evaluated", "unlimited", "limited", "unverified"}
	families    = []string{"openai", "anthropic"}
	operations  = []string{"chat", "embeddings", "messages.count_tokens"}
	callErrors  = []string{"timeout", "upstream_unreachable", "upstream_aborted", "cancelled", "_OTHER"}
	tokenTypes  = []string{"input", "output"}
	costReasons = []string{"no_price", "category_unpriced", "inconsistent_usage"}
	limitCodes  = []string{"rate_limited", "quota_exceeded", "no_price"}
	limitLevels = []string{"platform", "tenant"}
	limitKinds  = []string{"tenant", "service_account", "key"}
	limitScopes = []string{"all", "provider", "model"}
	quantities  = []string{"tokens", "cost"}
)

// ledgerSource and the other fixtures return the snapshots the Sources functions give in the tests
// that read the observable instruments. Every number is distinct so that a swapped field is seen.
var fixtureNow = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func fullSources() telemetry.Sources {
	return telemetry.Sources{
		Ledger: func() telemetry.LedgerStats {
			return telemetry.LedgerStats{Written: 105, Rejected: 7, Unwritten: 3, Refused: 11, InUse: 13, Queued: 5}
		},
		LedgerLimit: 256,
		Egress: func() telemetry.EgressStats {
			return telemetry.EgressStats{Blocked: map[string]int64{"private_address": 4, "scheme": 2}}
		},
		Authn: map[string]func() telemetry.AuthnStats{
			"inference": func() telemetry.AuthnStats {
				return telemetry.AuthnStats{Attempts: map[telemetry.AuthnKey]int64{
					{Kind: "key", Result: "ok"}:                        20,
					{Kind: "key", Result: "failed", Reason: "revoked"}: 3,
					{Kind: "token", Result: "unavailable"}:             1,
				}}
			},
			"admin": func() telemetry.AuthnStats {
				return telemetry.AuthnStats{Attempts: map[telemetry.AuthnKey]int64{
					{Kind: "key", Result: "ok"}:                              6,
					{Kind: "unknown", Result: "failed", Reason: "malformed"}: 2,
				}}
			},
		},
		JWKS: func() telemetry.JWKSStats {
			return telemetry.JWKSStats{
				Fetches: map[telemetry.JWKSFetchKey]int64{
					{Document: "keys", Result: "ok"}:      9,
					{Document: "keys", Result: "timeout"}: 2,
					{Document: "metadata", Result: "ok"}:  4,
				},
				Sources:     map[string]int64{"fresh": 2, "stale": 1, "missing": 1},
				OldestStale: fixtureNow.Add(-90 * time.Second),
			}
		},
		SignIn: func() telemetry.SignInStats {
			return telemetry.SignInStats{
				Attempts: map[telemetry.SignInKey]int64{
					{Method: "password", Result: "ok"}:     30,
					{Method: "password", Result: "failed"}: 5,
					{Method: "oidc", Result: "refused"}:    2,
				},
				Locks: 3,
			}
		},
		Sessions:    func() (int64, bool) { return 17, true },
		Hashes:      func() telemetry.HashStats { return telemetry.HashStats{Active: 2, Waiting: 6} },
		Idempotency: func() int64 { return 8 },
		Admin: func() telemetry.AdminStats {
			return telemetry.AdminStats{GrantsRefused: 4, SupportOpened: 6, SupportEnded: map[string]int64{"closed": 3, "ended": 2}}
		},
		Limits: func() telemetry.LimitsStats {
			return telemetry.LimitsStats{
				Reservations: 12, Counters: 34, ReservationsExpired: 0,
				Reconciliations:   map[string]int64{"ok": 21, "failed": 1},
				ReconcileDuration: 1500 * time.Millisecond,
				LastCut:           fixtureNow.Add(-45 * time.Second),
				Events:            map[string]int64{"recorded": 50, "dropped": 2, "failed": 1},
				RuleLoads:         map[string]int64{"ok": 14, "failed": 1},
			}
		},
		Resolver: func() telemetry.ResolverStats {
			return telemetry.ResolverStats{AdmittedUnpriced: 9, Unlimited: map[string]int64{"written": 40, "skipped": 3, "failed": 1}}
		},
	}
}

// chatRequest is a forwarded, successful non-stream chat request on one model.
func chatRequest(model string) telemetry.Request {
	return telemetry.Request{
		Method: "POST", Scheme: "https", Route: "/{provider}/v1/chat/completions", Status: 200,
		Outcome: "ok", LimitState: "limited", Provider: "acme", Family: "openai", Operation: "chat", Model: model,
		Duration: 120 * time.Millisecond, Forwarded: true, Call: 100 * time.Millisecond, UpstreamStatus: 200,
		InputTokens: 100, OutputTokens: 20, InputSource: "provider", OutputSource: "provider",
		CostKnown: true, CostAmount: 0.25, CostCurrency: "EUR",
	}
}

// streamRequest is a forwarded stream request with a first chunk.
func streamRequest(model string) telemetry.Request {
	r := chatRequest(model)
	r.Stream, r.FirstChunk = true, 40*time.Millisecond
	return r
}

// withFullSources sets every source and the fixed clock.
func withFullSources(o *telemetry.MetricsOptions) {
	o.Sources = fullSources()
	o.Now = func() time.Time { return fixtureNow }
}
