# Epic 12 — Operability: record

Completed 2026-11-25 for v0.3.0 ([release page](../../releases/v0.3.0.md); the draft notes became the page), together with sprint 6.4 of
epic 6. Epic file as closed: [epic.md](epic.md).

## Outcome

**External key-encryption key (12.1).** With `vault.kek.provider: transit`, every tenant data key is wrapped and
unwrapped by the transit secrets engine of an OpenBao or HashiCorp Vault service; Pandokos never holds, reads or
stores the key-encryption key. The token it presents can encrypt, decrypt and describe that one key and nothing
else; its secrets (token, AppRole role ID and secret ID) are paths to files, and the service is reached through
the egress guard on a dedicated transport. Version identifiers are computed by the service from the key and
checked against its fingerprint, so a different key under the same name is refused. An installation moves from a
key file to the service, and back, by configuration and restarts: the new version becomes active, the worker
re-wraps every data key with the job of sprint 6.3, and the old version is retired; tenants do nothing. When the
service is slow or away, cached data keys keep serving until their lifetime ends and everything else is refused
with `503 unavailable` after a bounded wait; a process started during an outage listens on no port and starts by
itself when the service answers. Four metrics and seven log lines report the provider, and no token, secret ID or
key appears in any output ([external key guide](../../operations/external-key.md),
[vault](../../operations/vault.md), [behaviour on failure](../../reference/failure-behaviour.md)).

**Threat model review and fuzzing (12.2).** Every threat and trust boundary of the
[security model](../../architecture/security-model.md) has an identifier and names the code that answers it and
the test that proves it; a repository test fails when a named file or test no longer exists. The dated
[review record](../../architecture/threat-model-review-2026-11.md) lists 21 findings with their decisions: two
rows corrected, tests added to two controls, three threats added, a request member spelled in another case is
refused, the missing instance and pod credential addresses join the forbidden class of the egress guard,
and an exportable key of the key service is refused unless `vault.kek.transit.allow_exportable_key` allows
it. Fourteen new fuzz targets
join the twenty that existed, so every parser of network input has one; all run for a bounded time on every pull
request and longer every night. The supply-chain controls are stated as the pipeline produces them: the
published image is the scanned image, verification is by digest, vulnerabilities are looked for every night,
every pinned thing is under automated update and under a test, and the protection of the main branch and of
version tags is watched.

**Restore drill, upgrade policy, an instance by configuration alone (12.3).** The
[backup and restore guide](../../operations/backup-and-restore.md) is executed by a suite (`make test-drill`, CI
job `drill`): a logical dump and a physical backup with WAL restored into an empty server, the four cases of the
key with a key file and the restore of an OpenBao key service from its snapshot; after every restore both audit
chains verify against the heads kept outside, each tenant is served with its own credential, deletions are
replayed and destinations receive events signed with the secrets issued before the backup. The
[drill record](../../operations/restore-drill-2026-11.md) states, case by case, what came back, what cannot be
recovered and what each owner must enter again, with the time of each step, and the findings G-1 to G-10 and F-1
corrected in the guide and in the recovery script (which now disables event destinations). The
[upgrade guide](../../operations/upgrade.md) states the policy: from any patch of the previous minor, a patch
release without migration or breaking change, stop–migrate–start, going back by the backup, and the checks at
every release; the `upgrade` job upgrades a binary built from v0.2.0 under traffic and asserts what happened to
every request (1.0 s from stop to readiness, indicative). Every configuration key has a scope, `instance` or
`installation` ([configuration reference](../../operations/configuration.md)), and two instances started side by
side from two configurations share nothing but the binary. `pandokos export` and `apply` gain platform-level
token issuers with their tenants, tenant token issuers, subject mappings, limit rules on a mapping, event
destinations (their secrets written by `--secrets-out`) and the external reference
([declarative guide](../../operations/declarative.md)).

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 12.1 External key-encryption key | [sprint](12.1-external-key-encryption-key/sprint.md), [plan](12.1-external-key-encryption-key/plan.md) | [#76](https://github.com/pandokos/pandokos/pull/76), merged as f82638f | 3 rounds: round 1, 1 major and 5 minor; round 2, 1 minor; round 3, none. A product defect was found later by the parallel test suite: an instance not restarted refused, for up to 10 s, data keys that another instance had re-wrapped under a newer transit version; it now reads the key's description again, at most once a second, keeping the identification of the version (fixed in [#84](https://github.com/pandokos/pandokos/pull/84)) |
| 12.2 Threat model review and fuzzing | [sprint](12.2-threat-model-review-and-fuzzing/sprint.md), [plan](12.2-threat-model-review-and-fuzzing/plan.md) | [#77](https://github.com/pandokos/pandokos/pull/77), merged as add3c65 | 2 rounds: round 1, 6 minor; round 2, none |
| 12.3 Restore drill and upgrade policy | [sprint](12.3-restore-drill-and-upgrade-policy/sprint.md), [plan](12.3-restore-drill-and-upgrade-policy/plan.md) | [#85](https://github.com/pandokos/pandokos/pull/85) | 3 rounds: round 1, 2 major and 5 minor; round 2, 1 minor; round 3, none after the fix |

Each sprint followed the cycle of the earlier epics: a frozen design, tests committed failing before the
implementation, a review against the design repeated until it reported no findings, a pull request with green
CI. Open points raised by each design and the maintainer's decisions on them are at the end of section 12 of each
`sprint.md`.

Two pull requests between the sprints belong to the epic's record:
[#74](https://github.com/pandokos/pandokos/pull/74) accepted ADRs 0070 to 0072 of sprint 6.4, and
[#84](https://github.com/pandokos/pandokos/pull/84) made the test suite faster (from about 35 to about 10–15
minutes per CI job) with template databases and parallel tests; the timing faults that parallel running exposed
were fixed at their root, the vault defect above among them.

## Decisions recorded

ADR 0073 (the transit key provider: associated data, version identifiers, authentication, several providers and
behaviour on failure), sprint 12.1; ADR 0074 (threat model review and continuous fuzzing), sprint 12.2; ADR 0075
(upgrade policy and the tested upgrade), 0076 (the restore drill, its record, and recovery after a lost key) and
0077 (an instance defined by configuration alone), sprint 12.3 — all `proposed`, for the maintainer's review.
History lines were added to the ADRs each sprint touched: 0008, 0018 (amended by 12.2), 0019, 0033, 0037 (amended
by 12.2), 0038 (amended by 12.2), 0062, 0063, 0066, 0068, 0071 and 0072. See the [ADR index](../../adr/README.md).

## Carried forward

- **Every later sprint.** A threat or control added to the model comes with its code path and its test; a parser
  of network input with its fuzz target and its row; a new workflow, Compose file, Dockerfile or nested module
  pinned and with its dependabot entry (T-R89, T-R91, T-R93, T-R94). A migration is tested from the previous minor
  by the `upgrade` job; a change to a table the vault, the chains or the event log use, or to a step of the backup
  guide, fails T-R100 or the drill until the guide, the suite and the record agree. A new configuration key needs
  a scope; a new resource that distinguishes installations is in the declarative document or in its list of what
  never is.
- **8.1 (shared counters, replicas).** Rolling upgrades and a compatibility window for migrations (point 5 of the
  upgrade policy is reopened there and the traffic test becomes the rolling one); a start-time check that the
  instances of an installation agree on the `installation` settings; step 0 of the 12.1 rotation procedure for
  replicas; the drill gains the counter store; our decoding of counter values joins the fuzz targets.
- **8.2 (Kubernetes).** The chart carries the `vault.kek.transit.*` keys and `allow_exportable_key`, mounts the
  token or AppRole files and the client secret, gives pods a start-up allowance for the wait on the key service,
  sets `auth.public_origin` from the ingress host, exposes `support.*`, runs `migrate` and `init` as jobs and
  upgrades by stop–migrate–start; Kubernetes authentication to the key service; images in the chart pinned by
  digest; the scraper's credential decided before a `ServiceMonitor` ships (F-20).
- **10.1 (figures, failure injection).** Failure injection for the key service (section 2a of the failure
  reference) and during a restore (the drill's harness), latency with a remote key service, restore figures at
  scale (the drill takes its dataset from a variable); a script that moves the event sequences past a value kept
  outside after a restore (an issue, decision O-3 of 12.3).
- **10.2 (assessment and privacy).** The review record is the assessor's map; it inherits F-20 (the scraper's
  `platform-auditor` credential reads more than metrics), the pending-password gap, the decision on forbidding
  every link-local address for every class (O-3 of 12.2), and a fuzzing
  service with private reports (O-4 of 12.2). The assessment reads the drill record's "what was not drilled".
- **Epic 10 (release candidate).** `make test-drill` and `make test-upgrade` on the candidate, with a new dated
  record.
- **9.x (providers).** A new family declares its read set and gets the fuzz targets of its request parser, usage
  reader, stream reader and credential header, with their rows; nothing for the drill.
- **Release v0.3.0.** The notes say: a body with a member in another case is refused; an exportable key of the
  key service is refused unless allowed; verification is by digest; the restore is tested and where its record
  is; the upgrade from v0.2.x, its procedure, the measured interval and the single migration crossed; the recovery
  script disables destinations; the new sections of the declarative document and `--secrets-out`; the scope of
  keys. After the release, `test/upgrade/PREVIOUS` moves to v0.3.0, and the threat model's review for v0.3.0 is a
  new record that may name the drill's tests as proofs.
- **Issue #75.** Cloud key services (AWS KMS, Google Cloud KMS, Azure Key Vault) after v1.0: a further
  `KeyProvider` that derives its identifiers from the key and passes `vault.Check`; a direct move between two
  external providers needs a second configuration block.
- **Not planned or not drilled.** A platform webhook (6.4 O-5) needs a platform data key and is tracked as an
  issue; HashiCorp Vault's snapshot is not drilled (OpenBao is the tested reference); backup tooling (scheduling,
  retention, encryption of the backup store) is the operator's.

## Maintainer actions

- Review and accept ADRs 0073 to 0077 (done on 2026-11-26).
- Run `scheduled.yml` once by hand and check that its `settings` job can read the rulesets of the branch and tag
  protection.
- Make `fuzz` a required check after a week of green runs; consider making `transit`, `upgrade` and `drill`
  required checks too.
- O-2 (12.2): commit the maintainer's public signing key to the repository so that the release workflow can
  verify the signature of the tag (done on 2026-11-26: `.github/allowed_signers`, verified by `check-tag`).
- O-7 (12.3): the maintainer's own full run of the corrected guides in a lab, backup and restore included, after
  this epic and before tagging v0.3.0.
- Recreate the local test database (`make db-down && make db-up`) after #84, which changed it to use template
  databases.
- The container image tests (`make image && make test-container`) were not runnable in the sandbox of the
  implementation; CI runs them.
