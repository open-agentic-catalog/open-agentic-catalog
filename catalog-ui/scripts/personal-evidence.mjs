// Owned OIDC diagnostics retain fixed classifications, never private item data.
export function personalCommandOutcome({ status, method, problem, requestId }) {
  const code = [
    "CONFLICT",
    "PRECONDITION_FAILED",
    "IDEMPOTENCY_KEY_REUSED",
    "FORBIDDEN",
    "UNAUTHENTICATED",
    "DEPENDENCY_UNAVAILABLE",
    "INVALID_ARGUMENT",
    "INTERNAL",
  ].includes(problem?.code)
    ? problem.code
    : undefined;
  return {
    operation:
      method === "DELETE"
        ? "personal.remove"
        : method === "PATCH"
          ? "personal.update"
          : "personal.save",
    status,
    code,
    refusal:
      status === 409 &&
      code === "CONFLICT" &&
      problem?.detail ===
        "saved item transition time is before its retained update"
        ? "transition_time_regression"
        : "unclassified",
    requestId:
      typeof requestId === "string" && /^[A-Za-z0-9._-]{1,128}$/.test(requestId)
        ? requestId
        : undefined,
  };
}
