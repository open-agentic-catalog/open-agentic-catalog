const vscode = require("vscode");
const assert = require("node:assert/strict");
exports.run = async (api) => {
  await api.select("oidc");
  const callbackURI = await vscode.env.asExternalUri(
    vscode.Uri.parse("vscode://open-agentic-catalog.open-agentic-catalog/oidc"),
  );
  const authorization = await api.beginLogin(callbackURI.toString(true));
  assert.equal(
    new URL(authorization).origin,
    process.env.CATALOG_VSCODE_ISSUER,
  );
  const response = await fetch(authorization, { redirect: "manual" });
  const callback = response.headers.get("location");
  assert.ok(callback);
  const original = new URL(callback);
  const normalized = new URL(vscode.Uri.parse(callback).toString(true));
  const fields = ["protocol", "host", "pathname"];
  for (const field of fields)
    assert.equal(
      normalized[field] === original[field],
      true,
      `Local URI preserves ${field}`,
    );
  for (const field of ["state", "code", "iss"])
    assert.equal(
      normalized.searchParams.get(field) === original.searchParams.get(field),
      true,
      `Local URI preserves ${field}`,
    );
  try {
    require("node:child_process").execFileSync(
      process.env.CATALOG_VSCODE_HOST,
      [
        ...JSON.parse(process.env.CATALOG_VSCODE_PROFILE_ARGS),
        "--no-sandbox",
        "--open-url",
        callback,
      ],
      {
        timeout: 15000,
        stdio: "ignore",
        env: {
          ...process.env,
          ELECTRON_RUN_AS_NODE: undefined,
          DONT_PROMPT_WSL_INSTALL: "1",
        },
      },
    );
  } catch {
    throw new Error("Native URI dispatcher failed");
  }
  const ui = await require("./webview.cjs").webview(true);
  try {
    const deadline = performance.now() + 10000;
    while (api.snapshot()?.identity?.subject !== "oidc-reader") {
      if (performance.now() > deadline) {
        await ui.screenshot(
          require("node:path").join(
            process.env.CATALOG_VSCODE_SCREENSHOTS,
            "oidc-failure.png",
          ),
        );
        throw new Error("Native OIDC callback deadline");
      }
      // An ordinary editor can ask for URI-handler consent. Test-mode editors
      // automatically refuse this modal and cannot qualify the callback route.
      await ui.evaluate(
        "(()=>{const button=[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>/^Open(?:$| )/.test(b.textContent));if(button)button.click();})()",
      );
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
  } finally {
    ui.close();
  }
  assert.equal(api.snapshot().identity.subject, "oidc-reader");
  // Restore the actual native SecretStorage credential through reselection.
  await api.select("oidc");
  assert.equal((await api.test()).identity.subject, "oidc-reader");
  await assert.rejects(api.completeLogin(callback), /no_pending_login/u);
  await api.signOut();
  await assert.rejects(api.test(), /sign_in_required/u);
};
