package usage_test

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/pandokos/pandokos/internal/usage"
	"github.com/pandokos/pandokos/test/fixtures"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

// TestParseMetadata is T-466: each rule of section 5.3.
func TestParseMetadata(t *testing.T) {
	const p = "X-Pandokos-Metadata-"
	long := func(n int) string { return strings.Repeat("v", n) }

	for _, c := range []struct {
		name        string
		h           http.Header
		kept        map[string]string
		dropped     int
		descriptive string
	}{
		{name: "no headers", h: http.Header{}, kept: nil, dropped: 0},
		{name: "one entry", h: hdr(p+"team", "alpha"), kept: map[string]string{"team": "alpha"}},
		{name: "the key is lower-cased", h: hdr(p+"Cost-Center", "x1", p+"TEAM", "a"), kept: map[string]string{"cost-center": "x1", "team": "a"}},
		{name: "a lower-case header name in the map qualifies", h: http.Header{"x-pandokos-metadata-lower": {"v"}}, kept: map[string]string{"lower": "v"}},
		{name: "key characters", h: hdr(p+"a_b-c9", "v", p+"9", "n"), kept: map[string]string{"a_b-c9": "v", "9": "n"}},
		{name: "a key may not start with - or _", h: hdr(p+"-x", "v", p+"_x", "v"), kept: nil, dropped: 2},
		{name: "an empty key", h: hdr(p, "v"), kept: nil, dropped: 1},
		{name: "a key of 32 characters", h: hdr(p+strings.Repeat("a", 32), "v"), kept: map[string]string{strings.Repeat("a", 32): "v"}},
		{name: "a key of 33 characters", h: hdr(p+strings.Repeat("a", 33), "v"), kept: nil, dropped: 1},
		{name: "a key with a dot", h: hdr(p+"a.b", "v"), kept: nil, dropped: 1},
		{name: "value trimmed of spaces", h: hdr(p+"a", "  padded  "), kept: map[string]string{"a": "padded"}},
		{name: "inner spaces stay", h: hdr(p+"a", "two words"), kept: map[string]string{"a": "two words"}},
		{name: "an empty value", h: hdr(p+"a", ""), kept: nil, dropped: 1},
		{name: "a value of spaces", h: hdr(p+"a", "   "), kept: nil, dropped: 1},
		{name: "a value of 128 bytes", h: hdr(p+"a", long(128)), kept: map[string]string{"a": long(128)}},
		{name: "a value of 129 bytes", h: hdr(p+"a", long(129)), kept: nil, dropped: 1},
		{name: "the bound counts the trimmed value", h: hdr(p+"a", " "+long(128)+" "), kept: map[string]string{"a": long(128)}},
		{name: "printable ASCII bounds", h: hdr(p+"a", "~ !"), kept: map[string]string{"a": "~ !"}},
		{name: "a control byte", h: http.Header{p + "A": {"a\x01b"}}, kept: nil, dropped: 1},
		{name: "a tab", h: http.Header{p + "A": {"a\tb"}}, kept: nil, dropped: 1},
		{name: "DEL", h: http.Header{p + "A": {"a\x7fb"}}, kept: nil, dropped: 1},
		{name: "a non-ASCII value", h: http.Header{p + "A": {"café"}}, kept: nil, dropped: 1},
		{name: "a value with an emoji", h: http.Header{p + "A": {"x\U0001F600"}}, kept: nil, dropped: 1},
		{name: "two values for one key: neither is kept", h: http.Header{p + "A": {"one", "two"}}, kept: nil, dropped: 1},
		{name: "the dropped counter header is not metadata", h: hdr(p+"Dropped", "5", p+"a", "v"), kept: map[string]string{"a": "v"}},
		{name: "other X-Pandokos headers are ignored", h: hdr("X-Pandokos-Request-Id", "x", "X-Pandokos-Error", "y", "X-Pandokos-Metadata", "z", "X-Pandokos-Metadatax", "w", "X-Other-Metadata-a", "v"), kept: nil},
		{name: "other headers are ignored", h: hdr("Authorization", "Bearer x", "Content-Type", "application/json"), kept: nil},
		{name: "a bad entry beside a good one", h: hdr(p+"ok", "v", p+"bad key", "v"), kept: map[string]string{"ok": "v"}, dropped: 1},
		{
			name:    "the ninth key in key order is dropped",
			h:       hdr(p+"i", "9", p+"h", "8", p+"g", "7", p+"f", "6", p+"e", "5", p+"d", "4", p+"c", "3", p+"b", "2", p+"a", "1"),
			kept:    map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8"},
			dropped: 1,
		},
		{
			name:    "key order is the byte order of the lower-cased keys",
			h:       hdr(p+"b", "1", p+"a_x", "1", p+"a-x", "1", p+"a1", "1", p+"0", "1", p+"Z", "1", p+"a", "1", p+"c", "1", p+"d", "1", p+"e", "1"),
			kept:    map[string]string{"0": "1", "a": "1", "a-x": "1", "a1": "1", "a_x": "1", "b": "1", "c": "1", "d": "1"},
			dropped: 2, // "e" and "z"
		},
		{
			name:    "entries that fail do not take a place among the eight",
			h:       hdr(p+"-bad", "v", p+"a", "1", p+"b", "1", p+"c", "1", p+"d", "1", p+"e", "1", p+"f", "1", p+"g", "1", p+"h", "1", p+"i", "1", p+"empty", ""),
			kept:    map[string]string{"a": "1", "b": "1", "c": "1", "d": "1", "e": "1", "f": "1", "g": "1", "h": "1"},
			dropped: 3, // "-bad", "empty", "i"
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := c.h.Clone()
			kept, dropped := usage.ParseMetadata(c.h)
			if !reflect.DeepEqual(c.h, before) {
				t.Error("the header was modified")
			}
			if len(kept) != len(c.kept) || (len(kept) > 0 && !reflect.DeepEqual(kept, c.kept)) {
				t.Errorf("kept %v, want %v", kept, c.kept)
			}
			if dropped != c.dropped {
				t.Errorf("dropped %d, want %d", dropped, c.dropped)
			}
		})
	}

	t.Run("the dropped count saturates at the bound of the table", func(t *testing.T) {
		h := http.Header{}
		for i := 0; i < 1200; i++ {
			h[fmt.Sprintf("%sbad.%d", p, i)] = []string{"v"}
		}
		h[p+"ok"] = []string{"v"}
		kept, dropped := usage.ParseMetadata(h)
		if dropped != 1000 || len(kept) != 1 {
			t.Errorf("kept %d dropped %d, want 1 and 1000", len(kept), dropped)
		}
	})

	t.Run("no header that qualifies allocates nothing", func(t *testing.T) {
		h := hdr("Authorization", "Bearer x", "Content-Type", "application/json", "X-Pandokos-Request-Id", "r", "X-Pandokos-Metadata-Dropped", "3")
		allocs := testing.AllocsPerRun(100, func() { usage.ParseMetadata(h) })
		if allocs != 0 {
			t.Errorf("%v allocations", allocs)
		}
		if kept, dropped := usage.ParseMetadata(h); kept != nil || dropped != 0 {
			t.Errorf("%v %d", kept, dropped)
		}
	})

	t.Run("a canary value is returned as sent", func(t *testing.T) {
		kept, _ := usage.ParseMetadata(hdr(p+"note", fixtures.CanaryPrompt))
		if kept["note"] != fixtures.CanaryPrompt {
			t.Errorf("%v", kept)
		}
	})
}

// FuzzParseMetadata is T-467: no panic; at most eight entries; every kept key and value within
// its bound; the counts add up.
func FuzzParseMetadata(f *testing.F) {
	f.Add("a|B|c", "1|two words|", uint8(1))
	f.Add("a|a|a", "1|2|3", uint8(2))
	f.Add("-x|"+strings.Repeat("k", 40)+"|dropped", "v|\x01|9", uint8(0))
	f.Add("a|b|c|d|e|f|g|h|i|j", "1|2|3|4|5|6|7|8|9|0", uint8(3))
	f.Add("é|x y", "é| ", uint8(1))
	f.Fuzz(func(t *testing.T, names, values string, dup uint8) {
		h := http.Header{}
		ns, vs := strings.Split(names, "|"), strings.Split(values, "|")
		for i, n := range ns {
			v := ""
			if i < len(vs) {
				v = vs[i]
			}
			name := "X-Pandokos-Metadata-" + n
			h[name] = append(h[name], v)
			for range int(dup % 3) {
				h[name] = append(h[name], v)
			}
		}
		kept, dropped := usage.ParseMetadata(h)
		if len(kept) > 8 {
			t.Fatalf("%d entries kept", len(kept))
		}
		if dropped < 0 || dropped > 1000 {
			t.Fatalf("dropped %d", dropped)
		}
		for k, v := range kept {
			if !validKey(k) {
				t.Fatalf("kept key %q", k)
			}
			if len(v) < 1 || len(v) > 128 || v != strings.Trim(v, " ") {
				t.Fatalf("kept value %q", v)
			}
			for i := 0; i < len(v); i++ {
				if v[i] < 0x20 || v[i] > 0x7e {
					t.Fatalf("kept value %q has byte %#x", v, v[i])
				}
			}
		}
		if len(kept)+dropped > len(h) {
			t.Fatalf("%d kept and %d dropped from %d headers", len(kept), dropped, len(h))
		}
	})
}

func validKey(k string) bool {
	if len(k) < 1 || len(k) > 32 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}
