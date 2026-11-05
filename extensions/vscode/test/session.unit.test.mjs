import test from "node:test";
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
const {
  connections,
  binding,
  diagnostic,
  ClientError,
  Session,
  identity,
  bearer,
  opaqueToken,
} = await import(pathToFileURL(process.env.CATALOG_VSCODE_LIB));
const definition = {
  id: "local",
  label: "Local",
  origin: "https://catalog.example",
  mode: "control",
  auth: "manual",
};
const connection = connections([definition])[0];
const me = {
  tenantId: "00000000-0000-7000-8000-000000000001",
  realmId: "00000000-0000-7000-8000-000000000002",
  membershipId: "00000000-0000-7000-8000-000000000003",
  subject: "reader",
  permissions: ["resource.read"],
  availablePermissions: ["resource.read"],
};
function fixture(fetcher) {
  const saved = new Map();
  const secrets = {
    get: async (k) => saved.get(k),
    store: async (k, v) => {
      saved.set(k, v);
    },
    delete: async (k) => {
      saved.delete(k);
    },
  };
  let closed = 0;
  const session = new Session(secrets, "test-host", async () => ({
    fetch: (input, init) =>
      input instanceof Request
        ? fetcher(input.url, { headers: input.headers, signal: input.signal })
        : fetcher(input, init),
    close: async () => {
      closed++;
    },
  }));
  return { session, saved, secrets, closed: () => closed };
}
const json = (value) =>
  new Response(JSON.stringify(value), {
    headers: { "content-type": "application/json" },
  });

test("connection presentation distinguishes first use, credentials and anonymous reads", async () => {
  const f = fixture((url) =>
    json(url.endsWith("/me") ? me : { resourceKinds: ["agent"] }),
  );
  assert.equal(f.session.connectionState(), "none");
  await f.session.select(connection);
  assert.equal(f.session.connectionState(), "signed_out");
  await f.session.manual("private-token-sentinel");
  assert.equal(f.session.connectionState(), "unverified");
  await f.session.test();
  assert.equal(f.session.connectionState(), "verified");
  await f.session.signOut();
  assert.equal(f.session.connectionState(), "signed_out");
  await f.session.select(
    connections([{ ...definition, mode: "published", auth: "anonymous" }])[0],
  );
  assert.equal(f.session.connectionState(), "anonymous");
  assert.ok(
    !JSON.stringify(f.session.connectionState()).includes("private-token"),
  );
  await f.session.dispose();
});

test("connection settings use strict explicit modes, bounds and origins", () => {
  assert.equal(connection.timeoutMs, 30000);
  assert.equal(connection.responseBytes, 8 * 1024 ** 2);
  assert.equal(connection.requestBytes, 16 * 1024 ** 2);
  assert.equal(connection.headerBytes, 32768);
  for (const change of [
    { unknown: true },
    { auth: "anonymous" },
    { mode: "other" },
    { token: "secret" },
    { origin: "https://user:secret@catalog.example" },
    { origin: "https://catalog.example/path" },
    { origin: "https://catalog.example?token=secret" },
    { origin: "https://catalog.example/#" },
    { origin: "http://example.com", allowLoopbackHTTP: true },
    { origin: "http://localhost", allowLoopbackHTTP: true },
    { origin: "https://catalog.example:0" },
    { timeoutMs: 0 },
    { timeoutMs: 120001 },
    { timeoutMs: 1.5 },
    { responseBytes: 33554433 },
    { responseBytes: 1023 },
    { requestBytes: 1000 },
    { headerBytes: 1023 },
    { proxyURL: "http://proxy.example?" },
    { headerBytes: 65537 },
    { requestBytes: 16777217 },
    { allowLoopbackHTTP: "true" },
    { caFile: "relative.pem" },
    { proxyURL: "http://user:password@proxy.example" },
    { id: "../unsafe" },
    { label: "line\nbreak" },
    { oidc: {} },
  ])
    assert.throws(() => connections([{ ...definition, ...change }]));
  assert.throws(() => connections([definition, definition]));
  assert.throws(() => connections(Array(21).fill(definition)));
  assert.throws(() => connections({}));
  assert.equal(
    connections([
      {
        ...definition,
        origin: "http://127.0.0.1:8080",
        allowLoopbackHTTP: true,
      },
    ])[0].origin,
    "http://127.0.0.1:8080",
  );
  assert.equal(
    connections([{ ...definition, mode: "published", auth: "anonymous" }])[0]
      .auth,
    "anonymous",
  );
});
test("connection labels use Unicode code points and reject malformed display input", () => {
  for (const length of [40, 41, 80])
    assert.equal(
      connections([{ ...definition, label: "\u{1f916}".repeat(length) }])[0]
        .label,
      "\u{1f916}".repeat(length),
    );
  for (const label of [
    "\u{1f916}".repeat(81),
    "bad\ud800",
    "bad\u0085",
    "line\nfeed",
  ])
    assert.throws(
      () => connections([{ ...definition, label }]),
      /invalid_configuration/u,
    );
});
test("delayed connection removal preserves the newly selected connection", async () => {
  const f = fixture((url) =>
    json(url.endsWith("/me") ? me : { resourceKinds: ["agent"] }),
  );
  await f.session.select(connection);
  await f.session.manual("first-token");
  let enter, release;
  const entered = new Promise((done) => {
      enter = done;
    }),
    held = new Promise((done) => {
      release = done;
    });
  const remove = f.secrets.delete;
  f.secrets.delete = async (key) => {
    enter();
    await held;
    await remove(key);
  };
  const removal = f.session.remove(connection.id);
  await entered;
  const second = connections([
    { ...definition, id: "second", origin: "https://second.example" },
  ])[0];
  // Selection waits on earlier writes; begin it before completing deletion.
  const selection = f.session.select(second);
  release();
  await Promise.all([removal, selection]);
  await f.session.manual("second-token");
  assert.equal(f.session.connection().id, "second");
  assert.equal((await f.session.test()).identity.subject, "reader");
  assert.equal(f.saved.size, 1);
  await f.session.dispose();
});
test("short non-refreshable cached tokens remain usable until actual expiry", async () => {
  const f = fixture((url) =>
    json(url.endsWith("/me") ? me : { resourceKinds: ["agent"] }),
  );
  const oidc = connections([
    {
      ...definition,
      auth: "oidc",
      oidc: {
        issuer: "https://issuer.example",
        clientId: "client",
        audience: "catalog",
        scopes: ["openid"],
      },
    },
  ])[0];
  const key = "openAgenticCatalog:test-host:local";
  for (const refresh of [false, true]) {
    const c = { ...oidc, oidc: { ...oidc.oidc, refresh } };
    f.saved.set(
      key,
      JSON.stringify({
        binding: binding(c, "test-host"),
        accessToken: "short-token",
        subject: "reader",
        nonce: "nonce",
        expiresAt: Date.now() + 30000,
      }),
    );
    await f.session.select(c);
    assert.equal((await f.session.test()).identity.subject, "reader");
    assert.equal(f.saved.size, 1);
    f.saved.set(
      key,
      JSON.stringify({
        binding: binding(c, "test-host"),
        accessToken: "expired-token",
        subject: "reader",
        nonce: "nonce",
        expiresAt: Date.now() - 1,
      }),
    );
    await f.session.select(c);
    await assert.rejects(f.session.test(), /sign_in_required/u);
    assert.equal(f.saved.size, 0);
  }
  await f.session.dispose();
});
test("HTTP diagnostics retain only bounded correlation fields, never private bodies", async () => {
  let id = "owned-request_01",
    status = 503;
  const f = fixture(
    () =>
      new Response("private-body-secret", {
        status,
        headers: { "x-request-id": id },
      }),
  );
  await f.session.select(connection);
  await f.session.manual("private-token");
  for (const value of [
    "owned-request_01",
    "hostile body text",
    "x".repeat(129),
    "private-token",
  ]) {
    id = value;
    await assert.rejects(f.session.read("/api/v1/me"), (error) => {
      assert.equal(error.status, 503);
      assert.equal(
        error.requestId,
        value === "owned-request_01" ? value : undefined,
      );
      assert.doesNotMatch(
        diagnostic(error),
        /private-body|private-token|hostile body/u,
      );
      return true;
    });
  }
  for (const value of [400, 401, 403, 404, 409, 410]) {
    status = value;
    await assert.rejects(
      f.session.read("/api/v1/me"),
      (error) => error.status === value,
    );
  }
  await f.session.dispose();
});
test("OIDC inputs require explicit issuer, public client, audience and safe scopes", () => {
  const oidc = {
    issuer: "https://issuer.example/realm",
    clientId: "public-client",
    audience: "catalog",
    scopes: ["openid", "catalog.read"],
  };
  assert.equal(
    connections([{ ...definition, auth: "oidc", oidc }])[0].oidc.refresh,
    false,
  );
  for (const update of [
    { scopes: [] },
    { scopes: ["openid", "openid"] },
    { scopes: ["openid", 'unescaped"quote'] },
    { scopes: ["openid", "unicode-\u2603"] },
    { scopes: ["openid", "invalid scope"] },
    { scopes: ["openid", "offline_access"] },
    { clientSecret: "secret" },
    { issuer: "http://issuer.example" },
    { audience: "" },
    { refresh: "yes" },
  ])
    assert.throws(() =>
      connections([
        { ...definition, auth: "oidc", oidc: { ...oidc, ...update } },
      ]),
    );
});
test("binding covers connection, execution host, issuer, auth and budgets", () => {
  const original = binding(connection, "host-a");
  assert.notEqual(original, binding(connection, "host-b"));
  for (const change of [
    { origin: "https://other.example" },
    { auth: "oidc" },
    { mode: "published" },
    { timeoutMs: 60000 },
  ])
    assert.notEqual(original, binding({ ...connection, ...change }, "host-a"));
});
test("only consumed identity fields are admitted and diagnostic causes stay private", () => {
  assert.deepEqual(identity({ ...me, future: true }), me);
  for (const update of [
    { tenantId: "bad" },
    { subject: "" },
    { permissions: null },
    { availablePermissions: [42] },
  ])
    assert.throws(() => identity({ ...me, ...update }));
  assert.equal(
    diagnostic(new ClientError("access_refused")),
    "Catalog: access_refused",
  );
  assert.doesNotMatch(
    diagnostic(new Error("secret body token=private")),
    /private|secret body/u,
  );
  for (const value of ["", "with spaces", "Bearer token", "token\n", 42])
    assert.throws(() => bearer(value));
});
test("SDK calls require a token, use only Control connection reads and clear on sign-out", async () => {
  const calls = [];
  const f = fixture(async (url, init) => {
    calls.push([url, new Headers(init.headers).get("authorization")]);
    return json(
      url.endsWith("/me")
        ? me
        : { resourceKinds: ["agent", "mcp_server", "skill", "plugin"] },
    );
  });
  await f.session.select(connection);
  await assert.rejects(f.session.test(), /sign_in_required/u);
  assert.equal(calls.length, 0);
  await f.session.manual("reader-token");
  const result = await f.session.test();
  assert.equal(result.identity.subject, "reader");
  assert.deepEqual(
    calls.map((c) => c[0]),
    [
      "https://catalog.example/api/v1/me",
      "https://catalog.example/api/v1/capabilities",
    ],
  );
  assert.ok(calls.every((c) => c[1] === "Bearer reader-token"));
  assert.equal(f.saved.size, 1);
  await f.session.signOut();
  assert.equal(f.saved.size, 0);
  assert.equal(f.session.snapshot(), undefined);
  await assert.rejects(f.session.test(), /sign_in_required/u);
  await f.session.dispose();
  assert.equal(f.closed(), 1);
});
test("connection verification fences changed identities but permits current permission hints", async () => {
  for (const key of ["tenantId", "realmId", "membershipId", "subject"]) {
    let principal = { ...me };
    const f = fixture(async (url) =>
      json(url.endsWith("/me") ? principal : { resourceKinds: ["agent"] }),
    );
    try {
      await f.session.select(connection);
      await f.session.manual("reader-token");
      await f.session.test();
      const guard = f.session.guard(connection);
      // Permission hints can change without a change of account. The Catalog
      // still decides whether the next exact request is authorized.
      principal = { ...principal, permissions: [], availablePermissions: [] };
      assert.deepEqual((await f.session.test()).identity.permissions, []);
      guard();
      // A failed probe must not erase the verified account binding.
      const valid = principal;
      principal = { ...principal, subject: null };
      await assert.rejects(f.session.test(), /invalid_response/u);
      assert.equal(f.session.snapshot(), undefined);
      principal = {
        ...valid,
        [key]:
          key === "subject"
            ? "other-reader"
            : "00000000-0000-7000-8000-000000000009",
      };
      let invalidated = false;
      const listener = f.session.onChange(() => {
        invalidated = true;
      });
      await assert.rejects(f.session.test(), /identity_changed/u, key);
      assert.equal(invalidated, true, key);
      assert.throws(guard, /connection_changed/u, key);
      assert.equal(f.session.snapshot(), undefined);
      await assert.rejects(f.session.test(), /sign_in_required/u);
      listener.dispose();
    } finally {
      await f.session.dispose();
    }
  }
});
test("publication stays separate: anonymous manifest only and no private identity", async () => {
  const calls = [];
  const f = fixture(async (url, init) => {
    calls.push([url, new Headers(init.headers).get("authorization")]);
    return json({ entries: [{ identifier: "one" }] });
  });
  await f.session.select(
    connections([{ ...definition, mode: "published", auth: "anonymous" }])[0],
  );
  assert.deepEqual(await f.session.test(), {
    mode: "published",
    entryCount: 1,
  });
  assert.deepEqual(calls, [
    ["https://catalog.example/.well-known/ard.json", null],
  ]);
  await assert.rejects(f.session.manual("token"), /wrong_authentication_mode/u);
  await assert.rejects(
    f.session.startLogin("vscode://extension/oidc"),
    /wrong_authentication_mode/u,
  );
});
test("refusal never falls back, retries or exposes dependency text", async () => {
  for (const [status, code] of [
    [401, "sign_in_required"],
    [403, "access_refused"],
    [404, "publication_unavailable"],
    [503, "service_unavailable"],
  ]) {
    let calls = 0;
    const f = fixture(async () => {
      calls++;
      return new Response("private token", { status });
    });
    await f.session.select(connection);
    await f.session.manual("token");
    await assert.rejects(f.session.test(), new RegExp(code, "u"));
    assert.equal(calls, 1);
    assert.equal(f.session.snapshot(), undefined);
  }
});
test("malformed successful data and wrong media types remain failures", async () => {
  for (const response of [
    new Response("{}", { headers: { "content-type": "text/html" } }),
    json({ ...me, subject: null }),
    new Response("not json", {
      headers: { "content-type": "application/json" },
    }),
  ]) {
    const f = fixture(async () => response);
    await f.session.select(connection);
    await f.session.manual("token");
    await assert.rejects(f.session.test(), /invalid_response/u);
  }
  const f = fixture(async (url) =>
    json(url.endsWith("/me") ? me : { resourceKinds: ["agent", "agent"] }),
  );
  await f.session.select(connection);
  await f.session.manual("token");
  await assert.rejects(f.session.test(), /invalid_response/u);
});
test("late responses cannot restore private state after a connection change", async () => {
  let respond;
  let started;
  const ready = new Promise((resolve) => {
    started = resolve;
  });
  const f = fixture(async () => {
    started();
    return new Promise((resolve) => {
      respond = resolve;
    });
  });
  await f.session.select(connection);
  await f.session.manual("token");
  const pending = f.session.test();
  await ready;
  await f.session.select({
    ...connection,
    id: "other",
    origin: "https://other.example",
  });
  respond(json(me));
  await assert.rejects(pending);
  assert.equal(f.session.snapshot(), undefined);
});
test("changed connection cannot read old credentials; removal and external secret changes invalidate state", async () => {
  const f = fixture(async (url) =>
    json(url.endsWith("/me") ? me : { resourceKinds: ["agent"] }),
  );
  await f.session.select(connection);
  await f.session.manual("token");
  await f.session.test();
  await f.session
    .select({ ...connection, origin: "https://other.example" })
    .then(
      () => assert.fail("old binding accepted"),
      () => {},
    );
  assert.equal(f.saved.size, 0);
  await f.session.select(connection);
  await f.session.manual("token");
  await f.session.test();
  const guard = f.session.guard(connection);
  const cancellation = f.session.cancellation();
  f.saved.clear();
  await f.session.secretsChanged();
  assert.throws(guard, /connection_changed/u);
  assert.equal(cancellation.aborted, true);
  assert.equal(f.session.snapshot(), undefined);
  await assert.rejects(f.session.test(), /sign_in_required/u);
  await f.session.manual("token");
  await f.session.remove(connection.id);
  assert.equal(f.saved.size, 0);
  await assert.rejects(f.session.test(), /select_connection/u);
});
test("SecretStorage failure never produces a plaintext or memory fallback", async () => {
  const f = fixture(async () => json(me));
  f.secrets.store = async () => {
    throw new Error("keyring secret");
  };
  await f.session.select(connection);
  await assert.rejects(f.session.manual("token"));
  await assert.rejects(f.session.test(), /sign_in_required/u);
  assert.equal(f.saved.size, 0);
});

test("OIDC reload requires the original login binding before any API request", async () => {
  const selected = connections([
    {
      ...definition,
      auth: "oidc",
      oidc: {
        issuer: "https://issuer.example",
        clientId: "extension",
        audience: "catalog",
        scopes: ["openid"],
      },
    },
  ])[0];
  let calls = 0;
  let subject = me.subject;
  const f = fixture(async (url) => {
    calls++;
    return json(
      url.endsWith("/me") ? { ...me, subject } : { resourceKinds: ["agent"] },
    );
  });
  const key = "openAgenticCatalog:test-host:local";
  const saved = {
    binding: binding(selected, "test-host"),
    accessToken: "access",
    subject: "reader",
    expiresAt: Date.now() + 3600000,
    nonce: "original-login",
    authTime: 1000,
  };
  f.saved.set(key, JSON.stringify(saved));
  await f.session.select(selected);
  assert.equal((await f.session.test()).identity.subject, "reader");
  assert.equal(calls, 2);
  const restoredGuard = f.session.guard(selected);
  const cancellation = f.session.cancellation();
  await f.session.secretsChanged(key);
  restoredGuard();
  assert.equal(cancellation.aborted, false);
  assert.equal(f.session.snapshot().identity.subject, "reader");
  assert.equal(
    f.closed(),
    0,
    "unchanged restored credentials must not reselect",
  );
  f.saved.set(key, JSON.stringify({ ...saved, accessToken: "replacement" }));
  await f.session.secretsChanged(key);
  assert.throws(restoredGuard, /connection_changed/u);
  assert.equal(cancellation.aborted, true);
  assert.equal(f.session.snapshot(), undefined);
  f.saved.set(key, JSON.stringify(saved));
  await f.session.select(selected);
  const guard = f.session.guard(selected);
  subject = "other-reader";
  await assert.rejects(f.session.test(), /identity_changed/u);
  assert.throws(guard, /connection_changed/u);
  assert.equal(f.session.snapshot(), undefined);
  await assert.rejects(f.session.test(), /sign_in_required/u);
  assert.equal(calls, 3, "a subject mismatch must stop before capabilities");
  for (const change of [
    { nonce: undefined },
    { subject: undefined },
    { expiresAt: undefined },
    { nonce: "" },
    { authTime: "1000" },
  ]) {
    f.saved.set(key, JSON.stringify({ ...saved, ...change }));
    await assert.rejects(f.session.select(selected), /sign_in_required/u);
    await assert.rejects(f.session.test(), /sign_in_required/u);
    assert.equal(f.saved.size, 0);
  }
  assert.equal(calls, 3, "invalid stored login context must not reach the API");
});

test("a delayed SecretStorage read cannot reselect an earlier connection", async () => {
  const f = fixture(async () => json({ entries: [] }));
  await f.session.select(connection);
  await f.session.manual("token");
  let finish;
  f.secrets.get = () =>
    new Promise((resolve) => {
      finish = resolve;
    });
  const changed = f.session.secretsChanged();
  await Promise.resolve();
  f.secrets.get = async () => undefined;
  const next = {
    ...connection,
    id: "next",
    mode: "published",
    auth: "anonymous",
    origin: "https://next.example",
  };
  await f.session.select(next);
  finish(undefined);
  await changed;
  assert.deepEqual(await f.session.test(), {
    mode: "published",
    entryCount: 0,
  });
});

test("byte minima match native settings; opaque refresh values are not bearer values", () => {
  assert.equal(
    connections([{ ...definition, responseBytes: 1024, timeoutMs: 1000 }])[0]
      .responseBytes,
    1024,
  );
  assert.equal(opaqueToken("opaque refresh:value"), "opaque refresh:value");
  assert.throws(() => bearer("opaque refresh:value"));
  assert.throws(() => opaqueToken("token\nsecret"));
});

test("reselection waits for an in-flight sign-out instead of reloading a revoked credential", async () => {
  const { session, secrets } = fixture(async () => json({}));
  await session.select(connection);
  await session.manual("reader");
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  const original = secrets.delete;
  secrets.delete = async (key) => {
    await gate;
    await original(key);
  };
  const out = session.signOut();
  const selection = session.select(connection);
  release();
  await Promise.all([out, selection]);
  await assert.rejects(session.test(), /sign_in_required/u);
  await session.dispose();
});

test("a stale SecretStorage failure cannot clear credentials for the new connection", async () => {
  const { session, secrets } = fixture(async (input) =>
    json(String(input).endsWith("/me") ? me : { resourceKinds: ["agent"] }),
  );
  await session.select(connection);
  await session.manual("first");
  const original = secrets.get;
  let rejectRead;
  const delayed = new Promise((_, reject) => {
    rejectRead = reject;
  });
  secrets.get = () => delayed;
  const event = session.secretsChanged();
  // secretsChanged first waits for its serialized writes before it reads storage.
  await new Promise((resolve) => queueMicrotask(resolve));
  secrets.get = original;
  await session.select({ ...connection, id: "second" });
  await session.manual("second");
  rejectRead(new Error("old storage read failed"));
  await event;
  assert.equal((await session.test()).identity.subject, me.subject);
  // A current storage failure must still invalidate the active session.
  secrets.get = async () => {
    throw new Error("storage unavailable");
  };
  await assert.rejects(session.secretsChanged());
  await assert.rejects(session.test(), /sign_in_required/u);
  await session.dispose();
});

test("own SecretStorage notifications observe a complete save across sign-out and sign-in", async () => {
  const { session, secrets } = fixture(async (input) =>
    json(String(input).endsWith("/me") ? me : { resourceKinds: ["skill"] }),
  );
  const notifications = [];
  const store = secrets.store,
    remove = secrets.delete;
  secrets.store = async (k, v) => {
    await store(k, v);
    notifications.push(session.secretsChanged(k));
  };
  secrets.delete = async (k) => {
    await remove(k);
    notifications.push(session.secretsChanged(k));
  };
  await session.select(connection);
  for (let i = 0; i < 4; i++) {
    await session.manual("reader");
    await Promise.all(notifications.splice(0));
    const guard = session.guard(connection);
    await session.test();
    guard();
    await session.signOut();
  }
  await Promise.all(notifications);
  await session.dispose();
});

test(
  "delayed duplicate SecretStorage notifications preserve an unchanged session guard",
  { timeout: 5000 },
  async () => {
    const f = fixture(async (input) =>
      json(String(input).endsWith("/me") ? me : { resourceKinds: ["skill"] }),
    );
    await f.session.select(connection);
    await f.session.manual("reader");
    const guard = f.session.guard(connection);
    const cancellation = f.session.cancellation();
    const get = f.secrets.get;
    let release, started;
    const gate = new Promise((resolve) => {
      release = resolve;
    });
    const readStarted = new Promise((resolve) => {
      started = resolve;
    });
    let reads = 0;
    f.secrets.get = async (key) => {
      const value = await get(key);
      if (++reads === 2) started();
      await gate;
      return value;
    };
    const key = "openAgenticCatalog:test-host:local";
    const notifications = Promise.all([
      f.session.secretsChanged(key),
      f.session.secretsChanged(key),
    ]);
    try {
      await readStarted;
      // Verify current access while both notification reads remain in flight.
      assert.equal((await f.session.test()).identity.subject, me.subject);
      guard();
      release();
      await notifications;
      guard();
      assert.equal(f.session.cancellation(), cancellation);
      assert.equal(cancellation.aborted, false);
      assert.equal(f.session.snapshot().identity.subject, me.subject);
      assert.equal(f.closed(), 0, "unchanged credentials must not reselect");
    } finally {
      release();
      try {
        await notifications;
      } finally {
        f.secrets.get = get;
        await f.session.dispose();
      }
    }
  },
);

test("identity accepts the complete 50-action inventory and rejects excess hints", () => {
  const permissions = Array.from({ length: 50 }, (_, i) => `permission-${i}`);
  assert.equal(
    identity({ ...me, permissions, availablePermissions: permissions })
      .permissions.length,
    50,
  );
  for (const field of ["permissions", "availablePermissions"])
    assert.throws(
      () => identity({ ...me, [field]: [...permissions, "excess"] }),
      /invalid_response/u,
    );
});
test("stale OIDC callbacks cannot consume a newer login, and native cancellation clears pending state", async () => {
  let requests = 0;
  const f = fixture(() => {
    requests++;
    return json({
      issuer: "https://issuer.example",
      authorization_endpoint: "https://issuer.example/authorize",
      token_endpoint: "https://issuer.example/token",
      jwks_uri: "https://issuer.example/keys",
      response_types_supported: ["code"],
      code_challenge_methods_supported: ["S256"],
    });
  });
  const connection = connections([
    {
      ...definition,
      auth: "oidc",
      oidc: {
        issuer: "https://issuer.example",
        clientId: "client",
        audience: "catalog",
        scopes: ["openid"],
      },
    },
  ])[0];
  await f.session.select(connection);
  const old = new URL(await f.session.startLogin("vscode://extension/oidc"));
  await f.session.signOut();
  await f.session.startLogin("vscode://extension/oidc");
  assert.equal(f.session.connectionState(), "signing_in");
  await assert.rejects(
    f.session.callback(
      `vscode://extension/oidc?state=${old.searchParams.get("state")}&code=stale`,
    ),
    /invalid_login_callback/u,
  );
  assert.equal(f.session.connectionState(), "signing_in");
  assert.equal(requests, 2);
  const cancel = new AbortController();
  const completed = assert.rejects(
    f.session.waitForLogin(cancel.signal),
    /request_cancelled/u,
  );
  cancel.abort();
  await completed;
  assert.equal(f.session.connectionState(), "signed_out");
  assert.equal(f.saved.size, 0);
  await assert.rejects(
    f.session.callback("vscode://extension/oidc?state=stale&code=stale"),
    /no_pending_login/u,
  );
  await f.session.dispose();
});
test("delayed login recovery cannot erase a replacement connection's fresh credential", async () => {
  const f = fixture(() => assert.fail("recovery must not contact a Catalog"));
  await f.session.select(connection);
  await f.session.manual("old-token");
  let enter, release;
  const entered = new Promise((done) => {
      enter = done;
    }),
    held = new Promise((done) => {
      release = done;
    });
  const original = f.secrets.delete;
  f.secrets.delete = async (key) => {
    enter();
    await held;
    await original(key);
  };
  const recovering = assert.rejects(
    f.session.recoverCredentials(),
    /connection_changed/u,
  );
  await entered;
  const changed = connections([
    { ...definition, origin: "https://replacement.example" },
  ])[0];
  const selecting = f.session.select(changed);
  release();
  await Promise.all([recovering, selecting]);
  await f.session.manual("fresh-token");
  assert.equal(JSON.parse([...f.saved.values()][0]).accessToken, "fresh-token");
  assert.equal(f.session.connection().origin, "https://replacement.example");
  await f.session.dispose();
});
test("an old transport failure is classified as a changed connection without replacing the new account diagnostics", async () => {
  let enter, reject;
  const entered = new Promise((done) => {
    enter = done;
  });
  const f = fixture((url) =>
    url.startsWith("https://catalog.example")
      ? new Promise((_, fail) => {
          enter();
          reject = fail;
        })
      : json(url.endsWith("/me") ? me : { resourceKinds: ["agent"] }),
  );
  await f.session.select(connection);
  await f.session.manual("old-reader");
  const pending = assert.rejects(
    f.session.read("/api/v1/me"),
    /connection_changed/u,
  );
  await entered;
  const next = connections([
    { ...definition, id: "second", origin: "https://second.example" },
  ])[0];
  await f.session.select(next);
  await f.session.manual("reader");
  reject(new Error("private-late-transport-cause"));
  await pending;
  assert.equal(f.session.diagnostics(), undefined);
  assert.equal((await f.session.test()).identity.subject, "reader");
  await f.session.dispose();
});
