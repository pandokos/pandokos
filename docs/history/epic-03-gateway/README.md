# Epic 3 — Gateway: record

Completed 2026-11-14. Epic file as closed: [epic.md](epic.md).

## Outcome

Unmodified OpenAI and Anthropic SDKs call models through Pandokos with a virtual key, streaming
included. The gateway resolves provider, endpoint, model and credential from the catalogue and the
vault, and the call is made with the tenant's own credential (BYOK); the operator's single key of
`skeleton.*` is gone. Every outbound connection passes an address-checking egress guard (private
endpoints are marked explicitly, forbidden addresses are never reachable), with upstream TLS
settings and an optional explicit proxy. Chat completions, responses and embeddings of the `openai`
family and messages (with `count_tokens`) of the `anthropic` family are forwarded with their
streams, and OpenAI-compatible providers are added by configuration, with a per-provider switch for
the stream usage option.

Each authenticated request leaves exactly one usage record (tokens, their source, estimated cost at
the price version in force, outcome, caller metadata) written by a bounded ledger writer into
monthly partitions, and two spans exported over OTLP/HTTP, joined to the caller's trace. A tenant
administrator can ask which models and credentials a tenant can actually use (capability check) and
list the tenant's usage records. Authentication failures on the inference listener are audited, with
suppression. The SDK compatibility suite runs the real Python and TypeScript SDKs in CI, and a
Docker Compose reference with TLS on both listeners takes an operator from nothing to a first call.

## Sprints

| Sprint | Design and plan | Delivered | Review |
|---|---|---|---|
| 3.1 Authenticated passthrough | [sprint](3.1-authenticated-passthrough/sprint.md), [plan](3.1-authenticated-passthrough/plan.md) | PR #11 | 3 rounds (5 findings and 3 nits, then 1, then none) |
| 3.2 Streaming, usage and ledger | [sprint](3.2-streaming-usage-and-ledger/sprint.md), [plan](3.2-streaming-usage-and-ledger/plan.md) | PR #12 | 2 rounds (9 findings and 2 nits, then none) |
| 3.3 Anthropic and OpenAI-compatible providers | [sprint](3.3-anthropic-and-openai-compatible-providers/sprint.md), [plan](3.3-anthropic-and-openai-compatible-providers/plan.md) | PR #13 | 3 rounds (2 findings, then 1, then none) |

Each sprint followed the cycle of the earlier epics: a frozen design, tests committed failing before
the implementation, a review against the design repeated until it reported no findings, a pull
request with green CI. Open points raised by each design and the maintainer's decisions on them are
at the end of section 12 of each `sprint.md`; deviations accepted at review are listed in the pull
requests and under "Amendments" in section 12. During sprint 3.2 the Go floor was raised to 1.26.0
(govulncheck on `golang.org/x/net` brought by the OTLP exporter; the fixed version needs Go 1.26 and
Go 1.25 is out of support) and the OpenTelemetry SDK was taken at v1.47.0; the database listener no
longer flushes the caches when it loses its connection, only when it reconnects.

## Decisions recorded

ADR 0037–0038 (sprint 3.1: request pipeline and resolution; egress guard, upstream TLS and proxy),
ADR 0039–0040 (sprint 3.2: usage record, admission and ledger writer; traces),
ADR 0041–0044 (sprint 3.3: the `anthropic` family; OpenAI-compatible providers and the stream usage
option; tenant capability check and usage list; SDK compatibility suite in Python and TypeScript),
with ADR 0020 amended and history lines on several earlier ADRs. See the
[ADR index](../../adr/README.md).

## Carried forward

- Sprint 4.1: token principals behind `Authenticator` (`principal_kind` `token`; a migration widens
  two check constraints); a token principal has no pin and no key model list, and arrives as
  `Authorization: Bearer` for both families.
- Sprint 5.1: *reserve* around steps 7 and 8 and *settle* in the handler's finish; the 429 codes and
  per-tenant concurrency (the rest of GW-16); the output estimate of a stream, a count of content
  events, is revisited (3.2 O-5); an incremental scanner for the top-level `usage` member of JSON
  answers over 4 MiB, which are estimated today (3.2 O-14); the request-side estimate for Anthropic
  from `max_tokens`; the `budget` field of the capability check, which reads `not_enforced`, becomes
  `available` or `exhausted` with the reason `budget_exhausted`.
- Epic 6: sprint 6.1 adds metrics (by family, by source mix, by `error_code`, egress blocks by
  reason, authentication failures, dropped spans, refused admissions, rejected and unwritten
  records), the `gen_ai.*` attributes and token counts on spans (3.2 O-16). Sprint 6.2 builds reports
  and rollups on `usage_record`, retention by dropping partitions through an audited function,
  the decision on rows in the default partition, and reading `auth.failure` rows of both listeners
  (`detail.listener`). Sprint 6.3 pseudonymizes
  `principal_id` and `key_id` through a definer function, since the table is append-only for every
  role.
- Sprint 6.4: the usage event is written with the record in the writer's transaction.
- Epic 8: each instance has its own ledger buffer (`service.instance.id` tells spans apart); separate
  processes reload `upstream.tls.ca_file` with the listener certificates and run the partitioner;
  in sprint 8.2 the admin process keeps a guard of its own for validation and the worker for the
  download; the one-transaction-per-tenant flush (3.2 O-12) is measured and revisited; the Helm
  chart reuses the choices of the Compose reference.
- Release R4 and later: the unified `/v1/…` endpoint routing by model to providers of family
  `openai`, with the stream usage option applied per resolved provider; Gemini and Azure OpenAI as
  new adapters; tenant-supplied endpoints reuse the guard as the public class.
- Not scheduled: the capability check for a specific key's pin and model list (3.3 O-3);
  exact-case member matching in both families (hardening); proxy authentication for
  `upstream.proxy.url` (3.1 O-1); a gRPC OTLP exporter (3.2 O-2).
- Maintainer actions outside the repository: run the manual check of
  [providers.md](../../operations/providers.md) against Groq, Mistral and Ollama with real keys and
  fill its table, which ships "not yet run" (rerun it when an SDK major version or a provider's API
  version changes; Dependabot pull requests of `pip` and `npm` go through the SDK jobs). Operators
  upgrading from epic 2 remove `skeleton.*` from the configuration, create a tenant credential per
  provider (BYOK), set `upstream.proxy.url` behind a proxy, give the container a stop grace period
  of at least `shutdown.drain_timeout` + 5 s + `ledger.shutdown_flush_timeout` + a margin (46 s
  with the defaults), and know that tracing is off until `telemetry.otlp.endpoint` is set. The
  binary grew from 14.2 MB to 22.1 MB with the OpenTelemetry SDK.
