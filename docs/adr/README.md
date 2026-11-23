# Architecture decision records

Each record states one decision, its context, its consequences and the alternatives that were
rejected. A decision is changed by a new record that supersedes the old one.

A record is *proposed* until the maintainer accepts it, by merging its pull request or after a review recorded in its History section; it is then *accepted*.

| No. | Decision | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | accepted |
| [0002](0002-implementation-language-go.md) | Implementation language: Go | accepted |
| [0003](0003-single-binary-with-roles.md) | One binary with three roles; separate planes | accepted |
| [0004](0004-postgresql-and-rls.md) | PostgreSQL as system of record, with forced row-level security | accepted |
| [0005](0005-data-plane-authentication.md) | Data-plane authentication: virtual keys and trusted token issuers | accepted |
| [0006](0006-human-authentication-external-oidc.md) | People sign in to the administration API only, through an external OIDC provider or a local account | accepted |
| [0007](0007-authorization-rbac.md) | Authorization: role-based, with a permission catalogue defined in code | accepted |
| [0008](0008-vault-envelope-encryption.md) | Vault: built-in envelope encryption with a pluggable key-encryption key | accepted |
| [0009](0009-native-passthrough-by-api-family.md) | Native passthrough organized by API family; no translation | accepted |
| [0010](0010-usage-extraction.md) | Usage extraction from responses and streams | accepted |
| [0011](0011-pricing-source.md) | Prices: versioned in the database, seeded from an open dataset | accepted |
| [0012](0012-limits-and-budget-reservation.md) | Limits: reserve and settle, with in-process or Valkey counters | accepted |
| [0013](0013-ledger-and-audit.md) | Usage ledger and audit log: append-only tables in PostgreSQL | accepted |
| [0014](0014-events-outbox.md) | Events for external systems: transactional outbox and CloudEvents webhooks | accepted |
| [0015](0015-observability.md) | Observability: OpenTelemetry, pinned GenAI conventions, no content | accepted |
| [0016](0016-cells-pool-silo-bridge.md) | Pool, silo and bridge as deployments of cells | accepted |
| [0017](0017-admin-api-spec-first.md) | Administration API: specification first, with a CLI | accepted |
| [0018](0018-supply-chain.md) | Supply chain: few dependencies, minimal image, signed releases | accepted |
| [0019](0019-configuration.md) | Configuration: static settings in a file, dynamic state in the database | accepted |
| [0020](0020-error-contract.md) | Error contract: the shape of the API being called | accepted |
| [0022](0022-local-accounts-and-sessions.md) | Local accounts and sessions | accepted |
| [0023](0023-test-dependencies-in-nested-modules.md) | Test-only dependencies live in nested modules | accepted |
| [0024](0024-openai-stream-usage-option.md) | OpenAI chat streams: how usage is requested and what is removed | accepted |
| [0025](0025-go-toolchain.md) | Go toolchain: language floor and release toolchain | accepted |
| [0026](0026-schema-migrations.md) | Schema migrations: in-house, forward-only, exact match at start | accepted |
| [0027](0027-database-roles-and-session-context.md) | Database roles, session context and pooler safety | accepted |
| [0028](0028-admin-api-generator.md) | Administration API: in-repository generator and conventions | accepted |
| [0029](0029-static-sql-sqlc.md) | Static SQL: `sqlc`, text-mode rules, and a test against built SQL | accepted |
| [0030](0030-keys-format-storage-lookup.md) | Keys: format, storage, lookup, platform keys, revocation bound | accepted |
| [0031](0031-authorization-core-and-provisioning.md) | Authorization core and tenant provisioning | accepted |
| [0032](0032-audit-log-first-version.md) | Audit log, first version | accepted |
| [0033](0033-vault-formats-and-key-identifiers.md) | Vault: formats, derived keys, key identifiers, caches | accepted |
| [0034](0034-credential-model-and-platform-oversight.md) | Credential model and platform oversight | accepted |
| [0035](0035-catalogue-and-model-access.md) | Catalogue and model access | accepted |
| [0036](0036-prices-costs-and-importer.md) | Prices, costs and the price importer | accepted |
| [0037](0037-request-pipeline-and-resolution.md) | Request pipeline and resolution | accepted |
| [0038](0038-egress-guard-upstream-tls-and-proxy.md) | Egress guard, upstream TLS and proxy | accepted |
| [0039](0039-usage-record-admission-and-ledger-writer.md) | Usage record, admission and the ledger writer | accepted |
| [0040](0040-traces.md) | Traces: SDK, span model, sampling and propagation | accepted |
| [0041](0041-anthropic-api-family.md) | The anthropic API family | accepted |
| [0042](0042-openai-compatible-providers-and-stream-usage-option.md) | OpenAI-compatible providers and the per-provider stream usage option | accepted |
| [0043](0043-tenant-capability-check-and-usage-list.md) | The tenant capability check and the minimal usage list | accepted |
| [0044](0044-sdk-compatibility-suite-python-typescript.md) | The SDK compatibility suite in Python and TypeScript | accepted |
| [0045](0045-trusted-token-issuers.md) | Trusted token issuers: registration, selection and validation | accepted |
| [0046](0046-issuer-key-retrieval-and-caching.md) | Retrieval and caching of issuer keys | accepted |
| [0047](0047-identity-providers-in-the-test-suite.md) | Identity providers in the test suite | accepted |
| [0048](0048-reserve-and-settle.md) | Reserve and settle in the pipeline: estimate, counters, guarantees and the 429 contract | accepted |
| [0049](0049-limit-rules.md) | Limit rules: levels, subjects and resolution | accepted |
| [0050](0050-quotas-and-budgets-periods-attribution-currencies.md) | Quotas and budgets: periods, attribution, currencies and unknown prices | accepted |
| [0051](0051-period-counters-and-the-ledger.md) | Period counters and the ledger: load, cut, reconciliation, the failing store | accepted |
| [0052](0052-administration-of-limits.md) | Administration of limits: scopes, thresholds, events, API and permissions | accepted |
| [0053](0053-tenant-price-lists-and-active-currency.md) | Tenant price lists and the active currency | accepted |
| [0054](0054-usage-data-is-the-tenants.md) | Usage data is the tenant's: what the platform does not see | accepted |
| [0055](0055-operational-telemetry-holds-no-tenant-data.md) | Operational telemetry holds no tenant data | accepted |
| [0056](0056-users-memberships-and-sessions-as-principals.md) | Users, memberships, and how a session becomes a principal | accepted |
| [0057](0057-local-accounts-and-sessions-formats-and-numbers.md) | Local accounts and sessions: formats and numbers | accepted |
| [0058](0058-openid-connect-relying-party.md) | The OpenID Connect relying party | accepted |
| [0059](0059-idempotency-keys.md) | Idempotency keys | accepted |
| [0060](0060-built-in-roles-custom-roles-and-the-rule-against-escalation.md) | Built-in roles, custom roles and the rule against escalation | accepted |
| [0061](0061-support-actions-and-reasons-how-the-platform-acts-inside-a-tenant.md) | Support actions and reasons: how the platform acts inside a tenant | accepted |
| [0062](0062-metrics-instruments-attributes-cardinality-and-scrape.md) | Metrics: instruments, attributes, cardinality and the scrape endpoint | proposed |
| [0063](0063-the-log-redaction-and-the-log-reference.md) | The log: redaction by key and by shape, and the log reference | proposed |
| [0064](0064-usage-rollup-reports-and-exports.md) | Usage rollup, reports and exports | proposed |
| [0065](0065-the-audit-hash-chain-reading-and-retention.md) | The audit hash chain, reading and retention | proposed |
| [0066](0066-declarative-export-and-apply.md) | Declarative export and apply | proposed |
| [0067](0067-tenant-deletion-stages-closed-tenants-and-kept-records.md) | Tenant deletion: stages, closed tenants and the records that are kept | proposed |
| [0068](0068-key-encryption-key-versions-rotation-and-erasure-in-backups.md) | Key-encryption key versions: activation, re-wrap, retirement, and erasure in backups | proposed |
| [0069](0069-tenant-data-export.md) | The tenant data export and its schema | proposed |

## Questions from SOW 0.1 and where they were decided

| Open question in SOW 0.1 (§23) | Decided in |
|---|---|
| 1. Language and framework | 0002 |
| 2. Database and row-level security strategy | 0004 |
| 3. Redis or another system for counters | 0012 |
| 4. Vault architecture and key management | 0008 |
| 5. Separation of control and data plane | 0003 |
| 6, 7. Service-account authentication, mTLS, JWT, SPIFFE, DPoP | 0005 |
| 8. Federated identity, tenants and roles | 0006, 0022, 0056, 0057, 0058 |
| 9. RBAC model and granularity | 0007, 0060, 0061 |
| 10. What the platform admin can do with credentials | 0007, 0008 |
| 11, 12. Provider APIs and degree of transparency | 0009 |
| 13. Token extraction, streaming | 0010 |
| 14. Price list source and versioning | 0011 |
| 15. Budget reservation under concurrency | 0012 |
| 16. Audit retention and immutability | 0013 |
| 17, 18. Events for external systems | 0014 |
| 19. Kubernetes topologies and silos | 0016 |
| 20. Service-level objectives | Targets in requirement GW-17; validated in epic 10 |
| 21. Backup, recovery, cryptographic erasure | 0008, data model; procedures in epics 6 and 10 |
| 22. Trust boundaries and threats | [Security model](../architecture/security-model.md) |
