# 0076. The restore drill, its record, and recovery after a lost key

- Status: proposed
- Date: 2026-11-25

**Amends** [ADR 0068](0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md) (the rule for a key
service's versions and the backups kept) and [ADR 0073](0073-the-transit-key-provider.md) (the service's backup is
its snapshot, and what must be kept with it). Builds on [ADR 0008](0008-vault-envelope-encryption.md). Requirements
OPS-10 and VLT-16; sprint 12.3.

## Context

The backup guide documented a procedure and said that a rehearsed and tested restore was a later sprint. A restore
that nobody has run is a hope. The drill found where the written procedure did not work (the check of the audit
heads, a database created without its grants, a reading of the key cases that could be taken either way) and one
thing the recovery script had not followed: since sprint 6.4 the signing secret of an event destination is sealed
under the same data keys as the credentials, and the script did not touch destinations.

## Decision

1. **The tested restore is an automated suite** that executes the guide step by step, with the guide's step
   identifiers held equal to the suite's by a repository test. (D-1)
2. **The drill owns its servers**, started per case in a Compose project of its own, never the shared test server;
   it is not in `make test`. (D-2)
3. **Both backup methods are drilled**: a superuser's `pg_dump` in custom format restored whole with `pg_restore`,
   and a base backup with archived WAL recovered to a named point and to the end. (D-3)
4. **The tools run in the database containers**, so the host needs Docker and Go. (D-4)
5. **A finding of the drill corrects the guide**, and the drill executes the corrected text; findings are listed in
   the record. (D-5)
6. **With a key service** the drill uses OpenBao with integrated storage and its snapshot, and a key that is not
   exportable. (D-6)
7. **The rule for a key service:** snapshot after every rotation and before Pandokos is restarted; never trim or
   raise `min_decryption_version` past a version that a kept database backup needs; keep the unseal shares and the
   means to authenticate with the snapshot. (D-7)
8. **`recover-lost-kek.sql` also disables the destinations** whose signing secret was sealed under the lost
   version, keeping their cursor and configuration, and drops an affected previous secret; the owner rotates the
   secret and enables the destination. It prints the three counts. (D-8)
9. **A dated record** states the cases, the proofs, the findings, what cannot be recovered and what must be
   entered again, the figures and what was not drilled; a new record when the procedure or the vault's tables
   change and on a release candidate. (D-9)

## Consequences

- The guide and the suite change together: a step added to one without the other fails a repository test.
- A drill needs Docker. It runs on every pull request while it stays under fifteen minutes; otherwise it moves to
  the nightly run and stays in the release's run.
- Destinations are part of the loss of a key: a tenant that loses a key re-enters its credentials and rotates its
  destinations' signing secrets.
- After a restore the log of events is reissued from the backup's head; a script that moves the sequences is
  decided from the record's evidence, as an issue for the scale work.

## Alternatives considered

- **A manual drill only:** true once. A manual walk by the maintainer stays, as the one check not made by the
  authors' own automation.
- **The shared test server:** it is not an empty environment and the drill destroys servers.
- **Deleting destinations in the script:** the URL, types and cursor are the owner's configuration.
- **The transit engine's key backup endpoint with an exportable key:** it needs the weak setting Pandokos refuses.

## History

- 2026-11-25: proposed with sprint 12.3.
