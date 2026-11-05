import { test } from "node:test";
import assert from "node:assert/strict";
import {
  authoredPluginDocuments,
  packAuthoredPlugin,
} from "./authoring-archive.mjs";

test("Plugin download packaging rejects extra, missing, duplicate, oversized and unsafe paths before tools or files", async () => {
  for (const documents of [
    null,
    [],
    authoredPluginDocuments.slice(1),
    [...authoredPluginDocuments, authoredPluginDocuments[0]],
    authoredPluginDocuments.map((item, index) =>
      index === 0 ? { ...item, path: "../plugin.json" } : item,
    ),
    authoredPluginDocuments.map((item, index) =>
      index === 0 ? { ...item, content: "x".repeat(16385) } : item,
    ),
  ]) {
    await assert.rejects(
      packAuthoredPlugin("/must-not-write", documents, () =>
        assert.fail("must not execute a tool"),
      ),
      /Invalid owned/,
    );
  }
});
