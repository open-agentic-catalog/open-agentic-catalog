import { test } from "node:test";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";

const run = promisify(execFile);
const moduleURL = new URL("./fixture-inputs.mjs", import.meta.url).href;
const child = `
import { writeFixtureInput } from ${JSON.stringify(moduleURL)};
process.umask(Number.parseInt(process.argv[1], 8));
await writeFixtureInput(process.argv[2], "mask control\\n");
`;

for (const mask of ["022", "077"]) {
  test(
    `fresh fixture input keeps container read access under umask ${mask}`,
    { skip: process.platform === "win32" ? "POSIX umask control" : false },
    async (t) => {
      const parentMask = process.umask();
      const parent = await mkdtemp(join(tmpdir(), "catalog-fixture-mask-"));
      t.after(() => rm(parent, { recursive: true, force: true }));
      const path = join(parent, "input");
      await assert.rejects(stat(path), { code: "ENOENT" });
      await run(
        process.execPath,
        ["--input-type=module", "--eval", child, mask, path],
        { timeout: 5000, maxBuffer: 8192 },
      );
      assert.equal(await readFile(path, "utf8"), "mask control\n");
      assert.equal((await stat(path)).mode & 0o777, 0o444);
      assert.equal((await stat(parent)).mode & 0o777, 0o700);
      assert.equal(process.umask(), parentMask);
    },
  );
}
