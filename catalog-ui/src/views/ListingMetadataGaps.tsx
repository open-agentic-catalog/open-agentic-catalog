import { useState } from "react";
import type { components } from "../api/schema";
import { result, type Kind } from "../api/client";
import { availablePermission, permitsNamespace } from "../api/permissions";
import { kindChoices, kindLabel } from "../resource-kinds";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  type WorkspaceProps,
} from "./workspace";
import { useNamespaceAccess } from "./namespace-access";
import { listingMetadataGapPage } from "./responses";
import { ScrollableTable } from "./ScrollableTable";

type Row = components["schemas"]["ListingMetadataGap"];
type Check = Row["missing"][number];
const labels = {
  documentation: "Documentation URL",
  support: "Support URL",
  license: "License declaration",
} satisfies Record<Check, string>;
const fields = Object.keys(labels) as Check[];
const initial = { checks: fields, kind: "" as Kind | "", namespaceId: "" };

export default function ListingMetadataGaps(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Missing Listing metadata"
        icon="filters"
        description="Find current releases missing an optional documentation URL, support URL or license declaration."
      />
      {availablePermission(props.access, "resource.read", "catalog.read") ? (
        <Inventory key={JSON.stringify(props.access)} {...props} />
      ) : (
        <p role="alert">
          Reading this report requires Resource metadata access.
        </p>
      )}
    </>
  );
}

function Inventory(props: WorkspaceProps) {
  const [applied, setApplied] = useState(initial);
  const [draft, setDraft] = useState(initial);
  const [cursor, setCursor] = useState("");
  const read = useRead(
    JSON.stringify([applied, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/listing-metadata-gaps", {
          params: {
            query: {
              checks: applied.checks.join(","),
              limit: 20,
              ...(applied.kind && { kind: applied.kind }),
              ...(applied.namespaceId && { namespaceId: applied.namespaceId }),
              ...(cursor && { cursor }),
            },
          },
          signal,
        }),
        listingMetadataGapPage,
      ),
    props.onFailure,
  );
  const refresh = () => (cursor ? setCursor("") : read.refresh());
  const pending = JSON.stringify(draft) !== JSON.stringify(applied);
  return (
    <>
      <form
        className="workspace-form"
        aria-label="Filter missing Listing metadata"
        onSubmit={(event) => {
          event.preventDefault();
          if (!draft.checks.length) return;
          if (!pending && !cursor) read.refresh();
          setApplied(draft);
          setCursor("");
        }}
      >
        <fieldset>
          <legend>Show Listings missing any selected field</legend>
          {fields.map((field) => (
            <Field key={field} label={labels[field]}>
              <input
                type="checkbox"
                checked={draft.checks.includes(field)}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    checks: fields.filter((name) =>
                      name === field
                        ? event.target.checked
                        : draft.checks.includes(name),
                    ),
                  })
                }
              />
            </Field>
          ))}
        </fieldset>
        <Field label="Resource kind">
          <select
            value={draft.kind}
            onChange={(event) =>
              setDraft({ ...draft, kind: event.target.value as Kind | "" })
            }
          >
            <option value="">All kinds</option>
            {kindChoices.map((kind) => (
              <option value={kind.value} key={kind.value}>
                {kind.label}
              </option>
            ))}
          </select>
        </Field>
        <Field
          label="Namespace ID"
          help="Leave blank to include all readable namespaces."
        >
          <input
            type="text"
            value={draft.namespaceId}
            pattern="[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}"
            onChange={(event) =>
              setDraft({ ...draft, namespaceId: event.target.value })
            }
          />
        </Field>
        {!draft.checks.length ? (
          <p role="alert">Select at least one field.</p>
        ) : (
          pending && (
            <p className="muted">Apply filters to update the results.</p>
          )
        )}
        <div className="workspace-toolbar">
          <button type="submit" disabled={!draft.checks.length || read.busy}>
            Apply filters
          </button>
          <button type="button" onClick={refresh} disabled={read.busy}>
            Refresh from first page
          </button>
        </div>
      </form>
      <p>
        These fields are optional. Saving a draft leaves the released fields
        unchanged. A correction clears the omission when its revision becomes
        the current release. Refresh from the first page when releases change.
      </p>
      <ReadState {...read} refresh={refresh} />
      {read.data &&
        (read.data.items.length ? (
          <ScrollableTable label="Released Listings missing metadata">
            <table className="workspace-table">
              <thead>
                <tr>
                  <th>Released Listing</th>
                  <th>Missing fields</th>
                  <th>References</th>
                </tr>
              </thead>
              <tbody>
                {read.data.items.map((row) => (
                  <ListingRow key={row.listingId} row={row} {...props} />
                ))}
              </tbody>
            </table>
          </ScrollableTable>
        ) : (
          <p className="workspace-empty">
            No current releases are missing the selected fields.
          </p>
        ))}
      <Pager
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        onPage={setCursor}
        busy={read.busy}
      />
    </>
  );
}

function ListingRow({ row, ...props }: WorkspaceProps & { row: Row }) {
  const [expanded, setExpanded] = useState(false);
  const access = useNamespaceAccess(props, expanded ? row.namespaceId : "");
  const content = permitsNamespace(
    access,
    "resource.read_content",
    "catalog.read",
    row.namespaceId,
  );
  const edit =
    content &&
    permitsNamespace(
      access,
      "resource.read_history",
      "catalog.read",
      row.namespaceId,
    ) &&
    permitsNamespace(
      access,
      "resource.read",
      "catalog.read",
      row.namespaceId,
    ) &&
    permitsNamespace(access, "resource.edit", "catalog.write", row.namespaceId);
  const base = props.runtime.basePath;
  return (
    <tr>
      <td>
        <a href={`${base}/catalog/${row.releaseId}`}>{row.displayName}</a>
        <br />
        {kindLabel(row.kind)} / {row.version} / {row.state}
      </td>
      <td>{row.missing.map((field) => labels[field]).join(", ")}</td>
      <td>
        <details onToggle={(event) => setExpanded(event.currentTarget.open)}>
          <summary>Release references</summary>
          <dl>
            <dt>Namespace</dt>
            <dd>{row.namespaceId}</dd>
            <dt>Resource</dt>
            <dd>{row.resourceId}</dd>
            <dt>Listing</dt>
            <dd>{row.listingId}</dd>
            <dt>Release</dt>
            <dd>{row.releaseId}</dd>
            <dt>Released Listing revision</dt>
            <dd>
              {content ? (
                <a href={`${base}/listing-revisions/${row.listingRevisionId}`}>
                  {row.listingRevisionId}
                </a>
              ) : (
                row.listingRevisionId
              )}
            </dd>
          </dl>
          {edit && (
            <a href={`${base}/listings/${row.listingId}`}>
              Edit current candidate
            </a>
          )}
        </details>
      </td>
    </tr>
  );
}
