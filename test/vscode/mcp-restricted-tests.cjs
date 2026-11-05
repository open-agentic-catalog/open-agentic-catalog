const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async () => {
  assert.equal(
    vscode.workspace.isTrusted,
    false,
    "Owned fresh workspace must remain in Restricted Mode",
  );
  const extension = vscode.extensions.getExtension(
      "open-agentic-catalog.open-agentic-catalog",
    ),
    api = await extension.activate();
  await require("./help-host-tests.cjs").verify();
  await require("./help-host-tests.cjs").images();
  await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  await api.select("qualification");
  await api.authenticate("reader");
  const page = await api.browse({ kind: "mcp_server" });
  await api.detail(page.items[0].key);
  await assert.rejects(api.useMcp(0), /workspace_trust_required/u);
  assert.deepEqual(api.mcpSelections(), []);
  await assert.rejects(api.installSkill(0), /trusted_workspace_required/u);
  assert.deepEqual(await api.installedSkills(), []);
  await assert.rejects(api.installPlugin(0), /trusted_workspace_required/u);
  await assert.rejects(api.activatePlugin(0), /trusted_workspace_required/u);
  assert.deepEqual(await api.installedPlugins(), []);
  const agents = await api.browse({ kind: "agent" });
  await api.detail(agents.items[0].key);
  await assert.rejects(api.talkAgent(0), /trusted_workspace_required/u);
  assert.equal(api.a2aState(), undefined);
  assert.ok(
    !vscode.lm.tools.some(
      (t) =>
        t.name.includes("owned_https_echo") ||
        t.name.includes("owned_stdio_echo"),
    ),
  );
  console.log(
    "Actual Restricted Mode: browsing available; MCP, Skill, Plugin and A2A consumption refused",
  );
};
