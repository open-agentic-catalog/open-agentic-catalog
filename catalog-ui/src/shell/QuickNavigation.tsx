import { useEffect, useRef, useState } from "react";
import type { Access } from "../api/client";
import { navigationGroups } from "./navigation";
import { Icon, controlIcons } from "./Icon";

/** Fixed navigation only; links retain the editor's native leave protection. */
export function QuickNavigation({
  access,
  basePath,
}: {
  access: Access;
  basePath: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  const [query, setQuery] = useState("");
  const close = () => {
    dialog.current?.close();
    setQuery("");
    opener.current?.focus();
  };
  useEffect(() => {
    const shortcut = (event: KeyboardEvent) => {
      const target = event.target;
      if (
        event.defaultPrevented ||
        event.altKey ||
        event.shiftKey ||
        !(event.ctrlKey || event.metaKey) ||
        event.key.toLowerCase() !== "k" ||
        document.querySelector("dialog[open]") ||
        (target instanceof HTMLElement &&
          (target.isContentEditable ||
            target.closest("input, textarea, select, [contenteditable]")))
      )
        return;
      event.preventDefault();
      document
        .querySelectorAll<HTMLDetailsElement>(".header-actions details[open]")
        .forEach((entry) => {
          entry.open = false;
        });
      dialog.current?.showModal();
    };
    document.addEventListener("keydown", shortcut);
    return () => document.removeEventListener("keydown", shortcut);
  }, []);
  const policy = JSON.stringify(access);
  // Policy refresh must remove old matches and dismiss a stale page choice.
  useEffect(() => {
    if (dialog.current?.open) {
      dialog.current.close();
      opener.current?.focus();
    }
    setQuery("");
  }, [policy]);
  const groups = navigationGroups(access);
  return (
    <>
      <button
        ref={opener}
        className="quick-navigation-trigger"
        title="Quick navigation (Ctrl+K or Command+K)"
        onClick={() => {
          document
            .querySelectorAll<HTMLDetailsElement>(
              ".header-actions details[open]",
            )
            .forEach((entry) => {
              entry.open = false;
            });
          dialog.current?.showModal();
        }}
      >
        <Icon name="search" />
        <span>Quick navigation</span>
      </button>
      <dialog
        className="quick-navigation"
        ref={dialog}
        aria-labelledby="quick-navigation-title"
        onCancel={(event) => {
          event.preventDefault();
          close();
        }}
      >
        <h2 id="quick-navigation-title">Quick navigation</h2>
        <p>
          Choose a page. Shortcut: Ctrl+K or Command+K outside a text field.
        </p>
        <label>
          Find a page
          <input
            autoFocus
            value={query}
            maxLength={100}
            onChange={(event) => setQuery(event.target.value)}
          />
        </label>
        <nav aria-label="Matching pages">
          {groups.flatMap((group) =>
            group.entries
              .filter(
                (entry) =>
                  entry.allowed &&
                  entry.label
                    .toLowerCase()
                    .includes(query.trim().toLowerCase()),
              )
              .map((entry) => (
                <a
                  key={entry.path}
                  href={`${basePath}/${entry.path}`}
                  onClick={close}
                >
                  <Icon name={entry.icon} />
                  <span>{entry.label}</span>
                  <small>{group.label}</small>
                </a>
              )),
          )}
          {!groups.some((group) =>
            group.entries.some(
              (entry) =>
                entry.allowed &&
                entry.label.toLowerCase().includes(query.trim().toLowerCase()),
            ),
          ) && <p role="status">No matching pages.</p>}
        </nav>
        <button onClick={close}>
          {controlIcons["close"]} Close quick navigation
        </button>
      </dialog>
    </>
  );
}
