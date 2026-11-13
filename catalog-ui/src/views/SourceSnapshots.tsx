import { SourceSnapshotState } from "./operational-labels";
import { Facts } from "./Facts";
import * as checks from "./responses";
// Synchronization history keeps bounded snapshot summaries separate from selected retained detail.
import { useState } from "react";
import { result } from "../api/client";
import { permits } from "../api/permissions";
import type { WorkspaceProps } from "./workspace";
import { EvidenceDetails } from "./authoring";
import { Field, Heading, Pager } from "./workspace";
import { CurrentRead, useCurrentRead } from "./current-read";
import { CopyReference } from "./CopyReference";
import { ExactReference } from "./ExactReference";
import { DateValue } from "./date-value";
import { SourceSnapshotComparison } from "./SourceSnapshotComparison";

/** Mounted by the authorized source detail screen. Summaries do not load
 * their retained retrieval evidence; the detail route below owns that read. */
export function SourceSnapshots(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const [before, setBefore] = useState<{
    id: string;
    sourceId: string;
    revision: number;
  }>();
  const [after, setAfter] = useState<typeof before>();
  const page = useCurrentRead(
    props.id + cursor,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/federation/sources/{sourceId}/snapshots",
          {
            params: {
              path: { sourceId: props.id },
              query: { limit: 25, ...(cursor ? { cursor: cursor } : {}) },
            },
            signal,
          },
        ),
        checks.FederationSnapshotPage,
      ),
    props.onFailure,
  );
  return (
    <section className="workspace-panel">
      <h2>Synchronization snapshots</h2>
      <p>
        Incomplete or failed snapshots cannot withdraw the last complete source
        view.
      </p>
      <p>
        Select two complete snapshots of one source revision to compare saved
        entries. Selections remain available while paging this history.
      </p>
      {before?.sourceId === props.id && (
        <CopyReference label="Selected before snapshot" value={before.id} />
      )}
      {after?.sourceId === props.id && (
        <CopyReference label="Selected after snapshot" value={after.id} />
      )}
      {before?.sourceId === props.id &&
        after?.sourceId === props.id &&
        (before.revision === after.revision ? (
          <a
            href={`${props.runtime.basePath}/source-snapshots/${before.id}?${new URLSearchParams({ compare: after.id })}#snapshot-comparison`}
          >
            Compare selected snapshots
          </a>
        ) : (
          <p role="status">Choose snapshots from the same source revision.</p>
        ))}
      <ExactReference
        basePath={props.runtime.basePath}
        path="source-snapshots"
        label="Snapshot"
      />
      <CurrentRead
        read={page}
        label="synchronization history"
        reload="Refresh snapshots"
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      {page.data?.items.map((item) => (
        <article className="workspace-panel" key={item.id}>
          <h3>
            <a href={`${props.runtime.basePath}/source-snapshots/${item.id}`}>
              <DateValue value={item.startedAt} />
            </a>
          </h3>
          <Facts
            rows={[
              ["Snapshot state", <SourceSnapshotState state={item.state} />],
              ["Source revision", item.sourceRevision],
              ["Entries", item.memberCount],
              [
                "All entries enumerated",
                item.enumerationComplete ? "Yes" : "No",
              ],
              ["Withdrawal authority", item.withdrawalAuthority ? "Yes" : "No"],
            ]}
          />
          <CopyReference label="Snapshot ID" value={item.id} />
          {item.state === "complete" && item.enumerationComplete && (
            <div className="workspace-toolbar">
              <Field label="Before">
                <input
                  type="radio"
                  aria-label={`Before ${item.id}`}
                  name="comparison-before"
                  checked={
                    before?.sourceId === props.id && before.id === item.id
                  }
                  onChange={() =>
                    setBefore({
                      id: item.id,
                      sourceId: props.id,
                      revision: item.sourceRevision,
                    })
                  }
                />
              </Field>
              <Field label="After">
                <input
                  type="radio"
                  aria-label={`After ${item.id}`}
                  name="comparison-after"
                  checked={after?.sourceId === props.id && after.id === item.id}
                  onChange={() =>
                    setAfter({
                      id: item.id,
                      sourceId: props.id,
                      revision: item.sourceRevision,
                    })
                  }
                />
              </Field>
            </div>
          )}
        </article>
      ))}
      {page.data && !page.data.items.length && <p>No snapshots.</p>}
      <Pager
        label="Synchronization snapshot pages"
        busy={page.busy}
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        onPage={setCursor}
      />
    </section>
  );
}
/** Check source-administration access before loading snapshot detail. */
export function SourceSnapshot(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Source snapshot"
        icon="globe"
        description="Inspect a source synchronization result and the evidence collected."
      />
      {permits(props.access, "source.read", "sources.read") ? (
        <SnapshotRead {...props} />
      ) : (
        <p role="alert">
          The source.read permission is required. When scopes are enforced,
          sources.read is also required.
        </p>
      )}
    </>
  );
}
/** Fetch one retained synchronization snapshot after explicit detail navigation. */
function SnapshotRead(props: WorkspaceProps) {
  const read = useCurrentRead(
    props.id,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/federation/snapshots/{snapshotId}",
          { params: { path: { snapshotId: props.id } }, signal },
        ),
        checks.FederationSnapshot,
      ),
    props.onFailure,
  );
  return (
    <>
      <CurrentRead
        read={read}
        label="source snapshot"
        reload="Reload snapshot"
      />
      {read.data && (
        <>
          <a href={`${props.runtime.basePath}/sources/${read.data.sourceId}`}>
            Source
          </a>
          <CopyReference label="Snapshot ID" value={read.data.id} />
          <CopyReference label="Source ID" value={read.data.sourceId} />
          <p>
            Exact retained evidence can remain after Source removal. Current
            configuration and source-restricted history may be unavailable; this
            does not mean the evidence was deleted. Open an exact retained
            Observation ID or clear the source restriction to investigate
            independently.
          </p>
          <section className="workspace-panel">
            <h2>
              Snapshot: <SourceSnapshotState state={read.data.state} />
            </h2>
            <Facts
              rows={[
                ["Started", <DateValue value={read.data.startedAt} />],
                [
                  "Finished",
                  read.data.completedAt ? (
                    <DateValue value={read.data.completedAt} />
                  ) : (
                    "Not recorded"
                  ),
                ],
                ["Entry count", read.data.memberCount],
                ["Source revision", read.data.sourceRevision],
                ["Added", read.data.added],
                ["Updated", read.data.updated],
                ["Marked withdrawn", read.data.withdrawn],
                [
                  "All entries enumerated",
                  read.data.enumerationComplete ? "Yes" : "No",
                ],
                [
                  "Withdrawal authority",
                  read.data.withdrawalAuthority ? "Yes" : "No",
                ],
              ]}
            />
            <p>
              {read.data.state !== "complete"
                ? "This snapshot cannot replace the last complete source view. Inspect retained retrievals and findings before retrying."
                : read.data.reconcileCurrentView
                  ? "This Registry List snapshot updates the current indexed view. Missing entries leave current results without becoming authoritative upstream withdrawals."
                  : "This is a retained complete snapshot. Its withdrawal authority determines whether absent entries can be marked withdrawn; it does not approve or adopt their content."}
            </p>
            <details>
              <summary>How snapshot withdrawal works</summary>
              <p>
                A complete snapshot with withdrawal authority can mark earlier
                entries as withdrawn when they are absent from it. Registry List
                snapshots can remove absent entries from current results, but
                cannot establish upstream withdrawal. Failed or incomplete
                synchronization preserves the last complete source view.
              </p>
            </details>
            <a
              href={`${props.runtime.basePath}/observations?source=${read.data.sourceId}`}
            >
              Browse source observations
            </a>
          </section>
          <EvidenceDetails title="Snapshot and retrievals" value={read.data} />
          <SourceSnapshotComparison
            key={read.data.id}
            {...props}
            snapshot={read.data}
          />
        </>
      )}
    </>
  );
}
