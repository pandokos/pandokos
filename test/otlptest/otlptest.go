// Package otlptest is an OTLP/HTTP trace collector on loopback for tests: it decodes
// ExportTraceServiceRequest bodies and keeps the spans and the raw requests.
package otlptest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Request is one export the collector received.
type Request struct {
	Header http.Header
	Body   []byte
	Path   string
}

// Collector is the test collector.
type Collector struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	spans    []*tracepb.Span
	resource []*tracepb.ResourceSpans
	stall    chan struct{}
	fail     int
}

// New starts a collector; it is stopped when the test ends.
func New(tb testing.TB) *Collector {
	tb.Helper()
	c := &Collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(c.handle))
	tb.Cleanup(c.srv.Close)
	return c
}

// URL returns the base URL of the collector (the exporter appends /v1/traces).
func (c *Collector) URL() *url.URL {
	u, err := url.Parse(c.srv.URL)
	if err != nil {
		panic(err)
	}
	return u
}

// Spans returns the spans received so far, in order.
func (c *Collector) Spans() []*tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*tracepb.Span(nil), c.spans...)
}

// ResourceSpans returns the resource blocks received so far, in order.
func (c *Collector) ResourceSpans() []*tracepb.ResourceSpans {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*tracepb.ResourceSpans(nil), c.resource...)
}

// Requests returns the exports received so far with their headers and raw bodies.
func (c *Collector) Requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Request(nil), c.requests...)
}

// Stall makes every export wait until Release is called or the client gives up.
func (c *Collector) Stall() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stall == nil {
		c.stall = make(chan struct{})
	}
}

// Release ends a Stall.
func (c *Collector) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stall != nil {
		close(c.stall)
		c.stall = nil
	}
}

// Fail makes the next n exports answer 503.
func (c *Collector) Fail(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = n
}

// Close stops the collector now, closing open connections.
func (c *Collector) Close() {
	c.Release()
	c.srv.CloseClientConnections()
	c.srv.Close()
}

func (c *Collector) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	stall := c.stall
	failing := c.fail > 0
	if failing {
		c.fail--
	}
	c.requests = append(c.requests, Request{Header: r.Header.Clone(), Body: body, Path: r.URL.Path})
	c.mu.Unlock()
	if stall != nil {
		select {
		case <-stall:
		case <-r.Context().Done():
			return
		}
	}
	if failing {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var req collectorpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	for _, rs := range req.GetResourceSpans() {
		c.resource = append(c.resource, rs)
		for _, ss := range rs.GetScopeSpans() {
			c.spans = append(c.spans, ss.GetSpans()...)
		}
	}
	c.mu.Unlock()
	out, _ := proto.Marshal(&collectorpb.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}
