import {
  AgentCard,
  Message,
  Task,
  StreamResponse,
  SendMessageRequest,
} from "@a2a-js/sdk";
import { Client, JsonRpcTransportFactory } from "@a2a-js/sdk/client";
import { ClientError, record, safeURL, type Connection } from "./config.js";
import {
  a2aEvent,
  a2aLimits,
  a2aTerminal,
  a2aInterrupted,
  type A2APlan,
  type A2AInterface,
  type A2ASettings,
  type A2APort,
} from "./a2a.js";
import { uniqueJSON } from "./json.js";
import { transport } from "./transport.js";

type Network = typeof transport;
function envelope(
  raw: string,
  id: unknown,
  task: boolean,
): Record<string, unknown> {
  let value: Record<string, unknown>;
  try {
    value = record(uniqueJSON(raw, "invalid_a2a_response"));
  } catch {
    throw new ClientError("invalid_a2a_response");
  }
  if (
    value.jsonrpc !== "2.0" ||
    value.id !== id ||
    (value.result === undefined) === (value.error === undefined)
  )
    throw new ClientError("invalid_a2a_response");
  if (value.error !== undefined) throw new ClientError("a2a_remote_refusal");
  a2aEvent(task ? { task: value.result } : value.result);
  return value;
}

/** Parse complete SSE frames before the SDK performs its typed conversion. */
export async function* a2aFrames(
  body: ReadableStream<Uint8Array>,
  id: unknown,
  signal?: AbortSignal,
): AsyncGenerator<string> {
  const reader = body.getReader(),
    decoder = new TextDecoder("utf-8", { fatal: true });
  const abort = () => {
    void reader.cancel().catch(() => {});
  };
  signal?.addEventListener("abort", abort, { once: true });
  let pending = "",
    data: string[] = [],
    total = 0,
    count = 0,
    frameBytes = 0;
  const line = (value: string) => {
    frameBytes += Buffer.byteLength(value) + 1;
    if (frameBytes > a2aLimits.event) throw new ClientError("a2a_event_limit");
    if (value.startsWith("data:")) data.push(value.slice(5).replace(/^ /u, ""));
  };
  const frame = () => {
    frameBytes = 0;
    if (!data.length) return undefined;
    const raw = data.join("\n");
    data = [];
    if (++count > a2aLimits.events) throw new ClientError("a2a_event_limit");
    envelope(raw, id, false);
    return `data: ${raw}\n\n`;
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (value) {
        total += value.byteLength;
        if (total > a2aLimits.stream) throw new ClientError("a2a_stream_limit");
      }
      pending += decoder.decode(value, { stream: !done });
      for (;;) {
        const match = /\r\n|\r|\n/u.exec(pending);
        if (
          !match ||
          (!done && match[0] === "\r" && match.index === pending.length - 1)
        )
          break;
        const value = pending.slice(0, match.index);
        pending = pending.slice(match.index + match[0].length);
        if (value === "") {
          const next = frame();
          if (next) yield next;
        } else line(value);
      }
      if (Buffer.byteLength(pending) + frameBytes > a2aLimits.event)
        throw new ClientError("a2a_event_limit");
      if (done) {
        // SSE dispatch requires a blank line. EOF discards an unfinished event.
        return;
      }
    }
  } finally {
    signal?.removeEventListener("abort", abort);
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}
function responseStream(
  frames: AsyncGenerator<string>,
  abort: AbortController,
): ReadableStream<Uint8Array> {
  return new ReadableStream({
    async pull(controller) {
      try {
        const next = await frames.next();
        if (next.done) controller.close();
        else controller.enqueue(new TextEncoder().encode(next.value));
      } catch (error) {
        controller.error(error);
      }
    },
    async cancel() {
      abort.abort();
      await frames.return(undefined);
    },
  });
}

/** The selected endpoint and separately supplied target credentials are the entire authority. */
export function a2aClient(
  plan: A2APlan,
  selected: A2AInterface,
  settings: A2ASettings,
  headers: Record<string, string>,
  network: Network = transport,
): A2APort {
  if (!selected.url || selected.reason)
    throw new ClientError("a2a_interface_unavailable");
  const endpoint = safeURL(selected.url, settings.allowLoopbackHTTP, false);
  const connection: Connection = {
    id: "a2a",
    label: "A2A",
    origin: new URL(endpoint).origin,
    mode: "published",
    auth: "anonymous",
    allowLoopbackHTTP: settings.allowLoopbackHTTP,
    timeoutMs: settings.timeoutMs,
    requestBytes: 65536,
    responseBytes: a2aLimits.response,
    headerBytes: 32768,
    ...(settings.caFile ? { caFile: settings.caFile } : {}),
    ...(settings.proxyURL ? { proxyURL: settings.proxyURL } : {}),
  };
  const card = AgentCard.fromJSON({
    ...plan.card,
    supportedInterfaces: [
      {
        protocolBinding: "JSONRPC",
        protocolVersion: "1.0",
        url: endpoint,
        tenant: selected.tenant,
      },
    ],
  });
  async function operation<T>(
    stream: boolean,
    signal: AbortSignal,
    action: (client: Client, signal: AbortSignal) => Promise<T>,
  ): Promise<T> {
    const io = await network(connection),
      timeout = new AbortController();
    const timer = setTimeout(
      () => timeout.abort(),
      stream ? settings.streamTimeoutMs : settings.timeoutMs,
    );
    const combined = AbortSignal.any([signal, timeout.signal]);
    const fetchImpl: typeof fetch = async (input, init) => {
      if (
        String(input) !== endpoint ||
        init?.method !== "POST" ||
        typeof init.body !== "string" ||
        Buffer.byteLength(init.body) > 65536
      )
        throw new ClientError("a2a_request_refused");
      const request = record(JSON.parse(init.body));
      if (
        ![
          "SendMessage",
          "SendStreamingMessage",
          "GetTask",
          "CancelTask",
        ].includes(String(request.method))
      )
        throw new ClientError("a2a_request_refused");
      const response = await io.fetch(endpoint, {
        method: "POST",
        body: init.body,
        signal: combined,
        redirect: "manual",
        credentials: "omit",
        headers: {
          ...headers,
          "Content-Type": "application/json",
          Accept: stream ? "text/event-stream" : "application/json",
          "A2A-Version": "1.0",
        },
      });
      try {
        if (!response.ok || !response.body)
          throw new ClientError(
            [401, 403].includes(response.status)
              ? "a2a_auth_refused"
              : "a2a_http_failure",
          );
        const mime = response.headers
          .get("content-type")
          ?.split(";")[0]
          ?.trim()
          .toLowerCase();
        if (stream) {
          if (mime !== "text/event-stream")
            throw new ClientError("invalid_a2a_response");
          const stopped = new AbortController();
          return new Response(
            responseStream(
              a2aFrames(response.body, request.id, stopped.signal),
              stopped,
            ),
            { headers: { "content-type": "text/event-stream" } },
          );
        }
        if (mime !== "application/json")
          throw new ClientError("invalid_a2a_response");
        const reader = response.body.getReader(),
          chunks: Uint8Array[] = [];
        let length = 0;
        try {
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            length += value.byteLength;
            if (length > a2aLimits.response)
              throw new ClientError("a2a_response_limit");
            chunks.push(value);
          }
        } finally {
          await reader.cancel().catch(() => {});
          reader.releaseLock();
        }
        const raw = new TextDecoder("utf-8", { fatal: true }).decode(
          Buffer.concat(chunks),
        );
        envelope(
          raw,
          request.id,
          request.method === "GetTask" || request.method === "CancelTask",
        );
        return new Response(raw, {
          headers: { "content-type": "application/json" },
        });
      } catch (error) {
        await response.body?.cancel().catch(() => {});
        throw error;
      }
    };
    try {
      const wire = await new JsonRpcTransportFactory({ fetchImpl }).create(
        endpoint,
        card,
      );
      return await action(
        new Client(wire, card, {
          polling: true,
          acceptedOutputModes: ["text/plain"],
        }),
        combined,
      );
    } catch (error) {
      throw error instanceof ClientError
        ? error
        : new ClientError(
            combined.aborted ? "a2a_request_interrupted" : "a2a_request_failed",
          );
    } finally {
      clearTimeout(timer);
      await io.close();
    }
  }
  return {
    async send(prompt, messageId, context, task, stream, signal, event) {
      await operation(stream, signal, async (client, signal) => {
        const request = SendMessageRequest.fromJSON({
          message: {
            messageId,
            role: "ROLE_USER",
            parts: [{ text: prompt, mediaType: "text/plain" }],
            ...(context ? { contextId: context } : {}),
            ...(task ? { taskId: task } : {}),
          },
          configuration: {
            acceptedOutputModes: ["text/plain"],
            historyLength: 10,
            returnImmediately: true,
          },
          tenant: selected.tenant,
        });
        if (stream) {
          for await (const value of client.sendMessageStream(request, {
            signal,
          })) {
            const raw = StreamResponse.toJSON(value);
            event(raw);
            const e = a2aEvent(raw);
            if (
              e.type === "message" ||
              ((e.type === "task" || e.type === "statusUpdate") &&
                (a2aTerminal(e.state) || a2aInterrupted(e.state)))
            )
              break;
          }
        } else {
          const value = await client.sendMessage(request, { signal });
          event(
            "messageId" in value
              ? { message: Message.toJSON(value) }
              : { task: Task.toJSON(value) },
          );
        }
      });
    },
    async task(id, cancel, signal) {
      return operation(false, signal, async (client, signal) =>
        Task.toJSON(
          await (cancel
            ? client.cancelTask(
                { id, tenant: selected.tenant, metadata: {} },
                { signal },
              )
            : client.getTask(
                { id, tenant: selected.tenant, historyLength: 10 },
                { signal },
              )),
        ),
      );
    },
  };
}
