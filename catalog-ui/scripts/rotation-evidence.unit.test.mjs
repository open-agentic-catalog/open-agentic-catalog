import assert from "node:assert/strict";
import test from "node:test";
import {
  rotationParts,
  rotationDiagnostics,
  rotationDatabaseDiagnostics,
  rotationResponse,
  rotationStateSQL,
  collectRotationEvidence,
} from "./rotation-evidence.mjs";

test("rotation evidence excludes private documents and unadmitted headers", () => {
  const evidence = rotationResponse({
    status: 503,
    headers: {
      "x-request-id": "owned-123",
      "retry-after": "1",
      authorization: "private-token",
    },
    problem: {
      code: "DEPENDENCY_UNAVAILABLE",
      detail: "private-sql",
      secret: "private-secret",
    },
    elapsedMs: 12.6,
  });
  assert.deepEqual(evidence, {
    operation: "webhook.rotate-secret",
    status: 503,
    code: "DEPENDENCY_UNAVAILABLE",
    requestId: "owned-123",
    elapsedMs: 13,
    retryAfter: "1",
  });
  const malformed = rotationResponse({
    status: 503,
    headers: {
      "x-request-id": "private\ninput",
      "retry-after": "private-text",
    },
    problem: { code: "private-code" },
    elapsedMs: 0,
  });
  assert.equal(malformed.code, undefined);
  assert.equal(malformed.requestId, undefined);
  assert.equal(malformed.retryAfter, undefined);
  assert.equal(
    JSON.stringify(
      rotationResponse({
        status: 200,
        headers: {},
        problem: { secret: "private-secret" },
        elapsedMs: 0,
      }),
    ).includes("private-secret"),
    false,
  );
});

test("rotation state projection scopes exact tenant endpoint command and excludes retained bodies", () => {
  const sql = rotationStateSQL({
    tenant: "00000000-0000-7000-8000-000000000001",
    endpoint: "00000000-0000-7000-8000-000000000002",
    key: "owned'command",
    realm: "00000000-0000-7000-8000-000000000003",
    membership: "00000000-0000-7000-8000-000000000004",
  });
  assert.match(sql, /idempotency_key='owned''command'/);
  assert.match(sql, /protocol_version=2/);
  assert.match(
    sql,
    /actor='membership:00000000-0000-7000-8000-000000000003:00000000-0000-7000-8000-000000000004'/,
  );
  assert.doesNotMatch(sql, /actor='local-admin'/);
  assert.match(sql, /POST \/api\/v1\/webhooks\/[0-9a-f-]+:rotate-secret/);
  assert.match(sql, /details->>'commandId'/);
  assert.match(sql, /action='webhook.secret_rotated'/);
  assert.match(
    sql,
    /'databaseClockBeforeUpdate',clock_timestamp\(\)<updated_at/,
  );
  assert.doesNotMatch(sql, /atomic_response_body|encrypted_secret|SELECT \*/);
  assert.throws(() =>
    rotationStateSQL({ tenant: "private", endpoint: "private", key: "key" }),
  );
});

test("correlated diagnostics allowlist actual time-regression stages", () => {
  for (const stage of [
    "webhook.endpoint.time_regression",
    "authorization.assignment.time_regression",
    "authorization.group.time_regression",
    "private-stage-secret",
    undefined,
  ]) {
    const [record] = rotationDiagnostics(
      JSON.stringify({
        event: "http.request_failed",
        request_id: "owned",
        error_code:
          stage === "authorization.assignment.time_regression" ||
          stage === "authorization.group.time_regression"
            ? "CONFLICT"
            : "DEPENDENCY_UNAVAILABLE",
        operation_stage: stage,
        updated_at: "private-timestamp",
        encrypted_secret: "private-secret",
      }),
      "owned",
    );
    assert.equal(
      record.code,
      stage === "authorization.assignment.time_regression" ||
        stage === "authorization.group.time_regression"
        ? "CONFLICT"
        : "DEPENDENCY_UNAVAILABLE",
    );
    if (
      stage === "webhook.endpoint.time_regression" ||
      stage === "authorization.assignment.time_regression" ||
      stage === "authorization.group.time_regression"
    )
      assert.equal(record.operationStage, stage);
    else assert.equal("operationStage" in record, false);
    assert.doesNotMatch(JSON.stringify(record), /private-/);
  }
});

test("failed evidence collection preserves original assertion and independent cleanup", async () => {
  const observed = [];
  try {
    await collectRotationEvidence(
      async () => {
        throw new Error("private-database-error");
      },
      () => observed.push("unavailable"),
    );
    throw new Error("original assertion");
  } catch (error) {
    assert.equal(error.message, "original assertion");
  } finally {
    observed.push("cleanup");
  }
  assert.deepEqual(observed, ["unavailable", "cleanup"]);
});

test("correlated log projection excludes private fields and other requests", () => {
  const result = rotationDiagnostics(
    [
      JSON.stringify({
        event: "idempotency.completion_failed",
        request_id: "owned",
        error_code: "DEPENDENCY_UNAVAILABLE",
        msg: "private SQL",
        idempotency_key_digest: "private",
      }),
      JSON.stringify({
        event: "ui.request_failed",
        request_id: "ui-owned",
        upstream_request_id: "owned",
        error_code: "private code",
        msg: "private",
      }),
      JSON.stringify({
        event: "http.request_failed",
        request_id: "other",
        error_code: "DEPENDENCY_UNAVAILABLE",
      }),
      "private malformed log",
      "null",
      "123",
      "[]",
    ].join("\n"),
    "owned",
  );
  assert.equal(result.length, 2);
  assert.equal(result[0].code, "DEPENDENCY_UNAVAILABLE");
  assert.equal(result[1].upstreamRequestId, "owned");
  assert.doesNotMatch(JSON.stringify(result), /private|other/);
  assert.deepEqual(rotationDiagnostics("{}", "invalid\n"), []);
});

test("database failure still collects safe containers and correlated logs", async () => {
  const observed = [];
  const result = await rotationParts({
    state: async () => {
      observed.push("state");
      throw new Error("private database failure");
    },
    containers: async () => {
      observed.push("containers");
      return [{ service: "api", restarts: 0 }];
    },
    diagnostics: async () => {
      observed.push("diagnostics");
      return [{ event: "idempotency.completion_failed" }];
    },
  });
  assert.deepEqual(observed, ["state", "containers", "diagnostics"]);
  assert.deepEqual(result.state, { status: "unavailable" });
  assert.equal(result.containers.status, "available");
  assert.equal(result.diagnostics.status, "available");
  assert.doesNotMatch(JSON.stringify(result), /private/);
});

test("rotation request IDs follow the API and UI admission grammar", () => {
  for (const value of ["private:input", "line\ninput", "a".repeat(129)]) {
    assert.equal(
      rotationResponse({
        status: 503,
        headers: { "x-request-id": value },
        elapsedMs: 0,
      }).requestId,
      undefined,
    );
  }
});

test("host JSON dependency diagnostics retain distinct admitted UI and API IDs", () => {
  const records = rotationDiagnostics(
    JSON.stringify({
      level: "WARN",
      msg: "UI dependency unavailable",
      request_id: "current-safe",
      upstream_request_id: "upstream-safe",
      route: "catalog",
      status: 503,
      event: "dependency.unavailable",
      component: "ui_api",
      error_code: "dependency_unavailable",
    }),
    "upstream-safe",
  );
  assert.deepEqual(records, [
    {
      event: "dependency.unavailable",
      requestId: "current-safe",
      upstreamRequestId: "upstream-safe",
      code: "dependency_unavailable",
    },
  ]);
});

test("database diagnostics classify the fixed lifecycle error without retaining statements", () => {
  for (const [logs, expected] of [
    [
      "2026-10-05 ERROR:  webhook endpoint lifecycle transition is invalid\nSTATEMENT: private encrypted_secret and private SQL",
      true,
    ],
    ["ERROR: private dependency failure\nSTATEMENT: private SQL", false],
    ["LOG: webhook endpoint lifecycle transition is invalid", false],
    [
      "ERROR: webhook endpoint lifecycle transition is invalid private suffix",
      false,
    ],
    ["", false],
  ]) {
    const result = rotationDatabaseDiagnostics(logs);
    assert.deepEqual(result, { webhookLifecycleRejection: expected });
    assert.doesNotMatch(JSON.stringify(result), /private|secret|STATEMENT/);
  }
});
