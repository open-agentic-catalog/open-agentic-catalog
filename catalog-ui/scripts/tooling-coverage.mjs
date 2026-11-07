// Native tooling coverage stays separate from frontend and browser-source coverage.
import { createHash } from "node:crypto";
import { createWriteStream } from "node:fs";
import {
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { pipeline } from "node:stream/promises";
import { run } from "node:test";
import * as reporters from "node:test/reporters";
import { fileURLToPath } from "node:url";

const repository = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const sourceRoots = ["catalog-ui/scripts", "documentation/scripts"];
const maximumBytes = 16 << 20;
const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");
const compare = (a, b) => Number(a > b) - Number(a < b);
const testSource = (path) => /(?:\.test|\.functional)\.mjs$/.test(path);
const lcovCount = /^\d+$/;
const lcovPayloads = new Map([
  ["DA", /^[1-9]\d*,\d+$/],
  ["BRDA", /^[1-9]\d*,\d+,\d+,(?:\d+|-)$/],
  ["FN", /^[1-9]\d*,[^\r\n]+$/],
  ["FNDA", /^\d+,[^\r\n]+$/],
  ["FNF", lcovCount],
  ["FNH", lcovCount],
  ["LF", lcovCount],
  ["LH", lcovCount],
  ["BRF", lcovCount],
  ["BRH", lcovCount],
]);

/** Bind all native sources and assertions, including modules never imported. */
export async function collectSources(root) {
  const sources = {};
  const visit = async (path) => {
    const info = await lstat(join(root, path));
    if (info.isSymbolicLink()) throw new Error("Tooling source is a symlink");
    if (info.isDirectory()) {
      for (const name of (await readdir(join(root, path))).sort(compare))
        await visit(`${path}/${name}`);
    } else if (path.endsWith(".mjs")) {
      if (!info.isFile() || info.size > maximumBytes || /[\r\n]/.test(path))
        throw new Error("Invalid tooling source");
      sources[path] = digest(await readFile(join(root, path)));
    }
  };
  for (const path of sourceRoots) await visit(path);
  if (!Object.keys(sources).length)
    throw new Error("No native tooling sources");
  return sources;
}

/** Keep the owning native unit selection and the packaged script argv intact. */
export async function profileSelection(profile, root) {
  if (profile === "compose")
    return {
      cwd: join(root, "catalog-ui"),
      files: [join(root, "catalog-ui/scripts/packaged-test.mjs")],
      argv: ["compose"],
      scope: "packaged-browser",
      receiptProfile: "node-compose",
    };
  const owner = { "ui-unit": "catalog-ui", "docs-unit": "documentation" }[
    profile
  ];
  if (!owner) throw new Error("Select ui-unit, docs-unit or compose");
  const files = (await readdir(join(root, owner, "scripts")))
    .filter((name) => name.endsWith(".unit.test.mjs"))
    .sort(compare)
    .map((name) => join(root, owner, "scripts", name));
  if (!files.length) throw new Error("No native tooling unit tests");
  return {
    cwd: join(root, owner),
    files,
    argv: [],
    scope: "unit",
    receiptProfile: profile === "ui-unit" ? "node-ui-unit" : "node-docs-unit",
  };
}

/** Normalize native reporter paths; never invent counters for unobserved roots. */
export function normalizeLcov(raw, root, cwd, measuredFiles) {
  const allowed = new Set(measuredFiles);
  const observed = new Set();
  let inRecord = false;
  const framed = raw.split("\n");
  const last = framed.length - 1;
  if (framed[last] === "") framed.pop();
  const lines = framed.map((row, index) => {
    const line = index < last && row.endsWith("\r") ? row.slice(0, -1) : row;
    if (line.includes("\r")) throw new Error("Invalid native LCOV row");
    if (line.startsWith("SF:")) {
      if (inRecord) throw new Error("Unterminated native LCOV record");
      const path = relative(root, resolve(cwd, line.slice(3)))
        .split(sep)
        .join("/");
      if (!allowed.has(path) || observed.has(path))
        throw new Error("Unexpected or duplicate native LCOV source");
      observed.add(path);
      inRecord = true;
      return `SF:${path}`;
    }
    if (line === "end_of_record") {
      if (!inRecord) throw new Error("Unexpected native LCOV record end");
      inRecord = false;
    } else if (!(line === "TN:" && !inRecord)) {
      const separator = line.indexOf(":");
      const payload = lcovPayloads.get(line.slice(0, separator));
      if (
        !inRecord ||
        separator < 0 ||
        !payload?.test(line.slice(separator + 1))
      )
        throw new Error("Invalid native LCOV row");
    }
    return line;
  });
  if (inRecord || !observed.size || lines.at(-1) !== "end_of_record")
    throw new Error("Empty or incomplete native LCOV");
  return {
    lcov: lines.join("\n") + "\n",
    observedSourceFiles: [...observed].sort(compare),
  };
}

async function capture(selection, root, pending, output) {
  let failed = false;
  let coverageEvents = 0;
  const stream = run({
    files: selection.files,
    argv: selection.argv,
    isolation: "process",
    concurrency: 1,
    coverage: true,
    coverageIncludeGlobs: sourceRoots.map((path) =>
      join(root, path, "**/*.mjs"),
    ),
    coverageExcludeGlobs: ["**/*.test.mjs", "**/*.functional.mjs"],
  });
  stream.on("data", (event) => {
    if (event.type === "test:fail") failed = true;
    if (event.type === "test:coverage") coverageEvents++;
  });
  const lcov = reporters.lcov;
  const spec = reporters.spec();
  stream.on("error", (error) => {
    lcov.destroy(error);
    spec.destroy(error);
  });
  const reports = [
    pipeline(lcov, createWriteStream(pending, { flags: "wx", mode: 0o600 })),
    pipeline(spec, output, { end: false }),
  ];
  stream.pipe(lcov);
  stream.pipe(spec);
  await Promise.all(reports);
  if (failed || coverageEvents !== 1)
    throw new Error("Native tooling selection failed or coverage is missing");
}

/** Run one existing selection; publish a source-bound receipt only on success. */
export async function runCoverage(
  profile,
  root = repository,
  output = process.stdout,
) {
  root = resolve(root);
  const selection = await profileSelection(profile, root);
  const sources = await collectSources(root);
  const measuredFiles = Object.keys(sources).filter(
    (path) => !testSource(path),
  );
  const parent = join(root, "output/coverage/tooling-node");
  await mkdir(parent, { recursive: true });
  const directory = await mkdtemp(join(parent, "run-"));
  const pending = join(directory, "lcov.pending");
  const previous = process.cwd();
  try {
    process.chdir(selection.cwd);
    await capture(selection, root, pending, output);
    const after = await collectSources(root);
    if (JSON.stringify(sources) !== JSON.stringify(after))
      throw new Error("Native tooling sources changed during selection");
    const raw = await readFile(pending);
    if (raw.length > maximumBytes) throw new Error("Native LCOV exceeds bound");
    const normalized = normalizeLcov(
      raw.toString("utf8"),
      root,
      selection.cwd,
      measuredFiles,
    );
    await writeFile(pending, normalized.lcov, { mode: 0o600 });
    await rename(pending, join(directory, "lcov.info"));
    const report = {
      schemaVersion: 1,
      status: "passed",
      kind: "javascript",
      profile: selection.receiptProfile,
      sourceFiles: sources,
      reports: [
        {
          format: "lcov",
          path: relative(root, join(directory, "lcov.info"))
            .split(sep)
            .join("/"),
          sha256: digest(normalized.lcov),
        },
      ],
      tool: { name: "node", version: process.version },
      sourceRoots,
      observedSourceFiles: normalized.observedSourceFiles,
      unobservedSourceFiles: measuredFiles.filter(
        (path) => !normalized.observedSourceFiles.includes(path),
      ),
      selection: {
        files: selection.files.map((path) =>
          relative(root, path).split(sep).join("/"),
        ),
        argv: selection.argv,
        scope: selection.scope,
      },
    };
    await writeFile(
      join(directory, "report.pending"),
      JSON.stringify(report, null, 2) + "\n",
      { mode: 0o600 },
    );
    await rename(
      join(directory, "report.pending"),
      join(directory, "report.json"),
    );
    return join(directory, "report.json");
  } catch (error) {
    await rm(directory, { recursive: true, force: true });
    throw error;
  } finally {
    process.chdir(previous);
  }
}

if (
  process.argv[1] &&
  isAbsolute(process.argv[1]) &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    if (process.argv.length !== 3)
      throw new Error("Select one tooling profile");
    console.log(await runCoverage(process.argv[2]));
  } catch {
    console.error(
      "Native tooling coverage failed; no successful receipt published.",
    );
    process.exitCode = 1;
  }
}
