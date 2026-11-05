// @vitest-environment jsdom
import { expect, it } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import Reviews from "./Reviews";
import { APIError } from "../api/client";
import { deferred } from "../../tests/unit/fixtures";
import { submission } from "../../tests/unit/responses";
import {
  administrator,
  change,
  click,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/reviews");
const listingId = "00000000-0000-7000-8000-000000000001";
const otherId = "00000000-0000-7000-8000-000000000002";
async function ready() {
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Apply review filters" }),
    ).toHaveProperty("disabled", false),
  );
}

it("stages exact filters, pages applied criteria and refreshes without submitting the draft", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({ items: [submission], nextCursor: "next" }),
  );
  render(<Reviews {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: submission.id });
  await ready();
  change("Exact Listing ID", listingId);
  change("Review state", "approved");
  expect(w.get).toHaveBeenCalledOnce();
  expect(location.search).toBe("");
  click("Next page");
  await ready();
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { state: "submitted", cursor: "next", limit: 25 } },
  });
  click("Apply review filters");
  await ready();
  expect(location.search).toBe(`?state=approved&listing=${listingId}`);
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { listingId, state: "approved", limit: 25 } },
  });
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(
    screen.getByRole("group", { name: "Review submissions pagination" }),
  ).toBeTruthy();
  change("Exact Listing ID", otherId);
  change("Review state", "rejected");
  click("Next page");
  await ready();
  click("Refresh");
  await ready();
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: { listingId, state: "approved", limit: 25, cursor: "next" },
    },
  });
  expect(screen.getByLabelText("Exact Listing ID")).toHaveProperty(
    "value",
    otherId,
  );
  expect(screen.getByLabelText("Review state")).toHaveProperty(
    "value",
    "rejected",
  );
  const historyLength = history.length;
  change("Exact Listing ID", listingId);
  change("Review state", "approved");
  click("Apply review filters");
  await ready();
  expect(history.length).toBe(historyLength);
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(w.get).toHaveBeenCalledTimes(6);
  expect(w.post).not.toHaveBeenCalled();
});

it("clears to all states and restores applied URL criteria on Back", async () => {
  history.replaceState(
    null,
    "",
    `/unit/reviews?listing=${listingId}&state=approved`,
  );
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(<Reviews {...w.props} />);
  await screen.findByText("No matching review submissions.");
  await ready();
  click("Clear review filters");
  await ready();
  expect(location.search).toBe("?state=");
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { limit: 25 } },
  });
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.listingId",
  );
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.state");
  await act(async () => {
    history.back();
  });
  await waitFor(() =>
    expect(screen.getByLabelText("Exact Listing ID")).toHaveProperty(
      "value",
      listingId,
    ),
  );
  await ready();
  expect(screen.getByLabelText("Review state")).toHaveProperty(
    "value",
    "approved",
  );
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { listingId, state: "approved", limit: 25 } },
  });
  const reads = w.get.mock.calls.length;
  await act(async () => {
    history.replaceState(null, "", "/unit/resources?state=rejected");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(w.get).toHaveBeenCalledTimes(reads);
});

it("discards an obsolete continuation after URL navigation resets the queue", async () => {
  const w = screenWorkspace();
  const pending = deferred<unknown>();
  w.get
    .mockResolvedValueOnce(
      etagReply({ items: [submission], nextCursor: "next" }),
    )
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValue(etagReply({ items: [] }));
  render(<Reviews {...w.props} />);
  await screen.findByRole("link", { name: submission.id });
  await ready();
  click("Next page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  const options = w.get.mock.calls[1]?.[1] as { signal: AbortSignal };
  await act(async () => {
    history.replaceState(
      null,
      "",
      `/unit/reviews?state=approved&listing=${listingId}`,
    );
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByText("No matching review submissions.");
  expect(options.signal.aborted).toBe(true);
  await act(async () => {
    pending.resolve(etagReply({ items: [submission] }));
  });
  expect(screen.queryByRole("link", { name: submission.id })).toBeNull();
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(w.get).toHaveBeenCalledTimes(3);
});

it.each(["APPROVED", "__proto__", "constructor"])(
  "keeps unknown URL state %s unsupported and recovers through an explicit valid Apply",
  async (state) => {
    history.replaceState(null, "", `/unit/reviews?state=${state}`);
    const w = screenWorkspace();
    w.get
      .mockRejectedValueOnce(new APIError(400, "INVALID_ARGUMENT"))
      .mockResolvedValue(etagReply({ items: [] }));
    render(<Reviews {...w.props} />);
    await screen.findByRole("alert");
    expect(
      screen.getByRole("option", { name: "Unsupported review state" }),
    ).toHaveProperty("disabled", true);
    change("Review state", "approved");
    click("Apply review filters");
    await screen.findByText("No matching review submissions.");
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(location.search).toBe("?state=approved");
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("prepares a native exact-submission navigation without reading a private record or accepting a path", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(<Reviews {...w.props} />);
  await screen.findByText("No matching review submissions.");
  const form = screen.getByRole("form", {
    name: "Open exact review submission",
  }) as HTMLFormElement;
  expect(
    screen.getByRole("button", { name: "Open submission" }),
  ).toHaveProperty("disabled", true);
  change("Exact Submission ID", "../outside");
  expect(form.checkValidity()).toBe(false);
  expect(form.getAttribute("action")).toBe("/unit/reviews/..%2Foutside");
  change("Exact Submission ID", otherId);
  expect(form.checkValidity()).toBe(true);
  expect(form.getAttribute("action")).toBe(`/unit/reviews/${otherId}`);
  expect(form.method).toBe("get");
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
});

it("preserves duplicate submitted names and independent missing projections without hydrating rows", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({
      items: [
        { ...submission, id: "first", submittedDisplayName: "Same name" },
        {
          ...submission,
          id: "second",
          submittedDisplayName: "Same name",
          canonicalIdentifier: "urn:exact",
        },
      ],
    }),
  );
  render(<Reviews {...w.props} />);
  await screen.findByRole("link", { name: "first" });
  expect(screen.getAllByRole("cell", { name: "Same name" })).toHaveLength(2);
  expect(
    screen.getByRole("cell", { name: "Identifier unavailable" }),
  ).toBeTruthy();
  expect(screen.getByRole("cell", { name: "urn:exact" })).toBeTruthy();
  expect(w.get).toHaveBeenCalledOnce();
});
