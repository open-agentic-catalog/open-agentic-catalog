import { useLayoutEffect, useRef } from "react";
import { Field } from "./workspace";

/** One row is one value: commas and embedded whitespace are never separators. */
export function ValueList({
  id,
  label,
  itemLabel,
  value,
  onChange,
  maximum,
  length,
  hint,
  clearable = false,
  required = true,
}: {
  id: string;
  label: string;
  itemLabel: string;
  value: string[];
  onChange: (value: string[]) => void;
  maximum: number;
  length: number;
  hint?: string;
  clearable?: boolean;
  required?: boolean;
}) {
  const container = useRef<HTMLFieldSetElement>(null);
  const add = useRef<HTMLButtonElement>(null);
  const focus = useRef<number | null>(null);
  // Resolve a requested row against the DOM from the same commit, before a
  // subsequent interaction can replace that request.
  useLayoutEffect(() => {
    if (focus.current === null) return;
    const index = Math.min(focus.current, value.length - 1);
    if (index < 0) add.current!.focus();
    else container.current!.querySelectorAll("textarea").item(index).focus();
    focus.current = null;
  }, [value.length]);
  return (
    <fieldset ref={container} className="workspace-panel">
      <legend>{label}</legend>
      <p className="muted">
        {hint} One row is one value, including any comma. {value.length} /{" "}
        {maximum} values; up to {length} characters each. Remove a row to remove
        its value.
      </p>
      {value.map((item, index) => (
        <div key={index} className="value-row">
          <Field
            label={`${itemLabel} ${index + 1}`}
            hint="Commas are literal, not separators."
          >
            <textarea
              id={index ? `${id}-${index}` : id}
              rows={1}
              required={required}
              maxLength={length * 2}
              value={item}
              onChange={(event) =>
                onChange(
                  value.map((old, at) =>
                    at === index ? event.target.value : old,
                  ),
                )
              }
            />
          </Field>
          <button
            type="button"
            onClick={() => {
              focus.current = Math.min(index, value.length - 2);
              onChange(value.filter((_, at) => at !== index));
            }}
          >
            Remove {itemLabel.toLowerCase()} {index + 1}
          </button>
        </div>
      ))}
      <button
        ref={add}
        type="button"
        id={value.length ? `${id}-add` : id}
        disabled={value.length >= maximum}
        onClick={() => {
          focus.current = value.length;
          onChange([...value, ""]);
        }}
      >
        Add {itemLabel.toLowerCase()}
      </button>
      {clearable && value.length > 0 && (
        <button
          type="button"
          onClick={() => {
            focus.current = -1;
            onChange([]);
          }}
        >
          Clear {label.toLowerCase()}
        </button>
      )}
    </fieldset>
  );
}
