import { ClientError } from "./config.js";

// These identifiers and README anchors are local product navigation, never
// resource destinations or values taken from a Catalog response.
export const helpTopics = [
  {
    id: "start",
    label: "First use and commands",
    anchor: "first-use-and-command-index",
  },
  {
    id: "setup",
    label: "Add or edit a connection",
    anchor: "add-a-connection",
  },
  {
    id: "connection",
    label: "Sign-in, OIDC and login recovery",
    anchor: "sign-in-and-sign-out",
  },
  {
    id: "trust",
    label: "Separate trust and transport settings",
    anchor: "trust-proxies-and-limits",
  },
  {
    id: "catalog",
    label: "Search, filters and result pages",
    anchor: "browse-the-catalog",
  },
  {
    id: "tools",
    label: "My tools, package updates and recovery",
    anchor: "manage-my-tools",
  },
  {
    id: "agent",
    label: "Agent outcomes and safe recovery",
    anchor: "agent-outcomes",
  },
] as const;

export function helpAnchor(input: unknown): string {
  const topic = helpTopics.find((item) => item.id === input);
  if (!topic) throw new ClientError("invalid_help_topic");
  return topic.anchor;
}
