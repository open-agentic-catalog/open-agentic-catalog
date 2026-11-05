import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";

let steps = [],
  messages = [],
  cancelled = false;
const hooks = registerHooks({
  resolve(specifier, context, next) {
    return specifier === "vscode"
      ? { url: "package-credential-unit:vscode", shortCircuit: true }
      : next(specifier, context);
  },
  load(url, context, next) {
    return url === "package-credential-unit:vscode"
      ? {
          format: "module",
          source:
            "export const {window,ProgressLocation}=globalThis.packageCredentialNativeFixture;",
          shortCircuit: true,
        }
      : next(url, context);
  },
});
function answer(kind, value, before) {
  return (actual, items, options) => {
    assert.equal(actual, kind);
    before?.(items, options);
    return typeof value === "function" ? value(items, options) : value;
  };
}
function prompt(kind, items, options) {
  const step = steps.shift();
  assert.ok(step, `Unexpected native ${kind} prompt`);
  return step(kind, items, options);
}
globalThis.packageCredentialNativeFixture = {
  ProgressLocation: { Notification: 1 },
  window: {
    showQuickPick: async (items, options) => prompt("pick", items, options),
    showInputBox: async (options) => prompt("input", undefined, options),
    showWarningMessage: async (message, ...items) => {
      messages.push(message);
      return prompt("warning", items, { message });
    },
    showInformationMessage: (message) => messages.push(message),
    withProgress: async (options, work) => {
      assert.equal(options.cancellable, true);
      return work({}, { isCancellationRequested: cancelled });
    },
  },
};
const { CatalogPackageCredentials } = await import(
  new URL(
    "package-credentials-host.js",
    pathToFileURL(process.env.CATALOG_VSCODE_LIB),
  )
);
const { PackageCredentials } = await import(process.env.CATALOG_VSCODE_LIB);
const account = "a".repeat(64);
const source = { type: "git", repository: "https://packages.owned.test/repo" };
const credential = {
  type: "git",
  origin: "https://packages.owned.test",
  scheme: "basic",
  username: "package-reader",
  token: "private-source-sentinel",
};
function fixture() {
  steps = [];
  messages = [];
  cancelled = false;
  let valid = true,
    tests = 0;
  const saved = new Map();
  const secrets = {
    keys: async () => [...saved.keys()],
    get: async (key) => saved.get(key),
    store: async (key, value) => {
      saved.set(key, value);
    },
    delete: async (key) => {
      saved.delete(key);
    },
  };
  const session = {
    test: async () => {
      tests++;
    },
    connection: () => ({}),
    consumerBinding: () => account,
    guard: () => () => {
      if (!valid)
        throw Object.assign(new Error("connection_changed"), {
          code: "connection_changed",
        });
    },
  };
  return {
    host: new CatalogPackageCredentials(secrets, session),
    credentials: new PackageCredentials(secrets),
    saved,
    invalidate() {
      valid = false;
    },
    get tests() {
      return tests;
    },
  };
}
function manageSteps(token = credential.token, type = "git") {
  return [
    answer("pick", (items) => items.find((item) => item.type === type)),
    answer("input", credential.origin),
    answer("pick", "Add or replace"),
    ...(type === "https"
      ? [answer("pick", "Bearer")]
      : [answer("input", credential.username)]),
    answer("input", token, (_, options) => {
      assert.equal(options.password, true);
      assert.equal(options.ignoreFocusOut, true);
      assert.match(options.prompt, /Never use your Catalog access token/u);
      assert.ok(options.prompt.includes(credential.origin));
    }),
  ];
}
function finished() {
  assert.deepEqual(steps, []);
  assert.doesNotMatch(messages.join("\n"), /private-source-sentinel/u);
}
test("native package prompts save and replace Basic credentials and support HTTPS Bearer without a username", async () => {
  const f = fixture();
  for (const token of [credential.token, "replacement-source-sentinel"]) {
    steps = manageSteps(token);
    await f.host.manage();
    finished();
    assert.deepEqual(await f.credentials.get(account, source), {
      ...credential,
      token,
    });
    assert.equal(f.saved.size, 1);
  }
  steps = manageSteps("private-source-sentinel", "https");
  await f.host.manage();
  finished();
  assert.deepEqual(
    await f.credentials.get(account, {
      type: "https",
      url: credential.origin + "/archive",
    }),
    {
      type: "https",
      origin: credential.origin,
      scheme: "bearer",
      token: credential.token,
    },
  );
  assert.deepEqual(await f.credentials.get(account, source), {
    ...credential,
    token: "replacement-source-sentinel",
  });
});
test("canceling any native credential prompt leaves the previous secret unchanged", async () => {
  for (const type of ["git", "https"]) {
    for (let at = 0; at < manageSteps(undefined, type).length; at++) {
      const f = fixture();
      await f.credentials.save(account, credential);
      const before = [...f.saved];
      steps = manageSteps(undefined, type).slice(0, at + 1);
      const original = steps[at];
      steps[at] = (...args) => {
        original(...args);
        return undefined;
      };
      await f.host.manage();
      finished();
      assert.deepEqual([...f.saved], before);
    }
  }
});
test("an account change at any native credential prompt refuses writes to either account", async () => {
  for (let at = 0; at < manageSteps().length; at++) {
    const f = fixture();
    steps = manageSteps().slice(0, at + 1);
    const original = steps[at];
    steps[at] = (...args) => {
      f.invalidate();
      return original(...args);
    };
    await assert.rejects(f.host.manage(), /connection_changed/u);
    finished();
    assert.equal(f.saved.size, 0);
  }
});
test("invalid origins are refused before a username or secret is requested", async () => {
  for (const origin of [
    "http://packages.owned.test",
    "https://packages.owned.test/path",
    "https://user:secret@packages.owned.test",
    "relative",
  ]) {
    const f = fixture();
    steps = [manageSteps()[0], answer("input", origin)];
    await assert.rejects(f.host.manage());
    finished();
    assert.equal(f.saved.size, 0);
  }
});
test("saved package credentials require explicit native selection and never cross a changed account", async () => {
  const f = fixture();
  assert.equal(
    await f.host.select(source),
    undefined,
    "No credential means no prompt",
  );
  await f.credentials.save(account, credential);
  for (const choice of [true, false, undefined]) {
    steps = [
      answer("pick", (items, options) => {
        assert.match(options.title, /separate from Catalog login/u);
        assert.doesNotMatch(JSON.stringify(items), /private-source-sentinel/u);
        return items.find((item) => item.value === choice);
      }),
    ];
    if (choice === undefined)
      await assert.rejects(f.host.select(source), /package_cancelled/u);
    else
      assert.deepEqual(
        await f.host.select(source),
        choice ? credential : undefined,
      );
    finished();
  }
  steps = [
    answer(
      "pick",
      (items) => items[0],
      () => f.invalidate(),
    ),
  ];
  await assert.rejects(f.host.select(source), /connection_changed/u);
  finished();
});
test("native Forget removes only the selected package credential", async () => {
  const f = fixture();
  await f.credentials.save(account, credential);
  f.saved.set("catalog-login", "separate-sentinel");
  steps = [...manageSteps().slice(0, 2), answer("pick", "Forget")];
  await f.host.manage();
  finished();
  assert.deepEqual([...f.saved], [["catalog-login", "separate-sentinel"]]);
  assert.match(messages.at(-1), /does not revoke/u);
});
test("offline native Forget All requires confirmation, respects Stop/cancel and can finish multiple bounded batches", async () => {
  for (const mode of ["decline", "stop", "cancel", "continue"]) {
    const f = fixture();
    for (let i = 0; i < 51; i++)
      f.saved.set(
        `openAgenticCatalog.package:${account}:${i.toString(16).padStart(64, "0")}`,
        "inert-secret",
      );
    f.saved.set("openAgenticCatalog.token:unrelated", "preserve-login");
    steps = [
      answer(
        "warning",
        mode === "decline" ? undefined : "Forget package credentials",
        (_, options) => {
          assert.match(options.message, /Close other Catalog windows/u);
          assert.match(options.message, /does not revoke/u);
        },
      ),
    ];
    cancelled = mode === "cancel";
    if (mode !== "decline")
      steps.push(answer("warning", mode === "continue" ? "Continue" : "Stop"));
    await f.host.forgetAll();
    finished();
    assert.equal(
      f.tests,
      0,
      "Offline removal must not require Catalog login or access",
    );
    assert.equal(
      f.saved.get("openAgenticCatalog.token:unrelated"),
      "preserve-login",
    );
    assert.equal(
      f.saved.size,
      mode === "continue" ? 1 : mode === "stop" ? 2 : 52,
    );
  }
});
process.on("exit", () => {
  hooks.deregister();
  delete globalThis.packageCredentialNativeFixture;
});
