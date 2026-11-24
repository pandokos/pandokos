# 0038. Egress guard, upstream TLS and proxy

- Status: accepted
- Date: 2026-11-14

Refines [ADR 0009](0009-native-passthrough-by-api-family.md),
[ADR 0019](0019-configuration.md), [ADR 0035](0035-catalogue-and-model-access.md) and
[ADR 0036](0036-prices-costs-and-importer.md).

## Context

GW-05 and GW-06 ask that requests go only to catalogue destinations, that redirects are not
followed, and that private, loopback and link-local addresses are refused unless an endpoint
is marked private, with the check made on the address actually dialled. Endpoints are written
as names, which resolve later and can be made to resolve anywhere. Three parts of the product
connect outwards: provider calls, credential validation and the price download. Enterprises
route outbound traffic through a proxy.

## Decision

1. Every outbound HTTP connection of the server is made by one package. No other package
   builds a transport or a dialer.
2. Addresses are classified as public, private (loopback, RFC 1918, carrier-grade NAT,
   link-local, unique-local, documentation and benchmarking ranges) or forbidden (unspecified,
   multicast, reserved, and the instance-metadata addresses). IPv4-mapped addresses are
   unmapped; NAT64 and 6to4 addresses take the class of the IPv4 address they embed.
3. There are two transports with separate connection pools. The one for public endpoints
   accepts `https` only and connects to public addresses only. The one for endpoints marked
   private also accepts `http` and private addresses. Forbidden addresses are refused by both.
4. The class is checked in the dialer's control hook, after name resolution, for every
   connection attempt, on the literal address about to be connected; address literals are
   also refused before dialling.
5. Redirects are never followed. Certificates are always verified, with TLS 1.2 or later;
   there is no switch to turn verification off. `upstream.tls.ca_file` adds certificate
   authorities to the system roots.
6. Proxy variables of the environment are never used. `upstream.proxy.url` sends the traffic
   of public endpoints through an HTTP proxy with `CONNECT`; private endpoints always connect
   directly. Through a proxy, the connection that is checked is the one to the proxy; target
   literals are still classified, target names are resolved by the proxy.
7. Credential validation and the price download use the same transports.

## Consequences

A name that resolves to an internal address is refused when it is dialled, whatever it
resolved to when the endpoint was written. A provider reached over private addressing must be
declared private. Behind a proxy the operator's proxy is the control point for name-based
destinations. A private endpoint cannot be on a metadata address.

## Alternatives considered

Resolving and checking before dialling, then dialling the name (the second resolution can
differ); pinning the resolved address into the connection by hand (duplicates the dialer and
breaks TLS server names and happy eyeballs); one transport with a per-request policy in the
context (a pooled connection made under the private rule could serve a public endpoint of the
same host); honouring `HTTPS_PROXY` and `NO_PROXY` (ambient settings would move credentials
without a validated key); a per-endpoint CA (a schema change nobody asked for).

## History

- 2026-11: sprint 4.1 amends rule 7: credential validation, the price download and the retrieval of the keys of trusted issuers use the same transports ([ADR 0046](0046-issuer-key-retrieval-and-caching.md)); the public transport is used unless the operator lists the host in `tokens.private_hosts`.
- 2026-11-18: accepted by the maintainer after review.
- 2026-11-23: sprint 6.4 adds a fourth caller of the egress guard, the delivery of events to webhook destinations: the public transport, no redirect, the private transport only for hosts of `events.webhook.private_hosts` ([ADR 0071](0071-webhook-destinations-signature-secrets-and-egress.md)).
- 2026-11-24: sprint 12.1 amends point 7: the key service is a fourth consumer of the guard, on a dedicated transport (`https` only, certificate always verified, no proxy, no redirect, private addresses accepted because the address is the operator's) ([ADR 0073](0073-the-transit-key-provider.md)).
