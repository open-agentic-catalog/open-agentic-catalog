// Stryker's command runner treats every nonzero exit as a catch. Retain a
// separate marker so its caller can reject broken tooling or incomplete tests.
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
export const testCounts = {
  "src/api/commands.test.ts": 5,
  "src/views/code-document.test.tsx": 17,
  "src/views/code-document.property.test.ts": 1,
  "src/views/code-block.test.tsx": 6,
};
export function classifyVitest(report, code) {
  if (
    ![0, 1].includes(code) ||
    !report ||
    report.numTotalTests !== 29 ||
    report.numPendingTests !== 0 ||
    report.numTodoTests !== 0 ||
    report.testResults?.length !== 4
  )
    throw new Error("Incomplete Vitest run");
  let failures = 0;
  const seen = new Set();
  for (const file of report.testResults) {
    const name = Object.keys(testCounts).find((path) =>
      file.name.endsWith("/" + path),
    );
    if (
      !name ||
      seen.has(name) ||
      file.assertionResults?.length !== testCounts[name]
    )
      throw new Error("Missing owning tests");
    seen.add(name);
    for (const test of file.assertionResults) {
      if (!["passed", "failed"].includes(test.status))
        throw new Error("Skipped owning test");
      failures += Number(test.status === "failed");
    }
  }
  if (
    report.numFailedTests !== failures ||
    report.numPassedTests !== 29 - failures ||
    code !== Number(failures > 0) ||
    report.success !== (failures === 0)
  )
    throw new Error("Contradictory Vitest outcome");
  return { status: failures ? "caught" : "passed", tests: 29 };
}
if (
  process.argv[1] &&
  pathToFileURL(resolve(process.argv[1])).href === import.meta.url
) {
  let outcome = { status: "error", tests: 0 };
  const directory = mkdtempSync(join(tmpdir(), "catalog-ui-mutation-"));
  const output = join(directory, "result.json");
  try {
    const result = spawnSync(
      process.execPath,
      [
        "node_modules/vitest/vitest.mjs",
        "run",
        ...Object.keys(testCounts),
        "--reporter=json",
        "--outputFile=" + output,
      ],
      { encoding: "utf8", timeout: 45000, maxBuffer: 8 * 1024 * 1024 },
    );
    if (result.error || result.signal)
      throw new Error("Vitest execution failed");
    if (statSync(output).size > 8 * 1024 * 1024)
      throw new Error("Vitest report exceeds limit");
    outcome = classifyVitest(
      JSON.parse(readFileSync(output, "utf8")),
      result.status,
    );
  } catch {
    /* Invalid runs are errors, never assertion catches. */
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
  console.log("MUTATION_RESULT " + JSON.stringify(outcome));
  process.exitCode = { passed: 0, caught: 1, error: 2 }[outcome.status];
}
