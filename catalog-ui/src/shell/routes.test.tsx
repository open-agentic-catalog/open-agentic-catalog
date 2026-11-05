import { testAccess } from "../../tests/unit/access-fixtures";
// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { APIError } from "../api/client";
import {
  deferred,
  processSample,
  sessionAccess as reader,
} from "../../tests/unit/fixtures";
import { App } from "./App";

const { auth, get, post, reference, invalidate } = vi.hoisted(() => ({
  auth: vi.fn<(...args: [string, ...unknown[]]) => Promise<unknown>>(),
  get: vi.fn<
    (
      path: string,
      options: {
        signal: AbortSignal;
        params: { path?: object; query?: object };
        body?: object;
      },
    ) => Promise<unknown>
  >(),
  post: vi.fn<
    (
      path: string,
      options: {
        signal: AbortSignal;
        params: { path?: object; query?: object };
        body?: object;
      },
    ) => Promise<unknown>
  >(),
  reference: vi.fn<(signal: AbortSignal) => Promise<unknown>>(),
  invalidate: vi.fn(),
}));
vi.mock("../api/client", async (original) => {
  const actual = await original<typeof import("../api/client")>();
  return {
    ...actual,
    makeAPI: (runtime: import("../api/client").Runtime) => {
      const client = actual.makeAPI(runtime);
      return {
        auth,
        api: { GET: get, POST: post },
        reference,
        processInfo: async () => ({
          ...processSample,
          role: "ui",
          version: "ui-fixture",
          commit: "ui-fixture",
          sourceState: "unknown",
        }),
        buildInfo: async () => ({
          version: "ui-fixture",
          commit: "ui-fixture",
          sourceState: "unknown",
        }),
        get signal() {
          return client.signal;
        },
        invalidate: () => {
          invalidate();
          client.invalidate();
        },
      };
    },
  };
});

// Real lazy-loaded screens must receive the bookmark identity and propagate
// expired reads to the shell. Their command/input permutations stay colocated.
type Route = {
  route: string;
  heading: string;
  endpoint: string;
  path?: Record<string, string>;
  query?: Record<string, unknown>;
  additional?: string;
  additionalReads?: string[];
  action?: string;
  method?: "POST" | "reference";
};
const routes: Route[] = [
  {
    route: "overview?view=content",
    heading: "Overview",
    endpoint: "/api/v1/overview/resources",
    additionalReads: [
      "/api/v1/overview/releases",
      "/api/v1/overview/namespaces",
      "/api/v1/overview/publishers",
      "/api/v1/overview/indexed",
      "/api/v1/overview/reviews",
      "/api/v1/overview/publication",
      "/api/v1/capabilities",
    ],
  },
  {
    route: "my-work",
    heading: "My work",
    endpoint: "/api/v1/me/work",
    query: { queue: "drafts", limit: 25 },
  },
  {
    route: "saved-items",
    heading: "Saved items and searches",
    endpoint: "/api/v1/personal/items",
    query: { limit: 25 },
  },
  {
    route: "settings",
    heading: "Tenant settings",
    endpoint: "/api/v1/tenant-settings",
    additionalReads: ["/api/v1/capabilities"],
  },
  {
    route:
      "trust-assessments/00000000-0000-7000-8000-000000000002?sourceId=00000000-0000-7000-8000-000000000001",
    heading: "ANS assessments",
    endpoint: "/api/v1/federation/sources/{sourceId}/trust-assessments/{jobId}",
    action: "Read private assessment",
    path: {
      sourceId: "00000000-0000-7000-8000-000000000001",
      jobId: "00000000-0000-7000-8000-000000000002",
    },
  },
  {
    route: "my-ratings",
    heading: "My ratings",
    endpoint: "/api/v1/ratings",
    query: { limit: 25 },
  },
  {
    route: "ratings",
    heading: "Rating moderation",
    endpoint: "/api/v1/ratings/inbox",
    query: { limit: 25 },
  },
  {
    route: "my-feedback",
    heading: "My feedback",
    endpoint: "/api/v1/feedback",
    additional: "/api/v1/resources",
  },
  {
    route: "feedback",
    heading: "Feedback inbox",
    endpoint: "/api/v1/feedback/inbox",
    additional: "/api/v1/resources",
  },
  {
    route: "access",
    heading: "My access",
    endpoint: "/api/v1/me",
    action: "Next page",
    query: { cursor: "grants-next" },
  },
  {
    route: "unknown-bookmark",
    heading: "My access",
    endpoint: "/api/v1/me",
    action: "Next page",
    query: { cursor: "grants-next" },
  },
  ...["constructor", "toString", "__proto__", "hasOwnProperty"].map(
    (route) => ({
      route,
      heading: "My access",
      endpoint: "/api/v1/me",
      action: "Next page",
      query: { cursor: "grants-next" },
    }),
  ),
  {
    route: "namespaces/ns-bookmark",
    heading: "Namespaces",
    endpoint: "/api/v1/namespaces/{namespaceId}",
    path: { namespaceId: "ns-bookmark" },
  },
  {
    route: "publishers/publisher-bookmark",
    heading: "Publishers",
    endpoint: "/api/v1/publishers/{publisherId}",
    path: { publisherId: "publisher-bookmark" },
  },
  {
    route: "jobs/job-bookmark",
    heading: "Jobs",
    endpoint: "/api/v1/jobs/{jobId}",
    path: { jobId: "job-bookmark" },
  },
  {
    route: "service",
    heading: "Service information",
    endpoint: "/api/v1/service-information",
    additional: "/api/v1/capabilities",
  },
  {
    route: "resources/resource-bookmark",
    heading: "Authoring",
    endpoint: "/api/v1/resources/{resourceId}",
    path: { resourceId: "resource-bookmark" },
  },
  {
    route:
      "indexed-record?identifier=urn:air:example.com:agents:helper&versionPresent=true&version=2.0",
    heading: "External catalog record",
    endpoint: "/api/v1/catalog/indexed-record",
    query: {
      identifier: "urn:air:example.com:agents:helper",
      versionPresent: true,
      version: "2.0",
    },
  },
  {
    route: "versions/version-bookmark",
    heading: "Resource versions",
    endpoint: "/api/v1/resource-versions/{versionId}",
    path: { versionId: "version-bookmark" },
  },
  {
    route: "version-revisions/revision-bookmark",
    heading: "Version revision",
    endpoint: "/api/v1/version-revisions/{revisionId}",
    path: { revisionId: "revision-bookmark" },
  },
  {
    route: "listings/listing-bookmark",
    heading: "Listing",
    endpoint: "/api/v1/catalog-listings/{listingId}",
    path: { listingId: "listing-bookmark" },
  },
  {
    route: "listing-revisions/revision-bookmark",
    heading: "Listing revision",
    endpoint: "/api/v1/listing-revisions/{revisionId}",
    path: { revisionId: "revision-bookmark" },
  },
  {
    route: "reviews/submission-bookmark",
    heading: "Reviews",
    endpoint: "/api/v1/review-submissions/{submissionId}",
    path: { submissionId: "submission-bookmark" },
  },
  {
    route: "releases/release-bookmark",
    heading: "Release",
    endpoint: "/api/v1/listing-releases/{releaseId}",
    path: { releaseId: "release-bookmark" },
  },
  {
    route: "members/member-bookmark",
    heading: "Members and access",
    endpoint: "/api/v1/memberships/{membershipId}",
    path: { membershipId: "member-bookmark" },
  },
  {
    route: "publication/snapshot-bookmark?host=catalog.example.com",
    heading: "Discovery publication",
    endpoint: "/api/v1/publication/snapshots/{snapshotId}",
    additional: "/api/v1/publication/hosts",
    path: { snapshotId: "snapshot-bookmark" },
    query: { host: "catalog.example.com" },
  },
  {
    route: "audit",
    heading: "Audit investigation",
    endpoint: "/api/v1/audit-events",
    action: "Search audit events",
  },
  {
    route: "explore",
    heading: "Explore local catalog",
    endpoint: "/api/v1/catalog/resources",
  },
  { route: "webhooks", heading: "Webhooks", endpoint: "/api/v1/capabilities" },
  {
    route: "sources/source-bookmark",
    heading: "Federation sources",
    endpoint: "/api/v1/federation/sources/{sourceId}",
    path: { sourceId: "source-bookmark" },
  },
  {
    route: "inspections/inspection-bookmark",
    heading: "Source inspection",
    endpoint: "/api/v1/federation/source-inspections/{inspectionId}",
    path: { inspectionId: "inspection-bookmark" },
  },
  {
    route: "source-snapshots/snapshot-bookmark",
    heading: "Source snapshot",
    endpoint: "/api/v1/federation/snapshots/{snapshotId}",
    path: { snapshotId: "snapshot-bookmark" },
  },
  {
    route: "observations?source=source-bookmark",
    heading: "Source observations",
    endpoint: "/api/v1/observations",
    query: { sourceId: "source-bookmark" },
  },
  {
    route:
      "observation-group?identifier=urn:air:example.com:agents:helper&versionPresent=true&version=2.0",
    heading: "Observation contributions",
    endpoint: "/api/v1/observation-groups",
    query: {
      identifier: "urn:air:example.com:agents:helper",
      versionPresent: true,
      version: "2.0",
    },
  },
  {
    route: "federation-search",
    heading: "Federation diagnostics",
    endpoint: "/api/v1/observations:search",
    method: "POST",
    action: "Search indexed observations",
  },
  {
    route: "api-reference",
    heading: "API reference",
    endpoint: "reference",
    method: "reference",
  },
  { route: "access-roles", heading: "Roles", endpoint: "/api/v1/access/roles" },
  {
    route: "access-groups",
    heading: "Groups",
    endpoint: "/api/v1/access/groups",
  },
  {
    route: "access-assignments",
    heading: "Access assignments",
    endpoint: "/api/v1/access/assignments",
    additionalReads: ["/api/v1/access/roles", "/api/v1/memberships"],
  },
];

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      close() {}
      postMessage() {}
    },
  );
  auth.mockImplementation(async (path: string) => {
    if (path === "options")
      return {
        mode: "development",
        users: [{ alias: "reader", label: "Test user" }],
      };
    if (path === "session")
      return {
        access: {
          ...reader,
          ...testAccess(["tenant_admin"]),
          nextCursor: "grants-next",
        },
        expiresAt: Math.floor(Date.now() / 1000) + 600,
      };
    throw new Error("Unexpected authentication command");
  });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});

it("consults the private case's dirty guard before replacing the mounted editor on Back", async () => {
  // Qualify navigation after the lazy screen module is loaded, independently of import scheduling.
  await import("../views/Ratings");
  history.replaceState(null, "", "/unit/ratings");
  const rating = {
    id: "00000000-0000-7000-8000-000000000077",
    tenantId: reader.tenantId,
    target: {
      origin: "foreign",
      sourceId: "source-1",
      presentationGeneration: 2,
      identifier: "urn:air:example.com:agent:case",
      kind: "agent",
      versionPresent: false,
    },
    score: 4,
    consent: false,
    state: "active",
    revision: 1,
    createdAt: "2026-09-01T12:00:00Z",
    updatedAt: "2026-09-01T12:00:00Z",
  };
  get.mockImplementation(async (path) => ({
    data: path === "/api/v1/ratings/{ratingId}" ? rating : { items: [rating] },
    response: new Response(),
  }));
  await act(async () => {
    render(
      <App
        runtime={{
          basePath: "/unit",
          authMode: "development",
          documentationUrl: "",
        }}
      />,
    );
  });
  fireEvent.click(
    await screen.findByRole("link", { name: "Open rating case" }),
  );
  await screen.findByRole("button", { name: "Copy rating ID" });
  expect(document.title).toBe("Rating case | Open Agentic Catalog");
  fireEvent.click(screen.getByRole("button", { name: "Remove rating" }));
  fireEvent.change(screen.getByLabelText("Moderation reason"), {
    target: { value: "Preserve this case draft" },
  });
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const go = vi.spyOn(history, "go").mockImplementation(() => {});
  act(() => {
    history.replaceState({ caseNavigation: 0 }, "", "/unit/ratings");
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
  });
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(go).toHaveBeenCalledWith(1);
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Preserve this case draft",
  );
  expect(
    screen.getByRole("button", { name: "Read current rating" }),
  ).toBeTruthy();
  expect(get).toHaveBeenCalledTimes(2);
  confirm.mockReturnValue(true);
  act(() => {
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: { caseNavigation: 0 } }),
    );
  });
  await screen.findByRole("link", { name: "Open rating case" });
  expect(get).toHaveBeenCalledTimes(3);
});

it.each(routes)(
  "routes $route to its exact read and clears it on session expiry",
  async (route) => {
    history.replaceState(null, "", `/unit/${route.route}`);
    const pending = deferred<never>();
    get.mockImplementation((path) =>
      path === "/api/v1/access/self"
        ? Promise.resolve({
            data: { items: [], authorizationRevision: 1 },
            response: new Response(),
          })
        : pending.promise,
    );
    post.mockImplementation(() => pending.promise);
    reference.mockImplementation(() => pending.promise);
    // Let React settle session and lazy-import updates before inspecting the
    // bookmarked screen; its API response stays explicitly controlled.
    await act(async () => {
      render(
        <App
          runtime={{
            basePath: "/unit",
            authMode: "development",
            documentationUrl: "",
          }}
        />,
      );
    });
    await screen.findByRole("heading", { name: route.heading, level: 1 });
    expect(document.title).toMatch(/ \| Open Agentic Catalog$/);
    expect(document.title).not.toContain("bookmark");
    expect(document.title).not.toContain("urn:air");
    expect(document.title).not.toContain("private");
    expect(
      screen.getByRole("navigation", { name: "Breadcrumb" }).textContent,
    ).not.toContain("bookmark");
    if (route.action) {
      expect(
        get.mock.calls.filter(([path]) => path !== "/api/v1/access/self"),
      ).toEqual([]);
      expect(post).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole("button", { name: route.action }));
    }
    const request =
      route.method === "reference"
        ? reference
        : route.method === "POST"
          ? post
          : get;
    await waitFor(() =>
      expect(
        request.mock.calls.filter(([path]) => path !== "/api/v1/access/self"),
      ).toHaveLength(
        1 + (route.additional ? 1 : 0) + (route.additionalReads?.length ?? 0),
      ),
    );
    if (route.method === "reference") {
      expect(reference).toHaveBeenCalledWith(expect.any(AbortSignal));
      expect(
        get.mock.calls.filter(([path]) => path !== "/api/v1/access/self"),
      ).toEqual([]);
    } else {
      const apiRequest = route.method === "POST" ? post : get;
      const expected = [
        route.endpoint,
        ...(route.additional ? [route.additional] : []),
        ...(route.additionalReads ?? []),
      ];
      expect(
        apiRequest.mock.calls
          .filter(([path]) => path !== "/api/v1/access/self")
          .map(([path]) => path)
          .sort(),
      ).toEqual(expected.sort());
      const options = apiRequest.mock.calls.find(
        ([path]) => path === route.endpoint,
      )![1];
      expect(options.signal).toBeInstanceOf(AbortSignal);
      if (route.path) expect(options.params.path).toEqual(route.path);
      if (route.query) expect(options.params.query).toMatchObject(route.query);
      if (route.method === "POST")
        expect(options.body).toEqual({
          text: undefined,
          tags: [],
          kinds: undefined,
          limit: 25,
          cursor: undefined,
        });
    }
    if (route.method !== "POST") expect(post).not.toHaveBeenCalled();
    const calls = request.mock.calls.length;
    await act(async () => pending.reject(new APIError(401, "login_required")));
    await screen.findByRole("button", { name: "Test user" });
    expect(document.title).toBe("Sign in | Open Agentic Catalog");
    expect(
      screen.queryByRole("heading", { name: route.heading, level: 1 }),
    ).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain(
      "Your session is unavailable",
    );
    expect(invalidate).toHaveBeenCalled();
    expect(request).toHaveBeenCalledTimes(calls);
    // Session invalidation starts options again under the new client lifetime.
    expect(auth.mock.calls.map(([path]) => path).sort()).toEqual([
      "options",
      "options",
      "session",
    ]);
  },
);

// Navigation hints must not require an unrelated metadata permission. Exact
// server checks still decide which history/content record may be disclosed.
it.each([
  ["resources/new", "Authoring", "resource.create", "/api/v1/namespaces"],
  [
    "reviews/submission-bookmark",
    "Reviews",
    "resource.read_history",
    "/api/v1/review-submissions/{submissionId}",
  ],
  [
    "releases/release-bookmark",
    "Release",
    "resource.read_history",
    "/api/v1/listing-releases/{releaseId}",
  ],
  [
    "version-revisions/revision-bookmark",
    "Version revision",
    "resource.read_content",
    "/api/v1/version-revisions/{revisionId}",
  ],
  [
    "listing-revisions/revision-bookmark",
    "Listing revision",
    "resource.read_content",
    "/api/v1/listing-revisions/{revisionId}",
  ],
] as const)(
  "opens %s with its actual read capability",
  async (route, heading, permission, endpoint) => {
    history.replaceState(null, "", `/unit/${route}`);
    auth.mockImplementation(async (path: string) =>
      path === "options"
        ? { mode: "development", users: [] }
        : {
            access: {
              ...reader,
              permissions: [],
              availablePermissions: [permission],
              namespaceGrants: [
                { namespaceId: "ns-1", permissions: [permission] },
              ],
            },
            expiresAt: Math.floor(Date.now() / 1000) + 600,
          },
    );
    const pending = deferred<never>();
    get.mockImplementation(() => pending.promise);
    await act(async () => {
      render(
        <App
          runtime={{
            basePath: "/unit",
            authMode: "development",
            documentationUrl: "",
          }}
        />,
      );
    });
    await screen.findByRole("heading", { name: heading, level: 1 });
    if (route === "resources/new")
      expect(await screen.findByLabelText("Namespace ID")).toBeTruthy();
    expect(get.mock.calls.map(([path]) => path)).toEqual(
      route === "resources/new" ? [] : [endpoint],
    );
    expect(screen.queryByRole("alert")).toBeNull();
  },
);

it.each(["resources", "resources/private-resource"])(
  "does not mount %s without its metadata permission",
  async (route) => {
    history.replaceState(null, "", `/unit/${route}`);
    auth.mockImplementation(async (path: string) =>
      path === "options"
        ? { mode: "development", users: [] }
        : {
            access: {
              ...reader,
              permissions: [],
              availablePermissions: [],
              namespaceGrants: [],
            },
            expiresAt: Math.floor(Date.now() / 1000) + 600,
          },
    );
    render(
      <App
        runtime={{
          basePath: "/unit",
          authMode: "development",
          documentationUrl: "",
        }}
      />,
    );
    await screen.findByRole("alert");
    expect(get).not.toHaveBeenCalled();
  },
);

it.each([
  [["federation.discover"], ["catalog.read"], true],
  [["discovery.read"], ["catalog.read"], false],
  [["federation.discover"], ["sources.read"], false],
] as const)(
  "uses indexed discovery authority %j with scopes %j",
  async (permissions, scopes, allowed) => {
    history.replaceState(
      null,
      "",
      "/unit/indexed-record?identifier=urn:air:peer.example:tools:test&versionPresent=false",
    );
    auth.mockImplementation(async (path: string) =>
      path === "options"
        ? { mode: "development", users: [] }
        : {
            access: {
              ...reader,
              permissions: [...permissions],
              availablePermissions: [...permissions],
              namespaceGrants: [],
              scopeEnforcement: true,
              scopes: [...scopes],
            },
            expiresAt: Math.floor(Date.now() / 1000) + 600,
          },
    );
    get.mockImplementation(() => deferred<never>().promise);
    // Settle the lazy screen's read effect before inspecting exact requests.
    // Finding its heading alone can finish before that effect has run.
    await act(async () => {
      render(
        <App
          runtime={{
            basePath: "/unit",
            authMode: "development",
            documentationUrl: "",
          }}
        />,
      );
    });
    if (allowed) {
      await screen.findByRole("heading", {
        name: "External catalog record",
        level: 1,
      });
      expect(get.mock.calls.map(([path]) => path)).toEqual([
        "/api/v1/catalog/indexed-record",
      ]);
    } else {
      await screen.findByRole("alert");
      expect(get).not.toHaveBeenCalled();
    }
  },
);

it("opens Authoring navigation for a creation-only identity without inventory requests", async () => {
  history.replaceState(null, "", "/unit/resources");
  auth.mockImplementation(async (path: string) =>
    path === "options"
      ? { mode: "development", users: [] }
      : {
          access: {
            ...reader,
            permissions: ["resource.create", "namespace.read"],
            availablePermissions: ["resource.create", "namespace.read"],
            namespaceGrants: [],
            scopeEnforcement: true,
            scopes: ["catalog.write"],
          },
          expiresAt: Math.floor(Date.now() / 1000) + 600,
        },
  );
  render(
    <App
      runtime={{
        basePath: "/unit",
        authMode: "development",
        documentationUrl: "",
      }}
    />,
  );
  await screen.findByRole("heading", { name: "Authoring", level: 1 });
  expect(
    screen.getByRole("link", { name: "Create resource" }).getAttribute("href"),
  ).toBe("/unit/resources/new");
  expect(get).not.toHaveBeenCalled();
});
