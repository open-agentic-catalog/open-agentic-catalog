import { nativeAbsolute } from "./platform.js";
import * as vscode from "vscode";
import {
  ClientError,
  connectionLabel,
  connections,
  safeURL,
  type Connection,
} from "./config.js";

const section = "openAgenticCatalog";
export function connectionSettings(): unknown[] {
  const config = vscode.workspace.getConfiguration(section);
  const inspected = config.inspect<unknown>("connections");
  if (
    inspected &&
    [
      inspected.workspaceValue,
      inspected.workspaceFolderValue,
      inspected.workspaceLanguageValue,
      inspected.workspaceFolderLanguageValue,
    ].some((value) => value !== undefined)
  )
    throw new ClientError("workspace_settings_refused");
  const values = config.get<unknown>("connections", []);
  connections(values);
  return values as unknown[];
}
const back = Symbol("back");
type Answer = string | typeof back | undefined;
type Field = {
  key: string;
  title: string;
  prompt: string;
  choices?: { label: string; value: string; description?: string }[];
  validate?(value: string): void;
  file?: boolean;
};
async function ask(
  field: Field,
  value: string,
  step: number,
  total: number,
): Promise<Answer> {
  const control = field.choices
    ? vscode.window.createQuickPick<vscode.QuickPickItem & { value: string }>()
    : vscode.window.createInputBox();
  control.title = `Catalog connection: ${field.title}`;
  control.step = step;
  control.totalSteps = total;
  control.ignoreFocusOut = true;
  const help = {
    iconPath: new vscode.ThemeIcon("question"),
    tooltip: "Open connection setup help (offline)",
  };
  control.buttons = [
    vscode.QuickInputButtons.Back,
    ...(field.file
      ? [
          {
            iconPath: new vscode.ThemeIcon("folder-opened"),
            tooltip: "Choose a file on this execution host",
          },
        ]
      : []),
    help,
  ];
  if ("items" in control) {
    control.placeholder = field.prompt;
    control.items = field.choices!;
    control.activeItems = control.items.filter((item) => item.value === value);
  } else {
    control.value = value;
    control.prompt = field.prompt;
  }
  const subscriptions: vscode.Disposable[] = [];
  let active = true;
  try {
    return await new Promise<Answer>((resolve) => {
      subscriptions.push(
        control.onDidHide(() => resolve(undefined)),
        control.onDidTriggerButton(async (button) => {
          if (button === help) {
            await vscode.commands.executeCommand(
              "openAgenticCatalog.help",
              "setup",
            );
            return;
          }
          if (button === vscode.QuickInputButtons.Back) {
            resolve(back);
            control.hide();
            return;
          }
          if (!field.file || !("value" in control)) return;
          try {
            const picked = await vscode.window.showOpenDialog({
              title: field.title,
              canSelectMany: false,
              canSelectFiles: true,
              canSelectFolders: false,
            });
            if (active && picked?.[0]) control.value = picked[0].fsPath;
          } catch {
            if (active && "validationMessage" in control)
              control.validationMessage =
                "File selection failed. Enter an absolute path manually or cancel.";
          }
        }),
        control.onDidAccept(() => {
          const result =
            "selectedItems" in control
              ? control.selectedItems[0]?.value
              : control.value;
          if (result === undefined) return;
          try {
            field.validate?.(result);
          } catch (error) {
            if ("validationMessage" in control)
              control.validationMessage =
                error instanceof ClientError &&
                error.code === "connection_id_exists"
                  ? "This connection ID already exists. Choose another ID."
                  : `Enter a valid ${field.title.toLowerCase()}. ${field.prompt}`;
            return;
          }
          resolve(result);
          control.hide();
        }),
      );
      if ("onDidChangeValue" in control && "validationMessage" in control)
        subscriptions.push(
          control.onDidChangeValue(() => {
            control.validationMessage = undefined;
          }),
        );
      control.show();
    });
  } finally {
    active = false;
    for (const item of subscriptions) item.dispose();
    control.dispose();
  }
}
const choices = (values: [string, string, string?][]) =>
  values.map(([value, label, description]) => ({
    value,
    label,
    ...(description ? { description } : {}),
  }));

/** Native settings are the only definition store. No issuer is contacted here. */
export async function editConnection(
  original?: Connection,
): Promise<Connection | undefined> {
  const raw = connectionSettings();
  const originalRaw = raw.find(
    (value) => (value as Connection).id === original?.id,
  );
  const draft: Record<string, string> = {
    id: original?.id ?? "",
    label: original?.label ?? "",
    origin: original?.origin ?? "",
    mode: original?.mode ?? "control",
    auth: original?.auth ?? "oidc",
    allowLoopbackHTTP: String(original?.allowLoopbackHTTP ?? false),
    issuer: original?.oidc?.issuer ?? "",
    clientId: original?.oidc?.clientId ?? "",
    audience: original?.oidc?.audience ?? "",
    scopes: original?.oidc?.scopes.join(" ") ?? "openid catalog.read",
    refresh: String(original?.oidc?.refresh ?? false),
    transport: "defaults",
    caFile: original?.caFile ?? "",
    proxyURL: original?.proxyURL ?? "",
    timeoutMs: String(original?.timeoutMs ?? 30000),
    requestBytes: String(original?.requestBytes ?? 16777216),
    responseBytes: String(original?.responseBytes ?? 8388608),
    headerBytes: String(original?.headerBytes ?? 32768),
  };
  const value = (): Connection =>
    connections([
      {
        id: draft.id,
        label: draft.label,
        origin: draft.origin,
        mode: draft.mode,
        auth: draft.auth,
        allowLoopbackHTTP: draft.allowLoopbackHTTP === "true",
        timeoutMs: Number(draft.timeoutMs),
        requestBytes: Number(draft.requestBytes),
        responseBytes: Number(draft.responseBytes),
        headerBytes: Number(draft.headerBytes),
        ...(draft.caFile ? { caFile: draft.caFile } : {}),
        ...(draft.proxyURL ? { proxyURL: draft.proxyURL } : {}),
        ...(draft.auth === "oidc"
          ? {
              oidc: {
                issuer: draft.issuer,
                clientId: draft.clientId,
                audience: draft.audience,
                scopes: draft.scopes!.trim().split(/\s+/u),
                refresh: draft.refresh === "true",
              },
            }
          : {}),
      },
    ])[0]!;
  const required = (text: string) => {
    if (!text || /[\p{Cc}\p{Cs}]/u.test(text) || text.length > 512)
      throw new Error();
  };
  const fields = (): Field[] => [
    ...(!original
      ? [
          {
            key: "id",
            title: "Connection ID",
            prompt:
              "Start with a lowercase letter; use up to 40 letters, digits or hyphens.",
            validate: (id: string) => {
              if (!/^[a-z][a-z0-9-]{0,39}$/u.test(id)) throw new Error();
              if (connectionSettings().some((v) => (v as Connection).id === id))
                throw new ClientError("connection_id_exists");
            },
          },
        ]
      : []),
    {
      key: "label",
      title: "Connection name",
      prompt: "A readable name, up to 80 Unicode characters.",
      validate: connectionLabel,
    },
    {
      key: "allowLoopbackHTTP",
      title: "Connection security",
      prompt:
        "HTTPS is standard. Literal loopback HTTP is only for development credentials.",
      choices: choices([
        ["false", "HTTPS only"],
        [
          "true",
          "Allow literal loopback HTTP",
          "Only 127.0.0.1 or ::1; never use production credentials.",
        ],
      ]),
    },
    {
      key: "origin",
      title: "Catalog origin",
      prompt: "Root HTTPS URL, without credentials, path or query.",
      validate: (v) => {
        safeURL(v, draft.allowLoopbackHTTP === "true");
      },
    },
    {
      key: "mode",
      title: "Catalog API mode",
      prompt:
        "Control reads authorized resources; Published reads ARD discovery.",
      choices: choices([
        ["control", "Authenticated Control API"],
        ["published", "Published ARD discovery"],
      ]),
    },
    {
      key: "auth",
      title: "Authentication",
      prompt:
        "Browser login uses your issuer; token entry uses an existing access token.",
      choices: choices([
        ["oidc", "Browser login (OIDC/PKCE)"],
        ["manual", "Enter an access token"],
        ...(draft.mode === "published"
          ? [["anonymous", "Anonymous publication reads"] as [string, string]]
          : []),
      ]),
    },
    ...(draft.auth === "oidc"
      ? [
          {
            key: "issuer",
            title: "OIDC issuer",
            prompt: "Exact issuer URL for the extension's public client.",
            validate: (v: string) => {
              safeURL(v, draft.allowLoopbackHTTP === "true", false);
            },
          },
          {
            key: "clientId",
            title: "OIDC public client ID",
            prompt:
              "Register a separate public client; no client secret is used.",
            validate: required,
          },
          {
            key: "audience",
            title: "Catalog token audience",
            prompt: "The access-token audience expected by this Catalog.",
            validate: required,
          },
          {
            key: "refresh",
            title: "Optional token refresh",
            prompt:
              "Enable only when this issuer permits refresh tokens for the extension.",
            choices: choices([
              ["false", "Sign in again when the token expires"],
              [
                "true",
                "Request refresh tokens",
                "Requests offline_access and explicit issuer consent.",
              ],
            ]),
          },
          {
            key: "scopes",
            title: "Requested scopes",
            prompt:
              "Space-separated scopes, including openid. offline_access requires refresh enabled.",
            validate: (v: string) => {
              connections([
                {
                  id: "check",
                  label: "Check",
                  origin: "https://catalog.example",
                  mode: "control",
                  auth: "oidc",
                  oidc: {
                    issuer: "https://issuer.example",
                    clientId: "check",
                    audience: "catalog",
                    scopes: v.trim().split(/\s+/u),
                    refresh: draft.refresh === "true",
                  },
                },
              ]);
            },
          },
        ]
      : []),
    {
      key: "transport",
      title: "Optional transport settings",
      prompt:
        "Defaults preserve the current values. Customize private trust, proxy or request limits.",
      choices: choices([
        [
          "defaults",
          original ? "Keep current transport settings" : "Use defaults",
        ],
        ["custom", "Customize transport settings"],
      ]),
    },
    ...(draft.transport === "custom"
      ? [
          {
            key: "caFile",
            title: "Catalog CA file",
            prompt:
              "Optional absolute PEM file on this execution host; empty uses public trust.",
            file: true,
            validate: (v: string) => {
              if (v && !nativeAbsolute(v)) throw new Error();
            },
          },
          {
            key: "proxyURL",
            title: "Catalog proxy",
            prompt: "Optional credential-free HTTP(S) proxy origin.",
            validate: (v: string) => {
              if (v)
                connections([
                  {
                    id: "check",
                    label: "Check",
                    origin: "https://catalog.example",
                    mode: "control",
                    auth: "manual",
                    proxyURL: v,
                  },
                ]);
            },
          },
          ...(
            [
              ["timeoutMs", "Request timeout (milliseconds)", 1000, 120000],
              ["requestBytes", "Request limit (bytes)", 1024, 16777216],
              ["responseBytes", "Response limit (bytes)", 1024, 33554432],
              ["headerBytes", "Header limit (bytes)", 1024, 65536],
            ] as const
          ).map(([key, title, min, max]) => ({
            key,
            title,
            prompt: `Whole number from ${min} to ${max}.`,
            validate: (v: string) => {
              if (
                !/^\d+$/u.test(v) ||
                !Number.isSafeInteger(Number(v)) ||
                Number(v) < min ||
                Number(v) > max
              )
                throw new Error();
            },
          })),
        ]
      : []),
    {
      key: "review",
      title: "Review and save",
      prompt: `${draft.label} | ${draft.origin} | ${draft.mode} / ${draft.auth}. Secrets are entered separately after saving.`,
      choices: choices([
        [
          "save",
          "Save connection",
          "Back changes the previous field; Escape cancels without saving.",
        ],
      ]),
    },
  ];
  let step = 0;
  for (;;) {
    if (draft.mode === "control" && draft.auth === "anonymous")
      draft.auth = "oidc";
    const flow = fields(),
      field = flow[step]!;
    const answer = await ask(
      field,
      draft[field.key] ?? "",
      step + 1,
      flow.length,
    );
    if (answer === undefined) return;
    if (answer === back) {
      if (step === 0) return;
      step--;
      continue;
    }
    draft[field.key] = answer;
    if (field.key !== "review") {
      step++;
      continue;
    }
    const candidate = value();
    const fresh = connectionSettings();
    if (original) {
      const index = fresh.findIndex(
        (item) => (item as Connection).id === original.id,
      );
      if (
        index < 0 ||
        JSON.stringify(fresh[index]) !== JSON.stringify(originalRaw)
      )
        throw new ClientError("connection_changed");
      fresh[index] = candidate;
    } else {
      if (fresh.some((item) => (item as Connection).id === candidate.id))
        throw new ClientError("connection_id_exists");
      fresh.push(candidate);
    }
    connections(fresh);
    try {
      await vscode.workspace
        .getConfiguration(section)
        .update("connections", fresh, vscode.ConfigurationTarget.Global);
    } catch {
      throw new ClientError("settings_write_unconfirmed");
    }
    const committed = connections(connectionSettings()).find(
      (item) => item.id === candidate.id,
    );
    if (JSON.stringify(committed) !== JSON.stringify(candidate))
      throw new ClientError("settings_write_unconfirmed");
    return candidate;
  }
}

export async function transportSettings(): Promise<void> {
  const choice = await vscode.window.showQuickPick(
    [
      {
        label: "Catalog connection",
        description:
          "Origin, PEM trust, proxy, timeout and request/response/header bytes.",
        id: "connections",
      },
      {
        label: "Skill and Plugin package tools",
        description:
          "Git/ORAS executable paths, separate PEM trust and acquisition timeout (1000-300000 ms). No package proxy setting.",
        id: "packageTools",
      },
      {
        label: "Agent (A2A) transport",
        description:
          "Separate PEM trust/proxy, loopback policy, request timeout and stream timeout.",
        id: "a2a",
      },
      {
        label: "Native MCP transport and OAuth",
        description:
          "VS Code owns MCP trust and authentication; Catalog settings do not configure it.",
        id: "mcp",
      },
      {
        label: "Transport help",
        description:
          "Open the offline connection, package and Agent transport guide.",
        id: "help",
      },
    ],
    { title: `Transport settings on ${vscode.env.remoteName ?? "local"} host` },
  );
  if (!choice) return;
  if (choice.id === "help") {
    await vscode.commands.executeCommand("openAgenticCatalog.help", "trust");
    return;
  }
  if (choice.id === "mcp") {
    await vscode.commands.executeCommand(
      "workbench.action.openSettings",
      "mcp",
    );
    return;
  }
  if (choice.id !== "connections") {
    const group = choice.id;
    const fields =
      group === "packageTools" ? ["gitPath", "orasPath", "caFile"] : ["caFile"];
    const field = await vscode.window.showQuickPick(
      [
        { label: "Open native settings", key: "settings" },
        ...fields.map((key) => ({ label: `Choose or enter ${key}`, key })),
      ],
      { title: choice.label },
    );
    if (!field) return;
    if (field.key !== "settings") {
      const read = () => {
        const config = vscode.workspace.getConfiguration(section),
          scope = config.inspect<Record<string, unknown>>(group);
        if (
          scope &&
          [
            scope.workspaceValue,
            scope.workspaceFolderValue,
            scope.workspaceLanguageValue,
            scope.workspaceFolderLanguageValue,
          ].some((v) => v !== undefined)
        )
          throw new ClientError("workspace_settings_refused");
        const value = config.get<Record<string, unknown>>(group, {});
        if (!value || typeof value !== "object" || Array.isArray(value))
          throw new ClientError("invalid_configuration");
        return { ...value };
      };
      const original = read()[field.key];
      const value = await ask(
        {
          key: field.key,
          title: field.key,
          prompt: `Optional absolute path on ${vscode.env.remoteName ?? "local"} host. Empty restores the default. Catalog, package and Agent trust are separate.`,
          file: true,
          validate: (v) => {
            if (v && (!nativeAbsolute(v) || /[\p{Cc}\p{Cs}]/u.test(v)))
              throw new Error();
          },
        },
        typeof original === "string" ? original : "",
        1,
        1,
      );
      if (value === undefined || value === back) return;
      const fresh = read();
      if (fresh[field.key] !== original)
        throw new ClientError("connection_changed");
      if (value) fresh[field.key] = value;
      else delete fresh[field.key];
      await vscode.workspace
        .getConfiguration(section)
        .update(group, fresh, vscode.ConfigurationTarget.Global);
      return;
    }
  }
  await vscode.commands.executeCommand(
    "workbench.action.openSettings",
    `@id:openAgenticCatalog.${choice.id}`,
  );
}
