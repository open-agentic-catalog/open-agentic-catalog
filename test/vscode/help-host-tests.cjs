const assert = require("node:assert/strict");
const vscode = require("vscode");
const progress = (stage) =>
  require("node:fs").appendFileSync(
    require("node:path").join(
      process.env.CATALOG_VSCODE_SCREENSHOTS,
      "help-progress.log",
    ),
    stage + "\n",
  );
const sections = [
  ["start", "first-use-and-command-index"],
  ["setup", "add-a-connection"],
  ["connection", "sign-in-and-sign-out"],
  ["trust", "trust-proxies-and-limits"],
  ["catalog", "browse-the-catalog"],
  ["tools", "manage-my-tools"],
  ["agent", "agent-outcomes"],
];
exports.section = async (anchor) => {
  const guide = await require("./webview.cjs").webview("guide");
  try {
    await guide.until(
      `(()=>{const e=document.getElementById(${JSON.stringify(anchor)});if(!e)return false;const r=e.getBoundingClientRect();return r.bottom>0 && r.top<innerHeight;})()`,
    );
  } finally {
    guide.close();
  }
};
exports.verify = async () => {
  progress("start Help verification");
  for (const [topic, anchor] of sections) {
    await vscode.commands.executeCommand("openAgenticCatalog.help", topic);
    await exports.section(anchor);
  }
  // The unchanged command ID is also useful without a caller-supplied topic.
  const picker = vscode.commands.executeCommand("openAgenticCatalog.help");
  const native = await require("./webview.cjs").webview(true);
  try {
    await native.until(
      "document.querySelector('.quick-input-title')?.textContent==='Catalog Help (offline)'",
    );
    await native.key("Enter");
    await picker;
  } finally {
    native.close();
  }
  await exports.section("first-use-and-command-index");
  progress("topics and picker passed");
  const guide = await require("./webview.cjs").webview("guide");
  try {
    await guide.until(
      "document.readyState==='complete' && document.images.length===4",
    );
    for (let index = 0; index < 4; index++) {
      await guide.until(
        `document.images[${index}].complete && document.images[${index}].naturalWidth>0`,
      );
    }
    const anchors = await guide.evaluate(
      "[...document.querySelectorAll('a[href^=\"#\"]')].map(a=>a.getAttribute('href').slice(1))",
    );
    assert.ok(
      anchors.length >= 4,
      "Offline resource-workflow links remain present",
    );
    for (const anchor of anchors)
      assert.equal(
        await guide.evaluate(
          `!!document.getElementById(${JSON.stringify(anchor)})`,
        ),
        true,
        anchor,
      );
    await guide.evaluate(
      "document.querySelector('a[href^=\"#\"]').scrollIntoView({block:'center',inline:'nearest'})",
    );
    await guide.until(
      "(()=>{const r=document.querySelector('a[href^=\"#\"]').getBoundingClientRect();return r.top>innerHeight/4 && r.bottom<innerHeight*3/4;})()",
    );
    await guide.click("document.querySelector('a[href^=\"#\"]')");
    try {
      await guide.until(
        `(()=>{const r=document.getElementById(${JSON.stringify(anchors[0])}).getBoundingClientRect();return r.bottom>0 && r.top<innerHeight;})()`,
      );
    } catch (error) {
      const state = await guide.evaluate(
        `(()=>{const target=document.getElementById(${JSON.stringify(anchors[0])});const link=document.querySelector('a[href^="#"]');const r=target.getBoundingClientRect();const a=link.getBoundingClientRect();return {hash:location.hash,viewport:innerHeight,scrollTop:document.scrollingElement.scrollTop,scrollHeight:document.scrollingElement.scrollHeight,target:{top:r.top,bottom:r.bottom},link:{top:a.top,bottom:a.bottom,href:link.getAttribute('href')},active:document.activeElement?.tagName};})()`,
      );
      const workbench = await require("./webview.cjs").webview(true);
      try {
        throw new Error(
          error.message +
            " guide=" +
            JSON.stringify(state) +
            " workbench=" +
            (await workbench.evaluate("document.body.innerText.slice(-4000)")),
          { cause: error },
        );
      } finally {
        workbench.close();
      }
    }
    // Finish the section-link check before image scrolling can trigger the
    // native preview's editor/scroll synchronization.
    for (let index = 0; index < 4; index++) {
      await guide.evaluate(`document.images[${index}].scrollIntoView()`);
      await guide.until(
        `document.images[${index}].complete && document.images[${index}].naturalWidth>0`,
      );
    }
  } finally {
    guide.close();
  }
  progress("inline images and section link passed");
};
exports.images = async () => {
  // Activate the real Markdown links rather than issuing an image-open command.
  // VS Code reports image dimensions only after its native preview loads bytes.
  const extension = vscode.extensions.getExtension(
    "open-agentic-catalog.open-agentic-catalog",
  );
  assert.ok(extension);
  for (const [name, label] of [
    ["catalog-preview.png", "Open the Catalog screenshot at full size"],
    ["agent-conversation.png", "Open the Agent screenshot at full size"],
    [
      "mcp-selection-preview.png",
      "Open the MCP selection screenshot at full size",
    ],
    ["my-tools-installed.png", "Open the My tools screenshot at full size"],
  ]) {
    progress("opening full-size " + name);
    const expected = vscode.Uri.joinPath(extension.extensionUri, "media", name);
    const imageGuide = await require("./webview.cjs").webview("guide");
    try {
      const link = `[...document.querySelectorAll('a')].find(a=>a.textContent===${JSON.stringify(label)})`;
      await imageGuide.evaluate(`${link}.scrollIntoView({block:'center'})`);
      await imageGuide.click(link);
      const workbench = await require("./webview.cjs").webview(true);
      try {
        await workbench.until(
          "document.querySelector('[id$=\"status.imagePreview.size\"]')?.textContent.trim()==='1440x1150'",
        );
      } catch (error) {
        throw new Error(
          error.message +
            " image=" +
            name +
            " workbench=" +
            (await workbench.evaluate("document.body.innerText.slice(-4000)")),
          { cause: error },
        );
      } finally {
        workbench.close();
      }
      const tab = vscode.window.tabGroups.activeTabGroup.activeTab;
      assert.ok(
        tab?.input instanceof vscode.TabInputCustom,
        "Native image preview",
      );
      assert.equal(tab.input.viewType, "imagePreview.previewEditor");
      // The Markdown renderer retains its current section fragment when
      // resolving a relative image. It must still open the exact local file.
      assert.ok(
        ["", "first-use-and-command-index"].includes(tab.input.uri.fragment),
      );
      assert.equal(
        tab.input.uri.with({ fragment: "" }).toString(),
        expected.toString(),
      );
      progress("decoded exact full-size " + name);
    } finally {
      imageGuide.close();
      const owned = vscode.window.tabGroups.all.flatMap((group) =>
        group.tabs.filter(
          (tab) =>
            tab.input instanceof vscode.TabInputCustom &&
            tab.input.uri.with({ fragment: "" }).toString() ===
              expected.toString(),
        ),
      );
      if (owned.length) await vscode.window.tabGroups.close(owned);
      progress("closed full-size " + name);
    }
    await vscode.commands.executeCommand("openAgenticCatalog.help", "start");
    progress("returning to guide from " + name);
    await exports.section("first-use-and-command-index");
    progress("guide restored after " + name);
  }
};
