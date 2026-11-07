import { expect, it } from "vitest";
import {
  agentFieldError,
  agentVersion,
  newAgentCard,
  patchAgentCard,
  readAgentCard,
  signedAgentCard,
  type AgentCard,
} from "./agent-card";

const imported: AgentCard = {
  name: "Order Agent",
  description: "Explain an order",
  version: "release 2026 Q4",
  capabilities: {
    streaming: true,
    extensions: [
      { uri: "https://example.test/extension", params: { ratio: 0.25 } },
    ],
  },
  supportedInterfaces: [
    {
      url: "https://example.test/grpc",
      protocolBinding: "GRPC",
      protocolVersion: "1.0",
      tenant: "kept",
    },
    {
      url: "https://example.test/json",
      protocolBinding: "JSONRPC",
      protocolVersion: "1.0",
    },
  ],
  defaultInputModes: ["application/json", "text/plain; charset=utf-8"],
  defaultOutputModes: ["text/plain"],
  provider: { organization: "Example", url: "https://example.test" },
  documentationUrl: "https://example.test/docs",
  iconUrl: "https://example.test/icon.png",
  securitySchemes: { bearer: { httpAuthSecurityScheme: { scheme: "Bearer" } } },
  securityRequirements: [{ schemes: { bearer: { list: [] } } }],
  skills: [
    {
      id: "explain",
      name: "Explain",
      description: "Explain the order",
      tags: ["orders, status", "注文"],
      examples: ["Explain order 42"],
      inputModes: ["application/json"],
      securityRequirements: [],
    },
  ],
};

it("starts only new Cards with an unsigned JSON-RPC interface, Text modes and no advertised capabilities", () => {
  expect(newAgentCard("\u0085 release 2026 Q4 \u0085")).toEqual({
    name: "",
    description: "",
    version: "release 2026 Q4",
    capabilities: {},
    supportedInterfaces: [
      { url: "", protocolBinding: "JSONRPC", protocolVersion: "1.0" },
    ],
    defaultInputModes: ["text/plain"],
    defaultOutputModes: ["text/plain"],
    skills: [{ id: "", name: "", description: "", tags: [""] }],
  });
  expect(agentVersion("\ufeffv\ufeff")).toBe("\ufeffv\ufeff");
  expect(readAgentCard("", "nightly").card).toEqual(newAgentCard("nightly"));
});

it("preserves long interior whitespace in a native version before field validation", () => {
  const version = "release" + " ".repeat(40_000) + "candidate🙂";
  expect(agentVersion(`\u0085 ${version} \u0085`)).toBe(version);
  expect(readAgentCard("", `\u0085${version}\u0085`).card?.version).toBe(
    version,
  );
});

it("keeps advanced values and ordered interfaces while patching only an explicitly edited field", () => {
  const original = JSON.stringify(imported, null, 4);
  const read = readAgentCard(original, "do not overwrite");
  expect(read).toEqual({ card: imported });
  const result = JSON.parse(
    patchAgentCard(read.card!, { name: "Changed name" }),
  ) as AgentCard;
  expect(result).toEqual({ ...imported, name: "Changed name" });
  expect(result.supportedInterfaces![0]!.tenant).toBe("kept");
  expect(result.skills![0]!.tags).toEqual(["orders, status", "注文"]);
  expect(JSON.stringify(imported, null, 4)).toBe(original);
  expect(result).not.toHaveProperty("$schema");
  expect(result).not.toHaveProperty("url");
});

it.each([
  { name: 5 },
  { description: null },
  { version: false },
  { capabilities: null },
  { capabilities: { streaming: "true" } },
  { capabilities: { push_notifications: true } },
  { supportedInterfaces: "url" },
  { supportedInterfaces: [null] },
  { supportedInterfaces: [{ url: 3 }] },
  { supportedInterfaces: [{ protocol_binding: "JSONRPC" }] },
  { defaultInputModes: "text/plain" },
  { defaultOutputModes: [3] },
  { skills: false },
  { skills: [null] },
  { skills: [{ name: 1 }] },
  { skills: [{ tags: [1] }] },
  { skills: [{ input_modes: [] }] },
  { supported_interfaces: [] },
  { security_requirements: [] },
])("leaves an ambiguous imported shape in Advanced: %j", (patch) => {
  const content = JSON.stringify({ ...imported, ...patch });
  const read = readAgentCard(content, "new version");
  expect(read.card).toBeUndefined();
  expect(read.reason).toContain("cannot be safely edited");
});

it("does not apply new-item defaults to missing imported fields or empty ordered collections", () => {
  const partial = { capabilities: {}, supportedInterfaces: [], skills: [{}] };
  expect(readAgentCard(JSON.stringify(partial), "new")).toEqual({
    card: partial,
  });
  const absent = { capabilities: {}, supportedInterfaces: [{}] };
  expect(readAgentCard(JSON.stringify(absent), "new")).toEqual({
    card: absent,
  });
  expect(readAgentCard("{}", "new")).toHaveProperty("reason");
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
  "preserves malformed or numerically lossy Advanced input",
  (content, reason) => {
    const read = readAgentCard(content, "v");
    expect(read.card).toBeUndefined();
    expect(read.reason).toContain(reason);
  },
);

it.each([
  [undefined, false],
  [[], false],
  [[{ protected: "external", signature: "external" }], true],
  [null, true],
  [{ unexpected: true }, true],
] as const)(
  "requires explicit unsigned replacement for signature evidence %j",
  (signatures, signed) => {
    const card =
      signatures === undefined ? imported : { ...imported, signatures };
    expect(signedAgentCard(card)).toBe(signed);
    if (signed)
      expect(() => patchAgentCard(card, { name: "changed" })).toThrow(
        /unsigned copy/,
      );
    else
      expect(
        (JSON.parse(patchAgentCard(card, { name: "changed" })) as AgentCard)
          .name,
      ).toBe("changed");
  },
);

it.each(["", " \u0085 ", "bad\0text"])(
  "explains missing text and NUL without accepting them",
  (value) => {
    expect(agentFieldError(value)).not.toBe("");
  },
);
it.each([
  "http://example.test",
  "https://user:pass@example.test",
  "https://example.test/#fragment",
  "https://example.test?access_token=secret",
  "https://example.test:0",
  "https://éxample.test",
  "https:\\example.test",
  "not a URL",
])("rejects an unsafe advertised endpoint without fetching it: %s", (url) =>
  expect(agentFieldError(url, "url")).toContain("absolute HTTPS"),
);
it.each([
  "https://example.test",
  "https://example.test:9443/a2a?tenant=42",
  "HTTPS://example.test/注文",
  "https://[::1]:443/grpc",
])("accepts safe HTTPS metadata syntax: %s", (url) =>
  expect(agentFieldError(url, "url")).toBe(""),
);
it.each(["1.0", "0.3", "12.34"])(
  "keeps protocol version independent: %s",
  (value) => expect(agentFieldError(value, "protocol")).toBe(""),
);
it.each(["1.0.0", "v1.0", "01.0", "1", "1.0\n"])(
  "explains invalid protocol versions: %s",
  (value) =>
    expect(agentFieldError(value, "protocol")).toContain("major.minor"),
);
it.each([
  "text/plain",
  "application/json",
  "image/png",
  'text/plain; charset="utf-8"',
])("accepts media syntax: %s", (value) =>
  expect(agentFieldError(value, "media")).toBe(""),
);
it.each(["text", "text plain", "text//plain"])(
  "explains invalid media syntax: %s",
  (value) => expect(agentFieldError(value, "media")).toContain("media type"),
);
