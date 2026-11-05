// Namespace choices use explicit tenant-authorized bounded reads, never source topology.
import { useEffect, useRef, useState } from "react";
import { result } from "../api/client";
import type { Client } from "../api/client";
import type { components } from "../api/schema";
import * as checks from "./responses";

type Page = components["schemas"]["NamespacePage"];

export function NamespaceFilter({
  client,
  value,
  onChange,
  onFailure,
  onChoices,
}: {
  client: Client;
  value: string | undefined;
  onChange: (value: string | undefined) => void;
  onFailure: (error: unknown) => void;
  onChoices?: (items: Page["items"]) => void;
}) {
  const [page, setPage] = useState<Page>();
  const [selection, setSelection] = useState<{ id: string; name: string }>();
  const currentChoice = page?.items.find((item) => item.id === value);
  if (
    currentChoice &&
    (selection?.id !== currentChoice.id ||
      selection.name !== currentChoice.displayName)
  )
    setSelection({ id: currentChoice.id, name: currentChoice.displayName });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => pending.current?.abort(), [client]);
  async function load(cursor?: string) {
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setError("");
    try {
      const reply = result(
        await client.api.GET("/api/v1/namespaces", {
          params: { query: { limit: 25, ...(cursor ? { cursor } : {}) } },
          signal: controller.signal,
        }),
        checks.NamespacePage,
      );
      if (!controller.signal.aborted) {
        setPage(reply);
        onChoices?.(reply.items);
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setError("Namespace choices could not be loaded.");
        onFailure(error);
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  return (
    <section className="filter-group">
      <label className="filter-field">
        Local namespace
        <select
          value={value ?? ""}
          onChange={(e) => onChange(e.target.value || undefined)}
        >
          <option value="">Any local namespace</option>
          {value && !page?.items.some((item) => item.id === value) && (
            <option value={value}>
              {selection?.id === value ? selection.name : value}
            </option>
          )}
          {page?.items.map((item) => (
            <option key={item.id} value={item.id}>
              {item.displayName}
            </option>
          ))}
        </select>
      </label>
      <div className="namespace-filter-notes">
        <p className="filter-help">
          Selecting a namespace shows local releases only. A namespace may have
          no matching releases.
        </p>
        <div className="workspace-toolbar">
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              void load();
            }}
          >
            {page ? "First namespace page" : "Load namespaces"}
          </button>
          {page?.nextCursor && (
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                void load(page.nextCursor);
              }}
            >
              Next namespace page
            </button>
          )}
        </div>
        {busy && <p role="status">Loading namespaces...</p>}
        {error && <p role="alert">{error}</p>}
      </div>
    </section>
  );
}
