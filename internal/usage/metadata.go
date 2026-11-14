package usage

import (
	"net/http"
	"sort"
	"strings"
)

const (
	metadataPrefix  = "X-Pandokos-Metadata-"
	metadataDropped = "dropped"
	maxMetadata     = 8
	maxMetadataKey  = 32
	maxMetadataVal  = 128
	// maxDropped is the bound of the table's count (usage_record_metadata_dropped_range): the
	// count saturates, whatever the caller sends.
	maxDropped = 1000
)

// ParseMetadata reads the caller metadata headers (X-Pandokos-Metadata-*) and returns the
// entries kept and how many were dropped. The key is the rest of the header name, lower-cased,
// of [a-z0-9][a-z0-9_-]{0,31}; the value is one value of 1 to 128 printable ASCII bytes, trimmed
// of spaces. An entry that breaks a rule is dropped, and so are the entries after the eighth in key
// order. The count saturates at 1000. The header is not modified; nothing is allocated when no header qualifies.
func ParseMetadata(h http.Header) (kept map[string]string, dropped int) {
	type entry struct {
		key, val string
		n        int // values seen under this key
	}
	var entries []entry
	for name, vals := range h {
		if len(name) < len(metadataPrefix) || !strings.EqualFold(name[:len(metadataPrefix)], metadataPrefix) {
			continue
		}
		rest := name[len(metadataPrefix):]
		if strings.EqualFold(rest, metadataDropped) {
			continue
		}
		key := lowerASCII(rest)
		e := entry{key: key, n: len(vals)}
		if len(vals) == 1 {
			e.val = vals[0]
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, 0
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		// The same key under two spellings of the header name: more than one value.
		for i+1 < len(entries) && entries[i+1].key == e.key {
			e.n += entries[i+1].n
			i++
		}
		val := strings.Trim(e.val, " ")
		if e.n != 1 || !validMetadataKey(e.key) || !validMetadataValue(val) || len(kept) >= maxMetadata {
			dropped++
			continue
		}
		if kept == nil {
			kept = make(map[string]string, maxMetadata)
		}
		kept[e.key] = val
	}
	return kept, min(dropped, maxDropped)
}

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func validMetadataKey(k string) bool {
	if len(k) < 1 || len(k) > maxMetadataKey {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

func validMetadataValue(v string) bool {
	if len(v) < 1 || len(v) > maxMetadataVal {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}
