// Reuse only stages exact supported inputs through an injected authenticated transport.
import { expect, it } from "vitest";
import { reusableJSON, reuseDraft } from "./draft-reuse";
import { workspace } from "../../tests/unit/fixtures";

const base = {
  role: "primary" as const,
  mediaType: "application/json",
  profileId: "",
  contentDigest: "sha256:retained",
};
const signal = () => new AbortController().signal;

/** Keep a complete rooted inventory so only the selected document field can fail. */
function retainedPlugin() {
  const manifest = '{ "name":"helper", "version":"1.0.0" }\n';
  const skill = "---\nname: helper\ndescription: Help\n---\nExact Skill text\n";
  const digest = "sha256:" + "a".repeat(64);
  const documents = [
    { path: "bundle/plugin.json", content: manifest },
    { path: "bundle/skills/helper/SKILL.md", content: skill },
  ];
  const files = [
    ...documents.map((doc) => ({
      path: doc.path,
      size: new TextEncoder().encode(doc.content).byteLength,
      digest,
      executable: false,
    })),
    {
      path: "bundle/scripts/helper.py",
      size: 10,
      digest,
      executable: true,
    },
  ];
  return {
    representations: [
      {
        ...base,
        mediaType: "application/zip",
        url: "https://metadata.example/plugin.zip",
        contentDigest: digest,
        plugin: {
          rootDirectory: "bundle",
          expandedSize: files.reduce((size, file) => size + file.size, 0),
          files,
          documents,
          assessments: {
            malware: "not_assessed" as const,
            secrets: "not_assessed" as const,
            license: "not_assessed" as const,
            sbom: "not_assessed" as const,
            provenance: "not_assessed" as const,
          },
        },
      },
    ],
  };
}

async function artifact(text: string | Uint8Array) {
  const bytes =
    typeof text === "string" ? new TextEncoder().encode(text) : text;
  const hash = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new Uint8Array(bytes)),
  );
  const id = Array.from(hash, (b) => b.toString(16).padStart(2, "0")).join("");
  return {
    id,
    reply: {
      data: bytes.buffer,
      response: new Response(null, {
        headers: {
          "Content-Type": "application/json",
          "Content-Digest": `sha-256=:${btoa(String.fromCharCode(...hash))}:`,
        },
      }),
    },
  };
}
it("retains representable decimal values and rejects lossy or ambiguous JSON", () => {
  for (const text of [
    "{}",
    '{"n":1.25,"x":1e-3,"name":"12.123456789123456789"}',
    '{"n":-0}',
    '{"n":1.0}',
  ])
    expect(reusableJSON(text)).toBeTruthy();
  for (const text of [
    '{"n":9007199254740993}',
    '{"n":0.123456789012345678901}',
    '{"n":1e-9999}',
    '{"a":1,"a":2}',
    "[]",
    "{",
  ])
    expect(() => reusableJSON(text)).toThrow();
});
it("stages every URL and safe inline representation without fetching destinations", async () => {
  const { props, get } = workspace();
  const drafts = await reuseDraft(
    props.client,
    "agent",
    {
      representations: [
        { ...base, url: "https://metadata.example/card.json" },
        {
          ...base,
          role: "metadata",
          data: { name: "Example", enabled: true, nested: [null, "text"] },
        },
      ],
    },
    signal(),
  );
  expect(drafts.map((d) => [d.role, d.mode])).toEqual([
    ["primary", "url"],
    ["metadata", "data"],
  ]);
  expect(JSON.parse(drafts[1]!.content)).toEqual({
    name: "Example",
    enabled: true,
    nested: [null, "text"],
  });
  expect(drafts[0]!.url).toBe("https://metadata.example/card.json");
  expect(get).not.toHaveBeenCalled();
  const plugin = await reuseDraft(
    props.client,
    "plugin",
    {
      representations: [
        {
          ...base,
          url: "https://metadata.example/plugin.zip",
          plugin: { rootDirectory: "", files: [], documents: [] } as never,
        },
      ],
    },
    signal(),
  );
  expect(plugin[0]!.expectedDigest).toBe(base.contentDigest);
});
it("refuses incomplete packages, unsupported inline numeric fidelity and missing documents atomically", async () => {
  const { props, get } = workspace();
  for (const value of [
    { ...base, package: {} },
    { ...base, plugin: {} as never },
    { ...base },
    { ...base, data: { n: 0.5 } },
    { ...base, data: { deep: [1] } },
  ]) {
    await expect(
      reuseDraft(
        props.client,
        "skill",
        { representations: [{ ...base, data: { name: "Valid" } }, value] },
        signal(),
      ),
    ).rejects.toThrow();
  }
  let deep: unknown = "leaf";
  for (let i = 0; i < 66; i++) deep = { nested: deep };
  await expect(
    reuseDraft(
      props.client,
      "agent",
      { representations: [{ ...base, data: { nested: deep } }] },
      signal(),
    ),
  ).rejects.toThrow("depth");
  expect(get).not.toHaveBeenCalled();
});
it.each([
  [
    "application/json",
    '{ "n": 1.25, "name": "Literal <script> text" }',
    "data",
  ],
  ["text/markdown", "# Exact\r\ntext\n", "content"],
])(
  "verifies authenticated retained %s bytes before staging",
  async (mediaType, text, mode) => {
    const { props, get } = workspace();
    const stored = await artifact(text!);
    get.mockResolvedValue(stored.reply);
    const drafts = await reuseDraft(
      props.client,
      "agent",
      {
        representations: [
          { ...base, mediaType: mediaType!, artifactId: stored.id },
        ],
      },
      signal(),
    );
    expect(drafts[0]).toMatchObject({ mode, content: text, mediaType });
    expect(get).toHaveBeenCalledWith(
      "/api/v1/artifacts/{artifactId}/content",
      expect.objectContaining({
        params: { path: { artifactId: stored.id } },
        parseAs: "arrayBuffer",
      }),
    );
  },
);
it("rejects damaged bytes, invalid encoding, oversized text, NUL and cancelled reads", async () => {
  const { props, get } = workspace();
  for (const text of [
    new Uint8Array([255]),
    "a\0b",
    "x".repeat(2 * 1024 * 1024 + 1),
    '{"n":0.123456789012345678901}',
  ]) {
    const stored = await artifact(text);
    get.mockResolvedValue(stored.reply);
    await expect(
      reuseDraft(
        props.client,
        "agent",
        { representations: [{ ...base, artifactId: stored.id }] },
        signal(),
      ),
    ).rejects.toThrow();
  }
  const stored = await artifact("{}");
  get.mockResolvedValue(stored.reply);
  await expect(
    reuseDraft(
      props.client,
      "agent",
      { representations: [{ ...base, artifactId: "wrong" }] },
      signal(),
    ),
  ).rejects.toThrow("checksum");
  const controller = new AbortController();
  controller.abort();
  await expect(
    reuseDraft(
      props.client,
      "agent",
      { representations: [{ ...base, artifactId: stored.id }] },
      controller.signal,
    ),
  ).rejects.toThrow("cancelled");
});

it("refuses empty revisions instead of erasing the required representation", async () => {
  const { props } = workspace();
  await expect(
    reuseDraft(props.client, "agent", { representations: [] }, signal()),
  ).rejects.toThrow("no reusable");
});

it("rejects an over-limit source before reading any documents", async () => {
  const { props, get } = workspace();
  await expect(
    reuseDraft(
      props.client,
      "agent",
      {
        representations: Array.from({ length: 17 }, () => ({
          ...base,
          url: "https://example.test/card",
        })),
      },
      signal(),
    ),
  ).rejects.toThrow("sixteen");
  expect(get).not.toHaveBeenCalled();
});

it("stages exact rooted Plugin documents without an optional MCP document or archive fetch", async () => {
  const { props, get } = workspace();
  const input = retainedPlugin();
  const before = structuredClone(input);
  const [draft] = await reuseDraft(props.client, "plugin", input, signal());
  expect(draft).toMatchObject({
    role: "primary",
    mode: "url",
    url: "https://metadata.example/plugin.zip",
    expectedDigest: "sha256:" + "a".repeat(64),
    pluginPreparation: {
      preparing: false,
      manifest: '{ "name":"helper", "version":"1.0.0" }\n',
      mcp: "",
      skills: [
        {
          content:
            "---\nname: helper\ndescription: Help\n---\nExact Skill text\n",
        },
      ],
      sourceFiles: [
        { path: "bundle/plugin.json", retained: true },
        { path: "bundle/skills/helper/SKILL.md", retained: true },
        { path: "bundle/scripts/helper.py", retained: false },
      ],
    },
  });
  expect(draft?.pluginPreparation?.skills).toHaveLength(1);
  expect(input).toEqual(before);
  expect(get).not.toHaveBeenCalled();
});

it.each(["document content", "document path", "inventory path"] as const)(
  "rejects incomplete retained Plugin %s without mutating input or making a read",
  async (field) => {
    const { props, get } = workspace();
    const input = retainedPlugin();
    const valid = structuredClone(input);
    const [control] = await reuseDraft(props.client, "plugin", input, signal());
    expect(control?.pluginPreparation?.manifest).toBe(
      '{ "name":"helper", "version":"1.0.0" }\n',
    );
    expect(input).toEqual(valid);
    const plugin = input.representations[0]!.plugin;
    if (field === "document content")
      Object.assign(plugin.documents[0]!, { content: undefined });
    else if (field === "document path")
      Object.assign(plugin.documents[0]!, { path: undefined });
    else Object.assign(plugin.files[0]!, { path: false });
    const damaged = structuredClone(input);
    await expect(
      reuseDraft(props.client, "plugin", input, signal()),
    ).rejects.toThrow(
      "The retained Plugin documents are incomplete. Supply the original archive.",
    );
    expect(input).toEqual(damaged);
    expect(get).not.toHaveBeenCalled();
  },
);
