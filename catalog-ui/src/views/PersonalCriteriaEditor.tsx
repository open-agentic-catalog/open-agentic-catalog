import { catalogOrders } from "./catalog-order";
import type { PersonalCriteria } from "./personal-criteria";
import { criteriaDraft } from "./personal-criteria";
import { MetadataEditor } from "./MetadataEditor";
import { KindSelection } from "./KindSelection";
import { ValueList } from "./ValueList";
import { Field } from "./workspace";
import { visibilityOptions } from "./SearchFilters";

/** Shared native controls edit one private JSON draft without Search, Explore or provider requests. */
export function PersonalCriteriaEditor({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <MetadataEditor
      id={id}
      label="Private saved criteria"
      value={value}
      onChange={onChange}
      validate={criteriaDraft}
    >
      {(criteria, update) => {
        const optional = (key: keyof PersonalCriteria, value: string) => {
          const next = { ...criteria };
          if (value === "") delete next[key];
          else Object.assign(next, { [key]: value });
          update(next);
        };
        return (
          <>
            <p className="muted">
              Editing and saving send no Search or Explore request. Search
              execution later requires current access and deliberate
              provider/live disclosure consent. Values below are captured when
              you prepare the save.
            </p>
            <Field
              label="Saved search text"
              hint="Up to 500 UTF-8 bytes. Empty text is supported; live-only execution can need text later."
            >
              <textarea
                id={`${id}-text`}
                rows={3}
                value={criteria.text ?? ""}
                onChange={(e) => update({ ...criteria, text: e.target.value })}
              />
            </Field>
            <details>
              <summary>More saved filters</summary>
              <Field label="Saved sort order">
                <select
                  id={`${id}-order`}
                  value={criteria.order ?? ""}
                  onChange={(e) => optional("order", e.target.value)}
                >
                  <option value="">Default order</option>
                  {catalogOrders.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </Field>
              <KindSelection
                value={criteria.kinds ?? []}
                onChange={(kinds) => update({ ...criteria, kinds })}
              />
              <ValueList
                id={`${id}-tags`}
                label="Saved tags"
                itemLabel="Tag"
                value={criteria.tags ?? []}
                onChange={(tags) => update({ ...criteria, tags })}
                maximum={32}
                length={80}
                required={false}
                hint="All selected tags must match. Exact stored spelling is preserved."
              />
              <ValueList
                id={`${id}-categories`}
                label="Saved categories"
                itemLabel="Category"
                value={criteria.categories ?? []}
                onChange={(categories) => update({ ...criteria, categories })}
                maximum={16}
                length={80}
                hint="All selected categories must match after search normalization. These controls load no suggestions."
              />
              <fieldset className="filter-group">
                <legend>Saved origins</legend>
                <label>
                  <input
                    id={`${id}-origins`}
                    type="checkbox"
                    checked={criteria.origins !== undefined}
                    onChange={(e) => {
                      const next = { ...criteria };
                      if (e.target.checked) next.origins = ["local"];
                      else delete next.origins;
                      update(next);
                    }}
                  />
                  Specify origins
                </label>
                {criteria.origins !== undefined ? (
                  (["local", "indexed", "live"] as const).map((origin) => (
                    <label className="filter-option" key={origin}>
                      <input
                        type="checkbox"
                        checked={criteria.origins!.includes(origin)}
                        onChange={(e) =>
                          update({
                            ...criteria,
                            origins: e.target.checked
                              ? [...criteria.origins!, origin]
                              : criteria.origins!.filter((v) => v !== origin),
                          })
                        }
                      />
                      {origin === "local"
                        ? "Local Catalog"
                        : origin === "indexed"
                          ? "Indexed external"
                          : "Live external"}
                    </label>
                  ))
                ) : (
                  <p>
                    Origins are not specified. Saving retains that omission;
                    current Catalog participation applies when you explicitly
                    run the search.
                  </p>
                )}
                <p className="muted">
                  Live search can disclose text and filters to approved sources.
                  A Namespace permits unspecified origins or Local Catalog
                  alone.
                </p>
              </fieldset>
              <Field
                label="Saved Namespace ID"
                hint="Optional exact UUID. No directory read or access grant is implied."
              >
                <input
                  id={`${id}-namespaceId`}
                  value={criteria.namespaceId ?? ""}
                  onChange={(e) => optional("namespaceId", e.target.value)}
                />
              </Field>
              <Field
                label="Saved Publisher"
                hint="Exact local Publisher UUID or external AIR publisher, up to 256 UTF-8 bytes."
              >
                <input
                  id={`${id}-publisher`}
                  value={criteria.publisher ?? ""}
                  onChange={(e) => optional("publisher", e.target.value)}
                />
              </Field>
              <Field
                label="Saved license declaration"
                hint="Up to 256 characters; this declaration does not establish rights."
              >
                <input
                  id={`${id}-license`}
                  value={criteria.license ?? ""}
                  onChange={(e) => optional("license", e.target.value)}
                />
              </Field>
              <Field label="Saved visibility">
                <select
                  id={`${id}-visibility`}
                  value={criteria.visibility ?? ""}
                  onChange={(e) => optional("visibility", e.target.value)}
                >
                  {visibilityOptions.map((v) => (
                    <option key={v.value} value={v.value}>
                      {v.label}
                    </option>
                  ))}
                </select>
              </Field>
            </details>
          </>
        );
      }}
    </MetadataEditor>
  );
}
