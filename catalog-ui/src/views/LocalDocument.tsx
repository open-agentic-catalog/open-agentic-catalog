import { controlIcons } from "../shell/Icon";
// Local document selection stages bounded text without saving or fetching external content.
import { useEffect, useRef, useState } from "react";
import { Field, useUnsaved } from "./workspace";
import { DocumentInputError } from "./input-validation";
import { nativeFormatLabel } from "./native-starters";

export const documentFileLimit = 2 * 1024 * 1024;

/** Decode non-empty UTF-8 input within the existing browser editor's byte budget. */
export async function documentText(file: File): Promise<string> {
  if (!file.size || file.size > documentFileLimit)
    throw new DocumentInputError("size");
  const bytes = await file.arrayBuffer();
  if (bytes.byteLength > documentFileLimit)
    throw new DocumentInputError("size");
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw new DocumentInputError("encoding");
  }
  if (!text.trim() || text.includes("\0")) throw new DocumentInputError("text");
  return text;
}

/** Stage a local file, then replace the current editor only after an explicit action. */
export function LocalDocument({
  content,
  format,
  json,
  validate,
  onLoad,
  onStagedChange,
  editorId,
}: {
  editorId?: string;
  content: string;
  format: string;
  json: boolean;
  validate?: ((text: string) => unknown) | undefined;
  onLoad: (text: string) => void;
  onStagedChange?: ((staged: boolean) => void) | undefined;
}) {
  const [candidate, setCandidate] = useState<{ name: string; text: string }>();
  const [error, setError] = useState("");
  const [loadedName, setLoadedName] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const chooser = useRef<HTMLInputElement>(null);
  const selectedFile = useRef<File | undefined>(undefined);
  useEffect(() => {
    const control = chooser.current!;
    const restoreSelection = () => {
      if (!selectedFile.current || typeof DataTransfer === "undefined") return;
      const transfer = new DataTransfer();
      transfer.items.add(selectedFile.current);
      control.files = transfer.files;
    };
    control.addEventListener("cancel", restoreSelection);
    return () => control.removeEventListener("cancel", restoreSelection);
  }, []);
  useUnsaved(busy || !!candidate);
  useEffect(() => {
    onStagedChange?.(busy || !!candidate);
  }, [busy, candidate, onStagedChange]);
  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );
  return (
    <div className="document-import">
      <Field
        label="Document file"
        hint={`Choose a UTF-8 ${json ? "JSON" : "Markdown"} file up to 2 MiB. Select Use loaded document to apply it. Deployment request limits may be smaller.`}
      >
        <input
          ref={chooser}
          id={editorId ? `${editorId}-file` : undefined}
          type="file"
          accept={
            json ? ".json,application/json" : ".md,.markdown,text/markdown"
          }
          onClick={(event) => {
            // Reselecting the same path must reread its current bytes.
            event.currentTarget.value = "";
          }}
          onChange={(event) => {
            const file = event.target.files?.[0];
            selectedFile.current = file;
            const current = ++generation.current;
            setCandidate(undefined);
            setError("");
            setBusy(false);
            if (!file) return;
            setBusy(true);
            void documentText(file)
              .then((text) => {
                if (current !== generation.current) return;
                validate?.(text);
                setCandidate({ name: file.name, text });
              })
              .catch((cause: unknown) => {
                if (current === generation.current) {
                  selectedFile.current = undefined;
                  chooser.current!.value = "";
                  setError(
                    cause instanceof DocumentInputError
                      ? cause.message
                      : "Could not read this document. Choose another file and try again.",
                  );
                }
              })
              .finally(() => {
                if (current === generation.current) setBusy(false);
              });
          }}
        />
      </Field>
      <p className="muted">
        Format: {nativeFormatLabel(format)} ({json ? "JSON" : "Markdown"}).
      </p>
      {busy && <p role="status">Reading document...</p>}
      {error && <p role="alert">{error}</p>}
      {candidate && (
        <>
          <p>
            Ready to load: <code>{candidate.name}</code>
          </p>
          <button
            className="document-apply"
            type="button"
            onClick={() => {
              if (
                content &&
                content !== candidate.text &&
                !window.confirm(
                  "Replace the current document text? Unsaved text in this editor will be lost.",
                )
              )
                return;
              onLoad(candidate.text);
              setLoadedName(candidate.name);
              setCandidate(undefined);
              selectedFile.current = undefined;
              chooser.current!.value = "";
              if (editorId) document.getElementById(editorId)?.focus();
            }}
          >
            {controlIcons["edit"]} Use loaded document
          </button>
        </>
      )}
      {(busy || candidate) && (
        <button
          className="document-discard"
          type="button"
          onClick={() => {
            generation.current++;
            setBusy(false);
            setCandidate(undefined);
            setError("");
            selectedFile.current = undefined;
            chooser.current!.value = "";
            if (editorId) document.getElementById(editorId)?.focus();
          }}
        >
          {controlIcons.close} Discard selected document
        </button>
      )}
      {loadedName && (
        <p>
          Loaded <code>{loadedName}</code> into the editor. Review the text
          before saving.
        </p>
      )}
    </div>
  );
}
