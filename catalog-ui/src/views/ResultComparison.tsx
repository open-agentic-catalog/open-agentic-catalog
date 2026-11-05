import { Facts } from "./Facts";
import { KindBadge } from "./KindBadge";
// Compare only the two explicitly selected, already loaded result summaries.
import { useEffect, useId, useRef } from "react";
import type { Access, SearchResult } from "../api/client";
import { originLabel } from "./CatalogPresentation";
import { IndexedProvenance } from "./Handoff";
import { RatingLabels } from "./Ratings";
import { CopyReference } from "./CopyReference";

export function comparisonIdentity(item: SearchResult) {
  return JSON.stringify([
    item.id,
    item.type,
    item.identifier,
    item.kind,
    item.versionPresent,
    item.version,
    item.local?.release.id,
    item.indexed?.presentationSourceId,
    item.indexed?.presentationGeneration,
    item.indexed?.normalizedEntryDigest,
    item.live?.sourceId,
    item.live?.presentationGeneration,
    item.live?.sourceRevision,
  ]);
}

export default function ResultComparison({
  items,
  close,
  returnFocus,
  access,
  basePath,
}: {
  items: SearchResult[];
  close: () => void;
  returnFocus: string;
  access: Access | undefined;
  basePath: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const title = useId();
  useEffect(() => {
    const element = dialog.current!;
    const selectedResult = document.getElementById(returnFocus);
    element.showModal();
    return () => {
      element.close();
      if (selectedResult?.isConnected) selectedResult.focus();
    };
  }, [returnFocus]);
  return (
    <dialog
      ref={dialog}
      className="detail-dialog result-comparison"
      aria-labelledby={title}
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        close();
      }}
    >
      <div className="dialog-toolbar">
        <h2 id={title}>Compare selected resources</h2>
        <button onClick={close}>Close comparison</button>
      </div>
      <p>
        These summaries come from one result page. Comparison does not establish
        approval, safety or permission to execute.
      </p>
      <div className="comparison-grid">
        {items.map((item) => (
          <section key={comparisonIdentity(item)} className="workspace-panel">
            <h3>{item.displayName}</h3>
            <Facts
              rows={[
                ["Kind", <KindBadge kind={item.kind} />],
                ["Origin", originLabel(item.type)],
                [
                  "Version",
                  item.versionPresent
                    ? item.version || "Empty version"
                    : "No version supplied",
                ],
                ["Description", item.description || "Not supplied"],
                [
                  "Publisher",
                  item.local?.display?.publisherName ||
                    item.publisher ||
                    "Not supplied",
                ],
                [
                  "License",
                  item.local?.display?.license ||
                    "Not supplied in this summary",
                ],
                [
                  "Governance",
                  item.local
                    ? `Local release: ${item.local.release.state}`
                    : "External metadata; no local review or approval",
                ],
                [
                  "Interfaces and authentication",
                  "Not assessed by this summary. Inspect details explicitly.",
                ],
                [
                  "Tags",
                  item.tags.length
                    ? item.tags.map((tag, index) => (
                        <span className="badge" key={index}>
                          {tag}
                        </span>
                      ))
                    : "Not supplied",
                ],
              ]}
            />
            <CopyReference label="Catalog identifier" value={item.identifier} />
            {item.publisher && (
              <CopyReference
                label="Publisher identifier"
                value={item.publisher}
              />
            )}
            {item.local && (
              <CopyReference label="Release ID" value={item.local.release.id} />
            )}
            {item.live && (
              <p className="notice">
                Transient live result. Source metadata can change; this is not a
                retained local record.
              </p>
            )}
            <IndexedProvenance
              item={item}
              access={access}
              basePath={basePath}
            />
            <RatingLabels item={item} basePath={basePath} />
          </section>
        ))}
      </div>
    </dialog>
  );
}
