import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { activeTools, boundedResponses } from "./script.mjs";

const mode = process.argv[2];
const configFile = "/state/agent/pi-ard/config.json";
const expected = {
  registries: [
    { name: "owned", searchUrl: "http://127.0.0.1:8080/ard/v1/search" },
  ],
  trustedDomains: [],
};
if (!existsSync(configFile)) {
  writeFileSync(
    "/state/missing-refused",
    "no native import or default registry",
  );
  throw new Error("owned Pi configuration is required");
}
assert.deepEqual(JSON.parse(readFileSync(configFile, "utf8")), expected);
assert.equal(process.env.PI_OFFLINE, "1");
assert.equal(process.env.PI_ARD_CONFIG, configFile);
assert.equal(process.env.HOME, "/state/home");
assert.deepEqual(readdirSync("/state/home"), []);
const nativeFetch = globalThis.fetch;
const control = "http://127.0.0.1:" + process.env.PI_CONTROL_PORT + "/ready";
assert.equal(await (await fetch(control)).text(), "ready");
const symbols = [
  Symbol.for("undici.globalDispatcher.1"),
  Symbol.for("undici.globalDispatcher.2"),
];
const dispatchers = symbols.map((key) => globalThis[key]);
const { piHarness } = await import("./host-session.mjs");
const { fauxAssistantMessage, fauxToolCall } =
  await import("/pi/node_modules/@earendil-works/pi-ai/dist/index.js");
const { DEFAULT_TIMEOUT_MS, DEFAULT_MAX_BYTES } =
  await import("/pi/src/core/http.ts");
assert.equal(DEFAULT_TIMEOUT_MS, 15000);
assert.equal(DEFAULT_MAX_BYTES, 1000000);
// The unchanged Undici 8 module initializes both global dispatcher symbols on import.
assert.equal(dispatchers[1], undefined);
const importedDispatchers = symbols.map((key) => globalThis[key]);
assert.equal(importedDispatchers[0]?.constructor.name, "Dispatcher1Wrapper");
assert.equal(importedDispatchers[1]?.constructor.name, "Agent");
assert.notEqual(importedDispatchers[0], dispatchers[0]);
const nativeUndici = await import("/pi/node_modules/undici/index.js");
const undiciVersion = JSON.parse(
  readFileSync("/pi/node_modules/undici/package.json", "utf8"),
).version;
assert.equal(undiciVersion, "8.12.3");
assert.equal(nativeUndici.getGlobalDispatcher(), importedDispatchers[1]);
const h = await piHarness({ agentDir: "/state/agent" });
try {
  h.session.setActiveToolsByName(activeTools);
  assert.deepEqual(h.session.getActiveToolNames().sort(), activeTools);
  assert.equal(h.session.modelRuntime.modelNetworkEnabled, false);
  assert.equal(globalThis.fetch, nativeFetch);
  assert.ok(
    symbols.every((key, i) => globalThis[key] === importedDispatchers[i]),
  );
  await h.session.prompt("/ard");
  assert.equal(h.notifications.length, 1);
  assert.ok(h.notifications[0].includes(expected.registries[0].searchUrl));
  assert.ok(!h.notifications[0].includes("huggingface"));
  let executed = 0;
  h.session.subscribe((event) => {
    if (event.type === "tool_execution_start") executed++;
  });
  writeFileSync("/state/ready", "native session ready");
  if (mode === "failure") throw new Error("owned native failure control");
  if (mode === "output") {
    process.stdout.write(Buffer.alloc(1048577, 65));
    await new Promise((resolve) => setInterval(resolve, 60000));
  }
  if (mode === "timeout" || mode === "interruption")
    await new Promise((resolve) => setInterval(resolve, 60000));
  if (mode === "tool-budget" || mode === "page-budget") {
    const count = mode === "tool-budget" ? 2 : 4;
    const name = mode === "tool-budget" ? "ard_catalog" : "ard_search";
    const steps = Array.from({ length: count }, (_, i) =>
      fauxAssistantMessage(
        fauxToolCall(name, {
          registry: "not-configured",
          text: "owned calibration",
          ...(i ? { pageToken: "calibration-only" } : {}),
        }),
        { stopReason: "toolUse" },
      ),
    );
    h.setResponses(boundedResponses(steps, mode === "tool-budget" ? 1 : 20));
    await h.session.prompt("Run the bounded scripted control.");
    assert.equal(executed, mode === "tool-budget" ? 1 : 3);
    const last = h.session.messages.at(-1);
    assert.ok(
      last.errorMessage?.includes(
        mode === "tool-budget" ? "tool budget" : "page budget",
      ),
    );
    writeFileSync(
      "/state/budget.json",
      JSON.stringify({ blocked: true, executed }),
    );
  } else {
    h.setResponses(
      boundedResponses([fauxAssistantMessage("Pi startup ready.")]),
    );
    await h.session.prompt("Return the fixed startup acknowledgement.");
    assert.ok(
      h.session.messages.some(
        (m) =>
          m.role === "assistant" &&
          m.content.some((c) => c.text === "Pi startup ready."),
      ),
    );
    assert.equal(executed, 0);
    assert.deepEqual(h.session.getActiveToolNames().sort(), activeTools);
    assert.equal(h.confirms.length, 0);
    assert.ok(
      !existsSync("/state/agent/skills") &&
        !existsSync("/state/agent/pi-ard/lock.json"),
    );
    assert.equal(globalThis.fetch, nativeFetch);
    assert.ok(
      symbols.every((key, i) => globalThis[key] === importedDispatchers[i]),
    );
    assert.equal(await (await fetch(control)).text(), "ready");
    writeFileSync(
      "/state/native.json",
      JSON.stringify({
        activeTools,
        fetchUnchanged: true,
        dispatcherInitialized: true,
        dispatcherPackage: "undici",
        dispatcherVersion: undiciVersion,
        dispatcherStableAfterImport: true,
        offline: true,
        scriptedTurn: true,
        localConfig: true,
        toolCalls: executed,
        searchPages: 0,
        confirms: 0,
        artifactFiles: 0,
        httpTimeoutMs: DEFAULT_TIMEOUT_MS,
        httpBytes: DEFAULT_MAX_BYTES,
        nativeLoopbackRequests: 2,
      }),
    );
  }
} finally {
  h.dispose();
}
