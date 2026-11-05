import * as vscode from "vscode";
import { supportedPlatform, sameNativePath } from "./platform.js";
import { join, resolve, sep } from "node:path";
import { homedir } from "node:os";
import { rm } from "node:fs/promises";
import { ClientError } from "./config.js";
import {
  SkillStore,
  receiptSummary,
  type SkillSummary,
  type SkillReceipt,
} from "./install.js";
import {
  PluginLocations,
  locationMap,
  pluginName,
  pluginPlan,
  pluginPreview,
} from "./plugins.js";
import { packageHash, packageChanges } from "./packages.js";
import { acquirePackage, packageTools } from "./acquire.js";
import type { Browser } from "./browse.js";
import type { CatalogPackageCredentials } from "./package-credentials-host.js";
import type { Session } from "./session.js";
export interface PluginRow {
  pluginStore: SkillStore;
  receipt: SkillSummary;
  enabled?: boolean;
  configurationError?: string;
}
export interface ToolIssueRow {
  issue: string;
  workspace: string;
  kind: "skill" | "plugin";
  entry?: string;
  recover?: boolean;
}
export class CatalogPlugins implements vscode.Disposable {
  private busy = false;
  private changed = new vscode.EventEmitter<void>();
  readonly onChange = this.changed.event;
  private configurationChanged = vscode.workspace.onDidChangeConfiguration(
    (event) => {
      if (
        event.affectsConfiguration("chat.pluginLocations") ||
        event.affectsConfiguration("chat.plugins")
      )
        this.changed.fire();
    },
  );
  constructor(
    private browser: Browser,
    private session: Session,
    private preview: (content: string) => Promise<void>,
    private packageCredentials?: CatalogPackageCredentials,
  ) {}
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
      .map((f) => new SkillStore(f.uri.fsPath, "plugin"));
  }
  private guard(store: SkillStore): void {
    this.trusted();
    if (!this.stores().some((s) => s.workspace === store.workspace))
      throw new ClientError("selection_changed");
  }
  private locations(path: string): PluginLocations {
    return new PluginLocations({
      read: () => {
        const configuration = vscode.workspace.getConfiguration("chat");
        const inspect = configuration.inspect<unknown>("pluginLocations");
        const effective = configuration.get<unknown>("pluginLocations", {});
        // Machine-scoped user settings are the only supported native target.
        // Refuse conflicting workspace scopes and aliases of this exact directory.
        const alias = Object.keys(locationMap(effective)).some(
          (key) =>
            key !== path &&
            [homedir(), ...this.stores().map((s) => s.workspace)].some((base) =>
              sameNativePath(
                resolve(
                  base,
                  key.startsWith("~/")
                    ? join(homedir(), key.slice(2))
                    : key.trim(),
                ),
                path,
              ),
            ),
        );
        return {
          target: inspect?.globalValue,
          effective,
          shadowed:
            alias ||
            [
              inspect?.workspaceValue,
              inspect?.workspaceFolderValue,
              inspect?.globalLanguageValue,
              inspect?.workspaceLanguageValue,
              inspect?.workspaceFolderLanguageValue,
            ].some((v) => v !== undefined),
        };
      },
      write: (value) =>
        Promise.resolve(
          vscode.workspace
            .getConfiguration("chat")
            .update(
              "pluginLocations",
              value,
              vscode.ConfigurationTarget.Global,
            ),
        ),
    });
  }
  private policy(): void {
    const configuration = vscode.workspace.getConfiguration("chat");
    if (
      !configuration.get<boolean>("plugins.enabled", true) ||
      configuration.get<boolean>("disableAIFeatures", false)
    )
      throw new ClientError("native_plugins_disabled");
  }
  private async operation(action: () => Promise<void>): Promise<void> {
    if (this.busy) throw new ClientError("installation_busy_or_interrupted");
    this.busy = true;
    try {
      await action();
    } finally {
      this.busy = false;
      this.changed.fire();
    }
  }
  async rows(): Promise<(PluginRow | ToolIssueRow)[]> {
    const rows: (PluginRow | ToolIssueRow)[] = [];
    for (const store of this.stores()) {
      try {
        const scanned = await store.scan();
        for (const receipt of scanned.items) {
          const row: PluginRow = { pluginStore: store, receipt };
          try {
            row.enabled =
              this.locations(join(store.destination, receipt.name)).value(
                join(store.destination, receipt.name),
              ) === true;
          } catch (error) {
            row.configurationError =
              error instanceof ClientError
                ? error.code
                : "plugin_configuration_unavailable";
          }
          rows.push(row);
        }
        for (const issue of scanned.issues)
          rows.push({
            issue: issue.code,
            entry: issue.entry,
            workspace: store.workspace,
            kind: "plugin",
            recover: true,
          });
      } catch (error) {
        rows.push({
          issue:
            error instanceof ClientError
              ? error.code
              : "local_tools_unavailable",
          workspace: store.workspace,
          kind: "plugin",
          recover: true,
        });
      }
    }
    const admitted = new Set(this.stores().map((store) => store.workspace));
    for (const folder of vscode.workspace.workspaceFolders ?? [])
      if (!admitted.has(folder.uri.fsPath))
        rows.push({
          issue: ["file", "vscode-remote"].includes(folder.uri.scheme)
            ? "workspace_root_limit"
            : "filesystem_workspace_required",
          workspace: folder.name,
          kind: "plugin",
        });
    return rows;
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
    return (
      await vscode.window.showQuickPick(
        stores.map((store) => ({ label: store.workspace, store })),
        { title: "Choose the workspace that owns this Plugin" },
      )
    )?.store;
  }
  install(index = 0, path?: string): Promise<void> {
    return this.operation(async () => {
      this.trusted();
      const detail = this.browser.state.detail,
        read = this.browser.packageConsumer(index),
        sessionGuard = this.session.guard(this.session.connection());
      const check = () => {
        this.trusted();
        sessionGuard();
        if (this.browser.state.detail !== detail)
          throw new ClientError("selection_changed");
      };
      if (detail?.item.kind !== "plugin")
        throw new ClientError("complete_plugin_package_required");
      const plan = pluginPlan(
        await read(),
        packageHash(this.session.consumerBinding()),
      );
      plan.identity = packageHash(
        JSON.stringify([plan.identity, detail.item.releaseId]),
      );
      check();
      const store = await this.workspace(path);
      check();
      if (!store) return;
      const guard = () => {
        check();
        this.guard(store);
      };
      const destination = join(store.destination, plan.name),
        locations = this.locations(destination),
        current = locations.value(destination);
      const status = await store.inspect(plan);
      guard();
      if (status.identical) {
        void vscode.window.showInformationMessage(
          "This exact Plugin is already installed. Its enablement is unchanged; no download or write was needed.",
        );
        return;
      }
      if (current === true)
        throw new ClientError("disable_plugin_before_update");
      const configuration =
          vscode.workspace.getConfiguration("openAgenticCatalog"),
        toolsInspection = configuration.inspect<unknown>("packageTools");
      if (
        [
          toolsInspection?.workspaceValue,
          toolsInspection?.workspaceFolderValue,
          toolsInspection?.workspaceLanguageValue,
          toolsInspection?.workspaceFolderLanguageValue,
        ].some((v) => v !== undefined)
      )
        throw new ClientError("workspace_settings_refused");
      const tools = packageTools(
        configuration.get<unknown>("packageTools", {}),
      );
      const summary = `Install ${plan.name} disabled at ${destination}.\nNative setting: chat.pluginLocations; execution-host user/machine scope (shared by other windows on this host). This is not workspace-only enablement.\nExact HTTPS source: ${JSON.stringify(plan.sources[0])}\nArchive: ${plan.archiveSize} bytes; ${plan.archiveDigest}.\nDeclared license: ${detail.facts.find((f) => f.label === "Declared license")?.value ?? "not supplied"}.\nNo scripts are run. Activation needs separate consent after the verified component preview. Catalog approval is not execution trust.`;
      await this.preview(
        [
          summary,
          "Complete inventory:",
          ...plan.files.map(
            (f) =>
              `${f.path} ${f.size} bytes ${f.executable ? "executable" : "regular"} ${f.digest}`,
          ),
          "Changes:",
          ...packageChanges(status.previous?.files ?? [], plan.files),
        ].join("\n"),
      );
      guard();
      if (
        (await vscode.window.showWarningMessage(
          `Install ${plan.name} disabled?`,
          { modal: true, detail: summary },
          "Install Plugin disabled",
        )) !== "Install Plugin disabled"
      )
        return;
      guard();
      await read();
      guard();
      // Register false BEFORE any installed directory appears. A crash cannot enable it.
      await locations.set(destination, current, false, guard);
      const work = await store.temporary();
      try {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: "Verify and install disabled Catalog Plugin",
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
            const fence = () => {
              guard();
              if (signal.aborted) throw new ClientError("package_cancelled");
              if (locations.value(destination) !== false)
                throw new ClientError("plugin_configuration_changed");
            };
            try {
              const content = await acquirePackage(
                plan,
                plan.sources[0]!,
                work,
                tools,
                signal,
                await this.packageCredentials?.select(plan.sources[0]!),
              );
              fence();
              await store.install(
                plan,
                content,
                async () => {
                  await read();
                  fence();
                },
                fence,
              );
            } finally {
              listener.dispose();
            }
          },
        );
      } finally {
        await rm(work, { recursive: true, force: true });
      }
      void vscode.window.showInformationMessage(
        "Plugin installed disabled. Select Catalog: Activate Selected Plugin to inspect components and grant whole-package consent.",
      );
    });
  }
  activate(index = 0, path?: string): Promise<void> {
    return this.operation(async () => {
      this.trusted();
      this.policy();
      const detail = this.browser.state.detail,
        read = this.browser.packageConsumer(index),
        sessionGuard = this.session.guard(this.session.connection());
      if (detail?.item.kind !== "plugin")
        throw new ClientError("complete_plugin_package_required");
      const plan = pluginPlan(
        await read(),
        packageHash(this.session.consumerBinding()),
      );
      plan.identity = packageHash(
        JSON.stringify([plan.identity, detail.item.releaseId]),
      );
      const store = await this.workspace(path);
      if (!store) return;
      const guard = () => {
        sessionGuard();
        this.guard(store);
        this.policy();
        if (this.browser.state.detail !== detail)
          throw new ClientError("selection_changed");
      };
      guard();
      if (!(await store.inspect(plan)).identical)
        throw new ClientError("install_selected_plugin_first");
      const destination = join(store.destination, plan.name),
        locations = this.locations(destination),
        current = locations.value(destination);
      if (current === true) {
        void vscode.window.showInformationMessage(
          "This Plugin path is already enabled. Native policy, collisions and trust still determine loaded components.",
        );
        return;
      }
      if (current !== false)
        throw new ClientError("plugin_configuration_changed");
      const preview = pluginPreview(plan, await store.content(plan.name));
      guard();
      await this.preview(preview.text + "\n\n" + preview.refusals.join("\n"));
      guard();
      if (preview.refusals.length)
        throw new ClientError("plugin_native_semantics_unsupported");
      if (
        (await vscode.window.showWarningMessage(
          `Activate all native components in ${plan.name}?`,
          {
            modal: true,
            detail: `Enables ${destination} in execution-host user/machine settings, including other windows. Instructions, hooks and MCP definitions can become available. Native trust and enterprise policy still apply. No Catalog credentials are supplied. Local hook matchers are ignored. Review the plain-text preview first.`,
          },
          "Activate Plugin",
        )) !== "Activate Plugin"
      )
        return;
      await store.activatePlugin(
        plan,
        async () => {
          await read();
          guard();
        },
        () => locations.set(destination, false, true, guard),
        guard,
      );
      void vscode.window.showInformationMessage(
        "Plugin path enabled. Open Chat: Open Customizations to inspect native loading. This is not a guarantee that every component is supported or running.",
      );
    });
  }
  private async selected(
    name?: string,
    path?: string,
  ): Promise<PluginRow | undefined> {
    this.trusted();
    const rows = (await this.rows()).filter(
      (row): row is PluginRow => "pluginStore" in row,
    );
    if (name && path) {
      const row = rows.find(
        (r) => r.receipt.name === name && r.pluginStore.workspace === path,
      );
      if (!row) throw new ClientError("owned_package_required");
      return row;
    }
    return (
      await vscode.window.showQuickPick(
        rows.map((row) => ({
          label: row.receipt.name,
          description: row.pluginStore.workspace,
          row,
        })),
        { title: "Choose an owned Plugin (no Catalog connection needed)" },
      )
    )?.row;
  }
  private async confirmed(
    row: PluginRow,
    expected?: SkillSummary,
  ): Promise<SkillReceipt> {
    const current = await row.pluginStore.get(row.receipt.name);
    if (
      !current ||
      receiptSummary(current).fingerprint !==
        (expected ?? row.receipt).fingerprint
    )
      throw new ClientError("selection_changed");
    return current;
  }
  disable(
    name?: string,
    path?: string,
    expected?: SkillSummary,
  ): Promise<void> {
    return this.operation(async () => {
      const row = await this.selected(name, path);
      if (!row) return;
      const confirmed = await this.confirmed(row, expected);
      const destination = join(row.pluginStore.destination, row.receipt.name),
        locations = this.locations(destination);
      // Disabling is safe even if local package contents were edited.
      await row.pluginStore.withReceipt(
        confirmed,
        () =>
          locations.set(destination, locations.value(destination), false, () =>
            this.guard(row.pluginStore),
          ),
        () => this.guard(row.pluginStore),
      );
      void vscode.window.showInformationMessage(
        "Plugin path disabled. Native hosts own stopping running processes; close affected sessions or reload VS Code when needed.",
      );
    });
  }
  remove(name?: string, path?: string, expected?: SkillSummary): Promise<void> {
    return this.operation(async () => {
      const row = await this.selected(name, path);
      if (!row) return;
      const confirmed = await this.confirmed(row, expected);
      if (
        (await vscode.window.showWarningMessage(
          `Remove owned Plugin ${row.receipt.name}? Local edits block deletion.`,
          { modal: true },
          "Remove Plugin",
        )) !== "Remove Plugin"
      )
        return;
      const store = row.pluginStore,
        destination = join(store.destination, row.receipt.name),
        locations = this.locations(destination),
        guard = () => this.guard(store);
      let disabled = false;
      await store.remove(
        row.receipt.name,
        () => {
          guard();
          if (disabled && locations.value(destination) !== false)
            throw new ClientError("plugin_configuration_changed");
        },
        confirmed,
        async () => {
          await locations.set(
            destination,
            locations.value(destination),
            false,
            guard,
          );
          disabled = true;
        },
        () => locations.set(destination, false, undefined, guard),
      );
    });
  }
  private async recoverySelection(store: SkillStore) {
    this.guard(store);
    await store.validateRecoveryDirectory();
    const scanned = await store.scan();
    let issues = scanned.issues.length;
    const paths = new Set(
      scanned.items.map((receipt) => join(store.destination, receipt.name)),
    );
    for (const path of Object.keys(
      locationMap(
        vscode.workspace
          .getConfiguration("chat")
          .get<unknown>("pluginLocations", {}),
      ),
    )) {
      if (path.startsWith(store.destination + sep)) {
        const name = path.slice(store.destination.length + 1);
        try {
          pluginName(name);
          if (join(store.destination, name) !== path)
            throw new ClientError("unsafe_installation_path");
          paths.add(path);
        } catch {
          issues++;
        }
      }
    }
    this.guard(store);
    return { paths: [...paths].sort(), receipts: scanned.items, issues };
  }
  recover(path?: string): Promise<void> {
    return this.operation(async () => {
      const store = await this.workspace(path);
      if (!store) return;
      const selected = await this.recoverySelection(store);
      if (
        (await vscode.window.showWarningMessage(
          "Close other Catalog installation windows before recovery. Owned Plugin paths are disabled first; unexpected local changes are preserved.",
          {
            modal: true,
            detail: [
              `Workspace: ${JSON.stringify(store.workspace)}`,
              "Paths to disable, including pending registrations:",
              ...(selected.paths.length
                ? selected.paths.map((value) => JSON.stringify(value))
                : ["None."]),
              "Native settings apply to other windows on this execution host. Registration alone grants no ownership of files.",
            ].join("\n"),
          },
          "Recover Plugin operation",
        )) !== "Recover Plugin operation"
      )
        return;
      const current = await this.recoverySelection(store);
      if (
        JSON.stringify(current.paths) !== JSON.stringify(selected.paths) ||
        JSON.stringify(current.receipts) !== JSON.stringify(selected.receipts)
      )
        throw new ClientError("selection_changed");
      const paths = selected.paths;
      for (const path of paths) {
        const locations = this.locations(path);
        await locations.set(path, locations.value(path), false, () =>
          this.guard(store),
        );
      }
      const guard = () => {
        this.guard(store);
        for (const path of paths)
          if (this.locations(path).value(path) !== false)
            throw new ClientError("plugin_configuration_changed");
      };
      await store.recover(guard);
      if (current.issues)
        void vscode.window.showWarningMessage(
          "Owned Plugin paths were disabled. Damaged receipts and invalid registration keys were preserved; refresh My tools to inspect the remaining errors.",
        );
    });
  }
  compare(index = 0, path?: string): Promise<void> {
    return this.operation(async () => {
      this.trusted();
      const detail = this.browser.state.detail,
        check = this.session.guard(this.session.connection());
      if (detail?.item.kind !== "plugin")
        throw new ClientError("complete_plugin_package_required");
      const plan = pluginPlan(
        await this.browser.packageConsumer(index)(),
        packageHash(this.session.consumerBinding()),
      );
      plan.identity = packageHash(
        JSON.stringify([plan.identity, detail.item.releaseId]),
      );
      const store = await this.workspace(path);
      check();
      if (this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
      if (!store) return;
      this.guard(store);
      let text: string;
      try {
        const status = await store.inspect(plan);
        const enabled = this.locations(
          join(store.destination, plan.name),
        ).value(join(store.destination, plan.name));
        text = [
          `Plugin ${plan.name}`,
          `Workspace: ${store.workspace}`,
          `Native path: ${enabled === true ? "enabled" : enabled === false ? "disabled" : "not registered"}`,
          status.identical
            ? "The selected exact package is installed."
            : status.previous
              ? "The selected package differs from the installed inventory."
              : "No owned installation exists.",
          ...packageChanges(status.previous?.files ?? [], plan.files),
        ].join("\n");
      } catch (error) {
        if (!(error instanceof ClientError)) throw error;
        text = `Comparison refused: ${error.code}. Local changes and native settings are preserved. No local edit paths or file-content diff is inferred.`;
      }
      check();
      this.guard(store);
      if (this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
      await this.preview(
        text +
          "\nRead-only inventory comparison: no acquisition, filesystem write or activation.",
      );
    });
  }
  dispose(): void {
    this.configurationChanged.dispose();
    this.changed.dispose();
  }
}
