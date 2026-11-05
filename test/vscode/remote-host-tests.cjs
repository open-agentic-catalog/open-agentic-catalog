const vscode = require("vscode");
const fs = require("node:fs/promises");
const path = require("node:path");
const assert = require("node:assert/strict");
exports.activate = (context) => {
  void (async () => {
    const root = process.env.OAC_REMOTE_FIXTURE;
    try {
      assert.ok(root);
      assert.equal(vscode.env.remoteName, "ssh-remote");
      assert.equal(process.platform, "linux");
      assert.ok(process.execPath.startsWith(root + path.sep));
      const folder = vscode.workspace.workspaceFolders[0];
      assert.equal(folder.uri.fsPath, path.join(root, "workspace"));
      assert.equal(
        folder.uri.scheme,
        "file",
        "The real SSH host receives its native filesystem URI after VS Code remoting translation",
      );
      assert.equal(vscode.workspace.isTrusted, true);
      const extension = vscode.extensions.getExtension(
        "open-agentic-catalog.open-agentic-catalog",
      );
      assert.equal(extension.extensionKind, vscode.ExtensionKind.Workspace);
      assert.ok(extension.extensionUri.fsPath.startsWith(root + path.sep));
      const api = await extension.activate();
      await api.select("remote-oidc");
      const previous = context.globalState.get("firstRemoteHost");
      if (previous) {
        assert.notEqual(
          previous.pid,
          process.pid,
          "A new real remote extension-host process restores native SecretStorage",
        );
        assert.equal((await api.test()).identity.subject, "oidc-reader");
        await api.signOut();
        await assert.rejects(api.test(), /sign_in_required/u);
        const result = {
          passed: true,
          remoteName: vscode.env.remoteName,
          platform: process.platform,
          executable: process.execPath,
          workspace: folder.uri.toString(),
          extension: extension.extensionUri.toString(),
          globalStorage: context.globalStorageUri.toString(),
          initialPid: previous.pid,
          restoredPid: process.pid,
          restoredCredentials: true,
          signOut: true,
        };
        await fs.writeFile(
          path.join(root, "result.json"),
          JSON.stringify(result),
          { mode: 0o600 },
        );
        return;
      }
      const redirect = await vscode.env.asExternalUri(
        vscode.Uri.parse(
          "vscode://open-agentic-catalog.open-agentic-catalog/oidc",
        ),
      );
      await api.beginLogin(redirect.toString(true));
      const wrong = new URL(redirect.toString(true));
      wrong.searchParams.set("code", "owned-invalid-code");
      wrong.searchParams.set("state", "wrong-session");
      wrong.searchParams.set("iss", process.env.OAC_REMOTE_ISSUER);
      await assert.rejects(api.completeLogin(wrong.href));
      await api.signOut();
      await fs.writeFile(
        path.join(root, "ready.json"),
        JSON.stringify({
          ready: true,
          callbackScheme: redirect.scheme,
          callbackAuthority: redirect.authority,
        }),
        { mode: 0o600 },
      );
      const authorization = await api.beginLogin(
        redirect.toString(true),
        "vscode://open-agentic-catalog.open-agentic-catalog/oidc",
      );
      const completion = api.waitForLogin();
      void completion.catch(() => {});
      await fs.writeFile(path.join(root, "browser-url"), authorization, {
        mode: 0o600,
      });
      await completion;
      assert.equal((await api.test()).identity.subject, "oidc-reader");
      const tool = require("node:child_process").execFileSync(
        "/usr/bin/git",
        ["--version"],
        { encoding: "utf8", timeout: 5000 },
      );
      assert.match(tool, /^git version /u);
      await fs.writeFile(
        path.join(root, "workspace", "remote-owned.txt"),
        "Written by the real SSH extension host.\n",
        { mode: 0o600 },
      );
      await context.globalState.update("firstRemoteHost", { pid: process.pid });
      void vscode.commands
        .executeCommand("workbench.action.reloadWindow")
        .catch(async (error) => {
          // The reload deliberately terminates this remote host and cancels its RPC.
          // Completion still requires a different host PID and a successful read.
          if (error.name === "Canceled") return;
          await fs.writeFile(
            path.join(root, "result.json"),
            JSON.stringify({
              passed: false,
              error: String(error.stack ?? error),
            }),
            { mode: 0o600 },
          );
        });
    } catch (error) {
      await fs.writeFile(
        path.join(root, "result.json"),
        JSON.stringify({ passed: false, error: String(error.stack ?? error) }),
        { mode: 0o600 },
      );
    }
  })();
};
