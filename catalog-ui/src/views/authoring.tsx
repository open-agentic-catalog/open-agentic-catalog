// Authoring controls combine native form validation with prepared-command confirmation.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { InputError } from "./input-validation";
import { JSONBlock } from "./CodeBlock";
import type { ReactNode, RefObject } from "react";
import { controlIcons, type IconName } from "../shell/Icon";
import {
  CommandDialog,
  Field,
  RequiredFields,
  FieldValidationContext,
  Pager,
  ReadState,
  useRead,
  useUnsaved,
} from "./workspace";
import type { ActionIntent, Command } from "./workspace";

/** Reveal invalid controls before native validation tries to focus them. */
export function revealInvalidControl(control: Element) {
  for (
    let section = control.closest("details");
    section;
    section = section.parentElement?.closest("details") || null
  )
    section.open = true;
}

/** Validate one step or local preparation area without submitting the form. */
export function reportInvalidFields(container: HTMLElement): boolean {
  let first:
    HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | undefined;
  for (const control of container.querySelectorAll<
    HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement
  >("input,select,textarea")) {
    if (!control.willValidate || control.checkValidity()) continue;
    revealInvalidControl(control);
    first ??= control;
  }
  if (!first) return true;
  first.focus();
  first.reportValidity();
  return false;
}

/** Common confirmation/recovery behavior; each screen still owns its typed API
 * command. prepare validates and captures input without sending a request;
 * only the returned function may write after explicit confirmation. */
export type ActionValidation = { check: (validate: () => void) => boolean };

export function ActionForm({
  title,
  description,
  intent = "primary",
  icon,
  allowed,
  editable = true,
  retainDraft = false,
  unavailableReason,
  resultLink,
  another,
  onAcknowledged,
  resetToken,
  focusAfterReset,
  onPendingChange,
  dirty = false,
  requiredMarkers = false,
  children,
  prepare,
  onFailure,
  validation,
}: {
  title: string;
  description: string;
  intent?: ActionIntent;
  icon?: IconName;
  allowed: boolean;
  /** Editing authority is separate from readiness; prerequisite inputs may remain usable. */
  editable?: boolean;
  /** The parent retains actual unsent input across readback remounts. */
  retainDraft?: boolean;
  unavailableReason?: string | undefined;
  resultLink?: { href: string; label: string };
  another?: { label: string; reset: () => void; allowed?: boolean };
  /** A parent may own the receipt when explicit readback replaces this editor. */
  onAcknowledged?: (message: string) => void;
  resetToken?: number;
  focusAfterReset?: readonly string[] | undefined;
  onPendingChange?: (pending: boolean) => void;
  dirty?: boolean;
  requiredMarkers?: boolean;
  children?: ReactNode;
  prepare: () =>
    Command["run"] | Pick<Command, "run" | "summary" | "details" | "expiresAt">;
  onFailure: (e: unknown) => void;
  validation?: RefObject<ActionValidation | null>;
}) {
  const [command, setCommand] = useState<Command>();
  const previousReset = useRef(resetToken);
  useEffect(() => {
    if (previousReset.current === resetToken) return;
    previousReset.current = resetToken;
    setConsumed(false);
    setMessage("");
  }, [resetToken]);
  useEffect(() => {
    onPendingChange?.(!!command);
  }, [command, onPendingChange]);
  const [message, setMessage] = useState("");
  const [consumed, setConsumed] = useState(false);
  const [acknowledgements, setAcknowledgements] = useState(0);
  // Access loss disables new editing, but previously typed input still needs
  // discard protection. Initial read-only facts never become an editable draft.
  const [protectedDraft, setProtectedDraft] = useState(
    dirty && (editable || retainDraft),
  );
  if (dirty && (editable || retainDraft) && !protectedDraft)
    setProtectedDraft(true);
  else if (!dirty && protectedDraft) setProtectedDraft(false);
  const [error, setError] = useState<{
    text: string;
    control: HTMLElement | null;
  }>();
  const summary = useRef<HTMLParagraphElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const checkInput = (validate: () => void): boolean => {
    setError(undefined);
    try {
      validate();
      return true;
    } catch (cause) {
      const control =
        cause instanceof InputError
          ? (form.current!.elements.namedItem(cause.fieldId) ??
            (cause.fallbackId
              ? form.current!.elements.namedItem(cause.fallbackId)
              : null))
          : null;
      setError({
        text: cause instanceof Error ? cause.message : "Check the input.",
        control: control instanceof HTMLElement ? control : null,
      });
      return false;
    }
  };
  useLayoutEffect(() => {
    if (!validation) return;
    validation.current = { check: checkInput };
    return () => {
      validation.current = null;
    };
  });
  useEffect(() => {
    if (!consumed && acknowledgements) {
      const preferred = focusAfterReset
        ?.map((id) => document.getElementById(id))
        .find(
          (control) =>
            control &&
            !control.matches(":disabled") &&
            !control.closest("[hidden]"),
        );
      (
        preferred ??
        form.current?.querySelector<HTMLElement>(
          "input:not(:disabled), select:not(:disabled), textarea:not(:disabled), button:not(:disabled)",
        )
      )?.focus();
    }
  }, [consumed, acknowledgements, focusAfterReset]);
  useEffect(() => {
    if (error) summary.current!.focus();
  }, [error]);
  useEffect(() => {
    // Mode changes and row removal can remove a field named by an earlier error.
    if (error?.control && !form.current?.contains(error.control))
      setError(undefined);
  }, [children, error]);
  useUnsaved(dirty && (editable || protectedDraft) && !consumed);
  return (
    <section className="workspace-panel">
      <h2>{title}</h2>
      <p className="muted">
        {description}
        {requiredMarkers && (
          <span className="required-legend required-legend-inline">
            <span aria-hidden="true">*</span> Required
          </span>
        )}
      </p>
      <form
        ref={form}
        className="workspace-form"
        onChangeCapture={(event) => {
          if (error?.control === event.target) setError(undefined);
        }}
        onInvalidCapture={(e) => revealInvalidControl(e.target as Element)}
        onSubmit={(e) => {
          e.preventDefault();
          if (!editable || !allowed || consumed || command) return;
          checkInput(() => {
            const prepared = prepare();
            setCommand({
              title,
              description,
              intent,
              ...(icon ? { icon } : {}),
              ...(typeof prepared === "function"
                ? { run: prepared }
                : prepared),
            });
          });
        }}
      >
        <FieldValidationContext
          value={
            error?.control?.id
              ? { fieldId: error.control.id, message: error.text }
              : undefined
          }
        >
          <fieldset
            className="workspace-fields"
            disabled={!editable || !!command || consumed}
          >
            {(editable || protectedDraft || command) &&
              (requiredMarkers ? (
                <RequiredFields legend={false}>{children}</RequiredFields>
              ) : (
                children
              ))}
            {(editable || protectedDraft || command) && (
              <div className="form-actions">
                <button
                  className={intent}
                  type="submit"
                  disabled={!editable || !allowed}
                >
                  {icon && controlIcons[icon]}
                  {title}
                </button>
              </div>
            )}
          </fieldset>
        </FieldValidationContext>
      </form>
      {(!allowed || !editable) && (
        <p className="muted">
          {unavailableReason ??
            "This action is unavailable with your current access or the record's current state."}
        </p>
      )}
      {error && (
        <p role="alert" className="notice error" tabIndex={-1} ref={summary}>
          {error.control ? (
            <a
              href={`#${error.control.id}`}
              onClick={(event) => {
                event.preventDefault();
                revealInvalidControl(error.control!);
                error.control!.focus();
              }}
            >
              {error.text}
            </a>
          ) : (
            error.text
          )}
        </p>
      )}
      {message && (
        <CommandReceipt key={acknowledgements} message={message}>
          {resultLink && (
            <>
              {" "}
              <a href={resultLink.href}>{resultLink.label}</a>
            </>
          )}
          {another && consumed && (
            <button
              type="button"
              disabled={!editable || !(another.allowed ?? allowed)}
              onClick={() => {
                another.reset();
                setConsumed(false);
              }}
            >
              {another.label}
            </button>
          )}
        </CommandReceipt>
      )}
      {command && (
        <CommandDialog
          command={command}
          allowed={editable && allowed}
          onFailure={onFailure}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            if (onAcknowledged) onAcknowledged(text);
            else setMessage(text);
            setConsumed(true);
            setAcknowledgements((v) => v + 1);
            setCommand(undefined);
          }}
        />
      )}
    </section>
  );
}

/** The acknowledgement owns focus after the command dialog closes. */
export function CommandReceipt({
  message,
  children,
}: {
  message: string;
  children?: ReactNode;
}) {
  const receipt = useRef<HTMLParagraphElement>(null);
  useEffect(() => {
    if (message) receipt.current?.focus();
  }, [message]);
  return message ? (
    <p role="status" className="notice success" tabIndex={-1} ref={receipt}>
      {message}
      {children}
    </p>
  ) : null;
}

/** Keep structured diagnostic/evidence details collapsed until the reader opens them. */
export function EvidenceDetails({
  title,
  value,
}: {
  title: string;
  value: unknown;
}) {
  return (
    <details className="workspace-panel">
      <summary>{title}</summary>
      <JSONBlock value={value} />
    </details>
  );
}

export type Choice = { id: string; label: string };
/** A page selector, not an eager tenant-wide inventory. Selected IDs stay explicit. */
type ChoiceProps = {
  label: string;
  value: string;
  onChange: (id: string) => void;
  load: (
    cursor: string,
    signal: AbortSignal,
    search: string,
  ) => Promise<{ items: Choice[]; nextCursor?: string | undefined }>;
  onFailure: (e: unknown) => void;
  queryKey?: string;
  manualLabel?: string;
  pagerLabel?: string;
  required?: boolean;
  nextSteps?: ReactNode;
  searchLabel?: string;
};

export function ChoiceField(props: ChoiceProps) {
  return <PagedChoiceField key={props.queryKey ?? props.label} {...props} />;
}

function PagedChoiceField({
  label,
  value,
  onChange,
  load,
  onFailure,
  queryKey = label,
  manualLabel,
  pagerLabel,
  required = true,
  nextSteps,
  searchLabel,
}: ChoiceProps) {
  const [cursor, setCursor] = useState("");
  const [search, setSearch] = useState("");
  const [applied, setApplied] = useState("");
  const page = useRead(
    JSON.stringify([queryKey, applied, cursor]),
    (signal) => load(cursor, signal, applied),
    onFailure,
  );
  const [selected, setSelected] = useState<Choice>();
  useEffect(() => {
    const choice = page.data?.items.find((item) => item.id === value);
    if (choice) setSelected(choice);
  }, [page.data, value]);
  return (
    <div className="workspace-choice">
      {searchLabel && (
        <div className="workspace-toolbar">
          <Field
            label={searchLabel}
            hint="Exact spelling. Applying a filter preserves the selected ID."
          >
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  setCursor("");
                  setApplied(search);
                }
              }}
            />
          </Field>
          <button
            type="button"
            onClick={() => {
              setCursor("");
              setApplied(search);
            }}
          >
            Apply choice filter
          </button>
        </div>
      )}
      <Field
        label={label}
        hint="Choose an existing item. Next page shows more choices; your selection is kept while paging."
      >
        <select
          required={required}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">Select {label.toLowerCase()}</option>
          {value && !page.data?.items.some((i) => i.id === value) && (
            <option value={value}>
              {selected?.id === value
                ? `${selected.label} (selected)`
                : `${value} (${manualLabel ? "unverified ID" : "selected"})`}
            </option>
          )}
          {page.data?.items.map((item) => (
            <option key={item.id} value={item.id}>
              {item.label}
            </option>
          ))}
        </select>
      </Field>
      {manualLabel && (
        <Field
          label={manualLabel}
          hint="Enter an exact ID if it is not in the current choices. A manual ID is unverified; the Catalog checks its tenant, state and scope when you confirm."
        >
          <input value={value} onChange={(e) => onChange(e.target.value)} />
        </Field>
      )}
      <ReadState
        {...page}
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      {!value &&
        (page.error || (page.data && !page.data.items.length)) &&
        nextSteps}
      <button
        type="button"
        disabled={page.busy}
        onClick={() => {
          if (cursor) setCursor("");
          else page.refresh();
        }}
      >
        Refresh {label.toLowerCase()} choices
      </button>
      <Pager
        label={pagerLabel ?? `${label} choices pagination`}
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </div>
  );
}
