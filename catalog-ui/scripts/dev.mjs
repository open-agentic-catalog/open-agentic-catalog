import { developmentUsers } from "./development-users.mjs";
// Local-only launcher. Temporary key material is removed when the host stops.
import { cp, mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { stageDocumentation } from "./dev-documentation.mjs";
import { randomBytes } from "node:crypto";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { parseArgs } from "node:util";

const { values } = parseArgs({
  options: {
    "api-url": { type: "string", default: "http://127.0.0.1:8080" },
    port: { type: "string", default: "4180" },
    "base-path": { type: "string", default: "" },
  },
});
const upstream = new URL(values["api-url"]);
if (
  !["127.0.0.1", "[::1]"].includes(upstream.hostname) ||
  upstream.protocol !== "http:" ||
  upstream.pathname !== "/" ||
  upstream.search ||
  upstream.hash ||
  upstream.username ||
  upstream.password
)
  throw new Error("Use an owned literal-loopback HTTP Catalog origin");
if (!/^\d+$/.test(values.port) || +values.port < 1024 || +values.port > 65535)
  throw new Error("Port must be 1024..65535");
const directory = await mkdtemp(join(tmpdir(), "catalog-ui-dev-"));
try {
  let assets = resolve("catalog-ui/dist");
  if (process.env.UI_DOCUMENTATION_DIRECTORY) {
    const staged = join(directory, "assets");
    await cp(assets, staged, { recursive: true });
    await stageDocumentation(process.env.UI_DOCUMENTATION_DIRECTORY, staged);
    assets = staged;
  }
  const profile = JSON.parse(await readFile("config/local.json", "utf8"));
  const keysFile = join(directory, "keys.json");
  const usersFile = join(directory, "users.json");
  const configFile = join(directory, "config.json");
  await writeFile(
    keysFile,
    JSON.stringify([
      {
        hash: randomBytes(64).toString("hex"),
        block: randomBytes(32).toString("hex"),
      },
    ]),
    { mode: 0o600 },
  );
  await writeFile(
    usersFile,
    JSON.stringify(developmentUsers(profile.auth.developmentTokens)),
    { mode: 0o600 },
  );
  await writeFile(
    configFile,
    JSON.stringify({
      listen: `127.0.0.1:${values.port}`,
      environment: "development",
      logLevel: process.env.CATALOG_UI_LOG_LEVEL ?? "info",
      logFormat: process.env.CATALOG_UI_LOG_FORMAT ?? "text",
      origin: `http://127.0.0.1:${values.port}`,
      basePath: values["base-path"],
      assets,
      documentationUrl: process.env.CATALOG_UI_DOCUMENTATION_URL ?? "",
      upstream: upstream.origin,
      cookieName: `catalog-ui-dev-${values.port}`,
      keysFile,
      sessionSeconds: 3600,
      requestBytes: 2097152,
      responseBytes: 16777216,
      timeoutSeconds: 60,
      authMode: "development",
      developmentUsersFile: usersFile,
    }),
    { mode: 0o600 },
  );
  const child = spawn(resolve("bin/catalog-ui"), ["--config", configFile], {
    stdio: "inherit",
  });
  for (const signal of ["SIGINT", "SIGTERM"])
    process.once(signal, () => child.kill(signal));
  process.exitCode = await new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => resolve(code ?? 1));
  });
} finally {
  await rm(directory, { recursive: true, force: true });
}
