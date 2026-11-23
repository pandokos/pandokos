package main

import (
	"time"

	"github.com/pandokos/pandokos/internal/account"
	"github.com/pandokos/pandokos/internal/admin"
	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/idem"
	"github.com/pandokos/pandokos/internal/jwks"
	"github.com/pandokos/pandokos/internal/ledger"
	"github.com/pandokos/pandokos/internal/lifecycle"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/outbox"
	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/vault"
)

// The components import neither internal/telemetry nor the OpenTelemetry metric API; this file is
// where their snapshots become the telemetry package's (ADR 0062).

// gatewayMetrics is the gateway's Metrics port over the telemetry package: a field-by-field copy of
// the observation, with no allocation.
type gatewayMetrics struct{ m *telemetry.Metrics }

var _ gateway.Metrics = gatewayMetrics{}

func (g gatewayMetrics) Observe(o gateway.RequestObservation) {
	g.m.ObserveRequest(telemetry.Request{
		Method: o.Method, Scheme: o.Scheme, Route: o.Route, Status: o.Status, Outcome: o.Outcome, ErrorCode: o.ErrorCode,
		LimitState: o.LimitState, Provider: o.Provider, Family: o.Family, Operation: o.Operation, Model: o.Model, Duration: o.Duration,
		Forwarded: o.Forwarded, Call: o.Call, FirstChunk: o.FirstChunk, Stream: o.Stream, CallError: o.CallError,
		UpstreamStatus: o.UpstreamStatus, CredentialID: o.CredentialID,
		InputTokens: o.InputTokens, OutputTokens: o.OutputTokens, InputSource: o.InputSource, OutputSource: o.OutputSource,
		CostKnown: o.CostKnown, CostAmount: o.CostAmount, CostCurrency: o.CostCurrency, CostReason: o.CostReason,
		Refusal:  (*telemetry.RefusalObservation)(o.Refusal),
		Estimate: (*telemetry.EstimateObservation)(o.Estimate),
	})
}

func ledgerSource(w *ledger.Writer) func() telemetry.LedgerStats {
	return func() telemetry.LedgerStats {
		s := w.Stats()
		return telemetry.LedgerStats{
			Written: s.Written, Rejected: s.Rejected, Unwritten: s.Unwritten, Refused: s.Refused,
			InUse: int64(s.InUse), Queued: int64(s.Queued),
		}
	}
}

func egressSource(g *egress.Guard) func() telemetry.EgressStats {
	return func() telemetry.EgressStats { return telemetry.EgressStats{Blocked: g.Stats().Blocked} }
}

func authnSource(a *authn.Authenticator) func() telemetry.AuthnStats {
	return func() telemetry.AuthnStats {
		src := a.Stats().Attempts
		out := make(map[telemetry.AuthnKey]int64, len(src))
		for k, n := range src {
			out[telemetry.AuthnKey(k)] = n
		}
		return telemetry.AuthnStats{Attempts: out}
	}
}

func jwksSource(c *jwks.Cache) func() telemetry.JWKSStats {
	return func() telemetry.JWKSStats {
		s := c.Stats()
		out := telemetry.JWKSStats{Fetches: make(map[telemetry.JWKSFetchKey]int64, len(s.Fetches)), Sources: s.Sources, OldestStale: s.OldestStale}
		for k, n := range s.Fetches {
			out.Fetches[telemetry.JWKSFetchKey(k)] = n
		}
		return out
	}
}

// signInSource merges the local sign-ins of the account service with the OIDC ones of the
// administration handler.
func signInSource(a *account.Service, h *admin.Handler) func() telemetry.SignInStats {
	return func() telemetry.SignInStats {
		local, oidc := a.Stats(), h.Stats().SignIn
		out := telemetry.SignInStats{Attempts: make(map[telemetry.SignInKey]int64, len(local.Attempts)+len(oidc.Attempts)), Locks: local.Locks + oidc.Locks}
		for _, s := range []account.SignInStats{local, oidc} {
			for k, n := range s.Attempts {
				out.Attempts[telemetry.SignInKey(k)] += n
			}
		}
		return out
	}
}

func hashSource(h *password.Hasher) func() telemetry.HashStats {
	return func() telemetry.HashStats {
		s := h.Stats()
		return telemetry.HashStats{Active: s.Active, Waiting: s.Waiting}
	}
}

func idempotencySource(s *idem.Store) func() int64 {
	return func() int64 { return s.Stats().Replays }
}

func adminSource(h *admin.Handler) func() telemetry.AdminStats {
	return func() telemetry.AdminStats {
		s := h.Stats()
		return telemetry.AdminStats{GrantsRefused: s.GrantsRefused, SupportOpened: s.SupportOpened, SupportEnded: s.SupportEnded}
	}
}

func limitsSource(m *limits.Memory, r *limits.Reconciler, e *limits.EventWriter, c *limits.RuleCache) func() telemetry.LimitsStats {
	return func() telemetry.LimitsStats {
		rs, es, cs := r.Stats(), e.Stats(), c.Stats()
		return telemetry.LimitsStats{
			Reservations: int64(m.Outstanding()), Counters: int64(m.Counters()), ReservationsExpired: m.Stats().Expired,
			Reconciliations:   map[string]int64{"ok": rs.OK, "failed": rs.Failed},
			ReconcileDuration: rs.LastDuration, LastCut: rs.LastCut,
			Events:    map[string]int64{"recorded": es.Recorded, "dropped": es.Dropped, "failed": es.Failed},
			RuleLoads: map[string]int64{"ok": cs.OK, "failed": cs.Failed},
		}
	}
}

func resolverSource(l *resolver.Limits, u *resolver.UnlimitedAuditor) func() telemetry.ResolverStats {
	return func() telemetry.ResolverStats {
		us := u.Stats()
		return telemetry.ResolverStats{
			AdmittedUnpriced: l.Stats().AdmittedUnpriced,
			Unlimited:        map[string]int64{"written": us.Written, "skipped": us.Skipped, "failed": us.Failed},
		}
	}
}

// reportsSource reads the usage rollup, retention and the exports of the administration handler
// (M42 to M46): the counters of the whole installation, never of a tenant.
func reportsSource(r *reports.Rollup, t *reports.Retention, j *outbox.Janitor, h *admin.Handler) func() telemetry.ReportsStats {
	return func() telemetry.ReportsStats {
		rs, ts, es := r.Stats(), t.Stats(), h.Stats()
		out := telemetry.ReportsStats{
			Rollups:          map[string]int64{"ok": rs.OK, "failed": rs.Failed},
			RollupLag:        rs.Lag,
			RetentionRemoved: map[string]int64{},
			RetentionRuns:    map[string]int64{"ok": max(ts.Runs-ts.Failed, 0), "failed": ts.Failed},
			Exports:          map[telemetry.ExportKey]int64{},
		}
		for _, kind := range telemetry.RetentionKinds {
			out.RetentionRemoved[kind] = ts.Removed[kind]
		}
		js := j.Stats()
		out.RetentionRemoved[telemetry.RetentionOutboxEvent] = int64(js.Events)     //nolint:gosec // a count
		out.RetentionRemoved[telemetry.RetentionLimitEvent] = int64(js.LimitEvents) //nolint:gosec // a count
		for _, kind := range telemetry.ExportKinds {
			out.Exports[telemetry.ExportKey{Kind: kind, Result: "complete"}] = es.ExportsComplete[kind]
			out.Exports[telemetry.ExportKey{Kind: kind, Result: "aborted"}] = es.ExportsAborted[kind]
		}
		return out
	}
}

// lifecycleSource reads the deletion job and the re-wrap job (M47 to M51): counts of the whole
// installation, never of a tenant.
func lifecycleSource(d *lifecycle.Deleter, r *vault.Rewrapper) func() telemetry.LifecycleStats {
	return func() telemetry.LifecycleStats {
		ds, rs := d.Stats(), r.Stats()
		return telemetry.LifecycleStats{
			Steps: ds.Steps, Open: ds.Open,
			Rewraps:     map[string]int64{"ok": rs.OK, "failed": rs.Failed},
			KEKVersions: rs.Versions, ErasuresPending: rs.ErasuresPending,
		}
	}
}

// eventsSource reads the relay of events and its janitor (M52, M54 to M58): counts of the whole
// installation, never of a tenant, a destination or a URL.
func eventsSource(r *outbox.Relay, j *outbox.Janitor) func() telemetry.EventsStats {
	return func() telemetry.EventsStats {
		rs, js := r.Stats(), j.Stats()
		out := telemetry.EventsStats{
			Deliveries:  make(map[string]int64, len(telemetry.DeliveryOutcomes)),
			DeadLetters: int64(rs.DeadLetters), //nolint:gosec // a count
			Expired:     int64(js.Expired),     //nolint:gosec // a count
			Destinations: map[string]int64{
				telemetry.DestinationEnabled: rs.Destinations, telemetry.DestinationFailing: rs.Failing, telemetry.DestinationDisabled: rs.Disabled,
			},
			Lag:    time.Duration(rs.LagSeconds * float64(time.Second)),
			Passes: map[string]int64{"ok": int64(rs.PassesOK), "failed": int64(rs.PassesFailed)}, //nolint:gosec // counts
		}
		for _, o := range telemetry.DeliveryOutcomes {
			out.Deliveries[o] = int64(rs.Deliveries[outbox.Outcome(o)]) //nolint:gosec // a count
		}
		return out
	}
}
