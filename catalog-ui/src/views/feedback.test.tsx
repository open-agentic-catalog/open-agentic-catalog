import { revealEditor } from "../../tests/unit/reveal-editor";
import { testAccess } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
import { deferred } from "../../tests/unit/fixtures";
// @vitest-environment jsdom
// Feedback tests substitute only transport, preserving forms, permissions and confirmation.
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import Feedback, { FeedbackEntry, OwnFeedback } from "./Feedback";
import {
  administrator,
  change as baseChange,
  click as baseClick,
  confirmCommand as baseConfirmCommand,
  etagReply,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
function change(name: string, value: string) {
  revealEditor(screen.getByLabelText(name));
  baseChange(name, value);
}
function click(name: string | RegExp) {
  const target = screen.getAllByText(name, { selector: "button" })[0];
  if (target) revealEditor(target);
  baseClick(name);
}
async function confirmCommand(name: string) {
  revealEditor(screen.getByText(name, { selector: "button" }));
  await baseConfirmCommand(name);
}
function openEditor(name: string) {
  const summary = screen.getByText(name, { selector: "summary" });
  revealEditor(summary);
  fireEvent.click(summary);
}

screenLifecycle("/unit/feedback");
const item = {
  id: "feedback-1",
  tenantId: "tenant-a",
  membershipId: "member-a",
  resourceId: "resource-1",
  releaseId: "release-1",
  versionRevisionId: "revision-1",
  category: "problem",
  comment: "<script>private comment</script>",
  response: "Current response",
  state: "addressed",
  revision: 2,
  createdAt: stamp,
  updatedAt: stamp,
};

/** Open the lazy private panel through its real details disclosure. */
function openFeedback() {
  fireEvent.click(screen.getByText("Item feedback"));
}

it("keeps a submission draft when disclosure, refresh, filters or navigation discard is declined", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/feedback": { items: [], nextCursor: "next" } });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <FeedbackEntry {...w.props} namespaceId="ns-1" resourceId="resource-1" />,
  );
  openFeedback();
  await screen.findByText("No feedback matches these filters.");
  change("Category", "documentation");
  change("Feedback comment", "Keep my private draft");
  const initialReads = w.get.mock.calls.length;
  for (const action of [
    () => openFeedback(),
    () => click("Refresh feedback"),
    () => change("Feedback category", "positive"),
    () => change("Feedback state", "addressed"),
    () => {
      const next = screen.getByRole("button", { name: "Next page" });
      next.focus();
      fireEvent.click(next);
      expect(document.activeElement).toBe(next);
    },
  ]) {
    action();
    expect(screen.getByLabelText("Feedback comment")).toHaveProperty(
      "value",
      "Keep my private draft",
    );
    expect(screen.getByLabelText("Category")).toHaveProperty(
      "value",
      "documentation",
    );
  }
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(false);
  expect(w.get).toHaveBeenCalledTimes(initialReads);
  expect(w.post).not.toHaveBeenCalled();
  expect(discard).toHaveBeenCalledTimes(5);
  discard.mockReturnValue(true);
  openFeedback();
  await waitFor(() =>
    expect(screen.queryByLabelText("Feedback comment")).toBeNull(),
  );
  openFeedback();
  expect(await screen.findByLabelText("Feedback comment")).toHaveProperty(
    "value",
    "",
  );
});

it("preserves unrelated row and new-submission drafts after one row succeeds, and clears only a deliberately closed editor", async () => {
  const w = screenWorkspace();
  const second = {
    ...item,
    id: "feedback-2",
    comment: "Second private submission",
  };
  const routes = { "/api/v1/feedback": { items: [item, second] } };
  readReplies(w.get, routes);
  w.post.mockImplementation(async () => {
    routes["/api/v1/feedback"].items = [
      { ...item, response: "Saved first response", revision: 3 },
      second,
    ];
    return etagReply({ id: item.id, revision: 3 });
  });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <FeedbackEntry
      {...w.props}
      access={administrator}
      namespaceId="ns-1"
      resourceId="resource-1"
    />,
  );
  openFeedback();
  await screen.findByText(second.comment, { selector: "p" });
  const firstRow = screen
    .getByText(item.comment, { selector: "p" })
    .closest("article")!;
  const secondRow = screen
    .getByText(second.comment, { selector: "p" })
    .closest("article")!;
  for (const [row, response] of [
    [firstRow, "Saved first response"],
    [secondRow, "Unsent second response"],
  ] as const) {
    fireEvent.click(within(row).getByText("Respond or manage feedback"));
    fireEvent.change(
      within(row).getByLabelText("Moderator response (optional)"),
      { target: { value: response } },
    );
  }
  fireEvent.click(
    within(secondRow).getByText("Edit or withdraw your feedback"),
  );
  fireEvent.change(within(secondRow).getByLabelText("Feedback comment"), {
    target: { value: "Unsent second comment" },
  });
  fireEvent.change(screen.getAllByLabelText("Feedback comment")[0]!, {
    target: { value: "Unsent new submission" },
  });
  fireEvent.click(
    within(firstRow).getByRole("button", { name: "Address feedback" }),
  );
  fireEvent.click(
    within(await screen.findByRole("dialog")).getByRole("button", {
      name: "Confirm",
    }),
  );
  await screen.findByText("Saved first response", { selector: "p" });
  expect(
    within(secondRow).getByLabelText("Moderator response (optional)"),
  ).toHaveProperty("value", "Unsent second response");
  expect(within(secondRow).getByLabelText("Feedback comment")).toHaveProperty(
    "value",
    "Unsent second comment",
  );
  expect(screen.getAllByLabelText("Feedback comment")[0]).toHaveProperty(
    "value",
    "Unsent new submission",
  );
  expect(secondRow.isConnected).toBe(true);
  expect(
    within(secondRow)
      .getByText("Respond or manage feedback")
      .closest("details"),
  ).toHaveProperty("open", true);
  const initialReads = w.get.mock.calls.length;
  fireEvent.click(
    within(secondRow).getByText("Edit or withdraw your feedback"),
  );
  expect(
    within(secondRow)
      .getByText("Edit or withdraw your feedback")
      .closest("details"),
  ).toHaveProperty("open", true);
  expect(within(secondRow).getByLabelText("Feedback comment")).toHaveProperty(
    "value",
    "Unsent second comment",
  );
  expect(w.get).toHaveBeenCalledTimes(initialReads);
  expect(w.post).toHaveBeenCalledTimes(1);
  fireEvent.click(within(secondRow).getByText("Respond or manage feedback"));
  expect(
    within(secondRow).getByLabelText("Moderator response (optional)"),
  ).toHaveProperty("value", "Unsent second response");
  expect(
    within(secondRow)
      .getByText("Respond or manage feedback")
      .closest("details"),
  ).toHaveProperty("open", true);
  expect(w.get).toHaveBeenCalledTimes(initialReads);
  expect(w.post).toHaveBeenCalledTimes(1);
  discard.mockReturnValue(true);
  fireEvent.click(within(secondRow).getByText("Respond or manage feedback"));
  expect(
    within(secondRow).getByLabelText("Moderator response (optional)"),
  ).toHaveProperty("value", "Current response");
  expect(within(secondRow).getByLabelText("Feedback comment")).toHaveProperty(
    "value",
    "Unsent second comment",
  );
  fireEvent.click(
    within(secondRow).getByText("Edit or withdraw your feedback"),
  );
  expect(within(secondRow).getByLabelText("Feedback comment")).toHaveProperty(
    "value",
    second.comment,
  );
  expect(screen.getAllByLabelText("Feedback comment")[0]).toHaveProperty(
    "value",
    "Unsent new submission",
  );
  expect(w.post).toHaveBeenCalledTimes(1);
});

it("protects moderator drafts before changing the exact resource filter", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/feedback/inbox": { items: [item] } });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <Feedback
      {...w.props}
      access={{
        ...administrator,
        permissions: ["feedback.moderate"],
        availablePermissions: ["feedback.moderate"],
      }}
    />,
  );
  await screen.findByText(item.comment, { selector: "p" });
  openEditor("Respond or manage feedback");
  change("Moderator response (optional)", "Unsent moderation");
  change(
    "Resource ID filter (optional)",
    "00000000-0000-0000-0000-000000000001",
  );
  const reads = w.get.mock.calls.length;
  click("Apply resource filter");
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "Unsent moderation",
  );
  discard.mockReturnValue(true);
  click("Apply resource filter");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(reads + 1));
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/feedback/inbox",
    expect.objectContaining({
      params: {
        query: {
          resourceId: "00000000-0000-0000-0000-000000000001",
          limit: 20,
        },
      },
    }),
  );
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "Current response",
  );
  discard.mockReturnValue(false);
  change("Moderator response (optional)", "Second moderation draft");
  click("All resources");
  expect(w.get).toHaveBeenCalledTimes(reads + 1);
  expect(screen.getByLabelText("Resource ID filter (optional)")).toHaveProperty(
    "value",
    "00000000-0000-0000-0000-000000000001",
  );
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "Second moderation draft",
  );
  discard.mockReturnValue(true);
  click("All resources");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(reads + 2));
  expect(screen.getByLabelText("Resource ID filter (optional)")).toHaveProperty(
    "value",
    "",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("shows general resource feedback without inventing release or revision context", async () => {
  const { props, get, post, patch } = screenWorkspace();
  readReplies(get, {
    "/api/v1/feedback": {
      items: [{ ...item, releaseId: undefined, versionRevisionId: undefined }],
    },
  });
  render(
    <FeedbackEntry namespaceId="ns-1" {...props} resourceId="resource-1" />,
  );
  openFeedback();
  expect(await screen.findByText("General resource feedback")).toBeTruthy();
  expect(screen.queryByText(/Exact version revision:/)).toBeNull();
  expect(screen.queryByRole("link", { name: "Original release" })).toBeNull();
  expect(
    screen
      .getByRole("link", { name: "Resource and versions" })
      .getAttribute("href"),
  ).toBe("/unit/resources/resource-1");
  expect(
    screen.getByText(
      "New feedback is about the resource generally, not a specific version.",
    ),
  ).toBeTruthy();
  expect(screen.getByText(/Do not include credentials/)).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  openEditor("Edit or withdraw your feedback");
  const form = screen
    .getByRole("button", { name: "Save feedback changes" })
    .closest("form")!;
  fireEvent.change(within(form).getByLabelText("Feedback comment"), {
    target: { value: "Corrected general resource feedback" },
  });
  click("Save feedback changes");
  const dialog = await screen.findByRole("dialog");
  const summary = within(dialog).getByLabelText("Command summary").textContent;
  expect(summary).toContain("General resource feedback");
  expect(summary).not.toContain("release-1");
  expect(summary).not.toContain("revision-1");
  expect(patch).not.toHaveBeenCalled();
  click("Cancel");
  const currentForm = screen
    .getByRole("button", { name: "Save feedback changes" })
    .closest("form")!;
  expect(within(currentForm).getByLabelText("Feedback comment")).toHaveProperty(
    "value",
    "Corrected general resource feedback",
  );
  expect(get).toHaveBeenCalledOnce();
  expect(post).not.toHaveBeenCalled();
  expect(patch).not.toHaveBeenCalled();
});

it("shows exact version guidance beside an eligible submission form", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/feedback": { items: [] } });
  render(
    <FeedbackEntry
      {...w.props}
      namespaceId="ns-1"
      resourceId="resource-1"
      versionRevisionId="revision-1"
    />,
  );
  openFeedback();
  await screen.findByRole("button", { name: "Submit feedback" });
  expect(
    screen.getByText("New feedback refers to this exact version revision."),
  ).toBeTruthy();
  expect(
    screen.queryByText(/New feedback refers to this exact release/),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("does not fetch private content before opening and respects human/scope eligibility", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, { "/api/v1/feedback": { items: [] } });
  const view = render(
    <FeedbackEntry namespaceId="ns-1" {...props} resourceId="resource-1" />,
  );
  expect(get).not.toHaveBeenCalled();
  openFeedback();
  expect(
    await screen.findByText("No feedback matches these filters."),
  ).toBeTruthy();
  view.rerender(
    <FeedbackEntry
      namespaceId="ns-1"
      {...props}
      access={{ ...props.access, actorType: "service" }}
      resourceId="resource-1"
    />,
  );
  expect(screen.queryByText("Item feedback")).toBeNull();
  view.rerender(
    <FeedbackEntry
      namespaceId="ns-1"
      {...props}
      access={{ ...props.access, ...testAccess([]) }}
      resourceId="resource-1"
    />,
  );
  expect(screen.queryByText("Item feedback")).toBeNull();
  view.rerender(
    <FeedbackEntry
      namespaceId="ns-1"
      {...props}
      access={{
        ...props.access,
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      }}
      resourceId="resource-1"
      versionRevisionId="revision-1"
    />,
  );
  openFeedback();
  expect(
    await screen.findByText(/To submit feedback, you need access/),
  ).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Submit feedback" })).toBeNull();
  expect(screen.queryByText(/New feedback refers/)).toBeNull();
  expect(screen.getByText(/Do not include credentials/)).toBeTruthy();
  const reads = get.mock.calls.length;
  fireEvent.click(screen.getByText("Feedback access"));
  expect(
    screen
      .getByText(/Sending or editing feedback also requires/)
      .closest("details"),
  ).toHaveProperty("open", true);
  expect(get).toHaveBeenCalledTimes(reads);

  const namespace = deferred<ReturnType<typeof etagReply>>();
  get.mockImplementation(async (path) => {
    if (path === "/api/v1/feedback") return etagReply({ items: [] });
    expect(path).toBe("/api/v1/me");
    return namespace.promise;
  });
  view.unmount();
  render(
    <FeedbackEntry
      namespaceId="ns-1"
      {...props}
      access={{ ...props.access, nextCursor: "more-grants" }}
      resourceId="resource-1"
    />,
  );
  openFeedback();
  await waitFor(() => expect(get).toHaveBeenCalledTimes(reads + 2));
  expect(screen.getByText(/To submit feedback, you need access/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Submit feedback" })).toBeNull();
  namespace.reject(new Error("Controlled namespace lookup failure"));
  await screen.findByRole("alert");
  expect(screen.getByText(/To submit feedback, you need access/)).toBeTruthy();
  expect(screen.queryByText(/do not currently have access/)).toBeNull();
  expect(screen.queryByRole("button", { name: "Submit feedback" })).toBeNull();
});

it("submits contextual feedback without accepting a supplied identity and refreshes current state", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/feedback": { items: [] } });
  post.mockResolvedValue(etagReply({ id: "feedback-1", revision: 1 }));
  render(
    <FeedbackEntry
      namespaceId="ns-1"
      {...props}
      resourceId="resource-1"
      releaseId="release-1"
      versionRevisionId="revision-1"
    />,
  );
  openFeedback();
  await screen.findByText("No feedback matches these filters.");
  expect(
    screen.getByText("New feedback refers to this exact release."),
  ).toBeTruthy();
  expect(
    screen
      .getByRole("button", { name: "Submit feedback" })
      .hasAttribute("disabled"),
  ).toBe(true);
  change("Category", "positive");
  change("Feedback comment", "x".repeat(4001));
  expect(
    screen
      .getByRole("button", { name: "Submit feedback" })
      .hasAttribute("disabled"),
  ).toBe(true);
  change("Feedback comment", "\u{1f600}".repeat(4000));
  expect(
    screen
      .getByRole("button", { name: "Submit feedback" })
      .hasAttribute("disabled"),
  ).toBe(false);
  change("Feedback comment", "Helpful documentation");
  await confirmCommand("Submit feedback");
  expect(post).toHaveBeenCalledWith(
    "/api/v1/feedback",
    expect.objectContaining({
      body: {
        resourceId: "resource-1",
        releaseId: "release-1",
        versionRevisionId: "revision-1",
        category: "positive",
        comment: "Helpful documentation",
      },
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
    }),
  );
  expect(await screen.findByText("Feedback submitted.")).toBeTruthy();
  change("Feedback category", "positive");
  change("Feedback state", "addressed");
  click("Refresh feedback");
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      "/api/v1/feedback",
      expect.objectContaining({
        params: {
          query: expect.objectContaining({
            category: "positive",
            state: "addressed",
          }) as unknown,
        },
      }),
    ),
  );
});

it("edits and withdraws own content with the feedback ETag, clears text and retains no markup", async () => {
  const { props, get, post, patch } = screenWorkspace();
  const routes = { "/api/v1/feedback": { items: [item] } };
  readReplies(get, routes);
  patch.mockImplementation(async () => {
    routes["/api/v1/feedback"].items = [
      {
        ...item,
        comment: "Edited private comment",
        response: "",
        state: "open",
        revision: 3,
      },
    ];
    return etagReply({ id: item.id, revision: 3 });
  });
  post.mockImplementation(async () => {
    routes["/api/v1/feedback"].items = [
      { ...item, state: "withdrawn", comment: "", response: "", revision: 4 },
    ];
    return etagReply({ id: item.id, revision: 4 });
  });
  render(
    <FeedbackEntry namespaceId="ns-1" {...props} resourceId="resource-1" />,
  );
  openFeedback();
  await screen.findByText(item.comment, { selector: "p" });
  expect(
    screen.getByText(item.comment, { selector: "p" }).querySelector("script"),
  ).toBeNull();
  openEditor("Edit or withdraw your feedback");
  const editor = screen
    .getByText("Edit or withdraw your feedback")
    .closest("details")!;
  fireEvent.change(within(editor).getByLabelText("Feedback comment"), {
    target: { value: "Edited private comment" },
  });
  await confirmCommand("Save feedback changes");
  expect(patch).toHaveBeenCalledWith(
    "/api/v1/feedback/{feedbackId}",
    expect.objectContaining({
      params: {
        path: { feedbackId: item.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"rev-2"',
        },
      },
    }),
  );
  await screen.findByText("Edited private comment", { selector: "p" });
  openEditor("Edit or withdraw your feedback");
  click("Withdraw feedback");
  const confirmation = within(await screen.findByRole("dialog"));
  expect(
    confirmation.getByText(/This feedback cannot be reopened/),
  ).toBeTruthy();
  expect(
    confirmation.getByText(/Its identifying context and audit records remain/),
  ).toBeTruthy();
  expect(confirmation.queryByText("Edited private comment")).toBeNull();
  expect(post).not.toHaveBeenCalled();
  click("Confirm");
  expect(
    await screen.findByText("Comment and response have been cleared."),
  ).toBeTruthy();
  expect(screen.queryByText(item.comment)).toBeNull();
});

it("offers an administrator inbox, paged resource choices, address/reopen/remove and no submitter impersonation", async () => {
  const { props, get, post } = screenWorkspace();
  const routes = {
    "/api/v1/resources": {
      items: [
        { ...resource, id: "resource-1", canonicalIdentifier: "Resource one" },
      ],
      nextCursor: "resources-next",
    },
    "/api/v1/feedback/inbox": {
      items: [{ ...item, membershipId: "someone-else" }],
      nextCursor: "feedback-next",
    },
  };
  readReplies(get, routes);
  post.mockImplementation(async (path) => {
    const action = String(path).split(":")[1];
    routes["/api/v1/feedback/inbox"].items = [
      {
        ...item,
        membershipId: "someone-else",
        state:
          action === "remove"
            ? "removed"
            : action === "reopen"
              ? "open"
              : "addressed",
        response: action === "address" ? "New response" : "",
        comment: action === "remove" ? "" : item.comment,
        revision: routes["/api/v1/feedback/inbox"].items[0]!.revision + 1,
      },
    ];
    return etagReply({
      id: item.id,
      revision: routes["/api/v1/feedback/inbox"].items[0]!.revision,
    });
  });
  const view = render(<Feedback {...props} />);
  expect(screen.getByRole("alert")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
  view.rerender(<Feedback {...props} access={administrator} />);
  await screen.findByText(item.comment, { selector: "p" });
  expect(screen.queryByLabelText("Feedback comment")).toBeNull();
  change("Resource filter", "resource-1");
  await screen.findByText(item.comment);
  openEditor("Respond or manage feedback");
  change("Moderator response (optional)", "New response");
  click("Address feedback");
  expect(
    within(await screen.findByRole("dialog")).getByText(
      /replace its current moderator response/,
    ),
  ).toBeTruthy();
  expect(
    within(screen.getByRole("dialog")).queryByText("New response"),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Confirm");
  await screen.findByText("New response", { selector: "p" });
  openEditor("Respond or manage feedback");
  click("Reopen feedback");
  expect(
    within(await screen.findByRole("dialog")).getByText(
      /clear its current moderator response/,
    ),
  ).toBeTruthy();
  expect(post).toHaveBeenCalledTimes(1);
  click("Confirm");
  await waitFor(() => expect(screen.queryByText("New response")).toBeNull());
  openEditor("Respond or manage feedback");
  click("Remove feedback");
  const dialog = await screen.findByRole("dialog");
  expect(
    within(dialog)
      .getByRole("button", { name: "Confirm" })
      .classList.contains("danger"),
  ).toBe(true);
  expect(
    within(dialog).getByText(/This feedback cannot be reopened/),
  ).toBeTruthy();
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  openEditor("Respond or manage feedback");
  openEditor("Respond or manage feedback");
  await confirmCommand("Remove feedback");
  await screen.findByText("Comment and response have been cleared.");
  const next = screen.getAllByRole("button", { name: "Next page" });
  fireEvent.click(required(next[0]));
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      "/api/v1/resources",
      expect.objectContaining({
        params: { query: { limit: 20, cursor: "resources-next" } },
      }),
    ),
  );
  fireEvent.click(
    required(screen.getAllByRole("button", { name: "Next page" })[1]),
  );
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      "/api/v1/feedback/inbox",
      expect.objectContaining({
        params: {
          query: expect.objectContaining({
            cursor: "feedback-next",
          }) as unknown,
        },
      }),
    ),
  );
});

it("submits resource feedback without manufacturing a release or revision context", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/feedback": { items: [] } });
  post.mockResolvedValue(etagReply({ id: "feedback-1", revision: 1 }));
  render(
    <FeedbackEntry namespaceId="ns-1" {...props} resourceId="resource-1" />,
  );
  openFeedback();
  await screen.findByText("No feedback matches these filters.");
  change("Feedback comment", "Useful resource");
  await confirmCommand("Submit feedback");
  await screen.findByText("Feedback submitted.");
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/feedback",
    { body: { resourceId: "resource-1", comment: "Useful resource" } },
  ]);
  expect(post.mock.calls[0]?.[1]).not.toHaveProperty("body.releaseId");
  expect(post.mock.calls[0]?.[1]).not.toHaveProperty("body.versionRevisionId");
});

it("admits a minimal moderator without resource reads and keeps exact filters independent", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/feedback/inbox": { items: [item] } });
  const access = {
    ...administrator,
    permissions: ["feedback.moderate" as const],
    availablePermissions: ["feedback.moderate" as const],
    scopeEnforcement: true,
    scopes: ["tenant.admin" as const],
  };
  const view = render(<Feedback {...props} access={access} />);
  await screen.findByText(item.comment);
  expect(get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/feedback/inbox",
  ]);
  expect(
    screen.queryByRole("link", { name: "Resource and versions" }),
  ).toBeNull();
  expect(screen.queryByRole("link", { name: "Original release" })).toBeNull();
  const id = "00000000-0000-0000-0000-000000000010";
  change("Resource ID filter (optional)", id);
  expect(get).toHaveBeenCalledOnce();
  click("Apply resource filter");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  expect(get.mock.calls[1]?.[1]).toMatchObject({
    params: { query: { resourceId: id } },
  });
  click("All resources");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(3));
  expect(get.mock.calls[2]?.[1]).not.toHaveProperty("params.query.resourceId");
  view.rerender(<Feedback {...props} access={{ ...access, scopes: [] }} />);
  expect(screen.getByRole("alert").textContent).toContain("feedback.moderate");
  expect(screen.queryByText(item.comment)).toBeNull();
  expect(post).not.toHaveBeenCalled();
});

it("keeps own feedback editable after resource-read revocation without inbox or resource disclosure", async () => {
  const { props, get, patch, post } = screenWorkspace();
  readReplies(get, { "/api/v1/feedback": { items: [item] } });
  patch.mockResolvedValue(etagReply(item));
  const access = {
    ...props.access,
    ...testAccess(["feedback_member"]),
    scopeEnforcement: true,
    scopes: ["catalog.read" as const, "feedback.write" as const],
  };
  const view = render(<OwnFeedback {...props} access={access} />);
  await screen.findByText(item.comment, { selector: "p" });
  expect(
    screen.getByRole("heading", { name: "Your submissions" }),
  ).toBeTruthy();
  expect(screen.getAllByText(/Do not include credentials/)).toHaveLength(1);
  expect(screen.getByText(/To send feedback, open a resource/)).toBeTruthy();
  expect(
    screen.getByText(/existing submissions remain available/),
  ).toBeTruthy();
  expect(screen.queryByText(/New feedback (is about|refers)/)).toBeNull();
  const reads = get.mock.calls.length;
  fireEvent.click(screen.getByText("Feedback access"));
  expect(get).toHaveBeenCalledTimes(reads);
  expect(post).not.toHaveBeenCalled();
  change(
    "Resource ID filter (optional)",
    "00000000-0000-0000-0000-000000000001",
  );
  click("Apply resource filter");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(reads + 1));
  expect(screen.queryByText(/New feedback (is about|refers)/)).toBeNull();
  expect(screen.queryByRole("button", { name: "Submit feedback" })).toBeNull();
  expect(
    screen.queryByRole("link", { name: "Resource and versions" }),
  ).toBeNull();
  expect(screen.queryByText("Respond or manage feedback")).toBeNull();
  openEditor("Edit or withdraw your feedback");
  change("Feedback comment", "Updated private report");
  await confirmCommand("Save feedback changes");
  expect(patch.mock.calls[0]?.[1]).toMatchObject({
    params: {
      path: { feedbackId: item.id },
      header: { "If-Match": '"rev-2"' },
    },
    body: { comment: "Updated private report" },
  });
  expect(get.mock.calls.every(([path]) => path === "/api/v1/feedback")).toBe(
    true,
  );
  expect(post).not.toHaveBeenCalled();
  view.rerender(
    <OwnFeedback {...props} access={{ ...access, scopes: ["catalog.read"] }} />,
  );
  expect(screen.getByLabelText("Feedback comment")).toHaveProperty(
    "value",
    "Updated private report",
  );
  expect(
    screen.getByLabelText("Feedback comment").closest("fieldset"),
  ).toHaveProperty("disabled", true);
  expect(await screen.findByText(item.comment, { selector: "p" })).toBeTruthy();
  view.rerender(
    <OwnFeedback {...props} access={{ ...access, actorType: "service" }} />,
  );
  expect(screen.queryByText(item.comment)).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain("human");
});

it("does not read own feedback without catalog.read scope", () => {
  const { props, get } = screenWorkspace();
  render(
    <OwnFeedback
      {...props}
      access={{
        ...props.access,
        ...testAccess(["feedback_member"]),
        scopeEnforcement: true,
        scopes: ["feedback.write"],
      }}
    />,
  );
  expect(screen.getByRole("alert").textContent).toContain("catalog.read");
  expect(get).not.toHaveBeenCalled();
});

it("distinguishes identical-looking private feedback by exact ID/revision and refreshes without directory reads", async () => {
  const w = screenWorkspace();
  const access = {
    ...w.props.access,
    permissions: ["feedback.own" as const],
    availablePermissions: ["feedback.own" as const],
  };
  w.get.mockResolvedValue(
    etagReply({ items: [item, { ...item, id: "feedback-2", revision: 3 }] }),
  );
  const writeText = vi.fn().mockResolvedValue(undefined);
  const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
  try {
    render(<OwnFeedback {...w.props} access={access} />);
    const ids = await screen.findAllByText("Copy feedback ID", {
      selector: "button",
    });
    ids.forEach(revealEditor);
    const rows = screen.getAllByRole("article");
    fireEvent.click(
      within(rows[1]!).getByRole("button", { name: "Copy feedback ID" }),
    );
    await waitFor(() =>
      expect(writeText).toHaveBeenLastCalledWith("feedback-2"),
    );
    fireEvent.click(
      within(rows[1]!).getByRole("button", { name: "Copy feedback revision" }),
    );
    await waitFor(() => expect(writeText).toHaveBeenLastCalledWith("3"));
    expect(w.get).toHaveBeenCalledOnce();
    expect(
      screen.queryByRole("link", { name: "Resource and versions" }),
    ).toBeNull();
    w.get.mockResolvedValue(etagReply({ items: [{ ...item, revision: 4 }] }));
    click("Refresh feedback");
    await waitFor(() => expect(screen.getAllByRole("article")).toHaveLength(1));
    click("Copy feedback revision");
    await waitFor(() => expect(writeText).toHaveBeenLastCalledWith("4"));
    expect(w.get).toHaveBeenCalledTimes(2);
  } finally {
    if (original) Object.defineProperty(navigator, "clipboard", original);
    else Reflect.deleteProperty(navigator, "clipboard");
  }
});

it("retains only entered feedback response drafts and disables a prepared command after same-identity access loss", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/feedback/inbox": { items: [item] },
    "/api/v1/resources": { items: [] },
  });
  const view = render(<Feedback {...w.props} access={administrator} />);
  await screen.findByText(item.comment, { selector: "p" });
  openEditor("Respond or manage feedback");
  change("Moderator response (optional)", "Retain this private response");
  click("Address feedback");
  const dialog = await screen.findByRole("dialog");
  const reads = w.get.mock.calls.length;
  view.rerender(
    <Feedback
      {...w.props}
      access={{ ...administrator, permissions: [], availablePermissions: [] }}
    />,
  );
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "Retain this private response",
  );
  expect(
    screen.getByLabelText("Moderator response (optional)").closest("fieldset"),
  ).toHaveProperty("disabled", true);
  expect(screen.queryByText(item.comment, { selector: "p" })).toBeNull();
  const confirm = within(dialog).getByRole("button", {
    name: "Confirm",
  });
  expect(confirm).toHaveProperty("disabled", true);
  fireEvent.click(confirm);
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(w.post).not.toHaveBeenCalled();
  view.rerender(
    <Feedback
      {...w.props}
      access={{ ...administrator, realmId: "another-realm" }}
    />,
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    (
      screen.queryByLabelText(
        "Moderator response (optional)",
      ) as HTMLTextAreaElement | null
    )?.value,
  ).not.toBe("Retain this private response");
});

it("protects an entered direct-case response when its moderator read scope is withdrawn", async () => {
  const w = screenWorkspace();
  const caseId = "00000000-0000-7000-8000-000000000091";
  history.replaceState(null, "", "/unit/feedback/" + caseId);
  readReplies(w.get, {
    "/api/v1/feedback/{feedbackId}": { ...item, id: caseId },
  });
  const access = {
    ...administrator,
    permissions: ["feedback.moderate" as const],
    availablePermissions: ["feedback.moderate" as const],
    scopeEnforcement: true,
    scopes: ["catalog.read" as const, "tenant.admin" as const],
  };
  const view = render(<Feedback {...w.props} access={access} id={caseId} />);
  await screen.findByText(item.comment, { selector: "p" });
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.get).toHaveBeenCalledWith(
    "/api/v1/feedback/{feedbackId}",
    expect.objectContaining({ params: { path: { feedbackId: caseId } } }),
  );
  openEditor("Respond or manage feedback");
  change("Moderator response (optional)", "Keep this unsent case response");
  view.rerender(
    <Feedback
      {...w.props}
      access={{ ...access, scopes: ["tenant.admin"] }}
      id={caseId}
    />,
  );
  expect(screen.getByRole("alert").textContent).toContain(
    "Feedback case reads require feedback.moderate and catalog.read scope when enforced.",
  );
  expect(screen.getByRole("alert").textContent).toContain(
    "Entered drafts remain protected.",
  );
  expect(
    screen.getByRole("link", { name: "Review My access" }).getAttribute("href"),
  ).toBe("/unit/access");
  expect(screen.queryByText(item.comment, { selector: "p" })).toBeNull();
  const draft = screen.getByLabelText("Moderator response (optional)");
  expect(draft).toHaveProperty("value", "Keep this unsent case response");
  expect(draft.closest("fieldset")).toHaveProperty("disabled", true);
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("leads inline moderator detail with full text and dates before deliberate identifiers without extra reads", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/feedback/inbox": { items: [item] } });
  render(
    <Feedback
      {...w.props}
      access={{
        ...administrator,
        permissions: ["feedback.moderate"],
        availablePermissions: ["feedback.moderate"],
        scopeEnforcement: true,
        scopes: ["tenant.admin"],
      }}
    />,
  );
  const comment = await screen.findByText(item.comment, { selector: "p" });
  revealEditor(comment);
  const article = comment.closest("article")!;
  const submitted = within(article).getByText(/Submitted/);
  const response = within(article).getByText(item.response, { selector: "p" });
  const references = within(article).getByText("Identifiers and revision", {
    selector: "summary",
  });
  expect(
    submitted.compareDocumentPosition(comment) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    comment.compareDocumentPosition(response) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    response.compareDocumentPosition(references) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(references.closest("details")).toHaveProperty("open", false);
  openEditor("Respond or manage feedback");
  expect(screen.getByLabelText("Moderator response (optional)")).toBeTruthy();
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/feedback/inbox",
  ]);
  expect(w.post).not.toHaveBeenCalled();
});

it("reuses an authorized resource caption when its inventory case opens, without resolving the target again", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources": { items: [resource] },
    "/api/v1/feedback/inbox": { items: [item] },
    "/api/v1/feedback/{feedbackId}": item,
  });
  render(<Feedback {...w.props} access={administrator} />);
  await screen.findByText(resource.canonicalIdentifier, { selector: "code" });
  fireEvent.click(screen.getByRole("link", { name: "Open feedback case" }));
  await screen.findByRole("button", { name: "Read current feedback" });
  await screen.findByText(item.comment, { selector: "p" });
  expect(
    w.get.mock.calls.filter(([path]) => path === "/api/v1/resources"),
  ).toHaveLength(1);
  expect(
    w.get.mock.calls.filter(
      ([path]) => path === "/api/v1/feedback/{feedbackId}",
    ),
  ).toHaveLength(1);
  expect(
    w.get.mock.calls.some(
      ([path]) => path === "/api/v1/resources/{resourceId}",
    ),
  ).toBe(false);
  expect(w.post).not.toHaveBeenCalled();
});

it("previews a long general report compactly and captures an explicit empty moderator response", async () => {
  const w = screenWorkspace();
  const general = {
    ...item,
    versionRevisionId: undefined,
    releaseId: undefined,
    response: undefined,
    comment: "😀".repeat(181),
  };
  readReplies(w.get, { "/api/v1/feedback/inbox": { items: [general] } });
  render(
    <Feedback
      {...w.props}
      access={{
        ...administrator,
        permissions: ["feedback.moderate"],
        availablePermissions: [],
      }}
    />,
  );
  const full = await screen.findByText(general.comment, { selector: "p" });
  const row = full.closest(".case-summary")!;
  const preview = row.querySelector("summary")!;
  expect(preview.textContent).toContain("😀".repeat(180) + "…");
  expect(preview.textContent).not.toContain(general.comment);
  openEditor("Respond or manage feedback");
  click("Address feedback");
  const dialog = await screen.findByRole("dialog");
  expect(
    within(dialog).getByLabelText("Command summary").textContent,
  ).toContain("General resource feedback");
  expect(dialog.textContent).toContain("Empty response");
  expect(w.post).not.toHaveBeenCalled();
  click("Cancel");
  expect(screen.getByLabelText("Moderator response (optional)")).toHaveProperty(
    "value",
    "",
  );
});
