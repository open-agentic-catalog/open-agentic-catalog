// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Settings, { isSettings } from "./Settings";
import { APIError } from "../api/client";
import { deferred, installDialog, workspace } from "../../tests/unit/fixtures";

const defaults = {
  automaticSourceSyncEnabled: true,
  automaticSourceSyncInterval: "15m0s",
  newJobMaxAttempts: 5,
};
const initial = {
  tenantId: "tenant-a",
  revision: 0,
  overrides: {},
  deploymentDefaults: defaults,
  effective: defaults,
};
const tag = '"settings-' + "a".repeat(64) + '"';
function response(value: unknown, etag = tag) {
  return {
    data: value,
    response: new Response(null, { headers: { ETag: etag } }),
  };
}
function fixture(manage = true) {
  const f = workspace();
  f.props.access = {
    ...f.props.access,
    scopeEnforcement: false,
    scopes: [],
    permissions: [
      "settings.read",
      ...(manage ? ["settings.manage" as const] : []),
    ],
  };
  f.get.mockResolvedValue(response(initial));
  const post = vi.fn(async (..._args: unknown[]) => response(initial));
  Object.assign(f.props.client.api, { POST: post });
  Object.assign(f.props.client.api, {
    GET: (...args: unknown[]) =>
      args[0] === "/api/v1/capabilities"
        ? Promise.resolve({
            data: {
              resourceKinds: [],
              profiles: {},
              operations: [],
              governance: {},
              search: {
                profile: "core",
                semantic: "disabled",
                degraded: false,
              },
              optionalCapabilities: { webhooks: "disabled" },
              limits: {},
              profileBundle: { digest: "fixture", artifacts: 0 },
              degradations: [],
            },
            response: new Response(),
          })
        : f.get(...args),
  });
  return { ...f, post };
}
function setSetting(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(`${label} source`), {
    target: { value: value === "" ? "inherit" : "override" },
  });
  if (value !== "")
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
}
let restore: () => void;
beforeEach(() => {
  restore = installDialog();
});
afterEach(() => {
  cleanup();
  restore();
  vi.restoreAllMocks();
});
it.each(["missing", "namespace", "scope"])(
  "refuses settings reads with %s authority",
  (mode) => {
    const f = fixture();
    f.props.access.permissions = [];
    if (mode === "namespace")
      f.props.access.availablePermissions = [
        "settings.read",
        "settings.manage",
      ];
    if (mode === "scope") {
      f.props.access.permissions = ["settings.read"];
      f.props.access.scopeEnforcement = true;
    }
    render(<Settings {...f.props} />);
    expect(screen.getByRole("alert").textContent).toContain("settings.read");
    expect(f.get).not.toHaveBeenCalled();
  },
);
it("shows effective and inherited values for a read-only custom delegate", async () => {
  const f = fixture(false);
  render(<Settings {...f.props} />);
  await screen.findByRole("heading", { name: "Current policy" });
  expect(screen.getByText("Revision 0 · No saved overrides")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save settings" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Audit history" })).toBeNull();
  const values = screen.getByRole("group", { name: "Tenant settings values" });
  expect(values).toHaveProperty("tabIndex", 0);
  values.focus();
  expect(document.activeElement).toBe(values);
  expect(within(values).getByRole("table")).toBeTruthy();
  const rows = screen.getAllByRole("row").slice(1);
  expect(rows.map((row) => row.textContent)).toEqual([
    "Automatic source syncInherit deployment defaultEnabledEnabled",
    "Automatic sync intervalInherit deployment default15m0s15m0s",
    "New Job maximum attemptsInherit deployment default55",
  ]);
  expect(screen.getByText(/worker defaults can differ/)).toBeTruthy();
  expect(
    screen.getByText("Exact settings values").closest("details"),
  ).toHaveProperty("open", false);
});
it("preserves false and exact duration precision, captures requests and resets all overrides", async () => {
  const f = fixture();
  const overrides = {
    automaticSourceSyncEnabled: false,
    automaticSourceSyncInterval: "1m30.123456789s",
    newJobMaxAttempts: 2,
  };
  const savedOverrides = { ...overrides, newJobMaxAttempts: 3 };
  const saved = {
    ...initial,
    revision: 2,
    overrides: savedOverrides,
    effective: { ...defaults, ...savedOverrides },
  };
  const savedTag = '"settings-' + "b".repeat(64) + '"';
  const resetSaved = { ...initial, revision: 3 };
  const resetTag = '"settings-' + "c".repeat(64) + '"';
  const refresh = deferred<ReturnType<typeof response>>();
  f.get
    .mockResolvedValue(response(resetSaved, resetTag))
    .mockResolvedValueOnce(
      response({
        ...initial,
        revision: 1,
        overrides,
        effective: { ...defaults, ...overrides },
      }),
    )
    .mockReturnValueOnce(refresh.promise);
  f.post
    .mockResolvedValue(response(resetSaved, resetTag))
    .mockResolvedValueOnce(response(saved, savedTag));
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Automatic sync interval");
  expect(
    (screen.getByLabelText("Automatic source sync") as HTMLSelectElement).value,
  ).toBe("paused");
  setSetting("New Job maximum attempts", "3");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Change attempts" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(f.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText("Tenant settings saved.");
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      overrides: { ...overrides, newJobMaxAttempts: 3 },
      reason: "Change attempts",
    },
    params: { header: { "If-Match": tag } },
  });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Reset all" },
  });
  const reset = screen.getByRole("button", {
    name: "Use deployment defaults for all settings",
  });
  await waitFor(() => expect(f.get).toHaveBeenCalledTimes(2));
  expect(reset).toHaveProperty("disabled", true);
  fireEvent.click(reset);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(f.post).toHaveBeenCalledTimes(1);
  await act(async () => refresh.resolve(response(saved, savedTag)));
  await waitFor(() => expect(reset).toHaveProperty("disabled", false));
  fireEvent.click(reset);
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await waitFor(() => expect(f.post).toHaveBeenCalledTimes(2));
  expect(f.post.mock.calls[1]?.[1]).toMatchObject({
    body: { overrides: {}, reason: "Reset all" },
    params: { header: { "If-Match": savedTag } },
  });
  await waitFor(() =>
    expect(screen.getByLabelText("Automatic source sync")).toHaveProperty(
      "value",
      "inherit",
    ),
  );
  expect(screen.getByLabelText("Automatic sync interval")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "",
  );
});
it("returns one field to inheritance without changing other overrides", async () => {
  const f = fixture();
  const overrides = {
    automaticSourceSyncEnabled: false,
    automaticSourceSyncInterval: "15m0.123s",
    newJobMaxAttempts: 2,
  };
  f.get.mockResolvedValue(response({ ...initial, overrides }));
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Automatic sync interval");
  setSetting("Automatic sync interval", "");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Inherit interval" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await waitFor(() => expect(f.post).toHaveBeenCalledTimes(1));
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      overrides: { automaticSourceSyncEnabled: false, newJobMaxAttempts: 2 },
    },
  });
});
it("retains a draft after a stale save and compares explicitly reloaded policy", async () => {
  const f = fixture();
  f.post.mockRejectedValue(new APIError(412, "PRECONDITION_FAILED", "request"));
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Automatic sync interval");
  setSetting("Automatic sync interval", "2h");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Change interval" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByRole("button", { name: "Back to form" });
  fireEvent.click(screen.getByRole("button", { name: "Back to form" }));
  f.get.mockResolvedValue(
    response({ ...initial, revision: 2 }, '"settings-' + "b".repeat(64) + '"'),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  await screen.findByText(/The current policy differs/);
  expect(
    (screen.getByLabelText("Automatic sync interval") as HTMLInputElement)
      .value,
  ).toBe("2h");
  expect(f.post).toHaveBeenCalledTimes(1);
  vi.spyOn(window, "confirm").mockReturnValue(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Use current saved values" }),
  );
  expect(
    (screen.getByLabelText("Automatic sync interval") as HTMLInputElement)
      .value,
  ).toBe("");
});
it("recovers malformed successful replies with exactly the same command", async () => {
  const f = fixture();
  f.post
    .mockResolvedValueOnce(response({ ...initial, effective: null }))
    .mockResolvedValueOnce(response({ ...initial, revision: 1 }));
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Reason");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Recover saved reply" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByRole("button", { name: "Retry same command" });
  expect(f.get).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Retry same command" }));
  await screen.findByText("Tenant settings saved.");
  expect(f.post.mock.calls[1]).toEqual(f.post.mock.calls[0]);
});
it.each([
  null,
  { ...initial, overrides: { unknownSetting: true } },
  { ...initial, overrides: { newJobMaxAttempts: 21 } },
  { ...initial, overrides: { automaticSourceSyncEnabled: null } },
  { ...initial, overrides: { automaticSourceSyncInterval: 60 } },
  { ...initial, deploymentDefaults: { ...defaults, newJobMaxAttempts: 0 } },
  { ...initial, effective: { ...defaults, automaticSourceSyncEnabled: null } },
  { ...initial, revision: -1 },
  { ...initial, effective: {} },
  { ...initial, updatedAt: "bad" },
])("rejects malformed consumed policy", (value) => {
  expect(isSettings(value)).toBe(false);
});
it("clears private drafts and fences an old read after identity changes", async () => {
  const f = fixture();
  const view = render(<Settings {...f.props} />);
  await screen.findByLabelText("Automatic sync interval");
  setSetting("Automatic sync interval", "2h");
  f.get.mockResolvedValue(response({ ...initial, tenantId: "tenant-b" }));
  view.rerender(
    <Settings
      {...f.props}
      access={{ ...f.props.access, tenantId: "tenant-b" }}
    />,
  );
  await waitFor(() =>
    expect(
      (screen.getByLabelText("Automatic sync interval") as HTMLInputElement)
        .value,
    ).toBe(""),
  );
});

it("edits the automatic setting, restores previous overrides as a draft and keeps related authorities separate", async () => {
  const f = fixture();
  const saved = {
    ...initial,
    revision: 1,
    overrides: { automaticSourceSyncEnabled: true },
    effective: defaults,
    updatedAt: "2026-10-14T09:30:00Z",
  };
  f.post.mockResolvedValue(response(saved));
  f.props.access.permissions.push(
    "audit.read",
    "source.read",
    "access.manage",
    "publication.read",
    "webhook.manage",
  );
  f.props.runtime.documentationUrl = "/docs/";
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Automatic source sync");
  expect(
    screen.getByRole("link", { name: "Audit history" }).getAttribute("href"),
  ).toBe("/unit/audit");
  expect(
    screen
      .getByRole("link", { name: "Configuration documentation" })
      .getAttribute("href"),
  ).toBe("/docs/reference/configuration/");
  fireEvent.change(screen.getByLabelText("Automatic source sync"), {
    target: { value: "enabled" },
  });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Enable source scheduling" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText("Tenant settings saved.");
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: { overrides: { automaticSourceSyncEnabled: true } },
  });
  expect(screen.getByLabelText("Automatic source sync")).toHaveProperty(
    "value",
    "enabled",
  );
  // The acknowledgement updates the draft, while this panel remains the last explicit read.
  f.get.mockResolvedValue(response(saved));
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  await screen.findByText(
    `Revision 1 · Saved ${new Date(saved.updatedAt).toLocaleString()}`,
  );
  const unchanged = vi.spyOn(window, "confirm");
  fireEvent.click(
    screen.getByRole("button", { name: "Restore previous overrides to draft" }),
  );
  expect(unchanged).not.toHaveBeenCalled();
  expect(f.post).toHaveBeenCalledTimes(1);
  setSetting("Automatic sync interval", "2h");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Restore previous overrides to draft" }),
  );
  expect(screen.getByLabelText("Automatic sync interval")).toHaveProperty(
    "value",
    "2h",
  );
  expect(f.post).toHaveBeenCalledTimes(1);
  discard.mockReturnValue(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Restore previous overrides to draft" }),
  );
  expect(
    (screen.getByLabelText("Automatic sync interval") as HTMLInputElement)
      .value,
  ).toBe("");
  expect(f.post).toHaveBeenCalledTimes(1);
  setSetting("Automatic sync interval", "2h");
  vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Use current saved values" }),
  );
  expect(
    (screen.getByLabelText("Automatic sync interval") as HTMLInputElement)
      .value,
  ).toBe("2h");
});
it("adopts explicitly reloaded settings without a discard prompt when the draft is unchanged", async () => {
  const f = fixture();
  const current = {
    ...initial,
    revision: 2,
    overrides: { newJobMaxAttempts: 8 },
    effective: { ...defaults, newJobMaxAttempts: 8 },
  };
  f.get
    .mockResolvedValueOnce(response(initial))
    .mockResolvedValueOnce(
      response(current, '"settings-' + "b".repeat(64) + '"'),
    );
  const discard = vi.spyOn(window, "confirm");
  render(<Settings {...f.props} />);
  await screen.findByLabelText("New Job maximum attempts");
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  await screen.findByText(
    /current policy differs from your draft's base revision/,
  );
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "",
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Use current saved values" }),
  );
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "8",
  );
  expect(
    screen.queryByText(
      /current policy differs from your draft's base revision/,
    ),
  ).toBeNull();
  expect(discard).not.toHaveBeenCalled();
  expect(f.get).toHaveBeenCalledTimes(2);
  expect(f.post).not.toHaveBeenCalled();
});
it.each(["missing tag", "foreign tenant"])(
  "refuses an incomplete read: %s",
  async (mode) => {
    const f = fixture();
    f.get.mockResolvedValue(
      response(
        {
          ...initial,
          tenantId: mode === "foreign tenant" ? "another" : initial.tenantId,
        },
        mode === "missing tag" ? "" : tag,
      ),
    );
    render(<Settings {...f.props} />);
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Reason")).toBeNull();
    expect(f.post).not.toHaveBeenCalled();
  },
);
it("blocks an invalid attempt value even when a form is submitted directly", async () => {
  const f = fixture();
  render(<Settings {...f.props} />);
  await screen.findByLabelText("New Job maximum attempts");
  setSetting("New Job maximum attempts", "21");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Check attempts" },
  });
  const form = screen
    .getByRole("button", { name: "Save settings" })
    .closest("form");
  expect(form).not.toBeNull();
  fireEvent.submit(form!);
  expect(screen.getByRole("alert").textContent).toContain("1 to 20");
  expect(f.post).not.toHaveBeenCalled();
});

it("labels paused and inherited settings separately while retaining exact duration evidence", async () => {
  const f = fixture(false);
  f.get.mockResolvedValue(
    response({
      ...initial,
      overrides: {
        automaticSourceSyncEnabled: false,
        automaticSourceSyncInterval: "1m30.123456789s",
      },
      effective: {
        ...defaults,
        automaticSourceSyncEnabled: false,
        automaticSourceSyncInterval: "1m30.123456789s",
      },
    }),
  );
  render(<Settings {...f.props} />);
  await screen.findByRole("heading", { name: "Current policy" });
  const syncRow = screen
    .getByRole("rowheader", { name: "Automatic source sync" })
    .closest("tr")!;
  expect(
    within(syncRow)
      .getAllByRole("cell")
      .map((cell) => cell.textContent),
  ).toEqual(["Paused", "Enabled", "Paused"]);
  const intervalRow = screen
    .getByRole("rowheader", { name: "Automatic sync interval" })
    .closest("tr")!;
  expect(
    within(intervalRow)
      .getAllByRole("cell")
      .map((cell) => cell.textContent),
  ).toEqual(["1m30.123456789s", "15m0s", "1m30.123456789s"]);
  const evidence = screen
    .getByText("Exact settings values")
    .closest("details")!;
  fireEvent.click(screen.getByText("Exact settings values"));
  expect(evidence.textContent).toContain('"automaticSourceSyncEnabled": false');
  expect(evidence.textContent).toContain(
    '"automaticSourceSyncInterval": "1m30.123456789s"',
  );
  expect(f.post).not.toHaveBeenCalled();
});

it.each(["", "/nested"])(
  "keeps a safe custom documentation destination exact under UI base %s",
  async (basePath) => {
    const f = fixture(false);
    const href = "https://manual.example.test/custom%20manual/";
    f.props.runtime = { ...f.props.runtime, basePath, documentationUrl: href };
    render(<Settings {...f.props} />);
    await screen.findByRole("heading", { name: "Current policy" });
    const link = screen.getByRole("link", {
      name: "Documentation",
    });
    expect(link.getAttribute("href")).toBe(href);
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(
      document.getElementById(link.getAttribute("aria-describedby")!)
        ?.textContent,
    ).toBe("Opens in a new tab");
    expect(
      screen.queryByRole("link", { name: "Configuration documentation" }),
    ).toBeNull();
    expect(
      screen.getByRole("link", { name: "API reference" }).getAttribute("href"),
    ).toBe(`${basePath}/api-reference`);
    expect(f.post).not.toHaveBeenCalled();
  },
);
