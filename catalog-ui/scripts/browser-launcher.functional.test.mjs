// A bounded child control qualifies launcher interruption without Go builds or a browser install.
import assert from "node:assert/strict";
import { test } from "node:test";
import { spawn } from "node:child_process";
import { once } from "node:events";
import {
  mkdtemp,
  mkdir,
  readdir,
  readFile,
  writeFile,
  rm,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { runtimeEnvironment } from "./browser-fixture.mjs";

test(
  "launcher interruption removes child scratch files and refuses a successful performance report",
  { timeout: 15000 },
  async () => {
    const directory = await mkdtemp(join(tmpdir(), "catalog-ui-launcher-"));
    const scratch = join(directory, "scratch");
    const bin = join(directory, "bin");
    const ui = fileURLToPath(new URL("../", import.meta.url));
    await mkdir(scratch);
    await mkdir(bin);
    await writeFile(
      join(bin, "go"),
      `#!${process.execPath}
import {writeFileSync} from 'node:fs';
import {join} from 'node:path';
if(process.argv[2] === 'version') console.log('fixture-go');
else {
 writeFileSync(join(process.env.TMPDIR, 'child-scratch'), 'owned');
 console.log('owned-child-ready');
 setInterval(() => {}, 1000);
}
`,
      { mode: 0o700 },
    );
    const child = spawn(
      process.execPath,
      ["scripts/browser-test.mjs", "--performance"],
      {
        cwd: ui,
        env: {
          ...runtimeEnvironment(),
          PATH: bin + ":" + process.env.PATH,
          TMPDIR: scratch,
          UI_PERFORMANCE_TIMEOUT_SCALE: "1",
        },
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    const done = once(child, "close");
    let stdout = "";
    let stderr = "";
    let signaled = false;
    child.stdout.on("data", (data) => {
      stdout += data;
      if (!signaled && stdout.includes("owned-child-ready")) {
        signaled = true;
        child.kill("SIGTERM");
      }
    });
    child.stderr.on("data", (data) => {
      stderr += data;
    });
    const timer = setTimeout(() => child.kill("SIGTERM"), 10000);
    let report;
    try {
      const [code] = await done;
      assert.equal(signaled, true, stderr);
      assert.equal(code, 1, stderr);
      assert.deepEqual(
        await readdir(scratch),
        [],
        "All child temporary paths belong to the removed private build",
      );
      const match = stdout.match(/Browser results: (.+)/);
      assert.ok(match, stdout);
      report = resolve(match[1]);
      assert.ok(
        report.startsWith(resolve(ui, "../output/performance/ui") + "/run-"),
      );
      const failure = JSON.parse(
        await readFile(join(report, "failed.json"), "utf8"),
      );
      assert.equal(failure.reason, "interrupted");
      assert.equal(failure.phase, "build");
      assert.ok(!(await readdir(report)).includes("report.json"));
    } finally {
      clearTimeout(timer);
      if (child.exitCode === null && child.signalCode === null) {
        child.kill("SIGTERM");
        await done;
      }
      if (report) await rm(report, { recursive: true, force: true });
      await rm(directory, { recursive: true, force: true });
    }
  },
);
