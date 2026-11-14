package vault

import (
	"context"
	"errors"
)

// The errors of the key provider and of the envelope. Their texts are constant: none of them
// quotes a key, a secret or the content of a file.
var (
	// ErrKeyVersionUnknown means the provider holds no key for the version asked for.
	ErrKeyVersionUnknown = errors.New("vault: key-encryption key version unknown")
	// ErrUnwrap means a wrapped key could not be unwrapped: wrong associated data, damaged
	// bytes or an unknown format.
	ErrUnwrap = errors.New("vault: the wrapped key cannot be unwrapped")
	// ErrUndecryptable means a sealed secret could not be opened: a wrong tenant, credential
	// or key version, damaged bytes or an unknown format. It carries no detail on purpose.
	ErrUndecryptable = errors.New("vault: the sealed secret cannot be opened")
	// ErrInvalidKey means the content of a key file is not a valid key-encryption key.
	ErrInvalidKey = errors.New("vault: the key-encryption key is not valid")
	// ErrUnavailable means the key material needed is out of reach: the provider cannot serve
	// a version, or a data key does not unwrap. The administration API answers `unavailable`.
	ErrUnavailable = errors.New("vault: the key material is unavailable")
)

// KeyProvider wraps and unwraps data keys with a key-encryption key that it never reveals.
type KeyProvider interface {
	// Name identifies the provider ("local"); it is registered with every key version.
	Name() string
	// Wrap encrypts plaintext under the active key version and binds aad.
	Wrap(ctx context.Context, plaintext, aad []byte) (version string, wrapped []byte, err error)
	// Unwrap fails with ErrKeyVersionUnknown or ErrUnwrap; it never returns partial data.
	Unwrap(ctx context.Context, version string, wrapped, aad []byte) ([]byte, error)
	// Available reports whether Unwrap can serve that version now.
	Available(ctx context.Context, version string) error
}
