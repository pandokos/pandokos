package openai

import (
	"bytes"
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

// wireResponsesUsage is the usage object of the responses operation.
type wireResponsesUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// wireEmbeddingsUsage is the usage object of the embeddings operation.
type wireEmbeddingsUsage struct {
	PromptTokens int64 `json:"prompt_tokens"`
}

var errNegativeUsage = errors.New("negative token count")

func anyNegative(ns ...int64) bool {
	for _, n := range ns {
		if n < 0 {
			return true
		}
	}
	return false
}

// normalize maps the provider's figures to family.Usage (sprint.md section 5.7).
func (w *wireUsage) normalize() (family.Usage, error) {
	pd, cd := &w.PromptTokensDetails, &w.CompletionTokensDetails
	if anyNegative(
		w.PromptTokens, w.CompletionTokens, pd.CachedTokens, pd.AudioTokens,
		cd.ReasoningTokens, cd.AudioTokens, cd.AcceptedPredictionTokens, cd.RejectedPredictionTokens,
	) {
		return family.Usage{}, errNegativeUsage
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

// normalizeUsage reads the raw usage object of an operation: the normalized figures and the
// provider's own counters. A negative count is an error.
func normalizeUsage(operation string, raw []byte) (family.Usage, error) {
	var u family.Usage
	switch operation {
	case OperationResponses:
		var w wireResponsesUsage
		if err := json.Unmarshal(raw, &w); err != nil {
			return family.Usage{}, err
		}
		if anyNegative(w.InputTokens, w.OutputTokens, w.InputTokensDetails.CachedTokens, w.OutputTokensDetails.ReasoningTokens) {
			return family.Usage{}, errNegativeUsage
		}
		u = family.Usage{InputTokens: w.InputTokens, OutputTokens: w.OutputTokens,
			CacheReadTokens: w.InputTokensDetails.CachedTokens, ReasoningTokens: w.OutputTokensDetails.ReasoningTokens}
	case OperationEmbeddings:
		var w wireEmbeddingsUsage
		if err := json.Unmarshal(raw, &w); err != nil {
			return family.Usage{}, err
		}
		if w.PromptTokens < 0 {
			return family.Usage{}, errNegativeUsage
		}
		u = family.Usage{InputTokens: w.PromptTokens}
	default:
		var w wireUsage
		if err := json.Unmarshal(raw, &w); err != nil {
			return family.Usage{}, err
		}
		var err error
		if u, err = w.normalize(); err != nil {
			return family.Usage{}, err
		}
	}
	u.Reported = family.FlattenUsage(raw)
	return u, nil
}

// isAbsent reports whether a raw JSON value is missing or null.
func isAbsent(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// ReadUsage reads usage from a complete chat completion JSON response.
// ok is false when the response is valid JSON without a usage object.
func ReadUsage(body []byte) (u family.Usage, ok bool, err error) {
	return ReadUsageOf(OperationChatCompletions, body)
}

// ReadUsageOf reads usage from a complete JSON response of an operation of the family.
// ok is false when the response is valid JSON without a usage object; a negative count is an
// error. ReadUsage stays and means chat completions.
func ReadUsageOf(operation string, body []byte) (u family.Usage, ok bool, err error) {
	var r struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return family.Usage{}, false, err
	}
	if isAbsent(r.Usage) {
		return family.Usage{}, false, nil
	}
	u, err = normalizeUsage(operation, r.Usage)
	if err != nil {
		return family.Usage{}, false, err
	}
	return u, true, nil
}
