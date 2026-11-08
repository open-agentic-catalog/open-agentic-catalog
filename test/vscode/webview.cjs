// Test-only CDP client for the actual installed editor's rendered webview.
// Native WebSocket avoids a second browser download or a product UI dependency.
const { readFile, writeFile } = require("node:fs/promises");
const { join } = require("node:path");
const assert = require("node:assert/strict");
const { rendererErrors } = require("./renderer-errors.cjs");
const pause = () => new Promise((resolve) => setTimeout(resolve, 100));
const contextGone = (error) =>
  error.message === "CDP session detached" ||
  /^CDP Runtime\.evaluate failed: (?:Cannot find context with specified id|Session with given id not found|Execution context was destroyed)\.?$/u.test(
    error.message,
  );
const setupGone = (error) =>
  contextGone(error) ||
  /^CDP (?:Runtime\.enable|Network\.enable|Runtime\.runIfWaitingForDebugger|Target\.setAutoAttach) failed: Session with given id not found\.?$/u.test(
    error.message,
  );
const ownedTarget = (info) => {
  try {
    return (
      new URL(info.url).searchParams.get("extensionId") ===
      "open-agentic-catalog.open-agentic-catalog"
    );
  } catch {
    return false;
  }
};
exports.webview = async (
  workbench = false,
  directory = process.env.CATALOG_VSCODE_DEBUG_DIR,
) => {
  const [port, path] = (
    await readFile(join(directory, "DevToolsActivePort"), "utf8")
  )
    .trim()
    .split("\n");
  const socket = new WebSocket(`ws://127.0.0.1:${port}${path}`),
    waiting = new Map(),
    contexts = [],
    detachedSessions = new Set();
  const errors = rendererErrors();
  const observer = workbench === "observer";
  const background = new Set();
  const attachedIds = new Set();
  const sessions = new Map();
  let observationFailure = false;
  let closing = false;
  let sequence = 0;
  let phase = "discovery";
  const surface =
    workbench === true
      ? "workbench"
      : workbench === "guide"
        ? "guide"
        : workbench === "browser"
          ? "browser-login"
          : "catalog";
  await new Promise((resolve, reject) => {
    socket.addEventListener("open", resolve, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  socket.addEventListener("message", (event) => {
    const m = JSON.parse(event.data);
    errors.observe(m);
    if (m.id) {
      const pending = waiting.get(m.id);
      if (pending) {
        waiting.delete(m.id);
        clearTimeout(pending.timer);
        if (m.error) {
          pending.failure.message += `: ${m.error.message}`;
          pending.reject(pending.failure);
        } else pending.resolve(m.result);
      }
    } else if (m.method === "Target.attachedToTarget" && observer) {
      attachedIds.add(m.params.targetInfo.targetId);
      sessions.set(m.params.sessionId, m.params.targetInfo.targetId);
      if (ownedTarget(m.params.targetInfo))
        errors.ownSession(m.params.sessionId);
      const operation = initialize(m.params.sessionId).catch(() => {
        observationFailure = true;
      });
      background.add(operation);
      void operation.finally(() => background.delete(operation));
    } else if (m.method === "Target.targetInfoChanged" && observer) {
      if (ownedTarget(m.params.targetInfo))
        for (const [session, id] of sessions)
          if (id === m.params.targetInfo.targetId) errors.ownSession(session);
    } else if (m.method === "Target.detachedFromTarget") {
      const sessionId = m.params.sessionId;
      detachedSessions.add(sessionId);
      for (const [id, pending] of waiting) {
        if (pending.sessionId !== sessionId) continue;
        waiting.delete(id);
        clearTimeout(pending.timer);
        pending.reject(new Error("CDP session detached"));
      }
      for (let at = contexts.length - 1; at >= 0; at--)
        if (contexts[at].sessionId === sessionId) contexts.splice(at, 1);
    } else if (
      m.method === "Runtime.executionContextCreated" &&
      m.params.context.auxData?.isDefault
    )
      contexts.push({
        sessionId: m.sessionId,
        id: m.params.context.id,
        frameId: m.params.context.auxData.frameId,
      });
  });
  const rpc = (method, params = {}, sessionId) =>
    new Promise((resolve, reject) => {
      if (sessionId && detachedSessions.has(sessionId)) {
        reject(new Error("CDP session detached"));
        return;
      }
      const id = ++sequence,
        failure = new Error(`CDP ${method} failed`),
        timer = setTimeout(() => {
          waiting.delete(id);
          failure.message = `CDP deadline: ${method} (${phase}, ${surface})`;
          reject(failure);
        }, 10000);
      waiting.set(id, {
        resolve,
        reject,
        timer,
        sessionId,
        failure,
      });
      socket.send(
        JSON.stringify({
          id,
          method,
          params,
          ...(sessionId ? { sessionId } : {}),
        }),
      );
    });
  let target;
  const ownedExpression =
    "typeof document !== 'undefined' && (document.getElementById('catalog') !== null || document.querySelector('h1')?.textContent === 'VS Code Catalog client')";
  const initialize = async (sessionId) => {
    let failure;
    try {
      await rpc("Runtime.enable", {}, sessionId);
      await rpc("Network.enable", {}, sessionId);
      if (observer && !closing)
        await rpc(
          "Target.setAutoAttach",
          {
            autoAttach: true,
            waitForDebuggerOnStart: true,
            flatten: true,
            filter: [{ type: "page" }, { type: "iframe" }, { exclude: true }],
          },
          sessionId,
        );
    } catch (error) {
      if (!setupGone(error)) failure = error;
    } finally {
      if (observer && !detachedSessions.has(sessionId)) {
        try {
          await rpc("Runtime.runIfWaitingForDebugger", {}, sessionId);
        } catch (error) {
          if (!setupGone(error)) failure ??= error;
        }
      }
    }
    if (failure) throw failure;
  };
  try {
    async function attach() {
      const targets = (await rpc("Target.getTargets")).targetInfos;
      for (const t of targets.filter((t) =>
        ["page", "iframe"].includes(t.type),
      )) {
        if (attachedIds.has(t.targetId)) continue;
        attachedIds.add(t.targetId);
        let attached;
        try {
          attached = await rpc("Target.attachToTarget", {
            targetId: t.targetId,
            flatten: true,
          });
          sessions.set(attached.sessionId, t.targetId);
          if (ownedTarget(t)) errors.ownSession(attached.sessionId);
        } catch (error) {
          // A hidden editor frame may disappear after Target.getTargets.
          // Discovery is read-only; no user action has been delivered.
          if (
            error.message ===
            "CDP Target.attachToTarget failed: No target with given id found"
          )
            continue;
          throw error;
        }
        try {
          await initialize(attached.sessionId);
        } catch (error) {
          // A startup frame can disappear after attachment as well. Continue
          // only confirmed session loss during this read-only discovery step;
          // deadlines, permission failures and all actions still fail.
          if (
            !/^(?:CDP Runtime\.enable failed: Session with given id not found\.?|CDP session detached)$/u.test(
              error.message,
            )
          )
            throw error;
        }
      }
    }
    const discover = async () => {
      await attach();
      for (const context of contexts.toReversed()) {
        try {
          const result = await rpc(
            "Runtime.evaluate",
            {
              expression: observer
                ? ownedExpression
                : workbench === "browser"
                  ? "typeof document !== 'undefined' && document.getElementById('owned-browser-login') !== null"
                  : workbench === true
                    ? "typeof document !== 'undefined' && document.querySelector('.monaco-workbench') !== null"
                    : workbench === "guide"
                      ? "typeof document !== 'undefined' && document.querySelector('h1')?.textContent === 'VS Code Catalog client'"
                      : "typeof document !== 'undefined' && document.getElementById('catalog') !== null",
              contextId: context.id,
              returnByValue: true,
            },
            context.sessionId,
          );
          if (result.result.value) {
            if (observer || (workbench !== true && workbench !== "browser"))
              errors.own(context.sessionId, context.id, context.frameId);
            target = context;
            if (!observer) break;
          }
        } catch (error) {
          // Only confirmed frame loss permits another read-only discovery.
          if (!contextGone(error)) throw error;
        }
      }
    };
    if (observer) {
      await rpc("Target.setDiscoverTargets", { discover: true });
      await rpc("Target.setAutoAttach", {
        autoAttach: true,
        waitForDebuggerOnStart: true,
        flatten: true,
        filter: [{ type: "page" }, { type: "iframe" }, { exclude: true }],
      });
    }
    await attach();
    if (observer) {
      let stopped = false,
        timer,
        polling;
      const poll = async () => {
        try {
          await discover();
        } catch {
          observationFailure = true;
        }
        if (!stopped)
          timer = setTimeout(() => {
            polling = poll();
          }, 100);
      };
      polling = poll();
      return {
        errorCounts: () => errors.counts(),
        async finish(expectedErrors = {}) {
          closing = true;
          stopped = true;
          clearTimeout(timer);
          try {
            await polling;
            await discover();
            await rpc("Target.setAutoAttach", {
              autoAttach: false,
              waitForDebuggerOnStart: false,
              flatten: true,
            });
            for (const session of sessions.keys())
              if (!detachedSessions.has(session)) {
                try {
                  await rpc(
                    "Target.setAutoAttach",
                    {
                      autoAttach: false,
                      waitForDebuggerOnStart: false,
                      flatten: true,
                    },
                    session,
                  );
                } catch (error) {
                  if (!setupGone(error)) throw error;
                }
              }
            while (background.size) await Promise.all([...background]);
            assert.equal(
              observationFailure,
              false,
              "Owned renderer observation failed",
            );
            assert.deepEqual(
              errors.counts(),
              expectedErrors,
              "Unexpected owned renderer errors",
            );
          } finally {
            socket.close();
          }
        },
      };
    }
    const deadline = Date.now() + 10000;
    while (!target && Date.now() < deadline) {
      await discover();
      if (!target) await pause();
    }
    assert.ok(target, "Installed Catalog webview did not render");
    phase = "interaction";
    const evaluate = async (expression) => {
      const result = await rpc(
        "Runtime.evaluate",
        {
          expression,
          contextId: target.id,
          returnByValue: true,
          awaitPromise: true,
        },
        target.sessionId,
      );
      if (result.exceptionDetails)
        throw new Error("Webview assertion script failed");
      return result.result.value;
    };
    const until = async (expression) => {
      const deadline = Date.now() + 10000;
      while (Date.now() < deadline) {
        try {
          if (!target) await discover();
          if (target) {
            const result = await evaluate(expression);
            if (result) return result;
          }
        } catch (error) {
          // Markdown preview can replace its startup frame. Retry only a
          // read-only wait predicate, never a click or other side effect.
          if (!contextGone(error)) throw error;
          target = undefined;
        }
        await pause();
      }
      throw new Error(`Rendered Catalog deadline: ${expression}`);
    };
    return {
      evaluate,
      until,
      accessibility: async () => {
        await rpc("Accessibility.enable", {}, target.sessionId);
        return (
          await rpc(
            "Accessibility.getFullAXTree",
            { frameId: target.frameId },
            target.sessionId,
          )
        ).nodes;
      },
      navigate: (url) => rpc("Page.navigate", { url }, target.sessionId),
      screenshot: async (path) => {
        const result = await rpc(
          "Page.captureScreenshot",
          {},
          target.sessionId,
        );
        await writeFile(path, Buffer.from(result.data, "base64"));
      },
      click: async (expression, button = "left") => {
        await until(
          `(()=>{const e=${expression};if(!e)return false;const r=e.getBoundingClientRect();return r.width>0&&r.height>0&&r.x+r.width/2>=0&&r.x+r.width/2<innerWidth&&r.y+r.height/2>=0&&r.y+r.height/2<innerHeight;})()`,
        );
        const point = await evaluate(
          `(()=>{const element=${expression};if(!element)throw new Error('Missing click target');const r=element.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`,
        );
        await rpc(
          "Input.dispatchMouseEvent",
          { type: "mouseMoved", ...point, button: "none", buttons: 0 },
          target.sessionId,
        );
        await rpc(
          "Input.dispatchMouseEvent",
          {
            type: "mousePressed",
            ...point,
            button,
            buttons: button === "right" ? 2 : 1,
            clickCount: 1,
          },
          target.sessionId,
        );
        await rpc(
          "Input.dispatchMouseEvent",
          {
            type: "mouseReleased",
            ...point,
            button,
            buttons: 0,
            clickCount: 1,
          },
          target.sessionId,
        );
      },
      key: async (key) => {
        await rpc(
          "Input.dispatchKeyEvent",
          {
            type: "keyDown",
            key,
            code: key === " " ? "Space" : key,
            ...(key === "Enter" ? { text: "\r", unmodifiedText: "\r" } : {}),
            windowsVirtualKeyCode:
              {
                " ": 32,
                Tab: 9,
                Enter: 13,
                Escape: 27,
                Home: 36,
                End: 35,
                ArrowDown: 40,
                ArrowUp: 38,
              }[key] ?? 13,
          },
          target.sessionId,
        );
        await rpc(
          "Input.dispatchKeyEvent",
          { type: "keyUp", key, code: key === " " ? "Space" : key },
          target.sessionId,
        );
      },
      errorCounts: () => errors.counts(),
      assertHealthy: async () => {
        await evaluate("0");
        assert.deepEqual(
          errors.counts(),
          {},
          "Unexpected owned renderer errors",
        );
      },
      close: (expectedErrors = {}) => {
        socket.close();
        assert.deepEqual(
          errors.counts(),
          expectedErrors,
          "Unexpected owned renderer errors",
        );
      },
    };
  } catch (error) {
    socket.close();
    throw error;
  }
};
