// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it } from "vitest";
import { deferred } from "../../tests/unit/fixtures";
import { Release } from "./Reviews";
import * as checks from "./responses";
import { release as releaseSeed } from "../../tests/unit/responses";
import {
  administrator,
  change,
  click,
  etagReply,
  listing,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/releases");
const release = { ...releaseSeed, id: "00000000-0000-7000-8000-000000000101" };
const path = "/api/v1/listing-releases/{releaseId}/withdrawal-preview";
const snapshot = {
  snapshotId: "00000000-0000-7000-8000-000000000201",
  host: "public.example.com",
  audience: "anonymous",
  generation: 4,
  etag: '"bundle-four"',
};
const preview = {
  releaseId: release.id,
  releaseRevision: release.revision,
  transition: "retired",
  observedAt: stamp,
  snapshots: [snapshot],
};

function fixture(value: unknown = preview) {
  const w = screenWorkspace();
  const routes: Record<string, unknown> = {
    "/api/v1/listing-releases/{releaseId}": release,
    "/api/v1/catalog-listings/{listingId}": listing,
    [path]: value,
  };
  readReplies(w.get, routes);
  return { ...w, routes };
}

it("reads only on explicit request, refreshes changed feeds and captures their exact references in confirmation", async () => {
  const w = fixture();
  render(<Release {...w.props} id={release.id} access={administrator} />);
  await screen.findByLabelText("Disposition");
  expect(w.get.mock.calls.some(([url]) => url === path)).toBe(false);
  change("Disposition", "retire");
  change("Reason", "Deliberate retirement");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Preview or refresh affected feeds" }),
    ).toHaveProperty("disabled", false),
  );
  expect(w.get.mock.calls.some(([url]) => url === path)).toBe(false);
  click("Preview or refresh affected feeds");
  await screen.findByText(snapshot.snapshotId);
  expect(w.get.mock.calls.find(([url]) => url === path)?.[1]).toMatchObject({
    params: {
      path: { releaseId: release.id },
      query: { transition: "retired" },
    },
  });
  expect(w.post).not.toHaveBeenCalled();
  click("Change release disposition");
  let dialog = await screen.findByRole("dialog");
  expect(
    within(dialog).getByRole("region", { name: "Observed discovery feeds" })
      .textContent,
  ).toContain(snapshot.host);
  expect(
    within(dialog).getByRole("region", { name: "Observed discovery feeds" })
      .textContent,
  ).toContain(snapshot.snapshotId);
  expect(
    screen.getByRole("button", { name: "Preview or refresh affected feeds" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  const changed = {
    ...snapshot,
    snapshotId: "00000000-0000-7000-8000-000000000202",
    generation: 5,
    etag: '"bundle-five"',
  };
  w.routes[path] = { ...preview, snapshots: [changed] };
  click("Preview or refresh affected feeds");
  await screen.findByText(changed.snapshotId);
  expect(screen.getByText(/Affected feeds changed/)).toBeTruthy();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Deliberate retirement",
  );
  click("Change release disposition");
  dialog = await screen.findByRole("dialog");
  expect(
    within(dialog).getByRole("region", { name: "Observed discovery feeds" })
      .textContent,
  ).toContain(changed.snapshotId);
  expect(
    within(dialog).getByRole("region", { name: "Observed discovery feeds" })
      .textContent,
  ).not.toContain(snapshot.snapshotId);
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["malformed response", "access denied"] as const)(
  "keeps the snapshot preview and confirmation usable after a separate dependency read returns %s",
  async (failure) => {
    const w = fixture();
    w.routes["/api/v1/listing-releases/{releaseId}/relationships"] = {
      items: [{ declaration: {} }],
    };
    render(<Release {...w.props} id={release.id} access={administrator} />);
    await screen.findByLabelText("Disposition");
    change("Disposition", "retire");
    change("Reason", "Reviewed retirement");
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "Preview or refresh affected feeds",
        }),
      ).toHaveProperty("disabled", false),
    );
    click("Preview or refresh affected feeds");
    await screen.findByText(snapshot.snapshotId);
    if (failure === "access denied") {
      w.get.mockImplementationOnce(async () => ({
        error: { code: "NOT_FOUND" },
        response: new Response(null, { status: 404 }),
      }));
    }
    click("Check declared dependencies");
    const panel = screen.getByRole("region", {
      name: "Declared dependency impact",
    });
    const alert = await within(panel).findByRole("alert");
    if (failure === "access denied")
      expect(alert.textContent).toContain(
        "unavailable with your current access",
      );
    expect(
      within(panel).queryByText("No currently readable declarations match."),
    ).toBeNull();
    click("Change release disposition");
    expect(
      within(await screen.findByRole("dialog")).getByText(snapshot.snapshotId),
    ).toBeTruthy();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps original-key recovery separate from preview refresh after an uncertain write", async () => {
  const w = fixture();
  w.post.mockRejectedValueOnce(new Error("Owned lost response"));
  w.post.mockResolvedValueOnce(
    etagReply({ ...release, state: "retired", revision: 2 }),
  );
  const view = render(
    <Release {...w.props} id={release.id} access={administrator} />,
  );
  await screen.findByLabelText("Disposition");
  change("Disposition", "retire");
  change("Reason", "Keep original command");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Preview or refresh affected feeds" }),
    ).toHaveProperty("disabled", false),
  );
  click("Preview or refresh affected feeds");
  await screen.findByText(snapshot.snapshotId);
  click("Change release disposition");
  const dialog = await screen.findByRole("dialog");
  await act(async () => {
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  });
  await screen.findByRole("button", { name: "Retry same command" });
  const original = w.post.mock.calls[0];
  const reads = w.get.mock.calls.filter(([url]) => url === path).length;
  expect(
    screen.getByRole("button", { name: "Preview or refresh affected feeds" }),
  ).toHaveProperty("disabled", true);
  expect(within(dialog).getByText(snapshot.snapshotId)).toBeTruthy();
  const limited = {
    ...administrator,
    availablePermissions: administrator.availablePermissions.filter(
      (value) => value !== "publication.read",
    ),
    permissions: administrator.permissions.filter(
      (value) => value !== "publication.read",
    ),
  };
  view.rerender(<Release {...w.props} id={release.id} access={limited} />);
  await waitFor(() =>
    expect(within(dialog).queryByText(snapshot.snapshotId)).toBeNull(),
  );
  expect(within(dialog).queryByText(snapshot.host)).toBeNull();
  expect(screen.queryByText(snapshot.snapshotId)).toBeNull();
  expect(screen.queryByText(snapshot.host)).toBeNull();
  click("Retry same command");
  await screen.findByText("Release retired.");
  expect(w.post.mock.calls[1]).toEqual(original);
  expect(w.get.mock.calls.filter(([url]) => url === path)).toHaveLength(reads);
});

it("labels verified absence distinctly and refuses a preview for a different release revision", async () => {
  const w = fixture({ ...preview, snapshots: [] });
  render(<Release {...w.props} id={release.id} access={administrator} />);
  await screen.findByLabelText("Disposition");
  change("Disposition", "retire");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Preview or refresh affected feeds" }),
    ).toHaveProperty("disabled", false),
  );
  change("Reason", "Explicit observed retirement");
  click("Preview or refresh affected feeds");
  await screen.findByText(/No active discovery snapshot contains/);
  expect(
    screen.getByRole("button", { name: "Change release disposition" }),
  ).toHaveProperty("disabled", false);
  w.routes[path] = { ...preview, releaseRevision: 2 };
  click("Preview or refresh affected feeds");
  await screen.findByRole("alert");
  expect(
    screen.getByRole("button", { name: "Change release disposition" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.queryByText(/No active discovery snapshot contains/),
  ).toBeNull();
});

it("clears private preview data after access loss and does not substitute zero for invalid evidence", async () => {
  const w = fixture();
  const view = render(
    <Release {...w.props} id={release.id} access={administrator} />,
  );
  await screen.findByLabelText("Disposition");
  change("Disposition", "retire");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Preview or refresh affected feeds" }),
    ).toHaveProperty("disabled", false),
  );
  click("Preview or refresh affected feeds");
  await screen.findByText(snapshot.snapshotId);
  w.routes[path] = {
    ...preview,
    snapshots: [{ ...snapshot, audience: "unknown" }],
  };
  click("Preview or refresh affected feeds");
  await screen.findByText(/Impact is unavailable, not zero/);
  expect(screen.queryByText(snapshot.snapshotId)).toBeNull();
  const limited = {
    ...administrator,
    availablePermissions: administrator.availablePermissions.filter(
      (value) => value !== "publication.read",
    ),
    permissions: administrator.permissions.filter(
      (value) => value !== "publication.read",
    ),
  };
  view.rerender(<Release {...w.props} id={release.id} access={limited} />);
  await screen.findByText(/Detailed impact requires/);
  expect(
    screen.queryByRole("button", { name: "Preview or refresh affected feeds" }),
  ).toBeNull();
  expect(
    screen.queryByText(/No active discovery snapshot contains/),
  ).toBeNull();
});

it.each(["disposition", "access"] as const)(
  "ignores a late preview after %s changes and requires another explicit read",
  async (boundary) => {
    const w = fixture();
    const reads = w.get.getMockImplementation()!;
    const pending = deferred<ReturnType<typeof etagReply>>();
    let requested = 0;
    const replacement = {
      ...snapshot,
      snapshotId: "00000000-0000-7000-8000-000000000203",
    };
    w.get.mockImplementation((url, ...args) => {
      if (url !== path) return reads(url, ...args);
      requested++;
      return requested === 1
        ? pending.promise
        : Promise.resolve(
            etagReply({
              ...preview,
              transition: boundary === "disposition" ? "revoked" : "retired",
              snapshots: [replacement],
            }),
          );
    });
    const view = render(
      <Release {...w.props} id={release.id} access={administrator} />,
    );
    await screen.findByLabelText("Disposition");
    change("Disposition", "retire");
    change("Reason", "Keep the selected release intent");
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "Preview or refresh affected feeds",
        }),
      ).toHaveProperty("disabled", false),
    );
    click("Preview or refresh affected feeds");
    await waitFor(() => expect(requested).toBe(1));
    expect(
      screen.getByRole("button", { name: "Change release disposition" }),
    ).toHaveProperty("disabled", true);
    if (boundary === "disposition") change("Disposition", "revoke");
    else {
      const limited = {
        ...administrator,
        permissions: administrator.permissions.filter(
          (value) => value !== "publication.read",
        ),
        availablePermissions: administrator.availablePermissions.filter(
          (value) => value !== "publication.read",
        ),
      };
      view.rerender(<Release {...w.props} id={release.id} access={limited} />);
      await screen.findByText(/Detailed impact requires/);
    }
    await act(async () => pending.resolve(etagReply(preview)));
    expect(screen.queryByText(snapshot.snapshotId)).toBeNull();
    expect(requested).toBe(1);
    expect(w.post).not.toHaveBeenCalled();
    if (boundary === "access")
      view.rerender(
        <Release {...w.props} id={release.id} access={administrator} />,
      );
    await waitFor(() =>
      expect(
        screen.getByRole("button", {
          name: "Preview or refresh affected feeds",
        }),
      ).toHaveProperty("disabled", false),
    );
    expect(requested).toBe(1);
    click("Preview or refresh affected feeds");
    await screen.findByText(replacement.snapshotId);
    expect(requested).toBe(2);
    expect(screen.queryByText(snapshot.snapshotId)).toBeNull();
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Keep the selected release intent",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("validates consumed preview fields, duplicates, bounds and the deprecation exclusion", () => {
  expect(checks.ReleaseWithdrawalPreview(preview)).toBe(true);
  expect(checks.ReleaseWithdrawalPreview({ ...preview, snapshots: [] })).toBe(
    true,
  );
  for (const value of [
    null,
    { ...preview, releaseRevision: 0 },
    { ...preview, transition: "deprecated" },
    { ...preview, observedAt: "bad" },
    { ...preview, snapshots: null },
    { ...preview, snapshots: [snapshot, snapshot] },
    { ...preview, snapshots: [{ ...snapshot, generation: 0 }] },
    { ...preview, snapshots: [{ ...snapshot, audience: "public" }] },
    { ...preview, snapshots: [{ ...snapshot, etag: "" }] },
    { ...preview, snapshots: [{ ...snapshot, etag: "x".repeat(257) }] },
  ])
    expect(checks.ReleaseWithdrawalPreview(value)).toBe(false);
});
