import { testAccess } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import {
  evidence as evidenceSeed,
  versionRevision,
  representation,
} from "../../tests/unit/responses";
import { expect, it, vi } from "vitest";
import { Profiler } from "react";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Versions, { VersionRevision } from "./Versions";
import { sessionAccess } from "../../tests/unit/fixtures";
import { deferred, reply } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  version,
} from "../../tests/unit/screens";
screenLifecycle("/unit/versions/new?resource=resource-1");
const evidence = {
  ...evidenceSeed,
  state: "complete",
  trust: { state: "not_assessed" },
  legalRights: { state: "not_assessed" },
};

it("focuses a representation added before the initial passive effects", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/resources/{resourceId}": resource });
  let activated = false;
  // A commit callback activates the real control before passive mount effects.
  // This controls the ordering without timers or a production test seam.
  render(
    <Profiler
      id="early-representation-action"
      onRender={() => {
        const add = screen.queryByRole("button", {
          name: "Add representation",
        });
        if (!activated && add) {
          activated = true;
          add.click();
        }
      }}
    >
      <Versions {...props} id="new" access={administrator} />
    </Profiler>,
  );
  const row = await screen.findByRole("group", { name: /content 2$/ });
  await waitFor(() =>
    expect(within(row).getByLabelText("Content source")).toBe(
      document.activeElement,
    ),
  );
  expect(post).not.toHaveBeenCalled();
});

it.each(["new", "revision"] as const)(
  "protects a populated %s representation and keeps row focus after accepted removal",
  async (mode) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/resources/{resourceId}": resource,
      "/api/v1/resource-versions/{versionId}": version,
      "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
    });
    render(
      <Versions
        {...props}
        id={mode === "new" ? "new" : version.id}
        access={administrator}
      />,
    );
    if (mode === "revision") {
      await screen.findByRole("button", { name: "New revision" });
      click("New revision");
    }
    await screen.findByLabelText("Version label");
    click("Add representation");
    const row = screen.getByRole("group", { name: /content 2$/ });
    await waitFor(() =>
      expect(within(row).getByLabelText("Content source")).toBe(
        document.activeElement,
      ),
    );
    fireEvent.change(within(row).getByLabelText("Standards document (JSON)"), {
      target: { value: '{"name":"Unsaved"}' },
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    click("Remove representation 2");
    expect(
      within(row).getByLabelText("Standards document (JSON)"),
    ).toHaveProperty("value", '{"name":"Unsaved"}');
    expect(confirm).toHaveBeenCalledOnce();
    confirm.mockReturnValue(true);
    click("Remove representation 2");
    expect(screen.queryByRole("group", { name: /content 2$/ })).toBeNull();
    await waitFor(() =>
      expect(screen.getByLabelText("Content source")).toBe(
        document.activeElement,
      ),
    );
    expect(
      screen.queryByRole("button", { name: /^Remove representation/ }),
    ).toBeNull();
    expect(post).not.toHaveBeenCalled();
  },
);

it.each(["new", "revision"] as const)(
  "focuses Add when a surviving %s Skill archive row is temporarily disabled",
  async (mode) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/resources/{resourceId}": {
        ...resource,
        kind: "skill",
        canonicalIdentifier: "urn:air:example.com:skills:helper",
      },
      "/api/v1/resource-versions/{versionId}": version,
      "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
    });
    render(
      <Versions
        {...props}
        id={mode === "new" ? "new" : version.id}
        access={administrator}
      />,
    );
    if (mode === "revision") {
      await screen.findByRole("button", { name: "New revision" });
      click("New revision");
    }
    await screen.findByLabelText("Version label");
    change("Content source", "package");
    const pending = deferred<ArrayBuffer>();
    fireEvent.change(screen.getByLabelText("Skill archive"), {
      target: {
        files: [
          { name: "skill.zip", size: 3, arrayBuffer: () => pending.promise },
        ],
      },
    });
    click("Add representation");
    click("Remove representation 2");
    expect(screen.getByRole("button", { name: "Add representation" })).toBe(
      document.activeElement,
    );
    await act(async () => pending.resolve(new Uint8Array([1, 2, 3]).buffer));
    expect(screen.getByLabelText("Media type")).toHaveProperty(
      "value",
      "application/agent-skills+zip",
    );
    expect(post).not.toHaveBeenCalled();
  },
);

it("protects an edited empty row and a staged local document without treating default rows as dirty", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, { "/api/v1/resources/{resourceId}": resource });
  render(<Versions {...props} id="new" access={administrator} />);
  await screen.findByLabelText("Version label");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Add representation");
  click("Remove representation 2");
  expect(confirm).not.toHaveBeenCalled();
  click("Add representation");
  const row = screen.getByRole("group", { name: /content 2$/ });
  fireEvent.change(within(row).getByLabelText("Profile ID (optional)"), {
    target: { value: "selected-profile" },
  });
  click("Remove representation 2");
  expect(confirm).toHaveBeenCalledOnce();
  fireEvent.change(within(row).getByLabelText("Profile ID (optional)"), {
    target: { value: "" },
  });
  const bytes = new TextEncoder().encode('{"staged":true}');
  fireEvent.change(within(row).getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "draft.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await within(row).findByRole("button", { name: "Use loaded document" });
  click("Remove representation 2");
  expect(confirm).toHaveBeenCalledTimes(2);
  expect(
    within(row).getByLabelText("Standards document (JSON)"),
  ).toHaveProperty("value", "");
  expect(post).not.toHaveBeenCalled();
});

it.each(["x", "🙂"])(
  "rejects an oversized version (%s) before preparing a command",
  async (character) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, { "/api/v1/resources/{resourceId}": resource });
    render(<Versions {...props} id="new" access={administrator} />);
    await screen.findByLabelText("Version label");
    change("Version label", character.repeat(129));
    change("Standards document (JSON)", '{"name":"Helper"}');
    click("Create version");
    await screen.findByText(
      "Version label needs 1 to 128 characters without NUL.",
    );
    expect(post).not.toHaveBeenCalled();
  },
);

it.each([
  { name: "ASCII", label: "1.0.0" },
  { name: "Unicode boundary", label: "🙂".repeat(128) },
])(
  "creates a $name version from representation inputs and links the saved version",
  async ({ label }) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, { "/api/v1/resources/{resourceId}": resource });
    post.mockResolvedValue(
      etagReply({
        version: {
          ...version,
          version: label,
          candidateRevisionId: versionRevision.id,
        },
        revision: versionRevision,
        evidence,
      }),
    );
    render(<Versions {...props} id="new" access={administrator} />);
    await screen.findByLabelText("Version label");
    expect(screen.getByLabelText("Version label")).toHaveProperty(
      "maxLength",
      256,
    );
    expect(
      document.getElementById(
        screen
          .getByLabelText("Version label")
          .getAttribute("aria-describedby")!,
      )?.textContent,
    ).toContain("Match any version declared in the supplied document.");
    change("Version label", label);
    click("Add representation");
    expect(screen.getAllByLabelText("Representation role")).toHaveLength(2);
    fireEvent.click(
      required(
        screen.getAllByRole("button", {
          name: /^Remove representation \d+$/,
        })[1],
      ),
    );
    change("Standards document (JSON)", '{"name":"Helper"}');
    await confirmCommand("Create version");
    await screen.findByRole("link", { name: "Open saved version" });
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/resources/{resourceId}/versions",
      {
        params: { path: { resourceId: resource.id } },
        body: {
          version: label,
          representations: [expect.objectContaining({ role: "primary" })],
        },
      },
    ]);
  },
);

it("rejects missing primary representation and submits an exact draft revision", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    "/api/v1/resource-versions/{versionId}": {
      ...version,
      candidateRevisionId: "rev-1",
    },
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resource-versions/{versionId}/revisions": {
      items: [{ id: "rev-1", revisionNumber: 1, createdBy: "author" }],
      nextCursor: "page-2",
    },
  });
  post.mockResolvedValue(
    etagReply({
      version: { ...version, candidateRevisionId: versionRevision.id },
      revision: versionRevision,
      evidence,
    }),
  );
  const view = render(
    <Versions {...props} id={version.id} access={administrator} />,
  );
  await screen.findByRole("button", { name: "New revision" });
  fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "page-2" } },
    }),
  );
  click("New revision");
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "readOnly",
    true,
  );
  change("Standards document (JSON)", '{"name":"Helper"}');
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("New revision");
  expect(
    (screen.getByLabelText("Standards document (JSON)") as HTMLTextAreaElement)
      .value,
  ).toBe('{"name":"Helper"}');
  expect(discard).toHaveBeenCalledTimes(1);
  expect(post).not.toHaveBeenCalled();
  change("Representation role", "metadata");
  click("Create version revision");
  await screen.findByText("Include exactly one primary representation.");
  expect(post).not.toHaveBeenCalled();
  change("Representation role", "primary");
  await confirmCommand("Create version revision");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/resource-versions/{versionId}/revisions",
    {
      params: {
        path: { versionId: version.id },
        header: { "If-Match": '"revision-1"' },
      },
      body: {
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            data: { name: "Helper" },
          },
        ],
      },
    },
  ]);
  expect(post.mock.calls[0]?.[1]).not.toHaveProperty("body.version");
  discard.mockReturnValue(true);
  view.rerender(<Versions {...props} id={version.id} />);
  await screen.findByText(
    /resource.edit permission is required in this namespace/,
  );
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    '{"name":"Helper"}',
  );
  click("New revision");
  expect(screen.queryByLabelText("Version label")).toBeNull();
});

it("keeps remaining edits and exact revision preconditions after removing a middle representation", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resource-versions/{versionId}": version,
    "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
  });
  w.post.mockResolvedValue(
    etagReply({
      version: { ...version, candidateRevisionId: versionRevision.id },
      revision: versionRevision,
      evidence,
    }),
  );
  render(<Versions {...w.props} id={version.id} access={administrator} />);
  await screen.findByRole("button", { name: "New revision" });
  click("New revision");
  change("Standards document (JSON)", '{"name":"Primary"}');
  click("Add representation");
  const middle = screen.getByRole("group", { name: /content 2$/ });
  fireEvent.change(within(middle).getByLabelText("Standards document (JSON)"), {
    target: { value: '{"name":"Remove this draft"}' },
  });
  click("Add representation");
  const last = screen.getByRole("group", { name: /content 3$/ });
  const retainedSource = within(last).getByLabelText("Content source");
  fireEvent.change(retainedSource, { target: { value: "url" } });
  fireEvent.change(within(last).getByLabelText("Document URL"), {
    target: { value: "https://metadata.example/readme.md" },
  });
  fireEvent.change(within(last).getByLabelText("Media type"), {
    target: { value: "text/markdown" },
  });
  fireEvent.change(within(last).getByLabelText("Profile ID (optional)"), {
    target: { value: "kept-profile" },
  });
  vi.spyOn(window, "confirm").mockReturnValue(true);
  click("Remove representation 2");
  expect(screen.queryByRole("group", { name: /content 3$/ })).toBeNull();
  expect(
    within(screen.getByRole("group", { name: /content 2$/ })).getByLabelText(
      "Content source",
    ),
  ).toBe(retainedSource);
  expect(document.activeElement).toBe(retainedSource);
  expect(screen.getByLabelText("Document URL")).toHaveProperty(
    "value",
    "https://metadata.example/readme.md",
  );
  await confirmCommand("Create version revision");
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/resource-versions/{versionId}/revisions",
    {
      params: {
        path: { versionId: version.id },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: {
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            data: { name: "Primary" },
          },
          {
            role: "metadata",
            mediaType: "text/markdown",
            profileId: "kept-profile",
            url: "https://metadata.example/readme.md",
          },
        ],
      },
    },
  );
});

it("does not offer editing for a sealed version or fetch without Reader", async () => {
  const { props, get } = screenWorkspace();
  history.replaceState(null, "", `/unit/versions/${version.id}`);
  const view = render(
    <Versions
      {...props}
      id={version.id}
      access={{ ...props.access, ...testAccess([]) }}
    />,
  );
  expect(get).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toContain(
    "When scopes are enforced, catalog.read is also required.",
  );
  readReplies(get, {
    "/api/v1/resource-versions/{versionId}": {
      ...version,
      sealedRevisionId: "sealed-1",
    },
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
  });
  view.rerender(<Versions {...props} id={version.id} access={administrator} />);
  const sealed = await screen.findByText("Sealed:", {
    selector: ".state-badge",
  });
  expect(sealed.parentElement?.textContent).toBe(
    "Sealed: create a new version label to change representations.",
  );
  expect(screen.queryByRole("button", { name: "New revision" })).toBeNull();
});

it("disables the open editor when navigation loads a sealed version", async () => {
  const { props, get, post } = screenWorkspace();
  const routes: Record<string, unknown> = {
    "/api/v1/resource-versions/{versionId}": version,
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
  };
  readReplies(get, routes);
  const view = render(
    <Versions {...props} id={version.id} access={administrator} />,
  );
  await screen.findByRole("button", { name: "New revision" });
  click("New revision");
  expect(screen.getByLabelText("Version label")).toBeTruthy();
  routes["/api/v1/resource-versions/{versionId}"] = {
    ...version,
    id: "sealed-version",
    sealedRevisionId: "sealed-1",
  };
  view.rerender(
    <Versions {...props} id="sealed-version" access={administrator} />,
  );
  await screen.findByText(
    "This version is sealed. Create a new version label to change its representations.",
  );
  expect(screen.queryByLabelText("Version label")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Create version revision" }),
  ).toBeNull();
  expect(post).not.toHaveBeenCalled();
});

it.each(["reader", "missing-etag"])(
  "keeps version history readable and separates editing authority from a missing ETag for %s",
  async (mode) => {
    const { props, get, post } = screenWorkspace();
    const values: Record<string, unknown> = {
      "/api/v1/resource-versions/{versionId}": version,
      "/api/v1/resources/{resourceId}": resource,
      "/api/v1/resource-versions/{versionId}/revisions": {
        items: [
          { id: "revision-history", revisionNumber: 1, createdBy: "author" },
        ],
      },
    };
    get.mockImplementation(async (path) => reply(values[String(path)]));
    render(
      <Versions
        {...props}
        id={version.id}
        access={mode === "reader" ? props.access : administrator}
      />,
    );
    await screen.findByRole("link", { name: "Revision 1" });
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "New revision" }),
      ).toHaveProperty("disabled", mode === "reader"),
    );
    expect(
      screen.getByText(
        mode === "reader"
          ? /resource.edit permission is required in this namespace/
          : "Reload the version to obtain its current revision before saving.",
      ),
    ).toBeTruthy();
    click("New revision");
    if (mode === "reader")
      expect(screen.queryByLabelText("Version label")).toBeNull();
    else {
      const source = await screen.findByLabelText("Content source");
      expect(source.matches(":disabled")).toBe(false);
      expect(
        screen.getByText(
          "Reload the version to obtain its current revision before saving.",
        ),
      ).toBeTruthy();
      change("Standards document (JSON)", '{"name":"staged-without-an-etag"}');
      expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
        "value",
        '{"name":"staged-without-an-etag"}',
      );
      expect(
        screen.getByRole("button", { name: "Create version revision" }),
      ).toHaveProperty("disabled", true);
      fireEvent.submit(source.closest("form")!);
      expect(screen.queryByRole("dialog")).toBeNull();
    }
    expect(post).not.toHaveBeenCalled();
  },
);

it("reads retained documents only explicitly and revalidates without replacing revision identity", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    "/api/v1/version-revisions/{revisionId}": {
      namespaceId: resource.namespaceId,
      revision: {
        id: "rev-1",
        resourceVersionId: version.id,
        revisionNumber: 1,
        representations: [
          {
            ...representation,
            id: "inline",
            role: "primary",
            mediaType: "application/json",
            validationState: "valid",
            data: { name: "Retained JSON" },
          },
          {
            ...representation,
            id: "artifact",
            role: "metadata",
            mediaType: "text/plain",
            validationState: "valid",
            artifactId: "artifact-1",
          },
          {
            ...representation,
            id: "external",
            role: "metadata",
            mediaType: "text/plain",
            validationState: "valid",
          },
        ],
      },
      evidence,
    },
    "/api/v1/resource-versions/{versionId}": version,
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/artifacts/{artifactId}/content": "Exact retained bytes",
  });
  post.mockResolvedValue(
    reply({
      namespaceId: resource.namespaceId,
      revision: { ...versionRevision, id: "rev-1" },
      evidence: { ...evidence, state: "blocked" },
    }),
  );
  render(<VersionRevision {...props} id="rev-1" access={administrator} />);
  await screen.findByText(
    "No retained document. See the external handoff metadata.",
  );
  expect(get.mock.calls.some(([p]) => String(p).includes("artifacts"))).toBe(
    false,
  );
  const buttons = screen.getAllByRole("button", {
    name: "Read retained document",
  });
  fireEvent.click(required(buttons[0]));
  fireEvent.click(required(buttons[1]));
  await screen.findByText("Exact retained bytes");
  expect(
    get.mock.calls.some(
      ([path]) =>
        path === "/api/v1/resource-versions/{versionId}" ||
        path === "/api/v1/resources/{resourceId}",
    ),
  ).toBe(false);
  expect(get).toHaveBeenCalledWith(
    "/api/v1/artifacts/{artifactId}/content",
    expect.objectContaining({
      parseAs: "text",
      params: { path: { artifactId: "artifact-1" } },
    }),
  );
  for (const b of screen.getAllByRole("button", {
    name: "Hide retained document",
  }))
    fireEvent.click(b);
  expect(
    screen.getByText(/even when content is temporarily unavailable/),
  ).toBeTruthy();
  click("Revalidate revision");
  const confirmation = await screen.findByRole("dialog");
  expect(
    within(confirmation).getByText(
      /an independent reviewer must request changes or reject it/,
    ),
  ).toBeTruthy();
  expect(
    within(confirmation).getByText(
      /entire active discovery snapshots containing this revision/,
    ),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Confirm");
  await screen.findByRole("heading", { name: "Latest validation: Blocked" });
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/version-revisions/{revisionId}:validate",
    { params: { path: { revisionId: "rev-1" } }, body: {} },
  ]);
});

it.each([
  "tenant",
  "scoped",
  "later-page",
  "wrong-namespace",
  "missing-write-scope",
])(
  "revalidates without parent metadata reads using %s authority",
  async (mode) => {
    const w = screenWorkspace();
    const permissions = ["resource.read_content", "resource.validate"] as const;
    const grant = {
      namespaceId: "00000000-0000-7000-8000-000000000081",
      permissions: [...permissions],
    };
    const access = {
      ...sessionAccess,
      ...testAccess([]),
      permissions: mode === "tenant" ? [...permissions] : [],
      availablePermissions: [...permissions],
      namespaceGrants:
        mode === "tenant" || mode === "later-page"
          ? []
          : [
              {
                ...grant,
                namespaceId:
                  mode === "wrong-namespace"
                    ? "other-namespace"
                    : grant.namespaceId,
              },
            ],
      scopeEnforcement: true,
      scopes:
        mode === "missing-write-scope"
          ? ["catalog.read" as const]
          : ["catalog.read" as const, "catalog.write" as const],
      ...(mode === "later-page" ? { nextCursor: "later" } : {}),
    };
    const bundle = {
      namespaceId: "00000000-0000-7000-8000-000000000081",
      revision: versionRevision,
      evidence,
    };
    readReplies(w.get, {
      "/api/v1/version-revisions/{revisionId}": bundle,
      "/api/v1/me": { ...access, namespaceGrants: [grant] },
    });
    w.post.mockResolvedValue(
      reply({ ...bundle, evidence: { ...evidence, state: "blocked" } }),
    );
    render(
      <VersionRevision {...w.props} access={access} id={versionRevision.id} />,
    );
    await screen.findByRole("heading", { name: "Latest validation: Complete" });
    if (mode === "wrong-namespace" || mode === "missing-write-scope") {
      expect(
        screen.queryByRole("button", { name: "Revalidate revision" }),
      ).toBeNull();
      expect(w.post).not.toHaveBeenCalled();
    } else {
      const action = await screen.findByRole("button", {
        name: "Revalidate revision",
      });
      await waitFor(() => expect(action).toHaveProperty("disabled", false));
      await confirmCommand("Revalidate revision");
      await screen.findByRole("heading", {
        name: "Latest validation: Blocked",
      });
      expect(w.post).toHaveBeenCalledWith(
        "/api/v1/version-revisions/{revisionId}:validate",
        expect.objectContaining({
          body: {},
          params: expect.objectContaining({
            path: { revisionId: versionRevision.id },
          }) as unknown,
        }),
      );
    }
    expect(w.get.mock.calls.map(([path]) => path)).toEqual(
      mode === "later-page"
        ? ["/api/v1/version-revisions/{revisionId}", "/api/v1/me"]
        : ["/api/v1/version-revisions/{revisionId}"],
    );
    expect(screen.queryByRole("link", { name: "Back to version" })).toBeNull();
  },
);

it.each(["denied", "obsolete"])(
  "keeps validation disabled after a %s namespace hint",
  async (mode) => {
    const w = screenWorkspace();
    const pending = deferred<ReturnType<typeof reply>>();
    const access = {
      ...sessionAccess,
      ...testAccess([]),
      availablePermissions: [
        "resource.read_content",
        "resource.validate",
      ] as typeof w.props.access.permissions,
      nextCursor: "later",
    };
    w.get.mockImplementation((path) =>
      path === "/api/v1/version-revisions/{revisionId}"
        ? Promise.resolve(
            reply({
              namespaceId: "00000000-0000-7000-8000-000000000081",
              revision: versionRevision,
              evidence,
            }),
          )
        : pending.promise,
    );
    const view = render(
      <VersionRevision {...w.props} access={access} id={versionRevision.id} />,
    );
    await screen.findByRole("heading", { name: "Latest validation: Complete" });
    expect(
      screen.queryByRole("button", { name: "Revalidate revision" }),
    ).toBeNull();
    await waitFor(() =>
      expect(w.get).toHaveBeenLastCalledWith("/api/v1/me", {
        params: {
          query: { namespaceId: "00000000-0000-7000-8000-000000000081" },
        },
        signal: expect.any(AbortSignal) as unknown,
      }),
    );
    if (mode === "denied") {
      await act(async () =>
        pending.reject(new Error("Controlled namespace refusal")),
      );
      await screen.findByRole("alert");
    } else {
      const revoked: typeof w.props.access = {
        ...access,
        availablePermissions: [],
      };
      delete revoked.nextCursor;
      view.rerender(
        <VersionRevision
          {...w.props}
          access={revoked}
          id={versionRevision.id}
        />,
      );
      await act(async () =>
        pending.resolve(
          reply({
            ...access,
            namespaceGrants: [
              {
                namespaceId: "00000000-0000-7000-8000-000000000081",
                permissions: ["resource.validate"],
              },
            ],
          }),
        ),
      );
    }
    expect(
      screen.queryByRole("button", { name: "Revalidate revision" }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["no-history", "history-only", "history-and-content"])(
  "reads version metadata with independently permitted history: %s",
  async (mode) => {
    const history = mode !== "no-history";
    const content = mode === "history-and-content";
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/resource-versions/{versionId}": version,
      "/api/v1/resources/{resourceId}": resource,
      "/api/v1/resource-versions/{versionId}/revisions": {
        items: [
          { id: "history-revision", revisionNumber: 1, createdBy: "author" },
        ],
      },
    });
    const permissions: typeof w.props.access.permissions = [
      "resource.read",
      ...(history ? ["resource.read_history" as const] : []),
      ...(content ? ["resource.read_content" as const] : []),
    ];
    render(
      <Versions
        {...w.props}
        id={version.id}
        access={{
          ...w.props.access,
          ...testAccess([]),
          permissions,
          availablePermissions: permissions,
        }}
      />,
    );
    await screen.findByRole("heading", { name: `Version ${version.version}` });
    if (history) {
      await screen.findByRole("heading", { name: "Revision history" });
      await screen.findByText("Revision 1", { exact: true });
      expect(screen.queryByRole("link", { name: "Revision 1" }) !== null).toBe(
        content,
      );
    } else
      expect(
        screen.queryByRole("heading", { name: "Revision history" }),
      ).toBeNull();
    await waitFor(() => expect(w.get.mock.calls.length).toBe(history ? 3 : 2));
  },
);

it.each([
  "success",
  "missing",
  "foreign",
  "failed",
  "changed",
  "cancelled",
  "cancelled-failure",
  "unknown-failure",
  "permission-loss",
] as const)(
  "stages complete current-draft reuse without overwriting edits: %s",
  async (mode) => {
    const w = screenWorkspace();
    w.props.runtime.documentationUrl = "/docs/";
    const current = {
      ...version,
      candidateRevisionId: mode === "missing" ? undefined : "current-revision",
    };
    const pending = deferred<ReturnType<typeof reply>>();
    const bundle = {
      namespaceId: resource.namespaceId,
      revision: {
        ...versionRevision,
        id: "current-revision",
        resourceVersionId: version.id,
        representations: [
          { ...representation, data: { name: "Current text" } },
          {
            ...representation,
            id: "secondary",
            role: "documentation",
            mediaType: "text/markdown",
            url: "https://metadata.example/readme.md",
          },
        ],
      },
      evidence,
    };
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/resource-versions/{versionId}")
        return {
          data: current,
          response: new Response(null, { headers: { ETag: '"revision-1"' } }),
        };
      if (path === "/api/v1/resources/{resourceId}") return reply(resource);
      if (path === "/api/v1/resource-versions/{versionId}/revisions")
        return reply({ items: [] });
      if (path === "/api/v1/version-revisions/{revisionId}") {
        if (
          mode === "changed" ||
          mode === "cancelled" ||
          mode === "cancelled-failure" ||
          mode === "permission-loss"
        )
          return pending.promise;
        if (mode === "unknown-failure")
          return Promise.reject("untrusted detail");
        if (mode === "failed")
          throw Error("Complete revision could not be loaded.");
        return reply(
          mode === "foreign" ? { ...bundle, namespaceId: "another" } : bundle,
        );
      }
      throw Error("Unexpected current-draft read");
    });
    const view = render(
      <Versions {...w.props} access={administrator} id={version.id} />,
    );
    await screen.findByRole("button", { name: "New revision" });
    click("New revision");
    expect(
      w.get.mock.calls.some(
        ([p]) => p === "/api/v1/version-revisions/{revisionId}",
      ),
    ).toBe(false);
    click("Start from current draft");
    if (mode === "permission-loss")
      view.rerender(
        <Versions
          {...w.props}
          id={version.id}
          access={{
            ...administrator,
            permissions: administrator.permissions.filter(
              (p) => p !== "resource.read_content",
            ),
            availablePermissions: administrator.availablePermissions.filter(
              (p) => p !== "resource.read_content",
            ),
          }}
        />,
      );
    if (mode === "changed")
      change("Standards document (JSON)", '{"name":"Typed during read"}');
    if (mode === "cancelled" || mode === "cancelled-failure")
      click("New revision");
    if (mode === "cancelled-failure")
      await act(async () => pending.reject(new Error("obsolete failure")));
    if (
      mode === "changed" ||
      mode === "cancelled" ||
      mode === "permission-loss"
    )
      await act(async () => pending.resolve(reply(bundle)));
    if (mode === "success") {
      await screen.findByText(/Starting from revision/);
      expect(
        within(
          screen.getByRole("group", { name: /content 1$/ }),
        ).getByLabelText("Content source"),
      ).toBe(document.activeElement);
      expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
        "value",
        JSON.stringify({ name: "Current text" }, null, 2),
      );
      expect(screen.getByLabelText("Document URL")).toHaveProperty(
        "value",
        "https://metadata.example/readme.md",
      );
      expect(
        screen.getAllByRole("group", { name: /content [12]$/ }),
      ).toHaveLength(2);
      vi.spyOn(window, "confirm").mockReturnValue(false);
      click("Start from current draft");
      expect(
        w.get.mock.calls.filter(
          ([p]) => p === "/api/v1/version-revisions/{revisionId}",
        ),
      ).toHaveLength(1);
      click("Start with blank replacement");
      expect(screen.getByLabelText("Document URL")).toBeTruthy();
      vi.mocked(window.confirm).mockReturnValue(true);
      click("Start with blank replacement");
      expect(screen.queryByLabelText("Document URL")).toBeNull();
      expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
        "value",
        "",
      );
    } else if (mode === "changed") {
      await screen.findByText(/Input changed while the draft was loading/);
      expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
        "value",
        '{"name":"Typed during read"}',
      );
    } else if (mode === "permission-loss") {
      expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
        "value",
        "",
      );
      expect(screen.queryByLabelText("Document URL")).toBeNull();
      expect(screen.queryByText(/Starting from revision/)).toBeNull();
    } else if (mode !== "cancelled" && mode !== "cancelled-failure")
      await screen.findByRole("alert");
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("blocks preparing old text while a file is pending or ready, then saves after explicit application", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/resources/{resourceId}": resource });
  w.post.mockResolvedValue(
    etagReply({
      version: { ...version, candidateRevisionId: versionRevision.id },
      revision: versionRevision,
      evidence,
    }),
  );
  render(<Versions {...w.props} access={administrator} id="new" />);
  await screen.findByLabelText("Version label");
  change("Version label", "1.0.0");
  change("Standards document (JSON)", '{"name":"Old"}');
  const pending = deferred<ArrayBuffer>();
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        { name: "new.json", size: 14, arrayBuffer: () => pending.promise },
      ],
    },
  });
  click("Create version");
  await screen.findByRole("alert");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
  await act(async () =>
    pending.resolve(new TextEncoder().encode('{"name":"New"}').buffer),
  );
  click("Create version");
  expect(screen.queryByRole("dialog")).toBeNull();
  vi.spyOn(window, "confirm").mockReturnValue(true);
  click("Use loaded document");
  expect(screen.getByLabelText("Standards document (JSON)")).toBe(
    document.activeElement,
  );
  await confirmCommand("Create version");
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    { body: { representations: [{ data: { name: "New" } }] } },
  ]);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
});

it("keeps blank replacement available without offering an unauthorized current-draft read", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resource-versions/{versionId}": version,
    "/api/v1/resource-versions/{versionId}/revisions": { items: [] },
  });
  render(
    <Versions
      {...w.props}
      id={version.id}
      access={{
        ...administrator,
        permissions: administrator.permissions.filter(
          (p) => p !== "resource.read_content",
        ),
        availablePermissions: administrator.availablePermissions.filter(
          (p) => p !== "resource.read_content",
        ),
      }}
    />,
  );
  await screen.findByRole("button", { name: "New revision" });
  click("New revision");
  expect(
    screen.queryByRole("button", { name: "Start from current draft" }),
  ).toBeNull();
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    "",
  );
  expect(
    w.get.mock.calls.some(
      ([p]) => p === "/api/v1/version-revisions/{revisionId}",
    ),
  ).toBe(false);
  expect(w.post).not.toHaveBeenCalled();
});
