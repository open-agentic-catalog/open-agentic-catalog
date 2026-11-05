// Diagnostic copying projects a closed set of public build identifiers and safe response correlation.
import { APIError } from "../api/client";
import { isBuildInfo, type BuildInfo } from "../api/build-info";

export type DiagnosticError = {
  status: number;
  code: string;
  requestId: string;
};
function errorDetails(
  status: unknown,
  code: unknown,
  requestId: unknown,
): DiagnosticError {
  return {
    status:
      typeof status === "number" &&
      Number.isInteger(status) &&
      (status === 0 || (status >= 100 && status <= 599))
        ? status
        : 0,
    code:
      typeof code === "string" && /^[A-Za-z][A-Za-z0-9_]{0,79}$/.test(code)
        ? code
        : "",
    requestId:
      typeof requestId === "string" && /^[A-Za-z0-9._-]{1,128}$/.test(requestId)
        ? requestId
        : "",
  };
}
export function diagnosticError(cause: unknown): DiagnosticError | undefined {
  if (!(cause instanceof APIError)) return;
  return errorDetails(cause.status, cause.code, cause.requestId);
}

/** Pick individual fields; never serialize a response, Error, session or process record wholesale. */
export function diagnosticDetails(
  api: unknown,
  ui: unknown,
  failures: { api?: unknown; ui?: unknown },
  recent?: DiagnosticError,
) {
  const build = (value: unknown): BuildInfo | undefined =>
    isBuildInfo(value)
      ? {
          version: value.version,
          commit: value.commit,
          sourceState: value.sourceState,
        }
      : undefined;
  const safeRecent = recent
    ? errorDetails(recent.status, recent.code, recent.requestId)
    : undefined;
  return {
    formatVersion: 1,
    apiBuild: build(api),
    uiBuild: build(ui),
    apiReadFailure: diagnosticError(failures.api),
    uiReadFailure: diagnosticError(failures.ui),
    recentFailure: safeRecent,
  };
}
