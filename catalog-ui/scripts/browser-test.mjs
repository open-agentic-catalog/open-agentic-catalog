// Build into a private temporary directory, leaving a running ui-dev untouched.
import {
  access,
  mkdtemp,
  mkdir,
  rm,
  readFile,
  writeFile,
} from "node:fs/promises";

import { firefox, webkit } from "@playwright/test";
import {
  completeEngineReport,
  engineJourneys,
  lockedPlaywrightVersion,
  selectedEngine,
} from "./engine-qualification.mjs";
import { spawn, spawnSync } from "node:child_process";
import { release, arch, tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { reportBrowserCoverage } from "./e2e-coverage.mjs";
import { performancePolicy, checkAssets } from "./performance.mjs";
import {
  buildInventory,
  performanceInputs,
  sourceIdentity,
  finishPerformance,
} from "./performance-files.mjs";
import { browserSelections, runtimeEnvironment } from "./browser-fixture.mjs";

const ui = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const root = resolve(ui, "..");
// Select visual tests only through this launcher; ambient Catalog settings are stripped.
const measuring = process.argv[2] === "--performance";
const policy = measuring
  ? performancePolicy(process.env.UI_PERFORMANCE_TIMEOUT_SCALE)
  : undefined;
const coverage = process.argv[2] === "--coverage";
const visual = process.argv[2] === "--visual";
const engine = selectedEngine(process.argv.slice(2));
const testArgs = engine
  ? []
  : process.argv.slice(visual || coverage || measuring ? 3 : 2);
if ((coverage || measuring) && testArgs.length)
  throw new Error("Measurement requires the complete local browser selection");
const directory = await mkdtemp(join(tmpdir(), "catalog-ui-browser-"));
const outputRoot = join(
  root,
  engine
    ? "output/playwright/engines/" + engine
    : measuring
      ? "output/performance/ui"
      : coverage
        ? "output/coverage/e2e/local"
        : "output/playwright/ui-tests",
);
await mkdir(outputRoot, { recursive: true });
const output = await mkdtemp(join(outputRoot, "run-"));
const report = engine && {
  engine,
  platform: process.platform,
  architecture: arch(),
  kernel: release(),
  status: "failed",
  journeys: engineJourneys.map((title) => ({ title, status: "missing" })),
  oidc: { status: "missing" },
  cleanup: "failed",
};
let active;
let inputs;
let build;
let budgets;
let phase = "build";
let interrupted = false;
let timedOut = false;
/** Signal only the active invocation-owned process group, tolerating an already exited child. */
function signalGroup(signal) {
  if (!active?.pid) return;
  try {
    process.kill(-active.pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}
/** Stop admitting build/test phases and request graceful child termination. */
function interrupt() {
  interrupted = true;
  signalGroup("SIGTERM");
}
process.on("SIGINT", interrupt);
process.on("SIGTERM", interrupt);
/** Run one bounded phase with explicit fixture paths and reap its owned descendants. */
async function run(
  command,
  args,
  cwd = ui,
  testOutput = join(engine ? directory : output, "tests"),
) {
  if (interrupted) throw new Error("Browser tests interrupted");
  active = spawn(command, args, {
    cwd,
    detached: true,
    stdio: "inherit",
    env: {
      ...runtimeEnvironment(),
      // Own compiler/browser/cache scratch paths, including interrupted runs.
      TMPDIR: engine && phase === "packaged-oidc" ? tmpdir() : directory,
      CATALOG_UI_TEST_BUILD: directory,
      CATALOG_UI_TEST_OUTPUT: testOutput,
      ...(engine
        ? {
            CATALOG_UI_ENGINE: engine,
            CATALOG_UI_ENGINE_VERSION: report.binary.version,
            CATALOG_UI_ENGINE_OUTPUT: output,
            PLAYWRIGHT_NO_COPY_PROMPT: "1",
          }
        : {}),
      ...(coverage ? { CATALOG_UI_E2E_COVERAGE: output } : {}),
      ...(visual ? { CATALOG_UI_VISUAL: "1" } : {}),
      ...(measuring
        ? {
            CATALOG_UI_PERFORMANCE: output,
            CATALOG_UI_PERFORMANCE_SCALE: String(policy.scale),
          }
        : {}),
    },
  });
  const timer = setTimeout(
    () => {
      interrupted = true;
      timedOut = true;
      signalGroup("SIGTERM");
      forceStop();
    },
    policy?.phase ?? 12 * 60 * 1000,
  );
  let killTimer;
  const forceStop = () => {
    // The packaged child owns Compose shutdown, image tags and private credentials.
    killTimer ??= setTimeout(
      () => signalGroup("SIGKILL"),
      engine && phase === "packaged-oidc" ? 210000 : 10000,
    );
  };
  process.on("SIGINT", forceStop);
  process.on("SIGTERM", forceStop);
  try {
    const code = await new Promise((resolve, reject) => {
      active.once("error", reject);
      active.once("exit", resolve);
    });
    if (code !== 0 || interrupted)
      throw new Error(`${command} failed or was interrupted`);
  } finally {
    clearTimeout(timer);
    clearTimeout(killTimer);
    process.off("SIGINT", forceStop);
    process.off("SIGTERM", forceStop);
    // Reap only this command's process group, including abandoned fixture children.
    signalGroup("SIGKILL");
    active = undefined;
  }
}
try {
  if (engine) {
    phase = "prerequisites";
    report.source = await sourceIdentity(root);
    phase = "playwright-lock";
    report.playwright = await lockedPlaywrightVersion(ui);
    const { browsers } = JSON.parse(
      await readFile(join(ui, "node_modules/playwright-core/browsers.json")),
    );
    const pin = browsers.find((b) => b.name === engine);
    report.binary = { revision: pin.revision, version: pin.browserVersion };
    report.os = (await readFile("/etc/os-release", "utf8")).match(
      /^PRETTY_NAME="([^"]+)"/m,
    )?.[1];
    if (!report.os?.startsWith("Ubuntu 24.04") || arch() !== "x64")
      throw new Error("Select Ubuntu 24.04 x86-64 for this qualification");
    phase = "browser-install";
    await access({ firefox, webkit }[engine].executablePath(), 1).catch(() => {
      throw new Error("Engine binary missing. Run make ui-engine-install");
    });
    const tool = engine === "firefox" ? "certutil" : "bwrap";
    phase = tool;
    if (
      spawnSync("which", [tool], { stdio: "ignore", timeout: 5000 }).status !==
      0
    )
      throw new Error("Missing engine prerequisite " + tool);

    phase = "build";
  }
  if (measuring) {
    inputs = await performanceInputs(root, policy);
    budgets = JSON.parse(
      await readFile(join(ui, "scripts/performance-budgets.json"), "utf8"),
    );
  }
  for (const [name, pkg] of [
    ["catalogd", "./cmd/catalogd"],
    ["catalogctl", "./cmd/catalogctl"],
    ["catalog-ui", "./catalog-ui/cmd/catalog-ui"],
    ["demo-archives", "./test/demo"],
  ])
    await run(
      "go",
      [
        "build",
        "-mod=readonly",
        "-trimpath",
        ...(name === "catalogd"
          ? ["-ldflags", `-X main.version=${basename(directory)}`]
          : []),
        "-o",
        join(directory, name),
        pkg,
      ],
      root,
    );
  await run(process.execPath, [
    "node_modules/vite/bin/vite.js",
    "build",
    "--outDir",
    join(directory, "assets"),
    ...(measuring || engine ? ["--manifest"] : []),
  ]);
  if (measuring) {
    build = await buildInventory(join(directory, "assets"));
    phase = "assets";
    await writeFile(
      join(output, "inputs.json"),
      JSON.stringify({ inputs, build, budgets }, null, 2),
    );
    checkAssets(build, budgets);
  }
  if (engine)
    report.assetsSHA256 = (
      await buildInventory(join(directory, "assets"))
    ).sha256;
  phase = "browser";
  const selections = browserSelections(
    testArgs,
    coverage || visual || measuring || !!engine,
  );
  for (const [index, args] of selections.entries()) {
    await run(
      process.execPath,
      ["node_modules/@playwright/test/cli.js", "test", ...args],
      ui,
      selections.length > 1
        ? join(output, `shard-${index + 1}`, "tests")
        : join(engine ? directory : output, "tests"),
    );
  }
  if (engine) {
    phase = "packaged-oidc";
    await run(process.execPath, [
      "scripts/packaged-test.mjs",
      "oidc",
      "--engine",
      engine,
    ]);
    phase = "reconciliation";
    const after = await sourceIdentity(root);
    if (
      after.commit !== report.source.commit ||
      after.sha256 !== report.source.sha256
    )
      throw new Error("Qualification source changed");
  }
} catch (error) {
  if (measuring)
    await writeFile(
      join(output, "failed.json"),
      JSON.stringify({
        status: "failed",
        phase,
        reason: timedOut
          ? "timeout"
          : interrupted
            ? "interrupted"
            : "execution",
      }),
    );
  if (coverage)
    await writeFile(
      join(output, "failed.json"),
      JSON.stringify({ status: "failed" }),
    );
  console.error(error.message);
  process.exitCode = 1;
} finally {
  process.off("SIGINT", interrupt);
  process.off("SIGTERM", interrupt);
  try {
    await rm(directory, { recursive: true, force: true });
    if (engine) report.cleanup = "passed";
  } catch {
    process.exitCode = 1;
    if (coverage)
      await writeFile(
        join(output, "failed.json"),
        JSON.stringify({ status: "failed", phase: "cleanup" }),
      );
    if (measuring)
      await writeFile(
        join(output, "failed.json"),
        JSON.stringify({ status: "failed", phase: "cleanup" }),
      );
    console.error("Owned browser build cleanup failed");
  }
  if (engine) {
    for (const file of ["journeys", "oidc"]) {
      try {
        report[file] = JSON.parse(await readFile(join(output, file + ".json")));
      } catch (error) {
        if (error.code !== "ENOENT") {
          console.error("Invalid engine evidence: " + file);
          process.exitCode = 1;
        }
      }
    }
    const passed =
      !process.exitCode && !interrupted && completeEngineReport(report);
    report.status = passed ? "passed" : "failed";
    report.phase = phase;
    if (!passed) process.exitCode = 1;
    await writeFile(
      join(output, "report.json"),
      JSON.stringify(report, null, 2),
    );
  }
  console.log(`Browser results: ${output}`);
}

if (coverage && !process.exitCode) {
  try {
    const execution = JSON.parse(
      await readFile(join(output, "execution.json")),
    );
    await reportBrowserCoverage(output, {
      ...execution,
      profile: "local",
      cleanup: "passed",
      executionUnit: "test case",
    });
  } catch (error) {
    await writeFile(
      join(output, "failed.json"),
      JSON.stringify({ status: "failed" }),
    );
    console.error(error.message);
    process.exitCode = 1;
  }
}

if (measuring && !process.exitCode) {
  try {
    const after = await sourceIdentity(root);
    if (
      after.sha256 !== inputs.source.sha256 ||
      after.commit !== inputs.source.commit
    )
      throw new Error("Performance source changed during execution");
    await finishPerformance(output, inputs, build, budgets);
  } catch (error) {
    await writeFile(
      join(output, "failed.json"),
      JSON.stringify({ status: "failed", phase: "reconciliation" }),
    );
    console.error(error.message);
    process.exitCode = 1;
  }
}
