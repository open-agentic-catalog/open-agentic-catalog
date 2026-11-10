const vscode = require("vscode");
const assert = require("node:assert/strict");
const {
  readFile,
  writeFile,
  rm,
  mkdir,
  appendFile,
} = require("node:fs/promises");
const { join } = require("node:path");
const { pathToFileURL } = require("node:url");
async function owners() {
  const extension = process.env.CATALOG_VSCODE_SOURCE_EXTENSION;
  const lib = await import(process.env.CATALOG_VSCODE_LIB);
  const skill = await import(
    pathToFileURL(join(extension, "test/package-fixtures.mjs"))
  );
  const plugin = await import(
    pathToFileURL(join(extension, "test/plugin-fixtures.mjs"))
  );
  return { lib, skill, plugin };
}
async function install(o, root, kind, replacement) {
  const store = new o.lib.SkillStore(root, kind);
  let plan, bytes;
  if (kind === "plugin") {
    const value = o.plugin.fixture(
      replacement ? { "readme.txt": "Replacement after native consent" } : {},
    );
    plan = value.plan;
    bytes = o.lib.unpackPackage(plan, value.archive);
  } else {
    const files = o.skill.files(
      replacement
        ? "Replacement after native consent"
        : "Original native consent",
    );
    plan = o.lib.skillPlan(
      o.skill.representation(files),
      o.skill.hash("owned local actor"),
    );
    bytes = o.skill.content(files);
  }
  await store.install(plan, bytes, async () => {});
  return { store, plan };
}
async function wait(read, label) {
  const deadline = performance.now() + 10000;
  while (performance.now() < deadline) {
    const value = await read();
    if (value) return value;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw Error("Local tools deadline: " + label);
}
exports.run = async (api) => {
  const o = await owners(),
    root = vscode.workspace.workspaceFolders[0].uri.fsPath;
  const ui = await require("./webview.cjs").webview(true),
    chat = () => vscode.workspace.getConfiguration("chat");
  const original = chat().inspect("pluginLocations").globalValue;
  let captured,
    captureCalled = false;
  const capture = vscode.commands.registerCommand(
    "catalogQualification.captureTool",
    (row) => {
      captured = row;
      captureCalled = true;
    },
  );
  const progress = (value) =>
    appendFile(
      join(process.env.CATALOG_VSCODE_DEBUG_DIR, "local-tools-progress.log"),
      value + "\n",
    );
  const click = async (title) => {
    const expression = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(title)})`;
    await ui.until(`!!(${expression})`);
    await ui.evaluate(`(${expression}).click()`);
    await ui.until("!document.querySelector('.monaco-dialog-box')");
  };
  const action = async (name, command) => {
    const chooseMenu = async (title) => {
      await ui.until("!!document.activeElement?.closest('.context-view')");
      await ui.key("Home");
      for (let step = 0; step < 10; step++) {
        const text = await ui.evaluate(
          "document.querySelector('.context-view .action-item.focused .action-label')?.textContent ?? ''",
        );
        if (text.includes(title)) {
          await ui.key("Enter");
          await ui.until(
            "!document.querySelector('.context-view .action-item.focused')",
          );
          return;
        }
        await ui.key("ArrowDown");
      }
      throw Error(
        "Native menu did not focus " +
          title +
          ": " +
          (await ui.evaluate(
            "document.querySelector('.context-view')?.outerHTML.slice(-4000)",
          )),
      );
    };
    await vscode.commands.executeCommand("openAgenticCatalog.refreshTools");
    await vscode.commands.executeCommand("openAgenticCatalog.tools.focus");
    const row = `[...document.querySelectorAll('.monaco-list-row')].find(r=>r.textContent.includes(${JSON.stringify(name)})&&r.textContent.includes(${JSON.stringify(root)}))`;
    await ui.until(`!!(${row})`);
    if (command === "Remove Installed Skill") {
      await ui.click(`(${row})`);
      const inline = `(${row})?.querySelector('.action-label[aria-label*="Remove Installed Skill"], .action-label[title*="Remove Installed Skill"]')`;
      await ui.until(`!!(${inline})`);
      await ui.click(inline);
      return;
    }
    await ui.click(`(${row})`);
    await ui.click(`(${row})`, "right");
    if (command === "Reveal Owned Package") {
      const probe =
        "[...document.querySelectorAll('.context-view .action-label')].find(n=>n.textContent.includes('Owned: Inspect Catalog tool selection'))";
      await ui.until(`!!(${probe})`);
      await chooseMenu("Owned: Inspect Catalog tool selection");
      await wait(() => captureCalled, "native context arguments");
      const confirmed = await new o.lib.SkillStore(root).get(name);
      assert.equal(captured.store.workspace, root);
      assert.equal(captured.receipt.name, name);
      assert.equal(
        captured.receipt.fingerprint,
        o.lib.receiptSummary(confirmed).fingerprint,
      );
      await ui.click(`(${row})`);
      await ui.click(`(${row})`, "right");
    }
    const item = `[...document.querySelectorAll('.context-view .action-label')].find(n=>n.textContent.includes(${JSON.stringify(command)}))`;
    await ui.until(`!!(${item})`);
    await chooseMenu(command);
  };
  const installed = [];
  try {
    await api.signOut();
    for (const kind of ["skill", "plugin"]) {
      await progress(kind + " stale native removal consent");
      const a = await install(o, root, kind, false);
      installed.push(a);
      const path = join(a.store.destination, a.plan.name);
      if (kind === "plugin")
        await chat().update(
          "pluginLocations",
          { ...chat().get("pluginLocations", {}), [path]: true },
          vscode.ConfigurationTarget.Global,
        );
      await vscode.commands.executeCommand("notifications.clearAll");
      await action(
        a.plan.name,
        kind === "plugin"
          ? "Remove Installed Plugin"
          : "Remove Installed Skill",
      );
      await ui
        .until(
          "document.querySelector('.monaco-dialog-box')?.textContent.includes('Remove')",
        )
        .catch(async (error) => {
          const diagnostic = await ui.evaluate(
            "({active:document.activeElement?.outerHTML.slice(0,1200),notifications:[...document.querySelectorAll('.notification-list-item-message')].map(e=>e.textContent),row:[...document.querySelectorAll('.monaco-list-row')].find(r=>r.textContent.includes('catalog-owned'))?.outerHTML.slice(-2500)})",
          );
          throw new Error(
            error.message + " owned removal DOM=" + JSON.stringify(diagnostic),
            { cause: error },
          );
        });
      const b = await install(o, root, kind, true);
      const before = { ...chat().get("pluginLocations", {}) };
      await click(kind === "plugin" ? "Remove Plugin" : "Remove Skill");
      await ui.until(
        "document.body.innerText.includes('Catalog: selection_changed.')",
      );
      assert.equal((await b.store.inspect(b.plan)).identical, true);
      assert.deepEqual(chat().get("pluginLocations", {}), before);
      await writeFile(
        join(b.store.state, "receipts/damaged.json"),
        "private-local-corruption",
      );
      await vscode.commands.executeCommand("openAgenticCatalog.refreshTools");
      await vscode.commands.executeCommand("openAgenticCatalog.tools.focus");
      await ui.until(
        `document.body.innerText.includes(${JSON.stringify(b.plan.name)})&&document.body.innerText.includes('damaged.json')`,
      );
      assert.equal(
        await ui.evaluate(
          "document.body.innerText.includes('private-local-corruption')",
        ),
        false,
      );
      if (kind === "skill") {
        await progress("native contained reveal");
        await action(b.plan.name, "Reveal Owned Package");
        try {
          await ui.until(
            "[...document.querySelectorAll('.explorer-viewlet .monaco-list-row[aria-selected=true]')].some(row=>row.textContent.includes('owned-skill'))",
          );
        } catch (error) {
          throw new Error(
            error.message +
              " explorer=" +
              JSON.stringify(
                await ui.evaluate(
                  "[...document.querySelectorAll('.explorer-viewlet .monaco-list-row')].map(row=>({text:row.textContent,selected:row.getAttribute('aria-selected'),cls:row.className}))",
                ),
              ) +
              " native=" +
              (await ui.evaluate("document.body.innerText.slice(-2500)")),
            { cause: error },
          );
        }
      } else {
        await progress(
          "native healthy disable and pre-journal recovery beside damaged receipt",
        );
        await action(b.plan.name, "Disable Installed Plugin");
        await wait(
          () => chat().get("pluginLocations", {})[path] === false,
          "healthy disable",
        );
        const pending = join(b.store.destination, "pending-install"),
          invalid = b.store.destination + "/../unrelated-key";
        await mkdir(pending, { mode: 0o700 });
        await writeFile(
          join(pending, "unowned.txt"),
          "Preserve this unowned tree",
        );
        const beforeRecovery = {
          ...chat().get("pluginLocations", {}),
          [path]: true,
          [pending]: true,
          [invalid]: true,
        };
        await chat().update(
          "pluginLocations",
          beforeRecovery,
          vscode.ConfigurationTarget.Global,
        );
        const raced = api.recoverPlugin(root);
        await ui.until(
          "document.querySelector('.monaco-dialog-box')?.textContent.includes('Paths to disable, including pending registrations:')",
        );
        const reviewed = await ui.evaluate(
          "document.querySelector('.monaco-dialog-box').textContent",
        );
        assert.ok(reviewed.includes(JSON.stringify(path)));
        assert.ok(reviewed.includes(JSON.stringify(pending)));
        assert.equal(reviewed.includes(JSON.stringify(invalid)), false);
        const late = join(b.store.destination, "late-registration"),
          changed = { ...beforeRecovery, [late]: true };
        await chat().update(
          "pluginLocations",
          changed,
          vscode.ConfigurationTarget.Global,
        );
        await click("Recover Plugin operation");
        await assert.rejects(raced, /selection_changed/);
        assert.deepEqual(chat().get("pluginLocations", {}), changed);
        assert.equal((await b.store.inspect(b.plan)).identical, true);
        await assert.rejects(readFile(join(b.store.state, "journal.json")), {
          code: "ENOENT",
        });
        const recovering = api.recoverPlugin(root);
        await ui.until(
          `document.querySelector('.monaco-dialog-box')?.textContent.includes(${JSON.stringify(JSON.stringify(late))})`,
        );
        await click("Recover Plugin operation");
        await recovering;
        const after = chat().get("pluginLocations", {});
        assert.equal(after[path], false);
        assert.equal(after[pending], false);
        assert.equal(after[late], false);
        assert.equal(after[invalid], true);
        for (const [key, value] of Object.entries(beforeRecovery))
          if (![path, pending].includes(key)) assert.equal(after[key], value);
        assert.equal(
          await readFile(join(pending, "unowned.txt"), "utf8"),
          "Preserve this unowned tree",
        );
        await rm(pending, { recursive: true });
      }
      await progress(
        kind + " current native removal remains usable beside damaged receipt",
      );
      await action(
        b.plan.name,
        kind === "plugin"
          ? "Remove Installed Plugin"
          : "Remove Installed Skill",
      );
      await click(kind === "plugin" ? "Remove Plugin" : "Remove Skill");
      await wait(
        async () => !(await b.store.get(b.plan.name)),
        "current receipt removed",
      );
      assert.equal(
        await readFile(join(b.store.state, "receipts/damaged.json"), "utf8"),
        "private-local-corruption",
      );
      await rm(join(b.store.state, "receipts/damaged.json"));
    }
    await progress("local tools native checks complete");
  } finally {
    capture.dispose();
    await chat().update(
      "pluginLocations",
      original,
      vscode.ConfigurationTarget.Global,
    );
    for (const { store } of installed)
      await rm(join(store.state, "receipts/damaged.json"), { force: true });
    ui.close();
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  }
};
exports.duplicateRoots = async (api) => {
  const roots = vscode.workspace.workspaceFolders.map((f) => f.uri.fsPath),
    o = await owners();
  assert.equal(roots.length, 2);
  const a = await install(o, roots[0], "skill", false),
    b = await install(o, roots[1], "skill", true);
  const row = {
    store: { workspace: roots[1] },
    receipt: (await b.store.list())[0],
  };
  const removing = vscode.commands.executeCommand(
    "openAgenticCatalog.removeSkill",
    row,
  );
  const ui = await require("./webview.cjs").webview(true);
  try {
    await ui.until(
      `document.querySelector('.monaco-dialog-box')?.textContent.includes(${JSON.stringify(roots[1])})`,
    );
    await ui.evaluate(
      "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent==='Remove Skill').click()",
    );
    await removing;
    assert.equal(await b.store.get(b.plan.name), undefined);
    assert.equal((await a.store.inspect(a.plan)).identical, true);
  } finally {
    ui.close();
    await a.store.remove(a.plan.name, () => {}, await a.store.get(a.plan.name));
  }
  await api.signOut();
};
