import { required } from "../../tests/required";
// @vitest-environment jsdom
import { job } from "../../tests/unit/responses";
import { deferred } from "../../tests/unit/fixtures";
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Federation, { Inspection } from "./Federation";
import type { components } from "../api/schema";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
screenLifecycle("/unit/sources");
it.each([
  ["missing existing flag", { job: { ...job, kind: "source.sync" } }],
  [
    "nonboolean existing flag",
    { job: { ...job, kind: "source.sync" }, existing: "false" },
  ],
  [
    "foreign tenant",
    {
      job: { ...job, kind: "source.sync", tenantId: "tenant-b" },
      existing: false,
    },
  ],
  ["wrong kind", { job, existing: false }],
])(
  "recovers a synchronization reply with %s using the exact command key",
  async (_name, malformed) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(source));
    w.post
      .mockResolvedValueOnce(etagReply(malformed))
      .mockResolvedValueOnce(
        etagReply({ job: { ...job, kind: "source.sync" }, existing: true }),
      );
    render(<Federation {...w.props} access={administrator} id={source.id} />);
    await screen.findByLabelText("Source action");
    change("Source action", "sync");
    await confirmCommand("Synchronize source");
    expect(screen.getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      screen.queryByRole("link", { name: "Inspect synchronization Job" }),
    ).toBeNull();
    click("Retry same command");
    await screen.findByText(/Existing synchronization Job reused/);
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(w.get).toHaveBeenCalledOnce();
  },
);
const source = {
  id: "source-1",
  tenantId: administrator.tenantId,
  revision: 1,
  displayName: "Partner catalog",
  status: "active",
  retrievalMode: "indexed",
  updateMode: "manual_review",
  profileId: "ard",
  endpoint: "https://example.com/catalog",
  scope: { kinds: ["agent"], tags: ["support, tools"] },
  live: {},
  nesting: {},
  referrals: {},
};

it.each([
  ["Search mode", "hybrid"],
  ["ANS verification policy", "operator-ans"],
  ["Synchronization", "automatic"],
  ["Scope tag 1", "preserve,this-tag"],
  [
    "Topology JSON",
    '{"referrals":{"enabled":true},"nesting":{"enabled":false},"discoveryAssignments":[]}',
  ],
  ["Enable live queries", "checkbox"],
])(
  "protects an inspected connection changed through %s without a name or reason",
  async (label, value) => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
    const w = screenWorkspace();
    w.get.mockResolvedValue(
      etagReply({
        id: "inspection-1",
        resolvedUrl: source.endpoint,
        profileId: "ard",
        entryCount: 0,
        expiresAt: "2026-09-02T12:00:00Z",
        warnings: [],
      }),
    );
    render(
      <Inspection {...w.props} id="inspection-1" access={administrator} />,
    );
    await screen.findByLabelText("Display name");
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
    if (value === "checkbox") fireEvent.click(screen.getByLabelText(label));
    else {
      if (label === "Scope tag 1") click("Add scope tag");
      if (label === "Topology JSON") click("Edit topology as JSON");
      change(label, value);
    }
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(false);
    expect(w.post).not.toHaveBeenCalled();
    expect(w.get).toHaveBeenCalledOnce();
  },
);

const preview: components["schemas"]["FederationChangePreview"] = {
  id: "preview-1",
  tenantId: administrator.tenantId,
  sourceId: source.id,
  intent: "update",
  currentMatched: 3,
  proposedMatched: 1,
  configurationDigest: "sha256:" + "c".repeat(64),
  observationSetDigest: "sha256:" + "b".repeat(64),
  createdAt: stamp,
  sourceRevision: source.revision,
  digest: "sha256:" + "d".repeat(64),
  excludedObservations: 2,
  artworkOverridesAffected: 0,
  sendsQueriesUpstream: false,
  expiresAt: "2026-09-02T12:00:00Z",
};

it("refuses a foreign-Source preview and recovers only the same preview command", async () => {
  const w = await changeSetup({ ...preview, sourceId: "another-source" });
  await confirmCommand("Preview source change");
  expect(screen.getByRole("alert").textContent).toContain("outcome is unknown");
  expect(
    screen.queryByRole("button", { name: "Apply source change" }),
  ).toBeNull();
  w.post.mockResolvedValue(etagReply(preview));
  click("Retry same command");
  await screen.findByRole("button", { name: "Apply source change" });
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
  expect(w.patch).not.toHaveBeenCalled();
});

it.each([
  { tenantId: "another-tenant" },
  { intent: "remove" as const },
  { configurationDigest: "invalid" },
  { observationSetDigest: "invalid" },
  { digest: "invalid" },
  { currentMatched: -1 },
  { proposedMatched: 0.5 },
  { excludedObservations: NaN },
  { artworkOverridesAffected: 10001 },
  { sendsQueriesUpstream: "yes" },
  { expiresAt: "invalid" },
  { createdAt: "invalid" },
])(
  "rejects a malformed or foreign preview before installing Apply: %j",
  async (invalid) => {
    const w = await changeSetup({
      ...preview,
      ...invalid,
    } as components["schemas"]["FederationChangePreview"]);
    await confirmCommand("Preview source change");
    expect(screen.getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      screen.queryByRole("button", { name: "Apply source change" }),
    ).toBeNull();
    expect(w.patch).not.toHaveBeenCalled();
  },
);

it("blocks preview expiry at preparation even before its next local timer tick", async () => {
  const w = await changeSetup();
  await confirmCommand("Preview source change");
  change("Reason", "Preserve this reason");
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(preview.expiresAt));
  click("Apply source change");
  expect(screen.getByRole("alert").textContent).toContain("Preview expired");
  expect(w.patch).not.toHaveBeenCalled();
});

it("updates an idle preview deadline without clearing prepared settings or making requests", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const w = await changeSetup();
    await confirmCommand("Preview source change");
    change("Reason", "Preserve reason");
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(preview.expiresAt));
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(
      screen.getByRole("button", { name: "Apply source change" }),
    ).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Preserve reason",
    );
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).toHaveBeenCalledOnce();
  } finally {
    vi.useRealTimers();
  }
});

it("shows all changed settings in both captured confirmations while withholding credential references", async () => {
  const w = await changeSetup({ ...preview, sendsQueriesUpstream: true });
  change("Synchronization", "automatic");
  change("Search mode", "hybrid");
  change("Replacement inspection ID", "inspection-new");
  change("New credential binding reference", "opaque-binding");
  fireEvent.click(screen.getByLabelText("Display source artwork"));
  click("Preview source change");
  let dialog = screen.getByRole("dialog");
  expect(dialog.textContent).toContain("automatic");
  expect(dialog.textContent).toContain("hybrid");
  expect(
    within(dialog).getByRole("region", {
      name: "Prepared source configuration",
    }).textContent,
  ).toContain("inspection-new");
  for (const label of [
    "Metadata to include",
    "Source artwork",
    "ANS policy",
    "Live query disclosure",
    "Referral policy",
    "Nested retrieval policy",
    "Discovery audiences",
    "Network and credential changes",
  ])
    expect(dialog.textContent).toContain(label);
  expect(dialog.textContent).not.toContain("opaque-binding");
  expect(w.post).not.toHaveBeenCalled();
  await act(async () => {
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  });
  change("Reason", "Reviewed all settings");
  click("Apply source change");
  dialog = screen.getByRole("dialog");
  expect(dialog.textContent).toContain("automatic");
  expect(dialog.textContent).toContain(preview.digest);
  expect(dialog.textContent).toContain("3 / 1 / 2");
  expect(dialog.textContent).toContain("Artwork overrides affected");
  expect(
    within(dialog).getByRole("region", {
      name: "Prepared source configuration",
    }).textContent,
  ).toContain("inspection-new");
  expect(dialog.textContent).not.toContain("opaque-binding");
  expect(w.patch).not.toHaveBeenCalled();
  click("Cancel");
});

it("retains an existing deployment binding without revealing its value during an unrelated change", async () => {
  const w = await changeSetup(preview, {
    ...source,
    credentialBinding: "private-reference",
  } as typeof source);
  click("Preview source change");
  const dialog = screen.getByRole("dialog");
  expect(dialog.textContent).toContain(
    "Keep existing network and credential bindings",
  );
  expect(dialog.textContent).toContain(
    "Existing deployment reference (value withheld)",
  );
  expect(dialog.textContent).not.toContain("private-reference");
  expect(w.post).not.toHaveBeenCalled();
  click("Cancel");
});
async function changeSetup(
  impact = preview,
  currentSource = source,
  access = administrator,
) {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const setup = screenWorkspace();
  readReplies(setup.get, {
    "/api/v1/federation/sources/{sourceId}": currentSource,
  });
  setup.post.mockResolvedValue(etagReply(impact));
  render(<Federation {...setup.props} id={source.id} access={access} />);
  await screen.findByRole("heading", { name: source.displayName });
  click("Preview configuration change or removal");
  return setup;
}

it("clears a named policy through preview/apply without requiring assessment permissions", async () => {
  const minimal = {
    ...administrator,
    permissions: [
      "source.read",
      "source.manage",
    ] as typeof administrator.permissions,
    availablePermissions: [
      "source.read",
      "source.manage",
    ] as typeof administrator.availablePermissions,
  };
  const current = { ...source, trustVerificationPolicy: "operator-ans" };
  const w = await changeSetup(preview, current, minimal);
  expect(screen.getByText(/Add at least one filter/)).toBeTruthy();
  const scopeHelp = screen
    .getByText("How scope filters match")
    .closest("details")!;
  expect(scopeHelp.open).toBe(false);
  fireEvent.click(screen.getByText("How scope filters match"));
  expect(scopeHelp.open).toBe(true);
  expect(scopeHelp.textContent).toContain("All listed tags must match");
  expect(scopeHelp.textContent).toContain(
    "Publishers are domains from AIR identifiers",
  );
  expect(w.post).not.toHaveBeenCalled();
  w.patch.mockResolvedValue(etagReply({ ...source, revision: 2 }));
  expect(screen.getByLabelText("ANS verification policy")).toHaveProperty(
    "value",
    "operator-ans",
  );
  change("ANS verification policy", "");
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(w.post.mock.calls[0]?.[1]).toHaveProperty(
    "body.trustVerificationPolicy",
    "",
  );
  change("Reason", "Clear unused ANS policy");
  await confirmCommand("Apply source change");
  await screen.findByText(/Source change applied/);
  expect(w.patch.mock.calls[0]?.[1]).toMatchObject({
    params: { header: { "If-Match": '"revision-1"' } },
    body: { trustVerificationPolicy: "", changePreviewDigest: preview.digest },
  });
});

it("applies the exact previewed source configuration with its ETag and inspection bindings", async () => {
  const { props, post, patch, remove } = await changeSetup({
    ...preview,
    sendsQueriesUpstream: true,
  });
  patch.mockResolvedValue(etagReply({ ...source, revision: 2 }));
  fireEvent.click(screen.getByLabelText("Display source artwork"));
  expect(screen.getByLabelText("Display source artwork")).toHaveProperty(
    "checked",
    true,
  );
  fireEvent.click(screen.getByLabelText("Display source artwork"));
  change("Display name", "Updated partner");
  change("ANS verification policy", "operator-ans");
  change("Search mode", "hybrid");
  fireEvent.click(screen.getByLabelText("Enable live queries"));
  fireEvent.click(
    screen.getByLabelText("I acknowledge queries are sent to this upstream"),
  );
  change("Replacement inspection ID", " inspection-new ");
  change("New network profile reference", " private-network ");
  change("New credential binding reference", " opaque-binding ");
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(
    screen.getByText("Current saved entries").nextElementSibling?.textContent,
  ).toBe("3");
  expect(
    screen.getByText("Entries included after this change").nextElementSibling
      ?.textContent,
  ).toBe("1");
  expect(
    screen.getByText("Entries excluded after this change").nextElementSibling
      ?.textContent,
  ).toBe("2");
  expect(
    screen.getByText("Queries sent upstream").nextElementSibling?.textContent,
  ).toBe("Yes");
  expect(screen.getByLabelText("Display name").matches(":disabled")).toBe(true);
  const completedPreview = screen.getByRole("button", {
    name: "Preview source change",
  });
  expect(completedPreview.matches(":disabled")).toBe(true);
  fireEvent.submit(completedPreview.closest("form")!);
  expect(patch).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledTimes(1);
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/federation/sources/{sourceId}/change-previews",
    {
      params: {
        path: { sourceId: source.id },
        header: { "Idempotency-Key": expect.any(String) as unknown },
      },
      body: {
        intent: "update",
        displayName: "Updated partner",
        trustVerificationPolicy: "operator-ans",
        retrievalMode: "hybrid",
        live: { enabled: true, queryPrivacyAcknowledged: true },
        scope: source.scope,
        inspectionId: "inspection-new",
        networkProfile: "private-network",
        credentialBinding: "opaque-binding",
      },
    },
  ]);
  change("Reason", " Apply reviewed configuration ");
  await confirmCommand("Apply source change");
  await screen.findByText(/Source change applied\. Reload the source/);
  const { intent: _intent, ...settings } = (
    required(post.mock.calls[0])[1] as { body: Record<string, unknown> }
  ).body;
  expect(patch).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/federation/sources/{sourceId}",
    {
      params: {
        path: { sourceId: source.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"revision-1"',
        },
      },
      body: {
        ...settings,
        changePreviewDigest: preview.digest,
        reason: "Apply reviewed configuration",
      },
    },
  );
  expect(remove).not.toHaveBeenCalled();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("previews and saves exclusions without changing the positive scope", async () => {
  const { post, patch } = await changeSetup();
  patch.mockResolvedValue(etagReply({ ...source, revision: 2 }));
  click("Add excluded identifier");
  change("Excluded identifier 1", "urn:air:example.com:agents:hidden");
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      scope: {
        ...source.scope,
        excludedIdentifiers: ["urn:air:example.com:agents:hidden"],
      },
    },
  });
  change("Reason", "Limit this source");
  await confirmCommand("Apply source change");
  await screen.findByText(/Source change applied/);
  expect(patch.mock.calls[0]?.[1]).toMatchObject({
    body: {
      changePreviewDigest: preview.digest,
      scope: {
        ...source.scope,
        excludedIdentifiers: ["urn:air:example.com:agents:hidden"],
      },
    },
  });
});

it("preserves saved exclusions on unrelated edits and clears them with a new preview", async () => {
  const { props, get, post } = screenWorkspace();
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  readReplies(get, {
    "/api/v1/federation/sources/{sourceId}": {
      ...source,
      scope: { ...source.scope, excludedIdentifiers: ["legacy:hidden"] },
    },
  });
  post.mockResolvedValue(etagReply(preview));
  render(<Federation {...props} id={source.id} access={administrator} />);
  await screen.findByRole("heading", { name: source.displayName });
  click("Preview configuration change or removal");
  expect(screen.getByLabelText("Excluded identifier 1")).toHaveProperty(
    "value",
    "legacy:hidden",
  );
  change("Display name", "Renamed source");
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      scope: { ...source.scope, excludedIdentifiers: ["legacy:hidden"] },
    },
  });
  click("Revise configuration and discard preview");
  click("Clear excluded identifiers");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add excluded identifier" }),
  );
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(post.mock.calls[1]?.[1]).toMatchObject({
    body: { scope: source.scope },
  });
  expect(
    (post.mock.calls[1]?.[1] as { body: { scope: Record<string, unknown> } })
      .body.scope,
  ).not.toHaveProperty("excludedIdentifiers");
});

it("removes only with a removal preview and omits update settings from the removal command", async () => {
  const { post, patch, remove } = await changeSetup({
    ...preview,
    intent: "remove",
    proposedMatched: 0,
    excludedObservations: 3,
  });
  change("Change type", "remove");
  expect(screen.queryByLabelText("Display name")).toBeNull();
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(post.mock.calls[0]?.[1]).toMatchObject({ body: { intent: "remove" } });
  expect(post).toHaveBeenCalledTimes(1);
  expect(remove).not.toHaveBeenCalled();
  post.mockResolvedValue({ response: new Response(null, { status: 204 }) });
  change("Reason", " Remove unused source ");
  click("Remove source");
  const confirm = within(screen.getByRole("dialog")).getByRole("button", {
    name: "Confirm",
  });
  expect(confirm.className).toBe("danger");
  expect(confirm.querySelector('svg[aria-hidden="true"]')).toBeTruthy();
  fireEvent.click(confirm);
  await screen.findByText(/^Source removed\./);
  expect(
    screen
      .getAllByRole("link", { name: "Back to sources" })
      .at(-1)
      ?.getAttribute("href"),
  ).toBe("/unit/sources");
  expect(screen.queryByRole("link", { name: "Reload source" })).toBeNull();
  expect(post).toHaveBeenNthCalledWith(
    2,
    "/api/v1/federation/sources/{sourceId}:remove",
    {
      params: {
        path: { sourceId: source.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"revision-1"',
        },
      },
      body: {
        changePreviewDigest: preview.digest,
        reason: "Remove unused source",
      },
    },
  );
  expect(patch).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledTimes(2);
  expect(remove).not.toHaveBeenCalled();
});

it.each([
  [
    "expired",
    { ...preview, createdAt: "2026-09-01T11:00:00Z", expiresAt: stamp },
  ],
  ["another source revision", { ...preview, sourceRevision: 2 }],
])("does not apply a preview that is %s", async (_name, impact) => {
  const { patch, remove } = await changeSetup(impact);
  await confirmCommand("Preview source change");
  if (_name === "another source revision") {
    expect(screen.getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      screen.queryByRole("button", { name: "Apply source change" }),
    ).toBeNull();
    expect(patch).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
    return;
  }
  await screen.findByText("Exact change impact");
  change("Reason", "Do not apply stale evidence");
  expect(
    (
      screen.getByRole("button", {
        name: "Apply source change",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  click("Apply source change");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(patch).not.toHaveBeenCalled();
  expect(remove).not.toHaveBeenCalled();
});

it("requires explicit discard and a fresh preview after editing proposed settings", async () => {
  const { post, patch } = await changeSetup();
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  change("Reason", "Keep my pending reason");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Revise configuration and discard preview");
  expect(discard).toHaveBeenCalledOnce();
  expect(screen.getByText("Exact change impact")).toBeTruthy();
  discard.mockReturnValue(true);
  click("Revise configuration and discard preview");
  expect(
    screen.queryByRole("button", { name: "Apply source change" }),
  ).toBeNull();
  expect(screen.getByLabelText("Display name").matches(":disabled")).toBe(
    false,
  );
  change("Display name", "New proposed name");
  post.mockResolvedValue(
    etagReply({
      ...preview,
      id: "preview-2",
      digest: "sha256:" + "e".repeat(64),
    }),
  );
  await confirmCommand("Preview source change");
  await screen.findByText("Exact change impact");
  expect(post).toHaveBeenCalledTimes(2);
  expect(post.mock.calls[1]?.[1]).toMatchObject({
    body: { displayName: "New proposed name" },
  });
  patch.mockResolvedValue(etagReply({ ...source, revision: 2 }));
  change("Reason", "Apply newly reviewed settings");
  await confirmCommand("Apply source change");
  await screen.findByText(/Source change applied\. Reload the source/);
  expect(patch.mock.calls[0]?.[1]).toMatchObject({
    body: {
      changePreviewDigest: "sha256:" + "e".repeat(64),
      displayName: "New proposed name",
    },
  });
});

it("lists sources and resets pagination on first-page navigation", async () => {
  const { props, get } = screenWorkspace();
  get.mockResolvedValue(
    etagReply({
      items: [
        {
          ...source,
          lastError: "Upstream unavailable",
          lastSyncAt: stamp,
          lastCompleteSnapshotId: "snapshot-last",
        },
        { ...source, id: "source-2", displayName: "Unattempted source" },
      ],
      nextCursor: "next",
    }),
  );
  render(<Federation {...props} access={administrator} />);
  await screen.findByRole("link", { name: source.displayName });
  expect(screen.getByText("Upstream unavailable")).toBeTruthy();
  expect(screen.getByText(new Date(stamp).toLocaleString())).toBeTruthy();
  expect(screen.getByText("No recorded synchronization")).toBeTruthy();
  expect(screen.getByText("No complete snapshot recorded")).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Inspect last complete snapshot" })
      .getAttribute("href"),
  ).toBe("/unit/source-snapshots/snapshot-last");
  expect(
    screen
      .getAllByRole("link", { name: "Synchronization history" })
      .map((a) => a.getAttribute("href")),
  ).toEqual(["/unit/sources/source-1", "/unit/sources/source-2"]);
  expect(get).toHaveBeenCalledOnce();
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "next" } },
    }),
  );
  click("First page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    ),
  );
  get.mockResolvedValue(etagReply({ items: [] }));
  click("Refresh");
  await screen.findByText("No sources on this page.");
});

it.each([true, false])(
  "inspects a destination without implicitly connecting it (references=%s)",
  async (references) => {
    const { props, post } = screenWorkspace();
    post.mockResolvedValue(
      etagReply({
        id: "inspection-1",
        resolvedUrl: source.endpoint,
        profileId: "ard",
        entryCount: 2,
        expiresAt: "2026-09-02T12:00:00Z",
      }),
    );
    render(<Federation {...props} id="new" access={administrator} />);
    change("Source URL", source.endpoint);
    change("Network policy reference", references ? " partner " : " ");
    change("Credential reference", references ? " credential-reference " : " ");
    await confirmCommand("Inspect source");
    expect(
      (
        await screen.findByRole("link", {
          name: "Review inspection and connect",
        })
      ).getAttribute("href"),
    ).toBe("/unit/inspections/inspection-1");
    expect(post).toHaveBeenCalledTimes(1);
    if (!references) {
      expect(post.mock.calls[0]?.[1]).not.toHaveProperty("body.networkProfile");
      expect(post.mock.calls[0]?.[1]).not.toHaveProperty(
        "body.credentialBinding",
      );
    }
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/federation/source-inspections",
      {
        body: {
          url: source.endpoint,
          ...(references
            ? {
                networkProfile: "partner",
                credentialBinding: "credential-reference",
              }
            : {}),
        },
      },
    ]);
  },
);

it("connects only retained, unexpired inspection evidence with explicit replication/disclosure settings", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const { props, get, post } = screenWorkspace();
  get.mockResolvedValue(
    etagReply({
      id: "inspection-1",
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 2,
      expiresAt: "2026-09-02T12:00:00Z",
      networkProfile: "partner",
      credentialBinding: "binding",
      warnings: [
        {
          code: "UPSTREAM_WARNING",
          severity: "warning",
          message: "<b>Review declared capabilities</b>",
        },
      ],
    }),
  );
  post.mockResolvedValue(etagReply(source));
  render(<Inspection {...props} id="inspection-1" access={administrator} />);
  await screen.findByLabelText("Display name");
  expect(
    screen.getByRole("heading", { name: "Inspection summary" }),
  ).toBeTruthy();
  expect(
    screen.getByText("Entries found").nextElementSibling?.textContent,
  ).toBe("2");
  expect(
    screen
      .getByText("UPSTREAM_WARNING: <b>Review declared capabilities</b>")
      .querySelector("b"),
  ).toBeNull();
  change("Display name", source.displayName);
  change("Search mode", "hybrid");
  change("Synchronization", "automatic");
  for (const label of ["Agents", "MCP Servers", "Skills", "Plugins"])
    fireEvent.click(screen.getByLabelText(label));
  change("Reason", "Connect reviewed source");
  click("Connect inspected source");
  await screen.findByRole("alert");
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByLabelText("Agents"));
  click("Add scope tag");
  change("Scope tag 1", "temporary,tag");
  click("Remove scope tag 1");
  click("Add scope publisher");
  change("Scope publisher 1", "example.com");
  fireEvent.click(screen.getByLabelText("Enable live queries"));
  fireEvent.click(
    screen.getByLabelText("I acknowledge queries are sent to this upstream"),
  );
  change("Referral traversal", "referrals");
  screen.getByText("Advanced source participation").click();
  click("Edit topology as JSON");
  change(
    "Topology JSON",
    '{"referrals":{"enabled":false},"nesting":{"enabled":false},"discoveryAssignments":[]}',
  );
  await confirmCommand("Connect inspected source");
  expect(
    (await screen.findByRole("link", { name: "Open source" })).getAttribute(
      "href",
    ),
  ).toBe("/unit/sources/source-1");
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/federation/sources",
    {
      body: {
        inspectionId: "inspection-1",
        networkProfile: "partner",
        credentialBinding: "binding",
        scope: { kinds: ["agent"], publishers: ["example.com"] },
        retrievalMode: "hybrid",
        updateMode: "automatic",
        live: {
          enabled: true,
          queryPrivacyAcknowledged: true,
          federationMode: "referrals",
        },
      },
    },
  ]);
});

it.each(["live", "hybrid"])(
  "summarizes %s source policy and last complete evidence without extra reads",
  async (retrievalMode) => {
    const { props, get, post } = screenWorkspace();
    get.mockResolvedValue(
      etagReply({
        ...source,
        retrievalMode,
        updateMode: retrievalMode === "live" ? "manual_review" : "automatic",
        live: { enabled: true, queryPrivacyAcknowledged: true },
        lastSyncAt: stamp,
        lastCompleteSnapshotId: "snapshot-last",
        scope: { kinds: ["agent"], futureScope: null },
        lastError: "Latest synchronization was incomplete",
      }),
    );
    render(
      <Federation
        {...props}
        id={source.id}
        access={{
          ...administrator,
          scopeEnforcement: true,
          scopes: ["sources.read"],
        }}
      />,
    );
    await screen.findByRole("heading", { name: source.displayName });
    expect(
      screen.getByText("Metadata to include").nextElementSibling?.textContent,
    ).toBe(
      "kinds: 1; identifiers: 0; excluded identifiers: 0; versions: 0; publishers: 0; tags: 0",
    );
    expect(
      screen.getByText(
        retrievalMode === "live"
          ? "Manual synchronization"
          : "Automatic synchronization",
      ),
    ).toBeTruthy();
    expect(
      screen.getByText("Query disclosure acknowledged").nextElementSibling
        ?.textContent,
    ).toBe("Yes");
    expect(
      screen
        .getByRole("link", { name: "Inspect last complete snapshot" })
        .getAttribute("href"),
    ).toBe("/unit/source-snapshots/snapshot-last");
    expect(
      screen.getByText("Latest synchronization was incomplete"),
    ).toBeTruthy();
    expect(
      screen.getByText("Last sync attempt finished").nextElementSibling
        ?.textContent,
    ).toBe(new Date(stamp).toLocaleString());
    expect(screen.queryByRole("button", { name: "Test source" })).toBeNull();
    expect(get).toHaveBeenCalledTimes(1);
    expect(post).not.toHaveBeenCalled();
  },
);

it("does not allow connecting an expired inspection", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const { props, get, post } = screenWorkspace();
  get.mockResolvedValue(
    etagReply({
      id: "inspection-1",
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 2,
      expiresAt: stamp,
    }),
  );
  render(<Inspection {...props} id="inspection-1" access={administrator} />);
  const button = await screen.findByRole("button", {
    name: "Connect inspected source",
  });
  expect((button as HTMLButtonElement).disabled).toBe(true);
  expect(post).not.toHaveBeenCalled();
});

it.each(["test", "sync", "pause", "resume"])(
  "sends revision-bound source %s and distinguishes queued work from completion",
  async (action) => {
    const { props, get, post } = screenWorkspace();
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    readReplies(get, {
      "/api/v1/federation/sources/{sourceId}": {
        ...source,
        status: action === "resume" ? "paused" : "active",
      },
      "/api/v1/federation/sources/{sourceId}/snapshots": { items: [] },
    });
    post.mockResolvedValue(
      etagReply(
        action === "test"
          ? {
              sourceId: source.id,
              sourceRevision: source.revision + 1,
              status: "succeeded",
              httpStatus: 200,
              mediaType: "application/json",
              bodyDigest: "",
              entryCount: 0,
              capabilities: {},
              profileId: "ard",
              policyId: "",
              policyRevision: 0,
              policyDigest: "",
              testedAt: stamp,
            }
          : action === "sync"
            ? { job: { ...job, kind: "source.sync" }, existing: false }
            : { ...source, status: action === "pause" ? "paused" : "active" },
      ),
    );
    render(<Federation {...props} id={source.id} access={administrator} />);
    await screen.findByLabelText("Source action");
    change("Source action", action);
    if (["pause", "resume"].includes(action))
      change("Reason", " Reviewed change ");
    if (action === "pause") {
      click("Sync history");
      change("Source action", "sync");
      expect(
        (screen.getByLabelText("Source action") as HTMLSelectElement).value,
      ).toBe("pause");
      expect(
        (screen.getByLabelText("Reason") as HTMLTextAreaElement).value,
      ).toBe(" Reviewed change ");
      expect(screen.queryByText("No snapshots.")).toBeNull();
      expect(discard).toHaveBeenCalledTimes(2);
      expect(post).not.toHaveBeenCalled();
    }
    await confirmCommand(
      {
        test: "Test source",
        sync: "Synchronize source",
        pause: "Pause source",
        resume: "Resume source",
      }[action]!,
    );
    await screen.findByText(
      /Source (test returned|paused|resumed)|Synchronization Job accepted. Inspect/,
    );
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/federation/sources/{sourceId}:${action}`,
      {
        params: {
          path: { sourceId: source.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: ["pause", "resume"].includes(action)
          ? { reason: "Reviewed change" }
          : {},
      },
    ]);
    if (action === "sync") {
      expect(screen.getByText("job-1")).toBeTruthy();
      expect(
        screen
          .getByRole("link", { name: "Inspect synchronization Job" })
          .getAttribute("href"),
      ).toBe("/unit/jobs/job-1");
    }
    discard.mockClear();
    click("Sync history");
    await screen.findByText("No snapshots.");
    expect(discard).not.toHaveBeenCalled();
    click("Overview");
    await screen.findByLabelText("Source action");
  },
);

it("connects an inspection without inventing optional credential or network references", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const { props, get, post } = screenWorkspace();
  get.mockResolvedValue(
    etagReply({
      id: "inspection-1",
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 2,
      expiresAt: "2026-09-02T12:00:00Z",
    }),
  );
  post.mockResolvedValue(etagReply(source));
  render(<Inspection {...props} id="inspection-1" access={administrator} />);
  await screen.findByLabelText("Display name");
  change("Display name", source.displayName);
  change("Reason", "Connect public source");
  await confirmCommand("Connect inspected source");
  await screen.findByRole("link", { name: "Open source" });
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/federation/sources",
    { body: { inspectionId: "inspection-1" } },
  ]);
  expect(post.mock.calls[0]?.[1]).not.toHaveProperty("body.networkProfile");
  expect(post.mock.calls[0]?.[1]).not.toHaveProperty("body.credentialBinding");
});

it.each(["permission", "scope"])(
  "reports sync acceptance without a Job link when read %s is missing",
  async (missing) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, { "/api/v1/federation/sources/{sourceId}": source });
    post.mockResolvedValue(
      etagReply({ job: { ...job, kind: "source.sync" }, existing: false }),
    );
    render(
      <Federation
        {...props}
        id={source.id}
        access={{
          ...administrator,
          permissions:
            missing === "permission"
              ? ["source.read", "source.manage", "source.sync"]
              : ["source.read", "source.manage", "source.sync", "job.read"],
          availablePermissions: ["source.read", "source.manage", "source.sync"],
          scopeEnforcement: true,
          scopes: ["sources.read", "sources.manage"],
        }}
      />,
    );
    await screen.findByLabelText("Source action");
    change("Source action", "sync");
    await confirmCommand("Synchronize source");
    await screen.findByText(job.id);
    expect(
      screen.queryByRole("link", { name: "Inspect synchronization Job" }),
    ).toBeNull();
    expect(
      screen.getByText(/Inspecting progress requires job.read/),
    ).toBeTruthy();
    expect(post).toHaveBeenCalledOnce();
  },
);

it("explains live participation without changing source settings or sending commands", async () => {
  const w = await changeSetup();
  const live = screen.getByLabelText("Enable live queries");
  const mode = screen.getByLabelText("Referral traversal");
  const referral = screen.getByLabelText(
    "Only participate as an approved referral",
  );
  const artwork = screen.getByLabelText("Display source artwork");
  const reads = w.get.mock.calls.length;
  expect(artwork).toHaveProperty("checked", false);
  expect(artwork.closest(".workspace-field")).toBeTruthy();
  expect(screen.getByText(/Images use credential-free requests/)).toBeTruthy();
  expect(
    screen.getByText(/Live requests send query text and filters/),
  ).toBeTruthy();
  expect(screen.getByText(/Live queries use the setting above/)).toBeTruthy();
  expect(screen.getByLabelText(/I acknowledge anonymous results/)).toBeTruthy();
  for (const [label, control, definition] of [
    [
      "only participate as an approved referral",
      referral,
      /Exclude this source from initial live selection/,
    ],
    [
      "referral traversal",
      mode,
      /Every contacted referral still needs independent source and topology approval/,
    ],
  ] as const) {
    const about = screen.getByRole("button", { name: `About ${label}` });
    expect(about).toHaveProperty("type", "button");
    expect(about.getAttribute("aria-expanded")).toBe("false");
    const initialDescription = control.getAttribute("aria-describedby");
    fireEvent.click(about);
    expect(about.getAttribute("aria-expanded")).toBe("true");
    const help = screen.getByText(definition);
    expect((help as HTMLElement).hidden).toBe(false);
    expect(control.getAttribute("aria-describedby")?.split(" ")).toContain(
      help.id,
    );
    fireEvent.click(about);
    expect((help as HTMLElement).hidden).toBe(true);
    expect(control.getAttribute("aria-describedby")).toBe(initialDescription);
  }
  expect(live).toHaveProperty("checked", false);
  expect(referral).toHaveProperty("checked", false);
  expect(mode).toHaveProperty("value", "none");
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(w.post).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
  fireEvent.click(artwork);
  expect(artwork).toHaveProperty("checked", true);
  fireEvent.click(artwork);
  expect(artwork).toHaveProperty("checked", false);
  expect(w.post).not.toHaveBeenCalled();
});

it.each([
  ["test", "Test source", "source.inspect"],
  ["sync", "Synchronize source", "source.sync"],
  ["pause", "Pause source", "source.manage"],
  ["resume", "Resume source", "source.manage"],
] as const)(
  "uses only %s authority for the matching source action",
  async (action, label, permission) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/federation/sources/{sourceId}": {
        ...source,
        status: action === "resume" ? "paused" : "active",
      },
    });
    const access = {
      ...administrator,
      permissions: [
        "source.read" as const,
        ...(permission === "source.manage" ? ["source.inspect" as const] : []),
        permission,
      ],
      availablePermissions: [
        "source.read" as const,
        ...(permission === "source.manage" ? ["source.inspect" as const] : []),
        permission,
      ],
      scopeEnforcement: true,
      scopes: ["sources.read" as const, "sources.manage" as const],
    };
    const view = render(
      <Federation {...w.props} id={source.id} access={access} />,
    );
    await screen.findByLabelText("Source action");
    expect(screen.getByLabelText("Source action")).toHaveProperty(
      "value",
      action === "pause" ? "test" : action,
    );
    change("Source action", action);
    expect(screen.getByRole("button", { name: label })).toHaveProperty(
      "disabled",
      false,
    );
    for (const [other, otherLabel] of [
      ["test", "Test source"],
      ["sync", "Synchronize source"],
      ["pause", "Pause source"],
    ]) {
      if (other === action) continue;
      change("Source action", action);
      change("Source action", other!);
      const companionAction =
        other === "test" && permission === "source.manage";
      expect(screen.getByLabelText("Source action")).toHaveProperty(
        "value",
        companionAction ? other : action,
      );
      if (companionAction)
        expect(
          screen.getByRole("button", { name: otherLabel! }),
        ).toHaveProperty("disabled", false);
      else
        expect(screen.queryByRole("button", { name: otherLabel! })).toBeNull();
    }
    change("Source action", action);
    view.rerender(
      <Federation
        {...w.props}
        id={source.id}
        access={{ ...access, scopes: ["sources.read"] }}
      />,
    );
    expect(screen.queryByRole("button", { name: label })).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("validates the full Unicode source-name budget before connection", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const { props, get, post } = screenWorkspace();
  get.mockResolvedValue(
    etagReply({
      id: "inspection-1",
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 2,
      expiresAt: "2026-09-02T12:00:00Z",
    }),
  );
  render(<Inspection {...props} id="inspection-1" access={administrator} />);
  const name = await screen.findByLabelText("Display name");
  expect(name.getAttribute("maxlength")).toBe("320");
  change("Reason", "Connect a reviewed source");
  change("Display name", "🙂".repeat(160) + "a");
  click("Connect inspected source");
  expect(screen.getByRole("alert").textContent).toContain("160 characters");
  expect(screen.queryByRole("dialog")).toBeNull();
  change("Display name", "🙂".repeat(160));
  click("Connect inspected source");
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Cancel");
});

it.each(["Refresh", "Reload"])(
  "recovers source-directory cursor and first-page failures through %s",
  async (recovery) => {
    const w = screenWorkspace();
    w.get
      .mockResolvedValueOnce(
        etagReply({ items: [], nextCursor: "source-next" }),
      )
      .mockRejectedValueOnce(new Error("cursor refused"))
      .mockResolvedValueOnce(etagReply({ items: [] }))
      .mockRejectedValueOnce(new Error("first page unavailable"))
      .mockResolvedValue(etagReply({ items: [] }));
    render(<Federation {...w.props} access={administrator} />);
    fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    await screen.findByRole("alert");
    click(recovery);
    await screen.findByText("No sources on this page.");
    expect(w.get.mock.calls[2]?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    });
    click("Refresh");
    await screen.findByRole("alert");
    click("Reload");
    await screen.findByText("No sources on this page.");
    expect(w.get).toHaveBeenCalledTimes(5);
    expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("permits write-scope inspection without requiring a directory read or an administrator grant", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(
    etagReply({
      id: "inspection-write-only",
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 0,
      expiresAt: "2030-01-01T00:00:00Z",
    }),
  );
  const access = {
    ...administrator,
    permissions: ["source.read" as const, "source.inspect" as const],
    availablePermissions: ["source.read" as const, "source.inspect" as const],
    scopeEnforcement: true,
    scopes: ["sources.manage" as const],
  };
  render(<Federation {...w.props} access={access} id="new" />);
  expect(screen.getByLabelText("Source URL").matches(":disabled")).toBe(false);
  change("Source URL", source.endpoint);
  await confirmCommand("Inspect source");
  await screen.findByText(
    "Inspection retained. Review its resolved endpoint, policy and expiry before connecting.",
  );
  expect(screen.getByText("inspection-write-only")).toBeTruthy();
  expect(
    screen.queryByRole("link", { name: "Review inspection and connect" }),
  ).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).toHaveBeenCalledOnce();
});

it("keeps synchronization history and its independent cursor when rereading the parent source", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply(source))
    .mockResolvedValueOnce(etagReply({ items: [], nextCursor: "history-next" }))
    .mockResolvedValueOnce(etagReply({ items: [] }))
    .mockResolvedValueOnce(etagReply(source))
    .mockResolvedValue(etagReply({ items: [] }));
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByLabelText("Source action");
  click("Sync history");
  fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
  await screen.findByText("No snapshots.");
  click("Reload source");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(4));
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Reload source" }),
    ).toHaveProperty("disabled", false),
  );
  expect(
    screen
      .getByRole("button", { name: "Sync history" })
      .getAttribute("aria-pressed"),
  ).toBe("true");
  expect(screen.getByRole("button", { name: "First page" })).toBeTruthy();
  click("Refresh snapshots");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(5));
  expect(w.get.mock.calls[4]?.[1]).toMatchObject({
    params: {
      path: { sourceId: source.id },
      query: { limit: 25, cursor: "history-next" },
    },
  });
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps a source reader's navigation and direct inspection fields honest without private companion reads", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  const access = {
    ...administrator,
    permissions: ["source.read" as const],
    availablePermissions: ["source.read" as const],
    scopeEnforcement: true,
    scopes: ["sources.read" as const],
  };
  const view = render(<Federation {...w.props} access={access} />);
  await screen.findByText("No sources on this page.");
  expect(
    screen.queryByRole("link", { name: "Inspect a new source" }),
  ).toBeNull();
  expect(screen.queryByRole("link", { name: "Search diagnostics" })).toBeNull();
  expect(screen.getByRole("link", { name: "Observations" })).toBeTruthy();
  view.rerender(<Federation {...w.props} access={access} id="new" />);
  expect(screen.queryByLabelText("Source URL")).toBeNull();
  expect(screen.queryByRole("button", { name: "Inspect source" })).toBeNull();
  expect(screen.getByText(/source.inspect/)).toBeTruthy();
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
});

it("retains a source acknowledgement and last-loaded state after failed readback, then deliberately reloads", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(source));
  w.post.mockResolvedValue(
    etagReply({ ...source, status: "paused", revision: 2 }),
  );
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByLabelText("Source action");
  change("Source action", "pause");
  change("Reason", "Pause this source deliberately");
  await confirmCommand("Pause source");
  await screen.findByText(
    "Source paused. Reload to inspect its current state.",
  );
  expect(screen.getByText(/not been reread/)).toBeTruthy();
  expect(
    screen.getByText("Configured state").nextElementSibling?.textContent,
  ).toBe("Active");
  w.get.mockRejectedValueOnce(new Error("read unavailable"));
  click("Reload source");
  await screen.findByRole("alert");
  expect(
    screen.getByText("Source paused. Reload to inspect its current state."),
  ).toBeTruthy();
  expect(
    screen.getByText("Configured state").nextElementSibling?.textContent,
  ).toBe("Active");
  w.get.mockResolvedValue(
    etagReply({ ...source, status: "paused", revision: 2 }),
  );
  click("Reload");
  await waitFor(() =>
    expect(
      screen.getByText("Configured state").nextElementSibling?.textContent,
    ).toBe("Paused"),
  );
  expect(screen.queryByText(/not been reread/)).toBeNull();
  expect(
    screen.getByText("Source paused. Reload to inspect its current state."),
  ).toBeTruthy();
  expect(w.get).toHaveBeenCalledTimes(3);
  expect(w.post).toHaveBeenCalledOnce();
});

it("preserves proposed source configuration after failed readback and requires discard again before replacement", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(source));
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByLabelText("Source action");
  click("Preview configuration change or removal");
  change("Display name", "Keep this proposed name");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Reload source");
  expect(discard).toHaveBeenCalledOnce();
  expect(w.get).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Keep this proposed name",
  );
  discard.mockReturnValue(true);
  w.get.mockRejectedValueOnce(new Error("owned read unavailable"));
  click("Reload source");
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Keep this proposed name",
  );
  expect(screen.getByLabelText("Display name").matches(":disabled")).toBe(
    false,
  );
  expect(
    screen.getByRole("button", { name: "Preview source change" }),
  ).toHaveProperty("disabled", true);
  expect(screen.getByText(/Last loaded source/)).toBeTruthy();
  expect(w.get).toHaveBeenCalledTimes(2);
  discard.mockReturnValue(false);
  click("Reload");
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Keep this proposed name",
  );
  discard.mockReturnValue(true);
  click("Reload");
  await waitFor(() =>
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      source.displayName,
    ),
  );
  expect(w.get).toHaveBeenCalledTimes(3);
  expect(discard).toHaveBeenCalledTimes(4);
  expect(w.post).not.toHaveBeenCalled();
});

it("honors discard protection before opening source configuration from a missing ANS policy", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(source));
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByLabelText("Source action");
  click("ANS registration");
  const shortcut = screen.getByRole("button", {
    name: "Change source configuration",
  });
  const protect = (event: Event) => event.preventDefault();
  window.addEventListener("catalog-ui:leave", protect);
  try {
    fireEvent.click(shortcut);
    expect(confirm).toHaveBeenCalledOnce();
    expect(screen.queryByLabelText("Change type")).toBeNull();
    expect(screen.getByText(/Choose an ANS policy/)).toBeTruthy();
    confirm.mockReturnValue(true);
    fireEvent.click(shortcut);
    expect(screen.getByLabelText("Change type")).toHaveProperty(
      "value",
      "update",
    );
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
  } finally {
    window.removeEventListener("catalog-ui:leave", protect);
  }
});

it.each(["action", "configuration", "apply"] as const)(
  "keeps authorized %s input during delayed reload and holds successful readback when new input is kept",
  async (mode) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(source));
    w.post.mockResolvedValue(etagReply(preview));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(<Federation {...w.props} access={administrator} id={source.id} />);
    await screen.findByLabelText("Source action");
    if (mode === "action") change("Source action", "pause");
    else {
      click("Preview configuration change or removal");
      if (mode === "apply") {
        await confirmCommand("Preview source change");
        await screen.findByText("Exact change impact");
      }
    }
    const pending = deferred<ReturnType<typeof etagReply>>();
    w.get.mockReturnValueOnce(pending.promise);
    const writes = w.post.mock.calls.length;
    click("Reload source");
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
    const field = mode === "configuration" ? "Display name" : "Reason";
    const send =
      mode === "action"
        ? "Pause source"
        : mode === "apply"
          ? "Apply source change"
          : "Preview source change";
    expect(screen.getByLabelText(field).matches(":disabled")).toBe(false);
    change(field, "New input entered while source reloads");
    const submit = screen.getByRole("button", { name: send });
    expect(submit).toHaveProperty("disabled", true);
    fireEvent.submit(submit.closest("form")!);
    expect(w.post).toHaveBeenCalledTimes(writes);
    expect(w.patch).not.toHaveBeenCalled();
    const latest = {
      ...source,
      displayName: "Freshly read source",
      revision: 2,
    };
    await act(async () => pending.resolve(etagReply(latest, '"revision-2"')));
    await screen.findByText(/Your unsaved input was kept/);
    expect(screen.getByLabelText(field)).toHaveProperty(
      "value",
      "New input entered while source reloads",
    );
    expect(
      screen.getByRole("heading", { name: source.displayName }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("heading", { name: latest.displayName }),
    ).toBeNull();
    expect(screen.getByRole("button", { name: send })).toHaveProperty(
      "disabled",
      true,
    );
    expect(w.props.onFailure).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    w.get.mockResolvedValueOnce(etagReply(latest, '"revision-2"'));
    click("Reload");
    await screen.findByRole("heading", { name: latest.displayName });
    expect(screen.queryByText(/Your unsaved input was kept/)).toBeNull();
    if (mode === "configuration")
      expect(screen.getByLabelText("Display name")).toHaveProperty(
        "value",
        latest.displayName,
      );
    else if (mode === "action") {
      expect(screen.getByLabelText("Source action")).toHaveProperty(
        "value",
        "test",
      );
      change("Source action", "pause");
      expect(screen.getByLabelText("Reason")).toHaveProperty("value", "");
    } else
      expect(
        screen.queryByRole("button", { name: "Apply source change" }),
      ).toBeNull();
    expect(w.post).toHaveBeenCalledTimes(writes);
    expect(w.patch).not.toHaveBeenCalled();
  },
);

it("discards aborted late source readback without asking about newly entered input", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValueOnce(etagReply(source));
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const ui = render(
    <Federation {...w.props} access={administrator} id={source.id} />,
  );
  await screen.findByLabelText("Source action");
  change("Source action", "pause");
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValueOnce(pending.promise);
  click("Reload source");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  change("Reason", "Keep this new private source note");
  const options = w.get.mock.calls[1]?.[1] as { signal: AbortSignal };
  ui.unmount();
  expect(options.signal.aborted).toBe(true);
  await act(async () => pending.resolve(etagReply({ ...source, revision: 2 })));
  expect(confirm).not.toHaveBeenCalled();
  expect(w.props.onFailure).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it.each([false, true])(
  "preserves the selected sync command after partial authority loss, uncertain=%s",
  async (uncertain) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(source));
    w.post
      .mockResolvedValueOnce(etagReply({}))
      .mockResolvedValue(
        etagReply({ job: { ...job, kind: "source.sync" }, existing: true }),
      );
    const ui = render(
      <Federation {...w.props} access={administrator} id={source.id} />,
    );
    await screen.findByLabelText("Source action");
    change("Source action", "sync");
    click("Synchronize source");
    if (uncertain) {
      click("Confirm");
      await screen.findByText(/The outcome is unknown/);
    }
    const access = {
      ...administrator,
      permissions: administrator.permissions.filter(
        (value) => value !== "source.sync",
      ),
    };
    ui.rerender(<Federation {...w.props} access={access} id={source.id} />);
    expect(screen.getByLabelText("Source action")).toHaveProperty(
      "value",
      "sync",
    );
    expect(screen.getByRole("dialog").textContent).toContain(
      "Synchronize source",
    );
    if (uncertain) {
      expect(
        screen.getByRole("button", { name: "Retry same command" }),
      ).toHaveProperty("disabled", false);
      click("Retry same command");
      await screen.findByText(/Existing synchronization Job reused/);
      expect(w.post).toHaveBeenCalledTimes(2);
      expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    } else {
      const confirm = screen.getByRole("button", { name: "Confirm" });
      expect(confirm).toHaveProperty("disabled", true);
      fireEvent.click(confirm);
      expect(w.post).not.toHaveBeenCalled();
    }
    expect(w.get).toHaveBeenCalledOnce();
  },
);

it("installs current source data only after discarding edits added during a successful reload", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValueOnce(etagReply(source));
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByLabelText("Source action");
  change("Source action", "pause");
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValueOnce(pending.promise);
  click("Reload source");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  change("Reason", "Discard only after this successful read arrives");
  await act(async () =>
    pending.resolve(
      etagReply(
        { ...source, revision: 2, displayName: "Current source revision" },
        '"revision-2"',
      ),
    ),
  );
  await screen.findByRole("heading", { name: "Current source revision" });
  expect(confirm).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("Source action")).toHaveProperty(
    "value",
    "test",
  );
  change("Source action", "pause");
  expect(screen.getByLabelText("Reason")).toHaveProperty("value", "");
  expect(screen.queryByText(/Your unsaved input was kept/)).toBeNull();
  change("Reason", "Pause after reviewing the latest source revision");
  w.post.mockResolvedValue(
    etagReply({ ...source, revision: 3, status: "paused" }),
  );
  await confirmCommand("Pause source");
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/federation/sources/{sourceId}:pause",
    expect.objectContaining({
      params: {
        path: { sourceId: source.id },
        header: {
          "If-Match": '"revision-2"',
          "Idempotency-Key": expect.any(String) as string,
        },
      },
      body: { reason: "Pause after reviewing the latest source revision" },
    }),
  );
});

it("reloads source facts for a reader without inventing an editable action", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValueOnce(etagReply(source)).mockResolvedValueOnce(
    etagReply({
      ...source,
      revision: 2,
      displayName: "Current readable source",
    }),
  );
  const access = {
    ...administrator,
    permissions: ["source.read"] as typeof administrator.permissions,
    availablePermissions: [
      "source.read",
    ] as typeof administrator.availablePermissions,
  };
  render(<Federation {...w.props} access={access} id={source.id} />);
  await screen.findByRole("heading", { name: source.displayName });
  click("Reload source");
  await screen.findByRole("heading", { name: "Current readable source" });
  expect(screen.queryByLabelText("Source action")).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});
