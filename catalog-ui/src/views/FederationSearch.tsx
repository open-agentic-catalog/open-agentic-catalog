import { commandSummary } from "./workspace";
import {
  CompletenessState,
  StructureState,
  StatusLabel,
  retrievalLabels,
} from "./operational-labels";

import { controlIcons } from "../shell/Icon";
import { KindBadge } from "./KindBadge";
import { Facts } from "./Facts";
import * as checks from "./responses";
// Federation diagnostics separate indexed observations from explicitly disclosed live queries.
import { useState } from "react";
import { result } from "../api/client";
import { permits } from "../api/permissions";
import type { components } from "../api/schema";
import type { WorkspaceProps } from "./workspace";
import { ActionForm, EvidenceDetails } from "./authoring";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  useRead,
  confirmDiscard,
} from "./workspace";
import { parseObject } from "./representations";
import { sourceValues } from "./federation-inputs";
import { kindLabel } from "../resource-kinds";

import { ValueList } from "./ValueList";
import { KindSelection } from "./KindSelection";
import {
  atField,
  byteText,
  characterText,
  metadataTerms,
  type InputError,
} from "./input-validation";
import { LiveTrust } from "./SourceTrust";
import { canRequestTrust, liveTrustSelection } from "./trust-selection";
import { CurrentRead, useCurrentRead } from "./current-read";

type Schema = components["schemas"];
const liveStatuses = {
  succeeded: "This bounded source query succeeded.",
  failed:
    "The source query failed; this status alone does not identify the cause.",
  invalid_response: "The returned response could not be accepted.",
  insufficient_evidence:
    "Some results lacked the declarations required by the selected filters.",
  policy_stale: "Source policy evidence is no longer current.",
  fanout_limited: "Configured fan-out bounds limited source processing.",
  diagnostics_truncated: "Additional source diagnostics were omitted.",
  continuation_unavailable:
    "The remaining upstream page was not followed; accepted results remain available.",
};

/** Indexed reads and explicitly confirmed live queries have separate results. */
export function FederationSearch(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Federation diagnostics"
        icon="search"
        description="Search collected source entries or send an explicit live query to approved sources."
      />
      {permits(props.access, "source.read", "sources.read") &&
      permits(props.access, "federation.discover", "sources.read") ? (
        <SearchForms {...props} />
      ) : (
        <p role="alert">
          The source.read and federation.discover permissions are required. When
          scopes are enforced, sources.read is also required.
        </p>
      )}
    </>
  );
}
/** Keep indexed diagnostic searches and confirmed live queries in separate submitted state. */
function SearchForms(props: WorkspaceProps) {
  const [text, setText] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [kinds, setKinds] = useState<Schema["ResourceKind"][]>([]);
  const [searchGeneration, setSearchGeneration] = useState(0);
  const [inputError, setInputError] = useState("");
  const [request, setRequest] = useState<Schema["ObservationSearch"]>();
  const [reason, setReason] = useState("");
  const [sourceIDs, setSourceIDs] = useState("");
  const [picker, setPicker] = useState(false);
  const [advanced, setAdvanced] = useState("{}");
  const [live, setLive] = useState<Schema["LiveSearchResponse"]>();
  const [liveQuery, setLiveQuery] = useState("");
  const [selectedLive, setSelectedLive] = useState<number>();
  const [liveGeneration, setLiveGeneration] = useState(0);
  const [indexedInput, setIndexedInput] = useState("");
  const [liveInput, setLiveInput] = useState("");
  const [liveRequest, setLiveRequest] = useState<{
    text: string;
    sourceIds: string[];
    options: object;
  }>();
  return (
    <>
      <section className="workspace-panel" aria-label="Shared search query">
        <Field
          label="Query"
          hint="Use this text for indexed searches or explicit live queries. Do not enter sensitive information in a live query."
        >
          <input
            id="observation-text"
            maxLength={1000}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </Field>
      </section>
      <section className="workspace-panel">
        <h2>Indexed observations</h2>
        <p>Kind and tag filters apply only to indexed search.</p>
        <form
          className="workspace-form"
          onSubmit={(e) => {
            e.preventDefault();
            let next: Schema["ObservationSearch"];
            try {
              const submittedText = atField("observation-text", () =>
                byteText(text.trim(), "Indexed search text", 500),
              );
              next = {
                ...(submittedText ? { text: submittedText } : {}),
                tags: atField("observation-tags", () =>
                  metadataTerms(
                    [...new Set(tags.map((tag) => tag.trim()))],
                    "tags",
                    32,
                  ),
                ),
                ...(kinds.length ? { kinds: [...kinds] } : {}),
                limit: 25,
              };
            } catch (cause) {
              // Only atField validators can fail here; their controls are mounted.
              const error = cause as InputError;
              setInputError(error.message);
              document.getElementById(error.fieldId)!.focus();
              return;
            }
            setRequest(next);
            setIndexedInput(JSON.stringify({ text, tags, kinds }));
            setSearchGeneration((generation) => generation + 1);
            setInputError("");
          }}
        >
          <KindSelection value={kinds} onChange={setKinds} />
          <ValueList
            id="observation-tags"
            label="Observation tags"
            itemLabel="Observation tag"
            value={tags}
            onChange={setTags}
            maximum={32}
            length={80}
            hint="All selected tags must match."
          />
          {inputError && <p role="alert">{inputError}</p>}
          <button type="submit">
            {controlIcons["search"]} Search indexed observations
          </button>
        </form>
        {request && (
          <section aria-label="Applied indexed criteria">
            <h3>Applied indexed criteria</h3>
            <p>
              Text: <code>{request.text || "No text filter"}</code>.
            </p>
            <p>
              Kinds: {request.kinds?.map(kindLabel).join(", ") || "All kinds"}.
              Tags: {request.tags?.join(", ") || "No tag filter"}.
            </p>
            {indexedInput !== JSON.stringify({ text, tags, kinds }) && (
              <p role="status">
                Filters changed. Search again to update these results.
              </p>
            )}
          </section>
        )}
        {request && (
          <ObservationResults
            key={searchGeneration}
            {...props}
            request={request}
          />
        )}
      </section>
      <ActionForm
        key={liveGeneration}
        title="Query live sources"
        icon="globe"
        intent="warning"
        description="Send this query to approved sources. Do not include sensitive information. Results are temporary; source failures are shown alongside any results."
        unavailableReason={
          !text.trim()
            ? "Enter a query to run a live diagnostic query."
            : "Live queries require source.read and sources.read scope when enforced."
        }
        allowed={
          !!text.trim() && permits(props.access, "source.read", "sources.read")
        }
        dirty={!!reason}
        onFailure={props.onFailure}
        prepare={() => {
          const options = parseObject(advanced);
          if (
            Object.keys(options).some(
              (key) => !["filter", "@context", "pageSize"].includes(key),
            )
          )
            throw new Error(
              "Live options accept filter, @context and pageSize only.",
            );
          const body: Schema["LiveSearch"] = {
            ...options,
            text: characterText(text.trim(), "Live search text", 500, 1),
            reason: reason.trim(),
            ...(sourceIDs.trim() ? { sourceIds: sourceValues(sourceIDs) } : {}),
          };
          if ((body.sourceIds?.length ?? 0) > 8)
            throw new Error("Select at most eight unique source IDs.");
          const capturedInput = JSON.stringify({ text, sourceIDs, advanced });
          return {
            summary: commandSummary([
              ["Action", "Send live diagnostic query"] as const,
              ["Query", body.text] as const,
              [
                "Source IDs",
                body.sourceIds?.join(", ") ??
                  "Administrator-managed eligible sources",
              ] as const,
              ["Options", JSON.stringify(options)] as const,
              ["Reason", body.reason] as const,
            ]),
            run: async (key) => {
              setLive(
                result(
                  await props.client.api.POST(
                    "/api/v1/federation/sources:live-search",
                    { params: { header: { "Idempotency-Key": key } }, body },
                  ),
                  checks.LiveSearchResponse,
                ),
              );
              setLiveQuery(body.text);
              setLiveRequest({
                text: body.text,
                sourceIds: body.sourceIds ?? [],
                options,
              });
              setLiveInput(capturedInput);
              setSelectedLive(undefined);
              return "Diagnostic query completed. Review partial results and source diagnostics.";
            },
          };
        }}
      >
        <p>Query: {text || "Enter a query above."}</p>
        <Field
          label="Source IDs (optional)"
          hint="Leave blank to use approved sources, or choose up to eight configured sources. New source URLs cannot be added here."
        >
          <input
            value={sourceIDs}
            onChange={(e) => setSourceIDs(e.target.value)}
          />
        </Field>
        <p className="muted">
          Selected source IDs: {sourceValues(sourceIDs).length} / 8. Listed
          status and live settings describe configuration, not availability or
          guaranteed query eligibility. Server policy is checked at execution.
        </p>
        <button type="button" onClick={() => setPicker(!picker)}>
          {picker ? "Close source picker" : "Choose configured sources"}
        </button>
        {picker && (
          <SourcePicker
            {...props}
            selected={sourceValues(sourceIDs)}
            onChange={(ids) => setSourceIDs(ids.join(", "))}
          />
        )}
        <Field
          label="Advanced live options (JSON)"
          hint={
            'Optional JSON object with filter, @context and pageSize only. Leave {} for defaults. Example: {"pageSize": 10}.'
          }
        >
          <textarea
            rows={6}
            value={advanced}
            onChange={(e) => setAdvanced(e.target.value)}
          />
        </Field>
        <Reason value={reason} onChange={setReason} />
      </ActionForm>
      {live && (
        <>
          <button
            type="button"
            onClick={() => {
              if (!confirmDiscard()) return;
              setLive(undefined);
              setLiveRequest(undefined);
              setSelectedLive(undefined);
              setLiveGeneration((value) => value + 1);
            }}
          >
            {controlIcons["globe"]} Start a new live query
          </button>
          <p className={live.partial ? "notice" : "muted"}>
            {live.partial
              ? "Some source queries or results are incomplete. Available results are shown."
              : "Live diagnostic complete."}
          </p>
          <section aria-label="Live diagnostic results">
            <h2>Transient live results</h2>
            {liveRequest && (
              <section aria-label="Applied live query">
                <h3>Applied live query</h3>
                <p>
                  Submitted query: <code>{liveRequest.text}</code>.
                </p>
                <p>
                  Requested sources:{" "}
                  {liveRequest.sourceIds.join(", ") ||
                    "Administrator-managed eligible sources"}
                  .
                </p>
                <EvidenceDetails
                  title="Applied live options"
                  value={liveRequest.options}
                />
                {liveInput !==
                  JSON.stringify({ text, sourceIDs, advanced }) && (
                  <p role="status">
                    Unsubmitted live changes. Existing results retain their
                    submitted query and requested sources.
                  </p>
                )}
              </section>
            )}
            <p className="muted">
              These are external declarations, not retained observations,
              approval or availability checks. There is no adoption action or
              durable bookmark for a live result.
            </p>
            {!live.results.length && <p>No live results returned.</p>}
            {live.results.map((item, index) => (
              <article className="workspace-panel" key={index}>
                <h3>{item.identifier}</h3>
                <p>{versionLabel(item.versionPresent, item.version)}</p>
                <Facts
                  rows={[
                    ["Score", item.score],
                    [
                      "Source ID",
                      <a
                        href={`${props.runtime.basePath}/sources/${item.sourceId}`}
                      >
                        {item.sourceId}
                      </a>,
                    ],
                    ["Declared source", item.source],
                    [
                      "Declared resource type",
                      typeof item.entry.type === "string"
                        ? item.entry.type
                        : "Not supplied as text; inspect the raw entry.",
                    ],
                    [
                      "Native kind and compatibility",
                      <>
                        Not supplied in this live projection; inspect the raw
                        entry.
                      </>,
                    ],
                  ]}
                />
                {canRequestTrust(props.access) && (
                  <>
                    <button
                      type="button"
                      disabled={
                        selectedLive !== undefined ||
                        !liveTrustSelection(item, liveQuery)
                      }
                      onClick={() => setSelectedLive(index)}
                    >
                      Select for ANS assessment
                    </button>
                    {!liveTrustSelection(item, liveQuery) && (
                      <p>
                        This result lacks the exact native entry fields required
                        for an assessment.
                      </p>
                    )}
                  </>
                )}
              </article>
            ))}
            <h3>Source diagnostics</h3>
            {!live.diagnostics?.length && (
              <p>No source diagnostics supplied.</p>
            )}
            {live.diagnostics?.map((item, index) => (
              <p key={index}>
                {item.sourceId ? (
                  <a
                    href={`${props.runtime.basePath}/sources/${item.sourceId}`}
                  >
                    {item.sourceId}
                  </a>
                ) : (
                  "No source attribution"
                )}
                {" / "}
                <StatusLabel
                  state={item.status}
                  tone={
                    item.status === "succeeded"
                      ? "success"
                      : item.status === "failed" ||
                          item.status === "invalid_response"
                        ? "danger"
                        : "warning"
                  }
                />
                : {liveStatuses[item.status]}
                {item.message && `: ${item.message}`}
              </p>
            ))}
          </section>
          {selectedLive !== undefined &&
            live.results[selectedLive] &&
            (() => {
              const item = live.results[selectedLive];
              const selection = liveTrustSelection(item, liveQuery);
              return (
                selection && (
                  <LiveTrust
                    key={selectedLive}
                    {...props}
                    sourceId={item.sourceId}
                    selection={selection}
                    onReselect={() => setSelectedLive(undefined)}
                  />
                )
              );
            })()}
          <EvidenceDetails title="Live results and diagnostics" value={live} />
        </>
      )}
    </>
  );
}
/** Page indexed observation search results for the submitted diagnostic query. */
function ObservationResults(
  props: WorkspaceProps & { request: Schema["ObservationSearch"] },
) {
  const [cursor, setCursor] = useState("");
  const read = useCurrentRead(
    JSON.stringify(props.request) + cursor,
    async (signal) =>
      result(
        await props.client.api.POST("/api/v1/observations:search", {
          body: { ...props.request, ...(cursor ? { cursor: cursor } : {}) },
          signal,
        }),
        checks.ObservationSearchPage,
      ),
    props.onFailure,
  );
  return (
    <>
      <CurrentRead
        read={read}
        label="indexed diagnostic results"
        reload="Refresh indexed results"
        refresh={() => (read.error && cursor ? setCursor("") : read.refresh())}
      />
      {read.data && (
        <>
          <section aria-label="Indexed diagnostic results">
            <p className="muted">
              Freshness describes the retained source view, not peer
              availability. Structural validity and native support do not
              establish trust or permission to execute.
            </p>
            {!read.data.items.length && (
              <p>No matching indexed observations.</p>
            )}
            {read.data.items.map(({ observation, score, source }) => (
              <article className="workspace-panel" key={observation.id}>
                <h3>
                  <a
                    href={`${props.runtime.basePath}/observations/${observation.id}`}
                  >
                    {observation.identifier || observation.sourceKey}
                  </a>
                </h3>
                <p>
                  {versionLabel(
                    observation.versionPresent,
                    observation.version,
                  )}
                </p>
                <Facts
                  rows={[
                    [
                      "Kind",
                      observation.kind ? (
                        <KindBadge kind={observation.kind} />
                      ) : (
                        "Native kind not supplied"
                      ),
                    ],
                    ["Score", score],
                    [
                      "Source",
                      <a href={`${props.runtime.basePath}/sources/${source}`}>
                        {source}
                      </a>,
                    ],
                    ["Freshness", observation.freshness],
                    [
                      "Completeness",
                      <CompletenessState state={observation.completeness} />,
                    ],
                    [
                      "Structural validation",
                      <StructureState
                        state={observation.compatibility.structural}
                      />,
                    ],
                    [
                      "Native support",
                      <StatusLabel
                        state={observation.compatibility.support}
                        tone="neutral"
                      />,
                    ],
                    [
                      "Profile",
                      observation.compatibility.profileId || "Not supplied",
                    ],
                  ]}
                />
              </article>
            ))}
          </section>
          <EvidenceDetails title="Indexed results" value={read.data} />
        </>
      )}
      <Pager
        label="Indexed diagnostic result pages"
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        busy={read.busy}
        onPage={setCursor}
      />
    </>
  );
}

function versionLabel(present: boolean, version: string | undefined) {
  return present ? `Version: ${version || "(empty)"}` : "No version supplied";
}

/** Opening this optional picker loads one bounded source page, never a live query. */
function SourcePicker(
  props: WorkspaceProps & {
    selected: string[];
    onChange: (ids: string[]) => void;
  },
) {
  const [name, setName] = useState("");
  const [draft, setDraft] = useState("");
  const [cursor, setCursor] = useState("");
  const read = useRead(
    JSON.stringify([name, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/federation/sources", {
          params: {
            query: {
              limit: 25,
              ...(name ? { displayName: name } : {}),
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        checks.FederationSourcePage,
      ),
    props.onFailure,
  );
  return (
    <section aria-label="Configured source picker">
      <Field
        label="Source name (exact match)"
        hint="Match the complete saved name, including capital letters and spaces. Selected sources remain selected when they are absent from these choices."
      >
        <input
          maxLength={320}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
      </Field>
      <div className="workspace-actions">
        <button
          type="button"
          onClick={() => {
            setName(draft);
            setCursor("");
          }}
        >
          Apply source choice filter
        </button>
        <button
          type="button"
          onClick={() => {
            setName("");
            setDraft("");
            setCursor("");
          }}
        >
          Clear source choice filter
        </button>
      </div>
      <p>
        Applied source choice name: {name ? JSON.stringify(name) : "all names"}.
      </p>
      <button
        type="button"
        onClick={() => (read.error && cursor ? setCursor("") : read.refresh())}
        disabled={read.busy}
      >
        {controlIcons["refresh"]} Refresh source choices
      </button>
      <ReadState
        {...read}
        refresh={() => (read.error && cursor ? setCursor("") : read.refresh())}
      />
      {read.data && !read.data.items.length && (
        <p>No configured sources on this page. Manual IDs remain available.</p>
      )}
      {read.data?.items.map((source) => (
        <label className="source-choice" key={source.id}>
          <input
            type="checkbox"
            checked={props.selected.includes(source.id)}
            disabled={
              props.selected.length >= 8 && !props.selected.includes(source.id)
            }
            onChange={(e) =>
              props.onChange(
                e.target.checked
                  ? [...props.selected, source.id]
                  : props.selected.filter((id) => id !== source.id),
              )
            }
          />
          <span>
            {source.displayName} ({source.id}) — {source.status};{" "}
            {retrievalLabels[source.retrievalMode]}; live{" "}
            {source.live.enabled ? "enabled" : "disabled"}
          </span>
        </label>
      ))}
      <Pager
        label="Configured source choice pages"
        busy={read.busy}
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        onPage={setCursor}
      />
    </section>
  );
}
