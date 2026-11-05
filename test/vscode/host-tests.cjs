const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async () => {
  const origin = process.env.CATALOG_VSCODE_CATALOG;
  assert.ok(origin);
  const target = vscode.extensions.getExtension(
    "open-agentic-catalog.open-agentic-catalog",
  );
  assert.ok(target, "Packaged extension missing");
  assert.equal(process.versions.node, "24.21.0");
  assert.equal(vscode.version, "1.141.0");
  const api = await target.activate();
  const commands = await vscode.commands.getCommands(true);
  for (const command of target.packageJSON.contributes.commands)
    assert.ok(commands.includes(command.command), command.command);
  assert.equal(target.isActive, true);
  assert.equal(api.snapshot(), undefined);
  // Exercise the installed command, including VSCE's canonical lowercase filename.
  const guide = vscode.Uri.joinPath(target.extensionUri, "readme.md");
  assert.match(
    Buffer.from(await vscode.workspace.fs.readFile(guide)).toString("utf8"),
    /# VS Code Catalog client/u,
  );
  const preview = new Promise((resolve, reject) => {
    const changed = vscode.window.tabGroups.onDidChangeTabs(() => {
      if (
        vscode.window.tabGroups.all.some((group) =>
          group.tabs.some(
            (tab) =>
              tab.input instanceof vscode.TabInputWebview &&
              tab.label.includes("readme.md"),
          ),
        )
      ) {
        clearTimeout(deadline);
        changed.dispose();
        resolve();
      }
    });
    const deadline = setTimeout(() => {
      changed.dispose();
      reject(new Error("Packaged connection guide did not open"));
    }, 10000);
  });
  await vscode.commands.executeCommand("openAgenticCatalog.help", "start");
  await preview;
  await require("./help-host-tests.cjs").verify();
  await require("./help-host-tests.cjs").images();
  await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  const configuration = vscode.workspace.getConfiguration("openAgenticCatalog");
  assert.equal(configuration.inspect("connections").workspaceValue, undefined);
  const connection = configuration.get("connections")[0];
  await api.select(connection.id);
  await assert.rejects(api.test(), /sign_in_required/u);
  await api.authenticate("reader");
  // Reuse one native SecretStorage key. Do not wait, retry a failed operation,
  // or suppress storage notifications between deletion and replacement.
  for (let cycle = 0; cycle < 12; cycle++) {
    await api.signOut();
    await api.authenticate("reader");
    assert.equal((await api.test()).identity.subject, "reader");
    assert.equal((await api.browse({ limit: 2 })).items.length, 2);
    await api.select(connection.id);
    assert.equal((await api.test()).identity.subject, "reader");
  }
  let state = await api.test();
  assert.equal(state.identity.subject, "reader");
  assert.equal(state.mode, "control");
  assert.ok(state.resourceKinds.includes("plugin"));
  const firstTenant = state.identity.tenantId;
  assert.deepEqual(
    target.packageJSON.contributes.views.openAgenticCatalog.map((v) => v.name),
    ["Catalog", "My tools"],
  );
  // A restored SecretStorage token is verified by ordinary native Refresh.
  await api.select(connection.id);
  assert.equal(api.snapshot(), undefined);
  await vscode.commands.executeCommand("openAgenticCatalog.refresh");
  assert.equal(api.snapshot().identity.subject, "reader");
  assert.equal(api.browsing().page.items.length, 4);
  let page = await api.browse({ limit: 2 });
  assert.equal(page.items.length, 2);
  assert.equal(page.summary, "4 matches");
  const firstKeys = page.items.map((i) => i.key);
  const next = await api.next();
  assert.equal(next.items.length, 2);
  assert.ok(next.items.every((i) => !firstKeys.includes(i.key)));
  assert.equal(next.next, undefined);
  page = await api.browse({ kind: "agent" });
  assert.equal(page.items.length, 1);
  const detail = await api.detail(page.items[0].key);
  assert.equal(detail.item.kind, "agent");
  assert.ok(
    detail.documents[0].value,
    "Inline native metadata remains available without an invented artifact URL",
  );
  const exact = await api.document(0);
  assert.match(Buffer.from(exact.bytes).toString("utf8"), /Owned Agent/u);
  assert.ok(
    Buffer.from(exact.bytes).toString("utf8").includes("9007199254740993"),
  );
  await api.saveBookmark();
  await api.saveBookmark();
  assert.equal(api.browsing().bookmarks.length, 1);
  await api.reopen(0);
  for (const kind of ["mcp_server", "skill", "plugin"]) {
    page = await api.browse({ kind });
    assert.equal(page.items.length, 1, kind);
    const d = await api.detail(page.items[0].key);
    assert.equal(d.item.kind, kind);
    if (kind === "skill")
      assert.match(
        Buffer.from((await api.document(0)).bytes).toString("utf8"),
        /name: owned-skill/u,
      );
    if (kind === "plugin") {
      assert.ok(d.documents.some((d) => d.label === "plugin.json"));
      const index = d.documents.findIndex((d) => d.label === "plugin.json");
      assert.match(
        Buffer.from((await api.document(index)).bytes).toString("utf8"),
        /owned.tools/u,
      );
    }
  }
  page = await api.browse({
    text: "owned",
    tag: "owned",
    license: "MIT",
    category: "tools",
  });
  assert.equal(page.items.length, 4);
  const facets = await api.explore();
  assert.ok(facets.some((f) => f.field === "kind" && f.buckets.length === 4));
  // Render the installed webview and exercise its real message bridge and DOM.
  await vscode.commands.executeCommand("openAgenticCatalog.open");
  const web = await require("./webview.cjs").webview();
  try {
    await web.until("document.querySelectorAll('article').length===4");
    const workbench = await require("./webview.cjs").webview(true);
    try {
      for (const command of [
        "workbench.action.closePanel",
        "workbench.action.closeAuxiliaryBar",
        "notifications.clearAll",
      ])
        await vscode.commands.executeCommand(command);
      await vscode.commands.executeCommand(
        "workbench.view.extension.openAgenticCatalog",
      );
      await web.evaluate("document.querySelector('article').scrollIntoView()");
      await workbench.screenshot(
        require("node:path").join(
          process.env.CATALOG_VSCODE_SCREENSHOTS,
          "catalog.png",
        ),
      );
      await web.evaluate("document.scrollingElement.scrollTop = 0");
    } finally {
      workbench.close();
    }
    assert.equal(
      await web.evaluate(
        "document.querySelectorAll('article script, article img').length",
      ),
      0,
    );
    assert.equal(await web.evaluate("typeof globalThis.hostile"), "undefined");
    assert.equal(
      await web.evaluate(
        "document.querySelector('article .description').textContent.includes('<script>hostile()</script>')",
      ),
      true,
    );
    assert.equal(
      await web.evaluate(
        "document.querySelector('input[name=text]').labels.length",
      ),
      1,
    );
    await web.evaluate("document.getElementById('text').focus()");
    await web.key("Tab");
    assert.equal(await web.evaluate("document.activeElement.id"), "kind");
    await web.evaluate(
      "document.getElementById('kind').value='plugin';document.querySelector('form').requestSubmit()",
    );
    await web.until(
      "document.querySelectorAll('article').length===1 && document.querySelector('article').textContent.includes('Plugin')",
    );
    await web.evaluate("document.querySelector('article .title').click()");
    await web.until(
      "document.querySelector('.detail')?.textContent.includes('plugin.json')",
    );
    await web.evaluate(
      "[...document.querySelectorAll('article .tags button')].find(b=>b.textContent==='owned').click()",
    );
    await web.until(
      "document.getElementById('tag').value==='owned'&&!document.querySelector('input').disabled",
    );
    await vscode.workspace
      .getConfiguration("workbench")
      .update(
        "colorTheme",
        "Default High Contrast",
        vscode.ConfigurationTarget.Global,
      );
    assert.equal(
      await web.until(
        "document.body.classList.contains('vscode-high-contrast')",
      ),
      true,
    );
    assert.equal(
      await web.evaluate(
        "document.documentElement.scrollWidth <= document.documentElement.clientWidth+1",
      ),
      true,
    );
    // The actual webview opens a read-only native document, not an HTML preview.
    await web.evaluate("document.getElementById('return-results').click()");
    await web.until(
      "!document.querySelector('.detail') && !document.querySelector('#search-submit').disabled",
    );
    await web.evaluate("document.querySelector('article .title').click()");
    await web.until(
      "document.querySelector('.detail')?.textContent.includes('plugin.json')",
    );
    // Reading through the API here emits new view revisions before the click.
    // Use retained fixture bytes, so the expected value does not change UI state.
    const expectedPluginMetadata = api
      .browsing()
      .detail.documents.find((d) => d.label === "mcp.json").content;
    assert.equal(typeof expectedPluginMetadata, "string");
    await web.until(
      "[...document.querySelectorAll('.detail button')].some(b=>b.textContent==='Inspect Plugin metadata' && !b.disabled)",
    );
    const opened = new Promise((resolve, reject) => {
      const listener = vscode.workspace.onDidOpenTextDocument((document) => {
        if (document.uri.scheme !== "oac-evidence") return;
        clearTimeout(deadline);
        listener.dispose();
        resolve(document);
      });
      const deadline = setTimeout(() => {
        listener.dispose();
        reject(new Error("Read-only evidence did not open"));
      }, 10000);
    });
    await web.evaluate(
      "[...document.querySelectorAll('.detail button')].find(b=>b.textContent==='Inspect Plugin metadata').click()",
    );
    const firstEvidence = await opened;
    const languageReady = Date.now() + 10000;
    while (
      Date.now() < languageReady &&
      (vscode.window.activeTextEditor?.document.uri.toString() !==
        firstEvidence.uri.toString() ||
        vscode.window.activeTextEditor?.document.languageId !== "json")
    )
      await new Promise((resolve) => setTimeout(resolve, 50));
    const evidence = vscode.window.activeTextEditor.document;
    assert.equal(evidence.uri.toString(), firstEvidence.uri.toString());
    assert.equal(evidence.languageId, "json");
    assert.ok(evidence.uri.path.endsWith("mcp.json"));
    assert.equal(evidence.getText(), expectedPluginMetadata);
    const redacted = new Promise((resolve, reject) => {
      const listener = vscode.workspace.onDidChangeTextDocument(
        ({ document }) => {
          if (
            document.uri.toString() !== evidence.uri.toString() ||
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
        reject(new Error("Private evidence was not redacted"));
      }, 10000);
    });
    await api.signOut();
    await redacted;
    // Hidden webviews are recreated by VS Code; the next Open Catalog projects cleared state.
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    await web.until("document.querySelectorAll('article').length===0");
  } finally {
    web.close();
  }
  await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  await api.authenticate("scoped");
  state = await api.test();
  assert.ok(state.identity.availablePermissions.includes("resource.read"));
  assert.ok(!state.identity.permissions.includes("resource.read"));
  page = await api.browse();
  assert.equal(page.items.length, 4);
  const namespace = page.items[0].namespaceId;
  assert.equal((await api.browse({ namespace })).items.length, 4);
  assert.equal(
    (await api.browse({ namespace: "00000000-0000-7000-8000-000000000999" }))
      .items.length,
    0,
  );
  await api.authenticate("reader");
  await api.browse();
  assert.equal(api.browsing().bookmarks.length, 1);
  await api.removeBookmark(0);
  await vscode.commands.executeCommand("openAgenticCatalog.signOut");
  assert.equal(api.snapshot(), undefined);
  await assert.rejects(api.test(), /sign_in_required/u);
  await api.authenticate("other");
  state = await api.test();
  assert.equal(state.identity.subject, "other");
  assert.notEqual(state.identity.tenantId, firstTenant);
  await api.authenticate("invalid-credential");
  await assert.rejects(api.test(), /sign_in_required/u);
  assert.equal(api.snapshot(), undefined);
  await api.signOut();
  for (const [id, token] of [
    ["published-real", undefined],
    ["published-private", "reader"],
  ]) {
    await api.select(id);
    if (token) {
      await assert.rejects(api.browse(), /sign_in_required/u);
      await api.authenticate(token);
    }
    page = await api.browse({ limit: 2 });
    assert.equal(page.items.length, 2);
    assert.ok(page.next);
    const publicNext = await api.next();
    assert.equal(publicNext.items.length, 2);
    assert.ok((await api.explore()).some((f) => f.field === "type"));
    const kinds = new Set();
    for (const kind of ["agent", "mcp_server", "skill", "plugin"]) {
      page = await api.browse({ kind });
      assert.equal(page.items.length, 1, kind);
      kinds.add(page.items[0].kind);
      const d = await api.detail(page.items[0].key);
      assert.ok(d.documents.length);
      if (kind === "skill") {
        const index = d.documents.findIndex((document) => document.path);
        assert.ok(
          index >= 0,
          "Published immutable Skill document is readable without Control endpoints",
        );
        assert.match(
          Buffer.from((await api.document(index)).bytes).toString("utf8"),
          /name: owned-skill/u,
        );
      }
    }
    assert.equal(kinds.size, 4);
    page = await api.browse({ text: "owned", tag: "owned", license: "MIT" });
    assert.equal(page.items.length, 4);
    assert.ok((await api.explore()).length > 0);
    assert.match(
      Buffer.from(await api.manifest()).toString("utf8"),
      /urn:air:/u,
    );
    await assert.rejects(api.browse({ namespace }), /unsupported_filter/u);
  }
  await api.select("public");
  const anonymous = await api.test();
  assert.deepEqual(anonymous, { mode: "published", entryCount: 1 });
  await api.select("public-auth");
  await assert.rejects(api.test(), /sign_in_required/u);
  await api.authenticate("public-reader");
  assert.equal((await api.test()).mode, "published");
  await configuration.update(
    "connections",
    [],
    vscode.ConfigurationTarget.Global,
  );
  console.log(
    "Actual packaged extension: connection guide, embedded Node, SDK reads, four-kind browse/cards/filters/paging, scoped access, exact reads, bookmarks, hostile content, keyboard/high-contrast, publication, refusal, sign-out and real host SecretStorage passed",
  );
};
