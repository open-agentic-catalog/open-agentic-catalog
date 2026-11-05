// Focused real-editor debugger iteration; full qualification selects the same case.
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { developmentHost } from "./editor.mjs";
const root = resolve(import.meta.dirname, "../..");
const work = await mkdtemp(join(root, "output/vscode/debug-"));
const user = join(work, "user"),
  driver = join(work, "driver"),
  result = join(work, "result.json");
await mkdir(driver);
await mkdir(join(work, "workspace"));
await mkdir(join(user, "User"), { recursive: true });
const { createServer } = await import("node:http");
const blocked = createServer((_, response) => {
  response.writeHead(403);
  response.end();
});
blocked.on("connect", (_, socket) =>
  socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"),
);
await new Promise((done) => blocked.listen(0, "127.0.0.1", done));
const proxy = `http://127.0.0.1:${blocked.address().port}`;
await writeFile(
  join(user, "User/settings.json"),
  JSON.stringify({
    "http.proxy": proxy,
    "http.proxySupport": "override",
    "http.noProxy": ["127.0.0.1", "localhost", "::1"],
    "extensions.autoUpdate": false,
    "extensions.autoCheckUpdates": false,
    "telemetry.telemetryLevel": "off",
    "security.workspace.trust.enabled": false,
  }),
);
await writeFile(
  join(driver, "package.json"),
  JSON.stringify({
    name: "catalog-debug-parent",
    publisher: "local",
    version: "0.0.1",
    engines: { vscode: "^1.139.1" },
    activationEvents: ["onStartupFinished"],
    main: "index.cjs",
  }),
);
await writeFile(
  join(driver, "index.cjs"),
  `const vscode=require('vscode');const fs=require('node:fs');exports.activate=()=>{setImmediate(async()=>{try{await require(${JSON.stringify(resolve(import.meta.dirname, "debug-host-tests.cjs"))}).run();fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}});};`,
);
const testState = JSON.parse(
  await readFile(join(root, "test/vscode/.tools/installed.json"), "utf8"),
);
try {
  await developmentHost(
    [
      `--extensionDevelopmentPath=${driver}`,
      join(work, "workspace"),
      `--user-data-dir=${user}`,
      `--extensions-dir=${join(work, "extensions")}`,
      `--shared-data-dir=${join(work, "shared")}`,
      "--no-sandbox",
      "--disable-gpu",
      "--skip-welcome",
      "--skip-release-notes",
      "--disable-updates",
      "--disable-telemetry",
      "--disable-workbench-experiments",
      "--disable-workspace-trust",
      "--remote-debugging-port=0",
      "--remote-debugging-address=127.0.0.1",
      "--password-store=basic",
    ],
    {
      ...process.env,
      CATALOG_VSCODE_HOST: testState.host,
      CATALOG_VSCODE_DEBUG_DIR: user,
      CATALOG_VSCODE_SOURCE_EXTENSION: join(root, "extensions/vscode"),
      CATALOG_VSCODE_SCREENSHOTS: join(root, "output/vscode/screenshots"),
    },
    result,
    60000,
  );
  console.log("Focused real-editor TypeScript breakpoint passed");
} finally {
  blocked.closeAllConnections();
  await new Promise((done) => blocked.close(done));
  await rm(work, {
    recursive: true,
    force: true,
    maxRetries: 5,
    retryDelay: 200,
  });
}
