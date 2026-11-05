// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import SettingsHistory, { settingsHistoryValues } from "./settings-history";
import {
  administrator,
  click,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
import { deferred } from "../../tests/unit/fixtures";

screenLifecycle("/unit/settings");
const event: components["schemas"]["AuditEvent"] = {
  formatVersion: 1,
  id: "00000000-0000-7000-8000-000000000001",
  tenantId: administrator.tenantId,
  actor: "00000000-0000-7000-8000-000000000002",
  action: "tenant.settings.changed",
  targetType: "tenant_settings",
  targetId: administrator.tenantId,
  at: stamp,
  reason: "A synthetic policy change",
  details: {
    principalType: "catalog_membership",
    actorType: "human",
    realmId: administrator.realmId,
    facts: {
      beforeRevision: 2,
      afterRevision: 3,
      beforeEnabledSet: true,
      beforeEnabled: false,
      beforeIntervalSet: false,
      beforeAttemptsSet: true,
      beforeAttempts: 3,
      afterEnabledSet: false,
      afterIntervalSet: true,
      afterInterval: "1m30.123456789s",
      afterAttemptsSet: true,
      afterAttempts: 3,
    },
  },
};
const expected = {
  before: { automaticSourceSyncEnabled: false, newJobMaxAttempts: 3 },
  after: {
    automaticSourceSyncInterval: "1m30.123456789s",
    newJobMaxAttempts: 3,
  },
  beforeRevision: 2,
  afterRevision: 3,
};
it("preserves explicit false, inherited absence, fractional duration and default-equal overrides", () => {
  expect(settingsHistoryValues(event, administrator.tenantId)).toEqual(
    expected,
  );
});
it("accepts all explicit override fields at their inclusive bounds without turning false into inheritance", () => {
  const changed = structuredClone(event);
  changed.details.facts = {
    beforeRevision: 0,
    afterRevision: 1,
    beforeEnabledSet: true,
    beforeEnabled: true,
    beforeIntervalSet: true,
    beforeInterval: "1m",
    beforeAttemptsSet: true,
    beforeAttempts: 1,
    afterEnabledSet: true,
    afterEnabled: false,
    afterIntervalSet: true,
    afterInterval: "24h",
    afterAttemptsSet: true,
    afterAttempts: 20,
  };
  expect(settingsHistoryValues(changed, administrator.tenantId)).toEqual({
    beforeRevision: 0,
    afterRevision: 1,
    before: {
      automaticSourceSyncEnabled: true,
      automaticSourceSyncInterval: "1m",
      newJobMaxAttempts: 1,
    },
    after: {
      automaticSourceSyncEnabled: false,
      automaticSourceSyncInterval: "24h",
      newJobMaxAttempts: 20,
    },
  });
});
it("recognizes complete inheritance facts for the first recorded change", () => {
  const changed = structuredClone(event);
  changed.details.facts = {
    beforeRevision: 0,
    afterRevision: 1,
    beforeEnabledSet: false,
    beforeIntervalSet: false,
    beforeAttemptsSet: false,
    afterEnabledSet: false,
    afterIntervalSet: false,
    afterAttemptsSet: false,
  };
  expect(settingsHistoryValues(changed, administrator.tenantId)).toEqual({
    beforeRevision: 0,
    afterRevision: 1,
    before: {},
    after: {},
  });
});
it.each([
  "beforeEnabledSet",
  "beforeIntervalSet",
  "beforeAttemptsSet",
  "afterEnabledSet",
  "afterIntervalSet",
  "afterAttemptsSet",
])("never infers inheritance when the %s presence flag is missing", (key) => {
  const changed = structuredClone(event);
  delete (changed.details.facts as Record<string, unknown>)[key];
  expect(
    settingsHistoryValues(changed, administrator.tenantId),
  ).toBeUndefined();
});
it.each([
  ["afterAttemptsSet", null],
  ["beforeEnabled", "false"],
  ["afterInterval", "30s"],
  ["afterInterval", "24h1ns"],
  ["afterAttempts", 1.5],
  ["afterAttempts", 21],
  ["beforeRevision", -1],
  ["afterRevision", 5],
  ["afterRevision", 3.5],
  ["unrecognized", true],
  ["beforeInterval", "1m"],
  ["afterEnabled", false],
] as const)(
  "withholds restore for malformed or unrecognized fact %s=%s",
  (key, value) => {
    const changed = structuredClone(event);
    const facts = changed.details.facts as Record<string, unknown>;
    facts[key] = value;
    expect(
      settingsHistoryValues(changed, administrator.tenantId),
    ).toBeUndefined();
  },
);
it.each([
  { action: "feedback.address" },
  { tenantId: "other-tenant" },
  { targetId: "other-tenant" },
  { targetType: "publisher" },
  { formatVersion: 2 },
  { at: "invalid" },
  { details: {} },
])(
  "withholds restore for a different event binding or format: %j",
  (change) => {
    expect(
      settingsHistoryValues(
        { ...event, ...change } as typeof event,
        administrator.tenantId,
      ),
    ).toBeUndefined();
  },
);
it("reads exact settings history only on request, preserves pages and retains unrecognized original evidence", async () => {
  const w = screenWorkspace();
  const invalid = {
    ...event,
    id: "00000000-0000-7000-8000-000000000003",
    details: { facts: {} },
  };
  w.get
    .mockResolvedValueOnce(
      etagReply({ items: [event], nextCursor: "next-history" }),
    )
    .mockResolvedValueOnce(etagReply({ items: [invalid] }));
  const restore = vi.fn(async () => {});
  render(
    <SettingsHistory
      {...w.props}
      access={administrator}
      restore={restore}
      disabled={false}
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  click("Read settings history");
  await screen.findByRole("heading", { name: "Revision 2 → 3" });
  expect(w.get.mock.calls[0]).toMatchObject([
    "/api/v1/audit-events",
    {
      params: {
        query: {
          action: "tenant.settings.changed",
          targetType: "tenant_settings",
          targetId: administrator.tenantId,
          limit: 20,
        },
      },
    },
  ]);
  click("Use revision 2 values in draft");
  await waitFor(() => expect(restore).toHaveBeenCalledWith(expected.before));
  click("Next page");
  await screen.findByText(/facts are incomplete or unrecognized/);
  expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: {
        cursor: "next-history",
        action: "tenant.settings.changed",
        targetId: administrator.tenantId,
      },
    },
  });
  expect(
    screen.queryByRole("button", { name: /Restore .* overrides to draft/ }),
  ).toBeNull();
  fireEvent.click(screen.getByText("Original settings audit evidence"));
  expect(screen.getByText(/"facts"/)).toBeTruthy();
  expect(w.post).not.toHaveBeenCalled();
  w.get.mockResolvedValueOnce(etagReply({ items: [] }));
  click("Refresh settings history");
  await screen.findByText("No settings changes match this page.");
});
it("fences overlapping restores and reports failed current reads without a mutation", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [event] }));
  const pending = deferred<void>();
  const restore = vi.fn(() => pending.promise);
  render(
    <SettingsHistory
      {...w.props}
      access={administrator}
      restore={restore}
      disabled={false}
    />,
  );
  click("Read settings history");
  await screen.findByRole("heading", { name: "Revision 2 → 3" });
  click("Use revision 3 values in draft");
  expect(
    screen.getByRole("button", { name: "Use revision 2 values in draft" }),
  ).toHaveProperty("disabled", true);
  pending.reject(new Error("private database sentinel"));
  await screen.findByRole("alert");
  expect(screen.getByRole("alert").textContent).not.toContain(
    "private database sentinel",
  );
  expect(w.props.onFailure).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
});
it("does not forward a failed restore from an obsolete private page to session reconciliation", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [event] }));
  const pending = deferred<void>();
  const ui = render(
    <SettingsHistory
      {...w.props}
      access={administrator}
      restore={() => pending.promise}
      disabled={false}
    />,
  );
  click("Read settings history");
  await screen.findByRole("heading", { name: "Revision 2 → 3" });
  click("Use revision 2 values in draft");
  ui.unmount();
  await act(async () =>
    pending.reject(new Error("Obsolete private read refusal")),
  );
  expect(w.props.onFailure).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});
it("allows read-only history but refuses export-only OAuth ceilings", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [event] }));
  const access = {
    ...administrator,
    permissions: [
      "settings.read" as const,
      "audit.read" as const,
      "audit.export" as const,
    ],
    scopeEnforcement: true,
    scopes: ["tenant.admin" as const, "audit.read" as const],
  };
  const view = render(
    <SettingsHistory
      {...w.props}
      access={access}
      restore={vi.fn()}
      disabled={false}
    />,
  );
  click("Read settings history");
  await screen.findByRole("heading", { name: "Revision 2 → 3" });
  expect(
    screen.queryByRole("button", { name: /Restore .* overrides to draft/ }),
  ).toBeNull();
  view.unmount();
  render(
    <SettingsHistory
      {...w.props}
      access={{ ...access, scopes: ["tenant.admin", "audit.export"] }}
      restore={vi.fn()}
      disabled={false}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "Read settings history" }),
  ).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(1);
});
