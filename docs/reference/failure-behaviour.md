# Behaviour on failure

What Pandokos does when each thing it depends on fails (OPS-11): what the caller gets, what is kept or
lost, which log line and instrument show it, how it recovers, and **the test that holds it**. The rule is the
one of the [architecture overview](../architecture/overview.md#9-failure-behaviour): **when Pandokos cannot
verify that a request is allowed, or cannot open the credential, it does not forward it.** Failing closed is
the default; the exceptions are named and configured (`limits.on_store_failure`).

A repository test (T-R80) keeps this page true: every dependency and every job of the worker has a row, and
every test, log line, instrument and configuration key a row cites exists. Failure injection for every
dependency as a suite, and published figures, are sprint 10.1; the restore is sprint 12.3. Log lines are in
the [log reference](logs.md), instruments in the [metrics reference](metrics.md).

## 1. PostgreSQL

Through the pooler for every statement; one direct connection for change notifications
(`database.direct_address`, [database guide](../operations/database.md)).

| Part | The caller gets | Kept or lost | Log line | Instrument | Recovers | Test |
|---|---|---|---|---|---|---|
| PostgreSQL: inference | Calls continue from the caches (principal, credential, data key, catalogue, rules) until they expire, then `503 unavailable` with `Retry-After: 5`. Nothing is forwarded that could not be verified | Usage records wait in the bounded buffer of the ledger; when it is full new authenticated requests are refused 503 and are the only ones without a record | `ledger write failing`, `readiness check failed` | `pandokos.ledger.admission_refused`, `pandokos.ledger.buffer.usage` | When the database answers: the buffer drains, every admitted request has its row, `/readyz` is 200 again (`ledger write recovered`) | `TestLedgerDatabaseDown`, `TestDatabaseDown` |
| PostgreSQL: administration | `503 unavailable` for any operation that cannot read or write; a write that did not commit changed nothing | Nothing is half done: every change and its audit row are one transaction | `readiness check failed` | none | When the database answers; the caller retries | `TestReady` |
| PostgreSQL: notification connection | None: it carries invalidations only | The caches are not emptied on a loss: their lifetimes bound the staleness (`cache.principal_ttl`, `cache.credential_ttl`, `cache.data_key_ttl`) | `cache listener disconnected`, `cache invalidation unavailable` | none | The connection is retried; `cache listener connected` | `TestRevocationBoundWithoutNotifications` |
| PostgreSQL: schema not at the binary's version | `serve` does not start | Nothing | `startup failed` | none | `pandokos migrate`, then start | `TestServeRefusesUnmigratedDatabase` |
| PostgreSQL: limit counters and rules | A counter that cannot be loaded from the ledger: `503 unavailable` by default, or the request is admitted without the limits that could not be verified (`limits.on_store_failure: allow`); a loaded counter keeps counting | Nothing is recorded as spent that was not | `limit rules unavailable`, `limit counters unavailable` | `pandokos.limits.rule_loads` | When the ledger can be read again | `TestStoreFailure`, `TestLimiterFailures` |

## 2. The key file and its versions

The key-encryption key is a file read at start ([vault guide](../operations/vault.md)); the provider
`local` has no other state.

| Part | The caller gets | Kept or lost | Log line | Instrument | Recovers | Test |
|---|---|---|---|---|---|---|
| Key file: start | `serve` does not start when a version the database still needs (`active` or `retiring`) is missing from the configuration, or when the configured version was replaced; the message names the versions | Nothing | `startup failed` | none | Put the key of the active version in `vault.kek.local.key_file` and the others in `vault.kek.local.unwrap_key_files` | `TestActivateAtStart`, `TestCheck` |
| Key file: opening a credential | A call whose credential cannot be opened is not forwarded: `503 unavailable`; calls with a cached credential or data key continue until `cache.credential_ttl` and `cache.data_key_ttl` | Nothing; the credential is untouched | `credential cannot be opened` | none | Restore the key version; the next call opens it | `TestOpenerDB`, `TestOpenerCache` |
| Key file: sealing a credential | A write of a secret fails with `503 unavailable`; an instance whose key was replaced cannot create a data key (`cause: kek_replaced`) but still opens | Nothing is stored | `credential cannot be sealed` | none | Restart with the key of the active version | `TestFailSealLog`, `TestNoDataKeyForAClosedTenant` |
| Key file: lost | Every credential sealed under the lost version is unreadable | Everything but the credentials | `credential cannot be opened` | `pandokos.vault.kek.versions` | `recover-lost-kek.sql`, a new key, tenants enter credentials again ([vault guide](../operations/vault.md#5-what-its-loss-means)) | `TestLostKeyRecovery` |
| Key file: backup older than a rotation | A backup restored with only the current key refuses to start and opens no credential | The backup's credentials, once the old key is destroyed | `startup failed` | none | The old key as an unwrap key, or the recovery script ([backup guide](../operations/backup-and-restore.md)) | `TestBackupBeforeDeletionYieldsNothing` |

## 3. The identity provider

Token keys of a registered issuer, and the sign-in of people through OpenID Connect.

| Part | The caller gets | Kept or lost | Log line | Instrument | Recovers | Test |
|---|---|---|---|---|---|---|
| Identity provider: token keys | Tokens verify while the keys are cached, up to `tokens.jwks.max_stale` after the last fetch; a new process without keys answers `503 unavailable`, never `401`, and forwards nothing. Virtual keys are unaffected | Nothing | `issuer keys not fetched`, `issuer metadata not fetched` | `pandokos.jwks.fetches`, `pandokos.jwks.staleness` | The next successful fetch, within the refetch interval (`issuer keys fetched`) | `TestTokenIssuerOutage`, `TestOutage` |
| Identity provider: sign-in | Sign-in through the provider is unavailable; open sessions, local accounts and keys are unaffected | The sign-in transaction (minutes) | `sign-in through the identity provider unavailable`, `token endpoint not reached` | `pandokos.signin.attempts` | When the provider answers; people sign in again | `TestOidcExit` |

## 4. The OTLP collector

| Part | The caller gets | Kept or lost | Log line | Instrument | Recovers | Test |
|---|---|---|---|---|---|---|
| Collector: traces and metrics | Nothing: no request waits for the collector | Spans are dropped when the export queue is full; metrics are exported at the next interval | `trace export failing`, `metric export failing` | `pandokos.telemetry.export_failures`, `pandokos.telemetry.spans` | When the collector answers; the series resume | `TestCollectorNeverBlocks`, `TestCollectorAwayNeverBlocks` |

## 5. The provider and the price dataset

| Part | The caller gets | Kept or lost | Log line | Instrument | Recovers | Test |
|---|---|---|---|---|---|---|
| Provider | The provider's own error, unchanged; an unreachable provider, a refusal by the egress guard or a redirect is `502 upstream_unreachable` | The usage record of the request, with its outcome | `request`, `egress blocked` | `pandokos.provider.rate_limited`, `pandokos.egress.blocked` | The next request | `TestProviderOutage` |
| Price dataset source | Nothing: the process stays ready and the prices in force are kept; credential validation is separate | The dataset that was not imported | `price import failed` | none | The next refresh (`pricing.refresh.interval`); `pandokos import-prices` by hand | `TestPriceRefresh` |

## 6. The jobs of the worker

Each job runs in the worker role, per tenant in the tenant's own session; a failing tenant does not stop the
others; each is tried again at its next tick.

| Part | The caller gets | Kept or lost | Log line | Instrument | Recovers | Test |
|---|---|---|---|---|---|---|
| Ledger writer | No change for the caller while the buffer has room; then 503 (section 1) | A record is lost only at a forced shutdown, and counted | `ledger write failing`, `usage record not written`, `ledger closed with records lost` | `pandokos.ledger.records` | The writer retries; `ledger write recovered` | `TestLedgerAtShutdown`, `TestShutdownRecordsEverything` |
| Partitions | Nothing: a missing partition is a warning, and the default partition takes the rows | Nothing; rows in the default partition stay until retention | `usage partitions not ensured`, `usage records in the default partition` | none | The keeper tries again; `usage partitions created` | `TestServeContinuesWhenPartitionsFail`, `TestPartitioner` |
| Rollup | Nothing wrong: reports and limit counters read the exact way, slower | The rollup lags; it is derived and can be rebuilt | `usage rollup failing` | `pandokos.reports.rollup.lag`, `pandokos.reports.rollups` | The next pass that succeeds (`usage rollup recovered`) | `TestRollupTenantLoop`, `TestRollupAtomicAndConcurrent` |
| Retention | Nothing | Nothing is removed until the job succeeds; with both keys `0` nothing is ever removed | `retention failing` | `pandokos.retention.runs`, `pandokos.retention.removed` | The next run | `TestRetentionJob`, `TestRetentionEndToEnd` |
| Limit reconciler | Counters keep counting in memory; a counter that cannot be rebuilt is refused or admitted by `limits.on_store_failure` | Reservations that expire are counted | `limit reconciliation failed`, `limit reservation expired` | `pandokos.limits.reconciliations`, `pandokos.limits.cut_lag` | The next tick | `TestReconciler` |
| Event writer (limit events) | None | An event is dropped when its queue is full and counted; the limit itself is enforced | `limit event not recorded`, `limit events not recorded` | `pandokos.limits.events` | The next event | `TestEventWriter`, `TestEventWriterStats` |
| Deletion | The tenant stays suspended or `deleting`; nothing is half done | The step that failed changed nothing; the row says where the deletion is | `tenant deletion failing` | `pandokos.tenant_deletion.steps`, `pandokos.tenant_deletion.open` | The step is tried again every `tenant_deletion.interval`; `tenant deletion recovered` | `TestDeleterDatabaseOutage`, `TestDeleterCrashResume` |
| Re-wrap | Nothing: every credential keeps opening, the old version stays `retiring` and its key is still needed at start | A tenant whose data key does not unwrap keeps its old version; the others go on | `key rewrap failing` | `pandokos.vault.rewraps`, `pandokos.vault.kek.versions` | The next pass; `key rotation complete` when none is left | `TestRewrapFailureAndResume` |
| Price refresh | Nothing (section 5) | The prices in force are kept | `price import failed`, `price import skipped` | none | The next interval | `TestPriceRefresh` |
