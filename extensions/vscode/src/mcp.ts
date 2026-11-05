import { createHash } from "node:crypto";
import { ClientError, record, abortable } from "./config.js";
import { literal, paragraph, type DocumentRef } from "./browse.js";
import { uniqueJSON } from "./json.js";

export interface TargetInput {
  name: string;
  secret: boolean;
  required: boolean;
  value?: string;
  fallback?: string;
  choices?: string[];
  format: "string" | "number" | "boolean";
}
export interface McpRecipe {
  family: "Server Card" | "Registry server.json";
  name: string;
  version: string;
  type: "http" | "stdio";
  url?: string;
  command?: "npx" | "uvx" | "docker";
  args: string[];
  inputs: TargetInput[];
  description: string;
}
export interface McpAlternative {
  label: string;
  recipe?: McpRecipe;
  reason?: string;
}
const fail = (): never => {
  throw new ClientError("unsupported_mcp_recipe");
};
function array(value: unknown, max = 32): unknown[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > max) return fail();
  return value;
}
function scalar(value: unknown, max = 2048): string {
  const text = literal(value, max, true);
  // Templates are never silently substituted, including in flags and secrets.
  if (/[{}]/u.test(text)) return fail();
  return text;
}
export function mcpURL(value: unknown, allowLoopback = false): string {
  const text = scalar(value),
    url = new URL(text);
  if (
    url.username ||
    url.password ||
    url.hash ||
    /[\s\\]/u.test(text) ||
    (url.protocol !== "https:" &&
      !(
        allowLoopback &&
        url.protocol === "http:" &&
        ["127.0.0.1", "[::1]"].includes(url.hostname)
      ))
  )
    return fail();
  return url.href;
}
function inputs(value: unknown, headers: boolean): TargetInput[] {
  const seen = new Set<string>();
  return array(value).map((entry) => {
    const v = record(entry),
      name = scalar(v.name, 128);
    if (
      !(headers ? /^[!#$%&'*+.^_`|~0-9a-z-]+$/iu : /^[A-Z_][A-Z0-9_]*$/u).test(
        name,
      ) ||
      seen.has(name.toLowerCase())
    )
      return fail();
    seen.add(name.toLowerCase());
    if (
      headers
        ? /^(host|cookie|proxy-.*|connection|content-.*|transfer-encoding|mcp-.*|accept|origin)$/iu.test(
            name,
          )
        : /^(PATH|HOME|SHELL|USER|USERPROFILE|SYSTEMROOT|WINDIR|PATHEXT|COMSPEC|APPDATA|LOCALAPPDATA|TEMP|TMP|TMPDIR|NODE_.*|NPM_.*|PYTHON.*|UV_.*|LD_.*|DYLD_.*|DOCKER_.*|.*PROXY)$/iu.test(
            name,
          )
    )
      return fail();
    if (
      v.variables !== undefined ||
      ![undefined, "string", "number", "boolean"].includes(
        v.format as string | undefined,
      )
    )
      return fail();
    for (const key of ["isSecret", "isRequired"])
      if (v[key] !== undefined && typeof v[key] !== "boolean") return fail();
    const result: TargetInput = {
      name,
      secret:
        v.isSecret === true ||
        (headers && /^(authorization|x-api-key|api-key)$/iu.test(name)),
      required: v.isRequired === true,
      format: (v.format ?? "string") as TargetInput["format"],
    };
    if (v.value !== undefined) result.value = scalar(v.value, 8192);
    if (v.default !== undefined) result.fallback = scalar(v.default, 8192);
    if (v.choices !== undefined)
      result.choices = array(v.choices, 50).map((c) => scalar(c, 8192));
    // Never retain a descriptor-supplied secret as a reusable credential.
    if (
      result.secret &&
      (result.value !== undefined || result.fallback !== undefined)
    )
      return fail();
    if (result.value !== undefined) inputValue(result, result.value);
    if (result.fallback !== undefined) inputValue(result, result.fallback);
    return result;
  });
}
export function inputValue(input: TargetInput, value: string): string {
  // User-supplied credentials are literal values, not descriptor templates.
  literal(value, 8192, true);
  if (
    (!value && input.required) ||
    (value && input.choices && !input.choices.includes(value)) ||
    (value &&
      input.format === "number" &&
      !/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/u.test(value)) ||
    (value && input.format === "boolean" && !["true", "false"].includes(value))
  )
    throw new ClientError("invalid_mcp_input");
  return value;
}
function packageArgs(value: unknown): string[] {
  return array(value, 64).flatMap((entry) => {
    const v = record(entry);
    if (
      v.variables !== undefined ||
      v.isSecret === true ||
      v.isRepeated === true ||
      v.value === undefined
    )
      return fail();
    const value = scalar(v.value);
    if (v.type === "positional") return [value];
    if (
      v.type !== "named" ||
      !/^--?[a-zA-Z][a-zA-Z0-9-]*$/u.test(scalar(v.name, 100))
    )
      return fail();
    // Keep the value in the same argv entry. It cannot become a runner option.
    return [`${String(v.name)}=${value}`];
  });
}
export function mcpAlternatives(
  bytes: Uint8Array,
  allowLoopback = false,
  mediaType?: string,
): McpAlternative[] {
  if (bytes.length > 1024 ** 2) throw new ClientError("mcp_document_too_large");
  let v: Record<string, unknown>;
  try {
    v = record(
      uniqueJSON(new TextDecoder("utf-8", { fatal: true }).decode(bytes)),
    );
  } catch {
    throw new ClientError("invalid_mcp_document");
  }
  const schema = v.$schema;
  const card =
    schema ===
    "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json";
  const registry =
    (schema === undefined && mediaType === "application/vnd.mcp.server+json") ||
    (typeof schema === "string" &&
      /^https:\/\/static\.modelcontextprotocol\.io\/schemas\/2025-12-11\/server\.schema\.json$/u.test(
        schema,
      ));
  if (!card && !registry) throw new ClientError("unknown_mcp_document_family");
  if (
    (mediaType === "application/mcp-server-card+json" && !card) ||
    (mediaType === "application/vnd.mcp.server+json" && !registry)
  )
    throw new ClientError("invalid_mcp_document");
  if (!paragraph(v.description, 100).trim())
    throw new ClientError("invalid_mcp_document");
  const name = literal(v.name, 200),
    version = literal(v.version, 255);
  if (!/^[a-zA-Z0-9.-]+\/[a-zA-Z0-9._-]+$/u.test(name))
    throw new ClientError("invalid_mcp_document");
  const base = {
    family: card ? ("Server Card" as const) : ("Registry server.json" as const),
    name,
    version,
    args: [],
    inputs: [],
  };
  const alternatives: McpAlternative[] = [];
  const add = (label: string, build: () => McpRecipe, reason: string) => {
    try {
      alternatives.push({ label, recipe: build() });
    } catch {
      alternatives.push({
        label,
        reason,
      });
    }
  };
  for (const [index, entry] of array(v.remotes).entries())
    add(
      `Remote ${index + 1}`,
      () => {
        const remote = record(entry);
        if (remote.type !== "streamable-http" || remote.variables !== undefined)
          return fail();
        const url = mcpURL(remote.url, allowLoopback);
        return {
          ...base,
          type: "http",
          url,
          inputs: inputs(remote.headers, true),
          description: `Streamable HTTP: ${url}`,
        };
      },
      remoteRefusal(entry),
    );
  if (registry)
    for (const [index, entry] of array(v.packages).entries())
      add(
        `Package ${index + 1}`,
        () => {
          const p = record(entry),
            type = p.registryType,
            identifier = scalar(p.identifier, 512);
          if (
            record(p.transport).type !== "stdio" ||
            p.fileSha256 !== undefined ||
            array(p.runtimeArguments).length
          )
            return fail();
          const command =
            type === "npm"
              ? "npx"
              : type === "pypi"
                ? "uvx"
                : type === "oci"
                  ? "docker"
                  : fail();
          if (p.runtimeHint !== undefined && p.runtimeHint !== command)
            return fail();
          if (
            p.registryBaseUrl !== undefined &&
            p.registryBaseUrl !==
              (
                {
                  npm: "https://registry.npmjs.org",
                  pypi: "https://pypi.org",
                  oci: "https://docker.io",
                } as Record<string, string>
              )[String(type)]
          )
            return fail();
          const declaredVersion =
            p.version === undefined ? "" : scalar(p.version, 128);
          let args: string[];
          if (type === "oci") {
            if (
              !/^(?:[a-z0-9.-]+(?::[0-9]+)?\/)?[a-z0-9/_-]+@sha256:[a-f0-9]{64}$/u.test(
                identifier,
              )
            )
              return fail();
            args = ["run", "--rm", "-i", identifier];
          } else {
            if (
              type === "npm"
                ? !/^(?:@[a-z0-9._-]+\/)?[a-z0-9][a-z0-9._-]*$/u.test(
                    identifier,
                  )
                : !/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/u.test(identifier)
            )
              return fail();
            if (
              !/^\d+\.\d+\.\d+(?:[-+][a-zA-Z0-9.-]+)?$/u.test(declaredVersion)
            )
              return fail();
            args =
              type === "npm"
                ? ["--yes", "--", `${identifier}@${declaredVersion}`]
                : [`${identifier}==${declaredVersion}`];
          }
          const env = inputs(p.environmentVariables, false);
          if (command === "docker")
            args.splice(3, 0, ...env.flatMap((i) => ["--env", i.name]));
          args.push(...packageArgs(p.packageArguments));
          return {
            ...base,
            type: "stdio",
            command,
            args,
            inputs: env,
            description: `${command} arguments: ${JSON.stringify(args)} (runtime must already exist; may download and execute third-party code)`,
          };
        },
        packageRefusal(entry),
      );
  if (!alternatives.length) throw new ClientError("no_mcp_alternatives");
  return alternatives;
}
function remoteRefusal(value: unknown): string {
  try {
    const remote = record(value);
    if (remote.type !== "streamable-http")
      return "Inspect-only: this remote does not declare Streamable HTTP. Legacy SSE is unsupported.";
    if (remote.variables !== undefined)
      return "Inspect-only: templated remote variables are unsupported.";
    return "Inspect-only: the remote requires a safe credential-free HTTPS URL (or explicitly allowed literal loopback HTTP) and supported target headers.";
  } catch {
    return "Inspect-only: the remote declaration is incomplete or invalid.";
  }
}
function packageRefusal(value: unknown): string {
  try {
    const p = record(value);
    if (!["npm", "pypi", "oci"].includes(String(p.registryType)))
      return "Inspect-only: only npm, PyPI and OCI package recipes are supported.";
    if (record(p.transport).type !== "stdio")
      return "Inspect-only: package recipes require stdio transport.";
    if (p.fileSha256 !== undefined)
      return "Inspect-only: this recipe cannot verify the declared fileSha256.";
    if (array(p.runtimeArguments).length)
      return "Inspect-only: runtime arguments and custom runners are unsupported.";
    if (p.runtimeHint !== undefined || p.registryBaseUrl !== undefined)
      return "Inspect-only: check the declared runner and registry base; custom runners and registry overrides are unsupported.";
    return "Inspect-only: require an exact package version or OCI digest, static non-secret arguments and supported environment inputs.";
  } catch {
    return "Inspect-only: the package declaration is incomplete or invalid.";
  }
}
export const mcpDigest = (bytes: Uint8Array): string =>
  createHash("sha256").update(bytes).digest("hex");

export interface McpPorts {
  trusted(): boolean;
  acquire(ref: DocumentRef, signal?: AbortSignal): Promise<Uint8Array>;
  confirm(recipe: McpRecipe, signal?: AbortSignal): Promise<boolean>;
  input(
    input: TargetInput,
    previous?: string,
    signal?: AbortSignal,
  ): Promise<string | undefined>;
  secrets: {
    get(key: string): PromiseLike<string | undefined>;
    store(key: string, value: string): PromiseLike<void>;
    delete(key: string): PromiseLike<void>;
    keys?(): PromiseLike<string[]>;
  };
  changed(): void;
}
export interface McpSelection {
  id: string;
  recipe: McpRecipe;
  digest: string;
  alternative: number;
  read(signal?: AbortSignal): Promise<DocumentRef>;
  guard(): void;
}
/** Host-independent state. Provision is inert; resolution is the only start gate. */
export class McpSelections {
  readonly selections = new Map<string, McpSelection>();
  private removals = new Map<string, string[]>();
  private writes: Promise<void> = Promise.resolve();
  private generation = 0;
  constructor(private ports: McpPorts) {}
  private write<T>(action: () => Promise<T>): Promise<T> {
    const next = this.writes.then(action);
    this.writes = next.then(
      () => {},
      () => {},
    );
    return next;
  }
  async add(
    read: (signal?: AbortSignal) => Promise<DocumentRef>,
    guard: () => void,
    choose: (values: McpAlternative[]) => Promise<number | undefined>,
    allowLoopback: boolean,
    scope: string,
    expected?: { digest: string; alternative: number },
  ): Promise<McpSelection | undefined> {
    const check = () => {
      guard();
      if (!this.ports.trusted())
        throw new ClientError("workspace_trust_required");
    };
    check();
    const ref = await read();
    check();
    const bytes = await this.ports.acquire(ref);
    check();
    const alternatives = mcpAlternatives(bytes, allowLoopback, ref.mediaType),
      index = await choose(alternatives);
    check();
    if (index === undefined) return;
    const recipe = alternatives[index]?.recipe;
    if (!recipe) throw new ClientError("unsupported_mcp_recipe");
    if (
      expected &&
      (expected.digest !== mcpDigest(bytes) || expected.alternative !== index)
    )
      throw new ClientError("mcp_reference_changed");
    if (!(await this.ports.confirm(recipe))) return;
    check();
    const digest = mcpDigest(bytes),
      id = mcpDigest(Buffer.from(JSON.stringify([scope, digest, index])));
    if (
      new Set([...this.selections.keys(), ...this.removals.keys()]).size >=
        20 &&
      !this.selections.has(id) &&
      !this.removals.has(id)
    )
      throw new ClientError("mcp_selection_limit");
    const selection = { id, recipe, digest, alternative: index, read, guard };
    this.selections.set(id, selection);
    this.ports.changed();
    return selection;
  }
  async resolve(
    id: string,
    cancellation: () => void = () => {},
    signal?: AbortSignal,
  ): Promise<{ recipe: McpRecipe; values: Record<string, string> }> {
    const selection = this.selections.get(id);
    if (!selection) throw new ClientError("selection_changed");
    const generation = this.generation;
    const check = () => {
      cancellation();
      if (signal?.aborted) throw new ClientError("mcp_start_cancelled");
      selection.guard();
      if (!this.ports.trusted())
        throw new ClientError("workspace_trust_required");
      if (
        generation !== this.generation ||
        this.selections.get(id) !== selection
      )
        throw new ClientError("selection_changed");
    };
    const verify = async () => {
      check();
      const ref = await abortable(
        selection.read(signal),
        signal,
        "mcp_start_cancelled",
      );
      check();
      const bytes = await abortable(
        this.ports.acquire(ref, signal),
        signal,
        "mcp_start_cancelled",
      );
      check();
      if (mcpDigest(bytes) !== selection.digest)
        throw new ClientError("selection_changed");
    };
    await verify();
    if (
      !(await abortable(
        this.ports.confirm(selection.recipe, signal),
        signal,
        "mcp_start_cancelled",
      ))
    )
      throw new ClientError("mcp_start_cancelled");
    check();
    const values: Record<string, string> = Object.create(null) as Record<
      string,
      string
    >;
    const credentials: [string, string][] = [];
    for (const input of selection.recipe.inputs) {
      const key = `mcp-target:${id}:${input.name}`;
      const stored = input.secret
        ? await abortable(
            this.ports.secrets.get(key),
            signal,
            "mcp_start_cancelled",
          )
        : undefined;
      check();
      const value =
        input.value ??
        (await abortable(
          this.ports.input(input, stored ?? input.fallback, signal),
          signal,
          "mcp_start_cancelled",
        ));
      check();
      if (value === undefined) throw new ClientError("mcp_start_cancelled");
      inputValue(input, value);
      if (value) values[input.name] = value;
      if (input.secret) credentials.push([key, value]);
    }
    // A prompt may outlive publication/access. Recheck before returning to the host.
    await verify();
    for (const [key, value] of credentials) {
      await this.write(async () => {
        check();
        if (value) await this.ports.secrets.store(key, value);
        else await this.ports.secrets.delete(key);
        check();
      });
    }
    return { recipe: selection.recipe, values };
  }
  async remove(id: string): Promise<void> {
    const selection = this.selections.get(id);
    if (selection)
      this.removals.set(
        id,
        selection.recipe.inputs
          .filter((input) => input.secret)
          .map((input) => `mcp-target:${id}:${input.name}`),
      );
    this.selections.delete(id);
    this.ports.changed();
    await this.write(async () => {
      for (const key of this.removals.get(id) ?? [])
        await this.ports.secrets.delete(key);
      this.removals.delete(id);
    });
  }
  pendingRemovals(): string[] {
    return [...this.removals.keys()];
  }
  async forget(
    cancelled: () => boolean = () => false,
  ): Promise<{ deleted: number; remaining: number; failed: number }> {
    if (!this.ports.secrets.keys)
      throw new ClientError("secret_enumeration_unavailable");
    // Invalidate resolutions even when sign-out already cleared the selections.
    this.generation++;
    return this.write(async () => {
      const keys = (await this.ports.secrets.keys!()).filter((key) =>
        /^mcp-target:[a-f0-9]{64}:[!#$%&'*+.^_`|~0-9a-z-]{1,128}$/iu.test(key),
      );
      let deleted = 0,
        failed = 0;
      for (const key of keys.slice(0, 50)) {
        if (cancelled()) break;
        try {
          await this.ports.secrets.delete(key);
          deleted++;
        } catch {
          failed++;
        }
      }
      if (keys.length === deleted) this.removals.clear();
      return { deleted, remaining: keys.length - deleted, failed };
    });
  }
  clear(): void {
    this.generation++;
    this.selections.clear();
    this.ports.changed();
  }
}
