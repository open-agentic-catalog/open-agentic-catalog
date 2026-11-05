// @vitest-environment jsdom
// Isolated screen tests use injected transport; no application process or database.
import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import Ratings, { OwnRatings } from "./Ratings";
import Feedback, { OwnFeedback } from "./Feedback";
import { revealEditor } from "../../tests/unit/reveal-editor";
import { APIError } from "../api/client";
import { CaseLookup, useCaseNavigation } from "./case-navigation";
import { deferred, sessionAccess } from "../../tests/unit/fixtures";
import {
  administrator,
  change as baseChange,
  click as baseClick,
  confirmCommand,
  etagReply,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
  resource,
} from "../../tests/unit/screens";

function click(name: string | RegExp) {
  const target = screen.getAllByText(name, { selector: "button" })[0];
  if (target) revealEditor(target);
  baseClick(name);
}

function change(label: string, value: string) {
  if (label === "Moderation reason" && !screen.queryByLabelText(label))
    click("Remove rating");
  const control = screen.getByLabelText(label);
  revealEditor(control);
  baseChange(label, value);
}

screenLifecycle("/unit/ratings");
const rating = {
  id: "rating-1",
  tenantId: "tenant-a",
  target: {
    origin: "foreign",
    sourceId: "source-1",
    presentationGeneration: 2,
    identifier: "urn:air:example.com:agents:helper",
    kind: "agent",
    versionPresent: false,
  },
  state: "active",
  score: 4,
  consent: false,
  revision: 2,
  createdAt: stamp,
  updatedAt: stamp,
};
const feedback = {
  id: "feedback-1",
  membershipId: "member-a",
  resourceId: "resource-1",
  category: "problem",
  state: "open",
  comment: "Private report",
  revision: 2,
  createdAt: stamp,
  updatedAt: stamp,
};

it("offers same-page rating refresh after success without clearing the moderation reason", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/ratings/inbox": { items: [rating] } });
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("button", { name: "Remove rating" });
  click("Remove rating");
  fireEvent.change(screen.getByLabelText("Moderation reason"), {
    target: { value: "Retain this reason" },
  });
  click("Refresh ratings");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Retain this reason",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps rating filters, current cursor, reason and receipt through read failure and rejected continuation", async () => {
  const w = screenWorkspace();
  let failure = false;
  w.get.mockImplementation(async () => {
    if (failure) throw new APIError(400, "INVALID_CURSOR");
    return etagReply({ items: [rating], nextCursor: "next-private" });
  });
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("button", { name: "Remove rating" });
  change("Rating state", "active");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  change("Moderation reason", "Keep reason");
  click("Next page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  failure = true;
  click("Refresh ratings");
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Keep reason",
  );
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { state: "active", cursor: "next-private" } },
  });
  failure = false;
  click("First page");
  await screen.findByRole("button", { name: "Remove rating" });
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { state: "active" } },
  });
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("recovers a rating conflict by exact read without resending the command or losing its reason", async () => {
  const w = screenWorkspace();
  let current = rating;
  w.get.mockImplementation(async () => etagReply(current));
  w.post.mockRejectedValue(new APIError(412, "PRECONDITION_FAILED"));
  render(<Ratings {...w.props} access={administrator} id={rating.id} />);
  await screen.findByRole("button", { name: "Remove rating" });
  change("Moderation reason", "Keep exact reason");
  await confirmCommand("Remove rating");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "The record changed",
  );
  click("Back to form");
  current = { ...rating, state: "removed", score: 0, revision: 3 };
  click("Read current rating");
  await screen.findByRole("button", { name: "Permit new submission" });
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Keep exact reason",
  );
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.get).toHaveBeenCalledTimes(2);
});

it("retains rating acknowledgement when current readback fails and retries only the read", async () => {
  const w = screenWorkspace();
  let failed = false;
  w.get.mockImplementation(async () => {
    if (failed) throw new APIError(503, "UNAVAILABLE");
    return etagReply(rating);
  });
  w.post.mockImplementation(async () => {
    failed = true;
    return etagReply({ id: rating.id, revision: 3 });
  });
  render(<Ratings {...w.props} access={administrator} id={rating.id} />);
  await screen.findByRole("button", { name: "Remove rating" });
  change("Moderation reason", "Confirmed case");
  await confirmCommand("Remove rating");
  await screen.findByRole("alert");
  expect(screen.getByText("Rating state updated.")).toBeTruthy();
  failed = false;
  click("Reload");
  await screen.findByRole("button", { name: "Remove rating" });
  expect(screen.getByText("Rating state updated.")).toBeTruthy();
  expect(w.post).toHaveBeenCalledTimes(1);
});

it.each(["rating", "feedback"] as const)(
  "opens an exact %s and rereads its captured page without storing private criteria in history",
  async (kind) => {
    const w = screenWorkspace();
    const isRating = kind === "rating";
    const row = isRating ? rating : feedback;
    const base = isRating ? "/api/v1/ratings" : "/api/v1/feedback";
    w.get.mockImplementation(async (path) =>
      etagReply(
        String(path).includes("{ratingId}") ||
          String(path).includes("{feedbackId}")
          ? row
          : { items: [row], nextCursor: "private-cursor" },
      ),
    );
    const minimal = {
      ...administrator,
      permissions: [
        isRating
          ? ("ratings.moderate" as const)
          : ("feedback.moderate" as const),
      ],
      availablePermissions: [
        isRating
          ? ("ratings.moderate" as const)
          : ("feedback.moderate" as const),
      ],
    };
    render(
      isRating ? (
        <Ratings {...w.props} access={minimal} />
      ) : (
        <Feedback {...w.props} access={minimal} />
      ),
    );
    await screen.findByRole("link", { name: `Open ${kind} case` });
    change(
      isRating ? "Rating state" : "Feedback state",
      isRating ? "active" : "open",
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
    click("Next page");
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
    fireEvent.click(screen.getByRole("link", { name: `Open ${kind} case` }));
    await screen.findByRole("button", { name: `Read current ${kind}` });
    expect(history.state).toEqual({ caseNavigation: 2 }); // Applied state, then exact case; no private cursor in history.
    expect(location.pathname).toBe(
      `/unit/${isRating ? "ratings" : "feedback"}/${row.id}`,
    );
    fireEvent.click(
      screen.getByRole("link", { name: `Return to ${kind} list` }),
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(5));
    expect(w.get.mock.calls.at(-1)).toMatchObject([
      `${base}/inbox`,
      {
        params: {
          query: {
            cursor: "private-cursor",
            state: isRating ? "active" : "open",
          },
        },
      },
    ]);
  },
);

it.each(["rating", "feedback"] as const)(
  "uses the explicit %s ID opener without scanning any pages",
  async (kind) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/ratings/inbox": { items: [] },
      "/api/v1/feedback/inbox": { items: [] },
      "/api/v1/resources": { items: [] },
      "/api/v1/ratings/{ratingId}": rating,
      "/api/v1/feedback/{feedbackId}": feedback,
    });
    render(
      kind === "rating" ? (
        <Ratings {...w.props} access={administrator} />
      ) : (
        <Feedback {...w.props} access={administrator} />
      ),
    );
    await screen.findByLabelText(`Exact ${kind} ID`);
    change(`Exact ${kind} ID`, kind === "rating" ? rating.id : feedback.id);
    fireEvent.submit(
      screen.getByLabelText(`Exact ${kind} ID`).closest("form")!,
    );
    await screen.findByRole("button", { name: `Read current ${kind}` });
    expect(
      w.get.mock.calls.filter(([path]) => String(path).endsWith("inbox")),
    ).toHaveLength(1);
  },
);

it("does not claim an unproven moderator case is owned, but permits withdrawal of an own-list case", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/ratings": { items: [rating] },
    "/api/v1/ratings/{ratingId}": rating,
  });
  render(<OwnRatings {...w.props} access={administrator} id={rating.id} />);
  await screen.findByText(/To confirm ownership/);
  expect(screen.queryByRole("button", { name: "Withdraw rating" })).toBeNull();
  fireEvent.click(screen.getByRole("link", { name: "Open rating list" }));
  await screen.findByRole("button", { name: "Withdraw rating" });
  fireEvent.click(screen.getByRole("link", { name: "Open rating case" }));
  await screen.findByRole("button", { name: "Read current rating" });
  expect(screen.getByRole("button", { name: "Withdraw rating" })).toBeTruthy();
});

it("admits retained owner case reads without resource permission and fences scope or identity changes", async () => {
  const w = screenWorkspace();
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValue(pending.promise);
  const access = {
    ...w.props.access,
    permissions: [],
    availablePermissions: [],
    scopeEnforcement: true,
    scopes: ["catalog.read" as const, "ratings.write" as const],
  };
  const view = render(
    <OwnRatings {...w.props} access={access} id={rating.id} />,
  );
  expect(
    screen.getByRole("button", { name: "Read current rating" }),
  ).toHaveProperty("disabled", true);
  view.rerender(
    <OwnRatings
      {...w.props}
      access={{ ...access, scopes: [] }}
      id={rating.id}
    />,
  );
  await act(async () => pending.resolve(etagReply(rating)));
  expect(screen.queryByRole("button", { name: "Copy rating ID" })).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps exact reads separate from moderation scopes and rejects a different feedback ID", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    scopeEnforcement: true,
    scopes: ["catalog.read" as const],
  };
  readReplies(w.get, { "/api/v1/ratings/{ratingId}": rating });
  const view = render(<Ratings {...w.props} access={access} id={rating.id} />);
  await screen.findByRole("button", { name: "Copy rating ID" });
  expect(screen.queryByRole("button", { name: "Remove rating" })).toBeNull();
  view.unmount();
  readReplies(w.get, {
    "/api/v1/feedback/{feedbackId}": { ...feedback, id: "wrong-case" },
  });
  render(<Feedback {...w.props} access={access} id={feedback.id} />);
  await screen.findByRole("alert");
  expect(screen.queryByText(feedback.comment)).toBeNull();
});

it("explains the case-read scope when a permitted inbox moderator lacks catalog.read", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    permissions: ["feedback.moderate" as const],
    availablePermissions: ["feedback.moderate" as const],
    scopeEnforcement: true,
    scopes: ["tenant.admin" as const],
  };
  readReplies(w.get, { "/api/v1/feedback/inbox": { items: [] } });
  const view = render(<Feedback {...w.props} access={access} />);
  await screen.findByRole("button", { name: "Refresh feedback" });
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(1));
  expect(screen.queryByRole("alert")).toBeNull();
  view.rerender(<Feedback {...w.props} access={access} id={feedback.id} />);
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Feedback case reads require feedback.moderate and catalog.read scope when enforced.",
  );
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  readReplies(w.get, { "/api/v1/feedback/{feedbackId}": feedback });
  view.rerender(
    <Feedback
      {...w.props}
      access={{ ...access, scopes: ["tenant.admin", "catalog.read"] }}
      id={feedback.id}
    />,
  );
  await screen.findByRole("button", { name: "Read current feedback" });
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  expect(w.get.mock.calls.at(-1)).toMatchObject([
    "/api/v1/feedback/{feedbackId}",
    { params: { path: { feedbackId: feedback.id } } },
  ]);
});

it("protects changed feedback before exact readback, then clears terminal text without a mutation", async () => {
  const w = screenWorkspace();
  let current: typeof feedback | Omit<typeof feedback, "comment"> = feedback;
  w.get.mockImplementation(async () => etagReply(current));
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(<Feedback {...w.props} access={administrator} id={feedback.id} />);
  await screen.findByText(feedback.comment, { selector: "p" });
  fireEvent.click(screen.getByText("Respond or manage feedback"));
  change("Moderator response (optional)", "Keep private draft");
  click("Read current feedback");
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "Keep private draft",
  );
  discard.mockReturnValue(true);
  current = { ...feedback, state: "removed", revision: 3 };
  delete (current as Partial<typeof feedback>).comment;
  click("Read current feedback");
  await screen.findByText("Comment and response have been cleared.");
  expect(screen.queryByText(feedback.comment)).toBeNull();
  expect(screen.queryByLabelText("Moderator response (optional)")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("shows a known resource name and exact filter together without coupling category/state to resource choices", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources": { items: [resource] },
    "/api/v1/feedback/inbox": { items: [feedback] },
  });
  render(<Feedback {...w.props} access={administrator} />);
  await screen.findByText(resource.canonicalIdentifier, { selector: "code" });
  expect(screen.getByLabelText("Resource filter")).toBeTruthy();
  change("Feedback category", "problem");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  change("Feedback state", "open");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(4));
  change(
    "Resource ID filter (optional)",
    "00000000-0000-7000-8000-000000000001",
  );
  click("Apply resource filter");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(5));
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: {
        resourceId: "00000000-0000-7000-8000-000000000001",
        category: "problem",
        state: "open",
      },
    },
  });
  expect(
    screen.getByRole("option", { name: "Selected resource (another page)" }),
  ).toBeTruthy();
});

it("requires a deliberate exact target read before offering namespace-scoped resource links", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    permissions: ["feedback.moderate" as const],
    namespaceGrants: [
      {
        namespaceId: resource.namespaceId,
        permissions: [
          "resource.read" as const,
          "resource.read_history" as const,
        ],
      },
    ],
  };
  readReplies(w.get, {
    "/api/v1/feedback/{feedbackId}": { ...feedback, releaseId: "release-1" },
    "/api/v1/resources/{resourceId}": resource,
  });
  render(<Feedback {...w.props} access={access} id={feedback.id} />);
  await screen.findByText(feedback.comment, { selector: "p" });
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(
    screen.queryByRole("link", { name: "Resource and versions" }),
  ).toBeNull();
  click("Read resource context");
  await screen.findByRole("link", { name: "Resource and versions" });
  expect(screen.getByRole("link", { name: "Original release" })).toBeTruthy();
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});

it("does not hydrate later-page namespace authority for every feedback row", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    tenantId: sessionAccess.tenantId,
    membershipId: sessionAccess.membershipId,
    realmId: sessionAccess.realmId,
    permissions: ["feedback.moderate" as const],
    namespaceGrants: [],
    nextCursor: "later-grants",
  };
  const selected = {
    ...resource,
    tenantId: access.tenantId,
    namespaceId: "00000000-0000-7000-8000-000000000088",
  };
  readReplies(w.get, {
    "/api/v1/resources": { items: [selected] },
    "/api/v1/feedback/inbox": { items: [feedback] },
    "/api/v1/resources/{resourceId}": selected,
    "/api/v1/me": {
      ...access,
      nextCursor: undefined,
      namespaceGrants: [
        { namespaceId: selected.namespaceId, permissions: ["resource.read"] },
      ],
    },
  });
  render(<Feedback {...w.props} access={access} />);
  await screen.findByText(resource.canonicalIdentifier, { selector: "code" });
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(
    screen.queryByRole("link", { name: "Resource and versions" }),
  ).toBeNull();
  click("Read resource context");
  await screen.findByRole("link", { name: "Resource and versions" });
  expect(w.get).toHaveBeenCalledTimes(4);
  expect(w.get.mock.calls.at(-1)).toMatchObject([
    "/api/v1/me",
    { params: { query: { namespaceId: selected.namespaceId } } },
  ]);
  expect(w.post).not.toHaveBeenCalled();
});

it("does not reveal another submitter's feedback through a personal case route", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/feedback/{feedbackId}": {
      ...feedback,
      membershipId: "other-member",
    },
  });
  render(<OwnFeedback {...w.props} access={administrator} id={feedback.id} />);
  await screen.findByRole("alert");
  expect(screen.queryByText(feedback.comment)).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps the captured case cursor when an unchanged Resource filter is applied again", async () => {
  const w = screenWorkspace();
  const resourceId = "00000000-0000-7000-8000-000000000088";
  const access = {
    ...administrator,
    permissions: ["feedback.moderate" as const],
    availablePermissions: ["feedback.moderate" as const],
  };
  readReplies(w.get, {
    "/api/v1/feedback/inbox": {
      items: [{ ...feedback, resourceId }],
      nextCursor: "second-page",
    },
    "/api/v1/feedback/{feedbackId}": { ...feedback, resourceId },
  });
  render(<Feedback {...w.props} access={access} />);
  await screen.findByText(feedback.comment, { selector: "p" });
  change("Resource ID filter (optional)", resourceId);
  click("Apply resource filter");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  click("Next page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  click("Apply resource filter");
  expect(w.get).toHaveBeenCalledTimes(3);
  fireEvent.click(screen.getByRole("link", { name: "Open feedback case" }));
  await screen.findByRole("button", { name: "Read current feedback" });
  fireEvent.click(
    screen.getByRole("link", { name: "Return to feedback list" }),
  );
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(5));
  expect(w.get.mock.calls.at(-1)).toMatchObject([
    "/api/v1/feedback/inbox",
    { params: { query: { resourceId, cursor: "second-page" } } },
  ]);
  expect(w.post).not.toHaveBeenCalled();
});

it("refuses dirty browser Back before another shell listener can replace the case", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/ratings/inbox": { items: [rating] },
    "/api/v1/ratings/{ratingId}": rating,
  });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  const go = vi.spyOn(history, "go").mockImplementation(() => {});
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Open rating case" });
  fireEvent.click(screen.getByRole("link", { name: "Open rating case" }));
  await screen.findByRole("button", { name: "Read current rating" });
  change("Moderation reason", "Keep Back draft");
  const shell = vi.fn();
  const moved = (event: PopStateEvent) => {
    if (
      window.dispatchEvent(
        new CustomEvent("catalog-ui:navigate", {
          detail: event.state as unknown,
          cancelable: true,
        }),
      )
    )
      shell();
  };
  window.addEventListener("popstate", moved);
  act(() => {
    history.replaceState({ caseNavigation: 0 }, "", "/unit/ratings");
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
  });
  expect(discard).toHaveBeenCalledTimes(1);
  expect(go).toHaveBeenCalledWith(1);
  expect(shell).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Keep Back draft",
  );
  discard.mockReturnValue(true);
  act(() => {
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: { caseNavigation: 0 } }),
    );
  });
  await screen.findByRole("link", { name: "Open rating case" });
  expect(shell).toHaveBeenCalledTimes(1);
  window.removeEventListener("popstate", moved);
});

it("bounds case history to an ordinal and ignores unowned or malformed history states", () => {
  const w = screenWorkspace();
  history.replaceState(
    { caseNavigation: 4, unrelated: "discard" },
    "",
    "/unit/ratings",
  );
  const { result, unmount } = renderHook(() =>
    useCaseNavigation(w.props, "ratings"),
  );
  expect(history.state).toEqual({ caseNavigation: 4 });
  const navigate = (state: unknown) =>
    act(() => {
      window.dispatchEvent(
        new CustomEvent("catalog-ui:navigate", {
          detail: state,
          cancelable: true,
        }),
      );
    });
  for (const state of [
    null,
    {},
    { caseNavigation: "4" },
    { caseNavigation: Infinity },
  ])
    navigate(state);
  expect(result.current.id).toBe("");
  act(() => result.current.move("exact-case"));
  expect(result.current.id).toBe("exact-case");
  expect(history.state).toEqual({ caseNavigation: 5 });
  history.replaceState({ caseNavigation: 4 }, "", "/unit/ratings");
  navigate({ caseNavigation: 4 });
  expect(result.current.id).toBe("");
  history.replaceState({ caseNavigation: 6 }, "", "/unit/other");
  navigate({ caseNavigation: 6 });
  expect(result.current.id).toBe("");
  unmount();
  expect(
    window.dispatchEvent(
      new CustomEvent("catalog-ui:navigate", {
        detail: { caseNavigation: 5 },
        cancelable: true,
      }),
    ),
  ).toBe(true);
});

it("keeps a changed moderation reason when deliberate detail navigation is declined", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/ratings/inbox": { items: [rating] } });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Open rating case" });
  change("Moderation reason", "Unsent reason");
  fireEvent.click(screen.getByRole("link", { name: "Open rating case" }));
  expect(discard).toHaveBeenCalledTimes(1);
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(location.pathname).toBe("/unit/ratings");
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Unsent reason",
  );
  click("Cancel moderation");
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Unsent reason",
  );
  discard.mockReturnValue(true);
  click("Cancel moderation");
  expect(screen.queryByLabelText("Moderation reason")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("preserves native modified-link behavior and does not open an empty exact reference", () => {
  const w = screenWorkspace();
  const { result } = renderHook(() => useCaseNavigation(w.props, "ratings"));
  for (const modifiers of [
    { button: 1 },
    { metaKey: true },
    { ctrlKey: true },
    { altKey: true },
    { shiftKey: true },
  ]) {
    const preventDefault = vi.fn();
    result.current.open(
      {
        ...modifiers,
        preventDefault,
      } as unknown as React.MouseEvent<HTMLAnchorElement>,
      rating.id,
    );
    expect(preventDefault).not.toHaveBeenCalled();
  }
  const open = vi.fn();
  render(<CaseLookup kind="rating" open={open} />);
  fireEvent.submit(screen.getByLabelText("Exact rating ID").closest("form")!);
  expect(open).not.toHaveBeenCalled();
  expect(w.get).not.toHaveBeenCalled();
});

it("reads an exact rating directly instead of scanning the inbox", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/ratings/{ratingId}": rating });
  render(<Ratings {...w.props} access={administrator} id={rating.id} />);
  await screen.findByRole("button", { name: "Copy rating ID" });
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.get.mock.calls[0]).toMatchObject([
    "/api/v1/ratings/{ratingId}",
    { params: { path: { ratingId: rating.id } } },
  ]);
});

it("reads an exact feedback case without its resource directory", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) => {
    expect(path).toBe("/api/v1/feedback/{feedbackId}");
    return etagReply(feedback);
  });
  render(
    <Feedback
      {...w.props}
      access={{
        ...administrator,
        permissions: ["feedback.moderate"],
        availablePermissions: ["feedback.moderate"],
      }}
      id={feedback.id}
    />,
  );
  await screen.findByText(feedback.comment, { selector: "p" });
  expect(
    screen.getByRole("button", { name: "Copy submitter membership ID" }),
  ).not.toBeNull();
  expect(w.get).toHaveBeenCalledTimes(1);
});

it.each([
  ["rating", "success"],
  ["rating", "failure"],
  ["feedback", "success"],
  ["feedback", "failure"],
] as const)(
  "fences an obsolete %s case's late %s after opening a different case",
  async (kind, outcome) => {
    const w = screenWorkspace();
    const pending = deferred<ReturnType<typeof etagReply>>();
    const firstId = "00000000-0000-7000-8000-000000000001";
    const secondId = "00000000-0000-7000-8000-000000000002";
    const first = { ...(kind === "rating" ? rating : feedback), id: firstId };
    const second = { ...first, id: secondId };
    const access = {
      ...administrator,
      permissions: [
        kind === "rating"
          ? ("ratings.moderate" as const)
          : ("feedback.moderate" as const),
      ],
      availablePermissions: [],
    };
    w.get
      .mockImplementationOnce(() => pending.promise)
      .mockResolvedValue(etagReply(second));
    const Screen = kind === "rating" ? Ratings : Feedback;
    const view = render(<Screen {...w.props} access={access} id={firstId} />);
    expect(w.get).toHaveBeenCalledTimes(1);
    view.rerender(<Screen {...w.props} access={access} id={secondId} />);
    await screen.findByText(secondId, { selector: "code" });
    expect(w.get.mock.calls).toMatchObject([
      [
        `/api/v1/${kind === "rating" ? "ratings/{ratingId}" : "feedback/{feedbackId}"}`,
        { params: { path: { [`${kind}Id`]: firstId } } },
      ],
      [
        `/api/v1/${kind === "rating" ? "ratings/{ratingId}" : "feedback/{feedbackId}"}`,
        { params: { path: { [`${kind}Id`]: secondId } } },
      ],
    ]);
    await act(async () => {
      if (outcome === "success") pending.resolve(etagReply(first));
      else pending.reject(new APIError(403, "FORBIDDEN"));
    });
    expect(screen.getByText(secondId, { selector: "code" })).toBeTruthy();
    expect(screen.queryByText(firstId, { selector: "code" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["rating", "feedback"] as const)(
  "discards the private %s return filters and cursor after an authorization revision changes",
  async (kind) => {
    const w = screenWorkspace();
    const isRating = kind === "rating";
    const row = isRating ? rating : feedback;
    const access = {
      ...administrator,
      permissions: [
        isRating
          ? ("ratings.moderate" as const)
          : ("feedback.moderate" as const),
      ],
      availablePermissions: [],
    };
    const base = `/api/v1/${isRating ? "ratings" : "feedback"}`;
    readReplies(w.get, {
      [`${base}/inbox`]: { items: [row], nextCursor: "private-case-page" },
      [`${base}/{${kind}Id}`]: row,
    });
    const Screen = isRating ? Ratings : Feedback;
    const view = render(<Screen {...w.props} access={access} />);
    await screen.findByRole("link", { name: `Open ${kind} case` });
    change(
      isRating ? "Rating state" : "Feedback state",
      isRating ? "active" : "open",
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
    click("Next page");
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
    fireEvent.click(screen.getByRole("link", { name: `Open ${kind} case` }));
    await screen.findByRole("link", { name: `Return to ${kind} list` });
    view.rerender(<Screen {...w.props} access={access} id={row.id} />);
    view.rerender(
      <Screen
        {...w.props}
        access={{
          ...access,
          authorizationRevision: access.authorizationRevision + 1,
        }}
        id={row.id}
      />,
    );
    await screen.findByRole("link", { name: `Open ${kind} list` });
    // A hint refresh invalidates return criteria without replacing the exact-case editor.
    expect(w.get).toHaveBeenCalledTimes(4);
    fireEvent.click(screen.getByRole("link", { name: `Open ${kind} list` }));
    await screen.findByRole("link", { name: `Open ${kind} case` });
    expect(
      screen.getByLabelText(isRating ? "Rating state" : "Feedback state"),
    ).toHaveProperty("value", "");
    expect(w.get.mock.calls.at(-1)).toMatchObject([
      `${base}/inbox`,
      { params: { query: { limit: isRating ? 25 : 20 } } },
    ]);
    expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    );
    expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.state",
    );
    expect(w.get).toHaveBeenCalledTimes(5);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("restarts feedback continuation after a policy refresh only after protecting the current response draft", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/feedback/inbox": {
      items: [feedback],
      nextCursor: "policy-cursor",
    },
  });
  const access = {
    ...administrator,
    permissions: ["feedback.moderate" as const],
    availablePermissions: [],
  };
  const view = render(<Feedback {...w.props} access={access} />);
  await screen.findByText(feedback.comment, { selector: "p" });
  const summary = screen.getByText("Respond or manage feedback", {
    selector: "summary",
  });
  revealEditor(summary);
  fireEvent.click(summary);
  change("Moderator response (optional)", "Protect this policy-bound draft");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  const reads = w.get.mock.calls.length;
  view.rerender(
    <Feedback
      {...w.props}
      access={{
        ...access,
        authorizationRevision: access.authorizationRevision + 1,
      }}
    />,
  );
  click("Next page");
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "Protect this policy-bound draft",
  );
  discard.mockReturnValue(true);
  click("Next page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(reads + 1));
  expect(w.get.mock.calls.at(-1)).toMatchObject([
    "/api/v1/feedback/inbox",
    { params: { query: { limit: 20 } } },
  ]);
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["first page", "continuation", "exact case"] as const)(
  "refreshes feedback from the %s after a policy hint changes without resending a mutation",
  async (variant) => {
    const w = screenWorkspace();
    const access = {
      ...administrator,
      permissions: ["feedback.moderate" as const],
      availablePermissions: [],
    };
    const path =
      variant === "exact case"
        ? "/api/v1/feedback/{feedbackId}"
        : "/api/v1/feedback/inbox";
    readReplies(w.get, {
      [path]:
        variant === "exact case"
          ? feedback
          : { items: [feedback], nextCursor: "previous-policy" },
    });
    const view = render(
      <Feedback
        {...w.props}
        access={access}
        id={variant === "exact case" ? feedback.id : ""}
      />,
    );
    await screen.findByText(feedback.comment, { selector: "p" });
    if (variant === "continuation") {
      click("Next page");
      await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
    }
    const summary = screen.getByText("Respond or manage feedback", {
      selector: "summary",
    });
    revealEditor(summary);
    fireEvent.click(summary);
    change("Moderator response (optional)", "Retain until explicit refresh");
    view.rerender(
      <Feedback
        {...w.props}
        access={{
          ...access,
          authorizationRevision: access.authorizationRevision + 1,
        }}
        id={variant === "exact case" ? feedback.id : ""}
      />,
    );
    const before = w.get.mock.calls.length;
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    click(
      variant === "exact case" ? "Read current feedback" : "Refresh feedback",
    );
    expect(w.get).toHaveBeenCalledTimes(before);
    expect(
      screen.getByLabelText("Moderator response (optional)"),
    ).toHaveProperty("value", "Retain until explicit refresh");
    confirm.mockReturnValue(true);
    click(
      variant === "exact case" ? "Read current feedback" : "Refresh feedback",
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(before + 1));
    expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    );
    expect(w.get.mock.calls.at(-1)?.[0]).toBe(path);
    expect(w.post).not.toHaveBeenCalled();
  },
);
