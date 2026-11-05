import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { listenFixture } from "../../../test/vscode/listen.mjs";
const { packageProcess } = await import(process.env.CATALOG_VSCODE_LIB);
test("real child process uses literal arguments, bounds output and cancels without raw stderr", async (t) => {
  const work = await mkdtemp(join(tmpdir(), "oac-package-process-"));
  t.after(() => rm(work, { recursive: true, force: true }));
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
  // A real running child cannot keep an unsafe link in its acquisition tree.
  await assert.rejects(
    run(
      "require('node:fs').symlinkSync('/not-owned', 'unsafe-link');setInterval(()=>{},100)",
    ),
    { code: "package_disk_limit" },
  );
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
    operation = acquire(tools, cancelled.signal);
  await pending.promise;
  cancelled.abort();
  await assert.rejects(operation, { code: "package_cancelled" });
  await disconnected.promise;
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
