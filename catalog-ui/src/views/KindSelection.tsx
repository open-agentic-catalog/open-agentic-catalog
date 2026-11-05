// Shared multi-kind controls keep Search and local facets on the same any-kind semantics.
import type { Kind } from "../api/client";
import "./search-filters.css";
import { kindChoices } from "../resource-kinds";

/** Stage a union of resource kinds; selecting none leaves the query unrestricted by kind. */
export function KindSelection({
  value,
  onChange,
}: {
  value: Kind[];
  onChange: (value: Kind[]) => void;
}) {
  return (
    <fieldset className="filter-group kind-selection">
      <legend>Resource kinds</legend>
      <p className="filter-help">
        Match any selected kind. None selected includes all kinds.
      </p>
      <div className="kind-options">
        {kindChoices.map(({ value: kind, label }) => (
          <label className="filter-option" key={kind}>
            <input
              type="checkbox"
              checked={value.includes(kind)}
              onChange={(event) =>
                onChange(
                  event.target.checked
                    ? [...value, kind]
                    : value.filter((item) => item !== kind),
                )
              }
            />
            <span>{label}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
