package fixtures

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFixturesAreWellFormed(t *testing.T) {
	for _, name := range []string{"chat_completion.json", "chat_completion_details.json", "error_401.json", "error_429.json"} {
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
