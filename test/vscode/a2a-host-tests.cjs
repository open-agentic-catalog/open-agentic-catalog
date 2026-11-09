const vscode = require("vscode");
const assert = require("node:assert/strict");
const { webview } = require("./webview.cjs");
exports.qualifyA2A = async (api) => {
  const ui = await webview(true);
  const click = async (title) => {
    const expression = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim() === ${JSON.stringify(title)} && b.getBoundingClientRect().width>0)`;
    await ui.until(`${expression} !== undefined`);
    await ui.evaluate(`(${expression}).click()`);
    await ui.until("document.querySelector('.monaco-dialog-box') === null");
  };
  const pick = async (title) => {
    await ui.until(
      `document.querySelector(".quick-input-title")?.textContent.includes(${JSON.stringify(title)})`,
    );
    await ui.until(
      "document.querySelector('.quick-input-widget:not([style*=\"display: none\"]) input') !== null",
    );
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
  };
  const revealConversation = async () => {
    const tabs = vscode.window.tabGroups.all.flatMap((g) => g.tabs);
    assert.equal(
      tabs.filter((t) => t.label === "Agent conversation").length,
      1,
      "Expected one live conversation tab: " +
        JSON.stringify(
          tabs.map((t) => ({
            label: t.label,
            active: t.isActive,
            preview: t.isPreview,
          })),
        ),
    );
    const tab =
      "[...document.querySelectorAll('.tabs-container .tab')].find(t=>t.querySelector('.label-name')?.textContent==='Agent conversation')";
    await ui.until(`!!(${tab})`);
    await ui.click(tab);
  };
  let chat;
  try {
    await api.select("a2a-host");
    await api.authenticate("reader");
    const page = await api.browse({ kind: "agent" });
    const detail = await api.detail(page.items[0].key);
    const index = detail.documents.findIndex(
      (d) => d.mediaType === "application/a2a+json",
    );
    const selected = index >= 0 ? index : 0;
    const before = (
      await (
        await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
      ).json()
    ).length;
    assert.equal(before, 0, "Browsing must generate no agent traffic");
    const opening = api.talkAgent(selected);
    await pick("Choose one A2A interface");
    await pick("Choose A2A target authentication");
    await click("Open conversation");
    await opening;
    assert.equal(
      (
        await (
          await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
        ).json()
      ).length,
      before,
      "Opening the view must not contact the agent",
    );
    chat = await webview();
    await chat.until("document.body.innerText.includes('Status: ready')");
    assert.equal(
      await chat.evaluate("document.querySelectorAll('#catalog h1').length"),
      1,
    );
    assert.equal(
      await chat.evaluate(
        "document.body.innerText.includes('Select a connection, then refresh to browse.')",
      ),
      false,
    );
    await chat.evaluate(
      "(()=>{const p=document.getElementById('prompt');p.value='é'.repeat(8193);p.dispatchEvent(new Event('input',{bubbles:true}))})()",
    );
    assert.equal(
      await chat.evaluate(
        "document.getElementById('prompt-budget').textContent==='16,386 / 16,384 UTF-8 bytes' && document.getElementById('prompt').getAttribute('aria-invalid')==='true' && [...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt').disabled",
      ),
      true,
      "Native multibyte prompt validation uses bytes",
    );
    // Actual form typography, native themes and workbench zoom are separate controls.
    const zoom = vscode.workspace.getConfiguration("window").get("zoomLevel");
    const theme = vscode.workspace
      .getConfiguration("workbench")
      .get("colorTheme");
    const originalRatio = await ui.evaluate("window.devicePixelRatio");
    try {
      await vscode.workspace
        .getConfiguration("window")
        .update("zoomLevel", 4, vscode.ConfigurationTarget.Global);
      await ui.until(`window.devicePixelRatio >= ${originalRatio * 2}`);
      for (const [name, cls] of [
        ["Default Light Modern", "vscode-light"],
        ["Default Dark Modern", "vscode-dark"],
        ["Default High Contrast", "vscode-high-contrast"],
      ]) {
        await vscode.workspace
          .getConfiguration("workbench")
          .update("colorTheme", name, vscode.ConfigurationTarget.Global);
        await chat.until(
          `document.body.classList.contains(${JSON.stringify(cls)})`,
        );
        for (const size of [14, 28]) {
          await chat.evaluate(
            `document.documentElement.style.setProperty('--vscode-font-size', '${size}px');document.getElementById('catalog').style.maxWidth='328px';document.body.style.boxSizing='border-box'`,
          );
          assert.equal(
            await chat.evaluate(
              "getComputedStyle(document.getElementById('prompt')).fontSize",
            ),
            `${size}px`,
          );
          assert.equal(
            await chat.evaluate(
              "document.getElementById('catalog').scrollWidth <= document.getElementById('catalog').clientWidth+1 && document.documentElement.scrollWidth<=innerWidth+1",
            ),
            true,
            `${name} ${size}px`,
          );
          assert.equal(
            await chat.evaluate(
              "(()=>{const c=document.querySelector('input[type=checkbox]').getBoundingClientRect(), l=document.querySelector('.conversation-stream').getBoundingClientRect();return c.width<40 && c.width<l.width/2})()",
            ),
            true,
          );
        }
      }
      await chat.evaluate("document.getElementById('stream-reply').focus()");
      await chat.key(" ");
      await chat.until("document.getElementById('stream-reply').checked");
      assert.equal(
        await chat.evaluate(
          "document.querySelector('label[for=prompt]').textContent",
        ),
        "Prompt",
      );
      const accessibility = (await chat.accessibility()).filter(
        (node) => !node.ignored,
      );
      assert.ok(
        accessibility.some(
          (node) =>
            node.role?.value === "textbox" &&
            node.name?.value === "Prompt" &&
            /16,?384/u.test(node.description?.value),
        ),
      );
      assert.ok(
        accessibility.some(
          (node) =>
            node.role?.value === "checkbox" &&
            node.name?.value === "Stream reply",
        ),
      );
      assert.ok(
        accessibility.some(
          (node) =>
            node.role?.value === "region" &&
            node.name?.value === "Conversation transcript",
        ),
      );
      assert.equal(
        accessibility
          .find((node) => node.role?.value === "status")
          ?.properties.find((property) => property.name === "live")?.value
          .value,
        "polite",
      );
      assert.equal(
        accessibility.filter(
          (node) =>
            ["button", "textbox", "checkbox"].includes(node.role?.value) &&
            !node.name?.value.trim(),
        ).length,
        0,
        "Conversation controls need accessible names",
      );
      assert.equal(
        await chat.evaluate(
          "document.querySelectorAll('[role=status]').length===1 && document.querySelector('[role=status]').textContent.length<=512 && !document.querySelector('.conversation-transcript').hasAttribute('aria-live')",
        ),
        true,
        "Status stays bounded and does not announce the complete private transcript",
      );
    } finally {
      await vscode.workspace
        .getConfiguration("window")
        .update("zoomLevel", zoom, vscode.ConfigurationTarget.Global);
      await vscode.workspace
        .getConfiguration("workbench")
        .update("colorTheme", theme, vscode.ConfigurationTarget.Global);
      await chat.evaluate(
        "document.documentElement.style.removeProperty('--vscode-font-size');document.getElementById('catalog').style.maxWidth='';document.body.style.boxSizing=''",
      );
    }
    await chat.evaluate(
      "(()=>{const p=document.getElementById('prompt');p.value='draft before tab switch';p.dispatchEvent(new Event('input',{bubbles:true}));})()",
    );
    const conversationTab = vscode.window.tabGroups.activeTabGroup.activeTab;
    assert.equal(
      conversationTab.isPreview,
      false,
      "A live conversation must survive opening another preview",
    );
    const scratch = await vscode.workspace.openTextDocument({
      content: "Owned conversation visibility check",
      language: "plaintext",
    });
    await vscode.window.showTextDocument(scratch, { preview: false });
    assert.equal(conversationTab.isActive, false);
    chat.close();
    await revealConversation();
    chat = await webview();
    await chat.until(
      "document.getElementById('prompt')?.value === 'draft before tab switch' && document.getElementById('stream-reply').checked",
    );
    // Use the actual installed webview buttons, not an injected view or protocol client.
    await chat.evaluate(
      "(()=>{const p=document.getElementById('prompt');p.value='hello';p.dispatchEvent(new Event('input',{bubbles:true}));[...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt').click();})()",
    );
    await click("Cancel");
    assert.equal(
      (
        await (
          await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
        ).json()
      ).length,
      before,
      "Declined prompt sends nothing",
    );
    assert.equal(
      await chat.evaluate("document.getElementById('prompt').value"),
      "hello",
      "Declined consent keeps the draft",
    );
    await chat.evaluate(
      "window.ownedPrompt=document.getElementById('prompt');window.ownedTranscript=document.querySelector('.conversation-transcript');window.ownedPrompt.focus();window.ownedPrompt.setSelectionRange(1,3)",
    );
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt').click()",
    );
    await click("Send prompt");
    await chat.until(
      "document.body.innerText.includes('Status: completed') && document.body.innerText.includes('Hello World! Nice to meet you!')",
    );
    assert.equal(api.a2aState().status, "completed");
    assert.equal(
      await chat.evaluate(
        "window.ownedPrompt === document.getElementById('prompt') && window.ownedTranscript === document.querySelector('.conversation-transcript') && window.ownedPrompt.selectionStart===1 && window.ownedPrompt.selectionEnd===3 && window.ownedPrompt.value==='hello' && document.getElementById('stream-reply').checked",
      ),
      true,
      "Streaming must preserve prompt nodes, caret and retained draft",
    );
    assert.equal(
      await chat.evaluate(
        "document.querySelector('.conversation-transcript').getAttribute('aria-live')",
      ),
      null,
    );
    for (const command of [
      "workbench.action.closePanel",
      "workbench.action.closeAuxiliaryBar",
      "notifications.clearAll",
    ])
      await vscode.commands.executeCommand(command);
    await ui.screenshot(
      require("node:path").join(
        process.env.CATALOG_VSCODE_SCREENSHOTS,
        "a2a.png",
      ),
    );
    await chat.until(
      "[...document.querySelectorAll('button')].some(b=>b.textContent==='Read task' && !b.disabled)",
    );
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Read task').click()",
    );
    const deadline = performance.now() + 10000;
    for (;;) {
      const requests = await (
        await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
      ).json();
      if (
        requests.some((r) => r.request.method === "GetTask") &&
        !api.a2aState().busy
      )
        break;
      assert.ok(
        performance.now() < deadline,
        "Read task button did not finish GetTask",
      );
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    // A second terminal-task turn keeps context. Its long plain-text prompt makes
    // scrolling real; a streamed reply and task read must leave the reader there.
    const context = api.a2aState().context;
    await chat.evaluate(
      "(()=>{const p=document.getElementById('prompt');p.value='hello\\n'+'Earlier message line\\n'.repeat(120);p.dispatchEvent(new Event('input',{bubbles:true}));[...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt').click()})()",
    );
    await click("Send prompt");
    await chat.until(
      "document.querySelector('.conversation-transcript').scrollHeight>document.querySelector('.conversation-transcript').clientHeight && document.getElementById('prompt').readOnly",
    );
    await chat.evaluate(
      "(()=>{const t=document.querySelector('.conversation-transcript');t.scrollTop=40;window.ownedScroll=t.scrollTop;const p=document.getElementById('prompt');p.focus();p.setSelectionRange(2,4)})()",
    );
    await chat.until(
      "document.body.innerText.includes('Status: completed') && !document.getElementById('prompt').readOnly",
    );
    assert.equal(
      await chat.evaluate(
        "document.querySelector('.conversation-transcript').scrollTop === window.ownedScroll && window.ownedScroll>0 && document.activeElement===document.getElementById('prompt') && document.getElementById('prompt').selectionStart===2",
      ),
      true,
      "Streaming keeps actual focus, caret and transcript reading position",
    );
    assert.equal(api.a2aState().context, context);
    const readsBefore = (
      await (
        await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
      ).json()
    ).filter((r) => r.request.method === "GetTask").length;
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Read task').click()",
    );
    const readDeadline = performance.now() + 10000;
    for (;;) {
      const count = (
        await (
          await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
        ).json()
      ).filter((r) => r.request.method === "GetTask").length;
      if (count === readsBefore + 1 && !api.a2aState().busy) break;
      assert.ok(
        performance.now() < readDeadline,
        "Explicit second task read did not finish",
      );
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    await chat.until("!document.getElementById('prompt').readOnly");
    assert.equal(
      await chat.evaluate(
        "document.querySelector('.conversation-transcript').scrollTop === window.ownedScroll && document.activeElement===document.getElementById('prompt')",
      ),
      true,
    );
    // Restore a short retained draft for the hide/show/export checks below.
    await chat.evaluate(
      "(()=>{const p=document.getElementById('prompt');p.value='hello';p.dispatchEvent(new Event('input',{bubbles:true}))})()",
    );
    const seen = await (
      await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
    ).json();
    assert.ok(seen.some((r) => r.request.method === "SendStreamingMessage"));
    assert.ok(seen.some((r) => r.request.method === "GetTask"));
    assert.ok(
      seen.every((r) => !r.headers.authorization && !r.headers.cookie),
      "Catalog reader credentials must never reach an anonymous agent",
    );
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Disconnect').click()",
    );
    await chat.until("document.body.innerText.includes('(disconnected)')");
    assert.equal(api.a2aState().connected, false);
    assert.ok(
      !(
        await (
          await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
        ).json()
      ).some((r) => r.request.method === "CancelTask"),
      "Disconnect must not send cancellation",
    );
    const exported = new Promise((resolve, reject) => {
      const listener = vscode.workspace.onDidOpenTextDocument((document) => {
        if (document.uri.scheme !== "oac-evidence") return;
        clearTimeout(deadline);
        listener.dispose();
        resolve(document);
      });
      const deadline = setTimeout(() => {
        listener.dispose();
        reject(new Error("Export transcript button opened no document"));
      }, 10000);
    });
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Export transcript').click()",
    );
    const document = await exported;
    assert.equal(document.languageId, "json");
    assert.ok(document.uri.path.endsWith("Agent-transcript.json"));
    const transcript = JSON.parse(document.getText());
    assert.equal(transcript.connected, false);
    assert.ok(
      transcript.messages.some((m) => m.role === "user" && m.text === "hello"),
    );
    assert.ok(
      transcript.artifacts.some((a) =>
        a.parts.join("").includes("Hello World!"),
      ),
    );
    assert.doesNotMatch(document.getText(), /Authorization|Bearer|Cookie/u);
    // Return/replacement are view actions, never implicit remote cancellation.
    chat.close();
    await revealConversation();
    chat = await webview();
    await chat.until("document.getElementById('prompt')?.value==='hello'");
    const helpBefore = api.a2aState();
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Agent help').click()",
    );
    await require("./help-host-tests.cjs").section("agent-outcomes");
    assert.equal(
      api.a2aState(),
      helpBefore,
      "Help cannot change the conversation",
    );
    await revealConversation();
    chat.close();
    chat = await webview();
    await chat.until("document.getElementById('prompt')?.value==='hello'");
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Return to Catalog').click()",
    );
    const catalog = await webview();
    try {
      await catalog.until(
        "document.body.innerText.includes('Catalog') && !document.getElementById('prompt')",
      );
    } finally {
      catalog.close();
    }
    await revealConversation();
    chat.close();
    chat = await webview();
    await chat.until("document.getElementById('prompt')?.value==='hello'");
    await chat.evaluate(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='New conversation / reselect').click()",
    );
    await pick("Choose one A2A interface");
    await pick("Choose A2A target authentication");
    await click("Open conversation");
    chat.close();
    chat = await webview();
    await chat.until(
      "document.getElementById('prompt')?.value==='' && document.body.innerText.includes('Status: ready') && !document.getElementById('stream-reply').checked",
    );
    assert.equal(api.a2aState().context, undefined);
    assert.deepEqual(api.a2aState().messages, []);
    assert.ok(
      !(
        await (
          await fetch(process.env.CATALOG_VSCODE_A2A + "/__owned_stats")
        ).json()
      ).some((r) => r.request.method === "CancelTask"),
    );
    const redacted = new Promise((resolve, reject) => {
      const listener = vscode.workspace.onDidChangeTextDocument(
        ({ document: current }) => {
          if (
            current.uri.toString() !== document.uri.toString() ||
            !current.getText().includes("Catalog session changed")
          )
            return;
          clearTimeout(deadline);
          listener.dispose();
          resolve();
        },
      );
      const deadline = setTimeout(() => {
        listener.dispose();
        reject(
          new Error("Sign-out did not clear the private transcript document"),
        );
      }, 10000);
    });
    await api.signOut();
    await redacted;
    assert.equal(api.a2aState(), undefined);
    assert.doesNotMatch(document.getText(), /Hello World|"hello"/u);
  } finally {
    chat?.close();
    ui.close();
  }
};

// Real renderer messages cross the native host before reaching this owned peer.
exports.qualifyRecovery = async (api) => {
  const peer = process.env.CATALOG_VSCODE_ISSUER;
  const ui = await webview(true);
  const stats = async () => (await fetch(peer + "/owned-test/agent")).json();
  const consent = async (label) => {
    const button = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent.trim()===${JSON.stringify(label)})`;
    await ui.until(`!!(${button})`);
    await ui.click(button);
    await ui.until("!document.querySelector('.monaco-dialog-box')");
  };
  const pick = async (title) => {
    await ui.until(
      `document.querySelector('.quick-input-title')?.textContent.includes(${JSON.stringify(title)}) && !!document.querySelector('.quick-input-list .monaco-list-row.focused')`,
    );
    await ui.key("Enter");
  };
  let chat;
  const open = async () => {
    chat?.close();
    const opening = api.talkAgent(1);
    await consent("Revalidate with peers");
    await pick("Choose one A2A interface");
    await pick("Choose A2A target authentication");
    await consent("Open conversation");
    await opening;
    chat = await webview();
    await chat.until("document.body.innerText.includes('Status: ready')");
  };
  const button = (label) =>
    `[...document.querySelectorAll('button')].find(b=>b.textContent===${JSON.stringify(label)})`;
  const send = async (prompt, stream = false) => {
    await chat.evaluate(
      `(()=>{const p=document.getElementById('prompt');p.value=${JSON.stringify(prompt)};p.dispatchEvent(new Event('input',{bubbles:true}));const c=document.getElementById('stream-reply');c.checked=${stream};c.dispatchEvent(new Event('change',{bubbles:true}));})()`,
    );
    await chat.click(button("Send prompt"));
    await consent("Send prompt");
    await consent("Revalidate with peers");
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
    await open();
    const initial = (await stats()).methods.length;
    await send("owned-interrupted", true);
    await chat.until(
      "document.body.innerText.includes('Status: working') && document.body.innerText.includes('Task: ') && document.getElementById('prompt').readOnly",
    );
    const task = api.a2aState().task;
    assert.ok(task, "The held stream must acknowledge its exact task first");
    assert.equal((await stats()).held, 1);
    await fetch(peer + "/owned-test/agent", { method: "POST" });
    await chat.until(
      "document.body.innerText.includes('Outcome uncertain. Do not resend.') && !document.getElementById('prompt').readOnly && [...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt').disabled",
    );
    assert.equal(api.a2aState().uncertain, true);
    assert.equal(
      await chat.evaluate("document.getElementById('prompt').value"),
      "owned-interrupted",
    );
    assert.deepEqual((await stats()).methods.slice(initial), [
      { method: "SendStreamingMessage", prompt: "owned-interrupted" },
    ]);
    assert.equal((await stats()).closed, 1);
    await chat.click(button("Read task"));
    await consent("Revalidate with peers");
    await chat.until(
      "document.body.innerText.includes('Status: completed') && ![...document.querySelectorAll('button')].find(b=>b.textContent==='Send prompt').disabled",
    );
    assert.equal(api.a2aState().uncertain, false);
    assert.deepEqual((await stats()).methods.slice(initial), [
      { method: "SendStreamingMessage", prompt: "owned-interrupted" },
      { method: "GetTask", task },
    ]);
    // A declined cancellation sends nothing; an approved one cancels this task once.
    await open();
    await send("work");
    await chat.until(
      "document.body.innerText.includes('Status: working') && !document.getElementById('prompt').readOnly",
    );
    const working = api.a2aState().task;
    const before = (await stats()).methods.length;
    await chat.click(button("Cancel remote task"));
    await consent("Cancel");
    assert.equal((await stats()).methods.length, before);
    assert.equal(api.a2aState().status, "working");
    await chat.click(button("Cancel remote task"));
    await consent("Cancel remote task");
    await consent("Revalidate with peers");
    await chat.until("document.body.innerText.includes('Status: canceled')");
    assert.deepEqual((await stats()).methods.slice(before), [
      { method: "CancelTask", task: working },
    ]);
    assert.equal(
      await chat.evaluate(`(${button("Cancel remote task")}).disabled`),
      true,
    );
    // The same real path reports an agent refusal without disconnecting or replaying.
    await open();
    await send("refuse");
    await chat.until(
      "document.body.innerText.includes('Status: working') && !document.getElementById('prompt').readOnly",
    );
    const refused = api.a2aState().task;
    const refusedBefore = (await stats()).methods.length;
    await chat.click(button("Cancel remote task"));
    await consent("Cancel remote task");
    await consent("Revalidate with peers");
    await chat.until(
      "document.body.innerText.includes('a2a_remote_refusal') && !document.getElementById('prompt').readOnly",
    );
    assert.equal(api.a2aState().connected, true);
    assert.equal(api.a2aState().status, "working");
    assert.equal(api.a2aState().task, refused);
    assert.deepEqual((await stats()).methods.slice(refusedBefore), [
      { method: "CancelTask", task: refused },
    ]);
    assert.equal(
      await chat.evaluate(
        "document.body.innerText.includes('private refusal')",
      ),
      false,
    );
  } finally {
    api.a2aDisconnect();
    await api.signOut();
    chat?.close();
    ui.close();
    await fetch(peer + "/owned-test/inspection", {
      method: "POST",
      body: JSON.stringify({ scenario: "legacy", holdDocument: false }),
    });
  }
};
