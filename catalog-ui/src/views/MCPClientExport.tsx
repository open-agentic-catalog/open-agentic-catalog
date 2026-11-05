// Prepare inert client JSON from authorized native metadata, entirely in memory.
import { useLayoutEffect, useRef, useState } from "react";
import { result } from "../api/client";
import type { Client, RecordDetail } from "../api/client";
import { permitsNamespace } from "../api/permissions";
import { controlIcons } from "../shell/Icon";
import { verifyArtifact } from "./ArtifactDownload";
import { CodeBlock } from "./CodeBlock";
import { Field } from "./workspace";
import type { useNamespaceAccessState } from "./namespace-access";
import type { Representation } from "./handoff";
import { parseNativeJSONObject } from "./json-input";
import {
  mcpClientFormats,
  mcpExportByteLimit,
  mcpRemoteChoices,
  projectMCPConfiguration,
  supportsMCPExport,
} from "./mcp-client-export";
import type { MCPClientFormat } from "./mcp-client-export";

type Props = {
  record: RecordDetail;
  client: Client;
  onFailure: (error: unknown) => void;
  permissionState?: ReturnType<typeof useNamespaceAccessState> | undefined;
  compact?: boolean;
};
type ArtifactReply = {
  data?: ArrayBuffer;
  error?: unknown;
  response: Response;
};
type Source = {
  representation: Representation;
  data: unknown;
  reply?: ArtifactReply;
};

export function MCPClientExport(props: Props) {
  const { record, permissionState } = props;
  if (
    record.resource.kind !== "mcp_server" ||
    (record.release.state !== "published" &&
      record.release.state !== "deprecated")
  )
    return null;
  if (
    permissionState &&
    !permissionState.busy &&
    !permissionState.error &&
    !permitsNamespace(
      permissionState.access,
      "resource.read",
      "catalog.read",
      record.resource.namespaceId,
    )
  )
    return null;
  return (
    <ClientConfiguration
      key={`${record.release.id}:${record.versionRevision.id}:${JSON.stringify(permissionState?.access)}`}
      {...props}
    />
  );
}

function ClientConfiguration({
  record,
  client,
  onFailure,
  compact = false,
}: Props) {
  const Heading = compact ? "h4" : "h3";
  const representations = record.versionRevision.representations;
  const [representationID, setRepresentationID] = useState(
    representations.length === 1 ? representations[0]!.id : "",
  );
  const [format, setFormat] = useState<MCPClientFormat>("mcp-vscode");
  const [position, setPosition] = useState(0);
  const [source, setSource] = useState<Source>();
  const [prepared, setPrepared] =
    useState<ReturnType<typeof projectMCPConfiguration>>();
  const [href, setHref] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [invalidated, setInvalidated] = useState(false);
  const request = useRef<AbortController | undefined>(undefined);
  const attachment = useRef("");
  const preview = useRef<HTMLDivElement>(null);
  const selected = representations.find((rep) => rep.id === representationID);
  const current = source?.representation === selected ? source : undefined;
  const data: unknown = current?.data ?? selected?.data;
  let choices: ReturnType<typeof mcpRemoteChoices> = [];
  let unavailable = "";
  if (selected) {
    if (!supportsMCPExport(selected))
      unavailable =
        "This representation does not contain a supported MCP Registry document or Server Card.";
    else if (data !== undefined) {
      try {
        choices = mcpRemoteChoices(data, selected);
      } catch (cause) {
        unavailable =
          cause instanceof Error
            ? cause.message
            : "Unsupported native metadata.";
      }
    } else if (!selected.artifactId)
      unavailable =
        "No retained native document is available. Use the existing delivery references below.";
  }
  const clear = () => {
    request.current?.abort();
    if (attachment.current) URL.revokeObjectURL(attachment.current);
    attachment.current = "";
    setHref("");
    setPrepared(undefined);
    setError("");
    setBusy(false);
  };
  useLayoutEffect(() => {
    setInvalidated(client.signal?.aborted === true);
    setSource(undefined);
    setPrepared(undefined);
    setHref("");
    setError("");
    setBusy(false);
    setPosition(0);
    setRepresentationID(
      record.versionRevision.representations.length === 1
        ? record.versionRevision.representations[0]!.id
        : "",
    );
    const invalidate = () => {
      request.current?.abort();
      if (attachment.current) URL.revokeObjectURL(attachment.current);
      attachment.current = "";
      setHref("");
      setPrepared(undefined);
      setError("");
      setBusy(false);
      setInvalidated(true);
      setSource(undefined);
    };
    client.signal?.addEventListener("abort", invalidate);
    return () => {
      client.signal?.removeEventListener("abort", invalidate);
      request.current?.abort();
      if (attachment.current) URL.revokeObjectURL(attachment.current);
      attachment.current = "";
    };
  }, [client, record]);

  const prepare = async () => {
    clear();
    if (!selected || unavailable || client.signal?.aborted) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    try {
      let native: unknown = data;
      let reply = current?.reply;
      if (reply) result(reply);
      if (native === undefined && selected.artifactId) {
        reply = await client.api.GET("/api/v1/artifacts/{artifactId}/content", {
          params: { path: { artifactId: selected.artifactId } },
          parseAs: "arrayBuffer",
          signal: controller.signal,
        });
        const bytes = result(reply);
        if (
          bytes.byteLength > mcpExportByteLimit ||
          bytes.byteLength !== selected.size ||
          reply.response.headers.get("Content-Type")?.trim() !==
            selected.mediaType.trim() ||
          !/^\d+$/.test(reply.response.headers.get("Content-Length") ?? "") ||
          Number(reply.response.headers.get("Content-Length")) !==
            selected.size ||
          reply.response.headers.get("Content-Encoding") ||
          selected.contentDigest !== `sha256:${selected.artifactId}`
        )
          throw new Error(
            "The retained document's type, size or checksum does not match the selected representation.",
          );
        await verifyArtifact(bytes, reply.response, selected.artifactId);
        result(reply);
        native = parseNativeJSONObject(
          new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
            bytes,
          ),
          mcpExportByteLimit,
        );
      }
      if (controller.signal.aborted || client.signal?.aborted) return;
      if (reply) result(reply);
      setSource({
        representation: selected,
        data: native,
        ...(reply ? { reply } : {}),
      });
      const options = mcpRemoteChoices(native, selected);
      if (!position && options.length > 1) {
        setError("Choose a remote connection, then prepare the configuration.");
        return;
      }
      const value = projectMCPConfiguration(
        native,
        selected,
        record.release.id,
        format,
        position,
      );
      if (new TextEncoder().encode(value.text).length > mcpExportByteLimit)
        throw new Error(
          "The configuration exceeds the browser download limit. Use the CLI instead.",
        );
      attachment.current = URL.createObjectURL(
        new Blob([value.text], { type: "application/json" }),
      );
      setPrepared(value);
      setHref(attachment.current);
      requestAnimationFrame(() => {
        if (!controller.signal.aborted) preview.current?.focus();
      });
    } catch (cause) {
      if (controller.signal.aborted || client.signal?.aborted) return;
      setError(
        cause instanceof Error && !(cause instanceof TypeError)
          ? cause.message
          : "Configuration could not be prepared. Check the native document or try again.",
      );
      onFailure(cause);
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };

  if (invalidated) return null;

  return (
    <section
      className="workspace-panel mcp-client-export"
      aria-label="MCP client configuration"
    >
      <Heading>MCP client configuration</Heading>
      <p>
        Download one remote connection for your client. Configure its
        authentication and certificate trust separately.
      </p>
      <div className="workspace-toolbar">
        {representations.length > 1 && (
          <Field label="MCP representation">
            <select
              value={representationID}
              onChange={(event) => {
                clear();
                setSource(undefined);
                setPosition(0);
                setRepresentationID(event.target.value);
              }}
            >
              <option value="">Choose a representation</option>
              {representations.map((rep, index) => (
                <option key={rep.id} value={rep.id}>
                  Representation {index + 1}: {rep.mediaType}
                </option>
              ))}
            </select>
          </Field>
        )}
        <Field label="MCP client">
          <select
            value={format}
            onChange={(event) => {
              clear();
              setFormat(event.target.value as MCPClientFormat);
            }}
          >
            {Object.entries(mcpClientFormats).map(([value, item]) => (
              <option key={value} value={value}>
                {item.label}
              </option>
            ))}
          </select>
        </Field>
        {choices.length > 0 && (
          <Field label="Remote connection">
            <select
              value={position || (choices.length === 1 ? 1 : 0)}
              onChange={(event) => {
                clear();
                setPosition(Number(event.target.value));
              }}
            >
              <option value={0}>Choose a connection</option>
              {choices.map((choice) => (
                <option
                  key={choice.position}
                  value={choice.position}
                  disabled={!!choice.reason}
                >
                  {choice.label}
                  {choice.reason
                    ? ` - ${choice.reason}`
                    : ` - ${choice.url.slice(0, 160)}`}
                </option>
              ))}
            </select>
          </Field>
        )}
      </div>
      {unavailable && <p className="notice">{unavailable}</p>}
      {!data && selected?.artifactId && (
        <p className="muted">
          Prepare to read the stored document and inspect its connections. The
          server checks your current content permission.
        </p>
      )}
      <div className="workspace-actions">
        <button
          type="button"
          disabled={busy || !selected || !!unavailable}
          onClick={() => {
            void prepare();
          }}
        >
          {controlIcons["download"]}
          {busy ? "Preparing configuration..." : "Prepare client configuration"}
        </button>
      </div>
      {error && <p role="alert">{error}</p>}
      {prepared && href && (
        <div
          ref={preview}
          tabIndex={-1}
          className="mcp-configuration-preview"
          role="group"
          aria-label="Prepared MCP configuration"
        >
          <p>
            Place or merge this entry in{" "}
            <code>{mcpClientFormats[format].path}</code>. Rename its server key
            if needed.
          </p>
          {format === "mcp-claude-code" && (
            <p>
              Rename the downloaded <code>mcp.json</code> to{" "}
              <code>.mcp.json</code> in your project.
            </p>
          )}
          <p className="notice">
            Loading this file in your client can start a connection. The Catalog
            does not contact or run the MCP Server.
          </p>
          {prepared.omittedHeaders.length > 0 && (
            <p>
              Header values are omitted. Set these in your client:{" "}
              {prepared.omittedHeaders
                .map(
                  (header) =>
                    `${header.name}${header.required ? " (required)" : " (optional)"}`,
                )
                .join(", ")}
              .
            </p>
          )}
          <p className="muted">
            Check the server's sign-in requirements, even if no headers are
            listed. Review the endpoint before sharing this file.
          </p>
          <CodeBlock text={prepared.text} original />
          <a className="button" href={href} download="mcp.json">
            {controlIcons["download"]}Download client configuration
          </a>
        </div>
      )}
    </section>
  );
}
