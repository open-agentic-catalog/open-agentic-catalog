import { required } from "../../tests/required";
// @vitest-environment jsdom
import {
  release,
  evidence,
  versionRevision,
  representation,
  listingRevision,
  submission,
} from "../../tests/unit/responses";
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Listings, { ListingRevision } from "./Listings";
import { confirmDiscard } from "./workspace";
import { reply } from "../../tests/unit/fixtures";
import { reader, sessionAccess } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  listing,
  publisher,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
  version,
} from "../../tests/unit/screens";
screenLifecycle("/unit/listings/listing-1");

it.each([true, false])(
  "opens and focuses direct release history (empty=%s) without reading revisions",
  async (empty) => {
    history.replaceState(null, "", "/unit/listings/listing-1#release-history");
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      ...routes(),
      "/api/v1/catalog-listings/{listingId}/releases": {
        items: empty ? [] : [release],
      },
    });
    render(<Listings {...props} access={reader} id={listing.id} />);
    const heading = await screen.findByRole("heading", {
      name: "Release history",
    });
    await waitFor(() => expect(document.activeElement).toBe(heading));
    expect(
      screen.queryByRole("heading", { name: "Listing revision history" }),
    ).toBeNull();
    if (empty) await screen.findByText("No history yet.");
    else {
      expect(
        (await screen.findByRole("link", { name: /published/ })).getAttribute(
          "href",
        ),
      ).toBe("/unit/releases/release-1");
      expect(screen.getByText("Current listing release")).toBeTruthy();
    }
    expect(
      get.mock.calls.filter(([path]) => String(path).includes("/revisions")),
    ).toHaveLength(0);
    expect(
      get.mock.calls.filter(([path]) =>
        String(path).includes("/version-revisions"),
      ),
    ).toHaveLength(0);
    expect(post).not.toHaveBeenCalled();
  },
);

it("preserves page order and recovers from a failed release continuation without stale rows", async () => {
  history.replaceState(null, "", "/unit/listings/listing-1#release-history");
  const { props, get } = screenWorkspace();
  const original = routes();
  get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/catalog-listings/{listingId}/releases") {
      const params = options as {
        params: { query: { cursor?: string; limit: number } };
      };
      expect(params.params.query.limit).toBe(25);
      if (params.params.query.cursor)
        return {
          error: {
            code: "invalid_cursor",
            detail: "Continuation expired. Start from the first page.",
          },
          response: new Response(null, { status: 400 }),
        };
      return etagReply({
        items: [{ ...release, id: "z-release", state: "retired" }, release],
        nextCursor: "opaque-continuation",
      });
    }
    expect(path).toBe("/api/v1/catalog-listings/{listingId}");
    return etagReply(original["/api/v1/catalog-listings/{listingId}"]);
  });
  render(<Listings {...props} access={reader} id={listing.id} />);
  await screen.findByRole("link", { name: /retired/ });
  const links = screen
    .getAllByRole("link")
    .filter((link) => link.getAttribute("href")?.startsWith("/unit/releases/"));
  expect(links.slice(-2).map((link) => link.getAttribute("href"))).toEqual([
    "/unit/releases/z-release",
    "/unit/releases/release-1",
  ]);
  click("Next page");
  await screen.findByRole("alert");
  expect(screen.queryByText("Current listing release")).toBeNull();
  expect(screen.queryByRole("link", { name: /retired/ })).toBeNull();
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { cursor: "opaque-continuation" } },
  });
  click("First page");
  await screen.findByRole("link", { name: /retired/ });
  expect(screen.queryByRole("alert")).toBeNull();
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { limit: 25 } },
  });
});
const revision = {
  tenantId: "tenant-a",
  id: "listing-rev-1",
  listingId: listing.id,
  versionRevisionId: "version-rev-1",
  displayName: "Listed helper",
  description: "Helpful agent",
  publisherId: publisher.id,
  visibility: "private",
  tags: ["old"],
  categories: [],
  representativeQueries: [],
  relationships: [],
  extensions: { retained: "yes" },
  revisionNumber: 1,
  createdBy: "author",
};
function routes(hasRevision = true) {
  return {
    "/api/v1/catalog-listings/{listingId}": {
      ...listing,
      ...(hasRevision
        ? { candidateRevisionId: revision.id, currentReleaseId: "release-1" }
        : {}),
    },
    "/api/v1/catalog-listings/{listingId}/revisions": {
      items: [revision],
      nextCursor: "next",
    },
    "/api/v1/catalog-listings/{listingId}/releases": {
      items: [
        { ...release, id: "release-1", state: "published", activatedAt: stamp },
      ],
    },
    "/api/v1/listing-revisions/{revisionId}": revision,
    "/api/v1/resources/{resourceId}/versions": { items: [version] },
    "/api/v1/resource-versions/{versionId}/revisions": {
      items: [{ id: "version-rev-2", revisionNumber: 2, createdBy: "author" }],
    },
    "/api/v1/publishers": { items: [publisher] },
  };
}

it.each(["both", "name", "description"])(
  "saves explicitly prefilled %s fields while retaining other listing metadata",
  async (fields) => {
    const { props, get, post } = screenWorkspace();
    const facts = {
      ...(fields !== "description" ? { name: "Prefilled name" } : {}),
      ...(fields !== "name" ? { description: "Prefilled description" } : {}),
      visibility: "public",
      publisherId: "not-copied",
    };
    readReplies(get, {
      ...routes(),
      "/api/v1/version-revisions/{revisionId}": {
        namespaceId: "ns-1",
        evidence: { ...evidence, state: "complete" },
        revision: {
          ...versionRevision,
          id: "version-rev-1",
          representations: [
            {
              ...representation,
              id: "primary",
              role: "primary",
              mediaType: "application/json",
              validationState: "valid",
              normalizedFacts: facts,
            },
          ],
        },
      },
    });
    post.mockResolvedValue(
      etagReply(
        {
          listing: { ...listing, candidateRevisionId: "new-listing-revision" },
          revision: { ...listingRevision, id: "new-listing-revision" },
        },
        '"revision-2"',
      ),
    );
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<Listings {...props} id={listing.id} access={administrator} />);
    await screen.findByRole("button", { name: "New listing revision" });
    click("New listing revision");
    await screen.findByLabelText("Display name");
    click("Load name and description suggestions");
    await screen.findByLabelText("Prefill representation");
    change("Prefill representation", "primary");
    expect(post).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      revision.displayName,
    );
    // Reading and selecting a preview do not change the listing draft.
    expect(confirmDiscard()).toBe(true);
    expect(window.confirm).not.toHaveBeenCalled();
    click("Use suggested name and description");
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      facts.name ?? revision.displayName,
    );
    expect(screen.getByLabelText("Description")).toHaveProperty(
      "value",
      facts.description ?? revision.description,
    );
    expect(window.confirm).toHaveBeenCalledTimes(1);
    expect(confirmDiscard()).toBe(true);
    expect(window.confirm).toHaveBeenLastCalledWith(
      expect.stringContaining("Discard unsaved input"),
    );
    await confirmCommand("Create listing revision");
    await screen.findByRole("link", {
      name: "Reload listing to review saved revision",
    });
    expect(required(post.mock.calls[0])[1]).toMatchObject({
      body: {
        versionRevisionId: revision.versionRevisionId,
        displayName: facts.name ?? revision.displayName,
        description: facts.description ?? revision.description,
        publisherId: publisher.id,
        visibility: "private",
        tags: revision.tags,
        extensions: revision.extensions,
      },
    });
  },
);

it("pages revision/release history and submits the selected listing for independent review", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  post.mockResolvedValue(etagReply({ ...submission, id: "submission-1" }));
  // Settle the listing read and its subsequently mounted history read before
  // querying the mocked history; neither request is intentionally deferred.
  await act(async () => {
    render(<Listings {...props} id={listing.id} access={administrator} />);
  });
  await screen.findByRole("link", { name: "Revision 1 by author" });
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "next" } },
    }),
  );
  click("Release history");
  await screen.findByRole("link", { name: /published/ });
  click("Revision history");
  await screen.findByRole("link", { name: "Revision 1 by author" });
  await confirmCommand("Submit for review");
  expect(
    (
      await screen.findByRole("link", { name: "Open review submission" })
    ).getAttribute("href"),
  ).toBe("/unit/reviews/submission-1");
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/catalog-listings/{listingId}/submissions",
    { params: { path: { listingId: listing.id } }, body: {} },
  ]);
  readReplies(get, {
    ...routes(),
    "/api/v1/catalog-listings/{listingId}/releases": { items: [] },
  });
  click("Release history");
  await screen.findByText("No history yet.");
});

it("does not discard an edited listing revision without confirmation", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  const discard = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValue(true);
  const view = render(
    <Listings {...props} id={listing.id} access={administrator} />,
  );
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  await screen.findByLabelText("Display name");
  change("Display name", "Unsaved metadata");
  view.rerender(<Listings {...props} id={listing.id} />);
  expect(
    screen.getByRole("button", { name: "Create listing revision" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByText(/resource.edit permission is required in this namespace/),
  ).toBeTruthy();
  click("New listing revision");
  expect(
    (screen.getByLabelText("Display name") as HTMLInputElement).value,
  ).toBe("Unsaved metadata");
  click("New listing revision");
  expect(screen.queryByLabelText("Display name")).toBeNull();
  expect(discard).toHaveBeenCalledTimes(2);
  expect(post).not.toHaveBeenCalled();
});

it.each(["reader", "missing-etag"])(
  "keeps listing history readable and separates editing authority from a missing ETag for %s",
  async (mode) => {
    const { props, get, post } = screenWorkspace();
    const values: Record<string, unknown> = routes();
    get.mockImplementation(async (path) => reply(values[String(path)]));
    render(
      <Listings
        {...props}
        id={listing.id}
        access={mode === "reader" ? props.access : administrator}
      />,
    );
    await screen.findByRole("button", { name: "New listing revision" });
    expect(
      screen.getByRole("button", { name: "New listing revision" }),
    ).toHaveProperty("disabled", mode === "reader");
    expect(
      screen.getByText(
        mode === "reader"
          ? /resource.edit permission is required in this namespace/
          : "Reload the listing to obtain its current revision before saving.",
      ),
    ).toBeTruthy();
    await screen.findByRole("link", { name: "Revision 1 by author" });
    click("New listing revision");
    if (mode === "reader")
      expect(screen.queryByLabelText("Display name")).toBeNull();
    else {
      const name = await screen.findByLabelText("Display name");
      expect(name.matches(":disabled")).toBe(false);
      expect(
        screen.getByText(
          "Reload the listing to obtain its current revision before saving.",
        ),
      ).toBeTruthy();
      change("Display name", "Staged while the revision marker is unavailable");
      expect(screen.getByLabelText("Display name")).toHaveProperty(
        "value",
        "Staged while the revision marker is unavailable",
      );
      expect(
        screen.getByRole("button", { name: "Create listing revision" }),
      ).toHaveProperty("disabled", true);
      fireEvent.submit(name.closest("form")!);
      expect(screen.queryByRole("dialog")).toBeNull();
    }
    expect(post).not.toHaveBeenCalled();
  },
);

it.each([true, false])(
  "writes listing metadata while keeping the current version=%s",
  async (keep) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes(keep));
    post.mockResolvedValue(
      etagReply(
        {
          listing: { ...listing, candidateRevisionId: "listing-rev-2" },
          revision: { ...listingRevision, id: "listing-rev-2" },
        },
        '"revision-2"',
      ),
    );
    render(<Listings {...props} id={listing.id} access={administrator} />);
    await screen.findByRole("button", { name: "New listing revision" });
    click("New listing revision");
    await screen.findByLabelText("Display name");
    const editor = within(
      required(
        screen.getByLabelText("Display name").closest("form") ?? undefined,
      ),
    );
    const edit = (name: string, value: string) =>
      fireEvent.change(editor.getByLabelText(name), { target: { value } });
    const press = (name: string) =>
      fireEvent.click(editor.getByRole("button", { name }));
    if (!keep) {
      await editor.findByRole("option", { name: "1.0.0" });
      edit("Resource version", version.id);
      await editor.findByRole("option", { name: "Revision 2 by author" });
      edit("Version revision", "version-rev-2");
    }
    edit("Display name", " Updated listing ");
    edit("Description", " New description ");
    await editor.findByRole("option", {
      name: `${publisher.displayName} / ${publisher.id}`,
    });
    edit("Publisher", publisher.id);
    edit("Visibility", "public");
    if (!editor.queryByLabelText("Tag 1")) press("Add tag");
    edit("Tag 1", " tools ");
    press("Add tag");
    edit("Tag 2", "tools");
    press("Add tag");
    edit("Tag 3", "new");
    press("Add category");
    edit("Category 1", "assistants");
    edit("Representative queries (one per line)", "find helper\nhelp me");
    editor.getByText("Links, license and relationships").click();
    edit("Documentation URL", "https://example.com/docs");
    edit("License", "Apache-2.0");
    press("Add relationship");
    edit("Relationship type", "supersedes");
    edit("Target identifier", " urn:air:example.com:agents:previous ");
    edit("Target version (required)", " 1.0 ");
    edit(
      "Extensions (JSON object)",
      '{"retained":"yes","extra":true,"minimum":-9007199254740991,"maximum":9007199254740991}',
    );
    const readsBeforeReview = get.mock.calls.length;
    press("Create listing revision");
    const dialog = await screen.findByRole("dialog");
    const facts = within(dialog).getByLabelText("Command summary");
    expect(facts.textContent).toContain(
      keep ? "Edit saved candidate" : "First listing revision",
    );
    expect(facts.textContent).toContain(keep ? revision.id : "None");
    const comparison = within(dialog).getByRole("region", {
      name: "Prepared listing changes",
    });
    expect(comparison.textContent).toContain("New description");
    expect(comparison.textContent).toContain("find helper");
    expect(get).toHaveBeenCalledTimes(readsBeforeReview);
    expect(post).not.toHaveBeenCalled();
    // A programmatic background edit cannot alter the prepared request. The
    // native dialog/disabled fieldset prevent ordinary background interaction.
    edit("Display name", "Later background change");
    expect(facts.textContent).toContain("Updated listing");
    expect(facts.textContent).not.toContain("Later background change");
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await screen.findByRole("link", {
      name: "Reload listing to review saved revision",
    });
    expect(post.mock.calls).toEqual([
      [
        "/api/v1/catalog-listings/{listingId}/revisions",
        {
          params: {
            path: { listingId: listing.id },
            header: {
              "Idempotency-Key": expect.any(String) as unknown,
              "If-Match": '"revision-1"',
            },
          },
          body: {
            versionRevisionId: keep ? "version-rev-1" : "version-rev-2",
            displayName: "Updated listing",
            description: "New description",
            tags: ["tools", "new"],
            categories: ["assistants"],
            visibility: "public",
            publisherId: publisher.id,
            representativeQueries: ["find helper", "help me"],
            documentationUrl: "https://example.com/docs",
            license: "Apache-2.0",
            relationships: [
              {
                type: "supersedes",
                targetIdentifier: "urn:air:example.com:agents:previous",
                targetVersion: "1.0",
              },
            ],
            extensions: {
              retained: "yes",
              extra: true,
              minimum: -9007199254740991,
              maximum: 9007199254740991,
            },
          },
        },
      ],
    ]);
  },
);

it("allows deliberate replacement of retained version and rejects missing public queries before sending", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  render(<Listings {...props} id={listing.id} access={administrator} />);
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  await screen.findByLabelText("Keep the current version revision");
  fireEvent.click(screen.getByLabelText("Keep the current version revision"));
  await screen.findByRole("option", { name: "1.0.0" });
  change("Resource version", version.id);
  await screen.findByRole("option", { name: "Revision 2 by author" });
  change("Version revision", "version-rev-2");
  change("Visibility", "public");
  click("Create listing revision");
  const summary = await screen.findByRole("alert");
  expect(summary).toBe(document.activeElement);
  fireEvent.click(screen.getByRole("link", { name: /Public listings need/ }));
  expect(screen.getByLabelText("Representative queries (one per line)")).toBe(
    document.activeElement,
  );
  expect(screen.getByLabelText("Version revision")).toHaveProperty(
    "value",
    "version-rev-2",
  );
  expect(post).not.toHaveBeenCalled();
});

it("explains why a listing without a candidate cannot be submitted", async () => {
  const { props, get, post } = screenWorkspace();
  const data = routes(false);
  Reflect.deleteProperty(
    data["/api/v1/catalog-listings/{listingId}"],
    "candidateRevisionId",
  );
  readReplies(get, data);
  render(<Listings {...props} id={listing.id} access={administrator} />);
  await screen.findByText(/No candidate revision yet/);
  expect(
    screen.getByRole("button", { name: "Submit for review" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByText(/Submitting requires a saved candidate/),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["[]", "Enter a JSON object."],
  [
    '{"count":9007199254740992}',
    "JSON numbers must be finite. Whole numbers must be between -9007199254740991 and 9007199254740991.",
  ],
  [
    '{"count":-9007199254740992}',
    "JSON numbers must be finite. Whole numbers must be between -9007199254740991 and 9007199254740991.",
  ],
  [
    '{"count":1e400}',
    "JSON numbers must be finite. Whole numbers must be between -9007199254740991 and 9007199254740991.",
  ],
])(
  "opens a collapsed extension editor after rejecting %s without losing listing input",
  async (input, message) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes());
    render(<Listings {...props} id={listing.id} access={administrator} />);
    await screen.findByRole("button", { name: "New listing revision" });
    click("New listing revision");
    await screen.findByLabelText("Keep the current version revision");
    change("Display name", "Retained edited name");
    change("Extensions (JSON object)", input);
    click("Create listing revision");
    const alert = await screen.findByRole("alert");
    expect(alert).toBe(document.activeElement);
    expect(alert.textContent).not.toContain("URL");
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(screen.getByRole("link", { name: message }));
    const editor = screen.getByLabelText("Extensions (JSON object)");
    expect(editor).toBe(document.activeElement);
    expect(editor.closest("details")?.open).toBe(true);
    expect(editor).toHaveProperty("value", input);
    change("Extensions (JSON object)", "{}");
    expect(editor).toBe(document.activeElement);
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Retained edited name",
    );
    expect(post).not.toHaveBeenCalled();
  },
);

it("renders immutable listing metadata and exact version links", async () => {
  const { props, get } = screenWorkspace();
  get.mockResolvedValue(etagReply(revision));
  render(<ListingRevision {...props} id={revision.id} />);
  expect(
    (
      await screen.findByRole("link", { name: "Inspect version revision" })
    ).getAttribute("href"),
  ).toBe("/unit/version-revisions/version-rev-1");
  expect(screen.getByText("All listing metadata")).toBeTruthy();
});

it.each(["content-only", "available", "tenant", "scope-denied"])(
  "requires confirmed metadata authority for the standalone listing-revision Back link: %s",
  async (mode) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(revision));
    render(
      <ListingRevision
        {...w.props}
        id={revision.id}
        access={{
          ...reader,
          permissions:
            mode === "tenant"
              ? ["resource.read_content", "resource.read"]
              : ["resource.read_content"],
          availablePermissions:
            mode === "content-only"
              ? ["resource.read_content"]
              : ["resource.read_content", "resource.read"],
          namespaceGrants: [],
          scopeEnforcement: true,
          scopes: mode === "scope-denied" ? [] : ["catalog.read"],
        }}
      />,
    );
    await screen.findByRole("link", { name: "Inspect version revision" });
    expect(
      screen.queryByRole("link", { name: "Back to listing" }) !== null,
    ).toBe(mode === "tenant");
    if (mode !== "tenant")
      expect(screen.getByText(revision.listingId)).toBeTruthy();
    expect(w.get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/listing-revisions/{revisionId}",
    ]);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("retains listing authoring without publisher-list scope and sends the explicitly entered publisher ID", async () => {
  const { props, get, post } = screenWorkspace();
  const allowedRoutes = routes();
  Reflect.deleteProperty(allowedRoutes, "/api/v1/publishers");
  readReplies(get, allowedRoutes);
  post.mockResolvedValue(
    etagReply(
      {
        listing: { ...listing, candidateRevisionId: "listing-rev-2" },
        revision: { ...listingRevision, id: "listing-rev-2" },
      },
      '"revision-2"',
    ),
  );
  render(
    <Listings
      {...props}
      id={listing.id}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["catalog.read", "catalog.write"],
      }}
    />,
  );
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  await screen.findByLabelText("Publisher ID");
  const publisher = screen.getByLabelText("Publisher ID");
  expect(
    document.getElementById(publisher.getAttribute("aria-describedby")!)
      ?.textContent,
  ).toContain("Your current access does not allow browsing Publishers.");
  expect(screen.queryByRole("link", { name: "Publishers" })).toBeNull();
  change("Publisher ID", "publisher-2");
  await confirmCommand("Create listing revision");
  await screen.findByRole("link", {
    name: "Reload listing to review saved revision",
  });
  expect(post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog-listings/{listingId}/revisions",
    {
      params: {
        path: { listingId: listing.id },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: {
        versionRevisionId: revision.versionRevisionId,
        displayName: revision.displayName,
        description: revision.description,
        publisherId: "publisher-2",
        visibility: revision.visibility,
        tags: revision.tags,
        categories: [],
        representativeQueries: [],
        relationships: [],
        extensions: revision.extensions,
      },
    },
  );
  expect(get.mock.calls.map(([path]) => path)).not.toContain(
    "/api/v1/publishers",
  );
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("pages each listing choice with its own cursor while retaining selected identifiers", async () => {
  const { props, get, post } = screenWorkspace();
  const pages: Record<string, unknown> = {
    ...routes(false),
    "/api/v1/resources/{resourceId}/versions": {
      items: [version],
      nextCursor: "version-next",
    },
    "/api/v1/resource-versions/{versionId}/revisions": {
      items: [{ id: "version-rev-2", revisionNumber: 2, createdBy: "author" }],
      nextCursor: "revision-next",
    },
    "/api/v1/publishers": { items: [publisher], nextCursor: "publisher-next" },
  };
  readReplies(get, pages);
  render(<Listings {...props} id={listing.id} access={administrator} />);
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  for (const [label, option, value, path, cursor] of [
    [
      "Resource version",
      "1.0.0",
      version.id,
      "/api/v1/resources/{resourceId}/versions",
      "version-next",
    ],
    [
      "Version revision",
      "Revision 2 by author",
      "version-rev-2",
      "/api/v1/resource-versions/{versionId}/revisions",
      "revision-next",
    ],
    [
      "Publisher",
      `${publisher.displayName} / ${publisher.id}`,
      publisher.id,
      "/api/v1/publishers",
      "publisher-next",
    ],
  ] as const) {
    await screen.findByRole("option", { name: option });
    change(label, value);
    const choices = within(
      required(
        screen.getByLabelText(label).closest(".workspace-field")
          ?.parentElement ?? undefined,
      ),
    );
    // The selected item is genuinely absent from the next page, not repeated.
    const firstPage = pages[path];
    pages[path] = { items: [] };
    fireEvent.click(choices.getByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(get.mock.calls.at(-1)).toMatchObject([
        path,
        { params: { query: { cursor } } },
      ]),
    );
    await waitFor(() => expect(choices.queryByRole("status")).toBeNull());
    expect(choices.queryByRole("alert")).toBeNull();
    expect(
      choices.getByRole("option", { name: `${option} (selected)` }),
    ).toHaveProperty("selected", true);
    expect(choices.queryByRole("option", { name: option })).toBeNull();
    expect(screen.getByLabelText(label)).toHaveProperty("value", value);
    expect(choices.queryByRole("button", { name: "Next page" })).toBeNull();
    pages[path] = firstPage;
    fireEvent.click(choices.getByRole("button", { name: "First page" }));
    await waitFor(() => {
      expect(get.mock.calls.at(-1)?.[0]).toBe(path);
      expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
        "params.query.cursor",
      );
    });
    await choices.findByRole("option", { name: option });
    expect(
      choices.queryByRole("option", { name: `${option} (selected)` }),
    ).toBeNull();
    expect(screen.getByLabelText(label)).toHaveProperty("value", value);
  }
  expect(post).not.toHaveBeenCalled();
});

it.each(["resource.edit", "review.submit"] as const)(
  "uses the independent %s permission for its listing action",
  async (permission) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes());
    post.mockResolvedValue(etagReply(submission));
    const access = {
      ...administrator,
      permissions: ["resource.read_history" as const],
      availablePermissions: ["resource.read_history" as const, permission],
      namespaceGrants: [
        { namespaceId: listing.namespaceId, permissions: [permission] },
      ],
      scopeEnforcement: true,
      scopes: ["catalog.read" as const, "catalog.write" as const],
    };
    const view = render(
      <Listings {...props} id={listing.id} access={access} />,
    );
    await screen.findByRole("button", { name: "New listing revision" });
    expect(
      screen.getByRole("button", { name: "New listing revision" }),
    ).toHaveProperty("disabled", permission !== "resource.edit");
    if (permission === "review.submit") {
      await confirmCommand("Submit for review");
      expect(post.mock.calls[0]?.[0]).toBe(
        "/api/v1/catalog-listings/{listingId}/submissions",
      );
    } else {
      expect(
        screen.queryByRole("button", { name: "Submit for review" }),
      ).toBeNull();
      click("New listing revision");
      await screen.findByLabelText("Display name");
      expect(
        screen.getByRole("button", { name: "Create listing revision" }),
      ).toHaveProperty("disabled", false);
      change("Display name", "Unsent scoped metadata");
    }
    view.rerender(
      <Listings
        {...props}
        id={listing.id}
        access={{ ...access, scopes: ["catalog.read"] }}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Submit for review" }),
    ).toBeNull();
    if (permission === "resource.edit") {
      expect(screen.getByLabelText("Display name")).toHaveProperty(
        "value",
        "Unsent scoped metadata",
      );
      expect(
        screen.getByRole("button", { name: "Create listing revision" }),
      ).toHaveProperty("disabled", true);
    }
  },
);

it.each([
  ["Display name", "🙂".repeat(161)],
  ["Description", "🙂".repeat(4001)],
  ["Tag 1", "🙂".repeat(81)],
  ["Documentation URL", "http://example.com"],
  ["Support URL", "https://example.com?token=private"],
  ["Terms URL", "https://user:pass@example.com"],
])(
  "rejects invalid listing %s and links the error to the preserved field",
  async (label, value) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes(true));
    render(<Listings {...props} id={listing.id} access={administrator} />);
    await screen.findByRole("button", { name: "New listing revision" });
    click("New listing revision");
    await screen.findByLabelText("Display name");
    if (label.endsWith("URL"))
      screen.getByText("Links, license and relationships").click();
    change(label, value);
    click("Create listing revision");
    const alert = await screen.findByRole("alert");
    expect(post).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(within(alert).getByRole("link"));
    expect(document.activeElement).toBe(screen.getByLabelText(label));
    expect(screen.getByLabelText(label)).toHaveProperty("value", value);
  },
);
it("submits full supplementary listing field and query boundaries unchanged", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes(true));
  post.mockResolvedValue(
    etagReply(
      {
        listing: { ...listing, candidateRevisionId: listingRevision.id },
        revision: listingRevision,
      },
      '"revision-2"',
    ),
  );
  render(<Listings {...props} id={listing.id} access={administrator} />);
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  await screen.findByLabelText("Display name");
  const name = "🙂".repeat(160),
    description = "🙂".repeat(4000),
    query = "🙂".repeat(240);
  change("Display name", name);
  change("Description", description);
  change("Representative queries (one per line)", query + "\n🙂🙂🙂");
  await confirmCommand("Create listing revision");
  await screen.findByRole("link", {
    name: "Reload listing to review saved revision",
  });
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      displayName: name,
      description,
      representativeQueries: [query, "🙂🙂🙂"],
    },
  });
});

it("preserves comma-bearing tags and categories when only another listing field changes", async () => {
  const { props, get, post } = screenWorkspace();
  const exact = {
    ...revision,
    tags: ["support, tools"],
    categories: ["design, review"],
  };
  readReplies(get, {
    ...routes(true),
    "/api/v1/listing-revisions/{revisionId}": exact,
  });
  post.mockResolvedValue(
    etagReply(
      {
        listing: { ...listing, candidateRevisionId: exact.id },
        revision: exact,
      },
      '"revision-2"',
    ),
  );
  render(<Listings {...props} id={listing.id} access={administrator} />);
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  await screen.findByLabelText("Display name");
  expect(screen.getByLabelText("Tag 1")).toHaveProperty(
    "value",
    "support, tools",
  );
  expect(screen.getByLabelText("Category 1")).toHaveProperty(
    "value",
    "design, review",
  );
  change("Display name", "Changed name");
  await confirmCommand("Create listing revision");
  await screen.findByRole("link", {
    name: "Reload listing to review saved revision",
  });
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      displayName: "Changed name",
      tags: ["support, tools"],
      categories: ["design, review"],
    },
  });
});

it("uses already-resolved later-page namespace access for an empty version prerequisite link", async () => {
  const { props, get, post } = screenWorkspace();
  const namespaceId = "00000000-0000-0000-0000-000000000099";
  const access = {
    ...sessionAccess,
    permissions: ["resource.read_history" as const],
    availablePermissions: [
      "resource.read_history" as const,
      "resource.read" as const,
      "resource.edit" as const,
    ],
    namespaceGrants: [],
    nextCursor: "more-grants",
  };
  readReplies(get, {
    ...routes(false),
    "/api/v1/catalog-listings/{listingId}": { ...listing, namespaceId },
    "/api/v1/resources/{resourceId}/versions": { items: [] },
    "/api/v1/me": {
      ...access,
      namespaceGrants: [
        {
          namespaceId,
          permissions: [
            "resource.read",
            "resource.read_history",
            "resource.edit",
          ],
        },
      ],
    },
  });
  render(<Listings {...props} id={listing.id} access={access} />);
  const edit = await screen.findByRole("button", {
    name: "New listing revision",
  });
  await waitFor(() => expect(edit).toHaveProperty("disabled", false));
  fireEvent.click(edit);
  const link = await screen.findByRole("link", {
    name: "Open resource to create a version",
  });
  expect(link.getAttribute("href")).toBe(
    `/unit/resources/${listing.resourceId}`,
  );
  for (const [path, options] of get.mock.calls)
    if (path === "/api/v1/me")
      expect(options).toMatchObject({ params: { query: { namespaceId } } });
  expect(post).not.toHaveBeenCalled();
});
it("captures declared artwork author input without sealed facts alongside unrelated extensions", async () => {
  const iri =
    "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor";
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    ...routes(true),
    "/api/v1/listing-revisions/{revisionId}": {
      ...revision,
      extensions: {
        [iri]: {
          schemaVersion: 1,
          icon: {
            url: "https://images.example/old.png",
            alt: "Old icon",
            rights: { basis: "owned", attribution: "Example team" },
          },
        },
        "urn:example:other": { value: true },
      },
    },
  });
  post.mockResolvedValue(
    etagReply(
      {
        listing: { ...listing, candidateRevisionId: listingRevision.id },
        revision: listingRevision,
      },
      '"revision-2"',
    ),
  );
  render(<Listings {...props} id={listing.id} access={administrator} />);
  await screen.findByRole("button", { name: "New listing revision" });
  click("New listing revision");
  await screen.findByLabelText("Display name");
  expect(screen.getByLabelText("Icon alternative text")).toHaveProperty(
    "value",
    "Old icon",
  );
  change("Icon HTTPS URL", "https://images.example/robot.png");
  change("Icon alternative text", "Team robot");
  change("Icon attribution", "Example team");
  await confirmCommand("Create listing revision");
  await screen.findByRole("link", {
    name: "Reload listing to review saved revision",
  });
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      extensions: {
        [iri]: {
          schemaVersion: 1,
          icon: {
            url: "https://images.example/robot.png",
            alt: "Team robot",
            rights: { basis: "owned", attribution: "Example team" },
          },
        },
      },
    },
  });
  const body = post.mock.calls[0]?.[1] as {
    body: { extensions: Record<string, { icon: Record<string, unknown> }> };
  };
  expect(body.body.extensions[iri]!.icon).not.toHaveProperty("digest");
});

it.each(["no-history", "history-only", "history-and-content"])(
  "reads listing metadata with independently permitted history: %s",
  async (mode) => {
    const history = mode !== "no-history";
    const content = mode === "history-and-content";
    const w = screenWorkspace();
    readReplies(w.get, routes());
    const permissions: typeof w.props.access.permissions = [
      "resource.read",
      ...(history ? ["resource.read_history" as const] : []),
      ...(content ? ["resource.read_content" as const] : []),
    ];
    render(
      <Listings
        {...w.props}
        id={listing.id}
        access={{
          ...w.props.access,
          permissions,
          availablePermissions: permissions,
          namespaceGrants: [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "Listing workspace" });
    if (history) {
      await screen.findByRole("heading", { name: "Listing revision history" });
      await screen.findByText("Revision 1 by author", { exact: true });
      expect(
        screen.queryByRole("link", { name: "Revision 1 by author" }) !== null,
      ).toBe(content);
    } else {
      expect(
        screen.queryByRole("button", { name: "Revision history" }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Release history" }),
      ).toBeNull();
      expect(
        screen.queryByRole("link", { name: "Review submissions" }),
      ).toBeNull();
    }
    expect(
      w.get.mock.calls.some(
        ([path]) => path === "/api/v1/catalog-listings/{listingId}/revisions",
      ),
    ).toBe(history);
  },
);

it.each([true, false])(
  "reconciles a saved Listing candidate immediately (prior candidate=%s)",
  async (prior) => {
    const w = screenWorkspace();
    readReplies(w.get, routes(prior));
    w.post.mockResolvedValue(
      etagReply(
        {
          listing: { ...listing, candidateRevisionId: "new-candidate" },
          revision: { ...listingRevision, id: "new-candidate" },
        },
        '"revision-2"',
      ),
    );
    render(<Listings {...w.props} id={listing.id} access={administrator} />);
    await screen.findByRole("button", { name: "New listing revision" });
    click("New listing revision");
    await screen.findByLabelText("Display name");
    if (!prior) {
      await screen.findByRole("option", { name: version.version });
      change("Resource version", version.id);
      await screen.findByRole("option", { name: "Revision 2 by author" });
      change("Version revision", "version-rev-2");
      await screen.findByRole("option", {
        name: `${publisher.displayName} / ${publisher.id}`,
      });
      change("Publisher", publisher.id);
    }
    change("Display name", "Exact new listing");
    change("Description", "New description");
    await confirmCommand("Create listing revision");
    await screen.findByText("Listing revision saved.");
    expect(
      screen.getByRole("link", { name: "Inspect candidate revision" }),
    ).toHaveProperty(
      "href",
      "http://localhost:3000/unit/listing-revisions/new-candidate",
    );
    expect(
      screen.getByRole("button", { name: "Submit for review" }),
    ).toHaveProperty("disabled", false);
    w.post.mockResolvedValue(
      etagReply({ ...submission, listingRevisionId: "concurrent-candidate" }),
    );
    click("Submit for review");
    const facts = within(screen.getByRole("dialog")).getByLabelText(
      "Command summary",
    );
    expect(facts.textContent).toContain("new-candidate");
    expect(facts.textContent).toContain("not a precondition");
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await screen.findByText(
      /Submitted listing revision concurrent-candidate.*changed since confirmation/,
    );
    const acknowledgedLink = screen.getByRole("link", {
      name: "Open review submission",
    });
    click("Prepare another review submission");
    expect(acknowledgedLink.isConnected).toBe(true);
    expect(
      screen.getByRole("button", { name: "Submit for review" }),
    ).toHaveProperty("disabled", false);
    click("Submit for review");
    click("Cancel");
    expect(w.post).toHaveBeenCalledTimes(2);
    expect(w.post.mock.calls.at(-1)?.[1]).toMatchObject({ body: {} });
    expect(
      (
        w.post.mock.calls.at(-1)?.[1] as {
          params: { header: Record<string, string> };
        }
      ).params.header["If-Match"],
    ).toBeUndefined();
  },
);

it.each([
  "listing-id",
  "namespace",
  "resource",
  "listing-tenant",
  "revision-listing",
  "revision-tenant",
  "candidate",
  "etag-missing",
  "etag-weak",
  "etag-old",
])(
  "refuses to adopt a mismatched Listing acknowledgement (%s) and preserves exact recovery",
  async (mismatch) => {
    const w = screenWorkspace();
    readReplies(w.get, routes(true));
    const updatedListing = { ...listing, candidateRevisionId: "new-candidate" };
    const updatedRevision = { ...listingRevision, id: "new-candidate" };
    if (mismatch === "listing-id") updatedListing.id = "other";
    if (mismatch === "namespace") updatedListing.namespaceId = "other";
    if (mismatch === "resource") updatedListing.resourceId = "other";
    if (mismatch === "listing-tenant") updatedListing.tenantId = "other";
    if (mismatch === "revision-listing") updatedRevision.listingId = "other";
    if (mismatch === "revision-tenant") updatedRevision.tenantId = "other";
    if (mismatch === "candidate") updatedListing.candidateRevisionId = "other";
    const etag =
      mismatch === "etag-missing"
        ? null
        : mismatch === "etag-weak"
          ? 'W/"revision-2"'
          : mismatch === "etag-old"
            ? '"revision-1"'
            : '"revision-2"';
    w.post
      .mockResolvedValueOnce(
        etagReply({ listing: updatedListing, revision: updatedRevision }, etag),
      )
      .mockResolvedValue(
        etagReply(
          {
            listing: { ...listing, candidateRevisionId: "new-candidate" },
            revision: { ...listingRevision, id: "new-candidate" },
          },
          '"revision-2"',
        ),
      );
    render(<Listings {...w.props} id={listing.id} access={administrator} />);
    await screen.findByRole("button", { name: "New listing revision" });
    click("New listing revision");
    await screen.findByLabelText("Display name");
    change("Display name", "Captured name");
    await confirmCommand("Create listing revision");
    await screen.findByText(/outcome is unknown/);
    expect(
      screen
        .getByRole("link", { name: "Inspect candidate revision" })
        .getAttribute("href"),
    ).toBe("/unit/listing-revisions/listing-rev-1");
    expect(screen.queryByText("Listing revision saved.")).toBeNull();
    click("Retry same command");
    await screen.findByText("Listing revision saved.");
    expect(w.post.mock.calls[0]).toEqual(w.post.mock.calls[1]);
    expect(
      screen
        .getByRole("link", { name: "Inspect candidate revision" })
        .getAttribute("href"),
    ).toBe("/unit/listing-revisions/new-candidate");
  },
);

it("retains an exact submission receipt without offering an unauthorized history link", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes(true));
  w.post.mockResolvedValue(etagReply(submission));
  const access = {
    ...administrator,
    permissions: [
      "resource.read",
      "review.submit",
    ] as typeof administrator.permissions,
    availablePermissions: [
      "resource.read",
      "review.submit",
    ] as typeof administrator.permissions,
    namespaceGrants: [],
  };
  render(<Listings {...w.props} id={listing.id} access={access} />);
  await screen.findByRole("button", { name: "Submit for review" });
  await confirmCommand("Submit for review");
  await screen.findByText(new RegExp(`Submission ${submission.id}`));
  expect(
    screen.queryByRole("link", { name: "Open review submission" }),
  ).toBeNull();
  expect(w.post).toHaveBeenCalledOnce();
});
