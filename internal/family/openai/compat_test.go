package openai

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/fixtures"
)

// TestCompatProfiles is T-528: the readers over the fixtures of OpenAI-compatible servers.
func TestCompatProfiles(t *testing.T) {
	wantCore := family.Usage{InputTokens: 9, OutputTokens: 12}
	extraReported := map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21, "prompt_cache_hit_tokens": 3}

	for _, strip := range []bool{false, true} {
		name := map[bool]string{false: "observing", true: "stripping"}[strip]

		t.Run("usage on the last content chunk, "+name, func(t *testing.T) {
			data := fixtures.ReadCompat("chat_stream_usage_last_content_chunk.sse")
			r := NewStreamReader(strip)
			if got := run(r, data, 7); !bytes.Equal(got, data) {
				t.Errorf("the stream was changed: the chunk that carries content and usage must be forwarded whole")
			}
			u, parts := r.Usage()
			if parts != family.PartsAll || core(u).InputTokens != 9 || core(u).OutputTokens != 12 || r.Failed() {
				t.Errorf("usage %+v parts %v", u, parts)
			}
			if !reflect.DeepEqual(u.Reported, map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}) {
				t.Errorf("reported %v", u.Reported)
			}
		})

		t.Run("provider-specific members, "+name, func(t *testing.T) {
			data := fixtures.ReadCompat("chat_stream_extra_usage.sse")
			r := NewStreamReader(strip)
			got := run(r, data, 11)
			u, parts := r.Usage()
			if parts != family.PartsAll || core(u).InputTokens != 9 || core(u).OutputTokens != 12 {
				t.Errorf("usage %+v parts %v", u, parts)
			}
			if !reflect.DeepEqual(u.Reported, extraReported) {
				t.Errorf("reported %v, want the integer members only %v", u.Reported, extraReported)
			}
			evs := events(data)
			wantStream := data
			if strip { // the usage-only chunk is the one before [DONE]; Pandokos added the option, so it is removed
				wantStream = without(data, len(evs)-2)
			}
			if !bytes.Equal(got, wantStream) {
				t.Errorf("forwarded %d bytes, want %d", len(got), len(wantStream))
			}
		})

		t.Run("no usage at all, "+name, func(t *testing.T) {
			data := fixtures.ReadCompat("chat_stream_no_usage.sse")
			r := NewStreamReader(strip)
			if got := run(r, data, 5); !bytes.Equal(got, data) {
				t.Error("the stream was changed")
			}
			u, parts := r.Usage()
			if parts != 0 || !reflect.DeepEqual(u, family.Usage{}) {
				t.Errorf("usage %+v parts %v", u, parts)
			}
			if r.Deltas() == 0 {
				t.Error("no content events counted: the estimate would have no evidence")
			}
		})
	}

	t.Run("a plain answer with provider-specific members", func(t *testing.T) {
		u, ok, err := ReadUsageOf(OperationChatCompletions, fixtures.ReadCompat("chat_completion_extra_usage.json"))
		if err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if core(u).InputTokens != wantCore.InputTokens || core(u).OutputTokens != wantCore.OutputTokens {
			t.Errorf("usage %+v", u)
		}
		if !reflect.DeepEqual(u.Reported, extraReported) {
			t.Errorf("reported %v, want %v", u.Reported, extraReported)
		}
		for k := range u.Reported {
			if k == "queue_time" || k == "x_vendor.total_time" {
				t.Errorf("a fractional member %q was kept", k)
			}
		}
	})

	t.Run("the compat fixtures never hold a key", func(t *testing.T) {
		for _, name := range fixtures.Dir("compat") {
			for _, c := range fixtures.Canaries() {
				if c == fixtures.CanaryCompletion || c == fixtures.CanaryPrompt {
					continue
				}
				if bytes.Contains(fixtures.ReadCompat(name), []byte(c)) {
					t.Errorf("%s holds %q", name, c)
				}
			}
		}
	})
}
