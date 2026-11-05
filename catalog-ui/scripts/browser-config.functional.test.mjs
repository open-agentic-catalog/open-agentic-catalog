// Load the actual Playwright configuration without launching a browser or service.
import assert from "node:assert/strict";
import test from "node:test";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { runtimeEnvironment } from "./browser-fixture.mjs";

test("browser discovery keeps visual comparisons opt-in and requires owned fixtures", () => {
  const cwd = fileURLToPath(new URL("../", import.meta.url));
  const list = (overrides, args = []) =>
    spawnSync(
      process.execPath,
      [
        "node_modules/@playwright/test/cli.js",
        "test",
        "--list",
        "--reporter=list",
        ...args,
      ],
      {
        cwd,
        env: { ...runtimeEnvironment(), ...overrides },
        encoding: "utf8",
        timeout: 30000,
      },
    );
  const missing = list({});
  assert.notEqual(missing.status, 0);
  assert.match(missing.stderr, /owned fixtures/);
  // --list only loads test declarations: these sentinel paths must never be used.
  const inputs = {
    CATALOG_UI_TEST_BUILD: "/nonexistent/catalog-ui-build",
    CATALOG_UI_TEST_OUTPUT: "/nonexistent/catalog-ui-output",
  };
  const normal = list(inputs);
  assert.equal(normal.status, 0, normal.stderr);
  assert.match(normal.stdout, /accessibility\.spec\.ts/);
  assert.match(normal.stdout, /plugins\.spec\.ts/);
  assert.match(normal.stdout, /authoring-preparation\.spec\.ts/);
  assert.doesNotMatch(normal.stdout, /visual\.spec\.ts|performance\.spec\.ts/);
  const names = (stdout) =>
    stdout
      .split("\n")
      .filter((line) => /\.spec\.ts:\d+:\d+/.test(line))
      .map((line) => line.trim());
  const shards = ["--shard=1/2", "--shard=2/2"].map((arg) => {
    const result = list(inputs, [arg]);
    assert.equal(result.status, 0, result.stderr);
    return names(result.stdout);
  });
  const combined = shards.flat();
  assert.equal(
    new Set(combined).size,
    combined.length,
    "No test runs in both shards",
  );
  assert.deepEqual(
    combined.sort(),
    names(normal.stdout).sort(),
    "Both shards cover the complete native discovery",
  );
  const measurement = list({
    ...inputs,
    CATALOG_UI_PERFORMANCE: "/nonexistent/performance",
    CATALOG_UI_PERFORMANCE_SCALE: "6",
  });
  assert.equal(measurement.status, 0, measurement.stderr);
  assert.match(measurement.stdout, /Total: 1 test in 1 file/);
  assert.match(measurement.stdout, /performance\.spec\.ts/);
  assert.doesNotMatch(measurement.stdout, /visual\.spec\.ts|catalog\.spec\.ts/);
  assert.notEqual(
    list({
      ...inputs,
      CATALOG_UI_PERFORMANCE: "/nonexistent/performance",
      CATALOG_UI_E2E_COVERAGE: "/nonexistent/coverage",
    }).status,
    0,
  );
  assert.notEqual(
    list({
      ...inputs,
      CATALOG_UI_PERFORMANCE: "/nonexistent/performance",
      CATALOG_UI_PERFORMANCE_SCALE: "0",
    }).status,
    0,
  );
  const visual = list({ ...inputs, CATALOG_UI_VISUAL: "1" });
  assert.equal(visual.status, 0, visual.stderr);
  assert.match(visual.stdout, /visual\.spec\.ts/);
  assert.match(
    visual.stdout,
    /native preparation and captured Listing metadata/,
  );
  assert.match(
    visual.stdout,
    /API endpoint sections and readable authentication/,
  );
  assert.match(visual.stdout, /API definition index and declared schema facts/);
  assert.match(visual.stdout, /Explore kind markers use the Catalog palette/);
  assert.match(
    visual.stdout,
    /Operational Job outcomes keep contextual colors/,
  );
  assert.match(
    visual.stdout,
    /Settings actions distinguish save from confirmed reset/,
  );
  assert.match(
    visual.stdout,
    /public About and Getting started use small familiar symbols/,
  );
  assert.match(visual.stdout, /Overview kind cards share the existing palette/);
  assert.match(
    visual.stdout,
    /MCP client configuration controls in light and narrow dark layouts/,
  );
  assert.match(visual.stdout, /Total: 20 tests in 1 file/);
  assert.doesNotMatch(
    visual.stdout,
    /accessibility\.spec\.ts|plugins\.spec\.ts/,
  );
  const configuration = spawnSync(
    process.execPath,
    [
      "--experimental-strip-types",
      "--input-type=module",
      "--eval",
      'import config from "./playwright.config.ts"; console.log(JSON.stringify(config));',
    ],
    {
      cwd,
      env: { ...runtimeEnvironment(), ...inputs, CATALOG_UI_VISUAL: "1" },
      encoding: "utf8",
      timeout: 10000,
    },
  );
  assert.equal(configuration.status, 0, configuration.stderr);
  const performanceConfig = spawnSync(
    process.execPath,
    [
      "--experimental-strip-types",
      "--input-type=module",
      "--eval",
      'import config from "./playwright.config.ts"; console.log(JSON.stringify(config));',
    ],
    {
      cwd,
      env: {
        ...runtimeEnvironment(),
        ...inputs,
        CATALOG_UI_PERFORMANCE: "/nonexistent/performance",
        CATALOG_UI_PERFORMANCE_SCALE: "6",
      },
      encoding: "utf8",
      timeout: 10000,
    },
  );
  assert.equal(performanceConfig.status, 0, performanceConfig.stderr);
  const perf = JSON.parse(performanceConfig.stdout);
  assert.equal(perf.workers, 1);
  assert.equal(perf.timeout, 360000);
  assert.equal(perf.expect.timeout, 60000);
  assert.equal(perf.globalTimeout, 3600000);
  assert.equal(perf.use.screenshot, "off");
  assert.equal(perf.retries, 0);
  const config = JSON.parse(configuration.stdout);
  assert.equal(config.workers, 1);
  for (const [environment, workers] of [
    [{}, 2],
    [{ CATALOG_UI_E2E_COVERAGE: "/nonexistent/coverage" }, 4],
  ]) {
    const result = spawnSync(
      process.execPath,
      [
        "--experimental-strip-types",
        "--input-type=module",
        "--eval",
        'import config from "./playwright.config.ts"; console.log(JSON.stringify(config));',
      ],
      {
        cwd,
        env: { ...runtimeEnvironment(), ...inputs, ...environment },
        encoding: "utf8",
        timeout: 10000,
      },
    );
    assert.equal(result.status, 0, result.stderr);
    const ordinary = JSON.parse(result.stdout);
    assert.equal(ordinary.workers, workers);
    assert.equal(ordinary.globalTimeout, 600000);
    assert.equal(ordinary.timeout, 60000);
    assert.equal(ordinary.expect.timeout, 10000);
    assert.equal(ordinary.retries, 0);
  }
  assert.equal(
    config.updateSnapshots,
    "none",
    "Missing/changed baselines must fail without explicit update",
  );
  assert.equal(config.retries, 0);
  assert.equal(config.forbidOnly, true);
  assert.equal(config.use.trace, "off");
  assert.equal(config.use.video, "off");
  assert.equal(config.use.locale, "en-US");
  assert.equal(config.use.timezoneId, "UTC");
  assert.equal(config.use.deviceScaleFactor, 1);
  assert.equal(config.expect.toHaveScreenshot.maxDiffPixels, 0);
  assert.equal(config.expect.toHaveScreenshot.threshold, 0.05);
});
