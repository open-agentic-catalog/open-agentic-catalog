import assert from "node:assert/strict";
import test from "node:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  completeEngineReport,
  engineJourneys,
  journeyResults,
  lockedPlaywrightVersion,
  selectedEngine,
} from "./engine-qualification.mjs";

test("additional engines require an exclusive, explicit selection", () => {
  assert.equal(selectedEngine(["--visual"]), undefined);
  for (const engine of ["firefox", "webkit"])
    assert.equal(selectedEngine(["--engine", engine]), engine);
  for (const args of [
    ["--engine"],
    ["--engine", "chromium"],
    ["--coverage", "--engine", "webkit"],
    ["--engine", "firefox", "--retries=1"],
  ])
    assert.throws(() => selectedEngine(args));
});

test("a stale Playwright runner, launcher or binary manifest cannot qualify a locked installation", async (t) => {
  const ui = await mkdtemp(join(tmpdir(), "catalog-engine-lock-"));
  t.after(() => rm(ui, { recursive: true, force: true }));
  const packages = {};
  for (const name of ["@playwright/test", "playwright", "playwright-core"]) {
    const path = "node_modules/" + name;
    packages[path] = { version: "1.2.3" };
    await mkdir(join(ui, path), { recursive: true });
    await writeFile(
      join(ui, path, "package.json"),
      JSON.stringify(packages[path]),
    );
  }
  await writeFile(join(ui, "package-lock.json"), JSON.stringify({ packages }));
  assert.equal(await lockedPlaywrightVersion(ui), "1.2.3");
  for (const [path, pinned] of Object.entries(packages)) {
    const manifest = join(ui, path, "package.json");
    await writeFile(manifest, JSON.stringify({ version: "1.2.2" }));
    await assert.rejects(
      lockedPlaywrightVersion(ui),
      /differs from the UI lockfile/,
    );
    await writeFile(manifest, JSON.stringify(pinned));
  }
});

test("qualification requires every named journey once, the pinned engine, real packaged images and cleanup", () => {
  const tests = engineJourneys.map((title) => ({
    title,
    expectedStatus: "passed",
    results: [{ status: "passed" }],
  }));
  const report = {
    engine: "webkit",
    platform: "linux",
    architecture: "x64",
    os: "Ubuntu 24.04 LTS",
    kernel: "6.8",
    playwright: "1.2.3",
    source: { commit: "a".repeat(40), sha256: "b".repeat(64) },
    assetsSHA256: "c".repeat(64),
    journeys: journeyResults(tests),
    binary: { revision: "2370", version: "27.2" },
    oidc: {
      engine: "webkit",
      status: "passed",
      browser: "27.2",
      images: ["sha256:" + "a".repeat(64), "sha256:" + "b".repeat(64)],
      cleanup: "passed",
    },
    cleanup: "passed",
  };
  assert.equal(completeEngineReport(report), true);
  for (const replacement of [
    [],
    tests.slice(1),
    [...tests, tests[0]],
    [{ ...tests[0], results: [] }, ...tests.slice(1)],
    [{ ...tests[0], results: [{ status: "skipped" }] }, ...tests.slice(1)],
    [
      { ...tests[0], results: [{ status: "failed" }, { status: "passed" }] },
      ...tests.slice(1),
    ],
    [{ ...tests[0], expectedStatus: "failed" }, ...tests.slice(1)],
  ])
    assert.equal(
      completeEngineReport({
        ...report,
        journeys: journeyResults(replacement),
      }),
      false,
    );
  assert.equal(completeEngineReport({ ...report, journeys: [] }), false);
  for (const key of [
    "engine",
    "os",
    "kernel",
    "playwright",
    "platform",
    "architecture",
    "source",
    "assetsSHA256",
  ])
    assert.equal(
      completeEngineReport({ ...report, [key]: undefined }),
      false,
      key,
    );
  assert.equal(
    completeEngineReport({ ...report, binary: { version: "27.2" } }),
    false,
  );
  assert.equal(
    completeEngineReport({
      ...report,
      source: { commit: report.source.commit },
    }),
    false,
  );
  assert.equal(completeEngineReport({ ...report, cleanup: "failed" }), false);
  for (const overrides of [
    { engine: "firefox" },
    { browser: "28" },
    { status: "missing" },
    { images: [] },
    { cleanup: "failed" },
  ])
    assert.equal(
      completeEngineReport({
        ...report,
        oidc: { ...report.oidc, ...overrides },
      }),
      false,
    );
});
