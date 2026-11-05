// Shared source inputs validate editable scope/topology shape before server-authorized changes.
import type { components } from "../api/schema";
import { characterText } from "./input-validation";
import { parseObject } from "./representations";

type Schema = components["schemas"];
export type Scope = {
  identifiers?: string[];
  excludedIdentifiers?: string[];
  versions?: string[];
  kinds?: Schema["ResourceKind"][];
  publishers?: string[];
  tags?: string[];
};
export type Settings = Pick<
  Schema["ConnectSource"],
  | "artworkEnabled"
  | "trustVerificationPolicy"
  | "displayName"
  | "retrievalMode"
  | "updateMode"
  | "live"
  | "referrals"
  | "nesting"
  | "discoveryAssignments"
> & { scope: Scope };

/** Policy names refer to deployment configuration, never browser-supplied keys. */
export function trustPolicyName(value = ""): string {
  if (
    value !== value.trim() ||
    !/^(?:|[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)$/.test(value)
  )
    throw new Error(
      "ANS policy must be empty or a name of up to 63 lowercase letters, digits and hyphens.",
    );
  return value;
}
/** Validate the editable scope shape; the API remains authoritative for policy. */
export function sourceScope(value: unknown): Scope {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Source scope must be an object.");
  const fields = [
    "identifiers",
    "versions",
    "kinds",
    "publishers",
    "tags",
    "excludedIdentifiers",
  ];
  for (const [key, items] of Object.entries(value)) {
    if (
      !fields.includes(key) ||
      !Array.isArray(items) ||
      (!items.length && key !== "excludedIdentifiers") ||
      !items.every(
        (item): item is string => typeof item === "string" && !!item.trim(),
      )
    )
      throw new Error("Source scope requires nonempty supported filters.");
    const limits: Record<string, [number, number]> = {
      identifiers: [1000, 512],
      excludedIdentifiers: [1000, 512],
      versions: [100, 128],
      publishers: [100, 255],
      tags: [100, 80],
      kinds: [4, 10],
    };
    const [count, length] = limits[key]!;
    if (items.length > count || new Set(items).size !== items.length)
      throw new Error(`Scope ${key} needs up to ${count} distinct values.`);
    for (const item of items) {
      characterText(item, `Scope ${key}`, length, 1);
      if (item !== item.trim())
        throw new Error(
          "Scope values must not have leading or trailing whitespace.",
        );
    }
    if (
      key === "kinds" &&
      items.some(
        (item) => !["agent", "mcp_server", "skill", "plugin"].includes(item),
      )
    )
      throw new Error(
        "Scope kinds must be agent, mcp_server, skill or plugin.",
      );
  }
  if (!Object.keys(value).some((key) => key !== "excludedIdentifiers"))
    throw new Error("Select at least one source content scope filter.");
  return value as Scope;
}

// Advanced topology settings remain explicit JSON objects with closed server
// validation. They are never inferred from URLs returned by an upstream.
export function topologyInput(
  text: string,
): Pick<Settings, "referrals" | "nesting" | "discoveryAssignments"> {
  const value = topologyShape(parseObject(text));
  const seen = new Set<string>();
  for (const assignment of value.discoveryAssignments ?? []) {
    const host = assignment.host.trim().toLowerCase();
    if (!host || host.length > 253 || /[\s/#?@]/u.test(host))
      throw new Error(
        "Use an exact configured discovery host, without a URL path or credentials.",
      );
    const key = host + "\0" + assignment.audience;
    if (seen.has(key))
      throw new Error(
        "Discovery host and audience assignments must be unique.",
      );
    seen.add(key);
  }
  return value;
}

/** Preserve exact values while removing empty and repeated comma/newline entries. */
export const sourceValues = (value: string) => [
  ...new Set(
    value
      .split(/[\n,]/)
      .map((v) => v.trim())
      .filter(Boolean),
  ),
];

/** Shape validation allows incomplete guided text; command preparation checks completed rows. */
export function topologyShape(
  value: Record<string, unknown>,
): Pick<Settings, "referrals" | "nesting" | "discoveryAssignments"> {
  if (
    Object.keys(value).some(
      (key) => !["referrals", "nesting", "discoveryAssignments"].includes(key),
    )
  )
    throw new Error(
      "Topology accepts only referrals, nesting and discoveryAssignments.",
    );
  for (const key of ["referrals", "nesting"] as const) {
    const item = value[key];
    if (
      item !== undefined &&
      (!item || typeof item !== "object" || Array.isArray(item))
    )
      throw new Error("Referral and nesting policies must be objects.");
  }
  if (
    value.discoveryAssignments !== undefined &&
    (!Array.isArray(value.discoveryAssignments) ||
      value.discoveryAssignments.length > 16 ||
      value.discoveryAssignments.some(
        (item: unknown) =>
          !item ||
          typeof item !== "object" ||
          Array.isArray(item) ||
          Object.keys(item).some(
            (key) => !["host", "audience"].includes(key),
          ) ||
          !("host" in item) ||
          typeof item.host !== "string" ||
          !("audience" in item) ||
          (item.audience !== "anonymous" && item.audience !== "authenticated"),
      ))
  )
    throw new Error(
      "Use up to sixteen discovery assignments, each with a host and anonymous or authenticated audience.",
    );
  return value as Pick<
    Settings,
    "referrals" | "nesting" | "discoveryAssignments"
  >;
}
