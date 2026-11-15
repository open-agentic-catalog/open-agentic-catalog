import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";

// Exercise the real view owner with inert editor tabs and content providers.
class EventEmitter {
  listeners = [];
  event = (listener) => {
    this.listeners.push(listener);
    return { dispose() {} };
  };
  fire(value) {
    for (const listener of this.listeners) listener(value);
  }
  dispose() {}
}
class TabInputText {
  constructor(uri) {
    this.uri = uri;
  }
}
class TabInputTextDiff {
  constructor(original, modified) {
    this.original = original;
    this.modified = modified;
  }
}
const closed = new EventEmitter();
const changed = new EventEmitter();
let provider;
const redacted = [];
const vscode = {
  EventEmitter,
  TabInputText,
  TabInputTextDiff,
  Uri: {
    parse: (value) => ({
      scheme: new URL(value).protocol.slice(0, -1),
      toString: () => value,
    }),
  },
  window: {
    createTreeView: () => ({ dispose() {} }),
    tabGroups: { all: [], onDidChangeTabs: closed.event },
    showTextDocument: async () => undefined,
  },
  workspace: {
    onDidChangeWorkspaceFolders: () => ({ dispose() {} }),
    registerTextDocumentContentProvider: (_, value) => {
      provider = value;
      value.onDidChange((uri) => redacted.push(uri.toString()));
      return { dispose() {} };
    },
    openTextDocument: async (uri) => ({ uri }),
  },
  languages: { setTextDocumentLanguage: async (document) => document },
};
globalThis.catalogViewsFixture = vscode;
const hooks = registerHooks({
  resolve(specifier, context, next) {
    if (specifier === "vscode")
      return { url: "catalog-unit:vscode", shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === "catalog-unit:vscode")
      return {
        format: "module",
        source:
          "export const {EventEmitter, TabInputText, TabInputTextDiff, Uri, window, workspace, languages} = globalThis.catalogViewsFixture;",
        shortCircuit: true,
      };
    return next(url, context);
  },
});
const { CatalogViews } = await import(
  new URL("views.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);

test("evidence follows text and diff tab references and redacts on session change", async (t) => {
  const views = new CatalogViews(
    {},
    { onChange: () => ({ dispose() {} }) },
    {
      onChange: changed.event,
      guard: () => () => undefined,
      connection: () => ({}),
    },
  );
  t.after(() => {
    views.dispose();
    hooks.deregister();
    delete globalThis.catalogViewsFixture;
  });
  const uris = [];
  const open = vscode.workspace.openTextDocument;
  vscode.workspace.openTextDocument = async (uri) => {
    uris.push(uri);
    return open(uri);
  };
  await views.preview("before", "before", "json");
  await views.preview("after", "after", "json");
  const [before, after] = uris;
  const text = { input: new TabInputText(before) };
  const diff = { input: new TabInputTextDiff(before, after) };
  const other = {
    input: new TabInputTextDiff(before, vscode.Uri.parse("file:/other")),
  };
  vscode.window.tabGroups.all = [{ tabs: [diff, other] }];
  closed.fire({ closed: [text] });
  assert.equal(provider.provideTextDocumentContent(before), "before");
  assert.equal(provider.provideTextDocumentContent(after), "after");
  assert.deepEqual(redacted, []);
  // Closing one diff releases only the side with no remaining references.
  vscode.window.tabGroups.all = [{ tabs: [other] }];
  closed.fire({ closed: [diff] });
  assert.equal(provider.provideTextDocumentContent(before), "before");
  assert.match(
    provider.provideTextDocumentContent(after),
    /no longer available/u,
  );
  assert.deepEqual(redacted, [after.toString()]);
  changed.fire();
  assert.match(
    provider.provideTextDocumentContent(before),
    /Catalog session changed/u,
  );
  vscode.window.tabGroups.all = [];
  closed.fire({ closed: [other] });
  assert.match(
    provider.provideTextDocumentContent(before),
    /no longer available/u,
  );
  assert.deepEqual(redacted, [
    after.toString(),
    before.toString(),
    before.toString(),
  ]);
  // Released references free the finite view budget.
  for (let index = 0; index < 16; index++)
    await views.preview("next", "next", "json");
  await assert.rejects(
    views.preview("overflow", "overflow", "json"),
    /close_evidence_documents/u,
  );
});
