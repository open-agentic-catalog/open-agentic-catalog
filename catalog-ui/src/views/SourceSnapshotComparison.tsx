// Historical comparison displays compact saved membership; raw evidence stays
// behind the observation screen's explicit, separately authorized read.
import { useState } from "react";
import type { components } from "../api/schema";
import { APIError, result } from "../api/client";
import type { WorkspaceProps } from "./workspace";
import { Field, Pager, ReadState, useRead } from "./workspace";
import { Facts } from "./Facts";
import { CopyReference } from "./CopyReference";
import { DateValue } from "./date-value";
import { EvidenceDetails } from "./authoring";
import { FederationSnapshot, FederationSnapshotComparison } from "./responses";
import { SourceSnapshotState } from "./operational-labels";

type Summary = components["schemas"]["FederationSnapshot"];
type Member = components["schemas"]["FederationSnapshotComparisonMember"];
type ComparisonReadResult =
  | {
      status: "ready";
      page: components["schemas"]["FederationSnapshotComparisonPage"];
    }
  | { status: "ineligible"; snapshot: Summary; reason: string };

/** Select an exact after snapshot without replacing either side with a source head. */
export function SourceSnapshotComparison(
  props: WorkspaceProps & { snapshot: Summary },
) {
  const [draft, setDraft] = useState(
    () => new URLSearchParams(location.search).get("compare") || "",
  );
  const [after, setAfter] = useState(draft.toLowerCase());
  const [includeUnchanged, setIncludeUnchanged] = useState(false);
  if (
    props.snapshot.state !== "complete" ||
    !props.snapshot.enumerationComplete
  )
    return (
      <section className="workspace-panel">
        <h2>Compare saved entries</h2>
        <p>
          Comparison requires two complete enumerations from the same source
          revision. Failed or incomplete attempts do not provide comparable
          membership.
        </p>
      </section>
    );
  return (
    <section className="workspace-panel" id="snapshot-comparison">
      <h2>Compare saved entries</h2>
      <p>
        Compare this snapshot with another complete snapshot from the same
        source and configuration revision. This reads saved data without
        contacting the source or changing current results.
      </p>
      <CopyReference label="Before snapshot ID" value={props.snapshot.id} />
      <form
        className="workspace-toolbar"
        aria-label="Select source snapshots"
        onSubmit={(event) => {
          event.preventDefault();
          setAfter(draft.toLowerCase());
        }}
      >
        <Field label="After snapshot ID">
          <input
            required
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
          />
        </Field>
        <button type="submit">Compare snapshots</button>
      </form>
      {after && (
        <ComparisonRead
          key={`${props.snapshot.id}/${after}/${includeUnchanged}`}
          {...props}
          after={after}
          includeUnchanged={includeUnchanged}
          onIncludeUnchanged={setIncludeUnchanged}
        />
      )}
    </section>
  );
}

/** Discard earlier pages during refresh/failure and fence late replies through useRead. */
function ComparisonRead(
  props: WorkspaceProps & {
    snapshot: Summary;
    after: string;
    includeUnchanged: boolean;
    onIncludeUnchanged: (value: boolean) => void;
  },
) {
  const [cursor, setCursor] = useState("");
  const read = useRead<ComparisonReadResult>(
    `${props.snapshot.id}/${props.after}/${props.includeUnchanged}/${cursor}`,
    async (signal) => {
      if (!cursor) {
        const selected =
          props.after === props.snapshot.id
            ? props.snapshot
            : result(
                await props.client.api.GET(
                  "/api/v1/federation/snapshots/{snapshotId}",
                  { params: { path: { snapshotId: props.after } }, signal },
                ),
                FederationSnapshot,
              );
        signal.throwIfAborted();
        if (selected.id !== props.after)
          throw new Error("After snapshot selection does not match.");
        let reason = "";
        if (selected.sourceId !== props.snapshot.sourceId)
          reason =
            "These snapshots belong to different sources. Their entries cannot be paired.";
        else if (selected.sourceRevision !== props.snapshot.sourceRevision)
          reason =
            "These snapshots use different source revisions. A changed scope or configuration cannot be interpreted as upstream deletion.";
        else if (selected.state !== "complete" || !selected.enumerationComplete)
          reason =
            "Comparable membership is unavailable for this after snapshot. Failed, incomplete or unfinished enumeration cannot be treated as an empty successful import.";
        if (reason) return { status: "ineligible", snapshot: selected, reason };
      }
      let page: components["schemas"]["FederationSnapshotComparisonPage"];
      try {
        page = result(
          await props.client.api.GET(
            "/api/v1/federation/snapshots/{snapshotId}/comparison",
            {
              params: {
                path: { snapshotId: props.snapshot.id },
                query: {
                  otherSnapshotId: props.after,
                  view: props.includeUnchanged ? "all" : "changes",
                  limit: 25,
                  ...(cursor ? { cursor } : {}),
                },
              },
              signal,
            },
          ),
          FederationSnapshotComparison,
        );
      } catch (error) {
        // The shared mutation-oriented 412 message does not describe immutable
        // membership. Keep safe local explanations and the original error codes.
        if (error instanceof APIError) {
          const explanations: Record<number, string> = {
            400: "Choose snapshots from the same source and source revision. If continuation was refused, reload the comparison from its first page.",
            412: "Comparison requires two complete enumerations. Failed, incomplete or over-budget membership cannot be compared.",
            503: "Saved membership is unavailable. No comparison is shown. Check service availability and retained evidence before retrying.",
          };
          const explanation = explanations[error.status];
          if (explanation) error.message = explanation;
        }
        throw error;
      }
      if (
        page.before.id !== props.snapshot.id ||
        page.after.id !== props.after ||
        page.sourceId !== props.snapshot.sourceId ||
        page.sourceRevision !== props.snapshot.sourceRevision
      )
        throw new Error("Comparison selection does not match.");
      signal.throwIfAborted();
      return { status: "ready", page };
    },
    props.onFailure,
  );
  const comparison = read.data?.status === "ready" ? read.data.page : undefined;
  const ineligible = read.data?.status === "ineligible" ? read.data : undefined;
  const refresh = () => (cursor && read.error ? setCursor("") : read.refresh());
  return (
    <>
      <p>
        Added and absent describe these two memberships. Absence alone does not
        prove upstream deletion. These differences are separate from
        synchronization counters and current observation freshness.
      </p>
      <Field label="Include unchanged entries">
        <input
          type="checkbox"
          checked={props.includeUnchanged}
          onChange={(event) => props.onIncludeUnchanged(event.target.checked)}
        />
      </Field>
      <button type="button" disabled={read.busy} onClick={refresh}>
        Reload comparison
      </button>
      <ReadState {...read} refresh={refresh} />
      <a
        href={`${props.runtime.basePath}/source-snapshots/${encodeURIComponent(props.after)}`}
      >
        Inspect after snapshot
      </a>
      {ineligible && (
        <section
          className="workspace-panel"
          aria-label="Ineligible after snapshot"
        >
          <p role="alert">{ineligible.reason}</p>
          <CopyReference
            label="After snapshot ID"
            value={ineligible.snapshot.id}
          />
          <CopyReference
            label="After source ID"
            value={ineligible.snapshot.sourceId}
          />
          <Facts
            rows={[
              ["Before source revision", props.snapshot.sourceRevision],
              ["After source revision", ineligible.snapshot.sourceRevision],
              [
                "After snapshot state",
                <SourceSnapshotState state={ineligible.snapshot.state} />,
              ],
              [
                "After enumeration complete",
                ineligible.snapshot.enumerationComplete ? "Yes" : "No",
              ],
            ]}
          />
        </section>
      )}
      {comparison && (
        <>
          <Facts
            rows={[
              ["Source revision", comparison.sourceRevision],
              ["Before entries", comparison.before.memberCount],
              [
                "Before started",
                <DateValue value={comparison.before.startedAt} />,
              ],
              ["After entries", comparison.after.memberCount],
              [
                "After started",
                <DateValue value={comparison.after.startedAt} />,
              ],
              [
                "After withdrawal authority",
                comparison.after.withdrawalAuthority ? "Yes" : "No",
              ],
            ]}
          />
          <CopyReference
            label="After snapshot ID"
            value={comparison.after.id}
          />
          {comparison.items.map((item) => (
            <article className="workspace-panel" key={item.position}>
              <h3>
                {
                  {
                    added: "Added",
                    absent: "Absent",
                    changed: "Changed",
                    unchanged: "Unchanged",
                    multiple: "Multiple occurrences",
                  }[item.change]
                }
                :{" "}
                {item.before?.identifier ||
                  item.after?.identifier ||
                  item.sourceKey}
              </h3>
              {item.change === "multiple" && (
                <p>
                  This key occurs {item.beforeCount} times before and{" "}
                  {item.afterCount} times after. Each saved occurrence is shown
                  separately; no match or preferred entry is inferred.
                </p>
              )}
              {item.before && (
                <ComparisonMember
                  member={item.before}
                  label="Before"
                  basePath={props.runtime.basePath}
                />
              )}
              {item.after && (
                <ComparisonMember
                  member={item.after}
                  label="After"
                  basePath={props.runtime.basePath}
                />
              )}
              {item.before && item.after && (
                <Facts
                  rows={[
                    [
                      "Exact entry",
                      item.before.occurrence.exactEntryDigest ===
                      item.after.occurrence.exactEntryDigest
                        ? "Unchanged"
                        : "Changed",
                    ],
                    [
                      "Normalization inputs",
                      !item.before.occurrence.normalizationInputDigest &&
                      !item.after.occurrence.normalizationInputDigest
                        ? "None recorded"
                        : item.before.occurrence.normalizationInputDigest ===
                            item.after.occurrence.normalizationInputDigest
                          ? "Unchanged"
                          : "Changed",
                    ],
                    [
                      "Normalized entry",
                      !item.before.occurrence.normalizedEntryDigest &&
                      !item.after.occurrence.normalizedEntryDigest
                        ? "Not retained"
                        : item.before.occurrence.normalizedEntryDigest ===
                            item.after.occurrence.normalizedEntryDigest
                          ? "Unchanged"
                          : "Changed",
                    ],
                    [
                      "Observation",
                      item.before.observationId === item.after.observationId
                        ? "Reused"
                        : "Different retained observation",
                    ],
                  ]}
                />
              )}
            </article>
          ))}
          {!comparison.items.length && (
            <p>
              {props.includeUnchanged
                ? "Both selected memberships are empty."
                : "No entry differences between these snapshots."}
            </p>
          )}
          <Pager
            label="Source comparison pages"
            busy={read.busy}
            cursor={cursor}
            nextCursor={comparison.nextCursor}
            onPage={setCursor}
          />
        </>
      )}
      <p>
        Comparison supports up to 10,000 occurrences per snapshot and 32 MiB of
        projected text across both inputs. Inconsistent or over-budget evidence
        is refused without a partial result.
      </p>
    </>
  );
}

/** Keep exact occurrence references visible without prefetching audited raw bytes. */
function ComparisonMember({
  member,
  label,
  basePath,
}: {
  member: Member;
  label: string;
  basePath: string;
}) {
  return (
    <section>
      <h4>{label}</h4>
      <Facts
        rows={[
          [
            "Version",
            member.versionPresent
              ? member.version || "(explicitly empty)"
              : "Not supplied",
          ],
          ["Kind", member.kind || "Not available"],
        ]}
      />
      <a href={`${basePath}/observations/${member.observationId}`}>
        Inspect {label.toLowerCase()} observation and occurrences
      </a>
      <CopyReference
        label={`${label} observation ID`}
        value={member.observationId}
      />
      <CopyReference
        label={`${label} identity digest`}
        value={member.identityDigest}
      />
      <EvidenceDetails
        title={`Exact ${label.toLowerCase()} occurrence`}
        value={member.occurrence}
      />
    </section>
  );
}
