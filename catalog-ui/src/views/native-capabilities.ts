// Exact private card declarations; editing never inspects resource endpoints.
import type { components } from "../api/schema";
import { object } from "../api/values";
import { characterText, trimGoWhitespace } from "./input-validation";

export type NativeCapabilities =
  components["schemas"]["NativeCapabilitiesFilter"];
export const nativeArrayFields = [
  ["agentSkillIds", "Agent skill IDs", "Skill ID"],
  ["agentSkillTags", "Agent skill tags", "Skill tag"],
  ["agentDefaultInputModes", "Agent default input modes", "Input mode"],
  ["agentDefaultOutputModes", "Agent default output modes", "Output mode"],
  ["mcpTransports", "MCP transports", "Transport"],
  ["mcpProtocolVersions", "MCP protocol versions", "Protocol version"],
] as const;

/** Drafts may have unfinished values but never unknown fields or unbounded rows. */
export function nativeDraft(value: unknown): NativeCapabilities {
  if (
    !object(value) ||
    Object.keys(value).some(
      (key) =>
        key !== "agentStreaming" &&
        !nativeArrayFields.some(([name]) => name === key),
    ) ||
    (value.agentStreaming !== undefined &&
      typeof value.agentStreaming !== "boolean")
  )
    throw new Error(
      "Use supported native capability fields; streaming must be a boolean.",
    );
  for (const [field] of nativeArrayFields) {
    const values = value[field];
    if (
      values !== undefined &&
      (!Array.isArray(values) ||
        values.length > 16 ||
        values.some((v) => typeof v !== "string"))
    )
      throw new Error("Use up to 16 text values per native capability field.");
  }
  return value as NativeCapabilities;
}

export function nativeKind(value: NativeCapabilities): "agent" | "mcp_server" {
  return Object.keys(value).some((key) => key.startsWith("agent"))
    ? "agent"
    : "mcp_server";
}

/** Account for Go's JSON HTML and line-separator escaping in the 8 KiB budget. */
export function checkedNative(value: unknown): NativeCapabilities {
  const native = nativeDraft(value);
  if (
    !Object.keys(native).length ||
    (Object.keys(native).some((key) => key.startsWith("agent")) &&
      Object.keys(native).some((key) => key.startsWith("mcp")))
  )
    throw new Error(
      "Select declarations from one card kind, Agent or MCP server.",
    );
  let count = 0;
  for (const [field, label] of nativeArrayFields) {
    const values = native[field];
    if (values === undefined) continue;
    if (!values.length || new Set(values).size !== values.length)
      throw new Error(`${label} require one to 16 distinct exact values.`);
    for (const value of values) {
      characterText(value, label, 256);
      if (
        /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(
          value,
        )
      )
        throw new Error(`${label} cannot contain malformed Unicode.`);
      if (!trimGoWhitespace(value))
        throw new Error(`${label} cannot be blank.`);
    }
    count += values.length;
  }
  const encoded = JSON.stringify(native).replace(
    /[<>&\u2028\u2029]/gu,
    (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );
  if (count > 64 || new TextEncoder().encode(encoded).length > 8192)
    throw new Error(
      "Native capability filters exceed 64 values or 8192 encoded bytes.",
    );
  return native;
}
