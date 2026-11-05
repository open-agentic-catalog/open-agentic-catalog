// Local Plugin preparation never retrieves, packages or submits archive bytes.
import { parseJSONObject } from "./json-input";
import { readSkillDocument, skillFieldError } from "./skill-markdown";

export const pluginSchema =
  "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json";
export const pluginMCPSchema =
  "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json";
export type PluginPreparation = {
  preparing: boolean;
  sourceFiles?: { path: string; retained: boolean }[];
  manifest: string;
  mcp: string;
  skills: { id: string; content: string }[];
};
export type PluginFile = { path: string; content: string };
export const jsonText = (value: unknown) =>
  JSON.stringify(value, null, 2) + "\n";
const object = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown) => typeof value === "string";
const strings = (value: unknown) =>
  object(value) && Object.values(value).every(text);

export function newPluginPreparation(): PluginPreparation {
  return {
    preparing: false,
    manifest: jsonText({ $schema: pluginSchema, name: "" }),
    mcp: "",
    skills: [],
  };
}

/** Recognize editable shapes without converting, deleting or repairing advanced values. */
export function pluginManifest(source: string) {
  const value = parseJSONObject(source);
  if (
    value.$schema !== pluginSchema ||
    !text(value.name) ||
    ["version", "description", "license"].some(
      (key) => value[key] !== undefined && !text(value[key]),
    )
  )
    throw new Error(
      "Use the pinned Plugin schema and string fields, or keep the manifest in Advanced JSON.",
    );
  return value;
}
export function pluginMCP(source: string) {
  const value = parseJSONObject(
    source || jsonText({ $schema: pluginMCPSchema, mcpServers: {} }),
  );
  if (value.$schema !== pluginMCPSchema || !object(value.mcpServers))
    throw new Error(
      "Use the Plugin MCP schema and a named mcpServers map. Cards and Registry documents use different formats.",
    );
  for (const server of Object.values(value.mcpServers)) {
    if (
      !object(server) ||
      !["stdio", "streamable-http", "sse"].includes(server.type as string) ||
      ["command", "url", "cwd"].some(
        (key) => server[key] !== undefined && !text(server[key]),
      ) ||
      (server.args !== undefined &&
        (!Array.isArray(server.args) || !server.args.every(text))) ||
      ["env", "headers"].some(
        (key) => server[key] !== undefined && !strings(server[key]),
      )
    )
      throw new Error(
        "These server fields need Advanced MCP JSON. The current text stays unchanged.",
      );
  }
  return value as Record<string, unknown> & {
    mcpServers: Record<string, Record<string, unknown>>;
  };
}

function contained(value: string) {
  if (
    /[\\\0\r\n]/u.test(value) ||
    value.includes("${") ||
    value.startsWith("/")
  )
    return false;
  const parts: string[] = [];
  for (const part of value.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (!parts.length) return false;
      parts.pop();
    } else parts.push(part);
  }
  return !parts.join("/").includes(":");
}
export function pluginEndpointError(value: string): string {
  try {
    // Check the original host. WHATWG URL normalizes shorthand and numeric IPv4 addresses.
    const host = value.match(
      /^https?:\/\/(\[[^\]]+\]|[^/:?#]+)(?::[0-9]+)?(?:[/?#]|$)/iu,
    )?.[1];
    // The backend also permits scoped IPv6 literals, which WHATWG URL does not parse.
    const url = new URL(value.replace(/(\[[\da-f:.]+)%25[^\]]+\]/iu, "$1]"));
    if (
      !host ||
      url.username ||
      url.password ||
      url.hash ||
      /[\\\s]/u.test(value)
    )
      throw new Error();
    if (url.protocol === "https:") return "";
    const loopback =
      host === "localhost" ||
      (host.startsWith("[") &&
        (url.hostname === "[::1]" ||
          /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/u.test(url.hostname))) ||
      (/^127\.(?:0|[1-9][0-9]{0,2})\.(?:0|[1-9][0-9]{0,2})\.(?:0|[1-9][0-9]{0,2})$/u.test(
        host,
      ) &&
        host.split(".").every((part) => Number(part) <= 255));
    if (url.protocol === "http:" && loopback) return "";
  } catch {
    /* Return the same field guidance for malformed and disallowed URLs. */
  }
  return "Use HTTPS, or HTTP with exact localhost or a loopback IP address. Do not include credentials or a fragment.";
}

export function pluginServerError(server: Record<string, unknown>): string {
  if (server.type === "stdio") {
    const command = server.command as string | undefined;
    if (
      !command ||
      /[\\\0]|\p{White_Space}/u.test(command) ||
      command.includes("${") ||
      (command.includes("/") &&
        (!command.startsWith("./") || !contained(command.slice(2))))
    )
      return "Enter one executable name or a contained ./path. Put each argument in a separate row.";
    if (
      object(server.env) &&
      ["PLUGIN_ROOT", "PLUGIN_DATA"].some((key) =>
        Object.hasOwn(server.env as object, key),
      )
    )
      return "Do not set PLUGIN_ROOT or PLUGIN_DATA. The Plugin consumer supplies them.";
    const cwd = server.cwd as string | undefined;
    if (
      cwd &&
      !["${PLUGIN_ROOT}", "${PLUGIN_DATA}"].some(
        (base) =>
          cwd === base ||
          (cwd.startsWith(base + "/") && contained(cwd.slice(base.length + 1))),
      ) &&
      !(cwd.startsWith("./") && contained(cwd.slice(2)))
    )
      return "Use a contained ./path, ${PLUGIN_ROOT} or ${PLUGIN_DATA} for the working directory.";
  } else {
    const error = pluginEndpointError((server.url as string | undefined) ?? "");
    if (error) return error;
    const seen = new Set<string>();
    for (const [key, value] of Object.entries(
      (server.headers as Record<string, string> | undefined) ?? {},
    )) {
      const lower = key.toLowerCase();
      if (
        !/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(key) ||
        seen.has(lower) ||
        ["authorization", "proxy-authorization", "cookie"].includes(lower) ||
        [...value].some(
          (character) =>
            (character.codePointAt(0)! < 32 && character !== "\t") ||
            character === "\x7f",
        )
      )
        return "Use distinct valid header names and non-secret values. Do not supply Authorization, Proxy-Authorization or Cookie.";
      seen.add(lower);
    }
  }
  const allowed =
    server.type === "stdio"
      ? ["type", "command", "args", "env", "cwd"]
      : ["type", "url", "headers"];
  return Object.keys(server).some((key) => !allowed.includes(key))
    ? "Remove unsupported server fields in Advanced MCP JSON."
    : "";
}

/** Validate generated paths and consumed fields before offering packaging instructions. */
export function pluginNameError(name: string): string {
  return /^(?!.*(?:--|\.\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/u.test(name) &&
    name.length <= 64
    ? ""
    : "Use a Plugin name of 1 to 64 lowercase letters, digits, dots or hyphens. Do not repeat dots or hyphens.";
}

export function pluginFiles(preparation: PluginPreparation): PluginFile[] {
  const manifest = pluginManifest(preparation.manifest);
  const nameError = pluginNameError(manifest.name as string);
  if (nameError) throw new Error(nameError);
  const files: PluginFile[] = [
    { path: "plugin.json", content: preparation.manifest },
  ];
  if (preparation.mcp) {
    const mcp = pluginMCP(preparation.mcp);
    for (const [name, server] of Object.entries(mcp.mcpServers)) {
      if (!name) throw new Error("Enter a name for each MCP server.");
      const error = pluginServerError(server);
      if (error) throw new Error(`${name}: ${error}`);
    }
    files.push({ path: "mcp.json", content: preparation.mcp });
  }
  for (const skill of preparation.skills) {
    const { document, reason } = readSkillDocument(skill.content);
    if (!document) throw new Error(reason);
    for (const [key, value] of Object.entries(document)) {
      if (typeof value !== "string") continue;
      const error = skillFieldError(key, value);
      if (error) throw new Error(error);
    }
    const path = `skills/${document.name}/SKILL.md`;
    if (files.some((file) => file.path === path))
      throw new Error("Use a different name for each Skill component.");
    files.push({ path, content: skill.content });
  }
  return files;
}

export function pluginPackaging(name: string, files: PluginFile[]) {
  const members = [
    "plugin.json",
    ...(files.some((file) => file.path === "mcp.json") ? ["mcp.json"] : []),
    ...(files.some((file) => file.path.startsWith("skills/"))
      ? ["skills"]
      : []),
  ].join(" ");
  return `zip -r ../${name}.zip ${members}\ntar -czf ../${name}.tar.gz ${members}`;
}
