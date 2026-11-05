import { useState } from "react";
import { Field } from "./workspace";

type DirectoryFilters = {
  displayName?: string;
  slug?: string;
  declaredDomain?: string;
};

/** Submit exact server-side criteria; typing never scans or changes a page. */
export function CatalogDirectoryFilters({
  kind,
  busy,
  applied,
  onApply,
}: {
  kind: "namespace" | "publisher";
  busy: boolean;
  applied: DirectoryFilters;
  onApply: (filters: DirectoryFilters) => void;
}) {
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const changed =
    name !== (applied.displayName ?? "") ||
    key !==
      ((kind === "namespace" ? applied.slug : applied.declaredDomain) ?? "");
  return (
    <>
      <form
        className="workspace-toolbar"
        aria-label={`${kind === "namespace" ? "Namespace" : "Publisher"} filters`}
        onSubmit={(event) => {
          event.preventDefault();
          onApply({
            ...(name ? { displayName: name } : {}),
            ...(key
              ? kind === "namespace"
                ? { slug: key }
                : { declaredDomain: key }
              : {}),
          });
        }}
      >
        <Field
          label="Exact display name"
          hint="Case and spacing must match. Duplicate names remain separate records."
        >
          <input
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field
          label={kind === "namespace" ? "Exact slug" : "Exact declared domain"}
          hint={
            kind === "namespace"
              ? "Slugs are trimmed and lowercased. Both filters must match when supplied."
              : "Domains are normalized as on creation. Declaration is separate from publication approval. Both filters must match when supplied."
          }
        >
          <input value={key} onChange={(event) => setKey(event.target.value)} />
        </Field>
        <div className="workspace-actions">
          <button type="submit" disabled={busy}>
            Apply filters
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              setName("");
              setKey("");
              onApply({});
            }}
          >
            Clear filters
          </button>
        </div>
      </form>
      <p className="muted" role="group" aria-label="Applied directory filters">
        Applied filters:{" "}
        {applied.displayName || applied.slug || applied.declaredDomain ? (
          <>
            {applied.displayName && (
              <>
                display name{" "}
                <code style={{ whiteSpace: "pre-wrap" }}>
                  {JSON.stringify(applied.displayName)}
                </code>
              </>
            )}
            {(applied.slug || applied.declaredDomain) && (
              <>
                {applied.displayName ? "; " : ""}
                {kind === "namespace" ? "slug" : "declared domain"}{" "}
                <code style={{ whiteSpace: "pre-wrap" }}>
                  {JSON.stringify(applied.slug ?? applied.declaredDomain)}
                </code>
              </>
            )}
          </>
        ) : (
          `all ${kind}s`
        )}
        .{" "}
        {changed
          ? "Edits are not applied. Choose Apply filters to update results."
          : "Edit the fields, then choose Apply filters to update results."}
      </p>
    </>
  );
}
