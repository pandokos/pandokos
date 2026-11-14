package fixtures

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFixturesAreWellFormed(t *testing.T) {
	for _, name := range []string{"chat_completion.json", "chat_completion_details.json", "error_401.json", "error_429.json", "responses.json", "embeddings.json"} {
		if !json.Valid(Read(name)) {
			t.Errorf("%s is not valid JSON", name)
		}
	}
	for _, name := range []string{"chat_stream_usage.sse", "chat_stream.sse", "chat_stream_usage_on_last_chunk.sse"} {
		b := Read(name)
		if !bytes.HasSuffix(b, []byte("data: [DONE]\n\n")) {
			t.Errorf("%s does not end with data: [DONE]", name)
		}
		for _, ev := range bytes.Split(bytes.TrimSuffix(b, []byte("\n\n")), []byte("\n\n")) {
			payload, ok := bytes.CutPrefix(ev, []byte("data: "))
			if !ok {
				t.Errorf("%s: event without data prefix", name)
				continue
			}
			if string(payload) == "[DONE]" {
				continue
			}
			if !json.Valid(payload) {
				t.Errorf("%s: invalid JSON payload", name)
			}
		}
	}
	// The streams of sprint 3.2: events end with a blank line (LF or CRLF), JSON data is valid, and
	// the only content-like strings are the canaries.
	for _, name := range []string{"responses_stream.sse", "responses_stream_incomplete.sse", "chat_stream_tools.sse", "chat_stream_error.sse", "chat_stream_keepalive_crlf.sse"} {
		b := Read(name)
		norm := bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
		if !bytes.HasSuffix(norm, []byte("\n\n")) {
			t.Errorf("%s does not end with a blank line", name)
		}
		for _, ev := range bytes.Split(bytes.TrimSuffix(norm, []byte("\n\n")), []byte("\n\n")) {
			for _, line := range bytes.Split(ev, []byte("\n")) {
				if payload, ok := bytes.CutPrefix(line, []byte("data: ")); ok && string(payload) != "[DONE]" && !json.Valid(payload) {
					t.Errorf("%s: invalid JSON payload", name)
				}
			}
		}
	}
	if bytes.HasSuffix(Read("chat_stream_error.sse"), []byte("[DONE]\n\n")) {
		t.Error("chat_stream_error.sse must end without [DONE]")
	}
	if !bytes.Contains(Read("chat_stream_keepalive_crlf.sse"), []byte(": ping\r\n\r\n")) {
		t.Error("chat_stream_keepalive_crlf.sse has no CRLF keep-alive event")
	}
	seen := map[string]bool{}
	for _, c := range Canaries() {
		if seen[c] {
			t.Errorf("duplicate canary %q", c)
		}
		seen[c] = true
	}
	if len(Canaries()) != 4 {
		t.Errorf("Canaries() returns %d values, want 4", len(Canaries()))
	}
}

// TestSprint33FixturesAreWellFormed checks the Anthropic and compatible fixtures: JSON and event
// data are valid, streams end with a blank line, and the directories list the files of sprint 3.3.
func TestSprint33FixturesAreWellFormed(t *testing.T) {
	wantAnthropic := []string{"count_tokens.json", "error_401.json", "error_429.json", "error_529.json", "message.json", "message_cache.json",
		"message_stream.sse", "message_stream_cumulative.sse", "message_stream_error.sse", "message_stream_ping_crlf.sse", "message_stream_tools.sse"}
	wantCompat := []string{"chat_completion_extra_usage.json", "chat_stream_extra_usage.sse", "chat_stream_no_usage.sse",
		"chat_stream_usage_last_content_chunk.sse", "error_400_stream_options.json"}
	for dir, want := range map[string][]string{"anthropic": wantAnthropic, "compat": wantCompat} {
		got := Dir(dir)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s holds %v, want %v", dir, got, want)
		}
		for _, name := range got {
			b := read(dir + "/" + name)
			switch {
			case strings.HasSuffix(name, ".json"):
				if !json.Valid(b) {
					t.Errorf("%s/%s is not valid JSON", dir, name)
				}
			case strings.HasSuffix(name, ".sse"):
				norm := bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
				if !bytes.HasSuffix(norm, []byte("\n\n")) {
					t.Errorf("%s/%s does not end with a blank line", dir, name)
				}
				for _, line := range bytes.Split(norm, []byte("\n")) {
					if payload, ok := bytes.CutPrefix(line, []byte("data: ")); ok && string(payload) != "[DONE]" && !json.Valid(payload) {
						t.Errorf("%s/%s: invalid JSON payload", dir, name)
					}
				}
			}
		}
	}
	if !bytes.Contains(ReadAnthropic("message_stream_ping_crlf.sse"), []byte("\r\n\r\n")) {
		t.Error("message_stream_ping_crlf.sse has no CRLF event end")
	}
	if bytes.Contains(ReadAnthropic("message_stream_error.sse"), []byte("message_delta")) {
		t.Error("message_stream_error.sse must have no message_delta")
	}
	for _, name := range []string{"chat_stream_usage_last_content_chunk.sse", "chat_stream_extra_usage.sse", "chat_stream_no_usage.sse"} {
		if !bytes.HasSuffix(ReadCompat(name), []byte("data: [DONE]\n\n")) {
			t.Errorf("%s does not end with data: [DONE]", name)
		}
	}
	if !bytes.Contains(ReadCompat("chat_stream_extra_usage.sse"), []byte(`"queue_time":0.012`)) {
		t.Error("chat_stream_extra_usage.sse has no fractional member")
	}
}
