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
const one = "/fixture/one.mjs";
const two = "/fixture/two.mjs";
function finish(required, events) {
  const admission = requiredUnitCompletion(required);
  for (const event of events) admission.observe(event);
  return admission.finish();
}

test("native completion counts passing cases in each required file exactly once", () => {
  const files = [one, two];
  const first = summary(one);
  const second = summary(two, { passed: 2 });
  const total = summary(undefined, { passed: 3 });
  assert.deepEqual(finish(files, [first, second, total]), {
    [one]: 1,
    [two]: 2,
  });
  for (const [name, events] of [
    ["missing required file", [first, summary(undefined)]],
    ["duplicate file", [first, first, second, total]],
    [
      "unexpected file",
      [
        first,
        second,
        summary("/fixture/extra.mjs"),
        summary(undefined, { passed: 4 }),
      ],
    ],
    ["missing terminal", [first, second]],
    [
      "partial terminal count",
      [first, second, summary(undefined, { passed: 2 })],
    ],
    ["duplicate terminal", [first, second, total, total]],
    ["late file summary", [first, total, second]],
  ]) {
    assert.throws(() => finish(files, events), /did not complete/, name);
  }
  assert.throws(
    () => finish([], [summary(undefined, { passed: 0 })]),
    /did not complete/,
  );
});

test("a failed case, a nonpassing outcome or a file with zero cases prevents completion", () => {
  for (const field of ["failed", "skipped", "todo", "cancelled"]) {
    for (const at of [0, 1]) {
      const events = [one, undefined].map((file, i) =>
        summary(file, i === at ? { [field]: 1 } : {}),
      );
      assert.throws(() => finish([one], events), /did not complete/, field);
    }
  }
  assert.throws(
    () =>
      finish(
        [one],
        [{ type: "test:fail", data: {} }, summary(one), summary(undefined)],
      ),
    /did not complete/,
  );
  assert.throws(
    () =>
      finish(
        [one],
        [summary(one, { passed: 0 }), summary(undefined, { passed: 0 })],
      ),
    /did not complete/,
  );
});

test("native completion requires valid counters even when totals agree", () => {
  for (const value of [undefined, true, 1.5, -1]) {
    assert.throws(
      () =>
        finish(
          [one],
          [
            summary(one, { passed: value }),
            summary(undefined, { passed: value }),
          ],
        ),
      /did not complete/,
    );
  }
  assert.throws(
    () =>
      finish([one], [summary(one, { skipped: undefined }), summary(undefined)]),
    /did not complete/,
  );
});

test("empty native selection refuses before launching runner workers", async () => {
  await assert.rejects(runRequiredTests([]), /No required native test files/);
});

test("source coverage requires executed lines from the selected module", () => {
  const file = "/fixture/suite.mjs";
  const source = "/fixture/package/index.js";
  const measured = { path: source, totalLineCount: 9, coveredLineCount: 3 };
  function finish(reports) {
    const admission = requiredUnitCompletion([file], source);
    admission.observe(summary(file));
    admission.observe(summary(undefined));
    for (const files of reports) {
      admission.observe({
        type: "test:coverage",
        data: { summary: { files } },
      });
    }
    return admission.finish();
  }
  assert.deepEqual(finish([[measured]]), { [file]: 1 });
  for (const reports of [
    [],
    [[]],
    [[{ ...measured, path: "/fixture/other/index.js" }]],
    [[{ ...measured, coveredLineCount: 0 }]],
    [[{ ...measured, coveredLineCount: 10 }]],
    [[measured, { ...measured, path: "/fixture/other.js" }]],
    [[measured], [measured]],
  ]) {
    assert.throws(() => finish(reports), /coverage/i);
  }
});
