package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/openai"
	"github.com/pandokos/pandokos/internal/secret"
)

// ErrShutdown is the cancellation cause of the base context of a server whose drain deadline
// passed. The gateway reports requests cancelled this way as shutdown_aborted.
var ErrShutdown = errors.New("forced shutdown")

const (
	routePrefix = "/openai/v1"
	routeLabel  = "POST /openai/v1/chat/completions"
)

// Options configures a Handler. The gateway receives plain values, not the configuration.
type Options struct {
	Logger                 *slog.Logger      // required
	BaseURL                *url.URL          // provider base URL, path without trailing slash
	ProviderKey            secret.Secret     // required
	AccessToken            secret.Secret     // required
	MaxRequestBodyBytes    int64             // must be positive
	RequestBodyReadTimeout time.Duration     // must be positive
	MaxRequestDuration     time.Duration     // must be positive
	Transport              http.RoundTripper // required; production value from NewTransport
}

// Handler is the inference handler.
type Handler struct {
	logger      *slog.Logger
	baseURL     *url.URL
	providerKey secret.Secret
	accessToken secret.Secret
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
	case o.BaseURL == nil:
		return nil, errors.New("gateway: BaseURL is required")
	case o.ProviderKey.IsZero():
		return nil, errors.New("gateway: ProviderKey is required")
	case o.AccessToken.IsZero():
		return nil, errors.New("gateway: AccessToken is required")
	case o.Transport == nil:
		return nil, errors.New("gateway: Transport is required")
	case o.MaxRequestBodyBytes <= 0:
		return nil, errors.New("gateway: MaxRequestBodyBytes must be positive")
	case o.RequestBodyReadTimeout <= 0:
		return nil, errors.New("gateway: RequestBodyReadTimeout must be positive")
	case o.MaxRequestDuration <= 0:
		return nil, errors.New("gateway: MaxRequestDuration must be positive")
	}
	h := &Handler{
		logger:      o.Logger,
		baseURL:     o.BaseURL,
		providerKey: o.ProviderKey,
		accessToken: o.AccessToken,
		maxBody:     o.MaxRequestBodyBytes,
		readTimeout: o.RequestBodyReadTimeout,
		maxDuration: o.MaxRequestDuration,
	}
	h.proxy = h.newProxy(o.Transport)
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

	matched bool
	method  string

	bodyRead     bool
	stream       bool
	strip        bool
	requestBytes int

	errCode     family.ErrorCode
	clientError bool

	answered       bool
	upstreamStatus int
	ttfb           time.Duration

	streamBody *streamBody
	teeBody    *teeBody
	countBody  *countBody
}

func stateOf(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateKey{}).(*reqState)
	return st
}

func (h *Handler) fail(st *reqState, code family.ErrorCode) {
	openai.WriteError(st.sw, code)
	st.errCode = code
}

// ServeHTTP handles one request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	st := &reqState{start: time.Now(), id: NewRequestID(), callerCx: r.Context(), method: logMethod(r.Method)}
	st.sw = &statusWriter{ResponseWriter: w}
	w.Header().Set(family.HeaderRequestID, st.id)
	defer func() { h.finish(st, recover()) }()

	rest, underPrefix := strings.CutPrefix(r.URL.Path, routePrefix)
	if _, ok := openai.Match(r.Method, rest); !underPrefix || !ok {
		h.fail(st, family.CodeEndpointNotSupported)
		return
	}
	st.matched = true

	cred, ok := openai.InboundCredential(r.Header)
	if !ok || !h.accessToken.Equal(cred) {
		h.fail(st, family.CodeUnauthenticated)
		return
	}

	if r.ContentLength > h.maxBody {
		h.fail(st, family.CodeRequestTooLarge)
		return
	}
	body, err := h.readBody(w, r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.fail(st, family.CodeRequestTooLarge)
			return
		}
		st.clientError = true
		panic(http.ErrAbortHandler)
	}

	prepared, stream, strip := openai.PrepareRequest(body)
	st.bodyRead, st.stream, st.strip, st.requestBytes = true, stream, strip, len(body)

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
	attrs = append(attrs, "duration_ms", time.Since(st.start).Milliseconds())
	if st.matched {
		attrs = append(attrs, "provider", openai.Name, "operation", openai.OperationChatCompletions)
	}
	if st.bodyRead {
		attrs = append(attrs, "stream", st.stream, "usage_injected", st.strip)
	}
	if st.answered {
		attrs = append(attrs, "upstream_status", st.upstreamStatus, "ttfb_ms", st.ttfb.Milliseconds())
	}
	if st.bodyRead {
		attrs = append(attrs, "request_bytes", st.requestBytes)
	}
	if st.answered {
		attrs = append(attrs, "response_bytes", st.responseBytes())
	}
	if st.bodyRead {
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
		return routeLabel
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
