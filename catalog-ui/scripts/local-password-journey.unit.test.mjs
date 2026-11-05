import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, mkdir, readFile, readdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { localPasswordJourney } from "./local-password-journey.mjs";

for (const failure of ["engine", "build", "cleanup"]) {
  test(`password qualification ${failure} refusal preserves the safe cleanup outcome`, async (t) => {
    const root = await mkdtemp(join(tmpdir(), "catalog-password-unit-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const privateDirectory = join(root, "private");
    await mkdir(privateDirectory, { mode: 0o700 });
    const calls = [];
    let cleanupStarted = 0;
    const command = async (args) => {
      calls.push(args);
      if (
        args[0] === (failure === "engine" ? "python3" : "make") ||
        (failure === "cleanup" && args[0] === "docker")
      )
        throw new Error("private-failure-sentinel");
      return "";
    };
    await assert.rejects(
      localPasswordJourney({
        command,
        root,
        privateDirectory,
        image: "backend:owned-unit",
        uiImage: "ui:owned-unit",
        onBrowser() {
          assert.fail("A refused prerequisite must not launch a browser");
        },
        onCleanup() {
          cleanupStarted++;
        },
      }),
      /Local password workflow incomplete/,
    );
    assert.equal(cleanupStarted, 1);
    if (failure === "engine") assert.equal(calls.length, 1);
    else if (failure === "build")
      assert.deepEqual(
        calls.filter((args) => args[0] === "docker"),
        [
          ["docker", "image", "ls", "--quiet", "backend:owned-unit"],
          ["docker", "image", "ls", "--quiet", "ui:owned-unit"],
        ],
      );
    if (failure === "cleanup")
      assert((await stat(privateDirectory)).isDirectory());
    else await assert.rejects(stat(privateDirectory), { code: "ENOENT" });
    const output = join(root, "output/playwright/local-passwords");
    const directories = await readdir(output);
    assert.equal(directories.length, 1);
    const raw = await readFile(
      join(output, directories[0], "report.json"),
      "utf8",
    );
    assert(!raw.includes("private-failure-sentinel"));
    assert.deepEqual(JSON.parse(raw), {
      status: "failed",
      phase: "build",
      checks: [],
      cleanup: failure === "cleanup" ? "failed" : "passed",
    });
  });
}
