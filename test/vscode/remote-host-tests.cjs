const vscode = require("vscode");
const fs = require("node:fs/promises");
const path = require("node:path");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
async function verifySkill(root) {
  const workspace = path.join(root, "workspace");
  const files = [
    [
      "SKILL.md",
      "---\nname: owned-skill\ndescription: Owned discovery.\n---\nRead this owned instruction.",
      false,
    ],
    ["references/help.txt", "Supporting file.", false],
    ["scripts/never-run.sh", "#!/bin/sh\nexit 91\n", true],
  ];
  for (const [name, bytes, executable] of files) {
    const target = path.join(workspace, ".agents/skills/owned-skill", name);
    assert.equal(await fs.readFile(target, "utf8"), bytes);
    assert.equal(Boolean((await fs.stat(target)).mode & 0o111), executable);
  }
  const receipt = await fs.readFile(
    path.join(workspace, ".oac-catalog/skills/receipts/owned-skill.json"),
    "utf8",
  );
  assert.deepEqual(
    JSON.parse(receipt).files,
    files.map(([name, bytes, executable]) => ({
      path: "owned-skill/" + name,
      size: Buffer.byteLength(bytes),
      digest: "sha256:" + createHash("sha256").update(bytes).digest("hex"),
      executable,
    })),
  );
  return receipt;
}
async function installSkill(api, root) {
  await api.select("remote-skill");
  await api.authenticate("reader");
  const page = await api.browse({ kind: "skill" });
  assert.equal(page.items.length, 1);
  const detail = await api.detail(page.items[0].key);
  assert.equal(detail.packages.length, 1);
  const configuration = vscode.workspace.getConfiguration("openAgenticCatalog");
  const tools = configuration.get("packageTools");
  assert.equal(tools.gitPath, path.join(root, "remote-tools/git"));
  const ui = await require("./webview.cjs").webview(
    true,
    path.join(root, "ui"),
  );
  const prepare = async () => {
    await ui.until(
      "document.querySelector('.quick-input-title')?.textContent.includes('immutable package source') && document.querySelector('.quick-input-list .monaco-list-row.focused')?.textContent.includes('owned.git')",
    );
    const state = () =>
      ui.evaluate(
        "({activeTag:document.activeElement?.tagName,activeInPicker:!!document.activeElement?.closest('.quick-input-widget'),activeInEditor:!!document.activeElement?.closest('.editor-instance'),pickerTitle:document.querySelector('.quick-input-title')?.textContent,focusedOwnedSource:document.querySelector('.quick-input-list .monaco-list-row.focused')?.textContent.includes('owned.git')??false,modalButtons:[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].map(b=>b.textContent.trim())})",
      );
    const before = await state();
    // A previous preview can hold editor focus while the next picker is visible.
    // Deliver one native selection only after focusing its actual input.
    const input =
      "document.querySelector('.quick-input-widget:not([style*=\"display: none\"]) input')";
    await ui.click(input);
    await ui.until(
      `document.activeElement===(${input}) && document.getElementById((${input})?.getAttribute('aria-activedescendant'))?.textContent.includes('owned.git')`,
    );
    await ui.key("Enter");
    const button =
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()==='Install Skill')";
    try {
      await ui.until(`!!(${button})`);
    } catch (error) {
      throw new Error(
        error.message +
          " Remote native consent state=" +
          JSON.stringify({ before, after: await state() }),
        { cause: error },
      );
    }
    await ui.click(button);
  };
  try {
    await configuration.update(
      "packageTools",
      { ...tools, gitPath: path.join(root, "remote-tools/missing-git") },
      vscode.ConfigurationTarget.Global,
    );
    const missing = assert.rejects(
      api.installSkill(0, path.join(root, "workspace")),
      /package_tool_unavailable/u,
    );
    await prepare();
    await missing;
    await assert.rejects(
      fs.access(path.join(root, "workspace/.agents/skills/owned-skill")),
      { code: "ENOENT" },
    );
    await assert.rejects(fs.access(path.join(root, "ui-git.log")), {
      code: "ENOENT",
    });
    await configuration.update(
      "packageTools",
      tools,
      vscode.ConfigurationTarget.Global,
    );
    const installing = api.installSkill(0, path.join(root, "workspace"));
    await prepare();
    await installing;
    assert.deepEqual(await api.installedSkills(), [
      { name: "owned-skill", workspace: path.join(root, "workspace") },
    ]);
    const receipt = await verifySkill(root);
    const gitCalls = await fs.readFile(
      path.join(root, "remote-git.log"),
      "utf8",
    );
    assert.match(gitCalls, /^(?:remote-git\n)+$/u);
    await api.compareSkill(0, path.join(root, "workspace"));
    assert.ok(
      vscode.workspace.textDocuments.some(
        (d) =>
          d.uri.scheme === "oac-evidence" &&
          d.getText().includes("The selected exact package is installed.") &&
          d.getText().includes("Workspace: " + path.join(root, "workspace")),
      ),
    );
    assert.equal(await verifySkill(root), receipt);
    assert.equal(
      await fs.readFile(path.join(root, "remote-git.log"), "utf8"),
      gitCalls,
      "Read-only comparison must not execute Git",
    );
    await api.select("remote-oidc");
    assert.equal((await api.test()).identity.subject, "oidc-reader");
    return receipt;
  } finally {
    await configuration.update(
      "packageTools",
      tools,
      vscode.ConfigurationTarget.Global,
    );
    ui.close();
  }
}
exports.activate = (context) => {
  void (async () => {
    const root = process.env.OAC_REMOTE_FIXTURE;
    try {
      assert.ok(root);
      assert.equal(vscode.env.remoteName, "ssh-remote");
      assert.equal(process.platform, "linux");
      assert.ok(process.execPath.startsWith(root + path.sep));
      const folder = vscode.workspace.workspaceFolders[0];
      assert.equal(folder.uri.fsPath, path.join(root, "workspace"));
      assert.equal(
        folder.uri.scheme,
        "file",
        "The real SSH host receives its native filesystem URI after VS Code remoting translation",
      );
      assert.equal(vscode.workspace.isTrusted, true);
      const extension = vscode.extensions.getExtension(
        "open-agentic-catalog.open-agentic-catalog",
      );
      assert.equal(extension.extensionKind, vscode.ExtensionKind.Workspace);
      assert.ok(extension.extensionUri.fsPath.startsWith(root + path.sep));
      const api = await extension.activate();
      await api.select("remote-oidc");
      const previous = context.globalState.get("firstRemoteHost");
      if (previous) {
        assert.notEqual(
          previous.pid,
          process.pid,
          "A new real remote extension-host process restores native SecretStorage",
        );
        assert.equal((await api.test()).identity.subject, "oidc-reader");
        assert.equal(
          await verifySkill(root),
          previous.skillReceipt,
          "A real remote host reload preserves the exact installation and receipt",
        );
        assert.deepEqual(await api.installedSkills(), [
          { name: "owned-skill", workspace: folder.uri.fsPath },
        ]);
        await api.signOut();
        await assert.rejects(api.test(), /sign_in_required/u);
        const catalogOrigin = vscode.workspace
          .getConfiguration("openAgenticCatalog")
          .get("connections")
          .find((c) => c.id === "remote-skill").origin;
        const refused = await fetch(catalogOrigin + "/owned-remote-offline", {
          method: "POST",
        });
        assert.equal(refused.status, 200);
        const ui = await require("./webview.cjs").webview(
          true,
          path.join(root, "ui"),
        );
        try {
          const removal = api.removeSkill("owned-skill", folder.uri.fsPath);
          const button =
            "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()==='Remove Skill')";
          await ui.until(`!!(${button})`);
          await ui.click(button);
          await removal;
        } finally {
          ui.close();
        }
        assert.deepEqual(await api.installedSkills(), []);
        await assert.rejects(
          fs.access(path.join(folder.uri.fsPath, ".agents/skills/owned-skill")),
          { code: "ENOENT" },
        );
        await assert.rejects(
          fs.access(
            path.join(
              folder.uri.fsPath,
              ".oac-catalog/skills/receipts/owned-skill.json",
            ),
          ),
          { code: "ENOENT" },
        );
        const result = {
          remoteSkillInstalled: true,
          remoteSkillCompared: true,
          remoteSkillRestored: true,
          remoteSkillRemovedOffline: true,
          missingRemoteToolRefused: true,
          passed: true,
          remoteName: vscode.env.remoteName,
          platform: process.platform,
          executable: process.execPath,
          workspace: folder.uri.toString(),
          extension: extension.extensionUri.toString(),
          globalStorage: context.globalStorageUri.toString(),
          initialPid: previous.pid,
          restoredPid: process.pid,
          restoredCredentials: true,
          signOut: true,
        };
        await fs.writeFile(
          path.join(root, "result.json"),
          JSON.stringify(result),
          { mode: 0o600 },
        );
        return;
      }
      const redirect = await vscode.env.asExternalUri(
        vscode.Uri.parse(
          "vscode://open-agentic-catalog.open-agentic-catalog/oidc",
        ),
      );
      await api.beginLogin(redirect.toString(true));
      const wrong = new URL(redirect.toString(true));
      wrong.searchParams.set("code", "owned-invalid-code");
      wrong.searchParams.set("state", "wrong-session");
      wrong.searchParams.set("iss", process.env.OAC_REMOTE_ISSUER);
      await assert.rejects(api.completeLogin(wrong.href));
      await api.signOut();
      await fs.writeFile(
        path.join(root, "ready.json"),
        JSON.stringify({
          ready: true,
          callbackScheme: redirect.scheme,
          callbackAuthority: redirect.authority,
        }),
        { mode: 0o600 },
      );
      const authorization = await api.beginLogin(
        redirect.toString(true),
        "vscode://open-agentic-catalog.open-agentic-catalog/oidc",
      );
      const completion = api.waitForLogin();
      void completion.catch(() => {});
      await fs.writeFile(path.join(root, "browser-url"), authorization, {
        mode: 0o600,
      });
      await completion;
      assert.equal((await api.test()).identity.subject, "oidc-reader");
      const tool = require("node:child_process").execFileSync(
        "/usr/bin/git",
        ["--version"],
        { encoding: "utf8", timeout: 5000 },
      );
      assert.match(tool, /^git version /u);
      await fs.writeFile(
        path.join(root, "workspace", "remote-owned.txt"),
        "Written by the real SSH extension host.\n",
        { mode: 0o600 },
      );
      const skillReceipt = await installSkill(api, root);
      await context.globalState.update("firstRemoteHost", {
        pid: process.pid,
        skillReceipt,
      });
      void vscode.commands
        .executeCommand("workbench.action.reloadWindow")
        .catch(async (error) => {
          // The reload deliberately terminates this remote host and cancels its RPC.
          // Completion still requires a different host PID and a successful read.
          if (error.name === "Canceled") return;
          await fs.writeFile(
            path.join(root, "result.json"),
            JSON.stringify({
              passed: false,
              error: String(error.stack ?? error),
            }),
            { mode: 0o600 },
          );
        });
    } catch (error) {
      await fs.writeFile(
        path.join(root, "result.json"),
        JSON.stringify({ passed: false, error: String(error.stack ?? error) }),
        { mode: 0o600 },
      );
    }
  })();
};
