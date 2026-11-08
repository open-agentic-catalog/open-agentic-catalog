import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import test from "node:test";
import { browserErrors, browserFailureLocation } from "./browser-errors.mjs";
import { networkObserver } from "./network-observer.mjs";

function fixture() {
  const context = new EventEmitter();
  const pages = [];
  context.pages = () => pages;
  const page = () => {
    const value = new EventEmitter();
    pages.push(value);
    context.emit("page", value);
    return value;
  };
  return { context, page };
}
const consoleEvent = (
  text,
  type = "error",
  native = false,
  url = "https://calibration.invalid/private-path?private-query",
) => ({
  type: () => type,
  text: () => text,
  args: () => (native ? [] : [undefined]),
  location: () => ({ url }),
});

test("context observation covers existing, additional and independent reopened pages", () => {
  const first = fixture();
  const primary = first.page();
  const observer = browserErrors();
  observer.observe(first.context);
  primary.emit("console", consoleEvent("synthetic-private-info", "info"));
  assert.deepEqual(observer.results(), []);
  const additional = first.page();
  additional.emit("console", consoleEvent("synthetic-private-console"));
  primary.emit("pageerror", new Error("synthetic-private-exception"));
  // Duplicate page notifications cannot double count.
  first.context.emit("page", additional);
  additional.emit("console", consoleEvent("synthetic-private-console"));
  const reopened = fixture();
  observer.observe(reopened.context);
  reopened.page().emit("pageerror", new Error("synthetic-private-reopened"));
  assert.deepEqual(observer.results(), [
    { category: "console", count: 2 },
    { category: "page", count: 2 },
  ]);
  assert.doesNotMatch(JSON.stringify(observer.results()), /synthetic-private/);
});

test("only exact native resource messages delegate to mandatory network accounting", () => {
  const f = fixture();
  const observer = browserErrors();
  observer.observe(f.context, ["https://calibration.invalid/trusted-base/"]);
  const page = f.page();
  const native =
    "Failed to load resource: the server responded with a status of 503 (Service Unavailable)";
  page.emit("console", consoleEvent(native, "error", true));
  page.emit(
    "console",
    consoleEvent(
      "Failed to load resource: net::ERR_CONNECTION_REFUSED",
      "error",
      true,
    ),
  );
  assert.deepEqual(observer.results(), []);
  const ledger = networkObserver("https://calibration.invalid");
  ledger.expectFailure("GET", "/private-path", 503);
  ledger.observe(
    "GET",
    "https://calibration.invalid/private-path?private-query",
    503,
  );
  assert.deepEqual(ledger.results(), []);
  ledger.observe(
    "GET",
    "https://calibration.invalid/private-path?private-query",
    503,
  );
  assert.deepEqual(ledger.results(), ["Unexpected UI HTTP 503 (GET)"]);
  for (const text of [
    native,
    native + " private-suffix",
    "Failed to load resource: private-message",
    "Failed to load resource: net::ERR_CONNECTION_REFUSED\nprivate-message",
  ])
    page.emit("console", consoleEvent(text));
  assert.deepEqual(observer.results(), [{ category: "console", count: 4 }]);
  for (const url of [
    "https://foreign.invalid/private-path",
    "",
    "private-invalid-url",
  ])
    page.emit("console", consoleEvent(native, "error", true, url));
  assert.deepEqual(observer.results(), [{ category: "console", count: 7 }]);
});

test("failure locations retain only fixed protocol and route categories", () => {
  for (const [value, expected] of [
    [
      "https://private-user:private-password@private-host/browser/resources/private-id?private-query#private-fragment",
      { protocol: "https:", route: "resources" },
    ],
    [
      "https://private-host/realms/private-realm/protocol/openid-connect/auth?private-token",
      { protocol: "https:", route: "provider" },
    ],
    [
      "https://private-host/private-label/private-id",
      { protocol: "https:", route: "other" },
    ],
    ["data:private-content", { protocol: "other", route: "other" }],
    ["private invalid URL", { protocol: "other", route: "other" }],
  ]) {
    const result = browserFailureLocation(value);
    assert.deepEqual(result, expected);
    assert.doesNotMatch(JSON.stringify(result), /private/);
  }
});

test("diagnostic storage is bounded and page/context shutdown detaches every listener", () => {
  const f = fixture();
  const observer = browserErrors();
  const stop = observer.observe(f.context);
  const page = f.page();
  for (let n = 0; n < 1200; n++) page.emit("console", consoleEvent("private"));
  assert.deepEqual(observer.results(), [{ category: "console", count: 1000 }]);
  page.emit("close");
  assert.equal(page.listenerCount("pageerror"), 0);
  assert.equal(page.listenerCount("console"), 0);
  const additional = f.page();
  f.context.emit("close");
  stop();
  assert.equal(f.context.listenerCount("page"), 0);
  assert.equal(f.context.listenerCount("close"), 0);
  assert.equal(additional.listenerCount("pageerror"), 0);
  assert.equal(additional.listenerCount("console"), 0);
  f.page().emit("console", consoleEvent("private"));
  assert.deepEqual(observer.results(), [{ category: "console", count: 1000 }]);
});
