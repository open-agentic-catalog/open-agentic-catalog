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

test("an older clipboard reply cannot replace newer feedback or mark it invalid", async (t) => {
  const f = fixture();
  t.after(() => f.host.dispose());
  for (const lateResult of ["text", "error"]) {
    const reply = Promise.withResolvers();
    const opened = Promise.withResolvers();
    scenario = {
      clipboard: reply.promise,
      drive(input, callbacks) {
        opened.resolve({ input, callbacks });
      },
    };
    const editing = f.host.text("", false);
    const { input, callbacks } = await opened.promise;
    const oldPaste = callbacks.button(input.buttons[0]);
    if (lateResult === "text") {
      input.value = "More recent typing";
      reply.resolve("Old clipboard contents");
    } else {
      scenario.clipboard = "Second paste\nwith a new line";
      await callbacks.button(input.buttons[0]);
      reply.reject(new Error("private clipboard error"));
    }
    await oldPaste;
    callbacks.accept();
    assert.equal(input.validationMessage, undefined);
    assert.equal(
      await editing,
      lateResult === "text"
        ? "More recent typing"
        : "Second paste\nwith a new line",
    );
  }
});

test("canceling feedback discards a paste that is still reading the clipboard", async (t) => {
  const f = fixture();
  t.after(() => f.host.dispose());
  const reply = Promise.withResolvers();
  const pending = Promise.withResolvers();
  scenario = {
    clipboard: reply.promise,
    drive(input, callbacks) {
      pending.resolve({ input, paste: callbacks.button(input.buttons[0]) });
      input.hide();
    },
  };
  assert.equal(await f.host.text("", false), undefined);
  const { input, paste } = await pending.promise;
  assert.equal(input.disposed, true);
  reply.resolve("Late clipboard contents");
  await paste;
  assert.equal(input.value, "");
  assert.equal(input.validationMessage, undefined);
});

test("recent feedback, refresh and submission-order switches restart only the private page", async () => {
  const f = fixture(),
    calls = [];
  const original = globalThis.opinionNativeFixture.window.showQuickPick;
  const selections = [
    "Next page",
    "Recent addressed outcomes",
    "Refresh feedback",
    "Submission order",
    undefined,
  ];
  f.host.opinions = {
    clear() {},
    page: async (family, cursor, recent) => {
      calls.push({ family, cursor, recent });
      return {
        items: recent
          ? [
              {
                id: "private-id",
                category: "problem",
                state: "addressed",
                updatedAt: "2026-11-15T12:00:00Z",
                response: "",
              },
            ]
          : [],
        ...(calls.length === 1 ? { next: "opaque" } : {}),
      };
    },
  };
  globalThis.opinionNativeFixture.window.showQuickPick = async (items) => {
    if (calls.at(-1).recent)
      assert.ok(
        items[0].detail.includes("Addressed without a written response"),
      );
    const label = selections.shift();
    if (label === undefined) return undefined;
    const selected = items.find((item) => item.label === label);
    assert.ok(selected, label);
    return selected;
  };
  try {
    await f.host.list("feedback");
    assert.deepEqual(calls, [
      { family: "feedback", cursor: undefined, recent: false },
      { family: "feedback", cursor: "opaque", recent: false },
      { family: "feedback", cursor: undefined, recent: true },
      { family: "feedback", cursor: undefined, recent: true },
      { family: "feedback", cursor: undefined, recent: false },
    ]);
    assert.equal(selections.length, 0);
  } finally {
    globalThis.opinionNativeFixture.window.showQuickPick = original;
    f.host.dispose();
  }
});

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

test("a feedback page returned after an account change never reaches the picker", async (t) => {
  const f = fixture();
  t.after(() => f.host.dispose());
  const reply = Promise.withResolvers();
  const entered = Promise.withResolvers();
  f.host.opinions = {
    clear() {},
    page: async () => {
      entered.resolve();
      return reply.promise;
    },
  };
  const picker = t.mock.method(
    globalThis.opinionNativeFixture.window,
    "showQuickPick",
    async () => undefined,
  );
  const listing = f.host.list("feedback");
  await entered.promise;
  f.change();
  reply.resolve({
    items: [
      {
        id: "old-account-id",
        category: "problem",
        state: "addressed",
        response: "Private moderator reply",
      },
    ],
  });
  await assert.rejects(listing, { code: "connection_changed" });
  assert.equal(picker.mock.callCount(), 0);
});

test("a refused exact opinion read stops before private preview or action selection", async (t) => {
  const f = fixture(),
    selected = "00000000-0000-7000-8000-000000000001";
  t.after(() => f.host.dispose());
  const picker = t.mock.method(
    globalThis.opinionNativeFixture.window,
    "showQuickPick",
    async (items) => items[0],
  );
  f.host.preview = t.mock.fn();
  const failed = Object.assign(new Error("invalid_response"), {
    code: "invalid_response",
  });
  for (const family of ["ratings", "feedback"]) {
    f.host.opinions = {
      clear() {},
      page: async () => ({
        items: [
          {
            id: selected,
            score: 5,
            category: "problem",
            state: family === "ratings" ? "active" : "open",
          },
        ],
      }),
      get: async (requestedFamily, id) => {
        assert.equal(requestedFamily, family);
        assert.equal(id, selected);
        throw failed;
      },
    };
    picker.mock.resetCalls();
    await assert.rejects(f.host.list(family), { code: "invalid_response" });
    assert.equal(picker.mock.callCount(), 1);
    assert.equal(f.host.preview.mock.callCount(), 0);
  }
});
