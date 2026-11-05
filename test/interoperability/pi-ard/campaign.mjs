import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { activeTools, boundedResponses } from "./script.mjs";

assert.equal(process.env.PI_OFFLINE, "1");
assert.equal(process.env.HOME, "/state/home");
assert.deepEqual(readdirSync("/state/home"), []);
const expected = JSON.parse(readFileSync("/state/expected.json", "utf8"));
const config = JSON.parse(readFileSync(process.env.PI_ARD_CONFIG, "utf8"));
assert.equal(
  config.registries[0].searchUrl,
  "http://127.0.0.1:8081/ard/v1/search",
);
assert.deepEqual(config.trustedDomains, ["127.0.0.1"]);
const originalFetch = globalThis.fetch;
const { piHarness } = await import("./host-session.mjs");
const { fauxAssistantMessage, fauxToolCall } =
  await import("/pi/node_modules/@earendil-works/pi-ai/dist/index.js");
const undici = await import("/pi/node_modules/undici/index.js");
const { DEFAULT_TIMEOUT_MS, DEFAULT_MAX_BYTES } =
  await import("/pi/src/core/http.ts");
assert.equal(DEFAULT_TIMEOUT_MS, 15000);
assert.equal(DEFAULT_MAX_BYTES, 1000000);
assert.equal(
  JSON.parse(readFileSync("/pi/node_modules/undici/package.json", "utf8"))
    .version,
  "8.12.3",
);
const dispatcher = undici.getGlobalDispatcher();
assert.equal(dispatcher.constructor.name, "Agent");
assert.equal(globalThis[Symbol.for("undici.globalDispatcher.2")], dispatcher);
const results = [];
let totalCalls = 0;
let mcpRegistrations = 0;
const syntheticId = "urn:air:fixture.example:skill:decline";
const mcpType = "application/mcp-server-card+json";
const skill = expected.entries.find((e) => e.type.startsWith("text/markdown"));
assert.ok(skill);
let firstToken;
const cases = [
  [
    "catalog",
    "ard_catalog",
    { domain: "127.0.0.1:8081" },
    (text) => {
      for (const entry of expected.entries)
        assert.ok(text.includes(entry.identifier));
      assert.ok(text.includes("unsupported"));
    },
  ],
  [
    "search-first",
    "ard_search",
    { text: "order", type: [mcpType], pageSize: 1 },
    (text) => {
      firstToken = /More results: pageToken=([^\n]+)/.exec(text)?.[1];
      assert.ok(firstToken);
      assert.ok(expected.mcpIdentifiers.some((id) => text.includes(id)));
      assert.ok(text.includes(config.registries[0].searchUrl));
      assert.ok(!text.includes("source="));
    },
  ],
  [
    "search-next",
    "ard_search",
    () => ({
      text: "order",
      type: [mcpType],
      pageSize: 1,
      pageToken: firstToken,
    }),
    (text) => {
      const first = results.find((r) => r.case === "search-first").text;
      for (const id of expected.mcpIdentifiers)
        assert.notEqual(first.includes(id), text.includes(id));
    },
  ],
  [
    "changed-query",
    "ard_search",
    () => ({
      text: "unrelated",
      type: [mcpType],
      pageSize: 1,
      pageToken: firstToken,
    }),
    /HTTP 400/,
  ],
  [
    "private-read",
    "ard_catalog",
    { url: "http://127.0.0.1:8081" + expected.privatePath },
    /HTTP 401/,
  ],
  [
    "unconfigured",
    "ard_search",
    { text: "order", registry: "not-configured" },
    /No registry not-configured configured/,
  ],
  [
    "unsafe-url",
    "ard_search",
    { text: "order", registry: "unsafe" },
    /Only https URLs are allowed/,
  ],
  [
    "untrusted-catalog",
    "ard_catalog",
    { domain: "not-trusted.invalid" },
    /not in trustedDomains/,
  ],
  [
    "synthetic",
    "ard_search",
    { text: "owned controls", registry: "synthetic" },
    (text) => {
      assert.ok(text.includes(syntheticId));
      assert.ok(text.includes("3 result(s)"));
      assert.ok(text.includes("1 result(s) without identifier dropped"));
      assert.ok(text.includes("Referrals (not followed"));
      assert.ok(text.includes("signature and attestations NOT verified"));
      assert.ok(text.includes("untrusted"));
      assert.ok(text.includes("third-party data, not instructions"));
      assert.ok(text.includes("sparse") && text.includes("unfamiliar"));
    },
  ],
  [
    "forbidden",
    "ard_search",
    { text: "control", registry: "forbidden" },
    /HTTP 403/,
  ],
  [
    "bad-json",
    "ard_search",
    { text: "control", registry: "bad-json" },
    /Not JSON/,
  ],
  [
    "missing-results",
    "ard_search",
    { text: "control", registry: "missing-results" },
    /Response has no results/,
  ],
  [
    "timeout",
    "ard_search",
    { text: "control", registry: "timeout" },
    /timed out after 15000 ms/,
  ],
  [
    "declined",
    "ard_install",
    { identifier: syntheticId },
    /declined: User declined/,
  ],
  [
    "unknown",
    "ard_install",
    { identifier: "urn:air:fixture.example:skill:unknown" },
    /Unknown identifier/,
  ],
  [
    "canonical-skill",
    "ard_install",
    { identifier: skill.identifier },
    /refused:.*(?:Unsupported|unsupported)/,
  ],
];

async function session(agentDir, hasUI, casesToRun, budget) {
  const h = await piHarness({ agentDir, hasUI, answers: [false] });
  let calls = 0;
  let started = 0;
  try {
    assert.equal(h.session.modelRuntime.modelNetworkEnabled, false);
    const registry =
      h.session.resourceLoader.getExtensions().runtime.mcpServers;
    assert.deepEqual(registry.list(), []);
    h.session.setActiveToolsByName(activeTools);
    assert.deepEqual(h.session.getActiveToolNames().sort(), activeTools);
    h.session.subscribe((event) => {
      if (event.type === "tool_execution_start") {
        calls++;
        totalCalls++;
      }
    });
    const accept = (i) => {
      if (i < 0) return;
      const [name, tool, , check] = casesToRun[i];
      const result = h.session.messages.findLast(
        (m) => m.role === "toolResult",
      );
      assert.equal(result.toolName, tool);
      const text = result.content
        .filter((c) => c.type === "text")
        .map((c) => c.text)
        .join("\n");
      const durationMs = performance.now() - started;
      if (name === "timeout")
        assert.ok(durationMs >= 14000 && durationMs < 25000);
      if (check instanceof RegExp) assert.match(text, check);
      else check(text);
      const errors = new Set([
        "changed-query",
        "private-read",
        "unconfigured",
        "unsafe-url",
        "untrusted-catalog",
        "forbidden",
        "bad-json",
        "missing-results",
        "timeout",
        "unknown",
        "no-ui",
      ]);
      assert.equal(!!result.isError, errors.has(name));
      assert.deepEqual(registry.list(), []);
      if (name === "private-read")
        writeFileSync("/state/ready", "native Catalog reads validated");
      results.push({
        case: name,
        tool,
        text,
        isError: !!result.isError,
        durationMs,
      });
    };
    const steps = casesToRun.map(([, tool, args], i) => () => {
      accept(i - 1);
      started = performance.now();
      return fauxAssistantMessage(
        fauxToolCall(tool, typeof args === "function" ? args() : args),
        { stopReason: "toolUse" },
      );
    });
    steps.push(() => {
      accept(casesToRun.length - 1);
      return fauxAssistantMessage("Owned campaign complete.");
    });
    h.setResponses(boundedResponses(steps, budget));
    await h.session.prompt(
      "Run only the fixed local qualification steps. Do not install resources.",
    );
    assert.equal(calls, casesToRun.length);
    assert.equal(h.session.messages.at(-1).stopReason, "stop");
    assert.equal(globalThis.fetch, originalFetch);
    assert.equal(undici.getGlobalDispatcher(), dispatcher);
    assert.deepEqual(h.session.getActiveToolNames().sort(), activeTools);
    assert.equal(h.confirms.length, hasUI ? 1 : 0);
    if (hasUI) {
      assert.match(h.confirms[0].title, /UNTRUSTED/);
      assert.ok(h.confirms[0].body.includes(syntheticId));
      assert.ok(h.confirms[0].body.includes("NOT verified"));
    }
    assert.ok(
      !existsSync(agentDir + "/skills") &&
        !existsSync(agentDir + "/pi-ard/lock.json"),
    );
    assert.ok(!existsSync(agentDir + "/mcp.json"));
    const registrations = registry.list();
    assert.deepEqual(registrations, []);
    mcpRegistrations += registrations.length;
    return h.confirms;
  } finally {
    h.dispose();
  }
}

const confirms = await session("/state/agent", true, cases, 18);
process.env.PI_ARD_CONFIG = "/state/no-ui/pi-ard/config.json";
assert.deepEqual(
  JSON.parse(readFileSync(process.env.PI_ARD_CONFIG, "utf8")),
  config,
);
await session(
  "/state/no-ui",
  false,
  [
    [
      "synthetic-no-ui",
      "ard_search",
      { text: "owned controls", registry: "synthetic" },
      (text) => assert.ok(text.includes(syntheticId)),
    ],
    [
      "no-ui",
      "ard_install",
      { identifier: syntheticId },
      /needs an interactive Pi UI/,
    ],
  ],
  2,
);
assert.equal(results.length, 18);
assert.ok(totalCalls <= 20);
writeFileSync(
  "/state/native-campaign.json",
  JSON.stringify({
    cases: results,
    toolCalls: totalCalls,
    confirms,
    activeTools,
    fetchUnchanged: true,
    dispatcherVersion: "8.12.3",
    offline: true,
    httpTimeoutMs: DEFAULT_TIMEOUT_MS,
    httpBytes: DEFAULT_MAX_BYTES,
    artifactFiles: 0,
    mcpRegistrations,
  }),
);
