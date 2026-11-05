import { loadCandidates } from "./candidate-inputs.mjs";
import { developmentUsers } from "./development-users.mjs";
// Small owned image/browser gate. No existing stack URL, retained database,
// provider account, publication, or global TLS verification bypass is accepted.
import { spawn } from "node:child_process";
import { randomBytes, createHash, X509Certificate } from "node:crypto";
import {
  chmod,
  mkdtemp,
  mkdir,
  readFile,
  writeFile,
  rm,
} from "node:fs/promises";
import { writeFixtureInput } from "./fixture-inputs.mjs";
import { request } from "node:http";
import { createServer, get, request as tlsRequest } from "node:https";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { chromium, expect } from "@playwright/test";
import { localPorts, runtimeEnvironment } from "./browser-fixture.mjs";
import { networkObserver } from "./network-observer.mjs";
import { waitForStableNetwork } from "./network-readiness.mjs";
import {
  humanRealm,
  namespaceReadControl,
  ratingsRealm,
} from "./oidc-fixture.mjs";
import {
  ratingsService,
  ratingsOIDCJourney,
  ratingsMissingScope,
} from "./ratings-oidc-journey.mjs";
import {
  collectBrowserCoverage,
  reportBrowserCoverage,
} from "./e2e-coverage.mjs";
import {
  rotationParts,
  rotationDiagnostics,
  rotationDatabaseDiagnostics,
  rotationResponse,
  rotationStateSQL,
  collectRotationEvidence,
} from "./rotation-evidence.mjs";
import { authorizationJourney } from "./authorization-journey.mjs";
import { authorizationQualificationJourney } from "./authorization-qualification-journey.mjs";
import { packagedJourneys } from "./packaged-journeys.mjs";
import { mcpRealm } from "./mcp-fixture.mjs";
import { mcpJourney, mcpDisabled } from "./mcp-journey.mjs";
import { cliOIDCJourney } from "./cli-oidc-journey.mjs";
import { publisherVerificationJourney } from "./publisher-verification-journey.mjs";
import { localPasswordJourney } from "./local-password-journey.mjs";
import { manualLayout } from "./manual-layout.mjs";

const mode = process.argv[2];
if (!["compose", "oidc"].includes(mode))
  throw new Error("Select compose or oidc");
const coverage = process.argv[3] === "--coverage";
const mcp = process.argv[3] === "--mcp";
const publisherDNSOnly = process.argv[3] === "--publisher-dns";
const localPasswords = process.argv[3] === "--local-passwords";
if (
  (mcp && mode !== "oidc") ||
  (localPasswords && mode !== "oidc") ||
  (publisherDNSOnly && mode !== "compose") ||
  (process.argv[3] &&
    !coverage &&
    !mcp &&
    !publisherDNSOnly &&
    !localPasswords) ||
  process.argv.length >
    (coverage || mcp || publisherDNSOnly || localPasswords ? 4 : 3)
)
  throw new Error("Unexpected packaged test selection");
const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const candidates = loadCandidates(root, process.env, {
  coverage,
  mcp,
  publisherDNSOnly,
  localPasswords,
});
const coverageRoot = join(root, "output/coverage/e2e", mode);
if (coverage) await mkdir(coverageRoot, { recursive: true });
const coverageOutput = coverage
  ? await mkdtemp(join(coverageRoot, "run-"))
  : undefined;
let coverageContexts = 0;
let coverageBrowser;
const privateDirectory = await mkdtemp(join(tmpdir(), "catalog-ui-packaged-"));
const directory = join(privateDirectory, "fixture");
await mkdir(directory, { mode: 0o755 });
await chmod(directory, 0o755);
const project = basename(privateDirectory).toLowerCase();
const image = candidates?.backend.imageId ?? `open-agentic-catalog:${project}`;
const uiImage = candidates?.ui.imageId ?? `open-agentic-catalog-ui:${project}`;
const expectedBuild = candidates?.backend.build ?? {
  version: project,
  commit: "unknown",
  sourceState: "unknown",
};
const expectedUIBuild = candidates?.ui.build ?? expectedBuild;
const fixtureImage = `open-agentic-catalog-fixture:${project}`;
const tenant = "00000000-0000-7000-8000-000000000001";
const subject = "00000000-0000-4000-8000-000000000011";
const password = randomBytes(24).toString("hex");
const secret = randomBytes(32).toString("hex");
const ratingsSecret = randomBytes(32).toString("hex");
const env = {
  ...runtimeEnvironment(),
  IMAGE: image,
  UI_IMAGE: uiImage,
  COMPOSE_PROGRESS: "quiet",
  CATALOG_COMPOSE_POSTGRES_PORT: "0",
  CATALOG_UI_FIXTURE: directory,
};
let browser,
  proxy,
  documentationServer,
  interrupted = false,
  cleaning = false,
  active,
  inspectAuthorizationCommandOutcome,
  stopTimer;
/** Signal the active packaged-test process group without touching other container launchers. */
function signalCommand(signal) {
  if (!active?.pid) return;
  try {
    process.kill(-active.pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}
const stop = () => {
  process.exitCode = 1;
  if (interrupted) return;
  interrupted = true;
  if (cleaning) return;
  signalCommand("SIGTERM");
  stopTimer ??= setTimeout(() => signalCommand("SIGKILL"), 10000);
  void browser?.close();
};
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
// Deferred browser waiters can reject before their later await after cancellation.
// Fail the owned runner and unwind its main task so finally can remove the fixture.
process.on("unhandledRejection", stop);

// Suppress command output: provider imports, CLI identity replies and engine
// diagnostics can include private fixture inputs. Errors report fixed phases.
async function command(args, seconds = 120, cleanup = false, onDiagnostic) {
  if (interrupted && !cleanup) throw new Error("Packaged UI test interrupted");
  return new Promise((done, fail) => {
    const child = spawn(args[0], args.slice(1), {
      cwd: root,
      env,
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    active = child;
    let output = "",
      diagnostic = "",
      bytes = 0,
      exceeded = false,
      spawnFailed = false;
    const capture = (chunk) => {
      bytes += chunk.length;
      if (bytes <= 8 << 20) output += chunk;
      else {
        exceeded = true;
        signalCommand("SIGKILL");
      }
    };
    child.stdout.on("data", capture);
    child.stderr.on("data", (chunk) => {
      bytes += chunk.length;
      if (bytes <= 8 << 20) diagnostic += chunk;
      if (bytes <= 8 << 20) onDiagnostic?.(diagnostic);
      if (bytes > 8 << 20) {
        exceeded = true;
        signalCommand("SIGKILL");
      }
    });
    const timer = setTimeout(() => signalCommand("SIGKILL"), seconds * 1000);
    child.once("error", () => {
      spawnFailed = true;
    });
    // Wait for both output streams to close before consuming command output
    // (notably the published-port lookup). exit alone can precede their drain.
    child.once("close", (code) => {
      clearTimeout(timer);
      clearTimeout(stopTimer);
      stopTimer = undefined;
      signalCommand("SIGKILL");
      active = undefined;
      if (spawnFailed) fail(new Error("Packaged command could not start"));
      else if (code !== 0 || exceeded) {
        // Only fixed categories, not arbitrary engine/CLI output.
        const categories = [
          "unhealthy",
          "address already in use",
          "port is already allocated",
          "permission denied",
          "no space left",
          "tenant already exists",
          "tenant_admin",
          "snapshot",
          "publication",
          "not found",
          "invalid",
          "deadline",
          "timeout",
          "failed to solve",
          "auth: issuer declined authorization",
          "auth: authorization code exchange failed",
          "auth: id token verification failed",
          "auth: id token nonce or identity changed",
          "auth: catalog refused the access token",
          "auth: catalog identity does not match",
          "auth: cannot sync credential directory",
          "auth: issuer must return",
          "auth: issuer did not return",
          "issuer refused code, redirect or pkce binding",
          "issuer refused public client registration",
          "issuer refused scope or access",
          "issuer request failed",
          "invalid or oversized issuer response",
          "issuer refused token request",
          "issuer rejected token request parameters",
          "issuer does not support code exchange",
          "issuer is unavailable",
        ];
        console.error(
          `Command exit ${code}; categories: ${categories.filter((value) => (output + diagnostic).toLowerCase().includes(value)).join(",") || "unclassified"}`,
        );
        fail(new Error("Packaged command failed or exceeded its bound"));
      } else done(output);
    });
  });
}
const json = async (name, value) =>
  writeFixtureInput(join(directory, name), JSON.stringify(value));
const baseCompose = [
  "docker",
  "compose",
  "--env-file",
  "/dev/null",
  "-p",
  project,
  "-f",
  "compose.yaml",
  "-f",
  "catalog-ui/tests/compose.yaml",
];
const compose = (...args) => command([...baseCompose, ...args]);
// This selection exercises the documented launcher instead of preparing the
// ordinary packaged realm. It shares bounded commands and cancellation.
if (localPasswords) {
  delete env.BUILDKIT_HOST;
  env.BUILDX_BUILDER = "default";
  try {
    await localPasswordJourney({
      command,
      root,
      privateDirectory,
      image,
      uiImage,
      onBrowser: (value) => {
        browser = value;
      },
      onCleanup: () => {
        cleaning = true;
        clearTimeout(stopTimer);
        console.log("Local password cleanup started");
      },
      isInterrupted: () => interrupted,
    });
    process.exit(interrupted || process.exitCode ? 1 : 0);
  } catch {
    console.error(
      interrupted
        ? "Local password workflow interrupted; inspect its safe report."
        : "Local password workflow failed; inspect its safe report.",
    );
    process.exit(1);
  }
}
/** Resolve an invocation-owned Compose service only when its published port is loopback-bound. */
async function publishedPort(service) {
  const value = (await compose("port", service, "8080")).trim();
  if (!/^127\.0\.0\.1:\d+$/.test(value))
    throw new Error("Owned loopback port not found");
  return Number(value.split(":")[1]);
}
/** Poll bounded fixture readiness using the supplied private trust when HTTPS is required. */
async function ready(url, ca) {
  const deadline = Date.now() + 120000;
  while (Date.now() < deadline && !interrupted) {
    try {
      const status = ca
        ? await new Promise((done, fail) => {
            const req = get(
              url,
              {
                ca,
                timeout: 1000,
                lookup: (_host, options, cb) =>
                  options.all
                    ? cb(null, [{ address: "127.0.0.1", family: 4 }])
                    : cb(null, "127.0.0.1", 4),
              },
              (res) => {
                res.resume();
                done(res.statusCode);
              },
            );
            req.on("error", fail);
            req.on("timeout", () => req.destroy(new Error("timeout")));
          })
        : await fetch(url, {
            signal: AbortSignal.timeout(1000),
            redirect: "error",
          }).then(async (res) => {
            await res.body?.cancel();
            return res.status;
          });
      if (status === 200) return;
    } catch {
      /* Bounded startup reads, never mutation retries. */
    }
    await delay(200);
  }
  throw new Error("Owned packaged listener did not become ready");
}
let phase = "setup";
let mcpChecks = [];
let mcpCleanup = false;
let mcpBuild;
let outcomes;
let lastPage;
try {
  let apiPort, uiPort, ratingsSubject, ratingsFilename;
  let issuer;
  Object.assign(env, {
    CATALOG_COMPOSE_HTTP_PORT: "8080",
    CATALOG_UI_PORT: "0",
  });
  await command([
    "openssl",
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-days",
    "2",
    "-keyout",
    join(directory, "server-key.pem"),
    "-out",
    join(directory, "server.pem"),
    "-subj",
    "/CN=catalog-oidc",
    "-addext",
    "subjectAltName=DNS:catalog-oidc,DNS:catalog-mcp.test,IP:127.0.0.1",
    "-addext",
    "basicConstraints=critical,CA:TRUE",
  ]);
  // The private invocation directory is 0700. Container-readable leaves are
  // required for the non-root UI and external provider, not host-global trust.
  const key = await readFile(join(directory, "server-key.pem"));
  await rm(join(directory, "server-key.pem"));
  await writeFixtureInput(join(directory, "server-key.pem"), key);
  const cert = await readFile(join(directory, "server.pem"));
  await writeFixtureInput(join(directory, "server.pem"), cert);
  // The host ephemeral range can include browser-blocked ports.
  const [documentationPort, proxyPort] = await localPorts();
  let documentationAvailable = true;
  const documentationRequests = [];
  documentationServer = createServer({ key, cert }, (incoming, outgoing) => {
    if (incoming.url === "/favicon.ico") {
      outgoing.writeHead(204);
      outgoing.end();
      return;
    }
    documentationRequests.push({
      exactPath: incoming.url === "/manual/",
      authorization: Boolean(incoming.headers.authorization),
      referrer: Boolean(incoming.headers.referer),
    });
    outgoing.writeHead(documentationAvailable ? 200 : 503, {
      "Content-Type": "text/html",
    });
    outgoing.end(
      documentationAvailable
        ? "<h1>Owned operator manual</h1>"
        : "<h1>Documentation unavailable</h1>",
    );
  });
  await new Promise((done, fail) => {
    documentationServer.once("error", fail);
    documentationServer.listen(documentationPort, "127.0.0.1", done);
  });
  const documentationOrigin = `https://127.0.0.1:${documentationServer.address().port}`;
  let dropNextSettingsReply = false;
  let droppedSettingsReplies = 0;
  const settingsFault = {
    arm() {
      if (dropNextSettingsReply)
        throw new Error("Owned settings reply fault already armed");
      dropNextSettingsReply = true;
    },
    dropped: () => droppedSettingsReplies,
  };
  let publisherDNSReply;
  const publisherDNSFault = {
    arm(path, corrupt) {
      if (publisherDNSReply)
        throw new Error("Owned Publisher DNS reply fault already armed");
      publisherDNSReply = { path, corrupt, attempts: [], replies: [] };
      return publisherDNSReply;
    },
    clear() {
      publisherDNSReply = undefined;
    },
  };
  proxy = createServer({ key, cert }, (incoming, outgoing) => {
    if (mcp && incoming.url?.startsWith("/mcp-oauth/callback?")) {
      outgoing.writeHead(200, {
        "Content-Type": "text/html",
        "Cache-Control": "no-store",
        "Referrer-Policy": "no-referrer",
      });
      outgoing.end("<h1>Owned MCP authorization complete</h1>");
      return;
    }
    const upstreamPort =
      mcp &&
      ["/mcp", "/.well-known/oauth-protected-resource/mcp"].includes(
        incoming.url,
      )
        ? apiPort
        : uiPort;
    if (!upstreamPort) {
      outgoing.writeHead(503);
      outgoing.end();
      return;
    }
    const dnsReply =
      incoming.method === "POST" && incoming.url === publisherDNSReply?.path
        ? publisherDNSReply
        : undefined;
    if (dnsReply) {
      const attempt = {
        url: incoming.url,
        method: incoming.method,
        body: "",
        key: incoming.headers["idempotency-key"],
        etag: incoming.headers["if-match"],
      };
      dnsReply.attempts.push(attempt);
      incoming.on("data", (chunk) => {
        attempt.body += chunk.toString("utf8");
        if (Buffer.byteLength(attempt.body) > 4096) incoming.destroy();
      });
    }
    const upstream = request(
      {
        host: "127.0.0.1",
        port: upstreamPort,
        path: incoming.url,
        method: incoming.method,
        headers: incoming.headers,
        timeout: 30000,
      },
      (reply) => {
        if (dnsReply) {
          const chunks = [];
          let bytes = 0;
          reply.on("data", (chunk) => {
            bytes += chunk.length;
            if (bytes > 65536) reply.destroy();
            else chunks.push(chunk);
          });
          reply.once("error", () => outgoing.destroy());
          reply.once("end", () => {
            const original = Buffer.concat(chunks).toString("utf8");
            dnsReply.replies.push({
              status: reply.statusCode,
              body: original,
              replayed: reply.headers["idempotency-replayed"],
            });
            try {
              const body =
                dnsReply.replies.length === 1 && reply.statusCode === 200
                  ? JSON.stringify(dnsReply.corrupt(JSON.parse(original)))
                  : original;
              const headers = {
                ...reply.headers,
                "content-length": Buffer.byteLength(body),
              };
              delete headers["transfer-encoding"];
              outgoing.writeHead(reply.statusCode, headers);
              outgoing.end(body);
            } catch {
              outgoing.destroy();
            }
          });
          return;
        }
        if (
          dropNextSettingsReply &&
          incoming.method === "POST" &&
          [
            "/_catalog/api/v1/tenant-settings",
            "/browser/_catalog/api/v1/tenant-settings",
          ].includes(incoming.url) &&
          reply.statusCode === 200
        ) {
          dropNextSettingsReply = false;
          // Consume the real committed reply, then truncate its browser hop.
          // Sending headers first prevents a transparent connection retry.
          reply.once("end", () => {
            droppedSettingsReplies++;
            outgoing.writeHead(200, {
              "Content-Type": "application/json",
              "Content-Length": "2",
              "Cache-Control": "no-store",
            });
            outgoing.write("{", () => outgoing.destroy());
          });
          reply.once("error", () => outgoing.destroy());
          reply.resume();
          return;
        }
        outgoing.writeHead(reply.statusCode, reply.headers);
        reply.pipe(outgoing);
      },
    );
    upstream.on("error", () => {
      if (!outgoing.headersSent) outgoing.writeHead(502);
      outgoing.end();
    });
    upstream.on("timeout", () => upstream.destroy());
    incoming.on("aborted", () => upstream.destroy());
    incoming.pipe(upstream);
  });
  await new Promise((done, fail) => {
    proxy.once("error", fail);
    proxy.listen(proxyPort, "127.0.0.1", done);
  });
  const origin = `https://127.0.0.1:${proxy.address().port}`;
  const mcpOrigin = `https://catalog-mcp.test:${proxy.address().port}`;
  const spki = createHash("sha256")
    .update(
      new X509Certificate(cert).publicKey.export({
        type: "spki",
        format: "der",
      }),
    )
    .digest("base64");
  await json("keys.json", [
    {
      hash: randomBytes(64).toString("hex"),
      block: randomBytes(32).toString("hex"),
    },
  ]);
  await writeFixtureInput(join(directory, "secret"), secret);
  const config = JSON.parse(
    await readFile(join(root, "config/postgres.json"), "utf8"),
  );
  config.logFormat = "json";
  // Expected authorization conflicts stay DEBUG. Retain only fixed correlated
  // fields below if an owned command fails; private fixture logs are removed.
  config.logLevel = "debug";
  config.auth.developmentTokens["local-policy"] = {
    subject: "local-policy",
    tenantId: tenant,
    roles: ["reader"],
  };
  await json("users.json", developmentUsers(config.auth.developmentTokens));
  config.discovery = {
    hosts: [
      {
        host: "127.0.0.1",
        tenantId: tenant,
        mode: "anonymous",
        publisherDomain: "catalog.example.com",
      },
    ],
  };
  await json("api.json", config);
  phase = "build";
  console.log(
    candidates
      ? "Using admitted application candidates; building the owned fixture image"
      : `Building owned ${mcp ? "MCP" : mode + " browser"} images`,
  );
  const buildMetadata = mcp
    ? JSON.parse(
        await command([
          "python3",
          "-B",
          "test/packaging/versioning.py",
          "metadata",
        ]),
      )
    : undefined;
  if (!candidates)
    await command(
      [
        "docker",
        "build",
        "--build-arg",
        `VERSION=${project}`,
        ...(mcp
          ? [
              "--build-arg",
              `COMMIT=${buildMetadata.commit}`,
              "--build-arg",
              `SOURCE_STATE=${buildMetadata.sourceState}`,
            ]
          : []),
        "-f",
        "Containerfile",
        "-t",
        image,
        ".",
      ],
      600,
    );
  if (!mcp && !candidates)
    await command(
      [
        "docker",
        "build",
        "--build-arg",
        `VERSION=${project}`,
        "-f",
        "catalog-ui/Containerfile",
        "-t",
        uiImage,
        ".",
      ],
      600,
    );
  if (coverage) {
    env.CATALOG_UI_E2E_COVERAGE = coverageOutput;
    const assets = join(directory, "assets");
    await command([
      process.execPath,
      "catalog-ui/node_modules/vite/bin/vite.js",
      "build",
      "catalog-ui",
      "--outDir",
      assets,
    ]);
    delete env.CATALOG_UI_E2E_COVERAGE;
    // Instrument only React. Preserve the exact manual from this image when
    // mounting the coverage frontend; docs stay outside its coverage budgets.
    const extracted = (
      await command(["docker", "create", "--network=none", uiImage])
    ).trim();
    if (!/^[a-f0-9]{64}$/.test(extracted))
      throw new Error("Invalid manual extraction container");
    try {
      await command([
        "docker",
        "cp",
        `${extracted}:/app/assets/docs`,
        join(assets, "docs"),
      ]);
    } finally {
      await command(["docker", "rm", extracted], 30, true);
    }
    await json("coverage.json", {
      services: { "ui-browser": { volumes: [`${assets}:/app/assets:ro`] } },
    });
    baseCompose.push("-f", join(directory, "coverage.json"));
  }
  phase = "catalog-startup";
  if (mcp) {
    // Exercise the shipped Compose substitutions for both runtime roles.
    // Rendering does not bind these test ports or start another service.
    for (const [port, publicURL, expected] of [
      ["8080", undefined, "http://localhost:8080"],
      ["18443", undefined, "http://localhost:18443"],
      ["8080", mcpOrigin, mcpOrigin],
    ]) {
      env.CATALOG_COMPOSE_HTTP_PORT = port;
      if (publicURL) env.CATALOG_PUBLIC_BASE_URL = publicURL;
      else delete env.CATALOG_PUBLIC_BASE_URL;
      const model = JSON.parse(await compose("config", "--format", "json"));
      for (const role of ["api", "worker"])
        if (
          model.services[role].environment.CATALOG_PUBLIC_BASE_URL !== expected
        )
          throw new Error("Owned Compose public URL substitution mismatch");
    }
    env.CATALOG_COMPOSE_HTTP_PORT = "8080";
    delete env.CATALOG_PUBLIC_BASE_URL;
    mcpChecks.push(
      "API and worker preserve default, custom-port and explicit public URLs",
    );
  }
  let fixtureRequest;
  if (mode === "compose") {
    // Allocate the invocation's ordinary Docker network before generating an
    // exact private-route policy. No reserved developer subnet or host trust.
    await compose("up", "-d", "--wait", "postgres");
    const [network] = JSON.parse(
      await command(["docker", "network", "inspect", `${project}_default`]),
    );
    if (network.Labels["com.docker.compose.project"] !== project)
      throw new Error("Fixture network ownership mismatch");
    const upstreamDirectory = join(directory, "upstream");
    await command([
      "go",
      "run",
      "./test/functional/fixture",
      "generate",
      "--directory",
      upstreamDirectory,
      "--subnet",
      network.IPAM.Config[0].Subnet,
      "--webhooks",
    ]);
    env.CATALOG_COMPOSE_FEDERATION_POLICY_FILE = join(
      upstreamDirectory,
      "policy.json",
    );
    env.CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY = join(
      upstreamDirectory,
      "trust",
    );
    env.CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY = upstreamDirectory;
    env.CATALOG_FUNCTIONAL_FIXTURE_IMAGE = fixtureImage;
    env.CATALOG_COMPOSE_FIXTURE_PORT = "0";
    baseCompose.push(
      "-f",
      "test/functional/compose.yaml",
      "-f",
      "test/functional/webhooks.compose.yaml",
    );
    await command(
      [
        "docker",
        "build",
        "-f",
        "test/functional/Containerfile",
        "-t",
        fixtureImage,
        ".",
      ],
      300,
    );
    await json("publisher-dns.json", {
      services: {
        "federation-fixture": {
          command: [
            "serve",
            "--directory",
            "/fixture",
            "--credentials",
            "--publisher-dns",
          ],
          sysctls: { "net.ipv4.ip_unprivileged_port_start": "0" },
        },
        worker: { volumes: [`${directory}/api.json:/config/config.json:ro`] },
      },
    });
    baseCompose.push("-f", join(directory, "publisher-dns.json"));
    await compose("up", "-d", "--no-deps", "federation-fixture");
    const fixtureID = (
      await compose("ps", "--quiet", "federation-fixture")
    ).trim();
    const [fixtureContainer] = JSON.parse(
      await command(["docker", "inspect", fixtureID]),
    );
    const resolverIP =
      fixtureContainer.NetworkSettings.Networks[`${project}_default`].IPAddress;
    if (!/^\d+\.\d+\.\d+\.\d+$/.test(resolverIP))
      throw new Error("Owned DNS address is missing");
    await json("publisher-resolver.json", {
      services: { api: { dns: [resolverIP] } },
    });
    baseCompose.push("-f", join(directory, "publisher-resolver.json"));
    const address = (
      await compose("port", "federation-fixture", "8443")
    ).trim();
    if (!/^127\.0\.0\.1:\d+$/.test(address))
      throw new Error("Fixture port is not loopback");
    const ca = await readFile(
      join(upstreamDirectory, "trust/functional-ca.pem"),
    );
    // The test-only controls are reached over their own verified TLS origin.
    // Replies are bounded and never printed (receiver registration has a key).
    fixtureRequest = async (path, body) =>
      new Promise((done, fail) => {
        const req = tlsRequest(
          {
            hostname: "127.0.0.1",
            port: Number(address.split(":")[1]),
            servername: "federation-fixture",
            path,
            ca,
            method: body ? "POST" : "GET",
            timeout: 5000,
            headers: body ? { "Content-Type": "application/json" } : {},
          },
          (res) => {
            let bytes = 0,
              text = "";
            res.on("data", (chunk) => {
              bytes += chunk.length;
              if (bytes > 65536) req.destroy(new Error("Fixture reply limit"));
              else text += chunk;
            });
            res.on("end", () => {
              if (res.statusCode < 200 || res.statusCode >= 300)
                fail(new Error("Fixture control rejected"));
              else {
                try {
                  done(text ? JSON.parse(text) : undefined);
                } catch {
                  fail(new Error("Invalid fixture reply"));
                }
              }
            });
            res.on("error", fail);
          },
        );
        req.on("error", fail);
        req.on("timeout", () =>
          req.destroy(new Error("Fixture control timeout")),
        );
        if (body) req.write(JSON.stringify(body));
        req.end();
      });
  }
  console.log("Starting owned Catalog API, worker and PostgreSQL");
  await compose("up", "-d", "--wait", "api", "worker");
  if (candidates) {
    for (const service of ["api", "worker"]) {
      const container = (await compose("ps", "--quiet", service)).trim();
      if (
        (
          await command([
            "docker",
            "inspect",
            "--format",
            "{{.Image}}",
            container,
          ])
        ).trim() !== candidates.backend.imageId
      )
        throw new Error("Running backend differs from the admitted candidate");
    }
  }
  apiPort = await publishedPort("api");
  await ready(`http://127.0.0.1:${apiPort}/readyz`);
  const version = await fetch(`http://127.0.0.1:${apiPort}/version`, {
    signal: AbortSignal.timeout(3000),
  }).then((res) => res.json());
  if (
    version.version !== expectedBuild.version ||
    (!mcp &&
      (version.commit !== expectedBuild.commit ||
        version.sourceState !== expectedBuild.sourceState))
  )
    throw new Error("Packaged API ownership mismatch");
  if (mcp) {
    const imageId = (
      await command([
        "docker",
        "image",
        "inspect",
        "--format",
        "{{.Id}}",
        image,
      ])
    ).trim();
    const apiContainer = (await compose("ps", "--quiet", "api")).trim();
    const runningImageId = (
      await command([
        "docker",
        "inspect",
        "--format",
        "{{.Image}}",
        apiContainer,
      ])
    ).trim();
    if (
      !/^sha256:[a-f0-9]{64}$/.test(imageId) ||
      runningImageId !== imageId ||
      version.commit !== buildMetadata.commit ||
      version.sourceState !== buildMetadata.sourceState
    )
      throw new Error("Owned MCP build identity mismatch");
    mcpBuild = { ...version, imageId, runningImageId };
    mcpChecks.push(
      "running API image matches the rebuilt artifact and embedded source identity",
    );
    mcpChecks.push(...(await mcpDisabled(mcpOrigin, cert)));
  }
  phase = "catalog-seed";
  await compose(
    "exec",
    "-T",
    "api",
    "/usr/local/bin/catalogctl",
    "seed-demo",
    "--sample-set",
    "minimal",
    "--base-url",
    "http://127.0.0.1:8080",
    "--state-file",
    "/tmp/browser-demo.json",
    "--timeout",
    "60s",
  );
  let foreignNamespace;
  if (mcp) {
    const response = await fetch(
      `http://127.0.0.1:${apiPort}/api/v1/namespaces`,
      {
        method: "POST",
        signal: AbortSignal.timeout(15000),
        headers: {
          Authorization: "Bearer other-tenant-admin",
          "Content-Type": "application/json",
          "Idempotency-Key": randomBytes(24).toString("hex"),
        },
        body: JSON.stringify({
          slug: "mcp-foreign",
          displayName: "Other tenant namespace",
        }),
      },
    );
    if (!response.ok)
      throw new Error("Owned foreign namespace creation failed");
    foreignNamespace = (await response.json()).id;
  }
  if (mode === "oidc") {
    // Select the provider port immediately before binding, not before builds.
    const [providerPort] = await localPorts();
    issuer = `https://catalog-oidc:${providerPort}/realms/catalog`;
    phase = "provider-startup";
    console.log("Starting pinned external test provider");
    const realm = JSON.parse(
      await readFile(join(root, "test/functional/oidc-realm.json"), "utf8"),
    );
    const human = humanRealm(realm, { origin, subject, password, secret });
    const providerRealm = mcp ? mcpRealm(human, mcpOrigin) : human;
    await json("catalog-realm.json", providerRealm);
    if (!mcp) {
      const ratings = ratingsRealm(human, ratingsSecret);
      ratingsSubject = subject;
      // Keycloak requires <realm-name>-realm.json for directory imports.
      ratingsFilename = `${ratings.realm}-realm.json`;
      await json(ratingsFilename, ratings);
    }
    if (mcp)
      await json("other-catalog-realm.json", {
        ...providerRealm,
        realm: "other-catalog",
        id: "other-catalog",
        users: providerRealm.users.map((user) => ({ ...user, id: undefined })),
      });
    // Read the repository's single digest pin rather than introduce a second.
    const overlay = await readFile(
      join(root, "test/functional/compose.oidc.yaml"),
      "utf8",
    );
    const providerImage = overlay.match(
      /image: (quay\.io\/keycloak\/keycloak:[^\s]+@sha256:[a-f0-9]{64})/,
    )?.[1];
    if (!providerImage) throw new Error("Missing pinned external provider");
    await json("provider.json", {
      services: {
        "catalog-oidc": {
          image: providerImage,
          command: [
            "start-dev",
            "--import-realm",
            "--http-enabled=false",
            `--https-port=${providerPort}`,
            `--hostname=https://catalog-oidc:${providerPort}`,
            "--https-certificate-file=/fixture/server.pem",
            "--https-certificate-key-file=/fixture/server-key.pem",
          ],
          ports: [`127.0.0.1:${providerPort}:${providerPort}`],
          volumes: [
            `${directory}/server.pem:/fixture/server.pem:ro`,
            `${directory}/server-key.pem:/fixture/server-key.pem:ro`,
            `${directory}/catalog-realm.json:/opt/keycloak/data/import/catalog-realm.json:ro`,
            ...(mcp
              ? [
                  `${directory}/other-catalog-realm.json:/opt/keycloak/data/import/other-catalog-realm.json:ro`,
                ]
              : [
                  `${directory}/${ratingsFilename}:/opt/keycloak/data/import/${ratingsFilename}:ro`,
                ]),
          ],
          mem_limit: "1g",
          cpus: 2,
          pids_limit: 256,
          cap_drop: ["ALL"],
          security_opt: ["no-new-privileges:true"],
        },
      },
    });
    baseCompose.push("-f", join(directory, "provider.json"));
    await compose("up", "-d", "--no-deps", "catalog-oidc");
    await ready(issuer + "/.well-known/openid-configuration", cert);
    phase = "human-provisioning";
    console.log("Provisioning the synthetic human Catalog membership");
    await compose(
      "run",
      "--rm",
      "--no-deps",
      "--entrypoint",
      "/usr/local/bin/catalogctl",
      "migrate",
      "identity",
      "provision",
      "--config",
      "/config/config.json",
      "--tenant",
      tenant,
      "--tenant-name",
      "Tenant " + tenant,
      "--issuer",
      issuer,
      "--tenancy-mode",
      "dedicated",
      "--subject",
      subject,
      "--actor-type",
      "human",
      "--roles",
      "tenant_admin,reader",
      "--operator",
      "owned-browser-fixture",
      "--reason",
      "Human browser qualification",
    );
    if (mcp) {
      await compose(
        "run",
        "--rm",
        "--no-deps",
        "--entrypoint",
        "/usr/local/bin/catalogctl",
        "migrate",
        "identity",
        "provision",
        "--config",
        "/config/config.json",
        "--tenant",
        tenant,
        "--tenant-name",
        "Tenant " + tenant,
        "--issuer",
        issuer,
        "--tenancy-mode",
        "dedicated",
        "--subject",
        "00000000-0000-4000-8000-000000000013",
        "--actor-type",
        "human",
        "--roles",
        "tenant_admin,reader",
        "--operator",
        "owned-mcp-fixture",
        "--reason",
        "MCP current permission qualification",
      );
      Object.assign(env, {
        CATALOG_PUBLIC_BASE_URL: mcpOrigin,
        CATALOG_MCP_ENABLED: "true",
        CATALOG_MCP_ALLOWED_ORIGINS: JSON.stringify([origin]),
        CATALOG_MCP_MAX_TOOL_BYTES: "16384",
        CATALOG_MCP_MAX_CONCURRENCY: "2",
      });
    }
    config.auth = {
      mode: "oidc",
      issuer,
      audience: "open-agentic-catalog",
      tenancyMode: "dedicated",
      dedicatedTenantId: tenant,
      trustBundleFile: "/fixture/server.pem",
      ...(mcp ? { enforceScopes: true } : {}),
    };
    // Atomic file replacement is followed by container recreation, not an
    // in-place configuration mutation observed halfway through startup.
    await rm(join(directory, "api.json"));
    await json("api.json", config);
    await compose("up", "-d", "--no-deps", "--force-recreate", "api");
    apiPort = await publishedPort("api");
    await ready(`http://127.0.0.1:${apiPort}/readyz`);
  }
  const launchBrowser = () =>
    chromium.launch({
      handleSIGINT: false,
      handleSIGTERM: false,
      args: [
        `--ignore-certificate-errors-spki-list=${spki}`,
        "--host-resolver-rules=MAP catalog-oidc 127.0.0.1, MAP catalog-mcp.test 127.0.0.1",
        "--no-proxy-server",
      ],
    });
  if (mode === "oidc" && !mcp && !coverage) {
    phase = "cli-network-readiness";
    await waitForStableNetwork({ interrupted: () => interrupted });
    phase = "cli-human-oidc";
    const cliBrowser = await launchBrowser();
    try {
      await cliOIDCJourney({
        command,
        browser: cliBrowser,
        image,
        directory: privateDirectory,
        certificate: join(directory, "server.pem"),
        issuer,
        apiOrigin: `http://127.0.0.1:${apiPort}`,
        username: "browser-human",
        password,
        subject,
        project,
        onPhase: (next) => {
          phase = next;
        },
      });
    } finally {
      await cliBrowser.close();
    }
  }
  if (mcp) {
    const report = JSON.parse(
      await compose(
        "exec",
        "-T",
        "api",
        "/usr/local/bin/catalogctl",
        "config",
        "validate",
        "--config",
        "/config/config.json",
        "--role",
        "api",
      ),
    );
    const settings = report.configuration.mcp;
    if (
      report.configuration.http.publicBaseUrl !== mcpOrigin ||
      !settings.enabled ||
      settings.maxToolBytes !== 16384 ||
      settings.maxConcurrency !== 2 ||
      JSON.stringify(settings.allowedOrigins) !== JSON.stringify([origin])
    )
      throw new Error("Owned Compose MCP configuration mismatch");
    mcpChecks.push(
      "all four MCP environment inputs reach the packaged API configuration",
    );
    await waitForStableNetwork({ interrupted: () => interrupted });
    browser = await launchBrowser();
    mcpChecks.push(
      ...(await mcpJourney({
        browser,
        origin: mcpOrigin,
        browserOrigin: origin,
        issuer,
        ca: cert,
        password,
        apiBase: `http://127.0.0.1:${apiPort}`,
        foreignNamespace,
        onPhase: (value) => {
          phase = value;
          console.log(`Checking ${phase}`);
        },
      })),
    );
    phase = "mcp-disabled-restart";
    env.CATALOG_MCP_ENABLED = "false";
    await compose("up", "-d", "--no-deps", "--force-recreate", "api");
    apiPort = await publishedPort("api");
    await ready(`http://127.0.0.1:${apiPort}/readyz`);
    mcpChecks.push(...(await mcpDisabled(mcpOrigin, cert)));
  }
  for (const basePath of mcp
    ? []
    : publisherDNSOnly
      ? ["/browser"]
      : ["", "/browser"]) {
    phase = `${mode}-${basePath ? "subpath" : "root"}`;
    console.log(`Checking ${phase}`);
    await rm(join(directory, "ui.json"), { force: true });
    const uiConfig = {
      listen: "0.0.0.0:8080",
      environment: mode === "oidc" ? "production" : "development",
      logFormat: "json",
      origin,
      basePath,
      upstream: "http://api:8080",
      assets: "/app/assets",
      cookieName: "catalog-ui-packaged",
      keysFile: "/fixture/keys.json",
      sessionSeconds: 3600,
      requestBytes: 2097152,
      responseBytes: 16777216,
      timeoutSeconds: 30,
      authMode: mode === "oidc" ? "oidc" : "development",
      ...(mode === "oidc"
        ? {
            issuer,
            issuerCAFile: "/fixture/server.pem",
            clientId: "catalog-ui",
            clientSecretFile: "/fixture/secret",
            scopes: ["profile", "email"],
          }
        : { developmentUsersFile: "/fixture/users.json" }),
    };
    await json("ui.json", uiConfig);
    await compose("up", "-d", "--no-deps", "--force-recreate", "ui-browser");
    if (candidates) {
      const container = (await compose("ps", "--quiet", "ui-browser")).trim();
      if (
        (
          await command([
            "docker",
            "inspect",
            "--format",
            "{{.Image}}",
            container,
          ])
        ).trim() !== candidates.ui.imageId
      )
        throw new Error("Running UI differs from the admitted candidate");
    }
    uiPort = await publishedPort("ui-browser");
    await ready(`http://127.0.0.1:${uiPort}/readyz`);
    // Match actual host output to the version supplied to the image build.
    // Both profiles select JSON for bounded incident diagnostics. Host functional
    // tests separately qualify omitted-format text defaults and both formats.
    const startupLines = (
      await compose("logs", "--no-color", "--no-log-prefix", "ui-browser")
    )
      .split("\n")
      .filter((line) => line.includes("process.logging_configured"));
    if (startupLines.length !== 1)
      throw new Error("Missing or duplicate UI startup configuration record");
    const record = JSON.parse(startupLines[0]);
    if (
      record.version !== expectedUIBuild.version ||
      record.log_level !== "info" ||
      record.log_format !== "json" ||
      record.logger_source !== "configured"
    )
      throw new Error(
        "UI startup identity or selected logging settings differ from the image inputs",
      );
    let context, page, collector;
    const ledger = networkObserver(origin, basePath);
    const providerLedger = issuer
      ? networkObserver(new URL(issuer).origin)
      : undefined;
    outcomes = {
      results: () => [
        ...ledger.results(),
        ...(providerLedger?.results() || []),
      ],
    };
    let jsErrors = 0;
    // Docker veth changes notify the host browser and can cancel an in-flight
    // POST with ERR_NETWORK_CHANGED. Finish topology changes before opening
    // Chromium; never mask a failed login with a retry or a fixed sleep.
    const openBrowser = async () => {
      const browserPhase = phase;
      phase = `${mode}-network-readiness`;
      await waitForStableNetwork({ interrupted: () => interrupted });
      phase = browserPhase;
      browser = await launchBrowser();
      context = await browser.newContext();
      collector = await collectBrowserCoverage(context, coverageOutput);
      coverageContexts++;
      coverageBrowser = browser.version();
      page = await context.newPage();
      await collector.ready(page);
      lastPage = page;
      page.setDefaultTimeout(10000);
      context.on("response", (r) => {
        ledger.observe(r.request().method(), r.url(), r.status());
        providerLedger?.observe(r.request().method(), r.url(), r.status());
      });
      context.on("response", async (r) => {
        if (
          r.url().startsWith(origin + basePath + "/auth/callback?") &&
          r.status() >= 400
        ) {
          try {
            const code = (await r.json()).code;
            if (typeof code === "string" && /^[a-z_]{1,64}$/.test(code))
              console.log(`UI callback result: ${code}`);
          } catch {
            /* Never print a raw provider or browser response. */
          }
        }
      });
      context.on("requestfailed", (r) => {
        ledger.failure(r.method(), r.url(), r.failure()?.errorText);
        providerLedger?.failure(r.method(), r.url(), r.failure()?.errorText);
        const category = r.failure()?.errorText;
        if (
          category !== "net::ERR_ABORTED" &&
          /^net::ERR_[A-Z_]{1,64}$/.test(category || "")
        )
          console.error(`Browser transport category: ${category}`);
      });
      page.on("pageerror", () => jsErrors++);
    };
    await openBrowser();
    if (publisherDNSOnly) {
      await collector.finish();
      await context.close();
      await browser.close();
      continue;
    }
    // Qualify actual immutable manual bytes through the packaged Go host,
    // independently of UI basePath and before any browser login.
    const manual = await context.newPage();
    const manualErrors = [];
    manual.on("pageerror", () => manualErrors.push("page"));
    manual.on("console", (message) => {
      if (message.type() === "error") manualErrors.push("console");
    });
    await manual.route("**/*", async (route) => {
      if (new URL(route.request().url()).origin !== origin) {
        manualErrors.push("external-request");
        await route.abort();
      } else await route.continue();
    });
    const manualResponse = await manual.goto(origin + "/docs/");
    expect(manualResponse.status()).toBe(200);
    expect(manualResponse.headers()["content-security-policy"]).toContain(
      "wasm-unsafe-eval",
    );
    await expect(manual).toHaveTitle(/Open Agentic Catalog/);
    await manualLayout(manual, origin + "/docs/");
    const theme = manual.getByRole("combobox", { name: "Select theme" });
    await theme.selectOption("dark");
    await expect(manual.locator("html")).toHaveAttribute("data-theme", "dark");
    await manual.getByRole("button", { name: "Search", exact: true }).click();
    await manual.locator("site-search dialog input").fill("federation");
    await expect(
      manual.locator(".pagefind-ui__result-link").first(),
    ).toBeVisible();
    await manual.keyboard.press("Escape");
    await manual.goto(origin + "/docs/guides/catalog-ui/");
    await expect(manual.getByRole("heading", { level: 1 })).toHaveText(
      "Browse and manage with the Catalog UI",
    );
    for (const label of [
      "Search API operations",
      "Search definitions",
      "Return to originating operation",
      "Reveal originating operation",
      "Last loaded API contract",
      "Download OpenAPI YAML",
    ])
      await expect(manual.locator("main")).toContainText(label);
    // Walk the finished reference guide and its exact full-size illustrations
    // through the actual image. This runs with both root and subpath UI hosts;
    // the bundled manual keeps its independent, fixed /docs/ route.
    for (const [heading, imageName] of [
      ["Browse API documentation", "catalog-api-areas.png"],
      ["Inspect shared definitions", "catalog-api-definitions.png"],
      ["Refresh the connected contract", "catalog-api-reference.png"],
      [
        "Check DNS evidence in the Publisher page",
        "publisher-dns-verification.png",
      ],
    ]) {
      if (imageName === "publisher-dns-verification.png") {
        await manual.goto(origin + "/docs/guides/publishing/");
        for (const label of [
          "Enter the TXT record correctly",
          "Renew or clear DNS evidence",
          "Recover a DNS check",
          "_open-agentic-catalog-challenge.agents",
          "Retry same command",
        ])
          await expect(manual.locator("main")).toContainText(label);
      }
      const section = manual.getByRole("heading", {
        name: heading,
        exact: true,
      });
      await section.scrollIntoViewIfNeeded();
      await expect(section).toBeInViewport();
      const image = manual.locator(`main img[src$="${imageName}"]`);
      await image.scrollIntoViewIfNeeded();
      await expect
        .poll(() =>
          image.evaluate(
            (element) => element.complete && element.naturalWidth > 0,
          ),
        )
        .toBe(true);
      if (imageName === "publisher-dns-verification.png") {
        const viewport = manual.viewportSize();
        await manual.setViewportSize({ width: 390, height: 844 });
        expect(
          await manual
            .locator("html")
            .evaluate((element) => element.scrollWidth),
        ).toBeLessThanOrEqual(390);
        await manual.setViewportSize(viewport);
      }
      const fullSize = image.locator("..");
      const destination = await fullSize.getAttribute("href");
      expect(destination).toBe(
        `/docs/generated/documentation/assets/catalog-ui/${imageName}`,
      );
      const opened = manual.waitForResponse(origin + destination);
      await fullSize.click();
      const imageResponse = await opened;
      expect(imageResponse.status()).toBe(200);
      expect(imageResponse.headers()["content-type"]).toContain("image/png");
      expect(await imageResponse.body()).toEqual(
        await readFile(
          join(root, "documentation/assets/catalog-ui", imageName),
        ),
      );
      await expect(manual).toHaveURL(origin + destination);
      await manual.goto(origin + "/docs/guides/catalog-ui/");
    }
    await expect(
      manual
        .locator("main")
        .getByRole("link", { name: "Connect as a reader", exact: true }),
    ).toHaveAttribute("href", "/docs/guides/reader-quickstart/");
    const cookbook = manual
      .locator("main")
      .getByRole("link", { name: "Search cookbook", exact: true })
      .first();
    await expect(cookbook).toHaveAttribute(
      "href",
      "/docs/guides/search-cookbook/",
    );
    await cookbook.click();
    await expect(manual.getByRole("heading", { level: 1 })).toHaveText(
      "Search cookbook",
    );
    const screenshot = manual.locator('img[src$="catalog-browse.png"]');
    await screenshot.scrollIntoViewIfNeeded();
    await expect(screenshot).toBeVisible();
    expect(
      await screenshot.evaluate(
        (image) => image.complete && image.naturalWidth > 0,
      ),
    ).toBe(true);
    await manual.goto(origin + "/docs/guides/resource-authoring/");
    await expect(
      manual.getByRole("heading", {
        name: "Check access before authoring",
        exact: true,
      }),
    ).toBeVisible();
    const authoringAccess = manual
      .locator("main table")
      .filter({ hasText: "Create a Resource" });
    await expect(authoringAccess).toContainText("resource.create");
    await expect(authoringAccess).toContainText("namespace.read");
    await expect(authoringAccess).toContainText("catalog.write");
    const publishingAccess = manual.locator("main").getByRole("link", {
      name: "review and publication access",
      exact: true,
    });
    await expect(publishingAccess).toHaveAttribute(
      "href",
      "/docs/guides/publishing/#check-access-for-each-publishing-task",
    );
    await publishingAccess.click();
    await expect(
      manual.getByRole("heading", {
        name: "Check access for each publishing task",
        exact: true,
      }),
    ).toBeVisible();
    const publicationAccess = manual
      .locator("main table")
      .filter({ hasText: "Activate, roll back or unpublish discovery" });
    await expect(publicationAccess).toContainText("publication.activate");
    await expect(publicationAccess).toContainText("catalog.publish");
    await manual.goto(
      origin + "/docs/guides/publishing/#choose-the-next-action",
    );
    await expect(
      manual.getByRole("heading", {
        name: "Choose the next action",
        exact: true,
      }),
    ).toBeVisible();
    const recovery = manual
      .locator("main table")
      .filter({ hasText: "The outcome is unknown" });
    await expect(recovery).toContainText("Retry same command");
    await expect(recovery).toContainText("412");
    await expect(recovery).toContainText("acknowledged mutation");
    await manual.goto(origin + "/docs/operations/webhooks/");
    await expect(
      manual.getByRole("heading", { name: "Setup and recovery" }),
    ).toBeVisible();
    const rotationImage = manual.locator('img[src$="webhook-rotation.png"]');
    await rotationImage.scrollIntoViewIfNeeded();
    await expect(rotationImage).toBeVisible();
    await expect
      .poll(() =>
        rotationImage.evaluate(
          (image) => image.complete && image.naturalWidth > 0,
        ),
      )
      .toBe(true);
    await manual.setViewportSize({ width: 390, height: 844 });
    expect(
      await rotationImage.evaluate(
        (image) =>
          image.getBoundingClientRect().width <=
          image.ownerDocument.defaultView.innerWidth,
      ),
    ).toBe(true);
    expect(
      await rotationImage.evaluate(
        (image) =>
          image.ownerDocument.documentElement.scrollWidth <=
          image.ownerDocument.defaultView.innerWidth,
      ),
    ).toBe(true);
    const receiverGuide = manual
      .locator("main")
      .getByRole("link", { name: "verifier outline", exact: true })
      .first();
    await expect(receiverGuide).toHaveAttribute(
      "href",
      "/docs/reference/receiver-example/#connect-and-operate-a-receiver",
    );
    await receiverGuide.click();
    await expect(manual.getByRole("heading", { level: 1 })).toHaveText(
      "Receiving Catalog webhooks",
    );
    await manual.setViewportSize({ width: 1440, height: 900 });
    // Qualify current Operations/reader procedures and exact reviewed illustrations
    // through the real image/manual host, independently of synthetic UI replies.
    for (const guide of [
      {
        area: "guides",
        path: "resource-authoring",
        heading: "Create an Agent from fields",
        image: "guided-listing-details.png",
        text: "The next Listing save uses the corrected revision",
      },
      {
        area: "guides",
        path: "agent-plugins",
        heading: "Create a review-tools Plugin in the browser",
        image: "plugin-field-authoring.png",
        text: "The Catalog supplies neither archive hosting nor a local Plugin upload",
      },
      {
        area: "guides",
        path: "publishing",
        heading: "Publish an Agent in the browser",
        image: "guided-review-approved.png",
        text: "The independent Reviewer approves the corrected submission",
      },
      {
        path: "observability",
        heading: "Investigate one retained Job",
        image: "jobs-filtered.png",
        text: "There is no raw-result JSON fallback.",
      },
      {
        path: "hybrid-search",
        heading: "Check a rebuild outcome",
        image: "search-indexing-confirmation.png",
        text: "Accounting includes reused vectors and failed inputs",
      },
      {
        path: "federation",
        heading: "Troubleshoot source tests, commands and retained evidence",
        image: "source-confirmation.png",
        text: "It cannot recall a peer request already sent.",
      },
      {
        path: "federation",
        heading: "Read a private ANS assessment",
        image: "ans-assessment.png",
        text: "Saving a download sends no new evidence request",
      },
      {
        area: "guides",
        path: "understand-results",
        heading: "Read a local result",
        image: "catalog-record.png",
        text: "This preserves pending text, other filters and exact spelling",
      },
      {
        area: "guides",
        path: "search-cookbook",
        heading: "Apply text, refresh and compare results",
        image: "catalog-preview.png",
        text: "The pending-text notice explains when results still use previous text",
      },
      {
        area: "guides",
        path: "catalog-ui",
        heading: "Save resources and searches",
        image: "saved-search.png",
        text: "These controls make no Search, Explore or suggestion request",
      },
    ]) {
      const [delivered] = await Promise.all([
        manual.waitForResponse(
          (response) =>
            response.url() ===
            `${origin}/docs/generated/documentation/assets/catalog-ui/${guide.image}`,
        ),
        manual.goto(
          `${origin}/docs/${"area" in guide ? guide.area : "operations"}/${guide.path}/`,
        ),
      ]);
      await expect(
        manual.getByRole("heading", { name: guide.heading, exact: true }),
      ).toBeVisible();
      await expect(manual.locator("main")).toContainText(guide.text);
      const illustration = manual.locator(`img[src$="${guide.image}"]`);
      await illustration.scrollIntoViewIfNeeded();
      await expect(illustration).toBeVisible();
      await expect
        .poll(() =>
          illustration.evaluate(
            (image) => image.complete && image.naturalWidth > 0,
          ),
        )
        .toBe(true);
      const original = await readFile(
        join(root, "documentation", "assets", "catalog-ui", guide.image),
      );
      // Use the actual browser delivery and its existing exact fixture SPKI pin.
      // APIRequestContext has independent TLS trust and must not bypass it.
      expect(delivered.status()).toBe(200);
      expect(await delivered.body()).toEqual(original);
      await manual.setViewportSize({ width: 390, height: 844 });
      expect(
        await illustration.evaluate(
          (image) =>
            image.getBoundingClientRect().width <=
              image.ownerDocument.defaultView.innerWidth &&
            image.ownerDocument.documentElement.scrollWidth <=
              image.ownerDocument.defaultView.innerWidth,
        ),
      ).toBe(true);
      await manual.setViewportSize({ width: 1440, height: 900 });
    }
    await manual.goto(origin + "/docs/architecture/overview/");
    await expect(manual.locator("svg.architecture-svg")).toBeVisible();
    await manual.goto(origin + "/docs/api/operations/listaccessgroups/");
    await expect(manual.getByRole("heading", { level: 1 })).toHaveText(
      "List groups",
    );
    await manual.goto(origin + "/docs/contributing/documentation/");
    await expect(manual.getByRole("heading", { level: 1 })).toHaveText(
      "Build and maintain this documentation",
    );
    await expect(
      manual.getByRole("heading", {
        name: "How the manual is built",
        exact: true,
      }),
    ).toBeVisible();
    const developmentCommands = manual
      .locator(".expressive-code")
      .filter({ hasText: "UI_DOCUMENTATION_DIRECTORY=" });
    await expect(developmentCommands.locator(".ec-line")).toHaveText([
      "make docs-build DOCS_BASE=/docs/",
      'make ui-dev UI_DOCUMENTATION_DIRECTORY="$PWD/documentation/dist"',
    ]);
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    await developmentCommands
      .getByRole("button", { name: "Copy to clipboard", exact: true })
      .click();
    await expect
      .poll(() => manual.evaluate(() => navigator.clipboard.readText()))
      .toBe(
        'make docs-build DOCS_BASE=/docs/\nmake ui-dev UI_DOCUMENTATION_DIRECTORY="$PWD/documentation/dist"',
      );
    await manual.goto(origin + "/docs/guides/local-development/");
    await manual
      .getByRole("tab", { name: "Docker Compose", exact: true })
      .first()
      .click();
    const panel = manual
      .getByRole("tabpanel")
      .filter({ visible: true })
      .first();
    await expect(panel).toContainText("make compose-up LOCAL_UI=on");
    await panel
      .getByRole("button", { name: "Copy to clipboard", exact: true })
      .first()
      .click();
    await expect
      .poll(() => manual.evaluate(() => navigator.clipboard.readText()))
      .toBe("make compose-up LOCAL_UI=on");
    const yaml = await manual.evaluate(async () => {
      const response = await fetch(
        "/docs/generated/api/openapi/control-v1.yaml",
      );
      if (!response.ok) throw new Error("Static download failed");
      return response.text();
    });
    expect(createHash("sha256").update(yaml).digest("hex")).toBe(
      createHash("sha256")
        .update(await readFile(join(root, "api/openapi/control-v1.yaml")))
        .digest("hex"),
    );
    await manual
      .getByRole("combobox", { name: "Select theme" })
      .selectOption("light");
    await expect(manual.locator("html")).toHaveAttribute("data-theme", "light");
    await manual.setViewportSize({ width: 390, height: 844 });
    await manual.locator('button[popovertarget="starlight__sidebar"]').click();
    await expect(manual.locator("#starlight__sidebar")).toBeVisible();
    await manual.locator('button[popovertarget="starlight__sidebar"]').click();
    const missingManual = await new Promise((done, fail) => {
      const req = get(
        origin + "/docs/nonexistent-page/",
        { ca: cert, timeout: 5000 },
        (res) => {
          res.resume();
          done(res.statusCode);
        },
      );
      req.on("error", fail);
      req.on("timeout", () => req.destroy(new Error("Manual request timeout")));
    });
    expect(missingManual).toBe(404);
    expect(manualErrors).toEqual([]);
    await manual.close();
    const signIn = async (username = "browser-human") => {
      const loginPhase = phase;
      phase = loginPhase + "-start";
      await (
        mode === "oidc"
          ? page.getByRole("button", {
              name: "Sign in",
              exact: true,
            })
          : page.locator('[data-login-alias="local-reader"]')
      ).click();
      if (mode === "oidc") {
        phase = loginPhase + "-provider-form";
        // Provider SSO may redirect without a form after local UI sign-out.
        await expect(
          page
            .getByLabel("Account", { exact: true })
            .or(page.getByLabel("Username or email")),
        ).toBeVisible();
        if (await page.getByLabel("Username or email").isVisible()) {
          phase = loginPhase + "-login-cookie";
          const pending = (
            await context.cookies(origin + basePath + "/")
          ).filter((c) => c.name.startsWith("catalog-ui-packaged-login-"));
          expect(pending.length).toBe(1);
          expect(pending[0].value.length).toBeLessThanOrEqual(3600);
          expect(pending[0].secure && pending[0].httpOnly).toBe(true);
          phase = loginPhase + "-credentials";
          await page.getByLabel("Username or email").fill(username);
          await page.getByLabel("Password", { exact: true }).fill(password);
          await page
            .getByRole("button", { name: "Sign In", exact: true })
            .click();
        }
      }
      phase = loginPhase;
    };
    phase += "-public-help";
    await page.goto(origin + basePath + "/about");
    await expect(
      page.getByRole("heading", { name: "About", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Connected builds", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Documentation", exact: true }),
    ).toHaveAttribute("href", "/docs/");
    const legal = await page.evaluate(
      async (prefix) =>
        Promise.all(
          ["LICENSE.txt", "third-party-notices.txt"].map(async (name) => {
            const response = await fetch(`${prefix}/legal/${name}`);
            if (
              response.status !== 200 ||
              !response.headers.get("content-type")?.startsWith("text/plain")
            )
              throw new Error("Packaged legal text unavailable");
            return response.text();
          }),
        ),
      basePath,
    );
    expect(legal[0]).toBe(await readFile(join(root, "LICENSE"), "utf8"));
    expect(legal[1]).toContain(
      await readFile(
        join(root, "catalog-ui/node_modules/react/LICENSE"),
        "utf8",
      ),
    );
    await page.goto(origin + basePath + "/getting-started");
    await expect(
      page.getByRole("heading", { name: "Getting started", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Open Catalog", exact: true }),
    ).toHaveCount(0);
    await page.goto(origin + basePath + "/troubleshooting");
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Preview diagnostic details",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: /^Troubleshooting in the manual/ }),
    ).toHaveAttribute("href", "/docs/guides/catalog-ui/#troubleshooting");
    phase += "-login";
    await page.goto(origin + basePath + "/catalog");
    await signIn();
    phase += "-catalog";
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    const help = page.getByRole("group", { name: "Help" });
    const documentationLink = help.getByRole("link", {
      name: "Documentation",
      exact: true,
    });
    await expect(
      page.getByRole("link", { name: "Documentation", exact: true }),
    ).toHaveCount(1);
    await expect(documentationLink).toHaveAttribute("href", "/docs/");
    await expect(documentationLink).toHaveAttribute(
      "rel",
      "noopener noreferrer",
    );
    await expect(documentationLink).toHaveAccessibleDescription(
      "Opens in a new tab",
    );
    await documentationLink.focus();
    await expect(documentationLink).toBeFocused();
    const [manualTab] = await Promise.all([
      context.waitForEvent("page"),
      documentationLink.press("Enter"),
    ]);
    await manualTab.waitForURL(origin + "/docs/");
    await expect(manualTab).toHaveTitle(/Open Agentic Catalog/);
    expect(await manualTab.evaluate(() => globalThis.opener === null)).toBe(
      true,
    );
    await manualTab.close();
    await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
    // Both authentication modes expose only release-linked current labels.
    const presented = page.locator("article.resource-card").first();
    await expect(
      presented.getByText("Local Catalog", { exact: true }),
    ).toBeVisible();
    await expect(
      presented.locator(".catalog-presentation dt", {
        hasText: "Publisher",
      }),
    ).toBeVisible();
    await expect(
      presented.locator(".catalog-presentation dt", {
        hasText: "Namespace",
      }),
    ).toBeVisible();
    await expect(
      presented.locator(".catalog-presentation dd").first(),
    ).not.toBeEmpty();
    // Qualify actual namespace filtering and memory-only return through both hosts.
    // Observe both promises immediately so an action failure cannot leave an
    // unhandled request/response waiter when cleanup closes the browser.
    const [namespaceSearch] = await Promise.all([
      page.waitForRequest(
        (request) =>
          request.method() === "POST" &&
          new URL(request.url()).pathname ===
            basePath + "/_catalog/api/v1/catalog/search",
      ),
      presented.locator(".catalog-presentation button").click(),
    ]);
    const submitted = namespaceSearch.postDataJSON();
    expect(submitted.origins).toEqual(["local"]);
    expect(submitted.namespaceId).toMatch(/^[0-9a-f-]{36}$/i);
    await expect(
      page.getByRole("button", {
        name: /^Remove local namespace /,
      }),
    ).toBeVisible();
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    await page.getByRole("button", { name: /^Filters/ }).click();
    const [namespaceChoices] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === "GET" &&
          new URL(response.url()).pathname ===
            basePath + "/_catalog/api/v1/namespaces",
      ),
      page
        .getByRole("button", { name: "Load namespaces", exact: true })
        .click(),
    ]);
    expect(namespaceChoices.status()).toBe(200);
    await expect(
      page.getByRole("combobox", { name: "Local namespace", exact: true }),
    ).toHaveValue(submitted.namespaceId);
    for (const name of ["Indexed external", "Live external"])
      await expect(
        page.getByRole("checkbox", { name, exact: true }),
      ).toBeDisabled();
    await page.keyboard.press("Escape");
    // Observe native panel dismissal and restored focus before the next pointer action.
    await expect(
      page.getByRole("region", { name: "Filters", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Filters", exact: true }),
    ).toBeFocused();
    await page
      .locator("article.resource-card")
      .first()
      .locator("button.title-button")
      .click();
    const preview = page.getByRole("dialog");
    await expect(preview.getByRole("heading", { level: 3 })).toHaveText([
      "Overview",
      "Listing categories",
      "Validation and assessments",
      "Use this resource",
      "Technical details and evidence",
    ]);
    const assessment = preview.getByRole("region", {
      name: "Resource assessment",
    });
    await expect(assessment).toContainText("Completed");
    await expect(
      assessment.getByText("Not assessed", { exact: true }),
    ).toHaveCount(2);
    const evidence = preview.locator("details").filter({
      has: page.locator("summary", { hasText: "Representation evidence" }),
    });
    await expect(evidence).not.toHaveAttribute("open");
    await evidence.locator("summary").click();
    await expect(
      evidence.getByText("Media type", { exact: true }),
    ).toBeVisible();
    await expect(
      evidence.getByText("Digest scope", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("link", { name: "Open full record", exact: true })
      .click();
    await expect(
      page.locator(".full-record").getByRole("heading", { level: 2 }),
    ).toHaveText([
      "Overview",
      "Validation and assessments",
      "Use this resource",
      "Technical details and evidence",
    ]);
    await page.getByRole("link", { name: /Back to Catalog/ }).click();
    await expect(
      page.getByRole("button", {
        name: /^Remove local namespace /,
      }),
    ).toBeVisible();
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    await page
      .getByRole("button", { name: /^Remove local namespace / })
      .click();
    await expect(
      page.getByRole("button", {
        name: /^Remove local namespace /,
      }),
    ).toHaveCount(0);
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    await expect(
      page.getByRole("button", {
        name: "Remove origin Local Catalog",
        exact: true,
      }),
    ).toBeEnabled();
    const identities = await page.evaluate(
      async (prefix) =>
        Promise.all(
          ["/_build", "/_catalog/version"].map(async (path) => {
            const reply = await fetch(prefix + path, {
              headers: { "X-Catalog-Request": "1" },
              credentials: "same-origin",
              cache: "no-store",
            });
            if (reply.status !== 200)
              throw new Error("Packaged build identity unavailable");
            return reply.json();
          }),
        ),
      basePath,
    );
    expect(identities[0]).toEqual(expectedUIBuild);
    expect(identities[1]).toMatchObject({
      service: "open-agentic-catalog",
      ...expectedBuild,
    });
    phase += "-overview";
    await page.goto(origin + basePath + "/overview");
    await expect(
      page.getByRole("heading", { name: "Overview", exact: true }),
    ).toBeVisible();
    const overview = await page.evaluate(async (prefix) => {
      const response = await fetch(
        prefix + "/_catalog/api/v1/overview/resources",
        {
          headers: { "X-Catalog-Request": "1" },
          credentials: "same-origin",
          cache: "no-store",
        },
      );
      if (!response.ok || response.headers.get("Cache-Control") !== "no-store")
        throw new Error("Private resource summary unavailable");
      return response.json();
    }, basePath);
    const inventory = page.getByRole("region", {
      name: "Local resources",
      exact: true,
    });
    expect(overview.total.limited).toBe(false);
    expect(overview.total.value).toBeGreaterThan(0);
    await expect(inventory.locator(".overview-number")).toHaveText(
      overview.total.value.toLocaleString("en-US"),
    );
    const inventoryRows = inventory.locator(".overview-kind dd");
    await expect(inventoryRows).toHaveCount(4);
    for (let index = 0; index < 4; index++)
      await expect(inventoryRows.nth(index)).toHaveText(
        overview.kinds[index].count.toLocaleString("en-US"),
      );
    await expect(inventory.getByRole("table")).toHaveCount(0);
    const reviewMetadata = await page.evaluate(
      async (endpoint) => {
        const response = await fetch(endpoint, {
          headers: { "X-Catalog-Request": "1" },
          credentials: "same-origin",
          cache: "no-store",
        });
        if (
          !response.ok ||
          response.headers.get("Cache-Control") !== "no-store"
        )
          throw new Error("Private review summary unavailable");
        return response.json();
      },
      origin + basePath + "/_catalog/api/v1/overview/reviews",
    );
    const reviewPanel = page.getByRole("region", {
      name: "Reviews",
      exact: true,
    });
    await expect(reviewPanel.locator("dd").nth(0)).toHaveText(
      reviewMetadata.pending.value.toLocaleString("en-US"),
    );
    await expect(reviewPanel.locator("dd").nth(1)).toHaveText(
      reviewMetadata.approvedUnreleased.value.toLocaleString("en-US"),
    );
    await reviewPanel.getByText("Daily breakdown", { exact: true }).click();
    await expect(
      reviewPanel
        .getByRole("table", {
          name: "Daily submissions and decision outcomes in UTC",
        })
        .getByRole("row"),
    ).toHaveCount(31);
    await expect(
      inventory.getByRole("link", { name: "Agent", exact: true }),
    ).toHaveAttribute("href", basePath + "/resources?kind=agent");
    await page.goto(origin + basePath + "/about");
    await expect(
      page.getByRole("heading", { name: "Connected builds", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(expectedBuild.version, { exact: true }),
    ).toHaveCount(2);
    await page.goto(origin + basePath + "/troubleshooting");
    await page
      .getByRole("button", { name: "Preview diagnostic details", exact: true })
      .click();
    await expect(
      page.getByRole("button", {
        name: "Copy diagnostic details",
        exact: true,
      }),
    ).toBeVisible();
    const diagnosticPreview = page.getByRole("group", {
      name: "JSON document",
      exact: true,
    });
    const safeDiagnostic = JSON.parse(await diagnosticPreview.innerText());
    expect(safeDiagnostic).toEqual({
      formatVersion: 1,
      apiBuild: expectedBuild,
      uiBuild: expectedUIBuild,
    });
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin,
    });
    await page
      .getByRole("button", { name: "Copy diagnostic details", exact: true })
      .click();
    await expect(page.getByText("Copied.", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      JSON.stringify(safeDiagnostic, null, 2),
    );
    await page
      .getByRole("button", { name: "Close diagnostic preview", exact: true })
      .click();
    await page
      .getByRole("main")
      .getByRole("link", { name: "Service information", exact: true })
      .click();
    for (const name of ["Catalog API", "UI host"])
      await expect(
        page
          .locator("section")
          .filter({ has: page.getByRole("heading", { name, exact: true }) })
          .locator("dt")
          .filter({ hasText: /^Version$/ })
          .locator("+ dd"),
      ).toHaveText(expectedBuild.version);
    await page.goto(origin + basePath + "/catalog");
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    const session = (await context.cookies(origin + basePath + "/")).find(
      (c) => c.name === "catalog-ui-packaged",
    );
    expect(
      !!session &&
        session.httpOnly &&
        session.secure &&
        session.sameSite === "Lax" &&
        session.path === basePath + "/",
    ).toBe(true);
    expect(session.value.length).toBeLessThanOrEqual(3600);
    console.log(`Session cookie: ${session.value.length} bytes (${phase})`);
    await page.goto(origin + basePath + "/access");
    await expect(
      page
        .getByRole("definition")
        .filter({ hasText: mode === "oidc" ? subject : "local-reader" }),
    ).toBeVisible();
    let savedNamespace, savedNamespaceURL, savedNamespaceRead;
    if (mode === "oidc") {
      // Persist work through the actual human-authorized UI and PostgreSQL,
      // then return to that exact record after losing the local UI session.
      phase = `oidc-${basePath ? "subpath" : "root"}-saved-work`;
      const slug = basePath ? "browser-subpath" : "browser-root";
      await page.goto(origin + basePath + "/namespaces/new");
      await page.getByLabel("Display name", { exact: true }).fill(slug);
      await page.getByLabel("Slug", { exact: true }).fill(slug);
      await page
        .getByRole("button", { name: "Create namespace", exact: true })
        .click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click();
      await expect(page.getByRole("status")).toContainText(
        `Created namespace ${slug}`,
      );
      await page
        .getByRole("link", { name: "Back to namespaces", exact: true })
        .click();
      await page.getByRole("link", { name: slug, exact: true }).click();
      savedNamespace = slug;
      savedNamespaceURL = page.url();
      const namespaceId = new URL(savedNamespaceURL).pathname.split("/").pop();
      savedNamespaceRead = {
        route: basePath + "/_catalog/api/v1/namespaces/" + namespaceId,
        id: namespaceId,
        slug,
        tenant,
      };
      await page.reload();
      await expect(
        page.getByLabel("Display name", { exact: true }),
      ).toHaveValue(slug);
      expect(
        await page.evaluate(namespaceReadControl, savedNamespaceRead),
      ).toEqual({ status: 200, retained: true, forbidden: false });
    }
    if (mode === "oidc" && basePath) {
      phase = "oidc-cookie-loss";
      const providerCookies = (await context.cookies()).filter(
        (cookie) => cookie.name !== "catalog-ui-packaged",
      );
      await context.clearCookies({
        name: "catalog-ui-packaged",
        domain: session.domain,
        path: session.path,
      });
      expect(await context.cookies()).toEqual(providerCookies);
      const route =
        basePath +
        "/_catalog/api/v1/namespaces/" +
        new URL(savedNamespaceURL).pathname.split("/").pop();
      ledger.expectFailure("GET", route.slice(basePath.length), 401);
      const [denied] = await Promise.all([
        page.waitForResponse((r) => new URL(r.url()).pathname === route),
        page
          .getByRole("button", { name: "Reload namespace", exact: true })
          .click(),
      ]);
      expect(denied.status()).toBe(401);
      await expect(
        page.getByLabel("Display name", { exact: true }),
      ).toHaveCount(0);
      await expect(page.getByLabel("Account", { exact: true })).toHaveCount(0);
      await signIn();
      await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
      await page.goto(savedNamespaceURL);
      await expect(
        page.getByLabel("Display name", { exact: true }),
      ).toHaveValue(savedNamespace);
    }
    await page.goto(origin + basePath + "/troubleshooting");
    await page
      .getByRole("button", { name: "Preview diagnostic details", exact: true })
      .click();
    await expect(
      page.getByRole("button", {
        name: "Copy diagnostic details",
        exact: true,
      }),
    ).toBeVisible();
    await page.getByLabel("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    expect(
      (await context.cookies(origin + basePath + "/")).some(
        (c) => c.name === "catalog-ui-packaged",
      ),
    ).toBe(false);
    await expect(
      page.getByRole("heading", { name: "Troubleshooting", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Copy diagnostic details",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("group", { name: "JSON document", exact: true }),
    ).toHaveCount(0);
    await page.goto(origin + basePath + "/catalog");
    await signIn();
    await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
    if (savedNamespace) {
      await page.goto(savedNamespaceURL);
      await expect(
        page.getByLabel("Display name", { exact: true }),
      ).toHaveValue(savedNamespace);
    }
    await page.goto(origin + basePath + "/catalog");
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    expect(await page.evaluate(() => Object.keys(sessionStorage))).toEqual([]);
    if (mode === "compose" && basePath) {
      // Initial root/subpath checks finish before mutations alter the seed.
      await packagedJourneys({
        page,
        captureDocumentation: async (name, target) => {
          const directory = join(root, "output/playwright", project);
          await mkdir(directory, { recursive: true });
          const viewport = page.viewportSize();
          try {
            await page.setViewportSize({ width: 1440, height: 3000 });
            await target.evaluate((element) =>
              element.ownerDocument.defaultView.scrollTo(
                0,
                element.ownerDocument.defaultView.scrollY +
                  element.getBoundingClientRect().top -
                  100,
              ),
            );
            await target.screenshot({ path: join(directory, name + ".png") });
          } finally {
            await page.setViewportSize(viewport);
          }
        },
        origin: origin + basePath,
        settingsFault,
        inspectAssignmentOutcome: (response) =>
          inspectAuthorizationCommandOutcome({
            title: "Revoke access assignment",
            response,
          }),
        fixtureRequest,
        // Match the Catalog screen's first-page size; continuation tests override it explicitly.
        remoteSearch: async ({
          text,
          limit = 24,
          cursor = "",
          license = "",
          categories = [],
          origins = [],
        }) =>
          JSON.parse(
            await compose(
              "exec",
              "-T",
              "-e",
              "CATALOG_REMOTE_TOKEN=local-reader",
              "api",
              "/usr/local/bin/catalogctl",
              "remote",
              "search",
              "--base-url",
              "http://127.0.0.1:8080",
              "--text",
              text,
              "--limit",
              String(limit),
              "--cursor",
              cursor,
              ...(license ? ["--license", license] : []),
              ...categories.flatMap((value) => ["--category", value]),
              ...origins.flatMap((value) => ["--origin", value]),
            ),
          ).data,
        phase: (value) => {
          phase = value;
          console.log(`Checking ${phase}`);
        },
        rotationEvidence: async (response, endpoint, elapsedMs, principal) => {
          let problem;
          if (response.status() >= 400) {
            try {
              problem = await response.json();
            } catch {
              /* Malformed replies still retain status. */
            }
          }
          const evidence = rotationResponse({
            status: response.status(),
            headers: response.headers(),
            problem,
            elapsedMs,
          });
          console.log(`Owned rotation response: ${JSON.stringify(evidence)}`);
          const parts = await rotationParts({
            state: async () => {
              return JSON.parse(
                await compose(
                  "exec",
                  "-T",
                  "-e",
                  "PGOPTIONS=-c statement_timeout=5000",
                  "postgres",
                  "psql",
                  "-X",
                  "-A",
                  "-t",
                  "-v",
                  "ON_ERROR_STOP=1",
                  "-U",
                  "catalog",
                  "-d",
                  "catalog",
                  "-c",
                  rotationStateSQL({
                    tenant,
                    endpoint,
                    ...principal,
                    key: response.request().headers()["idempotency-key"],
                  }),
                ),
              );
            },
            containers: async () => {
              const containers = JSON.parse(
                await compose("ps", "--all", "--format", "json").then(
                  (value) => "[" + value.trim().split("\n").join(",") + "]",
                ),
              )
                .filter((item) =>
                  ["api", "worker", "postgres", "ui-browser"].includes(
                    item.Service,
                  ),
                )
                .map((item) => ({
                  service: item.Service,
                  id: item.ID,
                  image: item.Image,
                  state: item.State,
                  health: item.Health,
                }));
              const identities = [];
              for (const item of containers) {
                const identity = JSON.parse(
                  await command(
                    [
                      "docker",
                      "inspect",
                      "--format",
                      '{"imageId":{{json .Image}},"restarts":{{.RestartCount}}}',
                      item.id,
                    ],
                    10,
                  ),
                );
                identities.push({
                  ...item,
                  imageId: identity.imageId,
                  restarts: identity.restarts,
                });
              }
              return identities;
            },
            diagnostics: async () => {
              return response.status() >= 400
                ? rotationDiagnostics(
                    await compose(
                      "logs",
                      "--tail",
                      "150",
                      "--no-color",
                      "--no-log-prefix",
                      "api",
                      "ui-browser",
                    ),
                    evidence.requestId,
                  )
                : [];
            },
            databaseDiagnostics: async () => {
              if (response.status() < 400) return {};
              // Retain a fixed classification only. PostgreSQL log statements
              // may contain encrypted secrets or other private parameter data.
              const logs = await compose(
                "logs",
                "--tail",
                "80",
                "--no-color",
                "--no-log-prefix",
                "postgres",
              );
              return rotationDatabaseDiagnostics(logs);
            },
          });
          const snapshot = { response: evidence, ...parts };
          const saved = await collectRotationEvidence(
            async () => {
              const evidenceDirectory = join(
                root,
                "output/playwright",
                project,
              );
              await mkdir(evidenceDirectory, { recursive: true });
              await writeFile(
                join(evidenceDirectory, "rotation.json"),
                JSON.stringify(snapshot, null, 2),
              );
              console.log(
                `Owned rotation state: ${JSON.stringify(parts.state)}; evidence: ${evidenceDirectory}/rotation.json`,
              );
            },
            () =>
              console.error(
                "Owned rotation evidence file unavailable; original browser assertion and cleanup retained",
              ),
          );
          if (response.status() === 200) {
            expect(saved).toBe(true);
            expect(parts.state.status).toBe("available");
            expect(parts.containers.status).toBe("available");
            expect(parts.diagnostics.status).toBe("available");
            expect(parts.containers.value).toHaveLength(4);
            expect(parts.diagnostics.value).toEqual([]);
            for (const container of parts.containers.value) {
              expect(container.imageId).toMatch(/^sha256:[0-9a-f]{64}$/);
              expect(container.restarts).toBe(0);
              expect(container.state).toBe("running");
            }
            const observedState = parts.state.value;
            expect(observedState.endpoint).toEqual({
              revision: 2,
              secretVersion: 2,
              status: "active",
              databaseClockBeforeUpdate: expect.any(Boolean),
            });
            expect(observedState.command).toEqual({
              reserved: true,
              completed: true,
              released: false,
              responseStatus: 200,
            });
            expect(observedState.rotationAuditCount).toBe(1);
          }
        },
        network: ledger,
        api: async (running) => {
          await compose(running ? "start" : "stop", "api");
          if (running) {
            apiPort = await publishedPort("api");
            await ready(`http://127.0.0.1:${apiPort}/readyz`);
          }
          await waitForStableNetwork({ interrupted: () => interrupted });
        },
        ui: async () => {
          await compose("restart", "ui-browser");
          uiPort = await publishedPort("ui-browser");
          await ready(`http://127.0.0.1:${uiPort}/readyz`);
          await waitForStableNetwork({ interrupted: () => interrupted });
        },
        worker: async (running) => {
          await compose(running ? "start" : "stop", "worker");
          await waitForStableNetwork({ interrupted: () => interrupted });
        },
      });
    }
    if (mode === "oidc") {
      phase = `oidc-${basePath ? "subpath" : "root"}-missing-membership`;
      await context.clearCookies();
      await page.goto(origin + basePath + "/catalog");
      const [callback] = await Promise.all([
        page.waitForResponse(
          (response) =>
            new URL(response.url()).pathname === basePath + "/auth/callback",
        ),
        signIn("unregistered-human"),
      ]);
      expect(callback.status()).toBe(303);
      expect(callback.headers().location).toBe(
        basePath + "/?sign_in_error=catalog_login_denied",
      );
      await expect(page.getByRole("alert")).toHaveText(
        "The Catalog did not accept this account. Ask an administrator to check your access.",
      );
      await expect(
        page.getByRole("button", { name: "Sign in", exact: true }),
      ).toBeEnabled();
      await expect(page.getByLabel("Account", { exact: true })).toHaveCount(0);
      expect(
        (await context.cookies(origin + basePath + "/")).some(
          (cookie) => cookie.name === "catalog-ui-packaged",
        ),
      ).toBe(false);
      // Test the deployed scope ceiling once, not a role x scope x path matrix.
      if (basePath) {
        phase = "oidc-scope-ceiling";
        await collector.finish();
        await browser.close();
        config.auth.enforceScopes = true;
        await rm(join(directory, "api.json"));
        await json("api.json", config);
        await compose("up", "-d", "--no-deps", "--force-recreate", "api");
        apiPort = await publishedPort("api");
        await ready(`http://127.0.0.1:${apiPort}/readyz`);
        await openBrowser();
        await page.goto(origin + basePath + "/catalog");
        await signIn();
        await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
        await expect(page.locator("article.resource-card")).toHaveCount(0);
        await expect(
          page
            .getByRole("navigation", { name: "Workspace", exact: true })
            .getByRole("link", { name: "Catalog", exact: true }),
        ).toHaveCount(0);
        await page.goto(origin + basePath + "/access");
        await expect(
          page.getByRole("definition").filter({ hasText: subject }),
        ).toBeVisible();
        await expect(
          page.getByText("Additional scope checks are disabled", {
            exact: true,
          }),
        ).toHaveCount(0);
        ledger.expectFailure(
          "GET",
          savedNamespaceRead.route.slice(basePath.length),
          403,
        );
        expect(
          await page.evaluate(namespaceReadControl, savedNamespaceRead),
        ).toEqual({ status: 403, retained: false, forbidden: true });
        phase = "oidc-ratings-provisioning";
        const originalPrincipal = await page.evaluate(async (prefix) => {
          const response = await fetch(prefix + "/_catalog/api/v1/me", {
            headers: { "X-Catalog-Request": "1" },
          });
          if (response.status !== 200)
            throw new Error("Original OIDC principal unavailable");
          const body = await response.json();
          return {
            subject: body.subject,
            membershipId: body.membershipId,
            realmId: body.realmId,
            administrator: body.permissions.includes("access.manage"),
          };
        }, basePath);
        expect(originalPrincipal.subject).toBe(subject);
        expect(originalPrincipal.administrator).toBe(true);
        await page.goto(origin + basePath + "/members");
        const originalContext = page.getByRole("region", {
          name: "Current administration context",
        });
        await expect(originalContext).toContainText(originalPrincipal.realmId);
        await expect(originalContext).toContainText(tenant);
        await collector.finish();
        await browser.close();
        const ratingsIssuer = issuer.replace(/\/catalog$/, "/catalog-ratings");
        await ready(ratingsIssuer + "/.well-known/openid-configuration", cert);
        const service = await ratingsService({
          issuer: ratingsIssuer,
          ca: cert,
          secret: ratingsSecret,
          apiOrigin: () => `http://127.0.0.1:${apiPort}`,
        });
        // The owner command bootstraps administrators; ordinary readers are
        // admitted by that administrator through the existing role-grant API.
        await compose(
          "run",
          "--rm",
          "--no-deps",
          "--entrypoint",
          "/usr/local/bin/catalogctl",
          "migrate",
          "identity",
          "provision",
          "--config",
          "/config/config.json",
          "--tenant",
          tenant,
          "--tenant-name",
          "Tenant " + tenant,
          "--issuer",
          ratingsIssuer,
          "--tenancy-mode",
          "dedicated",
          "--subject",
          service.subject,
          "--actor-type",
          "service",
          "--roles",
          "service_client,tenant_admin,reader",
          "--operator",
          "owned-ratings-fixture",
          "--reason",
          "Bootstrap the scope-enforced ratings fixture administrator",
        );
        config.auth.issuer = ratingsIssuer;
        await rm(join(directory, "api.json"));
        await json("api.json", config);
        await compose("up", "-d", "--no-deps", "--force-recreate", "api");
        apiPort = await publishedPort("api");
        await ready(`http://127.0.0.1:${apiPort}/readyz`);
        const readerGrant = await service.request(
          "POST",
          "/api/v1/role-assignments",
          {
            subject: ratingsSubject,
            actorType: "human",
            role: "reader",
            reason: "Admit the ordinary human ratings reader",
          },
        );
        expect(readerGrant.status).toBe(201);
        expect(readerGrant.data).toMatchObject({
          tenantId: tenant,
          subject: ratingsSubject,
          actorType: "human",
          role: "reader",
          state: "active",
        });
        // Ordinary Reader grants remain exact. Add the global library permission explicitly.
        const definitions = await service.request(
          "GET",
          "/api/v1/access/roles?limit=100",
        );
        expect(definitions.status).toBe(200);
        expect(definitions.data.nextCursor).toBeUndefined();
        const library = definitions.data.items.find(
          (definition) => definition.key === "library_member",
        );
        expect(library).toMatchObject({
          builtin: true,
          namespaceCapable: false,
          permissions: ["library.own"],
        });
        expect(readerGrant.data.membershipId).toMatch(/^[0-9a-f-]{36}$/);
        const libraryGrant = await service.request(
          "POST",
          "/api/v1/access/assignments",
          {
            roleDefinitionId: library.id,
            membershipId: readerGrant.data.membershipId,
            reason: "Admit the ordinary human's private library explicitly",
          },
        );
        expect(libraryGrant.status).toBe(201);
        expect(libraryGrant.data).toMatchObject({
          roleDefinitionId: library.id,
          membershipId: readerGrant.data.membershipId,
          state: "active",
        });
        const ratingsUI = {
          ...uiConfig,
          issuer: ratingsIssuer,
          scopes: [
            "catalog.read",
            "ratings.write",
            "personal.write",
            "tenant.admin",
          ],
        };
        await rm(join(directory, "ui.json"));
        await json("ui.json", ratingsUI);
        await compose(
          "up",
          "-d",
          "--no-deps",
          "--force-recreate",
          "ui-browser",
        );
        uiPort = await publishedPort("ui-browser");
        await ready(`http://127.0.0.1:${uiPort}/readyz`);
        await openBrowser();
        await page.goto(origin + basePath + "/catalog");
        await signIn();
        phase = "oidc-same-subject-other-realm";
        const distinctPrincipal = await page.evaluate(async (prefix) => {
          const response = await fetch(prefix + "/_catalog/api/v1/me", {
            headers: { "X-Catalog-Request": "1" },
          });
          if (response.status !== 200)
            throw new Error("Second OIDC principal unavailable");
          const body = await response.json();
          return {
            subject: body.subject,
            membershipId: body.membershipId,
            realmId: body.realmId,
            administrator: body.permissions.includes("access.manage"),
            scopes: body.scopes,
          };
        }, basePath);
        expect(distinctPrincipal.subject).toBe(originalPrincipal.subject);
        expect(distinctPrincipal.realmId).not.toBe(originalPrincipal.realmId);
        expect(distinctPrincipal.membershipId).not.toBe(
          originalPrincipal.membershipId,
        );
        expect(distinctPrincipal.administrator).toBe(false);
        expect(distinctPrincipal.scopes).toContain("tenant.admin");
        await page.goto(origin + basePath + "/ratings");
        const distinctContext = page.getByRole("region", {
          name: "Current administration context",
        });
        await expect(distinctContext).toContainText(distinctPrincipal.realmId);
        await expect(distinctContext).not.toContainText(
          originalPrincipal.realmId,
        );
        await page.goto(origin + basePath + "/catalog");
        // A valid OAuth ceiling cannot supply the other realm's administrator
        // grant, even though both genuine provider tokens have the same sub.
        ledger.expectFailure("GET", "/_catalog/api/v1/access/roles", 403);
        const refusedDirectory = await page.evaluate(async (prefix) => {
          const response = await fetch(
            prefix + "/_catalog/api/v1/access/roles?limit=1",
            {
              headers: { "X-Catalog-Request": "1" },
            },
          );
          const body = await response.json();
          return {
            status: response.status,
            code: body.code,
            disclosed: Object.hasOwn(body, "items"),
          };
        }, basePath);
        expect(refusedDirectory).toEqual({
          status: 403,
          code: "FORBIDDEN",
          disclosed: false,
        });
        const rating = await ratingsOIDCJourney({
          page,
          origin,
          basePath,
          subject: ratingsSubject,
          service,
          expectFailure: ledger.expectFailure,
          restartAPI: async () => {
            await compose("restart", "api");
            apiPort = await publishedPort("api");
            await ready(`http://127.0.0.1:${apiPort}/readyz`);
          },
          onPhase: (name) => {
            phase = name;
            console.log(`Checking ${name}`);
          },
        });
        await collector.finish();
        await browser.close();
        await rm(join(directory, "ui.json"));
        await json("ui.json", { ...ratingsUI, scopes: ["catalog.read"] });
        await compose(
          "up",
          "-d",
          "--no-deps",
          "--force-recreate",
          "ui-browser",
        );
        uiPort = await publishedPort("ui-browser");
        await ready(`http://127.0.0.1:${uiPort}/readyz`);
        await openBrowser();
        await page.goto(origin + basePath + "/catalog");
        await signIn();
        await ratingsMissingScope({
          page,
          origin,
          basePath,
          rating,
          expectFailure: ledger.expectFailure,
          onPhase: (name) => {
            phase = name;
            console.log(`Checking ${name}`);
          },
        });
        phase = "oidc-provider-outage";
        await page.getByLabel("Account", { exact: true }).click();
        await page
          .getByRole("button", { name: "Sign out", exact: true })
          .click();
        await collector.finish();
        await browser.close();
        await compose("stop", "catalog-oidc");
        await openBrowser();
        await page.goto(origin + basePath + "/access");
        ledger.expectFailure("POST", "/auth/login", 503);
        const [unavailable] = await Promise.all([
          page.waitForResponse(
            (response) =>
              new URL(response.url()).pathname === basePath + "/auth/login",
          ),
          page.getByRole("button", { name: "Sign in", exact: true }).click(),
        ]);
        expect(unavailable.status()).toBe(503);
        await expect(page.getByRole("alert")).toBeVisible();
        await expect(page.getByLabel("Account", { exact: true })).toHaveCount(
          0,
        );
      }
    }
    if (mode === "compose") {
      phase = `compose-${basePath ? "subpath" : "root"}-custom-policy`;
      await page.getByLabel("Account", { exact: true }).click();
      await page.getByRole("button", { name: "Sign out", exact: true }).click();
      const memberContext = await browser.newContext();
      const memberCollector = await collectBrowserCoverage(
        memberContext,
        coverageOutput,
      );
      coverageContexts++;
      memberContext.on("response", (r) =>
        ledger.observe(r.request().method(), r.url(), r.status()),
      );
      memberContext.on("requestfailed", (r) =>
        ledger.failure(r.method(), r.url(), r.failure()?.errorText),
      );
      const memberPage = await memberContext.newPage();
      memberPage.on("pageerror", () => jsErrors++);
      await memberCollector.ready(memberPage);
      try {
        const authorizationDiagnostics = async (response) => {
          const requestId = response.headers()["x-request-id"];
          const diagnostics = [];
          for (const service of ["api", "ui-browser"]) {
            try {
              const logs = await compose(
                "logs",
                "--no-color",
                "--no-log-prefix",
                "--tail",
                "500",
                service,
              );
              diagnostics.push(...rotationDiagnostics(logs, requestId));
            } catch {
              console.log("Owned authorization log diagnostics unavailable");
            }
          }
          return diagnostics;
        };
        let assignmentEvidenceQualified = false;
        await authorizationJourney({
          page,
          memberPage,
          origin,
          basePath,
          memberAlias: "local-policy",
          // The subpath policy journey follows the independently published Agent.
          expectedCatalogCount: basePath ? 4 : 3,
          expectFailure: ledger.expectFailure,
          inspectCommandOutcome: async function inspectOwnedCommandOutcome({
            title,
            response,
          }) {
            inspectAuthorizationCommandOutcome = inspectOwnedCommandOutcome;
            // Calibrate this projection against one real successful revoke,
            // then retain only unexpected refusals. No private command inputs.
            if (title === "Change group state") {
              const route = new URL(response.url()).pathname.match(
                /\/access\/groups\/([0-9a-f-]{36}):(suspend|reactivate|archive)$/,
              );
              const headers = response.request().headers();
              const key = headers["idempotency-key"];
              const revision = headers["if-match"]?.match(
                /^"rev-([1-9][0-9]{0,9})"$/,
              )?.[1];
              const uuid =
                /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
              if (
                !route ||
                !uuid.test(route[1]) ||
                !uuid.test(key ?? "") ||
                !uuid.test(tenant) ||
                !revision
              )
                throw new Error("Owned group diagnostic inputs are invalid");
              const [, id, transition] = route;
              const expectedState = {
                suspend: "suspended",
                reactivate: "active",
                archive: "archived",
              }[transition];
              // Retain only bounded state predicates, never command bodies, keys,
              // actor identifiers, authorization digests or raw timestamps.
              const sql = `WITH g AS (
                SELECT state,revision,updated_at FROM access_groups
                WHERE tenant_id='${tenant}' AND id='${id}'
              ), c AS (
                SELECT started_at,completed_at,atomic_response_status FROM idempotency_keys
                WHERE tenant_id='${tenant}' AND protocol_version=2
                  AND route='POST /api/v1/access/groups/${id}:${transition}'
                  AND actor IN (SELECT 'membership:'||realm_id::text||':'||id::text
                    FROM tenant_memberships WHERE tenant_id='${tenant}' AND subject='local-admin')
                  AND idempotency_key='${key}' LIMIT 1
              ) SELECT json_build_object(
                'groupFound',EXISTS(SELECT 1 FROM g),
                'requestedStateMatches',(SELECT state='${expectedState}' FROM g),
                'expectedRevisionMatches',(SELECT revision=${revision} FROM g),
                'nextRevisionMatches',(SELECT revision=${revision}+1 FROM g),
                'databaseClockBeforeGroupUpdate',(SELECT clock_timestamp()<updated_at FROM g),
                'commandFound',EXISTS(SELECT 1 FROM c),
                'commandCompleted',(SELECT completed_at IS NOT NULL FROM c),
                'commandStatus',(SELECT atomic_response_status FROM c),
                'commandStartedBeforeGroupUpdate',(SELECT c.started_at<g.updated_at FROM c CROSS JOIN g)
              )`;
              const state = JSON.parse(
                await compose(
                  "exec",
                  "-T",
                  "-e",
                  "PGOPTIONS=-c statement_timeout=5000",
                  "postgres",
                  "psql",
                  "-X",
                  "-A",
                  "-t",
                  "-v",
                  "ON_ERROR_STOP=1",
                  "-U",
                  "catalog",
                  "-d",
                  "catalog",
                  "-c",
                  sql,
                ),
              );
              if (response.status() < 400) {
                expect(state).toMatchObject({
                  groupFound: true,
                  requestedStateMatches: true,
                  expectedRevisionMatches: false,
                  nextRevisionMatches: true,
                  commandFound: true,
                  commandCompleted: true,
                  commandStatus: 200,
                });
                expect(typeof state.databaseClockBeforeGroupUpdate).toBe(
                  "boolean",
                );
                expect(typeof state.commandStartedBeforeGroupUpdate).toBe(
                  "boolean",
                );
              }
              const diagnostics =
                response.status() >= 400
                  ? await authorizationDiagnostics(response)
                  : [];
              const evidenceDirectory = join(
                root,
                "output/playwright",
                project,
              );
              await mkdir(evidenceDirectory, { recursive: true });
              const evidencePath = join(
                evidenceDirectory,
                `group-${basePath ? "subpath" : "root"}-${transition}.json`,
              );
              await writeFile(
                evidencePath,
                JSON.stringify({ ...state, diagnostics }, null, 2) + "\n",
              );
              console.log(
                `Owned group ${transition} outcome: ${JSON.stringify({ ...state, diagnostics })}; evidence: ${evidencePath}`,
              );
              return;
            }
            if (title !== "Revoke access assignment") return;
            if (response.status() < 400 && assignmentEvidenceQualified) return;
            const id = new URL(response.url()).pathname.match(
              /\/access\/assignments\/([0-9a-f-]{36}):revoke$/,
            )?.[1];
            const headers = response.request().headers();
            const key = headers["idempotency-key"];
            const revision = headers["if-match"]?.match(
              /^"rev-([1-9][0-9]{0,9})"$/,
            )?.[1];
            const uuid =
              /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
            if (
              !uuid.test(id ?? "") ||
              !uuid.test(key ?? "") ||
              !uuid.test(tenant) ||
              !revision
            )
              throw new Error("Owned assignment diagnostic inputs are invalid");
            const sql = `WITH a AS (
              SELECT state,revision,granted_at FROM role_assignments
              WHERE tenant_id='${tenant}' AND id='${id}'
            ), c AS (
              SELECT started_at,completed_at,atomic_response_status FROM idempotency_keys
              WHERE tenant_id='${tenant}' AND protocol_version=2
                AND route='POST /api/v1/access/assignments/${id}:revoke'
                AND actor IN (SELECT 'membership:'||realm_id::text||':'||id::text
                  FROM tenant_memberships WHERE tenant_id='${tenant}' AND subject='local-admin')
                AND idempotency_key='${key}' LIMIT 1
            ) SELECT json_build_object(
              'assignmentFound',EXISTS(SELECT 1 FROM a),
              'assignmentActive',(SELECT state='active' FROM a),
              'expectedRevisionMatches',(SELECT revision=${revision} FROM a),
              'nextRevisionMatches',(SELECT revision=${revision}+1 FROM a),
              'commandFound',EXISTS(SELECT 1 FROM c),
              'commandCompleted',(SELECT completed_at IS NOT NULL FROM c),
              'commandStatus',(SELECT atomic_response_status FROM c),
              'commandStartedBeforeGrant',(SELECT c.started_at<a.granted_at FROM c CROSS JOIN a)
            )`;
            const state = JSON.parse(
              await compose(
                "exec",
                "-T",
                "-e",
                "PGOPTIONS=-c statement_timeout=5000",
                "postgres",
                "psql",
                "-X",
                "-A",
                "-t",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "catalog",
                "-d",
                "catalog",
                "-c",
                sql,
              ),
            );
            if (response.status() < 400) {
              expect(state).toEqual({
                assignmentFound: true,
                assignmentActive: false,
                expectedRevisionMatches: false,
                nextRevisionMatches: true,
                commandFound: true,
                commandCompleted: true,
                commandStatus: 200,
                commandStartedBeforeGrant: false,
              });
              assignmentEvidenceQualified = true;
              console.log("Owned assignment evidence calibration passed");
              return;
            }
            console.log(`Owned assignment refusal: ${JSON.stringify(state)}`);
            const diagnostics = await authorizationDiagnostics(response);
            console.log(
              `Owned assignment diagnostics: ${JSON.stringify(diagnostics)}`,
            );
            const evidenceDirectory = join(root, "output/playwright", project);
            await mkdir(evidenceDirectory, { recursive: true });
            await writeFile(
              join(evidenceDirectory, "assignment-refusal.json"),
              JSON.stringify({ ...state, diagnostics }, null, 2) + "\n",
            );
          },
          qualifyRetainedPolicy: async (policy) => {
            // Explicitly repeat the existing operator/dev sample procedures. They
            // may provision configured built-ins, but must not replace custom data.
            // Use the owned owner-role operator container, never the API login.
            phase = `compose-${basePath ? "subpath" : "root"}-repeat-provision`;
            await compose("run", "--rm", "--no-deps", "provision");
            if (!basePath) {
              phase = "compose-root-repeat-seed";
              await compose(
                "exec",
                "-T",
                "api",
                "/usr/local/bin/catalogctl",
                "seed-demo",
                "--sample-set",
                "minimal",
                "--base-url",
                "http://127.0.0.1:8080",
                "--state-file",
                "/tmp/browser-demo.json",
                "--timeout",
                "60s",
              );
            }
            const policyRead = async (path) => {
              const response = await page.evaluate(
                async (url) => {
                  const reply = await fetch(url, {
                    headers: { "X-Catalog-Request": "1" },
                  });
                  return { status: reply.status, data: await reply.json() };
                },
                origin + basePath + "/_catalog" + path,
              );
              expect(response.status).toBe(200);
              return response.data;
            };
            expect(
              await policyRead(`/api/v1/access/roles/${policy.roleId}`),
            ).toMatchObject({
              id: policy.roleId,
              revision: 1,
              permissions: [
                "namespace.read",
                "resource.create",
                "resource.read",
              ],
            });
            const bindings = await policyRead(
              `/api/v1/access/assignments?groupId=${policy.groupId}&limit=25`,
            );
            expect(bindings.items).toHaveLength(1);
            expect(bindings.items[0]).toMatchObject({
              id: policy.assignmentId,
              state: "active",
            });
            const relations = await policyRead(
              `/api/v1/access/groups/${policy.groupId}/members?limit=25`,
            );
            expect(relations.items).toHaveLength(1);
            expect(relations.items[0].membershipId).toBe(policy.membershipId);
          },
        });
      } finally {
        await memberCollector.finish();
        await memberContext.close();
      }
      phase = `compose-${basePath ? "subpath" : "root"}-access-qualification`;
      console.log(`Checking ${phase}`);
      await authorizationQualificationJourney({ page, origin, basePath });
    }
    if (mode === "compose" && basePath) {
      phase = "compose-database-unavailable";
      console.log(`Checking ${phase}`);
      const retainedCookies = await context.cookies(origin + basePath + "/");
      expect(
        retainedCookies.some((cookie) => cookie.name === "catalog-ui-packaged"),
      ).toBe(true);
      await collector.finish();
      await browser.close();
      await compose("stop", "postgres");
      await openBrowser();
      await context.addCookies(retainedCookies);
      ledger.expectFailure("GET", "/auth/session", 503, 2);
      const [unavailable] = await Promise.all([
        page.waitForResponse(
          (r) => new URL(r.url()).pathname === basePath + "/auth/session",
        ),
        page.goto(origin + basePath + "/access"),
      ]);
      expect(unavailable.status()).toBe(503);
      await expect(page.getByRole("alert")).toBeVisible();
      // The application rejects the failed session response without reading its
      // body. Read a separate bounded browser request rather than waiting for
      // Playwright to retrieve that unconsumed response through Chromium.
      const sessionFailure = await page.evaluate(async (route) => {
        const response = await fetch(route, {
          headers: { "X-Catalog-Request": "1" },
          credentials: "same-origin",
          signal: AbortSignal.timeout(10000),
        });
        return { status: response.status, body: await response.json() };
      }, basePath + "/auth/session");
      expect(sessionFailure).toMatchObject({
        status: 503,
        body: { status: 503, code: "catalog_unavailable" },
      });
      await expect(
        page.getByRole("heading", { name: "My access", exact: true }),
      ).toHaveCount(0);
      expect(
        (await context.cookies(origin + basePath + "/")).map((c) => ({
          name: c.name,
          value: c.value,
        })),
      ).toEqual(retainedCookies.map((c) => ({ name: c.name, value: c.value })));
      await collector.finish();
      await browser.close();
      phase = "compose-database-recovered";
      console.log(`Checking ${phase}`);
      await compose("start", "postgres");
      await ready(`http://127.0.0.1:${apiPort}/readyz`);
      // Finish the owned UI topology change before reopening Chromium. Docker
      // veth changes can otherwise interrupt an unrelated in-flight browser request.
      phase = "compose-documentation-override";
      await rm(join(directory, "ui.json"));
      await json("ui.json", {
        ...uiConfig,
        documentationUrl: documentationOrigin + "/manual/",
      });
      await compose("up", "-d", "--no-deps", "--force-recreate", "ui-browser");
      uiPort = await publishedPort("ui-browser");
      await ready(`http://127.0.0.1:${uiPort}/readyz`);
      await openBrowser();
      await context.addCookies(retainedCookies);
      await page.goto(origin + basePath + "/access");
      await expect(
        page.getByRole("heading", { name: "My access", exact: true }),
      ).toBeVisible();
      await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
    }
    if (mode === "compose" && basePath) {
      // Only this selected fixture origin is used for the optional external link.
      // Ordinary cookie domain/path rules still apply across ports on one host.
      const externalLink = page
        .getByRole("group", { name: "Help" })
        .getByRole("link", { name: "Documentation", exact: true });
      await expect(externalLink).toHaveAttribute(
        "href",
        documentationOrigin + "/manual/",
      );
      const [externalTab] = await Promise.all([
        context.waitForEvent("page"),
        externalLink.click(),
      ]);
      await expect(
        externalTab.getByRole("heading", { name: "Owned operator manual" }),
      ).toBeVisible();
      expect(await externalTab.evaluate(() => globalThis.opener === null)).toBe(
        true,
      );
      await externalTab.close();
      expect(documentationRequests).toEqual([
        { exactPath: true, authorization: false, referrer: false },
      ]);
      documentationAvailable = false;
      const [unavailableTab] = await Promise.all([
        context.waitForEvent("page"),
        externalLink.click(),
      ]);
      await expect(
        unavailableTab.getByRole("heading", {
          name: "Documentation unavailable",
        }),
      ).toBeVisible();
      await unavailableTab.close();
      // The unavailable manual cannot become a host readiness/login dependency.
      await ready(`http://127.0.0.1:${uiPort}/readyz`);
      await page.getByLabel("Account", { exact: true }).click();
      await page.getByRole("button", { name: "Sign out", exact: true }).click();
      await signIn();
      await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
      await page.goto(origin + basePath + "/api-reference");
      await expect(
        page.getByRole("heading", { name: "API reference", exact: true }),
      ).toBeVisible();
      await page.goto(origin + basePath + "/catalog");
      await expect(
        page.getByRole("heading", { name: "Catalog", exact: true }),
      ).toBeVisible();
      expect(
        documentationRequests.every(
          (r) => r.exactPath && !r.authorization && !r.referrer,
        ),
      ).toBe(true);
    }
    expect(jsErrors).toBe(0);
    expect(outcomes.results()).toEqual([]);
    await collector.finish();
    await context.close();
    await browser.close();
  }
  if (mode === "compose" && !coverage) {
    await publisherVerificationJourney({
      origin: origin + "/browser",
      fixtureRequest,
      replyFault: publisherDNSFault,
      output: join(root, "output/playwright", project),
      phase: (name) => {
        phase = `publisher-dns-${name}`;
        console.log(`Checking ${phase}`);
      },
      request: async (
        method,
        path,
        { body, key, etag, token = "local-admin" } = {},
      ) => {
        const response = await fetch(`http://127.0.0.1:${apiPort}${path}`, {
          method,
          signal: AbortSignal.timeout(15000),
          headers: {
            Authorization: `Bearer ${token}`,
            ...(body ? { "Content-Type": "application/json" } : {}),
            ...(key ? { "Idempotency-Key": key } : {}),
            ...(etag ? { "If-Match": etag } : {}),
          },
          ...(body ? { body: JSON.stringify(body) } : {}),
        });
        const text = await response.text();
        const suffix = path.split("/").at(-1);
        const operations = new Set([
          "verification",
          "challenge",
          "verification:issue",
          "verification:check",
          "verification:clear",
          "livez",
          "readyz",
          "capabilities",
          "ard.json",
        ]);
        return {
          operation: operations.has(suffix) ? suffix : "publisher",
          status: response.status,
          headers: response.headers,
          text,
          data: JSON.parse(text),
        };
      },
      restart: async (selectedMode) => {
        await browser?.close();
        const current = JSON.parse(
          await readFile(join(directory, "api.json"), "utf8"),
        );
        current.publisherDomainVerification.mode = selectedMode;
        await rm(join(directory, "api.json"));
        await json("api.json", current);
        await compose(
          "up",
          "-d",
          "--wait",
          "--no-deps",
          "--force-recreate",
          "api",
          "worker",
        );
        apiPort = await publishedPort("api");
        await ready(`http://127.0.0.1:${apiPort}/readyz`);
        await waitForStableNetwork({ interrupted: () => interrupted });
        browser = await launchBrowser();
        const context = await browser.newContext();
        const page = await context.newPage();
        lastPage = page;
        page.setDefaultTimeout(15000);
        return page;
      },
    });
  }
  console.log(
    mcp
      ? `Packaged MCP OAuth/TLS passed (${mcpChecks.length} checks)`
      : publisherDNSOnly
        ? "Packaged Publisher DNS workflow passed"
        : `Packaged UI ${mode} passed (root and subpath)`,
  );
} catch (error) {
  const site = String(error?.stack).match(
    /(?:packaged-(?:journeys|test)|authorization(?:-qualification)?-journey|mcp-journey|cli-oidc-journey|ratings-oidc-journey|publisher-verification-journey)\.mjs:\d+:\d+/,
  )?.[0];
  if (site) console.error(`Failing packaged assertion: ${site}`);
  if (
    mcp &&
    /^MCP qualification failed: [a-zA-Z0-9 _./:-]+$/.test(error?.message ?? "")
  )
    console.error(error.message);
  console.error(
    `Packaged UI test failed during ${phase}; private command/browser payloads were not recorded`,
  );
  if (outcomes) console.error(outcomes.results());
  if (!interrupted && lastPage && !lastPage.isClosed()) {
    if (mode === "compose") {
      const output = join(root, "output/playwright", project);
      await mkdir(output, { recursive: true });
      // Only synthetic local-user fixtures; provider screens are never saved.
      try {
        await lastPage.screenshot({
          path: join(output, "failure.png"),
          fullPage: true,
          mask: [lastPage.getByLabel("Signing secret", { exact: true })],
          timeout: 5000,
        });
        console.error(`Synthetic fixture screenshot: ${output}/failure.png`);
      } catch {
        console.error("Synthetic fixture screenshot unavailable");
      }
    }
    // Route names and counts only: no query parameters, form values or DOM
    // dumps from the provider, which can contain login credentials/state.
    console.error(
      `Browser protocol: ${new URL(lastPage.url()).protocol}; route: ${new URL(lastPage.url()).pathname}; catalog cards: ${await lastPage.locator("article.resource-card").count()}; provider input error: ${await lastPage.locator("#input-error").count()}`,
    );
  }
  process.exitCode = 1;
} finally {
  cleaning = true;
  clearTimeout(stopTimer);
  await browser?.close();
  if (documentationServer) {
    documentationServer.closeAllConnections();
    await new Promise((done) => documentationServer.close(done));
  }
  if (proxy) {
    proxy.closeAllConnections();
    await new Promise((done) => proxy.close(done));
  }
  let cleaned = false;
  try {
    await command(
      [...baseCompose, "down", "--volumes", "--remove-orphans"],
      90,
      true,
    );
    for (const tag of candidates
      ? [fixtureImage]
      : [image, uiImage, fixtureImage]) {
      if (
        (
          await command(["docker", "image", "ls", "--quiet", tag], 10, true)
        ).trim()
      )
        await command(["docker", "image", "rm", tag], 30, true);
    }
    cleaned = true;
  } catch {
    console.error(`Cleanup requires inspection of owned project ${project}`);
    process.exitCode = 1;
  }
  if (cleaned) {
    try {
      await rm(privateDirectory, { recursive: true, force: true });
      mcpCleanup = true;
    } catch {
      process.exitCode = 1;
      console.error("Owned private fixture cleanup failed");
    }
  } else
    console.error(
      `Private fixture inputs retained for inspection: ${privateDirectory}`,
    );
  process.off("SIGINT", stop);
  process.off("SIGTERM", stop);
}

if (coverage) {
  // Each replaced browser has a separately finished collector. OIDC includes
  // the positive-rating and missing-write-scope sessions in addition to its
  // existing root, subpath, scope-ceiling and provider-outage sessions.
  const expectedContexts = { compose: 6, oidc: 6 }[mode];
  if (process.exitCode || coverageContexts !== expectedContexts) {
    await writeFile(
      join(coverageOutput, "failed.json"),
      JSON.stringify({ status: "failed", phase }),
    );
    process.exitCode = 1;
  } else {
    try {
      await reportBrowserCoverage(coverageOutput, {
        status: "passed",
        passed: 1,
        failed: 0,
        skipped: 0,
        executionUnit: "packaged workflow",
        profile: mode,
        contexts: coverageContexts,
        browser: coverageBrowser,
        cleanup: "passed",
      });
    } catch {
      await writeFile(
        join(coverageOutput, "failed.json"),
        JSON.stringify({ status: "failed", phase: "coverage-report" }),
      );
      console.error("Packaged browser coverage report incomplete");
      process.exitCode = 1;
    }
  }
}

if (mcp) {
  const reportDirectory = join(root, "output/mcp-http", project);
  await mkdir(reportDirectory, { recursive: true });
  await writeFile(
    join(reportDirectory, "report.json"),
    JSON.stringify(
      {
        status: !process.exitCode && mcpCleanup ? "passed" : "failed",
        phase,
        cleanup: mcpCleanup ? "passed" : "failed",
        build: mcpBuild,
        protocols: ["2026-07-28", "2025-11-25"],
        checks: mcpChecks,
      },
      null,
      2,
    ),
  );
  console.log(`MCP qualification report: ${reportDirectory}/report.json`);
}
