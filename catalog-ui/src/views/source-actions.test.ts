import { expect, it } from "vitest";
import type { Access } from "../api/client";
import { reader } from "../../tests/unit/fixtures";
import {
  sourceActionChoices,
  sourceActionReason,
  type SourceAction,
} from "./source-actions";

const delegate = (permission: Access["permissions"][number]): Access => ({
  ...reader,
  permissions: [
    "source.read",
    ...(permission === "source.manage" ? ["source.inspect" as const] : []),
    permission,
  ],
  availablePermissions: [
    "source.read",
    ...(permission === "source.manage" ? ["source.inspect" as const] : []),
    permission,
  ],
  scopeEnforcement: true,
  scopes: ["sources.read", "sources.manage"],
});

it.each([
  ["source.inspect", "active", ["test"]],
  ["source.inspect", "paused", ["test"]],
  ["source.sync", "active", ["sync"]],
  ["source.sync", "error", ["sync"]],
  ["source.sync", "paused", []],
  ["source.manage", "active", ["test", "pause"]],
  ["source.manage", "error", ["test", "pause"]],
  ["source.manage", "paused", ["resume", "test"]],
  ["source.manage", "removed", []],
] as const)(
  "selects only %s commands eligible for loaded %s state",
  (permission, state, expected) => {
    expect(sourceActionChoices(delegate(permission), state)).toEqual(expected);
  },
);

it.each(["test", "sync", "pause", "resume"] as SourceAction[])(
  "keeps %s scope ceilings and read-only access independent",
  (action) => {
    expect(
      sourceActionReason(
        { ...reader, permissions: ["source.read"] },
        "active",
        action,
      ),
    ).toMatch(/permission/);
    for (const permission of [
      "source.inspect",
      "source.sync",
      "source.manage",
    ] as const)
      expect(
        sourceActionChoices(
          { ...delegate(permission), scopes: ["sources.read"] },
          "active",
        ),
      ).toEqual([]);
  },
);

it("explains paused, resume and unavailable states without network access", () => {
  const access = {
    ...delegate("source.manage"),
    permissions: [...delegate("source.manage").permissions, "source.sync"],
  } as Access;
  expect(sourceActionReason(access, "paused", "sync")).toMatch(/Resume/);
  expect(sourceActionReason(access, "paused", "pause")).toMatch(
    /already paused/,
  );
  expect(sourceActionReason(access, "active", "resume")).toMatch(
    /paused source/,
  );
  expect(sourceActionReason(access, "removed", "pause")).toMatch(/unavailable/);
});
