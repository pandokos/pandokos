package password

import (
	"crypto/rand"
	"fmt"

	"github.com/pandokos/pandokos/internal/secret"
)

const (
	alphabet        = "abcdefghijklmnopqrstuvwxyz234567"
	generatedLength = 24
)

// Generate returns a password of 24 characters of a-z2-7 (120 bits) from crypto/rand.
func Generate() (secret.Secret, error) {
	b := make([]byte, generatedLength)
	if _, err := rand.Read(b); err != nil {
		return secret.Secret{}, fmt.Errorf("password: random: %w", err)
	}
	for i, v := range b {
		b[i] = alphabet[v&31] // 32 symbols: no modulo bias
	}
	return secret.New(string(b)), nil
}
