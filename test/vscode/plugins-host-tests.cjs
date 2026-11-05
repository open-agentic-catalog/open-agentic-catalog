const vscode = require("vscode");
const assert = require("node:assert/strict");
const { readFile, writeFile, access, readdir } = require("node:fs/promises");
const { join } = require("node:path");
exports.qualifyPlugins = async (api) => {
  const progress = (stage) =>
    require("node:fs").appendFileSync(
      join(process.env.CATALOG_VSCODE_DEBUG_DIR, "plugin-progress.log"),
      stage + "\n",
    );
  const ui = await require("./webview.cjs").webview(true);
  const archives = new Set();
  let networkEvents = 0;
  const code = (value) =>
    typeof value === "string" && /^[A-Z0-9_]{1,64}$/u.test(value)
      ? value
      : "unclassified";
  const subscriptions = [
    [
      "undici:request:error",
      ({ request, error }) => {
        if (
          !archives.has(String(request.origin) + request.path) ||
          networkEvents++ >= 32
        )
          return;
        progress(
          `archive transport error: ${code(error.code)}; cause: ${code(error.cause?.code)}`,
        );
      },
    ],
    [
      "undici:request:headers",
      ({ request, response }) => {
        if (
          !archives.has(String(request.origin) + request.path) ||
          networkEvents++ >= 32
        )
          return;
        progress(
          `archive HTTP status: ${Number.isInteger(response.statusCode) ? response.statusCode : "invalid"}`,
        );
      },
    ],
  ].map(([name, listener]) => {
    const channel = require("node:diagnostics_channel").channel(name);
    channel.subscribe(listener);
    return () => channel.unsubscribe(listener);
  });
  const root = vscode.workspace.workspaceFolders[0].uri.fsPath;
  const destination = join(root, ".oac-catalog/plugins/installed/owned.tools");
  const settings = () => vscode.workspace.getConfiguration("chat");
  const sentinel = async () =>
    assert.deepEqual(
      await readdir(process.env.CATALOG_VSCODE_PLUGIN_SENTINELS),
      [],
      "Disabled/unapproved Plugin hook or MCP executed",
    );
  const click = async (title) => {
    progress("wait dialog: " + title);
    const dialog = await require("./webview.cjs").webview(true);
    // Progress notifications also have role=dialog and Cancel buttons.
    // Consent must select a native modal, never the acquisition notification.
    const expr = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button, .monaco-dialog-box button')].find(b=>b.textContent.trim() === ${JSON.stringify(title)} && b.getBoundingClientRect().width > 0)`;
    try {
      await dialog.until(`${expr} !== undefined`);
      progress("click dialog: " + title);
      await dialog.evaluate(
        `(()=>{globalThis.oacPluginConsentButton=${expr};globalThis.oacPluginConsentButton.click();})()`,
      );
      await dialog.until("!globalThis.oacPluginConsentButton.isConnected");
      progress("closed dialog: " + title);
    } catch (e) {
      throw Error(
        `Plugin dialog ${title}: ${e.message}; ${await dialog.evaluate("document.body.innerText.slice(-5000)")}`,
        { cause: e },
      );
    } finally {
      dialog.close();
    }
  };
  const native = async (present) => {
    await vscode.commands.executeCommand("workbench.action.showCommands");
    await ui.until(
      "document.querySelector('.quick-input-widget input') !== null",
    );
    await ui.evaluate(
      `(()=>{const input=document.querySelector('.quick-input-widget input');input.value='>Chat: Open Customizations';input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.key("Enter");
    await ui.until(
      "document.body.innerText.includes('Customizations') && document.body.innerText.includes('Skills')",
    );
    await ui.evaluate(
      "[...document.querySelectorAll('.ai-customization-management-editor .section-label')].find(e=>e.textContent.trim()==='Skills')?.closest('.monaco-list-row')?.click()",
    );
    try {
      await ui.until(
        `Boolean(document.querySelector('.ai-customization-management-editor .management-content')?.innerText.includes('owned-plugin-skill')) === ${present}`,
      );
    } catch {
      throw Error(
        `Native Plugin skill present=${present}: ${await ui.evaluate("document.body.innerText.slice(-12000)")}`,
      );
    }
  };
  try {
    await api.select("plugin-host");
    await api.authenticate("reader");
    let page = await api.browse({ kind: "plugin" }),
      detail = await api.detail(page.items[0].key);
    assert.equal(detail.packages.length, 2);
    for (const archive of detail.packages) {
      const url = new URL(archive.url);
      assert.equal(
        url.hostname,
        "127.0.0.1",
        "Archive diagnostics must stay within the owned fixture",
      );
      archives.add(url.href);
    }
    assert.equal(
      vscode.workspace
        .getConfiguration("openAgenticCatalog")
        .get("packageTools").caFile,
      process.env.CATALOG_VSCODE_PUBLIC_CA,
      "Native acquisition must retain the configured package CA",
    );
    await sentinel();
    // Unapproved installation leaves no files or native registration.
    let install = api.installPlugin(0, root);
    await click("Cancel");
    await install;
    progress("install returned");
    await assert.rejects(access(destination));
    assert.equal(settings().get("pluginLocations", {})[destination], undefined);
    const unrelated = join(root, "not-a-plugin");
    const original = settings().get("pluginLocations", {});
    await settings().update(
      "pluginLocations",
      { ...original, [unrelated]: false },
      vscode.ConfigurationTarget.Global,
    );
    install = api.installPlugin(0, root);
    await click("Install Plugin disabled");
    await install;
    progress("install returned");
    assert.equal(settings().get("pluginLocations")[destination], false);
    assert.equal(settings().get("pluginLocations")[unrelated], false);
    assert.deepEqual(await api.installedPlugins(), [
      { name: "owned.tools", workspace: root, enabled: false },
    ]);
    assert.equal(
      await readFile(join(destination, "readme.txt"), "utf8"),
      "Owned variant 0",
    );
    const receiptPath = join(
      root,
      ".oac-catalog/plugins/receipts/owned.tools.json",
    );
    const comparisonReceipt = await readFile(receiptPath);
    const comparisonSettings = { ...settings().get("pluginLocations") };
    await api.comparePlugin(0, root);
    assert.match(
      vscode.window.activeTextEditor.document.getText(),
      /The selected exact package is installed\./u,
    );
    assert.match(
      vscode.window.activeTextEditor.document.getText(),
      /Native path: disabled/u,
    );
    assert.deepEqual(await readFile(receiptPath), comparisonReceipt);
    assert.deepEqual(settings().get("pluginLocations"), comparisonSettings);
    await native(false);
    await sentinel();
    await api.installPlugin(0, root); // No-op must not download or change enablement.
    // Explicit native policy refusal must not change policy or registration.
    await settings().update(
      "plugins.enabled",
      false,
      vscode.ConfigurationTarget.Global,
    );
    await assert.rejects(
      api.activatePlugin(0, root),
      /native_plugins_disabled/u,
    );
    assert.equal(settings().get("plugins.enabled"), false);
    await settings().update(
      "plugins.enabled",
      true,
      vscode.ConfigurationTarget.Global,
    );
    let activation = api.activatePlugin(0, root);
    await click("Cancel");
    await activation;
    progress("activation returned");
    assert.equal(settings().get("pluginLocations")[destination], false);
    await sentinel();
    // A local edit after the preview must still prevent activation.
    const support = join(destination, "readme.txt"),
      supportBytes = await readFile(support);
    activation = api.activatePlugin(0, root);
    await ui.until(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].some(b=>b.textContent.trim()==='Activate Plugin')",
    );
    await writeFile(support, "changed during consent");
    const refusedActivation = assert.rejects(
      activation,
      /local_skill_changed/u,
    );
    await click("Activate Plugin");
    await refusedActivation;
    assert.equal(settings().get("pluginLocations")[destination], false);
    await writeFile(support, supportBytes);
    activation = api.activatePlugin(0, root);
    await click("Activate Plugin");
    await activation;
    progress("activation returned");
    assert.equal(settings().get("pluginLocations")[destination], true);
    await native(true); // Actual native component loading; no public model or account.
    await sentinel();
    await assert.rejects(
      api.installPlugin(1, root),
      /disable_plugin_before_update/u,
    );
    await api.disablePlugin("owned.tools", root);
    assert.equal(settings().get("pluginLocations")[destination], false);
    await native(false);
    // Switch to another inspected exact variant, whose archive has a wrapper root.
    install = api.installPlugin(1, root);
    await click("Install Plugin disabled");
    await install;
    progress("install returned");
    assert.equal(
      await readFile(join(destination, "readme.txt"), "utf8"),
      "Owned variant 1",
    );
    assert.equal(settings().get("pluginLocations")[destination], false);
    const manifest = join(destination, "plugin.json"),
      bytes = await readFile(manifest);
    await writeFile(
      manifest,
      Buffer.concat([bytes, Buffer.from("\nlocal edit")]),
    );
    await assert.rejects(api.activatePlugin(1, root), /local_skill_changed/u);
    await api.disablePlugin("owned.tools", root); // Safe even after a local edit.
    await writeFile(manifest, bytes);
    await api.signOut();
    await assert.rejects(api.activatePlugin(1, root));
    await api.disablePlugin("owned.tools", root); // No Catalog connection needed.
    const removal = api.removePlugin("owned.tools", root);
    await click("Remove Plugin");
    await removal;
    assert.equal(settings().get("pluginLocations")[destination], undefined);
    assert.equal(settings().get("pluginLocations")[unrelated], false);
    assert.deepEqual(await api.installedPlugins(), []);
    await assert.rejects(access(destination));
    await sentinel();
    assert.equal(
      networkEvents,
      2,
      "Archive diagnostics observe the two actual acquisitions",
    );
    console.log(
      "Actual native Plugins: exact HTTPS archives, disabled install, cancelled consent, native policy refusal, whole-package activation and Skill loading, disable, wrapper update, local-edit refusal, no-op, offline removal, preserved settings and zero hook/MCP sentinel execution passed",
    );
  } finally {
    subscriptions.forEach((unsubscribe) => unsubscribe());
    ui.close();
  }
};
