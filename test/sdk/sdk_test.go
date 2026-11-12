// Package sdk runs the official OpenAI Go SDK against an in-process Pandokos.
package sdk

import (
	"context"
	"errors"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/pandokos/pandokos/internal/config"
	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/server"
	"github.com/pandokos/pandokos/internal/telemetry"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/testutil"
)

func start(t *testing.T) (baseURL string, up *fakeprovider.Server) {
	t.Helper()
	up = fakeprovider.New(t)
	cfg := config.Defaults()
	cfg.Listeners.Inference.Address = "127.0.0.1:0"
	u, err := url.Parse(up.BaseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Skeleton.OpenAI.BaseURL = up.BaseURL()
	cfg.Skeleton.OpenAI.ParsedBaseURL = u
	cfg.Skeleton.AccessToken = secret.New(fixtures.CanaryAccessToken)
	cfg.Skeleton.OpenAI.APIKey = secret.New(fixtures.CanaryProviderKey)
	srv, err := server.New(cfg, telemetry.NewLogger(io.Discard, "info"))
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
	return "http://" + srv.InferenceAddr().String() + "/openai/v1", up
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
	client := newClient(base, fixtures.CanaryAccessToken)
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
	client := newClient(base, fixtures.CanaryAccessToken)
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
	client := newClient(base, fixtures.CanaryAccessToken)
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
		client := newClient(base, fixtures.CanaryAccessToken)
		_, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelError429))
		var apiErr *openai.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
			t.Fatalf("got %v", err)
		}
	})
}

func TestCancelStream(t *testing.T) {
	base, up := start(t)
	client := newClient(base, fixtures.CanaryAccessToken)
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
