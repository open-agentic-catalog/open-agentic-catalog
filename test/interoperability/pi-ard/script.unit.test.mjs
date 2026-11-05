import assert from "node:assert/strict";
import test from "node:test";
import { boundedResponses } from "./script.mjs";
const message = (name, args = {}) => ({
  content: [{ type: "toolCall", name, arguments: args }],
});
test("only ARD tools reach the scripted provider", async () => {
  for (const name of ["bash", "read", "mcp"]) {
    const [reply] = boundedResponses([message(name)]);
    await assert.rejects(reply(), /tool budget/);
  }
});
test("factories preserve native context and exact messages while fencing tool 21", async () => {
  const context = { native: true };
  const raw = message("ard_catalog");
  const replies = boundedResponses(
    Array.from({ length: 21 }, () => async (c) => {
      assert.equal(c, context);
      return raw;
    }),
  );
  for (const reply of replies.slice(0, 20))
    assert.equal(await reply(context), raw);
  await assert.rejects(replies[20](context), /tool budget/);
});
test("a continuation walk cannot request page four", async () => {
  const replies = boundedResponses([
    message("ard_search"),
    ...Array.from({ length: 3 }, () =>
      message("ard_search", { pageToken: "native-token" }),
    ),
  ]);
  for (const reply of replies.slice(0, 3)) await reply();
  await assert.rejects(replies[3](), /page budget/);
  const reset = boundedResponses([
    message("ard_search"),
    message("ard_search", { pageToken: "token" }),
    message("ard_search"),
    message("ard_search", { pageToken: "next" }),
  ]);
  for (const reply of reset) await reply();
});
test("invalid scripts and enlarged limits are refused", async () => {
  for (const limit of [0, 21, 1.5])
    assert.throws(() => boundedResponses([], limit));
  assert.throws(() => boundedResponses([], 20, 4));
  assert.throws(() => boundedResponses(Array(42).fill({ content: [] })));
  await assert.rejects(boundedResponses([{}])[0](), /scripted response/);
});
