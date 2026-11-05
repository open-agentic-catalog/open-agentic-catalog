import { developmentUsers } from "./development-users.mjs";
// Disposable real-service fixture. Never connects to an existing Catalog.
import { performancePolicy } from "./performance.mjs";
import {
  authoredPluginDocuments,
  packAuthoredPlugin,
} from "./authoring-archive.mjs";
import {
  authorizationCommandOutcome,
  fixtureSetupError,
  roleRefusalReadback,
} from "./authorization-evidence.mjs";
import { spawn } from "node:child_process";
import { createHash, randomBytes, randomInt, randomUUID } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { request } from "node:https";
import { basename, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

/** Remove ambient Catalog settings so a fixture cannot inherit another deployment's inputs. */
export function runtimeEnvironment(env = process.env) {
  return Object.fromEntries(
    Object.entries(env).filter(([key]) => !key.startsWith("CATALOG_")),
  );
}

/** Run the complete ordinary suite in two serial native shards; keep explicit and exclusive selections intact. */
export function browserSelections(args, exclusive) {
  return args.length || exclusive
    ? [[...args]]
    : [["--shard=1/2"], ["--shard=2/2"]];
}

/** Retain bounded startup evidence; never expose child text or launch arguments. */
export function startupFailureStage(text) {
  return /(?:^|\n)Demo sample tool failed \(stage: (sample\.load|prepared\.(?:write|read)|tls\.(?:prepare|not-yet-valid|expired|hostname)|listener\.(?:address|address-in-use|serve))\)\.(?:\r?\n|$)/.exec(
    text,
  )?.[1];
}

/** Retain bounded startup evidence; never expose child text or launch arguments. */
export function startProcess(command, args, options = {}) {
  const { collectStages = false, ...spawnOptions } = options;
  const child = spawn(command, args, {
    env: runtimeEnvironment(),
    ...spawnOptions,
    stdio: ["ignore", collectStages ? "pipe" : "ignore", "pipe"],
  });
  let stderr = Buffer.alloc(0);
  let pendingLine = "";
  let droppingLine = false;
  let stdoutTruncated = false;
  const refusals = [];
  const observeLine = (line) => {
    try {
      const entry = JSON.parse(line);
      if (
        entry.event !== "http.request_failed" ||
        entry.error_code !== "CONFLICT" ||
        ![
          "authorization.assignment.time_regression",
          "authorization.group.time_regression",
          "authorization.group_member.time_regression",
        ].includes(entry.operation_stage)
      )
        return;
      refusals.push({
        event: "http.request_failed",
        code: "CONFLICT",
        operationStage: entry.operation_stage,
        ...(typeof entry.request_id === "string" &&
        /^[A-Za-z0-9._-]{1,128}$/.test(entry.request_id)
          ? { requestId: entry.request_id }
          : {}),
      });
      if (refusals.length > 8) refusals.shift();
    } catch {
      // Non-JSON process output is never diagnostic evidence.
    }
  };
  if (collectStages) {
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      const lines = chunk.split("\n");
      for (let index = 0; index < lines.length; index++) {
        const part = lines[index];
        if (!droppingLine) {
          if (pendingLine.length + part.length > 8192) {
            pendingLine = "";
            droppingLine = true;
            stdoutTruncated = true;
          } else pendingLine += part;
        }
        if (index < lines.length - 1) {
          if (!droppingLine) observeLine(pendingLine);
          pendingLine = "";
          droppingLine = false;
        }
      }
    });
  }
  let truncated = false;
  let spawnError = "none";
  child.stderr.on("data", (chunk) => {
    const remaining = 8192 - stderr.length;
    truncated ||= chunk.length > remaining;
    stderr = Buffer.concat([stderr, chunk.subarray(0, remaining)]);
  });
  const done = new Promise((resolve) => {
    child.once("error", (error) => {
      spawnError = ["ENOENT", "EACCES", "ENOEXEC"].includes(error.code)
        ? error.code
        : "other";
    });
    // close follows drained stderr, including failed spawn.
    child.once("close", (code) =>
      resolve(code >= 0 && code !== null ? code : -1),
    );
  });
  const diagnostics = () => {
    const operationStage = startupFailureStage(stderr.toString());
    return JSON.stringify({
      exitCode: child.exitCode,
      signal: child.signalCode,
      spawnError,
      stderr: operationStage
        ? "classified"
        : /EADDRINUSE|bind: address already in use/.test(stderr.toString())
          ? "address-in-use"
          : stderr.length
            ? "unclassified"
            : "empty",
      ...(operationStage ? { operationStage } : {}),
      truncated,
      ...(collectStages
        ? {
            stdoutTruncated,
            refusals: [...refusals],
          }
        : {}),
    });
  };
  return { child, done, diagnostics };
}

/** Stop only the supplied child, escalating after its bounded graceful shutdown period. */
export async function stopProcess(process) {
  if (!process.child.pid) {
    await process.done;
    return;
  }
  if (process.child.exitCode !== null || process.child.signalCode !== null)
    return;
  process.child.kill("SIGTERM");
  let timer;
  await Promise.race([
    process.done,
    new Promise((resolve) => {
      timer = setTimeout(resolve, 5000);
    }),
  ]);
  clearTimeout(timer);
  if (process.child.exitCode === null && process.child.signalCode === null) {
    process.child.kill("SIGKILL");
    await process.done;
  }
}

// Use dynamic/private ports: the host's ephemeral range can include ports
// blocked by browser/Node fetch. Reserve three together. A later bind race fails startup; it
// must never trigger reuse of another process or cleanup of its listener.
export async function localPorts(choosePort = () => randomInt(49152, 65536)) {
  const servers = [];
  try {
    for (let attempt = 0; attempt < 100 && servers.length < 3; attempt++) {
      const port = choosePort();
      if (!Number.isInteger(port) || port < 49152 || port > 65535)
        throw new Error("Browser fixture requires a dynamic/private port");
      const server = createServer();
      try {
        await new Promise((resolve, reject) => {
          server.once("error", reject);
          server.listen(port, "127.0.0.1", resolve);
        });
      } catch (error) {
        if (error.code === "EADDRINUSE") continue;
        throw error;
      }
      servers.push(server);
    }
    if (servers.length !== 3)
      throw new Error("Could not reserve three browser fixture ports");
    return servers.map((server) => server.address().port);
  } finally {
    await Promise.all(
      servers.map((server) => new Promise((resolve) => server.close(resolve))),
    );
  }
}

/** Poll bounded health reads while checking that the owned listener remains alive. */
export async function ready(origin, process, policy) {
  let lastResult = "no response";
  const deadline = performance.now() + (policy?.readiness ?? 0);
  for (
    let attempt = 0;
    policy ? performance.now() < deadline : attempt < 100;
    attempt++
  ) {
    if (
      !process.child.pid ||
      process.child.exitCode !== null ||
      process.child.signalCode !== null
    ) {
      await process.done;
      throw new Error(
        `Owned browser fixture exited before readiness: ${process.diagnostics()}`,
      );
    }
    try {
      const response = await fetch(`${origin}/readyz`, {
        signal: AbortSignal.timeout(
          policy
            ? Math.max(
                1,
                Math.min(policy.probe, Math.ceil(deadline - performance.now())),
              )
            : 300,
        ),
        redirect: "error",
      });
      lastResult = `HTTP ${response.status}`;
      await response.body?.cancel();
      if (response.ok) {
        // Detect an occupied-port startup failure before seeding anything.
        await delay(100);
        if (policy && performance.now() >= deadline) break;
        if (
          process.child.exitCode === null &&
          process.child.signalCode === null
        )
          return;
        await process.done;
        throw new Error(
          `Owned listener failed to bind: ${process.diagnostics()}`,
        );
      }
    } catch (error) {
      // Record only a fixed transport category, never response bodies or logs.
      lastResult =
        error.cause?.message === "bad port"
          ? "fetch blocked this port"
          : "transport failure";
      // Only bounded readiness reads are retried, never Catalog mutations.
    }
    await delay(50);
  }
  throw new Error(
    `Owned browser fixture did not become ready at ${origin} (${lastResult}): ${process.diagnostics()}`,
  );
}

/** Create and verify an isolated service/UI pair; return cleanup and fixture-only API helpers. */
export async function startCatalog(
  buildDirectory,
  root,
  basePath,
  {
    webhooks = false,
    publisherVerification = false,
    demoArchives = false,
    authoredPlugin = false,
    artwork = false,
    curated = false,
    publicationAudiences = false,
    maintainedIdentities = false,
    serviceActivity = false,
    performanceScale,
  } = {},
) {
  if (typeof serviceActivity !== "boolean")
    throw new Error("Invalid service activity fixture option");
  if (typeof publisherVerification !== "boolean")
    throw new Error("Invalid publisher verification fixture option");
  if (typeof webhooks !== "boolean")
    throw new Error("Invalid webhook fixture option");
  if (typeof maintainedIdentities !== "boolean")
    throw new Error("Invalid maintained identity fixture option");
  if (typeof artwork !== "boolean")
    throw new Error("Invalid artwork fixture option");
  if (typeof publicationAudiences !== "boolean")
    throw new Error("Invalid publication audience fixture option");
  if (typeof demoArchives !== "boolean")
    throw new Error("Invalid demo archive fixture option");
  if (typeof authoredPlugin !== "boolean" || (authoredPlugin && !demoArchives))
    throw new Error("Invalid authored Plugin fixture option");
  if (typeof curated !== "boolean" || (curated && (demoArchives || artwork)))
    throw new Error("Invalid curated fixture option");
  if (!["", "/browser-test"].includes(basePath))
    throw new Error("Unsupported browser fixture base path");
  const policy =
    performanceScale === undefined
      ? undefined
      : performancePolicy(performanceScale);
  const directory = await mkdtemp(join(buildDirectory, "case-"));
  const processes = [];
  const close = async () => {
    try {
      for (const process of processes.toReversed()) await stopProcess(process);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  };
  const json = async (name, value) => {
    const path = join(directory, name);
    await writeFile(path, JSON.stringify(value), { mode: 0o600 });
    return path;
  };
  try {
    const [apiPort, operationsPort, uiPort] = await localPorts();
    const apiOrigin = `http://127.0.0.1:${apiPort}`;
    const origin = `http://127.0.0.1:${uiPort}`;
    const config = JSON.parse(
      await readFile(join(root, "config/local.json"), "utf8"),
    );
    config.logFormat = "json";
    config.logLevel = "debug";
    if (publisherVerification)
      config.publisherDomainVerification.mode = "optional";
    config.http.serviceActivityEnabled = serviceActivity;
    config.http.address = `127.0.0.1:${apiPort}`;
    config.http.publicBaseUrl = apiOrigin;
    config.worker.operationsAddress = `127.0.0.1:${operationsPort}`;
    if (publicationAudiences)
      config.discovery.hosts.push(
        {
          host: "private.catalog.test",
          tenantId: config.auth.developmentTokens["local-reader"].tenantId,
          mode: "authenticated",
          publisherDomain: "catalog.example.com",
        },
        {
          host: "empty.catalog.test",
          tenantId:
            config.auth.developmentTokens["other-tenant-admin"].tenantId,
          mode: "anonymous",
          publisherDomain: "empty.catalog.test",
        },
      );
    // Extra identities belong only to this invocation, never config/local.json
    // or a retained development database. Grants are added by individual cases.
    for (const [alias, roles] of Object.entries(
      maintainedIdentities
        ? {}
        : {
            "local-source": ["source_admin"],
            "local-security": ["security_reviewer"],
            "local-task": ["contributor"],
            "local-scoped": [],
            "local-no-role": [],
            "local-policy": [],
          },
    )) {
      config.auth.developmentTokens[alias] = {
        subject: alias,
        tenantId: config.auth.developmentTokens["local-reader"].tenantId,
        roles,
      };
    }
    let archive;
    let archiveProcess;
    let generatedPlugin;
    let artworkTrustBundle;
    if (demoArchives || artwork || curated) {
      const archivePort = (await localPorts()).find(
        (port) => ![apiPort, operationsPort, uiPort].includes(port),
      );
      if (archivePort === undefined)
        throw new Error("Could not allocate a distinct archive port");
      const archiveOrigin = `https://127.0.0.1:${archivePort}`;
      const archiveDirectory = join(directory, "archives");
      const args = ["--directory", archiveDirectory];
      const prepare = startProcess(
        join(buildDirectory, "demo-archives"),
        [
          ...args,
          "--prepare",
          "--sample-set",
          curated ? "curated" : "expanded",
          ...(curated
            ? ["--data-file", join(root, "samples/catalog.yaml")]
            : []),
          "--archive-origin",
          archiveOrigin,
        ],
        { cwd: root },
      );
      processes.push(prepare);
      const result = await completed(prepare);
      if (result !== 0)
        throw new Error(
          `Owned archive preparation failed: ${prepare.diagnostics()}`,
        );
      const allowlistFile = join(archiveDirectory, "allowlist.json");
      const allowlist = JSON.parse(await readFile(allowlistFile, "utf8"));
      if (authoredPlugin) {
        const packageFiles = (documents) =>
          packAuthoredPlugin(directory, documents, async (command, args) => {
            const child = startProcess(command, args, { cwd: root });
            processes.push(child);
            if ((await completed(child)) !== 0)
              throw new Error("Owned Plugin packaging failed");
          });
        const expected = await packageFiles(authoredPluginDocuments);
        const digest =
          "sha256:" + createHash("sha256").update(expected).digest("hex");
        const path = "/browser-generated.zip";
        allowlist.entries.push({ path, digest, size: expected.length });
        allowlist.entries.sort((a, b) => a.path.localeCompare(b.path));
        await writeFile(
          join(archiveDirectory, "objects", digest.slice(7) + ".zip"),
          expected,
          { mode: 0o600 },
        );
        await writeFile(allowlistFile, JSON.stringify(allowlist), {
          mode: 0o600,
        });
        generatedPlugin = {
          url: archiveOrigin + path,
          digest,
          size: expected.length,
          verifyDownloads: async (documents) => {
            const actual = await packageFiles(documents);
            if (!actual.equals(expected))
              throw new Error(
                "Downloaded Plugin archive differs from served bytes",
              );
          },
        };
      }
      const entry = allowlist.entries.find((entry) =>
        curated
          ? entry.path.endsWith(".zip") && entry.size <= 65536
          : entry.path === "/release-workflow.zip",
      );
      if (!entry) throw new Error("Prepared browser archive identity mismatch");
      const bytes = await readFile(
        join(archiveDirectory, "objects", entry.digest.slice(7) + ".zip"),
      );
      if (curated) {
        const digest =
          "sha256:" + createHash("sha256").update(bytes).digest("hex");
        if (
          allowlist.format !== 1 ||
          allowlist.tenantId !==
            config.auth.developmentTokens["local-reader"].tenantId ||
          entry.digest !== digest ||
          entry.size !== bytes.length
        )
          throw new Error("Prepared browser archive identity mismatch");
        archive = {
          url: archiveOrigin + entry.path,
          digest,
          size: bytes.length,
          documents: [],
          files: [],
          components: [],
        };
      } else {
        const expectations = join(directory, "archive-documents.json");
        const oracle = startProcess(
          "python3",
          [
            "-I",
            "-c",
            `
import json, pathlib, sys, zipfile
archive, source, output = map(pathlib.Path, sys.argv[1:])
if archive.stat().st_size > 65536: raise ValueError("archive limit")
names = ["mcp.json", "plugin.json", "skills/prepare-release/SKILL.md"]
with zipfile.ZipFile(archive) as bundle:
    if bundle.namelist() != names: raise ValueError("archive inventory")
    documents = []
    for name in names:
        info = bundle.getinfo(name)
        if info.file_size > 65536 or info.is_dir(): raise ValueError("member limit")
        if (info.external_attr >> 16) & 0o111: raise ValueError("executable member")
        raw = bundle.read(info)
        if raw != (source / name).read_bytes(): raise ValueError("source identity")
        documents.append({"path": name, "content": raw.decode("utf-8")})
with output.open("x") as receipt:
    json.dump(documents, receipt)
`,
            join(archiveDirectory, "objects", entry.digest.slice(7) + ".zip"),
            join(root, "samples/packages/release-workflow"),
            expectations,
          ],
          { cwd: root },
        );
        processes.push(oracle);
        if ((await completed(oracle)) !== 0)
          throw new Error("Prepared archive document oracle failed");
        const documents = JSON.parse(await readFile(expectations, "utf8"));
        archive = archiveReceipt(archiveOrigin, allowlist, bytes, documents);
      }
      config.demoArchives = {
        origin: archiveOrigin,
        allowlistFile,
        trustBundleFile: join(archiveDirectory, "cert.pem"),
      };
      archiveProcess = startProcess(
        join(buildDirectory, "demo-archives"),
        [...args, "--address", `127.0.0.1:${archivePort}`],
        { cwd: root },
      );
      processes.push(archiveProcess);
      const ca = await readFile(config.demoArchives.trustBundleFile);
      await archiveReady(archive, ca, archiveProcess);
      if (curated) artworkTrustBundle = config.demoArchives.trustBundleFile;
      if (artwork) {
        artworkTrustBundle = config.demoArchives.trustBundleFile;
        config.environment = "test";
        config.federation.policyFile = await json("artwork-policy.json", {
          schemaVersion: 1,
          policyId: "browser-artwork",
          revision: 1,
          public: { mode: "allowlist", origins: [] },
          privateProfiles: [
            {
              name: "browser-artwork",
              origins: [archiveOrigin],
              addressRanges: ["127.0.0.1/32"],
              assignedTenantIds: [
                config.auth.developmentTokens["local-reader"].tenantId,
              ],
              transportRef: "direct",
              trustBundleFile: config.demoArchives.trustBundleFile,
            },
          ],
        });
        config.listingMedia.allowedOrigins = [
          { origin: archiveOrigin, networkProfile: "browser-artwork" },
        ];
        delete config.demoArchives;
      }
    }
    const apiEnvironment = runtimeEnvironment();
    if (webhooks) {
      config.webhooks.enabled = true;
      const keyFile = join(directory, "webhook-key");
      await writeFile(keyFile, randomBytes(32).toString("base64"), {
        mode: 0o600,
      });
      apiEnvironment.CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE = keyFile;
    }
    const apiConfig = await json("api.json", config);
    const api = startProcess(
      join(buildDirectory, "catalogd"),
      ["all", "--config", apiConfig],
      { cwd: root, env: apiEnvironment, collectStages: true },
    );
    processes.push(api);
    await ready(apiOrigin, api, policy);
    const versionReply = await fetch(`${apiOrigin}/version`, {
      signal: AbortSignal.timeout(policy?.ownership ?? 1000),
      redirect: "error",
    });
    if (
      !versionReply.ok ||
      (await versionReply.json()).version !== basename(buildDirectory)
    )
      throw new Error(
        "Browser fixture API ownership check failed; refusing to seed",
      );
    const receiptPath = join(directory, "demo.json");
    const seed = startProcess(
      join(buildDirectory, "catalogctl"),
      [
        "seed-demo",
        "--sample-set",
        curated ? "curated" : artwork ? "artwork" : "minimal",
        ...(curated ? ["--data-file", join(root, "samples/catalog.yaml")] : []),
        ...(artwork || curated
          ? [
              "--archive-origin",
              new URL(archive.url).origin,
              "--archive-trust-file",
              artworkTrustBundle,
            ]
          : []),
        "--base-url",
        apiOrigin,
        "--state-file",
        receiptPath,
        "--timeout",
        `${(policy?.seed ?? 30000) / 1000}s`,
      ],
      { cwd: root },
    );
    processes.push(seed);
    if ((await seed.done) !== 0)
      throw new Error(`Owned demo seed failed: ${seed.diagnostics()}`);
    const receipt = JSON.parse(await readFile(receiptPath, "utf8"));
    const keysFile = await json("keys.json", [
      {
        hash: randomBytes(64).toString("hex"),
        block: randomBytes(32).toString("hex"),
      },
    ]);
    const developmentUsersFile = await json(
      "users.json",
      developmentUsers(config.auth.developmentTokens),
    );
    const uiConfig = await json("ui.json", {
      listen: `127.0.0.1:${uiPort}`,
      environment: "development",
      origin,
      basePath,
      upstream: apiOrigin,
      assets: join(buildDirectory, "assets"),
      cookieName: `catalog-ui-test-${uiPort}`,
      keysFile,
      sessionSeconds: 3600,
      requestBytes: 2097152,
      responseBytes: 16777216,
      timeoutSeconds: 30,
      authMode: "development",
      developmentUsersFile,
      documentationUrl: `https://fixture.invalid/${basename(directory)}`,
    });
    const ui = startProcess(
      join(buildDirectory, "catalog-ui"),
      ["--config", uiConfig],
      { cwd: root },
    );
    processes.push(ui);
    await ready(origin, ui, policy);
    const runtimeReply = await fetch(`${origin}${basePath}/runtime.json`, {
      signal: AbortSignal.timeout(policy?.ownership ?? 1000),
      redirect: "error",
    });
    if (
      !runtimeReply.ok ||
      (await runtimeReply.json()).documentationUrl !==
        `https://fixture.invalid/${basename(directory)}`
    )
      throw new Error("Browser fixture UI ownership check failed");
    // Prerequisite setup stays outside the browser, but always targets this
    // verified owned API. Actions under test still use visible UI controls.
    const admin = async (method, path, body, etag, actor = "local-admin") => {
      const publicRead =
        method === "GET" &&
        ["/.well-known/ard.json", "/.well-known/ai-catalog.json"].includes(
          path,
        );
      if (
        !/^(GET|POST|PATCH|DELETE)$/.test(method) ||
        typeof path !== "string" ||
        (!path.startsWith("/api/v1/") && !publicRead) ||
        path.includes("..") ||
        path.includes("#") ||
        ![
          "local-admin",
          "local-contributor",
          "local-reviewer",
          "local-publisher",
          "other-tenant-admin",
        ].includes(actor)
      )
        throw new Error("Invalid fixture setup request");
      const response = await fetch(apiOrigin + path, {
        method,
        headers: {
          ...(publicRead ? {} : { Authorization: `Bearer ${actor}` }),
          "Content-Type": "application/json",
          ...(method !== "GET" ? { "Idempotency-Key": randomUUID() } : {}),
          ...(etag ? { "If-Match": etag } : {}),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        signal: AbortSignal.timeout(policy?.admin ?? 10000),
        redirect: "error",
      });
      if (!response.ok) {
        const problem = await response.json().catch(() => ({}));
        if (
          authorizationCommandOutcome({
            method,
            path,
            status: response.status,
            problem,
          }).refusal === "role_state_or_time"
        ) {
          let evidence = { readSucceeded: false, roleState: "unknown" };
          try {
            const current = await fetch(apiOrigin + path, {
              headers: { Authorization: `Bearer ${actor}` },
              signal: AbortSignal.timeout(policy?.admin ?? 10000),
              redirect: "error",
            });
            evidence = roleRefusalReadback({
              status: current.status,
              document: await current.json().catch(() => null),
              etag,
              date: response.headers.get("Date"),
            });
          } catch {
            // The original command failure remains authoritative if readback fails.
          }
          console.error(
            "Owned role refusal readback:",
            JSON.stringify(evidence),
          );
        }
        throw fixtureSetupError({
          method,
          path,
          status: response.status,
          problem,
        });
      }
      return {
        data: response.status === 204 ? undefined : await response.json(),
        etag: response.headers.get("ETag"),
        date: response.headers.get("Date"),
      };
    };
    return {
      origin,
      basePath,
      receipt,
      close,
      admin,
      diagnostics: () => ({
        api: JSON.parse(api.diagnostics()),
        ui: JSON.parse(ui.diagnostics()),
      }),
      ...(archive
        ? { archive, stopArchives: () => stopProcess(archiveProcess) }
        : {}),
      ...(generatedPlugin ? { generatedPlugin } : {}),
    };
  } catch (error) {
    await close();
    throw error;
  }
}

/** Expectations come from prepared bytes and maintained source, never a Catalog reply. */
export function archiveReceipt(origin, allowlist, bytes, documents) {
  const entry = allowlist.entries?.find(
    (entry) => entry.path === "/release-workflow.zip",
  );
  const digest = "sha256:" + createHash("sha256").update(bytes).digest("hex");
  if (
    allowlist.format !== 1 ||
    allowlist.tenantId !== "00000000-0000-7000-8000-000000000001" ||
    !entry ||
    entry.digest !== digest ||
    entry.size !== bytes.length ||
    bytes.length > 65536 ||
    documents.length !== 3 ||
    documents.some((doc) => !bytes.includes(Buffer.from(doc.content)))
  )
    throw new Error("Prepared browser archive identity mismatch");
  return {
    url: origin + entry.path,
    digest,
    size: bytes.length,
    documents,
    files: documents.map((doc) => ({
      path: doc.path,
      size: Buffer.byteLength(doc.content),
      digest:
        "sha256:" + createHash("sha256").update(doc.content).digest("hex"),
      executable: false,
    })),
    components: [
      {
        kind: "skill",
        name: "prepare-release",
        path: "skills/prepare-release/SKILL.md",
      },
      { kind: "mcp_server", name: "documentation", path: "mcp.json" },
    ].sort(
      (a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name),
    ),
  };
}

/** Bound short fixture-tool phases and reap them through the fixture cleanup on failure. */
async function completed(child) {
  let timer;
  try {
    return await Promise.race([
      child.done,
      new Promise((resolve) => {
        timer = setTimeout(() => resolve(-1), 15000);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

/** Probe anonymous exact bytes with invocation-owned trust and bounded transport. */
async function archiveReady(archive, ca, child) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (
      !child.child.pid ||
      child.child.exitCode !== null ||
      child.child.signalCode !== null
    )
      throw new Error(`Owned archive listener exited: ${child.diagnostics()}`);
    const ok = await new Promise((resolve) => {
      let probe;
      const timer = setTimeout(() => {
        probe?.destroy();
        resolve(false);
      }, 500);
      const finish = (value) => {
        clearTimeout(timer);
        resolve(value);
      };
      probe = request(archive.url, { ca, timeout: 300 }, (response) => {
        const chunks = [];
        let size = 0;
        response.on("data", (chunk) => {
          size += chunk.length;
          if (size > 65536) response.destroy();
          else chunks.push(chunk);
        });
        response.on("end", () =>
          finish(
            response.statusCode === 200 &&
              "sha256:" +
                createHash("sha256")
                  .update(Buffer.concat(chunks))
                  .digest("hex") ===
                archive.digest,
          ),
        );
        response.on("error", () => finish(false));
        response.on("aborted", () => finish(false));
      });
      probe.on("timeout", () => probe.destroy());
      probe.on("error", () => finish(false));
      probe.end();
    });
    if (ok) {
      await delay(100);
      if (child.child.exitCode === null && child.child.signalCode === null)
        return;
    }
    await delay(50);
  }
  throw new Error("Owned archive listener did not become ready");
}
