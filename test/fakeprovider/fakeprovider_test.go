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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL()+"/chat/completions", strings.NewReader(body))
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
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL()+"/chat/completions", strings.NewReader(`{"model":"fake-hang"}`))
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
