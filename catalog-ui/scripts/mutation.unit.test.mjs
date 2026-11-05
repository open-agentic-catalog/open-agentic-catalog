import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { classifyVitest, testCounts } from "./mutation-command.mjs";
import { mutantKey, validateReport } from "./mutation.mjs";
function native() {
  return {
    numTotalTests: 29,
    numPendingTests: 0,
    numTodoTests: 0,
    numPassedTests: 29,
    numFailedTests: 0,
    success: true,
    testResults: Object.entries(testCounts).map(([name, count]) => ({
      name: "/owned/" + name,
      assertionResults: Array.from({ length: count }, () => ({
        status: "passed",
      })),
    })),
  };
}
test("native results distinguish actual assertions from missing, skipped or broken execution", () => {
  const clean = native();
  assert.deepEqual(classifyVitest(clean, 0), { status: "passed", tests: 29 });
  const failed = native();
  failed.numFailedTests = 1;
  failed.numPassedTests = 28;
  failed.success = false;
  failed.testResults[0].assertionResults[0].status = "failed";
  assert.deepEqual(classifyVitest(failed, 1), { status: "caught", tests: 29 });
  for (const alter of [
    (v) => {
      v.testResults.pop();
    },
    (v) => {
      v.testResults[0].assertionResults = [];
    },
    (v) => {
      v.testResults[0].assertionResults[0].status = "pending";
    },
    (v) => {
      v.numFailedTests = 1;
    },
    (v) => {
      v.testResults[0] = v.testResults[1];
    },
    (v) => {
      v.numTotalTests = 0;
    },
    (v) => {
      v.numTotalTests = 25; // A predecessor count must not qualify the expanded owning selection.
    },
  ]) {
    const value = native();
    alter(value);
    assert.throws(() => classifyVitest(value, 0));
  }
  for (const code of [null, 1, 2, 137])
    assert.throws(() => classifyVitest(clean, code));
});
function fixture() {
  const sources = {
    "src/api/commands.ts": "commands",
    "src/views/code-document.ts": "document",
  };
  const report = { schemaVersion: "1.0", files: {} };
  const review = { sources: {}, counts: {}, survivors: {} };
  for (const [file, source] of Object.entries(sources)) {
    const mutant = {
      id: file,
      mutatorName: "BooleanLiteral",
      location: { start: { line: 1, column: 1 }, end: { line: 1, column: 5 } },
      replacement: "false",
      status: "Killed",
      testsCompleted: 1,
      statusReason: 'MUTATION_RESULT {"status":"caught","tests":29}',
    };
    if (source === "document") {
      mutant.status = "Survived";
      review.survivors[mutantKey(file, mutant)] =
        "Equivalent token spelling under validated JSON.";
    }
    report.files[file] = { source, mutants: [mutant] };
    review.sources[file] = createHash("sha256").update(source).digest("hex");
    review.counts[file] = 1;
  }
  return { sources, report, review };
}
test("mutation reconciliation rejects empty execution, new survivors and stale evidence", () => {
  const good = fixture();
  assert.deepEqual(validateReport(good.report, good.sources, good.review), {
    killed: 1,
    survived: 1,
  });
  good.report.files = Object.fromEntries(
    Object.entries(good.report.files).reverse(),
  );
  assert.deepEqual(validateReport(good.report, good.sources, good.review), {
    killed: 1,
    survived: 1,
  });
  for (const alter of [
    (v) => {
      v.report.files["src/extra.ts"] = v.report.files["src/api/commands.ts"];
    },
    (v) => {
      delete v.report.files["src/api/commands.ts"];
    },
    (v) => {
      v.sources["src/api/commands.ts"] = "changed";
    },
    (v) => {
      v.report.files["src/api/commands.ts"].mutants = [];
    },
    (v) => {
      v.report.files["src/api/commands.ts"].mutants[0].statusReason =
        "compiler failed";
    },
    (v) => {
      v.report.files["src/views/code-document.ts"].mutants[0].testsCompleted =
        0;
    },
    (v) => {
      v.review.survivors = {};
    },
    (v) => {
      v.review.survivors.extra = "stale";
    },
  ]) {
    const value = fixture();
    alter(value);
    assert.throws(() =>
      validateReport(value.report, value.sources, value.review),
    );
  }
  for (const status of [
    "Timeout",
    "NoCoverage",
    "CompileError",
    "RuntimeError",
    "Pending",
    "Survived",
  ]) {
    const value = fixture();
    value.report.files["src/api/commands.ts"].mutants[0].status = status;
    assert.throws(() =>
      validateReport(value.report, value.sources, value.review),
    );
  }
});
