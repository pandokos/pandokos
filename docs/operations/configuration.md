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
| `limits.max_request_body_bytes` | `PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES` | integer | `10485760` | Larger bodies get 413 `request_too_large` |
| `limits.request_body_read_timeout` | `PANDOKOS_LIMITS_REQUEST_BODY_READ_TIMEOUT` | duration | `30s` | Time allowed to read the request body |
| `limits.max_request_duration` | `PANDOKOS_LIMITS_MAX_REQUEST_DURATION` | duration | `10m` | Total time for one proxied request, stream included, measured from the end of the body read |
| `upstream.connect_timeout` | `PANDOKOS_UPSTREAM_CONNECT_TIMEOUT` | duration | `10s` | TCP connect timeout, and separately TLS handshake timeout, towards the provider |
| `shutdown.drain_timeout` | `PANDOKOS_SHUTDOWN_DRAIN_TIMEOUT` | duration | `30s` | Time in-flight requests get to finish after a termination signal |
| `cache.principal_ttl` | `PANDOKOS_CACHE_PRINCIPAL_TTL` | duration | `30s` | Lifetime of a cached principal in the gateway: the upper bound on the time a revocation or a suspension takes to reach the inference listener (1s to 60s) |
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
| `skeleton.openai.base_url` | `PANDOKOS_SKELETON_OPENAI_BASE_URL` | URL | `https://api.openai.com/v1` | Provider base URL, in the form SDKs use (ends with `/v1`) |
| `skeleton.openai.api_key_file` | `PANDOKOS_SKELETON_OPENAI_API_KEY_FILE` | path | none, required | File with the provider API key |

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
| `skeleton.openai.api_key_file` | required, read | not read | not read |
| `database.gateway.password_file`, `database.worker.password_file` | required, read | not read | not read |
| `database.admin.password_file` | required, read | not read | required, read |
| `database.owner.password_file` | **not read**, set or not; the file may be absent | required, read | not read |
| listener certificate and key files | checked and loaded | format rules only, files not opened | format rules only, files not opened |

So the owner's password file needs to exist only where `migrate` runs, and `serve` never has the
credential that can change the schema. Every format rule below applies to both purposes.

## Secret files

There is no key that holds a secret value: `skeleton.openai.api_key: ...` is an unknown key and
fails. A secret file is read once, at start; changing it needs a restart.

- Leading and trailing white space (including the final newline) is removed.
- The file must not be larger than 8192 bytes, must not be empty after trimming, and must not
  contain control characters, because the value goes into a header.
- Error messages name the file, never its content.

With Docker or Kubernetes, mount the secret as a file and point the key at it:

```
docker run -v ./secrets:/run/secrets:ro \
  -e PANDOKOS_SKELETON_OPENAI_API_KEY_FILE=/run/secrets/openai_api_key \
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
| `database.address` | set; `host:port`; the port is an integer 1 to 65535; the host is an IP address or a host name |
| `database.name`, `database.gateway.user`, `database.admin.user`, `database.worker.user`, `database.owner.user` | 1 to 63 characters: letters, digits, underscore, dot, hyphen; not starting with a dot or a hyphen |
| `database.tls.mode` | `verify-full` or `disable` |
| `database.tls.ca_file` | empty unless the mode is `verify-full`; when set, a readable file of at most 1048576 bytes with at least one PEM certificate |
| `database.admin.user`, `database.worker.user`, `database.owner.user` | differs from every user that precedes it in the order gateway, admin, worker, owner: each role needs its own database user |
| `database.gateway.max_connections`, `database.admin.max_connections`, `database.worker.max_connections` | 1 to 1000 |
| the password files the purpose needs | set and readable (see Purposes) |
| `skeleton.openai.base_url` | an absolute URL with scheme `http` or `https`, a host, no user information, no query, no fragment; a trailing `/` on the path is removed |
| `listeners.admin.address` | as the inference address; differs from `listeners.inference.address` unless the port is 0; a host that is not loopback needs TLS or `listeners.admin.allow_plaintext` |
| `listeners.admin.max_header_bytes` | 4096 to 1048576 |
| `listeners.<l>.tls.min_version` | `TLS1.2` or `TLS1.3` |
| `listeners.<l>.tls.cert_file`, `listeners.<l>.tls.key_file` | both set or both empty; for `serve`: readable, at most 1048576 bytes, a valid pair, valid now |
| `cache.principal_ttl` | 1s to 60s |
| `database.direct_address` | when set, the three address rules of `database.address` |
| `skeleton.openai.api_key_file` | set; readable |

## Warnings

Warnings are printed by `config check` and logged by `serve` (message `configuration warning`);
they do not prevent start.

| Key | Condition | Message |
|---|---|---|
| `skeleton.openai.base_url` | the scheme is `http` and the host is not a loopback address or `localhost` | the provider key is sent without TLS |
| `listeners.inference.address` | no TLS and the host is not a loopback address or `localhost` | the listener serves plain HTTP; set listeners.inference.tls.cert_file and key_file, or terminate TLS in front of it |
| `listeners.admin.address` | no TLS, host not loopback, `allow_plaintext` true | the administration API is served without TLS; keys travel in clear text unless TLS ends in front of it |
| `listeners.admin.allow_plaintext` | true while TLS is configured or the host is loopback | has no effect |
| `listeners.<l>.tls.cert_file` | the certificate expires in less than 14 days | the certificate expires on `<date>` |
| `database.tls.mode` | the mode is `disable` and the host of `database.address` is not a loopback address or `localhost` | the database connection is not encrypted |
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
skeleton:
  openai:
    base_url: https://api.openai.com/v1
    api_key_file: /run/secrets/openai_api_key
```

## The `skeleton` section is temporary

The `skeleton` section belongs to the walking skeleton. The access token went away with sprint 1.3
(virtual keys, [keys](keys.md)); the provider endpoint and key go away when the catalogue and the
vault replace them (sprint 3.1). Do not build on it.

## Variables of the API commands

`pandokos tenant`, `service-account`, `key`, `platform-key` and `whoami` are clients of the
administration API. They read `PANDOKOS_CLI_SERVER` (default `http://127.0.0.1:8081`),
`PANDOKOS_CLI_KEY_FILE` (required) and `PANDOKOS_CLI_CA_FILE`. These variables are not part of the
server configuration and cause no warning. No key of the server configuration starts with `cli`.
