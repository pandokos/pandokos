// Package transittest is an in-process double of the transit engine of OpenBao and HashiCorp
// Vault, for tests only: no production file imports it. It is real enough to be held to the
// conformance suite of sprint 12.1: AES-256-GCM with the request's associated_data as additional
// data, a key and an HMAC key per version, ciphertexts "vault:v<N>:<base64(nonce, ciphertext,
// tag)>", tokens with lifetimes, AppRole, namespaces and the statuses and bodies observed on the
// two services. Key types other than aes256-gcm96 and chacha20-poly1305 are encrypted with AES
// too but refuse associated_data, as the services do for a key that is not an AEAD.
//
// It sits under internal/vault because the repository allows AES there only.
package transittest
