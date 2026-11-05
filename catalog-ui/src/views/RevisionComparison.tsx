import { controlIcons } from "../shell/Icon";
// Compare selected immutable evidence without moving candidates or issuing commands.
import { useState } from "react";
import { result } from "../api/client";
import { page, object } from "../api/values";
import { permitsNamespace } from "../api/permissions";
import { ChoiceField } from "./authoring";
import { Field, ReadState, useRead, type WorkspaceProps } from "./workspace";
import { verifyArtifact } from "./ArtifactDownload";
import { CodeBlock, JSONBlock } from "./CodeBlock";
import * as checks from "./responses";
import type { components } from "../api/schema";

type Props = WorkspaceProps & {
  kind: "version" | "listing";
  ownerId: string;
  namespaceId: string;
  fixedAfter?: string;
};
type Metadata = Record<string, unknown>;
type Representation = Pick<
  components["schemas"]["Representation"],
  | "id"
  | "role"
  | "mediaType"
  | "profileId"
  | "url"
  | "contentDigest"
  | "size"
  | "artifactId"
  | "data"
> & { package?: unknown; plugin?: unknown };
type Compared = {
  id: string;
  fields: Metadata;
  representations?: Representation[];
  versionRevisionId?: string;
};

export function RevisionComparison(props: Props) {
  return (
    <ComparisonContext
      key={JSON.stringify([
        props.access,
        props.kind,
        props.ownerId,
        props.namespaceId,
        props.fixedAfter,
      ])}
      {...props}
    />
  );
}
function ComparisonContext(props: Props) {
  const [open, setOpen] = useState(false);
  const [before, setBefore] = useState("");
  const [after, setAfter] = useState(props.fixedAfter ?? "");
  const [selection, setSelection] = useState<{
    before: string;
    after: string;
  }>();
  const history = permitsNamespace(
    props.access,
    "resource.read_history",
    "catalog.read",
    props.namespaceId,
  );
  const content = permitsNamespace(
    props.access,
    "resource.read_content",
    "catalog.read",
    props.namespaceId,
  );
  const choices = async (cursor: string, signal: AbortSignal) => {
    const query = { limit: 25, ...(cursor ? { cursor } : {}) };
    const rows =
      props.kind === "version"
        ? result(
            await props.client.api.GET(
              "/api/v1/resource-versions/{versionId}/revisions",
              { params: { path: { versionId: props.ownerId }, query }, signal },
            ),
            page(checks.RevisionSummary),
          )
        : result(
            await props.client.api.GET(
              "/api/v1/catalog-listings/{listingId}/revisions",
              { params: { path: { listingId: props.ownerId }, query }, signal },
            ),
            page(checks.RevisionSummary),
          );
    return {
      ...rows,
      items: rows.items.map((item) => ({
        id: item.id,
        label: `Revision ${item.revisionNumber} / ${item.id}`,
      })),
    };
  };
  return (
    <section className="workspace-panel" aria-label="Revision comparison">
      <h2>Compare exact revisions</h2>
      <p>
        Compare immutable metadata before review or replacement. History
        permission does not grant document access. Comparison does not edit,
        approve, fetch an external archive or execute either side.
      </p>
      {!history || !content ? (
        <p>
          Comparison needs resource.read_history and resource.read_content in
          this namespace, plus catalog.read scope when enforced.
        </p>
      ) : (
        <>
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? "Close comparison choices" : "Choose revisions to compare"}
          </button>
          {open && (
            <>
              <ChoiceField
                label="Before revision"
                value={before}
                onChange={setBefore}
                load={choices}
                onFailure={props.onFailure}
                queryKey={props.kind + props.ownerId + "before"}
              />
              {props.fixedAfter ? (
                <p>
                  After is fixed to the submitted revision{" "}
                  <code>{props.fixedAfter}</code>.
                </p>
              ) : (
                <ChoiceField
                  label="After revision"
                  value={after}
                  onChange={setAfter}
                  load={choices}
                  onFailure={props.onFailure}
                  queryKey={props.kind + props.ownerId + "after"}
                />
              )}
              <button
                type="button"
                disabled={!before || !after}
                onClick={() => setSelection({ before, after })}
              >
                {controlIcons["compare"]} Compare selected revisions
              </button>
            </>
          )}
          {selection && (
            <LoadedComparison
              key={JSON.stringify(selection)}
              {...props}
              {...selection}
            />
          )}
        </>
      )}
    </section>
  );
}
export function semanticValue(value: unknown, depth = 0): unknown {
  if (depth > 64)
    throw new Error("Metadata exceeds the supported comparison depth.");
  if (Array.isArray(value))
    return value.map((item) => semanticValue(item, depth + 1));
  if (object(value))
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, semanticValue(value[key], depth + 1)]),
    );
  return value;
}
export function metadataChanges(before: Metadata, after: Metadata) {
  return [...new Set([...Object.keys(before), ...Object.keys(after)])]
    .sort()
    .filter(
      (key) =>
        JSON.stringify(semanticValue(before[key])) !==
        JSON.stringify(semanticValue(after[key])),
    )
    .map((key) => ({
      field: key,
      before: before[key],
      after: after[key],
      change:
        before[key] === undefined
          ? ("Added" as const)
          : after[key] === undefined
            ? ("Removed" as const)
            : ("Changed" as const),
    }));
}
export function ChangeLabel({
  change,
}: {
  change: "Added" | "Removed" | "Changed";
}) {
  return (
    <span
      className="state-badge"
      data-tone={
        change === "Added"
          ? "success"
          : change === "Removed"
            ? "warning"
            : "info"
      }
    >
      {change}:
    </span>
  );
}

/** Render bounded decoded field differences without loading documents or URLs. */
export function MetadataChanges({
  changes,
  beforeLabel = "Before",
  afterLabel = "After",
  emptyMessage = "No metadata changes.",
}: {
  changes: ReturnType<typeof metadataChanges>;
  beforeLabel?: string;
  afterLabel?: string;
  emptyMessage?: string;
}) {
  return (
    <>
      {!changes.length ? (
        <p>{emptyMessage}</p>
      ) : (
        changes.map((change) => (
          <section
            key={change.field}
            aria-label={`${change.change} ${change.field}`}
          >
            <h3>
              <ChangeLabel change={change.change} /> {change.field}
            </h3>
            <div className="revision-comparison-grid">
              <div>
                <h4>{beforeLabel}</h4>
                {change.before === undefined ? (
                  <p>Absent</p>
                ) : (
                  <JSONBlock value={change.before} />
                )}
              </div>
              <div>
                <h4>{afterLabel}</h4>
                {change.after === undefined ? (
                  <p>Absent</p>
                ) : (
                  <JSONBlock value={change.after} />
                )}
              </div>
            </div>
          </section>
        ))
      )}
    </>
  );
}

function LoadedComparison(props: Props & { before: string; after: string }) {
  const read = useRead(
    props.before + props.after,
    async (signal) => {
      const load = async (id: string): Promise<Compared> => {
        if (props.kind === "listing") {
          const revision = result(
            await props.client.api.GET(
              "/api/v1/listing-revisions/{revisionId}",
              { params: { path: { revisionId: id } }, signal },
            ),
            checks.ListingRevision,
          );
          if (
            revision.id !== id ||
            revision.listingId !== props.ownerId ||
            revision.tenantId !== props.access.tenantId
          )
            throw new Error("The selected Listing revision does not match.");
          const {
            id: _id,
            listingId: _listing,
            tenantId: _tenant,
            revisionNumber: _number,
            digest: _digest,
            createdAt: _at,
            createdBy: _actor,
            ...fields
          } = revision;
          return { id, fields, versionRevisionId: revision.versionRevisionId };
        }
        const bundle = result(
          await props.client.api.GET("/api/v1/version-revisions/{revisionId}", {
            params: { path: { revisionId: id } },
            signal,
          }),
          checks.RevisionEvidenceEnvelope,
        );
        if (
          bundle.revision.id !== id ||
          bundle.revision.resourceVersionId !== props.ownerId ||
          bundle.namespaceId !== props.namespaceId
        )
          throw new Error("The selected Version revision does not match.");
        const representations = bundle.revision.representations;
        return {
          id,
          fields: {
            representations: representations.map((item) => ({
              role: item.role,
              mediaType: item.mediaType,
              profileId: item.profileId,
              contentDigest: item.contentDigest,
              size: item.size,
              ...(item.url ? { url: item.url } : {}),
            })),
          },
          representations,
        };
      };
      const before = await load(props.before);
      const after = await load(props.after);
      return {
        before,
        after,
        changes: metadataChanges(before.fields, after.fields),
      };
    },
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data && (
        <div aria-label="Compared revisions">
          <p>
            Before <code>{read.data.before.id}</code> → After{" "}
            <code>{read.data.after.id}</code>.
          </p>
          <p className="muted">
            These are decoded metadata fields. Arrays keep their order. Inline
            numeric values may already be rounded; this is not a lossless
            document or archive comparison.
          </p>
          <MetadataChanges changes={read.data.changes} />
          {read.data.before.versionRevisionId &&
            read.data.after.versionRevisionId && (
              <LinkedVersionComparison
                {...props}
                before={read.data.before.versionRevisionId}
                after={read.data.after.versionRevisionId}
              />
            )}
          {read.data.before.representations &&
            read.data.after.representations && (
              <DocumentComparison
                {...props}
                before={read.data.before.representations}
                after={read.data.after.representations}
              />
            )}
        </div>
      )}
    </>
  );
}
function DocumentComparison(
  props: WorkspaceProps & { before: Representation[]; after: Representation[] },
) {
  const [left, setLeft] = useState(0);
  const [right, setRight] = useState(0);
  const [selected, setSelected] = useState<{
    before: Representation;
    after: Representation;
  }>();
  return (
    <section aria-label="Document comparison">
      <h3>Inspect document differences</h3>
      {(["Before", "After"] as const).map((side) => (
        <Field key={side} label={`${side} representation`}>
          <select
            value={side === "Before" ? left : right}
            onChange={(event) =>
              (side === "Before" ? setLeft : setRight)(
                Number(event.target.value),
              )
            }
          >
            {(side === "Before" ? props.before : props.after).map(
              (item, index) => (
                <option key={item.id} value={index}>
                  {item.role} / {item.mediaType} / {item.contentDigest}
                </option>
              ),
            )}
          </select>
        </Field>
      ))}
      <button
        type="button"
        disabled={!props.before.length || !props.after.length}
        onClick={() =>
          setSelected({
            before: props.before[left]!,
            after: props.after[right]!,
          })
        }
      >
        {controlIcons["compare"]} Inspect selected documents
      </button>
      {selected && (
        <SelectedDocuments
          key={JSON.stringify(selected)}
          {...props}
          {...selected}
        />
      )}
    </section>
  );
}
function SelectedDocuments(
  props: WorkspaceProps & { before: Representation; after: Representation },
) {
  const exact = !!props.before.artifactId && !!props.after.artifactId;
  const read = useRead(
    props.before.id + props.after.id,
    async (signal) => {
      if (!exact) return undefined;
      const load = async (id: string) => {
        const reply = await props.client.api.GET(
          "/api/v1/artifacts/{artifactId}/content",
          {
            params: { path: { artifactId: id } },
            parseAs: "arrayBuffer",
            signal,
          },
        );
        const bytes = result(reply);
        await verifyArtifact(bytes, reply.response, id);
        result(reply);
        if (bytes.byteLength > 2 * 1024 * 1024)
          throw new Error("This document exceeds the comparison text limit.");
        return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      };
      const before = await load(props.before.artifactId!);
      const after = await load(props.after.artifactId!);
      return { before, after };
    },
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {!exact && (
        <p>
          Lossless document comparison is unavailable for these representations.
          Decoded inline JSON may have rounded numbers. Retained Plugin/Skill
          inspection metadata cannot reconstruct an archive; no external archive
          is fetched.
        </p>
      )}
      {read.data && (
        <p>
          {read.data.before === read.data.after
            ? "Exact retained documents are identical."
            : "Exact retained documents differ, including any whitespace or numeric token changes."}
        </p>
      )}
      <div className="revision-comparison-grid">
        {(["before", "after"] as const).map((side) => (
          <div key={side}>
            <h4>{side === "before" ? "Before document" : "After document"}</h4>
            {read.data ? (
              <CodeBlock text={read.data[side]} original rawText />
            ) : (
              !exact && (
                <JSONBlock
                  value={
                    object(props[side].plugin)
                      ? props[side].plugin
                      : (props[side].package ??
                        props[side].data ?? {
                          contentDigest: props[side].contentDigest,
                          mediaType: props[side].mediaType,
                        })
                  }
                />
              )
            )}
          </div>
        ))}
      </div>
    </>
  );
}

/** Listing/review selections bind these native IDs; candidate movement cannot change them. */
function LinkedVersionComparison(
  props: WorkspaceProps & {
    namespaceId: string;
    before: string;
    after: string;
  },
) {
  const [open, setOpen] = useState(false);
  return (
    <section aria-label="Linked Version comparison">
      <h3>Linked Version revisions</h3>
      <p>
        Before <code>{props.before}</code>; after <code>{props.after}</code>.
        These are the selected Listing revisions' immutable native references.
      </p>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {controlIcons["compare"]}
        {open
          ? "Close linked Version comparison"
          : "Compare linked Version revisions"}
      </button>
      {open && <LinkedVersionDocuments {...props} />}
    </section>
  );
}
function LinkedVersionDocuments(
  props: WorkspaceProps & {
    namespaceId: string;
    before: string;
    after: string;
  },
) {
  const read = useRead(
    props.before + props.after,
    async (signal) => {
      const load = async (id: string) => {
        const bundle = result(
          await props.client.api.GET("/api/v1/version-revisions/{revisionId}", {
            params: { path: { revisionId: id } },
            signal,
          }),
          checks.RevisionEvidenceEnvelope,
        );
        if (
          bundle.revision.id !== id ||
          bundle.namespaceId !== props.namespaceId
        )
          throw new Error(
            "The linked native revision does not match this comparison.",
          );
        return bundle.revision.representations;
      };
      return {
        before: await load(props.before),
        after: await load(props.after),
      };
    },
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data && (
        <>
          <p>
            Representation roles, formats and digests below belong to these
            linked revisions. Document bytes still require an explicit read.
          </p>
          <div className="revision-comparison-grid">
            <div>
              <h4>Before representations</h4>
              <JSONBlock
                value={read.data.before.map(
                  ({ role, mediaType, profileId, contentDigest }) => ({
                    role,
                    mediaType,
                    profileId,
                    contentDigest,
                  }),
                )}
              />
            </div>
            <div>
              <h4>After representations</h4>
              <JSONBlock
                value={read.data.after.map(
                  ({ role, mediaType, profileId, contentDigest }) => ({
                    role,
                    mediaType,
                    profileId,
                    contentDigest,
                  }),
                )}
              />
            </div>
          </div>
          <DocumentComparison
            {...props}
            before={read.data.before}
            after={read.data.after}
          />
        </>
      )}
    </>
  );
}
