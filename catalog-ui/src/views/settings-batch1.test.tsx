// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  act,
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from "@testing-library/react";
import Settings from "./Settings";
import { OperationProvider } from "./operation-context";
import { deferred, installDialog, workspace } from "../../tests/unit/fixtures";
import { testAccess } from "../../tests/unit/access-fixtures";

const defaults = {
  automaticSourceSyncEnabled: true,
  automaticSourceSyncInterval: "15m0s",
  newJobMaxAttempts: 5,
};
const policy = {
  tenantId: "tenant-a",
  revision: 0,
  overrides: {},
  deploymentDefaults: defaults,
  effective: defaults,
};
const tag = '"settings-' + "a".repeat(64) + '"';
const newerTag = '"settings-' + "b".repeat(64) + '"';
const capabilities = {
  resourceKinds: [],
  profiles: {},
  operations: [],
  governance: {},
  search: { profile: "core", semantic: "disabled", degraded: false },
  optionalCapabilities: { webhooks: "disabled" },
  limits: {},
  profileBundle: { digest: "fixture", artifacts: 0 },
  degradations: [],
};
const reply = (data: unknown, etag = tag) => ({
  data,
  response: new Response(null, { headers: { ETag: etag } }),
});
function fixture() {
  const f = workspace();
  f.props.access = {
    ...f.props.access,
    permissions: ["settings.read", "settings.manage"],
    namespaceGrants: [],
  };
  const settings = vi.fn(async () => reply(policy));
  const caps = vi.fn(async () => reply(capabilities));
  const post = vi.fn(async (..._args: unknown[]) => reply(policy));
  f.get.mockImplementation(async (...args) =>
    args[0] === "/api/v1/capabilities" ? caps() : settings(),
  );
  Object.assign(f.props.client.api, { POST: post });
  return { ...f, settings, caps, post };
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

it("groups every authorized administrative task without directory hydration and labels reported capability states", async () => {
  const f = fixture();
  f.props.access = { ...f.props.access, ...testAccess(["tenant_admin"]) };
  f.props.runtime.documentationUrl = "/docs/";
  render(<Settings {...f.props} />);
  await screen.findByRole("heading", { name: "Current policy" });
  const controls = screen.getByLabelText("Related controls");
  for (const name of [
    "Members",
    "Groups",
    "Roles",
    "Access assignments",
    "Namespaces",
    "Publishers",
    "Feedback inbox",
    "Rating moderation",
    "Federation sources",
    "ANS assessments",
    "Discovery publication",
    "Webhooks",
    "Jobs",
    "Search indexing",
    "Audit history",
  ])
    expect(within(controls).getByRole("link", { name })).toBeTruthy();
  await screen.findByText(/Deployment capability: disabled/);
  expect(controls.textContent).toContain("Semantic indexing: disabled");
  expect(f.get.mock.calls.map((call) => call[0]).sort()).toEqual([
    "/api/v1/capabilities",
    "/api/v1/tenant-settings",
  ]);
});

it("uses destination predicates for read-only webhook, sharing and export-only Audit delegates", async () => {
  const f = fixture();
  f.props.access.permissions = [
    "settings.read",
    "webhook.read",
    "ratings.sharing.manage",
    "audit.read",
    "audit.export",
  ];
  f.props.access.scopeEnforcement = true;
  f.props.access.scopes = ["tenant.admin", "webhooks.manage", "audit.export"];
  render(<Settings {...f.props} />);
  await screen.findByRole("heading", { name: "Current policy" });
  expect(
    screen.getByRole("link", { name: "Webhooks" }).getAttribute("href"),
  ).toBe("/unit/webhooks");
  expect(
    screen
      .getByRole("link", { name: "Discovery publication" })
      .getAttribute("href"),
  ).toBe("/unit/publication");
  expect(
    screen.getByRole("link", { name: "Audit history" }).closest("li")
      ?.textContent,
  ).toContain("Export only");
  expect(screen.queryByRole("button", { name: "Save settings" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Members" })).toBeNull();
  expect(f.get.mock.calls.map((call) => call[0]).sort()).toEqual([
    "/api/v1/capabilities",
    "/api/v1/tenant-settings",
  ]);
});

it.each([
  [
    "Publishers",
    "publisher.read",
    "publisher.approve_domain",
    "publishers.read",
    "publishers.manage",
  ],
  [
    "Discovery publication",
    "publication.read",
    "publication.generate",
    "catalog.read",
    "catalog.publish",
  ],
  [
    "Discovery publication",
    "publication.read",
    "publication.activate",
    "catalog.read",
    "catalog.publish",
  ],
  [
    "Discovery publication",
    "publication.read",
    "publication.rollback",
    "catalog.read",
    "catalog.publish",
  ],
  [
    "Discovery publication",
    "publication.read",
    "publication.unpublish",
    "catalog.read",
    "catalog.publish",
  ],
  [
    "Federation sources",
    "source.read",
    "source.inspect",
    "sources.read",
    "sources.manage",
  ],
  [
    "Federation sources",
    "source.read",
    "source.sync",
    "sources.read",
    "sources.manage",
  ],
  [
    "Webhooks",
    "webhook.read",
    "webhook.test",
    "webhooks.manage",
    "webhooks.manage",
  ],
  [
    "Webhooks",
    "webhook.read",
    "webhook.rotate",
    "webhooks.manage",
    "webhooks.manage",
  ],
] as const)(
  "%s labels %s with delegated %s as management only with its scope ceiling",
  async (label, readPermission, taskPermission, readScope, taskScope) => {
    const f = fixture();
    const permissions = [
      "settings.read",
      readPermission,
      taskPermission,
    ] as const;
    f.props.access = {
      ...f.props.access,
      permissions: [...permissions],
      availablePermissions: [...permissions],
      scopeEnforcement: true,
      scopes: [...new Set(["tenant.admin", readScope, taskScope] as const)],
    };
    const ui = render(<Settings {...f.props} />);
    await screen.findByRole("heading", { name: "Current policy" });
    const row = () => screen.getByRole("link", { name: label }).closest("li");
    expect(row()?.textContent).toContain("Manage eligible targets.");
    f.props.access = {
      ...f.props.access,
      permissions: ["settings.read", readPermission],
      availablePermissions: ["settings.read", readPermission],
    };
    ui.rerender(<Settings {...f.props} />);
    await screen.findByRole("heading", { name: "Current policy" });
    expect(row()?.textContent).toContain(
      "View; changes require separate authority.",
    );
    f.props.access = {
      ...f.props.access,
      permissions: [...permissions],
      availablePermissions: [...permissions],
      scopes: (["tenant.admin", readScope] as const).filter(
        (scope) => scope !== taskScope,
      ),
    };
    ui.rerender(<Settings {...f.props} />);
    await screen.findByRole("heading", { name: "Current policy" });
    if (readScope === taskScope)
      expect(screen.queryByRole("link", { name: label })).toBeNull();
    else
      expect(row()?.textContent).toContain(
        "View; changes require separate authority.",
      );
    // Permission hints update links without replacing the caller's settings draft.
    expect(f.get.mock.calls.map((call) => call[0]).sort()).toEqual([
      "/api/v1/capabilities",
      "/api/v1/tenant-settings",
    ]);
  },
);

it("does not interpret a failed capability read as disabled and allows explicit refresh", async () => {
  const f = fixture();
  f.caps.mockRejectedValueOnce(new Error("not available"));
  render(<Settings {...f.props} />);
  await screen.findByText(/Enablement is unknown/);
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh capability evidence" }),
  );
  await screen.findByRole("button", { name: "Refresh capability evidence" });
  expect(f.caps).toHaveBeenCalledTimes(2);
});

it.each(["x".repeat(2001), "😀".repeat(2001), "a\0b"])(
  "blocks reset before confirmation for an invalid reason of %i code units",
  async (reason) => {
    const f = fixture();
    render(<Settings {...f.props} />);
    await screen.findByLabelText("Reason");
    fireEvent.change(screen.getByLabelText("Reason"), {
      target: { value: reason },
    });
    fireEvent.click(
      screen.getByRole("button", {
        name: "Use deployment defaults for all settings",
      }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("Reason");
    expect(document.activeElement).toBe(screen.getByLabelText("Reason"));
    expect(f.post).not.toHaveBeenCalled();
  },
);

it("resets valid Unicode reasons while discarding invalid overrides, captures the ETag and permits cancellation", async () => {
  const f = fixture();
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Reason");
  fireEvent.change(screen.getByLabelText("Automatic sync interval source"), {
    target: { value: "override" },
  });
  fireEvent.change(screen.getByLabelText("Automatic sync interval"), {
    target: { value: "invalid" },
  });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "😀".repeat(2000) },
  });
  fireEvent.click(
    screen.getByRole("button", {
      name: "Use deployment defaults for all settings",
    }),
  );
  expect(screen.getByRole("dialog").textContent).toContain(tag);
  expect(
    screen
      .getByRole("button", { name: "Confirm" })
      .classList.contains("warning"),
  ).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(f.post).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", {
      name: "Use deployment defaults for all settings",
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText("Tenant settings saved.");
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: { overrides: {}, reason: "😀".repeat(2000) },
    params: { header: { "If-Match": tag } },
  });
});

it("shows explicit-equal-default changes and adopts a changed API default only on deliberate action", async () => {
  const f = fixture();
  const changed = deferred<ReturnType<typeof reply>>();
  f.settings
    .mockReturnValueOnce(Promise.resolve(reply(policy)))
    .mockReturnValueOnce(changed.promise);
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Reason");
  expect(
    screen.getByRole("button", { name: "Use current saved values" }),
  ).toHaveProperty("disabled", false);
  fireEvent.change(screen.getByLabelText("New Job maximum attempts source"), {
    target: { value: "override" },
  });
  const comparison = screen.getByRole("group", {
    name: "Proposed settings changes",
  });
  expect(
    within(comparison).getByRole("row", { name: /New Job maximum attempts/ })
      .textContent,
  ).toContain("Inherit deployment default555");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Keep explicit limit" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  changed.resolve(
    reply(
      {
        ...policy,
        deploymentDefaults: { ...defaults, newJobMaxAttempts: 11 },
        effective: { ...defaults, newJobMaxAttempts: 11 },
      },
      newerTag,
    ),
  );
  await screen.findByText(/The current policy differs/);
  expect(
    within(comparison).getByRole("row", { name: /New Job maximum attempts/ })
      .textContent,
  ).toContain("Inherit deployment default555");
  fireEvent.click(
    screen.getByRole("button", { name: "Keep draft and adopt current base" }),
  );
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "5",
  );
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(screen.getByRole("dialog").textContent).toContain(newerTag);
  expect(screen.getByRole("dialog").textContent).toContain("11 → 5");
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText("Tenant settings saved.");
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: { overrides: { newJobMaxAttempts: 5 } },
    params: { header: { "If-Match": newerTag } },
  });
});

it("keeps the draft, reason and old validator after a failed policy reload until the new base is deliberately adopted", async () => {
  const f = fixture();
  const failed = deferred<ReturnType<typeof reply>>();
  f.settings
    .mockResolvedValueOnce(reply(policy))
    .mockReturnValueOnce(failed.promise)
    .mockResolvedValueOnce(
      reply(
        {
          ...policy,
          revision: 1,
          overrides: { newJobMaxAttempts: 3 },
          effective: { ...defaults, newJobMaxAttempts: 3 },
        },
        newerTag,
      ),
    );
  const discard = vi.spyOn(window, "confirm");
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Reason");
  expect(
    screen.getByRole("button", { name: "Use current saved values" }),
  ).toHaveProperty("disabled", false);
  fireEvent.change(screen.getByLabelText("New Job maximum attempts source"), {
    target: { value: "override" },
  });
  fireEvent.change(screen.getByLabelText("New Job maximum attempts"), {
    target: { value: "7" },
  });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Keep my draft" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  expect(screen.getByRole("button", { name: "Save settings" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    screen.getByRole("button", { name: "Use current saved values" }),
  ).toHaveProperty("disabled", true);
  await act(async () => failed.reject(new Error("unavailable policy read")));
  expect(screen.getByRole("alert").textContent).toContain(
    "could not be loaded",
  );
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "7",
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep my draft",
  );
  expect(
    screen.getByRole("button", { name: "Use current saved values" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(screen.getByRole("dialog").textContent).toContain(tag);
  expect(
    screen
      .getByRole("button", { name: "Confirm" })
      .classList.contains("primary"),
  ).toBe(true);
  expect(screen.getByRole("dialog").textContent).not.toContain(newerTag);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  await screen.findByText(/The current policy differs/);
  expect(
    screen.getByRole("button", { name: "Use current saved values" }),
  ).toHaveProperty("disabled", false);
  expect(
    screen.getByRole("button", { name: "Keep draft and adopt current base" }),
  ).toHaveProperty("disabled", false);
  fireEvent.click(
    screen.getByRole("button", { name: "Keep draft and adopt current base" }),
  );
  expect(discard).not.toHaveBeenCalled();
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "7",
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep my draft",
  );
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  const summary = within(screen.getByRole("dialog")).getByLabelText(
    "Command summary",
  );
  expect(summary.textContent).toContain(newerTag);
  expect(summary.textContent).not.toContain(tag);
  expect(summary.textContent).toContain("3 → 7");
  expect(f.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(f.post).not.toHaveBeenCalled();
  expect(f.settings).toHaveBeenCalledTimes(3);
});

it("keeps a successful settings receipt and saved draft after readback fails without replaying the mutation", async () => {
  const f = fixture();
  const saved = {
    ...policy,
    revision: 1,
    overrides: { newJobMaxAttempts: 7 },
    effective: { ...defaults, newJobMaxAttempts: 7 },
    updatedAt: "2026-10-19T00:00:00Z",
  };
  f.post.mockResolvedValue(reply(saved, newerTag));
  f.settings
    .mockResolvedValueOnce(reply(policy))
    .mockRejectedValueOnce(new Error("unavailable policy read"))
    .mockResolvedValueOnce(reply(saved, newerTag));
  render(
    <OperationProvider
      access={f.props.access}
      runtime={f.props.runtime}
      view="settings"
    >
      <Settings {...f.props} />
    </OperationProvider>,
  );
  await screen.findByLabelText("Reason");
  fireEvent.change(screen.getByLabelText("New Job maximum attempts source"), {
    target: { value: "override" },
  });
  fireEvent.change(screen.getByLabelText("New Job maximum attempts"), {
    target: { value: "7" },
  });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Save once" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByRole("alert");
  expect(screen.getByText("Tenant settings saved.")).toBeTruthy();
  const receipt = screen.getByRole("region", {
    name: "Acknowledged administration change",
  });
  expect(receipt.textContent).toContain("tenant.settings.changed");
  expect(
    within(receipt).getByRole("button", { name: "Copy acknowledged revision" })
      .parentElement?.textContent,
  ).toContain("1");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Retry same command" }),
  ).toBeNull();
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "7",
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty("value", "");
  for (const name of [
    "Use current saved values",
    "Keep draft and adopt current base",
  ]) {
    const adopt = screen.getByRole("button", { name });
    expect(adopt).toHaveProperty("disabled", true);
    fireEvent.click(adopt);
  }
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "7",
  );
  expect(f.post).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByRole("button", { name: "Reload current policy" }),
  );
  await screen.findByText(/Revision 1/);
  expect(screen.queryByRole("alert")).toBeNull();
  expect(
    screen.getByRole("button", { name: "Use current saved values" }),
  ).toHaveProperty("disabled", false);
  expect(screen.getByText("Tenant settings saved.")).toBeTruthy();
  expect(f.post).toHaveBeenCalledTimes(1);
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: { overrides: { newJobMaxAttempts: 7 }, reason: "Save once" },
    params: { header: { "If-Match": tag } },
  });
  expect(f.settings).toHaveBeenCalledTimes(3);
});

function historyFixture() {
  const f = fixture();
  f.props.access.permissions = [
    "settings.read",
    "settings.manage",
    "audit.read",
  ];
  const event = {
    id: "history-event",
    formatVersion: 1,
    tenantId: "tenant-a",
    actor: "principal",
    at: "2026-10-19T00:00:00Z",
    action: "tenant.settings.changed",
    targetType: "tenant_settings",
    targetId: "tenant-a",
    reason: "Historical change",
    details: {
      principalType: "catalog_membership",
      actorType: "human",
      realmId: f.props.access.realmId,
      facts: {
        beforeRevision: 0,
        afterRevision: 1,
        beforeEnabledSet: false,
        beforeIntervalSet: false,
        beforeAttemptsSet: false,
        afterEnabledSet: true,
        afterEnabled: false,
        afterIntervalSet: false,
        afterAttemptsSet: true,
        afterAttempts: 5,
      },
    },
  };
  const history = vi.fn(async () => reply({ items: [event] }));
  f.get.mockImplementation(async (...args) =>
    args[0] === "/api/v1/audit-events"
      ? history()
      : args[0] === "/api/v1/capabilities"
        ? f.caps()
        : f.settings(),
  );
  return { ...f, history, event };
}
it.each(
  (["permission", "scope", "tenant", "realm"] as const).flatMap((change) =>
    (["success", "failure"] as const).map((outcome) => ({ change, outcome })),
  ),
)(
  "clears loaded Settings history and its cursor after $change changes, ignoring a late $outcome",
  async ({ change, outcome }) => {
    const f = historyFixture();
    const pending = deferred<ReturnType<typeof reply>>();
    f.props.access.scopeEnforcement = true;
    f.props.access.scopes = ["tenant.admin", "audit.read"];
    f.history
      .mockResolvedValueOnce(
        reply({ items: [f.event], nextCursor: "previous-private-page" }),
      )
      .mockReturnValueOnce(pending.promise);
    const view = render(<Settings {...f.props} />);
    await screen.findByRole("heading", { name: "Current policy" });
    fireEvent.click(
      screen.getByRole("button", { name: "Read settings history" }),
    );
    await screen.findByText("Historical change");
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await waitFor(() => expect(f.history).toHaveBeenCalledTimes(2));
    const request = f.get.mock.calls.at(-1)?.[1] as {
      signal: AbortSignal;
      params: { query: { cursor: string } };
    };
    expect(request.params.query.cursor).toBe("previous-private-page");

    const next = { ...f.props.access };
    if (change === "permission")
      next.permissions = ["settings.read", "settings.manage"];
    if (change === "scope") next.scopes = ["tenant.admin"];
    if (change === "tenant") next.tenantId = "tenant-b";
    if (change === "realm") next.realmId = "realm-b";
    f.settings.mockResolvedValue(reply({ ...policy, tenantId: next.tenantId }));
    f.history.mockResolvedValue(
      reply({
        items: [
          {
            ...f.event,
            tenantId: next.tenantId,
            targetId: next.tenantId,
            reason: "Current authorized history",
          },
        ],
      }),
    );
    view.rerender(<Settings {...f.props} access={next} />);
    await screen.findByRole("heading", { name: "Current policy" });
    expect(request.signal.aborted).toBe(true);
    expect(screen.queryByText("Historical change")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Refresh settings history" }),
    ).toBeNull();
    await act(async () => {
      if (outcome === "success") pending.resolve(reply({ items: [f.event] }));
      else pending.reject(new Error("obsolete private history failure"));
    });
    expect(screen.queryByText("Historical change")).toBeNull();
    expect(f.props.onFailure).not.toHaveBeenCalled();
    expect(f.history).toHaveBeenCalledTimes(2);
    if (change === "permission" || change === "scope") {
      expect(
        screen.queryByRole("button", { name: "Read settings history" }),
      ).toBeNull();
    } else {
      fireEvent.click(
        screen.getByRole("button", { name: "Read settings history" }),
      );
      await screen.findByText("Current authorized history");
      expect(f.get.mock.calls.at(-1)).toMatchObject([
        "/api/v1/audit-events",
        {
          params: {
            query: {
              action: "tenant.settings.changed",
              targetType: "tenant_settings",
              targetId: next.tenantId,
              limit: 20,
            },
          },
        },
      ]);
      expect(f.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
        "params.query.cursor",
      );
    }
    expect(f.post).not.toHaveBeenCalled();
  },
);
it("restores historical overrides only after confirmation and a fresh current base, then requires a new save reason", async () => {
  const f = historyFixture();
  const fresh = {
    ...policy,
    revision: 9,
    deploymentDefaults: { ...defaults, newJobMaxAttempts: 11 },
    effective: { ...defaults, newJobMaxAttempts: 11 },
  };
  f.settings
    .mockResolvedValueOnce(reply(policy))
    .mockResolvedValue(reply(fresh, newerTag));
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Reason");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Existing private reason" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Read settings history" }),
  );
  await screen.findByRole("heading", { name: "Revision 0 → 1" });
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Use revision 1 values in draft" }),
  );
  expect(f.settings).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Existing private reason",
  );
  confirm.mockReturnValue(true);
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Use revision 1 values in draft" }),
    ).toHaveProperty("disabled", false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Use revision 1 values in draft" }),
  );
  await screen.findByText(/Historical overrides are staged/);
  expect(f.settings).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Reason")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Automatic source sync")).toHaveProperty(
    "value",
    "paused",
  );
  expect(screen.getByLabelText("New Job maximum attempts")).toHaveProperty(
    "value",
    "5",
  );
  expect(
    screen.getByRole("group", { name: "Proposed settings changes" })
      .textContent,
  ).toContain("Comparison uses base revision 9");
  expect(
    screen.getByRole("group", { name: "Proposed settings changes" })
      .textContent,
  ).toContain("11");
  expect(screen.getByRole("button", { name: "Save settings" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(f.post).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Restore as a new policy change" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(screen.getByRole("dialog").textContent).toContain(newerTag);
  expect(screen.getByRole("dialog").textContent).toContain("11 → 5");
  expect(f.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText("Tenant settings saved.");
  expect(f.post.mock.calls[0]?.[1]).toMatchObject({
    body: {
      overrides: { automaticSourceSyncEnabled: false, newJobMaxAttempts: 5 },
      reason: "Restore as a new policy change",
    },
    params: { header: { "If-Match": newerTag } },
  });
});
it("keeps a dirty draft when the fresh restore read fails and fences fields while that read is pending", async () => {
  const f = historyFixture();
  const pending = deferred<ReturnType<typeof reply>>();
  f.settings
    .mockResolvedValueOnce(reply(policy))
    .mockReturnValueOnce(pending.promise);
  vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<Settings {...f.props} />);
  await screen.findByLabelText("Reason");
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Keep private draft" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Read settings history" }),
  );
  await screen.findByRole("heading", { name: "Revision 0 → 1" });
  fireEvent.click(
    screen.getByRole("button", { name: "Use revision 0 values in draft" }),
  );
  expect(screen.getByLabelText("Reason").closest("fieldset")).toHaveProperty(
    "disabled",
    true,
  );
  await act(async () => pending.reject(new Error("private failure")));
  expect(screen.getByRole("alert").textContent).toContain(
    "Your draft is unchanged",
  );
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep private draft",
  );
  expect(screen.getByLabelText("Reason").closest("fieldset")).toHaveProperty(
    "disabled",
    false,
  );
  expect(f.post).not.toHaveBeenCalled();
});

it("keeps a prepared Settings command and its draft while current authority disables the first send", async () => {
  const f = fixture();
  const view = render(<Settings {...f.props} />);
  await screen.findByLabelText("Automatic source sync");
  fireEvent.change(screen.getByLabelText("Automatic source sync"), {
    target: { value: "paused" },
  });
  fireEvent.change(screen.getByLabelText("Reason"), {
    target: { value: "Retain this settings reason" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  const dialog = await screen.findByRole("dialog");
  view.rerender(
    <Settings
      {...f.props}
      access={{ ...f.props.access, permissions: ["settings.read"] }}
    />,
  );
  const confirm = within(dialog).getByRole("button", {
    name: "Confirm",
  });
  expect(confirm).toHaveProperty("disabled", true);
  fireEvent.click(confirm);
  expect(f.post).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Retain this settings reason",
  );
  expect(dialog.textContent).toContain(tag);
});

it("admits newly granted Settings authority without replacing its later protected draft", async () => {
  const f = fixture();
  const denied = { ...f.props.access, permissions: [] };
  const view = render(<Settings {...f.props} access={denied} />);
  expect(screen.getByRole("heading", { name: "Tenant settings" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Review My access" })).toBeTruthy();
  expect(f.get).not.toHaveBeenCalled();
  view.rerender(
    <Settings
      {...f.props}
      access={{ ...f.props.access, permissions: ["settings.read"] }}
    />,
  );
  await screen.findByRole("heading", { name: "Current policy" });
  expect(screen.queryByLabelText("Reason")).toBeNull();
  view.rerender(<Settings {...f.props} />);
  const reason = await screen.findByLabelText("Reason");
  fireEvent.change(reason, {
    target: { value: "Preserve after authority refresh" },
  });
  fireEvent.change(screen.getByLabelText("Automatic source sync"), {
    target: { value: "paused" },
  });
  view.rerender(<Settings {...f.props} access={denied} />);
  fireEvent.submit(reason.closest("form")!);
  expect(f.post).not.toHaveBeenCalled();
  expect(reason).toHaveProperty("value", "Preserve after authority refresh");
  expect(reason.closest("fieldset")).toHaveProperty("disabled", true);
  expect(
    window.dispatchEvent(new Event("beforeunload", { cancelable: true })),
  ).toBe(false);
});
