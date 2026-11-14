// Command apigen generates internal/adminapi from the OpenAPI document of the administration
// API. It accepts a deliberate subset of OpenAPI 3.1 and fails on anything else, so that the
// document cannot say something the generated code does not enforce. The output imports only
// the standard library.
//
// Usage: go run ./internal/apigen -spec api/openapi/admin.v1.yaml -out internal/adminapi/adminapi.gen.go
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type document struct {
	OpenAPI    string                `yaml:"openapi"`
	Info       map[string]any        `yaml:"info"`
	Security   []map[string][]string `yaml:"security"`
	Tags       []map[string]string   `yaml:"tags"`
	Paths      map[string]pathItem   `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]map[string]string `yaml:"securitySchemes"`
		Parameters      map[string]*parameter        `yaml:"parameters"`
		Responses       map[string]*response         `yaml:"responses"`
		Schemas         map[string]*schema           `yaml:"schemas"`
	} `yaml:"components"`
}

type pathItem struct {
	Parameters []*parameter `yaml:"parameters"`
	Get        *operation   `yaml:"get"`
	Post       *operation   `yaml:"post"`
	Patch      *operation   `yaml:"patch"`
	Put        *operation   `yaml:"put"`
	Delete     *operation   `yaml:"delete"`
}

type operation struct {
	OperationID string               `yaml:"operationId"`
	Tags        []string             `yaml:"tags"`
	Summary     string               `yaml:"summary"`
	Description string               `yaml:"description"`
	Permission  string               `yaml:"x-pandokos-permission"`
	Parameters  []*parameter         `yaml:"parameters"`
	RequestBody *requestBody         `yaml:"requestBody"`
	Responses   map[string]*response `yaml:"responses"`
}

type parameter struct {
	Ref         string  `yaml:"$ref"`
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Required    bool    `yaml:"required"`
	Description string  `yaml:"description"`
	Schema      *schema `yaml:"schema"`
}

type mediaType struct {
	Schema *schema `yaml:"schema"`
}

type requestBody struct {
	Required bool                 `yaml:"required"`
	Content  map[string]mediaType `yaml:"content"`
}

type response struct {
	Ref         string               `yaml:"$ref"`
	Description string               `yaml:"description"`
	Content     map[string]mediaType `yaml:"content"`
}

// typeSpec is "string" or ["string", "null"].
type typeSpec struct {
	Name     string
	Nullable bool
}

func (t *typeSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		t.Name = n.Value
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil || len(list) != 2 || list[1] != "null" {
		return errors.New(`type must be a name or [name, "null"]`)
	}
	t.Name, t.Nullable = list[0], true
	return nil
}

// properties keeps the order of the document.
type properties struct {
	Names []string
	By    map[string]*schema
}

func (p *properties) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return errors.New("properties must be a mapping")
	}
	p.By = map[string]*schema{}
	for i := 0; i < len(n.Content); i += 2 {
		s := &schema{}
		if err := n.Content[i+1].Decode(s); err != nil {
			return err
		}
		p.Names = append(p.Names, n.Content[i].Value)
		p.By[n.Content[i].Value] = s
	}
	return nil
}

type schema struct {
	Ref                  string     `yaml:"$ref"`
	Type                 typeSpec   `yaml:"type"`
	Format               string     `yaml:"format"`
	Description          string     `yaml:"description"`
	Pattern              string     `yaml:"pattern"`
	Enum                 []string   `yaml:"enum"`
	Default              *string    `yaml:"default"`
	MinLength            *int       `yaml:"minLength"`
	MaxLength            *int       `yaml:"maxLength"`
	Minimum              *int       `yaml:"minimum"`
	Maximum              *int       `yaml:"maximum"`
	MinItems             *int       `yaml:"minItems"`
	MaxItems             *int       `yaml:"maxItems"`
	UniqueItems          bool       `yaml:"uniqueItems"`
	Items                *schema    `yaml:"items"`
	Properties           properties `yaml:"properties"`
	Required             []string   `yaml:"required"`
	AdditionalProperties *bool      `yaml:"additionalProperties"`
}

type generator struct {
	doc      document
	buf      bytes.Buffer
	update   map[string]bool // update schemas: component objects used as the body of a PATCH operation
	patterns []string        // regular expressions, in order of first use
}

func main() {
	spec := flag.String("spec", "api/openapi/admin.v1.yaml", "OpenAPI document")
	out := flag.String("out", "internal/adminapi/adminapi.gen.go", "output file")
	flag.Parse()
	src, err := generate(*spec)
	if err == nil {
		err = os.WriteFile(*out, src, 0o644) //nolint:gosec // generated source, not a secret
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "apigen:", err)
		os.Exit(1)
	}
}

func generate(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the path is a build input
	if err != nil {
		return nil, err
	}
	g := &generator{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a construct outside the subset is an error
	if err := dec.Decode(&g.doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if g.doc.OpenAPI != "3.1.0" {
		return nil, errors.New("openapi must be 3.1.0")
	}
	body, err := g.run()
	if err != nil {
		return nil, err
	}
	src, err := format.Source(body)
	if err != nil {
		return nil, fmt.Errorf("generated code does not parse: %w", err)
	}
	return src, nil
}

func (g *generator) p(format string, args ...any) { fmt.Fprintf(&g.buf, format+"\n", args...) }

func refName(ref, kind string) (string, error) {
	prefix := "#/components/" + kind + "/"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("reference %q must start with %s", ref, prefix)
	}
	return strings.TrimPrefix(ref, prefix), nil
}

// resolve follows a reference to a component schema. name is "" for an inline schema.
func (g *generator) resolve(s *schema) (*schema, string, error) {
	if s == nil {
		return nil, "", errors.New("missing schema")
	}
	if s.Ref == "" {
		return s, "", nil
	}
	name, err := refName(s.Ref, "schemas")
	if err != nil {
		return nil, "", err
	}
	t, ok := g.doc.Components.Schemas[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown schema %q", name)
	}
	if t.Ref != "" {
		return nil, "", fmt.Errorf("schema %q: a component schema cannot be a reference", name)
	}
	return t, name, nil
}

func (g *generator) re(pattern string) string {
	for i, p := range g.patterns {
		if p == pattern {
			return "re" + strconv.Itoa(i)
		}
	}
	g.patterns = append(g.patterns, pattern)
	return "re" + strconv.Itoa(len(g.patterns)-1)
}

// fieldName is the Go name of a schema property. A property whose name would collide with the
// generated Validate method of its type (a "validate" flag) is named ValidateAtWrite; the wire
// name does not change.
func fieldName(pn string) string {
	if n := goName(pn); n != "Validate" {
		return n
	}
	return "ValidateAtWrite"
}

func goName(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if r == '_' || r == '-' {
			up = true
			continue
		}
		if up {
			b.WriteString(strings.ToUpper(string(r)))
			up = false
		} else {
			b.WriteRune(r)
		}
	}
	n := b.String()
	for _, a := range [][2]string{{"Id", "ID"}, {"Url", "URL"}} {
		if strings.HasSuffix(n, a[0]) {
			n = strings.TrimSuffix(n, a[0]) + a[1]
		}
	}
	return n
}

func enumConst(typ, v string) string {
	return typ + goName(strings.ReplaceAll(strings.ReplaceAll(v, "-", "minus_"), ":", "_"))
}

func comment(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// field describes how one property or parameter is represented in Go.
type field struct {
	goType   string
	kind     string // "string", "enum", "time", "int", "bool", "object", "strings", "objects"
	nullable bool
	s        *schema // resolved schema, for the constraints
	item     *schema // resolved item schema of an array
}

func (g *generator) fieldOf(s *schema, required bool) (field, error) {
	t, name, err := g.resolve(s)
	if err != nil {
		return field{}, err
	}
	f := field{s: t, nullable: t.Type.Nullable}
	if f.nullable && (name != "" || t.Type.Name != "string") {
		return f, errors.New(`only an inline string can be nullable`)
	}
	switch {
	case t.Type.Name == "object":
		if name == "" {
			return f, errors.New("an object must be a component schema")
		}
		f.kind, f.goType = "object", name
		if !required {
			f.goType = "*" + name
		}
	case t.Type.Name == "string" && len(t.Enum) > 0:
		if name == "" {
			return f, errors.New("an enum must be a component schema")
		}
		f.kind, f.goType = "enum", name
	case t.Type.Name == "string" && t.Format == "date-time":
		f.kind, f.goType = "time", "time.Time"
		if f.nullable {
			f.goType = "Nullable[time.Time]"
		} else if !required {
			f.goType = "*time.Time"
		}
	case t.Type.Name == "string" && t.Format == "":
		f.kind, f.goType = "string", "string"
		if f.nullable {
			f.goType = "Nullable[string]"
		}
	case t.Type.Name == "integer":
		f.kind, f.goType = "int", "int"
	case t.Type.Name == "boolean":
		f.kind, f.goType = "bool", "bool"
	case t.Type.Name == "array":
		it, itName, err := g.resolve(t.Items)
		if err != nil {
			return f, fmt.Errorf("items: %w", err)
		}
		f.item = it
		switch {
		case it.Type.Name == "object" && itName != "":
			f.kind, f.goType = "objects", "[]"+itName
		case it.Type.Name == "string" && len(it.Enum) == 0 && it.Format == "":
			f.kind, f.goType = "strings", "[]string"
		default:
			return f, errors.New("array items must be a component object or a plain string")
		}
	default:
		return f, fmt.Errorf("unsupported type %q with format %q", t.Type.Name, t.Format)
	}
	return f, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (g *generator) run() ([]byte, error) {
	var body bytes.Buffer
	if err := g.findUpdateSchemas(); err != nil {
		return nil, err
	}
	if err := g.schemas(); err != nil {
		return nil, err
	}
	if err := g.operations(); err != nil {
		return nil, err
	}
	body.WriteString("// Code generated by internal/apigen from api/openapi/admin.v1.yaml. DO NOT EDIT.\n\n")
	body.WriteString("package adminapi\n\nimport (\n\t\"context\"\n\t\"net/http\"\n\t\"net/url\"\n\t\"regexp\"\n\t\"strconv\"\n\t\"time\"\n)\n\n")
	body.WriteString("var (\n\t_ = strconv.Itoa\n\t_ = time.Now\n\t_ = url.PathEscape\n)\n\n")
	if len(g.patterns) > 0 {
		body.WriteString("var (\n")
		for i, p := range g.patterns {
			fmt.Fprintf(&body, "\tre%d = regexp.MustCompile(%s)\n", i, strconv.Quote(p))
		}
		body.WriteString(")\n\n")
	}
	body.Write(g.buf.Bytes())
	return body.Bytes(), nil
}

// findUpdateSchemas records the update schemas: the component objects that are the request body
// of a PATCH operation. Such a schema is generated with Optional members, so it may not be used
// anywhere else (as another operation's body, a member of another object, an item, a response).
func (g *generator) findUpdateSchemas() error {
	g.update = map[string]bool{}
	elsewhere := map[string]string{}
	var walk func(where string, s *schema)
	walk = func(where string, s *schema) {
		if s == nil {
			return
		}
		if s.Ref != "" {
			if n, err := refName(s.Ref, "schemas"); err == nil {
				elsewhere[n] = where
			}
		}
		walk(where, s.Items)
		for _, pn := range s.Properties.Names {
			walk(where, s.Properties.By[pn])
		}
	}
	for _, name := range sortedKeys(g.doc.Components.Schemas) {
		s := g.doc.Components.Schemas[name]
		for _, pn := range s.Properties.Names {
			walk("a member of schema "+name, s.Properties.By[pn])
		}
		walk("an item of schema "+name, s.Items)
	}
	for _, name := range sortedKeys(g.doc.Components.Responses) {
		for _, m := range g.doc.Components.Responses[name].Content {
			walk("response "+name, m.Schema)
		}
	}
	for _, path := range sortedKeys(g.doc.Paths) {
		item := g.doc.Paths[path]
		for _, m := range []struct {
			method string
			op     *operation
		}{{"GET", item.Get}, {"POST", item.Post}, {"PATCH", item.Patch}, {"PUT", item.Put}, {"DELETE", item.Delete}} {
			if m.op == nil || m.op.RequestBody == nil {
				continue
			}
			mt, ok := m.op.RequestBody.Content["application/json"]
			if !ok || mt.Schema == nil || mt.Schema.Ref == "" {
				continue // reported by operations
			}
			n, err := refName(mt.Schema.Ref, "schemas")
			if err != nil {
				continue
			}
			if m.method == "PATCH" {
				g.update[n] = true
			} else {
				elsewhere[n] = m.method + " " + path
			}
		}
	}
	for _, n := range sortedKeys(g.update) {
		if w, ok := elsewhere[n]; ok {
			return fmt.Errorf("schema %s is the body of a PATCH operation (an update schema) and is also used by %s", n, w)
		}
	}
	return nil
}

func (g *generator) schemas() error {
	for _, name := range sortedKeys(g.doc.Components.Schemas) {
		s := g.doc.Components.Schemas[name]
		var err error
		switch {
		case s.Type.Name == "object":
			err = g.object(name, s)
		case s.Type.Name == "string" && len(s.Enum) > 0:
			g.enum(name, s)
		case s.Type.Name == "string" || s.Type.Name == "integer":
			// A named scalar: used through references, it has no Go type of its own.
		default:
			err = fmt.Errorf("unsupported component schema of type %q", s.Type.Name)
		}
		if err != nil {
			return fmt.Errorf("schema %s: %w", name, err)
		}
	}
	return nil
}

func (g *generator) enum(name string, s *schema) {
	if s.Description != "" {
		g.p("// %s: %s", name, comment(s.Description))
	} else {
		g.p("// %s is an enumeration of the administration API.", name)
	}
	g.p("type %s string\n", name)
	g.p("// Values of %s.", name)
	g.p("const (")
	for _, v := range s.Enum {
		g.p("%s %s = %q", enumConst(name, v), name, v)
	}
	g.p(")\n")
	g.p("// Valid reports whether v is a value of the enumeration.")
	g.p("func (v %s) Valid() bool {", name)
	g.p("switch v {")
	consts := make([]string, len(s.Enum))
	for i, v := range s.Enum {
		consts[i] = enumConst(name, v)
	}
	g.p("case %s:", strings.Join(consts, ", "))
	g.p("return true")
	g.p("}")
	g.p("return false")
	g.p("}\n")
}

func (g *generator) object(name string, s *schema) error {
	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		return errors.New("an object must say additionalProperties: false")
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		if _, ok := s.Properties.By[r]; !ok {
			return fmt.Errorf("required names %q, which is not a property", r)
		}
		required[r] = true
	}
	if s.Description != "" {
		g.p("// %s: %s", name, comment(s.Description))
	} else {
		g.p("// %s is a schema of the administration API.", name)
	}
	g.p("type %s struct {", name)
	var checks bytes.Buffer
	var nested []string // the members that are objects, or arrays of objects, for the presence check
	for _, pn := range s.Properties.Names {
		ps := s.Properties.By[pn]
		f, err := g.fieldOf(ps, required[pn])
		if err != nil {
			return fmt.Errorf("property %s: %w", pn, err)
		}
		tag := pn
		optional := g.update[name] && !required[pn] && !f.nullable
		switch {
		case optional:
			switch f.kind {
			case "string", "enum", "int", "bool", "strings":
			default:
				return fmt.Errorf("property %s: an optional member of an update schema must be a string, enum, integer, boolean or array of strings", pn)
			}
			f.goType = "Optional[" + f.goType + "]"
			tag += ",omitzero"
		case f.nullable:
			if required[pn] {
				return fmt.Errorf("property %s: a nullable property cannot be required", pn)
			}
			tag += ",omitzero"
		case !required[pn]:
			tag += ",omitempty"
		}
		if d := comment(ps.Description); d != "" {
			g.p("// %s", d)
		}
		g.p("%s %s `json:%q`", fieldName(pn), f.goType, tag)
		if f.kind == "object" || f.kind == "objects" {
			target := strings.TrimLeft(f.goType, "[]*")
			many := ""
			if f.kind == "objects" {
				many = "Many: true, "
			}
			nested = append(nested, fmt.Sprintf("{Name: %q, %sRequired: required%s, Nested: nested%s}", pn, many, target, target))
		}
		if optional {
			// Validation runs only when the member is present.
			fmt.Fprintf(&checks, "if v.%s.Set {\n", fieldName(pn))
			g.check(&checks, "v."+fieldName(pn)+".Value", pn, f, true)
			checks.WriteString("}\n")
			continue
		}
		g.check(&checks, "v."+fieldName(pn), pn, f, required[pn])
	}
	g.p("}\n")
	g.p("var required%s = []string{%s}\n", name, quoteList(s.Required))
	if len(nested) == 0 {
		g.p("var nested%s []nestedMember\n", name)
	} else {
		g.p("var nested%s = []nestedMember{%s}\n", name, strings.Join(nested, ", "))
	}
	g.p("// Validate returns the rules of the document that v breaks. A message never repeats a value.")
	g.p("func (v *%s) Validate() []FieldError {", name)
	g.p("var errs []FieldError")
	g.buf.Write(checks.Bytes())
	g.p("return errs")
	g.p("}\n")
	return nil
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

// check writes the validation of one value. expr is the Go expression, name the member name.
func (g *generator) check(w *bytes.Buffer, expr, name string, f field, required bool) {
	add := func(cond, msg string) {
		fmt.Fprintf(w, "if %s {\nerrs = append(errs, FieldError{Field: %q, Message: %q})\n}\n", cond, name, msg)
	}
	str := func(expr string, s *schema, guard string) {
		if s.Pattern != "" {
			add(guard+"!"+g.re(s.Pattern)+".MatchString("+expr+")", "must match "+s.Pattern)
		}
		if s.MinLength != nil {
			add(guard+"runeCount("+expr+") < "+strconv.Itoa(*s.MinLength), "length must be at least "+strconv.Itoa(*s.MinLength))
		}
		if s.MaxLength != nil {
			add("runeCount("+expr+") > "+strconv.Itoa(*s.MaxLength), "length must be at most "+strconv.Itoa(*s.MaxLength))
		}
	}
	switch f.kind {
	case "object":
		// The members of a nested object are checked by its own Validate; their names are
		// prefixed with the name of the member.
		if !required {
			fmt.Fprintf(w, "if %s != nil {\n", expr)
		}
		fmt.Fprintf(w, "for _, e := range %s.Validate() {\nerrs = append(errs, FieldError{Field: %q + e.Field, Message: e.Message})\n}\n", expr, name+".")
		if !required {
			w.WriteString("}\n")
		}
	case "objects":
		if f.s.MinItems != nil {
			add(expr+" != nil && len("+expr+") < "+strconv.Itoa(*f.s.MinItems), "must have at least "+strconv.Itoa(*f.s.MinItems)+" items")
		}
		if f.s.MaxItems != nil {
			add("len("+expr+") > "+strconv.Itoa(*f.s.MaxItems), "must have at most "+strconv.Itoa(*f.s.MaxItems)+" items")
		}
		fmt.Fprintf(w, "for i := range %s {\nfor _, e := range %s[i].Validate() {\nerrs = append(errs, FieldError{Field: %q + strconv.Itoa(i) + \"].\" + e.Field, Message: e.Message})\n}\n}\n", expr, expr, name+"[")
	case "string":
		if f.nullable {
			str(expr+".Value", f.s, expr+".Set && !"+expr+".Null && ")
			return
		}
		guard := ""
		if !required {
			guard = expr + ` != "" && `
		}
		str(expr, f.s, guard)
	case "enum":
		guard := ""
		if !required {
			guard = expr + ` != "" && `
		}
		add(guard+"!"+expr+".Valid()", "must be one of "+strings.Join(f.s.Enum, ", "))
	case "int":
		if f.s.Minimum != nil {
			add(expr+" < "+strconv.Itoa(*f.s.Minimum), "must be at least "+strconv.Itoa(*f.s.Minimum))
		}
		if f.s.Maximum != nil {
			add(expr+" > "+strconv.Itoa(*f.s.Maximum), "must be at most "+strconv.Itoa(*f.s.Maximum))
		}
	case "strings":
		if f.s.MinItems != nil {
			add(expr+" != nil && len("+expr+") < "+strconv.Itoa(*f.s.MinItems), "must have at least "+strconv.Itoa(*f.s.MinItems)+" items")
		}
		if f.s.MaxItems != nil {
			add("len("+expr+") > "+strconv.Itoa(*f.s.MaxItems), "must have at most "+strconv.Itoa(*f.s.MaxItems)+" items")
		}
		if f.s.UniqueItems {
			add("!unique("+expr+")", "must not repeat an item")
		}
		if f.item.Pattern != "" || f.item.MinLength != nil || f.item.MaxLength != nil {
			fmt.Fprintf(w, "for _, item := range %s {\n", expr)
			str("item", f.item, "")
			fmt.Fprintf(w, "}\n")
		}
	}
}

type param struct {
	name string
	f    field
	p    *parameter
}

func (g *generator) param(p *parameter) (*parameter, error) {
	if p.Ref == "" {
		return p, nil
	}
	name, err := refName(p.Ref, "parameters")
	if err != nil {
		return nil, err
	}
	t, ok := g.doc.Components.Parameters[name]
	if !ok {
		return nil, fmt.Errorf("unknown parameter %q", name)
	}
	return t, nil
}

// successBody returns the status and the schema name of the one 2xx response, and checks that
// every other response is a problem.
func (g *generator) successBody(op *operation) (int, string, error) {
	status, name := 0, ""
	for _, code := range sortedKeys(op.Responses) {
		r := op.Responses[code]
		rn, err := refName(r.Ref, "responses")
		if err != nil {
			return 0, "", fmt.Errorf("response %s: %w", code, err)
		}
		t, ok := g.doc.Components.Responses[rn]
		if !ok {
			return 0, "", fmt.Errorf("response %s: unknown response %q", code, rn)
		}
		n, err := strconv.Atoi(code)
		if err != nil || n < 200 || n > 599 {
			return 0, "", fmt.Errorf("response %q: the status must be a number", code)
		}
		if n >= 300 {
			m, ok := t.Content["application/problem+json"]
			if !ok || len(t.Content) != 1 || m.Schema == nil || m.Schema.Ref != "#/components/schemas/Problem" {
				return 0, "", fmt.Errorf("response %s: an error response must be application/problem+json with the schema Problem", code)
			}
			continue
		}
		m, ok := t.Content["application/json"]
		if !ok || len(t.Content) != 1 || status != 0 {
			return 0, "", errors.New("an operation has exactly one 2xx response, with application/json content")
		}
		s, sn, err := g.resolve(m.Schema)
		if err != nil || sn == "" || s.Type.Name != "object" {
			return 0, "", fmt.Errorf("response %s: the schema must be a component object", code)
		}
		status, name = n, sn
	}
	if status == 0 {
		return 0, "", errors.New("an operation has exactly one 2xx response")
	}
	return status, name, nil
}

var rePermission = regexp.MustCompile(`^[a-z_]+:[a-z_]+$`)

func (g *generator) operations() error {
	type opInfo struct {
		id, method, path, permission, input, output, body string
		status                                            int
		bodyRequired                                      bool
		pathParams, queryParams                           []param
		doc                                               string
	}
	var ops []opInfo
	seen := map[string]bool{}
	for _, path := range sortedKeys(g.doc.Paths) {
		item := g.doc.Paths[path]
		for _, m := range []struct {
			method string
			op     *operation
		}{{"GET", item.Get}, {"POST", item.Post}, {"PATCH", item.Patch}, {"PUT", item.Put}, {"DELETE", item.Delete}} {
			if m.op == nil {
				continue
			}
			op := m.op
			fail := func(err error) error { return fmt.Errorf("%s %s: %w", m.method, path, err) }
			if !regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`).MatchString(op.OperationID) || seen[op.OperationID] {
				return fail(errors.New("operationId must be a unique lowerCamelCase name"))
			}
			seen[op.OperationID] = true
			if !rePermission.MatchString(op.Permission) {
				return fail(errors.New("x-pandokos-permission must name one permission (resource:action)"))
			}
			info := opInfo{id: op.OperationID, method: m.method, path: path, permission: op.Permission, doc: comment(op.Summary)}
			info.input = goName(op.OperationID) + "Input"
			var err error
			if info.status, info.output, err = g.successBody(op); err != nil {
				return fail(err)
			}
			for _, raw := range append(append([]*parameter{}, item.Parameters...), op.Parameters...) {
				p, err := g.param(raw)
				if err != nil {
					return fail(err)
				}
				f, err := g.fieldOf(p.Schema, true)
				if err != nil {
					return fail(fmt.Errorf("parameter %s: %w", p.Name, err))
				}
				switch {
				case p.In == "path" && p.Required && f.kind == "string" && f.s.Pattern != "" && strings.Contains(path, "{"+p.Name+"}"):
					info.pathParams = append(info.pathParams, param{p.Name, f, p})
				case p.In == "query" && !p.Required && (f.kind == "string" || f.kind == "enum" || f.kind == "int"):
					info.queryParams = append(info.queryParams, param{p.Name, f, p})
				default:
					return fail(fmt.Errorf("parameter %s: must be a required path string with a pattern, or an optional query string, enum or integer", p.Name))
				}
			}
			if n := strings.Count(path, "{"); n != len(info.pathParams) {
				return fail(errors.New("every {name} of the path needs a path parameter"))
			}
			if op.RequestBody != nil {
				m, ok := op.RequestBody.Content["application/json"]
				if !ok || len(op.RequestBody.Content) != 1 {
					return fail(errors.New("a request body is application/json"))
				}
				s, sn, err := g.resolve(m.Schema)
				if err != nil || sn == "" || s.Type.Name != "object" {
					return fail(errors.New("a request body must be a component object"))
				}
				info.body, info.bodyRequired = sn, op.RequestBody.Required
			}
			ops = append(ops, info)
		}
	}

	// Operation table.
	g.p("// Operations lists every operation of the document, sorted by path and method.")
	g.p("var Operations = []Operation{")
	for _, o := range ops {
		var pp []string
		for _, p := range o.pathParams {
			pp = append(pp, fmt.Sprintf("{Name: %q, Pattern: %s}", p.name, g.re(p.f.s.Pattern)))
		}
		g.p("{ID: %q, Method: %q, Path: %q, Permission: %q, Status: %d, HasBody: %t, PathParams: []PathParam{%s}},",
			o.id, o.method, o.path, o.permission, o.status, o.body != "", strings.Join(pp, ", "))
	}
	g.p("}\n")

	// Inputs.
	for _, o := range ops {
		g.p("// %s is the input of %s.", o.input, o.id)
		g.p("type %s struct {", o.input)
		for _, p := range o.pathParams {
			g.p("%s string // path", goName(p.name))
		}
		for _, p := range o.queryParams {
			g.p("%s %s // query; the zero value means absent", goName(p.name), p.f.goType)
		}
		if o.body != "" {
			g.p("Body %s", o.body)
		}
		g.p("}\n")
	}

	// Server interface.
	g.p("// Server is implemented by the handlers of the administration API: one method per operation.")
	g.p("type Server interface {")
	for _, o := range ops {
		g.p("// %s: %s %s. %s", goName(o.id), o.method, o.path, o.doc)
		g.p("%s(ctx context.Context, in %s) (*%s, error)", goName(o.id), o.input, o.output)
	}
	g.p("}\n")

	// Dispatch.
	g.p("func (h *handler) serve(op *Operation, w http.ResponseWriter, r *http.Request, params map[string]string) {")
	g.p("switch op.ID {")
	for _, o := range ops {
		g.p("case %q:", o.id)
		g.p("var in %s", o.input)
		for _, p := range o.pathParams {
			g.p("in.%s = params[%q]", goName(p.name), p.name)
		}
		var names []string
		for _, p := range o.queryParams {
			names = append(names, p.name)
		}
		if len(names) == 0 {
			g.p("if _, err := parseQuery(r); err != nil { h.fail(w, r, op, err); return }")
		} else {
			g.p("q, err := parseQuery(r, %s)", quoteList(names))
			g.p("if err != nil { h.fail(w, r, op, err); return }")
		}
		if len(o.queryParams) > 0 {
			g.p("var errs []FieldError")
		}
		for _, p := range o.queryParams {
			fn, s := "in."+goName(p.name), p.f.s
			switch p.f.kind {
			case "int":
				if s.Default != nil {
					g.p("%s = %s", fn, *s.Default)
				}
				g.p("if v, ok := q[%q]; ok {", p.name)
				g.p("n, err := strconv.Atoi(v)")
				g.p("if err != nil { errs = append(errs, FieldError{Field: %q, Message: \"must be an integer\"}) } else { %s = n }", p.name, fn)
				g.p("}")
			case "enum":
				if s.Default != nil {
					g.p("%s = %s(%q)", fn, p.f.goType, *s.Default)
				}
				g.p("if v, ok := q[%q]; ok { %s = %s(v) }", p.name, fn, p.f.goType)
			default:
				g.p("if v, ok := q[%q]; ok {", p.name)
				g.p("if v == \"\" { errs = append(errs, FieldError{Field: %q, Message: \"must not be empty\"}) }", p.name)
				g.p("%s = v", fn)
				g.p("}")
			}
			var checks bytes.Buffer
			g.check(&checks, fn, p.name, p.f, p.f.kind == "int")
			g.buf.Write(checks.Bytes())
		}
		if len(o.queryParams) > 0 {
			g.p("if len(errs) > 0 { h.fail(w, r, op, invalid(errs)); return }")
		}
		if o.body != "" {
			g.p("if err := decodeBody(r, h.hooks.MaxBodyBytes, %t, required%s, nested%s, &in.Body); err != nil { h.fail(w, r, op, err); return }", o.bodyRequired, o.body, o.body)
			g.p("if errs := in.Body.Validate(); len(errs) > 0 { h.fail(w, r, op, invalid(errs)); return }")
		}
		g.p("out, err := h.server.%s(r.Context(), in)", goName(o.id))
		g.p("if err != nil { h.fail(w, r, op, err); return }")
		g.p("writeJSON(w, %d, out)", o.status)
	}
	g.p("}")
	g.p("}\n")

	// Client.
	for _, o := range ops {
		g.p("// %s calls %s %s. %s", goName(o.id), o.method, o.path, o.doc)
		g.p("func (c *Client) %s(ctx context.Context, in %s) (*%s, error) {", goName(o.id), o.input, o.output)
		expr := strconv.Quote(o.path)
		for _, p := range o.pathParams {
			expr = strings.Replace(expr, "{"+p.name+"}", `" + url.PathEscape(in.`+goName(p.name)+`) + "`, 1)
		}
		g.p("q := url.Values{}")
		for _, p := range o.queryParams {
			fn := "in." + goName(p.name)
			switch p.f.kind {
			case "int":
				g.p("if %s != 0 { q.Set(%q, strconv.Itoa(%s)) }", fn, p.name, fn)
			default:
				g.p("if %s != \"\" { q.Set(%q, string(%s)) }", fn, p.name, fn)
			}
		}
		bodyArg := "nil"
		if o.body != "" {
			bodyArg = "&in.Body"
		}
		g.p("var out %s", o.output)
		g.p("if err := c.do(ctx, %q, %s, q, %s, %d, &out); err != nil { return nil, err }", o.method, expr, bodyArg, o.status)
		g.p("return &out, nil")
		g.p("}\n")
	}
	return nil
}
