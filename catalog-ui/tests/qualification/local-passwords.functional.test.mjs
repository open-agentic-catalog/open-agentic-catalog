// Explicit native failure control. This is not part of the unit selection.
import assert from "node:assert/strict";
import { test } from "node:test";
import { execFileSync, spawn } from "node:child_process";
import { once } from "node:events";
import {
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { runtimeEnvironment } from "../../scripts/browser-fixture.mjs";

const ui = fileURLToPath(new URL("../../", import.meta.url));
const root = resolve(ui, "..");
const docker = (...args) =>
  execFileSync("docker", args, {
    encoding: "utf8",
    timeout: 60000,
    stdio: ["ignore", "pipe", "pipe"],
  })
    .trim()
    .split("\n")
    .filter(Boolean)
    .sort();
const inventory = () => ({
  containers: docker("ps", "-aq", "--no-trunc").map(
    (id) =>
      docker("inspect", "--format", "{{.Id}} {{.State.StartedAt}}", id)[0],
  ),
  volumes: docker("volume", "ls", "-q"),
  networks: docker("network", "ls", "-q", "--no-trunc"),
});

for (const cause of ["browser-failure", "cleanup-signals", "late-signal"]) {
  test(
    `password ${cause} removes only its owned deployment and private inputs`,
    { timeout: 900000 },
    async () => {
      execFileSync(
        "python3",
        [
          "-c",
          "import os,sys;sys.path.insert(0,'examples/tutorials/local-oidc');import local_auth;local_auth.local_engine(os.environ.copy())",
        ],
        {
          cwd: root,
          env: runtimeEnvironment(),
          timeout: 30000,
          stdio: ["ignore", "pipe", "pipe"],
        },
      );
      const retained = inventory();
      const scratch = await mkdtemp(
        join(tmpdir(), "catalog-password-qualification-"),
      );
      const children = join(scratch, "children");
      await mkdir(children, { mode: 0o700 });
      const preload = join(scratch, "failure.mjs");
      await writeFile(
        preload,
        `
import { chromium } from ${JSON.stringify(pathToFileURL(join(ui, "node_modules/@playwright/test/index.mjs")).href)};
const cause = ${JSON.stringify(cause)};
const log = console.log.bind(console);
console.log = (...args) => {
  if (cause === "late-signal" && String(args[0]).startsWith("Local password workflow report:")) process.emit("SIGTERM");
  if (cause === "cleanup-signals" && args[0] === "Local password cleanup started") {
    log("Native cleanup signal control fired");
    process.kill(process.pid, "SIGTERM");
    process.kill(process.pid, "SIGINT");
  }
  log(...args);
};
const launch = chromium.launch.bind(chromium);
chromium.launch = async (...args) => {
  const browser = await launch(...args);
  if (cause !== "late-signal") browser.newContext = async () => { throw new Error("private-password-failure-sentinel"); };
  if (cause === "browser-failure") {
    const close = browser.close.bind(browser);
    browser.close = async () => { await close(); throw new Error("private-password-failure-sentinel"); };
  }
  return browser;
};
`,
      );
      let child,
        output = "";
      try {
        child = spawn(
          process.execPath,
          [
            "--import",
            preload,
            "scripts/packaged-test.mjs",
            "oidc",
            "--local-passwords",
          ],
          {
            cwd: ui,
            env: { ...runtimeEnvironment(), TMPDIR: children },
            stdio: ["ignore", "pipe", "pipe"],
          },
        );
        child.stdout.on("data", (chunk) => {
          if (output.length < 65536) output += chunk;
        });
        child.stderr.on("data", (chunk) => {
          if (output.length < 65536) output += chunk;
        });
        const timer = setTimeout(() => child.kill("SIGTERM"), 800000);
        let code;
        try {
          [code] = await once(child, "close");
        } finally {
          clearTimeout(timer);
        }
        assert.equal(code, 1);
        assert(!output.includes("private-password-failure-sentinel"));
        const filename = output.match(
          /Local password workflow report: ([^\n]+\/report.json)/,
        )?.[1];
        assert(
          filename &&
            filename.startsWith(
              join(root, "output/playwright/local-passwords/run-"),
            ),
        );
        const report = JSON.parse(await readFile(filename, "utf8"));
        if (cause !== "late-signal") {
          assert.equal(report.status, "failed");
          assert.equal(report.phase, "regular-sign-in");
          assert.deepEqual(report.checks, ["launcher-readiness"]);
        } else {
          assert.equal(report.status, "passed");
          assert.equal(report.checks.length, 15);
          assert(
            report.checks.includes("human-cli-pkce-and-private-credentials"),
          );
        }
        if (cause === "browser-failure")
          assert(output.includes("Owned password browser cleanup failed"));
        if (cause === "cleanup-signals")
          assert(output.includes("Native cleanup signal control fired"));
        assert.equal(report.cleanup, "passed");
        assert.deepEqual(await readdir(children), []);
        assert.deepEqual(inventory(), retained);
      } finally {
        if (child && child.exitCode === null && child.signalCode === null) {
          child.kill("SIGTERM");
          await once(child, "close");
        }
        // Preserve generated project metadata if the supervisor could not clean up.
        if ((await readdir(children)).length === 0)
          await rm(scratch, { recursive: true, force: true });
      }
    },
  );
}
