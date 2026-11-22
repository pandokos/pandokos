package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/crypto/argon2"

	"github.com/pandokos/pandokos/internal/secret"
)

// Bounds of the parameters read from a stored hash and accepted for new ones (sprint 4.2, §5.4).
const (
	maxMemoryKiB   = 1048576
	maxIterations  = 10
	maxParallelism = 16
	saltLength     = 16
	keyLength      = 32
)

// Params are the Argon2id cost parameters of a new hash.
type Params struct {
	MemoryKiB, Iterations uint32
	Parallelism           uint8
}

// Hasher computes and checks Argon2id hashes, at most a fixed number at the same time.
type Hasher struct {
	params Params
	slots  chan struct{}
	count  atomic.Uint64
	dummy  string
}

// ErrBusy is returned when too many computations are running; the API answers 503.
var ErrBusy = errors.New("password: too many computations")

// New returns a Hasher that makes new hashes with p and runs at most maxConcurrent computations.
func New(p Params, maxConcurrent int) (*Hasher, error) {
	if maxConcurrent < 1 {
		return nil, errors.New("password: the limit of concurrent computations must be at least 1")
	}
	if p.MemoryKiB < 1 || p.MemoryKiB > maxMemoryKiB || p.Iterations < 1 || p.Iterations > maxIterations || p.Parallelism < 1 || p.Parallelism > maxParallelism {
		return nil, errors.New("password: parameters out of bounds")
	}
	h := &Hasher{params: p, slots: make(chan struct{}, maxConcurrent)}
	pw, err := Generate()
	if err != nil {
		return nil, err
	}
	// The dummy hash is made once, here, before any counter is read.
	h.dummy, err = h.Hash(context.Background(), pw)
	if err != nil {
		return nil, err
	}
	h.count.Store(0)
	return h, nil
}

// acquire takes a slot, or fails with ErrBusy when ctx ends first. A context that has already
// ended never takes a slot.
func (h *Hasher) acquire(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrBusy
	}
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrBusy
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns the PHC string of pw.
func (h *Hasher) Hash(ctx context.Context, pw secret.Secret) (phc string, err error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	h.count.Add(1)
	key := argon2.IDKey([]byte(pw.Reveal()), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, keyLength)
	return encode(h.params, salt, key), nil
}

// Verify reports whether pw matches phc, and whether phc was made with other parameters. A
// string that is not exactly the format is an error and computes nothing.
func (h *Hasher) Verify(ctx context.Context, phc string, pw secret.Secret) (ok, outdated bool, err error) {
	p, salt, want, err := parse(phc)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	h.count.Add(1)
	got := argon2.IDKey([]byte(pw.Reveal()), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want))) //nolint:gosec // len(want) is 32
	return subtle.ConstantTimeCompare(got, want) == 1, p != h.params, nil
}

// Computations returns the number of Argon2id computations the hasher has started: the counter
// that the tests of sprint 4.2 read to prove that a refusal computes nothing and that an answer
// for an unknown username costs what an answer for a known one costs.
func (h *Hasher) Computations() uint64 { return h.count.Load() }

// Dummy costs what Verify costs and returns no result: it answers for an unknown username.
func (h *Hasher) Dummy(ctx context.Context, pw secret.Secret) error {
	_, _, err := h.Verify(ctx, h.dummy, pw)
	return err
}
