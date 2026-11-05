import { permissionKeys } from "../../src/api/permission-keys";
export const role = {
  id: "role-a",
  tenantId: "tenant-a",
  builtin: false,
  name: "Metadata reader",
  description: "Read one namespace",
  state: "active" as const,
  revision: 2,
  permissions: [
    "namespace.read",
    "resource.read",
  ] as (typeof permissionKeys)[number][],
  namespaceCapable: true,
  createdAt: "2026-10-05T10:00:00Z",
  updatedAt: "2026-10-05T10:00:00Z",
};
export const group = {
  id: "group-a",
  tenantId: "tenant-a",
  realmId: "realm-a",
  name: "Metadata readers",
  description: "Direct group",
  state: "active" as const,
  revision: 2,
  createdAt: role.createdAt,
  updatedAt: role.updatedAt,
};
export const assignment = {
  id: "assignment-a",
  tenantId: "tenant-a",
  realmId: "realm-a",
  roleDefinitionId: role.id,
  membershipId: "member-a",
  namespaceId: "namespace-a",
  state: "active" as const,
  revision: 3,
  grantedBy: "admin",
  grantReason: "Reviewed assignment",
  grantedAt: role.createdAt,
};
export const member = {
  id: "group-member-a",
  tenantId: "tenant-a",
  realmId: "realm-a",
  groupId: group.id,
  membershipId: "member-a",
  state: "active" as const,
  revision: 2,
  addedBy: "admin",
  addReason: "Reviewed group membership",
  addedAt: role.createdAt,
};
export const descriptors = permissionKeys.map((key) => ({
  key,
  delegable: key !== "access.manage",
  namespaceScoped:
    key.startsWith("resource.") ||
    key.startsWith("namespace.") ||
    key.startsWith("review.") ||
    key.startsWith("release."),
  requires: key === "resource.create" ? ["namespace.read"] : [],
}));
