// Stage literal native-card selectors using the shared finite value editor.
import { useId } from "react";
import { Field } from "./workspace";
import { ValueList } from "./ValueList";
import {
  nativeArrayFields,
  type NativeCapabilities,
} from "./native-capabilities";

export function NativeCapabilityFilters({
  value,
  onChange,
  prefix = "filter",
}: {
  prefix?: string;
  value: NativeCapabilities | undefined;
  onChange: (value: NativeCapabilities | undefined) => void;
}) {
  const heading = useId();
  function update(
    field: keyof NativeCapabilities,
    next: string[] | boolean | undefined,
  ) {
    const changed = { ...value, [field]: next };
    if (next === undefined) delete changed[field];
    onChange(Object.keys(changed).length ? changed : undefined);
  }
  return (
    <section className="filter-group" aria-labelledby={heading}>
      <h3 id={heading}>Declared capabilities</h3>
      <p className="filter-help">
        Match exact declarations in a released primary card. Requires
        content-read access and Local Catalog only. Choose one card kind. Values
        within a field are alternatives; every selected field must match.
        Different skills or remotes may supply those matches. This does not
        verify compatibility or call a resource.
      </p>
      {(["Agent", "MCP server"] as const).map((kind) => (
        <details className="filter-more" key={kind}>
          <summary>{kind} declarations</summary>
          {nativeArrayFields
            .filter(([field]) =>
              field.startsWith(kind === "Agent" ? "agent" : "mcp"),
            )
            .map(([field, label, itemLabel]) => (
              <ValueList
                key={field}
                id={`${prefix}-${field}`}
                label={label}
                itemLabel={itemLabel}
                value={value?.[field] ?? []}
                maximum={16}
                length={256}
                required={false}
                onChange={(values) =>
                  update(field, values.length ? values : undefined)
                }
              />
            ))}
          {kind === "Agent" && (
            <Field label="Agent streaming declaration">
              <select
                id={`${prefix}-agentStreaming`}
                aria-label="Agent streaming declaration"
                value={
                  value?.agentStreaming === undefined
                    ? ""
                    : String(value.agentStreaming)
                }
                onChange={(event) =>
                  update(
                    "agentStreaming",
                    event.target.value === ""
                      ? undefined
                      : event.target.value === "true",
                  )
                }
              >
                <option value="">Any streaming declaration</option>
                <option value="true">Declared true</option>
                <option value="false">Declared false</option>
              </select>
            </Field>
          )}
        </details>
      ))}
      <p className="filter-help">
        Input and output modes are card defaults, not per-skill modes. Missing
        declarations do not match, including a missing streaming flag when false
        is selected. Case, spaces and commas are literal.
      </p>
      {value && (
        <button type="button" onClick={() => onChange(undefined)}>
          Clear declared capabilities
        </button>
      )}
    </section>
  );
}
