// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Reviews from "./Reviews";
import { required } from "../../tests/required";
import { reader, sessionAccess } from "../../tests/unit/fixtures";
import { release, submission } from "../../tests/unit/responses";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  listing,
  readReplies,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/reviews/submission-1");
const approved = {
  ...submission,
  namespaceId: listing.namespaceId,
  listingConcurrencyRevision: 1,
  state: "approved",
};
const next = { ...release, tenantId: listing.tenantId, id: "new-release" };
const prior = {
  ...release,
  tenantId: listing.tenantId,
  id: "old-release",
  state: "retired",
};
function receipt(priorRelease: typeof prior | null = prior) {
  return {
    listing: { ...listing, currentReleaseId: next.id },
    release: next,
    priorRelease,
  };
}
function routes(currentReleaseId: string | undefined = prior.id) {
  return {
    "/api/v1/review-submissions/{submissionId}": approved,
    "/api/v1/catalog-listings/{listingId}": { ...listing, currentReleaseId },
    "/api/v1/catalog-listings/{listingId}/releases": { items: [prior, next] },
  };
}
async function ready() {
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Publish approved listing" }),
    ).toHaveProperty("disabled", false),
  );
}

it.each(["id", "tenantId"] as const)(
  "refuses an exact submission read with a mismatched %s before loading Listing context",
  async (field) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      ...routes(),
      "/api/v1/review-submissions/{submissionId}": {
        ...approved,
        [field]: "unrelated-identity",
      },
    });
    render(<Reviews {...w.props} access={administrator} id={approved.id} />);
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", { name: "Publish approved listing" }),
    ).toBeNull();
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
    readReplies(w.get, routes());
    click("Reload review and next steps");
    await ready();
    expect(w.get).toHaveBeenCalledTimes(3);
  },
);

it("normalizes an uppercase exact Submission UUID before lookup and identity matching", async () => {
  const id = "00000000-0000-7000-8000-abcdef000001";
  const w = screenWorkspace();
  readReplies(w.get, {
    ...routes(),
    "/api/v1/review-submissions/{submissionId}": { ...approved, id },
  });
  render(<Reviews {...w.props} access={administrator} id={id.toUpperCase()} />);
  await ready();
  expect(w.get.mock.calls[0]?.[1]).toMatchObject({
    params: { path: { submissionId: id } },
  });
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});

it("discards a confirmed prior-release draft after explicit readback finds no current pointer", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  const published = receipt(null);
  published.listing.revision = 3;
  w.post.mockResolvedValue(etagReply(published, '"rev-3"'));
  render(<Reviews {...w.props} access={administrator} id={approved.id} />);
  await ready();
  change("Previous release disposition", "superseded");
  change("Reason", "Obsolete supersession draft");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Reload review and next steps");
  expect(discard).toHaveBeenCalledOnce();
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Obsolete supersession draft",
  );
  discard.mockReturnValue(true);
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/review-submissions/{submissionId}")
      return etagReply(approved);
    expect(path).toBe("/api/v1/catalog-listings/{listingId}");
    return etagReply({ ...listing, revision: 2 }, '"rev-2"');
  });
  click("Reload review and next steps");
  await screen.findByText("No current release at the loaded Listing revision");
  await ready();
  expect(discard).toHaveBeenCalledTimes(2);
  expect(screen.queryByLabelText("Previous release disposition")).toBeNull();
  expect(screen.queryByLabelText("Reason")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
  click("Publish approved listing");
  const dialog = screen.getByRole("dialog");
  expect(dialog.textContent).not.toContain("Obsolete supersession draft");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain('"rev-2"');
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await screen.findByText("No prior release was changed by this command.");
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog-listings/{listingId}:publish",
    expect.objectContaining({
      params: {
        path: { listingId: listing.id },
        header: {
          "If-Match": '"rev-2"',
          "Idempotency-Key": expect.stringMatching(/\S+/) as unknown,
        },
      },
      body: { submissionId: approved.id, priorDisposition: "retired" },
    }),
  );
  expect(w.get).toHaveBeenCalledTimes(4);
});

it("publishes an uninspected existing release with supersession and reads history without metadata access", async () => {
  const w = screenWorkspace();
  const replaced = {
    ...prior,
    state: "superseded",
    replacementReleaseId: next.id,
  };
  readReplies(w.get, {
    ...routes(),
    "/api/v1/catalog-listings/{listingId}/releases": {
      items: [replaced, next],
    },
  });
  w.post.mockResolvedValue(etagReply(receipt(replaced)));
  const access: typeof reader = {
    ...reader,
    permissions: [
      "resource.read_history",
      "resource.read_content",
      "release.publish",
    ],
    availablePermissions: [
      "resource.read_history",
      "resource.read_content",
      "release.publish",
    ],
    scopeEnforcement: true,
    scopes: ["catalog.read", "catalog.publish"],
  };
  render(<Reviews {...w.props} access={access} id={approved.id} />);
  await ready();
  await screen.findByText("Uninspected: current Listing metadata was not read");
  expect(w.get).toHaveBeenCalledOnce();
  change("Previous release disposition", "superseded");
  change("Reason", "Replace an uninspected prior release");
  click("Publish approved listing");
  const dialog = screen.getByRole("dialog");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain("Uninspected");
  expect(dialog.textContent).not.toContain(prior.id);
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  const outcome = await screen.findByRole("region", {
    name: "Publication command outcome",
  });
  expect(outcome.textContent).toContain(prior.id);
  expect(outcome.textContent).toContain("superseded");
  expect(
    within(outcome)
      .getByRole("link", { name: "Open prior release" })
      .getAttribute("href"),
  ).toBe("/unit/releases/old-release");
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    params: { header: { "If-Match": '"rev-1"' } },
    body: {
      submissionId: approved.id,
      priorDisposition: "superseded",
      priorReason: "Replace an uninspected prior release",
    },
  });
  expect(w.get).toHaveBeenCalledOnce();
  click("Read current release history");
  await screen.findByRole("link", { name: /^superseded -/ });
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/review-submissions/{submissionId}",
    "/api/v1/catalog-listings/{listingId}/releases",
  ]);
  expect(w.post).toHaveBeenCalledOnce();
});

it("captures observed replacement context and keeps a validated receipt through failed explicit history readback", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  const replaced = {
    ...prior,
    state: "superseded",
    replacementReleaseId: next.id,
  };
  w.post.mockResolvedValue(etagReply(receipt(replaced)));
  const view = render(
    <Reviews {...w.props} access={administrator} id={approved.id} />,
  );
  await ready();
  change("Previous release disposition", "superseded");
  change("Reason", "  Replace the reviewed release 🙂  ");
  click("Publish approved listing");
  const dialog = screen.getByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  for (const text of [
    prior.id,
    approved.id,
    listing.id,
    '"revision-1"',
    "superseded",
    "Replace the reviewed release 🙂",
  ])
    expect(facts.textContent).toContain(text);
  expect(dialog.textContent).toContain(
    "Public discovery snapshots are managed separately",
  );
  expect(w.post).not.toHaveBeenCalled();
  change("Previous release disposition", "retired");
  change("Reason (optional)", "Later unconfirmed reason");
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  const outcome = await screen.findByRole("region", {
    name: "Publication command outcome",
  });
  expect(outcome.textContent).toContain("superseded");
  expect(outcome.textContent).toContain("historical replay");
  expect(
    within(outcome)
      .getByRole("link", { name: "Open prior release" })
      .getAttribute("href"),
  ).toBe("/unit/releases/old-release");
  expect(w.post.mock.calls[0]).toMatchObject({
    0: "/api/v1/catalog-listings/{listingId}:publish",
    1: {
      params: {
        path: { listingId: listing.id },
        header: { "If-Match": '"revision-1"' },
      },
      body: {
        submissionId: approved.id,
        priorDisposition: "superseded",
        priorReason: "Replace the reviewed release 🙂",
      },
    },
  });
  expect(w.get).toHaveBeenCalledTimes(2);
  w.get.mockRejectedValueOnce(new Error("Controlled history outage"));
  click("Read current release history");
  await screen.findByRole("alert");
  expect(
    screen.getByRole("region", { name: "Publication command outcome" }),
  ).toBe(outcome);
  expect(screen.getByRole("status").textContent).toContain(
    "Listing published.",
  );
  expect(w.post).toHaveBeenCalledOnce();
  click("Reload current release history");
  await screen.findByText(
    "Historical releases may no longer be available in discovery.",
    { exact: false },
  );
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(w.get).toHaveBeenCalledTimes(4);
  const historyAccess = {
    ...administrator,
    permissions: [
      "resource.read",
      "release.publish",
    ] as typeof administrator.permissions,
    availablePermissions: [
      "resource.read",
      "release.publish",
    ] as typeof administrator.permissions,
  };
  view.rerender(
    <Reviews {...w.props} access={historyAccess} id={approved.id} />,
  );
  expect(
    screen.queryByRole("link", { name: "Open published release" }),
  ).toBeNull();
  expect(screen.queryByRole("link", { name: "Open prior release" })).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Reload current release history" }),
  ).toBeNull();
  expect(
    screen.getByRole("region", { name: "Publication command outcome" })
      .textContent,
  ).toContain(prior.id);
  expect(w.get).toHaveBeenCalledTimes(4);
});

it.each(["retired", "revoked", "superseded"])(
  "does not infer first publication from a missing pointer after retained %s history",
  async (state) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      ...routes(),
      "/api/v1/catalog-listings/{listingId}": listing,
      "/api/v1/catalog-listings/{listingId}/releases": {
        items: [{ ...prior, state }],
      },
    });
    w.post.mockResolvedValue(etagReply(receipt(null)));
    render(<Reviews {...w.props} access={administrator} id={approved.id} />);
    await ready();
    expect(
      screen.getByText("No current release at the loaded Listing revision"),
    ).toBeTruthy();
    expect(screen.queryByLabelText("Previous release disposition")).toBeNull();
    expect(
      screen.getByText(/does not mean this Listing was never published/),
    ).toBeTruthy();
    expect(w.get).toHaveBeenCalledTimes(2);
    await confirmCommand("Publish approved listing");
    await screen.findByText("No prior release was changed by this command.");
    expect(w.post.mock.calls[0]?.[1]).toMatchObject({
      body: { submissionId: approved.id, priorDisposition: "retired" },
    });
    expect(w.post.mock.calls[0]?.[1]).not.toHaveProperty("body.priorReason");
    click("Read current release history");
    await screen.findByRole("link", { name: new RegExp(`^${state} -`) });
    expect(w.get).toHaveBeenCalledTimes(3);
    expect(w.post).toHaveBeenCalledOnce();
  },
);

it.each([undefined, 7])(
  "preserves minimal publication without metadata reads with concurrency %s",
  async (revision) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      ...routes(),
      "/api/v1/review-submissions/{submissionId}": {
        ...approved,
        listingConcurrencyRevision: revision,
      },
    });
    w.post.mockResolvedValue(etagReply(receipt(null)));
    const access: typeof reader = {
      ...reader,
      permissions: [
        "resource.read_history",
        "release.publish",
      ] as typeof reader.permissions,
      availablePermissions: [
        "resource.read_history",
        "release.publish",
      ] as typeof reader.permissions,
      scopeEnforcement: true,
      scopes: ["catalog.read", "catalog.publish"],
    };
    render(<Reviews {...w.props} access={access} id={approved.id} />);
    await screen.findByText(
      "Uninspected: current Listing metadata was not read",
    );
    expect(w.get).toHaveBeenCalledOnce();
    expect(
      screen.getByRole("button", { name: "Publish approved listing" }),
    ).toHaveProperty("disabled", !revision);
    if (!revision) {
      expect(
        screen.getByText("Unavailable", { selector: "code" }),
      ).toBeTruthy();
      click("Publish approved listing");
      expect(w.post).not.toHaveBeenCalled();
      return;
    }
    await confirmCommand("Publish approved listing");
    await screen.findByRole("link", { name: "Open published release" });
    expect(w.post.mock.calls[0]?.[1]).toMatchObject({
      params: { header: { "If-Match": '"rev-7"' } },
    });
    expect(w.get).toHaveBeenCalledOnce();
  },
);

it.each(["listing", "tenant", "namespace"])(
  "rejects unrelated %s metadata before publication, then recovers through a valid read",
  async (field) => {
    const w = screenWorkspace();
    const changed = {
      ...listing,
      [field === "listing"
        ? "id"
        : field === "tenant"
          ? "tenantId"
          : "namespaceId"]: "other",
    };
    readReplies(w.get, {
      ...routes(),
      "/api/v1/catalog-listings/{listingId}": changed,
    });
    render(<Reviews {...w.props} access={administrator} id={approved.id} />);
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", { name: "Publish approved listing" }),
    ).toBeNull();
    expect(w.props.onFailure).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({
        message: "Listing context does not match the submission.",
      }),
    );
    readReplies(w.get, routes());
    click("Reload review and next steps");
    await ready();
    expect(w.get).toHaveBeenCalledTimes(4);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  "missing-prior",
  "foreign-prior",
  "foreign-new",
  "wrong-prior-reference",
  "missing-observed-prior",
  "unexpected-prior",
])(
  "refuses a %s receipt and retains exact-key recovery without publishing unsafe links",
  async (failure) => {
    const w = screenWorkspace();
    const absent = failure === "unexpected-prior";
    readReplies(w.get, {
      ...routes(),
      ...(absent ? { "/api/v1/catalog-listings/{listingId}": listing } : {}),
    });
    const bad: Record<string, unknown> = structuredClone(receipt());
    if (failure === "missing-prior") delete bad.priorRelease;
    if (failure === "foreign-prior")
      bad.priorRelease = {
        ...prior,
        tenantId: "foreign-tenant",
        id: "foreign-release",
      };
    if (failure === "foreign-new")
      bad.release = { ...next, listingId: "foreign-listing" };
    if (failure === "wrong-prior-reference")
      bad.priorRelease = { ...prior, id: "other-old-release" };
    if (failure === "missing-observed-prior") bad.priorRelease = null;
    w.post
      .mockResolvedValueOnce(etagReply(bad))
      .mockResolvedValue(etagReply(receipt(absent ? null : prior)));
    render(<Reviews {...w.props} access={administrator} id={approved.id} />);
    await ready();
    await confirmCommand("Publish approved listing");
    await screen.findByText(/The outcome is unknown/);
    expect(
      screen.queryByRole("region", { name: "Publication command outcome" }),
    ).toBeNull();
    expect(
      screen.queryByRole("link", { name: "Open prior release" }),
    ).toBeNull();
    expect(screen.queryByText("foreign-release")).toBeNull();
    expect(screen.queryByText("Listing published.")).toBeNull();
    const original = structuredClone(required(w.post.mock.calls[0]));
    click("Retry same command");
    await screen.findByRole("region", { name: "Publication command outcome" });
    expect(w.post).toHaveBeenCalledTimes(2);
    expect(w.post.mock.calls[1]).toEqual(original);
    expect(w.get).toHaveBeenCalledTimes(2);
  },
);

it("uses a later-page metadata hint only on deliberate review reload", async () => {
  const w = screenWorkspace();
  const namespaceId = "00000000-0000-7000-8000-000000000099";
  const permissions = [
    "resource.read",
    "resource.read_history",
    "release.publish",
  ] as const;
  const access = {
    ...sessionAccess,
    permissions: [],
    availablePermissions: [...permissions],
    namespaceGrants: [],
    nextCursor: "later",
  };
  readReplies(w.get, {
    ...routes(),
    "/api/v1/review-submissions/{submissionId}": {
      ...approved,
      namespaceId,
      tenantId: access.tenantId,
    },
    "/api/v1/catalog-listings/{listingId}": {
      ...listing,
      namespaceId,
      tenantId: access.tenantId,
      currentReleaseId: prior.id,
    },
    "/api/v1/me": {
      ...access,
      nextCursor: undefined,
      namespaceGrants: [{ namespaceId, permissions: [...permissions] }],
    },
  });
  render(<Reviews {...w.props} access={access} id={approved.id} />);
  await screen.findByText("Uninspected: current Listing metadata was not read");
  await ready();
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/review-submissions/{submissionId}",
    "/api/v1/me",
  ]);
  await act(async () => {
    click("Reload review and next steps");
  });
  await screen.findByText(prior.id);
  await ready();
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/review-submissions/{submissionId}",
    "/api/v1/me",
    "/api/v1/review-submissions/{submissionId}",
    "/api/v1/catalog-listings/{listingId}",
  ]);
  expect(w.get.mock.calls[1]?.[1]).toMatchObject({
    params: { query: { namespaceId } },
  });
  expect(w.post).not.toHaveBeenCalled();
});
