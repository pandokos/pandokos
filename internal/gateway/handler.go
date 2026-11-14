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

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/secret"
)

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
}

// Handler is the inference handler.
type Handler struct {
	logger      *slog.Logger
	auth        Authenticator
	catalogue   Catalogue
	credentials Credentials
	failures    FailureRecorder
	families    map[string]family.Adapter
	def         family.Adapter
	maxBody     int64
	readTimeout time.Duration
	maxDuration time.Duration
	proxy       *httputil.ReverseProxy
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
	h := &Handler{
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
	}
	h.proxy = h.newProxy(&classTransport{public: o.Public, private: o.Private})
	return h, nil
}

type stateKey struct{}

// reqState is the state of one request. It is reached through the request context by the proxy
// callbacks, which run on the handler goroutine.
type reqState struct {
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
	keyID     string // from a failed authentication, when the key parsed

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
	upstreamStatus    int
	ttfb              time.Duration
	upstreamErr       string
	egressReason      string
	upstreamRequestID string

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
	st := &reqState{start: time.Now(), id: NewRequestID(), callerCx: r.Context(), method: logMethod(r.Method), adapter: h.def}
	st.sw = &statusWriter{ResponseWriter: w, requestID: st.id}
	w.Header().Set(family.HeaderRequestID, st.id)
	defer func() { h.finish(st, recover()) }()

	if !h.route(r, st) { // step 2
		return
	}
	if !h.authenticate(r, st) { // steps 3 and 4
		return
	}

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
		h.unavailable(st)
		return
	}
	st.credential, st.credentialID, st.pinned, st.opened = cred.Secret, cred.ID, cred.Pinned, true

	// Step 9: forward. A request that was cancelled in steps 2 to 8 is not forwarded.
	if r.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	prepared, strip := parsed.Prepare(target.UpstreamModel)
	st.parsed, st.stream, st.strip = true, parsed.Stream(), strip

	outCx, cancel := context.WithTimeout(r.Context(), h.maxDuration)
	defer cancel()
	st.outCx = outCx
	req := r.WithContext(context.WithValue(outCx, stateKey{}, st))
	req.Body = io.NopCloser(bytes.NewReader(prepared))
	req.ContentLength = int64(len(prepared))
	req.TransferEncoding = nil
	req.GetBody = nil
	h.proxy.ServeHTTP(st.sw, req)
}

// route runs step 2: the path is /<provider>/<native path>. It answers and returns false when
// the request is not served.
func (h *Handler) route(r *http.Request, st *reqState) bool {
	p := r.URL.Path
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
	op, ok := adapter.Match(r.Method, native)
	if !ok {
		h.fail(st, family.CodeEndpointNotSupported)
		return false
	}
	st.adapter, st.matched, st.provider, st.family, st.op = adapter, true, segment, fam, op
	return true
}

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
		h.unavailable(st)
		return false
	case err != nil:
		reason, keyID, tenantID := "unauthenticated", "", ""
		var d interface {
			Detail() (string, string, string)
		}
		if errors.As(err, &d) {
			reason, keyID, tenantID = d.Detail()
		}
		st.keyID = keyID
		attrs := []any{"listener", "inference", "request_id", st.id, "reason", reason}
		if keyID != "" {
			attrs = append(attrs, "key_id", keyID)
		}
		if tenantID != "" {
			attrs = append(attrs, "tenant_id", tenantID)
			if h.failures != nil {
				audited := h.failures.Record(r.Context(), AuthFailure{RequestID: st.id, Reason: reason, KeyID: keyID, TenantID: tenantID})
				attrs = append(attrs, "audited", audited)
			}
		}
		h.logger.Warn("authentication failed", attrs...)
		h.fail(st, family.CodeUnauthenticated)
		return false
	}
	st.principal = &p
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

// outcome applies the table of sprint.md section 5.8, first match wins.
func (st *reqState) outcome(rec any) string {
	aborted := rec != nil && errors.Is(asError(rec), http.ErrAbortHandler)
	switch {
	case st.errCode != "":
		return "pandokos_error"
	case st.clientError:
		return "client_error"
	case errors.Is(context.Cause(st.callerCx), ErrShutdown):
		return "shutdown_aborted"
	case st.callerCx.Err() != nil:
		return "client_cancelled"
	case st.answered && st.outCx != nil && errors.Is(st.outCx.Err(), context.DeadlineExceeded):
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
	h.logRequest(st, outcome)

	switch {
	case aborted:
		panic(rec)
	case otherPanic && wroteBefore:
		panic(http.ErrAbortHandler)
	}
}

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
	if st.invalidReason != "" {
		attrs = append(attrs, "invalid_reason", st.invalidReason)
	}
	attrs = append(attrs, "duration_ms", time.Since(st.start).Milliseconds())
	if p := st.principal; p != nil {
		attrs = append(attrs, "tenant_id", p.TenantID.String(), "service_account_id", p.ID, "key_id", p.KeyID)
	} else if st.keyID != "" {
		attrs = append(attrs, "key_id", st.keyID)
	}
	if st.matched {
		attrs = append(attrs, "provider", st.provider, "api_family", st.family, "operation", st.op.Name)
	}
	if st.resolved {
		attrs = append(attrs, "model", st.target.Model, "model_id", st.target.ModelID, "endpoint", st.target.Endpoint)
	}
	if st.opened {
		attrs = append(attrs, "credential_id", st.credentialID, "credential_pinned", st.pinned, "funding", "byok")
	}
	if st.parsed {
		attrs = append(attrs, "stream", st.stream, "usage_injected", st.strip)
	}
	if st.upstreamStatus != 0 {
		attrs = append(attrs, "upstream_status", st.upstreamStatus)
	}
	if st.answered {
		attrs = append(attrs, "ttfb_ms", st.ttfb.Milliseconds())
	}
	if st.upstreamErr != "" {
		attrs = append(attrs, "upstream_error", st.upstreamErr)
	}
	if st.egressReason != "" {
		attrs = append(attrs, "egress_reason", st.egressReason)
	}
	if st.upstreamRequestID != "" {
		attrs = append(attrs, "upstream_request_id", st.upstreamRequestID)
	}
	if st.bodyRead {
		attrs = append(attrs, "request_bytes", st.requestBytes)
	}
	if st.answered {
		attrs = append(attrs, "response_bytes", st.responseBytes())
		if u, ok := st.usage(); ok {
			attrs = append(attrs, "usage_source", "provider", usageAttr(u))
		} else {
			attrs = append(attrs, "usage_source", "none")
		}
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

func (st *reqState) usage() (family.Usage, bool) {
	switch {
	case st.streamBody != nil:
		return st.streamBody.reader.Usage()
	case st.teeBody != nil:
		return st.teeBody.usage()
	}
	return family.Usage{}, false
}

func usageAttr(u family.Usage) slog.Attr {
	attrs := []any{
		slog.Int64("input_tokens", u.InputTokens),
		slog.Int64("output_tokens", u.OutputTokens),
		slog.Int64("cache_read_tokens", u.CacheReadTokens),
		slog.Int64("cache_write_tokens", u.CacheWriteTokens),
		slog.Int64("reasoning_tokens", u.ReasoningTokens),
	}
	names := make([]string, 0, len(u.Extra))
	for k := range u.Extra {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if u.Extra[k] != 0 {
			attrs = append(attrs, slog.Int64(k, u.Extra[k]))
		}
	}
	return slog.Group("usage", attrs...)
}
