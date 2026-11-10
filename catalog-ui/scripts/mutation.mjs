// Explicit, local mutation measurement. Installation remains part of ui-install.
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import "./mutation-command.mjs";
import { spawn } from "node:child_process";
import {
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const require = createRequire(import.meta.url);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const files = ["src/api/commands.ts", "src/views/code-document.ts"];
const digest = (text) => createHash("sha256").update(text).digest("hex");
export const mutantKey = (file, mutant) =>
  digest(
    JSON.stringify([
      file,
      mutant.mutatorName,
      mutant.location,
      mutant.replacement,
    ]),
  );

/** Reject incomplete execution and require an explicit disposition for survivors. */
export function validateReport(report, sources, review) {
  if (
    report?.schemaVersion !== "1.0" ||
    Object.keys(report.files ?? {})
      .sort((a, b) => Number(a > b) - Number(a < b))
      .join() !==
      [...files].sort((a, b) => Number(a > b) - Number(a < b)).join()
  )
    throw new Error("Incomplete mutation file inventory");
  const summary = { killed: 0, survived: 0 };
  const seen = new Set();
  const remaining = new Set(Object.keys(review.survivors ?? {}));
  for (const file of files) {
    const entry = report.files[file];
    if (
      entry.source !== sources[file] ||
      review.sources?.[file] !== digest(sources[file]) ||
      entry.mutants?.length !== review.counts?.[file]
    )
      throw new Error("Mutation source or inventory mismatch");
    for (const mutant of entry.mutants) {
      const key = mutantKey(file, mutant);
      if (seen.has(key)) throw new Error("Duplicate mutant");
      seen.add(key);
      if (
        mutant.status === "Killed" &&
        mutant.testsCompleted > 0 &&
        mutant.statusReason?.includes(
          'MUTATION_RESULT {"status":"caught","tests":29}',
        )
      )
        summary.killed++;
      else if (
        mutant.status === "Survived" &&
        mutant.testsCompleted > 0 &&
        typeof review.survivors?.[key] === "string" &&
        review.survivors[key].trim()
      ) {
        summary.survived++;
        remaining.delete(key);
      } else
        throw new Error(
          `Unreviewed or incomplete mutant: ${file} ${mutant.id} ${mutant.status}`,
        );
    }
  }
  if (!summary.killed || remaining.size)
    throw new Error("Missing catches or stale survivor dispositions");
  return summary;
}

async function identity() {
  const inputs = [];
  async function visit(directory) {
    for (const item of await readdir(join(root, directory), {
      withFileTypes: true,
    })) {
      const name = join(directory, item.name);
      if (item.isDirectory()) await visit(name);
      else if (item.isFile()) inputs.push(name);
      else throw new Error("Unsupported mutation input");
    }
  }
  await visit("src");
  await visit("scripts");
  inputs.push(
    "package.json",
    "package-lock.json",
    "vite.config.ts",
    "vitest.config.ts",
    "tests/mutation-dispositions.json",
    "tests/required.ts",
    "tests/unit/setup.ts",
  );
  const hash = createHash("sha256");
  // Hash exact paths in code-unit order; locale collation can equate distinct names.
  for (const name of inputs.sort((a, b) => Number(a > b) - Number(a < b)))
    hash
      .update(name)
      .update("\0")
      .update(await readFile(join(root, name)))
      .update("\0");
  return hash.digest("hex");
}

async function execute(args, directory) {
  const env = { ...process.env, CI: "1" };
  for (const key of Object.keys(env))
    if (/^(CATALOG_|UI_PROPERTY_|NODE_OPTIONS$|VITEST_|STRYKER_)/.test(key))
      delete env[key];
  // Property examples remain fixed during comparison, independent of stress settings.
  env.UI_PROPERTY_RUNS = "100";
  env.UI_PROPERTY_SEED = "42";
  const child = spawn(process.execPath, args, {
    cwd: root,
    env,
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let failed = false;
  let size = 0;
  const chunks = [];
  const stop = () => {
    failed = true;
    if (child.pid) {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch (error) {
        if (error.code !== "ESRCH") throw error;
      }
    }
  };
  const timer = setTimeout(stop, 15 * 60 * 1000);
  process.on("SIGINT", stop);
  process.on("SIGTERM", stop);
  const collect = (data) => {
    size += data.length;
    if (size > 8 * 1024 * 1024) stop();
    else chunks.push(data);
  };
  child.stdout.on("data", collect);
  child.stderr.on("data", collect);
  try {
    const code = await new Promise((done, reject) => {
      child.once("error", reject);
      child.once("close", done);
    });
    await writeFile(join(directory, "tool.log"), Buffer.concat(chunks));
    if (code !== 0 || failed)
      throw new Error(
        "Mutation tool failed, exceeded its bounds or was interrupted",
      );
  } finally {
    clearTimeout(timer);
    process.off("SIGINT", stop);
    process.off("SIGTERM", stop);
    stop();
  }
}

export async function main() {
  const output = resolve(root, "../output/ui-mutation");
  await mkdir(output, { recursive: true });
  const directory = await mkdtemp(join(output, "run-"));
  const state = { status: "incomplete", node: process.version, directory };
  try {
    const manifest = JSON.parse(
      await readFile(join(root, "package.json"), "utf8"),
    );
    for (const name of ["@stryker-mutator/core", "vitest", "fast-check"]) {
      const installed = JSON.parse(
        await readFile(
          join(root, "node_modules", name, "package.json"),
          "utf8",
        ),
      );
      if (installed.version !== manifest.devDependencies[name])
        throw new Error(`Install the locked ${name} with make ui-install`);
    }
    state.source = await identity();
    const config = {
      testRunner: "command",
      plugins: [],
      coverageAnalysis: "off",
      mutate: files,
      ignorePatterns: [
        "**",
        "!src/**",
        "!*.json",
        "!vite.config.ts",
        "!vitest.config.ts",
        "!scripts/*.mjs",
        "!tests/required.ts",
        "!tests/unit/setup.ts",
      ],
      commandRunner: {
        command: "node scripts/mutation-command.mjs",
      },
      concurrency: 2,
      timeoutMS: 10000,
      timeoutFactor: 1.5,
      dryRunTimeoutMinutes: 2,
      reporters: ["clear-text", "json"],
      jsonReporter: { fileName: join(directory, "mutation.json") },
      tempDirName: join(directory, "sandbox"),
      cleanTempDir: "always",
      thresholds: { break: 0 },
    };
    await writeFile(
      join(directory, "config.json"),
      JSON.stringify(config, null, 2),
    );
    await execute(
      [
        resolve(
          dirname(require.resolve("@stryker-mutator/core/package.json")),
          "bin/stryker.js",
        ),
        "run",
        join(directory, "config.json"),
      ],
      directory,
    );
    if ((await identity()) !== state.source)
      throw new Error("Mutation inputs changed during execution");
    const report = JSON.parse(
      await readFile(join(directory, "mutation.json"), "utf8"),
    );
    const review = JSON.parse(
      await readFile(join(root, "tests/mutation-dispositions.json"), "utf8"),
    );
    const sources = Object.fromEntries(
      await Promise.all(
        files.map(async (file) => [
          file,
          await readFile(join(root, file), "utf8"),
        ]),
      ),
    );
    state.summary = validateReport(report, sources, review);
    state.status = "passed";
  } finally {
    await rm(join(directory, "sandbox"), { recursive: true, force: true });
    await writeFile(
      join(directory, "report.json"),
      JSON.stringify(state, null, 2) + "\n",
    );
    console.log(`Mutation report: ${directory}`);
  }
}
if (
  process.argv[1] &&
  pathToFileURL(resolve(process.argv[1])).href === import.meta.url
) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
