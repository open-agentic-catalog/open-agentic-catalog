const vscode = require("vscode");
const assert = require("node:assert/strict");
const { readFile, writeFile } = require("node:fs/promises");
const { join } = require("node:path");
exports.run = async (stage, expectedVersion, expectedMarker) => {
  const target = vscode.extensions.getExtension(
    "open-agentic-catalog.open-agentic-catalog",
  );
  assert.equal(target.packageJSON.version, expectedVersion);
  const api = await target.activate();
  assert.equal(api.__catalogFixtureBuild ?? null, expectedMarker);
  const workspace = vscode.workspace.workspaceFolders[0].uri.fsPath;
  await api.select("qualification");
  let credentialsRestored = true;
  try {
    assert.equal((await api.test()).identity.subject, "reader");
  } catch (error) {
    if (
      !["setup", "reinstalled"].includes(stage) ||
      error.code !== "sign_in_required"
    )
      throw error;
    credentialsRestored = false;
    await api.authenticate("reader");
  }
  const ui = await require("./webview.cjs").webview(true);
  async function approve(title) {
    const expression = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(title)})`;
    await ui.until(`${expression} !== undefined`);
    await ui.evaluate(`${expression}.click()`);
  }
  try {
    let page = await api.browse({ kind: "agent" });
    await api.detail(page.items[0].key);
    if (stage === "setup") {
      await api.saveBookmark();
      page = await api.browse({ kind: "skill" });
      await api.detail(page.items[0].key);
      const installation = api.installSkill(0, workspace);
      await ui.until(
        "document.querySelector('.quick-input-title')?.textContent.includes('immutable package source')",
      );
      await vscode.commands.executeCommand(
        "workbench.action.acceptSelectedQuickOpenItem",
      );
      await approve("Install Skill");
      await installation;
      await writeFile(
        join(workspace, "saved-export.json"),
        '{"owned":"export"}\n',
      );
    } else {
      if (stage !== "reinstalled" || credentialsRestored)
        assert.equal(api.browsing().bookmarks.length, 1);
      assert.match(
        await readFile(
          join(workspace, ".agents/skills/owned-skill/SKILL.md"),
          "utf8",
        ),
        /owned-skill/u,
      );
      assert.deepEqual(await api.installedSkills(), [
        { name: "owned-skill", workspace },
      ]);
      assert.equal(
        await readFile(join(workspace, "saved-export.json"), "utf8"),
        '{"owned":"export"}\n',
      );
    }
    assert.deepEqual(
      api.mcpSelections(),
      [],
      "A reload restored an ephemeral MCP selection",
    );
    const bookmarksBeforeSignOut = api.browsing().bookmarks.length;
    if (stage === "reinstalled") {
      await api.signOut();
      assert.equal(api.browsing().bookmarks.length, 0);
      const removal = api.removeSkill("owned-skill", workspace);
      await approve("Remove Skill");
      await removal;
      assert.deepEqual(await api.installedSkills(), []);
    }
    return {
      credentialsRestored,
      bookmarks: bookmarksBeforeSignOut,
      ...(stage === "reinstalled"
        ? { bookmarksAfterSignOut: api.browsing().bookmarks.length }
        : {}),
      installedVersion: target.packageJSON.version,
      fixtureMarker: api.__catalogFixtureBuild ?? null,
      installedPath: target.extensionPath,
    };
  } finally {
    ui.close();
  }
};
