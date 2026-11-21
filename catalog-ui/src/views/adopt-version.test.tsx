// @vitest-environment jsdom
import { expect, it } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import UpstreamChanges, { SelectedUpstreamChange } from "./UpstreamChanges";
import { AdoptVersion } from "./AdoptVersion";
import { APIError } from "../api/client";
import {
  capabilities,
  evidence,
  versionRevision,
} from "../../tests/unit/responses";
import {
  administrator,
  click,
  confirmCommand,
  etagReply,
  namespace,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  stamp,
  version,
} from "../../tests/unit/screens";

screenLifecycle("/unit/adopt-version");
const occurrence = {
  observationId: "observation-2",
  sourceId: "source-1",
  sourceRevision: 2,
  snapshotId: "snapshot-2",
  retrievalDigest: "sha256:" + "a".repeat(64),
  entryLocation: "/entries/0",
  exactEntryDigest: "sha256:" + "b".repeat(64),
  normalizedEntryDigest: "sha256:" + "c".repeat(64),
  evidenceComplete: true as const,
  observedAt: stamp,
};
const target = {
  resourceId: resource.id,
  namespaceId: namespace.id,
  priorVersionRevisionId: "prior-adoption",
  version: "opaque-first",
  upstreamVersion: "opaque-next",
};
const created = {
  resourceId: resource.id,
  version: { ...version, version: "opaque-next" },
  versionRevision: {
    ...versionRevision,
    adoption: {
      observationId: occurrence.observationId,
      sourceId: occurrence.sourceId,
      sourceRevision: occurrence.sourceRevision,
      snapshotId: occurrence.snapshotId,
      retrievalDigest: occurrence.retrievalDigest,
      entryLocation: occurrence.entryLocation,
      exactEntryDigest: occurrence.exactEntryDigest,
      normalizedEntryDigest: "sha256:" + "c".repeat(64),
      normalizationProfile: "catalog-observation-v1",
      adoptedBy: administrator.subject,
      adoptedAt: stamp,
    },
  },
  evidence,
};
function setup(enabled = true) {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/capabilities": {
      ...capabilities,
      optionalCapabilities: {
        ...capabilities.optionalCapabilities,
        versionAdoption: enabled ? "enabled" : "disabled",
      },
    },
    "/api/v1/observations/{observationId}/adoption-target": target,
  });
  return w;
}
it("keeps disabled adoption read-only and never looks up a destination", async () => {
  const w = setup(false);
  render(
    <AdoptVersion
      {...w.props}
      access={administrator}
      id={occurrence.observationId}
      occurrence={occurrence}
    />,
  );
  await screen.findByText(
    "The administrator has not enabled upstream Version adoption.",
  );
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/capabilities",
  ]);
  expect(w.post).not.toHaveBeenCalled();
});
it("requires explicit lookup and confirmation, binds exact evidence and links the draft", async () => {
  const w = setup();
  w.post.mockResolvedValue(etagReply(created));
  render(
    <AdoptVersion
      {...w.props}
      access={administrator}
      id={occurrence.observationId}
      occurrence={occurrence}
    />,
  );
  await screen.findByRole("button", { name: "Find existing local Resource" });
  expect(w.get).toHaveBeenCalledTimes(1);
  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  expect(w.post).not.toHaveBeenCalled();
  await confirmCommand("Confirm new Version adoption");
  await screen.findByRole("link", { name: "Inspect adopted revision" });
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions:adopt",
    {
      params: {
        path: { resourceId: resource.id },
      },
      body: {
        observationId: occurrence.observationId,
        snapshotId: occurrence.snapshotId,
        retrievalDigest: occurrence.retrievalDigest,
        entryLocation: occurrence.entryLocation,
        priorVersionRevisionId: target.priorVersionRevisionId,
      },
    },
  ]);
  expect(
    screen
      .getByRole("link", { name: "Inspect adopted revision" })
      .getAttribute("href"),
  ).toContain(versionRevision.id);
});
it("recovers an uncertain result with the same frozen command and key", async () => {
  const w = setup();
  w.post
    .mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"))
    .mockResolvedValueOnce(etagReply(created));
  render(
    <AdoptVersion
      {...w.props}
      access={administrator}
      id={occurrence.observationId}
      occurrence={occurrence}
    />,
  );
  await screen.findByRole("button", { name: "Find existing local Resource" });
  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  await confirmCommand("Confirm new Version adoption");
  await screen.findByRole("alert");
  await screen.findByRole("button", { name: /Retry/ });
  click(/Retry/);
  await screen.findByRole("link", { name: "Inspect adopted revision" });
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
});
it.each([
  "resource",
  "observation",
  "source",
  "source-revision",
  "snapshot",
  "digest",
  "location",
  "exact-entry-digest",
  "normalized-entry-digest",
  "missing-proof",
])("treats mismatched %s success evidence as uncertain", async (field) => {
  const w = setup();
  const bad = structuredClone(created);
  switch (field) {
    case "resource":
      bad.resourceId = "other-resource";
      bad.version.resourceId = "other-resource";
      break;
    case "observation":
      bad.versionRevision.adoption.observationId = "another-observation";
      break;
    case "source":
      bad.versionRevision.adoption.sourceId = "another-source";
      break;
    case "source-revision":
      bad.versionRevision.adoption.sourceRevision++;
      break;
    case "snapshot":
      bad.versionRevision.adoption.snapshotId = "another-snapshot";
      break;
    case "digest":
      bad.versionRevision.adoption.retrievalDigest = "sha256:" + "f".repeat(64);
      break;
    case "location":
      bad.versionRevision.adoption.entryLocation = "/entries/9";
      break;
    case "exact-entry-digest":
      bad.versionRevision.adoption.exactEntryDigest =
        "sha256:" + "f".repeat(64);
      break;
    case "normalized-entry-digest":
      bad.versionRevision.adoption.normalizedEntryDigest =
        "sha256:" + "f".repeat(64);
      break;
    case "missing-proof":
      Reflect.deleteProperty(bad.versionRevision, "adoption");
      break;
  }
  w.post.mockResolvedValue(etagReply(bad));
  render(
    <AdoptVersion
      {...w.props}
      access={administrator}
      id={occurrence.observationId}
      occurrence={occurrence}
    />,
  );
  await screen.findByRole("button", { name: "Find existing local Resource" });
  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  await confirmCommand("Confirm new Version adoption");
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("link", { name: "Inspect adopted revision" }),
  ).toBeNull();
  expect(w.props.onFailure).toHaveBeenCalled();
});

it("disables confirmation when destination edit authority is removed", async () => {
  const w = setup();
  const props = { ...w.props, id: occurrence.observationId, occurrence };
  const view = render(<AdoptVersion {...props} access={administrator} />);
  await screen.findByRole("button", { name: "Find existing local Resource" });
  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  view.rerender(
    <AdoptVersion
      {...props}
      access={{
        ...administrator,
        permissions: administrator.permissions.filter(
          (p) => p !== "resource.edit",
        ),
        namespaceGrants: [],
      }}
    />,
  );
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Confirm new Version adoption" }),
    ).toHaveProperty("disabled", true),
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("returns a write-only receipt without catalog-read links and clears it when edit authority is revoked", async () => {
  const w = setup();
  w.post.mockResolvedValue(etagReply(created));
  const access = {
    ...administrator,
    scopeEnforcement: true,
    scopes: ["sources.read", "catalog.write"] as typeof administrator.scopes,
  };
  const view = render(
    <AdoptVersion
      {...w.props}
      access={access}
      id={occurrence.observationId}
      occurrence={occurrence}
    />,
  );
  await screen.findByRole("button", { name: "Find existing local Resource" });
  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  expect(screen.queryByRole("link", { name: resource.id })).toBeNull();
  await confirmCommand("Confirm new Version adoption");
  await screen.findByRole("region", { name: "New adopted Version" });
  expect(
    screen.queryByRole("link", { name: "Inspect adopted revision" }),
  ).toBeNull();
  expect(
    screen.queryByRole("link", { name: "Open Resource and existing Listings" }),
  ).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);

  const props = { ...w.props, id: occurrence.observationId, occurrence };
  view.rerender(
    <AdoptVersion
      {...props}
      access={{
        ...access,
        permissions: access.permissions.filter((p) => p !== "resource.edit"),
        namespaceGrants: [],
      }}
    />,
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "New adopted Version" }),
    ).toBeNull(),
  );
  expect(
    screen.getByRole("button", { name: "Confirm new Version adoption" }),
  ).toHaveProperty("disabled", true);

  view.rerender(<AdoptVersion {...props} access={access} />);
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Confirm new Version adoption" }),
    ).toHaveProperty("disabled", false),
  );
  expect(
    screen.queryByRole("region", { name: "New adopted Version" }),
  ).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
});

it("clears private adoption results on source access revocation and requires a fresh lookup after restoration", async () => {
  const w = setup();
  w.post.mockResolvedValue(etagReply(created));
  const props = { ...w.props, id: occurrence.observationId, occurrence };
  const view = render(<AdoptVersion {...props} access={administrator} />);
  await screen.findByRole("button", { name: "Find existing local Resource" });
  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  await confirmCommand("Confirm new Version adoption");
  await screen.findByRole("region", { name: "New adopted Version" });

  view.rerender(
    <AdoptVersion
      {...props}
      access={{
        ...administrator,
        permissions: administrator.permissions.filter(
          (p) => p !== "source.read_evidence",
        ),
      }}
    />,
  );
  expect(screen.queryByText("Adopt as a new Version")).toBeNull();
  expect(
    screen.queryByRole("region", { name: "New adopted Version" }),
  ).toBeNull();

  view.rerender(<AdoptVersion {...props} access={administrator} />);
  await screen.findByRole("button", { name: "Find existing local Resource" });
  expect(screen.queryByText("opaque-first")).toBeNull();
  expect(
    screen.queryByRole("region", { name: "New adopted Version" }),
  ).toBeNull();
  expect(
    w.get.mock.calls.filter(
      ([path]) =>
        path === "/api/v1/observations/{observationId}/adoption-target",
    ),
  ).toHaveLength(1);
  expect(w.post).toHaveBeenCalledTimes(1);

  click("Find existing local Resource");
  await screen.findByText("opaque-first");
  expect(
    w.get.mock.calls.filter(
      ([path]) =>
        path === "/api/v1/observations/{observationId}/adoption-target",
    ),
  ).toHaveLength(2);
  expect(
    screen.queryByRole("region", { name: "New adopted Version" }),
  ).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
});

const upstreamRow = {
  target,
  occurrence,
  identifier: "urn:air:example.com:agents:imported",
  kind: "agent",
  mediaType: "application/a2a-agent-card+json",
  outcome: "changed_entry",
  priorDigest: occurrence.exactEntryDigest,
  priorProfile: "catalog-observation-v1",
  currentProfile: "catalog-observation-v1",
  snapshotCompletedAt: stamp,
};

function inventoryWorkspace(row: typeof upstreamRow = upstreamRow) {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/capabilities": {
      ...capabilities,
      optionalCapabilities: {
        ...capabilities.optionalCapabilities,
        versionAdoption: "enabled",
      },
    },
    "/api/v1/upstream-changes": { items: [row] },
  });
  return w;
}

const secondRow = {
  ...upstreamRow,
  occurrence: { ...occurrence, observationId: "another-observation" },
};
it.each([
  {
    defect: "more than 100 results",
    items: Array.from({ length: 101 }, (_, i) => ({
      ...upstreamRow,
      occurrence: { ...occurrence, observationId: `observation-page-${i}` },
    })),
  },
  {
    defect: "invalid prior digest",
    items: [upstreamRow, { ...secondRow, priorDigest: "unverified" }],
  },
  ...(
    ["retrievalDigest", "exactEntryDigest", "normalizedEntryDigest"] as const
  ).map((field) => ({
    defect: `invalid ${field}`,
    items: [
      upstreamRow,
      {
        ...secondRow,
        occurrence: { ...secondRow.occurrence, [field]: "unverified" },
      },
    ],
  })),
])(
  "clears the inventory on reload with $defect and recovers on a valid reply",
  async ({ items }) => {
    const w = inventoryWorkspace();
    render(<UpstreamChanges {...w.props} access={administrator} />);
    await screen.findByRole("link", { name: "Inspect selected occurrence" });
    w.get.mockResolvedValueOnce(etagReply({ items }));
    click("Refresh from first page");
    await screen.findByRole("alert");
    expect(w.props.onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ code: "invalid_api_response" }),
    );
    expect(
      screen.queryAllByRole("link", { name: "Inspect selected occurrence" }),
    ).toHaveLength(0);
    click("Reload");
    await screen.findByRole("link", { name: "Inspect selected occurrence" });
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["changed_entry", "comparison_unavailable"])(
  "opens %s for exact inspection without offering a mutation",
  async (outcome) => {
    const w = inventoryWorkspace({ ...upstreamRow, outcome });
    const view = render(
      <UpstreamChanges {...w.props} access={administrator} />,
    );
    const link = await screen.findByRole("link", {
      name: "Inspect selected occurrence",
    });
    const href = link.getAttribute("href")!;
    expect(href).toContain(occurrence.observationId);
    view.unmount();
    history.replaceState(null, "", href);
    render(
      <SelectedUpstreamChange
        {...w.props}
        access={administrator}
        id={occurrence.observationId}
        raw={<span>Audited raw content</span>}
      />,
    );
    await screen.findByText(target.priorVersionRevisionId);
    expect(screen.queryByText("Audited raw content")).toBeNull();
    click("Read exact raw entry");
    await screen.findByText("Audited raw content");
    expect(
      screen.queryByRole("button", { name: "Confirm new Version adoption" }),
    ).toBeNull();
    expect(w.get.mock.calls.at(-1)).toMatchObject([
      "/api/v1/upstream-changes",
      { params: { query: { observationId: occurrence.observationId } } },
    ]);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("requires a fresh selection when the prior revision or occurrence has changed", async () => {
  const w = inventoryWorkspace({
    ...upstreamRow,
    outcome: "unadopted_version",
  });
  const view = render(<UpstreamChanges {...w.props} access={administrator} />);
  const link = await screen.findByRole("link", {
    name: "Inspect selected occurrence",
  });
  const stale = new URL(link.getAttribute("href")!, location.origin);
  stale.searchParams.set("priorVersionRevisionId", "other-proof");
  view.unmount();
  history.replaceState(null, "", stale);
  render(
    <SelectedUpstreamChange
      {...w.props}
      access={administrator}
      id={occurrence.observationId}
      raw={null}
    />,
  );
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Refresh the inventory",
  );
  expect(
    screen.queryByRole("button", { name: "Confirm new Version adoption" }),
  ).toBeNull();
  expect(w.get.mock.calls.map(([path]) => path)).not.toContain(
    "/api/v1/observations/{observationId}/adoption-target",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("applies exact source and namespace filters, pages and refreshes from page one", async () => {
  const w = inventoryWorkspace();
  const row = {
    ...upstreamRow,
    latestAttempt: {
      snapshotId: "attempt",
      revision: 2,
      state: "failed",
      completedAt: stamp,
    },
  };
  w.get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/capabilities")
      return etagReply({
        ...capabilities,
        optionalCapabilities: {
          ...capabilities.optionalCapabilities,
          versionAdoption: "enabled",
        },
      });
    const q = (options as { params: { query: { cursor?: string } } }).params
      .query;
    return etagReply(
      q.cursor ? { items: [] } : { items: [row], nextCursor: "continue" },
    );
  });
  render(<UpstreamChanges {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Inspect selected occurrence" });
  const before = w.get.mock.calls.length;
  fireEvent.change(screen.getByLabelText("Condition"), {
    target: { value: "entry_comparison" },
  });
  fireEvent.change(screen.getByLabelText("Source ID (optional)"), {
    target: { value: "source-id" },
  });
  fireEvent.change(screen.getByLabelText("Namespace ID (optional)"), {
    target: { value: "namespace-id" },
  });
  expect(w.get).toHaveBeenCalledTimes(before);
  click("Apply filters");
  const query = {
    check: "entry_comparison",
    sourceId: "source-id",
    namespaceId: "namespace-id",
  };
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)).toMatchObject([
      "/api/v1/upstream-changes",
      { params: { query } },
    ]),
  );
  click("Next page");
  await screen.findByText(
    "No matching changes in the retained complete source evidence.",
  );
  expect(w.get.mock.calls.at(-1)).toMatchObject([
    "/api/v1/upstream-changes",
    { params: { query: { ...query, cursor: "continue" } } },
  ]);
  click("Refresh from first page");
  await screen.findByRole("link", { name: "Inspect selected occurrence" });
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({ params: { query } });
  click("Apply filters");
  await screen.findByRole("link", { name: "Inspect selected occurrence" });
  click("Refresh from first page");
  await screen.findByRole("link", { name: "Inspect selected occurrence" });
  expect(w.post).not.toHaveBeenCalled();
});
it("discards results when permission is lost and explains a disabled capability", async () => {
  const w = inventoryWorkspace();
  const view = render(<UpstreamChanges {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Inspect selected occurrence" });
  view.rerender(
    <UpstreamChanges
      {...w.props}
      access={{ ...administrator, availablePermissions: [] }}
    />,
  );
  expect(
    screen.queryByRole("link", { name: "Inspect selected occurrence" }),
  ).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain("permission to edit");
  readReplies(w.get, {
    "/api/v1/capabilities": {
      ...capabilities,
      optionalCapabilities: {
        ...capabilities.optionalCapabilities,
        versionAdoption: "disabled",
      },
    },
  });
  view.rerender(<UpstreamChanges {...w.props} access={administrator} />);
  await screen.findByText(
    "The administrator has not enabled upstream Version adoption.",
  );
  expect(w.get.mock.calls.at(-1)?.[0]).toBe("/api/v1/capabilities");
});
it.each(["agent", "skill"])(
  "binds the exact %s selection and respects archive delivery limits",
  async (kind) => {
    const w = inventoryWorkspace({
      ...upstreamRow,
      outcome: "unadopted_version",
      kind,
      mediaType:
        kind === "skill"
          ? "application/agent-skills+gzip"
          : upstreamRow.mediaType,
    });
    const view = render(
      <UpstreamChanges {...w.props} access={administrator} />,
    );
    const link = await screen.findByRole("link", {
      name: "Inspect selected occurrence",
    });
    const href = link.getAttribute("href")!;
    view.unmount();
    history.replaceState(null, "", href);
    render(
      <SelectedUpstreamChange
        {...w.props}
        access={administrator}
        id={occurrence.observationId}
        raw={null}
      />,
    );
    if (kind === "skill") {
      await screen.findByText(/Use inspected package authoring/);
      expect(
        screen.queryByRole("button", { name: "Confirm new Version adoption" }),
      ).toBeNull();
    } else {
      w.post.mockResolvedValue(etagReply(created));
      await screen.findByRole("button", {
        name: "Confirm new Version adoption",
      });
      await confirmCommand("Confirm new Version adoption");
      await screen.findByRole("link", { name: "Inspect adopted revision" });
      expect(w.post.mock.calls[0]).toMatchObject([
        "/api/v1/resources/{resourceId}/versions:adopt",
        {
          params: { path: { resourceId: target.resourceId } },
          body: {
            snapshotId: occurrence.snapshotId,
            observationId: occurrence.observationId,
            priorVersionRevisionId: target.priorVersionRevisionId,
            retrievalDigest: occurrence.retrievalDigest,
            entryLocation: occurrence.entryLocation,
          },
        },
      ]);
    }
  },
);
