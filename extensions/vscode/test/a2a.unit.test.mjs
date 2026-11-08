import test from "node:test";
import assert from "node:assert/strict";
const {
  a2aPlan,
  a2aSettings,
  a2aHeaders,
  a2aEvent,
  a2aLimits,
  A2AConversation,
  a2aClient,
  a2aFrames,
  ClientError,
} = await import(process.env.CATALOG_VSCODE_LIB);
const bytes = (v) => Buffer.from(JSON.stringify(v));
const card = {
  name: "Local agent",
  description: "Text",
  version: "1.0.0",
  supportedInterfaces: [
    {
      url: "https://agent.example/a2a",
      protocolBinding: "JSONRPC",
      protocolVersion: "1.0",
    },
  ],
  capabilities: { streaming: true },
  defaultInputModes: ["text/plain"],
  defaultOutputModes: ["text/plain"],
  skills: [],
};
const settings = a2aSettings(),
  plan = a2aPlan(bytes(card), settings);
const task = (state = "TASK_STATE_WORKING", extra = {}) => ({
  id: "t",
  contextId: "c",
  status: { state },
  ...extra,
});
const reply = (text = "hello", extra = {}) => ({
  messageId: "m",
  contextId: "c",
  role: "ROLE_AGENT",
  parts: [{ text }],
  ...extra,
});
const conversation = (
  port,
  authorize = async () => {},
  guard = () => {},
  now = () => 0,
) => new A2AConversation(plan, port, authorize, guard, () => {}, now);

test("A2A Card selection validates settings, binding, text modes and declared auth without coercion", () => {
  assert.equal(plan.interfaces[0].url, "https://agent.example/a2a");
  assert.equal(plan.auth[0].label, "Anonymous");
  for (const value of [
    { timeoutMs: 0 },
    { timeoutMs: null },
    { streamTimeoutMs: null },
    { timeoutMs: "3000" },
    { streamTimeoutMs: 300001 },
    { allowLoopbackHTTP: "true" },
    { caFile: "relative" },
    { proxyURL: "https://u:p@proxy.example" },
    { extra: 1 },
  ])
    assert.throws(() => a2aSettings(value));
  assert.equal(
    a2aSettings({ caFile: "/a/ca", proxyURL: "https://proxy.example" })
      .proxyURL,
    "https://proxy.example",
  );
  for (const url of [
    "http://127.0.0.1/a2a",
    "http://localhost/a2a",
    "https://u:p@a.example/a",
    "https://a.example/a?token=x",
    "https://a.example/#x",
  ])
    assert.ok(
      a2aPlan(
        bytes({
          ...card,
          supportedInterfaces: [{ ...card.supportedInterfaces[0], url }],
        }),
        settings,
      ).interfaces[0].reason,
    );
  assert.ok(
    a2aPlan(
      bytes({
        ...card,
        supportedInterfaces: [
          { ...card.supportedInterfaces[0], url: "http://127.0.0.1/a2a" },
        ],
      }),
      a2aSettings({ allowLoopbackHTTP: true }),
    ).interfaces[0].url,
  );
  for (const change of [
    {
      supportedInterfaces: [
        { ...card.supportedInterfaces[0], protocolVersion: "0.3" },
      ],
    },
    { defaultInputModes: ["image/png"] },
    {
      capabilities: {
        extensions: [{ uri: "https://required.example", required: true }],
      },
    },
  ])
    assert.ok(
      a2aPlan(bytes({ ...card, ...change }), settings).interfaces[0].reason,
    );
  for (const change of [
    { capabilities: { streaming: "true" } },
    { supportedInterfaces: [] },
    { defaultOutputModes: null },
    { securityRequirements: [{}] },
  ])
    assert.throws(() => a2aPlan(bytes({ ...card, ...change }), settings));
  const acceptedCard = JSON.stringify(card);
  assert.equal(a2aPlan(Buffer.from(acceptedCard), settings).name, card.name);
  for (const [from, to] of [
    ['"name":"Local agent"', '"name":"Earlier agent","name":"Local agent"'],
    [
      '"name":"Local agent"',
      '"na\\u006de":"Earlier agent","name":"Local agent"',
    ],
    ['"streaming":true', '"streaming":false,"streaming":true'],
  ]) {
    const duplicate = acceptedCard.replace(from, to);
    assert.deepEqual(JSON.parse(duplicate), card);
    assert.throws(() => a2aPlan(Buffer.from(duplicate), settings), {
      code: "invalid_a2a_card",
    });
  }
  assert.throws(() => a2aPlan(Buffer.from([0xff]), settings));
  assert.throws(
    () => a2aPlan(Buffer.alloc(a2aLimits.response + 1), settings),
    /too_large/u,
  );
  const authPlan = a2aPlan(
    bytes({
      ...card,
      securitySchemes: {
        bearer: { httpAuthSecurityScheme: { scheme: "Bearer" } },
        key: {
          apiKeySecurityScheme: { location: "header", name: "X-Agent-Key" },
        },
      },
      securityRequirements: [
        { schemes: { bearer: { list: [] }, key: { list: [] } } },
      ],
    }),
    settings,
  );
  assert.deepEqual(a2aHeaders(authPlan.auth[0], ["agent-only", "key-only"]), {
    Authorization: "Bearer agent-only",
    "X-Agent-Key": "key-only",
  });
  for (const value of ["\r\nInjected:x", " token", "a b", ""])
    assert.throws(() => a2aHeaders(authPlan.auth[0], [value, "key"]));
  for (const scheme of [
    { oauth2SecurityScheme: {} },
    { apiKeySecurityScheme: { location: "query", name: "key" } },
    { apiKeySecurityScheme: { location: "header", name: "Cookie" } },
  ]) {
    const p = a2aPlan(
      bytes({
        ...card,
        securitySchemes: { s: scheme },
        securityRequirements: [{ schemes: { s: { list: [] } } }],
      }),
      settings,
    );
    assert.ok(p.auth[0].reason);
    assert.throws(() => a2aHeaders(p.auth[0], []));
  }
});

test("A2A events reject malformed identity, non-text parts, ambiguous payloads and invalid states", () => {
  assert.equal(
    a2aEvent({ task: task("TASK_STATE_INPUT_REQUIRED") }).state,
    "input-required",
  );
  assert.equal(a2aEvent({ message: reply() }).message.text, "hello");
  for (const value of [
    {},
    { task: task(), message: reply() },
    { message: reply("x", { role: "ROLE_USER" }) },
    { message: reply("x", { contextId: undefined }) },
    { task: task("working") },
    { task: task(99) },
    {
      task: task("TASK_STATE_WORKING", {
        history: [reply("x", { taskId: "other" })],
      }),
    },
    { message: reply("x", { parts: [{ url: "https://a.example" }] }) },
    {
      artifactUpdate: {
        taskId: "t",
        contextId: "c",
        artifact: { artifactId: "a", parts: [{ text: "x" }] },
        append: "true",
      },
    },
  ])
    assert.throws(() => a2aEvent(value));
});

test("Conversation preserves streamed artifact parts and context, resumes only input-required tasks", async () => {
  let turn = 0,
    sent = [];
  const c = conversation({
    send: async (prompt, id, context, taskID, stream, signal, event) => {
      sent.push({ prompt, id, context, taskID, stream });
      turn++;
      event({
        task: task("TASK_STATE_WORKING", {
          history: [
            { messageId: id, role: "ROLE_USER", parts: [{ text: prompt }] },
          ],
        }),
      });
      if (turn === 1) {
        event({
          artifactUpdate: {
            taskId: "t",
            contextId: "c",
            artifact: { artifactId: "a", parts: [{ text: "hello " }] },
          },
        });
        event({
          artifactUpdate: {
            taskId: "t",
            contextId: "c",
            artifact: { artifactId: "a", parts: [{ text: "world" }] },
            append: true,
            lastChunk: true,
          },
        });
        event({
          statusUpdate: {
            taskId: "t",
            contextId: "c",
            status: { state: "TASK_STATE_INPUT_REQUIRED" },
          },
        });
      } else
        event({
          statusUpdate: {
            taskId: "t",
            contextId: "c",
            status: {
              state: "TASK_STATE_COMPLETED",
              message: reply("done", { taskId: "t" }),
            },
          },
        });
    },
    task: async () => task("TASK_STATE_COMPLETED"),
  });
  await c.send("hello", true);
  assert.equal(
    c.state.messages.length,
    1,
    "Echoed request is deduplicated by the actual wire message ID",
  );
  assert.deepEqual(c.state.artifacts[0].parts, ["hello ", "world"]);
  assert.equal(c.state.status, "input-required");
  await c.send("continue", true);
  assert.equal(sent[1].taskID, "t");
  assert.equal(sent[1].context, "c");
  assert.equal(c.state.status, "completed");
  assert.match(c.export(), /done/u);
});

test("Immediate messages and task readback remain explicit; new turns keep context, not terminal task IDs", async () => {
  let calls = 0,
    clock = 0,
    previous;
  const c = conversation(
    {
      send: async (p, id, context, tid, stream, signal, event) => {
        previous = { context, tid };
        calls++;
        event(calls === 1 ? { message: reply() } : { task: task() });
      },
      task: async () => task("TASK_STATE_COMPLETED"),
    },
    undefined,
    undefined,
    () => clock,
  );
  await c.send("hello");
  assert.equal(c.state.status, "completed");
  await c.send("new");
  assert.deepEqual(previous, { context: "c", tid: undefined });
  await assert.rejects(c.send("repeat"), /not_ready/u);
  await c.read();
  assert.equal(c.state.status, "completed");
  await assert.rejects(c.read(), /rate_limit/u);
  clock += 1000;
  await c.read();
  assert.equal(calls, 2, "No polling or retries");
});

test("Unknown sends cannot replay, permissions and session changes fence all traffic and delayed replies", async () => {
  let contacts = 0,
    release;
  const c = conversation({
    send: async () => {
      contacts++;
      throw Error("PRIVATE TOKEN");
    },
    task: async () => task(),
  });
  await assert.rejects(c.send("hello"), /a2a_request_failed/u);
  assert.equal(c.state.uncertain, true);
  await assert.rejects(c.send("hello"), /not_ready/u);
  assert.equal(contacts, 1);
  assert.doesNotMatch(c.export(), /PRIVATE TOKEN/u);
  const denied = conversation(
    {
      send: async () => {
        contacts++;
      },
    },
    async () => {
      throw new ClientError("permission_denied");
    },
  );
  await assert.rejects(denied.send("hello"), /permission_denied/u);
  assert.equal(contacts, 1);
  assert.equal(denied.state.uncertain, false);
  const late = conversation({
    send: async (p, id, ctx, t, s, signal, event) => {
      await new Promise((r) => (release = r));
      event({ message: reply("private-late") });
    },
  });
  const pending = late.send("old");
  await Promise.resolve();
  late.disconnect(true);
  release();
  await assert.rejects(pending, /disconnected/u);
  assert.deepEqual(late.state.messages, []);
});

test("Stream identity/sequence errors fail atomically and never disguise interruption as completion", async () => {
  for (const events of [
    [],
    [{ task: task() }],
    [
      {
        statusUpdate: {
          taskId: "t",
          contextId: "c",
          status: { state: "TASK_STATE_WORKING" },
        },
      },
    ],
    [
      { task: task() },
      { task: task("TASK_STATE_COMPLETED", { contextId: "other" }) },
    ],
    [
      { task: task() },
      {
        artifactUpdate: {
          taskId: "t",
          contextId: "c",
          artifact: { artifactId: "a", parts: [{ text: "x" }] },
          append: true,
        },
      },
    ],
  ]) {
    const c = conversation({
      send: async (p, id, ctx, t, s, signal, event) => events.forEach(event),
    });
    await assert.rejects(c.send("x", true));
    assert.equal(c.state.uncertain, true);
    assert.notEqual(c.state.status, "completed");
  }
  for (const prompt of [
    "",
    " ",
    "x".repeat(a2aLimits.prompt + 1),
    "\0",
    "\ud800",
  ])
    await assert.rejects(conversation({}).send(prompt), /invalid_a2a_prompt/u);
});

test("Explicit cancellation works while streaming and refusal never claims success; disconnect does not cancel", async () => {
  let emit,
    finish,
    cancels = 0;
  const c = conversation({
    send: async (p, id, ctx, t, s, signal, event) => {
      emit = event;
      event({ task: task() });
      await new Promise((r) => (finish = r));
    },
    task: async () => {
      cancels++;
      return task("TASK_STATE_CANCELED");
    },
  });
  const sending = c.send("wait", true);
  await Promise.resolve();
  await Promise.resolve();
  await c.cancel();
  finish();
  await sending;
  assert.equal(c.state.status, "canceled");
  assert.equal(cancels, 1);
  emit({ task: task("TASK_STATE_WORKING", { contextId: "other" }) });
  assert.equal(
    c.state.status,
    "canceled",
    "Late stream events cannot regress confirmed cancellation",
  );
  const refusal = conversation({
    send: async (p, id, ctx, t, s, signal, event) => event({ task: task() }),
    task: async () => {
      throw Error("SECRET");
    },
  });
  await refusal.send("wait");
  await assert.rejects(refusal.cancel(), /a2a_cancel_failed/u);
  assert.equal(refusal.state.status, "working");
  refusal.disconnect();
  assert.equal(cancels, 1);
  assert.equal(refusal.state.connected, false);
});

function fakeNetwork(responder) {
  let closed = 0,
    requests = [];
  return {
    network: async (connection) => ({
      fetch: async (url, init) => {
        requests.push({ url, init, connection });
        return responder(JSON.parse(init.body), init);
      },
      close: async () => {
        closed++;
      },
    }),
    requests,
    closed: () => closed,
  };
}
test("Official SDK transport pins method/endpoint/version and target credentials, closes every operation", async () => {
  const io = fakeNetwork(
    (r) =>
      new Response(
        JSON.stringify({
          jsonrpc: "2.0",
          id: r.id,
          result:
            r.method === "GetTask"
              ? task("TASK_STATE_COMPLETED")
              : { message: reply() },
        }),
        { headers: { "content-type": "application/json" } },
      ),
  );
  const port = a2aClient(
    plan,
    plan.interfaces[0],
    settings,
    { Authorization: "Bearer TARGET" },
    io.network,
  );
  const c = conversation(port);
  await c.send("only prompt");
  assert.equal(io.requests[0].init.headers.Authorization, "Bearer TARGET");
  assert.equal(io.requests[0].init.headers["A2A-Version"], "1.0");
  assert.equal(io.requests[0].init.redirect, "manual");
  assert.equal(io.requests[0].init.credentials, "omit");
  assert.equal(io.requests[0].connection.caFile, undefined);
  assert.equal(io.closed(), 1);
  assert.deepEqual(JSON.parse(io.requests[0].init.body).params.message.parts, [
    { text: "only prompt", mediaType: "text/plain" },
  ]);
  assert.deepEqual(io.requests[0].init.headers, {
    Authorization: "Bearer TARGET",
    "Content-Type": "application/json",
    Accept: "application/json",
    "A2A-Version": "1.0",
  });
});

test("Wire validation precedes SDK coercion; malformed/refused/redirect responses close bodies and transport", async () => {
  for (const response of [
    () =>
      new Response(
        JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          result: { message: reply("x", { messageId: 42 }) },
        }),
        { headers: { "content-type": "application/json" } },
      ),
    () =>
      new Response('{"jsonrpc":"2.0","id":99,"result":{}}', {
        headers: { "content-type": "application/json" },
      }),
    () => new Response("SECRET", { status: 503 }),
    () =>
      new Response("PRIVATE", {
        status: 302,
        headers: { location: "https://external.example" },
      }),
    () =>
      new Response(
        JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          error: { code: -32001, message: "PRIVATE" },
        }),
        { headers: { "content-type": "application/json" } },
      ),
  ]) {
    const io = fakeNetwork(response);
    await assert.rejects(
      conversation(
        a2aClient(plan, plan.interfaces[0], settings, {}, io.network),
      ).send("x"),
      (e) => !e.message.includes("PRIVATE") && !e.message.includes("SECRET"),
    );
    assert.equal(io.requests.length, 1);
    assert.equal(io.closed(), 1);
  }
});

test("SSE accepts split UTF-8 and CR/LF frames with bounded bytes and exact RPC IDs", async () => {
  const raw = Buffer.from(
    `: heartbeat\r\ndata: ${JSON.stringify({ jsonrpc: "2.0", id: 1, result: { message: reply("hi 😀") } })}\r\n\r\n`,
  );
  let canceled = false,
    index = 0;
  const body = new ReadableStream({
    pull(c) {
      if (index < raw.length) c.enqueue(raw.subarray(index, ++index));
      else c.close();
    },
    cancel() {
      canceled = true;
    },
  });
  const frames = [];
  for await (const f of a2aFrames(body, 1)) frames.push(f);
  assert.equal(frames.length, 1);
  assert.match(frames[0], /hi 😀/u);
  for (const raw of [
    "data: {}\n\n",
    "data: " + "x".repeat(a2aLimits.event + 1),
    'data: {"jsonrpc":"2.0","id":2,"result":{}}\r\r',
  ]) {
    await assert.rejects(async () => {
      for await (const f of a2aFrames(new Response(raw).body, 1)) void f;
    });
  }
  void canceled;
});

test("Official SDK streaming and task operations use validated envelopes and release the stream on terminal state", async () => {
  let cancelled = false;
  const io = fakeNetwork((r) => {
    if (r.method !== "SendStreamingMessage")
      return new Response(
        JSON.stringify({
          jsonrpc: "2.0",
          id: r.id,
          result: task(
            r.method === "CancelTask"
              ? "TASK_STATE_CANCELED"
              : "TASK_STATE_WORKING",
          ),
        }),
        { headers: { "content-type": "application/json" } },
      );
    const frames = [
      { task: task() },
      {
        artifactUpdate: {
          taskId: "t",
          contextId: "c",
          artifact: { artifactId: "a", parts: [{ text: "result" }] },
          lastChunk: true,
        },
      },
      {
        statusUpdate: {
          taskId: "t",
          contextId: "c",
          status: { state: "TASK_STATE_COMPLETED" },
        },
      },
    ];
    let i = 0;
    return new Response(
      new ReadableStream({
        pull(c) {
          if (i < frames.length)
            c.enqueue(
              Buffer.from(
                `data: ${JSON.stringify({ jsonrpc: "2.0", id: r.id, result: frames[i++] })}\n\n`,
              ),
            );
        },
        cancel() {
          cancelled = true;
        },
      }),
      { headers: { "content-type": "text/event-stream" } },
    );
  });
  const port = a2aClient(plan, plan.interfaces[0], settings, {}, io.network),
    c = conversation(port);
  await c.send("stream", true);
  assert.equal(c.state.status, "completed");
  assert.equal(c.state.artifacts[0].parts[0], "result");
  assert.equal(cancelled, true);
  assert.equal(io.closed(), 1);
  const got = await port.task("t", false, new AbortController().signal);
  assert.equal(got.id, "t");
  const canceled = await port.task("t", true, new AbortController().signal);
  assert.equal(canceled.status.state, "TASK_STATE_CANCELED");
  assert.equal(io.closed(), 3);
});

test("Stream byte/event bounds cancel the source, including ignored heartbeat frames", async () => {
  for (const payload of [
    Array.from(
      { length: a2aLimits.events + 1 },
      () =>
        `data: ${JSON.stringify({ jsonrpc: "2.0", id: 1, result: { task: task() } })}\n\n`,
    ).join(""),
    Array.from({ length: 65 }, () => ":" + "h".repeat(65535) + "\n\n").join(""),
  ]) {
    let canceled = false;
    const body = new ReadableStream({
      start(c) {
        c.enqueue(Buffer.from(payload));
      },
      cancel() {
        canceled = true;
      },
    });
    await assert.rejects(async () => {
      for await (const f of a2aFrames(body, 1)) void f;
    }, /a2a_(?:stream|event)_limit/u);
    assert.equal(canceled, true);
  }
});

test("Cancellation requires exact identity and confirmed canceled state; a prior terminal result cannot hide denial", async () => {
  for (const result of [task(), task("TASK_STATE_CANCELED", { id: "wrong" })]) {
    const c = conversation({
      send: async (p, id, ctx, t, s, signal, event) => event({ task: task() }),
      task: async () => result,
    });
    await c.send("wait");
    await assert.rejects(c.cancel());
    assert.notEqual(c.state.status, "canceled");
  }
  let denied = false;
  const c = conversation(
    {
      send: async (p, id, ctx, t, s, signal, event) => event({ task: task() }),
      task: async () => task("TASK_STATE_CANCELED"),
    },
    async () => {
      if (denied) throw new ClientError("permission_denied");
    },
  );
  await c.send("wait");
  await c.cancel();
  denied = true;
  await assert.rejects(c.send("new"), /permission_denied/u);
});

test("Transcript admission is atomic, includes JSON escaping, and frees terminal artifacts for the next turn", async () => {
  let calls = 0;
  const port = {
    send: async (p, id, ctx, t, s, signal, event) => {
      calls++;
      event({ message: reply() });
    },
  };
  const full = conversation(port);
  full.state.messages = Array.from({ length: a2aLimits.messages }, (_, i) => ({
    id: String(i),
    role: "agent",
    text: "small",
  }));
  await assert.rejects(full.send("extra"), /transcript_limit/u);
  assert.equal(full.state.messages.length, a2aLimits.messages);
  assert.equal(full.state.status, "ready");
  assert.equal(full.state.uncertain, false);
  assert.equal(calls, 0);
  const escaped = conversation(port);
  escaped.state.messages = [
    {
      id: "large",
      role: "agent",
      text: "x".repeat(a2aLimits.transcript - 2000),
    },
  ];
  await assert.rejects(
    escaped.send("a" + "\n".repeat(a2aLimits.prompt - 1)),
    /transcript_limit/u,
  );
  assert.equal(escaped.state.messages.length, 1);
  assert.equal(calls, 0);
  const next = conversation(port);
  next.state.status = "completed";
  next.state.artifacts = [
    {
      id: "old",
      name: "Old",
      parts: ["x".repeat(a2aLimits.transcript - 100)],
      last: true,
    },
  ];
  await next.send("hello");
  assert.equal(calls, 1);
  assert.deepEqual(next.state.artifacts, []);
});

test("Incomplete SSE events at EOF never manufacture a completed task", async () => {
  const prefix = `data: ${JSON.stringify({ jsonrpc: "2.0", id: 1, result: { task: task("TASK_STATE_WORKING") } })}\n\n`;
  const ending = `data: ${JSON.stringify({ jsonrpc: "2.0", id: 1, result: { statusUpdate: { taskId: "t", contextId: "c", status: { state: "TASK_STATE_COMPLETED" } } } })}`;
  for (const suffix of ["", "\n", "\r", "\r\n"]) {
    const frames = [];
    for await (const f of a2aFrames(
      new Response(prefix + ending + suffix).body,
      1,
    ))
      frames.push(f);
    assert.equal(
      frames.length,
      1,
      "EOF discards an unfinished event; only blank lines dispatch it",
    );
  }
  for (const suffix of ["\n\n", "\r\r", "\r\n\r\n"]) {
    const frames = [];
    for await (const f of a2aFrames(
      new Response(prefix + ending + suffix).body,
      1,
    ))
      frames.push(f);
    assert.equal(frames.length, 2);
  }
  const io = fakeNetwork(
    () =>
      new Response(prefix + ending + "\n", {
        headers: { "content-type": "text/event-stream" },
      }),
  );
  const c = conversation(
    a2aClient(plan, plan.interfaces[0], settings, {}, io.network),
  );
  await assert.rejects(c.send("hello", true), /a2a_stream_interrupted/u);
  assert.equal(c.state.status, "working");
  assert.equal(c.state.uncertain, true);
  assert.equal(io.requests.length, 1);
  assert.equal(io.closed(), 1);
});

test("Conversation activity separates authorization, transport and settled outcomes without changing task state", async () => {
  let allow, entered;
  const waiting = new Promise((resolve) => {
    entered = resolve;
  });
  const gate = new Promise((resolve) => {
    allow = resolve;
  });
  const observed = [];
  let c;
  c = new A2AConversation(
    plan,
    {
      send: async (prompt, id, context, taskID, stream, signal, emit) => {
        assert.equal(c.state.activity, "streaming");
        emit({ task: task("TASK_STATE_COMPLETED") });
      },
      task: async () => {
        assert.equal(c.state.activity, "reading");
        return task("TASK_STATE_COMPLETED");
      },
    },
    async () => {
      entered();
      await gate;
    },
    () => {},
    () => observed.push(c.state.activity),
  );
  const sending = c.send("hi", true);
  await waiting;
  assert.equal(c.state.activity, "preparing");
  assert.equal(c.state.status, "ready");
  allow();
  await sending;
  assert.equal(c.state.activity, undefined);
  assert.equal(c.state.status, "completed");
  assert.ok(observed.includes("preparing"));
  assert.ok(observed.includes("streaming"));
  await c.read();
  assert.equal(c.state.activity, undefined);
  c.disconnect();
  assert.equal(c.state.activity, undefined);
});
