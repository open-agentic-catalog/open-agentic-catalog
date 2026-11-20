// @vitest-environment jsdom
import { render, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import {
  click,
  readReplies,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
import { ReleaseRelationships } from "./ReleaseRelationships";

screenLifecycle("/unit/catalog");
const path = "/api/v1/listing-releases/{releaseId}/relationships";
const exact = {
  type: "depends_on" as const,
  targetIdentifier: "opaque:renderer",
  targetVersion: "1.0",
};
const potential = {
  type: "depends_on" as const,
  targetIdentifier: "opaque:renderer",
};
const reference = {
  releaseId: "release-2",
  listingRevisionId: "revision-2",
  displayName: "Renderer",
  version: "2.0",
};

function show(view: "incoming" | "outgoing" | "dependencies", page: unknown) {
  const w = screenWorkspace();
  const routes: Record<string, unknown> = { [path]: page };
  readReplies(w.get, routes);
  render(
    <ReleaseRelationships
      client={w.props.client}
      basePath="/unit"
      onFailure={w.props.onFailure}
      view={view}
      releaseId="release-1"
      declarations={[exact, potential]}
    />,
  );
  return { w, routes };
}

it("keeps an unavailable exact declaration as text and links the unversioned target at its current version", async () => {
  const { w, routes } = show("outgoing", {
    items: [
      { declaration: exact },
      { declaration: potential, related: reference },
    ],
  });
  expect(screen.getByText(/1\.0/)).toBeTruthy();
  expect(w.get).not.toHaveBeenCalled();
  click("Check current destinations");
  expect((await screen.findByRole("link")).getAttribute("href")).toBe(
    "/unit/catalog/release-2",
  );
  expect(screen.getByRole("link").textContent).toBe("Renderer (2.0)");
  routes[path] = {
    items: [{ declaration: exact }, { declaration: potential }],
  };
  click("Check current destinations");
  await waitFor(() => expect(screen.queryByRole("link")).toBeNull());
  expect(screen.getAllByText("opaque:renderer")).toHaveLength(2);
});

it("pages incoming declarations without fetching a second page until requested", async () => {
  const { w, routes } = show("incoming", {
    items: [{ declaration: exact, related: reference }],
    nextCursor: "opaque+/=",
  });
  click("Find referring Listings");
  expect((await screen.findByRole("link")).textContent).toBe("Renderer (2.0)");
  expect(w.get).toHaveBeenCalledTimes(1);
  routes[path] = { items: [] };
  click("Next page");
  await screen.findByText("No currently readable declarations match.");
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { view: "incoming", cursor: "opaque+/=", limit: 20 } },
  });
  click("First page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
});

it("distinguishes a potentially affected resource from an exact version in the withdrawal view", async () => {
  show("dependencies", {
    items: [
      { declaration: exact, related: reference },
      { declaration: potential, related: reference },
    ],
  });
  click("Check declared dependencies");
  await screen.findByText(/exact version/);
  expect(screen.getByText(/\(unversioned\)/)).toBeTruthy();
  expect(screen.getAllByRole("link")).toHaveLength(2);
});

it.each([
  [
    "different identifier",
    {
      items: [
        {
          declaration: { ...exact, targetIdentifier: "other" },
          related: reference,
        },
        { declaration: potential },
      ],
    },
  ],
  [
    "different relation type",
    {
      items: [
        { declaration: { ...exact, type: "supersedes" } },
        { declaration: potential },
      ],
    },
  ],
  [
    "different declared version",
    {
      items: [
        { declaration: { ...exact, targetVersion: "2.0" } },
        { declaration: potential },
      ],
    },
  ],
  [
    "wrong destination version",
    {
      items: [
        { declaration: exact, related: reference },
        { declaration: potential },
      ],
    },
  ],
  [
    "extra page",
    {
      items: [{ declaration: exact }, { declaration: potential }],
      nextCursor: "unexpected",
    },
  ],
  ["missing declaration", { items: [{ declaration: potential }] }],
  [
    "invalid release link",
    {
      items: [
        { declaration: exact },
        {
          declaration: potential,
          related: { ...reference, releaseId: "//other.example/" },
        },
      ],
    },
  ],
])(
  "discards outgoing navigation with %s and preserves authored text",
  async (_reason, reply) => {
    show("outgoing", reply);
    click("Check current destinations");
    await screen.findByRole("alert");
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getAllByText("opaque:renderer")).toHaveLength(2);
  },
);

it.each([
  [
    "unresolved incoming edge",
    "incoming" as const,
    { items: [{ declaration: potential }] },
  ],
  [
    "unsupported dependency type",
    "dependencies" as const,
    {
      items: [
        {
          declaration: { ...potential, type: "recommended_with" },
          related: reference,
        },
      ],
    },
  ],
  [
    "over-limit incoming page",
    "incoming" as const,
    { items: Array(21).fill({ declaration: exact, related: reference }) },
  ],
])(
  "rejects %s without showing an empty result",
  async (_reason, view, reply) => {
    show(view, reply);
    click(
      view === "incoming"
        ? "Find referring Listings"
        : "Check declared dependencies",
    );
    await screen.findByRole("alert");
    expect(
      screen.queryByText("No currently readable declarations match."),
    ).toBeNull();
  },
);
