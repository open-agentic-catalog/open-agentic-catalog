// The installed VSIX inspects reviewed metadata without installing its packages.
const vscode = require("vscode");
const assert = require("node:assert/strict");
const { readFile } = require("node:fs/promises");

exports.run = async (api) => {
  const corpus = JSON.parse(
    await readFile(process.env.CATALOG_VSCODE_CORPUS, "utf8"),
  );
  assert.equal(corpus.cases.length, 6);
  const configuration = vscode.workspace.getConfiguration("openAgenticCatalog");
  const previous = configuration.get("connections");
  const id = "reviewed-upstream";
  const inspect = async (index, expected, json) => {
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    const web = await require("./webview.cjs").webview();
    try {
      const detail = api.browsing().detail;
      await web.until(
        `document.querySelector('.detail > code')?.textContent===${JSON.stringify(detail.item.identifier)} && document.querySelectorAll('.document-row').length===${detail.documents.length} && !document.querySelector('#search-submit').disabled`,
      );
      const selector = `[...document.querySelectorAll('.document-row')][${index}].querySelector('button[id^="action-inspect-"]')`;
      await web.evaluate(
        `document.querySelector('#all-documents').open=true;(${selector}).scrollIntoView({block:'center'})`,
      );
      const before = vscode.window.activeTextEditor?.document;
      await web.click(selector);
      const deadline = performance.now() + 10000;
      while (performance.now() < deadline) {
        const document = vscode.window.activeTextEditor?.document;
        if (
          document &&
          document !== before &&
          document.uri.scheme === "oac-evidence"
        ) {
          if (json) assert.deepEqual(JSON.parse(document.getText()), expected);
          else assert.equal(document.getText(), expected);
          assert.equal(document.isDirty, false);
          return;
        }
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      throw new Error("Reviewed corpus native inspection did not open");
    } finally {
      web.close();
    }
  };
  try {
    await configuration.update(
      "connections",
      [
        ...previous,
        {
          id,
          label: "Reviewed upstream",
          origin: corpus.origin,
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
      ],
      vscode.ConfigurationTarget.Global,
    );
    await api.select(id);
    await api.authenticate("reader");
    const selections = api.mcpSelections();
    const seen = new Set();
    let page = await api.browse({ limit: 2 });
    for (let index = 0; index < 3; index++) {
      assert.equal(page.items.length, 2);
      for (const item of page.items) {
        assert.ok(!seen.has(item.identifier));
        seen.add(item.identifier);
      }
      assert.equal(Boolean(page.next), index < 2);
      if (page.next) page = await api.next();
    }
    assert.deepEqual(seen, new Set(corpus.cases.map((c) => c.identifier)));
    for (const expected of corpus.cases) {
      // A refused external document deliberately clears the old page/detail.
      // Start each independent case with one current browse, without retries.
      page = await api.browse({ limit: 100 });
      const item = page.items.find(
        (item) => item.identifier === expected.identifier,
      );
      assert.ok(item);
      assert.equal(item.kind, expected.kind);
      assert.equal(item.releaseId, expected.releaseId);
      const detail = await api.detail(item.key);
      if (expected.data) {
        const document = await api.document(0);
        assert.deepEqual(
          JSON.parse(Buffer.from(document.bytes).toString("utf8")),
          expected.data,
        );
        if (expected.kind === "agent") await inspect(0, expected.data, true);
      }
      if (expected.text) {
        const document = await api.document(0);
        assert.equal(
          Buffer.from(document.bytes).toString("utf8"),
          expected.text,
        );
        await inspect(0, expected.text, false);
      }
      if (expected.fileCount) {
        assert.equal(detail.packages.length, 1);
        const representation = detail.packages[0];
        const packageData =
          representation[expected.kind === "plugin" ? "plugin" : "package"];
        assert.equal(packageData.files.length, expected.fileCount);
        for (const [key, value] of Object.entries(expected.representation))
          assert.deepEqual(representation[key], value);
      }
      if (expected.documents) {
        assert.equal(
          detail.documents.length,
          Object.keys(expected.documents).length + 1,
        );
        for (const [path, content] of Object.entries(expected.documents)) {
          const index = detail.documents.findIndex((d) => d.label === path);
          assert.ok(index > 0);
          assert.equal(
            Buffer.from((await api.document(index)).bytes).toString("utf8"),
            content,
          );
        }
      }
      if (expected.fileCount) {
        // Do this after retained documents: this refusal invalidates the page.
        await assert.rejects(
          api.document(0),
          /external_document_needs_confirmation/u,
        );
      }
    }
    assert.deepEqual(api.mcpSelections(), selections);
    await api.signOut();
    await api.authenticate("other");
    assert.equal((await api.browse()).items.length, 0);
    console.log(
      "Installed VSIX: six reviewed upstream cases, exact native documents, complete package metadata, tenant denial and inert handoffs passed",
    );
  } finally {
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    await api.signOut();
    await configuration.update(
      "connections",
      previous,
      vscode.ConfigurationTarget.Global,
    );
  }
};
