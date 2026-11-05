/// <reference types="node" />
import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { mcpRemoteChoices, projectMCPConfiguration } from "./mcp-client-export";
import type { MCPClientFormat } from "./mcp-client-export";
import type { Representation } from "./handoff";
import { parseNativeJSONObject, parseJSONObject } from "./json-input";

type Fixture = {
  name: string;
  profile: string;
  mediaType: string;
  releaseId: string;
  document: unknown;
  expected: Record<MCPClientFormat, unknown>;
  omittedHeaders: { name: string; required: boolean }[];
};
const fixtures = JSON.parse(
  readFileSync(
    new URL(
      "../../../cmd/catalogctl/testdata/mcp-client-export/cases.json",
      import.meta.url,
    ),
    "utf8",
  ),
) as Fixture[];
const rep = (fixture: Fixture): Representation => ({
  id: "representation",
  role: "primary",
  profileId: fixture.profile,
  mediaType: fixture.mediaType,
  contentDigest: `sha256:${"a".repeat(64)}`,
  size: 100,
  validationState: "valid",
});

for (const fixture of fixtures)
  for (const format of Object.keys(fixture.expected) as MCPClientFormat[]) {
    it(`matches the CLI golden: ${fixture.name}/${format}`, () => {
      const result = projectMCPConfiguration(
        fixture.document,
        rep(fixture),
        fixture.releaseId,
        format,
        0,
      );
      expect(JSON.parse(result.text)).toEqual(fixture.expected[format]);
      expect(result.position).toBe(1);
      expect(result.omittedHeaders).toEqual(fixture.omittedHeaders);
      expect(JSON.stringify(result)).not.toContain("PRIVATE_");
    });
  }
const fixture = fixtures[0]!;
const representation = rep(fixture);
const uri = "https://mcp.test/mcp?route=a%2Fb&tag=one+two";
const data = (url: unknown = uri, headers?: unknown) => ({
  remotes: [
    {
      type: "streamable-http",
      url,
      ...(headers !== undefined ? { headers } : {}),
    },
  ],
});

it("uses original remote positions and rejects ambiguous, invalid or unsupported selections", () => {
  const native = {
    remotes: [
      { type: "stdio", url: uri },
      { type: "sse", url: uri },
    ],
  };
  expect(
    mcpRemoteChoices(native, representation).map((choice) => [
      choice.position,
      !!choice.reason,
    ]),
  ).toEqual([
    [1, true],
    [2, false],
  ]);
  for (const selected of [0, -1, 3])
    expect(() =>
      projectMCPConfiguration(
        native,
        representation,
        fixture.releaseId,
        "mcp-vscode",
        selected,
      ),
    ).toThrow(/Choose a remote/);
  expect(() =>
    projectMCPConfiguration(
      native,
      representation,
      fixture.releaseId,
      "mcp-vscode",
      1,
    ),
  ).toThrow(/Unsupported transport/);
  expect(
    JSON.parse(
      projectMCPConfiguration(
        native,
        representation,
        fixture.releaseId,
        "mcp-vscode",
        2,
      ).text,
    ),
  ).toEqual({
    servers: { [`catalog-${fixture.releaseId}`]: { type: "sse", url: uri } },
  });
});

it.each([
  "http://mcp.test",
  "https:host",
  "https:///path",
  "https://user:secret@mcp.test",
  "https://@mcp.test",
  "https://mcp.test/#",
  "https://mcp.test/\\bad",
  "https://mcp.test/has space",
  "https://mcp.test/\n",
  "https://mcp.test/${command:run}",
  "https://mcp.test/{id}",
  "https://mcp.test/${TOKEN:-default}",
  "https://mcp.test/?x=%zz",
  "https://mcp.test:0",
  "https://mcp.test:/",
  "https://mcp.test/\ud800",
  7,
  null,
])("refuses unsafe endpoint %s without rewriting it", (url) => {
  expect(() =>
    projectMCPConfiguration(
      data(url),
      representation,
      fixture.releaseId,
      "mcp-vscode",
      1,
    ),
  ).toThrow(/literal HTTPS/);
});

it.each([
  null,
  {},
  [null],
  [{ name: "X\nBad" }],
  [{ name: "X" }, { name: "x" }],
  [{ name: "X", isRequired: null }],
  [{ name: "X", isRequired: "true" }],
])("refuses malformed header descriptors %j", (headers) => {
  expect(() =>
    projectMCPConfiguration(
      data(uri, headers),
      representation,
      fixture.releaseId,
      "mcp-cursor",
      1,
    ),
  ).toThrow(/Invalid header descriptors/);
});

it("requires the selected native profile and Card schema, without confusing Registry and Card", () => {
  for (const changed of [
    { ...representation, profileId: "private" },
    { ...representation, mediaType: "application/json" },
    { ...representation, validationState: "invalid" as const },
  ])
    expect(() => mcpRemoteChoices(data(), changed)).toThrow(
      /No supported remote/,
    );
  expect(() => mcpRemoteChoices(data(), rep(fixtures[2]!))).toThrow(
    /No supported remote/,
  );
  expect(() => mcpRemoteChoices({ remotes: [] }, representation)).toThrow(
    /No supported remote/,
  );
  expect(() =>
    projectMCPConfiguration(
      data(),
      representation,
      "not-a-release",
      "mcp-vscode",
      1,
    ),
  ).toThrow(/identity/);
});

it("rejects duplicate keys and invalid Unicode but ignores unused numeric precision only for native projection", () => {
  for (const raw of [
    '{"remotes":[],"remotes":[]}',
    '{"remotes":[],"extra":"\\ud800"}',
    "{} {}",
    "[]",
  ])
    expect(() => parseNativeJSONObject(raw, 8192)).toThrow();
  const raw =
    '{"extra":9007199254740993,"remotes":[{"type":"sse","url":"https://mcp.test/ok"}]}';
  expect(() => parseJSONObject(raw)).toThrow();
  const parsed = parseNativeJSONObject(raw, 8192);
  expect(
    projectMCPConfiguration(
      parsed,
      representation,
      fixture.releaseId,
      "mcp-cursor",
      1,
    ).text,
  ).not.toContain("extra");
  expect(() => parseNativeJSONObject(raw, 1)).toThrow();
});

it("accepts valid Unicode at the native depth boundary and rejects deeper unused metadata", () => {
  const url = "https://mcp.test/servers/\u{1f680}";
  const nested = (depth: number) => {
    let extra: unknown = 1;
    for (let index = 0; index < depth; index++) extra = { child: extra };
    return JSON.stringify({ ...data(url), extra });
  };
  const parsed = parseNativeJSONObject(nested(63), 8192);
  expect(
    JSON.parse(
      projectMCPConfiguration(
        parsed,
        representation,
        fixture.releaseId,
        "mcp-cursor",
        1,
      ).text,
    ),
  ).toEqual({ mcpServers: { [`catalog-${fixture.releaseId}`]: { url } } });
  expect(() => parseNativeJSONObject(nested(64), 8192)).toThrow(
    /nesting exceeds 64 levels/i,
  );
});
