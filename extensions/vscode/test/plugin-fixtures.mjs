import { zip, hash } from "./package-fixtures.mjs";
const { pluginPlan } = await import(process.env.CATALOG_VSCODE_LIB);
const binding = hash("owned identity and host");
export function fixture(extra = {}, root = "") {
  const values = {
    "plugin.json": JSON.stringify({
      $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
      name: "owned.tools",
    }),
    "skills/owned-plugin/SKILL.md":
      "---\nname: owned-plugin\ndescription: Owned plugin instructions.\n---\nHarmless instructions.",
    ...extra,
  };
  const entries = Object.entries(values).map(([path, value]) => ({
    path: (root ? root + "/" : "") + path,
    bytes: Buffer.from(value),
    executable: false,
  }));
  const archive = zip(entries);
  const representation = {
    validationState: "valid",
    profileId: "agent-plugins-1.0.0-1fc1b627",
    mediaType: "application/agent-plugins+zip",
    contentDigest: hash(archive),
    size: archive.length,
    url: "https://packages.example/plugin.zip",
    plugin: {
      rootDirectory: root,
      expandedSize: entries.reduce((n, f) => n + f.bytes.length, 0),
      files: entries.map((f) => ({
        path: f.path,
        size: f.bytes.length,
        digest: hash(f.bytes),
        executable: f.executable,
      })),
      documents: entries
        .filter(
          (f) =>
            f.path.endsWith("plugin.json") ||
            f.path.endsWith("SKILL.md") ||
            f.path.endsWith("mcp.json"),
        )
        .map((f) => ({ path: f.path, content: f.bytes.toString() })),
    },
  };
  return {
    entries,
    archive,
    representation,
    plan: pluginPlan(representation, binding),
  };
}
