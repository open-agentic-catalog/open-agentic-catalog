// UI hints use bounded effective permissions; the API remains authoritative.
import type { Access } from "./client";

// Tenant permissions cannot authorize a namespace granted only elsewhere.
export function permits(
  access: Access,
  permission: Access["permissions"][number],
  scope: Access["scopes"][number],
) {
  return (
    access.permissions.includes(permission) &&
    (!access.scopeEnforcement || access.scopes.includes(scope))
  );
}

/** Any-scope hints enable navigation without granting an arbitrary target. */
export function availablePermission(
  access: Access,
  permission: Access["permissions"][number],
  scope: Access["scopes"][number],
) {
  return (
    access.availablePermissions.includes(permission) &&
    (!access.scopeEnforcement || access.scopes.includes(scope))
  );
}

/** Owner withdrawal survives target/permission loss, but retains its OAuth ceiling. */
export function ownRatingAccess(access: Access) {
  const human = access.actorType === "human";
  return {
    read:
      human &&
      (!access.scopeEnforcement || access.scopes.includes("catalog.read")),
    withdraw:
      human &&
      (!access.scopeEnforcement || access.scopes.includes("ratings.write")),
  };
}

/** Keep publisher read, creation and administration hints distinct. */
export function publisherAccess(access: Access) {
  return {
    read: permits(access, "publisher.read", "publishers.read"),
    create: permits(access, "publisher.create", "publishers.manage"),
    update: permits(access, "publisher.manage", "publishers.manage"),
  };
}

/** Combine tenant and exact-namespace permission hints without bypassing scope enforcement. */
export function permitsNamespace(
  access: Access,
  permission: Access["permissions"][number],
  scope: Access["scopes"][number],
  namespaceId: string,
) {
  return (
    (!access.scopeEnforcement || access.scopes.includes(scope)) &&
    (permits(access, permission, scope) ||
      access.namespaceGrants.some(
        (grant) =>
          grant.namespaceId === namespaceId &&
          grant.permissions.includes(permission),
      ))
  );
}

/** An incomplete grant page cannot establish that creation is forbidden. */
export function resourceCreationAccess(access: Access) {
  if (access.scopeEnforcement && !access.scopes.includes("catalog.write"))
    return "denied";
  if (permits(access, "resource.create", "catalog.write")) return "allowed";
  return availablePermission(access, "resource.create", "catalog.write")
    ? "choose_namespace"
    : "denied";
}

/** Explain the selected editor's access separately from input and lifecycle requirements. */
export function contributionReason(
  access: Access,
  namespaceId?: string,
  permission: Access["permissions"][number] = "resource.edit",
  scope: Access["scopes"][number] = "catalog.write",
): string | undefined {
  if (access.scopeEnforcement && !access.scopes.includes(scope))
    return scope === "catalog.write"
      ? "The catalog.write scope is required to create or edit content."
      : `The ${scope} scope is required for this action.`;
  if (namespaceId === undefined)
    return `The ${permission} permission is required for this action.`;
  if (!namespaceId) return "Choose a namespace to check access.";
  if (permitsNamespace(access, permission, scope, namespaceId))
    return undefined;
  if (
    access.nextCursor &&
    !access.namespaceGrants.some((grant) => grant.namespaceId === namespaceId)
  )
    return "Access to this namespace has not been confirmed. Check My access or try again.";
  return `The ${permission} permission is required in this namespace.`;
}

/** A readable family enables the Job page; the API still filters exact targets. */
export function canReadJobs(access: Access) {
  return (
    permits(access, "job.read", "catalog.read") &&
    (
      [
        "source.read",
        "source.read_evidence",
        "publication.read",
        "operations.reindex",
      ] as const
    ).some((permission) => access.permissions.includes(permission))
  );
}

/** Offer job commands only for the owning permission and eligible operational states. */
export function jobActions(
  access: Access,
  job: { kind: string; state: string; cancellationRequested: boolean },
) {
  return {
    cancel: jobActionReason(access, job, "cancel") === undefined,
    retry: jobActionReason(access, job, "retry") === undefined,
  };
}

/** Explain loaded permission/state hints; current hidden target eligibility remains server-owned. */
export function jobActionReason(
  access: Access,
  job: { kind: string; state: string; cancellationRequested: boolean },
  action: "cancel" | "retry",
) {
  const family =
    job.kind === "source.sync"
      ? "source.manage"
      : job.kind === "source.trust_verify"
        ? "source.read_evidence"
        : job.kind === "publication.generate"
          ? "publication.generate"
          : job.kind === "semantic.reindex"
            ? "operations.reindex"
            : undefined;
  if (!family) return "This Job kind has no supported browser command.";
  if (access.scopeEnforcement && !access.scopes.includes("catalog.write"))
    return "The catalog.write OAuth scope is required.";
  if (!permits(access, family, "catalog.write"))
    return `The ${family} permission is required.`;
  if (
    job.kind === "source.trust_verify" &&
    (!permits(access, "source.manage", "catalog.write") ||
      !permits(access, "federation.discover", "catalog.write"))
  )
    return "ANS Job commands also require source.manage and federation.discover.";
  if (
    !permits(
      access,
      action === "cancel" ? "job.cancel" : "job.retry",
      "catalog.write",
    )
  )
    return `The job.${action} permission is required.`;
  if (action === "cancel") {
    if (job.cancellationRequested)
      return "Cancellation has already been requested; running work stops cooperatively.";
    if (!["queued", "running", "waiting"].includes(job.state))
      return "Only queued, running or waiting Jobs can be cancelled.";
  } else if (!["failed", "dead_letter"].includes(job.state))
    return "Only failed or dead-letter Jobs can create a manual retry child.";
  return undefined;
}

/** Mirror release lifecycle eligibility for action visibility; the API enforces transitions. */
export function releaseTransitionAllowed(state: string, action: string) {
  return state === "published"
    ? ["deprecate", "retire", "supersede", "revoke"].includes(action)
    : state === "deprecated"
      ? ["retire", "supersede", "revoke"].includes(action)
      : ["retired", "superseded"].includes(state) && action === "revoke";
}
