package fakeprovider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/test/fixtures"
)

func post(t *testing.T, s *Server, key, body string) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := s.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestFakeProvider(t *testing.T) {
	s := New(t)
	key := fixtures.CanaryProviderKey
	cases := []struct {
		name, body, ctype string
		status            int
	}{
		{"basic", `{"model":"fake-basic"}`, "application/json", 200},
		{"details", `{"model":"fake-details"}`, "application/json", 200},
		{"error429", `{"model":"fake-error-429"}`, "application/json", 429},
		{"error500", `{"model":"fake-error-500-echo"}`, "application/json", 500},
		{"lastchunk", `{"model":"fake-usage-on-last-chunk"}`, "text/event-stream", 200},
		{"unknown", `{"model":"nope"}`, "application/json", 404},
		{"stream", `{"model":"fake-basic","stream":true}`, "text/event-stream", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _ := post(t, s, key, c.body)
			if resp.StatusCode != c.status || resp.Header.Get("Content-Type") != c.ctype {
				t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
		})
	}
	t.Run("wrong key", func(t *testing.T) {
		resp, b := post(t, s, "wrong", `{"model":"fake-basic"}`)
		if resp.StatusCode != 401 || !bytes.Equal(b, fixtures.Read("error_401.json")) {
			t.Fatalf("got %d %s", resp.StatusCode, b)
		}
	})
	t.Run("usage option", func(t *testing.T) {
		_, with := post(t, s, key, `{"model":"fake-basic","stream":true,"stream_options":{"include_usage":true}}`)
		_, without := post(t, s, key, `{"model":"fake-basic","stream":true}`)
		if !bytes.Equal(with, fixtures.Read("chat_stream_usage.sse")) || !bytes.Equal(without, fixtures.Read("chat_stream.sse")) {
			t.Fatal("stream bodies differ from the fixtures")
		}
	})
	t.Run("hang is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/v1/chat/completions", strings.NewReader(`{"model":"fake-hang"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		if resp, err := s.ts.Client().Do(req); err == nil {
			resp.Body.Close()
			t.Fatal("expected an error")
		}
		if !s.WaitCancelled(2 * time.Second) {
			t.Fatal("handler did not see the cancellation")
		}
	})
}

func TestFakeProviderBehaviours(t *testing.T) {
	s := New(t)
	key := fixtures.CanaryProviderKey
	do := func(method, path string, hdr http.Header, body string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(t.Context(), method, s.URL()+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header[k] = v
		}
		return s.ts.Client().Do(req)
	}
	noFollow := s.ts.Client()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	t.Run("redirect", func(t *testing.T) {
		s.RedirectTo("http://127.0.0.1:1/elsewhere")
		for _, c := range []struct {
			hdr  string
			want int
		}{{"", 307}, {"302", 302}, {"308", 308}} {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL()+"/v1/chat/completions", strings.NewReader(`{"model":"fake-redirect"}`))
			req.Header.Set("Authorization", "Bearer "+key)
			if c.hdr != "" {
				req.Header.Set("X-Fake-Status", c.hdr)
			}
			resp, err := noFollow.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want || resp.Header.Get("Location") != "http://127.0.0.1:1/elsewhere" {
				t.Errorf("%q: %d %q", c.hdr, resp.StatusCode, resp.Header.Get("Location"))
			}
		}
	})

	t.Run("echo", func(t *testing.T) {
		resp, err := do("POST", "/v1/chat/completions", http.Header{"Authorization": {"Bearer " + key}}, `{"model":"fake-echo-auth"}`)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("X-Echo-Authorization") != "Bearer "+key || resp.Header.Get("Www-Authenticate") != "Bearer "+key {
			t.Errorf("%d %v", resp.StatusCode, resp.Header)
		}
	})

	t.Run("close", func(t *testing.T) {
		resp, err := do("POST", "/v1/chat/completions", http.Header{"Authorization": {"Bearer " + key}}, `{"model":"fake-close"}`)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("answered %d", resp.StatusCode)
		}
	})

	t.Run("connections", func(t *testing.T) {
		if s.Connections() < 1 {
			t.Errorf("%d connections counted", s.Connections())
		}
	})

	t.Run("models accepts an anthropic key with anthropic-version", func(t *testing.T) {
		s.Accept("synthetic-anthropic-key")
		defer s.Accept(key)
		resp, err := do("GET", "/v1/models", http.Header{"X-Api-Key": {"synthetic-anthropic-key"}, "Anthropic-Version": {"2023-06-01"}}, "")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("x-api-key with anthropic-version: %d", resp.StatusCode)
		}
		resp, _ = do("GET", "/v1/models", http.Header{"X-Api-Key": {"synthetic-anthropic-key"}}, "")
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("x-api-key without anthropic-version: %d", resp.StatusCode)
		}
		resp, _ = do("GET", "/v1/models", http.Header{"Authorization": {"Bearer synthetic-anthropic-key"}, "Anthropic-Version": {"2023-06-01"}}, "")
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("a bearer with anthropic-version: %d", resp.StatusCode)
		}
	})

	t.Run("requests record method, path, query, header and body", func(t *testing.T) {
		before := len(s.Requests())
		resp, err := do("POST", "/v1/chat/completions?a=b", http.Header{"Authorization": {"Bearer " + key}, "X-Test": {"v"}}, `{"model":"fake-basic"}`)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		r := s.Requests()[before]
		if r.Method != "POST" || r.Path != "/v1/chat/completions" || r.RawQuery != "a=b" || r.Header.Get("X-Test") != "v" || string(r.Body) != `{"model":"fake-basic"}` {
			t.Errorf("%+v", r)
		}
	})
}

func postPath(t *testing.T, s *Server, path, body string) (*http.Response, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+fixtures.CanaryProviderKey)
	resp, err := s.ts.Client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

// TestOperationsAndBehaviours serves every behaviour of section 7 of sprint 3.2 for the three
// operations.
func TestOperationsAndBehaviours(t *testing.T) {
	s := New(t)
	paths := map[string]bool{"/v1/chat/completions": true, "/v1/responses": true, "/v1/embeddings": false}
	for path, streams := range paths {
		for _, model := range []string{ModelBasic, ModelNoUsage, ModelStreamError, ModelKeepalive, ModelTools, ModelTinyWrites, ModelCRLF, ModelBigEvent, ModelCut} {
			for _, stream := range []bool{false, true} {
				if stream && !streams {
					continue
				}
				name := path + " " + model
				if stream {
					name += " stream"
				}
				t.Run(name, func(t *testing.T) {
					body := `{"model":"` + model + `"`
					if stream {
						body += `,"stream":true`
					}
					resp, b, err := postPath(t, s, path, body+"}")
					if model == ModelCut {
						if err == nil {
							t.Fatalf("a cut answer was read to its end (%d bytes)", len(b))
						}
						return
					}
					if err != nil || (resp.StatusCode/100 != 2 && (model != ModelStreamError || stream)) {
						t.Fatalf("%v %v", resp, err)
					}
					if len(b) == 0 {
						t.Fatal("empty answer")
					}
					if model == ModelNoUsage && bytes.Contains(b, []byte(`"usage":{`)) {
						t.Errorf("the answer carries usage: %.200s", b)
					}
					if model == ModelCRLF && stream && !bytes.Contains(b, []byte("\r\n\r\n")) {
						t.Error("no CRLF event end")
					}
					if model == ModelKeepalive && stream && !bytes.HasPrefix(b, []byte(": ping\n\n")) {
						t.Error("no comment event")
					}
					if model == ModelBigEvent && len(b) < BigChatEventBytes {
						t.Errorf("answer of %d bytes is not over the bound", len(b))
					}
				})
			}
		}
	}
}

// TestGateEachEvent is the gate the streaming tests rely on: after the first event the provider
// sends nothing until Advance.
func TestGateEachEvent(t *testing.T) {
	s := New(t)
	s.GateEachEvent()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/v1/responses", strings.NewReader(`{"model":"fake-basic","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+fixtures.CanaryProviderKey)
	resp, err := s.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan int, 1)
	go func() {
		n := 0
		buf := make([]byte, 64<<10)
		for {
			k, err := resp.Body.Read(buf)
			n += k
			got <- n
			if err != nil {
				close(got)
				return
			}
		}
	}()
	first := <-got
	select {
	case n := <-got:
		if n != first {
			t.Fatal("the provider sent a second event before Advance")
		}
	case <-time.After(200 * time.Millisecond):
	}
	s.Advance()
	if n := <-got; n <= first {
		t.Fatalf("no progress after Advance: %d", n)
	}
	s.Release()
	for range got {
	}
}

// anthropicPost calls an Anthropic route of the fake with an x-api-key (empty: none).
func anthropicPost(t *testing.T, s *Server, path, key, body string) (*http.Response, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	req.Header.Set("Anthropic-Version", "2026-10-21")
	resp, err := s.ts.Client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

// TestAnthropicRoutes serves the Messages API: the key in X-Api-Key is checked against Accept, the
// answers are the fixtures, and every answer carries a request-id.
func TestAnthropicRoutes(t *testing.T) {
	s := New(t)
	key := fixtures.CanaryProviderKey
	cases := []struct {
		name, path, model string
		stream            bool
		status            int
		want              []byte
	}{
		{"message", "/v1/messages", ModelBasic, false, 200, fixtures.ReadAnthropic("message.json")},
		{"stream", "/v1/messages", ModelBasic, true, 200, fixtures.ReadAnthropic("message_stream.sse")},
		{"cache", "/v1/messages", ModelCache, false, 200, fixtures.ReadAnthropic("message_cache.json")},
		{"cache stream", "/v1/messages", ModelCache, true, 200, fixtures.ReadAnthropic("message_stream_cumulative.sse")},
		{"tools", "/v1/messages", ModelTools, true, 200, fixtures.ReadAnthropic("message_stream_tools.sse")},
		{"stream error", "/v1/messages", ModelStreamError, true, 200, fixtures.ReadAnthropic("message_stream_error.sse")},
		{"crlf", "/v1/messages", ModelCRLF, true, 200, fixtures.ReadAnthropic("message_stream_ping_crlf.sse")},
		{"count", "/v1/messages/count_tokens", ModelBasic, false, 200, fixtures.ReadAnthropic("count_tokens.json")},
		{"count never streams", "/v1/messages/count_tokens", ModelBasic, true, 200, fixtures.ReadAnthropic("count_tokens.json")},
		{"429", "/v1/messages", ModelError429, false, 429, fixtures.ReadAnthropic("error_429.json")},
		{"529", "/v1/messages", ModelError529, false, 529, fixtures.ReadAnthropic("error_529.json")},
		{"unknown model", "/v1/messages", "nope", false, 404, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"model":"` + c.model + `"`
			if c.stream {
				body += `,"stream":true`
			}
			resp, b, err := anthropicPost(t, s, c.path, key, body+"}")
			if err != nil || resp.StatusCode != c.status {
				t.Fatalf("%v %v", resp, err)
			}
			if c.want != nil && !bytes.Equal(b, c.want) {
				t.Errorf("body differs from the fixture: %.120s", b)
			}
			if !strings.HasPrefix(resp.Header.Get("Request-Id"), "req_synthetic-") {
				t.Errorf("Request-Id %q", resp.Header.Get("Request-Id"))
			}
		})
	}
	t.Run("a wrong or missing key is error_401.json", func(t *testing.T) {
		for _, k := range []string{"", "wrong"} {
			resp, b, err := anthropicPost(t, s, "/v1/messages", k, `{"model":"fake-basic"}`)
			if err != nil || resp.StatusCode != 401 || !bytes.Equal(b, fixtures.ReadAnthropic("error_401.json")) {
				t.Errorf("%q: %v %s %v", k, resp, b, err)
			}
		}
	})
	t.Run("a bearer token is not a key", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL()+"/v1/messages", strings.NewReader(`{"model":"fake-basic"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Anthropic-Version", "2026-10-21")
		resp, err := s.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%d", resp.StatusCode)
		}
	})
	t.Run("Accept replaces the key set", func(t *testing.T) {
		s.Accept("synthetic-other-key")
		defer s.Accept(key)
		if resp, _, _ := anthropicPost(t, s, "/v1/messages", key, `{"model":"fake-basic"}`); resp.StatusCode != 401 {
			t.Errorf("old key: %d", resp.StatusCode)
		}
		if resp, _, _ := anthropicPost(t, s, "/v1/messages", "synthetic-other-key", `{"model":"fake-basic"}`); resp.StatusCode != 200 {
			t.Errorf("new key: %d", resp.StatusCode)
		}
	})
	t.Run("request ids are distinct", func(t *testing.T) {
		a, _, _ := anthropicPost(t, s, "/v1/messages", key, `{"model":"fake-basic"}`)
		b, _, _ := anthropicPost(t, s, "/v1/messages", key, `{"model":"fake-basic"}`)
		if a.Header.Get("Request-Id") == b.Header.Get("Request-Id") {
			t.Error("two answers share a request-id")
		}
	})
	t.Run("the headers a gateway must filter are present", func(t *testing.T) {
		resp, _, _ := anthropicPost(t, s, "/v1/messages", key, `{"model":"fake-basic"}`)
		for _, n := range []string{"Set-Cookie", "Via", "X-Pandokos-Error", "Anthropic-Ratelimit-Requests-Remaining"} {
			if resp.Header.Get(n) == "" {
				t.Errorf("no %s", n)
			}
		}
	})
}

// TestAnthropicBehaviours serves the behaviours of the 3.2 models and the new ones on the Messages
// API.
func TestAnthropicBehaviours(t *testing.T) {
	s := New(t)
	key := fixtures.CanaryProviderKey
	for _, model := range []string{ModelNoUsage, ModelStreamError, ModelKeepalive, ModelTools, ModelTinyWrites, ModelCRLF, ModelBigEvent, ModelCut} {
		for _, stream := range []bool{false, true} {
			t.Run(model+map[bool]string{false: "", true: " stream"}[stream], func(t *testing.T) {
				body := `{"model":"` + model + `"`
				if stream {
					body += `,"stream":true`
				}
				resp, b, err := anthropicPost(t, s, "/v1/messages", key, body+"}")
				if model == ModelCut {
					if err == nil {
						t.Fatalf("a cut answer was read to its end (%d bytes)", len(b))
					}
					return
				}
				if err != nil || (resp.StatusCode/100 != 2 && (model != ModelStreamError || stream)) {
					t.Fatalf("%v %v", resp, err)
				}
				if model == ModelNoUsage && bytes.Contains(b, []byte(`"usage":{`)) {
					t.Errorf("the answer carries usage: %.200s", b)
				}
				if model == ModelBigEvent && stream && len(b) < BigAnthropicEventBytes {
					t.Errorf("event of %d bytes is not over the bound", len(b))
				}
				if model == ModelKeepalive && stream && !bytes.HasPrefix(b, []byte(": ping\n\n")) {
					t.Error("no comment event")
				}
			})
		}
	}
	t.Run("cut after start sends message_start only and then fails", func(t *testing.T) {
		resp, b, err := anthropicPost(t, s, "/v1/messages", key, `{"model":"fake-cut-after-start","stream":true}`)
		if err == nil {
			t.Fatalf("the stream ended cleanly: %d %s", resp.StatusCode, b)
		}
		if !bytes.HasPrefix(b, []byte("event: message_start")) || bytes.Contains(b, []byte("content_block")) {
			t.Errorf("read %.200s", b)
		}
	})
	t.Run("stall after first waits for the cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/v1/messages", strings.NewReader(`{"model":"fake-stall-after-first","stream":true}`))
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Anthropic-Version", "2026-10-21")
		resp, err := s.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		if n, err := resp.Body.Read(buf); n == 0 || err != nil {
			t.Fatalf("no first event: %d %v", n, err)
		}
		cancel()
		resp.Body.Close()
		if !s.WaitCancelled(2 * time.Second) {
			t.Fatal("the handler did not see the cancellation")
		}
	})
	t.Run("the same two models on the chat route", func(t *testing.T) {
		resp, b, err := postPath(t, s, "/v1/chat/completions", `{"model":"fake-cut-after-start","stream":true}`)
		if err == nil || resp == nil || !bytes.HasPrefix(b, []byte("data: ")) {
			t.Errorf("cut after start: %v %q", err, b)
		}
		ctx, cancel := context.WithCancel(t.Context())
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/v1/chat/completions", strings.NewReader(`{"model":"fake-stall-after-first","stream":true}`))
		req.Header.Set("Authorization", "Bearer "+key)
		r2, err := s.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r2.Body.Read(make([]byte, 16))
		cancel()
		r2.Body.Close()
		if !s.WaitCancelled(2 * time.Second) {
			t.Fatal("no cancellation seen")
		}
	})
	t.Run("a missing anthropic-version is the provider's 400", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL()+"/v1/messages", strings.NewReader(`{"model":"fake-basic"}`))
		req.Header.Set("X-Api-Key", key)
		resp, err := s.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("%d", resp.StatusCode)
		}
	})
}

// TestCompatProfiles serves each compatible profile on the chat route, and the usual answers on
// the other two operations.
func TestCompatProfiles(t *testing.T) {
	s := New(t)
	cases := []struct {
		name, model, body string
		status            int
		want              []byte
	}{
		{"last chunk", ModelCompatUsageLastChunk, `"stream":true`, 200, fixtures.ReadCompat("chat_stream_usage_last_content_chunk.sse")},
		{"extra usage", ModelCompatExtraUsage, `"stream":true`, 200, fixtures.ReadCompat("chat_stream_extra_usage.sse")},
		{"extra usage plain", ModelCompatExtraUsage, `"x":1`, 200, fixtures.ReadCompat("chat_completion_extra_usage.json")},
		{"ignores without option", ModelCompatIgnoresOption, `"stream":true`, 200, fixtures.ReadCompat("chat_stream_no_usage.sse")},
		{"ignores with option", ModelCompatIgnoresOption, `"stream":true,"stream_options":{"include_usage":true}`, 200, fixtures.ReadCompat("chat_stream_no_usage.sse")},
		{"refuses with option", ModelCompatRefusesOption, `"stream":true,"stream_options":{"include_usage":true}`, 400, fixtures.ReadCompat("error_400_stream_options.json")},
		{"refuses without option", ModelCompatRefusesOption, `"stream":true`, 200, fixtures.ReadCompat("chat_stream_no_usage.sse")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, b, err := postPath(t, s, "/v1/chat/completions", `{"model":"`+c.model+`",`+c.body+`}`)
			if err != nil || resp.StatusCode != c.status || !bytes.Equal(b, c.want) {
				t.Fatalf("%v %.200s %v", resp, b, err)
			}
		})
	}
	t.Run("a nested stream_options is not the option", func(t *testing.T) {
		resp, _, err := postPath(t, s, "/v1/chat/completions", `{"model":"compat-refuses-option","stream":true,"metadata":{"stream_options":1}}`)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", resp, err)
		}
	})
	t.Run("the other operations answer as usual", func(t *testing.T) {
		for _, path := range []string{"/v1/responses", "/v1/embeddings"} {
			for _, m := range []string{ModelCompatUsageLastChunk, ModelCompatExtraUsage, ModelCompatIgnoresOption, ModelCompatRefusesOption} {
				if resp, _, err := postPath(t, s, path, `{"model":"`+m+`"}`); err != nil || resp.StatusCode != 200 {
					t.Errorf("%s %s: %v %v", path, m, resp, err)
				}
			}
		}
	})
}

// TestBasePathAndNewOn: SetBasePath makes every route also answer under a prefix, and NewOn
// listens on the address given.
func TestBasePathAndNewOn(t *testing.T) {
	s := NewOn(t, "127.0.0.1:0")
	if s.Port() == 0 || !strings.HasSuffix(s.URL(), ":"+strconv.Itoa(s.Port())) {
		t.Fatalf("URL %q port %d", s.URL(), s.Port())
	}
	s.SetBasePath("/compat-base")
	for _, path := range []string{"/v1/chat/completions", "/compat-base/v1/chat/completions"} {
		resp, _, err := postPath(t, s, path, `{"model":"fake-basic"}`)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: %v %v", path, resp, err)
		}
	}
	for _, path := range []string{"/v1/messages", "/compat-base/v1/messages"} {
		if resp, _, err := anthropicPost(t, s, path, fixtures.CanaryProviderKey, `{"model":"fake-basic"}`); err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: %v %v", path, resp, err)
		}
	}
	if resp, _, _ := postPath(t, s, "/other/v1/chat/completions", `{"model":"fake-basic"}`); resp.StatusCode != 404 {
		t.Errorf("another prefix: %d", resp.StatusCode)
	}
	reqs := s.Requests()
	if reqs[1].Path != "/compat-base/v1/chat/completions" {
		t.Errorf("the recorded path is %q", reqs[1].Path)
	}
	t.Run("all interfaces", func(t *testing.T) {
		any := NewOn(t, "0.0.0.0:0")
		if resp, _, err := postPath(t, any, "/v1/chat/completions", `{"model":"fake-basic"}`); err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", resp, err)
		}
	})
}

// TestReportTokens: the usage of an answer that is not a stream reports the tokens a test asks for.
func TestReportTokens(t *testing.T) {
	s := New(t)
	key := fixtures.CanaryProviderKey
	body := `{"model":"fake-basic","messages":[]}`
	if _, b := post(t, s, key, body); !bytes.Contains(b, []byte(`"total_tokens": 21`)) {
		t.Fatalf("by default the fixture is served: %s", b)
	}
	s.ReportTokens(500)
	_, b := post(t, s, key, body)
	for _, want := range []string{`"prompt_tokens":1`, `"completion_tokens":499`, `"total_tokens":500`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("the answer lacks %s: %s", want, b)
		}
	}
	s.ReportTokens(0)
	if _, b := post(t, s, key, body); !bytes.Contains(b, []byte(`"total_tokens": 21`)) {
		t.Errorf("zero restores the fixture: %s", b)
	}
}
