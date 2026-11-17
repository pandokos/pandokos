# Tokens from trusted issuers

A workload that already holds a signed token from its identity provider can call a model through
Pandokos with that token as its "API key". This page says when to prefer that to a virtual key, how to
register an identity provider, how a token becomes a service account, what is checked and in which
order, how to choose the audience, what happens when the identity provider is down, and how to read a
refusal. It applies from sprint 4.1 ([ADR 0045](../adr/0045-trusted-token-issuers.md),
[ADR 0046](../adr/0046-issuer-key-retrieval-and-caching.md)). Tokens are accepted on the inference
listener only; the administration API takes virtual keys and platform keys.

## 1. Tokens or keys

| | Virtual key | Token of a trusted issuer |
|---|---|---|
| Who holds the secret | Pandokos issues it; you store it | your identity provider issues short-lived ones; the workload fetches them |
| Rotation | create a second key, deploy, revoke the first | none for you: the provider rotates its signing keys |
| Revoking one credential | revoke the key | not possible for one token: delete the mapping, or wait for `exp` (section 9) |
| Needs | nothing | an identity provider whose key set Pandokos can reach |
| Limits and pins per credential | scopes, model list, pin to a provider credential | none: the principal is the service account with its roles |

Prefer a token when the workload already runs under an identity (a Kubernetes service account, a CI
system, a company-wide realm) and you do not want a long-lived secret in its configuration. Prefer a
key when there is no identity provider, or when you need a scope list, a model list or a pinned
credential per caller. Both can be used in one tenant, for the same service account.

## 2. The two kinds of registration

| | Tenant-bound | Platform-level |
|---|---|---|
| Created by | a tenant administrator (`token_issuer:write`) | a platform administrator (`platform_issuer:write`) |
| Tenant of a token | the tenant that owns the registration | the value of the claim named by `tenant_claim`, looked up in the registration's list of tenants |
| Use it when | one tenant has its own identity provider, or its own client of a shared one | one identity provider serves many tenants and says in each token which one |
| Wildcard | none | none: a tenant that is not in the list cannot be reached by any claim value |

A registration has these members:

| Member | Rule | Default |
|---|---|---|
| `name` | lower case letters, digits and hyphens, at most 63; unique per tenant (per installation for platform-level) | required |
| `issuer` | an `https` URL, no query, no fragment, no user information, at most 255 bytes; compared byte for byte with the `iss` claim | required |
| `audience` | 1 to 255 characters; compared byte for byte with the entries of `aud`. There is no way to skip the audience check | required |
| `jwks_uri` | an `https` URL; empty means OpenID Connect discovery under `issuer` | empty |
| `algorithms` | 1 to 10 of `RS256 RS384 RS512 PS256 PS384 PS512 ES256 ES384 ES512 EdDSA` | `RS256`, `ES256` |
| `subject_claim` | the string claim that identifies the caller | `sub` |
| `required_claims` | up to 16 claims, name to string: the token's claim must be that string, or an array of strings that contains it; the stored size is at most 4096 bytes | none |
| `enabled` | a disabled registration refuses every token | `true` |
| `description` | at most 200 characters | empty |
| `tenant_claim` | platform-level only: the claim that designates the tenant | required |

A claim name is a top-level member of the token's payload, 1 to 64 characters of letters, digits and
`_ . : / -`, matched exactly. There is no path into a nested object. A name that Pandokos itself
checks (`iss`, `aud`, `exp`, `nbf`, `iat`) cannot be a subject, tenant or required claim.

A **subject mapping** says that one subject of one registration acts as one service account of the
tenant. The subject is compared byte for byte; there are no patterns and no prefixes. A mapping cannot
be changed: delete it and create it again. A token whose subject has no mapping is refused. The
principal is the service account, with its roles: authorization, the model check, the vault and the
usage record work as for a virtual key.

### Worked example: a tenant-bound issuer

The tenant `acme` runs its own identity provider at `https://idp.acme.example/`. Its workload
authenticates there as the client `batch`, and the provider puts the client ID in `sub`.

```
pandokos token-issuer create --tenant acme --name acme-idp \
  --issuer https://idp.acme.example/ \
  --audience pandokos.example.com/acme

pandokos token-issuer list --tenant acme --name acme-idp      # the ID of the registration

pandokos subject-mapping create --tenant acme --issuer <issuer-id> \
  --subject batch --service-account batch-sa
```

The CLI takes registrations by ID, never by name. The service account may be given by ID or name.
The workload asks its provider for a token with the audience `pandokos.example.com/acme` and calls:

```
curl https://pandokos.example.com/openai/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}'
```

With the OpenAI SDK the token is the API key:

```python
from openai import OpenAI
client = OpenAI(base_url="https://pandokos.example.com/openai/v1", api_key=os.environ["TOKEN"])
```

With the Anthropic SDK either `api_key` (sent as `X-Api-Key`) or `auth_token` (sent as a bearer)
works, because a token is accepted wherever the API family reads a credential:

```python
from anthropic import Anthropic
client = Anthropic(base_url="https://pandokos.example.com/anthropic", auth_token=os.environ["TOKEN"])
```

A token has a short life. The workload must fetch a new one before `exp`; most SDKs take a fresh
value for each client, so build the client from the current token.

### Worked example: a platform-level issuer

One Keycloak realm serves several tenants. Each client of the realm carries the organisation in the
claim `pandokos_org`, and the client ID is in `azp`. The platform administrator registers the realm
and lists the tenants it may act for, each with the value that designates it:

```
pandokos platform-issuer create --name corp-sso \
  --issuer https://sso.example.com/realms/corp \
  --audience pandokos.example.com \
  --tenant-claim pandokos_org --subject-claim azp

pandokos platform-issuer add-tenant <issuer-id> acme --claim-value acme-corp
```

`--claim-value` defaults to the tenant's ID. The tenant `acme` can now see the registration
(`pandokos token-issuer platform --tenant acme`) and maps its own subjects, with `--platform`:

```
pandokos subject-mapping create --tenant acme --issuer <issuer-id> --platform \
  --subject batch-client --service-account batch-sa
```

A token whose `pandokos_org` is `acme-corp` and whose `azp` is `batch-client` acts for `acme` as
`batch-sa`. Another tenant using the same `azp` value needs its own claim value and its own mapping.
Removing `acme` from the list (`platform-issuer remove-tenant`) stops its tokens at once; the mapping
stays and is inert.

### Worked example: Authentik

In Authentik a workload is a service account with an app password, and the token comes from the
client-credentials grant of an OAuth2/OpenID provider whose "allowed grant types" include
`client_credentials` (empty by default: every request is then `invalid_grant`). The issuer is per
provider (`https://auth.example.com/application/o/<slug>/`, with the slash), and `aud` is the
provider's client ID, so that is the audience to register:

```
pandokos token-issuer create --tenant acme --name authentik \
  --issuer https://auth.example.com/application/o/pandokos/ --audience <client-id>

pandokos subject-mapping create --tenant acme --issuer <issuer-id> \
  --subject batch --service-account batch-sa
```

The workload asks for a token with `client_id`, the service account's `username` and its app password
as `password` (and the `client_secret` of a confidential provider):

```
curl https://auth.example.com/application/o/token/ \
  -d grant_type=client_credentials -d client_id=<client-id> \
  -d username=batch -d password=$APP_PASSWORD -d scope=pandokos
```

With the provider's subject mode set to the user name, `sub` is `batch`. To make the provider carry a
tenant (a platform-level registration with `--tenant-claim pandokos_org`), add a scope mapping that
returns `{"pandokos_org": request.user.attributes.get("pandokos_org", "")}` and have the workload
request its scope: a token without the scope has no claim and is refused. The setup that is tested
is in `test/idp/authentik-blueprint.yaml`.

## 3. Finding the subject of a workload

The subject is whatever the registration's `subject_claim` holds in a token of the workload. Decode a
token of your own provider on your own machine and read the claim; do not paste a token into a web
page.

```
printf '%s' "$TOKEN" | cut -d. -f2 | tr '_-' '/+' | base64 -d 2>/dev/null; echo
```

Many providers use an opaque `sub` (Keycloak uses a generated UUID per service-account user); the
client ID is then in another claim, and `subject_claim` names it.

| Provider | Suggested `subject_claim` | Notes | Tested |
|---|---|---|---|
| Keycloak 26.8 | `azp` | `sub` is a generated UUID; `azp` and `client_id` carry the client ID; the audience comes from an audience mapper; an organisation can be added by a claim mapper | yes |
| Ory Hydra v26.2 | `sub` (the default) | `sub` equals the client ID; the caller asks for the audience with the `audience` parameter, and a token without one has an empty `aud`, which Pandokos refuses | yes |
| Authentik 2026.8 | `sub` (the default is a hash of the user ID; set the provider's subject mode to the user name to read it) | a workload is a service account; the audience is the provider's client ID and cannot be chosen; the tenant can be added by a scope mapping, which applies only when the caller requests that scope | yes |
| Kubernetes service account tokens | `sub` (`system:serviceaccount:<namespace>:<name>`) | the issuer is the cluster's, the audience is chosen when the token is requested | not tested |
| GitHub Actions | `sub` (repository and ref) or a claim such as `repository` | one issuer is shared by every user of GitHub: see section 5 | not tested |
| Microsoft Entra ID | `azp` or `appid` | version 1 and version 2 tokens differ in issuer and claim names | not tested |
| SPIFFE JWT-SVID | `sub` (the SPIFFE ID) | to Pandokos a JWT-SVID is a token of a registered issuer | not tested |

"Not tested" means that Pandokos has no test against that provider; the rules of section 4 apply to
every provider alike.

## 4. What is checked, in order

The first failure ends the request. The caller always receives the same `401 unauthenticated` in the
shape of the API family, byte for byte the answer to an unknown key; the reason is in the log and, when
a tenant is known, in the audit log (section 10).

| # | Check | Reason on failure |
|---|---|---|
| 1 | At most `tokens.max_token_bytes`; three segments of unpadded base64url; header and payload are JSON objects without duplicate member names; the signature segment is in canonical base64url | `malformed` |
| 2 | The header's `alg` is on the product's list of ten asymmetric algorithms (`none` and every `HS*` are refused); no `crit` member. `jwk`, `jku`, `x5c` and `x5u` are never read | `algorithm_not_allowed` |
| 3 | The unverified `iss` and `aud` select exactly one registration. A token whose audiences match two registrations is refused | `unknown_issuer`, `ambiguous_audience` |
| 4 | The registration is enabled and its `algorithms` include `alg` | `issuer_disabled`, `algorithm_not_allowed` |
| 5 | A verification key of the issuer: the key whose `kid` equals the header's (without a header `kid`, the only usable key for the algorithm). Keys are public, `use` absent or `sig`, `alg` absent or equal, and fit the algorithm: RSA of 2048 to 8192 bits for `RS*` and `PS*`, P-256, P-384 and P-521 for `ES256`, `ES384` and `ES512`, Ed25519 for `EdDSA` | `unknown_signing_key`; keys not obtainable: 503 (section 7) |
| 6 | The signature, with exactly the algorithm of the header | `bad_signature` |
| 7 | `iss` equals the registration's issuer; `aud` contains its audience | `issuer_mismatch`, `wrong_audience` |
| 8 | `exp` is a number and now is before `exp` plus `tokens.clock_skew`; `nbf`, when present, is not after now plus the skew; `iat`, when present, is not after now plus the skew | `expired`, `not_yet_valid` |
| 9 | The lifetime, `exp` minus `iat` (without `iat`: `exp` minus now), is at most `tokens.max_lifetime` | `lifetime_too_long` |
| 10 | Every member of `required_claims` | `required_claim_missing` |
| 11 | The subject claim is a string of 1 to 255 characters without control characters | `subject_missing` |
| 12 | Platform-level: the tenant claim is a non-empty string of at most 255 characters without control characters, and designates a listed tenant | `tenant_claim_missing`, `tenant_not_listed` |
| 13 | The registration still exists for the tenant (a tenant-bound one) or lists it (a platform-level one), and a mapping exists for the subject | `unknown_issuer`, `tenant_not_listed`, `subject_not_mapped` |
| 14 | The service account is active and within its validity dates | `service_account_disabled`, `service_account_not_valid` |

The signature is checked on every request; no token, and no digest of one, is stored. Nothing read from
a token is logged, audited or cached before the signature has been verified, except that `iss` and
`aud` select the registration whose keys then verify the token. Numeric claims must be JSON numbers; a
number given as a string fails its check. A suspended tenant is not an authentication failure: the
call is refused with `tenant_suspended`, as for a key.

## 5. Choosing the audience

The pair (`issuer`, `audience`) is unique in the whole installation, over both kinds of registration.
A second registration with a taken pair is refused with `409 conflict`, and the answer does not say
who holds it. This is what lets several tenants trust one issuer: a token selects the registration
whose audience it carries, so the audience is what tells their tokens apart.

**Requirement.** Whenever an issuer is shared, that is, whenever more than one tenant may register it
(a company-wide realm, a cloud provider's workload identity, GitHub Actions), the audience must name
the installation and the tenant, for example `pandokos.example.com/acme`, and the workloads must
request exactly that audience. Never register a generic audience such as `api` or the name of the
issuer's own client. For a platform-level registration the audience names the installation
(`pandokos.example.com`) and the tenant is the claim's job.

**The residual case.** A tenant that registers an audience is protected from other tenants only
because nobody else can register the same pair. If the workloads of tenant A present tokens whose
audience A has not registered, or no longer registers, and tenant B registers that pair (and maps the
subject to one of its own service accounts), those calls run as B's service account, with B's provider
credential and B's budget. Pandokos cannot prevent this: the token is valid for that pair. Two habits
close it. Request an audience that names the tenant, so B has no reason to guess it. And stop the
workloads, or change their audience, before deleting or changing a registration, so that no token is
in flight for a pair that becomes free.

**Timing.** A refusal before the signature check is faster than one after it. A caller can learn from
the time of a refusal that an (`issuer`, `audience`) pair is registered somewhere. A tenant
administrator can learn the same from a `409`. Timing is not equalised.

## 6. Where the keys come from

The gateway, and only the gateway, fetches an issuer's documents; the administration API never does,
so registering an issuer checks nothing about it. A wrong URL shows at the first token.

- With an explicit `jwks_uri`, that URL. Otherwise `<issuer>/.well-known/openid-configuration` (one
  trailing `/` of the issuer removed), whose `issuer` member must equal the registered issuer byte for
  byte and whose `jwks_uri` must be an `https` URL without user information.
- Through the egress guard ([gateway guide](gateway.md), section 6): `https` only, certificates
  verified against the system roots and `upstream.tls.ca_file`, no redirect followed, the proxy of
  `upstream.proxy.url` for public addresses, the same forbidden addresses as for providers.
- Limits: 5 s for one exchange (`tokens.jwks.fetch_timeout`), 64 KiB for a discovery document, 256 KiB
  for a key set, status 200 only, at most 32 usable keys kept. A key set member that cannot be parsed
  is skipped, not an error; an encryption key (`use: enc`) is dropped.

### Private hosts and the proxy

An issuer whose host resolves to a private address (an identity provider inside your network) is
refused as `blocked`, like a private provider endpoint without the `private` declaration. The
operator, not a tenant, allows it: list the host name in `tokens.private_hosts` (lower-case DNS names,
no address literals, no wildcards, at most 32). The private transport is used for each URL (discovery
document, key set) whose host is listed, so list the host of both. The transport stays `https`-only and addresses that are forbidden stay unreachable. Because the
list widens what tenant administrators can make the gateway fetch, `serve` warns at start naming the
hosts. Public issuers use the proxy of `upstream.proxy.url` when it is set; listed private hosts
always connect directly.

## 7. When the identity provider is down

Keys are fetched when the first token of a source arrives, then refreshed in the background every
`tokens.jwks.refresh_interval` (15 minutes), the next use after that being served from the keys in
memory. A token with an unknown `kid` causes one fetch, at most one per source every
`tokens.jwks.min_refetch_interval` (10 seconds), so a provider that rotates its keys is followed within
that time and a flow of forged tokens cannot flood it. Cache headers of the issuer are not read.

If the provider stops answering, calls keep verifying with the keys in memory for up to
`tokens.jwks.max_stale` (6 hours) after the last successful fetch. After that, and for a source whose
keys were never obtained, a call is answered `503 unavailable` with `Retry-After: 5`, never `401`:
the credential could not be verified, which says nothing about it, and nothing is forwarded. A
database that cannot answer produces the same 503. The log line is `issuer keys not fetched` with the
issuer (an address literal appears as `(address)`) and one class: `blocked`, `timeout`, `status`,
`too_large`, `invalid_document`, `issuer_mismatch`, `no_usable_key` or `network`.

A key the provider withdraws stops being trusted within `refresh_interval`, or `max_stale` while the
provider cannot be reached. Raise `max_stale` only if you accept that.

## 8. Configuration

| Key | Default | Meaning |
|---|---|---|
| `tokens.enabled` | `true` | `false`: every credential that is not a virtual key is `malformed` and no issuer is ever contacted |
| `tokens.max_token_bytes` | `8192` | largest token; at most half of `listeners.inference.max_header_bytes` |
| `tokens.clock_skew` | `60s` | tolerance on `exp`, `nbf` and `iat` |
| `tokens.max_lifetime` | `24h` | longest `exp` minus `iat` accepted |
| `tokens.private_hosts` | none | hosts allowed to resolve to private addresses |
| `tokens.jwks.fetch_timeout` | `5s` | one exchange with an issuer |
| `tokens.jwks.refresh_interval` | `15m` | background refresh of a key set |
| `tokens.jwks.min_refetch_interval` | `10s` | shortest time between fetches caused by an unknown `kid` |
| `tokens.jwks.max_stale` | `6h` | use of a key set after its last successful fetch |

Bounds and the warnings are in the [configuration reference](configuration.md). Without a
registration nothing is accepted and nothing is fetched, so the default `true` costs nothing.

## 9. How fast a change takes effect

See also the [revocation table](keys.md#how-fast-a-revocation-takes-effect).

| Change | Inference listener |
|---|---|
| A mapping is deleted; a registration is disabled, changed or deleted; a tenant leaves a platform-level list; a service account is disabled or loses a role | at once where notifications arrive, at most `cache.principal_ttl` otherwise |
| A token expires | at `exp` plus `tokens.clock_skew` |
| The provider withdraws a signing key | at most `tokens.jwks.refresh_interval`; at most `tokens.jwks.max_stale` while it cannot be reached |
| One token is to be revoked before `exp` | not possible: there is no revocation list. Delete the mapping, or wait |

A token is a bearer credential: whoever holds it can use it until `exp` (SEC-02). Serve the listener
with TLS, ask the provider for short lifetimes, and use `tokens.max_lifetime` to refuse long ones.
Sender-constrained tokens (mutual TLS, DPoP) are not built.

## 10. Logs, audit and usage

- A failure is logged as `authentication failed` (warn) with `listener`, `request_id`, `reason` and,
  when known, `issuer_id`, `issuer_scope`, `tenant_id`, `audited`; `subject` only after check 11.
  Never the token, a segment of it, its header, a claim that was not verified, or the body of an
  issuer's document. Key retrieval logs name the issuer, not the registration ID.
- A failure that names a tenant is written to that tenant's audit log as `auth.failure` with
  `resource_type` `token_issuer`, `resource_id` the registration's ID and `detail` `reason`,
  `listener`, `issuer_scope`, and `subject` after check 11. It is bounded: at most one row per
  registration, reason and subject in ten minutes, 60 a minute per instance. Failures at checks 1 to 3
  name no tenant and are logged only. Failures of a platform-level registration before check 13 are
  logged with `issuer_id` and not audited: the tenant is not yet known.
- Changes to registrations and mappings are audited: `token_issuer.create|update|delete`,
  `subject_mapping.create|delete` in the tenant's log, and
  `platform_issuer.create|update|delete|tenant_add|tenant_remove` in the platform log.
- A successful call is recorded with `principal_kind` `token`, `principal_id` the service account and
  `key_id` the ID of the mapping ([usage guide](usage.md)); the `request` log line carries `issuer_id`.

## 11. What is not there

A subject mapping can be the subject of a limit rule, named by its ID, like a virtual key ([limits guide](limits.md)); limits of the service account apply to tokens too.

- Introspection, revocation of one token, replay tracking by `jti`, opaque tokens, encrypted tokens.
- Patterns, prefixes or expressions for subjects: a mapping is an exact match.
- Scopes, model lists or pins per mapping: a token principal has every permission of the service
  account's roles and the tenant's default credential of the provider.
- Tokens on the administration listener, and mapping a subject to a user: both come with users and
  sessions (sprint 4.2).
- A check of a registration or a token from the API; metrics for token authentication.

## 12. Troubleshooting

Find the `reason` in the log line `authentication failed` (or in the audit log, when the tenant is known).

| Reason | What it means and what to do |
|---|---|
| `malformed` | not a token (three base64url segments), larger than `tokens.max_token_bytes`, tokens disabled, or a non-canonical encoding. Check that the whole token is sent, with no quotes or line breaks |
| `algorithm_not_allowed` | the token's `alg` is not on the registration's list (default `RS256`, `ES256`). Add it with `--algorithm` if the provider signs with it |
| `unknown_issuer` | no registration has this `iss` and one of the token's audiences. Compare both byte for byte (a trailing slash counts), and check that the registration still exists |
| `ambiguous_audience` | the token carries audiences that select two registrations. Request one audience |
| `issuer_disabled` | the registration has `enabled: false` |
| `unknown_signing_key` | the key set has no usable key for the token's `kid` and algorithm, or the provider rotated and `min_refetch_interval` has not passed. Retry after ten seconds; check the key set's algorithms and sizes |
| `bad_signature` | the signature does not verify with the issuer's key |
| `issuer_mismatch`, `wrong_audience` | `iss` or `aud` differs from the registration after the signature verified |
| `expired`, `not_yet_valid` | check the workload's clock and `tokens.clock_skew` |
| `lifetime_too_long` | the provider issues tokens that live longer than `tokens.max_lifetime`: shorten them there |
| `required_claim_missing` | a claim of `required_claims` is absent or different |
| `subject_missing` | the subject claim is absent, not a string, empty, longer than 255 characters or holds control characters. Check `subject_claim` |
| `tenant_claim_missing` | platform-level: the tenant claim is absent, not a string, empty or holds control characters |
| `tenant_not_listed` | the claim value designates no tenant of the list. Use the listed `claim_value`, not the tenant name |
| `subject_not_mapped` | the tenant has no mapping for this subject. Create one |
| `service_account_disabled`, `service_account_not_valid` | the mapped service account is disabled or outside its validity dates |
| 503 `unavailable` | the issuer's keys cannot be obtained, or the database cannot answer; see the log line `issuer keys not fetched` and its `class` (`blocked` for a private host that is not in `tokens.private_hosts`) |

## 13. Deleting a registration

Stop the workloads that use it first, or switch them to another audience (section 5, residual case).
Deleting a registration deletes its subject mappings. Deleting a platform-level registration also
deletes the mappings of every tenant; its audit row records in `mappings_deleted` how many, counted
across all tenants by a database function that returns only the number. `enabled: false` is the reversible switch and keeps the mappings.
