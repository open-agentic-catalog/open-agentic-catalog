import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
import { documentationGuideLink } from "../api/session";
import * as checks from "./responses";
// Audit investigation captures submitted time/filter bounds for explicit reads and exports.
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { CopyReference } from "./CopyReference";
import { acknowledged, useInvestigation } from "./audit-handoff";
import type { AuditCriteria } from "./audit-handoff";
import { permits } from "../api/permissions";
import { ActionForm, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  confirmDiscard,
  useRead,
} from "./workspace";

type Filters = {
  actor: string;
  action: string;
  requestId: string;
  targetType: string;
  targetId: string;
  occurredFrom: string;
  occurredBefore: string;
};
const empty: Filters = {
  actor: "",
  action: "",
  requestId: "",
  targetType: "",
  targetId: "",
  occurredFrom: "",
  occurredBefore: "",
};
const labels = {
  actor: "Attributed principal",
  action: "Exact action",
  requestId: "Request ID",
  targetType: "Target type",
  targetId: "Target ID",
  occurredFrom: "Occurred from (inclusive, local time)",
  occurredBefore: "Occurred before (exclusive, local time)",
};

// Common starting points, not a second exhaustive audit vocabulary. Exact
// action entry remains available for less common and historical events.
export const auditActions = [
  ["resource.created", "Resource created"],
  ["resource.deleted", "Resource deleted"],
  ["catalog_listing.created", "Listing created"],
  ["catalog_listing.revised", "Listing revised"],
  ["listing_release.published", "Release published"],
  ["publication.activated", "Discovery snapshot activated"],
  ["identity.role_assignment.granted", "Named-template access granted"],
  ["identity.role_assignment.revoked", "Named-template access revoked"],
  ["authorization.assignment.granted", "Role or group access assigned"],
  ["authorization.assignment.revoked", "Role or group access revoked"],
  ["authorization.role.created", "Role definition created"],
  ["authorization.role.updated", "Role definition updated"],
  ["authorization.role.archived", "Role definition archived"],
  ["authorization.group.created", "Group created"],
  ["authorization.group.updated", "Group updated"],
  ["authorization.group.suspended", "Group suspended"],
  ["authorization.group.reactivated", "Group reactivated"],
  ["authorization.group.archived", "Group archived"],
  ["authorization.group_member.added", "Group member added"],
  ["authorization.group_member.revoked", "Group member revoked"],
  ["identity.membership.suspended", "Member suspended"],
  ["identity.membership.reactivated", "Member reactivated"],
  ["identity.membership.offboarded", "Member offboarded"],
  ["tenant.settings.changed", "Tenant settings changed"],
  ["feedback.created", "Feedback submitted"],
  ["feedback.edit", "Feedback edited"],
  ["feedback.address", "Feedback addressed"],
  ["feedback.reopen", "Feedback reopened"],
  ["feedback.withdraw", "Feedback withdrawn"],
  ["feedback.remove", "Feedback removed"],
  ["rating.submit", "Rating submitted"],
  ["rating.withdraw", "Rating withdrawn"],
  ["rating.remove", "Rating removed"],
  ["rating.permit", "New rating permitted"],
  ["rating.sharing", "Rating sharing changed"],
  ["audit.events_read", "Audit events read"],
  ["audit.exported", "Audit exported"],
] as const;

/** Keep the instant when staging an applied UTC bound in a local-time control. */
export function auditLocalTime(utc: string) {
  const instant = new Date(utc);
  return new Date(instant.getTime() - instant.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, -1);
}

/** Capture relative time bounds once so pagination and export reuse the same investigation. */
export function auditRange(draft: Filters, range: string, now = Date.now()) {
  if (range === "custom") return auditFilters(draft);
  const hours = { hour: 1, day: 24, week: 168 }[range];
  if (!hours) throw new Error("Choose a valid time range.");
  return auditFilters({
    ...draft,
    occurredFrom: new Date(now - hours * 3600000).toISOString(),
    occurredBefore: new Date(now).toISOString(),
  });
}

/** Validate paired target filters and convert entered time bounds to ordered UTC instants. */
export function auditFilters(draft: Filters) {
  if (!!draft.targetType !== !!draft.targetId)
    throw new Error("Supply both target type and target ID.");
  const filters = Object.fromEntries(
    Object.entries(draft).filter(([, value]) => !!value),
  ) as Partial<Filters>;
  if (filters.occurredFrom)
    filters.occurredFrom = new Date(filters.occurredFrom).toISOString();
  if (filters.occurredBefore)
    filters.occurredBefore = new Date(filters.occurredBefore).toISOString();
  if (
    filters.occurredFrom &&
    filters.occurredBefore &&
    filters.occurredFrom >= filters.occurredBefore
  )
    throw new Error("The end time must follow the start time.");
  return filters;
}

/** Start audited reads only after explicit filter submission and preserve the captured range. */
export default function Audit(props: WorkspaceProps) {
  return (
    <AuditInvestigation
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
function AuditInvestigation(props: WorkspaceProps) {
  const policyKey = JSON.stringify(props.access);
  const [policy, setPolicy] = useState(policyKey);
  const handoff = useInvestigation();
  const [draft, setDraft] = useState(empty);
  const [filters, setFilters] = useState<Partial<Filters>>();
  const [search, setSearch] = useState(0);
  const [error, setError] = useState("");
  const [range, setRange] = useState("day");
  const [customAction, setCustomAction] = useState(false);
  const [appliedInput, setAppliedInput] = useState("");
  const [appliedPolicy, setAppliedPolicy] = useState("");
  const [handoffNote, setHandoffNote] = useState("");
  // datetime-local cannot encode the second occurrence of a repeated DST hour.
  // Keep an untouched staged instant exact; editing that control uses local parsing.
  const [boundInstants, setBoundInstants] = useState<
    Partial<Pick<Filters, "occurredFrom" | "occurredBefore">>
  >({});
  if (policy !== policyKey) {
    setPolicy(policyKey);
    setDraft(empty);
    setRange("day");
    setCustomAction(false);
    setAppliedInput("");
    setAppliedPolicy("");
    setBoundInstants({});
    setHandoffNote("");
    setError("");
  }
  const stage = (
    next: AuditCriteria,
    note = "Filters staged. Choose Search to read matching audit events.",
  ) => {
    if (!confirmDiscard()) return;
    const bounds = { ...filters, ...next };
    const staged = { ...draft, ...next };
    staged.occurredFrom = bounds.occurredFrom
      ? auditLocalTime(bounds.occurredFrom)
      : "";
    staged.occurredBefore = bounds.occurredBefore
      ? auditLocalTime(bounds.occurredBefore)
      : "";
    setBoundInstants(
      Object.fromEntries(
        (["occurredFrom", "occurredBefore"] as const).flatMap((key) =>
          bounds[key] ? [[key, bounds[key]]] : [],
        ),
      ),
    );
    setDraft(staged);
    setRange("custom");
    setCustomAction(
      !!staged.action && !auditActions.some(([key]) => key === staged.action),
    );
    setHandoffNote(
      `${note} Initially staged UTC interval: ${bounds.occurredFrom || "unbounded"} to ${bounds.occurredBefore || "unbounded"} (exclusive). Adjust the local-time controls to change it.`,
    );
    setError("");
  };
  const pending = handoff?.pending;
  useEffect(() => {
    if (!pending) return;
    stage(pending.criteria, pending.note);
    handoff.clear();
  });
  return (
    <>
      <Heading
        title="Audit investigation"
        icon="history"
        description="Find out what changed, when it happened and who was responsible. Choose a time range and search; all other filters are optional. Viewing or exporting this history is also recorded."
        help={(() => {
          const help = documentationGuideLink(
            props.runtime.documentationUrl,
            "audit",
          );
          return help && <a href={help.href}>{help.label}</a>;
        })()}
      />
      {handoffNote && (
        <p role="status" className="notice">
          {handoffNote}
        </p>
      )}
      {!permits(props.access, "audit.read", "audit.read") &&
      !permits(props.access, "audit.export", "audit.export") ? (
        <p role="alert">
          This page requires audit.read or audit.export permission and the
          matching OAuth scope when scopes are enforced.{" "}
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </p>
      ) : (
        <>
          <form
            className="workspace-panel workspace-form"
            onSubmit={(e) => {
              e.preventDefault();
              setError("");
              try {
                const exactDraft = { ...draft };
                if (range === "custom")
                  for (const key of [
                    "occurredFrom",
                    "occurredBefore",
                  ] as const) {
                    const retained = boundInstants[key];
                    if (retained) exactDraft[key] = retained;
                  }
                const next = auditRange(exactDraft, range);
                if (!confirmDiscard()) return;
                setFilters(next);
                setAppliedInput(JSON.stringify({ draft, range }));
                setAppliedPolicy(policyKey);
                // Explicit Search always reloads, even with identical custom
                // filters, and starts again from the first results page.
                setSearch((value) => value + 1);
              } catch (e) {
                setError(e instanceof Error ? e.message : "Check the filters.");
              }
            }}
          >
            <Field
              label="Time range"
              hint="The time range is captured when you search and stays fixed while you browse pages or prepare an export. Search again to reload results."
            >
              <select value={range} onChange={(e) => setRange(e.target.value)}>
                <option value="hour">Last hour</option>
                <option value="day">Last 24 hours</option>
                <option value="week">Last 7 days</option>
                <option value="custom">Custom range</option>
              </select>
            </Field>
            {range === "custom" &&
              (["occurredFrom", "occurredBefore"] as const).map((key) => (
                <Field
                  key={key}
                  label={labels[key]}
                  hint="Optional for searching; both dates are needed to export. Times use your browser's local timezone."
                >
                  <input
                    type="datetime-local"
                    step="0.001"
                    value={draft[key]}
                    onChange={(e) => {
                      setDraft((v) => ({ ...v, [key]: e.target.value }));
                      setBoundInstants((saved) => {
                        const next = { ...saved };
                        delete next[key];
                        return next;
                      });
                    }}
                  />
                </Field>
              ))}
            <Field
              label="Activity"
              hint="Start with all activities, or narrow the results to a common action."
            >
              <select
                value={customAction ? "custom" : draft.action}
                onChange={(e) => {
                  setCustomAction(e.target.value === "custom");
                  setDraft((v) => ({
                    ...v,
                    action: e.target.value === "custom" ? "" : e.target.value,
                  }));
                }}
              >
                <option value="">All activities</option>
                {auditActions.map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
                <option value="custom">Other exact action...</option>
              </select>
            </Field>
            {customAction && (
              <Field
                label="Exact action"
                hint="Copy the action from an event's details, for example resource_version.created. This is an exact match, not a text search."
              >
                <input
                  value={draft.action}
                  maxLength={160}
                  onChange={(e) =>
                    setDraft((v) => ({ ...v, action: e.target.value }))
                  }
                />
              </Field>
            )}
            <details className="workspace-advanced">
              <summary>Advanced filters: person, request or object</summary>
              <p className="muted">
                Leave these blank unless you are investigating a specific event.
                Copy identifiers from event details; display names do not match
                here.
              </p>
              {(["actor", "requestId", "targetType", "targetId"] as const).map(
                (key) => (
                  <Field
                    key={key}
                    label={labels[key]}
                    hint={
                      key === "actor"
                        ? "Exact audit principal, not a login name or email. Copy the actor value from an event."
                        : key === "requestId"
                          ? "Copy X-Request-ID from an API response or an event to follow one request."
                          : key === "targetType"
                            ? "Object type from event details, for example resource. Also supply its target ID."
                            : "Exact object identifier from event details. Also supply its target type."
                    }
                  >
                    <input
                      type="text"
                      maxLength={
                        key === "requestId"
                          ? 128
                          : key === "targetType"
                            ? 160
                            : 512
                      }
                      value={draft[key]}
                      onChange={(e) =>
                        setDraft((v) => ({ ...v, [key]: e.target.value }))
                      }
                    />
                  </Field>
                ),
              )}
            </details>
            <div className="form-actions">
              <button type="submit" className="primary">
                {controlIcons["search"]}
                {permits(props.access, "audit.read", "audit.read")
                  ? "Search audit events"
                  : "Select export range"}
              </button>
            </div>
            {error && <p role="alert">{error}</p>}
          </form>
          {filters && appliedPolicy === policyKey && (
            <section
              className="workspace-panel"
              aria-label="Applied audit criteria"
            >
              <h2>Applied investigation criteria</h2>
              <p>
                These criteria produced the displayed results and any export
                prepared below. Time bounds stay fixed until a new search.
              </p>
              <Facts
                rows={(Object.keys(labels) as (keyof Filters)[])
                  .filter(
                    (key) =>
                      key === "occurredFrom" ||
                      key === "occurredBefore" ||
                      !!filters[key],
                  )
                  .map((key) => [
                    key === "occurredFrom"
                      ? "Occurred from (inclusive, UTC)"
                      : key === "occurredBefore"
                        ? "Occurred before (exclusive, UTC)"
                        : labels[key],
                    filters[key] || "Not restricted",
                  ])}
              />
              {appliedInput !== JSON.stringify({ draft, range }) && (
                <p role="status">
                  Unsubmitted filter changes. Results and prepared exports still
                  use the applied criteria.
                </p>
              )}
            </section>
          )}
        </>
      )}
      {filters && (
        <AuditResults
          key={search}
          {...props}
          filters={filters}
          evidenceAllowed={appliedPolicy === policyKey}
          stage={stage}
        />
      )}
    </>
  );
}

/** Page and export the submitted filter set, retaining explicit export confirmation. */
function AuditResults(
  props: WorkspaceProps & {
    filters: Partial<Filters>;
    evidenceAllowed: boolean;
    stage: (criteria: AuditCriteria) => void;
  },
) {
  const policyKey = JSON.stringify(props.access);
  const current = useRef({ policyKey, epoch: 0, access: props.access });
  current.current = {
    policyKey,
    epoch:
      current.current.epoch + Number(current.current.policyKey !== policyKey),
    access: props.access,
  };
  const [policy, setPolicy] = useState(policyKey);
  const [cursor, setCursor] = useState("");
  if (policy !== policyKey) {
    setPolicy(policyKey);
    setCursor("");
  }
  const page = useRead(
    JSON.stringify([cursor, policyKey, props.evidenceAllowed]),
    async (signal) =>
      props.evidenceAllowed && permits(props.access, "audit.read", "audit.read")
        ? result(
            await props.client.api.GET("/api/v1/audit-events", {
              params: {
                query: {
                  ...props.filters,
                  limit: 25,
                  ...(cursor ? { cursor: cursor } : {}),
                },
              },
              signal,
            }),
            checks.AuditEventPage,
          )
        : null,
    props.onFailure,
  );
  const [reason, setReason] = useState("");
  const [download, setDownload] = useState<{ bytes: Blob; epoch: number }>();
  const [href, setHref] = useState<{ value: string; epoch: number }>();
  useEffect(() => setDownload(undefined), [policyKey]);
  useEffect(() => {
    if (!download) return;
    const url = URL.createObjectURL(download.bytes);
    setHref({ value: url, epoch: download.epoch });
    return () => URL.revokeObjectURL(url);
  }, [download]);
  return (
    <>
      {!props.evidenceAllowed && (
        <p role="status" className="notice">
          Access changed. Search again to read audit events with current access.
          Your export draft and any prepared request keep their captured range;
          each send is checked again by the Catalog.
        </p>
      )}
      <details
        className="workspace-advanced"
        open={!permits(props.access, "audit.read", "audit.read")}
      >
        <summary>Export matching events</summary>
        <p>
          Export includes all events matching the applied filters and fixed time
          range, not only the current list page. The exact server-produced JSON
          is preserved.
        </p>
        <ActionForm
          title="Prepare audit export"
          icon="list"
          editable={permits(props.access, "audit.export", "audit.export")}
          description="Download the exact server-produced JSON bytes for these filters. Choose a bounded range ending in the past: the server rejects future end times. Narrow the range if it exceeds the export limit. Do not put secrets or document content in the reason."
          allowed={
            !!props.filters.occurredFrom &&
            !!props.filters.occurredBefore &&
            permits(props.access, "audit.export", "audit.export")
          }
          dirty={!!reason}
          onFailure={props.onFailure}
          prepare={() => {
            const body: components["schemas"]["AuditExportRequest"] = {
              ...props.filters,
              occurredFrom: props.filters.occurredFrom!,
              occurredBefore: props.filters.occurredBefore!,
              reason: reason.trim(),
            };
            return {
              summary: Object.entries(body)
                .filter(([, value]) => value !== undefined)
                .map(([label, value]) => ({
                  label,
                  value: String(value),
                })),
              run: async (key) => {
                const requestEpoch = current.current.epoch;
                const reply = await props.client.api.POST(
                  "/api/v1/audit-exports",
                  {
                    params: { header: { "Idempotency-Key": key } },
                    body,
                    parseAs: "blob",
                  },
                );
                const bytes = result(reply);
                const disclose =
                  current.current.epoch === requestEpoch &&
                  permits(
                    current.current.access,
                    "audit.export",
                    "audit.export",
                  );
                if (!disclose)
                  return "Audit export completed. Access changed and the returned file was cleared. Prepare a new export with current access to download it.";
                setDownload({ bytes, epoch: requestEpoch });
                return acknowledged(
                  "Audit export ready. Store the file securely.",
                  reply.response,
                  {
                    action: "audit.exported",
                    targetType: "audit_range",
                    targetId: props.access.tenantId,
                  },
                );
              },
            };
          }}
        >
          <Reason value={reason} onChange={setReason} />
        </ActionForm>
        {download?.epoch === current.current.epoch &&
          href?.epoch === current.current.epoch && (
            <a
              className="button primary"
              href={href.value}
              download="catalog-audit-export.json"
            >
              {controlIcons["download"]}
              Download exact audit export
            </a>
          )}
      </details>
      {props.evidenceAllowed && (
        <ReadState
          {...page}
          refresh={() =>
            page.error && cursor ? setCursor("") : page.refresh()
          }
        />
      )}
      {props.evidenceAllowed && page.data && (
        <section className="workspace-panel">
          <h2>Audit events</h2>
          <p>
            {page.data.items.length} events on this page. Export uses the entire
            applied range, not just this page.
          </p>
          {page.data.items.map((i) => (
            <article className="audit-entry" key={i.id}>
              <strong>
                {auditActions.find(([action]) => action === i.action)?.[1] ||
                  i.action.replace(/[._]/g, " ")}
              </strong>
              <p>
                <time dateTime={i.at}>{new Date(i.at).toLocaleString()}</time> /{" "}
                {i.targetType}
              </p>
              <p>
                Attributed to <code>{i.actor}</code> · Target{" "}
                <code>{i.targetId}</code>
              </p>
              {i.reason && (
                <p>
                  {Array.from(i.reason).slice(0, 180).join("")}
                  {Array.from(i.reason).length > 180 ? "…" : ""}
                </p>
              )}
              <details className="workspace-advanced">
                <summary>Details, identifiers and filter actions</summary>
                <CopyReference label="event ID" value={i.id} />
                <CopyReference label="attributed principal" value={i.actor} />
                <button
                  type="button"
                  onClick={() => props.stage({ actor: i.actor })}
                >
                  {controlIcons["filters"]}
                  Use principal as filter
                </button>
                <button
                  type="button"
                  onClick={() => props.stage({ action: i.action })}
                >
                  {controlIcons["filters"]}
                  Use action as filter
                </button>
                {i.requestId && (
                  <>
                    <CopyReference
                      label="event request ID"
                      value={i.requestId}
                    />
                    <button
                      type="button"
                      onClick={() => props.stage({ requestId: i.requestId! })}
                    >
                      {controlIcons["filters"]}
                      Use request as filter
                    </button>
                  </>
                )}
                <CopyReference label="event target type" value={i.targetType} />
                <CopyReference label="event target ID" value={i.targetId} />
                <button
                  type="button"
                  onClick={() =>
                    props.stage({
                      targetType: i.targetType,
                      targetId: i.targetId,
                    })
                  }
                >
                  {controlIcons["filters"]}
                  Use target as filter
                </button>
                <EvidenceDetails
                  title="Event details and identifiers"
                  value={i}
                />
              </details>
            </article>
          ))}
          {!page.data.items.length && (
            <p>
              No matching events. Try a longer time range, select All activities
              or clear the advanced filters.
            </p>
          )}
        </section>
      )}
      {props.evidenceAllowed && (
        <Pager
          label="Audit events pagination"
          cursor={cursor}
          nextCursor={page.data?.nextCursor}
          busy={page.busy}
          onPage={setCursor}
        />
      )}
    </>
  );
}
