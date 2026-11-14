package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/pandokos/pandokos/internal/family"
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

// edit replaces body[start:end] by ins.
type edit struct {
	start, end int
	ins        string
}

// usageEdit computes the splice that makes a stream report its usage (ADR 0024). stream says
// whether the last "stream" member is the literal true; the edit is nil when none is needed.
func usageEdit(body []byte, members []member, closeIdx int) (e *edit, stream bool) {
	sm, ok := last(members, "stream")
	if !ok || !bytes.Equal(body[sm.start:sm.end], []byte("true")) {
		return nil, false
	}
	so, ok := last(members, "stream_options")
	if !ok {
		return &edit{closeIdx, closeIdx, `,"stream_options":{` + includeUsage + `}`}, true
	}
	val := body[so.start:so.end]
	if bytes.Equal(val, []byte("null")) {
		return &edit{so.start, so.end, `{` + includeUsage + `}`}, true
	}
	inner, _, err := scanObject(val)
	if err != nil {
		return nil, true
	}
	iu, ok := last(inner, "include_usage")
	if !ok {
		ins := includeUsage
		if len(inner) > 0 {
			ins += ","
		}
		return &edit{so.start + 1, so.start + 1, ins}, true
	}
	if bytes.Equal(val[iu.start:iu.end], []byte("true")) {
		return nil, true
	}
	return &edit{so.start + iu.start, so.start + iu.end, "true"}, true
}

// PrepareRequest inspects a chat completions request body. For a stream that does not ask
// for usage it returns the body with the option added and strip=true. In every other case
// it returns body unchanged (the same slice). The change is a splice: every byte outside the
// inserted or replaced range is forwarded as received. It is Parse followed by Prepare("")
// for a body Parse accepts; a body that is a JSON object but has a problem with its model
// still gets the usage splice, and any other body is returned unchanged.
func PrepareRequest(body []byte) (out []byte, stream, strip bool) {
	members, closeIdx, err := scanObject(body)
	if err != nil {
		return body, false, false
	}
	e, stream := usageEdit(body, members, closeIdx)
	if e == nil {
		return body, stream, false
	}
	return splice(body, e.start, e.end, e.ins), stream, true
}

// Request is a chat completions request body that was read.
type Request struct {
	body   []byte
	model  string
	stream bool
	modelV member // byte range of the value of the model member
	usage  *edit
}

// Parse reads a chat completions request body. Every error wraps family.ErrInvalidRequest and
// carries a reason (family.InvalidReason): not_object, model_missing, model_duplicate or
// model_not_string. The body is not modified and is kept by the Request.
func Parse(body []byte) (*Request, error) { return ParseOperation(OperationChatCompletions, body) }

// ParseOperation reads the request body of an operation of the family. Every error wraps
// family.ErrInvalidRequest as for Parse, which stays and means chat completions. For responses a
// body whose last top-level background member is true is refused (background_not_supported); for
// embeddings the request never streams.
func ParseOperation(operation string, body []byte) (*Request, error) {
	members, closeIdx, err := scanObject(body)
	if err != nil {
		return nil, family.Invalid("not_object")
	}
	var mm member
	n := 0
	for _, m := range members {
		if m.key == "model" {
			mm = m
			n++
		}
	}
	switch {
	case n == 0:
		return nil, family.Invalid("model_missing")
	case n > 1:
		return nil, family.Invalid("model_duplicate")
	}
	raw := body[mm.start:mm.end]
	var model string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &model) != nil || model == "" {
		return nil, family.Invalid("model_not_string")
	}
	var e *edit
	var stream bool
	switch operation {
	case OperationChatCompletions:
		e, stream = usageEdit(body, members, closeIdx)
	case OperationResponses:
		if bm, ok := last(members, "background"); ok && bytes.Equal(body[bm.start:bm.end], []byte("true")) {
			return nil, family.Invalid("background_not_supported")
		}
		_, stream = usageEdit(body, members, closeIdx)
	}
	return &Request{body: body, model: model, stream: stream, modelV: mm, usage: e}, nil
}

// Model returns the model as the caller wrote it, JSON-decoded.
func (r *Request) Model() string { return r.model }

// Stream reports whether the caller asked for a stream.
func (r *Request) Stream() bool { return r.stream }

// Prepare returns the body to forward and whether the usage option was added by Pandokos. The
// usage option is added when the request is a stream that does not ask for it, and the value of
// the model member is replaced by upstreamModel when that is not empty and differs from
// Model(). Every other byte is forwarded as received; with no edit the input slice is returned.
func (r *Request) Prepare(upstreamModel string) (out []byte, strip bool) {
	var edits []edit
	if r.usage != nil {
		edits = append(edits, *r.usage)
		strip = true
	}
	if upstreamModel != "" && upstreamModel != r.model {
		edits = append(edits, edit{r.modelV.start, r.modelV.end, encodeString(upstreamModel)})
	}
	if len(edits) == 0 {
		return r.body, false
	}
	// Apply from the higher offset to the lower so earlier offsets stay valid.
	if len(edits) == 2 && edits[0].start < edits[1].start {
		edits[0], edits[1] = edits[1], edits[0]
	}
	out = r.body
	for _, e := range edits {
		out = splice(out, e.start, e.end, e.ins)
	}
	return out, strip
}

// encodeString returns the JSON string of s without escaping HTML characters.
func encodeString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil { // cannot happen for a string
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
