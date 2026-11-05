import assert from "node:assert/strict";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { join, resolve } from "node:path";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";

export async function lifecycle({
  work,
  driver,
  launchArgs,
  editorEnv,
  developmentHost,
  cli,
  args,
  profileSettings,
  root,
}) {
  const user = join(work, "lifecycle-user"),
    extensions = join(work, "lifecycle-extensions"),
    workspace = join(work, "lifecycle-workspace");
  await mkdir(join(user, "User"), { recursive: true });
  await mkdir(workspace);
  await writeFile(join(user, "User/settings.json"), profileSettings);
  const original = process.env.CATALOG_VSCODE_VSIX;
  const version = JSON.parse(
    await readFile(join(root, "extensions/vscode/package.json"), "utf8"),
  ).version;
  const profile = [
    "--shared-data-dir",
    join(work, "lifecycle-shared"),
    "--user-data-dir",
    user,
    "--extensions-dir",
    extensions,
  ];
  const invoke = (...command) =>
    execFileSync(cli, [...args, ...profile, ...command], {
      timeout: 60000,
      env: { ...process.env, DONT_PROMPT_WSL_INSTALL: "1" },
      stdio: "pipe",
    }).toString();
  const results = [];
  let hostIdentity;
  const settings =
    JSON.parse(profileSettings).openAgenticCatalog?.connections ??
    JSON.parse(profileSettings)["openAgenticCatalog.connections"];
  for (const [stage, selectedVersion, marker] of [
    ["setup", version, null],
    ["same-version", version, "same-version-replacement"],
    ["updated", "0.2.0", "version-update"],
    ["rollback", version, "deliberate-rollback"],
    ["reinstalled", version, null],
  ]) {
    const vsix = join(work, stage + ".vsix");
    execFileSync(
      "python3",
      [
        "-B",
        "-c",
        `import json,sys,zipfile
source,target,version,marker=sys.argv[1:]
marker=json.loads(marker)
with zipfile.ZipFile(source) as original, zipfile.ZipFile(target,'w',compression=zipfile.ZIP_DEFLATED) as output:
 for name in original.namelist():
  value=original.read(name)
  if name=='extension/package.json':
   metadata=json.loads(value); previous=metadata['version']; metadata['version']=version; value=json.dumps(metadata).encode()
  elif name=='extension.vsixmanifest':
   previous=json.loads(original.read('extension/package.json'))['version']; value=value.replace(('Version="'+previous+'"').encode(),('Version="'+version+'"').encode())
  elif name=='extension/dist/extension.cjs' and marker:
   value+=('\\nconst fixtureActivate=module.exports.activate;module.exports={...module.exports,activate:async(...args)=>({...await fixtureActivate(...args),__catalogFixtureBuild:'+json.dumps(marker)+'})};\\n').encode()
  output.writestr(name,value)
`,
        original,
        vsix,
        selectedVersion,
        JSON.stringify(marker),
      ],
      { stdio: "pipe", timeout: 30000 },
    );
    if (stage === "reinstalled") {
      invoke(
        "--uninstall-extension",
        "open-agentic-catalog.open-agentic-catalog",
      );
      assert.ok(
        !invoke("--list-extensions")
          .split(/\r?\n/u)
          .includes("open-agentic-catalog.open-agentic-catalog"),
      );
      assert.match(
        await readFile(
          join(workspace, ".agents/skills/owned-skill/SKILL.md"),
          "utf8",
        ),
        /owned-skill/u,
      );
      assert.equal(
        await readFile(join(workspace, "saved-export.json"), "utf8"),
        '{"owned":"export"}\n',
      );
    }
    invoke("--install-extension", vsix, "--force");
    const resultFile = join(work, stage + "-result.json");
    await writeFile(
      join(driver, "index.cjs"),
      `const vscode=require('vscode');const fs=require('node:fs');exports.activate=async()=>{try{const outcome=await require(${JSON.stringify(resolve(import.meta.dirname, "lifecycle-host-tests.cjs"))}).run(${JSON.stringify(stage)},${JSON.stringify(selectedVersion)},${JSON.stringify(marker)});fs.writeFileSync(${JSON.stringify(resultFile)},JSON.stringify({passed:true,outcome}));}catch(e){fs.writeFileSync(${JSON.stringify(resultFile)},JSON.stringify({passed:false,error:String(e.stack??e)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}};`,
    );
    const selectedArgs = launchArgs.map((arg) =>
      arg === join(work, "user")
        ? user
        : arg === join(work, "shared")
          ? join(work, "lifecycle-shared")
          : arg === join(work, "extensions")
            ? extensions
            : arg === join(work, "workspace")
              ? workspace
              : arg,
    );
    await developmentHost(
      selectedArgs,
      {
        ...editorEnv,
        CATALOG_VSCODE_DEBUG_DIR: user,
        CATALOG_VSCODE_PROFILE_ARGS: JSON.stringify(profile),
      },
      resultFile,
      60000,
    );
    const result = JSON.parse(await readFile(resultFile, "utf8")).outcome;
    const installed = await readFile(
      join(result.installedPath, "dist/extension.cjs"),
    );
    const expected = execFileSync(
      "python3",
      [
        "-B",
        "-c",
        "import sys,zipfile;sys.stdout.buffer.write(zipfile.ZipFile(sys.argv[1]).read('extension/dist/extension.cjs'))",
        vsix,
      ],
      { maxBuffer: 4 * 1024 * 1024, timeout: 10000 },
    );
    assert.deepEqual(
      installed,
      expected,
      "Installed payload differs from selected fixture VSIX",
    );
    assert.deepEqual(
      JSON.parse(await readFile(join(user, "User/settings.json"), "utf8"))[
        "openAgenticCatalog.connections"
      ],
      settings,
    );
    const identity = await readFile(
      join(
        user,
        "User/globalStorage/open-agentic-catalog.open-agentic-catalog/connection-host-id",
      ),
      "utf8",
    );
    if (stage === "setup") hostIdentity = identity;
    else if (stage !== "reinstalled") assert.equal(identity, hostIdentity);
    results.push({
      ...result,
      installedPath: undefined,
      stage,
      hostIdentityPreserved: identity === hostIdentity,
      payloadSha256: createHash("sha256").update(installed).digest("hex"),
    });
  }
  await writeFile(
    join(root, "output/vscode/lifecycle.json"),
    JSON.stringify(
      {
        status: "passed",
        scope:
          "owned persistent Linux profile; current-format derivative fixtures, not release compatibility",
        stages: results,
      },
      null,
      2,
    ) + "\n",
  );
  console.log(
    "Installed VSIX lifecycle passed: same-version replacement, update, rollback, uninstall/reinstall, current payload activation, settings/credentials/bookmarks, retained Skill ownership and separate offline removal",
  );
}
