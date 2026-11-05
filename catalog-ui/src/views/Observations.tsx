import { commandSummary } from "./workspace";
import {
  CompletenessState,
  StructureState,
  StatusLabel,
  stateLabel,
} from "./operational-labels";

import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
import { DateValue } from "./date-value";

import { atField } from "./input-validation";
import { CopyReference } from "./CopyReference";
import { ExactReference } from "./ExactReference";
import { ChoicePrerequisite } from "./ChoicePrerequisite";
import { NamespaceChoice } from "./NamespaceChoice";
import { AdoptionMetadata, adoptionInput } from "./AdoptionMetadata";
import * as checks from "./responses";
// Observation browsing leaves audited raw evidence and local adoption behind explicit actions.
import { useState } from "react";
import { result } from "../api/client";
import {
  permits,
  permitsNamespace,
  resourceCreationAccess,
} from "../api/permissions";
import { CurrentRead, useCurrentRead } from "./current-read";
import type { components, operations } from "../api/schema";
import type { WorkspaceProps } from "./workspace";
import type { FetchResponse } from "openapi-fetch";
import { CodeBlock } from "./CodeBlock";
import { ActionForm, ChoiceField, EvidenceDetails } from "./authoring";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  confirmDiscard,
  useRead,
} from "./workspace";
import { parseObject } from "./representations";
import { useNamespaceAccess } from "./namespace-access";

type Schema = components["schemas"];
type Adoption = NonNullable<
  FetchResponse<
    operations["adoptObservation"],
    object,
    "application/json"
  >["data"]
>;

/** Browse provenance without fetching raw evidence or adopting on selection. */
export function Observations(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Source observations"
        icon="globe"
        description="Inspect external metadata and its source history, or adopt an exact occurrence as a local draft."
      />
      {permits(props.access, "source.read", "sources.read") ? (
        props.id ? (
          <ObservationDetail {...props} />
        ) : (
          <ObservationList {...props} />
        )
      ) : (
        <p role="alert">
          The source.read permission is required. When scopes are enforced,
          sources.read is also required.
        </p>
      )}
    </>
  );
}
/** Page observation metadata with explicit source and freshness filters. */
function ObservationList(props: WorkspaceProps) {
  const [sourceID, setSourceID] = useState(
    () => new URLSearchParams(location.search).get("source") || "",
  );
  const [freshness, setFreshness] = useState<"current" | "all">("current");
  const [draft, setDraft] = useState({ sourceID, freshness });
  const [cursor, setCursor] = useState("");
  const read = useCurrentRead(
    sourceID + freshness + cursor,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/observations", {
          params: {
            query: {
              ...(sourceID ? { sourceId: sourceID } : {}),
              freshness,
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.ObservationPage,
      ),
    props.onFailure,
  );
  return (
    <>
      <form
        className="workspace-toolbar"
        aria-label="Observation filters"
        onSubmit={(event) => {
          event.preventDefault();
          const sourceId = draft.sourceID.trim().toLowerCase();
          const url = new URL(location.href);
          if (sourceId) url.searchParams.set("source", sourceId);
          else url.searchParams.delete("source");
          history.replaceState(null, "", url.pathname + url.search + url.hash);
          setSourceID(sourceId);
          setFreshness(draft.freshness);
          setCursor("");
        }}
      >
        <Field
          label="Source ID (optional)"
          hint="Optional exact current Source UUID. Removed sources cannot supply this directory restriction; open exact retained evidence separately."
        >
          <input
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={draft.sourceID}
            onChange={(event) =>
              setDraft({ ...draft, sourceID: event.target.value })
            }
          />
        </Field>
        <Field label="Show entries">
          <select
            value={draft.freshness}
            onChange={(e) =>
              setDraft({
                ...draft,
                freshness: e.target.value as typeof freshness,
              })
            }
          >
            <option value="current">Current saved view</option>
            <option value="all">Current and historical entries</option>
          </select>
        </Field>
        <div className="workspace-actions">
          <button>Apply observation filters</button>
          <button
            type="button"
            onClick={() => {
              setSourceID("");
              setFreshness("current");
              setDraft({ sourceID: "", freshness: "current" });
              setCursor("");
              const url = new URL(location.href);
              url.searchParams.delete("source");
              history.replaceState(
                null,
                "",
                url.pathname + url.search + url.hash,
              );
            }}
          >
            Clear observation filters
          </button>
        </div>
      </form>
      <p>
        Applied observation filters: Source {sourceID || "all sources"}; view{" "}
        {freshness === "current"
          ? "Current saved view"
          : "Current and historical entries"}
        .
      </p>
      <ExactReference
        basePath={props.runtime.basePath}
        path="observations"
        label="Observation"
      />
      <ExactReference
        basePath={props.runtime.basePath}
        path="source-snapshots"
        label="Snapshot"
      />
      {sourceID && (
        <section aria-label="Applied source restriction">
          <p>
            Showing observations from Source <code>{sourceID}</code>.
          </p>
          <button
            type="button"
            onClick={() => {
              const url = new URL(location.href);
              url.searchParams.delete("source");
              history.replaceState(
                null,
                "",
                url.pathname + url.search + url.hash,
              );
              setSourceID("");
              setDraft((current) => ({ ...current, sourceID: "" }));
              setCursor("");
            }}
          >
            All sources
          </button>
        </section>
      )}
      <p className="muted">
        Current means the last saved source view. It does not check the source
        now. To adopt an entry, select a complete saved occurrence.
      </p>
      <details className="workspace-advanced">
        <summary>Find evidence after source removal</summary>
        <p>
          A source filter requires current source configuration. If the source
          is unavailable, clear the filter or open an exact saved Observation or
          Snapshot ID. Source removal does not mean its saved evidence was
          deleted.
        </p>
      </details>
      <CurrentRead
        read={read}
        label="observations"
        reload="Refresh observations"
        refresh={() => (read.error && cursor ? setCursor("") : read.refresh())}
      />
      {read.data?.items.map((item) => (
        <section className="workspace-panel" key={item.id}>
          <h2>
            <a href={`${props.runtime.basePath}/observations/${item.id}`}>
              {item.identifier || item.sourceKey}
            </a>
          </h2>
          <Facts
            rows={[
              [
                "Version",
                item.versionPresent
                  ? item.version || "(explicitly empty)"
                  : "Not supplied",
              ],
              ["Saved view", stateLabel(item.freshness)],
              ["Completeness", <CompletenessState state={item.completeness} />],
              [
                "Compatibility",
                <StatusLabel
                  state={item.compatibility.support}
                  tone="neutral"
                />,
              ],
              [
                "Source",
                <a href={`${props.runtime.basePath}/sources/${item.sourceId}`}>
                  {item.sourceId}
                </a>,
              ],
            ]}
          />
          <details className="workspace-advanced">
            <summary>Observation identifiers</summary>
            <CopyReference label="Observation ID" value={item.id} />
            <CopyReference label="Source ID" value={item.sourceId} />
          </details>
          {item.identifier && (!item.versionPresent || !!item.version) && (
            <p>
              <a
                href={`${props.runtime.basePath}/observation-group?${new URLSearchParams({ identifier: item.identifier, versionPresent: String(item.versionPresent), ...(item.versionPresent ? { version: item.version! } : {}) })}`}
              >
                Compare current contributions and conflicts
              </a>
            </p>
          )}
          {item.identifier && item.versionPresent && !item.version && (
            <p>
              Current contribution grouping is unavailable for an explicitly
              empty version. Its exact retained identity remains readable; the
              empty version is not omitted.
            </p>
          )}
          <EvidenceDetails title="Observation metadata" value={item} />
        </section>
      ))}
      {read.data && !read.data.items.length && <p>No matching observations.</p>}
      <Pager
        label="Observation pages"
        busy={read.busy}
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        onPage={setCursor}
      />
    </>
  );
}
/** Read occurrence provenance and expose raw-evidence and adoption controls explicitly. */
function ObservationDetail(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const [showRaw, setShowRaw] = useState(false);
  const canReadRaw = permits(
    props.access,
    "source.read_evidence",
    "sources.read",
  );
  // Revoked private evidence is discarded; restored authority requires another explicit read.
  if (showRaw && !canReadRaw) setShowRaw(false);
  const [selected, setSelected] = useState<Schema["ObservationOccurrence"]>();
  const page = useCurrentRead(
    props.id + cursor,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/observations/{observationId}/occurrences",
          {
            params: {
              path: { observationId: props.id },
              query: { limit: 25, ...(cursor ? { cursor: cursor } : {}) },
            },
            signal,
          },
        ),
        checks.ObservationOccurrencePage,
      ),
    props.onFailure,
  );
  return (
    <>
      <a href={`${props.runtime.basePath}/observations`}>
        Back to observations
      </a>
      <CopyReference label="Observation ID" value={props.id} />
      <button
        disabled={!canReadRaw}
        onClick={() => setShowRaw(!showRaw)}
        aria-expanded={showRaw}
      >
        {controlIcons["book"]}{" "}
        {showRaw ? "Hide raw entry" : "Read exact raw entry"}
      </button>
      {canReadRaw && showRaw && <RawObservation {...props} />}
      <section className="workspace-panel">
        <h2>Saved occurrences</h2>
        <p>
          Each occurrence records where and when the source supplied this entry.
          Adoption creates a local draft, never approval or publication.
        </p>
        <p>
          These are saved historical occurrences. Use the Current saved view
          filter to check current membership. Exact evidence can remain readable
          after source removal, even when source configuration and filtered
          history are unavailable.
        </p>
        <p>
          Reading raw entries requires source.read_evidence and sources.read
          scope when enforced. Adoption also requires complete retained evidence
          and resource.create in the destination Namespace, with catalog.write
          scope when enforced.
        </p>
        <CurrentRead
          read={page}
          label="retained occurrences"
          reload="Refresh occurrences"
          refresh={() =>
            page.error && cursor ? setCursor("") : page.refresh()
          }
        />
        {page.data?.items.map((item, index) => (
          <article
            className="workspace-panel"
            key={`${item.snapshotId}-${item.entryLocation}-${index}`}
          >
            <h3>Occurrence {index + 1}</h3>
            <details>
              <summary>Exact occurrence references</summary>
              <CopyReference label="Snapshot ID" value={item.snapshotId} />
              <CopyReference label="Source ID" value={item.sourceId} />
              <CopyReference
                label="Retrieval digest"
                value={item.retrievalDigest}
              />
              <CopyReference
                label="Entry location"
                value={item.entryLocation}
              />
            </details>
            <p>
              <a
                href={`${props.runtime.basePath}/source-snapshots/${item.snapshotId}`}
              >
                <DateValue value={item.observedAt} />
              </a>{" "}
              / <code>{item.entryLocation}</code>
            </p>
            <p>
              <a href={`${props.runtime.basePath}/sources/${item.sourceId}`}>
                Source
              </a>{" "}
              revision {item.sourceRevision}. Adoption binds this exact
              snapshot, retrieval and entry location.
            </p>
            <EvidenceDetails title="Occurrence provenance" value={item} />
            <button
              disabled={
                !item.evidenceComplete ||
                !permits(
                  props.access,
                  "source.read_evidence",
                  "sources.read",
                ) ||
                resourceCreationAccess(props.access) === "denied"
              }
              onClick={() => {
                if (confirmDiscard()) setSelected(item);
              }}
            >
              {controlIcons["add"]} Use this occurrence for adoption
            </button>
          </article>
        ))}
        {page.data && !page.data.items.length && (
          <p>No complete retained occurrences.</p>
        )}
        <Pager
          label="Retained occurrence pages"
          busy={page.busy}
          cursor={cursor}
          nextCursor={page.data?.nextCursor}
          onPage={setCursor}
        />
      </section>
      {selected && (
        <Adopt
          key={`${selected.snapshotId}-${selected.entryLocation}`}
          {...props}
          occurrence={selected}
        />
      )}
    </>
  );
}
// Mount only after an explicit evidence request; this read creates audit evidence.
function RawObservation(props: WorkspaceProps) {
  const read = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/observations/{observationId}/raw",
        {
          params: { path: { observationId: props.id } },
          signal,
          parseAs: "text",
        },
      );
      return {
        body: result(reply),
        digest: reply.response.headers.get("Content-Digest"),
      };
    },
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data && (
        <section className="workspace-panel">
          <h2>Exact raw entry</h2>
          <p>
            This explicit private-evidence read is audited. The entry is
            displayed as text, not executed.
          </p>
          <code>{read.data.digest}</code>
          <CodeBlock text={read.data.body} original rawText />
        </section>
      )}
    </>
  );
}
// Bind adoption to the selected retained occurrence, not the latest source state.
function Adopt(
  props: WorkspaceProps & { occurrence: Schema["ObservationOccurrence"] },
) {
  const [namespaceID, setNamespace] = useState("");
  const namespaceAccess = useNamespaceAccess(props, namespaceID);
  const [publisherID, setPublisher] = useState("");
  const [metadata, setMetadata] = useState('{"visibility":"private"}');
  const [created, setCreated] = useState<Adoption>();
  const createdAccess = useNamespaceAccess(
    props,
    created?.resource.namespaceId ?? "",
  );
  return (
    <>
      <ActionForm
        title="Adopt as local draft"
        icon="add"
        editable={
          permits(props.access, "source.read_evidence", "sources.read") &&
          resourceCreationAccess(props.access) !== "denied"
        }
        unavailableReason="Adoption requires source.read_evidence, resource.create in the destination Namespace, and their enforced OAuth scopes. Choose an authorized destination; read access to local resources is separate."
        description="Use this exact complete occurrence. Structural validity and publisher constraints are checked again; review and publication remain separate."
        allowed={
          permits(props.access, "source.read_evidence", "sources.read") &&
          permitsNamespace(
            namespaceAccess,
            "resource.create",
            "catalog.write",
            namespaceID,
          )
        }
        dirty={
          !!namespaceID ||
          !!publisherID ||
          metadata !== '{"visibility":"private"}'
        }
        onFailure={props.onFailure}
        prepare={() => {
          const extra = atField("adoption-overrides", () =>
            adoptionInput(parseObject(metadata)),
          );
          const body: Schema["AdoptObservation"] = {
            ...extra,
            snapshotId: props.occurrence.snapshotId,
            retrievalDigest: props.occurrence.retrievalDigest,
            entryLocation: props.occurrence.entryLocation,
            namespaceId: namespaceID,
            publisherId: publisherID,
          };
          return {
            summary: commandSummary([
              ["Action", "Adopt exact occurrence as local draft"] as const,
              ["Observation ID", props.id] as const,
              ["Source ID", props.occurrence.sourceId] as const,
              [
                "Source revision",
                String(props.occurrence.sourceRevision),
              ] as const,
              ["Snapshot ID", body.snapshotId] as const,
              ["Entry location", body.entryLocation] as const,
              ["Retrieval digest", body.retrievalDigest] as const,
              ["Namespace ID", body.namespaceId] as const,
              ["Publisher ID", body.publisherId] as const,
              [
                "Local metadata overrides",
                Object.keys(extra).length
                  ? JSON.stringify(extra)
                  : "None supplied; API checks retained evidence and defaults",
              ] as const,
            ]),
            run: async (key) => {
              setCreated(
                result(
                  await props.client.api.POST(
                    "/api/v1/observations/{observationId}:adopt",
                    {
                      params: {
                        path: { observationId: props.id },
                        header: { "Idempotency-Key": key },
                      },
                      body,
                    },
                  ),
                  checks.ObservationAdoption,
                ),
              );
              return "Local draft created.";
            },
          };
        }}
      >
        <p>
          Selected occurrence:{" "}
          <a
            href={`${props.runtime.basePath}/source-snapshots/${props.occurrence.snapshotId}`}
          >
            Inspect adoption snapshot
          </a>
          , entry <code>{props.occurrence.entryLocation}</code>. This creates
          locally governed draft history; it does not rewrite the upstream
          observation.
        </p>
        <NamespaceChoice
          workspace={props}
          label="Destination namespace"
          value={namespaceID}
          onChange={setNamespace}
        />
        {permits(props.access, "publisher.read", "publishers.read") ? (
          <ChoiceField
            label="Local publisher"
            manualLabel="Local publisher ID"
            nextSteps={<ChoicePrerequisite props={props} kind="publisher" />}
            value={publisherID}
            onChange={setPublisher}
            onFailure={props.onFailure}
            load={async (cursor, signal) => {
              const page = result(
                await props.client.api.GET("/api/v1/publishers", {
                  params: {
                    query: { limit: 25, ...(cursor ? { cursor: cursor } : {}) },
                  },
                  signal,
                }),
                checks.PublisherPage,
              );
              return {
                ...page,
                items: page.items.map((item) => ({
                  id: item.id,
                  label: `${item.displayName} / ${item.id}`,
                })),
              };
            }}
          />
        ) : (
          <Field
            label="Local publisher ID"
            hint="Required local publisher UUID, not its display name. Your current access does not allow listing publishers; ask an administrator for the ID."
          >
            <input
              required
              value={publisherID}
              onChange={(e) => setPublisher(e.target.value)}
            />
          </Field>
        )}
        <AdoptionMetadata value={metadata} onChange={setMetadata} />
      </ActionForm>
      {created && (
        <>
          {permitsNamespace(
            createdAccess,
            "resource.read",
            "catalog.read",
            created.resource.namespaceId,
          ) && (
            <a
              href={`${props.runtime.basePath}/resources/${created.resource.id}`}
            >
              Open adopted resource
            </a>
          )}
          <p>
            An authorized author can declare selected artwork in a local listing
            revision, with image URLs, alternative text and rights. Adoption
            does not copy source artwork or private overrides.
          </p>
          <EvidenceDetails title="Adoption result" value={created} />
        </>
      )}
    </>
  );
}

/** Gate provenance-group lookup before reading its retained occurrences. */
export function ObservationGroup(props: WorkspaceProps) {
  const query = new URLSearchParams(location.search);
  return (
    <>
      <Heading
        title="Observation contributions"
        icon="globe"
        description="Compare entries with the same identifier and version. See which sources supplied each entry and where it was recorded."
      />
      {permits(props.access, "source.read", "sources.read") ? (
        query.get("versionPresent") === "true" && !query.get("version") ? (
          <p role="alert">
            A present-empty version cannot be compared. Inspect its exact
            observation and raw evidence instead.
          </p>
        ) : (
          <GroupRead {...props} />
        )
      ) : (
        <p role="alert">
          The source.read permission is required. When scopes are enforced,
          sources.read is also required.
        </p>
      )}
    </>
  );
}
/** Read the requested observation group using its explicit identifier/version query. */
function GroupRead(props: WorkspaceProps) {
  const query = new URLSearchParams(location.search);
  const read = useCurrentRead(
    query.toString(),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/observation-groups", {
          params: {
            query: {
              identifier: query.get("identifier") || "",
              versionPresent: query.get("versionPresent") === "true",
              ...(query.has("version")
                ? { version: query.get("version")! }
                : {}),
            },
          },
          signal,
        }),
        checks.ObservationGroup,
      ),
    props.onFailure,
  );
  return (
    <>
      <CurrentRead
        read={read}
        label="observation contributions"
        reload="Reload contributions"
      />
      {read.data && (
        <section className="workspace-panel">
          <h2>{read.data.identifier}</h2>
          <p>
            Selected version:{" "}
            {query.get("versionPresent") === "true"
              ? query.get("version")
              : "No version supplied"}
            .
          </p>
          <p
            className={
              read.data.conflict || read.data.contributionsTruncated
                ? "notice"
                : "muted"
            }
          >
            {read.data.conflict
              ? "Different entry contents"
              : "Sources supplied matching entry contents"}
            {read.data.contributionsTruncated
              ? "; contribution list truncated"
              : ""}
          </p>
          {read.data.members.map((member, index) => (
            <article
              className="workspace-panel"
              key={member.normalizedEntryDigest}
            >
              <h3>Variant {index + 1}</h3>
              <details>
                <summary>Contents digest</summary>
                <code>{member.normalizedEntryDigest}</code>
              </details>
              {member.contributions.map((item) => (
                <section key={item.observation.id}>
                  <h4>
                    <a
                      href={`${props.runtime.basePath}/observations/${item.observation.id}`}
                    >
                      {item.observation.sourceKey}
                    </a>
                  </h4>
                  <Facts
                    rows={[
                      [
                        "Source",
                        <a
                          href={`${props.runtime.basePath}/sources/${item.observation.sourceId}`}
                        >
                          {item.observation.sourceId}
                        </a>,
                      ],
                      [
                        "Completeness",
                        <CompletenessState
                          state={item.observation.completeness}
                        />,
                      ],
                      [
                        "Structure",
                        <StructureState
                          state={item.observation.compatibility.structural}
                        />,
                      ],
                      [
                        "Support",
                        <StatusLabel
                          state={item.observation.compatibility.support}
                          tone="neutral"
                        />,
                      ],
                      ["Profile", item.observation.compatibility.profileId],
                      ["Saved occurrences", item.occurrenceCount],
                      [
                        "Different contents from this source",
                        <StatusLabel
                          state={item.sourceConflict ? "Yes" : "No"}
                          tone={item.sourceConflict ? "warning" : "neutral"}
                        />,
                      ],
                    ]}
                  />
                </section>
              ))}
            </article>
          ))}
          <EvidenceDetails title="Group evidence" value={read.data} />
        </section>
      )}
    </>
  );
}
