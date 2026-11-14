package fakeprovider

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
