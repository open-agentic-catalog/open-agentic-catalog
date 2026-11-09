import assert from "node:assert/strict";
import test from "node:test";
import { publisherVerificationFailureEvidence as evidence } from "./publisher-verification-journey.mjs";

const detail = "publisher domain challenge changed or expired during lookup";
const input = {
  action: ":check",
  status: 412,
  problem: { code: "PRECONDITION_FAILED", detail },
  requestETag: '"private-etag"',
  readback: {
    status: 200,
    headers: new Headers({
      etag: '"private-etag"',
      date: "Sun, 08 Nov 2026 12:00:00 GMT",
    }),
    data: {
      challengeState: "pending",
      state: "not_verified",
      challengeIssuedAt: "2026-11-08T12:00:01.100Z",
      challengeId: "private-challenge",
    },
  },
};

test("DNS refusal evidence binds exact code, status and action without declaring a time-only cause", () => {
  assert.deepEqual(evidence(input), {
    code: "PRECONDITION_FAILED",
    refusal: "lookup_state_or_time",
    etagReadback: "matches",
    challengeState: "pending",
    proofState: "not_verified",
    responseBeforeIssue: "proven",
  });
  for (const changed of [
    { status: 200 },
    { action: ":issue" },
    { action: "unrelated" },
    { problem: { code: "CONFLICT", detail } },
    { problem: { code: "PRECONDITION_FAILED", detail: "private-sentinel" } },
  ])
    assert.equal(evidence({ ...input, ...changed }).refusal, "unclassified");
  assert.equal(
    evidence({
      ...input,
      action: ":issue",
      problem: {
        code: "PRECONDITION_FAILED",
        detail: "If-Match must contain the current publisher verification ETag",
      },
    }).refusal,
    "etag_changed",
  );
});

test("DNS readback preserves unavailable evidence and HTTP Date precision", () => {
  const sameSecond = {
    ...input.readback,
    data: {
      ...input.readback.data,
      challengeIssuedAt: "2026-11-08T12:00:00.999Z",
    },
  };
  assert.equal(
    evidence({ ...input, readback: sameSecond }).responseBeforeIssue,
    "not_proven",
  );
  assert.equal(
    evidence({ ...input, requestETag: '"different"' }).etagReadback,
    "differs",
  );
  const absent = evidence({ ...input, readback: undefined });
  assert.equal(absent.etagReadback, "unavailable");
  assert.equal(absent.responseBeforeIssue, "unavailable");
  assert.equal(absent.challengeState, "unavailable");
  assert.equal(absent.proofState, "unavailable");
  assert.equal(
    evidence({
      ...input,
      readback: { ...input.readback, status: 503 },
    }).responseBeforeIssue,
    "unavailable",
  );
  assert.equal(
    evidence({
      ...input,
      readback: { ...input.readback, status: 503 },
    }).etagReadback,
    "unavailable",
  );
  for (const challengeIssuedAt of [null, 1, {}, [], false])
    assert.equal(
      evidence({
        ...input,
        readback: {
          ...input.readback,
          data: { ...input.readback.data, challengeIssuedAt },
        },
      }).responseBeforeIssue,
      "unavailable",
    );
});

test("DNS diagnostics omit arbitrary messages, private values and malformed dates", () => {
  const value = evidence({
    ...input,
    problem: { code: "private-sentinel", detail: "private-sentinel" },
    readback: {
      status: 200,
      headers: new Headers({
        etag: "private-sentinel",
        date: "private-sentinel",
      }),
      data: {
        challengeState: "private-sentinel",
        state: "private-sentinel",
        challengeIssuedAt: "private-sentinel",
      },
    },
  });
  assert.equal(value.refusal, "unclassified");
  assert.equal(value.responseBeforeIssue, "unavailable");
  assert.doesNotMatch(JSON.stringify(value), /private/);
  assert.doesNotMatch(JSON.stringify(evidence(input)), /private|2026|12:00/);
});
