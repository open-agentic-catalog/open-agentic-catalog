// Explicit real-image qualification; ordinary UI tests remain independent of Docker.
import assert from "node:assert/strict";
import { test } from "node:test";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { readdirSync } from "node:fs";
import { mkdtemp, mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { runtimeEnvironment } from "../../scripts/browser-fixture.mjs";

const ui = fileURLToPath(new URL("../../", import.meta.url));
const root = resolve(ui, "..");
const docker = (...args) =>
  execFileSync("docker", args, {
    encoding: "utf8",
    timeout: 90000,
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
const rows = (value) => (value ? value.split("\n") : []);
const baseline = () =>
  docker("ps", "-aq", "--no-trunc")
    .split("\n")
    .filter(Boolean)
    .map((id) =>
      docker(
        "inspect",
        "--format",
        "{{.Id}} {{.Image}} {{.State.Status}} {{.State.StartedAt}}",
        id,
      ),
    )
    .sort();

for (const cause of ["SIGTERM", "SIGINT", "UNHANDLED", "STEP_FAILURE"]) {
  test(
    `packaged ${cause} drains pending browser failures and removes its owned fixture`,
    { timeout: 300000 },
    async () => {
      const retained = baseline();
      const outputRoot = join(root, "output/playwright/ui-interruption");
      await mkdir(outputRoot, { recursive: true });
      const output = await mkdtemp(join(outputRoot, "run-"));
      const scratch = join(output, "scratch");
      await mkdir(scratch);
      const preload = join(output, "preload.mjs");
      // Use a real Playwright waiter that cannot finish before browser closure.
      // This isolates the unhandled deferred-rejection path without faking API success.
      await writeFile(
        preload,
        `
import { chromium } from ${JSON.stringify(pathToFileURL(join(ui, "node_modules/playwright/index.mjs")).href)};
let notified = false;
const cause = ${JSON.stringify(cause)};
const launch = chromium.launch.bind(chromium);
chromium.launch = async (...args) => {
  const browser = await launch(...args);
  const newContext = browser.newContext.bind(browser);
  browser.newContext = async (...options) => {
    const context = await newContext(...options);
    const newPage = context.newPage.bind(context);
    context.newPage = async (...pageOptions) => {
      const page = await newPage(...pageOptions);
      const wait = page.waitForResponse.bind(page);
      page.waitForResponse = (...waitOptions) => {
        const response = wait(...waitOptions);
        if (!notified) {
          notified = true;
          wait(() => false);
          console.log("owned-pending-browser-wait-ready");
          if (cause === "UNHANDLED") Promise.reject(new Error("synthetic owned task failure"));
          if (cause === "STEP_FAILURE") {
            const failed = Promise.reject(new Error("synthetic owned step failure"));
            void failed.catch(() => {});
            return failed;
          }
        }
        return response;
      };
      return page;
    };
    return context;
  };
  return browser;
};
`,
      );
      const child = spawn(
        process.execPath,
        ["--import", preload, "scripts/packaged-test.mjs", "compose"],
        {
          cwd: ui,
          env: { ...runtimeEnvironment(), TMPDIR: scratch },
          stdio: ["ignore", "pipe", "pipe"],
        },
      );
      const closed = once(child, "close");
      let signaled = false;
      let observed = "";
      const projects = new Set();
      child.stdout.on("data", (data) => {
        observed = (observed + data).slice(-8192);
        if (
          !signaled &&
          observed.includes("owned-pending-browser-wait-ready")
        ) {
          signaled = true;
          for (const name of readdirSync(scratch))
            if (/^catalog-ui-packaged-[a-z0-9]{6}$/i.test(name))
              projects.add(name);
          if (cause.startsWith("SIG")) child.kill(cause);
        }
      });
      // Native fixture output can contain private inputs; it is not copied into reports.
      child.stderr.resume();
      const graceful = setTimeout(() => child.kill("SIGTERM"), 210000);
      const force = setTimeout(() => child.kill("SIGKILL"), 240000);
      const report = {
        cause,
        status: "failed",
        fallbackCleanup: "not_needed",
      };
      try {
        const [code, exitSignal] = await closed;
        report.exitCode = code;
        report.exitSignal = exitSignal;
        assert.equal(
          signaled,
          true,
          "The real pending-browser-wait control was not reached",
        );
        assert.equal(
          code,
          1,
          "An interrupted campaign must fail after its cleanup",
        );
        assert.deepEqual(
          (await readdir(scratch)).filter((name) =>
            name.startsWith("catalog-ui-packaged-"),
          ),
          [],
          "The packaged supervisor left private fixture inputs",
        );
        assert.deepEqual(
          baseline(),
          retained,
          "Owned containers must be removed; retained containers must remain unchanged",
        );
        assert.equal(
          projects.size,
          1,
          "The control must own one exact Compose project",
        );
        for (const name of projects) {
          const project = name.toLowerCase();
          const filter = "label=com.docker.compose.project=" + project;
          for (const kind of ["volume", "network"])
            assert.equal(
              docker(kind, "ls", "-q", "--filter", filter),
              "",
              "Owned fixture infrastructure remains",
            );
          for (const repository of [
            "open-agentic-catalog",
            "open-agentic-catalog-ui",
            "open-agentic-catalog-fixture",
          ])
            assert.equal(
              docker("image", "ls", "--quiet", repository + ":" + project),
              "",
              "Owned fixture image tag remains",
            );
        }
        report.status = "passed";
      } finally {
        clearTimeout(graceful);
        clearTimeout(force);
        if (child.exitCode === null && child.signalCode === null) {
          child.kill("SIGKILL");
          await closed;
        }
        // On a failing control, clean only projects created below this test's private scratch root.
        for (const name of await readdir(scratch))
          if (/^catalog-ui-packaged-[a-z0-9]{6}$/i.test(name))
            projects.add(name);
        for (const name of projects) {
          if (!/^catalog-ui-packaged-[a-z0-9]{6}$/i.test(name)) continue;
          const project = name.toLowerCase();
          const filter = "label=com.docker.compose.project=" + project;
          const ids = rows(
            docker("ps", "-aq", "--no-trunc", "--filter", filter),
          );
          let removed =
            ids.length > 0 || (await readdir(scratch)).includes(name);
          for (const id of ids) {
            assert(!retained.some((row) => row.startsWith(id + " ")));
            docker("rm", "-f", id);
          }
          for (const kind of ["volume", "network"])
            for (const id of rows(
              docker(kind, "ls", "-q", "--filter", filter),
            )) {
              removed = true;
              docker(kind, "rm", id);
            }
          for (const repository of [
            "open-agentic-catalog",
            "open-agentic-catalog-ui",
            "open-agentic-catalog-fixture",
          ]) {
            const tag = repository + ":" + project;
            if (docker("image", "ls", "--quiet", tag)) {
              removed = true;
              docker("image", "rm", tag);
            }
          }
          await rm(join(scratch, name), { recursive: true, force: true });
          if (removed) report.fallbackCleanup = "completed";
        }
        assert.deepEqual(baseline(), retained);
        await writeFile(
          join(output, "report.json"),
          JSON.stringify(report, null, 2) + "\n",
        );
        await rm(scratch, { recursive: true, force: true });
        await rm(preload, { force: true });
        console.log("Owned interruption evidence: " + output);
      }
    },
  );
}
