package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/tenant"
)

const (
	dataKeyLen          = 32
	sealFormat          = 0x01
	hkdfInfoEncryption  = "pandokos/vault/v1/credential-encryption"
	hkdfInfoFingerprint = "pandokos/vault/v1/credential-fingerprint"
	labelDataAAD        = "pandokos/dek/v1"
	labelCredAAD        = "pandokos/credential/v1" //nolint:gosec // a format label, not a credential
	fingerprintLen      = 16
	hintMinChars        = 16
	hintChars           = 4
)

var errCredentialID = errors.New("vault: the credential id is not a canonical UUID")

// DataAAD returns the associated data bound to a wrapped data key: "pandokos/dek/v1", 0x00,
// the 16 bytes of the tenant ID and the version as four big-endian bytes.
func DataAAD(id tenant.ID, version int32) []byte {
	b := make([]byte, 0, len(labelDataAAD)+1+16+4)
	b = append(b, labelDataAAD...)
	b = append(b, 0)
	b = append(b, id[:]...)
	return binary.BigEndian.AppendUint32(b, uint32(version)) // #nosec G115 -- a bit pattern, fixed by the format
}

// credentialAAD returns the associated data bound to a sealed credential: "pandokos/credential/v1",
// 0x00, tenant, credential and data key version. credentialID must be a canonical UUID.
func credentialAAD(id tenant.ID, credentialID string, version int32) ([]byte, error) {
	cid, err := tenant.ParseID(credentialID) // the same canonical form; the zero UUID is never an ID
	if err != nil {
		return nil, errCredentialID
	}
	b := make([]byte, 0, len(labelCredAAD)+1+16+16+4)
	b = append(b, labelCredAAD...)
	b = append(b, 0)
	b = append(b, id[:]...)
	b = append(b, cid[:]...)
	return binary.BigEndian.AppendUint32(b, uint32(version)), nil // #nosec G115 -- a bit pattern
}

// DataKey is the data key of a tenant, reduced to the two subkeys derived from it. It has no
// exported field and every formatting path prints [REDACTED].
type DataKey struct {
	enc     cipher.AEAD
	fpKey   []byte
	tenant  tenant.ID
	version int32
}

// NewDataKey derives the subkeys of a data key of 32 bytes. It does not retain raw; the caller
// clears it.
func NewDataKey(raw []byte, id tenant.ID, version int32) (*DataKey, error) {
	if len(raw) != dataKeyLen {
		return nil, errors.New("vault: a data key is 32 bytes")
	}
	enc, err := hkdf.Key(sha256.New, raw, nil, hkdfInfoEncryption, 32)
	if err != nil {
		return nil, fmt.Errorf("vault: key derivation: %w", err)
	}
	defer clear(enc)
	fp, err := hkdf.Key(sha256.New, raw, nil, hkdfInfoFingerprint, 32)
	if err != nil {
		return nil, fmt.Errorf("vault: key derivation: %w", err)
	}
	blk, err := aes.NewCipher(enc)
	if err != nil {
		return nil, fmt.Errorf("vault: cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(blk)
	if err != nil {
		return nil, fmt.Errorf("vault: cipher: %w", err)
	}
	return &DataKey{enc: aead, fpKey: fp, tenant: id, version: version}, nil
}

// Seal encrypts plaintext for credentialID: 0x01, nonce, ciphertext, tag. An empty plaintext is
// refused.
func (k *DataKey) Seal(credentialID string, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("vault: nothing to seal")
	}
	aad, err := credentialAAD(k.tenant, credentialID, k.version)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1, 1+len(plaintext)+k.enc.Overhead())
	out[0] = sealFormat
	return k.enc.Seal(out, nil, plaintext, aad), nil
}

// Open decrypts a sealed value of credentialID. Every failure is ErrUndecryptable. The result
// is the caller's to clear.
func (k *DataKey) Open(credentialID string, sealed []byte) ([]byte, error) {
	aad, err := credentialAAD(k.tenant, credentialID, k.version)
	if err != nil {
		return nil, err
	}
	if len(sealed) < 1 || sealed[0] != sealFormat {
		return nil, ErrUndecryptable
	}
	out, err := k.enc.Open(nil, nil, sealed[1:], aad)
	if err != nil {
		return nil, ErrUndecryptable
	}
	return out, nil
}

// Fingerprint returns the first 16 bytes of HMAC-SHA-256 of plaintext under the fingerprint
// subkey, as 32 hexadecimal characters.
func (k *DataKey) Fingerprint(plaintext []byte) string {
	m := hmac.New(sha256.New, k.fpKey)
	m.Write(plaintext)
	return hex.EncodeToString(m.Sum(nil)[:fingerprintLen])
}

// Hint returns the last four characters of a secret of 16 characters or more, and the empty
// string for a shorter one.
func Hint(secretText string) string {
	r := []rune(secretText)
	if len(r) < hintMinChars {
		return ""
	}
	return string(r[len(r)-hintChars:])
}

// String returns the redaction marker.
func (DataKey) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (DataKey) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (DataKey) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (DataKey) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (DataKey) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// MarshalText returns the redaction marker.
func (DataKey) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }
