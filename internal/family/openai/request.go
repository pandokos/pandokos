package openai

import (
	"bytes"

	"github.com/pandokos/pandokos/internal/family"
)

const includeUsage = `"include_usage":true`

// The names of the members of a request body that this package reads (sprint 12.2, section 5.5).
// Each is used both in ReadMembers and at the call site that reads it, so the two cannot drift;
// "model" is read by family.ParseModel, which takes the literal.
const (
	memberModel               = "model"
	memberStream              = "stream"
	memberStreamOptions       = "stream_options"
	memberBackground          = "background"
	memberMaxOutputTokens     = "max_output_tokens"
	memberMaxCompletionTokens = "max_completion_tokens"
	memberMaxTokens           = "max_tokens"
	memberN                   = "n"
	memberIncludeUsage        = "include_usage"
)

// ReadMembers is the set of top-level members of a request body that this family reads.
var ReadMembers = []string{
	memberModel, memberStream, memberStreamOptions, memberBackground,
	memberMaxOutputTokens, memberMaxCompletionTokens, memberMaxTokens, memberN,
}

// ReadStreamOptions is the set of members of the object stream_options that this family reads.
var ReadStreamOptions = []string{memberIncludeUsage}

// edit replaces body[start:end] by ins.
type edit struct {
	start, end int
	ins        string
}

// memberCaseConflict reports whether a top-level member, or a member of stream_options when that is
// an object, differs only in case from a name this family reads.
func memberCaseConflict(body []byte, members []family.Member) bool {
	if family.CaseConflict(members, ReadMembers...) {
		return true
	}
	for _, m := range members {
		if m.Key != memberStreamOptions {
			continue
		}
		if inner, _, err := family.ScanObject(body[m.Start:m.End]); err == nil && family.CaseConflict(inner, ReadStreamOptions...) {
			return true
		}
	}
	return false
}

// usageEdit computes the splice that makes a stream report its usage (ADR 0024). stream says
// whether the last "stream" member is the literal true; the edit is nil when none is needed.
func usageEdit(body []byte, members []family.Member, closeIdx int) (e *edit, stream bool) {
	sm, ok := family.LastMember(members, memberStream)
	if !ok || !bytes.Equal(body[sm.Start:sm.End], []byte("true")) {
		return nil, false
	}
	so, ok := family.LastMember(members, memberStreamOptions)
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
	iu, ok := family.LastMember(inner, memberIncludeUsage)
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
	bound  family.OutputBound
}

// Parse reads a chat completions request body. Every error wraps family.ErrInvalidRequest and
// carries a reason (family.InvalidReason): not_object, member_case, model_missing,
// model_duplicate or model_not_string. The body is not modified and is kept by the Request.
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
	if memberCaseConflict(body, members) {
		return nil, family.Invalid("member_case")
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
		if bm, ok := family.LastMember(members, memberBackground); ok && bytes.Equal(body[bm.Start:bm.End], []byte("true")) {
			return nil, family.Invalid("background_not_supported")
		}
		_, stream = usageEdit(body, members, closeIdx)
	}
	return &Request{body: body, model: model, stream: stream, modelV: mm, usage: e, bound: outputBoundOf(operation, body, members)}, nil
}

// maxBoundDigits is the longest integer read as an output limit; maxChoices the largest n.
const (
	maxBoundDigits = 12
	maxChoices     = 128
)

// outputBoundOf reads the output limit of the members already scanned: the table of sprint 5.1
// section 5.3. Nothing else of the body is read.
func outputBoundOf(operation string, body []byte, members []family.Member) family.OutputBound {
	b := family.OutputBound{Choices: 1}
	switch operation {
	case OperationEmbeddings:
		b.None = true
	case OperationResponses:
		b.Tokens, _ = family.PositiveInt(body, members, memberMaxOutputTokens, maxBoundDigits)
	default: // chat completions
		if n, ok := family.PositiveInt(body, members, memberMaxCompletionTokens, maxBoundDigits); ok {
			b.Tokens = n
		} else {
			b.Tokens, _ = family.PositiveInt(body, members, memberMaxTokens, maxBoundDigits)
		}
		if n, ok := family.PositiveInt(body, members, memberN, 3); ok && n <= maxChoices {
			b.Choices = n
		}
	}
	return b
}

// Model returns the model as the caller wrote it, JSON-decoded.
func (r *Request) Model() string { return r.model }

// Stream reports whether the caller asked for a stream.
func (r *Request) Stream() bool { return r.stream }

// OutputBound returns what the request says about the output it may produce.
func (r *Request) OutputBound() family.OutputBound { return r.bound }

// Prepare returns the body to forward and whether the usage option was added by Pandokos. The
// usage option is added when p.StreamUsage is true and the request is a stream that does not ask for it, and the value of
// the model member is replaced by upstreamModel when that is not empty and differs from
// Model(). Every other byte is forwarded as received; with no edit the input slice is returned.
func (r *Request) Prepare(p family.Preparation) (out []byte, strip bool) {
	upstreamModel := p.UpstreamModel
	var edits []edit
	if r.usage != nil && p.StreamUsage {
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
