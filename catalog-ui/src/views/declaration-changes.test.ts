import { expect, it } from "vitest";
import { declarationChanges } from "./declaration-changes";

const endpoint = {
  protocolBinding: "JSONRPC",
  protocolVersion: "1.0",
  url: "https://example.com",
  tenant: "sales",
};
const agent = {
  skills: [{ id: "one" }],
  defaultInputModes: ["text/plain"],
  defaultOutputModes: ["text/plain"],
  supportedInterfaces: [endpoint],
  capabilities: {},
};
const remote = {
  type: "sse",
  url: "{base}/stream",
  supportedProtocolVersions: ["v1", "v2"],
};
const compareAgent = (after: unknown, before: unknown = agent) =>
  declarationChanges("Agent", before, after);
const compareRemotes = (before: unknown[], after: unknown[]) =>
  declarationChanges("MCP", { remotes: before }, { remotes: after });

it("compares only selected Agent facts, with exact skill IDs and an explicit streaming declaration", () => {
  expect(
    compareAgent({
      ...agent,
      skills: [{ id: "One" }],
      capabilities: { streaming: false },
      defaultInputModes: ["image/png"],
    }),
  ).toEqual({
    changes: [
      { group: "Skills", field: 'skills[id="One"]', after: '"One"' },
      { group: "Skills", field: 'skills[id="one"]', before: '"one"' },
      {
        group: "Capabilities",
        field: "defaultInputModes",
        membership: true,
        before: '["text/plain"]',
        after: '["image/png"]',
      },
      {
        group: "Capabilities",
        field: "capabilities.streaming",
        after: "false",
      },
    ],
  });
});
it("matches ProtoJSON aliases, unset optional fields and literal mode sets", () => {
  const alias = {
    skills: [{ id: "one", input_modes: null, output_modes: [] }],
    default_input_modes: ["text/plain", "text/plain"],
    default_output_modes: ["text/plain"],
    supported_interfaces: [
      {
        url: endpoint.url,
        protocol_version: "1.0",
        protocol_binding: "JSONRPC",
        tenant: null,
      },
    ],
    capabilities: { streaming: null },
  };
  expect(
    compareAgent(alias, {
      ...agent,
      supportedInterfaces: [{ ...endpoint, tenant: "" }],
    }),
  ).toEqual({ changes: [] });
  expect(
    compareAgent({
      ...agent,
      skills: [
        { id: "one", inputModes: ["image/png"], outputModes: ["text/html"] },
      ],
    }),
  ).toEqual({
    changes: [
      {
        group: "Skills",
        field: 'skills[id="one"].inputModes',
        after: '["image/png"]',
      },
      {
        group: "Skills",
        field: 'skills[id="one"].outputModes',
        after: '["text/html"]',
      },
    ],
  });
});
it("reports preferred and ordered interfaces, including the opaque routing tenant", () => {
  const other = { ...endpoint, tenant: "other" };
  const compared = compareAgent(
    { ...agent, supportedInterfaces: [other, endpoint] },
    { ...agent, supportedInterfaces: [endpoint, other] },
  );
  expect(compared).toMatchObject({
    changes: [
      {
        field: "supportedInterfaces[0] (preferred)",
        before: JSON.stringify(endpoint),
        after: JSON.stringify(other),
      },
      {
        field: "supportedInterfaces[1]",
        before: JSON.stringify(other),
        after: JSON.stringify(endpoint),
      },
    ],
  });
});
it("compares unique remotes by key, including missing vs explicitly empty versions", () => {
  expect(
    compareRemotes(
      [remote],
      [{ ...remote, supportedProtocolVersions: ["v2", "v2"] }],
    ),
  ).toEqual({
    changes: [
      {
        group: "Remote connections",
        field: 'remotes ["sse","{base}/stream"].supportedProtocolVersions',
        membership: true,
        before: '["v1"]',
        after: "[]",
      },
    ],
  });
  expect(
    compareRemotes(
      [{ type: "sse", url: "a" }],
      [{ type: "sse", url: "a", supportedProtocolVersions: [] }],
    ),
  ).toMatchObject({ changes: [{ after: "[]" }] });
  expect(
    compareRemotes(
      [remote],
      [
        {
          ...remote,
          headers: [{ value: "secret" }],
          variables: { hidden: true },
          supportedProtocolVersions: ["v2", "v1", "v2"],
        },
      ],
    ),
  ).toEqual({ changes: [] });
});
it("preserves duplicate multiplicity and relative order without pairing different tuples", () => {
  const b = { ...remote, supportedProtocolVersions: ["v3"] };
  expect(compareRemotes([remote, remote, b], [remote, b])).toMatchObject({
    changes: [{ group: "Remote connections", before: "2", after: "1" }],
  });
  expect(compareRemotes([remote, b], [b, remote])).toMatchObject({
    changes: [{ field: "remotes (order of retained declarations)" }],
  });
  expect(compareRemotes([remote], [{ ...remote, url: "other" }])).toMatchObject(
    { changes: [{ before: "1" }, { after: "1" }] },
  );
});
it("refuses ambiguous IDs, alias collisions and inadmissible selected shapes", () => {
  for (const v of [
    { ...agent, skills: [{ id: "one" }, { id: "one" }] },
    { ...agent, default_input_modes: [] },
    { ...agent, capabilities: { streaming: 0 } },
    { ...agent, skills: [{ id: "one", inputModes: "text" }] },
  ])
    expect(compareAgent(v)).toHaveProperty("unavailable");
  for (const r of [
    { ...remote, supportedProtocolVersions: null },
    { ...remote, type: "other" },
    null,
  ])
    expect(compareRemotes([remote], [r])).toHaveProperty("unavailable");
});
it("refuses entire summaries above collection, count and rendering limits", () => {
  const skills = (n: number) =>
    Array.from({ length: n }, (_, i) => ({ id: String(i) }));
  expect(
    compareAgent(
      { ...agent, skills: skills(128) },
      { ...agent, skills: skills(128) },
    ),
  ).toEqual({ changes: [] });
  for (const v of [
    { ...agent, skills: skills(129) },
    { ...agent, skills: skills(65) },
    { ...agent, skills: [{ id: "x".repeat(16384) }] },
  ])
    expect(compareAgent(v)).toHaveProperty("unavailable");
});

it("separates absence from empty remotes and avoids deducing a reorder from a disappearing duplicate", () => {
  const other = { ...remote, url: "https://second" };
  expect(declarationChanges("MCP", {}, { remotes: [] })).toEqual({
    changes: [{ group: "Remote connections", field: "remotes", after: "[]" }],
  });
  expect(
    compareRemotes([remote, other, remote], [other, remote]),
  ).toMatchObject({ changes: [{ before: "2", after: "1" }] });
  expect(compareRemotes([remote, other], [other, remote])).toMatchObject({
    changes: [{ field: "remotes (order of retained declarations)" }],
  });
});
it("ignores opaque numbers and secrets but refuses excess nesting and ambiguous text", () => {
  const original = { remotes: [remote] };
  const raw = JSON.stringify({
    ...original,
    _meta: { counter: "MARKER" },
  }).replace('"MARKER"', "9007199254740993123456789");
  expect(
    declarationChanges("MCP", raw, { ...original, _meta: { counter: 100 } }),
  ).toEqual({ changes: [] });
  let deep: unknown = null;
  for (let depth = 0; depth < 70; depth++) deep = [deep];
  for (const card of [
    { ...original, _meta: deep },
    JSON.stringify({ ...original, _meta: deep }),
    '{"remotes":[],"remotes":[]}',
    [],
    { remotes: null },
  ])
    expect(declarationChanges("MCP", card, original)).toHaveProperty(
      "unavailable",
    );
});

it("applies the same nesting ceiling to inline and retained cards, including empty containers", () => {
  for (const leaf of [{}, [], { value: false }]) {
    let nested: unknown = leaf;
    // The card and the leaf each occupy one of the 64 container levels.
    for (let level = 2; level < 64; level++) nested = [nested];
    const atLimit = { _meta: nested };
    const overLimit = { _meta: [nested] };
    expect(declarationChanges("MCP", atLimit, JSON.stringify(atLimit))).toEqual(
      {
        changes: [],
      },
    );
    for (const input of [overLimit, JSON.stringify(overLimit)])
      expect(declarationChanges("MCP", input, {})).toHaveProperty(
        "unavailable",
      );
  }
});

it("uses the same structural work budget for retained text and decoded JSON", () => {
  // Each empty object contributes two delimiters and (after the first) a comma.
  const metadata = Array.from({ length: 87380 }, () => ({}));
  const card = { _meta: metadata };
  expect(declarationChanges("MCP", card, JSON.stringify(card))).toEqual({
    changes: [],
  });
  metadata.push({});
  for (const input of [card, JSON.stringify(card)])
    expect(declarationChanges("MCP", {}, input)).toHaveProperty("unavailable");
});

it("rejects malformed required Agent declarations and routing values instead of reporting an empty change", () => {
  for (const patch of [
    { skills: [{ id: "" }] },
    { skills: [] },
    { defaultInputModes: [] },
    { defaultOutputModes: [] },
    { supportedInterfaces: [] },
    { supportedInterfaces: [{ ...endpoint, tenant: false }] },
    { supportedInterfaces: [{ ...endpoint, protocolBinding: null }] },
  ])
    expect(compareAgent({ ...agent, ...patch })).toHaveProperty("unavailable");
});

it("identifies a forced reorder even when a surviving remote gains another occurrence", () => {
  const other = { ...remote, url: "https://other.example" };
  expect(
    compareRemotes([remote, other], [other, remote, remote]),
  ).toMatchObject({
    changes: [
      { before: "1", after: "2", occurrences: 1 },
      { field: "remotes (order of retained declarations)" },
    ],
  });
});
