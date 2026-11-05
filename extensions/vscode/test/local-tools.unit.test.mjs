import test from "node:test";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";
import {
  mkdtemp,
  rm,
  writeFile,
  readFile,
  mkdir,
  readdir,
  symlink,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { files, representation, content, hash } from "./package-fixtures.mjs";
import { fixture as pluginFixture } from "./plugin-fixtures.mjs";
const lib = await import(process.env.CATALOG_VSCODE_LIB);
const { SkillStore, skillPlan, pluginPlan, unpackPackage, packageHash } = lib;
let f;
const uri = (path) => ({
  scheme: "file",
  fsPath: path,
  toString: () => `file://${path}`,
});
const hooks = registerHooks({
  resolve(specifier, context, next) {
    return specifier === "vscode"
      ? { url: "local-tools-unit:vscode", shortCircuit: true }
      : next(specifier, context);
  },
  load(url, context, next) {
    if (url !== "local-tools-unit:vscode") return next(url, context);
    return {
      format: "module",
      source:
        "export const {window,workspace,commands,Uri,EventEmitter,TreeItem,ThemeIcon,ConfigurationTarget}=globalThis.localToolsVscode;",
      shortCircuit: true,
    };
  },
});
globalThis.localToolsVscode = {
  EventEmitter: class {
    event = () => ({ dispose() {} });
    fire() {}
    dispose() {}
  },
  TreeItem: class {
    constructor(label) {
      this.label = label;
    }
  },
  ThemeIcon: class {
    constructor(id) {
      this.id = id;
    }
  },
  ConfigurationTarget: { Global: 1 },
  Uri: { joinPath: (base, ...paths) => uri(join(base.fsPath, ...paths)) },
  commands: {
    executeCommand: async (command, value) =>
      f.commands.push({ command, value }),
  },
  window: {
    createTreeView: (_, options) => {
      f.provider = options.treeDataProvider;
      return { dispose() {} };
    },
    showQuickPick: async (choices) => choices[0],
    showWarningMessage: async (title, options, ...choices) =>
      f.warning ? f.warning(title, options, choices) : choices[0],
    showInformationMessage: async () => {},
  },
  workspace: {
    get isTrusted() {
      return f.trusted;
    },
    get workspaceFolders() {
      return f.folders;
    },
    onDidChangeConfiguration: () => ({ dispose() {} }),
    onDidChangeWorkspaceFolders: () => ({ dispose() {} }),
    getConfiguration: (section) => ({
      inspect: (key) =>
        section === "chat" && key === "pluginLocations"
          ? { globalValue: f.locations }
          : {},
      get: (key, fallback) =>
        section === "chat" && key === "pluginLocations"
          ? f.locations
          : fallback,
      update: async (key, value) => {
        assert.equal(section, "chat");
        assert.equal(key, "pluginLocations");
        f.writes++;
        f.locations = value;
      },
    }),
  },
};
const base = pathToFileURL(process.env.CATALOG_VSCODE_LIB);
const { CatalogPlugins } = await import(new URL("./plugins-host.js", base));
const { CatalogSkills } = await import(new URL("./skills-host.js", base));
test.after(() => {
  hooks.deregister();
  delete globalThis.localToolsVscode;
});
async function setup(t) {
  const root = await mkdtemp(join(tmpdir(), "catalog-local-tools-unit-"));
  f = {
    root,
    trusted: true,
    folders: [{ name: "Owned root", uri: uri(root) }],
    locations: { "/unrelated/plugin": true },
    writes: 0,
    previews: [],
    commands: [],
  };
  const browser = {
    state: { detail: { item: { kind: "skill", releaseId: "owned-release" } } },
    packageConsumer: () => async () => f.representation,
  };
  const session = {
    guard: () => () => {},
    connection: () => ({}),
    consumerBinding: () => "owned account and host",
  };
  const plugins = new CatalogPlugins(browser, session, async (value) =>
    f.previews.push(value),
  );
  const skills = new CatalogSkills(
    browser,
    session,
    {
      selections: { selections: new Map() },
      onDidChangeSelections: () => ({ dispose() {} }),
    },
    plugins,
    async (value) => f.previews.push(value),
  );
  t.after(async () => {
    skills.dispose();
    plugins.dispose();
    await rm(root, { recursive: true, force: true });
  });
  return { root, plugins, skills, browser, session };
}
async function installed(root, kind, replacement = false) {
  const store = new SkillStore(root, kind);
  let plan, bytes;
  if (kind === "plugin") {
    const value = pluginFixture(
      replacement ? { "readme.txt": "Replacement" } : {},
    );
    plan = value.plan;
    bytes = unpackPackage(plan, value.archive);
  } else {
    const entries = files(replacement ? "Replacement" : "Original");
    plan = skillPlan(representation(entries), hash("owned"));
    bytes = content(entries);
  }
  await store.install(plan, bytes, async () => {});
  return { store, plan };
}
test("healthy receipts remain listed and actionable beside damaged receipts", async (t) => {
  const { root, plugins, skills } = await setup(t);
  const skill = await installed(root, "skill"),
    plugin = await installed(root, "plugin");
  for (const { store } of [skill, plugin])
    await writeFile(
      join(store.state, "receipts", "damaged.json"),
      "{private-local-corruption",
    );
  const rows = await skills.rows();
  assert.equal(rows.filter((row) => "store" in row).length, 1);
  assert.equal(rows.filter((row) => "pluginStore" in row).length, 1);
  assert.equal(rows.filter((row) => "issue" in row).length, 2);
  for (const row of rows.filter((row) => "issue" in row)) {
    const item = f.provider.getTreeItem(row);
    assert.equal(item.iconPath.id, "warning");
    assert.ok(
      item.command.command.endsWith(
        row.kind === "plugin" ? "recoverPlugin" : "recoverSkill",
      ),
    );
    assert.equal(
      JSON.stringify(item).includes("private-local-corruption"),
      false,
    );
  }
  const path = join(plugin.store.destination, plugin.plan.name);
  f.locations[path] = true;
  await plugins.disable(plugin.plan.name, root);
  assert.equal(f.locations[path], false);
  await skills.remove(skill.plan.name, root);
  assert.equal(await skill.store.get(skill.plan.name), undefined);
  assert.equal(
    await readFile(join(skill.store.state, "receipts/damaged.json"), "utf8"),
    "{private-local-corruption",
  );
  assert.equal(f.locations["/unrelated/plugin"], true);
});
test("Skill and Plugin removal consent cannot transfer to a replacement receipt", async (t) => {
  for (const kind of ["skill", "plugin"]) {
    const { root, plugins, skills } = await setup(t);
    const a = await installed(root, kind);
    const originalNode = f.provider.getTreeItem(
      (await skills.rows()).find((row) => row.receipt?.name === a.plan.name),
    );
    const path = join(a.store.destination, a.plan.name);
    if (kind === "plugin") f.locations[path] = true;
    const entered = Promise.withResolvers(),
      decision = Promise.withResolvers();
    f.warning = async () => {
      entered.resolve();
      return decision.promise;
    };
    const removing =
      kind === "plugin"
        ? plugins.remove(a.plan.name, root)
        : skills.remove(a.plan.name, root);
    await entered.promise;
    const b = await installed(root, kind, true);
    const replacedNode = f.provider.getTreeItem(
      (await skills.rows()).find((row) => row.receipt?.name === b.plan.name),
    );
    assert.notEqual(
      originalNode.id,
      replacedNode.id,
      "A replaced receipt has a new native row identity",
    );
    assert.equal(
      replacedNode.id,
      f.provider.getTreeItem(
        (await skills.rows()).find((row) => row.receipt?.name === b.plan.name),
      ).id,
      "Unchanged receipts keep their native row identity",
    );
    const settings = structuredClone(f.locations),
      writes = f.writes;
    decision.resolve(kind === "plugin" ? "Remove Plugin" : "Remove Skill");
    await assert.rejects(removing, { code: "selection_changed" });
    assert.equal((await b.store.inspect(b.plan)).identical, true);
    assert.deepEqual(f.locations, settings);
    assert.equal(f.writes, writes);
    assert.equal(
      (await readdir(b.store.state)).includes("journal.json"),
      false,
    );
    f.warning = undefined;
    if (kind === "plugin") await plugins.remove(b.plan.name, root);
    else await skills.remove(b.plan.name, root);
    assert.equal(await b.store.get(b.plan.name), undefined);
  }
});
test("Plugin recovery disables healthy and valid pending paths despite unrelated damaged entries", async (t) => {
  const { root, plugins } = await setup(t),
    p = await installed(root, "plugin");
  const owned = join(p.store.destination, p.plan.name),
    pending = join(p.store.destination, "pending-owned"),
    invalid = p.store.destination + "/../do-not-touch";
  await mkdir(pending, { mode: 0o700 });
  await writeFile(
    join(pending, "unowned.txt"),
    "Never adopt or delete this tree",
  );
  await writeFile(join(p.store.state, "receipts", "damaged.json"), "invalid");
  f.locations = {
    "/unrelated/plugin": true,
    [owned]: true,
    [pending]: true,
    [invalid]: true,
  };
  await plugins.recover(root);
  assert.equal(f.locations[owned], false);
  assert.equal(f.locations[pending], false);
  assert.equal(f.locations[invalid], true);
  assert.equal(f.locations["/unrelated/plugin"], true);
  assert.equal(
    await readFile(join(pending, "unowned.txt"), "utf8"),
    "Never adopt or delete this tree",
  );
  assert.equal(
    await readFile(join(p.store.state, "receipts/damaged.json"), "utf8"),
    "invalid",
  );
  assert.equal((await p.store.inspect(p.plan)).identical, true);
  const another = await mkdtemp(join(tmpdir(), "catalog-pre-journal-unit-"));
  t.after(() => rm(another, { recursive: true, force: true }));
  f.folders.push({ name: "Pre-journal root", uri: uri(another) });
  const preJournal = join(
    another,
    ".oac-catalog/plugins/installed/first-install",
  );
  f.locations[preJournal] = true;
  await plugins.recover(another);
  assert.equal(f.locations[preJournal], false);
  assert.deepEqual(
    await readdir(another),
    [],
    "a registration key never creates or adopts package files",
  );
});
test("Plugin recovery presents exact paths before consent and cancellation writes nothing", async (t) => {
  const { root, plugins } = await setup(t),
    p = await installed(root, "plugin"),
    owned = join(p.store.destination, p.plan.name),
    pending = join(p.store.destination, "pending-owned"),
    invalid = p.store.destination + "/../do-not-touch";
  f.locations = { [owned]: true, [pending]: true, [invalid]: true };
  let consent;
  f.warning = async (title, options) => {
    consent = { title, options };
    return undefined;
  };
  const before = structuredClone(f.locations);
  await plugins.recover(root);
  assert.equal(f.writes, 0);
  assert.deepEqual(f.locations, before);
  assert.equal((await p.store.inspect(p.plan)).identical, true);
  assert.equal(typeof consent.options.detail, "string");
  assert.ok(consent.options.detail.includes(JSON.stringify(owned)));
  assert.ok(consent.options.detail.includes(JSON.stringify(pending)));
  assert.equal(consent.options.detail.includes(JSON.stringify(invalid)), false);
});
test("Plugin recovery refuses registration or receipt changes during consent and preserves unrelated edits", async (t) => {
  for (const change of ["registration", "receipt", "unrelated"]) {
    const { root, plugins } = await setup(t),
      a = await installed(root, "plugin"),
      owned = join(a.store.destination, a.plan.name),
      late = join(a.store.destination, "late-registration");
    f.locations[owned] = true;
    let current = a,
      before;
    f.warning = async () => {
      if (change === "registration") f.locations[late] = true;
      else if (change === "receipt")
        current = await installed(root, "plugin", true);
      else f.locations["/unrelated/new-plugin"] = true;
      before = structuredClone(f.locations);
      return "Recover Plugin operation";
    };
    if (change === "unrelated") {
      await plugins.recover(root);
      assert.equal(f.locations[owned], false);
      assert.equal(f.locations["/unrelated/new-plugin"], true);
    } else {
      await assert.rejects(plugins.recover(root), {
        code: "selection_changed",
      });
      assert.equal(f.writes, 0);
      assert.deepEqual(f.locations, before);
    }
    assert.equal((await current.store.inspect(current.plan)).identical, true);
    assert.equal(
      (await readdir(current.store.state)).includes("journal.json"),
      false,
    );
  }
});
test("reveal rechecks trust, receipt and contained exact files without executing them", async (t) => {
  const { root, skills } = await setup(t),
    p = await installed(root, "skill");
  const row = (await skills.rows()).find((row) => "store" in row);
  await skills.reveal(row);
  assert.equal(f.commands.at(-1).command, "revealInExplorer");
  assert.equal(
    f.commands.at(-1).value.fsPath,
    join(p.store.destination, p.plan.name),
  );
  const calls = f.commands.length;
  f.trusted = false;
  await assert.rejects(skills.reveal(row), {
    code: "trusted_workspace_required",
  });
  f.trusted = true;
  await writeFile(
    join(p.store.destination, p.plan.name, "SKILL.md"),
    "local edit",
  );
  await assert.rejects(skills.reveal(row), { code: "local_skill_changed" });
  assert.equal(f.commands.length, calls);
});
test("comparison reads inventories and reports edits without acquisition, writes or activation", async (t) => {
  const { root, skills, plugins, browser } = await setup(t);
  for (const kind of ["skill", "plugin"]) {
    const store = new SkillStore(root, kind),
      binding = packageHash("owned account and host");
    const p = pluginFixture({
      "references/change.txt": "Original",
      "references/remove.txt": "Remove from the next inventory",
    });
    const selected = kind === "plugin" ? p.representation : representation();
    const plan =
      kind === "plugin"
        ? pluginPlan(selected, binding)
        : skillPlan(selected, binding);
    plan.identity = packageHash(
      JSON.stringify([plan.identity, "owned-release"]),
    );
    await store.install(
      plan,
      kind === "plugin" ? unpackPackage(plan, p.archive) : content(),
      async () => {},
    );
    browser.state.detail = { item: { kind, releaseId: "owned-release" } };
    f.representation = selected;
    if (kind === "plugin")
      f.locations[join(store.destination, plan.name)] = true;
    const before = await readFile(
      join(store.state, "receipts", plan.name + ".json"),
    );
    const settings = structuredClone(f.locations),
      writes = f.writes;
    if (kind === "plugin") await plugins.compare(0, root);
    else await skills.compare(0, root);
    assert.match(f.previews.at(-1), /selected exact package is installed/);
    if (kind === "plugin")
      assert.match(f.previews.at(-1), /Native path: enabled/);
    const updated =
      kind === "plugin"
        ? pluginFixture({
            "references/change.txt": "Updated",
            "references/new.txt": "New inventory entry",
          }).representation
        : representation(
            files("Updated instructions").map((file) =>
              file.path === "owned-skill/references/help.txt"
                ? { ...file, path: "owned-skill/references/new.txt" }
                : file.path === "owned-skill/scripts/never-run.sh"
                  ? { ...file, executable: false }
                  : file,
            ),
          );
    f.representation = updated;
    if (kind === "plugin") await plugins.compare(0, root);
    else await skills.compare(0, root);
    const preview = f.previews.at(-1);
    assert.match(preview, /differs from the installed inventory/);
    assert.deepEqual(
      preview.split("\n").filter((line) => /^(Add|Remove|Change) /.test(line)),
      kind === "plugin"
        ? [
            "Change owned.tools/references/change.txt",
            "Add owned.tools/references/new.txt",
            "Remove owned.tools/references/remove.txt",
          ]
        : [
            "Change owned-skill/SKILL.md",
            "Remove owned-skill/references/help.txt",
            "Add owned-skill/references/new.txt",
            "Change owned-skill/scripts/never-run.sh",
          ],
    );
    if (kind === "plugin") assert.match(preview, /Native path: enabled/);
    assert.equal((await store.inspect(plan)).identical, true);
    assert.deepEqual(
      await readFile(join(store.state, "receipts", plan.name + ".json")),
      before,
    );
    assert.deepEqual(f.locations, settings);
    assert.equal(f.writes, writes);
    f.representation = selected;
    const file = join(
      store.destination,
      plan.name,
      kind === "plugin" ? "plugin.json" : "SKILL.md",
    );
    await writeFile(file, "local edit");
    if (kind === "plugin") await plugins.compare(0, root);
    else await skills.compare(0, root);
    assert.match(f.previews.at(-1), /Comparison refused: local_skill_changed/);
    assert.deepEqual(
      await readFile(join(store.state, "receipts", plan.name + ".json")),
      before,
    );
    assert.equal(await readFile(file, "utf8"), "local edit");
    assert.deepEqual(f.locations, settings);
    assert.equal(f.writes, writes);
    assert.deepEqual(
      (await readdir(store.state)).sort(),
      ["receipts"].concat(kind === "plugin" ? ["installed"] : []).sort(),
    );
  }
});
test("unsupported and excess roots and unsafe receipt directories are explicit rows", async (t) => {
  const { root, skills } = await setup(t);
  f.folders.push({
    name: "Virtual",
    uri: { scheme: "untitled", fsPath: "not-a-file" },
  });
  for (let index = 0; index < 21; index++)
    f.folders.push({
      name: `Root ${index}`,
      uri: uri(join(root, `missing-${index}`)),
    });
  const rows = await skills.rows();
  assert.ok(rows.some((row) => row.issue === "filesystem_workspace_required"));
  assert.ok(rows.some((row) => row.issue === "workspace_root_limit"));
  assert.ok(rows.some((row) => row.issue === "local_tools_unavailable"));
  f.folders = [{ name: "Owned", uri: uri(root) }];
  await symlink(root, join(root, ".oac-catalog"));
  assert.ok(
    (await skills.rows()).some(
      (row) => row.issue === "unsafe_installation_path",
    ),
  );
});
