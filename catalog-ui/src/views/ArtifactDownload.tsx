// Retained downloads use the authenticated API and never follow representation URLs.
import { useEffect, useRef, useState } from "react";
import { controlIcons } from "../shell/Icon";
import { APIError, result } from "../api/client";
import type { Client } from "../api/client";

/** Fixed extensions never use resource names, response filenames or arbitrary subtype text. */
function extension(mediaType: string) {
  const extensions: Readonly<Record<string, string>> = {
    "application/json": "json",
    "application/a2a-agent-card+json": "json",
    "application/vnd.open-agentic-catalog.agent+json": "json",
    "application/mcp-server-card+json": "json",
    "application/vnd.mcp.server+json": "json",
    "application/vnd.open-agentic-catalog.mcp-registration+json": "json",
    "application/agent-skills+json": "json",
    "application/ld+json": "jsonld",
    "text/markdown": "md",
    "application/agent-skills+md": "md",
    "text/plain": "txt",
    "application/yaml": "yaml",
  };
  return extensions[mediaType.split(";", 1)[0]!.trim().toLowerCase()] ?? "bin";
}

/** Verify exact response bytes against both the response digest and content-addressed ID. */
export async function verifyArtifact(
  bytes: ArrayBuffer,
  response: Response,
  id: string,
) {
  const type = response.headers.get("Content-Type");
  if (!type || !/^[\w!#$&^_.+-]+\/[\w!#$&^_.+-]+(?:;[^\r\n]*)?$/.test(type))
    throw new Error("Missing or invalid document media type.");
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  const hex = Array.from(hash, (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
  const digest = `sha-256=:${btoa(String.fromCharCode(...hash))}:`;
  if (id !== hex || response.headers.get("Content-Digest") !== digest)
    throw new Error("Document checksum verification failed.");
  return new Blob([bytes], { type });
}

/** Fetch only on request; abort obsolete reads and revoke local attachments on replacement/unmount. */
export function ArtifactDownload({
  client,
  id,
  onFailure,
}: {
  client: Client;
  id: string;
  onFailure: (error: unknown) => void;
}) {
  const [href, setHref] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [mediaType, setMediaType] = useState("");
  const request = useRef<AbortController | undefined>(undefined);
  const objectURL = useRef("");
  useEffect(() => {
    setHref("");
    setError("");
    setBusy(false);
    setMediaType("");
    return () => {
      request.current?.abort();
      if (objectURL.current) URL.revokeObjectURL(objectURL.current);
      objectURL.current = "";
    };
  }, [client, id]);
  return (
    <div className="handoff-download">
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          void (async () => {
            request.current?.abort();
            const controller = new AbortController();
            request.current = controller;
            if (objectURL.current) URL.revokeObjectURL(objectURL.current);
            objectURL.current = "";
            setHref("");
            setMediaType("");
            setError("");
            setBusy(true);
            try {
              const reply = await client.api.GET(
                "/api/v1/artifacts/{artifactId}/content",
                {
                  params: { path: { artifactId: id } },
                  parseAs: "arrayBuffer",
                  signal: controller.signal,
                },
              );
              const bytes = result(reply);
              const blob = await verifyArtifact(bytes, reply.response, id);
              // Recheck session authority after asynchronous hashing, as well as body decoding.
              result(reply);
              if (controller.signal.aborted) return;
              objectURL.current = URL.createObjectURL(blob);
              setHref(objectURL.current);
              setMediaType(blob.type);
            } catch (cause) {
              if (controller.signal.aborted) return;
              setError(
                cause instanceof APIError
                  ? cause.message
                  : "Download could not be prepared or verified. Try again.",
              );
              onFailure(cause);
            } finally {
              if (!controller.signal.aborted) setBusy(false);
            }
          })();
        }}
      >
        {controlIcons["download"]}
        {busy ? "Preparing download..." : "Prepare retained document download"}
      </button>
      {error && <p role="alert">{error}</p>}
      {href && (
        <a
          className="button"
          href={href}
          download={`catalog-document-${id}.${extension(mediaType)}`}
        >
          {controlIcons["download"]}
          Save retained document
        </a>
      )}
      {href && (
        <p role="status">
          Exact bytes verified (SHA-256). Media type: <code>{mediaType}</code>.
        </p>
      )}
    </div>
  );
}
