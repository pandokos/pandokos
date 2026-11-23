package vault

import (
	"context"
	"errors"

	"github.com/pandokos/pandokos/internal/secret"
	"github.com/pandokos/pandokos/internal/store"
	"github.com/pandokos/pandokos/internal/tenant"
)

// SecretKind names what a sealed named secret is for. It is the first part of the associated data
// of the secret, so that a sealed secret opens only as the kind it was sealed as.
type SecretKind string

// KindWebhookSecret is the signing secret of an event destination (sprint 6.4, ADR 0071).
const KindWebhookSecret SecretKind = "pandokos/webhook-secret/v1" //nolint:gosec // a kind name, not a credential

// errNamedNotBuilt is what the operations on named secrets answer until their part of the plan
// (6.4, step C1) is implemented.
var errNamedNotBuilt = errors.New("vault: named secrets are not implemented yet")

// SealNamed seals plaintext under the data key of tenant id as the secret of kind and objectID. The
// associated data is: kind, 0x00, the tenant (16 bytes), the object ID (16 bytes) and the version
// of the data key (4 bytes, big endian). tx is a session of the tenant.
func (s *Sealer) SealNamed(ctx context.Context, tx store.Tx, id tenant.ID, kind SecretKind, objectID string, plaintext secret.Secret) (Sealed, error) {
	_, _, _, _, _, _ = ctx, tx, id, kind, objectID, plaintext
	return Sealed{}, errNamedNotBuilt
}

// OpenNamed opens a secret that SealNamed sealed, unwrapping the data key with p. The plaintext
// and the unwrapped data key are the caller's to clear when it is done.
func OpenNamed(ctx context.Context, tx store.Tx, p KeyProvider, id tenant.ID, kind SecretKind, objectID string, keyVersion int32, sealed []byte) (secret.Secret, error) {
	_, _, _, _, _, _, _, _ = ctx, tx, p, id, kind, objectID, keyVersion, sealed
	return secret.Secret{}, errNamedNotBuilt
}
