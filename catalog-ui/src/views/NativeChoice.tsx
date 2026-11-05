import { useEffect, useRef, useState } from "react";
import { Field } from "./workspace";

/** Offer common metadata values without narrowing the native format's open strings. */
export function NativeChoice({
  id,
  label,
  customLabel,
  value,
  choices,
  onChange,
  validate,
  hint,
}: {
  id: string;
  label: string;
  customLabel: string;
  value: string;
  choices: readonly (readonly [string, string])[];
  onChange: (value: string) => void;
  validate: (value: string) => string;
  hint?: string;
}) {
  const known = choices.some(([key]) => key === value);
  const [touched, setTouched] = useState(false);
  const select = useRef<HTMLSelectElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const message = validate(value);
  useEffect(() => {
    const control = known ? select.current! : input.current!;
    control.setCustomValidity(message);
  }, [known, message]);
  const touch = () => setTouched(true);
  return (
    <div className="native-choice">
      <Field
        label={label}
        {...(hint ? { hint } : {})}
        error={known && touched ? message : ""}
      >
        <select
          id={id}
          ref={select}
          required
          value={known ? value : "__custom__"}
          onInvalid={touch}
          onBlur={touch}
          onChange={(event) => {
            touch();
            const custom = event.target.value === "__custom__";
            onChange(custom ? "" : event.target.value);
            if (custom) queueMicrotask(() => input.current?.focus());
          }}
        >
          {choices.map(([key, text]) => (
            <option key={key} value={key}>
              {text}
            </option>
          ))}
          <option value="__custom__">Other (enter a value)</option>
        </select>
      </Field>
      {!known && (
        <Field label={customLabel} error={touched ? message : ""}>
          <input
            ref={input}
            id={`${id}-custom`}
            required
            value={value}
            maxLength={2 * 1024 * 1024}
            onInvalid={touch}
            onBlur={touch}
            onFocus={value ? touch : undefined}
            onChange={(event) => {
              touch();
              const next = event.target.value;
              onChange(next);
              if (choices.some(([key]) => key === next))
                queueMicrotask(() => select.current?.focus());
            }}
          />
        </Field>
      )}
    </div>
  );
}
