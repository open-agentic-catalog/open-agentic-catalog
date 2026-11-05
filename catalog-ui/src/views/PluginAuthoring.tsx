import { controlIcons } from "../shell/Icon";
import {
  lazy,
  Suspense,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { RepresentationDraft } from "./representations";
import { Field } from "./workspace";
import { reportInvalidFields } from "./authoring";
import { LocalPreview } from "./NativeStarter";
import { CodeBlock } from "./CodeBlock";
import { pluginStarterFiles } from "./plugin-starters";
import {
  jsonText,
  newPluginPreparation,
  pluginFiles,
  pluginManifest,
  pluginNameError,
  pluginEndpointError,
  pluginMCP,
  pluginPackaging,
  pluginServerError,
  type PluginPreparation,
} from "./plugin-files";

const SkillEditor = lazy(() => import("./SkillEditor"));
// Integer-like map keys must not move the field that an author is typing in.
function ordered<T>(value: Record<string, T>, order: string[]) {
  return Object.entries(value).sort(([a], [b]) => {
    const rank = (name: string) => {
      const index = order.indexOf(name);
      return index < 0 ? order.length : index;
    };
    return rank(a) - rank(b);
  });
}

function TextField({
  label,
  value,
  onChange,
  hint,
  required = false,
  error = "",
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  hint?: string;
  required?: boolean;
  error?: string;
}) {
  const id = useId();
  const control = useRef<HTMLInputElement>(null);
  const [touched, setTouched] = useState(false);
  const touch = () => setTouched(true);
  const message = required && !value ? `Enter ${label.toLowerCase()}.` : error;
  useEffect(() => {
    control.current!.setCustomValidity(message);
  }, [message]);
  return (
    <Field
      label={label}
      error={touched ? message : ""}
      {...(hint ? { hint } : {})}
    >
      <input
        ref={control}
        id={id}
        required={required}
        maxLength={500_000}
        value={value}
        onBlur={touch}
        onInvalid={touch}
        onFocus={value ? touch : undefined}
        onChange={(event) => {
          touch();
          onChange(event.target.value);
        }}
      />
    </Field>
  );
}
function JSONFields<T>({
  filename,
  label,
  source,
  read,
  onChange,
  children,
}: {
  filename: string;
  label: string;
  source: string;
  read: (source: string) => T;
  onChange: (source: string) => void;
  children: (value: T, update: (value: T) => void) => React.ReactNode;
}) {
  const [advanced, setAdvanced] = useState(false);
  const control = useRef<HTMLTextAreaElement>(null);
  let value: T | undefined;
  let reason = "";
  try {
    value = read(source);
  } catch (cause) {
    reason = (cause as Error).message;
  }
  useEffect(() => {
    control.current?.setCustomValidity(reason);
  }, [reason, advanced]);
  return (
    <fieldset className="workspace-panel workspace-fields">
      <legend>{label}</legend>
      <button
        type="button"
        aria-pressed={advanced}
        onClick={() => setAdvanced(!advanced)}
      >
        {advanced ? `Use ${label} fields` : `Edit ${label} JSON`}
      </button>
      {advanced || value === undefined ? (
        <>
          <Field label={`${label} JSON`} error={reason}>
            <textarea
              ref={control}
              rows={10}
              maxLength={2 * 1024 * 1024}
              value={source}
              onChange={(event) => onChange(event.target.value)}
            />
          </Field>
          <LocalPreview content={source} filename={filename} preview={false} />
        </>
      ) : (
        children(value, (next) => onChange(jsonText(next)))
      )}
    </fieldset>
  );
}
function StringRows({
  label,
  rows,
  onChange,
}: {
  label: string;
  rows: string[];
  onChange: (next: string[]) => void;
}) {
  return (
    <fieldset className="workspace-panel workspace-fields">
      <legend>{label}</legend>
      <p>Add one argument per row. Preserve spaces and literal placeholders.</p>
      {rows.map((value, index) => (
        <div key={index} className="workspace-fields">
          <TextField
            label={`Argument ${index + 1}`}
            value={value}
            onChange={(next) =>
              onChange(rows.map((row, at) => (at === index ? next : row)))
            }
          />
          <button
            type="button"
            onClick={() => onChange(rows.filter((_, at) => at !== index))}
          >
            {controlIcons.remove} Remove argument {index + 1}
          </button>
        </div>
      ))}
      <button type="button" onClick={() => onChange([...rows, ""])}>
        {controlIcons.add} Add argument
      </button>
    </fieldset>
  );
}
function Pairs({
  label,
  value,
  onChange,
}: {
  label: string;
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}) {
  const [error, setError] = useState("");
  const order = useRef<string[]>([]);
  const rows = ordered(value, order.current);
  const update = (index: number, key: string, text: string) => {
    if (rows.some(([name], at) => at !== index && name === key)) {
      setError("Use a different name for each entry.");
      return;
    }
    setError("");
    order.current = rows.map((row, at) => (at === index ? key : row[0]));
    onChange(
      Object.fromEntries(
        rows.map((row, at) => (at === index ? [key, text] : row)),
      ),
    );
  };
  return (
    <fieldset className="workspace-panel workspace-fields">
      <legend>{label}</legend>
      {rows.map(([key, text], index) => (
        <div key={index} className="workspace-fields">
          <TextField
            label={`${label} ${index + 1} name`}
            value={key}
            onChange={(next) => update(index, next, text)}
          />
          <TextField
            label={`${label} ${index + 1} value`}
            value={text}
            onChange={(next) => update(index, key, next)}
          />
          <button
            type="button"
            onClick={() =>
              onChange(Object.fromEntries(rows.filter((_, at) => at !== index)))
            }
          >
            {controlIcons.remove} Remove {label} {index + 1}
          </button>
        </div>
      ))}
      {error && <p role="alert">{error}</p>}
      <button
        type="button"
        onClick={() => {
          if (Object.hasOwn(value, "")) {
            setError("Complete the unnamed entry first.");
            return;
          }
          onChange(Object.fromEntries([...rows, ["", ""]]));
        }}
      >
        {controlIcons.add} Add {label}
      </button>
    </fieldset>
  );
}
function PluginSkill({
  id,
  content,
  onChange,
  onRemove,
}: {
  id: string;
  content: string;
  onChange: (content: string) => void;
  onRemove: () => void;
}) {
  const [advanced, setAdvanced] = useState(false);
  const [origin, setOrigin] = useState(0);
  const value: RepresentationDraft = {
    editorID: id,
    role: "metadata",
    mediaType: "application/agent-skills+md",
    profileId: "",
    mode: "content",
    content,
    url: "",
    packageData: "",
    handoff: "",
  };
  return (
    <fieldset className="workspace-panel workspace-fields">
      <legend>Skill component</legend>
      <Suspense fallback={<p role="status">Loading Skill fields...</p>}>
        <SkillEditor
          value={value}
          onChange={(next) => onChange(next.content)}
          advanced={advanced}
          onAdvanced={setAdvanced}
          origin={origin}
        />
      </Suspense>
      <button
        type="button"
        aria-pressed={advanced}
        onClick={() => setAdvanced(!advanced)}
      >
        {advanced ? "Use Skill fields" : "Edit component SKILL.md"}
      </button>
      {advanced && (
        <>
          <Field label="Component SKILL.md">
            <textarea
              rows={12}
              maxLength={500_000}
              value={content}
              onChange={(event) => {
                setOrigin(origin + 1);
                onChange(event.target.value);
              }}
            />
          </Field>
          <LocalPreview content={content} filename="SKILL.md" preview={false} />
        </>
      )}
      <button type="button" onClick={onRemove}>
        {controlIcons.remove} Remove Skill component
      </button>
    </fieldset>
  );
}

/** The preparation stays in the private draft. Only the HTTPS archive enters the command. */
export default function PluginAuthoring({
  value,
  onChange,
  version,
}: {
  value: RepresentationDraft;
  onChange: (value: RepresentationDraft) => void;
  version?: string | undefined;
}) {
  const preparation = value.pluginPreparation ?? newPluginPreparation();
  const serverOrder = useRef<string[]>([]);
  const controls = useRef<HTMLElement>(null);
  const focusArchive = useRef(false);
  useLayoutEffect(() => {
    if (focusArchive.current) {
      focusArchive.current = false;
      document.getElementById(`${value.editorID}-url`)?.focus();
    }
  }, [preparation.preparing, value.editorID]);
  const catalogVersion = version?.replace(
    /^\p{White_Space}+|\p{White_Space}+$/gu,
    "",
  );
  const [error, setError] = useState("");
  const [preview, setPreview] = useState(false);
  const change = (patch: Partial<PluginPreparation>) =>
    onChange({ ...value, pluginPreparation: { ...preparation, ...patch } });
  const update = (patch: Partial<RepresentationDraft>) =>
    onChange({ ...value, ...patch });
  let files: ReturnType<typeof pluginFiles> = [];
  let fileError = "";
  try {
    files = pluginFiles(preparation);
  } catch (cause) {
    fileError = (cause as Error).message;
  }
  return (
    <section
      ref={controls}
      aria-label="Plugin authoring"
      className="workspace-fields"
    >
      <Field label="Plugin input">
        <select
          id={`${value.editorID}-plugin-input`}
          value={preparation.preparing ? "prepare" : "archive"}
          onChange={(event) =>
            change({ preparing: event.target.value === "prepare" })
          }
        >
          <option value="archive">Use an existing archive</option>
          <option value="prepare">Prepare Plugin files</option>
        </select>
      </Field>
      {preparation.sourceFiles && (
        <details>
          <summary>Original Plugin files</summary>
          <p>
            Only selected documents are retained. Restore scripts, assets and
            other files from the original package before building a replacement
            archive.
          </p>
          <ul>
            {preparation.sourceFiles.map((file) => (
              <li key={file.path}>
                <code>{file.path}</code>:{" "}
                {file.retained
                  ? "Retained document"
                  : "Original package required"}
              </li>
            ))}
          </ul>
          <p>
            Prepared files do not recreate the original archive. Submit a
            replacement HTTPS archive after any manifest or component change.
          </p>
        </details>
      )}
      {preparation.preparing ? (
        <>
          <p>
            Create plugin.json below. Add Skill or MCP files if needed, then
            download the files.
          </p>
          <JSONFields
            filename="plugin.json"
            label="Plugin manifest"
            source={preparation.manifest}
            read={pluginManifest}
            onChange={(manifest) => change({ manifest })}
          >
            {(manifest, write) => (
              <>
                <TextField
                  label="Plugin name"
                  required
                  error={pluginNameError(manifest.name as string)}
                  value={manifest.name as string}
                  onChange={(name) => write({ ...manifest, name })}
                  hint="Use up to 64 lowercase letters, digits, dots or hyphens. Do not repeat dots or hyphens."
                />
                {(["version", "description", "license"] as const).map((key) => (
                  <TextField
                    key={key}
                    label={`Plugin ${key} (optional)`}
                    value={(manifest[key] as string | undefined) ?? ""}
                    onChange={(text) => {
                      const next = { ...manifest };
                      if (text) next[key] = text;
                      else delete next[key];
                      write(next);
                    }}
                  />
                ))}
                {catalogVersion && (
                  <button
                    type="button"
                    onClick={() =>
                      write({ ...manifest, version: catalogVersion })
                    }
                  >
                    Use Catalog version {catalogVersion} in manifest
                  </button>
                )}
              </>
            )}
          </JSONFields>
          <p>
            These Skills need no separate Catalog Resource or package handoff.
          </p>
          {preparation.skills.map((skill) => (
            <PluginSkill
              key={skill.id}
              {...skill}
              onChange={(content) =>
                change({
                  skills: preparation.skills.map((row) =>
                    row.id === skill.id ? { ...row, content } : row,
                  ),
                })
              }
              onRemove={() => {
                if (
                  window.confirm(
                    "Remove this Skill component from the prepared files?",
                  )
                )
                  change({
                    skills: preparation.skills.filter(
                      (row) => row.id !== skill.id,
                    ),
                  });
              }}
            />
          ))}
          <button
            type="button"
            onClick={() =>
              change({
                skills: [
                  ...preparation.skills,
                  { id: crypto.randomUUID(), content: "" },
                ],
              })
            }
          >
            {controlIcons.add} Add Skill component
          </button>
          <JSONFields
            filename="mcp.json"
            label="Plugin MCP"
            source={preparation.mcp}
            read={pluginMCP}
            onChange={(mcp) => change({ mcp })}
          >
            {(mcp, write) => (
              <>
                <p>
                  Name each MCP server. This file uses the Plugin schema,
                  separate from Cards and Registry metadata.
                </p>
                {ordered(mcp.mcpServers, serverOrder.current).map(
                  ([name, server], index) => {
                    const rows = ordered(mcp.mcpServers, serverOrder.current);
                    const save = (next: Record<string, unknown>) =>
                      write({
                        ...mcp,
                        mcpServers: Object.fromEntries(
                          rows.map((row, at) =>
                            at === index ? [name, next] : row,
                          ),
                        ),
                      });
                    return (
                      <fieldset
                        key={index}
                        className="workspace-panel workspace-fields"
                      >
                        <legend>MCP server {index + 1}</legend>
                        <TextField
                          label={`Server ${index + 1} name`}
                          required
                          value={name}
                          onChange={(next) => {
                            if (
                              rows.some(
                                ([key], at) => at !== index && key === next,
                              )
                            ) {
                              setError(
                                "Use a different name for each MCP server.",
                              );
                              return;
                            }
                            setError("");
                            serverOrder.current = rows.map((row, at) =>
                              at === index ? next : row[0],
                            );
                            write({
                              ...mcp,
                              mcpServers: Object.fromEntries(
                                rows.map((row, at) =>
                                  at === index ? [next, server] : row,
                                ),
                              ),
                            });
                          }}
                        />
                        <Field label={`Server ${index + 1} transport`}>
                          <select
                            value={server.type as string}
                            onChange={(event) => {
                              if (
                                !window.confirm(
                                  "Change transport and replace this server's transport fields?",
                                )
                              )
                                return;
                              save(
                                event.target.value === "stdio"
                                  ? { type: "stdio", command: "" }
                                  : { type: event.target.value, url: "" },
                              );
                            }}
                          >
                            <option value="stdio">stdio</option>
                            <option value="streamable-http">
                              Streamable HTTP
                            </option>
                            <option value="sse">SSE</option>
                          </select>
                        </Field>
                        {server.type === "stdio" ? (
                          <>
                            <TextField
                              label="Command"
                              required
                              error={pluginServerError({
                                type: "stdio",
                                command: server.command,
                              })}
                              value={
                                (server.command as string | undefined) ?? ""
                              }
                              onChange={(command) =>
                                save({ ...server, command })
                              }
                              hint="Use one executable. Add arguments below."
                            />
                            <StringRows
                              label="Arguments"
                              rows={(server.args as string[] | undefined) ?? []}
                              onChange={(args) => save({ ...server, args })}
                            />
                          </>
                        ) : (
                          <TextField
                            label="MCP endpoint URL"
                            required
                            error={pluginEndpointError(
                              (server.url as string | undefined) ?? "",
                            )}
                            value={(server.url as string | undefined) ?? ""}
                            onChange={(url) => save({ ...server, url })}
                            hint="Use HTTPS, or HTTP on exact localhost or a loopback IP address."
                          />
                        )}
                        <details>
                          <summary>Optional connection fields</summary>
                          <div className="workspace-fields">
                            <p>
                              Use non-secret values or literal client
                              placeholders such as ${"{API_TOKEN}"}. Do not
                              paste runtime credentials.
                            </p>
                            {server.type === "stdio" ? (
                              <>
                                <TextField
                                  label="Working directory (optional)"
                                  value={
                                    (server.cwd as string | undefined) ?? ""
                                  }
                                  onChange={(cwd) => {
                                    const next = { ...server };
                                    if (cwd) next.cwd = cwd;
                                    else delete next.cwd;
                                    save(next);
                                  }}
                                  hint="Use a contained ./path, ${PLUGIN_ROOT} or ${PLUGIN_DATA}."
                                />
                                <Pairs
                                  label="Environment variable"
                                  value={
                                    (server.env as
                                      Record<string, string> | undefined) ?? {}
                                  }
                                  onChange={(env) => save({ ...server, env })}
                                />
                              </>
                            ) : (
                              <Pairs
                                label="Header"
                                value={
                                  (server.headers as
                                    Record<string, string> | undefined) ?? {}
                                }
                                onChange={(headers) =>
                                  save({ ...server, headers })
                                }
                              />
                            )}
                          </div>
                        </details>
                        {pluginServerError(server) && (
                          <p role="status">{pluginServerError(server)}</p>
                        )}
                        <button
                          type="button"
                          onClick={() => {
                            if (
                              window.confirm(
                                "Remove this MCP server from the prepared files?",
                              )
                            )
                              write({
                                ...mcp,
                                mcpServers: Object.fromEntries(
                                  rows.filter((_, at) => at !== index),
                                ),
                              });
                          }}
                        >
                          {controlIcons.remove} Remove MCP server {index + 1}
                        </button>
                      </fieldset>
                    );
                  },
                )}
                <button
                  type="button"
                  onClick={() => {
                    let name = "server";
                    for (let n = 2; Object.hasOwn(mcp.mcpServers, name); n++)
                      name = `server-${n}`;
                    write({
                      ...mcp,
                      mcpServers: Object.fromEntries([
                        ...Object.entries(mcp.mcpServers),
                        [name, { type: "stdio", command: "" }],
                      ]),
                    });
                  }}
                >
                  {controlIcons.add} Add MCP server
                </button>
              </>
            )}
          </JSONFields>
          {preparation.mcp && (
            <button
              type="button"
              onClick={() => {
                if (
                  window.confirm(
                    "Remove mcp.json and all of its server configuration?",
                  )
                )
                  change({ mcp: "" });
              }}
            >
              {controlIcons.remove} Remove MCP configuration
            </button>
          )}
          {error && <p role="alert">{error}</p>}
          <details>
            <summary>Use Plugin example files</summary>
            <p>Replace the prepared files only. Keep archive inputs.</p>
            <button
              type="button"
              onClick={() => {
                if (
                  !window.confirm(
                    "Replace the prepared files with the Plugin example?",
                  )
                )
                  return;
                change({
                  manifest: pluginStarterFiles[0]!.content,
                  mcp: pluginStarterFiles[1]!.content,
                  skills: [
                    {
                      id: crypto.randomUUID(),
                      content: pluginStarterFiles[2]!.content,
                    },
                  ],
                });
              }}
            >
              Use Plugin example
            </button>
          </details>
          <button
            type="button"
            aria-expanded={preview}
            onClick={() => {
              setPreview(!preview);
              if (!preview && controls.current)
                reportInvalidFields(controls.current);
            }}
          >
            {preview
              ? "Hide prepared files"
              : "Preview and download Plugin files"}
          </button>
          {preview && (
            <section aria-label="Prepared Plugin files">
              {fileError ? (
                <p role="alert">{fileError}</p>
              ) : (
                <>
                  <p>
                    Save each file at its displayed path. Add any referenced
                    scripts or assets.
                  </p>
                  <CodeBlock
                    text={files.map((file) => file.path).join("\n")}
                    rawText
                  />
                  {files.map((file) => (
                    <details key={file.path}>
                      <summary>{file.path}</summary>
                      <LocalPreview
                        content={file.content}
                        filename={file.path.split("/").at(-1)!}
                      />
                    </details>
                  ))}
                  <p>
                    Run one command from the directory containing plugin.json.
                    Add any supporting file paths.
                  </p>
                  <CodeBlock
                    text={pluginPackaging(
                      pluginManifest(preparation.manifest).name as string,
                      files,
                    )}
                    rawText
                  />
                  <p>
                    Host the complete archive at a credential-free HTTPS URL.
                    Enter its format, URL and optional SHA-256 checksum below.
                  </p>
                </>
              )}
            </section>
          )}
          <p>
            Package and host file changes before replacing the archive URL or
            checksum.
          </p>
          <button
            type="button"
            onClick={() => {
              focusArchive.current = true;
              change({ preparing: false });
            }}
          >
            Continue with archive inputs
          </button>
        </>
      ) : (
        <>
          <Field label="Plugin archive format">
            <select
              value={value.mediaType}
              onChange={(event) => update({ mediaType: event.target.value })}
            >
              <option value="application/agent-plugins+zip">ZIP</option>
              <option value="application/agent-plugins+gzip">
                Gzipped tar
              </option>
              {![
                "application/agent-plugins+zip",
                "application/agent-plugins+gzip",
              ].includes(value.mediaType) && (
                <option value={value.mediaType}>{value.mediaType}</option>
              )}
            </select>
          </Field>
          <Field
            label="Plugin archive URL"
            hint="Supply a complete ZIP or gzipped-tar archive with root plugin.json. Use credential-free HTTPS without a fragment. The URL needs no filename suffix."
          >
            <input
              id={`${value.editorID}-url`}
              required
              type="url"
              maxLength={2048}
              value={value.url}
              onChange={(event) => update({ url: event.target.value })}
            />
          </Field>
          <Field
            label="Expected archive checksum (optional)"
            hint="Use sha256: followed by 64 lowercase hexadecimal digits. Leave blank to record the computed checksum. Reused archives keep their pinned checksum."
          >
            <input
              id={`${value.editorID}-checksum`}
              maxLength={71}
              value={value.expectedDigest ?? ""}
              placeholder="sha256:..."
              onChange={(event) =>
                update({ expectedDigest: event.target.value })
              }
            />
          </Field>
          <p>
            After confirmation, the Catalog inspects the HTTPS archive. It does
            not run the Plugin.
          </p>
        </>
      )}
    </section>
  );
}
