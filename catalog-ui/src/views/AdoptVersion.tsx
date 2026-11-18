import { useState } from "react";
import { result } from "../api/client";
import { object } from "../api/values";
import { permits, permitsNamespace } from "../api/permissions";
import type { FetchResponse } from "openapi-fetch";
import type { operations } from "../api/schema";
import type { components } from "../api/schema";
import { ActionForm, EvidenceDetails } from "./authoring";
import { ReadState, commandSummary, useRead } from "./workspace";
import type { WorkspaceProps } from "./workspace";
import { useNamespaceAccess } from "./namespace-access";
import * as checks from "./responses";

type Schema = components["schemas"];
type Props = WorkspaceProps & { occurrence: Schema["ObservationOccurrence"] };

/** Capability availability never replaces current source/destination authority. */
export function AdoptVersion(props: Props) {
  const [find, setFind] = useState(false);
  const caps = useRead(
    "version-adoption-capability",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        checks.Capabilities,
      ),
    props.onFailure,
  );
  const canReadEvidence = permits(
    props.access,
    "source.read_evidence",
    "sources.read",
  );
  if (find && !canReadEvidence) setFind(false);
  if (!canReadEvidence) return null;
  return (
    <section className="workspace-panel">
      <h2>Adopt as a new Version</h2>
      <ReadState {...caps} />
      {caps.data &&
        (caps.data.optionalCapabilities.versionAdoption === "enabled" ? (
          <>
            <p>
              Append this selected upstream version to a Resource previously
              adopted from the same Source. Existing Listing choices and
              published releases stay unchanged.
            </p>
            {!find && (
              <button
                type="button"
                disabled={
                  !permits(
                    props.access,
                    "source.read_evidence",
                    "catalog.write",
                  )
                }
                onClick={() => setFind(true)}
              >
                Find existing local Resource
              </button>
            )}
            {find && <Destination {...props} />}
          </>
        ) : (
          <p>The administrator has not enabled upstream Version adoption.</p>
        ))}
    </section>
  );
}

function Destination(props: Props) {
  const read = useRead(
    props.id,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/observations/{observationId}/adoption-target",
          { params: { path: { observationId: props.id } }, signal },
        ),
        checks.VersionAdoptionTarget,
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data && <Append {...props} target={read.data} />}
    </>
  );
}

function Append(props: Props & { target: Schema["VersionAdoptionTarget"] }) {
  const [created, setCreated] =
    useState<
      NonNullable<
        FetchResponse<
          operations["adoptResourceVersion"],
          object,
          "application/json"
        >["data"]
      >
    >();
  const access = useNamespaceAccess(props, props.target.namespaceId);
  const allowed =
    permitsNamespace(
      access,
      "resource.edit",
      "catalog.write",
      props.target.namespaceId,
    ) && permits(props.access, "source.read_evidence", "sources.read");
  if (created && !allowed) setCreated(undefined);
  const canReadResource = permitsNamespace(
    access,
    "resource.read",
    "catalog.read",
    props.target.namespaceId,
  );
  const canReadContent = permitsNamespace(
    access,
    "resource.read_content",
    "catalog.read",
    props.target.namespaceId,
  );

  return (
    <>
      {(allowed || canReadResource) && (
        <p>
          Destination:{" "}
          {canReadResource ? (
            <a
              href={`${props.runtime.basePath}/resources/${props.target.resourceId}`}
            >
              {props.target.resourceId}
            </a>
          ) : (
            <code>{props.target.resourceId}</code>
          )}
          . Prior adopted version: <code>{props.target.version}</code>. Selected
          upstream version: <code>{props.target.upstreamVersion}</code>.
        </p>
      )}

      <ActionForm
        title="Confirm new Version adoption"
        icon="add"
        onFailure={props.onFailure}
        description="Use this exact complete occurrence and retained prior adoption. Inspect validation findings before selecting the draft in an existing Listing."
        allowed={allowed}
        prepare={() => {
          const body: Schema["AdoptResourceVersion"] = {
            observationId: props.id,
            snapshotId: props.occurrence.snapshotId,
            retrievalDigest: props.occurrence.retrievalDigest,
            entryLocation: props.occurrence.entryLocation,
            priorVersionRevisionId: props.target.priorVersionRevisionId,
          };
          return {
            summary: commandSummary([
              ["Resource ID", props.target.resourceId],
              ["Selected upstream version", props.target.upstreamVersion],
              ["Source ID", props.occurrence.sourceId],
              ["Observation ID", props.id],
              ["Snapshot ID", body.snapshotId],
              ["Entry location", body.entryLocation],
              ["Retrieval digest", body.retrievalDigest],
              ["Prior adopted revision", body.priorVersionRevisionId],
              [
                "Effect",
                "Create a draft Version; preserve existing Listing and releases",
              ],
            ]),
            run: async (key) => {
              setCreated(
                result(
                  await props.client.api.POST(
                    "/api/v1/resources/{resourceId}/versions:adopt",
                    {
                      params: {
                        path: { resourceId: props.target.resourceId },
                        header: { "Idempotency-Key": key },
                      },
                      body,
                    },
                  ),
                  (value) =>
                    checks.ResourceVersionAdoption(value) &&
                    object(value) &&
                    value.resourceId === props.target.resourceId &&
                    object(value.versionRevision) &&
                    object(value.versionRevision.adoption) &&
                    value.versionRevision.adoption.observationId === props.id &&
                    value.versionRevision.adoption.sourceId ===
                      props.occurrence.sourceId &&
                    value.versionRevision.adoption.sourceRevision ===
                      props.occurrence.sourceRevision &&
                    value.versionRevision.adoption.snapshotId ===
                      body.snapshotId &&
                    value.versionRevision.adoption.retrievalDigest ===
                      body.retrievalDigest &&
                    value.versionRevision.adoption.entryLocation ===
                      body.entryLocation &&
                    value.versionRevision.adoption.exactEntryDigest ===
                      props.occurrence.exactEntryDigest &&
                    (!props.occurrence.normalizedEntryDigest ||
                      value.versionRevision.adoption.normalizedEntryDigest ===
                        props.occurrence.normalizedEntryDigest),
                ),
              );
              return "New draft Version created. Existing Listing and releases are unchanged.";
            },
          };
        }}
      />
      {created && (
        <section aria-label="New adopted Version">
          <p>
            Draft version <code>{created.version.version}</code> is ready to
            inspect. Validation findings can block review or release.
          </p>
          <div className="workspace-actions">
            {canReadContent && (
              <a
                href={`${props.runtime.basePath}/version-revisions/${created.versionRevision.id}`}
              >
                Inspect adopted revision
              </a>
            )}
            {canReadResource && (
              <a
                href={`${props.runtime.basePath}/resources/${created.resourceId}`}
              >
                Open Resource and existing Listings
              </a>
            )}
          </div>
          <EvidenceDetails title="Version adoption result" value={created} />
        </section>
      )}
    </>
  );
}
