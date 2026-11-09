import assert from "node:assert/strict";
import { test } from "node:test";
import {
  requiredUnitCompletion,
  runRequiredTests,
} from "./native-test-completion.mjs";

const summary = (file, extra = {}) => ({
  type: "test:summary",
  data: {
    file,
    success: true,
    counts: {
      passed: 1,
      failed: 0,
      cancelled: 0,
      skipped: 0,
      todo: 0,
      ...extra,
    },
  },
});
test("native completion needs every required file's actual registered-case summary", () => {
  const required = ["/fixture/one.mjs", "/fixture/two.mjs"];
  const good = requiredUnitCompletion(required);
  for (const file of required) good.observe(summary(file));
  good.observe(summary(undefined, { passed: 2 }));
  assert.deepEqual(good.finish(), {
    "/fixture/one.mjs": 1,
    "/fixture/two.mjs": 1,
  });
  for (const bad of [
    [],
    [summary(required[0])],
    [summary(undefined)],
    [summary(required[0]), summary(required[0])],
  ]) {
    const admission = requiredUnitCompletion(required);
    for (const event of bad) admission.observe(event);
    assert.throws(() => admission.finish(), /did not complete/);
  }
  assert.throws(() => requiredUnitCompletion([]).finish(), /did not complete/);
});
test("native failed, skipped, todo, cancelled and file-only outcomes cannot stand in for passing cases", () => {
  for (const field of ["failed", "skipped", "todo", "cancelled"])
    for (const file of ["/fixture/one.mjs", undefined]) {
      const admission = requiredUnitCompletion(["/fixture/one.mjs"]);
      admission.observe(summary("/fixture/one.mjs"));
      admission.observe(summary(file, { [field]: 1 }));
      assert.throws(() => admission.finish(), /did not complete/);
    }
  const failed = requiredUnitCompletion(["/fixture/one.mjs"]);
  failed.observe({ type: "test:fail", data: {} });
  failed.observe(summary("/fixture/one.mjs"));
  assert.throws(() => failed.finish(), /did not complete/);
  const empty = requiredUnitCompletion(["/fixture/one.mjs"]);
  empty.observe(summary("/fixture/one.mjs", { passed: 0 }));
  assert.throws(() => empty.finish(), /did not complete/);
});

test("missing or contradictory terminal summaries and malformed counters refuse native completion", () => {
  for (const events of [
    [summary("/fixture/one.mjs")],
    [summary("/fixture/one.mjs"), summary(undefined, { passed: 2 })],
    [summary("/fixture/one.mjs"), summary(undefined), summary(undefined)],
    [summary(undefined), summary("/fixture/one.mjs")],
    [summary("/fixture/one.mjs", { passed: undefined }), summary(undefined)],
    [summary("/fixture/one.mjs", { skipped: undefined }), summary(undefined)],
    [summary("/fixture/one.mjs", { passed: true }), summary(undefined)],
    [summary("/fixture/one.mjs", { passed: 1.5 }), summary(undefined)],
  ]) {
    const admission = requiredUnitCompletion(["/fixture/one.mjs"]);
    for (const event of events) admission.observe(event);
    assert.throws(() => admission.finish(), /did not complete/);
  }
});

test("empty native selection refuses before launching runner workers", async () => {
  await assert.rejects(runRequiredTests([]), /No required native test files/);
});
