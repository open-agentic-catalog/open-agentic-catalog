import { ScrollableTable } from "./ScrollableTable";
// Membership comes from the selected retained document, never a staged audience or directory lookup.
import { useId, useState } from "react";
import { compareTableText, usePageSort } from "./table-sort";
import { Pager } from "./workspace";
import type { components } from "../api/schema";

type Entry = { identifier: string; type: string; version?: string };
export function PublicationContents({
  snapshot,
}: {
  snapshot: Pick<
    components["schemas"]["PublicationSnapshot"],
    "ard" | "aiCatalog" | "ardProfile" | "aiCatalogProfile"
  >;
}) {
  const registry = snapshot.ard.entries.filter(
    (entry) => entry.metadata?.catalogReleaseId === "registry:self",
  ).length;
  return (
    <section aria-label="Included discovery entries">
      <h3>Included discovery entries</h3>
      <dl className="workspace-kv">
        <dt>ARD profile</dt>
        <dd>{snapshot.ardProfile}</dd>
        <dt>ARD Registry self-entries</dt>
        <dd>{registry}</dd>
        <dt>ARD resource entries</dt>
        <dd>{snapshot.ard.entries.length - registry}</dd>
        <dt>AI Catalog profile</dt>
        <dd>{snapshot.aiCatalogProfile}</dd>
        <dt>AI Catalog resource entries</dt>
        <dd>{snapshot.aiCatalog.entries.length}</dd>
      </dl>
      <p>
        Counts describe entries in these retained documents, not unique
        Resources. ARD's generated Registry self-entry is separate from resource
        entries. Profiles can include different populations. No reported
        exclusions does not establish completeness, approval or trust.
      </p>
      <EntryInventory name="ARD" entries={snapshot.ard.entries} />
      <EntryInventory name="AI Catalog" entries={snapshot.aiCatalog.entries} />
    </section>
  );
}

function EntryInventory({ name, entries }: { name: string; entries: Entry[] }) {
  const sortHint = useId();
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState(0);
  const sorted = usePageSort(
    entries.map((entry, sourceIndex) => ({ ...entry, sourceIndex })),
    {
      identifier: (a, b) => compareTableText(a.identifier, b.identifier),
      version: (a, b) =>
        a.version === undefined
          ? b.version === undefined
            ? 0
            : 1
          : b.version === undefined
            ? -1
            : compareTableText(a.version, b.version),
      media: (a, b) => compareTableText(a.type, b.type),
    },
    () => setPage(0),
  );
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        Inspect {name} entries ({entries.length})
      </summary>
      {open &&
        (entries.length ? (
          <>
            <p id={sortHint} className="muted">
              Select a column heading to sort all entries in this document.
            </p>
            <ScrollableTable label={`${name} included entries`}>
              <table
                className="workspace-table"
                aria-label={`${name} included entries`}
                aria-describedby={sortHint}
              >
                <thead>
                  <tr>
                    {sorted.heading("Identifier", "identifier")}
                    {sorted.heading("Version", "version")}
                    {sorted.heading("Media type", "media")}
                  </tr>
                </thead>
                <tbody>
                  {sorted.rows
                    .slice(page * 25, (page + 1) * 25)
                    .map((entry) => (
                      <tr key={entry.sourceIndex}>
                        <td>
                          <code>{entry.identifier}</code>
                        </td>
                        <td>
                          {entry.version === undefined
                            ? "No version supplied"
                            : entry.version || "(empty)"}
                        </td>
                        <td>{entry.type}</td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </ScrollableTable>
            <Pager
              label={`${name} included entries pagination`}
              cursor={page ? String(page) : ""}
              nextCursor={
                (page + 1) * 25 < entries.length ? String(page + 1) : undefined
              }
              onPage={(value) => setPage(Number(value || 0))}
            />
          </>
        ) : (
          <p>No {name} entries in this document.</p>
        ))}
    </details>
  );
}
