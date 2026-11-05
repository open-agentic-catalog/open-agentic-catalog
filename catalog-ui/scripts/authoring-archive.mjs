// Independent expected files qualify the browser downloads against frozen HTTPS bytes.
import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

export const authoredPluginDocuments = [
  {
    path: "plugin.json",
    content:
      JSON.stringify(
        {
          $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
          name: "review-tools",
          version: "1.0.0",
          description: "Prepare a release review.",
          license: "Apache-2.0",
        },
        null,
        2,
      ) + "\n",
  },
  {
    path: "mcp.json",
    content:
      JSON.stringify(
        {
          $schema: "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
          mcpServers: {
            documentation: {
              type: "streamable-http",
              url: "https://mcp.example.test/documentation",
            },
          },
        },
        null,
        2,
      ) + "\n",
  },
  {
    path: "skills/review-notes/SKILL.md",
    content:
      '---\nname: "review-notes"\ndescription: "Prepare release review notes."\n---\n# Review\n\nRead the release notes.',
  },
];

/** Use the standard ZIP library; the caller owns the child process and directory. */
export async function packAuthoredPlugin(directory, documents, run) {
  const paths = authoredPluginDocuments.map((item) => item.path).sort();
  if (
    !Array.isArray(documents) ||
    documents.length !== paths.length ||
    documents.some(
      (item) =>
        !item ||
        typeof item.content !== "string" ||
        Buffer.byteLength(item.content) > 16384,
    ) ||
    JSON.stringify(documents.map((item) => item.path).sort()) !==
      JSON.stringify(paths)
  )
    throw new Error("Invalid owned Plugin download inventory");
  const input = join(directory, `plugin-${randomUUID()}.json`);
  const output = input + ".zip";
  await writeFile(input, JSON.stringify(documents), { mode: 0o600 });
  await run("python3", [
    "-I",
    "-c",
    `import json, os, sys, zipfile
with open(sys.argv[1], encoding="utf-8") as source:
    documents = json.load(source)
with zipfile.ZipFile(sys.argv[2], "x", compression=zipfile.ZIP_STORED) as archive:
    for item in sorted(documents, key=lambda item: item["path"]):
        info = zipfile.ZipInfo(item["path"], (2020, 1, 1, 0, 0, 0))
        info.external_attr = 0o100644 << 16
        archive.writestr(info, item["content"].encode("utf-8"))
os.chmod(sys.argv[2], 0o600)
`,
    input,
    output,
  ]);
  const bytes = await readFile(output);
  if (bytes.length > 65536)
    throw new Error("Owned Plugin archive exceeds its fixture bound");
  return bytes;
}
