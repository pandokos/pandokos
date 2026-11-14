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
        note: {type: string, maxLength: 5}
        rates:
          type: array
          minItems: 1
          maxItems: 3
          items: {$ref: '#/components/schemas/Rate'}
        extra: {$ref: '#/components/schemas/Rate'}
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
