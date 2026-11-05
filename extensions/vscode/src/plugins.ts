import { nativeAbsolute } from "./platform.js";
import { record, safeURL } from "./config.js";
import { decodeJSON } from "./json.js";
import {
  inventory,
  packageDigest,
  packageFail,
  packageHash,
  packageLimits,
  packageNumber,
  packagePath,
  verifyPackage,
  type SkillPlan,
} from "./packages.js";
import type { PackageContent } from "./archive.js";

export function pluginName(value: unknown): string {
  if (
    typeof value !== "string" ||
    value.length > 64 ||
    !/^(?!.*(?:--|\.\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/u.test(value)
  )
    return packageFail("invalid_plugin_package");
  return packagePath(value);
}
/** Only Catalog-inspected immutable local evidence qualifies for installation. */
export function pluginPlan(value: unknown, binding: string): SkillPlan {
  const r = record(value),
    p = record(r.plugin);
  if (
    r.validationState !== "valid" ||
    r.profileId !== "agent-plugins-1.0.0-1fc1b627" ||
    ![
      "application/agent-plugins+zip",
      "application/agent-plugins+gzip",
    ].includes(String(r.mediaType))
  )
    return packageFail("complete_plugin_package_required");
  const root = p.rootDirectory;
  if (
    typeof root !== "string" ||
    (root && (packagePath(root) !== root || root.includes("/")))
  )
    return packageFail("invalid_plugin_package");
  if (
    !Array.isArray(p.documents) ||
    !p.documents.length ||
    p.documents.length > 130 ||
    !Array.isArray(p.files)
  )
    return packageFail("invalid_plugin_package");
  const prefix = root ? root + "/" : "";
  const documents = new Map<string, string>();
  for (const entry of p.documents) {
    const d = record(entry),
      path = packagePath(d.path);
    if (
      !path.startsWith(prefix) ||
      typeof d.content !== "string" ||
      Buffer.byteLength(d.content) > 500000 ||
      documents.has(path)
    )
      return packageFail("invalid_plugin_package");
    documents.set(path, d.content);
  }
  const manifestText = documents.get(prefix + "plugin.json");
  if (!manifestText) return packageFail("complete_plugin_package_required");
  const manifest = record(decodeJSON(manifestText));
  if (
    manifest.$schema !==
    "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
  )
    return packageFail("unsupported_plugin_profile");
  const name = pluginName(manifest.name);
  const files = inventory(
    p.files.map((entry) => {
      const f = record(entry),
        path = packagePath(f.path);
      if (!path.startsWith(prefix) || path === root)
        return packageFail("unsafe_package_path");
      const text = documents.get(path);
      if (
        text !== undefined &&
        (packageHash(text) !== f.digest || Buffer.byteLength(text) !== f.size)
      )
        return packageFail("package_integrity_failed");
      return { ...f, path: name + "/" + path.slice(prefix.length) };
    }),
    name,
    "plugin.json",
  );
  if (
    files.reduce((sum, f) => sum + f.size, 0) !== p.expandedSize ||
    [...documents.keys()].some(
      (path) =>
        !files.some((f) => f.path === name + "/" + path.slice(prefix.length)),
    )
  )
    return packageFail("invalid_plugin_package");
  const url = safeURL(r.url, false, false);
  const plan: SkillPlan = {
    kind: "plugin",
    archiveRoot: root,
    name,
    mediaType: String(r.mediaType),
    archiveDigest: packageDigest(r.contentDigest),
    archiveSize: packageNumber(r.size, packageLimits.archive, 1),
    files,
    sources: [{ type: "https", url }],
    binding,
    identity: "",
  };
  plan.identity = packageHash(JSON.stringify(plan));
  return plan;
}
export interface PluginPreview {
  text: string;
  refusals: string[];
}
/** Native interpretation is qualified against VS Code 1.139.1, not an execution sandbox. */
export function pluginPreview(
  plan: SkillPlan,
  content: PackageContent,
): PluginPreview {
  verifyPackage(plan, content);
  const lines = [
      "Whole-package activation can expose instructions and start hooks or MCP processes. Native policy and trust controls still apply.",
      "Local VS Code ignores hook matcher values. No per-component enablement is promised.",
    ],
    refusals: string[] = [];
  const text = (path: string) => {
    const bytes = content.get(plan.name + "/" + path)!.bytes;
    if (bytes.length > 500000) return packageFail("plugin_preview_limit");
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  };
  const manifest = record(decodeJSON(text("plugin.json")));
  if (pluginName(manifest.name) !== plan.name)
    return packageFail("package_integrity_failed");
  if (manifest.extensions !== undefined) {
    const extensions = record(manifest.extensions);
    for (const [namespace, value] of Object.entries(extensions)) {
      lines.push(`Extension ${namespace}: ${JSON.stringify(value)}`);
      if (
        namespace === "com.github.copilot" &&
        Object.keys(record(value)).length
      )
        refusals.push(
          "Custom Copilot component overrides are not qualified. Inspect the package; activation is unavailable.",
        );
      else if (namespace !== "com.github.copilot")
        lines.push(
          `VS Code does not load the ${namespace} extension through this workflow.`,
        );
    }
  }
  let displayed = 0;
  for (const file of plan.files) {
    const path = file.path.slice(plan.name.length + 1);
    lines.push(
      `${path}: ${file.size} bytes; ${file.executable ? "executable" : "regular"}; ${file.digest}`,
    );
    const native =
      path === "plugin.json" ||
      path === "mcp.json" ||
      (path.startsWith("skills/") && path.endsWith("/SKILL.md")) ||
      path.startsWith("com.github.copilot/") ||
      path.startsWith("automations/");
    if (!native) continue;
    if (file.size > 500000 || (displayed += file.size) > 2 * 1024 ** 2)
      return packageFail("plugin_preview_limit");
    const body = text(path);
    lines.push("--- " + path + " ---", body);
    if (path === "mcp.json" || path.endsWith("/hooks.json")) {
      const value = record(decodeJSON(body));
      if (/\$\{|\$[A-Z_]/u.test(JSON.stringify(value)))
        refusals.push(
          `${path}: placeholders are not safely supported by this native format.`,
        );
      if (
        path.endsWith("/hooks.json") &&
        /"matcher"\s*:/u.test(JSON.stringify(value))
      )
        refusals.push(
          `${path}: Local hook matchers are ignored; activation would broaden execution.`,
        );
      if (path === "mcp.json") {
        for (const [server, entry] of Object.entries(
          record(value.mcpServers),
        )) {
          const m = record(entry);
          if (
            m.type === "stdio" &&
            (typeof m.command !== "string" ||
              m.command.startsWith(".") ||
              (/[\\/]/u.test(m.command) && !nativeAbsolute(m.command)) ||
              m.command.includes("\\") ||
              m.cwd !== undefined)
          )
            refusals.push(
              `${server}: relative executables or cwd semantics are not qualified.`,
            );
          if (m.type === "sse")
            refusals.push(`${server}: legacy SSE is not qualified.`);
          if (m.type === "streamable-http") safeURL(m.url, false, false);
          if (
            m.headers &&
            Object.keys(record(m.headers)).some((key) =>
              /authorization|cookie|token|key|secret/iu.test(key),
            )
          )
            refusals.push(
              `${server}: package credentials are refused. Configure target credentials in the native host.`,
            );
          if (
            m.env &&
            Object.keys(record(m.env)).some((key) =>
              /token|password|key|secret/iu.test(key),
            )
          )
            refusals.push(
              `${server}: embedded secret-like environment values are refused.`,
            );
        }
      }
    }
  }
  return { text: lines.join("\n"), refusals: [...new Set(refusals)] };
}
export interface LocationsState {
  target: unknown;
  effective: unknown;
  shadowed: boolean;
}
export interface LocationsPort {
  read(): LocationsState;
  write(value: Record<string, boolean>): Promise<void>;
}
export function locationMap(value: unknown): Record<string, boolean> {
  if (value === undefined) return {};
  const r = record(value);
  if (
    Object.keys(r).length > 2048 ||
    Object.values(r).some((v) => typeof v !== "boolean")
  )
    return packageFail("invalid_plugin_locations");
  return { ...r } as Record<string, boolean>;
}
/** VS Code exposes no compare-and-swap configuration API. Read immediately before each write. */
export class PluginLocations {
  constructor(private port: LocationsPort) {}
  private target(path: string): Record<string, boolean> {
    const state = this.port.read();
    if (state.shadowed)
      return packageFail("plugin_configuration_scope_conflict");
    const target = locationMap(state.target),
      effective = locationMap(state.effective);
    if (target[path] !== effective[path])
      return packageFail("plugin_configuration_changed");
    return target;
  }
  value(path: string): boolean | undefined {
    return this.target(path)[path];
  }
  async set(
    path: string,
    expected: boolean | undefined,
    next: boolean | undefined,
    guard: () => void,
  ): Promise<void> {
    const target = this.target(path);
    if (target[path] !== expected)
      return packageFail("plugin_configuration_changed");
    guard();
    if (next === undefined) delete target[path];
    else target[path] = next;
    try {
      await this.port.write(target);
      guard();
      if (this.value(path) !== next)
        return packageFail("plugin_configuration_changed");
    } catch (error) {
      // An uncertain native write or late identity change must not leave our
      // newly enabled path active. Disable only this key using fresh settings.
      if (next === true) {
        try {
          const fresh = this.target(path);
          if (fresh[path] === true) {
            fresh[path] = false;
            await this.port.write(fresh);
          }
          if (this.value(path) === true)
            return packageFail("plugin_disable_required");
        } catch {
          return packageFail("plugin_disable_required");
        }
      }
      throw error;
    }
  }
}
