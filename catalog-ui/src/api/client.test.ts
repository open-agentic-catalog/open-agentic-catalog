import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, canRead, makeAPI, result, safeLink } from "./client";
import type { Access } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
describe("Catalog client boundaries", () => {
  it("explains publication rejection without conflating it with a stale revision", () => {
    expect(new APIError(409, "PUBLICATION_NOT_READY").message).toContain(
      "approved publisher domain",
    );
    expect(new APIError(409, "CONFLICT").message).toContain(
      "conflicts with the current state",
    );
    expect(new APIError(412, "PRECONDITION_FAILED").message).toContain(
      "record changed",
    );
    expect(new APIError(404, "NOT_FOUND").message).toContain(
      "unavailable with your current access",
    );
  });
  const access: Access = {
    tenantId: "tenant",
    subject: "reader",
    membershipId: "member",
    realmId: "realm",
    actorType: "human",
    ...testAccess(["reader"]),
    namespaceGrants: [],
    scopeEnforcement: false,
    scopes: [],
  };
  it("allows scoped local reads while preserving the OAuth scope ceiling", () => {
    expect(canRead(access)).toBe(true);
    expect(canRead({ ...access, ...testAccess(["tenant_admin"]) })).toBe(true);
    expect(
      canRead({
        ...access,
        ...testAccess([]),
        availablePermissions: ["resource.read"],
        namespaceGrants: [
          { namespaceId: "n", permissions: testPermissions(["reader"]) },
        ],
      }),
    ).toBe(true);
    expect(canRead({ ...access, scopeEnforcement: true })).toBe(false);
    expect(
      canRead({ ...access, scopeEnforcement: true, scopes: ["catalog.read"] }),
    ).toBe(true);
  });
  it("renders only credential-free HTTPS handoff links", () => {
    for (const input of [
      "javascript:alert(1)",
      "data:text/html,x",
      "//attacker.test",
      "https://user:password@example.com",
      "/relative",
      "http://example.com",
      undefined,
    ])
      expect(safeLink(input)).toBeUndefined();
    expect(safeLink("https://example.com/docs")).toBe(
      "https://example.com/docs",
    );
  });
  it("does not expose unstructured server error details", () => {
    expect(() =>
      result({
        response: new Response("", { status: 500 }),
        error: { detail: "secret DSN" },
      }),
    ).toThrow(APIError);
    try {
      result({
        response: new Response("", { status: 403 }),
        error: { detail: "secret DSN", code: "FORBIDDEN" },
      });
    } catch (error) {
      expect(String(error)).not.toContain("secret DSN");
    }
  });
  it("forwards colon operations and validators with same-origin credentials", async () => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180/nested/catalog"));
    const transport = vi.fn(
      async (_request: Request) =>
        new Response("{}", {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", transport);
    const client = makeAPI({
      basePath: "/nested",
      authMode: "development",
      documentationUrl: "",
    });
    await client.api.POST("/api/v1/version-revisions/{revisionId}:validate", {
      params: {
        path: { revisionId: "revision" },
        header: { "Idempotency-Key": "command", "If-Match": '"rev-1"' },
      },
    });
    const request = transport.mock.calls[0]?.[0] as Request | undefined;
    expect(request?.url).toContain(
      "/nested/_catalog/api/v1/version-revisions/revision:validate",
    );
    expect(request?.headers.get("X-Catalog-Request")).toBe("1");
    expect(request?.headers.get("If-Match")).toBe('"rev-1"');
    expect(request?.headers.has("Authorization")).toBe(false);
  });
  it("rejects late data after changing identity even if fetch ignores abort", async () => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
    let finish!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    const client = makeAPI({
      basePath: "",
      authMode: "development",
      documentationUrl: "",
    });
    const pending = client.auth("session");
    client.invalidate();
    finish(new Response("{}"));
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });
  it("aborts old requests while allowing the new identity's requests", async () => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
    const requests: Request[] = [];
    vi.stubGlobal("fetch", async (request: Request) => {
      requests.push(request);
      return new Response('{"access":null,"expiresAt":0}');
    });
    const client = makeAPI({
      basePath: "",
      authMode: "development",
      documentationUrl: "",
    });
    await client.auth("session");
    client.invalidate();
    expect(required(requests[0]).signal.aborted).toBe(true);
    await expect(client.auth("session")).resolves.toEqual({
      access: null,
      expiresAt: 0,
    });
    expect(required(requests[1]).signal.aborted).toBe(false);
    expect(required(requests[1]).credentials).toBe("same-origin");
    expect(required(requests[1]).cache).toBe("no-store");
    expect(required(requests[1]).redirect).toBe("error");
  });
  it("propagates an individual read cancellation without cancelling the whole identity", async () => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
    const requests: Request[] = [];
    vi.stubGlobal("fetch", async (request: Request) => {
      requests.push(request);
      return new Response("{}", {
        headers: { "Content-Type": "application/json" },
      });
    });
    const client = makeAPI({
      basePath: "",
      authMode: "development",
      documentationUrl: "",
    });
    const controller = new AbortController();
    await client.api.GET("/api/v1/me", { signal: controller.signal });
    controller.abort();
    expect(required(requests[0]).signal.aborted).toBe(true);
    await client.api.GET("/api/v1/me");
    expect(required(requests[1]).signal.aborted).toBe(false);
  });
  it("accepts an empty logout response without attempting JSON decoding", async () => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180/nested"));
    const response = new Response(null, { status: 204 });
    const decode = vi.spyOn(response, "json");
    const transport = vi.fn(async (_request: Request) => response);
    vi.stubGlobal("fetch", transport);
    const client = makeAPI({
      basePath: "/nested",
      authMode: "development",
      documentationUrl: "",
    });
    await expect(client.auth("logout", "POST")).resolves.toBeUndefined();
    expect(decode).not.toHaveBeenCalled();
    expect(required(transport.mock.calls[0])[0].url).toBe(
      "http://127.0.0.1:4180/nested/auth/logout",
    );
    expect(required(transport.mock.calls[0])[0].method).toBe("POST");
  });
  it("serializes explicit login inputs without adding browser bearer credentials", async () => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180/nested"));
    const transport = vi.fn(
      async (_request: Request) =>
        new Response('{"location":"/nested/catalog"}'),
    );
    vi.stubGlobal("fetch", transport);
    const client = makeAPI({
      basePath: "/nested",
      authMode: "development",
      documentationUrl: "",
    });
    await expect(
      client.auth("login", "POST", { alias: "reader" }),
    ).resolves.toEqual({ location: "/nested/catalog" });
    const request = required(transport.mock.calls[0])[0];
    expect(request.method).toBe("POST");
    expect(await request.json()).toEqual({ alias: "reader" });
    expect(request.headers.get("Authorization")).toBeNull();
    expect(request.headers.get("X-Catalog-Request")).toBe("1");
    expect(request.credentials).toBe("same-origin");
  });
  it.each(["auth", "reference", "login"])(
    "%s rejects failed responses without decoding private error bodies",
    async (kind) => {
      vi.stubGlobal("location", new URL("http://127.0.0.1:4180/nested"));
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
      const response = new Response("private upstream error", {
        status: 503,
        headers: { "Retry-After": "30" },
      });
      const decode = vi.spyOn(response, "json");
      vi.stubGlobal("fetch", async () => response);
      const client = makeAPI({
        basePath: "/nested",
        authMode: "development",
        documentationUrl: "",
      });
      const pending =
        kind === "login"
          ? client.auth("login", "POST", {})
          : kind === "auth"
            ? client.auth("session")
            : client.reference(new AbortController().signal);
      await expect(pending).rejects.toMatchObject({
        status: 503,
        retryAt: Date.UTC(2026, 8, 26, 12, 0, 30),
        message: expect.stringContaining(
          "The server suggests retrying after",
        ) as unknown,
        code:
          kind === "login"
            ? "sign_in_unavailable"
            : kind === "auth"
              ? "session_request_failed"
              : "catalog_contract_unavailable",
      });
      expect(decode).not.toHaveBeenCalled();
      if (kind === "login")
        await expect(pending).rejects.toMatchObject({
          message: expect.stringContaining(
            "Sign-in is temporarily unavailable.",
          ) as unknown,
        });
    },
  );
  it("accepts defined falsy response values but rejects missing successful bodies", () => {
    for (const data of [null, false, 0, ""]) {
      expect(
        result({ data, response: new Response(null, { status: 200 }) }),
      ).toBe(data);
    }
    expect(() =>
      result({ response: new Response(null, { status: 200 }) }),
    ).toThrow(APIError);
    expect(() =>
      result({ response: new Response(null, { status: 204 }) }),
    ).toThrow(APIError);
  });
  it("retains request correlation but ignores malformed problem detail fields", () => {
    for (const error of [
      null,
      "private details",
      { code: 12, detail: "private details" },
    ]) {
      try {
        result({
          response: new Response(null, {
            status: 500,
            headers: { "X-Request-ID": "request-123" },
          }),
          error,
        });
        expect.fail("expected error");
      } catch (error) {
        expect(error).toMatchObject({
          status: 500,
          code: "request_failed",
          requestId: "request-123",
        });
        expect(String(error)).not.toContain("private details");
      }
    }
  });
  it.each([
    ["session", 200],
    ["product", 200],
    ["reference", 200],
    ["product", 503],
  ] as const)(
    "rejects %s replies (%s) when identity changes while decoding the body",
    async (kind, status) => {
      vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
      let complete!: () => void;
      let started!: () => void;
      const bodyRead = new Promise<void>((resolve) => {
        started = resolve;
      });
      const response = new Response("{}", {
        status,
        headers: { "Content-Type": "application/json", "Retry-After": "30" },
      });
      response.json = () => {
        started();
        return new Promise((resolve) => {
          complete = () => resolve({ private: "previous identity" });
        });
      };
      response.text = async () => JSON.stringify(await response.json());
      vi.stubGlobal("fetch", async () => response);
      const client = makeAPI({
        basePath: "",
        authMode: "development",
        documentationUrl: "",
      });
      const pending =
        kind === "session"
          ? client.auth("session")
          : kind === "reference"
            ? client.reference(new AbortController().signal)
            : client.api.GET("/api/v1/me").then(result);
      await bodyRead;
      client.invalidate();
      complete();
      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    },
  );
});

// Drive the real client through an inert transport: no listener or timer sends.
it.each([429, 503, 400, 401, 403, 409, 412, 500, 200])(
  "captures advice only on supported error status %s before body decoding",
  async (status) => {
    vi.useFakeTimers();
    const received = Date.UTC(2026, 8, 26, 12);
    vi.setSystemTime(received);
    vi.stubGlobal("location", new URL("https://catalog.test"));
    const response = new Response("{}", {
      status,
      headers: {
        "Retry-After": "30",
        "Content-Type": "application/json",
        "X-Request-ID": "correlation",
      },
    });
    response.text = async () => {
      vi.setSystemTime(received + 10000);
      return '{"code":"RETRY","detail":"private upstream"}';
    };
    const transport = vi.fn(async () => response);
    vi.stubGlobal("fetch", transport);
    const client = makeAPI({
      basePath: "",
      authMode: "development",
      documentationUrl: "",
    });
    const reply = await client.api.GET("/api/v1/me");
    if (status === 200) {
      expect(result(reply)).toMatchObject({ code: "RETRY" });
    } else {
      let caught: APIError | undefined;
      try {
        result(reply);
      } catch (error) {
        caught = error as APIError;
      }
      expect(caught).toBeInstanceOf(APIError);
      expect(caught).toMatchObject({
        status,
        code: "RETRY",
        requestId: "correlation",
      });
      expect(caught!.retryAt).toBe(
        status === 429 || status === 503 ? received + 30000 : undefined,
      );
      expect(caught!.message).not.toContain("private upstream");
      const message = caught!.message;
      if (status === 429 || status === 503) {
        expect(message).toContain(new Date(received + 30000).toLocaleString());
        expect(message).toContain("Retry manually when ready");
      } else expect(message).not.toContain("server suggests");
      await vi.advanceTimersByTimeAsync(120000);
      expect(caught!.message).toBe(message);
      // Consuming the same decoded response again must retain its original time.
      expect(() => result(reply)).toThrow(
        expect.objectContaining({ message, retryAt: caught!.retryAt }),
      );
      expect(transport).toHaveBeenCalledTimes(1);
      client.invalidate();
      expect(() => result(reply)).toThrow(
        expect.objectContaining({ name: "AbortError" }),
      );
    }
  },
);

it("keeps advice response-local across explicit attempts and ignores unusable replacements", async () => {
  vi.useFakeTimers();
  const received = Date.UTC(2026, 8, 26, 12);
  vi.setSystemTime(received);
  vi.stubGlobal("location", new URL("https://catalog.test"));
  const transport = vi.fn<typeof fetch>();
  vi.stubGlobal("fetch", transport);
  const client = makeAPI({
    basePath: "",
    authMode: "development",
    documentationUrl: "",
  });
  const cases = [
    { status: 503, hint: "30", at: received + 30000 },
    {
      status: 429,
      hint: "Sat, 26 Sep 2026 12:01:00 GMT",
      at: received + 60000,
    },
    { status: 503, hint: "private-header-sentinel", at: undefined },
    { status: 503, hint: undefined, at: undefined },
  ];
  for (const [index, entry] of cases.entries()) {
    vi.setSystemTime(received + index * 10000);
    transport.mockResolvedValueOnce(
      new Response('{"code":"UNAVAILABLE"}', {
        status: entry.status,
        headers: {
          "Content-Type": "application/json",
          ...(entry.hint === undefined ? {} : { "Retry-After": entry.hint }),
        },
      }),
    );
    const error: unknown = await client.api
      .GET("/api/v1/me")
      .then(result)
      .catch((error: unknown) => error);
    expect(error).toBeInstanceOf(APIError);
    expect(error).toMatchObject({ status: entry.status, retryAt: entry.at });
    expect((error as APIError).message).not.toContain(
      "private-header-sentinel",
    );
    expect((error as APIError).message.includes("server suggests")).toBe(
      entry.at !== undefined,
    );
    expect(transport).toHaveBeenCalledTimes(index + 1);
  }
});

it("reads only the UI host build with the existing fixed session-bound transport", async () => {
  vi.stubGlobal("location", new URL("http://127.0.0.1:4180/nested"));
  const identity = {
    version: "ui-build",
    commit: "ui-sha",
    sourceState: "modified",
  };
  const fetch = vi.fn(
    async (_input: RequestInfo | URL) => new Response(JSON.stringify(identity)),
  );
  vi.stubGlobal("fetch", fetch);
  const client = makeAPI({
    basePath: "/nested",
    authMode: "development",
    documentationUrl: "",
  });
  expect(await client.buildInfo(new AbortController().signal)).toEqual(
    identity,
  );
  const request = required(fetch.mock.calls[0]?.[0]) as Request;
  expect(request.url).toBe("http://127.0.0.1:4180/nested/_build");
  expect(request.credentials).toBe("same-origin");
  expect(request.headers.get("X-Catalog-Request")).toBe("1");
});

it.each([
  new Response("private issuer error", { status: 503 }),
  new Response("{}"),
  new Response("{private malformed content"),
  new Response("{}", { status: 201 }),
])(
  "fails safely on an unavailable or malformed UI build reply",
  async (response) => {
    vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
    vi.stubGlobal("fetch", async () => response);
    const client = makeAPI({
      basePath: "",
      authMode: "development",
      documentationUrl: "",
    });
    await expect(
      client.buildInfo(new AbortController().signal),
    ).rejects.toMatchObject({ code: "ui_build_unavailable" });
  },
);

it("discards a UI build body if the identity changes during JSON decoding", async () => {
  vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
  let finish!: (value: unknown) => void;
  const response = new Response("{}");
  vi.spyOn(response, "json").mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", async () => response);
  const client = makeAPI({
    basePath: "",
    authMode: "development",
    documentationUrl: "",
  });
  const pending = client.buildInfo(new AbortController().signal);
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  client.invalidate();
  finish({ version: "ui", commit: "fixture", sourceState: "clean" });
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
});

it("retains bounded diagnostic identifiers and drops malformed private-looking values", () => {
  const safe = new APIError(503, "DEPENDENCY_UNAVAILABLE", "trace.id_123-abc");
  expect(safe.code).toBe("DEPENDENCY_UNAVAILABLE");
  expect(safe.requestId).toBe("trace.id_123-abc");
  for (const value of ["", "secret=private", "a".repeat(129), "bad\nvalue"]) {
    expect(new APIError(503, value, value)).toMatchObject({
      code: "",
      requestId: "",
    });
  }
});

it("reads UI process information through the fixed local host", async () => {
  vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
  const payload = {
    role: "ui",
    version: "ui",
    commit: "fixture",
    sourceState: "clean",
    instanceId: "00000000-0000-7000-8000-000000000002",
    startedAt: "2026-09-01T12:00:00Z",
    observedAt: "2026-09-01T12:00:05Z",
    uptimeSeconds: 5,
  };
  const fetcher = vi.fn(
    async (..._args: unknown[]) => new Response(JSON.stringify(payload)),
  );
  vi.stubGlobal("fetch", fetcher);
  const client = makeAPI({
    basePath: "/nested",
    authMode: "development",
    documentationUrl: "",
  });
  expect(await client.processInfo(new AbortController().signal)).toEqual(
    payload,
  );
  expect(fetcher.mock.calls[0]?.[0]).toMatchObject({
    url: "http://127.0.0.1:4180/nested/_status",
  });
});
it.each([
  new Response(null, { status: 503 }),
  new Response(null, { status: 204 }),
  new Response("not JSON"),
  new Response(
    JSON.stringify({ version: "ui", commit: "fixture", sourceState: "clean" }),
  ),
])("rejects malformed or unavailable UI status safely", async (response) => {
  vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
  vi.stubGlobal("fetch", async () => response);
  const client = makeAPI({
    basePath: "",
    authMode: "development",
    documentationUrl: "",
  });
  await expect(
    client.processInfo(new AbortController().signal),
  ).rejects.toMatchObject({ code: "ui_status_unavailable" });
});
it("fences a UI status response decoded after an identity change", async () => {
  vi.stubGlobal("location", new URL("http://127.0.0.1:4180"));
  let finish!: (value: unknown) => void;
  const response = new Response("{}");
  vi.spyOn(response, "json").mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", async () => response);
  const client = makeAPI({
    basePath: "",
    authMode: "development",
    documentationUrl: "",
  });
  const pending = client.processInfo(new AbortController().signal);
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  client.invalidate();
  finish({});
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
});
