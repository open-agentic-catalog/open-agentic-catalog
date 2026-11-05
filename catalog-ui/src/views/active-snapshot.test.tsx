// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { ActiveSnapshot } from "./ActiveSnapshot";
import { baseline } from "../../tests/unit/publication-fixtures";
import { deferred } from "../../tests/unit/fixtures";
import {
  click,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/publication");
it("reads one exact active summary only on request and retains a failed or pending observation", async () => {
  const w = screenWorkspace(),
    choose = vi.fn();
  w.get.mockResolvedValue(etagReply({ items: [baseline] }));
  render(
    <ActiveSnapshot
      {...w.props}
      host={baseline.host}
      onChoose={choose}
      baselineId="previous-baseline"
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  click("Read active snapshot");
  await screen.findByText(baseline.id);
  expect(w.get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/publication/snapshots",
    expect.objectContaining({
      params: { query: { host: baseline.host, state: "active", limit: 1 } },
    }),
  );
  expect(screen.getByText(/selected baseline differs/)).toBeTruthy();
  click("Use observed snapshot as baseline");
  expect(choose).toHaveBeenCalledExactlyOnceWith(baseline.id, baseline);
  expect(screen.getByRole("link").getAttribute("href")).toBe(
    `/unit/publication/${baseline.id}?host=catalog.example.com`,
  );
  const pending = deferred<unknown>();
  w.get.mockReturnValueOnce(pending.promise);
  click("Refresh active snapshot");
  await screen.findByText(/previous observation is retained/);
  await act(async () => pending.reject(new Error("Controlled read failure")));
  await screen.findByRole("alert");
  expect(screen.getByText(baseline.id)).toBeTruthy();
  expect(
    screen.queryByText("No active snapshot at this observation."),
  ).toBeNull();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  click("Refresh active snapshot");
  await screen.findByText("No active snapshot at this observation.");
  expect(screen.queryByText(baseline.id)).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});
it.each(["host", "tenant", "state", "ambiguous", "oversized"])(
  "rejects a mismatched %s active observation",
  async (failure) => {
    const w = screenWorkspace();
    const item = {
      ...baseline,
      ...(failure === "host"
        ? { host: "other.example.com" }
        : failure === "tenant"
          ? { tenantId: "other" }
          : failure === "state"
            ? { state: "retired" }
            : {}),
    };
    w.get.mockResolvedValue(
      etagReply({
        items: failure === "oversized" ? [item, item] : [item],
        ...(failure === "ambiguous" ? { nextCursor: "next" } : {}),
      }),
    );
    render(<ActiveSnapshot {...w.props} host={baseline.host} />);
    click("Read active snapshot");
    await screen.findByRole("alert");
    expect(screen.queryByText(baseline.id)).toBeNull();
    expect(
      screen.queryByText("No active snapshot at this observation."),
    ).toBeNull();
    expect(w.get).toHaveBeenCalledOnce();
  },
);
