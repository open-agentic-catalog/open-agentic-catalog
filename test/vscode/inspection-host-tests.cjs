const vscode = require("vscode");
const assert = require("node:assert/strict");
const { readFile, appendFile } = require("node:fs/promises");
const { join } = require("node:path");
const pause = () => new Promise((resolve) => setTimeout(resolve, 50));
async function until(read, label) {
  const deadline = performance.now() + 10000;
  while (performance.now() < deadline) {
    const value = await read();
    if (value) return value;
    await pause();
  }
  throw new Error("Inspection fixture deadline: " + label);
}
exports.run = async (api) => {
  const ui = await require("./webview.cjs").webview(true);
  const progress = (value) =>
    appendFile(
      join(process.env.CATALOG_VSCODE_DEBUG_DIR, "inspection-progress.log"),
      value + "\n",
    );
  const inspect = async (index, expected, language) => {
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    const web = await require("./webview.cjs").webview();
    const before = vscode.window.activeTextEditor?.document.uri.toString();
    try {
      const current = api.browsing().detail;
      await web.until(
        `document.querySelector('.detail > code')?.textContent===${JSON.stringify(current.item.identifier)} && document.querySelectorAll('.document-row').length===${current.documents.length} && !document.querySelector('#search-submit').disabled`,
      );
      const action = `[...document.querySelectorAll('.document-row')][${index}].querySelector('button[id^="action-inspect-"]')`;
      await web.evaluate(
        `document.querySelector('#all-documents').open=true;(${action}).scrollIntoView({block:'center'})`,
      );
      await web.click(action);
      let document;
      try {
        document = await until(() => {
          const d = vscode.window.activeTextEditor?.document;
          return d?.uri.scheme === "oac-evidence" &&
            d.uri.toString() !== before &&
            d.languageId === language
            ? d
            : undefined;
        }, "native evidence language");
      } catch (error) {
        throw new Error(
          error.message +
            " editor=" +
            JSON.stringify({
              uri: vscode.window.activeTextEditor?.document.uri.toString(),
              language: vscode.window.activeTextEditor?.document.languageId,
              tabs: vscode.window.tabGroups.all
                .flatMap((g) => g.tabs)
                .map((t) => t.label),
            }) +
            " native=" +
            (await ui.evaluate("document.body.innerText.slice(-3000)")),
          { cause: error },
        );
      }
      assert.equal(document.getText(), expected);
      assert.equal(document.isDirty, false);
      return document;
    } finally {
      web.close();
    }
  };
  try {
    await progress("large retained document collection");
    await api.select("qualification");
    await api.authenticate("reader");
    const page = await api.browse({ kind: "plugin" });
    const detail = await api.detail(page.items[0].key);
    assert.ok(
      detail.documents.length >= 166,
      `Actual Catalog returned ${detail.documents.length} documents`,
    );
    const expected = detail.documents[151].content;
    assert.equal(typeof expected, "string");
    await progress("read-only package comparison through the actual renderer");
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    const comparing = await require("./webview.cjs").webview();
    try {
      await comparing.until(
        "!!document.querySelector('button[id^=\"action-compare-\"]') && !document.querySelector('#search-submit').disabled",
      );
      await comparing.evaluate(
        "document.querySelector('button[id^=\"action-compare-\"]').click()",
      );
      await ui.until(
        "document.querySelector('.quick-input-title')?.textContent.includes('workspace')",
      );
      await vscode.commands.executeCommand(
        "workbench.action.acceptSelectedQuickOpenItem",
      );
      await until(
        () =>
          vscode.window.activeTextEditor?.document
            .getText()
            .includes("No owned installation exists."),
        "read-only comparison view",
      );
      assert.equal(
        vscode.window.activeTextEditor.document.languageId,
        "plaintext",
      );
      await assert.rejects(
        readFile(
          join(
            vscode.workspace.workspaceFolders[0].uri.fsPath,
            ".oac-catalog/plugins/journal.json",
          ),
        ),
      );
    } finally {
      comparing.close();
    }
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    const selected = await inspect(151, expected, "markdown");
    assert.ok(selected.uri.path.includes("SKILL.md"));
    await assert.rejects(api.document(999), /selection_changed/);
    await assert.rejects(
      api.detail("not-on-the-current-page"),
      /selection_changed/,
    );
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    // Exercise the native save UI in this owned profile, with no OS dialog or retained settings.
    const files = vscode.workspace.getConfiguration("files"),
      originalSimple = files.inspect("simpleDialog.enable").globalValue;
    await files.update(
      "simpleDialog.enable",
      true,
      vscode.ConfigurationTarget.Global,
    );
    const destination = join(
      vscode.workspace.workspaceFolders[0].uri.fsPath,
      "owned-document-151.md",
    );
    try {
      await vscode.commands.executeCommand("openAgenticCatalog.open");
      const web = await require("./webview.cjs").webview();
      try {
        await web.until(
          "document.querySelector('#all-documents') && !document.querySelector('#search-submit').disabled",
        );
        await web.evaluate(
          "document.querySelector('#all-documents').open=true;[...document.querySelectorAll('.document-row')][151].querySelector('button[id^=\"action-export-\"]').click()",
        );
        const visible = ".quick-input-widget:not([style*='display: none'])";
        const nativeInput = `document.querySelector(${JSON.stringify(`${visible} input`)})`;
        await ui.until(
          `${nativeInput} && document.querySelector(${JSON.stringify(visible)}).textContent.includes('Export')`,
        );
        await ui.click(nativeInput);
        await ui.evaluate(
          `(()=>{const input=${nativeInput};input.value=${JSON.stringify(destination)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
        );
        await ui.until(
          `document.activeElement===${nativeInput} && ${nativeInput}.value===${JSON.stringify(destination)} && !document.querySelector(${JSON.stringify(`${visible} .monaco-progress-container.active`)})`,
        );
        const accept = `[...document.querySelectorAll(${JSON.stringify(`${visible} .quick-input-action .monaco-button`)})].find(button=>button.textContent.trim()==='OK')`;
        await ui.until(
          `!!(${accept}) && !(${accept}).classList.contains('disabled') && (${accept}).getAttribute('aria-disabled')!=='true'`,
        );
        await ui.click(accept);
        try {
          await until(async () => {
            try {
              return (await readFile(destination, "utf8")) === expected;
            } catch {
              return false;
            }
          }, "exact exported document 151");
        } catch (error) {
          throw new Error(
            error.message +
              " native=" +
              (await ui.evaluate("document.body.innerText.slice(-4000)")),
            { cause: error },
          );
        }
      } finally {
        web.close();
      }
    } finally {
      await files.update(
        "simpleDialog.enable",
        originalSimple,
        vscode.ConfigurationTarget.Global,
      );
    }
    await progress("sixteen evidence views and close accounting");
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    const opened = [];
    for (let index = 0; index < 16; index++) {
      await progress("opening evidence " + index);
      opened.push(await inspect(151, expected, "markdown"));
      await vscode.commands.executeCommand("workbench.action.keepEditor");
    }
    assert.equal(new Set(opened.map((d) => d.uri.toString())).size, 16);
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    const blocked = await require("./webview.cjs").webview();
    try {
      await blocked.until(
        "document.querySelector('#all-documents') && !document.querySelector('#search-submit').disabled",
      );
      await blocked.evaluate(
        "document.querySelector('#all-documents').open=true;[...document.querySelectorAll('.document-row')][151].querySelector('button[id^=\"action-inspect-\"]').click()",
      );
      await ui.until(
        "document.body.innerText.includes('close_evidence_documents')",
      );
      assert.equal(
        vscode.window.tabGroups.all
          .flatMap((g) => g.tabs)
          .filter(
            (tab) =>
              tab.input instanceof vscode.TabInputText &&
              tab.input.uri.scheme === "oac-evidence",
          ).length,
        16,
      );
    } finally {
      blocked.close();
    }
    const closed = new Promise((resolve) => {
      const remaining = new Set(opened.map((d) => d.uri.toString()));
      const listener = vscode.window.tabGroups.onDidChangeTabs((event) => {
        for (const tab of event.closed)
          if (tab.input instanceof vscode.TabInputText)
            remaining.delete(tab.input.uri.toString());
        if (!remaining.size) {
          listener.dispose();
          resolve();
        }
      });
    });
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    await Promise.race([
      closed,
      new Promise((_, reject) =>
        setTimeout(
          () => reject(Error("Evidence close accounting deadline")),
          10000,
        ),
      ),
    ]);
    await until(
      () => opened.every((d) => d.isClosed || !d.getText().includes(expected)),
      "closed buffers release private evidence",
    );
    await inspect(151, expected, "markdown");
    await api.signOut();
    await until(
      () =>
        vscode.window.activeTextEditor.document
          .getText()
          .includes("Catalog session changed"),
      "private evidence redaction",
    );
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
    await progress("long Published identities and all version states");
    const peer = process.env.CATALOG_VSCODE_ISSUER;
    await fetch(peer + "/owned-test/inspection", {
      method: "POST",
      body: JSON.stringify({ scenario: "large", holdDocument: false }),
    });
    await api.select("public");
    await api.browse({});
    for (let index = 0; index < 4; index++) {
      await progress("Published version state " + index);
      await vscode.commands.executeCommand("openAgenticCatalog.open");
      const web = await require("./webview.cjs").webview();
      try {
        await web.until(
          "document.querySelectorAll('.cards article').length===4 && !document.querySelector('#search-submit').disabled",
        );
        await web.evaluate(
          `document.getElementById('result-${index}').click()`,
        );
        await web.until(
          `document.querySelector('#detail-heading')?.textContent === ${JSON.stringify("Owned exact version " + index)} && !document.querySelector('#search-submit').disabled`,
        );
        const current = api.browsing().detail;
        assert.equal(current.item.identifier.length, 512);
        assert.equal(current.item.versionPresent, index !== 0);
        if (index === 1) assert.equal(current.item.version, "");
        if (index === 2) assert.equal([...current.item.version].length, 128);
        if (index === 3) assert.equal(current.item.version, "\n".repeat(128));
        await web.evaluate(
          "[...document.querySelectorAll('.detail button')].find(b=>b.textContent==='Copy exact reference').click()",
        );
        const copied = await until(async () => {
          const value = await vscode.env.clipboard.readText();
          try {
            const r = JSON.parse(value);
            return r.versionPresent === current.item.versionPresent &&
              r.version === current.item.version
              ? r
              : undefined;
          } catch {
            return undefined;
          }
        }, "exact copied version");
        assert.equal(copied.identifier, current.item.identifier);
        const native = JSON.stringify(current.documents[1].value, null, 2);
        const doc = await inspect(1, native, "json");
        assert.ok(doc.uri.path.endsWith(".json"));
        // Enter the real native consumer selector, then decline before any target contact.
        const selecting = index === 2 ? api.talkAgent(1) : api.useMcp(1);
        await ui.until(
          "document.querySelector('.quick-input-widget:not([style*=\"display: none\"]) input')!==null",
        );
        await vscode.commands.executeCommand("workbench.action.closeQuickOpen");
        await selecting;
        await vscode.commands.executeCommand(
          "workbench.action.closeAllEditors",
        );
      } finally {
        web.close();
      }
    }
    const stats = await (await fetch(peer + "/owned-test/inspection")).json();
    assert.equal(stats.targetRequests, 0);
    await progress("copied local reference and native bookmarks");
    await api.select("qualification");
    await api.authenticate("reader");
    const local = await api.browse({ kind: "agent" });
    await api.detail(local.items[0].key);
    const reference = {
      origin: "local",
      identifier: local.items[0].identifier,
      versionPresent: true,
      version: local.items[0].version,
      releaseId: local.items[0].releaseId,
    };
    const response = await fetch(
      `${process.env.CATALOG_VSCODE_CATALOG}/api/v1/catalog/resources?limit=25`,
      { headers: { Authorization: "Bearer reader" } },
    );
    assert.equal(response.status, 200);
    const inventory = await response.json();
    const record = inventory.items.find(
      (item) =>
        item.resource.canonicalIdentifier ===
        "urn:air:catalog.example.com:vscode:agent",
    );
    assert.ok(record, "The supplied published Agent must be readable");
    assert.deepEqual(reference, {
      origin: "local",
      identifier: record.resource.canonicalIdentifier,
      versionPresent: true,
      version: "1.0.0",
      releaseId: record.release.id,
    });
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    const reader = await require("./webview.cjs").webview();
    try {
      await reader.until(
        `document.querySelector('.detail > code')?.textContent === ${JSON.stringify(reference.identifier)} && !document.querySelector('#search-submit').disabled`,
      );
      await vscode.env.clipboard.writeText("newcomer-copy-control");
      await reader.evaluate(
        "[...document.querySelectorAll('.detail button')].find(b=>b.textContent==='Copy exact reference').scrollIntoView({block:'center'})",
      );
      await reader.click(
        "[...document.querySelectorAll('.detail button')].find(b=>b.textContent==='Copy exact reference')",
      );
      const copied = await until(async () => {
        const value = await vscode.env.clipboard.readText();
        return value !== "newcomer-copy-control"
          ? JSON.parse(value)
          : undefined;
      }, "local copied reference");
      assert.deepEqual(copied, reference);
    } finally {
      reader.close();
    }
    await vscode.commands.executeCommand(
      "openAgenticCatalog.openReference",
      JSON.stringify(reference),
    );
    assert.equal(api.browsing().detail.item.releaseId, reference.releaseId);
    const raw = await api.document(0);
    await inspect(0, Buffer.from(raw.bytes).toString(), "json");
    assert.ok(
      vscode.window.activeTextEditor.document
        .getText()
        .includes("9007199254740993"),
    );
    assert.equal(
      api.browsing().detail?.item.releaseId,
      reference.releaseId,
      "Native evidence inspection must retain the exact local selection",
    );
    await api.saveBookmark();
    const bookmarkScope = api.browsing().scope;
    await progress("local reference saved; show native bookmark row");
    await vscode.commands.executeCommand("openAgenticCatalog.catalog.focus");
    await ui.until(
      `document.body.innerText.includes(${JSON.stringify("Release " + reference.releaseId)})`,
    );
    const button = (title) =>
      `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(title)})`;
    const canceledRemove = vscode.commands.executeCommand(
      "openAgenticCatalog.removeReference",
      0,
    );
    await ui.until(
      `!!(${button("Remove reference")}) && !!(${button("Cancel")})`,
    );
    await ui.click(button("Cancel"));
    await canceledRemove;
    await ui.until(`!(${button("Remove reference")})`);
    assert.equal(api.browsing().scope, bookmarkScope);
    assert.equal(api.browsing().error, undefined);
    assert.equal(api.browsing().detail?.item.releaseId, reference.releaseId);
    assert.deepEqual(api.browsing().bookmarks, [
      { releaseId: reference.releaseId },
    ]);
    const remove = vscode.commands.executeCommand(
      "openAgenticCatalog.removeReference",
      0,
    );
    await ui.until(`!!(${button("Remove reference")})`);
    await ui.evaluate(`(${button("Remove reference")}).click()`);
    await remove;
    await progress("native reference removal returned");
    assert.equal(
      api.browsing().scope,
      bookmarkScope,
      "Confirmed reference removal must preserve the account scope",
    );
    assert.equal(api.browsing().error, undefined);
    assert.equal(
      api.browsing().detail?.item.releaseId,
      reference.releaseId,
      "Confirmed reference removal must preserve the selected local release",
    );
    assert.equal(api.browsing().bookmarks.length, 0);
    await api.saveBookmark();
    assert.equal(api.browsing().detail?.item.releaseId, reference.releaseId);
    assert.deepEqual(api.browsing().bookmarks, [
      { releaseId: reference.releaseId },
    ]);
    await api.signOut();
    const forget = vscode.commands.executeCommand(
      "openAgenticCatalog.forgetReferences",
    );
    await ui.until(
      "document.querySelector('.quick-input-title')?.textContent.includes('inaccessible account')",
    );
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
    await ui.until(`!!(${button("Forget references")})`);
    await ui.evaluate(`(${button("Forget references")}).click()`);
    await forget;
    await api.authenticate("reader");
    const restored = await api.browse({ kind: "agent" });
    assert.equal(
      api.browsing().bookmarks.length,
      0,
      "Signed-out local forgetting removes the bound references",
    );
    await api.detail(restored.items[0].key);
    await api.saveBookmark();
    await progress("inspection complete");
  } catch (error) {
    const state = api.browsing();
    let codes = [];
    try {
      codes = await ui.evaluate(
        "[...new Set([...document.body.innerText.matchAll(/Catalog: ([a-z_]{1,128})/g)].map(match=>match[1]))].slice(-16)",
      );
    } catch {
      // A closed native frame must not hide the original assertion failure.
    }
    await progress(
      "inspection failure state " +
        JSON.stringify({
          scope: state.scope,
          busy: state.busy,
          error: state.error ?? null,
          origin: state.detail?.item.origin ?? null,
          documents: state.detail?.documents.length ?? null,
          bookmarks: state.bookmarks.length,
          nativeCodes: codes,
        }),
    );
    throw error;
  } finally {
    ui.close();
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  }
};
