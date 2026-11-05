import test from "node:test";
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
import { readFile } from "node:fs/promises";
const { helpAnchor, helpTopics } = await import(
  new URL("help.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);

test("Help admits only fixed local topics with real README headings", async () => {
  const source = await readFile(
    new URL("../README.md", import.meta.url),
    "utf8",
  );
  const anchors = [...source.matchAll(/^#{1,3} (.+)$/gm)].map((m) =>
    m[1]
      .toLowerCase()
      .replace(/[^\w\s-]/g, "")
      .replace(/\s/g, "-"),
  );
  // Keep the navigation contract independent of the implementation's table.
  // Swapping two valid headings must still fail this regression.
  const expected = [
    ["start", "first-use-and-command-index"],
    ["setup", "add-a-connection"],
    ["connection", "sign-in-and-sign-out"],
    ["trust", "trust-proxies-and-limits"],
    ["catalog", "browse-the-catalog"],
    ["tools", "manage-my-tools"],
    ["agent", "agent-outcomes"],
  ];
  assert.deepEqual(
    helpTopics.map(({ id, anchor }) => [id, anchor]),
    expected,
  );
  for (const [id, anchor] of expected) {
    assert.equal(helpAnchor(id), anchor);
    assert.ok(anchors.includes(anchor), anchor);
  }
  for (const value of [
    undefined,
    null,
    "",
    "__proto__",
    "https://private.example/?token=secret",
    "#browse-the-catalog",
    { id: "agent" },
    ["tools"],
  ])
    assert.throws(() => helpAnchor(value), /invalid_help_topic/);
});
test("Catalog Help bridge cannot carry arbitrary targets or private link values", async () => {
  const { message } = await import(
    pathToFileURL(process.env.CATALOG_VSCODE_LIB)
  );
  assert.deepEqual(message({ type: "help", revision: 7 }), {
    type: "help",
    revision: 7,
  });
  for (const extra of [
    { url: "https://private.example" },
    { topic: "agent" },
    { query: "private query" },
  ])
    assert.throws(
      () => message({ type: "help", revision: 7, ...extra }),
      /invalid_message/,
    );
});
