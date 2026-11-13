package requestid

import (
	"regexp"
	"testing"

	"github.com/pandokos/pandokos/internal/family"
)

func TestNew(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New()
		if !re.MatchString(id) {
			t.Fatalf("request ID %q is not 32 hexadecimal characters", id)
		}
		if seen[id] {
			t.Fatalf("request ID %q repeated", id)
		}
		seen[id] = true
	}
}

func TestHeader(t *testing.T) {
	if Header != "X-Pandokos-Request-Id" || Header != family.HeaderRequestID {
		t.Errorf("Header = %q", Header)
	}
}
