import { controlIcons } from "../shell/Icon";
import { navigationGroups } from "../shell/navigation";
import {
  availablePermission,
  permits,
  publisherAccess,
} from "../api/permissions";
import { result } from "../api/client";
import { documentationGuideLink } from "../api/session";
import * as checks from "./responses";
import { useRead } from "./workspace";
import type { WorkspaceProps } from "./workspace";

const tasks = [
  [
    "Membership and access",
    ["members", "access-groups", "access-roles", "access-assignments"],
  ],
  ["Content ownership", ["namespaces", "publishers"]],
  ["Moderation", ["feedback", "ratings"]],
  ["Sources and assessments", ["sources", "trust-assessments"]],
  ["Publication and sharing", ["publication"]],
  ["Notifications", ["webhooks"]],
  ["Operational work", ["jobs", "service#search-indexing"]],
  ["Evidence", ["audit"]],
] as const;

export default function SettingsControls(props: WorkspaceProps) {
  const entries = navigationGroups(props.access)
    .flatMap((group) => group.entries)
    .filter((entry) => entry.allowed);
  const capabilities = useRead(
    props.access.tenantId,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        checks.Capabilities,
      ),
    props.onFailure,
  );
  const manage: Record<string, boolean> = {
    members: permits(props.access, "access.manage", "tenant.admin"),
    "access-groups": permits(props.access, "access.manage", "tenant.admin"),
    "access-roles": permits(props.access, "access.manage", "tenant.admin"),
    "access-assignments": permits(
      props.access,
      "access.manage",
      "tenant.admin",
    ),
    namespaces:
      availablePermission(props.access, "namespace.manage", "tenant.admin") ||
      permits(props.access, "namespace.create", "tenant.admin"),
    publishers:
      publisherAccess(props.access).update ||
      publisherAccess(props.access).create ||
      permits(props.access, "publisher.approve_domain", "publishers.manage"),
    sources: (["source.inspect", "source.manage", "source.sync"] as const).some(
      (permission) => permits(props.access, permission, "sources.manage"),
    ),
    webhooks: (
      ["webhook.manage", "webhook.test", "webhook.rotate"] as const
    ).some((permission) =>
      permits(props.access, permission, "webhooks.manage"),
    ),
    publication:
      (
        [
          "publication.generate",
          "publication.activate",
          "publication.rollback",
          "publication.unpublish",
        ] as const
      ).some((permission) =>
        permits(props.access, permission, "catalog.publish"),
      ) || permits(props.access, "ratings.sharing.manage", "tenant.admin"),
    feedback: permits(props.access, "feedback.moderate", "tenant.admin"),
    ratings: permits(props.access, "ratings.moderate", "tenant.admin"),
    "service#search-indexing": permits(
      props.access,
      "operations.reindex",
      "tenant.admin",
    ),
  };
  const docs = [
    "configuration",
    "database-roles",
    "sign-in",
    "security",
  ] as const;
  const links = docs
    .map((guide) =>
      documentationGuideLink(props.runtime.documentationUrl, guide),
    )
    .filter((link) => !!link);
  return (
    <details className="workspace-advanced" aria-label="Related controls">
      <summary>Related controls and deployment settings</summary>
      <p>
        Settings access does not grant access to these controls. Each
        destination checks current permissions, OAuth scopes and its exact
        target.
      </p>
      {tasks.map(([title, paths]) => {
        const available = entries.filter((entry) =>
          paths.some((path) => path === entry.path),
        );
        return (
          available.length > 0 && (
            <div key={title}>
              <h3>{title}</h3>
              <ul>
                {available.map((entry) => (
                  <li key={entry.path}>
                    <a href={`${props.runtime.basePath}/${entry.path}`}>
                      {entry.path === "audit" ? "Audit history" : entry.label}
                    </a>
                    {" — "}
                    {entry.path === "audit"
                      ? permits(props.access, "audit.read", "audit.read")
                        ? "Read evidence; export requires its separate scope."
                        : "Export only; event browsing is unavailable with this token."
                      : entry.path === "jobs"
                        ? "View eligible Jobs; controls require family and target authority."
                        : manage[entry.path]
                          ? "Manage eligible targets."
                          : "View; changes require separate authority."}
                    {entry.path === "webhooks" && capabilities.data && (
                      <>
                        {" "}
                        Deployment capability:{" "}
                        {capabilities.data.optionalCapabilities.webhooks}.
                      </>
                    )}
                    {entry.path === "service#search-indexing" &&
                      capabilities.data && (
                        <>
                          {" "}
                          Semantic indexing: {capabilities.data.search.semantic}
                          .
                        </>
                      )}
                  </li>
                ))}
              </ul>
            </div>
          )
        );
      })}
      {capabilities.busy ? (
        <p role="status">Loading deployment capability evidence…</p>
      ) : capabilities.error ? (
        <p role="status">
          Capability evidence could not be read. Enablement is unknown; this
          does not mean a feature is disabled.
        </p>
      ) : null}
      <button
        type="button"
        disabled={capabilities.busy}
        onClick={capabilities.refresh}
      >
        {controlIcons["refresh"]}
        Refresh capability evidence
      </button>
      <p>
        Only the three live overrides above change here. Database,
        authentication, credentials, network policy, logging, retention,
        concurrency and model/provider configuration remain deployment-owned.
        Capability activation requires deployment configuration.
      </p>
      <ul>
        {links
          .filter(
            (link, i) =>
              links.findIndex((other) => other.href === link.href) === i,
          )
          .map((link) => (
            <li key={link.href}>
              <a
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={link.label}
                aria-describedby="settings-documentation-new-tab"
              >
                {link.label}
              </a>
            </li>
          ))}
      </ul>
      {links.length > 0 && (
        <span id="settings-documentation-new-tab" className="sr-only">
          Opens in a new tab
        </span>
      )}
      <a href={`${props.runtime.basePath}/api-reference`}>API reference</a>
    </details>
  );
}
