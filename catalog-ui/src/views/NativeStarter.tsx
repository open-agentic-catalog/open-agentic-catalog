// Examples and previews share the parent's controlled draft and never submit or retrieve content.
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { controlIcons } from "../shell/Icon";
import type { Kind } from "../api/client";
import { CodeBlock } from "./CodeBlock";
import { Field } from "./workspace";
import type { RepresentationDraft } from "./representations";
import {
  applyNativeStarter,
  nativeDocument,
  nativeStarters,
} from "./native-starters";

/** Downloads preserve current text. Advanced editors can omit the duplicate preview. */
export function LocalPreview({
  content,
  filename,
  preview = true,
  actions,
}: {
  content: string;
  filename: string;
  preview?: boolean;
  actions?: ReactNode;
}) {
  const [download, setDownload] = useState<{ content: string; href: string }>();
  useEffect(() => {
    const href = URL.createObjectURL(
      new Blob([content], { type: "text/plain;charset=utf-8" }),
    );
    setDownload({ content, href });
    return () => URL.revokeObjectURL(href);
  }, [content]);
  return (
    <div>
      {preview && <CodeBlock text={content} original rawText />}
      <div className="workspace-actions document-downloads">
        {download?.content === content && (
          <a className="button" href={download.href} download={filename}>
            {controlIcons.download} Download {filename}
          </a>
        )}
        {actions}
      </div>
    </div>
  );
}

/** Display the current local draft only; URL and package modes expose no document preview. */
export function NativeDocumentPreview({
  kind,
  value,
  open,
}: {
  kind: Kind;
  value: RepresentationDraft;
  open: boolean;
}) {
  const document = open ? nativeDocument(kind, value) : undefined;
  const panel = useRef<HTMLElement>(null);
  const hasDocument = !!document;
  useEffect(() => {
    if (
      open &&
      hasDocument &&
      window.matchMedia("(max-width: 1119px)").matches
    ) {
      panel.current?.focus();
      panel.current?.scrollIntoView({ block: "start" });
    }
  }, [open, hasDocument, value.editorID]);
  return document ? (
    <section
      className="representation-preview"
      aria-label="Current document preview"
      id={`${value.editorID}-preview`}
      tabIndex={-1}
      ref={panel}
    >
      <h3>Document preview</h3>
      <p className="preview-filename">
        <code>{document.filename}</code>
      </p>
      <p>
        This shows the current editor content. Saving inline JSON can change
        formatting and number notation. The Catalog validates content when you
        save.
      </p>
      <LocalPreview
        content={document.text}
        filename={document.filename}
        actions={
          <button
            type="button"
            onClick={() => {
              const target = globalThis.document.getElementById(
                `${value.editorID}-preview-toggle`,
              );
              target?.focus();
              target?.scrollIntoView({ block: "center" });
            }}
          >
            <span className="icon-back">{controlIcons.arrow}</span> Back to
            editor
          </button>
        }
      />
    </section>
  ) : null;
}

export function NativeStarter({
  kind,
  value,
  onUseExample,
  version,
  onVersionChange,
  previewOpen,
  onPreviewChange,
  inlinePreview = true,
}: {
  kind: Kind;
  value: RepresentationDraft;
  onUseExample: (next: RepresentationDraft) => void;
  version?: string | undefined;
  onVersionChange?: ((next: string) => void) | undefined;
  previewOpen?: boolean | undefined;
  onPreviewChange?: ((open: boolean) => void) | undefined;
  inlinePreview?: boolean;
}) {
  const starters = nativeStarters(kind);
  const [selected, setSelected] = useState("");
  const [localPreview, setLocalPreview] = useState(false);
  const preview = previewOpen ?? localPreview;
  const starter =
    starters.find((item) => item.id === selected) ??
    starters.find((item) => item.mediaType === value.mediaType) ??
    starters[0];
  const document = nativeDocument(kind, value);
  const suggestVersion = (next: string | undefined) => {
    if (!next || !onVersionChange || next === version) return;
    if (
      version &&
      !window.confirm(
        `Replace Version label ${version} with the document version ${next}? Protocol and package versions stay unchanged.`,
      )
    )
      return;
    onVersionChange(next);
  };
  return kind === "plugin" ? null : (
    <section
      className="native-starter"
      aria-label="Local examples and document preview"
    >
      {starter && (
        <details>
          <summary>Use a local example</summary>
          <Field label="Native example">
            <select
              value={starter.id}
              onChange={(event) => setSelected(event.target.value)}
            >
              {starters.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label}
                </option>
              ))}
            </select>
          </Field>
          <p>{starter.guidance}</p>
          <p>
            Examples replace this content format only. They stay in the browser
            until you save and never contact advertised endpoints.
          </p>
          <button
            type="button"
            onClick={() => {
              if (
                (value.content ||
                  value.url ||
                  value.packageData ||
                  value.handoff ||
                  value.expectedDigest ||
                  value.documentStaged) &&
                !window.confirm(
                  "Replace this representation's current content and selected document with the example? Other representation rows stay unchanged.",
                )
              )
                return;
              const next = applyNativeStarter(value, starter);
              onUseExample(next);
              suggestVersion(nativeDocument(kind, next)?.version);
              documentForFocus(value.editorID)?.focus();
            }}
          >
            {controlIcons.edit} Use example
          </button>
        </details>
      )}
      {document && (
        <>
          {document.guidance && (
            <p className="notice warning">{document.guidance}</p>
          )}
          {document.version &&
            onVersionChange &&
            document.version !== version && (
              <button
                type="button"
                onClick={() => suggestVersion(document.version)}
              >
                Use document version {document.version} as Version label
              </button>
            )}
          <button
            type="button"
            id={`${value.editorID}-preview-toggle`}
            aria-controls={preview ? `${value.editorID}-preview` : undefined}
            aria-expanded={preview}
            onClick={() => {
              if (previewOpen === undefined) setLocalPreview(!preview);
              onPreviewChange?.(!preview);
            }}
          >
            {controlIcons.eye}
            {preview
              ? "Hide document preview"
              : "Preview and download document"}
          </button>
          {inlinePreview && (
            <NativeDocumentPreview kind={kind} value={value} open={preview} />
          )}
        </>
      )}
    </section>
  );
}

function documentForFocus(editorID: string) {
  return globalThis.document.getElementById(`${editorID}-content`);
}
