const vscode = require("vscode");
const assert = require("node:assert/strict");
const { join } = require("node:path");
const { webview } = require("./webview.cjs");
exports.run = async () => {
  const api = await vscode.extensions
    .getExtension("open-agentic-catalog.open-agentic-catalog")
    .activate();
  const native = await webview(true);
  let page, chat;
  const button = (label) =>
    `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(label)})`;
  const consent = async (label) => {
    await native.until(`!!(${button(label)})`);
    await native.click(button(label));
  };
  const pick = async (title, label) => {
    await native.until(
      `(()=>{const widget=document.querySelector('.quick-input-widget:not([style*="display: none"])'),input=widget?.querySelector('input'),active=input&&document.getElementById(input.getAttribute('aria-activedescendant'));return widget?.getBoundingClientRect().width>0 && widget.querySelector('.quick-input-title')?.textContent.includes(${JSON.stringify(title)}) && active?.textContent.includes(${JSON.stringify(label)});})()`,
    );
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
  };
  try {
    await api.select("sample");
    await api.authenticate("reader");
    const all = await api.browse({});
    assert.deepEqual(all.items.map((i) => i.kind).sort(), [
      "agent",
      "mcp_server",
      "plugin",
      "skill",
    ]);
    assert.ok(
      all.items.every(
        (i) => !i.name.includes("Owned") && !i.description.includes("<script>"),
      ),
    );
    const agent = all.items.find((i) => i.kind === "agent");
    await api.detail(agent.key);
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    await vscode.commands.executeCommand(
      "workbench.view.extension.openAgenticCatalog",
    );
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    await vscode.commands.executeCommand("openAgenticCatalog.refresh");
    await api.detail(agent.key);
    for (const command of [
      "workbench.action.closePanel",
      "workbench.action.closeAuxiliaryBar",
      "notifications.clearAll",
    ])
      await vscode.commands.executeCommand(command);
    page = await webview();
    await page.until(
      "document.body.innerText.includes('Hello World Agent') && document.body.innerText.includes('Documents and package')",
    );
    await native.until(
      "document.body.innerText.includes('4 shown on this page; 4 matches') && !document.body.innerText.includes('Add or select a Catalog connection to begin.')",
    );
    for (const label of [
      "Combine filters",
      "My ratings",
      "My private feedback",
      "Rate this release",
      "Private item feedback",
    ])
      await page.until(
        `[...document.querySelectorAll('button')].some(button=>button.textContent===${JSON.stringify(label)})`,
      );
    // Open only a native disclosure; never rewrite product text or layout for capture.
    await page.evaluate("document.querySelector('main').scrollIntoView()");
    await native.screenshot(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "catalog-preview.png"),
    );
    page.close();
    page = undefined;
    const detail = api.browsing().detail;
    const selected = detail.documents.findIndex(
      (d) => d.mediaType === "application/a2a+json",
    );
    const opening = api.talkAgent(selected >= 0 ? selected : 0);
    await pick("Choose one A2A interface", "JSONRPC 1.0:");
    await pick("Choose A2A target authentication", "Anonymous");
    await consent("Open conversation");
    await opening;
    chat = await webview();
    await chat.until(
      "document.getElementById('prompt') && document.body.innerText.includes('Status: ready')",
    );
    await chat.evaluate(
      "(()=>{const p=document.getElementById('prompt');p.value='Hello!';p.dispatchEvent(new Event('input',{bubbles:true}));})()",
    );
    await chat.click("document.getElementById('stream-reply')");
    await chat.until("document.getElementById('stream-reply').checked");
    await chat.click(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt')",
    );
    await consent("Send prompt");
    await chat.until(
      "document.body.innerText.includes('Hello World! Nice to meet you!') && document.body.innerText.includes('Status: completed')",
    );
    assert.equal(api.a2aState().status, "completed");
    await vscode.commands.executeCommand("notifications.clearAll");
    await native.screenshot(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "agent-conversation.png"),
    );
    const seen = await new Promise((resolve, reject) => {
      const request = require("node:https").get(
        process.env.CATALOG_VSCODE_A2A + "/__owned_stats",
        {
          ca: require("node:fs").readFileSync(
            process.env.CATALOG_VSCODE_PUBLIC_CA,
          ),
        },
        (response) => {
          let body = "";
          response.on("data", (chunk) => {
            body += chunk;
            if (body.length > 65536) request.destroy(Error("Stats body limit"));
          });
          response.on("end", () => {
            try {
              assert.equal(response.statusCode, 200);
              resolve(JSON.parse(body));
            } catch (error) {
              reject(error);
            }
          });
          response.on("error", reject);
        },
      );
      request.setTimeout(5000, () => request.destroy(Error("Stats deadline")));
      request.on("error", reject);
    });
    assert.equal(seen.length, 1);
    assert.equal(seen[0].request.params.message.parts[0].text, "Hello!");
    chat.close();
    chat = undefined;
    const mcp = all.items.find((item) => item.kind === "mcp_server");
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    const mcpDetail = await api.detail(mcp.key);
    const mcpView = await webview();
    await mcpView.until(
      "[...document.querySelectorAll('h2')].some(h=>h.textContent.includes('Text tools MCP Server'))",
    );
    mcpView.close();
    const mcpIndex = mcpDetail.documents.findIndex(
      (d) => d.mediaType === "application/mcp-server-card+json",
    );
    assert.ok(mcpIndex >= 0);
    const selectionOutcome = { state: "pending" };
    let selection = api.useMcp(mcpIndex).then(
      () => {
        selectionOutcome.state = "complete";
      },
      (error) => {
        selectionOutcome.state = String(error.code ?? error.message);
        throw error;
      },
    );
    void selection.catch(() => {});
    await pick("Choose one declared MCP connection or package", "Remote 1");
    try {
      await native.until(`!!(${button("Use server")})`);
    } catch (error) {
      throw new Error(
        error.message + " outcome=" + JSON.stringify(selectionOutcome),
        { cause: error },
      );
    }
    await native.until(
      "document.body.innerText.includes('VS Code owns target TLS and OAuth') && document.body.innerText.includes('Streamable HTTP: https://127.0.0.1:')",
    );
    await native.screenshot(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "mcp-selection-preview.png"),
    );
    assert.equal(api.mcpSelections().length, 0);
    await consent("Use server");
    await selection;
    assert.equal(api.mcpSelections().length, 1);
    const plugin = all.items.find((item) => item.kind === "plugin");
    await api.detail(plugin.key);
    const workspace = vscode.workspace.workspaceFolders[0].uri.fsPath;
    const installation = api.installPlugin(0, workspace);
    await consent("Install Plugin disabled");
    await installation;
    assert.deepEqual(await api.installedPlugins(), [
      { name: "review.toolkit", workspace, enabled: false },
    ]);
    await vscode.commands.executeCommand("workbench.action.closeActiveEditor");
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    await api.detail(plugin.key);
    const pluginView = await webview();
    await pluginView.until(
      "[...document.querySelectorAll('h2')].some(h=>h.textContent.includes('Review toolkit Plugin'))",
    );
    pluginView.close();
    await vscode.commands.executeCommand("notifications.clearAll");
    await vscode.commands.executeCommand("openAgenticCatalog.refreshTools");
    await vscode.commands.executeCommand(
      "workbench.view.extension.openAgenticCatalog",
    );
    await native.until(
      "document.body.innerText.includes('MCP · selected') && document.body.innerText.includes('Plugin · installed disabled')",
    );
    await native.until(
      "!document.body.innerText.includes('Verify and install disabled Catalog Plugin')",
    );
    await vscode.commands.executeCommand("notifications.clearAll");
    const pluginRow =
      "[...document.querySelectorAll('.monaco-list-row')].find(row=>row.textContent.includes('review.toolkit') && row.textContent.includes('installed disabled'))";
    await native.click(pluginRow, "right");
    await native.until(
      "document.body.innerText.includes('Disable Installed Plugin') && document.body.innerText.includes('Remove Installed Plugin')",
    );
    await native.until(
      "[...document.querySelectorAll('.context-view, .monaco-menu-container')].filter(e=>e.getBoundingClientRect().height>0).every(e=>Number(getComputedStyle(e).opacity)===1)",
    );
    assert.ok(
      !(await native.evaluate("document.body.innerText")).includes("/home/"),
    );
    await native.screenshot(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "my-tools-installed.png"),
    );
    await native.key("Escape");
  } catch (error) {
    await native.screenshot(
      join(process.env.CATALOG_VSCODE_SCREENSHOTS, "failed-capture.png"),
    );
    throw new Error(
      error.message + " state=" + JSON.stringify(api.a2aState()),
      { cause: error },
    );
  } finally {
    page?.close();
    chat?.close();
    native.close();
    await api.signOut();
  }
};
