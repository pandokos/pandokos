package openai

import (
	"bytes"

	"github.com/pandokos/pandokos/internal/family"
)

const includeUsage = `"include_usage":true`

// edit replaces body[start:end] by ins.
type edit struct {
	start, end int
	ins        string
}

// usageEdit computes the splice that makes a stream report its usage (ADR 0024). stream says
// whether the last "stream" member is the literal true; the edit is nil when none is needed.
func usageEdit(body []byte, members []family.Member, closeIdx int) (e *edit, stream bool) {
	sm, ok := family.LastMember(members, "stream")
	if !ok || !bytes.Equal(body[sm.Start:sm.End], []byte("true")) {
		return nil, false
	}
	so, ok := family.LastMember(members, "stream_options")
	if !ok {
		return &edit{closeIdx, closeIdx, `,"stream_options":{` + includeUsage + `}`}, true
	}
	val := body[so.Start:so.End]
	if bytes.Equal(val, []byte("null")) {
		return &edit{so.Start, so.End, `{` + includeUsage + `}`}, true
	}
	inner, _, err := family.ScanObject(val)
	if err != nil {
		return nil, true
	}
	iu, ok := family.LastMember(inner, "include_usage")
	if !ok {
		ins := includeUsage
		if len(inner) > 0 {
			ins += ","
		}
		return &edit{so.Start + 1, so.Start + 1, ins}, true
	}
	if bytes.Equal(val[iu.Start:iu.End], []byte("true")) {
		return nil, true
	}
	return &edit{so.Start + iu.Start, so.Start + iu.End, "true"}, true
}

// PrepareRequest inspects a chat completions request body. For a stream that does not ask
// for usage it returns the body with the option added and strip=true. In every other case
// it returns body unchanged (the same slice). The change is a splice: every byte outside the
// inserted or replaced range is forwarded as received. It is Parse followed by Prepare("")
// for a body Parse accepts; a body that is a JSON object but has a problem with its model
// still gets the usage splice, and any other body is returned unchanged.
func PrepareRequest(body []byte) (out []byte, stream, strip bool) {
	members, closeIdx, err := family.ScanObject(body)
	if err != nil {
		return body, false, false
	}
	e, stream := usageEdit(body, members, closeIdx)
	if e == nil {
		return body, stream, false
	}
	return family.Splice(body, e.start, e.end, e.ins), stream, true
}

// Request is a chat completions request body that was read.
type Request struct {
	body   []byte
	model  string
	stream bool
	modelV family.Member // byte range of the value of the model member
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
	members, closeIdx, err := family.ScanObject(body)
	if err != nil {
		return nil, family.Invalid("not_object")
	}
	model, mm, err := family.ParseModel(body, members)
	if err != nil {
		return nil, err
	}
	var e *edit
	var stream bool
	switch operation {
	case OperationChatCompletions:
		e, stream = usageEdit(body, members, closeIdx)
	case OperationResponses:
		if bm, ok := family.LastMember(members, "background"); ok && bytes.Equal(body[bm.Start:bm.End], []byte("true")) {
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
func (r *Request) Prepare(p family.Preparation) (out []byte, strip bool) {
	upstreamModel := p.UpstreamModel
	var edits []edit
	if r.usage != nil {
		edits = append(edits, *r.usage)
		strip = true
	}
	if upstreamModel != "" && upstreamModel != r.model {
		edits = append(edits, edit{r.modelV.Start, r.modelV.End, family.EncodeString(upstreamModel)})
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
		out = family.Splice(out, e.start, e.end, e.ins)
	}
	return out, strip
}
