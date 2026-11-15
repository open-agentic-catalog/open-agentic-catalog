const vscode = require("vscode");
const assert = require("node:assert/strict");
const { webview } = require("./webview.cjs");

// Actual editor calibrations, separate from the pure CDP socket fixtures.
// Each private panel has a fresh frame; expected errors are never carried into
// production workflow observations or accepted through a broad ignore list.
exports.run = async (extension) => {
  for (const kind of ["clean", "exception", "console", "asset", "refusal"]) {
    const monitor = await webview("observer");
    const panel = vscode.window.createWebviewPanel(
      "catalog-error-calibration",
      "Owned renderer calibration",
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        localResourceRoots: [
          vscode.Uri.joinPath(extension.extensionUri, "media"),
        ],
      },
    );
    let finished = false,
      view;
    try {
      const asset = panel.webview.asWebviewUri(
        vscode.Uri.joinPath(
          extension.extensionUri,
          "media",
          ["asset", "refusal"].includes(kind)
            ? "catalog-calibration-missing.png"
            : "catalog.svg",
        ),
      );
      const action =
        kind === "exception"
          ? "throw new Error('private-renderer-sentinel')"
          : kind === "console"
            ? "console.error('private-console-sentinel')"
            : "";
      panel.webview.html = `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${panel.webview.cspSource}; script-src 'nonce-owned'"></head><body><div id="catalog">Owned calibration</div><img src="${asset}"><script nonce="owned">${action}</script></body></html>`;
      const expected =
        kind === "exception"
          ? { exception: 1 }
          : kind === "console"
            ? { console: 1 }
            : ["asset", "refusal"].includes(kind)
              ? { asset: 1 }
              : {};
      // A native read proves startup completed. It does not replace the monitor
      // that subscribed before the new frame's scripts were allowed to run.
      view = await webview();
      await view.until("document.body.innerText.includes('Owned calibration')");
      if (Object.keys(expected).length) {
        const deadline = Date.now() + 5000;
        while (
          JSON.stringify(monitor.errorCounts()) !== JSON.stringify(expected) &&
          Date.now() < deadline
        )
          await new Promise((resolve) => setTimeout(resolve, 25));
        // The normal success gate must reject this otherwise healthy panel.
        finished = true;
        if (kind === "refusal") await monitor.finish(expected);
        else
          await assert.rejects(
            monitor.finish(),
            /Unexpected owned renderer errors/u,
          );
        assert.deepEqual(monitor.errorCounts(), expected);
        if (kind === "exception") {
          // Runtime.enable replays exceptions from a still-live document.
          // Prove why requesting panel disposal alone cannot isolate observers.
          const replay = await webview("observer");
          await assert.rejects(
            replay.finish(),
            /Unexpected owned renderer errors/u,
          );
          const counts = replay.errorCounts();
          assert.deepEqual(Object.keys(counts), ["exception"]);
          assert.ok(
            counts.exception >= 1,
            "The live frame replays its exception",
          );
        }
      } else {
        finished = true;
        await monitor.finish();
      }
    } finally {
      try {
        if (!finished) await monitor.finish();
      } finally {
        panel.dispose();
        try {
          // Disposal is asynchronous in the renderer. Do not let the next
          // observer attach to this frame and replay its intentional errors.
          await view?.waitForClosed();
        } finally {
          view?.close(view.errorCounts());
        }
      }
    }
  }
};
