import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, writeFile, chmod, symlink, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const { options, tokenFromFile, page, line, list } = await import(
  pathToFileURL(process.env.CATALOG_ARD_EXAMPLE)
);
const { createCatalogClient } = await import(
  pathToFileURL(process.env.CATALOG_SDK_CONSUMER)
);
const input = {
  origin: "https://catalog.example.test",
  pageSize: 2,
  maxPages: 4,
  timeoutMs: 5000,
  allowLoopbackHTTP: false,
};
const entry = {
  identifier: "urn:air:catalog.example.test:example:agent",
  displayName: "Weather agent",
  type: "application/a2a-agent-card+json",
};

test("arguments bound work and keep credentials out of options", () => {
  assert.deepEqual(options(["--origin", input.origin]), {
    ...input,
    maxPages: 10,
    pageSize: 20,
  });
  for (const args of [
    [],
    ["--token", "secret"],
    ["--origin", input.origin, "extra"],
    ["--origin", input.origin, "--page-size", "0"],
    ["--origin", input.origin, "--max-pages", "101"],
    ["--origin", input.origin, "--timeout-ms", "30001"],
    ["--origin", input.origin, "--page-size", "1.5"],
    ["--origin", input.origin, "--token-file", ""],
  ])
    assert.throws(() => options(args));
});

test("List validation checks consumed fields and ignores unused extensions", () => {
  assert.deepEqual(
    page(
      {
        items: [{ ...entry, unused: { future: true } }],
        total: 1,
        extension: "ignored",
      },
      2,
    ).items,
    [entry],
  );
  assert.equal(page({ items: [], total: 0, pageToken: "" }, 2).next, undefined);
  for (const value of [
    null,
    [],
    {},
    { items: null, total: 0 },
    { items: {}, total: 0 },
    { items: [], total: null },
    { items: [], total: "0" },
    { items: [], total: -1 },
    { items: [], total: 1.5 },
    { items: [], total: Number.MAX_SAFE_INTEGER + 1 },
    { items: [], total: 0, pageToken: null },
    { items: [], total: 0, pageToken: 1 },
    { items: [], total: 0, pageToken: "x".repeat(2049) },
    { items: [entry, entry, entry], total: 3 },
    { items: [null], total: 1 },
  ])
    assert.throws(() => page(value, 2), /invalid_response/);
  for (const key of ["identifier", "displayName", "type"]) {
    for (const value of [undefined, null, 0, "", "x".repeat(2049)])
      assert.throws(() =>
        page({ items: [{ ...entry, [key]: value }], total: 1 }, 2),
      );
  }
  for (const [key, maximum] of [
    ["identifier", 2048],
    ["displayName", 512],
    ["type", 256],
  ]) {
    const value = "\u{1f600}".repeat(maximum);
    assert.equal(
      page({ items: [{ ...entry, [key]: value }], total: 1 }, 2).items[0][key],
      value,
    );
    assert.throws(
      () => page({ items: [{ ...entry, [key]: value + "a" }], total: 1 }, 2),
      /invalid_response/,
    );
  }
  assert.equal(
    page({ items: [], total: 0, pageToken: "\u00e9".repeat(1024) }, 2).next,
    "\u00e9".repeat(1024),
  );
  assert.throws(
    () => page({ items: [], total: 0, pageToken: "\u00e9".repeat(1025) }, 2),
    /invalid_response/,
  );
});

function scripted(bodies) {
  const requests = [];
  const client = createCatalogClient(
    { origin: input.origin },
    async (request) => {
      requests.push(request);
      const next = bodies.shift();
      assert.ok(next, "unexpected request");
      return new Response(JSON.stringify(next.body), {
        status: next.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    },
  );
  return { client, requests };
}

test("empty pages continue, Registry entries are skipped and criteria stay fixed", async () => {
  const { client, requests } = scripted([
    { body: { items: [], total: 2, pageToken: "first" } },
    {
      body: {
        items: [{ ...entry, type: "application/ai-registry+json" }],
        total: 2,
        pageToken: "second",
      },
    },
    { body: { items: [entry], total: 2 } },
  ]);
  const output = [];
  assert.equal(
    await list(client, input, new AbortController().signal, (value) =>
      output.push(JSON.parse(value)),
    ),
    0,
  );
  assert.deepEqual(output, [
    { kind: "entry", ...entry },
    {
      kind: "summary",
      complete: true,
      reason: "exhausted",
      pages: 3,
      resourceEntries: 1,
      registryEntriesSkipped: 1,
      reportedTotal: 2,
    },
  ]);
  assert.deepEqual(
    requests.map((r) => new URL(r.url).searchParams.get("pageToken")),
    [null, "first", "second"],
  );
  for (const request of requests) {
    const url = new URL(request.url);
    assert.equal(url.pathname, "/ard/v1/agents");
    assert.equal(url.searchParams.get("pageSize"), "2");
    assert.equal(url.searchParams.get("orderBy"), "displayName");
  }
});

test("page limits and later failures never claim completion or restart", async () => {
  for (const [bodies, maxPages, exit, reason] of [
    [
      [{ body: { items: [entry], total: 2, pageToken: "next" } }],
      1,
      2,
      "page_limit",
    ],
    [
      [
        { body: { items: [entry], total: 2, pageToken: "next" } },
        { status: 400, body: { message: "private-error-sentinel" } },
      ],
      4,
      1,
      "http_error",
    ],
    [
      [
        { body: { items: [], total: 1, pageToken: "loop" } },
        { body: { items: [], total: 1, pageToken: "loop" } },
      ],
      4,
      1,
      "repeated_page_token",
    ],
    [[{ body: { items: [], total: 3 } }], 4, 1, "invalid_response"],
    [
      [
        { body: { items: [], total: 1, pageToken: "next" } },
        { body: { items: [], total: 2 } },
      ],
      4,
      1,
      "invalid_response",
    ],
  ]) {
    const { client, requests } = scripted(bodies.slice());
    const output = [];
    assert.equal(
      await list(
        client,
        { ...input, maxPages },
        new AbortController().signal,
        (value) => output.push(JSON.parse(value)),
      ),
      exit,
    );
    const summary = output.at(-1);
    assert.equal(summary.complete, false);
    assert.equal(summary.reason, reason);
    assert.equal(requests.length, bodies.length);
    assert.ok(!JSON.stringify(output).includes("private-error-sentinel"));
  }
});

test("cancellation sends no request and metadata cannot inject terminal controls", async () => {
  const { client, requests } = scripted([]);
  const controller = new AbortController();
  controller.abort();
  const output = [];
  assert.equal(
    await list(client, input, controller.signal, (value) =>
      output.push(JSON.parse(value)),
    ),
    1,
  );
  assert.equal(output[0].reason, "cancelled");
  assert.equal(requests.length, 0);
  const raw = line({ ...entry, displayName: "name\n\u001b[31m\u009b\u2028" });
  for (const control of ["\n", "\u001b", "\u009b", "\u2028"])
    assert.ok(!raw.includes(control));
  assert.equal(JSON.parse(raw).displayName, "name\n\u001b[31m\u009b\u2028");
});

test("token files are private, bounded and reread after replacement", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ard-token-unit-"));
  try {
    const file = join(directory, "token");
    await writeFile(file, "first-token\n", { mode: 0o600 });
    const signal = new AbortController().signal;
    assert.equal(await tokenFromFile(file, signal), "first-token");
    await writeFile(file, "second-token");
    assert.equal(await tokenFromFile(file, signal), "second-token");
    for (const raw of [
      "",
      "x".repeat(8193),
      "invalid token",
      Buffer.from([0xff]),
    ]) {
      await writeFile(file, raw);
      await assert.rejects(tokenFromFile(file, signal));
    }
    await writeFile(file, "valid");
    await chmod(file, 0o644);
    await assert.rejects(tokenFromFile(file, signal));
    await chmod(file, 0o600);
    await symlink(file, join(directory, "link"));
    await assert.rejects(tokenFromFile(join(directory, "link"), signal));
    const controller = new AbortController();
    controller.abort();
    await assert.rejects(
      tokenFromFile(join(directory, "missing"), controller.signal),
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
