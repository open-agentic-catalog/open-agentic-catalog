// @vitest-environment jsdom
import { expect, it } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { SnapshotComparison } from "./SnapshotComparison";
import { APIError } from "../api/client";
import { baseline, snapshot } from "../../tests/unit/publication-fixtures";
import { deferred } from "../../tests/unit/fixtures";
import {
  change,
  click,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/publication");
const entry = {
  identifier: "urn:example:agent",
  type: "application/json",
  description: "Original",
  url: "https://example.invalid/inert",
};
async function prepare(w: ReturnType<typeof screenWorkspace>) {
  render(
    <SnapshotComparison
      {...w.props}
      host={snapshot.host}
      snapshotId={snapshot.id}
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  click("Choose snapshot baseline");
  change("Baseline Snapshot ID", baseline.id.toUpperCase());
  click("Compare selected snapshots");
}
it("compares complete per-profile content, self-entries and exclusions without contacting references", async () => {
  const w = screenWorkspace();
  const self = {
    ...entry,
    identifier: "registry",
    metadata: { catalogReleaseId: "registry:self" },
  };
  const before = {
    ...baseline,
    ard: {
      context: "old",
      entries: [entry, self, { ...entry, identifier: "removed" }],
    },
    aiCatalog: { entries: [entry] },
    exclusions: [
      {
        resourceId: "old",
        identifier: "old",
        profiles: ["ard"],
        reasons: ["blocked"],
      },
    ],
  };
  const after = {
    ...snapshot,
    ardProfile: "new-profile",
    ard: {
      context: "new",
      entries: [
        { ...entry, description: "Updated" },
        { ...self, description: "Registry update" },
        { ...entry, identifier: "added" },
      ],
    },
    aiCatalog: { entries: [entry, { ...entry, version: "" }] },
    exclusions: [
      {
        resourceId: "new",
        identifier: "new",
        profiles: ["ard"],
        reasons: ["unavailable"],
      },
    ],
  };
  w.get
    .mockResolvedValueOnce(etagReply(before))
    .mockResolvedValueOnce(etagReply(after));
  await prepare(w);
  await screen.findByRole("heading", {
    name: "ARD resource entries: 3 changed occurrences",
  });
  expect(
    screen.getByRole("heading", {
      name: "ARD Registry self-entries: 1 changed occurrences",
    }),
  ).toBeTruthy();
  expect(
    screen.getByRole("heading", {
      name: "AI Catalog resource entries: 1 changed occurrences",
    }),
  ).toBeTruthy();
  expect(
    screen.getByRole("heading", {
      name: "Publication exclusions: 2 changed occurrences",
    }),
  ).toBeTruthy();
  const outcome = screen.getByRole("region", {
    name: "Captured snapshot comparison",
  });
  expect(outcome.textContent).toContain(
    "Observed baseline state: active; selected state: candidate",
  );
  expect(outcome.textContent).toContain("Original");
  expect(outcome.textContent).toContain("Updated");
  expect(outcome.textContent).toContain("ardProfile");
  expect(outcome.textContent).toContain("ardDocument");
  expect(
    outcome.querySelector('a[href="https://example.invalid/inert"]'),
  ).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.get.mock.calls.map((call) => call[1])).toEqual([
    expect.objectContaining({
      params: {
        path: { snapshotId: baseline.id },
        query: { host: baseline.host },
      },
    }),
    expect.objectContaining({
      params: {
        path: { snapshotId: snapshot.id },
        query: { host: snapshot.host },
      },
    }),
  ]);
  expect(w.post).not.toHaveBeenCalled();
  click("Close snapshot choices");
  expect(
    screen.getByRole("region", { name: "Captured snapshot comparison" }),
  ).toBe(outcome);
});
it("refreshes active context without replacing the captured comparison or silently changing its baseline", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply(baseline))
    .mockResolvedValueOnce(etagReply(snapshot));
  await prepare(w);
  await screen.findByText(/Observed baseline state/);
  const outcome = screen.getByRole("region", {
    name: "Captured snapshot comparison",
  });
  const next = {
    ...baseline,
    id: "00000000-0000-7000-8000-000000000003",
    generation: 3,
  };
  w.get.mockResolvedValue(etagReply({ items: [next] }));
  click("Read active snapshot");
  await screen.findByText(/selected baseline differs/);
  expect(screen.getByLabelText("Baseline Snapshot ID")).toHaveProperty(
    "value",
    baseline.id.toUpperCase(),
  );
  click("Use observed snapshot as baseline");
  expect(screen.getByLabelText("Baseline Snapshot ID")).toHaveProperty(
    "value",
    next.id,
  );
  expect(outcome.textContent).toContain(baseline.id);
  expect(outcome.textContent).not.toContain(next.id);
  expect(w.get).toHaveBeenCalledTimes(3);
  expect(w.post).not.toHaveBeenCalled();
});
it.each([false, true])(
  "pages all four comparison sections independently without transferring disclosure state (duplicate identities: %s)",
  async (duplicates) => {
    const w = screenWorkspace();
    const entries = Array.from({ length: 26 }, (_, i) => ({
      ...entry,
      identifier: `urn:example:entry-${duplicates && i === 1 ? 0 : i}`,
    }));
    w.get.mockResolvedValueOnce(etagReply(baseline)).mockResolvedValueOnce(
      etagReply({
        ...snapshot,
        ard: {
          entries: [
            ...entries,
            ...entries.map((item) => ({
              ...item,
              metadata: { catalogReleaseId: "registry:self" },
            })),
          ],
        },
        aiCatalog: { entries },
        exclusions: entries.map((item, index) => ({
          resourceId: `resource-${index}`,
          identifier: item.identifier,
          profiles: ["ard"],
          reasons: ["blocked"],
        })),
      }),
    );
    await prepare(w);
    await screen.findByRole("heading", {
      name: "ARD resource entries: 26 changed occurrences",
    });
    const regions = [
      "ARD resource entries",
      "ARD Registry self-entries",
      "AI Catalog resource entries",
      "Publication exclusions",
    ].map((name) => screen.getByRole("region", { name }));
    const pagers = regions.map((region) =>
      within(region).getByRole("group", {
        name: `${region.getAttribute("aria-label")} comparison pagination`,
      }),
    );
    const expectPage = (region: HTMLElement, last: boolean) => {
      expect(region.querySelectorAll("details")).toHaveLength(last ? 1 : 25);
      expect(region.textContent).toContain(
        `urn:example:entry-${last ? 25 : 0}`,
      );
      expect(region.textContent).not.toContain(
        `urn:example:entry-${last ? 0 : 25}`,
      );
    };
    for (const region of regions) expectPage(region, false);
    for (const [index, pager] of pagers.entries()) {
      const first = regions[index]!.querySelector("details")!;
      first.open = true;
      fireEvent.click(within(pager).getByRole("button", { name: "Next page" }));
      const next = regions[index]!.querySelector("details")!;
      expect(next.open).toBe(false);
      expect(document.activeElement).toBe(pager);
      for (const [otherIndex, other] of regions.entries())
        expectPage(other, otherIndex <= index);
      expect(
        within(pager).queryByRole("button", { name: "Next page" }),
      ).toBeNull();
    }
    for (const [index, pager] of pagers.entries()) {
      fireEvent.click(
        within(pager).getByRole("button", { name: "First page" }),
      );
      expect(regions[index]!.querySelector("details")).toHaveProperty(
        "open",
        false,
      );
      expect(document.activeElement).toBe(pager);
      for (const [otherIndex, other] of regions.entries())
        expectPage(other, otherIndex > index);
      expect(
        within(pager).queryByRole("button", { name: "First page" }),
      ).toBeNull();
    }
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.post).not.toHaveBeenCalled();
  },
);
it.each(["id", "tenant", "host", "audience", "malformed", "missing"])(
  "rejects a %s baseline and recovers through explicit reread",
  async (failure) => {
    const w = screenWorkspace();
    const bad = {
      ...baseline,
      ...(failure === "id"
        ? { id: "other" }
        : failure === "tenant"
          ? { tenantId: "other" }
          : failure === "host"
            ? { host: "other.example.com" }
            : failure === "audience"
              ? { audience: "authenticated" }
              : failure === "malformed"
                ? { ard: null }
                : {}),
    };
    if (failure === "missing")
      w.get.mockRejectedValueOnce(new APIError(404, "NOT_FOUND"));
    else w.get.mockResolvedValueOnce(etagReply(bad));
    w.get.mockResolvedValueOnce(etagReply(snapshot));
    await prepare(w);
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("heading", { name: /^ARD resource entries:/ }),
    ).toBeNull();
    expect(w.get).toHaveBeenCalledTimes(2);
    w.get
      .mockResolvedValueOnce(etagReply(baseline))
      .mockResolvedValueOnce(etagReply(snapshot));
    click("Reload");
    await screen.findByText(/Observed baseline state/);
    expect(w.get).toHaveBeenCalledTimes(4);
    expect(w.post).not.toHaveBeenCalled();
  },
);
it.each(["baseline", "selected"])(
  "discards a late %s read when a new explicit comparison replaces the old selection",
  async (side) => {
    const w = screenWorkspace(),
      pending = deferred<unknown>();
    w.get
      .mockReturnValueOnce(
        side === "baseline"
          ? pending.promise
          : Promise.resolve(etagReply(baseline)),
      )
      .mockReturnValueOnce(
        side === "selected"
          ? pending.promise
          : Promise.resolve(etagReply(snapshot)),
      );
    await prepare(w);
    const old = w.get.mock.calls[side === "baseline" ? 0 : 1]?.[1] as {
      signal: AbortSignal;
    };
    const next = { ...baseline, id: "00000000-0000-7000-8000-000000000003" };
    w.get
      .mockResolvedValueOnce(etagReply(next))
      .mockResolvedValueOnce(etagReply(snapshot));
    change("Baseline Snapshot ID", next.id);
    click("Compare selected snapshots");
    await screen.findByText(/Observed baseline state/);
    expect(old.signal.aborted).toBe(true);
    await act(async () =>
      pending.resolve(etagReply(side === "baseline" ? baseline : snapshot)),
    );
    const outcome = screen.getByRole("region", {
      name: "Captured snapshot comparison",
    });
    expect(outcome.textContent).toContain(next.id);
    expect(outcome.textContent).not.toContain(baseline.id);
    expect(w.get).toHaveBeenCalledTimes(4);
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
  },
);
