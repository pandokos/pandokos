// Package family holds what is common to all API families: the normalized usage and the
// error codes of the inference side.
package family

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Usage is the normalized form of ADR 0010. InputTokens includes CacheReadTokens and
// CacheWriteTokens; OutputTokens includes ReasoningTokens.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	Extra            map[string]int64 // provider-specific categories, non-zero entries only; nil if none
	Reported         map[string]int64 // the provider's own counters, keyed by path; nil if none
}

// FlattenUsage returns the integer members of a provider's usage object keyed by their path
// with dots, bounded as sprint 3.2 section 5.2 says; everything else in the object is dropped.
// It returns nil when nothing is kept.
func FlattenUsage(usageObject []byte) map[string]int64 {
	dec := json.NewDecoder(bytes.NewReader(usageObject))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	out := map[string]int64{}
	if err := flattenObject(dec, "", 1, out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}

const (
	maxReportedEntries = 32
	maxReportedDepth   = 3
	maxReportedKey     = 64
)

// flattenObject reads the members of an object whose opening brace was consumed. It stops, without
// error, when the map holds maxReportedEntries entries.
func flattenObject(dec *json.Decoder, prefix string, depth int, out map[string]int64) error {
	for dec.More() {
		if len(out) >= maxReportedEntries {
			return nil
		}
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := keyTok.(string)
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		usable := name != "" && validReportedKey(key)
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Number:
			if n, err := strconv.ParseInt(string(v), 10, 64); err == nil && n >= 0 && usable {
				out[key] = n
			}
		case json.Delim:
			if v == '{' && depth < maxReportedDepth && usable {
				if err := flattenObject(dec, key, depth+1, out); err != nil {
					return err
				}
				if len(out) >= maxReportedEntries {
					return nil
				}
				continue
			}
			if err := skipValue(dec, v); err != nil {
				return err
			}
		}
	}
	_, err := dec.Token() // the closing brace
	return err
}

// skipValue consumes the rest of the object or array whose opening delimiter was read.
func skipValue(dec *json.Decoder, open json.Delim) error {
	if open != '{' && open != '[' {
		return nil
	}
	for level := 1; level > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				level++
			} else {
				level--
			}
		}
	}
	return nil
}

func validReportedKey(k string) bool {
	if len(k) < 1 || len(k) > maxReportedKey {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// Evidence is what the gateway measured about a request whose provider reported no usage.
type Evidence struct {
	RequestBytes, ResponseBytes, Deltas int64
	Stream                              bool
}

// ErrorCode is a stable code of ADR 0020.
type ErrorCode string

// The codes the inference side can raise (ADR 0020 and its amendment).
const (
	CodeUnauthenticated      ErrorCode = "unauthenticated"        // 401
	CodeEndpointNotSupported ErrorCode = "endpoint_not_supported" // 404
	CodeRequestTooLarge      ErrorCode = "request_too_large"      // 413
	CodeUpstreamUnreachable  ErrorCode = "upstream_unreachable"   // 502
	CodeUpstreamTimeout      ErrorCode = "upstream_timeout"       // 504
	CodeInternal             ErrorCode = "internal"               // 500
	CodeForbidden            ErrorCode = "forbidden"              // 403
	CodeTenantSuspended      ErrorCode = "tenant_suspended"       // 403
	CodeUnavailable          ErrorCode = "unavailable"            // 503
	CodeModelNotAllowed      ErrorCode = "model_not_allowed"      // 403
	CodeNoCredential         ErrorCode = "no_credential"          //nolint:gosec // 409; an error code, not a credential
	CodeInvalidRequest       ErrorCode = "invalid_request"        // 400
	CodeRequestTimeout       ErrorCode = "request_timeout"        // 408
)

// Status returns the HTTP status of the code.
func (c ErrorCode) Status() int {
	switch c {
	case CodeUnauthenticated:
		return 401
	case CodeEndpointNotSupported:
		return 404
	case CodeRequestTooLarge:
		return 413
	case CodeUpstreamUnreachable:
		return 502
	case CodeUpstreamTimeout:
		return 504
	case CodeForbidden, CodeTenantSuspended, CodeModelNotAllowed:
		return 403
	case CodeNoCredential:
		return 409
	case CodeInvalidRequest:
		return 400
	case CodeRequestTimeout:
		return 408
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}

// Message returns the fixed message of the code. It never contains detail of the failure.
func (c ErrorCode) Message() string {
	switch c {
	case CodeUnauthenticated:
		return "Pandokos: missing or invalid credential."
	case CodeEndpointNotSupported:
		return "Pandokos: this endpoint is not supported."
	case CodeRequestTooLarge:
		return "Pandokos: the request body exceeds the configured limit."
	case CodeUpstreamUnreachable:
		return "Pandokos: the provider could not be reached."
	case CodeUpstreamTimeout:
		return "Pandokos: the provider did not answer in time."
	case CodeForbidden:
		return "Pandokos: the credential does not allow this operation."
	case CodeTenantSuspended:
		return "Pandokos: the tenant is not active."
	case CodeUnavailable:
		return "Pandokos: the request cannot be verified right now."
	case CodeModelNotAllowed:
		return "Pandokos: the model is not available to this credential."
	case CodeNoCredential:
		return "Pandokos: no usable provider credential for this provider."
	case CodeInvalidRequest:
		return `Pandokos: the request body is not accepted: it must be a JSON object with one string member "model", and must not ask for background processing.`
	case CodeRequestTimeout:
		return "Pandokos: the request body was not received in time."
	default:
		return "Pandokos: internal error."
	}
}

// Headers set on every error raised by Pandokos.
const (
	HeaderError     = "X-Pandokos-Error"
	HeaderRequestID = "X-Pandokos-Request-Id"
	// HeaderMetadataDropped tells the caller how many metadata headers were not kept.
	HeaderMetadataDropped = "X-Pandokos-Metadata-Dropped"
)
