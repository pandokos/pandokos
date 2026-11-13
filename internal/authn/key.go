// Package authn authenticates the keys of Pandokos: the key format, the authenticator of the
// two listeners and the principal cache of the gateway (ADR 0005, ADR 0030).
package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"hash/crc32"

	"github.com/pandokos/pandokos/internal/secret"
)

// Kind is the kind letter of a key.
type Kind byte

// The kinds of key.
const (
	KindTenant   Kind = 't' // a virtual key of a tenant's service account
	KindPlatform Kind = 'p' // a platform key
)

// ErrMalformed is returned by Parse for anything that is not a key. It never holds the input.
var ErrMalformed = errors.New("authn: malformed key")

// Pattern is the regular expression of a key, for documentation and secret scanners.
const Pattern = `pdk_[tp][a-z2-7]{16}_[a-z2-7]{59}`

const (
	keyAlphabet     = "abcdefghijklmnopqrstuvwxyz234567"
	keyLength    = 81
	checksumFrom = 74
)

var enc = base32.NewEncoding(keyAlphabet).WithPadding(base32.NoPadding)

func checksum(prefix string) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], crc32.ChecksumIEEE([]byte(prefix)))
	return enc.EncodeToString(b[:])
}

// Generate returns a new key, its key ID and the hash to store (SHA-256 of the 32 secret bytes).
func Generate(kind Kind) (key secret.Secret, keyID string, hash [32]byte, err error) {
	if kind != KindTenant && kind != KindPlatform {
		return secret.Secret{}, "", hash, errors.New("authn: unknown key kind")
	}
	var id [10]byte
	var sec [32]byte
	if _, err = rand.Read(id[:]); err != nil {
		return secret.Secret{}, "", hash, err
	}
	if _, err = rand.Read(sec[:]); err != nil {
		return secret.Secret{}, "", hash, err
	}
	keyID = string(kind) + enc.EncodeToString(id[:])
	prefix := "pdk_" + keyID + "_" + enc.EncodeToString(sec[:])
	return secret.New(prefix + checksum(prefix)), keyID, sha256.Sum256(sec[:]), nil
}

// Parse checks length, prefix, keyAlphabet and checksum, without any lookup, and returns the key ID,
// the kind and the SHA-256 of the 32 secret bytes.
func Parse(presented string) (keyID string, kind Kind, hash [32]byte, err error) {
	if len(presented) != keyLength || presented[:4] != "pdk_" {
		return "", 0, hash, ErrMalformed
	}
	kind = Kind(presented[4])
	if kind != KindTenant && kind != KindPlatform {
		return "", 0, hash, ErrMalformed
	}
	if presented[3] != '_' || presented[21] != '_' {
		return "", 0, hash, ErrMalformed
	}
	for i := 5; i < keyLength; i++ {
		if i == 21 {
			continue
		}
		c := presented[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return "", 0, hash, ErrMalformed
		}
	}
	if checksum(presented[:checksumFrom]) != presented[checksumFrom:] {
		return "", 0, hash, ErrMalformed
	}
	raw, derr := enc.DecodeString(presented[22:checksumFrom])
	if derr != nil || len(raw) != 32 {
		return "", 0, hash, ErrMalformed
	}
	return presented[4:21], kind, sha256.Sum256(raw), nil
}
