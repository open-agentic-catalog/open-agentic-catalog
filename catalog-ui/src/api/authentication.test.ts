// Exercise endpoint decoding and identity fences through the real client and inert responses.
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { deferred, sessionAccess } from "../../tests/unit/fixtures";
import { APIError, makeAPI } from "./client";
import type { Runtime } from "./client";

const runtime: Runtime = {
  basePath: "/unit",
  authMode: "development",
  documentationUrl: "",
};
beforeEach(() => {
  vi.stubGlobal("location", new URL("http://127.0.0.1:4180/unit/catalog"));
});
afterEach(() => {
  vi.unstubAllGlobals();
});

it("decodes actual host replies and exposes the current identity cancellation signal", async () => {
  const replies = [
    { access: sessionAccess, expiresAt: Math.floor(Date.now() / 1000) + 600 },
    { mode: "development", users: [{ alias: "reader", label: "Reader" }] },
    { location: "https://issuer.example.test/authorize?state=a%2Bb" },
  ];
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  const client = makeAPI(runtime);
  fetch.mockResolvedValueOnce(Response.json(replies[0]));
  await expect(client.auth("session")).resolves.toEqual(replies[0]);
  fetch.mockResolvedValueOnce(Response.json(replies[1]));
  await expect(client.auth("options")).resolves.toEqual(replies[1]);
  fetch.mockResolvedValueOnce(Response.json(replies[2]));
  await expect(
    makeAPI({ ...runtime, authMode: "oidc" }).auth("login", "POST", {}),
  ).resolves.toEqual(replies[2]);
  const previous = client.signal;
  client.invalidate();
  expect(previous.aborted).toBe(true);
  expect(client.signal).not.toBe(previous);
  expect(client.signal.aborted).toBe(false);
});

it.each(["session", "options", "login"] as const)(
  "rejects missing or malformed successful %s bodies with safe response-local errors",
  async (path) => {
    for (const response of [
      new Response(null, { status: 204 }),
      new Response("private invalid JSON"),
      Response.json({ private: "body sentinel" }),
      Response.json({}, { status: 201 }),
    ]) {
      response.headers.set("X-Request-ID", "local-request");
      const transport = vi.fn(async () => response);
      vi.stubGlobal("fetch", transport);
      const client = makeAPI(runtime);
      const pending =
        path === "login"
          ? client.auth("login", "POST", {})
          : path === "options"
            ? client.auth("options")
            : client.auth("session");
      const error: unknown = await pending.catch((value: unknown) => value);
      expect(error).toBeInstanceOf(APIError);
      expect(error).toMatchObject({
        status: response.status,
        code:
          path === "session"
            ? "invalid_session_response"
            : "invalid_auth_response",
        requestId: "local-request",
      });
      expect(String(error)).not.toMatch(/private|sentinel/);
      expect(error).not.toHaveProperty("cause");
      expect(transport).toHaveBeenCalledOnce();
    }
  },
);

it("does not accept arbitrary success bodies as logout acknowledgement", async () => {
  vi.stubGlobal("fetch", async () => Response.json({ status: "ok" }));
  await expect(
    makeAPI(runtime).auth("logout", "POST", {}),
  ).rejects.toMatchObject({ status: 200, code: "invalid_auth_response" });
});

it("checks identity again between receiving headers and consuming the auth reply", async () => {
  const headers = deferred<Response>();
  vi.stubGlobal("fetch", () => headers.promise);
  const client = makeAPI(runtime);
  const pending = client.auth("session");
  headers.resolve(Response.json({ access: null, expiresAt: 0 }));
  // A competing identity action may run after transport receipt but before
  // the auth continuation resumes. No response or decoder is patched here.
  queueMicrotask(() => queueMicrotask(() => client.invalidate()));
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
});

it.each([false, true])(
  "classifies a rejected session body safely (superseded=%s)",
  async (superseded) => {
    const body = deferred<unknown>();
    const started = deferred<void>();
    const response = new Response("", {
      headers: { "X-Request-ID": "local-body" },
    });
    response.json = () => {
      started.resolve();
      return body.promise;
    };
    vi.stubGlobal("fetch", async () => response);
    const client = makeAPI(runtime);
    const pending = client.auth("session");
    await started.promise;
    if (superseded) client.invalidate();
    body.reject(new SyntaxError("private streamed body"));
    if (superseded)
      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    else
      await expect(pending).rejects.toMatchObject({
        status: 200,
        code: "invalid_session_response",
        requestId: "local-body",
      });
  },
);

it.each([false, true])(
  "retains current transport errors but fences old failures (superseded=%s)",
  async (superseded) => {
    const pendingFetch = deferred<Response>();
    vi.stubGlobal("fetch", () => pendingFetch.promise);
    const client = makeAPI(runtime);
    const pending = client.auth("session");
    if (superseded) client.invalidate();
    const failure = new TypeError("connection failed");
    pendingFetch.reject(failure);
    if (superseded)
      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    else await expect(pending).rejects.toBe(failure);
  },
);
