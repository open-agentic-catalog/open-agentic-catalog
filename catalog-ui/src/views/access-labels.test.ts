import { expect, it } from "vitest";
import { builtinHelp, readOnlyTask, templateHelp } from "./access-labels";
import { sessionAccess } from "../../tests/unit/fixtures";
import { testAccess } from "../../tests/unit/access-fixtures";

it("covers all fourteen templates with their distinct tasks and limits", () => {
  const expected = {
    reader: [/metadata, history and retained content/, /unpublished records/],
    contributor: [/submit for review/, /existing Publisher ID/],
    reviewer: [/decide reviews independently/, /cannot approve their own/],
    publisher: [
      /Publish approved releases/,
      /separate Publication administrator/,
    ],
    tenant_admin: [/all tenant tasks/, /Independent review/],
    discovery_reader: [/published snapshots/, /job.read alone/],
    publisher_creator: [/create Publishers/, /approving its domains/],
    publication_admin: [
      /audience snapshots/,
      /does not make that context public/,
    ],
    source_admin: [/synchronization/, /destination namespace/],
    security_reviewer: [/private tenant audit/, /Resource access is absent/],
    webhook_admin: [/enabled webhook/, /deployment must enable/],
    library_member: [/own saved/, /separate target access/],
    feedback_member: [/existing private feedback/, /new report needs separate/],
    service_client: [/no product permissions/, /Human baseline ownership/],
  };
  expect(Object.keys(builtinHelp).sort()).toEqual(Object.keys(expected).sort());
  for (const [key, [task, limit]] of Object.entries(expected)) {
    const help = templateHelp({ builtin: true, key });
    expect(help?.summary).toMatch(task!);
    expect(help?.limits).toMatch(limit!);
  }
});

it.each([
  { builtin: false, key: "reader" },
  { builtin: true },
  { builtin: true, key: "" },
  { builtin: true, key: "unrecognized" },
  { builtin: true, key: "__proto__" },
  { builtin: true, key: "constructor" },
])(
  "does not derive template help from a custom or unverified key: %j",
  (role) => {
    expect(templateHelp(role)).toBeUndefined();
  },
);

it("labels inspection separately from current scoped writing and token ceilings", () => {
  const reader = { ...sessionAccess, ...testAccess(["reader"]) };
  expect(readOnlyTask(reader, "resources")).toBe(true);
  expect(readOnlyTask(reader, "reviews")).toBe(true);
  expect(readOnlyTask(reader, "service")).toBe(false);
  for (const permission of [
    "resource.create",
    "resource.edit",
    "resource.delete_draft",
    "resource.validate",
    "review.submit",
  ] as const) {
    const scoped = {
      ...reader,
      permissions: [],
      availablePermissions: [permission],
      namespaceGrants: [],
      nextCursor: "more-scopes",
      scopeEnforcement: true,
      scopes: ["catalog.write" as const],
    };
    expect(readOnlyTask(scoped, "resources")).toBe(false);
    expect(
      readOnlyTask({ ...scoped, scopes: ["catalog.read"] }, "resources"),
    ).toBe(true);
  }
  const review = {
    ...reader,
    permissions: [...reader.permissions, "review.decide" as const],
    availablePermissions: [
      ...reader.availablePermissions,
      "review.decide" as const,
    ],
    scopeEnforcement: true,
    scopes: ["catalog.review" as const],
  };
  expect(readOnlyTask(review, "reviews")).toBe(false);
  expect(readOnlyTask({ ...review, scopes: [] }, "reviews")).toBe(true);
  for (const permission of ["release.publish", "release.transition"] as const) {
    const publishing = {
      ...reader,
      permissions: [...reader.permissions, permission],
      availablePermissions: [...reader.availablePermissions, permission],
      scopeEnforcement: true,
      scopes: ["catalog.publish" as const],
    };
    expect(readOnlyTask(publishing, "reviews")).toBe(false);
    expect(readOnlyTask({ ...publishing, scopes: [] }, "reviews")).toBe(true);
  }
});
