import { expect, it } from "vitest";
import {
  buildAIRIdentifier,
  canonicalResourceIdentifier,
  suggestResourceName,
} from "./resource-identity";

it("normalizes only outer Go whitespace, AIR scheme and publisher domain", () => {
  expect(
    canonicalResourceIdentifier(
      "\u0085 URN:AIR:Example.COM.:Team_09:Agent-1.2 \n",
    ),
  ).toBe("urn:air:example.com:Team_09:Agent-1.2");
  expect(canonicalResourceIdentifier("\tPrivate/Équipe 🙂 \n")).toBe(
    "Private/Équipe 🙂",
  );
  expect(canonicalResourceIdentifier("\ufeffOpaque\ufeff")).toBe(
    "\ufeffOpaque\ufeff",
  );
  expect(canonicalResourceIdentifier("URN:AIR")).toBe("URN:AIR");
  expect(canonicalResourceIdentifier("🙂".repeat(512))).toBe("🙂".repeat(512));
});

it.each([
  "",
  " \t\n",
  "a\0b",
  "x".repeat(513),
  "🙂".repeat(513),
  "bad\ud800",
  "bad\udc00",
])("rejects an invalid opaque creation input %j", (value) => {
  expect(() => canonicalResourceIdentifier(value)).toThrow();
});

it("builds terminal-only and namespaced AIR identifiers with preserved segment case", () => {
  expect(
    buildAIRIdentifier({
      publisher: "Example.COM.",
      namespace: "",
      name: "Agent_1",
    }),
  ).toBe("urn:air:example.com:Agent_1");
  expect(
    buildAIRIdentifier({
      publisher: "example.com",
      namespace: "Team:agents",
      name: "Order-1.2",
    }),
  ).toBe("urn:air:example.com:Team:agents:Order-1.2");
  expect(
    buildAIRIdentifier({
      publisher: "a0-z9.example",
      namespace: "",
      name: "aAzZ09-_.",
    }),
  ).toBe("urn:air:a0-z9.example:aAzZ09-_.");
});

it.each([
  "",
  "example",
  "127.0.0.1",
  "255.255.255.255",
  "0.0.0.0",
  "https://example.com",
  "example.com:443",
  "example..com",
  ".example.com",
  "-example.com",
  "example-.com",
  "exam_ple.com",
  "éxample.com",
  "example.com..",
  `${"a".repeat(64)}.com`,
  `${"a".repeat(63)}.${"a".repeat(63)}.${"a".repeat(63)}.${"b".repeat(62)}`,
])("rejects an invalid publisher component %j", (publisher) => {
  expect(() =>
    buildAIRIdentifier({ publisher, namespace: "", name: "agent" }),
  ).toThrow(/publisher domain/);
});

it.each(["999.0.0.1", "127.0.0.01", `${"a".repeat(63)}.com`])(
  "preserves numeric DNS names and label boundaries accepted by the backend: %s",
  (publisher) => {
    expect(
      buildAIRIdentifier({ publisher, namespace: "", name: "agent" }),
    ).toBe(`urn:air:${publisher}:agent`);
  },
);

it.each([
  "",
  ".",
  "..",
  "agent name",
  "agent/one",
  "agent:one",
  "agént",
  "x".repeat(65),
])("rejects an invalid terminal-name component %j", (name) => {
  expect(() =>
    buildAIRIdentifier({ publisher: "example.com", namespace: "", name }),
  ).toThrow(/namespace segments and resource name/);
});

it.each([
  ":Team",
  "Team:",
  "Team::agents",
  "Team:..",
  "Team/agents",
  "Team:agéñts",
])("rejects an invalid namespace component %j", (namespace) => {
  expect(() =>
    buildAIRIdentifier({ publisher: "example.com", namespace, name: "agent" }),
  ).toThrow(/namespace segments and resource name/);
});

it("enforces the AIR byte, publisher and segment boundaries independently", () => {
  const namespace = Array.from({ length: 7 }, () => "n".repeat(64)).join(":");
  const atLimit = { publisher: "example.com", namespace, name: "x".repeat(37) };
  const expected =
    "urn:air:example.com:" + ("n".repeat(64) + ":").repeat(7) + "x".repeat(37);
  expect(expected.length).toBe(512);
  expect(buildAIRIdentifier(atLimit)).toBe(expected);
  expect(() =>
    buildAIRIdentifier({ ...atLimit, name: "x".repeat(38) }),
  ).toThrow();
  const publisher = `${("a".repeat(63) + ".").repeat(3)}${"b".repeat(61)}`;
  expect(publisher.length).toBe(253);
  expect(
    buildAIRIdentifier({ publisher, namespace: "", name: "x".repeat(64) }),
  ).toBe(`urn:air:${publisher}:${"x".repeat(64)}`);
  expect(() =>
    canonicalResourceIdentifier(`urn:air:example.com:${"é".repeat(250)}`),
  ).toThrow(/512 bytes/);
  expect(() => canonicalResourceIdentifier("urn:air:example.com:")).toThrow(
    /namespace segments/,
  );
  expect(() => canonicalResourceIdentifier("urn:air:example.com")).toThrow(
    /namespace segments/,
  );
  expect(() => canonicalResourceIdentifier("urn:air:127.0.0.1:agent")).toThrow(
    /publisher domain/,
  );
});

it("suggests a bounded short name only from the supplied item name", () => {
  expect(suggestResourceName("  Über Order / Review 2.0  ")).toBe(
    "uber-order-review-2-0",
  );
  expect(suggestResourceName("日本語 🙂")).toBe("");
  expect(suggestResourceName("---")).toBe("");
  expect(suggestResourceName("A".repeat(64) + " rest")).toBe("a".repeat(64));
  expect(suggestResourceName("a".repeat(63) + " rest")).toBe("a".repeat(63));
});
