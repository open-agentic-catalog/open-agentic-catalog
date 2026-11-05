import { Facts } from "./Facts";
// Shared screen primitives enforce keyed reads, deliberate commands and dirty-editor handling.
import {
  Fragment,
  cloneElement,
  createContext,
  isValidElement,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { ReactNode, SyntheticEvent } from "react";
import { APIError } from "../api/client";
import type { Access, Client, Runtime } from "../api/client";
import { prepareCommand, uncertain } from "../api/commands";
import { Icon, controlIcons } from "../shell/Icon";
import type { IconName } from "../shell/Icon";
import "./workspace.css";
import { useOperationContext } from "./operation-context";
import { useExpired } from "./use-expired";
import type { CommandOutcome } from "./audit-handoff";

/** Context passed by the shell to routed workflow screens. */
export type WorkspaceProps = {
  client: Client;
  runtime: Runtime;
  access: Access;
  /** Route segment: empty for a list, otherwise an ID or screen-owned action. */
  id: string;
  /** Report request failures to the shell for session/access reconciliation. */
  onFailure: (error: unknown) => void;
};

/** Keep confirmation rows compact without changing exact labels, values or order. */
export function commandSummary(rows: readonly (readonly [string, string])[]) {
  return rows.map(([label, value]) => ({ label, value }));
}

/** Load on key changes or explicit refresh, discarding obsolete results.
 * The caller includes all request-changing inputs in key and bounds its API
 * query. Changing callback identity alone must not refetch private evidence. */
export function useRead<T>(
  key: string,
  load: (signal: AbortSignal) => Promise<T>,
  onFailure: (e: unknown) => void,
  retainDataOnRefresh = false,
) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState("");
  const [problem, setProblem] = useState<APIError>();
  const [busy, setBusy] = useState(true);
  const [revision, setRevision] = useState(0);
  // Keep current callbacks without making inline functions effect dependencies.
  const previousKey = useRef(key);
  const loader = useRef(load);
  loader.current = load;
  const failure = useRef(onFailure);
  failure.current = onFailure;
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    if (!retainDataOnRefresh || previousKey.current !== key) setData(undefined);
    previousKey.current = key;
    setError("");
    setProblem(undefined);
    setBusy(true);
    loader
      .current(controller.signal)
      .then((value) => {
        if (current) setData(value);
      })
      .catch((e) => {
        if (current) {
          setProblem(e instanceof APIError ? e : undefined);
          setError(
            e instanceof APIError
              ? e.message
              : "This page could not be loaded.",
          );
          failure.current(e);
        }
      })
      .finally(() => {
        if (current) setBusy(false);
      });
    return () => {
      // Abort saves work; the current flag also fences loaders that ignore it.
      current = false;
      controller.abort();
    };
  }, [key, revision, retainDataOnRefresh]);
  return {
    data,
    error,
    problem,
    busy,
    refresh: () => setRevision((v) => v + 1),
  };
}

/** Render the shared page heading and its optional workflow actions. */
export function Heading({
  title,
  description,
  icon,
  help,
  children,
}: {
  title: string;
  description: string;
  icon: IconName;
  help?: ReactNode;
  children?: ReactNode;
}) {
  const titleNode = (
    <h1 className="workspace-title">
      <Icon name={icon} />
      {title}
    </h1>
  );
  return (
    <div className="page-heading">
      <div>
        {help ? (
          <div className="workspace-heading-title">
            {titleNode}
            {help}
          </div>
        ) : (
          titleNode
        )}
        {description && <p className="muted">{description}</p>}
      </div>
      {children}
    </div>
  );
}

/** Present a read's busy/error state and leave retries under explicit user control. */
export function ReadState({
  busy,
  error,
  refresh,
  problem,
}: {
  problem?: APIError | undefined;
  busy: boolean;
  error: string;
  refresh: () => void;
}) {
  return (
    <>
      {busy && (
        <p role="status" className="muted">
          Loading...
        </p>
      )}
      {error && (
        <div className="notice error" role="alert">
          <p>{error}</p>
          {problem && (problem.code || problem.requestId) && (
            <details>
              <summary>Request details</summary>
              <Facts
                rows={[
                  ...(problem.code
                    ? [["Service code", problem.code] as const]
                    : []),
                  ...(problem.requestId
                    ? [["Request ID", problem.requestId] as const]
                    : []),
                ]}
              />
            </details>
          )}
          <button type="button" onClick={refresh}>
            {controlIcons["refresh"]}
            Reload
          </button>
        </div>
      )}
    </>
  );
}

/** Navigate using opaque server cursors without inferring totals or page numbers. */
export function Pager({
  label = "Pagination",
  cursor,
  nextCursor,
  onPage,
  busy = false,
}: {
  label?: string;
  cursor: string;
  nextCursor?: string | undefined;
  onPage: (cursor: string) => void | boolean;
  busy?: boolean;
}) {
  const focus = useRef<HTMLDivElement>(null);
  const changePage = (value: string) => {
    if (onPage(value) === false) return;
    // The activated button may disappear after paging; keep keyboard focus in
    // the persistent recovery controls instead of dropping it to the document.
    focus.current?.focus();
  };
  return (
    <div
      className="pagination"
      ref={focus}
      tabIndex={-1}
      role="group"
      aria-label={label}
    >
      {cursor && (
        <button type="button" disabled={busy} onClick={() => changePage("")}>
          {controlIcons["first"]}
          First page
        </button>
      )}
      {nextCursor && (
        <button
          type="button"
          disabled={busy}
          onClick={() => changePage(nextCursor)}
        >
          {controlIcons["arrow"]}
          Next page
        </button>
      )}
    </div>
  );
}

/** Register a dirty editor with native unload and in-page discard checks. */
export function useUnsaved(dirty: boolean) {
  // Navigation must see the editor state committed with the current controls.
  useLayoutEffect(() => {
    if (!dirty) return;
    const unload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    const leave = (e: Event) => e.preventDefault();
    window.addEventListener("beforeunload", unload);
    window.addEventListener("catalog-ui:leave", leave);
    return () => {
      window.removeEventListener("beforeunload", unload);
      window.removeEventListener("catalog-ui:leave", leave);
    };
  }, [dirty]);
}

// In-page editor dismissal does not trigger beforeunload like a normal link.
export function confirmDiscard() {
  return (
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })) ||
    window.confirm(
      "Discard unsaved input or an unresolved command? A request already sent may still complete.",
    )
  );
}

/** Presentation intent never changes command admission or request semantics. */
export type ActionIntent = "primary" | "secondary" | "warning" | "danger";

/** A prepared user action retains its exact request inputs across confirmation and retries. */
export type Command = {
  title: string;
  description: string;
  /** Capture presentation with the request; never infer it from the title. */
  intent?: ActionIntent;
  icon?: IconName;
  /** Workflow-owned, non-secret facts captured alongside the send closure. */
  summary?: readonly { label: string; value: string }[];
  /** Inert workflow-owned review content captured with the prepared request. */
  details?: ReactNode;
  /** A new send needs current local evidence; uncertain exact-key recovery remains available. */
  expiresAt?: string;
  /** Capture target, body and ETag when preparing, not from later form edits. */
  run: (key: string) => Promise<string | CommandOutcome>;
};

/** A deliberate confirmation boundary. Unknown outcomes retain the command
 * and offer only an exact-key retry, never an automatic replacement request. */
export function CommandDialog({
  command: initialCommand,
  allowed = true,
  onClose,
  onDone,
  onFailure,
}: {
  command: Command;
  /** Reactive readiness affects new sends; uncertain exact-command recovery remains available. */
  allowed?: boolean;
  onClose: () => void;
  onDone: (message: string) => void;
  onFailure: (e: unknown) => void;
}) {
  // Facts and execution belong to one prepared lifetime, even if the parent rerenders.
  const context = useOperationContext();
  const [command] = useState(() => ({
    ...initialCommand,
    summary: context
      ? [
          ...commandSummary([
            ["Current tenant ID", context.tenantId],
            ["Operator realm ID", context.realmId],
            ["Operation scope", context.scope],
          ]),
          ...(initialCommand.summary ?? []),
        ]
      : initialCommand.summary,
  }));
  const dialog = useRef<HTMLDialogElement>(null);
  // Capture during render, before the same commit disables the originating form.
  const returnFocus = useRef(document.activeElement);
  const mounted = useRef(true);
  const [send] = useState(() => prepareCommand(command.run));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{
    text: string;
    uncertain: boolean;
    conflict: boolean;
  }>();
  const expired = useExpired(command.expiresAt);
  useUnsaved(busy || !!failure?.uncertain);
  useEffect(() => {
    const previous = returnFocus.current;
    mounted.current = true;
    dialog.current?.showModal();
    return () => {
      mounted.current = false;
      if (previous instanceof HTMLElement && previous.isConnected)
        previous.focus();
    };
  }, []);
  const execute = async () => {
    if (busy || (!allowed && !failure?.uncertain)) return;
    if (
      !failure?.uncertain &&
      command.expiresAt &&
      !(Date.parse(command.expiresAt) > Date.now())
    )
      return;
    setBusy(true);
    setFailure(undefined);
    try {
      const outcome = await send();
      if (mounted.current) {
        if (typeof outcome !== "string") context?.acknowledge(outcome.receipt);
        onDone(typeof outcome === "string" ? outcome : outcome.message);
      }
    } catch (e) {
      if (mounted.current) {
        setFailure({
          text:
            e instanceof APIError
              ? `${e.message}${e.code ? ` (${e.code})` : ""}${e.requestId ? ` Request: ${e.requestId}` : ""}`
              : "The response was not received.",
          uncertain: uncertain(e),
          conflict: e instanceof APIError && e.status === 412,
        });
        onFailure(e);
      }
    } finally {
      if (mounted.current) setBusy(false);
    }
  };
  const cancel = useCallback(
    (event: SyntheticEvent | KeyboardEvent) => {
      event.preventDefault();
      event.stopPropagation();
      if (!busy && !failure?.uncertain) onClose();
    },
    [busy, failure?.uncertain, onClose],
  );
  useLayoutEffect(() => {
    // Disabling Confirm can move focus to body. Capture Escape before native
    // close requests even when the event no longer targets this dialog.
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") cancel(event);
    };
    window.addEventListener("keydown", escape, true);
    return () => window.removeEventListener("keydown", escape, true);
  }, [cancel]);
  return (
    <dialog
      ref={dialog}
      className="command-dialog"
      aria-labelledby="command-title"
      closedby={busy || failure?.uncertain ? "none" : "closerequest"}
      onCancel={cancel}
    >
      <h2 id="command-title">{command.title}</h2>
      <p>{command.description}</p>
      {command.summary && (
        <dl className="metadata-grid" aria-label="Command summary">
          {command.summary.map(({ label, value }) => (
            <Fragment key={label}>
              <dt>{label}</dt>
              <dd style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
                {value}
              </dd>
            </Fragment>
          ))}
        </dl>
      )}
      {command.details}
      {!allowed && !failure?.uncertain && (
        <p className="notice">
          Access or prerequisites changed. Return to the form to review your
          input before submitting.
        </p>
      )}
      {expired && !failure?.uncertain && (
        <p role="status">
          This evidence has expired locally. Return to the form and obtain fresh
          inspection or preview evidence before a new command. The API checks
          its own clock and current policy.
        </p>
      )}
      {failure && (
        <div role="alert" className="notice error">
          <p>{failure.text}</p>
          {failure.conflict && (
            <p>
              The record changed. Your input is retained. Return to the form to
              copy it, then reload and compare the current revision before
              preparing a new command.
            </p>
          )}
          {failure.uncertain && (
            <p>
              The outcome is unknown. Retry the same command to recover its
              result. Reloading or leaving loses this retry; do not repeat it as
              a new action without checking the record.
            </p>
          )}
        </div>
      )}
      <div className="form-actions">
        <button disabled={busy || failure?.uncertain} onClick={onClose}>
          {failure ? "Back to form" : "Cancel"}
        </button>
        {(!failure || failure.uncertain) && (
          <button
            className={command.intent ?? "primary"}
            disabled={busy || ((!allowed || expired) && !failure?.uncertain)}
            onClick={() => void execute()}
          >
            {command.icon && controlIcons[command.icon]}
            {busy
              ? "Submitting..."
              : failure
                ? "Retry same command"
                : "Confirm"}
          </button>
        )}
      </div>
    </dialog>
  );
}

/** Keep denied routes oriented without disclosing a hidden target. */
export function PermissionDenied({
  title,
  runtime,
  description = "You do not have access to this page.",
}: {
  title: string;
  runtime: Runtime;
  description?: string;
}) {
  return (
    <div role="alert">
      <Heading title={title} icon="access" description={description} />
      <p>
        <a href={`${runtime.basePath}/access`}>View my access</a>
      </p>
    </div>
  );
}

export const FieldValidationContext = createContext<
  { fieldId: string; message: string } | undefined
>(undefined);

/** Wrap a control in its label and associate errors, hints and help through stable IDs. */
const RequiredMarkerContext = createContext(false);

/** Enable label markers only where the enclosing form explains their meaning. */
export function RequiredFields({
  children,
  legend = true,
}: {
  children: ReactNode;
  legend?: boolean;
}) {
  return (
    <RequiredMarkerContext value={true}>
      {legend && (
        <p className="required-legend">
          <span aria-hidden="true">*</span> Required
        </p>
      )}
      {children}
    </RequiredMarkerContext>
  );
}

export function Field({
  label,
  children,
  hint,
  help,
  error: suppliedError,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
  help?: string;
  error?: string;
}) {
  const id = useId();
  const markers = useContext(RequiredMarkerContext);
  const required =
    isValidElement<Record<string, unknown>>(children) &&
    children.props.required === true;
  const failure = useContext(FieldValidationContext);
  const controlId = isValidElement<{ id?: string }>(children)
    ? children.props.id
    : undefined;
  const error =
    suppliedError ||
    (controlId && failure?.fieldId === controlId ? failure.message : "");
  const errorId = `${isValidElement<{ id?: string }>(children) && children.props.id ? children.props.id : id}-error`;
  const [expanded, setExpanded] = useState(false);
  // Keep help outside the label so it does not change the control's name.
  const control =
    isValidElement<Record<string, unknown>>(children) &&
    typeof children.type === "string"
      ? cloneElement(children, {
          // Keep the name independent of the decorative required marker.
          "aria-label":
            children.props["aria-label"] ??
            (markers && required ? label : undefined),
          "aria-invalid": error ? true : children.props["aria-invalid"],
          "aria-describedby":
            [
              ...new Set(
                [
                  children.props["aria-describedby"],
                  error && errorId,
                  hint && `${id}-hint`,
                  help && expanded && `${id}-help`,
                ]
                  .filter(Boolean)
                  .join(" ")
                  .split(/\s+/),
              ),
            ].join(" ") || undefined,
        })
      : children;
  return (
    <div className="workspace-field">
      <label className="workspace-field-control">
        <span>
          {label}
          {markers && required && (
            <span className="required-marker" aria-hidden="true">
              {" "}
              *
            </span>
          )}
        </span>
        {control}
      </label>
      <div className="workspace-field-notes">
        {/* Keep blur validation from moving the control being clicked below it. */}
        {!error && markers && required && suppliedError !== undefined && (
          <p className="field-error-placeholder" aria-hidden="true" />
        )}
        {error && (
          <p className="field-error" id={errorId}>
            {error}
          </p>
        )}
        {!markers && required && !error && !label.includes("(required)") && (
          <small className="field-hint">Required</small>
        )}

        {hint && (
          <small className="field-hint" id={`${id}-hint`}>
            {hint}
          </small>
        )}
        {help && (
          <>
            <button
              type="button"
              className="field-help-toggle"
              aria-expanded={expanded}
              aria-controls={`${id}-help`}
              title={`More about ${label.toLowerCase()}`}
              onClick={() => setExpanded(!expanded)}
            >
              About {label.toLowerCase()}
            </button>
            <p className="field-help" id={`${id}-help`} hidden={!expanded}>
              {help}
            </p>
          </>
        )}
      </div>
    </div>
  );
}

/** Collect the bounded audit reason required by explicit governance commands. */
export function Reason({
  value,
  onChange,
  label = "Reason",
  required = true,
  readOnly = false,
  id,
}: {
  value: string;
  onChange: (value: string) => void;
  label?: string;
  required?: boolean;
  readOnly?: boolean;
  id?: string;
}) {
  const control = useRef<HTMLTextAreaElement>(null);
  const length = [...value].length;
  useEffect(() => {
    control.current!.setCustomValidity(
      length > 2000 ? "Use no more than 2000 characters for the reason." : "",
    );
  }, [length]);
  return (
    <Field
      label={label}
      hint={`Briefly explain the purpose of this action. This is saved in the audit history; do not include passwords, tokens or private document content. ${length} / 2000 characters.`}
    >
      <textarea
        id={id}
        required={required}
        readOnly={readOnly}
        ref={control}
        maxLength={4000}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        rows={3}
      />
    </Field>
  );
}
