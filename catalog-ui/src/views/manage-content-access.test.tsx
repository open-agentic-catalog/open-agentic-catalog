// @vitest-environment jsdom
import { expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import type { Access } from "../api/client";
import Resources from "./Resources";
import Versions from "./Versions";
import Reviews, { Release } from "./Reviews";
import Publication from "./Publication";
import { reader } from "../../tests/unit/fixtures";
import {
  evidence,
  listingRevision,
  release,
  submission,
} from "../../tests/unit/responses";
import {
  administrator,
  click,
  listing,
  readReplies,
  resource,
  version,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/resources");

const access: Access = {
  ...reader,
  permissions: [
    "resource.read",
    "resource.read_history",
    "resource.read_content",
    "publication.read",
  ],
  availablePermissions: [
    "resource.read",
    "resource.read_history",
    "resource.read_content",
    "publication.read",
  ],
};
const snapshot = {
  tenantId: "tenant-a",
  etag: '"snapshot"',
  createdAt: "2026-10-20T00:00:00Z",
  id: "snapshot-1",
  host: "catalog.example.com",
  generation: 1,
  state: "candidate",
  audience: "anonymous",
  exclusions: [],
  ardProfile: "ard-0.91-aa3e598",
  aiCatalogProfile: "ai-catalog-1.0-b062278",
  ard: { entries: [] },
  aiCatalog: { entries: [] },
};

function setup() {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [] },
    "/api/v1/catalog-listings/{listingId}": listing,
    "/api/v1/catalog-listings/{listingId}/revisions": { items: [] },
    "/api/v1/listing-releases/{releaseId}": release,
    "/api/v1/review-submissions/{submissionId}": submission,
    "/api/v1/review-submissions/{submissionId}/evidence": evidence,
    "/api/v1/listing-revisions/{revisionId}": listingRevision,
    "/api/v1/publication/hosts": { items: [] },
    "/api/v1/publication/snapshots": { items: [snapshot] },
    "/api/v1/publication/snapshots/{snapshotId}": snapshot,
  });
  return w;
}

const controls = [
  ["release", "Disposition"],
  ["review", "Decision"],
  ["new-version", "Version label"],
  ["snapshot", "Reason"],
  ["unpublish", "Reason"],
  ["delete-resource", "Reason"],
] as const;
it.each(
  controls.flatMap(([view, label]) => [
    { view, label, variant: "narrow reader", access },
    {
      view,
      label,
      variant: "missing OAuth write/review/publish ceiling",
      access: {
        ...administrator,
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      } satisfies Access,
    },
  ]),
)(
  "omits initial $view mutation fields without editing authority for $variant",
  async ({ view, label, access }) => {
    const w = setup();
    if (view === "release")
      render(<Release {...w.props} id={release.id} access={access} />);
    else if (view === "review") {
      render(<Reviews {...w.props} id={submission.id} access={access} />);
      await screen.findByRole("button", { name: "Inspect submitted evidence" });
      click("Inspect submitted evidence");
      await screen.findByRole("heading", {
        name: "Submitted evidence",
      });
    } else if (view === "new-version") {
      history.replaceState(
        null,
        "",
        `/unit/versions/new?resource=${resource.id}`,
      );
      render(<Versions {...w.props} id="new" access={access} />);
    } else if (view === "delete-resource")
      render(<Resources {...w.props} id={resource.id} access={access} />);
    else {
      history.replaceState(null, "", `/unit/publication?host=${snapshot.host}`);
      render(
        <Publication
          {...w.props}
          id={view === "snapshot" ? snapshot.id : ""}
          access={access}
        />,
      );
    }
    const loaded =
      view === "release"
        ? "Release published"
        : view === "new-version"
          ? "Create version"
          : view === "snapshot"
            ? "Generation 1: candidate"
            : view === "unpublish"
              ? "Build candidate snapshot"
              : view === "delete-resource"
                ? "Delete unused resource"
                : "Submitted evidence";
    await screen.findByRole("heading", { name: loaded, hidden: true });
    expect(screen.queryByLabelText(label!)).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
  },
);

it("withholds a replacement-release link when its Namespace is unknown to a scoped history reader", async () => {
  const w = setup();
  readReplies(w.get, {
    "/api/v1/listing-releases/{releaseId}": {
      ...release,
      replacementReleaseId: "another-release",
    },
    "/api/v1/catalog-listings/{listingId}": listing,
  });
  render(
    <Release
      {...w.props}
      id={release.id}
      access={{
        ...access,
        permissions: ["resource.read"],
        namespaceGrants: [
          {
            namespaceId: resource.namespaceId,
            permissions: ["resource.read_history"],
          },
        ],
      }}
    />,
  );
  await screen.findByText("another-release");
  expect(screen.queryByRole("link", { name: "another-release" })).toBeNull();
  expect(
    screen.getByRole("link", { name: "Approved submission" }),
  ).toBeTruthy();
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/listing-releases/{releaseId}",
    "/api/v1/catalog-listings/{listingId}",
  ]);
});

it("retains history IDs while independent metadata access changes", async () => {
  const w = setup();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [version] },
    "/api/v1/resources/{resourceId}/listings": { items: [listing] },
  });
  const rendered = render(
    <Resources {...w.props} id={resource.id} access={access} />,
  );
  await screen.findByRole("link", { name: "Version 1.0.0" });
  rendered.rerender(
    <Resources
      {...w.props}
      id={resource.id}
      access={{
        ...access,
        permissions: ["resource.read_history"],
        namespaceGrants: [
          { namespaceId: "elsewhere", permissions: ["resource.read"] },
        ],
      }}
    />,
  );
  expect(screen.queryByRole("link", { name: "Version 1.0.0" })).toBeNull();
  expect(screen.getByText(version.id)).toBeTruthy();
  click("Listings");
  await screen.findByText(listing.id, { selector: "code" });
  expect(
    screen.queryByRole("link", { name: `Listing ${listing.id}` }),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["none", "another-namespace", "exact-namespace"])(
  "offers Namespace navigation only with its own exact permission (%s)",
  async (grant) => {
    const w = setup();
    render(
      <Resources
        {...w.props}
        id={resource.id}
        access={{
          ...access,
          availablePermissions: [
            ...access.availablePermissions,
            "namespace.read",
          ],
          namespaceGrants:
            grant === "none"
              ? []
              : [
                  {
                    namespaceId:
                      grant === "exact-namespace"
                        ? resource.namespaceId
                        : "elsewhere",
                    permissions: ["namespace.read"],
                  },
                ],
        }}
      />,
    );
    await screen.findByRole("heading", { name: resource.canonicalIdentifier });
    const link = screen.queryByRole("link", { name: "Namespace" });
    if (grant === "exact-namespace")
      expect(link?.getAttribute("href")).toBe(
        `/unit/namespaces/${resource.namespaceId}`,
      );
    else {
      expect(link).toBeNull();
      expect(screen.getAllByText(resource.namespaceId).length).toBeGreaterThan(
        0,
      );
    }
    expect(
      w.get.mock.calls.some(([path]) => String(path).includes("/namespaces")),
    ).toBe(false);
  },
);
