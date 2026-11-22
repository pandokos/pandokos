# 0046. Retrieval and caching of issuer keys

- Status: accepted
- Date: 2026-11-16

Amends [ADR 0038](0038-egress-guard-upstream-tls-and-proxy.md) (rule 7). Refines
[ADR 0005](0005-data-plane-authentication.md) and [ADR 0018](0018-supply-chain.md).

## Context

Tenant administrators supply the URLs that the gateway fetches to verify tokens. Identity providers are
often on private networks. An outage of the provider must not stop calls, and a key that the provider
withdraws must stop being trusted within a known time.

## Decision

1. **Library.** `github.com/go-jose/go-jose/v4`, root package only, for compact JWS and JWK. Pandokos
   decodes and checks the claims itself. It is imported by `internal/authn`, `internal/jwks` and
   `test/fakeidp` only.
2. **Where from.** An explicit `jwks_uri`, or OpenID Connect discovery at
   `<issuer>/.well-known/openid-configuration` (the `issuer` member must equal the registered issuer
   and `jwks_uri` must be `https` without user information). Both are fetched at every refresh.
3. **How.** Through `egress.Guard.Transport`, never another transport. `https` always, certificates
   verified (`upstream.tls.ca_file` adds roots), no redirect followed, the proxy of `upstream.proxy.url`
   for public addresses. The public class is used unless the URL's host is listed in
   `tokens.private_hosts` (an operator decision: lower-case DNS names, no address literal, at most 32),
   in which case the private transport is used, still `https` only. Forbidden addresses stay
   unreachable. Limits: 5 s for the exchange, 64 KiB for a discovery document, 256 KiB for a key set, 32
   usable keys kept, status 200 only. A key set member that does not parse is skipped.
4. **When.** Lazily: the first token for a source (the pair `issuer`, `jwks_uri`) fetches, and
   concurrent callers wait for that one fetch. After `tokens.jwks.refresh_interval` (15 min) the next use
   refreshes in the background and is served from memory. Cache headers of the issuer are not read.
5. **Unknown key ID.** One fetch, shared, if the last attempt for the source is older than
   `tokens.jwks.min_refetch_interval` (10 s); otherwise the token is refused at once.
6. **Outage.** A failed refresh keeps the keys. Keys are used for at most `tokens.jwks.max_stale` (6 h)
   after the last success; then the source has no keys and a call with a token is `503 unavailable`, not
   `401`: the credential cannot be verified, which is not a statement about it. At most 1024 sources are
   kept.
7. **Logs.** Failures are logged with a class (`blocked`, `timeout`, `status`, `too_large`,
   `invalid_document`, `issuer_mismatch`, `no_usable_key`, `network`) and the issuer, never the body of
   a document; an issuer whose host is an address literal is logged as `(address)`. Only the gateway
   fetches.

ADR 0038 rule 7 now reads: credential validation, the price download and the retrieval of issuer keys use
the same transports. The cache table of the overview replaces "as the issuer's cache headers say, at most
1 h" with the bounds above.

## Consequences

- What a tenant administrator can make the gateway do with a registration is one `GET` of two URLs of
  fixed shape on a public address, or on a host the operator listed, at most every 10 s per source, with
  nothing of the answer observable except whether tokens verify.
- A withdrawn key is trusted for at most 15 min, or 6 h while the provider is unreachable.
- A private issuer needs an operator's action, and a warning is logged at start for each such host.
- The registration is not checked when it is created: a wrong URL shows at the first token.

## Alternatives considered

**`go-oidc`.** Builds its own HTTP client and refetches without a bound under our control.
**A hand-written JWS verifier.** No dependency, but signature code that we would own.
**Honouring cache headers.** Lets the issuer set the bound on withdrawal.
**A private flag on each registration.** Puts the decision in the hands of the tenant administrator who
supplies the address.
**Fetching at registration time from the administration plane.** Would give the control plane an
outbound path and a way to probe addresses with an answer.

## History

Sprint 4.1.
- 2026-11-18: accepted by the maintainer after review.
- 2026-11-22: sprint 4.2 gives the admin role a key cache and lets it fetch documents, only of the issuer the operator configured for sign-in (`auth.oidc.issuer`) and, with `tokens.admin_listener`, of registered issuers ([ADR 0058](0058-openid-connect-relying-party.md)).
