import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import {
  fixturePoll,
  fixtureWait as registryEvent,
} from "../../../test/vscode/wait.mjs";
const { packageProcess } = await import(process.env.CATALOG_VSCODE_LIB);
test("real child process uses literal arguments, bounds output and cancels without raw stderr", async (t) => {
  const work = await mkdtemp(join(tmpdir(), "oac-package-process-"));
  const rootRecord = join(work, "owned-parent.json"),
    childRecord = join(work, "owned-child.json"),
    heartbeat = join(work, "owned-heartbeat"),
    ownedPids = new Set();
  let groupCancellation, groupRejection;
  const remember = async (path) => {
    try {
      const record = JSON.parse(await readFile(path, "utf8"));
      for (const pid of Object.values(record)) {
        assert.ok(Number.isInteger(pid) && pid > 1 && pid !== process.pid);
        ownedPids.add(pid);
      }
      return record;
    } catch (error) {
      if (error.code === "ENOENT") return undefined;
      throw error;
    }
  };
  t.after(async () => {
    // Readiness records belong only to these children. Never kill by process name.
    await remember(rootRecord);
    await remember(childRecord);
    groupCancellation?.abort();
    for (const pid of ownedPids)
      try {
        process.kill(pid, "SIGKILL");
      } catch (error) {
        if (error.code !== "ESRCH") throw error;
      }
    if (groupRejection)
      await registryEvent(
        Promise.allSettled([groupRejection]),
        "process-tree cleanup",
      );
    await rm(work, { recursive: true, force: true });
  });
  const run = (code, signal = AbortSignal.timeout(10000), limit = 1000) =>
    packageProcess(
      process.execPath,
      ["-e", code, "--", "$(do-not-run);literal"],
      work,
      {},
      signal,
      limit,
    );
  assert.equal(
    (await run("process.stdout.write(process.argv[1])")).toString(),
    "$(do-not-run);literal",
  );
  await assert.rejects(
    run("process.stderr.write('secret dependency error');process.exit(1)"),
    (e) =>
      e.code === "package_acquisition_failed" && !e.message.includes("secret"),
  );
  await assert.rejects(
    run("process.stdout.write('x'.repeat(10000))", undefined, 16),
    { code: "package_output_limit" },
  );
  await assert.rejects(
    run("setInterval(()=>{},100)", AbortSignal.timeout(100)),
    { code: "package_cancelled" },
  );
  await assert.rejects(
    packageProcess(
      "/no-such-owned-tool",
      [],
      work,
      {},
      AbortSignal.timeout(1000),
    ),
    { code: "package_tool_unavailable" },
  );
  await assert.rejects(run("process.stderr.write('x'.repeat(70000))"), {
    code: "package_output_limit",
  });
  await assert.rejects(
    run("process.stdout.write('not run')", AbortSignal.abort()),
    { code: "package_cancelled" },
  );
  const grandchild = `
const fs=require('node:fs');
fs.writeFileSync(${JSON.stringify(childRecord)},JSON.stringify({child:process.pid}),{mode:0o600});
const timer=setInterval(()=>fs.appendFileSync(${JSON.stringify(heartbeat)},'.'),20);
setTimeout(()=>{clearInterval(timer);process.exit(0)},15000);
`,
    parent = `
const fs=require('node:fs');
const {spawn}=require('node:child_process');
const child=spawn(process.execPath,['-e',${JSON.stringify(grandchild)}],{stdio:'ignore'});
fs.writeFileSync(${JSON.stringify(rootRecord)},JSON.stringify({parent:process.pid,child:child.pid}),{mode:0o600});
setInterval(()=>{},100);
`;
  groupCancellation = new AbortController();
  groupRejection = assert.rejects(run(parent, groupCancellation.signal), {
    code: "package_cancelled",
  });
  // Retain any assertion failure for the awaited result while readiness is held.
  void groupRejection.catch(() => {});
  const parentReady = await fixturePoll(
      () => remember(rootRecord),
      "package parent readiness",
    ),
    childReady = await fixturePoll(
      () => remember(childRecord),
      "package descendant readiness",
    );
  assert.equal(parentReady.child, childReady.child);
  await fixturePoll(async () => {
    try {
      return (await readFile(heartbeat)).length > 0;
    } catch (error) {
      if (error.code === "ENOENT") return false;
      throw error;
    }
  }, "package descendant heartbeat");
  groupCancellation.abort();
  await groupRejection;
  for (const pid of [parentReady.parent, childReady.child])
    await fixturePoll(async () => {
      try {
        process.kill(pid, 0);
        // A simultaneously killed orphan can remain a zombie until host init
        // reaps it. It cannot execute or write; it is not a live descendant.
        if (process.platform === "linux") {
          const state = await readFile(`/proc/${pid}/stat`, "utf8");
          return state.slice(state.lastIndexOf(")") + 2).startsWith("Z ");
        }
        return false;
      } catch (error) {
        if (["ESRCH", "ENOENT"].includes(error.code)) return true;
        throw error;
      }
    }, "package process-tree termination");
  const stoppedHeartbeat = await readFile(heartbeat);
  await new Promise((resolve) => setTimeout(resolve, 60));
  assert.deepEqual(await readFile(heartbeat), stoppedHeartbeat);
  // A real running child cannot keep an unsafe link in its acquisition tree.
  await assert.rejects(
    run(
      "require('node:fs').symlinkSync('/not-owned', 'unsafe-link');setInterval(()=>{},100)",
    ),
    { code: "package_disk_limit" },
  );
});

// A real credentialed OCI peer. Only loopback TLS requests participate; the
// private acquisition path deliberately does not run ORAS or use ambient auth.
async function privateRegistry(t) {
  const { createServer, get } = await import("node:https");
  const { readFile } = await import("node:fs/promises");
  const { execFileSync } = await import("node:child_process");
  const { files, zip, hash, representation } =
    await import("./package-fixtures.mjs");
  const { skillPlan, acquirePackage } = await import(
    process.env.CATALOG_VSCODE_LIB
  );
  const directory = await mkdtemp(join(tmpdir(), "oac-private-oci-")),
    servers = [];
  t.after(async () => {
    await Promise.all(
      servers.map(
        (server) =>
          new Promise((done) => {
            server.closeAllConnections();
            server.close(done);
          }),
      ),
    );
    await rm(directory, { recursive: true, force: true });
  });
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-days",
      "1",
      "-subj",
      "/CN=localhost",
      "-addext",
      "subjectAltName=IP:127.0.0.1",
      "-keyout",
      join(directory, "key"),
      "-out",
      join(directory, "cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  const tls = {
      key: await readFile(join(directory, "key")),
      cert: await readFile(join(directory, "cert")),
    },
    entries = files(),
    archive = zip(entries),
    seen = [],
    foreign = [],
    basic =
      "Basic " +
      Buffer.from("registry-reader:source-secret").toString("base64"),
    bearer = "Bearer registry.token";
  const state = {
    token: "valid",
    challenge: "valid",
    blob: "valid",
    repeat: false,
  };
  const start = async (handler) => {
    const server = createServer(tls, handler);
    servers.push(server);
    await listenFixture(server);
    return `https://127.0.0.1:${server.address().port}`;
  };
  const foreignOrigin = await start((req, res) => {
    foreign.push({ path: req.url, headers: req.headers });
    res.end('{"token":"must-not-be-read"}');
  });
  // Prove that this exact target is reachable with the same trust input before
  // using zero contacts as evidence of a credential-origin or redirect fence.
  const probeAbort = new AbortController(),
    probeTimer = setTimeout(() => probeAbort.abort(), 3000);
  try {
    const probe = await new Promise((done, fail) => {
      const request = get(
        foreignOrigin + "/token",
        {
          ca: tls.cert,
          rejectUnauthorized: true,
          agent: false,
          signal: probeAbort.signal,
        },
        (response) => {
          const chunks = [];
          response.on("data", (chunk) => chunks.push(chunk));
          response.on("error", fail);
          response.on("end", () =>
            done({
              status: response.statusCode,
              bytes: Buffer.concat(chunks).toString("utf8"),
            }),
          );
        },
      );
      request.on("error", fail);
    });
    assert.deepEqual(probe, {
      status: 200,
      bytes: '{"token":"must-not-be-read"}',
    });
    assert.equal(foreign.length, 1);
    assert.equal(foreign[0].path, "/token");
    assert.equal(foreign[0].headers.authorization, undefined);
    assert.equal(foreign[0].headers.cookie, undefined);
    foreign.length = 0;
  } finally {
    clearTimeout(probeTimer);
    probeAbort.abort();
  }
  let origin, manifest;
  const began = Promise.withResolvers(),
    disconnected = Promise.withResolvers();
  origin = await start((req, res) => {
    const url = new URL(req.url, origin);
    seen.push({
      path: url.pathname,
      method: req.method,
      query: Object.fromEntries(url.searchParams),
      queryCount: url.searchParams.size,
      headers: req.headers,
    });
    if (url.pathname === "/token") {
      if (state.token === "redirect") {
        res.writeHead(302, { location: foreignOrigin + "/token" });
        res.end();
        return;
      }
      res.setHeader("content-type", "application/json");
      res.end(
        JSON.stringify({
          token:
            state.token === "invalid"
              ? "contains a space"
              : state.token === "oversize"
                ? "x".repeat(8193)
                : "registry.token",
        }),
      );
      return;
    }
    if (req.headers.authorization !== bearer || state.repeat) {
      const realm =
        state.challenge === "foreign"
          ? foreignOrigin + "/token"
          : origin + "/token";
      res.writeHead(401, {
        "www-authenticate":
          state.challenge === "malformed"
            ? 'Basic realm="owned"'
            : `Bearer realm="${realm}",service="owned-registry",scope="repository:${state.challenge === "scope" ? "other/repository" : "skills/owned"}:pull"`,
      });
      res.end("owned challenge");
      return;
    }
    if (url.pathname.startsWith("/v2/skills/owned/manifests/")) {
      res.setHeader(
        "content-type",
        "application/vnd.oci.image.manifest.v1+json",
      );
      res.end(manifest);
    } else if (url.pathname.startsWith("/v2/skills/owned/blobs/")) {
      res.setHeader("content-type", "application/agent-skills+zip");
      if (state.blob === "pending") {
        res.once("close", disconnected.resolve);
        res.write(archive.subarray(0, 2));
        began.resolve();
      } else
        res.end(
          state.blob === "wrong"
            ? Buffer.alloc(archive.length)
            : state.blob === "large"
              ? Buffer.alloc(archive.length + 1)
              : archive,
        );
    } else {
      res.writeHead(404);
      res.end();
    }
  });
  const layer = {
    mediaType: "application/agent-skills+zip",
    digest: hash(archive),
    size: archive.length,
  };
  const document = () => ({
    schemaVersion: 2,
    mediaType: "application/vnd.oci.image.manifest.v1+json",
    config: {
      mediaType: "application/vnd.oci.empty.v1+json",
      digest: hash("{}"),
      size: 2,
    },
    layers: [{ ...layer }],
  });
  const acquire = (
    value = document(),
    signal = AbortSignal.timeout(10000),
    corrupt = false,
  ) => {
    const exact = Buffer.from(JSON.stringify(value));
    manifest = corrupt
      ? Buffer.from(
          JSON.stringify({
            ...value,
            annotations: {
              "org.opencontainers.image.description": "changed bytes",
            },
          }),
        )
      : exact;
    const input = representation(entries);
    input.handoff.oci.reference = `${new URL(origin).host}/skills/owned@${hash(exact)}`;
    const plan = skillPlan(input, hash("owned registry account"));
    return acquirePackage(
      plan,
      plan.sources.find((source) => source.type === "oci"),
      directory,
      { timeoutMs: 10000, caFile: join(directory, "cert") },
      signal,
      {
        type: "oci",
        origin,
        scheme: "basic",
        username: "registry-reader",
        token: "source-secret",
      },
    );
  };
  const expectContent = (content) =>
    assert.deepEqual(
      [...content].sort(),
      entries
        .map((entry) => [
          entry.path,
          { bytes: entry.bytes, executable: entry.executable },
        ])
        .sort(),
    );
  const counts = () => ({
    manifests: seen.filter((r) => r.path.includes("/manifests/")).length,
    tokens: seen.filter((r) => r.path === "/token").length,
    blobs: seen.filter((r) => r.path.includes("/blobs/")).length,
  });
  return {
    acquire,
    expectContent,
    document,
    manifestPath: `/v2/skills/owned/manifests/${hash(Buffer.from(JSON.stringify(document())))}`,
    layer,
    archive,
    origin,
    basic,
    bearer,
    state,
    seen,
    foreign,
    counts,
    began,
    disconnected,
  };
}

test("real private OCI authentication binds one token exchange to the selected origin and repository", async (t) => {
  const f = await privateRegistry(t);
  f.expectContent(await f.acquire());
  assert.deepEqual(f.counts(), { manifests: 2, tokens: 1, blobs: 1 });
  assert.deepEqual(
    f.seen.map((request) => request.path),
    [
      f.manifestPath,
      "/token",
      f.manifestPath,
      `/v2/skills/owned/blobs/${f.layer.digest}`,
    ],
  );
  assert.ok(f.seen.every((request) => request.method === "GET"));
  assert.deepEqual(
    f.seen.map((request) => request.headers.authorization),
    [f.basic, f.basic, f.bearer, f.bearer],
  );
  assert.deepEqual(f.seen[1].query, {
    scope: "repository:skills/owned:pull",
    service: "owned-registry",
  });
  assert.equal(f.seen[1].queryCount, 2);
  assert.equal(
    f.seen[0].headers.accept,
    "application/vnd.oci.image.manifest.v1+json",
  );
  assert.equal(f.seen[3].headers.accept, "application/agent-skills+zip");
  for (const [field, value, code, delta] of [
    [
      "challenge",
      "malformed",
      "unsupported_oci_authentication",
      { manifests: 1, tokens: 0, blobs: 0 },
    ],
    [
      "challenge",
      "foreign",
      "package_credential_origin_mismatch",
      { manifests: 1, tokens: 0, blobs: 0 },
    ],
    [
      "challenge",
      "scope",
      "package_credential_origin_mismatch",
      { manifests: 1, tokens: 0, blobs: 0 },
    ],
    [
      "token",
      "redirect",
      "package_archive_unavailable",
      { manifests: 1, tokens: 1, blobs: 0 },
    ],
    [
      "token",
      "invalid",
      "invalid_package_credentials",
      { manifests: 1, tokens: 1, blobs: 0 },
    ],
    [
      "token",
      "oversize",
      "invalid_package_credentials",
      { manifests: 1, tokens: 1, blobs: 0 },
    ],
    [
      "repeat",
      true,
      "package_archive_unavailable",
      { manifests: 2, tokens: 1, blobs: 0 },
    ],
  ]) {
    const prior = f.state[field],
      before = f.counts();
    f.state[field] = value;
    try {
      await assert.rejects(f.acquire(), { code });
      const after = f.counts();
      assert.deepEqual(
        Object.fromEntries(
          Object.keys(after).map((key) => [key, after[key] - before[key]]),
        ),
        delta,
      );
      assert.deepEqual(
        f.foreign,
        [],
        "No credential or token reaches another origin, including redirects",
      );
    } finally {
      f.state[field] = prior;
    }
  }
  assert.ok(f.seen.every((request) => !request.headers.cookie));
  f.expectContent(await f.acquire());
});

test("real private OCI acquisition verifies exact manifest and layer evidence and closes a cancelled blob", async (t) => {
  const f = await privateRegistry(t);
  f.expectContent(await f.acquire());
  for (const [value, corrupt, code] of [
    [f.document(), true, "package_integrity_failed"],
    [{ ...f.document(), schemaVersion: 3 }, false, "unsupported_oci_manifest"],
    [
      { ...f.document(), layers: [f.layer, f.layer] },
      false,
      "ambiguous_package_layer",
    ],
    [
      { ...f.document(), layers: [{ ...f.layer, size: f.layer.size + 1 }] },
      false,
      "package_integrity_failed",
    ],
    [
      {
        ...f.document(),
        layers: [{ ...f.layer, digest: "sha256:" + "a".repeat(64) }],
      },
      false,
      "package_integrity_failed",
    ],
    [
      {
        ...f.document(),
        layers: [{ ...f.layer, urls: [f.origin + "/must-not-follow"] }],
      },
      false,
      "package_integrity_failed",
    ],
  ]) {
    const before = f.counts();
    await assert.rejects(f.acquire(value, undefined, corrupt), { code });
    const after = f.counts();
    assert.equal(after.manifests - before.manifests, 2);
    assert.equal(after.tokens - before.tokens, 1);
    assert.equal(
      after.blobs,
      before.blobs,
      "Refused manifest evidence must not authorize a blob request",
    );
  }
  for (const [mode, code] of [
    ["wrong", "package_integrity_failed"],
    ["large", "package_size_limit"],
  ]) {
    f.state.blob = mode;
    const before = f.counts();
    await assert.rejects(f.acquire(), { code });
    assert.equal(f.counts().blobs, before.blobs + 1);
  }
  f.state.blob = "pending";
  const controller = new AbortController(),
    operation = f.acquire(undefined, controller.signal),
    rejected = assert.rejects(operation, { code: "package_cancelled" });
  try {
    await registryEvent(f.began.promise, "blob readiness", operation);
    controller.abort();
    await rejected;
    await registryEvent(f.disconnected.promise, "blob disconnect");
  } finally {
    controller.abort();
    await registryEvent(
      Promise.allSettled([rejected]),
      "package operation cleanup",
    );
  }
  f.state.blob = "valid";
  f.expectContent(await f.acquire());
  assert.deepEqual(f.foreign, []);
});

test("real HTTPS Plugin download verifies private trust, identity and streaming limits without credentials", async (t) => {
  const { createServer } = await import("node:https");
  const { readFile } = await import("node:fs/promises");
  const { execFileSync } = await import("node:child_process");
  const { zip, hash } = await import("./package-fixtures.mjs");
  const { pluginPlan, acquireArchive, transport } = await import(
    process.env.CATALOG_VSCODE_LIB
  );
  const directory = await mkdtemp(join(tmpdir(), "oac-plugin-tls-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-days",
      "1",
      "-subj",
      "/CN=localhost",
      "-addext",
      "subjectAltName=IP:127.0.0.1",
      "-keyout",
      join(directory, "key"),
      "-out",
      join(directory, "cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  const bytes = Buffer.from(
    JSON.stringify({
      $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
      name: "owned.tools",
    }),
  );
  const archive = zip([{ path: "plugin.json", bytes, executable: false }]);
  let mode = "valid",
    expectedAuthorization;
  const seen = [],
    pending = Promise.withResolvers(),
    disconnected = Promise.withResolvers();
  const server = createServer(
    {
      key: await readFile(join(directory, "key")),
      cert: await readFile(join(directory, "cert")),
    },
    (req, res) => {
      seen.push({ path: req.url, headers: req.headers });
      if (
        expectedAuthorization &&
        req.headers.authorization !== expectedAuthorization
      ) {
        res.writeHead(401);
        res.end();
        return;
      }
      res.setHeader("content-type", "application/zip");
      if (mode === "redirect") {
        res.writeHead(302, { location: "/must-not-follow" });
        res.end();
      } else if (mode === "pending") {
        res.on("close", disconnected.resolve);
        res.write(archive.subarray(0, 2));
        pending.resolve();
      } else if (mode === "large") res.end(Buffer.alloc(archive.length + 1));
      else if (mode === "wrong") res.end(Buffer.alloc(archive.length));
      else if (mode === "outage") {
        res.writeHead(503);
        res.end("upstream-secret");
      } else res.end(archive);
    },
  );
  await listenFixture(server);
  t.after(
    () =>
      new Promise((resolve) => {
        server.closeAllConnections();
        server.close(resolve);
      }),
  );
  const url = `https://127.0.0.1:${server.address().port}/plugin.zip`,
    tools = { timeoutMs: 10000, caFile: join(directory, "cert") };
  const plan = pluginPlan(
    {
      validationState: "valid",
      profileId: "agent-plugins-1.0.0-1fc1b627",
      mediaType: "application/agent-plugins+zip",
      contentDigest: hash(archive),
      size: archive.length,
      url,
      plugin: {
        rootDirectory: "",
        expandedSize: bytes.length,
        files: [
          {
            path: "plugin.json",
            size: bytes.length,
            digest: hash(bytes),
            executable: false,
          },
        ],
        documents: [{ path: "plugin.json", content: bytes.toString() }],
      },
    },
    hash("identity"),
  );
  const acquire = (settings = tools, signal = AbortSignal.timeout(10000)) =>
    acquireArchive(plan, url, settings, signal);
  assert.equal(
    (await acquire()).get("owned.tools/plugin.json").bytes.toString(),
    bytes.toString(),
  );
  // Reproduce Fetch refusal independently of TLS availability and archive bytes.
  // This host's ephemeral range includes Fetch-reserved ports.
  let blockedCalls = 0,
    blockedConnections = 0;
  const blocked = createServer(
    {
      key: await readFile(join(directory, "key")),
      cert: await readFile(join(directory, "cert")),
    },
    (_, response) => {
      blockedCalls++;
      response.setHeader("content-type", "application/zip");
      response.end(archive);
    },
  );
  blocked.on("connection", () => blockedConnections++);
  t.after(
    () =>
      new Promise((done) => {
        blocked.closeAllConnections();
        blocked.close(done);
      }),
  );
  let blockedPort;
  for (const port of [
    6000, 6566, 6665, 6666, 6667, 6668, 6669, 6679, 6697, 10080,
  ]) {
    try {
      await new Promise((done, fail) => {
        const failed = (error) => {
          blocked.off("error", failed);
          fail(error);
        };
        blocked.once("error", failed);
        blocked.listen(port, "127.0.0.1", () => {
          blocked.off("error", failed);
          done();
        });
      });
      blockedPort = port;
      break;
    } catch (error) {
      if (error.code !== "EADDRINUSE") throw error;
    }
  }
  assert.ok(
    blockedPort,
    "An owned Fetch-reserved port is required for calibration",
  );
  const blockedURL = `https://127.0.0.1:${blockedPort}/plugin.zip`;
  const { get } = await import("node:https");
  const ca = await readFile(join(directory, "cert"));
  const direct = await new Promise((done, fail) => {
    const request = get(
      blockedURL,
      { ca, agent: false, rejectUnauthorized: true },
      (response) => {
        const chunks = [];
        response.on("data", (chunk) => chunks.push(chunk));
        response.on("error", fail);
        response.on("end", () =>
          done({ status: response.statusCode, bytes: Buffer.concat(chunks) }),
        );
      },
    );
    request.on("error", fail);
    request.setTimeout(5000, () =>
      request.destroy(new Error("Owned TLS control deadline")),
    );
  });
  assert.equal(direct.status, 200);
  assert.deepEqual(
    direct.bytes,
    archive,
    "The same TLS listener serves the exact archive to a direct client",
  );
  assert.equal(blockedCalls, 1);
  assert.equal(blockedConnections, 1);
  let refusal;
  await assert.rejects(
    acquireArchive(
      plan,
      blockedURL,
      tools,
      AbortSignal.timeout(10000),
      async (connection) => {
        const client = await transport(connection);
        return {
          ...client,
          async fetch(...args) {
            try {
              return await client.fetch(...args);
            } catch (error) {
              refusal = error;
              throw error;
            }
          },
        };
      },
    ),
    { code: "package_archive_unavailable" },
  );
  assert.equal(refusal?.cause?.message, "bad port");
  assert.equal(
    blockedConnections,
    1,
    "Fetch-reserved port refusal precedes any additional TCP connection",
  );
  assert.equal(
    blockedCalls,
    1,
    "Fetch-reserved port refusal precedes any additional HTTP request",
  );
  await assert.rejects(acquire({ timeoutMs: 1000 }), {
    code: "package_archive_unavailable",
  });
  for (mode of ["redirect", "large", "wrong", "outage"])
    await assert.rejects(acquire());
  assert.ok(
    seen.every(
      (r) =>
        r.path === "/plugin.zip" &&
        !r.headers.authorization &&
        !r.headers.cookie,
    ),
  );
  mode = "pending";
  const cancelled = new AbortController(),
    operation = acquire(tools, cancelled.signal),
    rejected = assert.rejects(operation, { code: "package_cancelled" });
  try {
    await registryEvent(pending.promise, "Plugin archive readiness", operation);
    cancelled.abort();
    await rejected;
    await registryEvent(disconnected.promise, "Plugin archive response close");
  } finally {
    cancelled.abort();
    await registryEvent(
      Promise.allSettled([rejected]),
      "package operation cleanup",
    );
  }
  mode = "valid";
  assert.equal((await acquire()).size, 1);
  for (const credential of [
    {
      type: "https",
      origin: new URL(url).origin,
      scheme: "basic",
      username: "owned",
      token: "private-password",
    },
    {
      type: "https",
      origin: new URL(url).origin,
      scheme: "bearer",
      token: "private.token",
    },
  ]) {
    expectedAuthorization =
      credential.scheme === "basic"
        ? "Basic " + Buffer.from("owned:private-password").toString("base64")
        : "Bearer private.token";
    await assert.rejects(acquire(), { code: "package_archive_unavailable" });
    const download = (signal) =>
      acquireArchive(
        plan,
        url,
        tools,
        signal ?? AbortSignal.timeout(10000),
        undefined,
        credential,
      );
    assert.equal((await download()).size, 1);
    const count = seen.length;
    mode = "redirect";
    await assert.rejects(download(), { code: "package_archive_unavailable" });
    assert.equal(
      seen.length,
      count + 1,
      "Credential-bearing redirects are refused, including same-origin redirects",
    );
    assert.equal(seen.at(-1).path, "/plugin.zip");
    mode = "valid";
    await assert.rejects(
      acquireArchive(plan, url, tools, AbortSignal.timeout(10000), undefined, {
        ...credential,
        origin: "https://another.invalid",
      }),
      { code: "package_credential_origin_mismatch" },
    );
    assert.equal(
      seen.length,
      count + 1,
      "Wrong-origin credentials are refused before HTTP",
    );
    await assert.rejects(download(AbortSignal.abort()), {
      code: "package_cancelled",
    });
  }
});
