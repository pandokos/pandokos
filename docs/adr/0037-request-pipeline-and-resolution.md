# 0037. Request pipeline and resolution

- Status: proposed
- Date: 2026-11-14

Refines [ADR 0009](0009-native-passthrough-by-api-family.md), [ADR 0020](0020-error-contract.md),
[ADR 0034](0034-credential-model-and-platform-oversight.md) and
[ADR 0035](0035-catalogue-and-model-access.md).

## Context

The catalogue and the vault exist; the inference route still forwarded with one key from the
configuration. Joining them needs an order of steps that bounds what an unauthenticated or
unauthorized caller costs, a place for the knowledge of each wire protocol, and answers to
points earlier sprints left open: which permissions a call needs, what happens to a key's pin,
whether the model name is rewritten, and what is done with a body the proxy cannot read.

## Decision

1. One pipeline, in this order: request ID; route (the first path segment names an enabled
   catalogue provider, whose API family has an adapter and whose allow-list has the path);
   authentication from the family's native header; authorization; body read within the size
   and time limits; parsing of model and stream flag; model check; credential; forward. The
   body is not read before authorization and no credential is opened before the model check
   passed.
2. An API family is an adapter (operations, credential header, header lists, request parser,
   usage readers, error shape). A provider uses the adapter of its catalogue family. A name
   that is unknown, disabled, reserved or of a family without adapter is the same 404.
3. Under BYOK a call needs `model:invoke` and `credential:use`; both are decided before the
   body is read. A key limited to the first is refused with `forbidden`.
4. The credential is the pin of the key when the pin is of the provider called, otherwise the
   tenant's default for that provider; nothing else is tried. No credential, or one that is
   disabled or revoked, is `no_credential`.
5. A request body must be one JSON object with exactly one top-level string member `model`;
   anything else is refused with `invalid_request`, because parsers disagree on duplicates
   and the model check must see what the provider will see. When a model's upstream
   identifier differs from its public name, the value is replaced by a byte splice; the
   response is not rewritten.
6. The request allow-list of the `openai` family is `Accept`, `Content-Type`, `User-Agent`,
   `OpenAI-Organization`, `OpenAI-Project`, `OpenAI-Beta`. A header named in `Connection` is
   dropped even when listed. `Idempotency-Key` is not forwarded and the outbound request
   cannot be replayed by the transport.
7. A redirect from a provider is not followed and not passed on: it is answered as
   `upstream_unreachable`.
8. The gateway package depends on interfaces for the catalogue, the vault and the audit
   record; a wiring package adapts the real ones.

## Consequences

A second family is a second adapter. Limits, the ledger and traces attach at fixed points of
the pipeline. An unauthenticated caller can learn which provider names are served. A client
that sends duplicate `model` members is refused. Callers see the upstream identifier in
responses when names differ.

## Alternatives considered

Authenticating before routing (needs every family's header tried in turn); checking
`credential:use` only in the vault (a key without it would learn its model access); rewriting
the model in responses (a JSON rewrite of every response and stream event); passing redirects
through; a pinned key limited to its credential's provider.
