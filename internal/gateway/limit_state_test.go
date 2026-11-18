package gateway_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/test/limitstest"
)

// failingEnsurer is what loads the period counters, failing.
type failingEnsurer struct{}

func (failingEnsurer) Ensure(context.Context, tenant.ID, []limits.Limit) error {
	return errors.New("the ledger cannot be read")
}

// failingRules is a rule cache whose load always fails: the rules of the tenant cannot be read.
func failingRules(t *testing.T) *limits.RuleCache {
	t.Helper()
	cache, err := limits.NewRuleCacheFunc(func(context.Context, tenant.ID) (limits.RuleSet, error) {
		return limits.RuleSet{}, errors.New("the rules cannot be read")
	}, limits.RuleCacheOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

// TestLimitStateRecorded is T-708: each state of section 5.5 reaches the usage record and the
// field limit_state of the request line.
func TestLimitStateRecorded(t *testing.T) {
	type tc struct {
		name   string
		setup  func(t *testing.T) (*limitstest.Real, func(*harness)) // limits, and a change of the harness
		model  string
		sends  int // requests sent; the last one is the one that is checked
		status int
		state  string
	}
	count := func(m limits.Metric, n int64) limitstest.Options {
		return budgetRules(countRule("r-count", m, n))
	}
	unpriced := func(f limFam) func(*harness) {
		return func(h *harness) {
			tgt := h.targets[f.provider+"/fake-basic"]
			tgt.Prices = nil
			h.targets[f.provider+"/fake-basic"] = tgt
		}
	}
	for _, f := range limFamilies() {
		cases := []tc{
			{name: "no rule: unlimited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, limitstest.Options{}), nil
			}, model: "fake-basic", sends: 1, status: 200, state: "unlimited"},
			{name: "a rule that admits: limited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, count(limits.RequestsPerDay, 10)), nil
			}, model: "fake-basic", sends: 1, status: 200, state: "limited"},
			{name: "a rule of the platform alone: limited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, limitstest.Options{PlatformDefault: []limits.Rule{{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 100, Enforce: true}}}), nil
			}, model: "fake-basic", sends: 1, status: 200, state: "limited"},
			{name: "a 429: limited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, count(limits.RequestsPerDay, 1)), nil
			}, model: "fake-basic", sends: 2, status: 429, state: "limited"},
			{name: "a 409 no_price: limited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000))), unpriced(f)
			}, model: "fake-basic", sends: 1, status: 409, state: "limited"},
			{name: "unpriced and allowed: the budget was evaluated, limited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				o := budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000))
				o.OnUnknownPrice = "allow"
				return limitstest.NewWith(t, o), unpriced(f)
			}, model: "fake-basic", sends: 1, status: 200, state: "limited"},
			{name: "unpriced, allowed, with a count rule that applies: limited", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				o := budgetRules(costRule("r-budget", limits.CostPerDay, "USD", 100_000_000), countRule("r-count", limits.RequestsPerDay, 5))
				o.OnUnknownPrice = "allow"
				return limitstest.NewWith(t, o), unpriced(f)
			}, model: "fake-basic", sends: 1, status: 200, state: "limited"},
			{name: "counters unavailable, refuse: unverified", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				o := count(limits.RequestsPerDay, 10)
				o.Ensurer, o.OnStoreFailure = failingEnsurer{}, "refuse"
				return limitstest.NewWith(t, o), nil
			}, model: "fake-basic", sends: 1, status: 503, state: "unverified"},
			{name: "counters unavailable, allow: unverified, admitted", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				o := count(limits.RequestsPerDay, 10)
				o.Ensurer, o.OnStoreFailure = failingEnsurer{}, "allow"
				return limitstest.NewWith(t, o), nil
			}, model: "fake-basic", sends: 1, status: 200, state: "unverified"},
			{name: "rules unavailable: unverified", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, limitstest.Options{Cache: failingRules(t)}), nil
			}, model: "fake-basic", sends: 1, status: 503, state: "unverified"},
			{name: "rules unavailable, allow: still unverified and still refused", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, limitstest.Options{Cache: failingRules(t), OnStoreFailure: "allow"}), nil
			}, model: "fake-basic", sends: 1, status: 503, state: "unverified"},
			{name: "a request refused before step 7b: not evaluated", setup: func(t *testing.T) (*limitstest.Real, func(*harness)) {
				return limitstest.NewWith(t, count(limits.RequestsPerDay, 10)), nil
			}, model: "no-such-model", sends: 1, status: 403, state: "not_evaluated"},
		}
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				real, mutate := c.setup(t)
				h := f.newH(t, func(o *gateway.Options) { o.Limits = real })
				if mutate != nil {
					mutate(h)
				}
				var last captured
				for i := 0; i < c.sends; i++ {
					last = f.send(t, h, f.body(c.model, false))
				}
				if last.status != c.status {
					t.Fatalf("status %d, want %d\n%s", last.status, c.status, last.body)
				}
				recs := h.records(t, c.sends)
				if got := recs[len(recs)-1].LimitState; got != c.state {
					t.Errorf("usage record limit_state %q, want %q", got, c.state)
				}
				lines := h.requestLines(t, c.sends)
				if got := lines[len(lines)-1]["limit_state"]; got != c.state {
					t.Errorf("request line limit_state %v, want %q", got, c.state)
				}
			})
		}

		t.Run(f.name+"/a gateway without a limiter: not evaluated", func(t *testing.T) {
			h := f.newH(t, nil)
			if c := f.send(t, h, f.body("fake-basic", false)); c.status != 200 {
				t.Fatalf("%d %s", c.status, c.body)
			}
			if got := h.oneRecord(t).LimitState; got != "not_evaluated" {
				t.Errorf("limit_state %q", got)
			}
			if got := h.lastRequestLine(t)["limit_state"]; got != "not_evaluated" {
				t.Errorf("request line limit_state %v", got)
			}
		})
	}
}
