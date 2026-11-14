// Package sdk runs the official OpenAI Go SDK against an in-process Pandokos.
package sdk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/pandokos/pandokos/internal/authn"
	"github.com/pandokos/pandokos/internal/authz"
	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/server"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

// testKey is generated when the tests start; the static authenticator knows it.
var testKey = func() string {
	k, _, _, err := authn.Generate(authn.KindTenant)
	if err != nil {
		panic(err)
	}
	return k.Reveal()
}()

func start(t *testing.T) (baseURL string, up *fakeprovider.Server) {
	t.Helper()
	baseURL, up, _ = startRecording(t)
	return baseURL, up
}

// startRecording is start, and also returns the recorder that receives the usage records.
func startRecording(t *testing.T) (baseURL string, up *fakeprovider.Server, rec *testutil.MemoryRecorder) {
	t.Helper()
	up = fakeprovider.New(t)
	rec = &testutil.MemoryRecorder{}
	tp, _ := testutil.SpanRecorder(t)
	cfg := config.Defaults()
	cfg.Listeners.Inference.Address = "127.0.0.1:0"
	cfg.Listeners.Admin.Address = "127.0.0.1:0"
	targets := map[string]gateway.Target{}
	for _, m := range []string{
		fakeprovider.ModelBasic, fakeprovider.ModelDetails, fakeprovider.ModelGatedStream, fakeprovider.ModelHang,
		fakeprovider.ModelError429, fakeprovider.ModelError500Echo, fakeprovider.ModelUsageOnLastChunk,
	} {
		targets["openai/"+m] = testutil.TargetFor(up, "openai", m)
	}
	srv, err := server.New(server.Options{
		Config: cfg, Logger: telemetry.NewLogger(io.Discard, "info"), Admin: http.NotFoundHandler(),
		Authenticator: testutil.StaticAuthenticator(map[string]authz.Principal{testKey: testutil.InferencePrincipal()}),
		Catalogue:     testutil.StaticCatalogue(map[string]string{"openai": "openai"}, targets),
		Credentials:   testutil.StaticCredentials(map[string]string{"openai": fixtures.CanaryProviderKey}),
		Egress:        testutil.TestGuard(t),
		Recorder:      rec,
		Tracer:        tp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	testutil.Eventually(t, 5*time.Second, "readiness", func() bool { return srv.Health().Ready(context.Background()) })
	return "http://" + srv.InferenceAddr().String() + "/openai/v1", up, rec
}

func newClient(baseURL, key string) openai.Client {
	return openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(key),
		option.WithMaxRetries(0),
	)
}

func ctx10(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func params(model string) openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{
		Model:    model,
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(fixtures.CanaryPrompt)},
	}
}

func TestChatCompletion(t *testing.T) {
	base, _ := start(t)
	client := newClient(base, testKey)
	completion, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelBasic))
	if err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 1 || completion.Choices[0].Message.Content != fixtures.CanaryCompletion {
		t.Fatalf("choices: %+v", completion.Choices)
	}
	if completion.Usage.PromptTokens != 9 || completion.Usage.CompletionTokens != 12 || completion.Usage.TotalTokens != 21 {
		t.Fatalf("usage: %+v", completion.Usage)
	}
}

func TestChatCompletionStream(t *testing.T) {
	base, _ := start(t)
	client := newClient(base, testKey)
	stream := client.Chat.Completions.NewStreaming(ctx10(t), params(fakeprovider.ModelBasic))
	acc := openai.ChatCompletionAccumulator{}
	n := 0
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) != 1 {
			t.Fatalf("chunk %d has %d choices: the usage-only event was not removed", n, len(chunk.Choices))
		}
		acc.AddChunk(chunk)
		n++
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("got %d chunks, want 4", n)
	}
	if got := acc.Choices[0].Message.Content; got != fixtures.CanaryCompletion+" end" {
		t.Fatalf("content %q", got)
	}
}

func TestChatCompletionStreamIncludeUsage(t *testing.T) {
	base, _ := start(t)
	client := newClient(base, testKey)
	p := params(fakeprovider.ModelBasic)
	p.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	stream := client.Chat.Completions.NewStreaming(ctx10(t), p)
	var last openai.ChatCompletionChunk
	n := 0
	for stream.Next() {
		last = stream.Current()
		n++
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 5 || len(last.Choices) != 0 || last.Usage.TotalTokens != 21 {
		t.Fatalf("chunks %d, last %+v", n, last)
	}
}

func TestErrorsAreSDKErrors(t *testing.T) {
	base, _ := start(t)
	t.Run("wrong key", func(t *testing.T) {
		client := newClient(base, "not-the-token")
		_, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelBasic))
		var apiErr *openai.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Code != "unauthenticated" {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("provider 429", func(t *testing.T) {
		client := newClient(base, testKey)
		_, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelError429))
		var apiErr *openai.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
			t.Fatalf("got %v", err)
		}
	})
}

func TestCancelStream(t *testing.T) {
	base, up := start(t)
	client := newClient(base, testKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := client.Chat.Completions.NewStreaming(ctx, params(fakeprovider.ModelGatedStream))
	if !stream.Next() {
		t.Fatalf("no first chunk: %v", stream.Err())
	}
	cancel()
	if !up.WaitCancelled(2 * time.Second) {
		t.Fatal("the provider request was not cancelled")
	}
	_ = stream.Close()
}

// oneRecord waits for the recorder to hold one record and returns it.
func oneRecord(t *testing.T, rec *testutil.MemoryRecorder) usage.Record {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, "the usage record", func() bool { return len(rec.Records()) >= 1 })
	recs := rec.Records()
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1", len(recs))
	}
	return recs[0]
}

// TestResponses is T-513: the official SDK calls the responses operation with only the base URL
// and the key changed; one record with provider usage.
func TestResponses(t *testing.T) {
	base, up, rec := startRecording(t)
	client := newClient(base, testKey)
	resp, err := client.Responses.New(ctx10(t), responses.ResponseNewParams{
		Model: fakeprovider.ModelBasic,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(fixtures.CanaryPrompt)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.OutputText(); got != fixtures.CanaryCompletion+" end" {
		t.Errorf("output %q", got)
	}
	if resp.Usage.InputTokens != 20 || resp.Usage.OutputTokens != 15 || resp.Usage.TotalTokens != 35 {
		t.Errorf("usage %+v", resp.Usage)
	}
	r := oneRecord(t, rec)
	if r.Operation != "responses" || r.Outcome != usage.OutcomeOK || r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 20 || r.Usage.OutputTokens != 15 ||
		r.Usage.CacheReadTokens != 5 || r.Usage.ReasoningTokens != 4 {
		t.Errorf("record %+v", r)
	}
	if reqs := up.Requests(); len(reqs) != 1 || reqs[0].Path != "/v1/responses" {
		t.Errorf("the provider saw %+v", reqs)
	}
}

// TestResponsesStream is T-513 for a streamed answer.
func TestResponsesStream(t *testing.T) {
	base, _, rec := startRecording(t)
	client := newClient(base, testKey)
	stream := client.Responses.NewStreaming(ctx10(t), responses.ResponseNewParams{
		Model: fakeprovider.ModelBasic,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(fixtures.CanaryPrompt)},
	})
	var text strings.Builder
	var completed bool
	var usageSeen int64
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
		case "response.completed":
			completed = true
			usageSeen = ev.Response.Usage.InputTokens
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !completed || text.String() != fixtures.CanaryCompletion+" end" || usageSeen != 20 {
		t.Errorf("completed %v text %q usage %d", completed, text.String(), usageSeen)
	}
	r := oneRecord(t, rec)
	if r.Operation != "responses" || r.Stream == nil || !*r.Stream || r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 20 || r.Usage.OutputTokens != 15 {
		t.Errorf("record %+v", r)
	}
}

// TestEmbeddings is T-513 for the embeddings operation.
func TestEmbeddings(t *testing.T) {
	base, _, rec := startRecording(t)
	client := newClient(base, testKey)
	resp, err := client.Embeddings.New(ctx10(t), openai.EmbeddingNewParams{
		Model: fakeprovider.ModelBasic,
		Input: openai.EmbeddingNewParamsInputUnion{OfString: openai.String(fixtures.CanaryPrompt)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Embedding) != 3 || resp.Usage.PromptTokens != 8 {
		t.Errorf("response %+v", resp)
	}
	r := oneRecord(t, rec)
	if r.Operation != "embeddings" || r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 8 || r.Usage.OutputTokens != 0 || r.Stream == nil || *r.Stream {
		t.Errorf("record %+v", r)
	}
}

// TestMetadataAndTraceHeaders is T-514: headers set through the SDK's request options arrive as
// metadata and trace context on the record and do not reach the provider.
func TestMetadataAndTraceHeaders(t *testing.T) {
	base, up, rec := startRecording(t)
	client := newClient(base, testKey)
	const trace = "4bf92f3577b34da6a3ce929d0e0e4736"
	_, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelBasic),
		option.WithHeader("X-Pandokos-Metadata-Team", "alpha"),
		option.WithHeader("X-Pandokos-Metadata-Job", "nightly-7"),
		option.WithHeader("Traceparent", "00-"+trace+"-00f067aa0ba902b7-01"),
		option.WithHeader("Tracestate", "vendor=value"),
	)
	if err != nil {
		t.Fatal(err)
	}
	r := oneRecord(t, rec)
	if len(r.Metadata) != 2 || r.Metadata["team"] != "alpha" || r.Metadata["job"] != "nightly-7" || r.MetadataDropped != 0 {
		t.Errorf("metadata %v dropped %d", r.Metadata, r.MetadataDropped)
	}
	if r.TraceID != trace {
		t.Errorf("trace ID %q, want %s", r.TraceID, trace)
	}
	reqs := up.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d provider requests", len(reqs))
	}
	for name := range reqs[0].Header {
		if strings.HasPrefix(strings.ToLower(name), "x-pandokos") || strings.EqualFold(name, "Traceparent") || strings.EqualFold(name, "Tracestate") {
			t.Errorf("the provider received %s", name)
		}
	}
}
