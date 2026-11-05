import test from "node:test";
import assert from "node:assert/strict";
import {
  authorizationCommandOutcome,
  fixtureSetupError,
  roleRefusalReadback,
} from "./authorization-evidence.mjs";

const id = "00000000-0000-7000-8000-000000000001";

const roleRead = {
  status: 200,
  document: {
    state: "active",
    revision: 1,
    updatedAt: "2026-09-01T12:00:00.900Z",
    name: "private-role-name",
    permissions: ["private-permission"],
  },
  etag: '"rev-1"',
  date: "Tue, 01 Sep 2026 12:00:00 GMT",
};
test("role readback exposes only fixed state and revision/clock comparisons", () => {
  const evidence = roleRefusalReadback(roleRead);
  assert.deepEqual(evidence, {
    readSucceeded: true,
    roleState: "active",
    revisionMatchesExpected: true,
    responseClockBeforeUpdate: false,
  });
  assert.doesNotMatch(JSON.stringify(evidence), /private|2026|rev-1/);
});

test("role clock evidence respects whole-second HTTP Date precision", () => {
  for (const [updatedAt, earlier] of [
    ["2026-09-01T12:00:00.999Z", false],
    ["2026-09-01T12:00:01.000Z", true],
    ["2026-09-01T12:00:02.123Z", true],
  ])
    assert.equal(
      roleRefusalReadback({
        ...roleRead,
        document: { ...roleRead.document, updatedAt },
      }).responseClockBeforeUpdate,
      earlier,
    );
});

test("role readback distinguishes archived and concurrently changed records", () => {
  assert.deepEqual(
    roleRefusalReadback({
      ...roleRead,
      document: { ...roleRead.document, state: "archived", revision: 2 },
    }),
    {
      readSucceeded: true,
      roleState: "archived",
      revisionMatchesExpected: false,
      responseClockBeforeUpdate: false,
    },
  );
  assert.deepEqual(roleRefusalReadback({ ...roleRead, status: 403 }), {
    readSucceeded: false,
    roleState: "unknown",
    revisionMatchesExpected: undefined,
    responseClockBeforeUpdate: undefined,
  });
});

test("missing and malformed role evidence remains unknown without private output", () => {
  for (const input of [
    { document: null },
    {
      document: {
        state: "private-state",
        revision: -1,
        updatedAt: "private-time",
      },
    },
    {
      document: {
        state: "private-state",
        revision: Number.MAX_SAFE_INTEGER + 1,
      },
    },
    { document: [], etag: "private-etag", date: "private-date" },
  ]) {
    const evidence = roleRefusalReadback({ ...roleRead, ...input });
    assert.equal(evidence.roleState, "unknown");
    assert.equal(evidence.revisionMatchesExpected, undefined);
    assert.equal(evidence.responseClockBeforeUpdate, undefined);
    assert.doesNotMatch(JSON.stringify(evidence), /private/);
  }
  for (const etag of [
    null,
    '"rev-0"',
    '"rev-01"',
    '"rev-' + "1".repeat(100) + '"',
  ])
    assert.equal(
      roleRefusalReadback({ ...roleRead, etag }).revisionMatchesExpected,
      undefined,
    );
});

test("setup errors preserve the publication-negative status/code contract without private detail", () => {
  const error = fixtureSetupError({
    method: "POST",
    path: `/api/v1/catalog-listings/${id}:publish`,
    status: 422,
    problem: {
      code: "PUBLICATION_NOT_READY",
      detail: "private-publication-evidence",
      token: "private-token",
    },
  });
  assert.match(error.message, /422, PUBLICATION_NOT_READY/);
  assert.doesNotMatch(error.message, /private-|catalog-listings|00000000/);
  assert.match(error.message, /"refusal":"unclassified"/);
});
test("owned authorization refusals retain exact method, target family and fixed category", () => {
  for (const [method, path, operation, detail, refusal] of [
    [
      "PATCH",
      `/api/v1/access/roles/${id}`,
      "role.update",
      "role cannot be changed in this state or time",
      "role_state_or_time",
    ],
    [
      "POST",
      `/api/v1/access/groups/${id}:reactivate`,
      "group.transition",
      "group cannot be changed in this state or time",
      "group_state_or_time",
    ],
    [
      "POST",
      `/api/v1/access/assignments/${id}:revoke`,
      "assignment.revoke",
      "assignment cannot be revoked",
      "assignment_state_or_time",
    ],
    [
      "POST",
      `/api/v1/access/groups/${id}/members/${id}:revoke`,
      "group_member.revoke",
      "group member cannot be revoked",
      "group_member_state_or_time",
    ],
  ]) {
    assert.deepEqual(
      authorizationCommandOutcome({
        method,
        path,
        status: 409,
        problem: { code: "CONFLICT", detail },
      }),
      { operation, method, status: 409, code: "CONFLICT", refusal },
    );
  }
});

test("state-or-time categories require every binding and do not assert a time-only cause", () => {
  const input = {
    method: "PATCH",
    path: `/api/v1/access/roles/${id}`,
    status: 409,
    problem: {
      code: "CONFLICT",
      detail: "role cannot be changed in this state or time",
    },
  };
  for (const change of [
    { method: "POST" },
    { path: `/api/v1/access/groups/${id}` },
    { path: input.path + "?private-query" },
    { status: 412 },
    { problem: { code: "PRECONDITION_FAILED", detail: input.problem.detail } },
    {
      problem: {
        code: "CONFLICT",
        detail: input.problem.detail + " private-suffix",
      },
    },
  ])
    assert.equal(
      authorizationCommandOutcome({ ...input, ...change }).refusal,
      "unclassified",
    );
});

test("group-member refusal evidence requires the exact relation and reviewed conflict", () => {
  const memberId = "00000000-0000-7000-8000-000000000002";
  const input = {
    method: "POST",
    path: `/api/v1/access/groups/${id}/members/${memberId}:revoke`,
    status: 409,
    problem: { code: "CONFLICT", detail: "group member cannot be revoked" },
  };
  assert.equal(
    authorizationCommandOutcome(input).refusal,
    "group_member_state_or_time",
  );
  for (const change of [
    { method: "PATCH" },
    { path: `/api/v1/access/groups/private-group/members/${memberId}:revoke` },
    { path: `/api/v1/access/groups/${id}/members/private-member:revoke` },
    { path: input.path + "?private-query" },
    { path: input.path + "/" },
    { path: `/api/v1/access/groups/${id}/members/${memberId}:archive` },
    { status: 412 },
    { status: "409" },
    { problem: { code: "PRECONDITION_FAILED", detail: input.problem.detail } },
    { problem: { code: "CONFLICT", detail: "group is archived" } },
    {
      problem: {
        code: "CONFLICT",
        detail: input.problem.detail + " private-suffix",
      },
    },
    { problem: { code: "CONFLICT" } },
    { problem: { detail: input.problem.detail } },
    { problem: null },
  ]) {
    const outcome = authorizationCommandOutcome({ ...input, ...change });
    assert.equal(outcome.refusal, "unclassified");
    assert.doesNotMatch(JSON.stringify(outcome), /private/);
  }
});

test("authorization diagnostics exclude paths, identities, payloads and unknown codes", () => {
  for (const problem of [
    null,
    [],
    "private-query",
    {
      code: "PRIVATE_SECRET",
      detail: "private-token",
      subject: "private-identity",
      permissions: ["private-policy"],
    },
  ]) {
    const value = authorizationCommandOutcome({
      method: "private-method",
      path: "/private-path",
      status: "private-status",
      problem,
    });
    assert.deepEqual(value, {
      operation: "unclassified",
      method: "unknown",
      status: undefined,
      code: "unknown",
      refusal: "unclassified",
    });
    assert.doesNotMatch(JSON.stringify(value), /private|PRIVATE/);
  }
});
