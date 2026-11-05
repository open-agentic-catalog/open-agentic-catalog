// Exact source reads stage every representation before replacing any editor input.
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { permitsNamespace } from "../api/permissions";
import { object, page as checkedPage } from "../api/values";
import { ChoiceField } from "./authoring";
import { Field, type WorkspaceProps } from "./workspace";
import * as checks from "./responses";
import { reuseDraft } from "./draft-reuse";
import type { RepresentationDraft } from "./representations";

type Resource = components["schemas"]["Resource"];
export default function VersionSource(
  props: WorkspaceProps & {
    resource: Resource;
    initialRevision?: string | undefined;
    drafts: RepresentationDraft[];
    onApply: (drafts: RepresentationDraft[], revision: string) => void;
  },
) {
  const [browse, setBrowse] = useState(false);
  const [retained, setRetained] = useState(false);
  const [version, setVersion] = useState("");
  const [revision, setRevision] = useState(props.initialRevision ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const controller = useRef<AbortController | undefined>(undefined);
  const latest = useRef(props.drafts);
  latest.current = props.drafts;
  const readable = permitsNamespace(
    props.access,
    "resource.read_content",
    "catalog.read",
    props.resource.namespaceId,
  );
  const history = permitsNamespace(
    props.access,
    "resource.read_history",
    "catalog.read",
    props.resource.namespaceId,
  );
  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    if (!readable) controller.current?.abort();
  }, [readable]);
  const cancelRead = () => {
    controller.current?.abort();
    setBusy(false);
  };
  const selectRevision = (id: string) => {
    cancelRead();
    setRevision(id);
  };
  const load = async (id: string) => {
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    const before = latest.current;
    setBusy(true);
    setError("");
    try {
      const bundle = result(
        await props.client.api.GET("/api/v1/version-revisions/{revisionId}", {
          params: { path: { revisionId: id } },
          signal: request.signal,
        }),
        (value) =>
          checks.RevisionEvidenceEnvelope(value) &&
          object(value) &&
          object(value.revision) &&
          value.namespaceId === props.resource.namespaceId &&
          value.revision.id === id &&
          value.revision.tenantId === props.resource.tenantId,
      );
      if (request.signal.aborted) return;
      // A revision has no Resource ID: verify its parent before adopting it.
      result(
        await props.client.api.GET("/api/v1/resource-versions/{versionId}", {
          params: { path: { versionId: bundle.revision.resourceVersionId } },
          signal: request.signal,
        }),
        (value) =>
          checks.ResourceVersion(value) &&
          object(value) &&
          value.id === bundle.revision.resourceVersionId &&
          value.resourceId === props.resource.id &&
          value.tenantId === props.resource.tenantId,
      );
      if (request.signal.aborted) return;
      const drafts = await reuseDraft(
        props.client,
        props.resource.kind,
        bundle.revision,
        request.signal,
        retained,
      );
      if (request.signal.aborted) return;
      if (latest.current !== before)
        throw new Error(
          "Your content changed during the read. Load the source again to replace it.",
        );
      props.onApply(drafts, id);
    } catch (cause) {
      if (!request.signal.aborted) {
        setError(
          cause instanceof Error ? cause.message : "Could not load the source.",
        );
        props.onFailure(cause);
      }
    } finally {
      if (!request.signal.aborted) setBusy(false);
    }
  };
  const initial = useRef(props.initialRevision);
  useEffect(() => {
    if (!readable || !initial.current) return;
    const id = initial.current;
    initial.current = undefined;
    void load(id);
    // The initial exact source is read once. Subsequent replacement is explicit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readable]);
  return (
    <section
      className="workspace-panel"
      aria-label="Start from an existing revision"
    >
      <h3>Start from an existing revision</h3>
      <p>
        Copy retained content into this new Version. Sealed source content stays
        unchanged. Enter a new Version label below.
      </p>
      {history && (
        <button
          type="button"
          onClick={() => setBrowse((value) => !value)}
          aria-expanded={browse}
        >
          Browse source history
        </button>
      )}
      {history && browse && (
        <>
          <ChoiceField
            label="Source Version"
            required={false}
            value={version}
            onChange={(id) => {
              cancelRead();
              setVersion(id);
              setRevision("");
            }}
            onFailure={props.onFailure}
            load={async (cursor, signal) => {
              const page = result(
                await props.client.api.GET(
                  "/api/v1/resources/{resourceId}/versions",
                  {
                    params: {
                      path: { resourceId: props.resource.id },
                      query: { limit: 25, ...(cursor ? { cursor } : {}) },
                    },
                    signal,
                  },
                ),
                checkedPage(checks.ResourceVersion),
              );
              if (
                page.items.length > 25 ||
                page.items.some(
                  (item) =>
                    item.resourceId !== props.resource.id ||
                    item.tenantId !== props.resource.tenantId,
                )
              )
                throw new Error(
                  "The Version history does not match this Resource.",
                );
              return {
                ...page,
                items: page.items.map((item) => ({
                  id: item.id,
                  label: `${item.version}${item.sealedRevisionId ? " (sealed)" : " (draft)"}`,
                })),
              };
            }}
          />
          {version && (
            <ChoiceField
              key={version}
              label="Source Revision"
              required={false}
              value={revision}
              onChange={selectRevision}
              onFailure={props.onFailure}
              load={async (cursor, signal) => {
                const page = result(
                  await props.client.api.GET(
                    "/api/v1/resource-versions/{versionId}/revisions",
                    {
                      params: {
                        path: { versionId: version },
                        query: { limit: 25, ...(cursor ? { cursor } : {}) },
                      },
                      signal,
                    },
                  ),
                  checkedPage(checks.RevisionSummary),
                );
                return {
                  ...page,
                  items: page.items.map((item) => ({
                    id: item.id,
                    label: `Revision ${item.revisionNumber} by ${item.createdBy}`,
                  })),
                };
              }}
            />
          )}
        </>
      )}
      <Field
        label="Exact source revision ID"
        hint="Use an exact retained revision when history browsing is unavailable."
      >
        <input
          value={revision}
          onChange={(event) => selectRevision(event.target.value)}
        />
      </Field>
      <label>
        <input
          type="checkbox"
          checked={retained}
          onChange={(event) => {
            cancelRead();
            setRetained(event.target.checked);
          }}
        />{" "}
        Use retained document bytes instead of HTTPS references, where available
      </label>
      <button
        type="button"
        disabled={!readable || !revision || busy}
        onClick={() => {
          if (
            props.drafts.some(
              (item) =>
                item.content ||
                item.url ||
                item.packageData ||
                item.documentStaged ||
                item.pluginPreparation,
            ) &&
            !window.confirm(
              "Replace the current content and staged files with this exact revision?",
            )
          )
            return;
          void load(revision);
        }}
      >
        Load source revision
      </button>
      {busy && (
        <>
          <p role="status">Loading complete source content...</p>
          <button type="button" onClick={cancelRead}>
            Cancel source read
          </button>
        </>
      )}
      {error && <p role="alert">{error} Current input stays unchanged.</p>}
      <p className="muted">
        Skill packages need the original archive and handoff. Plugin inspection
        retains selected documents, not a complete archive. HTTPS references are
        fetched again when saved.
      </p>
    </section>
  );
}
