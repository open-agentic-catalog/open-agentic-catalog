import { controlIcons } from "../shell/Icon";
import { SkillHandoffEditor, handoffInput } from "./SkillHandoff";
import { atField, InputError } from "./input-validation";
import { parseJSONObject } from "./json-input";
// Representation drafts support bounded local input; backend adapters own authoritative validation.
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import type { components } from "../api/schema";
import type { Kind } from "../api/client";
import { Field } from "./workspace";
import { LocalDocument } from "./LocalDocument";
import { NativeDocumentPreview, NativeStarter } from "./NativeStarter";
import { kindLabel } from "../resource-kinds";

const PluginAuthoring = lazy(() => import("./PluginAuthoring"));
const AgentCardEditor = lazy(() => import("./AgentCardEditor"));
const MCPMetadataEditor = lazy(() => import("./MCPMetadataEditor"));
const SkillEditor = lazy(() => import("./SkillEditor"));
const SkillPackageGuide = lazy(() => import("./SkillPackageGuide"));

type Representation = components["schemas"]["RepresentationInput"];
/** Editor-local input state; editorID is stable across file reads and is never serialized. */
export type RepresentationDraft = {
  editorID: string;
  role: "primary" | "distribution" | "metadata" | "documentation";
  mediaType: string;
  profileId: string;
  mode: "data" | "content" | "url" | "package";
  content: string;
  url: string;
  packageData: string;
  handoff: string;
  expectedDigest?: string;
  documentStaged?: boolean;
  pluginPreparation?: import("./plugin-files").PluginPreparation;
};

/** Choose kind-specific editing defaults and allocate a stable row identity. */
export function newRepresentation(kind: Kind): RepresentationDraft {
  return {
    editorID: crypto.randomUUID(),
    role: "primary",
    mediaType:
      kind === "agent"
        ? "application/a2a-agent-card+json"
        : kind === "mcp_server"
          ? "application/mcp-server-card+json"
          : kind === "plugin"
            ? "application/agent-plugins+zip"
            : "application/agent-skills+md",
    profileId: "",
    mode: kind === "plugin" ? "url" : kind === "skill" ? "content" : "data",
    content: "",
    url: "",
    packageData: "",
    handoff: "",
  };
}

// An asynchronous file read belongs to its editor, not its current row index.
// Removing that editor must not resurrect it or change another representation.
export function replaceRepresentation(
  drafts: RepresentationDraft[],
  editorID: string,
  next: RepresentationDraft,
) {
  return drafts.map((draft) =>
    draft.editorID === editorID ? { ...next, editorID } : draft,
  );
}

/** Preserve row identity and keyboard focus across bounded additions and removals. */
export function RepresentationList({
  kind,
  value,
  onChange,
  version,
  onVersionChange,
}: {
  kind: Kind;
  value: RepresentationDraft[];
  onChange: Dispatch<SetStateAction<RepresentationDraft[]>>;
  version?: string | undefined;
  onVersionChange?: ((value: string) => void) | undefined;
}) {
  const rows = useRef<HTMLDivElement>(null);
  const add = useRef<HTMLButtonElement>(null);
  const focusRow = useRef<string | undefined>(undefined);
  useLayoutEffect(() => {
    const id = focusRow.current;
    if (id === undefined || !value.some((item) => item.editorID === id)) return;
    const control =
      rows.current?.ownerDocument.getElementById(`${id}-source`) ??
      rows.current?.ownerDocument.getElementById(`${id}-plugin-input`);
    if (control && !control.matches(":disabled")) control.focus();
    else add.current?.focus();
    focusRow.current = undefined;
  }, [value]);
  return (
    <div ref={rows}>
      {value.map((item, index) => (
        <RepresentationEditor
          key={item.editorID}
          kind={kind}
          index={index}
          value={item}
          version={version}
          onVersionChange={onVersionChange}
          onChange={(next) =>
            onChange((items) =>
              replaceRepresentation(items, item.editorID, next),
            )
          }
          onPrepareDiscovery={
            kind === "skill" &&
            item.mode === "package" &&
            item.role === "primary" &&
            value.length < 16 &&
            !value.some(
              (row) => row.editorID !== item.editorID && row.role === "primary",
            )
              ? () =>
                  onChange((items) => {
                    const primary = newRepresentation("skill");
                    focusRow.current = primary.editorID;
                    return [
                      ...items.map((row) =>
                        row.editorID === item.editorID
                          ? { ...row, role: "distribution" as const }
                          : row,
                      ),
                      primary,
                    ];
                  })
              : undefined
          }
          onRemove={
            value.length > 1
              ? () =>
                  onChange((items) => {
                    const remaining = items.filter(
                      (row) => row.editorID !== item.editorID,
                    );
                    focusRow.current =
                      remaining[
                        Math.min(index, remaining.length - 1)
                      ]!.editorID;
                    return remaining;
                  })
              : undefined
          }
        />
      ))}
      <button
        ref={add}
        type="button"
        disabled={value.length >= 16}
        onClick={() => {
          const next = {
            ...newRepresentation(kind),
            role: "metadata" as const,
          };
          focusRow.current = next.editorID;
          onChange((items) => [...items, next]);
        }}
      >
        {controlIcons["add"]} Add representation
      </button>
    </div>
  );
}

/** Infer the Skill archive upload media type from its filename; the backend validates bytes. */
export function packageMediaType(filename: string) {
  return /\.(?:tar\.gz|tgz)$/i.test(filename)
    ? "application/agent-skills+gzip"
    : "application/agent-skills+zip";
}

/** Require an object after the editor's shared bounded JSON checks. */
export function parseObject(text: string): Record<string, unknown> {
  return parseJSONObject(text);
}

/** Serialize only the selected representation mode and validate its browser-editable fields. */
export function representationInput(
  draft: RepresentationDraft,
): Representation {
  if (draft.pluginPreparation?.preparing)
    throw new InputError(
      "Package and host the Plugin files, then continue with archive inputs.",
      `${draft.editorID}-plugin-input`,
    );
  if (draft.documentStaged)
    throw new InputError(
      "Use or discard the selected document before saving.",
      `${draft.editorID}-content-file`,
    );
  const common = {
    role: draft.role,
    mediaType: draft.mediaType.trim(),
    ...(draft.profileId.trim() ? { profileId: draft.profileId.trim() } : {}),
  };
  switch (draft.mode) {
    case "data":
      return {
        ...common,
        data: atField(`${draft.editorID}-content`, () =>
          parseObject(draft.content),
        ),
      };
    case "content":
      if (!draft.content.trim())
        throw new InputError(
          "Enter representation content.",
          `${draft.editorID}-content`,
        );
      return { ...common, content: draft.content };
    case "url": {
      const url = atField(`${draft.editorID}-url`, () => new URL(draft.url));
      if (url.protocol !== "https:" || url.username || url.password || url.hash)
        throw new InputError(
          "Use a credential-free HTTPS URL without a fragment.",
          `${draft.editorID}-url`,
        );
      const expectedDigest = draft.expectedDigest?.trim();
      if (expectedDigest && !/^sha256:[a-f0-9]{64}$/.test(expectedDigest))
        throw new InputError(
          "Enter a SHA-256 checksum as sha256: followed by 64 lowercase hexadecimal characters.",
          `${draft.editorID}-checksum`,
        );
      return {
        ...common,
        url: draft.url,
        ...(expectedDigest ? { expectedDigest } : {}),
      };
    }
    case "package":
      if (!draft.packageData)
        throw new InputError(
          "Choose a Skill package.",
          `${draft.editorID}-package`,
        );
      return {
        ...common,
        packageData: draft.packageData,
        handoff: atField(`${draft.editorID}-handoff`, () =>
          handoffInput(draft.handoff, `${draft.editorID}-handoff`),
        ),
      };
  }
}

// The service performs authoritative archive validation; this check only bounds
// browser reads and base64 expansion before a request is prepared.
export async function packageBytes(file: File): Promise<string> {
  if (!file.size || file.size > 16 * 1024 * 1024)
    throw new Error(
      "Choose a non-empty package no larger than 16 MiB. The deployment request limit may be smaller.",
    );
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 8192)
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 8192));
  return btoa(binary);
}

/** Edit one representation and discard file-read results after its component unmounts. */
export function RepresentationEditor({
  kind,
  index,
  value,
  onChange,
  onRemove,
  onPrepareDiscovery,
  version,
  onVersionChange,
}: {
  kind: Kind;
  index: number;
  value: RepresentationDraft;
  onChange: (draft: RepresentationDraft) => void;
  onRemove?: (() => void) | undefined;
  onPrepareDiscovery?: (() => void) | undefined;
  version?: string | undefined;
  onVersionChange?: ((value: string) => void) | undefined;
}) {
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [initial] = useState(value);
  const [staged, setStaged] = useState(false);
  const [documentEpoch, setDocumentEpoch] = useState(0);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [nativeAdvanced, setNativeAdvanced] = useState(false);
  const [agentSigned, setAgentSigned] = useState(false);
  const [documentOrigin, setDocumentOrigin] = useState(0);
  const focusLoadedNative = useRef(false);
  useLayoutEffect(() => {
    if (!focusLoadedNative.current || !nativeAdvanced) return;
    document.getElementById(`${value.editorID}-content`)!.focus();
    focusLoadedNative.current = false;
  }, [nativeAdvanced, value.content, value.editorID]);
  const current = useRef({ value, onChange });
  current.current = { value, onChange };
  const reportStaged = useCallback((next: boolean) => {
    setStaged(next);
    const { value: latest, onChange: change } = current.current;
    if (!!latest.documentStaged === next) return;
    const draft = { ...latest };
    if (next) draft.documentStaged = true;
    else delete draft.documentStaged;
    change(draft);
  }, []);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const update = (patch: Partial<RepresentationDraft>) =>
    onChange({ ...value, ...patch });
  const replaceDocument = (next: RepresentationDraft) => {
    setDocumentOrigin((origin) => origin + 1);
    setStaged(false);
    setDocumentEpoch((epoch) => epoch + 1);
    onChange(next);
  };
  const guidedSkill =
    kind === "skill" &&
    value.mode === "content" &&
    (value.mediaType === "application/agent-skills+md" ||
      value.mediaType === 'text/markdown; profile="urn:air:agent-skills"');
  const documentEditor = (
    <Field
      label={
        value.mode === "data" ? "Standards document (JSON)" : "Text content"
      }
      hint={
        value.mode === "data"
          ? "For exact numbers outside the browser's safe range, choose Anonymous HTTPS document under Content source. The Catalog fetches and validates that document through its outbound security policy."
          : ""
      }
    >
      <textarea
        required
        readOnly={
          kind === "agent" &&
          value.mode === "data" &&
          value.mediaType === "application/a2a-agent-card+json" &&
          agentSigned
        }
        rows={12}
        maxLength={2 * 1024 * 1024}
        spellCheck={false}
        id={`${value.editorID}-content`}
        value={value.content}
        onChange={(event) => {
          setDocumentOrigin((origin) => origin + 1);
          update({ content: event.target.value });
        }}
      />
    </Field>
  );
  const documentImport = (
    <LocalDocument
      key={`${value.editorID}:${value.mode}:${documentEpoch}`}
      editorId={`${value.editorID}-content`}
      content={value.content}
      format={value.mediaType}
      json={value.mode === "data"}
      validate={value.mode === "data" ? parseObject : undefined}
      onLoad={(content) => {
        setDocumentOrigin((origin) => origin + 1);
        if (kind === "agent" || kind === "mcp_server" || guidedSkill) {
          focusLoadedNative.current = true;
          setNativeAdvanced(true);
        }
        const next = { ...value, content };
        delete next.documentStaged;
        onChange(next);
      }}
      onStagedChange={reportStaged}
    />
  );
  return (
    <fieldset
      id={value.editorID}
      className="workspace-panel workspace-fields representation-editor"
      disabled={loading}
    >
      <legend>
        {kindLabel(kind)} content {index + 1}
      </legend>
      <div className="representation-layout">
        <div className="representation-controls workspace-fields">
          <NativeStarter
            kind={kind}
            value={value}
            {...(version !== undefined ? { version } : {})}
            {...(onVersionChange ? { onVersionChange } : {})}
            onUseExample={replaceDocument}
            previewOpen={previewOpen}
            onPreviewChange={setPreviewOpen}
            inlinePreview={false}
          />
          {kind === "skill" && (
            <Suspense
              fallback={<p role="status">Loading Skill package guidance...</p>}
            >
              <SkillPackageGuide
                completePackage={value.mode === "package"}
                onPrepareDiscovery={onPrepareDiscovery}
              />
            </Suspense>
          )}
          {kind !== "plugin" && (
            <Field
              label="Content source"
              hint="Choose how to provide metadata. Remote documents pass the deployment's network policy; Skill files are inspected, never executed."
            >
              <select
                id={`${value.editorID}-source`}
                required
                value={value.mode}
                onChange={(e) => {
                  const mode = e.target.value as RepresentationDraft["mode"];
                  if (mode === value.mode) return;
                  if (
                    (staged ||
                      value.content ||
                      value.url ||
                      value.packageData ||
                      value.handoff ||
                      value.expectedDigest) &&
                    !window.confirm(
                      "Changing content source discards this representation's content, URL, package, handoff and expected digest. Continue?",
                    )
                  )
                    return;
                  setStaged(false);
                  setNativeAdvanced(false);
                  const next = {
                    ...value,
                    mode,
                    content: "",
                    url: "",
                    packageData: "",
                    handoff: "",
                  };
                  delete next.expectedDigest;
                  delete next.documentStaged;
                  if (value.mode === "package") {
                    next.mediaType =
                      mode === "data"
                        ? "application/json"
                        : "application/agent-skills+md";
                    next.profileId = "";
                  }
                  onChange(next);
                }}
              >
                {
                  <option value="data">
                    {kind === "agent" &&
                    value.mediaType === "application/a2a-agent-card+json"
                      ? "Agent fields or JSON"
                      : kind === "mcp_server"
                        ? "MCP fields or JSON"
                        : "JSON document"}
                  </option>
                }
                {
                  <option value="content">
                    {kind === "skill"
                      ? "Write/import SKILL.md"
                      : "Text / Skill Markdown"}
                  </option>
                }
                <option value="url">Anonymous HTTPS document</option>
                {kind === "skill" && (
                  <option value="package">
                    Upload a complete Skill package
                  </option>
                )}
              </select>
            </Field>
          )}
          {kind === "plugin" ? (
            <Suspense
              fallback={<p role="status">Loading Plugin authoring...</p>}
            >
              <PluginAuthoring
                origin={documentOrigin}
                value={value}
                onChange={onChange}
                version={version}
              />
            </Suspense>
          ) : value.mode === "url" ? (
            <>
              <Field
                label="Document URL"
                hint="An allowed URL to the metadata document. Do not embed credentials. This does not install or run the resource."
              >
                <input
                  required
                  type="url"
                  maxLength={2048}
                  id={`${value.editorID}-url`}
                  value={value.url}
                  onChange={(e) => update({ url: e.target.value })}
                />
              </Field>
            </>
          ) : value.mode === "package" ? (
            <>
              <Field label="Skill archive">
                <input
                  id={`${value.editorID}-package`}
                  type="file"
                  required
                  accept=".zip,.tar.gz,.tgz"
                  disabled={loading}
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    update({ packageData: "" });
                    if (!file) return;
                    setLoading(true);
                    setError("");
                    void packageBytes(file)
                      .then((packageData) => {
                        if (alive.current)
                          update({
                            packageData,
                            mediaType: packageMediaType(file.name),
                          });
                      })
                      .catch(() => {
                        if (alive.current)
                          setError(
                            "Could not read this package. Choose a non-empty file up to 16 MiB.",
                          );
                      })
                      .finally(() => {
                        if (alive.current) setLoading(false);
                      });
                  }}
                />
              </Field>
              <SkillHandoffEditor
                id={`${value.editorID}-handoff`}
                value={value.handoff}
                onChange={(handoff) => update({ handoff })}
              />
              {loading && <p role="status">Reading package...</p>}
            </>
          ) : (
            <>
              {kind === "agent" || kind === "mcp_server" || guidedSkill ? (
                <details>
                  <summary>
                    Import an existing{" "}
                    {kind === "agent"
                      ? "Agent"
                      : kind === "skill"
                        ? "Skill"
                        : "MCP"}{" "}
                    document
                  </summary>
                  {documentImport}
                </details>
              ) : (
                documentImport
              )}
              {guidedSkill ||
              (value.mode === "data" &&
                (kind === "mcp_server" ||
                  (kind === "agent" &&
                    value.mediaType === "application/a2a-agent-card+json"))) ? (
                <>
                  <Suspense
                    fallback={
                      <p role="status">
                        Loading{" "}
                        {guidedSkill
                          ? "Skill"
                          : kind === "agent"
                            ? "Agent"
                            : "MCP"}{" "}
                        fields...
                      </p>
                    }
                  >
                    {guidedSkill ? (
                      <SkillEditor
                        origin={documentOrigin}
                        value={value}
                        onChange={onChange}
                        advanced={nativeAdvanced}
                        onAdvanced={setNativeAdvanced}
                      />
                    ) : kind === "agent" ? (
                      <AgentCardEditor
                        value={value}
                        onChange={onChange}
                        version={version}
                        advanced={nativeAdvanced}
                        onAdvanced={() => setNativeAdvanced(true)}
                        onSignedChange={setAgentSigned}
                        origin={documentOrigin}
                      />
                    ) : (
                      <MCPMetadataEditor
                        value={value}
                        onChange={onChange}
                        onReplace={replaceDocument}
                        version={version}
                        origin={documentOrigin}
                        advanced={nativeAdvanced}
                        onAdvanced={setNativeAdvanced}
                      />
                    )}
                  </Suspense>
                  <details
                    open={nativeAdvanced}
                    onToggle={(event) =>
                      setNativeAdvanced(event.currentTarget.open)
                    }
                  >
                    <summary>
                      {guidedSkill
                        ? "Advanced SKILL.md"
                        : kind === "agent"
                          ? "Advanced Agent Card JSON"
                          : "Advanced MCP JSON"}
                    </summary>
                    <p>
                      {guidedSkill
                        ? "Edit YAML frontmatter and Markdown."
                        : kind === "agent"
                          ? "Use JSON for provider details, security declarations, extensions, signatures and other Card fields."
                          : "Use JSON for repository data, URL variables, package arguments, non-secret defaults and extensions."}{" "}
                      Switching views keeps your document.
                    </p>
                    <fieldset
                      disabled={!nativeAdvanced}
                      className="agent-raw-fields"
                    >
                      {documentEditor}
                    </fieldset>
                  </details>
                </>
              ) : (
                documentEditor
              )}
            </>
          )}
          <details>
            <summary>Advanced representation settings</summary>
            <p>
              <strong>
                {kind === "mcp_server"
                  ? "MCP Server"
                  : kind === "agent"
                    ? "Agent"
                    : kind === "skill"
                      ? "Skill"
                      : "Plugin"}
              </strong>
              : {value.role} / {value.mediaType}
            </p>
            <p className="muted">
              {kind === "agent"
                ? "Provide an A2A Agent Card JSON document."
                : kind === "mcp_server"
                  ? "MCP Registry metadata can validate and release locally. Discovery snapshots for either audience need a supported primary MCP Server Card."
                  : kind === "plugin"
                    ? "Provide a ZIP or gzipped tar archive with root plugin.json. Reusing a URL retrieves and validates the archive again; provide its checksum to require unchanged bytes."
                    : "Provide a Skill document, anonymous delivery reference, or archive with an immutable Git/OCI handoff. Retained inspection documents cannot reconstruct the complete archive."}
            </p>
            <Field
              label="Representation role"
              hint="Primary is the main document for this version. Supplementary documents describe the same resource without replacing its primary representation."
            >
              <select
                required
                value={value.role}
                onChange={(e) =>
                  update({
                    role: e.target.value as RepresentationDraft["role"],
                  })
                }
              >
                {["primary", "distribution", "metadata", "documentation"].map(
                  (role) => (
                    <option key={role}>{role}</option>
                  ),
                )}
              </select>
            </Field>
            <Field
              label="Media type"
              hint="Use a supported media type for this resource kind. Keep the default unless your document uses another supported format."
            >
              <input
                required
                maxLength={255}
                value={value.mediaType}
                onChange={(e) => update({ mediaType: e.target.value })}
              />
            </Field>
            <Field
              label="Profile ID (optional)"
              hint="Usually leave this blank. The resource kind and media type select validation; this field does not choose another validator."
            >
              <input
                maxLength={160}
                value={value.profileId}
                onChange={(e) => update({ profileId: e.target.value })}
              />
            </Field>
          </details>
          {error && <p role="alert">{error}</p>}
          {onRemove && (
            <button
              type="button"
              aria-label={`Remove representation ${index + 1}`}
              onClick={() => {
                const changed = Object.entries(value).some(
                  ([field, item]) =>
                    field !== "editorID" &&
                    (field === "expectedDigest"
                      ? item !== (initial.expectedDigest ?? "")
                      : item !== initial[field as keyof RepresentationDraft]),
                );
                if (
                  (changed ||
                    staged ||
                    value.content ||
                    value.url ||
                    value.packageData ||
                    value.handoff ||
                    value.expectedDigest ||
                    value.pluginPreparation) &&
                  !window.confirm(
                    "Remove this representation? Its unsaved input and selected document will be lost.",
                  )
                )
                  return;
                onRemove();
              }}
            >
              {controlIcons["close"]} Remove representation
            </button>
          )}
        </div>
        <NativeDocumentPreview kind={kind} value={value} open={previewOpen} />
      </div>
    </fieldset>
  );
}
