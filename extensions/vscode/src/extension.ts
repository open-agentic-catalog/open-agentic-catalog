import * as vscode from "vscode";
import {
  connections,
  diagnostic,
  ClientError,
  type Connection,
  abortable,
} from "./config.js";
import { Session } from "./session.js";
import { transport } from "./transport.js";
import { executionHost, credentialAccess } from "./host.js";
import {
  connectionSettings,
  editConnection,
  transportSettings,
} from "./connection-ui.js";
import {
  Browser,
  bookmarkLabel,
  exactReference,
  consumerFamily,
} from "./browse.js";
import type { SkillSummary } from "./install.js";
import { CatalogViews } from "./views.js";
import { CatalogPlugins } from "./plugins-host.js";
import { CatalogSkills } from "./skills-host.js";
import { CatalogA2A } from "./a2a-host.js";
import { CatalogMcp } from "./mcp-host.js";
import { guidance } from "./panel.js";
import { CatalogPackageCredentials } from "./package-credentials-host.js";
import { CatalogOpinions } from "./opinions-host.js";
import { query, kinds } from "./browse.js";
import { helpAnchor, helpTopics } from "./help.js";

export async function activate(context: vscode.ExtensionContext) {
  const host = await executionHost(
    context.globalStorageUri.fsPath,
    vscode.env.remoteName ?? "local",
  );
  const session = new Session(
    context.secrets,
    host,
    transport,
    credentialAccess(context.globalStorageUri.fsPath),
  );
  const browser = new Browser(
    session,
    context.globalState,
    host,
    async (request, signal) => {
      const check = session.guard(session.connection());
      const accepted = await abortable(
        vscode.window.showWarningMessage(
          "Revalidate this resource by sending its original query and filters to administrator-approved external sources?",
          {
            modal: true,
            detail: `This consumer read can repeat peer traffic${request.continuation ? " from a continuation page" : ""}. Query: ${JSON.stringify(request.query)}. Catalog access and unchanged evidence are checked again. No resource target is contacted without its separate consent.`,
          },
          "Revalidate with peers",
        ),
        signal,
      );
      check();
      return accepted === "Revalidate with peers";
    },
  );
  const packageCredentials = new CatalogPackageCredentials(
    context.secrets,
    session,
  );
  const mcp = new CatalogMcp(context, browser, session);
  const plugins: CatalogPlugins = new CatalogPlugins(
    browser,
    session,
    (content) => views.preview(content, "Plugin package preview"),
    packageCredentials,
  );
  const skills = new CatalogSkills(
    browser,
    session,
    mcp,
    plugins,
    (content) => views.preview(content, "Skill package preview"),
    packageCredentials,
  );
  const views: CatalogViews = new CatalogViews(
    context,
    browser,
    session,
    (index) => mcp.select(index),
    (index) => skills.install(index),
    (index) => plugins.install(index),
    (index) => plugins.activate(index),
    (index) => a2a.select(index),
    (index) =>
      browser.state.detail?.item.kind === "plugin"
        ? plugins.compare(index)
        : skills.compare(index),
  );
  const a2a = new CatalogA2A(context, browser, session, (content) =>
    views.preview(content, "Agent transcript", "json"),
  );
  const opinions = new CatalogOpinions(browser, session, (text, title) =>
    views.preview(text, title, "json"),
  );
  context.subscriptions.push(
    views,
    browser,
    mcp,
    skills,
    plugins,
    a2a,
    opinions,
  );
  let selected: string | undefined;
  let selectedDefinition: string | undefined;
  let selecting: Promise<Connection> | undefined;
  let selectionEpoch = 0;
  let rememberedWrites: Promise<void> = Promise.resolve();
  function remember(id?: string): Promise<void> {
    const next = rememberedWrites.then(async () => {
      if (selected !== id) return;
      await context.globalState.update("activeConnection", id);
    });
    rememberedWrites = next.catch(() => {});
    return next;
  }
  const status = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Right,
    100,
  );
  status.command = "openAgenticCatalog.account";
  status.name = "Catalog account";
  function settings(): Connection[] {
    return connections(connectionSettings());
  }
  function show() {
    let connection: Connection | undefined;
    try {
      connection = settings().find((c) => c.id === selected);
    } catch {
      /* Render safe invalid-settings guidance. */
    }
    const account = session.snapshot()?.identity;
    status.text = connection
      ? `$(database) ${connection.label} · ${session.connectionState().replaceAll("_", " ")}`
      : "$(database) Catalog: select connection";
    status.tooltip = connection
      ? `${connection.mode === "control" ? "Authenticated Catalog" : "Published ARD"}; ${vscode.env.remoteName ?? "local"} host; ${connection.origin}${account ? `; ${account.subject}; tenant ${account.tenantId}` : "; connection not verified"}`
      : "Choose a Catalog connection. No connection or credentials are active by default.";
    status.show();
    views.refreshState();
  }
  // Browsing verifies restored credentials too. Keep the account display in
  // step with successful reads, refusals and cleared session state.
  context.subscriptions.push(browser.onChange(show));
  context.subscriptions.push(session.onStatusChange(show));
  async function select(id: string) {
    const connection = settings().find((c) => c.id === id);
    if (!connection) throw new Error("missing connection");
    selected = id;
    selectedDefinition = JSON.stringify(connection);
    const epoch = ++selectionEpoch;
    const operation = (async () => {
      await session.select(connection);
      if (epoch !== selectionEpoch) throw new ClientError("connection_changed");
      const check = session.guard(connection);
      await remember(id);
      check();
      show();
      return connection;
    })();
    selecting = operation;
    try {
      return await operation;
    } finally {
      if (selecting === operation) selecting = undefined;
    }
  }
  async function pick(): Promise<Connection | undefined> {
    const options = settings();
    const choice = await vscode.window.showQuickPick(
      [
        ...options.map((c) => ({
          label: c.label,
          description: `${c.mode} · ${c.auth} · ${c.origin}`,
          id: c.id,
        })),
        {
          label: "Add connection",
          description: "Guided setup; credentials are entered separately.",
          id: "#add",
        },
        ...(options.length
          ? [
              {
                label: "Edit connection",
                description: "Change a saved definition without editing JSON.",
                id: "#edit",
              },
            ]
          : []),
      ],
      { title: "Catalog connections on this host" },
    );
    if (choice?.id === "#add") return saveConnection();
    if (choice?.id === "#edit") {
      await editSaved();
      return undefined;
    }
    if (choice) return select(choice.id);
    return undefined;
  }
  async function ensure(): Promise<Connection | undefined> {
    if (selecting) await selecting;
    return settings().find((c) => c.id === selected) ?? pick();
  }
  async function saveConnection(
    original?: Connection,
  ): Promise<Connection | undefined> {
    const next = await editConnection(original);
    if (!next) return;
    try {
      if (selecting) await selecting;
      if (selected !== next.id || selectedDefinition !== JSON.stringify(next))
        await select(next.id);
      else {
        try {
          session.guard(next);
        } catch {
          await select(next.id);
        }
      }
    } catch (error) {
      if (
        !(error instanceof ClientError) ||
        error.code !== "sign_in_required" ||
        JSON.stringify(session.connection()) !== JSON.stringify(next)
      )
        throw error;
      // Saving a changed definition succeeded; its old credential was refused.
      selected = next.id;
      selectedDefinition = JSON.stringify(next);
      await remember(next.id);
      void vscode.window.showInformationMessage(
        next.auth === "anonymous"
          ? "Connection saved. Test its anonymous publication access."
          : "Connection saved. Sign in again to verify it.",
      );
    }
    show();
    return next;
  }
  async function editSaved() {
    const picked = await vscode.window.showQuickPick(
      settings().map((c) => ({
        label: c.label,
        description: `${c.mode} · ${c.origin}`,
        connection: c,
      })),
      { title: "Edit a Catalog connection" },
    );
    if (picked) await saveConnection(picked.connection);
  }
  async function test(signal?: AbortSignal, expected?: Connection) {
    const connection = await ensure();
    if (!connection) return;
    const check = session.guard(expected ?? connection);
    const result = await session.test(signal);
    check();
    show();
    void vscode.window.showInformationMessage(
      result.identity
        ? `Catalog connection verified for ${result.identity.subject}. Permissions remain enforced by the Catalog.`
        : `Published Catalog verified (${result.entryCount ?? 0} manifest entries).`,
    );
  }
  const command = (
    name: string,
    action: (argument?: unknown) => Promise<unknown>,
  ) => {
    context.subscriptions.push(
      vscode.commands.registerCommand(
        `openAgenticCatalog.${name}`,
        async (argument?: unknown) => {
          try {
            return await action(argument);
          } catch (error) {
            show();
            if (
              ![
                "refresh",
                "search",
                "filterKind",
                "testConnection",
                "connect",
                "addConnection",
                "signIn",
              ].includes(name) ||
              (error instanceof ClientError &&
                [
                  "connection_changed",
                  "selection_changed",
                  "identity_changed",
                ].includes(error.code))
            ) {
              void vscode.window.showErrorMessage(diagnostic(error));
              return undefined;
            }
            const signal = session.cancellation();
            const hint = guidance(
              session.connectionState(),
              !!browser.state.page,
              browser.state.page?.items.length ?? 0,
              browser.state.page?.partial ?? false,
              error instanceof ClientError ? error.code : "request_failed",
            );
            const titles: Record<string, string> = {
              addConnection: "Add connection",
              connect: "Select connection",
              signIn: "Sign in",
              testConnection: "Test connection",
              settings: "Connection settings",
              trust: "Review workspace trust",
              restart: "Start new search",
              help: "Sign-in and trust help",
            };
            const choice = await vscode.window.showErrorMessage(
              `${diagnostic(error)}. ${hint.message}`,
              ...hint.actions.map((action) => titles[action]!),
            );
            const recovery = hint.actions.find(
              (action) => titles[action] === choice,
            );
            if (recovery && !signal.aborted)
              await vscode.commands.executeCommand(
                recovery === "restart"
                  ? "openAgenticCatalog.refresh"
                  : recovery === "trust"
                    ? "workbench.trust.manage"
                    : `openAgenticCatalog.${recovery}`,
                ...(recovery === "help" ? ["connection"] : []),
              );
            return undefined;
          }
        },
      ),
    );
  };
  command("open", async () => {
    views.open();
  });
  command("useMcp", async () => {
    const detail = browser.state.detail;
    if (detail?.item.kind !== "mcp_server")
      throw new ClientError("select_mcp_resource");
    const check = session.guard(session.connection());
    const choices = detail.documents.flatMap((document, index) =>
      consumerFamily(document, "mcp_server")
        ? [
            {
              label: document.label,
              index,
            },
          ]
        : [],
    );
    if (!choices.length) throw new ClientError("native_mcp_document_required");
    const selected = await vscode.window.showQuickPick(choices, {
      title: "Select the native MCP document",
    });
    check();
    if (browser.state.detail !== detail)
      throw new ClientError("selection_changed");
    if (selected) await mcp.select(selected.index);
  });
  command("talkAgent", async () => {
    const detail = browser.state.detail,
      check = session.guard(session.connection());
    if (detail?.item.kind !== "agent")
      throw new ClientError("select_agent_resource");
    const choices = detail.documents.flatMap((d, index) =>
      consumerFamily(d, "agent") ? [{ label: d.label, index }] : [],
    );
    if (!choices.length)
      throw new ClientError("native_agent_document_required");
    const choice = await vscode.window.showQuickPick(choices, {
      title: "Choose an Agent Card",
    });
    check();
    if (browser.state.detail !== detail)
      throw new ClientError("selection_changed");
    if (choice) await a2a.select(choice.index);
  });
  command("removeMcp", async (id) =>
    mcp.remove(
      typeof id === "string"
        ? id
        : typeof id === "object" &&
            id !== null &&
            "mcp" in id &&
            typeof id.mcp === "string"
          ? id.mcp
          : undefined,
    ),
  );
  command("installSkill", async () => skills.install());
  command("removeSkill", async (row) => {
    if (row && typeof row === "object" && "store" in row && "receipt" in row) {
      const current = (await skills.rows()).find(
        (r) =>
          "store" in r &&
          r.store.workspace ===
            (row as { store: { workspace?: string } }).store?.workspace &&
          r.receipt.name ===
            (row as { receipt: { name?: string } }).receipt?.name,
      );
      if (!current || !("store" in current))
        throw new ClientError("selection_changed");
      const expected = (row as { receipt: SkillSummary }).receipt;
      if (expected.fingerprint !== current.receipt.fingerprint)
        throw new ClientError("selection_changed");
      return skills.removeConfirmed(
        current.receipt.name,
        current.store.workspace,
        expected,
      );
    }
    if (row !== undefined) throw new ClientError("selection_changed");
    return skills.remove();
  });
  command("recoverSkill", async (path) =>
    skills.recover(typeof path === "string" ? path : undefined),
  );
  command("installPlugin", async () => plugins.install());
  command("activatePlugin", async () => plugins.activate());
  async function pluginRow(row: unknown, remove: boolean) {
    if (row !== undefined) {
      if (
        !row ||
        typeof row !== "object" ||
        !("pluginStore" in row) ||
        !("receipt" in row)
      )
        throw new ClientError("selection_changed");
      const current = (await plugins.rows()).find(
        (r) =>
          "pluginStore" in r &&
          r.pluginStore.workspace ===
            (row as { pluginStore: { workspace?: string } }).pluginStore
              ?.workspace &&
          r.receipt.name ===
            (row as { receipt: { name?: string } }).receipt?.name,
      );
      if (!current || !("pluginStore" in current))
        throw new ClientError("selection_changed");
      const expected = (row as { receipt: SkillSummary }).receipt;
      if (expected.fingerprint !== current.receipt.fingerprint)
        throw new ClientError("selection_changed");
      return remove
        ? plugins.remove(
            current.receipt.name,
            current.pluginStore.workspace,
            expected,
          )
        : plugins.disable(
            current.receipt.name,
            current.pluginStore.workspace,
            expected,
          );
    }
    return remove ? plugins.remove() : plugins.disable();
  }
  command("disablePlugin", (row) => pluginRow(row, false));
  command("removePlugin", (row) => pluginRow(row, true));
  command("recoverPlugin", async (path) =>
    plugins.recover(typeof path === "string" ? path : undefined),
  );
  command("refreshTools", async () => skills.refresh());
  command("revealTool", async (row) => {
    const rows = (await skills.rows()).filter(
      (r) => "store" in r || "pluginStore" in r,
    );
    const selected =
      row === undefined
        ? (
            await vscode.window.showQuickPick(
              rows.map((value) => ({
                label: value.receipt.name,
                description:
                  "store" in value
                    ? value.store.workspace
                    : value.pluginStore.workspace,
                value,
              })),
              { title: "Reveal an owned package in Explorer" },
            )
          )?.value
        : rows.find((value) => {
            if (!row || typeof row !== "object" || !("receipt" in row))
              return false;
            const r = row as {
              receipt: SkillSummary;
              store?: { workspace?: string };
              pluginStore?: { workspace?: string };
            };
            return (
              value.receipt.name === r.receipt?.name &&
              value.receipt.fingerprint === r.receipt?.fingerprint &&
              ("store" in value
                ? value.store.workspace === r.store?.workspace
                : value.pluginStore.workspace === r.pluginStore?.workspace)
            );
          });
    if (!selected) {
      if (row !== undefined) throw new ClientError("selection_changed");
      return;
    }
    await skills.reveal(selected);
  });
  command("comparePackage", async () => {
    const detail = browser.state.detail,
      check = session.guard(session.connection());
    const packages = detail?.packages ?? [];
    if (!packages.length)
      throw new ClientError("complete_skill_package_required");
    const selected =
      packages.length === 1
        ? { index: 0 }
        : await vscode.window.showQuickPick(
            packages.map((_, index) => ({
              label: `Inspected package ${index + 1}`,
              index,
            })),
            { title: "Choose the exact package to compare" },
          );
    check();
    if (browser.state.detail !== detail)
      throw new ClientError("selection_changed");
    if (selected)
      await (detail!.item.kind === "plugin"
        ? plugins.compare(selected.index)
        : skills.compare(selected.index));
  });
  command("refresh", async () => {
    if (await ensure()) await views.search();
  });
  command("nextPage", async () => {
    if (await ensure()) await views.search(browser.state.query, true);
  });
  command("cancelRead", async () => browser.cancel());
  command("search", async () => {
    if (!(await ensure())) return;
    const connection = session.connection(),
      check = session.guard(connection);
    const value = await vscode.window.showInputBox({
      title: "Search Catalog",
      value: browser.state.query.text,
    });
    if (value === undefined) return;
    check();
    await views.search({ ...browser.state.query, text: value });
  });
  command("forgetPackageCredentials", async () =>
    packageCredentials.forgetAll(),
  );
  command("packageCredentials", async () => {
    if (await ensure()) await packageCredentials.manage();
  });
  command("savedMcp", async () => {
    if (await ensure()) await mcp.saved();
  });
  command("forgetMcpReferences", async () => mcp.forgetReferences());
  command("rate", async () => {
    if (await ensure()) await opinions.rate();
  });
  command("feedback", async () => {
    if (await ensure()) await opinions.feedback();
  });
  command("myRatings", async () => {
    if (await ensure()) await opinions.list("ratings");
  });
  command("myFeedback", async () => {
    if (await ensure()) await opinions.list("feedback");
  });
  command("recoverOpinion", async () => {
    if (await ensure()) await opinions.recover();
  });
  command("combinedFilters", async () => {
    if (!(await ensure())) return;
    if (session.connection().mode !== "control")
      throw new ClientError("control_mode_required");
    const check = session.guard(session.connection()),
      current = browser.state.query;
    const origins = await vscode.window.showQuickPick(
      ["local", "indexed", "live"].map((value) => ({
        label: value,
        picked: (current.origins ?? [current.origin]).includes(
          value as "local",
        ),
        value,
      })),
      {
        title: "Search origins",
        canPickMany: true,
        placeHolder:
          "Choose at least one; Live sends your query to approved peers after consent",
      },
    );
    check();
    if (!origins) return;
    const selectedKinds = await vscode.window.showQuickPick(
      kinds.map((value) => ({
        label: value,
        picked: (current.kinds ?? [current.kind]).includes(value),
        value,
      })),
      { title: "Resource kinds (any selected kind)", canPickMany: true },
    );
    check();
    if (!selectedKinds) return;
    const many = async (key: "tags" | "categories", max: number) => {
      let values = [
        ...(current[key] ??
          (current[key === "tags" ? "tag" : "category"]
            ? [current[key === "tags" ? "tag" : "category"]]
            : [])),
      ];
      for (;;) {
        const choice = await vscode.window.showQuickPick(
          [
            { label: "Use these values", action: "done", value: "" },
            ...(values.length < max
              ? [{ label: "Add exact value", action: "add", value: "" }]
              : []),
            ...values.map((value) => ({
              label: JSON.stringify(value),
              description: "Remove this value",
              action: "remove",
              value,
            })),
          ],
          {
            title: `${key === "categories" ? "Categories (all must match)" : "Tags (all must match)"}: ${values.length}/${max}`,
          },
        );
        check();
        if (!choice) return;
        if (choice.action === "done") return values;
        if (choice.action === "remove")
          values = values.filter((value) => value !== choice.value);
        else {
          const value = await vscode.window.showInputBox({
            title: `Add ${key === "tags" ? "tag" : "category"}`,
            prompt:
              "Exact value, up to 80 Unicode code points. Cancel returns to the list.",
            validateInput: (value) => {
              try {
                query({ [key]: [value] }, false);
                return undefined;
              } catch {
                return "Enter a valid value (1–80 code points).";
              }
            },
          });
          check();
          if (value !== undefined && !values.includes(value))
            values.push(value);
        }
      }
    };
    const tags = await many("tags", 32);
    if (!tags) return;
    const categories = await many("categories", 16);
    if (!categories) return;
    const next = query(
      {
        ...current,
        origins: origins.map((v) => v.value),
        kinds: selectedKinds.map((v) => v.value),
        tags,
        categories,
        kind: "",
        tag: "",
        category: "",
      },
      false,
    );
    check();
    await views.search(next);
  });
  command("filterKind", async () => {
    if (!(await ensure())) return;
    const check = session.guard(session.connection());
    const choices = [
      { label: "All kinds", value: "" },
      { label: "Agents", value: "agent" },
      { label: "MCP Servers", value: "mcp_server" },
      { label: "Skills", value: "skill" },
      { label: "Plugins", value: "plugin" },
    ];
    const choice = await vscode.window.showQuickPick(choices, {
      title: "Catalog resource kind",
    });
    if (!choice) return;
    check();
    const next = { ...browser.state.query, kind: choice.value };
    delete next.kinds;
    await views.search(next);
  });
  command("detail", async (input) => {
    const key =
      typeof input === "string"
        ? input
        : input &&
            typeof input === "object" &&
            "item" in input &&
            browser.state.page?.items.includes(input.item as never)
          ? (input.item as { key: string }).key
          : undefined;
    if (typeof key !== "string") throw new ClientError("selection_changed");
    await views.detail(key);
  });
  function savedRow(input: unknown): {
    saved: number;
    reference: unknown;
    scope: number;
  } {
    if (Number.isSafeInteger(input))
      input = {
        saved: input,
        reference: browser.state.bookmarks[input as number],
        scope: browser.state.scope,
      };
    if (!input || typeof input !== "object")
      throw new ClientError("selection_changed");
    const row = input as { saved: number; reference: unknown; scope: number };
    if (
      !Number.isSafeInteger(row.saved) ||
      row.saved < 0 ||
      row.scope !== browser.state.scope ||
      !browser.state.bookmarks[row.saved] ||
      JSON.stringify(row.reference) !==
        JSON.stringify(browser.state.bookmarks[row.saved])
    )
      throw new ClientError("selection_changed");
    return row;
  }
  async function chooseReference(input?: unknown) {
    if (input !== undefined) return savedRow(input);
    const picked = await vscode.window.showQuickPick(
      browser.state.bookmarks.map((reference, saved) => ({
        label: bookmarkLabel(reference),
        row: { saved, reference: { ...reference }, scope: browser.state.scope },
      })),
      { title: "Choose an exact saved reference" },
    );
    return picked ? savedRow(picked.row) : undefined;
  }
  command("reopen", async (input) => {
    const row = await chooseReference(input);
    if (!row) return;
    await browser.reopen(row.saved);
    views.open();
  });
  command("removeReference", async (input) => {
    const row = await chooseReference(input);
    if (!row) return;
    if (
      (await vscode.window.showWarningMessage(
        "Remove this exact saved reference? Installed resources and Catalog records are unchanged.",
        { modal: true },
        "Remove reference",
      )) !== "Remove reference"
    )
      return;
    savedRow(row);
    await browser.removeBookmark(row.saved);
  });
  command("forgetReferences", async () => {
    const picked = await vscode.window.showQuickPick(
      browser.bookmarkAccounts().map((account) => ({
        label: `Local account binding ${account.key.slice(0, 12)}`,
        description: `${account.count} exact references`,
        account,
      })),
      { title: "Forget saved references for an inaccessible account" },
    );
    if (!picked) return;
    if (
      (await vscode.window.showWarningMessage(
        `Forget ${picked.account.count} locally saved references for binding ${picked.account.key.slice(0, 12)}? This needs no Catalog connection and does not remove installed resources or credentials.`,
        { modal: true },
        "Forget references",
      )) !== "Forget references"
    )
      return;
    await browser.forgetBookmarks(picked.account.key);
  });
  command("openReference", async (input) => {
    if (!(await ensure())) return;
    const check = session.guard(session.connection());
    const value =
      input === undefined
        ? await vscode.window.showInputBox({
            title: "Open copied exact Catalog reference",
            prompt:
              "Paste the complete JSON from Copy exact reference. Local and indexed references only.",
            validateInput: (value) => {
              try {
                exactReference(value);
                return undefined;
              } catch {
                return "Paste a complete local or indexed reference, including exact version fields and local releaseId.";
              }
            },
          })
        : input;
    if (value === undefined) return;
    check();
    if (typeof value !== "string") throw new ClientError("invalid_reference");
    await browser.openReference(value);
    views.open();
  });
  command("settings", async () =>
    vscode.commands.executeCommand(
      "workbench.action.openSettings",
      "@ext:open-agentic-catalog.open-agentic-catalog",
    ),
  );
  command("transportSettings", transportSettings);
  command("editConnection", editSaved);
  command("account", async () => {
    const connection = await ensure();
    if (!connection) return;
    const check = session.guard(connection),
      identity = session.snapshot()?.identity;
    const picked = await vscode.window.showQuickPick(
      [
        { label: "Select connection", action: "connect" },
        { label: "Edit connection", action: "editConnection" },
        { label: "Test connection", action: "testConnection" },
        ...(connection.auth !== "anonymous"
          ? [
              {
                label:
                  session.connectionState() === "signed_out"
                    ? "Sign in"
                    : "Sign in again",
                action: "signIn",
              },
              { label: "Sign out", action: "signOut" },
            ]
          : []),
        { label: "Transport settings", action: "transportSettings" },
        {
          label: "Copy last connection diagnostics",
          action: "copyDiagnostics",
        },
        { label: "Sign-in and OIDC help", action: "help" },
      ],
      {
        title: `${connection.label} · ${connection.mode} · ${session.connectionState().replaceAll("_", " ")}`,
        placeHolder: `${vscode.env.remoteName ?? "local"} host · ${connection.origin}${identity ? ` · ${identity.subject} · tenant ${identity.tenantId}` : " · identity not verified"}`,
      },
    );
    if (picked) {
      check();
      await vscode.commands.executeCommand(
        `openAgenticCatalog.${picked.action}`,
        ...(picked.action === "help" ? ["connection"] : []),
      );
    }
  });
  command("copyDiagnostics", async () => {
    const result = session.diagnostics();
    if (!result) {
      void vscode.window.showInformationMessage(
        "No connection failure diagnostics are available for this session.",
      );
      return;
    }
    await vscode.env.clipboard.writeText(JSON.stringify(result, null, 2));
    void vscode.window.showInformationMessage(
      "Copied the operation, safe error code and available HTTP status/request ID. No credentials, queries or response bodies are included.",
    );
  });
  command("forgetMcpCredentials", async () => mcp.forget());
  command("recoverLogin", async () => {
    const connection = await ensure();
    if (!connection) return;
    const check = session.guard(connection);
    if (
      (await vscode.window.showWarningMessage(
        "Close all other Catalog windows on this host before recovering this connection. This clears its Catalog credentials and abandoned renewal lock; a fresh sign-in is required.",
        { modal: true },
        "Other windows closed: recover",
      )) !== "Other windows closed: recover"
    )
      return;
    check();
    await session.recoverCredentials();
    show();
  });
  command("connect", pick);
  async function verify(expected?: Connection) {
    return vscode.window.withProgress(
      {
        location: vscode.ProgressLocation.Notification,
        title: "Testing Catalog connection",
        cancellable: true,
      },
      async (_, token) => {
        const controller = new AbortController();
        const subscription = token.onCancellationRequested(() =>
          controller.abort(),
        );
        try {
          if (token.isCancellationRequested) controller.abort();
          await test(controller.signal, expected);
        } finally {
          subscription.dispose();
        }
      },
    );
  }
  command("testConnection", () => verify());
  command("signIn", async () => {
    const connection = await ensure();
    if (!connection) return;
    // A prompt or browser handoff must not retarget credentials after a switch.
    const check = session.guard(connection);
    if (connection.auth === "anonymous") {
      await test();
      return;
    }
    if (connection.auth === "manual") {
      const token = await vscode.window.showInputBox({
        title: `Catalog token for ${connection.label}`,
        prompt:
          "Use an access token for this Catalog. It is stored only in VS Code SecretStorage on this host.",
        password: true,
        ignoreFocusOut: true,
      });
      if (token === undefined) return;
      check();
      await session.manual(token);
      await verify(connection);
    } else {
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: `Signing in to ${connection.label}`,
          cancellable: true,
        },
        async (_, token) => {
          const controller = new AbortController();
          let currentCheck = check;
          const cancel = () => {
            controller.abort();
            try {
              currentCheck();
              session.invalidate();
            } catch {
              /* A later connection owns its own login. */
            }
          };
          const subscription = token.onCancellationRequested(cancel);
          try {
            if (token.isCancellationRequested) cancel();
            const callback = vscode.Uri.parse(
              `${vscode.env.uriScheme}://${context.extension.id}/oidc`,
            );
            const redirect = await vscode.env.asExternalUri(callback);
            currentCheck();
            controller.signal.throwIfAborted();
            const url = await session.startLogin(
              redirect.toString(true),
              callback.toString(true),
              controller.signal,
            );
            currentCheck = session.guard(connection);
            controller.signal.throwIfAborted();
            const completed = session.waitForLogin(controller.signal);
            void completed.catch(() => {});
            const opened = await vscode.env.openExternal(vscode.Uri.parse(url));
            currentCheck();
            if (!opened) {
              session.invalidate();
              throw new ClientError("browser_unavailable");
            }
            await completed;
          } finally {
            subscription.dispose();
          }
        },
      );
    }
  });
  command("signOut", async () => {
    await session.signOut();
    show();
  });
  command("addConnection", async () => saveConnection());
  command("removeConnection", async () => {
    const connection = await ensure();
    if (!connection) return;
    const check = session.guard(connection);
    if (
      (await vscode.window.showWarningMessage(
        `Remove ${connection.label} and its Catalog credentials? Installed resources remain on disk.`,
        { modal: true },
        "Remove",
      )) !== "Remove"
    )
      return;
    check();
    try {
      await session.remove(connection.id);
    } catch {
      throw new ClientError("connection_removal_incomplete");
    }
    const fresh = settings().find((c) => c.id === connection.id);
    if (JSON.stringify(fresh) !== JSON.stringify(connection))
      throw new ClientError("connection_changed");
    const configuration =
      vscode.workspace.getConfiguration("openAgenticCatalog");
    const values = configuration
      .get<unknown[]>("connections", [])
      .filter(
        (v) =>
          typeof v === "object" &&
          v !== null &&
          "id" in v &&
          v.id !== connection.id,
      );
    try {
      await configuration.update(
        "connections",
        values,
        vscode.ConfigurationTarget.Global,
      );
    } catch {
      throw new ClientError("connection_removal_incomplete");
    }
    if (
      selected === connection.id &&
      selectedDefinition === JSON.stringify(connection)
    ) {
      selectionEpoch++;
      selected = undefined;
      selectedDefinition = undefined;
      await remember();
    } else if (
      !selected &&
      context.globalState.get("activeConnection") === connection.id
    )
      await remember();
    show();
  });
  command("help", async (input) => {
    const topic =
      input === undefined
        ? await vscode.window.showQuickPick(
            helpTopics.map((item) => ({ ...item })),
            {
              title: "Catalog Help (offline)",
              placeHolder: "Choose a section of the installed user guide",
            },
          )
        : { id: input };
    if (!topic) return;
    const fragment = helpAnchor(topic.id);
    // Only these two maintained layouts exist: source README.md and VSCE readme.md.
    let guide: vscode.Uri | undefined;
    for (const name of ["README.md", "readme.md"]) {
      const candidate = vscode.Uri.joinPath(context.extensionUri, name);
      try {
        const info = await vscode.workspace.fs.stat(candidate);
        if (info.type === vscode.FileType.File) {
          guide = candidate;
          break;
        }
      } catch {
        /* Try the other exact layout. */
      }
    }
    if (!guide) throw new ClientError("help_unavailable");
    await vscode.commands.executeCommand(
      "markdown.showPreview",
      guide.with({ fragment }),
    );
  });
  context.subscriptions.push(
    status,
    vscode.window.registerUriHandler({
      async handleUri(uri) {
        try {
          const connection = session.connection(),
            check = session.guard(connection);
          // VS Code's default serialization escapes query separators. Preserve
          // the OAuth query; finishLogin still validates destination/state/issuer.
          await session.callback(uri.toString(true));
          check();
          await verify(connection);
        } catch (error) {
          show();
          void vscode.window.showErrorMessage(diagnostic(error));
        }
      },
    }),
    vscode.workspace.onDidChangeConfiguration((event) => {
      if (!event.affectsConfiguration("openAgenticCatalog.connections")) return;
      const id = selected;
      try {
        const next = settings().find((c) => c.id === id);
        if (JSON.stringify(next) === selectedDefinition) return;
        selectedDefinition = undefined;
        session.invalidate();
        if (next) {
          const changing = select(next.id),
            epoch = selectionEpoch;
          void changing.catch((error) => {
            if (
              selectionEpoch === epoch &&
              selected === id &&
              !(
                error instanceof ClientError &&
                error.code === "sign_in_required"
              )
            )
              selected = undefined;
            show();
          });
        } else {
          selected = undefined;
          void session.dispose().catch(() => {});
          show();
        }
      } catch {
        selected = undefined;
        session.invalidate();
        show();
      }
    }),
    context.secrets.onDidChange((event) => {
      void session
        .secretsChanged(event.key)
        .then(show)
        .catch(() => {
          show();
        });
    }),
    {
      dispose: () => {
        void session.dispose().catch(() => {});
      },
    },
  );
  const remembered = context.globalState.get<string>("activeConnection");
  if (remembered) {
    try {
      await select(remembered);
    } catch {
      selected = undefined;
    }
  }
  show();
  // Test and future native consumers share the host-owned browser; no credentials leave it.
  return {
    select,
    browse: (input?: unknown) => browser.search(input),
    next: () => browser.search(browser.state.query, true),
    explore: (input?: unknown) => browser.explore(input),
    cancelRead: () => browser.cancel(),
    back: () => browser.back(),
    detail: (key: string) => browser.select(key),
    document: (index: number) => browser.document(index),
    manifest: () => browser.manifest(),
    saveBookmark: () => browser.saveBookmark(),
    reopen: (index: number) => browser.reopen(index),
    removeBookmark: (index: number) => browser.removeBookmark(index),
    installSkill: (index: number, path?: string) => skills.install(index, path),
    compareSkill: (index: number, path?: string) => skills.compare(index, path),
    installedSkills: async () =>
      (await skills.rows())
        .filter((r) => "store" in r)
        .map((r) => ({ name: r.receipt.name, workspace: r.store.workspace })),
    removeSkill: (name: string, path: string) => skills.remove(name, path),
    recoverSkill: (path: string) => skills.recover(path),
    installPlugin: (index: number, path?: string) =>
      plugins.install(index, path),
    comparePlugin: (index: number, path?: string) =>
      plugins.compare(index, path),
    activatePlugin: (index: number, path?: string) =>
      plugins.activate(index, path),
    installedPlugins: async () =>
      (await plugins.rows())
        .filter((r) => "pluginStore" in r)
        .map((r) => ({
          name: r.receipt.name,
          workspace: r.pluginStore.workspace,
          enabled: r.enabled,
        })),
    disablePlugin: (name: string, path: string) => plugins.disable(name, path),
    removePlugin: (name: string, path: string) => plugins.remove(name, path),
    recoverPlugin: (path: string) => plugins.recover(path),
    talkAgent: (index: number) => a2a.select(index),
    a2aSend: (prompt: string, stream = false) => a2a.send(prompt, stream),
    a2aRead: () => a2a.read(),
    a2aCancel: () => a2a.cancel(),
    a2aDisconnect: () => a2a.conversation?.disconnect(),
    a2aState: () => a2a.conversation?.state,
    useMcp: (index: number) => mcp.select(index),
    mcpSelections: () =>
      [...mcp.selections.selections.values()].map((s) => ({
        id: s.id,
        name: s.recipe.name,
      })),
    removeMcp: (id: string) => mcp.remove(id),
    browsing: () => browser.state,
    authenticate: (token: string) => session.manual(token),
    beginLogin: (redirect: string, nativeCallback?: string) =>
      session.startLogin(redirect, nativeCallback),
    waitForLogin: () => session.waitForLogin(AbortSignal.timeout(60000)),
    completeLogin: (callback: string) => session.callback(callback),
    snapshot: () => session.snapshot(),
    test: () => session.test(),
    signOut: () => session.signOut(),
  };
}
