const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async (api) => {
  const note = (text) =>
    require("node:fs").appendFileSync(
      require("node:path").join(
        process.env.CATALOG_VSCODE_DEBUG_DIR,
        "capabilities-progress.log",
      ),
      text + "\n",
    );
  note("start fresh capability window");
  const ui = await require("./webview.cjs").webview(true);
  const title = (text) =>
    `document.querySelector('.quick-input-widget')?.offsetParent!==null && document.querySelector('.quick-input-title')?.textContent.includes(${JSON.stringify(text)})`;
  const input = async (text, value) => {
    await ui.until(title(text));
    await ui.evaluate(
      `(()=>{const input=document.querySelector('.quick-input-widget input');input.value=${JSON.stringify(value)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.key("Enter");
  };
  const focus = async (many = false) => {
    await ui.click("document.querySelector('.quick-input-widget input')");
    await ui.until(
      `document.activeElement===document.querySelector('.quick-input-widget input') && ${many ? "document.querySelectorAll('.quick-input-list .monaco-list-row').length>0" : "!!document.querySelector('.quick-input-list .monaco-list-row.focused')"}`,
    );
  };
  const first = async (text, many = false) => {
    await ui.until(title(text));
    await focus(many);
    await ui.key("Enter");
  };
  const pick = async (text, label) => {
    await ui.until(title(text));
    await ui.evaluate(
      `(()=>{const input=document.querySelector('.quick-input-widget input');input.value=${JSON.stringify(label)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.until(
      `[...document.querySelectorAll('.quick-input-list .monaco-list-row')].some(row=>row.textContent.includes(${JSON.stringify(label)}))`,
    );
    await focus();
    await ui.until(
      `document.querySelector('.quick-input-list .monaco-list-row.focused')?.textContent.includes(${JSON.stringify(label)})`,
    );
    await ui.key("Enter");
  };
  const consent = async (text) => {
    const button = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(button=>button.textContent.trim()===${JSON.stringify(text)})`;
    await ui.until(`!!(${button})`);
    await ui.click(button);
  };
  const toggle = async (text, label) => {
    await ui.until(title(text));
    const checkbox = `[...document.querySelectorAll('.quick-input-list .monaco-list-row')].find(row=>row.textContent.trim()===${JSON.stringify(label)})?.querySelector('[role=checkbox],input[type=checkbox]')`;
    await ui.until(`!!(${checkbox})`);
    await ui.click(checkbox);
    await ui.until(
      `(${checkbox})?.getAttribute('aria-checked')==='true' || (${checkbox})?.checked===true`,
    );
  };
  const values = async (text, name, entries) => {
    for (const value of entries) {
      await pick(text, "Add exact value");
      await input(`Add ${name}`, value);
    }
    await pick(text, JSON.stringify("missing"));
    await pick(text, "Use these values");
  };
  try {
    await api.select("qualification");
    await api.authenticate("reader");
    const page = await api.browse({ kind: "agent" });
    await api.detail(page.items[0].key);
    assert.deepEqual(
      api.mcpSelections(),
      [],
      "Reload never restores executable definitions",
    );
    let restored = vscode.commands.executeCommand(
      "openAgenticCatalog.savedMcp",
    );
    await pick("Saved inert MCP references", '"origin":"local"');
    await pick("Saved MCP reference", "Reselect with fresh access and consent");
    await consent("Use server");
    await restored;
    assert.equal(
      api.mcpSelections().length,
      1,
      "Explicit saved reselection uses fresh access/evidence and consent",
    );
    await api.removeMcp(api.mcpSelections()[0].id);
    restored = vscode.commands.executeCommand(
      "openAgenticCatalog.forgetMcpReferences",
    );
    await consent("Forget MCP references");
    await restored;
    note("combined native filters");
    const combined = vscode.commands.executeCommand(
      "openAgenticCatalog.combinedFilters",
    );
    await toggle("Search origins", "indexed");
    await first("Search origins", true);
    await toggle("Resource kinds", "mcp_server");
    await first("Resource kinds", true);
    await values("Tags (all must match)", "tag", ["owned", "missing"]);
    await values("Categories (all must match)", "category", [
      "tools",
      "missing",
    ]);
    await combined;
    assert.deepEqual(api.browsing().query.origins, ["local", "indexed"]);
    assert.deepEqual(api.browsing().query.kinds, ["agent", "mcp_server"]);
    assert.deepEqual(api.browsing().query.tags, ["owned"]);
    assert.deepEqual(api.browsing().query.categories, ["tools"]);
    assert.ok(
      api.browsing().page.items.length > 0,
      "Finishing the wizard submits the actual combined search",
    );
    const beforeCancel = structuredClone(api.browsing().query);
    const cancelled = vscode.commands.executeCommand(
      "openAgenticCatalog.combinedFilters",
    );
    await toggle("Search origins", "live");
    await first("Search origins", true);
    await first("Resource kinds", true);
    await ui.until(title("Tags (all must match)"));
    await ui.key("Escape");
    await cancelled;
    assert.deepEqual(
      api.browsing().query,
      beforeCancel,
      "Cancel does not apply partial choices or initiate a live search",
    );
    const local = await api.browse({ kind: "agent" });
    await api.detail(local.items[0].key);
    note("rating submit");
    let operation = vscode.commands.executeCommand("openAgenticCatalog.rate");
    await first("Your personal rating");
    await first("Rating sharing");
    await operation;
    operation = vscode.commands.executeCommand("openAgenticCatalog.myRatings");
    await pick("My ratings", "active");
    await pick("Manage my rating", "Edit");
    await pick("Your personal rating", "5 / 5");
    await pick("Rating sharing", "Allow publisher summary");
    await operation;
    operation = vscode.commands.executeCommand("openAgenticCatalog.myRatings");
    await pick("My ratings", "active");
    await pick("Manage my rating", "Read current item");
    await operation;
    let retained = JSON.parse(
      vscode.window.activeTextEditor.document.getText(),
    );
    assert.equal(retained.score, 5);
    assert.equal(retained.consent, true);
    operation = vscode.commands.executeCommand("openAgenticCatalog.myRatings");
    await pick("My ratings", "active");
    await pick("Manage my rating", "Withdraw");
    await consent("Withdraw");
    await operation;
    note("feedback submit");
    operation = vscode.commands.executeCommand("openAgenticCatalog.feedback");
    await first("Private feedback category");
    await input("Private item feedback", "Native editor private feedback");
    await operation;
    operation = vscode.commands.executeCommand("openAgenticCatalog.myFeedback");
    await pick("My private feedback", "problem");
    await pick("Manage my private feedback", "Read current item");
    await operation;
    assert.ok(
      vscode.workspace.textDocuments.some(
        (document) =>
          document.uri.scheme === "oac-evidence" &&
          document.getText().includes("Native editor private feedback"),
      ),
      "Native own-feedback command displays the retained private record",
    );
    operation = vscode.commands.executeCommand("openAgenticCatalog.myFeedback");
    await pick("My private feedback", "problem");
    await pick("Manage my private feedback", "Edit");
    await pick("Private feedback category", "suggestion");
    await input("Edit feedback", "Edited native private feedback");
    await operation;
    operation = vscode.commands.executeCommand("openAgenticCatalog.myFeedback");
    await pick("My private feedback", "suggestion");
    await pick("Manage my private feedback", "Read current item");
    await operation;
    retained = JSON.parse(vscode.window.activeTextEditor.document.getText());
    assert.equal(retained.comment, "Edited native private feedback");
    assert.equal(retained.category, "suggestion");
    assert.equal(retained.state, "open");
    operation = vscode.commands.executeCommand("openAgenticCatalog.myFeedback");
    await pick("My private feedback", "suggestion");
    await pick("Manage my private feedback", "Withdraw");
    await consent("Withdraw");
    await operation;
    operation = vscode.commands.executeCommand(
      "openAgenticCatalog.recoverOpinion",
    );
    await operation;
    note("package credentials");
    for (const action of ["Add or replace", "Add or replace", "Forget"]) {
      operation = vscode.commands.executeCommand(
        "openAgenticCatalog.packageCredentials",
      );
      await first("Package credential source");
      await input(
        "Exact package HTTPS origin",
        process.env.CATALOG_VSCODE_PACKAGE_PEER,
      );
      await pick("Git over HTTPS", action);
      if (action !== "Forget") {
        await input("Package username", "package-user");
        await input(
          "Package password or token",
          action === "Forget" ? "unused" : "package-secret",
        );
      }
      await operation;
    }
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    console.log(
      "Installed editor: personal rating/withdraw, private feedback/read/withdraw and native package credential add/replace/forget passed",
    );
  } catch (error) {
    note("failure: " + String(error.message));
    note(
      await ui.evaluate(
        "JSON.stringify({title:document.querySelector('.quick-input-title')?.textContent,visible:document.querySelector('.quick-input-widget')?.offsetParent!==null,focused:document.activeElement?.className,rows:[...document.querySelectorAll('.quick-input-list .monaco-list-row')].map(row=>({text:row.textContent,focused:row.classList.contains('focused')})),buttons:[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].map(button=>button.textContent),notifications:[...document.querySelectorAll('.notification-list-item-message')].map(message=>message.textContent)})",
      ),
    );
    const screenshotDirectory = require("node:path").resolve(
      __dirname,
      "../../output/vscode/screenshots",
    );
    require("node:fs").mkdirSync(screenshotDirectory, { recursive: true });
    await ui.screenshot(
      require("node:path").join(
        screenshotDirectory,
        "capabilities-failure.png",
      ),
    );
    throw error;
  } finally {
    ui.close();
  }
};
