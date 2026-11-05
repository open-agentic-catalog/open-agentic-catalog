// Owned-fixture refusals expose fixed categories, never policy or request data.
export function authorizationCommandOutcome({ method, path, status, problem }) {
  const uuid = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
  const context = [
    [
      "PATCH",
      new RegExp(`^/api/v1/access/roles/${uuid}$`),
      "role.update",
      "role cannot be changed in this state or time",
      "role_state_or_time",
    ],
    [
      "POST",
      new RegExp(
        `^/api/v1/access/groups/${uuid}:(suspend|reactivate|archive)$`,
      ),
      "group.transition",
      "group cannot be changed in this state or time",
      "group_state_or_time",
    ],
    [
      "POST",
      new RegExp(`^/api/v1/access/assignments/${uuid}:revoke$`),
      "assignment.revoke",
      "assignment cannot be revoked",
      "assignment_state_or_time",
    ],
    [
      "POST",
      new RegExp(`^/api/v1/access/groups/${uuid}/members/${uuid}:revoke$`),
      "group_member.revoke",
      "group member cannot be revoked",
      "group_member_state_or_time",
    ],
  ].find(
    ([verb, route]) =>
      method === verb && typeof path === "string" && route.test(path),
  );
  const code = [
    "CONFLICT",
    "PRECONDITION_FAILED",
    "IDEMPOTENCY_KEY_REUSED",
    "FORBIDDEN",
    "UNAUTHENTICATED",
    "DEPENDENCY_UNAVAILABLE",
    "INVALID_ARGUMENT",
    "INTERNAL",
    "NOT_FOUND",
    "SEPARATION_OF_DUTIES",
    "METHOD_NOT_ALLOWED",
    "VALIDATION_FAILED",
    "VERSION_SEALED",
    "INVALID_TRANSITION",
    "UNSUPPORTED_PROFILE",
    "PUBLICATION_NOT_READY",
    "SOURCE_POLICY_DENIED",
    "AUDIT_EXPORT_RANGE_TOO_LARGE",
    "WORK_LIMIT_EXCEEDED",
    "REQUEST_DEADLINE_EXCEEDED",
  ].includes(problem?.code)
    ? problem.code
    : "unknown";
  return {
    operation: context?.[2] ?? "unclassified",
    method: ["GET", "POST", "PATCH", "DELETE"].includes(method)
      ? method
      : "unknown",
    status:
      Number.isInteger(status) && status >= 100 && status <= 599
        ? status
        : undefined,
    code,
    refusal:
      context &&
      status === 409 &&
      code === "CONFLICT" &&
      problem?.detail === context[3]
        ? context[4]
        : "unclassified",
  };
}

// Preserve the setup-error status/code contract used by negative journeys.
export function fixtureSetupError(input) {
  const outcome = authorizationCommandOutcome(input);
  return new Error(
    `Owned fixture setup failed (${outcome.method}, ${outcome.status}, ${outcome.code}); ${JSON.stringify(outcome)}`,
  );
}

// A refused role update can be inspected without logging its private metadata.
// HTTP Date has whole-second precision; only an entirely earlier second proves
// that the response clock precedes the retained update time.
export function roleRefusalReadback({ status, document, etag, date }) {
  const revision = document?.revision;
  const knownRevision = Number.isSafeInteger(revision) && revision > 0;
  const knownETag =
    typeof etag === "string" &&
    etag.length <= 24 &&
    /^"rev-[1-9][0-9]*"$/.test(etag);
  const updated =
    typeof document?.updatedAt === "string" && document.updatedAt.length <= 64
      ? Date.parse(document.updatedAt)
      : NaN;
  const received =
    typeof date === "string" && date.length <= 64 ? Date.parse(date) : NaN;
  const success = status === 200;
  return {
    readSucceeded: success,
    roleState:
      success && ["active", "archived"].includes(document?.state)
        ? document.state
        : "unknown",
    revisionMatchesExpected:
      success && knownRevision && knownETag
        ? etag === `"rev-${revision}"`
        : undefined,
    responseClockBeforeUpdate:
      success && Number.isFinite(updated) && Number.isFinite(received)
        ? received + 1000 <= updated
        : undefined,
  };
}
