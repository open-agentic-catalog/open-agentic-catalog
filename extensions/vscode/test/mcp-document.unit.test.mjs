// External document policy uses injected streams and promise gates, never a network.
import test from "node:test";
import assert from "node:assert/strict";

const { ClientError, mcpDocument } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const settings = {
  origin: "https://catalog.example",
  timeoutMs: 1000,
  allowLoopbackHTTP: false,
  caFile: "/private/catalog-ca.pem",
  proxyURL: "https://catalog-proxy.example",
  oidc: { issuer: "https://catalog-issuer.example" },
};
const reference = { url: "https://native.example/server.json" };
const denyNetwork = async () =>
  assert.fail("A transport must not be allocated");

test("inline native evidence enforces the byte boundary without consent or transport", async () => {
  const consent = async () =>
    assert.fail("Inline evidence needs no URL consent");
  const exact = new Uint8Array(1024 ** 2);
  assert.equal(
    await mcpDocument({ bytes: exact }, settings, consent, denyNetwork),
    exact,
  );
  await assert.rejects(
    mcpDocument(
      { bytes: new Uint8Array(1024 ** 2 + 1) },
      settings,
      consent,
      denyNetwork,
    ),
    { code: "mcp_document_too_large" },
  );
});

test("cancelled pending document consent cannot start a later transport", async () => {
  const controller = new AbortController();
  const entered = Promise.withResolvers();
  const approved = Promise.withResolvers();
  let confirmations = 0;
  const pending = mcpDocument(
    reference,
    settings,
    async (url) => {
      assert.equal(url, reference.url);
      confirmations++;
      entered.resolve();
      return approved.promise;
    },
    denyNetwork,
    controller.signal,
  );
  await entered.promise;
  controller.abort();
  await assert.rejects(pending, { code: "mcp_start_cancelled" });
  approved.resolve(true);
  await approved.promise;
  assert.equal(confirmations, 1);
});

test("cancellation during transport allocation closes it before any fetch", async () => {
  const controller = new AbortController();
  const allocated = Promise.withResolvers();
  const entered = Promise.withResolvers();
  let closed = 0;
  const pending = mcpDocument(
    reference,
    settings,
    async () => true,
    async (connection) => {
      assert.deepEqual(connection, {
        origin: "https://native.example",
        timeoutMs: 1000,
        allowLoopbackHTTP: false,
      });
      entered.resolve();
      return allocated.promise;
    },
    controller.signal,
  );
  await entered.promise;
  controller.abort();
  allocated.resolve({
    fetch: async () => assert.fail("A cancelled document must not fetch"),
    close: async () => closed++,
  });
  await assert.rejects(pending, { code: "mcp_start_cancelled" });
  assert.equal(closed, 1);
  assert.equal(settings.origin, "https://catalog.example");
  assert.equal(settings.caFile, "/private/catalog-ca.pem");
});

test("successful bytes survive reader-cancellation failure and close the transport once", async () => {
  const expected = Buffer.from(
    '{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"io.example/unit","description":"Café","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://native.example/mcp"}]}\n',
  );
  // Split inside the UTF-8 encoding to require exact byte concatenation.
  const boundary = expected.indexOf(0xc3) + 1;
  const parts = [expected.subarray(0, boundary), expected.subarray(boundary)];
  let reads = 0,
    cancellations = 0,
    closed = 0;
  const result = await mcpDocument(
    reference,
    settings,
    async () => true,
    async () => ({
      fetch: async (url, options) => {
        assert.equal(url, reference.url);
        assert.equal(options.method, "GET");
        assert.equal(options.redirect, "manual");
        assert.deepEqual(options.headers, { accept: "application/json" });
        return {
          status: 200,
          headers: new Headers({
            "content-type": "application/vnd.example+json; charset=utf-8",
          }),
          body: {
            getReader: () => ({
              read: async () =>
                reads < parts.length
                  ? { done: false, value: parts[reads++] }
                  : { done: true },
              cancel: async () => {
                cancellations++;
                throw new Error("private stream cleanup detail");
              },
            }),
          },
        };
      },
      close: async () => closed++,
    }),
  );
  assert.deepEqual(result, expected);
  assert.equal(reads, 2);
  assert.equal(cancellations, 1);
  assert.equal(closed, 1);
});

test("wrong media and missing bodies refuse safely and close their transport", async () => {
  for (const kind of ["wrong-media", "missing-body"]) {
    let cancelled = 0,
      closed = 0;
    await assert.rejects(
      mcpDocument(
        reference,
        settings,
        async () => true,
        async () => ({
          fetch: async () => ({
            status: 200,
            headers: new Headers({
              "content-type":
                kind === "wrong-media" ? "text/html" : "application/json",
            }),
            body:
              kind === "missing-body"
                ? null
                : {
                    cancel: async () => cancelled++,
                    getReader: () => assert.fail("HTML must not be read"),
                  },
          }),
          close: async () => closed++,
        }),
      ),
      {
        code:
          kind === "wrong-media"
            ? "mcp_document_unavailable"
            : "invalid_mcp_document",
      },
    );
    assert.equal(cancelled, kind === "wrong-media" ? 1 : 0);
    assert.equal(closed, 1);
  }
});

test("owned size refusals and unexpected reader failures release both resources", async () => {
  for (const kind of ["oversize", "failed-read"]) {
    let cancelled = 0,
      closed = 0;
    await assert.rejects(
      mcpDocument(
        reference,
        settings,
        async () => true,
        async () => ({
          fetch: async () => ({
            status: 200,
            headers: new Headers({ "content-type": "application/json" }),
            body: {
              getReader: () => ({
                read: async () => {
                  if (kind === "oversize")
                    return {
                      done: false,
                      value: new Uint8Array(1024 ** 2 + 1),
                    };
                  throw new Error("private upstream credential detail");
                },
                cancel: async () => cancelled++,
              }),
            },
          }),
          close: async () => closed++,
        }),
      ),
      (error) => {
        assert.ok(error instanceof ClientError);
        assert.equal(
          error.code,
          kind === "oversize"
            ? "mcp_document_too_large"
            : "mcp_document_unavailable",
        );
        assert.doesNotMatch(error.message, /private upstream credential/u);
        return true;
      },
    );
    assert.equal(cancelled, 1);
    assert.equal(closed, 1);
  }
});
