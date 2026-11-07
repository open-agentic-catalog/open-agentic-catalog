import { testAccess } from "../../tests/unit/access-fixtures";
// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import Jobs from "./Jobs";
import { APIError } from "../api/client";
import type { components } from "../api/schema";
import { installDialog, reply, workspace } from "../../tests/unit/fixtures";

const job: components["schemas"]["Job"] = {
  id: "job-1",
  tenantId: "tenant-a",
  kind: "source.sync",
  state: "queued",
  priority: 0,
  progress: 0,
  attempt: 0,
  attemptLimit: 3,
  cancellationRequested: false,
  notBefore: "2026-09-01T12:00:00Z",
  createdAt: "2026-09-01T12:00:00Z",
  updatedAt: "2026-09-01T12:00:00Z",
};

// Generic Reader grants do not read a Job family. These transport controls use
// a custom source-read grant; they do not broaden the Reader template/bundle.
function jobWorkspace() {
  const fixture = workspace();
  fixture.props.access = {
    ...fixture.props.access,
    permissions: [...fixture.props.access.permissions, "source.read"],
    availablePermissions: [
      ...fixture.props.access.availablePermissions,
      "source.read",
    ],
  };
  return fixture;
}

it.each(["complete", "incomplete", "failed"] as const)(
  "shows the exact %s source Job outcome without prefetch",
  async (state) => {
    const w = jobWorkspace();
    w.get.mockResolvedValue(
      reply({
        ...job,
        result: {
          type: "source_snapshot",
          sourceId: "source-1",
          snapshotId: "snapshot-1",
          state,
        },
      }),
    );
    render(<Jobs {...w.props} id={job.id} />);
    const link = await screen.findByRole("link", {
      name: "Inspect source snapshot",
    });
    expect(link.getAttribute("href")).toBe("/unit/source-snapshots/snapshot-1");
    expect(
      screen
        .getByRole("link", { name: "Open resulting source" })
        .getAttribute("href"),
    ).toBe("/unit/sources/source-1");
    expect(
      screen.getByText(
        state === "complete"
          ? /produced a complete source snapshot/
          : /did not replace the last complete/,
      ),
    ).toBeTruthy();
    expect(w.get).toHaveBeenCalledTimes(1);
  },
);

it.each(["candidate", "active", "retired"] as const)(
  "labels publication Job result %s without inferring current activation",
  async (state) => {
    const w = jobWorkspace();
    const access = {
      ...w.props.access,
      permissions: [...w.props.access.permissions, "publication.read" as const],
    };
    w.get.mockResolvedValue(
      reply({
        ...job,
        kind: "publication.generate",
        result: {
          type: "publication_snapshot",
          snapshotId: "snapshot-2",
          host: "private.catalog.test",
          generation: 3,
          state,
        },
      }),
    );
    render(<Jobs {...w.props} access={access} id={job.id} />);
    expect(
      (
        await screen.findByRole("link", {
          name: "Inspect resulting publication snapshot",
        })
      ).getAttribute("href"),
    ).toBe("/unit/publication/snapshot-2?host=private.catalog.test");
    expect(
      screen.getByText(
        state === "active"
          ? /inspect its current state/
          : /not proof of current discovery activation/,
      ),
    ).toBeTruthy();
    expect(w.get).toHaveBeenCalledTimes(1);
  },
);

it("keeps source references literal when the source OAuth ceiling denies navigation", async () => {
  const w = jobWorkspace();
  w.get.mockResolvedValue(
    reply({
      ...job,
      result: {
        type: "source_snapshot",
        sourceId: "source-1",
        snapshotId: "snapshot-1",
        state: "complete",
      },
    }),
  );
  render(
    <Jobs
      {...w.props}
      id={job.id}
      access={{
        ...w.props.access,
        scopeEnforcement: true,
        scopes: ["catalog.read"],
      }}
    />,
  );
  await screen.findByRole("heading", { name: "Result" });
  expect(
    screen.queryByRole("link", { name: "Open resulting source" }),
  ).toBeNull();
  expect(
    screen.queryByRole("link", { name: "Inspect source snapshot" }),
  ).toBeNull();
});

let restoreDialog: () => void;
beforeEach(() => {
  history.replaceState(null, "", "/unit/jobs");
  restoreDialog = installDialog();
});

it("applies exact Job filters, resets continuation, clears them and restores browser history", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply({ items: [], nextCursor: "next-page" }));
  const view = render(<Jobs {...props} />);
  await screen.findByText("No jobs on this page.");
  fireEvent.change(screen.getByLabelText("Job type"), {
    target: { value: "custom" },
  });
  fireEvent.change(screen.getByLabelText("Exact job type code"), {
    target: { value: " source.sync " },
  });
  fireEvent.change(screen.getByLabelText("Job state"), {
    target: { value: "waiting" },
  });
  fireEvent.change(screen.getByLabelText("Retries of job ID"), {
    target: { value: "00000000-0000-7000-8000-000000000abc" },
  });
  expect(get).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Apply Job filters" }));
  const query = {
    limit: 25,
    kind: "source.sync",
    state: "waiting",
    parentJobId: "00000000-0000-7000-8000-000000000abc",
  };
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({ params: { query } }),
  );
  expect(location.search).toBe(
    "?kind=source.sync&state=waiting&parentJobId=00000000-0000-7000-8000-000000000abc",
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { ...query, cursor: "next-page" } },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Clear Job filters" }));
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    }),
  );
  expect(location.search).toBe("");
  await act(async () => {
    history.replaceState(
      null,
      "",
      "/unit/jobs?kind=unknown.kind&state=unknown",
    );
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(screen.getByLabelText("Exact job type code")).toHaveProperty(
    "value",
    "unknown.kind",
  );
  expect(screen.getByLabelText("Job state")).toHaveProperty("value", "unknown");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { limit: 25, kind: "unknown.kind", state: "unknown" } },
    }),
  );
  const calls = get.mock.calls.length;
  await act(async () => {
    history.replaceState(null, "", "/unit/sources");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(get).toHaveBeenCalledTimes(calls);
  view.unmount();
});

it("loads an unknown exact Job type from the URL and avoids duplicate history or reads for unchanged filters", async () => {
  history.replaceState(
    null,
    "",
    "/unit/jobs?kind=extension.scan&state=waiting",
  );
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply({ items: [] }));
  const push = vi.spyOn(history, "pushState");
  const mutate = vi.fn(() => {
    throw new Error("Job filter controls must not mutate state");
  });
  Object.assign(props.client.api, {
    POST: mutate,
    PATCH: mutate,
    PUT: mutate,
    DELETE: mutate,
  });
  render(<Jobs {...props} />);
  await screen.findByText("No jobs match these filters.");
  expect(screen.getByLabelText("Job type")).toHaveProperty("value", "custom");
  expect(screen.getByLabelText("Exact job type code")).toHaveProperty(
    "value",
    "extension.scan",
  );
  expect(get.mock.calls[0]?.[0]).toBe("/api/v1/jobs");
  expect(
    (get.mock.calls[0]?.[1] as { params: { query: unknown } }).params.query,
  ).toEqual({ limit: 25, kind: "extension.scan", state: "waiting" });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Apply Job filters" }));
  });
  expect(get).toHaveBeenCalledOnce();
  expect(push).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Job type"), {
    target: { value: "source.sync" },
  });
  expect(screen.queryByLabelText("Exact job type code")).toBeNull();
  expect(screen.getByLabelText("Job type")).toHaveProperty(
    "value",
    "source.sync",
  );
  expect(get).toHaveBeenCalledOnce();
  fireEvent.click(screen.getByRole("button", { name: "Apply Job filters" }));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  expect(
    (get.mock.calls[1]?.[1] as { params: { query: unknown } }).params.query,
  ).toEqual({ limit: 25, kind: "source.sync", state: "waiting" });
  expect(push).toHaveBeenCalledExactlyOnceWith(
    null,
    "",
    "/unit/jobs?kind=source.sync&state=waiting",
  );
  expect(location.pathname + location.search).toBe(
    "/unit/jobs?kind=source.sync&state=waiting",
  );
  expect(props.onFailure).not.toHaveBeenCalled();
  expect(mutate).not.toHaveBeenCalled();
});

it("holds filter submission while loading and prepares an exact subpath Job destination", async () => {
  const { props, get } = jobWorkspace();
  let resolve!: (value: ReturnType<typeof reply>) => void;
  get.mockReturnValue(
    new Promise((done) => {
      resolve = done;
    }),
  );
  render(<Jobs {...props} />);
  expect(
    screen.getByRole("button", { name: "Apply Job filters" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByRole("button", { name: "Clear Job filters" }),
  ).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "Open Job" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.change(screen.getByLabelText("Exact Job ID"), {
    target: { value: "00000000-0000-7000-8000-000000000ABC" },
  });
  expect(
    screen.getByRole("form", { name: "Open exact Job" }).getAttribute("action"),
  ).toBe("/unit/jobs/00000000-0000-7000-8000-000000000abc");
  expect(
    screen.getByRole("form", { name: "Open exact Job" }).getAttribute("method"),
  ).toBe("get");
  expect(get).toHaveBeenCalledOnce();
  await act(async () => resolve(reply({ items: [] })));
  expect(
    screen.getByRole("button", { name: "Apply Job filters" }),
  ).toHaveProperty("disabled", false);
});

it("shows accounted semantic inputs including zero without claiming current readiness", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(
    reply({
      ...job,
      kind: "semantic.reindex",
      state: "succeeded",
      progress: 100,
      result: { type: "semantic_index", generationId: job.id, members: 0 },
    }),
  );
  render(
    <Jobs
      {...props}
      access={{ ...props.access, ...testAccess(["tenant_admin"]) }}
      id={job.id}
    />,
  );
  await screen.findByText(/Accounted inputs: 0/);
  expect(
    screen.getByRole("progressbar", { name: "Reported Job progress" }),
  ).toHaveProperty("value", 100);
  expect(screen.getByText(/including reused|includes reused/)).toBeTruthy();
  expect(
    screen.getByText(/Check the job state to confirm completion/),
  ).toBeTruthy();
  expect(screen.getByText(/Cancel unavailable:/).textContent).toContain(
    "Only queued",
  );
  expect(screen.getByText(/Retry unavailable:/).textContent).toContain(
    "Only failed",
  );
  expect(get).toHaveBeenCalledOnce();
});

it.each([
  ["cancel", { ...job, id: "other-job" }],
  ["cancel", { ...job, tenantId: "tenant-b" }],
  ["cancel", { ...job, kind: "publication.generate" }],
  ["retry", { ...job, parentJobId: job.id }],
  ["retry", { ...job, id: job.id.toUpperCase(), parentJobId: job.id }],
  ["retry", { ...job, id: "child", parentJobId: "another-parent" }],
  ["retry", { ...job, id: "child", parentJobId: job.id, tenantId: "tenant-b" }],
  [
    "retry",
    { ...job, id: "child", parentJobId: job.id, kind: "semantic.reindex" },
  ],
] as const)(
  "keeps a misbound %s acknowledgement unknown and recovers the captured command",
  async (action, malformed) => {
    const { props, get } = jobWorkspace();
    get.mockResolvedValue(
      reply({ ...job, state: action === "retry" ? "failed" : "queued" }),
    );
    const valid =
      action === "retry"
        ? { ...job, id: "child", parentJobId: job.id }
        : { ...job, state: "cancelled" };
    const post = vi
      .fn()
      .mockResolvedValueOnce(reply(malformed))
      .mockResolvedValueOnce(reply(valid));
    Object.assign(props.client.api, { POST: post });
    render(
      <Jobs
        {...props}
        access={{ ...props.access, ...testAccess(["source_admin"]) }}
        id={job.id}
      />,
    );
    await screen.findByLabelText("Reason");
    fireEvent.change(screen.getByLabelText("Reason"), {
      target: { value: "Investigate exact command" },
    });
    fireEvent.click(
      screen.getByRole("button", {
        name: action === "retry" ? "Retry job" : "Cancel job",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await screen.findByText(/The outcome is unknown/);
    expect(screen.queryByRole("link", { name: "Open retry job" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry same command" }));
    await screen.findByText(
      action === "retry"
        ? "Retry accepted. New job state: Queued."
        : "Cancellation response: Cancelled. Reload to check current state.",
    );
    expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
    expect(post.mock.calls[0]?.[1]).not.toHaveProperty(
      "params.header.If-Match",
    );
    expect(get).toHaveBeenCalledOnce();
  },
);
afterEach(() => {
  cleanup();
  restoreDialog();
  vi.restoreAllMocks();
});

it("preserves a cancellation acknowledgement through failed readback without repeating the command", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply(job));
  const post = vi.fn().mockResolvedValue(reply({ ...job, state: "cancelled" }));
  Object.assign(props.client.api, { POST: post });
  render(
    <Jobs
      {...props}
      id={job.id}
      access={{ ...props.access, ...testAccess(["source_admin"]) }}
    />,
  );
  await screen.findByRole("button", { name: "Cancel job" });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Cancel owned work" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Cancel job" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText(
    "Cancellation response: Cancelled. Reload to check current state.",
  );
  expect(screen.getByText("Queued")).toBeTruthy();
  get.mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
  fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
  await screen.findByRole("alert");
  expect(
    screen.getByText(
      "Cancellation response: Cancelled. Reload to check current state.",
    ),
  ).toBeTruthy();
  expect(screen.getByText("Queued")).toBeTruthy();
  get.mockResolvedValue(reply({ ...job, state: "cancelled" }));
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByText("Cancelled");
  expect(screen.queryByText(/not been reread/)).toBeNull();
  expect(post).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledTimes(3);
});

it("does not fetch job evidence without global read access", () => {
  const { props, get } = jobWorkspace();
  render(<Jobs {...props} access={{ ...props.access, ...testAccess([]) }} />);
  expect(screen.getByRole("alert").textContent).toContain("requires job.read");
  expect(get).not.toHaveBeenCalled();
});

it.each(["", job.id])(
  "explains missing family access without reading Job route %s",
  (id) => {
    const { props, get } = workspace();
    render(<Jobs {...props} id={id} />);
    expect(screen.getByRole("alert").textContent).toContain(
      "Your access does not include source, publication or search-indexing jobs",
    );
    expect(
      screen.getByRole("link", { name: "My access" }).getAttribute("href"),
    ).toBe("/unit/access");
    expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
    expect(get).not.toHaveBeenCalled();
  },
);

it("distinguishes empty authorized history and clears it when family access is lost", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply({ items: [] }));
  const view = render(<Jobs {...props} />);
  await screen.findByText("No jobs on this page.");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/jobs",
    expect.objectContaining({ params: { query: { limit: 25 } } }),
  );
  view.rerender(
    <Jobs {...props} access={{ ...props.access, ...testAccess(["reader"]) }} />,
  );
  expect(screen.getByRole("alert").textContent).toContain(
    "Your access does not include",
  );
  expect(screen.queryByText("No jobs on this page.")).toBeNull();
  expect(get).toHaveBeenCalledTimes(1);
});

it("links an ANS Job to explicit private lookup without reading its evidence", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply({ ...job, kind: "source.trust_verify" }));
  const view = render(
    <Jobs
      {...props}
      access={{ ...props.access, ...testAccess(["tenant_admin"]) }}
      id={job.id}
    />,
  );
  expect(
    (
      await screen.findByRole("link", { name: "Open private ANS assessment" })
    ).getAttribute("href"),
  ).toBe(`/unit/trust-assessments/${job.id}`);
  expect(
    screen.getByText(/This generic Job has no Source context/),
  ).toBeTruthy();
  expect(get).toHaveBeenCalledTimes(1);
  view.rerender(
    <Jobs
      {...props}
      access={{ ...props.access, ...testAccess(["reader"]) }}
      id={job.id}
    />,
  );
  expect(screen.getByRole("alert").textContent).toContain(
    "Your access does not include",
  );
  expect(
    screen.queryByRole("link", { name: "Open private ANS assessment" }),
  ).toBeNull();
  expect(screen.queryByText(job.id)).toBeNull();
  expect(get).toHaveBeenCalledTimes(1);
});

it("preserves an explicit Source reference on the ANS Job return link without treating it as Job payload", async () => {
  const sourceId = "00000000-0000-7000-8000-000000000099";
  history.replaceState(null, "", `/unit/jobs/${job.id}?sourceId=${sourceId}`);
  const w = jobWorkspace();
  w.get.mockResolvedValue(reply({ ...job, kind: "source.trust_verify" }));
  render(
    <Jobs
      {...w.props}
      access={{ ...w.props.access, ...testAccess(["tenant_admin"]) }}
      id={job.id}
    />,
  );
  expect(
    (
      await screen.findByRole("link", { name: "Open private ANS assessment" })
    ).getAttribute("href"),
  ).toBe(`/unit/trust-assessments/${job.id}?sourceId=${sourceId}`);
  expect(
    screen.getByText(/The Source ID comes from the navigation link/),
  ).toBeTruthy();
  expect(w.get).toHaveBeenCalledOnce();
});

it.each([
  ["semantic.reindex", "Search indexing"],
  ["source.trust_verify", "ANS registration assessment"],
  ["future.kind", "future.kind"],
])("labels %s jobs while preserving their exact kind", async (kind, label) => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply({ ...job, kind }));
  render(<Jobs {...props} id={job.id} />);
  await screen.findByRole("heading", { name: label });
  expect(
    screen.getByText("Job type").nextElementSibling?.textContent,
  ).toContain(kind);
  expect(
    screen.getByText("Job type").nextElementSibling?.textContent,
  ).toContain(label);
  expect(get).toHaveBeenCalledTimes(1);
});

it("pages retained jobs and refreshes the selected page manually", async () => {
  const { props, get } = jobWorkspace();
  get
    .mockResolvedValueOnce(reply({ items: [job], nextCursor: "next-1" }))
    .mockResolvedValueOnce(reply({ items: [] }))
    .mockResolvedValueOnce(reply({ items: [] }))
    .mockResolvedValueOnce(reply({ items: [job] }));
  render(<Jobs {...props} />);
  expect(
    (
      await screen.findByRole("link", { name: "Source synchronization" })
    ).getAttribute("href"),
  ).toBe("/unit/jobs/job-1");
  expect(get.mock.calls[0]).toMatchObject([
    "/api/v1/jobs",
    { params: { query: { limit: 25 } } },
  ]);
  expect(get.mock.calls[0]).not.toHaveProperty("1.params.query.cursor");
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByText("No jobs on this page.");
  expect(get.mock.calls[1]?.[1]).toMatchObject({
    params: { query: { cursor: "next-1" } },
  });
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(3));
  await screen.findByText("No jobs on this page.");
  expect(get.mock.calls[2]?.[1]).toMatchObject({
    params: { query: { cursor: "next-1" } },
  });
  fireEvent.click(screen.getByRole("button", { name: "First page" }));
  await screen.findByRole("link", { name: "Source synchronization" });
  expect(get.mock.calls[3]?.[1]).toMatchObject({
    params: { query: {} },
  });
  expect(get.mock.calls[3]?.[1]).not.toHaveProperty("params.query.cursor");
});

it("restarts a rejected Job continuation without a cursor only after explicit reload", async () => {
  const { props, get } = jobWorkspace();
  get
    .mockResolvedValueOnce(reply({ items: [job], nextCursor: "expired-page" }))
    .mockRejectedValueOnce(new APIError(400, "invalid_cursor", "job-page"))
    .mockResolvedValueOnce(reply({ items: [job] }));
  render(<Jobs {...props} />);
  await screen.findByRole("link", { name: "Source synchronization" });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  expect(get.mock.calls[1]?.[1]).toMatchObject({
    params: { query: { limit: 25, cursor: "expired-page" } },
  });
  expect(
    screen.queryByRole("link", { name: "Source synchronization" }),
  ).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("link", { name: "Source synchronization" });
  expect(get).toHaveBeenCalledTimes(3);
  expect(get.mock.calls[2]?.[1]).toMatchObject({
    params: { query: { limit: 25 } },
  });
  expect(get.mock.calls[2]?.[1]).not.toHaveProperty("params.query.cursor");
  expect(screen.queryByRole("button", { name: "First page" })).toBeNull();
});

it("shows retained outcome and parent evidence without granting a source reader job controls", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(
    reply({
      ...job,
      state: "failed",
      cancellationRequested: true,
      parentJobId: "original-1",
      failureClass: "dependency",
      safeError: "Source unavailable",
      result: {
        type: "source_snapshot",
        snapshotId: "snapshot-1",
        sourceId: "source-1",
        state: "incomplete",
      },
    } satisfies components["schemas"]["Job"]),
  );
  render(<Jobs {...props} id={job.id} />);
  await screen.findByText("Source unavailable");
  expect(screen.getByText("Yes")).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "original-1" }).getAttribute("href"),
  ).toBe("/unit/jobs/original-1");
  expect(
    screen.getByRole("heading", { name: "Result" }).nextElementSibling
      ?.textContent,
  ).toContain("snapshot-1");
  expect(screen.queryByRole("textbox", { name: "Reason" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry job" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
});

it("distinguishes an active cancellation request from its retained terminal flag after explicit reload", async () => {
  const { props, get } = jobWorkspace();
  get
    .mockResolvedValueOnce(
      reply({
        ...job,
        state: "running",
        attempt: 1,
        progress: 20,
        cancellationRequested: true,
      } satisfies components["schemas"]["Job"]),
    )
    .mockResolvedValue(
      reply({
        ...job,
        state: "cancelled",
        attempt: 1,
        progress: 20,
        cancellationRequested: true,
      } satisfies components["schemas"]["Job"]),
    );
  const mutate = vi.fn(() => {
    throw new Error("A retained cancellation flag must not submit a command");
  });
  Object.assign(props.client.api, {
    POST: mutate,
    PATCH: mutate,
    PUT: mutate,
    DELETE: mutate,
  });
  render(<Jobs {...props} id={job.id} />);
  expect((await screen.findByText("Yes")).getAttribute("data-tone")).toBe(
    "warning",
  );
  expect(get).toHaveBeenCalledOnce();
  fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
  await screen.findByText("Cancelled");
  expect(screen.getByText("Yes").getAttribute("data-tone")).toBe("neutral");
  expect(
    screen.getByText(
      "Cancellation was requested. Check the recorded state above; completed side effects are not undone.",
    ),
  ).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Cancel job" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry job" })).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
  expect(get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/jobs/{jobId}",
    "/api/v1/jobs/{jobId}",
  ]);
  expect(props.onFailure).not.toHaveBeenCalled();
  expect(mutate).not.toHaveBeenCalled();
});

it.each([
  [
    "source.sync",
    "cancelled",
    false,
    "Cancellation response: Cancelled. Reload to check current state.",
  ],
  [
    "source.sync",
    "running",
    true,
    "Cancellation response: Running; cancellation requested. Reload to check current state.",
  ],
  [
    "source.trust_verify",
    "cancelled",
    false,
    "Cancellation response: Cancelled. Reload to check current state.",
  ],
  [
    "source.trust_verify",
    "running",
    true,
    "Cancellation response: Running; cancellation requested. Reload to check current state.",
  ],
] as const)(
  "confirms %s cancellation and reports %s without claiming rollback",
  async (kind, state, cancellationRequested, message) => {
    const { props, get } = jobWorkspace();
    get.mockResolvedValue(
      reply({
        ...job,
        kind,
        state: state === "running" ? "running" : "queued",
      }),
    );
    const post = vi
      .fn()
      .mockResolvedValue(reply({ ...job, kind, state, cancellationRequested }));
    Object.assign(props.client.api, { POST: post });
    render(
      <Jobs
        {...props}
        id={job.id}
        access={{ ...props.access, ...testAccess(["reader", "source_admin"]) }}
      />,
    );
    const button = await screen.findByRole("button", { name: "Cancel job" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByRole("textbox", { name: "Reason" }), {
      target: { value: "  Stop duplicate work  " },
    });
    fireEvent.click(button);
    expect(screen.getByRole("dialog").textContent).toContain(
      "completed side effects are not undone",
    );
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await screen.findByText(message);
    expect(post).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/jobs/{jobId}:cancel",
      {
        params: {
          path: { jobId: job.id },
          header: { "Idempotency-Key": expect.any(String) as unknown },
        },
        body: { reason: "Stop duplicate work" },
      },
    );
    expect(screen.queryByRole("textbox", { name: "Reason" })).toBeNull();
    expect(get).toHaveBeenCalledTimes(1);
    const confirm = vi.spyOn(window, "confirm");
    fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    expect(confirm).not.toHaveBeenCalled();
  },
);

it.each([
  ["publication.generate", "publisher"],
  ["source.trust_verify", "source_admin"],
] as const)(
  "retries %s with same-key recovery and retains the child reference across parent readback",
  async (kind, role) => {
    const { props, get } = jobWorkspace();
    get.mockResolvedValue(reply({ ...job, kind, state: "failed" }));
    const post = vi
      .fn()
      .mockResolvedValueOnce(reply({}))
      .mockResolvedValue(
        reply({ ...job, kind, id: "retry-1", parentJobId: job.id }),
      );
    Object.assign(props.client.api, { POST: post });
    render(
      <Jobs
        {...props}
        id={job.id}
        access={{ ...props.access, ...testAccess(["reader", role]) }}
      />,
    );
    await screen.findByRole("button", { name: "Retry job" });
    fireEvent.change(screen.getByRole("textbox", { name: "Reason" }), {
      target: { value: "  Dependency restored  " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Retry job" }));
    expect(screen.getByRole("dialog").textContent).toContain(
      `Create a new ${kind} Job linked to this one`,
    );
    expect(screen.getByRole("dialog").textContent).toContain(
      "Finished Job records can expire",
    );
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect((await screen.findByRole("alert")).textContent).toContain(
      "outcome is unknown",
    );
    expect(screen.queryByRole("link", { name: "Open retry job" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry same command" }));
    expect(
      (
        await screen.findByRole("link", { name: "Open retry job" })
      ).getAttribute("href"),
    ).toBe("/unit/jobs/retry-1");
    expect(screen.getByRole("status").textContent).toContain(
      "Retry accepted. New job state: Queued.",
    );
    expect(post).toHaveBeenCalledTimes(2);
    expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
    expect(post.mock.calls[0]).toEqual([
      "/api/v1/jobs/{jobId}:retry",
      {
        params: {
          path: { jobId: job.id },
          header: { "Idempotency-Key": expect.any(String) as unknown },
        },
        body: { reason: "Dependency restored" },
      },
    ]);
    expect(get).toHaveBeenCalledTimes(1);
    get.mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
    fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
    await screen.findByRole("alert");
    expect(
      screen.getByRole("link", { name: "Open retry job" }).getAttribute("href"),
    ).toBe("/unit/jobs/retry-1");
    expect(screen.getByText(/not been reread/)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain(
      "Retry accepted. New job state: Queued.",
    );
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() =>
      expect(screen.queryByText(/not been reread/)).toBeNull(),
    );
    expect(
      screen.getByRole("link", { name: "Open retry job" }).getAttribute("href"),
    ).toBe("/unit/jobs/retry-1");
    expect(screen.getByRole("textbox", { name: "Reason" })).toHaveProperty(
      "value",
      "",
    );
    expect(screen.getByRole("button", { name: "Retry job" })).toBeTruthy();
    expect(post).toHaveBeenCalledTimes(2);
    expect(get).toHaveBeenCalledTimes(3);
    for (const call of get.mock.calls)
      expect(call).toEqual([
        "/api/v1/jobs/{jobId}",
        expect.objectContaining({ params: { path: { jobId: job.id } } }),
      ]);
  },
);

it("preserves a dismissed reason and asks before discarding it on reload", async () => {
  const { props, get } = jobWorkspace();
  get
    .mockResolvedValueOnce(reply(job))
    .mockResolvedValue(reply({ ...job, updatedAt: "2026-09-01T12:01:00Z" }));
  const post = vi.fn();
  Object.assign(props.client.api, { POST: post });
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValueOnce(true);
  render(
    <Jobs
      {...props}
      id={job.id}
      access={{ ...props.access, ...testAccess(["reader", "source_admin"]) }}
    />,
  );
  const reason = await screen.findByRole("textbox", { name: "Reason" });
  fireEvent.change(reason, { target: { value: "Keep this draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Cancel job" }));
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
  expect(get).toHaveBeenCalledTimes(1);
  expect((reason as HTMLTextAreaElement).value).toBe("Keep this draft");
  fireEvent.click(screen.getByRole("button", { name: "Reload job" }));
  await waitFor(() =>
    expect(
      (screen.getByRole("textbox", { name: "Reason" }) as HTMLTextAreaElement)
        .value,
    ).toBe(""),
  );
  expect(confirm).toHaveBeenCalledTimes(2);
  expect(get).toHaveBeenCalledTimes(2);
});

it("keeps a rejected job command editable and reports permission loss to the shell", async () => {
  const { props, get } = jobWorkspace();
  get.mockResolvedValue(reply(job));
  const post = vi.fn().mockResolvedValue({
    error: { title: "Forbidden", status: 403 },
    response: new Response(null, { status: 403 }),
  });
  Object.assign(props.client.api, { POST: post });
  render(
    <Jobs
      {...props}
      id={job.id}
      access={{ ...props.access, ...testAccess(["reader", "source_admin"]) }}
    />,
  );
  const reason = await screen.findByRole("textbox", { name: "Reason" });
  fireEvent.change(reason, { target: { value: "No longer needed" } });
  fireEvent.click(screen.getByRole("button", { name: "Cancel job" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Your current access does not permit this action.",
  );
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ status: 403 }),
  );
  expect(screen.queryByRole("link", { name: "Open retry job" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Back to form" }));
  expect(
    (screen.getByRole("textbox", { name: "Reason" }) as HTMLTextAreaElement)
      .value,
  ).toBe("No longer needed");
  expect(post).toHaveBeenCalledTimes(1);
  expect(get).toHaveBeenCalledTimes(1);
});

it.each(["queued", "failed"] as const)(
  "validates Unicode reason limits before preparing a %s job action",
  async (state) => {
    const { props, get } = jobWorkspace();
    get.mockResolvedValue(reply({ ...job, state }));
    render(
      <Jobs
        {...props}
        id={job.id}
        access={{ ...props.access, ...testAccess(["reader", "source_admin"]) }}
      />,
    );
    const action = await screen.findByRole("button", {
      name: state === "queued" ? "Cancel job" : "Retry job",
    });
    const reason = screen.getByRole("textbox", { name: "Reason" });
    fireEvent.change(reason, { target: { value: "🙂".repeat(2000) + "a" } });
    expect(action).toHaveProperty("disabled", true);
    fireEvent.click(action);
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.change(reason, { target: { value: "🙂".repeat(2000) } });
    expect(action).toHaveProperty("disabled", false);
    fireEvent.click(action);
    expect(await screen.findByRole("dialog")).toBeTruthy();
  },
);

it.each(["cancel", "retry"] as const)(
  "blocks a prepared %s before its first send after authority changes and retains the draft",
  async (action) => {
    const w = jobWorkspace();
    const access = {
      ...w.props.access,
      ...testAccess(["reader", "source_admin"]),
    };
    w.get.mockResolvedValue(
      reply({ ...job, state: action === "retry" ? "failed" : "queued" }),
    );
    const post = vi.fn();
    Object.assign(w.props.client.api, { POST: post });
    const ui = render(<Jobs {...w.props} id={job.id} access={access} />);
    const reason = await screen.findByLabelText("Reason");
    fireEvent.change(reason, {
      target: { value: "Preserve this exact job reason" },
    });
    fireEvent.click(
      screen.getByRole("button", {
        name: action === "retry" ? "Retry job" : "Cancel job",
      }),
    );
    const next = {
      ...access,
      permissions: access.permissions.filter(
        (value) => value !== `job.${action}`,
      ),
    };
    ui.rerender(<Jobs {...w.props} id={job.id} access={next} />);
    const confirm = screen.getByRole("button", { name: "Confirm" });
    expect(confirm).toHaveProperty("disabled", true);
    expect(screen.getByRole("dialog").textContent).toContain(
      "Access or prerequisites changed",
    );
    fireEvent.click(confirm);
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Preserve this exact job reason",
    );
    expect(screen.getByLabelText("Reason").matches(":disabled")).toBe(true);
    ui.rerender(<Jobs {...w.props} id={job.id} access={access} />);
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Preserve this exact job reason",
    );
    expect(screen.getByLabelText("Reason").matches(":disabled")).toBe(false);
    expect(post).not.toHaveBeenCalled();
  },
);

it.each(["cancel", "retry"] as const)(
  "retains exact uncertain %s recovery after losing the write scope",
  async (action) => {
    const w = jobWorkspace();
    const access = {
      ...w.props.access,
      ...testAccess(["reader", "source_admin"]),
      scopeEnforcement: true,
      scopes: ["catalog.read", "catalog.write"] as typeof w.props.access.scopes,
    };
    const current = { ...job, state: action === "retry" ? "failed" : "queued" };
    w.get.mockResolvedValue(reply(current));
    const valid =
      action === "retry"
        ? { ...job, id: "retry-after-access-change", parentJobId: job.id }
        : { ...job, state: "cancelled" };
    const post = vi
      .fn()
      .mockResolvedValueOnce(reply({}))
      .mockResolvedValueOnce(reply(valid));
    Object.assign(w.props.client.api, { POST: post });
    const ui = render(<Jobs {...w.props} id={job.id} access={access} />);
    const reason = await screen.findByLabelText("Reason");
    fireEvent.change(reason, {
      target: { value: "  Recover this exact prepared command  " },
    });
    fireEvent.click(
      screen.getByRole("button", {
        name: action === "retry" ? "Retry job" : "Cancel job",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await screen.findByText(/The outcome is unknown/);
    const next = {
      ...access,
      scopes: ["catalog.read"] as typeof access.scopes,
    };
    ui.rerender(<Jobs {...w.props} id={job.id} access={next} />);
    const retry = screen.getByRole("button", { name: "Retry same command" });
    expect(retry).toHaveProperty("disabled", false);
    fireEvent.click(retry);
    await screen.findByText(
      action === "retry"
        ? "Retry accepted. New job state: Queued."
        : "Cancellation response: Cancelled. Reload to check current state.",
    );
    expect(post).toHaveBeenCalledTimes(2);
    expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/jobs/{jobId}:${action}`,
      {
        params: {
          path: { jobId: job.id },
          header: { "Idempotency-Key": expect.any(String) as string },
        },
        body: { reason: "Recover this exact prepared command" },
      },
    ]);
    expect(post.mock.calls[0]?.[1]).not.toHaveProperty(
      "params.header.If-Match",
    );
  },
);
