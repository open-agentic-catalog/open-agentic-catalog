import * as vscode from "vscode";
import { nativeEnvironmentAllowed } from "./platform.js";
import { ClientError, diagnostic, abortable } from "./config.js";
import { McpSelections, inputValue, type McpRecipe } from "./mcp.js";
import { McpReferences, type McpReference } from "./mcp-references.js";
import { mcpDocument } from "./mcp-document.js";
import type { Browser } from "./browse.js";
import type { Session } from "./session.js";

const providerID = "openAgenticCatalog.selectedMcp";
type Definition =
  vscode.McpHttpServerDefinition | vscode.McpStdioServerDefinition;
export class CatalogMcp implements vscode.Disposable {
  private references: McpReferences;
  private changed = new vscode.EventEmitter<void>();
  readonly onDidChangeSelections = this.changed.event;
  readonly selections: McpSelections;
  private definitions = new WeakMap<Definition, string>();
  private disposables: vscode.Disposable[];
  constructor(
    context: vscode.ExtensionContext,
    private browser: Browser,
    private session: Session,
  ) {
    this.references = new McpReferences(context.globalState);
    this.selections = new McpSelections({
      trusted: () => this.trusted(),
      acquire: (ref, signal) =>
        mcpDocument(
          ref,
          session.connection(),
          async (url) => {
            const check = session.guard(session.connection());
            const yes = await vscode.window.showWarningMessage(
              `Read the external native document at ${url}? No Catalog credentials or private CA are forwarded. Redirects are refused.`,
              { modal: true },
              "Read document",
            );
            check();
            return yes === "Read document";
          },
          undefined,
          AbortSignal.any([
            session.cancellation(),
            ...(signal ? [signal] : []),
          ]),
        ),
      confirm: (recipe, signal) =>
        abortable(this.confirm(recipe), signal, "mcp_start_cancelled"),
      input: async (input, previous, signal) =>
        abortable(
          vscode.window.showInputBox({
            title: `MCP target: ${input.name}`,
            prompt: `${input.required ? "Required" : "Optional"} ${input.secret ? "target credential (separate from Catalog login)" : input.format}. Empty skips an optional value.`,
            password: input.secret,
            ignoreFocusOut: true,
            ...(previous === undefined ? {} : { value: previous }),
            validateInput: (value) => {
              try {
                inputValue(input, value);
                return undefined;
              } catch {
                return "Enter a valid value for the declared input.";
              }
            },
          }),
          signal,
          "mcp_start_cancelled",
        ),
      secrets: context.secrets,
      changed: () => {
        this.changed.fire();
      },
    });
    this.disposables = [
      session.onChange(() => this.selections.clear()),
      vscode.lm.registerMcpServerDefinitionProvider(providerID, {
        onDidChangeMcpServerDefinitions: this.changed.event,
        provideMcpServerDefinitions: () => {
          if (!vscode.workspace.isTrusted) return [];
          return [...this.selections.selections.values()].map((selection) => {
            const definition = this.definition(
              selection.id,
              selection.recipe,
              {},
            );
            this.definitions.set(definition, selection.id);
            return definition;
          });
        },
        resolveMcpServerDefinition: async (definition, token) => {
          // The qualified editor supplies CancellationToken.None to providers.
          // Its own progress token is cancellable; also honor provider tokens.
          return vscode.window.withProgress(
            {
              location: vscode.ProgressLocation.Notification,
              title: "Preparing Catalog MCP server",
              cancellable: true,
            },
            async (_, progressToken) => {
              const cancelled = new AbortController();
              const listeners = [token, progressToken].map((value) =>
                value.onCancellationRequested(() => cancelled.abort()),
              );
              if (
                token.isCancellationRequested ||
                progressToken.isCancellationRequested
              )
                cancelled.abort();
              try {
                const id = this.definitions.get(definition);
                if (!id) throw new ClientError("selection_changed");
                if (cancelled.signal.aborted)
                  throw new ClientError("mcp_start_cancelled");
                const { recipe, values } = await this.selections.resolve(
                  id,
                  () => {
                    if (cancelled.signal.aborted)
                      throw new ClientError("mcp_start_cancelled");
                  },
                  AbortSignal.any([cancelled.signal, session.cancellation()]),
                );
                if (cancelled.signal.aborted)
                  throw new ClientError("mcp_start_cancelled");
                return this.definition(id, recipe, values);
              } catch (error) {
                // Never throw a dependency error into the editor's MCP logs.
                void vscode.window.showErrorMessage(diagnostic(error));
                return undefined;
              } finally {
                for (const listener of listeners) listener.dispose();
              }
            },
          );
        },
      }),
    ];
  }
  private definition(
    id: string,
    recipe: McpRecipe,
    values: Record<string, string>,
  ): Definition {
    const label = `Catalog ${recipe.name} [${id.slice(0, 8)}]`;
    if (recipe.type === "http")
      return new vscode.McpHttpServerDefinition(
        label,
        vscode.Uri.parse(recipe.url!),
        values,
        id,
      );
    // Child processes receive a small conventional environment, not host credentials.
    const env: Record<string, string | null> = Object.create(null) as Record<
      string,
      string | null
    >;
    for (const key of Object.keys(process.env))
      env[key] = nativeEnvironmentAllowed(key)
        ? (process.env[key] ?? null)
        : null;
    for (const input of recipe.inputs)
      env[input.name] = values[input.name] ?? null;
    const definition = new vscode.McpStdioServerDefinition(
      label,
      recipe.command!,
      recipe.args,
      env,
      id,
    );
    if (vscode.workspace.workspaceFolders?.length === 1)
      definition.cwd = vscode.workspace.workspaceFolders[0]!.uri;
    return definition;
  }
  private async confirm(recipe: McpRecipe): Promise<boolean> {
    return (
      (await vscode.window.showWarningMessage(
        `Use ${recipe.name} (${recipe.version}) from ${recipe.family}?`,
        {
          modal: true,
          detail: `${recipe.description}\nExecution host: ${vscode.env.remoteName ?? "local"}.\nInputs: ${recipe.inputs.map((i) => `${i.name}${i.secret ? " (secret)" : ""}`).join(", ") || "none"}.\n${recipe.type === "http" ? "VS Code owns target TLS and OAuth. Catalog trust and tokens do not apply." : "Requires the named runner on this host. This can download and execute code with your user permissions. No runtime is installed."}\nCatalog publication is not a runtime safety approval. Native MCP and tool approvals still apply.`,
        },
        "Use server",
      )) === "Use server"
    );
  }
  async select(index: number, expected?: McpReference): Promise<void> {
    const connection = this.session.connection(),
      check = this.session.guard(connection),
      detail = this.browser.state.detail;
    if (!detail) throw new ClientError("selection_changed");
    let selecting = true;
    const guard = () => {
      check();
      if (!this.trusted()) throw new ClientError("workspace_trust_required");
      if (selecting && this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
    };
    const read = this.browser.consumer(index, guard);
    // The browser can move after registration. Only prompts during selection bind its view.
    try {
      const selected = await this.selections.add(
        read,
        guard,
        async (values) => {
          guard();
          if (expected) return expected.alternative;
          const picked = await vscode.window.showQuickPick(
            values.map((v, index) => ({
              label: v.label,
              description: v.recipe?.description ?? v.reason ?? "Unavailable",
              index,
            })),
            { title: "Choose one declared MCP connection or package" },
          );
          guard();
          return picked?.index;
        },
        connection.allowLoopbackHTTP,
        JSON.stringify([
          this.session.consumerBinding(),
          detail.item.origin,
          detail.item.key,
        ]),
        expected,
      );
      if (selected) {
        const mode = connection.mode;
        const item = detail.item;
        const reference =
          mode === "control" && ["local", "indexed"].includes(item.origin)
            ? JSON.stringify({
                origin: item.origin,
                identifier: item.identifier,
                versionPresent: item.versionPresent,
                ...(item.versionPresent ? { version: item.version } : {}),
                ...(item.releaseId ? { releaseId: item.releaseId } : {}),
              })
            : "fresh-search-required";
        await this.references.add(
          this.session.consumerBinding(),
          {
            reference,
            mode:
              reference === "fresh-search-required" ? "published" : "control",
            document: index,
            alternative: selected.alternative,
            digest: selected.digest,
          },
          guard,
        );
        guard();
      }
    } finally {
      selecting = false;
    }
  }
  async saved(): Promise<void> {
    await this.session.test();
    const check = this.session.guard(this.session.connection()),
      account = this.session.consumerBinding();
    const values = this.references.list(account);
    const selected = await vscode.window.showQuickPick(
      values.map((value) => ({
        label:
          value.mode === "control"
            ? value.reference
            : "Fresh search required (Published/live)",
        description: `Alternative ${value.alternative + 1} · ${value.digest.slice(0, 12)}`,
        value,
      })),
      {
        title: "Saved inert MCP references (this host, connection and account)",
      },
    );
    check();
    if (!selected) return;
    const action = await vscode.window.showQuickPick(
      [
        { label: "Reselect with fresh access and consent", value: "reselect" },
        { label: "Remove saved reference", value: "remove" },
      ],
      { title: "Saved MCP reference" },
    );
    check();
    if (!action) return;
    if (action.value === "remove") {
      await this.references.remove(account, selected.value, check);
      return;
    }
    if (
      selected.value.mode !== "control" ||
      this.session.connection().mode !== "control"
    ) {
      void vscode.window.showInformationMessage(
        "This reference cannot be resolved exactly without its old query. Search explicitly and select the desired declaration again. No private query or cursor was saved.",
      );
      return;
    }
    await this.browser.openReference(selected.value.reference);
    check();
    await this.select(selected.value.document, selected.value);
  }
  async forgetReferences(): Promise<void> {
    if (
      (await vscode.window.showWarningMessage(
        "Forget all inert MCP references on this execution host? This does not remove active definitions, target credentials or running servers. Close other Catalog windows first.",
        { modal: true },
        "Forget MCP references",
      )) !== "Forget MCP references"
    )
      return;
    await this.references.forget();
  }
  private trusted(): boolean {
    return (
      vscode.workspace.isTrusted &&
      !vscode.workspace.workspaceFolders?.some(
        (folder) => !["file", "vscode-remote"].includes(folder.uri.scheme),
      )
    );
  }
  async remove(id?: string): Promise<void> {
    if (!id)
      id = (
        await vscode.window.showQuickPick(
          [...this.selections.selections.values()]
            .map((s) => ({
              label: s.recipe.name,
              id: s.id,
            }))
            .concat(
              this.selections
                .pendingRemovals()
                .filter((id) => !this.selections.selections.has(id))
                .map((id) => ({
                  label: "Retry incomplete MCP credential removal",
                  id,
                })),
            ),
          { title: "Remove an owned MCP selection" },
        )
      )?.id;
    if (!id) return;
    await this.selections.remove(id);
    void vscode.window.showInformationMessage(
      "Selection removed. Use MCP: List Servers to stop a running server. Removal does not confirm remote shutdown.",
    );
  }
  async forget(): Promise<void> {
    if (
      (await vscode.window.showWarningMessage(
        "Forget all MCP target credentials stored by this extension? Close other Catalog windows first: they can retain or save credentials again. Catalog login and Agent credentials remain separate. This does not revoke credentials or stop running servers.",
        { modal: true },
        "Other windows closed: forget",
      )) !== "Other windows closed: forget"
    )
      return;
    for (;;) {
      const result = await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Forgetting MCP target credentials",
          cancellable: true,
        },
        async (_, token) =>
          this.selections.forget(() => token.isCancellationRequested),
      );
      if (!result.remaining) {
        void vscode.window.showInformationMessage(
          `Forgot ${result.deleted} MCP credential entries in this batch.`,
        );
        return;
      }
      if (
        (await vscode.window.showWarningMessage(
          `${result.deleted} entries deleted; ${result.remaining} remain, including ${result.failed} failed deletions. Continue with another batch of up to 50?`,
          "Continue",
          "Stop",
        )) !== "Continue"
      )
        return;
    }
  }
  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.selections.clear();
    this.changed.dispose();
  }
}
