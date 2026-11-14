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
