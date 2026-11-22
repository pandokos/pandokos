# Log reference

Every message Pandokos writes to standard output, with its level and its fields. The log is JSON, one object per line, with the fields `time`, `level` and `msg` and then the fields of the message.

This page is a contract checked by tests: a message or a field that is not listed here fails them (sprint 6.1). The descriptions are completed with the telemetry guide.

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
| `usage_reader` | string | failed when the reader of the answer gave up. |
| `version` | string | The build version. |
| `warning` | string | The text of a configuration warning; never a value. |

## Telemetry

### `Tracer created`

Level: debug
Fields: `name`, `version`, `schemaURL`, `attributes`

Written by the telemetry.

### `TracerProvider created`

Level: debug
Fields: `config`

Written by the telemetry.

### `Meter created`

Level: debug
Fields: `Name`, `Version`, `SchemaURL`, `Attributes`

Written by the telemetry.

### `MeterProvider created`

Level: debug
Fields: `Resource`, `Readers`, `Views`

Written by the telemetry.

### `metric export failing`

Level: warn
Fields: `count`, `error_class`

Written by the telemetry.

### `telemetry error`

Level: warn
Fields: `count`, `error_class`

Written by the telemetry.

### `trace export failing`

Level: warn
Fields: `count`, `error_class`

Written by the telemetry.

## Admin

### `admin request`

Level: info, debug
Fields: `request_id`, `method`, `operation`, `status`, `duration_ms`, `error_code`, `principal_type`, `support`, `principal_id`, `key_id`
Platform identities: `key_id`, `principal_id`

Written by the admin.

### `authentication failed`

Level: warn
Fields: `listener`, `request_id`, `reason`, `audited`

Written by the admin.

### `authentication failure not recorded`

Level: debug
Fields: `request_id`

Written by the admin.

### `credential cannot be sealed`

Level: error
Fields: `request_id`, `operation`, `cause`, `data_key_version`, `kek_version`

Written by the admin.

### `credential validation`

Level: info
Fields: `request_id`, `provider`, `outcome`, `upstream_status`, `duration_ms`

Written by the admin.

### `grant refused`

Level: warn
Fields: `request_id`, `operation`

Written by the admin.

### `group rule skipped`

Level: warn
Fields: `rule`

Written by the admin.

### `idempotency response not stored`

Level: warn
Fields: `request_id`, `operation`, `status`

Written by the admin.

### `internal error`

Level: error
Fields: `request_id`, `operation`, `sqlstate`, `error`

Written by the admin.

### `panic recovered`

Level: error
Fields: `request_id`, `panic_type`, `stack`

Written by the admin.

### `price import failed`

Level: warn
Fields: `source`, `error_class`, `duration_ms`

Written by the admin.

### `price import failure not recorded`

Level: warn
Fields: `source`

Written by the admin.

### `prices imported`

Level: info
Fields: `source`, `import_id`, `digest`, `created`, `unchanged`, `overridden`, `unmatched`, `skipped`, `duration_ms`

Written by the admin.

### `session use not recorded`

Level: debug
Fields: `request_id`, `error`

Written by the admin.

### `sign-in through the identity provider unavailable`

Level: warn
Fields: `request_id`

Written by the admin.

### `throttle row not removed after a password reset`

Level: warn
Fields: `request_id`

Written by the admin.

## Serve

### `administrator check failed`

Level: debug
Fields: `error`

Written by the serve.

### `bootstrap key active`

Level: warn
Fields: `key_id`, `expires_at`
Platform identities: `key_id`

Written by the serve.

### `bootstrap key check failed`

Level: debug
Fields: `error`

Written by the serve.

### `configuration error`

Level: error
Fields: `key`, `problem`

Written by the serve.

### `configuration warning`

Level: warn
Fields: `key`, `warning`

Written by the serve.

### `database ready`

Level: info
Fields: `role`, `schema_version`

Written by the serve.

### `ledger flushed`

Level: info
Fields: `unwritten`

Written by the serve.

### `limit events not recorded`

Level: warn
Fields: `unwritten`

Written by the serve.

### `limits ready`

Level: info
Fields: `time_zone`, `on_unknown_price`, `on_store_failure`

Written by the serve.

### `no user with a platform role exists`

Level: warn
Fields: `hint`

Written by the serve.

### `purge failed`

Level: warn
Fields: `target`, `sqlstate`

Written by the serve.

### `session count failed`

Level: warn
Fields: `sqlstate`

Written by the serve.

### `starting`

Level: info
Fields: `version`, `commit`, `go_version`

Written by the serve.

### `startup failed`

Level: error
Fields: `error`

Written by the serve.

### `vault ready`

Level: info
Fields: `kek_provider`, `kek_version`

Written by the serve.

## Store

### `cache invalidation unavailable`

Level: warn
Fields: `role`, `detail`

Written by the store.

### `cache listener connected`

Level: info
Fields: `role`, `direct`

Written by the store.

### `cache listener disconnected`

Level: warn
Fields: `role`, `error`

Written by the store.

## Catalog

### `catalog cache flushed`

Level: debug
Fields: `reason`

Written by the catalog.

### `price import skipped`

Level: info
Fields: `source`, `reason`

Written by the catalog.

## Vault

### `credential cannot be opened`

Level: error
Fields: `request_id`, `data_key_version`, `kek_version`, `cause`

Written by the vault.

### `vault cache flushed`

Level: debug
Fields: `reason`

Written by the vault.

## Gateway

### `egress blocked`

Level: warn
Fields: `request_id`, `provider`, `endpoint`, `reason`, `address`

Written by the gateway.

### `limit settle failed`

Level: error
Fields: `request_id`, `panic_type`, `error`

Written by the gateway.

### `metrics observation failed`

Level: error
Fields: `request_id`, `panic_type`

Written by the gateway.

### `request`

Level: info, debug
Fields: `request_id`, `method`, `route`, `status`, `outcome`, `error_code`, `invalid_reason`, `duration_ms`, `provider`, `endpoint`, `stream`, `usage_injected`, `upstream_status`, `ttfb_ms`, `upstream_error`, `egress_reason`, `ledger`, `trace_id`, `usage_reader`, `limit_state`, `limits_unverified`

Written by the gateway.

### `usage reader failed`

Level: error
Fields: `request_id`, `panic_type`

Written by the gateway.

### `usage record failed`

Level: error
Fields: `request_id`, `panic_type`

Written by the gateway.

## Resolver

### `endpoint unusable`

Level: error
Fields: `provider`, `endpoint`

Written by the resolver.

### `limit counters unavailable`

Level: warn
Fields: `error_class`

Written by the resolver.

### `unlimited traffic not recorded`

Level: debug
Fields: `request_id`, `error_class`

Written by the resolver.

## Oidc

### `groups claim has an unexpected shape: no group is read`

Level: warn
Fields: `shape`

Written by the oidc.

### `token endpoint failed`

Level: warn
Fields: `status`

Written by the oidc.

### `token endpoint not reached`

Level: warn
Fields: none

Written by the oidc.

## Server

### `http: *`

Level: warn
Fields: none

Written by the server.

### `listening`

Level: info
Fields: `listener`, `address`, `tls`

Written by the server.

### `shutdown complete`

Level: info, warn
Fields: `forced`, `duration_ms`

Written by the server.

### `shutdown started`

Level: info
Fields: `drain_timeout_ms`

Written by the server.

## Jwks

### `issuer keys fetched`

Level: info
Fields: `issuer`, `keys`, `duration`

Written by the jwks.

### `issuer keys not fetched`

Level: warn
Fields: `issuer`, `class`, `reason`

Written by the jwks.

### `issuer metadata not fetched`

Level: warn
Fields: `issuer`, `class`, `reason`

Written by the jwks.

## Ledger

### `ledger closed`

Level: info
Fields: `unwritten`, `missing`

Written by the ledger.

### `ledger closed with records lost`

Level: error
Fields: `unwritten`, `missing`

Written by the ledger.

### `ledger write failing`

Level: warn
Fields: `error`, `queued`, `in_use`, `failures`

Written by the ledger.

### `ledger write recovered`

Level: info
Fields: `failures`

Written by the ledger.

### `usage partitions created`

Level: info
Fields: `count`

Written by the ledger.

### `usage partitions not ensured`

Level: warn
Fields: `error_class`, `sqlstate`

Written by the ledger.

### `usage record not written`

Level: error
Fields: none

Written by the ledger.

### `usage record rejected`

Level: error
Fields: `request_id`, `error`, `sqlstate`, `constraint`

Written by the ledger.

### `usage record without admission`

Level: error
Fields: none

Written by the ledger.

### `usage records in the default partition`

Level: warn
Fields: none

Written by the ledger.

## Limits

### `limit event not recorded`

Level: error
Fields: `kind`, `sqlstate`, `error_class`

Written by the limits.

### `limit reconciliation failed`

Level: warn
Fields: `error_class`

Written by the limits.

### `limit reservation expired`

Level: error
Fields: `request_id`

Written by the limits.

### `limit rule cache flushed`

Level: debug
Fields: `reason`

Written by the limits.

### `limit rules of an unknown metric or subject skipped`

Level: debug
Fields: `skipped`

Written by the limits.

### `limit rules unavailable`

Level: warn
Fields: none

Written by the limits.

## Authn

### `lookup cache flushed`

Level: debug
Fields: `cache`, `reason`

Written by the authn.

### `principal cache flushed`

Level: debug
Fields: `reason`

Written by the authn.

## Health

### `readiness check failed`

Level: warn
Fields: `check`, `error`

Written by the health.
