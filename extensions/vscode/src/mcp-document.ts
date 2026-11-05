import { ClientError, abortable, type Connection } from "./config.js";
import type { DocumentRef } from "./browse.js";
import { mcpURL } from "./mcp.js";
import { transport } from "./transport.js";

/** Only explicit consumer reads enter here. No Catalog credentials or CA cross origins. */
export async function mcpDocument(
  ref: DocumentRef,
  connection: Connection,
  confirm: (url: string) => Promise<boolean>,
  network = transport,
  signal?: AbortSignal,
): Promise<Uint8Array> {
  const check = () => {
    if (signal?.aborted) throw new ClientError("mcp_start_cancelled");
  };
  check();
  if (ref.bytes) {
    if (ref.bytes.length > 1024 ** 2)
      throw new ClientError("mcp_document_too_large");
    return ref.bytes;
  }
  const url = mcpURL(ref.url, connection.allowLoopbackHTTP);
  if (!(await abortable(confirm(url), signal, "mcp_start_cancelled")))
    throw new ClientError("mcp_start_cancelled");
  check();
  const settings = { ...connection, origin: new URL(url).origin };
  delete settings.caFile;
  delete settings.proxyURL;
  delete settings.oidc;
  const client = await network(settings);
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  try {
    check();
    const response = await client.fetch(url, {
      method: "GET",
      redirect: "manual",
      headers: { accept: "application/json" },
      signal: AbortSignal.any([
        AbortSignal.timeout(Math.min(connection.timeoutMs, 30000)),
        ...(signal ? [signal] : []),
      ]),
    });
    if (
      response.status !== 200 ||
      !/^application\/(?:[^;]+\+)?json(?:;|$)/iu.test(
        response.headers.get("content-type") ?? "",
      )
    ) {
      await response.body?.cancel();
      throw new ClientError("mcp_document_unavailable");
    }
    reader = response.body?.getReader();
    if (!reader) throw new ClientError("invalid_mcp_document");
    const chunks: Uint8Array[] = [];
    let size = 0;
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      size += chunk.value.length;
      if (size > 1024 ** 2) throw new ClientError("mcp_document_too_large");
      chunks.push(chunk.value);
    }
    return Buffer.concat(chunks, size);
  } catch (error) {
    check();
    if (error instanceof ClientError) throw error;
    throw new ClientError("mcp_document_unavailable");
  } finally {
    await reader?.cancel().catch(() => {});
    await client.close();
  }
}
