import { useState, type ReactNode } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { availablePermission, permits } from "../api/permissions";
import {
  choice,
  cursor,
  identifier,
  number,
  object,
  optional,
  sha256Digest,
  text,
  timestamp,
} from "../api/values";
import * as checks from "./responses";
import { Append } from "./AdoptVersion";
import { DateValue } from "./date-value";
import { Facts } from "./Facts";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  type WorkspaceProps,
} from "./workspace";

type Row = components["schemas"]["UpstreamChange"];
const outcomes = {
  unadopted_version: "Additional upstream version",
  changed_entry: "Changed upstream metadata",
  comparison_unavailable: "Comparison unavailable",
};
const initial = {
  check: "all" as "all" | "unadopted_version" | "entry_comparison",
  sourceId: "",
  namespaceId: "",
};

function pageResponse(value: unknown) {
  return (
    object(value) &&
    cursor(value.nextCursor) &&
    Array.isArray(value.items) &&
    value.items.length <= 100 &&
    value.items.every(
      (row) =>
        object(row) &&
        checks.VersionAdoptionTarget(row.target) &&
        text(row.identifier) &&
        choice(row.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
        text(row.mediaType) &&
        choice(row.outcome, Object.keys(outcomes)) &&
        checks.ObservationOccurrence(row.occurrence) &&
        object(row.occurrence) &&
        identifier(row.occurrence.observationId) &&
        sha256Digest(row.occurrence.retrievalDigest) &&
        sha256Digest(row.occurrence.exactEntryDigest) &&
        sha256Digest(row.occurrence.normalizedEntryDigest) &&
        sha256Digest(row.priorDigest) &&
        text(row.priorProfile) &&
        text(row.currentProfile) &&
        timestamp(row.snapshotCompletedAt) &&
        optional(
          row.latestAttempt,
          (a) =>
            object(a) &&
            identifier(a.snapshotId) &&
            number(a.revision, 1, Infinity, true) &&
            choice(a.state, ["complete", "incomplete", "failed"]) &&
            timestamp(a.completedAt),
        ),
    )
  );
}

function references(row: Row) {
  const o = row.occurrence;
  return new URLSearchParams({
    from: "upstream-changes",
    resourceId: row.target.resourceId,
    priorVersionRevisionId: row.target.priorVersionRevisionId,
    snapshotId: o.snapshotId,
    retrievalDigest: o.retrievalDigest,
    entryLocation: o.entryLocation,
    outcome: row.outcome,
  });
}

export function canBrowseUpstream(props: WorkspaceProps) {
  return (
    permits(props.access, "source.read_evidence", "sources.read") &&
    availablePermission(props.access, "resource.edit", "catalog.write")
  );
}

export default function UpstreamChanges(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Upstream changes"
        icon="globe"
        description="Find additional versions and changed source metadata for adopted Resources you can edit."
      />
      <Gate {...props}>
        <Inventory {...props} />
      </Gate>
    </>
  );
}

function Gate(props: WorkspaceProps & { children: ReactNode }) {
  if (!canBrowseUpstream(props))
    return (
      <p role="alert">
        Source evidence access and permission to edit Resources are required.
      </p>
    );
  return <Capability key={JSON.stringify(props.access)} {...props} />;
}
function Capability(props: WorkspaceProps & { children: ReactNode }) {
  const caps = useRead(
    "upstream-capability",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        checks.Capabilities,
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState {...caps} />
      {caps.data &&
        (caps.data.optionalCapabilities.versionAdoption === "enabled" ? (
          props.children
        ) : (
          <p>The administrator has not enabled upstream Version adoption.</p>
        ))}
    </>
  );
}

function Inventory(props: WorkspaceProps) {
  const [draft, setDraft] = useState(initial),
    [filters, setFilters] = useState(initial),
    [position, setPosition] = useState("");
  const read = useRead(
    JSON.stringify([filters, position]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/upstream-changes", {
          params: {
            query: {
              check: filters.check,
              ...(filters.sourceId && { sourceId: filters.sourceId }),
              ...(filters.namespaceId && { namespaceId: filters.namespaceId }),
              ...(position && { cursor: position }),
            },
          },
          signal,
        }),
        pageResponse,
      ),
    props.onFailure,
  );
  const refresh = () => (position ? setPosition("") : read.refresh());
  return (
    <>
      <form
        className="workspace-form"
        onSubmit={(e) => {
          e.preventDefault();
          setFilters(draft);
          setPosition("");
          if (JSON.stringify(draft) === JSON.stringify(filters) && !position)
            read.refresh();
        }}
      >
        <Field label="Condition">
          <select
            value={draft.check}
            onChange={(e) =>
              setDraft({
                ...draft,
                check: e.target.value as typeof draft.check,
              })
            }
          >
            <option value="all">Both checks</option>
            <option value="unadopted_version">
              Additional upstream versions
            </option>
            <option value="entry_comparison">
              Previously adopted entry comparisons
            </option>
          </select>
        </Field>
        <Field label="Source ID (optional)">
          <input
            value={draft.sourceId}
            onChange={(e) =>
              setDraft({ ...draft, sourceId: e.target.value.trim() })
            }
          />
        </Field>
        <Field label="Namespace ID (optional)">
          <input
            value={draft.namespaceId}
            onChange={(e) =>
              setDraft({ ...draft, namespaceId: e.target.value.trim() })
            }
          />
        </Field>
        <div className="workspace-toolbar">
          <button type="submit">Apply filters</button>
          <button type="button" onClick={refresh}>
            Refresh from first page
          </button>
        </div>
      </form>
      <ReadState {...read} />
      {read.data?.items.length === 0 && (
        <p>No matching changes in the retained complete source evidence.</p>
      )}
      {read.data?.items.map((row) => (
        <section key={row.occurrence.observationId} className="workspace-panel">
          <h2>{row.identifier}</h2>
          <RowFacts row={row} />
          <a
            href={
              props.runtime.basePath +
              "/observations/" +
              row.occurrence.observationId +
              "?" +
              references(row)
            }
          >
            Inspect selected occurrence
          </a>
        </section>
      ))}
      <Pager
        label="Upstream change pages"
        busy={read.busy}
        cursor={position}
        nextCursor={read.data?.nextCursor}
        onPage={setPosition}
      />
    </>
  );
}

function RowFacts({ row }: { row: Row }) {
  return (
    <Facts
      rows={[
        ["Condition", outcomes[row.outcome]],
        ["Upstream version", row.target.upstreamVersion],
        ["Prior local version", row.target.version],
        ["Source", row.occurrence.sourceId],
        ["Complete snapshot", row.occurrence.snapshotId],
        ["Snapshot completed", <DateValue value={row.snapshotCompletedAt} />],
        ["Observed", <DateValue value={row.occurrence.observedAt} />],
        [
          "Latest sync attempt",
          row.latestAttempt ? (
            <>
              {row.latestAttempt.state} on{" "}
              <DateValue value={row.latestAttempt.completedAt} />
            </>
          ) : (
            "No completed attempt under the current configuration"
          ),
        ],
      ]}
    />
  );
}

export function SelectedUpstreamChange(
  props: WorkspaceProps & { raw: ReactNode },
) {
  return (
    <Gate {...props}>
      <Selection {...props} />
    </Gate>
  );
}

function Selection(props: WorkspaceProps & { raw: ReactNode }) {
  const [showRaw, setShowRaw] = useState(false);
  const read = useRead(
    props.id + location.search,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/upstream-changes", {
          params: { query: { observationId: props.id } },
          signal,
        }),
        pageResponse,
      ),
    props.onFailure,
  );
  const captured = new URLSearchParams(location.search);
  const row = read.data?.items.find(
    (r) =>
      r.occurrence.observationId === props.id &&
      [...references(r)].every(
        ([key, value]) =>
          captured.getAll(key).length === 1 && captured.get(key) === value,
      ),
  );
  const archive =
    row?.kind === "skill" &&
    ["application/agent-skills+zip", "application/agent-skills+gzip"].includes(
      row.mediaType,
    );
  return (
    <>
      <a href={props.runtime.basePath + "/upstream-changes"}>
        Return to upstream changes
      </a>
      <ReadState {...read} />
      {read.data && !row && (
        <p role="alert">
          This selection is no longer available. Refresh the inventory and
          select an occurrence again.
        </p>
      )}
      {row && (
        <section className="workspace-panel">
          <h2>{row.identifier}</h2>
          <RowFacts row={row} />
          <Facts
            rows={[
              ["Local Resource", row.target.resourceId],
              ["Prior revision", row.target.priorVersionRevisionId],
              ["Entry location", row.occurrence.entryLocation],
              ["Retrieval digest", row.occurrence.retrievalDigest],
              ["Prior normalized digest", row.priorDigest],
              [
                "Current normalized digest",
                row.occurrence.normalizedEntryDigest,
              ],
              ["Prior normalization profile", row.priorProfile],
              ["Current normalization profile", row.currentProfile],
            ]}
          />
          <button
            type="button"
            aria-expanded={showRaw}
            onClick={() => setShowRaw(!showRaw)}
          >
            {showRaw ? "Hide raw entry" : "Read exact raw entry"}
          </button>
          {showRaw && props.raw}
          {archive ? (
            <p>
              Use inspected package authoring with an immutable Git or OCI
              reference for this Skill archive.
            </p>
          ) : row.outcome === "unadopted_version" ? (
            <Append
              {...props}
              occurrence={row.occurrence}
              target={row.target}
            />
          ) : (
            <p>
              {row.outcome === "comparison_unavailable"
                ? "The normalization profiles do not support a digest comparison."
                : "The upstream metadata changed for the same version."}{" "}
              Inspect the evidence here. No Version replacement is available.
            </p>
          )}
        </section>
      )}
    </>
  );
}
