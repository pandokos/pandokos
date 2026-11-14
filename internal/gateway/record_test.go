package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// opSpec is one operation of the openai family as the callers see it.
type opSpec struct {
	name, path string
	streams    bool
}

var (
	opChat       = opSpec{"chat.completions", "/openai/v1/chat/completions", true}
	opResponses  = opSpec{"responses", "/openai/v1/responses", true}
	opEmbeddings = opSpec{"embeddings", "/openai/v1/embeddings", false}
	allOps       = []opSpec{opChat, opResponses, opEmbeddings}
)

// body is a request of the operation: its prompt is a canary.
func (o opSpec) body(model string, stream bool) []byte {
	if o.name == opChat.name {
		return chat(model, stream)
	}
	s := `{"model":"` + model + `","input":"` + fixtures.CanaryPrompt + `"`
	if stream {
		s += `,"stream":true`
	}
	return []byte(s + "}")
}

func (h *harness) sendOp(t *testing.T, o opSpec, model string, stream bool) captured {
	t.Helper()
	return h.sendAuthed(t, o.path, o.body(model, stream))
}

var (
	reUUIDv7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	reTrace  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// settle waits until the recorder holds n records, lets a moment pass and checks that no more
// came: a record of an aborted handler arrives after the client saw the connection close.
func (h *harness) settle(t *testing.T, n int) []usage.Record {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, fmt.Sprintf("%d usage records", n), func() bool { return len(h.rec.Records()) >= n })
	time.Sleep(50 * time.Millisecond)
	recs := h.rec.Records()
	if len(recs) != n {
		t.Fatalf("%d records, want %d", len(recs), n)
	}
	return recs
}

// principalWith returns the test principal after mutate.
func principalWith(mutate func(*authz.Principal)) authz.Principal {
	p := testPrincipal
	mutate(&p)
	return p
}

// bodyPanicTransport answers 200 with a JSON body whose second read panics: a panic after the
// first write.
type bodyPanicTransport struct{}

func (bodyPanicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   &panicOnSecondRead{},
	}, nil
}

type panicOnSecondRead struct{ n int }

func (p *panicOnSecondRead) Read(b []byte) (int, error) {
	p.n++
	if p.n > 1 {
		panic("synthetic panic after the first write")
	}
	return copy(b, `{"id":"x",`), nil
}
func (p *panicOnSecondRead) Close() error { return nil }

// endCase is one way a request ends and what the ledger must say about it.
type endCase struct {
	name    string
	mutate  func(*gateway.Options)
	prepare func(h *harness)
	do      func(t *testing.T, h *harness)
	outcome usage.Outcome
	code    string
	status  int // the status of the provider, when it answered
}

// TestOneRecordPerOutcome is T-484: every way a request can end after authentication has exactly
// one Admit and one Record, with the outcome and code of the table of section 5.5; requests that
// are not authenticated have neither.
func TestOneRecordPerOutcome(t *testing.T) {
	post := func(o opSpec, model string, stream bool) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) { h.sendOp(t, o, model, stream) }
	}
	chatPost := post(opChat, "fake-basic", false)
	auth := func(p authz.Principal) func(*gateway.Options) {
		return func(o *gateway.Options) { o.Authenticator = fixedAuth{p: p} }
	}
	direct := func(body io.Reader, contentLength int64, ctx context.Context) func(t *testing.T, h *harness) {
		return func(_ *testing.T, h *harness) {
			req := httptest.NewRequest(http.MethodPost, route, body).WithContext(ctx)
			req.ContentLength = contentLength
			req.Header = authed()
			func() {
				defer func() { _ = recover() }()
				h.handler.ServeHTTP(httptest.NewRecorder(), req)
			}()
		}
	}
	retarget := func(mutate func(*gateway.Target)) func(h *harness) {
		return func(h *harness) {
			tgt := h.targets["openai/fake-basic"]
			mutate(&tgt)
			h.targets["openai/fake-basic"] = tgt
		}
	}
	short := func(o *gateway.Options) { o.MaxRequestDuration = 300 * time.Millisecond }
	rejected, internal := usage.OutcomeRejectedPolicy, usage.OutcomeInternalError

	cases := []endCase{
		// Step 4.
		{name: "step 4: a suspended tenant", mutate: auth(principalWith(func(p *authz.Principal) { p.TenantState = "suspended" })), do: chatPost, outcome: rejected, code: "tenant_suspended"},
		{name: "step 4: no role", mutate: auth(principalWith(func(p *authz.Principal) { p.Roles = nil })), do: chatPost, outcome: rejected, code: "forbidden"},
		{name: "step 4: only model:invoke", mutate: auth(principalWith(func(p *authz.Principal) { p.Roles = nil; p.Scopes = []string{authz.ModelInvoke} })), do: chatPost, outcome: rejected, code: "forbidden"},
		{name: "step 4: only credential:use", mutate: auth(principalWith(func(p *authz.Principal) { p.Roles = nil; p.Scopes = []string{authz.CredentialUse} })), do: chatPost, outcome: rejected, code: "forbidden"},
		// Step 5.
		{name: "step 5: too large", mutate: func(o *gateway.Options) { o.MaxRequestBodyBytes = 10 }, do: chatPost, outcome: rejected, code: "request_too_large"},
		{name: "step 5: the body does not arrive in time", do: direct(failingBody{os.ErrDeadlineExceeded}, 10, context.Background()), outcome: usage.OutcomeTimeout, code: "request_timeout"},
		{name: "step 5: the caller closes while sending the body", do: direct(failingBody{io.ErrUnexpectedEOF}, 10, context.Background()), outcome: usage.OutcomeCancelled},
		// Step 6.
		{name: "step 6: not a request", do: func(t *testing.T, h *harness) { h.sendAuthed(t, route, []byte(`{"messages":[]}`)) }, outcome: rejected, code: "invalid_request"},
		{name: "step 6: background processing", do: func(t *testing.T, h *harness) {
			h.sendAuthed(t, opResponses.path, []byte(`{"model":"fake-basic","background":true}`))
		}, outcome: rejected, code: "invalid_request"},
		// Step 7.
		{name: "step 7: a model that is not allowed", do: post(opChat, "nope", false), outcome: rejected, code: "model_not_allowed"},
		{name: "step 7: a provider without an endpoint", prepare: func(h *harness) { h.cat.ResolveErr = gateway.ErrProviderUnavailable }, do: chatPost, outcome: rejected, code: "endpoint_not_supported"},
		{name: "step 7: a decision that cannot be taken", prepare: func(h *harness) { h.cat.ResolveErr = errors.New("synthetic") }, do: chatPost, outcome: internal, code: "unavailable"},
		// Step 8.
		{name: "step 8: no credential", mutate: func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) }, do: chatPost, outcome: rejected, code: "no_credential"},
		{name: "step 8: the principal may not use a credential", prepare: func(h *harness) { h.creds.Err = gateway.ErrCredentialDenied }, do: chatPost, outcome: rejected, code: "forbidden"},
		{name: "step 8: a credential that cannot be verified", prepare: func(h *harness) { h.creds.Err = errors.New("synthetic") }, do: chatPost, outcome: internal, code: "unavailable"},
		// Cancelled while resolving.
		{name: "cancelled while the model is resolved", prepare: func(h *harness) { h.cat.Block = make(chan struct{}) }, do: func(t *testing.T, h *harness) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				resp, err := h.request(ctx, t, http.MethodPost, route, chat("fake-basic", false), authed())
				if err == nil {
					resp.Body.Close()
				}
			}()
			testutil.Eventually(t, 5*time.Second, "the model to be resolved", func() bool { return h.cat.Resolves.Load() == 1 })
			cancel()
			<-done
		}, outcome: usage.OutcomeCancelled},
		// Step 9 without an answer.
		{name: "a forbidden address", prepare: retarget(func(g *gateway.Target) { g.BaseURL, _ = url.Parse("http://169.254.169.254:80"); g.Private = true }), do: chatPost, outcome: usage.OutcomeUpstreamError, code: "upstream_unreachable"},
		{name: "a connection error", prepare: closedPortTarget, do: chatPost, outcome: usage.OutcomeUpstreamError, code: "upstream_unreachable"},
		{name: "a timeout before the headers", mutate: short, do: post(opChat, fakeprovider.ModelHang, false), outcome: usage.OutcomeTimeout, code: "upstream_timeout"},
		// The provider answered with an error status.
		{name: "a 4xx", do: post(opChat, fakeprovider.ModelError429, false), outcome: usage.OutcomeUpstreamError, status: 429},
		{name: "a 5xx", do: post(opChat, fakeprovider.ModelError500Echo, false), outcome: usage.OutcomeUpstreamError, status: 500},
		{name: "a redirect", do: post(opChat, fakeprovider.ModelRedirect, false), prepare: func(h *harness) { h.up.RedirectTo("http://127.0.0.1:1/elsewhere") },
			outcome: usage.OutcomeUpstreamError, code: "upstream_unreachable", status: 307},
		// Success.
		{name: "success: chat", do: post(opChat, "fake-basic", false), outcome: usage.OutcomeOK, status: 200},
		{name: "success: chat stream", do: post(opChat, "fake-basic", true), outcome: usage.OutcomeOK, status: 200},
		{name: "success: responses", do: post(opResponses, "fake-basic", false), outcome: usage.OutcomeOK, status: 200},
		{name: "success: responses stream", do: post(opResponses, "fake-basic", true), outcome: usage.OutcomeOK, status: 200},
		{name: "success: embeddings", do: post(opEmbeddings, "fake-basic", false), outcome: usage.OutcomeOK, status: 200},
		// Mid-stream.
		{name: "the caller cancels mid-stream", do: func(t *testing.T, h *harness) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp, err := h.request(ctx, t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if _, err := testutil.ReadEvent(bufio.NewReader(resp.Body)); err != nil {
				t.Fatal(err)
			}
			cancel()
			if !h.up.WaitCancelled(2 * time.Second) {
				t.Fatal("the provider request was not cancelled")
			}
		}, outcome: usage.OutcomeCancelled, status: 200},
		{name: "the duration limit passes mid-stream", mutate: short, do: func(t *testing.T, h *harness) {
			resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat(fakeprovider.ModelGatedStream, true), authed())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			_, _ = io.ReadAll(resp.Body)
		}, outcome: usage.OutcomeTimeout, status: 200},
		{name: "the provider cuts the stream", do: func(t *testing.T, h *harness) {
			resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat(fakeprovider.ModelCut, true), authed())
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
		}, outcome: usage.OutcomeUpstreamError, status: 200},
		// Panics and shutdown.
		{name: "a panic before the first write", mutate: func(o *gateway.Options) { o.Private = panicTransport{} }, do: chatPost, outcome: internal, code: "internal"},
		{name: "a panic after the first write", mutate: func(o *gateway.Options) { o.Private = bodyPanicTransport{} }, do: func(t *testing.T, h *harness) {
			resp, err := h.request(ctx5(t), t, http.MethodPost, route, chat("fake-basic", false), authed())
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
		}, outcome: internal, code: "internal", status: 200},
	}
	shutdown, stop := context.WithCancelCause(context.Background())
	stop(gateway.ErrShutdown)
	cases = append(cases, endCase{name: "a forced shutdown before the forward", do: direct(bytes.NewReader(chat("fake-basic", false)), -1, shutdown), outcome: usage.OutcomeCancelled})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.mutate)
			if c.prepare != nil {
				c.prepare(h)
			}
			c.do(t, h)
			recs := h.settle(t, 1)
			r := recs[0]
			if got := h.rec.Admits(); got != 1 {
				t.Errorf("Admit was granted %d times, want 1", got)
			}
			if r.Outcome != c.outcome || r.ErrorCode != c.code {
				t.Errorf("outcome %q code %q, want %q %q", r.Outcome, r.ErrorCode, c.outcome, c.code)
			}
			if r.UpstreamStatus != c.status {
				t.Errorf("upstream status %d, want %d", r.UpstreamStatus, c.status)
			}
			if r.TenantID != testPrincipal.TenantID || len(r.RequestID) != 32 {
				t.Errorf("attribution: tenant %s request %q", r.TenantID, r.RequestID)
			}
		})
	}

	t.Run("requests that are not authenticated have neither", func(t *testing.T) {
		h := newHarness(t, nil)
		h.send(t, http.MethodPost, "/nope/v1/chat/completions", chat("fake-basic", false), authed())                                 // not routed
		h.send(t, http.MethodGet, route, nil, authed())                                                                              // not routed
		h.send(t, http.MethodPost, route, chat("fake-basic", false), http.Header{})                                                  // no credential
		h.send(t, http.MethodPost, route, chat("fake-basic", false), http.Header{"Authorization": {"Bearer " + testutil.NewKey(t)}}) // a key nobody knows
		h.send(t, http.MethodPost, route, chat("fake-basic", false), http.Header{"Authorization": {"Basic x"}})
		h.requestLines(t, 5)
		if got := h.rec.Admits(); got != 0 {
			t.Errorf("%d admissions", got)
		}
		if got := len(h.rec.Records()); got != 0 {
			t.Errorf("%d records", got)
		}
	})
}

// TestRecordFields is T-485: every field of the record of section 5.5, for a plain and a streamed
// success, with the price in force at the start of the request, and for refusals.
func TestRecordFields(t *testing.T) {
	check := func(t *testing.T, c captured, r usage.Record, stream bool, reqBody []byte) {
		t.Helper()
		if !reUUIDv7.MatchString(r.ID) {
			t.Errorf("ID %q", r.ID)
		}
		if r.StartedAt.IsZero() || r.StartedAt.Location() != time.UTC || time.Since(r.StartedAt) > time.Minute || r.StartedAt.After(time.Now()) {
			t.Errorf("StartedAt %v", r.StartedAt)
		}
		if got := usage.NewID(r.StartedAt)[:13]; r.ID[:13] != got {
			t.Errorf("the ID %q does not carry the start %v", r.ID, r.StartedAt)
		}
		if r.TenantID != testPrincipal.TenantID || r.RequestID != c.header.Get("X-Pandokos-Request-Id") || !reTrace.MatchString(r.TraceID) {
			t.Errorf("tenant %s, request ID %q (header %q), trace %q", r.TenantID, r.RequestID, c.header.Get("X-Pandokos-Request-Id"), r.TraceID)
		}
		if r.PrincipalKind != "service_account" || r.PrincipalID != testPrincipal.ID || r.KeyID != testPrincipal.KeyID || r.Funding != "byok" {
			t.Errorf("principal %q %q %q funding %q", r.PrincipalKind, r.PrincipalID, r.KeyID, r.Funding)
		}
		if r.CredentialID != "synthetic-credential-openai" {
			t.Errorf("credential %q", r.CredentialID)
		}
		if r.Provider != "openai" || r.APIFamily != "openai" || r.Operation != "chat.completions" || r.Endpoint != "synthetic-endpoint" ||
			r.Model != "fake-basic" || r.ModelID != "synthetic-model-openai-fake-basic" || r.UpstreamModel != "fake-basic" {
			t.Errorf("what was called: %+v", r)
		}
		if r.Stream == nil || *r.Stream != stream {
			t.Errorf("Stream %v", r.Stream)
		}
		if r.Outcome != usage.OutcomeOK || r.ErrorCode != "" || r.UpstreamStatus != 200 {
			t.Errorf("outcome %q %q %d", r.Outcome, r.ErrorCode, r.UpstreamStatus)
		}
		if r.TTFB == nil || *r.TTFB < 0 || *r.TTFB > r.Duration || r.Duration <= 0 {
			t.Errorf("ttfb %v duration %v", r.TTFB, r.Duration)
		}
		if r.RequestBytes == nil || *r.RequestBytes != int64(len(reqBody)) {
			t.Errorf("RequestBytes %v, want %d", r.RequestBytes, len(reqBody))
		}
		if r.ResponseBytes == nil || *r.ResponseBytes != int64(len(c.body)) {
			t.Errorf("ResponseBytes %v, want %d", r.ResponseBytes, len(c.body))
		}
		if r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider || r.Usage.InputTokens != 9 || r.Usage.OutputTokens != 12 {
			t.Errorf("usage %+v sources %q %q", r.Usage, r.InputSource, r.OutputSource)
		}
		if want := map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}; fmt.Sprint(r.Usage.Reported) != fmt.Sprint(want) {
			t.Errorf("Reported %v", r.Usage.Reported)
		}
		if r.Estimated.InputTokens != 0 || r.Estimated.OutputTokens != 0 {
			t.Errorf("estimate beside provider usage: %+v", r.Estimated)
		}
		if !r.Cost.Known || r.CostStatus != "known" || r.Cost.Currency != "USD" || r.Cost.VersionID != "synthetic-price-openai-fake-basic" || r.Cost.Amount.String() != "0.000099" {
			t.Errorf("cost %+v %q (amount %s)", r.Cost, r.CostStatus, r.Cost.Amount)
		}
		if len(r.Metadata) != 0 || r.MetadataDropped != 0 {
			t.Errorf("metadata %v dropped %d", r.Metadata, r.MetadataDropped)
		}
	}

	t.Run("a plain success", func(t *testing.T) {
		h := newHarness(t, nil)
		body := chat("fake-basic", false)
		c := h.sendAuthed(t, route, body)
		check(t, c, h.oneRecord(t), false, body)
	})
	t.Run("a streamed success", func(t *testing.T) {
		h := newHarness(t, nil)
		body := chat("fake-basic", true)
		c := h.sendAuthed(t, route, body)
		check(t, c, h.oneRecord(t), true, body)
	})
	t.Run("responses and embeddings", func(t *testing.T) {
		for _, o := range []opSpec{opResponses, opEmbeddings} {
			h := newHarness(t, nil)
			c := h.sendOp(t, o, "fake-basic", false)
			r := h.oneRecord(t)
			if r.Operation != o.name || r.Outcome != usage.OutcomeOK || r.InputSource != usage.SourceProvider || r.Stream == nil || *r.Stream {
				t.Errorf("%s: %+v", o.name, r)
			}
			if r.ResponseBytes == nil || *r.ResponseBytes != int64(len(c.body)) {
				t.Errorf("%s: ResponseBytes %v", o.name, r.ResponseBytes)
			}
		}
	})

	t.Run("the price in force at the start prices the request", func(t *testing.T) {
		for _, c := range []struct {
			name            string
			before, after   time.Duration // ValidFrom of the two versions, from now
			wantVersion     string
			wantAmount      string
			wantStatus      string
			wantReason      string
			noVersionsAtAll bool
		}{
			{"the earlier version, the later one not yet in force", -time.Hour, time.Hour, "synthetic-price-before", "0.000033", "known", "", false},
			{"the later version once it is in force", -2 * time.Hour, -time.Hour, "synthetic-price-after", "0.00033", "known", "", false},
			{"no version yet", time.Hour, 2 * time.Hour, "", "", "unknown", pricing.ReasonNoPrice, false},
			{"no price at all", 0, 0, "", "", "unknown", pricing.ReasonNoPrice, true},
		} {
			h := newHarness(t, nil)
			tgt := h.targets["openai/fake-basic"]
			tgt.Prices = testutil.TwoPrices(time.Now().Add(c.before), time.Now().Add(c.after))
			if c.noVersionsAtAll {
				tgt.Prices = nil
			}
			h.targets["openai/fake-basic"] = tgt
			h.sendAuthed(t, route, chat("fake-basic", false))
			r := h.oneRecord(t)
			if r.CostStatus != c.wantStatus || r.Cost.VersionID != c.wantVersion || r.Cost.Reason != c.wantReason {
				t.Errorf("%s: status %q version %q reason %q", c.name, r.CostStatus, r.Cost.VersionID, r.Cost.Reason)
			}
			if c.wantAmount != "" && r.Cost.Amount.String() != c.wantAmount {
				t.Errorf("%s: amount %s, want %s", c.name, r.Cost.Amount, c.wantAmount)
			}
			if c.wantStatus == "unknown" && r.Cost.Known {
				t.Errorf("%s: the cost is known", c.name)
			}
		}
	})

	t.Run("refusals carry attribution only", func(t *testing.T) {
		type refusal struct {
			name   string
			mutate func(*gateway.Options)
			body   []byte
			// what the record may hold
			stream, bytes, resolved bool
		}
		for _, c := range []refusal{
			{name: "step 4", mutate: func(o *gateway.Options) {
				o.Authenticator = fixedAuth{p: principalWith(func(p *authz.Principal) { p.Roles = nil })}
			}, body: chat("fake-basic", false)},
			{name: "step 6", body: []byte(`{"messages":[]}`), bytes: true},
			{name: "step 7", body: chat("synthetic-unresolved-model", false), bytes: true, stream: true},
			{name: "step 8", mutate: func(o *gateway.Options) { o.Credentials = testutil.StaticCredentials(nil) }, body: chat("fake-basic", true), bytes: true, stream: true, resolved: true},
		} {
			h := newHarness(t, c.mutate)
			resp := h.sendAuthed(t, route, c.body)
			r := h.oneRecord(t)
			if r.TenantID != testPrincipal.TenantID || r.RequestID != resp.header.Get("X-Pandokos-Request-Id") || r.PrincipalID != testPrincipal.ID || r.KeyID != testPrincipal.KeyID {
				t.Errorf("%s: attribution %+v", c.name, r)
			}
			if r.Provider != "openai" || r.APIFamily != "openai" || r.Operation != "chat.completions" || r.Funding != "byok" || !reTrace.MatchString(r.TraceID) || !reUUIDv7.MatchString(r.ID) {
				t.Errorf("%s: what was called: %+v", c.name, r)
			}
			if (r.Stream != nil) != c.stream || (r.RequestBytes != nil) != c.bytes {
				t.Errorf("%s: Stream %v RequestBytes %v", c.name, r.Stream, r.RequestBytes)
			}
			if (r.Model != "") != c.resolved || (r.ModelID != "") != c.resolved || (r.Endpoint != "") != c.resolved || (r.UpstreamModel != "") != c.resolved {
				t.Errorf("%s: model %q id %q endpoint %q upstream %q", c.name, r.Model, r.ModelID, r.Endpoint, r.UpstreamModel)
			}
			if r.CredentialID != "" || r.TTFB != nil || r.UpstreamStatus != 0 {
				t.Errorf("%s: credential %q ttfb %v status %d", c.name, r.CredentialID, r.TTFB, r.UpstreamStatus)
			}
			if r.InputSource != usage.SourceNone || r.OutputSource != usage.SourceNone || r.Usage.InputTokens != 0 || r.Estimated.InputTokens != 0 || r.CostStatus != "none" || r.Cost.Known {
				t.Errorf("%s: usage %+v estimate %+v cost %q", c.name, r.Usage, r.Estimated, r.CostStatus)
			}
			if r.Outcome != usage.OutcomeRejectedPolicy || r.ErrorCode == "" || r.Duration < 0 {
				t.Errorf("%s: outcome %q code %q", c.name, r.Outcome, r.ErrorCode)
			}
			if strings.Contains(fmt.Sprintf("%+v", r), "synthetic-unresolved-model") {
				t.Errorf("%s: the model string of the caller is in the record", c.name)
			}
		}
	})

	t.Run("an upstream identifier of 256 characters is recorded whole", func(t *testing.T) {
		h := newHarness(t, nil)
		tgt := h.targets["openai/fake-basic"]
		tgt.UpstreamModel = strings.Repeat("u", 256)
		h.targets["openai/fake-basic"] = tgt
		h.sendAuthed(t, route, chat("fake-basic", false))
		if r := h.oneRecord(t); r.UpstreamModel != tgt.UpstreamModel || r.Validate() != nil {
			t.Errorf("upstream model of %d characters, %v", len(r.UpstreamModel), r.Validate())
		}
	})
}

// TestAdmissionRefused is T-487: with no room in the ledger the request is refused with 503
// right after authentication and before any other work.
func TestAdmissionRefused(t *testing.T) {
	full := &testutil.MemoryRecorder{Capacity: 1}
	full.Admit()
	spy := &readSpy{t: t}
	h := newHarness(t, func(o *gateway.Options) { o.Recorder = full })
	req := httptest.NewRequest(http.MethodPost, route, spy)
	req.ContentLength = 10
	req.Header = authed()
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	res := rec.Result()
	b, _ := io.ReadAll(res.Body)
	assertContract(t, "a full ledger", captured{status: res.StatusCode, header: res.Header, body: b}, family.CodeUnavailable)
	if spy.read.Load() {
		t.Error("the body was read")
	}
	if n := h.cat.Resolves.Load(); n != 0 {
		t.Errorf("the model was resolved %d times", n)
	}
	if n := len(h.creds.Calls()); n != 0 {
		t.Errorf("a credential was opened %d times", n)
	}
	if n := len(h.up.Requests()); n != 0 {
		t.Errorf("the provider received %d requests", n)
	}
	if n := len(full.Records()); n != 0 {
		t.Errorf("%d records", n)
	}
	if n := full.Admits(); n != 1 {
		t.Errorf("%d admissions", n)
	}
	if n := len(h.spans.Started()) + len(h.spans.Ended()); n != 0 {
		t.Errorf("%d spans", n)
	}
	line := h.lastRequestLine(t)
	if line["ledger"] != "full" || line["error_code"] != "unavailable" || line["status"] != float64(503) {
		t.Errorf("the line: %v", line)
	}
	for _, k := range []string{"record_id", "trace_id", "model", "credential_id", "cost_status"} {
		if _, ok := line[k]; ok {
			t.Errorf("the field %s is logged for a request that was not admitted", k)
		}
	}
}

// TestRecordsAlwaysValidate is the principle of the ledger: a record built by the gateway passes
// usage.Record.Validate whatever the caller or the provider sends. Bounded values are saturated at
// the source, so no call is answered without its row.
func TestRecordsAlwaysValidate(t *testing.T) {
	rawChat := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	const okBody = `{"id":"x","object":"chat.completion","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	const hugeBody = `{"id":"x","object":"chat.completion","choices":[],"usage":{"prompt_tokens":9223372036854775807,"completion_tokens":9223372036854775807}}`
	rate, err := pricing.ParseDecimal("1000000000000")
	if err != nil {
		t.Fatal(err)
	}
	dear := []pricing.Version{{ID: "synthetic-price-dear", ModelID: "synthetic-model-openai-fake-basic", ValidFrom: time.Now().Add(-time.Hour),
		Currency: "USD", Source: "override",
		Rates: []pricing.Rate{{Category: pricing.CategoryInput, PerMillion: rate}, {Category: pricing.CategoryOutput, PerMillion: rate}}}}

	manyHeaders := http.Header{}
	for i := 0; i < 1200; i++ {
		manyHeaders["X-Pandokos-Metadata-Bad."+strconv.Itoa(i)] = []string{"v"}
	}

	for _, c := range []struct {
		name    string
		handler http.HandlerFunc
		hdr     http.Header
		target  func(*gateway.Target)
		check   func(*testing.T, captured, usage.Record)
	}{
		{name: "1200 metadata headers", handler: rawChat(200, okBody), hdr: manyHeaders, check: func(t *testing.T, c captured, r usage.Record) {
			if r.MetadataDropped != 1000 || c.header.Get("X-Pandokos-Metadata-Dropped") != "1000" {
				t.Errorf("dropped %d, header %q", r.MetadataDropped, c.header.Get("X-Pandokos-Metadata-Dropped"))
			}
		}},
		{name: "the longest model name and upstream id of the catalogue", handler: rawChat(200, okBody), target: func(g *gateway.Target) {
			g.Model, g.UpstreamModel = "fake-basic", strings.Repeat("u", 256)
		}},
		{name: "a status outside 100-599", handler: rawChat(999, `{}`), check: func(t *testing.T, c captured, r usage.Record) {
			if c.status != 999 || r.UpstreamStatus != 0 || r.Outcome != usage.OutcomeUpstreamError {
				t.Errorf("status %d, recorded %d, outcome %q", c.status, r.UpstreamStatus, r.Outcome)
			}
		}},
		{name: "a status of 600", handler: rawChat(600, `{}`), check: func(t *testing.T, c captured, r usage.Record) {
			if r.UpstreamStatus != 0 {
				t.Errorf("recorded %d", r.UpstreamStatus)
			}
		}},
		{name: "a status of 599", handler: rawChat(599, `{}`), check: func(t *testing.T, c captured, r usage.Record) {
			if r.UpstreamStatus != 599 {
				t.Errorf("recorded %d", r.UpstreamStatus)
			}
		}},
		{name: "usage and prices whose cost the column cannot hold", handler: rawChat(200, hugeBody),
			target: func(g *gateway.Target) { g.Prices = dear },
			check: func(t *testing.T, c captured, r usage.Record) {
				if r.CostStatus != "unknown" || r.Cost.Reason != pricing.ReasonInconsistentUsage || r.Cost.Known {
					t.Errorf("cost %q %+v", r.CostStatus, r.Cost)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newRaw(t, c.handler)
			if c.target != nil {
				tgt := h.targets["openai/fake-basic"]
				c.target(&tgt)
				h.targets["openai/fake-basic"] = tgt
			}
			resp := h.send(t, http.MethodPost, route, chat("fake-basic", false), withHeaders(c.hdr))
			r := h.oneRecord(t)
			if err := r.Validate(); err != nil {
				t.Fatalf("the record is refused (status %d): %v", resp.status, err)
			}
			if c.check != nil {
				c.check(t, resp, r)
			}
		})
	}
}
