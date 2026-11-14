// Package vault keeps the secrets a tenant gives Pandokos: the provider credentials. It
// defines the key provider that wraps and unwraps data keys, the envelope that seals a
// credential under the data key of its tenant, the Sealer used by the administration plane,
// the Opener with its bounded caches used by the data plane, and the validator that presents a
// credential to its provider.
//
// The key-encryption key never reaches the database: a data key is stored wrapped and a
// credential is stored sealed, so a dump of the database holds no usable secret (ADR 0008). The
// package sits below both planes and imports neither of them; it is the only package that may
// import crypto/aes, crypto/cipher and crypto/hkdf (ADR 0033, enforced by the linter).
//
// A secret is never logged, never returned to a caller of the administration API and never
// part of an error: only its fingerprint and hint are shown.
package vault
