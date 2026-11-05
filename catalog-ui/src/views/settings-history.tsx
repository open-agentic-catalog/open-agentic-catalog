import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
// Historical overrides are evidence; restoration always creates a new private draft.
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { permits } from "../api/permissions";
import { object, number, timestamp } from "../api/values";
import { EvidenceDetails } from "./authoring";
import { CopyReference } from "./CopyReference";
import {
  settingsDraft,
  settingsOverrides,
  settingLabels,
  settingText,
} from "./settings-policy";
import { Pager, ReadState, useRead } from "./workspace";
import type { WorkspaceProps } from "./workspace";
import * as checks from "./responses";

type Overrides = components["schemas"]["TenantSettingsOverrides"];
type Event = components["schemas"]["AuditEvent"];

/** Missing or contradictory presence facts never mean inheritance. */
export function settingsHistoryValues(
  event: Event,
  tenant: string,
):
  | {
      before: Overrides;
      after: Overrides;
      beforeRevision: number;
      afterRevision: number;
    }
  | undefined {
  if (
    event.formatVersion !== 1 ||
    event.tenantId !== tenant ||
    event.targetType !== "tenant_settings" ||
    event.targetId !== tenant ||
    event.action !== "tenant.settings.changed" ||
    !timestamp(event.at) ||
    !object(event.details) ||
    !object(event.details.facts)
  )
    return;
  const facts = event.details.facts;
  const names = ["beforeRevision", "afterRevision"];
  const values: Overrides[] = [];
  for (const prefix of ["before", "after"]) {
    const value: Overrides = {};
    for (const suffix of ["Enabled", "Interval", "Attempts"] as const) {
      names.push(prefix + suffix + "Set");
      const present = facts[prefix + suffix + "Set"];
      if (typeof present !== "boolean") return;
      if (present) {
        names.push(prefix + suffix);
        const raw = facts[prefix + suffix];
        if (suffix === "Enabled" && typeof raw === "boolean")
          value.automaticSourceSyncEnabled = raw;
        else if (suffix === "Interval" && typeof raw === "string")
          value.automaticSourceSyncInterval = raw;
        else if (suffix === "Attempts" && number(raw, 1, 20, true))
          value.newJobMaxAttempts = raw as number;
        else return;
      } else if (Object.hasOwn(facts, prefix + suffix)) return;
    }
    try {
      settingsOverrides(settingsDraft(value));
    } catch {
      return;
    }
    values.push(value);
  }
  if (
    Object.keys(facts).some((key) => !names.includes(key)) ||
    !number(facts.beforeRevision, 0, Number.MAX_SAFE_INTEGER, true) ||
    !number(facts.afterRevision, 1, Number.MAX_SAFE_INTEGER, true) ||
    facts.afterRevision !== (facts.beforeRevision as number) + 1
  )
    return;
  return {
    before: values[0]!,
    after: values[1]!,
    beforeRevision: facts.beforeRevision as number,
    afterRevision: facts.afterRevision as number,
  };
}

export default function SettingsHistory(
  props: WorkspaceProps & {
    restore: (overrides: Overrides) => Promise<void>;
    disabled: boolean;
  },
) {
  const [opened, setOpened] = useState(false);
  const [cursor, setCursor] = useState("");
  const [restoring, setRestoring] = useState(false);
  const [error, setError] = useState("");
  const current = useRef(true);
  useEffect(() => {
    current.current = true;
    return () => {
      current.current = false;
    };
  }, []);
  const allowed = permits(props.access, "audit.read", "audit.read");
  const page = useRead(
    JSON.stringify([opened, cursor]),
    async (signal) =>
      !opened || !allowed
        ? null
        : result(
            await props.client.api.GET("/api/v1/audit-events", {
              params: {
                query: {
                  action: "tenant.settings.changed",
                  targetType: "tenant_settings",
                  targetId: props.access.tenantId,
                  limit: 20,
                  ...(cursor ? { cursor } : {}),
                },
              },
              signal,
            }),
            checks.AuditEventPage,
          ),
    props.onFailure,
  );
  if (!allowed)
    return (
      <p>
        Settings history requires audit.read permission and its OAuth scope when
        enforced.
      </p>
    );
  return (
    <section className="workspace-panel" aria-label="Settings history">
      <h2>Settings history</h2>
      <p>
        Read recorded override changes explicitly. Historical facts do not
        include past deployment defaults or past effective worker settings.
      </p>
      <button
        disabled={page.busy && opened}
        onClick={() => {
          if (!opened) setOpened(true);
          else page.refresh();
        }}
      >
        {controlIcons["history"]}
        {opened ? "Refresh settings history" : "Read settings history"}
      </button>
      {opened && (
        <>
          <ReadState {...page} />
          {error && <p role="alert">{error}</p>}
          {page.data?.items.map((event) => {
            const values = settingsHistoryValues(event, props.access.tenantId);
            return (
              <article key={event.id} className="workspace-panel">
                <h3>
                  {values
                    ? `Revision ${values.beforeRevision} → ${values.afterRevision}`
                    : "Recorded settings evidence"}
                </h3>
                <p>
                  <time dateTime={event.at}>
                    {new Date(event.at).toLocaleString()}
                  </time>
                </p>
                <CopyReference
                  label="settings history event ID"
                  value={event.id}
                />
                <CopyReference
                  label="settings history principal"
                  value={event.actor}
                />
                {event.reason && <p>{event.reason}</p>}
                {values ? (
                  <>
                    <Facts
                      rows={settingLabels.map(([key, label]) => [
                        `${label} override`,
                        `${settingText(values.before[key])} → ${settingText(values.after[key])}`,
                      ])}
                    />
                    {permits(props.access, "settings.manage", "tenant.admin") &&
                      (["before", "after"] as const).map((side) => (
                        <button
                          key={side}
                          disabled={props.disabled || restoring}
                          onClick={() => {
                            setError("");
                            setRestoring(true);
                            void props
                              .restore(values[side])
                              .catch((failure: unknown) => {
                                if (!current.current) return;
                                setError(
                                  "The current settings could not be read. Your draft is unchanged.",
                                );
                                props.onFailure(failure);
                              })
                              .finally(() => {
                                if (current.current) setRestoring(false);
                              });
                          }}
                        >
                          {controlIcons["edit"]}
                          Use revision{" "}
                          {side === "before"
                            ? values.beforeRevision
                            : values.afterRevision}{" "}
                          values in draft
                        </button>
                      ))}
                  </>
                ) : (
                  <p>
                    These facts are incomplete or unrecognized. Restore is
                    unavailable; inspect the original authorized evidence below.
                  </p>
                )}
                <EvidenceDetails
                  title="Original settings audit evidence"
                  value={event}
                />
              </article>
            );
          })}
          {page.data?.items.length === 0 && (
            <p>No settings changes match this page.</p>
          )}
          <Pager
            label="Settings history pagination"
            cursor={cursor}
            nextCursor={page.data?.nextCursor}
            busy={page.busy || restoring}
            onPage={setCursor}
          />
        </>
      )}
    </section>
  );
}
