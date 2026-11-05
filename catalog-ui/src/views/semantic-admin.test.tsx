// @vitest-environment jsdom
import { job } from "../../tests/unit/responses";
import { expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { APIError } from "../api/client";
import SemanticAdmin from "./SemanticAdmin";
import { baseline } from "../../tests/unit/publication-fixtures";
import {
  change,
  administrator,
  click,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/service");

function reindexWorkspace() {
  const w = screenWorkspace();
  w.props.access = administrator;
  return w;
}

it("shows current rebuild access requirements without empty mutation fields for a reader", () => {
  const w = screenWorkspace();
  render(<SemanticAdmin {...w.props} />);
  expect(screen.queryByLabelText("Reason")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Rebuild search index" }),
  ).toBeNull();
  expect(
    screen.getByText(/Rebuilding requires operations.reindex permission/),
  ).toBeTruthy();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it("chooses one observed active snapshot and keeps its captured population beside the accepted Job", async () => {
  const w = reindexWorkspace();
  w.props.access = administrator;
  w.get.mockImplementation(async (path) =>
    etagReply({
      items:
        path === "/api/v1/publication/hosts"
          ? [{ host: baseline.host, mode: "anonymous" }]
          : [baseline],
    }),
  );
  w.post.mockResolvedValue(etagReply(job));
  render(<SemanticAdmin {...w.props} />);
  expect(w.get).not.toHaveBeenCalled();
  change("Search index", "public");
  click("Choose active snapshot");
  await screen.findByRole("option", { name: `${baseline.host} / anonymous` });
  change("Publication host", baseline.host);
  expect(w.get).toHaveBeenCalledTimes(1);
  click("Read active snapshot");
  await screen.findByText(baseline.id);
  expect(w.get.mock.calls[1]).toMatchObject([
    "/api/v1/publication/snapshots",
    { params: { query: { host: baseline.host, state: "active", limit: 1 } } },
  ]);
  click("Use observed snapshot for reindexing");
  expect(screen.getByLabelText("Active snapshot ID")).toHaveProperty(
    "value",
    baseline.id,
  );
  change("Reason", "Rebuild observed public population");
  click("Rebuild search index");
  expect(screen.getByLabelText("Command summary").textContent).toContain(
    baseline.host,
  );
  expect(screen.getByLabelText("Command summary").textContent).toContain(
    "anonymous",
  );
  click("Confirm");
  await screen.findByRole("link", { name: "View indexing Job" });
  change("Search index", "control");
  expect(
    screen.getByLabelText("Accepted indexing request").textContent,
  ).toContain(baseline.id);
  expect(
    screen.getByLabelText("Accepted indexing request").textContent,
  ).toContain(baseline.host);
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/catalog:reindex",
    {
      body: {
        snapshotId: baseline.id,
        reason: "Rebuild observed public population",
      },
    },
  ]);
  expect(w.get).toHaveBeenCalledTimes(2);
});

it.each(["permission", "scope"])(
  "keeps manual snapshot entry without the publication %s and makes no choice reads",
  async (missing) => {
    const w = reindexWorkspace();
    w.props.access = {
      ...administrator,
      permissions:
        missing === "permission"
          ? ["operations.reindex"]
          : ["operations.reindex", "publication.read"],
      scopeEnforcement: true,
      scopes: ["tenant.admin"],
    };
    w.post.mockResolvedValue(etagReply(job));
    render(<SemanticAdmin {...w.props} />);
    change("Search index", "public");
    expect(
      screen.queryByRole("button", { name: "Choose active snapshot" }),
    ).toBeNull();
    change("Active snapshot ID", baseline.id);
    change("Reason", "Manual exact snapshot without publication grants");
    await confirmCommand("Rebuild search index");
    await screen.findByText(job.id);
    expect(w.get).not.toHaveBeenCalled();
    expect(w.post.mock.calls[0]).toMatchObject([
      "/api/v1/catalog:reindex",
      { body: { snapshotId: baseline.id } },
    ]);
  },
);

it.each([
  "inventory unavailable",
  "empty",
  "wrong tenant",
  "wrong host",
  "retired",
  "extra row",
  "continuation",
])(
  "keeps manual input and refuses an unqualified active choice: %s",
  async (kind) => {
    const w = reindexWorkspace();
    w.props.access = administrator;
    const item = {
      ...baseline,
      ...(kind === "wrong tenant" ? { tenantId: "other" } : {}),
      ...(kind === "wrong host" ? { host: "other.example.com" } : {}),
      ...(kind === "retired" ? { state: "retired" } : {}),
    };
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/publication/hosts") {
        if (kind === "inventory unavailable")
          throw new APIError(503, "DEPENDENCY_UNAVAILABLE");
        return etagReply({
          items: [{ host: baseline.host, mode: "anonymous" }],
        });
      }
      return etagReply({
        items:
          kind === "empty"
            ? []
            : kind === "extra row"
              ? [item, { ...item, id: job.id }]
              : [item],
        ...(kind === "continuation" ? { nextCursor: "next" } : {}),
      });
    });
    render(<SemanticAdmin {...w.props} />);
    change("Search index", "public");
    change("Active snapshot ID", baseline.id);
    change("Reason", "Preserve manual population and reason");
    click("Choose active snapshot");
    if (kind !== "inventory unavailable") {
      await screen.findByRole("option", {
        name: `${baseline.host} / anonymous`,
      });
      change("Publication host", baseline.host);
      click("Read active snapshot");
    }
    if (kind === "empty")
      await screen.findByText("No active snapshot at this observation.");
    else await screen.findByRole("alert");
    expect(
      screen.queryByRole("button", {
        name: "Use observed snapshot for reindexing",
      }),
    ).toBeNull();
    expect(screen.getByLabelText("Active snapshot ID")).toHaveProperty(
      "value",
      baseline.id,
    );
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Preserve manual population and reason",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  ["foreign tenant", { ...job, tenantId: "tenant-b" }],
  ["wrong family", { ...job, kind: "source.sync" }],
  ["missing tenant", { ...job, tenantId: undefined }],
])(
  "keeps a %s reindex acknowledgement unknown until exact-key recovery",
  async (_name, malformed) => {
    const w = reindexWorkspace();
    w.post
      .mockResolvedValueOnce(etagReply(malformed))
      .mockResolvedValueOnce(etagReply(job));
    render(<SemanticAdmin {...w.props} />);
    change("Reason", "Rebuild exact population");
    await confirmCommand("Rebuild search index");
    expect(screen.getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      screen.queryByRole("link", { name: "View indexing Job" }),
    ).toBeNull();
    click("Retry same command");
    await screen.findByRole("link", { name: "View indexing Job" });
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(w.get).not.toHaveBeenCalled();
  },
);

it.each(["control", "public"])(
  "confirms a %s rebuild and links the returned Job",
  async (scope) => {
    const { props, post } = reindexWorkspace();
    const id = "00000000-0000-7000-8000-000000000099";
    post.mockResolvedValue(
      etagReply({ ...job, id, kind: "semantic.reindex", state: "queued" }),
    );
    render(<SemanticAdmin {...props} />);
    change("Search index", scope);
    if (scope === "public") change("Active snapshot ID", id);
    change("Reason", "Rebuild search metadata");
    click("Rebuild search index");
    expect(screen.getByLabelText("Command summary").textContent).toContain(
      scope === "public" ? "Public discovery snapshot" : "Catalog search",
    );
    expect(screen.getByLabelText("Command summary").textContent).toContain(
      scope === "public" ? id : "Not applicable to Catalog search",
    );
    expect(screen.getByLabelText("Command summary").textContent).toContain(
      "Rebuild search metadata",
    );
    expect(post).not.toHaveBeenCalled();
    click("Cancel");
    await confirmCommand("Rebuild search index");
    expect(
      (
        await screen.findByRole("link", { name: "View indexing Job" })
      ).getAttribute("href"),
    ).toBe(`/unit/jobs/${id}`);
    expect(post).toHaveBeenCalledTimes(1);
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/catalog:reindex",
      {
        params: {
          header: { "Idempotency-Key": expect.any(String) as unknown },
        },
        body: {
          reason: "Rebuild search metadata",
          ...(scope === "public" ? { snapshotId: id } : {}),
        },
      },
    ]);
  },
);

it("does not submit a hidden snapshot when switching back to Catalog search", async () => {
  const { props, post } = reindexWorkspace();
  post.mockResolvedValue(etagReply({ ...job, id: "job-control" }));
  render(<SemanticAdmin {...props} />);
  change("Search index", "public");
  change("Active snapshot ID", "00000000-0000-7000-8000-000000000099");
  change("Search index", "control");
  expect(screen.queryByLabelText("Active snapshot ID")).toBeNull();
  change("Reason", "  Rebuild metadata  ");
  await confirmCommand("Rebuild search index");
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: { reason: "Rebuild metadata" },
  });
  expect((post.mock.calls[0]?.[1] as { body: object }).body).not.toHaveProperty(
    "snapshotId",
  );
});

it("keeps a rejected snapshot rebuild editable without claiming a Job was created", async () => {
  const { props, post } = reindexWorkspace();
  const error = new APIError(409, "CONFLICT", "reindex-request");
  post.mockRejectedValue(error);
  render(<SemanticAdmin {...props} />);
  change("Search index", "public");
  change("Active snapshot ID", "00000000-0000-7000-8000-000000000099");
  change("Reason", "Rebuild published metadata");
  await confirmCommand("Rebuild search index");
  expect(screen.getByRole("alert").textContent).toContain("reindex-request");
  expect(screen.queryByRole("link", { name: "View indexing Job" })).toBeNull();
  click("Back to form");
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(error);
  expect((screen.getByLabelText("Reason") as HTMLTextAreaElement).value).toBe(
    "Rebuild published metadata",
  );
  expect(post).toHaveBeenCalledTimes(1);
});

it.each(["permission", "scope"])(
  "reports acceptance without a Job link when the read %s is missing",
  async (missing) => {
    const { props, post } = reindexWorkspace();
    post.mockResolvedValue(
      etagReply({ ...job, id: "accepted-job", state: "queued" }),
    );
    render(
      <SemanticAdmin
        {...props}
        access={{
          ...props.access,
          permissions:
            missing === "permission"
              ? ["operations.reindex"]
              : ["operations.reindex", "job.read"],
          scopeEnforcement: true,
          scopes: ["tenant.admin"],
        }}
      />,
    );
    change("Reason", "Rebuild derived data");
    await confirmCommand("Rebuild search index");
    await screen.findByText("accepted-job");
    expect(
      screen.queryByRole("link", { name: "View indexing Job" }),
    ).toBeNull();
    expect(screen.getByText(/Acceptance is not completion/)).toBeTruthy();
    expect(
      screen.getByText(/Inspecting progress requires job.read/),
    ).toBeTruthy();
    expect(post).toHaveBeenCalledOnce();
  },
);

it.each([
  ["queued", "Queued"],
  ["running", "Running"],
  ["waiting", "Waiting"],
] as const)(
  "reports reused %s indexing jobs as accepted observations",
  async (state, label) => {
    const w = reindexWorkspace();
    w.props.access = administrator;
    w.post.mockResolvedValue(etagReply({ ...job, state }));
    render(<SemanticAdmin {...w.props} />);
    change("Reason", "Rebuild this Catalog search index");
    await confirmCommand("Rebuild search index");
    await screen.findByText(
      "Rebuild request accepted. View the job to check progress.",
    );
    const accepted = screen.getByLabelText("Accepted indexing request");
    expect(accepted.textContent).toContain("Acceptance is not completion.");
    expect(accepted.querySelector("dt")?.textContent).toBe("Job ID");
    expect(
      screen.getByText("State when accepted").nextElementSibling?.textContent,
    ).toBe(label);
    expect(
      screen.getByText("Search index", { selector: "dt" }).nextElementSibling
        ?.textContent,
    ).toBe("Catalog search");
    expect(w.post).toHaveBeenCalledOnce();
    expect(w.get).not.toHaveBeenCalled();
  },
);
