const vscode = require("vscode");
const assert = require("node:assert/strict");
const { readFile, writeFile, access } = require("node:fs/promises");
const { join } = require("node:path");
exports.qualifySkills = async (api, multiRoot = false) => {
  const ui = await require("./webview.cjs").webview(true);
  const root = vscode.workspace.workspaceFolders.at(multiRoot ? -1 : 0).uri
    .fsPath;
  const click = async (title) => {
    const expr = `[...document.querySelectorAll('[role=dialog] .monaco-button, [role=dialog] button')].find(b=>b.textContent.includes(${JSON.stringify(title)}))`;
    try {
      await ui.until(`${expr} !== undefined`);
    } catch {
      throw Error(
        "Missing Skill dialog: " +
          (await ui.evaluate("document.body.innerText.slice(-8000)")),
      );
    }
    await ui.evaluate(`${expr}.click()`);
  };
  const source = async (label) => {
    await ui.until(
      `document.querySelector('.quick-input-title')?.textContent.includes('immutable package source')`,
    );
    for (let step = 0; step < 3; step++) {
      const active = await ui.evaluate(
        `(()=>{const input=document.querySelector('.quick-input-widget input');return document.getElementById(input.getAttribute('aria-activedescendant'))?.textContent??'';})()`,
      );
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
        `(()=>{const input=document.querySelector('.quick-input-widget input');return document.getElementById(input.getAttribute('aria-activedescendant'))?.textContent !== ${JSON.stringify(active)};})()`,
      );
    }
    throw Error("No active immutable source: " + label);
  };
  try {
    await api.select("qualification");
    await api.authenticate("reader");
    for (const type of ["owned.git", "skills/owned@"]) {
      if (type === "skills/owned@") {
        await api.select("skill-oci");
        await api.authenticate("reader");
      }
      const page = await api.browse({ kind: "skill" });
      const detail = await api.detail(page.items[0].key);
      assert.equal(detail.packages.length, 1);
      let outcome;
      const installation = api
        .installSkill(0, multiRoot ? undefined : root)
        .then(
          () => {
            outcome = "complete";
          },
          (e) => {
            outcome = e.code;
            throw e;
          },
        );
      if (multiRoot) {
        await ui.until(
          "document.querySelector('.quick-input-title')?.textContent.includes('workspace for this Skill')",
        );
        await vscode.commands.executeCommand(
          "workbench.action.quickOpenSelectNext",
        );
        await vscode.commands.executeCommand(
          "workbench.action.acceptSelectedQuickOpenItem",
        );
      }
      await source(type);
      try {
        await click("Install Skill");
      } catch (e) {
        throw new Error(`${type}: ${outcome ?? "pending"}; ${e.message}`, {
          cause: e,
        });
      }
      await installation;
      const preview = vscode.workspace.textDocuments.find((document) =>
        document.getText().includes("Complete inventory:"),
      );
      assert.ok(preview, "Complete Skill inventory preview did not open");
      assert.equal(preview.uri.scheme, "oac-evidence");
      assert.equal(preview.languageId, "plaintext");
      assert.match(preview.getText(), /references\/help\.txt/);
      assert.match(preview.getText(), /Declared license:/);
      assert.deepEqual(await api.installedSkills(), [
        { name: "owned-skill", workspace: root },
      ]);
      const skillPath = join(root, ".agents/skills/owned-skill/SKILL.md");
      const original = await readFile(skillPath, "utf8");
      if (multiRoot)
        await assert.rejects(
          access(
            join(
              vscode.workspace.workspaceFolders[0].uri.fsPath,
              ".agents/skills/owned-skill",
            ),
          ),
          { code: "ENOENT" },
        );
      assert.match(original, /Read this owned instruction/);
      assert.equal(
        await readFile(
          join(root, ".agents/skills/owned-skill/references/help.txt"),
          "utf8",
        ),
        "Supporting file.",
      );
      // A second exact selection skips source selection, consent, download and writes.
      await api.installSkill(0, root);
      // Use the supported user command through the real Command Palette.
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
          "document.querySelector('.ai-customization-management-editor .management-content')?.innerText.includes('owned-skill')",
        );
      } catch {
        throw Error(
          "Native Skill discovery missing: " +
            (await ui.evaluate("document.body.innerText.slice(-12000)")),
        );
      }
      await writeFile(skillPath, original + "\nlocal edit");
      await assert.rejects(api.installSkill(0, root));
      assert.equal(
        await readFile(skillPath, "utf8"),
        original + "\nlocal edit",
      );
      await writeFile(skillPath, original);
      const redacted = new Promise((resolve, reject) => {
        const listener = vscode.workspace.onDidChangeTextDocument(
          ({ document }) => {
            if (
              document.uri.toString() !== preview.uri.toString() ||
              !document.getText().includes("Catalog session changed")
            )
              return;
            clearTimeout(deadline);
            listener.dispose();
            resolve();
          },
        );
        const deadline = setTimeout(() => {
          listener.dispose();
          reject(new Error("Private Skill preview was not redacted"));
        }, 10000);
      });
      await api.signOut();
      await redacted;
      assert.doesNotMatch(preview.getText(), /Complete inventory:|help\.txt/);
      const removal = api.removeSkill("owned-skill", root);
      await click("Remove Skill");
      await removal;
      assert.deepEqual(await api.installedSkills(), []);
      await assert.rejects(access(skillPath));
      // Removal works after sign-out; a new install needs current Catalog access.
      await assert.rejects(api.installSkill(0, root));
    }
    console.log(
      "Actual native Skills: HTTPS Git and OCI installation, read-only session-redacted previews, complete support files, native discovery, identical no-op, local-edit refusal and offline removal passed",
    );
  } finally {
    ui.close();
  }
};
