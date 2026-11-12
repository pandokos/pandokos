package openai

import (
	"encoding/json"
	"errors"

	"github.com/pandokos/pandokos/internal/family"
)

type wireUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
		AudioTokens  int64 `json:"audio_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens          int64 `json:"reasoning_tokens"`
		AudioTokens              int64 `json:"audio_tokens"`
		AcceptedPredictionTokens int64 `json:"accepted_prediction_tokens"`
		RejectedPredictionTokens int64 `json:"rejected_prediction_tokens"`
	} `json:"completion_tokens_details"`
}

var errNegativeUsage = errors.New("negative token count")

// normalize maps the provider's figures to family.Usage (sprint.md section 5.7).
func (w *wireUsage) normalize() (family.Usage, error) {
	pd, cd := &w.PromptTokensDetails, &w.CompletionTokensDetails
	for _, n := range []int64{
		w.PromptTokens, w.CompletionTokens, pd.CachedTokens, pd.AudioTokens,
		cd.ReasoningTokens, cd.AudioTokens, cd.AcceptedPredictionTokens, cd.RejectedPredictionTokens,
	} {
		if n < 0 {
			return family.Usage{}, errNegativeUsage
		}
	}
	u := family.Usage{
		InputTokens:     w.PromptTokens,
		OutputTokens:    w.CompletionTokens,
		CacheReadTokens: pd.CachedTokens,
		ReasoningTokens: cd.ReasoningTokens,
	}
	for name, n := range map[string]int64{
		"input_audio_tokens":         pd.AudioTokens,
		"output_audio_tokens":        cd.AudioTokens,
		"accepted_prediction_tokens": cd.AcceptedPredictionTokens,
		"rejected_prediction_tokens": cd.RejectedPredictionTokens,
	} {
		if n != 0 {
			if u.Extra == nil {
				u.Extra = map[string]int64{}
			}
			u.Extra[name] = n
		}
	}
	return u, nil
}

// ReadUsage reads usage from a complete chat completion JSON response.
// ok is false when the response is valid JSON without a usage object.
func ReadUsage(body []byte) (u family.Usage, ok bool, err error) {
	var r struct {
		Usage *wireUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return family.Usage{}, false, err
	}
	if r.Usage == nil {
		return family.Usage{}, false, nil
	}
	u, err = r.Usage.normalize()
	if err != nil {
		return family.Usage{}, false, err
	}
	return u, true, nil
}
