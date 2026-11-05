import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";
const hooks = registerHooks({
  resolve(specifier, context, next) {
    if (specifier === "vscode" || specifier === "undici")
      return { url: `a2a-unit:${specifier}`, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === "a2a-unit:vscode")
      return {
        format: "module",
        source:
          "export const {window,workspace,Uri,ViewColumn,env,commands}=globalThis.a2aHostFixture.vscode;",
        shortCircuit: true,
      };
    if (url === "a2a-unit:undici")
      return {
        format: "module",
        source:
          "export class Agent {async destroy(){}};export class ProxyAgent extends Agent{};export const fetch=(...args)=>globalThis.a2aHostFixture.fetch(...args);",
        shortCircuit: true,
      };
    return next(url, context);
  },
});
const bytes = (value) => Buffer.from(JSON.stringify(value));
const card = {
  name: "Local",
  description: "Text",
  version: "1",
  supportedInterfaces: [
    {
      url: "https://agent.example/rpc",
      protocolBinding: "JSONRPC",
      protocolVersion: "1.0",
    },
  ],
  capabilities: { streaming: true },
  defaultInputModes: ["text/plain"],
  defaultOutputModes: ["text/plain"],
  securitySchemes: { bearer: { httpAuthSecurityScheme: { scheme: "Bearer" } } },
  securityRequirements: [{ schemes: { bearer: { list: [] } } }],
};
let state;
globalThis.a2aHostFixture = {
  vscode: {
    commands: {
      executeCommand: async (name, ...args) => {
        state.commands.push(name);
        state.commandArgs = args;
      },
    },
    env: {},
    ViewColumn: { Active: 1 },
    Uri: {
      joinPath: (base, ...parts) => ({
        toString: () => base.toString() + "/" + parts.join("/"),
      }),
    },
    workspace: {
      isTrusted: true,
      workspaceFolders: [],
      getConfiguration: () => ({
        inspect: () => state.inspect,
        get: () => state.settings,
      }),
      onDidChangeConfiguration: (cb) => {
        state.config = cb;
        return { dispose() {} };
      },
    },
    window: {
      showInformationMessage: async (message) => {
        state.information = message;
      },
      showQuickPick: async (options) => options[0],
      showInputBox: async () => "agent-target",
      showWarningMessage: async (message, options, yes) =>
        state.confirm(message, options, yes),
      showErrorMessage: async (error) => {
        state.errors.push(error);
      },
      createWebviewPanel: () => {
        const panel = {
          webview: {
            asWebviewUri: (uri) => uri,
            cspSource: "vscode-resource:",
            html: "",
            postMessage: async (m) => {
              state.projections.push(m);
            },
            onDidReceiveMessage: (cb) => {
              state.message = cb;
            },
          },
          onDidDispose: (cb) => {
            panel.closed = cb;
          },
          dispose: () => {
            panel.closed?.();
          },
        };
        return panel;
      },
    },
  },
  fetch: async (url, init) => {
    state.requests.push({ url, init });
    const request = JSON.parse(init.body);
    return new Response(
      JSON.stringify({
        jsonrpc: "2.0",
        id: request.id,
        result: {
          message: {
            messageId: crypto.randomUUID(),
            contextId: "ctx",
            role: "ROLE_AGENT",
            parts: [{ text: "<script>private reply</script>" }],
          },
        },
      }),
      { headers: { "content-type": "application/json" } },
    );
  },
};
const { CatalogA2A } = await import(
  new URL("a2a-host.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);
function setup() {
  state = {
    epoch: 0,
    inspect: {},
    settings: {},
    errors: [],
    requests: [],
    projections: [],
    confirm: async (m, o, yes) => yes,
    bytes: bytes(card),
    reads: 0,
    commands: [],
  };
  const original = state;
  const session = {
    connection: () => ({
      caFile: "/must-not-read-Catalog-CA.pem",
      timeoutMs: 30000,
      allowLoopbackHTTP: false,
    }),
    guard: () => {
      const epoch = state.epoch;
      return () => {
        if (state.epoch !== epoch)
          throw Error("stale private dependency cause");
      };
    },
    onChange: (cb) => {
      state.sessionChanged = cb;
      return { dispose() {} };
    },
    cancellation: () => new AbortController().signal,
  };
  const browser = {
    state: { detail: { item: { kind: "agent" } } },
    consumer: () => async () => {
      state.reads++;
      return { bytes: state.bytes };
    },
  };
  const a2a = new CatalogA2A(
    { extensionUri: { toString: () => "file:///extension" } },
    browser,
    session,
    async (s) => {
      state.exported = s;
    },
  );
  return { a2a, browser, original };
}
test("A2A native host: separate target secret, inert opening, closed bridge and explicit transcript export", async () => {
  const { a2a } = setup();
  try {
    await a2a.select(0);
    assert.equal(state.requests.length, 0);
    assert.equal(state.reads, 1);
    const ready = state.projections.at(-1);
    assert.doesNotMatch(
      JSON.stringify(ready),
      /agent-target|Authorization|must-not-read/u,
    );
    await state.message({
      type: "send",
      revision: ready.revision,
      prompt: "hello",
      stream: false,
      extra: "bad",
    });
    assert.equal(state.requests.length, 0);
    assert.ok(state.errors[0].includes("selection_changed"));
    state.confirm = async () => undefined;
    await assert.rejects(a2a.send("hello"), /send_cancelled/u);
    assert.equal(state.requests.length, 0);
    state.confirm = async (m, o, yes) => yes;
    await a2a.send("only this prompt");
    assert.equal(state.reads, 2);
    assert.equal(state.requests.length, 1);
    assert.equal(
      state.requests[0].init.headers.Authorization,
      "Bearer agent-target",
    );
    assert.equal(state.requests[0].url, "https://agent.example/rpc");
    assert.equal(a2a.conversation.state.status, "completed");
    const current = state.projections.at(-1);
    await state.message({ type: "export", revision: current.revision });
    assert.match(state.exported, /only this prompt/u);
    assert.doesNotMatch(state.exported, /agent-target|Authorization/u);
    await state.message({ type: "disconnect", revision: current.revision });
    assert.equal(a2a.conversation.state.connected, false);
    assert.equal(state.requests.length, 1);
    state.sessionChanged();
    assert.equal(a2a.conversation, undefined);
  } finally {
    a2a.dispose();
  }
});
test("A2A host refuses untrusted/workspace configuration, changed Cards and stale credential/consent prompts", async () => {
  const { a2a, browser } = setup();
  try {
    globalThis.a2aHostFixture.vscode.workspace.isTrusted = false;
    await assert.rejects(a2a.select(0), /trusted_workspace/u);
    assert.equal(state.reads, 0);
    globalThis.a2aHostFixture.vscode.workspace.isTrusted = true;
    state.inspect = { workspaceValue: { allowLoopbackHTTP: true } };
    await assert.rejects(a2a.select(0), /workspace_settings/u);
    state.inspect = {};
    await a2a.select(0);
    state.bytes = bytes({ ...card, version: "changed" });
    await assert.rejects(a2a.send("hello"), /selection_changed/u);
    assert.equal(state.requests.length, 0);
    state.bytes = bytes(card);
    state.confirm = async (m, o, yes) => {
      state.epoch++;
      return yes;
    };
    await assert.rejects(a2a.select(0));
    assert.equal(state.requests.length, 0);
    state.confirm = async (m, o, yes) => yes;
    await a2a.select(0);
    state.config({ affectsConfiguration: () => true });
    assert.equal(a2a.conversation, undefined);
    browser.state.detail.item.kind = "skill";
    await assert.rejects(a2a.select(0), /select_agent/u);
  } finally {
    a2a.dispose();
  }
});
test("A2A credentials from a stale native prompt cannot reach another session or target", async () => {
  const { a2a } = setup();
  const window = globalThis.a2aHostFixture.vscode.window,
    previous = window.showInputBox;
  let entered, release;
  const waiting = new Promise((r) => (entered = r));
  window.showInputBox = async () => {
    entered();
    return new Promise((r) => (release = r));
  };
  try {
    const opening = a2a.select(0);
    await waiting;
    state.epoch++;
    release("must-never-send");
    await assert.rejects(opening);
    assert.equal(state.requests.length, 0);
    assert.equal(a2a.conversation, undefined);
    assert.doesNotMatch(JSON.stringify(state.projections), /must-never-send/u);
  } finally {
    window.showInputBox = previous;
    a2a.dispose();
  }
});
test("Changing A2A settings during credential entry invalidates the pending destination", async () => {
  const { a2a } = setup();
  const window = globalThis.a2aHostFixture.vscode.window,
    previous = window.showInputBox;
  window.showInputBox = async () => {
    state.config({ affectsConfiguration: () => true });
    return "stale-target-secret";
  };
  try {
    await assert.rejects(a2a.select(0), /selection_changed/u);
    assert.equal(a2a.conversation, undefined);
    assert.equal(state.requests.length, 0);
  } finally {
    window.showInputBox = previous;
    a2a.dispose();
  }
});
test("Native task read and cancellation preserve separate consent and current access checks", async () => {
  const { a2a } = setup();
  const fixture = globalThis.a2aHostFixture,
    previous = fixture.fetch;
  fixture.fetch = async (url, init) => {
    state.requests.push({ url, init });
    const request = JSON.parse(init.body);
    const task = {
      id: "t",
      contextId: "ctx",
      status: {
        state:
          request.method === "CancelTask"
            ? "TASK_STATE_CANCELED"
            : "TASK_STATE_WORKING",
      },
    };
    return new Response(
      JSON.stringify({
        jsonrpc: "2.0",
        id: request.id,
        result: request.method === "SendMessage" ? { task } : task,
      }),
      { headers: { "content-type": "application/json" } },
    );
  };
  try {
    await a2a.select(0);
    await a2a.send("work");
    const working = state.projections.at(-1);
    await state.message({ type: "read", revision: working.revision });
    assert.equal(JSON.parse(state.requests.at(-1).init.body).method, "GetTask");
    state.confirm = async () => undefined;
    await a2a.cancel();
    assert.equal(
      state.requests.length,
      2,
      "Declined remote cancellation must send nothing",
    );
    assert.equal(a2a.conversation.state.status, "working");
    state.confirm = async (message, options, yes) => {
      assert.match(message, /task t at https:\/\/agent.example\/rpc/u);
      return yes;
    };
    await state.message({
      type: "cancel",
      revision: state.projections.at(-1).revision,
    });
    assert.equal(
      JSON.parse(state.requests.at(-1).init.body).method,
      "CancelTask",
    );
    assert.equal(a2a.conversation.state.status, "canceled");
    assert.equal(
      state.reads,
      4,
      "Open, send, read and cancel each check current Card access",
    );
  } finally {
    fixture.fetch = previous;
    a2a.dispose();
  }
});
test("Cancellation consent cannot transfer to a later task in the same conversation", async () => {
  const { a2a } = setup();
  const fixture = globalThis.a2aHostFixture,
    previous = fixture.fetch;
  let taskID = "first",
    status = "TASK_STATE_WORKING",
    entered,
    release;
  const waiting = new Promise((resolve) => (entered = resolve));
  fixture.fetch = async (url, init) => {
    state.requests.push({ url, init });
    const request = JSON.parse(init.body);
    const task = {
      id: taskID,
      contextId: "ctx",
      status: {
        state: request.method === "CancelTask" ? "TASK_STATE_CANCELED" : status,
      },
    };
    return new Response(
      JSON.stringify({
        jsonrpc: "2.0",
        id: request.id,
        result: request.method === "SendMessage" ? { task } : task,
      }),
      { headers: { "content-type": "application/json" } },
    );
  };
  try {
    await a2a.select(0);
    await a2a.send("first task");
    state.confirm = async (message, options, yes) => {
      if (yes !== "Cancel remote task") return yes;
      assert.match(message, /task first at/u);
      entered();
      return new Promise((resolve) => (release = resolve));
    };
    const cancelling = a2a.cancel();
    await waiting;
    // An already queued read/send can finish while native consent is pending.
    status = "TASK_STATE_COMPLETED";
    await a2a.read();
    taskID = "second";
    status = "TASK_STATE_WORKING";
    await a2a.send("next task");
    assert.equal(a2a.conversation.state.task, "second");
    release("Cancel remote task");
    await assert.rejects(cancelling, /selection_changed/u);
    assert.equal(a2a.conversation.state.status, "working");
    assert.equal(state.requests.length, 3);
    assert.ok(
      state.requests.every(
        (request) => JSON.parse(request.init.body).method !== "CancelTask",
      ),
      "Consent for the first task must never cancel the second task",
    );
  } finally {
    release?.();
    fixture.fetch = previous;
    a2a.dispose();
  }
});
test("Drafts survive renderer recreation only in the same live conversation; stale panels cannot restore them", async () => {
  const { a2a } = setup();
  try {
    await a2a.select(0);
    const oldMessage = state.message;
    await state.message({ type: "draft", prompt: "私の下書き", stream: true });
    await state.message({ type: "ready" });
    assert.deepEqual(state.projections.at(-1).draft, {
      prompt: "私の下書き",
      stream: true,
    });
    await state.message({
      type: "draft",
      prompt: "x".repeat(16385),
      stream: false,
    });
    await state.message({ type: "ready" });
    assert.equal(state.projections.at(-1).draft.prompt, "私の下書き");
    state.confirm = async () => undefined;
    await assert.rejects(a2a.send("私の下書き"), /send_cancelled/);
    assert.equal(state.requests.length, 0);
    await state.message({ type: "ready" });
    assert.equal(state.projections.at(-1).draft.prompt, "私の下書き");
    state.confirm = async (m, o, yes) => yes;
    await a2a.select(0);
    await oldMessage({
      type: "draft",
      prompt: "stale private input",
      stream: true,
    });
    await state.message({ type: "ready" });
    assert.deepEqual(state.projections.at(-1).draft, {
      prompt: "",
      stream: false,
    });
    assert.equal(a2a.conversation.state.context, undefined);
    assert.equal(state.requests.length, 0);
    state.sessionChanged();
    await oldMessage({ type: "ready" });
    assert.equal(a2a.conversation, undefined);
    assert.doesNotMatch(
      JSON.stringify(state.projections.at(-1)),
      /私の下書き|stale private input/,
    );
  } finally {
    a2a.dispose();
  }
});
test("New conversation rechecks the current source and target consent; changed selection asks for reselection", async () => {
  const { a2a, browser } = setup();
  try {
    await a2a.select(0);
    const original = a2a.conversation;
    state.confirm = async () => undefined;
    await state.message({
      type: "new",
      revision: state.projections.at(-1).revision,
    });
    assert.equal(
      a2a.conversation,
      original,
      "Declined replacement keeps the conversation",
    );
    state.confirm = async (m, o, yes) => yes;
    await state.message({
      type: "new",
      revision: state.projections.at(-1).revision,
    });
    assert.notEqual(a2a.conversation, original);
    assert.equal(
      state.requests.length,
      0,
      "Replacement must not send or cancel remotely",
    );
    const replacement = a2a.conversation;
    browser.state.detail = { item: { kind: "agent" } };
    await state.message({
      type: "new",
      revision: state.projections.at(-1).revision,
    });
    assert.equal(a2a.conversation, replacement);
    assert.equal(state.commands.at(-1), "openAgenticCatalog.open");
    assert.match(state.information, /Select an Agent Card/);
    await state.message({
      type: "catalog",
      revision: state.projections.at(-1).revision,
    });
    assert.equal(a2a.conversation, replacement);
  } finally {
    a2a.dispose();
  }
});
test("Agent Help opens only its host-owned topic without private input or traffic", async () => {
  const { a2a } = setup();
  try {
    await a2a.select(0);
    await state.message({
      type: "draft",
      prompt: "private draft",
      stream: false,
    });
    const revision = state.projections.at(-1).revision;
    await state.message({ type: "help", revision });
    assert.equal(state.commands.at(-1), "openAgenticCatalog.help");
    assert.deepEqual(state.commandArgs, ["agent"]);
    assert.deepEqual(state.requests, []);
    await state.message({
      type: "help",
      revision,
      url: "https://private.example/?prompt=secret",
    });
    assert.match(state.errors.at(-1), /selection_changed/);
    assert.equal(state.commands.length, 1);
    await state.message({ type: "ready" });
    assert.equal(state.projections.at(-1).draft.prompt, "private draft");
  } finally {
    a2a.dispose();
  }
});
test.after(() => {
  hooks.deregister();
  delete globalThis.a2aHostFixture;
});
