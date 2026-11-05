const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async (api) => {
  const ui = await require("./webview.cjs").webview(true);
  const options = JSON.parse(process.env.CATALOG_VSCODE_RUNNERS);
  const note = (text) =>
    require("node:fs").appendFileSync(
      require("node:path").join(
        process.env.CATALOG_VSCODE_DEBUG_DIR,
        "runners-progress.log",
      ),
      `${options.missing ? "missing" : "normal"}: ${text}\n`,
    );
  const visible = ".quick-input-widget:not([style*='display: none'])";
  const pick = async (label) => {
    await ui.until(
      `[...document.querySelectorAll(${JSON.stringify(visible + " .quick-input-list .monaco-list-row")})].some(r=>r.textContent.includes(${JSON.stringify(label)}))`,
    );
    for (let step = 0; step < 6; step++) {
      const active = await ui.evaluate(
        `(()=>{const input=document.querySelector(${JSON.stringify(visible + " input")});if(!input)return null;return document.getElementById(input.getAttribute('aria-activedescendant'))?.textContent??'';})()`,
      );
      if (active === null) throw new Error("Native runner picker lost focus");
      if (active.includes(label)) {
        await vscode.commands.executeCommand(
          "workbench.action.acceptSelectedQuickOpenItem",
        );
        return;
      }
      await vscode.commands.executeCommand(
        "workbench.action.quickOpenSelectNext",
      );
      await ui.until(
        `(()=>{const input=document.querySelector(${JSON.stringify(visible + " input")});return !input || (document.getElementById(input.getAttribute('aria-activedescendant'))?.textContent??'') !== ${JSON.stringify(active)};})()`,
      );
    }
    throw new Error("Exact native runner picker item was not active: " + label);
  };
  const consent = async (label) => {
    const button = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(label)})`;
    await ui.until(`!!(${button})`);
    await ui.click(button);
  };
  const start = async (id) => {
    // Native output can take focus while the preceding start finishes.
    // Reopen only the read-only server picker, never a start or consent action.
    for (let attempt = 0; attempt < 3; attempt++) {
      await vscode.commands.executeCommand(
        "workbench.action.quickOpen",
        ">MCP: List Servers",
      );
      await pick("List Servers");
      try {
        await pick(id.slice(0, 8));
        break;
      } catch (error) {
        if (
          error.message !== "Native runner picker lost focus" ||
          attempt === 2
        )
          throw error;
      }
    }
    const actions = await ui.until(
      `[...document.querySelectorAll(${JSON.stringify(visible + " .quick-input-list .monaco-list-row")})].map(r=>r.textContent).find(t=>t.includes('Start Server')||t.includes('Restart Server'))`,
    );
    await pick(
      actions.includes("Restart Server") ? "Restart Server" : "Start Server",
    );
  };
  const originalPath = process.env.PATH;
  process.env.PATH = options.missing ? options.missingBin : options.runnerPath;
  const originalHome = process.env.HOME;
  process.env.HOME = options.fixtureHome;
  try {
    await api.select("runners");
    const page = await api.browse({ text: "owned" });
    const detail = await api.detail(page.items[0].key);
    const index = detail.documents.findIndex(
      (d) => d.mediaType === "application/vnd.mcp.server+json",
    );
    assert.ok(index >= 0);
    for (const [alternative, suffix] of [
      ["Package 1", "owned_uvx_echo"],
      ["Package 2", "owned_docker_echo"],
      ...(options.missing ? [["Package 4", "owned_stdio_echo"]] : []),
    ]) {
      note(alternative + " select");
      const selecting = api.useMcp(index);
      await pick(alternative);
      await consent("Use server");
      await selecting;
      const selection = api.mcpSelections()[0];
      assert.ok(selection);
      assert.ok(
        !vscode.lm.tools.some((t) => t.name.includes(suffix)),
        "Selection alone never starts a runner",
      );
      const beforeStarts = await fetch(
        options.peer + "/owned-runner-stats",
      ).then((r) => r.json());
      let beforeUv = "";
      try {
        beforeUv = require("node:fs").readFileSync(options.uvxSentinel, "utf8");
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      await vscode.commands.executeCommand("notifications.clearAll");
      await start(selection.id);
      await ui.until(
        "document.querySelector('.monaco-dialog-box')?.textContent.includes('Use io.example/owned-runners')",
      );
      await consent("Cancel");
      await ui.until("document.body.innerText.includes('mcp_start_cancelled')");
      note(alternative + " provider cancellation observed");
      await vscode.commands.executeCommand("notifications.clearAll");
      assert.deepEqual(
        await fetch(options.peer + "/owned-runner-stats").then((r) => r.json()),
        beforeStarts,
      );
      let afterUv = "";
      try {
        afterUv = require("node:fs").readFileSync(options.uvxSentinel, "utf8");
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      assert.equal(afterUv, beforeUv);
      assert.ok(
        !vscode.lm.tools.some((t) => t.name.includes(suffix)),
        "Canceled native preparation exposes no tools",
      );
      await start(selection.id);
      await consent("Use server");
      note(alternative + " native start consent");
      if (options.missing) {
        await ui.until(
          `document.body.innerText.includes(${JSON.stringify(alternative === "Package 1" ? "uvx" : alternative === "Package 2" ? "docker" : "npx")}) && /ENOENT|not found|not available|could not find|not installed/i.test(document.body.innerText)`,
        );
        assert.ok(!vscode.lm.tools.some((t) => t.name.includes(suffix)));
      } else {
        const deadline = performance.now() + 20000;
        while (
          !vscode.lm.tools.some((t) => t.name.includes(suffix)) &&
          performance.now() < deadline
        ) {
          const trust = await ui.evaluate(
            "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].map(b=>b.textContent.trim()).find(t=>/trust/i.test(t)&&!/don't|do not/i.test(t))",
          );
          if (trust) await consent(trust);
          await new Promise((done) => setTimeout(done, 100));
        }
        const tool = vscode.lm.tools.find((t) => t.name.includes(suffix));
        assert.ok(
          tool,
          `Real native ${alternative} runner tool did not appear: ${await ui.evaluate("document.body.innerText.slice(-6000)")}`,
        );
        const result = await vscode.lm.invokeTool(tool.name, {
          input: {},
          toolInvocationToken: undefined,
        });
        assert.ok(
          result.content.some(
            (c) =>
              c instanceof vscode.LanguageModelTextPart &&
              c.value.includes(
                "native invocation; literal arguments preserved",
              ),
          ),
        );
      }
      note(alternative + " complete");
      await api.removeMcp(selection.id);
    }
    const refused = api.useMcp(index);
    await pick("Package 3");
    await assert.rejects(refused, /unsupported_mcp_recipe/u);
    assert.deepEqual(api.mcpSelections(), []);
    console.log(
      options.missing
        ? "Native MCP missing uvx/Docker prerequisites and refused declarations passed"
        : "Native MCP real offline uvx and owned Docker: inert selection, cancellation, literal argv, tool visibility/invocation and refused declaration passed",
    );
  } finally {
    if (originalPath === undefined) delete process.env.PATH;
    else process.env.PATH = originalPath;
    if (originalHome === undefined) delete process.env.HOME;
    else process.env.HOME = originalHome;
    ui.close();
  }
};
