import { controlIcons } from "../shell/Icon";
import { useEffect, useRef, useState } from "react";
import { Field } from "./workspace";
import type { RepresentationDraft } from "./representations";
import {
  readSkillDocument,
  skillFieldError,
  writeSkillDocument,
  type SkillDocument,
} from "./skill-markdown";

function SkillField({
  id,
  label,
  field,
  value,
  onChange,
  multiline = false,
  hint,
}: {
  id: string;
  label: string;
  field: string;
  value: string;
  onChange: (value: string) => void;
  multiline?: boolean;
  hint?: string;
}) {
  const control = useRef<HTMLInputElement & HTMLTextAreaElement>(null);
  const [touched, setTouched] = useState(false);
  const touch = () => setTouched(true);
  const error =
    (field === "name" || field === "description") && !value
      ? "Enter a value."
      : skillFieldError(field, value);
  useEffect(() => {
    control.current!.setCustomValidity(error);
  }, [error]);
  const props = {
    id,
    value,
    required: field === "name" || field === "description",
    maxLength: 500_000,
    "aria-invalid": (touched && !!error) || undefined,
    "aria-describedby": touched && error ? `${id}-error` : undefined,
    onBlur: touch,
    onInvalid: touch,
    onFocus: value ? touch : undefined,
    onChange: (
      event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
    ) => {
      touch();
      onChange(event.target.value);
    },
  };
  return (
    <div>
      <Field
        label={label}
        error={touched ? error : ""}
        {...(hint ? { hint } : {})}
      >
        {multiline ? (
          <textarea ref={control} rows={field === "body" ? 10 : 3} {...props} />
        ) : (
          <input ref={control} {...props} />
        )}
      </Field>
    </div>
  );
}

/** The native document is the only draft. Mode changes never rewrite imports. */
export default function SkillEditor({
  value,
  onChange,
  advanced,
  onAdvanced,
  origin,
}: {
  value: RepresentationDraft;
  onChange: (value: RepresentationDraft) => void;
  advanced: boolean;
  origin: number;
  onAdvanced: (open: boolean) => void;
}) {
  const { document, reason } = readSkillDocument(value.content);
  const [failure, setFailure] = useState<{ message: string; origin: number }>();
  const focus = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (focus.current) {
      globalThis.document.getElementById(focus.current)?.focus();
      focus.current = undefined;
    }
  }, [value.content]);
  const prefix = `${value.editorID}-skill`;
  const change = (next: SkillDocument) => {
    try {
      const content = writeSkillDocument(next);
      setFailure(undefined);
      onChange({ ...value, content });
    } catch (cause) {
      setFailure({
        message:
          cause instanceof Error ? cause.message : "Check the Skill fields.",
        origin,
      });
    }
  };
  const field = (
    key:
      | "name"
      | "description"
      | "body"
      | "license"
      | "compatibility"
      | "allowed-tools",
    label: string,
    multiline: boolean,
    hint: string,
  ) => (
    <SkillField
      id={`${prefix}-${key}`}
      label={label}
      field={key}
      value={document![key] ?? ""}
      multiline={multiline}
      hint={hint}
      onChange={(text) => {
        const next = { ...document! };
        if (key !== "name" && key !== "description" && key !== "body" && !text)
          delete next[key];
        else next[key] = text;
        change(next);
      }}
    />
  );
  if (advanced) return null;
  if (!document)
    return (
      <div role="status">
        <p>{reason}</p>
        <button type="button" onClick={() => onAdvanced(true)}>
          Open Advanced SKILL.md
        </button>
      </div>
    );
  return (
    <section className="workspace-fields" aria-label="Skill fields">
      <p>
        Describe the Skill and write its instructions. The Catalog creates
        SKILL.md. Saving does not run the Skill.
      </p>
      {field(
        "name",
        "Skill name",
        false,
        "Use 1 to 64 lowercase letters, digits or single hyphens. The package directory must use this name.",
      )}
      {field(
        "description",
        "Skill description",
        true,
        "Explain what the Skill does and when to use it. Use at most 1024 characters.",
      )}
      {field(
        "body",
        "Skill instructions (Markdown)",
        true,
        "Write the steps, examples and references that a consumer needs.",
      )}
      <details>
        <summary>Optional Skill fields</summary>
        <div className="workspace-fields">
          {field(
            "license",
            "Skill license (optional)",
            false,
            "Enter a license name or the path to a license file in the complete package.",
          )}
          {field(
            "compatibility",
            "Skill compatibility (optional)",
            true,
            "Describe environment requirements in at most 500 characters. Leave blank when none apply.",
          )}
          {field(
            "allowed-tools",
            "Skill allowed tools (optional)",
            false,
            "Experimental, space-separated tool declarations. These do not grant Catalog permissions or authorize execution.",
          )}
          <fieldset className="workspace-panel workspace-fields">
            <legend>Skill metadata</legend>
            <p>
              Add string keys and values. For example, use a version key here if
              your consumer needs one.
            </p>
            {(document.metadata ?? []).map(([key, text], index) => (
              <div key={index} className="workspace-fields">
                <SkillField
                  id={`${prefix}-metadata-${index}-key`}
                  label={`Skill metadata ${index + 1} key`}
                  field="metadata"
                  value={key}
                  onChange={(next) =>
                    change({
                      ...document,
                      metadata: document.metadata!.map((row, at) =>
                        at === index ? [next, row[1]] : row,
                      ),
                    })
                  }
                />
                <SkillField
                  id={`${prefix}-metadata-${index}-value`}
                  label={`Skill metadata ${index + 1} value`}
                  field="metadata"
                  value={text}
                  onChange={(next) =>
                    change({
                      ...document,
                      metadata: document.metadata!.map((row, at) =>
                        at === index ? [row[0], next] : row,
                      ),
                    })
                  }
                />
                <button
                  type="button"
                  onClick={() => {
                    focus.current = `${prefix}-metadata-add`;
                    const next = { ...document };
                    next.metadata = document.metadata!.filter(
                      (_, at) => at !== index,
                    );
                    if (!next.metadata.length) delete next.metadata;
                    change(next);
                  }}
                >
                  {controlIcons.remove} Remove Skill metadata {index + 1}
                </button>
              </div>
            ))}
            <button
              id={`${prefix}-metadata-add`}
              type="button"
              onClick={() => {
                focus.current = `${prefix}-metadata-${document.metadata?.length ?? 0}-key`;
                change({
                  ...document,
                  metadata: [...(document.metadata ?? []), ["", ""]],
                });
              }}
            >
              {controlIcons.add} Add Skill metadata
            </button>
          </fieldset>
        </div>
      </details>
      {failure && failure.origin === origin && (
        <p role="alert">{failure.message}</p>
      )}
    </section>
  );
}
