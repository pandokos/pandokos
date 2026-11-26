package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/buildinfo"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/usage"
)

// unavailableRetryAfter is the Retry-After, in seconds, of a 503 because the credential cannot be
// verified.
const unavailableRetryAfter = "5"

// ErrShutdown is the cancellation cause of the base context of a server whose drain deadline
// passed. The gateway reports requests cancelled this way as shutdown_aborted.
var ErrShutdown = errors.New("forced shutdown")

// Authenticator turns the presented credential into a principal. It returns an error that wraps
// authz.ErrUnauthenticated or authz.ErrUnavailable.
type Authenticator interface {
	Authenticate(ctx context.Context, presented string) (authz.Principal, error)
}

// Options configures a Handler. The gateway receives plain values, not the configuration.
type Options struct {
	Logger                 *slog.Logger              // required
	Authenticator          Authenticator             // required
	Catalogue              Catalogue                 // required
	Credentials            Credentials               // required
	Failures               FailureRecorder           // optional
	Families               map[string]family.Adapter // required: API family -> adapter
	DefaultFamily          string                    // required, a key of Families
	Public, Private        http.RoundTripper         // required: by endpoint class
	MaxRequestBodyBytes    int64                     // must be positive
	RequestBodyReadTimeout time.Duration             // must be positive
	MaxRequestDuration     time.Duration             // must be positive
	Recorder               Recorder                  // required: receives the usage record of every admitted request
	Tracer                 trace.TracerProvider      // required
	PropagateTrace         bool                      // send traceparent and tracestate to the provider
	Limits                 Limits                    // optional: nil enforces no limit
	Metrics                Metrics                   // optional: nil records nothing
	DefaultOutputTokens    int64                     // output tokens reserved per choice when the caller sets no limit; positive
	Now                    func() time.Time          // optional: the clock of the start of a request (default time.Now); a seam for tests
}

// Handler is the inference handler.
type Handler struct {
	logger      *slog.Logger
	auth        Authenticator
	catalogue   Catalogue
	credentials Credentials
	failures    FailureRecorder
	families    map[string]family.Adapter
	familyNames []string // the keys of families in name order
	def         family.Adapter
	maxBody     int64
	readTimeout time.Duration
	maxDuration time.Duration
	proxy       *httputil.ReverseProxy
	recorder    Recorder
	tracer      trace.Tracer
	propagator  propagation.TextMapPropagator
	propagate   bool
	limits      Limits
	metrics     Metrics
	defaultOut  int64
	now         func() time.Time // Options.Now
}

// New validates the options and returns a Handler.
func New(o Options) (*Handler, error) {
	switch {
	case o.Logger == nil:
		return nil, errors.New("gateway: Logger is required")
	case o.Authenticator == nil:
		return nil, errors.New("gateway: Authenticator is required")
	case o.Catalogue == nil:
		return nil, errors.New("gateway: Catalogue is required")
	case o.Credentials == nil:
		return nil, errors.New("gateway: Credentials is required")
	case len(o.Families) == 0:
		return nil, errors.New("gateway: Families is required")
	case o.Public == nil:
		return nil, errors.New("gateway: Public transport is required")
	case o.Private == nil:
		return nil, errors.New("gateway: Private transport is required")
	case o.MaxRequestBodyBytes <= 0:
		return nil, errors.New("gateway: MaxRequestBodyBytes must be positive")
	case o.RequestBodyReadTimeout <= 0:
		return nil, errors.New("gateway: RequestBodyReadTimeout must be positive")
	case o.MaxRequestDuration <= 0:
		return nil, errors.New("gateway: MaxRequestDuration must be positive")
	case o.Recorder == nil:
		return nil, errors.New("gateway: Recorder is required")
	case o.Tracer == nil:
		return nil, errors.New("gateway: Tracer is required")
	case o.DefaultOutputTokens <= 0:
		return nil, errors.New("gateway: DefaultOutputTokens must be positive")
	}
	families := make(map[string]family.Adapter, len(o.Families))
	for name, a := range o.Families {
		if a == nil {
			return nil, errors.New("gateway: a family has no adapter")
		}
		families[name] = a
	}
	def, ok := families[o.DefaultFamily]
	if !ok {
		return nil, errors.New("gateway: DefaultFamily is not a key of Families")
	}
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	sort.Strings(names)
	now := o.Now
	if now == nil {
		now = time.Now
	}
	h := &Handler{
		now:         now,
		familyNames: names,
		logger:      o.Logger,
		auth:        o.Authenticator,
		catalogue:   o.Catalogue,
		credentials: o.Credentials,
		failures:    o.Failures,
		families:    families,
		def:         def,
		maxBody:     o.MaxRequestBodyBytes,
		readTimeout: o.RequestBodyReadTimeout,
		maxDuration: o.MaxRequestDuration,
		recorder:    o.Recorder,
		tracer:      o.Tracer.Tracer(instrumentationScope, trace.WithInstrumentationVersion(buildinfo.Version)),
		propagator:  propagation.TraceContext{},
		propagate:   o.PropagateTrace,
		limits:      o.Limits,
		metrics:     o.Metrics,
		defaultOut:  o.DefaultOutputTokens,
	}
	h.proxy = h.newProxy(&classTransport{public: o.Public, private: o.Private})
	return h, nil
}

type stateKey struct{}

// reqState is the state of one request. It is reached through the request context by the proxy
// callbacks, which run on the handler goroutine.
type reqState struct {
	now      func() time.Time // the clock of the handler
	start    time.Time
	id       string
	sw       *statusWriter
	callerCx context.Context
	outCx    context.Context

	adapter  family.Adapter // the default family's until step 2 found one
	matched  bool
	method   string
	provider string // catalogue name, from the path
	family   string
	op       family.Operation

	principal *authz.Principal

	bodyRead      bool
	parsed        bool
	stream        bool
	strip         bool
	requestBytes  int
	invalidReason string

	target       Target
	resolved     bool
	credential   secret.Secret
	credentialID string
	pinned       bool
	opened       bool

	errCode     family.ErrorCode
	clientError bool

	answered          bool
	delivered         bool // the proxy returned: the answer was copied to the caller completely
	upstreamStatus    int
	upstreamErr       string
	egressReason      string
	upstreamRequestID string

	// Sprint 3.2: the usage record and the spans of an admitted request.
	admitted        bool
	ledgerFull      bool
	recordID        string
	traceID         string
	metadata        map[string]string
	metadataDropped int
	// metaKept and metaDropped are what the headers gave, before the tenant's policy (step 7).
	metaKept        map[string]string
	metaDropped     int
	streamKnown     bool // the stream flag was read (step 6 passed)
	server, client  trace.Span
	serverCx        context.Context
	firstByte       time.Time
	forwardAt       time.Time // the start of step 9, before the client span starts
	callEnd         time.Time // the end of the client span; zero when the call never produced a body
	scheme          string
	readerAbandoned bool // a family reader panicked and is no longer called
	readerFailed    bool // a reader panicked or gave up: logged as usage_reader
	deltas          int64
	costUsage       family.Usage // the figures the cost was computed on
	rec             *usage.Record

	// Sprint 5.1: the reservation of step 7b.
	reservation    Reservation
	reservedTokens int64
	reservedCost   pricing.Cost // the estimate's cost, kept for the observation of the estimate
	refusal        *LimitRefusal
	counted        bool       // the credential was opened: the request counts (sprint 5.2 D-2)
	limitState     LimitState // how the limits treated the request (sprint 5.3, 5.5); not_evaluated until step 7b says

	streamBody *streamBody
	teeBody    *teeBody
	countBody  *countBody
}

func stateOf(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateKey{}).(*reqState)
	return st
}

// fail answers with a Pandokos error in the shape of the family of the request.
func (h *Handler) fail(st *reqState, code family.ErrorCode) {
	st.adapter.WriteError(st.sw, code)
	st.errCode = code
}

// unavailable answers 503, unless the caller is gone or the server is being forced down: then
// the failure is the cancellation, there is nobody to answer and the outcome says so. The
// connection is aborted: returning without a write would let net/http send an empty 200.
func (h *Handler) unavailable(st *reqState) {
	if st.callerCx.Err() != nil {
		panic(http.ErrAbortHandler)
	}
	h.fail(st, family.CodeUnavailable)
}

// providerName reports whether s can be the name of a provider: 1 to 63 characters of [a-z0-9-].
func providerName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// ServeHTTP handles one request: the steps of sprint.md section 5.1, in that order.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	st := &reqState{now: h.now, start: h.now(), id: NewRequestID(), callerCx: r.Context(), method: logMethod(r.Method), adapter: h.def, limitState: LimitStateNotEvaluated}
	st.sw = &statusWriter{ResponseWriter: w, requestID: st.id}
	st.scheme = "http"
	if r.TLS != nil {
		st.scheme = "https"
	}
	w.Header().Set(family.HeaderRequestID, st.id)
	defer func() { h.finish(st, recover()) }()

	if !h.route(r, st) { // step 2
		return
	}
	if !h.authenticate(r, st) { // steps 3, 3a, 3b and 4
		return
	}
	r = r.WithContext(st.serverCx)

	// Step 5: the body.
	if r.ContentLength > h.maxBody {
		h.fail(st, family.CodeRequestTooLarge)
		return
	}
	body, err := h.readBody(w, r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		var netErr net.Error
		switch {
		case errors.As(err, &tooLarge):
			h.fail(st, family.CodeRequestTooLarge)
		case errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
			w.Header().Set("Connection", "close")
			h.fail(st, family.CodeRequestTimeout)
		default:
			st.clientError = true
			panic(http.ErrAbortHandler)
		}
		return
	}
	st.bodyRead, st.requestBytes = true, len(body)

	// Step 6: the model and the stream flag, nothing else.
	parsed, err := st.adapter.ParseRequest(st.op, body)
	if err != nil {
		st.invalidReason = family.InvalidReason(err)
		h.fail(st, family.CodeInvalidRequest)
		return
	}

	st.streamKnown, st.stream = true, parsed.Stream()

	// Step 7: the model.
	target, err := h.catalogue.Resolve(r.Context(), *st.principal, st.provider, parsed.Model())
	switch {
	case errors.Is(err, ErrModelNotAllowed):
		h.fail(st, family.CodeModelNotAllowed)
		return
	case errors.Is(err, ErrProviderUnavailable):
		h.fail(st, family.CodeEndpointNotSupported)
		return
	case err != nil || target.BaseURL == nil:
		h.unavailable(st)
		return
	}
	st.target, st.resolved = target, true
	st.metadata, st.metadataDropped = usage.Restrict(st.metaKept, st.metaDropped, target.MetadataKeys)
	metadataHeader(st.sw.Header(), st.metadataDropped)

	// Step 7b: the limits.
	if !h.reserve(st, parsed.OutputBound()) {
		return
	}

	// Step 8: the credential.
	cred, err := h.credentials.Open(r.Context(), *st.principal, st.id, st.provider)
	switch {
	case errors.Is(err, ErrNoCredential):
		h.fail(st, family.CodeNoCredential)
		return
	case errors.Is(err, ErrCredentialDenied):
		h.fail(st, family.CodeForbidden)
		return
	case err != nil:
		// The key service may be away: tell the caller to come back.
		if r.Context().Err() == nil {
			st.sw.Header().Set("Retry-After", unavailableRetryAfter)
		}
		h.unavailable(st)
		return
	}
	st.credential, st.credentialID, st.pinned, st.opened, st.counted = cred.Secret, cred.ID, cred.Pinned, true, true

	// Step 9: forward. A request that was cancelled in steps 2 to 8 is not forwarded.
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	prepared, strip := parsed.Prepare(family.Preparation{UpstreamModel: target.UpstreamModel, StreamUsage: target.StreamUsage})
	st.parsed, st.stream, st.strip = true, parsed.Stream(), strip

	st.forwardAt = st.now()
	outCx, cancel := context.WithTimeout(r.Context(), h.maxDuration)
	defer cancel()
	outCx, st.client = h.tracer.Start(outCx, st.op.GenAI, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(st.clientSpanAttributes()...))
	st.outCx = outCx
	req := r.WithContext(context.WithValue(outCx, stateKey{}, st))
	req.Body = io.NopCloser(bytes.NewReader(prepared))
	req.ContentLength = int64(len(prepared))
	req.TransferEncoding = nil
	req.GetBody = nil
	h.proxy.ServeHTTP(st.sw, req)
	// The proxy aborts the handler by panic when the copy of the answer fails, the caller's side
	// included: a normal return means the caller was sent the whole answer.
	st.delivered = true
}

// route runs step 2: the path is /<provider>/<native path>. It answers and returns false when
// the request is not served.
func (h *Handler) route(r *http.Request, st *reqState) bool {
	p := r.URL.Path
	h.pickErrorShape(r, st)
	if len(p) < 2 || p[0] != '/' {
		h.fail(st, family.CodeEndpointNotSupported)
		return false
	}
	i := strings.IndexByte(p[1:], '/')
	if i <= 0 {
		h.fail(st, family.CodeEndpointNotSupported)
		return false
	}
	segment, native := p[1:1+i], p[1+i:]
	if !providerName(segment) {
		h.fail(st, family.CodeEndpointNotSupported)
		return false
	}
	fam, ok, err := h.catalogue.Provider(r.Context(), segment)
	if err != nil {
		h.unavailable(st)
		return false
	}
	adapter := h.families[fam]
	if !ok || adapter == nil {
		h.fail(st, family.CodeEndpointNotSupported)
		return false
	}
	st.adapter = adapter // a known provider answers in its own family's shape
	op, ok := adapter.Match(r.Method, native)
	if !ok {
		h.fail(st, family.CodeEndpointNotSupported)
		return false
	}
	st.adapter, st.matched, st.provider, st.family, st.op = adapter, true, segment, fam, op
	return true
}

// pickErrorShape chooses the adapter that writes the error of a request that reaches no family
// (sprint.md section 5.3): the first, in name order, that recognizes the method, the native
// path and the header; the default otherwise. It reads nothing else of the request.
func (h *Handler) pickErrorShape(r *http.Request, st *reqState) {
	p := r.URL.Path
	native := p
	if len(p) > 1 && p[0] == '/' {
		if i := strings.IndexByte(p[1:], '/'); i > 0 {
			native = p[1+i:]
		}
	}
	for _, name := range h.familyNames {
		if a := h.families[name]; a.Recognizes(r.Method, native, r.Header) {
			st.adapter = a
			return
		}
	}
}

// failureDetail is the shape of authn.FailureDetail, which is an alias of this struct: the gateway
// does not import authn and reads a failure's detail through an interface of its own.
type failureDetail = struct{ Reason, KeyID, TenantID, IssuerID, IssuerScope, Subject string }

// authenticate runs steps 3 and 4: the credential, then the decision. It answers and returns
// false when the request is refused.
func (h *Handler) authenticate(r *http.Request, st *reqState) bool {
	cred, ok := st.adapter.InboundCredential(r.Header)
	if !ok {
		h.logger.Warn("authentication failed", "listener", "inference", "request_id", st.id, "reason", "malformed")
		h.fail(st, family.CodeUnauthenticated)
		return false
	}
	p, err := h.auth.Authenticate(r.Context(), cred)
	switch {
	case errors.Is(err, authz.ErrUnavailable):
		// The credential cannot be verified now (a database or an issuer's keys): tell the
		// caller to come back.
		st.sw.Header().Set("Retry-After", unavailableRetryAfter)
		h.unavailable(st)
		return false
	case err != nil:
		detail := failureDetail{Reason: "unauthenticated"}
		var d interface {
			Detail() failureDetail
		}
		if errors.As(err, &d) {
			detail = d.Detail()
		}
		// The line says why, never who: the key, the tenant, the issuer and the subject go to the
		// tenant's audit log only (ADR 0055).
		attrs := []any{"listener", "inference", "request_id", st.id, "reason", detail.Reason}
		if detail.TenantID != "" && h.failures != nil {
			audited := h.failures.Record(r.Context(), AuthFailure{
				RequestID: st.id, Reason: detail.Reason, KeyID: detail.KeyID, TenantID: detail.TenantID,
				IssuerID: detail.IssuerID, IssuerScope: detail.IssuerScope, Subject: detail.Subject,
			})
			attrs = append(attrs, "audited", audited)
		}
		h.logger.Warn("authentication failed", attrs...)
		h.fail(st, family.CodeUnauthenticated)
		return false
	}
	st.principal = &p
	// Step 3a: room in the ledger for this request's usage record.
	if !h.recorder.Admit() {
		st.ledgerFull = true
		h.fail(st, family.CodeUnavailable)
		return false
	}
	st.admitted = true
	// Step 3b: attribution. Nothing of this runs for a caller that is not authenticated.
	// What the tenant accepts is not known yet (step 7): until then nothing is recorded and every
	// entry kept counts as dropped.
	st.metaKept, st.metaDropped = usage.ParseMetadata(r.Header)
	st.metadata, st.metadataDropped = usage.Restrict(st.metaKept, st.metaDropped, map[string]bool{})
	metadataHeader(st.sw.Header(), st.metadataDropped)
	st.recordID = usage.NewID(st.start)
	ctx := h.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	ctx, st.server = h.tracer.Start(ctx, st.op.Method, trace.WithTimestamp(st.start), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(st.serverSpanAttributes(scheme)...))
	st.serverCx = ctx
	st.traceID = traceIDOf(st.server)
	// Under BYOK a call needs both permissions (D-4).
	for _, perm := range []string{authz.ModelInvoke, authz.CredentialUse} {
		switch authz.Decide(p, perm, authz.Target{TenantID: p.TenantID}) {
		case authz.Allow:
			continue
		case authz.DenyTenantState:
			h.fail(st, family.CodeTenantSuspended)
		default:
			h.fail(st, family.CodeForbidden)
		}
		return false
	}
	return true
}

// readBody reads the request body under the read deadline and the size limit.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(h.readTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		// The deadline stays armed: when the caller stalled, the server's own cleanup of the
		// unread body must not wait for data that never comes.
		return nil, err
	}
	if err := rc.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	return body, nil
}

func logMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// outcome applies the table of sprint.md section 5.8, first match wins. A caller that goes away
// after the answer was completely written (the proxy returned) does not change the outcome:
// net/http ends the context of a request when the caller closes the connection, which a client
// may do as soon as it has read a complete answer, while the handler still records and settles.
func (st *reqState) outcome(rec any) string {
	aborted := rec != nil && errors.Is(asError(rec), http.ErrAbortHandler)
	switch {
	case st.errCode != "":
		return "pandokos_error"
	case st.clientError:
		return "client_error"
	case !st.delivered && errors.Is(context.Cause(st.callerCx), ErrShutdown):
		return "shutdown_aborted"
	case !st.delivered && st.callerCx.Err() != nil:
		return "client_cancelled"
	case !st.delivered && st.answered && st.outCx != nil && errors.Is(st.outCx.Err(), context.DeadlineExceeded):
		return "timeout"
	case aborted:
		return "upstream_aborted"
	case rec != nil:
		return "pandokos_error"
	}
	return "ok"
}

func asError(v any) error {
	err, _ := v.(error)
	return err
}

// finish runs when the handler ends, also by panic. It classifies the request, writes the
// request log line and ends the response.
func (h *Handler) finish(st *reqState, rec any) {
	aborted := rec != nil && errors.Is(asError(rec), http.ErrAbortHandler)
	otherPanic := rec != nil && !aborted
	wroteBefore := st.sw.wroteHeader()

	if otherPanic {
		h.logger.Error("panic recovered",
			"request_id", st.id,
			"panic_type", fmt.Sprintf("%T", rec),
			"stack", string(debug.Stack()))
		if !wroteBefore {
			h.fail(st, family.CodeInternal)
		}
	}
	outcome := st.outcome(rec)
	if otherPanic && st.errCode == "" {
		st.errCode = family.CodeInternal
		outcome = "pandokos_error"
	}
	// The reservation ends whatever the record, the client or the server end do: a panic in
	// any of them must not leave the slot and the tokens held until they expire.
	// The observation follows the settle, which gives its figures, and runs after the request
	// line, also when the record panicked.
	defer func() {
		est := h.settleLimit(st)
		if h.metrics != nil {
			h.observe(st, outcome, est)
		}
	}()
	if st.admitted {
		h.finishRecord(st, outcome)
	}
	h.logRequest(st, outcome)

	switch {
	case aborted:
		panic(rec)
	case otherPanic && wroteBefore:
		panic(http.ErrAbortHandler)
	}
}

// logRequest writes the request line. It carries what the operator needs to run the installation
// and to explain a refusal or a failure: no identity of the caller or of its tenant, no model, no
// size, no usage and no limit rule. The tenant reaches a line through its request ID (ADR 0055).
func (h *Handler) logRequest(st *reqState, outcome string) {
	attrs := []any{
		"request_id", st.id,
		"method", st.method,
		"route", routeLabelOf(st),
		"status", st.sw.status(),
		"outcome", outcome,
	}
	if st.errCode != "" {
		attrs = append(attrs, "error_code", string(st.errCode))
	}
	attrs = append(attrs, st.limitAttrs()...)
	if st.invalidReason != "" {
		attrs = append(attrs, "invalid_reason", st.invalidReason)
	}
	attrs = append(attrs, "duration_ms", st.now().Sub(st.start).Milliseconds())
	if st.matched {
		attrs = append(attrs, "provider", st.provider)
	}
	if st.resolved {
		attrs = append(attrs, "endpoint", st.target.Endpoint)
	}
	if st.parsed {
		attrs = append(attrs, "stream", st.stream, "usage_injected", st.strip)
	}
	if st.upstreamStatus != 0 {
		attrs = append(attrs, "upstream_status", st.upstreamStatus)
	}
	if d, ok := st.ttfb(); ok {
		attrs = append(attrs, "ttfb_ms", d.Milliseconds())
	}
	if st.upstreamErr != "" {
		attrs = append(attrs, "upstream_error", st.upstreamErr)
	}
	if st.egressReason != "" {
		attrs = append(attrs, "egress_reason", st.egressReason)
	}
	if st.ledgerFull {
		attrs = append(attrs, "ledger", "full")
	}
	if r := st.rec; r != nil && r.TraceID != "" {
		attrs = append(attrs, "trace_id", r.TraceID)
	}
	if st.readerFailed {
		attrs = append(attrs, "usage_reader", "failed")
	}
	h.logger.Info("request", attrs...)
}

func routeLabelOf(st *reqState) string {
	if st.matched {
		return st.op.Method + " /{provider}" + st.op.Path
	}
	return "unmatched"
}

func (st *reqState) responseBytes() int64 {
	switch {
	case st.streamBody != nil:
		return st.streamBody.bytes
	case st.teeBody != nil:
		return st.teeBody.bytes
	case st.countBody != nil:
		return st.countBody.bytes
	}
	return 0
}
