import assert from "node:assert/strict";
import test from "node:test";
import { spawnSync } from "node:child_process";

test("CLI rejects unsupported modes without running tools", () => {
  const result = spawnSync(
    process.execPath,
    [new URL("dependencies.mjs", import.meta.url).pathname, "fix"],
    { encoding: "utf8" },
  );
  assert.equal(result.status, 2);
  assert.match(result.stderr, /Usage:/);
});
