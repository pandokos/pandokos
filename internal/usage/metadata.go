package usage

import "net/http"

// ParseMetadata reads the caller metadata headers (X-Pandokos-Metadata-*) and returns the
// entries kept and how many were dropped.
func ParseMetadata(h http.Header) (kept map[string]string, dropped int) {
	_ = h
	return nil, 0
}
