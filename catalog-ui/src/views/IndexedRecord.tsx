import { Facts } from "./Facts";
import { KindBadge } from "./KindBadge";
import { SaveResource } from "./Personal";
import { RatingEntry, RatingLabels, requestForResult } from "./Ratings";
import { SourceArtworkControls } from "./SourceArtwork";
import { ArtworkGallery } from "./Artwork";
import * as checks from "./responses";
// External record bookmarks resolve a current indexed projection rather than immutable governance evidence.
import { result } from "../api/client";
import { object } from "../api/values";
import { ExternalHandoff, IndexedProvenance } from "./Handoff";
import { EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import { Heading, confirmDiscard } from "./workspace";
import { CurrentRead, useCurrentRead } from "./current-read";
import { RecordNavigation, externalRecordSections } from "./RecordNavigation";

/** Resolve current indexed metadata with explicit absent-versus-empty version semantics. */
export default function IndexedRecord(
  props: WorkspaceProps & {
    restoreSearch?: boolean;
    search?: string;
    returnPath?: string;
    returnLabel?: string;
  },
) {
  const query = new URLSearchParams(props.search ?? location.search);
  const read = useCurrentRead(
    query.toString(),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/catalog/indexed-record", {
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
        (value) =>
          checks.CatalogSearchResult(value) &&
          object(value) &&
          value.type === "indexed" &&
          value.identifier === (query.get("identifier") || "") &&
          value.versionPresent === (query.get("versionPresent") === "true") &&
          // The shared predicate already refuses a version when presence is false.
          (query.get("versionPresent") !== "true" ||
            (value.version ?? "") === query.get("version")),
      ),
    props.onFailure,
  );
  return (
    <>
      <Heading
        title="External catalog record"
        icon="globe"
        description="Inspect an external resource and its source."
      />
      <a href={props.returnPath ?? `${props.runtime.basePath}/catalog`}>
        Back to {props.returnLabel ?? "Catalog"}
      </a>
      <p className="muted">
        {props.restoreSearch
          ? "Returning in this tab restores your search; reloading starts a fresh search."
          : "Returning to Catalog starts a fresh search."}
      </p>
      <CurrentRead
        read={read}
        label="indexed record"
        reload="Reload indexed record"
        refresh={() => {
          if (confirmDiscard()) read.refresh();
        }}
      />
      <p className="muted">
        Reload the saved external entry. Reload does not query the source live;
        its saved contents may have changed. If reload fails, the last loaded
        entry stays visible below.
      </p>
      {read.data && (
        <section className="workspace-panel" aria-busy={read.busy}>
          <h2>{read.data.displayName}</h2>
          <p>{read.data.description || "No description supplied."}</p>
          <RecordNavigation sections={externalRecordSections} />
          <h3
            className="section-heading record-section-target"
            id="record-overview"
            tabIndex={-1}
          >
            Overview
          </h3>
          <Facts
            rows={[
              ["Kind", <KindBadge kind={read.data.kind} />],
              [
                "Version",
                read.data.versionPresent
                  ? read.data.version || "Empty version"
                  : "No version supplied",
              ],
              ["Publisher", read.data.publisher || "Not supplied"],
            ]}
          />
          <p className="muted">
            This link shows current source metadata. This bookmark is not an
            immutable approval or permission to execute the resource.
          </p>
          <p className="notice">
            External metadata is not a locally reviewed or approved resource.
          </p>
          <SaveResource
            {...props}
            basePath={props.runtime.basePath}
            label={read.data.displayName}
            item={read.data}
          />
          <IndexedProvenance
            item={read.data}
            access={props.access}
            basePath={props.runtime.basePath}
            headingId="record-provenance"
          />
          <ArtworkGallery
            client={props.client}
            item={read.data}
            onFailure={props.onFailure}
          />
          <SourceArtworkControls
            client={props.client}
            access={props.access}
            item={read.data}
            onFailure={props.onFailure}
          />
          <RatingLabels item={read.data} basePath={props.runtime.basePath} />
          <RatingEntry
            client={props.client}
            access={props.access}
            onFailure={props.onFailure}
            request={requestForResult(read.data)}
          />
          <ExternalHandoff
            key={read.data.id}
            item={read.data}
            headingId="record-handoff"
            documentationUrl={props.runtime.documentationUrl}
          />
          <p className="muted">
            This link becomes unavailable if the source is paused or removed, or
            if the entry is withdrawn or excluded. Live query results remain
            transient and have no durable record link.
          </p>
          <h3
            className="section-heading record-section-target"
            id="record-evidence"
            tabIndex={-1}
          >
            Technical details and evidence
          </h3>
          <Facts
            rows={[
              [
                "Entry digest (not a document checksum)",
                <code>{read.data.indexed?.normalizedEntryDigest}</code>,
              ],
            ]}
          />
          <EvidenceDetails
            title="Current indexed projection"
            value={read.data}
          />
          <a
            className="back-link"
            href={props.returnPath ?? `${props.runtime.basePath}/catalog`}
          >
            {props.returnLabel === "saved search"
              ? "Return to saved search"
              : "Return to results"}
          </a>
        </section>
      )}
    </>
  );
}
