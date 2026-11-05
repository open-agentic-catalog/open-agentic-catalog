// @vitest-environment jsdom
import { expect, it } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Publication from "./Publication";
import { APIError } from "../api/client";
import { baseline, snapshot } from "../../tests/unit/publication-fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
import { required } from "../../tests/required";

screenLifecycle("/unit/publication?host=catalog.example.com");
async function ready() {
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Build candidate snapshot" }),
    ).toHaveProperty("disabled", false),
  );
}
async function open() {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/publication/hosts") return etagReply({ items: [] });
    expect(path).toBe("/api/v1/publication/snapshots");
    return etagReply({ items: [baseline], nextCursor: "next" });
  });
  render(<Publication {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Generation 1" });
  await ready();
  return w;
}
it("filters before paging, shows exact facts and keeps an uncertain command through inventory changes", async () => {
  const w = await open();
  fireEvent.click(screen.getByText("Withdraw public discovery"));
  change("Reason", "Preserved host withdrawal");
  w.post.mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
  await confirmCommand("Unpublish host");
  await screen.findByText(/The outcome is unknown/);
  const original = structuredClone(required(w.post.mock.calls[0]));
  const pager = screen.getByRole("group", {
    name: "Publication snapshots pagination",
  });
  fireEvent.click(within(pager).getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).toHaveProperty(
      "params.query.cursor",
      "next",
    ),
  );
  change("Snapshot state", "active");
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    ),
  );
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { host: baseline.host, state: "active", limit: 25 } },
  });
  expect(screen.getByText(/The outcome is unknown/)).toBeTruthy();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Preserved host withdrawal",
  );
  expect(screen.getByText(baseline.id)).toBeTruthy();
  expect(screen.getByText(baseline.createdAt)).toBeTruthy();
  expect(screen.getByText(baseline.activatedAt)).toBeTruthy();
  w.post.mockResolvedValue(
    etagReply({
      ...baseline,
      state: "retired",
      ard: { entries: [] },
      aiCatalog: { entries: [] },
      exclusions: [],
    }),
  );
  click("Retry same command");
  await screen.findByText("Host unpublished.", { exact: true });
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(w.post.mock.calls[1]).toEqual(original);
});
it("opens exact snapshot UUIDs natively and keeps the configured host in the query", async () => {
  const w = await open();
  const form = screen.getByRole("form", {
    name: "Open exact publication snapshot",
  }) as HTMLFormElement;
  change("Exact Snapshot ID", "../outside");
  expect(form.checkValidity()).toBe(false);
  change("Exact Snapshot ID", baseline.id.toUpperCase());
  expect(form.checkValidity()).toBe(true);
  expect(form.getAttribute("action")).toBe(`/unit/publication/${baseline.id}`);
  expect(form.method).toBe("get");
  expect(form.querySelector('input[name="host"]')).toHaveProperty(
    "value",
    baseline.host,
  );
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["host", "tenant", "state"])(
  "rejects a mismatched %s summary before showing inventory links",
  async (failure) => {
    const w = screenWorkspace();
    const item = {
      ...baseline,
      ...(failure === "host"
        ? { host: "other.example.com" }
        : failure === "tenant"
          ? { tenantId: "other" }
          : {}),
    };
    w.get.mockImplementation(async (path) =>
      etagReply(
        path === "/api/v1/publication/hosts"
          ? { items: [] }
          : { items: [item] },
      ),
    );
    render(<Publication {...w.props} access={administrator} />);
    if (failure === "state") {
      await screen.findByRole("link", { name: "Generation 1" });
      change("Snapshot state", "retired");
    }
    await screen.findByRole("alert");
    expect(screen.queryByRole("link", { name: "Generation 1" })).toBeNull();
    expect(
      screen.getByRole("button", { name: "Build candidate snapshot" }),
    ).toHaveProperty("disabled", true);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("reconciles commands after a successful explicit refresh following an initial inventory failure", async () => {
  const w = screenWorkspace();
  let fail = true;
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/publication/hosts") return etagReply({ items: [] });
    if (fail) throw new Error("Controlled inventory failure");
    return etagReply({ items: [baseline] });
  });
  render(<Publication {...w.props} access={administrator} />);
  await screen.findByRole("alert");
  expect(
    screen.getByRole("button", { name: "Build candidate snapshot" }),
  ).toHaveProperty("disabled", true);
  fail = false;
  click("Refresh snapshots");
  await screen.findByRole("link", { name: "Generation 1" });
  await ready();
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["other-tenant", undefined])(
  "rejects a selected snapshot with tenant %s before showing its contents or comparison, then reloads the valid record",
  async (tenantId) => {
    const w = screenWorkspace();
    let record: Record<string, unknown> = snapshot;
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/publication/hosts") return etagReply({ items: [] });
      expect(path).toBe("/api/v1/publication/snapshots/{snapshotId}");
      return etagReply(record);
    });
    const view = render(
      <Publication {...w.props} access={administrator} id={snapshot.id} />,
    );
    await screen.findByRole("region", { name: "Included discovery entries" });
    expect(
      screen.getByRole("region", { name: "Snapshot comparison" }),
    ).toBeTruthy();
    record = { ...baseline, tenantId };
    view.rerender(
      <Publication {...w.props} access={administrator} id={baseline.id} />,
    );
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("region", { name: "Included discovery entries" }),
    ).toBeNull();
    expect(
      screen.queryByRole("region", { name: "Snapshot comparison" }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
    record = baseline;
    click("Reload");
    await screen.findByRole("region", { name: "Included discovery entries" });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(
      screen.getByRole("region", { name: "Snapshot comparison" }),
    ).toBeTruthy();
    expect(
      w.get.mock.calls.filter(
        ([path]) => path === "/api/v1/publication/snapshots/{snapshotId}",
      ),
    ).toHaveLength(3);
    expect(w.post).not.toHaveBeenCalled();
  },
);
