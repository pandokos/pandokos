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
