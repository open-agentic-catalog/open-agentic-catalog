// @vitest-environment jsdom
// Scripted transport tests retain actual serializers, response predicates and command dialogs.
import { it, expect, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { AuthoringWorkspace } from "./AuthoringWorkspace";
import VersionSource from "./VersionSource";
import { newRepresentation } from "./representations";
import { newVersionContent } from "./new-version-content";
import {
  commandETag,
  savePreparedListing,
  type ListingCheckpoint,
} from "./authoring-save";
import { newMCPDocument, mcpFormats } from "./mcp-metadata";
import { reuseDraft } from "./draft-reuse";
import { versionCommandResult } from "./responses";
import { uncertain } from "../api/commands";
import { APIError } from "../api/client";
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
import { listingRevision } from "../../tests/unit/responses";

screenLifecycle("/unit/versions/new");
import { bundle, digest, native } from "../../tests/unit/authoring-fixtures";
const body = {
  versionRevisionId: "not-selected-again",
  displayName: "Draft name",
  description: "Draft description",
  publisherId: publisher.id,
  visibility: "private" as const,
};
const keys = { listing: "listing-key", revision: "revision-key" };
const savedListing = {
  listing: { ...listing, revision: 2, candidateRevisionId: listingRevision.id },
  revision: { ...listingRevision, versionRevisionId: bundle.revision.id },
};

it("binds a Version acknowledgement to its target, revision, tenant and evidence", () => {
  const target = {
    tenantId: resource.tenantId,
    resourceId: resource.id,
    label: version.version,
    versionId: version.id,
  };
  expect(versionCommandResult(bundle, target)).toBe(true);
  for (const field of ["tenantId", "resourceId", "label", "versionId"] as const)
    expect(
      versionCommandResult(bundle, { ...target, [field]: "different" }),
    ).toBe(false);
  for (const [section, field] of [
    ["version", "candidateRevisionId"],
    ["version", "id"],
    ["version", "tenantId"],
    ["revision", "tenantId"],
    ["revision", "evidenceSetId"],
    ["revision", "contentSetDigest"],
  ] as const) {
    const bad = structuredClone(bundle);
    Object.assign(bad[section], { [field]: "different" });
    expect(versionCommandResult(bad, target)).toBe(false);
  }
  expect(versionCommandResult({ ...bundle, revision: null }, target)).toBe(
    false,
  );
});
it.each([null, "unquoted", 'W/"weak"', '""'])(
  "refuses an unusable write ETag %j",
  (etag) => {
    expect(() =>
      commandETag(new Response(null, { headers: etag ? { ETag: etag } : {} })),
    ).toThrow("uncertain");
  },
);
it.each(["agent", "mcp_server"] as const)(
  "changes only the supported %s native version in final input",
  (kind) => {
    const doc =
      kind === "agent"
        ? native
        : {
            ...newMCPDocument("registry", "1.0.0"),
            packages: [{ version: "8.2.1", identifier: "pkg:keep" }],
            protocolVersion: "do not infer",
          };
    const row = {
      ...newRepresentation(kind),
      mediaType:
        kind === "agent"
          ? "application/a2a-agent-card+json"
          : mcpFormats.registry.media,
      content: JSON.stringify(doc),
    };
    const outcome = newVersionContent(kind, [row], "release two");
    expect(JSON.parse(outcome.drafts[0]!.content)).toEqual({
      ...doc,
      version: "release two",
    });
    expect(row.content).toBe(JSON.stringify(doc));
    expect(outcome.changes).toHaveLength(1);
    expect(
      newVersionContent(kind, outcome.drafts, "release two").changes,
    ).toEqual([]);
  },
);
it("keeps unsupported, empty, URL, Skill and supplementary documents intact", () => {
  const rows = [
    newRepresentation("agent"),
    { ...newRepresentation("agent"), content: '{"name":"advanced"}' },
    {
      ...newRepresentation("agent"),
      mode: "url" as const,
      content: JSON.stringify(native),
      url: "https://example.test/card",
    },
  ];
  expect(newVersionContent("agent", rows, "2").drafts).toEqual(rows);
  expect(
    newVersionContent("skill", [{ ...rows[0]!, content: "opaque" }], "2")
      .changes,
  ).toEqual([]);
  expect(
    newVersionContent(
      "mcp_server",
      [{ ...rows[0]!, mediaType: "application/json", content: "{}" }],
      "2",
    ).changes,
  ).toEqual([]);
  const missing = {
    ...newRepresentation("agent"),
    content: JSON.stringify({ ...native, version: undefined }),
  };
  expect(newVersionContent("agent", [missing], "2").changes[0]).toContain(
    "not set",
  );
});
it("requires an explicit unsigned or externally re-signed Card", () => {
  const row = {
    ...newRepresentation("agent"),
    content: JSON.stringify({
      ...native,
      signatures: [{ signature: "retain-exact" }],
    }),
  };
  expect(() => newVersionContent("agent", [row], "2")).toThrow("unsigned");
  expect(newVersionContent("agent", [row], "1.0.0").drafts[0]).toBe(row);
});
it("captures separate Listing writes, exact content binding and returned concurrency", async () => {
  const w = screenWorkspace();
  w.post
    .mockResolvedValueOnce(etagReply(listing))
    .mockResolvedValueOnce(etagReply(savedListing, '"revision-2"'));
  const checkpoint: ListingCheckpoint = {};
  const acknowledged = vi.fn();
  await savePreparedListing(
    w.props.client,
    resource,
    bundle,
    { body },
    checkpoint,
    keys,
    acknowledged,
  );
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/catalog-listings",
    {
      body: { namespaceId: namespace.id, resourceId: resource.id },
      params: { header: { "Idempotency-Key": "listing-key" } },
    },
  ]);
  expect(w.post.mock.calls[1]).toMatchObject([
    "/api/v1/catalog-listings/{listingId}/revisions",
    {
      body: { ...body, versionRevisionId: bundle.revision.id },
      params: {
        header: {
          "Idempotency-Key": "revision-key",
          "If-Match": '"revision-1"',
        },
      },
    },
  ]);
  expect(acknowledged).toHaveBeenCalledTimes(2);
  expect(checkpoint.etag).toBe('"revision-2"');
  await savePreparedListing(
    w.props.client,
    resource,
    bundle,
    { body },
    checkpoint,
    keys,
  );
  expect(w.post).toHaveBeenCalledTimes(2);
});
it("keeps an acknowledged Listing and retries only its revision after a lost reply", async () => {
  const w = screenWorkspace();
  w.post
    .mockResolvedValueOnce(etagReply(listing))
    .mockRejectedValueOnce(new Error("lost response"))
    .mockResolvedValueOnce(etagReply(savedListing, '"revision-2"'));
  const checkpoint: ListingCheckpoint = {};
  await expect(
    savePreparedListing(
      w.props.client,
      resource,
      bundle,
      { body },
      checkpoint,
      keys,
    ),
  ).rejects.toThrow("lost");
  expect(checkpoint.listing?.id).toBe(listing.id);
  await savePreparedListing(
    w.props.client,
    resource,
    bundle,
    { body },
    checkpoint,
    keys,
  );
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[2]);
  expect(w.post).toHaveBeenCalledTimes(3);
});
it.each(["resourceId", "namespaceId", "tenantId"])(
  "does not continue from an unrelated Listing %s",
  async (field) => {
    const w = screenWorkspace();
    w.post.mockResolvedValue(etagReply({ ...listing, [field]: "other" }));
    await expect(
      savePreparedListing(w.props.client, resource, bundle, { body }, {}, keys),
    ).rejects.toMatchObject({ code: "invalid_api_response" });
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);
it.each(["id", "tenantId", "versionRevisionId", "listingId"])(
  "does not accept an unrelated saved Listing revision %s",
  async (field) => {
    const w = screenWorkspace();
    const changed = structuredClone(savedListing);
    Object.assign(changed.revision, { [field]: "other" });
    w.post.mockResolvedValue(etagReply(changed, '"revision-2"'));
    await expect(
      savePreparedListing(
        w.props.client,
        resource,
        bundle,
        { body },
        { listing, etag: '"revision-1"' },
        keys,
      ),
    ).rejects.toMatchObject({ code: "invalid_api_response" });
  },
);
it("uses only a single validated primary for empty name and description", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply(savedListing, '"revision-2"'));
  await savePreparedListing(
    w.props.client,
    resource,
    bundle,
    { body: { ...body, displayName: "", description: "" } },
    { listing, etag: '"revision-1"' },
    keys,
  );
  expect(w.post.mock.calls[0]).toMatchObject([
    expect.any(String),
    { body: { displayName: "Order Agent", description: "Explain an order." } },
  ]);
  for (const bad of [
    { ...bundle, evidence: { ...bundle.evidence, state: "blocked" as const } },
    {
      ...bundle,
      revision: {
        ...bundle.revision,
        representations: [
          ...bundle.revision.representations,
          ...bundle.revision.representations,
        ],
      },
    },
  ]) {
    try {
      await savePreparedListing(
        w.props.client,
        resource,
        bad,
        { body: { ...body, displayName: "", description: "" } },
        {},
        keys,
      );
      throw new Error("should refuse");
    } catch (error) {
      expect(error).toBeInstanceOf(APIError);
      expect(uncertain(error)).toBe(false);
    }
  }
  await expect(
    savePreparedListing(
      w.props.client,
      resource,
      bundle,
      { body },
      { listing },
      keys,
    ),
  ).rejects.toThrow("captured ETag");
});
it("populates Plugin documents while retaining the URL, digest and missing original inventory", async () => {
  const w = screenWorkspace();
  const plugin = {
    rootDirectory: "bundle",
    expandedSize: 5,
    files: [
      { path: "bundle/plugin.json", size: 1, digest, executable: false },
      { path: "bundle/scripts/tool.py", size: 1, digest, executable: true },
    ],
    documents: [
      {
        path: "bundle/plugin.json",
        content: '{"name":"helper","advanced":{"retain":true}}',
      },
      { path: "bundle/mcp.json", content: '{"mcpServers":{}}' },
      {
        path: "bundle/skills/helper/SKILL.md",
        content: "---\nname: helper\ndescription: Help\n---\nHelp\n",
      },
    ],
    assessments: {
      malware: "not_assessed" as const,
      secrets: "not_assessed" as const,
      license: "not_assessed" as const,
      sbom: "not_assessed" as const,
      provenance: "not_assessed" as const,
    },
  };
  const drafts = await reuseDraft(
    w.props.client,
    "plugin",
    {
      representations: [
        {
          ...bundle.revision.representations[0]!,
          plugin,
          url: "https://example.test/plugin.zip",
          contentDigest: digest,
        },
      ],
    },
    new AbortController().signal,
  );
  expect(drafts[0]).toMatchObject({
    mode: "url",
    expectedDigest: digest,
    pluginPreparation: {
      preparing: false,
      manifest: plugin.documents[0]!.content,
      mcp: plugin.documents[1]!.content,
      skills: [{ content: plugin.documents[2]!.content }],
      sourceFiles: [
        { path: "bundle/plugin.json", retained: true },
        { path: "bundle/scripts/tool.py", retained: false },
      ],
    },
  });
  expect(w.get).not.toHaveBeenCalled();
});
it("reads complete exact source content without a mutation and requires explicit history browsing", async () => {
  const w = screenWorkspace();
  const onApply = vi.fn();
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": {
      namespaceId: namespace.id,
      revision: bundle.revision,
      evidence: bundle.evidence,
    },
    "/api/v1/resource-versions/{versionId}": {
      ...bundle.version,
      sealedRevisionId: bundle.revision.id,
    },
    "/api/v1/resources/{resourceId}/versions": {
      items: [{ ...bundle.version, sealedRevisionId: bundle.revision.id }],
    },
    "/api/v1/resource-versions/{versionId}/revisions": {
      items: [
        { id: bundle.revision.id, revisionNumber: 1, createdBy: "author" },
      ],
    },
  });
  render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      drafts={[newRepresentation("agent")]}
      onApply={onApply}
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  click("Browse source history");
  await screen.findByRole("option", { name: "1.0.0 (sealed)" });
  change("Source Version", version.id);
  await screen.findByRole("option", { name: "Revision 1 by author" });
  change("Source Revision", bundle.revision.id);
  click("Load source revision");
  await waitFor(() => expect(onApply).toHaveBeenCalledOnce());
  expect(onApply.mock.calls[0]?.[0]).toMatchObject([
    { content: JSON.stringify(native, null, 2), role: "primary" },
  ]);
  expect(w.post).not.toHaveBeenCalled();
});
it("cancels an initial source read without replacing inputs", async () => {
  const w = screenWorkspace();
  const read = deferred<unknown>();
  w.get.mockReturnValue(read.promise);
  const onApply = vi.fn();
  render(
    <VersionSource
      {...w.props}
      access={administrator}
      resource={resource}
      initialRevision={bundle.revision.id}
      drafts={[]}
      onApply={onApply}
    />,
  );
  await screen.findByRole("button", { name: "Cancel source read" });
  click("Cancel source read");
  await act(async () =>
    read.resolve(
      etagReply({
        namespaceId: namespace.id,
        revision: bundle.revision,
        evidence: bundle.evidence,
      }),
    ),
  );
  expect(onApply).not.toHaveBeenCalled();
});
it("preserves optional Listing input through Back and corrects content using its returned ETag", async () => {
  // Load the real lazy module before testing form interactions. Browser tests
  // separately exercise chunk loading; this unit retains the actual editor.
  await import("./ListingPreparation");
  const w = screenWorkspace();
  const publishers = deferred<unknown>();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}/listings": { items: [] },
  });
  const route = w.get.getMockImplementation()!;
  w.get.mockImplementation((path, ...args) =>
    path === "/api/v1/publishers" ? publishers.promise : route(path, ...args),
  );
  const blocked = {
    ...bundle,
    evidence: {
      ...bundle.evidence,
      state: "blocked",
      findings: [
        {
          code: "NATIVE_VERSION_MISMATCH",
          severity: "error",
          path: "/representations/0/version",
          message: "Native version must match.",
        },
        {
          code: "OPAQUE",
          severity: "warning",
          path: "",
          message: "Retain this diagnostic.",
        },
      ],
    },
  };
  const corrected = {
    ...bundle,
    version: {
      ...bundle.version,
      revision: 2,
      candidateRevisionId: "revision-corrected",
    },
    revision: {
      ...bundle.revision,
      id: "revision-corrected",
      revisionNumber: 2,
    },
  };
  w.post
    .mockResolvedValueOnce(etagReply(blocked, '"v-1"'))
    .mockResolvedValueOnce(etagReply(corrected, '"v-2"'))
    .mockResolvedValueOnce(etagReply(listing))
    .mockResolvedValueOnce(
      etagReply(
        {
          ...savedListing,
          revision: {
            ...savedListing.revision,
            versionRevisionId: "revision-corrected",
          },
        },
        '"listing-2"',
      ),
    );
  render(
    <AuthoringWorkspace
      {...w.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", "1.0.0");
  change("Standards document (JSON)", JSON.stringify(native));
  await confirmCommand("Create version");
  await screen.findByText("Native version must match.");
  expect(screen.getByText("Retain this diagnostic.")).toBeTruthy();
  click("Correct native version");
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "readOnly",
    true,
  );
  change(
    "Standards document (JSON)",
    JSON.stringify({ ...native, description: "Corrected description" }),
  );
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  click("Next: Listing details");
  const details = await screen.findByRole("region", {
    name: "Listing details",
  });
  const publisherChoices = await within(details).findByRole("combobox", {
    name: "Publisher",
  });
  expect(w.get).toHaveBeenCalledWith(
    "/api/v1/publishers",
    expect.objectContaining({ params: { query: { limit: 25 } } }),
  );
  expect(
    within(publisherChoices).queryByRole("option", {
      name: `${publisher.displayName} / ${publisher.id}`,
    }),
  ).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
  await act(async () => publishers.resolve(etagReply({ items: [publisher] })));
  await within(publisherChoices).findByRole("option", {
    name: `${publisher.displayName} / ${publisher.id}`,
  });
  change("Display name", "Entered listing name");
  change("Description", "Entered listing description");
  change("Publisher", publisher.id);
  click("Back: Content");
  click("Next: Listing details");
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Entered listing name",
  );
  expect(screen.getByLabelText("Description")).toHaveProperty(
    "value",
    "Entered listing description",
  );
  expect(screen.getByRole("combobox", { name: "Publisher" })).toHaveProperty(
    "value",
    publisher.id,
  );
  expect(w.post).toHaveBeenCalledTimes(1);
  await confirmCommand("Save corrected draft");
  await screen.findByRole("heading", { name: "Listing draft saved" });
  expect(w.post).toHaveBeenCalledTimes(4);
  expect(w.post.mock.calls[1]).toMatchObject([
    "/api/v1/resource-versions/{versionId}/revisions",
    { params: { header: { "If-Match": '"v-1"' } } },
  ]);
  expect(w.post.mock.calls[3]).toMatchObject([
    "/api/v1/catalog-listings/{listingId}/revisions",
    {
      body: {
        versionRevisionId: "revision-corrected",
        displayName: "Entered listing name",
        description: "Entered listing description",
        publisherId: publisher.id,
      },
    },
  ]);
});
