package family

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Member is one member of a JSON object: its key and the byte range of its value.
type Member struct {
	Key        string
	Start, End int // byte range of the value
}

// ScanObject returns the members of the JSON object in b, in order, and the index of its
// closing brace. It fails if b is not exactly one JSON object.
func ScanObject(b []byte) (members []Member, closeIdx int, err error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, 0, errors.New("not a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, 0, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, 0, errors.New("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, err
		}
		end := int(dec.InputOffset())
		members = append(members, Member{Key: key, Start: end - len(raw), End: end})
	}
	if _, err := dec.Token(); err != nil {
		return nil, 0, err
	}
	closeIdx = int(dec.InputOffset()) - 1
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, 0, errors.New("data after the JSON object")
	}
	return members, closeIdx, nil
}

// LastMember returns the last member with the given key: a repeated key resolves to its last
// occurrence, as in encoding/json.
func LastMember(ms []Member, key string) (Member, bool) {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Key == key {
			return ms[i], true
		}
	}
	return Member{}, false
}

// CaseConflict reports whether ms holds a member whose name is not spelled exactly as a name of
// read but would be taken for it by a reader that ignores case. The names are compared three ways,
// because readers differ: by Unicode simple folding (strings.EqualFold, as Go does), by upper
// casing and by lower casing both (as Java equalsIgnoreCase or Python str.upper do). The three
// differ on U+0131 (dotless i), U+0130 (dotted I), U+017F (long s) and U+212A (Kelvin sign), so a
// name that any of them equates with a name of read is a conflict.
func CaseConflict(ms []Member, read ...string) bool {
	for _, m := range ms {
		for _, name := range read {
			if m.Key != name && sameIgnoringCase(m.Key, name) {
				return true
			}
		}
	}
	return false
}

func sameIgnoringCase(a, b string) bool {
	return strings.EqualFold(a, b) || strings.ToUpper(a) == strings.ToUpper(b) || strings.ToLower(a) == strings.ToLower(b) //nolint:staticcheck // SA6005: the explicit upper and lower comparisons are the point, they differ from simple folding
}

// ParseModel applies the model rules of sprint 3.1 section 5.8 to the members of a request body:
// exactly one top-level member "model", whose value is a non-empty JSON string. It returns the
// decoded model and its member, or an Invalid error whose reason is model_missing,
// model_duplicate or model_not_string.
func ParseModel(body []byte, ms []Member) (model string, m Member, err error) {
	n := 0
	for _, c := range ms {
		if c.Key == "model" {
			m = c
			n++
		}
	}
	switch {
	case n == 0:
		return "", Member{}, Invalid("model_missing")
	case n > 1:
		return "", Member{}, Invalid("model_duplicate")
	}
	raw := body[m.Start:m.End]
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &model) != nil || model == "" {
		return "", Member{}, Invalid("model_not_string")
	}
	return model, m, nil
}

// PositiveInt reads the last top-level member key as a plain positive integer: a JSON number
// written with decimal digits alone (no sign, fraction or exponent), of at least 1 and at most
// maxDigits digits. Anything else, a missing member included, is "not set" (0, false). The last
// occurrence of a repeated member decides, as in encoding/json.
func PositiveInt(body []byte, ms []Member, key string, maxDigits int) (int64, bool) {
	m, ok := LastMember(ms, key)
	if !ok {
		return 0, false
	}
	raw := body[m.Start:m.End]
	if len(raw) == 0 || len(raw) > maxDigits {
		return 0, false
	}
	var n int64
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	if n < 1 {
		return 0, false
	}
	return n, true
}

// IsLiteral reports whether the value of m is exactly the literal lit (such as "true").
func IsLiteral(body []byte, m Member, lit string) bool {
	return bytes.Equal(body[m.Start:m.End], []byte(lit))
}

// Splice returns a copy of b with b[start:end] replaced by ins.
func Splice(b []byte, start, end int, ins string) []byte {
	out := make([]byte, 0, len(b)-(end-start)+len(ins))
	out = append(out, b[:start]...)
	out = append(out, ins...)
	return append(out, b[end:]...)
}

// EncodeString returns the JSON string of s without escaping HTML characters.
func EncodeString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil { // cannot happen for a string
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
