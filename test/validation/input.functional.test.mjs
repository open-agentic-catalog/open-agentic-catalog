import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { readBounded } from "./input.mjs";
import { execute } from "./process.mjs";

test("subprocesses preserve exit status and bound runtime/output", async () => {
  const result = await execute(process.execPath, ["-e", 'process.stdout.write("ok"); process.stderr.write("detail"); process.exitCode=7']);
  assert.deepEqual(result, { code: 7, stdout: "ok", stderr: "detail" });
  await assert.rejects(execute("/missing/catalog-validation-tool", []), /unavailable/);
  await assert.rejects(execute(process.execPath, ["-e", "setInterval(()=>{},1000)"], { timeout: 100 }), /deadline/);
  await assert.rejects(execute(process.execPath, ["-e", 'process.stdout.write("x".repeat(10000))'], { limit: 128 }), /output exceeds/);
});

test("file reads reject FIFO inputs without opening them", async () => {
  const dir = await mkdtemp(path.join(tmpdir(), "catalog-api-input-test-"));
  try {
    if (process.platform !== "win32") {
      const fifo = path.join(dir, "pipe");
      assert.equal((await execute("mkfifo", [fifo])).code, 0);
      await assert.rejects(readBounded(fifo), /bounded regular file/);
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});
