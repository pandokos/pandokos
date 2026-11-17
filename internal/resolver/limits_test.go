package resolver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
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
}

func (f *fakeStore) Reserve(_ context.Context, r limits.Request) (limits.Reservation, *limits.Refusal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserves = append(f.reserves, r)
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
		want := gateway.LimitRefusal{Level: "tenant", Subject: "service_account", Metric: "tokens_per_minute", Limit: 1000, Remaining: 40,
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
