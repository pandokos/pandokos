package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"

	"github.com/pandokos/pandokos/internal/family"
)

// errUnusable is the error of a usage object that cannot be used. It never holds the body.
var errUnusable = errors.New("anthropic: unusable usage")

// wireUsage is the usage object of the Messages API (sprint 3.3 section 5.4). A missing or null
// member counts 0, so the members are pointers.
type wireUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreation            *struct {
		Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse *struct {
		WebSearchRequests *int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// normalize maps the provider's figures to family.Usage. A negative count, or a total that does
// not fit, is an error.
func (w *wireUsage) normalize() (family.Usage, error) {
	var h1, ws int64
	if w.CacheCreation != nil {
		h1 = val(w.CacheCreation.Ephemeral1h)
	}
	if w.ServerToolUse != nil {
		ws = val(w.ServerToolUse.WebSearchRequests)
	}
	in, cw, cr, out := val(w.InputTokens), val(w.CacheCreationInputTokens), val(w.CacheReadInputTokens), val(w.OutputTokens)
	for _, n := range []int64{in, cw, cr, out, h1, ws} {
		if n < 0 {
			return family.Usage{}, errUnusable
		}
	}
	if in > math.MaxInt64-cw || in+cw > math.MaxInt64-cr {
		return family.Usage{}, errUnusable
	}
	u := family.Usage{InputTokens: in + cw + cr, OutputTokens: out, CacheReadTokens: cr, CacheWriteTokens: cw}
	for name, n := range map[string]int64{"cache_write_1h_tokens": h1, "web_search_requests": ws} {
		if n != 0 {
			if u.Extra == nil {
				u.Extra = map[string]int64{}
			}
			u.Extra[name] = n
		}
	}
	return u, nil
}

// normalizeUsage reads a raw usage object: the normalized figures and the provider's own
// counters.
func normalizeUsage(raw []byte) (family.Usage, error) {
	var w wireUsage
	if err := json.Unmarshal(raw, &w); err != nil {
		return family.Usage{}, errUnusable
	}
	u, err := w.normalize()
	if err != nil {
		return family.Usage{}, err
	}
	u.Reported = family.FlattenUsage(raw)
	return u, nil
}

// isAbsent reports whether a raw JSON value is missing or null.
func isAbsent(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// ReadUsageOf reads usage from a complete JSON answer of an operation. ok is false, without an
// error, for token counting and for an answer without a usage object; a usage that cannot be
// used is an error.
func ReadUsageOf(operation string, body []byte) (u family.Usage, ok bool, err error) {
	if operation != OperationMessages {
		return family.Usage{}, false, nil
	}
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return family.Usage{}, false, errUnusable
	}
	if isAbsent(r.Usage) {
		return family.Usage{}, false, nil
	}
	u, err = normalizeUsage(r.Usage)
	if err != nil {
		return family.Usage{}, false, err
	}
	return u, true, nil
}
