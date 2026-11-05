const vscode = require("vscode");
const assert = require("node:assert/strict");
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
    console.log(
      "Actual multi-root: workspace connection overrides refused; explicit second-root Git/OCI installs, native discovery and offline removal pass; first root unchanged",
    );
  }
  await api.signOut();
};
