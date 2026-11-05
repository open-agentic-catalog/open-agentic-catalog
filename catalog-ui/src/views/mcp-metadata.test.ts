// Pure metadata units use the pinned native contracts, without a service or network.
import { expect, it } from "vitest";
import {
  mcpEndpointError,
  mcpFieldError,
  mcpFormat,
  mcpFormats,
  mcpOptional,
  mcpVersion,
  newMCPDocument,
  readMCPDocument,
} from "./mcp-metadata";

const card = {
  $schema:
    "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
  name: "com.example/documentation",
  description: "Search documentation",
  version: "external",
  remotes: [
    {
      type: "sse",
      url: "{baseUrl}/sse",
      variables: {
        baseUrl: { description: "Server origin", isRequired: true },
      },
      headers: [
        {
          name: "Authorization",
          value: "Bearer {token}",
          variables: { token: { isSecret: true } },
        },
      ],
      supportedProtocolVersions: ["2025-11-25", "2026-07-28"],
    },
  ],
  repository: {
    source: "github",
    url: "https://example.test/source",
    subfolder: "servers/docs",
    id: "retained",
  },
  icons: [{ src: "https://example.test/icon.svg", theme: "dark" }],
  _meta: { "com.example/extra": { ratio: 0.25 } },
};
const registry = {
  ...card,
  $schema:
    "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
  packages: [
    {
      registryType: "custom",
      identifier: "docs",
      transport: { type: "stdio", extra: true },
      version: "2.3.4",
      registryBaseUrl: "https://registry.example.test",
      runtimeHint: "node",
      runtimeArguments: [{ type: "named", name: "--mode", value: "read only" }],
      packageArguments: [{ type: "positional", value: "comma, literal" }],
      environmentVariables: [
        {
          name: "API_KEY",
          isSecret: true,
          isRequired: true,
          description: "Client credential",
        },
      ],
    },
  ],
};

it.each(["card", "registry"] as const)(
  "generates only required %s fields with a separate resource version",
  (format) => {
    expect(newMCPDocument(format, "\u0085 release 2026 Q4 \u0085")).toEqual({
      $schema: mcpFormats[format].schema,
      name: "",
      description: "",
      version: "release 2026 Q4",
    });
    expect(mcpFormat(mcpFormats[format].media)).toBe(format);
    expect(
      readMCPDocument("", mcpFormats[format].media, "1.0.0").document,
    ).toEqual({
      $schema: mcpFormats[format].schema,
      name: "",
      description: "",
      version: "1.0.0",
    });
  },
);
it("retains Go version whitespace semantics without stripping a BOM", () => {
  expect(mcpVersion("\ufeffv\ufeff")).toBe("\ufeffv\ufeff");
  expect(
    mcpFormat("application/vnd.open-agentic-catalog.mcp-registration+json"),
  ).toBeUndefined();
  expect(readMCPDocument("{}", "private", "new").reason).toContain(
    "Advanced JSON",
  );
});
it.each([
  ["card", card],
  ["registry", registry],
] as const)(
  "recognizes %s imports without replacing advanced values, array order or versions",
  (format, value) => {
    expect(
      readMCPDocument(JSON.stringify(value), mcpFormats[format].media, "new"),
    ).toEqual({ document: value });
    expect(mcpOptional(value, "title", "Changed")).toEqual({
      ...value,
      title: "Changed",
    });
    expect(mcpOptional({ ...value, title: "Removed" }, "title", "")).toEqual(
      value,
    );
  },
);
it("allows Registry metadata without a schema or distribution, and preserves an unfamiliar schema URI", () => {
  for (const value of [
    {},
    { name: "com.example/docs", description: "Metadata", version: "v" },
    { $schema: "https://example.test/schema" },
  ])
    expect(
      readMCPDocument(JSON.stringify(value), mcpFormats.registry.media, "new"),
    ).toEqual({ document: value });
});
it.each([
  ["card", registry],
  ["card", {}],
  ["registry", card],
  ["registry", { ...registry, $schema: null }],
] as const)(
  "refuses an incompatible %s schema without converting it",
  (format, value) => {
    expect(
      readMCPDocument(JSON.stringify(value), mcpFormats[format].media, "new")
        .reason,
    ).toContain("schema does not match");
  },
);
it.each([
  { name: 4 },
  { title: false },
  { description: null },
  { version: [] },
  { remotes: null },
  { remotes: [null] },
  { remotes: [{ type: "stdio" }] },
  { remotes: [{ type: "other" }] },
  { remotes: [{ url: false }] },
  { remotes: [{ headers: "Authorization" }] },
  { remotes: [{ headers: [null] }] },
  { remotes: [{ headers: [{ name: 3 }] }] },
  { remotes: [{ headers: [{ isSecret: "yes" }] }] },
  { remotes: [{ supportedProtocolVersions: false }] },
  { remotes: [{ supportedProtocolVersions: [3] }] },
])("preserves an unsupported Card shape in Advanced: %j", (patch) => {
  const read = readMCPDocument(
    JSON.stringify({ ...card, ...patch }),
    mcpFormats.card.media,
    "new",
  );
  expect(read.document).toBeUndefined();
  expect(read.reason).toContain("cannot be safely edited");
});
it.each([
  { packages: null },
  { packages: [null] },
  { packages: [{ registryType: 3 }] },
  { packages: [{ identifier: false }] },
  { packages: [{ version: [] }] },
  { packages: [{ fileSha256: false }] },
  { packages: [{ transport: null }] },
  { packages: [{ transport: { type: "other" } }] },
  { packages: [{ environmentVariables: [null] }] },
  { packages: [{ environmentVariables: [{ isRequired: 1 }] }] },
])("keeps malformed Registry packages in Advanced: %j", (patch) => {
  expect(
    readMCPDocument(
      JSON.stringify({ ...registry, ...patch }),
      mcpFormats.registry.media,
      "new",
    ).reason,
  ).toContain("cannot be safely edited");
});
it("keeps unconsumed Card packages and Registry protocol extensions opaque", () => {
  const value = { ...card, packages: "opaque" };
  expect(
    readMCPDocument(JSON.stringify(value), mcpFormats.card.media, "new")
      .document,
  ).toEqual(value);
  const other = {
    ...registry,
    remotes: [{ supportedProtocolVersions: "opaque" }],
  };
  expect(
    readMCPDocument(JSON.stringify(other), mcpFormats.registry.media, "new")
      .document,
  ).toEqual(other);
});
it.each([
  ['{"name":', "valid JSON"],
  ['{"name":"first","name":"second"}', "Duplicate"],
  ['{"value":9007199254740992}', "Whole numbers"],
  ['{"value":0.123456789012345678901}', "cannot preserve"],
  ['{"value":1e-9999}', "cannot preserve"],
  ["[]", "JSON object"],
  [" ".repeat(2 * 1024 * 1024 + 1), "exceeds"],
])(
  "retains invalid or lossy input instead of populating fields",
  (content, reason) => {
    expect(
      readMCPDocument(content, mcpFormats.card.media, "new").reason,
    ).toContain(reason);
  },
);
it.each(["com.example/docs", "COM.Example/Docs_1-2.3", "com..example/server"])(
  "accepts the pinned name spelling: %s",
  (value) => expect(mcpFieldError(value, "name")).toBe(""),
);
it.each([
  "docs",
  "com.example/a/b",
  "com.example/docs\n",
  "com.example/é",
  "a/",
  " /a",
])("explains invalid names: %s", (value) =>
  expect(mcpFieldError(value, "name")).not.toBe(""),
);
it("counts description and title lengths as code points", () => {
  expect(mcpFieldError("🙂".repeat(100), "description")).toBe("");
  expect(mcpFieldError("🙂".repeat(101), "description")).toContain("100");
  expect(mcpFieldError("x".repeat(101), "title")).toContain("100");
  expect(mcpFieldError("a/" + "x".repeat(199), "name")).toContain("200");
});
it.each(["release 2026 Q4", "1.0.0", "2026-07-28", "v1.0.0-rc.1"])(
  "preserves nonempty specific versions: %s",
  (value) => expect(mcpVersion(value)).toBe(value),
);
it.each(["^1.2.3", "~1.2.3", ">=1.2.3", "1.x", "1.*"])(
  "explains version ranges: %s",
  (value) =>
    expect(mcpFieldError(value, "package-version")).toContain(
      "specific version",
    ),
);
it("checks optional package versions and exact hashes without imposing semantic versions", () => {
  expect(mcpFieldError("", "package-version", false)).toBe("");
  expect(mcpFieldError("latest", "package-version")).toContain(
    "specific version",
  );
  expect(mcpFieldError("v".repeat(256), "package-version")).toContain("255");
  expect(mcpFieldError("a".repeat(64), "checksum")).toBe("");
  expect(mcpFieldError("A".repeat(64), "checksum")).toContain("hexadecimal");
  expect(mcpFieldError("a".repeat(64) + "\n", "checksum")).toContain(
    "hexadecimal",
  );
  expect(mcpFieldError("\u0085", "text")).toBe("Enter a value.");
  expect(mcpFieldError("a\0b")).toContain("NUL");
});
it.each(["https://example.test/mcp", "https://example.test/mcp/{tenant}"])(
  "accepts safe literal remote metadata: %s",
  (value) => {
    expect(mcpEndpointError(value, "card")).toBe("");
    expect(mcpEndpointError(value, "registry")).toBe("");
  },
);
it.each(["{baseUrl}/mcp", "{url}"])(
  "allows Card templates but keeps Registry remote HTTPS admission: %s",
  (value) => {
    expect(mcpEndpointError(value, "card")).toBe("");
    expect(mcpEndpointError(value, "registry")).toContain("absolute HTTPS");
  },
);
it.each([
  "http://example.test/mcp",
  "https://user:pass@example.test",
  "https://example.test/#fragment",
  "https://example.test?token=x",
  "https://example.test:0",
  "https://éxample.test",
  "https:\\example.test",
  "https://example.test/a b",
  "https://example.test/mcp\n",
])("refuses unsafe or schema-invalid remote syntax: %s", (value) => {
  expect(mcpEndpointError(value, "card")).not.toBe("");
  expect(mcpEndpointError(value, "registry")).not.toBe("");
});
it("preserves local package URL templates without connecting or imposing remote HTTPS rules", () => {
  for (const value of [
    "http://localhost:8080/mcp",
    "{--port}/mcp",
    "https://example.test/mcp",
  ]) {
    // A template variable must start with a letter or underscore under this pinned schema.
    expect(mcpEndpointError(value, "package")).toBe(
      value.startsWith("{--")
        ? "Use an HTTP(S) URL or a declared URL template."
        : "",
    );
  }
  expect(mcpEndpointError("{baseUrl}/mcp", "package")).toBe("");
  expect(mcpEndpointError("{bad space}", "card")).toContain("URL template");
  expect(mcpEndpointError("", "card")).toContain("Enter an endpoint");
});
