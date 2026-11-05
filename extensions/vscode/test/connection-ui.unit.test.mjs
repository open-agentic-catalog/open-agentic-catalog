import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";

const hooks = registerHooks({
  resolve(specifier, context, next) {
    return specifier === "vscode"
      ? { url: "connection-unit:vscode", shortCircuit: true }
      : next(specifier, context);
  },
  load(url, context, next) {
    return url === "connection-unit:vscode"
      ? {
          format: "module",
          source:
            "export const {window,workspace,env,commands,QuickInputButtons,ThemeIcon,ConfigurationTarget}=globalThis.connectionUIFixture;",
          shortCircuit: true,
        }
      : next(url, context);
  },
});
let values = [],
  steps = [],
  failures = [],
  scope = {},
  updates = 0,
  writeMode = "save";
const groups = {},
  opened = [];
const Back = {};
class Input {
  value = "";
  validationMessage = undefined;
  callbacks = {};
  onDidHide(fn) {
    this.callbacks.hide = fn;
    return { dispose() {} };
  }
  onDidAccept(fn) {
    this.callbacks.accept = fn;
    return { dispose() {} };
  }
  onDidTriggerButton(fn) {
    this.callbacks.button = fn;
    return { dispose() {} };
  }
  onDidChangeValue() {
    return { dispose() {} };
  }
  hide() {
    this.callbacks.hide();
  }
  dispose() {}
  show() {
    queueMicrotask(async () => {
      try {
        const next = steps.shift();
        assert.ok(next, `unexpected field ${this.title}`);
        await next(this);
      } catch (error) {
        failures.push(error);
        this.hide();
      }
    });
  }
}
class Picker extends Input {
  items = [];
  selectedItems = [];
}
globalThis.connectionUIFixture = {
  QuickInputButtons: { Back },
  ThemeIcon: class {},
  ConfigurationTarget: { Global: 1 },
  env: { remoteName: undefined },
  window: {
    createInputBox: () => new Input(),
    createQuickPick: () => new Picker(),
    showQuickPick: async (items) => {
      const step = steps.shift();
      assert.ok(step);
      return step(items);
    },
    showOpenDialog: async () => undefined,
  },
  workspace: {
    getConfiguration: () => ({
      inspect: () => scope,
      get: (key) =>
        key === "connections"
          ? structuredClone(values)
          : structuredClone(groups[key] ?? {}),
      update: async (key, value, target) => {
        assert.equal(target, 1);
        updates++;
        if (writeMode === "refuse") throw new Error("private-settings-error");
        if (writeMode === "ignore") return;
        if (key === "connections") values = structuredClone(value);
        else groups[key] = structuredClone(value);
        if (writeMode === "uncertain")
          throw new Error("private-settings-error");
      },
    }),
  },
  commands: { executeCommand: async (...args) => opened.push(args) },
};
const { editConnection, transportSettings } = await import(
  new URL("connection-ui.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);
const { connections } = await import(
  pathToFileURL(process.env.CATALOG_VSCODE_LIB)
);
const first = {
  id: "first",
  label: "First",
  origin: "https://first.example",
  mode: "control",
  auth: "manual",
};
const answer = (name, value, before) => (control) => {
  assert.equal(control.title, `Catalog connection: ${name}`);
  before?.(control);
  if (control instanceof Picker)
    control.selectedItems = control.items.filter(
      (item) => item.value === value,
    );
  else control.value = value;
  control.callbacks.accept();
};
const add = (label = "New") => [
  answer("Connection ID", "new"),
  answer("Connection name", label),
  answer("Connection security", "false"),
  answer("Catalog origin", "https://new.example"),
  answer("Catalog API mode", "published"),
  answer("Authentication", "anonymous"),
  answer("Optional transport settings", "defaults"),
  answer("Review and save", "save"),
];
function reset() {
  values = [structuredClone(first)];
  steps = [];
  failures = [];
  scope = {};
  updates = 0;
  writeMode = "save";
  opened.length = 0;
}
function finished() {
  assert.deepEqual(failures, []);
  assert.deepEqual(steps, []);
}
test("setup Help retains unsaved answers and uses a fixed offline target", async () => {
  reset();
  steps = [
    async (control) => {
      control.value = "unsaved-name";
      const help = control.buttons.find(
        (b) => b.tooltip === "Open connection setup help (offline)",
      );
      assert.ok(help);
      await control.callbacks.button(help);
      assert.equal(control.value, "unsaved-name");
      assert.deepEqual(opened, [["openAgenticCatalog.help", "setup"]]);
      assert.equal(updates, 0);
      control.hide();
    },
  ];
  assert.equal(await editConnection(), undefined);
  assert.equal(updates, 0);
  finished();
});
test("guided setup validates early, supports Back and merges fresh unrelated settings", async () => {
  reset();
  steps = add("\u{1f916}".repeat(80));
  steps[0] = (control) => {
    control.value = "first";
    control.callbacks.accept();
    assert.ok(control.validationMessage);
    control.value = "new";
    control.callbacks.accept();
  };
  steps[1] = answer("Connection name", "\u{1f916}".repeat(80), (control) => {
    control.value = "\u{1f916}".repeat(81);
    control.callbacks.accept();
    assert.ok(control.validationMessage);
  });
  steps.splice(
    3,
    0,
    (control) => {
      assert.match(control.title, /Catalog origin$/u);
      control.callbacks.button(Back);
    },
    answer("Connection security", "false"),
  );
  steps[steps.length - 1] = answer("Review and save", "save", () =>
    values.push({ ...first, id: "concurrent", label: "Concurrent" }),
  );
  const result = await editConnection();
  finished();
  assert.equal(result.id, "new");
  assert.equal([...result.label].length, 80);
  assert.equal(updates, 1);
  assert.deepEqual(values.slice(0, 2), [
    first,
    { ...first, id: "concurrent", label: "Concurrent" },
  ]);
  assert.equal(values[2].auth, "anonymous");
  assert.equal(Object.hasOwn(values[2], "oidc"), false);
});
test("canceled setup and same-definition conflicts perform no settings writes", async () => {
  reset();
  steps = [(control) => control.hide()];
  assert.equal(await editConnection(), undefined);
  finished();
  assert.equal(updates, 0);
  assert.deepEqual(values, [first]);
  const original = connections(values)[0];
  steps = add().slice(1);
  steps[0] = answer("Connection name", "Edited");
  steps[steps.length - 1] = answer("Review and save", "save", () => {
    values[0] = { ...first, origin: "https://changed.example" };
  });
  await assert.rejects(editConnection(original), /connection_changed/u);
  finished();
  assert.equal(updates, 0);
  assert.equal(values[0].origin, "https://changed.example");
  scope = { workspaceValue: [] };
  await assert.rejects(editConnection(), /workspace_settings_refused/u);
  assert.equal(updates, 0);
});
test("OIDC setup exposes refresh only in its mode and preserves explicit trust/limits", async () => {
  reset();
  steps = [
    ...add().slice(0, 4),
    answer("Catalog API mode", "control"),
    answer("Authentication", "oidc"),
    answer("OIDC issuer", "https://issuer.example/realm"),
    answer("OIDC public client ID", "extension"),
    answer("Catalog token audience", "catalog"),
    answer("Optional token refresh", "true"),
    answer("Requested scopes", "openid catalog.read offline_access"),
    answer("Optional transport settings", "custom"),
    answer("Catalog CA file", "/private/catalog.pem"),
    answer("Catalog proxy", "http://proxy.example"),
    answer("Request timeout (milliseconds)", "5000"),
    answer("Request limit (bytes)", "4096"),
    answer("Response limit (bytes)", "8192"),
    answer("Header limit (bytes)", "2048"),
    answer("Review and save", "save"),
  ];
  const result = await editConnection();
  finished();
  assert.equal(result.oidc.refresh, true);
  assert.deepEqual(result.oidc.scopes, [
    "openid",
    "catalog.read",
    "offline_access",
  ]);
  assert.equal(result.caFile, "/private/catalog.pem");
  assert.equal(result.timeoutMs, 5000);
  assert.equal(result.headerBytes, 2048);
});
test("connection saves report refused, ignored and uncertain settings writes without replay", async () => {
  for (const mode of ["refuse", "ignore", "uncertain"]) {
    reset();
    writeMode = mode;
    steps = add();
    await assert.rejects(editConnection(), (error) => {
      assert.equal(error.code, "settings_write_unconfirmed");
      assert.doesNotMatch(String(error), /private-settings-error/u);
      return true;
    });
    finished();
    assert.equal(
      updates,
      1,
      "an uncertain settings write must not be repeated",
    );
    assert.deepEqual(values[0], first);
    assert.equal(values.length, mode === "uncertain" ? 2 : 1);
  }
});
test("connection saves recheck newly conflicting IDs and workspace scope before writing", async () => {
  reset();
  steps = add();
  const competing = { ...first, id: "new", label: "Another window" };
  steps[steps.length - 1] = answer("Review and save", "save", () =>
    values.push(competing),
  );
  await assert.rejects(editConnection(), /connection_id_exists/u);
  finished();
  assert.equal(updates, 0);
  assert.deepEqual(values, [first, competing]);
  reset();
  steps = add();
  steps[steps.length - 1] = answer("Review and save", "save", () => {
    scope = { workspaceFolderValue: [] };
  });
  await assert.rejects(editConnection(), /workspace_settings_refused/u);
  finished();
  assert.equal(updates, 0);
  assert.deepEqual(values, [first]);
});
test("transport navigation uses fixed setting IDs and path edits preserve fresh values", async (t) => {
  t.after(() => {
    hooks.deregister();
    delete globalThis.connectionUIFixture;
  });
  reset();
  steps = [(items) => items.find((item) => item.id === "connections")];
  await transportSettings();
  finished();
  assert.deepEqual(opened, [
    ["workbench.action.openSettings", "@id:openAgenticCatalog.connections"],
  ]);
  groups.packageTools = { timeoutMs: 5000, caFile: "/private/original.pem" };
  steps = [
    (items) => items.find((item) => item.id === "packageTools"),
    (items) => items.find((item) => item.key === "gitPath"),
    answer("gitPath", "/usr/bin/git", (control) => {
      control.value = "relative";
      control.callbacks.accept();
      assert.ok(control.validationMessage);
      groups.packageTools.timeoutMs = 7000;
    }),
  ];
  await transportSettings();
  finished();
  assert.deepEqual(groups.packageTools, {
    timeoutMs: 7000,
    caFile: "/private/original.pem",
    gitPath: "/usr/bin/git",
  });
  steps = [
    (items) => items.find((item) => item.id === "a2a"),
    (items) => items.find((item) => item.key === "caFile"),
    (control) => control.hide(),
  ];
  const before = updates;
  await transportSettings();
  finished();
  assert.equal(updates, before);
  steps = [(items) => items.find((item) => item.id === "mcp")];
  await transportSettings();
  finished();
  assert.deepEqual(opened.at(-1), ["workbench.action.openSettings", "mcp"]);
  steps = [(items) => items.find((item) => item.id === "help")];
  await transportSettings();
  finished();
  assert.deepEqual(opened.at(-1), ["openAgenticCatalog.help", "trust"]);
  globalThis.connectionUIFixture.window.showOpenDialog = async () => {
    throw new Error("private-file-picker-sentinel");
  };
  steps = [
    (items) => items.find((item) => item.id === "packageTools"),
    (items) => items.find((item) => item.key === "gitPath"),
    async (control) => {
      await control.callbacks.button(control.buttons[1]);
      assert.match(control.validationMessage, /File selection failed/u);
      assert.doesNotMatch(
        control.validationMessage,
        /private-file-picker-sentinel/u,
      );
      control.hide();
    },
  ];
  const savedUpdates = updates;
  await transportSettings();
  finished();
  assert.equal(updates, savedUpdates);
  // A native file dialog can finish after its owning input was canceled.
  let finishPicker, pendingPicker, canceledInput;
  globalThis.connectionUIFixture.window.showOpenDialog = () =>
    new Promise((resolve) => {
      finishPicker = resolve;
    });
  steps = [
    (items) => items.find((item) => item.id === "packageTools"),
    (items) => items.find((item) => item.key === "gitPath"),
    (control) => {
      canceledInput = control;
      pendingPicker = control.callbacks.button(control.buttons[1]);
      control.hide();
    },
  ];
  await transportSettings();
  finished();
  const canceledValue = canceledInput.value;
  finishPicker([{ fsPath: "/private/late-file-choice" }]);
  await pendingPicker;
  assert.equal(canceledInput.value, canceledValue);
  assert.equal(updates, savedUpdates);
  for (const id of ["packageTools", "a2a"]) {
    steps = [
      (items) => items.find((item) => item.id === id),
      (items) => items.find((item) => item.key === "settings"),
    ];
    await transportSettings();
    finished();
    assert.deepEqual(opened.at(-1), [
      "workbench.action.openSettings",
      `@id:openAgenticCatalog.${id}`,
    ]);
  }
  steps = [
    (items) => items.find((item) => item.id === "packageTools"),
    (items) => items.find((item) => item.key === "gitPath"),
    answer("gitPath", "/usr/bin/git", () => {
      groups.packageTools.gitPath = "/private/concurrent-git";
    }),
  ];
  await assert.rejects(transportSettings(), /connection_changed/u);
  finished();
  assert.equal(groups.packageTools.gitPath, "/private/concurrent-git");
  assert.equal(updates, savedUpdates);
});
