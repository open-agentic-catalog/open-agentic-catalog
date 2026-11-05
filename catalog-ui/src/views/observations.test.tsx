import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { Observations, ObservationGroup } from "./Observations";
import { APIError } from "../api/client";
import type { components } from "../api/schema";
import { deferred, reader } from "../../tests/unit/fixtures";
import {
  change,
  click,
  confirmCommand,
  etagReply,
  namespace,
  publisher,
  resource,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/observations");
const sourceAdmin = {
  ...reader,
  ...testAccess(["reader", "source_admin"]),
};
const contributor = {
  ...sourceAdmin,
  availablePermissions: [
    ...new Set([
      ...sourceAdmin.availablePermissions,
      ...testPermissions(["contributor"]),
    ]),
  ],
  namespaceGrants: [
    {
      namespaceId: namespace.id,
      permissions: testPermissions(["contributor" as const]),
    },
  ],
};
const observation: components["schemas"]["Observation"] = {
  id: "observation-1",
  tenantId: reader.tenantId,
  sourceId: "source-1",
  sourceKey: "external-agent",
  identifier: "urn:air:example.com:agents:helper",
  versionPresent: false,
  completeness: "complete",
  freshness: "current",
  rawEntryAvailable: true,
  exactEntryDigest: "exact-entry",
  identityDigest: "identity",
  compatibility: {
    structural: "valid",
    profileId: "ard",
    support: "supported",
  },
  firstSeenAt: stamp,
  lastSeenAt: stamp,
};
const occurrence: components["schemas"]["ObservationOccurrence"] = {
  observationId: observation.id,
  sourceId: observation.sourceId,
  sourceRevision: 1,
  snapshotId: "snapshot-1",
  retrievalDigest: "retrieval-1",
  entryLocation: "/entries/0",
  exactEntryDigest: observation.exactEntryDigest,
  evidenceComplete: true,
  observedAt: stamp,
};

function adoptionSetup() {
  const setup = screenWorkspace();
  readReplies(setup.get, {
    "/api/v1/observations/{observationId}/occurrences": { items: [occurrence] },
    "/api/v1/namespaces": {
      items: [
        namespace,
        { ...namespace, id: "other-ns", displayName: "Other namespace" },
      ],
    },
    "/api/v1/publishers": { items: [publisher] },
  });
  return setup;
}
async function selectOccurrence() {
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Use this occurrence for adoption",
    }),
  );
  await screen.findByRole("option", {
    name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
  });
}

it.each([false, true])(
  "clears private raw evidence on permission loss without reading again on restoration, pending=%s",
  async (pending) => {
    const w = adoptionSetup();
    const original = w.get.getMockImplementation()!;
    const raw = deferred<ReturnType<typeof etagReply>>();
    w.get.mockImplementation(async (path, options) =>
      path === "/api/v1/observations/{observationId}/raw"
        ? pending
          ? raw.promise
          : etagReply('{"private":"retained entry"}')
        : original(path, options),
    );
    const view = render(
      <Observations {...w.props} id={observation.id} access={contributor} />,
    );
    await screen.findByRole("button", { name: "Read exact raw entry" });
    click("Read exact raw entry");
    if (!pending)
      await screen.findByRole("heading", { name: "Exact raw entry" });
    await waitFor(() =>
      expect(
        w.get.mock.calls.filter(
          ([path]) => path === "/api/v1/observations/{observationId}/raw",
        ),
      ).toHaveLength(1),
    );
    const rawOptions = w.get.mock.calls.find(
      ([path]) => path === "/api/v1/observations/{observationId}/raw",
    )?.[1] as { signal: AbortSignal };
    view.rerender(
      <Observations
        {...w.props}
        id={observation.id}
        access={{
          ...contributor,
          permissions: contributor.permissions.filter(
            (permission) => permission !== "source.read_evidence",
          ),
        }}
      />,
    );
    expect(
      screen.queryByRole("heading", { name: "Exact raw entry" }),
    ).toBeNull();
    expect(rawOptions.signal.aborted).toBe(true);
    if (pending)
      await act(async () =>
        raw.resolve(etagReply('{"private":"retained entry"}')),
      );
    expect(screen.queryByText(/retained entry/)).toBeNull();
    view.rerender(
      <Observations {...w.props} id={observation.id} access={contributor} />,
    );
    expect(
      screen.queryByRole("heading", { name: "Exact raw entry" }),
    ).toBeNull();
    expect(
      w.get.mock.calls.filter(
        ([path]) => path === "/api/v1/observations/{observationId}/raw",
      ),
    ).toHaveLength(1);
    click("Read exact raw entry");
    await screen.findByRole("heading", { name: "Exact raw entry" });
    expect(
      w.get.mock.calls.filter(
        ([path]) => path === "/api/v1/observations/{observationId}/raw",
      ),
    ).toHaveLength(2);
    expect(w.post).not.toHaveBeenCalled();
  },
);

function selectDestination() {
  change("Destination namespace", namespace.id);
  change("Local publisher", publisher.id);
}

it.each(["list", "occurrences"] as const)(
  "recovers %s metadata pages while preserving applied selection and avoiding evidence reads",
  async (view) => {
    const path =
      view === "list"
        ? "/api/v1/observations"
        : "/api/v1/observations/{observationId}/occurrences";
    const label =
      view === "list" ? "Refresh observations" : "Refresh occurrences";
    const w = screenWorkspace();
    history.replaceState(
      null,
      "",
      `/unit/observations?source=${observation.sourceId}`,
    );
    w.get
      .mockResolvedValueOnce(
        etagReply({ items: [], nextCursor: "observation-next" }),
      )
      .mockResolvedValueOnce(etagReply({ items: [], nextCursor: "later" }))
      .mockRejectedValueOnce(new Error("current page unavailable"))
      .mockResolvedValueOnce(etagReply({ items: [] }))
      .mockRejectedValueOnce(new Error("first page unavailable"))
      .mockResolvedValue(etagReply({ items: [] }));
    render(
      <Observations
        {...w.props}
        id={view === "list" ? "" : observation.id}
        access={sourceAdmin}
      />,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: label })).toHaveProperty(
        "disabled",
        false,
      ),
    );
    click(label);
    await screen.findByRole("alert");
    expect(w.get.mock.calls[2]?.[1]).toMatchObject({
      params: { query: { limit: 25, cursor: "observation-next" } },
    });
    click("Reload");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: label })).toHaveProperty(
        "disabled",
        false,
      ),
    );
    expect(w.get.mock.calls[3]?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    });
    click(label);
    await screen.findByRole("alert");
    click("Reload");
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(6));
    if (view === "list")
      for (const [, options] of w.get.mock.calls)
        expect(options).toMatchObject({
          params: {
            query: { sourceId: observation.sourceId, freshness: "current" },
          },
        });
    expect(w.get.mock.calls.map(([route]) => route)).toEqual(
      Array(6).fill(path),
    );
    expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps an exact occurrence and adoption draft while refreshing metadata without another audited raw read", async () => {
  const w = adoptionSetup();
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/observations/{observationId}/raw"
      ? etagReply('{"identifier":"retained"}')
      : original(path, options),
  );
  render(
    <Observations {...w.props} id={observation.id} access={contributor} />,
  );
  await selectOccurrence();
  selectDestination();
  fireEvent.click(
    screen.getByRole("checkbox", { name: "Override local display name" }),
  );
  change("Local display name", "Preserve my local proposal");
  click("Read exact raw entry");
  await screen.findByRole("heading", { name: "Exact raw entry" });
  click("Refresh occurrences");
  await waitFor(() =>
    expect(
      w.get.mock.calls.filter(
        ([path]) => path === "/api/v1/observations/{observationId}/occurrences",
      ),
    ).toHaveLength(2),
  );
  expect(screen.getByLabelText("Local display name")).toHaveProperty(
    "value",
    "Preserve my local proposal",
  );
  expect(screen.getByLabelText("Destination namespace")).toHaveProperty(
    "value",
    namespace.id,
  );
  expect(
    screen
      .getByRole("link", { name: "Inspect adoption snapshot" })
      .getAttribute("href"),
  ).toContain(occurrence.snapshotId);
  expect(
    w.get.mock.calls.filter(
      ([path]) => path === "/api/v1/observations/{observationId}/raw",
    ),
  ).toHaveLength(1);
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps a create-only destination selector usable with write scope and no local metadata reads", async () => {
  const w = adoptionSetup();
  const access = {
    ...contributor,
    scopeEnforcement: true,
    scopes: ["sources.read", "catalog.write"] as typeof contributor.scopes,
    permissions: [
      "source.read",
      "source.read_evidence",
    ] as typeof contributor.permissions,
    namespaceGrants: [
      {
        namespaceId: namespace.id,
        permissions: [
          "namespace.read",
          "resource.create",
        ] as typeof contributor.permissions,
      },
    ],
  };
  render(<Observations {...w.props} id={observation.id} access={access} />);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Use this occurrence for adoption",
    }),
  );
  await screen.findByLabelText("Destination namespace ID");
  expect(
    screen.getByLabelText("Destination namespace ID").matches(":disabled"),
  ).toBe(false);
  expect(
    screen.getByRole("button", { name: "Adopt as local draft" }),
  ).toHaveProperty("disabled", true);
  change("Destination namespace ID", namespace.id);
  change("Local publisher ID", publisher.id);
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Adopt as local draft" }),
    ).toHaveProperty("disabled", false),
  );
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/observations/{observationId}/occurrences",
  ]);
  expect(w.post).not.toHaveBeenCalled();
});

it("disambiguates adoption publishers and accepts an explicit publisher outside loaded choices", async () => {
  const w = adoptionSetup();
  const original = w.get.getMockImplementation()!;
  w.get.mockImplementation(async (path, options) =>
    path === "/api/v1/publishers"
      ? etagReply({
          items: [publisher, { ...publisher, id: "other-publisher" }],
        })
      : original(path, options),
  );
  w.post.mockResolvedValue(etagReply({ resource }));
  render(
    <Observations {...w.props} id={observation.id} access={contributor} />,
  );
  await selectOccurrence();
  await screen.findByRole("option", {
    name: `${publisher.displayName} / ${publisher.id}`,
  });
  expect(
    screen.getByRole("option", {
      name: `${publisher.displayName} / other-publisher`,
    }),
  ).toBeTruthy();
  change("Destination namespace", namespace.id);
  fireEvent.click(
    screen.getByRole("checkbox", { name: "Override local display name" }),
  );
  change("Local display name", "Retained adoption draft");
  change("Local publisher ID", "unlisted-publisher");
  expect(screen.getByLabelText("Local display name")).toHaveProperty(
    "value",
    "Retained adoption draft",
  );
  expect(w.post).not.toHaveBeenCalled();
  await confirmCommand("Adopt as local draft");
  await screen.findByRole("link", { name: "Open adopted resource" });
  expect(w.post).toHaveBeenCalledOnce();
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/observations/{observationId}:adopt",
    {
      body: {
        publisherId: "unlisted-publisher",
        displayName: "Retained adoption draft",
      },
    },
  ]);
});

it("shows a safe error for a contribution bookmark missing its required identifier", async () => {
  history.replaceState(null, "", "/unit/observation-group");
  const { props, get } = screenWorkspace();
  const error = new APIError(400, "INVALID_ARGUMENT");
  get.mockRejectedValue(error);
  render(<ObservationGroup {...props} access={sourceAdmin} />);
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Start a new search or check your input.",
  );
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/observation-groups",
    expect.objectContaining({
      params: {
        query: { identifier: "", versionPresent: false, version: undefined },
      },
    }),
  );
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(error);
  expect(screen.queryByText("Group evidence")).toBeNull();
});
it("preserves absent, empty and populated version identities in observation links", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, {
    "/api/v1/observations": {
      items: [
        observation,
        { ...observation, id: "empty", versionPresent: true, version: "" },
        {
          ...observation,
          id: "versioned",
          versionPresent: true,
          version: "1.0",
        },
        {
          ...observation,
          id: "unresolved",
          identifier: undefined,
          sourceKey: "unknown-entry",
          completeness: "unresolved",
        },
      ],
    },
  });
  render(<Observations {...props} access={sourceAdmin} />);
  await screen.findByRole("link", { name: "unknown-entry" });
  const retained = screen
    .getByRole("link", { name: "unknown-entry" })
    .closest("section")!;
  expect(retained.textContent).toContain("Not supplied");
  expect(
    within(retained).getByText("Saved view").nextElementSibling?.textContent,
  ).toBe("Current");
  expect(
    within(retained).getByText("Completeness").nextElementSibling?.textContent,
  ).toBe("Unresolved");
  expect(
    within(retained).getByText("Compatibility").nextElementSibling?.textContent,
  ).toBe("Supported");
  expect(screen.getByText("(explicitly empty)")).toBeTruthy();
  expect(screen.getByText("1.0")).toBeTruthy();
  const links = screen.getAllByRole("link", {
    name: "Compare current contributions and conflicts",
  });
  expect(
    links.map((link) =>
      new URL(
        link.getAttribute("href")!,
        "http://localhost",
      ).searchParams.toString(),
    ),
  ).toEqual([
    new URLSearchParams({
      identifier: observation.identifier!,
      versionPresent: "false",
    }).toString(),
    new URLSearchParams({
      identifier: observation.identifier!,
      versionPresent: "true",
      version: "1.0",
    }).toString(),
  ]);
  expect(
    screen.getByRole("link", { name: "unknown-entry" }).getAttribute("href"),
  ).toBe("/unit/observations/unresolved");
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/observations",
    expect.objectContaining({
      params: {
        query: {
          sourceId: undefined,
          freshness: "current",
          limit: 25,
          cursor: undefined,
        },
      },
    }),
  );
});

it.each([false, true])(
  "shows contribution counts and conflict=%s without fetching raw evidence",
  async (conflict) => {
    history.replaceState(
      null,
      "",
      `/unit/observation-group?identifier=${encodeURIComponent(observation.identifier!)}`,
    );
    const { props, get } = screenWorkspace();
    readReplies(get, {
      "/api/v1/observation-groups": {
        identifier: observation.identifier,
        conflict,
        contributionsTruncated: false,
        members: [
          {
            normalizedEntryDigest: "normalized",
            contributions: [
              { observation, occurrenceCount: 2, sourceConflict: conflict },
            ],
          },
          ...(conflict
            ? [
                {
                  normalizedEntryDigest: "other-normalized",
                  contributions: [
                    {
                      observation: {
                        ...observation,
                        id: "conflict",
                        sourceKey: "other-source-key",
                      },
                      occurrenceCount: 1,
                      sourceConflict: true,
                    },
                  ],
                },
              ]
            : []),
        ],
      },
    });
    render(<ObservationGroup {...props} access={sourceAdmin} />);
    await screen.findByText(
      conflict
        ? "Different entry contents"
        : "Sources supplied matching entry contents",
    );
    const link = screen.getByRole("link", { name: observation.sourceKey });
    expect(link.getAttribute("href")).toBe("/unit/observations/observation-1");
    const contribution = within(link.closest("section")!);
    expect(
      contribution.getByText("Saved occurrences").nextElementSibling
        ?.textContent,
    ).toBe("2");
    expect(
      contribution.getByText("Different contents from this source")
        .nextElementSibling?.textContent,
    ).toBe(conflict ? "Yes" : "No");
    if (conflict)
      expect(
        within(
          screen
            .getByRole("link", { name: "other-source-key" })
            .closest("section")!,
        ).getByText("Saved occurrences").nextElementSibling?.textContent,
      ).toBe("1");
    expect(get).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/observation-groups",
      expect.objectContaining({
        params: {
          query: {
            identifier: observation.identifier,
            versionPresent: false,
            version: undefined,
          },
        },
      }),
    );
  },
);

it("binds adoption to the selected retained occurrence and explicit local overrides", async () => {
  const { props, get, post } = adoptionSetup();
  post.mockResolvedValue(
    etagReply({ resource: { ...resource, id: "adopted-resource" } }),
  );
  render(<Observations {...props} id={observation.id} access={contributor} />);
  await selectOccurrence();
  expect(
    screen.getByRole("link", { name: "Source" }).getAttribute("href"),
  ).toBe(`/unit/sources/${occurrence.sourceId}`);
  expect(screen.getByText(/revision 1. Adoption binds/)).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Inspect adoption snapshot" })
      .getAttribute("href"),
  ).toBe(`/unit/source-snapshots/${occurrence.snapshotId}`);
  expect(
    screen.getByText(/it does not rewrite the upstream observation/),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  expect(
    (
      screen.getByRole("button", {
        name: "Adopt as local draft",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  selectDestination();
  const overrides = {
    visibility: "private",
    version: "1.0",
    displayName: "Local helper",
    description: "Local draft",
    categories: ["support"],
    documentationUrl: "https://example.com/docs",
    supportUrl: "https://example.com/support",
    license: "Apache-2.0",
    termsUrl: "https://example.com/terms",
  };
  if (!screen.queryByLabelText("Local metadata overrides JSON"))
    click("Edit local metadata overrides as JSON");
  change("Local metadata overrides JSON", JSON.stringify(overrides));
  await confirmCommand("Adopt as local draft");
  expect(
    (
      await screen.findByRole("link", { name: "Open adopted resource" })
    ).getAttribute("href"),
  ).toBe("/unit/resources/adopted-resource");
  expect(post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/observations/{observationId}:adopt",
    {
      params: {
        path: { observationId: observation.id },
        header: { "Idempotency-Key": expect.any(String) as unknown },
      },
      body: {
        ...overrides,
        snapshotId: occurrence.snapshotId,
        retrievalDigest: occurrence.retrievalDigest,
        entryLocation: occurrence.entryLocation,
        namespaceId: namespace.id,
        publisherId: publisher.id,
      },
    },
  );
  expect(get.mock.calls.some(([path]) => String(path).endsWith("/raw"))).toBe(
    false,
  );
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("rejects overrides of provenance and permits correcting the form before sending", async () => {
  const { props, post } = adoptionSetup();
  post.mockResolvedValue(
    etagReply({ resource: { ...resource, id: "adopted-resource" } }),
  );
  render(<Observations {...props} id={observation.id} access={contributor} />);
  await selectOccurrence();
  selectDestination();
  if (!screen.queryByLabelText("Local metadata overrides JSON"))
    click("Edit local metadata overrides as JSON");
  change("Local metadata overrides JSON", '{"snapshotId":"another-snapshot"}');
  click("Adopt as local draft");
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Use only the documented local metadata fields.",
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(post).not.toHaveBeenCalled();
  if (!screen.queryByLabelText("Local metadata overrides JSON"))
    click("Edit local metadata overrides as JSON");
  change("Local metadata overrides JSON", "{}");
  await confirmCommand("Adopt as local draft");
  await screen.findByRole("link", { name: "Open adopted resource" });
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: { snapshotId: occurrence.snapshotId, namespaceId: namespace.id },
  });
  expect(screen.queryByRole("alert")).toBeNull();
});

it("requires Contributor access to the destination even for a Source Administrator", async () => {
  const { props, post } = adoptionSetup();
  render(<Observations {...props} id={observation.id} access={contributor} />);
  await selectOccurrence();
  selectDestination();
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Adopt as local draft",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  change("Destination namespace", "other-ns");
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Adopt as local draft",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true),
  );
  click("Adopt as local draft");
  expect(post).not.toHaveBeenCalled();
  expect(screen.queryByRole("dialog")).toBeNull();
});

it("accepts an explicit publisher ID without fetching a publisher list when its scope is absent", async () => {
  const { props, get, post } = adoptionSetup();
  post.mockResolvedValue(
    etagReply({ resource: { ...resource, id: "adopted-resource" } }),
  );
  render(
    <Observations
      {...props}
      id={observation.id}
      access={{
        ...contributor,
        scopeEnforcement: true,
        scopes: ["catalog.read", "catalog.write", "sources.read"],
      }}
    />,
  );
  await selectOccurrence();
  change("Destination namespace", namespace.id);
  change("Local publisher ID", publisher.id);
  await confirmCommand("Adopt as local draft");
  await screen.findByRole("link", { name: "Open adopted resource" });
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: { publisherId: publisher.id, visibility: "private" },
  });
  expect(get.mock.calls.map(([path]) => path)).not.toContain(
    "/api/v1/publishers",
  );
});

it("keeps a dirty occurrence selection unless discard is confirmed, then resets the editor", async () => {
  const { props, get, post } = adoptionSetup();
  const second = {
    ...occurrence,
    snapshotId: "snapshot-2",
    retrievalDigest: "retrieval-2",
    entryLocation: "/entries/3",
  };
  readReplies(get, {
    "/api/v1/observations/{observationId}/occurrences": {
      items: [occurrence, second],
    },
    "/api/v1/namespaces": { items: [namespace] },
    "/api/v1/publishers": { items: [publisher] },
  });
  post.mockResolvedValue(
    etagReply({ resource: { ...resource, id: "adopted-resource" } }),
  );
  render(<Observations {...props} id={observation.id} access={contributor} />);
  const choices = await screen.findAllByRole("button", {
    name: "Use this occurrence for adoption",
  });
  fireEvent.click(required(choices[0]));
  await screen.findByRole("option", {
    name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
  });
  selectDestination();
  if (!screen.queryByLabelText("Local metadata overrides JSON"))
    click("Edit local metadata overrides as JSON");
  change("Local metadata overrides JSON", '{"displayName":"Unsaved draft"}');
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(required(choices[1]));
  expect(discard).toHaveBeenCalledOnce();
  expect(
    (
      screen.getByLabelText(
        "Local metadata overrides JSON",
      ) as HTMLTextAreaElement
    ).value,
  ).toContain("Unsaved draft");
  discard.mockReturnValue(true);
  fireEvent.click(required(choices[1]));
  await waitFor(() =>
    expect(
      (screen.getByLabelText("Destination namespace") as HTMLSelectElement)
        .value,
    ).toBe(""),
  );
  click("Edit local metadata overrides as JSON");
  expect(
    (
      screen.getByLabelText(
        "Local metadata overrides JSON",
      ) as HTMLTextAreaElement
    ).value,
  ).toBe('{"visibility":"private"}');
  await screen.findByRole("option", {
    name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
  });
  selectDestination();
  await confirmCommand("Adopt as local draft");
  await screen.findByRole("link", { name: "Open adopted resource" });
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      snapshotId: second.snapshotId,
      retrievalDigest: second.retrievalDigest,
      entryLocation: second.entryLocation,
    },
  });
});

it("pages occurrences and adoption selectors independently without a hidden mutation", async () => {
  const { props, get, post } = adoptionSetup();
  readReplies(get, {
    "/api/v1/observations/{observationId}/occurrences": {
      items: [occurrence],
      nextCursor: "occurrences-next",
    },
    "/api/v1/namespaces": { items: [namespace], nextCursor: "namespaces-next" },
    "/api/v1/publishers": { items: [publisher], nextCursor: "publishers-next" },
  });
  render(<Observations {...props} id={observation.id} access={contributor} />);
  await selectOccurrence();
  const occurrences = screen
    .getByRole("heading", { name: "Saved occurrences" })
    .closest("section")!;
  fireEvent.click(
    within(occurrences).getByRole("button", { name: "Next page" }),
  );
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      "/api/v1/observations/{observationId}/occurrences",
      expect.objectContaining({
        params: {
          path: { observationId: observation.id },
          query: { limit: 25, cursor: "occurrences-next" },
        },
      }),
    ),
  );
  for (const [label, endpoint, cursor] of [
    ["Destination namespace", "/api/v1/namespaces", "namespaces-next"],
    ["Local publisher", "/api/v1/publishers", "publishers-next"],
  ] as const) {
    const field = screen
      .getByLabelText(label)
      .closest(".workspace-field")!.parentElement!;
    fireEvent.click(within(field).getByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        endpoint,
        expect.objectContaining({ params: { query: { limit: 25, cursor } } }),
      ),
    );
  }
  expect(post).not.toHaveBeenCalled();
});

it("requires private source evidence independently of metadata read and local creation", async () => {
  const { props, get, post } = adoptionSetup();
  const permissions = [
    "source.read",
    "namespace.read",
    "resource.create",
  ] as const;
  render(
    <Observations
      {...props}
      id={observation.id}
      access={{
        ...props.access,
        permissions: [...permissions],
        availablePermissions: [...permissions],
      }}
    />,
  );
  await screen.findByRole("button", { name: "Read exact raw entry" });
  expect(
    screen.getByRole("button", { name: "Read exact raw entry" }),
  ).toHaveProperty("disabled", true);
  expect(
    await screen.findByRole("button", {
      name: "Use this occurrence for adoption",
    }),
  ).toHaveProperty("disabled", true);
  click("Use this occurrence for adoption");
  expect(
    screen.queryByRole("button", { name: "Adopt as local draft" }),
  ).toBeNull();
  expect(get.mock.calls.map(([path]) => path)).not.toContain(
    "/api/v1/observations/{observationId}/raw",
  );
  expect(post).not.toHaveBeenCalled();
});

it.each([false, true])(
  "captures guided adoption with explicit empty categories=%s and retains the same command on an uncertain response",
  async (explicit) => {
    const { props, post } = adoptionSetup();
    post
      .mockRejectedValueOnce(new APIError(503, "UNAVAILABLE"))
      .mockResolvedValueOnce(etagReply({ resource }));
    render(
      <Observations {...props} id={observation.id} access={contributor} />,
    );
    await selectOccurrence();
    selectDestination();
    fireEvent.click(screen.getByLabelText("Override local display name"));
    change("Local display name", "Guided draft");
    if (explicit)
      fireEvent.click(screen.getByLabelText("Override local categories"));
    click("Adopt as local draft");
    const dialog = await screen.findByRole("dialog");
    expect(
      screen.getByLabelText("Local display name").matches(":disabled"),
    ).toBe(true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Retry same command" }),
    );
    await screen.findByRole("link", { name: "Open adopted resource" });
    expect(post).toHaveBeenCalledTimes(2);
    expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
    const body = (post.mock.calls[0]?.[1] as { body: Record<string, unknown> })
      .body;
    expect(body).toEqual({
      displayName: "Guided draft",
      visibility: "private",
      ...(explicit ? { categories: [] } : {}),
      snapshotId: occurrence.snapshotId,
      retrievalDigest: occurrence.retrievalDigest,
      entryLocation: occurrence.entryLocation,
      namespaceId: namespace.id,
      publisherId: publisher.id,
    });
    expect(props.onFailure).toHaveBeenCalledOnce();
  },
);

it("adopts into an exact namespace without catalog.read, directory reads or an unauthorized resource link", async () => {
  const w = adoptionSetup();
  w.post.mockResolvedValue(etagReply({ resource }));
  render(
    <Observations
      {...w.props}
      id={observation.id}
      access={{
        ...reader,
        permissions: ["source.read", "source.read_evidence"],
        availablePermissions: [
          "source.read",
          "source.read_evidence",
          "resource.create",
          "namespace.read",
        ],
        namespaceGrants: [
          {
            namespaceId: namespace.id,
            permissions: ["resource.create", "namespace.read"],
          },
        ],
        scopeEnforcement: true,
        scopes: ["sources.read", "catalog.write"],
      }}
    />,
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Use this occurrence for adoption",
    }),
  );
  change("Destination namespace ID", namespace.id);
  change("Local publisher ID", publisher.id);
  await confirmCommand("Adopt as local draft");
  await screen.findByText("Adoption result");
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/observations/{observationId}/occurrences",
  ]);
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      namespaceId: namespace.id,
      publisherId: publisher.id,
      snapshotId: occurrence.snapshotId,
      retrievalDigest: occurrence.retrievalDigest,
      entryLocation: occurrence.entryLocation,
    },
  });
  expect(
    screen.queryByRole("link", { name: "Open adopted resource" }),
  ).toBeNull();
  expect(
    screen.getByText("Adoption result").closest("details")?.textContent,
  ).toContain(resource.id);
});

it("clears an applied Source restriction while retaining freshness and resetting paging", async () => {
  history.replaceState(
    null,
    "",
    "/unit/observations?source=00000000-0000-7000-8000-000000000099",
  );
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [], nextCursor: "next" }));
  render(<Observations {...w.props} access={sourceAdmin} />);
  await screen.findByText("No matching observations.");
  expect(
    screen.getByRole("region", { name: "Applied source restriction" })
      .textContent,
  ).toContain("00000000-0000-7000-8000-000000000099");
  change("Show entries", "all");
  click("Apply observation filters");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  click("Next page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: {
        sourceId: "00000000-0000-7000-8000-000000000099",
        freshness: "all",
        cursor: "next",
      },
    },
  });
  click("All sources");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(4));
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { freshness: "all", limit: 25 } },
  });
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.sourceId",
  );
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(location.search).toBe("");
  expect(screen.queryByRole("button", { name: "All sources" })).toBeNull();
});

it.each([false, true])(
  "distinguishes identical Source keys across sources and normalized conflicts: %s",
  async (conflict) => {
    history.replaceState(
      null,
      "",
      `/unit/observation-group?identifier=${encodeURIComponent(observation.identifier!)}&versionPresent=${conflict}&${conflict ? "version=1.0" : ""}`,
    );
    const w = screenWorkspace();
    const left = { ...observation, sourceKey: "same-key-hash" };
    const right = {
      ...left,
      id: "observation-2",
      sourceId: "source-2",
      completeness: "projection",
      compatibility: {
        structural: "valid",
        support: "unknown",
        profileId: "other-profile",
      },
    };
    const members = [
      {
        normalizedEntryDigest: "normalized-one",
        contributions: [
          { observation: left, occurrenceCount: 2, sourceConflict: conflict },
          { observation: right, occurrenceCount: 1, sourceConflict: false },
        ],
      },
    ];
    w.get.mockResolvedValue(
      etagReply({
        identifier: observation.identifier,
        conflict,
        contributionsTruncated: conflict,
        members: [
          ...members,
          ...(conflict
            ? [
                {
                  normalizedEntryDigest: "normalized-two",
                  contributions: [
                    {
                      observation: { ...left, id: "observation-3" },
                      occurrenceCount: 1,
                      sourceConflict: true,
                    },
                  ],
                },
              ]
            : []),
        ],
      }),
    );
    render(<ObservationGroup {...w.props} access={sourceAdmin} />);
    await screen.findByText(
      conflict
        ? /Different entry contents/
        : "Sources supplied matching entry contents",
    );
    expect(screen.getAllByRole("link", { name: "same-key-hash" })).toHaveLength(
      conflict ? 3 : 2,
    );
    expect(
      screen.getByRole("link", { name: "source-2" }).getAttribute("href"),
    ).toBe("/unit/sources/source-2");
    const sourceFacts = within(
      screen.getByRole("link", { name: "source-2" }).closest("section")!,
    );
    expect(
      sourceFacts.getByText("Completeness").nextElementSibling?.textContent,
    ).toBe("Projection");
    expect(
      sourceFacts.getByText("Structure").nextElementSibling?.textContent,
    ).toBe("Valid");
    expect(
      sourceFacts.getByText("Support").nextElementSibling?.textContent,
    ).toBe("Unknown");
    expect(screen.getByText(/Selected version:/).textContent).toContain(
      conflict ? "1.0" : "No version supplied",
    );
    expect(w.get).toHaveBeenCalledOnce();
  },
);
