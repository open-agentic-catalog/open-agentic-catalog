// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import Federation, { Inspection } from "./Federation";
import { Observations } from "./Observations";
import { SourceSnapshots } from "./SourceSnapshots";
import { CommandDialog } from "./workspace";
import { APIError } from "../api/client";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

it("restores the Source status URL through Apply, Reload and Back/Forward", async () => {
  history.replaceState(null, "", "/unit/sources?status=error");
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(<Federation {...w.props} access={administrator} />);
  await screen.findByText("No matching sources.");
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { status: "error", limit: 25 } },
  });
  change("Source status", "paused");
  click("Apply source filters");
  await screen.findByText(/status paused/);
  expect(location.search).toBe("?status=paused");
  history.replaceState(null, "", "/unit/sources?status=active");
  fireEvent(window, new PopStateEvent("popstate"));
  await screen.findByText(/status active/);
  expect(
    screen.getByRole("combobox", { name: "Source status" }),
  ).toHaveProperty("value", "active");
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { status: "active", limit: 25 } },
  });
  click("Clear source filters");
  await screen.findByText(/status all nonremoved/);
  expect(location.search).toBe("");
  const readCount = w.get.mock.calls.length;
  history.replaceState(null, "", "/unit/jobs");
  fireEvent(window, new PopStateEvent("popstate"));
  expect(w.get).toHaveBeenCalledTimes(readCount);
});

it.each(["?status=unknown", "?status=active&status=paused"])(
  "refuses to widen malformed Source status criteria %s",
  async (query) => {
    history.replaceState(null, "", "/unit/sources" + query);
    const w = screenWorkspace();
    w.get.mockRejectedValue(new Error("Invalid source filter"));
    render(<Federation {...w.props} access={administrator} />);
    await screen.findByRole("alert");
    expect(
      screen.getByRole("option", { name: "Unsupported status" }),
    ).toBeTruthy();
    expect(w.get.mock.calls[0]?.[1]).toMatchObject({
      params: {
        query: {
          limit: 25,
          status: query.includes("&") ? "unsupported" : "unknown",
        },
      },
    });
  },
);

screenLifecycle("/unit/sources");
afterEach(() => vi.useRealTimers());

it.each([false, true])(
  "keeps exact reinspection recovery after inspection authority is lost, uncertain=%s",
  async (uncertain) => {
    const w = screenWorkspace();
    const inspection = {
      id: "inspection-1",
      tenantId: administrator.tenantId,
      resolvedUrl: "https://peer.example/catalog",
      requestedUrl: "https://peer.example/catalog",
      profileId: "ard",
      entryCount: 0,
      expiresAt: "2020-01-01T00:00:00Z",
    };
    w.get.mockResolvedValue(etagReply(inspection));
    w.post.mockRejectedValueOnce(new Error("Reply lost"));
    const view = render(
      <Inspection {...w.props} access={administrator} id={inspection.id} />,
    );
    await screen.findByRole("button", { name: "Reinspect the same interface" });
    click("Reinspect the same interface");
    if (uncertain) {
      click("Confirm");
      await screen.findByRole("button", { name: "Retry same command" });
    }
    view.rerender(
      <Inspection
        {...w.props}
        access={{
          ...administrator,
          permissions: administrator.permissions.filter(
            (permission) => permission !== "source.inspect",
          ),
        }}
        id={inspection.id}
      />,
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain(inspection.resolvedUrl);
    const send = within(dialog).getByRole("button", {
      name: uncertain ? "Retry same command" : "Confirm",
    });
    expect(send).toHaveProperty("disabled", !uncertain);
    if (uncertain) {
      w.post.mockResolvedValueOnce(
        etagReply({
          ...inspection,
          id: "inspection-2",
          expiresAt: "2099-01-01T00:00:00Z",
        }),
      );
      click("Retry same command");
      await screen.findByText(/Fresh inspection retained/);
      expect(w.post).toHaveBeenCalledTimes(2);
      expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    } else {
      fireEvent.click(send);
      expect(w.post).not.toHaveBeenCalled();
    }
  },
);
const source = {
  id: "source-1",
  tenantId: administrator.tenantId,
  revision: 1,
  displayName: "Exact Name",
  status: "active",
  retrievalMode: "indexed",
  updateMode: "manual_review",
  profileId: "ard",
  endpoint: "https://peer.example/catalog",
  scope: { kinds: ["agent"] },
  live: {},
  referrals: {},
  nesting: {},
};
const tested = {
  sourceId: source.id,
  sourceRevision: 1,
  status: "failed",
  httpStatus: 0,
  mediaType: "",
  bodyDigest: "",
  entryCount: 0,
  capabilities: {},
  profileId: "ard",
  policyId: "",
  policyRevision: 0,
  policyDigest: "",
  testedAt: stamp,
};

it.each([{ id: "other-source" }, { tenantId: "other-tenant" }])(
  "refuses current Source metadata for another requested identity before enabling actions: %j",
  async (mismatch) => {
    const w = screenWorkspace();
    w.get
      .mockResolvedValueOnce(etagReply({ ...source, ...mismatch }))
      .mockResolvedValueOnce(etagReply(source));
    render(<Federation {...w.props} access={administrator} id={source.id} />);
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "Test source" })).toBeNull();
    expect(
      screen.queryByRole("heading", { name: source.displayName }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
    click("Reload source");
    await screen.findByRole("button", { name: "Test source" });
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.get.mock.calls[1]?.[1]).toMatchObject({
      params: { path: { sourceId: source.id } },
    });
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("applies exact source filters before paging and preserves unsent text on cursor recovery", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [], nextCursor: "next" }));
  render(<Federation {...w.props} access={administrator} />);
  await screen.findByText("No sources on this page.");
  change("Source name (exact match)", " Exact Name ");
  change("Source status", "paused");
  expect(w.get).toHaveBeenCalledOnce();
  click("Apply source filters");
  await screen.findByText(/name " Exact Name "; status paused/);
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: { displayName: " Exact Name ", status: "paused", limit: 25 },
    },
  });
  click("Next page");
  await screen.findByRole("button", { name: "First page" });
  change("Source name (exact match)", "unsent");
  w.get.mockRejectedValueOnce(new Error("cursor refused"));
  click("Refresh");
  await screen.findByRole("alert");
  click("Reload");
  await screen.findByText("No matching sources.");
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: { displayName: " Exact Name ", status: "paused", limit: 25 },
    },
  });
  expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
    "params.query.cursor",
  );
  expect(screen.getByLabelText("Source name (exact match)")).toHaveProperty(
    "value",
    "unsent",
  );
  const directoryReads = w.get.mock.calls.length;
  change("Exact Source ID", "not-a-source-uuid");
  const exactID = screen.getByLabelText("Exact Source ID") as HTMLInputElement;
  expect(exactID.validity.patternMismatch).toBe(true);
  expect(exactID.closest("form")!.checkValidity()).toBe(false);
  change("Exact Source ID", "00000000-0000-7000-8000-000000000ABC");
  expect(exactID.closest("form")!.checkValidity()).toBe(true);
  expect(w.get).toHaveBeenCalledTimes(directoryReads);
  expect(
    screen
      .getByRole("form", { name: "Open exact Source" })
      .getAttribute("action"),
  ).toBe("/unit/sources/00000000-0000-7000-8000-000000000abc");
  click("Clear source filters");
  await screen.findByText(/name any; status all nonremoved/);
  expect(screen.getByLabelText("Source status")).toHaveProperty("value", "");
  expect(w.post).not.toHaveBeenCalled();
});

it.each([
  ["failed", 0, {}, "No HTTP response", undefined],
  ["failed", 503, { enumerable: false }, "HTTP 503", "Controlled safe refusal"],
  ["succeeded", 200, { enumerable: true }, "HTTP 200", undefined],
] as const)(
  "shows a recorded %s outcome with HTTP %s before complete evidence",
  async (status, httpStatus, capabilities, httpLabel, safeDiagnostic) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(source));
    w.post.mockResolvedValue(
      etagReply({
        ...tested,
        sourceRevision: status === "succeeded" ? 2 : 1,
        status,
        httpStatus,
        capabilities,
        ...(safeDiagnostic ? { safeDiagnostic } : {}),
      }),
    );
    render(<Federation {...w.props} access={administrator} id={source.id} />);
    await screen.findByLabelText("Source action");
    await confirmCommand("Test source");
    const outcome = screen.getByRole("region", {
      name: "Recorded source test outcome",
    });
    expect(outcome.textContent).toContain(
      `Source test: ${status === "failed" ? "Failed" : "Succeeded"}`,
    );
    expect(outcome.textContent).toContain(httpLabel);
    expect(outcome.textContent).toContain(
      safeDiagnostic || "No diagnostic recorded",
    );
    expect(outcome.textContent).toContain("not synchronization");
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).toHaveBeenCalledOnce();
    change("Source action", "sync");
    expect(
      screen.queryByRole("region", { name: "Recorded source test outcome" }),
    ).toBeNull();
  },
);

it.each([
  { sourceId: "other-source" },
  { sourceRevision: 2 },
  { capabilities: { enumerable: "yes" } },
  { status: "unknown" },
  { httpStatus: 600 },
  { entryCount: -1 },
  { testedAt: "invalid" },
])(
  "keeps malformed source-test success replies in exact-command recovery: %j",
  async (malformed) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(source));
    w.post
      .mockResolvedValueOnce(etagReply({ ...tested, ...malformed }))
      .mockResolvedValueOnce(etagReply(tested));
    render(<Federation {...w.props} access={administrator} id={source.id} />);
    await screen.findByLabelText("Source action");
    await confirmCommand("Test source");
    expect(screen.getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      screen.queryByRole("region", { name: "Recorded source test outcome" }),
    ).toBeNull();
    click("Retry same command");
    await screen.findByRole("heading", { name: "Source test: Failed" });
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
  },
);

it.each([false, true])(
  "updates idle inspection expiry with an open dialog=%s and sends no new request",
  async (open) => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
    const w = screenWorkspace();
    w.get.mockResolvedValue(
      etagReply({
        id: "inspection-1",
        resolvedUrl: source.endpoint,
        profileId: "ard",
        entryCount: 0,
        expiresAt: "2026-09-01T12:00:02Z",
      }),
    );
    render(
      <Inspection {...w.props} access={administrator} id="inspection-1" />,
    );
    await screen.findByLabelText("Display name");
    change("Display name", "Preserve my choices");
    change("Reason", "Reviewed source");
    if (open) click("Connect inspected source");
    vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-09-01T12:00:03Z"));
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(
      screen.getByRole("button", { name: "Connect inspected source" }),
    ).toHaveProperty("disabled", true);
    if (open) {
      expect(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: "Confirm",
        }),
      ).toHaveProperty("disabled", true);
      click("Cancel");
    }
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Preserve my choices",
    );
    expect(
      screen.getByRole("button", {
        name: "Reinspect the same interface",
      }),
    ).toHaveProperty("disabled", false);
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  { tenantId: "other-tenant" },
  { requestedUrl: "https://other.example/catalog" },
  { networkProfile: "other-network" },
  { credentialBinding: "other-binding" },
])(
  "preserves the connection draft through reinspection and refuses a mismatched reply: %j",
  async (mismatch) => {
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
    const w = screenWorkspace();
    const inspection = {
      id: "inspection-1",
      tenantId: administrator.tenantId,
      requestedUrl: source.endpoint,
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 0,
      networkProfile: "selected-network",
      credentialBinding: "private-binding-reference",
      expiresAt: stamp,
    };
    const fresh = {
      ...inspection,
      id: "inspection-2",
      expiresAt: "2026-09-02T12:00:00Z",
    };
    w.get.mockResolvedValue(etagReply(inspection));
    w.post
      .mockResolvedValueOnce(etagReply({ ...fresh, ...mismatch }))
      .mockResolvedValueOnce(etagReply(fresh))
      .mockResolvedValueOnce(etagReply(source));
    render(
      <Inspection {...w.props} access={administrator} id={inspection.id} />,
    );
    await screen.findByLabelText("Display name");
    change("Display name", "Preserve my choices");
    change("Synchronization", "automatic");
    change("Reason", "Retain this reason");
    await confirmCommand("Reinspect the same interface");
    expect(screen.getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      screen.getByRole("button", {
        name: "Connect inspected source",
      }),
    ).toHaveProperty("disabled", true);
    expect(screen.queryByText("private-binding-reference")).toBeNull();
    click("Retry same command");
    await screen.findByText(/Fresh inspection retained/);
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(screen.getByLabelText("Display name")).toHaveProperty(
      "value",
      "Preserve my choices",
    );
    expect(screen.getByLabelText("Synchronization")).toHaveProperty(
      "value",
      "automatic",
    );
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "Retain this reason",
    );
    await confirmCommand("Connect inspected source");
    expect(w.post.mock.calls[2]?.[1]).toMatchObject({
      body: {
        inspectionId: fresh.id,
        displayName: "Preserve my choices",
        updateMode: "automatic",
        networkProfile: "selected-network",
        credentialBinding: "private-binding-reference",
        reason: "Retain this reason",
      },
    });
    expect(w.get).toHaveBeenCalledOnce();
  },
);

it("reinspects with default bindings only after confirmation and retains a failed attempt's draft", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const w = screenWorkspace();
  const inspection = {
    id: "inspection-1",
    tenantId: administrator.tenantId,
    requestedUrl: source.endpoint,
    resolvedUrl: source.endpoint,
    profileId: "ard",
    entryCount: 0,
    expiresAt: stamp,
  };
  w.get.mockResolvedValue(etagReply(inspection));
  w.post
    .mockRejectedValueOnce(new Error("unknown transport outcome"))
    .mockResolvedValueOnce(
      etagReply({
        ...inspection,
        id: "inspection-2",
        expiresAt: "2026-09-01T12:00:02Z",
      }),
    );
  render(<Inspection {...w.props} access={administrator} id={inspection.id} />);
  await screen.findByLabelText("Display name");
  change("Display name", "Retain this choice");
  click("Reinspect the same interface");
  expect(w.post).not.toHaveBeenCalled();
  click("Cancel");
  await confirmCommand("Reinspect the same interface");
  expect(screen.getByRole("alert").textContent).toContain("outcome is unknown");
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Retain this choice",
  );
  click("Retry same command");
  await screen.findByText(/Fresh inspection retained/);
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    body: { url: source.endpoint },
  });
  expect(w.post.mock.calls[0]?.[1]).not.toHaveProperty("body.networkProfile");
  expect(w.post.mock.calls[0]?.[1]).not.toHaveProperty(
    "body.credentialBinding",
  );
  expect(
    screen.getByRole("button", {
      name: "Connect inspected source",
    }),
  ).toHaveProperty("disabled", false);
  vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-09-01T12:00:03Z"));
  await act(async () => {
    vi.advanceTimersByTime(1000);
  });
  click("Prepare another inspection");
  click("Reinspect the same interface");
  expect(
    within(screen.getByRole("dialog")).getByText(source.endpoint),
  ).toBeDefined();
  click("Cancel");
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Retain this choice",
  );
});

it("allows expired exact-key recovery only after an uncertain send", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const run = vi
    .fn()
    .mockRejectedValueOnce(new Error("lost reply"))
    .mockResolvedValueOnce("Recovered");
  const onDone = vi.fn();
  render(
    <CommandDialog
      command={{
        title: "Bound action",
        description: "One exact action",
        expiresAt: "2026-09-01T12:00:02Z",
        run,
      }}
      onClose={vi.fn()}
      onDone={onDone}
      onFailure={vi.fn()}
    />,
  );
  await act(async () => {
    click("Confirm");
  });
  vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-09-01T12:00:03Z"));
  await act(async () => {
    vi.advanceTimersByTime(1000);
  });
  expect(
    screen.getByRole("button", { name: "Retry same command" }),
  ).toHaveProperty("disabled", false);
  await act(async () => {
    click("Retry same command");
  });
  expect(run.mock.calls[1]).toEqual(run.mock.calls[0]);
  expect(onDone).toHaveBeenCalledWith("Recovered");
});

it("rechecks inspection expiry when preparing before the presentation timer fires", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const w = screenWorkspace();
  w.get.mockResolvedValue(
    etagReply({
      id: "inspection-1",
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 0,
      expiresAt: "2026-09-01T12:00:02Z",
    }),
  );
  render(<Inspection {...w.props} access={administrator} id="inspection-1" />);
  await screen.findByLabelText("Display name");
  change("Display name", "Keep my settings");
  change("Reason", "Reviewed source");
  vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-09-01T12:00:03Z"));
  click("Connect inspected source");
  expect(screen.getByRole("alert").textContent).toContain("Inspection expired");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("rechecks expiry at confirmation before a new send even before a timer fires", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  const run = vi.fn().mockResolvedValue("Must not send");
  render(
    <CommandDialog
      command={{
        title: "Bound action",
        description: "Captured evidence",
        expiresAt: "2026-09-01T12:00:02Z",
        run,
      }}
      onClose={vi.fn()}
      onDone={vi.fn()}
      onFailure={vi.fn()}
    />,
  );
  vi.spyOn(Date, "now").mockReturnValue(Date.parse("2026-09-01T12:00:03Z"));
  await act(async () => {
    click("Confirm");
  });
  expect(run).not.toHaveBeenCalled();
});

it("keeps definite source-test admission refusal separate from a recorded failed retrieval", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(source));
  w.post.mockRejectedValue(new APIError(412, "STALE_REVISION"));
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByLabelText("Source action");
  await confirmCommand("Test source");
  expect(screen.getByRole("alert").textContent).not.toContain(
    "outcome is unknown",
  );
  expect(
    screen.queryByRole("region", { name: "Recorded source test outcome" }),
  ).toBeNull();
  click("Back to form");
  expect(w.post).toHaveBeenCalledOnce();
});

it("stages observation filters, clears unavailable parents and opens exact retained references independently", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(<Observations {...w.props} access={administrator} />);
  await screen.findByText("No matching observations.");
  change("Source ID (optional)", "00000000-0000-7000-8000-000000000099");
  change("Show entries", "all");
  expect(w.get).toHaveBeenCalledOnce();
  click("Apply observation filters");
  await screen.findByRole("region", { name: "Applied source restriction" });
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: {
        sourceId: "00000000-0000-7000-8000-000000000099",
        freshness: "all",
      },
    },
  });
  change("Exact Observation ID", "00000000-0000-7000-8000-000000000011");
  change("Exact Snapshot ID", "00000000-0000-7000-8000-000000000012");
  expect(
    screen
      .getByRole("form", { name: "Open exact Observation" })
      .getAttribute("action"),
  ).toBe("/unit/observations/00000000-0000-7000-8000-000000000011");
  click("Clear observation filters");
  await screen.findByText(
    /Applied observation filters: Source all sources; view Current saved view/,
  );
  click("Apply observation filters");
  expect(location.search).toBe("");
  expect(w.post).not.toHaveBeenCalled();
});

it("opens a selected snapshot by exact reference without a parent read", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(<SourceSnapshots {...w.props} access={administrator} id="source-1" />);
  await screen.findByText("No snapshots.");
  change("Exact Snapshot ID", "00000000-0000-7000-8000-000000000012");
  expect(
    screen
      .getByRole("form", { name: "Open exact Snapshot" })
      .getAttribute("action"),
  ).toBe("/unit/source-snapshots/00000000-0000-7000-8000-000000000012");
  expect(w.get).toHaveBeenCalledOnce();
});
