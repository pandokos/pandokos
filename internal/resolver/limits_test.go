package resolver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/resolver"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/limitstest"
)

// fakeStore is a limits.Store that records what the adapter asks of it. It admits unless refuse
// is set.
type fakeStore struct {
	mu              sync.Mutex
	reserves        []limits.Request
	settles         []limits.Amounts
	releases        int
	settleCtx       context.Context
	settleErrAtCall error // ctx.Err() as the store saw it
	refuse          *limits.Refusal
	settleErr       error
	releaseErr      error
	reserveErr      error // Reserve returns it
}

func (f *fakeStore) Reserve(_ context.Context, r limits.Request) (limits.Reservation, *limits.Refusal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserves = append(f.reserves, r)
	if f.reserveErr != nil {
		return limits.Reservation{}, nil, f.reserveErr
	}
	if f.refuse != nil {
		return limits.Reservation{}, f.refuse, nil
	}
	return limits.Reservation{ID: "res-1", Limits: r.Limits, Amounts: r.Estimate}, nil, nil
}

func (f *fakeStore) Settle(ctx context.Context, _ limits.Reservation, a limits.Amounts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settles = append(f.settles, a)
	f.settleCtx, f.settleErrAtCall = ctx, ctx.Err()
	return f.settleErr
}

func (f *fakeStore) Release(ctx context.Context, _ limits.Reservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	f.settleCtx, f.settleErrAtCall = ctx, ctx.Err()
	return f.releaseErr
}

type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func limitTenantID(n byte) tenant.ID {
	var id tenant.ID
	id[0], id[6], id[8], id[15] = 0xCD, 0x70, 0x80, n
	return id
}

// adapter builds the adapter over rules of one tenant and a fake store.
func adapter(t *testing.T, rules []limits.Rule, st *fakeStore, loadErr error) (*resolver.Limits, *logSink) {
	t.Helper()
	sink := &logSink{}
	log := slog.New(slog.NewJSONHandler(sink, nil))
	cache, err := limits.NewRuleCacheFunc(func(_ context.Context, tn tenant.ID) (limits.RuleSet, error) {
		if loadErr != nil {
			return limits.RuleSet{}, loadErr
		}
		return limits.NewRuleSet(tn, nil, nil, rules), nil
	}, limits.RuleCacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	l, err := resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: st, TTL: 7 * time.Minute, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	return l, sink
}

func limitPrincipal(id tenant.ID, sa, key, issuer string) authz.Principal {
	return authz.Principal{Kind: authz.KindServiceAccount, ID: sa, KeyID: key, IssuerID: issuer, TenantID: id,
		TenantState: tenant.StateActive, Roles: []string{authz.RoleInference}}
}

// TestLimitsAdapter is T-637.
func TestLimitsAdapter(t *testing.T) {
	a := limitTenantID(1)
	est := gateway.LimitEstimate{RequestID: "req-1", InputTokens: 100, OutputTokens: 400}

	t.Run("constructor", func(t *testing.T) {
		quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
		cache, _ := limits.NewRuleCacheFunc(func(context.Context, tenant.ID) (limits.RuleSet, error) { return limits.RuleSet{}, nil },
			limits.RuleCacheOptions{Logger: quiet, TTL: time.Minute})
		for name, o := range map[string]resolver.LimitsOptions{
			"no rules":  {Store: &fakeStore{}, Logger: quiet},
			"no store":  {Rules: cache, Logger: quiet},
			"no logger": {Rules: cache, Store: &fakeStore{}},
		} {
			if _, err := resolver.NewLimits(o); err == nil {
				t.Errorf("%s accepted", name)
			}
		}
	})

	t.Run("no applicable rule: no store call and a reservation that does nothing", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, nil, st, nil)
		res, ref, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est)
		if err != nil || ref != nil || res == nil {
			t.Fatalf("%v %v %v: want an admission with a reservation", res, ref, err)
		}
		res.Settle(gateway.LimitUsage{Tokens: 5})
		res2, _, err2 := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est)
		if err2 != nil || res2 == nil {
			t.Fatalf("second reserve: %v %v", res2, err2)
		}
		res2.Release()
		if len(st.reserves)+len(st.settles)+st.releases != 0 {
			t.Errorf("the store was called: %d reserves, %d settles, %d releases", len(st.reserves), len(st.settles), st.releases)
		}
	})

	t.Run("rules of other subjects do not apply", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, []limits.Rule{
			{SubjectKind: limits.SubjectServiceAccount, SubjectID: "sa-other", Metric: limits.RequestsPerMinute, Value: 1},
			{SubjectKind: limits.SubjectKey, SubjectID: "key-other", Metric: limits.RequestsPerMinute, Value: 1},
		}, st, nil)
		if _, ref, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est); err != nil || ref != nil {
			t.Fatalf("%v %v", ref, err)
		}
		if len(st.reserves) != 0 {
			t.Errorf("the store was called for a caller no rule names")
		}
	})

	t.Run("a key is limited by its service account and its key ID", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, []limits.Rule{
			{SubjectKind: limits.SubjectTenant, Metric: limits.TokensPerMinute, Value: 9000},
			{SubjectKind: limits.SubjectServiceAccount, SubjectID: "sa-1", Metric: limits.RequestsPerMinute, Value: 60},
			{SubjectKind: limits.SubjectKey, SubjectID: "key-1", Metric: limits.ConcurrentRequests, Value: 2},
		}, st, nil)
		res, ref, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est)
		if err != nil || ref != nil || res == nil {
			t.Fatalf("%v %v %v", res, ref, err)
		}
		if len(st.reserves) != 1 {
			t.Fatalf("%d store reserves", len(st.reserves))
		}
		r := st.reserves[0]
		if len(r.Limits) != 3 || r.Estimate.Tokens != 500 || r.TTL != 7*time.Minute || r.ID != "req-1" {
			t.Fatalf("request %+v", r)
		}
		for i, want := range []struct {
			kind limits.SubjectKind
			id   string
			m    limits.Metric
		}{{limits.SubjectTenant, "", limits.TokensPerMinute}, {limits.SubjectServiceAccount, "sa-1", limits.RequestsPerMinute}, {limits.SubjectKey, "key-1", limits.ConcurrentRequests}} {
			g := r.Limits[i]
			if g.Subject != want.kind || g.ID != want.id || g.Metric != want.m || g.Tenant != a {
				t.Errorf("limit %d: %+v, want %+v", i, g, want)
			}
		}
	})

	t.Run("a token principal is limited by the mapping ID in KeyID", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, []limits.Rule{
			{SubjectKind: limits.SubjectKey, SubjectID: "mapping-1", Metric: limits.RequestsPerMinute, Value: 3},
			{SubjectKind: limits.SubjectServiceAccount, SubjectID: "sa-1", Metric: limits.ConcurrentRequests, Value: 4},
		}, st, nil)
		tok := limitPrincipal(a, "sa-1", "mapping-1", "issuer-1")
		tok.Subject = "user-42"
		if _, _, err := l.Reserve(context.Background(), tok, est); err != nil {
			t.Fatal(err)
		}
		if len(st.reserves) != 1 || len(st.reserves[0].Limits) != 2 {
			t.Fatalf("reserves %+v", st.reserves)
		}
		if g, ok := findLimit(st.reserves[0].Limits, limits.SubjectKey, "mapping-1"); !ok || g.Max != 3 {
			t.Errorf("the mapping rule is missing: %+v", st.reserves[0].Limits)
		}
		if _, ok := findLimit(st.reserves[0].Limits, limits.SubjectServiceAccount, "sa-1"); !ok {
			t.Errorf("the service account rule is missing: %+v", st.reserves[0].Limits)
		}
	})

	t.Run("a refusal is passed on in plain values", func(t *testing.T) {
		st := &fakeStore{refuse: &limits.Refusal{
			Limit:      limits.Limit{Tenant: a, Subject: limits.SubjectServiceAccount, ID: "sa-1", Metric: limits.TokensPerMinute, Max: 1000, Level: limits.LevelTenant},
			Remaining:  40,
			RetryAfter: 17 * time.Second,
			Reset:      23 * time.Second,
			OverLimit:  true,
		}}
		l, _ := adapter(t, []limits.Rule{{SubjectKind: limits.SubjectServiceAccount, SubjectID: "sa-1", Metric: limits.TokensPerMinute, Value: 1000}}, st, nil)
		res, ref, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est)
		if err != nil || res != nil || ref == nil {
			t.Fatalf("%v %v %v: want a refusal", res, ref, err)
		}
		want := gateway.LimitRefusal{Code: "rate_limited", Scope: "all", Level: "tenant", Subject: "service_account", Metric: "tokens_per_minute", Limit: 1000, Remaining: 40,
			RetryAfter: 17 * time.Second, Reset: 23 * time.Second, OverLimit: true}
		if *ref != want {
			t.Errorf("refusal %+v, want %+v", *ref, want)
		}
	})

	t.Run("the cost of the estimate reaches the store", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, []limits.Rule{{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 5}}, st, nil)
		e := est
		e.Cost = pricing.Cost{Known: true, Currency: "USD", VersionID: "v1"}
		if _, _, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), e); err != nil {
			t.Fatal(err)
		}
		if got := st.reserves[0].Estimate.Cost; !got.Known || got.VersionID != "v1" {
			t.Errorf("cost %+v", got)
		}
	})

	t.Run("settle and release call the store with a context that outlives the request's", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, []limits.Rule{{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 5}}, st, nil)
		for _, end := range []string{"settle", "release"} {
			ctx, cancel := context.WithCancel(context.Background())
			res, _, err := l.Reserve(ctx, limitPrincipal(a, "sa-1", "key-1", ""), est)
			if err != nil || res == nil {
				t.Fatal(err)
			}
			cancel() // the caller went away
			if end == "settle" {
				res.Settle(gateway.LimitUsage{Tokens: 321})
			} else {
				res.Release()
			}
			if st.settleCtx == nil || st.settleErrAtCall != nil {
				t.Errorf("%s: the store got a cancelled context", end)
			}
		}
		if len(st.settles) != 1 || st.settles[0].Tokens != 321 || st.releases != 1 {
			t.Errorf("settles %+v releases %d", st.settles, st.releases)
		}
	})

	t.Run("ErrNotOutstanding is logged at error", func(t *testing.T) {
		for _, end := range []string{"settle", "release"} {
			st := &fakeStore{settleErr: limits.ErrNotOutstanding, releaseErr: limits.ErrNotOutstanding}
			l, sink := adapter(t, []limits.Rule{{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 5}}, st, nil)
			res, _, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est)
			if err != nil || res == nil {
				t.Fatalf("%s: reserve: %v %v", end, res, err)
			}
			if end == "settle" {
				res.Settle(gateway.LimitUsage{})
			} else {
				res.Release()
			}
			out := sink.String()
			if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "req-1") {
				t.Errorf("%s: log %q, want an error line with the request ID", end, out)
			}
		}
	})

	t.Run("rules that cannot be loaded are an error, not an admission", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := adapter(t, nil, st, errors.New("synthetic load failure"))
		res, ref, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est)
		if err == nil || res != nil || ref != nil {
			t.Fatalf("%v %v %v: want an error", res, ref, err)
		}
		if len(st.reserves) != 0 {
			t.Error("the store was called")
		}
	})

	t.Run("a store error is an error", func(t *testing.T) {
		st := &errStore{}
		sink := &logSink{}
		_ = sink
		cache, _ := limits.NewRuleCacheFunc(func(_ context.Context, tn tenant.ID) (limits.RuleSet, error) {
			return limits.NewRuleSet(tn, nil, nil, []limits.Rule{{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 5}}), nil
		}, limits.RuleCacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: time.Minute})
		l, err := resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: st, TTL: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		if res, ref, err := l.Reserve(context.Background(), limitPrincipal(a, "sa-1", "key-1", ""), est); err == nil || res != nil || ref != nil {
			t.Fatalf("%v %v %v: want an error", res, ref, err)
		}
	})
}

type errStore struct{}

func (errStore) Reserve(context.Context, limits.Request) (limits.Reservation, *limits.Refusal, error) {
	return limits.Reservation{}, nil, errors.New("synthetic store failure")
}
func (errStore) Settle(context.Context, limits.Reservation, limits.Amounts) error { return nil }
func (errStore) Release(context.Context, limits.Reservation) error                { return nil }

func findLimit(ls []limits.Limit, kind limits.SubjectKind, id string) (limits.Limit, bool) {
	for _, l := range ls {
		if l.Subject == kind && l.ID == id {
			return l, true
		}
	}
	return limits.Limit{}, false
}

// ---- sprint 5.2: period limits in the adapter --------------------------------------------------

type fakeEnsurer struct {
	mu    sync.Mutex
	calls []ensureCall
	err   error
}

type ensureCall struct {
	tenant tenant.ID
	ls     []limits.Limit
}

func (f *fakeEnsurer) Ensure(_ context.Context, t tenant.ID, ls []limits.Limit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ensureCall{t, append([]limits.Limit(nil), ls...)})
	return f.err
}

func (f *fakeEnsurer) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// notLoadedStore returns limits.ErrNotLoaded from the first failures reserves.
type notLoadedStore struct {
	*fakeStore
	failures int
	n        int
}

func (s *notLoadedStore) Reserve(ctx context.Context, r limits.Request) (limits.Reservation, *limits.Refusal, error) {
	s.mu.Lock()
	s.n++
	fail := s.n <= s.failures
	s.mu.Unlock()
	if fail {
		return limits.Reservation{}, nil, limits.ErrNotLoaded
	}
	return s.fakeStore.Reserve(ctx, r)
}

func (s *notLoadedStore) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func periodAdapter(t *testing.T, rules []limits.Rule, store limits.Store, ens resolver.Ensurer, unknownPrice, storeFailure string) (*resolver.Limits, *logSink) {
	t.Helper()
	sink := &logSink{}
	log := slog.New(slog.NewJSONHandler(sink, nil))
	cal, err := limits.NewCalendar(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := limits.NewRuleCacheFunc(func(_ context.Context, tn tenant.ID) (limits.RuleSet, error) {
		return limits.NewScopedRuleSet(tn, nil, nil, rules), nil
	}, limits.RuleCacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	l, err := resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: store, TTL: 7 * time.Minute, Logger: log,
		Calendar: cal, Reconciler: ens, OnUnknownPrice: unknownPrice, OnStoreFailure: storeFailure})
	if err != nil {
		t.Fatal(err)
	}
	return l, sink
}

func periodRule(id string, m limits.Metric, v int64, provider, model, currency string) limits.Rule {
	return limits.Rule{ID: id, SubjectKind: limits.SubjectTenant, Provider: provider, Model: model, Metric: m, Value: v, Currency: currency, Enforce: true}
}

// marks is an optional interface of a reservation the gateway reads for its two log fields.
type unverifiedMark interface{ Unverified() bool }
type unpricedMark interface{ Unpriced() bool }

// TestLimitsAdapterPeriods is T-664.
func TestLimitsAdapterPeriods(t *testing.T) {
	a := limitTenantID(2)
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	est := gateway.LimitEstimate{RequestID: "req-2", InputTokens: 100, OutputTokens: 400, Provider: "openai", Model: "gpt-4o", Start: start,
		Cost: pricing.Cost{Known: true, Amount: dec(t, "0.5"), Currency: "USD", VersionID: "v1"}}
	p := limitPrincipal(a, "sa-1", "key-1", "")
	bg := context.Background()

	t.Run("provider, model and start reach the rules; the period is that of the start", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := periodAdapter(t, []limits.Rule{
			periodRule("r-all", limits.RequestsPerDay, 100, "", "", ""),
			periodRule("r-prov", limits.TokensPerMonth, 1000, "openai", "", ""),
			periodRule("r-model", limits.RequestsPerMonth, 10, "openai", "gpt-4o", ""),
			periodRule("r-other", limits.RequestsPerDay, 1, "anthropic", "", ""),
			periodRule("r-model-other", limits.RequestsPerDay, 1, "openai", "gpt-3", ""),
		}, st, &fakeEnsurer{}, "", "")
		if _, ref, err := l.Reserve(bg, p, est); err != nil || ref != nil {
			t.Fatalf("%v %v", ref, err)
		}
		if len(st.reserves) != 1 {
			t.Fatalf("%d store reserves", len(st.reserves))
		}
		r := st.reserves[0]
		if !r.Start.Equal(start) {
			t.Errorf("start %v, want %v", r.Start, start)
		}
		var ids []string
		for _, l := range r.Limits {
			ids = append(ids, l.RuleID)
		}
		if want := []string{"r-all", "r-prov", "r-model"}; !reflect.DeepEqual(ids, want) {
			t.Fatalf("limits of rules %v, want %v", ids, want)
		}
		if got, want := r.Limits[0].Period.Start, time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Errorf("day period starts %v", got)
		}
		if got, want := r.Limits[1].Period.Start, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Errorf("month period starts %v", got)
		}
		if r.Estimate.Cost.Amount.String() != "0.5" {
			t.Errorf("estimate cost %+v", r.Estimate.Cost)
		}
	})

	t.Run("the counters are loaded only when a period limit applies", func(t *testing.T) {
		ens := &fakeEnsurer{}
		l, _ := periodAdapter(t, []limits.Rule{{ID: "r", SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 5, Enforce: true}}, &fakeStore{}, ens, "", "")
		if _, _, err := l.Reserve(bg, p, est); err != nil {
			t.Fatal(err)
		}
		if ens.n() != 0 {
			t.Errorf("%d loads for a rate limit", ens.n())
		}
		ens2 := &fakeEnsurer{}
		l, _ = periodAdapter(t, []limits.Rule{
			{ID: "r", SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 5, Enforce: true},
			periodRule("q", limits.RequestsPerDay, 5, "", "", ""),
		}, &fakeStore{}, ens2, "", "")
		if _, _, err := l.Reserve(bg, p, est); err != nil {
			t.Fatal(err)
		}
		if ens2.n() != 1 || ens2.calls[0].tenant != a {
			t.Fatalf("%d loads %+v", ens2.n(), ens2.calls)
		}
		found := false
		for _, l := range ens2.calls[0].ls {
			if l.RuleID == "q" {
				found = true
			}
		}
		if !found {
			t.Errorf("the period limit is not among those to load: %+v", ens2.calls[0].ls)
		}
		// A request that no rule matches loads nothing and calls nothing.
		ens3 := &fakeEnsurer{}
		st3 := &fakeStore{}
		l, _ = periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "anthropic", "", "")}, st3, ens3, "", "")
		if res, _, err := l.Reserve(bg, p, est); err != nil || res == nil || ens3.n() != 0 || len(st3.reserves) != 0 {
			t.Errorf("a rule of another provider: loads %d, reserves %d, %v", ens3.n(), len(st3.reserves), err)
		}
	})

	budget := periodRule("b", limits.CostPerMonth, 100_000_000, "", "", "EUR")

	t.Run("on_unknown_price refuse: a currency mismatch is unpriced", func(t *testing.T) {
		for name, e := range map[string]gateway.LimitEstimate{
			"another currency": est, // the estimate is in USD, the budget in EUR
			"no price": func() gateway.LimitEstimate {
				x := est
				x.Cost = pricing.Cost{Reason: pricing.ReasonNoPrice}
				return x
			}(),
		} {
			for _, setting := range []string{"", "refuse"} {
				st := &fakeStore{}
				l, _ := periodAdapter(t, []limits.Rule{budget}, st, &fakeEnsurer{}, setting, "")
				res, ref, err := l.Reserve(bg, p, e)
				if err != nil || res != nil || ref == nil {
					t.Fatalf("%s/%q: %v %v %v, want a refusal", name, setting, res, ref, err)
				}
				if ref.Code != "no_price" {
					t.Errorf("%s/%q: code %q", name, setting, ref.Code)
				}
				if len(st.reserves) != 0 {
					t.Errorf("%s/%q: the store was called", name, setting)
				}
			}
		}
	})

	t.Run("on_unknown_price refuse: an unenforced budget never refuses; it is dropped and marked", func(t *testing.T) {
		soft := periodRule("soft", limits.CostPerMonth, 100_000_000, "", "", "EUR")
		soft.Enforce = false
		soft.Thresholds = []int{80}
		for _, setting := range []string{"", "refuse"} {
			st := &fakeStore{}
			l, _ := periodAdapter(t, []limits.Rule{soft, periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{}, setting, "")
			res, ref, err := l.Reserve(bg, p, est) // the estimate is in USD, the budget in EUR
			if err != nil || ref != nil || res == nil {
				t.Fatalf("%q: %v %v %v, want an admission: enforce false never refuses", setting, res, ref, err)
			}
			if len(st.reserves) != 1 || len(st.reserves[0].Limits) != 1 || st.reserves[0].Limits[0].RuleID != "q" {
				t.Errorf("%q: reserves %+v, want the request quota only", setting, st.reserves)
			}
			if m, ok := res.(unpricedMark); !ok || !m.Unpriced() {
				t.Errorf("%q: the reservation does not say the budget was left out", setting)
			}
		}
		// An enforced budget beside it still refuses.
		l, _ := periodAdapter(t, []limits.Rule{soft, budget}, &fakeStore{}, &fakeEnsurer{}, "refuse", "")
		if _, ref, err := l.Reserve(bg, p, est); err != nil || ref == nil || ref.Code != "no_price" {
			t.Errorf("an enforced budget: %v %v, want no_price", ref, err)
		}
	})

	t.Run("on_unknown_price refuse: a price in the currency of the budget is measured", func(t *testing.T) {
		st := &fakeStore{}
		usd := periodRule("b", limits.CostPerMonth, 100_000_000, "", "", "USD")
		l, _ := periodAdapter(t, []limits.Rule{usd}, st, &fakeEnsurer{}, "refuse", "")
		if _, ref, err := l.Reserve(bg, p, est); err != nil || ref != nil || len(st.reserves) != 1 {
			t.Fatalf("%v %v, %d reserves", ref, err, len(st.reserves))
		}
	})

	t.Run("on_unknown_price refuse: a request no budget applies to needs no price", func(t *testing.T) {
		st := &fakeStore{}
		e := est
		e.Cost = pricing.Cost{Reason: pricing.ReasonNoPrice}
		l, _ := periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{}, "refuse", "")
		if _, ref, err := l.Reserve(bg, p, e); err != nil || ref != nil || len(st.reserves) != 1 {
			t.Fatalf("%v %v, %d reserves", ref, err, len(st.reserves))
		}
	})

	t.Run("on_unknown_price allow: admitted against every other limit, the budget not counted", func(t *testing.T) {
		st := &fakeStore{}
		l, _ := periodAdapter(t, []limits.Rule{budget, periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{}, "allow", "")
		res, ref, err := l.Reserve(bg, p, est)
		if err != nil || ref != nil || res == nil {
			t.Fatalf("%v %v %v", res, ref, err)
		}
		if len(st.reserves) != 1 || len(st.reserves[0].Limits) != 1 || st.reserves[0].Limits[0].RuleID != "q" {
			t.Fatalf("reserves %+v, want the request quota only", st.reserves)
		}
		if m, ok := res.(unpricedMark); !ok || !m.Unpriced() {
			t.Errorf("the reservation does not say it was admitted without a price: %#v", res)
		}
	})

	t.Run("the refusal carries the code, the scope and the currency", func(t *testing.T) {
		for _, c := range []struct {
			limit limits.Limit
			want  gateway.LimitRefusal
		}{
			{limits.Limit{Tenant: a, Subject: limits.SubjectTenant, Metric: limits.CostPerMonth, Currency: "EUR", Provider: "openai", Model: "gpt-4o", Max: 250_500_000, Level: limits.LevelTenant},
				gateway.LimitRefusal{Code: "quota_exceeded", Scope: "model", Currency: "EUR", Level: "tenant", Subject: "tenant", Metric: "cost_per_month", Limit: 250_500_000}},
			{limits.Limit{Tenant: a, Subject: limits.SubjectKey, ID: "key-1", Metric: limits.TokensPerDay, Provider: "openai", Max: 5000, Level: limits.LevelPlatform},
				gateway.LimitRefusal{Code: "quota_exceeded", Scope: "provider", Level: "platform", Subject: "key", Metric: "tokens_per_day", Limit: 5000}},
			{limits.Limit{Tenant: a, Subject: limits.SubjectTenant, Metric: limits.RequestsPerMonth, Max: 7, Level: limits.LevelTenant},
				gateway.LimitRefusal{Code: "quota_exceeded", Scope: "all", Level: "tenant", Subject: "tenant", Metric: "requests_per_month", Limit: 7}},
			{limits.Limit{Tenant: a, Subject: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Provider: "openai", Model: "gpt-4o", Max: 3, Level: limits.LevelTenant},
				gateway.LimitRefusal{Code: "rate_limited", Scope: "model", Level: "tenant", Subject: "tenant", Metric: "requests_per_minute", Limit: 3}},
		} {
			st := &fakeStore{refuse: &limits.Refusal{Limit: c.limit, Remaining: 0, RetryAfter: 90 * time.Second, Reset: 90 * time.Second}}
			l, _ := periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{}, "", "")
			_, ref, err := l.Reserve(bg, p, est)
			if err != nil || ref == nil {
				t.Fatalf("%v %v", ref, err)
			}
			want := c.want
			want.RetryAfter, want.Reset = 90*time.Second, 90*time.Second
			if *ref != want {
				t.Errorf("refusal %+v, want %+v", *ref, want)
			}
		}
	})

	t.Run("on_store_failure refuse: a failed load is an error", func(t *testing.T) {
		for _, setting := range []string{"", "refuse"} {
			st := &fakeStore{}
			l, _ := periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{err: errors.New("synthetic ledger failure")}, "", setting)
			res, ref, err := l.Reserve(bg, p, est)
			if err == nil || res != nil || ref != nil {
				t.Errorf("%q: %v %v %v, want an error", setting, res, ref, err)
			}
			if len(st.reserves) != 0 {
				t.Errorf("%q: the store was called", setting)
			}
		}
	})

	t.Run("a counter dropped between ensure and reserve is loaded again once", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			failures int
			setting  string
			wantErr  bool
			wantUnv  bool
		}{
			{"one drop", 1, "refuse", false, false},
			{"two drops, refuse", 2, "refuse", true, false},
			{"two drops, allow", 2, "allow", false, true},
		} {
			st := &notLoadedStore{fakeStore: &fakeStore{}, failures: c.failures}
			ens := &fakeEnsurer{}
			l, _ := periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, ens, "", c.setting)
			res, ref, err := l.Reserve(bg, p, est)
			if (err != nil) != c.wantErr || ref != nil || (err == nil && res == nil) {
				t.Fatalf("%s: %v %v %v", c.name, res, ref, err)
			}
			if ens.n() != 2 {
				t.Errorf("%s: %d loads, want 2 (one more after ErrNotLoaded, and no more)", c.name, ens.n())
			}
			if st.attempts() != min(c.failures+1, 2) {
				t.Errorf("%s: %d reserves, want %d", c.name, st.attempts(), min(c.failures+1, 2))
			}
			if err == nil {
				if m, ok := res.(unverifiedMark); c.wantUnv && (!ok || !m.Unverified()) || !c.wantUnv && ok && m.Unverified() {
					t.Errorf("%s: unverified mark wrong: %#v", c.name, res)
				}
			}
		}
	})

	t.Run("on_store_failure refuse: a failing Reserve is an error", func(t *testing.T) {
		st := &fakeStore{reserveErr: errors.New("synthetic store failure")}
		l, _ := periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{}, "refuse", "refuse")
		if res, ref, err := l.Reserve(bg, p, est); err == nil || res != nil || ref != nil {
			t.Errorf("%v %v %v, want an error", res, ref, err)
		}
	})

	t.Run("on_store_failure allow: a failing Reserve admits without a reservation", func(t *testing.T) {
		st := &fakeStore{reserveErr: errors.New("synthetic store failure")}
		l, _ := periodAdapter(t, []limits.Rule{periodRule("q", limits.RequestsPerDay, 5, "", "", "")}, st, &fakeEnsurer{}, "", "allow")
		res, ref, err := l.Reserve(bg, p, est)
		if err != nil || ref != nil || res == nil {
			t.Fatalf("%v %v %v: want an admission", res, ref, err)
		}
		if m, ok := res.(unverifiedMark); !ok || !m.Unverified() {
			t.Errorf("the reservation does not say it is unverified: %#v", res)
		}
		res.Settle(gateway.LimitUsage{Tokens: 5})
		res.Release()
		if len(st.settles)+st.releases != 0 {
			t.Errorf("a reservation that was never taken was settled or released")
		}
	})

	t.Run("on_store_failure allow: after a failed load the loaded counters still apply", func(t *testing.T) {
		ens := &fakeEnsurer{err: errors.New("synthetic ledger failure")}
		sink := &logSink{}
		rules := []limits.Rule{
			{ID: "rate", SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 1, Enforce: true},
			periodRule("month", limits.RequestsPerMonth, 100, "", "", ""),
			periodRule("day", limits.RequestsPerDay, 100, "", "", ""),
		}
		real := limitstest.NewWith(t, limitstest.Options{Rules: map[tenant.ID][]limits.Rule{a: rules}, Ensurer: ens, OnStoreFailure: "allow", Logs: sink})
		now := real.Now()
		est := est
		est.Start = now
		// The cut is the start of today: today's counters need no load; the month started before it.
		real.Mem.SetCut(a, real.Calendar.Of(limits.PerDay, now).Start)
		res, ref, err := real.Reserve(bg, p, est)
		if err != nil || ref != nil || res == nil {
			t.Fatalf("%v %v %v: want an admission", res, ref, err)
		}
		if m, ok := res.(unverifiedMark); !ok || !m.Unverified() {
			t.Errorf("the reservation does not say it is unverified")
		}
		ls := limits.NewScopedRuleSet(a, nil, nil, rules).For(limits.Query{ServiceAccountID: "sa-1", KeyID: "key-1", Provider: "openai", Model: "gpt-4o", Start: now}, real.Calendar)
		for _, l := range ls {
			c, ok := real.Mem.Counted(l)
			switch l.RuleID {
			case "day":
				if !ok || c.N != 1 {
					t.Errorf("the day counter %v %v: it exists and must count the request", c, ok)
				}
			case "month":
				if ok {
					t.Errorf("the month counter exists although it could not be loaded")
				}
			}
		}
		// The rate limit still refuses the second request.
		_, ref, err = real.Reserve(bg, p, est)
		if err != nil || ref == nil || ref.Code != "rate_limited" {
			t.Errorf("second reserve: %v %v, want the rate limit", ref, err)
		}
		if out := sink.String(); strings.Count(out, "limit counters unavailable") != 1 || !strings.Contains(out, `"level":"WARN"`) {
			t.Errorf("log %q, want one warning in two requests of a tenant", out)
		}
	})

	t.Run("rules that cannot be loaded are an error with either setting", func(t *testing.T) {
		for _, setting := range []string{"refuse", "allow"} {
			st := &fakeStore{}
			cache, _ := limits.NewRuleCacheFunc(func(context.Context, tenant.ID) (limits.RuleSet, error) {
				return limits.RuleSet{}, errors.New("synthetic load failure")
			}, limits.RuleCacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: time.Minute})
			cal, _ := limits.NewCalendar(time.UTC)
			l, err := resolver.NewLimits(resolver.LimitsOptions{Rules: cache, Store: st, TTL: time.Minute, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Calendar: cal, Reconciler: &fakeEnsurer{}, OnStoreFailure: setting})
			if err != nil {
				t.Fatal(err)
			}
			if res, ref, err := l.Reserve(bg, p, est); err == nil || res != nil || ref != nil {
				t.Errorf("%s: %v %v %v, want an error", setting, res, ref, err)
			}
		}
	})
}

func dec(t *testing.T, s string) pricing.Decimal {
	t.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
