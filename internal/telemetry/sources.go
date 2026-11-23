package telemetry

import (
	"context"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// attr is the measurement option of one attribute set built from key/value pairs; "" values are
// left out.
func attr(kv ...string) metric.MeasurementOption {
	kvs := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			kvs = append(kvs, attribute.String(kv[i], kv[i+1]))
		}
	}
	return metric.WithAttributeSet(attribute.NewSet(kvs...))
}

// seconds is the time from t to now, in seconds; never negative.
func seconds(now time.Time, t time.Time) float64 {
	d := now.Sub(t).Seconds()
	if d < 0 {
		return 0
	}
	return d
}

// sortedKeys returns the keys of a map in order, so that the observations of one collection are
// made in a fixed order.
func sortedKeys[K ~string, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// registerSources registers one callback for each non-nil source of the options, over the
// observable instruments of that source. A nil source leaves its instruments out. Each callback
// calls its source once and observes every value it holds.
func registerSources(k *maker, o MetricsOptions) {
	register := func(f func(context.Context, metric.Observer) error, insts ...metric.Observable) {
		if k.err != nil {
			return
		}
		_, err := k.meter.RegisterCallback(f, insts...)
		k.fail(err)
	}
	s := o.Sources

	if s.Ledger != nil {
		records := k.int64ObservableCounter("pandokos.ledger.records")
		refused := k.int64ObservableCounter("pandokos.ledger.admission_refused")
		usage := k.int64ObservableGauge("pandokos.ledger.buffer.usage")
		limit := k.int64ObservableGauge("pandokos.ledger.buffer.limit")
		register(func(_ context.Context, ob metric.Observer) error {
			l := s.Ledger()
			ob.ObserveInt64(records, l.Written, attr("pandokos.result", "written"))
			ob.ObserveInt64(records, l.Rejected, attr("pandokos.result", "rejected"))
			ob.ObserveInt64(records, l.Unwritten, attr("pandokos.result", "lost"))
			ob.ObserveInt64(refused, l.Refused)
			ob.ObserveInt64(usage, l.InUse, attr("pandokos.ledger.state", "in_use"))
			ob.ObserveInt64(usage, l.Queued, attr("pandokos.ledger.state", "queued"))
			ob.ObserveInt64(limit, int64(s.LedgerLimit))
			return nil
		}, records, refused, usage, limit)
	}

	if s.Egress != nil {
		blocked := k.int64ObservableCounter("pandokos.egress.blocked")
		register(func(_ context.Context, ob metric.Observer) error {
			e := s.Egress()
			for _, reason := range sortedKeys(e.Blocked) {
				ob.ObserveInt64(blocked, e.Blocked[reason], attr("pandokos.egress.reason", reason))
			}
			return nil
		}, blocked)
	}

	if len(s.Authn) > 0 {
		attempts := k.int64ObservableCounter("pandokos.authn.attempts")
		register(func(_ context.Context, ob metric.Observer) error {
			for _, listener := range sortedKeys(s.Authn) {
				fn := s.Authn[listener]
				if fn == nil {
					continue
				}
				for key, n := range fn().Attempts {
					ob.ObserveInt64(attempts, n, attr("pandokos.listener", listener, "pandokos.authn.kind", key.Kind,
						"pandokos.authn.result", key.Result, "pandokos.authn.reason", key.Reason))
				}
			}
			return nil
		}, attempts)
	}

	if s.JWKS != nil {
		fetches := k.int64ObservableCounter("pandokos.jwks.fetches")
		sources := k.int64ObservableGauge("pandokos.jwks.sources")
		staleness := k.float64ObservableGauge("pandokos.jwks.staleness")
		register(func(_ context.Context, ob metric.Observer) error {
			j := s.JWKS()
			for key, n := range j.Fetches {
				ob.ObserveInt64(fetches, n, attr("pandokos.jwks.document", key.Document, "pandokos.result", key.Result))
			}
			for _, state := range sortedKeys(j.Sources) {
				ob.ObserveInt64(sources, j.Sources[state], attr("pandokos.jwks.state", state))
			}
			var age float64
			if !j.OldestStale.IsZero() {
				age = seconds(o.Now(), j.OldestStale)
			}
			ob.ObserveFloat64(staleness, age)
			return nil
		}, fetches, sources, staleness)
	}

	if s.SignIn != nil {
		attempts := k.int64ObservableCounter("pandokos.signin.attempts")
		locks := k.int64ObservableCounter("pandokos.account.locks")
		register(func(_ context.Context, ob metric.Observer) error {
			si := s.SignIn()
			for key, n := range si.Attempts {
				ob.ObserveInt64(attempts, n, attr("pandokos.signin.method", key.Method, "pandokos.result", key.Result))
			}
			ob.ObserveInt64(locks, si.Locks)
			return nil
		}, attempts, locks)
	}

	if s.Sessions != nil {
		open := k.int64ObservableGauge("pandokos.sessions.open")
		register(func(_ context.Context, ob metric.Observer) error {
			if n, ok := s.Sessions(); ok {
				ob.ObserveInt64(open, n)
			}
			return nil
		}, open)
	}

	if s.Hashes != nil {
		hashes := k.int64ObservableGauge("pandokos.password.hashes")
		register(func(_ context.Context, ob metric.Observer) error {
			h := s.Hashes()
			ob.ObserveInt64(hashes, h.Active, attr("pandokos.password.state", "active"))
			ob.ObserveInt64(hashes, h.Waiting, attr("pandokos.password.state", "waiting"))
			return nil
		}, hashes)
	}

	if s.Idempotency != nil {
		replays := k.int64ObservableCounter("pandokos.idempotency.replays")
		register(func(_ context.Context, ob metric.Observer) error {
			ob.ObserveInt64(replays, s.Idempotency())
			return nil
		}, replays)
	}

	if s.Admin != nil {
		grants := k.int64ObservableCounter("pandokos.authz.grants_refused")
		opened := k.int64ObservableCounter("pandokos.support_actions.opened")
		ended := k.int64ObservableCounter("pandokos.support_actions.ended")
		register(func(_ context.Context, ob metric.Observer) error {
			a := s.Admin()
			ob.ObserveInt64(grants, a.GrantsRefused)
			ob.ObserveInt64(opened, a.SupportOpened)
			for _, cause := range sortedKeys(a.SupportEnded) {
				ob.ObserveInt64(ended, a.SupportEnded[cause], attr("pandokos.support.cause", cause))
			}
			return nil
		}, grants, opened, ended)
	}

	if s.Limits != nil {
		reservations := k.int64ObservableGauge("pandokos.limits.reservations")
		counters := k.int64ObservableGauge("pandokos.limits.counters")
		expired := k.int64ObservableCounter("pandokos.limits.reservations_expired")
		reconciliations := k.int64ObservableCounter("pandokos.limits.reconciliations")
		lastDuration := k.float64ObservableGauge("pandokos.limits.reconciliation.last_duration")
		cutLag := k.float64ObservableGauge("pandokos.limits.cut_lag")
		events := k.int64ObservableCounter("pandokos.limits.events")
		ruleLoads := k.int64ObservableCounter("pandokos.limits.rule_loads")
		register(func(_ context.Context, ob metric.Observer) error {
			l := s.Limits()
			ob.ObserveInt64(reservations, l.Reservations)
			ob.ObserveInt64(counters, l.Counters)
			ob.ObserveInt64(expired, l.ReservationsExpired)
			for _, result := range sortedKeys(l.Reconciliations) {
				ob.ObserveInt64(reconciliations, l.Reconciliations[result], attr("pandokos.result", result))
			}
			ob.ObserveFloat64(lastDuration, l.ReconcileDuration.Seconds())
			if !l.LastCut.IsZero() { // before the first cut there is no lag to report
				ob.ObserveFloat64(cutLag, seconds(o.Now(), l.LastCut))
			}
			for _, result := range sortedKeys(l.Events) {
				ob.ObserveInt64(events, l.Events[result], attr("pandokos.result", result))
			}
			for _, result := range sortedKeys(l.RuleLoads) {
				ob.ObserveInt64(ruleLoads, l.RuleLoads[result], attr("pandokos.result", result))
			}
			return nil
		}, reservations, counters, expired, reconciliations, lastDuration, cutLag, events, ruleLoads)
	}

	if s.Reports != nil {
		rollups := k.int64ObservableCounter("pandokos.reports.rollups")
		lag := k.float64ObservableGauge("pandokos.reports.rollup.lag")
		removed := k.int64ObservableCounter("pandokos.retention.removed")
		runs := k.int64ObservableCounter("pandokos.retention.runs")
		exports := k.int64ObservableCounter("pandokos.exports")
		register(func(_ context.Context, ob metric.Observer) error {
			r := s.Reports()
			for _, result := range sortedKeys(r.Rollups) {
				ob.ObserveInt64(rollups, r.Rollups[result], attr("pandokos.result", result))
			}
			ob.ObserveFloat64(lag, r.RollupLag.Seconds())
			for _, kind := range sortedKeys(r.RetentionRemoved) {
				ob.ObserveInt64(removed, r.RetentionRemoved[kind], attr(AttrRetentionKind, kind))
			}
			for _, result := range sortedKeys(r.RetentionRuns) {
				ob.ObserveInt64(runs, r.RetentionRuns[result], attr("pandokos.result", result))
			}
			keys := make([]ExportKey, 0, len(r.Exports))
			for key := range r.Exports {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				if keys[i].Kind != keys[j].Kind {
					return keys[i].Kind < keys[j].Kind
				}
				return keys[i].Result < keys[j].Result
			})
			for _, key := range keys {
				ob.ObserveInt64(exports, r.Exports[key], attr(AttrExportKind, key.Kind), attr("pandokos.result", key.Result))
			}
			return nil
		}, rollups, lag, removed, runs, exports)
	}

	if s.Events != nil {
		deliveries := k.int64ObservableCounter("pandokos.events.deliveries")
		dead := k.int64ObservableCounter("pandokos.events.dead_letters")
		expired := k.int64ObservableCounter("pandokos.events.expired")
		dests := k.int64ObservableGauge("pandokos.events.destinations")
		lag := k.float64ObservableGauge("pandokos.events.lag")
		passes := k.int64ObservableCounter("pandokos.events.relay.passes")
		register(func(_ context.Context, ob metric.Observer) error {
			e := s.Events()
			for _, outcome := range sortedKeys(e.Deliveries) {
				ob.ObserveInt64(deliveries, e.Deliveries[outcome], attr(AttrDeliveryOutcome, outcome))
			}
			ob.ObserveInt64(dead, e.DeadLetters)
			ob.ObserveInt64(expired, e.Expired)
			for _, state := range sortedKeys(e.Destinations) {
				ob.ObserveInt64(dests, e.Destinations[state], attr(AttrDestinationState, state))
			}
			ob.ObserveFloat64(lag, e.Lag.Seconds())
			for _, result := range sortedKeys(e.Passes) {
				ob.ObserveInt64(passes, e.Passes[result], attr("pandokos.result", result))
			}
			return nil
		}, deliveries, dead, expired, dests, lag, passes)
	}

	if s.Resolver != nil {
		unpriced := k.int64ObservableCounter("pandokos.limits.admitted_unpriced")
		unlimited := k.int64ObservableCounter("pandokos.limits.unlimited_traffic")
		register(func(_ context.Context, ob metric.Observer) error {
			r := s.Resolver()
			ob.ObserveInt64(unpriced, r.AdmittedUnpriced)
			for _, result := range sortedKeys(r.Unlimited) {
				ob.ObserveInt64(unlimited, r.Unlimited[result], attr("pandokos.result", result))
			}
			return nil
		}, unpriced, unlimited)
	}

	if s.Lifecycle != nil {
		steps := k.int64ObservableCounter("pandokos.tenant_deletion.steps")
		open := k.int64ObservableGauge("pandokos.tenant_deletion.open")
		rewraps := k.int64ObservableCounter("pandokos.vault.rewraps")
		versions := k.int64ObservableGauge("pandokos.vault.kek.versions")
		pending := k.int64ObservableGauge("pandokos.vault.erasures.pending")
		register(func(_ context.Context, ob metric.Observer) error {
			l := s.Lifecycle()
			keys := make([][2]string, 0, len(l.Steps))
			for key := range l.Steps {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				if keys[i][0] != keys[j][0] {
					return keys[i][0] < keys[j][0]
				}
				return keys[i][1] < keys[j][1]
			})
			for _, key := range keys {
				ob.ObserveInt64(steps, l.Steps[key], attr(AttrDeletionStep, key[0], "pandokos.result", key[1]))
			}
			for _, state := range sortedKeys(l.Open) {
				ob.ObserveInt64(open, l.Open[state], attr(AttrDeletionState, state))
			}
			for _, result := range sortedKeys(l.Rewraps) {
				ob.ObserveInt64(rewraps, l.Rewraps[result], attr("pandokos.result", result))
			}
			for _, state := range sortedKeys(l.KEKVersions) {
				ob.ObserveInt64(versions, l.KEKVersions[state], attr(AttrKEKState, state))
			}
			ob.ObserveInt64(pending, l.ErasuresPending)
			return nil
		}, steps, open, rewraps, versions, pending)
	}
}
