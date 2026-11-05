const vscode = require("vscode");
const assert = require("node:assert/strict");
const { appendFile } = require("node:fs/promises");
const { join } = require("node:path");
exports.run = async (api) => {
  const peer = process.env.CATALOG_VSCODE_ISSUER;
  const state = async () =>
    (await fetch(peer + "/owned-test/inspection")).json();
  const configure = (holdDocument) =>
    fetch(peer + "/owned-test/inspection", {
      method: "POST",
      body: JSON.stringify({ scenario: "peer", holdDocument }),
    });
  const ui = await require("./webview.cjs").webview(true);
  const progress = (value) =>
    appendFile(
      join(process.env.CATALOG_VSCODE_DEBUG_DIR, "consumer-progress.log"),
      value + "\n",
    );
  const click = async (title) => {
    const target = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(title)})`;
    await ui.until(`!!(${target})`);
    const previous = await ui.evaluate(
      `(()=>{const b=${target};const text=b.closest('.monaco-dialog-box').textContent;b.click();return text;})()`,
    );
    await ui.until(
      `document.querySelector('.monaco-dialog-box')?.textContent !== ${JSON.stringify(previous)}`,
    );
  };
  const until = async (predicate, message) => {
    const deadline = performance.now() + 10000;
    while (performance.now() < deadline) {
      if (await predicate()) return;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error("Native consumer deadline: " + message);
  };
  const select = async () => {
    await api.select("public");
    await api.browse({ text: "owned", federation: "auto" });
    await api.next();
    await api.detail(api.browsing().page.items[0].key);
  };
  let nativeID;
  try {
    await configure(false);
    await select();
    await progress("declined initial peer replay");
    let before = await state();
    let selecting = api.useMcp(1);
    await click("Cancel");
    await assert.rejects(selecting, /peer_query_cancelled/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).targetRequests, 0);
    await progress("stale initial peer consent");
    selecting = api.useMcp(1);
    await ui.until(
      "document.querySelector('.monaco-dialog-box')?.textContent.includes('original query')",
    );
    await api.select("qualification");
    await click("Revalidate with peers");
    await assert.rejects(selecting, /connection_changed/);
    assert.equal((await state()).queries, before.queries);
    await select();
    await progress(
      "initial view changes refuse approved consent before replay",
    );
    before = await state();
    selecting = api.useMcp(1);
    await ui.until(
      "document.querySelector('.monaco-dialog-box')?.textContent.includes('original query')",
    );
    await api.back();
    await click("Revalidate with peers");
    await assert.rejects(selecting, /selection_changed/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).targetRequests, 0);
    await select();
    before = await state();
    await progress("approved selection remains inert");
    selecting = api.useMcp(1);
    await click("Revalidate with peers");
    await click("Read document");
    await ui.until(
      "document.querySelector('.quick-input-widget:not([style*=\"display: none\"]) .quick-input-title')?.textContent.includes('MCP')",
    );
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
    await click("Use server");
    await selecting;
    const selection = api
      .mcpSelections()
      .find((s) => s.name === "io.example/consumer-replay");
    assert.ok(selection);
    assert.equal((await state()).queries, before.queries + 1);
    assert.equal((await state()).targetRequests, 0);
    nativeID = `open-agentic-catalog.open-agentic-catalog/Catalog ${selection.name} [${selection.id.slice(0, 8)}]`;
    const listing = vscode.commands.executeCommand("workbench.mcp.listServer");
    await ui.until(
      "document.querySelector('.quick-input-widget')?.textContent.includes('consumer-replay')",
    );
    await vscode.commands.executeCommand("workbench.action.closeQuickOpen");
    await listing;
    await progress("native startup declined before peer replay");
    before = await state();
    let starting = vscode.commands.executeCommand(
      "workbench.mcp.startServer",
      nativeID,
    );
    await click("Cancel");
    await starting;
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).targetRequests, 0);
    await vscode.commands.executeCommand("workbench.mcp.stopServer", nativeID);
    await progress(
      "actual progress-token cancellation closes held native-document response",
    );
    await configure(true);
    before = await state();
    starting = vscode.commands.executeCommand(
      "workbench.mcp.startServer",
      nativeID,
    );
    await click("Revalidate with peers");
    await click("Read document");
    await until(
      async () => (await state()).documents === before.documents + 1,
      "held document entered",
    );
    const cancel =
      "[...document.querySelectorAll('.notification-list-item')].find(n=>n.textContent.includes('Preparing Catalog MCP server')) && [...[...document.querySelectorAll('.notification-list-item')].find(n=>n.textContent.includes('Preparing Catalog MCP server')).querySelectorAll('.monaco-button')].find(b=>b.textContent.trim()==='Cancel')";
    await ui.until(`!!(${cancel})`);
    await ui.evaluate(`(${cancel}).click()`);
    await starting;
    await until(
      async () =>
        (await state()).abortedDocuments === before.abortedDocuments + 1,
      "canceled response closed",
    );
    assert.equal((await state()).targetRequests, 0);
    assert.equal(
      vscode.lm.tools.some((t) => t.name.includes("owned_cancel_echo")),
      false,
    );
    await vscode.commands.executeCommand("workbench.mcp.stopServer", nativeID);
    await progress(
      "later native startup succeeds with fresh consent for each replay",
    );
    await configure(false);
    before = await state();
    starting = vscode.commands.executeCommand(
      "workbench.mcp.startServer",
      nativeID,
    );
    await click("Revalidate with peers");
    await click("Read document");
    await click("Use server");
    await click("Revalidate with peers");
    await click("Read document");
    await until(async () => {
      const text = await ui.evaluate(
        "document.querySelector('.monaco-dialog-box')?.textContent ?? ''",
      );
      if (/trust/iu.test(text)) {
        assert.equal(
          await ui.evaluate(
            "(()=>{const b=[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>/trust/i.test(b.textContent)&&!/don't|do not/i.test(b.textContent));if(!b)return false;b.click();return true;})()",
          ),
          true,
        );
        return true;
      }
      return vscode.lm.tools.some((t) => t.name.includes("owned_cancel_echo"));
    }, "native MCP trust or running tools");
    await starting;
    await until(
      () => vscode.lm.tools.some((t) => t.name.includes("owned_cancel_echo")),
      "native later startup tools",
    );
    assert.equal((await state()).queries, before.queries + 2);
    assert.ok((await state()).targetRequests > 0);
    await progress("consumer replay and cancellation complete");
    await vscode.commands.executeCommand("workbench.mcp.stopServer", nativeID);
    await api.removeMcp(selection.id);
  } finally {
    if (nativeID)
      await vscode.commands.executeCommand(
        "workbench.mcp.stopServer",
        nativeID,
      );
    await configure(false);
    ui.close();
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  }
};
exports.agent = async (api) => {
  const peer = process.env.CATALOG_VSCODE_ISSUER,
    ui = await require("./webview.cjs").webview(true);
  const state = async () =>
    (await fetch(peer + "/owned-test/inspection")).json();
  const progress = (value) =>
    appendFile(
      join(process.env.CATALOG_VSCODE_DEBUG_DIR, "consumer-progress.log"),
      value + "\n",
    );
  const click = async (title) => {
    const expression = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(title)})`;
    await ui.until(`!!(${expression})`);
    const previous = await ui.evaluate(
      `(()=>{const b=${expression};const text=b.closest('.monaco-dialog-box').textContent;b.click();return text;})()`,
    );
    await ui.until(
      `document.querySelector('.monaco-dialog-box')?.textContent !== ${JSON.stringify(previous)}`,
    );
  };
  const pick = async (title) => {
    await ui.until(
      `document.querySelector('.quick-input-title')?.textContent.includes(${JSON.stringify(title)})`,
    );
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
  };
  try {
    await fetch(peer + "/owned-test/inspection", {
      method: "POST",
      body: JSON.stringify({ scenario: "agent", holdDocument: false }),
    });
    await api.select("public");
    await api.browse({ text: "owned", federation: "auto" });
    await api.next();
    await api.detail(api.browsing().page.items[0].key);
    await progress("Agent initial and subsequent consumer peer consent");
    let before = await state(),
      opening = api.talkAgent(1);
    await click("Cancel");
    await assert.rejects(opening, /peer_query_cancelled/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).agentRequests, 0);
    opening = api.talkAgent(1);
    await ui.until(
      "document.querySelector('.monaco-dialog-box')?.textContent.includes('original query')",
    );
    await api.back();
    await click("Revalidate with peers");
    await assert.rejects(opening, /selection_changed/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).agentRequests, 0);
    await api.detail(api.browsing().page.items[0].key);
    opening = api.talkAgent(1);
    await click("Revalidate with peers");
    await pick("Choose one A2A interface");
    await pick("Choose A2A target authentication");
    await click("Open conversation");
    await opening;
    assert.equal(
      (await state()).agentRequests,
      0,
      "Opening a conversation remains inert",
    );
    before = await state();
    let sending = api.a2aSend("work");
    await click("Send prompt");
    await click("Cancel");
    await assert.rejects(sending, /peer_query_cancelled/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).agentRequests, before.agentRequests);
    sending = api.a2aSend("work");
    await click("Send prompt");
    await click("Revalidate with peers");
    await sending;
    assert.equal((await state()).agentRequests, before.agentRequests + 1);
    assert.ok(api.a2aState().task);
    before = await state();
    let reading = api.a2aRead();
    await click("Cancel");
    await assert.rejects(reading, /peer_query_cancelled/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).agentRequests, before.agentRequests);
    // Respect the existing reader budget after the declined explicit read.
    await new Promise((resolve) => setTimeout(resolve, 1020));
    reading = api.a2aRead();
    await click("Revalidate with peers");
    await reading;
    assert.equal((await state()).agentRequests, before.agentRequests + 1);
    before = await state();
    let canceling = api.a2aCancel();
    await click("Cancel remote task");
    await click("Cancel");
    await assert.rejects(canceling, /peer_query_cancelled/);
    assert.equal((await state()).queries, before.queries);
    assert.equal((await state()).agentRequests, before.agentRequests);
    canceling = api.a2aCancel();
    await click("Cancel remote task");
    await click("Revalidate with peers");
    await canceling;
    assert.equal((await state()).agentRequests, before.agentRequests + 1);
    assert.equal(api.a2aState().status, "canceled");
    await progress("Agent select/send/read/cancel replay checks complete");
  } finally {
    await fetch(peer + "/owned-test/inspection", {
      method: "POST",
      body: JSON.stringify({ scenario: "legacy", holdDocument: false }),
    });
    api.a2aDisconnect();
    await api.signOut();
    ui.close();
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  }
};
