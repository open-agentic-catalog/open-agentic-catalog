import { commandSummary } from "./workspace";
import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
// Private registration requests reuse current source policy and exact server evidence.
import { useState } from "react";
import { APIError, result } from "../api/client";
import { permits } from "../api/permissions";
import { uncertain } from "../api/commands";
import type { components } from "../api/schema";
import { ActionForm } from "./authoring";
import { confirmDiscard, Pager, ReadState, Reason, useRead } from "./workspace";
import type { WorkspaceProps } from "./workspace";
import * as checks from "./responses";
import { characterText } from "./input-validation";
import {
  canRequestTrust,
  eligibleObservation,
  indexedTrustSelection,
} from "./trust-selection";
import type { TrustSelection } from "./trust-selection";
import { canReadTrust, canReadTrustJob, trustResultHref } from "./trust-result";
import { CopyText } from "./CopyReference";

type Schema = components["schemas"];
type SourceProps = WorkspaceProps & {
  source: Schema["FederationSource"];
  refreshSource?: () => void;
  onConfigure?: () => void;
};
const requiredAccess =
  "Requesting an assessment requires source.manage, source.read_evidence and federation.discover, plus sources.manage scope when enforced.";

/** Source editing has its own permissions; assessment evidence is loaded only on demand. */
export function SourceTrust(props: SourceProps) {
  const [choosing, setChoosing] = useState(false);
  return (
    <section className="workspace-panel">
      <h2>ANS registration assessment</h2>
      {canReadTrust(props.access) && (
        <p>
          <a
            href={`${props.runtime.basePath}/trust-assessments?sourceId=${props.source.id}`}
          >
            Open an assessment by Job ID
          </a>
        </p>
      )}
      <p>
        Check one external registration against the selected operator policy.
        This does not approve software, grant rights or publish a resource.
      </p>
      {!canRequestTrust(props.access) ? (
        <p role="alert">{requiredAccess}</p>
      ) : props.source.status !== "active" ? (
        <p>
          Assessments require an active source. Reload after the source is
          resumed or replaced.
        </p>
      ) : !props.source.trustVerificationPolicy ? (
        <p>
          Choose an ANS policy in the source configuration before requesting an
          assessment.
          {props.onConfigure &&
            permits(props.access, "source.manage", "sources.manage") && (
              <button type="button" onClick={props.onConfigure}>
                {controlIcons["edit"]} Change source configuration
              </button>
            )}
        </p>
      ) : (
        <>
          <p>
            Policy: <code>{props.source.trustVerificationPolicy}</code>.
            Verification must also be enabled on the API and worker. The server
            checks availability when you submit.
          </p>
          <p>
            For a live entry, run a query in{" "}
            <a href={`${props.runtime.basePath}/federation-search`}>
              Federation diagnostics
            </a>{" "}
            and select its exact result.
          </p>
          {!props.source.lastCompleteSnapshotId ? (
            <p>
              No complete indexed snapshot is available. Synchronize this source
              before selecting an indexed entry.
            </p>
          ) : !choosing ? (
            <button type="button" onClick={() => setChoosing(true)}>
              {controlIcons["list"]} Choose indexed entry
            </button>
          ) : (
            <IndexedEntries {...props} />
          )}
        </>
      )}
    </section>
  );
}

function IndexedEntries(props: SourceProps) {
  const [cursor, setCursor] = useState("");
  const [selected, setSelected] = useState<Schema["Observation"]>();
  const page = useRead(
    props.source.id + cursor,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/observations", {
          params: {
            query: {
              sourceId: props.source.id,
              freshness: "current",
              limit: 25,
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        checks.ObservationPage,
      ),
    props.onFailure,
  );
  if (selected)
    return (
      <IndexedEntry
        key={selected.id}
        {...props}
        observation={selected}
        onReselect={() => setSelected(undefined)}
      />
    );
  return (
    <>
      <p>
        Selecting an entry reads its private raw evidence and records an audit
        event. It does not start an assessment.
      </p>
      <ReadState
        {...page}
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      {page.data && !page.data.items.length && (
        <p>No current indexed entries.</p>
      )}
      {page.data?.items.map((item) => (
        <article className="workspace-panel" key={item.id}>
          <h3>{item.identifier || item.sourceKey}</h3>
          <p>
            {item.versionPresent
              ? `Version: ${item.version || "(empty)"}`
              : "No version supplied"}
          </p>
          <button
            type="button"
            disabled={!eligibleObservation(props.source, item)}
            onClick={() => setSelected(item)}
          >
            {controlIcons["book"]} Select indexed entry
          </button>
          {!eligibleObservation(props.source, item) && (
            <p>
              Current source membership and exact retained evidence are
              required.
            </p>
          )}
        </article>
      ))}
      <Pager
        label="ANS indexed entry pages"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

function IndexedEntry(
  props: SourceProps & {
    observation: Schema["Observation"];
    onReselect: () => void;
  },
) {
  const read = useRead(
    props.observation.id,
    async (signal) => {
      const raw = result(
        await props.client.api.GET("/api/v1/observations/{observationId}/raw", {
          params: { path: { observationId: props.observation.id } },
          signal,
          parseAs: "text",
        }),
      );
      const entry: unknown = JSON.parse(raw);
      return (
        indexedTrustSelection(props.source, props.observation, entry) ?? null
      );
    },
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data ? (
        <TrustRequest
          {...props}
          selection={read.data}
          onReselect={props.onReselect}
        />
      ) : (
        <>
          {!read.busy && !read.error && (
            <p>
              The selected entry lacks matching identifier, version, native type
              or delivery evidence. Select another current entry.
            </p>
          )}
          <button type="button" onClick={props.onReselect}>
            {controlIcons["list"]} Choose another entry
          </button>
        </>
      )}
    </>
  );
}

/** A live result is selected only from the administrator's original diagnostic response. */
export function LiveTrust(
  props: WorkspaceProps & {
    sourceId: string;
    selection: TrustSelection;
    onReselect: () => void;
  },
) {
  const read = useRead(
    props.sourceId,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/federation/sources/{sourceId}", {
          params: { path: { sourceId: props.sourceId } },
          signal,
        }),
        checks.FederationSource,
      ),
    props.onFailure,
  );
  const source = read.data;
  return (
    <section className="workspace-panel">
      <ReadState {...read} />
      {source &&
      source.id === props.sourceId &&
      source.revision === props.selection.sourceRevision ? (
        <TrustRequest {...props} source={source} />
      ) : (
        <>
          {source && (
            <p>
              The source changed. Run a fresh live diagnostic query and select
              the entry again.
            </p>
          )}
          <button type="button" onClick={props.onReselect}>
            Return to live results
          </button>
        </>
      )}
    </section>
  );
}

function TrustRequest(
  props: SourceProps & { selection: TrustSelection; onReselect: () => void },
) {
  const [reason, setReason] = useState("");
  const [stale, setStale] = useState(false);
  const [locked, setLocked] = useState(false);
  const [job, setJob] = useState<Schema["Job"]>();
  const [unavailable, setUnavailable] = useState(false);
  const allowed =
    canRequestTrust(props.access) &&
    props.source.status === "active" &&
    !!props.source.trustVerificationPolicy &&
    !stale;
  return (
    <>
      <ActionForm
        title="Request ANS assessment"
        icon="search"
        description={
          "Request a private assessment for this exact entry. A completed job may contain failed checks; completion does not mean a valid registration." +
          (props.selection.text
            ? " Confirming repeats the original query with this source and requires one exact matching entry."
            : "")
        }
        allowed={allowed}
        editable={canRequestTrust(props.access)}
        dirty={!!reason}
        onFailure={props.onFailure}
        unavailableReason={
          !canRequestTrust(props.access)
            ? requiredAccess
            : stale
              ? "This selection is stale. Select a current entry before another request."
              : "An active source and a selected operator policy are required."
        }
        prepare={() => {
          const sourceID = props.source.id;
          const body: Schema["TrustAssessmentRequest"] = {
            ...props.selection,
            reason: characterText(reason.trim(), "Assessment reason", 2000, 1),
          };
          return {
            summary: commandSummary([
              [
                "Source",
                `${props.source.displayName} (${sourceID}), revision ${body.sourceRevision}`,
              ] as const,
              ["Policy", props.source.trustVerificationPolicy!] as const,
              ["Entry", body.identifier] as const,
              [
                "Version",
                body.versionPresent
                  ? body.version || "(empty)"
                  : "Not supplied",
              ] as const,
              [
                "Native type and delivery",
                `${body.mediaType}, ${body.delivery}`,
              ] as const,
              ["Exact entry digest", body.exactEntryDigest] as const,
              [
                body.observationId
                  ? "Indexed observation"
                  : "Original live query",
                body.observationId || body.text!,
              ] as const,
              ["Reason", body.reason] as const,
            ]),
            run: async (key) => {
              setLocked(true);
              setUnavailable(false);
              try {
                const accepted = result(
                  await props.client.api.POST(
                    "/api/v1/federation/sources/{sourceId}/trust-assessments",
                    {
                      params: {
                        path: { sourceId: sourceID },
                        header: { "Idempotency-Key": key },
                      },
                      body,
                    },
                  ),
                  (value) =>
                    checks.Job(value) &&
                    typeof value === "object" &&
                    value !== null &&
                    "kind" in value &&
                    value.kind === "source.trust_verify",
                );
                setJob(accepted);
                return "Assessment Job accepted. Open its private assessment after it completes; the result can contain negative checks.";
              } catch (error) {
                if (!uncertain(error)) setLocked(false);
                if (error instanceof APIError && error.status === 412)
                  setStale(true);
                if (error instanceof APIError && error.status === 503)
                  setUnavailable(true);
                throw error;
              }
            },
          };
        }}
      >
        <Facts
          rows={[
            ["Selected entry", props.selection.identifier],
            ["Source revision", props.selection.sourceRevision],
            [
              "Policy",
              props.source.trustVerificationPolicy || "No policy selected",
            ],
            [
              "Exact entry digest",
              <code>{props.selection.exactEntryDigest}</code>,
            ],
          ]}
        />
        {props.selection.text ? (
          <p>
            Confirming this request sends the original query,{" "}
            <code>{props.selection.text}</code>, to this source again. The
            server requires one exact matching entry.
          </p>
        ) : (
          <p>
            The server checks membership in the current complete snapshot.
            Historical occurrence revisions are not used for this request.
          </p>
        )}
        <Reason value={reason} onChange={setReason} />
      </ActionForm>
      {unavailable && (
        <p role="alert">
          Verification or the selected policy is unavailable. Ask your operator
          to check API and worker configuration. Keep this command for an
          explicit retry.
        </p>
      )}
      {stale && props.refreshSource && (
        <button
          type="button"
          disabled={locked}
          onClick={() => {
            if (confirmDiscard()) props.refreshSource?.();
          }}
        >
          {controlIcons["refresh"]} Reload source and reselect
        </button>
      )}
      {job && (
        <p role="status">
          Assessment Job: <code>{job.id}</code>. Source:{" "}
          <code>{props.source.id}</code>.{" "}
          <CopyText label="Copy assessment Source ID" text={props.source.id} />
          <CopyText label="Copy assessment Job ID" text={job.id} />
          {canReadTrust(props.access) && (
            <a
              href={trustResultHref(
                props.runtime.basePath,
                props.source.id,
                job.id,
              )}
            >
              {controlIcons["book"]} Open private assessment
            </a>
          )}{" "}
          {canReadTrustJob(props.access) && (
            <a
              href={`${props.runtime.basePath}/jobs/${job.id}?sourceId=${encodeURIComponent(props.source.id)}`}
            >
              View Job status
            </a>
          )}{" "}
          Job status is not the private registration result.
        </p>
      )}
      {!job && (
        <button
          type="button"
          disabled={locked}
          onClick={() => {
            if (confirmDiscard()) props.onReselect();
          }}
        >
          {controlIcons["list"]} Choose another entry
        </button>
      )}
    </>
  );
}
