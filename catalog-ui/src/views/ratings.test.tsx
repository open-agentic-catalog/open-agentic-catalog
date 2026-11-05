// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import Ratings, {
  OwnRatings,
  RatingEntry,
  RatingLabels,
  RatingSharing,
  requestForResult,
} from "./Ratings";
import { confirmDiscard } from "./workspace";
import { deferred, reply } from "../../tests/unit/fixtures";
import {
  administrator,
  click,
  confirmCommand,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
import type { components } from "../api/schema";
import type { SearchResult, Access } from "../api/client";

function moderationReason() {
  if (!screen.queryByLabelText("Moderation reason"))
    fireEvent.click(
      screen.getAllByRole("button", {
        name: /^(Remove rating|Permit new submission)$/,
      })[0]!,
    );
  return screen.getByLabelText("Moderation reason");
}
screenLifecycle("/unit/catalog");
const target: components["schemas"]["RatingTarget"] = {
  origin: "local",
  resourceId: "resource-1",
  versionRevisionId: "revision-1",
  identifier: "urn:air:example.com:agents:helper",
  kind: "agent",
  versionPresent: true,
  version: "1.0.0",
};
const request = { target, releaseId: "release-1" };
const vote = {
  id: "rating-1",
  tenantId: "tenant-a",
  target,
  score: 4,
  consent: true,
  state: "active" as const,
  revision: 2,
  createdAt: stamp,
  updatedAt: stamp,
};
const empty = { target, count: 0, changedEntry: false };
function setup(view: unknown = empty) {
  const w = screenWorkspace();
  const own = (view as { own?: typeof vote }).own;
  w.post.mockImplementation(async (path) => {
    if (path === "/api/v1/ratings/read") return reply(view);
    return reply({
      id: vote.id,
      revision:
        path === "/api/v1/ratings"
          ? own
            ? own.revision + 1
            : 1
          : vote.revision + 1,
    });
  });
  return w;
}
async function open() {
  click("Ratings for this version");
  await screen.findByText(/No ratings|4.0 \/ 5/);
}

it("rejects a well-shaped aggregate for another exact rating target", async () => {
  const w = setup({
    ...empty,
    target: { ...target, versionRevisionId: "other-revision" },
  });
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  click("Ratings for this version");
  await screen.findByRole("alert");
  expect(screen.queryByText(/No ratings/)).toBeNull();
  expect(screen.queryByRole("button", { name: "Submit rating" })).toBeNull();
});
it.each([
  { resourceId: "another-resource" },
  { versionRevisionId: "another-revision" },
  { identifier: "urn:air:example.com:agents:another" },
  { kind: "skill" },
  { version: "2.0.0" },
])("rejects a different local target %j", async (change) => {
  const w = setup({ ...empty, target: { ...target, ...change } });
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  click("Ratings for this version");
  await screen.findByRole("alert");
  expect(screen.queryByRole("radio")).toBeNull();
});
it.each([
  "source",
  "generation",
  "kind",
  "identifier",
  "version-presence",
  "version",
])("rejects a foreign aggregate with mismatched %s", async (field) => {
  const foreign = {
    origin: "foreign" as const,
    sourceId: "source-1",
    presentationGeneration: 2,
    kind: "agent" as const,
    identifier: target.identifier,
    versionPresent: true,
    version: "",
  };
  const changes = {
    source: { sourceId: "source-2" },
    generation: { presentationGeneration: 3 },
    kind: { kind: "skill" },
    identifier: { identifier: "urn:air:example.com:agents:other" },
    "version-presence": { versionPresent: false, version: undefined },
    version: { version: "1.0.0" },
  };
  const w = setup({
    target: { ...foreign, ...changes[field as keyof typeof changes] },
    count: 0,
    changedEntry: false,
  });
  render(<RatingEntry {...w.props} request={{ target: foreign }} />);
  click("Ratings for this version");
  await screen.findByRole("alert");
  expect(screen.queryByRole("radio")).toBeNull();
});
it.each(["tenant", "target"])(
  "rejects an own vote with mismatched %s",
  async (field) => {
    const w = setup({
      ...empty,
      own: {
        ...vote,
        ...(field === "tenant"
          ? { tenantId: "other-tenant" }
          : { target: { ...target, resourceId: "other-resource" } }),
      },
    });
    render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
    click("Ratings for this version");
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", { name: "Withdraw rating" }),
    ).toBeNull();
  },
);

it.each([
  "first-revision",
  "update-id",
  "update-revision",
  "withdraw-id",
  "withdraw-revision",
  "resubmit-id",
])(
  "retains exact-key recovery after a well-shaped wrong %s receipt",
  async (mode) => {
    const own =
      mode === "first-revision"
        ? undefined
        : mode === "resubmit-id"
          ? { ...vote, state: "withdrawn" as const, score: 0, consent: false }
          : vote;
    const w = setup({
      ...empty,
      ...(own ? { count: 1, average: 4, own } : {}),
    });
    let writes = 0;
    w.post.mockImplementation(async (path) => {
      if (path === "/api/v1/ratings/read")
        return reply({
          ...empty,
          ...(own ? { count: 1, average: 4, own } : {}),
        });
      writes++;
      return reply({
        id: writes === 1 && mode.endsWith("id") ? "wrong-vote" : vote.id,
        revision:
          writes === 1 && mode.endsWith("revision")
            ? 99
            : own
              ? own.revision + 1
              : 1,
      });
    });
    render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
    await open();
    if (!mode.startsWith("withdraw"))
      fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
    await confirmCommand(
      mode.startsWith("withdraw")
        ? "Withdraw rating"
        : own?.state === "active"
          ? "Update rating"
          : "Submit rating",
    );
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(screen.queryByText("Rating saved.")).toBeNull();
    expect(screen.queryByText("Rating withdrawn.")).toBeNull();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Retry same command" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const commands = w.post.mock.calls.filter(
      ([path]) => path !== "/api/v1/ratings/read",
    );
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
  },
);
async function confirmScore(name: string, score: number, consent: boolean) {
  click(name);
  const dialog = within(await screen.findByRole("dialog"));
  const facts = dialog.getByLabelText("Command summary");
  expect(within(facts).getByText(`${score} / 5`)).toBeTruthy();
  expect(
    within(facts).getByText(consent ? "Allowed" : "Not allowed"),
  ).toBeTruthy();
  await act(async () =>
    fireEvent.click(dialog.getByRole("button", { name: "Confirm" })),
  );
}

it.each(["é", "🙂"])(
  "reads a 128-character %s version without rejecting its rating target",
  async (character) => {
    const unicodeTarget = { ...target, version: character.repeat(128) };
    const w = setup({ ...empty, target: unicodeTarget });
    render(
      <RatingEntry
        {...w.props}
        request={{ ...request, target: unicodeTarget }}
        namespaceId="ns-1"
      />,
    );
    await open();
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/ratings/read",
      expect.objectContaining({ body: { ...request, target: unicodeTarget } }),
    );
  },
);
it.each(["new", "withdrawn"])(
  "defers reads, defaults a %s rating private, and submits one exact version through confirmation",
  async (state) => {
    const w = setup(
      state === "new"
        ? empty
        : {
            ...empty,
            own: { ...vote, state: "withdrawn", score: 0, consent: false },
          },
    );
    render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
    expect(w.post).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("region", { name: "Ratings for this version" }),
    ).toBeNull();
    await open();
    expect(
      screen.getByRole("region", { name: "Ratings for this version" }),
    ).toBeTruthy();
    expect(
      screen.getByText(/Published snapshots and copies cannot be recalled/),
    ).toBeTruthy();
    const consent = screen.getByRole<HTMLInputElement>("checkbox", {
      name: /Allow my score/,
    });
    expect(consent.checked).toBe(false);
    expect(
      screen.getByText(
        /Published summaries include only consenting ratings of locally released versions/,
      ),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("radio", { name: "5 stars" }));
    await confirmScore("Submit rating", 5, false);
    await waitFor(() =>
      expect(w.post).toHaveBeenCalledWith(
        "/api/v1/ratings",
        expect.objectContaining({
          body: { ...request, score: 5 },
          params: {
            header: {
              "Idempotency-Key": expect.any(String) as unknown,
              ...(state === "withdrawn" ? { "If-Match": '"rev-2"' } : {}),
            },
          },
        }),
      ),
    );
  },
);
it.each([true, false])(
  "preserves active consent %s on an edit and uses the existing revision ETag",
  async (consent) => {
    const w = setup({
      ...empty,
      count: 1,
      average: 4,
      own: { ...vote, consent },
    });
    render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
    await open();
    expect(
      screen.getByRole<HTMLInputElement>("checkbox", { name: /Allow my score/ })
        .checked,
    ).toBe(consent);
    expect(
      screen.getByText(
        /Editing an active rating keeps your saved choice unless you change it/,
      ),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("radio", { name: "2 stars" }));
    await confirmScore("Update rating", 2, consent);
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/ratings",
      expect.objectContaining({
        body: { ...request, score: 2 },
        params: {
          header: {
            "If-Match": '"rev-2"',
            "Idempotency-Key": expect.any(String) as unknown,
          },
        },
      }),
    );
  },
);
it.each([true, false])(
  "retains the exact rating command and sharing %s after a malformed successful acknowledgement",
  async (consent) => {
    const w = setup({ ...empty, count: 1, average: 4, own: vote });
    let writes = 0;
    w.post.mockImplementation(async (path) => {
      if (path === "/api/v1/ratings/read")
        return reply({ ...empty, count: 1, average: 4, own: vote });
      writes++;
      return reply({ id: vote.id, revision: writes === 1 ? 0 : 3 });
    });
    render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
    await open();
    fireEvent.click(screen.getByRole("radio", { name: "2 stars" }));
    if (!consent)
      fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
    await confirmScore("Update rating", 2, consent);
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByLabelText("Command summary").textContent,
    ).toContain(consent ? "Allowed" : "Not allowed");
    expect(within(dialog).getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(w.props.onFailure).toHaveBeenCalledTimes(1);
    expect(
      within(dialog).getByRole<HTMLButtonElement>("button", {
        name: "Back to form",
      }).disabled,
    ).toBe(true);
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "2 stars" }).checked,
    ).toBe(true);
    expect(
      screen.getByRole<HTMLInputElement>("checkbox", { name: /Allow my score/ })
        .checked,
    ).toBe(consent);
    expect(writes).toBe(1);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Retry same command" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const calls = w.post.mock.calls.filter(
      ([path]) => path === "/api/v1/ratings",
    );
    expect(calls).toHaveLength(2);
    expect(calls[1]).toEqual(calls[0]);
    expect(calls[0]).toEqual([
      "/api/v1/ratings",
      {
        body: { ...request, score: 2, ...(consent ? {} : { consent: false }) },
        params: {
          header: {
            "Idempotency-Key": expect.any(String) as unknown,
            "If-Match": '"rev-2"',
          },
        },
      },
    ]);
  },
);
it("allows explicit consent withdrawal and preserves a draft when collapse is declined", async () => {
  const w = setup({ ...empty, count: 1, average: 4, own: vote });
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  await open();
  fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
  vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Ratings for this version");
  expect(
    screen.getByRole<HTMLInputElement>("checkbox", { name: /Allow my score/ })
      .checked,
  ).toBe(false);
  await confirmCommand("Update rating");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings",
    expect.objectContaining({ body: { ...request, score: 4, consent: false } }),
  );
});
it("withdraws through the owner command and never sends another score", async () => {
  const w = setup({ ...empty, count: 1, average: 4, own: vote });
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  await open();
  await confirmCommand("Withdraw rating");
  expect(w.post).toHaveBeenCalledWith("/api/v1/ratings/{ratingId}:withdraw", {
    params: {
      path: { ratingId: vote.id },
      header: {
        "If-Match": '"rev-2"',
        "Idempotency-Key": expect.any(String) as unknown,
      },
    },
    body: {},
  });
});
it("keeps a removed opinion blocked and displays changed-source context", async () => {
  const w = setup({
    ...empty,
    own: { ...vote, score: 0, consent: false, state: "removed" },
    changedEntry: true,
  });
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  await open();
  expect(screen.getByText(/source entry has changed/i)).toBeTruthy();
  expect(screen.queryByRole("radio")).toBeNull();
  expect(screen.getByText(/moderator removed/)).toBeTruthy();
});
it("keeps rating prerequisites neutral while namespace access is pending or fails", async () => {
  const w = setup();
  const namespace = deferred<ReturnType<typeof reply>>();
  w.get.mockImplementation(async (path) => {
    expect(path).toBe("/api/v1/me");
    return namespace.promise;
  });
  render(
    <RatingEntry
      {...w.props}
      access={{ ...w.props.access, nextCursor: "more-grants" }}
      request={request}
      namespaceId="ns-1"
    />,
  );
  await open();
  expect(
    screen.getByText(/To rate this version, you need a human identity/),
  ).toBeTruthy();
  expect(screen.queryByRole("radio")).toBeNull();
  namespace.reject(new Error("Controlled namespace lookup failure"));
  await screen.findByRole("alert");
  expect(
    screen.getByText(/To rate this version, you need a human identity/),
  ).toBeTruthy();
  expect(screen.queryByText(/do not currently have access/)).toBeNull();
  expect(screen.queryByRole("radio")).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.get).toHaveBeenCalledTimes(1);
});
it.each(["pending", "failed"])(
  "keeps owner withdrawal available with a %s namespace lookup",
  async (phase) => {
    const w = setup({ ...empty, count: 1, average: 4, own: vote });
    const namespace = deferred<ReturnType<typeof reply>>();
    w.get.mockReturnValue(namespace.promise);
    const access: Access = {
      ...w.props.access,
      permissions: [],
      availablePermissions: ["ratings.own"],
      namespaceGrants: [],
      nextCursor: "more-grants",
      scopeEnforcement: true,
      scopes: ["catalog.read", "ratings.write"],
    };
    render(
      <RatingEntry
        {...w.props}
        access={access}
        request={request}
        namespaceId="ns-1"
      />,
    );
    await open();
    if (phase === "failed") {
      await act(async () =>
        namespace.reject(new Error("Controlled namespace lookup failure")),
      );
      await screen.findByRole("alert");
    }
    expect(screen.queryByRole("radio")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Withdraw rating" }),
    ).toHaveProperty("disabled", false);
    await confirmCommand("Withdraw rating");
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/ratings/{ratingId}:withdraw",
      expect.objectContaining({
        body: {},
        params: {
          path: { ratingId: vote.id },
          header: {
            "If-Match": '"rev-2"',
            "Idempotency-Key": expect.any(String) as unknown,
          },
        },
      }),
    );
    expect(
      w.post.mock.calls.every(
        ([path]) =>
          path === "/api/v1/ratings/read" ||
          path === "/api/v1/ratings/{ratingId}:withdraw",
      ),
    ).toBe(true);
    if (phase === "pending") {
      await act(async () =>
        namespace.reject(new Error("Controlled namespace lookup failure")),
      );
      await screen.findByRole("alert");
    }
    expect(w.get).toHaveBeenCalledTimes(1);
  },
);
it.each(["service", "permission", "scope"])(
  "refuses scoring for a %s limitation",
  async (reason) => {
    const w = setup();
    const access: Access = {
      ...w.props.access,
      ...(reason === "service"
        ? { actorType: "service" as const }
        : reason === "scope"
          ? { scopeEnforcement: true, scopes: ["catalog.read"] }
          : { permissions: [], availablePermissions: [] }),
    };
    render(
      <RatingEntry
        {...w.props}
        access={access}
        request={request}
        namespaceId="ns-1"
      />,
    );
    await open();
    expect(screen.queryByRole("radio")).toBeNull();
    expect(
      screen.getByText(/To rate this version, you need a human identity/),
    ).toBeTruthy();
    const reads = w.post.mock.calls.length;
    fireEvent.click(screen.getByText("Rating access"));
    expect(
      screen.getByText(/Scoring requires/).closest("details"),
    ).toHaveProperty("open", true);
    expect(w.post).toHaveBeenCalledTimes(reads);
  },
);
it.each([
  { ...empty, average: 0 },
  { ...empty, count: 1, average: "bad" },
  { ...empty, own: { ...vote, score: 9 } },
  { ...empty, target: { ...target, versionPresent: "yes" } },
])("rejects malformed consumed rating data", async (view) => {
  const w = setup(view);
  render(<RatingEntry {...w.props} request={request} />);
  click("Ratings for this version");
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
  expect(screen.queryByRole("radio")).toBeNull();
});
it("keeps local, peer and import signals distinct", () => {
  const item = {
    type: "indexed",
    ratings: { count: 2, average: 3.5 },
    peerRatings: {
      reporter: "https://peer.example",
      count: 7,
      average: 4.1,
      measuredAt: stamp,
    },
    importedHere: { resourceId: "resource-1", changedEntry: true },
  } as SearchResult;
  render(<RatingLabels item={item} basePath="/unit" />);
  expect(screen.getByText(/Ratings here:/).textContent).toContain(
    "3.5 / 5 (2)",
  );
  expect(screen.getByText(/Peer-reported rating/).textContent).toContain(
    "https://peer.example",
  );
  expect(
    screen.getByRole("link", { name: "Imported here" }).getAttribute("href"),
  ).toBe("/unit/resources/resource-1");
});
it("preserves absent foreign versions and binds the actual live source proof", () => {
  const item = {
    identifier: target.identifier,
    kind: "agent",
    versionPresent: false,
    type: "live",
    live: { sourceId: "source-1", presentationGeneration: 3 },
    ratings: { count: 0, receipt: "rating-proof" },
  } as SearchResult;
  expect(requestForResult(item)).toEqual({
    target: {
      origin: "foreign",
      sourceId: "source-1",
      presentationGeneration: 3,
      identifier: target.identifier,
      kind: "agent",
      versionPresent: false,
    },
    receipt: "rating-proof",
  });
  expect(
    requestForResult({
      ...item,
      live: { ...item.live, sourceId: "" },
    } as SearchResult),
  ).toBeUndefined();
});
it("binds local ratings to the immutable version revision and admitting release", () => {
  // Only the fields consumed by the projection are relevant to this unit.
  const item = {
    identifier: target.identifier,
    kind: target.kind,
    versionPresent: true,
    version: "1.0.0",
    type: "local",
    local: {
      resource: { id: target.resourceId },
      versionRevision: { id: target.versionRevisionId },
      release: { id: request.releaseId },
    },
  } as SearchResult;
  expect(requestForResult(item)).toEqual(request);
});
it.each([
  { versionPresent: false },
  { versionPresent: true, version: "" },
  { versionPresent: true, version: "2.0.0" },
])("preserves indexed advertised version identity: %j", (advertised) => {
  const item = {
    identifier: target.identifier,
    kind: target.kind,
    type: "indexed",
    ...advertised,
    indexed: {
      presentationSourceId: "source-2",
      presentationGeneration: 4,
    },
  } as SearchResult;
  expect(requestForResult(item)).toEqual({
    target: {
      origin: "foreign",
      sourceId: "source-2",
      presentationGeneration: 4,
      identifier: target.identifier,
      kind: target.kind,
      ...advertised,
    },
  });
});
it("pages owner ratings without requiring current resource visibility", async () => {
  const w = setup();
  w.get
    .mockResolvedValueOnce(reply({ items: [vote], nextCursor: "next" }))
    .mockResolvedValue(reply({ items: [] }));
  render(<OwnRatings {...w.props} />);
  await screen.findByRole("heading", { name: target.identifier });
  click("Next page");
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/ratings",
      expect.objectContaining({
        params: { query: { limit: 25, cursor: "next" } },
      }),
    ),
  );
  await screen.findByText(/Return to the first page or choose another state/);
  click("First page");
  await screen.findByText(/You have not rated any resources/);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/ratings",
    expect.objectContaining({ params: { query: { limit: 25 } } }),
  );
});
it("distinguishes absent and explicitly empty foreign versions in the owner inventory", async () => {
  const w = setup();
  const foreign = {
    origin: "foreign" as const,
    sourceId: "source-1",
    presentationGeneration: 3,
    identifier: target.identifier,
    kind: "agent" as const,
  };
  w.get.mockResolvedValue(
    reply({
      items: [
        {
          ...vote,
          id: "absent-version",
          target: { ...foreign, versionPresent: false },
        },
        {
          ...vote,
          id: "empty-version",
          target: { ...foreign, versionPresent: true, version: "" },
        },
      ],
    }),
  );
  render(<OwnRatings {...w.props} />);
  await screen.findByText(/No version supplied/);
  expect(screen.getByText(/Version: Empty version/)).toBeTruthy();
  expect(
    screen.getAllByRole("heading", { name: target.identifier }),
  ).toHaveLength(2);
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("moderates with a reason and never offers administrator score editing", async () => {
  const w = setup();
  w.get.mockResolvedValue(reply({ items: [vote] }));
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("heading", { name: target.identifier });
  expect(screen.queryByRole("radio")).toBeNull();
  const reason = moderationReason() as HTMLTextAreaElement;
  fireEvent.change(reason, { target: { value: "🙂".repeat(2000) + "a" } });
  expect(reason.validity.customError).toBe(true);
  expect(screen.getByRole("button", { name: "Remove rating" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.change(reason, { target: { value: "🙂".repeat(2000) } });
  expect(reason.validity.valid).toBe(true);
  expect(screen.getByRole("button", { name: "Remove rating" })).toHaveProperty(
    "disabled",
    false,
  );
  fireEvent.change(moderationReason(), {
    target: { value: "Confirmed abuse" },
  });
  click("Remove rating");
  expect(
    within(await screen.findByRole("dialog")).getByText(
      /cannot submit again until a moderator permits/,
    ),
  ).toBeTruthy();
  click("Confirm");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/{ratingId}:remove",
    expect.objectContaining({ body: { reason: "Confirmed abuse" } }),
  );
});
it("starts sharing off and requires an administrator reason and explicit confirmation", async () => {
  const w = setup();
  w.get.mockResolvedValue(
    reply({
      host: "catalog.example",
      audience: "anonymous",
      enabled: false,
      revision: 0,
    }),
  );
  w.post.mockResolvedValue(
    reply({
      host: "catalog.example",
      audience: "anonymous",
      enabled: true,
      revision: 1,
    }),
  );
  render(
    <RatingSharing
      {...w.props}
      access={administrator}
      host="catalog.example"
      audience="anonymous"
    />,
  );
  click("Rating summary sharing");
  const sharingState = await screen.findByText("Disabled", {
    selector: ".workspace-status",
  });
  expect(sharingState.getAttribute("data-tone")).toBe("neutral");
  expect(sharingState.parentElement?.textContent).toContain(
    "Audience opt-in: Disabled",
  );
  const reason = screen.getByLabelText("Sharing reason") as HTMLTextAreaElement;
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
  fireEvent.change(reason, { target: { value: "a".repeat(2001) } });
  expect(reason.validity.customError).toBe(true);
  expect(screen.getByRole("button", { name: "Enable sharing" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(w.post).not.toHaveBeenCalled();
  for (const type of ["beforeunload", "catalog-ui:leave"])
    expect(window.dispatchEvent(new Event(type, { cancelable: true }))).toBe(
      false,
    );
  fireEvent.change(reason, { target: { value: "" } });
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
  fireEvent.change(reason, { target: { value: "🙂".repeat(2000) + "a" } });
  expect(reason.validity.customError).toBe(true);
  expect(screen.getByRole("button", { name: "Enable sharing" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.change(reason, { target: { value: "🙂".repeat(2000) } });
  expect(reason.validity.valid).toBe(true);
  expect(screen.getByRole("button", { name: "Enable sharing" })).toHaveProperty(
    "disabled",
    false,
  );
  fireEvent.change(screen.getByLabelText("Sharing reason"), {
    target: { value: "Opt in to consented summaries" },
  });
  await confirmCommand("Enable sharing");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/sharing",
    expect.objectContaining({
      body: {
        host: "catalog.example",
        audience: "anonymous",
        enabled: true,
        reason: "Opt in to consented summaries",
      },
    }),
  );
});
it("refreshes live authority explicitly without losing the entered opinion", async () => {
  const foreign = {
    origin: "foreign" as const,
    sourceId: "source-1",
    presentationGeneration: 1,
    identifier: target.identifier,
    kind: "agent" as const,
    versionPresent: false,
  };
  const input = { target: foreign };
  const w = setup({ target: foreign, count: 0, changedEntry: false });
  const refresh = vi
    .fn<() => Promise<typeof input & { receipt?: string }>>()
    .mockResolvedValue(input);
  render(
    <RatingEntry {...w.props} request={input} live refreshProof={refresh} />,
  );
  await open();
  expect(screen.getByText(/did not supply a version/)).toBeTruthy();
  click("Refresh live ratings");
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  const freshRead = deferred<ReturnType<typeof reply>>();
  w.post.mockImplementation(async (_path, request) => {
    const body = (request as { body: { receipt?: string } }).body;
    return body.receipt === "fresh-proof"
      ? freshRead.promise
      : reply({ ...empty, target: foreign });
  });
  refresh.mockResolvedValue({ ...input, receipt: "fresh-proof" });
  click("Refresh live ratings");
  await waitFor(() =>
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/ratings/read",
      expect.objectContaining({ body: { ...input, receipt: "fresh-proof" } }),
    ),
  );
  await act(async () =>
    freshRead.resolve(reply({ ...empty, target: foreign })),
  );
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLFieldSetElement>("group", { name: "Your score" })
        .disabled,
    ).toBe(false),
  );
  fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
  fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
  refresh.mockRejectedValue(new Error("Controlled refresh failure"));
  click("Refresh live ratings");
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
  expect(
    screen.getByRole<HTMLInputElement>("radio", { name: "3 stars" }).checked,
  ).toBe(true);
  expect(
    screen.getByRole<HTMLInputElement>("checkbox", { name: /Allow my score/ })
      .checked,
  ).toBe(true);
  expect(w.post.mock.calls.some(([path]) => path === "/api/v1/ratings")).toBe(
    false,
  );
  expect(
    screen.getByText(
      /Ratings of external entries are excluded from published summaries/,
    ),
  ).toBeTruthy();
  await confirmScore("Submit rating", 3, true);
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings",
    expect.objectContaining({
      body: { ...input, receipt: "fresh-proof", score: 3, consent: true },
    }),
  );
});
it("cancels prepared commands without changing the current rating", async () => {
  const w = setup({ ...empty, count: 1, average: 4, own: vote });
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  await open();
  click("Update rating");
  click("Cancel");
  expect(
    w.post.mock.calls.filter(([path]) => path === "/api/v1/ratings"),
  ).toHaveLength(0);
});
it("withdraws from the owner inventory, filters states and closes a cancelled command", async () => {
  const w = setup();
  w.get.mockResolvedValue(reply({ items: [vote] }));
  render(<OwnRatings {...w.props} />);
  await screen.findByRole("heading", { name: target.identifier });
  fireEvent.change(screen.getByLabelText("Rating state"), {
    target: { value: "active" },
  });
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/ratings",
      expect.objectContaining({
        params: { query: { limit: 25, state: "active" } },
      }),
    ),
  );
  click("Withdraw rating");
  expect(
    within(await screen.findByRole("dialog"))
      .getByRole("button", { name: "Confirm" })
      .classList.contains("danger"),
  ).toBe(true);
  expect(
    within(await screen.findByRole("dialog")).getByText(
      /Clear your current score and sharing consent/,
    ),
  ).toBeTruthy();
  expect(w.post).not.toHaveBeenCalled();
  click("Cancel");
  await confirmCommand("Withdraw rating");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/{ratingId}:withdraw",
    expect.objectContaining({ body: {} }),
  );
});
it("permits a fresh opinion without selecting an administrator score", async () => {
  const w = setup();
  w.get.mockResolvedValue(
    reply({ items: [{ ...vote, state: "removed", score: 0, consent: false }] }),
  );
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("heading", { name: target.identifier });
  fireEvent.change(moderationReason(), {
    target: { value: "Allow a fresh opinion" },
  });
  click("Permit new submission");
  expect(
    within(await screen.findByRole("dialog"))
      .getByRole("button", { name: "Confirm" })
      .classList.contains("primary"),
  ).toBe(true);
  expect(
    within(await screen.findByRole("dialog")).getByText(
      /The old score stays withdrawn and sharing consent stays off/,
    ),
  ).toBeTruthy();
  expect(w.post).not.toHaveBeenCalled();
  click("Confirm");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/{ratingId}:permit",
    expect.objectContaining({ body: { reason: "Allow a fresh opinion" } }),
  );
  expect(screen.queryByRole("radio")).toBeNull();
});
it.each([true, false])(
  "does not fetch a prohibited %s inventory",
  async (own) => {
    const w = setup();
    render(
      <Ratings
        {...w.props}
        own={own}
        access={{
          ...w.props.access,
          actorType: "service",
          permissions: [],
          availablePermissions: [],
        }}
      />,
    );
    await screen.findByText(/You do not have access/);
    expect(w.get).not.toHaveBeenCalled();
  },
);
it("labels empty lists and rejects malformed individual records", async () => {
  const w = setup();
  w.get
    .mockResolvedValueOnce(reply({ items: [] }))
    .mockResolvedValue(
      reply({ items: [{ ...vote, state: "removed", score: 5 }] }),
    );
  render(<OwnRatings {...w.props} />);
  await screen.findByText(/You have not rated any resources/);
  w.get.mockResolvedValueOnce(reply({ items: [] }));
  fireEvent.change(screen.getByLabelText("Rating state"), {
    target: { value: "withdrawn" },
  });
  await screen.findByText(/No ratings match this state. Choose another state/);
  fireEvent.change(screen.getByLabelText("Rating state"), {
    target: { value: "removed" },
  });
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
});
it("disables audience sharing with the current ETag and supports cancellation", async () => {
  const w = setup();
  w.get.mockResolvedValue(
    reply({
      host: "catalog.example",
      audience: "anonymous",
      enabled: true,
      revision: 3,
    }),
  );
  w.post.mockResolvedValue(
    reply({
      host: "catalog.example",
      audience: "anonymous",
      enabled: false,
      revision: 4,
    }),
  );
  render(
    <RatingSharing
      {...w.props}
      access={administrator}
      host="catalog.example"
      audience="anonymous"
    />,
  );
  click("Rating summary sharing");
  const sharingState = await screen.findByText("Enabled", {
    selector: ".workspace-status",
  });
  expect(sharingState.getAttribute("data-tone")).toBe("neutral");
  expect(sharingState.parentElement?.textContent).toContain(
    "Audience opt-in: Enabled",
  );
  fireEvent.change(screen.getByLabelText("Sharing reason"), {
    target: { value: "Stop future disclosure" },
  });
  click("Disable sharing");
  click("Cancel");
  await confirmCommand("Disable sharing");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/sharing",
    expect.objectContaining({
      body: expect.objectContaining({ enabled: false }) as unknown,
      params: {
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"rev-3"',
        },
      },
    }),
  );
  click("Rating summary sharing");
  expect(screen.queryByLabelText("Sharing reason")).toBeNull();
});
it("hides audience controls without sharing permission", () => {
  const w = setup();
  render(
    <RatingSharing {...w.props} host="catalog.example" audience="anonymous" />,
  );
  expect(screen.queryByText("Rating summary sharing")).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
});
it.each([
  { ...empty, count: 1, average: 4, own: { ...vote, revision: 0 } },
  { ...empty, target: { ...target, origin: "unknown" } },
  { ...empty, target: { ...target, kind: "unknown" } },
  { ...empty, target: { ...target, version: 9 } },
  { ...empty, target: { ...target, resourceId: "" } },
  {
    ...empty,
    target: {
      ...target,
      origin: "foreign",
      sourceId: "s",
      presentationGeneration: 0,
    },
  },
  { ...empty, changedEntry: "no" },
])("rejects malformed targets and opinion envelopes", async (view) => {
  const w = setup(view);
  render(<RatingEntry {...w.props} request={request} />);
  click("Ratings for this version");
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
});

it("explains an empty moderation inbox without suggesting a submission", async () => {
  const w = setup();
  w.get.mockResolvedValue(reply({ items: [] }));
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByText("No ratings to moderate.");
  expect(screen.queryByLabelText("Moderation reason")).toBeNull();
  expect(
    screen.queryByText(/Open a resource version to leave a rating/),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["success", "failure"])(
  "aborts live proof refresh on dismissal and ignores its late %s",
  async (outcome) => {
    const foreign = {
      origin: "foreign" as const,
      sourceId: "source-1",
      presentationGeneration: 1,
      identifier: target.identifier,
      kind: "agent" as const,
      versionPresent: false,
    };
    const input = { target: foreign, receipt: "old-proof" };
    const w = setup({ target: foreign, count: 0, changedEntry: false });
    const pending = deferred<typeof input>();
    const refresh = vi.fn<(signal: AbortSignal) => Promise<typeof input>>(
      () => pending.promise,
    );
    render(
      <RatingEntry {...w.props} request={input} live refreshProof={refresh} />,
    );
    await open();
    click("Refresh live ratings");
    const signal = refresh.mock.calls[0]![0];
    click("Ratings for this version");
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "success")
        pending.resolve({ ...input, receipt: "late-proof" });
      else pending.reject(new Error("Controlled late refresh failure"));
    });
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(w.post).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(/proof refreshed/)).toBeNull();
  },
);

it("rejects a proof refresh for another source without replacing the opinion target", async () => {
  const foreign = {
    origin: "foreign" as const,
    sourceId: "source-1",
    presentationGeneration: 1,
    identifier: target.identifier,
    kind: "agent" as const,
    versionPresent: false,
  };
  const w = setup({ target: foreign, count: 0, changedEntry: false });
  const refresh = vi.fn().mockResolvedValue({
    target: { ...foreign, sourceId: "other-source" },
    receipt: "wrong-target-proof",
  });
  render(
    <RatingEntry
      {...w.props}
      request={{ target: foreign }}
      live
      refreshProof={refresh}
    />,
  );
  await open();
  fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
  click("Refresh live ratings");
  await screen.findByText(/Could not refresh live ratings/);
  expect(w.props.onFailure).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("radio", { name: "3 stars" })).toHaveProperty(
    "checked",
    true,
  );
  expect(w.post).toHaveBeenCalledTimes(1);
});

it("keeps issuer-bound own votes usable after a membership replacement", async () => {
  const w = setup({ ...empty, count: 1, average: 4, own: vote });
  render(
    <RatingEntry
      {...w.props}
      access={{ ...w.props.access, membershipId: "recreated-membership" }}
      request={request}
      namespaceId="ns-1"
    />,
  );
  await open();
  await confirmCommand("Withdraw rating");
  await screen.findByText("Rating withdrawn.");
  expect(
    w.post.mock.calls.find(
      ([path]) => path === "/api/v1/ratings/{ratingId}:withdraw",
    )?.[1],
  ).toMatchObject({
    params: { path: { ratingId: vote.id }, header: { "If-Match": '"rev-2"' } },
  });
});

it("prevents duplicate pending proof refresh and preserves the opinion across explicit serial reads", async () => {
  const foreign = {
    origin: "foreign" as const,
    sourceId: "source-1",
    presentationGeneration: 1,
    identifier: target.identifier,
    kind: "agent" as const,
    versionPresent: false,
  };
  const input = { target: foreign, receipt: "old-proof" };
  const w = setup({ target: foreign, count: 0, changedEntry: false });
  const older = deferred<typeof input>();
  const newer = deferred<typeof input>();
  const refresh = vi
    .fn<(signal: AbortSignal) => Promise<typeof input>>()
    .mockImplementationOnce(() => older.promise)
    .mockImplementationOnce(() => newer.promise);
  render(
    <RatingEntry {...w.props} request={input} live refreshProof={refresh} />,
  );
  await open();
  fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
  fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
  click("Refresh live ratings");
  const button = screen.getByRole("button", { name: "Refresh live ratings" });
  expect(button).toHaveProperty("disabled", true);
  expect(button.getAttribute("aria-busy")).toBe("true");
  click("Refresh live ratings");
  expect(refresh).toHaveBeenCalledOnce();
  expect(refresh.mock.calls[0]![0].aborted).toBe(false);
  await act(async () => older.resolve({ ...input, receipt: "first-proof" }));
  await screen.findByText(/Live ratings refreshed/);
  expect(button).toHaveProperty("disabled", false);
  click("Refresh live ratings");
  expect(refresh.mock.calls[0]![0].aborted).toBe(true);
  expect(refresh).toHaveBeenCalledTimes(2);
  await act(async () => newer.resolve({ ...input, receipt: "new-proof" }));
  await screen.findByText(/Live ratings refreshed/);
  await confirmCommand("Submit rating");
  expect(
    w.post.mock.calls.find(([path]) => path === "/api/v1/ratings")?.[1],
  ).toMatchObject({
    body: { receipt: "new-proof", score: 3, consent: true },
  });
  expect(screen.queryByRole("alert")).toBeNull();
});

it.each(["no-read", "read-only", "owner-withdraw", "service"])(
  "respects personal rating scope and identity: %s",
  async (mode) => {
    const w = setup({ ...empty, count: 1, average: 4, own: vote });
    w.get.mockResolvedValue(reply({ items: [vote] }));
    const access: Access = {
      ...w.props.access,
      permissions: [],
      availablePermissions: [],
      namespaceGrants: [],
      scopeEnforcement: true,
      scopes:
        mode === "no-read"
          ? ["ratings.write"]
          : mode === "read-only"
            ? ["catalog.read"]
            : ["catalog.read", "ratings.write"],
      actorType: mode === "service" ? "service" : "human",
    };
    render(<OwnRatings {...w.props} access={access} />);
    if (mode === "no-read" || mode === "service") {
      expect(screen.getByText(/You do not have access/)).toBeTruthy();
      expect(w.get).not.toHaveBeenCalled();
    } else {
      await screen.findByRole("heading", { name: target.identifier });
      const action = screen.getByRole("button", { name: "Withdraw rating" });
      expect(action).toHaveProperty("disabled", mode === "read-only");
      if (mode === "owner-withdraw") {
        expect(
          screen.getByText(
            /after losing access to its resource or permission to submit ratings/,
          ),
        ).toBeTruthy();
        await confirmCommand("Withdraw rating");
        expect(w.post).toHaveBeenCalledWith(
          "/api/v1/ratings/{ratingId}:withdraw",
          expect.objectContaining({
            body: {},
            params: {
              path: { ratingId: vote.id },
              header: {
                "If-Match": '"rev-2"',
                "Idempotency-Key": expect.any(String) as unknown,
              },
            },
          }),
        );
        expect(
          w.get.mock.calls.every(([path]) => path === "/api/v1/ratings"),
        ).toBe(true);
      }
    }
  },
);

it.each([false, true])(
  "keeps per-item owner withdrawal independent of ratings.own with write scope: %s",
  async (write) => {
    const w = setup({ ...empty, count: 1, average: 4, own: vote });
    const access: Access = {
      ...w.props.access,
      permissions: [],
      availablePermissions: [],
      namespaceGrants: [],
      scopeEnforcement: true,
      scopes: ["catalog.read", ...(write ? ["ratings.write" as const] : [])],
    };
    render(
      <RatingEntry
        {...w.props}
        access={access}
        request={request}
        namespaceId="ns-1"
      />,
    );
    await open();
    expect(screen.queryByRole("radio")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Withdraw rating" }),
    ).toHaveProperty("disabled", !write);
    if (write) await confirmCommand("Withdraw rating");
    else
      expect(
        w.post.mock.calls.every(([path]) => path === "/api/v1/ratings/read"),
      ).toBe(true);
    expect(w.get).not.toHaveBeenCalled();
  },
);

it("retains aggregate reads and separately granted moderation for service identities", async () => {
  const w = setup({ ...empty, count: 1, average: 4 });
  const service: Access = {
    ...administrator,
    actorType: "service",
    scopeEnforcement: true,
    scopes: ["catalog.read", "tenant.admin"],
  };
  const view = render(
    <RatingEntry
      {...w.props}
      access={service}
      request={request}
      namespaceId="ns-1"
    />,
  );
  await open();
  expect(screen.queryByRole("radio")).toBeNull();
  expect(screen.queryByRole("button", { name: "Withdraw rating" })).toBeNull();
  w.get.mockResolvedValue(reply({ items: [vote] }));
  view.rerender(<Ratings {...w.props} access={service} />);
  await screen.findByRole("heading", { name: target.identifier });
  fireEvent.change(moderationReason(), {
    target: { value: "Remove a misleading opinion" },
  });
  await confirmCommand("Remove rating");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/ratings/{ratingId}:remove",
    expect.objectContaining({
      body: { reason: "Remove a misleading opinion" },
    }),
  );
});

it("does not request aggregate or personal data without catalog.read scope", async () => {
  const w = setup();
  render(
    <RatingEntry
      {...w.props}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["ratings.write"],
      }}
      request={request}
      namespaceId="ns-1"
    />,
  );
  click("Ratings for this version");
  await screen.findByText(/Reading ratings requires catalog.read/);
  expect(w.post).not.toHaveBeenCalled();
});

it("distinguishes identical identifiers by revision and source before withdrawal", async () => {
  const w = setup();
  w.get.mockResolvedValue(
    reply({
      items: [
        vote,
        {
          ...vote,
          id: "rating-2",
          target: { ...target, versionRevisionId: "revision-2" },
        },
        {
          ...vote,
          id: "rating-3",
          target: {
            identifier: target.identifier,
            versionPresent: true,
            version: "1.0.0",
            kind: "plugin",
            origin: "foreign",
            sourceId: "source-1",
            presentationGeneration: 2,
          },
        },
        {
          ...vote,
          id: "rating-4",
          target: {
            identifier: target.identifier,
            versionPresent: true,
            version: "1.0.0",
            kind: "plugin",
            origin: "foreign",
            sourceId: "source-2",
            presentationGeneration: 3,
          },
        },
      ],
    }),
  );
  render(<OwnRatings {...w.props} />);
  await screen.findAllByRole("heading", { name: target.identifier });
  const rows = screen.getAllByRole("article");
  expect(rows).toHaveLength(4);
  expect(rows[0]?.textContent).toContain("revision-1");
  expect(rows[1]?.textContent).toContain("revision-2");
  expect(rows[2]?.textContent).toContain("source-1");
  expect(rows[3]?.textContent).toContain("source-2");
  fireEvent.click(
    within(rows[1]!).getByRole("button", { name: "Withdraw rating" }),
  );
  const dialog = screen.getByRole("dialog");
  expect(dialog.textContent).toContain("rating-2");
  expect(dialog.textContent).toContain("revision-2");
  expect(dialog.textContent).not.toContain("revision-1");
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["host", "audience"])(
  "refuses sharing evidence for another %s before preparing a command",
  async (field) => {
    const w = setup();
    w.get.mockResolvedValue(
      reply({
        host: "catalog.example",
        audience: "anonymous",
        enabled: true,
        revision: 3,
        [field]: field === "host" ? "other.example" : "authenticated",
      }),
    );
    render(
      <RatingSharing
        {...w.props}
        access={administrator}
        host="catalog.example"
        audience="anonymous"
      />,
    );
    click("Rating summary sharing");
    await waitFor(() => expect(w.props.onFailure).toHaveBeenCalledTimes(1));
    expect(
      screen.queryByRole("button", { name: /Enable sharing|Disable sharing/ }),
    ).toBeNull();
    expect(screen.queryByLabelText("Sharing reason")).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("protects a private moderation reason and keeps a known success through malformed readback", async () => {
  const w = setup();
  w.get.mockResolvedValue(reply({ items: [vote] }));
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findByRole("heading", { name: target.identifier });
  fireEvent.change(moderationReason(), {
    target: { value: "Exact private reason" },
  });
  const leave = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(leave);
  expect(leave.defaultPrevented).toBe(true);
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(false);
  expect(discard).toHaveBeenCalled();
  fireEvent.change(moderationReason(), {
    target: { value: "" },
  });
  const cleared = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(cleared);
  expect(cleared.defaultPrevented).toBe(false);
  fireEvent.change(moderationReason(), {
    target: { value: "Exact private reason" },
  });
  click("Remove rating");
  const dialog = within(screen.getByRole("dialog"));
  expect(dialog.getByLabelText("Command summary").textContent).toContain(
    "Exact private reason",
  );
  expect(dialog.getByLabelText("Command summary").textContent).toContain(
    vote.id,
  );
  expect(dialog.getByLabelText("Command summary").textContent).toContain(
    '"rev-2"',
  );
  w.get.mockResolvedValue(reply({ items: [{ bad: true }] }));
  fireEvent.click(dialog.getByRole("button", { name: "Confirm" }));
  await screen.findByText("Rating state updated.");
  await screen.findByRole("alert");
  expect(screen.queryByLabelText("Moderation reason")).toBeNull();
  const clean = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(clean);
  expect(clean.defaultPrevented).toBe(false);
  expect(
    w.post.mock.calls.filter(([path]) => path !== "/api/v1/ratings/read"),
  ).toHaveLength(1);
  w.get.mockResolvedValue(reply({ items: [] }));
  click("Reload");
  await screen.findByText("No ratings to moderate.");
  expect(screen.getByText("Rating state updated.")).toBeTruthy();
});

it.each(["save", "withdraw"])(
  "retains the %s receipt after a failed rating read and manual recovery",
  async (action) => {
    const w = setup({ ...empty, own: vote });
    render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
    await open();
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/ratings/read"
        ? reply({ invalid: true })
        : reply({ id: vote.id, revision: 3 }),
    );
    if (action === "save") {
      fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
      click("Update rating");
    } else click("Withdraw rating");
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await screen.findByText(
      action === "save" ? "Rating saved." : "Rating withdrawn.",
    );
    await screen.findByRole("alert");
    const count = w.post.mock.calls.filter(
      ([path]) => path !== "/api/v1/ratings/read",
    ).length;
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/ratings/read"
        ? reply(empty)
        : reply({ id: vote.id, revision: 3 }),
    );
    click("Reload");
    await screen.findByRole("group", { name: "Your score" });
    expect(
      w.post.mock.calls.filter(([path]) => path !== "/api/v1/ratings/read"),
    ).toHaveLength(count);
    expect(
      screen.getByText(
        action === "save" ? "Rating saved." : "Rating withdrawn.",
      ),
    ).toBeTruthy();
  },
);

it("keeps a sharing acknowledgement through failed readback and manual recovery", async () => {
  const w = setup();
  const sharing = {
    host: "catalog.example",
    audience: "anonymous" as const,
    enabled: false,
    revision: 0,
  };
  w.get.mockResolvedValue(reply(sharing));
  w.post.mockResolvedValue(reply({ ...sharing, enabled: true, revision: 1 }));
  render(
    <RatingSharing
      {...w.props}
      access={administrator}
      host={sharing.host}
      audience={sharing.audience}
    />,
  );
  click("Rating summary sharing");
  await screen.findByLabelText("Sharing reason");
  fireEvent.change(screen.getByLabelText("Sharing reason"), {
    target: { value: "Exact sharing choice" },
  });
  w.get.mockResolvedValue(reply({ invalid: true }));
  await confirmCommand("Enable sharing");
  await screen.findByText("Rating sharing choice saved.");
  await screen.findByRole("alert");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.post).toHaveBeenCalledOnce();
  w.get.mockResolvedValue(reply({ ...sharing, enabled: true, revision: 1 }));
  click("Reload");
  await screen.findByLabelText("Sharing reason");
  expect(screen.getByText("Rating sharing choice saved.")).toBeTruthy();
  expect(screen.getByLabelText("Sharing reason")).toHaveProperty("value", "");
  expect(w.post).toHaveBeenCalledOnce();
});

it.each([
  ["active", true, false],
  ["active", false, false],
  ["active", true, true],
  ["withdrawn", false, false],
  ["removed", false, true],
] as const)(
  "shows saved personal consent for %s, consent %s, foreign %s without target reads",
  async (state, consent, foreign) => {
    const w = setup();
    const own = {
      ...vote,
      state,
      consent,
      score: state === "active" ? 4 : 0,
      target: foreign
        ? {
            identifier: target.identifier,
            versionPresent: true,
            version: "1.0.0",
            kind: "agent",
            origin: "foreign",
            sourceId: "source-1",
            presentationGeneration: 2,
          }
        : target,
    };
    w.get.mockResolvedValue(reply({ items: [own] }));
    render(<OwnRatings {...w.props} />);
    await screen.findByRole("heading", { name: target.identifier });
    expect(
      screen.getByText("Saved sharing consent").nextElementSibling?.textContent,
    ).toBe(consent ? "Yes" : "No");
    expect(
      screen
        .getByText("Last rating update")
        .nextElementSibling?.querySelector("time")
        ?.getAttribute("datetime"),
    ).toBe(stamp);
    expect(screen.getByText(/does not prove publication/)).toBeTruthy();
    expect(w.get).toHaveBeenCalledOnce();
  },
);
it("refreshes a non-live target explicitly, retains its draft and shows last-loaded state after failure", async () => {
  const w = setup();
  const pending = deferred<ReturnType<typeof reply>>();
  w.post
    .mockResolvedValueOnce(reply(empty))
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValue(reply({ ...empty, count: 1, average: 4 }));
  render(<RatingEntry {...w.props} request={request} namespaceId="ns-1" />);
  await open();
  fireEvent.click(screen.getByRole("radio", { name: "4 stars" }));
  fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
  click("Refresh this rating");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Refresh this rating" }),
    ).toHaveProperty("disabled", true),
  );
  expect(screen.getByRole("radio", { name: "4 stars" })).toHaveProperty(
    "checked",
    true,
  );
  await act(async () => pending.reject(new Error("read failed")));
  await screen.findByRole("alert");
  expect(
    screen.getByText("Last loaded rating for this exact target"),
  ).toBeTruthy();
  expect(
    screen.getByRole("radio", { name: "4 stars" }).matches(":disabled"),
  ).toBe(true);
  click("Refresh this rating");
  await screen.findByText(/4.0 \/ 5/);
  expect(screen.getByRole("radio", { name: "4 stars" })).toHaveProperty(
    "checked",
    true,
  );
  expect(
    screen.getByRole("checkbox", { name: /Allow my score/ }),
  ).toHaveProperty("checked", true);
  expect(w.post.mock.calls).toHaveLength(3);
  for (const [path, options] of w.post.mock.calls) {
    expect(path).toBe("/api/v1/ratings/read");
    expect(options).toMatchObject({ body: request });
  }
});

it("refreshes My ratings without target lookups and keeps the last loaded page on failure", async () => {
  const w = screenWorkspace();
  const pending = deferred<ReturnType<typeof reply>>();
  w.get
    .mockResolvedValueOnce(reply({ items: [vote] }))
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValue(reply({ items: [] }));
  render(<OwnRatings {...w.props} id="" />);
  await screen.findByRole("heading", { name: target.identifier });
  click("Refresh ratings");
  await act(async () => pending.reject(new Error("read failed")));
  await screen.findByRole("alert");
  expect(screen.getByRole("heading", { name: target.identifier })).toBeTruthy();
  expect(screen.getByText("Last loaded own ratings page")).toBeTruthy();
  click("Refresh ratings");
  await screen.findByText(/You have not rated any resources/);
  expect(w.get).toHaveBeenCalledTimes(3);
  for (const [path] of w.get.mock.calls) expect(path).toBe("/api/v1/ratings");
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps the selected moderation target when switching is declined, then binds a newer revision without losing its reason", async () => {
  const w = screenWorkspace();
  const first = { ...vote, target: { ...target, version: "" } };
  const second = {
    ...vote,
    id: "rating-2",
    target: {
      origin: "foreign",
      sourceId: "source-1",
      presentationGeneration: 1,
      identifier: target.identifier,
      kind: "agent" as const,
      versionPresent: false,
    },
  };
  let current = first;
  w.get.mockImplementation(async () => reply({ items: [current, second] }));
  render(<Ratings {...w.props} access={administrator} />);
  await screen.findAllByRole("button", { name: "Remove rating" });
  fireEvent.click(screen.getAllByRole("button", { name: "Remove rating" })[0]!);
  const panel = screen.getByRole("region", {
    name: "Selected rating moderation",
  });
  expect(panel.textContent).toContain("Empty version");
  fireEvent.submit(panel.querySelector("form")!);
  expect(screen.queryByRole("dialog")).toBeNull();
  fireEvent.change(screen.getByLabelText("Moderation reason"), {
    target: { value: "Retain exact first target reason" },
  });
  const decline = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getAllByRole("button", { name: "Remove rating" }).at(-1)!,
  );
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Retain exact first target reason",
  );
  expect(panel.textContent).toContain("Empty version");
  current = { ...first, revision: 3 };
  click("Refresh ratings");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  const fresh = screen
    .getAllByRole("article")
    .find((row) => row.textContent?.includes("Empty version"))!;
  fireEvent.click(within(fresh).getByRole("button", { name: "Remove rating" }));
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "Retain exact first target reason",
  );
  expect(panel.textContent).toContain("Revision 3");
  decline.mockReturnValue(true);
  const other = screen
    .getAllByRole("article")
    .find((row) => row.textContent?.includes("No version supplied"))!;
  fireEvent.click(within(other).getByRole("button", { name: "Remove rating" }));
  expect(panel.textContent).toContain("No version supplied");
  expect(screen.getByLabelText("Moderation reason")).toHaveProperty(
    "value",
    "",
  );
  expect(w.post).not.toHaveBeenCalled();
});
