import type { components } from "../api/schema";
import type { Access } from "../api/client";
import { availablePermission } from "../api/permissions";
type Role = components["schemas"]["RoleDefinition"];
type TemplateKey = NonNullable<Role["key"]>;

// UI-owned guidance never alters an immutable template or supplies permission.
export const builtinHelp: Record<
  TemplateKey,
  { summary: string; limits: string }
> = {
  reader: {
    summary:
      "Read local Resource metadata, history and retained content, and use Explore. Eligible humans can submit personal ratings.",
    limits:
      "Private reads cover the assigned scope, including unpublished records. External discovery, saved items and authoring require separate grants.",
  },
  contributor: {
    summary:
      "Create, edit and validate local Resources, versions and listings, then submit for review. Eligible humans can rate readable targets.",
    limits:
      "Use an existing Publisher ID or obtain publisher creation separately. Approval needs a different reviewer; release publication is separate.",
  },
  reviewer: {
    summary:
      "Inspect local evidence and decide reviews independently. Eligible humans can rate readable targets.",
    limits:
      "Authors cannot approve their own submissions. This template does not grant authoring, release publication or saved-item management.",
  },
  publisher: {
    summary:
      "Publish approved releases and change their lifecycle, with local evidence reads and eligible human ratings.",
    limits:
      "Discovery-snapshot administration needs a separate Publication administrator grant. Authoring, review decisions and Publisher creation remain separate.",
  },
  tenant_admin: {
    summary:
      "Manage all tenant tasks, including access, settings, content and operations.",
    limits:
      "Independent review, target lifecycle, human-only actions, OAuth ceilings and disabled capabilities still apply. This is elevated tenant administration.",
  },
  discovery_reader: {
    summary:
      "Discover external records and read eligible published snapshots and Publisher metadata. Humans can manage saved items and eligible ratings.",
    limits:
      "No local Resource metadata, history or content grant is included. job.read alone does not make operational Jobs visible; for example, a source-sync Job also requires source.read for its source.",
  },
  publisher_creator: {
    summary: "Read and create Publishers for an authoring handoff.",
    limits:
      "Updating an existing Publisher or approving its domains needs separate permissions. This template grants no local Resource reads or authoring.",
  },
  publication_admin: {
    summary:
      "Manage eligible audience snapshots and their Jobs without private Resource API reads.",
    limits:
      "Release publication is separate. Snapshot contents can include authorized audience metadata; absence of Resource API access does not make that context public.",
  },
  source_admin: {
    summary:
      "Inspect and manage federation sources, synchronization, retained source evidence and eligible Jobs, with external discovery.",
    limits:
      "Local authoring and Resource reads are separate. Adoption also needs destination namespace authority and its other prerequisites.",
  },
  security_reviewer: {
    summary:
      "Read and export private tenant audit context without Resource API reads.",
    limits:
      "Audit evidence is private even when Resource access is absent. Export requires its read companion and the appropriate OAuth scope; target actions remain separate.",
  },
  webhook_admin: {
    summary:
      "Manage enabled webhook endpoints, signing-key rotation, tests and delivery evidence.",
    limits:
      "The deployment must enable webhooks. Reading a notification target's current Resource state needs separate permissions; delivery grants no execution authority.",
  },
  library_member: {
    summary:
      "Humans can manage their own saved Resource references and named searches.",
    limits:
      "New references need separate target access. Saving a search does not authorize its execution; resolving a reference uses current visibility. Nonhuman identities cannot use personal library actions.",
  },
  feedback_member: {
    summary:
      "Humans can read, edit and withdraw their existing private feedback.",
    limits:
      "A new report needs separate Resource-read permission. Existing own feedback can survive target-access loss; moderation and other people's reports are separate.",
  },
  service_client: {
    summary:
      "The empty template supplies no product permissions. Identity type independently selects Human or Service.",
    limits:
      "Membership-only creation is tenant-wide. Human baseline ownership access remains separate; a Service identity has no human-only tasks. Existing grants are unchanged, and specialist grants are separate commands.",
  },
};

export function templateHelp(role: { builtin: boolean; key?: string }) {
  return role.builtin && role.key && Object.hasOwn(builtinHelp, role.key)
    ? builtinHelp[role.key as TemplateKey]
    : undefined;
}

export function readOnlyTask(access: Access, path: string) {
  if (path === "resources")
    return !(
      [
        "resource.create",
        "resource.edit",
        "resource.delete_draft",
        "resource.validate",
        "review.submit",
      ] as const
    ).some((p) => availablePermission(access, p, "catalog.write"));
  if (path === "reviews")
    return (
      !availablePermission(access, "review.decide", "catalog.review") &&
      !(["release.publish", "release.transition"] as const).some((p) =>
        availablePermission(access, p, "catalog.publish"),
      )
    );
  return false;
}
// Labels describe the existing finite actions; keys remain visible and authoritative.
export const permissionLabels: Record<Role["permissions"][number], string> = {
  "library.own": "Manage own saved items and searches",
  "settings.read": "Read tenant settings",
  "settings.manage": "Change tenant settings",
  "resource.read": "Read resource metadata",
  "resource.read_history": "Read resource history",
  "resource.read_content": "Read resource content",
  "resource.create": "Create resources",
  "resource.edit": "Edit resources",
  "resource.delete_draft": "Delete resource drafts",
  "resource.validate": "Validate resource revisions",
  "review.submit": "Submit listings for review",
  "review.decide": "Decide reviews",
  "release.publish": "Publish releases",
  "release.transition": "Change release state",
  "namespace.read": "Read namespaces",
  "namespace.manage": "Change namespaces",
  "namespace.create": "Create namespaces",
  "publisher.read": "Read publishers",
  "publisher.create": "Create publishers",
  "publisher.manage": "Change publishers",
  "publisher.approve_domain": "Approve publisher domains",
  "federation.discover": "Discover external resources",
  "source.read": "Read federation sources",
  "source.inspect": "Inspect source endpoints",
  "source.manage": "Manage federation sources",
  "source.sync": "Synchronize sources",
  "source.read_evidence": "Read source evidence",
  "publication.read": "Read discovery snapshots",
  "publication.generate": "Generate discovery snapshots",
  "publication.activate": "Activate discovery snapshots",
  "publication.rollback": "Restore discovery snapshots",
  "publication.unpublish": "Withdraw discovery snapshots",
  "discovery.read": "Read discovery endpoints",
  "job.read": "Read Jobs",
  "job.cancel": "Cancel Jobs",
  "job.retry": "Retry Jobs",
  "audit.read": "Read audit history",
  "audit.export": "Export audit history",
  "webhook.read": "Read webhooks",
  "webhook.manage": "Manage webhooks",
  "webhook.test": "Send webhook tests",
  "webhook.rotate": "Rotate webhook signing secrets",
  "webhook.read_evidence": "Read webhook delivery evidence",
  "ratings.own": "Manage personal ratings",
  "ratings.moderate": "Moderate ratings",
  "ratings.sharing.manage": "Manage rating summary sharing",
  "feedback.own": "Manage personal feedback",
  "feedback.moderate": "Moderate feedback",
  "operations.reindex": "Rebuild the search index",
  "access.manage": "Manage tenant access",
};
