// The filter panel stages edits locally and applies them as one validated criteria update.
import { useCallback, useId, useLayoutEffect, useRef, useState } from "react";
import { Icon, controlIcons } from "../shell/Icon";
import {
  atField,
  byteText,
  characterText,
  metadataTerms,
  InputError,
} from "./input-validation";
import { applyFilters, categoryFacetQuery, mergeTags } from "./search-filters";
import type { FilterValues, PublisherSuggestion } from "./search-filters";
import { NamespaceFilter } from "./NamespaceFilter";
import type { Client } from "../api/client";
import { KindSelection } from "./KindSelection";
import "./search-filters.css";
import { ValueList } from "./ValueList";
import { CategoryChoices } from "./CategoryChoices";

export const visibilityOptions = [
  { value: "", label: "Any visibility" },
  { value: "public", label: "Public" },
  { value: "tenant", label: "Tenant" },
  { value: "private", label: "Private" },
] as const;

/** Keep filter edits local until Apply and derive suggestions from the supplied results page. */
export default function SearchFilters({
  value,
  tags,
  publishers,
  onApply,
  client,
  onFailure,
  externalAllowed = true,
  namespaceAllowed = true,
  searchText = "",
  identityKey = "",
  suggestionsAllowed = true,
  onRefresh,
  refreshDisabled = false,
}: {
  client?: Client;
  onFailure?: (error: unknown) => void;
  value: FilterValues;
  tags: string[];
  publishers: PublisherSuggestion[];
  onApply: (filters: FilterValues) => void;
  externalAllowed?: boolean;
  namespaceAllowed?: boolean;
  searchText?: string;
  identityKey?: string;
  suggestionsAllowed?: boolean;
  onRefresh?: () => void;
  refreshDisabled?: boolean;
}) {
  const descriptionID = useId();
  const [open, setOpen] = useState(false);
  const [suggestionsRequested, setSuggestionsRequested] = useState(false);
  if (!suggestionsAllowed && suggestionsRequested)
    setSuggestionsRequested(false);
  const [draft, setDraft] = useState<FilterValues>({});
  const [tagInput, setTagInput] = useState("");
  const [suggestionText, setSuggestionText] = useState("");
  const [error, setError] = useState("");
  const [narrow, setNarrow] = useState(() => window.innerWidth <= 900);
  const panel = useRef<HTMLDialogElement>(null);
  const lastFocused = useRef<HTMLElement | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const opener = useRef<HTMLButtonElement | null>(null);
  const target = useRef<string | undefined>(undefined);
  const [advanced, setAdvanced] = useState(true);
  const closeButton = useRef<HTMLButtonElement>(null);
  const count =
    (value.tags?.length ?? 0) +
    (value.kinds?.length ?? 0) +
    Number(!!value.publisher) +
    Number(!!value.license) +
    (value.categories?.length ?? 0) +
    Number(!!value.visibility) +
    Number(!!value.namespaceId) +
    (value.origins?.length ?? 0);
  // Tags are exact values; preserve case, Unicode spelling and code-unit order.
  const tagChoices = [...new Set([...(draft.tags ?? []), ...tags])].sort(
    (a, b) => Number(a > b) - Number(a < b),
  );
  const matchingTags = tagChoices.filter((tag) =>
    tag.toLowerCase().includes(suggestionText.toLowerCase()),
  );
  const visibleTags = tagChoices.filter(
    (tag) => matchingTags.includes(tag) || draft.tags?.includes(tag),
  );
  // Explicit dismissal restores the opener; outside-pointer dismissal leaves focus alone.
  const close = useCallback(() => {
    panel.current?.close();
    setOpen(false);
    (opener.current ?? trigger.current)?.focus();
  }, []);
  useLayoutEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target))
        setOpen(false);
    };

    // A disabled loading control can leave focus on body. Escape must still
    // dismiss the open panel, including its nonmodal desktop presentation.
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) {
        event.preventDefault();
        event.stopPropagation();
        close();
      }
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", escape);
    };
  }, [open, close]);
  useLayoutEffect(() => {
    const resize = () => setNarrow(window.innerWidth <= 900);
    window.addEventListener("resize", resize);
    return () => window.removeEventListener("resize", resize);
  }, []);
  useLayoutEffect(() => {
    if (!open) return;
    const dialog = panel.current!;
    const focused = lastFocused.current;
    if (narrow) dialog.showModal();
    else dialog.show();
    // Preserve the active control and draft when the same panel changes mode.
    if (focused instanceof HTMLElement && dialog.contains(focused))
      focused.focus();
    else if (target.current)
      dialog.querySelector<HTMLElement>(target.current)?.focus();
    else closeButton.current?.focus();
    return () => {
      const active = document.activeElement;
      lastFocused.current =
        active instanceof HTMLElement && dialog.contains(active)
          ? active
          : null;
      dialog.close();
    };
  }, [open, narrow]);
  function show(button: HTMLButtonElement, selector?: string) {
    setSuggestionsRequested(suggestionsAllowed);
    opener.current = button;
    target.current = selector;
    if (open) {
      if (selector)
        panel.current?.querySelector<HTMLElement>(selector)?.focus();
      else close();
      return;
    }
    setDraft({
      ...value,
      tags: [...(value.tags ?? [])],
      ...(value.categories ? { categories: [...value.categories] } : {}),
    });
    setTagInput("");
    setSuggestionText("");
    setError("");
    setAdvanced(!selector);
    setOpen(true);
  }
  // Validate pending tag text before publishing the complete filter draft to the caller.
  function commit() {
    try {
      if (draft.origins?.length === 0)
        throw new Error("Select at least one origin.");
      if (
        !externalAllowed &&
        draft.origins?.some((origin) => origin !== "local")
      )
        throw new Error(
          "External origins require federation.discover permission and catalog.read when scopes are enforced. Remove those selections or clear the filters.",
        );
      const license = atField("filter-license", () =>
        characterText((draft.license ?? "").trim(), "License", 256),
      );
      const categories = atField("filter-categories", () =>
        metadataTerms(
          [...new Set((draft.categories ?? []).map((value) => value.trim()))],
          "categories",
          16,
          1,
        ),
      );
      const publisher = atField("filter-publisher", () =>
        byteText((draft.publisher ?? "").trim(), "Publisher", 256),
      );
      const tags = atField("filter-tags", () =>
        mergeTags(draft.tags ?? [], tagInput),
      );
      onApply({
        ...draft,
        ...(draft.publisher !== undefined ? { publisher } : {}),
        tags,
        ...(draft.license !== undefined ? { license } : {}),
        ...(draft.categories !== undefined ? { categories } : {}),
      });
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Check the filter values.");
      if (e instanceof InputError) document.getElementById(e.fieldId)!.focus();
    }
  }
  return (
    <div
      className="filter-anchor"
      ref={root}
      onBlur={(event) => {
        if (
          event.relatedTarget instanceof Node &&
          !event.currentTarget.contains(event.relatedTarget)
        )
          setOpen(false);
      }}
    >
      <div className="filter-controls">
        <button
          type="button"
          className="filter-trigger"
          aria-expanded={open}
          aria-controls={open ? "search-filters" : undefined}
          aria-haspopup={narrow ? "dialog" : undefined}
          onClick={(event) => show(event.currentTarget, "#filter-categories")}
        >
          Categories
          {value.categories?.length ? ` (${value.categories.length})` : ""}
        </button>
        <button
          type="button"
          className="filter-trigger"
          aria-expanded={open}
          aria-controls={open ? "search-filters" : undefined}
          aria-haspopup={narrow ? "dialog" : undefined}
          onClick={(event) => show(event.currentTarget, "#filter-origin-local")}
        >
          Origin
        </button>
        {client && onFailure && (
          <button
            type="button"
            className="filter-trigger"
            aria-expanded={open}
            aria-controls={open ? "search-filters" : undefined}
            aria-haspopup={narrow ? "dialog" : undefined}
            onClick={(event) =>
              show(
                event.currentTarget,
                namespaceAllowed ? "select" : "#filter-namespace",
              )
            }
          >
            Namespace
          </button>
        )}
        <button
          type="button"
          className={`filter-trigger ${count ? "is-active" : ""}`}
          ref={trigger}
          aria-label="Filters"
          aria-describedby={descriptionID}
          aria-expanded={open}
          aria-controls={open ? "search-filters" : undefined}
          aria-haspopup={narrow ? "dialog" : undefined}
          onClick={(event) => show(event.currentTarget)}
        >
          <Icon name="filters" size={18} /> Filters{" "}
          {count > 0 && <span className="filter-count">{count}</span>}
        </button>
        {onRefresh && (
          <button
            type="button"
            className="filter-trigger"
            disabled={refreshDisabled}
            onClick={onRefresh}
            aria-describedby="refresh-search-disclosure"
          >
            {controlIcons["refresh"]} Refresh results
          </button>
        )}
      </div>
      {onRefresh && (
        <p className="sr-only" id="refresh-search-disclosure">
          Refresh reruns your search with the same filters, starting from the
          first page.
        </p>
      )}
      <span id={descriptionID} className="sr-only">
        {count} applied filters
      </span>
      {open && (
        <dialog
          ref={panel}
          id="search-filters"
          className="filter-panel"
          role={narrow ? "dialog" : "region"}
          aria-modal={narrow ? true : undefined}
          aria-labelledby="filter-title"
          onCancel={(event) => {
            event.preventDefault();
            close();
          }}
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              commit();
            }}
          >
            <header className="filter-panel-header">
              <h2 id="filter-title">Filters</h2>
              <button
                type="button"
                className="icon-button"
                ref={closeButton}
                aria-label="Close filters"
                onClick={() => close()}
              >
                <Icon name="close" />
              </button>
            </header>
            <div className="filter-panel-body">
              <div className="filter-summary">
                <p className="filter-help">
                  Edit filters for the applied search text. Changes take effect
                  when you choose Apply filters. Tag and Publisher suggestions
                  come from the displayed page.
                </p>
                {onRefresh && (
                  <button
                    type="button"
                    disabled={refreshDisabled}
                    onClick={onRefresh}
                  >
                    {controlIcons["refresh"]} Refresh applied results
                  </button>
                )}
              </div>
              <section
                className="filter-group category-filter"
                aria-labelledby="category-filter-heading"
              >
                <h3 id="category-filter-heading">Categories</h3>
                <p className="filter-help">
                  Match all selected categories. Labels are not a trust
                  assessment.
                </p>
                {client &&
                onFailure &&
                suggestionsAllowed &&
                suggestionsRequested &&
                (!value.origins || value.origins.includes("local")) ? (
                  <CategoryChoices
                    client={client}
                    identityKey={identityKey}
                    onFailure={onFailure}
                    query={categoryFacetQuery(
                      applyFilters({ text: searchText }, value),
                    )}
                    selected={draft.categories ?? []}
                    onChange={(categories) =>
                      setDraft({ ...draft, categories })
                    }
                  />
                ) : (
                  <p className="filter-help">
                    {suggestionsAllowed
                      ? "Enter categories for the selected origins. Local suggestions do not include external results."
                      : "Allow embedding-provider processing, then reopen Categories or Filters to load local suggestions. You can enter exact categories without consent."}
                  </p>
                )}
                <ValueList
                  id="filter-categories"
                  label="Categories"
                  itemLabel="Category"
                  value={draft.categories ?? []}
                  onChange={(categories) => setDraft({ ...draft, categories })}
                  maximum={16}
                  length={80}
                  hint="All selected categories must match. Missing declarations do not match."
                />
              </section>
              <KindSelection
                value={draft.kinds ?? []}
                onChange={(kinds) => setDraft({ ...draft, kinds })}
              />
              <section
                className="filter-group"
                aria-labelledby="tag-filter-heading"
              >
                <div className="filter-group-title">
                  <h3 id="tag-filter-heading">Tags</h3>
                  {!!draft.tags?.length && (
                    <button
                      type="button"
                      className="group-clear"
                      aria-label="Clear tag filters"
                      onClick={() => setDraft({ ...draft, tags: [] })}
                    >
                      {draft.tags.length}
                      <Icon name="close" size={12} />
                    </button>
                  )}
                </div>
                <p className="filter-help">
                  Suggestions from current results. You can also add any exact
                  tag. All selected tags must match.
                </p>
                <label className="filter-field">
                  Search tag suggestions
                  <input
                    type="search"
                    value={suggestionText}
                    maxLength={160}
                    onChange={(event) => setSuggestionText(event.target.value)}
                  />
                </label>
                <div className="filter-options">
                  {visibleTags.map((tag) => (
                    <label className="filter-option" key={tag}>
                      <input
                        type="checkbox"
                        checked={draft.tags?.includes(tag) ?? false}
                        onChange={(event) =>
                          setDraft({
                            ...draft,
                            tags: event.target.checked
                              ? [...(draft.tags ?? []), tag]
                              : draft.tags?.filter((value) => value !== tag),
                          })
                        }
                      />
                      <span>{tag}</span>
                    </label>
                  ))}
                  {!tagChoices.length && (
                    <p className="filter-help">No suggestions on this page.</p>
                  )}
                  {!!tagChoices.length && !matchingTags.length && (
                    <p className="filter-help">
                      No matching tag suggestions. Selected tags remain
                      available.
                    </p>
                  )}
                </div>
                <label className="filter-field">
                  Add tag
                  <input
                    id="filter-tags"
                    value={tagInput}
                    onChange={(event) => {
                      setTagInput(event.target.value);
                      setError("");
                    }}
                    maxLength={160}
                    placeholder="e.g. customer support"
                  />
                </label>
                <button
                  type="button"
                  onClick={() => {
                    try {
                      setDraft({
                        ...draft,
                        tags: mergeTags(draft.tags ?? [], tagInput),
                      });
                      setTagInput("");
                      setError("");
                    } catch (error) {
                      setError(
                        error instanceof Error
                          ? error.message
                          : "Check the tag.",
                      );
                    }
                  }}
                >
                  Add exact tag
                </button>
                <p className="filter-help">
                  Enter one exact tag. Commas are part of the value. All
                  selected tags must match.
                </p>
              </section>
              <div className="filter-column">
                <fieldset className="filter-group">
                  <legend>Origin</legend>
                  {(["local", "indexed", "live"] as const).map((origin) => (
                    <label className="filter-option" key={origin}>
                      <input
                        id={`filter-origin-${origin}`}
                        type="checkbox"
                        checked={(
                          draft.origins ??
                          (externalAllowed
                            ? ["local", "indexed", "live"]
                            : ["local"])
                        ).includes(origin)}
                        disabled={
                          origin !== "local" &&
                          (!!draft.namespaceId ||
                            (!externalAllowed &&
                              !draft.origins?.includes(origin)))
                        }
                        onChange={(e) => {
                          const current =
                            draft.origins ??
                            (externalAllowed
                              ? ["local", "indexed", "live"]
                              : ["local"]);
                          setDraft({
                            ...draft,
                            origins: e.target.checked
                              ? [...current, origin]
                              : current.filter((value) => value !== origin),
                          });
                        }}
                      />
                      <span>
                        {origin === "local"
                          ? "Local Catalog"
                          : origin === "indexed"
                            ? "Indexed external"
                            : "Live external"}
                      </span>
                    </label>
                  ))}
                  <p className="filter-help">
                    Origin describes where metadata comes from, not where a
                    resource runs.{" "}
                    {!externalAllowed && (
                      <>
                        External origins require federation.discover permission
                        and catalog.read when scopes are enforced. Existing
                        selections can be removed.{" "}
                      </>
                    )}
                    Live results require search text. Pages can continue one
                    unchanged bounded live window. Live search may disclose
                    text, tags and filters to approved external Catalogs.
                    Uncheck Live external to search only stored local/indexed
                    data. Optional semantic search has a separate
                    embedding-provider boundary.
                  </p>
                </fieldset>
                {client && onFailure && namespaceAllowed && (
                  <NamespaceFilter
                    client={client}
                    value={draft.namespaceId}
                    onFailure={onFailure}
                    onChange={(namespaceId) =>
                      setDraft({
                        ...draft,
                        namespaceId,
                        origins: namespaceId ? ["local"] : draft.origins,
                      })
                    }
                  />
                )}
                <label className="filter-field">
                  Exact namespace ID
                  <input
                    id="filter-namespace"
                    value={draft.namespaceId ?? ""}
                    maxLength={36}
                    pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
                    placeholder="Known namespace UUID"
                    onChange={(event) => {
                      const namespaceId = event.target.value || undefined;
                      setDraft({
                        ...draft,
                        namespaceId,
                        origins: namespaceId ? ["local"] : draft.origins,
                      });
                    }}
                  />
                </label>
                <p className="filter-help">
                  A manually entered ID is not a verified namespace or an access
                  grant. Namespace directory access is needed only for loading
                  choices.
                </p>
                <p className="filter-help">
                  A Namespace limits results to local resources. Removing it
                  leaves Local Catalog selected until you change Origin.
                </p>
                <details
                  className="filter-more"
                  open={advanced}
                  onToggle={(event) => setAdvanced(event.currentTarget.open)}
                >
                  <summary>More filters</summary>
                  <section
                    className="filter-group"
                    aria-labelledby="listing-filter-heading"
                  >
                    <h3 id="listing-filter-heading">Declared license</h3>
                    <label className="filter-field">
                      <span>License declaration</span>
                      <input
                        id="filter-license"
                        value={draft.license ?? ""}
                        placeholder="For example, Apache-2.0"
                        onChange={(event) =>
                          setDraft({
                            ...draft,
                            license: event.target.value,
                          })
                        }
                      />
                    </label>
                    <p className="filter-help">
                      Exact declaration, including case, up to 256 characters.
                      Missing declarations do not match.
                    </p>
                  </section>
                  <fieldset className="filter-group">
                    <legend>Visibility</legend>
                    {visibilityOptions.map((option) => (
                      <label className="filter-option" key={option.value}>
                        <input
                          type="radio"
                          name="filter-visibility"
                          value={option.value}
                          checked={(draft.visibility ?? "") === option.value}
                          onChange={() =>
                            setDraft({
                              ...draft,
                              visibility: option.value || undefined,
                            })
                          }
                        />
                        <span>{option.label}</span>
                      </label>
                    ))}
                  </fieldset>
                  <section
                    className="filter-group"
                    aria-labelledby="publisher-filter-heading"
                  >
                    <div className="filter-group-title">
                      <h3 id="publisher-filter-heading">Publisher</h3>
                      {draft.publisher && (
                        <button
                          type="button"
                          className="group-clear"
                          aria-label="Clear publisher filter"
                          onClick={() =>
                            setDraft({ ...draft, publisher: undefined })
                          }
                        >
                          <Icon name="close" size={12} />
                        </button>
                      )}
                    </div>
                    <label className="filter-field">
                      <span className="sr-only">Publisher identifier</span>
                      <input
                        id="filter-publisher"
                        value={draft.publisher ?? ""}
                        onChange={(event) =>
                          setDraft({ ...draft, publisher: event.target.value })
                        }
                        maxLength={256}
                        placeholder="Publisher UUID or AIR name"
                      />
                    </label>
                    <p className="filter-help">
                      Exact UUID for local items, AIR publisher for external
                      items.
                    </p>
                    {!!publishers.length && (
                      <>
                        <p className="filter-suggestion-label">
                          From current results
                        </p>
                        {publishers.map((publisher) => (
                          <button
                            className="publisher-choice"
                            type="button"
                            key={publisher.value}
                            aria-pressed={draft.publisher === publisher.value}
                            onClick={() =>
                              setDraft({
                                ...draft,
                                publisher:
                                  draft.publisher === publisher.value
                                    ? undefined
                                    : publisher.value,
                              })
                            }
                          >
                            <Icon name="user" size={14} />
                            <span>
                              {publisher.label}{" "}
                              <small>
                                {publisher.origin === "local"
                                  ? "Local publisher"
                                  : "External AIR publisher"}
                                {publisher.label !== publisher.value &&
                                  ` · ${publisher.value}`}
                              </small>
                            </span>
                          </button>
                        ))}
                      </>
                    )}
                  </section>
                </details>
              </div>
            </div>
            {error && (
              <p className="filter-error" role="alert">
                {error}
              </p>
            )}
            <footer className="filter-panel-footer">
              <button
                type="button"
                onClick={() => {
                  setDraft({});
                  setTagInput("");
                  setSuggestionText("");
                  setError("");
                }}
              >
                Clear filters
              </button>
              <button type="submit" className="primary">
                Apply filters
                {controlIcons["arrow"]}
              </button>
            </footer>
          </form>
        </dialog>
      )}
    </div>
  );
}
