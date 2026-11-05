import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
import { useState } from "react";
import { result } from "../api/client";
import { object } from "../api/values";
import {
  Field,
  Pager,
  ReadState,
  useRead,
  type WorkspaceProps,
} from "./workspace";
import { JSONBlock } from "./CodeBlock";
import {
  metadataChanges,
  MetadataChanges,
  ChangeLabel,
} from "./RevisionComparison";
import { snapshotChanges, type SnapshotChange } from "./snapshot-comparison";
import { ActiveSnapshot } from "./ActiveSnapshot";
import * as checks from "./responses";

/** Read only the two explicitly selected retained documents; never follow entry URLs. */
export function SnapshotComparison(
  props: WorkspaceProps & { host: string; snapshotId: string },
) {
  const [open, setOpen] = useState(false);
  const [baseline, setBaseline] = useState("");
  const [selection, setSelection] = useState<{
    before: string;
    after: string;
    read: number;
  }>();
  return (
    <section className="workspace-panel" aria-label="Snapshot comparison">
      <h2>Compare retained snapshots</h2>
      <p>
        Choose an exact baseline for this host and audience. Comparison reads
        retained metadata; it does not predict eligibility, activate content or
        contact references.
      </p>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        {open ? "Close snapshot choices" : "Choose snapshot baseline"}
      </button>
      {open && (
        <>
          <ActiveSnapshot
            {...props}
            baselineId={baseline.trim().toLowerCase()}
            onChoose={setBaseline}
          />
          <form
            onSubmit={(event) => {
              event.preventDefault();
              setSelection({
                before: baseline.trim().toLowerCase(),
                after: props.snapshotId,
                read: (selection?.read ?? 0) + 1,
              });
            }}
          >
            <Field
              label="Baseline Snapshot ID"
              hint="Use the observed active reference or copy an exact retained reference from host history."
            >
              <input
                required
                maxLength={36}
                pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
                value={baseline}
                onChange={(event) => setBaseline(event.target.value)}
              />
            </Field>
            <p>
              Selected snapshot: <code>{props.snapshotId}</code>. Changing
              choices or active observations does not replace an open
              comparison.
            </p>
            <button
              disabled={
                !baseline.trim() ||
                baseline.trim().toLowerCase() === props.snapshotId
              }
            >
              {controlIcons["compare"]} Compare selected snapshots
            </button>
          </form>
        </>
      )}
      {selection && (
        <LoadedSnapshots
          key={JSON.stringify(selection)}
          {...props}
          {...selection}
        />
      )}
    </section>
  );
}

function LoadedSnapshots(
  props: WorkspaceProps & { host: string; before: string; after: string },
) {
  const read = useRead(
    JSON.stringify([props.host, props.before, props.after]),
    async (signal) => {
      const load = async (id: string) => {
        const value = result(
          await props.client.api.GET(
            "/api/v1/publication/snapshots/{snapshotId}",
            {
              params: { path: { snapshotId: id }, query: { host: props.host } },
              signal,
            },
          ),
          (value) =>
            checks.PublicationSnapshot(value) &&
            checks.PublicationSnapshotSummary(value),
        );
        if (
          value.id !== id ||
          value.host !== props.host ||
          value.tenantId !== props.access.tenantId
        )
          throw new Error(
            "Comparison snapshot does not match its requested identity.",
          );
        return value;
      };
      const [before, after] = await Promise.all([
        load(props.before),
        load(props.after),
      ]);
      if (before.audience !== after.audience)
        throw new Error("Comparison snapshots must have the same audience.");
      const registry = (entry: { metadata?: { catalogReleaseId?: unknown } }) =>
        entry.metadata?.catalogReleaseId === "registry:self";
      const info = (value: Record<string, unknown>) =>
        Object.fromEntries(
          Object.entries(value).filter(([key]) => key !== "entries"),
        );
      return {
        before,
        after,
        ard: snapshotChanges(
          before.ard.entries.filter((entry) => !registry(entry)),
          after.ard.entries.filter((entry) => !registry(entry)),
        ),
        self: snapshotChanges(
          before.ard.entries.filter(registry),
          after.ard.entries.filter(registry),
        ),
        ai: snapshotChanges(before.aiCatalog.entries, after.aiCatalog.entries),
        exclusions: snapshotChanges(
          before.exclusions,
          after.exclusions,
          (item) => JSON.stringify([item.resourceId, item.identifier]),
        ),
        metadata: metadataChanges(
          {
            ardProfile: before.ardProfile,
            aiCatalogProfile: before.aiCatalogProfile,
            ardDocument: info(before.ard),
            aiCatalogDocument: info(before.aiCatalog),
          },
          {
            ardProfile: after.ardProfile,
            aiCatalogProfile: after.aiCatalogProfile,
            ardDocument: info(after.ard),
            aiCatalogDocument: info(after.aiCatalog),
          },
        ),
      };
    },
    props.onFailure,
  );
  return (
    <section aria-label="Captured snapshot comparison">
      <h3>
        Baseline <code>{props.before}</code> / selected{" "}
        <code>{props.after}</code>
      </h3>
      <p>
        These are observations of retained snapshots. ETags identify retained
        content, not current active state. Parsed metadata comparison does not
        prove original-byte equality or external availability. Duplicate
        identities remain separate occurrences; after exact matches, changed
        occurrences align in retained order.
      </p>
      <ReadState {...read} refresh={read.refresh} />
      {read.data && (
        <>
          <p>
            {read.data.before.host} / {read.data.before.audience}. Observed
            baseline state: {read.data.before.state}; selected state:{" "}
            {read.data.after.state}.
          </p>
          <Facts
            rows={[
              ["Baseline content ETag", <code>{read.data.before.etag}</code>],
              ["Selected content ETag", <code>{read.data.after.etag}</code>],
            ]}
          />
          <Changes label="ARD resource entries" changes={read.data.ard} />
          <Changes label="ARD Registry self-entries" changes={read.data.self} />
          <Changes label="AI Catalog resource entries" changes={read.data.ai} />
          <Changes
            label="Publication exclusions"
            changes={read.data.exclusions}
          />
          <details>
            <summary>Profile identities and document metadata</summary>
            <MetadataChanges
              changes={read.data.metadata}
              emptyMessage="No profile or document metadata changes."
            />
          </details>
        </>
      )}
    </section>
  );
}

function Changes({
  label,
  changes,
}: {
  label: string;
  changes: SnapshotChange[];
}) {
  const [page, setPage] = useState(0);
  return (
    <section aria-label={label}>
      <h3>
        {label}: {changes.length} changed occurrences
      </h3>
      {!changes.length && <p>No entry changes.</p>}
      {changes.slice(page * 25, (page + 1) * 25).map((change, index) => (
        <details key={index}>
          <summary>
            <ChangeLabel change={change.change} /> {change.identity} (occurrence{" "}
            {page * 25 + index + 1})
          </summary>
          {object(change.before) && object(change.after) ? (
            <MetadataChanges
              changes={metadataChanges(change.before, change.after)}
              beforeLabel="Baseline"
              afterLabel="Selected"
            />
          ) : (
            <>
              <h4>{change.before ? "Baseline" : "Selected"}</h4>
              <JSONBlock value={change.before ?? change.after} />
            </>
          )}
        </details>
      ))}
      <Pager
        label={`${label} comparison pagination`}
        cursor={page ? String(page) : ""}
        nextCursor={
          (page + 1) * 25 < changes.length ? String(page + 1) : undefined
        }
        onPage={(value) => setPage(Number(value || 0))}
      />
    </section>
  );
}
