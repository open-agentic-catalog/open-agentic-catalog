// Small local starters reuse maintained native documents; nothing here retrieves their destinations.
import type { Kind } from "../api/client";
import { parseJSONObject } from "./json-input";
import type { RepresentationDraft } from "./representations";

type Starter = {
  id: string;
  label: string;
  mediaType: string;
  mode: "data" | "content";
  content: string;
  filename: string;
  guidance: string;
};

/** Keep familiar format names beside local inputs, with an exact fallback for extensions. */
export function nativeFormatLabel(mediaType: string): string {
  const labels: Record<string, string> = {
    "application/a2a-agent-card+json": "A2A Agent Card",
    "application/mcp-server-card+json": "MCP Server Card",
    "application/vnd.mcp.server+json": "MCP Registry metadata",
    "application/agent-skills+md": "SKILL.md",
    'text/markdown; profile="urn:air:agent-skills"': "SKILL.md",
    "application/agent-skills+json": "Skill metadata",
    "application/vnd.open-agentic-catalog.agent+json": "Agent metadata",
    "application/vnd.open-agentic-catalog.mcp-registration+json":
      "MCP connection metadata",
    "text/markdown": "Markdown",
  };
  return Object.hasOwn(labels, mediaType) ? labels[mediaType]! : mediaType;
}
const json = (value: object) => JSON.stringify(value, null, 2) + "\n";
// Isolated tests bind these bundled values to the maintained repository samples.
const agentCard =
  '{"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"description":"Resolves delayed and blocked customer orders.","name":"Order exception Agent","skills":[{"description":"Resolves delayed and blocked customer orders","id":"resolve-order-exceptions","name":"Resolve order exceptions","tags":["orders"]}],"supportedInterfaces":[{"protocolBinding":"JSONRPC","protocolVersion":"1.0","url":"https://agents.example.com/order-exceptions"}],"version":"1.0.0"}';
const serverCard =
  '{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","description":"Describes documentation search tools.","name":"com.example/documentation-mcp","remotes":[{"supportedProtocolVersions":["2025-11-25"],"type":"streamable-http","url":"https://mcp.example.com/documentation-mcp"}],"title":"Documentation search MCP Server","version":"1.0.0"}';
const skillDocument = `---
name: review-notes
description: Prepares short notes for a change review.
---

# Review notes

Read the supplied change description.
List the affected components.
List the checks that the change needs.
Record any unanswered questions.
`;
const registrySchema =
  "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json";
const cardSchema =
  "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json";
const examples: Record<Kind, readonly Starter[]> = {
  agent: [
    {
      id: "agent-card",
      label: "A2A Agent Card",
      mediaType: "application/a2a-agent-card+json",
      mode: "data",
      content: agentCard,
      filename: "agent-card.json",
      guidance:
        "Replace the example Agent name, description, ability and HTTPS endpoint with your own values.",
    },
  ],
  mcp_server: [
    {
      id: "mcp-card",
      label: "Hosted MCP Server Card (experimental)",
      mediaType: "application/mcp-server-card+json",
      mode: "data",
      content: serverCard,
      filename: "server-card.json",
      guidance:
        "Replace the example server identity and HTTPS endpoint. Keep only protocol versions your server supports. Discovery snapshots need a primary Server Card; its format remains experimental.",
    },
    {
      id: "mcp-registry-remote",
      label: "Registry server.json for a remote server",
      mediaType: "application/vnd.mcp.server+json",
      mode: "data",
      content: json({
        $schema: registrySchema,
        name: "com.example/documentation-mcp",
        description: "Describes documentation search tools.",
        version: "1.0.0",
        remotes: [
          {
            type: "streamable-http",
            url: "https://mcp.example.com/documentation-mcp",
          },
        ],
      }),
      filename: "server.json",
      guidance:
        "Replace the example identity, description and illustrative HTTPS endpoint. Registry distribution metadata is separate from a Server Card; Registry-only drafts remain supported.",
    },
    {
      id: "mcp-registry-npm",
      label: "Registry server.json for an npm package",
      mediaType: "application/vnd.mcp.server+json",
      mode: "data",
      // Shape and illustrative package values come from the pinned Registry
      // generic-server-json.md Basic Server with NPM Package example.
      content: json({
        $schema: registrySchema,
        name: "io.modelcontextprotocol.anonymous/brave-search",
        description: "MCP server for Brave Search API integration",
        version: "1.0.2",
        packages: [
          {
            registryType: "npm",
            registryBaseUrl: "https://registry.npmjs.org",
            identifier: "@modelcontextprotocol/server-brave-search",
            version: "1.0.2",
            transport: { type: "stdio" },
          },
        ],
      }),
      filename: "server.json",
      guidance:
        "This package and version illustrate the pinned Registry example, not a recommendation or availability check. Replace them with your package metadata. Package version is separate from the document/Catalog version. No package is installed or executed.",
    },
  ],
  skill: [
    {
      id: "skill-markdown",
      label: "Standalone SKILL.md",
      mediaType: "application/agent-skills+md",
      mode: "content",
      content: skillDocument,
      filename: "SKILL.md",
      guidance:
        "Replace the Skill name, description and instructions. A standalone document does not include supporting files or establish its parent directory name. Complete packages retain their separate upload and immutable handoff.",
    },
  ],
  plugin: [],
};

export function nativeStarters(kind: Kind) {
  return examples[kind];
}

/** A starter replaces only its own row, preserving its role, profile and stable identity. */
export function applyNativeStarter(
  value: RepresentationDraft,
  starter: Starter,
): RepresentationDraft {
  const next = {
    ...value,
    mediaType: starter.mediaType,
    mode: starter.mode,
    content: starter.content,
    url: "",
    packageData: "",
    handoff: "",
  };
  delete next.expectedDigest;
  delete next.documentStaged;
  return next;
}

/** Guidance and optional version suggestions never replace the backend's native validation. */
export function nativeDocument(
  kind: Kind,
  value: Pick<RepresentationDraft, "mode" | "mediaType" | "content">,
) {
  if ((value.mode !== "data" && value.mode !== "content") || !value.content)
    return undefined;
  const starter = examples[kind].find(
    (item) => item.mediaType === value.mediaType,
  );
  const markdown =
    kind === "skill" &&
    (value.mediaType === "application/agent-skills+md" ||
      value.mediaType === 'text/markdown; profile="urn:air:agent-skills"');
  const document: {
    text: string;
    filename: string;
    guidance?: string;
    version?: string;
  } = {
    text: value.content,
    filename:
      starter?.filename ??
      (markdown
        ? "SKILL.md"
        : value.mode === "data"
          ? "document.json"
          : "document.txt"),
  };
  if (value.mode === "data" || /(?:\+|\/)json$/.test(value.mediaType)) {
    try {
      const data = parseJSONObject(value.content);
      if (
        (value.mediaType === "application/mcp-server-card+json" &&
          data.$schema === registrySchema) ||
        (value.mediaType === "application/vnd.mcp.server+json" &&
          data.$schema === cardSchema)
      ) {
        document.guidance =
          "The declared document schema does not match the selected media type. Select the matching native format or correct the document; changing a label does not convert it.";
      } else if (
        kind === "mcp_server" &&
        starter &&
        data.name === "" &&
        ((value.mediaType === "application/mcp-server-card+json" &&
          data.$schema === cardSchema) ||
          (value.mediaType === "application/vnd.mcp.server+json" &&
            data.$schema === registrySchema))
      ) {
        // Required fields can be incomplete while the native editor builds a draft.
        return document;
      } else if (
        (kind === "agent" &&
          value.mediaType === "application/a2a-agent-card+json" &&
          Array.isArray(data.supportedInterfaces)) ||
        (kind === "mcp_server" && starter && data.name)
      ) {
        if (
          typeof data.name === "string" &&
          typeof data.version === "string" &&
          data.version.trim() &&
          [...data.version.trim()].length <= 128
        )
          document.version = data.version.trim();
      } else if (starter) {
        document.guidance =
          "This input is not recognized as the selected native format. Keep the raw input while correcting it, or explicitly choose the matching example. ARD wrappers and MCP protocol responses are separate formats.";
      }
    } catch (cause) {
      document.guidance =
        cause instanceof Error ? cause.message : "Check the JSON document.";
    }
  }
  return document;
}
