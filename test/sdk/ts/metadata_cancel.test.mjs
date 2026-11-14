// T-562, T-563: metadata and trace headers; cancellation.
import assert from "node:assert/strict";
import { test } from "node:test";

import { CANARY_PROMPT, anthropic, header, oneRecord, openai, requestIdOf, upstreamCancelled, upstreamLast } from "./helpers.mjs";

const messages = [{ role: "user", content: CANARY_PROMPT }];
const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
const headers = {
  "X-Pandokos-Metadata-Team": "alpha",
  "X-Pandokos-Metadata-Job": "nightly-7",
  "X-Pandokos-Metadata--bad": "dropped",
  Traceparent: `00-${TRACE}-00f067aa0ba902b7-01`,
};

async function nothingOfPandokosUpstream() {
  const names = (await upstreamLast()).header_names.map((n) => n.toLowerCase());
  assert.deepEqual(names.filter((n) => n.startsWith("x-pandokos")), []);
  assert.ok(!names.includes("traceparent") && !names.includes("tracestate"), String(names));
}

test("T-562 OpenAI: metadata is recorded, never forwarded; the trace is honoured", async () => {
  const { response } = await openai().chat.completions.create({ model: "fake-basic", messages }, { headers }).withResponse();
  assert.equal(header(response.headers, "x-pandokos-metadata-dropped"), "1");
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual(rec.metadata, { team: "alpha", job: "nightly-7" });
  assert.equal(rec.metadata_dropped, 1);
  assert.equal(rec.trace_id, TRACE);
  await nothingOfPandokosUpstream();
});

test("T-562 Anthropic: metadata is recorded, never forwarded; the trace is honoured", async () => {
  const { response } = await anthropic().messages.create({ model: "fake-basic", max_tokens: 16, messages }, { headers }).withResponse();
  assert.equal(header(response.headers, "x-pandokos-metadata-dropped"), "1");
  const rec = await oneRecord(requestIdOf(response.headers));
  assert.deepEqual(rec.metadata, { team: "alpha", job: "nightly-7" });
  assert.equal(rec.metadata_dropped, 1);
  assert.equal(rec.trace_id, TRACE);
  await nothingOfPandokosUpstream();
});

test("T-562 no dropped header when nothing is dropped", async () => {
  const { response } = await anthropic()
    .messages.create({ model: "fake-basic", max_tokens: 16, messages }, { headers: { "X-Pandokos-Metadata-Team": "alpha" } })
    .withResponse();
  assert.equal(header(response.headers, "x-pandokos-metadata-dropped"), undefined);
  assert.deepEqual((await oneRecord(requestIdOf(response.headers))).metadata, { team: "alpha" });
});

test("T-563 OpenAI: a stream aborted after the first event is cancelled", async () => {
  const { data: stream, response } = await openai()
    .chat.completions.create({ model: "fake-stall-after-first", messages, stream: true })
    .withResponse();
  const rid = requestIdOf(response.headers);
  for await (const _ of stream) {
    stream.controller.abort();
    break;
  }
  assert.equal(await upstreamCancelled("5s"), true);
  assert.equal((await oneRecord(rid)).outcome, "cancelled");
});

test("T-563 Anthropic: a stream aborted after the first event is cancelled", async () => {
  const stream = anthropic().messages.stream({ model: "fake-stall-after-first", max_tokens: 16, messages });
  stream.on("error", () => {});
  const { response } = await stream.withResponse();
  const rid = requestIdOf(response.headers);
  for await (const event of stream) {
    assert.equal(event.type, "message_start");
    stream.controller.abort();
    break;
  }
  await stream.done().catch(() => {});
  assert.equal(await upstreamCancelled("5s"), true);
  const rec = await oneRecord(rid);
  assert.equal(rec.outcome, "cancelled");
  assert.deepEqual([rec.input_source, rec.input_tokens, rec.output_source], ["provider", 12, "estimated"]);
});
