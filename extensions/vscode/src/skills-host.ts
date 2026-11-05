import * as vscode from "vscode";
import { supportedPlatform } from "./platform.js";
import { rm } from "node:fs/promises";
import { ClientError } from "./config.js";
import { skillPlan, packageHash, packageChanges } from "./packages.js";
import { acquirePackage, packageTools } from "./acquire.js";
import { SkillStore, receiptSummary, type SkillSummary } from "./install.js";
import type { Browser } from "./browse.js";
import type { CatalogPackageCredentials } from "./package-credentials-host.js";
import type { Session } from "./session.js";
import type {
  CatalogPlugins,
  PluginRow,
  ToolIssueRow,
} from "./plugins-host.js";
import type { CatalogMcp } from "./mcp-host.js";

type Row =
  | { mcp: string }
  | { store: SkillStore; receipt: SkillSummary }
  | PluginRow
  | ToolIssueRow;
export class CatalogSkills implements vscode.Disposable {
  private busy = false;
  private changed = new vscode.EventEmitter<Row | undefined>();
  private disposables: vscode.Disposable[];
  constructor(
    private browser: Browser,
    private session: Session,
    private mcp: CatalogMcp,
    private plugins: CatalogPlugins,
    private preview: (content: string) => Promise<void>,
    private packageCredentials?: CatalogPackageCredentials,
  ) {
    this.disposables = [
      plugins.onChange(() => this.changed.fire(undefined)),
      mcp.onDidChangeSelections(() => this.changed.fire(undefined)),
      vscode.workspace.onDidChangeWorkspaceFolders(() =>
        this.changed.fire(undefined),
      ),
      vscode.window.createTreeView<Row>("openAgenticCatalog.tools", {
        treeDataProvider: {
          onDidChangeTreeData: this.changed.event,
          getChildren: () => this.rows(),
          getTreeItem: (row) => {
            if ("issue" in row) {
              const node = new vscode.TreeItem(
                `${row.kind === "plugin" ? "Plugin" : "Skill"}: ${row.entry ? row.entry + " - " : ""}${row.issue.replaceAll("_", " ")}`,
              );
              node.description = row.workspace;
              node.tooltip = `${row.entry ? `Receipt ${row.entry}. ` : ""}Local files and settings are preserved. Use Refresh after repair, or review the existing recovery command.`;
              node.iconPath = new vscode.ThemeIcon("warning");
              node.contextValue = "catalogToolIssue";
              node.command = {
                command: row.recover
                  ? `openAgenticCatalog.recover${row.kind === "plugin" ? "Plugin" : "Skill"}`
                  : "openAgenticCatalog.help",
                title: "Review local tool recovery",
                arguments: row.recover ? [row.workspace] : [],
              };
              return node;
            }
            if ("mcp" in row) {
              const selection = mcp.selections.selections.get(row.mcp);
              const node = new vscode.TreeItem(
                selection?.recipe.name ?? "MCP selection",
              );
              node.description = "MCP · selected";
              node.contextValue = "catalogMcpSelection";

              node.iconPath = new vscode.ThemeIcon("server");
              return node;
            }
            if ("pluginStore" in row) {
              const node = new vscode.TreeItem(row.receipt.name);
              node.id = JSON.stringify([
                "plugin",
                row.pluginStore.workspace,
                row.receipt.name,
                row.receipt.fingerprint,
              ]);
              node.description =
                "Plugin · " +
                (row.configurationError
                  ? row.configurationError
                  : row.enabled
                    ? "path enabled"
                    : "installed disabled") +
                ` · ${row.pluginStore.workspace}`;
              node.tooltip = `Owned by ${row.pluginStore.workspace}. Native registration uses execution-host machine settings. Select the exact Catalog release to activate or update; use commands to disable or remove offline.`;
              node.contextValue = "catalogInstalledPlugin";
              node.iconPath = new vscode.ThemeIcon("extensions");
              return node;
            }
            const node = new vscode.TreeItem(row.receipt.name);
            node.id = JSON.stringify([
              "skill",
              row.store.workspace,
              row.receipt.name,
              row.receipt.fingerprint,
            ]);
            node.description = `Skill · installed · ${row.store.workspace}`;
            node.tooltip = `Installed in ${row.store.workspace}. Native Skill discovery does not establish execution or trust. Updates require selecting a current complete Catalog package.`;
            node.contextValue = "catalogInstalledSkill";
            node.iconPath = new vscode.ThemeIcon("book");
            return node;
          },
        },
      }),
    ];
  }
  private async operation(action: () => Promise<void>): Promise<void> {
    if (this.busy) throw new ClientError("installation_busy_or_interrupted");
    this.busy = true;
    try {
      await action();
    } finally {
      this.busy = false;
    }
  }
  install(index = 0, path?: string): Promise<void> {
    return this.operation(() => this.installSelected(index, path));
  }
  remove(name?: string, path?: string): Promise<void> {
    return this.operation(() => this.removeSelected(name, path));
  }
  removeConfirmed(
    name: string,
    path: string,
    expected: SkillSummary,
  ): Promise<void> {
    return this.operation(() => this.removeSelected(name, path, expected));
  }
  refresh(): void {
    this.changed.fire(undefined);
  }
  recover(path?: string): Promise<void> {
    return this.operation(() => this.recoverSelected(path));
  }
  private trusted(): void {
    if (!vscode.workspace.isTrusted)
      throw new ClientError("trusted_workspace_required");
    if (!supportedPlatform())
      throw new ClientError("unsupported_package_platform");
  }
  private stores(): SkillStore[] {
    return (vscode.workspace.workspaceFolders ?? [])
      .filter((f) => ["file", "vscode-remote"].includes(f.uri.scheme))
      .slice(0, 20)
      .map((f) => new SkillStore(f.uri.fsPath));
  }
  private async workspace(path?: string): Promise<SkillStore | undefined> {
    this.trusted();
    const stores = this.stores();
    if (!stores.length) throw new ClientError("filesystem_workspace_required");
    if (path) {
      const store = stores.find((s) => s.workspace === path);
      if (!store) throw new ClientError("selection_changed");
      return store;
    }
    const picked = await vscode.window.showQuickPick(
      stores.map((store) => ({ label: store.workspace, store })),
      { title: "Choose the workspace for this Skill" },
    );
    this.trusted();
    return picked?.store;
  }
  async rows(): Promise<Row[]> {
    const result: Row[] = [...this.mcp.selections.selections.keys()].map(
      (mcp) => ({ mcp }),
    );
    for (const store of this.stores()) {
      try {
        const scanned = await store.scan();
        for (const receipt of scanned.items) result.push({ store, receipt });
        for (const issue of scanned.issues)
          result.push({
            issue: issue.code,
            entry: issue.entry,
            workspace: store.workspace,
            kind: "skill",
            recover: true,
          });
      } catch (error) {
        result.push({
          issue:
            error instanceof ClientError
              ? error.code
              : "local_tools_unavailable",
          workspace: store.workspace,
          kind: "skill",
          recover: true,
        });
      }
    }
    const admitted = new Set(this.stores().map((store) => store.workspace));
    for (const folder of vscode.workspace.workspaceFolders ?? [])
      if (!admitted.has(folder.uri.fsPath))
        result.push({
          issue: ["file", "vscode-remote"].includes(folder.uri.scheme)
            ? "workspace_root_limit"
            : "filesystem_workspace_required",
          workspace: folder.name,
          kind: "skill",
        });
    result.push(...(await this.plugins.rows()));
    return result;
  }
  private async installSelected(index = 0, path?: string): Promise<void> {
    this.trusted();
    const detail = this.browser.state.detail,
      read = this.browser.packageConsumer(index);
    const guard = this.session.guard(this.session.connection());
    const check = () => {
      this.trusted();
      guard();
      if (this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
    };
    const binding = packageHash(this.session.consumerBinding());
    const plan = skillPlan(await read(), binding);
    check();
    plan.identity = packageHash(
      JSON.stringify([plan.identity, detail!.item.releaseId]),
    );
    const store = await this.workspace(path);
    check();
    if (!store) return;
    const status = await store.inspect(plan);
    check();
    if (status.identical) {
      void vscode.window.showInformationMessage(
        "This exact Skill is already installed. No download or write was needed.",
      );
      return;
    }
    const configuration =
      vscode.workspace.getConfiguration("openAgenticCatalog");
    const inspected = configuration.inspect<unknown>("packageTools");
    if (
      inspected?.workspaceValue !== undefined ||
      inspected?.workspaceFolderValue !== undefined ||
      inspected?.workspaceLanguageValue !== undefined ||
      inspected?.workspaceFolderLanguageValue !== undefined
    )
      throw new ClientError("workspace_settings_refused");
    const tools = packageTools(configuration.get<unknown>("packageTools", {}));
    const source =
      plan.sources.length === 1
        ? plan.sources[0]
        : (
            await vscode.window.showQuickPick(
              plan.sources.map((source) => ({
                label:
                  source.type === "git"
                    ? source.repository
                    : source.type === "oci"
                      ? source.reference
                      : source.url,
                source,
              })),
              { title: "Choose an immutable package source" },
            )
          )?.source;
    check();
    if (!source) return;
    const changes = packageChanges(status.previous?.files ?? [], plan.files);
    const summary = `Workspace: ${store.workspace}\nDestination: .agents/skills/${plan.name}/\nSource: ${JSON.stringify(source)}\nDeclared license: ${detail!.facts.find((f) => f.label === "Declared license")?.value ?? "not supplied"}\nFiles: ${plan.files.length}; expanded bytes: ${plan.files.reduce((n, f) => n + f.size, 0)}.\n${status.previous ? "This update briefly removes the old directory during the two-rename switch. Local edits block replacement." : "VS Code can discover these instructions immediately after installation."}\nGit/ORAS uses separate package TLS trust and explicit source access, with no Catalog credentials. Scripts are copied, never run. Installation is not a safety approval.`;
    const preview = [
      summary,
      "",
      "Complete inventory:",
      ...plan.files.map(
        (f) =>
          `${f.path} ${f.size} bytes ${f.executable ? "executable" : "regular"} ${f.digest}`,
      ),
      "",
      "Changes:",
      ...changes,
    ].join("\n");
    // Reuse the read-only, session-bound text provider. Never render instructions as Markdown.
    await this.preview(preview);
    check();
    if (
      (await vscode.window.showWarningMessage(
        `Install ${plan.name} in the selected workspace?`,
        { modal: true, detail: summary },
        "Install Skill",
      )) !== "Install Skill"
    )
      return;
    check();
    await read();
    check();
    if (!this.stores().some((s) => s.workspace === store.workspace))
      throw new ClientError("selection_changed");
    const work = await store.temporary();
    try {
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Verify and install Catalog Skill",
          cancellable: true,
        },
        async (_, token) => {
          const cancelled = new AbortController(),
            listener = token.onCancellationRequested(() => cancelled.abort());
          if (token.isCancellationRequested) cancelled.abort();
          const signal = AbortSignal.any([
            cancelled.signal,
            this.session.cancellation(),
          ]);
          try {
            const content = await acquirePackage(
              plan,
              source,
              work,
              tools,
              signal,
              await this.packageCredentials?.select(source),
            );
            check();
            await store.install(
              plan,
              content,
              async () => {
                if (signal.aborted) throw new ClientError("package_cancelled");
                await read();
                check();
                if (signal.aborted) throw new ClientError("package_cancelled");
                if (!this.stores().some((s) => s.workspace === store.workspace))
                  throw new ClientError("selection_changed");
              },
              () => {
                check();
                if (signal.aborted) throw new ClientError("package_cancelled");
                if (!this.stores().some((s) => s.workspace === store.workspace))
                  throw new ClientError("selection_changed");
              },
            );
          } finally {
            listener.dispose();
          }
        },
      );
    } finally {
      await rm(work, { recursive: true, force: true });
      this.changed.fire(undefined);
    }
    void vscode.window.showInformationMessage(
      "Skill installed. Open Chat: Open Customizations and select Skills to inspect native discovery. Package code was not executed.",
    );
  }
  private async removeSelected(
    name?: string,
    path?: string,
    expected?: SkillSummary,
  ): Promise<void> {
    this.trusted();
    const rows = (await this.rows()).filter(
      (r): r is Extract<Row, { store: SkillStore }> => "store" in r,
    );
    const picked =
      name && path
        ? rows.find(
            (r) => r.receipt.name === name && r.store.workspace === path,
          )
        : (
            await vscode.window.showQuickPick(
              rows.map((row) => ({
                label: row.receipt.name,
                description: row.store.workspace,
                row,
              })),
              { title: "Remove an owned Skill (no Catalog connection needed)" },
            )
          )?.row;
    if (!picked) {
      if (name || path) throw new ClientError("owned_package_required");
      return;
    }
    const confirmed = await picked.store.get(picked.receipt.name);
    if (
      !confirmed ||
      receiptSummary(confirmed).fingerprint !==
        (expected ?? picked.receipt).fingerprint
    )
      throw new ClientError("selection_changed");
    if (
      (await vscode.window.showWarningMessage(
        `Remove ${picked.receipt.name} from ${picked.store.workspace}? Local edits prevent removal.`,
        { modal: true },
        "Remove Skill",
      )) !== "Remove Skill"
    )
      return;
    this.trusted();
    if (!this.stores().some((s) => s.workspace === picked.store.workspace))
      throw new ClientError("selection_changed");
    await picked.store.remove(
      picked.receipt.name,
      () => {
        this.trusted();
        if (!this.stores().some((s) => s.workspace === picked.store.workspace))
          throw new ClientError("selection_changed");
      },
      confirmed,
    );
    this.changed.fire(undefined);
  }
  async reveal(row: Row): Promise<void> {
    if (!("store" in row) && !("pluginStore" in row))
      throw new ClientError("owned_package_required");
    this.trusted();
    const workspace =
      "store" in row ? row.store.workspace : row.pluginStore.workspace;
    if (!this.stores().some((s) => s.workspace === workspace))
      throw new ClientError("selection_changed");
    const store = new SkillStore(
      workspace,
      "store" in row ? "skill" : "plugin",
    );
    const confirmed = await store.get(row.receipt.name);
    if (
      !confirmed ||
      receiptSummary(confirmed).fingerprint !== row.receipt.fingerprint
    )
      throw new ClientError("selection_changed");
    const path = await store.ownedPath(confirmed);
    this.trusted();
    const folder = vscode.workspace.workspaceFolders?.find(
      (f) =>
        f.uri.fsPath === store.workspace &&
        ["file", "vscode-remote"].includes(f.uri.scheme),
    );
    if (!folder) throw new ClientError("selection_changed");
    const relative = path.slice(store.workspace.length + 1).split(/[\\/]/u);
    await vscode.commands.executeCommand(
      "revealInExplorer",
      vscode.Uri.joinPath(folder.uri, ...relative),
    );
  }
  compare(index = 0, path?: string): Promise<void> {
    return this.operation(async () => {
      const detail = this.browser.state.detail,
        guard = this.session.guard(this.session.connection());
      this.trusted();
      const plan = skillPlan(
        await this.browser.packageConsumer(index)(),
        packageHash(this.session.consumerBinding()),
      );
      plan.identity = packageHash(
        JSON.stringify([plan.identity, detail!.item.releaseId]),
      );
      const store = await this.workspace(path);
      guard();
      if (this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
      if (!store) return;
      let text: string;
      try {
        const status = await store.inspect(plan);
        text = [
          `Skill ${plan.name}`,
          `Workspace: ${store.workspace}`,
          status.identical
            ? "The selected exact package is installed."
            : status.previous
              ? "The selected package differs from the installed inventory."
              : "No owned installation exists.",
          ...packageChanges(status.previous?.files ?? [], plan.files),
        ].join("\n");
      } catch (error) {
        if (!(error instanceof ClientError)) throw error;
        text = `Comparison refused: ${error.code}. Local changes are not overwritten. No local edit paths or file-content diff is inferred.`;
      }
      guard();
      this.trusted();
      if (
        this.browser.state.detail !== detail ||
        !this.stores().some((s) => s.workspace === store.workspace)
      )
        throw new ClientError("selection_changed");
      await this.preview(
        text +
          "\nRead-only inventory comparison: no acquisition, filesystem write or execution.",
      );
    });
  }
  private async recoverSelected(path?: string): Promise<void> {
    const store = await this.workspace(path);
    if (!store) return;
    if (
      (await vscode.window.showWarningMessage(
        "Recover an interrupted Skill operation? First close every other Catalog installation window for this workspace. Unexpected local changes will not be deleted.",
        { modal: true },
        "Recover Skill operation",
      )) !== "Recover Skill operation"
    )
      return;
    const guard = () => {
      this.trusted();
      if (!this.stores().some((s) => s.workspace === store.workspace))
        throw new ClientError("selection_changed");
    };
    guard();
    await store.recover(guard);
    this.changed.fire(undefined);
  }
  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.changed.dispose();
  }
}
