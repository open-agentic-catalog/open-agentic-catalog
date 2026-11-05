// Local files are staged before replacement. A file never sends a Catalog command.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { controlIcons } from "../shell/Icon";
import { Field, useUnsaved } from "./workspace";
import { documentationGuideLink } from "../api/session";
import { kindLabel } from "../resource-kinds";
import {
  checkBrowserDraft,
  maximumDraftFileBytes,
  readDraftFile,
  writeDraftFile,
  type DraftFile,
} from "./draft-file";

export type ListingChoice = "imported" | "existing" | "none";
export function DraftFileActions({
  locked,
  dirty,
  downloadLocked = locked,
  target,
  documentationUrl,
  read,
  apply,
}: {
  locked: boolean;
  dirty: boolean;
  downloadLocked?: boolean;
  target?: { kind: DraftFile["kind"]; canonicalIdentifier: string } | undefined;
  documentationUrl?: string | undefined;
  read: () => DraftFile;
  apply: (file: DraftFile, listing: ListingChoice) => void;
}) {
  const [candidate, setCandidate] = useState<DraftFile>();
  const guide = documentationGuideLink(
    documentationUrl ?? "",
    "draft-transfer",
  );
  const [choice, setChoice] = useState<ListingChoice>("none");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [download, setDownload] = useState<{ href: string }>();
  const generation = useRef(0);
  const href = useRef("");
  const notice = useRef<HTMLParagraphElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const tools = useRef<HTMLDetailsElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useUnsaved(busy || !!candidate);
  const preview = useRef<HTMLElement>(null);
  const clearDownload = () => {
    if (href.current) URL.revokeObjectURL(href.current);
    href.current = "";
    setDownload(undefined);
  };
  useLayoutEffect(
    () => () => {
      generation.current++;
      if (href.current) URL.revokeObjectURL(href.current);
    },
    [],
  );
  useLayoutEffect(() => {
    if (locked) {
      generation.current++;
      setCandidate(undefined);
      setBusy(false);
    }
  }, [locked]);
  useLayoutEffect(() => {
    if (downloadLocked) {
      if (href.current) URL.revokeObjectURL(href.current);
      href.current = "";
      setDownload(undefined);
    }
  }, [downloadLocked]);
  useEffect(() => {
    if (error || message) notice.current?.focus();
  }, [error, message]);
  useEffect(() => {
    if (candidate) preview.current?.focus();
  }, [candidate]);
  return (
    <details ref={tools}>
      <summary>Import or download draft JSON</summary>
      <section className="workspace-panel" aria-label="Reusable draft JSON">
        <div className="workspace-actions document-downloads">
          <button
            type="button"
            ref={trigger}
            disabled={locked}
            onClick={() => picker.current?.click()}
          >
            {controlIcons.add} Import draft JSON
          </button>
          <input
            ref={picker}
            type="file"
            accept=".json,application/json"
            aria-label="Choose draft JSON file"
            hidden
            disabled={locked}
            onChange={(event) => {
              const file = event.target.files?.[0];
              event.target.value = "";
              if (!file || locked) return;
              tools.current!.open = true;
              const attempt = ++generation.current;
              setBusy(true);
              setError("");
              setMessage("");
              setCandidate(undefined);
              clearDownload();
              void (async () => {
                try {
                  if (file.size > maximumDraftFileBytes)
                    throw new Error(
                      "Draft JSON exceeds the 64 MiB file limit.",
                    );
                  const bytes = new Uint8Array(await file.arrayBuffer());
                  if (attempt !== generation.current) return;
                  const value = readDraftFile(bytes);
                  try {
                    checkBrowserDraft(value);
                  } catch {
                    throw new Error(
                      "This draft exceeds a browser editor limit. Each document and the Listing extension text must fit within 2 MiB. Use the CLI for larger files.",
                    );
                  }
                  if (
                    target &&
                    (value.kind !== target.kind ||
                      value.canonicalIdentifier !== target.canonicalIdentifier)
                  )
                    throw new Error(
                      "This file has a different Resource kind or canonical identifier. Start a new Resource to import it.",
                    );
                  setChoice(value.listing ? "imported" : "none");
                  setCandidate(value);
                } catch (cause) {
                  if (attempt === generation.current)
                    setError((cause as Error).message);
                } finally {
                  if (attempt === generation.current) setBusy(false);
                }
              })();
            }}
          />
          <button
            type="button"
            disabled={downloadLocked || busy}
            onClick={() => {
              setError("");
              setMessage("");
              clearDownload();
              try {
                const content = writeDraftFile(read());
                href.current = URL.createObjectURL(
                  new Blob([content], { type: "application/json" }),
                );
                setDownload({ href: href.current });
                setMessage(
                  "Draft JSON is ready. It contains the editor values captured when you selected Download.",
                );
              } catch (cause) {
                setError((cause as Error).message);
              }
            }}
          >
            {controlIcons.download} Download draft JSON
          </button>
          {download && (
            <a
              className="button"
              href={download.href}
              download="catalog-draft.json"
            >
              {controlIcons.download} Save catalog-draft.json
            </a>
          )}
        </div>
        <details>
          <summary>About draft files</summary>
          <p>
            Move one Resource and one Version, with optional Listing metadata,
            between Catalogs. Select a destination Namespace and Publisher
            before saving a Listing. Review and publication stay separate.
          </p>
          {guide && (
            <a href={guide.href} target="_blank" rel="noopener noreferrer">
              {guide.label} (opens in a new tab)
            </a>
          )}
        </details>
        {locked && (
          <p>
            Start a fresh authoring flow, or choose Create another version after
            saving, to import a file.
          </p>
        )}
        {busy && <p role="status">Read draft JSON...</p>}
        {(error || message) && (
          <p ref={notice} tabIndex={-1} role={error ? "alert" : "status"}>
            {error || message}
          </p>
        )}
        {candidate && !locked && (
          <section
            ref={preview}
            tabIndex={-1}
            aria-label="Draft import preview"
            className="workspace-panel"
          >
            <h2>Import preview</h2>
            <dl className="workspace-kv">
              <dt>Kind</dt>
              <dd>{kindLabel(candidate.kind)}</dd>
              <dt>Canonical identifier</dt>
              <dd>{candidate.canonicalIdentifier || "Not entered"}</dd>
              <dt>Version</dt>
              <dd>{candidate.version || "Not entered"}</dd>
              <dt>Representations</dt>
              <dd>{candidate.representations.length}</dd>
            </dl>
            {candidate.representations.some(
              (row) => row.input.mode === "skill-package",
            ) && (
              <p>
                Supply the Skill package separately before saving. The JSON file
                contains no package bytes.
              </p>
            )}
            {candidate.representations.some(
              (row) => row.input.mode === "plugin-preparation",
            ) && (
              <p>
                Plugin preparation stays editable. Build and host its archive
                before saving.
              </p>
            )}
            <Field label="Listing metadata to use">
              <select
                value={choice}
                onChange={(event) =>
                  setChoice(event.target.value as ListingChoice)
                }
              >
                <option value="none">Content only</option>
                {candidate.listing && (
                  <option value="imported">
                    Imported Listing metadata, select a destination Publisher
                  </option>
                )}
                {target && (
                  <option value="existing">
                    Existing target Listing metadata
                  </option>
                )}
              </select>
            </Field>
            <div className="workspace-actions">
              <button
                type="button"
                onClick={() => {
                  if (
                    dirty &&
                    !window.confirm(
                      "Replace the unsaved authoring fields and staged documents with this draft file?",
                    )
                  )
                    return;
                  try {
                    apply(candidate, choice);
                    setCandidate(undefined);
                    setMessage(
                      "Draft imported into the editor. Check its fields before saving.",
                    );
                    clearDownload();
                  } catch (cause) {
                    setError((cause as Error).message);
                  }
                }}
              >
                {controlIcons.edit} Use this draft
              </button>
              <button
                type="button"
                onClick={() => {
                  generation.current++;
                  setCandidate(undefined);
                  trigger.current?.focus();
                }}
              >
                Cancel import
              </button>
            </div>
          </section>
        )}
      </section>
    </details>
  );
}
