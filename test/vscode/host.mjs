import {
  runTests,
  resolveCliArgsFromVSCodeExecutablePath,
} from "../../extensions/vscode/node_modules/@vscode/test-electron/out/index.js";
import {
  mkdtemp,
  mkdir,
  writeFile,
  rm,
  readFile,
  readdir,
  stat,
} from "node:fs/promises";
import assert from "node:assert/strict";
import { resolve, join } from "node:path";
import { peers } from "./host-peers.mjs";
import { createServer } from "node:http";
import { execFileSync } from "node:child_process";
import { dirname, delimiter } from "node:path";
import { developmentHost } from "./editor.mjs";

const localPeers = await peers();
const root = resolve(import.meta.dirname, "../..");
const work = await mkdtemp(
  join(
    process.env.CATALOG_VSCODE_RUN_DIR ?? join(root, "output/vscode"),
    "host-",
  ),
);
const blocked = createServer((_, response) => {
  response.writeHead(403);
  response.end();
});
blocked.on("connect", (_, socket) =>
  socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"),
);
await new Promise((resolve) => blocked.listen(0, "127.0.0.1", resolve));
const proxy = `http://127.0.0.1:${blocked.address().port}`;
try {
  const packageDir = join(work, "mcp-package");
  await mkdir(packageDir);
  await writeFile(
    join(packageDir, "package.json"),
    JSON.stringify({
      name: "catalog-owned-mcp",
      version: "1.0.0",
      bin: "index.cjs",
      license: "Apache-2.0",
    }),
  );
  await writeFile(
    join(packageDir, "index.cjs"),
    await readFile(resolve(import.meta.dirname, "mcp-stdio.cjs")),
  );
  const npm = resolve(
    dirname(process.execPath),
    "../lib/node_modules/npm/bin/npm-cli.js",
  );
  await writeFile(join(work, "empty.npmrc"), "");
  const npmEnv = {
    ...process.env,
    NPM_CONFIG_CACHE: join(work, "npm-cache"),
    NPM_CONFIG_USERCONFIG: "/dev/null",
    NPM_CONFIG_GLOBALCONFIG: join(work, "empty.npmrc"),
    NPM_CONFIG_IGNORE_SCRIPTS: "true",
    NPM_CONFIG_OFFLINE: "true",
  };
  execFileSync(process.execPath, [npm, "pack", "--ignore-scripts"], {
    cwd: packageDir,
    env: npmEnv,
    stdio: "pipe",
    timeout: 30000,
  });
  execFileSync(
    process.execPath,
    [
      npm,
      "install",
      "--prefix",
      join(work, "workspace"),
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      join(packageDir, "catalog-owned-mcp-1.0.0.tgz"),
    ],
    { env: npmEnv, stdio: "pipe", timeout: 30000 },
  );
  await mkdir(join(work, "user/User"), { recursive: true });
  await writeFile(
    join(work, "user/User/settings.json"),
    JSON.stringify({
      "openAgenticCatalog.connections": [
        {
          id: "qualification",
          label: "Owned Catalog",
          origin: process.argv[2],
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
        ...[
          ["published-real", process.env.CATALOG_VSCODE_PUBLIC, "anonymous"],
          ["published-private", process.env.CATALOG_VSCODE_PRIVATE, "manual"],
        ].map(([id, origin, auth]) => ({
          id,
          label: id,
          origin,
          mode: "published",
          caFile: process.env.CATALOG_VSCODE_PUBLIC_CA,
          auth,
          allowLoopbackHTTP: true,
        })),
        {
          id: "public",
          label: "Owned publication",
          origin: localPeers.origin,
          mode: "published",
          auth: "anonymous",
          allowLoopbackHTTP: true,
        },
        {
          id: "public-auth",
          label: "Owned private publication",
          origin: localPeers.origin,
          mode: "published",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
        {
          id: "oidc",
          label: "Owned issuer",
          origin: localPeers.origin,
          mode: "control",
          auth: "oidc",
          allowLoopbackHTTP: true,
          oidc: {
            issuer: localPeers.origin,
            clientId: "owned-extension",
            audience: "catalog",
            scopes: ["openid", "catalog.read"],
            refresh: false,
          },
        },
        {
          id: "artwork",
          label: "Owned artwork",
          origin: localPeers.origin,
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
        {
          id: "plugin-host",
          label: "Owned Plugin",
          origin: process.argv[2],
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
        {
          id: "a2a-host",
          label: "Owned A2A",
          origin: process.argv[2],
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
        {
          id: "skill-oci",
          label: "Owned OCI Skill",
          origin: process.argv[2],
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
      ],
      "openAgenticCatalog.a2a": {
        caFile: process.env.CATALOG_VSCODE_PUBLIC_CA,
        allowLoopbackHTTP: true,
        timeoutMs: 5000,
        streamTimeoutMs: 10000,
      },
      "openAgenticCatalog.packageTools": {
        gitPath: "/usr/bin/git",
        orasPath: process.env.CATALOG_VSCODE_ORAS,
        caFile: process.env.CATALOG_VSCODE_PUBLIC_CA,
        timeoutMs: 20000,
      },
      "git.openRepositoryInParentFolders": "never",
      "telemetry.telemetryLevel": "off",
      "update.mode": "none",
      "extensions.autoUpdate": false,
      "extensions.autoCheckUpdates": false,
      "workbench.enableExperiments": false,
      "chat.disableAIFeatures": false,
      "window.dialogStyle": "custom",
      "window.menuStyle": "custom",
      "chat.mcp.autostart": "never",
      "chat.mcp.gallery.enabled": false,
      "http.proxy": proxy,
      "http.proxySupport": "override",
      "http.noProxy": ["127.0.0.1", "localhost", "::1"],
    }),
  );
  await import("./package-controls.mjs").then((m) => m.qualifyPackages(work));
  await import("./capabilities.mjs").then((m) =>
    m.qualifyCapabilities(process.argv[2]),
  );
  const profileSettings = await readFile(
    join(work, "user/User/settings.json"),
    "utf8",
  );
  const driver = join(work, "driver");
  await mkdir(driver);
  await writeFile(
    join(driver, "codicon.ttf"),
    await readFile(
      join(
        dirname(process.env.CATALOG_VSCODE_HOST),
        "resources/app/out/media/codicon.ttf",
      ),
    ),
  );
  await writeFile(
    join(driver, "owned-product-icon-theme.json"),
    JSON.stringify({
      fonts: [
        {
          id: "owned-codicon",
          src: [{ path: "./codicon.ttf", format: "truetype" }],
        },
      ],
      iconDefinitions: Object.fromEntries(
        ["robot", "server", "book", "extensions"].map((id) => [
          id,
          { fontCharacter: "\\ea60", fontId: "owned-codicon" },
        ]),
      ),
    }),
  );
  await writeFile(
    join(driver, "package.json"),
    JSON.stringify({
      name: "catalog-qualification",
      publisher: "local",
      version: "0.0.1",
      engines: { vscode: "^1.139.1" },
      main: "./index.cjs",
      activationEvents: ["onStartupFinished"],
      contributes: {
        productIconThemes: [
          {
            id: "oac-owned-icons",
            label: "Owned local product icons",
            path: "./owned-product-icon-theme.json",
          },
        ],
        commands: [
          {
            command: "catalogQualification.captureTool",
            title: "Owned: Inspect Catalog tool selection",
          },
        ],
        menus: {
          "view/item/context": [
            {
              command: "catalogQualification.captureTool",
              when: "view == openAgenticCatalog.tools",
              group: "4_fixture",
            },
          ],
        },
      },
    }),
  );
  const resultFile = join(work, "result.json");
  await writeFile(
    join(driver, "index.cjs"),
    `const vscode=require('vscode');const fs=require('node:fs');exports.activate=()=>{void(async()=>{try {const observed=await require(${JSON.stringify(resolve(import.meta.dirname, "webview.cjs"))}).webview("observer");try {await require(${JSON.stringify(resolve(import.meta.dirname, "first-run-host-tests.cjs"))}).run();await require(${JSON.stringify(resolve(import.meta.dirname, "local-tools-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "connections-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "debug-host-tests.cjs"))}).run();await require(${JSON.stringify(resolve(import.meta.dirname, "identity-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "discovery-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "discovery-host-tests.cjs"))}).qualifyArtwork(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "inspection-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "consumer-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "consumer-host-tests.cjs"))}).agent(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "mcp-host-tests.cjs"))}).qualifyMcp(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "skills-host-tests.cjs"))}).qualifySkills(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "plugins-host-tests.cjs"))}).qualifyPlugins(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "a2a-host-tests.cjs"))}).qualifyA2A(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());}finally{await observed.finish();}fs.writeFileSync(${JSON.stringify(resultFile)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(resultFile)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}})();};`,
  );
  const [cli, ...args] = resolveCliArgsFromVSCodeExecutablePath(
    process.env.CATALOG_VSCODE_HOST,
  );
  const profile = [
    "--shared-data-dir",
    join(work, "shared"),
    "--user-data-dir",
    join(work, "user"),
    "--extensions-dir",
    join(work, "extensions"),
  ];
  execFileSync(
    cli,
    [
      ...args,
      ...profile,
      "--install-extension",
      process.env.CATALOG_VSCODE_VSIX,
      "--force",
    ],
    {
      timeout: 60000,
      env: { ...process.env, DONT_PROMPT_WSL_INSTALL: "1" },
      stdio: "pipe",
    },
  );
  const editorEnv = {
    ...process.env,
    CATALOG_VSCODE_SOURCE_EXTENSION: resolve(root, "extensions/vscode"),
    CATALOG_VSCODE_CATALOG: process.argv[2],
    CATALOG_VSCODE_ISSUER: localPeers.origin,
    CATALOG_VSCODE_DEBUG_DIR: join(work, "user"),
    CATALOG_VSCODE_PROFILE_ARGS: JSON.stringify(profile),
    CATALOG_VSCODE_SCREENSHOTS: resolve(root, "output/vscode/screenshots"),
    NODE_EXTRA_CA_CERTS: process.env.CATALOG_VSCODE_PUBLIC_CA,
    PATH: dirname(process.execPath) + delimiter + process.env.PATH,
    NPM_CONFIG_CACHE: join(work, "npm-cache"),
    NPM_CONFIG_OFFLINE: "true",
    CATALOG_MCP_SENTINEL_SECRET: "must-not-reach-stdio",
  };
  await mkdir(editorEnv.CATALOG_VSCODE_SCREENSHOTS, { recursive: true });
  const launchArgs = [
    `--extensionDevelopmentPath=${driver}`,
    join(work, "workspace"),
    ...profile,
    "--remote-debugging-port=0",
    "--remote-debugging-address=127.0.0.1",
    "--no-sandbox",
    "--disable-gpu",
    "--skip-welcome",
    "--skip-release-notes",
    "--disable-updates",
    "--disable-telemetry",
    "--disable-workbench-experiments",
    `--proxy-server=${proxy}`,
    "--proxy-bypass-list=127.0.0.1;[::1]",
    "--disable-workspace-trust",
    "--password-store=basic",
  ];
  // Ordinary development host: test-mode VS Code deliberately refuses modal
  // dialogs, which would skip the real consent and native MCP trust boundary.
  await developmentHost(launchArgs, editorEnv, resultFile, 120000);
  await writeFile(
    join(driver, "index.cjs"),
    `const vscode=require('vscode');const fs=require('node:fs');exports.activate=()=>{void(async()=>{try {const observed=await require(${JSON.stringify(resolve(import.meta.dirname, "webview.cjs"))}).webview("observer");try {await require(${JSON.stringify(resolve(import.meta.dirname, "capabilities-host-tests.cjs"))}).run(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());await require(${JSON.stringify(resolve(import.meta.dirname, "a2a-host-tests.cjs"))}).qualifyRecovery(await vscode.extensions.getExtension("open-agentic-catalog.open-agentic-catalog").activate());}finally{await observed.finish();}fs.writeFileSync(${JSON.stringify(resultFile)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(resultFile)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}})();};`,
  );
  await rm(resultFile, { force: true });
  await developmentHost(launchArgs, editorEnv, resultFile, 120000);

  console.log(
    "Actual native MCP and Skills: inert selection, HTTPS/offline-npx tools, exact Git/OCI Skill installation and native discovery, no-op, local-edit refusal and offline removal passed",
  );
  console.log(
    "Actual native Plugins: cancelled consent, exact HTTPS disabled install, native policy refusal, separate activation and Skill loading, disable, wrapper update, no-op, local-edit refusal, offline removal, preserved settings and zero hook/MCP execution passed",
  );
  console.log(
    "Actual native A2A: explicit interface/anonymous consent, rendered streaming reply, task readback, prompt refusal, disconnect and identity clearing passed",
  );
  // Test mode is appropriate for the existing notification-only journeys.
  // It deliberately refuses the modal approvals qualified above.
  await writeFile(join(driver, "index.cjs"), "exports.activate=()=>{};");
  await runTests({
    vscodeExecutablePath: process.env.CATALOG_VSCODE_HOST,
    extensionDevelopmentPath: driver,
    extensionTestsPath: resolve(import.meta.dirname, "host-tests.cjs"),
    launchArgs,
    extensionTestsEnv: editorEnv,
  });
  if (
    localPeers.stats.tokens !== 1 ||
    localPeers.stats.authorization !== 1 ||
    localPeers.stats.manifests !== 2 ||
    localPeers.stats.wrongCode !== 0
  )
    throw new Error(
      "Host peer traffic controls failed: " + JSON.stringify(localPeers.stats),
    );
  // Separate windows use the real installed VSIX and native filesystem URIs.
  // Neither changes the operator's profile or simulates a remote authority.
  for (const multiRoot of [false, true]) {
    const user = join(work, multiRoot ? "multi-user" : "empty-user");
    await mkdir(join(user, "User"), { recursive: true });
    await writeFile(join(user, "User/settings.json"), profileSettings);
    const folders = [join(work, "first-root"), join(work, "second-root")];
    for (const folder of folders) await mkdir(folder, { recursive: true });
    const workspace = join(work, "roots.code-workspace");
    await writeFile(
      workspace,
      JSON.stringify({ folders: folders.map((path) => ({ path })) }),
    );
    const result = join(
      work,
      multiRoot ? "multi-result.json" : "empty-result.json",
    );
    await writeFile(
      join(driver, "index.cjs"),
      `const vscode=require('vscode');const fs=require('node:fs');exports.activate=async()=>{try{await require(${JSON.stringify(resolve(import.meta.dirname, "workspace-tests.cjs"))}).run(${multiRoot});fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}};`,
    );
    const args = launchArgs
      .filter((arg) => arg !== join(work, "workspace"))
      .map((arg) => (arg === join(work, "user") ? user : arg));
    if (multiRoot) args.push(workspace);
    await developmentHost(
      args,
      { ...editorEnv, CATALOG_VSCODE_DEBUG_DIR: user },
      result,
      60000,
    );
    console.log(
      multiRoot
        ? "Packaged multi-root selection passed"
        : "Packaged no-workspace selection passed",
    );
  }
  const restrictedUser = join(work, "restricted-user"),
    restrictedDriver = join(work, "restricted-driver");
  await mkdir(join(restrictedUser, "User"), { recursive: true });
  const restrictedSettings = JSON.parse(profileSettings);
  restrictedSettings["security.workspace.trust.enabled"] = true;
  restrictedSettings["security.workspace.trust.startupPrompt"] = "never";
  await writeFile(
    join(restrictedUser, "User/settings.json"),
    JSON.stringify(restrictedSettings),
  );
  await mkdir(restrictedDriver);
  await writeFile(
    join(restrictedDriver, "package.json"),
    JSON.stringify({
      name: "catalog-restricted-qualification",
      publisher: "local",
      version: "0.0.1",
      engines: { vscode: "^1.139.1" },
      main: "index.cjs",
      activationEvents: ["onStartupFinished"],
    }),
  );
  const restrictedResult = join(work, "restricted-result.json");
  await writeFile(
    join(restrictedDriver, "index.cjs"),
    `const vscode=require('vscode');const fs=require('node:fs');exports.activate=async()=>{try{await require(${JSON.stringify(resolve(import.meta.dirname, "mcp-restricted-tests.cjs"))}).run();fs.writeFileSync(${JSON.stringify(restrictedResult)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(restrictedResult)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}};`,
  );
  await developmentHost(
    [
      ...launchArgs
        .filter(
          (arg) =>
            arg !== "--disable-workspace-trust" &&
            !arg.startsWith("--extensionDevelopmentPath="),
        )
        .map((arg) => (arg === join(work, "user") ? restrictedUser : arg)),
      `--extensionDevelopmentPath=${restrictedDriver}`,
    ],
    { ...editorEnv, CATALOG_VSCODE_DEBUG_DIR: restrictedUser },
    restrictedResult,
    45000,
  );
  console.log(
    "Actual Restricted Mode: Catalog browsing passes; MCP, Skill, Plugin and A2A consumption is refused",
  );
  await import("./lifecycle-host.mjs").then(({ lifecycle }) =>
    lifecycle({
      work,
      driver,
      launchArgs,
      editorEnv,
      developmentHost,
      cli,
      args,
      profileSettings,
      root,
    }),
  );
  const storage = join(
    work,
    "user/User/globalStorage/open-agentic-catalog.open-agentic-catalog",
  );
  const marker = join(storage, "connection-host-id");
  assert.match(
    await readFile(marker, "utf8"),
    /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/u,
  );
  assert.equal((await stat(marker)).mode & 0o777, 0o600);
  assert.ok(
    (await readdir(storage)).every(
      (name) => !name.startsWith(".connection-host-"),
    ),
  );
  console.log(
    "Packaged VSIX real-host connection and SecretStorage qualification passed",
  );
} finally {
  await localPeers.close();
  blocked.closeAllConnections();
  await new Promise((resolve) => blocked.close(resolve));
  await rm(work, {
    recursive: true,
    force: true,
    maxRetries: 5,
    retryDelay: 200,
  });
  console.log("Owned VS Code profile cleaned");
}
