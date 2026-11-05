const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async () => {
  const target = vscode.extensions.getExtension(
    "open-agentic-catalog.open-agentic-catalog",
  );
  const api = await target.activate();
  await vscode.commands.executeCommand("openAgenticCatalog.open");
  let ui = await require("./webview.cjs").webview();
  try {
    await ui.until("document.getElementById('recover-addConnection') !== null");
    await ui.evaluate(
      "document.getElementById('catalog').style.width='328px';document.body.style.width='360px';document.body.style.boxSizing='border-box'",
    );
    assert.equal(
      await ui.evaluate(
        "document.getElementById('recover-addConnection').getBoundingClientRect().top < 360",
      ),
      true,
    );
    assert.equal(
      await ui.evaluate(
        "document.body.innerText.includes('No resources on this page.')",
      ),
      false,
    );
    assert.equal(
      await ui.evaluate(
        "document.querySelector('button[type=submit]').disabled",
      ),
      true,
    );
    await ui.evaluate(
      "document.getElementById('catalog').style.width='';document.body.style.width=''",
    );
    await ui.click(
      "[...document.querySelectorAll('button')].find(b=>b.textContent==='Search and filter help')",
    );
    await require("./help-host-tests.cjs").section("browse-the-catalog");
    await vscode.commands.executeCommand("workbench.action.closeActiveEditor");
    await vscode.commands.executeCommand("openAgenticCatalog.tools.focus");
    const toolsNative = await require("./webview.cjs").webview(true);
    try {
      const help =
        "[...document.querySelectorAll('.monaco-button')].find(b=>b.textContent==='My tools help')";
      await toolsNative.until(`!!(${help})`);
      await toolsNative.click(help);
      await require("./help-host-tests.cjs").section("manage-my-tools");
      await vscode.commands.executeCommand(
        "workbench.action.closeActiveEditor",
      );
    } finally {
      toolsNative.close();
    }
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    await ui.until("document.getElementById('recover-addConnection') !== null");
    await api.select("qualification");
    await ui.until("document.getElementById('recover-signIn') !== null");
    await ui.until(
      "document.getElementById('catalog-account-state')?.textContent.endsWith(' · Control API · signed out')",
    );
    await ui.click("document.getElementById('recover-signIn')");
    const native = await require("./webview.cjs").webview(true);
    try {
      await native.until(
        "document.querySelector('.quick-input-widget input[type=password]') !== null",
      );
      await native.evaluate(
        "(()=>{const input=document.querySelector('.quick-input-widget input[type=password]');input.value='reader';input.dispatchEvent(new Event('input',{bubbles:true}));})()",
      );
      await native.key("Enter");
    } finally {
      native.close();
    }
    await ui.until(
      "document.getElementById('recover-restart')?.textContent === 'Load first page'",
    );
    await ui.click("document.getElementById('recover-restart')");
    await ui.until("document.querySelectorAll('.cards article').length === 4");
    await ui.until(
      "document.getElementById('catalog-account-state')?.textContent.endsWith(' · Control API · verified')",
    );
    await vscode.commands.executeCommand("notifications.clearAll");
    await ui.evaluate("document.getElementById('text').dataset.owned='kept'");
    const verification = vscode.commands.executeCommand(
      "openAgenticCatalog.testConnection",
    );
    const notification = await require("./webview.cjs").webview(true);
    try {
      await notification.until(
        "document.body.innerText.includes('Catalog connection verified for reader.')",
      );
    } finally {
      notification.close();
    }
    assert.equal(
      await ui.evaluate("document.getElementById('text').dataset.owned"),
      "kept",
      "Unchanged status redrew the Catalog and discarded field state",
    );
    await vscode.commands.executeCommand("notifications.clearAll");
    await verification;
    await api.signOut();
    await ui.until("document.getElementById('recover-signIn') !== null");
    await ui.until(
      "document.getElementById('catalog-account-state')?.textContent.endsWith(' · Control API · signed out')",
    );
    await api.select("published-real");
    await ui.until(
      "document.getElementById('recover-restart')?.textContent === 'Load first page'",
    );
    assert.equal(
      await ui.evaluate("document.getElementById('recover-signIn') === null"),
      true,
    );
    await ui.click("document.getElementById('recover-restart')");
    await ui.until("document.querySelectorAll('.cards article').length > 0");
    assert.equal(api.snapshot().mode, "published");
    assert.equal(api.snapshot().identity, undefined);
  } finally {
    ui.close();
    await api.signOut();
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  }
};
