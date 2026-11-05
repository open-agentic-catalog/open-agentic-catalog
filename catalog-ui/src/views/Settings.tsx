import { commandSummary } from "./workspace";
import { controlIcons } from "../shell/Icon";
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import SettingsControls from "./SettingsControls";
import SettingsHistory from "./settings-history";
import { acknowledged } from "./audit-handoff";
import { InputError, atField, characterText } from "./input-validation";
import {
  settingsDraft as toDraft,
  settingsOverrides as toOverrides,
  settingLabels,
  settingText,
  settingsChanges,
} from "./settings-policy";
import type { SettingsDraft as Draft } from "./settings-policy";
import { permits } from "../api/permissions";
import { object, number, timestamp } from "../api/values";
import { CommandReceipt, EvidenceDetails } from "./authoring";
import {
  CommandDialog,
  Field,
  Heading,
  ReadState,
  Reason,
  useRead,
  useUnsaved,
} from "./workspace";
import type { Command, WorkspaceProps } from "./workspace";

type Overrides = components["schemas"]["TenantSettingsOverrides"];
type SettingsValue = components["schemas"]["TenantSettings"];
type Loaded = { value: SettingsValue; tag: string };
const fields = [
  "automaticSourceSyncEnabled",
  "automaticSourceSyncInterval",
  "newJobMaxAttempts",
];
function validFields(value: unknown, required: boolean): boolean {
  return (
    object(value) &&
    Object.keys(value).every((key) => fields.includes(key)) &&
    (value.automaticSourceSyncEnabled === undefined
      ? !required
      : typeof value.automaticSourceSyncEnabled === "boolean") &&
    (value.automaticSourceSyncInterval === undefined
      ? !required
      : typeof value.automaticSourceSyncInterval === "string" &&
        value.automaticSourceSyncInterval.length >= 2 &&
        value.automaticSourceSyncInterval.length <= 64) &&
    (value.newJobMaxAttempts === undefined
      ? !required
      : number(value.newJobMaxAttempts, 1, 20))
  );
}
export function isSettings(value: unknown): value is SettingsValue {
  return (
    object(value) &&
    typeof value.tenantId === "string" &&
    number(value.revision) &&
    validFields(value.overrides, false) &&
    validFields(value.deploymentDefaults, true) &&
    validFields(value.effective, true) &&
    (value.updatedAt === undefined || timestamp(value.updatedAt))
  );
}
function loaded(
  response: Parameters<typeof result<SettingsValue>>[0],
  tenant: string,
): Loaded {
  const value = result(response, isSettings);
  const tag = response.response.headers.get("ETag");
  if (
    value.tenantId !== tenant ||
    !tag ||
    !/^"settings-[a-f0-9]{64}"$/.test(tag)
  )
    throw new Error("The settings response is incomplete.");
  return { value, tag };
}
export default function Settings(props: WorkspaceProps) {
  return (
    <SettingsAccess
      key={JSON.stringify([
        props.access.tenantId,
        props.access.realmId,
        props.access.membershipId,
        props.access.subject,
        props.access.actorType,
      ])}
      {...props}
    />
  );
}
function SettingsAccess(props: WorkspaceProps) {
  const authorized = permits(props.access, "settings.read", "tenant.admin");
  const [admitted, setAdmitted] = useState(authorized);
  if (authorized && !admitted) setAdmitted(true);
  return (
    <>
      {!authorized && (
        <>
          {!admitted && (
            <Heading
              title="Tenant settings"
              icon="settings"
              description="Manage tenant overrides for new work."
            />
          )}
          <p role="alert">
            You do not have access to tenant settings. Required access:
            settings.read and, when enforced, tenant.admin scope.
          </p>
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </>
      )}
      {(authorized || admitted) && <SettingsEditor {...props} />}
    </>
  );
}
function SettingsEditor(props: WorkspaceProps) {
  const readAllowed = permits(props.access, "settings.read", "tenant.admin");
  const manage =
    readAllowed && permits(props.access, "settings.manage", "tenant.admin");
  const [editableDraft, setEditableDraft] = useState(manage);
  if (manage && !editableDraft) setEditableDraft(true);
  const form = useRef<HTMLFormElement>(null);
  const policyRead = useRead(
    props.access.tenantId,
    async (signal) =>
      loaded(
        await props.client.api.GET("/api/v1/tenant-settings", { signal }),
        props.access.tenantId,
      ),
    props.onFailure,
    true,
  );
  const [restoredRead, setRestoredRead] = useState<Loaded>();
  const read = {
    ...policyRead,
    data: restoredRead ?? policyRead.data,
    error: restoredRead ? "" : policyRead.error,
    problem: restoredRead ? undefined : policyRead.problem,
    refresh: () => {
      setRestoredRead(undefined);
      policyRead.refresh();
    },
  };
  const [base, setBase] = useState<Loaded>();
  const [draft, setDraft] = useState<Draft>();
  const [previous, setPrevious] = useState<Overrides>();
  const [reason, setReason] = useState("");
  const [command, setCommand] = useState<Command>();
  const [message, setMessage] = useState("");
  const [acknowledgements, setAcknowledgements] = useState(0);
  const [error, setError] = useState("");
  const [restoringHistory, setRestoringHistory] = useState(false);
  const [historyNotice, setHistoryNotice] = useState("");
  useEffect(() => {
    if (read.data && !base) {
      const current = read.data;
      if (!current) return;
      setBase(current);
      setDraft(toDraft(current.value.overrides));
    }
  }, [read.data, base]);
  const dirty =
    !!base &&
    !!draft &&
    JSON.stringify(draft) !== JSON.stringify(toDraft(base.value.overrides));
  useUnsaved(dirty || !!reason);
  const invalid = (e: unknown) => {
    setError(e instanceof Error ? e.message : "Check the settings.");
    const target =
      e instanceof InputError ? document.getElementById(e.fieldId) : null;
    if (target instanceof HTMLElement && form.current?.contains(target))
      target.focus();
  };
  let comparison;
  if (base && draft) {
    try {
      comparison = settingsChanges(base.value, toOverrides(draft));
    } catch {
      /* An incomplete draft is validated on preparation. */
    }
  }
  const prepare = (overrides: Overrides, title: string, reset = false) => {
    if (!base) return;
    const before = base.value.overrides;
    const capturedReason = atField("settings-reason", () =>
      characterText(reason.trim(), "Reason", 2000, 1),
    );
    const body = { overrides: { ...overrides }, reason: capturedReason };
    const tag = base.tag;
    const changes = settingsChanges(base.value, body.overrides);
    setError("");
    setMessage("");
    setCommand({
      title,
      intent: reset ? "warning" : "primary",
      icon: reset ? "history" : "save",
      description:
        "Changes apply to new work. Existing Jobs keep their settings; manual sync and live discovery remain available.",
      summary: commandSummary([
        ["Based on revision", base.value.revision.toString()] as const,
        ["Base ETag", tag] as const,
        ...changes.flatMap((change) => [
          [
            `${change.label} override change`,
            `${change.before} → ${change.after}`,
          ] as const,
          [
            `${change.label} effective on this API`,
            `${change.effectiveBefore} → ${change.effectiveAfter}`,
          ] as const,
        ]),
        ...settingLabels.map(
          ([key, label]) => [label, settingText(body.overrides[key])] as const,
        ),
        ["Reason", body.reason] as const,
      ]),
      run: async (key) => {
        const reply = await props.client.api.POST("/api/v1/tenant-settings", {
          body,
          params: { header: { "Idempotency-Key": key, "If-Match": tag } },
        });
        const saved = loaded(reply, props.access.tenantId);
        setPrevious(before);
        setBase(saved);
        setDraft(toDraft(saved.value.overrides));
        setReason("");
        return acknowledged("Tenant settings saved.", reply.response, {
          action: "tenant.settings.changed",
          targetType: "tenant_settings",
          targetId: props.access.tenantId,
          revision: saved.value.revision,
        });
      },
    });
  };
  return (
    <>
      <Heading
        title="Tenant settings"
        icon="settings"
        description="Change selected tenant operations without restarting the Catalog."
      />
      <ReadState {...read} />
      {historyNotice && (
        <p role="status" className="notice">
          {historyNotice}
        </p>
      )}
      {read.data && (
        <section className="workspace-panel" aria-label="Current tenant policy">
          <h2>Current policy</h2>
          <p>
            Revision {read.data.value.revision}
            {read.data.value.updatedAt
              ? ` · Saved ${new Date(read.data.value.updatedAt).toLocaleString()}`
              : " · No saved overrides"}
          </p>
          <div
            className="workspace-table-wrap"
            role="group"
            aria-label="Tenant settings values"
            tabIndex={0}
          >
            <table className="workspace-table">
              <thead>
                <tr>
                  <th scope="col">Setting</th>
                  <th scope="col">Saved override</th>
                  <th scope="col">Deployment default on this API</th>
                  <th scope="col">Effective on this API</th>
                </tr>
              </thead>
              <tbody>
                {settingLabels.map(([key, label]) => (
                  <tr key={key}>
                    <th scope="row">{label}</th>
                    <td>
                      <span className="workspace-status" data-tone="neutral">
                        {settingText(read.data!.value.overrides[key])}
                      </span>
                    </td>
                    <td>
                      {settingText(read.data!.value.deploymentDefaults[key])}
                    </td>
                    <td>{settingText(read.data!.value.effective[key])}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p>
            Inherited settings use each process's deployment defaults. These
            defaults belong to this API; worker defaults can differ. Saved
            overrides take precedence in both processes.
          </p>
          <EvidenceDetails
            title="Exact settings values"
            value={read.data.value}
          />
          <button
            type="button"
            disabled={
              !readAllowed || read.busy || !!command || restoringHistory
            }
            onClick={read.refresh}
          >
            {controlIcons["refresh"]}
            Reload current policy
          </button>
          {base && read.data.tag !== base.tag && (
            <p role="status">
              The current policy differs from your draft's base revision{" "}
              {base.value.revision}. Compare the values above. Your draft is
              retained.
            </p>
          )}
        </section>
      )}
      {!manage && (
        <p className="notice">
          You can read these settings. Saving requires settings.manage.
        </p>
      )}
      {editableDraft && draft && base && (
        <form
          ref={form}
          className="workspace-panel"
          onSubmit={(event) => {
            event.preventDefault();
            if (!manage) return;
            try {
              prepare(toOverrides(draft), "Save tenant settings");
            } catch (e) {
              invalid(e);
            }
          }}
        >
          <h2>Edit tenant overrides</h2>
          <p>
            Leave a field inherited to use deployment defaults. Changes apply to
            new work; they do not cancel or change existing Jobs.
          </p>
          <fieldset
            className="workspace-fields"
            disabled={!manage || !!command || restoringHistory}
          >
            <fieldset className="source-settings-group">
              <legend>Automatic source sync</legend>
              <Field
                label="Automatic source sync"
                hint={`Inherited on this API: ${settingText(base.value.deploymentDefaults.automaticSourceSyncEnabled)}. Pause stops new automatic sync admission; it does not stop retained Jobs, manual sync or live discovery.`}
              >
                <select
                  id="settings-enabled"
                  value={draft.enabled}
                  onChange={(e) =>
                    setDraft({ ...draft, enabled: e.target.value })
                  }
                >
                  <option value="inherit">Use deployment default</option>
                  <option value="enabled">Enabled</option>
                  <option value="paused">Paused</option>
                </select>
              </Field>
            </fieldset>
            <fieldset className="source-settings-group">
              <legend>Automatic sync interval</legend>
              <Field
                label="Automatic sync interval source"
                hint={`Inherited on this API: ${base.value.deploymentDefaults.automaticSourceSyncInterval}. Scheduling keeps existing anchors and jitter.`}
              >
                <select
                  value={draft.intervalMode}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      intervalMode: e.target.value as Draft["intervalMode"],
                      interval:
                        draft.interval ||
                        base.value.deploymentDefaults
                          .automaticSourceSyncInterval,
                    })
                  }
                >
                  <option value="inherit">Use deployment default</option>
                  <option value="override">Set a tenant value</option>
                </select>
              </Field>
              <Field
                label="Automatic sync interval"
                hint="Set a tenant value from 1m to 24h, for example 15m or 1m30.5s."
                help="A longer interval can delay the next automatic sync. The existing source schedule still applies."
              >
                <input
                  id="settings-interval"
                  disabled={draft.intervalMode === "inherit"}
                  required={draft.intervalMode === "override"}
                  value={draft.interval}
                  maxLength={64}
                  onChange={(e) =>
                    setDraft({ ...draft, interval: e.target.value })
                  }
                  placeholder="Inherit deployment default"
                />
              </Field>
            </fieldset>
            <fieldset className="source-settings-group">
              <legend>New Job attempt limit</legend>
              <Field
                label="New Job maximum attempts source"
                hint={`Deployment default on this API: ${base.value.deploymentDefaults.newJobMaxAttempts}. Applies to eligible new Jobs.`}
                help="Applies to new default-budget source-sync, ANS and semantic Jobs. Existing Jobs, retry children, deduplicated work, explicit budgets and webhook retries keep their existing limits."
              >
                <select
                  value={draft.attemptsMode}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      attemptsMode: e.target.value as Draft["attemptsMode"],
                      attempts:
                        draft.attempts ||
                        String(base.value.deploymentDefaults.newJobMaxAttempts),
                    })
                  }
                >
                  <option value="inherit">Use deployment default</option>
                  <option value="override">Set a tenant value</option>
                </select>
              </Field>
              <Field
                label="New Job maximum attempts"
                hint="Set a tenant value from 1 to 20 attempts, including the first attempt. Existing retries keep their original limit."
              >
                <input
                  id="settings-attempts"
                  disabled={draft.attemptsMode === "inherit"}
                  required={draft.attemptsMode === "override"}
                  type="number"
                  min={1}
                  max={20}
                  step={1}
                  value={draft.attempts}
                  onChange={(e) =>
                    setDraft({ ...draft, attempts: e.target.value })
                  }
                  placeholder="Inherit deployment default"
                />
              </Field>
            </fieldset>
            <Reason id="settings-reason" value={reason} onChange={setReason} />
            <div
              className="workspace-table-wrap"
              tabIndex={0}
              role="group"
              aria-label="Proposed settings changes"
            >
              <p>
                Comparison uses base revision {base.value.revision} and its API
                defaults. Worker defaults can differ. A deployment-default
                change can invalidate the ETag without changing the saved
                revision.
              </p>
              {comparison ? (
                comparison.length ? (
                  <table className="workspace-table">
                    <thead>
                      <tr>
                        <th>Setting</th>
                        <th>Saved override before</th>
                        <th>Proposed override</th>
                        <th>Effective before on this API</th>
                        <th>Effective after on this API</th>
                      </tr>
                    </thead>
                    <tbody>
                      {comparison.map((change) => (
                        <tr key={change.key}>
                          <th scope="row">{change.label}</th>
                          <td>{change.before}</td>
                          <td>{change.after}</td>
                          <td>{change.effectiveBefore}</td>
                          <td>{change.effectiveAfter}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <p>No override changes.</p>
                )
              ) : (
                <p>Enter valid overrides to compare their effect.</p>
              )}
            </div>
            {error && <p role="alert">{error}</p>}
            <div className="form-actions">
              <button
                className="primary"
                disabled={!reason.trim() || read.busy}
              >
                {controlIcons["save"]}
                Save settings
              </button>
              <button
                className="warning"
                type="button"
                disabled={!reason.trim() || read.busy}
                onClick={() => {
                  try {
                    prepare({}, "Reset all tenant overrides", true);
                  } catch (e) {
                    invalid(e);
                  }
                }}
              >
                {controlIcons["history"]}
                Use deployment defaults for all settings
              </button>
              {read.data && (
                <button
                  type="button"
                  disabled={read.busy || !!read.error}
                  onClick={() => {
                    if (
                      dirty &&
                      !window.confirm(
                        "Discard your draft and use current saved values?",
                      )
                    )
                      return;
                    const current = read.data;
                    if (!current) return;
                    setBase(current);
                    setDraft(toDraft(current.value.overrides));
                  }}
                >
                  {controlIcons["edit"]}
                  Use current saved values
                </button>
              )}
              {read.data && read.data.tag !== base.tag && (
                <button
                  type="button"
                  disabled={read.busy || !!read.error}
                  onClick={() => {
                    if (read.data) setBase(read.data);
                  }}
                >
                  {controlIcons["edit"]}
                  Keep draft and adopt current base
                </button>
              )}
              {(read.busy || read.error) && (
                <p className="muted">
                  Reload successfully before adopting saved values. Your draft
                  and any confirmed save are retained.
                </p>
              )}
              {previous && (
                <button
                  type="button"
                  onClick={() => {
                    if (
                      dirty &&
                      !window.confirm(
                        "Discard your draft and restore previous overrides?",
                      )
                    )
                      return;
                    setDraft(toDraft(previous));
                  }}
                >
                  {controlIcons["edit"]}
                  Restore previous overrides to draft
                </button>
              )}
            </div>
          </fieldset>
        </form>
      )}
      <CommandReceipt key={acknowledgements} message={message} />
      <SettingsHistory
        key={JSON.stringify(props.access)}
        {...props}
        disabled={!!command || !manage || restoringHistory || read.busy}
        restore={async (overrides) => {
          if (!manage) return;
          if (
            !window.confirm(
              "Replace the current draft and reason with historical overrides after reading current settings? Saving still requires a new reason and confirmation.",
            )
          )
            return;
          setRestoringHistory(true);
          try {
            const current = loaded(
              await props.client.api.GET("/api/v1/tenant-settings", {}),
              props.access.tenantId,
            );
            setRestoredRead(current);
            setBase(current);
            setDraft(toDraft(overrides));
            setReason("");
            setError("");
            setHistoryNotice(
              "Historical overrides are staged against the freshly read revision and deployment defaults on this API. The preview uses these current defaults, not historical effective values. Nothing has been saved. Enter a new reason before saving.",
            );
          } finally {
            setRestoringHistory(false);
          }
        }}
      />
      <SettingsControls {...props} />
      {command && (
        <CommandDialog
          allowed={manage && !read.busy && !read.error && !restoringHistory}
          command={command}
          onFailure={props.onFailure}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            setCommand(undefined);
            setMessage(text);
            setAcknowledgements((v) => v + 1);
            read.refresh();
          }}
        />
      )}
    </>
  );
}
