# 0034. Credential model and platform oversight

- Status: proposed
- Date: 2026-11-13

Refines [ADR 0007](0007-authorization-rbac.md), [ADR 0008](0008-vault-envelope-encryption.md)
and [ADR 0027](0027-database-roles-and-session-context.md).

## Context

VLT-01 to VLT-06 and AZ-08 ask for labelled credentials with one default per provider,
write-only secrets, decryption by the data plane only, and a platform administrator who sees
metadata and can disable or revoke but never read or replace.

## Decision

1. A credential is `active`, `disabled` or `revoked`. Revocation is final and destroys the
   sealed secret in the same statement; the metadata stays until the credential is deleted.
   The database enforces the lifecycle by trigger.
2. The first credential of a provider is its default; the default moves only on request. A
   virtual key can be pinned to one credential when it is created. A pinned key uses its pin
   for that credential's provider and the default for any other; nothing ever falls back to
   another credential.
3. The admin database role cannot read the sealed secret in any session: it holds no `SELECT`
   on that column. It writes sealed secrets and reads metadata, through the view
   `provider_credential_metadata` (`security_invoker`). Only the gateway role reads sealed
   secrets, in the tenant's session. Vault tables carry the canonical tenant policy and
   nothing else: a transaction in platform scope sees none of their rows.
4. A platform administrator acts on a named tenant through four operations with the platform
   permissions `tenant_credential:read` and `tenant_credential:revoke`; they run in that
   tenant's session, require a reason for a change and write the audit row in the tenant's
   log. No platform operation accepts a secret.
5. Tenant permissions are `credential:read`, `write`, `rotate`, `delete` and `use`; the vault
   checks `credential:use` itself when asked to open a credential.
6. Until the catalogue exists a credential names its provider by an identifier from a list
   in code; validation at write time exists for the provider whose endpoint the installation
   knows, is asked per request and stores nothing unless the provider accepts.

## Consequences

AZ-08 holds at the database, not only in handlers. A platform administrator cannot search
all tenants for a fingerprint. In the single process of v0.1 both database users are in one
memory space; the separation becomes a deployment property when roles run apart.

## Alternatives considered

A platform-scope policy on the credential table with column privileges (a second policy on a
tenant table, and a platform session that sees every tenant's metadata at once); platform
principals holding tenant permissions (contradicts ADR 0031); deleting instead of revoking
(loses the fingerprint that identifies what was revoked); a pin table per provider.
