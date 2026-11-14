package family

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/test/fixtures"
)

var flattenKey = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

// TestFlattenUsage is T-464: the bounds of the flattener.
func TestFlattenUsage(t *testing.T) {
	many := func(n int) (string, map[string]int64) {
		var sb strings.Builder
		want := map[string]int64{}
		sb.WriteString("{")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `"k%02d":%d`, i, i)
			if i < 32 {
				want[fmt.Sprintf("k%02d", i)] = int64(i)
			}
		}
		sb.WriteString("}")
		return sb.String(), want
	}
	in40, want32 := many(40)
	in32, all32 := many(32)
	long64 := strings.Repeat("a", 64)
	long65 := strings.Repeat("a", 65)

	for _, c := range []struct {
		name, in string
		want     map[string]int64
	}{
		{"a flat object", `{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}`,
			map[string]int64{"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21}},
		{"nesting is joined with dots", `{"prompt_tokens":11,"prompt_tokens_details":{"cached_tokens":3,"audio_tokens":4},"x":{"y":{"z":5}}}`,
			map[string]int64{"prompt_tokens": 11, "prompt_tokens_details.cached_tokens": 3, "prompt_tokens_details.audio_tokens": 4, "x.y.z": 5}},
		{"depth three is kept and depth four dropped", `{"a":{"b":{"c":1,"d":{"e":2}}},"f":{"g":{"h":{"i":{"j":3}}}},"k":4}`,
			map[string]int64{"a.b.c": 1, "k": 4}},
		{"zero is kept", `{"a":0}`, map[string]int64{"a": 0}},
		{"strings are dropped", `{"a":"7","b":1,"c":"` + fixtures.CanaryCompletion + `"}`, map[string]int64{"b": 1}},
		{"arrays are dropped", `{"a":[1,2],"b":1}`, map[string]int64{"b": 1}},
		{"booleans and null are dropped", `{"a":true,"b":false,"c":null,"d":1}`, map[string]int64{"d": 1}},
		{"fractions are dropped", `{"a":1.5,"b":2.0,"c":0.0,"d":1}`, map[string]int64{"d": 1}},
		{"exponents are dropped", `{"a":1e3,"b":1E2,"c":5e-1,"d":1}`, map[string]int64{"d": 1}},
		{"negative numbers are dropped", `{"a":-1,"b":-9223372036854775808,"c":1}`, map[string]int64{"c": 1}},
		{"the largest int64 is kept, one more is dropped", `{"a":9223372036854775807,"b":9223372036854775808,"c":18446744073709551616,"d":1e400}`,
			map[string]int64{"a": 9223372036854775807}},
		{"a key with a canary is dropped", `{"` + fixtures.CanaryPrompt + `":5,"ok":1}`, map[string]int64{"ok": 1}},
		{"keys outside the pattern are dropped", `{"Upper":1,"with space":2,"dash-ed":3,"é":4,"":5,"` + long65 + `":6,"` + long64 + `":7,"ok_1.2":8}`,
			map[string]int64{long64: 7, "ok_1.2": 8}},
		{"a joined key over 64 characters is dropped", `{"` + strings.Repeat("a", 40) + `":{"` + strings.Repeat("b", 30) + `":1}}`, nil},
		{"the 33rd entry is dropped, in input order", in40, want32},
		{"exactly 32 entries are kept", in32, all32},
		{"nested entries count towards the bound", `{"a":{"` + strings.Join(func() []string {
			var ks []string
			for i := 0; i < 40; i++ {
				ks = append(ks, fmt.Sprintf(`k%02d":%d`, i, i))
			}
			return ks
		}(), `,"`) + `},"z":1}`, func() map[string]int64 {
			m := map[string]int64{}
			for i := 0; i < 32; i++ {
				m[fmt.Sprintf("a.k%02d", i)] = int64(i)
			}
			return m
		}()},
		{"an empty object", `{}`, nil},
		{"nothing kept", `{"a":"x","b":[1]}`, nil},
		{"an array", `[1,2,3]`, nil},
		{"a number", `5`, nil},
		{"a string", `"x"`, nil},
		{"null", `null`, nil},
		{"empty input", ``, nil},
		{"surrounding white space", " \n{\"a\":1}\t ", map[string]int64{"a": 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := []byte(c.in)
			orig := append([]byte(nil), in...)
			got := FlattenUsage(in)
			if !bytes.Equal(in, orig) {
				t.Error("the input was modified")
			}
			if c.want == nil {
				if got != nil {
					t.Errorf("got %v, want nil", got)
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v\nwant %v", got, c.want)
			}
		})
	}
	t.Run("broken JSON does not panic and stays within bounds", func(t *testing.T) {
		for _, in := range []string{`{`, `{"a"`, `{"a":`, `{"a":1`, `{"a":1,`, `{"a":{"b":`, `{"a":1}}`, `{"a":1} {"b":2}`} {
			checkReported(t, FlattenUsage([]byte(in)))
		}
	})
}

func checkReported(t testing.TB, m map[string]int64) {
	t.Helper()
	if len(m) > 32 {
		t.Fatalf("%d entries", len(m))
	}
	for k, v := range m {
		if !flattenKey.MatchString(k) || v < 0 {
			t.Fatalf("entry %q = %d", k, v)
		}
	}
}

// topLevelKeys lists the member names of the top-level object of valid JSON, with repeats.
func topLevelKeys(b []byte) (keys []string, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if d, isDelim := tok.(json.Delim); err != nil || !isDelim || d != '{' {
		return nil, false
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, _ := k.(string)
		keys = append(keys, name)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, false
		}
	}
	return keys, true
}

// FuzzFlattenUsage is T-463: no panic; the result is within its bounds; a top-level member that
// encoding/json reads as a non-negative integer is kept unless the map is at its bound.
func FuzzFlattenUsage(f *testing.F) {
	f.Add([]byte(`{"prompt_tokens":9,"prompt_tokens_details":{"cached_tokens":3}}`))
	f.Add([]byte(`{"a":{"b":{"c":{"d":1}}},"e":1e400,"f":9223372036854775808,"g":-1,"h":1.5}`))
	f.Add([]byte(`{"` + fixtures.CanaryPrompt + `":1,"A":2}`))
	f.Add([]byte(`[1,2]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, in []byte) {
		got := FlattenUsage(in)
		checkReported(t, got)
		var members map[string]json.RawMessage
		if json.Unmarshal(in, &members) != nil {
			return
		}
		keys, ok := topLevelKeys(in)
		if !ok {
			return
		}
		seen := map[string]int{}
		for _, k := range keys {
			seen[k]++
		}
		if len(got) >= 32 {
			return
		}
		for k, raw := range members {
			if seen[k] != 1 || !flattenKey.MatchString(k) {
				continue
			}
			n, err := strconv.ParseInt(string(raw), 10, 64)
			if err != nil || n < 0 || strings.HasPrefix(string(raw), "+") {
				continue
			}
			if v, ok := got[k]; !ok || v != n {
				t.Fatalf("member %q = %s is not kept (got %v)", k, raw, got)
			}
		}
	})
}
