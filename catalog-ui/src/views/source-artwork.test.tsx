import { testAccess } from "../../tests/unit/access-fixtures";
import { deferred } from "../../tests/unit/fixtures";
// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  act,
  within,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { SourceArtworkControls } from "./SourceArtwork";
import {
  administrator,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
screenLifecycle("/unit/indexed-record");
const id = "00000000-0000-7000-8000-000000000001";
const target = {
  sourceId: id,
  presentationGeneration: 2,
  identifier: "urn:air:example.test:team:agent",
  versionPresent: true,
  version: "1.0",
  kind: "agent",
};
const item = {
  type: "indexed",
  ...target,
  indexed: { presentationSourceId: id, presentationGeneration: 2 },
};
const head = {
  id,
  revision: 1,
  target,
  icon: { mode: "inherit" },
  screenshots: { mode: "inherit" },
  reason: "Earlier choice",
  createdAt: "2026-10-01T12:00:00Z",
};
it("preserves explicit empty-version artwork identity when the Catalog omits its empty JSON field", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({ ...head, target: { ...target, version: undefined } }),
  );
  render(
    <SourceArtworkControls
      {...w.props}
      access={administrator}
      item={{ ...item, version: undefined, versionPresent: true }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  expect(w.get.mock.calls[0]?.[1]).toMatchObject({
    params: { query: { versionPresent: true, version: "" } },
  });
  expect(w.post).not.toHaveBeenCalled();
});

it("creates a private override from missing head and captures target, modes and reason", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue({
    error: {},
    response: new Response(null, { status: 404 }),
  });
  w.post.mockResolvedValue(etagReply(head));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  fireEvent.change(screen.getByLabelText("Icon behavior"), {
    target: { value: "suppress" },
  });
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Do not show this source image" },
  });
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Save local artwork override" })
      .closest("form")!,
  );
  await act(async () => {
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    );
  });
  await waitFor(() => expect(w.post).toHaveBeenCalledOnce());
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/federation/sources/{sourceId}/artwork",
    {
      params: { path: { sourceId: id } },
      body: {
        target,
        icon: { mode: "suppress" },
        screenshots: { mode: "inherit" },
        reason: "Do not show this source image",
      },
    },
  ]);
  expect(
    (
      w.post.mock.calls[0]?.[1] as {
        params: { header: Record<string, string> };
      }
    ).params.header,
  ).not.toHaveProperty("If-Match");
});
it("updates with the head ETag and keeps live proof in POST only", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply(
      {
        ...head,
        icon: { mode: "suppress" },
        screenshots: { mode: "suppress" },
      },
      '"artwork-1"',
    ),
  );
  w.post.mockResolvedValue(etagReply({ ...head, revision: 2 }));
  const live = {
    ...item,
    type: "live",
    indexed: undefined,
    live: { sourceId: id, presentationGeneration: 2 },
    artwork: { receipt: "private-proof" },
  };
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={live} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  expect(screen.getByLabelText("Icon behavior")).toHaveProperty(
    "value",
    "suppress",
  );
  expect(screen.getByLabelText("Screenshot behavior")).toHaveProperty(
    "value",
    "suppress",
  );
  expect(screen.getByText(/Earlier overrides stay in history/)).toBeTruthy();
  expect(
    (w.get.mock.calls[0]?.[1] as { params: { query: Record<string, unknown> } })
      .params.query,
  ).not.toHaveProperty("receipt");
  fireEvent.click(
    screen.getByRole("button", { name: "Reset both choices to inherit" }),
  );
  expect(screen.getByLabelText("Icon behavior")).toHaveProperty(
    "value",
    "inherit",
  );
  expect(screen.getByLabelText("Screenshot behavior")).toHaveProperty(
    "value",
    "inherit",
  );
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Return to publisher artwork" },
  });
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Save local artwork override" })
      .closest("form")!,
  );
  expect(w.post).not.toHaveBeenCalled();
  await act(async () => {
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    );
  });
  await waitFor(() => expect(w.post).toHaveBeenCalledOnce());
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    params: { header: { "If-Match": '"artwork-1"' } },
    body: {
      receipt: "private-proof",
      icon: { mode: "inherit" },
      screenshots: { mode: "inherit" },
    },
  });
});
it("requires replacement content and preserves the form for correction", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(head));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  fireEvent.change(screen.getByLabelText("Icon behavior"), {
    target: { value: "replace" },
  });
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Save local artwork override" })
      .closest("form")!,
  );
  await screen.findByText("Add the replacement icon first.");
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Add icon" }));
  await screen.findByLabelText("Icon HTTPS URL");
});
it("loads bounded history only on explicit disclosure", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply(head))
    .mockResolvedValue(etagReply({ items: [head], nextCursor: "next-page" }));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  expect(w.get).toHaveBeenCalledOnce();
  const disclosure = screen
    .getByText("Artwork override history")
    .closest("details")!;
  disclosure.open = true;
  fireEvent(disclosure, new Event("toggle"));
  await screen.findByText("Artwork revision 1");
  expect(w.get.mock.calls[1]).toMatchObject([
    "/api/v1/federation/sources/{sourceId}/artwork/history",
    { params: { query: { limit: 25, presentationGeneration: 2 } } },
  ]);
});
it("preserves a dirty artwork override when closing or changing contribution is declined", async () => {
  const alternate = "00000000-0000-7000-8000-000000000002";
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(head));
  render(
    <SourceArtworkControls
      {...w.props}
      access={administrator}
      item={{
        ...item,
        indexed: { ...item.indexed, sourceIds: [id, alternate] },
      }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Keep this unsaved decision" },
  });
  fireEvent.change(screen.getByLabelText("Icon behavior"), {
    target: { value: "suppress" },
  });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(screen.getByRole("button", { name: "Close artwork editor" }));
  fireEvent.change(screen.getByLabelText("Artwork contribution source"), {
    target: { value: alternate },
  });
  expect(discard).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Artwork contribution source")).toHaveProperty(
    "value",
    id,
  );
  expect(screen.getByLabelText("Artwork override reason")).toHaveProperty(
    "value",
    "Keep this unsaved decision",
  );
  expect(screen.getByLabelText("Icon behavior")).toHaveProperty(
    "value",
    "suppress",
  );
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
  discard.mockReturnValue(true);
  fireEvent.click(screen.getByRole("button", { name: "Close artwork editor" }));
  expect(screen.queryByLabelText("Artwork override reason")).toBeNull();
  expect(w.get).toHaveBeenCalledOnce();
});
it.each(["unavailable", "malformed"])(
  "keeps the artwork editor closed after a %s head read and recovers explicitly",
  async (mode) => {
    const w = screenWorkspace();
    w.get
      .mockResolvedValueOnce(
        mode === "unavailable"
          ? { error: {}, response: new Response(null, { status: 503 }) }
          : etagReply({ ...head, revision: 0 }),
      )
      .mockResolvedValue(etagReply(head));
    render(
      <SourceArtworkControls {...w.props} access={administrator} item={item} />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Manage local artwork" }),
    );
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Icon behavior")).toBeNull();
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await screen.findByLabelText("Icon behavior");
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.get.mock.calls[1]?.[1]).toMatchObject({
      params: {
        path: { sourceId: id },
        query: {
          presentationGeneration: 2,
          identifier: target.identifier,
          versionPresent: true,
          version: "1.0",
          kind: "agent",
        },
      },
    });
    expect(w.post).not.toHaveBeenCalled();
  },
);
it("requires the existing artwork head ETag before allowing an update", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(head, null));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  expect(
    screen.getByRole("button", { name: "Save local artwork override" }),
  ).toHaveProperty("disabled", true);
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
});
it.each([true, false, undefined])(
  "edits an actual alternate contribution independently of publisher artwork opt-in (%s)",
  async (artworkEnabled) => {
    const alternate = "00000000-0000-7000-8000-000000000002";
    const w = screenWorkspace();
    w.get.mockImplementation(async (path, options) =>
      path === "/api/v1/federation/sources/{sourceId}"
        ? etagReply({
            id: alternate,
            presentationGeneration: 7,
            artworkEnabled,
          })
        : etagReply({
            ...head,
            target: {
              ...target,
              sourceId: (options as { params: { path: { sourceId: string } } })
                .params.path.sourceId,
              presentationGeneration: (
                options as {
                  params: { query: { presentationGeneration: number } };
                }
              ).params.query.presentationGeneration,
            },
          }),
    );
    render(
      <SourceArtworkControls
        {...w.props}
        access={administrator}
        item={{
          ...item,
          indexed: { ...item.indexed, sourceIds: [id, alternate] },
        }}
      />,
    );
    expect(w.get).not.toHaveBeenCalled();
    fireEvent.click(
      screen.getByRole("button", { name: "Manage local artwork" }),
    );
    await screen.findByLabelText("Icon behavior");
    expect(w.get).toHaveBeenCalledOnce();
    fireEvent.change(screen.getByLabelText("Artwork contribution source"), {
      target: { value: alternate },
    });
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
    expect(w.get.mock.calls[2]).toMatchObject([
      "/api/v1/federation/sources/{sourceId}/artwork",
      {
        params: {
          path: { sourceId: alternate },
          query: { presentationGeneration: 7 },
        },
      },
    ]);
    expect(
      screen.getByText(/does not change which source supplies/),
    ).toBeTruthy();
    expect(
      screen
        .getAllByRole("option")
        .filter((option) => option.textContent?.includes("contribution")),
    ).toHaveLength(1);
    w.post.mockResolvedValue(
      etagReply({
        ...head,
        target: { ...target, sourceId: alternate, presentationGeneration: 7 },
      }),
    );
    fireEvent.change(screen.getByLabelText("Icon behavior"), {
      target: { value: "suppress" },
    });
    fireEvent.change(screen.getByLabelText("Artwork override reason"), {
      target: { value: "Use only tenant-selected artwork" },
    });
    fireEvent.submit(
      screen
        .getByRole("button", { name: "Save local artwork override" })
        .closest("form")!,
    );
    await act(async () => {
      fireEvent.click(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: "Confirm",
        }),
      );
    });
    await waitFor(() => expect(w.post).toHaveBeenCalledOnce());
    expect(w.post.mock.calls[0]).toMatchObject([
      "/api/v1/federation/sources/{sourceId}/artwork",
      {
        params: { path: { sourceId: alternate } },
        body: {
          target: { ...target, sourceId: alternate, presentationGeneration: 7 },
          icon: { mode: "suppress" },
          screenshots: { mode: "inherit" },
        },
      },
    ]);
    expect(w.get).toHaveBeenCalledTimes(3);
    fireEvent.click(
      screen.getByRole("button", { name: "Refresh artwork override" }),
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(4));
    expect(w.get.mock.calls[3]).toMatchObject([
      "/api/v1/federation/sources/{sourceId}/artwork",
      {
        params: {
          path: { sourceId: alternate },
          query: { presentationGeneration: 7 },
        },
      },
    ]);
  },
);
it("preserves sealed replacement inputs for editing and submits only author fields", async () => {
  const w = screenWorkspace();
  const replacement = {
    url: "https://images.example/shot.png",
    alt: "Screenshot",
    rights: { basis: "permission", attribution: "Example team" },
    digest: `sha256:${"a".repeat(64)}`,
    mediaType: "image/png",
    size: 10,
    width: 1,
    height: 1,
    rightsAssessment: "not_assessed",
  };
  w.get.mockResolvedValue(
    etagReply({
      ...head,
      icon: { mode: "replace", asset: replacement },
      screenshots: { mode: "replace", assets: [replacement] },
    }),
  );
  w.post.mockResolvedValue(etagReply(head));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon HTTPS URL");
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Save local artwork override" })
      .closest("form")!,
  );
  await screen.findByText("Give a reason of up to 2000 characters.");
  fireEvent.change(screen.getByLabelText("Screenshot 1 alternative text"), {
    target: { value: "New screenshot text" },
  });
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Update alt text" },
  });
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Save local artwork override" })
      .closest("form")!,
  );
  await act(async () =>
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    ),
  );
  await waitFor(() => expect(w.post).toHaveBeenCalledOnce());
  const body = (
    w.post.mock.calls[0]?.[1] as {
      body: {
        icon: { asset: Record<string, unknown> };
        screenshots: { assets: Record<string, unknown>[] };
      };
    }
  ).body;
  expect(body.icon.asset).not.toHaveProperty("digest");
  expect(body.screenshots.assets[0]).toHaveProperty(
    "alt",
    "New screenshot text",
  );
});
it("requires screenshots for replacement and pages an empty explicit history", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(head));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Screenshot behavior");
  fireEvent.change(screen.getByLabelText("Screenshot behavior"), {
    target: { value: "replace" },
  });
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Save local artwork override" })
      .closest("form")!,
  );
  await screen.findByText("Add replacement screenshots first.");
  w.get.mockResolvedValue(etagReply({ items: [], nextCursor: "page-two" }));
  const details = screen
    .getByText("Artwork override history")
    .closest("details")!;
  details.open = true;
  fireEvent(details, new Event("toggle"));
  await screen.findByText("No local artwork history.");
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "page-two" } },
    }),
  );
});

it("lets a source reader inspect exact retained head/history without source discovery or edit authority", async () => {
  const w = screenWorkspace();
  const access = {
    ...administrator,
    ...testAccess([]),
    permissions: ["source.read"] as const,
    availablePermissions: ["source.read"] as const,
    scopeEnforcement: true,
    scopes: ["sources.read"] as const,
  };
  w.get.mockImplementation(async (path) =>
    etagReply(
      typeof path === "string" && path.endsWith("/history")
        ? { items: [head] }
        : head,
    ),
  );
  const view = render(
    <SourceArtworkControls
      {...w.props}
      access={{
        ...access,
        permissions: [...access.permissions],
        availablePermissions: [...access.availablePermissions],
        scopes: [...access.scopes],
      }}
      item={item}
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Inspect local artwork" }),
  );
  await screen.findByText("Current artwork override");
  expect(screen.queryByLabelText("Icon behavior")).toBeNull();
  expect(w.get).toHaveBeenCalledOnce();
  const history = screen
    .getByText("Artwork override history")
    .closest("details")!;
  history.open = true;
  fireEvent(history, new Event("toggle"));
  await screen.findByText("Artwork revision 1");
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/federation/sources/{sourceId}/artwork",
    "/api/v1/federation/sources/{sourceId}/artwork/history",
  ]);
  expect(w.post).not.toHaveBeenCalled();
  view.rerender(
    <SourceArtworkControls
      {...w.props}
      access={{ ...administrator, ...testAccess([]) }}
      item={item}
    />,
  );
  expect(screen.queryByText("Current artwork override")).toBeNull();
});
it("makes no artwork GETs when a manager lacks the source-read OAuth ceiling", async () => {
  const w = screenWorkspace();
  render(
    <SourceArtworkControls
      {...w.props}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["sources.manage"],
      }}
      item={item}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  expect(screen.getByText(/No evidence was requested/)).toBeTruthy();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});
it.each([
  ["identity", "success"],
  ["identity", "failure"],
  ["target", "success"],
  ["target", "failure"],
  ["scope", "success"],
  ["scope", "failure"],
] as const)(
  "discards a pending artwork head after %s changes and late %s",
  async (change, outcome) => {
    const w = screenWorkspace();
    const pending = deferred<ReturnType<typeof etagReply>>();
    w.get.mockReturnValue(pending.promise);
    const view = render(
      <SourceArtworkControls {...w.props} access={administrator} item={item} />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Manage local artwork" }),
    );
    const signal = (w.get.mock.calls[0]?.[1] as { signal: AbortSignal }).signal;
    view.rerender(
      <SourceArtworkControls
        {...w.props}
        access={
          change === "identity"
            ? { ...administrator, subject: "other-user" }
            : change === "scope"
              ? {
                  ...administrator,
                  scopeEnforcement: true,
                  scopes: ["sources.manage"],
                }
              : administrator
        }
        item={
          change === "target"
            ? { ...item, identifier: "urn:air:example.test:team:other" }
            : item
        }
      />,
    );
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "success") pending.resolve(etagReply(head));
      else pending.reject(new Error("Controlled late artwork failure"));
    });
    expect(screen.queryByText("Current artwork override")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Manage local artwork" }),
    ).toBeTruthy();
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("preserves the exact artwork command after a successful reply for another target", async () => {
  const w = screenWorkspace();
  const saved = {
    ...head,
    id: "00000000-0000-7000-8000-000000000003",
    revision: 2,
    icon: { mode: "suppress" },
    reason: "Hide this icon",
  };
  w.get
    .mockResolvedValue(etagReply(saved, '"artwork-2"'))
    .mockResolvedValueOnce(etagReply(head, '"artwork-1"'));
  w.post
    .mockResolvedValueOnce(
      etagReply({
        ...saved,
        target: { ...target, sourceId: "00000000-0000-7000-8000-000000000002" },
      }),
    )
    .mockResolvedValueOnce(etagReply(saved));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  fireEvent.change(screen.getByLabelText("Icon behavior"), {
    target: { value: "suppress" },
  });
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Hide this icon" },
  });
  await confirmCommand("Save local artwork override");
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("alert").textContent).toContain(
    "outcome is unknown",
  );
  expect(
    within(dialog).getByRole("button", { name: "Back to form" }),
  ).toHaveProperty("disabled", true);
  expect(w.get).toHaveBeenCalledOnce();
  expect(w.post).toHaveBeenCalledOnce();
  expect(w.props.onFailure).toHaveBeenCalledTimes(1);
  fireEvent.click(
    within(dialog).getByRole("button", { name: "Retry same command" }),
  );
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(w.post.mock.calls).toHaveLength(2);
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    params: {
      header: {
        "If-Match": '"artwork-1"',
        "Idempotency-Key": expect.any(String) as unknown,
      },
    },
    body: {
      target,
      icon: { mode: "suppress" },
      screenshots: { mode: "inherit" },
      reason: "Hide this icon",
    },
  });
  expect(
    screen.getByText(/Refresh the override head and history here/),
  ).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh artwork override" }),
  );
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  await waitFor(() =>
    expect(screen.getByLabelText("Icon behavior")).toHaveProperty(
      "value",
      "suppress",
    ),
  );
});

it("rejects a later history page for another target without displaying its evidence", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply(head))
    .mockResolvedValueOnce(etagReply({ items: [head], nextCursor: "page-two" }))
    .mockResolvedValueOnce(
      etagReply({
        items: [
          {
            ...head,
            revision: 2,
            target: { ...target, presentationGeneration: 3 },
          },
        ],
      }),
    );
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  const details = screen
    .getByText("Artwork override history")
    .closest("details")!;
  details.open = true;
  fireEvent(details, new Event("toggle"));
  await screen.findByText("Artwork revision 1");
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  expect(w.get.mock.calls[2]?.[1]).toMatchObject({
    params: {
      query: { limit: 25, cursor: "page-two", presentationGeneration: 2 },
    },
  });
  expect(screen.queryByText("Artwork revision 2")).toBeNull();
  expect(w.props.onFailure).toHaveBeenCalledOnce();
  expect(w.post).not.toHaveBeenCalled();
});
it("rejects artwork evidence returned for a different exact target", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({
      ...head,
      target: { ...target, identifier: "urn:air:other.test:team:agent" },
    }),
  );
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByRole("alert");
  expect(screen.queryByText("Current artwork override")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it.each([false, true])(
  "preserves version presence when the API omits an empty version string (%s)",
  async (versionPresent) => {
    const w = screenWorkspace();
    const received = {
      ...head,
      target: { ...target, versionPresent, version: undefined },
    };
    w.get.mockResolvedValue(etagReply(received));
    render(
      <SourceArtworkControls
        {...w.props}
        access={administrator}
        item={{
          ...item,
          versionPresent,
          ...(versionPresent ? { version: "" } : { version: undefined }),
        }}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Manage local artwork" }),
    );
    await screen.findByLabelText("Icon behavior");
    expect(w.get.mock.calls[0]?.[1]).toMatchObject({
      params: {
        query: { versionPresent, ...(versionPresent ? { version: "" } : {}) },
      },
    });
    if (!versionPresent)
      expect(
        (
          w.get.mock.calls[0]?.[1] as {
            params: { query: Record<string, unknown> };
          }
        ).params.query,
      ).not.toHaveProperty("version");
    expect(screen.getByText("Current artwork override")).toBeTruthy();
    expect(w.post).not.toHaveBeenCalled();
  },
);
it("rejects a null version in retained artwork instead of treating it as an empty version", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({
      ...head,
      target: { ...target, versionPresent: true, version: null },
    }),
  );
  render(
    <SourceArtworkControls
      {...w.props}
      access={administrator}
      item={{ ...item, version: "" }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByRole("alert");
  expect(screen.queryByText("Current artwork override")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("does not conflate absent and present-empty versions in returned artwork evidence", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({
      ...head,
      target: { ...target, versionPresent: false, version: undefined },
    }),
  );
  render(
    <SourceArtworkControls
      {...w.props}
      access={administrator}
      item={{ ...item, versionPresent: true, version: "" }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByRole("alert");
  expect(screen.queryByText("Current artwork override")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("retains artwork acknowledgement across explicit failed head/history refresh and later recovery", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/history") ? { items: [head] } : head,
      '"artwork-1"',
    ),
  );
  w.post.mockResolvedValue(etagReply(head));
  render(
    <SourceArtworkControls {...w.props} access={administrator} item={item} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Manage local artwork" }));
  await screen.findByLabelText("Icon behavior");
  fireEvent.click(screen.getByText("Artwork override history"));
  await screen.findByText("Artwork revision 1");
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Preserve success evidence" },
  });
  vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh artwork override" }),
  );
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Artwork override reason")).toHaveProperty(
    "value",
    "Preserve success evidence",
  );
  await confirmCommand("Save local artwork override");
  await screen.findByText(/Refresh the override head and history here/);
  expect(screen.getAllByRole("status")).toHaveLength(1);
  expect(document.activeElement).toBe(screen.getByRole("status"));
  const originalReads = w.get.mock.calls.length;
  expect(originalReads).toBe(2);
  w.get.mockResolvedValue(etagReply({ malformed: true }));
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh artwork override" }),
  );
  await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(2));
  expect(
    screen.getByText(/Refresh the override head and history here/),
  ).toBeTruthy();
  expect(w.post).toHaveBeenCalledOnce();
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/history") ? { items: [head] } : head,
      '"artwork-1"',
    ),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh artwork override" }),
  );
  await screen.findByLabelText("Icon behavior");
  await screen.findByText("Artwork revision 1");
  expect(
    screen.getByText(/Refresh the override head and history here/),
  ).toBeTruthy();
  expect(w.post).toHaveBeenCalledOnce();
  const previousReceipt = screen.getByRole("status");
  fireEvent.change(screen.getByLabelText("Artwork override reason"), {
    target: { value: "Second artwork decision" },
  });
  screen.getByRole("button", { name: "Save local artwork override" }).focus();
  await confirmCommand("Save local artwork override");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(screen.getAllByRole("status")).toHaveLength(1);
  expect(previousReceipt.isConnected).toBe(false);
  await waitFor(() =>
    expect(document.activeElement).toBe(screen.getByRole("status")),
  );
});
