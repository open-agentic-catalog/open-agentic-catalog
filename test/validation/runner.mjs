#!/usr/bin/env node
// Local validation only. Acquisition is explicit and never occurs during a check.
import { readFile, writeFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { cleanEnvironment, execute, Failure } from "./process.mjs";

export const DIRECTORY = path.dirname(fileURLToPath(import.meta.url));
export const ROOT = path.resolve(DIRECTORY, "../..");
export const CONTROL = path.join(ROOT, "api/openapi/control-v1.yaml");
const CONFIG = path.join(DIRECTORY, "redocly.yaml");
const REDOCLY = path.join(DIRECTORY, "node_modules/@redocly/cli/bin/cli.js");
const OASDIFF = path.join(DIRECTORY, ".tools", process.platform === "win32" ? "oasdiff.exe" : "oasdiff");
const STAMP = path.join(DIRECTORY, ".tools/installed.json");
const LOCKS = ["package.json", "package-lock.json", "tools/go.mod", "tools/go.sum"];
const hash = (data) => createHash("sha256").update(data).digest("hex");

// nodeVersion enforces the supported host toolchain before running local tools.
function nodeVersion() {
  const [major, minor] = process.versions.node.split(".").map(Number);
  if (major !== 22 || minor < 13) throw new Failure("validation requires Node 22.13+ within major 22");
}

// identities binds an installation stamp to all npm and Go tool lock inputs.
async function identities() {
  return Object.fromEntries(await Promise.all(LOCKS.map(async (name) =>
    [name, hash(await readFile(path.join(DIRECTORY, name)))])));
}

// pins reads the exact direct npm tool versions expected by the launcher.
async function pins() {
  return JSON.parse(await readFile(path.join(DIRECTORY, "package.json"), "utf8")).dependencies;
}

// prerequisites verifies installed pins, lock identities and the oasdiff binary;
// it never repairs or downloads tools as a side effect of validation.
export async function prerequisites() {
  nodeVersion();
  try {
    const stamp = JSON.parse(await readFile(STAMP, "utf8"));
    if (JSON.stringify(stamp.locks) !== JSON.stringify(await identities()) ||
        stamp.binary !== hash(await readFile(OASDIFF))) throw new Error("stale installation");
    for (const [name, version] of Object.entries(await pins())) {
      const installed = JSON.parse(await readFile(path.join(DIRECTORY, "node_modules", name, "package.json"), "utf8"));
      if (installed.version !== version) throw new Error("wrong version");
    }
  } catch {
    throw new Failure("validation tools are missing or stale; run make validation-tools-install");
  }
}

// install explicitly acquires locked tools and stamps only a verified installation.
// A lock change during acquisition invalidates the operation instead of blessing drift.
async function install() {
  nodeVersion();
  const before = await identities();
  const env = cleanEnvironment(process.env, true);
  console.log("Installing locked API validation tools (network acquisition)...");
  const npm = await execute("npm", ["ci", "--ignore-scripts", "--no-audit", "--no-fund"], {
    cwd: DIRECTORY, env, timeout: 300_000, limit: 8 << 20,
  });
  if (npm.code) throw new Failure("npm ci failed; check npm prerequisites and registry access", npm.code);
  await mkdir(path.dirname(OASDIFF), { recursive: true });
  const result = await execute("go", ["build", "-mod=readonly", "-trimpath", "-o", OASDIFF, "github.com/oasdiff/oasdiff"], {
    cwd: path.join(DIRECTORY, "tools"), env, timeout: 300_000, limit: 8 << 20,
  });
  if (result.code) throw new Failure("oasdiff build failed; check Go prerequisites and module access", result.code);
  const metadata = await execute("go", ["version", "-m", OASDIFF], { cwd: DIRECTORY, env });
  if (metadata.code || !/\tmod\tgithub.com\/oasdiff\/oasdiff\tv1\.33\.0\t/.test(metadata.stdout))
    throw new Failure("oasdiff executable does not match the pinned module");
  if (JSON.stringify(before) !== JSON.stringify(await identities())) throw new Failure("tool locks changed during installation");
  await writeFile(STAMP, JSON.stringify({ locks: before, binary: hash(await readFile(OASDIFF)) }, null, 2) + "\n");
  await prerequisites();
  console.log("Installed Redocly " + (await pins())["@redocly/cli"] + " and oasdiff v1.33.0; no tools are shipped with Catalog.");
}

// Keep diagnostics machine-readable: JSON escapes control characters in tool
// messages and locations, without echoing codeframes or unrestricted stderr.
function problems(report) {
  for (const item of report.problems.slice(0, 100)) console.log(JSON.stringify({
    rule: item.ruleId, severity: item.severity, message: item.message,
    locations: item.location?.map((location) => location.pointer),
  }));
  console.log(JSON.stringify({ tool: "redocly", version: report.version, totals: report.totals }));
  if (report.problems.length > 100) console.log("Further diagnostics omitted; narrow the input after resolving these findings.");
}

// Keep the checked-in configuration limited to built-in rules. Remote presets,
// custom plugins and decorators are not part of this local, read-only gate.
export function validateRules(config) {
  const allowed = new Set(["struct", "operation-operationId", "operation-operationId-unique",
    "operation-parameters-unique", "path-parameters-defined", "no-identical-paths", "no-ambiguous-paths",
    "no-invalid-media-type-examples", "no-invalid-parameter-examples", "no-invalid-schema-examples",
    "operation-summary", "operation-tag-defined", "tag-description", "security-defined",
    "no-unused-components", "component-name-unique"]);
  if (!config || Object.keys(config).some((key) => !["extends", "resolve", "rules"].includes(key)) ||
      JSON.stringify(config.extends) !== '["spec"]' ||
      JSON.stringify(config.resolve) !== '{"doNotResolveExamples":true}' ||
      !config.rules || typeof config.rules !== "object" || Array.isArray(config.rules))
    throw new Failure("lint configuration must use the local built-in rule profile");
  for (const [name, rule] of Object.entries(config.rules)) {
    if (!allowed.has(name)) throw new Failure("unknown or unreviewed lint rule");
    const options = typeof rule === "string" ? { severity: rule } : rule;
    if (!options || typeof options !== "object" ||
        Object.keys(options).some((key) => !["severity", "allowAdditionalProperties"].includes(key)) ||
        !["error", "warn", "off"].includes(options.severity) ||
        ("allowAdditionalProperties" in options && typeof options.allowAdditionalProperties !== "boolean"))
      throw new Failure("unsupported lint rule configuration");
  }
}

// lintSnapshot runs the reviewed rule profile and checks source/configuration identity
// afterwards. Findings remain structured; raw tool stderr is not a user diagnostic.
export async function lintSnapshot(input, cwd, config = CONFIG, show = true) {
  const { parseInput } = await import("./input.mjs");
  const configRaw = await readFile(config);
  validateRules(parseInput(configRaw));
  const configBefore = hash(configRaw);
  // No caller-selected plugins/configuration. The parameter is used only by
  // qualification to exercise configuration rejection without changing sources.
  const result = await execute(process.execPath, [REDOCLY, "lint", input.filename,
    "--config", config, "--lint-config", "error", "--format", "json", "--max-problems", "1000"], { cwd });
  const { unchanged } = await import("./input.mjs");
  await unchanged(input);
  if (hash(await readFile(config)) !== configBefore) throw new Failure("lint configuration changed during validation");
  let report;
  try {
    report = JSON.parse(result.stdout);
    if (report.version !== (await pins())["@redocly/cli"] || !Array.isArray(report.problems) ||
        !Number.isInteger(report.totals?.errors) || !Number.isInteger(report.totals?.warnings))
      throw new Error("invalid report");
  } catch {
    throw new Failure("Redocly did not produce a valid report; check the installed tool and rules", result.code || 2);
  }
  if (show) problems(report);
  if (result.code || report.totals.errors) {
    const failure = new Failure("OpenAPI lint failed (contract left unchanged)", result.code || 1);
    failure.findings = report.problems;
    throw failure;
  }
  return report;
}

// inTemporaryDirectory scopes tool scratch files to one invocation and removes
// only that owned directory after either success or failure.
async function inTemporaryDirectory(fn) {
  const cwd = await mkdtemp(path.join(tmpdir(), "catalog-api-validation-"));
  try { return await fn(cwd); }
  finally { await rm(cwd, { recursive: true, force: true }); }
}

// lint preflights a local contract and runs installed validators without editing it.
export async function lint(filename = CONTROL, show = true) {
  await prerequisites();
  const { snapshot } = await import("./input.mjs");
  const input = await snapshot(filename);
  return inTemporaryDirectory((cwd) => lintSnapshot(input, cwd, CONFIG, show));
}

// compare lints both explicit local inputs before reporting changes or breaking
// differences; it never chooses a release baseline or retrieves a remote contract.
export async function compare(base, candidate = CONTROL, breaking = false, show = true) {
  if (!base || !path.isAbsolute(base) || /^https?:/.test(base))
    throw new Failure("set API_BASE to an absolute local OpenAPI file; no baseline is selected automatically");
  await prerequisites();
  const { snapshot, unchanged } = await import("./input.mjs");
  const inputs = [await snapshot(base), await snapshot(candidate)];
  return inTemporaryDirectory(async (cwd) => {
    for (const input of inputs) await lintSnapshot(input, cwd, CONFIG, show);
    const args = [breaking ? "breaking" : "changelog", ...inputs.map((input) => input.filename),
      "--flatten-params", "--allow-external-refs=false", "--format", "json",
      ...(breaking ? ["--fail-on", "ERR"] : ["--level", "INFO"])];
    const result = await execute(OASDIFF, args, { cwd });
    for (const input of inputs) await unchanged(input);
    let report;
    try {
      report = JSON.parse(result.stdout);
      if (!Array.isArray(report)) throw new Error("invalid report");
    } catch {
      throw new Failure("oasdiff did not produce a valid report; check comparison inputs", result.code || 2);
    }
    if (show) {
      console.log(JSON.stringify({
        tool: "oasdiff", version: "v1.33.0", baselineSHA256: inputs[0].hash,
        candidateSHA256: inputs[1].hash, changes: report,
      }));
    }
    if (result.code) {
      const failure = new Failure("API comparison failed (inputs left unchanged)", result.code);
      failure.changes = report;
      throw failure;
    }
    return report;
  });
}

// main dispatches one action, keeping acquisition separate from every read-only gate.
export async function main(args = process.argv.slice(2)) {
  if (args.length !== 1) throw new Failure("usage: runner.mjs install|lint|diff|breaking");
  switch (args[0]) {
    case "install": return install();
    case "lint": return lint();
    case "diff": return compare(process.env.API_BASE, CONTROL, false);
    case "breaking": return compare(process.env.API_BASE, CONTROL, true);
    default: throw new Failure("unknown validation command");
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Failure ? error.message : "validation failed; check local tools and input files");
    process.exitCode = error instanceof Failure ? error.code : 2;
  });
}
