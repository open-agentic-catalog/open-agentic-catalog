// Session-independent public presentation uses only the fixed anonymous ARD read surface.
import { useCallback, useEffect, useRef, useState } from "react";
import { documentationLink, type Runtime } from "../api/session";
import {
  manifestCount,
  publicKind,
  publicPage,
  publicRead,
  PublicDiscoveryError,
  type PublicPage,
  type PublicSelection,
} from "./public-discovery";
import "./public-discovery.css";

/** Browse one page at a time; changing selection and refresh never merge generations. */
export default function PublicDiscovery({ runtime }: { runtime: Runtime }) {
  const [text, setText] = useState("");
  const [size, setSize] = useState(20);
  const [allowProvider, setAllowProvider] = useState(false);
  const [applied, setApplied] = useState<PublicSelection>({
    text: "",
    pageSize: 20,
  });
  const [page, setPage] = useState<PublicPage>();
  const [count, setCount] = useState<number>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<
    { controller: AbortController; id: number } | undefined
  >(undefined);
  const sequence = useRef(0);
  const documentation = documentationLink(runtime.documentationUrl);
  const load = useCallback(
    async (selection: PublicSelection, token = "", refresh = true) => {
      request.current?.controller.abort();
      const controller = new AbortController(),
        id = ++sequence.current;
      request.current = { controller, id };
      setPage(undefined);
      setBusy(true);
      setError("");
      setApplied(selection);
      if (refresh) setCount(undefined);
      try {
        if (refresh) {
          const value = await publicRead(
            runtime,
            "manifest",
            selection,
            "",
            controller.signal,
          );
          if (id !== sequence.current) return;
          setCount(manifestCount(value));
        }
        const value = await publicRead(
          runtime,
          selection.text.trim() ? "search" : "agents",
          selection,
          token,
          controller.signal,
        );
        if (id === sequence.current)
          setPage(
            publicPage(value, !!selection.text.trim(), selection.pageSize),
          );
      } catch (failure) {
        if (id === sequence.current) {
          setCount(undefined);
          setError(
            failure instanceof PublicDiscoveryError &&
              failure.code === "continuation_or_selection_refused"
              ? "This selection or continuation is no longer available. Refresh from the first page."
              : "Public discovery is unavailable. Refresh to try again; no private Catalog read was attempted.",
          );
        }
      } finally {
        if (id === sequence.current) setBusy(false);
      }
    },
    [runtime],
  );
  const cancelRequest = useCallback(() => {
    ++sequence.current;
    request.current?.controller.abort();
  }, []);
  useEffect(() => {
    void load({ text: "", pageSize: 20 });
    return cancelRequest;
  }, [load, cancelRequest]);
  const canSearch = !text.trim() || allowProvider;
  const canRepeat = !applied.text.trim() || allowProvider;
  return (
    <main id="main" className="public-discovery">
      <header>
        <h1>Public discovery</h1>
        <nav className="public-links" aria-label="Public navigation">
          <a href={`${runtime.basePath}/catalog`}>Sign in</a>
          <a href={`${runtime.basePath}/about`}>About</a>
          {documentation && (
            <a href={documentation} target="_blank" rel="noopener noreferrer">
              Documentation
            </a>
          )}
        </nav>
      </header>
      <p>
        Browse the published directory without a Catalog account. Published
        metadata does not establish trust, compatibility or permission to
        install or call a resource.
      </p>
      <form
        className="public-search"
        onSubmit={(event) => {
          event.preventDefault();
          if (canSearch) void load({ text, pageSize: size });
        }}
      >
        <label>
          Search public discovery
          <input
            type="search"
            aria-describedby="public-search-limit public-search-disclosure"
            value={text}
            maxLength={1000}
            onChange={(event) => setText(event.target.value)}
          />
        </label>
        <small id="public-search-limit">Use at most 500 characters.</small>
        <label>
          Entries per page
          <select
            value={size}
            onChange={(event) => setSize(Number(event.target.value))}
          >
            {[5, 20, 50, 100].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <p id="public-search-disclosure">
          Search text may be sent to the Catalog's configured embedding
          provider. This view does not send queries to peer Catalogs. Browsing
          the directory needs no search text.
        </p>
        <label className="public-consent">
          <input
            type="checkbox"
            checked={allowProvider}
            onChange={(event) => {
              setAllowProvider(event.target.checked);
              if (!event.target.checked && busy && applied.text.trim()) {
                cancelRequest();
                setBusy(false);
                setPage(undefined);
              }
            }}
          />
          <span>
            Allow search text to reach the configured embedding provider
          </span>
        </label>
        <div className="actions">
          <button
            type="submit"
            disabled={busy || !canSearch || [...text].length > 500}
          >
            Search
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              setText("");
              void load({ text: "", pageSize: size });
            }}
          >
            Browse directory
          </button>
          <button
            type="button"
            disabled={busy || !canRepeat}
            onClick={() => void load(applied)}
          >
            Refresh publication
          </button>
        </div>
      </form>
      {text !== applied.text || size !== applied.pageSize ? (
        <p>Displayed results use the last submitted selection.</p>
      ) : null}
      {busy && <p role="status">Loading public metadata...</p>}
      {error && <p role="alert">{error}</p>}
      {count !== undefined && (
        <p>
          Last read manifest: {count} entries. The manifest is a separate
          observation from this page.
        </p>
      )}
      {page && (
        <section aria-label="Published entries">
          <p role="status">
            Showing {page.entries.length} entries on this page.
            {page.pageToken
              ? " More pages are available."
              : " No further page was returned."}
          </p>
          {!page.entries.length && (
            <p>No entries were returned on this page.</p>
          )}
          <div className="public-entry-grid">
            {page.entries.map((entry, index) => (
              <article
                className="panel public-entry"
                key={`${index}:${entry.identifier}`}
              >
                <h2>{entry.name}</h2>
                <p>{publicKind(entry.type)}</p>
                {entry.description && <p>{entry.description}</p>}
                <details>
                  <summary>Inspect returned metadata</summary>
                  <dl>
                    <dt>Identifier</dt>
                    <dd>{entry.identifier}</dd>
                    <dt>Version</dt>
                    <dd>
                      {entry.version === undefined
                        ? "Not declared"
                        : entry.version === ""
                          ? "Declared empty"
                          : entry.version}
                    </dd>
                    <dt>Media type</dt>
                    <dd>{entry.type || "Not declared"}</dd>
                    {entry.url !== undefined && (
                      <>
                        <dt>Declared location</dt>
                        <dd>{entry.url}</dd>
                      </>
                    )}
                    {entry.source !== undefined && (
                      <>
                        <dt>Reported source</dt>
                        <dd>{entry.source}</dd>
                      </>
                    )}
                    {entry.tags.length > 0 && (
                      <>
                        <dt>Tags</dt>
                        <dd>{entry.tags.join(", ")}</dd>
                      </>
                    )}
                    {entry.capabilities.length > 0 && (
                      <>
                        <dt>Capabilities</dt>
                        <dd>{entry.capabilities.join(", ")}</dd>
                      </>
                    )}
                    {entry.representativeQueries.length > 0 && (
                      <>
                        <dt>Example queries</dt>
                        <dd>{entry.representativeQueries.join("; ")}</dd>
                      </>
                    )}
                  </dl>
                  <p>
                    These fields came from this page. Declared documents,
                    artwork and runtimes have not been requested.
                  </p>
                </details>
              </article>
            ))}
          </div>
          {page.pageToken && (
            <button
              type="button"
              disabled={busy || !canRepeat}
              onClick={() => void load(applied, page.pageToken, false)}
            >
              Next page
            </button>
          )}
        </section>
      )}
    </main>
  );
}
