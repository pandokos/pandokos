package anthropic_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/test/fixtures"
)

// TestReadUsage is T-521: the normalization table of section 5.4 over the JSON fixtures.
func TestReadUsage(t *testing.T) {
	t.Run("message.json", func(t *testing.T) {
		u, ok, err := anthropic.ReadUsageOf(anthropic.OperationMessages, fixtures.ReadAnthropic("message.json"))
		if err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		want := family.Usage{InputTokens: 12, OutputTokens: 7, Reported: map[string]int64{
			"input_tokens": 12, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 7,
		}}
		if !reflect.DeepEqual(u, want) {
			t.Errorf("got  %+v\nwant %+v", u, want)
		}
	})
	t.Run("message_cache.json", func(t *testing.T) {
		u, ok, err := anthropic.ReadUsageOf(anthropic.OperationMessages, fixtures.ReadAnthropic("message_cache.json"))
		if err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		want := family.Usage{
			InputTokens: 10 + 300 + 2000, CacheReadTokens: 2000, CacheWriteTokens: 300, OutputTokens: 50,
			Extra: map[string]int64{"cache_write_1h_tokens": 200, "web_search_requests": 2},
			Reported: map[string]int64{
				"input_tokens": 10, "cache_creation_input_tokens": 300, "cache_read_input_tokens": 2000,
				"cache_creation.ephemeral_5m_input_tokens": 100, "cache_creation.ephemeral_1h_input_tokens": 200,
				"output_tokens": 50, "server_tool_use.web_search_requests": 2,
			},
		}
		if !reflect.DeepEqual(u, want) {
			t.Errorf("got  %+v\nwant %+v", u, want)
		}
		if _, ok := u.Reported["service_tier"]; ok {
			t.Error("a string member is in Reported")
		}
		if u.ReasoningTokens != 0 {
			t.Errorf("reasoning tokens %d: the provider does not report them apart", u.ReasoningTokens)
		}
	})
	t.Run("a missing or null member counts 0", func(t *testing.T) {
		u, ok, err := anthropic.ReadUsageOf(anthropic.OperationMessages,
			[]byte(`{"usage":{"input_tokens":null,"cache_read_input_tokens":null,"output_tokens":5,"cache_creation":null,"server_tool_use":null}}`))
		if err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if u.InputTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 || u.OutputTokens != 5 || len(u.Extra) != 0 {
			t.Errorf("%+v", u)
		}
		u, ok, err = anthropic.ReadUsageOf(anthropic.OperationMessages, []byte(`{"usage":{}}`))
		if err != nil || !ok || u.InputTokens != 0 || u.OutputTokens != 0 {
			t.Errorf("an empty usage object: %+v %v %v", u, ok, err)
		}
	})
	t.Run("Extra holds non-zero entries only", func(t *testing.T) {
		u, _, err := anthropic.ReadUsageOf(anthropic.OperationMessages,
			[]byte(`{"usage":{"input_tokens":1,"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":0},"output_tokens":1,"server_tool_use":{"web_search_requests":0}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(u.Extra) != 0 {
			t.Errorf("Extra %v", u.Extra)
		}
		if u.InputTokens != 5 || u.CacheWriteTokens != 4 {
			t.Errorf("%+v", u)
		}
	})
	t.Run("no usage object is ok=false", func(t *testing.T) {
		for _, in := range []string{`{"id":"msg_synthetic-1","type":"message"}`, `{"usage":null}`, `{}`} {
			u, ok, err := anthropic.ReadUsageOf(anthropic.OperationMessages, []byte(in))
			if ok || err != nil || !reflect.DeepEqual(u, family.Usage{}) {
				t.Errorf("%s: %+v %v %v", in, u, ok, err)
			}
		}
	})
	t.Run("a usage that cannot be used is an error", func(t *testing.T) {
		for _, in := range []string{
			`{"usage":{"input_tokens":-1,"output_tokens":1}}`,
			`{"usage":{"input_tokens":1,"output_tokens":-1}}`,
			`{"usage":{"input_tokens":1,"cache_read_input_tokens":-1}}`,
			`{"usage":{"input_tokens":1,"cache_creation_input_tokens":-1}}`,
			`{"usage":{"input_tokens":1.5,"output_tokens":1}}`,
			`{"usage":{"input_tokens":1,"output_tokens":0.5}}`,
			`{"usage":{"input_tokens":"12","output_tokens":1}}`,
			`{"usage":{"input_tokens":true}}`,
			`{"usage":{"input_tokens":99999999999999999999999}}`,
			`{"usage":{"input_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":-1}}}`,
			`{"usage":{"input_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":0.5}}}`,
			`{"usage":{"input_tokens":1,"server_tool_use":{"web_search_requests":-1}}}`,
			`{"usage":[1]}`,
			`{"usage":7}`,
			`{"usage":"x"}`,
			`not json`,
			``,
			`[]`,
			`{"usage":{"input_tokens":1}`,
		} {
			u, ok, err := anthropic.ReadUsageOf(anthropic.OperationMessages, []byte(in))
			if err == nil || ok {
				t.Errorf("%s accepted: %+v %v", in, u, ok)
			}
		}
	})
	t.Run("token counting is never read", func(t *testing.T) {
		u, ok, err := anthropic.ReadUsageOf(anthropic.OperationCountTokens, fixtures.ReadAnthropic("count_tokens.json"))
		if ok || err != nil || !reflect.DeepEqual(u, family.Usage{}) {
			t.Errorf("%+v %v %v", u, ok, err)
		}
	})
	t.Run("the error never holds the body", func(t *testing.T) {
		_, _, err := anthropic.ReadUsageOf(anthropic.OperationMessages, []byte(`{"content":[{"text":"`+fixtures.CanaryCompletion+`"}],"usage":{"input_tokens":-1}}`))
		if err == nil {
			t.Fatal("accepted")
		}
		if got := err.Error(); len(got) > 0 && strings.Contains(got, fixtures.CanaryCompletion) {
			t.Errorf("the error quotes the body: %v", err)
		}
	})
}

// FuzzAnthropicReadUsage is T-524: no panic; what is accepted is non-negative and consistent;
// Reported is within the bounds of 3.2.
func FuzzAnthropicReadUsage(f *testing.F) {
	f.Add(fixtures.ReadAnthropic("message.json"))
	f.Add(fixtures.ReadAnthropic("message_cache.json"))
	f.Add(fixtures.ReadAnthropic("count_tokens.json"))
	f.Add([]byte(`{"usage":{"input_tokens":-1}}`))
	f.Add([]byte(`{"usage":{"input_tokens":1.5}}`))
	f.Add([]byte(`{"usage":{"cache_creation":{"ephemeral_1h_input_tokens":9999999999999999},"cache_creation_input_tokens":1}}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		for _, op := range operations {
			u, ok, err := anthropic.ReadUsageOf(op, in)
			if err != nil && ok {
				t.Fatal("ok with an error")
			}
			if !ok {
				if err == nil && !reflect.DeepEqual(u, family.Usage{}) {
					t.Fatalf("usage %+v without ok", u)
				}
				continue
			}
			if op == anthropic.OperationCountTokens {
				t.Fatal("token counting reported usage")
			}
			if !json.Valid(in) {
				t.Fatal("usage read from invalid JSON")
			}
			if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 || u.ReasoningTokens < 0 {
				t.Fatalf("negative figure: %+v", u)
			}
			if u.InputTokens < u.CacheReadTokens+u.CacheWriteTokens {
				t.Fatalf("input %d does not include the cache counts %d and %d", u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens)
			}
			if len(u.Reported) > 32 {
				t.Fatalf("%d reported entries", len(u.Reported))
			}
			for k, v := range u.Reported {
				if v < 0 || len(k) == 0 || len(k) > 64 {
					t.Fatalf("reported %q=%d", k, v)
				}
			}
			for k, v := range u.Extra {
				if v == 0 {
					t.Fatalf("extra %q is zero", k)
				}
			}
		}
	})
}
