// T-566: each SDK is built with exactly the base URL the gateway guide gives for it.
// The guide's table (docs/operations/gateway.md) has a column "Base URL"; the path of each cell is
// appended to the gateway's address, with a provider other than openai and anthropic (the example
// provider of the guide, or "<provider>") read as "compat", the compatible provider of the suite.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";

import { ANTHROPIC_BASE, BASE_URL, CANARY_COMPLETION, CANARY_PROMPT, COMPAT_BASE, OPENAI_BASE, anthropic, openai } from "./helpers.mjs";

const guide = path.resolve(import.meta.dirname, "../../../docs/operations/gateway.md");

function compatName(pathname) {
  const [first, ...rest] = pathname.replace(/^\//, "").split("/");
  const provider = first === "openai" || first === "anthropic" ? first : "compat";
  return "/" + [provider, ...rest].join("/");
}

function guideBasePaths() {
  const lines = readFileSync(guide, "utf8").split("\n");
  for (let i = 0; i < lines.length; i += 1) {
    if (!lines[i].trimStart().startsWith("|")) continue;
    const cells = lines[i].trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
    const column = cells.indexOf("Base URL");
    if (column < 0) continue;
    const paths = [];
    for (const row of lines.slice(i + 2)) {
      if (!row.trimStart().startsWith("|")) break;
      const cell = row.trim().replace(/^\||\|$/g, "").split("|")[column].trim().replaceAll("`", "");
      paths.push(compatName(new URL(cell.replace("<provider>", "provider")).pathname));
    }
    return paths;
  }
  throw new Error(`${guide} has no table with a Base URL column`);
}

test("T-566 the guide gives the base URLs the suite uses", () => {
  const paths = guideBasePaths();
  for (const p of ["/openai/v1", "/anthropic", "/compat/v1"]) assert.ok(paths.includes(p), `${p} is not in ${paths}`);
  assert.equal(OPENAI_BASE, `${BASE_URL}/openai/v1`);
  assert.equal(ANTHROPIC_BASE, `${BASE_URL}/anthropic`);
  assert.equal(COMPAT_BASE, `${BASE_URL}/compat/v1`);
});

for (const p of ["/openai/v1", "/compat/v1"]) {
  test(`T-566 the OpenAI SDK with the documented base URL ${p}`, async () => {
    assert.ok(guideBasePaths().includes(p));
    const completion = await openai(BASE_URL + p).chat.completions.create({ model: "fake-basic", messages: [{ role: "user", content: CANARY_PROMPT }] });
    assert.equal(completion.choices[0].message.content, CANARY_COMPLETION);
  });
}

test("T-566 the Anthropic SDK with the documented base URL /anthropic", async () => {
  assert.ok(guideBasePaths().includes("/anthropic"));
  const message = await anthropic(`${BASE_URL}/anthropic`).messages.create({ model: "fake-basic", max_tokens: 16, messages: [{ role: "user", content: CANARY_PROMPT }] });
  assert.equal(message.content[0].text, CANARY_COMPLETION);
});
