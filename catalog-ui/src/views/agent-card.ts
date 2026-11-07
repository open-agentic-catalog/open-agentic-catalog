// Guided edits patch the current Card; the backend owns pinned ProtoJSON validation.
import { object } from "../api/values";
import { listingLink, trimGoWhitespace } from "./input-validation";
import { parseJSONObject } from "./json-input";

export type AgentInterface = Record<string, unknown> & {
  url?: string;
  protocolBinding?: string;
  protocolVersion?: string;
};
export type AgentAbility = Record<string, unknown> & {
  id?: string;
  name?: string;
  description?: string;
  tags?: string[];
};
export type AgentCard = Record<string, unknown> & {
  name?: string;
  description?: string;
  version?: string;
  supportedInterfaces?: AgentInterface[];
  capabilities?: Record<string, unknown>;
  defaultInputModes?: string[];
  defaultOutputModes?: string[];
  skills?: AgentAbility[];
};

/** Use the backend's whitespace rules without changing opaque version labels. */
export function agentVersion(value: string): string {
  return trimGoWhitespace(value);
}

/** Defaults belong only to a new Card and advertise no optional capabilities. */
export function newAgentCard(version: string): AgentCard {
  return {
    name: "",
    description: "",
    version: agentVersion(version),
    capabilities: {},
    supportedInterfaces: [
      { url: "", protocolBinding: "JSONRPC", protocolVersion: "1.0" },
    ],
    defaultInputModes: ["text/plain"],
    defaultOutputModes: ["text/plain"],
    skills: [{ id: "", name: "", description: "", tags: [""] }],
  };
}

const strings = (value: unknown) =>
  Array.isArray(value) && value.every((item) => typeof item === "string");
const fields = (value: Record<string, unknown>, names: string[]) =>
  names.every(
    (name) => value[name] === undefined || typeof value[name] === "string",
  );
const noAliases = (value: Record<string, unknown>, names: string[]) =>
  names.every((name) => !Object.hasOwn(value, name));

/** Refuse ambiguous shapes and ProtoJSON aliases instead of reconstructing an import. */
export function readAgentCard(
  content: string,
  version: string,
): { card?: AgentCard; original?: Record<string, unknown>; reason?: string } {
  if (!content) return { card: newAgentCard(version) };
  let value: Record<string, unknown>;
  try {
    value = parseJSONObject(content);
  } catch (error) {
    return { reason: (error as Error).message };
  }
  const capabilities = value.capabilities;
  if (
    !object(capabilities) ||
    !Array.isArray(value.supportedInterfaces) ||
    !fields(value, ["name", "description", "version"]) ||
    !noAliases(value, [
      "supported_interfaces",
      "default_input_modes",
      "default_output_modes",
      "security_schemes",
      "security_requirements",
      "documentation_url",
      "icon_url",
    ]) ||
    !["streaming", "pushNotifications", "extendedAgentCard"].every(
      (name) =>
        capabilities[name] === undefined ||
        typeof capabilities[name] === "boolean",
    ) ||
    !noAliases(capabilities, ["push_notifications", "extended_agent_card"]) ||
    !value.supportedInterfaces.every(
      (item) =>
        object(item) &&
        fields(item, ["url", "protocolBinding", "protocolVersion"]) &&
        noAliases(item, ["protocol_binding", "protocol_version"]),
    ) ||
    !["defaultInputModes", "defaultOutputModes"].every(
      (name) => value[name] === undefined || strings(value[name]),
    ) ||
    (value.skills !== undefined &&
      (!Array.isArray(value.skills) ||
        !value.skills.every(
          (item) =>
            object(item) &&
            fields(item, ["id", "name", "description"]) &&
            (item.tags === undefined || strings(item.tags)) &&
            noAliases(item, [
              "input_modes",
              "output_modes",
              "security_requirements",
            ]),
        )))
  )
    return {
      original: value,
      reason:
        "This document cannot be safely edited through these fields. Use Advanced Agent Card JSON or import a different Card.",
    };
  return { card: value as AgentCard };
}

/** Even an unfamiliar signature shape must not survive generated-field edits. */
export function signedAgentCard(card: Record<string, unknown>): boolean {
  return (
    Object.hasOwn(card, "signatures") &&
    !(Array.isArray(card.signatures) && card.signatures.length === 0)
  );
}

/** A field change preserves every untouched member and never carries signatures forward. */
export function patchAgentCard(
  card: AgentCard,
  patch: Partial<AgentCard>,
): string {
  if (signedAgentCard(card))
    throw new Error(
      "Create an unsigned copy or import an externally re-signed Card before changing these fields.",
    );
  return JSON.stringify({ ...card, ...patch }, null, 2) + "\n";
}

/** Local field checks explain common mistakes; they do not replace backend validation. */
export function agentFieldError(
  value: string,
  type: "text" | "url" | "protocol" | "media" = "text",
): string {
  if (!agentVersion(value)) return "Enter a value.";
  if (value.includes("\0")) return "Remove the NUL character.";
  if (type === "protocol" && !/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(value))
    return "Use a major.minor A2A protocol version, such as 1.0.";
  if (
    type === "media" &&
    !/^[!#$%&'*+\-.^_`|~\da-z]+\/[!#$%&'*+\-.^_`|~\da-z]+(?:\s*;.*)?$/iu.test(
      value,
    )
  )
    return "Use a media type such as image/png.";
  if (type === "url") {
    try {
      listingLink(value);
      const url = new URL(value);
      const authority = value.match(/^https:\/\/([^/?#]+)/iu)?.[1];
      if (
        !authority ||
        /[^\u0021-\u007e]/u.test(authority) ||
        url.hash ||
        url.port === "0" ||
        value.includes("\\")
      )
        throw new Error();
    } catch {
      return "Use an absolute HTTPS URL without credentials, a fragment or secret query parameters.";
    }
  }
  return "";
}
