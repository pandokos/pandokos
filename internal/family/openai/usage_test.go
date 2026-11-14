package openai

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/test/fixtures"
)

func TestReadUsage(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		u, ok, err := ReadUsage(fixtures.Read("chat_completion.json"))
		u.Reported = nil // checked by TestReadUsageOf
		want := family.Usage{InputTokens: 9, OutputTokens: 12}
		if err != nil || !ok || !reflect.DeepEqual(u, want) {
			t.Fatalf("got %+v %v %v", u, ok, err)
		}
	})
	t.Run("details", func(t *testing.T) {
		u, ok, err := ReadUsage(fixtures.Read("chat_completion_details.json"))
		u.Reported = nil // checked by TestReadUsageOf
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

// TestReadUsageOf is T-458: each operation's fixture gives the normalized values of section 5.2
// and the exact map of the provider's own counters.
func TestReadUsageOf(t *testing.T) {
	cases := []struct {
		name, op, fixture string
		want              family.Usage
	}{
		{"chat", OperationChatCompletions, "chat_completion.json", family.Usage{
			InputTokens: 9, OutputTokens: 12,
			Reported: map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}}},
		{"chat details", OperationChatCompletions, "chat_completion_details.json", family.Usage{
			InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, ReasoningTokens: 2,
			Extra: map[string]int64{"input_audio_tokens": 4, "accepted_prediction_tokens": 1},
			Reported: map[string]int64{
				"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
				"prompt_tokens_details.cached_tokens": 3, "prompt_tokens_details.audio_tokens": 4,
				"completion_tokens_details.reasoning_tokens": 2, "completion_tokens_details.audio_tokens": 0,
				"completion_tokens_details.accepted_prediction_tokens": 1, "completion_tokens_details.rejected_prediction_tokens": 0}}},
		{"responses", OperationResponses, "responses.json", family.Usage{
			InputTokens: 20, OutputTokens: 15, CacheReadTokens: 5, ReasoningTokens: 4,
			Reported: map[string]int64{
				"input_tokens": 20, "input_tokens_details.cached_tokens": 5,
				"output_tokens": 15, "output_tokens_details.reasoning_tokens": 4, "total_tokens": 35}}},
		{"embeddings", OperationEmbeddings, "embeddings.json", family.Usage{
			InputTokens: 8, Reported: map[string]int64{"prompt_tokens": 8, "total_tokens": 8}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok, err := ReadUsageOf(c.op, fixtures.Read(c.fixture))
			if err != nil || !ok || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v %v %v\nwant %+v", got, ok, err, c.want)
			}
		})
	}

	for _, op := range []string{OperationChatCompletions, OperationResponses, OperationEmbeddings} {
		in, out := "prompt_tokens", "completion_tokens"
		if op == OperationResponses {
			in, out = "input_tokens", "output_tokens"
		}
		t.Run(op+" without usage", func(t *testing.T) {
			for _, body := range []string{`{"id":"x"}`, `{"id":"x","usage":null}`} {
				if _, ok, err := ReadUsageOf(op, []byte(body)); ok || err != nil {
					t.Errorf("%s: ok=%v err=%v", body, ok, err)
				}
			}
		})
		t.Run(op+" unusable", func(t *testing.T) {
			bodies := []string{`{"usage":`, `hello`, `{"usage":{"` + in + `":-1}}`}
			if op != OperationEmbeddings {
				bodies = append(bodies, `{"usage":{"`+out+`":-1}}`)
			}
			for _, body := range bodies {
				if _, _, err := ReadUsageOf(op, []byte(body)); err == nil {
					t.Errorf("%s: no error", body)
				}
			}
		})
		t.Run(op+" keeps integers only", func(t *testing.T) {
			body := `{"usage":{"` + in + `":3,"note":"` + fixtures.CanaryCompletion + `","ratio":1.5,"flag":true,"list":[1],"huge":1e3,"Upper":4,"neg":-1}}`
			u, ok, err := ReadUsageOf(op, []byte(body))
			if err != nil || !ok {
				t.Fatalf("%v %v", ok, err)
			}
			if want := map[string]int64{in: 3}; !reflect.DeepEqual(u.Reported, want) {
				t.Errorf("Reported %v, want %v", u.Reported, want)
			}
		})
	}
	t.Run("embeddings have no output", func(t *testing.T) {
		u, ok, err := ReadUsageOf(OperationEmbeddings, []byte(`{"usage":{"prompt_tokens":8,"total_tokens":8}}`))
		if err != nil || !ok || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.ReasoningTokens != 0 {
			t.Errorf("%+v %v %v", u, ok, err)
		}
	})
	t.Run("ReadUsage stays chat", func(t *testing.T) {
		a, _, _ := ReadUsage(fixtures.Read("chat_completion.json"))
		b, _, _ := ReadUsageOf(OperationChatCompletions, fixtures.Read("chat_completion.json"))
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%+v != %+v", a, b)
		}
	})
}

// FuzzReadUsage is T-463 for the readers: no panic for any operation, and the counters of the
// provider stay within their bounds whatever the input.
func FuzzReadUsage(f *testing.F) {
	for i, name := range []string{"chat_completion.json", "chat_completion_details.json", "responses.json", "embeddings.json"} {
		f.Add(fixtures.Read(name), uint8(i))
	}
	f.Add([]byte(`{"usage":{"prompt_tokens":-1}}`), uint8(0))
	f.Add([]byte(`{"usage":{"a":{"b":{"c":{"d":1}}},"e":1e400,"f":9223372036854775808}}`), uint8(1))
	f.Add([]byte(``), uint8(2))
	f.Fuzz(func(t *testing.T, b []byte, which uint8) {
		op := []string{OperationChatCompletions, OperationResponses, OperationEmbeddings}[int(which)%3]
		u, ok, err := ReadUsageOf(op, b)
		if err != nil || !ok {
			return
		}
		if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.ReasoningTokens < 0 {
			t.Fatalf("negative figure: %+v", u)
		}
		if len(u.Reported) > 32 {
			t.Fatalf("%d reported entries", len(u.Reported))
		}
		for k, v := range u.Reported {
			if !reportedKey.MatchString(k) || v < 0 {
				t.Fatalf("reported entry %q = %d", k, v)
			}
		}
	})
}

var reportedKey = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)
