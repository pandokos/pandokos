# Fixtures

The files under `openai/` are synthetic. They are shaped after the provider's documented wire
format and contain no real prompt, completion or key: the only content-like strings are the
canary constants of `canary.go`.

They are to be replaced by scrubbed recordings when a provider key is available (sprint 1.1,
risk R-4).
