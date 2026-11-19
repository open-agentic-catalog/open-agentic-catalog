import { expect, it } from "vitest";
import { checkedNative } from "./native-capabilities";
import { checkedCriteria } from "./personal-criteria";
import { applyFilters, categoryFacetQuery } from "./search-filters";

it("preserves exact alternatives, streaming false and unrelated criteria through Apply and saved restore", () => {
  const native = {
    agentStreaming: false,
    agentSkillTags: ["Finance, billing ", "finance"],
  };
  expect(checkedNative(native)).toEqual(native);
  const query = applyFilters(
    { text: "invoice", cursor: "obsolete", limit: 24 },
    { nativeCapabilities: native },
  );
  expect(query).toEqual({
    text: "invoice",
    limit: 24,
    origins: ["local"],
    nativeCapabilities: native,
  });
  expect(categoryFacetQuery(query)).toEqual({
    text: "invoice",
    nativeCapabilities: native,
  });
  const criteria = {
    text: "invoice",
    origins: ["local"],
    nativeCapabilities: native,
  };
  expect(checkedCriteria(JSON.parse(JSON.stringify(criteria)))).toEqual(
    criteria,
  );
  expect(applyFilters(query, {})).toEqual({ text: "invoice", limit: 24 });
  expect(native.agentStreaming).toBe(false);
});

it("rejects incompatible, unknown, empty, oversized and malformed declarations", () => {
  for (const value of [
    {},
    { agentStreaming: null },
    { agentStreaming: "false" },
    { unknown: [] },
    { agentSkillIds: [] },
    { agentSkillIds: ["a", "a"] },
    { agentSkillIds: [" "] },
    { agentSkillIds: ["a\0"] },
    { agentSkillIds: ["\ud800"] },
    { agentSkillIds: ["🙂".repeat(257)] },
    { agentSkillIds: ["a"], mcpTransports: ["sse"] },
    { agentSkillIds: Array.from({ length: 17 }, (_, i) => String(i)) },
    {
      agentSkillIds: Array.from(
        { length: 16 },
        (_, i) => "<".repeat(250) + String(i),
      ),
    },
  ])
    expect(() => checkedNative(value)).toThrow();
  expect(checkedNative({ agentSkillIds: ["🙂".repeat(256)] })).toBeDefined();
  for (const criteria of [
    { nativeCapabilities: { agentStreaming: false } },
    { nativeCapabilities: { agentStreaming: false }, origins: ["live"] },
    {
      nativeCapabilities: { agentStreaming: false },
      origins: ["local"],
      kinds: ["mcp_server"],
    },
    {
      nativeCapabilities: { mcpTransports: ["sse"] },
      origins: ["local"],
      agentProtocol: "JSONRPC",
    },
  ])
    expect(() => checkedCriteria(criteria)).toThrow();
});
