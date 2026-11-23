# Log reference

Every message Pandokos writes to standard output, with its level and its fields.

The log is JSON, one object per line, with the fields `time`, `level` and `msg` and then the fields of the message. Every logger of the product is built on one handler ([ADR 0063](../adr/0063-the-log-redaction-and-the-log-reference.md)) that replaces the value of a sensitive key (a password, a token, a key, a secret, an authorization header) with `[REDACTED]` and, in the message and in every value, anything that has the shape of a Pandokos secret: a virtual or platform key, a session token, a bearer value, a compact JWS. Each redaction by shape is counted by the metric `pandokos.log.redactions`; a value above zero means a secret reached a logger by mistake, and is a bug to report.

What a line may say about a tenant is decided by [ADR 0055](../adr/0055-operational-telemetry-holds-no-tenant-data.md): no tenant, caller, key, model, credential, size, usage, cost or limit rule. The only identifiers that appear are `request_id` and `trace_id`, which name a request and not its owner, and the identities of platform principals in the two lines marked below: `key_id` in `bootstrap key active` and `principal_id` and `key_id` in `admin request`.

This page is a contract checked by tests: a message or a field that is not listed here fails them, and so does a tenant identifier in the output of a process of the end-to-end suite. Adding a log line means adding a row here in the same change. Each row below gives the level (or levels) of the message, its fields, and when it is written and what to do about it. The rows are grouped by the component that writes them.

## Fields

| Field | Type | Meaning |
|---|---|---|
| `Attributes` | object | The attributes of an OpenTelemetry meter scope. |
| `Name` | string | The name of an OpenTelemetry meter scope. |
| `Readers` | array | The readers of an OpenTelemetry meter provider, as the SDK reports them. |
| `Resource` | array | The resource of an OpenTelemetry meter provider: the attributes of the process, no value of a secret. |
| `SchemaURL` | string | The schema URL of an OpenTelemetry meter scope. |
| `Version` | string | The version of an OpenTelemetry meter scope. |
| `Views` | array | The views of an OpenTelemetry meter provider, as the SDK reports them. |
| `address` | string | The address a listener is bound to, or an address the egress guard refused. |
| `attributes` | object | The attributes of an OpenTelemetry scope. |
| `audit_events` | integer | Audit events of the tenants removed by a retention run, in total over the installation. |
| `audit_months` | integer | The configured retention of the audit logs, in months; 0 keeps them. |
| `audited` | boolean | Whether the failure was recorded in the audit log. |
| `cache` | string | The cache that was flushed. |
| `cause` | string | The class of a failure of the vault. |
| `check` | string | The name of a readiness check. |
| `class` | string | The class of a failed fetch. |
| `commit` | string | The build commit. |
| `config` | object | The configuration of an OpenTelemetry provider, as the SDK reports it; no value of a secret. |
| `constraint` | string | The name of the database constraint a record broke. |
| `count` | integer | The number of occurrences since the last line. |
| `created` | integer | Rows created. |
| `data_key_version` | integer | The version of a data key. |
| `detail` | string | A short detail of the failure. |
| `digest` | string | The digest of the price dataset. |
| `direct` | boolean | Whether the listener connects around the pooler. |
| `drain_timeout_ms` | integer | The time given to open requests to finish, in milliseconds. |
| `duration` | string | How long a fetch took. |
| `duration_ms` | integer | Milliseconds from the start of the work to its end. |
| `egress_reason` | string | The reason the egress guard refused the call. |
| `endpoint` | string | The name of the endpoint of the provider the request was sent to. |
| `error` | string | The text of an error that holds nothing of a tenant or a secret. |
| `error_class` | string | The class of a failure: a short word, never the error text. |
| `error_code` | string | The Pandokos error code of the answer. |
| `expires_at` | string | A time in RFC 3339. |
| `failures` | integer | Failures counted since the last line. |
| `forced` | boolean | Whether the shutdown had to cut open requests short. |
| `go_version` | string | The Go version of the build. |
| `hint` | string | What to do. |
| `import_id` | string | The ID of a price import. |
| `in_use` | integer | Slots of the ledger buffer in use. |
| `invalid_reason` | string | Why the request was invalid, as a closed word. |
| `issuer` | string | The issuer URL of a token issuer, in the lines of the key fetch. |
| `kek_provider` | string | The key provider of the vault. |
| `kek_version` | string | The version of a key-encryption key. |
| `key` | string | The name of a configuration key. |
| `key_id` | string | The ID of a platform key. Only on the lines marked as platform identities. |
| `keys` | integer | The number of keys fetched. |
| `kind` | string | The kind of a limit event. |
| `ledger` | string | full when the usage ledger had no room. |
| `level` | string | DEBUG, INFO, WARN or ERROR. |
| `limit_state` | string | How the limits treated the request: not_evaluated, unlimited, limited or unverified. |
| `limits_unverified` | boolean | True when a matching limit could not be evaluated. |
| `listener` | string | The listener: inference or admin. |
| `method` | string | The HTTP method, or OTHER outside the usual set. |
| `missing` | integer | The number of records missing at the end of the ledger. |
| `msg` | string | The message. |
| `name` | string | The name of an OpenTelemetry scope. |
| `on_store_failure` | string | The setting limits.on_store_failure. |
| `on_unknown_price` | string | The setting limits.on_unknown_price. |
| `operation` | string | The operation of the administration API, or of the gateway route. |
| `outcome` | string | How the request ended: ok, pandokos_error, client_error, client_cancelled, shutdown_aborted, timeout or upstream_aborted. |
| `overridden` | integer | Rows overridden. |
| `panic_type` | string | The Go type of a recovered panic, never its value. |
| `partitions` | integer | Usage partitions dropped by a retention run, in total over the installation. |
| `platform_audit_events` | integer | Rows of the platform audit log removed by a retention run. |
| `principal_id` | string | The ID of a platform principal. Only on the lines marked as platform identities. |
| `principal_type` | string | The kind of platform principal: platform or user. |
| `problem` | string | What is wrong with the value of a configuration key; never the value. |
| `provider` | string | The provider named by the route. |
| `queued` | integer | Records waiting in the ledger buffer. |
| `reason` | string | A closed word that says why. |
| `request_id` | string | The request ID of the request the line is about (also the X-Pandokos-Request-Id header). |
| `role` | string | The database role of the pool. |
| `route` | string | The route of the request, as a template; "unmatched" when no route matched. |
| `rule` | string | The text of a group rule that was skipped. |
| `schemaURL` | string | The schema URL of an OpenTelemetry scope. |
| `schema_version` | integer | The number of migrations applied. |
| `shape` | string | The shape of a claim. |
| `skipped` | integer | Entries skipped. |
| `source` | string | The source of a price import: embedded or remote. |
| `sqlstate` | string | The five-character SQLSTATE of a database error. |
| `stack` | string | The stack of a recovered panic. |
| `status` | integer | The HTTP status written; 0 when none was. |
| `stream` | boolean | Whether the request asked for a stream. |
| `support` | boolean | True when the request ran under a support action. |
| `target` | string | What a purge round was purging. |
| `time` | string | The time of the line, UTC, RFC 3339. |
| `time_zone` | string | The time zone of the limits. |
| `tls` | boolean | Whether the listener serves TLS. |
| `trace_id` | string | The trace ID of the request, when it has one. |
| `ttfb_ms` | integer | Milliseconds from the call to the first byte of the answer. |
| `unchanged` | integer | Rows left as they were. |
| `unmatched` | integer | Entries that match no model. |
| `unwritten` | integer | The number of records or events that were not written. |
| `upstream_error` | string | The class of a failure of the call to the provider. |
| `upstream_status` | integer | The status the provider answered with. |
| `usage_injected` | boolean | Whether Pandokos asked the provider for usage in the stream. |
| `usage_months` | integer | The configured retention of the usage records, in months; 0 keeps them. |
| `usage_reader` | string | failed when the reader of the answer gave up. |
| `usage_records` | integer | Usage records removed by a retention run, in total over the installation. |
| `version` | string | The build version. |
| `warning` | string | The text of a configuration warning; never a value. |

## Telemetry

### `Tracer created`

Level: debug
Fields: `name`, `version`, `schemaURL`, `attributes`

The OpenTelemetry SDK created the tracer of the process, once at start-up when trace export is on; it is a diagnostic of the SDK and needs no action.

### `TracerProvider created`

Level: debug
Fields: `config`

The SDK built the trace provider with the configuration shown, which holds no secret; once at start-up when trace export is on.

### `Meter created`

Level: debug
Fields: `Name`, `Version`, `SchemaURL`, `Attributes`

The SDK created a meter of the metrics provider; once at start-up when metrics are on. Needs no action.

### `MeterProvider created`

Level: debug
Fields: `Resource`, `Readers`, `Views`

The SDK built the meter provider with the resource, the readers and the views shown; once at start-up when metrics are on. Needs no action.

### `metric export failing`

Level: warn
Fields: `count`, `error_class`

An export of metrics to the collector failed; written at most once a minute with the number of failures since the last line and their class (`timeout`, `status`, `connection` or `other`). Check that the collector is up and that `telemetry.otlp.endpoint` is right; requests are not affected.

### `telemetry error`

Level: warn
Fields: `count`, `error_class`

The OpenTelemetry SDK reported an error that belongs to no single signal, at most once a minute with a count and a class; the error text is never logged. Check the collector and the telemetry settings.

### `trace export failing`

Level: warn
Fields: `count`, `error_class`

An export of spans to the collector failed; written at most once a minute with the number of failures and their class. Check that the collector is up; spans are dropped meanwhile and requests are not delayed.

## Admin

### `admin request`

Level: info, debug
Fields: `request_id`, `method`, `operation`, `status`, `duration_ms`, `error_code`, `principal_type`, `support`, `principal_id`, `key_id`
Platform identities: `key_id`, `principal_id`

One line for each request of the administration listener: at info for the API, at debug for the health and metrics paths. The platform identities are those of the caller, which is a platform principal; a tenant principal never appears.

### `authentication failed`

Level: warn
Fields: `listener`, `request_id`, `reason`, `audited`

A request to the administration listener presented a credential that did not authenticate; `reason` is a closed word. Many in a row mean a client with a wrong or expired credential, or a guessing attempt.

### `authentication failure not recorded`

Level: debug
Fields: `request_id`

The failure of an authentication could not be written to the audit log; the request was refused as usual. At debug.

### `credential cannot be sealed`

Level: error
Fields: `request_id`, `operation`, `cause`, `data_key_version`, `kek_version`

The vault could not encrypt a provider credential that an administrator submitted, so the credential was not stored; `cause` says whether the key provider or the key was the problem. Check the vault and the key-encryption key.

### `credential validation`

Level: info
Fields: `request_id`, `provider`, `outcome`, `upstream_status`, `duration_ms`

A provider credential was checked against its provider when it was created or changed; `outcome` says whether the provider accepted it, rejected it or could not be reached.

### `grant refused`

Level: warn
Fields: `request_id`, `operation`

The authorization refused a request to give a role or permission that the caller does not hold itself; one line per refused request.

### `group rule skipped`

Level: warn
Fields: `rule`

A group-to-role rule of a token issuer or of the identity provider did not apply and was skipped; `rule` is its position in the list. Fix the rule in the configuration of the issuer.

### `idempotency response not stored`

Level: warn
Fields: `request_id`, `operation`, `status`

The response of an idempotent request could not be stored, so a retry with the same key will run the operation again instead of replaying it.

### `internal error`

Level: error
Fields: `request_id`, `operation`, `sqlstate`, `error`

A request failed with an error Pandokos did not expect and was answered `internal_error`; the line has the request ID, the operation, the SQLSTATE of a database error and the error text. Report it if it repeats.

### `panic recovered`

Level: error
Fields: `request_id`, `panic_type`, `stack`

A handler panicked and the request was answered with an error; the line holds the type of the panic value and the stack, never the value. This is a bug: report it.

### `price import failed`

Level: warn
Fields: `source`, `error_class`, `duration_ms`

A scheduled or manual import of prices failed; `error_class` is a short word and no error text is written. The prices in the catalogue are unchanged.

### `price import failure not recorded`

Level: warn
Fields: `source`

A price import failed and the failure could not be recorded in the import history either, usually because the database was away.

### `prices imported`

Level: info
Fields: `source`, `import_id`, `digest`, `created`, `unchanged`, `overridden`, `unmatched`, `skipped`, `duration_ms`

A price import finished; the line counts the rows created, unchanged and overridden and holds the digest of the dataset.

### `session use not recorded`

Level: debug
Fields: `request_id`, `error`

The time of last use of a sign-in session could not be written; the session is still valid and the request went on.

### `sign-in through the identity provider unavailable`

Level: warn
Fields: `request_id`

A sign-in through the OpenID Connect provider could not finish because the identity provider or the database was away; the user can try again later.

### `throttle row not removed after a password reset`

Level: warn
Fields: `request_id`

A password was reset but the record of failed sign-ins of that user could not be removed; any lock ends by itself.

### `export aborted`

Level: warn
Fields: `request_id`, `error_class`, `duration_ms`

A usage or audit export failed after its first byte: the connection was closed so that the client sees an incomplete file, and the metric `pandokos.exports` counts it as aborted. A failure before the first byte is an ordinary error answer and writes no such line.

## Serve

### `administrator check failed`

Level: debug
Fields: `error`

At start-up the check for a user with a platform role could not run; nothing is changed by it.

### `bootstrap key active`

Level: warn
Fields: `key_id`, `expires_at`
Platform identities: `key_id`

A bootstrap key still exists and has not expired; written at start-up and then every hour, with the ID and the expiry of the key. Replace it with a platform key and let it expire or revoke it.

### `bootstrap key check failed`

Level: debug
Fields: `error`

The check for active bootstrap keys could not run; it is tried again an hour later.

### `configuration error`

Level: error
Fields: `key`, `problem`

A configuration key has a value that the server refuses; the line names the key and what is wrong with it, never the value. The server does not start.

### `configuration warning`

Level: warn
Fields: `key`, `warning`

A configuration is accepted but probably not what was meant; the line names the key and the warning, never a value. Read the warning and decide.

### `database ready`

Level: info
Fields: `role`, `schema_version`

The server connected to the database with the role named and found the schema version shown.

### `ledger flushed`

Level: info
Fields: `unwritten`

At shutdown the usage ledger wrote what it could; `unwritten` is the number of records that could not be written in time.

### `limit events not recorded`

Level: warn
Fields: `unwritten`

At shutdown some limit events had not been written to the database and are lost; the count is in `unwritten`.

### `limits ready`

Level: info
Fields: `time_zone`, `on_unknown_price`, `on_store_failure`

The limits subsystem started with the time zone shown and the settings of its failure policies.

### `no user with a platform role exists`

Level: warn
Fields: `hint`

No user can administer the platform; the hint says how to create the first administrator.

### `purge failed`

Level: warn
Fields: `target`, `sqlstate`

A purge round of expired data (`target`) failed; it runs again at the next round. The SQLSTATE is shown when the database gave one.

### `session count failed`

Level: warn
Fields: `sqlstate`

The count of open sessions, read for the metric `pandokos.sessions.open`, failed; the metric keeps its last value.

### `starting`

Level: info
Fields: `version`, `commit`, `go_version`

The server began to start, with its version, commit and Go version.

### `startup failed`

Level: error
Fields: `error`

The server could not start; the text of the error holds nothing of a secret. The process exits with a failure.

### `vault ready`

Level: info
Fields: `kek_provider`, `kek_version`

The vault is ready with the key provider and the version of the key-encryption key shown.

## Store

### `cache invalidation unavailable`

Level: warn
Fields: `role`, `detail`

The database cannot deliver change notifications (a pooler in transaction mode without a direct connection, for example), so caches rely on their expiry; written once. See the database guide.

### `cache listener connected`

Level: info
Fields: `role`, `direct`

The connection that receives change notifications from the database is up; `direct` says whether it goes around the pooler.

### `cache listener disconnected`

Level: warn
Fields: `role`, `error`

The connection that receives change notifications was lost or could not be made; it is retried, and caches expire by time meanwhile.

## Catalog

### `catalog cache flushed`

Level: debug
Fields: `reason`

The catalogue cache was emptied, because of a change notification or because it was full; at debug.

### `price import skipped`

Level: info
Fields: `source`, `reason`

A price import did not run because the data was recent or unchanged; `reason` says which.

## Vault

### `credential cannot be opened`

Level: error
Fields: `request_id`, `data_key_version`, `kek_version`, `cause`

A stored provider credential could not be decrypted for a request, so the request failed; `cause` and the versions say whether the key provider or the key is at fault. Check the vault.

### `vault cache flushed`

Level: debug
Fields: `reason`

The cache of opened credentials was emptied, on a change or because it was full; at debug.

## Gateway

### `egress blocked`

Level: warn
Fields: `request_id`, `provider`, `endpoint`, `reason`, `address`

The egress guard refused a connection to the address of an endpoint, for the reason shown; the request was answered with an error. Check the endpoint of the provider.

### `limit settle failed`

Level: error
Fields: `request_id`, `panic_type`, `error`

The limits could not settle the reservation of a finished request; the reservation expires by itself. Report it if it repeats.

### `metrics observation failed`

Level: error
Fields: `request_id`, `panic_type`

Recording the metrics of a request failed or panicked; the request was not affected. This is a bug: report it.

### `request`

Level: info, debug
Fields: `request_id`, `method`, `route`, `status`, `outcome`, `error_code`, `invalid_reason`, `duration_ms`, `provider`, `endpoint`, `stream`, `usage_injected`, `upstream_status`, `ttfb_ms`, `upstream_error`, `egress_reason`, `ledger`, `trace_id`, `usage_reader`, `limit_state`, `limits_unverified`

One line for each request of the inference listener, after it ended, with its outcome, status, route, durations and the provider and model when they were resolved; the identifiers are the request's own and never the tenant's. At debug on the health paths of the administration listener.

### `usage reader failed`

Level: error
Fields: `request_id`, `panic_type`

Reading the usage of a stream failed or panicked; the request was served and its usage is estimated or missing.

### `usage record failed`

Level: error
Fields: `request_id`, `panic_type`

Building the usage record of a request failed or panicked; the line has the request ID only. The record is lost: report it.

## Resolver

### `endpoint unusable`

Level: error
Fields: `provider`, `endpoint`

The URL of a provider endpoint in the catalogue cannot be used; correct it with `pandokos endpoint update`.

### `limit counters unavailable`

Level: warn
Fields: `error_class`

The limits could not read the counters of a tenant, so the policy of `limits.on_store_failure` applies; at most once a minute for each tenant, and the tenant is not named.

### `unlimited traffic not recorded`

Level: debug
Fields: `request_id`, `error_class`

A row of the audit of traffic without limits could not be written; at debug, and counted by the metric `pandokos.limits.unlimited_traffic`.

## Reports

### `usage rollup failing`

Level: warn
Fields: `error_class`, `sqlstate`, `failures`

The usage rollup of a tenant failed; reports and limits are still exact because they read the newest records from the ledger, but the rollup lags. At most once a minute, and the tenant is not named; `failures` counts the failed tenant passes since the last clean pass.

### `usage rollup recovered`

Level: info
Fields: `failures`

The first pass that succeeded after failures; `failures` is how many tenant passes failed before it.

### `retention ready`

Level: info
Fields: `usage_months`, `audit_months`

Written at start with the worker role: the configured retention. With both values 0 nothing is removed and the job ends.

### `retention applied`

Level: info
Fields: `partitions`, `usage_records`, `audit_events`, `platform_audit_events`

A retention run removed something; the numbers are totals over the installation, never of one tenant.

### `retention failing`

Level: warn
Fields: `error_class`, `sqlstate`, `failures`

A retention run failed in the whole or in part; the next run tries again. At most once an hour.

## Oidc

### `groups claim has an unexpected shape: no group is read`

Level: warn
Fields: `shape`

The groups claim of an identity token is not a list of strings, so no role is given through it; `shape` names its JSON type.

### `token endpoint failed`

Level: warn
Fields: `status`

The token endpoint of the identity provider answered an error status; the user may try again.

### `token endpoint not reached`

Level: warn
Fields: none

The token endpoint of the identity provider could not be reached; check the network and the endpoint.

## Server

### `http: *`

Level: warn
Fields: none

A message of the Go HTTP server (a bad handshake or a malformed request, for example), written with the prefix `http:`; it holds nothing of a request.

### `listening`

Level: info
Fields: `listener`, `address`, `tls`

A listener is bound to its address, with or without TLS.

### `shutdown complete`

Level: info, warn
Fields: `forced`, `duration_ms`

The server stopped; `forced` is true, and the level warn, when open requests had to be cut after the drain time.

### `shutdown started`

Level: info
Fields: `drain_timeout_ms`

The server began its shutdown and drains open requests for the time shown.

## Jwks

### `issuer keys fetched`

Level: info
Fields: `issuer`, `keys`, `duration`

The signing keys of a token issuer were fetched, with their number and the time taken.

### `issuer keys not fetched`

Level: warn
Fields: `issuer`, `class`, `reason`

The signing keys of a token issuer could not be fetched; `class` and `reason` say why. Tokens of that issuer are verified with the keys already held while they are fresh.

### `issuer metadata not fetched`

Level: warn
Fields: `issuer`, `class`, `reason`

The discovery document of a token issuer could not be fetched; `class` and `reason` say why.

## Ledger

### `ledger closed`

Level: info
Fields: `unwritten`, `missing`

The usage ledger closed at shutdown with nothing lost.

### `ledger closed with records lost`

Level: error
Fields: `unwritten`, `missing`

The usage ledger closed with records that were not written (`unwritten`, `missing`); they are lost. Look at the lines before it for the cause.

### `ledger write failing`

Level: warn
Fields: `error`, `queued`, `in_use`, `failures`

The ledger cannot write to the database; the buffer fills meanwhile. Written at most once a minute. When the buffer is full, requests are refused (`ledger: full`).

### `ledger write recovered`

Level: info
Fields: `failures`

The ledger writes again after a failure; `failures` counts the attempts that failed.

### `usage partitions created`

Level: info
Fields: `count`

Partitions of the usage table were created ahead of time.

### `usage partitions not ensured`

Level: warn
Fields: `error_class`, `sqlstate`

The creation of the next partitions of the usage table failed; it is tried again. If it keeps failing, records go to the default partition.

### `usage record not written`

Level: error
Fields: none

A usage record reached the ledger after it had closed and is lost; the line holds nothing of the record.

### `usage record rejected`

Level: error
Fields: `request_id`, `error`, `sqlstate`, `constraint`

The database refused a usage record; the line has the request ID, the SQLSTATE and the constraint. The record is lost: report it.

### `usage record without admission`

Level: error
Fields: none

A usage record arrived that the ledger had not admitted. This is a bug: report it.

### `usage records in the default partition`

Level: warn
Fields: none

Usage records are in the default partition of the table, which means a partition was missing at the time; written once. Ensure the partitions and see the usage guide.

## Limits

### `limit event not recorded`

Level: error
Fields: `kind`, `sqlstate`, `error_class`

A limit event could not be written to the database; the line has its kind, the SQLSTATE and a class, nothing of the tenant.

### `limit reconciliation failed`

Level: warn
Fields: `error_class`

A round of reconciliation of the limit counters failed; it is tried again. A cut lag that grows (`pandokos.limits.cut_lag`) says it keeps failing.

### `limit reservation expired`

Level: error
Fields: `request_id`

A reservation of the limits expired without being settled; this should never happen and is a bug: report it with the request ID.

### `limit rule cache flushed`

Level: debug
Fields: `reason`

The cache of limit rules was emptied, on a change or because it was full; at debug.

### `limit rules of an unknown metric or subject skipped`

Level: debug
Fields: `skipped`

Rules that name a metric or a subject Pandokos does not know were left out of the evaluation; at debug.

### `limit rules unavailable`

Level: warn
Fields: none

The limit rules could not be loaded; the policy of `limits.on_store_failure` applies until they can.

## Authn

### `lookup cache flushed`

Level: debug
Fields: `cache`, `reason`

A cache of key or token lookups was emptied because it was full; at debug.

### `principal cache flushed`

Level: debug
Fields: `reason`

The cache of authenticated principals was emptied, on a change or because it was full; at debug.

## Health

### `readiness check failed`

Level: warn
Fields: `check`, `error`

A readiness check failed, so `/readyz` answers 503; `check` names it. Look at the cause in `error`.
