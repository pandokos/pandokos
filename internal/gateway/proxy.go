package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/pandokos/pandokos/internal/family"
)

// maxUpstreamRequestID is the longest provider request ID that is logged.
const maxUpstreamRequestID = 128

// errRedirect is returned by modifyResponse for a 3xx: it is neither followed nor passed on.
var errRedirect = errors.New("gateway: upstream redirect")

// blocked is what the gateway recognizes of a refusal of the egress guard; it does not import the
// guard.
type blocked interface {
	BlockedReason() string
	BlockedAddr() string
}

// classTransport sends a request through the round tripper of its endpoint's class. A request
// without state takes the public one, the stricter of the two.
type classTransport struct{ public, private http.RoundTripper }

func (t *classTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if st := stateOf(r.Context()); st != nil && st.target.Private {
		return t.private.RoundTrip(r)
	}
	return t.public.RoundTrip(r)
}

func (h *Handler) newProxy(transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:        h.rewrite,
		Transport:      transport,
		FlushInterval:  -1,
		ModifyResponse: h.modifyResponse,
		ErrorHandler:   h.errorHandler,
		ErrorLog:       slog.NewLogLogger(h.logger.Handler(), slog.LevelWarn),
	}
}

// connectionTokens returns the header names listed in the Connection headers, canonicalized.
func connectionTokens(h http.Header) map[string]bool {
	out := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				out[http.CanonicalHeaderKey(tok)] = true
			}
		}
	}
	return out
}

// rewrite builds the outbound request: a URL made of catalogue values only, and a header built
// from an empty one (sprint.md section 5.3).
func (h *Handler) rewrite(pr *httputil.ProxyRequest) {
	st := stateOf(pr.In.Context())
	base := st.target.BaseURL
	u := *base
	u.Path = base.Path + st.op.Path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	pr.Out.URL = &u
	pr.Out.Method = st.op.Method
	pr.Out.Host = base.Host

	named := connectionTokens(pr.In.Header)
	hdr := http.Header{}
	for _, name := range st.adapter.RequestHeaders() {
		name = http.CanonicalHeaderKey(name)
		if named[name] {
			continue
		}
		for _, v := range pr.In.Header.Values(name) {
			hdr.Add(name, v)
		}
	}
	st.adapter.AttachCredential(hdr, st.credential.Reveal())
	pr.Out.Header = hdr
}

func (h *Handler) modifyResponse(resp *http.Response) error {
	st := stateOf(resp.Request.Context())
	if st == nil {
		return nil
	}
	st.upstreamStatus = resp.StatusCode
	if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
		_ = resp.Body.Close()
		return errRedirect
	}
	st.answered = true
	st.ttfb = time.Since(st.start)
	if id := resp.Header.Get("X-Request-Id"); id != "" && len(id) <= maxUpstreamRequestID && printableASCII(id) {
		st.upstreamRequestID = id
	}

	allowed := http.Header{}
	for name, values := range resp.Header {
		if st.adapter.AllowResponseHeader(http.CanonicalHeaderKey(name)) {
			allowed[name] = values
		}
	}
	resp.Header = allowed
	resp.Trailer = nil // provider trailers are dropped; the wrappers drop them again at the end of the body

	contentType := resp.Header.Get("Content-Type")
	switch {
	case resp.StatusCode == http.StatusOK && strings.HasPrefix(contentType, "text/event-stream"):
		st.streamBody = newStreamBody(resp, st.adapter.NewStreamReader(st.op, st.strip))
		resp.Body = st.streamBody
		if st.strip {
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1
		}
	case resp.StatusCode == http.StatusOK && strings.HasPrefix(contentType, "application/json"):
		st.teeBody = &teeBody{src: resp.Body, trailers: trailerGuard{resp}, adapter: st.adapter, op: st.op}
		resp.Body = st.teeBody
	default:
		st.countBody = &countBody{src: resp.Body, trailers: trailerGuard{resp}}
		resp.Body = st.countBody
	}
	return nil
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// errorHandler runs when the provider did not answer, or answered with a redirect. Nothing of err
// reaches the caller or the log: it can quote a URL.
func (h *Handler) errorHandler(_ http.ResponseWriter, r *http.Request, err error) {
	st := stateOf(r.Context())
	if st == nil {
		return
	}
	var b blocked
	switch {
	case st.callerCx.Err() != nil:
		// The caller is gone: nothing to write, and the connection is aborted so that net/http
		// does not send an empty 200.
		panic(http.ErrAbortHandler)
	case errors.As(err, &b):
		st.upstreamErr, st.egressReason = "egress", b.BlockedReason()
		attrs := []any{"request_id", st.id, "provider", st.provider, "endpoint", st.target.Endpoint, "reason", b.BlockedReason()}
		if addr := b.BlockedAddr(); addr != "" {
			attrs = append(attrs, "address", addr)
		}
		h.logger.Warn("egress blocked", attrs...)
		h.fail(st, family.CodeUpstreamUnreachable)
	case errors.Is(err, errRedirect):
		st.upstreamErr = "redirect"
		h.fail(st, family.CodeUpstreamUnreachable)
	case errors.Is(st.outCx.Err(), context.DeadlineExceeded):
		st.upstreamErr = "timeout"
		h.fail(st, family.CodeUpstreamTimeout)
	default:
		st.upstreamErr = "connect"
		h.fail(st, family.CodeUpstreamUnreachable)
	}
}
