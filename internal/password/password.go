package password

import (
	"context"
	"errors"

	"golang.org/x/crypto/argon2"

	"github.com/pandokos/pandokos/internal/secret"
)

// Part A of sprint 4.2 gives the package its names and no behaviour; step C1 writes the bodies.

// argon2 is the only package of golang.org/x/crypto that Pandokos imports (sprint 4.2, rule 6).
var _ = argon2.IDKey

var errNotImplemented = errors.New("password: not implemented")

// Params are the Argon2id cost parameters of a new hash.
type Params struct {
	MemoryKiB, Iterations uint32
	Parallelism           uint8
}

// Hasher computes and checks Argon2id hashes, at most a fixed number at the same time.
type Hasher struct{}

// ErrBusy is returned when too many computations are running; the API answers 503.
var ErrBusy = errors.New("password: too many computations")

// New returns a Hasher that makes new hashes with p and runs at most maxConcurrent computations.
func New(p Params, maxConcurrent int) (*Hasher, error) {
	_, _ = p, maxConcurrent
	return nil, errNotImplemented
}

// Hash returns the PHC string of pw.
func (h *Hasher) Hash(ctx context.Context, pw secret.Secret) (phc string, err error) {
	_, _ = ctx, pw
	return "", errNotImplemented
}

// Verify reports whether pw matches phc, and whether phc was made with other parameters.
func (h *Hasher) Verify(ctx context.Context, phc string, pw secret.Secret) (ok, outdated bool, err error) {
	_, _, _ = ctx, phc, pw
	return false, false, errNotImplemented
}

// Dummy costs what Verify costs and returns no result: it answers for an unknown username.
func (h *Hasher) Dummy(ctx context.Context, pw secret.Secret) error {
	_, _ = ctx, pw
	return errNotImplemented
}

// Generate returns a password of 24 characters of a-z2-7.
func Generate() (secret.Secret, error) { return secret.Secret{}, errNotImplemented }

// Check returns the rule that pw breaks, or "" when it is acceptable.
func Check(pw secret.Secret, username string, minLength int) (violation string) {
	_, _, _ = pw, username, minLength
	return "not implemented"
}
