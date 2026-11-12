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
	"github.com/pandokos/pandokos/internal/family/openai"
)

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

// rewrite builds the outbound request: a fixed URL, and a header built from an empty one.
func (h *Handler) rewrite(pr *httputil.ProxyRequest) {
	u := *h.baseURL
	u.Path = h.baseURL.Path + openai.PathChatCompletions
	u.RawPath = ""
	u.RawQuery = ""
	pr.Out.URL = &u
	pr.Out.Host = h.baseURL.Host

	hdr := http.Header{}
	for _, name := range openai.RequestHeaders {
		for _, v := range pr.In.Header.Values(name) {
			hdr.Add(name, v)
		}
	}
	openai.AttachCredential(hdr, h.providerKey.Reveal())
	pr.Out.Header = hdr
}

func (h *Handler) modifyResponse(resp *http.Response) error {
	st := stateOf(resp.Request.Context())
	if st == nil {
		return nil
	}
	st.answered = true
	st.upstreamStatus = resp.StatusCode
	st.ttfb = time.Since(st.start)

	allowed := http.Header{}
	for name, values := range resp.Header {
		if openai.AllowResponseHeader(http.CanonicalHeaderKey(name)) {
			allowed[name] = values
		}
	}
	resp.Header = allowed

	contentType := resp.Header.Get("Content-Type")
	switch {
	case resp.StatusCode == http.StatusOK && strings.HasPrefix(contentType, "text/event-stream"):
		st.streamBody = newStreamBody(resp.Body, openai.NewStreamReader(st.strip))
		resp.Body = st.streamBody
		if st.strip {
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1
		}
	case resp.StatusCode == http.StatusOK && strings.HasPrefix(contentType, "application/json"):
		st.teeBody = &teeBody{src: resp.Body}
		resp.Body = st.teeBody
	default:
		st.countBody = &countBody{src: resp.Body}
		resp.Body = st.countBody
	}
	return nil
}

// errorHandler runs when the provider did not answer.
func (h *Handler) errorHandler(_ http.ResponseWriter, r *http.Request, _ error) {
	st := stateOf(r.Context())
	if st == nil {
		return
	}
	switch {
	case st.callerCx.Err() != nil:
		// The caller is gone: nothing to write.
	case errors.Is(st.outCx.Err(), context.DeadlineExceeded):
		h.fail(st, family.CodeUpstreamTimeout)
	default:
		h.fail(st, family.CodeUpstreamUnreachable)
	}
}
