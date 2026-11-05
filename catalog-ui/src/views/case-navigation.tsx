// Private case navigation retains one list's criteria in its mounted owner only.
import { useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { confirmDiscard, Field, type WorkspaceProps } from "./workspace";

/** History contains an ordinal only. Policy changes invalidate the list return context without replacing private editors. */
export function useCaseNavigation(
  props: WorkspaceProps,
  section: string,
  onListNavigate?: (search: string) => void,
  { preserveDirectoryFilters = false } = {},
) {
  const root = `${props.runtime.basePath}/${section}`;
  const [listSearch, setListSearch] = useState(() =>
    location.pathname === root ? location.search : "",
  );
  const currentSearch = useRef(listSearch);
  const listMoved = useRef(onListNavigate);
  listMoved.current = onListNavigate;
  const [id, setId] = useState(props.id.toLowerCase());
  const index = useRef(0);
  const returning = useRef(false);
  const policyKey = JSON.stringify(props.access);
  const returnPolicy = useRef(policyKey);
  useLayoutEffect(() => {
    returning.current = false;
    if (returnPolicy.current !== policyKey) {
      if (!preserveDirectoryFilters) {
        currentSearch.current = "";
        setListSearch("");
        if (location.pathname === root)
          history.replaceState(history.state as unknown, "", root);
      }
      returnPolicy.current = policyKey;
    }
  }, [policyKey, root, preserveDirectoryFilters]);
  useLayoutEffect(() => setId(props.id.toLowerCase()), [props.id]);
  useLayoutEffect(() => {
    const state: unknown = history.state;
    const ordinal =
      state && typeof state === "object" && "caseNavigation" in state
        ? state.caseNavigation
        : undefined;
    index.current =
      typeof ordinal === "number" && Number.isSafeInteger(ordinal)
        ? ordinal
        : 0;
    history.replaceState({ caseNavigation: index.current }, "");
    const moved = (event: Event) => {
      const state: unknown = (event as CustomEvent<unknown>).detail;
      const next =
        state && typeof state === "object" && "caseNavigation" in state
          ? state.caseNavigation
          : undefined;
      if (typeof next !== "number" || !Number.isSafeInteger(next)) return;
      if (next !== index.current && !confirmDiscard()) {
        event.preventDefault();
        history.go(index.current - next);
        return;
      }
      index.current = next;
      if (
        location.pathname === root ||
        location.pathname.startsWith(root + "/")
      )
        setId(location.pathname.slice(root.length + 1).toLowerCase());
      if (
        location.pathname === root &&
        currentSearch.current !== location.search
      ) {
        currentSearch.current = location.search;
        setListSearch(location.search);
        listMoved.current?.(location.search);
      }
    };
    window.addEventListener("catalog-ui:navigate", moved);
    return () => window.removeEventListener("catalog-ui:navigate", moved);
  }, [root]);
  const move = (next: string) => {
    if (!confirmDiscard()) return;
    next = next.toLowerCase();
    if (next) {
      returning.current = true;
      returnPolicy.current = policyKey;
    }
    history.pushState(
      { caseNavigation: ++index.current },
      "",
      next
        ? `${root}/${encodeURIComponent(next)}`
        : root + currentSearch.current,
    );
    setId(next);
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
  };
  return {
    id,
    root,
    listRoot: root + listSearch,
    listSearch,
    // The owning filter confirms discarded drafts before changing this list URL.
    updateListSearch: (search: string) => {
      history.pushState({ caseNavigation: ++index.current }, "", root + search);
      currentSearch.current = search;
      setListSearch(search);
    },
    hasReturn: returning.current && returnPolicy.current === policyKey,
    move,
    open: (event: MouseEvent<HTMLAnchorElement>, next: string) => {
      if (
        event.button ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      )
        return;
      event.preventDefault();
      move(next);
    },
  };
}

/** Explicit exact lookup never scans an inbox or stores private case text in a URL. */
export function CaseLookup({
  kind,
  open,
}: {
  kind: "feedback" | "rating";
  open: (id: string) => void;
}) {
  const [id, setId] = useState("");
  return (
    <form
      className="workspace-panel"
      onSubmit={(event) => {
        event.preventDefault();
        if (id.trim()) open(id.trim());
      }}
    >
      <Field
        label={`Exact ${kind} ID`}
        hint="Open one currently authorized case. This does not search the list or read its resource."
      >
        <input
          required
          maxLength={36}
          value={id}
          onChange={(event) => setId(event.target.value)}
          pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
        />
      </Field>
      <button type="submit">Open {kind} case</button>
    </form>
  );
}
