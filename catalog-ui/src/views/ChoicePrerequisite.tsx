import { permits, permitsNamespace } from "../api/permissions";
import type { WorkspaceProps } from "./workspace";

/** Choice recovery stays within already-known authority; it performs no reads. */
export function ChoicePrerequisite({
  props,
  kind,
  resourceId,
  namespaceId,
  versionId,
  newTab = false,
}: {
  props: WorkspaceProps;
  kind: "namespace" | "publisher" | "version" | "revision";
  resourceId?: string;
  namespaceId?: string;
  versionId?: string;
  newTab?: boolean;
}) {
  const choices = {
    namespace: {
      message:
        "A permitted namespace is required. Ask a Tenant Administrator to create one or grant access.",
      href: "/namespaces",
      label: "Manage namespaces",
      allowed:
        permits(props.access, "namespace.read", "catalog.read") &&
        permits(props.access, "namespace.create", "tenant.admin"),
    },
    publisher: {
      message:
        "A local publisher is required. Ask a publisher administrator to create one or provide its exact ID.",
      href: "/publishers",
      label: "Manage publishers",
      allowed:
        permits(props.access, "publisher.read", "publishers.read") &&
        permits(props.access, "publisher.create", "publishers.manage"),
    },
    version: {
      message:
        "A version and immutable revision of this resource must exist before a listing can reference it.",
      href: `/resources/${resourceId}`,
      label: "Open resource to create a version",
      allowed:
        !!resourceId &&
        !!namespaceId &&
        permitsNamespace(
          props.access,
          "resource.edit",
          "catalog.write",
          namespaceId,
        ) &&
        permitsNamespace(
          props.access,
          "resource.read",
          "catalog.read",
          namespaceId,
        ),
    },
    revision: {
      message:
        "Choose an immutable revision of this version. If none is available, ask its author to create one.",
      href: `/versions/${versionId}`,
      label: "Open version",
      allowed:
        !!versionId &&
        !!namespaceId &&
        permitsNamespace(
          props.access,
          "resource.read_history",
          "catalog.read",
          namespaceId,
        ),
    },
  };
  const choice = choices[kind];
  return (
    <p className="muted">
      {choice.message} These are bounded choices, not a complete inventory.
      Check your access or retry an unavailable read.{" "}
      {choice.allowed && (
        <a
          href={`${props.runtime.basePath}${choice.href}`}
          {...(newTab
            ? {
                target: "_blank",
                rel: "noopener noreferrer",
                title: "Opens in a new tab; your draft stays here.",
              }
            : {})}
        >
          {newTab && kind === "publisher" ? "Create publisher" : choice.label}
          {newTab && " (opens in a new tab)"}
        </a>
      )}
    </p>
  );
}
