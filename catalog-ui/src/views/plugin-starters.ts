// Plugin-only examples load with the file-preparation screen.
import {
  jsonText as json,
  pluginSchema,
  pluginMCPSchema,
} from "./plugin-files";
const pluginManifest = json({
  $schema: pluginSchema,
  name: "release-workflow",
  version: "1.0.0",
  description: "Synthetic development metadata. Do not install or execute.",
  license: "Apache-2.0",
  extensions: {
    "org.example.catalog-demo": { notice: "Inert release-review metadata" },
  },
});
const pluginMCP = json({
  $schema: pluginMCPSchema,
  mcpServers: {
    documentation: {
      type: "streamable-http",
      url: "https://mcp.example.com/documentation",
    },
  },
});
const pluginSkill = `---
name: prepare-release
description: Review synthetic development evidence and summarize recommendations.
license: Apache-2.0
---

# Instructions

Read the supplied evidence and explain the next review step. This demo does not authorize execution or installation.
`;
export const pluginStarterFiles = [
  { path: "plugin.json", content: pluginManifest },
  { path: "mcp.json", content: pluginMCP },
  { path: "skills/prepare-release/SKILL.md", content: pluginSkill },
] as const;
