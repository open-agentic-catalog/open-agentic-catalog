// Presentation names for the four validated resource kinds; wire values stay unchanged.
import type { Kind } from "./api/client";

const kinds = {
  agent: { label: "Agents", singular: "Agent", hint: "Agent capabilities" },
  mcp_server: {
    label: "MCP Servers",
    singular: "MCP Server",
    hint: "Tools and connections",
  },
  skill: { label: "Skills", singular: "Skill", hint: "Reusable instructions" },
  plugin: {
    label: "Plugins",
    singular: "Plugin",
    hint: "Skills and MCP packages",
  },
} satisfies Record<Kind, { label: string; singular: string; hint: string }>;

export const kindChoices = (Object.keys(kinds) as Kind[]).map((value) => ({
  value,
  ...kinds[value],
}));

/** Name a kind after the API response boundary has validated its wire value. */
export function kindLabel(kind: Kind): string {
  return kinds[kind].singular;
}
