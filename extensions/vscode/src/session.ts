import {
  createCatalogClient,
  SDKError,
  type RawOptions,
  type RawResponse,
} from "@open-agentic-catalog/client";
import { createHash } from "node:crypto";
import { decodeJSON } from "./json.js";
import type { CredentialAccess } from "./host.js";
import {
  binding,
  ClientError,
  responseError,
  requestCorrelation,
  record,
  text,
  type Connection,
} from "./config.js";
import {
  bearer,
  opaqueToken,
  beginLogin,
  finishLogin,
  refreshLogin,
  type Credentials,
  type Login,
} from "./oidc.js";

export interface Secrets {
  get(key: string): PromiseLike<string | undefined>;
  store(key: string, value: string): PromiseLike<void>;
  delete(key: string): PromiseLike<void>;
  keys?(): PromiseLike<string[]>;
}
export interface Network {
  fetch: typeof fetch;
  close(): Promise<void>;
}
export interface Identity {
  tenantId: string;
  subject: string;
  realmId: string;
  membershipId: string;
  permissions: string[];
  availablePermissions: string[];
}
export interface Connected {
  mode: "control" | "published";
  identity?: Identity;
  resourceKinds?: string[];
  entryCount?: number;
  etag?: string;
}
function strings(value: unknown, limit: number): string[] {
  if (!Array.isArray(value) || value.length > limit)
    throw new ClientError("invalid_response");
  return value.map((v) => text(v, 128));
}
function uuid(value: unknown): string {
  const result = text(value, 36);
  if (
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu.test(
      result,
    )
  )
    throw new ClientError("invalid_response");
  return result;
}
export function identity(value: unknown): Identity {
  const me = record(value);
  return {
    tenantId: uuid(me.tenantId),
    subject: text(me.subject),
    realmId: uuid(me.realmId),
    membershipId: uuid(me.membershipId),
    permissions: strings(me.permissions, 50),
    availablePermissions: strings(me.availablePermissions, 50),
  };
}
export class Session {
  private listeners = new Set<() => void>();
  private statusListeners = new Set<() => void>();
  private checking = 0;
  private signingIn = false;
  private lastFailure:
    | { operation: string; code: string; status?: number; requestId?: string }
    | undefined;
  private epoch = 0;
  private controller = new AbortController();
  private current:
    { connection: Connection; network: Network; bound: string } | undefined;
  private credentials: Credentials | undefined;
  private storedCredentials: string | undefined;
  private login: Login | undefined;
  private loginResult:
    | { promise: Promise<void>; resolve(): void; reject(error: unknown): void }
    | undefined;
  private renewal: Promise<string> | undefined;
  private writes: Promise<void> = Promise.resolve();
  private observed: Connected | undefined;
  private verifiedIdentity: Identity | undefined;
  constructor(
    private readonly secrets: Secrets,
    private readonly host: string,
    private readonly createNetwork: (
      connection: Connection,
    ) => Promise<Network>,
    private readonly credentialAccess?: CredentialAccess,
  ) {}
  private key(id: string): string {
    return `openAgenticCatalog:${this.host}:${id}`;
  }
  snapshot(): Connected | undefined {
    return this.observed;
  }
  /** Presentation only: no credentials or authorization decisions leave the host. */
  connectionState():
    | "none"
    | "anonymous"
    | "signed_out"
    | "unverified"
    | "verified"
    | "verifying"
    | "signing_in" {
    if (!this.current) return "none";
    if (this.signingIn) return "signing_in";
    if (this.checking) return "verifying";
    if (this.current.connection.auth === "anonymous") return "anonymous";
    if (!this.credentials) return "signed_out";
    return this.observed ? "verified" : "unverified";
  }
  diagnostics() {
    return this.lastFailure ? { ...this.lastFailure } : undefined;
  }
  onStatusChange(listener: () => void): { dispose(): void } {
    this.statusListeners.add(listener);
    return {
      dispose: () => {
        this.statusListeners.delete(listener);
      },
    };
  }
  private status() {
    for (const listener of this.statusListeners) listener();
  }
  /** A private cache binding, never an authorization assertion or raw token. */
  consumerBinding(): string {
    return createHash("sha256")
      .update(
        JSON.stringify([
          this.host,
          this.active().bound,
          this.verifiedIdentity
            ? [
                this.verifiedIdentity.tenantId,
                this.verifiedIdentity.realmId,
                this.verifiedIdentity.membershipId,
                this.verifiedIdentity.subject,
              ]
            : (this.credentials?.accessToken ?? "anonymous"),
        ]),
      )
      .digest("hex");
  }
  cancellation(): AbortSignal {
    return this.controller.signal;
  }
  onChange(listener: () => void): { dispose(): void } {
    this.listeners.add(listener);
    return {
      dispose: () => {
        this.listeners.delete(listener);
      },
    };
  }
  connection(): Connection {
    return this.active().connection;
  }
  invalidate(): void {
    this.epoch++;
    this.controller.abort();
    this.controller = new AbortController();
    this.login = undefined;
    this.loginResult?.reject(new ClientError("connection_changed"));
    this.loginResult = undefined;
    this.observed = undefined;
    this.verifiedIdentity = undefined;
    this.credentials = undefined;
    this.storedCredentials = undefined;
    this.renewal = undefined;
    this.checking = 0;
    this.signingIn = false;
    this.lastFailure = undefined;
    for (const listener of this.listeners) listener();
    this.status();
  }
  private assert(epoch: number): void {
    if (epoch !== this.epoch || this.controller.signal.aborted)
      throw new ClientError("connection_changed");
  }
  guard(connection: Connection): () => void {
    const epoch = this.epoch;
    const bound = binding(connection, this.host);
    const check = () => {
      this.assert(epoch);
      if (this.active().bound !== bound)
        throw new ClientError("connection_changed");
    };
    check();
    return check;
  }
  async select(connection: Connection): Promise<void> {
    this.invalidate();
    const epoch = this.epoch;
    const old = this.current;
    this.current = undefined;
    await old?.network.close();
    this.assert(epoch);
    const network = await this.createNetwork(connection);
    if (epoch !== this.epoch) {
      await network.close();
      throw new ClientError("connection_changed");
    }
    const bound = binding(connection, this.host);
    this.current = { connection, network, bound };
    await this.writes;
    this.assert(epoch);
    const saved = await this.secrets.get(this.key(connection.id));
    this.assert(epoch);
    if (saved !== undefined) {
      try {
        const credential = record(JSON.parse(saved));
        if (
          credential.binding !== bound ||
          typeof credential.accessToken !== "string"
        )
          throw new ClientError("credential_binding_changed");
        if (
          credential.expiresAt !== undefined &&
          (!Number.isSafeInteger(credential.expiresAt) ||
            (credential.expiresAt as number) <= 0)
        )
          throw new ClientError("invalid_credentials");
        if (
          (connection.auth === "oidc" &&
            (credential.subject === undefined ||
              credential.nonce === undefined ||
              credential.expiresAt === undefined)) ||
          (credential.authTime !== undefined &&
            (typeof credential.authTime !== "number" ||
              !Number.isFinite(credential.authTime)))
        )
          throw new ClientError("invalid_credentials");
        this.credentials = {
          binding: bound,
          accessToken: bearer(credential.accessToken),
          ...(credential.refreshToken === undefined
            ? {}
            : { refreshToken: opaqueToken(credential.refreshToken) }),
          ...(credential.expiresAt === undefined
            ? {}
            : { expiresAt: credential.expiresAt as number }),
          ...(credential.subject === undefined
            ? {}
            : { subject: text(credential.subject) }),
          ...(credential.nonce === undefined
            ? {}
            : { nonce: text(credential.nonce, 128) }),
          ...(credential.authTime === undefined
            ? {}
            : { authTime: credential.authTime as number }),
        };
        this.storedCredentials = saved;
      } catch {
        const key = this.key(connection.id);
        await this.write(async () => {
          this.assert(epoch);
          if (
            (this.credentialAccess &&
              !(await this.credentialAccess.current(key, saved))) ||
            (await this.secrets.get(key)) !== saved
          )
            throw new ClientError("credential_update_pending");
          this.assert(epoch);
          return this.deleteCredential(key);
        }, key);
        throw new ClientError("sign_in_required");
      }
    }
  }
  private write<T>(
    operation: () => PromiseLike<T>,
    key = this.current ? this.key(this.current.connection.id) : undefined,
    uncertain = false,
  ): Promise<T> {
    return this.enqueue(() =>
      this.credentialAccess && key
        ? this.credentialAccess.run(key, async () => operation(), uncertain)
        : operation(),
    );
  }
  private enqueue<T>(operation: () => PromiseLike<T>): Promise<T> {
    const next = this.writes.then(operation);
    this.writes = next.then(
      () => {},
      () => {},
    );
    return next;
  }
  private active() {
    if (!this.current) throw new ClientError("select_connection");
    return this.current;
  }
  private async deleteCredential(key: string): Promise<void> {
    await this.credentialAccess?.record(key, undefined);
    await this.secrets.delete(key);
  }
  private async save(value: Credentials, epoch: number): Promise<void> {
    const id = this.active().connection.id;
    const saved = JSON.stringify(value);
    await this.write(async () => {
      this.assert(epoch);
      await this.credentialAccess?.record(this.key(id), undefined);
      await this.secrets.store(this.key(id), saved);
      await this.credentialAccess?.record(this.key(id), saved);
    });
    this.assert(epoch);
    this.credentials = value;
    this.storedCredentials = saved;
  }
  async manual(token: string): Promise<void> {
    const current = this.active();
    if (current.connection.auth !== "manual")
      throw new ClientError("wrong_authentication_mode");
    this.invalidate();
    await this.save(
      { binding: current.bound, accessToken: bearer(token) },
      this.epoch,
    );
  }
  async signOut(): Promise<void> {
    const id = this.current?.connection.id;
    this.invalidate();
    if (id)
      await this.write(() => this.deleteCredential(this.key(id)), this.key(id));
  }
  async recoverCredentials(): Promise<void> {
    const key = this.key(this.active().connection.id);
    this.invalidate();
    const epoch = this.epoch;
    await this.enqueue(async () => {
      this.assert(epoch);
      if (this.credentialAccess)
        await this.credentialAccess.recover(key, async () => {
          await this.secrets.delete(key);
        });
      else await this.secrets.delete(key);
      this.assert(epoch);
    });
  }
  async secretsChanged(key?: string): Promise<void> {
    const epoch = this.epoch;
    await this.writes;
    const active = this.current;
    if (
      epoch !== this.epoch ||
      !active ||
      (key !== undefined && key !== this.key(active.connection.id))
    )
      return;
    let saved: string | undefined;
    try {
      saved = await this.secrets.get(this.key(active.connection.id));
      if (epoch !== this.epoch) return;
      if (this.credentialAccess) {
        const current = await this.credentialAccess.current(
          this.key(active.connection.id),
          saved,
        );
        if (epoch !== this.epoch) return;
        if (!current) {
          // A native read may lag a committed write. Retain only the exact
          // credential that still matches that commit; otherwise clear it.
          const accepted = await this.credentialAccess.current(
            this.key(active.connection.id),
            this.storedCredentials,
          );
          if (epoch !== this.epoch) return;
          // A local renewal owns its transition and already fences storage
          // before committing or clearing the credential under the host lock.
          if (!accepted && !this.renewal) this.invalidate();
          return;
        }
      }
    } catch (error) {
      if (epoch !== this.epoch) return;
      this.invalidate();
      throw error;
    }
    await this.writes;
    if (epoch !== this.epoch) return;
    // Compare the exact accepted string, not a rebuilt credential document.
    if (saved !== this.storedCredentials) await this.select(active.connection);
  }

  async remove(id: string): Promise<void> {
    if (this.current?.connection.id === id) {
      const removing = this.current;
      const operation = this.signOut();
      const epoch = this.epoch;
      await operation;
      if (this.current === removing && this.epoch === epoch)
        await this.dispose();
    } else
      await this.write(() => this.deleteCredential(this.key(id)), this.key(id));
  }
  async startLogin(
    redirectURI: string,
    callbackURI = redirectURI,
    signal?: AbortSignal,
  ): Promise<string> {
    const current = this.active();
    if (current.connection.auth !== "oidc")
      throw new ClientError("wrong_authentication_mode");
    this.invalidate();
    const epoch = this.epoch;
    this.signingIn = true;
    this.status();
    let result;
    try {
      result = await beginLogin(
        current.connection,
        redirectURI,
        current.network.fetch,
        signal
          ? AbortSignal.any([this.controller.signal, signal])
          : this.controller.signal,
        callbackURI,
      );
    } catch (error) {
      if (epoch === this.epoch) {
        this.signingIn = false;
        this.status();
      }
      throw error;
    }
    this.assert(epoch);
    this.login = result.login;
    let resolve!: () => void, reject!: (error: unknown) => void;
    const promise = new Promise<void>((yes, no) => {
      resolve = yes;
      reject = no;
    });
    void promise.catch(() => {});
    this.loginResult = { promise, resolve, reject };
    return result.url;
  }
  async waitForLogin(signal: AbortSignal): Promise<void> {
    const epoch = this.epoch,
      result = this.loginResult,
      login = this.login;
    if (!result || !login) throw new ClientError("no_pending_login");
    const deadline = AbortSignal.timeout(
      Math.max(1, login.expiresAt - Date.now()),
    );
    try {
      await this.wait(result.promise, AbortSignal.any([signal, deadline]));
      this.assert(epoch);
    } catch (error) {
      if (epoch === this.epoch) this.invalidate();
      if (deadline.aborted) throw new ClientError("login_expired");
      throw error;
    }
  }
  async callback(url: string): Promise<void> {
    const current = this.active();
    const login = this.login;
    if (!login) throw new ClientError("no_pending_login");
    const incoming = new URL(text(url, 8192)),
      expected = new URL(login.callbackURI);
    if (
      incoming.searchParams.get("state") !== login.state ||
      incoming.protocol !== expected.protocol ||
      incoming.host !== expected.host ||
      incoming.pathname !== expected.pathname
    )
      throw new ClientError("invalid_login_callback");
    const result = this.loginResult;
    // Consume once, before exchange. A duplicate can never spend the code again.
    this.login = undefined;
    const epoch = this.epoch;
    try {
      const credential = await finishLogin(
        current.connection,
        login,
        url,
        current.network.fetch,
        this.controller.signal,
        current.bound,
      );
      this.assert(epoch);
      await this.save(credential, epoch);
      result?.resolve();
    } catch (error) {
      result?.reject(error);
      throw error;
    } finally {
      if (epoch === this.epoch) {
        this.signingIn = false;
        this.status();
      }
    }
  }
  private async token(signal: AbortSignal): Promise<string | undefined> {
    const { connection, network } = this.active();
    if (connection.auth === "anonymous") return undefined;
    const old = this.credentials;
    if (!old) throw new ClientError("sign_in_required");
    const canRefresh = !!(connection.oidc?.refresh && old.refreshToken);
    if (!old.expiresAt || old.expiresAt > Date.now() + (canRefresh ? 60000 : 0))
      return old.accessToken;
    if (!canRefresh) {
      await this.signOut();
      throw new ClientError("sign_in_required");
    }
    if (this.renewal) return this.wait(this.renewal, signal);
    const epoch = this.epoch;
    const saved = this.storedCredentials,
      key = this.key(connection.id);
    this.renewal = this.write(
      async () => {
        this.assert(epoch);
        if (
          this.credentialAccess &&
          !(await this.credentialAccess.current(key, saved))
        )
          throw new ClientError("credential_update_pending");
        if ((await this.secrets.get(key)) !== saved) {
          this.invalidate();
          throw new ClientError("connection_changed");
        }
        try {
          await this.credentialAccess?.record(key, undefined);
          const next = await refreshLogin(
            connection,
            old,
            network.fetch,
            AbortSignal.any([
              this.controller.signal,
              AbortSignal.timeout(connection.timeoutMs),
            ]),
          );
          this.assert(epoch);
          if ((await this.secrets.get(key)) !== saved) {
            this.invalidate();
            throw new ClientError("connection_changed");
          }
          this.assert(epoch);
          const encoded = JSON.stringify(next);
          await this.secrets.store(key, encoded);
          await this.credentialAccess?.record(key, encoded);
          this.assert(epoch);
          this.credentials = next;
          this.storedCredentials = encoded;
          return next.accessToken;
        } catch {
          if (epoch !== this.epoch) throw new ClientError("connection_changed");
          if (epoch === this.epoch) {
            this.invalidate();
            if ((await this.secrets.get(key)) === saved)
              await this.deleteCredential(key);
          }
          throw new ClientError("sign_in_required");
        }
      },
      key,
      true,
    ).finally(() => {
      if (epoch === this.epoch) this.renewal = undefined;
    });
    return this.wait(this.renewal, signal);
  }
  private async wait<T>(
    operation: Promise<T>,
    signal: AbortSignal,
  ): Promise<T> {
    let cancel: () => void = () => {};
    try {
      return await Promise.race([
        operation,
        new Promise<never>((_, reject) => {
          cancel = () =>
            reject(
              new ClientError(
                signal.reason instanceof DOMException &&
                  signal.reason.name === "TimeoutError"
                  ? "request_timeout"
                  : "request_cancelled",
              ),
            );
          if (signal.aborted) cancel();
          else signal.addEventListener("abort", cancel, { once: true });
        }),
      ]);
    } finally {
      signal.removeEventListener("abort", cancel);
    }
  }
  async read(
    path: string,
    options: RawOptions = {},
    accepted: readonly number[] = [200],
  ): Promise<RawResponse> {
    const active = this.active();
    const { connection, network } = active;
    const epoch = this.epoch;
    const signal = AbortSignal.any([
      this.controller.signal,
      ...(options.signal ? [options.signal] : []),
    ]);
    const privateValues = [
      this.credentials?.accessToken,
      this.credentials?.refreshToken,
    ];
    try {
      const token = await this.token(
        AbortSignal.any([
          ...(options.signal ? [options.signal] : []),
          AbortSignal.timeout(connection.timeoutMs),
        ]),
      );
      privateValues.push(token, this.credentials?.refreshToken);
      this.assert(epoch);
      const client = createCatalogClient(
        {
          origin: connection.origin,
          allowLoopbackHTTP: connection.allowLoopbackHTTP,
          timeoutMs: connection.timeoutMs,
          requestBytes: connection.requestBytes,
          responseBytes: connection.responseBytes,
          headerBytes: connection.headerBytes,
          ...(token ? { token } : {}),
        },
        network.fetch,
      );
      const response = await client.raw(path, {
        ...options,
        signal,
        responseBytes: Math.min(
          connection.responseBytes,
          options.responseBytes ?? connection.responseBytes,
        ),
      });
      this.assert(epoch);
      if (!accepted.includes(response.status))
        throw responseError(
          response.status === 401
            ? "sign_in_required"
            : response.status === 403
              ? "access_refused"
              : response.status === 404
                ? "publication_unavailable"
                : response.status === 400 ||
                    response.status === 409 ||
                    response.status === 410
                  ? "query_or_continuation_changed"
                  : "service_unavailable",
          response.status,
          response.headers,
          privateValues,
        );
      return response;
    } catch (error) {
      if (
        epoch !== this.epoch &&
        !(
          error instanceof ClientError &&
          error.code === "sign_in_required" &&
          this.current === active &&
          !this.credentials
        )
      )
        throw new ClientError("connection_changed");
      const safe =
        error instanceof SDKError
          ? new ClientError(
              error.code,
              Number.isInteger(error.status) &&
                error.status! >= 100 &&
                error.status! <= 599
                ? error.status
                : undefined,
              requestCorrelation(error.requestID, privateValues),
            )
          : error;
      if (safe instanceof ClientError && epoch === this.epoch)
        this.lastFailure = {
          operation:
            path === "/api/v1/me"
              ? "Verify account"
              : path === "/api/v1/capabilities"
                ? "Read capabilities"
                : /^\/api\/v1\/(?:ratings|feedback)(?:\/|$)/u.test(path) &&
                    options.method &&
                    options.method !== "GET" &&
                    path !== "/api/v1/ratings/read"
                  ? "Personal opinion command"
                  : "Catalog read",
          code: safe.code,
          ...(safe.status ? { status: safe.status } : {}),
          ...(safe.requestId ? { requestId: safe.requestId } : {}),
        };
      if (error instanceof SDKError) throw safe;
      throw error;
    }
  }
  async json(
    path: string,
    options: RawOptions = {},
  ): Promise<Record<string, unknown>> {
    const response = await this.read(path, options);
    if (
      !/^application\/json(?:;|$)/iu.test(
        response.headers.get("content-type") ?? "",
      )
    )
      throw new ClientError("invalid_response");
    try {
      return record(decodeJSON(response.text()));
    } catch {
      throw new ClientError("invalid_response");
    }
  }
  async test(signal?: AbortSignal): Promise<Connected> {
    const { connection } = this.active();
    const epoch = this.epoch;
    this.observed = undefined;
    this.checking++;
    this.status();
    try {
      let result: Connected;
      if (connection.mode === "control") {
        const me = identity(
          await this.json("/api/v1/me", signal ? { signal } : {}),
        );
        if (
          this.credentials?.subject &&
          this.credentials.subject !== me.subject
        ) {
          this.invalidate();
          throw new ClientError("identity_changed");
        }
        const capabilities = await this.json(
          "/api/v1/capabilities",
          signal ? { signal } : {},
        );
        const kinds = strings(capabilities.resourceKinds, 4);
        if (
          new Set(kinds).size !== kinds.length ||
          kinds.some(
            (k) => !["agent", "mcp_server", "skill", "plugin"].includes(k),
          )
        )
          throw new ClientError("invalid_response");
        result = { mode: "control", identity: me, resourceKinds: kinds };
      } else {
        const manifest = await this.read(
          "/.well-known/ard.json",
          signal ? { signal } : {},
        );
        if (
          !/^application\/json(?:;|$)/iu.test(
            manifest.headers.get("content-type") ?? "",
          )
        )
          throw new ClientError("invalid_response");
        let value: Record<string, unknown>;
        try {
          value = record(decodeJSON(manifest.text()));
        } catch {
          throw new ClientError("invalid_response");
        }
        if (!Array.isArray(value.entries))
          throw new ClientError("invalid_response");
        // Connection verification reads only the bounded manifest count.
        result = {
          mode: "published",
          entryCount: value.entries.length,
          ...(manifest.headers.get("etag")
            ? { etag: manifest.headers.get("etag")! }
            : {}),
        };
      }
      this.assert(epoch);
      const previous = this.verifiedIdentity,
        current = result.identity;
      if (
        previous &&
        current &&
        (["tenantId", "realmId", "membershipId", "subject"] as const).some(
          (key) => previous[key] !== current[key],
        )
      ) {
        // Keep the account binding across failed probes. Identity changes
        // invalidate every private view and pending action, not just this read.
        this.invalidate();
        throw new ClientError("identity_changed");
      }
      this.verifiedIdentity = current;
      this.observed = result;
      return result;
    } catch (error) {
      if (epoch === this.epoch) this.observed = undefined;
      if (error instanceof SDKError) throw new ClientError(error.code);
      throw error;
    } finally {
      if (epoch === this.epoch) {
        this.checking--;
        this.status();
      }
    }
  }
  async dispose(): Promise<void> {
    this.invalidate();
    const old = this.current;
    this.current = undefined;
    await old?.network.close();
  }
}
