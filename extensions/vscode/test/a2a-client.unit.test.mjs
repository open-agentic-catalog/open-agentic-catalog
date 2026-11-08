import test from "node:test";
import assert from "node:assert/strict";

const { a2aClient, a2aFrames, a2aPlan, a2aSettings, ClientError } =
  await import(process.env.CATALOG_VSCODE_LIB);
const settings = a2aSettings({ timeoutMs: 1000, streamTimeoutMs: 3000 });
const endpoint = "https://agent.example/rpc";
const plan = a2aPlan(
  Buffer.from(
    JSON.stringify({
      name: "Text agent",
      description: "Isolated client fixture",
      version: "1.0.0",
      supportedInterfaces: [
        {
          url: endpoint,
          tenant: "agent-tenant",
          protocolBinding: "JSONRPC",
          protocolVersion: "1.0",
        },
      ],
      capabilities: { streaming: true },
      defaultInputModes: ["text/plain"],
      defaultOutputModes: ["text/plain"],
      skills: [],
    }),
  ),
  settings,
);
function deferred() {
  let resolve;
  const promise = new Promise((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
}
function message(id, text = "reply") {
  return {
    jsonrpc: "2.0",
    id,
    result: {
      message: {
        messageId: "agent-message",
        contextId: "agent-context",
        role: "ROLE_AGENT",
        parts: [{ text }],
      },
    },
  };
}
function port(fetch, close = async () => {}, configured = settings) {
  return a2aClient(plan, plan.interfaces[0], configured, {}, async () => ({
    fetch,
    close,
  }));
}
function send(client, signal, stream = false, event = () => {}) {
  return client.send(
    "hello",
    "user-message",
    undefined,
    undefined,
    stream,
    signal,
    event,
  );
}
function code(expected) {
  return (error) => {
    assert.ok(error instanceof ClientError);
    assert.equal(error.code, expected);
    assert.equal(error.message.includes("PRIVATE"), false);
    return true;
  };
}

test("A2A task read and cancellation preserve tenant and separately selected transport settings", async () => {
  const configured = a2aSettings({
    timeoutMs: 1000,
    streamTimeoutMs: 3000,
    caFile: "/private/target-ca.pem",
    proxyURL: "https://proxy.example:8443",
  });
  const requests = [];
  let closed = 0;
  const client = a2aClient(
    plan,
    plan.interfaces[0],
    configured,
    { "X-Target-Key": "target-secret" },
    async (connection) => ({
      fetch: async (url, init) => {
        const request = JSON.parse(init.body);
        requests.push({ url, init, request, connection });
        return new Response(
          JSON.stringify({
            jsonrpc: "2.0",
            id: request.id,
            result: {
              id: "selected-task",
              contextId: "selected-context",
              status: {
                state:
                  request.method === "CancelTask"
                    ? "TASK_STATE_CANCELED"
                    : "TASK_STATE_WORKING",
              },
            },
          }),
          { headers: { "content-type": "application/json; charset=utf-8" } },
        );
      },
      close: async () => {
        closed++;
      },
    }),
  );
  const signal = new AbortController().signal;
  assert.equal(
    (await client.task("selected-task", false, signal)).id,
    "selected-task",
  );
  assert.equal(
    (await client.task("selected-task", true, signal)).status.state,
    "TASK_STATE_CANCELED",
  );
  assert.equal(closed, 2);
  assert.deepEqual(
    requests.map(({ request }) => ({
      method: request.method,
      params: request.params,
    })),
    [
      {
        method: "GetTask",
        params: {
          id: "selected-task",
          tenant: "agent-tenant",
          historyLength: 10,
        },
      },
      {
        method: "CancelTask",
        params: { id: "selected-task", tenant: "agent-tenant", metadata: {} },
      },
    ],
  );
  for (const { url, init, connection } of requests) {
    assert.equal(url, endpoint);
    assert.deepEqual(init.headers, {
      "X-Target-Key": "target-secret",
      "Content-Type": "application/json",
      Accept: "application/json",
      "A2A-Version": "1.0",
    });
    assert.deepEqual(connection, {
      id: "a2a",
      label: "A2A",
      origin: "https://agent.example",
      mode: "published",
      auth: "anonymous",
      allowLoopbackHTTP: false,
      timeoutMs: 1000,
      requestBytes: 65536,
      responseBytes: 1048576,
      headerBytes: 32768,
      caFile: "/private/target-ca.pem",
      proxyURL: "https://proxy.example:8443",
    });
    assert.equal(init.redirect, "manual");
    assert.equal(init.credentials, "omit");
  }
});

test("A2A caller cancellation interrupts an outstanding injected request and waits for transport disposal", async () => {
  const started = deferred(),
    disposing = deferred(),
    release = deferred();
  const controller = new AbortController();
  let requestSignal,
    closed = 0,
    emitted = 0,
    settled = false;
  const client = port(
    async (url, init) => {
      requestSignal = init.signal;
      started.resolve();
      return new Promise((resolve, reject) => {
        init.signal.addEventListener(
          "abort",
          () => reject(Error("PRIVATE transport cancellation")),
          { once: true },
        );
      });
    },
    async () => {
      closed++;
      disposing.resolve();
      await release.promise;
    },
  );
  const pending = send(client, controller.signal, false, () => emitted++);
  const outcome = assert
    .rejects(pending, code("a2a_request_interrupted"))
    .then(() => {
      settled = true;
    });
  try {
    await started.promise;
    assert.equal(requestSignal.aborted, false);
    controller.abort();
    await disposing.promise;
    assert.equal(requestSignal.aborted, true);
    assert.equal(
      settled,
      false,
      "The operation must await its transport cleanup",
    );
    assert.equal(closed, 1);
    assert.equal(emitted, 0);
  } finally {
    controller.abort();
    release.resolve();
    await outcome;
  }
  assert.equal(settled, true);
});

test("A2A deadlines use the request budget for single replies and the stream budget for streams", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  for (const stream of [false, true]) {
    const started = deferred(),
      caller = new AbortController();
    let signal,
      closed = 0;
    const client = port(
      async (url, init) => {
        signal = init.signal;
        started.resolve();
        return new Promise((resolve, reject) => {
          init.signal.addEventListener(
            "abort",
            () => reject(Error("PRIVATE deadline")),
            { once: true },
          );
        });
      },
      async () => {
        closed++;
      },
    );
    const result = assert.rejects(
      send(client, caller.signal, stream),
      code("a2a_request_interrupted"),
    );
    try {
      await started.promise;
      t.mock.timers.tick(999);
      assert.equal(signal.aborted, false);
      t.mock.timers.tick(1);
      assert.equal(signal.aborted, !stream);
      if (stream) {
        t.mock.timers.tick(1999);
        assert.equal(signal.aborted, false);
        t.mock.timers.tick(1);
        assert.equal(signal.aborted, true);
      }
      await result;
      assert.equal(closed, 1);
    } finally {
      caller.abort();
      await Promise.allSettled([result]);
    }
  }
});

test("A2A HTTP authentication and media refusals retain safe codes when response cancellation fails", async () => {
  for (const fixture of [
    { status: 200, mime: "application/json" },
    { status: 401, mime: "application/json", expected: "a2a_auth_refused" },
    { status: 403, mime: "application/json", expected: "a2a_auth_refused" },
    { status: 200, mime: "text/plain", expected: "invalid_a2a_response" },
    { status: 200, mime: "text/event-stream", stream: true },
    {
      status: 200,
      mime: "application/json",
      stream: true,
      expected: "invalid_a2a_response",
    },
  ]) {
    let canceled = 0,
      closed = 0;
    const events = [];
    const client = port(
      async (url, init) => {
        const raw = JSON.stringify(message(JSON.parse(init.body).id));
        const bytes = Buffer.from(fixture.stream ? `data: ${raw}\n\n` : raw);
        return new Response(
          new ReadableStream({
            start(controller) {
              controller.enqueue(bytes);
            },
            pull(controller) {
              if (!fixture.stream) controller.close();
            },
            cancel() {
              canceled++;
              throw Error("PRIVATE cleanup failure");
            },
          }),
          { status: fixture.status, headers: { "content-type": fixture.mime } },
        );
      },
      async () => {
        closed++;
      },
    );
    const pending = send(
      client,
      new AbortController().signal,
      fixture.stream,
      (event) => events.push(event),
    );
    if (fixture.expected) {
      await assert.rejects(pending, code(fixture.expected));
      assert.deepEqual(events, []);
      assert.equal(canceled, 1);
    } else {
      await pending;
      assert.equal(events.length, 1);
      assert.equal(events[0].message.messageId, "agent-message");
      assert.equal(events[0].message.parts[0].text, "reply");
      assert.equal(canceled, Number(fixture.stream === true));
    }
    assert.equal(closed, 1);
  }
});

test("A2A frame abort cancels a pending read, releases the reader and tolerates cancellation refusal", async () => {
  const reading = deferred(),
    controller = new AbortController();
  let canceled = 0;
  const body = new ReadableStream({
    pull() {
      reading.resolve();
    },
    cancel() {
      canceled++;
      throw Error("PRIVATE cancellation failure");
    },
  });
  const frames = a2aFrames(body, "request-id", controller.signal);
  const pending = frames.next();
  await reading.promise;
  assert.equal(body.locked, true);
  controller.abort();
  assert.deepEqual(await pending, { value: undefined, done: true });
  assert.equal(canceled, 1);
  assert.equal(body.locked, false);
});

test("Malformed UTF-8 in an A2A stream emits no event and releases the source and transport", async () => {
  for (const malformed of [false, true]) {
    let canceled = 0,
      closed = 0;
    const events = [];
    const client = port(
      async (url, init) => {
        const id = JSON.parse(init.body).id;
        const frame = Buffer.from(
          `data: ${JSON.stringify(message(id, "reply é"))}\n\n`,
        );
        const accent = frame.indexOf(Buffer.from("é"));
        assert.ok(accent > 0);
        if (malformed) frame[accent + 1] = 0x28;
        return new Response(
          new ReadableStream({
            start(controller) {
              controller.enqueue(frame);
            },
            cancel() {
              canceled++;
            },
          }),
          { headers: { "content-type": "text/event-stream" } },
        );
      },
      async () => {
        closed++;
      },
    );
    const pending = send(client, new AbortController().signal, true, (event) =>
      events.push(event),
    );
    if (malformed) {
      await assert.rejects(pending, code("a2a_request_failed"));
      assert.deepEqual(events, []);
    } else {
      await pending;
      assert.equal(events.length, 1);
      assert.equal(events[0].message.messageId, "agent-message");
      assert.equal(events[0].message.parts[0].text, "reply é");
    }
    assert.equal(canceled, 1);
    assert.equal(closed, 1);
  }
});
