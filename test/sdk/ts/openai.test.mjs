// T-556, T-557, T-558, T-564: the OpenAI SDK against the openai family and a compatible provider.
import assert from "node:assert/strict";
import { test } from "node:test";

import { CANARY_COMPLETION, CANARY_PROMPT, COMPAT_BASE, oneRecord, openai, requestIdOf, upstreamLast } from "./helpers.mjs";

const messages = [{ role: "user", content: CANARY_PROMPT }];

test("T-556 chat, plain", async () => {
  const { data, response } = await openai().chat.completions.create({ model: "fake-basic", messages }).withResponse();
  assert.equal(data.choices[0].message.content, CANARY_COMPLETION);
  assert.deepEqual([data.usage.prompt_tokens, data.usage.completion_tokens], [9, 12]);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.provider, rec.api_family, rec.operation, rec.outcome], ["openai", "openai", "chat.completions", "ok"]);
  assert.deepEqual([rec.input_source, rec.output_source, rec.input_tokens, rec.output_tokens, rec.stream], ["provider", "provider", 9, 12, false]);
});

test("T-556 chat, stream without include_usage: the SDK sees no usage chunk", async () => {
  const { data: stream, response } = await openai().chat.completions.create({ model: "fake-basic", messages, stream: true }).withResponse();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  assert.ok(chunks.length > 0);
  assert.ok(chunks.every((c) => c.choices.length === 1), "a usage-only chunk reached the caller");
  assert.ok(chunks.every((c) => !c.usage));
  assert.equal(chunks.map((c) => c.choices[0].delta.content ?? "").join(""), CANARY_COMPLETION + " end");
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.stream, rec.input_source, rec.output_source, rec.input_tokens, rec.output_tokens], [true, "provider", "provider", 9, 12]);
});

test("T-556 chat, stream with include_usage: the SDK sees the usage", async () => {
  const { data: stream, response } = await openai()
    .chat.completions.create({ model: "fake-basic", messages, stream: true, stream_options: { include_usage: true } })
    .withResponse();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  const last = chunks.at(-1);
  assert.equal(last.choices.length, 0);
  assert.deepEqual([last.usage.prompt_tokens, last.usage.completion_tokens, last.usage.total_tokens], [9, 12, 21]);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.input_source, rec.output_source, rec.input_tokens, rec.output_tokens], ["provider", "provider", 9, 12]);
});

test("T-557 responses, plain", async () => {
  const { data, response } = await openai().responses.create({ model: "fake-basic", input: CANARY_PROMPT }).withResponse();
  assert.equal(data.output_text, CANARY_COMPLETION + " end");
  assert.deepEqual([data.usage.input_tokens, data.usage.output_tokens], [20, 15]);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.operation, rec.input_source, rec.output_source], ["responses", "provider", "provider"]);
  assert.deepEqual([rec.input_tokens, rec.output_tokens, rec.cache_read_tokens], [20, 15, 5]);
});

test("T-557 responses, stream", async () => {
  const { data: stream, response } = await openai().responses.create({ model: "fake-basic", input: CANARY_PROMPT, stream: true }).withResponse();
  let text = "";
  let completed;
  for await (const event of stream) {
    if (event.type === "response.output_text.delta") text += event.delta;
    if (event.type === "response.completed") completed = event.response;
  }
  assert.equal(text, CANARY_COMPLETION + " end");
  assert.equal(completed.usage.input_tokens, 20);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.operation, rec.stream, rec.input_tokens, rec.output_tokens], ["responses", true, 20, 15]);
});

test("T-558 embeddings", async () => {
  const { data, response } = await openai().embeddings.create({ model: "fake-basic", input: CANARY_PROMPT, encoding_format: "float" }).withResponse();
  assert.equal(data.data.length, 1);
  assert.equal(data.data[0].embedding.length, 3);
  assert.equal(data.usage.prompt_tokens, 8);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.operation, rec.input_source, rec.input_tokens, rec.output_tokens], ["embeddings", "provider", 8, 0]);
});

for (const model of ["fake-basic", "compat-refuses-option", "compat-usage-last-chunk"]) {
  test(`T-564 compatible provider, chat stream of ${model}: the option is not spliced`, async () => {
    const { data: stream, response } = await openai(COMPAT_BASE).chat.completions.create({ model, messages, stream: true }).withResponse();
    let n = 0;
    for await (const _ of stream) n += 1;
    assert.ok(n > 0);
    const last = await upstreamLast();
    assert.equal(last.path, "/compat-base/v1/chat/completions");
    assert.equal(last.body_has_stream_options, false, "the option was spliced into a provider set to omit");
    assert.equal(last.model, model);
    const rec = await oneRecord(requestIdOf(response.headers));
    assert.deepEqual([rec.provider, rec.api_family, rec.operation, rec.outcome], ["compat", "openai", "chat.completions", "ok"]);
  });
}

test("T-564 compatible provider, usage sources", async () => {
  for (const [model, sources, tokens] of [
    ["compat-ignores-option", ["estimated", "estimated"], null],
    ["compat-usage-last-chunk", ["provider", "provider"], [9, 12]],
  ]) {
    const { data: stream, response } = await openai(COMPAT_BASE).chat.completions.create({ model, messages, stream: true }).withResponse();
    for await (const _ of stream);
    const rec = await oneRecord(requestIdOf(response.headers));
    assert.deepEqual([rec.input_source, rec.output_source], sources);
    if (tokens) assert.deepEqual([rec.input_tokens, rec.output_tokens], tokens);
    else assert.ok(rec.estimated_output_tokens >= 1);
  }
});

test("T-564 compatible provider, embeddings", async () => {
  const { data, response } = await openai(COMPAT_BASE).embeddings.create({ model: "fake-basic", input: CANARY_PROMPT, encoding_format: "float" }).withResponse();
  assert.equal(data.data[0].embedding.length, 3);
  assert.equal((await upstreamLast()).path, "/compat-base/v1/embeddings");
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.provider, rec.api_family, rec.operation], ["compat", "openai", "embeddings"]);
});
