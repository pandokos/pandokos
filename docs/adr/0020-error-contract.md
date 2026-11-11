# 0020. Error contract: the shape of the API being called

- Status: accepted
- Date: 2026-11-11

## Context

A caller uses a provider SDK. When Pandokos itself refuses a request (bad key, model not allowed,
budget exhausted), the SDK will try to parse the response as a provider error. A response in a
format of our own surfaces as an unhelpful parsing failure. At the same time callers and operators
must be able to tell a Pandokos refusal from a provider error (GW-10, LIM-09).

## Decision

**Errors from the provider** pass through unchanged: status, body and the headers on the
allow-list, including the provider's rate-limit headers.

**Errors raised by Pandokos on the inference API**:

- use the HTTP status the API family's clients expect for that condition;
- carry a body in the error shape of the API family being called, with a message that says the
  error comes from Pandokos;
- carry `x-pandokos-error: <code>` and `x-pandokos-request-id`. The presence of the first header
  is how any client distinguishes the two origins.

Initial codes, stable once released:

| Code | Status | Meaning |
|---|---|---|
| `unauthenticated` | 401 | Missing, malformed, expired or revoked credential |
| `tenant_suspended` | 403 | The tenant is not active |
| `forbidden` | 403 | The principal lacks the permission |
| `model_not_allowed` | 403 | The model is unknown or not available to this tenant or key |
| `endpoint_not_supported` | 404 | The path is not on the family's allow-list |
| `no_credential` | 409 | No usable provider credential for this tenant and provider |
| `request_too_large` | 413 | Body over the configured limit |
| `rate_limited` | 429 | A rate limit of Pandokos was reached |
| `quota_exceeded` | 429 | A quota or budget of Pandokos was reached |
| `upstream_unreachable` | 502 | The provider could not be reached |
| `upstream_timeout` | 504 | The provider did not answer in time |
| `unavailable` | 503 | Pandokos cannot verify the request right now |
| `internal` | 500 | Unexpected failure |

`unauthenticated` and unknown-resource responses are deliberately uniform, so they do not reveal
whether a key ID, tenant or resource exists.

**Errors on the administration API** follow RFC 9457 problem details, with the same code
vocabulary extended for administrative conditions.

Error messages never include secrets, internal addresses, SQL, stack traces or anything about
another tenant.

## Consequences

- SDKs raise their usual exception types (authentication error, rate-limit error) for Pandokos
  refusals, and their built-in retry behaviour applies to 429 and 5xx as it would with the
  provider.
- Each API family must implement its error shape, which is a few lines.
- Callers that want to treat Pandokos limits differently from provider limits read one header.

## Alternatives considered

**One Pandokos error format everywhere.** Consistent for us, opaque to every SDK.

**Rewriting provider errors into a common format.** Loses provider detail and contradicts the
transparency principle.
