package anthropic_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
	"github.com/pandokos/pandokos/internal/family/anthropic"
	"github.com/pandokos/pandokos/test/fixtures"
)

type parseCase struct {
	name, in string
	reason   string // "" means accepted
	model    string
	stream   bool
}

var parseCases = []parseCase{
	{"minimal", `{"model":"claude-sonnet-4-5"}`, "", "claude-sonnet-4-5", false},
	{"a real-looking body", `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"` + fixtures.CanaryPrompt + `"}]}`, "", "claude-sonnet-4-5", false},
	{"stream true", `{"model":"m","stream":true}`, "", "m", true},
	{"stream false", `{"model":"m","stream":false}`, "", "m", false},
	{"the last stream member counts", `{"stream":true,"model":"m","stream":false}`, "", "m", false},
	{"the last stream member counts, true", `{"stream":false,"model":"m","stream":true}`, "", "m", true},
	{"stream as a string is not true", `{"model":"m","stream":"true"}`, "", "m", false},
	{"stream as a number is not true", `{"model":"m","stream":1}`, "", "m", false},
	{"stream null", `{"model":"m","stream":null}`, "", "m", false},
	{"white space", " \n{ \"model\" :\t\"m\" , \"stream\" : true }\n ", "", "m", true},
	{"nested model members are ignored", `{"model":"outer","messages":[{"model":"inner"}],"tools":{"model":"x"}}`, "", "outer", false},
	{"nested stream members are ignored", `{"model":"m","metadata":{"stream":true}}`, "", "m", false},
	{"escapes in the value are decoded", `{"model":"a\"b\\cé\nA"}`, "", "a\"b\\cé\nA", false},
	{"an escaped key is the key", `{"model":"m","stream":true}`, "", "m", true},

	{"an array", `[{"model":"m"}]`, "not_object", "", false},
	{"a string", `"model"`, "not_object", "", false},
	{"a number", `7`, "not_object", "", false},
	{"null", `null`, "not_object", "", false},
	{"empty input", ``, "not_object", "", false},
	{"white space only", " \n", "not_object", "", false},
	{"trailing data", `{"model":"m"} x`, "not_object", "", false},
	{"a second object", `{"model":"m"}{"model":"n"}`, "not_object", "", false},
	{"truncated", `{"model":"m"`, "not_object", "", false},
	{"invalid JSON", `{model:"m"}`, "not_object", "", false},

	{"model in another case", `{"Model":"m"}`, "member_case", "", false},
	{"model in another case beside the exact name", `{"model":"m","MODEL":"n"}`, "member_case", "", false},
	{"stream in another case", `{"model":"m","Stream":true}`, "member_case", "", false},
	{"stream in another case beside the exact name", `{"model":"m","stream":false,"STREAM":true}`, "member_case", "", false},
	{"the output limit in another case", `{"model":"m","Max_Tokens":1}`, "member_case", "", false},
	{"a member in another case that is not read", `{"model":"m","Messages":[],"Temperature":1,"N":2}`, "", "m", false},
	{"a nested member in another case", `{"model":"m","metadata":{"Stream":true},"messages":[{"Model":"x"}]}`, "", "m", false},

	{"an empty object", `{}`, "model_missing", "", false},
	{"other members only", `{"messages":[],"stream":true}`, "model_missing", "", false},
	{"model only nested", `{"messages":[{"model":"m"}]}`, "model_missing", "", false},

	{"two model members", `{"model":"a","model":"b"}`, "model_duplicate", "", false},
	{"two equal model members", `{"model":"a","x":1,"model":"a"}`, "model_duplicate", "", false},
	{"a duplicate whose second value is not a string", `{"model":"a","model":1}`, "model_duplicate", "", false},

	{"a number", `{"model":1}`, "model_not_string", "", false},
	{"null", `{"model":null}`, "model_not_string", "", false},
	{"a boolean", `{"model":true}`, "model_not_string", "", false},
	{"an array", `{"model":["a"]}`, "model_not_string", "", false},
	{"an object", `{"model":{"name":"a"}}`, "model_not_string", "", false},
	{"the empty string", `{"model":""}`, "model_not_string", "", false},
}

var operations = []string{anthropic.OperationMessages, anthropic.OperationCountTokens}

// TestParseRequest is T-520: the model rules and their reasons for both operations, the
// stream rule, and what Prepare does.
func TestParseRequest(t *testing.T) {
	for _, op := range operations {
		for _, c := range parseCases {
			t.Run(op+"/"+c.name, func(t *testing.T) {
				in := []byte(c.in)
				orig := append([]byte(nil), in...)
				r, err := anthropic.ParseOperation(op, in)
				if !bytes.Equal(in, orig) {
					t.Fatal("ParseOperation modified its input")
				}
				if c.reason != "" {
					if err == nil || r != nil {
						t.Fatalf("accepted: %v %v", r, err)
					}
					if !errors.Is(err, family.ErrInvalidRequest) {
						t.Errorf("the error does not wrap ErrInvalidRequest: %v", err)
					}
					if got := family.InvalidReason(err); got != c.reason {
						t.Errorf("reason %q, want %q", got, c.reason)
					}
					if len(c.in) > 12 && strings.Contains(err.Error(), c.in) {
						t.Errorf("the error quotes the body: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantStream := c.stream && op == anthropic.OperationMessages // counting never streams
				if r.Model() != c.model || r.Stream() != wantStream {
					t.Errorf("model %q stream %v, want %q %v", r.Model(), r.Stream(), c.model, wantStream)
				}
			})
		}
	}
	t.Run("the error never holds the body", func(t *testing.T) {
		_, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(`{"messages":[{"content":"`+fixtures.CanaryPrompt+`"}]}`))
		if err == nil || strings.Contains(err.Error(), fixtures.CanaryPrompt) {
			t.Errorf("%v", err)
		}
	})
	t.Run("a Request is a family.Request", func(*testing.T) {
		var _ family.Request = (*anthropic.Request)(nil)
	})
}

type prepareCase struct {
	name, in, upstream, out string // out == "" means the input slice itself
}

var prepareCases = []prepareCase{
	{"the model alone", `{"model":"nick","max_tokens":1}`, "claude-sonnet-4-5", `{"model":"claude-sonnet-4-5","max_tokens":1}`},
	{"white space around the value", `{"model" :  "nick" ,"x":1}`, "u", `{"model" :  "u" ,"x":1}`},
	{"the model last, nested models before", `{"messages":[{"model":"nested"}],"model":"nick"}`, "u", `{"messages":[{"model":"nested"}],"model":"u"}`},
	{"an escaped key", `{"model":"nick"}`, "u", `{"model":"u"}`},
	{"a quote and a backslash", `{"model":"nick"}`, `we"ird\id`, `{"model":"we\"ird\\id"}`},
	{"HTML characters are not escaped", `{"model":"nick"}`, `a<b>&c`, `{"model":"a<b>&c"}`},
	{"non-ASCII characters are not escaped", `{"model":"nick"}`, "modèle-é", `{"model":"modèle-é"}`},
	{"the same name", `{"model":"nick"}`, "nick", ""},
	{"an empty upstream identifier", `{"model":"nick"}`, "", ""},
	{"a stream is left alone", `{"model":"nick","stream":true}`, "nick", ""},
	{"a stream with another model", `{"model":"nick","stream":true,"max_tokens":5}`, "u", `{"model":"u","stream":true,"max_tokens":5}`},
	{"stream options are not an Anthropic member and are left alone", `{"model":"nick","stream":true,"stream_options":{"x":1}}`, "nick", ""},
}

// TestPrepare is T-520: the body is forwarded as received, only the value of model changes, and
// strip is always false.
func TestPrepare(t *testing.T) {
	for _, op := range operations {
		for _, c := range prepareCases {
			t.Run(op+"/"+c.name, func(t *testing.T) {
				in := []byte(c.in)
				orig := append([]byte(nil), in...)
				r, err := anthropic.ParseOperation(op, in)
				if err != nil {
					t.Fatal(err)
				}
				for _, streamUsage := range []bool{false, true} {
					out, strip := r.Prepare(family.Preparation{UpstreamModel: c.upstream, StreamUsage: streamUsage})
					if strip {
						t.Errorf("strip is true (StreamUsage %v)", streamUsage)
					}
					if !bytes.Equal(in, orig) {
						t.Fatal("Prepare modified the caller's bytes")
					}
					if c.out == "" {
						if !bytes.Equal(out, in) || &out[0] != &in[0] {
							t.Fatalf("an unchanged body is not the input slice: %q", out)
						}
						continue
					}
					if string(out) != c.out {
						t.Errorf("got  %q\nwant %q", out, c.out)
					}
					var m struct {
						Model string `json:"model"`
					}
					if err := json.Unmarshal(out, &m); err != nil || m.Model != c.upstream {
						t.Errorf("the output decodes to model %q (%v), want %q", m.Model, err, c.upstream)
					}
				}
			})
		}
	}
	t.Run("a body is never given an option", func(t *testing.T) {
		r, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(`{"model":"m","stream":true}`))
		if err != nil {
			t.Fatal(err)
		}
		out, strip := r.Prepare(family.Preparation{StreamUsage: true})
		if string(out) != `{"model":"m","stream":true}` || strip {
			t.Errorf("%q %v", out, strip)
		}
	})
	t.Run("Prepare may be called twice with the same result", func(t *testing.T) {
		r, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(`{"model":"nick"}`))
		if err != nil {
			t.Fatal(err)
		}
		a, _ := r.Prepare(family.Preparation{UpstreamModel: "u"})
		b, _ := r.Prepare(family.Preparation{UpstreamModel: "u"})
		if !bytes.Equal(a, b) {
			t.Errorf("%q then %q", a, b)
		}
	})
}

// FuzzAnthropicParseRequest is T-524: ParseOperation never panics; what it accepts,
// encoding/json reads the same; the output of Prepare decodes to the upstream model and to the
// same other members, and strip is false.
func FuzzAnthropicParseRequest(f *testing.F) {
	for i, c := range parseCases {
		f.Add([]byte(c.in), uint8(i))
	}
	for i, c := range prepareCases {
		f.Add([]byte(c.in), uint8(i))
	}
	f.Add([]byte(`{"model":"m","stream":true}`), uint8(1))
	f.Add([]byte(`{"Model":"m"}`), uint8(0))
	f.Add([]byte(`{"model":"m","MODEL":"n"}`), uint8(0))
	f.Add([]byte(`{"model":"m","Stream":true}`), uint8(0))
	f.Add([]byte(`{"model":"m","Max_Tokens":1,"max_tokens":2}`), uint8(1))
	f.Add([]byte("{\"model\":\"m\",\"\u017ftream\":true}"), uint8(0))
	f.Fuzz(func(t *testing.T, in []byte, which uint8) {
		op := operations[int(which)%len(operations)]
		orig := append([]byte(nil), in...)
		r, err := anthropic.ParseOperation(op, in)
		if !bytes.Equal(in, orig) {
			t.Fatal("input was modified")
		}
		if err != nil {
			if !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) == "" {
				t.Fatalf("a refusal without a reason: %v", err)
			}
			return
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(in, &members); err != nil {
			t.Fatalf("ParseOperation accepted what encoding/json refuses: %v", err)
		}
		var model string
		if err := json.Unmarshal(members["model"], &model); err != nil || model != r.Model() || model == "" {
			t.Fatalf("encoding/json reads model %q (%v), ParseOperation read %q", model, err, r.Model())
		}
		if op == anthropic.OperationCountTokens && r.Stream() {
			t.Fatal("token counting streams")
		}
		const upstream = `synthetic"up\stream`
		out, strip := r.Prepare(family.Preparation{UpstreamModel: upstream, StreamUsage: true})
		if strip {
			t.Fatal("strip is true")
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("the output is not a JSON object: %v", err)
		}
		var gotModel string
		if err := json.Unmarshal(got["model"], &gotModel); err != nil || gotModel != upstream {
			t.Fatalf("the output decodes to model %q (%v)", gotModel, err)
		}
		for k, v := range members {
			if k != "model" && !bytes.Equal(got[k], v) {
				t.Fatalf("member %q changed", k)
			}
		}
		if len(got) != len(members) {
			t.Fatalf("the output has %d members, the input %d", len(got), len(members))
		}
		checkMemberCase(t, in)
	})
}

// TestParseMemberCase is T-1097: each member of ReadMembers in another case, alone and beside the
// exact one, makes the request invalid with the reason member_case, for both operations; the read
// set equals the names the parser passes to the family helpers.
func TestParseMemberCase(t *testing.T) {
	swapped := func(name string) string {
		if u := strings.ToUpper(name); u != name {
			return u
		}
		return strings.ToLower(name)
	}
	refused := func(t *testing.T, body string) {
		t.Helper()
		for _, op := range operations {
			r, err := anthropic.ParseOperation(op, []byte(body))
			if err == nil || r != nil {
				t.Errorf("%s: %s accepted", op, body)
				continue
			}
			if !errors.Is(err, family.ErrInvalidRequest) || family.InvalidReason(err) != "member_case" {
				t.Errorf("%s: %s: reason %q (%v), want member_case", op, body, family.InvalidReason(err), err)
			}
			if strings.Contains(err.Error(), body) || strings.Contains(err.Error(), "MODEL") {
				t.Errorf("the error quotes the body: %v", err)
			}
		}
	}
	for _, name := range anthropic.ReadMembers {
		other := swapped(name)
		t.Run(name, func(t *testing.T) {
			if name == "model" {
				refused(t, `{"`+other+`":"m"}`)
				refused(t, `{"model":"m","`+other+`":"n"}`)
				refused(t, `{"`+other+`":"n","model":"m"}`)
				return
			}
			refused(t, `{"model":"m","`+other+`":1}`)
			refused(t, `{"model":"m","`+name+`":1,"`+other+`":1}`)
			refused(t, `{"`+other+`":1,"model":"m","`+name+`":1}`)
			refused(t, `{"model":"m","`+other+`":{"nested":[1,2]}}`)
		})
	}
	t.Run("letters that fold to an ASCII one", func(t *testing.T) {
		refused(t, "{\"model\":\"m\",\"ſtream\":true}")
		refused(t, "{\"model\":\"m\",\"max_to\u212Aens\":1}")
		refused(t, "{\"model\":\"m\",\"stream\":true,\"\u017Ftream\":false}")
		refused(t, "{\"model\":\"m\",\"Max_Tokens\":1}")
	})
	t.Run("the exact names are accepted", func(t *testing.T) {
		for _, op := range operations {
			if _, err := anthropic.ParseOperation(op, []byte(`{"model":"m","stream":true,"max_tokens":1}`)); err != nil {
				t.Errorf("%s: %v", op, err)
			}
		}
	})
	t.Run("the check comes before the model check", func(t *testing.T) {
		for _, body := range []string{`{"Model":1}`, `{"model":1,"MODEL":2}`, `{"model":"m","model":"n","MODEL":"o"}`} {
			_, err := anthropic.ParseOperation(anthropic.OperationMessages, []byte(body))
			if family.InvalidReason(err) != "member_case" {
				t.Errorf("%s: reason %q", body, family.InvalidReason(err))
			}
		}
	})
	t.Run("the read set is the names the parser passes to the family helpers", func(t *testing.T) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "request.go", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		consts := map[string]string{}
		ast.Inspect(f, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[id.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
			return true
		})
		seen := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "family" {
				return true
			}
			var key ast.Expr
			switch sel.Sel.Name {
			case "ParseModel":
				seen["model"] = true
				return true
			case "LastMember":
				key = call.Args[1]
			case "PositiveInt":
				key = call.Args[2]
			default:
				return true
			}
			switch v := key.(type) {
			case *ast.BasicLit:
				s, _ := strconv.Unquote(v.Value)
				seen[s] = true
			case *ast.Ident:
				if s, ok := consts[v.Name]; ok {
					seen[s] = true
				} else {
					t.Errorf("%s: the name %s is not a constant of the file", fset.Position(call.Pos()), v.Name)
				}
			default:
				t.Errorf("%s: a name that is neither a literal nor a constant", fset.Position(call.Pos()))
			}
			return true
		})
		var want []string
		for k := range seen {
			want = append(want, k)
		}
		sort.Strings(want)
		got := append([]string(nil), anthropic.ReadMembers...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("ReadMembers %v, the parser reads %v", got, want)
		}
	})
}

// sameIgnoringCase is the comparison of the invariant: simple folding, upper casing or lower
// casing equates the two names, as a reader of another language might.
func sameIgnoringCase(a, b string) bool {
	return strings.EqualFold(a, b) || strings.ToUpper(a) == strings.ToUpper(b) || strings.ToLower(a) == strings.ToLower(b) //nolint:staticcheck // SA6005: the explicit upper and lower comparisons are the point, they differ from simple folding
}

// checkMemberCase is the invariant of sprint 12.2 section 5.9: a body accepted has exactly one
// member equal to model when case is ignored, and no member that differs only in case from a
// member of the read set.
func checkMemberCase(t *testing.T, in []byte) {
	t.Helper()
	ms, _, err := family.ScanObject(in)
	if err != nil {
		t.Fatalf("accepted a body ScanObject refuses: %v", err)
	}
	models := 0
	for _, m := range ms {
		if sameIgnoringCase(m.Key, "model") {
			models++
		}
		for _, name := range anthropic.ReadMembers {
			if m.Key != name && sameIgnoringCase(m.Key, name) {
				t.Fatalf("accepted a member %q that differs only in case from %q", m.Key, name)
			}
		}
	}
	if models != 1 {
		t.Fatalf("accepted a body with %d members equal to model when case is ignored", models)
	}
}
