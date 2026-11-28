const assert = require("node:assert/strict");
const vscode = require("vscode");
const { readFile } = require("node:fs/promises");
const { join } = require("node:path");
const { channel } = require("node:diagnostics_channel");

exports.run = async (api, ui) => {
  const { visible, title, pick, consent } =
    require("./native-input.cjs").nativeInput(ui);
  const expected = JSON.parse(
    await readFile(
      join(process.env.CATALOG_VSCODE_DEBUG_DIR, "saved-mcp.json"),
      "utf8",
    ),
  );
  const saved = () =>
    vscode.commands.executeCommand("openAgenticCatalog.savedMcp");
  const listTitle = "Saved inert MCP references";
  const focused = `(document.querySelector(${JSON.stringify(visible + " .monaco-list-row.focused")})?.textContent || '')`;
  const release = JSON.stringify(expected.releaseId),
    digest = JSON.stringify(expected.digest.slice(0, 12));
  const listHasRelease = `document.querySelector(${JSON.stringify(visible)})?.textContent.includes(${release})`;
  async function choose(action = "Reselect with fresh access and consent") {
    await ui.until(title(listTitle));
    // Both rows name the same release. The digest in the description identifies the document.
    await ui.until(focused);
    if (!(await ui.evaluate(`${focused}.includes(${digest})`)))
      await vscode.commands.executeCommand(
        "workbench.action.quickOpenSelectNext",
      );
    await ui.until(
      `${focused}.includes(${release}) && ${focused}.includes(${digest})`,
    );
    await vscode.commands.executeCommand(
      "workbench.action.acceptSelectedQuickOpenItem",
    );
    await pick("Saved MCP reference", action);
  }
  async function approve() {
    await ui.until(
      "document.querySelector('.monaco-dialog-box')?.textContent.includes(" +
        JSON.stringify("Use " + expected.selection.name + " (") +
        ")",
    );
    await consent("Use server");
  }
  const open = saved();
  await ui.until(title(listTitle) + " && " + listHasRelease);
  await api.authenticate("scoped");
  await open;
  await ui.until("!(" + title(listTitle) + ")");
  const hidden = saved();
  await ui.until(title(listTitle));
  assert.equal(await ui.evaluate(listHasRelease), false);
  await ui.key("Escape");
  await hidden;

  await api.authenticate("reader");
  const restored = saved();
  await choose();
  await approve();
  await restored;
  assert.deepEqual(api.mcpSelections(), [expected.selection]);
  await api.removeMcp(expected.selection.id);

  // Save the same release as the scoped reader. Its saved row has a different binding.
  await api.authenticate("scoped");
  const entries = await api.browse({ kind: "mcp_server" });
  const { item, documents } = await api.detail(
    entries.items.find((value) => value.releaseId === expected.releaseId).key,
  );
  const index = documents.findIndex(
    (doc) => doc.mediaType === "application/mcp-server-card+json",
  );
  assert.ok(index >= 0);
  const initial = api.useMcp(index);
  await pick("Choose one declared MCP connection or package", "Remote 1");
  await approve();
  await initial;
  const [scoped] = api.mcpSelections();
  assert.notEqual(scoped.id, expected.selection.id);
  await api.removeMcp(scoped.id);
  const origin = process.env.CATALOG_VSCODE_CATALOG,
    path = "/api/v1/catalog/resources/" + item.releaseId;
  await require("./host-tests.cjs").withRevokedScopedReader(
    origin,
    api.snapshot().identity,
    item.namespaceId,
    "saved-mcp",
    async () => {
      const control = await fetch(origin + path, {
        headers: { Authorization: "Bearer admin" },
      });
      assert.equal(control.status, 200);
      assert.equal((await control.json()).release.id, item.releaseId);
      const replies = [];
      const observed = ({ request, response }) => {
        if (
          String(request.origin) === origin &&
          request.method === "GET" &&
          request.path === path
        )
          replies.push(response.statusCode);
      };
      const headers = channel("undici:request:headers");
      headers.subscribe(observed);
      try {
        const denied = saved();
        await choose();
        await denied;
        assert.deepEqual(replies, [403]);
        await ui.until(
          "[...document.querySelectorAll('.notification-list-item')].some(n=>n.textContent.includes('access_refused'))",
        );
        assert.deepEqual(api.mcpSelections(), []);
      } finally {
        headers.unsubscribe(observed);
      }
    },
  );
  const recovery = saved();
  await choose();
  await approve();
  await recovery;
  assert.deepEqual(api.mcpSelections(), [scoped]);
  const removing = saved();
  await choose("Remove saved reference");
  await removing;
  assert.deepEqual(api.mcpSelections(), [scoped]);
  const empty = saved();
  await ui.until(title(listTitle));
  assert.equal(await ui.evaluate(listHasRelease), false);
  await ui.key("Escape");
  await empty;
  await api.authenticate("reader");
  const retained = saved();
  await choose();
  await approve();
  await retained;
  assert.deepEqual(api.mcpSelections(), [expected.selection]);
  const forgetting = vscode.commands.executeCommand(
    "openAgenticCatalog.forgetMcpReferences",
  );
  await consent("Forget MCP references");
  await forgetting;
  assert.deepEqual(api.mcpSelections(), [expected.selection]);
  await api.removeMcp(expected.selection.id);
};
