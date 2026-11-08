// Inspect the transport's trust and request boundaries without opening a connection.
import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";
import { mkdtemp, open, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { rootCertificates } from "node:tls";

const moduleURL = new URL(
  "./transport.js",
  pathToFileURL(process.env.CATALOG_VSCODE_LIB),
).href;
let networkLoaded = false;
let filesLoaded = false;
const hooks = registerHooks({
  resolve(specifier, context, next) {
    if (context.parentURL === moduleURL) {
      if (specifier === "undici")
        return { url: "transport-unit:undici", shortCircuit: true };
      if (specifier === "node:fs/promises")
        return { url: "transport-unit:files", shortCircuit: true };
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === "transport-unit:files") {
      filesLoaded = true;
      return {
        format: "module",
        source:
          "export const open=(...args)=>globalThis.catalogTransportFixture.open(...args);",
        shortCircuit: true,
      };
    }
    if (url === "transport-unit:undici") {
      networkLoaded = true;
      return {
        format: "module",
        source: `
          class Dispatcher {
            constructor(kind, options) {
              this.fixture = globalThis.catalogTransportFixture;
              this.fixture.created.push({ kind, options, dispatcher: this });
            }
            async destroy() {
              this.fixture.destroyed.push(this);
              await this.fixture.destroy();
            }
          }
          export class Agent extends Dispatcher {
            constructor(options) { super("direct", options); }
          }
          export class ProxyAgent extends Dispatcher {
            constructor(options) { super("proxy", options); }
          }
          export const fetch=(...args)=>globalThis.catalogTransportFixture.fetch(...args);
        `,
        shortCircuit: true,
      };
    }
    return next(url, context);
  },
});
const { transport } = await import(moduleURL);
const { connections } = await import(new URL("./config.js", moduleURL));
assert.equal(
  networkLoaded,
  true,
  "The Undici edge must be replaced before tests",
);
assert.equal(filesLoaded, true, "The file-handle edge must be controllable");
const globalFetch = test.mock.method(globalThis, "fetch", () => {
  assert.fail("The transport unit must not fall back to global network fetch");
});
test.after(() => {
  globalFetch.mock.restore();
  hooks.deregister();
  delete globalThis.catalogTransportFixture;
});

const connection = connections([
  {
    id: "owned",
    label: "Owned",
    origin: "https://catalog.example",
    mode: "control",
    auth: "manual",
  },
])[0];

function fixture() {
  const value = {
    created: [],
    requests: [],
    destroyed: [],
    open,
    destroy: async () => {},
    response: new Response(null, { status: 204 }),
  };
  value.fetch = (input, init) => {
    value.requests.push({ input, init });
    return value.response;
  };
  globalThis.catalogTransportFixture = value;
  return value;
}

test("direct transport keeps verified TLS, header bounds and caller request inputs", async () => {
  const f = fixture();
  const client = await transport(connection);
  assert.equal(f.created.length, 1);
  const { dispatcher } = f.created[0];
  assert.deepEqual(f.created[0], {
    kind: "direct",
    dispatcher,
    options: {
      connect: { rejectUnauthorized: true, allowH2: false },
      allowH2: false,
      maxHeaderSize: 32768,
    },
  });
  assert.deepEqual(f.requests, [], "Constructing a transport sends no request");
  const abort = new AbortController();
  const init = Object.freeze({
    method: "POST",
    headers: { "x-owned-header": "literal value" },
    body: new Uint8Array([0, 127, 255]),
    redirect: "manual",
    signal: abort.signal,
  });
  for (const input of [
    "https://catalog.example/api/v1/resources?limit=1",
    new URL("https://catalog.example/api/v1/resources?limit=1"),
  ])
    assert.equal(await client.fetch(input, init), f.response);
  const expected = {
    method: "POST",
    headers: { "x-owned-header": "literal value" },
    body: new Uint8Array([0, 127, 255]),
    redirect: "manual",
    signal: abort.signal,
    dispatcher,
  };
  assert.deepEqual(f.requests, [
    {
      input: "https://catalog.example/api/v1/resources?limit=1",
      init: expected,
    },
    {
      input: "https://catalog.example/api/v1/resources?limit=1",
      init: expected,
    },
  ]);
  assert.deepEqual(init.headers, { "x-owned-header": "literal value" });
  assert.deepEqual(init.body, new Uint8Array([0, 127, 255]));
  await client.close();
  assert.deepEqual(f.destroyed, [dispatcher]);
});

test("Request inputs retain method, headers, cancellation, redirect and exact body bytes", async () => {
  const f = fixture();
  const client = await transport(connection);
  const abort = new AbortController();
  const request = new Request("https://catalog.example/api/v1/drafts", {
    method: "PATCH",
    headers: {
      "Content-Type": "application/json",
      "X-Catalog-Request": "1",
    },
    body: '{"name":"é😀"}\n',
    redirect: "manual",
    signal: abort.signal,
  });
  assert.equal(await client.fetch(request), f.response);
  assert.deepEqual(f.requests, [
    {
      input: "https://catalog.example/api/v1/drafts",
      init: {
        method: "PATCH",
        headers: {
          "content-type": "application/json",
          "x-catalog-request": "1",
        },
        body: new Uint8Array(Buffer.from('{"name":"é😀"}\n', "utf8")),
        redirect: "manual",
        signal: request.signal,
        dispatcher: f.created[0].dispatcher,
      },
    },
  ]);
  abort.abort("owned cancellation");
  assert.equal(f.requests[0].init.signal.aborted, true);
  assert.equal(f.requests[0].init.signal.reason, "owned cancellation");
  await client.fetch(new Request("https://catalog.example/api/v1/me"));
  assert.equal(f.requests[1].init.method, "GET");
  assert.equal(Object.hasOwn(f.requests[1].init, "body"), false);
  await client.close();
  assert.deepEqual(f.destroyed, [f.created[0].dispatcher]);
});

test("explicit proxy applies additive private trust and verified TLS to both hops", async (t) => {
  const f = fixture();
  const directory = await mkdtemp(join(tmpdir(), "catalog-transport-unit-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const caFile = join(directory, "ca.pem");
  const pem = rootCertificates[0] + "\n";
  await writeFile(caFile, pem, { mode: 0o600 });
  const client = await transport({
    ...connection,
    caFile,
    proxyURL: "https://proxy.example:8443",
    headerBytes: 8192,
  });
  const { dispatcher } = f.created[0];
  const tls = {
    ca: [...rootCertificates, pem],
    rejectUnauthorized: true,
    allowH2: false,
  };
  assert.deepEqual(f.created, [
    {
      kind: "proxy",
      dispatcher,
      options: {
        uri: "https://proxy.example:8443",
        allowH2: false,
        maxHeaderSize: 8192,
        requestTls: tls,
        proxyTls: tls,
      },
    },
  ]);
  assert.deepEqual(f.requests, []);
  await client.close();
  assert.deepEqual(f.destroyed, [dispatcher]);
});

test("trust file admission includes the exact one-MiB byte limit", async (t) => {
  const f = fixture();
  const directory = await mkdtemp(join(tmpdir(), "catalog-transport-unit-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const caFile = join(directory, "maximum.pem");
  const bytes = Buffer.alloc(1024 ** 2, " ");
  Buffer.from(rootCertificates[0], "utf8").copy(bytes);
  await writeFile(caFile, bytes, { mode: 0o600 });
  const client = await transport({ ...connection, caFile });
  assert.equal(f.created.length, 1);
  assert.equal(f.created[0].options.connect.ca.at(-1), bytes.toString("utf8"));
  assert.deepEqual(f.requests, []);
  await client.close();
  assert.deepEqual(f.destroyed, [f.created[0].dispatcher]);
});

test("invalid trust files are refused before constructing a dispatcher or sending data", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-transport-unit-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const caFile = join(directory, "ca.pem");
  for (const bytes of [
    Buffer.from("ordinary text without a PEM certificate"),
    Buffer.concat([
      Buffer.from(rootCertificates[0]),
      Buffer.alloc(1024 ** 2 + 1),
    ]),
  ]) {
    const f = fixture();
    await writeFile(caFile, bytes, { mode: 0o600 });
    await assert.rejects(transport({ ...connection, caFile }), {
      code: "invalid_trust_file",
    });
    assert.deepEqual(f.created, []);
    assert.deepEqual(f.requests, []);
  }
  const f = fixture();
  await assert.rejects(
    transport({ ...connection, caFile: join(directory, "missing.pem") }),
    { code: "ENOENT" },
  );
  assert.deepEqual(f.created, []);
  assert.deepEqual(f.requests, []);
});

test("trust reads enforce the byte bound after stat and close their handle on overflow", async () => {
  const f = fixture();
  const chunks = [Buffer.alloc(1024 ** 2, " "), Buffer.from("x")];
  Buffer.from(rootCertificates[0]).copy(chunks[0]);
  const calls = [];
  let closed = 0;
  f.open = async (path, flags) => {
    assert.equal(path, "/owned/growing.pem");
    assert.equal(flags, "r");
    return {
      stat: async () => ({ isFile: () => true, size: 1024 ** 2 }),
      read: async (buffer, offset, length, position) => {
        calls.push({ offset, length, position });
        const chunk = chunks.shift();
        chunk.copy(buffer, offset);
        return { bytesRead: chunk.length };
      },
      close: async () => closed++,
    };
  };
  await assert.rejects(
    transport({ ...connection, caFile: "/owned/growing.pem" }),
    { code: "invalid_trust_file" },
  );
  assert.deepEqual(calls, [
    { offset: 0, length: 1024 ** 2 + 1, position: null },
    { offset: 1024 ** 2, length: 1, position: null },
  ]);
  assert.equal(closed, 1);
  assert.deepEqual(f.created, []);
  assert.deepEqual(f.requests, []);
});

test("trust admission closes handles on non-files, stat failures and read failures", async () => {
  for (const stage of ["non-file", "stat", "read"]) {
    const f = fixture();
    const failure = new Error("owned file failure");
    let closed = 0;
    f.open = async () => ({
      stat: async () => {
        if (stage === "stat") throw failure;
        return { isFile: () => stage !== "non-file", size: 10 };
      },
      read: async () => {
        throw failure;
      },
      close: async () => closed++,
    });
    await assert.rejects(
      transport({ ...connection, caFile: "/owned/unreadable.pem" }),
      stage === "non-file"
        ? { code: "invalid_trust_file" }
        : (error) => error === failure,
    );
    assert.equal(closed, 1);
    assert.deepEqual(f.created, []);
    assert.deepEqual(f.requests, []);
  }
});

test("close awaits dispatcher disposal and propagates disposal failures", async () => {
  const f = fixture();
  const disposal = Promise.withResolvers();
  f.destroy = () => disposal.promise;
  const client = await transport(connection);
  let settled = false;
  const closing = client.close().then(() => {
    settled = true;
  });
  try {
    await Promise.resolve();
    assert.equal(settled, false);
  } finally {
    disposal.resolve();
    await Promise.allSettled([closing]);
  }
  await closing;
  assert.equal(settled, true);
  assert.deepEqual(f.destroyed, [f.created[0].dispatcher]);

  const failed = fixture();
  const failure = new Error("owned dispatcher disposal failure");
  failed.destroy = async () => {
    throw failure;
  };
  await assert.rejects((await transport(connection)).close(), (error) => {
    assert.equal(error, failure);
    return true;
  });
  assert.deepEqual(failed.destroyed, [failed.created[0].dispatcher]);
});
