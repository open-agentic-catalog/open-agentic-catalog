import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";
let active, scenario;
const hook = registerHooks({
  resolve(specifier, context, next) {
    return specifier === "vscode"
      ? { url: "opinion-unit:vscode", shortCircuit: true }
      : next(specifier, context);
  },
  load(url, context, next) {
    return url === "opinion-unit:vscode"
      ? {
          format: "module",
          source:
            "export const {window,env,ThemeIcon,CancellationTokenSource}=globalThis.opinionNativeFixture;",
          shortCircuit: true,
        }
      : next(url, context);
  },
});
const disposable = () => ({ dispose() {} });
globalThis.opinionNativeFixture = {
  CancellationTokenSource: class {
    listeners = [];
    token = {
      onCancellationRequested: (fn) => {
        this.listeners.push(fn);
        return disposable();
      },
    };
    cancel() {
      for (const fn of this.listeners) fn();
    }
    dispose() {}
  },
  ThemeIcon: class {
    constructor(id) {
      this.id = id;
    }
  },
  env: { clipboard: { readText: async () => scenario.clipboard } },
  window: {
    async showQuickPick(items, options, token) {
      assert.ok(items[0].description.includes("private"));
      return new Promise((resolve) => {
        token.onCancellationRequested(() => resolve(undefined));
        queueMicrotask(() => scenario.change());
      });
    },
    createInputBox() {
      const callbacks = {};
      let value = "";
      active = {
        buttons: [],
        get value() {
          return value;
        },
        set value(next) {
          value = next;
          callbacks.change?.(next);
        },
        onDidHide(fn) {
          callbacks.hide = fn;
          return disposable();
        },
        onDidChangeValue(fn) {
          callbacks.change = fn;
          return disposable();
        },
        onDidTriggerButton(fn) {
          callbacks.button = fn;
          return disposable();
        },
        onDidAccept(fn) {
          callbacks.accept = fn;
          return disposable();
        },
        hide() {
          callbacks.hide?.();
        },
        dispose() {
          this.disposed = true;
        },
        show() {
          queueMicrotask(() => scenario.drive(this, callbacks));
        },
      };
      return active;
    },
  },
};
const { CatalogOpinions } = await import(
  new URL("./opinions-host.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);
function fixture() {
  const listeners = new Set();
  let valid = true;
  const session = {
    onChange(fn) {
      listeners.add(fn);
      return {
        dispose() {
          listeners.delete(fn);
        },
      };
    },
    connection: () => ({}),
    guard: () => () => {
      if (!valid)
        throw Object.assign(new Error("connection_changed"), {
          code: "connection_changed",
        });
    },
  };
  const host = new CatalogOpinions({}, session, async () => {});
  return {
    host,
    change() {
      valid = false;
      for (const fn of listeners) fn();
    },
    listeners,
  };
}
test("native feedback preserves full multiline clipboard and existing text without a file or webview", async () => {
  for (const mode of ["paste", "keep", "type", "cancel"]) {
    const f = fixture();
    const original = "First line\nSecond line 😀";
    scenario = {
      clipboard: "Clipboard\n😀\nPrivate",
      async drive(input, cb) {
        assert.match(input.prompt, /clipboard/u);
        if (mode === "paste") await cb.button(input.buttons[0]);
        if (mode === "keep") await cb.button(input.buttons[1]);
        if (mode === "type") input.value = "Replacement";
        if (mode === "cancel") input.hide();
        else cb.accept();
      },
    };
    assert.equal(
      await f.host.text(original, true),
      mode === "paste"
        ? scenario.clipboard
        : mode === "keep"
          ? original
          : mode === "type"
            ? "Replacement"
            : undefined,
    );
    assert.equal(active.disposed, true);
    assert.equal(f.listeners.size, 1, "Temporary credential listener disposed");
    f.host.dispose();
    assert.equal(f.listeners.size, 0);
  }
});
test("native feedback rejects invalid clipboard without retaining it and redacts on session change", async () => {
  const f = fixture();
  scenario = {
    clipboard: "😀".repeat(4001),
    async drive(input, cb) {
      await cb.button(input.buttons[0]);
      assert.match(input.validationMessage, /4000/u);
      input.value = "Valid";
      cb.accept();
    },
  };
  assert.equal(await f.host.text("", false), "Valid");
  f.host.dispose();
  const changed = fixture();
  scenario = {
    drive() {
      changed.change();
    },
  };
  await assert.rejects(
    changed.host.text("Private\nBefore change", true),
    /connection_changed/u,
  );
  assert.equal(active.value, "");
  assert.equal(active.disposed, true);
  changed.host.dispose();
});
process.on("exit", () => hook.deregister());

test("private native personal lists close immediately on account invalidation", async () => {
  const f = fixture();
  scenario = { change: () => f.change() };
  await assert.rejects(
    f.host.pick([{ label: "My opinion", description: "private identifier" }], {
      title: "My private feedback",
    }),
    /connection_changed/u,
  );
  assert.equal(f.listeners.size, 1);
  f.host.dispose();
});
