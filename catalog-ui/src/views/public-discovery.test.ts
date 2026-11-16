// Public transport and consumed fields use only injected Fetch and in-memory bytes.
import { afterEach, expect, it, vi } from "vitest";
import {
  manifestCount,
  publicEntry,
  publicKind,
  publicPage,
  publicRead,
} from "./public-discovery";
const runtime = {
  basePath: "/nested",
  authMode: "development" as const,
  documentationUrl: "",
  publicDiscoveryEnabled: true,
};
const entry = {
  identifier: "urn:air:example.com:agent:item",
  displayName: "<script>literal</script>",
  type: "application/a2a-agent-card+json",
  url: "https://resource.invalid/never-fetch",
  version: "",
  metadata: { number: 9223372036854775807n.toString() },
};
afterEach(() => vi.unstubAllGlobals());

it("keeps missing/empty versions and untrusted locations as metadata", () => {
  expect(publicEntry(entry)).toMatchObject({
    name: entry.displayName,
    version: "",
    url: entry.url,
  });
  const { version: _omitted, ...missing } = entry;
  expect(publicEntry(missing)).not.toHaveProperty("version");
  expect(
    publicEntry({ identifier: "registry:owned", extra: [1, null, false] }),
  ).toMatchObject({ name: "registry:owned", type: "", tags: [] });
});
it.each([
  null,
  [],
  {},
  { ...entry, identifier: 1 },
  { ...entry, version: null },
  { ...entry, tags: [null] },
  { ...entry, tags: Array(65).fill("tag") },
  { ...entry, capabilities: "tool" },
  { ...entry, representativeQueries: ["\ud800"] },
  { ...entry, description: "a".repeat(32769) },
])("refuses malformed consumed metadata %#", (value) => {
  expect(() => publicEntry(value)).toThrow("invalid_publication_response");
});
it.each([
  ["application/a2a-agent-card+json; charset=utf-8", "Agent"],
  ["APPLICATION/VND.MCP.SERVER+JSON", "MCP Server"],
  ["text/markdown", "Skill"],
  ["application/agent-plugins+gzip", "Plugin"],
  ["application/ai-registry+json", "Registry metadata"],
  ["future/unknown", "Other metadata"],
  ["", "Other metadata"],
])("labels declared %s without claiming another kind", (type, label) =>
  expect(publicKind(type)).toBe(label),
);
it("keeps List and Search branches and a nonterminal empty page distinct", () => {
  expect(
    publicPage({ items: [], total: 4, pageToken: "opaque+漢/=" }, false, 5),
  ).toEqual({ entries: [], total: 4, pageToken: "opaque+漢/=" });
  expect(
    publicPage({ results: [entry], referrals: [] }, true, 5).entries,
  ).toHaveLength(1);
  expect(publicPage({ items: [] }, false, 5)).not.toHaveProperty("pageToken");
  expect(
    manifestCount({
      entries: [entry, { type: "application/ai-registry+json" }],
    }),
  ).toBe(2);
});
it.each([
  null,
  {},
  { items: [entry, entry] },
  { items: [], pageToken: null },
  { items: [], pageToken: "\0" },
  { items: [entry], total: 0 },
  { items: [], total: 0.5 },
  { items: [], referrals: [{}] },
  { results: [] },
])("refuses invalid page or unexpected referrals %#", (value) =>
  expect(() => publicPage(value, false, 1)).toThrow(),
);
it.each([null, {}, { entries: null }, { entries: Array(100002) }])(
  "refuses invalid manifest %#",
  (value) => expect(() => manifestCount(value)).toThrow(),
);
it("constructs only fixed credential-free operations and preserves opaque tokens", async () => {
  const fetch = vi
    .fn()
    .mockImplementation(async () => Response.json({ items: [] }));
  vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal,
    selection = { text: "hello", pageSize: 5 };
  await publicRead(runtime, "manifest", selection, "", signal);
  await publicRead(runtime, "agents", selection, "opaque+/=漢", signal);
  await publicRead(runtime, "search", selection, "opaque+/=漢", signal);
  expect(fetch.mock.calls.map((call: unknown[]) => call[0])).toEqual([
    "/nested/_discovery/manifest",
    "/nested/_discovery/agents?pageSize=5&pageToken=opaque%2B%2F%3D%E6%BC%A2",
    "/nested/_discovery/search",
  ]);
  expect(fetch).toHaveBeenLastCalledWith(
    "/nested/_discovery/search",
    expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        text: "hello",
        pageSize: 5,
        pageToken: "opaque+/=漢",
      }),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
      headers: {
        Accept: "application/json",
        "X-Catalog-Request": "1",
        "Content-Type": "application/json",
      },
    }),
  );
});
it("rejects disabled, unsafe origins and invalid selection before Fetch", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  for (const config of [
    { ...runtime, publicDiscoveryEnabled: false },
    { ...runtime, basePath: "//other.invalid" },
    { ...runtime, basePath: "/docs" },
  ])
    await expect(
      publicRead(config, "agents", { text: "", pageSize: 20 }, "", signal),
    ).rejects.toThrow("invalid_publication_request");
  for (const selection of [
    { text: "", pageSize: 0 },
    { text: "a".repeat(501), pageSize: 20 },
    { text: " ", pageSize: 20 },
  ])
    await expect(
      publicRead(runtime, "search", selection, "", signal),
    ).rejects.toThrow("invalid_publication_request");
  expect(fetch).not.toHaveBeenCalled();
});
it.each([
  [400, "continuation_or_selection_refused"],
  [401, "publication_unavailable"],
  [403, "publication_unavailable"],
  [404, "publication_unavailable"],
  [503, "publication_unavailable"],
  [429, "publication_read_failed"],
])(
  "sanitizes HTTP %i without retry or body disclosure",
  async (status, code) => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response("private failure sentinel", { status: Number(status) }),
      );
    vi.stubGlobal("fetch", fetch);
    await expect(
      publicRead(
        runtime,
        "agents",
        { text: "", pageSize: 20 },
        "",
        new AbortController().signal,
      ),
    ).rejects.toThrow(String(code));
    expect(fetch).toHaveBeenCalledOnce();
  },
);
it.each([
  new Response("not JSON", { headers: { "content-type": "application/json" } }),
  new Response("{}", { headers: { "content-type": "text/html" } }),
  new Response(new Uint8Array([0xff]), {
    headers: { "content-type": "application/json" },
  }),
])("refuses unusable successful bodies safely %#", async (response) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  await expect(
    publicRead(
      runtime,
      "manifest",
      { text: "", pageSize: 20 },
      "",
      new AbortController().signal,
    ),
  ).rejects.toThrow();
});
it("bounds unknown-length bytes and cancels the stream", async () => {
  const cancel = vi.fn();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new Uint8Array(16 * 1024 * 1024 + 1));
    },
    cancel,
  });
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(stream, {
        headers: { "content-type": "application/json" },
      }),
    ),
  );
  await expect(
    publicRead(
      runtime,
      "manifest",
      { text: "", pageSize: 20 },
      "",
      new AbortController().signal,
    ),
  ).rejects.toThrow("invalid_publication_response");
  expect(cancel).toHaveBeenCalledOnce();
});
it("contains a stream read failure even when cancelling that failed stream also rejects", async () => {
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      controller.error(new Error("private stream failure"));
    },
  });
  const fetch = vi
    .fn()
    .mockResolvedValue(
      new Response(stream, { headers: { "content-type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetch);
  await expect(
    publicRead(
      runtime,
      "manifest",
      { text: "", pageSize: 20 },
      "",
      new AbortController().signal,
    ),
  ).rejects.toThrow("publication_read_failed");
  expect(fetch).toHaveBeenCalledOnce();
  expect(stream.locked).toBe(false);
});

it("retains reported Search source as inert metadata and rejects malformed source values", () => {
  const entry = {
    identifier: "urn:air:example.com:agent:source",
    source: "https://publication.invalid/ard/v1/",
  };
  expect(publicEntry(entry).source).toBe(entry.source);
  expect(() =>
    publicEntry({ ...entry, source: { url: "https://other.invalid" } }),
  ).toThrow("invalid_publication_response");
});
it.each([
  ["text/html", false],
  ["", false],
  ["text/html", true],
] as const)(
  "cancels rejected media %s even if cancellation fails: %s",
  async (media, brokenCancel) => {
    const cancel = vi.fn(() => {
      if (brokenCancel) throw new Error("private cancellation detail");
    });
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("<html>untrusted body"));
      },
      cancel,
    });
    const fetch = vi.fn().mockResolvedValue(
      new Response(stream, {
        headers: media ? { "content-type": media } : {},
      }),
    );
    vi.stubGlobal("fetch", fetch);
    await expect(
      publicRead(
        runtime,
        "manifest",
        { text: "", pageSize: 20 },
        "",
        new AbortController().signal,
      ),
    ).rejects.toThrow("invalid_publication_response");
    expect(cancel).toHaveBeenCalledOnce();
    expect(stream.locked).toBe(false);
    expect(fetch).toHaveBeenCalledOnce();
  },
);
