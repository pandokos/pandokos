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

The path is, in order of precedence: the `--config FILE` flag of `pandokos serve` and
`pandokos config check`, the `PANDOKOS_CONFIG` environment variable, or none. A path that is given
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
| `limits.max_request_body_bytes` | `PANDOKOS_LIMITS_MAX_REQUEST_BODY_BYTES` | integer | `10485760` | Larger bodies get 413 `request_too_large` |
| `limits.request_body_read_timeout` | `PANDOKOS_LIMITS_REQUEST_BODY_READ_TIMEOUT` | duration | `30s` | Time allowed to read the request body |
| `limits.max_request_duration` | `PANDOKOS_LIMITS_MAX_REQUEST_DURATION` | duration | `10m` | Total time for one proxied request, stream included, measured from the end of the body read |
| `upstream.connect_timeout` | `PANDOKOS_UPSTREAM_CONNECT_TIMEOUT` | duration | `10s` | TCP connect timeout, and separately TLS handshake timeout, towards the provider |
| `shutdown.drain_timeout` | `PANDOKOS_SHUTDOWN_DRAIN_TIMEOUT` | duration | `30s` | Time in-flight requests get to finish after a termination signal |
| `skeleton.access_token_file` | `PANDOKOS_SKELETON_ACCESS_TOKEN_FILE` | path | none, required | File with the static token callers must present |
| `skeleton.openai.base_url` | `PANDOKOS_SKELETON_OPENAI_BASE_URL` | URL | `https://api.openai.com/v1` | Provider base URL, in the form SDKs use (ends with `/v1`) |
| `skeleton.openai.api_key_file` | `PANDOKOS_SKELETON_OPENAI_API_KEY_FILE` | path | none, required | File with the provider API key |

Durations are strings in Go syntax (`"30s"`, `"10m"`); a YAML number is rejected for a duration.
Sizes are integers in bytes.

## Secret files

There is no key that holds a secret value: `skeleton.openai.api_key: ...` is an unknown key and
fails. A secret file is read once, at start; changing it needs a restart.

- Leading and trailing white space (including the final newline) is removed.
- The file must not be larger than 8192 bytes, must not be empty after trimming, and must not
  contain control characters, because the value goes into a header.
- The access token must be at least 32 bytes long and must differ from the provider key (otherwise
  the provider key would be accepted as an inbound credential).
- Error messages name the file, never its content.

With Docker or Kubernetes, mount the secret as a file and point the key at it:

```
docker run -v ./secrets:/run/secrets:ro \
  -e PANDOKOS_SKELETON_ACCESS_TOKEN_FILE=/run/secrets/access_token \
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
| `skeleton.openai.base_url` | an absolute URL with scheme `http` or `https`, a host, no user information, no query, no fragment; a trailing `/` on the path is removed |
| `skeleton.access_token_file` | set; readable; the token is at least 32 bytes; it differs from the provider key |
| `skeleton.openai.api_key_file` | set; readable |

## Warnings

Warnings are printed by `config check` and logged by `serve` (message `configuration warning`);
they do not prevent start.

| Key | Condition | Message |
|---|---|---|
| `skeleton.openai.base_url` | the scheme is `http` and the host is not a loopback address or `localhost` | the provider key is sent without TLS |
| `listeners.inference.address` | the host is not a loopback address or `localhost` | the listener serves plain HTTP; terminate TLS in front of it |
| `environment` | an unknown `PANDOKOS_` variable | unknown variable `<NAME>` ignored (the value is never printed) |

Variables starting with `PANDOKOS_SERVICE_` or `PANDOKOS_PORT` are ignored silently: Kubernetes
injects them for a Service named `pandokos`. `PANDOKOS_CONFIG` is the file path.

## Checking a configuration

```
pandokos config check [--config FILE]
```

It loads and validates the configuration exactly as `serve` does, including reading the secret
files, and opens no socket. It prints `configuration OK` to standard output and each warning as
`warning: <key>: <message>` to standard error. On failure it prints each problem as
`error: <key>: <message>` to standard error.

| Exit code | Meaning |
|---|---|
| 0 | the configuration is valid |
| 1 | the configuration is invalid |
| 2 | wrong command line |

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
skeleton:
  access_token_file: /run/secrets/access_token
  openai:
    base_url: https://api.openai.com/v1
    api_key_file: /run/secrets/openai_api_key
```

## The `skeleton` section is temporary

The `skeleton` section belongs to the walking skeleton. The access token goes away when principals
and virtual keys arrive (sprint 1.3), and the provider endpoint and key go away when the catalogue
and the vault replace them (sprint 3.1). Do not build on it.
