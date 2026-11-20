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
import {
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
import { PersonalLibrary, SavePersonal } from "./Personal";
import type { components } from "../api/schema";

screenLifecycle("/unit/saved-items");
const listingId = "00000000-0000-7000-8000-000000000001";
const current = {
  releaseId: "00000000-0000-7000-8000-000000000002",
  revision: 3,
};
const item: components["schemas"]["PersonalItem"] = {
  type: "follow",
  id: "00000000-0000-7000-8000-000000000003",
  listingId,
  label: "My follow",
  revision: 4,
  createdAt: stamp,
  updatedAt: stamp,
};
const available = { status: "available", change: "changed", current };
it("checks only follows on the loaded page and clears all old target facts on a failed refresh", async () => {
  const w = screenWorkspace();
  const second = {
    ...item,
    id: "00000000-0000-7000-8000-000000000004",
    label: "Another follow",
  };
  const search = {
    ...item,
    id: "00000000-0000-7000-8000-000000000005",
    type: "search",
    listingId: undefined,
    label: "Search",
    search: { text: "hello" },
  };
  w.get.mockResolvedValue(etagReply({ items: [item, search, second] }));
  w.post
    .mockResolvedValueOnce(etagReply(available))
    .mockResolvedValueOnce(etagReply({ status: "unavailable" }));
  render(<PersonalLibrary {...w.props} />);
  const button = await screen.findByRole("button", {
    name: "Check followed Listings on this page",
  });
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(button);
  const region = screen.getByRole("region", { name: "Followed Listings" });
  await waitFor(() =>
    expect(
      within(region).getByText(/Changed since acknowledgement/),
    ).not.toBeNull(),
  );
  expect(within(region).getByText(/Unavailable/)).not.toBeNull();
  expect(
    w.post.mock.calls.map(
      (c) =>
        (c[1] as { params: { path: { itemId: string } } }).params.path.itemId,
    ),
  ).toEqual([item.id, second.id]);
  expect(w.patch).not.toHaveBeenCalled();
  w.post.mockResolvedValueOnce(
    etagReply({
      status: "available",
      reference: {
        origin: "local",
        releaseId: current.releaseId,
        versionRevisionId: listingId,
      },
    }),
  );
  fireEvent.click(button);
  expect(await within(region).findByRole("alert")).not.toBeNull();
  expect(
    within(region).queryByText(/Changed since acknowledgement/),
  ).toBeNull();
  expect(w.props.onFailure).toHaveBeenCalledOnce();
});

it("aborts an in-flight page check when the owner view closes", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [item] }));
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.post.mockReturnValue(pending.promise);
  const view = render(<PersonalLibrary {...w.props} />);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Check followed Listings on this page",
    }),
  );
  const signal = (w.post.mock.calls[0]![1] as { signal: AbortSignal }).signal;
  view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve(etagReply(available)));
  expect(w.props.onFailure).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
});

it("acknowledges the inspected release with the current owner validator only after confirmation", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(item, '"rev-4"'));
  w.post.mockResolvedValue(etagReply(available));
  w.patch.mockResolvedValue(etagReply({ id: item.id, revision: 5 }, '"rev-5"'));
  render(<PersonalLibrary {...w.props} id={item.id} />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Check current release" }),
  );
  expect(
    (
      await screen.findByRole("link", { name: "Open current release" })
    ).getAttribute("href"),
  ).toBe(w.props.runtime.basePath + "/catalog/" + current.releaseId);
  expect(w.patch).not.toHaveBeenCalled();
  await confirmCommand("Acknowledge current release");
  const options = w.patch.mock.calls[0]![1] as {
    params: { header: Record<string, string> };
  };
  const key = options.params.header["Idempotency-Key"];
  expect(key).toMatch(/.+/);
  expect(w.patch).toHaveBeenCalledWith(
    "/api/v1/personal/items/{itemId}",
    expect.objectContaining({
      params: {
        path: { itemId: item.id },
        header: {
          "If-Match": '"rev-4"',
          "Idempotency-Key": key,
        },
      },
      body: { label: item.label, type: "follow", listingId, observed: current },
    }),
  );
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  expect(screen.queryByText("Changed since acknowledgement")).toBeNull();
  w.post.mockResolvedValueOnce(etagReply({ status: "unavailable" }));
  fireEvent.click(
    await screen.findByRole("button", { name: "Check current release" }),
  );
  expect(
    await screen.findByText("This followed Listing is unavailable."),
  ).not.toBeNull();
  expect(
    screen.queryByRole("button", { name: "Acknowledge current release" }),
  ).toBeNull();
  expect(
    screen.getByRole("button", { name: "Unfollow Listing" }),
  ).not.toBeNull();
});

it("keeps a duplicate follow receipt and links to its existing preference", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply({ id: item.id, revision: 4 }, '"rev-4"'));
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{
        type: "follow",
        listingId,
        label: "New proposed label",
        observed: current,
      }}
    />,
  );
  await confirmCommand("Follow this Listing");
  expect(
    await screen.findByText("Already following this Listing."),
  ).not.toBeNull();
  expect(
    screen.getByRole("link", { name: "Open saved item" }).getAttribute("href"),
  ).toBe("/unit/saved-items/" + item.id);
  expect(w.patch).not.toHaveBeenCalled();
});
