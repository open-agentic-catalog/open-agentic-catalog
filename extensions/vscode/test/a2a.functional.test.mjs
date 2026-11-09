import test from "node:test";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import { fixtureWait } from "../../../test/vscode/wait.mjs";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { createServer } from "node:http";
import { connect } from "node:net";
import { execFileSync } from "node:child_process";
import { a2aPeer } from "../../../test/vscode/a2a-peer.mjs";
const { a2aPlan, a2aSettings, A2AConversation, a2aClient } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const make = (card, settings, headers = {}, changed = () => {}) => {
  const plan = a2aPlan(Buffer.from(JSON.stringify(card)), settings);
  return new A2AConversation(
    plan,
    a2aClient(plan, plan.interfaces[0], settings, headers),
    async () => {},
    () => {},
    changed,
  );
};
const until = async (predicate) => {
  const end = Date.now() + 5000;
  while (Date.now() < end) {
    if (predicate()) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw Error("Owned A2A deadline");
};

async function within(promise, label) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(Error(label)), 3000);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}
const lifecycleCard = (origin) => ({
  name: "Owned lifecycle agent",
  version: "1",
  supportedInterfaces: [
    {
      url: origin + "/rpc",
      protocolBinding: "JSONRPC",
      protocolVersion: "1.0",
    },
  ],
  capabilities: { streaming: true },
  defaultInputModes: ["text/plain"],
  defaultOutputModes: ["text/plain"],
});

test("Real TLS + unchanged official Hello World: streaming, readback, consent-free transport controls and cancellation", async (t) => {
  const dir = await mkdtemp(resolve("output/vscode/a2a-tls-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-keyout",
      join(dir, "key.pem"),
      "-out",
      join(dir, "fresh.pem"),
      "-days",
      "1",
      "-subj",
      "/CN=owned-a2a",
      "-addext",
      "subjectAltName=IP:127.0.0.1",
    ],
    { stdio: "pipe", timeout: 30000 },
  );
  // Allow bounded host clock drift without changing TLS validation or host time.
  await writeFile(join(dir, "index"), "");
  await writeFile(
    join(dir, "ca.conf"),
    `[ca]\ndefault_ca=owned\n[owned]\ndatabase=${join(dir, "index")}\nnew_certs_dir=${dir}\nserial=${join(dir, "serial")}\ndefault_md=sha256\ndefault_days=1\npolicy=subject\nx509_extensions=server\n[subject]\ncommonName=supplied\n[server]\nsubjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,digitalSignature,keyCertSign\nextendedKeyUsage=serverAuth\n`,
  );
  const start =
    new Date(Date.now() - 3600000)
      .toISOString()
      .replace(/[-:T]/gu, "")
      .slice(0, 14) + "Z";
  execFileSync(
    "openssl",
    [
      "ca",
      "-batch",
      "-notext",
      "-rand_serial",
      "-selfsign",
      "-config",
      join(dir, "ca.conf"),
      "-keyfile",
      join(dir, "key.pem"),
      "-cert",
      join(dir, "fresh.pem"),
      "-ss_cert",
      join(dir, "fresh.pem"),
      "-startdate",
      start,
      "-out",
      join(dir, "cert.pem"),
    ],
    { stdio: "pipe", timeout: 30000 },
  );
  const peer = await a2aPeer({
    key: await readFile(join(dir, "key.pem")),
    cert: await readFile(join(dir, "cert.pem")),
  });
  t.after(() => peer.close());
  const settings = a2aSettings({
    caFile: join(dir, "cert.pem"),
    timeoutMs: 3000,
    streamTimeoutMs: 5000,
  });
  await assert.rejects(
    make(peer.card, a2aSettings()).send("hello", true),
    /a2a_request_failed/u,
  );
  assert.equal(peer.seen.length, 0, "Untrusted TLS must fail before HTTP");
  const proxy = createServer(),
    sockets = new Set();
  let tunnels = 0;
  proxy.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  proxy.on("connect", (request, client, head) => {
    assert.equal(request.url, new URL(peer.origin).host);
    tunnels++;
    const upstream = connect(
      Number(new URL(peer.origin).port),
      "127.0.0.1",
      () => {
        client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
        if (head.length) upstream.write(head);
        client.pipe(upstream);
        upstream.pipe(client);
      },
    );
    sockets.add(upstream);
    upstream.on("close", () => sockets.delete(upstream));
    upstream.on("error", () => client.destroy());
    client.on("error", () => upstream.destroy());
    client.on("close", () => upstream.destroy());
  });
  await listenFixture(proxy);
  t.after(
    () =>
      new Promise((r) => {
        for (const s of sockets) s.destroy();
        proxy.close(r);
      }),
  );
  const proxied = make(peer.card, {
    ...settings,
    proxyURL: `http://127.0.0.1:${proxy.address().port}`,
  });
  await proxied.send("hello", true);
  assert.equal(proxied.state.status, "completed");
  assert.equal(tunnels, 1);
  const c = make(peer.card, settings, {
    Authorization: "Bearer owned-a2a-secret",
  });
  await c.send("hello", true);
  assert.equal(c.state.status, "completed");
  assert.match(
    c.state.artifacts[0].parts.join(""),
    /Hello World! Nice to meet you!/u,
  );
  assert.equal(c.state.messages.filter((m) => m.role === "user").length, 1);
  await c.read();
  assert.equal(c.state.status, "completed");
  const previous = c.state.context;
  await c.send("hello again", true);
  assert.equal(c.state.context, previous);
  const cancel = make(peer.card, settings);
  const sending = cancel.send("hello", true);
  await until(() => cancel.state.task && cancel.state.status === "working");
  await cancel.cancel();
  await sending;
  assert.equal(cancel.state.status, "canceled");
  assert.equal(cancel.state.uncertain, false);
  assert.ok(peer.seen.some((s) => s.request.method === "CancelTask"));
  assert.ok(peer.seen.every((s) => s.headers["a2a-version"] === "1.0"));
  assert.ok(
    peer.seen.every(
      (s) =>
        !s.headers.cookie &&
        (!s.headers.authorization ||
          s.headers.authorization === "Bearer owned-a2a-secret"),
    ),
  );
  assert.ok(
    peer.seen.every(
      (s) => !JSON.stringify(s.request).includes("CATALOG_TOKEN"),
    ),
  );
  const bad = make(peer.card, settings, {
    Authorization: "Bearer wrong-target-secret",
  });
  await assert.rejects(bad.send("hello"), /a2a_auth_refused/u);
});

test("Real owned A2A tasks: input-required continuation, readback, refused cancellation, separate contexts and outage", async (t) => {
  const peer = await a2aPeer();
  t.after(() => peer.close());
  const card = {
      ...peer.card,
      supportedInterfaces: [
        { ...peer.card.supportedInterfaces[0], url: peer.origin + "/owned" },
      ],
    },
    settings = a2aSettings({ allowLoopbackHTTP: true, timeoutMs: 1000 });
  const streaming = make(card, settings);
  await streaming.send("stream", true);
  assert.deepEqual(streaming.state.artifacts[0].parts, ["one ", "two"]);
  assert.equal(streaming.state.artifacts[0].last, true);
  const interrupted = make(card, settings);
  await interrupted.send("auth", true);
  assert.equal(interrupted.state.status, "authentication-required");
  await assert.rejects(interrupted.send("must not replay"), /not_ready/u);
  const c = make(card, settings);
  await c.send("input");
  assert.equal(c.state.status, "input-required");
  const { task, context } = c.state;
  await c.send("continue");
  assert.equal(c.state.task, task);
  assert.equal(c.state.context, context);
  assert.equal(c.state.status, "completed");
  const other = make(card, settings);
  await other.send("refuse");
  assert.notEqual(other.state.context, context);
  await other.read();
  assert.equal(other.state.status, "working");
  await assert.rejects(other.cancel(), /a2a_remote_refusal/u);
  assert.equal(other.state.status, "working");
  other.disconnect();
  assert.equal(other.state.connected, false);
  assert.equal(
    peer.seen.filter((s) => s.request.method === "CancelTask").length,
    1,
  );
  const waiting = make(card, settings);
  await waiting.send("wait");
  await waiting.cancel();
  assert.equal(waiting.state.status, "canceled");
  const newConversation = make(card, settings);
  await newConversation.send("input");
  assert.notEqual(newConversation.state.context, context);
  await peer.close();
  await assert.rejects(newConversation.send("continue"), /a2a_request_failed/u);
  assert.equal(newConversation.state.uncertain, true);
});

test("Real HTTP/SSE boundaries: direct Messages, no redirects, byte/type/identity refusal, timeout closure and no uncertain replay", async (t) => {
  let mode = "message",
    contacts = 0,
    stolen = 0,
    closed = 0;
  const task = {
    id: "t",
    contextId: "c",
    status: { state: "TASK_STATE_WORKING" },
  };
  const messages = [];
  const oversizedClosed = Promise.withResolvers();
  const server = createServer(async (req, res) => {
    contacts++;
    if (req.url === "/stolen") stolen++;
    res.on("close", () => closed++);
    let body = "";
    for await (const chunk of req) body += chunk;
    const request = JSON.parse(body);
    if (mode === "message") {
      messages.push(request);
      res.setHeader("content-type", "application/json");
      res.end(
        JSON.stringify({
          jsonrpc: "2.0",
          id: request.id,
          result: {
            message: {
              messageId: `direct-${messages.length}`,
              contextId: "direct-context",
              role: "ROLE_AGENT",
              parts: [{ text: "<script>inert direct reply</script>" }],
            },
          },
        }),
      );
      return;
    }
    if (mode === "redirect") {
      res.writeHead(302, { location: "/stolen" });
      res.end();
      return;
    }
    if (mode === "limit" || mode === "large") {
      res.setHeader("content-type", "application/json");
      const raw = Buffer.from(
          JSON.stringify({
            jsonrpc: "2.0",
            id: request.id,
            result: {
              message: {
                messageId: "bounded-message",
                contextId: "bounded-context",
                role: "ROLE_AGENT",
                parts: [{ text: "bounded reply é 😀" }],
              },
            },
          }),
        ),
        size = 1024 * 1024 + Number(mode === "large"),
        bytes = Buffer.concat([raw, Buffer.alloc(size - raw.length, 0x20)]);
      assert.equal(bytes.length, size);
      assert.equal(JSON.parse(bytes.toString()).id, request.id);
      if (mode === "large") res.once("close", oversizedClosed.resolve);
      // Multiple writes require chunked framing. The oversized peer deliberately
      // remains open; the client must stop it on bytes, without waiting for EOF.
      res.write(bytes.subarray(0, 8191));
      res.write(bytes.subarray(8191));
      if (mode === "limit") res.end();
      return;
    }
    if (mode === "nontext") {
      res.setHeader("content-type", "application/json");
      res.end(
        JSON.stringify({
          jsonrpc: "2.0",
          id: request.id,
          result: {
            message: {
              messageId: "m",
              contextId: "c",
              role: "ROLE_AGENT",
              parts: [{ data: { private: "not text" } }],
            },
          },
        }),
      );
      return;
    }
    res.setHeader("content-type", "text/event-stream");
    const event = (result) =>
      res.write(
        `data: ${JSON.stringify({ jsonrpc: "2.0", id: request.id, result })}\n\n`,
      );
    event({ task });
    if (mode === "stall") return;
    if (mode === "truncated") {
      res.end(
        `data: ${JSON.stringify({ jsonrpc: "2.0", id: request.id, result: { statusUpdate: { taskId: "t", contextId: "c", status: { state: "TASK_STATE_COMPLETED" } } } })}\n`,
      );
      return;
    }
    event({
      statusUpdate: {
        taskId: "t",
        contextId: "different",
        status: { state: "TASK_STATE_COMPLETED" },
      },
    });
    res.end();
  });
  await listenFixture(server);
  t.after(
    () =>
      new Promise((r) => {
        server.closeAllConnections();
        server.close(r);
      }),
  );
  const card = {
      name: "Adverse",
      version: "1",
      supportedInterfaces: [
        {
          url: `http://127.0.0.1:${server.address().port}/rpc`,
          protocolBinding: "JSONRPC",
          protocolVersion: "1.0",
        },
      ],
      capabilities: { streaming: true },
      defaultInputModes: ["text/plain"],
      defaultOutputModes: ["text/plain"],
    },
    settings = a2aSettings({
      allowLoopbackHTTP: true,
      timeoutMs: 1000,
      streamTimeoutMs: 1000,
    });
  const direct = make(card, settings);
  await direct.send("你好");
  assert.equal(direct.state.status, "completed");
  assert.equal(direct.state.context, "direct-context");
  assert.equal(direct.state.task, undefined);
  assert.equal(
    direct.state.messages[1].text,
    "<script>inert direct reply</script>",
  );
  assert.deepEqual(direct.state.artifacts, []);
  await assert.rejects(direct.read(), /a2a_task_required/);
  assert.equal(contacts, 1, "A direct Message has no task to poll");
  await direct.send("another prompt");
  assert.equal(contacts, 2);
  assert.equal(messages[0].method, "SendMessage");
  assert.equal(messages[0].params.message.contextId, undefined);
  assert.equal(messages[0].params.message.taskId, undefined);
  assert.equal(messages[1].params.message.contextId, "direct-context");
  assert.equal(messages[1].params.message.taskId, undefined);
  assert.deepEqual(
    messages.map((r) => r.params.message.parts.map((p) => p.text)),
    [["你好"], ["another prompt"]],
  );
  assert.equal(
    direct.state.messages.filter((m) => m.role === "agent").length,
    2,
  );
  mode = "limit";
  const exact = make(card, settings);
  await exact.send("prompt");
  assert.equal(exact.state.status, "completed");
  assert.equal(exact.state.messages[1].text, "bounded reply é 😀");
  mode = "large";
  const over = make(card, settings),
    beforeOversized = contacts;
  await assert.rejects(over.send("prompt"), { code: "a2a_response_limit" });
  await fixtureWait(oversizedClosed.promise, "oversized A2A response close");
  assert.equal(over.state.uncertain, true);
  assert.equal(over.state.messages.filter((m) => m.role === "agent").length, 0);
  await assert.rejects(over.send("prompt"), /not_ready/u);
  assert.equal(contacts, beforeOversized + 1, "No oversized request replay");
  for (const selected of [
    "redirect",
    "nontext",
    "bad-context",
    "truncated",
    "stall",
  ]) {
    mode = selected;
    const c = make(card, settings);
    const before = contacts;
    await assert.rejects(
      c.send("prompt", ["bad-context", "truncated", "stall"].includes(mode)),
    );
    assert.equal(c.state.uncertain, true);
    await assert.rejects(c.send("prompt"), /not_ready/u);
    assert.equal(contacts, before + 1);
    await until(() => closed >= contacts);
    assert.equal(stolen, 0);
  }
});

test("real terminal A2A streams close without peer EOF and discard queued post-terminal artifacts", async (t) => {
  const requests = [],
    closed = Promise.withResolvers();
  let responseEnded = false;
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    const request = JSON.parse(body);
    requests.push({ method: request.method, headers: req.headers });
    res.once("finish", () => {
      responseEnded = true;
    });
    res.once("close", closed.resolve);
    res.writeHead(200, { "content-type": "text/event-stream" });
    const results = [
      {
        task: {
          id: "terminal-task",
          contextId: "terminal-context",
          status: { state: "TASK_STATE_WORKING" },
        },
      },
      {
        artifactUpdate: {
          taskId: "terminal-task",
          contextId: "terminal-context",
          artifact: {
            artifactId: "answer",
            name: "Visible answer",
            parts: [{ text: "Café 😀" }],
          },
          lastChunk: true,
        },
      },
      {
        statusUpdate: {
          taskId: "terminal-task",
          contextId: "terminal-context",
          status: { state: "TASK_STATE_COMPLETED" },
        },
      },
      {
        artifactUpdate: {
          taskId: "terminal-task",
          contextId: "terminal-context",
          artifact: {
            artifactId: "late",
            parts: [{ text: "PRIVATE queued text" }],
          },
          lastChunk: true,
        },
      },
    ];
    const bytes = Buffer.from(
      results
        .map(
          (result) =>
            `data: ${JSON.stringify({ jsonrpc: "2.0", id: request.id, result })}\n\n`,
        )
        .join(""),
    );
    const boundary = bytes.indexOf(Buffer.from("é")) + 1;
    // Split real response writes; Fetch may choose another reader chunk layout.
    res.write(bytes.subarray(0, boundary));
    setImmediate(() => res.write(bytes.subarray(boundary)));
    // No end(): completion must stop the HTTP reader, not wait for EOF/deadline.
  });
  await listenFixture(server);
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const c = make(
    lifecycleCard(`http://127.0.0.1:${server.address().port}`),
    a2aSettings({
      allowLoopbackHTTP: true,
      timeoutMs: 1000,
      streamTimeoutMs: 5000,
    }),
    { "X-Target-Key": "owned-lifecycle-credential" },
  );
  t.after(() => c.disconnect(true));
  await within(
    c.send("complete without EOF", true),
    "terminal stream waited for EOF",
  );
  await within(
    closed.promise,
    "terminal stream did not close its HTTP response",
  );
  assert.equal(responseEnded, false, "only the client closed this response");
  assert.equal(c.state.status, "completed");
  assert.equal(c.state.uncertain, false);
  assert.equal(c.state.busy, false);
  assert.equal(c.state.error, undefined);
  assert.deepEqual(c.state.artifacts, [
    { id: "answer", name: "Visible answer", parts: ["Café 😀"], last: true },
  ]);
  assert.doesNotMatch(c.export(), /PRIVATE queued text/u);
  assert.deepEqual(
    requests.map(({ method }) => method),
    ["SendStreamingMessage"],
  );
  assert.equal(
    requests[0].headers["x-target-key"],
    "owned-lifecycle-credential",
  );
  assert.equal(requests[0].headers.authorization, undefined);
  assert.equal(requests[0].headers.cookie, undefined);
});

test("real interrupted A2A streams require explicit task readback and disconnect clears local evidence without cancellation", async (t) => {
  const requests = [],
    streams = new Map(),
    closures = new Map();
  const server = createServer(async (req, res) => {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const request = JSON.parse(raw);
    requests.push(request);
    if (request.method === "GetTask") {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(
        JSON.stringify({
          jsonrpc: "2.0",
          id: request.id,
          result: {
            id: "recover-task",
            contextId: "recover-context",
            status: { state: "TASK_STATE_COMPLETED" },
            artifacts: [
              {
                artifactId: "recovered",
                name: "Recovered answer",
                parts: [{ text: "Recovered Café 😀" }],
              },
            ],
          },
        }),
      );
    }
    const clear =
        request.params.message.parts[0].text === "clear private evidence",
      taskId = clear ? "clear-task" : "recover-task",
      contextId = clear ? "clear-context" : "recover-context",
      label = clear ? "clear" : "recover";
    streams.set(label, res);
    res.once("close", () => closures.get(label).resolve());
    res.writeHead(200, { "content-type": "text/event-stream" });
    for (const result of [
      {
        task: {
          id: taskId,
          contextId,
          status: { state: "TASK_STATE_WORKING" },
        },
      },
      {
        artifactUpdate: {
          taskId,
          contextId,
          artifact: {
            artifactId: "partial",
            parts: [
              {
                text: clear
                  ? "PRIVATE local transcript"
                  : "Acknowledged partial text",
              },
            ],
          },
        },
      },
    ])
      res.write(
        `data: ${JSON.stringify({ jsonrpc: "2.0", id: request.id, result })}\n\n`,
      );
    // Each response remains open until the controlled connection loss/disconnect.
  });
  await listenFixture(server);
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const card = lifecycleCard(`http://127.0.0.1:${server.address().port}`),
    settings = a2aSettings({
      allowLoopbackHTTP: true,
      timeoutMs: 1000,
      streamTimeoutMs: 5000,
    }),
    acknowledged = Promise.withResolvers();
  closures.set("recover", Promise.withResolvers());
  const c = make(card, settings, {}, () => {
    if (c.state.artifacts[0]?.parts[0] === "Acknowledged partial text")
      acknowledged.resolve();
  });
  t.after(() => c.disconnect(true));
  const sending = c.send("lose this transport", true),
    failed = assert.rejects(sending, { code: "a2a_request_failed" });
  t.after(() => Promise.allSettled([sending, failed]));
  await within(
    acknowledged.promise,
    "task was not acknowledged before connection loss",
  );
  streams.get("recover").destroy();
  await failed;
  await within(
    closures.get("recover").promise,
    "interrupted stream did not close",
  );
  assert.equal(c.state.status, "working");
  assert.equal(c.state.task, "recover-task");
  assert.equal(c.state.context, "recover-context");
  assert.equal(c.state.uncertain, true);
  assert.equal(c.state.error, "a2a_request_failed");
  assert.equal(c.state.busy, false);
  await assert.rejects(c.send("must not replay"), {
    code: "a2a_task_not_ready",
  });
  assert.deepEqual(
    requests.map(({ method }) => method),
    ["SendStreamingMessage"],
  );
  await c.read();
  assert.equal(c.state.status, "completed");
  assert.equal(c.state.uncertain, false);
  assert.equal(c.state.error, undefined);
  assert.deepEqual(
    c.state.artifacts.map(({ id, name, parts }) => ({ id, name, parts })),
    [
      {
        id: "recovered",
        name: "Recovered answer",
        parts: ["Recovered Café 😀"],
      },
    ],
  );
  assert.deepEqual(
    requests.map(({ method }) => method),
    ["SendStreamingMessage", "GetTask"],
  );
  assert.equal(requests[1].params.id, "recover-task");

  const privateObserved = Promise.withResolvers();
  closures.set("clear", Promise.withResolvers());
  const cleared = make(card, settings, {}, () => {
    if (cleared.state.artifacts[0]?.parts[0] === "PRIVATE local transcript")
      privateObserved.resolve();
  });
  t.after(() => cleared.disconnect(true));
  const clearing = cleared.send("clear private evidence", true),
    interrupted = assert.rejects(clearing, { code: "a2a_request_interrupted" });
  t.after(() => Promise.allSettled([clearing, interrupted]));
  await within(
    privateObserved.promise,
    "private evidence was not received before disconnect",
  );
  cleared.disconnect(true);
  await interrupted;
  await within(
    closures.get("clear").promise,
    "disconnect did not close the real HTTP response",
  );
  assert.deepEqual(cleared.state, {
    status: "ready",
    messages: [],
    artifacts: [],
    busy: false,
    connected: false,
    uncertain: false,
  });
  assert.doesNotMatch(
    cleared.export(),
    /PRIVATE local transcript|clear-task|clear-context/u,
  );
  await assert.rejects(cleared.send("must not reconnect"), {
    code: "a2a_disconnected",
  });
  await assert.rejects(cleared.cancel(), { code: "a2a_cancel_unavailable" });
  assert.deepEqual(
    requests.map(({ method }) => method),
    ["SendStreamingMessage", "GetTask", "SendStreamingMessage"],
  );
});
