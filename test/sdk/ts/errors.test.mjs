// T-561: Pandokos errors are the SDKs' own errors; the provider's errors pass unchanged.
import assert from "node:assert/strict";
import { test } from "node:test";

import Anthropic from "@anthropic-ai/sdk";
import OpenAI from "openai";

import { BASE_URL, CANARY_PROMPT, anthropic, header, openai } from "./helpers.mjs";

const messages = [{ role: "user", content: CANARY_PROMPT }];
const chatParams = (model) => ({ model, messages });
const messageParams = (model) => ({ model, max_tokens: 16, messages });

test("T-561 OpenAI: AuthenticationError, PermissionDeniedError, NotFoundError", async () => {
  await assert.rejects(openai(undefined, "synthetic-not-the-key").chat.completions.create(chatParams("fake-basic")), (err) => {
    assert.ok(err instanceof OpenAI.AuthenticationError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), "unauthenticated");
    return true;
  });
  await assert.rejects(openai().chat.completions.create(chatParams("synthetic-no-such-model")), (err) => {
    assert.ok(err instanceof OpenAI.PermissionDeniedError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), "model_not_allowed");
    return true;
  });
  await assert.rejects(openai(`${BASE_URL}/nope/v1`).chat.completions.create(chatParams("fake-basic")), (err) => {
    assert.ok(err instanceof OpenAI.NotFoundError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), "endpoint_not_supported");
    return true;
  });
});

test("T-561 OpenAI: a provider 429 is a RateLimitError without x-pandokos-error", async () => {
  await assert.rejects(openai().chat.completions.create(chatParams("fake-error-429")), (err) => {
    assert.ok(err instanceof OpenAI.RateLimitError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), undefined);
    return true;
  });
});

test("T-561 Anthropic: AuthenticationError, PermissionDeniedError, NotFoundError", async () => {
  await assert.rejects(anthropic(undefined, "synthetic-not-the-key").messages.create(messageParams("fake-basic")), (err) => {
    assert.ok(err instanceof Anthropic.AuthenticationError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), "unauthenticated");
    assert.equal(err.error?.error?.type, "authentication_error");
    return true;
  });
  await assert.rejects(anthropic().messages.create(messageParams("synthetic-no-such-model")), (err) => {
    assert.ok(err instanceof Anthropic.PermissionDeniedError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), "model_not_allowed");
    assert.equal(err.error?.error?.type, "permission_error");
    return true;
  });
  await assert.rejects(anthropic(`${BASE_URL}/nope`).messages.create(messageParams("fake-basic")), (err) => {
    assert.ok(err instanceof Anthropic.NotFoundError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), "endpoint_not_supported");
    assert.equal(err.error?.error?.type, "not_found_error");
    return true;
  });
});

test("T-561 Anthropic: a provider 429 is a RateLimitError without x-pandokos-error", async () => {
  await assert.rejects(anthropic().messages.create(messageParams("fake-error-429")), (err) => {
    assert.ok(err instanceof Anthropic.RateLimitError, String(err));
    assert.equal(header(err.headers, "x-pandokos-error"), undefined);
    assert.equal(err.error?.error?.type, "rate_limit_error");
    return true;
  });
});

test("T-561 Anthropic: a mid-stream error event raises the stream's error", async () => {
  await assert.rejects(async () => {
    const stream = anthropic().messages.stream(messageParams("fake-stream-error"));
    for await (const _ of stream);
  }, (err) => {
    assert.ok(err instanceof Anthropic.AnthropicError, String(err));
    return true;
  });
});

test("T-561 OpenAI: a mid-stream error event raises the stream's error", async () => {
  await assert.rejects(async () => {
    const stream = await openai().chat.completions.create({ ...chatParams("fake-stream-error"), stream: true });
    for await (const _ of stream);
  }, (err) => {
    assert.ok(err instanceof OpenAI.OpenAIError, String(err));
    return true;
  });
});
