import { pluginStarterFiles } from "./plugin-starters";
import { expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { newRepresentation } from "./representations";
import {
  applyNativeStarter,
  nativeDocument,
  nativeFormatLabel,
  nativeStarters,
} from "./native-starters";

const source = (path: string) =>
  readFileSync(new URL(`../../../${path}`, import.meta.url), "utf8");
const agentCard = source("samples/cards/order-agent.json");
const serverCard = source("samples/cards/documentation-mcp.json");
const skill = source("examples/native-import/skills/review-notes/SKILL.md");

it("bundles maintained native starters and a real Registry npm package shape", () => {
  expect(nativeStarters("agent")[0]!.content).toBe(agentCard);
  expect(nativeStarters("mcp_server")[0]!.content).toBe(serverCard);
  expect(nativeStarters("skill")[0]!.content).toBe(skill);
  const registry = nativeStarters("mcp_server").find(
    (item) => item.id === "mcp-registry-npm",
  )!;
  expect(JSON.parse(registry.content)).toMatchObject({
    name: "io.modelcontextprotocol.anonymous/brave-search",
    version: "1.0.2",
    packages: [
      {
        registryType: "npm",
        identifier: "@modelcontextprotocol/server-brave-search",
        version: "1.0.2",
        transport: { type: "stdio" },
      },
    ],
  });
  expect(JSON.parse(registry.content)).not.toHaveProperty("remotes");
  expect(nativeStarters("plugin")).toEqual([]);
  expect(pluginStarterFiles.map((file) => file.path)).toEqual([
    "plugin.json",
    "mcp.json",
    "skills/prepare-release/SKILL.md",
  ]);
  expect(JSON.parse(pluginStarterFiles[0].content)).toMatchObject({
    name: "release-workflow",
    $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  });
  for (const file of pluginStarterFiles) {
    const maintained = source(`samples/packages/release-workflow/${file.path}`);
    if (file.path.endsWith(".json"))
      expect(JSON.parse(file.content)).toEqual(JSON.parse(maintained));
    else expect(file.content).toBe(maintained);
  }
});

it("replaces one source without changing row identity, role or profile", () => {
  const previous = {
    ...newRepresentation("skill"),
    role: "documentation" as const,
    profileId: "kept-profile",
    url: "https://example.test/document",
    packageData: "staged-package",
    handoff: "staged-handoff",
    expectedDigest: "sha256:old",
    documentStaged: true,
  };
  const next = applyNativeStarter(previous, nativeStarters("skill")[0]!);
  expect(next).toEqual({
    editorID: previous.editorID,
    role: "documentation",
    profileId: "kept-profile",
    mode: "content",
    mediaType: "application/agent-skills+md",
    content: skill,
    url: "",
    packageData: "",
    handoff: "",
  });
  expect(previous.documentStaged).toBe(true);
  expect(previous.expectedDigest).toBe("sha256:old");
});

it.each(["agent", "mcp_server", "skill"] as const)(
  "reads the %s starter without rewriting its document",
  (kind) => {
    for (const starter of nativeStarters(kind)) {
      const value = applyNativeStarter(newRepresentation(kind), starter);
      expect(nativeDocument(kind, value)).toEqual({
        text: starter.content,
        filename: starter.filename,
        ...(kind === "skill"
          ? {}
          : { version: starter.id === "mcp-registry-npm" ? "1.0.2" : "1.0.0" }),
      });
    }
  },
);

it("keeps external inputs local and unsupported native input unchanged", () => {
  const value = newRepresentation("agent");
  expect(nativeDocument("agent", value)).toBeUndefined();
  expect(
    nativeDocument("agent", { ...value, mode: "url", content: "old" }),
  ).toBeUndefined();
  expect(
    nativeDocument("agent", { ...value, mode: "package", content: "old" }),
  ).toBeUndefined();
  const content = '{"specVersion":"1.0","entries":[]}';
  const unsupported = nativeDocument("agent", { ...value, content });
  expect(unsupported?.text).toBe(content);
  expect(unsupported?.guidance).toContain("ARD wrappers");
  expect(
    nativeDocument("agent", {
      ...value,
      mediaType: "application/vnd.open-agentic-catalog.agent+json",
      content: '{"name":"Private","framework":"custom"}',
    }),
  ).toEqual({
    text: '{"name":"Private","framework":"custom"}',
    filename: "document.json",
  });
  expect(
    nativeDocument("skill", {
      ...newRepresentation("skill"),
      mediaType: 'text/markdown; profile="urn:air:agent-skills"',
      content: skill,
    })?.filename,
  ).toBe("SKILL.md");
  expect(
    nativeDocument("skill", {
      ...newRepresentation("skill"),
      mediaType: "text/plain",
      content: "untouched",
    }),
  ).toEqual({ text: "untouched", filename: "document.txt" });
});

it.each([
  [
    "application/mcp-server-card+json",
    "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
  ],
  [
    "application/vnd.mcp.server+json",
    "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
  ],
])(
  "explains %s schema mismatches without relabelling the input",
  (mediaType, schema) => {
    const content = JSON.stringify({
      $schema: schema,
      name: "com.example/server",
      version: "1.0.0",
    });
    const document = nativeDocument("mcp_server", {
      mode: "data",
      mediaType,
      content,
    });
    expect(document?.text).toBe(content);
    expect(document?.filename).toBe(
      mediaType === "application/mcp-server-card+json"
        ? "server-card.json"
        : "server.json",
    );
    expect(document?.guidance).toContain(
      "does not match the selected media type",
    );
    expect(document).not.toHaveProperty("version");
  },
);

it.each([
  '{"name":"Agent","name":"Other"}',
  '{"n":0.1234567890123456789}',
  '{"n":1e-999}',
  "[]",
])(
  "retains refused JSON as text without suggesting its version: %s",
  (content) => {
    const document = nativeDocument("agent", {
      ...newRepresentation("agent"),
      content,
    });
    expect(document?.text).toBe(content);
    expect(document?.guidance).toBeTruthy();
    expect(document).not.toHaveProperty("version");
  },
);

it("does not truncate an overlong native version to fit the Catalog", () => {
  const content = JSON.stringify({
    name: "Agent",
    supportedInterfaces: [],
    version: "x".repeat(129),
  });
  expect(
    nativeDocument("agent", { ...newRepresentation("agent"), content }),
  ).not.toHaveProperty("version");
});

it.each([
  [
    "application/mcp-server-card+json",
    "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
    "server-card.json",
  ],
  [
    "application/vnd.mcp.server+json",
    "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
    "server.json",
  ],
])(
  "recognizes an unfinished %s draft without suggesting its version",
  (mediaType, schema, filename) => {
    const content = JSON.stringify({
      $schema: schema,
      name: "",
      description: "",
      version: "2.0.0",
    });
    expect(
      nativeDocument("mcp_server", { mode: "data", mediaType, content }),
    ).toEqual({ text: content, filename });
  },
);

it("uses readable format names and preserves unknown media types", () => {
  for (const [type, label] of [
    ["application/a2a-agent-card+json", "A2A Agent Card"],
    ["application/mcp-server-card+json", "MCP Server Card"],
    ["application/vnd.mcp.server+json", "MCP Registry metadata"],
    ["application/agent-skills+md", "SKILL.md"],
    ['text/markdown; profile="urn:air:agent-skills"', "SKILL.md"],
    ["application/agent-skills+json", "Skill metadata"],
    ["application/vnd.open-agentic-catalog.agent+json", "Agent metadata"],
    [
      "application/vnd.open-agentic-catalog.mcp-registration+json",
      "MCP connection metadata",
    ],
    ["text/markdown", "Markdown"],
    ["application/custom+json", "application/custom+json"],
    ["constructor", "constructor"],
    ["__proto__", "__proto__"],
    ["toString", "toString"],
  ])
    expect(nativeFormatLabel(type!)).toBe(label);
});
