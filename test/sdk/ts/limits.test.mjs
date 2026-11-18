// T-690: Pandokos limit refusals are the SDKs' own rate-limit errors, for rate_limited and for quota_exceeded.
import assert from "node:assert/strict";
import { test } from "node:test";

import Anthropic from "@anthropic-ai/sdk";
import OpenAI from "openai";

import { ANTHROPIC_BASE, CANARY_PROMPT, OPENAI_BASE, anthropic, header, openai } from "./helpers.mjs";

const messages = [{ role: "user", content: CANARY_PROMPT }];

for (const [kind, code] of [["RATE", "rate_limited"], ["QUOTA", "quota_exceeded"]]) {
  const key = (family) => {
    const value = process.env[`PANDOKOS_KEY_${kind}_${family}`];
    if (!value) throw new Error(`PANDOKOS_KEY_${kind}_${family} is not set: run the suite through the Go harness`);
    return value;
  };

  test(`T-690 OpenAI: ${code} is a RateLimitError with the status, the header and Retry-After`, async () => {
    const client = openai(OPENAI_BASE, key("OPENAI"));
    await client.chat.completions.create({ model: "fake-basic", messages });
    await assert.rejects(client.chat.completions.create({ model: "fake-basic", messages }), (err) => {
      assert.ok(err instanceof OpenAI.RateLimitError, String(err));
      assert.equal(err.status, 429);
      assert.equal(header(err.headers, "x-pandokos-error"), code);
      assert.match(header(err.headers, "retry-after"), /^[0-9]+$/);
      assert.ok(header(err.headers, "x-pandokos-limit-metric"));
      return true;
    });
  });

  test(`T-690 Anthropic: ${code} is a RateLimitError with the status, the header and Retry-After`, async () => {
    const client = anthropic(ANTHROPIC_BASE, key("ANTHROPIC"));
    await client.messages.create({ model: "fake-basic", max_tokens: 16, messages });
    await assert.rejects(client.messages.create({ model: "fake-basic", max_tokens: 16, messages }), (err) => {
      assert.ok(err instanceof Anthropic.RateLimitError, String(err));
      assert.equal(err.status, 429);
      assert.equal(header(err.headers, "x-pandokos-error"), code);
      assert.match(header(err.headers, "retry-after"), /^[0-9]+$/);
      assert.equal(err.error?.error?.type, "rate_limit_error");
      return true;
    });
  });
}
