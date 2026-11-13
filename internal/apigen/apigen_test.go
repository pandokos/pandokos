package main

import (
	"bytes"
	"os"
	"path/filepath"
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
		{"object member in a request body", replaceOnce(t, replaceOnce(t, minimal,
			"        \"404\": {$ref: '#/components/responses/Err'}\ncomponents:", "        \"404\": {$ref: '#/components/responses/Err'}\n"+postWithObjectBody+"components:"),
			"    ProblemCode:\n", "    Nested:\n      type: object\n      additionalProperties: false\n      properties:\n        inner: {$ref: '#/components/schemas/Thing'}\n    ProblemCode:\n"),
			[]string{"flat"}},
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
