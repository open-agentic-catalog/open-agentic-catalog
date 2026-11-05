import * as vscode from "vscode";
import {
  A2AConversation,
  a2aPlan,
  a2aSettings,
  a2aHeaders,
  a2aPrompt,
  a2aLimits,
} from "./a2a.js";
import { a2aClient } from "./a2a-client.js";
import { ClientError, diagnostic, record, safeURL } from "./config.js";
import type { Browser } from "./browse.js";
import type { Session } from "./session.js";
import { mcpDocument } from "./mcp-document.js";
import { html } from "./panel.js";

export class CatalogA2A implements vscode.Disposable {
  conversation: A2AConversation | undefined;
  private panel: vscode.WebviewPanel | undefined;
  private revision = 0;
  private generation = 0;
  private destination = "";
  private draft = { prompt: "", stream: false };
  private source:
    { detail: Browser["state"]["detail"]; index: number } | undefined;
  private disposables: vscode.Disposable[];
  constructor(
    private context: vscode.ExtensionContext,
    private browser: Browser,
    private session: Session,
    private preview: (content: string) => Promise<void>,
  ) {
    this.disposables = [
      session.onChange(() => this.clear()),
      vscode.workspace.onDidChangeConfiguration((e) => {
        if (e.affectsConfiguration("openAgenticCatalog.a2a")) this.clear();
      }),
    ];
  }
  private trusted(): void {
    if (
      !vscode.workspace.isTrusted ||
      vscode.workspace.workspaceFolders?.some(
        (f) => !["file", "vscode-remote"].includes(f.uri.scheme),
      )
    )
      throw new ClientError("trusted_workspace_required");
  }
  async select(index: number): Promise<void> {
    this.trusted();
    const detail = this.browser.state.detail;
    if (detail?.item.kind !== "agent")
      throw new ClientError("select_agent_resource");
    const configuration =
        vscode.workspace.getConfiguration("openAgenticCatalog"),
      inspected = configuration.inspect("a2a");
    if (
      inspected?.workspaceValue !== undefined ||
      inspected?.workspaceFolderValue !== undefined ||
      inspected?.workspaceLanguageValue !== undefined ||
      inspected?.workspaceFolderLanguageValue !== undefined
    )
      throw new ClientError("workspace_settings_refused");
    const settings = a2aSettings(configuration.get("a2a", {})),
      connection = this.session.connection(),
      check = this.session.guard(connection);
    let generation = this.generation,
      selecting = true;
    const guard = () => {
      if (generation !== this.generation)
        throw new ClientError("selection_changed");
      this.trusted();
      check();
      if (selecting && this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
    };
    const read = this.browser.consumer(index, guard);
    const acquire = async () => {
      const ref = await read();
      guard();
      if (ref.url && !ref.bytes)
        safeURL(ref.url, settings.allowLoopbackHTTP, false);
      return mcpDocument(
        ref,
        { ...connection, allowLoopbackHTTP: settings.allowLoopbackHTTP },
        async (url) => {
          const yes = await vscode.window.showWarningMessage(
            `Read the external Agent Card at ${url}? No credentials or Catalog trust are forwarded.`,
            { modal: true },
            "Read Card",
          );
          guard();
          return yes === "Read Card";
        },
        undefined,
        this.session.cancellation(),
      );
    };
    const plan = a2aPlan(await acquire(), settings);
    guard();
    const selected = await vscode.window.showQuickPick(
      plan.interfaces.map((i, index) => ({
        label: i.label,
        description: i.reason ?? "Direct agent connection",
        index,
      })),
      { title: "Choose one A2A interface" },
    );
    guard();
    if (this.browser.state.detail !== detail)
      throw new ClientError("selection_changed");
    if (!selected) return;
    const endpoint = plan.interfaces[selected.index]!;
    if (!endpoint.url || endpoint.reason)
      throw new ClientError("a2a_interface_unavailable");
    const auth = await vscode.window.showQuickPick(
      plan.auth.map((a, index) => ({
        label: a.label,
        description: a.reason ?? "Separate target credentials",
        index,
      })),
      { title: "Choose A2A target authentication" },
    );
    guard();
    if (!auth) return;
    const option = plan.auth[auth.index]!;
    if (option.reason) throw new ClientError("a2a_auth_unavailable");
    const values: string[] = [];
    for (const input of option.inputs) {
      const value = await vscode.window.showInputBox({
        title: `A2A target: ${input.label}`,
        prompt:
          "Agent credential, separate from Catalog login. Kept only in this conversation's memory.",
        password: true,
        ignoreFocusOut: true,
      });
      guard();
      if (value === undefined) return;
      values.push(value);
    }
    const headers = a2aHeaders(option, values);
    if (
      (await vscode.window.showWarningMessage(
        `Connect to ${plan.name} at ${endpoint.url}?`,
        {
          modal: true,
          detail: `${plan.notices.join("\n")}\nExecution host: ${vscode.env.remoteName ?? "local"}. Private and loopback addresses refer to this host. Each prompt requires separate consent. No editor content, files or Catalog token is sent. Starting a new conversation clears the previous transcript and does not cancel its remote task.`,
        },
        "Open conversation",
      )) !== "Open conversation"
    )
      return;
    guard();
    if (this.browser.state.detail !== detail)
      throw new ClientError("selection_changed");
    const authorize = async () => {
      guard();
      const current = a2aPlan(await acquire(), settings);
      guard();
      if (current.identity !== plan.identity)
        throw new ClientError("selection_changed");
    };
    selecting = false;
    this.clear();
    generation = this.generation;
    this.destination = endpoint.url;
    this.source = { detail, index };
    this.conversation = new A2AConversation(
      plan,
      a2aClient(plan, endpoint, settings, headers),
      authorize,
      guard,
      () => this.publish(),
    );
    this.open();
  }
  private publish(): void {
    this.revision++;
    void this.panel?.webview.postMessage({
      type: "state",
      revision: this.revision,
      destination: this.destination,
      streaming: this.conversation?.plan.streaming === true,
      name: this.conversation?.plan.name ?? "Agent conversation",
      draft: this.draft,
      promptLimit: a2aLimits.prompt,
      state: this.conversation?.state,
    });
  }
  private open(): void {
    const media = vscode.Uri.joinPath(this.context.extensionUri, "media");
    const panel = vscode.window.createWebviewPanel(
      "openAgenticCatalog.a2a",
      "Agent conversation",
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
        .asWebviewUri(vscode.Uri.joinPath(media, "a2a.js"))
        .toString(),
      panel.webview
        .asWebviewUri(vscode.Uri.joinPath(media, "catalog.css"))
        .toString(),
      panel.webview.cspSource,
    );
    panel.onDidDispose(() => {
      if (this.panel === panel) {
        this.draft = { prompt: "", stream: false };
        this.conversation?.disconnect(true);
        this.conversation = undefined;
        this.panel = undefined;
        this.destination = "";
        this.source = undefined;
      }
    });
    panel.webview.onDidReceiveMessage(async (input: unknown) => {
      try {
        if (this.panel !== panel || !this.conversation)
          throw new ClientError("selection_changed");
        const m = record(input);
        if (m.type === "ready" && Object.keys(m).length === 1) {
          this.publish();
          return;
        }
        if (m.type === "draft") {
          if (
            Object.keys(m).length !== 3 ||
            typeof m.prompt !== "string" ||
            m.prompt.length > a2aLimits.prompt ||
            typeof m.stream !== "boolean"
          )
            throw new ClientError("invalid_message");
          this.draft = { prompt: m.prompt, stream: m.stream };
          return;
        }
        const allowed =
          m.type === "send"
            ? ["type", "revision", "prompt", "stream"]
            : ["type", "revision"];
        if (
          m.revision !== this.revision ||
          Object.keys(m).length !== allowed.length ||
          Object.keys(m).some((key) => !allowed.includes(key))
        )
          throw new ClientError("selection_changed");
        if (
          m.type === "send" &&
          typeof m.prompt === "string" &&
          typeof m.stream === "boolean"
        )
          await this.send(m.prompt, m.stream);
        else if (m.type === "read") await this.read();
        else if (m.type === "cancel") await this.cancel();
        else if (m.type === "disconnect") this.conversation?.disconnect();
        else if (m.type === "catalog")
          await vscode.commands.executeCommand("openAgenticCatalog.open");
        else if (m.type === "help")
          await vscode.commands.executeCommand(
            "openAgenticCatalog.help",
            "agent",
          );
        else if (m.type === "new") {
          const source = this.source;
          if (source && this.browser.state.detail === source.detail)
            await this.select(source.index);
          else {
            await vscode.commands.executeCommand("openAgenticCatalog.open");
            await vscode.window.showInformationMessage(
              "Select an Agent Card in Catalog, then choose Talk to Agent to start a new conversation. The current conversation remains open; no remote task is canceled.",
            );
          }
        } else if (m.type === "export" && this.conversation)
          await this.preview(this.conversation.export());
        else throw new ClientError("invalid_message");
      } catch (error) {
        await vscode.window.showErrorMessage(diagnostic(error));
      }
    });
    this.publish();
  }
  async send(prompt: string, stream = false): Promise<void> {
    const conversation = this.conversation;
    if (!conversation) throw new ClientError("a2a_disconnected");
    a2aPrompt(prompt);
    // Consent contains the exact prompt and destination. It never reads editor context.
    if (
      (await vscode.window.showWarningMessage(
        `Send this prompt to ${this.destination}?`,
        { modal: true, detail: prompt },
        "Send prompt",
      )) !== "Send prompt"
    )
      throw new ClientError("a2a_send_cancelled");
    if (this.conversation !== conversation)
      throw new ClientError("selection_changed");
    await conversation.send(prompt, stream);
  }
  async read(): Promise<void> {
    if (!this.conversation) throw new ClientError("a2a_disconnected");
    await this.conversation.read();
  }
  async cancel(): Promise<void> {
    const conversation = this.conversation;
    if (!conversation) throw new ClientError("a2a_disconnected");
    const task = conversation.state.task;
    if (
      (await vscode.window.showWarningMessage(
        `Request cancellation of task ${task ?? "unknown"} at ${this.destination}?`,
        {
          modal: true,
          detail:
            "The agent can refuse cancellation. Disconnect only closes this client's connection.",
        },
        "Cancel remote task",
      )) !== "Cancel remote task"
    )
      return;
    if (this.conversation !== conversation || conversation.state.task !== task)
      throw new ClientError("selection_changed");
    await conversation.cancel();
  }
  clear(): void {
    this.generation++;
    this.draft = { prompt: "", stream: false };
    this.conversation?.disconnect(true);
    this.conversation = undefined;
    this.panel?.dispose();
    this.panel = undefined;
    this.destination = "";
    this.source = undefined;
  }
  dispose(): void {
    this.clear();
    this.disposables.forEach((d) => d.dispose());
  }
}
