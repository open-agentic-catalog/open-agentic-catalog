import { controlIcons } from "../shell/Icon";
import { useState } from "react";
import type { ReactNode } from "react";
import { parseObject } from "./representations";
import { Field } from "./workspace";

/** Both views edit one JSON value. Invalid advanced input stays visible until corrected. */
export function MetadataEditor<T>({
  id,
  value,
  onChange,
  label,
  validate,
  children,
  initiallyAdvanced = false,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  label: string;
  validate: (value: Record<string, unknown>) => T;
  children: (value: T, update: (value: T) => void) => ReactNode;
  initiallyAdvanced?: boolean;
}) {
  const [advanced, setAdvanced] = useState(initiallyAdvanced);
  const [error, setError] = useState("");
  return (
    <fieldset className="workspace-panel">
      <legend>{label}</legend>
      <button
        id={advanced ? undefined : id}
        type="button"
        aria-pressed={advanced}
        onClick={() => {
          if (advanced) {
            try {
              validate(parseObject(value));
            } catch (cause) {
              setError(
                cause instanceof Error ? cause.message : "Check the JSON.",
              );
              return;
            }
          }
          setError("");
          setAdvanced(!advanced);
        }}
      >
        {controlIcons.edit}
        {advanced
          ? `Use guided ${label.toLowerCase()}`
          : `Edit ${label.toLowerCase()} as JSON`}
      </button>
      {error && <p role="alert">{error}</p>}
      {advanced ? (
        <Field
          label={`${label} JSON`}
          hint="Advanced mode edits the same value as the guided controls. Only documented fields are accepted."
        >
          <textarea
            id={id}
            rows={8}
            value={value}
            onChange={(event) => {
              setError("");
              onChange(event.target.value);
            }}
          />
        </Field>
      ) : (
        children(validate(parseObject(value || "{}")), (next) => {
          try {
            const text = JSON.stringify(next);
            // Guided fields share the bounded JSON editor. Reject an oversized
            // update before it replaces the draft and fails during rendering.
            validate(parseObject(text));
            setError("");
            onChange(text);
          } catch (cause) {
            setError(
              cause instanceof Error ? cause.message : "Check the metadata.",
            );
          }
        })
      )}
    </fieldset>
  );
}
