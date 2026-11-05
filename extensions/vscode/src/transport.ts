import { open } from "node:fs/promises";
import { rootCertificates } from "node:tls";
import { Agent, ProxyAgent, fetch as nodeFetch, type Dispatcher } from "undici";
import { ClientError, type Connection } from "./config.js";

export async function transport(
  connection: Connection,
): Promise<{ fetch: typeof fetch; close(): Promise<void> }> {
  let ca: string[] | undefined;
  if (connection.caFile) {
    const file = await open(connection.caFile, "r");
    let bytes: Buffer;
    try {
      const info = await file.stat();
      if (!info.isFile() || info.size > 1024 ** 2)
        throw new ClientError("invalid_trust_file");
      const buffer = Buffer.alloc(1024 ** 2 + 1);
      let used = 0;
      for (;;) {
        const { bytesRead } = await file.read(
          buffer,
          used,
          buffer.length - used,
          null,
        );
        used += bytesRead;
        if (used > 1024 ** 2) throw new ClientError("invalid_trust_file");
        if (!bytesRead) break;
      }
      bytes = buffer.subarray(0, used);
      if (!bytes.toString("utf8").includes("-----BEGIN CERTIFICATE-----"))
        throw new ClientError("invalid_trust_file");
    } finally {
      await file.close();
    }
    ca = [...rootCertificates, bytes.toString("utf8")];
  }
  const connect = {
    ...(ca ? { ca } : {}),
    rejectUnauthorized: true,
    allowH2: false,
  };
  const dispatcher: Dispatcher = connection.proxyURL
    ? new ProxyAgent({
        uri: connection.proxyURL,
        allowH2: false,
        maxHeaderSize: connection.headerBytes,
        requestTls: connect,
        proxyTls: connect,
      })
    : new Agent({
        connect,
        allowH2: false,
        maxHeaderSize: connection.headerBytes,
      });
  return {
    fetch: (async (input: string | URL | Request, init?: RequestInit) => {
      if (input instanceof Request) {
        return nodeFetch(input.url, {
          method: input.method,
          headers: Object.fromEntries(input.headers),
          signal: input.signal,
          redirect: input.redirect,
          ...(input.body
            ? { body: new Uint8Array(await input.arrayBuffer()) }
            : {}),
          dispatcher,
        });
      }
      return nodeFetch(String(input), { ...init, dispatcher } as Parameters<
        typeof nodeFetch
      >[1]);
    }) as typeof fetch,
    close: async () => {
      await dispatcher.destroy();
    },
  };
}
