# Providers guide

Pandokos forwards a request in the provider's own API, one **API family** at a time
([ADR 0009](../adr/0009-native-passthrough-by-api-family.md)). Two families exist: `openai` and
`anthropic`. A provider that speaks one of them is configuration, not code. This page lists what is
supported, how to add an OpenAI-compatible provider, and which servers have been checked by hand.

## 1. Supported providers

Tier 1 (requirement CAT-02), seeded in every installation:

| Provider | Family | Operations | Base URL for an SDK |
|---|---|---|---|
| `openai` | `openai` | `chat.completions`, `responses`, `embeddings` | `https://<host>/openai/v1` |
| `anthropic` | `anthropic` | `messages`, `messages.count_tokens` | `https://<host>/anthropic` |

The [gateway guide](gateway.md) lists the headers, errors and limits of each family.

## 2. An OpenAI-compatible provider

Any server that speaks the OpenAI wire (chat completions, responses or embeddings) is a provider row
of family `openai` with an endpoint, models and the tenant's credential. No code is per vendor
([ADR 0042](../adr/0042-openai-compatible-providers-and-stream-usage-option.md)).

```sh
# a public provider
pandokos provider create groq --family openai --display-name Groq
pandokos endpoint create groq default --base-url https://api.groq.com/openai --default
pandokos model create groq/llama-3.3-70b-versatile --upstream-id llama-3.3-70b-versatile --availability all
# a self-hosted server on a private network
pandokos provider create ollama --family openai --stream-usage-option omit   # only if the server refuses the option
pandokos endpoint create ollama default --base-url http://ollama.internal:11434 --private --default
```

- **Base URL with a path.** Pandokos appends the native path (`/v1/chat/completions`) to the endpoint's
  base URL, path included, so `https://api.groq.com/openai` becomes `https://api.groq.com/openai/v1/...`.
- **Private endpoints.** A server on your network needs `--private`, which allows `http` and private
  addresses; public endpoints need `https` and public addresses ([gateway guide](gateway.md), section 6).
- **Credentials.** Credential validation calls `GET <base>/v1/models` with a bearer token. A server that
  needs no key still needs a credential of the tenant (BYOK): store any non-empty placeholder as the
  secret.
- **Models and prices.** Create the models of the provider in the catalogue ([catalogue guide](catalogue.md)).
  A model without a price version is recorded with `cost_status` `unknown` and `cost_reason` `no_price`;
  add a price override to cost it.
- The callers use the SDK of OpenAI with the base URL `https://<host>/<provider>/v1`.

## 3. The stream usage option

For a chat stream whose caller did not ask for usage, Pandokos adds `stream_options.include_usage` so
that the provider reports usage, and removes the usage-only event again
([ADR 0024](../adr/0024-openai-stream-usage-option.md)). Some servers refuse unknown request members
and answer 400 to it. For such a provider set `stream_usage_option` to `omit`:

```sh
pandokos provider update groq --stream-usage-option omit
```

Pandokos then never adds the option. A stream whose caller did not ask for usage is forwarded as the
server sends it and its usage is **estimated** (input from the request size, output from the number of
content events), unless the server reports usage anyway, which some do on the last content chunk and
which the reader takes. The record says `estimated`. A caller that asks for usage itself is unaffected.
`omit` applies to the provider of the `openai` family only; the administration API refuses it for
`anthropic`. A change reaches the gateway within `cache.catalog_ttl`.

## 4. What CI proves

CI reaches no provider and holds no provider key. It replays the following profiles of OpenAI-compatible
servers with a fake provider and checks that the usage and the cost are right:

| Profile | What the server does | Provider setting |
|---|---|---|
| usage on the last content chunk | no usage-only chunk; usage in the last chunk with content | `add` |
| extra usage members | a usage-only chunk with provider-specific integer members (kept in `provider_usage`) and fractional timing members (dropped) | `add` |
| ignores the option | streams without usage whatever `stream_options` says | `add` or `omit`: estimated |
| refuses the option | answers 400 when the body has `stream_options` | `omit` |
| no usage at all | a stream without usage | estimated |

The official OpenAI Python and TypeScript SDKs are also run against a compatible provider's base URL.
That a named real server behaves like one of these profiles is checked by hand, below.

## 5. The manual check

A maintainer checks a real server with a real key and records the result here, with the date and the
server version. Repeat it when an SDK or the provider's API changes.

1. Create the provider, endpoint and model as in section 2 (with `--stream-usage-option omit` for a
   server that refuses the option) and store a real key for a test tenant.
2. With the OpenAI SDK, make one plain and one streamed `chat.completions` call with the SDK's default
   options, and the same two calls with `stream_options={"include_usage": True}`.
3. Read the records (`pandokos usage list --tenant T --provider P`): the sources, token figures and cost.
4. Write the result in the table: `ok`, or what differed and which setting fixed it.

### Verified providers

No server has been checked yet: the check needs real keys and is run by a maintainer.

| Provider | Server version | Date | Family | Base URL | Stream usage option | Result | Checked by |
|---|---|---|---|---|---|---|---|
| Groq | not yet run | not yet run | `openai` | `https://api.groq.com/openai` | not yet run | not yet run | not yet run |
| Mistral | not yet run | not yet run | `openai` | `https://api.mistral.ai` | not yet run | not yet run | not yet run |
| Ollama | not yet run | not yet run | `openai` | `http://<host>:11434` (private) | not yet run | not yet run | not yet run |
