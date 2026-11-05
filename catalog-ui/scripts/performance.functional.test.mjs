import assert from "node:assert/strict";
import { test } from "node:test";
import { execFileSync } from "node:child_process";
import {
  mkdtemp,
  mkdir,
  readFile,
  writeFile,
  rm,
  access,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:http";
import { setTimeout as delay } from "node:timers/promises";
import {
  sourceIdentity,
  buildInventory,
  finishPerformance,
} from "./performance-files.mjs";
import { checkAssets, journeys } from "./performance.mjs";
import { ready, startProcess, stopProcess } from "./browser-fixture.mjs";

async function fixture(fn) {
  const directory = await mkdtemp(join(tmpdir(), "catalog-ui-performance-"));
  try {
    await fn(directory);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}
test("real build inventory rejects wrong builds and enforces exact byte budgets", async () =>
  fixture(async (directory) => {
    await mkdir(join(directory, ".vite"));
    await writeFile(
      join(directory, ".vite/manifest.json"),
      JSON.stringify({ "index.html": { isEntry: true, file: "app.js" } }),
    );
    await writeFile(
      join(directory, "index.html"),
      '<script type="module" src="app.js"></script>',
    );
    await writeFile(join(directory, "app.js"), 'console.log("production");');
    const first = await buildInventory(directory);
    const budget = {
      entryJavaScript: 26,
      entryCSS: 1,
      totalJavaScript: 26,
      totalCSS: 1,
    };
    checkAssets(first, budget);
    assert.equal(first.entry.javaScript, 26);
    await writeFile(join(directory, "app.js"), 'console.log("production!");');
    const larger = await buildInventory(directory);
    assert.notEqual(larger.sha256, first.sha256);
    assert.throws(() => checkAssets(larger, budget), /Asset budget exceeded/);
    await writeFile(join(directory, "app.js"), "__coverage__.counter++;");
    await assert.rejects(buildInventory(directory), /Instrumented/);
    await writeFile(join(directory, "app.js"), 'console.log("production");');
    await symlink(join(directory, "app.js"), join(directory, "linked.js"));
    await assert.rejects(buildInventory(directory), /Unsupported build/);
  }));

test("report publication requires complete real execution evidence and never masks missing samples", async () =>
  fixture(async (directory) => {
    const budgets = {
      entryJavaScript: 500,
      entryCSS: 100,
      totalJavaScript: 1000,
      totalCSS: 200,
      searchResponse: 65536,
    };
    const build = {
      entry: { javaScript: 100, css: 20 },
      total: { javaScript: 150, css: 25 },
    };
    await writeFile(
      join(directory, "execution.json"),
      JSON.stringify({ status: "passed", passed: 1, failed: 0, skipped: 0 }),
    );
    const samples = journeys.flatMap((journey) =>
      Array.from({ length: 6 }, (_, iteration) => ({
        journey,
        iteration,
        durationMs: 10,
        requests: {
          search: ["bootstrap", "detail"].includes(journey) ? 0 : 1,
          detail: journey === "detail" ? 1 : 0,
          assets: 2,
          other: 0,
        },
        searchBytes: ["bootstrap", "detail"].includes(journey) ? 0 : 100,
        resources: {
          entries: 2,
          transferBytes: 100,
          encodedBytes: 150,
          decodedBytes: 200,
          zeroTransferEntries: 1,
          assetTransferBytes: 100,
          cachedJavaScriptEntries: 1,
        },
      })),
    );
    const write = (input) =>
      writeFile(
        join(directory, "samples.json"),
        JSON.stringify({ browser: "test-browser", samples: input }),
      );
    await write(samples);
    await finishPerformance(directory, { fixture: true }, build, budgets);
    const result = await readFile(join(directory, "report.json"), "utf8");
    assert.equal(JSON.parse(result).cleanup, "passed");
    for (const execution of [
      { status: "failed", passed: 1, failed: 0, skipped: 0 },
      { status: "passed", passed: 0, failed: 0, skipped: 0 },
      { status: "passed", passed: 1, failed: 0, skipped: 1 },
      { status: "passed", passed: 2, failed: 0, skipped: 0 },
    ]) {
      await writeFile(
        join(directory, "execution.json"),
        JSON.stringify(execution),
      );
      await assert.rejects(
        finishPerformance(directory, {}, build, budgets),
        /Incomplete/,
      );
      assert.equal(
        await readFile(join(directory, "report.json"), "utf8"),
        result,
      );
    }
    await writeFile(
      join(directory, "execution.json"),
      JSON.stringify({ status: "passed", passed: 1, failed: 0, skipped: 0 }),
    );
    await write(samples.slice(1));
    await rm(join(directory, "report.json"));
    await assert.rejects(
      finishPerformance(directory, {}, build, budgets),
      /Incomplete/,
    );
    await assert.rejects(access(join(directory, "report.json")));
  }));

test("performance readiness observes real delayed health, elapsed deadlines and owned cleanup", async () => {
  let requests = 0;
  const server = createServer((_request, response) => {
    requests++;
    response.writeHead(requests >= 2 ? 200 : 503);
    response.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const child = startProcess(process.execPath, [
    "-e",
    "setInterval(() => {}, 1000)",
  ]);
  try {
    const origin = `http://127.0.0.1:${server.address().port}`;
    await ready(origin, child, { readiness: 2000, probe: 500 });
    assert.ok(requests >= 2);
    server.removeAllListeners("request");
    server.on("request", async (_request, response) => {
      await delay(100);
      response.writeHead(503);
      response.end();
    });
    await assert.rejects(
      ready(origin, child, { readiness: 200, probe: 50 }),
      /did not become ready/,
    );
    await stopProcess(child);
    await assert.rejects(
      ready(origin, child, { readiness: 200, probe: 50 }),
      /exited before readiness/,
    );
  } finally {
    await stopProcess(child);
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
});

test("source identity binds tracked and untracked inputs while excluding ignored reports", async () =>
  fixture(async (directory) => {
    const git = (...args) =>
      execFileSync("git", args, {
        cwd: directory,
        timeout: 10000,
        stdio: ["ignore", "pipe", "pipe"],
      });
    git("init");
    await writeFile(join(directory, ".gitignore"), "ignored/\n");
    await writeFile(join(directory, "source.txt"), "first");
    await writeFile(join(directory, "empty.txt"), "");
    git("add", ".");
    git(
      "-c",
      "user.name=Fixture",
      "-c",
      "user.email=fixture@example.invalid",
      "-c",
      "commit.gpgsign=false",
      "-c",
      "core.hooksPath=/dev/null",
      "commit",
      "-m",
      "fixture",
    );
    const initial = await sourceIdentity(directory);
    await mkdir(join(directory, "ignored"));
    await writeFile(join(directory, "ignored/report.json"), "{}");
    assert.deepEqual(await sourceIdentity(directory), initial);
    await rm(join(directory, "empty.txt"));
    assert.notEqual((await sourceIdentity(directory)).sha256, initial.sha256);
    await writeFile(join(directory, "empty.txt"), "");
    assert.deepEqual(await sourceIdentity(directory), initial);
    await writeFile(join(directory, "new.txt"), "new input");
    assert.notEqual((await sourceIdentity(directory)).sha256, initial.sha256);
    await rm(join(directory, "new.txt"));
    await writeFile(join(directory, "source.txt"), "changed");
    assert.notEqual((await sourceIdentity(directory)).sha256, initial.sha256);
    await rm(join(directory, "source.txt"));
    assert.notEqual((await sourceIdentity(directory)).sha256, initial.sha256);
  }));
