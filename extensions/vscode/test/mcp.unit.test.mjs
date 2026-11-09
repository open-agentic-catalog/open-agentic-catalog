import test from "node:test";
import assert from "node:assert/strict";
const { mcpAlternatives, mcpURL, inputValue, McpSelections, mcpDocument } =
  await import(process.env.CATALOG_VSCODE_LIB);
const card = {
  $schema:
    "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
  name: "io.example/owned",
  description: "Owned MCP fixture",
  version: "1.0.0",
  remotes: [{ type: "streamable-http", url: "https://owned.example/mcp" }],
};
const registry = {
  name: "io.example/owned",
  description: "Owned MCP fixture",
  version: "1.0.0",
  packages: [
    {
      registryType: "npm",
      identifier: "@owned/mcp",
      version: "1.2.3",
      transport: { type: "stdio" },
    },
  ],
};
const bytes = (value) => Buffer.from(JSON.stringify(value));
const alternatives = (value) =>
  mcpAlternatives(bytes(value), false, "application/vnd.mcp.server+json");

test("MCP document families stay distinct; an absent schema uses the declared media type", () => {
  assert.equal(mcpAlternatives(bytes(card))[0].recipe.family, "Server Card");
  assert.equal(alternatives(registry)[0].recipe.family, "Registry server.json");
  assert.throws(
    () =>
      mcpAlternatives(
        bytes({ ...card, $schema: undefined }),
        false,
        "application/mcp-server-card+json",
      ),
    /unknown_mcp_document_family/u,
  );
  assert.throws(
    () =>
      mcpAlternatives(bytes(card), false, "application/vnd.mcp.server+json"),
    /invalid_mcp_document/u,
  );
  for (const description of [undefined, "", "x".repeat(101)])
    assert.throws(() => alternatives({ ...registry, description }));
  assert.throws(
    () => mcpAlternatives(bytes(registry)),
    /unknown_mcp_document_family/u,
  );
  assert.throws(
    () => alternatives({ ...registry, $schema: "https://untrusted/schema" }),
    /unknown_mcp_document_family/u,
  );
  assert.throws(
    () => mcpAlternatives(Buffer.from('{"name":"x","name":"y"}')),
    /invalid_mcp_document/u,
  );
  for (const [from, to] of [
    [
      '"name":"io.example/owned"',
      '"name":"io.example/earlier","name":"io.example/owned"',
    ],
    [
      '"name":"io.example/owned"',
      '"na\\u006de":"io.example/earlier","name":"io.example/owned"',
    ],
    [
      '"url":"https://owned.example/mcp"',
      '"url":"https://earlier.example/mcp","url":"https://owned.example/mcp"',
    ],
  ]) {
    const duplicate = JSON.stringify(card).replace(from, to);
    assert.deepEqual(JSON.parse(duplicate), card);
    assert.throws(() => mcpAlternatives(Buffer.from(duplicate)), {
      code: "invalid_mcp_document",
    });
  }
  assert.throws(
    () => mcpAlternatives(new Uint8Array(1024 ** 2 + 1)),
    /too_large/u,
  );
  assert.throws(
    () => alternatives({ ...registry, name: "--inject" }),
    /invalid_mcp_document/u,
  );
  assert.throws(
    () => alternatives({ ...registry, packages: [] }),
    /no_mcp_alternatives/u,
  );
});
test("reviewed recipes preserve exact argv without shell interpolation", () => {
  const npm = alternatives({
    ...registry,
    packages: [
      {
        ...registry.packages[0],
        runtimeHint: "npx",
        packageArguments: [
          { type: "named", name: "--label", value: "$(do-not-execute); space" },
          { type: "positional", value: "some path" },
        ],
      },
    ],
  })[0].recipe;
  assert.deepEqual(npm.args, [
    "--yes",
    "--",
    "@owned/mcp@1.2.3",
    "--label=$(do-not-execute); space",
    "some path",
  ]);
  assert.ok(
    npm.description.includes(
      'arguments: ["--yes","--","@owned/mcp@1.2.3","--label=$(do-not-execute); space","some path"]',
    ),
    "The consent preview must show each exact argument, including spaces",
  );
  const uv = alternatives({
    ...registry,
    packages: [
      {
        registryType: "pypi",
        identifier: "owned-mcp",
        version: "1.2.3",
        transport: { type: "stdio" },
      },
    ],
  })[0].recipe;
  assert.equal(uv.command, "uvx");
  assert.deepEqual(uv.args, ["owned-mcp==1.2.3"]);
  const image = "example.com/owned/mcp@sha256:" + "a".repeat(64);
  const docker = alternatives({
    ...registry,
    packages: [
      {
        registryType: "oci",
        identifier: image,
        transport: { type: "stdio" },
        environmentVariables: [{ name: "TARGET_TOKEN", isSecret: true }],
      },
    ],
  })[0].recipe;
  assert.deepEqual(docker.args, [
    "run",
    "--rm",
    "-i",
    "--env",
    "TARGET_TOKEN",
    image,
  ]);
});
test("unsafe, ambiguous and unsupported package declarations remain inspect-only", () => {
  for (const change of [
    { version: "latest" },
    { version: "^1.2.3" },
    { version: "1.*" },
    { version: "1" },
    { identifier: "https://bad/package.tgz" },
    { identifier: "--flag" },
    { runtimeHint: "bash" },
    {
      runtimeArguments: [
        { type: "named", name: "--privileged", value: "true" },
      ],
    },
    { fileSha256: "a".repeat(64) },
    { registryBaseUrl: "https://another.registry" },
    { registryType: "cargo" },
    { transport: { type: "sse" } },
    { packageArguments: [{ type: "positional", valueHint: "path" }] },
    { packageArguments: [{ type: "positional", value: "{path}" }] },
    {
      packageArguments: [
        { type: "positional", value: "secret", isSecret: true },
      ],
    },
    {
      environmentVariables: [{ name: "NODE_OPTIONS", value: "--require=/bad" }],
    },
    { environmentVariables: [{ name: "PATH", value: "/bad" }] },
    {
      environmentVariables: [
        { name: "TOKEN", isSecret: true, value: "embedded-secret" },
      ],
    },
  ]) {
    const result = alternatives({
      ...registry,
      packages: [{ ...registry.packages[0], ...change }],
    })[0];
    assert.equal(result.recipe, undefined, JSON.stringify(change));
    assert.ok(result.reason);
  }
  assert.equal(
    alternatives({
      ...registry,
      packages: [
        {
          registryType: "oci",
          identifier: "owned/mcp:latest",
          transport: { type: "stdio" },
        },
      ],
    })[0].recipe,
    undefined,
  );
});
test("remote endpoints, header names and templates are checked before selection", () => {
  for (const remote of [
    { type: "sse", url: "https://owned.example/events" },
    { type: "streamable-http", url: "https://user:password@owned.example/mcp" },
    { type: "streamable-http", url: "https://owned.example/{tenant}" },
    {
      type: "streamable-http",
      url: "https://owned.example/mcp",
      headers: [{ name: "Host", value: "other" }],
    },
    {
      type: "streamable-http",
      url: "https://owned.example/mcp",
      headers: [{ name: "Authorization", value: "Bearer embedded" }],
    },
    {
      type: "streamable-http",
      url: "https://owned.example/mcp",
      headers: [
        { name: "X-Mode", value: "a" },
        { name: "x-mode", value: "b" },
      ],
    },
  ])
    assert.equal(
      mcpAlternatives(bytes({ ...card, remotes: [remote] }))[0].recipe,
      undefined,
    );
  assert.throws(() => mcpURL("http://127.0.0.1/mcp"));
  assert.equal(mcpURL("http://127.0.0.1/mcp", true), "http://127.0.0.1/mcp");
  assert.throws(() => mcpURL("http://example.com/mcp", true));
  const input = {
    name: "X-Count",
    format: "number",
    required: true,
    secret: false,
  };
  assert.equal(inputValue(input, "12"), "12");
  for (const value of [
    "",
    "1e100",
    "NaN",
    "12\r\nAuthorization: bad",
    "{missing}",
  ])
    assert.throws(() => inputValue(input, value));
  assert.equal(
    inputValue({ ...input, format: "string", secret: true }, "secret{literal}"),
    "secret{literal}",
  );
  assert.throws(() => inputValue({ ...input, format: "boolean" }, "yes"));
  assert.throws(() => inputValue({ ...input, choices: ["1"] }, "2"));
});
function setup() {
  const secrets = new Map(),
    calls = [];
  const state = {
    trusted: true,
    current: true,
    confirm: true,
    input: "owned-target-secret",
    ref: bytes({
      ...card,
      remotes: [
        {
          ...card.remotes[0],
          headers: [
            { name: "Authorization", isSecret: true, isRequired: true },
          ],
        },
      ],
    }),
  };
  const ports = {
    trusted: () => state.trusted,
    acquire: async (ref) => {
      calls.push("acquire");
      return ref.bytes;
    },
    confirm: async () => {
      calls.push("confirm");
      return state.confirm;
    },
    input: async () => {
      calls.push("input");
      return state.input;
    },
    secrets: {
      keys: async () => [...secrets.keys()],
      get: async (k) => secrets.get(k),
      store: async (k, v) => {
        secrets.set(k, v);
      },
      delete: async (k) => {
        secrets.delete(k);
      },
    },
    changed: () => {
      calls.push("changed");
    },
  };
  const selections = new McpSelections(ports);
  const read = async () => {
    calls.push("read");
    return { bytes: state.ref };
  };
  const guard = () => {
    if (!state.current) throw new Error("connection_changed");
  };
  const add = (scope = "account-one") =>
    selections.add(read, guard, async () => 0, false, scope);
  return { state, ports, selections, read, guard, add, secrets, calls };
}
test("explicit selection and every resolution recheck evidence, keeping secrets target-bound", async () => {
  const f = setup(),
    selected = await f.add();
  assert.equal(f.selections.selections.size, 1);
  assert.equal(f.secrets.size, 0);
  const result = await f.selections.resolve(selected.id);
  assert.equal(result.values.Authorization, "owned-target-secret");
  assert.equal(f.calls.filter((c) => c === "read").length, 3);
  assert.ok([...f.secrets.keys()].every((k) => k.startsWith("mcp-target:")));
  const other = await f.add("account-two");
  assert.notEqual(other.id, selected.id);
  await f.selections.remove(selected.id);
  assert.equal(f.secrets.size, 0);
  await assert.rejects(f.selections.resolve(selected.id), /selection_changed/u);
  f.selections.clear();
  assert.equal(f.selections.selections.size, 0);
});
test("Restricted Mode, removed selections, changed evidence and outages refuse startup", async () => {
  const restricted = setup();
  restricted.state.trusted = false;
  await assert.rejects(restricted.add(), /workspace_trust_required/u);
  assert.deepEqual(restricted.calls, []);
  for (const fault of ["trusted", "current", "evidence", "outage", "removed"]) {
    const f = setup(),
      selected = await f.add();
    if (fault === "evidence") f.state.ref = bytes(card);
    else if (fault === "outage")
      f.ports.acquire = async () => {
        throw new Error("offline");
      };
    else if (fault === "removed") await f.selections.remove(selected.id);
    else f.state[fault] = false;
    await assert.rejects(f.selections.resolve(selected.id));
    assert.equal(f.secrets.size, 0, fault);
  }
});
test("late identity or evidence changes and cancelled prompts cannot return a definition", async () => {
  const f = setup(),
    selected = await f.add();
  f.ports.input = async () => {
    f.state.ref = bytes(card);
    return "secret";
  };
  await assert.rejects(f.selections.resolve(selected.id), /selection_changed/u);
  assert.equal(f.secrets.size, 0);
  const g = setup(),
    other = await g.add();
  g.ports.confirm = async () => {
    g.state.current = false;
    return true;
  };
  await assert.rejects(g.selections.resolve(other.id), /connection_changed/u);
  const h = setup(),
    cancelled = await h.add();
  h.state.input = undefined;
  await assert.rejects(
    h.selections.resolve(cancelled.id),
    /mcp_start_cancelled/u,
  );
  assert.equal(h.secrets.size, 0);
  const late = setup(),
    registered = await late.add();
  let cancelledAfterPrompt = false;
  late.ports.input = async () => {
    cancelledAfterPrompt = true;
    return "secret";
  };
  await assert.rejects(
    late.selections.resolve(registered.id, () => {
      if (cancelledAfterPrompt) throw new Error("native_cancelled");
    }),
    /native_cancelled/u,
  );
  assert.equal(late.secrets.size, 0);
});
test("changes after a Catalog read refuse acquisition before contacting an external document", async () => {
  for (const phase of ["selection", "resolution"])
    for (const fault of ["current", "trusted", "removed"]) {
      if (phase === "selection" && fault === "removed") continue;
      const f = setup(),
        selected = phase === "resolution" ? await f.add() : undefined;
      f.calls.length = 0;
      const read = async () => {
        await f.read();
        // Change authority after the read's final guard, before its caller resumes.
        queueMicrotask(() => {
          if (fault === "removed") f.selections.clear();
          else f.state[fault] = false;
        });
        return { url: "https://not-contacted.invalid/native.json" };
      };
      let attempt;
      if (selected) {
        selected.read = read;
        attempt = f.selections.resolve(selected.id);
      } else
        attempt = f.selections.add(
          read,
          f.guard,
          async () => 0,
          false,
          "account",
        );
      await assert.rejects(
        attempt,
        /connection_changed|workspace_trust_required|selection_changed/u,
      );
      assert.equal(f.calls.includes("acquire"), false, `${phase}: ${fault}`);
      assert.equal(f.secrets.size, 0);
    }
});
test("selection cancellation and unsupported alternatives do not register anything", async () => {
  const f = setup();
  f.state.confirm = false;
  assert.equal(await f.add(), undefined);
  assert.equal(f.selections.selections.size, 0);
  assert.equal(
    await f.selections.add(
      f.read,
      f.guard,
      async () => undefined,
      false,
      "account",
    ),
    undefined,
  );
  await assert.rejects(
    f.selections.add(f.read, f.guard, async () => 100, false, "account"),
    /unsupported_mcp_recipe/u,
  );
});
test("external native document reads are bounded, credential-free, and refuse redirects", async () => {
  const settings = {
    timeoutMs: 1000,
    allowLoopbackHTTP: false,
    caFile: "/private/catalog.pem",
    proxyURL: "https://proxy.example",
    oidc: {},
    origin: "https://catalog.example",
  };
  let request,
    connection,
    closed = 0,
    allow = true,
    response = new Response(bytes(card), {
      headers: { "content-type": "application/json" },
    });
  const network = async (c) => {
    connection = c;
    return {
      fetch: async (url, options) => {
        request = { url, options };
        return response;
      },
      close: async () => {
        closed++;
      },
    };
  };
  assert.deepEqual(
    await mcpDocument(
      { bytes: bytes(card) },
      settings,
      async () => false,
      network,
    ),
    bytes(card),
  );
  assert.equal(closed, 0);
  const ref = { url: "https://owned.example/card.json" };
  assert.deepEqual(
    await mcpDocument(ref, settings, async () => allow, network),
    bytes(card),
  );
  assert.equal(connection.caFile, undefined);
  assert.equal(connection.proxyURL, undefined);
  assert.equal(connection.oidc, undefined);
  assert.equal(request.options.redirect, "manual");
  assert.deepEqual(request.options.headers, { accept: "application/json" });
  for (const status of [302, 401, 503]) {
    response = new Response("private", {
      status,
      headers: { location: "https://elsewhere.example" },
    });
    await assert.rejects(
      mcpDocument(ref, settings, async () => allow, network),
      /mcp_document_unavailable/u,
    );
  }
  response = new Response(new Uint8Array(1024 ** 2 + 1), {
    headers: { "content-type": "application/json" },
  });
  await assert.rejects(
    mcpDocument(ref, settings, async () => allow, network),
    /too_large/u,
  );
  allow = false;
  const before = closed;
  await assert.rejects(
    mcpDocument(ref, settings, async () => allow, network),
    /cancelled/u,
  );
  assert.equal(closed, before);
});

test("clearing an optional target credential deletes its saved value and omits the header", async () => {
  const f = setup();
  f.state.ref = bytes({
    ...card,
    remotes: [
      {
        ...card.remotes[0],
        headers: [{ name: "Authorization", isSecret: true }],
      },
    ],
  });
  const selected = await f.add();
  await f.selections.resolve(selected.id);
  assert.equal(f.secrets.size, 1);
  f.state.input = "";
  const result = await f.selections.resolve(selected.id);
  assert.equal(Object.hasOwn(result.values, "Authorization"), false);
  assert.equal(f.secrets.size, 0);
});

test("the selection ceiling preserves exact duplicates and frees a slot after removal", async () => {
  const f = setup();
  for (let index = 0; index < 20; index++) await f.add(`owned-${index}`);
  await assert.rejects(f.add("over-limit"), /mcp_selection_limit/u);
  assert.equal(f.selections.selections.size, 20);
  const duplicate = await f.add("owned-0");
  assert.equal(f.selections.selections.size, 20);
  await f.selections.remove(duplicate.id);
  await f.add("replacement");
  assert.equal(f.selections.selections.size, 20);
  assert.equal(f.secrets.size, 0);
});

function gate() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
test("startup cancellation stops held consumer reads and prompts without later credentials or definitions", async () => {
  for (const boundary of ["read", "acquire", "confirm", "input", "secret"]) {
    const f = setup(),
      selected = await f.add(),
      controller = new AbortController();
    const entered = gate(),
      held = gate();
    let writes = 0,
      propagated;
    f.ports.secrets.store = async () => {
      writes++;
    };
    const block = async (signal) => {
      propagated = signal;
      entered.resolve();
      return held.promise;
    };
    if (boundary === "read") selected.read = (signal) => block(signal);
    if (boundary === "acquire") f.ports.acquire = (_, signal) => block(signal);
    if (boundary === "confirm") f.ports.confirm = (_, signal) => block(signal);
    if (boundary === "input") f.ports.input = (_, __, signal) => block(signal);
    if (boundary === "secret") f.ports.secrets.get = () => block();
    const pending = f.selections.resolve(
      selected.id,
      () => {},
      controller.signal,
    );
    await entered.promise;
    controller.abort();
    await assert.rejects(pending, { code: "mcp_start_cancelled" });
    if (boundary !== "secret") assert.equal(propagated, controller.signal);
    held.resolve(
      boundary === "read"
        ? { bytes: f.state.ref }
        : boundary === "acquire"
          ? f.state.ref
          : boundary === "confirm"
            ? true
            : "late target secret",
    );
    await Promise.resolve();
    assert.equal(writes, 0, boundary);
    assert.equal(f.secrets.size, 0);
    // An independent later startup uses the valid unchanged source and succeeds.
    if (boundary === "read") selected.read = f.read;
    if (boundary === "acquire") f.ports.acquire = async (ref) => ref.bytes;
    if (boundary === "confirm") f.ports.confirm = async () => true;
    if (boundary === "input") f.ports.input = async () => "owned-target-secret";
    if (boundary === "secret") f.ports.secrets.get = async () => undefined;
    const result = await f.selections.resolve(selected.id);
    assert.equal(result.values.Authorization, "owned-target-secret");
    assert.equal(writes, 1);
  }
});
test("MCP removal retains retry ownership and drains earlier secret writes", async () => {
  const f = setup(),
    selected = await f.add();
  await f.selections.resolve(selected.id);
  const originalDelete = f.ports.secrets.delete;
  f.ports.secrets.delete = async () => {
    throw new Error("private storage failure");
  };
  await assert.rejects(f.selections.remove(selected.id));
  assert.equal(f.selections.selections.has(selected.id), false);
  assert.deepEqual(f.selections.pendingRemovals(), [selected.id]);
  assert.equal(f.secrets.size, 1);
  f.ports.secrets.delete = originalDelete;
  await f.selections.remove(selected.id);
  assert.equal(f.secrets.size, 0);
  assert.deepEqual(f.selections.pendingRemovals(), []);
  const late = await f.add();
  const entered = gate(),
    release = gate();
  f.ports.secrets.store = async (key, value) => {
    entered.resolve();
    await release.promise;
    f.secrets.set(key, value);
  };
  const resolution = assert.rejects(
    f.selections.resolve(late.id),
    /selection_changed/u,
  );
  await entered.promise;
  const removal = f.selections.remove(late.id);
  release.resolve();
  await Promise.all([resolution, removal]);
  assert.equal(
    f.secrets.size,
    0,
    "the earlier write cannot recreate removed credentials",
  );
  const fresh = await f.add();
  f.state.input = "fresh-replacement";
  await f.selections.resolve(fresh.id);
  assert.deepEqual([...f.secrets.values()], ["fresh-replacement"]);
});
test("MCP forgetting is store-wide, bounded, retryable and fences cleared selections", async () => {
  const f = setup(),
    selected = await f.add();
  const entered = gate(),
    release = gate();
  f.ports.secrets.store = async (key, value) => {
    entered.resolve();
    await release.promise;
    f.secrets.set(key, value);
  };
  const resolution = assert.rejects(
    f.selections.resolve(selected.id),
    /selection_changed/u,
  );
  await entered.promise;
  f.selections.clear();
  f.secrets.set("openAgenticCatalog:host:account", "catalog-sentinel");
  f.secrets.set("a2a-target:other", "agent-sentinel");
  f.secrets.set("mcp-target:invalid:Authorization", "unowned");
  const forgetting = f.selections.forget();
  release.resolve();
  await resolution;
  assert.deepEqual(await forgetting, { deleted: 1, remaining: 0, failed: 0 });
  assert.equal(f.secrets.size, 3);
  for (let index = 0; index < 53; index++)
    f.secrets.set(
      `mcp-target:${index.toString(16).padStart(64, "0")}:X-Api-Key`,
      "target-sentinel",
    );
  assert.deepEqual(await f.selections.forget(() => true), {
    deleted: 0,
    remaining: 53,
    failed: 0,
  });
  const originalDelete = f.ports.secrets.delete;
  f.ports.secrets.delete = async (key) => {
    if (
      key.includes(
        ":0000000000000000000000000000000000000000000000000000000000000000:",
      )
    )
      throw new Error("refused");
    return originalDelete(key);
  };
  assert.deepEqual(await f.selections.forget(), {
    deleted: 49,
    remaining: 4,
    failed: 1,
  });
  f.ports.secrets.delete = originalDelete;
  assert.deepEqual(await f.selections.forget(), {
    deleted: 4,
    remaining: 0,
    failed: 0,
  });
  assert.equal(
    f.secrets.get("openAgenticCatalog:host:account"),
    "catalog-sentinel",
  );
});

test("saved MCP reselection refuses changed exact document or alternative before consent/registration", async () => {
  const { mcpDigest } = await import(process.env.CATALOG_VSCODE_LIB);
  const f = setup(),
    expected = { digest: mcpDigest(f.state.ref), alternative: 0 };
  await assert.rejects(
    f.selections.add(f.read, f.guard, async () => 0, false, "account", {
      ...expected,
      digest: "a".repeat(64),
    }),
    { code: "mcp_reference_changed" },
  );
  await assert.rejects(
    f.selections.add(f.read, f.guard, async () => 0, false, "account", {
      ...expected,
      alternative: 1,
    }),
    { code: "mcp_reference_changed" },
  );
  assert.equal(f.calls.includes("confirm"), false);
  assert.equal(f.selections.selections.size, 0);
  const selected = await f.selections.add(
    f.read,
    f.guard,
    async () => 0,
    false,
    "account",
    expected,
  );
  assert.equal(selected.alternative, 0);
  assert.equal(selected.digest, expected.digest);
  assert.equal(f.calls.includes("confirm"), true);
});
