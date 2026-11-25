# 0075. Upgrade policy and the tested upgrade

- Status: proposed
- Date: 2026-11-25

**Amends** [ADR 0026](0026-schema-migrations.md) ("Consequences": the policy now exists; the window for rolling
upgrades still does not). Builds on [ADR 0074](0074-threat-model-review-and-continuous-fuzzing.md). Requirement
OPS-13; sprint 12.3.

## Context

OPS-13 asks that an upgrade is possible without losing a request and that going back is possible. ADR 0026 made
migrations forward-only and the start check exact, which makes an upgrade "stop, migrate, start" and a mistake a
restore, but nothing said which upgrades are supported, nothing tested one, and the interval without service was
not known. "Upgrades work" was a sentence.

## Decision

1. **Versions (point 1 of the guide).** Semantic versioning; before 1.0 a minor may break compatibility and only
   the latest minor is supported. Every release page has "Breaking changes" and "Upgrade notes".
2. **The supported upgrade is from any patch of the previous minor** (v0.2.x to v0.3.y), tested on every pull
   request from the newest patch of the previous minor. From an older minor, through each minor in turn. (D-10)
3. **A patch release has no migration**, removes or renames no key, changes no default that changes behaviour and
   has no breaking change: every patch of a minor has one schema. (D-10)
4. **The procedure is stop, migrate, start**, preceded by a backup and `config check` of the new binary on the
   current configuration, followed by a check; the interval without service is measured and stated. Rolling
   upgrades are decided with epic 8. (D-11)
5. **No two versions at once.** ADR 0026 stands.
6. **Going back from a minor is a restore** of the backup taken before `migrate`, run by the old binary. It is
   tested.
7. **Formats that leave the installation** are versioned by themselves and grow by addition within their version.
8. **PostgreSQL 16 or later**; a later major is the database's procedure and the chains stay verifiable.
9. **At every release** the `upgrade` and `drill` jobs are green, the threat model is reviewed with a record file,
   and the release page links that record and the drill record in force. (D-19)
10. **The previous release in the tests is pinned** in `test/upgrade/PREVIOUS` (version, commit, image digest): a
    binary built from the commit runs the traffic test and the released image runs once in the `image` job; the
    file moves after every release. (D-12)

## Consequences

- A patch release cannot carry a migration; a change that needs one waits for a minor.
- The pin moves at every release, in the first pull request after it; a pin under a test cannot go stale silently
  because a repository test checks its form and that it is the newest patch of the previous minor.
- Every later migration is tested from the previous minor without being asked.
- Rolling upgrades stay out until a compatibility rule for migrations exists (epic 8).

## Alternatives considered

- **Testing against the released image only:** the harness would have to drive containers for the traffic test;
  the binary built from the tag's commit is the cheaper way to drive the same code.
- **Supporting any-to-any upgrades:** the breaking changes and start checks of the versions in between would
  need to be tested together; one minor at a time is the tested path.
- **Down migrations:** ADR 0026 rejected them; going back is a restore.

## History

- 2026-11-25: proposed with sprint 12.3.
