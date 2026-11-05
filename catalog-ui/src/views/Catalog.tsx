import { DraftDownload } from "./DraftDownload";
import { Facts } from "./Facts";
import { catalogOriginPreset } from "./overview-values";
import "./catalog-results.css";
import { catalogOrders, type CatalogOrder } from "./catalog-order";
import { SaveSearch, SaveResource } from "./Personal";
import { recordForRequest } from "./record-context";
import { RecordNavigation, localRecordSections } from "./RecordNavigation";
import { LiveRefreshNotice } from "./LiveRefreshNotice";
import { useNamespaceAccessState } from "./namespace-access";
import ResultComparison, { comparisonIdentity } from "./ResultComparison";
import {
  RatingEntry,
  RatingLabels,
  requestForResult,
  requestForRecord,
} from "./Ratings";
import { foreignArtworkTarget, SourceArtworkControls } from "./SourceArtwork";
import { ArtworkIcon, ArtworkGallery } from "./Artwork";
import { byteText } from "./input-validation";
import * as checks from "./responses";
// Catalog search separates draft controls, submitted criteria and explicit result previews.
import {
  CatalogPresentation,
  SelectedNamespace,
  originLabel,
} from "./CatalogPresentation";
import { RecordWorkflows } from "./RecordWorkflows";
import { FeedbackEntry } from "./Feedback";
import { SearchMode } from "./SearchMode";
import {
  lazy,
  Suspense,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  LocalHandoff,
  ExternalHandoff,
  LocalAssessment,
  IndexedProvenance,
} from "./Handoff";
import { JSONBlock } from "./CodeBlock";
import { CopyReference } from "./CopyReference";
import { APIError, result } from "../api/client";
import type {
  Client,
  RecordDetail,
  Runtime,
  SearchResult,
  SearchResponse,
} from "../api/client";
import type { MouseEvent } from "react";
import type { components } from "../api/schema";
import { Icon, controlIcons } from "../shell/Icon";
import SearchFilters, { visibilityOptions } from "./SearchFilters";
import { applyFilters, publisherSuggestions } from "./search-filters";
import { kindChoices, kindLabel } from "../resource-kinds";
import { confirmDiscard } from "./workspace";
import {
  availablePermission,
  permits,
  resourceCreationAccess,
} from "../api/permissions";

function requestOriginsLocal(
  origins?: components["schemas"]["PersonalCriteria"]["origins"],
) {
  return !!origins && !origins.includes("live");
}
const IndexedRecord = lazy(() => import("./IndexedRecord"));
const Explore = lazy(() => import("./Explore"));

// Explain bounded outcomes without inferring an outage, trust or a failure cause.
const searchDiagnostics: Record<
  components["schemas"]["CatalogSearchDiagnostic"]["status"],
  string
> = {
  failed:
    "Source request failed. This status alone does not identify the cause.",
  invalid_response: "A source returned a response that could not be used.",
  insufficient_evidence:
    "Some results lacked the declarations required by the selected filters.",
  policy_stale:
    "Source policy evidence is not current. A Source Administrator can reinspect the source.",
  fanout_limited:
    "Live source selection was limited or a duplicate source was skipped.",
  diagnostics_truncated:
    "Additional source diagnostics were omitted from this bounded response.",
  live_results_limited:
    "Additional live results could not be safely continued. Start a new search.",
  continuation_unavailable:
    "An upstream Registry has more results; its next page was not followed.",
};

/** Restart pagination without changing the submitted criteria. */
function firstPage(query: components["schemas"]["CatalogSearch"]) {
  const { cursor: _cursor, ...criteria } = query;
  return criteria;
}

/** Own submitted search criteria, opaque cursor navigation and explicit record preview state. */
type CatalogProps = {
  stagedSearch?: components["schemas"]["PersonalCriteria"];
  searchPath?: string;
  managedNavigation?: boolean;
  access?: components["schemas"]["CurrentAccess"];
  client: Client;
  runtime: Runtime;
  onFailure: (error: unknown) => void;
};

export default function Catalog(props: CatalogProps) {
  return (
    <CatalogWorkspace
      key={JSON.stringify(
        props.stagedSearch ? [props.access, props.stagedSearch] : undefined,
      )}
      {...props}
    />
  );
}

function CatalogWorkspace({
  client,
  runtime,
  onFailure,
  access,
  stagedSearch,
  searchPath = runtime.basePath + "/catalog",
  managedNavigation = false,
}: CatalogProps) {
  const externalAllowed =
    !!access && permits(access, "federation.discover", "catalog.read");
  const [text, setText] = useState(stagedSearch?.text ?? "");
  const [searchArmed, setSearchArmed] = useState(!stagedSearch);
  const [disclosureConsent, setDisclosureConsent] = useState(false);
  const [providerConsent, setProviderConsent] = useState(false);

  const [{ request, previous, reason }, setPage] = useState<{
    request: components["schemas"]["CatalogSearch"];
    previous: string[];
    reason?: "refresh";
  }>({
    request: {
      ...(stagedSearch ?? {
        text: "",
        ...(location.pathname === searchPath &&
        catalogOriginPreset(location.search)
          ? { origins: [catalogOriginPreset(location.search)!] }
          : {}),
      }),
      limit: 24,
    },
    previous: [],
  });
  const liveConsentRequired =
    !request.namespaceId && !requestOriginsLocal(request.origins);
  const providerConsentRequired =
    !!request.namespaceId ||
    !request.origins?.length ||
    request.origins.some((origin) => origin !== "live");
  // Keep at most ten earlier request cursors, never previous result documents.
  function setRequest(next: components["schemas"]["CatalogSearch"]) {
    setPage({ request: next, previous: [] });
  }
  const [displayedPage, setDisplayedPage] = useState<{
    response: SearchResponse;
    request: components["schemas"]["CatalogSearch"];
  }>();
  const data = displayedPage?.response;
  const shownQuery = displayedPage?.request ?? request;
  const shownOrder =
    shownQuery.order ?? (shownQuery.text?.trim() ? undefined : "name_asc");
  function changeOrder(order: CatalogOrder | undefined) {
    const { order: _order, ...criteria } = firstPage(request);
    setRequest(order ? { ...criteria, order } : criteria);
  }
  function sortableHeading(
    label: string,
    field: "name" | "kind" | "publisher",
  ) {
    const active = shownOrder?.startsWith(field + "_") ?? false;
    const descending = active && shownOrder?.endsWith("_desc");
    const next =
      `${field}_${shownOrder === `${field}_asc` ? "desc" : "asc"}` as CatalogOrder;
    return (
      <th
        scope="col"
        aria-label={label}
        aria-sort={active ? (descending ? "descending" : "ascending") : "none"}
      >
        <button
          className="catalog-sort-heading"
          type="button"
          onClick={() => changeOrder(next)}
          aria-label={`Sort by ${label.toLowerCase()}, ${next.endsWith("_desc") ? "descending" : "ascending"}`}
        >
          {label}
          <span aria-hidden="true">
            {active ? (descending ? "↓" : "↑") : "↕"}
          </span>
        </button>
      </th>
    );
  }
  const comparisonId = useId();
  const identityKey = JSON.stringify(access);
  const [compared, setCompared] = useState<SearchResult[]>([]);
  const [comparisonOpen, setComparisonOpen] = useState(false);
  const [comparisonMessage, setComparisonMessage] = useState("");
  const comparisonContext = useRef({ data, request, client, identityKey });
  const comparisonCurrent =
    comparisonContext.current.data === data &&
    comparisonContext.current.request === request &&
    comparisonContext.current.client === client &&
    comparisonContext.current.identityKey === identityKey;
  useLayoutEffect(() => {
    comparisonContext.current = { data, request, client, identityKey };
    setCompared([]);
    setComparisonOpen(false);
    setComparisonMessage("");
  }, [data, request, client, identityKey]);
  const choices = comparisonCurrent ? compared : [];
  function compare(item: SearchResult) {
    const key = comparisonIdentity(item);
    if (choices.some((chosen) => comparisonIdentity(chosen) === key)) {
      setCompared(
        choices.filter((chosen) => comparisonIdentity(chosen) !== key),
      );
      setComparisonMessage("");
    } else if (choices.length === 2)
      setComparisonMessage(
        "Choose at most two results. Remove a selection first.",
      );
    else {
      setCompared([...choices, item]);
      setComparisonMessage("");
    }
  }
  function comparisonChoice(item: SearchResult, index: number) {
    return (
      <label className="comparison-choice">
        <input
          type="checkbox"
          id={`${comparisonId}-${index}`}
          aria-label={`Compare ${item.displayName} (${originLabel(item.type)}, result ${index + 1})`}
          checked={choices.some(
            (chosen) => comparisonIdentity(chosen) === comparisonIdentity(item),
          )}
          onChange={() => compare(item)}
        />
        Compare
      </label>
    );
  }
  const [error, setError] = useState("");
  const [retryContinuation, setRetryContinuation] = useState(false);
  const displayed = useRef<{ criteria: string; client: Client } | undefined>(
    undefined,
  );
  const [loading, setLoading] = useState(true);
  const [layout, setLayout] = useState<"cards" | "table">("cards");
  const [namespaceLabel, setNamespaceLabel] = useState<{
    id: string;
    label: string | undefined;
  }>();
  const [selected, setSelected] = useState<{
    item: SearchResult;
    request: components["schemas"]["CatalogSearch"];
  }>();
  function preview(item: SearchResult) {
    // A retained page can remain visible while another cursor is pending or failed.
    setSelected({ item, request: displayedPage!.request });
  }
  const [record, setRecord] = useState<RecordDetail>();
  const searchInput = useRef<HTMLInputElement>(null);
  const [address, setAddress] = useState(location.pathname + location.search);
  const pathname = address.split("?")[0]!;
  const preset =
    !stagedSearch && pathname === searchPath
      ? catalogOriginPreset(address.slice(pathname.length))
      : undefined;
  const priorPreset = useRef(preset);
  useEffect(() => {
    if (
      pathname !== searchPath ||
      stagedSearch ||
      priorPreset.current === preset
    )
      return;
    priorPreset.current = preset;
    setText("");
    setPage({
      request: {
        text: "",
        limit: 24,
        ...(preset ? { origins: [preset] } : {}),
      },
      previous: [],
    });
    setSearchArmed(true);
  }, [preset, pathname, searchPath, stagedSearch]);
  const isIndexed = pathname === runtime.basePath + "/indexed-record";
  const isExplore = pathname === runtime.basePath + "/explore";
  const [exploreVisited, setExploreVisited] = useState(isExplore);
  useEffect(() => {
    if (isExplore) setExploreVisited(true);
  }, [isExplore]);
  const searchVisited = useRef(false);
  const returnScroll = useRef({ catalog: 0, explore: 0 });
  const returnView = useRef("catalog");
  const restoreScroll = useRef(false);
  const priorPath = useRef(pathname);
  // Store only a navigation ordinal, never private queries or result documents.
  const navigationIndex = useRef<number>(0);
  useLayoutEffect(() => {
    // Native restoration runs after popstate and can overwrite our saved list
    // position. Own scrolling while this view owns same-document navigation.
    const previousScrollRestoration = history.scrollRestoration;
    history.scrollRestoration = "manual";
    const state: unknown = history.state;
    const index =
      state && typeof state === "object" && "catalogNavigation" in state
        ? state.catalogNavigation
        : undefined;
    navigationIndex.current =
      typeof index === "number" && Number.isSafeInteger(index) ? index : 0;
    history.replaceState({ catalogNavigation: navigationIndex.current }, "");
    const moved = (event: Event) => {
      const state: unknown = managedNavigation
        ? (event as CustomEvent<unknown>).detail
        : (event as PopStateEvent).state;
      const next =
        state && typeof state === "object" && "catalogNavigation" in state
          ? state.catalogNavigation
          : undefined;
      if (typeof next === "number" && Number.isSafeInteger(next)) {
        if (next !== navigationIndex.current && !confirmDiscard()) {
          event.preventDefault();
          // Restore the existing entry, including its forward history. No draft
          // is copied into history or discarded by the corrective event.
          history.go(navigationIndex.current - next);
          return;
        }
        navigationIndex.current = next;
      }
      // Native history leaves the old document scroll in place during popstate.
      // Save the departing list before its next entry and data are restored.
      if (priorPath.current === searchPath)
        returnScroll.current.catalog = window.scrollY;
      else if (priorPath.current === runtime.basePath + "/explore")
        returnScroll.current.explore = window.scrollY;
      setAddress(location.pathname + location.search);
    };
    const navigationEvent = managedNavigation
      ? "catalog-ui:navigate"
      : "popstate";
    window.addEventListener(navigationEvent, moved);
    return () => {
      window.removeEventListener(navigationEvent, moved);
      history.scrollRestoration = previousScrollRestoration;
    };
  }, [runtime.basePath, searchPath, managedNavigation]);
  function move(path: string) {
    history.pushState(
      { catalogNavigation: ++navigationIndex.current },
      "",
      path,
    );
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
  }
  useEffect(() => {
    if (
      (priorPath.current.startsWith(runtime.basePath + "/catalog/") ||
        priorPath.current === runtime.basePath + "/indexed-record") &&
      (pathname === searchPath || isExplore)
    )
      restoreScroll.current = true;
    priorPath.current = pathname;
  }, [pathname, runtime.basePath, isExplore, searchPath]);
  useEffect(() => {
    if ((!data && !isExplore) || !restoreScroll.current) return;
    const frame = requestAnimationFrame(() => {
      window.scrollTo(
        0,
        returnScroll.current[isExplore ? "explore" : "catalog"],
      );
      restoreScroll.current = false;
    });
    return () => cancelAnimationFrame(frame);
  }, [data, isExplore, pathname]);
  function navigate(event: MouseEvent<HTMLDivElement>) {
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.ctrlKey ||
      event.metaKey ||
      event.shiftKey ||
      event.altKey ||
      !(event.target instanceof Element)
    )
      return;
    const link = event.target.closest("a");
    if (!link || link.target || link.hasAttribute("download")) return;
    const destination = new URL(link.href);
    const prefix = runtime.basePath + "/catalog";
    if (
      destination.origin !== location.origin ||
      (destination.search &&
        destination.pathname !== runtime.basePath + "/indexed-record") ||
      destination.hash ||
      !(
        destination.pathname === prefix ||
        destination.pathname === searchPath ||
        destination.pathname === runtime.basePath + "/explore" ||
        destination.pathname === runtime.basePath + "/indexed-record" ||
        (destination.pathname.startsWith(prefix + "/") &&
          /^[0-9a-f-]{36}$/i.test(
            destination.pathname.slice(prefix.length + 1),
          ))
      )
    )
      return;
    event.preventDefault();
    if (!confirmDiscard()) return;
    if (!isDetail) {
      returnScroll.current[isExplore ? "explore" : "catalog"] = window.scrollY;
      returnView.current = isExplore ? "explore" : "catalog";
    }
    move(destination.pathname + destination.search);
    window.scrollTo(0, 0);
  }
  const detailID = pathname.slice((runtime.basePath + "/catalog/").length);
  const isLocalDetail =
    pathname.startsWith(runtime.basePath + "/catalog/") &&
    /^[0-9a-f-]{36}$/i.test(detailID);
  const isDetail = isLocalDetail || isIndexed;
  const failure = useRef(onFailure);
  failure.current = onFailure;
  const forbiddenOrigins =
    !externalAllowed && !!request.origins?.some((origin) => origin !== "local");
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setLoading(true);
    setError("");
    setRetryContinuation(false);
    const criteria = JSON.stringify(firstPage(request));
    const retain =
      !isDetail &&
      (!!request.cursor || reason === "refresh") &&
      !forbiddenOrigins &&
      displayed.current?.criteria === criteria &&
      displayed.current.client === client;
    if (!retain) setDisplayedPage(undefined);
    setRecord(undefined);
    setSelected(undefined);
    if (
      (!isDetail &&
        (!searchArmed ||
          (!!stagedSearch &&
            ((!disclosureConsent && liveConsentRequired) ||
              (!providerConsent && providerConsentRequired))))) ||
      isIndexed ||
      isExplore
    ) {
      setLoading(false);
      return () => {
        current = false;
        controller.abort();
      };
    }
    if (!isDetail && forbiddenOrigins) {
      setError(
        "These external origins require federation.discover and catalog.read scope when enforced. Remove the selections or reset the search.",
      );
      setLoading(false);
      return () => controller.abort();
    }
    const load = isDetail
      ? client.api
          .GET("/api/v1/catalog/resources/{catalogRecordId}", {
            params: { path: { catalogRecordId: detailID } },
            signal: controller.signal,
          })
          .then((reply) => {
            if (current)
              setRecord(
                result(reply, (value) =>
                  recordForRequest(value, detailID, access?.tenantId),
                ),
              );
          })
      : client.api
          .POST("/api/v1/catalog/search", {
            body: request,
            signal: controller.signal,
          })
          .then((reply) => {
            if (current) {
              setDisplayedPage({
                response: result(reply, checks.CatalogSearchResponse),
                request,
              });
              displayed.current = { criteria, client };
              searchVisited.current = true;
            }
          });
    load
      .catch((e) => {
        if (current) {
          const retryable =
            retain &&
            (!(e instanceof APIError) || e.status >= 500 || e.status === 429);
          setRetryContinuation(retryable);
          if (!retryable) {
            setDisplayedPage(undefined);
            setPage((page) => ({ ...page, previous: [] }));
          }
          failure.current(e);
          setError(
            e instanceof APIError
              ? `${e.message}${e.requestId ? ` Request: ${e.requestId}` : ""}`
              : "The Catalog could not be reached.",
          );
        }
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [
    searchArmed,
    stagedSearch,
    disclosureConsent,
    liveConsentRequired,
    providerConsent,
    providerConsentRequired,
    access?.tenantId,
    client,
    request,
    reason,
    isDetail,
    detailID,
    forbiddenOrigins,
    isIndexed,
    isExplore,
  ]);
  // Submit the text draft without replacing the already selected kinds.
  function search() {
    if (
      stagedSearch &&
      ((!disclosureConsent && liveConsentRequired) ||
        (!providerConsent && providerConsentRequired))
    ) {
      setError(
        "Review query disclosure and provider consent before searching.",
      );
      return;
    }
    try {
      byteText(text, "Search text", 500);
    } catch (error) {
      setError((error as Error).message);
      searchInput.current!.focus();
      return;
    }
    setSearchArmed(true);
    setRequest({
      ...firstPage(request),
      text,
      limit: 24,
    });
  }
  function refreshResults() {
    if (!confirmDiscard()) return;
    setPage({ request: firstPage(request), previous: [], reason: "refresh" });
  }
  // Clear all submitted criteria and return keyboard focus to the search input.
  function resetSearch() {
    setText("");
    setRequest({ text: "", limit: 24 });
    searchInput.current?.focus();
  }
  // Remove one applied criterion and restart pagination without discarding the others.
  function removeCriterion(
    key:
      | "text"
      | "kinds"
      | "tags"
      | "categories"
      | "license"
      | "publisher"
      | "visibility"
      | "origins"
      | "namespaceId",
    tag?: string,
  ) {
    const next = firstPage(request);
    if (key === "origins" && request.namespaceId) return;
    // Removal is invoked only by chips rendered from these existing arrays.
    if (key === "origins" && tag) {
      next.origins = next.origins!.filter((value) => value !== tag);
      if (!next.origins.length) delete next.origins;
    } else if ((key === "tags" || key === "categories") && tag)
      next[key] = next[key]!.filter((value) => value !== tag);
    else if (key === "kinds" && tag)
      next.kinds = next.kinds!.filter((value) => value !== tag);
    else delete next[key];
    if (key === "text") setText("");
    setRequest(next);
    searchInput.current?.focus();
  }
  const focusFilteredSearch = useRef(false);
  function addTag(tag: string) {
    addTerm("tags", tag, 32);
  }
  function addCategory(category: string) {
    addTerm("categories", category, 16);
  }
  function addTerm(key: "tags" | "categories", value: string, maximum: number) {
    const selected = request[key]?.includes(value);
    if (
      (selected && !isDetail) ||
      (!selected &&
        ((request[key]?.length ?? 0) >= maximum || [...value].length > 80)) ||
      !value.trim() ||
      value.includes("\0")
    )
      return;
    if (!confirmDiscard()) return;
    if (isDetail) {
      move(searchPath);
    }
    focusFilteredSearch.current = true;
    setSelected(undefined);
    setRequest({
      ...firstPage(request),
      [key]: selected ? request[key]! : [...(request[key] ?? []), value],
    });
    // Filtering removes the opener; focus the persistent search input after rendering.
  }
  useEffect(() => {
    if (!isDetail && focusFilteredSearch.current) {
      searchInput.current?.focus();
      focusFilteredSearch.current = false;
    }
  }, [isDetail, request]);
  function selectNamespace(namespaceId: string) {
    if (!confirmDiscard()) return;
    setNamespaceLabel({
      id: namespaceId,
      label:
        data?.items.find(
          (item) => item.local?.listing.namespaceId === namespaceId,
        )?.local?.display?.namespaceName ??
        (record?.listing.namespaceId === namespaceId
          ? record.display?.namespaceName
          : undefined),
    });
    if (isDetail) {
      move(searchPath);
    }
    setSelected(undefined);
    setRequest({ ...firstPage(request), namespaceId, origins: ["local"] });
    focusFilteredSearch.current = true;
  }
  const hasFilters = !!(
    request.text ||
    request.tags?.length ||
    request.categories?.length ||
    request.license ||
    request.publisher ||
    request.visibility ||
    request.kinds?.length ||
    request.origins?.length ||
    request.namespaceId
  );
  const content = isIndexed ? (
    <Suspense fallback={<p role="status">Loading indexed record...</p>}>
      {access && (
        <IndexedRecord
          key={address}
          client={client}
          runtime={runtime}
          access={access}
          id=""
          onFailure={onFailure}
          restoreSearch={searchVisited.current}
          returnPath={searchPath}
          returnLabel={stagedSearch ? "saved search" : "Catalog"}
          search={address.slice(pathname.length)}
        />
      )}
    </Suspense>
  ) : isLocalDetail ? (
    <div>
      <a
        className="back-link"
        href={
          returnView.current === "catalog"
            ? searchPath
            : `${runtime.basePath}/explore`
        }
      >
        &larr; Back to{" "}
        {returnView.current === "explore"
          ? "Explore"
          : stagedSearch
            ? "saved search"
            : "Catalog"}
      </a>
      <p className="muted">
        Returning in this tab restores your search; reloading starts a fresh
        search.
      </p>
      {loading && <p role="status">Loading record...</p>}
      {error && (
        <>
          <p role="alert" className="notice error">
            {error}
          </p>
          <p className="muted">
            The record may be unavailable to your current access or no longer
            discoverable. Return to Catalog or retry this read.
          </p>
          <button onClick={() => setRequest(firstPage(request))}>
            Retry record
          </button>
        </>
      )}
      {record && (
        <>
          {access?.actorType === "human" &&
            permits(access, "feedback.own", "catalog.read") && (
              <p>
                <a href={`${runtime.basePath}/my-feedback`}>
                  My private feedback
                </a>
                . Use Item feedback below to report a problem about this exact
                record. Ratings remain separate human opinions.
              </p>
            )}
          <Record
            record={record}
            client={client}
            onFailure={onFailure}
            basePath={runtime.basePath}
            access={access}
            tags={request.tags ?? []}
            onTag={addTag}
            categories={request.categories ?? []}
            onCategory={addCategory}
            documentationUrl={runtime.documentationUrl}
            onNamespace={selectNamespace}
          />
          <div className="record-feedback">
            <RatingEntry
              client={client}
              access={access}
              onFailure={onFailure}
              request={requestForRecord(record)}
              namespaceId={record.resource.namespaceId}
            />
            {access && (
              <FeedbackEntry
                client={client}
                runtime={runtime}
                access={access}
                id={record.resource.id}
                onFailure={onFailure}
                resourceId={record.resource.id}
                namespaceId={record.resource.namespaceId}
                releaseId={record.release.id}
                versionRevisionId={record.versionRevision.id}
              />
            )}
          </div>
          <a
            className="back-link"
            href={
              returnView.current === "explore"
                ? `${runtime.basePath}/explore`
                : searchPath
            }
          >
            {stagedSearch ? "Return to saved search" : "Return to results"}
          </a>
        </>
      )}
    </div>
  ) : isExplore ? null : (
    <div>
      <div className="page-heading catalog-hero">
        <div>
          <h1>{stagedSearch ? "Saved search" : "Catalog"}</h1>
          <p className="muted">
            Discover Agents, MCP Servers, Skills and Plugins.
          </p>
        </div>
        {access?.actorType === "human" &&
          permits(access, "library.own", "personal.write") && (
            <SaveSearch
              client={client}
              access={access}
              basePath={runtime.basePath}
              onFailure={onFailure}
              criteria={displayedPage?.request}
            />
          )}
      </div>
      <div
        className="kind-picker tabs"
        role="group"
        aria-label="Resource kinds"
      >
        <button
          aria-pressed={!request.kinds?.length}
          onClick={() =>
            setRequest(applyFilters(request, { ...request, kinds: undefined }))
          }
        >
          All kinds
        </button>
        {kindChoices.map((item) => (
          <button
            key={item.value}
            aria-label={item.label}
            data-kind={item.value}
            aria-pressed={
              request.kinds?.length === 1 && request.kinds[0] === item.value
            }
            onClick={() =>
              setRequest(
                applyFilters(request, { ...request, kinds: [item.value] }),
              )
            }
          >
            <span className={`kind-icon ${item.value}`}>
              <Icon name={item.value} size={18} />
            </span>
            {item.label}
          </button>
        ))}
      </div>
      <p id="search-disclosure" className="search-guidance">
        {externalAllowed &&
        !request.namespaceId &&
        (!request.origins || request.origins.includes("live")) ? (
          <>
            Live search may send your search text, tags and filters to approved
            external catalogs.{" "}
            <button
              type="button"
              className="text-button"
              onClick={() => {
                setRequest(
                  applyFilters(request, {
                    ...request,
                    origins: ["local", "indexed"],
                  }),
                );
                searchInput.current?.focus();
              }}
            >
              Exclude live results
            </button>{" "}
          </>
        ) : !externalAllowed ? (
          <>Your current access includes local Catalog results only. </>
        ) : (
          <>Live sources are excluded by the current filters. </>
        )}
        Search text may also be sent to the configured semantic search provider
        when local or synced results are included.
      </p>
      {text !== (request.text ?? "") && (
        <p className="notice" id="pending-search-text" role="status">
          Text changed. Choose Search to apply it. Filters and refresh use the
          applied search text.
        </p>
      )}
      <div className="search-toolbar">
        <form
          className="search-form"
          onSubmit={(e) => {
            e.preventDefault();
            search();
          }}
        >
          <label className="search-input">
            <Icon name="search" size={19} />
            <span className="sr-only">Search Catalog</span>
            <input
              ref={searchInput}
              type="search"
              aria-describedby={`search-disclosure${text !== (request.text ?? "") ? " pending-search-text" : ""}`}
              maxLength={500}
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder="Search resources, descriptions or tags..."
            />
          </label>
          <button className="primary" type="submit">
            Search
          </button>
        </form>
        {stagedSearch && (
          <fieldset>
            <legend>Review this saved search</legend>
            <p>
              Search runs only after you choose Search. Current permissions and
              administrator-managed sources apply.
            </p>
            {liveConsentRequired && (
              <label>
                <input
                  type="checkbox"
                  checked={disclosureConsent}
                  onChange={(e) => {
                    setDisclosureConsent(e.target.checked);
                    setSearchArmed(false);
                  }}
                />
                Allow disclosure of this query to current administrator-managed
                live sources.
              </label>
            )}
            <label>
              <input
                type="checkbox"
                checked={providerConsent}
                onChange={(e) => {
                  setProviderConsent(e.target.checked);
                  setSearchArmed(false);
                }}
              />
              Allow processing by the configured embedding provider if hybrid
              search is enabled.
            </label>
            {!providerConsentRequired && (
              <p className="muted">
                Embedding-provider consent is not required for an explicit
                live-only Search. Local category suggestions still require it.
              </p>
            )}
          </fieldset>
        )}
        <SearchFilters
          searchText={request.text ?? ""}
          suggestionsAllowed={!stagedSearch || providerConsent}
          onRefresh={refreshResults}
          refreshDisabled={
            loading ||
            !searchArmed ||
            (!!stagedSearch &&
              ((!providerConsent && providerConsentRequired) ||
                (!disclosureConsent && liveConsentRequired)))
          }
          identityKey={JSON.stringify(access)}
          externalAllowed={externalAllowed}
          namespaceAllowed={
            !!access &&
            availablePermission(access, "namespace.read", "catalog.read")
          }
          client={client}
          onFailure={onFailure}
          value={request}
          tags={[
            ...new Set(data?.items.flatMap((item) => item.tags) ?? []),
          ].slice(0, 24)}
          publishers={publisherSuggestions(data?.items ?? [])}
          onApply={(filters) => setRequest(applyFilters(request, filters))}
        />
      </div>
      {request.origins?.length === 1 &&
        request.origins[0] === "live" &&
        !request.text && (
          <p className="notice">
            Enter search text to request live external results. Live results may
            change. Start a new search if a page can no longer be loaded.
          </p>
        )}
      {hasFilters && (
        <div
          className="applied-filters"
          role="group"
          aria-label="Applied search"
        >
          <span>Active filters</span>
          {request.origins?.map((origin) => (
            <button
              type="button"
              className="filter-chip"
              key={origin}
              aria-label={`Remove origin ${originLabel(origin)}`}
              disabled={!!request.namespaceId}
              title={
                request.namespaceId
                  ? "This namespace requires local results. Remove the namespace before changing origins."
                  : undefined
              }
              onClick={() => removeCriterion("origins", origin)}
            >
              <span>Origin: {originLabel(origin)}</span>
              <Icon name="close" size={12} />
            </button>
          ))}
          {request.namespaceId && (
            <button
              type="button"
              className="filter-chip"
              aria-label={`Remove local namespace ${request.namespaceId}`}
              onClick={() => removeCriterion("namespaceId")}
            >
              <span>
                Namespace:{" "}
                <SelectedNamespace
                  id={request.namespaceId}
                  label={
                    (namespaceLabel?.id === request.namespaceId
                      ? namespaceLabel.label
                      : undefined) ??
                    data?.items.find(
                      (item) =>
                        item.local?.listing.namespaceId === request.namespaceId,
                    )?.local?.display?.namespaceName
                  }
                  client={client}
                  access={access}
                  onFailure={onFailure}
                />
              </span>
              <Icon name="close" size={12} />
            </button>
          )}
          {request.text && (
            <button
              className="filter-chip"
              aria-label="Remove search text"
              onClick={() => removeCriterion("text")}
            >
              <span>{request.text}</span>
              <Icon name="close" size={12} />
            </button>
          )}
          {request.kinds?.map((k) => (
            <button
              className="filter-chip"
              key={k}
              aria-label={`Remove kind ${kindLabel(k)}`}
              onClick={() => removeCriterion("kinds", k)}
            >
              <span>{kindLabel(k)}</span>
              <Icon name="close" size={12} />
            </button>
          ))}
          {request.tags?.map((tag) => (
            <button
              className="filter-chip"
              key={tag}
              aria-label={`Remove tag ${tag}`}
              onClick={() => removeCriterion("tags", tag)}
            >
              <span>#{tag}</span>
              <Icon name="close" size={12} />
            </button>
          ))}
          {request.license && (
            <button
              className="filter-chip"
              aria-label="Remove license filter"
              onClick={() => removeCriterion("license")}
            >
              <span>License: {request.license}</span>
              <Icon name="close" size={12} />
            </button>
          )}
          {request.categories?.map((category) => (
            <button
              className="filter-chip"
              key={category}
              aria-label={`Remove category ${category}`}
              onClick={() => removeCriterion("categories", category)}
            >
              <span>Category: {category}</span>
              <Icon name="close" size={12} />
            </button>
          ))}
          {request.publisher && (
            <button
              className="filter-chip"
              aria-label="Remove publisher filter"
              onClick={() => removeCriterion("publisher")}
            >
              <span>Publisher: {request.publisher}</span>
              <Icon name="close" size={12} />
            </button>
          )}
          {request.visibility && (
            <button
              className="filter-chip"
              aria-label="Remove visibility filter"
              onClick={() => removeCriterion("visibility")}
            >
              <span>{request.visibility}</span>
              <Icon name="close" size={12} />
            </button>
          )}
          <button className="text-button" onClick={resetSearch}>
            <Icon name="close" size={13} />
            Clear all
          </button>
        </div>
      )}
      <div className="results-toolbar">
        <div className="result-summary" role="status">
          {loading && !data ? (
            "Finding resources..."
          ) : data ? (
            <>
              <strong>
                {data.liveCount ||
                shownQuery.origins?.every((origin) => origin === "live")
                  ? `${data.durableTotal} local and synced matches · ${data.liveCount} live on this page`
                  : `Showing ${data.items.length} of ${data.durableTotal} ${data.durableTotal === 1 ? "match" : "matches"}`}
              </strong>
              <SearchMode result={data} browsing={!shownQuery.text?.trim()} />
            </>
          ) : (
            "Results unavailable"
          )}
        </div>
        <div className="visibility-switch" role="group" aria-label="Visibility">
          {visibilityOptions.map((option) => (
            <button
              key={option.value}
              type="button"
              aria-label={`Visibility: ${option.label}`}
              aria-pressed={(request.visibility ?? "") === option.value}
              onClick={() =>
                setRequest(
                  applyFilters(request, {
                    ...request,
                    visibility: option.value || undefined,
                  }),
                )
              }
            >
              {option.value ? option.label : "All"}
            </button>
          ))}
        </div>
        <label className="catalog-order">
          <span>Sort by</span>
          <select
            aria-label="Sort results"
            value={request.order ?? ""}
            onChange={(event) =>
              changeOrder(
                (event.target.value || undefined) as CatalogOrder | undefined,
              )
            }
          >
            <option value="">
              {request.text?.trim() ? "Relevance" : "Default: Name (A–Z)"}
            </option>
            {catalogOrders.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        <div className="segmented" role="group" aria-label="Result layout">
          <button
            aria-label="Cards"
            title="Card view"
            aria-pressed={layout === "cards"}
            onClick={() => setLayout("cards")}
          >
            {controlIcons["catalog"]}
            <span>Cards</span>
          </button>
          <button
            aria-label="Table"
            title="Table view"
            aria-pressed={layout === "table"}
            onClick={() => setLayout("table")}
          >
            <Icon name="list" size={17} />
            <span>Table</span>
          </button>
        </div>
      </div>
      {data && (loading || error) && (
        <p className="notice" role="status">
          Last loaded results are shown.{" "}
          {loading
            ? "The requested page is loading."
            : "The requested page could not be loaded."}
        </p>
      )}
      <div className="discovery-actions">
        {data &&
          data.items.length > 0 &&
          (choices.length > 0 || comparisonMessage) && (
            <div className="comparison-toolbar">
              <button
                type="button"
                disabled={choices.length !== 2}
                onClick={() => setComparisonOpen(true)}
              >
                {controlIcons["compare"]} Compare selected ({choices.length}/2)
              </button>
              {choices.length > 0 && (
                <button
                  type="button"
                  onClick={() => {
                    setCompared([]);
                    setComparisonMessage("");
                  }}
                >
                  Clear comparison selection
                </button>
              )}
              <p role="status">
                {comparisonMessage ||
                  `${choices.length} of 2 results selected for comparison.`}
              </p>
            </div>
          )}
        {request.order &&
          (Boolean(request.text?.trim()) ||
            (externalAllowed && liveConsentRequired)) && (
            <p className="search-guidance">
              {request.text?.trim()
                ? "Column sorting uses keyword matches. Choose Relevance to include meaning-based search when available. "
                : ""}
              {externalAllowed && liveConsentRequired
                ? "Local and synced results appear first; live results follow."
                : ""}
            </p>
          )}
        {externalAllowed && liveConsentRequired && (
          <details className="search-guidance">
            <summary>How results are counted</summary>
            <p>
              The total includes matches from this Catalog and synced external
              catalogs. Live results are counted separately on each page.
            </p>
          </details>
        )}
      </div>
      {error && (
        <p role="alert" className="notice error">
          {error}{" "}
          {retryContinuation && (
            <>
              The previous page is still shown. Retry this page, or start a new
              search.{" "}
              <button
                onClick={() =>
                  setPage((page) => ({ ...page, request: { ...page.request } }))
                }
              >
                Retry this page
              </button>{" "}
            </>
          )}
          <button onClick={() => setRequest(firstPage(request))}>
            Start a new search
          </button>
        </p>
      )}
      {loading && !data && (
        <div className="card-grid" aria-hidden="true">
          {[1, 2, 3].map((n) => (
            <div className="resource-card skeleton" key={n}>
              <span />
              <span />
              <span />
              <span />
            </div>
          ))}
        </div>
      )}
      {data && (
        <>
          {data.partial && (
            <p className="notice warning">
              Some source queries or results are incomplete. Available results
              are shown.{" "}
              <button onClick={() => setRequest(firstPage(request))}>
                Retry search
              </button>
            </p>
          )}
          {data.diagnostics?.length ? (
            <details className="notice">
              <summary>Search diagnostics</summary>
              <ul>
                {data.diagnostics.map((item, index) => (
                  <li key={index}>
                    <p>
                      {searchDiagnostics[item.status]}{" "}
                      <code>{item.status}</code>
                    </p>
                    {item.message && <p>{item.message}</p>}
                    {item.sourceId ? (
                      <>
                        <CopyReference
                          label="source ID"
                          value={item.sourceId}
                        />
                        {access &&
                          permits(access, "source.read", "sources.read") && (
                            <a
                              href={`${runtime.basePath}/sources/${item.sourceId}`}
                            >
                              Open source
                            </a>
                          )}
                      </>
                    ) : (
                      <p className="muted">No source ID supplied.</p>
                    )}
                  </li>
                ))}
              </ul>
            </details>
          ) : null}
          {data.items.length === 0 ? (
            <section className="empty">
              <span className="empty-icon">
                <Icon name="search" size={30} />
              </span>
              <h2>No resources found</h2>
              {access &&
                !availablePermission(
                  access,
                  "resource.read",
                  "catalog.read",
                ) && (
                  <p className="notice">
                    Your current permissions do not include local resources. An
                    administrator can help you get access. External results also
                    depend on your permissions and the sources enabled for this
                    Catalog.
                  </p>
                )}
              <p>
                {hasFilters
                  ? "No visible resources match this search. Try fewer filters or another search term."
                  : "No discoverable resources were returned. Drafts and unavailable sources may not appear here."}
              </p>
              {hasFilters ? (
                <button onClick={resetSearch}>
                  Clear search &amp; filters
                </button>
              ) : (
                <p>Use Refresh results to check again.</p>
              )}
              {access && resourceCreationAccess(access) !== "denied" && (
                <a
                  className="button"
                  href={`${runtime.basePath}/resources/new`}
                >
                  {resourceCreationAccess(access) === "allowed"
                    ? "Create a resource"
                    : "Choose a namespace"}
                </a>
              )}
            </section>
          ) : layout === "cards" ? (
            <div className="card-grid">
              {data.items.map((item) => (
                <article
                  className="resource-card"
                  data-kind={item.kind}
                  key={item.id}
                >
                  <div className="card-heading">
                    <ArtworkIcon
                      client={client}
                      item={item}
                      kind={item.kind}
                      onFailure={onFailure}
                    />
                    <h2>
                      <button
                        className="title-button"
                        onClick={() => preview(item)}
                      >
                        {item.displayName}
                      </button>
                    </h2>
                  </div>
                  <p className="description">
                    {item.description || "No description provided."}
                  </p>
                  <div className="card-badges">
                    <span className={`badge catalog-kind-badge ${item.kind}`}>
                      {kindLabel(item.kind)}
                    </span>
                    <span className="badge">{originLabel(item.type)}</span>
                    {item.local && (
                      <span
                        className={`state-badge ${item.local.release.state}`}
                      >
                        <Icon
                          name={
                            item.local.release.state === "published"
                              ? "check"
                              : "warning"
                          }
                          size={12}
                        />
                        {item.local.release.state}
                      </span>
                    )}
                    <span className="badge">
                      <span>Version:</span>{" "}
                      <span className="version-label">
                        {item.versionPresent
                          ? item.version || "Empty version"
                          : "No version supplied"}
                      </span>
                    </span>
                  </div>
                  <RatingLabels item={item} basePath={runtime.basePath} />
                  <Facts
                    className="catalog-presentation"
                    rows={[
                      [
                        "Publisher",
                        item.local?.display?.publisherName ||
                          item.publisher ||
                          "Not supplied",
                      ],
                    ]}
                  />
                  {item.local && (
                    <CatalogPresentation
                      display={item.local.display}
                      includePublisher={false}
                      includeLicense={false}
                      namespaceId={item.local.listing.namespaceId}
                      onNamespace={selectNamespace}
                    />
                  )}
                  <div className="tags">
                    {item.tags.slice(0, 4).map((tag) => (
                      <button
                        type="button"
                        className="tag-filter"
                        key={tag}
                        aria-label={`Filter by tag ${tag}`}
                        aria-pressed={request.tags?.includes(tag) ?? false}
                        title={
                          request.tags?.includes(tag)
                            ? "Tag already applied"
                            : "Filter by this tag"
                        }
                        disabled={
                          !request.tags?.includes(tag) &&
                          ((request.tags?.length ?? 0) >= 32 ||
                            [...tag].length > 80)
                        }
                        onClick={() => addTag(tag)}
                      >
                        {tag}
                      </button>
                    ))}
                    {item.tags.length > 4 && (
                      <button
                        type="button"
                        className="text-button"
                        aria-label={`Show all tags for ${item.displayName}`}
                        onClick={() => preview(item)}
                      >
                        +{item.tags.length - 4} more
                      </button>
                    )}
                  </div>
                  <footer>
                    {comparisonChoice(item, data.items.indexOf(item))}
                    {item.local && (
                      <span className="card-footnote">
                        <Icon
                          name={
                            item.local.listingRevision.visibility === "public"
                              ? "globe"
                              : "access"
                          }
                          size={14}
                        />
                        <span className="sr-only">Visibility: </span>
                        {item.local.listingRevision.visibility}
                      </span>
                    )}
                    <span className="card-arrow" aria-hidden="true">
                      Preview
                      <Icon name="arrow" size={17} />
                    </span>
                  </footer>
                </article>
              ))}
            </div>
          ) : (
            <div
              className="table-scroll catalog-table"
              role="region"
              aria-label="Catalog results table"
              tabIndex={0}
            >
              <table>
                <thead>
                  <tr>
                    {sortableHeading("Name", "name")}
                    {sortableHeading("Kind", "kind")}
                    <th scope="col">Status</th>
                    {sortableHeading("Publisher", "publisher")}
                    <th>Version</th>
                    <th>Origin</th>
                    <th>Compare</th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((item) => (
                    <tr key={item.id}>
                      <td>
                        <div className="table-resource">
                          <span className={`kind-icon ${item.kind}`}>
                            <Icon name={item.kind} size={21} />
                          </span>
                          <div>
                            <button
                              className="title-button"
                              onClick={() => preview(item)}
                            >
                              {item.displayName}
                            </button>
                            <p>
                              {item.description || "No description provided."}
                            </p>
                            <RatingLabels
                              item={item}
                              basePath={runtime.basePath}
                            />
                            {item.local && (
                              <CatalogPresentation
                                display={item.local.display}
                                namespaceId={item.local.listing.namespaceId}
                                onNamespace={selectNamespace}
                                includePublisher={false}
                              />
                            )}
                          </div>
                        </div>
                      </td>
                      <td>
                        <span
                          className={`badge catalog-kind-badge ${item.kind}`}
                        >
                          {kindLabel(item.kind)}
                        </span>
                      </td>
                      <td>
                        {item.local ? (
                          <div
                            className={`state-badge ${item.local.release.state}`}
                          >
                            <Icon
                              name={
                                item.local.release.state === "published"
                                  ? "check"
                                  : "warning"
                              }
                              size={12}
                            />
                            {item.local.release.state}
                          </div>
                        ) : (
                          <span className="muted">Not reported</span>
                        )}
                      </td>
                      <td>
                        <span className="publisher-id" title={item.publisher}>
                          {item.local?.display?.publisherName ||
                            item.publisher ||
                            "Not supplied"}
                        </span>
                      </td>
                      <td>
                        {item.versionPresent
                          ? item.version || "Empty version"
                          : "No version supplied"}
                      </td>
                      <td>
                        <span className="badge">{originLabel(item.type)}</span>
                      </td>
                      <td>
                        {comparisonChoice(item, data.items.indexOf(item))}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="pagination">
            {request.cursor && (
              <button onClick={() => setRequest(firstPage(request))}>
                First page
              </button>
            )}
            {previous.length > 0 && (
              <button
                disabled={loading || !!error}
                onClick={() => {
                  const cursor = previous[previous.length - 1]!;
                  setPage({
                    request: {
                      ...firstPage(request),
                      ...(cursor ? { cursor } : {}),
                    },
                    previous: previous.slice(0, -1),
                  });
                }}
              >
                Previous page
              </button>
            )}
            {data.nextCursor && (
              <button
                disabled={loading || !!error}
                onClick={() =>
                  setPage({
                    request: {
                      ...firstPage(request),
                      // This button is rendered only when the captured page has a cursor.
                      cursor: data.nextCursor!,
                    },
                    previous: [...previous, request.cursor ?? ""].slice(-10),
                  })
                }
              >
                Next page &rarr;
              </button>
            )}
          </div>
        </>
      )}
      {selected && (
        <Preview
          key={selected.item.id}
          client={client}
          onFailure={onFailure}
          item={selected.item}
          searchRequest={selected.request}
          access={access}
          basePath={runtime.basePath}
          tags={request.tags ?? []}
          onTag={addTag}
          categories={request.categories ?? []}
          onCategory={addCategory}
          documentationUrl={runtime.documentationUrl}
          onNamespace={selectNamespace}
          close={() => {
            if (confirmDiscard()) setSelected(undefined);
          }}
        />
      )}
      {comparisonOpen && comparisonCurrent && choices.length === 2 && (
        <ResultComparison
          items={choices}
          access={access}
          basePath={runtime.basePath}
          returnFocus={`${comparisonId}-${data!.items.findIndex((item) => comparisonIdentity(item) === comparisonIdentity(choices[0]!))}`}
          close={() => setComparisonOpen(false)}
        />
      )}
    </div>
  );
  return (
    <div onClickCapture={navigate}>
      {(isExplore || exploreVisited) && access && (
        <div hidden={!isExplore}>
          <Suspense fallback={<p role="status">Loading local catalog...</p>}>
            <Explore
              client={client}
              runtime={runtime}
              access={access}
              id=""
              onFailure={onFailure}
              onCatalogSearch={(query) => {
                setText(query.text ?? "");
                setRequest({ ...query, origins: ["local"], limit: 24 });
                returnScroll.current.catalog = 0;
                focusFilteredSearch.current = true;
                move(`${runtime.basePath}/catalog`);
              }}
            />
          </Suspense>
        </div>
      )}
      {content}
    </div>
  );
}

/** Show the selected result in a modal and restore focus without prefetching private history. */
function Preview({
  client,
  onFailure,
  item: initialItem,
  searchRequest,
  access,
  basePath,
  close,
  tags,
  onTag,
  onNamespace,
  categories,
  onCategory,
  documentationUrl,
}: {
  client: Client;
  onFailure: (error: unknown) => void;
  item: SearchResult;
  searchRequest: components["schemas"]["CatalogSearch"];
  access: components["schemas"]["CurrentAccess"] | undefined;
  basePath: string;
  close: () => void;
  tags: string[];
  onTag: (tag: string) => void;
  onNamespace?: (namespaceId: string) => void;
  categories: string[];
  onCategory: (category: string) => void;
  documentationUrl: string;
}) {
  const [item, setItem] = useState(initialItem);
  const artworkTarget = foreignArtworkTarget(item);
  const [refreshingProof, setRefreshingProof] = useState(false);
  const [proofMessage, setProofMessage] = useState("");
  const proofRequest = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => proofRequest.current?.abort(), []);
  async function refreshProof() {
    const controller = new AbortController();
    proofRequest.current = controller;
    setRefreshingProof(true);
    setProofMessage("Refreshing live artwork...");
    try {
      const response = result(
        await client.api.POST("/api/v1/catalog/search", {
          body: searchRequest,
          signal: controller.signal,
        }),
        checks.CatalogSearchResponse,
      );
      if (controller.signal.aborted) return;
      const binding = JSON.stringify(artworkTarget);
      const fresh = response.items.find(
        (candidate) =>
          binding !== undefined &&
          candidate.type === "live" &&
          JSON.stringify(foreignArtworkTarget(candidate)) === binding &&
          !!candidate.artwork?.receipt,
      );
      if (fresh) {
        setItem(fresh);
        setProofMessage("Live artwork refreshed.");
      } else
        setProofMessage(
          "This source entry is no longer on the captured result page. Start a new search and open the current result before saving.",
        );
    } catch (cause) {
      if (!controller.signal.aborted) {
        onFailure(cause);
        setProofMessage(
          "Could not refresh live artwork. Try again or start a new search.",
        );
      }
    } finally {
      if (!controller.signal.aborted) setRefreshingProof(false);
    }
  }
  const [record, setRecord] = useState<RecordDetail>();
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const failure = useRef(onFailure);
  failure.current = onFailure;
  const releaseID = item.local?.release.id;
  useEffect(() => {
    if (!releaseID) return;
    const controller = new AbortController();
    let current = true;
    setRecord(undefined);
    setError("");
    client.api
      .GET("/api/v1/catalog/resources/{catalogRecordId}", {
        params: { path: { catalogRecordId: releaseID } },
        signal: controller.signal,
      })
      .then((reply) => {
        if (current)
          setRecord(
            result(reply, (value) =>
              recordForRequest(value, releaseID, access?.tenantId, item),
            ),
          );
      })
      .catch((e) => {
        if (!current) return;
        failure.current(e);
        setError(
          e instanceof APIError
            ? `${e.message}${e.requestId ? ` Request: ${e.requestId}` : ""}`
            : "The Catalog could not be reached.",
        );
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [client, releaseID, revision, access?.tenantId, item]);
  const dialog = useRef<HTMLDialogElement>(null);
  const outsidePointer = useRef<number | undefined>(undefined);
  function outside(event: React.PointerEvent<HTMLDialogElement>) {
    const box = event.currentTarget.getBoundingClientRect();
    return (
      event.target === event.currentTarget &&
      (event.clientX < box.left ||
        event.clientX > box.right ||
        event.clientY < box.top ||
        event.clientY > box.bottom)
    );
  }
  useEffect(() => {
    const previousFocus = document.activeElement;
    dialog.current?.showModal();
    const element = dialog.current;
    return () => {
      element?.close();
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected)
        previousFocus.focus({ preventScroll: true });
    };
  }, []);
  return (
    <dialog
      className="detail-dialog"
      ref={dialog}
      aria-labelledby="preview-title"
      onCancel={(event) => {
        event.preventDefault();
        event.stopPropagation();
        close();
      }}
      onPointerDown={(event) => {
        outsidePointer.current =
          event.isPrimary && event.button === 0 && outside(event)
            ? event.pointerId
            : undefined;
        // Suppress touch-generated mouse focus on the backdrop; otherwise it
        // can undo the opener's restored focus after pointerup closes the dialog.
        if (outsidePointer.current !== undefined) event.preventDefault();
      }}
      onPointerUp={(event) => {
        const dismiss =
          outsidePointer.current !== undefined &&
          outsidePointer.current === event.pointerId &&
          outside(event);
        outsidePointer.current = undefined;
        if (dismiss) close();
      }}
      onPointerCancel={() => {
        outsidePointer.current = undefined;
      }}
    >
      <div className="dialog-toolbar">
        <span className="eyebrow">RESOURCE PREVIEW</span>
        <button
          className="icon-button"
          onClick={close}
          aria-label="Close preview"
        >
          <Icon name="close" />
        </button>
      </div>
      <div className="preview-content">
        <div className="preview-heading">
          <span className={`kind-icon ${item.kind}`}>
            <Icon name={item.kind} size={28} />
          </span>
          <span className="kind-label">{kindLabel(item.kind)}</span>
          <span className="badge">{originLabel(item.type)}</span>
        </div>
        <h2 className="preview-title" id="preview-title">
          {item.displayName}
        </h2>
        <p className="detail-description">
          {item.description || "No description supplied."}
        </p>
        {item.tags.length > 0 && (
          <div className="tags">
            {item.tags.map((tag) => (
              <button
                key={tag}
                type="button"
                className="tag-filter"
                aria-label={`Filter by tag ${tag}`}
                aria-pressed={tags.includes(tag)}
                disabled={
                  !tags.includes(tag) &&
                  (tags.length >= 32 || [...tag].length > 80)
                }
                onClick={() => onTag(tag)}
              >
                {tag}
              </button>
            ))}
          </div>
        )}
        <h3 className="section-heading">Overview</h3>
        <Facts
          className="metadata-grid"
          rows={[
            [
              "Version",
              item.versionPresent
                ? item.version || "Explicit empty version"
                : "Not specified",
            ],
            [
              "Publisher",
              item.local?.display?.publisherName ||
                item.publisher ||
                "Not supplied",
            ],
          ]}
        />
        {item.local ? (
          <>
            {!record && !error && <p role="status">Loading record...</p>}
            {error && (
              <>
                <p role="alert" className="notice error">
                  {error}
                </p>
                <button onClick={() => setRevision((value) => value + 1)}>
                  Retry record
                </button>
              </>
            )}
            {record && (
              <>
                <Record
                  onNamespace={onNamespace}
                  record={record}
                  client={client}
                  onFailure={onFailure}
                  basePath={basePath}
                  access={access}
                  categories={categories}
                  onCategory={onCategory}
                  documentationUrl={documentationUrl}
                  compact
                />
                <RatingEntry
                  client={client}
                  access={access}
                  onFailure={onFailure}
                  request={requestForRecord(record)}
                  namespaceId={record.resource.namespaceId}
                />
              </>
            )}
          </>
        ) : (
          <>
            <p className="notice">
              External metadata is not a locally reviewed or approved resource.
            </p>
            {item.type === "live" && (
              <p className="eyebrow">Last loaded live result</p>
            )}
            <IndexedProvenance
              item={item}
              access={access}
              basePath={basePath}
            />
            {item.type === "live" && (
              <>
                <LiveRefreshNotice />
                {!artworkTarget && (
                  <p className="notice">
                    Exact source and generation are unavailable. Search again
                    before refreshing artwork.
                  </p>
                )}
                <button
                  type="button"
                  disabled={refreshingProof || !artworkTarget}
                  aria-busy={refreshingProof}
                  onClick={() => void refreshProof()}
                >
                  {controlIcons["refresh"]} Refresh live artwork
                </button>
                {proofMessage && <p role="status">{proofMessage}</p>}
              </>
            )}
            <ArtworkGallery client={client} item={item} onFailure={onFailure} />
            <SourceArtworkControls
              client={client}
              access={access}
              item={item}
              onFailure={onFailure}
            />
            <RatingLabels item={item} basePath={basePath} />
            <RatingEntry
              client={client}
              access={access}
              onFailure={onFailure}
              request={requestForResult(item)}
              live={!!item.live}
              refreshProof={
                item.live
                  ? async (signal) => {
                      const reply = result(
                        await client.api.POST("/api/v1/catalog/search", {
                          body: searchRequest,
                          signal,
                        }),
                        checks.CatalogSearchResponse,
                      );
                      const found = reply.items.find(
                        (candidate) =>
                          candidate.type === "live" &&
                          candidate.live?.sourceId === item.live?.sourceId &&
                          candidate.identifier === item.identifier &&
                          candidate.versionPresent === item.versionPresent &&
                          (candidate.version ?? "") === (item.version ?? "") &&
                          candidate.kind === item.kind &&
                          candidate.live?.presentationGeneration ===
                            item.live?.presentationGeneration,
                      );
                      return found ? requestForResult(found) : undefined;
                    }
                  : undefined
              }
            />
            <ExternalHandoff item={item} documentationUrl={documentationUrl} />
            <h3 className="section-heading">Technical details and evidence</h3>
            {item.indexed && (
              <Facts
                className="metadata-grid"
                rows={[
                  [
                    "Entry digest (not a document checksum)",
                    <code>{item.indexed.normalizedEntryDigest}</code>,
                  ],
                ]}
              />
            )}
            {item.live && (
              <details>
                <summary>Live entry metadata</summary>
                <JSONBlock value={item.live.entry} />
              </details>
            )}
          </>
        )}
      </div>
      {item.indexed && (
        <div className="preview-actions">
          <a
            className="button primary"
            href={`${basePath}/indexed-record?${new URLSearchParams({ identifier: item.identifier, versionPresent: String(item.versionPresent), ...(item.versionPresent ? { version: item.version || "" } : {}) })}`}
          >
            Open current indexed record {controlIcons["arrow"]}
          </a>
        </div>
      )}
      {item.local && (
        <div className="preview-actions">
          <a
            className="button primary"
            href={`${basePath}/catalog/${item.local.release.id}`}
          >
            Open full record {controlIcons["arrow"]}
          </a>
          <p className="muted">
            Open a full record and return in this tab to keep your search.
          </p>
        </div>
      )}
    </dialog>
  );
}

/** Render a local Catalog record with explicit links to its retained governance history. */
type RecordProps = {
  record: RecordDetail;
  client: Client;
  onFailure: (error: unknown) => void;
  basePath: string;
  compact?: boolean;
  tags?: string[];
  onTag?: (tag: string) => void;
  categories?: string[];
  onCategory?: ((category: string) => void) | undefined;
  onNamespace?: ((namespaceId: string) => void) | undefined;
  access?: components["schemas"]["CurrentAccess"] | undefined;
  documentationUrl: string;
};
function Record(props: RecordProps) {
  return props.access ? (
    <RecordWithAccess {...props} access={props.access} />
  ) : (
    <RecordContent {...props} />
  );
}
function RecordWithAccess(
  props: RecordProps & { access: components["schemas"]["CurrentAccess"] },
) {
  const permissionState = useNamespaceAccessState(
    props,
    props.record.resource.namespaceId,
  );
  return <RecordContent {...props} permissionState={permissionState} />;
}
function RecordContent({
  record,
  client,
  onFailure,
  basePath,
  compact = false,
  tags = [],
  onTag,
  onNamespace,
  access,
  categories = [],
  onCategory,
  permissionState,
  documentationUrl,
}: RecordProps & {
  permissionState?: ReturnType<typeof useNamespaceAccessState>;
}) {
  const listing = record.listingRevision;
  const SectionHeading = compact ? "h3" : "h2";
  return (
    <section
      className={compact ? "record compact-record" : "record full-record"}
    >
      {!compact && (
        <>
          <div className="preview-heading">
            <span className={`kind-icon ${record.resource.kind}`}>
              <Icon name={record.resource.kind} size={28} />
            </span>
            <span>{kindLabel(record.resource.kind)}</span>
            <span className="badge">{originLabel("local")}</span>
          </div>
          <h1>{listing.displayName}</h1>
          <p className="detail-description">{listing.description}</p>
        </>
      )}
      {!compact && <RecordNavigation sections={localRecordSections} />}
      <ArtworkGallery client={client} item={record} onFailure={onFailure} />
      {!compact && onTag && listing.tags?.length ? (
        <div className="tags">
          {listing.tags.map((tag) => (
            <button
              key={tag}
              type="button"
              className="tag-filter"
              aria-label={`Filter by tag ${tag}`}
              aria-pressed={tags.includes(tag)}
              disabled={
                !tags.includes(tag) &&
                (tags.length >= 32 || [...tag].length > 80)
              }
              onClick={() => onTag(tag)}
            >
              {tag}
            </button>
          ))}
        </div>
      ) : null}
      {!compact && (
        <SectionHeading
          className="section-heading record-section-target"
          id="record-overview"
          tabIndex={-1}
        >
          Overview
        </SectionHeading>
      )}
      <Facts
        className="metadata-grid"
        rows={[
          ...(!compact
            ? ([
                [
                  "Canonical identifier",
                  <code>{record.resource.canonicalIdentifier}</code>,
                ],
                ["Version", record.version.version],
              ] as const)
            : []),
          [
            "Release state",
            <span className={`state-badge ${record.release.state}`}>
              <Icon
                name={
                  record.release.state === "published" ? "check" : "warning"
                }
                size={12}
              />
              {record.release.state}
            </span>,
          ],
          ["Visibility", listing.visibility],
          [
            "Declared listing license",
            compact &&
            (record.display?.licenseOmitted ||
              [...(listing.license || "")].length > 256)
              ? "See full record for the complete declaration."
              : listing.license || "Not supplied",
          ],
          ["Published", new Date(record.release.activatedAt).toLocaleString()],
        ]}
      />
      <CatalogPresentation
        display={record.display}
        namespaceId={record.listing.namespaceId}
        onNamespace={onNamespace}
        includePublisher={!compact}
        includeLicense={false}
      />
      <section aria-label="Listing categories">
        <h3 className="section-heading">Listing categories</h3>
        {listing.categories?.length ? (
          <>
            <p className="muted">
              Choose a category to add it to the applied search. All selected
              categories must match; Search trims and lowercases values.
            </p>
            <ul className="record-category-list">
              {listing.categories.map((category, index) => (
                <li key={index}>
                  {category.trim() ? (
                    onCategory ? (
                      <button
                        type="button"
                        className="tag-filter"
                        aria-label={`Filter by category ${category}`}
                        aria-pressed={categories.includes(category)}
                        disabled={
                          !categories.includes(category) &&
                          categories.length >= 16
                        }
                        onClick={() => onCategory(category)}
                      >
                        {category}
                      </button>
                    ) : (
                      <span className="badge">{category}</span>
                    )
                  ) : (
                    <span>
                      Empty category declaration; no filter available.
                    </span>
                  )}
                </li>
              ))}
            </ul>
            {categories.length >= 16 && (
              <p className="muted">
                All 16 category filter slots are used. Remove a filter before
                adding another.
              </p>
            )}
          </>
        ) : (
          <p className="muted">No Listing categories supplied.</p>
        )}
      </section>
      <LocalAssessment
        evidence={record.evidence}
        compact={compact}
        headingId={compact ? undefined : "record-assessments"}
      />
      {access && (
        <SaveResource
          client={client}
          access={access}
          basePath={basePath}
          onFailure={onFailure}
          label={listing.displayName}
          reference={{
            origin: "local",
            releaseId: record.release.id,
            versionRevisionId: record.versionRevision.id,
          }}
        />
      )}
      <LocalHandoff
        key={record.release.id}
        record={record}
        client={client}
        onFailure={onFailure}
        compact={compact}
        headingId={compact ? undefined : "record-handoff"}
        permissionState={permissionState}
        documentationUrl={documentationUrl}
      />
      <SectionHeading
        className="section-heading record-section-target"
        id={compact ? undefined : "record-evidence"}
        tabIndex={compact ? undefined : -1}
      >
        Technical details and evidence
      </SectionHeading>
      <details>
        <summary>Exact release and revision IDs</summary>
        <CopyReference label="Release ID" value={record.release.id} />
        <CopyReference
          label="Version revision ID"
          value={record.versionRevision.id}
        />
        <CopyReference
          label="Listing revision ID"
          value={record.listingRevision.id}
        />
      </details>
      {!compact &&
        permissionState &&
        !permissionState.busy &&
        !permissionState.error && (
          <DraftDownload
            heading="h3"
            client={client}
            access={permissionState.access}
            namespaceId={record.resource.namespaceId}
            revisionId={record.versionRevision.id}
            listingRevisionId={record.listingRevision.id}
            onFailure={onFailure}
          />
        )}
      <RecordWorkflows
        record={record}
        client={client}
        access={access}
        basePath={basePath}
        compact={compact}
        onFailure={onFailure}
        state={permissionState}
      />
      {!compact && (
        <Facts
          className="metadata-grid"
          rows={[
            [
              "Publisher ID",
              <>
                <code>{listing.publisherId}</code>
              </>,
            ],
          ]}
        />
      )}
      <details>
        <summary>Validation evidence</summary>
        <JSONBlock value={record.evidence} />
      </details>
      {!compact && (
        <details>
          <summary>Complete record metadata</summary>
          <JSONBlock value={record} />
        </details>
      )}
    </section>
  );
}
