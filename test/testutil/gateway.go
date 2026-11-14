package testutil

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/egress"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/pricing"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
)

// Catalogue is a gateway.Catalogue for in-process tests: a fixed list of providers and targets.
type Catalogue struct {
	providers map[string]string
	targets   map[string]gateway.Target

	// ProviderErr, when set, is the error of Provider; ResolveErr likewise for Resolve.
	ProviderErr, ResolveErr error
	// Block, when not nil, makes Resolve wait for it to be closed or for the context to end.
	Block chan struct{}
	// Providers and Resolves count the calls.
	Providers, Resolves atomic.Int64
}

// StaticCatalogue returns a Catalogue. providers maps a provider name to its API family; targets
// maps "provider/model" to the target of that model. A model that is not in targets is
// gateway.ErrModelNotAllowed.
func StaticCatalogue(providers map[string]string, targets map[string]gateway.Target) *Catalogue {
	return &Catalogue{providers: providers, targets: targets}
}

// Provider implements gateway.Catalogue.
func (c *Catalogue) Provider(_ context.Context, name string) (string, bool, error) {
	c.Providers.Add(1)
	if c.ProviderErr != nil {
		return "", false, c.ProviderErr
	}
	fam, ok := c.providers[name]
	return fam, ok, nil
}

// Resolve implements gateway.Catalogue.
func (c *Catalogue) Resolve(ctx context.Context, _ authz.Principal, provider, model string) (gateway.Target, error) {
	c.Resolves.Add(1)
	if c.Block != nil {
		select {
		case <-c.Block:
		case <-ctx.Done():
			return gateway.Target{}, ctx.Err()
		}
	}
	if c.ResolveErr != nil {
		return gateway.Target{}, c.ResolveErr
	}
	t, ok := c.targets[provider+"/"+model]
	if !ok {
		return gateway.Target{}, gateway.ErrModelNotAllowed
	}
	return t, nil
}

// CredentialCall is one call of Credentials.Open.
type CredentialCall struct{ RequestID, Provider string }

// Credentials is a gateway.Credentials for in-process tests.
type Credentials struct {
	secrets map[string]string
	// Err, when set, is the error of Open.
	Err error
	// Pinned is the value of Pinned of the credentials Open returns.
	Pinned bool

	mu    sync.Mutex
	calls []CredentialCall
}

// StaticCredentials returns a Credentials that holds one secret per provider. A provider that is
// not in secrets is gateway.ErrNoCredential.
func StaticCredentials(secrets map[string]string) *Credentials {
	return &Credentials{secrets: secrets}
}

// Open implements gateway.Credentials.
func (c *Credentials) Open(_ context.Context, _ authz.Principal, requestID, provider string) (gateway.Credential, error) {
	c.mu.Lock()
	c.calls = append(c.calls, CredentialCall{RequestID: requestID, Provider: provider})
	c.mu.Unlock()
	if c.Err != nil {
		return gateway.Credential{}, c.Err
	}
	s, ok := c.secrets[provider]
	if !ok {
		return gateway.Credential{}, gateway.ErrNoCredential
	}
	return gateway.Credential{ID: "synthetic-credential-" + provider, Pinned: c.Pinned, Secret: secret.New(s)}, nil
}

// Calls returns the calls of Open, in order.
func (c *Credentials) Calls() []CredentialCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CredentialCall(nil), c.calls...)
}

// TargetFor returns the target of a model served by the fake provider: a private endpoint at the
// fake's address, an upstream identifier equal to the name and identifiers that start with
// "synthetic-".
func TargetFor(up *fakeprovider.Server, provider, model string) gateway.Target {
	u, err := url.Parse(up.URL())
	if err != nil {
		panic(err)
	}
	return gateway.Target{
		Provider: provider, Endpoint: "synthetic-endpoint",
		ModelID: "synthetic-model-" + provider + "-" + model, Model: model, UpstreamModel: model,
		BaseURL: u, Private: true,
		Prices: []pricing.Version{syntheticPrice("synthetic-price-"+provider+"-"+model, "synthetic-model-"+provider+"-"+model,
			time.Now().Add(-time.Hour), "3", "6")},
	}
}

// syntheticPrice returns a USD price version with an input and an output rate (per million tokens).
func syntheticPrice(id, modelID string, validFrom time.Time, input, output string) pricing.Version {
	in, err := pricing.ParseDecimal(input)
	if err != nil {
		panic(err)
	}
	out, err := pricing.ParseDecimal(output)
	if err != nil {
		panic(err)
	}
	return pricing.Version{
		ID: id, ModelID: modelID, ValidFrom: validFrom, Currency: "USD", Source: "override",
		Rates: []pricing.Rate{
			{Category: pricing.CategoryInput, PerMillion: in},
			{Category: pricing.CategoryOutput, PerMillion: out},
		},
	}
}

// TwoPrices returns two versions of a price, "synthetic-price-before" valid from before (input 1,
// output 2 per million) and "synthetic-price-after" valid from after (input 10, output 20), for a
// test of the version in force at the start of a request.
func TwoPrices(before, after time.Time) []pricing.Version {
	return []pricing.Version{
		syntheticPrice("synthetic-price-before", "synthetic-model", before, "1", "2"),
		syntheticPrice("synthetic-price-after", "synthetic-model", after, "10", "20"),
	}
}

// TestGuard returns a real egress guard: connect timeout 2 s, the system roots, no proxy. Idle
// connections are closed when the test ends. An option function may change the options (for
// example RootCAs, to trust a TLS fake).
func TestGuard(tb testing.TB, opts ...func(*egress.Options)) *egress.Guard {
	tb.Helper()
	o := egress.Options{ConnectTimeout: 2 * time.Second}
	for _, f := range opts {
		f(&o)
	}
	g, err := egress.New(o)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(g.CloseIdleConnections)
	return g
}

// ConnectProxy starts an HTTP/1.1 proxy on loopback that answers CONNECT by dialling the target
// and splicing the two connections. It returns its URL and a function that lists the requests it
// received (method, request URI, host and header), in order.
func ConnectProxy(tb testing.TB) (proxyURL *url.URL, requests func() []*http.Request) {
	tb.Helper()
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, &http.Request{Method: r.Method, RequestURI: r.RequestURI, Host: r.Host, Header: r.Header.Clone(), URL: r.URL})
		mu.Unlock()
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		target, err := net.DialTimeout("tcp", r.Host, 2*time.Second)
		if err != nil {
			http.Error(w, "cannot connect", http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			target.Close()
			http.Error(w, "cannot hijack", http.StatusInternalServerError)
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			target.Close()
			return
		}
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(target, client); done <- struct{}{} }()
		go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
		<-done
		client.Close()
		target.Close()
		<-done
	}))
	tb.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		tb.Fatal(err)
	}
	return u, func() []*http.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]*http.Request(nil), seen...)
	}
}

// MemoryRecorder is a gateway.Recorder that keeps the records in memory.
type MemoryRecorder struct {
	// Capacity is the number of admissions; zero means no limit. Set it before use.
	Capacity int

	mu      sync.Mutex
	records []usage.Record
	admits  int
}

// Admit takes a slot while Capacity allows.
func (m *MemoryRecorder) Admit() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Capacity > 0 && m.admits >= m.Capacity {
		return false
	}
	m.admits++
	return true
}

// Record keeps the record.
func (m *MemoryRecorder) Record(r usage.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, r)
}

// Records returns the records received, in order.
func (m *MemoryRecorder) Records() []usage.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]usage.Record(nil), m.records...)
}

// Admits returns the number of successful Admit calls.
func (m *MemoryRecorder) Admits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admits
}

// SpanRecorder returns an SDK tracer provider that samples everything into an in-memory
// recorder; the provider is shut down when the test ends.
func SpanRecorder(tb testing.TB) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	tb.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	tb.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, rec
}
