package gateway_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fakeprovider"
	"github.com/pandokos/pandokos/test/fixtures"
)

// TestStreamUsageOption is T-538: a provider whose stream usage option is omit gets the body as
// received, a stream is forwarded in observe mode and recorded with estimated usage unless the
// server reports usage anyway; the same request to an add provider gets the provider's own 400.
func TestStreamUsageOption(t *testing.T) {
	t.Parallel()
	compatChat := compatOp(opChat)
	openaiChat := opChat

	t.Run("omit, a server that refuses the option: it is never sent", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", false)
		body := compatChat.body(fakeprovider.ModelCompatRefusesOption, true)
		c := h.sendAuthed(t, compatChat.path, body)
		if c.status != 200 || !bytes.Equal(c.body, fixtures.ReadCompat("chat_stream_no_usage.sse")) {
			t.Fatalf("%d %s", c.status, c.body)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
			t.Fatalf("the provider received %q, want the caller's bytes", reqs[0].Body)
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated || r.Outcome != usage.OutcomeOK {
			t.Errorf("record %q %q %q", r.Outcome, r.InputSource, r.OutputSource)
		}
		if want := contentEvents(opChat, c.body); r.Estimated.OutputTokens != want || want == 0 {
			t.Errorf("estimated output %d, want the %d content events delivered", r.Estimated.OutputTokens, want)
		}
		if line := h.lastRequestLine(t); line["usage_injected"] != false {
			t.Errorf("request line %v", line)
		}
	})

	t.Run("omit, a server that ignores the option: estimated", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", false)
		body := compatChat.body(fakeprovider.ModelCompatIgnoresOption, true)
		c := h.sendAuthed(t, compatChat.path, body)
		if c.status != 200 || !bytes.Equal(c.body, fixtures.ReadCompat("chat_stream_no_usage.sse")) {
			t.Fatalf("%d %s", c.status, c.body)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
			t.Fatalf("the provider received %q", reqs[0].Body)
		}
		if r := h.oneRecord(t); r.InputSource != usage.SourceEstimated || r.OutputSource != usage.SourceEstimated {
			t.Errorf("sources %q %q", r.InputSource, r.OutputSource)
		}
	})

	t.Run("omit, a server that reports usage on its last chunk anyway: the reader takes it", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", false)
		body := compatChat.body(fakeprovider.ModelCompatUsageLastChunk, true)
		c := h.sendAuthed(t, compatChat.path, body)
		if c.status != 200 || !bytes.Equal(c.body, fixtures.ReadCompat("chat_stream_usage_last_content_chunk.sse")) {
			t.Fatalf("%d %s", c.status, c.body)
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceProvider || r.OutputSource != usage.SourceProvider || r.Usage.InputTokens != 9 || r.Usage.OutputTokens != 12 {
			t.Errorf("record %q %q usage %+v", r.InputSource, r.OutputSource, r.Usage)
		}
	})

	t.Run("omit, the caller asks for usage itself: unaffected, and nothing is stripped", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", false)
		body := []byte(`{"model":"` + fakeprovider.ModelCompatExtraUsage + `","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]}`)
		c := h.sendAuthed(t, compatChat.path, body)
		if c.status != 200 || !bytes.Equal(c.body, fixtures.ReadCompat("chat_stream_extra_usage.sse")) {
			t.Fatalf("%d %s", c.status, c.body)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, body) {
			t.Fatalf("the provider received %q", reqs[0].Body)
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 9 || r.Usage.Reported["prompt_cache_hit_tokens"] != 3 {
			t.Errorf("record %q usage %+v", r.InputSource, r.Usage)
		}
	})

	t.Run("omit with a model alias: the replacement still applies, nothing else changes", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", false)
		tgt := h.targets["compat/"+fakeprovider.ModelCompatRefusesOption]
		tgt.Model = "synthetic-alias-2"
		h.targets["compat/synthetic-alias-2"] = tgt
		body := compatChat.body("synthetic-alias-2", true)
		if c := h.sendAuthed(t, compatChat.path, body); c.status != 200 {
			t.Fatalf("%d %s", c.status, c.body)
		}
		want := strings.Replace(string(body), `"model":"synthetic-alias-2"`, `"model":"`+fakeprovider.ModelCompatRefusesOption+`"`, 1)
		if reqs := h.up.Requests(); len(reqs) != 1 || string(reqs[0].Body) != want {
			t.Fatalf("the provider received %q, want %q", reqs[0].Body, want)
		}
	})

	t.Run("add: the same request gets the provider's 400 unchanged", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", true)
		body := compatChat.body(fakeprovider.ModelCompatRefusesOption, true)
		c := h.sendAuthed(t, compatChat.path, body)
		if c.status != 400 || !bytes.Equal(c.body, fixtures.ReadCompat("error_400_stream_options.json")) || c.header.Get("X-Pandokos-Error") != "" {
			t.Fatalf("%d %s %v", c.status, c.body, c.header)
		}
		if reqs := h.up.Requests(); len(reqs) != 1 || !strings.Contains(string(reqs[0].Body), `"stream_options":{"include_usage":true}`) {
			t.Fatalf("the provider received %q: the option was not added", reqs[0].Body)
		}
		r := h.oneRecord(t)
		if r.Outcome != usage.OutcomeUpstreamError || r.UpstreamStatus != 400 || r.InputSource != usage.SourceNone {
			t.Errorf("record %q status %d sources %q", r.Outcome, r.UpstreamStatus, r.InputSource)
		}
		if line := h.lastRequestLine(t); line["usage_injected"] != true {
			t.Errorf("usage_injected %v", line["usage_injected"])
		}
	})

	t.Run("add: the usage-only chunk is stripped for a caller that did not ask for it", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", true)
		body := compatChat.body(fakeprovider.ModelCompatExtraUsage, true)
		c := h.sendAuthed(t, compatChat.path, body)
		if c.status != 200 || strings.Contains(string(c.body), `"queue_time"`) || bytes.Equal(c.body, fixtures.ReadCompat("chat_stream_extra_usage.sse")) {
			t.Fatalf("the usage-only chunk was not removed: %d %s", c.status, c.body)
		}
		r := h.oneRecord(t)
		if r.InputSource != usage.SourceProvider || r.Usage.InputTokens != 9 {
			t.Errorf("record %q usage %+v", r.InputSource, r.Usage)
		}
	})

	t.Run("the openai provider is an add provider", func(t *testing.T) {
		h := newFamilies(t, nil)
		body := openaiChat.body("fake-basic", true)
		h.sendAuthed(t, openaiChat.path, body)
		reqs := h.up.Requests()
		if len(reqs) != 1 || !strings.Contains(string(reqs[0].Body), `"stream_options":{"include_usage":true}`) {
			t.Fatalf("the provider received %q", reqs[0].Body)
		}
	})

	t.Run("a change of the option is seen by the next request", func(t *testing.T) {
		h := newFamilies(t, nil)
		addCompat(h, "", true)
		body := compatChat.body(fakeprovider.ModelCompatIgnoresOption, true)
		h.sendAuthed(t, compatChat.path, body)
		for _, k := range []string{fakeprovider.ModelCompatIgnoresOption} {
			tgt := h.targets["compat/"+k]
			tgt.StreamUsage = false
			h.targets["compat/"+k] = tgt
		}
		h.sendAuthed(t, compatChat.path, body)
		reqs := h.up.Requests()
		if len(reqs) != 2 || !strings.Contains(string(reqs[0].Body), "include_usage") || strings.Contains(string(reqs[1].Body), "stream_options") {
			t.Fatalf("the provider received %q then %q", reqs[0].Body, reqs[1].Body)
		}
	})
}
