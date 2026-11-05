// Coverage contains source locations and numeric counters only, never browser payloads.
import { randomUUID, createHash } from "node:crypto";
import {
  readFile,
  writeFile,
  readdir,
  mkdir,
  realpath,
} from "node:fs/promises";
import { join, resolve } from "node:path";
import { isDeepStrictEqual } from "node:util";
import coverage from "istanbul-lib-coverage";
import reporting from "istanbul-lib-report";
import reports from "istanbul-reports";
import { fileURLToPath } from "node:url";
import { coverageSourceFiles } from "./e2e-instrument.mjs";
const { createCoverageMap } = coverage;
const { createContext } = reporting;

/** One collector per owned context, installed before its first page. */
export async function collectBrowserCoverage(context, directory) {
  if (!directory)
    return {
      ready: async () => {},
      flush: async () => {},
      finish: async () => {},
    };
  const documents = new Map();
  const errors = [];
  const binding = "__catalogCoverageReport";
  const setup = new Map();
  const receive = (payload) => {
    const data = JSON.parse(payload);
    if (data.event === "start") {
      if (documents.has(data.id)) errors.push("duplicate document");
      documents.set(data.id, null);
    } else if (!documents.has(data.id)) errors.push("missing document start");
    else documents.set(data.id, data);
  };
  // Install document hooks before any page exists, including auto-navigating popups.
  // A popup may run them before its CDP binding is ready; retain its start marker
  // and register it when ready attaches, without inventing an end checkpoint.
  await context.addInitScript(() => {
    let id;
    let started = false;
    const start = () => {
      if (!/^https?:$/.test(globalThis.location.protocol)) return;
      if (started || typeof globalThis.__catalogCoverageReport !== "function")
        return;
      id = crypto.randomUUID();
      globalThis.__catalogCoverageReport(
        JSON.stringify({ id, event: "start" }),
      );
      started = true;
    };
    const end = () => {
      if (!/^https?:$/.test(globalThis.location.protocol)) return;
      start();
      if (!started) throw new Error("Missing browser coverage binding");
      globalThis.__catalogCoverageReport(
        JSON.stringify({
          id,
          event: "end",
          build: globalThis.__catalogCoverageBuild,
          coverage: globalThis.__coverage__ ?? {},
        }),
      );
    };
    globalThis.__catalogCoverageStart = start;
    globalThis.__catalogCoverageFlush = end;
    globalThis.addEventListener("pagehide", end);
    globalThis.addEventListener("beforeunload", end);
    start();
  });
  const observe = (page) => {
    const ready = (async () => {
      const session = await context.newCDPSession(page);
      session.on("Runtime.bindingCalled", ({ name, payload }) => {
        if (name !== binding) return;
        receive(payload);
      });
      await session.send("Page.enable");
      await session.send("Runtime.enable");
      await session.send("Runtime.addBinding", { name: binding });
    })();
    setup.set(page, ready);
  };
  context.on("page", observe);
  for (const page of context.pages()) observe(page);
  const flush = async (page) => {
    for (const frame of page.frames()) {
      const captured = await frame.evaluate(async () => {
        if (!/^https?:$/.test(globalThis.location.protocol)) return true;
        if (typeof globalThis.__catalogCoverageFlush !== "function")
          return false;
        await globalThis.__catalogCoverageFlush();
        return true;
      });
      if (!captured) throw new Error("Missing browser coverage checkpoint");
    }
  };
  return {
    flush,
    async ready(page) {
      if (!setup.has(page)) throw new Error("Unobserved browser coverage page");
      await setup.get(page);
      // Existing late-attached documents still have no hook and fail flush/finish.
      for (const frame of page.frames())
        await frame.evaluate(() => {
          if (typeof globalThis.__catalogCoverageStart === "function")
            globalThis.__catalogCoverageStart();
        });
    },
    async finish() {
      await Promise.all(setup.values());
      for (const page of context.pages()) await flush(page);
      if (
        errors.length ||
        [...documents.values()].some((entry) => entry === null)
      )
        throw new Error("Incomplete browser coverage documents");
      await mkdir(join(directory, "contexts"), { recursive: true });
      await writeFile(
        join(directory, "contexts", `${randomUUID()}.json`),
        JSON.stringify({
          browser: context.browser().version(),
          documents: [...documents.values()],
        }),
      );
    },
  };
}

/** Validate the original-source maps and merge disjoint documents with a complete denominator. */
export function reconcileCoverage(manifest, documents) {
  const map = createCoverageMap(structuredClone(manifest.inventory));
  let contributions = 0;
  const seen = new Set();
  for (const document of documents) {
    if (!document || seen.has(document.id))
      throw new Error("Missing or duplicate document contribution");
    seen.add(document.id);
    if (!Object.keys(document.coverage).length && !document.build) continue; // Provider/about:blank documents.
    if (document.build !== manifest.build)
      throw new Error("Coverage build mismatch");
    if (!Object.keys(document.coverage).length)
      throw new Error("Missing application counters");
    for (const [path, file] of Object.entries(document.coverage)) {
      const expected = manifest.inventory[path];
      if (
        !expected ||
        file.path !== path ||
        ["statementMap", "fnMap", "branchMap"].some(
          (key) => JSON.stringify(file[key]) !== JSON.stringify(expected[key]),
        )
      )
        throw new Error("Coverage source map mismatch");
      for (const key of ["s", "f", "b"]) {
        if (
          JSON.stringify(Object.keys(file[key])) !==
          JSON.stringify(Object.keys(expected[key]))
        )
          throw new Error("Missing coverage counters");
        for (const [id, value] of Object.entries(file[key])) {
          if (
            key === "b" &&
            (!Array.isArray(value) || value.length !== expected.b[id].length)
          )
            throw new Error("Missing branch counters");
          const counters = key === "b" ? value : [value];
          if (!counters.every((n) => Number.isSafeInteger(n) && n >= 0))
            throw new Error("Invalid coverage counters");
        }
      }
      map.addFileCoverage(file);
      contributions++;
    }
  }
  if (!contributions) throw new Error("No browser coverage contributions");
  return map;
}

async function readBrowserCoverage(
  directory,
  execution,
  ui = fileURLToPath(new URL("../", import.meta.url)),
) {
  try {
    await readFile(join(directory, "failed.json"));
    throw new Error("Failed browser coverage run");
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  if (
    execution.cleanup !== "passed" ||
    execution.status !== "passed" ||
    !Number.isSafeInteger(execution.passed) ||
    execution.passed < 1 ||
    execution.failed !== 0 ||
    execution.skipped !== 0 ||
    !Number.isSafeInteger(execution.contexts) ||
    execution.contexts < 1
  )
    throw new Error("Incomplete browser execution");
  const manifest = JSON.parse(
    await readFile(join(directory, "inventory.json")),
  );
  if (
    !/^[a-f0-9]{64}$/.test(manifest.build ?? "") ||
    !/^[a-f0-9]{64}$/.test(manifest.javascriptSha256 ?? "")
  )
    throw new Error("Incomplete instrumented build");
  if (
    JSON.stringify(coverageSourceFiles(ui)) !==
    JSON.stringify(
      Object.keys(manifest.inventory).sort(
        (a, b) => Number(a > b) - Number(a < b),
      ),
    )
  )
    throw new Error("Coverage source inventory changed during execution");
  const expectedInputs = [
    ...Object.keys(manifest.inventory),
    "package-lock.json",
    "vite.config.ts",
    "scripts/e2e-instrument.mjs",
  ].sort((a, b) => Number(a > b) - Number(a < b));
  if (
    !isDeepStrictEqual(
      Object.keys(manifest.inputs ?? {}).sort(
        (a, b) => Number(a > b) - Number(a < b),
      ),
      expectedInputs,
    )
  )
    throw new Error("Incomplete coverage input inventory");
  for (const [path, hash] of Object.entries(manifest.inputs)) {
    const current = createHash("sha256")
      .update(await readFile(resolve(ui, path)))
      .digest("hex");
    if (current !== hash)
      throw new Error("Coverage source changed during execution");
  }
  const documents = [];
  const browsers = new Set();
  const contexts = await readdir(join(directory, "contexts"));
  if (contexts.length !== execution.contexts)
    throw new Error(
      "Context contribution count does not match declared execution",
    );
  for (const file of contexts) {
    const record = JSON.parse(
      await readFile(join(directory, "contexts", file)),
    );
    if (
      !record.documents.some(
        (document) =>
          document?.build === manifest.build &&
          Object.keys(document.coverage ?? {}).length > 0,
      )
    )
      throw new Error("Missing context application contribution");
    if (typeof record.browser !== "string" || !record.browser.trim())
      throw new Error("Missing browser identity");
    browsers.add(record.browser);
    documents.push(...record.documents);
  }
  if (browsers.size !== 1) throw new Error("Mixed browser versions");
  const map = reconcileCoverage(manifest, documents);
  return { manifest, documents, map, browser: [...browsers][0] };
}

/** Share per-profile and union output without changing the original reports. */
async function writeCoverage(
  directory,
  execution,
  { manifest, documents, map, browser },
) {
  const context = createContext({
    dir: directory,
    coverageMap: map,
    sourceFinder: (path) => manifest.sources[path],
  });
  for (const reporter of [
    "json",
    "lcovonly",
    "html",
    "json-summary",
    "text-summary",
  ])
    reports.create(reporter).execute(context);
  const files = map.files().map((path) => ({
    path,
    executable: Object.keys(map.fileCoverageFor(path).s).length > 0,
    ...map.fileCoverageFor(path).toSummary().toJSON(),
  }));
  files.sort(
    (a, b) =>
      (a.statements.total ? a.statements.covered / a.statements.total : 2) -
        (b.statements.total ? b.statements.covered / b.statements.total : 2) ||
      a.path.localeCompare(b.path),
  );
  const summary = {
    ...execution,
    browser,
    node: process.version,
    javascriptSha256: manifest.javascriptSha256,
    lockSha256: manifest.inputs["package-lock.json"],
    build: manifest.build,
    documents: documents.length,
    totals: map.getCoverageSummary().toJSON(),
    files,
  };
  await writeFile(
    join(directory, "summary.json"),
    JSON.stringify(summary, null, 2),
  );
  const gaps = map.files().map((path) => {
    const file = map.fileCoverageFor(path).toJSON();
    return {
      path,
      statements: Object.entries(file.s)
        .filter(([, hits]) => !hits)
        .map(([id]) => ({ id, location: file.statementMap[id] })),
      branches: Object.entries(file.b).flatMap(([id, hits]) =>
        hits.flatMap((count, arm) =>
          count
            ? []
            : [
                {
                  id,
                  arm,
                  location: file.branchMap[id].locations[arm],
                  branch: file.branchMap[id].loc,
                },
              ],
        ),
      ),
    };
  });
  await writeFile(join(directory, "gaps.json"), JSON.stringify(gaps, null, 2));
  await writeFile(
    join(directory, "files.txt"),
    files
      .map(
        (file) =>
          `${file.path} ${file.statements.covered}/${file.statements.total} statements (${file.statements.pct}%) ${file.branches.covered}/${file.branches.total} branches (${file.branches.pct}%)`,
      )
      .join("\n") + "\n",
  );
  console.log(`E2E source coverage: ${directory}`);
  return summary;
}

export async function reportBrowserCoverage(
  directory,
  execution,
  ui = fileURLToPath(new URL("../", import.meta.url)),
) {
  return writeCoverage(
    directory,
    execution,
    await readBrowserCoverage(directory, execution, ui),
  );
}

/** Read explicitly selected compatible runs; never discover a convenient successful subset. */
export async function combineBrowserCoverage(
  directories,
  output,
  ui = fileURLToPath(new URL("../", import.meta.url)),
) {
  if (!directories.length)
    throw new Error("Select browser coverage run directories explicitly");
  const paths = await Promise.all(directories.map((path) => realpath(path)));
  if (new Set(paths).size !== paths.length)
    throw new Error("Duplicate browser coverage run");
  const outputPath = await realpath(output).catch((error) => {
    if (error.code === "ENOENT") return resolve(output);
    throw error;
  });
  if (paths.includes(outputPath))
    throw new Error("Combined output must not replace an input");
  const documents = [];
  const profiles = new Set();
  let reference,
    contexts = 0;
  for (const path of paths) {
    const saved = JSON.parse(await readFile(join(path, "summary.json")));
    if (!["local", "compose", "oidc", "semantic"].includes(saved.profile))
      throw new Error("Unknown browser coverage profile");
    const run = await readBrowserCoverage(path, saved, ui);
    if (
      saved.documents !== run.documents.length ||
      saved.browser !== run.browser ||
      saved.build !== run.manifest.build ||
      saved.javascriptSha256 !== run.manifest.javascriptSha256 ||
      saved.lockSha256 !== run.manifest.inputs["package-lock.json"] ||
      !isDeepStrictEqual(saved.totals, run.map.getCoverageSummary().toJSON())
    )
      throw new Error("Saved browser summary disagrees with run evidence");
    if (
      reference &&
      (!isDeepStrictEqual(reference.manifest, run.manifest) ||
        reference.browser !== run.browser)
    )
      throw new Error("Incompatible browser coverage runs");
    reference ??= run;
    documents.push(...run.documents);
    profiles.add(saved.profile);
    contexts += saved.contexts;
  }
  const map = reconcileCoverage(reference.manifest, documents);
  await mkdir(output, { recursive: true });
  console.log(
    `Combined browser profiles: ${[...profiles].sort((a, b) => Number(a > b) - Number(a < b)).join(", ")} (explicit selection; no claim of complete workflow coverage)`,
  );
  return writeCoverage(
    output,
    {
      profile: "combined",
      profiles: [...profiles].sort((a, b) => Number(a > b) - Number(a < b)),
      runs: paths,
      contexts,
      status: "passed",
      cleanup: "passed",
      executionUnit: "selected successful runs",
      passed: paths.length,
      failed: 0,
      skipped: 0,
    },
    { ...reference, documents, map },
  );
}
