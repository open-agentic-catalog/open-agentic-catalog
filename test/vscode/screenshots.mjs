// Explicit guide capture: a fresh installed editor, benign governed records and
// a real local A2A reply. The security/regression selection stays independent.
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { execFileSync } from "node:child_process";
import { createServer } from "node:http";
import { resolveCliArgsFromVSCodeExecutablePath } from "../../extensions/vscode/node_modules/@vscode/test-electron/out/index.js";
import { developmentHost } from "./editor.mjs";

const work = await mkdtemp(join(process.env.CATALOG_VSCODE_RUN_DIR, "guide-"));
const blocked = createServer((_, response) => {
  response.writeHead(403);
  response.end();
});
blocked.on("connect", (_, socket) =>
  socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"),
);
try {
  await new Promise((done, reject) => {
    blocked.once("error", reject);
    blocked.listen(0, "127.0.0.1", done);
  });
  const proxy = `http://127.0.0.1:${blocked.address().port}`;
  const user = join(work, "user"),
    driver = join(work, "extensions/local.catalog-guide-capture-0.0.1"),
    workspace = process.env.CATALOG_VSCODE_WORKSPACE;
  for (const directory of [join(user, "User"), driver, workspace])
    await mkdir(directory, { recursive: true });
  const globalStorage = join(user, "User/globalStorage");
  await mkdir(globalStorage, { recursive: true });
  await writeFile(
    join(globalStorage, "storage.json"),
    JSON.stringify({
      windowsState: {
        lastActiveWindow: {
          uiState: { x: 0, y: 0, width: 1440, height: 1150, mode: 0 },
        },
        openedWindows: [],
      },
    }),
  );
  await writeFile(
    join(user, "User/settings.json"),
    JSON.stringify({
      "openAgenticCatalog.connections": [
        {
          id: "sample",
          label: "Sample Catalog",
          origin: process.argv[2],
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
      ],
      "openAgenticCatalog.a2a": {
        allowLoopbackHTTP: true,
        caFile: process.env.CATALOG_VSCODE_PUBLIC_CA,
      },
      "openAgenticCatalog.packageTools": {
        caFile: process.env.CATALOG_VSCODE_PUBLIC_CA,
      },
      "chat.plugins.enabled": true,
      "telemetry.telemetryLevel": "off",
      "update.mode": "none",
      "extensions.autoUpdate": false,
      "extensions.autoCheckUpdates": false,
      "workbench.enableExperiments": false,
      "workbench.colorTheme": "Default Dark Modern",
      "window.title": "Catalog workshop",
      "window.dialogStyle": "custom",
      "window.menuStyle": "custom",
      "git.enabled": false,
      "chat.mcp.autostart": "never",
      "chat.mcp.gallery.enabled": false,
      "http.proxy": proxy,
      "http.proxySupport": "override",
      "http.noProxy": ["127.0.0.1", "localhost", "::1"],
    }),
  );
  await writeFile(
    join(driver, "package.json"),
    JSON.stringify({
      name: "catalog-guide-capture",
      publisher: "local",
      version: "0.0.1",
      engines: { vscode: "^1.139.1" },
      main: "index.cjs",
      activationEvents: ["onStartupFinished"],
    }),
  );
  const result = join(work, "result.json");
  await writeFile(
    join(driver, "index.cjs"),
    `const vscode=require('vscode');const fs=require('node:fs');exports.activate=()=>{void(async()=>{try{await require(${JSON.stringify(resolve(import.meta.dirname, "screenshots-host.cjs"))}).run();fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}})();};`,
  );
  const [cli, ...cliArgs] = resolveCliArgsFromVSCodeExecutablePath(
    process.env.CATALOG_VSCODE_HOST,
  );
  const profile = [
    "--shared-data-dir",
    join(work, "shared"),
    "--user-data-dir",
    user,
    "--extensions-dir",
    join(work, "extensions"),
  ];
  execFileSync(
    cli,
    [
      ...cliArgs,
      ...profile,
      "--install-extension",
      process.env.CATALOG_VSCODE_VSIX,
      "--force",
    ],
    {
      env: { ...process.env, DONT_PROMPT_WSL_INSTALL: "1" },
      stdio: "pipe",
      timeout: 60000,
    },
  );
  await developmentHost(
    [
      ...profile,
      workspace,
      "--remote-debugging-port=0",
      "--remote-debugging-address=127.0.0.1",
      "--no-sandbox",
      "--disable-gpu",
      "--skip-welcome",
      "--skip-release-notes",
      "--disable-updates",
      "--disable-telemetry",
      "--disable-workbench-experiments",
      "--disable-workspace-trust",
      "--password-store=basic",
      `--proxy-server=${proxy}`,
      "--proxy-bypass-list=127.0.0.1;[::1]",
    ],
    {
      ...process.env,
      CATALOG_VSCODE_DEBUG_DIR: user,
      NODE_EXTRA_CA_CERTS: process.env.CATALOG_VSCODE_PUBLIC_CA,
    },
    result,
    90000,
  );
  console.log(
    "Benign guide screenshots captured from the installed VSIX and owned local Catalog/A2A peer",
  );
} finally {
  try {
    if (blocked.listening)
      await new Promise((done, reject) =>
        blocked.close((error) => (error ? reject(error) : done())),
      );
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}
