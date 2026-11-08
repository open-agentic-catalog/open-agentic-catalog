import assert from "node:assert/strict";
import test from "node:test";
import { waitForPackagedReadiness } from "./packaged-readiness.mjs";

function clock() {
  let at = 0;
  return {
    now: () => at,
    advance: (duration) => {
      at += duration;
    },
    sleep: async (duration) => {
      at += duration;
    },
  };
}
const failure = (promise) =>
  promise.then(
    () => assert.fail("Readiness unexpectedly succeeded"),
    (error) => {
      assert.equal(
        error.message,
        "Owned packaged listener did not become ready",
      );
      return error.diagnostic;
    },
  );

test("readiness admits only an in-budget 200 after actual probes", async () => {
  const time = clock();
  const timeouts = [];
  await waitForPackagedReadiness({
    ...time,
    probe: async (timeout) => {
      timeouts.push(timeout);
      return timeouts.length === 3 ? 200 : 503;
    },
  });
  assert.deepEqual(timeouts, [1000, 1000, 1000]);
  assert.equal(time.now(), 400);
});

test("the default 120s clock is monotonic despite backward wall-clock samples", async (t) => {
  const time = clock();
  let wall = 1000000;
  let calls = 0;
  t.mock.method(performance, "now", time.now);
  t.mock.method(Date, "now", () => wall);
  const result = await failure(
    waitForPackagedReadiness({
      sleep: async (duration) => {
        assert.ok(calls <= 600, "readiness exceeded its fixed poll budget");
        wall -= 2000;
        time.advance(duration);
      },
      probe: async () => {
        calls++;
        return 503;
      },
    }),
  );
  assert.equal(wall, -200000);
  assert.equal(calls, 600);
  assert.deepEqual(result, {
    interrupted: false,
    elapsedMs: 120000,
    lastStatus: 503,
    lastError: undefined,
  });
});

test("probe time is charged to the budget and its timeout is bounded by remaining time", async () => {
  const time = clock();
  const timeouts = [];
  const result = await failure(
    waitForPackagedReadiness({
      ...time,
      probe: async (timeout) => {
        timeouts.push(timeout);
        time.advance(timeouts.length === 1 ? 119700 : 100);
        return 503;
      },
    }),
  );
  assert.deepEqual(timeouts, [1000, 100]);
  assert.equal(result.elapsedMs, 120000);
});

test("late success and interruption before or during a probe never admit readiness", async () => {
  for (const mode of ["late", "before", "during"]) {
    const time = clock();
    let interrupted = mode === "before";
    let probes = 0;
    const result = await failure(
      waitForPackagedReadiness({
        ...time,
        interrupted: () => interrupted,
        probe: async () => {
          probes++;
          if (mode === "late") time.advance(120000);
          else interrupted = true;
          return 200;
        },
      }),
    );
    assert.equal(probes, mode === "before" ? 0 : 1);
    assert.equal(result.interrupted, mode !== "late");
    assert.equal(result.elapsedMs, mode === "late" ? 120000 : 0);
  }
});

test("last transport errors retain only reviewed classes and no URL/body/message", async () => {
  for (const [error, category] of [
    [
      Object.assign(new Error("private-url private-body"), {
        code: "ECONNREFUSED",
      }),
      "connection_refused",
    ],
    [
      Object.assign(new Error("private"), { cause: { code: "ECONNRESET" } }),
      "connection_reset",
    ],
    [
      Object.assign(new Error("private"), { code: "ENOTFOUND" }),
      "name_resolution",
    ],
    [Object.assign(new Error("private"), { code: "CERT_HAS_EXPIRED" }), "tls"],
    [Object.assign(new Error("private"), { name: "TimeoutError" }), "timeout"],
    [
      Object.assign(new Error("private"), {
        name: "AbortError",
        cause: { name: "TimeoutError" },
      }),
      "timeout",
    ],
    [Object.assign(new Error("private"), { name: "AbortError" }), "aborted"],
    [
      Object.assign(new Error("private"), { code: "private-code" }),
      "unclassified",
    ],
  ]) {
    const time = clock();
    const result = await failure(
      waitForPackagedReadiness({
        ...time,
        probe: async () => {
          time.advance(120000);
          throw error;
        },
      }),
    );
    assert.deepEqual(result, {
      interrupted: false,
      elapsedMs: 120000,
      lastStatus: undefined,
      lastError: category,
    });
    assert.doesNotMatch(JSON.stringify(result), /private/);
  }
});
