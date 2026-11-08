const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async (api) => {
  const stage = (name) =>
    require("node:fs").appendFileSync(
      require("node:path").join(
        process.env.CATALOG_VSCODE_DEBUG_DIR,
        "connection-progress.log",
      ),
      name + "\n",
    );
  stage("start connection setup");
  const ui = await require("./webview.cjs").webview(true);
  const configuration = () =>
    vscode.workspace.getConfiguration("openAgenticCatalog").get("connections");
  const before = configuration();
  const visible = ".quick-input-widget:not([style*='display: none'])";
  const title = async (name) =>
    ui.until(
      `document.querySelector(${JSON.stringify(`${visible} .quick-input-title`)})?.textContent.includes(${JSON.stringify(name)})`,
    );
  const input = async (name, value) => {
    await title(name);
    // Help opens a preview without discarding this wizard. Return focus to its
    // actual input before delivering a native key, as a user would.
    await ui.click(
      `document.querySelector(${JSON.stringify(`${visible} input`)})`,
    );
    await ui.evaluate(
      `(()=>{const input=document.querySelector(${JSON.stringify(`${visible} input`)});input.value=${JSON.stringify(value)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.key("Enter");
  };
  const pick = async (name, label) => {
    await title(name);
    await ui.evaluate(
      `(()=>{const input=document.querySelector(${JSON.stringify(`${visible} input`)});input.value=${JSON.stringify(label)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.until(
      `(()=>{const input=document.querySelector(${JSON.stringify(`${visible} input`)});return document.getElementById(input?.getAttribute('aria-activedescendant'))?.textContent.includes(${JSON.stringify(label)});})()`,
    );
    await ui.key("Enter");
  };
  try {
    const adding = vscode.commands.executeCommand(
      "openAgenticCatalog.addConnection",
    );
    await title("Connection ID");
    await ui.evaluate(
      `document.querySelector(${JSON.stringify(`${visible} input`)}).value='unsaved-help-draft'`,
    );
    const setupHelp = `document.querySelector(${JSON.stringify(`${visible} .codicon-question`)})`;
    await ui.until(`${setupHelp} !== null`);
    await ui.click(setupHelp);
    await require("./help-host-tests.cjs").section("add-a-connection");
    await title("Connection ID");
    assert.equal(
      await ui.evaluate(
        `document.querySelector(${JSON.stringify(`${visible} input`)}).value`,
      ),
      "unsaved-help-draft",
    );
    assert.deepEqual(
      configuration(),
      before,
      "Setup Help cannot save the draft",
    );
    await input("Connection ID", "qualification");
    await ui.until(
      `document.querySelector(${JSON.stringify(visible)})?.textContent.includes('This connection ID already exists')`,
    );
    await input("Connection ID", "owned-setup");
    await input("Connection name", "Owned setup");
    await pick("Connection security", "HTTPS only");
    await title("Catalog origin");
    const back = `document.querySelector(${JSON.stringify(`${visible} .codicon-quick-input-back`)})`;
    await ui.until(`${back} !== null`);
    await ui.click(back);
    await pick("Connection security", "HTTPS only");
    await input("Catalog origin", "https://127.0.0.1:9");
    await pick("Catalog API mode", "Published ARD discovery");
    await pick("Authentication", "Anonymous publication reads");
    await pick("Optional transport settings", "Use defaults");
    await pick("Review and save", "Save connection");
    await adding;
    stage("added anonymous connection");
    assert.deepEqual(configuration().slice(0, -1), before);
    assert.equal(configuration().at(-1).auth, "anonymous");
    // Cancel after changing a real draft, preserving every retained definition.
    const beforeCanceledEdit = structuredClone(configuration());
    const canceled = vscode.commands.executeCommand(
      "openAgenticCatalog.editConnection",
    );
    await pick("Edit a Catalog connection", "Owned setup");
    await input("Connection name", "Canceled native draft name");
    await title("Connection security");
    await ui.key("Escape");
    await canceled;
    assert.deepEqual(configuration(), beforeCanceledEdit);
    const editing = vscode.commands.executeCommand(
      "openAgenticCatalog.editConnection",
    );
    await pick("Edit a Catalog connection", "Owned setup");
    await input("Connection name", "Owned renamed");
    await pick("Connection security", "HTTPS only");
    await input("Catalog origin", "https://127.0.0.1:9");
    await pick("Catalog API mode", "Published ARD discovery");
    await pick("Authentication", "Anonymous publication reads");
    await pick(
      "Optional transport settings",
      "Keep current transport settings",
    );
    await pick("Review and save", "Save connection");
    await editing;
    stage("edited anonymous connection");
    assert.equal(configuration().at(-1).label, "Owned renamed");
    assert.deepEqual(configuration().slice(0, -1), before);
    const removing = vscode.commands.executeCommand(
      "openAgenticCatalog.removeConnection",
    );
    await ui.until(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].some(button=>button.textContent==='Remove')",
    );
    await ui.click(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(button=>button.textContent==='Remove')",
    );
    await removing;
    stage("removed anonymous connection");
    assert.deepEqual(configuration(), before);
    const addingControl = vscode.commands.executeCommand(
      "openAgenticCatalog.addConnection",
    );
    await input("Connection ID", "owned-reader");
    await input("Connection name", "Owned first reader");
    await pick("Connection security", "Allow literal loopback HTTP");
    await input(
      "Catalog origin",
      before.find((c) => c.id === "qualification").origin,
    );
    await pick("Catalog API mode", "Authenticated Control API");
    await pick("Authentication", "Enter an access token");
    await pick("Optional transport settings", "Use defaults");
    await pick("Review and save", "Save connection");
    await addingControl;
    assert.equal(configuration().at(-1).mode, "control");
    assert.equal(configuration().at(-1).auth, "manual");
    assert.equal(
      configuration().at(-1).origin,
      process.env.CATALOG_VSCODE_CATALOG,
    );
    await api.authenticate("reader");
    await vscode.commands.executeCommand("openAgenticCatalog.testConnection");
    assert.equal(api.snapshot().identity.subject, "reader");
    assert.equal((await api.browse({ kind: "agent" })).items.length, 1);
    const removingControl = vscode.commands.executeCommand(
      "openAgenticCatalog.removeConnection",
    );
    await ui.until(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].some(button=>button.textContent==='Remove')",
    );
    await ui.click(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(button=>button.textContent==='Remove')",
    );
    await removingControl;
    assert.deepEqual(configuration(), before);
    stage("added and verified Control reader connection");
    // Editing a signed-in connection saves its definition and selects it signed out.
    await api.select("qualification");
    await api.authenticate("reader");
    await api.test();
    const editingReader = vscode.commands.executeCommand(
      "openAgenticCatalog.editConnection",
    );
    const original = before.find((c) => c.id === "qualification");
    await pick("Edit a Catalog connection", original.label);
    await input("Connection name", "Owned reader edited");
    await pick("Connection security", "Allow literal loopback HTTP");
    await input("Catalog origin", original.origin);
    await pick("Catalog API mode", "Authenticated Control API");
    await pick("Authentication", "Enter an access token");
    await pick(
      "Optional transport settings",
      "Keep current transport settings",
    );
    await pick("Review and save", "Save connection");
    stage("saving signed-in definition");
    await editingReader;
    stage("saved signed-in definition");
    await assert.rejects(api.test(), /sign_in_required/u);
    const signing = vscode.commands.executeCommand("openAgenticCatalog.signIn");
    await input("Catalog token for Owned reader edited", "reader");
    await signing;
    stage("signed in after editing");
    assert.equal(api.snapshot().identity.subject, "reader");
    await api.signOut();
    await vscode.workspace
      .getConfiguration("openAgenticCatalog")
      .update("connections", before, vscode.ConfigurationTarget.Global);
    // Restore the actual fixture settings, then inspect real safe diagnostics.
    await api.select("qualification");
    await api.authenticate("invalid-private-token-sentinel");
    await assert.rejects(api.test(), /sign_in_required/u);
    await vscode.commands.executeCommand("openAgenticCatalog.copyDiagnostics");
    const diagnostic = JSON.parse(await vscode.env.clipboard.readText());
    stage("copied safe diagnostics");
    assert.equal(diagnostic.status, 401);
    assert.equal(diagnostic.operation, "Verify account");
    assert.ok(diagnostic.requestId);
    assert.doesNotMatch(
      JSON.stringify(diagnostic),
      /private-token|Bearer|subject|response body/u,
    );
    await api.authenticate("reader");
    await api.test();
    const recovering = vscode.commands.executeCommand(
      "openAgenticCatalog.recoverLogin",
    );
    await ui.until(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].some(button=>button.textContent.includes('Other windows closed: recover'))",
    );
    await ui.click(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(button=>button.textContent.includes('Other windows closed: recover'))",
    );
    await recovering;
    stage("recovered login");
    await assert.rejects(api.test(), /sign_in_required/u);
    await api.authenticate("reader");
    await api.test();
    console.log(
      "Native Add/Edit/Back, early validation, canceled edit, settings preservation and safe HTTP diagnostics passed",
    );
  } finally {
    ui.close();
  }
};
