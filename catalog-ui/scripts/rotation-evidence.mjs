// Narrow evidence for the owned packaged rotation. Never retain secret-bearing
// success bodies, command keys, dependency text or unrestricted container logs.
const requestID = (value) =>
  typeof value === "string" && /^[A-Za-z0-9._-]{1,128}$/.test(value)
    ? value
    : undefined;

/** Select fixed response evidence without retaining response documents. */
export function rotationResponse({ status, headers, problem, elapsedMs }) {
  const code = [
    "DEPENDENCY_UNAVAILABLE",
    "REQUEST_DEADLINE_EXCEEDED",
    "INTERNAL",
    "CONFLICT",
    "PRECONDITION_FAILED",
    "FORBIDDEN",
    "UNAUTHENTICATED",
    "INVALID_ARGUMENT",
    "IDEMPOTENCY_KEY_REUSED",
  ].includes(problem?.code)
    ? problem.code
    : undefined;
  return {
    operation: "webhook.rotate-secret",
    status,
    code,
    requestId: requestID(headers["x-request-id"]),
    elapsedMs: Math.max(0, Math.round(elapsedMs)),
    retryAfter: /^\d{1,5}$/.test(headers["retry-after"] ?? "")
      ? headers["retry-after"]
      : undefined,
  };
}

/** Build an exact command-scoped, read-only projection for an owned database. */
export function rotationStateSQL({ tenant, endpoint, key, realm, membership }) {
  if (
    !/^[0-9a-f-]{36}$/.test(tenant) ||
    !/^[0-9a-f-]{36}$/.test(endpoint) ||
    !/^[0-9a-f-]{36}$/.test(realm) ||
    !/^[0-9a-f-]{36}$/.test(membership) ||
    typeof key !== "string" ||
    key.length < 1 ||
    key.length > 200
  )
    throw new Error("Invalid owned rotation scope");
  const literal = (value) => "'" + value.replaceAll("'", "''") + "'";
  return `WITH command AS (
    SELECT reservation_id, atomic_response_status, completed_at IS NOT NULL AS completed,
      released_at IS NOT NULL AS released
    FROM idempotency_keys WHERE tenant_id=${literal(tenant)}
      AND protocol_version=2 AND actor=${literal("membership:" + realm + ":" + membership)} AND route=${literal("POST /api/v1/webhooks/" + endpoint + ":rotate-secret")}
      AND idempotency_key=${literal(key)} LIMIT 1
  ) SELECT json_build_object(
    'endpoint', (SELECT json_build_object('revision',revision,'secretVersion',secret_version,'status',status,
      'databaseClockBeforeUpdate',clock_timestamp()<updated_at)
      FROM webhook_endpoints WHERE tenant_id=${literal(tenant)} AND id=${literal(endpoint)}),
    'command', (SELECT json_build_object('reserved',true,'completed',completed,'released',released,
      'responseStatus',atomic_response_status) FROM command),
    'rotationAuditCount', (SELECT count(*) FROM audit_events WHERE tenant_id=${literal(tenant)}
      AND target_id=${literal(endpoint)} AND action='webhook.secret_rotated'
      AND details->>'commandId'=(SELECT reservation_id::text FROM command))
  );`;
}

/** Diagnostic failure must never replace the original assertion or skip cleanup. */
export async function collectRotationEvidence(collect, unavailable) {
  try {
    await collect();
    return true;
  } catch {
    unavailable();
    return false;
  }
}

/** Select only code-owned correlated diagnostics from bounded JSON logs. */
export function rotationDiagnostics(logs, id) {
  if (!requestID(id)) return [];
  return logs
    .split("\n")
    .flatMap((line) => {
      let item;
      try {
        item = JSON.parse(line);
      } catch {
        return [];
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) return [];
      if (item.request_id !== id && item.upstream_request_id !== id) return [];
      if (
        ![
          "http.request_failed",
          "idempotency.completion_failed",
          "idempotency.inspection_required",
          "ui.request_failed",
          "dependency.unavailable",
        ].includes(item.event)
      )
        return [];
      return [
        {
          event: item.event,
          requestId: requestID(item.request_id),
          upstreamRequestId: requestID(item.upstream_request_id),
          code: [
            "DEPENDENCY_UNAVAILABLE",
            "REQUEST_DEADLINE_EXCEEDED",
            "INTERNAL",
            "dependency_unavailable",
            "CONFLICT",
          ].includes(item.error_code)
            ? item.error_code
            : undefined,
          ...([
            "webhook.endpoint.time_regression",
            "authorization.assignment.time_regression",
            "authorization.group.time_regression",
          ].includes(item.operation_stage)
            ? { operationStage: item.operation_stage }
            : {}),
        },
      ];
    })
    .slice(-10);
}

/** Collect segments independently; a missing database cannot hide process logs. */
export async function rotationParts(collectors) {
  const result = {};
  for (const [name, collect] of Object.entries(collectors)) {
    try {
      result[name] = { status: "available", value: await collect() };
    } catch {
      result[name] = { status: "unavailable" };
    }
  }
  return result;
}

/** Classify bounded PostgreSQL logs without retaining private statement text. */
export function rotationDatabaseDiagnostics(logs) {
  return {
    webhookLifecycleRejection:
      /ERROR:\s+webhook endpoint lifecycle transition is invalid\s*$/m.test(
        logs,
      ),
  };
}
