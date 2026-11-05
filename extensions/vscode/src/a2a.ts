import { createHash, randomUUID } from "node:crypto";
import { isAbsolute } from "node:path";
import { ClientError, record, safeURL, text } from "./config.js";
import { uniqueJSON } from "./json.js";

export const a2aLimits = {
  prompt: 16384,
  response: 1024 ** 2,
  event: 256 * 1024,
  stream: 4 * 1024 ** 2,
  events: 1000,
  transcript: 2 * 1024 ** 2,
  messages: 200,
  artifacts: 64,
  readInterval: 1000,
};
export interface A2ASettings {
  timeoutMs: number;
  streamTimeoutMs: number;
  allowLoopbackHTTP: boolean;
  caFile?: string;
  proxyURL?: string;
}
export function a2aSettings(value: unknown = {}): A2ASettings {
  const r = record(value);
  if (
    Object.keys(r).some(
      (key) =>
        ![
          "timeoutMs",
          "streamTimeoutMs",
          "allowLoopbackHTTP",
          "caFile",
          "proxyURL",
        ].includes(key),
    )
  )
    throw new ClientError("invalid_a2a_settings");
  const number = (key: string, fallback: number, max: number) => {
    const n = r[key] === undefined ? fallback : r[key];
    if (!Number.isSafeInteger(n) || (n as number) < 1000 || (n as number) > max)
      throw new ClientError("invalid_a2a_settings");
    return n as number;
  };
  if (
    r.allowLoopbackHTTP !== undefined &&
    typeof r.allowLoopbackHTTP !== "boolean"
  )
    throw new ClientError("invalid_a2a_settings");
  const result: A2ASettings = {
    timeoutMs: number("timeoutMs", 30000, 120000),
    streamTimeoutMs: number("streamTimeoutMs", 120000, 300000),
    allowLoopbackHTTP: r.allowLoopbackHTTP === true,
  };
  if (r.caFile !== undefined) {
    result.caFile = text(r.caFile, 4096);
    if (!isAbsolute(result.caFile))
      throw new ClientError("invalid_a2a_settings");
  }
  if (r.proxyURL !== undefined) {
    const url = new URL(text(r.proxyURL, 2048));
    if (
      !["http:", "https:"].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.pathname !== "/" ||
      url.search ||
      url.hash ||
      url.port === "0" ||
      /[\s\\?#]/u.test(String(r.proxyURL))
    )
      throw new ClientError("invalid_a2a_settings");
    result.proxyURL = url.origin;
  }
  return result;
}
export interface A2ACredential {
  label: string;
  header: string;
  bearer: boolean;
}
export interface A2AAuth {
  label: string;
  inputs: A2ACredential[];
  reason?: string;
}
export interface A2AInterface {
  label: string;
  url?: string;
  tenant: string;
  reason?: string;
}
export interface A2APlan {
  name: string;
  version: string;
  identity: string;
  card: Record<string, unknown>;
  interfaces: A2AInterface[];
  auth: A2AAuth[];
  streaming: boolean;
  notices: string[];
}
function list(value: unknown, max = 100): unknown[] {
  if (!Array.isArray(value) || value.length > max)
    throw new ClientError("invalid_a2a_response");
  return value;
}
function optionalID(value: unknown): string | undefined {
  return value === undefined || value === "" ? undefined : text(value, 512);
}
export function a2aPlan(bytes: Uint8Array, settings: A2ASettings): A2APlan {
  if (bytes.length > a2aLimits.response)
    throw new ClientError("a2a_card_too_large");
  const card = record(
    uniqueJSON(
      new TextDecoder("utf-8", { fatal: true }).decode(bytes),
      "invalid_a2a_card",
    ),
  );
  const caps = record(card.capabilities),
    notices: string[] = [];
  for (const key of ["streaming", "extendedAgentCard", "pushNotifications"])
    if (caps[key] !== undefined && typeof caps[key] !== "boolean")
      throw new ClientError("invalid_a2a_card");
  const requiredExtension = list(caps.extensions ?? [], 20).some((value) => {
    const e = record(value);
    text(e.uri, 2048);
    if (e.required !== undefined && typeof e.required !== "boolean")
      throw new ClientError("invalid_a2a_card");
    return e.required === true;
  });
  const textModes = [card.defaultInputModes, card.defaultOutputModes].every(
    (value) => list(value, 20).includes("text/plain"),
  );
  if (caps.extendedAgentCard)
    notices.push(
      "Only the retained base Card is used. Authenticated extended Cards are unavailable.",
    );
  notices.push(
    "Text only. Files, push notifications, arbitrary OAuth and required protocol extensions are unavailable. Card signatures are not verified by this client.",
  );
  const interfaces = list(card.supportedInterfaces, 20).map(
    (value): A2AInterface => {
      const i = record(value),
        label = `${text(i.protocolBinding, 128)} ${text(i.protocolVersion, 32)}: ${text(i.url, 2048)}`;
      const tenant = optionalID(i.tenant) ?? "";
      if (i.protocolBinding !== "JSONRPC" || i.protocolVersion !== "1.0")
        return {
          label,
          tenant,
          reason: "Only A2A 1.0 JSONRPC text is supported.",
        };
      if (!textModes || requiredExtension)
        return {
          label,
          tenant,
          reason: "Plain text modes and no required extensions are necessary.",
        };
      try {
        return {
          label,
          tenant,
          url: safeURL(i.url, settings.allowLoopbackHTTP, false),
        };
      } catch {
        return {
          label,
          tenant,
          reason:
            "Use HTTPS, or explicitly enable literal loopback HTTP in A2A settings. Credentials, queries and fragments are refused.",
        };
      }
    },
  );
  if (!interfaces.length) throw new ClientError("invalid_a2a_card");
  const schemes = record(card.securitySchemes ?? {});
  const requirements = list(card.securityRequirements ?? [], 20);
  const auth: A2AAuth[] = (
    requirements.length ? requirements : [{ schemes: {} }]
  ).map((value) => {
    const requested = record(record(value).schemes),
      inputs: A2ACredential[] = [];
    let reason: string | undefined;
    if (Object.keys(requested).length > 10)
      throw new ClientError("invalid_a2a_card");
    for (const [key, scopes] of Object.entries(requested)) {
      text(key, 128);
      list(record(scopes).list ?? [], 20).forEach((scope) => text(scope, 256));
      const scheme = record(schemes[key]);
      if (
        Object.keys(scheme).filter((k) => /SecurityScheme$/u.test(k)).length !==
        1
      )
        throw new ClientError("invalid_a2a_card");
      if (
        scheme.httpAuthSecurityScheme !== undefined &&
        String(record(scheme.httpAuthSecurityScheme).scheme).toLowerCase() ===
          "bearer"
      )
        inputs.push({ label: key, header: "Authorization", bearer: true });
      else if (scheme.apiKeySecurityScheme !== undefined) {
        const api = record(scheme.apiKeySecurityScheme),
          header = text(api.name, 128);
        if (
          api.location !== "header" ||
          !/^[A-Za-z][A-Za-z0-9-]*$/u.test(header) ||
          /^(?:authorization|cookie|host|content-.+|accept|a2a-.+|proxy-.+|connection|transfer-encoding|te|trailer|upgrade|origin|referer|user-agent|accept-encoding)$/iu.test(
            header,
          )
        )
          reason =
            "Only non-reserved header API keys are supported; query and cookie credentials are unavailable.";
        else inputs.push({ label: key, header, bearer: false });
      } else
        reason =
          "This authentication scheme is unavailable. No automatic OAuth, OIDC, Basic or mutual-TLS login is performed.";
    }
    if (
      new Set(inputs.map((i) => i.header.toLowerCase())).size !== inputs.length
    )
      reason = "Conflicting target credential headers are unavailable.";
    return {
      label: Object.keys(requested).join(" + ") || "Anonymous",
      inputs,
      ...(reason ? { reason } : {}),
    };
  });
  return {
    name: text(card.name, 256),
    version: text(card.version, 128),
    card,
    interfaces,
    auth,
    streaming: caps.streaming === true,
    notices,
    identity: createHash("sha256").update(bytes).digest("hex"),
  };
}
export function a2aHeaders(
  auth: A2AAuth,
  values: string[],
): Record<string, string> {
  if (auth.reason || values.length !== auth.inputs.length)
    throw new ClientError("a2a_auth_unavailable");
  const headers: Record<string, string> = {};
  auth.inputs.forEach((input, index) => {
    const value = text(values[index], 8192);
    if (value.trim() !== value || !/^[\x21-\x7e]+$/u.test(value))
      throw new ClientError("invalid_a2a_credential");
    headers[input.header] = (input.bearer ? "Bearer " : "") + value;
  });
  return headers;
}
const states = [
  "unspecified",
  "submitted",
  "working",
  "completed",
  "failed",
  "canceled",
  "input-required",
  "rejected",
  "authentication-required",
] as const;
export type A2AState = (typeof states)[number];
export function a2aState(value: unknown): A2AState {
  const n =
    typeof value === "number"
      ? value
      : [
          "TASK_STATE_UNSPECIFIED",
          "TASK_STATE_SUBMITTED",
          "TASK_STATE_WORKING",
          "TASK_STATE_COMPLETED",
          "TASK_STATE_FAILED",
          "TASK_STATE_CANCELED",
          "TASK_STATE_INPUT_REQUIRED",
          "TASK_STATE_REJECTED",
          "TASK_STATE_AUTH_REQUIRED",
        ].indexOf(String(value));
  if (!Number.isInteger(n) || n < 1 || !states[n])
    throw new ClientError("invalid_a2a_state");
  return states[n]!;
}
export const a2aTerminal = (state: string) =>
  ["completed", "failed", "canceled", "rejected"].includes(state);
export const a2aInterrupted = (state: string) =>
  ["input-required", "authentication-required"].includes(state);
export interface A2AMessage {
  id: string;
  role: "user" | "agent";
  text: string;
  context?: string;
  task?: string;
}
export interface A2AArtifact {
  id: string;
  name: string;
  parts: string[];
  last: boolean;
}
function parts(value: unknown): string[] {
  const items = list(value, 128);
  if (!items.length) throw new ClientError("invalid_a2a_response");
  return items.map((entry) => {
    const p = record(entry);
    if (
      typeof p.text !== "string" ||
      ["raw", "url", "data"].some((key) => p[key] !== undefined) ||
      (p.mediaType !== undefined &&
        p.mediaType !== "" &&
        p.mediaType !== "text/plain")
    )
      throw new ClientError("a2a_non_text_response");
    if (Buffer.byteLength(p.text) > a2aLimits.response)
      throw new ClientError("a2a_response_limit");
    return p.text;
  });
}
function parsedMessage(value: unknown): A2AMessage {
  const m = record(value);
  if (!["ROLE_USER", "ROLE_AGENT", 1, 2].includes(m.role as string | number))
    throw new ClientError("invalid_a2a_response");
  const context = optionalID(m.contextId),
    task = optionalID(m.taskId);
  return {
    id: text(m.messageId),
    role: m.role === "ROLE_USER" || m.role === 1 ? "user" : "agent",
    text: parts(m.parts).join(""),
    ...(context ? { context } : {}),
    ...(task ? { task } : {}),
  };
}
function parsedArtifact(value: unknown): A2AArtifact {
  const a = record(value);
  return {
    id: text(a.artifactId),
    name:
      a.name === undefined || a.name === "" ? "Artifact" : text(a.name, 256),
    parts: parts(a.parts),
    last: false,
  };
}
export type A2AEvent =
  | { type: "message"; message: A2AMessage }
  | {
      type: "task";
      id: string;
      context: string;
      state: A2AState;
      messages: A2AMessage[];
      artifacts: A2AArtifact[];
    }
  | {
      type: "statusUpdate";
      id: string;
      context: string;
      state: A2AState;
      messages: A2AMessage[];
    }
  | {
      type: "artifactUpdate";
      id: string;
      context: string;
      artifact: A2AArtifact;
      append: boolean;
    };
export function a2aEvent(value: unknown, stream = true): A2AEvent {
  const envelope = record(value),
    keys = [
      "task",
      "message",
      ...(stream ? ["statusUpdate", "artifactUpdate"] : []),
    ].filter((key) => envelope[key] !== undefined);
  if (keys.length !== 1) throw new ClientError("invalid_a2a_response");
  const type = keys[0]!;
  const r = record(envelope[type]);
  if (type === "message") {
    const message = parsedMessage(r);
    if (message.role !== "agent" || !message.context)
      throw new ClientError("invalid_a2a_response");
    return { type, message };
  }
  const id = text(type === "task" ? r.id : r.taskId),
    context = text(r.contextId);
  if (type === "artifactUpdate") {
    for (const key of ["append", "lastChunk"])
      if (r[key] !== undefined && typeof r[key] !== "boolean")
        throw new ClientError("invalid_a2a_response");
    const artifact = parsedArtifact(r.artifact);
    artifact.last = r.lastChunk === true;
    return { type, id, context, artifact, append: r.append === true };
  }
  const status = record(r.status),
    state = a2aState(status.state);
  const messages = [
    ...(type === "task"
      ? list(r.history ?? [], a2aLimits.messages).map(parsedMessage)
      : []),
    ...(status.message === undefined ? [] : [parsedMessage(status.message)]),
  ];
  if (
    messages.some(
      (m) => (m.context && m.context !== context) || (m.task && m.task !== id),
    )
  )
    throw new ClientError("a2a_identity_mismatch");
  if (type === "task") {
    const artifacts = list(r.artifacts ?? [], a2aLimits.artifacts).map(
      parsedArtifact,
    );
    if (new Set(artifacts.map((a) => a.id)).size !== artifacts.length)
      throw new ClientError("invalid_a2a_response");
    return { type, id, context, state, messages, artifacts };
  }
  return { type: "statusUpdate", id, context, state, messages };
}
export interface A2ASnapshot {
  status: A2AState | "ready";
  activity?: "preparing" | "sending" | "streaming" | "reading";
  task?: string;
  context?: string;
  messages: A2AMessage[];
  artifacts: A2AArtifact[];
  busy: boolean;
  connected: boolean;
  uncertain: boolean;
  error?: string;
}
export interface A2APort {
  send(
    prompt: string,
    messageId: string,
    context: string | undefined,
    task: string | undefined,
    stream: boolean,
    signal: AbortSignal,
    event: (value: unknown) => void,
  ): Promise<void>;
  task(id: string, cancel: boolean, signal: AbortSignal): Promise<unknown>;
}
export function a2aPrompt(prompt: unknown): asserts prompt is string {
  if (
    typeof prompt !== "string" ||
    !prompt.trim() ||
    Buffer.byteLength(prompt) > a2aLimits.prompt ||
    prompt.includes("\0") ||
    /[\ud800-\udfff]/u.test(prompt)
  )
    throw new ClientError("invalid_a2a_prompt");
}

/** One explicit conversation; no retries, automatic polling or persistent transcript. */
export class A2AConversation {
  state: A2ASnapshot = {
    status: "ready",
    messages: [],
    artifacts: [],
    busy: false,
    connected: true,
    uncertain: false,
  };
  private abort = new AbortController();
  private generation = 0;
  private activeSend: AbortController | undefined;
  private readAt = -Infinity;
  constructor(
    readonly plan: A2APlan,
    private port: A2APort,
    private authorize: () => Promise<void>,
    private guard: () => void,
    private changed: () => void = () => {},
    private now = () => performance.now(),
  ) {}
  private apply(value: unknown, first: boolean): void {
    const event = a2aEvent(value);
    if (first && !["message", "task"].includes(event.type))
      throw new ClientError("invalid_a2a_stream_start");
    const next = structuredClone(this.state);
    const add = (message: A2AMessage) => {
      const old = next.messages.find((m) => m.id === message.id);
      if (old && (old.text !== message.text || old.role !== message.role))
        throw new ClientError("a2a_identity_mismatch");
      if (!old) next.messages.push(message);
    };
    const context =
      event.type === "message" ? event.message.context : event.context;
    const task = event.type === "message" ? event.message.task : event.id;
    if (
      (next.context && context !== next.context) ||
      (next.task && task !== next.task)
    )
      throw new ClientError("a2a_identity_mismatch");
    if (context) next.context = context;
    if (task) next.task = task;
    if (event.type === "message") {
      add(event.message);
      next.status = "completed";
    } else if (event.type === "artifactUpdate") {
      const index = next.artifacts.findIndex((a) => a.id === event.artifact.id),
        previous = next.artifacts[index];
      if (previous?.last || (event.append && !previous))
        throw new ClientError("invalid_a2a_artifact_sequence");
      const artifact = {
        ...event.artifact,
        parts: [
          ...(event.append ? previous!.parts : []),
          ...event.artifact.parts,
        ],
      };
      if (index < 0) next.artifacts.push(artifact);
      else next.artifacts[index] = artifact;
    } else {
      if (a2aTerminal(next.status) && next.status !== event.state)
        throw new ClientError("invalid_a2a_transition");
      next.status = event.state;
      event.messages.forEach(add);
      if (event.type === "task") next.artifacts = event.artifacts;
    }
    if (
      next.messages.length > a2aLimits.messages ||
      next.artifacts.length > a2aLimits.artifacts ||
      Buffer.byteLength(JSON.stringify(next)) > a2aLimits.transcript
    )
      throw new ClientError("a2a_transcript_limit");
    this.state = next;
    this.changed();
  }
  async send(prompt: string, stream = false): Promise<void> {
    a2aPrompt(prompt);
    if (stream && !this.plan.streaming)
      throw new ClientError("a2a_streaming_unavailable");
    if (
      this.state.uncertain ||
      ![
        "ready",
        "completed",
        "failed",
        "canceled",
        "rejected",
        "input-required",
      ].includes(this.state.status)
    )
      throw new ClientError("a2a_task_not_ready");
    await this.operation(
      async (signal, check, sent) => {
        const next = structuredClone(this.state),
          continuing = next.status === "input-required";
        const task = continuing ? next.task : undefined;
        if (!continuing) {
          delete next.task;
          next.artifacts = [];
        }
        next.status = "working";
        const messageId = randomUUID();
        next.messages.push({ id: messageId, role: "user", text: prompt });
        if (
          Buffer.byteLength(JSON.stringify(next)) > a2aLimits.transcript ||
          next.messages.length > a2aLimits.messages
        )
          throw new ClientError("a2a_transcript_limit");
        this.state = next;
        this.changed();
        let first = true;
        const sending = new AbortController();
        this.activeSend = sending;
        sent();
        await this.port.send(
          prompt,
          messageId,
          this.state.context,
          task,
          stream,
          AbortSignal.any([signal, sending.signal]),
          (value) => {
            check();
            if (this.state.status !== "canceled") this.apply(value, first);
            first = false;
          },
        );
        this.activeSend = undefined;
        check();
        if (first) throw new ClientError("a2a_empty_response");
        if (
          stream &&
          !a2aTerminal(this.state.status) &&
          !a2aInterrupted(this.state.status)
        )
          throw new ClientError("a2a_stream_interrupted");
      },
      true,
      stream ? "streaming" : "sending",
    );
  }
  async read(): Promise<void> {
    if (!this.state.task) throw new ClientError("a2a_task_required");
    if (this.now() - this.readAt < a2aLimits.readInterval)
      throw new ClientError("a2a_read_rate_limit");
    this.readAt = this.now();
    await this.operation(async (signal, check) => {
      const value = await this.port.task(this.state.task!, false, signal);
      check();
      this.apply({ task: value }, false);
      this.state.uncertain = false;
    });
  }
  async cancel(): Promise<void> {
    this.guard();
    if (
      !this.state.connected ||
      !this.state.task ||
      a2aTerminal(this.state.status)
    )
      throw new ClientError("a2a_cancel_unavailable");
    if (this.canceling) throw new ClientError("a2a_busy");
    this.canceling = true;
    const generation = this.generation;
    try {
      await this.authorize();
      this.guard();
      if (generation !== this.generation)
        throw new ClientError("a2a_disconnected");
      const task = await this.port.task(
        this.state.task,
        true,
        this.abort.signal,
      );
      this.guard();
      if (generation !== this.generation)
        throw new ClientError("a2a_disconnected");
      this.apply({ task }, false);
      if (this.state.status !== "canceled")
        throw new ClientError("a2a_cancel_not_confirmed");
      this.state.uncertain = false;
      this.activeSend?.abort();
    } catch (error) {
      if (generation === this.generation)
        this.state.error =
          error instanceof ClientError ? error.code : "a2a_cancel_failed";
      throw error instanceof ClientError
        ? error
        : new ClientError("a2a_cancel_failed");
    } finally {
      this.canceling = false;
      this.changed();
    }
  }
  private canceling = false;
  private async operation(
    action: (
      signal: AbortSignal,
      check: () => void,
      sent: () => void,
    ) => Promise<void>,
    uncertain = false,
    activity: "sending" | "streaming" | "reading" = "reading",
  ): Promise<void> {
    this.guard();
    if (!this.state.connected) throw new ClientError("a2a_disconnected");
    if (this.state.busy || this.canceling) throw new ClientError("a2a_busy");
    const generation = this.generation;
    const check = () => {
      this.guard();
      if (generation !== this.generation || this.abort.signal.aborted)
        throw new ClientError("a2a_disconnected");
    };
    this.state.busy = true;
    this.state.activity = "preparing";
    delete this.state.error;
    this.changed();
    let started = false;
    try {
      await this.authorize();
      check();
      this.state.activity = activity;
      this.changed();
      await action(this.abort.signal, check, () => {
        started = true;
      });
    } catch (error) {
      if (generation === this.generation) {
        if (
          uncertain &&
          started &&
          this.state.status === "canceled" &&
          !this.state.uncertain
        )
          return;
        this.state.error =
          error instanceof ClientError ? error.code : "a2a_request_failed";
        if (uncertain && started) this.state.uncertain = true;
      }
      throw error instanceof ClientError
        ? error
        : new ClientError("a2a_request_failed");
    } finally {
      if (generation === this.generation) {
        this.activeSend = undefined;
        this.state.busy = false;
        delete this.state.activity;
        this.changed();
      }
    }
  }
  disconnect(clear = false): void {
    const sending = this.activeSend !== undefined;
    this.generation++;
    this.abort.abort();
    this.state.busy = false;
    delete this.state.activity;
    this.state.connected = false;
    if (sending) this.state.uncertain = true;
    if (clear)
      this.state = {
        status: "ready",
        messages: [],
        artifacts: [],
        busy: false,
        connected: false,
        uncertain: false,
      };
    this.changed();
  }
  export(): string {
    this.guard();
    return JSON.stringify({ agent: this.plan.name, ...this.state }, null, 2);
  }
}
