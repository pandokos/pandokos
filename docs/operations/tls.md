# TLS

Both listeners can serve TLS themselves, or run behind a proxy that terminates it.

## The listeners

| Key | Meaning |
|---|---|
| `listeners.inference.tls.cert_file`, `listeners.admin.tls.cert_file` | PEM file with the server certificate and any intermediate certificates |
| `listeners.inference.tls.key_file`, `listeners.admin.tls.key_file` | PEM file with the private key |
| `listeners.inference.tls.min_version`, `listeners.admin.tls.min_version` | `TLS1.2` (default) or `TLS1.3` |
| `listeners.admin.allow_plaintext` | acknowledges plain HTTP on a non-loopback address of the administration API, because TLS ends in front of it |

Set both files of a listener to turn TLS on; setting one without the other is an error. At start
`serve` checks that the files are readable, form a pair and are valid now, and warns 14 days before
the certificate expires. Certificates are read once, at start: a renewed certificate needs a
restart. No client certificate is requested and the cipher suites are those of the Go standard
library.

The administration API listens on `127.0.0.1:8081` by default. It refuses a non-loopback address
without TLS unless `listeners.admin.allow_plaintext` is true, which warns that keys travel in clear
text unless TLS ends in front of it.

## Towards providers

Every outbound TLS connection of the server (provider calls, credential validation, the price download,
an `https` proxy) verifies the certificate chain and the host name, with TLS 1.2 or later; there is no
setting that turns verification off. `upstream.tls.ca_file` (`PANDOKOS_UPSTREAM_TLS_CA_FILE`) names a PEM
file whose certificates are added to the system roots, for a provider or a proxy behind a private
authority. The file is read when `serve` starts: restart to reload it. See the
[gateway guide](gateway.md).

## Towards PostgreSQL

`database.tls.mode` is `verify-full` (the default: the certificate chain and the host name are
verified) or `disable`. With a private authority set `database.tls.ca_file` to its PEM file; the
host name in `database.address` must be in the certificate. See the [database guide](database.md).
The notification connection uses the same settings.
