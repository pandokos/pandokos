package gateway_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/tenant"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// loadClient is an HTTP client that keeps many connections to the gateway.
func loadClient() *http.Client {
	tr := &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}
	return &http.Client{Transport: tr, Timeout: 8 * time.Second}
}

// fire sends n requests with the given number of workers and returns the statuses; do builds and
// sends request i and returns its status (0 when the caller gave up).
func fire(n, workers int, do func(i int) int) []int {
	statuses := make([]int, n)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				statuses[i] = do(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return statuses
}

func postChat(h *harness, c *http.Client, body []byte) int {
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+route, bytesReader(body))
	if err != nil {
		return -1
	}
	req.Header = authed()
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// TestConcurrencyLimit is T-638: with the real store and a limit of 3, three held streams fill
// it, the fourth is refused, and the next is admitted when one ends, however it ends.
func TestConcurrencyLimit(t *testing.T) {
	t.Parallel()
	rule := limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.ConcurrentRequests, Value: 3}

	// hold starts n gated streams and waits until each has delivered its first event.
	hold := func(t *testing.T, h *harness, n int) (cancels []context.CancelFunc, done chan struct{}) {
		t.Helper()
		done = make(chan struct{}, n)
		var started atomic.Int64
		for i := 0; i < n; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			cancels = append(cancels, cancel)
			t.Cleanup(cancel)
			go func() {
				defer func() { done <- struct{}{} }()
				resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
				if err != nil {
					return
				}
				defer resp.Body.Close()
				if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err == nil {
					started.Add(1)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
			}()
		}
		testutil.Eventually(t, 3*time.Second, "the streams to be held", func() bool { return started.Load() == int64(n) })
		return cancels, done
	}
	refused := func(t *testing.T, h *harness) {
		t.Helper()
		c := h.sendAuthed(t, route, chat("fake-basic", false))
		assertContract(t, "the fourth", c, family.CodeRateLimited)
		if c.header.Get("X-Pandokos-Limit-Metric") != "concurrent_requests" || c.header.Get("Retry-After") != "1" || c.header.Get("X-Pandokos-Limit") != "3" {
			t.Errorf("headers %v", c.header)
		}
		if c.header.Get("X-Pandokos-Limit-Reset") != "" {
			t.Errorf("a reset for concurrency: %q", c.header.Get("X-Pandokos-Limit-Reset"))
		}
	}
	admitted := func(t *testing.T, h *harness) {
		t.Helper()
		testutil.Eventually(t, 3*time.Second, "the next request to be admitted", func() bool {
			return h.sendAuthed(t, route, chat("fake-basic", false)).status == 200
		})
	}

	t.Run("a stream completes", func(t *testing.T) {
		real := limitstest.New(t, testPrincipal, rule)
		h := newHarness(t, func(o *gateway.Options) { o.Limits = real })
		_, done := hold(t, h, 3)
		refused(t, h)
		h.up.Release()
		for i := 0; i < 3; i++ {
			<-done
		}
		admitted(t, h)
		testutil.Eventually(t, 3*time.Second, "every reservation to end", func() bool { return real.Outstanding() == 0 })
	})

	t.Run("a client disconnects", func(t *testing.T) {
		real := limitstest.New(t, testPrincipal, rule)
		h := newHarness(t, func(o *gateway.Options) { o.Limits = real })
		cancels, done := hold(t, h, 3)
		refused(t, h)
		cancels[0]()
		<-done
		admitted(t, h)
		if n := real.Outstanding(); n < 2 {
			t.Errorf("%d reservations outstanding while two streams are held", n)
		}
		h.up.Release()
		for i := 0; i < 2; i++ {
			<-done
		}
	})

	t.Run("the upstream fails", func(t *testing.T) {
		real := limitstest.New(t, testPrincipal, rule)
		h := newHarness(t, func(o *gateway.Options) { o.Limits = real; o.MaxRequestDuration = 1500 * time.Millisecond })
		_, done := hold(t, h, 3)
		refused(t, h)
		for i := 0; i < 3; i++ { // the duration limit ends the held streams
			<-done
		}
		admitted(t, h)
		testutil.Eventually(t, 3*time.Second, "every reservation to end", func() bool { return real.Outstanding() == 0 })
	})
}

// TestParallelLoad is T-640: the exit condition of the sprint, with the real store and 64 workers.
func TestParallelLoad(t *testing.T) {
	// Serial on purpose: it measures a load or a short real-time window, which parallel tests disturb.
	start := time.Now()
	defer func() {
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("the load test took %s, the bound is 10 s", d)
		}
	}()
	const workers = 64

	t.Run("a: exactly the limit is forwarded", func(t *testing.T) {
		const total, limit = 2000, 137
		real := limitstest.New(t, testPrincipal, limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: limit})
		h := newHarness(t, func(o *gateway.Options) { o.Limits = real })
		c := loadClient()
		statuses := fire(total, workers, func(int) int { return postChat(h, c, chat("fake-basic", false)) })
		var ok, refused, other int
		for _, s := range statuses {
			switch s {
			case 200:
				ok++
			case 429:
				refused++
			default:
				other++
			}
		}
		if ok != limit || refused != total-limit || other != 0 {
			t.Fatalf("%d answered 200, %d answered 429, %d other; want %d, %d and 0", ok, refused, other, limit, total-limit)
		}
		if n := len(h.up.Requests()); n != limit {
			t.Errorf("%d requests forwarded, want %d", n, limit)
		}
		recs := h.records(t, total)
		var okRec, limRec int
		for _, r := range recs {
			switch r.Outcome {
			case usage.OutcomeOK:
				okRec++
			case usage.OutcomeRejectedLimit:
				limRec++
			}
		}
		if len(recs) != total || okRec != limit || limRec != total-limit {
			t.Errorf("%d records: %d ok and %d rejected_limit", len(recs), okRec, limRec)
		}
	})

	t.Run("b: tokens stay within the bound", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			d    int64 // tokens above every reservation
		}{{"d is 0", 0}, {"d is 40", 40}} {
			t.Run(tc.name, func(t *testing.T) {
				const k, outputs = 5, 100
				body := chat("fake-basic", false)
				reserved := ceil4(len(body)) + outputs
				limit := 20 * reserved
				real := limitstest.New(t, testPrincipal,
					limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.TokensPerMinute, Value: limit},
					limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.ConcurrentRequests, Value: k})
				h := newHarness(t, func(o *gateway.Options) { o.Limits = real; o.DefaultOutputTokens = outputs })
				h.up.ReportTokens(reserved + tc.d)
				c := loadClient()
				fire(400, workers, func(int) int { return postChat(h, c, body) })
				recs := h.records(t, 400)
				var tokens int64
				var ok, refused int
				for _, r := range recs {
					switch r.Outcome {
					case usage.OutcomeOK:
						ok++
						tokens += r.Usage.InputTokens + r.Usage.OutputTokens
					case usage.OutcomeRejectedLimit:
						refused++
					}
				}
				bound := limit + k*tc.d
				if tokens > bound {
					t.Errorf("%d tokens of forwarded requests, the bound is %d (limit %d, k %d, d %d)", tokens, bound, limit, k, tc.d)
				}
				if tc.d == 0 && tokens > limit {
					t.Errorf("%d tokens with no excess, the limit is %d", tokens, limit)
				}
				if ok == 0 || refused == 0 {
					t.Errorf("%d ok and %d refused: the test proves nothing", ok, refused)
				}
			})
		}
	})

	t.Run("c: nothing stays reserved", func(t *testing.T) {
		const total = 300
		real := limitstest.New(t, testPrincipal,
			limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.ConcurrentRequests, Value: 1000},
			limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 1000000},
			limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.TokensPerMinute, Value: 1000000000})
		var opens atomic.Int64
		h := newHarness(t, func(o *gateway.Options) {
			o.Limits = real
			o.Credentials = &sometimesNoCredential{inner: o.Credentials, n: &opens}
		})
		c := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 128}}
		givesUp := func(i int) bool { return i%3 == 0 }
		patient := 0 // the requests that have no short timeout: they always reach the handler
		for i := 0; i < total; i++ {
			if !givesUp(i) {
				patient++
			}
		}
		fire(total, workers, func(i int) int {
			switch {
			case givesUp(i): // the caller gives up
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.URL+route, bytesReader(chat(fakeprovider.ModelHang, false)))
				req.Header = authed()
				if resp, err := c.Do(req); err == nil {
					resp.Body.Close()
				}
				return 0
			case i%3 == 1: // the provider fails
				return postChat(h, c, chat(fakeprovider.ModelError500Echo, false))
			}
			return postChat(h, c, chat("fake-basic", false))
		})
		h.srv.Close() // waits for every handler to return
		if n := real.Outstanding(); n != 0 {
			t.Fatalf("%d reservations outstanding after the last handler returned", n)
		}
		// A caller that gives up after 40 ms may do so before its connection is set up, and then it
		// never reaches the handler: on a loaded machine that can be any number of them, up to all.
		// Every request the handler saw has a record, the others none; the requests that do not give
		// up always reach it (#100).
		seen := len(h.logs.Find(t, "request"))
		if got := len(h.rec.Records()); got != seen || seen > total || seen < patient {
			t.Errorf("%d records for %d requests that reached the handler (%d sent, at least %d expected)", got, seen, total, patient)
		}
		real.Advance(61 * time.Second)
		real.Sweep()
		if n := real.Counters(); n != 0 {
			t.Errorf("%d counters held 61 s later", n)
		}
	})
}

// sometimesNoCredential answers ErrNoCredential to every seventh request.
type sometimesNoCredential struct {
	inner gateway.Credentials
	n     *atomic.Int64
}

func (s *sometimesNoCredential) Open(ctx context.Context, p authz.Principal, requestID, provider string) (gateway.Credential, error) {
	if s.n.Add(1)%7 == 0 {
		return gateway.Credential{}, gateway.ErrNoCredential
	}
	return s.inner.Open(ctx, p, requestID, provider)
}

// inflightTracker knows the oldest request that has been reserved and has not yet handed its usage
// record to the recorder: what the ledger writer's OldestPendingStart gives the reconciler.
type inflightTracker struct {
	mu    sync.Mutex
	start map[string]time.Time
}

func newInflightTracker() *inflightTracker { return &inflightTracker{start: map[string]time.Time{}} }

func (t *inflightTracker) oldest() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var o time.Time
	for _, s := range t.start {
		if o.IsZero() || s.Before(o) {
			o = s
		}
	}
	return o, !o.IsZero()
}

type trackedLimits struct {
	inner gateway.Limits
	t     *inflightTracker
	shift func() time.Duration
}

func (l trackedLimits) Reserve(ctx context.Context, p authz.Principal, e gateway.LimitEstimate) (gateway.Reservation, *gateway.LimitRefusal, gateway.LimitState, error) {
	l.t.mu.Lock()
	l.t.start[e.RequestID] = e.Start.Add(l.shift()) // before the reserve: the cut cannot pass the request
	l.t.mu.Unlock()
	return l.inner.Reserve(ctx, p, e)
}

type trackedRecorder struct {
	inner *testutil.MemoryRecorder
	t     *inflightTracker
}

func (r trackedRecorder) Admit() bool { return r.inner.Admit() }

func (r trackedRecorder) Record(rec usage.Record) {
	r.inner.Record(rec)
	r.t.mu.Lock()
	delete(r.t.start, rec.RequestID)
	r.t.mu.Unlock()
}

// TestParallelBudget is T-671: the bound of the budget with 64 workers, a concurrency limit k and a
// provider that reports d above every reservation, with and without a fold running every 50 ms.
func TestParallelBudget(t *testing.T) {
	t.Parallel()
	const workers, k, outputs = 64, 5, 100
	body := chat("fake-basic", false)
	reservedTokens := ceil4(len(body)) + outputs

	run := func(t *testing.T, metric limits.Metric, limit, d int64, fold bool) (spentTokens int64, spentCost pricing.Decimal, ok, refused int) {
		t.Helper()
		rec := &testutil.MemoryRecorder{}
		tr := newInflightTracker()
		rules := []limits.Rule{
			{ID: "r-main", SubjectKind: limits.SubjectTenant, Metric: metric, Value: limit, Enforce: true},
			{ID: "r-conc", SubjectKind: limits.SubjectTenant, Metric: limits.ConcurrentRequests, Value: k, Enforce: true},
		}
		if metric.Cost() {
			rules[0].Currency = "USD"
		}
		opts := limitstest.Options{Rules: map[tenant.ID][]limits.Rule{testPrincipal.TenantID: rules}, Pending: tr.oldest,
			Start: time.Date(2026, 11, 17, 11, 59, 59, 0, time.UTC), Horizon: time.Second, Interval: 50 * time.Millisecond}
		var real *limitstest.Real
		opts.Ledger = recordLedger{rec: rec, shift: func() time.Duration { return real.Shift() }}
		real = limitstest.NewWith(t, opts)
		h := newHarness(t, func(o *gateway.Options) {
			o.Limits = trackedLimits{inner: real, t: tr, shift: real.Shift}
			o.Recorder = trackedRecorder{inner: rec, t: tr}
			o.DefaultOutputTokens = outputs
		})
		h.rec = rec
		h.up.ReportTokens(reservedTokens + d)
		stop := make(chan struct{})
		var folds sync.WaitGroup
		if fold {
			folds.Add(1)
			go func() {
				defer folds.Done()
				for {
					select {
					case <-stop:
						return
					case <-time.After(50 * time.Millisecond):
						real.Tick(context.Background())
					}
				}
			}()
		}
		c := loadClient()
		fire(400, workers, func(int) int { return postChat(h, c, body) })
		close(stop)
		folds.Wait()
		for _, r := range h.records(t, 400) {
			switch r.Outcome {
			case usage.OutcomeOK:
				ok++
				spentTokens += r.Usage.InputTokens + r.Usage.OutputTokens
				if r.Cost.Known {
					spentCost = spentCost.Add(r.Cost.Amount)
				}
			case usage.OutcomeRejectedLimit:
				refused++
			}
		}
		return
	}

	for _, fold := range []bool{false, true} {
		name := "no fold"
		if fold {
			name = "a fold every 50 ms"
		}
		for _, d := range []int64{0, 40} {
			t.Run(fmt.Sprintf("tokens, d %d, %s", d, name), func(t *testing.T) {
				limit := 20 * reservedTokens
				tokens, _, ok, refused := run(t, limits.TokensPerMonth, limit, d, fold)
				if bound := limit + k*d; tokens > bound {
					t.Errorf("%d tokens spent, the bound is %d (limit %d, k %d, d %d)", tokens, bound, limit, k, d)
				}
				if d == 0 && tokens > limit {
					t.Errorf("%d tokens spent with no excess, the limit is %d", tokens, limit)
				}
				if ok == 0 || refused == 0 {
					t.Errorf("%d ok and %d refused: the test proves nothing", ok, refused)
				}
			})
		}
		t.Run("cost, d 40, "+name, func(t *testing.T) {
			// What a request is reserved: input at 3 and output at 6 millionths of a dollar a token. What the
			// provider reports: one input token and the rest as output.
			c := ceil4(len(body))
			estMicro := c*3 + outputs*6
			actualMicro := int64(3) + (reservedTokens+40-1)*6
			excess := max(0, actualMicro-estMicro)
			limit := 20 * estMicro
			_, cost, ok, refused := run(t, limits.CostPerMonth, limit, 40, fold)
			bound := dec(t, fmt.Sprint(limit+k*excess)).DivPow10(6)
			if cost.Cmp(bound) > 0 {
				t.Errorf("%s spent, the bound is %s (limit %d, k %d, excess %d millionths)", cost, bound, limit, k, excess)
			}
			if ok == 0 || refused == 0 {
				t.Errorf("%d ok and %d refused: the test proves nothing", ok, refused)
			}
		})
	}
}

func dec(t *testing.T, s string) pricing.Decimal {
	t.Helper()
	d, err := pricing.ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
