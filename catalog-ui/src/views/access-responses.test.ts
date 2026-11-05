import { describe, expect, it } from "vitest";
import * as check from "./access-responses";
import { permissionKeys } from "../api/permission-keys";
import {
  role,
  group,
  assignment,
  member,
  descriptors,
} from "../../tests/unit/policy-fixtures";
describe("policy response consumption", () => {
  it.each([
    ["role", check.role, role],
    ["group", check.group, group],
    ["assignment", check.assignment, assignment],
    ["member", check.groupMember, member],
  ] as const)(
    "rejects malformed consumed %s fields",
    (_name, predicate, valid) => {
      expect(predicate(valid)).toBe(true);
      expect(predicate(null)).toBe(false);
      expect(predicate([])).toBe(false);
      for (const key of Object.keys(valid))
        expect(predicate({ ...valid, [key]: null })).toBe(false);
      expect(predicate({ ...valid, revision: 0 })).toBe(false);
      expect(predicate({ ...valid, state: "unknown" })).toBe(false);
      expect(predicate({ ...valid, id: "/bad/path" })).toBe(false);
    },
  );
  it("checks scoped/direct/group assignments without manufacturing a target", () => {
    const { membershipId, ...scoped } = assignment;
    expect(membershipId).toBe("member-a");
    expect(check.assignment({ ...scoped, groupId: "group-a" })).toBe(true);
    expect(check.assignment(scoped)).toBe(false);
    expect(check.assignment({ ...assignment, groupId: "group-a" })).toBe(false);
    const { namespaceId, ...global } = assignment;
    expect(namespaceId).toBe("namespace-a");
    expect(check.assignment(global)).toBe(true);
    expect(check.role({ ...role, permissions: [...permissionKeys] })).toBe(
      true,
    );
    expect(check.role({ ...role, permissions: ["invented"] })).toBe(false);
    expect(
      check.role({ ...role, permissions: ["resource.read", "resource.read"] }),
    ).toBe(false);
    expect(
      check.role({ ...role, permissions: Array(45).fill("resource.read") }),
    ).toBe(false);
  });
  it("bounds pages and checks effective role evidence and policy revisions", () => {
    const valid = {
      items: [{ assignment, role }],
      authorizationRevision: 3,
      nextCursor: "opaque",
    };
    const page = check.policyPage(check.effective);
    expect(page(valid)).toBe(true);
    for (const value of [
      null,
      [],
      { ...valid, items: null },
      { ...valid, items: [{ assignment: null, role }] },
      { ...valid, items: [{ assignment, role: null }] },
      { ...valid, items: Array(101).fill({ assignment, role }) },
      { ...valid, authorizationRevision: 0 },
      { ...valid, nextCursor: "x".repeat(2049) },
    ])
      expect(page(value)).toBe(false);
  });
  it("validates the complete bounded permission vocabulary and companion declarations", () => {
    expect(check.descriptors(descriptors)).toBe(true);
    for (const value of [
      null,
      [],
      [...descriptors, descriptors[0]],
      descriptors.map((d, i) => (i ? d : { ...d, key: "unknown" })),
      descriptors.map((d, i) => (i ? d : { ...d, requires: ["unknown"] })),
      descriptors.map((d, i) => (i ? d : { ...d, delegable: null })),
      descriptors.map((d, i) => (i ? d : { ...d, namespaceScoped: null })),
      [null, ...descriptors.slice(1)],
    ])
      expect(check.descriptors(value)).toBe(false);
  });
});

it("keeps runtime self-access vocabulary exhaustive for the generated permission contract", () => {
  // Compilation fails when the contract adds an action omitted by the runtime decoder.
  type Key = import("../api/schema").components["schemas"]["PermissionKey"];
  type Missing = Exclude<
    Key,
    (typeof import("../api/permission-keys").permissionKeys)[number]
  >;
  const exhaustive: Missing extends never ? true : false = true;
  expect(exhaustive).toBe(true);
});
