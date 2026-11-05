const vscode = require("vscode");
const assert = require("node:assert/strict");
const { readFile, writeFile, mkdir, rm } = require("node:fs/promises");
const { join, resolve } = require("node:path");
exports.run = async () => {
  const extension = process.env.CATALOG_VSCODE_SOURCE_EXTENSION;
  const work = join(process.env.CATALOG_VSCODE_DEBUG_DIR, "owned-debug");
  const trigger = join(work, "trigger");
  await mkdir(trigger, { recursive: true });
  await writeFile(
    join(trigger, "package.json"),
    JSON.stringify({
      name: "catalog-debug-trigger",
      publisher: "local",
      version: "0.0.1",
      engines: { vscode: "^1.139.1" },
      activationEvents: ["onStartupFinished"],
      main: "index.cjs",
    }),
  );
  const ready = join(work, "ready");
  const helpResult = join(work, "source-help.json");
  await writeFile(
    join(trigger, "index.cjs"),
    `exports.activate=()=>{void(async()=>{const fs=require('node:fs');try{while(!fs.existsSync(${JSON.stringify(ready)}))await new Promise(r=>setTimeout(r,50));const vscode=require('vscode');await vscode.commands.executeCommand('openAgenticCatalog.open');await vscode.commands.executeCommand('openAgenticCatalog.help','start');process.env.CATALOG_VSCODE_DEBUG_DIR=${JSON.stringify(join(work, "user"))};await require(${JSON.stringify(join(__dirname, "help-host-tests.cjs"))}).verify();if(!vscode.window.tabGroups.all.some(g=>g.tabs.some(t=>t.label.includes('README.md'))))throw Error('Source Help tab missing');fs.writeFileSync(${JSON.stringify(helpResult)},JSON.stringify({passed:true}));}catch(e){fs.writeFileSync(${JSON.stringify(helpResult)},JSON.stringify({passed:false,error:String(e.stack??e)}));}})();};`,
  );

  const configuration = JSON.parse(
    await readFile(join(extension, ".vscode/launch.json"), "utf8"),
  ).configurations[0];
  const source = join(extension, "src/extension.ts");
  const line =
    (await readFile(source, "utf8"))
      .split("\n")
      .findIndex((text) => text.includes("const session = new Session")) + 1;
  const progress = (stage) =>
    require("node:fs").appendFileSync(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "debug-progress.log"),
      stage + "\n",
    );
  progress("starting owned debug case");
  const bounded = async (promise, stage) => {
    let timer;
    try {
      return await Promise.race([
        promise,
        new Promise((_, reject) => {
          timer = setTimeout(
            () => reject(new Error("Debug deadline: " + stage)),
            20000,
          );
        }),
      ]);
    } finally {
      clearTimeout(timer);
    }
  };
  const messages = [];
  let session;
  const sessions = [];
  const tracker = vscode.debug.registerDebugAdapterTrackerFactory("*", {
    createDebugAdapterTracker: (value) => ({
      onDidSendMessage: (message) =>
        messages.push({ ...message, ownedSession: value.id }),
    }),
  });
  const started = vscode.debug.onDidStartDebugSession((value) => {
    sessions.push(value);
    if (value.name === "Catalog source-map qualification") session = value;
  });
  async function until(predicate) {
    const deadline = performance.now() + 20000;
    while (performance.now() < deadline) {
      const result = await predicate();
      if (result) return result;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error(
      "Source-map debug deadline: " +
        JSON.stringify(
          messages
            .filter(
              (m) =>
                m.type === "event" &&
                m.event !== "loadedSource" &&
                m.event !== "output",
            )
            .map((m) => ({ event: m.event, reason: m.body?.reason })),
        ),
    );
  }
  const { spawn } = require("node:child_process");
  const net = require("node:net");
  const listener = net.createServer();
  await new Promise((done) => listener.listen(0, "127.0.0.1", done));
  const port = listener.address().port;
  await new Promise((done) => listener.close(done));
  const child = spawn(
    "python3",
    [
      "-B",
      "-c",
      "import importlib.util,sys;from pathlib import Path;spec=importlib.util.spec_from_file_location('vscode_debug',sys.argv[1]);owner=importlib.util.module_from_spec(spec);spec.loader.exec_module(owner);owner.launch_editor(sys.argv[2],owner.installed(),Path(sys.argv[3]),port=int(sys.argv[4]),extra_args=sys.argv[5:])",
      join(extension, "tooling/runner.py"),
      process.execPath,
      work,
      String(port),
      `--extensionDevelopmentPath=${trigger}`,
      "--no-sandbox",
      "--disable-gpu",
      "--skip-welcome",
      "--skip-release-notes",
      "--disable-workbench-experiments",
      "--disable-workspace-trust",
      "--password-store=basic",
      "--remote-debugging-port=0",
      "--remote-debugging-address=127.0.0.1",
    ],
    {
      env: {
        ...process.env,
        ELECTRON_RUN_AS_NODE: undefined,
        DONT_PROMPT_WSL_INSTALL: "1",
      },
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let diagnostics = "";
  for (const stream of [child.stdout, child.stderr])
    stream.on("data", (chunk) => {
      diagnostics = (diagnostics + chunk.toString()).slice(-12000);
    });
  const exited = new Promise((done) => child.once("exit", done));
  async function stopChild() {
    for (const signal of ["SIGTERM", "SIGKILL"]) {
      try {
        process.kill(-child.pid, signal);
      } catch (error) {
        if (error.code !== "ESRCH") throw error;
      }
      await Promise.race([
        exited,
        new Promise((done) => setTimeout(done, 1000)),
      ]);
    }
    started.dispose();
    tracker.dispose();
    await rm(work, {
      recursive: true,
      force: true,
      maxRetries: 5,
      retryDelay: 200,
    });
  }
  try {
    const deadline = performance.now() + 20000;
    let listening = false;
    while (performance.now() < deadline) {
      try {
        const response = await fetch(`http://127.0.0.1:${port}/json/list`, {
          signal: AbortSignal.timeout(1000),
        });
        if (response.ok) {
          listening = true;
          break;
        }
      } catch {
        /* Child inspector is not ready. */
      }
      if (child.exitCode !== null)
        throw new Error("Owned development editor exited: " + diagnostics);
      await new Promise((done) => setTimeout(done, 50));
    }
    assert.ok(
      listening,
      "Owned development inspector did not start: " + diagnostics,
    );
    progress("inspector ready; attaching standard debugger");
    const launched = await bounded(
      vscode.debug.startDebugging(vscode.workspace.workspaceFolders?.[0], {
        ...configuration,
        name: "Catalog source-map qualification",
        port,
        outFiles: configuration.outFiles.map((path) =>
          path.replaceAll("${workspaceFolder}", extension),
        ),
      }),
      "attach",
    );
    progress("debugger attached");
    assert.equal(launched, true);
    await until(() => session);
    const parentSession = session;
    session = await until(() =>
      sessions.find((value) => value.parentSession?.id === parentSession.id),
    );
    progress("selecting target and setting TypeScript breakpoint");
    await bounded(
      session.customRequest("setBreakpoints", {
        source: { path: source },
        breakpoints: [{ line }],
      }),
      "set breakpoint",
    );
    progress("activating development extension");
    await writeFile(ready, "activate");
    const stopped = await until(() =>
      messages.find(
        (m) => m.event === "stopped" && m.body.reason === "breakpoint",
      ),
    );
    const stack = await bounded(
      session.customRequest("stackTrace", {
        threadId: stopped.body.threadId,
        startFrame: 0,
        levels: 1,
      }),
      "stack trace",
    );
    assert.equal(resolve(stack.stackFrames[0].source.path), resolve(source));
    assert.equal(stack.stackFrames[0].line, line);
    await writeFile(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "debug-source-map.json"),
      JSON.stringify({
        status: "passed",
        source: "src/extension.ts",
        line,
        engine: vscode.version,
      }) + "\n",
    );
    await session.customRequest("continue", {
      threadId: stopped.body.threadId,
    });
    const help = await until(async () => {
      try {
        return JSON.parse(await readFile(helpResult, "utf8"));
      } catch {
        return undefined;
      }
    });
    assert.equal(help.passed, true, help.error);
    progress("source Help, offline anchors and all four images passed");
  } finally {
    try {
      await writeFile(
        join(
          process.env.CATALOG_VSCODE_SCREENSHOTS,
          "debug-dap-" + require("node:crypto").randomUUID() + ".json",
        ),
        JSON.stringify(
          {
            sessions: sessions.map((value) => ({
              id: value.id,
              name: value.name,
              type: value.type,
              parent: value.parentSession?.id,
            })),
            messages: messages.slice(-300),
          },
          null,
          2,
        ),
      );
      for (const owned of sessions.toReversed())
        await bounded(vscode.debug.stopDebugging(owned), "stop debugger");
    } finally {
      await stopChild();
    }
  }
};
