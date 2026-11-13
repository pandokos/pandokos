package vault

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// dataKeyVersion is the only version of a tenant's data key this sprint creates.
const dataKeyVersion int32 = 1

// Sealer is the administration side of the vault: it creates the data key of a tenant and seals
// credentials under it. It never reads a sealed secret and keeps no data key in memory beyond
// one call.
type Sealer struct {
	p KeyProvider
}

// Sealed is what Seal returns: the sealed secret and the metadata derived from it. It prints
// nothing but the redaction marker.
type Sealed struct {
	// KeyVersion is the version of the data key the secret was sealed under.
	KeyVersion int32
	// Secret is the sealed secret: 0x01, nonce, ciphertext, tag.
	Secret []byte
	// Fingerprint and Hint identify the secret without revealing it.
	Fingerprint, Hint string
	// DataKeyCreated reports whether this call created the data key of the tenant.
	DataKeyCreated bool
	// KEKVersion is the version of the key-encryption key that wraps the data key.
	KEKVersion string
}

// NewSealer returns a Sealer that wraps data keys with p.
func NewSealer(p KeyProvider) (*Sealer, error) {
	if p == nil {
		return nil, errors.New("vault: a sealer needs a key provider")
	}
	return &Sealer{p: p}, nil
}

// EnsureDataKey creates version 1 of the data key of tenant id if it is missing. tx is a
// session of that tenant (or the provisioning transaction after bind). It reports whether this
// call created the key and the version of the key-encryption key that wraps it. Two concurrent
// first calls both wrap a fresh key; one row wins and the loser reads it back.
func (s *Sealer) EnsureDataKey(ctx context.Context, tx store.Tx, id tenant.ID) (created bool, kekVersion string, err error) {
	q := queries.New(tx)
	row, err := q.GetDataKey(ctx, dataKeyVersion)
	if err == nil {
		return false, row.KekVersion, nil
	}
	if !errors.Is(err, store.ErrNoRows) {
		return false, "", err
	}
	raw := make([]byte, dataKeyLen)
	defer clear(raw)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return false, "", fmt.Errorf("vault: no random bytes: %w", err)
	}
	version, wrapped, err := s.p.Wrap(ctx, raw, DataAAD(id, dataKeyVersion))
	if err != nil {
		return false, "", fmt.Errorf("%w: the data key cannot be wrapped: %w", ErrUnavailable, err)
	}
	if err := q.RegisterKEKVersion(ctx, queries.RegisterKEKVersionParams{ID: version, Provider: s.p.Name()}); err != nil {
		return false, "", err
	}
	n, err := q.CreateDataKey(ctx, queries.CreateDataKeyParams{
		TenantID: id.String(), Version: dataKeyVersion, KekVersion: version, WrappedKey: wrapped,
	})
	if err != nil {
		return false, "", err
	}
	if n == 1 {
		return true, version, nil
	}
	row, err = q.GetDataKey(ctx, dataKeyVersion)
	if err != nil {
		return false, "", err
	}
	return false, row.KekVersion, nil
}

// Seal ensures the data key of the tenant, unwraps it, seals plaintext for credentialID and
// computes the fingerprint and the hint. A data key that does not unwrap is ErrUnavailable.
func (s *Sealer) Seal(ctx context.Context, tx store.Tx, id tenant.ID, credentialID string, plaintext secret.Secret) (Sealed, error) {
	created, _, err := s.EnsureDataKey(ctx, tx, id)
	if err != nil {
		return Sealed{}, err
	}
	row, err := queries.New(tx).GetDataKey(ctx, dataKeyVersion)
	if err != nil {
		return Sealed{}, err
	}
	raw, err := s.p.Unwrap(ctx, row.KekVersion, row.WrappedKey, DataAAD(id, row.Version))
	if err != nil {
		return Sealed{}, fmt.Errorf("%w: the data key of the tenant does not unwrap: %w", ErrUnavailable, err)
	}
	defer clear(raw)
	dk, err := NewDataKey(raw, id, row.Version)
	if err != nil {
		return Sealed{}, err
	}
	plain := []byte(plaintext.Reveal())
	defer clear(plain)
	sealed, err := dk.Seal(credentialID, plain)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{
		KeyVersion:     row.Version,
		Secret:         sealed,
		Fingerprint:    dk.Fingerprint(plain),
		Hint:           Hint(plaintext.Reveal()),
		DataKeyCreated: created,
		KEKVersion:     row.KekVersion,
	}, nil
}

// String returns the redaction marker.
func (Sealed) String() string { return secret.Redacted }

// GoString returns the redaction marker.
func (Sealed) GoString() string { return secret.Redacted }

// Format writes the redaction marker for every verb.
func (Sealed) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, secret.Redacted) }

// LogValue returns the redaction marker as a log value.
func (Sealed) LogValue() slog.Value { return slog.StringValue(secret.Redacted) }

// MarshalJSON returns the redaction marker as a JSON string.
func (Sealed) MarshalJSON() ([]byte, error) { return []byte(`"` + secret.Redacted + `"`), nil }

// MarshalText returns the redaction marker.
func (Sealed) MarshalText() ([]byte, error) { return []byte(secret.Redacted), nil }
