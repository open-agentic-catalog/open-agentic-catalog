import assert from "node:assert/strict";
import test from "node:test";
import { personalCommandOutcome } from "./personal-evidence.mjs";

const timeRefusal = "saved item transition time is before its retained update";
test("saved-search update diagnostics retain exact success and unclassified-refusal outcomes", () => {
  for (const [status, problem] of [
    [
      200,
      {
        id: "private-id",
        label: "private-label",
        search: { text: "private-query" },
      },
    ],
    [409, { code: "CONFLICT", detail: "private-unclassified-refusal" }],
  ]) {
    const value = personalCommandOutcome({
      status,
      method: "PATCH",
      problem,
      requestId: "owned-request-123",
    });
    assert.deepEqual(value, {
      operation: "personal.update",
      status,
      code: status === 409 ? "CONFLICT" : undefined,
      refusal: "unclassified",
      requestId: "owned-request-123",
    });
    assert.doesNotMatch(JSON.stringify(value), /private/);
  }
});

test("private command diagnostics classify only the exact transition refusal", () => {
  assert.deepEqual(
    personalCommandOutcome({
      status: 409,
      method: "DELETE",
      requestId: "owned-123",
      problem: { code: "CONFLICT", detail: timeRefusal },
    }),
    {
      operation: "personal.remove",
      status: 409,
      code: "CONFLICT",
      refusal: "transition_time_regression",
      requestId: "owned-123",
    },
  );
  for (const input of [
    { status: 412, code: "CONFLICT", detail: timeRefusal },
    { status: 409, code: "PRECONDITION_FAILED", detail: timeRefusal },
    { status: 409, code: "CONFLICT", detail: timeRefusal + " private-suffix" },
  ])
    assert.equal(
      personalCommandOutcome({
        status: input.status,
        method: "DELETE",
        problem: { code: input.code, detail: input.detail },
      }).refusal,
      "unclassified",
    );
});

test("private command diagnostics omit arbitrary payloads and unadmitted values", () => {
  const value = personalCommandOutcome({
    status: 409,
    method: "PATCH",
    requestId: "private\nrequest",
    problem: {
      code: "private-code",
      detail: "private-query",
      label: "private-label",
      token: "private-token",
    },
  });
  assert.deepEqual(value, {
    operation: "personal.update",
    status: 409,
    code: undefined,
    refusal: "unclassified",
    requestId: undefined,
  });
  assert.doesNotMatch(JSON.stringify(value), /private/);
});

test("private command diagnostics tolerate absent and malformed problems", () => {
  for (const problem of [undefined, null, [], "private-query", { code: {} }]) {
    const value = personalCommandOutcome({
      status: 503,
      method: "POST",
      problem,
    });
    assert.equal(value.operation, "personal.save");
    assert.equal(value.refusal, "unclassified");
    assert.equal(value.code, undefined);
  }
});
