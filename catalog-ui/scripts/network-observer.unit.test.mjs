import { test } from "node:test";
import assert from "node:assert/strict";
import { networkObserver } from "./network-observer.mjs";

test("asset diagnostics retain ordered outcomes without private request or failure text", () => {
  const ledger = networkObserver("http://127.0.0.1:50000", "/ui");
  const service =
    "http://127.0.0.1:50000/ui/assets/Service-private.js?token=private";
  const discovery =
    "http://127.0.0.1:50000/ui/assets/DiscoveryWorkspace-private.js";
  ledger.request("GET", service);
  ledger.observe("GET", service, 200);
  ledger.finished("GET", service);
  ledger.request("GET", discovery);
  ledger.failure("GET", discovery, "net::ERR_CONNECTION_RESET");
  assert.deepEqual(ledger.diagnostics(), {
    assets: [
      { asset: "service", event: "request" },
      { asset: "service", event: "response", status: 200 },
      { asset: "service", event: "finished" },
      { asset: "discovery", event: "request" },
      { asset: "discovery", event: "failed", reason: "connection-reset" },
    ],
    transports: [{ family: "asset", reason: "connection-reset" }],
    truncated: false,
  });
  ledger.expectFailure("GET", "/assets/Service-private.js", 404);
  ledger.observe("GET", service, 404);
  ledger.failure("GET", service, "net::ERR_ABORTED");
  ledger.failure("GET", discovery, "private transport cause");
  assert.deepEqual(ledger.diagnostics().assets.slice(-3), [
    { asset: "service", event: "response", status: 404 },
    { asset: "service", event: "failed", reason: "aborted" },
    { asset: "discovery", event: "failed", reason: "other" },
  ]);
  assert.equal(JSON.stringify(ledger.diagnostics()).includes("private"), false);
  assert.equal(
    ledger.results().length,
    2,
    "Diagnostics do not excuse transport failures",
  );
});

test("asset diagnostics exclude other origins, paths and methods and bound retained events", () => {
  const ledger = networkObserver("http://127.0.0.1:50000", "/ui");
  for (const url of [
    "http://127.0.0.1:50001/ui/assets/Service-hash.js",
    "http://127.0.0.1:50000/assets/Service-hash.js",
    "http://127.0.0.1:50000/ui/_catalog/api/v1/private",
    "http://127.0.0.1:50000/ui/assets/PrivateName.js",
  ])
    ledger.request("GET", url);
  const url = "http://127.0.0.1:50000/ui/assets/Service-hash.js";
  ledger.request("POST", url);
  assert.deepEqual(ledger.diagnostics(), {
    assets: [],
    transports: [],
    truncated: false,
  });
  for (let at = 0; at < 33; at++) ledger.request("GET", url);
  const evidence = ledger.diagnostics();
  assert.equal(evidence.assets.length, 32);
  assert.equal(evidence.truncated, true);
  evidence.assets[0].asset = "changed";
  evidence.assets.pop();
  assert.equal(ledger.diagnostics().assets[0].asset, "service");
  assert.equal(ledger.diagnostics().assets.length, 32);
});

test("network evidence separates expected rejections from unexpected HTTP and transport failures", () => {
  const ledger = networkObserver("http://127.0.0.1:50000", "/ui");
  const url =
    "http://127.0.0.1:50000/ui/_catalog/api/v1/namespaces?private=value";
  ledger.expectFailure("GET", "/_catalog/api/v1/namespaces", 503);
  ledger.observe("GET", url, 200);
  ledger.observe("GET", url, 503);
  assert.deepEqual(ledger.results(), []);
  ledger.observe("GET", url, 503);
  ledger.failure("POST", url, "private transport text");
  assert.deepEqual(ledger.results(), [
    "Unexpected UI HTTP 503 (GET)",
    "Unexpected UI transport failure (POST)",
  ]);
  assert.ok(!JSON.stringify(ledger.results()).includes("private"));
});

test("only exact anonymous session probes and cancellation have built-in exceptions", () => {
  const ledger = networkObserver("http://127.0.0.1:50000", "/ui");
  ledger.observe("GET", "http://127.0.0.1:50000/ui/auth/session", 401);
  ledger.observe("GET", "http://127.0.0.1:50001/ui/auth/session", 503);
  ledger.failure(
    "GET",
    "http://127.0.0.1:50000/ui/assets/view.js",
    "net::ERR_ABORTED",
  );
  assert.deepEqual(ledger.results(), []);
  ledger.observe("POST", "http://127.0.0.1:50000/ui/auth/session", 401);
  ledger.observe("GET", "http://127.0.0.1:50000/ui/_catalog/api/v1/me", 401);
  assert.equal(ledger.results().length, 2);
  // Startup stabilization must never become permission to ignore a failed
  // login POST if the network changes later in the actual browser journey.
  ledger.failure(
    "POST",
    "http://127.0.0.1:50000/ui/auth/login",
    "net::ERR_NETWORK_CHANGED",
  );
  assert.equal(
    ledger.results().at(-1),
    "Unexpected UI transport failure (POST)",
  );
});

test("unused, wrong-method and wrong-path expectations cannot hide regressions", () => {
  const ledger = networkObserver("http://127.0.0.1:50000");
  ledger.expectFailure("POST", "/one", 0);
  ledger.failure("GET", "http://127.0.0.1:50000/one", "net::ERR_FAILED");
  ledger.failure("POST", "http://127.0.0.1:50000/two", "net::ERR_FAILED");
  assert.equal(ledger.results().length, 3);
  ledger.failure("POST", "http://127.0.0.1:50000/one", "net::ERR_FAILED");
  assert.equal(ledger.results().length, 2);
});

test("network declarations and diagnostics are bounded", () => {
  for (const [method, path, status, count] of [
    ["get", "/ok", 500, 1],
    ["GET", "/?secret", 500, 1],
    ["GET", "/ok", 200, 1],
    ["GET", "/ok", 600, 1],
    ["GET", "/ok", 500, 0],
  ]) {
    assert.throws(() =>
      networkObserver("http://127.0.0.1").expectFailure(
        method,
        path,
        status,
        count,
      ),
    );
  }
  const ledger = networkObserver("http://127.0.0.1");
  for (let i = 0; i < 101; i++)
    ledger.observe("GET", "http://127.0.0.1/private", 500);
  assert.equal(ledger.results().length, 100);
  for (let i = 0; i < 100; i++) ledger.expectFailure("GET", "/ok", 500);
  assert.throws(() => ledger.expectFailure("GET", "/ok", 500));
});

test("GET failure diagnostics retain bounded fixed families and reasons without private values", () => {
  const ledger = networkObserver("http://127.0.0.1:50000", "/ui");
  const url =
    "http://127.0.0.1:50000/ui/_catalog/api/v1/resources/private?token=private";
  ledger.failure("GET", url, "net::ERR_NETWORK_CHANGED");
  ledger.failure("GET", url, "private cause");
  ledger.failure("GET", url, "net::ERR_ABORTED");
  ledger.failure(
    "GET",
    "http://127.0.0.1:50001/private",
    "net::ERR_CONNECTION_RESET",
  );
  assert.deepEqual(ledger.diagnostics().transports, [
    { family: "api", reason: "network-changed" },
    { family: "api", reason: "other" },
  ]);
  assert.equal(
    ledger.results().length,
    2,
    "Diagnostics never excuse a failed GET",
  );
  for (let index = 0; index < 33; index++)
    ledger.failure("GET", url, "private cause");
  const evidence = ledger.diagnostics();
  assert.equal(evidence.transports.length, 32);
  assert.equal(evidence.truncated, true);
  assert.equal(JSON.stringify(evidence).includes("private"), false);
  evidence.transports.length = 0;
  assert.equal(ledger.diagnostics().transports.length, 32);
});
