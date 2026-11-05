import type { components } from "../api/schema";
import { ListingMetadata } from "./listing-metadata";
import { MetadataChanges, metadataChanges } from "./RevisionComparison";

type Input = components["schemas"]["CreateListingRevision"];
type Saved = components["schemas"]["ListingRevision"];

// Every closed request field participates; compiler drift must not hide a field.
const fields = {
  versionRevisionId: true,
  displayName: true,
  description: true,
  publisherId: true,
  visibility: true,
  tags: true,
  categories: true,
  representativeQueries: true,
  documentationUrl: true,
  supportUrl: true,
  termsUrl: true,
  license: true,
  relationships: true,
  extensions: true,
} satisfies Record<keyof Input, true>;

function inputFields(value: Input) {
  return {
    ...Object.fromEntries(
      Object.keys(fields).flatMap((key) => {
        const item = value[key as keyof Input];
        return item === undefined ? [] : [[key, item]];
      }),
    ),
    tags: value.tags ?? [],
    categories: value.categories ?? [],
    representativeQueries: value.representativeQueries ?? [],
    relationships: value.relationships ?? [],
    extensions: value.extensions ?? {},
  };
}

/** Compute during preparation so depth errors remain recoverable form errors. */
export function listingRevisionChanges(before: Saved | null, after: Input) {
  return metadataChanges(before ? inputFields(before) : {}, inputFields(after));
}

/** Review only captured, bounded metadata; all URLs remain inert text. */
export function ListingRevisionReview({
  changes,
  proposed,
}: {
  changes: ReturnType<typeof listingRevisionChanges>;
  proposed: Input;
}) {
  const advanced = new Set(["relationships", "extensions"]);
  return (
    <section aria-label="Prepared listing changes">
      <h3>Review saved and proposed metadata</h3>
      <p>
        These are the captured request fields. Removed values are shown as
        absent. Arrays keep their order; this is not a native document or
        representation comparison. Artwork is included in extensions; the server
        revalidates and seals it when saving.
      </p>
      <MetadataChanges
        changes={changes.filter((change) => !advanced.has(change.field))}
        beforeLabel="Saved"
        afterLabel="Proposed"
        emptyMessage="No basic field changes."
      />
      <details>
        <summary>Review relationships, extensions and artwork</summary>
        <p>
          These values are inert metadata. Opening this view fetches no URLs.
        </p>
        <MetadataChanges
          changes={changes.filter((change) => advanced.has(change.field))}
          beforeLabel="Saved"
          afterLabel="Proposed"
          emptyMessage="No relationship, extension or artwork changes."
        />
      </details>
      <details>
        <summary>Preview proposed listing metadata</summary>
        <ListingMetadata revision={proposed} />
      </details>
    </section>
  );
}
