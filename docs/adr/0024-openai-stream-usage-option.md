# 0024. OpenAI chat streams: how usage is requested and what is removed

- Status: proposed
- Date: 2026-11-12

Refines [ADR 0010](0010-usage-extraction.md); does not supersede it.

## Context

ADR 0010 says that for OpenAI chat streams Pandokos adds the option that makes the provider report
usage and removes the resulting usage-only event, so that the caller sees what it would have seen.
Building it showed three points the ADR leaves open: how the request body is changed, what exactly
is removed, and where the reader runs.

## Decision

1. The request body is changed by splicing bytes at the top-level `stream_options` member. It is
   never parsed and re-serialized. A body that is not a JSON object, or a stream that already asks
   for usage, is forwarded untouched. The cases, decided from the top-level members `stream` and
   `stream_options` only (when a key is repeated, the last occurrence counts, as in
   `encoding/json`):

   | Body | Result |
   |---|---|
   | not exactly one JSON object | unchanged; not a stream, nothing stripped |
   | no `stream`, or `stream` is anything but the literal `true` | unchanged; not a stream |
   | `stream: true`, no `stream_options` | `,"stream_options":{"include_usage":true}` inserted immediately before the closing brace of the top-level object; stripped |
   | `stream: true`, `stream_options: null` | the `null` replaced by `{"include_usage":true}`; stripped |
   | `stream: true`, `stream_options` an object without `include_usage` | `"include_usage":true` inserted immediately after the opening brace of that object, followed by `,` if the object has members; stripped |
   | `stream: true`, `include_usage` is the literal `true` | unchanged; the caller asked, nothing is stripped |
   | `stream: true`, `include_usage` is anything else | that value replaced by `true`; stripped |
   | `stream: true`, `stream_options` neither null nor an object | unchanged; nothing is stripped |

2. In the response only the usage-only event (non-null `usage`, empty `choices`) is removed. When
   the option is on, the provider also adds `"usage":null` to every other chunk; that member is
   left in place. This is the one observable difference for a caller whose request was modified,
   and it is documented in the operations guide.
3. The usage reader runs in line with the copy of the response, not in a parallel goroutine. It is
   bounded (one incomplete event of at most 1 MiB), fails open (on overflow it forwards everything
   unchanged and stops reading), and when nothing has to be removed it returns the provider's
   bytes untouched without waiting for event boundaries.
4. When a caller asked for usage itself, nothing is added and nothing is removed.

## Consequences

"Byte for byte" ([ADR 0009](0009-native-passthrough-by-api-family.md)) holds outside the spliced
range. When stripping, the reader holds an event until it is complete: the stream is forwarded
event by event, not byte by byte, in that mode. Strict clients that reject unknown or null members
would see `"usage":null`; the official SDK does not mind.

## Alternatives considered

- Re-serializing the body: reorders keys and reformats numbers.
- Rewriting every chunk to drop `"usage":null`: a JSON rewrite of every event of every stream.
- A parallel reader fed through a channel: needs a bounded queue and a drop policy, and cannot
  remove an event.
- Not adding the option and estimating instead: loses the provider's exact count for the most
  common call shape.

## History

- 2026-11: sprint 3.2 confirms the decision for chat streams; the responses and embeddings operations need no option.
