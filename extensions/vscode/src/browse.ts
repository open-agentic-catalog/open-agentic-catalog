import { createHash } from "node:crypto";
import { ClientError, binding, record, abortable } from "./config.js";
import { decodeJSON, encodeProjection, uniqueJSON } from "./json.js";
import type { Session } from "./session.js";

export const kinds = ["agent", "mcp_server", "skill", "plugin"] as const;
export type Kind = (typeof kinds)[number];
export type Origin = "local" | "indexed" | "live" | "published";
export type PeerReplayConsent = (
  request: { query: Query; continuation: boolean },
  signal?: AbortSignal,
) => Promise<boolean>;
export function consumerFamily(
  ref: DocumentRef,
  resourceKind: string,
): "a2a" | "mcp" | undefined {
  const type = ref.mediaType?.split(";")[0]!.trim().toLowerCase();
  if (resourceKind === "agent" && type === "application/a2a-agent-card+json")
    return "a2a";
  if (
    resourceKind === "mcp_server" &&
    [
      "application/mcp-server-card+json",
      "application/vnd.mcp.server+json",
    ].includes(type ?? "")
  )
    return "mcp";
  return undefined;
}
export interface Query {
  text: string;
  origins?: ("local" | "indexed" | "live")[];
  kinds?: string[];
  tags?: string[];
  categories?: string[];
  kind: string;
  tag: string;
  namespace: string;
  origin: "local" | "indexed" | "live";
  publisher: string;
  license: string;
  category: string;
  visibility: string;
  federation: "none" | "auto";
  limit: number;
}
export const emptyQuery: Query = {
  text: "",
  kind: "",
  tag: "",
  namespace: "",
  origin: "local",
  publisher: "",
  license: "",
  category: "",
  visibility: "",
  federation: "none",
  limit: 20,
};
export interface Reference {
  label: string;
  value: string;
  url?: string;
}
export interface Item {
  key: string;
  identifier: string;
  versionPresent: boolean;
  version?: string;
  kind: Kind | "unknown";
  name: string;
  description: string;
  tags: string[];
  origin: Origin;
  publisher?: string;
  namespace?: string;
  namespaceId?: string;
  license?: string;
  visibility?: string;
  releaseId?: string;
  listingRevisionId?: string;
  references: Reference[];
  ratings?: string;
  artwork?: Record<string, unknown>;
  artworkTarget?: Record<string, unknown>;
  raw?: Record<string, unknown>;
  sourceCount?: number;
  conflict?: boolean;
  sameSourceConflict?: boolean;
}
export interface Detail {
  item: Item;
  references: Reference[];
  documents: DocumentRef[];
  facts: Reference[];
  packages?: unknown[];
}
export interface DocumentRef {
  bytes?: Uint8Array;
  label: string;
  path?: string;
  url?: string;
  value?: unknown;
  content?: string;
  digest?: string;
  size?: number;
  mediaType?: string;
}
export interface Page {
  items: Item[];
  next?: string;
  summary: string;
  partial: boolean;
}
export interface Facet {
  field: string;
  buckets: {
    value: string;
    count: number;
    filter?: { field: string; value: string };
  }[];
  other: number;
}
export interface Store {
  get<T>(key: string): T | undefined;
  update(key: string, value: unknown): PromiseLike<void>;
}
export type Bookmark =
  | { releaseId: string }
  | { identifier: string; versionPresent: boolean; version?: string };
export function literal(value: unknown, max = 512, empty = false): string {
  if (
    typeof value !== "string" ||
    (!empty && !value) ||
    [...value].length > max ||
    [...value].some((c) => {
      const n = c.codePointAt(0)!;
      return n < 32 || n === 127 || (n >= 0xd800 && n <= 0xdfff);
    })
  )
    throw new ClientError("invalid_response");
  return value;
}
/** Catalog metadata permits controls other than NUL. Preserve exact evidence. */
export function metadataText(value: unknown, max = 512, empty = false): string {
  if (typeof value !== "string" || (!empty && !value))
    throw new ClientError("invalid_response");
  let count = 0;
  for (const c of value) {
    const n = c.codePointAt(0)!;
    if (++count > max || n === 0 || (n >= 0xd800 && n <= 0xdfff))
      throw new ClientError("invalid_response");
  }
  return value;
}
export function paragraph(value: unknown, max = 4000): string {
  return metadataText(value, max, true);
}
export function uuid(value: unknown): string {
  const s = literal(value, 36);
  if (
    !/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/iu.test(s)
  )
    throw new ClientError("invalid_response");
  return s;
}
function number(value: unknown, max = Number.MAX_SAFE_INTEGER): number {
  if (
    !Number.isSafeInteger(value) ||
    (value as number) < 0 ||
    (value as number) > max
  )
    throw new ClientError("invalid_response");
  return value as number;
}
function boolean(value: unknown): boolean {
  if (typeof value !== "boolean") throw new ClientError("invalid_response");
  return value;
}
function list(value: unknown, max: number): unknown[] {
  if (!Array.isArray(value) || value.length > max)
    throw new ClientError("invalid_response");
  return value;
}
function tags(value: unknown): string[] {
  return value === undefined
    ? []
    : list(value, 100).map((v) => metadataText(v, 80, true));
}
function kind(value: unknown): Kind | "unknown" {
  return kinds.includes(value as Kind) ? (value as Kind) : "unknown";
}
export function link(value: unknown): string | undefined {
  if (
    typeof value !== "string" ||
    value.length > 2048 ||
    /[\s\\]/u.test(value) ||
    [...value].some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)
  )
    return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !url.username && !url.password
      ? url.href
      : undefined;
  } catch {
    return undefined;
  }
}
function reference(label: string, value: unknown): Reference[] {
  if (typeof value !== "string" || !value) return [];
  const safe = literal(value, 2048);
  return [{ label, value: safe, ...(link(safe) ? { url: link(safe)! } : {}) }];
}
function ratings(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  const v = record(value),
    count = number(v.count);
  if (!count) return "No ratings";
  if (
    typeof v.average !== "number" ||
    !Number.isFinite(v.average) ||
    v.average < 1 ||
    v.average > 5
  )
    throw new ClientError("invalid_response");
  return `${v.average.toFixed(1)} / 5 (${count} ratings)`;
}
function presentation(item: Item, value: unknown): void {
  if (value === undefined) return;
  const v = record(value);
  if (v.publisherName !== undefined)
    item.publisher = metadataText(v.publisherName, 160);
  if (v.namespaceName !== undefined)
    item.namespace = metadataText(v.namespaceName, 160);
  if (v.license !== undefined)
    item.license = metadataText(v.license, 256, true);
}
export function localItem(value: unknown): Item {
  const v = record(value),
    resource = record(v.resource),
    version = record(v.version),
    display = record(v.listingRevision),
    release = record(v.release);
  const releaseId = uuid(release.id),
    identifier = metadataText(resource.canonicalIdentifier),
    k = kind(resource.kind);
  if (
    k === "unknown" ||
    !["published", "deprecated"].includes(String(release.state))
  )
    throw new ClientError("invalid_response");
  const item: Item = {
    key: `local:${releaseId}`,
    identifier,
    kind: k,
    name: metadataText(display.displayName, 160),
    description: paragraph(display.description),
    tags: tags(display.tags),
    versionPresent: true,
    version: metadataText(version.version, 128),
    origin: "local",
    releaseId,
    listingRevisionId: uuid(display.id),
    publisher: uuid(display.publisherId),
    namespace: uuid(resource.namespaceId),
    namespaceId: uuid(resource.namespaceId),
    visibility: literal(display.visibility, 16),
    references: [],
  };
  if (!["private", "tenant", "public"].includes(item.visibility!))
    throw new ClientError("invalid_response");
  presentation(item, v.display);
  if (v.artwork !== undefined) item.artwork = record(v.artwork);
  if (item.artwork && item.listingRevisionId)
    item.artworkTarget = {
      origin: "local",
      listingRevisionId: item.listingRevisionId,
    };
  if (v.ratings !== undefined) item.ratings = ratings(v.ratings)!;
  return item;
}
export function version(value: Record<string, unknown>): {
  versionPresent: boolean;
  version?: string;
} {
  const present =
    value.versionPresent === undefined
      ? Object.hasOwn(value, "version")
      : boolean(value.versionPresent);
  if (present !== Object.hasOwn(value, "version"))
    throw new ClientError("invalid_response");
  return {
    versionPresent: present,
    ...(present ? { version: metadataText(value.version, 128, true) } : {}),
  };
}
export function publicItem(value: unknown, index = 0): Item {
  const v = record(value),
    identifier = literal(v.identifier),
    ver = version(v);
  if (!identifier.startsWith("urn:air:"))
    throw new ClientError("invalid_response");
  const media = v.type === undefined ? "" : literal(v.type, 255);
  const k = mediaKind(media);
  const metadata = v.metadata === undefined ? {} : record(v.metadata);
  return {
    key: `published:${index}:${identifier}:${ver.versionPresent ? JSON.stringify(ver.version) : "absent"}`,
    identifier,
    ...ver,
    kind: k,
    name:
      v.displayName === undefined ? identifier : metadataText(v.displayName),
    description: v.description === undefined ? "" : paragraph(v.description),
    tags: tags(v.tags),
    origin: "published",
    publisher: literal(identifier.split(":")[2], 253),
    references: reference("Metadata document", v.url),
    raw: v,
    ...(publisherRating(v) ? { ratings: publisherRating(v)! } : {}),
    ...(typeof metadata.license === "string"
      ? { license: metadataText(metadata.license, 32 * 1024 ** 2, true) }
      : {}),
  };
}
function publisherRating(entry: Record<string, unknown>): string | undefined {
  const value =
    entry[
      "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/ratings/v1#summary"
    ];
  if (value === undefined) return;
  try {
    const r = record(value),
      count = number(r.count, Math.floor(Number.MAX_SAFE_INTEGER / 5)),
      sum = number(r.sum);
    if (
      !link(r.reporter) ||
      r.identifier !== entry.identifier ||
      r.versionPresent !== Object.hasOwn(entry, "version") ||
      (r.versionPresent
        ? (r.version ?? "") !== entry.version
        : r.version !== undefined && r.version !== "") ||
      r.minimum !== 1 ||
      r.maximum !== 5 ||
      sum < count ||
      sum > count * 5 ||
      !count
    )
      return;
    return `Publisher-declared: ${(Number((10n * BigInt(sum) + BigInt(count) / 2n) / BigInt(count)) / 10).toFixed(1)} / 5 (${count} ratings)`;
  } catch {
    return;
  } // Optional malformed opinions do not hide an otherwise usable resource.
}
export function searchItem(value: unknown): Item {
  const v = record(value),
    origin = v.type;
  if (!["local", "indexed", "live"].includes(String(origin)))
    throw new ClientError("invalid_response");
  const common = {
    key: literal(v.id, 256),
    identifier:
      origin === "local" ? metadataText(v.identifier) : literal(v.identifier),
    ...version(v),
    kind: kind(v.kind),
    name: metadataText(v.displayName),
    description: v.description === undefined ? "" : paragraph(v.description),
    tags: tags(v.tags),
    references: [] as Reference[],
  };
  if (
    common.kind === "unknown" ||
    (origin !== "local" && !common.identifier.startsWith("urn:air:"))
  )
    throw new ClientError("invalid_response");
  number(v.score, 100);
  const item: Item = { ...common, origin: origin as Origin };
  if (origin === "local") {
    const local = record(v.local),
      resource = record(local.resource),
      release = record(local.release),
      listing = record(local.listingRevision);
    item.releaseId = uuid(release.id);
    item.listingRevisionId = uuid(listing.id);
    item.namespaceId = uuid(resource.namespaceId);
    item.namespace = item.namespaceId;
    item.visibility = literal(listing.visibility, 16);
    uuid(resource.id);
    if (
      metadataText(record(local.version).version, 128) !== item.version ||
      resource.canonicalIdentifier !== item.identifier ||
      resource.kind !== item.kind
    )
      throw new ClientError("invalid_response");
    presentation(item, local.display);
  } else {
    const evidence = record(v[origin as string]);
    if (origin === "indexed") {
      list(evidence.sourceIds, 16).forEach(uuid);
      item.sourceCount = number(evidence.sourceCount);
      item.conflict = boolean(evidence.conflict);
      item.sameSourceConflict = boolean(evidence.sameSourceConflict);
    } else {
      uuid(evidence.sourceId);
      const source = link(evidence.source);
      if (!source) throw new ClientError("invalid_response");
      const entry = record(evidence.entry);
      if (
        entry.identifier !== item.identifier ||
        JSON.stringify(version(entry)) !== JSON.stringify(version(v))
      )
        throw new ClientError("invalid_response");
      item.references.push(
        ...reference("Responding source", evidence.source),
        ...reference("Metadata document", entry.url),
      );
      item.raw = entry;
    }
  }
  if (v.publisher !== undefined && !item.publisher)
    item.publisher = literal(v.publisher, 256, true);
  if (v.artwork !== undefined) {
    item.artwork = record(v.artwork);
    if (item.listingRevisionId)
      item.artworkTarget = {
        origin: "local",
        listingRevisionId: item.listingRevisionId,
      };
    else if (origin === "indexed") {
      const e = record(v.indexed);
      if (
        e.presentationSourceId !== undefined &&
        e.presentationGeneration !== undefined
      )
        item.artworkTarget = {
          origin: "indexed",
          target: {
            sourceId: uuid(e.presentationSourceId),
            presentationGeneration: number(e.presentationGeneration),
            identifier: item.identifier,
            versionPresent: item.versionPresent,
            ...(item.versionPresent ? { version: item.version } : {}),
            kind: item.kind,
          },
        };
    } else if (
      origin === "live" &&
      item.artwork.receipt !== undefined &&
      item.artwork.descriptor !== undefined
    )
      item.artworkTarget = {
        origin: "live",
        receipt: literal(item.artwork.receipt, 4096),
        descriptor: record(item.artwork.descriptor),
      };
  }
  if (v.ratings !== undefined) item.ratings = ratings(v.ratings)!;
  if (v.peerRatings !== undefined) {
    const peer = record(v.peerRatings);
    const declared = ratings(peer)!;
    item.references.push(...reference("Peer rating reporter", peer.reporter));
    item.ratings = `${item.ratings ? item.ratings + "; " : ""}Peer-reported: ${declared}`;
  }
  return item;
}
export function query(
  input: unknown,
  published: boolean,
  explore = false,
  draft = false,
): Query {
  const v = record(input);
  if (
    Object.keys(v).some(
      (k) =>
        !Object.hasOwn(emptyQuery, k) &&
        !["origins", "kinds", "tags", "categories"].includes(k),
    )
  )
    throw new ClientError("invalid_query");
  const q = { ...emptyQuery, ...v } as Query;
  for (const [key, max, length] of [
    ["origins", 3, 8],
    ["kinds", 4, 16],
    ["tags", 32, 80],
    ["categories", 16, 80],
  ] as const) {
    if (q[key] === undefined) continue;
    const values = list(q[key], max).map((value) =>
      metadataText(value, length),
    );
    if (
      new Set(values).size !== values.length ||
      (key === "origins" &&
        (!values.length ||
          values.some((v) => !["local", "indexed", "live"].includes(v)))) ||
      (key === "kinds" && values.some((v) => !kinds.includes(v as Kind))) ||
      (key === "categories" && values.some((v) => !v.trim()))
    )
      throw new ClientError("invalid_query");
    if (published) throw new ClientError("unsupported_filter");
    // Own the submitted selection across asynchronous consent and transport.
    Object.assign(q, { [key]: values });
  }
  for (const [key, max] of Object.entries({
    text: 500,
    kind: 16,
    tag: 80,
    namespace: 36,
    origin: 8,
    publisher: 256,
    license: 256,
    category: 80,
    visibility: 16,
    federation: 4,
  }))
    metadataText(q[key as keyof Query], draft ? 2 * max : max, true);
  if (
    (!draft &&
      (Buffer.byteLength(q.text) > 500 ||
        Buffer.byteLength(q.publisher) > 256)) ||
    (!draft &&
      ((q.license !== "" && !q.license.trim()) ||
        (q.category !== "" && !q.category.trim()))) ||
    !["", ...kinds].includes(q.kind) ||
    !["local", "indexed", "live"].includes(q.origin) ||
    !["", "private", "tenant", "public"].includes(q.visibility) ||
    !["none", "auto"].includes(q.federation) ||
    !Number.isSafeInteger(q.limit) ||
    q.limit < 1 ||
    q.limit > 100
  )
    throw new ClientError("invalid_query");
  if (q.namespace && !draft) uuid(q.namespace);
  if (
    !draft &&
    published &&
    (q.namespace || q.visibility || q.origin !== "local")
  )
    throw new ClientError("unsupported_filter");
  if (!draft && !published && q.namespace && !localOnly(q))
    throw new ClientError("namespace_is_local");
  if (
    published &&
    !explore &&
    !draft &&
    !q.text.trim() &&
    (q.tag || q.license || q.category || q.federation !== "none")
  )
    throw new ClientError("published_search_needs_text");
  return q;
}
export function localOnly(q: Query): boolean {
  const origins = q.origins ?? [q.origin];
  return origins.length === 1 && origins[0] === "local";
}
export function hasLive(q: Query): boolean {
  return (q.origins ?? [q.origin]).includes("live") || q.federation === "auto";
}
const mediaTypes: Record<string, string[]> = {
  agent: [
    "application/a2a-agent-card+json",
    "application/vnd.open-agentic-catalog.agent+json",
  ],
  mcp_server: [
    "application/mcp-server-card+json",
    "application/vnd.mcp.server+json",
    "application/vnd.open-agentic-catalog.mcp-registration+json",
  ],
  skill: [
    "application/agent-skills+md",
    "application/agent-skills+json",
    "application/agent-skills+zip",
    "application/agent-skills+gzip",
    "text/markdown",
    'text/markdown; profile="urn:air:agent-skills"',
  ],
  plugin: ["application/agent-plugins+zip", "application/agent-plugins+gzip"],
};
function mediaKind(media: string): Kind | "unknown" {
  const base = media.split(";")[0]!.trim().toLowerCase();
  return (
    (Object.entries(mediaTypes).find(([, types]) =>
      types.some((type) => type.split(";")[0]!.trim().toLowerCase() === base),
    )?.[0] as Kind | undefined) ?? "unknown"
  );
}
function facetFilter(
  field: string,
  value: string,
): { field: string; value: string } | undefined {
  if (field === "type") {
    const kind = mediaKind(value);
    return kind === "unknown" ? undefined : { field: "kind", value: kind };
  }
  const name = (
    {
      kind: "kind",
      tag: "tag",
      tags: "tag",
      publisher: "publisher",
      namespace: "namespace",
      category: "category",
      visibility: "visibility",
      "metadata.license": "license",
      "http://purl.org/dc/terms/subject": "category",
    } as Record<string, string>
  )[field];
  if (!name) return;
  try {
    query({ [name]: value }, false);
  } catch {
    return;
  }
  return { field: name, value };
}
export function requestFor(
  q: Query,
  published: boolean,
  next?: string,
): { path: string; body?: Record<string, unknown> } {
  if (next !== undefined) literal(next, 4096);
  if (published) {
    if (!q.text.trim()) {
      const params = new URLSearchParams({ pageSize: String(q.limit) });
      if (/[',\\]/u.test(q.publisher))
        throw new ClientError("invalid_publisher_filter");
      const filters = [
        q.kind ? `type = '${mediaTypes[q.kind]!.join(",")}'` : "",
        q.publisher ? `publisherId = '${q.publisher}'` : "",
      ].filter(Boolean);
      if (filters.length) params.set("filter", filters.join(" AND "));
      if (next) params.set("pageToken", next);
      return { path: `/ard/v1/agents?${params}` };
    }
    const filter: Record<string, string | string[]> = {};
    if (q.kind) filter.type = mediaTypes[q.kind]!;
    if (q.tag) filter.tags = q.tag;
    if (q.publisher) filter.publisher = q.publisher;
    if (q.license) filter["metadata.license"] = q.license;
    if (q.category) filter["http://purl.org/dc/terms/subject"] = q.category;
    return {
      path: "/ard/v1/search",
      body: {
        query: {
          text: q.text,
          ...(Object.keys(filter).length ? { filter } : {}),
        },
        federation: q.federation,
        pageSize: q.limit,
        ...(next ? { pageToken: next } : {}),
      },
    };
  }
  if (
    !q.text &&
    !q.tag &&
    !q.publisher &&
    !q.license &&
    !q.category &&
    localOnly(q) &&
    !q.kinds?.length &&
    !q.tags?.length &&
    !q.categories?.length
  ) {
    const p = new URLSearchParams({ limit: String(q.limit) });
    for (const [key, value] of Object.entries({
      namespaceId: q.namespace,
      kind: q.kind,
      visibility: q.visibility,
      cursor: next,
    }))
      if (value) p.set(key, value);
    return { path: `/api/v1/catalog/resources?${p}` };
  }
  return { path: "/api/v1/catalog/search", body: controlQuery(q, next) };
}
function controlQuery(q: Query, next?: string): Record<string, unknown> {
  return {
    text: q.text,
    origins: q.origins ?? [q.origin],
    limit: q.limit,
    ...(q.kinds?.length || q.kind
      ? { kinds: q.kinds?.length ? q.kinds : [q.kind] }
      : {}),
    ...(q.tags?.length || q.tag
      ? { tags: q.tags?.length ? q.tags : [q.tag] }
      : {}),
    ...(q.namespace ? { namespaceId: q.namespace } : {}),
    ...(q.publisher ? { publisher: q.publisher } : {}),
    ...(q.license ? { license: q.license } : {}),
    ...(q.categories?.length || q.category
      ? { categories: q.categories?.length ? q.categories : [q.category] }
      : {}),
    ...(q.visibility ? { visibility: q.visibility } : {}),
    ...(next ? { cursor: next } : {}),
  };
}
export function page(value: unknown, path: string, timing = ""): Page {
  const v = record(value);
  const isPublic = path.startsWith("/ard/"),
    search = path.endsWith("/search");
  const items = list(search && isPublic ? v.results : v.items, 100).map(
    (item, index) =>
      isPublic
        ? publicItem(item, index)
        : search
          ? searchItem(item)
          : localItem(item),
  );
  if (new Set(items.map((i) => i.key)).size !== items.length)
    throw new ClientError("invalid_response");
  const next = isPublic ? v.pageToken : v.nextCursor;
  const partial = isPublic
    ? /partial/iu.test(timing)
    : search
      ? boolean(v.partial)
      : false;
  let summary: string;
  if (!isPublic && search)
    summary = `${number(v.durableTotal)} durable matches; ${number(v.liveCount, 50)} live results on this page`;
  else if (search)
    summary = `${items.length} results on this page; no global total supplied`;
  else summary = `${number(v.total)} matches`;
  if (partial) summary += "; partial results";
  if (v.diagnostics !== undefined)
    summary +=
      "; " +
      list(v.diagnostics, 33)
        .map((d) => {
          const status = literal(record(d).status, 80);
          const meanings: Record<string, string> = {
            failed: "an approved source request failed",
            invalid_response: "a source returned unusable evidence",
            insufficient_evidence:
              "live metadata lacks declarations required by these filters",
            policy_stale:
              "source policy changed; administrator review is needed",
            fanout_limited: "the approved source request limit was reached",
            diagnostics_truncated:
              "additional diagnostics exceeded the response limit",
            live_results_limited:
              "the bounded live result population is incomplete",
            continuation_unavailable:
              "further peer results could not be continued within this read",
          };
          return `${status}: ${meanings[status] ?? "no additional public explanation is available"}`;
        })
        .join(", ");
  if (v.fallbackReason !== undefined)
    summary += `; lexical fallback: ${literal(v.fallbackReason, 32)}`;
  if (v.partialIndex === true) summary += "; partial semantic index";
  return {
    items,
    summary,
    partial,
    ...(next === undefined || next === "" ? {} : { next: literal(next, 4096) }),
  };
}
function indexedPath(item: {
  identifier: string;
  versionPresent: boolean;
  version?: string;
}): string {
  const p = new URLSearchParams({
    identifier: item.identifier,
    versionPresent: String(item.versionPresent),
  });
  if (item.versionPresent) p.set("version", item.version!);
  return `/api/v1/catalog/indexed-record?${p}`;
}
function bookmark(value: unknown): Bookmark {
  const v = record(value);
  if (Object.keys(v).length === 1 && v.releaseId !== undefined)
    return { releaseId: uuid(v.releaseId) };
  if (
    Object.keys(v).some(
      (k) => !["identifier", "versionPresent", "version"].includes(k),
    )
  )
    throw new ClientError("invalid_bookmarks");
  const id = literal(v.identifier);
  if (!id.startsWith("urn:air:")) throw new ClientError("invalid_bookmarks");
  return { identifier: id, ...version(v) };
}
export function bookmarkLabel(value: Bookmark): string {
  return "releaseId" in value
    ? `Release ${value.releaseId}`
    : `${value.identifier} · ${value.versionPresent ? (value.version === "" ? "empty version" : `version ${JSON.stringify(value.version)}`) : "version not supplied"}`;
}
export function exactReference(input: string): {
  origin: "local" | "indexed";
  identifier: string;
  versionPresent: boolean;
  version?: string;
  releaseId?: string;
} {
  if (typeof input !== "string" || Buffer.byteLength(input) > 8192)
    throw new ClientError("invalid_reference");
  const value = record(uniqueJSON(input, "invalid_reference"));
  const keys = [
    "origin",
    "identifier",
    "versionPresent",
    ...(value.versionPresent === true ? ["version"] : []),
    ...(value.origin === "local" ? ["releaseId"] : []),
  ];
  if (
    Object.keys(value).length !== keys.length ||
    Object.keys(value).some((k) => !keys.includes(k)) ||
    !["local", "indexed"].includes(value.origin as string) ||
    (value.origin === "local" && value.versionPresent !== true)
  )
    throw new ClientError("invalid_reference");
  const identifier =
    value.origin === "local"
      ? metadataText(value.identifier)
      : literal(value.identifier);
  if (value.origin !== "local" && !identifier.startsWith("urn:air:"))
    throw new ClientError("invalid_reference");
  return {
    origin: value.origin as "local" | "indexed",
    identifier,
    ...version(value),
    ...(value.origin === "local" ? { releaseId: uuid(value.releaseId) } : {}),
  };
}
export class Browser {
  private pageRequest: ReturnType<typeof requestFor> | undefined;
  private revision = 0;
  private continuation: { query: Query; cursor: string } | undefined;
  private abort = new AbortController();
  private listeners = new Set<() => void>();
  private account: string | undefined;
  private pendingWrites: Promise<void> = Promise.resolve();
  readonly state: {
    query: Query;
    page?: Page;
    detail?: Detail;
    facets?: Facet[];
    facetQuery?: Query;
    image?: { index: number; value: string; alt: string; caption: string };
    canNext: boolean;
    scope: number;
    busy: boolean;
    error?: string;
    bookmarks: Bookmark[];
  } = {
    query: { ...emptyQuery },
    busy: false,
    bookmarks: [],
    canNext: false,
    scope: 0,
  };
  private subscription;
  constructor(
    private session: Session,
    private store: Store,
    private host: string,
    private peerReplay?: PeerReplayConsent,
  ) {
    this.subscription = session.onChange(() => this.reset());
  }
  onChange(listener: () => void): { dispose(): void } {
    this.listeners.add(listener);
    return {
      dispose: () => {
        this.listeners.delete(listener);
      },
    };
  }
  private emit(): void {
    for (const listener of this.listeners) listener();
  }
  reset(): void {
    this.pageRequest = undefined;
    this.continuation = undefined;
    this.state.canNext = false;
    this.state.scope++;
    this.revision++;
    this.abort.abort();
    this.abort = new AbortController();
    this.account = undefined;
    delete this.state.page;
    delete this.state.detail;
    delete this.state.facets;
    delete this.state.facetQuery;
    delete this.state.image;
    delete this.state.error;
    this.state.query = { ...emptyQuery };
    this.state.bookmarks = [];
    this.state.busy = false;
    this.emit();
  }
  dispose(): void {
    this.reset();
    this.subscription.dispose();
    this.listeners.clear();
  }
  cancel(): void {
    if (this.state.busy) this.abort.abort();
  }
  back(): void {
    if (this.state.busy) throw new ClientError("read_in_progress");
    delete this.state.detail;
    delete this.state.image;
    this.emit();
  }
  private async authorized(signal?: AbortSignal) {
    const connection = this.session.connection(),
      guard = this.session.guard(connection),
      revision = this.revision;
    const connected = await this.session.test(signal);
    guard();
    if (revision !== this.revision) throw new ClientError("connection_changed");
    const identity = connected.identity;
    const key = createHash("sha256")
      .update(
        JSON.stringify([
          binding(connection, this.host),
          identity
            ? [
                identity.tenantId,
                identity.realmId,
                identity.membershipId,
                identity.subject,
              ]
            : "published",
        ]),
      )
      .digest("hex");
    // Session verification fences account changes for every private view.
    this.account = key;
    if (connection.mode === "control") {
      const all = record(this.store.get("catalogBookmarks") ?? {}),
        values = all[key] ?? [];
      this.state.bookmarks = list(values, 100).map(bookmark);
    }
    return () => {
      guard();
      if (signal?.aborted) throw new ClientError("request_canceled");
      if (revision !== this.revision)
        throw new ClientError("connection_changed");
    };
  }
  private async operation<T>(
    action: (check: () => void) => Promise<T>,
    signal?: AbortSignal,
  ): Promise<T> {
    const revision = ++this.revision;
    this.abort.abort();
    this.abort = new AbortController();
    const controller = this.abort;
    const cancel = () => controller.abort();
    if (signal?.aborted) cancel();
    else signal?.addEventListener("abort", cancel, { once: true });
    this.state.busy = true;
    delete this.state.error;
    this.emit();
    try {
      const check = await this.authorized(controller.signal);
      check();
      const value = await action(check);
      check();
      return value;
    } catch (error) {
      if (revision === this.revision) {
        delete this.state.page;
        delete this.state.detail;
        delete this.state.facets;
        delete this.state.facetQuery;
        delete this.state.image;
        const code = controller.signal.aborted
          ? "request_canceled"
          : error instanceof ClientError
            ? error.code
            : "request_failed";
        if (
          ![
            "service_unavailable",
            "request_canceled",
            "timeout",
            "request_timeout",
            "network_error",
            "aborted",
            "cancelled",
            "deadline_exceeded",
            "request_failed",
          ].includes(code)
        ) {
          this.continuation = undefined;
          this.state.canNext = false;
          this.pageRequest = undefined;
        }
        this.state.error = code;
      }
      if (controller.signal.aborted && revision === this.revision)
        throw new ClientError("request_canceled");
      throw error;
    } finally {
      signal?.removeEventListener("abort", cancel);
      if (revision === this.revision) {
        this.state.busy = false;
        this.emit();
      }
    }
  }
  async search(input: unknown = this.state.query, next = false): Promise<Page> {
    const q = query(input, this.session.connection().mode === "published");
    if (next && JSON.stringify(q) !== JSON.stringify(this.state.query))
      throw new ClientError("query_changed");
    const cursor = next ? this.continuation?.cursor : undefined;
    if (next && !cursor) throw new ClientError("no_next_page");
    if (next && JSON.stringify(q) !== JSON.stringify(this.continuation?.query))
      throw new ClientError("query_changed");
    this.state.query = q;
    if (!next) {
      this.continuation = undefined;
      this.state.canNext = false;
    }
    return this.operation(async (check) => {
      const request = requestFor(
        q,
        this.session.connection().mode === "published",
        cursor,
      );
      const response = await this.session.read(request.path, {
        ...(request.body
          ? {
              method: "POST",
              headers: { "content-type": "application/json" },
              body: JSON.stringify(request.body),
            }
          : {}),
        signal: this.abort.signal,
      });
      check();
      if (
        !/^application\/json(?:;|$)/iu.test(
          response.headers.get("content-type") ?? "",
        )
      )
        throw new ClientError("invalid_response");
      let value: unknown;
      try {
        value = decodeJSON(response.text());
      } catch {
        throw new ClientError("invalid_response");
      }
      const result = page(
        value,
        request.path,
        response.headers.get("server-timing") ?? "",
      );
      this.state.page = result;
      this.continuation = result.next
        ? { query: q, cursor: result.next }
        : undefined;
      this.state.canNext = !!this.continuation;
      delete this.state.image;
      this.pageRequest = request;
      delete this.state.detail;
      delete this.state.facets;
      return result;
    });
  }
  async explore(input: unknown = this.state.query): Promise<Facet[]> {
    const q = query(
        input,
        this.session.connection().mode === "published",
        true,
      ),
      published = this.session.connection().mode === "published";
    if (!published && !localOnly(q)) throw new ClientError("facets_are_local");
    return this.operation(async (check) => {
      const fields = published
        ? [
            "type",
            "tags",
            "publisher",
            "metadata.license",
            "http://purl.org/dc/terms/subject",
          ]
        : ["kind", "tag", "publisher", "namespace", "category", "visibility"];
      const facets = fields.map((field) => ({ field, limit: 20 }));
      const body = published
        ? {
            query: record(
              requestFor({ ...q, text: q.text || "*" }, true).body!.query,
            ),
            resultType: { facets },
          }
        : {
            query: Object.fromEntries(
              Object.entries(controlQuery(q)).filter(
                ([k]) => !["origins", "limit", "cursor"].includes(k),
              ),
            ),
            facets,
          };
      // Explore permits empty text, unlike Search. It does not contact live sources.
      if (published) record(body.query).text = q.text;
      const value = await this.session.json(
        published ? "/ard/v1/explore" : "/api/v1/catalog/explore",
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(body),
          signal: this.abort.signal,
        },
      );
      check();
      const result = record(value.facets);
      const projected = Object.entries(result).map(([field, data]) => {
        if (!fields.includes(field)) throw new ClientError("invalid_response");
        const f = record(data);
        return {
          field,
          buckets: list(f.buckets, 100).map((b) => {
            const value = metadataText(record(b).value),
              filter = facetFilter(field, value);
            return {
              value,
              count: number(record(b).count),
              ...(filter ? { filter } : {}),
            };
          }),
          other: f.otherCount === undefined ? 0 : number(f.otherCount),
        };
      });
      this.state.facets = projected;
      this.state.facetQuery = q;
      return projected;
    });
  }
  async select(key: string, signal?: AbortSignal): Promise<Detail> {
    const item = this.state.page?.items.find((i) => i.key === key);
    if (!item) throw new ClientError("selection_changed");
    return this.operation(async (check) => {
      let current = item,
        raw: Record<string, unknown> | undefined;
      if (item.origin === "local") {
        raw = await this.session.json(
          `/api/v1/catalog/resources/${item.releaseId}`,
          { signal: this.abort.signal, responseBytes: 32 * 1024 ** 2 },
        );
        check();
        current = localItem(raw);
        if (
          current.releaseId !== item.releaseId ||
          current.identifier !== item.identifier ||
          current.version !== item.version
        )
          throw new ClientError("selection_changed");
      } else if (item.origin === "indexed") {
        raw = await this.session.json(indexedPath(item), {
          signal: this.abort.signal,
        });
        check();
        current = searchItem(raw);
        if (
          current.origin !== "indexed" ||
          current.identifier !== item.identifier ||
          current.versionPresent !== item.versionPresent ||
          current.version !== item.version
        )
          throw new ClientError("selection_changed");
      }
      // Exact detail does not include the current collection artwork projection.
      if (item.artwork && !current.artwork) {
        current.artwork = item.artwork;
        if (item.artworkTarget) current.artworkTarget = item.artworkTarget;
      }
      const packages: unknown[] = [];
      const references = [...current.references],
        documents: DocumentRef[] = [],
        facts: Reference[] = [];
      if (raw && current.origin === "local") {
        const listing = record(raw.listingRevision);
        for (const [label, key] of Object.entries({
          Documentation: "documentationUrl",
          Support: "supportUrl",
          Terms: "termsUrl",
        }))
          references.push(...reference(label, listing[key]));
        const extensions =
          listing.extensions === undefined ? {} : record(listing.extensions);
        const descriptor =
          extensions[
            "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor"
          ];
        if (!current.artwork && descriptor !== undefined) {
          current.artwork = { descriptor: record(descriptor) };
          current.artworkTarget = {
            origin: "local",
            listingRevisionId: current.listingRevisionId,
          };
        }
        if (listing.license !== undefined)
          facts.push({
            label: "Declared license",
            value: metadataText(listing.license, 32 * 1024 ** 2, true),
          });
        for (const value of list(
          record(raw.versionRevision).representations,
          16,
        )) {
          const r = record(value),
            label = literal(r.mediaType, 255),
            doc: DocumentRef = { label, mediaType: label };
          if (r.artifactId !== undefined) {
            const digest = literal(r.artifactId, 64);
            if (!/^[a-f0-9]{64}$/u.test(digest))
              throw new ClientError("invalid_response");
            doc.path = `/api/v1/artifacts/${digest}/content`;
            doc.digest = `sha256:${digest}`;
            doc.size = number(r.size, 16 * 1024 ** 2);
          } else if (r.data !== undefined) doc.value = record(r.data);
          if (r.url !== undefined) {
            const url = link(r.url);
            if (url) {
              doc.url = url;
              references.push(
                ...reference("Declared metadata/archive URL", url),
              );
            }
          }
          documents.push(doc);
          if (r.package !== undefined) packages.push(r);
          if (r.plugin !== undefined) {
            packages.push(r);
            const plugin = record(r.plugin);
            for (const v of list(plugin.documents, 130)) {
              const document = record(v);
              const content = paragraph(document.content, 500000);
              if (Buffer.byteLength(content) > 500000)
                throw new ClientError("invalid_response");
              documents.push({ label: literal(document.path, 1024), content });
            }
          }
          if (r.handoff !== undefined) {
            const h = record(r.handoff);
            for (const [label, key] of Object.entries({
              Git: "git",
              OCI: "oci",
            }))
              if (h[key] !== undefined) {
                const fields = record(h[key]);
                for (const name of [
                  "repository",
                  "commit",
                  "directory",
                  "reference",
                ])
                  if (fields[name] !== undefined)
                    references.push(
                      ...reference(`${label} ${name}`, fields[name]),
                    );
              }
          }
        }
      } else if (current.raw) {
        documents.push({
          label: "Registry-declared entry (not original signed bytes)",
          value: current.raw,
        });
        const entry = current.raw;
        if (entry.data !== undefined)
          documents.push({
            label: "Peer-declared inline metadata",
            ...(typeof entry.type === "string"
              ? { mediaType: entry.type.split(";")[0]!.trim().toLowerCase() }
              : {}),
            value: record(entry.data),
          });
        if (entry.url !== undefined) {
          const url =
            link(entry.url) ??
            (typeof entry.url === "string" &&
            entry.url.startsWith(
              this.session.connection().origin + "/public/v1/artifacts/",
            )
              ? entry.url
              : undefined);
          if (url) {
            const target = new URL(url);
            documents.push({
              label: "Declared metadata document",
              ...(typeof entry.type === "string"
                ? { mediaType: entry.type.split(";")[0]!.trim().toLowerCase() }
                : {}),
              ...(target.origin === this.session.connection().origin &&
              /^\/public\/v1\/artifacts\/[a-f0-9]{64}$/u.test(
                target.pathname,
              ) &&
              !target.search &&
              !target.hash
                ? {
                    path: target.pathname,
                    digest: `sha256:${target.pathname.split("/").at(-1)}`,
                  }
                : { url }),
            });
          }
        }
      }
      if (raw && current.origin === "indexed") {
        const evidence = record(raw.indexed);
        facts.push(
          {
            label: "Indexed sources",
            value: String(number(evidence.sourceCount)),
          },
          {
            label: "Conflicting declarations",
            value: boolean(evidence.conflict) ? "Yes" : "No",
          },
        );
        documents.push({
          label: "Current indexed metadata projection",
          value: raw,
        });
      }
      const detail = { item: current, references, documents, facts, packages };
      delete this.state.image;
      this.state.detail = detail;
      return detail;
    }, signal);
  }
  async document(
    index: number,
    signal?: AbortSignal,
  ): Promise<{ bytes: Uint8Array; label: string }> {
    const before = this.state.detail,
      ref = before?.documents[index];
    if (!before || !ref) throw new ClientError("selection_changed");
    return this.operation(async (check) => {
      // Current exact release/indexed authority is checked again before private bytes.
      if (before.item.releaseId)
        await this.session.json(
          `/api/v1/catalog/resources/${before.item.releaseId}`,
          { signal: this.abort.signal, responseBytes: 32 * 1024 ** 2 },
        );
      else if (before.item.origin === "indexed")
        await this.session.json(indexedPath(before.item), {
          signal: this.abort.signal,
        });
      check();
      if (ref.content !== undefined)
        return { bytes: Buffer.from(ref.content), label: ref.label };
      if (ref.value !== undefined)
        return {
          bytes: Buffer.from(encodeProjection(ref.value)),
          label: ref.label,
        };
      if (!ref.path)
        throw new ClientError("external_document_needs_confirmation");
      const response = await this.session.read(ref.path, {
        signal: this.abort.signal,
        responseBytes: 16 * 1024 ** 2,
      });
      check();
      if (ref.size !== undefined && response.bytes.length !== ref.size)
        throw new ClientError("document_integrity_failed");
      if (
        ref.digest &&
        `sha256:${createHash("sha256").update(response.bytes).digest("hex")}` !==
          ref.digest
      )
        throw new ClientError("document_integrity_failed");
      return { bytes: response.bytes, label: ref.label };
    }, signal);
  }
  /** Complete locally retained packages only. Recheck current release access on each read. */
  packageConsumer(index: number): () => Promise<unknown> {
    const detail = this.state.detail;
    if (
      !detail ||
      !["skill", "plugin"].includes(detail.item.kind) ||
      detail.item.origin !== "local" ||
      !Number.isSafeInteger(index) ||
      !detail.packages?.[index]
    )
      throw new ClientError("complete_skill_package_required");
    const expected = encodeProjection(detail.packages[index]);
    const guard = this.session.guard(this.session.connection());
    return async () => {
      guard();
      const isolated = new Browser(this.session, this.store, this.host);
      try {
        await isolated.authorized();
        guard();
        isolated.state.page = {
          items: [detail.item],
          summary: "",
          partial: false,
        };
        const latest = await isolated.select(detail.item.key);
        guard();
        const value = latest.packages?.[index];
        if (!value || encodeProjection(value) !== expected)
          throw new ClientError("selection_changed");
        return value;
      } finally {
        isolated.dispose();
      }
    };
  }
  /** Freeze a reference, not access. Each consumer read checks current authority. */
  consumer(
    index: number,
    validate: () => void = () => {},
  ): (signal?: AbortSignal) => Promise<DocumentRef> {
    const detail = this.state.detail,
      request = this.pageRequest;
    if (
      !detail?.documents[index] ||
      !consumerFamily(detail.documents[index]!, detail.item.kind)
    )
      throw new ClientError("selection_changed");
    const item = detail.item,
      guard = this.session.guard(this.session.connection());
    const submitted = { ...this.state.query };
    const evidence =
      item.raw === undefined ? undefined : encodeProjection(item.raw);
    return async (signal) => {
      const check = () => {
        guard();
        validate();
        if (signal?.aborted) throw new ClientError("request_canceled");
      };
      check();
      const isolated = new Browser(
        this.session,
        this.store,
        this.host,
        this.peerReplay,
      );
      try {
        await isolated.authorized(signal);
        check();
        let current = item;
        if (["live", "published"].includes(item.origin)) {
          if (!request) throw new ClientError("selection_changed");
          if (hasLive(submitted)) {
            if (!this.peerReplay)
              throw new ClientError("peer_consent_required");
            const accepted = await abortable(
              this.peerReplay(
                {
                  query: { ...submitted },
                  continuation: !!(
                    request.body?.cursor ||
                    request.body?.pageToken ||
                    new URL(
                      request.path,
                      "https://catalog.invalid",
                    ).searchParams.has("pageToken")
                  ),
                },
                signal,
              ),
              signal,
            );
            check();
            if (!accepted) throw new ClientError("peer_query_cancelled");
          }
          const value = await this.session.json(
            request.path,
            request.body
              ? {
                  method: "POST",
                  headers: { "content-type": "application/json" },
                  body: JSON.stringify(request.body),
                  ...(signal ? { signal } : {}),
                }
              : signal
                ? { signal }
                : {},
          );
          check();
          const found = page(value, request.path).items.find(
            (candidate) => candidate.key === item.key,
          );
          if (!found || encodeProjection(found.raw) !== evidence)
            throw new ClientError("selection_changed");
          current = found;
        }
        isolated.state.page = { items: [current], summary: "", partial: false };
        const latest = await isolated.select(current.key, signal);
        check();
        const ref = latest.documents[index];
        if (!ref || ref.label !== detail.documents[index]!.label)
          throw new ClientError("selection_changed");
        if (ref.url && !ref.path && ref.value === undefined) return ref;
        const result = await isolated.document(index, signal);
        check();
        const acquired = { ...ref, bytes: result.bytes };
        delete acquired.value;
        delete acquired.content;
        return acquired;
      } finally {
        isolated.dispose();
      }
    };
  }
  async image(
    index: number,
  ): Promise<{ value: string; alt: string; caption: string }> {
    const item = this.state.detail?.item;
    if (
      !item?.artwork ||
      !item.artworkTarget ||
      !Number.isSafeInteger(index) ||
      index < 0 ||
      index > 8
    )
      throw new ClientError("artwork_unavailable");
    const descriptor = record(item.artwork.descriptor),
      asset = record(
        index === 0
          ? descriptor.icon
          : list(descriptor.screenshots, 8)[index - 1],
      );
    const alt = metadataText(asset.alt, 256),
      rights = asset.rights === undefined ? undefined : record(asset.rights);
    const caption = [
      asset.caption === undefined ? "" : metadataText(asset.caption, 512),
      rights
        ? metadataText(rights.attribution, 1024)
        : "Native icon declaration",
    ]
      .filter(Boolean)
      .join(" · ");
    if (this.state.image?.index === index) return this.state.image;
    return this.operation(async (check) => {
      const response = await this.session.read("/api/v1/artwork/read", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          ...item.artworkTarget,
          slot: index === 0 ? "icon" : "screenshot",
          ...(index > 0 ? { index: index - 1 } : {}),
        }),
        signal: this.abort.signal,
        responseBytes: 4 * 1024 ** 2,
      });
      check();
      const mime = response.headers.get("content-type");
      if (!["image/png", "image/jpeg"].includes(mime ?? ""))
        throw new ClientError("invalid_response");
      const bytes = Buffer.from(response.bytes);
      if (
        mime === "image/png"
          ? !bytes
              .subarray(0, 8)
              .equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]))
          : bytes[0] !== 255 || bytes[1] !== 216 || bytes[2] !== 255
      )
        throw new ClientError("invalid_response");
      const digest = `sha-256=:${createHash("sha256").update(bytes).digest("base64")}:`;
      if (response.headers.get("content-digest") !== digest)
        throw new ClientError("document_integrity_failed");
      const result = {
        index,
        value: `data:${mime};base64,${bytes.toString("base64")}`,
        alt,
        caption,
      };
      this.state.image = result;
      return result;
    });
  }
  async manifest(): Promise<Uint8Array> {
    return this.operation(async (check) => {
      if (this.session.connection().mode !== "published")
        throw new ClientError("wrong_api_mode");
      const result = await this.session.read("/.well-known/ard.json", {
        responseBytes: 32 * 1024 ** 2,
        signal: this.abort.signal,
      });
      check();
      const value = record(decodeJSON(result.text()));
      list(value.entries, 100000).forEach((v, i) => publicItem(v, i));
      return result.bytes;
    });
  }
  async saveBookmark(): Promise<void> {
    const item = this.state.detail?.item;
    if (!item || !["local", "indexed"].includes(item.origin))
      throw new ClientError("bookmark_unavailable");
    await this.operation(async (check) => {
      const value: Bookmark = item.releaseId
        ? { releaseId: item.releaseId }
        : {
            identifier: item.identifier,
            versionPresent: item.versionPresent,
            ...(item.versionPresent ? { version: item.version! } : {}),
          };
      check();
      await this.writeBookmarks((values) => {
        if (!values.some((v) => JSON.stringify(v) === JSON.stringify(value))) {
          if (values.length >= 100) throw new ClientError("bookmark_limit");
          values.push(value);
        }
        return values;
      });
      check();
    });
  }
  async removeBookmark(index: number): Promise<void> {
    const expected = this.state.bookmarks[index];
    if (!expected) throw new ClientError("selection_changed");
    await this.operation(async (check) => {
      if (
        JSON.stringify(this.state.bookmarks[index]) !== JSON.stringify(expected)
      )
        throw new ClientError("selection_changed");
      await this.writeBookmarks((values) => {
        if (JSON.stringify(values[index]) !== JSON.stringify(expected))
          throw new ClientError("selection_changed");
        return values.filter((_, i) => i !== index);
      });
      check();
    });
  }
  async forgetBookmarks(account: string): Promise<void> {
    if (!/^[a-f0-9]{64}$/u.test(account))
      throw new ClientError("invalid_bookmarks");
    const operation = this.pendingWrites.then(async () => {
      const all = record(this.store.get("catalogBookmarks") ?? {});
      const next = { ...all };
      delete next[account];
      await this.store.update("catalogBookmarks", next);
      if (this.account === account) this.state.bookmarks = [];
      this.emit();
    });
    this.pendingWrites = operation.catch(() => {});
    await operation;
  }
  bookmarkAccounts(): { key: string; count: number }[] {
    const all = record(this.store.get("catalogBookmarks") ?? {});
    if (Object.keys(all).length > 20)
      throw new ClientError("invalid_bookmarks");
    return Object.entries(all).map(([key, value]) => {
      if (!/^[a-f0-9]{64}$/u.test(key))
        throw new ClientError("invalid_bookmarks");
      return { key, count: list(value, 100).length };
    });
  }
  async openReference(input: string): Promise<Detail> {
    const reference = exactReference(input);
    if (this.session.connection().mode !== "control")
      throw new ClientError("wrong_api_mode");
    const revision = this.revision + 1;
    await this.operation(async (check) => {
      const item =
        reference.origin === "local"
          ? localItem(
              await this.session.json(
                `/api/v1/catalog/resources/${reference.releaseId}`,
                { signal: this.abort.signal, responseBytes: 32 * 1024 ** 2 },
              ),
            )
          : searchItem(
              await this.session.json(indexedPath(reference), {
                signal: this.abort.signal,
              }),
            );
      check();
      if (
        item.origin !== reference.origin ||
        item.identifier !== reference.identifier ||
        item.versionPresent !== reference.versionPresent ||
        item.version !== reference.version ||
        item.releaseId !== reference.releaseId
      )
        throw new ClientError("selection_changed");
      this.state.page = {
        items: [item],
        summary: "Exact copied reference",
        partial: false,
      };
      this.pageRequest = undefined;
      this.continuation = undefined;
      this.state.canNext = false;
      delete this.state.facets;
      delete this.state.facetQuery;
      delete this.state.detail;
      delete this.state.image;
    });
    if (revision !== this.revision) throw new ClientError("selection_changed");
    return this.select(this.state.page!.items[0]!.key);
  }
  private async writeBookmarks(
    change: (values: Bookmark[]) => Bookmark[],
  ): Promise<void> {
    const account = this.account!,
      revision = this.revision;
    const operation = this.pendingWrites.then(async () => {
      if (revision !== this.revision)
        throw new ClientError("connection_changed");
      const all = record(this.store.get("catalogBookmarks") ?? {}),
        values = list(all[account] ?? [], 100).map(bookmark);
      const next = change(values);
      if (
        next.length &&
        !Object.hasOwn(all, account) &&
        Object.keys(all).length >= 20
      )
        throw new ClientError("bookmark_account_limit");
      const updated = { ...all };
      if (next.length) updated[account] = next;
      else delete updated[account];
      await this.store.update("catalogBookmarks", updated);
      if (revision === this.revision) {
        this.state.bookmarks = next;
        this.emit();
      }
    });
    this.pendingWrites = operation.catch(() => {});
    await operation;
  }
  async reopen(index: number): Promise<Detail> {
    const saved = this.state.bookmarks[index];
    if (!saved) throw new ClientError("selection_changed");
    const revision = this.revision + 1;
    await this.operation(async (check) => {
      let item: Item;
      if ("releaseId" in saved) {
        item = localItem(
          await this.session.json(
            `/api/v1/catalog/resources/${saved.releaseId}`,
            { signal: this.abort.signal, responseBytes: 32 * 1024 ** 2 },
          ),
        );
        if (item.releaseId !== saved.releaseId)
          throw new ClientError("selection_changed");
      } else {
        item = searchItem(
          await this.session.json(indexedPath(saved), {
            signal: this.abort.signal,
          }),
        );
        if (
          item.origin !== "indexed" ||
          item.identifier !== saved.identifier ||
          item.versionPresent !== saved.versionPresent ||
          item.version !== saved.version
        )
          throw new ClientError("selection_changed");
      }
      check();
      this.state.page = {
        items: [item],
        summary: "Exact saved reference",
        partial: false,
      };
      this.pageRequest = undefined;
      this.continuation = undefined;
      this.state.canNext = false;
      delete this.state.facets;
      delete this.state.facetQuery;
    });
    if (revision !== this.revision) throw new ClientError("selection_changed");
    return this.select(this.state.page!.items[0]!.key);
  }
}
