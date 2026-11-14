package openai

import (
	"reflect"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
)

// TestEstimate is T-465: the table of section 5.4 for every operation, rounding up, zero sizes.
func TestEstimate(t *testing.T) {
	type row struct {
		name            string
		e               family.Evidence
		wantIn, wantOut int64
	}
	for _, op := range []string{OperationChatCompletions, OperationResponses} {
		for _, c := range []row{
			{"zero sizes", family.Evidence{}, 0, 0},
			{"zero sizes, stream", family.Evidence{Stream: true}, 0, 0},
			{"one byte rounds up", family.Evidence{RequestBytes: 1, ResponseBytes: 1}, 1, 1},
			{"exact multiples", family.Evidence{RequestBytes: 400, ResponseBytes: 40}, 100, 10},
			{"rounding up", family.Evidence{RequestBytes: 401, ResponseBytes: 43}, 101, 11},
			{"three bytes", family.Evidence{RequestBytes: 3, ResponseBytes: 3}, 1, 1},
			{"a stream counts content events, not bytes", family.Evidence{Stream: true, RequestBytes: 10, ResponseBytes: 5000, Deltas: 7}, 3, 7},
			{"a stream without events", family.Evidence{Stream: true, RequestBytes: 10, ResponseBytes: 5000}, 3, 0},
			{"a plain answer ignores the events", family.Evidence{RequestBytes: 8, ResponseBytes: 20, Deltas: 99}, 2, 5},
			{"large sizes do not overflow", family.Evidence{RequestBytes: 1 << 40, ResponseBytes: 1<<40 + 1}, 1 << 38, 1<<38 + 1},
		} {
			t.Run(op+" "+c.name, func(t *testing.T) {
				got := Estimate(op, c.e)
				want := family.Usage{InputTokens: c.wantIn, OutputTokens: c.wantOut}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("got %+v, want %+v", got, want)
				}
			})
		}
	}
	t.Run("embeddings have no output", func(t *testing.T) {
		for _, e := range []family.Evidence{
			{}, {RequestBytes: 1, ResponseBytes: 4000}, {RequestBytes: 401, ResponseBytes: 9, Deltas: 5, Stream: true},
		} {
			got := Estimate(OperationEmbeddings, e)
			want := family.Usage{InputTokens: (e.RequestBytes + 3) / 4}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%+v: got %+v, want %+v", e, got, want)
			}
		}
	})
	t.Run("the estimate holds the two totals only", func(t *testing.T) {
		got := Estimate(OperationChatCompletions, family.Evidence{RequestBytes: 100, ResponseBytes: 100})
		if got.CacheReadTokens != 0 || got.CacheWriteTokens != 0 || got.ReasoningTokens != 0 || got.Extra != nil || got.Reported != nil {
			t.Errorf("%+v", got)
		}
	})
	t.Run("through the adapter", func(t *testing.T) {
		a := Adapter()
		for _, op := range a.Operations() {
			e := family.Evidence{RequestBytes: 9, ResponseBytes: 9, Deltas: 2, Stream: true}
			if got, want := a.EstimateUsage(op, e), Estimate(op.Name, e); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %+v != %+v", op.Name, got, want)
			}
		}
	})
}
