package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const includeUsage = `"include_usage":true`

// member is one member of a JSON object: its key and the byte range of its value.
type member struct {
	key        string
	start, end int
}

// scanObject returns the members of the JSON object in b, in order, and the index of its
// closing brace. It fails if b is not exactly one JSON object.
func scanObject(b []byte) (members []member, closeIdx int, err error) {
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
		members = append(members, member{key: key, start: end - len(raw), end: end})
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

// last returns the last member with the given key: a repeated key resolves to its last
// occurrence, as in encoding/json.
func last(members []member, key string) (member, bool) {
	for i := len(members) - 1; i >= 0; i-- {
		if members[i].key == key {
			return members[i], true
		}
	}
	return member{}, false
}

func splice(b []byte, start, end int, ins string) []byte {
	out := make([]byte, 0, len(b)-(end-start)+len(ins))
	out = append(out, b[:start]...)
	out = append(out, ins...)
	return append(out, b[end:]...)
}

// PrepareRequest inspects a chat completions request body. For a stream that does not ask
// for usage it returns the body with the option added and strip=true. In every other case
// it returns body unchanged (the same slice). The change is a splice: every byte outside the
// inserted or replaced range is forwarded as received.
func PrepareRequest(body []byte) (out []byte, stream, strip bool) {
	members, closeIdx, err := scanObject(body)
	if err != nil {
		return body, false, false
	}
	sm, ok := last(members, "stream")
	if !ok || !bytes.Equal(body[sm.start:sm.end], []byte("true")) {
		return body, false, false
	}
	so, ok := last(members, "stream_options")
	if !ok {
		return splice(body, closeIdx, closeIdx, `,"stream_options":{`+includeUsage+`}`), true, true
	}
	val := body[so.start:so.end]
	if bytes.Equal(val, []byte("null")) {
		return splice(body, so.start, so.end, `{`+includeUsage+`}`), true, true
	}
	inner, _, err := scanObject(val)
	if err != nil {
		return body, true, false
	}
	iu, ok := last(inner, "include_usage")
	if !ok {
		ins := includeUsage
		if len(inner) > 0 {
			ins += ","
		}
		return splice(body, so.start+1, so.start+1, ins), true, true
	}
	if bytes.Equal(val[iu.start:iu.end], []byte("true")) {
		return body, true, false
	}
	return splice(body, so.start+iu.start, so.start+iu.end, "true"), true, true
}
