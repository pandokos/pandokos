# Fixtures

The files under `openai/` are synthetic. They are shaped after the provider's documented wire
format and contain no real prompt, completion or key: the only content-like strings are the
canary constants of `canary.go`.

They are to be replaced by scrubbed recordings when a provider key is available (sprint 1.1,
risk R-4).

Sprint 3.2 adds `responses.json`, `responses_stream.sse`, `responses_stream_incomplete.sse`,
`embeddings.json`, `chat_stream_tools.sse`, `chat_stream_error.sse` (an error event in the middle of
the stream, no final marker) and `chat_stream_keepalive_crlf.sse` (comment events, `id:` and
`event:` lines, CRLF line endings). They are as synthetic as the others: the usage figures are
made up, and the only content-like strings are the canaries.

Sprint 3.3 adds `anthropic/` (the Messages API: `message.json`, `message_cache.json`,
`message_stream.sse`, `message_stream_cumulative.sse`, `message_stream_tools.sse`,
`message_stream_error.sse`, `message_stream_ping_crlf.sse`, `count_tokens.json`, `error_401.json`,
`error_429.json`, `error_529.json`) and `compat/` (the profiles of OpenAI-compatible servers:
`chat_stream_usage_last_content_chunk.sse`, `chat_stream_extra_usage.sse`,
`chat_stream_no_usage.sse`, `chat_completion_extra_usage.json`, `error_400_stream_options.json`).
They are shaped after each wire format as documented; the only content-like strings are the
canaries and every identifier starts with `synthetic-`. A fixture made from a real response is
allowed only when every string value was replaced by a canary and every identifier by a
`synthetic-` value, by hand, and reviewed. `TestFixturesHoldNoKeys` (`test/repo`) scans these
directories for key patterns.
