package reports_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/reports"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/test/dbtest"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// reportKey is the group of a row of the report as the test sums it by hand.
type reportKey struct {
	period                                                  int
	provider, model, modelID, kind, principal, key, cur, ps string
}

type reportSum struct {
	requests, ok, rejected                            int64
	in, out, cacheRead, cacheWrite, reason, ein, eout int64
	cost                                              *big.Rat
	known, unknown                                    int64
}

func ratOf(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("not a decimal: %q", s)
	}
	return r
}

// periodBounds returns [from, every boundary of the period strictly inside, to) in the zone.
func periodBounds(from, to time.Time, period string, loc *time.Location) []time.Time {
	out := []time.Time{from}
	switch period {
	case "day", "month":
		f := from.In(loc)
		y, m, d := f.Date()
		for i := 0; i < 400; i++ {
			var b time.Time
			if period == "day" {
				b = time.Date(y, m, d+i, 0, 0, 0, 0, loc)
			} else {
				b = time.Date(y, m+time.Month(i), 1, 0, 0, 0, 0, loc)
			}
			if b.After(from) && b.Before(to) {
				out = append(out, b)
			}
			if !b.Before(to) {
				break
			}
		}
	}
	return append(out, to)
}

// TestExactReaderProperty is T-947: whatever the rollup holds, the exact reader gives the sum over
// the records. Records of every kind (outcomes, sources, known, unknown and no cost, two currencies,
// both price sources and none, with and without a credential, a provider that has been renamed, a
// record without a model ID) are written with random start times and random recording times, the
// rollup runs at random moments, and after each step the limit counters equal the ledger reader's
// and the report equals a sum in Go over the raw records, for random windows, groupings, periods and
// zones.
func TestExactReaderProperty(t *testing.T) {
	d := dbtest.NewMigrated(t)
	tid := d.SeedTenant(t, "exact")
	rng := seed(t)
	worker, admin := d.Open(t, store.RoleWorker), d.Open(t, store.RoleAdmin)
	roll := &reports.Rollup{DB: worker, Interval: time.Minute, Logger: quietLogger()}
	models := []string{"", d.SeedModel(t, "openai", "exact-a"), d.SeedModel(t, "anthropic", "exact-b")}
	principals := []string{uuidOf(1), uuidOf(2), uuidOf(3)}
	keys := []string{"", "tkeyaaaaaaaaaaaaa", "tkeybbbbbbbbbbbbb"}
	providers := []string{"openai", "anthropic", "renamed-before"}
	zones := []string{"UTC", "Europe/Rome", "Asia/Kolkata", "Asia/Kathmandu"}
	periods := []string{"day", "month", "total"}
	c := ctx(t)

	var all []rec
	var wm time.Time
	dbNow := func() time.Time {
		var n time.Time
		if err := d.SharedSuper(t).QueryRow(c, `SELECT clock_timestamp()`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n.UTC()
	}

	newRec := func(now time.Time) rec {
		r := rec{Principal: principals[rng.Intn(len(principals))], Key: keys[rng.Intn(len(keys))], Provider: providers[rng.Intn(len(providers))],
			ModelID: models[rng.Intn(len(models))], Credential: rng.Intn(4) > 0}
		if r.ModelID != "" {
			r.Model = "exact-model"
		}
		switch rng.Intn(5) {
		case 0, 1:
			r.Started = now.Add(-time.Duration(rng.Int63n(int64(2 * time.Hour))))
		case 2, 3:
			r.Started = now.Add(-time.Duration(rng.Int63n(int64(72 * time.Hour))))
		default:
			r.Started = now.Add(-time.Duration(rng.Int63n(int64(40 * 24 * time.Hour))))
		}
		r.Started = r.Started.Truncate(time.Microsecond)
		// Recorded at or after the watermark: what the database's clock guarantees. Mostly in the
		// past (a record that waited), sometimes now.
		lo := wm
		if lo.IsZero() || lo.After(now) {
			lo = now.Add(-10 * time.Minute)
		}
		r.Recorded = lo.Add(time.Duration(rng.Int63n(int64(now.Sub(lo)) + 1))).Truncate(time.Microsecond)
		if rng.Intn(6) == 0 {
			r.Recorded = now
		}
		r.Outcome = []string{"ok", "ok", "ok", "upstream_error", "cancelled", "timeout", "rejected_limit", "rejected_policy", "internal_error"}[rng.Intn(9)]
		r.InSource, r.OutSource = []string{"provider", "estimated", "none"}[rng.Intn(3)], []string{"provider", "estimated", "none"}[rng.Intn(3)]
		r.In, r.Out, r.Est = rng.Int63n(5000), rng.Int63n(5000), rng.Int63n(5000)
		if r.InSource == "provider" && rng.Intn(2) == 0 {
			r.CacheRead = 1 + rng.Int63n(100)
		}
		if r.OutSource == "provider" && rng.Intn(2) == 0 {
			r.Reason = 1 + rng.Int63n(100)
		}
		switch rng.Intn(4) {
		case 0, 1:
			r.CostStatus, r.Cost, r.Currency = "known", fmt.Sprintf("%d.%06d", rng.Intn(50), rng.Intn(1000000)), []string{"EUR", "USD"}[rng.Intn(2)]
			r.PriceSource = []string{"", "catalogue", "tenant"}[rng.Intn(3)]
		case 2:
			r.CostStatus = "unknown"
		}
		return r
	}

	sumGo := func(from, to time.Time, byProv, byModel, byCaller, byKey bool, provider string, bounds []time.Time) map[reportKey]*reportSum {
		out := map[reportKey]*reportSum{}
		for _, r := range all {
			if r.Started.Before(from) || !r.Started.Before(to) || provider != "" && r.Provider != provider {
				continue
			}
			p := 0
			for i, b := range bounds {
				if !r.Started.Before(b) {
					p = i + 1
				}
			}
			k := reportKey{period: p, cur: r.Currency, ps: r.PriceSource}
			if r.PriceSource == "" && r.CostStatus == "known" {
				k.ps = "catalogue"
			}
			if r.CostStatus != "known" {
				k.cur = ""
			}
			if byProv {
				k.provider = r.Provider
			}
			if byModel {
				k.model, k.modelID = r.Model, r.ModelID
			}
			if byCaller {
				k.kind, k.principal = "service_account", r.Principal
			}
			if byKey {
				k.key = r.Key
			}
			s := out[k]
			if s == nil {
				s = &reportSum{cost: new(big.Rat)}
				out[k] = s
			}
			if r.Outcome != "rejected_limit" {
				s.requests++
			}
			if r.Outcome == "ok" {
				s.ok++
			}
			if r.Outcome == "rejected_limit" {
				s.rejected++
			}
			if r.InSource == "provider" {
				s.in += r.In
				s.cacheRead += r.CacheRead
			}
			if r.OutSource == "provider" {
				s.out += r.Out
				s.reason += r.Reason
			}
			if r.InSource == "estimated" {
				s.ein += r.Est
			}
			if r.OutSource == "estimated" {
				s.eout += r.Out
			}
			if r.CostStatus == "known" {
				s.cost.Add(s.cost, ratOf(t, r.Cost))
				s.known++
			}
			if r.CostStatus == "unknown" {
				s.unknown++
			}
		}
		return out
	}

	checkReport := func(now time.Time) {
		t.Helper()
		zone := zones[rng.Intn(len(zones))]
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		period := periods[rng.Intn(len(periods))]
		from := now.Add(-time.Duration(rng.Int63n(int64(35 * 24 * time.Hour)))).Truncate(time.Microsecond)
		to := from.Add(time.Duration(rng.Int63n(int64(35*24*time.Hour))) + time.Minute).Truncate(time.Microsecond)
		switch rng.Intn(3) {
		case 0:
			from = from.Truncate(time.Hour)
		case 1:
			from, to = from.Truncate(time.Hour), to.Truncate(time.Hour).Add(time.Hour)
		}
		if to.After(now.Add(time.Hour)) {
			to = now.Add(time.Hour)
		}
		if !to.After(from) {
			return
		}
		byProv, byModel, byCaller, byKey := rng.Intn(2) == 0, rng.Intn(2) == 0, rng.Intn(3) == 0, rng.Intn(3) == 0
		provider := ""
		if rng.Intn(5) == 0 {
			provider = providers[rng.Intn(2)]
		}
		bounds := periodBounds(from, to, period, loc)
		want := sumGo(from, to, byProv, byModel, byCaller, byKey, provider, bounds)
		var rows []queries.UsageReportRow
		if err := admin.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			var err error
			rows, err = queries.New(tx).UsageReport(c, queries.UsageReportParams{ByProvider: byProv, ByModel: byModel, ByCaller: byCaller, ByKey: byKey,
				Provider: provider, MaxRows: 100000, Bounds: bounds})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		got := map[reportKey]*reportSum{}
		for _, r := range rows {
			k := reportKey{period: int(r.Period), provider: r.Provider, model: r.Model, modelID: r.ModelID, kind: r.PrincipalKind, principal: r.PrincipalID, key: r.KeyID, cur: r.Currency, ps: r.PriceSource}
			got[k] = &reportSum{requests: r.Requests, ok: r.RequestsOk, rejected: r.RejectedLimit, in: r.InputTokens, out: r.OutputTokens, cacheRead: r.CacheReadTokens,
				cacheWrite: r.CacheWriteTokens, reason: r.ReasoningTokens, ein: r.EstimatedInputTokens, eout: r.EstimatedOutputTokens, cost: ratOf(t, r.Cost), known: r.CostKnown, unknown: r.CostUnknown}
		}
		if len(got) != len(want) {
			t.Fatalf("report %s %s %v..%v by %v %v %v %v: %d groups, want %d (rollup state %v)", period, zone, from, to, byProv, byModel, byCaller, byKey, len(got), len(want), wm)
		}
		for k, w := range want {
			g := got[k]
			if g == nil {
				t.Fatalf("report %s %s %v..%v: the group %+v is missing", period, zone, from, to, k)
			}
			if g.requests != w.requests || g.ok != w.ok || g.rejected != w.rejected || g.in != w.in || g.out != w.out || g.cacheRead != w.cacheRead || g.reason != w.reason ||
				g.ein != w.ein || g.eout != w.eout || g.cost.Cmp(w.cost) != 0 || g.known != w.known || g.unknown != w.unknown {
				t.Fatalf("report %s %s %v..%v, group %+v:\n got %+v (cost %s)\nwant %+v (cost %s)", period, zone, from, to, k, *g, g.cost.FloatString(6), *w, w.cost.FloatString(6))
			}
		}
	}

	checkLimits := func(now time.Time) {
		t.Helper()
		from := now.Add(-time.Duration(rng.Int63n(int64(35 * 24 * time.Hour)))).Truncate(time.Microsecond)
		switch rng.Intn(3) {
		case 0:
			from = from.Truncate(time.Hour)
		case 1:
			from = from.Truncate(time.Minute)
		default: // inside one hour: the window starts and ends in the same hour
			from = now.Truncate(time.Hour).Add(time.Duration(rng.Intn(20)) * time.Minute)
		}
		to := now.Add(time.Minute)
		if rng.Intn(3) == 0 {
			to = from.Add(time.Duration(rng.Int63n(int64(10*time.Hour))) + time.Minute)
		}
		if rng.Intn(5) == 0 {
			to = from.Truncate(time.Hour).Add(time.Hour)
		}
		if !to.After(from) {
			return
		}
		live := from.Add(time.Duration(rng.Int63n(int64(to.Sub(from)) + 1)))
		if rng.Intn(3) == 0 {
			live = now.Add(-12 * time.Minute)
			if live.Before(from) {
				live = from
			}
		}
		if live.After(to) {
			live = to
		}
		var reader []queries.AggregateUsageForLimitsRow
		var ledger []queries.AggregateUsageForLimitsFromLedgerRow
		if err := worker.WithTenant(c, tid, func(c context.Context, tx store.Tx) error {
			q := queries.New(tx)
			var err error
			if reader, err = q.AggregateUsageForLimits(c, queries.AggregateUsageForLimitsParams{FromTime: from, LiveFrom: live, ToTime: to}); err != nil {
				return err
			}
			ledger, err = q.AggregateUsageForLimitsFromLedger(c, queries.AggregateUsageForLimitsFromLedgerParams{FromTime: from, LiveFrom: live, ToTime: to})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		norm := func(n int64, a, b, p, m, cur, req string, tokens, cost string) string {
			return fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%s", n, a, b, p, m, cur, req, ratOf(t, tokens).FloatString(6), ratOf(t, cost).FloatString(12))
		}
		var a, b []string
		for _, r := range reader {
			a = append(a, norm(r.Minute, r.PrincipalID, r.KeyID, r.ProviderID, r.ModelID, r.Currency, fmt.Sprint(r.Requests), r.Tokens, r.Cost))
		}
		for _, r := range ledger {
			b = append(b, norm(r.Minute, r.PrincipalID, r.KeyID, r.ProviderID, r.ModelID, r.Currency, fmt.Sprint(r.Requests), r.Tokens, r.Cost))
		}
		sort.Strings(a)
		sort.Strings(b)
		if strings.Join(a, "\n") != strings.Join(b, "\n") {
			t.Fatalf("AggregateUsageForLimits [%v, %v, %v) differs from the ledger reader (watermark %v):\n%s\n---\n%s", from, live, to, wm, strings.Join(a, "\n"), strings.Join(b, "\n"))
		}
	}

	for run := 0; run < 200; run++ {
		now := dbNow()
		var batch []rec
		for i, n := 0, rng.Intn(7); i < n; i++ {
			batch = append(batch, newRec(now))
		}
		insertRecords(t, d, tid, batch...)
		all = append(all, batch...)
		if rng.Intn(2) == 0 {
			if err := roll.Pass(c); err != nil {
				t.Fatalf("run %d: Pass: %v", run, err)
			}
			if err := d.SharedSuper(t).QueryRow(c, `SELECT watermark FROM pandokos.usage_rollup_state WHERE tenant_id = $1::uuid`, tid.String()).Scan(&wm); err != nil && run > 100 {
				t.Fatalf("run %d: the rollup has no state after the passes: %v", run, err)
			}
		}
		now = dbNow()
		for i := 0; i < 3; i++ {
			checkLimits(now)
		}
		checkReport(now)
		checkReport(now)
	}

	// The test proves nothing when the rollup stayed empty.
	var groups int
	if err := d.SharedSuper(t).QueryRow(c, `SELECT count(*) FROM pandokos.usage_rollup WHERE tenant_id = $1::uuid`, tid.String()).Scan(&groups); err != nil || groups == 0 {
		t.Errorf("the rollup holds %d rows after 200 runs (%v): the worker job did not run", groups, err)
	}
	if wm.IsZero() {
		t.Error("the watermark of the tenant was never set")
	}
}
