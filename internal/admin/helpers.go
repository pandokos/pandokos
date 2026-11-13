package admin

import (
	"errors"
	"time"

	"github.com/pandokos/pandokos/internal/store"
)

func isNoRowsErr(err error) bool { return errors.Is(err, store.ErrNoRows) }

func fmtTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func change(from, to any) map[string]any { return map[string]any{"from": from, "to": to} }

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// limitOf returns the page size and the number of rows to ask for.
func limitOf(n int) (limit int, ask int32) {
	if n < 1 {
		n = 50
	}
	return n, int32(n + 1) //nolint:gosec // limit is at most 200 by the API document
}
