import { DraftDownload } from "./DraftDownload";
import { Facts } from "./Facts";
const AuthoringWorkspace = lazy(() =>
  import("./AuthoringWorkspace").then((module) => ({
    default: module.AuthoringWorkspace,
  })),
);
import { controlIcons } from "../shell/Icon";
import { RevisionComparison } from "./RevisionComparison";
import { ValidationFindings } from "./ValidationFindings";
import { reuseDraft } from "./draft-reuse";
import { documentationGuideLink } from "../api/session";
import { NativeAuthoringHelp } from "./NativeAuthoringHelp";
import * as checks from "./responses";
import { number, object, page as checkedPage } from "../api/values";
// Version authoring retains immutable representation evidence and exposes validation as an explicit command.
import {
  lazy,
  Suspense,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { ArtifactDownload } from "./ArtifactDownload";
import { CodeBlock, JSONBlock } from "./CodeBlock";
import {
  useNamespaceAccess,
  useNamespaceAccessState,
} from "./namespace-access";
import type { components, operations } from "../api/schema";
import type { FetchResponse } from "openapi-fetch";
import { canRead, result, safeLink } from "../api/client";
import { permitsNamespace, contributionReason } from "../api/permissions";
import { ActionForm, EvidenceDetails } from "./authoring";
import { CopyReference } from "./CopyReference";
import { representationReferences } from "./handoff";
import { stateLabel } from "./operational-labels";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  confirmDiscard,
} from "./workspace";
import {
  RepresentationEditor,
  representationInput,
  newRepresentation,
  replaceRepresentation,
} from "./representations";

const SavedListingAuthoring = lazy(() =>
  import("./ListingPreparation").then((module) => ({
    default: module.SavedListingAuthoring,
  })),
);
import { commandETag } from "./authoring-save";
type RevisionBundle = NonNullable<
  FetchResponse<
    operations["getVersionRevision"],
    object,
    "application/json"
  >["data"]
>;

/** Select new-version authoring or existing-version detail from the resource route context. */
export default function Versions(props: WorkspaceProps) {
  const resourceID = new URLSearchParams(location.search).get("resource") || "";
  return (
    <>
      <Heading
        title="Resource versions"
        icon="book"
        description="Manage version documents and inspect their validation results and history."
      />
      {!canRead(props.access) ? (
        <p role="alert">
          The resource.read permission is required. When scopes are enforced,
          catalog.read is also required.
        </p>
      ) : props.id === "new" ? (
        <NewVersion {...props} resourceID={resourceID} />
      ) : (
        <VersionDetail {...props} />
      )}
    </>
  );
}

/** Resolve the parent resource before choosing kind-specific representation editing. */
function NewVersion(props: WorkspaceProps & { resourceID: string }) {
  const resource = useRead(
    props.resourceID,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/resources/{resourceId}", {
          params: { path: { resourceId: props.resourceID } },
          signal,
        }),
        (value) =>
          checks.Resource(value) &&
          object(value) &&
          value.id === props.resourceID &&
          value.tenantId === props.access.tenantId,
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState {...resource} />
      {resource.data && (
        <Suspense fallback={<p role="status">Loading authoring...</p>}>
          <AuthoringWorkspace
            {...props}
            resource={resource.data}
            sourceRevision={
              new URLSearchParams(location.search).get("sourceRevision") ??
              undefined
            }
          />
        </Suspense>
      )}
    </>
  );
}

/** Prepare an immutable revision of an existing unsealed Version. */
function VersionEditor(
  props: WorkspaceProps & {
    resource: components["schemas"]["Resource"];
    version: components["schemas"]["ResourceVersion"];
    etag?: string | null;
    onSaved: (
      version: components["schemas"]["ResourceVersion"],
      etag: string,
    ) => void;
  },
) {
  const namespace = useNamespaceAccessState(props, props.resource.namespaceId);
  const namespaceAccess = namespace.access;
  const editable =
    !props.version.sealedRevisionId &&
    permitsNamespace(
      namespaceAccess,
      "resource.edit",
      "catalog.write",
      props.resource.namespaceId,
    );
  const [initialRepresentations] = useState(() => [
    newRepresentation(props.resource.kind),
  ]);
  const [representations, setRepresentations] = useState(
    initialRepresentations,
  );
  const currentDrafts = useRef(representations);
  currentDrafts.current = representations;
  const [created, setCreated] = useState("");
  const [saved, setSaved] =
    useState<import("./authoring-save").VersionBundle>();
  const [currentETag, setCurrentETag] = useState(props.etag);
  const [prepareListing, setPrepareListing] = useState(false);
  const [reuseError, setReuseError] = useState("");
  const [reuseBusy, setReuseBusy] = useState(false);
  const [reusedRevision, setReusedRevision] = useState("");
  const reuseRequest = useRef<AbortController | undefined>(undefined);
  const reuseAllowed = permitsNamespace(
    namespaceAccess,
    "resource.read_content",
    "catalog.read",
    props.resource.namespaceId,
  );
  useEffect(() => () => reuseRequest.current?.abort(), []);
  useEffect(() => {
    if (reuseAllowed) return;
    reuseRequest.current?.abort();
    setReuseBusy(false);
  }, [reuseAllowed]);
  const guide = documentationGuideLink(
    props.runtime.documentationUrl,
    "resource-authoring",
  );
  const rows = useRef<HTMLDivElement>(null);
  const add = useRef<HTMLButtonElement>(null);
  const focusRow = useRef<string | undefined>(undefined);
  // Establish row focus during its commit. A delayed mount effect must not
  // consume a focus request for a row that a subsequent click is still adding.
  useLayoutEffect(() => {
    const id = focusRow.current;
    if (id === undefined) return;
    const control = rows.current!.ownerDocument.getElementById(`${id}-source`);
    if (control && !control.matches(":disabled")) control.focus();
    else add.current?.focus();
    focusRow.current = undefined;
  }, [representations]);
  return (
    <>
      <div className="workspace-toolbar">
        <a href={`${props.runtime.basePath}/resources/${props.resource.id}`}>
          Back to resource
        </a>
        {guide && (
          <a href={guide.href} target="_blank" rel="noreferrer">
            {guide.label}
          </a>
        )}
      </div>
      <ReadState {...namespace} />
      {!props.version.sealedRevisionId && reuseAllowed && (
        <button
          type="button"
          disabled={!editable || reuseBusy}
          onClick={() => {
            if (!confirmDiscard()) return;
            reuseRequest.current?.abort();
            const controller = new AbortController();
            reuseRequest.current = controller;
            const startingDrafts = currentDrafts.current;
            setReuseBusy(true);
            setReuseError("");
            void (async () => {
              try {
                const id = props.version.candidateRevisionId;
                if (!id)
                  throw new Error(
                    "This version has no current draft revision.",
                  );
                const bundle = result(
                  await props.client.api.GET(
                    "/api/v1/version-revisions/{revisionId}",
                    {
                      params: { path: { revisionId: id } },
                      signal: controller.signal,
                    },
                  ),
                  checks.RevisionEvidenceEnvelope,
                );
                if (
                  bundle.revision.id !== id ||
                  bundle.revision.resourceVersionId !== props.version.id ||
                  bundle.namespaceId !== props.resource.namespaceId
                )
                  throw new Error(
                    "The current draft does not match this version.",
                  );
                const drafts = await reuseDraft(
                  props.client,
                  props.resource.kind,
                  bundle.revision,
                  controller.signal,
                );
                if (controller.signal.aborted) return;
                if (currentDrafts.current !== startingDrafts)
                  throw new Error(
                    "Input changed while the draft was loading. No fields were replaced; start from the current draft again if needed.",
                  );
                focusRow.current = drafts[0]!.editorID;
                setRepresentations(drafts);
                setReusedRevision(id);
              } catch (error) {
                if (!controller.signal.aborted) {
                  setReuseError(
                    error instanceof Error
                      ? error.message
                      : "The complete draft could not be loaded. Existing input is unchanged.",
                  );
                  props.onFailure(error);
                }
              } finally {
                if (!controller.signal.aborted) setReuseBusy(false);
              }
            })();
          }}
        >
          {controlIcons["copy"]} Start from current draft
        </button>
      )}
      {reuseBusy && <p role="status">Reading the complete current draft...</p>}
      {reuseError && <p role="alert">{reuseError}</p>}
      {reusedRevision && (
        <p>
          Starting from revision <code>{reusedRevision}</code>. This is a
          complete replacement. Inline JSON is a decoded editing value, not
          original text. External URLs will be retrieved and validated again;
          Plugin checksums require the same archive. Skill inspection metadata
          cannot recreate archives.
        </p>
      )}
      <button
        type="button"
        disabled={!editable || reuseBusy}
        onClick={() => {
          if (!confirmDiscard()) return;
          setRepresentations([newRepresentation(props.resource.kind)]);
          setReusedRevision("");
          setReuseError("");
        }}
      >
        Start with blank replacement
      </button>
      <NativeAuthoringHelp
        kind={props.resource.kind}
        documentationUrl={props.runtime.documentationUrl}
      />
      <ActionForm
        requiredMarkers
        title="Create version revision"
        icon="save"
        editable={!!editable}
        description="Submit standards metadata for validation. This does not publish, install or execute the resource. A sealed version needs a new version label."
        allowed={
          editable &&
          !namespace.busy &&
          !namespace.error &&
          !reuseBusy &&
          !!currentETag &&
          permitsNamespace(
            namespaceAccess,
            "resource.edit",
            "catalog.write",
            props.resource.namespaceId,
          )
        }
        unavailableReason={
          contributionReason(namespaceAccess, props.resource.namespaceId) ??
          (props.version.sealedRevisionId
            ? "This version is sealed. Create a new version label to change its representations."
            : undefined) ??
          "Reload the version to obtain its current revision before saving."
        }
        dirty={
          JSON.stringify(representations) !==
          JSON.stringify(initialRepresentations)
        }
        onFailure={props.onFailure}
        another={{
          label: "Correct this draft",
          reset: () => {
            setCreated("");
          },
        }}
        prepare={() => {
          const inputs = representations.map(representationInput);
          if (inputs.filter((i) => i.role === "primary").length !== 1)
            throw new Error("Include exactly one primary representation.");
          const versionID = props.version.id;
          const etag = currentETag!;
          return {
            summary: [
              { label: "Resource", value: props.resource.canonicalIdentifier },
              { label: "Resource ID", value: props.resource.id },
              { label: "Namespace ID", value: props.resource.namespaceId },
              { label: "Version", value: props.version.version },
              {
                label: "Representations",
                value: inputs
                  .map((i) => `${i.role}: ${i.mediaType}`)
                  .join(", "),
              },
              { label: "Representation count", value: String(inputs.length) },
              { label: "Version ID", value: versionID },
              { label: "Revision", value: etag },
            ],
            run: async (key) => {
              const reply = await props.client.api.POST(
                "/api/v1/resource-versions/{versionId}/revisions",
                {
                  params: {
                    path: { versionId: versionID },
                    header: { "Idempotency-Key": key, "If-Match": etag },
                  },
                  body: { representations: inputs },
                },
              );
              result(reply);
              const updatedETag = commandETag(reply.response);
              const bundle = result(reply, (value) =>
                checks.versionCommandResult(value, {
                  tenantId: props.resource.tenantId,
                  resourceId: props.resource.id,
                  label: props.version.version,
                  versionId: versionID,
                }),
              );
              props.onSaved(bundle.version, updatedETag);
              setSaved(bundle);
              setCurrentETag(updatedETag);
              setCreated(bundle.version.id);
              return bundle.evidence.state === "complete"
                ? "Draft saved."
                : "Draft saved, corrections needed.";
            },
          };
        }}
      >
        <Field
          label="Version label"
          hint="A version label of up to 128 characters, for example 1.0.0. Match any version declared in the supplied document. Editing content creates a new immutable revision; it does not overwrite retained history."
        >
          <input
            required
            maxLength={128 * 2}
            readOnly
            value={props.version.version}
            placeholder="1.0.0"
          />
        </Field>
        <div ref={rows}>
          {representations.map((representation, index) => (
            <RepresentationEditor
              key={representation.editorID}
              kind={props.resource.kind}
              index={index}
              value={representation}
              version={props.version.version}
              onChange={(next) =>
                setRepresentations((items) =>
                  replaceRepresentation(items, representation.editorID, next),
                )
              }
              onRemove={
                representations.length > 1
                  ? () =>
                      setRepresentations((items) => {
                        const remaining = items.filter(
                          (item) => item.editorID !== representation.editorID,
                        );
                        // Remove is offered only with another required row present.
                        focusRow.current =
                          remaining[
                            Math.min(index, remaining.length - 1)
                          ]!.editorID;
                        return remaining;
                      })
                  : undefined
              }
            />
          ))}
          <button
            ref={add}
            type="button"
            disabled={representations.length >= 16}
            onClick={() => {
              const next = {
                ...newRepresentation(props.resource.kind),
                role: "metadata" as const,
              };
              focusRow.current = next.editorID;
              setRepresentations((items) => [...items, next]);
            }}
          >
            {controlIcons["add"]} Add representation
          </button>
        </div>
      </ActionForm>
      {saved && (
        <section className="workspace-panel">
          <ValidationFindings
            evidence={saved.evidence}
            revisionId={saved.revision.id}
          />
          <button
            type="button"
            onClick={() => setPrepareListing((value) => !value)}
          >
            Prepare Listing for this revision
          </button>
          {prepareListing && (
            <Suspense
              fallback={<p role="status">Loading Listing preparation...</p>}
            >
              <SavedListingAuthoring
                {...props}
                access={namespaceAccess}
                known={saved}
                version={saved.version}
              />
            </Suspense>
          )}
        </section>
      )}
      {created && (
        <p>
          Saved Version ID: <code>{created}</code>{" "}
          {permitsNamespace(
            namespaceAccess,
            "resource.read",
            "catalog.read",
            props.resource.namespaceId,
          ) && (
            <a
              className="button primary"
              href={`${props.runtime.basePath}/versions/${created}`}
            >
              Open saved version
            </a>
          )}
        </p>
      )}
    </>
  );
}

/** Read version and parent context with a bounded immutable revision history. */
function VersionDetail(props: WorkspaceProps) {
  const [acknowledged, setAcknowledged] = useState<{
    version: components["schemas"]["ResourceVersion"];
    etag: string;
  }>();
  const read = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/resource-versions/{versionId}",
        { params: { path: { versionId: props.id } }, signal },
      );
      const version = result(
        reply,
        (value) =>
          checks.ResourceVersion(value) &&
          object(value) &&
          value.id === props.id &&
          value.tenantId === props.access.tenantId &&
          number(value.revision, 1, Infinity, true),
      );
      const resource = result(
        await props.client.api.GET("/api/v1/resources/{resourceId}", {
          params: { path: { resourceId: version.resourceId } },
          signal,
        }),
        (value) =>
          checks.Resource(value) &&
          object(value) &&
          value.id === version.resourceId &&
          value.tenantId === props.access.tenantId,
      );
      return { version, resource, etag: reply.response.headers.get("ETag") };
    },
    props.onFailure,
  );
  // Preserve an acknowledgement through a failed refresh, until a read catches up.
  const detail = {
    ...read,
    data:
      read.data &&
      acknowledged &&
      acknowledged.version.revision > read.data.version.revision
        ? { ...read.data, ...acknowledged }
        : read.data,
  };
  const [edit, setEdit] = useState(false);
  const [prepareListing, setPrepareListing] = useState(false);
  const namespaceAccess = useNamespaceAccess(
    props,
    detail.data?.resource.namespaceId || "",
  );
  const contentAllowed =
    !!detail.data &&
    permitsNamespace(
      namespaceAccess,
      "resource.read_content",
      "catalog.read",
      detail.data.resource.namespaceId,
    );
  const editingAllowed =
    !!detail.data &&
    permitsNamespace(
      namespaceAccess,
      "resource.edit",
      "catalog.write",
      detail.data.resource.namespaceId,
    );
  const revisionReason =
    contributionReason(
      namespaceAccess,
      detail.data?.resource.namespaceId || "",
    ) ??
    (!detail.data?.etag
      ? "Reload the version to obtain its current revision before saving."
      : undefined);
  return (
    <>
      <ReadState {...detail} />
      {detail.data && (
        <>
          <a
            className="resource-return"
            href={`${props.runtime.basePath}/resources/${detail.data.resource.id}`}
          >
            {controlIcons.arrow} Back to resource
          </a>
          <section className="workspace-panel">
            <h2>Version {detail.data.version.version}</h2>
            <p>{detail.data.resource.canonicalIdentifier}</p>
            <p>
              <span className="state-badge" data-tone="neutral">
                {detail.data.version.sealedRevisionId ? "Sealed:" : "Draft:"}
              </span>{" "}
              {detail.data.version.sealedRevisionId
                ? "create a new version label to change representations."
                : "new revisions preserve earlier content."}
            </p>
            <EvidenceDetails
              title="Version metadata"
              value={detail.data.version}
            />
            <p>
              Inspect the exact revision and validation before selecting it in a
              listing. Reuse this Resource's one Listing for each version's
              metadata revisions.
            </p>
            <div className="workspace-toolbar">
              {!contributionReason(
                namespaceAccess,
                detail.data.resource.namespaceId,
              ) && (
                <button
                  type="button"
                  onClick={() => setPrepareListing((value) => !value)}
                >
                  Prepare listing metadata
                </button>
              )}
              {contentAllowed &&
                editingAllowed &&
                (detail.data.version.sealedRevisionId ||
                  detail.data.version.candidateRevisionId) && (
                  <a
                    className="button secondary"
                    href={`${props.runtime.basePath}/versions/new?resource=${detail.data.resource.id}&sourceRevision=${detail.data.version.sealedRevisionId || detail.data.version.candidateRevisionId}`}
                  >
                    Create new Version from this revision
                  </a>
                )}
              {contentAllowed && (
                <a
                  href={`${props.runtime.basePath}/version-revisions/${detail.data.version.sealedRevisionId || detail.data.version.candidateRevisionId}`}
                >
                  Inspect current revision and validation
                </a>
              )}
              {!detail.data.version.sealedRevisionId && (
                <button
                  disabled={!edit && !editingAllowed}
                  onClick={() => {
                    if (!edit || confirmDiscard()) setEdit((v) => !v);
                  }}
                  aria-expanded={edit}
                >
                  {controlIcons["edit"]} New revision
                </button>
              )}
            </div>
          </section>
          {!edit && revisionReason && <p className="muted">{revisionReason}</p>}
          {edit && (
            <VersionEditor
              {...props}
              {...detail.data}
              onSaved={(version, etag) => setAcknowledged({ version, etag })}
              access={namespaceAccess}
            />
          )}
          {prepareListing && contentAllowed && (
            <Suspense
              fallback={<p role="status">Loading Listing preparation...</p>}
            >
              <SavedListingAuthoring
                {...props}
                access={namespaceAccess}
                resource={detail.data.resource}
                version={detail.data.version}
              />
            </Suspense>
          )}
          <RevisionComparison
            {...props}
            access={namespaceAccess}
            kind="version"
            ownerId={detail.data.version.id}
            namespaceId={detail.data.resource.namespaceId}
          />
        </>
      )}
      {detail.data &&
        permitsNamespace(
          namespaceAccess,
          "resource.read_history",
          "catalog.read",
          detail.data.resource.namespaceId,
        ) && <VersionHistory {...props} contentAllowed={contentAllowed} />}
    </>
  );
}

/** Load history only after its canonical namespace and independent permission are known. */
function VersionHistory(props: WorkspaceProps & { contentAllowed: boolean }) {
  const [cursor, setCursor] = useState("");
  const history = useRead(
    props.id + cursor,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/resource-versions/{versionId}/revisions",
          {
            params: {
              path: { versionId: props.id },
              query: { limit: 25, ...(cursor ? { cursor: cursor } : {}) },
            },
            signal,
          },
        ),
        checkedPage(checks.RevisionSummary),
      ),
    props.onFailure,
  );
  return (
    <section className="workspace-panel">
      <h2>Revision history</h2>
      <ReadState {...history} />
      {history.data?.items.map((i) => (
        <p key={i.id}>
          {props.contentAllowed ? (
            <a href={`${props.runtime.basePath}/version-revisions/${i.id}`}>
              Revision {i.revisionNumber}
            </a>
          ) : (
            <span>Revision {i.revisionNumber}</span>
          )}{" "}
          <span className="muted">by {i.createdBy}</span>
        </p>
      ))}
      <Pager
        label="Version revision history pagination"
        cursor={cursor}
        nextCursor={history.data?.nextCursor}
        busy={history.busy}
        onPage={setCursor}
      />
    </section>
  );
}

/** Load a retained version-revision bundle before displaying its evidence. */
export function VersionRevision(props: WorkspaceProps) {
  const detail = useRead(
    props.id,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/version-revisions/{revisionId}", {
          params: { path: { revisionId: props.id } },
          signal,
        }),
        checks.RevisionEvidenceEnvelope,
      ),
    props.onFailure,
  );
  return (
    <>
      <Heading
        title="Version revision"
        icon="book"
        description="Exact retained representation metadata and latest validation. Reading this private evidence is audited."
      />
      <ReadState {...detail} />
      {detail.data && <RevisionContent {...props} bundle={detail.data} />}
    </>
  );
}

/** Use the returned canonical namespace without requiring parent metadata reads. */
function RevisionContent(props: WorkspaceProps & { bundle: RevisionBundle }) {
  const [evidence, setEvidence] = useState(props.bundle.evidence);
  const namespace = useNamespaceAccessState(props, props.bundle.namespaceId);
  return (
    <>
      {permitsNamespace(
        namespace.access,
        "resource.read",
        "catalog.read",
        props.bundle.namespaceId,
      ) && (
        <a
          className="resource-return"
          href={`${props.runtime.basePath}/versions/${props.bundle.revision.resourceVersionId}`}
        >
          {controlIcons.arrow} Back to version
        </a>
      )}
      <RevisionAuthoringActions {...props} access={namespace.access} />
      {!namespace.busy && !namespace.error && (
        <DraftDownload
          {...props}
          access={namespace.access}
          namespaceId={props.bundle.namespaceId}
          revisionId={props.bundle.revision.id}
        />
      )}
      <section className="workspace-panel">
        <h2>Revision {props.bundle.revision.revisionNumber}</h2>
        <p>
          Validation records checks on this revision; it does not grant trust,
          rights, review approval or permission to run it. Resolve blocking
          findings before submitting a listing. Earlier submissions keep their
          exact evidence.
        </p>
        <ValidationFindings
          evidence={evidence}
          revisionId={props.bundle.revision.id}
        />
        <EvidenceDetails
          title="Validation findings and evidence"
          value={evidence}
        />
        <EvidenceDetails
          title="Representation metadata"
          value={props.bundle.revision}
        />
        {props.bundle.revision.representations.map((item) => (
          <RepresentationContent key={item.id} {...props} item={item} />
        ))}
      </section>
      <ReadState {...namespace} />
      <ActionForm
        requiredMarkers
        title="Revalidate revision"
        intent="warning"
        icon="refresh"
        editable={permitsNamespace(
          namespace.access,
          "resource.validate",
          "catalog.write",
          props.bundle.namespaceId,
        )}
        unavailableReason={contributionReason(
          namespace.access,
          props.bundle.namespaceId,
          "resource.validate",
        )}
        description="Run validation again under the current policy. Earlier reviews keep their original evidence. If a submission is pending, an independent reviewer must request changes or reject it before you can submit new evidence. Blocked external checks can withdraw entire active discovery snapshots containing this revision, even when content is temporarily unavailable."
        allowed={
          !namespace.busy &&
          !namespace.error &&
          permitsNamespace(
            namespace.access,
            "resource.validate",
            "catalog.write",
            props.bundle.namespaceId,
          )
        }
        onFailure={props.onFailure}
        prepare={() => {
          const revisionId = props.bundle.revision.id;
          return {
            summary: [
              { label: "Version revision ID", value: revisionId },
              {
                label: "Version ID",
                value: props.bundle.revision.resourceVersionId,
              },
              { label: "Namespace ID", value: props.bundle.namespaceId },
              {
                label: "Revision number",
                value: String(props.bundle.revision.revisionNumber),
              },
              {
                label: "Precondition",
                value:
                  "No If-Match; validate this exact retained revision under current policy",
              },
            ],
            run: async (key) => {
              const updated = result(
                await props.client.api.POST(
                  "/api/v1/version-revisions/{revisionId}:validate",
                  {
                    params: {
                      path: { revisionId },
                      header: { "Idempotency-Key": key },
                    },
                    body: {},
                  },
                ),
                checks.RevisionEvidenceEnvelope,
              );
              setEvidence(updated.evidence);
              return `Validation: ${updated.evidence.state}.`;
            },
          };
        }}
      />
    </>
  );
}

/** Present retained representations and mount artifact reads only when requested. */
export function RepresentationContent(
  props: WorkspaceProps & {
    item: RevisionBundle["revision"]["representations"][number];
  },
) {
  const [open, setOpen] = useState(false);
  const artifactID = props.item.artifactId;
  return (
    <div className="workspace-panel">
      <h3>
        {props.item.role}: {props.item.mediaType}
      </h3>
      <p>Representation validation: {stateLabel(props.item.validationState)}</p>
      {props.item.package ? (
        <section aria-label="Skill package inspection">
          <h4>Skill package inspection</h4>
          <Facts
            rows={[
              ["Structure", stateLabel(props.item.package.structureState)],
              ["Files", props.item.package.fileCount],
              ["Expanded size", <> {props.item.package.expandedSize} bytes </>],
            ]}
          />
          <p>
            The complete archive is not stored here. Use the declared immutable
            Git or OCI reference. Structural validation does not assess malware,
            secrets, licenses or provenance, or establish remote availability.
          </p>
          <div role="group" aria-label="Declared immutable references">
            {representationReferences(props.item).map((reference) => (
              <CopyReference key={reference.label} {...reference} />
            ))}
          </div>
          {props.item.package.structureState === "passed" && (
            <details>
              <summary>
                File inventory ({props.item.package.files.length})
              </summary>
              <JSONBlock value={props.item.package.files} />
            </details>
          )}
        </section>
      ) : props.item.plugin ? (
        <>
          <p>
            Archive checksum: <code>{props.item.contentDigest}</code> (
            {props.item.size} bytes)
          </p>
          <p>
            Metadata inspection is not a security assessment. Components are not
            installed or executed.
          </p>
          {safeLink(props.item.url) && (
            <a href={safeLink(props.item.url)} target="_blank" rel="noreferrer">
              Open external archive
            </a>
          )}
          <h4>Components</h4>
          {props.item.plugin.components?.length ? (
            <ul>
              {props.item.plugin.components.map((component) => (
                <li key={`${component.kind}:${component.name}`}>
                  <strong>{component.name}</strong> ({component.kind})
                  <p>{component.path}</p>
                  {component.findings?.map((finding, index) => (
                    <p key={index}>{finding.message}</p>
                  ))}
                </li>
              ))}
            </ul>
          ) : (
            <p>No portable components declared.</p>
          )}
          <details>
            <summary>File inventory ({props.item.plugin.files.length})</summary>
            <JSONBlock value={props.item.plugin.files} />
          </details>
          {props.item.plugin.documents.map((document) => (
            <details key={document.path}>
              <summary>{document.path}</summary>
              <CodeBlock text={document.content} original rawText />
            </details>
          ))}
        </>
      ) : artifactID || props.item.data !== undefined ? (
        <>
          <button onClick={() => setOpen((v) => !v)} aria-expanded={open}>
            {open ? "Hide" : "Read"} retained document
          </button>
          {artifactID && (
            <ArtifactDownload
              key={artifactID}
              client={props.client}
              id={artifactID}
              onFailure={props.onFailure}
            />
          )}
          {open &&
            (artifactID ? (
              <Artifact {...props} id={artifactID} />
            ) : (
              <JSONBlock value={props.item.data} />
            ))}
        </>
      ) : (
        <p>No retained document. See the external handoff metadata.</p>
      )}
    </div>
  );
}

/** Fetch one retained artifact's exact content after explicit evidence selection. */
function Artifact(props: WorkspaceProps) {
  const content = useRead(
    props.id,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/artifacts/{artifactId}/content", {
          params: { path: { artifactId: props.id } },
          parseAs: "text",
          signal,
        }),
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState {...content} />
      {content.data !== undefined && (
        <CodeBlock text={content.data} original rawText />
      )}
    </>
  );
}

function RevisionAuthoringActions(
  props: WorkspaceProps & { bundle: RevisionBundle },
) {
  const allowed =
    permitsNamespace(
      props.access,
      "resource.read",
      "catalog.read",
      props.bundle.namespaceId,
    ) &&
    permitsNamespace(
      props.access,
      "resource.edit",
      "catalog.write",
      props.bundle.namespaceId,
    );
  const [show, setShow] = useState(false);
  const parent = useRead(
    show ? props.bundle.revision.resourceVersionId : "",
    async (signal) => {
      if (!show) return undefined;
      return result(
        await props.client.api.GET("/api/v1/resource-versions/{versionId}", {
          params: {
            path: { versionId: props.bundle.revision.resourceVersionId },
          },
          signal,
        }),
        (value) =>
          checks.ResourceVersion(value) &&
          object(value) &&
          value.id === props.bundle.revision.resourceVersionId &&
          value.tenantId === props.access.tenantId,
      );
    },
    props.onFailure,
  );
  if (!allowed) return null;
  return (
    <div className="workspace-toolbar">
      <button type="button" onClick={() => setShow(true)}>
        Prepare new Version from this revision
      </button>
      <ReadState {...parent} />
      {parent.data && (
        <a
          href={`${props.runtime.basePath}/versions/new?resource=${parent.data.resourceId}&sourceRevision=${props.bundle.revision.id}`}
        >
          Continue with source revision {props.bundle.revision.revisionNumber}
        </a>
      )}
    </div>
  );
}
