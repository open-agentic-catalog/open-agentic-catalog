import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { webview } from "../../../test/vscode/webview.cjs";

test("CDP deadlines retain the phase and caller without replaying unanswered commands", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-cdp-deadline-unit-"));
  const original = globalThis.WebSocket;
  await writeFile(join(directory, "DevToolsActivePort"), "1234\n/owned-unit\n");
  t.after(async () => {
    globalThis.WebSocket = original;
    await rm(directory, { recursive: true, force: true });
  });
  t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
  for (const [stage, workbench, surface] of [
    ["discovery", false, "catalog"],
    ["probe", "guide", "guide"],
    ["interaction", true, "workbench"],
    ["discovery", "browser", "browser-login"],
  ]) {
    const phase = stage === "interaction" ? "interaction" : "discovery";
    const waiting = Promise.withResolvers();
    const calls = [];
    let closed = false;
    globalThis.WebSocket = class extends EventTarget {
      constructor() {
        super();
        queueMicrotask(() => this.dispatchEvent(new Event("open")));
      }
      emit(value) {
        this.dispatchEvent(
          new MessageEvent("message", { data: JSON.stringify(value) }),
        );
      }
      send(encoded) {
        const request = JSON.parse(encoded);
        calls.push(request);
        queueMicrotask(() => {
          const reply = (result) => this.emit({ id: request.id, result });
          if (request.method === "Target.getTargets")
            reply({ targetInfos: [{ type: "iframe", targetId: "owned" }] });
          else if (request.method === "Target.attachToTarget")
            reply({ sessionId: "owned-session" });
          else if (request.method === "Runtime.enable") {
            if (stage === "discovery") {
              waiting.resolve();
              return;
            }
            this.emit({
              method: "Runtime.executionContextCreated",
              sessionId: "owned-session",
              params: { context: { id: 42, auxData: { isDefault: true } } },
            });
            reply({});
          } else if (request.method === "Runtime.evaluate") {
            if (
              stage === "probe" ||
              request.params.expression === "private expression sentinel"
            ) {
              waiting.resolve();
              return;
            }
            reply({ result: { value: true } });
          } else if (request.method === "Network.enable") reply({});
          else assert.fail("Unexpected CDP command " + request.method);
        });
      }
      close() {
        closed = true;
      }
    };
    let view;
    async function ownedDeadlineCaller() {
      view = await webview(workbench, directory);
      return await view.evaluate("private expression sentinel");
    }
    const method =
      stage === "discovery" ? "Runtime.enable" : "Runtime.evaluate";
    let settled = false;
    const operation = ownedDeadlineCaller();
    const markSettled = () => {
      settled = true;
    };
    void operation.then(markSettled, markSettled);
    const failed = assert.rejects(operation, (error) => {
      assert.equal(
        error.message,
        `CDP deadline: ${method} (${phase}, ${surface})`,
      );
      assert.match(error.stack, /ownedDeadlineCaller/);
      assert.equal(error.stack.includes("private expression sentinel"), false);
      return true;
    });
    await waiting.promise;
    t.mock.timers.tick(9999);
    for (let at = 0; at < 10; at++) await Promise.resolve();
    assert.equal(
      settled,
      false,
      "The command must retain its full ten-second budget",
    );
    assert.equal(closed, false);
    t.mock.timers.tick(1);
    for (let at = 0; at < 10; at++) await Promise.resolve();
    t.mock.timers.tick(100);
    try {
      await failed;
    } finally {
      view?.close();
    }
    assert.equal(closed, true);
    assert.equal(
      calls.filter((call) => call.method === method).length,
      stage === "interaction" ? 2 : 1,
    );
  }
});

test("CDP read waits rediscover destroyed frames without replaying actions or hiding other errors", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "catalog-cdp-unit-")),
    originalSocket = globalThis.WebSocket,
    originalDirectory = process.env.CATALOG_VSCODE_DEBUG_DIR;
  process.env.CATALOG_VSCODE_DEBUG_DIR = root;
  await writeFile(join(root, "DevToolsActivePort"), "1234\n/owned-unit\n");
  t.after(async () => {
    globalThis.WebSocket = originalSocket;
    if (originalDirectory === undefined)
      delete process.env.CATALOG_VSCODE_DEBUG_DIR;
    else process.env.CATALOG_VSCODE_DEBUG_DIR = originalDirectory;
    await rm(root, { recursive: true, force: true });
  });
  let probes = 0,
    mutations = 0,
    invalidReads = 0,
    presses = 0,
    releases = 0,
    frame = 1,
    staleError,
    probeError,
    attachError = "No target with given id found",
    vanished = 0;
  globalThis.WebSocket = class extends EventTarget {
    constructor(url) {
      super();
      assert.equal(url, "ws://127.0.0.1:1234/owned-unit");
      queueMicrotask(() => this.dispatchEvent(new Event("open")));
    }
    emit(value) {
      this.dispatchEvent(
        new MessageEvent("message", { data: JSON.stringify(value) }),
      );
    }
    context() {
      this.emit({
        method: "Runtime.executionContextCreated",
        sessionId: "owned-session",
        params: { context: { id: frame, auxData: { isDefault: true } } },
      });
    }
    send(encoded) {
      const request = JSON.parse(encoded),
        reply = (result) => this.emit({ id: request.id, result }),
        fail = (message) => this.emit({ id: request.id, error: { message } });
      queueMicrotask(() => {
        switch (request.method) {
          case "Target.getTargets":
            reply({
              targetInfos: [
                { type: "iframe", targetId: "vanished" },
                { type: "iframe", targetId: "owned" },
              ],
            });
            break;
          case "Target.attachToTarget":
            if (request.params.targetId === "vanished") {
              vanished++;
              fail(attachError);
              break;
            }
            reply({ sessionId: "owned-session" });
            break;
          case "Runtime.enable":
            this.context();
            reply({});
            break;
          case "Runtime.evaluate": {
            if (probeError) {
              fail(probeError);
              break;
            }
            const expression = request.params.expression;
            if (expression === "owned ready") {
              probes++;
              if (probes === 1) {
                frame++;
                this.context();
                fail(staleError);
              } else {
                assert.equal(request.params.contextId, frame);
                reply({ result: { value: true } });
              }
            } else if (expression === "owned mutation") {
              mutations++;
              fail("Execution context was destroyed.");
            } else if (expression === "owned invalid read") {
              invalidReads++;
              fail("Invalid parameters");
            } else {
              reply({
                result: {
                  value: expression.includes("return {x:")
                    ? { x: 10, y: 10 }
                    : true,
                },
              });
            }
            break;
          }
          case "Network.enable":
            reply({});
            break;
          case "Input.dispatchMouseEvent":
            if (request.params.type === "mousePressed") {
              presses++;
              fail("Execution context was destroyed.");
            } else {
              if (request.params.type === "mouseReleased") releases++;
              reply({});
            }
            break;
          default:
            assert.fail("Unexpected CDP method: " + request.method);
        }
      });
    }
    close() {}
  };
  const view = await webview();
  try {
    assert.equal(
      vanished,
      1,
      "A vanished discovery candidate cannot prevent attachment to the current frame",
    );
    for (staleError of [
      "Execution context was destroyed.",
      "Cannot find context with specified id",
      "Session with given id not found.",
    ]) {
      probes = 0;
      assert.equal(await view.until("owned ready"), true);
      assert.equal(probes, 2);
    }
    await assert.rejects(
      view.evaluate("owned mutation"),
      /context was destroyed/,
    );
    assert.equal(mutations, 1, "Direct actions are never retried");
    await assert.rejects(
      view.until("owned invalid read"),
      /Invalid parameters/,
    );
    assert.equal(invalidReads, 1, "Other errors remain failures");
    await assert.rejects(view.click("owned element"), /context was destroyed/);
    assert.equal(presses, 1, "A possibly delivered click is never repeated");
    assert.equal(releases, 0);
  } finally {
    view.close();
  }
  attachError = "Permission denied";
  await assert.rejects(
    webview(),
    /Target.attachToTarget failed: Permission denied/,
  );
  attachError = "No target with given id found";
  probeError = "Permission denied";
  await assert.rejects(webview(), /Runtime.evaluate failed: Permission denied/);
});

test("CDP setup skips only confirmed vanished sessions and reaches the current rendered frame", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-cdp-session-unit-"));
  const original = globalThis.WebSocket;
  await writeFile(
    join(directory, "DevToolsActivePort"),
    "1234\n/owned-session-unit\n",
  );
  t.after(async () => {
    globalThis.WebSocket = original;
    await rm(directory, { recursive: true, force: true });
  });
  for (const mode of ["missing", "detached", "early-detached", "permission"]) {
    const calls = [];
    let closed = false;
    globalThis.WebSocket = class extends EventTarget {
      constructor() {
        super();
        queueMicrotask(() => this.dispatchEvent(new Event("open")));
      }
      emit(value) {
        this.dispatchEvent(
          new MessageEvent("message", { data: JSON.stringify(value) }),
        );
      }
      send(encoded) {
        const request = JSON.parse(encoded);
        calls.push(request);
        queueMicrotask(() => {
          const reply = (result) => this.emit({ id: request.id, result });
          if (request.method === "Target.getTargets") {
            reply({
              targetInfos: [
                { type: "iframe", targetId: "startup" },
                { type: "iframe", targetId: "current" },
              ],
            });
          } else if (request.method === "Target.attachToTarget") {
            reply({ sessionId: request.params.targetId });
            if (
              mode === "early-detached" &&
              request.params.targetId === "startup"
            )
              this.emit({
                method: "Target.detachedFromTarget",
                params: { sessionId: "startup" },
              });
          } else if (
            request.method === "Runtime.enable" &&
            request.sessionId === "startup"
          ) {
            if (mode === "detached")
              this.emit({
                method: "Target.detachedFromTarget",
                params: { sessionId: "startup" },
              });
            else
              this.emit({
                id: request.id,
                error: {
                  message:
                    mode === "missing"
                      ? "Session with given id not found."
                      : "Permission denied",
                },
              });
          } else if (request.method === "Runtime.enable") {
            this.emit({
              method: "Runtime.executionContextCreated",
              sessionId: "current",
              params: { context: { id: 42, auxData: { isDefault: true } } },
            });
            reply({});
          } else if (request.method === "Runtime.evaluate") {
            assert.equal(request.sessionId, "current");
            assert.equal(request.params.contextId, 42);
            if (request.params.expression === "owned mutation") {
              this.emit({
                method: "Target.detachedFromTarget",
                params: { sessionId: "current" },
              });
              return;
            }
            reply({ result: { value: true } });
          } else if (request.method === "Network.enable") reply({});
          else assert.fail("Unexpected CDP method " + request.method);
        });
      }
      close() {
        closed = true;
      }
    };
    if (mode === "permission") {
      await assert.rejects(
        webview(false, directory),
        /Runtime.enable failed: Permission denied/,
      );
      assert.equal(
        calls.some((call) => call.method === "Runtime.evaluate"),
        false,
      );
    } else {
      const view = await webview(false, directory);
      assert.equal(await view.until("owned ready"), true);
      assert.equal(
        calls.filter(
          (call) =>
            call.method === "Runtime.enable" && call.sessionId === "startup",
        ).length,
        mode === "early-detached" ? 0 : 1,
      );
      assert.equal(
        calls.some((call) => call.method.startsWith("Input.")),
        false,
      );
      await assert.rejects(
        view.evaluate("owned mutation"),
        /CDP session detached/,
      );
      await assert.rejects(
        view.evaluate("owned mutation"),
        /CDP session detached/,
      );
      assert.equal(
        calls.filter((call) => call.params.expression === "owned mutation")
          .length,
        1,
        "A possibly delivered action is never replayed or sent to a detached session",
      );
      view.close();
    }
    assert.equal(closed, true);
  }
});

test("owned-surface observer subscribes before resuming startup and rejects safe asynchronous error counts", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-cdp-observer-unit-"));
  const original = globalThis.WebSocket;
  await writeFile(
    join(directory, "DevToolsActivePort"),
    "1234\n/observer-unit\n",
  );
  t.after(async () => {
    globalThis.WebSocket = original;
    await rm(directory, { recursive: true, force: true });
  });
  for (const mode of ["clean", "failed", "short", "vanished"]) {
    const failed = ["failed", "short"].includes(mode);
    const calls = [];
    let closed = false;
    globalThis.WebSocket = class extends EventTarget {
      constructor() {
        super();
        queueMicrotask(() => this.dispatchEvent(new Event("open")));
      }
      emit(value) {
        this.dispatchEvent(
          new MessageEvent("message", { data: JSON.stringify(value) }),
        );
      }
      send(encoded) {
        const request = JSON.parse(encoded);
        calls.push(request);
        queueMicrotask(() => {
          const reply = (result) => this.emit({ id: request.id, result });
          if (request.method === "Target.setDiscoverTargets") reply({});
          else if (request.method === "Target.setAutoAttach") {
            if (!request.sessionId && request.params.autoAttach)
              this.emit({
                method: "Target.attachedToTarget",
                params: {
                  sessionId: "startup",
                  targetInfo: {
                    targetId: "startup",
                    type: "iframe",
                    url: "vscode-webview://owned/index.html?extensionId=open-agentic-catalog.open-agentic-catalog",
                  },
                },
              });
            reply({});
          } else if (request.method === "Target.getTargets")
            reply({ targetInfos: [] });
          else if (request.method === "Runtime.enable") {
            this.emit({
              method: "Runtime.executionContextCreated",
              sessionId: "startup",
              params: {
                context: {
                  id: 77,
                  auxData: { isDefault: true, frameId: "frame" },
                },
              },
            });
            reply({});
          } else if (request.method === "Network.enable") {
            if (mode === "vanished")
              this.emit({
                id: request.id,
                error: { message: "Session with given id not found." },
              });
            else reply({});
          } else if (request.method === "Runtime.runIfWaitingForDebugger") {
            assert.ok(
              calls.findIndex((c) => c.method === "Network.enable") <
                calls.indexOf(request),
            );
            if (mode === "vanished") {
              this.emit({
                id: request.id,
                error: { message: "Session with given id not found." },
              });
              return;
            }
            if (failed)
              this.emit({
                method: "Runtime.exceptionThrown",
                sessionId: "startup",
                params: {
                  exceptionDetails: {
                    executionContextId: 77,
                    text: "private transcript token sentinel",
                  },
                },
              });
            if (mode === "short")
              this.emit({
                method: "Target.detachedFromTarget",
                params: { sessionId: "startup" },
              });
            reply({});
          } else if (request.method === "Runtime.evaluate")
            reply({ result: { value: true } });
          else assert.fail("Unexpected CDP command " + request.method);
        });
      }
      close() {
        closed = true;
      }
    };
    const monitor = await webview("observer", directory);
    if (failed)
      await assert.rejects(monitor.finish(), (error) => {
        assert.match(error.message, /Unexpected owned renderer errors/u);
        assert.equal(error.message.includes("private"), false);
        return true;
      });
    else await monitor.finish();
    assert.equal(closed, true);
    assert.deepEqual(monitor.errorCounts(), failed ? { exception: 1 } : {});
  }
});
