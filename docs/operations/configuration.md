# Configuration reference

Pandokos reads its static configuration from built-in defaults, one YAML file and environment
variables ([ADR 0019](../adr/0019-configuration.md)). Secrets are never written in the
configuration: a key names a file that holds the secret.

## Sources and precedence

1. Built-in defaults.
2. The YAML file, if one is given.
3. Environment variables. A variable whose value is the empty string is treated as unset.

A later source wins over an earlier one: an environment variable overrides the file, the file
overrides the default.

## The file

The path is, in order of precedence: the `--config FILE` flag of `pandokos serve`,
`pandokos migrate` and `pandokos config check`, the `PANDOKOS_CONFIG` environment variable, or none. A path that is given
but cannot be read is an error.

The file is strict: an unknown key is an error, an empty file is valid, and a second YAML document
in the same file is an error. Error messages name the line and the key, never the value.

## Keys

The environment variable of a key is `PANDOKOS_` followed by the key in upper case with every `.`
replaced by `_`.

| Key | Environment variable | Type | Default | Meaning |
|---|---|---|---|---|
| `log.level` | `PANDOKOS_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` | Minimum level written |
| `listeners.inference.address` | `PANDOKOS_LISTENERS_INFERENCE_ADDRESS` | `host:port` | `0.0.0.0:8080` | Listen address of the inference API |
| `listeners.inference.read_header_timeout` | `PANDOKOS_LISTENERS_INFERENCE_READ_HEADER_TIMEOUT` | duration | `10s` | Time allowed to read request headers |
| `listeners.inference.idle_timeout` | `PANDOKOS_LISTENERS_INFERENCE_IDLE_TIMEOUT` | duration | `120s` | Keep-alive idle time |
| `listeners.inference.max_header_bytes` | `PANDOKOS_LISTENERS_INFERENCE_MAX_HEADER_BYTES` | integer | `65536` | Maximum size of request headers |
| `listeners.inference.tls.cert_file` | `PANDOKOS_LISTENERS_INFERENCE_TLS_CERT_FILE` | path | none | PEM file with the server certificate and any intermediate certificates. With `key_file`, turns TLS on for the listener |
| `listeners.inference.tls.key_file` | `PANDOKOS_LISTENERS_INFERENCE_TLS_KEY_FILE` | path | none | PEM file with the private key of the certificate |
| `listeners.inference.tls.min_version` | `PANDOKOS_LISTENERS_INFERENCE_TLS_MIN_VERSION` | `TLS1.2`, `TLS1.3` | `TLS1.2` | Lowest protocol version accepted |
| `listeners.admin.address` | `PANDOKOS_LISTENERS_ADMIN_ADDRESS` | `host:port` | `127.0.0.1:8081` | Listen address of the administration API |
| `listeners.admin.read_header_timeout` | `PANDOKOS_LISTENERS_ADMIN_READ_HEADER_TIMEOUT` | duration | `10s` | Time allowed to read request headers |
| `listeners.admin.idle_timeout` | `PANDOKOS_LISTENERS_ADMIN_IDLE_TIMEOUT` | duration | `120s` | Keep-alive idle time |
| `listeners.admin.max_header_bytes` | `PANDOKOS_LISTENERS_ADMIN_MAX_HEADER_BYTES` | integer | `65536` | Maximum size of request headers |
| `listeners.admin.tls.cert_file` | `PANDOKOS_LISTENERS_ADMIN_TLS_CERT_FILE` | path | none | PEM file with the server certificate and any intermediate certificates. With `key_file`, turns TLS on for the listener |
| `listeners.admin.tls.key_file` | `PANDOKOS_LISTENERS_ADMIN_TLS_KEY_FILE` | path | none | PEM file with the private key of the certificate |
| `listeners.admin.tls.min_version` | `PANDOKOS_LISTENERS_ADMIN_TLS_MIN_VERSION` | `TLS1.2`, `TLS1.3` | `TLS1.2` | Lowest protocol version accepted |
| `listeners.admin.allow_plaintext` | `PANDOKOS_LISTENERS_ADMIN_ALLOW_PLAINTEXT` | boolean | `false` | Acknowledges that the administration API is served without TLS on an address that is not loopback, because TLS ends in front of it |
| `listeners.admin.trusted_proxies` | `PANDOKOS_LISTENERS_ADMIN_TRUSTED_PROXIES` | list | none | CIDR prefixes, separated by commas in the environment, of the proxies in front of the administration listener whose X-Forwarded-For is believed |
| `limits.max_request_body_bytes` | `PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES` | integer | `10485760` | Larger bodies get 413 `request_too_large` |
| `limits.request_body_read_timeout` | `PANDOKOS_LIMITS_REQUEST_BODY_READ_TIMEOUT` | duration | `30s` | Time allowed to read the request body |
| `limits.max_request_duration` | `PANDOKOS_LIMITS_MAX_REQUEST_DURATION` | duration | `10m` | Total time for one proxied request, stream included, measured from the end of the body read |
| `limits.default_output_tokens` | `PANDOKOS_LIMITS_DEFAULT_OUTPUT_TOKENS` | integer | `4096` | Output tokens reserved per choice for a request that sets no output limit; capped by the model's `max_output_tokens` when the catalogue has it (1 to 1000000) |
| `limits.time_zone` | `PANDOKOS_LIMITS_TIME_ZONE` | IANA zone name | `UTC` | The zone in which the days and months of quotas and budgets start for the tenants that have no time zone of their own (a tenant's zone is set through the administration API); a name known to the embedded time zone database whose offsets are whole minutes |
| `limits.on_unknown_price` | `PANDOKOS_LIMITS_ON_UNKNOWN_PRICE` | `refuse`, `allow` | `refuse` | What happens to a request that a budget applies to and cannot measure because the model has no price in the budget's currency: refused with 409 `no_price`, or admitted |
| `limits.on_store_failure` | `PANDOKOS_LIMITS_ON_STORE_FAILURE` | `refuse`, `allow` | `refuse` | What happens to a request whose quota and budget counters cannot be verified: refused with 503 `unavailable`, or admitted; `allow` is a start-up warning |
| `limits.reconcile_interval` | `PANDOKOS_LIMITS_RECONCILE_INTERVAL` | duration | `5m` | How often the period counters of quotas and budgets are folded into the usage ledger (10s to 1h) |
| `upstream.connect_timeout` | `PANDOKOS_UPSTREAM_CONNECT_TIMEOUT` | duration | `10s` | TCP connect timeout, and separately TLS handshake timeout, towards the provider |
| `upstream.tls.ca_file` | `PANDOKOS_UPSTREAM_TLS_CA_FILE` | path | none | PEM file whose certificates are added to the system roots for every outbound TLS connection of the server (providers, validation, price download, an `https` proxy); read by `serve` only |
| `upstream.proxy.url` | `PANDOKOS_UPSTREAM_PROXY_URL` | URL | none | Proxy for provider calls to public endpoints, credential validation and the price download; private endpoints go direct; `HTTPS_PROXY` and similar variables are never read; read by `serve` only |
| `shutdown.drain_timeout` | `PANDOKOS_SHUTDOWN_DRAIN_TIMEOUT` | duration | `30s` | Time in-flight requests get to finish after a termination signal |
| `cache.principal_ttl` | `PANDOKOS_CACHE_PRINCIPAL_TTL` | duration | `30s` | Lifetime of a cached principal in the gateway: the upper bound on the time a revocation or a suspension takes to reach the inference listener (1s to 60s) |
| `cache.credential_ttl` | `PANDOKOS_CACHE_CREDENTIAL_TTL` | duration | `30s` | Lifetime of a cached provider credential in the gateway: the upper bound on the time a disable, revocation or replacement takes to reach the inference listener when notifications are not delivered (1s to 60s) |
| `cache.data_key_ttl` | `PANDOKOS_CACHE_DATA_KEY_TTL` | duration | `5m` | Lifetime of an unwrapped data key in the gateway (1s to 15m) |
| `cache.catalog_ttl` | `PANDOKOS_CACHE_CATALOG_TTL` | duration | `30s` | Lifetime of the cached catalogue and of a tenant's model access, active currency and price lists in the gateway: the upper bound on the time a catalogue, price, model-access, tenant price or active-currency change takes to reach the inference listener when notifications are not delivered (1s to 5m); see the [catalogue guide](catalogue.md) |
| `cache.limits_ttl` | `PANDOKOS_CACHE_LIMITS_TTL` | duration | `30s` | Lifetime of a tenant's cached limit rules in the gateway: the upper bound on the time a changed rule or a changed active currency takes to apply when no notification arrives (1s to 5m) |
| `tokens.enabled` | `PANDOKOS_TOKENS_ENABLED` | boolean | `true` | Accept tokens of trusted issuers on the inference listener. `false`: every credential that is not a virtual key is malformed and no key of an issuer is ever fetched; read by `serve` only. See the [tokens guide](tokens.md) |
| `tokens.max_token_bytes` | `PANDOKOS_TOKENS_MAX_TOKEN_BYTES` | integer | `8192` | Largest token accepted (1024 to 16384, and at most half of `listeners.inference.max_header_bytes`) |
| `tokens.clock_skew` | `PANDOKOS_TOKENS_CLOCK_SKEW` | duration | `60s` | Tolerance on `exp`, `nbf` and `iat` of a token (0 to 5m) |
| `tokens.max_lifetime` | `PANDOKOS_TOKENS_MAX_LIFETIME` | duration | `24h` | Longest lifetime (`exp` minus `iat`) a token may have (1m to 720h) |
| `tokens.private_hosts` | `PANDOKOS_TOKENS_PRIVATE_HOSTS` | list | none | Host names, separated by commas in the environment, whose issuer and key URLs may resolve to private addresses (at most 32 lower-case DNS names, no address literal, no wildcard) |
| `tokens.jwks.fetch_timeout` | `PANDOKOS_TOKENS_JWKS_FETCH_TIMEOUT` | duration | `5s` | Time allowed to fetch one document of an issuer (1s to 30s) |
| `tokens.jwks.refresh_interval` | `PANDOKOS_TOKENS_JWKS_REFRESH_INTERVAL` | duration | `15m` | Time between background refreshes of the key set of an issuer (1m to 1h) |
| `tokens.jwks.min_refetch_interval` | `PANDOKOS_TOKENS_JWKS_MIN_REFETCH_INTERVAL` | duration | `10s` | Shortest time between two fetches of a key set caused by an unknown key ID (1s to 5m) |
| `tokens.jwks.max_stale` | `PANDOKOS_TOKENS_JWKS_MAX_STALE` | duration | `6h` | Time a key set is used after its last successful fetch, then calls are answered 503 (at least twice `tokens.jwks.refresh_interval`, at most 72h) |
| `tokens.admin_listener` | `PANDOKOS_TOKENS_ADMIN_LISTENER` | boolean | `false` | Accept tokens of trusted issuers on the administration listener too; needs tokens.enabled |
| `auth.public_origin` | `PANDOKOS_AUTH_PUBLIC_ORIGIN` | URL | none | Origin that browsers use to reach the administration API: https://host[:port], or http:// with a loopback host; required with auth.oidc.enabled |
| `auth.local.enabled` | `PANDOKOS_AUTH_LOCAL_ENABLED` | boolean | `true` | Allow sign-in with a local account; false is refused unless auth.oidc.enabled |
| `auth.local.password_min_length` | `PANDOKOS_AUTH_LOCAL_PASSWORD_MIN_LENGTH` | integer | `12` | Shortest password accepted, 8 to 128; below 12 warns |
| `auth.local.generated_password_ttl` | `PANDOKOS_AUTH_LOCAL_GENERATED_PASSWORD_TTL` | duration | `72h` | Time a generated password stays valid until it is changed; 1h to 30d |
| `auth.local.max_failures` | `PANDOKOS_AUTH_LOCAL_MAX_FAILURES` | integer | `10` | Failed sign-ins of one username in auth.local.failure_window that lock it; 3 to 100 |
| `auth.local.failure_window` | `PANDOKOS_AUTH_LOCAL_FAILURE_WINDOW` | duration | `15m` | Window in which failed sign-ins are counted; 1m to 24h |
| `auth.local.lock_duration` | `PANDOKOS_AUTH_LOCAL_LOCK_DURATION` | duration | `15m` | Time a username stays locked; 1m to 24h |
| `auth.local.source_max_failures` | `PANDOKOS_AUTH_LOCAL_SOURCE_MAX_FAILURES` | integer | `50` | Failed sign-ins from one source address prefix in the window that delay it; at least auth.local.max_failures |
| `auth.local.argon2.memory_kib` | `PANDOKOS_AUTH_LOCAL_ARGON2_MEMORY_KIB` | integer | `65536` | Argon2id memory cost of new password hashes, in KiB |
| `auth.local.argon2.iterations` | `PANDOKOS_AUTH_LOCAL_ARGON2_ITERATIONS` | integer | `3` | Argon2id time cost of new password hashes |
| `auth.local.argon2.parallelism` | `PANDOKOS_AUTH_LOCAL_ARGON2_PARALLELISM` | integer | `1` | Argon2id parallelism of new password hashes |
| `auth.local.max_concurrent_hashes` | `PANDOKOS_AUTH_LOCAL_MAX_CONCURRENT_HASHES` | integer | `4` | Password hashes computed at the same time; more are answered 503; 1 to 64 |
| `auth.session.idle_timeout` | `PANDOKOS_AUTH_SESSION_IDLE_TIMEOUT` | duration | `30m` | Time without use after which a session ends; 1m to 24h, at most auth.session.absolute_lifetime |
| `auth.session.absolute_lifetime` | `PANDOKOS_AUTH_SESSION_ABSOLUTE_LIFETIME` | duration | `12h` | Longest life of a session; at most 168h |
| `auth.session.max_per_user` | `PANDOKOS_AUTH_SESSION_MAX_PER_USER` | integer | `10` | Sessions a user may hold; the oldest end first; 1 to 100 |
| `auth.oidc.enabled` | `PANDOKOS_AUTH_OIDC_ENABLED` | boolean | `false` | Allow sign-in through an OpenID Connect identity provider; needs issuer, client_id, client_secret_file and auth.public_origin |
| `auth.oidc.issuer` | `PANDOKOS_AUTH_OIDC_ISSUER` | URL | none | Issuer of the identity provider; https |
| `auth.oidc.client_id` | `PANDOKOS_AUTH_OIDC_CLIENT_ID` | string | none | Client ID registered at the identity provider |
| `auth.oidc.client_secret_file` | `PANDOKOS_AUTH_OIDC_CLIENT_SECRET_FILE` | path | none | File with the client secret (at least 16 bytes); the value is a secret |
| `auth.oidc.client_auth` | `PANDOKOS_AUTH_OIDC_CLIENT_AUTH` | basic, post | `basic` | How the client authenticates at the token endpoint |
| `auth.oidc.scopes` | `PANDOKOS_AUTH_OIDC_SCOPES` | list | `openid profile email` | Scopes requested, separated by commas in the environment; contains openid |
| `auth.oidc.algorithms` | `PANDOKOS_AUTH_OIDC_ALGORITHMS` | list | `RS256 ES256` | Signature algorithms accepted for the ID token, separated by commas in the environment |
| `auth.oidc.display_name` | `PANDOKOS_AUTH_OIDC_DISPLAY_NAME` | string | `Single sign-on` | Name of the identity provider shown by the sign-in page |
| `auth.oidc.name_claim` | `PANDOKOS_AUTH_OIDC_NAME_CLAIM` | string | `name` | Claim of the ID token that gives the display name |
| `auth.oidc.email_claim` | `PANDOKOS_AUTH_OIDC_EMAIL_CLAIM` | string | `email` | Claim of the ID token that gives the e-mail address |
| `auth.oidc.groups_claim` | `PANDOKOS_AUTH_OIDC_GROUPS_CLAIM` | string | `groups` | Claim of the ID token that gives the groups; a dotted path of at most four members |
| `auth.oidc.group_mappings` | `PANDOKOS_AUTH_OIDC_GROUP_MAPPINGS` | list | none | Rules `<target>=<group>`, separated by commas in the environment; the target is `platform:<role>` or `tenant:<tenant name>:<role>` |
| `auth.oidc.auto_provision` | `PANDOKOS_AUTH_OIDC_AUTO_PROVISION` | boolean | `false` | Create a user at the first sign-in of an unknown subject that the group rules give a role; refused without auth.oidc.group_mappings |
| `auth.oidc.private_hosts` | `PANDOKOS_AUTH_OIDC_PRIVATE_HOSTS` | list | none | Host names, separated by commas in the environment, whose URLs may resolve to private addresses, as tokens.private_hosts |
| `auth.oidc.post_sign_in_path` | `PANDOKOS_AUTH_OIDC_POST_SIGN_IN_PATH` | path | `/` | Where the browser goes after sign-in through the identity provider: starts with one /, no //, no \, no control character |
| `idempotency.retention` | `PANDOKOS_IDEMPOTENCY_RETENTION` | duration | `24h` | Time an idempotency key is kept; 1h to 7d |
| `support.enabled` | `PANDOKOS_SUPPORT_ENABLED` | boolean | `true` | Allow the platform to open support actions inside a tenant; `false`: none can be opened and none is honoured, and a tenant that loses its administrators cannot be recovered through the API (a warning at start) |
| `support.max_duration` | `PANDOKOS_SUPPORT_MAX_DURATION` | duration | `1h` | Default and longest life of a support action; 1m to 24h |
| `reports.rollup_interval` | `PANDOKOS_REPORTS_ROLLUP_INTERVAL` | duration | `1m` | How often the worker adds the new usage records to the rollup that reports and limit counters read; 10s to 1h; `serve` only ([usage guide](usage.md#13-rollup)) |
| `retention.usage_months` | `PANDOKOS_RETENTION_USAGE_MONTHS` | integer | `0` | Months of usage kept besides the current UTC month: `0` keeps everything, otherwise 2 to 120; older monthly partitions are dropped and the rollup and default-partition rows of the tenants removed, for good; `serve` only ([usage guide](usage.md#7-partitions)) |
| `retention.audit_months` | `PANDOKOS_RETENTION_AUDIT_MONTHS` | integer | `0` | Months of audit rows kept besides the current UTC month: `0` keeps everything, otherwise 1 to 1200; older rows are removed from the start of each chain and the anchor moves; a value below 12 is a start-up warning; `serve` only ([audit guide](audit.md#6-retention-of-the-logs)) |
| `database.address` | `PANDOKOS_DATABASE_ADDRESS` | `host:port` | none, required | Address of PostgreSQL, or of the connection pooler in front of it |
| `database.direct_address` | `PANDOKOS_DATABASE_DIRECT_ADDRESS` | `host:port` | none | Address of PostgreSQL itself, used only by the notification listener. Set it when `database.address` is a pooler in transaction mode |
| `database.name` | `PANDOKOS_DATABASE_NAME` | name | `pandokos` | Database name |
| `database.tls.mode` | `PANDOKOS_DATABASE_TLS_MODE` | `verify-full`, `disable` | `verify-full` | `verify-full`: TLS with verification of the certificate chain and of the host name. `disable`: no TLS |
| `database.tls.ca_file` | `PANDOKOS_DATABASE_TLS_CA_FILE` | path | none | PEM file with the certificate authorities to trust; when empty the system roots are used |
| `database.connect_timeout` | `PANDOKOS_DATABASE_CONNECT_TIMEOUT` | duration | `5s` | Time allowed to establish one database connection |
| `database.gateway.user` | `PANDOKOS_DATABASE_GATEWAY_USER` | name | `pandokos_gateway` | Database user of the gateway role |
| `database.gateway.password_file` | `PANDOKOS_DATABASE_GATEWAY_PASSWORD_FILE` | path | none, required by `serve` | File with that user's password |
| `database.gateway.max_connections` | `PANDOKOS_DATABASE_GATEWAY_MAX_CONNECTIONS` | integer | `10` | Size limit of the gateway pool |
| `database.admin.user` | `PANDOKOS_DATABASE_ADMIN_USER` | name | `pandokos_admin` | Database user of the admin role |
| `database.admin.password_file` | `PANDOKOS_DATABASE_ADMIN_PASSWORD_FILE` | path | none, required by `serve` | File with that user's password |
| `database.admin.max_connections` | `PANDOKOS_DATABASE_ADMIN_MAX_CONNECTIONS` | integer | `10` | Size limit of the admin pool |
| `database.worker.user` | `PANDOKOS_DATABASE_WORKER_USER` | name | `pandokos_worker` | Database user of the worker role |
| `database.worker.password_file` | `PANDOKOS_DATABASE_WORKER_PASSWORD_FILE` | path | none, required by `serve` | File with that user's password |
| `database.worker.max_connections` | `PANDOKOS_DATABASE_WORKER_MAX_CONNECTIONS` | integer | `5` | Size limit of the worker pool |
| `database.owner.user` | `PANDOKOS_DATABASE_OWNER_USER` | name | `pandokos_owner` | Database user of `pandokos migrate` |
| `database.owner.password_file` | `PANDOKOS_DATABASE_OWNER_PASSWORD_FILE` | path | none, required by `migrate` | File with that user's password. Never read by `serve` |
| `vault.kek.provider` | `PANDOKOS_VAULT_KEK_PROVIDER` | `local`, `transit` | `local` | Key-encryption key provider that wraps data keys: `local` (a key file) or `transit` (an OpenBao or HashiCorp Vault transit key) |
| `vault.kek.local.key_file` | `PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE` | path | none, required by `serve` with the provider `local` | File with the key-encryption key: 32 random bytes in standard base64. Create it with `pandokos kek generate` ([vault guide](vault.md)) |
| `vault.kek.local.unwrap_key_files` | `PANDOKOS_VAULT_KEK_LOCAL_UNWRAP_KEY_FILES` | list of paths | empty | Up to 8 more key-encryption key files the instance can unwrap with: a version being retired, or a new version that other instances already use. Each file is read as `key_file`; separated by commas in the environment; a key equal to `key_file` or to another entry is refused at start, whatever its path. `serve` only ([vault guide](vault.md), "Rotating the key") |
| `vault.kek.transit.address` | `PANDOKOS_VAULT_KEK_TRANSIT_ADDRESS` | URL | empty | Address of the key service: `https`, or `http` for a loopback host only; with provider `local` it makes the service an unwrap-only provider. `serve` only |
| `vault.kek.transit.namespace` | `PANDOKOS_VAULT_KEK_TRANSIT_NAMESPACE` | string | empty | Namespace of the key service, sent as `X-Vault-Namespace`. `serve` only |
| `vault.kek.transit.mount` | `PANDOKOS_VAULT_KEK_TRANSIT_MOUNT` | string | `transit` | Mount path of the transit engine. `serve` only |
| `vault.kek.transit.key_name` | `PANDOKOS_VAULT_KEK_TRANSIT_KEY_NAME` | string | empty | Name of the transit key, which must exist; required with an address. `serve` only |
| `vault.kek.transit.tls.ca_file` | `PANDOKOS_VAULT_KEK_TRANSIT_TLS_CA_FILE` | path | empty | PEM file with the only certificate authorities trusted for the key service. `serve` only |
| `vault.kek.transit.auth.method` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_METHOD` | `token`, `approle` | empty | How Pandokos authenticates to the key service; required with an address. `serve` only |
| `vault.kek.transit.auth.token_file` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_TOKEN_FILE` | path | empty | File with the token; required with method `token`. `serve` only |
| `vault.kek.transit.auth.approle.mount` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_MOUNT` | string | `approle` | Mount path of the AppRole auth method. `serve` only |
| `vault.kek.transit.auth.approle.role_id_file` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_ROLE_ID_FILE` | path | empty | File with the AppRole role ID; required with method `approle`. `serve` only |
| `vault.kek.transit.auth.approle.secret_id_file` | `PANDOKOS_VAULT_KEK_TRANSIT_AUTH_APPROLE_SECRET_ID_FILE` | path | empty | File with the AppRole secret ID; required with method `approle`. `serve` only |
| `vault.kek.transit.request_timeout` | `PANDOKOS_VAULT_KEK_TRANSIT_REQUEST_TIMEOUT` | duration | `2s` | Time allowed to each request to the key service, `100ms` to `30s`. `serve` only |
| `vault.kek.transit.max_concurrent_requests` | `PANDOKOS_VAULT_KEK_TRANSIT_MAX_CONCURRENT_REQUESTS` | integer | `32` | Requests in flight to the key service at most, 1 to 1024. `serve` only |
| `vault.kek.transit.start_timeout` | `PANDOKOS_VAULT_KEK_TRANSIT_START_TIMEOUT` | duration | `0` | Time `serve` waits for the key service at start, up to `1h`; `0` waits without limit. `serve` only |
| `tenant_deletion.grace_period` | `PANDOKOS_TENANT_DELETION_GRACE_PERIOD` | duration | `168h` | Time between the request of a tenant deletion and its start; the tenant is suspended meanwhile. `0` to `2160h` (90 days); `0` starts the deletion at the worker's next tick. Read when a deletion is requested, `serve` only ([deletion guide](tenant-deletion.md)) |
| `tenant_deletion.interval` | `PANDOKOS_TENANT_DELETION_INTERVAL` | duration | `1m` | Time between two runs of the deletion job of the worker (`10s` to `1h`), `serve` only |
| `events.source` | `PANDOKOS_EVENTS_SOURCE` | string | `urn:pandokos` | The CloudEvents `source` of every event of this installation; a URI reference of 1 to 255 printable characters; `serve` only ([events guide](events.md)) |
| `events.retention` | `PANDOKOS_EVENTS_RETENTION` | duration | `168h` | How long events are kept, delivered or not, and how far back a destination can be rewound; `24h` to `2160h`, whole hours; `serve` only |
| `events.relay.interval` | `PANDOKOS_EVENTS_RELAY_INTERVAL` | duration | `1s` | Time between two passes of the event relay of the worker; `100ms` to `1m`; `serve` only |
| `events.relay.workers` | `PANDOKOS_EVENTS_RELAY_WORKERS` | integer | `4` | Destinations that this process delivers to at once; 1 to 64; `serve` only |
| `events.relay.batch_size` | `PANDOKOS_EVENTS_RELAY_BATCH_SIZE` | integer | `100` | Events read for one destination in one round; 1 to 1000; `serve` only |
| `events.delivery.timeout` | `PANDOKOS_EVENTS_DELIVERY_TIMEOUT` | duration | `10s` | Time allowed to one delivery request; `1s` to `30s`; `serve` only |
| `events.delivery.max_attempts` | `PANDOKOS_EVENTS_DELIVERY_MAX_ATTEMPTS` | integer | `10` | Attempts to deliver an event before it is parked as a dead letter; 1 to 100; `serve` only |
| `events.delivery.backoff_max` | `PANDOKOS_EVENTS_DELIVERY_BACKOFF_MAX` | duration | `15m` | Longest wait between two attempts to deliver an event; `5s` to `24h`; `serve` only |
| `events.webhook.private_hosts` | `PANDOKOS_EVENTS_WEBHOOK_PRIVATE_HOSTS` | list | none | Host names, separated by commas in the environment, whose webhook URLs may be `http` and resolve to private addresses, as `tokens.private_hosts`; at most 32 lower-case DNS names, no address literal, no wildcard; `serve` only. List only hosts you control |
| `pricing.import_at_start` | `PANDOKOS_PRICING_IMPORT_AT_START` | boolean | `true` | Import the embedded price snapshot when `serve` starts, unless an identical or a newer import exists ([catalogue guide](catalogue.md)) |
| `pricing.default_currency` | `PANDOKOS_PRICING_DEFAULT_CURRENCY` | ISO 4217 code | `USD` | The currency in which the costs of the tenants that have not chosen an active currency are computed, and in which their cost budgets apply; three capital letters. It must be the same on every instance, like `limits.time_zone`, and set before the first start of a release that has tenant price lists if your prices and budgets are not in USD ([database guide](database.md), section 17). It takes effect at restart, for every such tenant at once ([catalogue guide](catalogue.md), section 7) |
| `pricing.refresh.enabled` | `PANDOKOS_PRICING_REFRESH_ENABLED` | boolean | `false` | Download the price dataset periodically: an outbound request to `pricing.refresh.url` |
| `pricing.refresh.url` | `PANDOKOS_PRICING_REFRESH_URL` | URL | `https://raw.githubusercontent.com/pydantic/genai-prices/main/prices/new_data/v2/data.json` | Address of the price dataset; `https` only, no user information. Pin it to a commit to control what is imported |
| `pricing.refresh.interval` | `PANDOKOS_PRICING_REFRESH_INTERVAL` | duration | `24h` | Time between downloads (1h to 720h) |
| `ledger.flush_interval` | `PANDOKOS_LEDGER_FLUSH_INTERVAL` | duration | `50ms` | Longest time a usage record waits in memory before it is written (1ms to 10s) |
| `ledger.batch_size` | `PANDOKOS_LEDGER_BATCH_SIZE` | integer | `500` | Most usage records written in one transaction (1 to 5000) |
| `ledger.buffer_size` | `PANDOKOS_LEDGER_BUFFER_SIZE` | integer | `10000` | Usage records held in memory, admitted requests included; when it is full the gateway answers 503 (100 to 1000000, at least `ledger.batch_size`) |
| `ledger.shutdown_flush_timeout` | `PANDOKOS_LEDGER_SHUTDOWN_FLUSH_TIMEOUT` | duration | `10s` | Time allowed to write the records still in memory when the process stops (1s to 5m) |
| `telemetry.otlp.endpoint` | `PANDOKOS_TELEMETRY_OTLP_ENDPOINT` | URL | none | Base URL of the OTLP/HTTP collector; `/v1/traces` and `/v1/metrics` are appended to its path; empty means no export |
| `telemetry.otlp.protocol` | `PANDOKOS_TELEMETRY_OTLP_PROTOCOL` | `http/protobuf` | `http/protobuf` | Export protocol; only `http/protobuf` exists |
| `telemetry.otlp.headers_file` | `PANDOKOS_TELEMETRY_OTLP_HEADERS_FILE` | path | none | File with the headers sent to the collector, one `Name: value` per line; the values are secrets |
| `telemetry.otlp.timeout` | `PANDOKOS_TELEMETRY_OTLP_TIMEOUT` | duration | `10s` | Time allowed for one export (1s to 60s) |
| `telemetry.traces.sample_ratio` | `PANDOKOS_TELEMETRY_TRACES_SAMPLE_RATIO` | decimal | `1` | Share of the traces started here that is sampled (0 to 1); the sampled flag of a caller's `traceparent` is followed |
| `telemetry.traces.propagate` | `PANDOKOS_TELEMETRY_TRACES_PROPAGATE` | boolean | `false` | Send the trace context to the provider in a `traceparent` header |
| `telemetry.resource.attributes` | `PANDOKOS_TELEMETRY_RESOURCE_ATTRIBUTES` | list | none | Resource attributes of the traces and the metrics: `key=value` pairs separated by commas, in YAML and in the environment |
| `telemetry.metrics.otlp` | `PANDOKOS_TELEMETRY_METRICS_OTLP` | boolean | `true` | Export metrics over OTLP to `telemetry.otlp.endpoint` (`/v1/metrics` is appended), when that is set |
| `telemetry.metrics.export_interval` | `PANDOKOS_TELEMETRY_METRICS_EXPORT_INTERVAL` | duration | `60s` | Time between two OTLP metric exports (5s to 10m) |
| `telemetry.metrics.prometheus` | `PANDOKOS_TELEMETRY_METRICS_PROMETHEUS` | boolean | `false` | Serve `GET /metrics` on the administration listener, for a platform key holding `metrics:read` |

Durations are strings in Go syntax (`"30s"`, `"10m"`); a YAML number is rejected for a duration.
Sizes are integers in bytes.

There is no key that holds a database password and no key that takes a connection URL (a URL would
invite a password inside it). The address is one key, not a host and a port, on purpose: a key
named `database.port` would collide with the `PANDOKOS_DATABASE_PORT` variable that Kubernetes
injects for a Service named `pandokos-database`. The database is described in the
[database guide](database.md).

## Purposes

One configuration file serves three groups of commands. What differs is which secret files are
required and read:

| | `serve`, `config check` (`--for serve`, the default) | `migrate`, `migrate status`, `config check --for migrate` | `init`, `config check --for init` |
|---|---|---|---|
| `vault.kek.local.key_file`, `vault.kek.local.unwrap_key_files` | read; `key_file` required with the provider `local`, and refused with `transit` | not read | not read |
| `vault.kek.transit.*` (the token, the role ID and the secret ID files, the CA file) | read when set; required by the method chosen | not read | not read |
| `database.gateway.password_file`, `database.worker.password_file` | required, read | not read | not read |
| `database.admin.password_file` | required, read | not read | required, read |
| `database.owner.password_file` | **not read**, set or not; the file may be absent | required, read | not read |
| listener certificate and key files | checked and loaded | format rules only, files not opened | format rules only, files not opened |

So the owner's password file needs to exist only where `migrate` runs, and `serve` never has the
credential that can change the schema. Every format rule below applies to both purposes.

## Secret files

There is no key that holds a secret value: a key such as `vault.kek.local.key: ...` is an unknown key and
fails. A secret file is read once, at start; changing it needs a restart.

- Leading and trailing white space (including the final newline) is removed.
- The file must not be larger than 8192 bytes, must not be empty after trimming, and must not
  contain control characters, because the value goes into a header.
- Error messages name the file, never its content.

With Docker or Kubernetes, mount the secret as a file and point the key at it:

```
docker run -v ./secrets:/run/secrets:ro \
  -e PANDOKOS_VAULT_KEK_LOCAL_KEY_FILE=/run/secrets/pandokos_kek \
  ghcr.io/pandokos/pandokos:<version>
```

In Kubernetes, mount a `Secret` as a volume and use the path of the mounted file.

## Validation

All problems are reported together, not only the first. Each is attached to a key.

| Key | Rule |
|---|---|
| `log.level` | one of `debug`, `info`, `warn`, `error` |
| `listeners.inference.address` | `host:port`; the port is an integer 0 to 65535 (0 lets the system choose); the host is empty, an IP address or a host name |
| every duration | greater than zero |
| `listeners.inference.max_header_bytes` | 4096 to 1048576 |
| `limits.max_request_body_bytes` | 1 to 104857600 |
| `limits.default_output_tokens` | 1 to 1000000 |
| `limits.time_zone` | an IANA zone name known to the embedded database, whose offsets are whole minutes |
| `limits.on_unknown_price`, `limits.on_store_failure` | `refuse` or `allow` |
| `limits.reconcile_interval` | 10s to 1h |
| `cache.limits_ttl` | greater than zero, at most 5m (and at least 1s) |
| `database.address` | set; `host:port`; the port is an integer 1 to 65535; the host is an IP address or a host name |
| `database.name`, `database.gateway.user`, `database.admin.user`, `database.worker.user`, `database.owner.user` | 1 to 63 characters: letters, digits, underscore, dot, hyphen; not starting with a dot or a hyphen |
| `database.tls.mode` | `verify-full` or `disable` |
| `database.tls.ca_file` | empty unless the mode is `verify-full`; when set, a readable file of at most 1048576 bytes with at least one PEM certificate |
| `database.admin.user`, `database.worker.user`, `database.owner.user` | differs from every user that precedes it in the order gateway, admin, worker, owner: each role needs its own database user |
| `database.gateway.max_connections`, `database.admin.max_connections`, `database.worker.max_connections` | 1 to 1000 |
| the password files the purpose needs | set and readable (see Purposes) |
| `upstream.tls.ca_file` | when set (`serve` only): a readable file with at least one PEM certificate ("cannot be read", "holds no certificate"); no message quotes the content |
| `upstream.proxy.url` | when set (`serve` only): scheme `http` or `https`, a host, no user information, no query, no fragment, path empty or `/` |
| `listeners.admin.address` | as the inference address; differs from `listeners.inference.address` unless the port is 0; a host that is not loopback needs TLS or `listeners.admin.allow_plaintext` |
| `listeners.admin.max_header_bytes` | 4096 to 1048576 |
| `listeners.<l>.tls.min_version` | `TLS1.2` or `TLS1.3` |
| `listeners.<l>.tls.cert_file`, `listeners.<l>.tls.key_file` | both set or both empty; for `serve`: readable, at most 1048576 bytes, a valid pair, valid now |
| `cache.principal_ttl` | 1s to 60s |
| `cache.credential_ttl` | 1s to 60s |
| `cache.data_key_ttl` | 1s to 15m |
| `cache.catalog_ttl` | 1s to 5m (checked for every purpose) |
| `pricing.default_currency` | three capital letters (`must be three capital letters`) |
| `pricing.refresh.interval` | 1h to 720h; checked for `serve` and `config check --for serve`, also while the refresh is disabled |
| `pricing.refresh.url` | a valid `https` URL with a host and no user information; checked for `serve` as the interval is; no message quotes the URL |
| `vault.kek.provider` | `local` or `transit` |
| `vault.kek.transit.address` | required with `provider: transit`; `https`, or `http` for a loopback host only; a host and an optional port, no user information, path, query or fragment. Any other `vault.kek.transit.*` key set without an address, at a value that is not its default, is refused; `serve` and `kek check` only |
| `vault.kek.transit.namespace`, `.mount`, `.key_name`, `.auth.approle.mount` | namespace and mounts: segments of letters, digits, `_` and `-` joined by `/` (255 and 128 characters at most); the key name `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`, required with an address |
| `vault.kek.transit.auth.*` | `method` is `token` or `approle`, required with an address; `token_file` is required with `token` and refused with `approle`, and `role_id_file` and `secret_id_file` the other way round; each file is readable, non-empty and holds no control character; no message quotes a content |
| `vault.kek.transit.tls.ca_file` | a readable PEM file with at least one certificate; the only authorities trusted for the key service; a failure says only that the file cannot be read |
| `vault.kek.transit.request_timeout`, `.max_concurrent_requests`, `.start_timeout` | 100ms to 30s; 1 to 1024; 0 to 1h (0 waits without limit) |
| `vault.kek.local.key_file` with `provider: transit` | must be empty; list the file in `vault.kek.local.unwrap_key_files` while data keys are re-wrapped |
| `vault.kek.local.key_file` | set; a readable regular file holding 32 bytes in standard base64 (44 characters); no message quotes the content |
| `vault.kek.local.unwrap_key_files` | at most 8 entries, each a file as `key_file`; no entry equals `key_file` or another entry in content or path; `serve` only; no message quotes a content |
| `tenant_deletion.grace_period`, `tenant_deletion.interval` | `0` to `2160h`; `10s` to `1h` (`serve` only) |
| `database.direct_address` | when set, the three address rules of `database.address` |
| `ledger.flush_interval`, `ledger.shutdown_flush_timeout` | 1ms to 10s; 1s to 5m (`serve` only) |
| `ledger.batch_size`, `ledger.buffer_size` | 1 to 5000; 100 to 1000000 and at least the batch size (`serve` only) |
| `telemetry.otlp.endpoint` | when set (`serve` only): scheme `http` or `https`, a host, no user information, no query, no fragment; no message quotes the URL |
| `telemetry.otlp.protocol` | `http/protobuf` |
| `telemetry.otlp.headers_file` | when set: a readable file of at most 131072 bytes; lines `Name: value`, blank lines and lines starting with `#` ignored; at most 16 headers; names are HTTP tokens and none of `Host`, `Content-Type`, `Content-Length`, `Content-Encoding`, `User-Agent`; values 1 to 4096 printable ASCII bytes. A problem names the line, never its content |
| `telemetry.otlp.timeout` | 1s to 60s |
| `telemetry.traces.sample_ratio` | 0 to 1 |
| `telemetry.metrics.export_interval` | 5s to 10m, and at least `telemetry.otlp.timeout` |
| `telemetry.resource.attributes` | at most 32 `key=value` pairs; keys `[a-z][a-z0-9._-]{0,62}`, values 1 to 256 printable ASCII bytes without `,` and `=` |
| `tokens.max_token_bytes` | 1024 to 16384, and at most half of `listeners.inference.max_header_bytes`; `serve` refuses to start otherwise (all `tokens.*` rules are checked for `serve` and `config check --for serve`) |
| `tokens.clock_skew` | 0 to 5m (zero allowed) |
| `tokens.max_lifetime` | 1m to 720h |
| `tokens.private_hosts` | at most 32 entries, each a lower-case DNS name (letters, digits, hyphens, dots; at least one letter): no address literal, no wildcard, no port; no message quotes a host name |
| `tokens.jwks.fetch_timeout`, `tokens.jwks.refresh_interval`, `tokens.jwks.min_refetch_interval` | 1s to 30s; 1m to 1h; 1s to 5m |
| `reports.rollup_interval` | 10s to 1h |
| `events.source` | a URI reference of 1 to 255 printable characters |
| `events.retention` | `24h` to `2160h`, whole hours |
| `events.relay.interval`, `events.relay.workers`, `events.relay.batch_size` | `100ms` to `1m`; 1 to 64; 1 to 1000 |
| `events.delivery.timeout`, `events.delivery.max_attempts`, `events.delivery.backoff_max` | `1s` to `30s`; 1 to 100; `5s` to `24h` |
| `events.webhook.private_hosts` | at most 32 lower-case DNS names, no address literal, no wildcard, no port; lower-cased by the loader |
| `retention.usage_months` | 0, or 2 to 120 |
| `retention.audit_months` | 0, or 1 to 1200 |
| `tokens.jwks.max_stale` | at least twice `tokens.jwks.refresh_interval`, at most 72h |

## Warnings

Warnings are printed by `config check` and logged by `serve` (message `configuration warning`);
they do not prevent start.

| Key | Condition | Message |
|---|---|---|
| `upstream.proxy.url` | `HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY` (or the lower-case name) is set in the environment and the key is not (`serve` only) | `<VARIABLE>` is set in the environment and is not used; set upstream.proxy.url to send provider traffic through a proxy |
| `telemetry.resource.attributes` | `OTEL_RESOURCE_ATTRIBUTES` is set in the environment (`serve` only) | `OTEL_RESOURCE_ATTRIBUTES` is set in the environment: the OpenTelemetry SDK adds its attributes to the resource; they never replace those Pandokos sets. The value is never shown |
| `telemetry.otlp.endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set in the environment and the key is not (`serve` only) | `<VARIABLE>` is set in the environment and is not used; set telemetry.otlp.endpoint to export traces |
| `telemetry.otlp.endpoint` | `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` is set in the environment and the key is not (`serve` only) | `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` is set in the environment and is not used; set telemetry.otlp.endpoint to export metrics |
| `telemetry.metrics.export_interval` | `OTEL_METRIC_EXPORT_INTERVAL` is set in the environment (`serve` only) | `OTEL_METRIC_EXPORT_INTERVAL` is set in the environment and is ignored; set telemetry.metrics.export_interval |
| `telemetry.otlp.timeout` | `OTEL_METRIC_EXPORT_TIMEOUT` is set in the environment (`serve` only) | `OTEL_METRIC_EXPORT_TIMEOUT` is set in the environment and is ignored; set telemetry.otlp.timeout |
| `telemetry.metrics.export_interval` | the interval is not the default while no OTLP export runs (no endpoint, or `telemetry.metrics.otlp` false) | has no effect without telemetry.otlp.endpoint and telemetry.metrics.otlp |
| `listeners.inference.address` | no TLS and the host is not a loopback address or `localhost` | the listener serves plain HTTP; set listeners.inference.tls.cert_file and key_file, or terminate TLS in front of it |
| `listeners.admin.address` | no TLS, host not loopback, `allow_plaintext` true | the administration API is served without TLS; keys travel in clear text unless TLS ends in front of it |
| `listeners.admin.allow_plaintext` | true while TLS is configured or the host is loopback | has no effect |
| `listeners.<l>.tls.cert_file` | the certificate expires in less than 14 days | the certificate expires on `<date>` |
| `database.tls.mode` | the mode is `disable` and the host of `database.address` is not a loopback address or `localhost` | the database connection is not encrypted |
| `vault.kek.local.key_file` | the file grants any permission to other users (mode `& 0007`) | the file can be accessed by other users; restrict it to the service user (chmod 600) |
| `vault.kek.local.unwrap_key_files` | an entry grants any permission to other users (mode `& 0007`) | the file can be accessed by other users; restrict it to the service user (chmod 600) |
| `vault.kek.transit.address` | the address is `http` | the connection to the key service is not encrypted |
| `vault.kek.transit.auth.token_file`, `.approle.role_id_file`, `.approle.secret_id_file` | the file grants any permission to other users (mode `& 0007`) | the file can be accessed by other users; restrict it to the service user (chmod 600) |
| `support.enabled` | false | support actions are off: a tenant that loses its administrators cannot be recovered through the API |
| `tokens.private_hosts` | the list is not empty (`serve` only) | issuer and key URLs of these hosts may resolve to private addresses: `<hosts>` |
| `tokens.jwks.max_stale` | more than 24h | keys of an issuer are used for more than 24h after the last successful fetch: a key withdrawn by the issuer stays trusted that long while it is unreachable |
| `retention.audit_months` | between 1 and 11 | audit rows older than the value are removed for good: a value below 12 months keeps less than a year of the log |
| `environment` | an unknown `PANDOKOS_` variable | unknown variable `<NAME>` ignored (the value is never printed) |

Variables starting with `PANDOKOS_SERVICE_` or `PANDOKOS_PORT` are ignored silently: Kubernetes
injects them for a Service named `pandokos`. So are the variables of that form for a Service whose
name ends in another word, such as `pandokos-database`: a name that matches
`^PANDOKOS_[A-Z0-9_]*(_SERVICE_HOST|_SERVICE_PORT(_[A-Z0-9_]+)?|_PORT|_PORT_[0-9]+_(TCP|UDP)(_(PROTO|PORT|ADDR))?)$`.
No configuration key has `port` as its last segment or a segment that starts with `service_`, so
none of them can collide. Every other unknown `PANDOKOS_` variable still produces the warning.
`PANDOKOS_CONFIG` is the file path.

## Checking a configuration

```
pandokos config check [--config FILE] [--for serve|migrate]
```

It loads and validates the configuration exactly as the command named by `--for` does (`serve` by
default), including reading the secret files, and opens no socket or database connection. It prints `configuration OK` to standard output and each warning as
`warning: <key>: <message>` to standard error. On failure it prints each problem as
`error: <key>: <message>` to standard error.

| Exit code | Meaning |
|---|---|
| 0 | the configuration is valid |
| 1 | the configuration is invalid |
| 2 | wrong command line, including a `--for` value other than `serve` or `migrate` |

## Example

```yaml
log:
  level: info
listeners:
  inference:
    address: 0.0.0.0:8080
limits:
  max_request_body_bytes: 10485760
  max_request_duration: 10m
shutdown:
  drain_timeout: 30s
database:
  address: db.internal:5432
  name: pandokos
  tls:
    mode: verify-full
    ca_file: /run/secrets/database_ca.pem
  gateway:
    user: acme_gateway
    password_file: /run/secrets/db_gateway
  admin:
    user: acme_admin
    password_file: /run/secrets/db_admin
  worker:
    user: acme_worker
    password_file: /run/secrets/db_worker
  owner:
    user: acme_owner
    password_file: /run/secrets/db_owner
vault:
  kek:
    local:
      key_file: /run/secrets/pandokos_kek
cache:
  catalog_ttl: 30s
pricing:
  default_currency: USD
  import_at_start: true
  refresh:
    enabled: false
    url: https://raw.githubusercontent.com/pydantic/genai-prices/<commit>/prices/new_data/v2/data.json
    interval: 24h
upstream:
  connect_timeout: 10s
  tls:
    ca_file: /run/secrets/upstream_ca.pem
  proxy:
    url: http://proxy.internal.example:3128
```

## Variables of the API commands

`pandokos tenant`, `service-account`, `key`, `platform-key`, `credential`, `tenant-credential`, `provider`, `endpoint`, `model`, `price`, `price-import`, `price-list`, `tenant-model`, `model-restriction` and `whoami` are clients of the
administration API. They read `PANDOKOS_CLI_SERVER` (default `http://127.0.0.1:8081`),
`PANDOKOS_CLI_KEY_FILE` (required) and `PANDOKOS_CLI_CA_FILE`. These variables are not part of the
server configuration and cause no warning. No key of the server configuration starts with `cli`.
