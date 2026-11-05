// Read-only, opt-in advisory checks for the UI lockfile; no package installation.
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const uiRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
export const trivyVersion = JSON.parse(
  await readFile(new URL("../../test/packaging/trivy.json", import.meta.url)),
).version;
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const object = (value) =>
  value !== null && typeof value === "object" && !Array.isArray(value);

/** Reject scans that silently omit the selected lockfile or development tools. */
export function validateTrivy(report, lock) {
  const results = report?.Results;
  if (
    report?.SchemaVersion !== 2 ||
    !Array.isArray(results) ||
    results.length !== 1
  )
    throw new Error("Invalid Trivy report");
  const result = results[0];
  if (
    result.Type !== "npm" ||
    result.Target !== "package-lock.json" ||
    !Array.isArray(result.Packages) ||
    !result.Packages.length ||
    (result.Vulnerabilities != null && !Array.isArray(result.Vulnerabilities))
  )
    throw new Error("Missing UI npm inventory");
  for (const finding of result.Vulnerabilities ?? [])
    if (
      !finding?.VulnerabilityID ||
      !finding.PkgName ||
      !["UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL"].includes(
        finding.Severity,
      )
    )
      throw new Error("Invalid Trivy finding");
  for (const name of ["react", "vite", "@playwright/test"]) {
    const version = lock.packages?.[`node_modules/${name}`]?.version;
    if (
      !version ||
      !result.Packages.some(
        (pkg) => pkg.Name === name && pkg.Version === version,
      )
    )
      throw new Error("Missing locked runtime/build/test package");
  }
}

/** npm uses the same failure code for advisories and registry errors. */
export function validateAudit(report, status) {
  const counts = report?.metadata?.vulnerabilities;
  const dependencies = report?.metadata?.dependencies;
  if (
    report?.auditReportVersion !== 2 ||
    !object(report.vulnerabilities) ||
    !object(counts) ||
    !object(dependencies) ||
    report.error ||
    !Number.isInteger(dependencies.dev) ||
    dependencies.dev <= 0 ||
    !Number.isInteger(dependencies.total) ||
    dependencies.total <= 0
  )
    throw new Error("Invalid npm audit report");
  for (const name of ["info", "low", "moderate", "high", "critical", "total"])
    if (!Number.isInteger(counts[name]) || counts[name] < 0)
      throw new Error("Invalid npm audit counts");
  const expected = counts.high + counts.critical > 0 ? 1 : 0;
  if (status !== expected) throw new Error("npm audit command failed");
  return expected;
}

/** Run a scanner with bounded execution and retain private native reports. */
export async function checkDependencies({
  mode,
  root = uiRoot,
  output = resolve(root, "../output/ui-dependencies"),
  env = process.env,
  run = spawnSync,
} = {}) {
  if (!["trivy", "audit"].includes(mode))
    throw new Error("Select trivy or audit");
  const inputs = await Promise.all(
    ["package.json", "package-lock.json"].map((name) =>
      readFile(join(root, name)),
    ),
  );
  const lock = JSON.parse(inputs[1]);
  await mkdir(output, { recursive: true });
  const directory = await mkdtemp(join(output, `${mode}-`));
  const metadata = {
    scope: "ui-lockfile-including-dev-optional-peer",
    lockSha256: hash(inputs[1]),
    mode,
    startedAt: new Date().toISOString(),
    outcome: "error",
    commands: [],
  };
  const save = (name, value) =>
    writeFile(join(directory, name), value, { mode: 0o600 });
  let status;
  // Do not echo scanner stderr: it can contain credential-bearing registry URLs.
  // Explicit Trivy config/ignore files and removal of TRIVY_* prevent ambient
  // severity, suppression, skip, remote-server or offline settings narrowing scope.
  const scannerEnv = Object.fromEntries(
    Object.entries(env).filter(
      ([name]) => !name.toUpperCase().startsWith("TRIVY_"),
    ),
  );
  if (env.TRIVY_CACHE_DIR) scannerEnv.TRIVY_CACHE_DIR = env.TRIVY_CACHE_DIR;
  const invoke = (executable, args, cwd, environment) => {
    metadata.phase = args[0];
    const result = run(executable, args, {
      cwd,
      env: environment,
      encoding: "utf8",
      timeout: 180000,
      maxBuffer: 32 << 20,
      shell: false,
    });
    metadata.commands.push({
      operation: args[0],
      status: result.status ?? null,
    });
    if (result.error || result.signal || !Number.isInteger(result.status))
      throw new Error(
        "Scanner unavailable, timed out or exceeded output limit",
      );
    return result;
  };
  try {
    if (mode === "trivy") {
      const executable = env.UI_DEPS_TRIVY || "trivy";
      await save("config.yaml", "{}\n");
      await save("ignore", "");
      const config = ["--config", join(directory, "config.yaml")];
      const ignore = ["--ignorefile", join(directory, "ignore")];
      const preflight = invoke(
        executable,
        ["--version", ...config, "--format", "json"],
        directory,
        scannerEnv,
      );
      if (
        preflight.status !== 0 ||
        JSON.parse(preflight.stdout)?.Version !== trivyVersion
      )
        throw new Error("Install the qualified Trivy version");
      const result = invoke(
        executable,
        [
          "fs",
          ...config,
          ...ignore,
          "--scanners",
          "vuln",
          "--include-dev-deps",
          "--list-all-pkgs",
          "--severity",
          "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL",
          "--exit-code",
          "0",
          "--format",
          "json",
          "--output",
          join(directory, "trivy.json"),
          join(root, "package-lock.json"),
        ],
        directory,
        scannerEnv,
      );
      if (result.status !== 0) throw new Error("Trivy scan failed");
      metadata.phase = "inventory-validation";
      validateTrivy(
        JSON.parse(await readFile(join(directory, "trivy.json"), "utf8")),
        lock,
      );
      const version = invoke(
        executable,
        ["--version", ...config, "--format", "json"],
        directory,
        scannerEnv,
      );
      if (version.status !== 0) throw new Error("Trivy version query failed");
      const info = JSON.parse(version.stdout);
      if (
        info.Version !== trivyVersion ||
        !Number.isFinite(Date.parse(info.VulnerabilityDB?.UpdatedAt))
      )
        throw new Error(
          "Missing Trivy version or vulnerability database timestamp",
        );
      metadata.toolVersion = info.Version;
      metadata.vulnerabilityDatabase = info.VulnerabilityDB;
      // Reserve 10 for findings so Trivy's ordinary error code 1 cannot be
      // mistaken for a successfully evaluated advisory report.
      const gate = invoke(
        executable,
        [
          "convert",
          ...config,
          ...ignore,
          "--format",
          "table",
          "--scanners",
          "vuln",
          "--severity",
          "HIGH,CRITICAL",
          "--exit-code",
          "10",
          join(directory, "trivy.json"),
        ],
        directory,
        scannerEnv,
      );
      await save("summary.txt", gate.stdout);
      if (![0, 10].includes(gate.status))
        throw new Error("Trivy report conversion failed");
      status = gate.status === 10 ? 1 : 0;
    } else {
      const version = invoke("npm", ["--version"], root, env);
      if (version.status !== 0 || !/^\d+\.\d+\.\d+\s*$/.test(version.stdout))
        throw new Error("npm version query failed");
      metadata.toolVersion = version.stdout.trim();
      const result = invoke(
        "npm",
        [
          "audit",
          "--package-lock-only",
          "--include=dev",
          "--include=optional",
          "--include=peer",
          "--ignore-scripts",
          "--workspaces=false",
          "--audit-level=high",
          "--json",
        ],
        root,
        env,
      );
      // Failed registry replies can disclose private endpoints. Only retain a
      // validated advisory report; record the failure category/status otherwise.
      const report = JSON.parse(result.stdout);
      metadata.phase = "audit-validation";
      status = validateAudit(report, result.status);
      await save("npm-audit.json", result.stdout);
    }
    metadata.outcome = status === 0 ? "passed" : "findings";
  } catch {
    status = 2;
    metadata.outcome = "error";
  } finally {
    const after = await Promise.all(
      ["package.json", "package-lock.json"].map((name) =>
        readFile(join(root, name)).catch(() => null),
      ),
    );
    if (after.some((bytes, i) => !bytes?.equals(inputs[i]))) {
      status = 2;
      metadata.outcome = "inputs-changed";
    }
    await save("run.json", JSON.stringify(metadata, null, 2) + "\n");
  }
  return { status, directory, outcome: metadata.outcome };
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  try {
    if (process.argv.length !== 3) throw new Error("Select trivy or audit");
    const result = await checkDependencies({ mode: process.argv[2] });
    console.log(
      `UI dependency check: ${result.outcome}. Reports: ${result.directory}`,
    );
    process.exitCode = result.status;
  } catch {
    console.error(
      "UI dependency check could not start. Usage: dependencies.mjs trivy|audit; requires UI manifests and installed tools.",
    );
    process.exitCode = 2;
  }
}
