import * as vscode from "vscode";
import {
  Browser,
  query,
  emptyQuery,
  hasLive,
  bookmarkLabel,
  type Query,
  type Item,
  type Bookmark,
} from "./browse.js";
import { randomUUID } from "node:crypto";
import { ClientError, diagnostic } from "./config.js";
import { html, message, projection, guidance } from "./panel.js";
import type { Session } from "./session.js";

type Node =
  { item: Item } | { saved: number; reference: Bookmark; scope: number };
export class CatalogViews
  implements vscode.TreeDataProvider<Node>, vscode.Disposable
{
  private changed = new vscode.EventEmitter<Node | undefined>();
  readonly onDidChangeTreeData = this.changed.event;
  private panel: vscode.WebviewPanel | undefined;
  private revision = 0;
  private draft: Query = { ...emptyQuery };
  private scope = 0;
  private tree: vscode.TreeView<Node>;
  private lastProjection: string | undefined;
  private subscription;
  private contents = new Map<string, string>();
  private documentChanged = new vscode.EventEmitter<vscode.Uri>();
  private disposables: vscode.Disposable[] = [];
  constructor(
    private context: vscode.ExtensionContext,
    readonly browser: Browser,
    private session: Session,
    private useMcp: (index: number) => Promise<void>,
    private installSkill: (index: number) => Promise<void>,
    private installPlugin: (index: number) => Promise<void>,
    private activatePlugin: (index: number) => Promise<void>,
    private talkAgent: (index: number) => Promise<void>,
    private comparePackage: (index: number) => Promise<void>,
  ) {
    this.subscription = browser.onChange(() => {
      this.changed.fire(undefined);
      this.publish();
    });
    this.disposables.push(
      session.onChange(() => {
        for (const key of this.contents.keys()) {
          this.contents.set(
            key,
            "Catalog session changed. Close this document and inspect it again after sign-in.",
          );
          this.documentChanged.fire(vscode.Uri.parse(key));
        }
      }),
    );
    this.tree = vscode.window.createTreeView("openAgenticCatalog.catalog", {
      treeDataProvider: this,
      showCollapseAll: false,
    });
    this.disposables.push(this.tree);
    this.disposables.push(
      vscode.window.tabGroups.onDidChangeTabs((event) => {
        for (const tab of event.closed) {
          if (!(tab.input instanceof vscode.TabInputText)) continue;
          const uri = tab.input.uri;
          if (uri.scheme !== "oac-evidence") continue;
          const remainsOpen = vscode.window.tabGroups.all.some((group) =>
            group.tabs.some(
              (other) =>
                other.input instanceof vscode.TabInputText &&
                other.input.uri.toString() === uri.toString(),
            ),
          );
          if (!remainsOpen && this.contents.delete(uri.toString())) {
            // Closing a tab need not unload its TextDocument. Release the
            // view slot and redact any retained editor buffer immediately.
            this.documentChanged.fire(uri);
          }
        }
      }),
    );
    this.disposables.push(
      vscode.workspace.onDidChangeWorkspaceFolders(() => this.publish(true)),
    );
    this.disposables.push(
      vscode.workspace.registerTextDocumentContentProvider("oac-evidence", {
        onDidChange: this.documentChanged.event,
        provideTextDocumentContent: (uri) =>
          this.contents.get(uri.toString()) ??
          "Evidence is no longer available. Inspect it again.",
      }),
    );
  }
  getChildren(): Node[] {
    return [
      ...(this.browser.state.page?.items ?? []).map((item) => ({ item })),
      ...this.browser.state.bookmarks.map((reference, saved) => ({
        saved,
        reference: { ...reference },
        scope: this.browser.state.scope,
      })),
    ];
  }
  getTreeItem(node: Node): vscode.TreeItem {
    if ("saved" in node) {
      const item = new vscode.TreeItem(bookmarkLabel(node.reference));
      item.iconPath = new vscode.ThemeIcon("bookmark");
      item.contextValue = "catalogBookmark";
      item.id = `bookmark:${node.saved}`;
      item.command = {
        command: "openAgenticCatalog.reopen",
        title: "Open saved reference",
        arguments: [node],
      };
      return item;
    }
    const value = node.item,
      item = new vscode.TreeItem(
        value.name.replace(
          /\p{Cc}/gu,
          (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`,
        ),
      );
    item.iconPath = new vscode.ThemeIcon(
      {
        agent: "robot",
        mcp_server: "server",
        skill: "book",
        plugin: "extensions",
        unknown: "symbol-misc",
      }[value.kind],
    );
    const kindLabel = {
      agent: "Agent",
      mcp_server: "MCP Server",
      skill: "Skill",
      plugin: "Plugin",
      unknown: "Resource",
    }[value.kind];
    item.description = `${kindLabel} · ${value.origin} · ${value.versionPresent ? (value.version === "" ? "empty version" : JSON.stringify(value.version)) : "no version"}`;
    item.tooltip = `${value.name}\n${item.description}\n${value.identifier}\n${value.description}`;
    item.contextValue = "catalogResource";
    item.id = value.key;
    item.command = {
      command: "openAgenticCatalog.detail",
      title: "Inspect exact resource",
      arguments: [value.key],
    };
    return item;
  }
  private publish(force = false): void {
    let mode = "control";
    try {
      mode = this.session.connection().mode;
    } catch {
      /* No active connection. */
    }
    const state = this.session.connectionState();
    if (this.scope !== this.browser.state.scope) {
      this.scope = this.browser.state.scope;
      this.draft = { ...emptyQuery };
    }
    const g = guidance(
      state,
      !!this.browser.state.page,
      this.browser.state.page?.items.length ?? 0,
      this.browser.state.page?.partial ?? false,
      this.browser.state.error,
    );
    this.tree.message = this.browser.state.busy
      ? "Loading… Use Cancel Catalog Read to stop."
      : [
          this.browser.state.page
            ? `${this.browser.state.page.items.length} shown on this page; ${this.browser.state.page.summary}`
            : undefined,
          g.message,
          this.browser.state.canNext
            ? "Next page is available, including an explicit retry after a temporary failure."
            : "",
        ]
          .filter(Boolean)
          .join(" ");
    for (const [key, value] of Object.entries({
      connection: state,
      canBrowse: !["none", "signed_out", "signing_in"].includes(state),
      busy: this.browser.state.busy,
      next: this.browser.state.canNext,
      phase: this.browser.state.busy ? "loading" : g.phase,
      selectedKind: this.browser.state.detail?.item.kind ?? "",
    }))
      void vscode.commands.executeCommand(
        "setContext",
        `openAgenticCatalog.${key}`,
        value,
      );
    const value = {
      ...projection(
        this.browser,
        mode,
        0,
        ["verified", "verifying"].includes(state) ? "unverified" : state,
      ),
      environment: {
        trusted: vscode.workspace.isTrusted,
        filesystem: (vscode.workspace.workspaceFolders ?? []).some((folder) =>
          ["file", "vscode-remote"].includes(folder.uri.scheme),
        ),
        virtual: (vscode.workspace.workspaceFolders ?? []).some(
          (folder) => !["file", "vscode-remote"].includes(folder.uri.scheme),
        ),
        platform: process.platform,
      },
    };
    const encoded = JSON.stringify(value);
    if (!force && this.lastProjection === encoded) {
      void this.panel?.webview.postMessage({
        type: "account",
        value: this.account(),
      });
      return;
    }
    this.lastProjection = encoded;
    this.revision++;
    if (!this.panel) return;
    void this.panel.webview.postMessage({
      type: "state",
      value: {
        ...value,
        draft: this.draft,
        account: this.account(),
        revision: this.revision,
      },
    });
  }
  private account() {
    try {
      const connection = this.session.connection(),
        identity = this.session.snapshot()?.identity;
      return {
        label: connection.label,
        mode: connection.mode,
        state: this.session.connectionState(),
        host: vscode.env.remoteName ?? "local",
        origin: connection.origin,
        ...(identity
          ? { subject: identity.subject, tenantId: identity.tenantId }
          : {}),
      };
    } catch {
      return undefined;
    }
  }
  refreshState(): void {
    this.publish();
  }
  open(): void {
    if (this.panel) {
      this.panel.reveal();
      this.publish();
      return;
    }
    const media = vscode.Uri.joinPath(this.context.extensionUri, "media");
    const panel = vscode.window.createWebviewPanel(
      "openAgenticCatalog.browser",
      "Catalog",
      vscode.ViewColumn.Active,
      {
        enableScripts: true,
        localResourceRoots: [media],
        retainContextWhenHidden: false,
      },
    );
    this.panel = panel;
    panel.webview.html = html(
      panel.webview
        .asWebviewUri(vscode.Uri.joinPath(media, "catalog.js"))
        .toString(),
      panel.webview
        .asWebviewUri(vscode.Uri.joinPath(media, "catalog.css"))
        .toString(),
      panel.webview.cspSource,
    );
    panel.onDidDispose(() => {
      if (this.panel === panel) this.panel = undefined;
    });
    panel.webview.onDidReceiveMessage(async (input) => {
      try {
        const m = message(input);
        if (m.type === "ready") {
          this.publish(true);
          return;
        }
        if (this.panel !== panel) throw new ClientError("selection_changed");
        // Drafts do not read resources. Ordinary reads may advance the view
        // revision while a typed edit is still crossing the native bridge.
        if (m.type === "draft") {
          if (m.scope !== this.browser.state.scope)
            throw new ClientError("connection_changed");
          this.draft = query(m.query, false, true, true);
          return;
        }
        if (m.revision !== this.revision)
          throw new ClientError("selection_changed");
        const detail = this.browser.state.detail;
        const lengths: Record<string, number> = {
          inspect: detail?.documents.length ?? 0,
          export: detail?.documents.length ?? 0,
          mcp: detail?.documents.length ?? 0,
          a2a: detail?.documents.length ?? 0,
          skill: detail?.packages?.length ?? 0,
          plugin: detail?.packages?.length ?? 0,
          activatePlugin: detail?.packages?.length ?? 0,
          compare: detail?.packages?.length ?? 0,
          open: detail?.references.length ?? 0,
          reopen: this.browser.state.bookmarks.length,
          removeBookmark: this.browser.state.bookmarks.length,
        };
        if (
          Object.hasOwn(lengths, m.type as string) &&
          (m.index as number) >= lengths[m.type as string]!
        )
          throw new ClientError("selection_changed");
        switch (m.type) {
          case "help":
            await vscode.commands.executeCommand(
              "openAgenticCatalog.help",
              "catalog",
            );
            break;
          case "recover": {
            if (m.action === "restart") await this.search();
            else if (m.action === "trust")
              await vscode.commands.executeCommand("workbench.trust.manage");
            else
              await vscode.commands.executeCommand(
                `openAgenticCatalog.${m.action}`,
                ...(m.action === "help" ? ["connection"] : []),
              );
            break;
          }
          case "cancel":
            this.browser.cancel();
            break;
          case "back":
            this.browser.back();
            break;
          case "search":
            await this.search(m.query);
            break;
          case "next":
            await this.search(this.browser.state.query, true);
            break;
          case "explore":
            this.draft = query(m.query, false, true, true);
            await this.browser.explore(m.query);
            break;
          case "select":
            await this.browser.select(m.key as string);
            break;
          case "tag":
            await this.tag(m.tag as string);
            break;
          case "bookmark":
            await this.browser.saveBookmark();
            break;
          case "reopen":
            await this.browser.reopen(m.index as number);
            break;
          case "removeBookmark":
            await this.browser.removeBookmark(m.index as number);
            break;
          case "inspect":
            await this.inspect(m.index as number, false);
            break;
          case "plugin":
            await this.installPlugin(m.index as number);
            break;
          case "activatePlugin":
            await this.activatePlugin(m.index as number);
            break;
          case "compare":
            await this.comparePackage(m.index as number);
            break;
          case "skill":
            await this.installSkill(m.index as number);
            break;
          case "a2a":
            await this.talkAgent(m.index as number);
            break;
          case "mcp":
            await this.useMcp(m.index as number);
            break;
          case "export":
            await this.inspect(m.index as number, true);
            break;
          case "open":
            await this.openLink(m.index as number);
            break;
          case "copy":
            await vscode.env.clipboard.writeText(
              JSON.stringify(this.exactReference()),
            );
            break;
          case "manifest":
            await this.inspectManifest();
            break;
          case "image":
            await this.browser.image(m.index as number);
            break;
        }
      } catch (error) {
        void vscode.window.showErrorMessage(diagnostic(error));
      }
    });
  }
  private async tag(tag: string): Promise<void> {
    const q = { ...this.browser.state.query, tag };
    delete q.tags;
    if (this.session.connection().mode === "published" && !q.text.trim()) {
      const check = this.session.guard(this.session.connection()),
        revision = this.revision;
      const text = await vscode.window.showInputBox({
        title: "Enter search text to filter this published tag",
      });
      if (text === undefined) return;
      check();
      if (revision !== this.revision)
        throw new ClientError("selection_changed");
      q.text = text;
    }
    await this.search(q);
  }
  async search(
    input: unknown = this.browser.state.query,
    next = false,
  ): Promise<unknown> {
    // Confirmation is per explicit request, including continuation traffic.
    const q = query(input, this.session.connection().mode === "published");
    if (!next) this.draft = { ...q };
    if (hasLive(q)) {
      const connection = this.session.connection(),
        check = this.session.guard(connection),
        revision = this.revision;
      if (
        (await vscode.window.showWarningMessage(
          "Send this query and filters to administrator-approved external sources? Continuation can repeat peer traffic.",
          {
            modal: true,
            detail: `Query and filters: ${JSON.stringify(q)}. ${next ? "This continuation can repeat peer traffic." : "Only administrator-approved sources participate."}`,
          },
          "Search peers",
        )) !== "Search peers"
      )
        return;
      check();
      if (revision !== this.revision)
        throw new ClientError("selection_changed");
    }
    return this.browser.search(q, next);
  }
  exactReference(): unknown {
    const item = this.browser.state.detail?.item;
    if (!item) throw new ClientError("selection_changed");
    return {
      identifier: item.identifier,
      versionPresent: item.versionPresent,
      ...(item.versionPresent ? { version: item.version } : {}),
      origin: item.origin,
      ...(item.releaseId ? { releaseId: item.releaseId } : {}),
    };
  }
  async detail(key: string): Promise<void> {
    await this.browser.select(key);
    this.open();
  }
  async openLink(index: number): Promise<void> {
    const before = this.browser.state.detail,
      r = before?.references[index];
    if (!r?.url) throw new ClientError("link_unavailable");
    const connection = this.session.connection(),
      check = this.session.guard(connection);
    if (
      (await vscode.window.showWarningMessage(
        `Open ${r.url} in your browser? No Catalog token is forwarded. The destination may request its own login.`,
        { modal: true },
        "Open link",
      )) !== "Open link"
    )
      return;
    check();
    if (this.browser.state.detail !== before)
      throw new ClientError("selection_changed");
    await vscode.env.openExternal(vscode.Uri.parse(r.url));
  }
  async inspect(index: number, exportBytes: boolean): Promise<void> {
    const before = this.browser.state.detail;
    const check = this.session.guard(this.session.connection());
    const result = await this.browser.document(index);
    check();
    if (before !== this.browser.state.detail)
      throw new ClientError("selection_changed");
    if (exportBytes) {
      const destination = await vscode.window.showSaveDialog({
        title: "Export selected Catalog document",
        saveLabel: "Export document",
      });
      if (!destination) return;
      check();
      if (before !== this.browser.state.detail)
        throw new ClientError("selection_changed");
      await vscode.workspace.fs.writeFile(destination, result.bytes);
    } else {
      check();
      const ref = before!.documents[index]!;
      const language =
        ref.value !== undefined ||
        /(?:json|\.json)$/iu.test(ref.mediaType ?? ref.label)
          ? "json"
          : /(?:markdown|\.md)$/iu.test(ref.mediaType ?? ref.label)
            ? "markdown"
            : "plaintext";
      await this.showDocument(result.bytes, result.label, language);
    }
  }
  private async inspectManifest(): Promise<void> {
    const check = this.session.guard(this.session.connection());
    const bytes = await this.browser.manifest();
    check();
    await this.showDocument(bytes, "ARD manifest", "json");
  }
  async preview(
    content: string,
    label = "Package preview",
    language = "plaintext",
  ): Promise<void> {
    await this.showDocument(Buffer.from(content), label, language);
  }
  private async showDocument(
    bytes: Uint8Array,
    label: string,
    language: string,
  ): Promise<void> {
    const check = this.session.guard(this.session.connection());
    let content: string;
    try {
      content = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      if (content.includes("\0")) throw new Error();
    } catch {
      throw new ClientError("document_is_not_text");
    }
    if (this.contents.size >= 16)
      throw new ClientError("close_evidence_documents");
    const name =
      label
        .replace(/[^a-z0-9.-]+/giu, "-")
        .replace(/^[.-]+|[.-]+$/gu, "")
        .slice(0, 80) || "Catalog-document";
    const suffix =
      language === "json" ? "json" : language === "markdown" ? "md" : "txt";
    const filename = name.toLowerCase().endsWith(`.${suffix}`)
      ? name
      : `${name}.${suffix}`;
    const uri = vscode.Uri.parse(`oac-evidence:/${randomUUID()}/${filename}`);
    this.contents.set(uri.toString(), content);
    try {
      let document = await vscode.workspace.openTextDocument(uri);
      check();
      document = await vscode.languages.setTextDocumentLanguage(
        document,
        language,
      );
      check();
      await vscode.window.showTextDocument(document, { preview: true });
      check();
    } catch (error) {
      this.contents.delete(uri.toString());
      throw error;
    }
  }
  dispose(): void {
    this.panel?.dispose();
    this.subscription.dispose();
    for (const d of this.disposables) d.dispose();
    this.changed.dispose();
    this.documentChanged.dispose();
    this.contents.clear();
  }
}
