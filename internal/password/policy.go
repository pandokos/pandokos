package password

import (
	"strings"
	"unicode/utf8"

	"github.com/pandokos/pandokos/internal/secret"
)

const (
	maxCharacters = 256
	maxBytes      = 1024
)

// Check returns the rule that pw breaks, or "" when it is acceptable: too short, too long, the
// username, in this order. There is no composition rule and nothing is truncated.
func Check(pw secret.Secret, username string, minLength int) (violation string) {
	v := pw.Reveal()
	n := utf8.RuneCountInString(v)
	switch {
	case n < minLength:
		return "must have at least " + itoa(minLength) + " characters"
	case n > maxCharacters || len(v) > maxBytes:
		return "must have at most 256 characters"
	case username != "" && strings.EqualFold(v, username):
		return "must not be the username"
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
