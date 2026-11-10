// @vitest-environment jsdom
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { AuthoringWorkspace } from "./AuthoringWorkspace";
import { ListingForm, type ListingFormHandle } from "./Listings";
import {
  screenLifecycle,
  screenWorkspace,
  administrator,
  namespace,
  resource,
  publisher,
  listing,
  readReplies,
  click as press,
  confirmCommand,
  change,
  etagReply,
} from "../../tests/unit/screens";
import { listingRevision } from "../../tests/unit/responses";
import { editorDraft } from "./draft-editor";
import { newRepresentation } from "./representations";
import { readDraftFile, writeDraftFile, type DraftFile } from "./draft-file";
import { artworkIRI } from "./artwork";
import { deferred } from "../../tests/unit/fixtures";

screenLifecycle("/unit/resources/new");
const click = (name: string) => {
  if (
    ["Import draft JSON", "Download draft JSON"].includes(name) &&
    !screen.queryByRole("button", { name })
  )
    fireEvent.click(screen.getByText("Import or download draft JSON"));
  press(name);
};
const create = vi.fn((_blob: Blob) => "blob:editor-file");
beforeEach(() =>
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = vi.fn();
    },
  ),
);
afterEach(() => {
  vi.unstubAllGlobals();
  create.mockClear();
});
function file(): DraftFile {
  return {
    ...editorDraft("agent", resource.canonicalIdentifier, "release two", [
      {
        ...newRepresentation("agent"),
        content: '{"name":"Unfinished", "n":9007199254740993',
      },
    ]),
    listing: {
      displayName: " Imported name ",
      description: "",
      visibility: "private",
      tags: [" one ", " one "],
      representativeQueries: ["", " last "],
      relationships: [
        { type: "unsupported", targetIdentifier: "", targetVersion: "" },
      ],
      extensions: '{"exact":9007199254740993',
      artwork: { schemaVersion: 1, icon: { url: "", alt: "" } },
    },
  };
}
async function select(value = file()) {
  const bytes = new TextEncoder().encode(writeDraftFile(value)).buffer;
  await act(async () =>
    fireEvent.change(screen.getByLabelText("Choose draft JSON file"), {
      target: {
        files: [{ size: bytes.byteLength, arrayBuffer: async () => bytes }],
      },
    }),
  );
  click("Use this draft");
}
it("imports all fields without commands, exposes raw snapshots and refuses incomplete Save", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    "/api/v1/namespaces": { items: [namespace] },
    "/api/v1/publishers": { items: [publisher] },
  });
  render(<AuthoringWorkspace {...props} access={administrator} />);
  await select();
  expect(document.activeElement?.textContent).toContain(
    "Draft imported into the editor",
  );
  expect(
    (screen.getByLabelText("Canonical resource identifier") as HTMLInputElement)
      .value,
  ).toBe(resource.canonicalIdentifier);
  await screen.findByRole("option", { name: /Tools/ });
  change("Namespace", namespace.id);
  await screen.findByLabelText("Display name");
  expect(
    (screen.getByLabelText("Display name") as HTMLInputElement).value,
  ).toBe(" Imported name ");
  expect(
    (screen.getByLabelText("Description") as HTMLTextAreaElement).value,
  ).toBe("");
  expect((screen.getByLabelText("Publisher") as HTMLSelectElement).value).toBe(
    "",
  );
  expect(
    (screen.getByLabelText("Relationship type") as HTMLSelectElement).value,
  ).toBe("unsupported");
  expect(
    (screen.getByLabelText("Icon rights basis") as HTMLSelectElement).value,
  ).toBe("");
  click("Download draft JSON");
  expect(
    await screen.findByRole("link", { name: "Save catalog-draft.json" }),
  ).toBeTruthy();
  const blob = create.mock.calls.at(-1)![0];
  const text = await new Promise<string>((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(blob);
  });
  const downloaded = readDraftFile(text);
  expect(downloaded.representations).toEqual(file().representations);
  expect(downloaded.listing).toMatchObject(file().listing!);
  expect(text).not.toContain(publisher.id);
  expect(post).not.toHaveBeenCalled();
  click("Next: Content");
  click("Next: Listing details");
  await screen.findByRole("alert");
  expect(post).not.toHaveBeenCalled();
});
it.each(["imported", "existing"] as const)(
  "new Versions use the selected %s metadata and fixed Resource identity",
  async (choice) => {
    const { props, get, post } = screenWorkspace();
    get.mockImplementation(async (path) =>
      etagReply(
        {
          "/api/v1/resources/{resourceId}/listings": {
            items: [{ ...listing, candidateRevisionId: listingRevision.id }],
          },
          "/api/v1/catalog-listings/{listingId}": {
            ...listing,
            candidateRevisionId: listingRevision.id,
          },
          "/api/v1/listing-revisions/{revisionId}": listingRevision,
          "/api/v1/publishers": { items: [publisher] },
        }[String(path)],
      ),
    );
    render(
      <AuthoringWorkspace
        {...props}
        access={administrator}
        resource={resource}
      />,
    );
    const value = file();
    const bytes = new TextEncoder().encode(writeDraftFile(value)).buffer;
    await act(async () =>
      fireEvent.change(screen.getByLabelText("Choose draft JSON file"), {
        target: {
          files: [{ size: bytes.byteLength, arrayBuffer: async () => bytes }],
        },
      }),
    );
    change("Listing metadata to use", choice);
    click("Use this draft");
    await screen.findByLabelText("Display name");
    expect(
      (screen.getByLabelText("Display name") as HTMLInputElement).value,
    ).toBe(
      choice === "imported"
        ? value.listing!.displayName
        : listingRevision.displayName,
    );
    expect(
      (screen.getByLabelText("Version label") as HTMLInputElement).value,
    ).toBe(value.version);
    expect(post).not.toHaveBeenCalled();
  },
);
it("exports incomplete Listing fields independently of Save normalization and omits sealed artwork", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, { "/api/v1/publishers": { items: [publisher] } });
  const ref: { current: ListingFormHandle | undefined } = {
    current: undefined,
  };
  const view = render(
    <ListingForm
      {...props}
      access={administrator}
      embedded
      namespaceId={namespace.id}
      initial={null}
      formHandle={ref}
      snapshot={file().listing}
    />,
  );
  await waitFor(() => expect(ref.current).toBeTruthy());
  expect(ref.current!.snapshot()).toMatchObject(file().listing!);
  expect(() => ref.current!.read("revision")).toThrow();
  view.unmount();
  const handle: { current: ListingFormHandle | undefined } = {
    current: undefined,
  };
  render(
    <ListingForm
      {...props}
      access={administrator}
      embedded
      namespaceId={namespace.id}
      initial={{
        ...listingRevision,
        visibility: "private",
        digest: "sha256:" + "a".repeat(64),
        createdBy: "unit",
        createdAt: "2026-10-01T00:00:00Z",
        extensions: {
          [artworkIRI]: {
            schemaVersion: 1,
            icon: {
              url: "https://example.test/icon.png",
              alt: "icon",
              digest: "sha256:" + "a".repeat(64),
              mediaType: "image/png",
              rightsAssessment: "not_assessed",
              size: 1,
              width: 1,
              height: 1,
              rights: { basis: "owned", attribution: "author" },
            },
          },
        },
      }}
      formHandle={handle}
    />,
  );
  expect(handle.current!.snapshot().artwork?.icon).not.toHaveProperty("digest");
  expect(handle.current!.snapshot().extensions).toBe("{}");
});

it("requires AIR helper input to be applied or cleared before download", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, { "/api/v1/namespaces": { items: [namespace] } });
  render(<AuthoringWorkspace {...props} access={administrator} />);
  fireEvent.click(screen.getByText("Build an AIR identifier"));
  change("Publisher domain", "example.com");
  change("AIR resource name", "draft");
  click("Download draft JSON");
  expect(screen.getByRole("alert").textContent).toContain("Apply or clear");
  click("Clear AIR builder");
  click("Download draft JSON");
  expect(
    await screen.findByRole("link", { name: "Save catalog-draft.json" }),
  ).toBeTruthy();
  change("Publisher domain", "example.com");
  change("AIR resource name", "draft");
  click("Use AIR identifier");
  click("Download draft JSON");
  expect(screen.queryByRole("alert")).toBeNull();
});
it("preserves exact content and refuses export until an incomplete Listing lookup is explicitly recovered", async () => {
  const workspace = screenWorkspace();
  const currentListing = {
    ...listing,
    candidateRevisionId: listingRevision.id,
  };
  const metadata = {
    ...listingRevision,
    displayName: "Retained Listing",
    description: "Current metadata stays separate from the unsaved content.",
    tags: ["operations, support"],
    categories: ["Tools"],
    representativeQueries: ["How can this help?"],
    documentationUrl: "https://docs.example.test/helper",
    supportUrl: "https://support.example.test/helper",
    termsUrl: "https://terms.example.test/helper",
    license: "Apache-2.0",
  };
  readReplies(workspace.get, {
    "/api/v1/catalog-listings/{listingId}": currentListing,
    "/api/v1/listing-revisions/{revisionId}": metadata,
    "/api/v1/publishers": { items: [publisher] },
  });
  const reads = workspace.get.getMockImplementation()!;
  const pending = deferred<ReturnType<typeof etagReply>>();
  const recovered = deferred<ReturnType<typeof etagReply>>();
  const lookup = vi
    .fn<(...args: unknown[]) => Promise<unknown>>()
    .mockReturnValueOnce(pending.promise)
    .mockReturnValueOnce(recovered.promise);
  workspace.get.mockImplementation((path, ...options) =>
    path === "/api/v1/resources/{resourceId}/listings"
      ? lookup(...options)
      : reads(path, ...options),
  );
  render(
    <AuthoringWorkspace
      {...workspace.props}
      access={administrator}
      resource={resource}
    />,
  );
  const content = '{ "name": "Unsent helper", "extension": [true, "literal"] }';
  change("Version label", "unsaved-version");
  change("Standards document (JSON)", content);
  const label = screen.getByLabelText("Version label");
  const document = screen.getByLabelText("Standards document (JSON)");
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  click("Next: Listing details");
  const details = await screen.findByRole("region", {
    name: "Listing details",
  });
  await waitFor(() => expect(lookup).toHaveBeenCalledOnce());
  expect(lookup.mock.calls[0]![0]).toMatchObject({
    params: { path: { resourceId: resource.id }, query: { limit: 2 } },
  });
  expect(
    (lookup.mock.calls[0]![0] as { signal: AbortSignal }).signal,
  ).toBeInstanceOf(AbortSignal);
  const refuse = () => {
    click("Download draft JSON");
    expect(
      screen.getByText(
        "Wait for the complete Listing lookup before downloading draft JSON.",
      ),
    ).toBeTruthy();
    expect(create).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("link", { name: "Save catalog-draft.json" }),
    ).toBeNull();
    expect(label).toHaveProperty("value", "unsaved-version");
    expect(document).toHaveProperty("value", content);
    expect(workspace.post).not.toHaveBeenCalled();
    expect(workspace.patch).not.toHaveBeenCalled();
    expect(workspace.remove).not.toHaveBeenCalled();
  };
  refuse();
  await act(async () =>
    pending.resolve(
      etagReply({ items: [currentListing], nextCursor: "unfinished-page" }),
    ),
  );
  await within(details).findByText("This page could not be loaded.");
  expect(workspace.props.onFailure).toHaveBeenCalledWith(
    new Error(
      "The Listing lookup is incomplete or does not match this Resource. No new Listing will be created.",
    ),
  );
  expect(workspace.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources/{resourceId}/listings",
  ]);
  refuse();
  fireEvent.click(within(details).getByRole("button", { name: "Reload" }));
  await waitFor(() => expect(lookup).toHaveBeenCalledTimes(2));
  refuse();
  await act(async () =>
    recovered.resolve(etagReply({ items: [currentListing] })),
  );
  await within(details).findByLabelText("Display name");
  await waitFor(() =>
    expect(within(details).getByLabelText("Display name")).toHaveProperty(
      "value",
      metadata.displayName,
    ),
  );
  click("Download draft JSON");
  expect(create).toHaveBeenCalledOnce();
  expect(
    await screen.findByRole("link", { name: "Save catalog-draft.json" }),
  ).toBeTruthy();
  const text = await new Promise<string>((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(create.mock.calls[0]![0]);
  });
  const downloaded = readDraftFile(text);
  expect(downloaded).toMatchObject({
    format: "open-agentic-catalog-draft-v1",
    kind: "agent",
    canonicalIdentifier: resource.canonicalIdentifier,
    version: "unsaved-version",
    representations: [
      {
        role: "primary",
        mediaType: "application/a2a-agent-card+json",
        profileId: "",
        input: { mode: "data", text: content },
      },
    ],
  });
  expect(downloaded.listing).toEqual({
    displayName: metadata.displayName,
    description: metadata.description,
    visibility: "private",
    tags: ["operations, support"],
    categories: ["Tools"],
    representativeQueries: ["How can this help?"],
    documentationUrl: metadata.documentationUrl,
    supportUrl: metadata.supportUrl,
    termsUrl: metadata.termsUrl,
    license: "Apache-2.0",
    relationships: [],
    extensions: "{}",
    artwork: { schemaVersion: 1 },
  });
  expect(workspace.get).toHaveBeenCalledWith(
    "/api/v1/catalog-listings/{listingId}",
    expect.objectContaining({
      params: { path: { listingId: listing.id } },
    }),
  );
  expect(workspace.get).toHaveBeenCalledWith(
    "/api/v1/listing-revisions/{revisionId}",
    expect.objectContaining({
      params: { path: { revisionId: listingRevision.id } },
    }),
  );
  expect(workspace.props.onFailure).toHaveBeenCalledOnce();
  expect(lookup).toHaveBeenCalledTimes(2);
  expect(label).toHaveProperty("value", "unsaved-version");
  expect(document).toHaveProperty("value", content);
  expect(workspace.post).not.toHaveBeenCalled();
  expect(workspace.patch).not.toHaveBeenCalled();
  expect(workspace.remove).not.toHaveBeenCalled();
});
it("captures a minimal Listing and rejects lossy server extension exports until text is entered", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, { "/api/v1/publishers": { items: [publisher] } });
  const handle: { current: ListingFormHandle | undefined } = {
    current: undefined,
  };
  const view = render(
    <ListingForm
      {...props}
      access={administrator}
      embedded
      namespaceId={namespace.id}
      initial={null}
      formHandle={handle}
      snapshot={{
        displayName: "",
        description: "",
        visibility: "private",
        relationships: [{ type: "", targetIdentifier: "" }],
      }}
    />,
  );
  expect(handle.current!.snapshot()).toMatchObject({
    displayName: "",
    description: "",
    visibility: "private",
    tags: [],
    categories: [],
    representativeQueries: [],
    relationships: [{ type: "", targetIdentifier: "" }],
    extensions: "{}",
  });
  expect(
    (screen.getByLabelText("Relationship type") as HTMLSelectElement).value,
  ).toBe("");
  view.unmount();
  render(
    <ListingForm
      {...props}
      access={administrator}
      embedded
      namespaceId={namespace.id}
      initial={{
        ...listingRevision,
        visibility: "private",
        digest: "sha256:" + "a".repeat(64),
        createdBy: "unit",
        createdAt: "2026-10-01T00:00:00Z",
        extensions: { value: 9007199254740992 },
      }}
      formHandle={handle}
    />,
  );
  expect(() => handle.current!.snapshot()).toThrow("exact export");
  change("Extensions (JSON object)", '{"value":9007199254740993}');
  expect(handle.current!.snapshot().extensions).toBe(
    '{"value":9007199254740993}',
  );
});

it("captures a fresh optional Listing before any server metadata exists", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, { "/api/v1/publishers": { items: [publisher] } });
  const handle: { current: ListingFormHandle | undefined } = {
    current: undefined,
  };
  render(
    <ListingForm
      {...props}
      access={administrator}
      embedded
      namespaceId={namespace.id}
      initial={null}
      formHandle={handle}
    />,
  );
  expect(handle.current!.snapshot()).toEqual({
    displayName: "",
    description: "",
    visibility: "private",
    tags: [],
    categories: [],
    representativeQueries: [],
    documentationUrl: "",
    supportUrl: "",
    termsUrl: "",
    license: "",
    relationships: [],
    extensions: "{}",
    artwork: { schemaVersion: 1 },
  });
});

it("clears the completed import notice before saving so correction keeps its normal focus", async () => {
  const { props, post } = screenWorkspace();
  const { bundle, native } =
    await import("../../tests/unit/authoring-fixtures");
  post.mockResolvedValue(etagReply(bundle));
  render(
    <AuthoringWorkspace
      {...props}
      access={administrator}
      resource={resource}
    />,
  );
  const value = editorDraft(
    "agent",
    resource.canonicalIdentifier,
    bundle.version.version,
    [{ ...newRepresentation("agent"), content: JSON.stringify(native) }],
  );
  await select(value);
  expect(document.activeElement?.textContent).toContain(
    "Draft imported into the editor",
  );
  await confirmCommand("Create version");
  expect(
    screen.queryByText(
      "Draft imported into the editor. Check its fields before saving.",
    ),
  ).toBeNull();
  click("Correct this draft");
  await waitFor(() =>
    expect(document.activeElement).toBe(screen.getByLabelText("Version label")),
  );
});

it.each(["imported", "existing"] as const)(
  "%s metadata makes an explicit replacement or keeps the target's optional fields",
  async (choice) => {
    const { props, get } = screenWorkspace();
    readReplies(get, { "/api/v1/publishers": { items: [publisher] } });
    const handle: { current: ListingFormHandle | undefined } = {
      current: undefined,
    };
    const initial = {
      ...listingRevision,
      visibility: "private" as const,
      digest: "sha256:" + "a".repeat(64),
      createdBy: "unit",
      createdAt: "2026-10-01T00:00:00Z",
      tags: ["old tag"],
      categories: ["old category"],
      relationships: [
        {
          type: "depends_on" as const,
          targetIdentifier: "urn:example:old-target",
        },
      ],
    };
    render(
      <ListingForm
        {...props}
        access={administrator}
        embedded
        namespaceId={namespace.id}
        initial={initial}
        snapshot={
          choice === "imported"
            ? {
                displayName: "Imported",
                description: "",
                visibility: "private",
              }
            : undefined
        }
        formHandle={handle}
      />,
    );
    await waitFor(() => expect(handle.current).toBeTruthy());
    const captured = handle.current!.snapshot();
    expect(captured.tags).toEqual(choice === "imported" ? [] : initial.tags);
    expect(captured.categories).toEqual(
      choice === "imported" ? [] : initial.categories,
    );
    expect(captured.relationships).toEqual(
      choice === "imported" ? [] : initial.relationships,
    );
  },
);

it("keeps unsaved Listing edits when a new Resource changes its destination Namespace", async () => {
  const { props, get, post } = screenWorkspace();
  const second = {
    ...namespace,
    id: "second-namespace",
    slug: "second",
    displayName: "Second",
  };
  readReplies(get, {
    "/api/v1/namespaces": { items: [namespace, second] },
    "/api/v1/publishers": { items: [publisher] },
  });
  const { native, bundle } =
    await import("../../tests/unit/authoring-fixtures");
  render(<AuthoringWorkspace {...props} access={administrator} />);
  await select({
    ...editorDraft(
      "agent",
      resource.canonicalIdentifier,
      bundle.version.version,
      [{ ...newRepresentation("agent"), content: JSON.stringify(native) }],
    ),
    listing: {
      displayName: "Imported",
      description: "Initial description",
      visibility: "private",
    },
  });
  const namespaces = await screen.findByRole("combobox", {
    name: "Namespace",
  });
  await within(namespaces).findByRole("option", { name: /Tools/ });
  change("Namespace", namespace.id);
  click("Next: Content");
  click("Next: Listing details");
  const firstListing = within(
    await screen.findByRole("region", { name: "Listing details" }),
  );
  fireEvent.change(await firstListing.findByLabelText("Display name"), {
    target: { value: "My edited name" },
  });
  fireEvent.change(firstListing.getByLabelText("Description"), {
    target: { value: "My edited description" },
  });
  click("Back: Content");
  click("Back: Basics");
  change("Namespace", second.id);
  click("Next: Content");
  click("Next: Listing details");
  const nextListing = within(
    await screen.findByRole("region", { name: "Listing details" }),
  );
  expect(
    ((await nextListing.findByLabelText("Display name")) as HTMLInputElement)
      .value,
  ).toBe("My edited name");
  expect(
    (nextListing.getByLabelText("Description") as HTMLInputElement).value,
  ).toBe("My edited description");
  expect(post).not.toHaveBeenCalled();
});

it("refuses to save ambiguous decoded numeric extensions until the author supplies exact text", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/publishers": { items: [publisher] } });
  const source = '{"amount":0.10000000000000000000001}';
  const handle: { current: ListingFormHandle | undefined } = {
    current: undefined,
  };
  render(
    <ListingForm
      {...props}
      access={administrator}
      embedded
      namespaceId={namespace.id}
      initial={{
        ...listingRevision,
        visibility: "private",
        digest: "sha256:" + "a".repeat(64),
        createdBy: "unit",
        createdAt: "2026-10-01T00:00:00Z",
        extensions: JSON.parse(source) as Record<string, unknown>,
      }}
      formHandle={handle}
    />,
  );
  await waitFor(() => expect(handle.current).toBeTruthy());
  expect(() => handle.current!.read("next-content-revision", true)).toThrow(
    "exact export",
  );
  expect(post).not.toHaveBeenCalled();
  change("Extensions (JSON object)", '{"amount":0.1}');
  expect(
    handle.current!.read("next-content-revision", true).extensions,
  ).toEqual({ amount: 0.1 });
});
