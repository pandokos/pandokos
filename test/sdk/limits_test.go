package sdk

import (
	"errors"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/pandokos/pandokos/internal/gateway"
	"github.com/pandokos/pandokos/internal/limits"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
	"github.com/pandokos/pandokos/test/limitstest"
	"github.com/pandokos/pandokos/test/testutil"
)

// startLimited starts the in-process Pandokos with real limits for the caller of testKey.
func startLimited(t *testing.T, rules ...limits.Rule) (baseURL string, up *fakeprovider.Server) {
	t.Helper()
	up = fakeprovider.New(t)
	targets := map[string]gateway.Target{}
	for _, m := range []string{fakeprovider.ModelBasic, fakeprovider.ModelGatedStream} {
		targets["openai/"+m] = testutil.TargetFor(up, "openai", m)
	}
	principal := testutil.InferencePrincipal()
	addr, _ := startServerWith(t, map[string]string{"openai": "openai"}, targets, map[string]string{"openai": fixtures.CanaryProviderKey},
		limitstest.New(t, principal, rules...), principal)
	return "http://" + addr + "/openai/v1", up
}

// TestRateLimitError is T-646: the OpenAI Go SDK raises its error type for the refusal, with the
// status 429 and the Pandokos header readable from it; with the SDK's retries a call refused for
// concurrency succeeds on its own retry, after Retry-After.
func TestRateLimitError(t *testing.T) {
	t.Run("the SDK's error carries the status, the code and the header", func(t *testing.T) {
		base, up := startLimited(t, limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.RequestsPerMinute, Value: 1})
		client := newClient(base, testKey)
		if _, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelBasic)); err != nil {
			t.Fatalf("the first call: %v", err)
		}
		_, err := client.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelBasic))
		var apiErr *openai.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("the error is %T %v, want *openai.Error", err, err)
		}
		if apiErr.StatusCode != 429 || apiErr.Code != "rate_limited" {
			t.Errorf("status %d code %q, want 429 rate_limited", apiErr.StatusCode, apiErr.Code)
		}
		if apiErr.Response == nil || apiErr.Response.Header.Get("X-Pandokos-Error") != "rate_limited" {
			t.Fatalf("X-Pandokos-Error is not readable from the error: %+v", apiErr.Response)
		}
		h := apiErr.Response.Header
		if h.Get("X-Pandokos-Limit-Metric") != "requests_per_minute" || h.Get("Retry-After") == "" {
			t.Errorf("headers %v", h)
		}
		if n := len(up.Requests()); n != 1 {
			t.Errorf("the provider saw %d requests, want 1: the refused call must not reach it", n)
		}
	})

	t.Run("a call refused for concurrency succeeds on the SDK's own retry", func(t *testing.T) {
		base, up := startLimited(t, limits.Rule{SubjectKind: limits.SubjectTenant, Metric: limits.ConcurrentRequests, Value: 1})
		held := newClient(base, testKey)
		stream := held.Chat.Completions.NewStreaming(ctx10(t), params(fakeprovider.ModelGatedStream))
		if !stream.Next() { // the first event: the request is in flight and holds the slot
			t.Fatalf("no first event: %v", stream.Err())
		}
		retrying := openai.NewClient(option.WithBaseURL(base), option.WithAPIKey(testKey), option.WithMaxRetries(2))
		go func() {
			time.Sleep(300 * time.Millisecond)
			up.Release() // the held stream ends; the slot is free when the retry comes
		}()
		start := time.Now()
		completion, err := retrying.Chat.Completions.New(ctx10(t), params(fakeprovider.ModelBasic))
		if err != nil {
			t.Fatalf("the call did not succeed on the retry: %v", err)
		}
		if len(completion.Choices) != 1 || completion.Choices[0].Message.Content != fixtures.CanaryCompletion {
			t.Errorf("choices %+v", completion.Choices)
		}
		if d := time.Since(start); d < 800*time.Millisecond {
			t.Errorf("the retry came after %s: the SDK did not wait for Retry-After (1 s)", d)
		}
		for stream.Next() {
		}
		_ = stream.Close()
		if n := len(up.Requests()); n != 2 {
			t.Errorf("the provider saw %d requests, want 2 (the held stream and the retried call)", n)
		}
	})
}
