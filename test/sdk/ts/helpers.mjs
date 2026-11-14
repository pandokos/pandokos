// Helpers of the SDK suite: clients built as the gateway guide says, and the control server.
// The scripts hold no canary of their own: the harness passes the values in the environment.
import Anthropic from "@anthropic-ai/sdk";
import OpenAI from "openai";

export const BASE_URL = process.env.PANDOKOS_BASE_URL;
export const KEY = process.env.PANDOKOS_KEY;
export const CONTROL_URL = process.env.PANDOKOS_CONTROL_URL;
export const CANARY_PROMPT = process.env.CANARY_PROMPT;
export const CANARY_COMPLETION = process.env.CANARY_COMPLETION;

for (const [name, value] of Object.entries({ BASE_URL, KEY, CONTROL_URL, CANARY_PROMPT, CANARY_COMPLETION })) {
  if (!value) throw new Error(`${name} is not set: run the suite through the Go harness`);
}

export const REQUEST_ID_HEADER = "x-pandokos-request-id";
export const OPENAI_BASE = `${BASE_URL}/openai/v1`;
export const ANTHROPIC_BASE = `${BASE_URL}/anthropic`;
export const COMPAT_BASE = `${BASE_URL}/compat/v1`;

export function openai(baseURL = OPENAI_BASE, apiKey = KEY) {
  return new OpenAI({ baseURL, apiKey, maxRetries: 0, timeout: 20000 });
}

export function anthropic(baseURL = ANTHROPIC_BASE, apiKey = KEY, extra = {}) {
  return new Anthropic({ baseURL, apiKey, maxRetries: 0, timeout: 20000, ...extra });
}

/** The value of a header of a fetch Headers object or of a plain object. */
export function header(headers, name) {
  if (!headers) return undefined;
  if (typeof headers.get === "function") return headers.get(name) ?? undefined;
  return headers[name] ?? headers[name.toLowerCase()];
}

export function requestIdOf(headers) {
  const value = header(headers, REQUEST_ID_HEADER);
  if (!value) throw new Error(`no ${REQUEST_ID_HEADER} header`);
  return value;
}

async function get(path) {
  const response = await fetch(CONTROL_URL + path);
  return response.json();
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/** The records of a request, polling the control server for at most 5 seconds. */
export async function records(requestId, want = 1) {
  const deadline = Date.now() + 5000;
  for (;;) {
    const found = await get(`/records?request_id=${encodeURIComponent(requestId)}`);
    if (found.length >= want || Date.now() > deadline) return found;
    await sleep(20);
  }
}

export async function oneRecord(requestId) {
  const found = await records(requestId);
  if (found.length !== 1) throw new Error(`${found.length} records for ${requestId}`);
  return found[0];
}

export const upstreamLast = () => get("/upstream/last");

export async function upstreamCancelled(wait = "5s") {
  return (await get(`/upstream/cancelled?wait=${wait}`)).cancelled;
}
