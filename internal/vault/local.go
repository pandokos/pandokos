package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"

	"github.com/pandokos/pandokos/internal/secret"
)

const (
	localName      = "local"
	localKeyLen    = 32
	localKeyLine   = 44 // base64 with padding of 32 bytes
	wrapFormat     = 0x01
	localIDLabel   = "pandokos kek id v1"
	localIDHexSize = 8
)

// LocalProvider wraps data keys with a key-encryption key read from a file. It holds a map from
// version identifier to key and one active identifier; this sprint loads one key. Every
// formatting path prints [REDACTED].
type LocalProvider struct {
	keys   map[string]cipher.AEAD
	active string
}

// NewLocalProvider builds a provider from the content of a key file: the standard base64
// encoding, with padding, of 32 bytes. Anything else is ErrInvalidKey, which quotes nothing.
func NewLocalProvider(key secret.Secret) (*LocalProvider, error) {
	line := key.Reveal()
	if len(line) != localKeyLine {
		return nil, ErrInvalidKey
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(line)
	if err != nil || len(raw) != localKeyLen {
		clear(raw)
		return nil, ErrInvalidKey
	}
	defer clear(raw)
	blk, err := aes.NewCipher(raw)
	if err != nil {
		return nil, ErrInvalidKey
	}
	aead, err := cipher.NewGCMWithRandomNonce(blk)
	if err != nil {
		return nil, ErrInvalidKey
	}
	id := localVersion(raw)
	return &LocalProvider{keys: map[string]cipher.AEAD{id: aead}, active: id}, nil
}

// localVersion derives the identifier of a key: "local-" and the first 8 bytes, in
// hexadecimal, of SHA-256("pandokos kek id v1" 0x00 key). A wrong file can never be taken for
// the right one.
func localVersion(raw []byte) string {
	h := sha256.New()
	h.Write([]byte(localIDLabel))
	h.Write([]byte{0})
	h.Write(raw)
	return localName + "-" + hex.EncodeToString(h.Sum(nil)[:localIDHexSize])
}

// GenerateLocalKey returns a new key-encryption key in the form of a key file: 32 bytes from
// crypto/rand, in standard base64 with padding.
func GenerateLocalKey() (secret.Secret, error) {
	raw := make([]byte, localKeyLen)
	defer clear(raw)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return secret.Secret{}, fmt.Errorf("vault: no random bytes: %w", err)
	}
	return secret.New(base64.StdEncoding.EncodeToString(raw)), nil
}

// Name returns "local".
func (p *LocalProvider) Name() string { return localName }

// Wrap encrypts plaintext with AES-256-GCM under the active key and a random 96-bit nonce,
// binding aad. The result is 0x01, nonce, ciphertext, tag.
func (p *LocalProvider) Wrap(_ context.Context, plaintext, aad []byte) (string, []byte, error) {
	g := p.keys[p.active]
	out := make([]byte, 1, 1+len(plaintext)+g.Overhead())
	out[0] = wrapFormat
	return p.active, g.Seal(out, nil, plaintext, aad), nil
}

// Unwrap decrypts a value made by Wrap under the key of version. The result is the caller's.
func (p *LocalProvider) Unwrap(_ context.Context, version string, wrapped, aad []byte) ([]byte, error) {
	g, ok := p.keys[version]
	if !ok {
		return nil, ErrKeyVersionUnknown
	}
	if len(wrapped) < 1 || wrapped[0] != wrapFormat {
		return nil, ErrUnwrap
	}
	out, err := g.Open(nil, nil, wrapped[1:], aad)
	if err != nil {
		return nil, ErrUnwrap
	}
	return out, nil
}

// Available reports whether the provider holds the key of version.
func (p *LocalProvider) Available(_ context.Context, version string) error {
	if _, ok := p.keys[version]; !ok {
		return ErrKeyVersionUnknown
	}
	return nil
}

// String returns the redaction marker.
func (LocalProvider) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (LocalProvider) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (LocalProvider) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (LocalProvider) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (LocalProvider) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// MarshalText returns the redaction marker.
func (LocalProvider) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }
