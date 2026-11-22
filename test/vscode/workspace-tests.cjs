const vscode = require("vscode");
const assert = require("node:assert/strict");
const { channel } = require("node:diagnostics_channel");
const { access, readFile } = require("node:fs/promises");
const { join } = require("node:path");

async function pluginCredentialAfterFolderRemoval(api) {
  await api.select("qualification");
  await api.authenticate("reader");
  const page = await api.browse({ kind: "plugin" });
  const detail = await api.detail(page.items[0].key);
  const archive = new URL(detail.packages[0].url);
  assert.equal(archive.hostname, "127.0.0.1");
  const [remaining, removed] = vscode.workspace.workspaceFolders.map(
    (folder) => folder.uri.fsPath,
  );
  const state = (root) => join(root, ".oac-catalog/plugins");
  const destination = (root) => join(state(root), "installed/owned.tools");
  const locations = () =>
    vscode.workspace.getConfiguration("chat").get("pluginLocations", {});
  const originalLocations = { ...locations() };
  for (const root of [remaining, removed])
    assert.equal(originalLocations[destination(root)], undefined);
  const requests = channel("undici:request:create");
  let downloads = 0;
  const count = ({ request }) => {
    if (String(request.origin) + request.path === archive.href) downloads++;
  };
  const ui = await require("./webview.cjs").webview(true);
  const { field, input, pick, consent, title } =
    require("./native-input.cjs").nativeInput(ui);
  const authentication = "Package source authentication";
  const workspace = "Choose the workspace that owns this Plugin";
  const chooseOption = async (prompt, label) => {
    const active = `document.getElementById((${field})?.getAttribute('aria-activedescendant'))?.textContent`;
    await ui.until(title(prompt));
    await ui.evaluate(`(${field}).focus()`);
    await ui.until(`document.activeElement === (${field}) && !!(${active})`);
    if (!(await ui.evaluate(active)).includes(label))
      await vscode.commands.executeCommand(
        "workbench.action.quickOpenSelectNext",
      );
    await ui.until(`(${active})?.includes(${JSON.stringify(label)})`);
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
    await ui.until(`!(${title(prompt)})`);
  };
  requests.subscribe(count);
  try {
    const save = vscode.commands.executeCommand(
      "openAgenticCatalog.packageCredentials",
    );
    await pick("Package credential source", "HTTPS Plugin archive");
    await input("Exact package HTTPS origin", archive.origin);
    await pick("HTTPS Plugin archive", "Add or replace");
    await pick("Plugin archive authentication", "Bearer");
    await input("Package password or token", "owned-archive-credential");
    await save;

    const install = api.installPlugin(0).then(
      () => "installed",
      (error) => error.code,
    );
    await chooseOption(workspace, removed);
    await consent("Install Plugin disabled");
    await ui.until(title(authentication));
    assert.equal(vscode.workspace.updateWorkspaceFolders(1, 1), true);
    const { fixturePoll } = await import("./wait.mjs");
    await fixturePoll(
      () => vscode.workspace.workspaceFolders.length === 1,
      "removal of the selected workspace folder",
    );
    assert.equal(vscode.workspace.workspaceFolders[0].uri.fsPath, remaining);
    await chooseOption(authentication, "Use saved package credential");
    assert.equal(await install, "selection_changed");
    assert.equal(downloads, 0, "Stale consent must not contact the archive");
    assert.deepEqual(
      locations(),
      originalLocations,
      "A refused install must not register either folder",
    );
    for (const root of [removed, remaining]) {
      await assert.rejects(access(destination(root)), { code: "ENOENT" });
      await assert.rejects(
        access(join(state(root), "receipts/owned.tools.json")),
        { code: "ENOENT" },
      );
    }

    const retry = vscode.commands.executeCommand(
      "openAgenticCatalog.installPlugin",
    );
    await chooseOption(workspace, remaining);
    await consent("Install Plugin disabled");
    await chooseOption(authentication, "Anonymous");
    await retry;
    assert.equal(
      downloads,
      1,
      "Fresh consent must fetch the selected archive once",
    );
    assert.deepEqual(await api.installedPlugins(), [
      { name: "owned.tools", workspace: remaining, enabled: false },
    ]);
    assert.deepEqual(locations(), {
      ...originalLocations,
      [destination(remaining)]: false,
    });
    assert.equal(
      await readFile(join(destination(remaining), "readme.txt"), "utf8"),
      "Owned variant 0",
    );
    await assert.rejects(access(destination(removed)), { code: "ENOENT" });
  } finally {
    requests.unsubscribe(count);
    ui.close();
  }
}

exports.run = async (multiRoot = false) => {
  const target = vscode.extensions.getExtension(
    "open-agentic-catalog.open-agentic-catalog",
  );
  assert.equal(
    vscode.env.remoteName,
    undefined,
    "This selection qualifies a local workspace host only",
  );
  assert.deepEqual(target.packageJSON.extensionKind, ["workspace"]);
  assert.equal(
    vscode.workspace.workspaceFolders?.length ?? 0,
    multiRoot ? 2 : 0,
  );
  const api = await target.activate();
  await api.select("qualification");
  await api.authenticate("reader");
  const page = await api.browse({ kind: "skill" });
  assert.equal(page.items.length, 1);
  await api.detail(page.items[0].key);
  if (!multiRoot) {
    await assert.rejects(api.installSkill(0), /filesystem_workspace_required/u);
    const plugins = await api.browse({ kind: "plugin" });
    await api.detail(plugins.items[0].key);
    await assert.rejects(
      api.installPlugin(0),
      /filesystem_workspace_required/u,
    );
    assert.deepEqual(await api.installedSkills(), []);
    assert.deepEqual(await api.installedPlugins(), []);
    console.log(
      "Actual empty window: reader browsing passes; Skill and Plugin writes require a filesystem workspace",
    );
  } else {
    const configuration =
      vscode.workspace.getConfiguration("openAgenticCatalog");
    const connections = configuration.get("connections");
    await assert.rejects(
      configuration.update(
        "connections",
        connections,
        vscode.ConfigurationTarget.Workspace,
      ),
      /only into User settings/u,
    );
    assert.deepEqual(configuration.get("connections"), connections);
    assert.equal(
      configuration.inspect("connections").workspaceValue,
      undefined,
    );
    await require("./skills-host-tests.cjs").qualifySkills(api, true);
    await require("./local-tools-host-tests.cjs").duplicateRoots(api);
    await pluginCredentialAfterFolderRemoval(api);
    console.log(
      "Multi-root Skill installation and removal passed. Removing the selected Plugin folder refused stale credentials. Fresh consent installed the Plugin disabled in the remaining folder.",
    );
  }
  await api.signOut();
};
