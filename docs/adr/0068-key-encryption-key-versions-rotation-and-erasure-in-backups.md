# 0068. Key-encryption key versions: activation, re-wrap, retirement, and erasure in backups

- Status: accepted
- Date: 2026-11-23

Refines [ADR 0008](0008-vault-envelope-encryption.md) and [ADR 0033](0033-vault-formats-and-key-identifiers.md);
**amends** ADR 0008 (the sentence "including in backups taken before the deletion" becomes points 5 and 6
below) and ADR 0033 point 5 (versions are registered by activation and have a state; `serve` needs the active
and the retiring ones). Requirements VLT-10, VLT-11; sprint 6.3.

## Context

ADR 0008 asks for rotation by re-wrapping data keys, with no action by tenants, and for erasure by destroying
the wrapped data key. It claims the erasure holds "including in backups taken before the deletion". That is not
true: a backup still holds the wrapped data key and the sealed secrets, and whoever holds it and a key file of the
version that wrapped the data key can open them. ADR 0033 registered a version when it first wrapped a data key,
which leaves no way to say which version is in use, which is being replaced, and which may be forgotten.

## Decision

1. **`kek_version` has a state**: `active`, `retiring` or `retired`, with `activated_at` and `retired_at`; a unique
   index allows one `active` row. A data key is created and re-wrapped under the active version only (a trigger on
   `tenant_data_key`), so an instance that still runs with a replaced key cannot create a data key (the write answers
   `503 unavailable`, cause `kek_replaced`) while it still opens existing credentials. (D-14)
2. **The active version is the key of `vault.kek.local.key_file`**, activated by `serve` at start through
   `kek_activate(version, provider)` (admin role, platform scope, audited as `kek.activate` with the actor `host`).
   A version the database does not know is registered as active and the previous active one becomes `retiring`; the
   active one is left as it is; a `retiring` or `retired` version is refused and `serve` does not start: a replaced
   key never comes back. Other keys the instance may unwrap with are listed in
   `vault.kek.local.unwrap_key_files` (up to 8). The admin role loses `INSERT` on `kek_version`; only `kek_activate`
   and `kek_retire` write it. (D-15)
3. **`vault.Rewrapper`** (worker) re-wraps every data key whose version is not the active one, in its tenant's
   session, one transaction per tenant: unwrap with the old version, wrap with the active one under the same
   associated data, unwrap again and compare, then update `kek_version` and `wrapped_key`. The plaintext key is cleared
   after the transaction. Credentials are never read or written: their associated data name the data key version, not
   the key-encryption key (ADR 0033 point 3). (D-16)
4. **Retirement is decided by a foreign key.** `kek_retire()` (worker) deletes each `retiring` version and writes it
   again as `retired` in a subtransaction; the foreign key of `tenant_data_key` sees the rows of every tenant whatever
   the policies and waits for an insert in flight, so a version is retired exactly when no data key is wrapped by it.
   A pass retires only after a clean pass; each retirement is audited (`kek.retire`) and logged
   (`key rotation complete`). `serve` retires what can be retired before it checks, so an unused old version never
   blocks a start. (D-16)
5. **Erasure is immediate in the database.** At the commit of the step `secrets` of a tenant deletion
   ([ADR 0067](0067-tenant-deletion-stages-closed-tenants-and-kept-records.md)) the tenant's wrapped data keys and
   sealed secrets no longer exist; gateways forget the data key within `cache.data_key_ttl` plus `cache.credential_ttl`
   at most. (D-17)
6. **Erasure in backups is complete when the key versions recorded with the deletion are retired and their files
   destroyed.** A backup taken before still holds the wrapped data key; only two things change that: the backup
   expires, or the version is retired and every copy of its file destroyed. The deletion records the versions;
   `getTenantDeletion` shows `backup_erasure` (`pending` or `complete`), `listKEKVersions` shows `erasures_pending` per
   version and `pandokos.vault.erasures.pending` counts them. "Retired" is a fact of the database; destroying the file
   is the operator's act, which the product cannot see. The rule for operators: after a deletion, rotate within the time
   erasure is promised in, wait for `key rotation complete`, take a new backup, destroy the old file. The price: backups
   older than the rotation open no credential once the old key is destroyed. (D-17)
7. **A rotation is configuration and a restart**, not an API operation: the key file is configuration, and an operation
   could only point at a key the instance already holds. With several instances, every instance first gets the new key
   as an unwrap key; then it becomes `key_file`. (O-7)
8. **ADR 0008 and VLT-11 are reworded**: deleting a tenant destroys its data key, so residual ciphertext can no longer
   be decrypted, in the database at once and in backups once the version that wrapped the data key is retired.

## Consequences

- A rotation needs a new key file, two restarts at most, and a wait for `key rotation complete`; tenants do nothing.
- A replaced key cannot come back, so a restore from a backup older than a rotation starts with the current key as
  `key_file` and the old one as an unwrap key, or with the recovery script
  ([backup and restore guide](../operations/backup-and-restore.md)).
- An operator who keeps the old key to keep old backups whole keeps the deleted tenants' credentials recoverable from
  them; the guides make the choice explicit.
- There is no automatic rotation after a deletion or on a schedule: it needs a key file nobody made. A key held by an
  external service (sprint 12.1) can automate it; `KeyProvider` keeps its four methods and a transit provider maps its
  versions onto `kek_version` and `kek_activate`.
- `serve` writes the platform's log at start (`kek.activate`) and its start can fail for a replaced key.

## Alternatives considered

**An API operation that rotates.** It could only point at a key the instance already holds; the key file is the
act (O-7).

**Operator-chosen version labels** (ADR 0033 rejected them; kept rejected): a label can be attached to the wrong file.

**Counting data keys across tenants, with a policy for the owner**, to decide retirement. The foreign key sees every
row whatever the policies and needs none.

**Rotating by itself at each deletion.** It needs a key file nobody made; the product would have to write a secret to
disk.

**Keeping ADR 0008's claim.** It is false for any backup older than the deletion.

## History

- 2026-11-23: proposed with sprint 6.3.
- 2026-11-23: accepted by the maintainer after review.
- 2026-11-24: sprint 12.1 answers the consequence about automation: a key service makes it possible and the product does not do it; for a key service "its file destroyed" reads "made undecryptable and trimmed in the service" ([ADR 0073](0073-the-transit-key-provider.md)).
