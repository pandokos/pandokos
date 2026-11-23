package vault

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/store/queries"
	"github.com/pandokos/pandokos/internal/tenant"
)

// SecretKind names what a sealed named secret is for. It is the first part of the associated data
// of the secret, so that a sealed secret opens only as the kind it was sealed as.
type SecretKind string

// KindWebhookSecret is the signing secret of an event destination (sprint 6.4, ADR 0071).
const KindWebhookSecret SecretKind = "pandokos/webhook-secret/v1" //nolint:gosec // a kind name, not a credential

var (
	errNamedKind   = errors.New("vault: a named secret needs a kind")
	errNamedObject = errors.New("vault: the object of a named secret is not a canonical UUID")
)

// namedAAD returns the associated data of a named secret: the kind, 0x00, the tenant, the object
// and the version of the data key, the last as four big-endian bytes. A secret sealed for one
// kind, tenant, object or key version opens for no other, and never as a credential, whose
// associated data starts with another label.
func namedAAD(id tenant.ID, kind SecretKind, objectID string, version int32) ([]byte, error) {
	if kind == "" {
		return nil, errNamedKind
	}
	oid, err := tenant.ParseID(objectID)
	if err != nil {
		return nil, errNamedObject
	}
	b := make([]byte, 0, len(kind)+1+16+16+4)
	b = append(b, kind...)
	b = append(b, 0)
	b = append(b, id[:]...)
	b = append(b, oid[:]...)
	return binary.BigEndian.AppendUint32(b, uint32(version)), nil // #nosec G115 -- a bit pattern, fixed by the format
}

// SealNamed seals plaintext under the data key of tenant id as the secret of kind and objectID. The
// associated data is: kind, 0x00, the tenant (16 bytes), the object ID (16 bytes) and the version
// of the data key (4 bytes, big endian). tx is a session of the tenant. The sealed secret has
// neither fingerprint nor hint.
func (s *Sealer) SealNamed(ctx context.Context, tx store.Tx, id tenant.ID, kind SecretKind, objectID string, plaintext secret.Secret) (Sealed, error) {
	if _, err := namedAAD(id, kind, objectID, dataKeyVersion); err != nil {
		return Sealed{}, err
	}
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
		return Sealed{}, &SealError{
			Cause: sealCause(err), TenantID: id, KEKVersion: row.KekVersion, DataKeyVersion: row.Version,
			msg: "the data key of the tenant does not unwrap", err: err,
		}
	}
	defer clear(raw)
	dk, err := NewDataKey(raw, id, row.Version)
	if err != nil {
		return Sealed{}, err
	}
	plain := []byte(plaintext.Reveal())
	defer clear(plain)
	if len(plain) == 0 {
		return Sealed{}, errors.New("vault: nothing to seal")
	}
	aad, err := namedAAD(id, kind, objectID, row.Version)
	if err != nil {
		return Sealed{}, err
	}
	out := make([]byte, 1, 1+len(plain)+dk.enc.Overhead())
	out[0] = sealFormat
	out = dk.enc.Seal(out, nil, plain, aad)
	return Sealed{KeyVersion: row.Version, Secret: out, DataKeyCreated: created, KEKVersion: row.KekVersion}, nil
}

// OpenNamed opens a secret that SealNamed sealed, unwrapping the data key of keyVersion with p. The
// plaintext is the caller's to clear when it is done; the data key is cleared here. A data key or
// a key-encryption key that is out of reach is ErrUnavailable; a secret that does not open as the
// kind and object given is ErrUndecryptable.
func OpenNamed(ctx context.Context, tx store.Tx, p KeyProvider, id tenant.ID, kind SecretKind, objectID string, keyVersion int32, sealed []byte) (secret.Secret, error) {
	aad, err := namedAAD(id, kind, objectID, keyVersion)
	if err != nil {
		return secret.Secret{}, err
	}
	row, err := queries.New(tx).GetDataKey(ctx, keyVersion)
	switch {
	case errors.Is(err, store.ErrNoRows):
		return secret.Secret{}, fmt.Errorf("%w: the data key of the tenant is missing", ErrUndecryptable)
	case err != nil:
		return secret.Secret{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	raw, err := p.Unwrap(ctx, row.KekVersion, row.WrappedKey, DataAAD(id, row.Version))
	if err != nil {
		// A wrong key and a key out of reach are both unavailable for the caller: it retries later.
		return secret.Secret{}, fmt.Errorf("%w: the key-encryption key is not available", ErrUnavailable)
	}
	defer clear(raw)
	dk, err := NewDataKey(raw, id, row.Version)
	if err != nil {
		return secret.Secret{}, fmt.Errorf("%w: the data key is not usable", ErrUndecryptable)
	}
	if len(sealed) < 1 || sealed[0] != sealFormat {
		return secret.Secret{}, ErrUndecryptable
	}
	plain, err := dk.enc.Open(nil, nil, sealed[1:], aad)
	if err != nil {
		return secret.Secret{}, ErrUndecryptable
	}
	defer clear(plain)
	return secret.New(string(plain)), nil
}
