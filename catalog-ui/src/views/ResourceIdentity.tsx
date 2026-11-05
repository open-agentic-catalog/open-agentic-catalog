import { useEffect, useRef, useState } from "react";
import { Field } from "./workspace";
import {
  buildAIRIdentifier,
  canonicalResourceIdentifier,
  suggestResourceName,
} from "./resource-identity";

/** Optional local AIR construction beside the always-available exact identifier input. */
export function ResourceIdentity({
  value,
  onChange,
  itemName = "",
  disabled = false,
  id = "resource-identifier",
  onDraftChange,
}: {
  value: string;
  onChange: (value: string) => void;
  itemName?: string | undefined;
  disabled?: boolean | undefined;
  id?: string;
  onDraftChange?: (dirty: boolean) => void;
}) {
  const [publisher, setPublisher] = useState("");
  const [namespace, setNamespace] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [touched, setTouched] = useState(false);
  const touch = () => setTouched(true);
  const control = useRef<HTMLInputElement>(null);
  let validation = "";
  try {
    if (!value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, ""))
      throw new Error("Enter a canonical resource identifier.");
    canonicalResourceIdentifier(value);
  } catch (cause) {
    validation = (cause as Error).message;
  }
  useEffect(() => {
    control.current!.setCustomValidity(validation);
  }, [validation]);
  const errorRef = useRef<HTMLParagraphElement>(null);
  useEffect(() => {
    onDraftChange?.(!!publisher || !!namespace || !!name);
  }, [onDraftChange, publisher, namespace, name]);
  useEffect(() => {
    if (error) errorRef.current?.focus();
  }, [error]);
  let preview = "";
  try {
    preview = buildAIRIdentifier({ publisher, namespace, name });
  } catch {
    // Incomplete optional fields are not errors in the surrounding save form.
  }
  const suggestion = suggestResourceName(itemName);
  return (
    <>
      <Field
        label="Canonical resource identifier"
        error={touched ? validation : ""}
        hint="A permanent identifier. Private and tenant releases may use an opaque identifier. Public releases and either discovery audience need an AIR identifier with a domain you control."
      >
        <input
          ref={control}
          required
          id={id}
          disabled={disabled}
          maxLength={512 * 2}
          value={value}
          placeholder="urn:air:example.com:agents:my-agent"
          onBlur={touch}
          onInvalid={touch}
          onFocus={value ? touch : undefined}
          onChange={(event) => {
            touch();
            onChange(event.target.value);
          }}
        />
      </Field>
      <details className="workspace-identity-builder">
        <summary>Build an AIR identifier</summary>
        <p>
          Enter the publisher domain and resource name. AIR namespace segments
          are optional and separate from the administrative Namespace. This
          builder does not verify domain control.
        </p>
        <Field label="Publisher domain" hint="For example, example.com.">
          <input
            id={`${id}-publisher`}
            disabled={disabled}
            value={publisher}
            onChange={(event) => {
              setPublisher(event.target.value);
              setError("");
            }}
          />
        </Field>
        <Field
          label="AIR namespace segments (optional)"
          hint="Separate segments with colons, for example agents:finance. Leave empty for a terminal-only name."
        >
          <input
            id={`${id}-namespace`}
            disabled={disabled}
            value={namespace}
            onChange={(event) => {
              setNamespace(event.target.value);
              setError("");
            }}
          />
        </Field>
        <Field
          label="AIR resource name"
          hint="Use 1 to 64 ASCII letters, digits, dots, underscores or hyphens. Case is preserved."
        >
          <input
            id={`${id}-name`}
            disabled={disabled}
            value={name}
            onChange={(event) => {
              setName(event.target.value);
              setError("");
            }}
          />
        </Field>
        {suggestion && (
          <button
            type="button"
            disabled={disabled}
            onClick={() => {
              setName(suggestion);
              setError("");
            }}
          >
            Use suggested name: {suggestion}
          </button>
        )}
        <p>
          AIR identifier preview:{" "}
          <output aria-label="AIR identifier preview">
            {preview || "Complete the publisher domain and resource name."}
          </output>
        </p>
        <div className="workspace-actions">
          <button
            type="button"
            disabled={disabled}
            onClick={() => {
              try {
                const identifier = buildAIRIdentifier({
                  publisher,
                  namespace,
                  name,
                });
                if (
                  value &&
                  value !== identifier &&
                  !window.confirm(
                    "Replace the entered canonical resource identifier?",
                  )
                )
                  return;
                setError("");
                onChange(identifier);
                setPublisher("");
                setNamespace("");
                setName("");
              } catch (cause) {
                setError((cause as Error).message);
              }
            }}
          >
            Use AIR identifier
          </button>
          <button
            type="button"
            disabled={disabled || !(publisher || namespace || name)}
            onClick={() => {
              setPublisher("");
              setNamespace("");
              setName("");
              setError("");
            }}
          >
            Clear AIR builder
          </button>
        </div>
        {error && (
          <p role="alert" tabIndex={-1} ref={errorRef}>
            {error}
          </p>
        )}
      </details>
    </>
  );
}
