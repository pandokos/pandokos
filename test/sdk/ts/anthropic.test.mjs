// T-559, T-560, T-565: the Anthropic SDK against the anthropic family.
import assert from "node:assert/strict";
import { test } from "node:test";

import Anthropic from "@anthropic-ai/sdk";

import { ANTHROPIC_BASE, CANARY_COMPLETION, CANARY_PROMPT, KEY, anthropic, oneRecord, requestIdOf } from "./helpers.mjs";

const messages = [{ role: "user", content: CANARY_PROMPT }];

test("T-559 messages, plain", async () => {
  const { data, response } = await anthropic().messages.create({ model: "fake-basic", max_tokens: 16, messages }).withResponse();
  assert.equal(data.content[0].text, CANARY_COMPLETION);
  assert.deepEqual([data.usage.input_tokens, data.usage.output_tokens], [12, 7]);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.provider, rec.api_family, rec.operation, rec.outcome], ["anthropic", "anthropic", "messages", "ok"]);
  assert.deepEqual([rec.input_source, rec.output_source, rec.input_tokens, rec.output_tokens, rec.stream], ["provider", "provider", 12, 7, false]);
});

test("T-559 messages, streamed with the SDK's stream helper", async () => {
  const stream = anthropic().messages.stream({ model: "fake-basic", max_tokens: 16, messages });
  const { response } = await stream.withResponse();
  let text = "";
  stream.on("text", (delta) => {
    text += delta;
  });
  const final = await stream.finalMessage();
  assert.equal(text, CANARY_COMPLETION);
  assert.deepEqual([final.usage.input_tokens, final.usage.output_tokens], [12, 7]);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.equal(rec.stream, true);
  assert.deepEqual([rec.input_source, rec.output_source, rec.input_tokens, rec.output_tokens], ["provider", "provider", 12, 7]);
});

test("T-559 messages, streamed with cache usage: the input is inclusive", async () => {
  const stream = anthropic().messages.stream({ model: "fake-cache", max_tokens: 16, messages });
  const { response } = await stream.withResponse();
  const final = await stream.finalMessage();
  assert.equal(final.usage.cache_read_input_tokens, 2000);
  assert.equal(final.usage.output_tokens, 50);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.input_tokens, rec.cache_read_tokens, rec.cache_write_tokens, rec.output_tokens], [2310, 2000, 300, 50]);
  assert.equal(rec.other_units.cache_write_1h_tokens, 200);
});

test("T-560 token counting", async () => {
  const { data, response } = await anthropic().messages.countTokens({ model: "fake-basic", messages }).withResponse();
  assert.equal(data.input_tokens, 42);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.equal(rec.operation, "messages.count_tokens");
  assert.deepEqual([rec.input_source, rec.output_source, rec.cost_status, rec.input_tokens, rec.output_tokens], ["none", "none", "none", 0, 0]);
});

test("T-565 api_key (a bearer token) authenticates", async () => {
  const { data } = await anthropic(ANTHROPIC_BASE, KEY).messages.create({ model: "fake-basic", max_tokens: 16, messages }).withResponse();
  assert.equal(data.content[0].text, CANARY_COMPLETION);
});

test("T-565 an x-api-key header without api_key authenticates", async () => {
  const client = new Anthropic({ baseURL: ANTHROPIC_BASE, apiKey: null, authToken: null, defaultHeaders: { "x-api-key": KEY }, maxRetries: 0, timeout: 20000 });
  const { data, response } = await client.messages.create({ model: "fake-basic", max_tokens: 16, messages }).withResponse();
  assert.equal(data.content[0].text, CANARY_COMPLETION);
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual([rec.operation, rec.outcome], ["messages", "ok"]);
});

test("T-565 a wrong key is an AuthenticationError", async () => {
  const wrong = [
    anthropic(ANTHROPIC_BASE, "synthetic-not-the-key"),
    new Anthropic({ baseURL: ANTHROPIC_BASE, apiKey: null, authToken: null, defaultHeaders: { "x-api-key": "synthetic-not-the-key" }, maxRetries: 0, timeout: 20000 }),
  ];
  for (const client of wrong) {
    await assert.rejects(client.messages.create({ model: "fake-basic", max_tokens: 16, messages }), (err) => {
      assert.ok(err instanceof Anthropic.AuthenticationError, String(err));
      return true;
    });
  }
});
