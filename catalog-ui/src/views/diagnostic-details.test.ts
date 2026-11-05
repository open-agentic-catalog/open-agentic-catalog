import { expect, it } from "vitest";
import { APIError } from "../api/client";
import { diagnosticDetails, diagnosticError } from "./diagnostic-details";

it("copies only allowlisted build and response correlation fields", () => {
  const privateText =
    "PRIVATE_SENTINEL_token_cookie_identity_tenant_query_logs";
  const build = {
    version: "1.2.3",
    commit: "abc123",
    sourceState: "clean",
    identity: privateText,
    instanceId: privateText,
    token: privateText,
  };
  const error = Object.assign(
    new APIError(503, "DEPENDENCY_UNAVAILABLE", "api-request-1"),
    { message: privateText, stack: privateText, details: privateText },
  );
  const value = diagnosticDetails(
    build,
    build,
    { api: error, ui: new Error(privateText) },
    { status: 412, code: "PRECONDITION_FAILED", requestId: "ui-request-2" },
  );
  expect(value).toEqual({
    formatVersion: 1,
    apiBuild: { version: "1.2.3", commit: "abc123", sourceState: "clean" },
    uiBuild: { version: "1.2.3", commit: "abc123", sourceState: "clean" },
    apiReadFailure: {
      status: 503,
      code: "DEPENDENCY_UNAVAILABLE",
      requestId: "api-request-1",
    },
    uiReadFailure: undefined,
    recentFailure: {
      status: 412,
      code: "PRECONDITION_FAILED",
      requestId: "ui-request-2",
    },
  });
  expect(JSON.stringify(value)).not.toContain(privateText);
  expect(new TextEncoder().encode(JSON.stringify(value)).length).toBeLessThan(
    8192,
  );
});

it.each([
  undefined,
  null,
  { version: "a b", commit: "c", sourceState: "clean" },
  { version: "v", commit: "c", sourceState: "private" },
  { version: "v", commit: "x".repeat(129), sourceState: "clean" },
])(
  "omits malformed build facts without serializing their values: %j",
  (value) => {
    expect(diagnosticDetails(value, value, {})).toEqual({
      formatVersion: 1,
      apiBuild: undefined,
      uiBuild: undefined,
      apiReadFailure: undefined,
      uiReadFailure: undefined,
      recentFailure: undefined,
    });
  },
);

it.each([0, 100, 599])(
  "keeps a valid status and omits unavailable correlation: %s",
  (status) => {
    expect(diagnosticError(new APIError(status, "request_failed"))).toEqual({
      status,
      code: "request_failed",
      requestId: "",
    });
  },
);
it.each([1, -1, 600, 100.5, NaN])(
  "refuses malformed diagnostic status %s and unsafe identifiers",
  (status) => {
    const error = Object.assign(new APIError(status, "safe"), {
      code: "private\ncode",
      requestId: "x".repeat(129),
    });
    expect(diagnosticError(error)).toEqual({
      status: 0,
      code: "",
      requestId: "",
    });
  },
);
it("does not coerce malformed diagnostic values or copy ordinary error messages", () => {
  expect(diagnosticError(new Error("PRIVATE"))).toBeUndefined();
  const error = new APIError(200, "safe");
  Object.assign(error, {
    status: "PRIVATE",
    code: { toString: "PRIVATE" },
    requestId: { private: true },
  });
  expect(diagnosticError(error)).toEqual({
    status: 0,
    code: "",
    requestId: "",
  });
});
