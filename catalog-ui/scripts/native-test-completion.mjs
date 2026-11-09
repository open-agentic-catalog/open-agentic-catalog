// Admission uses the native Node event stream already consumed by owning runners.
import { resolve } from "node:path";
import { run } from "node:test";
import { spec } from "node:test/reporters";
import { pipeline } from "node:stream/promises";
import { fileURLToPath } from "node:url";

export function requiredUnitCompletion(files) {
  const required = new Set(files.map((file) => resolve(file)));
  const completed = new Map();
  let failed = false;
  let terminal;
  return {
    observe(event) {
      if (event.type === "test:fail") failed = true;
      if (event.type !== "test:summary") return;
      const { counts, success, file } = event.data;
      if (
        !counts ||
        success !== true ||
        ["passed", "failed", "cancelled", "skipped", "todo"].some(
          (name) => !Number.isSafeInteger(counts[name]) || counts[name] < 0,
        )
      ) {
        failed = true;
        return;
      }
      if (
        !success ||
        counts.failed ||
        counts.cancelled ||
        counts.skipped ||
        counts.todo
      )
        failed = true;
      // A file-only success has no per-file summary. It cannot stand in for
      // registered cases, even though Node reports it as one passing test.
      if (!file) {
        if (terminal) failed = true;
        terminal = counts;
        return;
      }
      if (terminal) failed = true;
      const path = resolve(file);
      if (!required.has(path) || completed.has(path) || counts.passed < 1)
        failed = true;
      completed.set(path, counts.passed);
    },
    finish() {
      if (
        failed ||
        !required.size ||
        !terminal ||
        terminal.passed !==
          [...completed.values()].reduce((total, count) => total + count, 0) ||
        [...required].some((file) => !completed.has(file))
      )
        throw new Error(
          "Required native tests failed, skipped or did not complete",
        );
      return Object.fromEntries(completed);
    },
  };
}

export async function runRequiredTests(files, options = {}) {
  if (!files.length) throw new Error("No required native test files selected");
  const completion = requiredUnitCompletion(files);
  const stream = run({
    files: files.map((file) => resolve(file)),
    isolation: "process",
    ...options,
  });
  stream.on("data", (event) => completion.observe(event));
  await pipeline(stream.compose(spec), process.stdout, { end: false });
  return completion.finish();
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
)
  await runRequiredTests(process.argv.slice(2));
