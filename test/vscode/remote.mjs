import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  rm,
  copyFile,
  readdir,
} from "node:fs/promises";
import { join, resolve, dirname } from "node:path";
import { userInfo } from "node:os";
import { spawn, execFileSync } from "node:child_process";
import { createServer } from "node:net";
import { createServer as httpServer } from "node:http";
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { peers } from "./host-peers.mjs";
const { webview } = createRequire(import.meta.url)("./webview.cjs");
const root = resolve(import.meta.dirname, "../..");
const pause = () => new Promise((done) => setTimeout(done, 100));
async function waitFile(path, timeout = 30000) {
  const deadline = performance.now() + timeout;
  while (performance.now() < deadline) {
    try {
      return await readFile(path, "utf8");
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
    await pause();
  }
  throw new Error(
    "Owned remote fixture file deadline: " + path.split("/").at(-1),
  );
}
function killOwned(pid) {
  try {
    process.kill(pid, "SIGKILL");
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}
async function stop(child) {
  if (!child?.pid) return;
  for (const signal of ["SIGTERM", "SIGKILL"]) {
    try {
      process.kill(-child.pid, signal);
    } catch (error) {
      if (error.code !== "ESRCH") throw error;
    }
    await new Promise((done) => setTimeout(done, 200));
  }
}
/** Real SSH remoting and real browser dispatch, all on the owned Linux host. */
export async function qualifyRemote(parent, tools, host, vsix) {
  const reportPath = join(root, "output/vscode/remote-qualification.json");
  const packageSHA256 = createHash("sha256")
    .update(await readFile(vsix))
    .digest("hex");
  await writeFile(
    reportPath,
    JSON.stringify({
      schemaVersion: 1,
      status: "running",
      passed: false,
      cleanup: "not_confirmed",
      packageSHA256,
    }) + "\n",
  );
  const work = await mkdtemp(join(parent, "remote-")),
    issuer = await peers();
  const children = [];
  let native,
    browser,
    passed = false;
  const blocked = httpServer((_, response) => {
    response.writeHead(403);
    response.end();
  });
  blocked.on("connect", (_, socket) =>
    socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"),
  );
  await new Promise((done) => blocked.listen(0, "127.0.0.1", done));
  const proxy = `http://127.0.0.1:${blocked.address().port}`;
  try {
    const user = join(work, "ui"),
      browserProfile = join(work, "browser"),
      remoteHome = join(work, "home"),
      serverRoot = join(remoteHome, ".vscode-server"),
      bin = join(work, "bin"),
      extensions = join(work, "ui-extensions"),
      workspace = join(work, "workspace"),
      commit = "e646868227c3e3a9e3f24277c8c7220be42c0be7",
      server = join(serverRoot, "bin", commit);
    for (const directory of [
      join(user, "User"),
      join(browserProfile, "Default"),
      remoteHome,
      server,
      bin,
      extensions,
      workspace,
      join(serverRoot, "data/Machine"),
    ])
      await mkdir(directory, { recursive: true, mode: 0o700 });
    execFileSync(
      "python3",
      [
        "-c",
        "import tarfile,zipfile,sys,pathlib; tarfile.open(sys.argv[1]).extractall(sys.argv[2], filter='data'); z=zipfile.ZipFile(sys.argv[3]); z.extractall(sys.argv[4]); [(pathlib.Path(sys.argv[4])/i.filename).chmod((i.external_attr>>16)&511 or 448) for i in z.infolist() if not i.is_dir()]",
        join(tools, "server-linux-x64-1.143.1.tar.gz"),
        join(work, "server-extract"),
        join(tools, "chrome-linux64-156.0.8078.47.zip"),
        work,
      ],
      { timeout: 60000 },
    );
    const extracted = join(
      work,
      "server-extract",
      (await readdir(join(work, "server-extract")))[0],
    );
    // Rename the verified archive tree into this invocation's SSH server layout.
    await rm(server, { recursive: true });
    const { rename } = await import("node:fs/promises");
    await rename(extracted, server);
    const remoteExtensions = join(serverRoot, "extensions"),
      driver = join(remoteExtensions, "local.catalog-remote-0.0.1");
    await mkdir(driver, { recursive: true });
    await copyFile(
      resolve(import.meta.dirname, "remote-host-tests.cjs"),
      join(driver, "index.cjs"),
    );
    await copyFile(
      resolve(import.meta.dirname, "webview.cjs"),
      join(driver, "webview.cjs"),
    );
    await copyFile(
      resolve(import.meta.dirname, "renderer-errors.cjs"),
      join(driver, "renderer-errors.cjs"),
    );
    const remoteGit = join(work, "remote-tools/git"),
      uiGit = join(work, "ui-tools/git");
    await mkdir(dirname(remoteGit), { recursive: true });
    await mkdir(dirname(uiGit), { recursive: true });
    await writeFile(
      remoteGit,
      `#!/bin/sh
printf '%s\n' remote-git >> '${join(work, "remote-git.log")}'
exec /usr/bin/git "$@"
`,
      { mode: 0o700 },
    );
    await writeFile(
      uiGit,
      `#!/bin/sh
printf '%s\n' ui-git >> '${join(work, "ui-git.log")}'
exit 91
`,
      { mode: 0o700 },
    );
    await writeFile(join(work, "ui-only-sentinel"), "Preserve the UI host.\n", {
      mode: 0o600,
    });
    await writeFile(
      join(driver, "package.json"),
      JSON.stringify({
        name: "catalog-remote",
        publisher: "local",
        version: "0.0.1",
        engines: { vscode: "^1.139.1" },
        extensionKind: ["workspace"],
        activationEvents: ["onStartupFinished"],
        main: "./index.cjs",
      }),
    );
    const machine = {
      "openAgenticCatalog.packageTools": {
        gitPath: remoteGit,
        caFile: process.env.CATALOG_VSCODE_REMOTE_SOURCE_CA,
        timeoutMs: 10000,
      },
      "openAgenticCatalog.connections": [
        {
          id: "remote-skill",
          label: "Owned real Catalog Skill",
          origin: process.env.CATALOG_VSCODE_REMOTE_CATALOG,
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
        {
          id: "remote-oidc",
          label: "Owned remote OIDC",
          origin: issuer.origin,
          mode: "control",
          auth: "oidc",
          allowLoopbackHTTP: true,
          oidc: {
            issuer: issuer.origin,
            clientId: "owned-extension",
            audience: "catalog",
            scopes: ["openid", "catalog.read"],
            refresh: false,
          },
        },
      ],
    };
    await writeFile(
      join(serverRoot, "data/Machine/settings.json"),
      JSON.stringify(machine),
    );
    const key = join(work, "client-key"),
      hostKey = join(work, "host-key");
    for (const path of [key, hostKey])
      execFileSync(
        "ssh-keygen",
        ["-q", "-t", "ed25519", "-N", "", "-f", path],
        { timeout: 10000 },
      );
    const portServer = createServer();
    await new Promise((done) => portServer.listen(0, "127.0.0.1", done));
    const port = portServer.address().port;
    await new Promise((done) => portServer.close(done));
    const alias = "oac-owned-remote",
      sshConfig = join(work, "ssh-config");
    await writeFile(
      join(work, "authorized_keys"),
      await readFile(key + ".pub"),
      { mode: 0o600 },
    );
    await writeFile(
      join(work, "known_hosts"),
      `[127.0.0.1]:${port} ${(await readFile(hostKey + ".pub", "utf8")).trim()}\n`,
      { mode: 0o600 },
    );
    await writeFile(
      sshConfig,
      `Host ${alias}\n HostName 127.0.0.1\n Port ${port}\n User ${userInfo().username}\n IdentityFile ${key}\n UserKnownHostsFile ${join(work, "known_hosts")}\n IdentitiesOnly yes
  IdentityAgent none\n StrictHostKeyChecking yes\n LogLevel ERROR\n`,
      { mode: 0o600 },
    );
    const sshdConfig = join(work, "sshd-config");
    await writeFile(
      sshdConfig,
      `Port ${port}\nListenAddress 127.0.0.1\nHostKey ${hostKey}\nPidFile ${join(work, "sshd.pid")}\nAuthorizedKeysFile ${join(work, "authorized_keys")}\nAllowUsers ${userInfo().username}\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nStrictModes yes\nLogLevel ERROR\nSetEnv HOME=${remoteHome} VSCODE_AGENT_FOLDER=${serverRoot} OAC_REMOTE_FIXTURE=${work} OAC_REMOTE_ISSUER=${issuer.origin} HTTP_PROXY=${proxy} HTTPS_PROXY=${proxy} http_proxy=${proxy} https_proxy=${proxy} NO_PROXY=127.0.0.1,localhost,::1 no_proxy=127.0.0.1,localhost,::1\nSubsystem sftp internal-sftp\n`,
      { mode: 0o600 },
    );
    let diagnostics = "";
    const launch = (command, args, env) => {
      const child = spawn(command, args, {
        env,
        detached: true,
        stdio: ["ignore", "pipe", "pipe"],
      });
      children.push(child);
      for (const stream of [child.stdout, child.stderr])
        stream.on("data", (data) => {
          diagnostics = (diagnostics + data.toString()).slice(-12000);
        });
      return child;
    };
    launch("/usr/sbin/sshd", ["-D", "-e", "-f", sshdConfig], process.env);
    let ready = false;
    for (let trial = 0; trial < 30; trial++) {
      try {
        execFileSync("ssh", ["-F", sshConfig, alias, "true"], {
          stdio: "pipe",
          timeout: 1000,
        });
        ready = true;
        break;
      } catch (error) {
        diagnostics = String(error.stderr ?? error.message).slice(-2000);
        await pause();
      }
    }
    assert.ok(ready, "Owned SSH server readiness: " + diagnostics);
    const baseEnv = {
      ...process.env,
      HOME: remoteHome,
      VSCODE_AGENT_FOLDER: serverRoot,
      DONT_PROMPT_WSL_INSTALL: "1",
      HTTP_PROXY: proxy,
      HTTPS_PROXY: proxy,
      NO_PROXY: "127.0.0.1,localhost,::1",
    };
    execFileSync(
      join(server, "bin/code-server"),
      [
        "--extensions-dir",
        remoteExtensions,
        "--install-extension",
        vsix,
        "--force",
      ],
      { env: baseEnv, timeout: 60000, stdio: "pipe" },
    );
    const profile = [
      "--user-data-dir",
      user,
      "--extensions-dir",
      extensions,
      "--shared-data-dir",
      join(work, "shared"),
    ];
    execFileSync(
      join(dirname(host), "bin/code"),
      [
        ...profile,
        "--no-sandbox",
        "--install-extension",
        join(tools, "remote-ssh-0.128.0.vsix"),
        "--force",
      ],
      {
        env: { ...process.env, DONT_PROMPT_WSL_INSTALL: "1" },
        stdio: "pipe",
        timeout: 60000,
      },
    );
    const userSettings = {
      "openAgenticCatalog.packageTools": { gitPath: uiGit, timeoutMs: 10000 },
      "remote.SSH.configFile": sshConfig,
      "remote.SSH.serverInstallPath": { [alias]: remoteHome },
      "remote.SSH.remotePlatform": { [alias]: "linux" },
      "remote.SSH.useLocalServer": false,
      "remote.SSH.useExecServer": false,
      "remote.SSH.localServerDownload": "off",
      "telemetry.telemetryLevel": "off",
      "update.mode": "none",
      "extensions.autoUpdate": false,
      "extensions.autoCheckUpdates": false,
      "workbench.enableExperiments": false,
      "security.workspace.trust.enabled": false,
      "git.enabled": false,
      "git.openRepositoryInParentFolders": "never",
      "window.dialogStyle": "custom",
      "http.proxy": proxy,
      "http.proxySupport": "override",
      "http.noProxy": ["127.0.0.1", "localhost", "::1"],
    };
    await writeFile(
      join(user, "User/settings.json"),
      JSON.stringify(userSettings),
    );
    await writeFile(
      join(browserProfile, "Default/Preferences"),
      JSON.stringify({
        protocol_handler: {
          allowed_origin_protocol_pairs: { [issuer.origin]: { vscode: true } },
        },
      }),
    );
    // This helper is visible only to the two owned processes. User MIME/OS settings stay untouched.
    const dispatch = join(bin, "xdg-open");
    await writeFile(
      dispatch,
      `#!${process.execPath}\nconst fs=require('node:fs');const cp=require('node:child_process');const u=new URL(process.argv[2]);if(u.origin===${JSON.stringify(issuer.origin)}&&u.pathname==='/authorize'){fs.writeFileSync(${JSON.stringify(join(work, "browser-url"))},u.href,{mode:0o600});}else if(u.protocol==='vscode:'){fs.appendFileSync(${JSON.stringify(join(work, "browser-dispatch"))},'callback\\n',{mode:0o600});cp.execFileSync(${JSON.stringify(host)},${JSON.stringify([...profile, "--no-sandbox", "--open-url"])}.concat(u.href),{stdio:'ignore',timeout:15000,env:{...process.env,ELECTRON_RUN_AS_NODE:undefined,DONT_PROMPT_WSL_INSTALL:'1'}});}else{process.exit(2);}\n`,
      { mode: 0o700 },
    );
    const env = {
      ...process.env,
      PATH: bin + ":" + process.env.PATH,
      DBUS_SESSION_BUS_ADDRESS: undefined,
      DONT_PROMPT_WSL_INSTALL: "1",
      XDG_CONFIG_HOME: join(work, "xdg-config"),
      XDG_DATA_HOME: join(work, "xdg-data"),
      BROWSER: dispatch,
    };
    launch(
      join(work, "chrome-linux64/chrome"),
      [
        "--no-sandbox",
        "--disable-gpu",
        "--no-first-run",
        "--disable-background-networking",
        "--disable-sync",
        "--no-default-browser-check",
        `--user-data-dir=${browserProfile}`,
        "--remote-debugging-port=0",
        "--remote-debugging-address=127.0.0.1",
        `--proxy-server=${proxy}`,
        "--proxy-bypass-list=127.0.0.1;[::1]",
        issuer.origin + "/owned-browser-entry",
      ],
      env,
    );
    await waitFile(join(browserProfile, "DevToolsActivePort"));
    browser = await webview("browser", browserProfile);
    launch(
      host,
      [
        ...profile,
        "--remote",
        `ssh-remote+${alias}`,
        workspace,
        "--no-sandbox",
        "--disable-gpu",
        "--skip-welcome",
        "--skip-release-notes",
        "--disable-workspace-trust",
        "--password-store=basic",
        "--remote-debugging-port=0",
        "--remote-debugging-address=127.0.0.1",
        `--proxy-server=${proxy}`,
        "--proxy-bypass-list=127.0.0.1;[::1]",
      ],
      env,
    );
    await waitFile(join(user, "DevToolsActivePort"));
    native = await webview(true, user);
    const deadline = performance.now() + 90000;
    let navigated = false,
      consentHandled = false,
      nextProgress = performance.now() + 10000;
    for (;;) {
      try {
        const result = JSON.parse(
          await readFile(join(work, "result.json"), "utf8"),
        );
        if (!result.passed) {
          await native
            .screenshot(join(dirname(reportPath), "remote-failure-native.png"))
            .catch(() => {});
          await browser
            .screenshot(join(dirname(reportPath), "remote-failure-browser.png"))
            .catch(() => {});
        }
        assert.equal(result.passed, true, result.error);
        assert.equal(
          await readFile(join(work, "workspace/remote-owned.txt"), "utf8"),
          "Written by the real SSH extension host.\n",
        );
        assert.equal(result.remoteSkillInstalled, true);
        assert.equal(result.remoteSkillCompared, true);
        assert.equal(result.remoteSkillRestored, true);
        assert.equal(result.remoteSkillRemovedOffline, true);
        assert.equal(result.missingRemoteToolRefused, true);
        assert.match(
          await readFile(join(work, "remote-git.log"), "utf8"),
          /^(?:remote-git\n)+$/u,
        );
        await assert.rejects(readFile(join(work, "ui-git.log")), {
          code: "ENOENT",
        });
        await assert.rejects(
          readFile(join(user, ".agents/skills/owned-skill/SKILL.md")),
          { code: "ENOENT" },
        );
        assert.equal(
          await readFile(join(work, "ui-only-sentinel"), "utf8"),
          "Preserve the UI host.\n",
        );
        assert.equal(
          await readFile(join(work, "browser-dispatch"), "utf8"),
          "callback\n",
        );
        assert.equal(
          issuer.stats.tokens,
          1,
          "Wrong-session callback never contacts token endpoint; correct browser callback exchanges exactly once",
        );
        await writeFile(
          join(root, "output/vscode/remote-qualification.json"),
          JSON.stringify(
            {
              ...result,
              schemaVersion: 1,
              status: "running",
              packageSHA256,
              topology:
                "Owned loopback OpenSSH server on the same Linux host; actual Remote SSH extension and separate server process",
              browser:
                "Chrome for Testing 156.0.8078.47; owned per-origin protocol permission and invocation-owned xdg-open",
              userAssociationsChanged: false,
              cleanup: "pending",
            },
            null,
            2,
          ) + "\n",
        );
        passed = true;
        break;
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      if (performance.now() > nextProgress) {
        const controls = await native.until(
          "JSON.stringify({buttons:[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].map(b=>b.textContent),picker:document.querySelector('.quick-input-title')?.textContent,notifications:[...document.querySelectorAll('.notification-list-item-message')].map(n=>n.textContent.split('?')[0].slice(0,100))})",
        );
        console.log(
          "Remote browser stage:",
          JSON.stringify({ navigated, tokens: issuer.stats.tokens, controls }),
        );
        nextProgress = performance.now() + 10000;
      }
      if (performance.now() > deadline) {
        await native.screenshot(
          join(dirname(reportPath), "remote-failure-native.png"),
        );
        await browser.screenshot(
          join(dirname(reportPath), "remote-failure-browser.png"),
        );
        throw new Error("Remote/browser workflow deadline: " + diagnostics);
      }
      if (!navigated) {
        try {
          const authorization = await readFile(
            join(work, "browser-url"),
            "utf8",
          );
          await rm(join(work, "browser-url"));
          await browser.navigate(
            issuer.origin +
              "/owned-browser-entry?authorization=" +
              encodeURIComponent(authorization),
          );
          await browser.until("!!document.getElementById('complete-oidc')");
          await browser.click("document.getElementById('complete-oidc')");
          navigated = true;
        } catch (error) {
          if (error.code !== "ENOENT") throw error;
        }
      }
      if (!consentHandled) {
        const controls = JSON.parse(
          await native.until(
            "JSON.stringify({open:[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].some(b=>/^Open(?:$| )/.test(b.textContent))})",
          ),
        );
        if (controls.open) {
          await native.click(
            "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>/^Open(?:$| )/.test(b.textContent))",
          );
          consentHandled = true;
        }
      }
      await pause();
    }
    console.log(
      "Actual Linux SSH workspace host, remote tools/files, native SecretStorage restoration, sign-out and real browser-to-editor OIDC callback passed",
    );
  } finally {
    native?.close();
    browser?.close();
    for (const child of children.toReversed()) await stop(child);
    // The remote server detaches from SSH. Match only executable/arguments inside this invocation.
    for (const line of execFileSync("ps", ["-eo", "pid=,args="], {
      encoding: "utf8",
    }).split("\n")) {
      const match = /^\s*(\d+)\s+(.*)$/u.exec(line);
      if (
        !match ||
        Number(match[1]) === process.pid ||
        !match[2].includes(work + "/")
      )
        continue;
      killOwned(Number(match[1]));
    }
    await issuer.close();
    blocked.closeAllConnections();
    await new Promise((done) => blocked.close(done));
    await rm(work, { recursive: true, force: true });
    if (!passed)
      await writeFile(
        reportPath,
        JSON.stringify({
          schemaVersion: 1,
          status: "failed",
          passed: false,
          cleanup: "passed",
          packageSHA256,
        }) + "\n",
      );
    if (passed) {
      const path = join(root, "output/vscode/remote-qualification.json"),
        report = JSON.parse(await readFile(path, "utf8"));
      report.cleanup = "passed";
      report.status = "passed";
      await writeFile(path, JSON.stringify(report, null, 2) + "\n");
    }
  }
}
if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  await qualifyRemote(
    join(root, "output/vscode"),
    process.env.CATALOG_VSCODE_REMOTE_TOOLS,
    process.env.CATALOG_VSCODE_HOST,
    process.env.CATALOG_VSCODE_VSIX,
  );
}
