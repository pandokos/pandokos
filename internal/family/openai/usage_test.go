package openai

import (
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/fixtures"
)

func TestReadUsage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		u, ok, err := ReadUsage(fixtures.Read("chat_completion.json"))
		want := family.Usage{InputTokens: 9, OutputTokens: 12}
		if err != nil || !ok || !reflect.DeepEqual(u, want) {
			t.Fatalf("got %+v %v %v", u, ok, err)
		}
	})
	t.Run("details", func(t *testing.T) {
		u, ok, err := ReadUsage(fixtures.Read("chat_completion_details.json"))
		want := family.Usage{
			InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, ReasoningTokens: 2,
			Extra: map[string]int64{"input_audio_tokens": 4, "accepted_prediction_tokens": 1},
		}
		if err != nil || !ok || !reflect.DeepEqual(u, want) {
			t.Fatalf("got %+v %v %v", u, ok, err)
		}
	})
	for name, body := range map[string]string{
		"no usage":   `{"id":"x","choices":[]}`,
		"null usage": `{"id":"x","usage":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok, err := ReadUsage([]byte(body)); ok || err != nil {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
	for name, body := range map[string]string{
		"invalid JSON":      `{"usage":`,
		"not JSON":          `hello`,
		"negative input":    `{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`,
		"negative output":   `{"usage":{"prompt_tokens":1,"completion_tokens":-1}}`,
		"negative detail":   `{"usage":{"prompt_tokens":1,"prompt_tokens_details":{"cached_tokens":-2}}}`,
		"negative reasoned": `{"usage":{"completion_tokens_details":{"reasoning_tokens":-2}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ReadUsage([]byte(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func FuzzReadUsage(f *testing.F) {
	f.Add(fixtures.Read("chat_completion.json"))
	f.Add(fixtures.Read("chat_completion_details.json"))
	f.Add([]byte(`{"usage":{"prompt_tokens":-1}}`))
	f.Add([]byte(``))
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _, _ = ReadUsage(b)
	})
}
