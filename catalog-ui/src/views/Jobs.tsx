import { commandSummary } from "./workspace";
import {
  StatusLabel,
  WorkState,
  jobKindLabel,
  jobKinds,
  stateLabel,
} from "./operational-labels";

import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
import { DateValue } from "./date-value";
import { CurrentRead, useCurrentRead } from "./current-read";
import { ScrollableTable } from "./ScrollableTable";
import * as checks from "./responses";
// Job inspection uses bounded reads and explicit refresh, cancel and retry controls.
import { useEffect, useId, useState } from "react";
import { compareTableText, usePageSort } from "./table-sort";
import { object, text } from "../api/values";
import {
  jobFilters,
  jobQueueURL,
  jobStates,
  type JobFilters,
} from "./job-state";
import { JSONBlock } from "./CodeBlock";
import { result } from "../api/client";
import { acknowledged } from "./audit-handoff";
import type { components } from "../api/schema";
import {
  permits,
  canReadJobs,
  jobActions,
  jobActionReason,
} from "../api/permissions";

import { canReadTrust, trustID, trustResultHref } from "./trust-result";
import {
  CommandDialog,
  Heading,
  Pager,
  ReadState,
  Reason,
  Field,
  useRead,
  useUnsaved,
  confirmDiscard,
} from "./workspace";
import type { Command, WorkspaceProps as Props } from "./workspace";

type Job = components["schemas"]["Job"];
const knownKind = (kind: string) => jobKinds.some((value) => value === kind);

/** Select a job list or detail view under the caller's job-read access. */
export default function Jobs(props: Props) {
  return (
    <>
      <Heading
        title="Jobs"
        icon="list"
        description="Track background jobs and inspect their results."
      />
      {!permits(props.access, "job.read", "catalog.read") ? (
        <p role="alert" className="notice error">
          Job status requires job.read and catalog.read when scopes are
          enforced.
        </p>
      ) : !canReadJobs(props.access) ? (
        <p role="alert" className="notice error">
          Your access does not include source, publication or search-indexing
          jobs. Check <a href={`${props.runtime.basePath}/access`}>My access</a>{" "}
          for your permissions.
        </p>
      ) : props.id ? (
        <JobDetail key={props.id} {...props} />
      ) : (
        <JobList {...props} />
      )}
    </>
  );
}

/** Page newest-first job summaries with manual refresh and opaque cursors. */
function JobList(props: Props) {
  const sortHint = useId();
  const [filters, setFilters] = useState(() => jobFilters(location.search));
  const [draft, setDraft] = useState(filters);
  const [customKind, setCustomKind] = useState(
    () => !!filters.kind && !knownKind(filters.kind),
  );
  const [jobId, setJobId] = useState("");
  useEffect(() => {
    const moved = () => {
      if (location.pathname !== `${props.runtime.basePath}/jobs`) return;
      const next = jobFilters(location.search);
      setFilters(next);
      setDraft(next);
      setCustomKind(!!next.kind && !knownKind(next.kind));
      setCursor("");
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, [props.runtime.basePath]);
  const [cursor, setCursor] = useState("");
  const page = useRead(
    JSON.stringify([filters, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/jobs", {
          params: {
            query: {
              limit: 25,
              ...(filters.kind ? { kind: filters.kind } : {}),
              ...(filters.state
                ? { state: filters.state as keyof typeof jobStates }
                : {}),
              ...(filters.parentJobId
                ? { parentJobId: filters.parentJobId }
                : {}),
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.JobPage,
      ),
    props.onFailure,
  );
  const sorted = usePageSort(page.data?.items, {
    kind: (a, b) =>
      compareTableText(jobKindLabel(a.kind), jobKindLabel(b.kind)),
    state: (a, b) => compareTableText(stateLabel(a.state), stateLabel(b.state)),
    progress: (a, b) => a.progress - b.progress,
    attempts: (a, b) => a.attempt - b.attempt,
    updated: (a, b) => Date.parse(a.updatedAt) - Date.parse(b.updatedAt),
  });
  const apply = (selected: JobFilters) => {
    const next = {
      kind: selected.kind.trim(),
      state: selected.state,
      parentJobId: selected.parentJobId.trim(),
    };
    const url = jobQueueURL(props.runtime.basePath, next);
    if (location.pathname + location.search !== url)
      history.pushState(null, "", url);
    setFilters(next);
    setDraft(next);
    setCustomKind(!!next.kind && !knownKind(next.kind));
    setCursor("");
  };
  return (
    <>
      <form
        className="workspace-toolbar"
        aria-label="Job filters"
        onSubmit={(event) => {
          event.preventDefault();
          apply(draft);
        }}
      >
        <Field label="Job type">
          <select
            value={customKind ? "custom" : draft.kind}
            onChange={(event) => {
              const custom = event.target.value === "custom";
              setCustomKind(custom);
              setDraft({ ...draft, kind: custom ? "" : event.target.value });
            }}
          >
            <option value="">All job types</option>
            {jobKinds.map((kind) => (
              <option value={kind} key={kind}>
                {jobKindLabel(kind)} ({kind})
              </option>
            ))}
            <option value="custom">Enter an exact type code</option>
          </select>
        </Field>
        {customKind && (
          <Field
            label="Exact job type code"
            hint="Enter the complete type code. Unknown and future job types can be searched by their exact code."
          >
            <input
              maxLength={200}
              pattern=".{0,100}"
              value={draft.kind}
              onChange={(event) =>
                setDraft({ ...draft, kind: event.target.value })
              }
            />
          </Field>
        )}
        <Field label="Job state">
          <select
            value={draft.state}
            onChange={(event) =>
              setDraft({ ...draft, state: event.target.value })
            }
          >
            <option value="">All states</option>
            {draft.state && !Object.hasOwn(jobStates, draft.state) && (
              <option value={draft.state}>{draft.state}</option>
            )}
            {Object.keys(jobStates).map((state) => (
              <option key={state} value={state}>
                {stateLabel(state)}
              </option>
            ))}
          </select>
        </Field>
        <Field
          label="Retries of job ID"
          hint="Show retry jobs created from this job. Leave blank to show all jobs."
        >
          <input
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={draft.parentJobId}
            onChange={(event) =>
              setDraft({ ...draft, parentJobId: event.target.value })
            }
          />
        </Field>
        <div className="workspace-actions">
          <button disabled={page.busy}>Apply Job filters</button>
          <button
            type="button"
            disabled={page.busy}
            onClick={() => apply({ kind: "", state: "", parentJobId: "" })}
          >
            Clear Job filters
          </button>
        </div>
      </form>
      <form
        className="workspace-toolbar workspace-lookup"
        aria-label="Open exact Job"
        method="get"
        action={`${props.runtime.basePath}/jobs/${encodeURIComponent(jobId.trim().toLowerCase())}`}
      >
        <Field
          label="Exact Job ID"
          hint="Open one authorized retained Job. Missing, foreign and expired references remain unavailable."
        >
          <input
            required
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={jobId}
            onChange={(event) => setJobId(event.target.value)}
          />
        </Field>
        <div className="workspace-actions">
          <button disabled={!jobId.trim()}>Open Job</button>
        </div>
      </form>
      <div className="workspace-toolbar">
        <button
          disabled={page.busy}
          onClick={() =>
            page.error && cursor ? setCursor("") : page.refresh()
          }
        >
          {controlIcons["refresh"]} Refresh
        </button>
        <span className="muted">
          {sorted.selection ? "Sorted on this page." : "Newest first."} Refresh
          is manual. Succeeded, failed, cancelled and dead-letter Jobs can
          expire under the deployment's retention policy.
        </span>
      </div>
      <ReadState
        {...page}
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      {page.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Job inventory">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Job type / ID", "kind")}
                  {sorted.heading("State", "state")}
                  {sorted.heading("Progress", "progress")}
                  {sorted.heading("Attempts", "attempts")}
                  {sorted.heading("Updated", "updated")}
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <a href={`${props.runtime.basePath}/jobs/${item.id}`}>
                        {jobKindLabel(item.kind)}
                      </a>
                      <br />
                      <code>{item.id}</code>
                    </td>
                    <td>
                      <WorkState state={item.state} />
                    </td>
                    <td>{item.progress}%</td>
                    <td>
                      {item.attempt} / {item.attemptLimit}
                    </td>
                    <td>
                      <DateValue value={item.updatedAt} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && (
              <p className="workspace-empty">
                {filters.kind || filters.state || filters.parentJobId
                  ? "No jobs match these filters."
                  : "No jobs on this page."}
              </p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        label="Job pages"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Load one job for inspection and explicit operational actions. */
function JobDetail(props: Props) {
  const [replacement, setReplacement] = useState("");
  const jobId = props.id.toLowerCase();
  const detail = useCurrentRead(
    jobId,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/jobs/{jobId}", {
          params: { path: { jobId } },
          signal,
        }),
        (value) =>
          checks.JobFor(value, props.access.tenantId) &&
          object(value) &&
          value.id === jobId,
      ),
    props.onFailure,
  );
  return (
    <>
      <a className="back-link" href={`${props.runtime.basePath}/jobs`}>
        Back to jobs
      </a>
      <CurrentRead
        read={detail}
        label="Job"
        reload="Reload job"
        refresh={() => {
          if (confirmDiscard()) detail.refresh();
        }}
      >
        {replacement && (
          <>
            {" "}
            <a href={`${props.runtime.basePath}/jobs/${replacement}`}>
              Open retry job
            </a>
          </>
        )}
      </CurrentRead>
      {detail.data && (
        <JobRecord
          key={detail.data.id + detail.epoch}
          {...props}
          job={detail.data}
          onAcknowledged={detail.acknowledge}
          onReplacement={setReplacement}
          busy={detail.busy || !!detail.error}
        />
      )}
    </>
  );
}

/** Show retained job state and prepare only eligible cancel/retry commands. */
function JobRecord({
  job,
  onAcknowledged,
  onReplacement,
  busy,
  ...props
}: Props & {
  job: Job;
  onAcknowledged: (message: string) => void;
  onReplacement: (id: string) => void;
  busy: boolean;
}) {
  const [reason, setReason] = useState("");
  const [command, setCommand] = useState<
    Command & { action: "cancel" | "retry" }
  >();
  const [done, setDone] = useState(false);
  const actions = jobActions(props.access, job);
  const sourceContext = new URLSearchParams(location.search).get("sourceId");
  useUnsaved(!!reason && !done);
  const prepare = (retry: boolean) => {
    const action = retry ? "retry" : "cancel";
    const body = { reason: reason.trim() };
    if (
      busy ||
      done ||
      !actions[action] ||
      !body.reason ||
      [...reason].length > 2000
    )
      return;
    setCommand({
      action,
      intent: "warning",
      icon: retry ? "refresh" : "close",
      title: retry ? "Retry job" : "Cancel job",
      description: retry
        ? `Create a new ${job.kind} Job linked to this one. Acceptance does not mean it has completed. Finished Job records can expire under the deployment's retention policy. Reason: ${body.reason}`
        : `Request cancellation of ${job.kind}. Running work stops cooperatively; completed side effects are not undone. Reason: ${body.reason}`,
      summary: commandSummary([
        ["Action", retry ? "Retry job" : "Cancel job"] as const,
        ["Job ID", job.id] as const,
        ["Kind", job.kind] as const,
        ["State", job.state] as const,
        ["Reason", body.reason] as const,
      ]),
      run: async (key) => {
        const params = {
          path: { jobId: job.id },
          header: { "Idempotency-Key": key },
        };
        const reply = retry
          ? await props.client.api.POST("/api/v1/jobs/{jobId}:retry", {
              params,
              body,
            })
          : await props.client.api.POST("/api/v1/jobs/{jobId}:cancel", {
              params,
              body,
            });
        const next = result(reply, (value) =>
          retry
            ? checks.JobFor(value, props.access.tenantId, job.kind) &&
              object(value) &&
              text(value.id) &&
              value.id.toLowerCase() !== job.id.toLowerCase() &&
              value.parentJobId === job.id
            : checks.JobFor(value, props.access.tenantId, job.kind) &&
              object(value) &&
              value.id === job.id,
        );
        if (retry) onReplacement(next.id);
        const message = retry
          ? `Retry accepted. New job state: ${stateLabel(next.state)}.`
          : `Cancellation response: ${stateLabel(next.state)}${next.cancellationRequested ? "; cancellation requested" : ""}. Reload to check current state.`;
        return acknowledged(message, reply.response, {
          action: retry ? "job.retried" : "job.cancellation_requested",
          targetType: "job",
          targetId: job.id,
        });
      },
    });
  };
  return (
    <section className="workspace-panel">
      <h2>{jobKindLabel(job.kind)}</h2>
      {job.kind === "source.trust_verify" && canReadTrust(props.access) && (
        <p>
          <a
            href={
              trustID(sourceContext)
                ? trustResultHref(props.runtime.basePath, sourceContext, job.id)
                : `${props.runtime.basePath}/trust-assessments/${job.id}`
            }
          >
            Open private ANS assessment
          </a>
          .{" "}
          {trustID(sourceContext)
            ? "The Source ID comes from the navigation link; the private read still checks the exact Source and Job."
            : "This generic Job has no Source context. Supply the original request's exact Source ID; its private payload is not exposed here."}{" "}
          Job success does not mean the registration passed.
        </p>
      )}
      <Facts
        rows={[
          [
            "Job type",
            <>
              {jobKindLabel(job.kind)} <code>{job.kind}</code>
            </>,
          ],
          ["Job ID", <code>{job.id}</code>],
          ["State", <WorkState state={job.state} />],
          [
            "Progress",
            <>
              <progress
                aria-label="Reported Job progress"
                max={100}
                value={job.progress}
              />{" "}
              {job.progress}%
            </>,
          ],
          [
            "Attempts",
            <>
              {job.attempt} / {job.attemptLimit}
            </>,
          ],
          ["Priority", job.priority],
          ["Eligible to run after", <DateValue value={job.notBefore} />],
          ["Created", <DateValue value={job.createdAt} />],
          ["Updated", <DateValue value={job.updatedAt} />],
          [
            "Cancellation requested",
            <StatusLabel
              state={job.cancellationRequested ? "Yes" : "No"}
              tone={
                job.cancellationRequested &&
                ["queued", "running", "waiting"].includes(job.state)
                  ? "warning"
                  : "neutral"
              }
            />,
          ],
          ...(job.parentJobId
            ? ([
                [
                  "Retried from job",
                  <a href={`${props.runtime.basePath}/jobs/${job.parentJobId}`}>
                    {job.parentJobId}
                  </a>,
                ],
              ] as const)
            : []),
          ...(job.failureClass
            ? ([["Failure class", job.failureClass]] as const)
            : []),
          ...(job.safeError ? ([["Error", job.safeError]] as const) : []),
        ]}
      />
      <p>{jobStates[job.state]}</p>
      {job.cancellationRequested && (
        <p>
          Cancellation was requested. Check the recorded state above; completed
          side effects are not undone.
        </p>
      )}
      <p>
        Progress is reported by the worker. Check the job state to confirm
        completion. Attempts can exceed the usual limit after interrupted work
        is reclaimed. Eligible to run after is the earliest the job can run; its
        actual start may be later.
      </p>
      <p>
        The API rechecks current target and policy eligibility for every
        command.
      </p>
      {!actions.cancel && (
        <p>
          Cancel unavailable: {jobActionReason(props.access, job, "cancel")}
        </p>
      )}
      {!actions.retry && (
        <p>Retry unavailable: {jobActionReason(props.access, job, "retry")}</p>
      )}
      {job.result && (
        <>
          <h3>Result</h3>
          {job.result.type === "semantic_index" && (
            <div>
              <p>
                Recorded indexing generation:{" "}
                <code>{job.result.generationId}</code>.
              </p>
              <p>
                Accounted inputs: {job.result.members}. This includes reused
                vectors and inputs recorded with failures. It is not a count of
                newly successful embeddings, a current active generation or a
                model-quality result.
              </p>
            </div>
          )}
          {job.result.type === "source_snapshot" && (
            <div>
              <p>
                Source snapshot: <strong>{stateLabel(job.result.state)}</strong>
                .{" "}
                {job.result.state === "complete"
                  ? "This synchronization produced a complete source snapshot."
                  : "This synchronization did not replace the last complete source snapshot."}
              </p>
              <p>
                Source: <code>{job.result.sourceId}</code>. Snapshot:{" "}
                <code>{job.result.snapshotId}</code>.
              </p>
              {permits(props.access, "source.read", "sources.read") && (
                <p>
                  <a
                    href={`${props.runtime.basePath}/sources/${job.result.sourceId}`}
                  >
                    Open resulting source
                  </a>
                  {" / "}
                  <a
                    href={`${props.runtime.basePath}/source-snapshots/${job.result.snapshotId}`}
                  >
                    Inspect source snapshot
                  </a>
                </p>
              )}
            </div>
          )}
          {job.result.type === "publication_snapshot" && (
            <div>
              <p>
                Publication snapshot:{" "}
                <strong>{stateLabel(job.result.state)}</strong>.{" "}
                {job.result.state === "active"
                  ? "The Job result recorded an active snapshot; inspect its current state."
                  : "A generated or retired snapshot is not proof of current discovery activation."}
              </p>
              <p>
                Host: <code>{job.result.host}</code>. Generation:{" "}
                {job.result.generation}. Snapshot:{" "}
                <code>{job.result.snapshotId}</code>.
              </p>
              {permits(props.access, "publication.read", "catalog.read") && (
                <p>
                  <a
                    href={`${props.runtime.basePath}/publication/${job.result.snapshotId}?host=${encodeURIComponent(job.result.host)}`}
                  >
                    Inspect resulting publication snapshot
                  </a>
                </p>
              )}
            </div>
          )}
          <JSONBlock value={job.result} />
        </>
      )}
      {(actions.cancel || actions.retry || !!reason || !!command) && !done && (
        <fieldset
          className="workspace-form workspace-fields"
          disabled={busy || (!actions.cancel && !actions.retry)}
          aria-label="Job command fields"
        >
          <Reason value={reason} onChange={setReason} />
          <div className="form-actions">
            {actions.cancel && (
              <button
                disabled={
                  !reason.trim() || [...reason].length > 2000 || !!command
                }
                className="warning"
                onClick={() => prepare(false)}
              >
                {controlIcons["close"]} Cancel job
              </button>
            )}
            {actions.retry && (
              <button
                disabled={
                  !reason.trim() || [...reason].length > 2000 || !!command
                }
                className="warning"
                onClick={() => prepare(true)}
              >
                {controlIcons["refresh"]} Retry job
              </button>
            )}
          </div>
        </fieldset>
      )}
      {command && (
        <CommandDialog
          command={command}
          allowed={!busy && !done && actions[command.action]}
          onClose={() => setCommand(undefined)}
          onFailure={props.onFailure}
          onDone={(text) => {
            setDone(true);
            onAcknowledged(text);
            setCommand(undefined);
          }}
        />
      )}
    </section>
  );
}
