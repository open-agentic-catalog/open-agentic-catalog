import { controlIcons } from "../shell/Icon";
// Connected OpenAPI browsing keeps descriptions and schemas as read-only data.
import { useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { JSONBlock } from "./CodeBlock";
import { OperationContract } from "./OperationContract";
import { SchemaSummary } from "./SchemaSummary";
import {
  definitionAnchor,
  definitionIndex,
  definitionPageSize,
  linkedDefinition,
  matchingDefinitions,
  operationAnchor,
} from "../api/definitions";
import type { Client } from "../api/client";
import {
  areaAnchor,
  matches,
  object,
  operationAreas,
  operations,
  references,
  referenceValue,
  text,
} from "../api/reference";
import type { Document, Operation } from "../api/reference";
import { Field, Heading, ReadState, useRead } from "./workspace";
import "./api-reference.css";

/** Load the connected API's read-only contract projection through the authenticated host. */
export default function APIReference({
  client,
  onFailure,
  basePath = "",
}: {
  client: Client;
  basePath?: string;
  onFailure: (e: unknown) => void;
}) {
  const contract = useRead(
    "reference",
    (signal) => client.reference(signal),
    onFailure,
    true,
  );
  return (
    <>
      <Heading
        title="API reference"
        icon="book"
        description="Browse the connected Catalog's endpoints, authentication, request types and responses."
      />
      <ReadState {...contract} />
      <button disabled={contract.busy} onClick={contract.refresh}>
        {controlIcons["refresh"]} Refresh contract
      </button>
      {contract.data !== undefined && (
        <p className="eyebrow">
          Last loaded API contract
          {contract.busy
            ? "; refreshing. The loaded contract remains available."
            : contract.error
              ? "; refresh failed. The loaded contract remains available."
              : "."}
        </p>
      )}
      {contract.data !== undefined && (
        <Reference
          document={object(object(contract.data).document)}
          yaml={text(object(contract.data).yaml)}
          basePath={basePath}
        />
      )}
    </>
  );
}

/** Browse filtered operations and offer an exact-source contract download. */
function Reference({
  document,
  yaml,
  basePath,
}: {
  document: Document;
  yaml: string;
  basePath: string;
}) {
  const [query, setQuery] = useState("");
  const [tag, setTag] = useState("");
  const [method, setMethod] = useState("");
  const [selected, setSelected] = useState("");
  const [download, setDownload] = useState<{ yaml: string; url: string }>();
  const [workspace, setWorkspace] = useState<"endpoints" | "definitions">(
    "endpoints",
  );
  const [definitionQuery, setDefinitionQuery] = useState("");
  const [definitionKind, setDefinitionKind] = useState("");
  const [definitionPage, setDefinitionPage] = useState(0);
  const [notices, setNotices] = useState<string[]>([]);
  const [linkNotice, setLinkNotice] = useState("");
  const [hiddenOperation, setHiddenOperation] = useState("");
  const [origin, setOrigin] = useState("");
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  const [activeArea, setActiveArea] = useState("");
  const [focusTarget, setFocusTarget] = useState<{
    kind:
      | "definition"
      | "definition-index"
      | "operation"
      | "area"
      | "index"
      | "area-toggle";
    id: string;
  }>();
  const definition = useRef<HTMLElement>(null);
  const definitionWorkspace = useRef<HTMLElement>(null);
  const endpoints = useRef<HTMLElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const items = useMemo(() => operations(document), [document]);
  const areas = useMemo(
    () => operationAreas(document, items),
    [document, items],
  );
  const methods = [
    "DELETE",
    "GET",
    "HEAD",
    "OPTIONS",
    "PATCH",
    "POST",
    "PUT",
    "TRACE",
  ];
  const filtered = useMemo(
    () => items.filter((operation) => matches(operation, query, tag, method)),
    [items, query, tag, method],
  );
  const visibleAreas = useMemo(() => {
    const included = new Set(filtered);
    return areas
      .map((area) => ({
        ...area,
        operations: area.operations.filter((item) => included.has(item)),
      }))
      .filter((area) => area.operations.length);
  }, [areas, filtered]);
  const currentArea = visibleAreas.some((area) => area.id === activeArea)
    ? activeArea
    : (visibleAreas[0]?.id ?? "");
  const definitions = useMemo(() => definitionIndex(document), [document]);
  const definitionKinds = useMemo(
    () => Object.keys(object(document.components)).sort(),
    [document],
  );
  const matching = useMemo(
    () => matchingDefinitions(definitions, definitionQuery, definitionKind),
    [definitions, definitionQuery, definitionKind],
  );
  const pageCount = Math.max(
    1,
    Math.ceil(matching.length / definitionPageSize),
  );
  const pageIndex = Math.min(definitionPage, pageCount - 1);
  const definitionRows = matching.slice(
    pageIndex * definitionPageSize,
    (pageIndex + 1) * definitionPageSize,
  );
  const selectedKnown =
    selected.startsWith("#/") &&
    referenceValue(document, selected) !== undefined;
  const definitionChoices =
    selected && !definitionRows.some((row) => row.reference === selected)
      ? [selected, ...definitionRows.map((row) => row.reference)]
      : definitionRows.map((row) => row.reference);
  const navigation = useRef({ document, items, areas, query, tag, method });
  navigation.current = { document, items, areas, query, tag, method };
  const previousDocument = useRef(document);
  const setOpen = (id: string, open: boolean) =>
    setExpanded((current) => {
      if (current.has(id) === open) return current;
      const next = new Set(current);
      if (open) next.add(id);
      else next.delete(id);
      return next;
    });
  useEffect(() => {
    const readFragment = () => {
      const { document, items, areas, query, tag, method } = navigation.current;
      let id: string;
      try {
        id = decodeURIComponent(location.hash.slice(1));
      } catch {
        setSelected("");
        setHiddenOperation("");
        setLinkNotice("This link is unavailable in the current contract.");
        return;
      }
      const operation = items.find((item) => operationAnchor(item) === id);
      const area = areas.find((item) => item.id === id);
      const schema = linkedDefinition(document, id);
      const savedOrigin = object(history.state).apiReferenceOrigin;
      setOrigin(
        items.some((item) => operationAnchor(item) === savedOrigin)
          ? text(savedOrigin)
          : "",
      );
      if (operation) {
        setWorkspace("endpoints");
        setLinkNotice("");
        setHiddenOperation(matches(operation, query, tag, method) ? "" : id);
        const home = areaAnchor(operation.tags.at(-1));
        setCollapsed(
          (current) => new Set([...current].filter((id) => id !== home)),
        );
        setActiveArea(home);
        setExpanded((current) => new Set([...current, id]));
        setFocusTarget({
          kind: matches(operation, query, tag, method) ? "operation" : "index",
          id: matches(operation, query, tag, method) ? id : "api-area-index",
        });
      } else if (schema) {
        setWorkspace("definitions");
        setLinkNotice("");
        setHiddenOperation("");
        setSelected(schema);
        setFocusTarget({ kind: "definition", id });
      } else if (area) {
        const visible = area.operations.some((item) =>
          matches(item, query, tag, method),
        );
        setWorkspace("endpoints");
        setHiddenOperation("");
        setLinkNotice(
          visible
            ? ""
            : "This API section has no operations matching current filters. Use an index or change the filters.",
        );
        setCollapsed(
          (current) => new Set([...current].filter((id) => id !== area.id)),
        );
        setActiveArea(area.id);
        setFocusTarget({
          kind: visible ? "area" : "index",
          id: visible ? id : "api-area-index",
        });
      } else if (id === "api-area-index") {
        setWorkspace("endpoints");
        setLinkNotice("");
        setHiddenOperation("");
        setFocusTarget({ kind: "index", id });
      } else if (id === "api-definitions") {
        setWorkspace("definitions");
        setLinkNotice("");
        setHiddenOperation("");
        setFocusTarget({ kind: "definition-index", id });
      } else if (!id) {
        setWorkspace("endpoints");
        setLinkNotice("");
        setHiddenOperation("");
      } else {
        setSelected("");
        setHiddenOperation("");
        setLinkNotice(
          "This link is unavailable in the current contract. Open an index to choose a current destination.",
        );
      }
    };
    readFragment();
    window.addEventListener("popstate", readFragment);
    window.addEventListener("hashchange", readFragment);
    return () => {
      window.removeEventListener("popstate", readFragment);
      window.removeEventListener("hashchange", readFragment);
    };
  }, [document]);
  useEffect(() => {
    if (previousDocument.current === document) return;
    previousDocument.current = document;
    const changes: string[] = [];
    if (tag && !areas.some((area) => area.tag === tag)) {
      setTag("");
      changes.push("The selected API area was removed. All areas now applies.");
    }
    if (definitionKind && !definitionKinds.includes(definitionKind)) {
      setDefinitionKind("");
      changes.push(
        "The selected component kind was removed. All component kinds now applies.",
      );
    }
    if (selected && referenceValue(document, selected) === undefined) {
      setSelected("");
      changes.push(
        "The selected definition was removed from the refreshed contract.",
      );
    }
    if (origin && !items.some((item) => operationAnchor(item) === origin)) {
      setOrigin("");
      changes.push(
        "The originating operation was removed from the refreshed contract.",
      );
    }
    const validOperations = new Set(items.map(operationAnchor));
    const validAreas = new Set(areas.map((area) => area.id));
    setExpanded(
      (current) =>
        new Set([...current].filter((id) => validOperations.has(id))),
    );
    setCollapsed(
      (current) => new Set([...current].filter((id) => validAreas.has(id))),
    );
    setNotices(changes);
  }, [
    document,
    areas,
    definitionKind,
    definitionKinds,
    items,
    origin,
    selected,
    tag,
  ]);
  useEffect(() => {
    if (!focusTarget) return;
    const element =
      focusTarget.kind === "definition"
        ? definition.current
        : focusTarget.kind === "definition-index"
          ? definitionWorkspace.current
          : documentElement(focusTarget.id);
    const target =
      focusTarget.kind === "operation"
        ? element?.querySelector("summary")
        : focusTarget.kind === "area-toggle"
          ? element?.closest("summary")
          : element;
    target?.focus();
    target?.scrollIntoView?.({ block: "start" });
  }, [focusTarget, workspace]);
  useEffect(() => {
    if (
      typeof IntersectionObserver === "undefined" ||
      workspace !== "endpoints"
    )
      return;
    const showing = new Set<string>();
    let observer: IntersectionObserver | undefined;
    let margin = "";
    const connect = () => {
      const rootMargin = `-${(parseFloat(getComputedStyle(endpoints.current!).getPropertyValue("--header-height")) || 56) + 16}px 0px -25% 0px`;
      if (margin === rootMargin) return;
      margin = rootMargin;
      observer?.disconnect();
      showing.clear();
      observer = new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            const id = (entry.target as HTMLElement).dataset.apiArea!;
            if (entry.isIntersecting) showing.add(id);
            else showing.delete(id);
          }
          const first = visibleAreas.find((area) => showing.has(area.id));
          if (first) setActiveArea(first.id);
        },
        { rootMargin },
      );
      endpoints
        .current!.querySelectorAll(".api-area-section")
        .forEach((node) => observer!.observe(node));
    };
    connect();
    // The shell already measures its header. Follow that published CSS value
    // after wrapping/text changes instead of adding another header measurement.
    const layout = endpoints.current!.closest(".workspace");
    const geometry = new MutationObserver(connect);
    if (layout)
      geometry.observe(layout, {
        attributes: true,
        attributeFilter: ["style"],
      });
    return () => {
      geometry.disconnect();
      observer!.disconnect();
    };
  }, [visibleAreas, workspace]);
  useEffect(() => {
    const url = URL.createObjectURL(
      new Blob([yaml], { type: "application/yaml" }),
    );
    setDownload({ yaml, url });
    return () => URL.revokeObjectURL(url);
  }, [yaml]);
  // Reference buttons navigate to the definition, even when it is already
  // selected. Changing the dropdown must not steal focus from the selector.
  const navigate = (id: string, from = origin) => {
    const hash = `#${encodeURIComponent(id)}`;
    // Each entry retains only its known contract origin, never local filters.
    if (location.hash !== hash)
      history.pushState(from ? { apiReferenceOrigin: from } : null, "", hash);
  };
  const showDefinition = (reference: string, from?: string) => {
    setWorkspace("definitions");
    setLinkNotice("");
    setHiddenOperation("");
    if (from) {
      // Save only a known contract operation anchor; no query or private input.
      setOrigin(from);
      if (origin !== from) navigate(from, from);
    }
    setSelected(reference);
    if (referenceValue(document, reference) !== undefined)
      navigate(definitionAnchor(reference), from ?? origin);
    else navigate("api-definitions", from ?? origin);
    setFocusTarget({ kind: "definition", id: definitionAnchor(reference) });
  };
  const originOperation = items.find(
    (item) => operationAnchor(item) === origin,
  );
  const hiddenTarget = items.find(
    (item) =>
      operationAnchor(item) === hiddenOperation && !filtered.includes(item),
  );
  const showOperation = (item: Operation, reveal = false) => {
    const id = operationAnchor(item);
    setWorkspace("endpoints");
    setLinkNotice("");
    setHiddenOperation("");
    if (reveal) {
      setQuery("");
      setTag("");
      setMethod("");
    }
    const home = areaAnchor(item.tags.at(-1));
    setCollapsed(
      (current) => new Set([...current].filter((id) => id !== home)),
    );
    setActiveArea(home);
    navigate(id);
    setOpen(id, true);
    setFocusTarget({ kind: "operation", id });
  };
  const showArea = (id: string) => {
    setWorkspace("endpoints");
    setLinkNotice("");
    setHiddenOperation("");
    setCollapsed(
      (current) => new Set([...current].filter((value) => value !== id)),
    );
    setActiveArea(id);
    navigate(id);
    setFocusTarget({ kind: "area", id });
  };
  const returnToIndex = (event: MouseEvent<HTMLAnchorElement>) => {
    event.preventDefault();
    navigate("api-area-index");
    setFocusTarget({ kind: "index", id: "api-area-index" });
  };
  return (
    <>
      {notices.map((notice) => (
        <p className="notice" key={notice}>
          {notice}
        </p>
      ))}
      {linkNotice && (
        <div className="notice">
          <p>{linkNotice}</p>
          <button
            type="button"
            onClick={() => {
              setWorkspace("endpoints");
              setLinkNotice("");
              navigate("api-area-index");
              setFocusTarget({ kind: "index", id: "api-area-index" });
            }}
          >
            Open endpoint index
          </button>
          <button
            type="button"
            onClick={() => {
              setWorkspace("definitions");
              setLinkNotice("");
              navigate("api-definitions");
              setFocusTarget({
                kind: "definition-index",
                id: "api-definitions",
              });
            }}
          >
            Open definition index
          </button>
        </div>
      )}
      <section className="workspace-panel api-overview">
        <div className="api-contract-title">
          <div>
            <h2>{text(object(document.info).title) || "Connected API"}</h2>
            <p className="muted">
              API {text(object(document.info).version)} · OpenAPI{" "}
              {text(document.openapi)} · {items.length} operations
            </p>
          </div>
          <a
            className="button"
            href={download?.yaml === yaml ? download.url : undefined}
            aria-disabled={download?.yaml !== yaml}
            download="control-v1.yaml"
          >
            {controlIcons["download"]} Download OpenAPI YAML
          </a>
          <div
            className="api-workspace-actions"
            role="group"
            aria-label="Reference workspace"
          >
            <button
              type="button"
              aria-pressed={workspace === "endpoints"}
              onClick={() => {
                setWorkspace("endpoints");
                navigate("api-area-index");
                setFocusTarget({ kind: "index", id: "api-area-index" });
              }}
            >
              Browse endpoints
            </button>
            <button
              type="button"
              aria-pressed={workspace === "definitions"}
              onClick={() => {
                setWorkspace("definitions");
                navigate("api-definitions");
                setFocusTarget({
                  kind: "definition-index",
                  id: "api-definitions",
                });
              }}
            >
              Browse definitions ({definitions.length})
            </button>
          </div>
        </div>
        <details>
          <summary>Overview</summary>
          <p className="api-description">
            {text(object(document.info).description) || "No overview supplied."}
          </p>
        </details>
        <p className="muted">
          This read-only reference requires UI sign-in and includes optional and
          operational endpoints. Check{" "}
          <a href={`${basePath}/service`}>Service information</a> for enabled
          features and <a href={`${basePath}/access`}>My access</a> for your
          permissions. API-listener service operations, public discovery and
          Catalog MCP belong to this contract; the worker-only operations
          listener is separate. Expanding an operation shows its contract; it
          does not call that endpoint.
        </p>
      </section>
      <section
        className="api-endpoints"
        hidden={workspace !== "endpoints"}
        ref={endpoints}
        aria-label="API endpoints"
      >
        <h2>Endpoints</h2>
        {hiddenTarget && (
          <div className="notice">
            <p>
              The linked operation is hidden by current filters. Reveal clears
              search, HTTP method and API area.
            </p>
            <button
              type="button"
              onClick={() => showOperation(hiddenTarget, true)}
            >
              Reveal linked operation
            </button>
          </div>
        )}
        <div className="workspace-toolbar api-reference-filters">
          <Field label="Search API operations">
            <input
              ref={search}
              type="search"
              value={query}
              onChange={(event) => {
                event.currentTarget.focus();
                setQuery(event.target.value);
                setCollapsed(new Set());
              }}
              placeholder="Path, method, name, description or area"
            />
          </Field>
          <Field label="HTTP method">
            <select
              value={method}
              onChange={(event) => {
                event.currentTarget.focus();
                setMethod(event.target.value);
                setCollapsed(new Set());
              }}
            >
              <option value="">Any method</option>
              {methods.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </Field>
          <Field label="API area">
            <select
              value={tag}
              onChange={(event) => {
                event.currentTarget.focus();
                setTag(event.target.value);
                setCollapsed(new Set());
              }}
            >
              <option value="">All areas</option>
              {areas
                .filter((area) => area.tag !== undefined)
                .map((area) => (
                  <option key={area.id} value={area.tag}>
                    {area.name}
                  </option>
                ))}
            </select>
          </Field>
          <div className="workspace-actions">
            <button
              type="button"
              onClick={() => {
                setQuery("");
                setTag("");
                setMethod("");
                setLinkNotice("");
                setCollapsed(new Set());
                search.current!.focus();
              }}
            >
              Clear filters
            </button>
            <button
              type="button"
              onClick={() => {
                setWorkspace("definitions");
                navigate("api-definitions");
                setFocusTarget({
                  kind: "definition-index",
                  id: "api-definitions",
                });
              }}
            >
              Browse schemas and shared definitions
            </button>
          </div>
        </div>
        <p className="api-filter-summary">
          Applied filters: search{" "}
          {query.trim() ? JSON.stringify(query) : "any text"}; method{" "}
          {method || "any"}; API area {tag ? JSON.stringify(tag) : "all"}.
        </p>
        <p role="status">
          {filtered.length} of {items.length} operations
        </p>
        <p className="muted">
          Each endpoint appears in one section, under its last operation tag.
          The API area filter matches all of its tags, so matches can appear in
          several sections. Section counts count each endpoint once.
        </p>
        <div className="api-reference-layout">
          <nav
            className="workspace-panel api-area-index"
            id="api-area-index"
            tabIndex={-1}
            aria-label="API areas"
          >
            <h2>API areas</h2>
            <p className="muted">Jump to a section without changing filters.</p>
            <ul className="api-area-links">
              {visibleAreas.map((area) => (
                <li key={area.id}>
                  <a
                    href={`#${encodeURIComponent(area.id)}`}
                    aria-label={`${area.name}: ${area.operations.length} ${area.operations.length === 1 ? "operation" : "operations"}`}
                    aria-current={
                      currentArea === area.id ? "location" : undefined
                    }
                    onClick={(event) => {
                      event.preventDefault();
                      showArea(area.id);
                    }}
                  >
                    <span>{area.name}</span>
                    <span className="badge">{area.operations.length}</span>
                  </a>
                </li>
              ))}
            </ul>
            <div className="api-area-select">
              <Field label="Jump to API section">
                <select
                  value={currentArea}
                  onChange={(event) => showArea(event.target.value)}
                  disabled={!visibleAreas.length}
                >
                  {!visibleAreas.length && (
                    <option value="">No matching sections</option>
                  )}
                  {visibleAreas.map((area) => (
                    <option key={area.id} value={area.id}>
                      {area.name} ({area.operations.length})
                    </option>
                  ))}
                </select>
              </Field>
            </div>
          </nav>
          <div className="api-area-sections">
            {!filtered.length && (
              <p>
                {items.length
                  ? "No operations match. Change or clear search, HTTP method or API area."
                  : "This contract contains no operations."}
              </p>
            )}
            {visibleAreas.map((area) => (
              <section
                className="api-area-section"
                data-api-area={area.id}
                aria-labelledby={area.id}
                key={area.id}
              >
                <details
                  className="api-area-disclosure"
                  open={!collapsed.has(area.id)}
                  onToggle={(event) => {
                    const open = event.currentTarget.open;
                    setCollapsed((current) => {
                      if (current.has(area.id) === !open) return current;
                      const next = new Set(current);
                      if (open) next.delete(area.id);
                      else next.add(area.id);
                      return next;
                    });
                  }}
                >
                  <summary
                    className="api-area-summary"
                    onClick={() => setActiveArea(area.id)}
                  >
                    <h2 id={area.id} tabIndex={-1}>
                      {area.name}
                    </h2>
                    <span className="badge">
                      {area.operations.length} operation
                      {area.operations.length === 1 ? "" : "s"}
                    </span>
                  </summary>
                  <div className="api-area-toolbar">
                    <a href="#api-area-index" onClick={returnToIndex}>
                      {controlIcons["list"]} Back to API areas
                    </a>
                    <button
                      type="button"
                      onClick={() => {
                        setCollapsed(
                          (current) => new Set([...current, area.id]),
                        );
                        setFocusTarget({ kind: "area-toggle", id: area.id });
                      }}
                    >
                      Collapse {area.name}
                    </button>
                  </div>
                  {area.description && (
                    <p className="api-description">{area.description}</p>
                  )}
                  {area.operations.map((operation) => (
                    <OperationDetail
                      key={`${operation.method}:${operation.path}`}
                      operation={operation}
                      document={document}
                      open={expanded.has(operationAnchor(operation))}
                      onOpen={(open) =>
                        setOpen(operationAnchor(operation), open)
                      }
                      onReference={(reference) =>
                        showDefinition(reference, operationAnchor(operation))
                      }
                    />
                  ))}
                </details>
              </section>
            ))}
          </div>
        </div>
      </section>
      <section
        className="workspace-panel api-definitions"
        id="api-definitions"
        hidden={workspace !== "definitions"}
        ref={definitionWorkspace}
        tabIndex={-1}
        aria-label="Schema and shared definitions"
      >
        <h2>Definitions</h2>
        <p>
          Search the complete connected component index. Index criteria and
          selected details are independent; no external schema is fetched.
        </p>
        <div className="api-definition-layout">
          <section
            className="api-definition-index"
            aria-label="Definition index"
            tabIndex={0}
          >
            <p className="muted">
              Scroll the index to browse this page. Search covers all
              definitions.
            </p>
            <div className="workspace-toolbar">
              <Field label="Search definitions">
                <input
                  type="search"
                  value={definitionQuery}
                  onChange={(event) => {
                    setDefinitionQuery(event.target.value);
                    setDefinitionPage(0);
                  }}
                />
              </Field>
              <Field label="Component kind">
                <select
                  value={definitionKind}
                  onChange={(event) => {
                    setDefinitionKind(event.target.value);
                    setDefinitionPage(0);
                  }}
                >
                  <option value="">All component kinds</option>
                  {definitionKinds.map((kind) => (
                    <option key={kind}>{kind}</option>
                  ))}
                </select>
              </Field>
              <div className="workspace-actions">
                <button
                  type="button"
                  onClick={() => {
                    setDefinitionQuery("");
                    setDefinitionKind("");
                    setDefinitionPage(0);
                  }}
                >
                  Clear definition filters
                </button>
              </div>
            </div>
            <p role="status">
              {matching.length} of {definitions.length} definitions; page{" "}
              {pageIndex + 1} of {pageCount}.
            </p>
            {!matching.length && (
              <p>
                {definitions.length
                  ? "No definitions match. Change or clear definition search and component kind."
                  : "This contract contains no shared definitions."}
              </p>
            )}
            <div className="api-definition-groups">
              {[...new Set(definitionRows.map((row) => row.kind))].map(
                (kind) => (
                  <section key={kind} aria-label={`Component kind ${kind}`}>
                    <h3>{kind}</h3>
                    <ul>
                      {definitionRows
                        .filter((row) => row.kind === kind)
                        .map((row) => (
                          <li key={row.reference}>
                            <button
                              type="button"
                              onClick={() => showDefinition(row.reference)}
                            >
                              Open {row.kind}/{row.name}
                            </button>
                            {row.description && (
                              <p className="api-description">
                                {row.description}
                              </p>
                            )}
                          </li>
                        ))}
                    </ul>
                  </section>
                ),
              )}
            </div>
            <div
              className="workspace-toolbar"
              role="group"
              aria-label="Definition pages"
            >
              <button
                type="button"
                disabled={pageIndex === 0}
                onClick={() => setDefinitionPage(0)}
              >
                First definitions
              </button>
              <button
                type="button"
                disabled={pageIndex === 0}
                onClick={() => setDefinitionPage(pageIndex - 1)}
              >
                Previous definitions
              </button>
              <button
                type="button"
                disabled={pageIndex + 1 >= pageCount}
                onClick={() => setDefinitionPage(pageIndex + 1)}
              >
                Next definitions
              </button>
            </div>
            <Field label="Schema or shared definition">
              <select
                value={selected}
                onChange={(event) => {
                  const reference = event.target.value;
                  setSelected(reference);
                  if (
                    reference &&
                    referenceValue(document, reference) !== undefined
                  )
                    navigate(definitionAnchor(reference));
                  else if (!reference) {
                    setOrigin("");
                    navigate("api-definitions", "");
                  }
                }}
              >
                <option value="">Select a definition from this page</option>
                {definitionChoices.map((ref) => (
                  <option key={ref} value={ref}>
                    {ref.startsWith("#/components/")
                      ? ref.slice("#/components/".length)
                      : ref}
                  </option>
                ))}
              </select>
            </Field>
          </section>
          {selected && (
            <section
              className="api-definition-detail"
              id={
                selectedKnown
                  ? definitionAnchor(selected)
                  : "api-definition-unavailable"
              }
              ref={definition}
              tabIndex={-1}
              aria-label="Selected definition"
            >
              <h3>{selected}</h3>
              {selectedKnown && (
                <a
                  href={`#${encodeURIComponent(definitionAnchor(selected))}`}
                  onClick={(event) => {
                    event.preventDefault();
                    navigate(definitionAnchor(selected));
                    setFocusTarget({
                      kind: "definition",
                      id: definitionAnchor(selected),
                    });
                  }}
                >
                  {controlIcons["arrow"]} Link to definition
                </a>
              )}
              {!definitionRows.some((row) => row.reference === selected) && (
                <p className="notice">
                  The selected definition is outside the current index page or
                  criteria. Index filters remain unchanged.
                </p>
              )}
              {originOperation && (
                <>
                  <button
                    type="button"
                    disabled={
                      !filtered.some((item) => operationAnchor(item) === origin)
                    }
                    onClick={() => showOperation(originOperation)}
                  >
                    Return to originating operation
                  </button>
                  {!filtered.some(
                    (item) => operationAnchor(item) === origin,
                  ) && (
                    <>
                      <p>
                        The originating operation is hidden by current filters.
                        Reveal clears search, HTTP method and API area before
                        returning.
                      </p>
                      <button
                        type="button"
                        onClick={() => showOperation(originOperation, true)}
                      >
                        Reveal originating operation
                      </button>
                    </>
                  )}
                </>
              )}
              <ContractValue
                document={document}
                schema={selected.startsWith("#/components/schemas/")}
                value={referenceValue(document, selected)}
                onReference={(reference) => showDefinition(reference)}
              />
            </section>
          )}
          {!selected && (
            <p>
              Choose a definition from the index or follow an operation
              reference.
            </p>
          )}
        </div>
      </section>
    </>
  );
}

/** Display one operation's parameters, security and request/response contract data. */
function OperationDetail({
  operation,
  document,
  onReference,
  open,
  onOpen,
}: {
  operation: Operation;
  document: Document;
  onReference: (ref: string) => void;
  open: boolean;
  onOpen: (open: boolean) => void;
}) {
  return (
    <details
      className="workspace-panel api-operation"
      id={operationAnchor(operation)}
      open={open}
      data-operation-id={operation.id}
      onToggle={(event) => onOpen(event.currentTarget.open)}
    >
      <summary>
        <span
          className={`api-method api-method-${operation.method.toLowerCase()}`}
        >
          {operation.method}
        </span>
        <code>{operation.path}</code>
        <span>{operation.summary}</span>
        {operation.deprecated && (
          <span className="badge notice">Deprecated</span>
        )}
      </summary>
      {open && (
        <div>
          <a href={`#${encodeURIComponent(operationAnchor(operation))}`}>
            {controlIcons["arrow"]} Link to operation
          </a>
          {operation.description && (
            <p className="api-description">{operation.description}</p>
          )}
          <OperationContract
            operation={operation}
            document={document}
            onReference={onReference}
          />
        </div>
      )}
    </details>
  );
}

function documentElement(id: string) {
  return window.document.getElementById(id);
}

/** Render contract values as data with navigable local references, without executing requests. */
function ContractValue({
  document,
  schema,
  value,
  onReference,
}: {
  document: Document;
  schema: boolean;
  value: unknown;
  onReference: (ref: string) => void;
}) {
  if (value === undefined)
    return <p>Definition unavailable in this contract.</p>;
  return (
    <>
      {schema && (
        <SchemaSummary
          document={document}
          value={value}
          onReference={onReference}
        />
      )}
      <p className="eyebrow">
        Definition shown as JSON. Download the OpenAPI YAML for the original
        file.
      </p>
      <JSONBlock value={value} />
      {references(value).length > 32 && (
        <p>
          Additional local references remain in complete decoded data and the
          definition index.
        </p>
      )}
      {references(value)
        .slice(0, 32)
        .map((ref) => (
          <button
            className="api-ref-link"
            key={ref}
            onClick={() => onReference(ref)}
          >
            View {ref.slice(2)}
          </button>
        ))}
    </>
  );
}
