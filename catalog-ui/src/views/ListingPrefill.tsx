import { nativeIcons, type NativeIcon } from "./artwork";
import * as checks from "./responses";
// Listing suggestions reuse validated server facts, never raw-document extraction or automatic writes.
import { useEffect, useRef, useState } from "react";
import type { FetchResponse } from "openapi-fetch";
import type { operations } from "../api/schema";
import { APIError, result } from "../api/client";
import type { Client } from "../api/client";
import { Field } from "./workspace";

type Bundle = NonNullable<
  FetchResponse<
    operations["getVersionRevision"],
    object,
    "application/json"
  >["data"]
>;
export type ListingSuggestion = {
  name?: string | undefined;
  description?: string | undefined;
};

/** Offer only non-empty strings that fit the existing form; never truncate or copy other fields. */
export function listingSuggestion(
  facts?: Record<string, unknown>,
): ListingSuggestion {
  const bounded = (value: unknown, limit: number) =>
    typeof value === "string" &&
    value.trim() &&
    !value.includes("\0") &&
    [...value].length <= limit
      ? value
      : undefined;
  return {
    name: bounded(facts?.name, 160),
    description: bounded(facts?.description, 4000),
  };
}

/** Read evidence only on request and preview one representation before applying editable fields. */
export function ListingPrefill({
  client,
  revisionID,
  name,
  description,
  onApply,
  onFailure,
  onNativeIcons,
}: {
  client: Client;
  revisionID: string;
  name: string;
  description: string;
  onApply: (suggestion: ListingSuggestion) => void;
  onFailure: (error: unknown) => void;
  onNativeIcons?: (icons: NativeIcon[]) => void;
}) {
  const [reply, setReply] = useState<{ data: Bundle; response: Response }>();
  const [selected, setSelected] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef<AbortController | undefined>(undefined);
  useEffect(() => {
    setReply(undefined);
    setSelected("");
    setError("");
    setBusy(false);
    return () => request.current?.abort();
  }, [client, revisionID]);
  useEffect(() => {
    onNativeIcons?.([]);
  }, [revisionID, onNativeIcons]);
  const candidates =
    reply?.data.evidence.state === "complete"
      ? reply.data.revision.representations.filter(
          (item) => item.validationState === "valid",
        )
      : [];
  const representation = candidates.find((item) => item.id === selected);
  const suggestion = listingSuggestion(representation?.normalizedFacts);
  const fail = (cause: unknown) => {
    setError(
      cause instanceof APIError
        ? cause.message
        : "Could not load current suggestions. Try again.",
    );
    onFailure(cause);
  };
  return (
    <section className="workspace-panel" aria-label="Listing prefill">
      <h3>Use existing name and description</h3>
      <p>
        Load validated metadata from the selected version revision, choose a
        representation, then review the suggested fields. This does not save or
        approve a listing.
      </p>
      <button
        type="button"
        disabled={!revisionID || busy}
        onClick={() => {
          request.current?.abort();
          const controller = new AbortController();
          request.current = controller;
          setReply(undefined);
          onNativeIcons?.([]);
          setSelected("");
          setError("");
          setBusy(true);
          void client.api
            .GET("/api/v1/version-revisions/{revisionId}", {
              params: { path: { revisionId: revisionID } },
              signal: controller.signal,
            })
            .then((value) => {
              const data = result(value, checks.RevisionEvidenceEnvelope);
              if (!controller.signal.aborted)
                setReply({ data, response: value.response });
            })
            .catch((cause: unknown) => {
              if (!controller.signal.aborted) fail(cause);
            })
            .finally(() => {
              if (!controller.signal.aborted) setBusy(false);
            });
        }}
      >
        Load name and description suggestions
      </button>
      {!revisionID && <p className="muted">Select a version revision first.</p>}
      {busy && <p role="status">Loading suggestions...</p>}
      {error && <p role="alert">{error}</p>}
      {reply && (
        <>
          <p>
            Source version revision: <code>{revisionID}</code>
          </p>
          {candidates.length ? (
            <Field label="Prefill representation">
              <select
                value={selected}
                onChange={(event) => {
                  // Preview selection is not a draft edit; applying marks the parent form dirty.
                  event.stopPropagation();
                  setSelected(event.target.value);
                  onNativeIcons?.(
                    nativeIcons(
                      candidates.find((item) => item.id === event.target.value)
                        ?.data,
                    ),
                  );
                }}
              >
                <option value="">Select a representation</option>
                {candidates.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.role}: {item.mediaType} ({item.id})
                  </option>
                ))}
              </select>
            </Field>
          ) : (
            <p>
              No validated representation is available for prefill. Fill the
              listing manually or validate a corrected version.
            </p>
          )}
          {representation && (
            <>
              <dl className="workspace-kv">
                <dt>Suggested name</dt>
                <dd>
                  {suggestion.name ?? "Not supplied or too long for this field"}
                </dd>
                <dt>Suggested description</dt>
                <dd>
                  {suggestion.description ??
                    "Not supplied or too long for this field"}
                </dd>
              </dl>
              <p className="muted">
                Missing suggestions leave your existing fields unchanged.
                Publisher, visibility, rights and other fields are not copied.
              </p>
              <button
                type="button"
                disabled={!suggestion.name && !suggestion.description}
                onClick={() => {
                  try {
                    result(reply, checks.RevisionEvidenceEnvelope);
                  } catch (cause) {
                    setReply(undefined);
                    fail(cause);
                    return;
                  }
                  if (
                    ((suggestion.name && name && suggestion.name !== name) ||
                      (suggestion.description &&
                        description &&
                        suggestion.description !== description)) &&
                    !window.confirm(
                      "Replace the existing name and/or description with the displayed suggestions? Other listing fields will stay unchanged.",
                    )
                  )
                    return;
                  onApply(suggestion);
                }}
              >
                Use suggested name and description
              </button>
            </>
          )}
        </>
      )}
    </section>
  );
}
