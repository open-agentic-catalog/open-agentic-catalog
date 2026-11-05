// Validate consumed policy records before they drive paths, permissions or commands.
import {
  array,
  choice,
  cursor,
  flag,
  identifier,
  number,
  object,
  text,
  timestamp,
  optional,
} from "../api/values";
import { permissionKeys } from "../api/permission-keys";
import { Membership } from "./responses";
export function role(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    flag(value.builtin) &&
    optional(value.key, text) &&
    text(value.name) &&
    text(value.description) &&
    choice(value.state, ["active", "archived"]) &&
    number(value.revision, 1) &&
    flag(value.namespaceCapable) &&
    Array.isArray(value.permissions) &&
    value.permissions.length <= permissionKeys.length &&
    new Set(value.permissions).size === value.permissions.length &&
    value.permissions.every((p) => choice(p, permissionKeys)) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}

export function group(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    identifier(value.realmId) &&
    text(value.name) &&
    text(value.description) &&
    choice(value.state, ["active", "suspended", "archived"]) &&
    number(value.revision, 1) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}

export function assignment(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    identifier(value.realmId) &&
    identifier(value.roleDefinitionId) &&
    ((identifier(value.membershipId) && value.groupId === undefined) ||
      (identifier(value.groupId) && value.membershipId === undefined)) &&
    optional(value.namespaceId, identifier) &&
    choice(value.state, ["active", "revoked"]) &&
    number(value.revision, 1) &&
    text(value.grantedBy) &&
    text(value.grantReason) &&
    timestamp(value.grantedAt)
  );
}

export function groupMember(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    identifier(value.realmId) &&
    identifier(value.membershipId) &&
    identifier(value.groupId) &&
    choice(value.state, ["active", "revoked"]) &&
    number(value.revision, 1) &&
    text(value.addedBy) &&
    text(value.addReason) &&
    timestamp(value.addedAt)
  );
}

export function effective(value: unknown): boolean {
  return (
    object(value) &&
    object(value.assignment) &&
    object(value.role) &&
    assignment(value.assignment) &&
    role(value.role) &&
    value.assignment.roleDefinitionId === value.role.id &&
    value.assignment.tenantId === value.role.tenantId &&
    (value.groupName === undefined ||
      (identifier(value.assignment.groupId) &&
        text(value.groupName) &&
        value.groupName.trim().length > 0 &&
        [...value.groupName].length <= 120))
  );
}

export function memberAccess(value: unknown): boolean {
  if (
    !object(value) ||
    !object(value.membership) ||
    !Membership(value.membership) ||
    !identifier(value.membership.tenantId) ||
    !choice(value.groupCoverage, ["current_realm", "unavailable"])
  )
    return false;
  const member = value.membership;
  return policyPage((item) => {
    if (
      !object(item) ||
      !object(item.assignment) ||
      !object(item.role) ||
      !assignment(item.assignment) ||
      !role(item.role) ||
      !flag(item.effective)
    )
      return false;
    const a = item.assignment,
      definition = item.role;
    if (
      a.tenantId !== member.tenantId ||
      a.realmId !== member.realmId ||
      definition.tenantId !== member.tenantId ||
      a.roleDefinitionId !== definition.id
    )
      return false;
    if (
      item.effective &&
      (member.state !== "active" ||
        a.state !== "active" ||
        definition.state !== "active")
    )
      return false;
    if (!a.groupId)
      return (
        a.membershipId === member.id &&
        item.group === undefined &&
        item.relation === undefined
      );
    if (
      value.groupCoverage !== "current_realm" ||
      !object(item.group) ||
      !object(item.relation) ||
      !group(item.group) ||
      !groupMember(item.relation)
    )
      return false;
    const g = item.group,
      relation = item.relation;
    return (
      g.id === a.groupId &&
      relation.groupId === g.id &&
      relation.membershipId === member.id &&
      g.tenantId === member.tenantId &&
      relation.tenantId === member.tenantId &&
      g.realmId === member.realmId &&
      relation.realmId === member.realmId &&
      (!item.effective || (g.state === "active" && relation.state === "active"))
    );
  })(value);
}

export function policyPage(check: (value: unknown) => boolean) {
  return (value: unknown) =>
    object(value) &&
    Array.isArray(value.items) &&
    value.items.length <= 100 &&
    array(value.items, check) &&
    number(value.authorizationRevision, 1) &&
    cursor(value.nextCursor);
}

export function descriptors(value: unknown): boolean {
  return (
    Array.isArray(value) &&
    value.length === permissionKeys.length &&
    new Set(value.map((v) => (object(v) ? v.key : undefined))).size ===
      permissionKeys.length &&
    value.every(
      (v) =>
        object(v) &&
        choice(v.key, permissionKeys) &&
        flag(v.namespaceScoped) &&
        flag(v.delegable) &&
        array(v.requires, (r) => choice(r, permissionKeys)),
    )
  );
}
