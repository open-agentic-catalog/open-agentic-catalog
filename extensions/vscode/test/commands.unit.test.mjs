import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Replace only the editor and network edges; execute the compiled command,
// Session, configuration and SDK code without a host, listener or subprocess.
const hooks = registerHooks({
  resolve(specifier, context, next) {
    if (specifier === "vscode" || specifier === "undici")
      return { url: `catalog-unit:${specifier}`, shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === "catalog-unit:vscode")
      return {
        format: "module",
        source: `export const {window, workspace, commands, env, StatusBarAlignment, ConfigurationTarget, Uri, EventEmitter, ThemeIcon, TreeItem, ViewColumn, ProgressLocation, QuickInputButtons, FileType, lm, McpHttpServerDefinition, McpStdioServerDefinition} = globalThis.catalogCommandFixture.vscode;`,
        shortCircuit: true,
      };
    if (url === "catalog-unit:undici")
      return {
        format: "module",
        source: `export class Agent { async destroy() {} };
          export class ProxyAgent extends Agent {};
          export const fetch = (...args) => globalThis.catalogCommandFixture.fetch(...args);`,
        shortCircuit: true,
      };
    return next(url, context);
  },
});

function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
const me = {
  tenantId: "00000000-0000-7000-8000-000000000001",
  realmId: "00000000-0000-7000-8000-000000000002",
  membershipId: "00000000-0000-7000-8000-000000000003",
  subject: "reader",
  permissions: ["resource.read"],
  availablePermissions: ["resource.read"],
};
const configuration = ["first", "second"].map((id) => ({
  id,
  label: id,
  origin: `https://${id}.example`,
  mode: "control",
  auth: "manual",
}));
const commands = new Map();
const saved = new Map();
const requests = [];
const errors = [];
const status = { show() {}, dispose() {} };
let prompt;
let input;
let deletion;
let bridge;
const frames = [];
const openedHelp = [];
const helpStats = [];
let helpLayout = "README.md";
globalThis.catalogCommandFixture = {
  vscode: {
    lm: { registerMcpServerDefinitionProvider: () => ({ dispose() {} }) },
    EventEmitter: class {
      event = () => ({ dispose() {} });
      fire() {}
      dispose() {}
    },
    ThemeIcon: class {},
    TreeItem: class {},
    ViewColumn: { Active: 1 },
    window: {
      tabGroups: { all: [], onDidChangeTabs: () => ({ dispose() {} }) },
      withProgress: async (_, action) =>
        action(
          { report() {} },
          {
            isCancellationRequested: false,
            onCancellationRequested: () => ({ dispose() {} }),
          },
        ),
      createTreeView: () => ({ dispose() {} }),
      createWebviewPanel: () => {
        let closed;
        return {
          reveal() {},
          dispose() {
            closed?.();
          },
          onDidDispose(listener) {
            closed = listener;
          },
          webview: {
            cspSource: "vscode-webview://owned",
            asWebviewUri: (uri) => uri,
            html: "",
            postMessage: async (value) => {
              frames.push(value);
              return true;
            },
            onDidReceiveMessage: (listener) => {
              bridge = listener;
            },
          },
        };
      },
      createStatusBarItem: () => status,
      showInputBox: async () => {
        prompt.resolve();
        return input.promise;
      },
      showInformationMessage: async () => undefined,
      showWarningMessage: async () => {
        prompt.resolve();
        return input.promise;
      },
      showErrorMessage: async (message) => {
        errors.push(message);
      },
      registerUriHandler: () => ({ dispose() {} }),
    },
    workspace: {
      fs: {
        stat: async (uri) => {
          helpStats.push(uri.toString());
          if (uri.toString() !== "file:///owned-extension/" + helpLayout)
            throw Error("absent");
          return { type: 1 };
        },
      },
      onDidChangeWorkspaceFolders: () => ({ dispose() {} }),
      registerTextDocumentContentProvider: () => ({ dispose() {} }),
      getConfiguration: () => ({
        inspect: () => ({}),
        get: () => configuration,
        update: async (_, value) => {
          configuration.splice(0, configuration.length, ...value);
        },
      }),
      onDidChangeConfiguration: () => ({ dispose() {} }),
    },
    commands: {
      executeCommand: async (...args) => openedHelp.push(args),
      registerCommand: (name, action) => {
        commands.set(name, action);
        return { dispose() {} };
      },
    },
    env: { uriScheme: "vscode" },
    StatusBarAlignment: { Right: 1 },
    ProgressLocation: { Notification: 15 },
    ConfigurationTarget: { Global: 1 },
    FileType: { File: 1 },
    Uri: {
      parse: (value) => ({ toString: () => value }),
      joinPath: (base, ...parts) => ({
        toString: () => base.toString() + "/" + parts.join("/"),
        with: ({ fragment }) => ({
          toString: () =>
            base.toString() + "/" + parts.join("/") + "#" + fragment,
        }),
      }),
    },
  },
  fetch: async (url, options) => {
    requests.push({ url, authorization: options.headers.authorization });
    const path = new URL(url).pathname;
    return new Response(
      JSON.stringify(
        path === "/api/v1/me"
          ? me
          : path === "/api/v1/catalog/resources"
            ? { items: [], total: 0 }
            : path === "/api/v1/catalog/search"
              ? {
                  items: [],
                  partial: false,
                  durableTotal: 0,
                  liveCount: 0,
                  nextCursor: "owned-continuation",
                }
              : { resourceKinds: ["agent", "mcp_server", "skill", "plugin"] },
      ),
      { headers: { "content-type": "application/json" } },
    );
  },
};
const { activate } = await import(
  new URL("extension.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);

test("native prompts and browser handoffs stay bound to the selected session", async (t) => {
  t.after(() => {
    hooks.deregister();
    delete globalThis.catalogCommandFixture;
  });
  const subscriptions = [];
  const directory = await mkdtemp(join(tmpdir(), "catalog-commands-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const api = await activate({
    subscriptions,
    extension: { id: "open-agentic-catalog.open-agentic-catalog" },
    globalStorageUri: { fsPath: directory },
    extensionUri: { toString: () => "file:///owned-extension" },
    globalState: { get: () => undefined, update: async () => undefined },
    secrets: {
      get: async (key) => saved.get(key),
      store: async (key, value) => {
        saved.set(key, value);
      },
      delete: async (key) => {
        if (deletion) await deletion(key);
        saved.delete(key);
      },
      onDidChange: () => ({ dispose() {} }),
    },
  });
  t.after(() => {
    for (const subscription of subscriptions) subscription.dispose();
  });
  // Offline Help works before authentication in both exact maintained layouts.
  for (const layout of ["README.md", "readme.md"]) {
    helpLayout = layout;
    await commands.get("openAgenticCatalog.help")("agent");
    assert.equal(openedHelp.at(-1)[0], "markdown.showPreview");
    assert.equal(
      openedHelp.at(-1)[1].toString(),
      `file:///owned-extension/${layout}#agent-outcomes`,
    );
  }
  const previews = () =>
    openedHelp.filter(([command]) => command === "markdown.showPreview");
  const window = globalThis.catalogCommandFixture.vscode.window;
  const pickerTopics = [
    "start",
    "setup",
    "connection",
    "trust",
    "catalog",
    "tools",
    "agent",
  ];
  let chosenTopic;
  let pickerCalls = 0;
  window.showQuickPick = async (items, options) => {
    pickerCalls++;
    assert.deepEqual(
      items.map(({ id }) => id),
      pickerTopics,
    );
    assert.equal(options.title, "Catalog Help (offline)");
    return items.find(({ id }) => id === chosenTopic);
  };
  const beforeCancel = { previews: previews().length, stats: helpStats.length };
  await commands.get("openAgenticCatalog.help")();
  assert.equal(previews().length, beforeCancel.previews);
  assert.equal(
    helpStats.length,
    beforeCancel.stats,
    "Canceled Help reads no file",
  );
  assert.deepEqual(errors, [], "Canceling Help is not an error");
  chosenTopic = "tools";
  await commands.get("openAgenticCatalog.help")();
  assert.equal(pickerCalls, 2);
  assert.equal(previews().length, beforeCancel.previews + 1);
  assert.equal(
    previews().at(-1)[1].toString(),
    "file:///owned-extension/readme.md#manage-my-tools",
  );
  assert.equal(saved.size, 0, "Help does not create credentials");
  delete window.showQuickPick;
  const helpCount = previews().length;
  await commands.get("openAgenticCatalog.help")(
    "https://private.example/?token=secret",
  );
  assert.match(errors.pop(), /invalid_help_topic/);
  helpLayout = "absent";
  await commands.get("openAgenticCatalog.help")("tools");
  assert.match(errors.pop(), /help_unavailable/);
  assert.equal(previews().length, helpCount);
  assert.deepEqual(requests, [], "Help never contacts a Catalog or issuer");
  async function signIn(change) {
    prompt = deferred();
    input = deferred();
    const operation = commands.get("openAgenticCatalog.signIn")();
    await prompt.promise;
    await change();
    input.resolve("reader");
    await operation;
  }
  // Successful control exercises actual SDK calls and the selected credential key.
  await api.select("first");
  await signIn(async () => {});
  assert.equal((await api.test()).identity.subject, "reader");
  assert.equal(saved.size, 1);
  const beforeNativeRequests = requests.length;
  let nativePickers = 0;
  globalThis.catalogCommandFixture.vscode.window.showQuickPick = async () => {
    nativePickers++;
  };
  for (const [kind, command, code] of [
    ["mcp_server", "useMcp", "native_mcp_document_required"],
    ["agent", "talkAgent", "native_agent_document_required"],
  ]) {
    api.browsing().detail = {
      item: {
        kind,
        origin: "indexed",
        key: "indexed-owned",
        identifier: "urn:air:example.com:test:owned",
        name: "Owned indexed metadata",
        description: "Inspect-only projection",
        tags: [],
        versionPresent: false,
      },
      facts: [],
      references: [],
      packages: [],
      documents: [{ label: "Indexed reader projection", value: {} }],
    };
    await commands.get("openAgenticCatalog." + command)();
    assert.ok(errors.pop().includes(code));
  }
  delete api.browsing().detail;
  delete globalThis.catalogCommandFixture.vscode.window.showQuickPick;
  assert.equal(
    nativePickers,
    0,
    "Inspect-only projections never open an empty native resource picker",
  );
  assert.equal(requests.length, beforeNativeRequests);
  assert.ok(
    requests.every(
      (request) =>
        new URL(request.url).hostname === "first.example" &&
        request.authorization === "Bearer reader",
    ),
  );
  // Force a late draft after an ordinary read, rather than relying on bridge timing.
  await commands.get("openAgenticCatalog.open")();
  await bridge({ type: "ready" });
  const originalFrame = frames.findLast((f) => f.type === "state").value;
  await api.browse({});
  assert.ok(
    frames.findLast((f) => f.type === "state").value.revision >
      originalFrame.revision,
  );
  const originalErrors = errors.length;
  await bridge({
    type: "draft",
    revision: originalFrame.revision,
    scope: originalFrame.scope,
    query: { text: "late ordinary-read draft" },
  });
  assert.equal(errors.length, originalErrors);
  await bridge({ type: "ready" });
  assert.equal(
    frames.findLast((f) => f.type === "state").value.draft.text,
    "late ordinary-read draft",
  );
  await api.signOut();
  await bridge({
    type: "draft",
    revision: originalFrame.revision,
    scope: originalFrame.scope,
    query: { text: "OLD-IDENTITY-PRIVATE-DRAFT" },
  });
  assert.equal(errors.at(-1), "Catalog: connection_changed");
  await bridge({ type: "ready" });
  assert.equal(frames.findLast((f) => f.type === "state").value.draft.text, "");
  errors.length = 0;
  await api.authenticate("reader");
  await bridge({ type: "ready" });
  const emptyDetailFrame = frames.findLast((f) => f.type === "state").value;
  const beforeInvalidIndex = requests.length;
  await bridge({
    type: "inspect",
    revision: emptyDetailFrame.revision,
    index: Number.MAX_SAFE_INTEGER,
  });
  assert.equal(
    errors.at(-1),
    "Catalog: selection_changed. The selection changed. Refresh the view and reselect the current resource or installed package before trying again.",
  );
  assert.equal(
    requests.length,
    beforeInvalidIndex,
    "out-of-range actions cannot start a read",
  );
  errors.length = 0;
  // Every explicit peer continuation requires fresh consent, including retries.
  await api.browse({ origin: "live", text: "owned" });
  const originalWarning =
    globalThis.catalogCommandFixture.vscode.window.showWarningMessage;
  let confirmations = 0;
  globalThis.catalogCommandFixture.vscode.window.showWarningMessage = async (
    _message,
    options,
    choice,
  ) => {
    assert.equal(options.modal, true);
    assert.equal(choice, "Search peers");
    confirmations++;
    return confirmations < 3 ? choice : undefined;
  };
  requests.length = 0;
  await commands.get("openAgenticCatalog.nextPage")();
  await commands.get("openAgenticCatalog.nextPage")();
  assert.equal(
    requests.filter((r) => r.url.endsWith("/api/v1/catalog/search")).length,
    2,
  );
  const beforeDecline = requests.length;
  await commands.get("openAgenticCatalog.nextPage")();
  assert.equal(confirmations, 3);
  assert.equal(requests.length, beforeDecline);
  assert.equal(api.browsing().canNext, true);
  const consentOpened = deferred(),
    consentChoice = deferred();
  globalThis.catalogCommandFixture.vscode.window.showWarningMessage =
    async () => {
      consentOpened.resolve();
      return consentChoice.promise;
    };
  const staleConsent = commands.get("openAgenticCatalog.nextPage")();
  await consentOpened.promise;
  await api.select("second");
  consentChoice.resolve("Search peers");
  await staleConsent;
  assert.equal(
    requests.length,
    beforeDecline,
    "stale peer consent must not send traffic",
  );
  assert.equal(api.browsing().canNext, false);
  assert.equal(errors.at(-1), "Catalog: connection_changed");
  errors.length = 0;
  globalThis.catalogCommandFixture.vscode.window.showWarningMessage =
    originalWarning;
  await api.select("first");
  await api.browse({});
  requests.length = 0;
  // Restored credentials become verified through ordinary browsing, without
  // requiring a second Test Connection command to update the account tooltip.
  await api.select("first");
  assert.match(status.tooltip, /connection not verified/u);
  await commands.get("openAgenticCatalog.refresh")();
  assert.match(status.tooltip, /reader; tenant/u);
  assert.doesNotMatch(status.tooltip, /connection not verified/u);
  await api.signOut();
  assert.equal(saved.size, 0);
  requests.length = 0;
  for (const change of [
    () => api.select("second"),
    async () => {
      await api.select("second");
      await api.select("first");
    },
    () => api.signOut(),
  ]) {
    await api.select("first");
    errors.length = 0;
    await signIn(change);
    assert.equal(saved.size, 0, "stale token must never reach SecretStorage");
    assert.equal(
      requests.length,
      0,
      "stale token must never reach either Catalog",
    );
    assert.equal(api.snapshot(), undefined);
    assert.equal(errors.length, 1);
    assert.doesNotMatch(errors[0], /reader/u);
    await assert.rejects(api.test(), /sign_in_required/u);
  }

  // A stale removal confirmation cannot delete the previously selected credentials.
  await api.select("first");
  await api.authenticate("reader");
  prompt = deferred();
  input = deferred();
  errors.length = 0;
  const removal = commands.get("openAgenticCatalog.removeConnection")();
  await prompt.promise;
  await api.select("second");
  await api.authenticate("reader");
  input.resolve("Remove");
  await removal;
  assert.equal(saved.size, 2);
  assert.equal(errors.length, 1);
  assert.equal((await api.test()).identity.subject, "reader");
  await api.signOut();
  await api.select("first");
  await api.signOut();

  configuration[0] = {
    ...configuration[0],
    auth: "oidc",
    oidc: {
      issuer: "https://issuer.example",
      clientId: "client",
      audience: "catalog",
      scopes: ["openid"],
    },
  };
  const environment = globalThis.catalogCommandFixture.vscode.env;
  const redirect = "vscode://open-agentic-catalog.open-agentic-catalog/oidc";
  let opened = 0;
  environment.openExternal = async () => {
    opened++;
    return true;
  };
  environment.asExternalUri = async () => {
    prompt.resolve();
    return input.promise;
  };
  await api.select("first");
  prompt = deferred();
  input = deferred();
  errors.length = 0;
  requests.length = 0;
  const handoff = commands.get("openAgenticCatalog.signIn")();
  await prompt.promise;
  await api.select("second");
  input.resolve({ toString: () => redirect });
  await handoff;
  assert.equal(opened, 0);
  assert.equal(
    requests.length,
    0,
    "stale URI conversion must not contact either issuer or Catalog",
  );
  assert.equal(errors.length, 1);

  // A failed late browser launch must not sign out a newer connection.
  const catalogFetch = globalThis.catalogCommandFixture.fetch;
  globalThis.catalogCommandFixture.fetch = async (url, options) => {
    if (url === "https://issuer.example/.well-known/openid-configuration")
      return new Response(
        JSON.stringify({
          issuer: "https://issuer.example",
          authorization_endpoint: "https://issuer.example/authorize",
          token_endpoint: "https://issuer.example/token",
          jwks_uri: "https://issuer.example/keys",
          code_challenge_methods_supported: ["S256"],
          response_types_supported: ["code"],
        }),
        { headers: { "content-type": "application/json" } },
      );
    return catalogFetch(url, options);
  };
  environment.asExternalUri = async () => ({ toString: () => redirect });
  environment.openExternal = async () => {
    prompt.resolve();
    return input.promise;
  };
  await api.select("first");
  prompt = deferred();
  input = deferred();
  errors.length = 0;
  const launch = commands.get("openAgenticCatalog.signIn")();
  await prompt.promise;
  await api.select("second");
  await api.authenticate("reader");
  await api.test();
  input.resolve(false);
  await launch;
  assert.equal(errors.length, 1);
  assert.equal(saved.size, 1);
  assert.equal(api.snapshot().identity.subject, "reader");
  assert.equal((await api.test()).identity.subject, "reader");
  // Even a missed configuration event cannot redirect a prompt to changed settings.
  configuration[1] = { ...configuration[1], origin: "https://changed.example" };
  errors.length = 0;
  const before = requests.length;
  globalThis.catalogCommandFixture.vscode.window.showInputBox = async () => {
    assert.fail("stale settings must be refused before credential input");
  };
  await commands.get("openAgenticCatalog.signIn")();
  assert.deepEqual(errors, ["Catalog: connection_changed"]);
  assert.equal(requests.length, before);
  // An old recovery toast cannot dispatch sign-in after a connection switch.
  await api.signOut();
  await api.select("second");
  const recovered = [];
  const recoveryOpened = deferred();
  const choice = deferred();
  globalThis.catalogCommandFixture.vscode.commands.executeCommand = async (
    name,
  ) => {
    if (name !== "setContext") recovered.push(name);
  };
  globalThis.catalogCommandFixture.vscode.window.showErrorMessage = async (
    message,
    ...actions
  ) => {
    assert.match(message, /sign_in_required/);
    assert.ok(actions.includes("Sign in"));
    recoveryOpened.resolve();
    return choice.promise;
  };
  const failedRead = commands.get("openAgenticCatalog.refresh")();
  await recoveryOpened.promise;
  await api.select("first");
  choice.resolve("Sign in");
  await failedRead;
  assert.deepEqual(recovered, []);
  globalThis.catalogCommandFixture.vscode.window.showErrorMessage = async (
    _message,
    ...actions
  ) => {
    assert.ok(actions.includes("Select connection"));
    return "Select connection";
  };
  await api.signOut();
  await commands.get("openAgenticCatalog.refresh")();
  assert.deepEqual(recovered, ["openAgenticCatalog.connect"]);
  // The real command's delayed deletion must remove A only and preserve B.
  globalThis.catalogCommandFixture.vscode.window.showErrorMessage = async (
    message,
  ) => {
    errors.push(message);
  };
  globalThis.catalogCommandFixture.vscode.window.showWarningMessage =
    async () => "Remove";
  configuration.push({
    ...configuration[1],
    id: "third",
    label: "third",
    origin: "https://third.example",
  });
  await api.select("second");
  await api.authenticate("reader");
  const entered = deferred(),
    release = deferred();
  deletion = async () => {
    entered.resolve();
    await release.promise;
  };
  const pending = commands.get("openAgenticCatalog.removeConnection")();
  await entered.promise;
  const choosing = api.select("third");
  release.resolve();
  await Promise.all([pending, choosing]);
  deletion = undefined;
  await api.authenticate("reader");
  assert.equal((await api.test()).identity.subject, "reader");
  assert.ok(configuration.some((value) => value.id === "third"));
  assert.equal(
    configuration.some((value) => value.id === "second"),
    false,
  );
  // Failed deletion and a later failed settings write report partial removal.
  const retainedDefinitions = structuredClone(configuration);
  const retainedSecrets = new Map(saved);
  errors.length = 0;
  deletion = async () => {
    throw new Error("private-secret-delete-error");
  };
  await commands.get("openAgenticCatalog.removeConnection")();
  assert.deepEqual(configuration, retainedDefinitions);
  assert.deepEqual(saved, retainedSecrets);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /connection_removal_incomplete/u);
  assert.doesNotMatch(errors[0], /private-secret-delete-error/u);
  deletion = undefined;
  await api.select("third");
  const workspace = globalThis.catalogCommandFixture.vscode.workspace;
  const readConfiguration = workspace.getConfiguration;
  workspace.getConfiguration = (...args) => ({
    ...readConfiguration(...args),
    update: async () => {
      throw new Error("private-settings-write-error");
    },
  });
  errors.length = 0;
  try {
    await commands.get("openAgenticCatalog.removeConnection")();
  } finally {
    workspace.getConfiguration = readConfiguration;
  }
  assert.deepEqual(configuration, retainedDefinitions);
  assert.equal(
    saved.size,
    0,
    "the successful credential deletion is not undone",
  );
  assert.equal(errors.length, 1);
  assert.match(errors[0], /connection_removal_incomplete/u);
  assert.doesNotMatch(errors[0], /private-settings-write-error/u);
  await api.select("third");
  await api.authenticate("reader");
  // A completed verification must not keep progress alive until a toast closes.
  const shown = deferred(),
    dismissed = deferred();
  globalThis.catalogCommandFixture.vscode.window.showInformationMessage =
    () => {
      shown.resolve();
      return dismissed.promise;
    };
  const verified = commands.get("openAgenticCatalog.testConnection")();
  await shown.promise;
  await verified;
  assert.equal(api.snapshot().identity.subject, "reader");
  dismissed.resolve();
  await commands.get("openAgenticCatalog.removeConnection")();
  assert.equal(
    configuration.some((value) => value.id === "third"),
    false,
  );
  assert.equal(saved.size, 0);
  assert.deepEqual(
    configuration,
    retainedDefinitions.filter((value) => value.id !== "third"),
  );
});
