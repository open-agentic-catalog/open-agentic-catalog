import { InputError } from "./input-validation";
// Listing relationship editing keeps browser row identity separate from retained contract fields.
import { useLayoutEffect, useRef } from "react";
import type { components } from "../api/schema";
import { Field } from "./workspace";

type Relationship = components["schemas"]["Relationship"];
const relationshipLabels: Record<Relationship["type"], string> = {
  depends_on: "Depends on",
  recommended_with: "Recommended with",
  supersedes: "Supersedes",
  bundled_with: "Bundled with",
};
export type RelationshipDraft = Relationship & { editorID: string };

/** Give a relationship row a stable editor identity while retaining existing contract fields. */
export function relationshipDraft(value?: Relationship): RelationshipDraft {
  return {
    editorID: crypto.randomUUID(),
    type: "depends_on",
    targetIdentifier: "",
    ...value,
  };
}

/** Serialize only contract fields, with no silent truncation or deduplication. */
export function relationshipInputs(
  drafts: RelationshipDraft[],
): Relationship[] {
  if (drafts.length > 64)
    throw new Error("Include no more than 64 relationships.");
  const seen = new Set<string>();
  return drafts.map((draft) => {
    const targetIdentifier = draft.targetIdentifier.trim();
    const targetVersion = draft.targetVersion?.trim();
    if (draft.type === "supersedes" && !targetVersion)
      throw new InputError(
        "A supersedes relationship requires an exact target version.",
        `${draft.editorID}-version`,
      );
    if (
      !Object.hasOwn(relationshipLabels, draft.type) ||
      !targetIdentifier ||
      [...targetIdentifier].length > 512 ||
      [...(targetVersion ?? "")].length > 128
    )
      throw new Error(
        "Each relationship needs a supported type and a target identifier of up to 512 characters; target versions may contain up to 128 characters.",
      );
    const result = {
      type: draft.type,
      targetIdentifier,
      ...(targetVersion ? { targetVersion } : {}),
    };
    const key = JSON.stringify(result);
    if (seen.has(key))
      throw new InputError(
        "Remove the duplicate relationship before saving.",
        `${draft.editorID}-target`,
      );
    seen.add(key);
    return result;
  });
}

/** Require an explicit retained or newly selected version revision before preparing a listing. */
export function listingVersionRevision(
  keepCurrent: boolean,
  current: string | undefined,
  selected: string,
) {
  const revision = keepCurrent ? current : selected;
  if (!revision) throw new Error("Select a version revision for this listing.");
  return revision;
}

/** Check all retained listing/version identities before displaying submitted review evidence. */
export function submittedListingMatches(
  submission: Pick<
    components["schemas"]["ReviewSubmission"],
    "listingRevisionId" | "listingId" | "versionRevisionId"
  >,
  listing: Pick<
    components["schemas"]["ListingRevision"],
    "id" | "listingId" | "versionRevisionId"
  >,
) {
  return (
    listing.id === submission.listingRevisionId &&
    listing.listingId === submission.listingId &&
    listing.versionRevisionId === submission.versionRevisionId
  );
}

/** Edit bounded relationship rows and restore keyboard focus after addition or removal. */
export function RelationshipEditor({
  value,
  onChange,
}: {
  value: RelationshipDraft[];
  onChange: (value: RelationshipDraft[]) => void;
}) {
  const container = useRef<HTMLElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const focusRow = useRef<number | null>(null);
  useLayoutEffect(() => {
    if (focusRow.current === null) return;
    const control = container.current?.querySelectorAll<HTMLSelectElement>(
      ".relationship-row select",
    )[focusRow.current];
    (control || addButton.current)?.focus();
    focusRow.current = null;
  }, [value]);
  return (
    <section
      ref={container}
      className="relationship-editor"
      aria-label="Listing relationships"
    >
      <h3>Relationships</h3>
      <p className="muted">
        Use a Resource identifier from this tenant Catalog and, when specified,
        an existing version label. Supersedes requires an exact target version.
        These references do not install or execute anything.
      </p>
      {!value.length && <p className="muted">No relationships added.</p>}
      {value.map((item, index) => (
        <fieldset key={item.editorID} className="relationship-row">
          <legend>Relationship {index + 1}</legend>
          <Field label="Relationship type">
            <select
              value={item.type}
              onChange={(e) =>
                onChange(
                  value.map((row) =>
                    row.editorID === item.editorID
                      ? { ...row, type: e.target.value as Relationship["type"] }
                      : row,
                  ),
                )
              }
            >
              {!Object.hasOwn(relationshipLabels, item.type) && (
                <option value={item.type}>
                  {item.type || "Select relationship type"}
                </option>
              )}
              {Object.entries(relationshipLabels).map(([key, label]) => (
                <option key={key} value={key}>
                  {label}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label="Target identifier"
            hint="The target resource's canonical identifier, not its database UUID or display name. Private resources may use opaque identifiers."
          >
            <input
              required
              maxLength={512 * 2}
              id={`${item.editorID}-target`}
              value={item.targetIdentifier}
              placeholder="urn:air:example.com:agents:related-agent"
              onChange={(e) =>
                onChange(
                  value.map((row) =>
                    row.editorID === item.editorID
                      ? { ...row, targetIdentifier: e.target.value }
                      : row,
                  ),
                )
              }
            />
          </Field>
          <Field
            label={
              item.type === "supersedes"
                ? "Target version (required)"
                : "Target version (optional)"
            }
          >
            <input
              required={item.type === "supersedes"}
              maxLength={128 * 2}
              id={`${item.editorID}-version`}
              value={item.targetVersion || ""}
              placeholder="1.0.0"
              onChange={(e) =>
                onChange(
                  value.map((row) =>
                    row.editorID === item.editorID
                      ? { ...row, targetVersion: e.target.value }
                      : row,
                  ),
                )
              }
            />
          </Field>
          <button
            type="button"
            aria-label={`Remove relationship ${index + 1}`}
            onClick={() => {
              focusRow.current = Math.min(index, value.length - 2);
              onChange(value.filter((row) => row.editorID !== item.editorID));
            }}
          >
            Remove relationship
          </button>
        </fieldset>
      ))}
      <button
        ref={addButton}
        type="button"
        disabled={value.length >= 64}
        onClick={() => {
          focusRow.current = value.length;
          onChange([...value, relationshipDraft()]);
        }}
      >
        Add relationship
      </button>
    </section>
  );
}

/** Render retained metadata as text, never HTML, embedded content or fetched URLs. */
export function ListingMetadata({
  revision,
}: {
  revision:
    | components["schemas"]["CreateListingRevision"]
    | components["schemas"]["ListingRevision"];
}) {
  return (
    <div className="listing-metadata">
      <div className="workspace-toolbar">
        {"revisionNumber" in revision && (
          <span className="badge">Revision {revision.revisionNumber}</span>
        )}
        <span className="badge">{revision.visibility}</span>
      </div>
      <h2>{revision.displayName}</h2>
      <p className="listing-description">{revision.description}</p>
      {!!revision.tags?.length && (
        <div className="listing-tags" aria-label="Tags">
          {revision.tags.map((tag) => (
            <span className="badge" key={tag}>
              {tag}
            </span>
          ))}
        </div>
      )}
      <dl>
        <div>
          <dt>Publisher ID</dt>
          <dd>
            <code>{revision.publisherId}</code>
          </dd>
        </div>
        {!!revision.categories?.length && (
          <div>
            <dt>Categories</dt>
            <dd>{revision.categories.join(", ")}</dd>
          </div>
        )}
        {(
          [
            ["License", revision.license],
            ["Documentation URL", revision.documentationUrl],
            ["Support URL", revision.supportUrl],
            ["Terms URL", revision.termsUrl],
          ] as const
        ).map(([label, value]) =>
          value ? (
            <div key={label}>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ) : null,
        )}
      </dl>
      {!!revision.representativeQueries?.length && (
        <>
          <h3>Representative queries</h3>
          <ul>
            {revision.representativeQueries.map((query) => (
              <li key={query}>{query}</li>
            ))}
          </ul>
        </>
      )}
      {!!revision.relationships?.length && (
        <>
          <h3>Relationships</h3>
          <ul>
            {revision.relationships.map((relation, index) => (
              <li key={index}>
                {relationshipLabels[relation.type]}{" "}
                <code>{relation.targetIdentifier}</code>
                {relation.targetVersion && <> / {relation.targetVersion}</>}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
