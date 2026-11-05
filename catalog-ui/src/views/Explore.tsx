import { KindBadge } from "./KindBadge";
import {
  atField,
  byteText,
  characterText,
  metadataTerms,
  InputError,
} from "./input-validation";
import * as checks from "./responses";
// Local browse and facets expose governed Catalog metadata through bounded API projections.
import { useId, useRef, useState } from "react";
import { ScrollableTable } from "./ScrollableTable";
import { NamespaceFilter } from "./NamespaceFilter";
import { SearchMode } from "./SearchMode";
import type { Kind } from "../api/client";
import type { components } from "../api/schema";
import { result } from "../api/client";
import type { WorkspaceProps } from "./workspace";
import { Field, Heading, Pager, ReadState, useRead } from "./workspace";
import { ValueList } from "./ValueList";
import { KindSelection } from "./KindSelection";
import { kindChoices, kindLabel } from "../resource-kinds";
import { compareTableText, usePageSort } from "./table-sort";
import { revealInvalidControl } from "./authoring";
import { Icon, controlIcons } from "../shell/Icon";
import { refineFacet } from "./facet-filters";
import type { FacetQuery } from "./facet-filters";

const filterableFacets = [
  "kind",
  "tag",
  "category",
  "publisher",
  "visibility",
  "namespace",
];

const facetLabels = new Map([
  ["kind", "Resource kind"],
  ["visibility", "Visibility"],
  ["publisher", "Publisher"],
  ["tag", "Tags"],
  ["category", "Categories"],
  ["namespace", "Local namespace"],
  ["trustState", "Trust assessment"],
  ["source", "Source"],
]);
const assessmentLabels = new Map([
  ["not_assessed", "Not assessed"],
  ["unsupported", "Unsupported"],
  ["unverified", "Unverified"],
  ["verified", "Reported verified"],
  ["failed", "Failed"],
  ["expired", "Expired"],
]);
const visibilityLabels = new Map([
  ["private", "Private"],
  ["tenant", "Tenant"],
  ["public", "Public"],
]);
/** Display known vocabulary without changing exact filter operands or extension terms. */
function facetValueLabel(field: string, value: string): string {
  if (field === "kind")
    return (
      kindChoices.find((choice) => choice.value === value)?.singular ?? value
    );
  if (field === "trustState") return assessmentLabels.get(value) ?? value;
  if (field === "visibility") return visibilityLabels.get(value) ?? value;
  return value;
}

/** Switch between metadata browse and facet exploration workflows. */
type BrowseState = {
  kind: Kind | "";
  visibility: components["schemas"]["Visibility"] | "";
  cursor: string;
  namespaceId: string | undefined;
};
type CountsState = {
  text: string;
  inputError: string;
  kinds: Kind[];
  tags: string[];
  categories: string[];
  license: string;
  publisher: string;
  visibility: components["schemas"]["Visibility"] | "";
  query: FacetQuery;
};
type Controls<T> = {
  state: T;
  update: <K extends keyof T>(field: K, value: T[K]) => void;
};
function countsState(query: FacetQuery = {}): CountsState {
  return {
    text: query.text ?? "",
    inputError: "",
    kinds: query.kinds ?? [],
    tags: query.tags ?? [],
    categories: query.categories ?? [],
    license: query.license ?? "",
    publisher: query.publisher ?? "",
    visibility: query.visibility ?? "",
    query,
  };
}

export default function Explore(
  props: WorkspaceProps & { onCatalogSearch?: (query: FacetQuery) => void },
) {
  const [tab, setTab] = useState("browse");
  return (
    <ExploreContext
      key={JSON.stringify(props.access)}
      {...props}
      tab={tab}
      setTab={setTab}
    />
  );
}

function ExploreContext(
  props: WorkspaceProps & {
    onCatalogSearch?: (query: FacetQuery) => void;
    tab: string;
    setTab: (tab: string) => void;
  },
) {
  const { tab, setTab } = props;
  const [browse, setBrowse] = useState<BrowseState>({
    kind: "",
    visibility: "",
    cursor: "",
    namespaceId: undefined,
  });
  const [counts, setCounts] = useState(() => countsState());
  const updateBrowse = <K extends keyof BrowseState>(
    field: K,
    value: BrowseState[K],
  ) => setBrowse((current) => ({ ...current, [field]: value }));
  const updateCounts = <K extends keyof CountsState>(
    field: K,
    value: CountsState[K],
  ) => setCounts((current) => ({ ...current, [field]: value }));
  const browseCompatible =
    !counts.query.text &&
    !counts.query.tags?.length &&
    !counts.query.categories?.length &&
    !counts.query.license &&
    !counts.query.publisher &&
    (counts.query.kinds?.length ?? 0) <= 1;
  return (
    <>
      <Heading
        title="Explore local catalog"
        icon="filters"
        description="Browse locally governed releases or see counts by kind, tag, category and other details. External federation results are not included in these counts."
      />
      <div className="workspace-toolbar">
        <button
          aria-pressed={tab === "browse"}
          onClick={() => setTab("browse")}
        >
          Browse releases
        </button>
        <button
          aria-pressed={tab === "facets"}
          onClick={() => setTab("facets")}
        >
          View counts
        </button>
        <div className="explore-transfer">
          {tab === "browse" ? (
            <>
              <button
                onClick={() => {
                  setCounts(
                    countsState({
                      ...(browse.kind ? { kinds: [browse.kind] } : {}),
                      ...(browse.visibility
                        ? { visibility: browse.visibility }
                        : {}),
                      ...(browse.namespaceId
                        ? { namespaceId: browse.namespaceId }
                        : {}),
                    }),
                  );
                  setTab("facets");
                }}
              >
                Use these filters for counts
              </button>
              <details>
                <summary>What transfers?</summary>
                <p>
                  The selected kind, visibility and namespace replace the count
                  filters. Paging is not transferred.
                </p>
              </details>
            </>
          ) : (
            <>
              <button
                disabled={!browseCompatible}
                onClick={() => {
                  setBrowse({
                    kind: counts.query.kinds?.[0] ?? "",
                    visibility: counts.query.visibility ?? "",
                    namespaceId: counts.query.namespaceId,
                    cursor: "",
                  });
                  setTab("browse");
                }}
              >
                Use these filters for browse
              </button>
              <details>
                <summary>What transfers?</summary>
                <p>
                  Browse accepts one kind, visibility and namespace. Use Catalog
                  search for other filters. Only applied filters transfer;
                  unsent edits stay here.
                </p>
              </details>
              <button
                disabled={!props.onCatalogSearch}
                onClick={() => props.onCatalogSearch?.(counts.query)}
              >
                Search local Catalog
              </button>
              <p className="muted">
                Search includes only local releases and may send text to the
                configured embedding provider when semantic search is enabled.
                Matching and ordering can differ from counts; identical totals
                are not guaranteed.
              </p>
            </>
          )}
        </div>
      </div>
      {tab === "browse" ? (
        <Browse {...props} state={browse} update={updateBrowse} />
      ) : (
        <Facets {...props} state={counts} update={updateCounts} />
      )}
    </>
  );
}

/** Page local Catalog records using kind/visibility filters and server ordering. */
function Browse(props: WorkspaceProps & Controls<BrowseState>) {
  const sortHint = useId();
  const { kind, visibility, cursor, namespaceId } = props.state;
  const setKind = (value: BrowseState["kind"]) => props.update("kind", value);
  const setVisibility = (value: BrowseState["visibility"]) =>
    props.update("visibility", value);
  const setCursor = (value: string) => props.update("cursor", value);
  const setNamespaceId = (value: string | undefined) =>
    props.update("namespaceId", value);
  const page = useRead(
    kind + visibility + cursor + namespaceId,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/catalog/resources", {
          params: {
            query: {
              ...(kind ? { kind: kind } : {}),
              ...(visibility ? { visibility: visibility } : {}),
              ...(namespaceId ? { namespaceId } : {}),
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.CatalogRecordPage,
      ),
    props.onFailure,
  );
  const refresh = () => (page.error && cursor ? setCursor("") : page.refresh());
  const sorted = usePageSort(page.data?.items, {
    listing: (a, b) =>
      compareTableText(
        a.listingRevision.displayName,
        b.listingRevision.displayName,
      ),
    kind: (a, b) =>
      compareTableText(kindLabel(a.resource.kind), kindLabel(b.resource.kind)),
    version: (a, b) => compareTableText(a.version.version, b.version.version),
    state: (a, b) => compareTableText(a.release.state, b.release.state),
  });
  return (
    <>
      <div className="workspace-toolbar">
        <NamespaceFilter
          client={props.client}
          value={namespaceId}
          onFailure={props.onFailure}
          onChange={(value) => {
            setNamespaceId(value);
            setCursor("");
          }}
        />
        <Field label="Kind">
          <select
            value={kind}
            onChange={(e) => {
              setKind(e.target.value as typeof kind);
              setCursor("");
            }}
          >
            <option value="">All kinds</option>
            {kindChoices.map(({ value, label }) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Visibility">
          <select
            value={visibility}
            onChange={(e) => {
              setVisibility(e.target.value as typeof visibility);
              setCursor("");
            }}
          >
            <option value="">All permitted visibility</option>
            {["private", "tenant", "public"].map((v) => (
              <option key={v} value={v}>
                {visibilityLabels.get(v)}
              </option>
            ))}
          </select>
        </Field>
        <div className="workspace-actions">
          <button disabled={page.busy} onClick={refresh}>
            {controlIcons["refresh"]} Refresh browse results
          </button>
        </div>
      </div>
      <ReadState {...page} refresh={refresh} />
      {page.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Local browse results">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Listing", "listing")}
                  {sorted.heading("Kind", "kind")}
                  {sorted.heading("Version", "version")}
                  {sorted.heading("State", "state")}
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((i) => (
                  <tr key={i.release.id}>
                    <td>
                      <a
                        href={`${props.runtime.basePath}/catalog/${i.release.id}`}
                      >
                        {i.listingRevision.displayName}
                      </a>
                    </td>
                    <td className="workspace-table-label">
                      <KindBadge kind={i.resource.kind} />
                    </td>
                    <td>{i.version.version}</td>
                    <td>
                      <span
                        className="state-badge"
                        data-state={i.release.state}
                      >
                        {i.release.state}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && <p>No local releases on this page.</p>}
          </ScrollableTable>
        </>
      )}
      <Pager
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Read bounded facet results for the submitted exploration query. */
function Facets(props: WorkspaceProps & Controls<CountsState>) {
  const [namespaceNames, setNamespaceNames] = useState<Record<string, string>>(
    {},
  );
  const valueLabel = (field: string, value: string) =>
    field === "namespace" || field === "namespaceId"
      ? (namespaceNames[value] ?? value)
      : facetValueLabel(field, value);
  const searchInput = useRef<HTMLInputElement>(null);
  const {
    text,
    inputError,
    kinds,
    tags,
    categories,
    license,
    publisher,
    visibility,
    query,
  } = props.state;
  const setText = (value: string) => props.update("text", value);
  const setInputError = (value: string) => props.update("inputError", value);
  const setKinds = (value: Kind[]) => props.update("kinds", value);
  const setTags = (value: string[]) => props.update("tags", value);
  const setCategories = (value: string[]) => props.update("categories", value);
  const setLicense = (value: string) => props.update("license", value);
  const setPublisher = (value: string) => props.update("publisher", value);
  const setVisibility = (value: CountsState["visibility"]) =>
    props.update("visibility", value);
  const setQuery = (value: FacetQuery) => props.update("query", value);
  const queryKey = JSON.stringify(query);
  const page = useRead(
    queryKey,
    async (signal) => ({
      queryKey,
      response: result(
        await props.client.api.POST("/api/v1/catalog/explore", {
          body: {
            query,
            facets: [
              "kind",
              "visibility",
              "publisher",
              "tag",
              "category",
              "namespace",
              "trustState",
              "source",
            ].map((field) => ({
              field:
                field as components["schemas"]["CatalogExplore"]["facets"][number]["field"],
              limit: 20,
            })),
          },
          signal,
        }),
        checks.CatalogExploreResponse,
      ),
    }),
    props.onFailure,
  );
  // A completed response belongs to one submitted query, including the render
  // before useRead's effect clears its previous data. Never relabel old counts.
  const data =
    page.data?.queryKey === queryKey ? page.data.response : undefined;

  /** Refine submitted criteria without applying unrelated edits in the form. */
  function refine(next: FacetQuery, field: keyof FacetQuery) {
    setQuery(next);
    // Removal is invoked only by chips rendered from these existing arrays.
    if (field === "kinds") setKinds(next.kinds ?? []);
    if (field === "categories") setCategories(next.categories ?? []);
    // License has no facet bucket; its chip only removes this criterion.
    if (field === "license") setLicense("");
    if (field === "tags") setTags(next.tags ?? []);
    if (field === "publisher") setPublisher(next.publisher ?? "");
    if (field === "visibility") setVisibility(next.visibility ?? "");
    if (field === "text") setText(next.text ?? "");
    // The clicked bucket or chip may disappear when the response changes.
    searchInput.current?.focus();
  }

  /** Remove one criterion while retaining the rest of the submitted query. */
  function remove(field: keyof FacetQuery, value: string) {
    const next = { ...query };
    // Removal is invoked only by chips rendered from these existing arrays.
    if (field === "kinds")
      next.kinds = next.kinds!.filter((kind) => kind !== value);
    else if (field === "tags" || field === "categories")
      next[field] = next[field]!.filter((term) => term !== value);
    else delete next[field];
    if (!next.kinds?.length) delete next.kinds;
    if (!next.tags?.length) delete next.tags;
    if (!next.categories?.length) delete next.categories;
    refine(next, field);
  }
  const criteria = [
    ...kindChoices
      .filter((choice) => query.kinds?.includes(choice.value))
      .map((choice) => ({
        field: "kinds" as const,
        value: choice.value,
        label: `resource kind ${choice.singular}`,
      })),
    ...(query.tags ?? []).map((value) => ({
      field: "tags" as const,
      value,
      label: `tag ${value}`,
    })),
    ...(query.categories ?? []).map((value) => ({
      field: "categories" as const,
      value,
      label: `category ${value}`,
    })),
    ...(
      ["text", "license", "publisher", "visibility", "namespaceId"] as const
    ).flatMap((field) =>
      query[field]
        ? [
            {
              field,
              value: query[field],
              label: `${field === "namespaceId" ? "local namespace" : field} ${valueLabel(field, query[field])}`,
            },
          ]
        : [],
    ),
  ];
  return (
    <>
      <div className="explore-namespace">
        <NamespaceFilter
          onChoices={(items) =>
            setNamespaceNames((previous) => {
              const needed = new Set([
                query.namespaceId,
                ...(data?.facets.namespace?.buckets.map(
                  (bucket) => bucket.value,
                ) ?? []),
              ]);
              return Object.fromEntries([
                ...Object.entries(previous).filter(([id]) => needed.has(id)),
                ...items.map((item) => [item.id, item.displayName] as const),
              ]);
            })
          }
          client={props.client}
          value={query.namespaceId}
          onFailure={props.onFailure}
          onChange={(namespaceId) => {
            const next = { ...query };
            if (namespaceId) next.namespaceId = namespaceId;
            else delete next.namespaceId;
            setQuery(next);
          }}
        />
      </div>
      <form
        className="workspace-panel workspace-form"
        onInvalidCapture={(event) =>
          revealInvalidControl(event.target as Element)
        }
        onSubmit={(e) => {
          e.preventDefault();
          try {
            const next = {
              ...(query.namespaceId ? { namespaceId: query.namespaceId } : {}),
              text: atField("explore-text", () =>
                byteText(text, "Search text", 500),
              ),
              ...(kinds.length ? { kinds: [...kinds] } : {}),
              ...(tags.length
                ? {
                    tags: atField("explore-tags", () =>
                      metadataTerms(
                        [...new Set(tags.map((value) => value.trim()))],
                        "tags",
                        32,
                      ),
                    ),
                  }
                : {}),
              ...(license.trim()
                ? {
                    license: atField("explore-license", () =>
                      characterText(license.trim(), "License", 256),
                    ),
                  }
                : {}),
              ...(categories.length
                ? {
                    categories: atField("explore-categories", () =>
                      metadataTerms(
                        [...new Set(categories.map((value) => value.trim()))],
                        "categories",
                        16,
                        1,
                      ),
                    ),
                  }
                : {}),
              ...(publisher
                ? {
                    publisher: atField("explore-publisher", () =>
                      byteText(publisher, "Publisher", 256),
                    ),
                  }
                : {}),
              ...(visibility ? { visibility } : {}),
            };
            setInputError("");
            if (JSON.stringify(next) === queryKey) page.refresh();
            else setQuery(next);
          } catch (error) {
            // Every local validation above is attributed by atField.
            const problem = error as InputError;
            setInputError(problem.message);
            const control = document.getElementById(problem.fieldId)!;
            revealInvalidControl(control);
            control.focus();
          }
        }}
      >
        <Field label="Search local metadata">
          <input
            id="explore-text"
            ref={searchInput}
            maxLength={500}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </Field>
        <KindSelection value={kinds} onChange={setKinds} />
        <details>
          <summary>Tags and categories</summary>
          <ValueList
            id="explore-tags"
            label="Tags"
            itemLabel="Tag"
            value={tags}
            onChange={setTags}
            maximum={32}
            length={80}
            hint="All selected tags must match."
          />
          <ValueList
            id="explore-categories"
            label="Categories"
            itemLabel="Category"
            value={categories}
            onChange={setCategories}
            maximum={16}
            length={80}
            hint="All selected categories must match."
          />
        </details>
        <details>
          <summary>Publisher, license and visibility</summary>
          <Field
            label="License declaration"
            hint="Case-sensitive exact local Listing declaration, up to 256 characters."
          >
            <input
              id="explore-license"
              value={license}
              onChange={(event) => setLicense(event.target.value)}
            />
          </Field>
          <Field
            label="Publisher ID (optional)"
            hint="Optional local publisher UUID from Publishers. Leave blank to include all publishers."
          >
            <input
              id="explore-publisher"
              value={publisher}
              onChange={(e) => setPublisher(e.target.value)}
            />
          </Field>
          <Field label="Visibility">
            <select
              value={visibility}
              onChange={(e) =>
                setVisibility(e.target.value as typeof visibility)
              }
            >
              <option value="">All permitted visibility</option>
              {["private", "tenant", "public"].map((v) => (
                <option key={v} value={v}>
                  {visibilityLabels.get(v)}
                </option>
              ))}
            </select>
          </Field>
        </details>
        {inputError && (
          <p role="alert" className="notice error">
            {inputError}
          </p>
        )}
        <button type="submit" className="primary">
          Apply filters
        </button>
        <button
          type="button"
          onClick={() => {
            setInputError("");
            setText("");
            setKinds([]);
            setTags([]);
            setCategories([]);
            setLicense("");
            setPublisher("");
            setVisibility("");
            setQuery({});
            searchInput.current?.focus();
          }}
        >
          Clear filters
        </button>
      </form>
      <div
        className="applied-filters"
        role="group"
        aria-label="Applied facet criteria"
      >
        {!!criteria.length && (
          <p className="muted">Select an applied criterion to remove it.</p>
        )}
        {criteria.map((criterion) => (
          <button
            key={`${criterion.field}:${criterion.value}`}
            className="filter-chip"
            aria-label={`Remove ${criterion.label}`}
            onClick={() => remove(criterion.field, criterion.value)}
          >
            <span>{criterion.label}</span>
            <Icon name="close" size={12} />
          </button>
        ))}
      </div>
      <p className="muted">
        Select a kind, tag, category, namespace, publisher or visibility to
        filter results. Other values are summaries, not filters. Results may
        match any selected kind but must match every selected tag and category.
        License text must match exactly. Counts describe the applied filters.
      </p>
      <button disabled={page.busy} onClick={page.refresh}>
        {controlIcons["refresh"]} Refresh counts
      </button>
      <p className="muted">
        Refresh updates the counts for the applied filters. Unsent edits stay in
        the form.
      </p>
      <ReadState {...page} />
      {data && (
        <>
          <p role="status" aria-label="Matched releases">
            {data.matched} matching local{" "}
            {data.matched === 1 ? "release" : "releases"}
          </p>
          <SearchMode result={data} />
          <div className="facet-grid">
            {Object.entries(data.facets).map(([field, facet]) => (
              <section key={field} className="workspace-panel">
                <h2>{facetLabels.get(field) ?? field}</h2>
                {facet.buckets.map((bucket) => {
                  const next = refineFacet(query, field, bucket.value);
                  const supported = filterableFacets.includes(field);
                  const caption =
                    field === "kind" &&
                    kindChoices.some((kind) => kind.value === bucket.value) ? (
                      <KindBadge kind={bucket.value as Kind} />
                    ) : (
                      valueLabel(field, bucket.value)
                    );
                  return supported ? (
                    <button
                      type="button"
                      className="facet-bucket"
                      key={bucket.value}
                      aria-label={`Filter by ${field === "tag" || field === "category" ? field : facetLabels.get(field)!.toLowerCase()} ${valueLabel(field, bucket.value)}`}
                      aria-pressed={next === query}
                      disabled={!next}
                      aria-describedby={
                        !next ? `facet-limit-${field}` : undefined
                      }
                      onClick={() => {
                        if (next && next !== query)
                          refine(
                            next,
                            field === "kind"
                              ? "kinds"
                              : field === "tag"
                                ? "tags"
                                : field === "category"
                                  ? "categories"
                                  : (field as "publisher" | "visibility"),
                          );
                      }}
                    >
                      <span>{caption}</span>
                      <strong>{bucket.count}</strong>
                    </button>
                  ) : (
                    <p className="facet-value" key={bucket.value}>
                      <span>{caption}</span> <strong>{bucket.count}</strong>
                    </p>
                  );
                })}
                {facet.buckets.some(
                  (bucket) =>
                    filterableFacets.includes(field) &&
                    !refineFacet(query, field, bucket.value),
                ) && (
                  <p id={`facet-limit-${field}`} className="muted">
                    Unavailable values exceed the supported filter limits.
                  </p>
                )}
                {!!facet.otherCount && (
                  <p>
                    {facet.otherCount} omitted bucket memberships (not distinct
                    releases). Only the top 20 values are shown; there is no
                    next page.
                  </p>
                )}
                {!facet.buckets.length && <p>No matching values.</p>}
              </section>
            ))}
          </div>
        </>
      )}
    </>
  );
}
