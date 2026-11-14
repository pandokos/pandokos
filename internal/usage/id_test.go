package usage_test

import (
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pandokos/pandokos/internal/usage"
)

var uuidV7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewID is T-468.
func TestNewID(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 30, 15, 987654321, time.UTC)

	t.Run("version, variant and canonical form", func(t *testing.T) {
		for range 1000 {
			id := usage.NewID(at)
			if !uuidV7.MatchString(id) {
				t.Fatalf("%q is not a canonical lower-case UUID version 7", id)
			}
		}
	})

	t.Run("the time of the ID is the given time", func(t *testing.T) {
		for _, when := range []time.Time{at, time.UnixMilli(1), time.UnixMilli(1<<47 - 1), time.Now(), at.Add(-24 * 365 * time.Hour)} {
			id := usage.NewID(when)
			raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
			if err != nil || len(raw) != 16 {
				t.Fatalf("%q: %v", id, err)
			}
			var ms int64
			for _, b := range raw[:6] {
				ms = ms<<8 | int64(b)
			}
			if ms != when.UnixMilli() {
				t.Errorf("%q carries %d ms, want %d", id, ms, when.UnixMilli())
			}
			// The same instant in another zone gives the same time field.
			other := usage.NewID(when.In(time.FixedZone("x", 5*3600)))
			if other[:13] != id[:13] {
				t.Errorf("the time field depends on the zone: %q %q", id, other)
			}
		}
	})

	t.Run("100 000 IDs of one millisecond are distinct", func(t *testing.T) {
		seen := make(map[string]struct{}, 100_000)
		for range 100_000 {
			id := usage.NewID(at)
			if _, dup := seen[id]; dup {
				t.Fatalf("%q twice", id)
			}
			seen[id] = struct{}{}
		}
	})

	t.Run("IDs of increasing times sort", func(t *testing.T) {
		var ids []string
		for i := range 200 {
			ids = append(ids, usage.NewID(at.Add(time.Duration(i)*time.Millisecond)))
		}
		if !sort.StringsAreSorted(ids) {
			t.Error("IDs of increasing milliseconds do not sort in order")
		}
	})

	t.Run("the random part is random", func(t *testing.T) {
		a, b := usage.NewID(at), usage.NewID(at)
		if a == b || a[24:] == b[24:] {
			t.Errorf("%q %q", a, b)
		}
	})
}
