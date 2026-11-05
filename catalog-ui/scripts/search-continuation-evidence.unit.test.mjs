import test from "node:test";
import assert from "node:assert/strict";
import { searchContinuationRefusal } from "./search-continuation-evidence.mjs";

test("search continuation evidence identifies only reviewed HTTP problem details", () => {
  for (const [detail, expected] of [
    ["catalog cursor is invalid for this request", "catalog_binding"],
    ["catalog cursor has expired", "catalog_expired"],
    ["catalog cursor is stale", "catalog_generation"],
    [
      "live search cursor is invalid or expired; restart the search",
      "live_cursor_validation",
    ],
  ])
    assert.equal(
      searchContinuationRefusal(400, { code: "INVALID_ARGUMENT", detail }),
      expected,
    );
});

test("search continuation evidence requires the exact status, code and detail", () => {
  const detail = "catalog cursor is stale";
  for (const [status, problem] of [
    [503, { code: "INVALID_ARGUMENT", detail }],
    [400, { code: "INTERNAL", detail }],
    ["400", { code: "INVALID_ARGUMENT", detail }],
    [400, { code: "INVALID_ARGUMENT", detail: detail + " private-suffix" }],
    [400, { code: "INVALID_ARGUMENT", detail: "catalog cursor is invalid" }],
    [400, { code: "INVALID_ARGUMENT", detail: "private-cause" }],
    [400, { code: "INVALID_ARGUMENT" }],
    [400, { detail }],
    [400, { code: "INVALID_ARGUMENT", detail: null }],
    [400, { code: "INVALID_ARGUMENT", detail: [detail] }],
    [400, { code: ["INVALID_ARGUMENT"], detail }],
    [400, undefined],
    [400, null],
    [400, []],
    [400, "private-token"],
  ]) {
    const outcome = searchContinuationRefusal(status, problem);
    assert.equal(outcome, "unclassified");
    assert.doesNotMatch(outcome, /private/);
  }
});

test("known and unknown search refusals omit private fields without changing the problem", () => {
  for (const [detail, expected] of [
    ["catalog cursor is invalid for this request", "catalog_binding"],
    ["private-cause", "unclassified"],
  ]) {
    const problem = Object.freeze({
      code: "INVALID_ARGUMENT",
      detail,
      query: "private-query",
      cursor: "private-cursor",
      cause: Object.freeze({ token: "private-token" }),
    });
    const before = JSON.stringify(problem);
    const outcome = searchContinuationRefusal(400, problem);
    assert.equal(outcome, expected);
    assert.doesNotMatch(outcome, /private|query|cursor|token|cause/);
    assert.equal(JSON.stringify(problem), before);
  }
});
