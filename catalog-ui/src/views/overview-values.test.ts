import { describe, expect, it } from "vitest";
import * as checks from "./overview-values";

const scope = {
  tenantId: "tenant",
  scope: "current_access",
  selectionLimit: 10000,
  observedAt: "2026-10-25T12:00:00Z",
};
const kinds = ["agent", "mcp_server", "skill", "plugin"].map((kind, index) => ({
  kind,
  count: index,
}));

describe("Overview consumed fields", () => {
  it("accepts complete counts, zero categories and bounded local lower bounds", () => {
    expect(
      checks.resourceOverview(
        { ...scope, total: { value: 6, limited: false }, kinds },
        "tenant",
      ),
    ).toBe(true);
    expect(
      checks.resourceOverview(
        { ...scope, total: { value: 10000, limited: true } },
        "tenant",
      ),
    ).toBe(true);
    expect(
      checks.releaseOverview(
        {
          ...scope,
          published: { value: 10000, limited: true },
          deprecated: { value: 0, limited: false },
        },
        "tenant",
      ),
    ).toBe(true);
    expect(
      checks.directoryOverview(
        { ...scope, count: { value: 0, limited: false } },
        "tenant",
      ),
    ).toBe(true);
    expect(
      checks.indexedOverview({ ...scope, count: 0, limited: false }, "tenant"),
    ).toBe(true);
    expect(checks.indexedOverview({ ...scope, limited: true }, "tenant")).toBe(
      true,
    );
  });
  it("rejects wrong scope, invalid time, unsafe counts and incomplete or inconsistent breakdowns", () => {
    const valid = { ...scope, total: { value: 6, limited: false }, kinds };
    for (const changed of [
      { tenantId: "other" },
      { scope: "tenant" },
      { observedAt: "yesterday" },
      { selectionLimit: 5000 },
      { total: { value: -1, limited: false } },
      { total: { value: Infinity, limited: false } },
      { total: { value: 1.5, limited: false } },
      { total: { value: 10001, limited: false } },
      { total: { value: 6, limited: true } },
      { kinds: undefined },
      { kinds: kinds.slice(1) },
      { kinds: [kinds[0], ...kinds.slice(0, 3)] },
      { kinds: [{ kind: "unknown", count: 0 }, ...kinds.slice(1)] },
      { kinds: [{ kind: "agent", count: 1 }, ...kinds.slice(1)] },
    ]) {
      expect(checks.resourceOverview({ ...valid, ...changed }, "tenant")).toBe(
        false,
      );
    }
    expect(
      checks.resourceOverview(
        { ...valid, total: { value: 10000, limited: true } },
        "tenant",
      ),
    ).toBe(false);
    expect(checks.boundedCount(null)).toBe(false);
    expect(
      checks.boundedCount({
        value: Number.MAX_SAFE_INTEGER + 1,
        limited: false,
      }),
    ).toBe(false);
    expect(
      checks.releaseOverview(
        {
          ...scope,
          published: { value: 0, limited: false },
          deprecated: { value: -1, limited: false },
        },
        "tenant",
      ),
    ).toBe(false);
    expect(checks.directoryOverview({ ...scope, count: 1 }, "tenant")).toBe(
      false,
    );
    for (const value of [
      { limited: true, count: 1 },
      { limited: false },
      { limited: false, count: -1 },
      { limited: false, count: 10001 },
      { limited: "false", count: 1 },
    ])
      expect(checks.indexedOverview({ ...scope, ...value }, "tenant")).toBe(
        false,
      );
  });
});

it("accepts only a single fixed origin or kind preset", () => {
  expect(checks.catalogOriginPreset("?origin=indexed")).toBe("indexed");
  expect(checks.catalogOriginPreset("?origin=local&text=private")).toBe(
    "local",
  );
  for (const query of [
    "",
    "?origin=live",
    "?origin=LOCAL",
    "?origin=local&origin=indexed",
    "?origin=",
  ])
    expect(checks.catalogOriginPreset(query)).toBeUndefined();
  expect(checks.resourceKindPreset("?kind=plugin")).toBe("plugin");
  for (const query of ["", "?kind=unknown", "?kind=agent&kind=skill"])
    expect(checks.resourceKindPreset(query)).toBe("");
});
