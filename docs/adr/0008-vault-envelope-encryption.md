# 0008. Vault: built-in envelope encryption with a pluggable key-encryption key

- Status: accepted
- Date: 2026-11-11

## Context

Provider credentials are the most valuable data in the system. Requirements: write-only access,
per-tenant keys, rotation without re-entry, cryptographic erasure, an optional external KMS, and
no mandatory dependency beyond PostgreSQL (VLT-01 to VLT-16).

## Decision

The vault is a package inside Pandokos, not a separate product.

**Key hierarchy.**

- A **key-encryption key (KEK)** lives outside the database, behind a `KeyProvider` interface with
  two operations: wrap and unwrap. Providers: a local 256-bit key read from a file (default); an
  external transit or KMS service, starting with the HashiCorp Vault / OpenBao transit API, which
  needs only HTTP. Cloud KMS providers follow on demand.
- Each tenant has one **data-encryption key (DEK)**, generated at tenant creation and stored
  wrapped by the KEK.
- Each credential is encrypted with the tenant DEK using AES-256-GCM with a random nonce. The
  associated data binds tenant ID, credential ID and key version, so a ciphertext moved to another
  row or tenant does not decrypt.

**Operations.**

- *Write*: the admin role encrypts and stores; the plaintext is never logged and never returned.
- *Use*: the gateway unwraps the DEK (cached for minutes), decrypts the credential (cached for at
  most a minute) and attaches it to the outbound request.
- *KEK rotation*: a new KEK version becomes active; the worker re-wraps every DEK; the old version
  stays available for decryption until the job completes. Credentials are untouched.
- *Erasure*: deleting the tenant's wrapped DEK makes all its credentials unreadable, including in
  backups taken before the deletion.
- *Fingerprint*: the last four characters and a keyed hash of the secret are stored so a tenant
  can recognize which key is registered without reading it.

Cryptography comes from the Go standard library only.

## Consequences

- No extra service to run in the default installation.
- With the local provider, the security of every credential rests on a file on the host. The
  documentation says how to protect and back it up, and that losing it means every tenant
  re-enters its keys.
- With an external provider, the KEK never reaches the Pandokos host; the provider becomes a
  dependency of the data plane, softened by the DEK cache.
- The admin role can decrypt in principle, because it holds a path to unwrap DEKs in order to
  encrypt. Removing that with public-key sealing is an open question in the architecture overview.
- A process memory dump of the gateway exposes the credentials in use. This limit is inherent and
  documented.

## Alternatives considered

**Delegating all secrets to an external secret manager.** Makes that product mandatory, and its
per-tenant path model would have to be mapped and kept consistent with Pandokos tenants.

**One key for all tenants.** Simple, but no per-tenant erasure and a single point of exposure.

**A key per credential.** No benefit over per-tenant keys for the stated threats, and more
objects to wrap and rotate.

**Database-native encryption (`pgcrypto`).** The key would travel in SQL statements and appear in
logs and statistics.

**A general cryptography framework with key-set management.** Adds dependencies for a narrow use
of one AEAD and one key hierarchy.
