import { expect, it } from "vitest";
import { testAccess } from "../../tests/unit/access-fixtures";
import { sessionAccess } from "../../tests/unit/fixtures";
import type { Access } from "../api/client";
import { navigationGroups, pageContext } from "./navigation";

it("marks only an explicitly opened indexing destination current with a fixed title", () => {
  const admin = { ...sessionAccess, ...testAccess(["tenant_admin"]) };
  expect(pageContext(admin, "service", "", "#search-indexing")).toMatchObject({
    current: "service#search-indexing",
    title: "Search indexing",
  });
  expect(pageContext(admin, "service", "", "#private-value")).toMatchObject({
    current: "service",
    title: "Service information",
  });
});

it.each([
  ["access-roles", "Create role", "Role"],
  ["access-groups", "Create group", "Group"],
])(
  "uses a singular %s creation and detail context without private labels",
  (route, created, detail) => {
    const admin = { ...sessionAccess, ...testAccess(["tenant_admin"]) };
    expect(pageContext(admin, route, "new")).toMatchObject({
      title: created,
      current: route,
      parent: { path: route },
    });
    expect(pageContext(admin, route, "private-id")).toMatchObject({
      title: detail,
      current: route,
      parent: { path: route },
    });
  },
);

it("shows the Overview to publication readers without local Resource access", () => {
  const base: Access = {
    ...sessionAccess,
    permissions: ["publication.read"],
    availablePermissions: ["publication.read"],
    namespaceGrants: [],
    scopeEnforcement: true,
    scopes: ["catalog.read"],
  };
  const visible = (access: Access) =>
    navigationGroups(access)
      .flatMap((group) => group.entries)
      .find((entry) => entry.path === "overview")?.allowed;
  expect(visible(base)).toBe(true);
  expect(visible({ ...base, scopes: [] })).toBe(false);
});

it("requires tenant Job admission and a readable family without unrelated OAuth ceilings", () => {
  const base: Access = {
    ...sessionAccess,
    permissions: ["job.read"],
    availablePermissions: ["job.read", "source.read"],
    namespaceGrants: [{ namespaceId: "n", permissions: ["source.read"] }],
    scopeEnforcement: true,
    scopes: ["catalog.read"],
  };
  const visible = (access: Access) =>
    navigationGroups(access)
      .flatMap((group) => group.entries)
      .find((entry) => entry.path === "jobs")?.allowed;
  expect(visible(base)).toBe(false);
  for (const family of [
    "source.read",
    "source.read_evidence",
    "publication.read",
    "operations.reindex",
  ] as const) {
    const readable = { ...base, permissions: [...base.permissions, family] };
    expect(visible(readable), family).toBe(true);
    expect(visible({ ...readable, scopes: [] }), family).toBe(false);
    expect(visible({ ...readable, permissions: [family] }), family).toBe(false);
    expect(
      visible({ ...readable, scopeEnforcement: false, scopes: [] }),
      family,
    ).toBe(true);
  }
});

it("keeps private ANS lookup independent of generic Job and source-read authority", () => {
  const access = {
    ...sessionAccess,
    permissions: ["job.read", "source.read_evidence"] as Access["permissions"],
    scopeEnforcement: true,
    scopes: ["sources.read"] as Access["scopes"],
  };
  const entries = navigationGroups(access).flatMap((group) => group.entries);
  expect(
    entries.find((entry) => entry.path === "trust-assessments")?.allowed,
  ).toBe(true);
  expect(entries.find((entry) => entry.path === "jobs")?.allowed).toBe(false);
  expect(entries.find((entry) => entry.path === "sources")?.allowed).toBe(
    false,
  );
  expect(pageContext(access, "trust-assessments", "")).toMatchObject({
    group: "Operations",
    title: "ANS assessments",
  });
  expect(pageContext(access, "trust-assessments", "exact-job")).toMatchObject({
    group: "Operations",
    title: "ANS assessment",
  });
});

it.each([
  [["resource.read"], ["catalog.read"], true, true],
  [["federation.discover"], ["catalog.read"], true, false],
  [["resource.read", "federation.discover"], ["catalog.read"], true, true],
  [["resource.read", "federation.discover"], [], false, false],
] as const)(
  "separates Catalog and local Explore for permissions %j / scopes %j",
  (permissions, scopes, catalog, explore) => {
    const access: Access = {
      ...sessionAccess,
      permissions: [...permissions],
      availablePermissions: [...permissions],
      scopeEnforcement: true,
      scopes: [...scopes],
    };
    const entries = navigationGroups(access).flatMap((group) => group.entries);
    expect(entries.find((entry) => entry.path === "catalog")?.allowed).toBe(
      catalog,
    );
    expect(entries.find((entry) => entry.path === "explore")?.allowed).toBe(
      explore,
    );
    expect(pageContext(access, "explore", "").title).toBe(
      explore ? "Explore local catalog" : "My access",
    );
  },
);

it("uses later-page available hints for navigation, and keeps own feedback independent of resource authority", () => {
  const access: Access = {
    ...sessionAccess,
    permissions: ["feedback.own"],
    availablePermissions: ["feedback.own", "resource.read"],
    nextCursor: "later",
    namespaceGrants: [],
    scopeEnforcement: true,
    scopes: ["catalog.read"],
  };
  const allowed = (value: Access, path: string) =>
    navigationGroups(value)
      .flatMap((group) => group.entries)
      .find((entry) => entry.path === path)?.allowed;
  expect(allowed(access, "explore")).toBe(true);
  expect(allowed(access, "my-feedback")).toBe(true);
  expect(allowed({ ...access, actorType: "service" }, "my-feedback")).toBe(
    false,
  );
  expect(allowed({ ...access, scopes: [] }, "my-feedback")).toBe(false);
  expect(allowed(access, "feedback")).toBe(false);
});

it("keeps every destination once in task order and reserves personal routes for Account", () => {
  const groups = navigationGroups(sessionAccess);
  expect(
    groups.map(({ key, label, placement }) => [key, label, placement]),
  ).toEqual([
    ["discover", "Discover", "sidebar"],
    ["content", "Manage content", "sidebar"],
    ["operations", "Operations", "sidebar"],
    ["administration", "Administration", "sidebar"],
    ["help", "Help", "sidebar"],
    ["account", "Account", "account"],
  ]);
  const destinations = groups.map((group) =>
    group.entries.map((entry) => entry.path),
  );
  expect(destinations).toEqual([
    ["catalog", "overview", "explore"],
    ["resources", "reviews", "publication", "namespaces", "publishers"],
    [
      "service",
      "jobs",
      "service#search-indexing",
      "sources",
      "webhooks",
      "trust-assessments",
    ],
    [
      "settings",
      "members",
      "access-groups",
      "access-roles",
      "access-assignments",
      "feedback",
      "ratings",
      "audit",
    ],
    ["getting-started", "about", "troubleshooting", "api-reference"],
    ["access", "my-work", "saved-items", "my-ratings", "my-feedback"],
  ]);
  expect(new Set(destinations.flat()).size).toBe(31);
  for (const path of [
    "access",
    "my-work",
    "saved-items",
    "my-ratings",
    "my-feedback",
  ])
    expect(pageContext(sessionAccess, path, "")).toMatchObject({
      group: "Account",
      current: path,
    });
  expect(
    pageContext(
      { ...sessionAccess, ...testAccess(["source_admin"]) },
      "observations",
      "opaque-id",
    ),
  ).toMatchObject({
    group: "Operations",
    title: "Observations",
    current: "sources",
    parent: { path: "sources" },
  });
});

it("settings navigation uses tenant read permission and the OAuth ceiling", () => {
  const visible = (access: Access) =>
    navigationGroups(access)
      .flatMap((group) => group.entries)
      .find((entry) => entry.path === "settings")?.allowed;
  const read: Access = {
    ...sessionAccess,
    permissions: ["settings.read"],
    availablePermissions: ["settings.read"],
    scopeEnforcement: true,
    scopes: ["tenant.admin"],
  };
  expect(visible(read)).toBe(true);
  expect(visible({ ...read, scopes: [] })).toBe(false);
  expect(visible({ ...read, permissions: [] })).toBe(false);
  expect(pageContext(read, "settings", "")?.title).toBe("Settings");
});

it("keeps sharing-only navigation independent of publication and personal scope ceilings", () => {
  const access: Access = {
    ...sessionAccess,
    ...testAccess([]),
    permissions: ["ratings.sharing.manage"],
    availablePermissions: ["ratings.sharing.manage"],
    scopeEnforcement: true,
    scopes: ["tenant.admin"],
  };
  const allowed = (value: Access, path: string) =>
    navigationGroups(value)
      .flatMap((group) => group.entries)
      .find((entry) => entry.path === path)?.allowed;
  expect(allowed(access, "publication")).toBe(true);
  expect(allowed(access, "my-ratings")).toBe(false);
  expect(allowed({ ...access, scopes: ["catalog.read"] }, "publication")).toBe(
    false,
  );
  expect(allowed({ ...access, scopes: ["catalog.read"] }, "my-ratings")).toBe(
    true,
  );
  expect(
    allowed(
      {
        ...access,
        actorType: "service",
        scopes: ["catalog.read", "tenant.admin"],
      },
      "my-ratings",
    ),
  ).toBe(false);
  expect(allowed({ ...access, actorType: "service" }, "publication")).toBe(
    true,
  );
});

it("keeps personal pages human-only and library reads separate from mutation scopes", () => {
  const access = {
    ...sessionAccess,
    ...testAccess(["reader"]),
    scopeEnforcement: true,
    scopes: ["catalog.read"] as Access["scopes"],
  };
  const entries = (a: Access) => navigationGroups(a).flatMap((g) => g.entries);
  expect(entries(access).find((e) => e.path === "my-work")?.allowed).toBe(true);
  expect(entries(access).find((e) => e.path === "saved-items")?.allowed).toBe(
    true,
  );
  expect(
    entries({ ...access, actorType: "service" })
      .filter((e) => ["my-work", "saved-items"].includes(e.path))
      .every((e) => !e.allowed),
  ).toBe(true);
  expect(
    entries({ ...access, permissions: [], availablePermissions: [] }).find(
      (e) => e.path === "saved-items",
    )?.allowed,
  ).toBe(false);
  expect(
    pageContext(access, "saved-items", "private-query-is-not-a-title")?.title,
  ).toBe("Saved item");
});
