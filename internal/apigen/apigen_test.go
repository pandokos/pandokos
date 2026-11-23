package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	specPath = "../../api/openapi/admin.v1.yaml"
	genPath  = "../adminapi/adminapi.gen.go"
)

// TestGeneratedCodeIsCurrent is T-210: generating from the document gives exactly the committed
// bytes.
func TestGeneratedCodeIsCurrent(t *testing.T) {
	got, err := generate(specPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/adminapi/adminapi.gen.go is not what the generator makes from api/openapi/admin.v1.yaml: run make generate")
	}
}

// minimal is a small valid document in the accepted subset. Each defect of T-211 is a copy of
// it with one change.
const minimal = `openapi: 3.1.0
info: {title: test, version: 0.0.1}
paths:
  /v1/things:
    get:
      operationId: listThings
      x-pandokos-permission: thing:read
      responses:
        "200": {$ref: '#/components/responses/Things'}
        "400": {$ref: '#/components/responses/Err'}
  /v1/things/{id}:
    parameters:
      - name: id
        in: path
        required: true
        schema: {$ref: '#/components/schemas/ID'}
    get:
      operationId: getThing
      x-pandokos-permission: thing:read
      responses:
        "200": {$ref: '#/components/responses/Thing'}
        "404": {$ref: '#/components/responses/Err'}
components:
  responses:
    Things:
      description: Things.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/ThingList'}
    Thing:
      description: A thing.
      content:
        application/json:
          schema: {$ref: '#/components/schemas/Thing'}
    Err:
      description: A problem.
      content:
        application/problem+json:
          schema: {$ref: '#/components/schemas/Problem'}
  schemas:
    ID:
      type: string
      pattern: '^[0-9a-f]{8}$'
    Thing:
      type: object
      additionalProperties: false
      required: [id]
      properties:
        id: {$ref: '#/components/schemas/ID'}
    ThingList:
      type: object
      additionalProperties: false
      required: [items]
      properties:
        items:
          type: array
          items: {$ref: '#/components/schemas/Thing'}
    ProblemCode:
      type: string
      enum: [not_found, invalid_request]
    Problem:
      type: object
      additionalProperties: false
      required: [code]
      properties:
        code: {$ref: '#/components/schemas/ProblemCode'}
`

func writeSpec(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// replaceOnce changes doc and fails the test when old is not in it, so that a defect cannot
// silently stop being one.
func replaceOnce(t *testing.T, doc, old, repl string) string {
	t.Helper()
	if strings.Count(doc, old) != 1 {
		t.Fatalf("the minimal document does not contain %q exactly once", old)
	}
	return strings.Replace(doc, old, repl, 1)
}

// TestRejectsOutsideTheSubset is T-211.
func TestRejectsOutsideTheSubset(t *testing.T) {
	if _, err := generate(writeSpec(t, minimal)); err != nil {
		t.Fatalf("the minimal document must be accepted: %v", err)
	}
	patchBody := func(schema string) string {
		return `    patch:
      operationId: updateThing
      x-pandokos-permission: thing:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/` + schema + `'}
      responses:
        "200": {$ref: '#/components/responses/Thing'}
`
	}
	postWithObjectBody := `    post:
      operationId: createThing
      x-pandokos-permission: thing:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Nested'}
      responses:
        "201": {$ref: '#/components/responses/Thing'}
`
	// Sprint 4.3: x-pandokos-reason: required. A POST that closes a thing, with the reason schema given.
	withReason := func(marker, body, schema string) string {
		op := "    post:\n      operationId: closeThing\n      x-pandokos-permission: thing:write\n" + marker + body +
			"      responses:\n        \"200\": {$ref: '#/components/responses/Thing'}\n"
		doc := replaceOnce(t, minimal, "        \"404\": {$ref: '#/components/responses/Err'}\ncomponents:", "        \"404\": {$ref: '#/components/responses/Err'}\n"+op+"components:")
		return replaceOnce(t, doc, "    ProblemCode:\n", schema+"    ProblemCode:\n")
	}
	const required = "      x-pandokos-reason: required\n"
	closingBody := "      requestBody:\n        required: true\n        content:\n          application/json:\n            schema: {$ref: '#/components/schemas/Closing'}\n"
	closing := func(props, req string) string {
		return "    Closing:\n      type: object\n      additionalProperties: false\n" + req + "      properties:\n" + props
	}
	goodReason := closing("        reason: {type: string, minLength: 1, maxLength: 500}\n", "      required: [reason]\n")
	cases := []struct {
		name   string
		doc    string
		wantIn []string // every string must be in the error text
	}{
		{"no permission", replaceOnce(t, minimal, "      operationId: listThings\n      x-pandokos-permission: thing:read\n", "      operationId: listThings\n"),
			[]string{"GET /v1/things", "x-pandokos-permission"}},
		{"unknown keyword", replaceOnce(t, minimal, "      operationId: listThings\n", "      operationId: listThings\n      deprecated: true\n"),
			[]string{"deprecated"}},
		{"object without additionalProperties", replaceOnce(t, minimal, "    Thing:\n      type: object\n      additionalProperties: false\n", "    Thing:\n      type: object\n"),
			[]string{"schema Thing", "additionalProperties"}},
		{"two success responses", replaceOnce(t, minimal, "        \"400\": {$ref: '#/components/responses/Err'}\n  /v1/things/{id}:", "        \"201\": {$ref: '#/components/responses/Things'}\n  /v1/things/{id}:"),
			[]string{"GET /v1/things", "2xx"}},
		{"error response that is not a problem", replaceOnce(t, minimal, "    Err:\n      description: A problem.\n      content:\n        application/problem+json:\n          schema: {$ref: '#/components/schemas/Problem'}\n",
			"    Err:\n      description: A problem.\n      content:\n        application/json:\n          schema: {$ref: '#/components/schemas/Thing'}\n"),
			[]string{"application/problem+json"}},
		{"update schema used as another body", replaceOnce(t, replaceOnce(t, minimal,
			"        \"404\": {$ref: '#/components/responses/Err'}\ncomponents:", "        \"404\": {$ref: '#/components/responses/Err'}\n"+patchBody("Nested")+postWithObjectBody+"components:"),
			"    ProblemCode:\n", "    Nested:\n      type: object\n      additionalProperties: false\n      properties:\n        label: {type: string}\n    ProblemCode:\n"),
			[]string{"Nested", "update schema", "POST /v1/things"}},
		{"update schema used as a member", replaceOnce(t, replaceOnce(t, minimal,
			"        \"404\": {$ref: '#/components/responses/Err'}\ncomponents:", "        \"404\": {$ref: '#/components/responses/Err'}\n"+patchBody("Nested")+"components:"),
			"    ProblemCode:\n", "    Nested:\n      type: object\n      additionalProperties: false\n      properties:\n        label: {type: string}\n    Holder:\n      type: object\n      additionalProperties: false\n      properties:\n        inner: {$ref: '#/components/schemas/Nested'}\n    ProblemCode:\n"),
			[]string{"Nested", "update schema", "Holder"}},
		{"optional time member of an update schema", replaceOnce(t, replaceOnce(t, minimal,
			"        \"404\": {$ref: '#/components/responses/Err'}\ncomponents:", "        \"404\": {$ref: '#/components/responses/Err'}\n"+patchBody("Nested")+"components:"),
			"    ProblemCode:\n", "    Nested:\n      type: object\n      additionalProperties: false\n      properties:\n        at: {type: string, format: date-time}\n    ProblemCode:\n"),
			[]string{"property at", "update schema"}},
		{"path parameter without a pattern", replaceOnce(t, minimal, "    ID:\n      type: string\n      pattern: '^[0-9a-f]{8}$'\n", "    ID:\n      type: string\n"),
			[]string{"parameter id"}},
		{"duplicate operationId", replaceOnce(t, minimal, "operationId: getThing", "operationId: listThings"),
			[]string{"operationId"}},
		{"openapi 3.0.3", replaceOnce(t, minimal, "openapi: 3.1.0", "openapi: 3.0.3"),
			[]string{"3.1.0"}},
		{"x-pandokos-reason with another value", withReason("      x-pandokos-reason: optional\n", closingBody, goodReason),
			[]string{"x-pandokos-reason", "required"}},
		{"x-pandokos-reason on an operation without a body", withReason(required, "", goodReason),
			[]string{"x-pandokos-reason", "body"}},
		{"x-pandokos-reason on an optional body", withReason(required, strings.Replace(closingBody, "required: true", "required: false", 1), goodReason),
			[]string{"x-pandokos-reason", "body"}},
		{"x-pandokos-reason with a reason that is not required", withReason(required, closingBody, closing("        reason: {type: string}\n", "")),
			[]string{"x-pandokos-reason", "reason"}},
		{"x-pandokos-reason with a body that has no reason", withReason(required, closingBody, closing("        note: {type: string}\n", "      required: [note]\n")),
			[]string{"x-pandokos-reason", "reason"}},
		{"x-pandokos-reason with a reason that is not a string", withReason(required, closingBody, closing("        reason: {type: integer}\n", "      required: [reason]\n")),
			[]string{"x-pandokos-reason", "string"}},
		{"x-pandokos-reason on a GET", replaceOnce(t, minimal, "      operationId: listThings\n", "      operationId: listThings\n      x-pandokos-reason: required\n"),
			[]string{"x-pandokos-reason"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := generate(writeSpec(t, c.doc))
			if err == nil {
				t.Fatal("the document was accepted")
			}
			for _, w := range c.wantIn {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error %q does not name %q", err, w)
				}
			}
		})
	}

	// The construct, accepted, generates the flag and the check that the reason is not empty once
	// its white space is trimmed (T-883).
	out, err := generate(writeSpec(t, withReason(required, closingBody, goodReason)))
	if err != nil {
		t.Fatalf("x-pandokos-reason on a POST with a required reason: %v", err)
	}
	text := string(out)
	for _, want := range []string{"ReasonRequired: true", "requireReason(in.Body.Reason)"} {
		if !strings.Contains(text, want) {
			t.Errorf("the generated code lacks %q", want)
		}
	}
	if plain, err := generate(writeSpec(t, withReason("", closingBody, goodReason))); err != nil || strings.Contains(string(plain), "ReasonRequired: true") || strings.Contains(string(plain), "requireReason(in.Body.Reason)") {
		t.Errorf("an operation without the construct: %v (it must not set the flag nor generate the check)", err)
	}
}

// withSchemas returns the minimal document with a PATCH and a POST operation on /v1/things and
// the given component schemas (YAML text, indented as the others).
func withSchemas(t *testing.T, schemas string) string {
	t.Helper()
	ops := `    patch:
      operationId: updateThing
      x-pandokos-permission: thing:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ThingUpdate'}
      responses:
        "200": {$ref: '#/components/responses/Thing'}
    post:
      operationId: createThing
      x-pandokos-permission: thing:write
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ThingCreate'}
      responses:
        "201": {$ref: '#/components/responses/Thing'}
`
	doc := replaceOnce(t, minimal, "        \"404\": {$ref: '#/components/responses/Err'}\ncomponents:", "        \"404\": {$ref: '#/components/responses/Err'}\n"+ops+"components:")
	return replaceOnce(t, doc, "    ProblemCode:\n", schemas+"    ProblemCode:\n")
}

const thingSchemas = `    Kind:
      type: string
      enum: [a, b]
    Rate:
      type: object
      additionalProperties: false
      required: [value]
      properties:
        value: {type: string, pattern: '^[0-9]+$'}
    ThingCreate:
      type: object
      additionalProperties: false
      required: [name]
      properties:
        name: {type: string, minLength: 1}
        note: {type: string, maxLength: 5, x-pandokos-not-empty: true}
        rates:
          type: array
          minItems: 1
          maxItems: 3
          items: {$ref: '#/components/schemas/Rate'}
        extra: {$ref: '#/components/schemas/Rate'}
        live: {type: boolean, default: true}
    ThingUpdate:
      type: object
      additionalProperties: false
      properties:
        name: {type: string, minLength: 1}
        note: {type: string, maxLength: 5}
        kind: {$ref: '#/components/schemas/Kind'}
        size: {type: integer, minimum: 0}
        on: {type: boolean}
        tags:
          type: array
          maxItems: 2
          items: {type: string}
        gone: {type: [string, "null"]}
        live: {type: boolean, default: true}
`

// TestOptionalMembersGenerated is part of T-384: the members of an update schema are Optional and
// validated only when set; a create schema is unchanged; nested objects are validated with
// prefixed names.
func TestOptionalMembersGenerated(t *testing.T) {
	src, err := generate(writeSpec(t, withSchemas(t, thingSchemas)))
	if err != nil {
		t.Fatal(err)
	}
	text := regexp.MustCompile(`[ \t]+`).ReplaceAllString(string(src), " ") // gofmt aligns struct fields
	for _, want := range []string{
		"Name Optional[string] `json:\"name,omitzero\"`",
		"Kind Optional[Kind] `json:\"kind,omitzero\"`",
		"Size Optional[int] `json:\"size,omitzero\"`",
		"On Optional[bool] `json:\"on,omitzero\"`",
		"Tags Optional[[]string] `json:\"tags,omitzero\"`",
		"Gone Nullable[string] `json:\"gone,omitzero\"`",
		"if v.Note.Set {",
		"runeCount(v.Note.Value) > 5",
		"Name string `json:\"name\"`", // the create schema keeps plain members
		"Note string `json:\"note,omitempty\"`",
		"Rates []Rate `json:\"rates,omitempty\"`",
		"Extra *Rate `json:\"extra,omitempty\"`",
		"if v.Extra != nil {",
		"for i := range v.Rates {",
		// The presence of the required members of nested objects is checked by decodeBody.
		`{Name: "rates", Many: true, Required: requiredRate, Nested: nestedRate}`,
		`{Name: "extra", Required: requiredRate, Nested: nestedRate}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the generated code does not contain %q", want)
		}
	}
	if strings.Contains(text, "Optional[Rate]") || strings.Contains(text, "Optional[[]Rate]") {
		t.Error("a create schema member is Optional")
	}
}

// TestUpdateSchemaRefusals is part of T-384: a null-able member stays Nullable, and a schema used
// as a PATCH body and elsewhere is refused (the cases of T-211 name the other refusals).
func TestUpdateSchemaRefusals(t *testing.T) {
	doc := replaceOnce(t, withSchemas(t, thingSchemas), "schema: {$ref: '#/components/schemas/ThingCreate'}", "schema: {$ref: '#/components/schemas/ThingUpdate'}")
	_, err := generate(writeSpec(t, doc))
	if err == nil || !strings.Contains(err.Error(), "ThingUpdate") || !strings.Contains(err.Error(), "POST /v1/things") {
		t.Fatalf("a PATCH body also used by a POST was accepted or badly reported: %v", err)
	}
}

// TestTrueByDefaultBoolean (and the marker x-pandokos-not-empty): an optional boolean that defaults to true is always written, so that
// false can be sent, and a request body starts with it set, so that an absent member means true. In
// an update schema it stays Optional.
func TestTrueByDefaultBoolean(t *testing.T) {
	src, err := generate(writeSpec(t, withSchemas(t, thingSchemas)))
	if err != nil {
		t.Fatal(err)
	}
	text := regexp.MustCompile(`[ \t]+`).ReplaceAllString(string(src), " ")
	for _, want := range []string{"Live bool `json:\"live\"`", "Live Optional[bool] `json:\"live,omitzero\"`", "in.Body.Live = true",
		"func (*ThingCreate) sentEmpty() []string { return []string{\"note\"} }"} {
		if !strings.Contains(text, want) {
			t.Errorf("the generated code does not contain %q", want)
		}
	}
	if strings.Contains(text, "live,omitempty") {
		t.Error("an optional boolean that defaults to true can be omitted when false")
	}
}

// TestRejectsSprint42Constructs is the rejecting half of T-822: each of the four constructs of
// sprint 4.2 has a form that the generator refuses.
func TestRejectsSprint42Constructs(t *testing.T) {
	publicList := replaceOnce(t, minimal, "      operationId: listThings\n      x-pandokos-permission: thing:read\n", "      operationId: listThings\n      x-pandokos-public: true\n      security: []\n")
	if _, err := generate(writeSpec(t, publicList)); err != nil {
		t.Fatalf("a public operation without a permission must be accepted: %v", err)
	}
	idemParam := "    IdempotencyKey:\n      name: Idempotency-Key\n      in: header\n      schema: {type: string, pattern: '^[\\x21-\\x7e]{1,128}$'}\n"
	withParams := func(doc string) string {
		return replaceOnce(t, doc, "components:\n  responses:", "components:\n  parameters:\n"+idemParam+"  responses:")
	}
	redirect := "    Redirect:\n      description: Redirect.\n      headers:\n        Location: {schema: {type: string}}\n"
	withRedirect := func(doc string) string {
		return replaceOnce(t, doc, "    Things:\n      description: Things.", redirect+"    Things:\n      description: Things.")
	}
	cases := []struct {
		name   string
		doc    string
		wantIn []string
	}{
		{"a public operation with a permission", replaceOnce(t, minimal, "      operationId: listThings\n", "      operationId: listThings\n      x-pandokos-public: true\n"),
			[]string{"x-pandokos-public", "x-pandokos-permission"}},
		{"a public operation that says false", replaceOnce(t, minimal, "      operationId: listThings\n      x-pandokos-permission: thing:read\n", "      operationId: listThings\n      x-pandokos-public: false\n"),
			[]string{"x-pandokos-public"}},
		{"a 303 beside a 2xx", replaceOnce(t, withRedirect(publicList), "        \"400\": {$ref: '#/components/responses/Err'}\n  /v1/things/{id}:", "        \"303\": {$ref: '#/components/responses/Redirect'}\n  /v1/things/{id}:"),
			[]string{"303"}},
		{"a 303 on an operation that is not public", replaceOnce(t, withRedirect(minimal), "        \"200\": {$ref: '#/components/responses/Things'}\n        \"400\"", "        \"303\": {$ref: '#/components/responses/Redirect'}\n        \"400\""),
			[]string{"303"}},
		{"the header parameter on an operation that does not answer 201", replaceOnce(t, withParams(minimal), "      operationId: listThings\n      x-pandokos-permission: thing:read\n",
			"      operationId: listThings\n      x-pandokos-permission: thing:read\n      parameters:\n        - {$ref: '#/components/parameters/IdempotencyKey'}\n"),
			[]string{"Idempotency", "201"}},
		{"a lenient query without x-pandokos-public", replaceOnce(t, minimal, "      operationId: listThings\n", "      operationId: listThings\n      x-pandokos-lenient-query: true\n"),
			[]string{"x-pandokos-lenient-query"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := generate(writeSpec(t, c.doc))
			if err == nil {
				t.Fatal("the document was accepted")
			}
			for _, w := range c.wantIn {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error %q does not name %q", err, w)
				}
			}
		})
	}

	// The four constructs, accepted, generate what section 5.12 says.
	good := replaceOnce(t, withRedirect(withParams(publicList)), "        \"200\": {$ref: '#/components/responses/Things'}\n        \"400\": {$ref: '#/components/responses/Err'}\n  /v1/things/{id}:", "        \"303\": {$ref: '#/components/responses/Redirect'}\n  /v1/things/{id}:")
	good = replaceOnce(t, good, "      x-pandokos-public: true\n      security: []\n", "      x-pandokos-public: true\n      x-pandokos-lenient-query: true\n      security: []\n")
	out, err := generate(writeSpec(t, good))
	if err != nil {
		t.Fatalf("the constructs together: %v", err)
	}
	text := string(out)
	for _, want := range []string{"Public: true", "Status: 303", "type Redirect struct{ Location string }", "(*Redirect, error)"} {
		if !strings.Contains(text, want) {
			t.Errorf("the generated code lacks %q", want)
		}
	}
}

// TestStreamedAnswers is T-958: the construct of sprint 6.2 (ADR 0064): a 200 whose content is
// application/x-ndjson, alone or with text/csv, generates a method that returns a stream and a
// client method that returns the body unread; the shapes outside it are refused.
func TestStreamedAnswers(t *testing.T) {
	const (
		ndjsonOnly = "    Export:\n      description: Lines.\n      content:\n        application/x-ndjson:\n          schema: {$ref: '#/components/schemas/Thing'}\n"
		withCSV    = "    Export:\n      description: Lines.\n      content:\n        application/x-ndjson:\n          schema: {$ref: '#/components/schemas/Thing'}\n        text/csv:\n          schema: {type: string}\n"
		path       = "  /v1/things/export:\n    get:\n      operationId: exportThings\n      x-pandokos-permission: thing:read\n" +
			"      parameters:\n        - {name: from_seq, in: query, schema: {type: integer, minimum: 1}}\n        - {name: format, in: query, schema: {$ref: '#/components/schemas/ExportFormat'}}\n" +
			"      responses:\n        \"200\": {$ref: '#/components/responses/Export'}\n        \"400\": {$ref: '#/components/responses/Err'}\n"
		format = "    ExportFormat: {type: string, enum: [ndjson, csv], default: ndjson}\n"
	)
	doc := func(response, extraPath string) string {
		d := replaceOnce(t, minimal, "components:\n  responses:\n", extraPath+"components:\n  responses:\n"+response)
		return replaceOnce(t, d, "    ProblemCode:\n", format+"    ProblemCode:\n")
	}
	squash := func(src []byte) string { return regexp.MustCompile(`[ \t]+`).ReplaceAllString(string(src), " ") }

	t.Run("ndjson alone and ndjson with csv are generated", func(t *testing.T) {
		for name, response := range map[string]string{"ndjson": ndjsonOnly, "ndjson and csv": withCSV} {
			src, err := generate(writeSpec(t, doc(response, path)))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			text := squash(src)
			for _, want := range []string{
				"ExportThings(ctx context.Context, in ExportThingsInput) (*Stream, error)", // the method of the server
				"func (c *Client) ExportThings(ctx context.Context, in ExportThingsInput) (*StreamBody, error)",
				"FromSeq int",
				"Format ExportFormat",
				"c.doStream(",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: the generated code does not contain %q", name, want)
				}
			}
			if !strings.Contains(text, "application/x-ndjson") {
				t.Errorf("%s: the client does not accept application/x-ndjson", name)
			}
		}
		src, _ := generate(writeSpec(t, doc(withCSV, path)))
		if !strings.Contains(squash(src), "text/csv") {
			t.Error("a stream that also has text/csv does not offer it")
		}
	})

	t.Run("an optional integer with a minimum is checked when it is given", func(t *testing.T) {
		src, err := generate(writeSpec(t, doc(ndjsonOnly, path)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "if in.FromSeq < 1") {
				continue
			}
			// The check is inside the block that parses the parameter, or says that zero is absent.
			indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, "\t")) }
			guarded := strings.Contains(l, "in.FromSeq != 0")
			for j := i - 1; j >= 0 && !guarded; j-- {
				if indent(lines[j]) < indent(l) {
					guarded = strings.Contains(lines[j], `q["from_seq"]`) || strings.Contains(lines[j], "in.FromSeq != 0")
					break
				}
			}
			if !guarded {
				t.Errorf("the minimum of from_seq is checked when the parameter is absent: %q refuses the requests that do not give it", strings.TrimSpace(l))
			}
		}
	})

	t.Run("the shapes outside the construct are refused", func(t *testing.T) {
		both := "    Export:\n      description: Lines.\n      content:\n        application/x-ndjson:\n          schema: {$ref: '#/components/schemas/Thing'}\n        application/json:\n          schema: {$ref: '#/components/schemas/Thing'}\n"
		csvOnly := "    Export:\n      description: Lines.\n      content:\n        text/csv:\n          schema: {type: string}\n        application/x-ndjson:\n          schema: {type: string}\n"
		otherType := "    Export:\n      description: Lines.\n      content:\n        application/x-ndjson:\n          schema: {$ref: '#/components/schemas/Thing'}\n        application/xml:\n          schema: {type: string}\n"
		created := replaceOnce(t, path, `"200": {$ref: '#/components/responses/Export'}`, `"201": {$ref: '#/components/responses/Export'}`)
		withBody := replaceOnce(t, replaceOnce(t, path, "    get:\n", "    post:\n"), "      responses:",
			"      requestBody:\n        required: true\n        content:\n          application/json:\n            schema: {$ref: '#/components/schemas/Thing'}\n      responses:")
		twoSuccess := replaceOnce(t, path, "        \"400\":", "        \"201\": {$ref: '#/components/responses/Thing'}\n        \"400\":")
		for name, c := range map[string]struct{ response, path string }{
			"a stream with application/json too":     {both, path},
			"a stream whose line schema is a string": {csvOnly, path},
			"a stream with another media type":       {otherType, path},
			"a 201 stream":                           {ndjsonOnly, created},
			"a stream with a request body":           {ndjsonOnly, withBody},
			"a stream beside another success answer": {ndjsonOnly, twoSuccess},
		} {
			if _, err := generate(writeSpec(t, doc(c.response, c.path))); err == nil {
				t.Errorf("%s: the document was accepted", name)
			}
		}
	})
}
