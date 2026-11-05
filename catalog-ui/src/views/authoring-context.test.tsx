// @vitest-environment jsdom
import { it, expect, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import ListingPreparation, {
  SavedListingAuthoring,
  type ListingPreparationHandle,
} from "./ListingPreparation";
import VersionSource from "./VersionSource";
import Versions, { VersionRevision } from "./Versions";
import { ActionForm } from "./authoring";
import { ListingForm, type ListingFormHandle } from "./Listings";
import { AuthoringWorkspace } from "./AuthoringWorkspace";
import { newRepresentation } from "./representations";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  listing,
  namespace,
  publisher,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  version,
} from "../../tests/unit/screens";
import { bundle, native } from "../../tests/unit/authoring-fixtures";
import { listingRevision } from "../../tests/unit/responses";

screenLifecycle("/unit/versions/new?resource=resource-1");
const sourceBundle = {
  namespaceId: namespace.id,
  revision: bundle.revision,
  evidence: bundle.evidence,
};
const contextRoutes = {
  "/api/v1/resources/{resourceId}/listings": { items: [listing] },
  "/api/v1/catalog-listings/{listingId}": {
    ...listing,
    candidateRevisionId: listingRevision.id,
  },
  "/api/v1/listing-revisions/{revisionId}": listingRevision,
  "/api/v1/publishers": { items: [publisher] },
};
const newRevision = {
  listing: { ...listing, revision: 2, candidateRevisionId: listingRevision.id },
  revision: { ...listingRevision, versionRevisionId: bundle.revision.id },
};

it("preserves an existing candidate and requires explicit replacement of nonempty Listing values", async () => {
  const w = screenWorkspace();
  const handle = { current: undefined as ListingPreparationHandle | undefined };
  const initial = {
    ...listingRevision,
    tags: ["one, two"],
    categories: ["tools"],
    representativeQueries: ["Find an Agent"],
    license: "Apache-2.0",
    extensions: { "https://example.test/extra": { keep: true } },
  };
  readReplies(w.get, {
    ...contextRoutes,
    "/api/v1/listing-revisions/{revisionId}": initial,
  });
  render(
    <ListingPreparation
      {...w.props}
      access={administrator}
      resource={resource}
      namespaceId={namespace.id}
      known={bundle}
      handle={handle}
    />,
  );
  await screen.findByLabelText("Display name");
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Helper",
  );
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Use validated content name and description");
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Helper",
  );
  confirmation.mockReturnValue(true);
  click("Use validated content name and description");
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Order Agent",
  );
  await waitFor(() => expect(handle.current).toBeDefined());
  const plan = handle.current!.read();
  expect(plan).toMatchObject({
    listing: { id: listing.id },
    etag: '"revision-1"',
    body: {
      displayName: "Order Agent",
      description: "Explain an order.",
      tags: ["one, two"],
      categories: ["tools"],
      representativeQueries: ["Find an Agent"],
      license: "Apache-2.0",
      extensions: initial.extensions,
    },
  });
  expect(w.post).not.toHaveBeenCalled();
});
it.each([
  "nextCursor",
  "duplicate",
  "wrongResource",
  "wrongNamespace",
  "wrongTenant",
  "missingETag",
  "wrongListing",
  "wrongCandidate",
  "wrongExtensions",
])("refuses an incomplete or mismatched Listing lookup: %s", async (fault) => {
  const w = screenWorkspace();
  const handle = { current: undefined as ListingPreparationHandle | undefined };
  const routes: Record<string, unknown> = { ...contextRoutes };
  if (fault === "nextCursor")
    routes["/api/v1/resources/{resourceId}/listings"] = {
      items: [],
      nextCursor: "more",
    };
  if (fault === "duplicate")
    routes["/api/v1/resources/{resourceId}/listings"] = {
      items: [listing, listing],
    };
  if (fault === "wrongResource")
    routes["/api/v1/resources/{resourceId}/listings"] = {
      items: [{ ...listing, resourceId: "other" }],
    };
  if (fault === "wrongNamespace")
    routes["/api/v1/resources/{resourceId}/listings"] = {
      items: [{ ...listing, namespaceId: "other" }],
    };
  if (fault === "wrongTenant")
    routes["/api/v1/resources/{resourceId}/listings"] = {
      items: [{ ...listing, tenantId: "other" }],
    };
  if (fault === "wrongListing")
    routes["/api/v1/catalog-listings/{listingId}"] = {
      ...listing,
      id: "other",
    };
  if (fault === "wrongCandidate")
    routes["/api/v1/listing-revisions/{revisionId}"] = {
      ...listingRevision,
      id: "other",
    };
  if (fault === "wrongExtensions")
    routes["/api/v1/listing-revisions/{revisionId}"] = {
      ...listingRevision,
      extensions: null,
    };
  readReplies(w.get, routes);
  if (fault === "missingETag")
    w.get.mockImplementation(async (path) =>
      etagReply(
        routes[String(path)],
        path === "/api/v1/catalog-listings/{listingId}" ? null : '"revision-1"',
      ),
    );
  render(
    <ListingPreparation
      {...w.props}
      access={administrator}
      resource={resource}
      namespaceId={namespace.id}
      handle={handle}
    />,
  );
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
  expect(handle.current).toBeUndefined();
  expect(screen.queryByLabelText("Display name")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});
it("keeps a first Listing candidate empty when the existing Listing has no candidate", async () => {
  const w = screenWorkspace();
  const handle = { current: undefined as ListingPreparationHandle | undefined };
  readReplies(w.get, {
    ...contextRoutes,
    "/api/v1/catalog-listings/{listingId}": listing,
  });
  render(
    <ListingPreparation
      {...w.props}
      access={administrator}
      resource={resource}
      namespaceId={namespace.id}
      handle={handle}
    />,
  );
  await screen.findByLabelText("Display name");
  await waitFor(() => expect(handle.current).toBeDefined());
  expect(handle.current!.read().listing?.id).toBe(listing.id);
  expect(screen.getByLabelText("Display name")).toHaveProperty("value", "");
});
it("enters the shared Listing step from known saved content without another evidence read", async () => {
  const w = screenWorkspace();
  readReplies(w.get, contextRoutes);
  w.post.mockResolvedValue(etagReply(newRevision, '"revision-2"'));
  render(
    <SavedListingAuthoring
      {...w.props}
      access={administrator}
      resource={resource}
      version={bundle.version}
      known={bundle}
    />,
  );
  await screen.findByLabelText("Display name");
  await waitFor(() =>
    expect(
      Array.from(
        document.querySelectorAll(
          "form input:invalid, form textarea:invalid, form select:invalid",
        ),
      ).map((field) => field.outerHTML),
    ).toEqual([]),
  );
  await confirmCommand("Save Listing draft");
  await screen.findByRole("link", { name: "Open saved Listing" });
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/catalog-listings/{listingId}/revisions",
    {
      body: { versionRevisionId: bundle.revision.id, displayName: "Helper" },
      params: { header: { "If-Match": '"revision-1"' } },
    },
  ]);
  expect(w.get.mock.calls.map((call) => call[0])).not.toContain(
    "/api/v1/version-revisions/{revisionId}",
  );
});
it("loads the exact saved candidate before preparation and blocks a save while its Listing lookup is pending", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  readReplies(w.get, {
    ...contextRoutes,
    "/api/v1/version-revisions/{revisionId}": sourceBundle,
  });
  const route = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, ...args) =>
    path === "/api/v1/resources/{resourceId}/listings"
      ? pending.promise
      : route(path, ...args),
  );
  render(
    <SavedListingAuthoring
      {...w.props}
      access={administrator}
      resource={resource}
      version={bundle.version}
    />,
  );
  await screen.findByRole("button", { name: "Save Listing draft" });
  click("Save Listing draft");
  await screen.findByRole("alert");
  expect(w.post).not.toHaveBeenCalled();
  await act(async () => pending.resolve(etagReply({ items: [listing] })));
  await screen.findByLabelText("Display name");
});
it("reports missing saved revisions without a mutation or private evidence request", async () => {
  const w = screenWorkspace();
  render(
    <SavedListingAuthoring
      {...w.props}
      access={administrator}
      resource={resource}
      version={version}
    />,
  );
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
  expect(w.get).not.toHaveBeenCalled();
});
it("offers Listing preparation from a Version detail and from a corrected revision receipt", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    ...contextRoutes,
    "/api/v1/resource-versions/{versionId}": bundle.version,
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/version-revisions/{revisionId}": sourceBundle,
    "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
  });
  w.post.mockResolvedValue(
    etagReply(
      { ...bundle, evidence: { ...bundle.evidence, state: "blocked" } },
      '"version-2"',
    ),
  );
  render(<Versions {...w.props} access={administrator} id={version.id} />);
  await screen.findByRole("button", { name: "Prepare listing metadata" });
  click("Prepare listing metadata");
  await screen.findByLabelText("Display name");
  click("Prepare listing metadata");
  click("New revision");
  await screen.findByLabelText("Standards document (JSON)");
  change("Standards document (JSON)", JSON.stringify(native));
  await confirmCommand("Create version revision");
  await screen.findByText("Draft saved, corrections needed.");
  click("Prepare Listing for this revision");
  await screen.findByLabelText("Display name");
  click("Prepare Listing for this revision");
  click("Correct this draft");
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    JSON.stringify(native),
  );
});
it("carries an exact revision from its private detail into a new-Version link", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": sourceBundle,
    "/api/v1/resource-versions/{versionId}": bundle.version,
  });
  render(
    <VersionRevision
      {...w.props}
      access={administrator}
      id={bundle.revision.id}
    />,
  );
  await screen.findByRole("button", {
    name: "Prepare new Version from this revision",
  });
  click("Prepare new Version from this revision");
  const link = await screen.findByRole("link", {
    name: "Continue with source revision 1",
  });
  expect(link).toHaveProperty(
    "href",
    `${location.origin}/unit/versions/new?resource=${resource.id}&sourceRevision=${bundle.revision.id}`,
  );
});
it("preserves source input after mismatched parent reads, cancellation and concurrent edits", async () => {
  const w = screenWorkspace();
  const onApply = vi.fn();
  const original = [
    { ...newRepresentation("agent"), content: "entered content" },
  ];
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": sourceBundle,
    "/api/v1/resource-versions/{versionId}": {
      ...bundle.version,
      resourceId: "other",
    },
  });
  const view = render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      drafts={original}
      onApply={onApply}
    />,
  );
  change("Exact source revision ID", bundle.revision.id);
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Load source revision");
  expect(w.get).not.toHaveBeenCalled();
  confirmation.mockReturnValue(true);
  click("Load source revision");
  await screen.findByRole("alert");
  expect(onApply).not.toHaveBeenCalled();
  const pending = deferred<unknown>();
  w.get
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(etagReply(bundle.version));
  click("Load source revision");
  view.rerender(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      drafts={[{ ...original[0]!, content: "changed while loading" }]}
      onApply={onApply}
    />,
  );
  await act(async () => pending.resolve(etagReply(sourceBundle)));
  await screen.findByText(/Your content changed during the read/);
  expect(onApply).not.toHaveBeenCalled();
});
it("preserves exact input after a failed or lossy source read and permits explicit retained-document selection", async () => {
  const w = screenWorkspace();
  const onApply = vi.fn();
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": {
      ...sourceBundle,
      revision: {
        ...bundle.revision,
        representations: [
          { ...bundle.revision.representations[0]!, data: { n: 1 } },
        ],
      },
    },
    "/api/v1/resource-versions/{versionId}": bundle.version,
  });
  render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      drafts={[]}
      onApply={onApply}
    />,
  );
  change("Exact source revision ID", bundle.revision.id);
  fireEvent.click(
    screen.getByLabelText(
      "Use retained document bytes instead of HTTPS references, where available",
    ),
  );
  click("Load source revision");
  await screen.findByRole("alert");
  expect(onApply).not.toHaveBeenCalled();
});
it("keeps manual source selection when history is unavailable and aborts it when content access is lost", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  w.get.mockReturnValue(pending.promise);
  const onApply = vi.fn();
  const noHistory = {
    ...administrator,
    permissions: administrator.permissions.filter(
      (p) => p !== "resource.read_history",
    ),
  };
  const view = render(
    <VersionSource
      {...w.props}
      access={noHistory}
      resource={resource}
      drafts={[]}
      onApply={onApply}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "Browse source history" }),
  ).toBeNull();
  change("Exact source revision ID", bundle.revision.id);
  click("Load source revision");
  view.rerender(
    <VersionSource
      {...w.props}
      access={{ ...noHistory, permissions: [] }}
      resource={resource}
      drafts={[]}
      onApply={onApply}
    />,
  );
  await act(async () => pending.resolve(etagReply(sourceBundle)));
  expect(onApply).not.toHaveBeenCalled();
});
it("opens Listing preparation after saving content without creating another Version", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    ...contextRoutes,
    "/api/v1/resources/{resourceId}/listings": { items: [] },
  });
  w.post
    .mockResolvedValueOnce(etagReply(bundle, '"version-1"'))
    .mockResolvedValueOnce(etagReply(listing))
    .mockResolvedValueOnce(etagReply(newRevision, '"revision-2"'));
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", JSON.stringify(native));
  await confirmCommand("Create version");
  await screen.findByRole("button", {
    name: "Prepare Listing for saved content",
  });
  click("Prepare Listing for saved content");
  await screen.findByLabelText("Display name");
  await screen.findByRole("option", {
    name: `${publisher.displayName} / ${publisher.id}`,
  });
  change("Publisher", publisher.id);
  await waitFor(() =>
    expect(
      Array.from(
        document.querySelectorAll(
          "form input:invalid, form textarea:invalid, form select:invalid",
        ),
      ).map((field) => field.outerHTML),
    ).toEqual([]),
  );
  await confirmCommand("Save Listing draft");
  await screen.findByRole("heading", { name: "Listing draft saved" });
  expect(w.post).toHaveBeenCalledTimes(3);
});

it("uses an automatically carried source and discloses its native version change in the final confirmation", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": sourceBundle,
    "/api/v1/resource-versions/{versionId}": bundle.version,
  });
  w.post.mockResolvedValue(
    etagReply({
      ...bundle,
      version: { ...bundle.version, version: "release two" },
    }),
  );
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
      sourceRevision={bundle.revision.id}
    />,
  );
  await screen.findByText(`Source revision:`, { exact: false });
  await waitFor(() =>
    expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
      "value",
      JSON.stringify(native, null, 2),
    ),
  );
  change("Version label", "release two");
  click("Create version");
  await screen.findByRole("dialog");
  expect(screen.getByText("Native version update")).toBeTruthy();
  click("Confirm");
  await screen.findByRole("heading", { name: "Draft saved" });
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    {
      body: {
        representations: [{ data: { ...native, version: "release two" } }],
      },
    },
  ]);
});
it("does not assume an absent Listing before its lookup finishes", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  w.get.mockReturnValue(pending.promise);
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", JSON.stringify(native));
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  click("Next: Listing details");
  click("Create version");
  await screen.findByRole("alert");
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  expect(screen.getByRole("button", { name: "Create version" })).toHaveProperty(
    "disabled",
    false,
  );
  await act(async () => pending.resolve(etagReply({ items: [] })));
});
it("uses the same captured Version command after a malformed successful acknowledgement", async () => {
  const w = screenWorkspace();
  w.post
    .mockResolvedValueOnce(
      etagReply({
        ...bundle,
        revision: { ...bundle.revision, evidenceSetId: "unrelated" },
      }),
    )
    .mockResolvedValueOnce(etagReply(bundle));
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", JSON.stringify(native));
  await confirmCommand("Create version");
  await screen.findByRole("button", { name: "Retry same command" });
  expect(screen.getByRole("button", { name: "Back to form" })).toHaveProperty(
    "disabled",
    true,
  );
  click("Retry same command");
  await screen.findByRole("heading", { name: "Draft saved" });
  expect(w.post.mock.calls[0]).toEqual(w.post.mock.calls[1]);
});
it("keeps a saved Version when missing validated Listing defaults require manual input", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}/listings": { items: [] },
    "/api/v1/publishers": { items: [publisher] },
  });
  w.post
    .mockResolvedValueOnce(
      etagReply({
        ...bundle,
        evidence: { ...bundle.evidence, state: "blocked" },
      }),
    )
    .mockResolvedValueOnce(etagReply(listing))
    .mockResolvedValueOnce(etagReply(newRevision, '"revision-2"'));
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", JSON.stringify(native));
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  click("Next: Listing details");
  await screen.findByRole("option", {
    name: `${publisher.displayName} / ${publisher.id}`,
  });
  change("Publisher", publisher.id);
  await confirmCommand("Create version");
  await screen.findByText(/Enter a Listing name and description/);
  click("Back to form");
  change("Display name", "A manual name");
  change("Description", "A manual description");
  await confirmCommand("Create version");
  await screen.findByRole("heading", { name: "Listing draft saved" });
  expect(w.post).toHaveBeenCalledTimes(3);
});
it("reports non-Error read failures and refuses malformed Version history", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(
      etagReply({ items: [{ ...bundle.version, resourceId: "other" }] }),
    )
    .mockRejectedValueOnce("offline");
  const onApply = vi.fn();
  render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      drafts={[]}
      onApply={onApply}
    />,
  );
  click("Browse source history");
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalledOnce());
  change("Exact source revision ID", bundle.revision.id);
  click("Load source revision");
  await screen.findByText(/Could not load the source/);
  expect(onApply).not.toHaveBeenCalled();
});

it("allows ordinary correction after an acknowledged save", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply(bundle));
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", JSON.stringify(native));
  await confirmCommand("Create version");
  await screen.findByRole("heading", { name: "Draft saved" });
  click("Correct this draft");
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "readOnly",
    true,
  );
});
it.each(["name", "description"] as const)(
  "applies an individual validated %s while preserving the other field",
  async (field) => {
    const w = screenWorkspace();
    const handle = { current: undefined as ListingFormHandle | undefined };
    const known = {
      ...bundle,
      revision: {
        ...bundle.revision,
        representations: [
          {
            ...bundle.revision.representations[0]!,
            normalizedFacts: { [field]: "Suggested value" },
          },
        ],
      },
    };
    readReplies(w.get, { "/api/v1/publishers": { items: [publisher] } });
    render(
      <ListingForm
        {...w.props}
        access={administrator}
        listing={listing}
        initial={{
          ...listingRevision,
          visibility: "private",
          digest: bundle.revision.contentSetDigest,
          createdBy: "author",
          createdAt: bundle.revision.createdAt,
        }}
        known={known}
        embedded
        formHandle={handle}
      />,
    );
    await screen.findByRole("option", {
      name: `${publisher.displayName} / ${publisher.id}`,
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    click("Use validated content name and description");
    expect(
      screen.getByLabelText(field === "name" ? "Display name" : "Description"),
    ).toHaveProperty("value", "Suggested value");
    change("Display name", "");
    change("Description", "");
    click("Use validated content name and description");
    expect(
      handle.current!.read(bundle.revision.id, true)[
        field === "name" ? "displayName" : "description"
      ],
    ).toBe("Suggested value");
  },
);
it("prepares a first Listing for a new Resource without a lookup", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/publishers": { items: [] } });
  const handle = { current: undefined as ListingPreparationHandle | undefined };
  render(
    <ListingPreparation
      {...w.props}
      access={administrator}
      namespaceId={namespace.id}
      handle={handle}
    />,
  );
  await screen.findByLabelText("Display name");
  await waitFor(() => expect(handle.current).toBeDefined());
  expect(handle.current!.read().listing).toBeUndefined();
  expect(w.get.mock.calls.map((call) => call[0])).toEqual([
    "/api/v1/publishers",
  ]);
});
it("restores an enabled visible correction field after a consumed command resets", async () => {
  const w = screenWorkspace();
  const props = {
    title: "Save field",
    description: "Save one field",
    allowed: true,
    onFailure: w.props.onFailure,
    prepare: () => async () => "Saved.",
  };
  const targets = ["absent", "hidden", "disabled", "target"];
  const fields = (
    <>
      <div hidden>
        <input id="hidden" aria-label="Hidden field" />
      </div>
      <input id="disabled" disabled aria-label="Disabled field" />
      <input id="target" aria-label="Current field" />
    </>
  );
  const view = render(
    <ActionForm {...props} resetToken={0} focusAfterReset={targets}>
      {fields}
    </ActionForm>,
  );
  await confirmCommand("Save field");
  await screen.findByText("Saved.");
  view.rerender(
    <ActionForm {...props} resetToken={1} focusAfterReset={targets}>
      {fields}
    </ActionForm>,
  );
  await waitFor(() =>
    expect(screen.getByLabelText("Current field")).toBe(document.activeElement),
  );
});
it("pages bounded readable Version and Revision choices", async () => {
  const w = screenWorkspace();
  const onApply = vi.fn();
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/resources/{resourceId}/versions"
        ? { items: [bundle.version], nextCursor: "versions-2" }
        : {
            items: [
              {
                id: bundle.revision.id,
                revisionNumber: 1,
                createdBy: "author",
              },
            ],
            nextCursor: "revisions-2",
          },
    ),
  );
  render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      drafts={[]}
      onApply={onApply}
    />,
  );
  click("Browse source history");
  await screen.findByRole("option", { name: "1.0.0 (draft)" });
  fireEvent.click(
    within(
      screen.getByLabelText("Source Version choices pagination"),
    ).getByRole("button", { name: "Next page" }),
  );
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "versions-2" } },
    }),
  );
  change("Source Version", version.id);
  await screen.findByRole("option", { name: "Revision 1 by author" });
  fireEvent.click(
    within(
      screen.getByLabelText("Source Revision choices pagination"),
    ).getByRole("button", { name: "Next page" }),
  );
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "revisions-2" } },
    }),
  );
});
it.each(["revision choice", "retained documents"] as const)(
  "discards a pending source after changing %s and allows an explicit new read",
  async (selection) => {
    const w = screenWorkspace();
    const onApply = vi.fn();
    const pending = deferred<unknown>();
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/version-revisions/{revisionId}")
        return pending.promise;
      if (path === "/api/v1/resource-versions/{versionId}")
        return etagReply(bundle.version);
      return etagReply({
        items:
          path === "/api/v1/resources/{resourceId}/versions"
            ? [bundle.version]
            : [
                {
                  id: bundle.revision.id,
                  revisionNumber: 1,
                  createdBy: "author",
                },
                {
                  id: "another-revision",
                  revisionNumber: 2,
                  createdBy: "author",
                },
              ],
      });
    });
    render(
      <VersionSource
        {...w.props}
        access={administrator}
        resource={resource}
        drafts={[]}
        onApply={onApply}
      />,
    );
    click("Browse source history");
    await screen.findByRole("option", { name: "1.0.0 (draft)" });
    change("Source Version", version.id);
    await screen.findByRole("option", { name: "Revision 1 by author" });
    change("Source Revision", bundle.revision.id);
    click("Load source revision");
    await screen.findByText("Loading complete source content...");
    const signal = (w.get.mock.calls.at(-1)![1] as { signal: AbortSignal })
      .signal;
    const selectedRevision =
      selection === "revision choice" ? "another-revision" : bundle.revision.id;
    if (selection === "revision choice")
      change("Source Revision", selectedRevision);
    else
      fireEvent.click(
        screen.getByLabelText(
          "Use retained document bytes instead of HTTPS references, where available",
        ),
      );
    await act(async () => pending.resolve(etagReply(sourceBundle)));
    expect(onApply).not.toHaveBeenCalled();
    expect(signal?.aborted).toBe(true);
    expect(screen.queryByText("Loading complete source content...")).toBeNull();
    expect(screen.getByLabelText("Exact source revision ID")).toHaveProperty(
      "value",
      selectedRevision,
    );
    expect(w.get).toHaveBeenCalledTimes(3);
    w.get
      .mockResolvedValueOnce(
        etagReply({
          ...sourceBundle,
          revision: { ...bundle.revision, id: selectedRevision },
        }),
      )
      .mockResolvedValueOnce(etagReply(bundle.version));
    click("Load source revision");
    await waitFor(() => expect(onApply).toHaveBeenCalledOnce());
    expect(onApply).toHaveBeenCalledWith(
      [expect.objectContaining({ content: JSON.stringify(native, null, 2) })],
      selectedRevision,
    );
  },
);
it.each(["url", "packageData", "documentStaged", "pluginPreparation"] as const)(
  "requires replacement confirmation for a staged %s source",
  async (field) => {
    const w = screenWorkspace();
    const row = {
      ...newRepresentation("agent"),
      [field]:
        field === "documentStaged"
          ? true
          : field === "pluginPreparation"
            ? { preparing: false, manifest: "{}", mcp: "", skills: [] }
            : "entered",
    };
    vi.spyOn(window, "confirm").mockReturnValue(false);
    render(
      <VersionSource
        {...w.props}
        access={administrator}
        resource={resource}
        drafts={[row]}
        onApply={vi.fn()}
      />,
    );
    change("Exact source revision ID", bundle.revision.id);
    click("Load source revision");
    expect(w.get).not.toHaveBeenCalled();
  },
);
it("cancels a source after its parent read starts", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  w.get
    .mockResolvedValueOnce(etagReply(sourceBundle))
    .mockReturnValueOnce(pending.promise);
  const apply = vi.fn();
  render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      initialRevision={bundle.revision.id}
      drafts={[]}
      onApply={apply}
    />,
  );
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  click("Cancel source read");
  await act(async () => pending.resolve(etagReply(bundle.version)));
  expect(apply).not.toHaveBeenCalled();
});

it("opens optional source choices without adding a required step", () => {
  const w = screenWorkspace();
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  const summary = screen.getByText("Start from an existing revision", {
    selector: "summary",
  });
  const details = summary.closest("details")!;
  details.open = true;
  fireEvent(details, new Event("toggle"));
  expect(details.open).toBe(true);
  expect(w.get).not.toHaveBeenCalled();
});

it("uses the acknowledged current revision for subsequent Version actions without a reload", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resource-versions/{versionId}": bundle.version,
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
  });
  const updated = {
    ...bundle,
    version: {
      ...bundle.version,
      revision: 2,
      candidateRevisionId: "corrected-exact",
    },
    revision: { ...bundle.revision, id: "corrected-exact", revisionNumber: 2 },
  };
  w.post.mockResolvedValue(etagReply(updated, '"version-2"'));
  render(<Versions {...w.props} id={version.id} access={administrator} />);
  await screen.findByRole("button", { name: "New revision" });
  click("New revision");
  change("Standards document (JSON)", JSON.stringify(native));
  await confirmCommand("Create version revision");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(
    screen.getByRole("link", { name: "Create new Version from this revision" }),
  ).toHaveProperty(
    "href",
    `${location.origin}/unit/versions/new?resource=${resource.id}&sourceRevision=corrected-exact`,
  );
});
it.each([
  ["version", "id", "other"],
  ["version", "tenantId", "other"],
  ["version", "revision", 0],
  ["resource", "id", "other"],
  ["resource", "tenantId", "other"],
] as const)(
  "refuses mismatched Version context: %s %s",
  async (subject, field, value) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/resource-versions/{versionId}":
        subject === "version"
          ? { ...bundle.version, [field]: value }
          : bundle.version,
      "/api/v1/resources/{resourceId}":
        subject === "resource" ? { ...resource, [field]: value } : resource,
    });
    render(<Versions {...w.props} id={version.id} access={administrator} />);
    await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "New revision" })).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps an HTTPS source reference and uses newly acknowledged facts instead of its earlier document", async () => {
  const w = screenWorkspace();
  const url = "https://metadata.example.test/card.json";
  const original = {
    ...sourceBundle,
    revision: {
      ...bundle.revision,
      representations: [{ ...bundle.revision.representations[0]!, url }],
    },
  };
  const changed = {
    ...bundle,
    version: {
      ...bundle.version,
      id: "version-new",
      version: "2.0.0",
      candidateRevisionId: "revision-new",
    },
    revision: {
      ...bundle.revision,
      id: "revision-new",
      resourceVersionId: "version-new",
      representations: [
        {
          ...bundle.revision.representations[0]!,
          url,
          normalizedFacts: {
            name: "Changed remote name",
            description: "Changed remote description",
          },
        },
      ],
    },
  };
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": original,
    "/api/v1/resource-versions/{versionId}": bundle.version,
    "/api/v1/resources/{resourceId}/listings": { items: [] },
    "/api/v1/publishers": { items: [publisher] },
  });
  w.post.mockResolvedValue(etagReply(changed));
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
      sourceRevision={bundle.revision.id}
    />,
  );
  await screen.findByLabelText("Document URL");
  await waitFor(() =>
    expect(screen.getByLabelText("Document URL")).toHaveProperty("value", url),
  );
  change("Version label", "2.0.0");
  await confirmCommand("Create version");
  await screen.findByRole("heading", { name: "Draft saved" });
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    {
      body: {
        version: "2.0.0",
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            url,
          },
        ],
      },
    },
  ]);
  const sent = w.post.mock.calls[0]?.[1] as {
    body: { representations: Record<string, unknown>[] };
  };
  expect(sent.body.representations[0]).not.toHaveProperty("data");
  expect(sent.body.representations[0]).not.toHaveProperty("expectedDigest");
  click("Prepare Listing for saved content");
  await screen.findByLabelText("Display name");
  await waitFor(() => {
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Changed remote name",
    );
    expect(screen.getByLabelText("Description")).toHaveProperty(
      "value",
      "Changed remote description",
    );
  });
  expect(original.revision.representations[0]?.data).toEqual(native);
  expect(w.post).toHaveBeenCalledTimes(1);
});
