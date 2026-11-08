import test from "node:test";
import assert from "node:assert/strict";
import { rendererErrors } from "../../../test/vscode/renderer-errors.cjs";

test("owned renderer observations attribute contexts, preserve frame history and retain only fixed counts", () => {
  const errors = rendererErrors();
  const emit = (method, params, sessionId = "owned") =>
    errors.observe({ method, params, sessionId });
  emit("Runtime.exceptionThrown", {
    exceptionDetails: {
      executionContextId: 1,
      text: "private transcript token",
    },
  });
  emit("Runtime.consoleAPICalled", {
    executionContextId: 1,
    type: "error",
    args: [{ value: "private argument" }],
  });
  emit("Runtime.consoleAPICalled", { executionContextId: 2, type: "error" });
  emit("Runtime.consoleAPICalled", { executionContextId: 1, type: "log" });
  assert.deepEqual(
    errors.counts(),
    {},
    "Unrelated workbench contexts are not owned",
  );
  errors.own("owned", 1, "old-frame");
  assert.deepEqual(errors.counts(), { exception: 1, console: 1 });
  errors.own("replacement", 1, "new-frame");
  emit(
    "Runtime.exceptionThrown",
    { exceptionDetails: { executionContextId: 1 } },
    "replacement",
  );
  emit("Runtime.consoleAPICalled", { executionContextId: 1, type: "assert" });
  assert.deepEqual(
    errors.counts(),
    { exception: 2, console: 2 },
    "Frame replacement does not erase old errors",
  );
  assert.equal(JSON.stringify(errors.counts()).includes("private"), false);
});

test("owned asset failures are bounded, scoped, counted once and omit intentional cancellation", () => {
  const errors = rendererErrors();
  errors.own("owned", 1, "frame");
  const emit = (method, params) =>
    errors.observe({ method, params, sessionId: "owned" });
  const start = (id, url, frameId = "frame") =>
    emit("Network.requestWillBeSent", {
      requestId: id,
      frameId,
      request: { url },
    });
  for (const url of [
    "https://foreign.example/catalog.js",
    "https://catalog.example/api/v1/private",
    "data:image/png,private",
    "invalid",
  ]) {
    start("unowned", url);
    emit("Network.loadingFailed", { requestId: "unowned" });
  }
  assert.deepEqual(errors.counts(), {});
  const url =
    "https://owned.vscode-cdn.net/file/extensions/open-agentic-catalog.open-agentic-catalog-0.1.0/media/catalog.js?private=token";
  start("cancel", url);
  emit("Network.loadingFailed", { requestId: "cancel", canceled: true });
  start("ok", url);
  emit("Network.responseReceived", {
    requestId: "ok",
    response: { status: 200 },
  });
  emit("Network.loadingFinished", { requestId: "ok" });
  start("other", url, "unrelated-frame");
  emit("Network.loadingFailed", {
    requestId: "other",
    errorText: "private error",
  });
  assert.deepEqual(errors.counts(), {});
  start("missing", url);
  emit("Network.responseReceived", {
    requestId: "missing",
    response: { status: 404 },
  });
  emit("Network.loadingFailed", { requestId: "missing" });
  assert.deepEqual(errors.counts(), { asset: 1 });
  start("network", url);
  emit("Network.loadingFailed", { requestId: "network" });
  assert.deepEqual(errors.counts(), { asset: 2 });
  for (let i = 0; i < 130; i++) start("held" + i, url);
  assert.deepEqual(errors.counts(), { asset: 2, overflow: 1 });
  assert.equal(JSON.stringify(errors.counts()).includes("private"), false);
});

test("renderer observation overflow fails closed without retaining unbounded event data", () => {
  const errors = rendererErrors();
  errors.own("owned", 1);
  for (let i = 0; i < 300; i++)
    errors.observe({
      method: "Runtime.consoleAPICalled",
      sessionId: "owned",
      params: {
        type: "error",
        executionContextId: 1,
        args: [{ value: "private".repeat(100) }],
      },
    });
  assert.deepEqual(errors.counts(), { console: 256, overflow: 1 });
});

test("unrelated workbench noise cannot manufacture an owned renderer failure", () => {
  const errors = rendererErrors();
  for (let i = 0; i < 300; i++)
    errors.observe({
      method: "Runtime.consoleAPICalled",
      sessionId: "workbench",
      params: { type: "error", executionContextId: 1 },
    });
  errors.own("owned", 2, "frame");
  assert.deepEqual(errors.counts(), {});
  errors.observe({
    method: "Runtime.exceptionThrown",
    sessionId: "startup",
    params: { exceptionDetails: { executionContextId: 7, text: "private" } },
  });
  errors.ownSession("startup");
  assert.deepEqual(
    errors.counts(),
    { exception: 1 },
    "Exact extension ownership retains errors before DOM polling",
  );
});
