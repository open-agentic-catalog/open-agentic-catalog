import { useState } from "react";
import type { LoginOptions } from "../api/session";
import { Icon, controlIcons } from "./Icon";

const groups = [
  { key: "bundle", label: "Development bundles" },
  { key: "template", label: "Exact templates" },
  { key: undefined, label: "Other configured identities" },
] as const;

/** Public host presentation describes seed intent, never current authority. */
export function SignInChoices({
  users,
  busy,
  onSelect,
}: {
  users: LoginOptions["users"];
  busy: boolean;
  onSelect: (alias: string) => void;
}) {
  const [query, setQuery] = useState("");
  if (!users.length) return null;
  const term = query.trim().toLocaleLowerCase();
  const matching = users.filter((user) =>
    `${user.label} ${user.description ?? ""} ${user.alias}`
      .toLocaleLowerCase()
      .includes(term),
  );
  return (
    <>
      <p className="muted">
        These are local shortcuts, not production accounts or passwords.
      </p>
      <details className="login-choice-help">
        <summary>About bundles and exact templates</summary>
        <p>
          Bundles describe configured workflows that can combine templates;
          exact templates start with one template. Retained grants can differ
          from this setup. Check My access after signing in.
        </p>
        <p>
          Reader and discovery is useful for trusted local reading, including
          private records. It is not published-only. Choose an administrator
          separately for management.
        </p>
      </details>
      <label className="login-identity-search">
        Find a development identity
        <input
          type="search"
          value={query}
          maxLength={300}
          disabled={busy}
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>
      {query && (
        <button type="button" disabled={busy} onClick={() => setQuery("")}>
          {controlIcons["close"]} Clear identity search
        </button>
      )}
      <p role="status">
        Showing {matching.length} of {users.length} identities.
      </p>
      {!matching.length && (
        <p>
          No matching development identity. Clear the search to see all choices.
        </p>
      )}
      <div className="login-users">
        {groups.map((group) => {
          const all = users.filter((user) => user.profileGroup === group.key);
          const visible = matching.filter(
            (user) => user.profileGroup === group.key,
          );
          if (!visible.length) return null;
          return (
            <section key={group.key ?? "other"} aria-label={group.label}>
              <h3>
                {group.label} ({visible.length}
                {visible.length !== all.length ? ` of ${all.length}` : ""})
              </h3>
              {visible.map((user) => (
                <button
                  type="button"
                  key={user.alias}
                  data-login-alias={user.alias}
                  aria-label={user.label}
                  aria-describedby={
                    [
                      user.description ? `login-description-${user.alias}` : "",
                      user.elevated ? `login-elevated-${user.alias}` : "",
                      user.separateTenant ? `login-tenant-${user.alias}` : "",
                    ]
                      .filter(Boolean)
                      .join(" ") || undefined
                  }
                  disabled={busy}
                  onClick={() => onSelect(user.alias)}
                >
                  <span className="login-user-icon">
                    <Icon name="user" />
                  </span>
                  <span className="login-user-copy">
                    <strong>{user.label}</strong>
                    {user.description && (
                      <small id={`login-description-${user.alias}`}>
                        {user.description}
                      </small>
                    )}
                    {user.elevated && (
                      <small id={`login-elevated-${user.alias}`}>
                        Elevated administration
                      </small>
                    )}
                    {user.separateTenant && (
                      <small id={`login-tenant-${user.alias}`}>
                        Separate tenant
                      </small>
                    )}
                  </span>
                  <Icon name="arrow" />
                </button>
              ))}
            </section>
          );
        })}
      </div>
    </>
  );
}
