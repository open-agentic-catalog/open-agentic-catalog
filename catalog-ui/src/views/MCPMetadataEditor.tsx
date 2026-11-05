import { controlIcons } from "../shell/Icon";
import { useEffect, useRef, useState } from "react";
import { Field } from "./workspace";
import { NativeChoice } from "./NativeChoice";
import type { RepresentationDraft } from "./representations";
import {
  mcpEndpointError,
  mcpFieldError,
  mcpFormat,
  mcpFormats,
  mcpOptional,
  mcpVersion,
  readMCPDocument,
  type MCPDocument,
  type MCPPackage,
  type MCPRemote,
  type MCPRequirement,
} from "./mcp-metadata";

/** Native validity and visible field errors use the same local check. */
function MCPField({
  id,
  label,
  value,
  onChange,
  required = true,
  error = mcpFieldError(value, "text", required),
  hint,
  multiline = false,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  required?: boolean;
  error?: string;
  hint?: string;
  multiline?: boolean;
}) {
  const control = useRef<HTMLInputElement & HTMLTextAreaElement>(null);
  const [touched, setTouched] = useState(false);
  const touch = () => setTouched(true);
  useEffect(() => {
    control.current!.setCustomValidity(error);
  }, [error]);
  const props = {
    id,
    value,
    required,
    maxLength: 2 * 1024 * 1024,
    "aria-invalid": (touched && !!error) || undefined,
    "aria-describedby": touched && error ? `${id}-error` : undefined,
    onChange: (
      event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
    ) => {
      touch();
      onChange(event.target.value);
    },
    onBlur: touch,
    onInvalid: touch,
    onFocus: value ? touch : undefined,
  };
  return (
    <div>
      <Field
        label={label}
        error={touched ? error : ""}
        {...(hint ? { hint } : {})}
      >
        {multiline ? (
          <textarea ref={control} rows={3} {...props} />
        ) : (
          <input ref={control} {...props} />
        )}
      </Field>
    </div>
  );
}

/** Requirements describe client inputs. The editor never asks for secret values. */
function Requirements({
  id,
  label,
  value,
  onChange,
  environment = false,
}: {
  id: string;
  label: string;
  value: MCPRequirement[];
  onChange: (value: MCPRequirement[]) => void;
  environment?: boolean;
}) {
  const focus = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (focus.current === undefined) return;
    document.getElementById(focus.current)?.focus();
    focus.current = undefined;
  }, [value]);
  const noun = environment ? "environment requirement" : "header requirement";
  return (
    <details>
      <summary>{label}</summary>
      <p>
        Declare what clients must supply. Do not enter passwords or tokens.
        Advanced JSON retains non-secret defaults and variable declarations.
      </p>
      {value.map((item, index) => {
        const change = (patch: Partial<MCPRequirement>) =>
          onChange(
            value.map((old, at) => (at === index ? { ...old, ...patch } : old)),
          );
        return (
          <fieldset key={index} className="workspace-panel workspace-fields">
            <legend>
              {label} {index + 1}
            </legend>
            <MCPField
              id={`${id}-${index}-name`}
              label={`${label} ${index + 1} name`}
              value={item.name ?? ""}
              onChange={(name) => change({ name })}
            />
            <MCPField
              id={`${id}-${index}-description`}
              label={`${label} ${index + 1} description (optional)`}
              value={item.description ?? ""}
              required={false}
              onChange={(description) =>
                onChange(
                  value.map((old, at) =>
                    at === index
                      ? mcpOptional(old, "description", description)
                      : old,
                  ),
                )
              }
            />
            {(
              [
                ["isRequired", "Required"],
                ["isSecret", "Secret"],
              ] as const
            ).map(([key, text]) => (
              <Field key={key} label={`${label} ${index + 1}: ${text}`}>
                <input
                  type="checkbox"
                  checked={item[key] === true}
                  onChange={(event) => change({ [key]: event.target.checked })}
                />
              </Field>
            ))}
            <button
              type="button"
              onClick={() => {
                focus.current =
                  value.length === 1
                    ? `${id}-add`
                    : `${id}-${Math.min(index, value.length - 2)}-name`;
                onChange(value.filter((_, at) => at !== index));
              }}
            >
              {controlIcons.remove} Remove {label.toLowerCase()} {index + 1}
            </button>
          </fieldset>
        );
      })}
      <button
        type="button"
        id={`${id}-add`}
        onClick={() => {
          focus.current = `${id}-${value.length}-name`;
          onChange([...value, { name: "", isRequired: true, isSecret: true }]);
        }}
      >
        {controlIcons.add} Add {noun}
      </button>
    </details>
  );
}

function ProtocolVersions({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string[];
  onChange: (value: string[]) => void;
}) {
  const focus = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (focus.current === undefined) return;
    document.getElementById(focus.current)?.focus();
    focus.current = undefined;
  }, [value]);
  return (
    <details>
      <summary>{label} protocol versions (optional)</summary>
      <p>
        Declare only MCP protocol versions this endpoint supports. For example,
        2025-11-25. The Catalog version is separate.
      </p>
      {value.map((item, index) => (
        <div key={index} className="mcp-value-row">
          <NativeChoice
            id={`${id}-${index}`}
            label={`${label} protocol version ${index + 1}`}
            customLabel={`${label} custom protocol version ${index + 1}`}
            choices={[
              ["2026-07-28", "2026-07-28"],
              ["2025-11-25", "2025-11-25"],
            ]}
            value={item}
            validate={(next) => mcpFieldError(next, "text", true)}
            onChange={(next) =>
              onChange(value.map((old, at) => (at === index ? next : old)))
            }
          />
          <button
            type="button"
            onClick={() => {
              focus.current =
                value.length === 1
                  ? `${id}-add`
                  : `${id}-${Math.min(index, value.length - 2)}`;
              onChange(value.filter((_, at) => at !== index));
            }}
          >
            {controlIcons.remove} Remove {label.toLowerCase()} protocol version{" "}
            {index + 1}
          </button>
        </div>
      ))}
      <button
        type="button"
        id={`${id}-add`}
        onClick={() => {
          focus.current = `${id}-${value.length}`;
          onChange([...value, ""]);
        }}
      >
        {controlIcons.add} Add {label.toLowerCase()} protocol version
      </button>
    </details>
  );
}

/** Card and Registry forms share native text without converting between formats. */
export default function MCPMetadataEditor({
  value,
  onChange,
  onReplace,
  version = "",
  origin,
  advanced,
  onAdvanced,
}: {
  value: RepresentationDraft;
  onChange: (value: RepresentationDraft) => void;
  onReplace: (value: RepresentationDraft) => void;
  version?: string | undefined;
  origin: number;
  advanced: boolean;
  onAdvanced: (open: boolean) => void;
}) {
  const format = mcpFormat(value.mediaType);
  const parsed = readMCPDocument(value.content, value.mediaType, version);
  const doc = parsed.document;
  const lastGenerated = useRef<string | undefined>(undefined);
  const linkedOrigin = useRef(origin);
  const [linked, setLinked] = useState(false);
  const rowFocus = useRef<string | undefined>(undefined);
  const current = useRef({ value, onChange, onAdvanced, origin });
  current.current = { value, onChange, onAdvanced, origin };
  const commit = (next: MCPDocument, followVersion: boolean) => {
    const content = JSON.stringify(next, null, 2) + "\n";
    lastGenerated.current = content;
    linkedOrigin.current = origin;
    setLinked(followVersion);
    onChange({ ...value, content });
  };
  const edit = (next: MCPDocument, focusID?: string) => {
    rowFocus.current = focusID;
    const followVersion =
      !value.content ||
      (linked &&
        lastGenerated.current === value.content &&
        linkedOrigin.current === origin);
    commit(
      { ...next, ...(followVersion ? { version: mcpVersion(version) } : {}) },
      followVersion,
    );
  };
  const patch = (next: Partial<MCPDocument>, focusID?: string) =>
    edit({ ...doc, ...next }, focusID);
  useEffect(() => {
    const target = rowFocus.current;
    if (target === undefined) return;
    rowFocus.current = undefined;
    document.getElementById(target)?.focus();
  }, [value.content]);
  useEffect(() => {
    const {
      value: latest,
      onChange: change,
      origin: latestOrigin,
    } = current.current;
    if (
      !linked ||
      lastGenerated.current !== latest.content ||
      linkedOrigin.current !== latestOrigin
    )
      return;
    const read = readMCPDocument(
      latest.content,
      latest.mediaType,
      version,
    ).document;
    if (!read || read.version === mcpVersion(version)) return;
    const content =
      JSON.stringify({ ...read, version: mcpVersion(version) }, null, 2) + "\n";
    lastGenerated.current = content;
    change({ ...latest, content });
  }, [linked, version, origin]);
  useEffect(() => {
    if (!doc) current.current.onAdvanced(true);
  }, [doc]);
  const remotes = doc?.remotes ?? [];
  const packages = doc?.packages ?? [];
  const registryTypes = [
    ["npm", "npm"],
    ["pypi", "PyPI"],
    ["cargo", "Cargo"],
    ["oci", "OCI"],
    ["nuget", "NuGet"],
    ["mcpb", "MCPB"],
  ] as const;
  return (
    <div className="mcp-metadata-editor workspace-fields">
      <Field
        label="MCP document"
        hint="Server Cards describe hosted connectivity. Registry documents describe remote or package distribution."
      >
        <select
          value={format ?? "other"}
          onChange={(event) => {
            const selected = event.target.value as "card" | "registry";
            if (selected === format) return;
            if (
              (value.content || value.documentStaged) &&
              !window.confirm(
                "Start a new MCP document in the selected format? This discards this representation's current document and selected file. Changing a label does not convert existing metadata.",
              )
            )
              return;
            const next = {
              ...value,
              mediaType: mcpFormats[selected].media,
              content: "",
            };
            delete next.documentStaged;
            lastGenerated.current = undefined;
            setLinked(false);
            onAdvanced(false);
            onReplace(next);
          }}
        >
          <option value="card">
            Hosted server (Server Card, experimental)
          </option>
          <option value="registry">
            Package or distribution metadata (Registry server.json)
          </option>
          {!format && (
            <option value="other" disabled>
              Other MCP format (Advanced)
            </option>
          )}
        </select>
      </Field>
      {format === "card" ? (
        <p>
          The Server Card format is experimental. Discovery snapshots require a
          primary Server Card. No endpoint is contacted.
        </p>
      ) : format === "registry" ? (
        <p>
          Registry metadata can be authored and released locally. To include it
          in discovery snapshots, add a primary Server Card in another row. No
          package is installed or executed.
        </p>
      ) : null}
      {!doc && (
        <p className="notice warning">
          {parsed.reason} Your document stays in Advanced JSON.
        </p>
      )}
      {doc && (
        <fieldset
          hidden={advanced}
          disabled={advanced}
          className="workspace-fields mcp-guided-fields"
        >
          <legend className="sr-only">MCP metadata fields</legend>
          <MCPField
            id={`${value.editorID}-mcp-name`}
            label="MCP server name"
            value={doc.name ?? ""}
            error={mcpFieldError(doc.name ?? "", "name")}
            onChange={(name) => patch({ name })}
            hint="Use namespace/server, such as com.example/my-server. This differs from the Catalog AIR identifier and Listing display name."
          />
          <MCPField
            id={`${value.editorID}-mcp-title`}
            label="MCP display title (optional)"
            value={doc.title ?? ""}
            required={false}
            error={mcpFieldError(doc.title ?? "", "title", false)}
            onChange={(title) => edit(mcpOptional(doc, "title", title))}
            hint="An optional readable title, up to 100 characters."
          />
          <MCPField
            id={`${value.editorID}-mcp-description`}
            label="MCP description"
            value={doc.description ?? ""}
            error={mcpFieldError(doc.description ?? "", "description")}
            onChange={(description) => patch({ description })}
            multiline
            hint="Describe the server in up to 100 characters."
          />
          <p>
            Document version:{" "}
            {value.content
              ? doc.version || "Not set"
              : mcpVersion(version) || "Enter the Version label above"}
            . Package and MCP protocol versions are separate.
          </p>
          {value.content && doc.version !== mcpVersion(version) && (
            <button
              type="button"
              id={`${value.editorID}-native-version`}
              disabled={!mcpVersion(version)}
              onClick={() =>
                commit({ ...doc, version: mcpVersion(version) }, true)
              }
            >
              {controlIcons.edit} Use Version label in this MCP document
            </button>
          )}
          <fieldset className="workspace-panel workspace-fields">
            <legend>Remote endpoints (optional)</legend>
            <p>
              Add endpoints that clients can use. Server Cards do not list
              tools, prompts or resources.
            </p>
            {remotes.map((item, index) => {
              const change = (next: MCPRemote) =>
                patch({
                  remotes: remotes.map((old, at) =>
                    at === index ? next : old,
                  ),
                });
              return (
                <fieldset
                  key={index}
                  className="workspace-panel workspace-fields"
                >
                  <legend>Endpoint {index + 1}</legend>
                  <Field label={`Endpoint ${index + 1} transport`}>
                    <select
                      value={item.type ?? ""}
                      onChange={(event) =>
                        change({ ...item, type: event.target.value })
                      }
                      required
                    >
                      {!item.type && (
                        <option value="">Choose a transport</option>
                      )}
                      <option value="streamable-http">Streamable HTTP</option>
                      <option value="sse">Server-Sent Events (SSE)</option>
                    </select>
                  </Field>
                  <MCPField
                    id={`${value.editorID}-mcp-remote-${index}`}
                    label={`Endpoint ${index + 1} URL`}
                    value={item.url ?? ""}
                    error={mcpEndpointError(item.url ?? "", format!)}
                    onChange={(url) => change({ ...item, url })}
                    hint={
                      format === "card"
                        ? "Use an HTTPS endpoint or a declared template such as {baseUrl}/mcp. Configure template variables in Advanced JSON."
                        : "Registry remote endpoints must use safe absolute HTTPS URLs."
                    }
                  />
                  {format === "card" && (
                    <ProtocolVersions
                      id={`${value.editorID}-mcp-protocol-${index}`}
                      label={`Endpoint ${index + 1}`}
                      value={item.supportedProtocolVersions ?? []}
                      onChange={(supportedProtocolVersions) =>
                        change({ ...item, supportedProtocolVersions })
                      }
                    />
                  )}
                  <Requirements
                    id={`${value.editorID}-mcp-header-${index}`}
                    label={`Endpoint ${index + 1} headers`}
                    value={item.headers ?? []}
                    onChange={(headers) => change({ ...item, headers })}
                  />
                  <button
                    type="button"
                    onClick={() =>
                      patch(
                        { remotes: remotes.filter((_, at) => at !== index) },
                        remotes.length === 1
                          ? `${value.editorID}-mcp-add-remote`
                          : `${value.editorID}-mcp-remote-${Math.min(index, remotes.length - 2)}`,
                      )
                    }
                  >
                    {controlIcons.remove} Remove endpoint {index + 1}
                  </button>
                </fieldset>
              );
            })}
            <button
              type="button"
              id={`${value.editorID}-mcp-add-remote`}
              onClick={() =>
                patch(
                  {
                    remotes: [...remotes, { type: "streamable-http", url: "" }],
                  },
                  `${value.editorID}-mcp-remote-${remotes.length}`,
                )
              }
            >
              {controlIcons.add} Add endpoint
            </button>
          </fieldset>
          {format === "registry" && (
            <fieldset className="workspace-panel workspace-fields">
              <legend>Packages (optional)</legend>
              <p>
                Describe existing packages. The Catalog does not check their
                availability or run their commands.
              </p>
              {packages.map((item, index) => {
                const change = (next: MCPPackage) =>
                  patch({
                    packages: packages.map((old, at) =>
                      at === index ? next : old,
                    ),
                  });
                const transport = item.transport ?? {};
                const knownRegistry = registryTypes.some(
                  ([key]) => key === item.registryType,
                );
                return (
                  <fieldset
                    key={index}
                    className="workspace-panel workspace-fields"
                  >
                    <legend>Package {index + 1}</legend>
                    <Field label={`Package ${index + 1} registry`}>
                      <select
                        value={knownRegistry ? item.registryType : "other"}
                        onChange={(event) =>
                          change({
                            ...item,
                            registryType:
                              event.target.value === "other"
                                ? ""
                                : event.target.value,
                          })
                        }
                      >
                        {registryTypes.map(([key, label]) => (
                          <option key={key} value={key}>
                            {label}
                          </option>
                        ))}
                        <option value="other">Other registry type</option>
                      </select>
                    </Field>
                    {!knownRegistry && (
                      <MCPField
                        id={`${value.editorID}-mcp-registry-${index}`}
                        label={`Package ${index + 1} custom registry type`}
                        value={item.registryType ?? ""}
                        onChange={(registryType) =>
                          change({ ...item, registryType })
                        }
                      />
                    )}
                    <MCPField
                      id={`${value.editorID}-mcp-package-${index}`}
                      label={`Package ${index + 1} identifier`}
                      value={item.identifier ?? ""}
                      onChange={(identifier) => change({ ...item, identifier })}
                      hint="For example, @example/documentation-mcp for npm or a registry/repository reference for OCI."
                    />
                    <MCPField
                      id={`${value.editorID}-mcp-package-version-${index}`}
                      label={`Package ${index + 1} version (optional)`}
                      value={item.version ?? ""}
                      required={false}
                      error={mcpFieldError(
                        item.version ?? "",
                        "package-version",
                        false,
                      )}
                      onChange={(next) =>
                        change(mcpOptional(item, "version", next))
                      }
                      hint="The package release version is independent of the document and Catalog version. Leave it blank when not declared."
                    />
                    <Field label={`Package ${index + 1} transport`}>
                      <select
                        required
                        value={transport.type ?? ""}
                        onChange={(event) =>
                          change({
                            ...item,
                            transport: {
                              ...transport,
                              type: event.target.value,
                              ...(event.target.value !== "stdio" &&
                              transport.url === undefined
                                ? { url: "" }
                                : {}),
                            },
                          })
                        }
                      >
                        {!transport.type && (
                          <option value="">Choose a transport</option>
                        )}
                        <option value="stdio">
                          Standard input/output (stdio)
                        </option>
                        <option value="streamable-http">Streamable HTTP</option>
                        <option value="sse">Server-Sent Events (SSE)</option>
                      </select>
                    </Field>
                    {transport.type && transport.type !== "stdio" && (
                      <MCPField
                        id={`${value.editorID}-mcp-package-url-${index}`}
                        label={`Package ${index + 1} transport URL or template`}
                        value={transport.url ?? ""}
                        error={mcpEndpointError(transport.url ?? "", "package")}
                        onChange={(url) =>
                          change({ ...item, transport: { ...transport, url } })
                        }
                        hint="Package transports can describe local HTTP URLs or templates. This is metadata, not a connection request."
                      />
                    )}
                    {(item.registryType === "mcpb" ||
                      item.fileSha256 !== undefined) && (
                      <MCPField
                        id={`${value.editorID}-mcp-package-sha-${index}`}
                        label={`Package ${index + 1} SHA-256`}
                        value={item.fileSha256 ?? ""}
                        required={item.registryType === "mcpb"}
                        error={mcpFieldError(
                          item.fileSha256 ?? "",
                          "checksum",
                          item.registryType === "mcpb",
                        )}
                        onChange={(next) =>
                          change(mcpOptional(item, "fileSha256", next))
                        }
                        hint="Enter the package file's actual SHA-256 hash. MCPB metadata requires it."
                      />
                    )}
                    <Requirements
                      id={`${value.editorID}-mcp-env-${index}`}
                      label={`Package ${index + 1} environment`}
                      value={item.environmentVariables ?? []}
                      onChange={(environmentVariables) =>
                        change({ ...item, environmentVariables })
                      }
                      environment
                    />
                    <p>
                      Advanced JSON retains runtime hints, argument arrays,
                      registry URLs and other package settings.
                    </p>
                    <button
                      type="button"
                      onClick={() =>
                        patch(
                          {
                            packages: packages.filter((_, at) => at !== index),
                          },
                          packages.length === 1
                            ? `${value.editorID}-mcp-add-package`
                            : `${value.editorID}-mcp-package-${Math.min(index, packages.length - 2)}`,
                        )
                      }
                    >
                      {controlIcons.remove} Remove package {index + 1}
                    </button>
                  </fieldset>
                );
              })}
              <button
                type="button"
                id={`${value.editorID}-mcp-add-package`}
                onClick={() =>
                  patch(
                    {
                      packages: [
                        ...packages,
                        {
                          registryType: "npm",
                          identifier: "",
                          transport: { type: "stdio" },
                        },
                      ],
                    },
                    `${value.editorID}-mcp-package-${packages.length}`,
                  )
                }
              >
                {controlIcons.add} Add package
              </button>
              {!remotes.length && !packages.length && (
                <p>
                  No distribution is declared. You can save this metadata;
                  validation reports a warning.
                </p>
              )}
            </fieldset>
          )}
        </fieldset>
      )}
    </div>
  );
}
