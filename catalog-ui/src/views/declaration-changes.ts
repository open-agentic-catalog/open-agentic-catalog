// Interpret only fields from the two pinned native profiles. Keep other data in the document view.
import { diffArrays } from "diff";
import { object } from "../api/values";
import {
  jsonNestingLimit,
  jsonStructureLimit,
  parseNativeJSONObject,
} from "./json-input";

export const documentLimit = 2 * 1024 * 1024;
type Profile = "Agent" | "MCP";
type Change = {
  group: string;
  field: string;
  before?: string;
  after?: string;
  membership?: boolean;
  occurrences?: number;
};
type Summary = { changes: Change[] } | { unavailable: string };
type AddChange = (
  group: string,
  field: string,
  before: unknown,
  after: unknown,
  kind?: "membership" | "occurrences",
) => void;

class Unavailable extends Error {}
const invalid =
  "A selected declaration is missing, ambiguous or has an unsupported shape.";
const overLimit =
  "The selected cards exceed the declaration comparison limits.";
function record(value: unknown) {
  if (!object(value)) throw new Unavailable(invalid);
  return value;
}
function list(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Unavailable(invalid);
  if (value.length > 128) throw new Unavailable(overLimit);
  return value;
}
function string(value: unknown) {
  if (typeof value !== "string") throw new Unavailable(invalid);
  return value;
}
function requiredString(value: unknown) {
  const text = string(value);
  if (!text.length) throw new Unavailable(invalid);
  return text;
}
function membership(value: unknown) {
  return [...new Set(list(value).map(string))].sort();
}
// Both spellings are accepted by ProtoJSON. An explicitly null field is unset.
function proto(row: Record<string, unknown>, camel: string, original: string) {
  if (Object.hasOwn(row, camel) && Object.hasOwn(row, original))
    throw new Unavailable(invalid);
  return row[camel] ?? row[original];
}
function agentCard(value: Record<string, unknown>) {
  const skills = new Map<string, Record<string, string[] | undefined>>();
  for (const entry of list(value.skills)) {
    const skill = record(entry);
    const id = requiredString(skill.id);
    if (skills.has(id))
      throw new Unavailable(
        "A skill ID occurs more than once in a selected Agent Card.",
      );
    const overrides: Record<string, string[] | undefined> = {};
    for (const [name, alias] of [
      ["inputModes", "input_modes"],
      ["outputModes", "output_modes"],
    ] as const) {
      const modes = membership(proto(skill, name, alias) ?? []);
      if (modes.length) overrides[name] = modes;
    }
    skills.set(id, overrides);
  }
  const interfaces = list(
    proto(value, "supportedInterfaces", "supported_interfaces"),
  ).map((entry) => {
    const i = record(entry);
    const tenant = string(i.tenant ?? "");
    return {
      protocolBinding: requiredString(
        proto(i, "protocolBinding", "protocol_binding"),
      ),
      protocolVersion: requiredString(
        proto(i, "protocolVersion", "protocol_version"),
      ),
      url: requiredString(i.url),
      ...(tenant && { tenant }),
    };
  });
  const streaming = record(value.capabilities).streaming ?? undefined;
  if (streaming !== undefined && typeof streaming !== "boolean")
    throw new Unavailable(invalid);
  const input = membership(
    proto(value, "defaultInputModes", "default_input_modes"),
  );
  const output = membership(
    proto(value, "defaultOutputModes", "default_output_modes"),
  );
  if (!input.length || !output.length || !skills.size || !interfaces.length)
    throw new Unavailable(invalid);
  return { skills, interfaces, streaming, input, output };
}
function compareAgents(
  before: Record<string, unknown>,
  after: Record<string, unknown>,
  add: AddChange,
) {
  const a = agentCard(before),
    b = agentCard(after);
  for (const id of [
    ...new Set([...a.skills.keys(), ...b.skills.keys()]),
  ].sort()) {
    const old = a.skills.get(id),
      next = b.skills.get(id),
      path = "skills[id=" + JSON.stringify(id) + "]";
    if (!old || !next) add("Skills", path, old && id, next && id);
    else
      for (const name of ["inputModes", "outputModes"])
        add("Skills", path + "." + name, old[name], next[name], "membership");
  }
  add("Capabilities", "defaultInputModes", a.input, b.input, "membership");
  add("Capabilities", "defaultOutputModes", a.output, b.output, "membership");
  add("Capabilities", "capabilities.streaming", a.streaming, b.streaming);
  for (let i = 0; i < Math.max(a.interfaces.length, b.interfaces.length); i++) {
    add(
      "Interfaces",
      `supportedInterfaces[${i}]${i === 0 ? " (preferred)" : ""}`,
      a.interfaces[i],
      b.interfaces[i],
    );
  }
}
function remotes(card: Record<string, unknown>) {
  if (card.remotes === undefined) return [];
  return list(card.remotes).map((entry) => {
    const remote = record(entry);
    if (remote.type !== "sse" && remote.type !== "streamable-http")
      throw new Unavailable(invalid);
    const url = requiredString(remote.url);
    const versions =
      remote.supportedProtocolVersions === undefined
        ? undefined
        : membership(remote.supportedProtocolVersions);
    return {
      key: JSON.stringify([remote.type, url]),
      tuple: JSON.stringify({
        type: remote.type,
        url,
        supportedProtocolVersions: versions,
      }),
      versions,
    };
  });
}
function counts(keys: string[]) {
  const result = new Map<string, number>();
  for (const key of keys) result.set(key, (result.get(key) ?? 0) + 1);
  return result;
}
function compareServers(
  before: Record<string, unknown>,
  after: Record<string, unknown>,
  add: AddChange,
) {
  const a = remotes(before),
    b = remotes(after);
  const oldKeys = counts(a.map((r) => r.key)),
    newKeys = counts(b.map((r) => r.key));
  const unique = (key: string) =>
    oldKeys.get(key) === 1 && newKeys.get(key) === 1;
  const newEndpoints = new Map(b.map((r) => [r.key, r]));
  for (const r of a)
    if (unique(r.key))
      add(
        "Remote connections",
        "remotes " + r.key + ".supportedProtocolVersions",
        r.versions,
        newEndpoints.get(r.key)!.versions,
        "membership",
      );
  // Duplicate endpoints have no identity beyond their selected literal tuple.
  const tokens = (rows: typeof a) =>
    rows.map((r) => (unique(r.key) ? r.key : r.tuple));
  const left = tokens(a),
    right = tokens(b);
  const old = counts(left),
    next = counts(right);
  for (const tuple of new Set([...left, ...right])) {
    add(
      "Remote connections",
      "remotes " + tuple + " (occurrences)",
      old.get(tuple),
      next.get(tuple),
      "occurrences",
    );
  }
  // Can additions and removals alone explain the order? Compare only the
  // length of the common subsequence, never pick an identity for a duplicate.
  // The 128-entry limits bound the diff. Compare small integers even for long URLs.
  const shared = new Map<string, number>();
  let commonCount = 0;
  for (const [key, count] of old) {
    if (next.has(key)) {
      shared.set(key, shared.size);
      commonCount += Math.min(count, next.get(key)!);
    }
  }
  const oldOrder = left.filter((key) => shared.has(key)),
    newOrder = right.filter((key) => shared.has(key));
  const ordinals = (keys: string[]) => keys.map((key) => shared.get(key)!);
  const inOrder = diffArrays(ordinals(oldOrder), ordinals(newOrder)).reduce(
    (count, part) => count + (part.added || part.removed ? 0 : part.count),
    0,
  );
  if (inOrder < commonCount) {
    const values = (keys: string[]) =>
      keys.map((key) => JSON.parse(key) as unknown);
    add(
      "Remote connections",
      "remotes (order of retained declarations)",
      values(oldOrder),
      values(newOrder),
    );
  }
  if (
    !a.length &&
    !b.length &&
    (before.remotes === undefined) !== (after.remotes === undefined)
  )
    add("Remote connections", "remotes", before.remotes, after.remotes);
}
export function cardProfile(r: {
  role: string;
  validationState: string;
  mediaType: string;
  profileId: string;
}): Profile | undefined {
  if (r.role !== "primary" || r.validationState !== "valid") return;
  const media = r.mediaType.split(";", 1)[0]!.trim().toLowerCase();
  if (
    r.profileId === "a2a-agent-card-1.0.1-3303592" &&
    media === "application/a2a-agent-card+json"
  )
    return "Agent";
  if (
    r.profileId === "mcp-server-card-v1-experimental-526201b" &&
    media === "application/mcp-server-card+json"
  )
    return "MCP";
}
// Inline JSON has already been decoded by the authenticated revision read.
// Bound its tree without round-tripping private data or comparing numeric extensions.
function decode(value: unknown) {
  if (typeof value === "string")
    return parseNativeJSONObject(value, documentLimit);
  // Count delimiters, colons and commas just as the retained-text parser does.
  let structure = 0;
  function spend(count: number) {
    structure += count;
    if (structure > jsonStructureLimit) throw new Unavailable(overLimit);
  }
  function visit(input: unknown, depth: number) {
    if (input === null || typeof input !== "object") return;
    if (depth > jsonNestingLimit) throw new Unavailable(overLimit);
    if (Array.isArray(input)) {
      spend(2 + Math.max(0, input.length - 1));
      for (const item of input) visit(item, depth + 1);
    } else {
      spend(2);
      let punctuation = 1;
      for (const key in input) {
        if (!Object.hasOwn(input, key)) continue;
        spend(punctuation);
        punctuation = 2;
        visit((input as Record<string, unknown>)[key], depth + 1);
      }
    }
  }
  visit(value, 1);
  return record(value);
}
export function declarationChanges(
  profile: Profile,
  before: unknown,
  after: unknown,
): Summary {
  const changes: Change[] = [];
  let length = 0;
  const add: AddChange = (group, field, old, next, kind) => {
    // For two declared sets display the members that changed. An unset list
    // and a declared empty list still have different presence on an MCP remote.
    const membership =
      kind === "membership" && Array.isArray(old) && Array.isArray(next);
    let before = JSON.stringify(old),
      after = JSON.stringify(next);
    if (membership) {
      const left = new Set<unknown>(old),
        right = new Set<unknown>(next);
      before = JSON.stringify(old.filter((v: unknown) => !right.has(v)));
      after = JSON.stringify(next.filter((v: unknown) => !left.has(v)));
    }
    if (before === after) return;
    length += field.length + (before?.length ?? 0) + (after?.length ?? 0);
    if (changes.length >= 64 || length > 16384)
      throw new Unavailable(overLimit);
    const occurrences =
      kind === "occurrences" ? Number(next ?? 0) - Number(old ?? 0) : undefined;
    changes.push({
      group,
      field,
      before,
      after,
      ...(membership && { membership }),
      ...(occurrences !== undefined && { occurrences }),
    });
  };
  try {
    const compare = profile === "Agent" ? compareAgents : compareServers;
    compare(decode(before), decode(after), add);
    return { changes };
  } catch (error) {
    return {
      unavailable:
        error instanceof Unavailable
          ? error.message
          : "A selected card could not be decoded within the JSON comparison limits.",
    };
  }
}
