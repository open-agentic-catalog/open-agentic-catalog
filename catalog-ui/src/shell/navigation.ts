import { canRead, canReadLocal } from "../api/client";
import type { Access } from "../api/client";
import {
  availablePermission,
  canReadJobs,
  permits,
  ownRatingAccess,
  publisherAccess,
} from "../api/permissions";
import type { IconName } from "./Icon";

type Entry = {
  label: string;
  path: string;
  icon: IconName;
  allowed: boolean;
  children?: Record<string, string>;
};

/** Presentation groups retain each screen's current permission and scope checks. */
export function navigationGroups(access: Access): {
  key: string;
  label: string;
  placement: "sidebar" | "account";
  entries: Entry[];
}[] {
  const read = canRead(access);
  const admin = permits(access, "access.manage", "tenant.admin");
  const publishers = publisherAccess(access);
  return [
    {
      key: "discover",
      label: "Discover",
      placement: "sidebar",
      entries: [
        {
          label: "Catalog",
          path: "catalog",
          icon: "catalog",
          allowed: read,
          children: { "indexed-record": "External catalog record" },
        },
        {
          label: "Overview",
          path: "overview",
          icon: "catalog",
          allowed:
            availablePermission(access, "resource.read", "catalog.read") ||
            availablePermission(
              access,
              "resource.read_history",
              "catalog.read",
            ) ||
            availablePermission(access, "namespace.read", "catalog.read") ||
            permits(access, "publication.read", "catalog.read") ||
            permits(access, "source.read", "sources.read") ||
            permits(access, "webhook.read", "webhooks.manage") ||
            canReadJobs(access) ||
            permits(access, "operations.reindex", "tenant.admin") ||
            admin ||
            permits(access, "feedback.moderate", "tenant.admin") ||
            permits(access, "ratings.moderate", "tenant.admin") ||
            publishers.read ||
            permits(access, "federation.discover", "catalog.read"),
        },
        {
          label: "Explore local catalog",
          path: "explore",
          icon: "filters",
          allowed: canReadLocal(access),
        },
      ],
    },
    {
      key: "content",
      label: "Manage content",
      placement: "sidebar",
      entries: [
        {
          label: "Authoring",
          path: "resources",
          icon: "book",
          allowed:
            availablePermission(access, "resource.read", "catalog.read") ||
            availablePermission(access, "resource.create", "catalog.write"),
          children: {
            versions: "Resource versions",
            "version-revisions": "Version revision",
            listings: "Listings",
            "listing-revisions": "Listing revision",
            releases: "Release",
          },
        },
        {
          label: "Reviews",
          path: "reviews",
          icon: "access",
          allowed: availablePermission(
            access,
            "resource.read_history",
            "catalog.read",
          ),
        },
        {
          label: "Discovery publication",
          path: "publication",
          icon: "publish",
          allowed:
            permits(access, "publication.read", "catalog.read") ||
            permits(access, "ratings.sharing.manage", "tenant.admin"),
        },
        {
          label: "Namespaces",
          path: "namespaces",
          icon: "catalog",
          allowed:
            availablePermission(access, "namespace.read", "catalog.read") ||
            permits(access, "namespace.create", "tenant.admin"),
        },
        {
          label: "Publishers",
          path: "publishers",
          icon: "globe",
          allowed: publishers.read || publishers.create,
        },
      ],
    },
    {
      key: "operations",
      label: "Operations",
      placement: "sidebar",
      entries: [
        {
          label: "Service information",
          path: "service",
          icon: "book",
          allowed: true,
        },
        {
          label: "Jobs",
          path: "jobs",
          icon: "list",
          allowed: canReadJobs(access),
        },
        {
          label: "Search indexing",
          path: "service#search-indexing",
          icon: "filters",
          allowed: permits(access, "operations.reindex", "tenant.admin"),
        },
        {
          label: "Federation sources",
          path: "sources",
          icon: "globe",
          allowed: permits(access, "source.read", "sources.read"),
          children: {
            inspections: "Source inspection",
            "source-snapshots": "Source snapshot",
            observations: "Observations",
            "observation-group": "Observation group",
            "federation-search": "Federation search",
          },
        },
        {
          label: "Webhooks",
          path: "webhooks",
          icon: "webhook",
          allowed: permits(access, "webhook.read", "webhooks.manage"),
        },
        {
          label: "ANS assessments",
          path: "trust-assessments",
          icon: "globe",
          allowed:
            permits(access, "job.read", "sources.read") &&
            permits(access, "source.read_evidence", "sources.read"),
        },
      ],
    },
    {
      key: "administration",
      label: "Administration",
      placement: "sidebar",
      entries: [
        {
          label: "Settings",
          path: "settings",
          icon: "settings",
          allowed: permits(access, "settings.read", "tenant.admin"),
        },
        {
          label: "Members",
          path: "members",
          icon: "user",
          allowed: admin,
        },
        {
          label: "Groups",
          path: "access-groups",
          icon: "group",
          allowed: admin,
        },
        {
          label: "Roles",
          path: "access-roles",
          icon: "access",
          allowed: admin,
        },
        {
          label: "Access assignments",
          path: "access-assignments",
          icon: "access",
          allowed: admin,
        },
        {
          label: "Feedback inbox",
          path: "feedback",
          icon: "message",
          allowed: permits(access, "feedback.moderate", "tenant.admin"),
        },
        {
          label: "Rating moderation",
          path: "ratings",
          icon: "star",
          allowed: permits(access, "ratings.moderate", "tenant.admin"),
        },
        {
          label: "Audit",
          path: "audit",
          icon: "history",
          allowed:
            permits(access, "audit.read", "audit.read") ||
            permits(access, "audit.export", "audit.export"),
        },
      ],
    },
    {
      key: "help",
      label: "Help",
      placement: "sidebar",
      entries: [
        {
          label: "Getting started",
          path: "getting-started",
          icon: "book",
          allowed: true,
        },
        { label: "About", path: "about", icon: "catalog", allowed: true },
        {
          label: "Troubleshooting",
          path: "troubleshooting",
          icon: "book",
          allowed: true,
        },
        {
          label: "API reference",
          path: "api-reference",
          icon: "book",
          allowed: true,
        },
      ],
    },
    {
      key: "account",
      label: "Account",
      placement: "account",
      entries: [
        { label: "My access", path: "access", icon: "access", allowed: true },
        {
          label: "My work",
          path: "my-work",
          icon: "book",
          allowed:
            access.actorType === "human" &&
            (!access.scopeEnforcement ||
              access.scopes?.includes("catalog.read") === true),
        },
        {
          label: "Saved items and searches",
          path: "saved-items",
          icon: "bookmark",
          allowed:
            access.actorType === "human" &&
            permits(access, "library.own", "catalog.read"),
        },
        {
          label: "My ratings",
          path: "my-ratings",
          icon: "star",
          allowed: ownRatingAccess(access).read,
        },
        {
          label: "My feedback",
          path: "my-feedback",
          icon: "message",
          allowed:
            access.actorType === "human" &&
            permits(access, "feedback.own", "catalog.read"),
        },
      ],
    },
  ];
}

function entryMatches(entry: Entry, view: string) {
  return entry.path === view || Object.hasOwn(entry.children ?? {}, view);
}

/** Only fixed vocabulary enters browser titles; never record names, IDs or queries. */
export function pageContext(
  access: Access,
  view: string,
  id: string,
  fragment = "",
) {
  const groups = navigationGroups(access);
  // Catalog/Explore without Reader authority deliberately render My access.
  const effective =
    (view === "catalog" && !canRead(access)) ||
    (view === "explore" && !canReadLocal(access))
      ? "access"
      : view === "service" && fragment === "#search-indexing"
        ? "service#search-indexing"
        : view;
  for (const group of groups) {
    for (const entry of group.entries) {
      if (!entryMatches(entry, effective)) continue;
      const child = entry.children?.[effective];
      const detailTitles: Record<string, string> = {
        catalog: "Catalog record",
        feedback: "Feedback case",
        "my-feedback": "Feedback case",
        ratings: "Rating case",
        "my-ratings": "Rating case",
        "saved-items": "Saved item",
        resources: "Resource",
        versions: "Resource version",
        listings: "Listing",
        reviews: "Review",
        sources: "Federation source",
        members: "Member",
        "access-roles": "Role",
        "access-groups": "Group",
        namespaces: "Namespace",
        publishers: "Publisher",
        jobs: "Job",
        "trust-assessments": "ANS assessment",
        webhooks: "Webhook",
        publication: "Publication snapshot",
      };
      const title =
        id === "new"
          ? `Create ${(detailTitles[effective] ?? entry.label).toLowerCase()}`
          : ((id ? detailTitles[effective] : undefined) ??
            child ??
            entry.label);
      return {
        group: group.label,
        title,
        current: entry.path,
        parent: (child || id) && entry.allowed ? entry : undefined,
      };
    }
  }
  return {
    group: "Account",
    title: "My access",
    current: "access",
    parent: undefined,
  };
}
