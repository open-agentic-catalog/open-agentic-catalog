const vscode = require("vscode");
const assert = require("node:assert/strict");
const pause = () => new Promise((resolve) => setTimeout(resolve, 100));
exports.qualifyMcp = async (api) => {
  const ui = await require("./webview.cjs").webview(true);
  const clickButton = async (text) => {
    const expression = `([...document.querySelectorAll('[role=dialog] .monaco-button, [role=dialog] button')].find(b=>b.textContent.includes(${JSON.stringify(text)})))`;
    try {
      await ui.until(`${expression} !== undefined`);
    } catch {
      throw new Error(
        "Missing dialog: " +
          (await ui.evaluate("document.body.innerText.slice(-8000)")),
      );
    }
    await ui.evaluate(`${expression}.click()`);
  };
  const visible = ".quick-input-widget:not([style*='display: none'])";
  const pick = async (label) => {
    await ui.until(
      `[...document.querySelectorAll("${visible} .quick-input-list .monaco-list-row")].some(r=>r.textContent.includes(${JSON.stringify(label)}))`,
    );
    for (let step = 0; step < 6; step++) {
      const active = await ui.evaluate(
        `(()=>{const e=document.querySelector("${visible} input");if(!e)return null;return document.getElementById(e.getAttribute('aria-activedescendant'))?.textContent??'';})()`,
      );
      if (active === null) throw new Error("Native picker lost focus");
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
        `(()=>{const e=document.querySelector("${visible} input");return !e || (document.getElementById(e.getAttribute('aria-activedescendant'))?.textContent??'') !== ${JSON.stringify(active)};})()`,
      );
    }
    throw new Error("Exact native picker item was not active: " + label);
  };
  const targetCredential = async (value) => {
    await ui.until(
      "document.querySelector('.quick-input-title')?.textContent.includes('MCP target:')",
    );
    await ui.evaluate(
      `(()=>{const e=document.querySelector('.quick-input-widget input');e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.key("Enter");
  };
  try {
    await api.select("qualification");
    await api.authenticate("reader");
    const page = await api.browse({ kind: "mcp_server" });
    const detail = await api.detail(page.items[0].key);
    for (const [media, suffix, secret] of [
      [
        "application/mcp-server-card+json",
        "owned_https_echo",
        "Bearer owned-mcp-secret",
      ],
      [
        "application/vnd.mcp.server+json",
        "owned_stdio_echo",
        "owned-stdio-secret",
      ],
    ]) {
      const index = detail.documents.findIndex((d) => d.mediaType === media);
      assert.ok(index >= 0);
      const selection = api.useMcp(index);
      await pick(media.includes("card") ? "Remote 1" : "Package 1");
      await clickButton("Use server");
      await selection;
      const name = media.includes("card")
        ? "com.example/owned-mcp"
        : "com.example/owned-stdio";
      assert.ok(api.mcpSelections().some((s) => s.name === name));
      assert.ok(
        !vscode.lm.tools.some((t) => t.name.includes(suffix)),
        "Selection alone must not start a server",
      );
      const start = async (expectCAFailure = false) => {
        // Native output from the preceding start can take focus while its
        // async command completes. Reopen only the non-mutating picker.
        for (let attempt = 0; attempt < 3; attempt++) {
          await vscode.commands.executeCommand(
            "workbench.action.quickOpen",
            ">MCP: List Servers",
          );
          await ui.until(
            `document.querySelector("${visible} input")?.value === '>MCP: List Servers' && [...document.querySelectorAll("${visible} .monaco-list-row")].some(r=>r.textContent.includes('List Servers'))`,
          );
          await pick("List Servers");
          try {
            await pick(name);
            break;
          } catch (error) {
            if (error.message !== "Native picker lost focus" || attempt === 2)
              throw error;
          }
        }
        let action;
        try {
          action = await ui.until(
            "[...document.querySelectorAll('.quick-input-list .monaco-list-row')].map(r=>r.textContent).find(t=>t.includes('Start Server')||t.includes('Restart Server'))",
          );
        } catch {
          throw new Error(
            "Native actions for " +
              name +
              ": " +
              (await ui.evaluate("document.body.innerText.slice(-6000)")),
          );
        }
        await pick(
          action.includes("Restart Server") ? "Restart Server" : "Start Server",
        );
        const startup = Date.now() + 20000;
        let supplied = false,
          confirmed = false;
        while (
          Date.now() < startup &&
          !vscode.lm.tools.some((t) => t.name.includes(suffix))
        ) {
          const text = await ui.evaluate(
            "document.querySelector('.monaco-dialog-box')?.textContent ?? ''",
          );
          if (text.includes("Use ") && text.includes(name)) {
            await clickButton("Use server");
            confirmed = true;
          } else if (/trust/i.test(text)) {
            assert.equal(
              await ui.evaluate(
                "(()=>{const b=[...document.querySelectorAll('[role=dialog] .monaco-button, [role=dialog] button')].find(b=>/trust/i.test(b.textContent)&&!/don't|do not/i.test(b.textContent));if(b){b.click();return true;}return false;})()",
              ),
              true,
            );
          }
          if (
            !supplied &&
            (await ui.evaluate(
              "document.querySelector('.quick-input-title')?.textContent.includes('MCP target:') ?? false",
            ))
          ) {
            await targetCredential(secret);
            supplied = true;
          }
          if (
            expectCAFailure &&
            /self\s+signed\s+certificate/iu.test(
              await ui.evaluate("document.body.innerText"),
            )
          )
            break;
          await pause();
        }
        assert.ok(confirmed, "Provider resolution must require a fresh review");
        assert.ok(supplied, "Target credential prompt was not shown");
        const tool = vscode.lm.tools.find((t) => t.name.includes(suffix));
        if (expectCAFailure) {
          assert.equal(tool, undefined);
          assert.match(
            await ui.evaluate("document.body.innerText"),
            /self\s+signed\s+certificate/iu,
          );
        } else
          assert.ok(
            tool,
            `Native MCP tool missing: ${suffix}; UI: ${await ui.evaluate("document.body.innerText.slice(-5000)")}`,
          );
        return tool;
      };
      if (media.includes("card")) {
        await start(true);
        // Test-process trust only; no system CA changes and no TLS bypass.
        const tls = require("node:tls"),
          fs = require("node:fs");
        tls.setDefaultCACertificates([
          ...tls.rootCertificates,
          fs.readFileSync(process.env.CATALOG_VSCODE_PUBLIC_CA, "utf8"),
        ]);
        await vscode.workspace
          .getConfiguration("http")
          .update(
            "systemCertificates",
            false,
            vscode.ConfigurationTarget.Global,
          );
      }
      const tool = await start();
      const result = await vscode.lm.invokeTool(tool.name, {
        input: {},
        toolInvocationToken: undefined,
      });
      assert.ok(
        result.content.some(
          (c) =>
            c instanceof vscode.LanguageModelTextPart &&
            c.value.includes("native invocation"),
        ),
      );
      const selected = api.mcpSelections().find((s) => s.name === name);
      if (media.includes("card")) {
        const forgetting = vscode.commands.executeCommand(
          "openAgenticCatalog.forgetMcpCredentials",
        );
        await clickButton("Other windows closed: forget");
        await forgetting;
        await ui.until(
          "document.body.innerText.includes('Forgot 1 MCP credential entries in this batch.')",
        );
        assert.equal(
          (await api.test()).identity.subject,
          "reader",
          "MCP forgetting preserves Catalog login",
        );
        assert.ok(api.mcpSelections().some((s) => s.id === selected.id));
      }
      await api.removeMcp(selected.id);
      assert.ok(!api.mcpSelections().some((s) => s.id === selected.id));
    }
    await api.signOut();
    assert.deepEqual(api.mcpSelections(), []);
    console.log(
      "Actual native MCP HTTPS and offline npx startup, target credentials and host-mediated tools passed",
    );
  } finally {
    ui.close();
  }
};
