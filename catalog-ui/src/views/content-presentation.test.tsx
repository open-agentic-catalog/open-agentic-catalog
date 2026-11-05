// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Namespaces from "./Namespaces";
import { APIError } from "../api/client";
import { confirmDiscard } from "./workspace";
import Publishers from "./Publishers";
import Publication from "./Publication";
import { RepresentationContent } from "./Versions";
import { representation } from "../../tests/unit/responses";
import { baseline, snapshot } from "../../tests/unit/publication-fixtures";
import {
  administrator,
  change,
  click,
  etagReply,
  namespace,
  publisher,
  readReplies,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

const validRepresentation: components["schemas"]["Representation"] = {
  ...representation,
  role: "primary",
  validationState: "valid",
};

screenLifecycle("/unit/resources");

it.each([
  ["namespace", Namespaces, namespace],
  ["publisher", Publishers, publisher],
] as const)(
  "keeps applied %s criteria visible while refreshing an unsent draft",
  async (kind, View, item) => {
    const w = screenWorkspace();
    w.get.mockImplementation(async () => etagReply({ items: [item] }));
    render(<View {...w.props} access={administrator} />);
    await screen.findByRole("link", { name: item.displayName });
    change("Exact display name", " Exact  日本語 ");
    click("Apply filters");
    const applied = screen.getByLabelText("Applied directory filters");
    await waitFor(() =>
      expect(applied.textContent).toContain('" Exact  日本語 "'),
    );
    change("Exact display name", "Unapplied");
    expect(applied.textContent).toContain("Edits are not applied");
    const reads = w.get.mock.calls.length;
    click("Refresh");
    await waitFor(() => expect(w.get.mock.calls.length).toBe(reads + 1));
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { displayName: " Exact  日本語 ", limit: 25 } },
    });
    expect(applied.textContent).toContain('" Exact  日本語 "');
    expect(screen.getByLabelText("Exact display name")).toHaveProperty(
      "value",
      "Unapplied",
    );
    click("Clear filters");
    expect(applied.textContent).toContain(`all ${kind}s`);
    expect(screen.getByLabelText("Exact display name")).toHaveProperty(
      "value",
      "",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  ["namespace", Namespaces, namespace, "Reason"],
  ["publisher", Publishers, publisher, "Metadata change reason"],
] as const)(
  "shows saved %s facts for an initial Reader and retains an editor's draft on access loss",
  async (kind, View, item, reason) => {
    const w = screenWorkspace();
    readReplies(w.get, { [`/api/v1/${kind}s/{${kind}Id}`]: item });
    const reader = {
      ...administrator,
      permissions: administrator.permissions.filter(
        (p) =>
          ![
            "namespace.manage",
            "publisher.manage",
            "publisher.approve_domain",
          ].includes(p),
      ),
    };
    const view = render(<View {...w.props} id={item.id} access={reader} />);
    await screen.findByRole("heading", { name: item.displayName });
    expect(screen.queryByLabelText(reason)).toBeNull();
    expect(screen.queryByLabelText("Display name")).toBeNull();
    expect(screen.getByText("Revision").nextElementSibling?.textContent).toBe(
      String(item.revision),
    );
    view.rerender(<View {...w.props} id={item.id} access={administrator} />);
    await screen.findByLabelText("Display name");
    change("Display name", "Unsaved name");
    change(reason, "Keep this draft");
    view.rerender(<View {...w.props} id={item.id} access={reader} />);
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Unsaved name",
    );
    expect(screen.getByLabelText(reason)).toHaveProperty(
      "value",
      "Keep this draft",
    );
    expect(screen.getByLabelText(reason).closest("fieldset")).toHaveProperty(
      "disabled",
      true,
    );
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    expect(confirmDiscard()).toBe(false);
    expect(discard).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
    view.rerender(
      <View
        {...w.props}
        id={item.id}
        access={{ ...reader, subject: "another-subject" }}
      />,
    );
    await screen.findByRole("heading", { name: item.displayName });
    expect(screen.queryByLabelText(reason)).toBeNull();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
  },
);

it("keeps an exact snapshot ahead of collapsed host setup and changes hosts only on Open", async () => {
  history.replaceState(
    null,
    "",
    `/unit/publication/${snapshot.id}?host=${snapshot.host}`,
  );
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/publication/hosts"
        ? { items: [{ host: snapshot.host, mode: "anonymous" }] }
        : {
            ...snapshot,
            ard: { entries: [] },
            aiCatalog: { entries: [] },
            exclusions: [],
          },
    ),
  );
  render(<Publication {...w.props} id={snapshot.id} access={administrator} />);
  await screen.findByRole("heading", {
    name: `Generation ${snapshot.generation}: ${snapshot.state}`,
  });
  const summary = screen.getByText("Change host");
  expect(summary.closest("details")).toHaveProperty("open", false);
  expect(
    screen.getByRole("region", { name: "Selected publication host" })
      .textContent,
  ).toContain(snapshot.host);
  const reads = w.get.mock.calls.length;
  fireEvent.click(summary);
  change("Configured discovery host", baseline.host + ".other");
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(
    screen.getByRole("region", { name: "Selected publication host" })
      .textContent,
  ).not.toContain(baseline.host + ".other");
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["anonymous", "authenticated"] as const)(
  "keeps a contradictory chooser audience out of exact %s snapshot context",
  async (audience) => {
    const staged = audience === "anonymous" ? "authenticated" : "anonymous";
    history.replaceState(
      null,
      "",
      `/unit/publication/${snapshot.id}?host=${snapshot.host}&audience=${staged}`,
    );
    const w = screenWorkspace();
    w.get.mockImplementation(async (path) =>
      etagReply(
        path === "/api/v1/publication/hosts"
          ? { items: [{ host: snapshot.host, mode: audience }] }
          : { ...snapshot, audience },
      ),
    );
    render(
      <Publication {...w.props} id={snapshot.id} access={administrator} />,
    );
    await screen.findByRole("heading", {
      name: `Generation ${snapshot.generation}: ${snapshot.state}`,
    });
    const selected = screen.getByRole("region", {
      name: "Selected publication host",
    });
    expect(within(selected).getByText(snapshot.host)).toBeTruthy();
    expect(within(selected).queryByText(/audience/i)).toBeNull();
    expect(within(selected).queryByText(/Anonymous|Authenticated/)).toBeNull();
    expect(screen.getByText(`${snapshot.host} / ${audience}`)).toBeTruthy();
    expect(screen.getByLabelText("Discovery audience")).toHaveProperty(
      "value",
      audience,
    );
    expect(w.get.mock.calls.map(([path]) => path).sort()).toEqual([
      "/api/v1/publication/hosts",
      "/api/v1/publication/snapshots/{snapshotId}",
    ]);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["git", "oci", "both"])(
  "presents exact %s Skill references without fetching or reconstructing an archive",
  async (mode) => {
    const w = screenWorkspace();
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const git = {
      repository: "https://example.com/skills",
      commit: "a".repeat(40),
      directory: "skills/helper",
    };
    const oci = {
      reference: `registry.example.com/skills/helper@sha256:${"b".repeat(64)}`,
    };
    render(
      <RepresentationContent
        {...w.props}
        item={{
          ...validRepresentation,
          id: "skill-package",
          mediaType: "application/agent-skills+zip",
          package: {
            structureState: "passed",
            expandedSize: 25,
            fileCount: 1,
            files: [
              {
                path: "helper/SKILL.md",
                size: 25,
                mediaType: "text/markdown",
                digest: "sha256:" + "c".repeat(64),
                executable: false,
              },
            ],
            assessments: {
              malware: "not_assessed",
              secrets: "not_assessed",
              license: "not_assessed",
              sbom: "not_assessed",
              provenance: "not_assessed",
            },
          },
          handoff: {
            ...(mode !== "oci" ? { git } : {}),
            ...(mode !== "git" ? { oci } : {}),
          },
        }}
      />,
    );
    const inspection = screen.getByRole("region", {
      name: "Skill package inspection",
    });
    expect(within(inspection).getByText("Passed")).toBeTruthy();
    expect(
      within(inspection).getByText(/complete archive is not stored/),
    ).toBeTruthy();
    if (mode !== "oci")
      for (const value of Object.values(git))
        expect(within(inspection).getByText(value)).toBeTruthy();
    if (mode !== "git")
      expect(within(inspection).getByText(oci.reference)).toBeTruthy();
    const copyLabel =
      mode === "oci" ? "OCI digest reference" : "Git repository";
    fireEvent.click(
      within(inspection).getByRole("button", { name: `Copy ${copyLabel}` }),
    );
    await within(inspection).findByText("Copied.");
    expect(writeText).toHaveBeenCalledExactlyOnceWith(
      mode === "oci" ? oci.reference : git.repository,
    );
    expect(screen.queryByRole("link")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Read retained document" }),
    ).toBeNull();
    expect(w.get).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps failed Skill package inspection distinct without inventing an inventory", () => {
  const w = screenWorkspace();
  render(
    <RepresentationContent
      {...w.props}
      item={{
        ...validRepresentation,
        package: {
          structureState: "failed",
          expandedSize: 0,
          fileCount: 0,
          assessments: {
            malware: "not_assessed",
            secrets: "not_assessed",
            license: "not_assessed",
            sbom: "not_assessed",
            provenance: "not_assessed",
          },
        },
      }}
    />,
  );
  expect(screen.getByText("Failed")).toBeTruthy();
  expect(screen.queryByText(/File inventory/)).toBeNull();
  expect(screen.queryByText(/undefined bytes/)).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
});

it.each([
  ["namespace", Namespaces],
  ["publisher", Publishers],
] as const)(
  "omits the initial read-only %s creation editor without blocking a later grant",
  async (_kind, View) => {
    const w = screenWorkspace();
    const reader = { ...administrator, permissions: [] };
    const view = render(<View {...w.props} id="new" access={reader} />);
    expect(screen.queryByLabelText("Display name")).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Create (namespace|publisher)/ }),
    ).toBeNull();
    view.rerender(<View {...w.props} id="new" access={administrator} />);
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "disabled",
      false,
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("protects an unsent publication-domain reason when approval permission is removed", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/publishers/{publisherId}": publisher });
  const view = render(
    <Publishers {...w.props} id={publisher.id} access={administrator} />,
  );
  await screen.findByLabelText("Publication approval reason");
  change(
    "Publication approval reason",
    "Keep the selected domain decision reason",
  );
  view.rerender(
    <Publishers
      {...w.props}
      id={publisher.id}
      access={{
        ...administrator,
        permissions: administrator.permissions.filter(
          (permission) => permission !== "publisher.approve_domain",
        ),
      }}
    />,
  );
  expect(screen.getByLabelText("Publication approval reason")).toHaveProperty(
    "value",
    "Keep the selected domain decision reason",
  );
  expect(
    screen.getByLabelText("Publication approval reason").closest("fieldset"),
  ).toHaveProperty("disabled", true);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(false);
  expect(screen.queryByRole("button", { name: "Approve domain" })).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps a disabled bookmarked publication host explicit while preserving the manual chooser after read refusal", async () => {
  const host = "disabled.example.com";
  history.replaceState(null, "", `/unit/publication?host=${host}`);
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/publication/hosts")
      return etagReply({ items: [{ host, mode: "disabled" }] });
    throw new APIError(403, "FORBIDDEN");
  });
  render(<Publication {...w.props} access={administrator} />);
  const selected = await screen.findByRole("region", {
    name: "Selected publication host",
  });
  await within(selected).findByText("Discovery disabled");
  await screen.findByRole("alert");
  expect(selected.textContent).toContain(host);
  fireEvent.click(screen.getByText("Change host", { exact: true }));
  expect(
    screen.getByRole("option", { name: `${host} — disabled` }),
  ).toHaveProperty("disabled", true);
  expect(screen.getByLabelText("Configured discovery host")).toHaveProperty(
    "value",
    host,
  );
  expect(w.post).not.toHaveBeenCalled();
});

it.each([
  ["namespace", Namespaces],
  ["publisher", Publishers],
] as const)(
  "distinguishes an empty %s inventory from empty applied-filter results",
  async (kind, View) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply({ items: [] }));
    render(<View {...w.props} access={administrator} />);
    await screen.findByText(`No ${kind}s on this page.`);
    change("Exact display name", " Exact missing name ");
    click("Apply filters");
    await screen.findByText(`No ${kind}s match the applied filters.`);
    expect(
      screen.getByLabelText("Applied directory filters").textContent,
    ).toContain('" Exact missing name "');
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { displayName: " Exact missing name ", limit: 25 } },
    });
    expect(w.post).not.toHaveBeenCalled();
  },
);
