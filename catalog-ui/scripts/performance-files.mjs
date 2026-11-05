// Owned build/report I/O; no browser payloads or authentication state are retained.
import { readFile, readdir, writeFile, lstat } from "node:fs/promises";
import { join, relative } from "node:path";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { cpus, platform, release, arch } from "node:os";
import { gzipSync } from "node:zlib";
import {
  assetInventory,
  checkAssets,
  summarizeSamples,
} from "./performance.mjs";

export async function sourceIdentity(root) {
  const files = execFileSync(
    "git",
    ["ls-files", "-co", "--exclude-standard", "-z"],
    { cwd: root, timeout: 10000, maxBuffer: 8 << 20 },
  )
    .toString()
    .split("\0")
    .filter(Boolean);
  const hash = createHash("sha256");
  for (const name of [...new Set(files)].sort()) {
    const file = join(root, name);
    // Missing tracked paths describe deletions without reintroducing old content.
    const stat = await lstat(file).catch((e) => {
      if (e.code !== "ENOENT") throw e;
    });
    hash.update(name + "\0");
    hash.update(stat ? "file\0" : "missing\0");
    if (stat?.isFile()) hash.update(await readFile(file));
    else if (stat) throw new Error("Unsupported source input");
    hash.update("\0");
  }
  return {
    commit: execFileSync("git", ["rev-parse", "HEAD"], {
      cwd: root,
      timeout: 10000,
    })
      .toString()
      .trim(),
    sha256: hash.digest("hex"),
  };
}

export async function buildInventory(directory) {
  const files = [];
  const hash = createHash("sha256");
  async function walk(current) {
    for (const entry of (await readdir(current, { withFileTypes: true })).sort(
      (a, b) => a.name.localeCompare(b.name),
    )) {
      if (entry.name === ".vite") continue;
      const file = join(current, entry.name);
      if (entry.isDirectory()) await walk(file);
      else if (entry.isFile()) {
        const data = await readFile(file);
        if (entry.name.endsWith(".js") && data.includes("__coverage__"))
          throw new Error("Instrumented build is not a performance input");
        const name = relative(directory, file);
        hash
          .update(name + "\0")
          .update(data)
          .update("\0");
        files.push({
          path: name,
          bytes: data.length,
          gzipBytes: gzipSync(data, { level: 9 }).length,
        });
      } else throw new Error("Unsupported build input");
    }
  }
  await walk(directory);
  return {
    sha256: hash.digest("hex"),
    ...assetInventory(
      JSON.parse(
        await readFile(join(directory, ".vite/manifest.json"), "utf8"),
      ),
      files,
    ),
  };
}

export async function performanceInputs(root, policy) {
  const lock = await readFile(join(root, "catalog-ui/package-lock.json"));
  return {
    source: await sourceIdentity(root),
    policy,
    lockSHA256: createHash("sha256").update(lock).digest("hex"),
    node: process.version,
    go: execFileSync("go", ["version"], { timeout: 10000 }).toString().trim(),
    playwright: JSON.parse(
      await readFile(
        join(root, "catalog-ui/node_modules/@playwright/test/package.json"),
        "utf8",
      ),
    ).version,
    os: {
      platform: platform(),
      release: release(),
      arch: arch(),
      cpu: cpus()[0]?.model ?? "unknown",
      logicalCPUs: cpus().length,
    },
  };
}

export async function finishPerformance(output, inputs, build, budgets) {
  const execution = JSON.parse(
    await readFile(join(output, "execution.json"), "utf8"),
  );
  if (
    execution.status !== "passed" ||
    execution.passed !== 1 ||
    execution.failed !== 0 ||
    execution.skipped !== 0
  )
    throw new Error("Incomplete performance execution");
  const measurements = JSON.parse(
    await readFile(join(output, "samples.json"), "utf8"),
  );
  if (typeof measurements.browser !== "string" || !measurements.browser)
    throw new Error("Missing browser identity");
  checkAssets(build, budgets);
  const summary = summarizeSamples(measurements.samples, budgets);
  const report = {
    version: 1,
    status: "passed",
    cleanup: "passed",
    profile: "chromium-desktop-loopback-memory",
    fixture: { releases: 25, page: 24, retainedPaddingBytes: 1048576 },
    cache: "fresh-context-cold; same-context-warm; no routing",
    timing:
      "report-only; destination performance.now for navigation; same-document delta for actions; includes driver/assertion overhead; iteration 0 warm-up excluded from summary",
    inputs,
    build,
    budgets,
    browser: measurements.browser,
    samples: measurements.samples,
    summary,
  };
  await writeFile(
    join(output, "report.json"),
    JSON.stringify(report, null, 2) + "\n",
  );
  console.log(
    "UI performance: asset/work budgets passed; milliseconds are report-only",
  );
  for (const [label, value] of Object.entries(summary))
    console.log(
      `${label}: median ${value.medianMs.toFixed(1)} ms, range ${value.minMs.toFixed(1)}-${value.maxMs.toFixed(1)} ms`,
    );
}
