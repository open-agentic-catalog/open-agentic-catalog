// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import Reviews, { Release } from "./Reviews";
import Publication from "./Publication";
import {
  evidence,
  listingRevision,
  release,
  submission,
} from "../../tests/unit/responses";
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

screenLifecycle("/unit/reviews");
const snapshot = {
  tenantId: "tenant-a",
  etag: '"snapshot"',
  createdAt: "2026-10-20T00:00:00Z",
  id: "snapshot-1",
  host: "catalog.example.com",
  generation: 1,
  state: "candidate",
  audience: "anonymous",
  exclusions: [],
  ardProfile: "ard-0.91-aa3e598",
  aiCatalogProfile: "ai-catalog-1.0-b062278",
  ard: { entries: [] },
  aiCatalog: { entries: [] },
};

function setup(view: string, current = false) {
  const values = {
    "/api/v1/catalog-listings/{listingId}": listing,
    "/api/v1/review-submissions/{submissionId}": {
      ...submission,
      state: current ? "approved" : "submitted",
    },
    "/api/v1/review-submissions/{submissionId}/evidence": evidence,
    "/api/v1/listing-revisions/{revisionId}": listingRevision,
    "/api/v1/listing-releases/{releaseId}": {
      ...release,
      state: current ? "deprecated" : "published",
      revision: current ? 2 : 1,
    },
    "/api/v1/publication/hosts": { items: [] },
    "/api/v1/publication/snapshots/{snapshotId}": {
      ...snapshot,
      state: current ? "active" : "candidate",
    },
  };
  const target =
    view === "review" ? submission : view === "release" ? release : snapshot;
  const body =
    view === "review"
      ? { submission: { ...submission, state: "approved" } }
      : { ...target, state: view === "release" ? "deprecated" : "active" };
  const title =
    view === "review"
      ? "Record review decision"
      : view === "release"
        ? "Change release disposition"
        : "Activate candidate";
  const reload =
    view === "review"
      ? "Reload review and next steps"
      : view === "release"
        ? "Reload release state"
        : "Reload snapshot state";
  const message =
    view === "review"
      ? "Review approved."
      : view === "release"
        ? "Release deprecated."
        : "Discovery snapshot activated.";
  const heading =
    view === "review"
      ? "Review approved"
      : view === "release"
        ? "Release deprecated"
        : "Generation 1: active";
  const initialHeading =
    view === "review"
      ? "Awaiting independent review"
      : view === "release"
        ? "Release published"
        : "Generation 1: candidate";
  const component = (props: ReturnType<typeof screenWorkspace>["props"]) =>
    view === "review" ? (
      <Reviews {...props} id={submission.id} />
    ) : view === "release" ? (
      <Release {...props} id={release.id} />
    ) : (
      <Publication {...props} id={snapshot.id} />
    );
  return {
    values,
    body,
    title,
    reload,
    message,
    heading,
    initialHeading,
    component,
  };
}

async function open(view: string, withoutETag = false) {
  const w = screenWorkspace();
  const test = setup(view);
  history.replaceState(
    null,
    "",
    view === "snapshot"
      ? `/unit/publication/${snapshot.id}?host=${snapshot.host}`
      : `/unit/${view}s`,
  );
  readReplies(w.get, test.values);
  if (withoutETag) {
    const configured = w.get.getMockImplementation()!;
    w.get.mockImplementation(async (...args) => {
      const reply = await configured(...args);
      if (
        !reply ||
        typeof reply !== "object" ||
        !("response" in reply) ||
        !(reply.response instanceof Response)
      )
        throw new Error("Configured unit reply requires a Response");
      reply.response.headers.delete("ETag");
      return reply;
    });
  }
  w.post.mockResolvedValue(etagReply(test.body));
  const props = { ...w.props, access: administrator };
  const rendered = render(test.component(props));
  await screen.findByRole("heading", { name: test.initialHeading });
  if (view === "review") {
    click("Inspect submitted evidence");
    await screen.findByRole("heading", {
      name: "Submitted evidence",
    });
  }
  change("Reason", "Keep the captured reason");
  return { ...w, test, rendered, props };
}

const hostCommands = [
  {
    action: "build",
    title: "Build candidate snapshot",
    path: "/api/v1/publication/snapshots",
    message: "Candidate snapshot built.",
    state: "candidate",
  },
  {
    action: "unpublish",
    title: "Unpublish host",
    path: "/api/v1/publication:unpublish",
    message: "Host unpublished.",
    state: "retired",
  },
];

async function openHost(command: (typeof hostCommands)[number]) {
  const w = screenWorkspace();
  history.replaceState(null, "", `/unit/publication?host=${snapshot.host}`);
  const values = {
    "/api/v1/publication/hosts": { items: [] },
    "/api/v1/publication/snapshots": {
      items: [{ ...snapshot, state: "active" }],
    },
  };
  readReplies(w.get, values);
  const body = {
    ...snapshot,
    id: command.action === "build" ? "new-candidate" : snapshot.id,
    generation: command.action === "build" ? 2 : 1,
    state: command.state,
  };
  w.post.mockResolvedValue(etagReply(body));
  const props = { ...w.props, access: administrator };
  const rendered = render(<Publication {...props} />);
  await screen.findByRole("link", { name: "Generation 1" });
  fireEvent.click(screen.getByText("Withdraw public discovery"));
  if (command.action === "unpublish")
    change("Reason", "Keep the host withdrawal reason");
  return { ...w, values, body, props, rendered };
}

it.each(hostCommands)(
  "retains the host $action receipt through failed readback and rearms only after a successful read",
  async (command) => {
    const w = await openHost(command);
    const reads = w.get.mock.calls.length;
    await confirmCommand(command.title);
    await screen.findByText(command.message, { exact: true });
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(screen.getByText(/snapshot inventory predates/)).toBeTruthy();
    for (const { title } of hostCommands)
      expect(screen.getByRole("button", { name: title })).toHaveProperty(
        "disabled",
        true,
      );
    if (command.action === "build")
      expect(
        screen
          .getByRole("link", { name: "Inspect candidate" })
          .getAttribute("href"),
      ).toBe(`/unit/publication/new-candidate?host=${snapshot.host}`);

    w.get.mockRejectedValueOnce(new Error("Controlled host readback failure"));
    click("Refresh snapshots");
    await screen.findByRole("alert");
    expect(screen.getByText(command.message, { exact: true })).toBeTruthy();
    expect(screen.getByText(/snapshot inventory predates/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Generation 1" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Generation 2" })).toBeNull();
    for (const { title } of hostCommands)
      expect(screen.getByRole("button", { name: title })).toHaveProperty(
        "disabled",
        true,
      );
    expect(w.post).toHaveBeenCalledTimes(1);

    readReplies(w.get, {
      ...w.values,
      "/api/v1/publication/snapshots": { items: [w.body] },
    });
    click("Refresh snapshots");
    await waitFor(() =>
      expect(screen.queryByText(/snapshot inventory predates/)).toBeNull(),
    );
    expect(
      screen.getByRole("link", {
        name: `Generation ${command.action === "build" ? 2 : 1}`,
      }).parentElement?.textContent,
    ).toContain(`/ ${command.state} / anonymous`);
    expect(screen.getByText(command.message, { exact: true })).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Build candidate snapshot" }),
    ).toHaveProperty("disabled", false);
    if (command.action === "unpublish")
      expect(screen.getByLabelText("Reason")).toHaveProperty(
        "value",
        "Keep the host withdrawal reason",
      );
    expect(w.get).toHaveBeenCalledTimes(reads + 2);
    expect(w.post).toHaveBeenCalledTimes(1);
    expect(w.props.onFailure).toHaveBeenCalledOnce();
  },
);

it.each(
  hostCommands.flatMap((command) =>
    ["host", "state"].map((invalidPart) => ({ ...command, invalidPart })),
  ),
)(
  "keeps the exact host $action command after a wrong $invalidPart acknowledgement and lost write scope",
  async (command) => {
    const w = await openHost(command);
    const invalid = {
      ...w.body,
      ...(command.invalidPart === "host"
        ? { host: "other.example.com" }
        : { state: command.action === "build" ? "active" : "candidate" }),
    };
    w.post
      .mockResolvedValueOnce(etagReply(invalid))
      .mockResolvedValue(etagReply(w.body));
    const reads = w.get.mock.calls.length;
    await confirmCommand(command.title);
    await screen.findByRole("button", { name: "Retry same command" });
    expect(screen.queryByText(command.message, { exact: true })).toBeNull();
    expect(
      screen.queryByRole("link", { name: "Inspect candidate" }),
    ).toBeNull();
    w.rendered.rerender(
      <Publication
        {...w.props}
        access={{
          ...administrator,
          scopeEnforcement: true,
          scopes: ["catalog.read"],
        }}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Retry same command" }),
    ).toBeTruthy();
    expect(w.post).toHaveBeenCalledTimes(1);
    w.rendered.rerender(<Publication {...w.props} />);
    click("Retry same command");
    await screen.findByText(command.message, { exact: true });
    expect(w.post).toHaveBeenCalledTimes(2);
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(w.post.mock.calls[0]).toMatchObject([
      command.path,
      {
        body:
          command.action === "build"
            ? { host: snapshot.host }
            : {
                host: snapshot.host,
                reason: "Keep the host withdrawal reason",
              },
      },
    ]);
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(w.props.onFailure).toHaveBeenCalledOnce();
    expect(screen.queryByRole("dialog")).toBeNull();
  },
);

it.each(["review", "release", "snapshot"])(
  "does not reload %s when the user keeps an unsaved reason",
  async (view) => {
    const w = await open(view);
    const reads = w.get.mock.calls.length;
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    click(w.test.reload);
    expect(discard).toHaveBeenCalledOnce();
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Keep the captured reason",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["review", "release"])(
  "keeps authorized %s inputs usable while a missing revision blocks preparation",
  async (view) => {
    const w = await open(view, true);
    expect(screen.getByLabelText("Reason").closest("fieldset")).toHaveProperty(
      "disabled",
      false,
    );
    expect(screen.getByRole("button", { name: w.test.title })).toHaveProperty(
      "disabled",
      true,
    );
    expect(
      screen.getByText(
        view === "review"
          ? "Reload the review to obtain its current revision."
          : "Reload the release to obtain its current revision.",
      ),
    ).toBeTruthy();
    click(w.test.title);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps an unsaved host-withdrawal reason when snapshot refresh is cancelled", async () => {
  const w = screenWorkspace();
  history.replaceState(null, "", `/unit/publication?host=${snapshot.host}`);
  readReplies(w.get, {
    "/api/v1/publication/hosts": { items: [] },
    "/api/v1/publication/snapshots": { items: [snapshot] },
  });
  render(<Publication {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Generation 1" });
  change("Reason", "Keep host withdrawal draft");
  const reads = w.get.mock.calls.length;
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Refresh snapshots");
  expect(discard).toHaveBeenCalledOnce();
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep host withdrawal draft",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["review", "release", "snapshot"])(
  "retains the %s command result and draft through a failed deliberate readback",
  async (view) => {
    const w = await open(view);
    const reads = w.get.mock.calls.length;
    await confirmCommand(w.test.title);
    await screen.findByText(w.test.message, { exact: true });
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(
      screen.getByRole("heading", { name: w.test.initialHeading }),
    ).toBeTruthy();
    if (view === "snapshot")
      expect(screen.queryByText(/This candidate is not served/)).toBeNull();
    w.get.mockRejectedValueOnce(new Error("Controlled current-read failure"));
    click(w.test.reload);
    await screen.findByRole("alert");
    expect(screen.getByText(w.test.message, { exact: true })).toBeTruthy();
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Keep the captured reason",
    );
    expect(screen.getByRole("button", { name: w.test.title })).toHaveProperty(
      "disabled",
      true,
    );
    expect(w.post).toHaveBeenCalledTimes(1);
    readReplies(w.get, setup(view, true).values);
    click(w.test.reload);
    await screen.findByRole("heading", { name: w.test.heading });
    expect(screen.getByText(w.test.message, { exact: true })).toBeTruthy();
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Keep the captured reason",
    );
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);

it.each(
  ["review", "release", "snapshot"].flatMap((view) => [
    { view, invalidPart: "target" },
    { view, invalidPart: "state" },
  ]),
)(
  "preserves exact $view recovery after an unknown $invalidPart response and access change",
  async ({ view, invalidPart }) => {
    const w = await open(view);
    const invalid =
      view === "review"
        ? {
            submission: {
              ...submission,
              id:
                invalidPart === "target" ? "foreign-submission" : submission.id,
              state: invalidPart === "state" ? "rejected" : "approved",
            },
          }
        : {
            ...w.test.body,
            ...(invalidPart === "target"
              ? { id: "foreign-target" }
              : { state: "retired" }),
          };
    w.post
      .mockResolvedValueOnce(etagReply(invalid))
      .mockResolvedValue(etagReply(w.test.body));
    await confirmCommand(w.test.title);
    await screen.findByRole("button", { name: "Retry same command" });
    expect(screen.queryByText(w.test.message, { exact: true })).toBeNull();
    w.rendered.rerender(
      w.test.component({
        ...w.props,
        access: {
          ...administrator,
          scopeEnforcement: true,
          scopes: ["catalog.read"],
        },
      }),
    );
    expect(
      screen.getByRole("button", { name: "Retry same command" }),
    ).toBeTruthy();
    expect(w.post).toHaveBeenCalledTimes(1);
    w.rendered.rerender(w.test.component(w.props));
    click("Retry same command");
    await screen.findByText(w.test.message, { exact: true });
    expect(w.post).toHaveBeenCalledTimes(2);
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(w.props.onFailure).toHaveBeenCalledTimes(1));
  },
);

it.each(["review", "release", "snapshot"])(
  "keeps the historical %s receipt separate from a newer current read",
  async (view) => {
    const w = await open(view);
    await confirmCommand(w.test.title);
    await screen.findByText(w.test.message, { exact: true });
    const values = {
      ...w.test.values,
      "/api/v1/review-submissions/{submissionId}": {
        ...submission,
        state: "approved",
      },
      "/api/v1/catalog-listings/{listingId}": { ...listing, revision: 4 },
      "/api/v1/listing-releases/{releaseId}": {
        ...release,
        state: "revoked",
        revision: 3,
      },
      "/api/v1/publication/snapshots/{snapshotId}": {
        ...snapshot,
        state: "retired",
      },
    };
    readReplies(w.get, values);
    click(w.test.reload);
    await screen.findByRole("heading", {
      name:
        view === "review"
          ? "Review approved"
          : view === "release"
            ? "Release revoked"
            : "Generation 1: retired",
    });
    expect(screen.getByText(w.test.message, { exact: true })).toBeTruthy();
    expect(
      screen.getByText(/This command result may be a historical replay/),
    ).toBeTruthy();
    if (view === "review")
      expect(
        screen.getByRole("button", { name: "Record review decision" }),
      ).toHaveProperty("disabled", true);
    else if (view === "release")
      expect(
        screen.getByLabelText("Disposition").closest("fieldset"),
      ).toHaveProperty("disabled", true);
    else {
      expect(
        screen.queryByRole("button", { name: "Activate candidate" }),
      ).toBeNull();
      expect(
        screen.getByRole("button", { name: "Restore retained snapshot" }),
      ).toHaveProperty("disabled", false);
    }
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);
