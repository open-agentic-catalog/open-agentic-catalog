import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";

// Only the native editor boundary is substituted. Both selections load the compiled host.
export const window = {};
let registered;
const disposable = () => ({ dispose() {} });
globalThis.savedMcpEditor = {
  window,
  workspace: { isTrusted: true },
  env: {},
  Uri: { parse: (value) => new URL(value) },
  EventEmitter: class {
    event = disposable;
    fire() {}
    dispose() {}
  },
  CancellationTokenSource: class {
    controller = new AbortController();
    token = {
      onCancellationRequested: (listener) => {
        this.controller.signal.addEventListener("abort", listener);
        return {
          dispose: () =>
            this.controller.signal.removeEventListener("abort", listener),
        };
      },
    };
    cancel() {
      this.controller.abort();
    }
    dispose() {}
  },
  McpHttpServerDefinition: class {
    constructor(label, uri, headers, version) {
      Object.assign(this, { label, uri, headers, version });
    }
  },
  lm: {
    registerMcpServerDefinitionProvider(_id, provider) {
      registered = provider;
      return disposable();
    },
  },
};
const hook = registerHooks({
  resolve(name, context, next) {
    return name === "vscode"
      ? { url: "fixture:saved-mcp-editor", shortCircuit: true }
      : next(name, context);
  },
  load(url, context, next) {
    return url === "fixture:saved-mcp-editor"
      ? {
          format: "module",
          shortCircuit: true,
          source:
            "export const {window,workspace,env,Uri,EventEmitter,CancellationTokenSource,McpHttpServerDefinition,lm}=globalThis.savedMcpEditor;",
        }
      : next(url, context);
  },
});
export const { CatalogMcp } = await import(
  new URL("./mcp-host.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);
hook.deregister();
export const definitions = () => registered.provideMcpServerDefinitions();
