// @vitest-environment jsdom
// Transport failures retain explicit cursor-free recovery without replaying a command.
import { expect, it } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import Authorization from "./Authorization";
import Access from "./Access";
import Jobs from "./Jobs";
import Audit from "./Audit";
import Webhooks from "./Webhooks";
import Feedback, { OwnFeedback } from "./Feedback";
import { FederationSearch } from "./FederationSearch";
import Explore from "./Explore";
import Resources from "./Resources";
import Reviews from "./Reviews";
import Namespaces from "./Namespaces";
import Publishers from "./Publishers";
import Members from "./Members";
import Publication from "./Publication";
import { deferred } from "../../tests/unit/fixtures";
import { group } from "../../tests/unit/policy-fixtures";
import {
  administrator,
  change,
  click,
  etagReply,
  namespace,
  publisher,
  resource,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
import { capabilities, submission } from "../../tests/unit/responses";
import { APIError } from "../api/client";
import { snapshot } from "../../tests/unit/publication-fixtures";

screenLifecycle("/unit/access-roles");
const cases = [
  ["roles", "/api/v1/access/roles", "access-roles", ""],
  ["groups", "/api/v1/access/groups", "access-groups", ""],
  ["assignments", "/api/v1/access/assignments", "access-assignments", ""],
  [
    "group members",
    "/api/v1/access/groups/{groupId}/members",
    "access-groups",
    group.id,
  ],
  ["assignment sources", "/api/v1/access/self", "access", ""],
  ["Jobs", "/api/v1/jobs", "jobs", ""],
  ["audit", "/api/v1/audit-events", "audit", ""],
  ["webhooks", "/api/v1/webhooks", "webhooks", ""],
  ["own feedback", "/api/v1/feedback", "my-feedback", ""],
  ["feedback resource choices", "/api/v1/resources", "feedback", ""],
  [
    "indexed diagnostics",
    "/api/v1/observations:search",
    "federation-search",
    "",
  ],
] as const;
it.each(cases)(
  "recovers failed %s continuation with First page and Reload",
  async (_label, endpoint, route, id) => {
    history.replaceState(null, "", `/unit/${route}`);
    const w = screenWorkspace();
    const requests: unknown[] = [];
    let failFirst = true;
    const transport = async (path: unknown, options: unknown) => {
      if (path === endpoint) {
        if (failFirst) {
          failFirst = false;
          throw new APIError(503, "unavailable", "initial-read");
        }
        const request = options as {
          params?: { query?: { cursor?: string } };
          body?: { cursor?: string };
        };
        requests.push(request);
        if (request.params?.query?.cursor || request.body?.cursor)
          throw new APIError(400, "invalid_cursor", "policy-changed");
        return etagReply({
          items: [],
          authorizationRevision: 3,
          nextCursor: "expired-after-policy-change",
        });
      }
      if (path === "/api/v1/access/groups/{groupId}") return etagReply(group);
      if (path === "/api/v1/capabilities")
        return etagReply({
          ...capabilities,
          optionalCapabilities: {
            ...capabilities.optionalCapabilities,
            webhooks: "enabled",
          },
        });
      if (path === "/api/v1/webhooks/reconciliation")
        return etagReply({
          pendingEvents: 0,
          pendingEventLimit: 1000,
          capacityLossCount: 0,
          deliveryLossCount: 0,
        });
      if (
        path === "/api/v1/feedback/inbox" ||
        path === "/api/v1/access/assignments" ||
        path === "/api/v1/access/groups" ||
        path === "/api/v1/access/roles" ||
        path === "/api/v1/memberships"
      )
        return etagReply({ items: [], authorizationRevision: 3 });
      throw new Error(`Unexpected fixture route ${String(path)}`);
    };
    w.get.mockImplementation(transport);
    if (route === "federation-search") w.post.mockImplementation(transport);
    const props = { ...w.props, access: administrator, id };
    if (route.startsWith("access-")) render(<Authorization {...props} />);
    else if (route === "access") render(<Access {...props} />);
    else if (route === "jobs") render(<Jobs {...props} />);
    else if (route === "audit") {
      render(<Audit {...props} />);
      click("Search audit events");
    } else if (route === "webhooks") render(<Webhooks {...props} />);
    else if (route === "feedback") render(<Feedback {...props} />);
    else if (route === "my-feedback")
      render(
        <OwnFeedback
          {...props}
          access={{
            ...administrator,
            permissions: ["feedback.own"],
            availablePermissions: ["feedback.own"],
          }}
        />,
      );
    else {
      render(<FederationSearch {...props} />);
      change("Query", "bounded diagnostic");
      click("Search indexed observations");
    }
    await screen.findByRole("alert");
    click("Reload");
    await screen.findByRole("button", { name: "Next page" });
    const pager = screen
      .getByRole("button", { name: "Next page" })
      .closest(".pagination");
    click("Next page");
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
    expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
      "disabled",
      false,
    );
    click("First page");
    await screen.findByRole("button", { name: "Next page" });
    expect(document.activeElement).toBe(pager);
    expect(requests[2]).not.toHaveProperty("params.query.cursor");
    expect(requests[2]).not.toHaveProperty("body.cursor");
    click("Next page");
    await screen.findByRole("alert");
    const refreshLabel =
      route === "jobs" || route === "webhooks"
        ? "Refresh"
        : route === "access-roles"
          ? "Refresh roles"
          : route === "access-groups" && !id
            ? "Refresh groups"
            : route === "access-assignments"
              ? "Refresh assignments"
              : "Reload";
    click(refreshLabel);
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(requests[4]).toMatchObject({
      ...(route === "federation-search"
        ? { body: (requests[0] as { body: unknown }).body }
        : { params: (requests[0] as { params: unknown }).params }),
    });
    expect(requests[4]).not.toHaveProperty("params.query.cursor");
    expect(requests[4]).not.toHaveProperty("body.cursor");
    if (
      route === "webhooks" ||
      route === "access-roles" ||
      (route === "access-groups" && !id) ||
      route === "access-assignments"
    ) {
      click("Next page");
      await screen.findByRole("alert");
      click("Reload");
      await screen.findByRole("button", { name: "Next page" });
      expect(requests.at(-1)).not.toHaveProperty("params.query.cursor");
    }
    if (route === "my-feedback") {
      change("Feedback category", "documentation");
      await waitFor(() =>
        expect(requests.at(-1)).toMatchObject({
          params: { query: { category: "documentation" } },
        }),
      );
      await screen.findByRole("button", { name: "Next page" });
      const beforeRefresh = requests.length;
      click("Refresh feedback");
      await waitFor(() => expect(requests).toHaveLength(beforeRefresh + 1));
      await screen.findByRole("button", { name: "Next page" });
      click("Next page");
      await screen.findByRole("alert");
      click("Refresh feedback");
      await screen.findByRole("button", { name: "Next page" });
      expect(requests.at(-1)).toMatchObject({
        params: { query: { category: "documentation" } },
      });
      expect(requests.at(-1)).not.toHaveProperty("params.query.cursor");
      expect(screen.queryByRole("alert")).toBeNull();
    }
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
    if (route !== "federation-search") expect(w.post).not.toHaveBeenCalled();
  },
);

const inventories = [
  {
    name: "Explore browse",
    View: Explore,
    endpoint: "/api/v1/catalog/resources",
    route: "explore",
    item: {
      release: { id: "release-1", state: "published" },
      listingRevision: { displayName: "Local skill" },
      resource: { kind: "skill" },
      version: { version: "1.0.0" },
    },
    row: "Local skill",
    refresh: "Refresh browse results",
    criteria: {
      kind: "skill",
      visibility: "tenant",
      namespaceId: namespace.id,
    },
  },
  {
    name: "Resources",
    View: Resources,
    endpoint: "/api/v1/resources",
    route: "resources",
    item: { ...resource, kind: "plugin" },
    row: resource.canonicalIdentifier,
    refresh: "Refresh",
    criteria: { kind: "plugin" },
  },
  {
    name: "Reviews",
    View: Reviews,
    endpoint: "/api/v1/review-submissions",
    route: "reviews?listing=00000000-0000-7000-8000-000000000001",
    item: { ...submission, state: "approved" },
    row: submission.id,
    refresh: "Refresh",
    criteria: {
      listingId: "00000000-0000-7000-8000-000000000001",
      state: "approved",
    },
  },
  {
    name: "Namespaces",
    View: Namespaces,
    endpoint: "/api/v1/namespaces",
    route: "namespaces",
    item: namespace,
    row: namespace.displayName,
    refresh: "Refresh",
    criteria: {},
  },
  {
    name: "Publishers",
    View: Publishers,
    endpoint: "/api/v1/publishers",
    route: "publishers",
    item: publisher,
    row: publisher.displayName,
    refresh: "Refresh",
    criteria: {},
  },
  {
    name: "Members",
    View: Members,
    endpoint: "/api/v1/memberships",
    route: "members",
    item: {
      id: "member-1",
      realmId: "realm-a",
      subject: "unit-reader",
      actorType: "human",
      state: "active",
    },
    row: "unit-reader",
    refresh: "Refresh",
    criteria: {},
  },
  {
    name: "Publication",
    View: Publication,
    endpoint: "/api/v1/publication/snapshots",
    route: "publication?host=catalog.example.com",
    item: {
      ...snapshot,
      id: "snapshot-1",
      generation: 1,
      state: "candidate",
      audience: "anonymous",
    },
    row: "Generation 1",
    refresh: "",
    criteria: { host: "catalog.example.com" },
  },
] as const;

it.each(inventories)(
  "$name recovers without stale rows or lost criteria and refreshes successful pages in place",
  async ({ name, View, endpoint, route, item, row, refresh, criteria }) => {
    history.replaceState(null, "", `/unit/${route}`);
    const w = screenWorkspace();
    const requests: { params: { query: { cursor?: string } } }[] = [];
    let initial = true;
    let pending: ReturnType<typeof deferred<never>> | undefined;
    w.get.mockImplementation(async (path, options) => {
      if (path === endpoint) {
        const request = options as { params: { query: { cursor?: string } } };
        requests.push(request);
        if (initial) {
          initial = false;
          throw new APIError(503, "unavailable", "initial-read");
        }
        if (request.params.query.cursor) {
          if (pending) return pending.promise;
          return etagReply({ items: [] });
        }
        return etagReply({ items: [item], nextCursor: "later-page" });
      }
      if (path === "/api/v1/publication/hosts") return etagReply({ items: [] });
      if (path === "/api/v1/namespaces")
        return etagReply({ items: [namespace] });
      throw new Error(`Unexpected fixture route ${String(path)}`);
    });
    render(<View {...w.props} access={administrator} />);
    await screen.findByRole("alert");
    click("Reload");
    await screen.findByRole("link", { name: row });
    expect(requests.at(-1)).not.toHaveProperty("params.query.cursor");
    if (name === "Explore browse") {
      click("Load namespaces");
      await screen.findByRole("option", {
        name: namespace.displayName,
      });
      change("Local namespace", namespace.id);
      change("Kind", "skill");
      change("Visibility", "tenant");
    } else if (name === "Resources") change("Resource kind", "plugin");
    else if (name === "Reviews") {
      change("Review state", "approved");
      click("Apply review filters");
    }
    await screen.findByRole("link", { name: row });
    const query = { limit: 25, ...criteria };
    await waitFor(() => expect(requests.at(-1)?.params.query).toEqual(query));
    const pager = screen
      .getByRole("button", { name: "Next page" })
      .closest(".pagination");
    for (const recovery of [
      "First page",
      "Reload",
      ...(refresh ? [refresh] : []),
    ]) {
      pending = deferred<never>();
      click("Next page");
      await screen.findByText("Loading...");
      expect(screen.queryByRole("link", { name: row })).toBeNull();
      expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
        "disabled",
        true,
      );
      expect(document.activeElement).toBe(pager);
      if (refresh)
        expect(screen.getByRole("button", { name: refresh })).toHaveProperty(
          "disabled",
          true,
        );
      await act(async () =>
        pending!.reject(new APIError(400, "invalid_cursor", "policy-changed")),
      );
      pending = undefined;
      await screen.findByRole("alert");
      expect(screen.queryByRole("link", { name: row })).toBeNull();
      expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
      expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
        "disabled",
        false,
      );
      click(recovery);
      await screen.findByRole("link", { name: row });
      expect(requests.at(-1)?.params.query).toEqual(query);
      expect(screen.queryByRole("alert")).toBeNull();
      if (recovery === "First page") expect(document.activeElement).toBe(pager);
    }
    // A successful later page must stay selected on ordinary Refresh.
    click("Next page");
    await waitFor(() => expect(screen.queryByText("Loading...")).toBeNull());
    expect(screen.queryByRole("link", { name: row })).toBeNull();
    expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
      "disabled",
      false,
    );
    if (refresh) {
      const count = requests.length;
      click(refresh);
      await waitFor(() => expect(requests).toHaveLength(count + 1));
      expect(requests.at(-1)?.params.query).toEqual({
        ...query,
        cursor: "later-page",
      });
      await waitFor(() =>
        expect(screen.getByRole("button", { name: refresh })).toHaveProperty(
          "disabled",
          false,
        ),
      );
      expect(screen.queryByRole("alert")).toBeNull();
    }
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
  },
);
