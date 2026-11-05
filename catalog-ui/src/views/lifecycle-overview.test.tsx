// @vitest-environment jsdom
// Lifecycle reads selected exact contexts; one loaded page cannot prove absence.
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { LifecycleOverview } from "./LifecycleOverview";
import { workspace, reply } from "../../tests/unit/fixtures";
import {
  administrator,
  resource,
  version,
  listing,
} from "../../tests/unit/screens";
import {
  evidence,
  listingRevision,
  versionRevision,
  submission,
  release,
} from "../../tests/unit/responses";
import { testAccess } from "../../tests/unit/access-fixtures";
import { snapshot } from "../../tests/unit/publication-fixtures";
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
function click(name: string) {
  fireEvent.click(screen.getByRole("button", { name }));
}
function choose(name: string, value: string) {
  fireEvent.change(screen.getByLabelText(name), { target: { value } });
}
function fixture() {
  const f = workspace();
  f.props.access = administrator;
  f.get.mockImplementation(async (path) => {
    if (path === "/api/v1/resources/{resourceId}/versions")
      return reply({
        items: [version, { ...version, id: "version-2", version: "2.0.0" }],
      });
    if (path === "/api/v1/resources/{resourceId}/listings")
      return reply({ items: [listing] });
    if (path === "/api/v1/catalog-listings/{listingId}/revisions")
      return reply({
        items: [
          { id: listingRevision.id, revisionNumber: 1, createdBy: "author" },
        ],
      });
    if (path === "/api/v1/resource-versions/{versionId}")
      return reply({
        ...version,
        candidateRevisionId: "new-version-candidate",
      });
    if (path === "/api/v1/catalog-listings/{listingId}")
      return reply({
        ...listing,
        candidateRevisionId: "new-listing-candidate",
      });
    if (path === "/api/v1/listing-revisions/{revisionId}")
      return reply(listingRevision);
    if (path === "/api/v1/version-revisions/{revisionId}")
      return reply({
        namespaceId: "ns-1",
        revision: versionRevision,
        evidence,
      });
    if (path === "/api/v1/review-submissions")
      return reply({
        nextCursor: "review-next",
        items: [
          {
            ...submission,
            tenantId: "tenant-a",
            state: "approved",
            decisions: [
              {
                action: "approve",
                actor: "independent-reviewer",
                at: "2026-01-01T00:00:00Z",
                reason: "Correct exact evidence",
              },
            ],
          },
        ],
      });
    if (path === "/api/v1/catalog-listings/{listingId}/releases")
      return reply({
        nextCursor: "release-next",
        items: [
          "published",
          "deprecated",
          "retired",
          "superseded",
          "revoked",
        ].map((state) => ({
          ...release,
          tenantId: "tenant-a",
          id: state,
          state,
          replacementReleaseId:
            state === "superseded" ? "replacement" : undefined,
        })),
      });
    if (path === "/api/v1/publication/snapshots")
      return reply({
        items: [
          {
            ...snapshot,
            id: "snapshot-a",
            host: "catalog.example",
            audience: "anonymous",
            state: "active",
            generation: 3,
          },
          {
            ...snapshot,
            id: "snapshot-b",
            host: "catalog.example",
            audience: "authenticated",
            state: "candidate",
            generation: 4,
          },
        ],
      });
    throw Error("Unexpected lifecycle read");
  });
  return {
    ...f,
    props: {
      ...f.props,
      resourceId: resource.id,
      namespaceId: resource.namespaceId,
    },
  };
}
async function select(listingKnown = false) {
  click("Choose lifecycle context");
  await screen.findByRole("option", { name: "1.0.0 / version-1" });
  choose("Lifecycle Version", version.id);
  if (!listingKnown) {
    await screen.findByRole("option", { name: listing.id });
    choose("Lifecycle Listing", listing.id);
  }
  await screen.findByRole("option", {
    name: `Revision 1 / ${listingRevision.id}`,
  });
  choose("Lifecycle Listing revision", listingRevision.id);
  click("Inspect selected lifecycle");
}
it("starts without reads and inspects only the chosen Version and Listing revision", async () => {
  const f = fixture();
  render(<LifecycleOverview {...f.props} />);
  expect(f.get).not.toHaveBeenCalled();
  expect(screen.getByText("Validation: not inspected.")).toBeTruthy();
  await select();
  await screen.findByRole("heading", { name: "Selected Version 1.0.0" });
  expect(screen.queryByText("Validation: not inspected.")).toBeNull();
  expect(screen.getByText(/historical Listing revision/)).toBeTruthy();
  await screen.findByText("Approved");
  for (const state of [
    "published",
    "deprecated",
    "retired",
    "superseded",
    "revoked",
  ])
    expect(
      screen.getByRole("link", { name: `${state} / ${state}` }),
    ).toBeTruthy();
  expect(screen.getByText("replacement")).toBeTruthy();
  expect(screen.getByText(/not proof of absence/)).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Prepare listing metadata" })
      .getAttribute("href"),
  ).toBe("/unit/resources/resource-1#listings");
  const pagers = screen.getAllByRole("button", { name: "Next page" });
  fireEvent.click(pagers[0]!);
  fireEvent.click(pagers[1]!);
  await screen.findByText("Correct exact evidence");
  expect(f.get).toHaveBeenCalledWith(
    "/api/v1/review-submissions",
    expect.objectContaining({
      params: {
        query: { listingId: listing.id, limit: 25, cursor: "review-next" },
      },
    }),
  );
  choose("Lifecycle discovery host", "CATALOG.EXAMPLE");
  choose("Lifecycle discovery audience", "authenticated");
  click("Inspect chosen discovery host");
  await screen.findByRole("link", { name: "Snapshot snapshot-b" });
  expect(
    screen.queryByRole("link", { name: "Snapshot snapshot-a" }),
  ).toBeNull();
  expect(screen.getByText(/Observed host/)).toBeTruthy();
  expect(screen.getByText(/does not by itself prove inclusion/)).toBeTruthy();
});
it("uses a known Listing, distinguishes sealed Versions and current candidates", async () => {
  const f = fixture();
  const original = f.get.getMockImplementation()!;
  f.get.mockImplementation(async (p, o) =>
    p === "/api/v1/resource-versions/{versionId}"
      ? reply({ ...version, sealedRevisionId: versionRevision.id })
      : p === "/api/v1/catalog-listings/{listingId}"
        ? reply({ ...listing, candidateRevisionId: listingRevision.id })
        : original(p, o),
  );
  render(
    <LifecycleOverview
      {...f.props}
      listing={{
        ...listing,
        currentReleaseId: "known-release",
        candidateRevisionId: listingRevision.id,
      }}
    />,
  );
  expect(screen.getByText("known-release")).toBeTruthy();
  await select(true);
  await screen.findByText("This is the current Listing candidate.");
  expect(screen.getByText(/sealed; use a new version/)).toBeTruthy();
  expect(
    screen.queryByRole("link", { name: "Correct Version draft" }),
  ).toBeNull();
  expect(
    f.get.mock.calls.some(
      ([p]) => p === "/api/v1/resources/{resourceId}/listings",
    ),
  ).toBe(false);
});
it.each(["versions", "listings"])(
  "rejects a foreign Resource in lifecycle %s choices before inspecting it",
  async (collection) => {
    const f = fixture();
    const original = f.get.getMockImplementation()!;
    f.get.mockImplementation(async (path, options) =>
      path === `/api/v1/resources/{resourceId}/${collection}`
        ? reply({
            items: [
              {
                ...(collection === "versions" ? version : listing),
                resourceId: "another-resource",
              },
            ],
          })
        : original(path, options),
    );
    render(<LifecycleOverview {...f.props} />);
    click("Choose lifecycle context");
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Selected lifecycle context")).toBeNull();
    expect(
      f.get.mock.calls.some(
        ([path]) =>
          path === "/api/v1/resource-versions/{versionId}" ||
          path === "/api/v1/catalog-listings/{listingId}" ||
          path === "/api/v1/version-revisions/{revisionId}",
      ),
    ).toBe(false);
  },
);
it.each([
  "version",
  "listing",
  "revision",
  "bundle",
  "review",
  "release",
  "snapshot",
])(
  "rejects a foreign %s binding without claiming a successful observation",
  async (kind) => {
    const f = fixture();
    const original = f.get.getMockImplementation()!;
    f.get.mockImplementation(async (p, o) => {
      const r = (await original(p, o)) as {
        data: Record<string, unknown>;
        response: Response;
      };
      if (kind === "version" && p === "/api/v1/resource-versions/{versionId}")
        return { ...r, data: { ...r.data, resourceId: "foreign" } };
      if (kind === "listing" && p === "/api/v1/catalog-listings/{listingId}")
        return { ...r, data: { ...r.data, namespaceId: "foreign" } };
      if (kind === "revision" && p === "/api/v1/listing-revisions/{revisionId}")
        return { ...r, data: { ...r.data, listingId: "foreign" } };
      if (kind === "bundle" && p === "/api/v1/version-revisions/{revisionId}")
        return { ...r, data: { ...r.data, namespaceId: "foreign" } };
      if (kind === "review" && p === "/api/v1/review-submissions")
        return reply({ items: [{ ...submission, tenantId: "foreign" }] });
      if (
        kind === "release" &&
        p === "/api/v1/catalog-listings/{listingId}/releases"
      )
        return reply({ items: [{ ...release, tenantId: "foreign" }] });
      if (kind === "snapshot" && p === "/api/v1/publication/snapshots")
        return reply({
          items: [
            {
              id: "wrong",
              host: "foreign.example",
              audience: "anonymous",
              state: "active",
              generation: 1,
            },
          ],
        });
      return r;
    });
    render(<LifecycleOverview {...f.props} />);
    await select();
    if (kind === "snapshot") {
      await screen.findByRole("heading", { name: "Selected Version 1.0.0" });
      choose("Lifecycle discovery host", "catalog.example");
      click("Inspect chosen discovery host");
    }
    await screen.findByRole("alert");
    if (["version", "listing", "revision", "bundle"].includes(kind))
      expect(screen.queryByLabelText("Selected lifecycle context")).toBeNull();
    if (kind === "snapshot")
      expect(screen.queryByText(/Observed host/)).toBeNull();
  },
);
it("does not infer content or publication permission from history access and clears stale context", async () => {
  const f = fixture();
  const view = render(<LifecycleOverview {...f.props} />);
  await select();
  await screen.findByRole("heading", { name: "Selected Version 1.0.0" });
  view.rerender(
    <LifecycleOverview
      {...f.props}
      access={{ ...administrator, ...testAccess([]) }}
    />,
  );
  expect(screen.queryByLabelText("Selected lifecycle context")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Choose lifecycle context" }),
  ).toBeNull();
  const history: typeof administrator = {
    ...administrator,
    ...testAccess(["reader"]),
    permissions: ["resource.read_history"],
    availablePermissions: ["resource.read_history"],
  };
  view.rerender(<LifecycleOverview {...f.props} access={history} />);
  click("Choose lifecycle context");
  await screen.findByRole("option", { name: "1.0.0 / version-1" });
  expect(
    screen.getByText(
      /Inspection of documents and validation additionally requires/,
    ),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Inspect selected lifecycle" }),
  ).toHaveProperty("disabled", true);
});

it("rereads an unchanged lifecycle and host selection after mutable server state changes", async () => {
  const f = fixture();
  const original = f.get.getMockImplementation()!;
  let changed = false;
  f.get.mockImplementation(async (p, o) => {
    if (changed && p === "/api/v1/publication/snapshots")
      return reply({
        items: [
          {
            ...snapshot,
            id: "refreshed-snapshot",
            host: "catalog.example",
            audience: "authenticated",
            state: "active",
            generation: 5,
          },
        ],
      });
    if (changed && p === "/api/v1/resource-versions/{versionId}")
      return reply({
        ...version,
        candidateRevisionId: "refreshed-version-candidate",
      });
    return original(p, o);
  });
  render(<LifecycleOverview {...f.props} />);
  await select();
  await screen.findByRole("heading", { name: "Selected Version 1.0.0" });
  choose("Lifecycle discovery host", "catalog.example");
  choose("Lifecycle discovery audience", "authenticated");
  click("Inspect chosen discovery host");
  await screen.findByRole("link", { name: "Snapshot snapshot-b" });
  changed = true;
  click("Inspect chosen discovery host");
  await screen.findByRole("link", { name: "Snapshot refreshed-snapshot" });
  click("Inspect selected lifecycle");
  await screen.findByText("refreshed-version-candidate");
  expect(
    screen.queryByRole("link", { name: "Snapshot refreshed-snapshot" }),
  ).toBeNull();
});
