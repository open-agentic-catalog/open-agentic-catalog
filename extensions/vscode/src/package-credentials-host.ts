import * as vscode from "vscode";
import { ClientError, safeURL } from "./config.js";
import {
  PackageCredentials,
  packageCredential,
  packageOrigin,
  type PackageCredential,
} from "./package-credentials.js";
import type { Session, Secrets } from "./session.js";
import type { PackageSource } from "./packages.js";

export class CatalogPackageCredentials {
  private credentials: PackageCredentials;
  constructor(
    secrets: Secrets,
    private session: Session,
  ) {
    this.credentials = new PackageCredentials(secrets);
  }
  async select(source: PackageSource): Promise<PackageCredential | undefined> {
    const check = this.session.guard(this.session.connection()),
      account = this.session.consumerBinding();
    const credential = await this.credentials.get(account, source);
    check();
    if (!credential) return;
    const choice = await vscode.window.showQuickPick(
      [
        {
          label: "Use saved package credential",
          value: true,
          description: `${source.type} · ${packageOrigin(source)} · ${credential.scheme}`,
        },
        { label: "Anonymous", value: false },
      ],
      { title: "Package source authentication (separate from Catalog login)" },
    );
    check();
    if (!choice) throw new ClientError("package_cancelled");
    return choice.value ? credential : undefined;
  }
  async manage() {
    await this.session.test();
    const check = this.session.guard(this.session.connection()),
      account = this.session.consumerBinding();
    const family = await vscode.window.showQuickPick(
      ["git", "oci", "https"].map((type) => ({
        label: {
          git: "Git over HTTPS",
          oci: "OCI Skill registry",
          https: "HTTPS Plugin archive",
        }[type]!,
        type: type as PackageSource["type"],
      })),
      { title: "Package credential source" },
    );
    check();
    if (!family) return;
    const suppliedOrigin = await vscode.window.showInputBox({
      title: "Exact package HTTPS origin",
      prompt:
        "Origin only, including a nonstandard port if needed. Bound to this execution host, Catalog connection and account. No URL paths or credentials.",
    });
    check();
    if (suppliedOrigin === undefined) return;
    const origin = safeURL(suppliedOrigin, false);
    // Validate the origin before asking for any secret.
    try {
      packageCredential({
        origin,
        type: family.type,
        scheme: "basic",
        username: "validation",
        token: "validation",
      });
    } catch {
      throw new ClientError("invalid_package_credential_origin");
    }
    const action = await vscode.window.showQuickPick(
      ["Add or replace", "Forget"],
      { title: `${family.label}: ${origin}` },
    );
    check();
    if (!action) return;
    if (action === "Forget") {
      await this.credentials.forget(account, family.type, origin);
      check();
      void vscode.window.showInformationMessage(
        "Package credential forgotten on this host. This does not revoke it at its issuer; running acquisitions may already have read it.",
      );
      return;
    }
    const scheme =
      family.type === "https"
        ? await vscode.window.showQuickPick(["Basic", "Bearer"], {
            title: "Plugin archive authentication",
          })
        : "Basic";
    check();
    if (!scheme) return;
    const username =
      scheme === "Basic"
        ? await vscode.window.showInputBox({
            title: "Package username",
            ignoreFocusOut: true,
          })
        : undefined;
    check();
    if (scheme === "Basic" && username === undefined) return;
    const token = await vscode.window.showInputBox({
      title: "Package password or token",
      prompt: `Only for ${origin}; stored in VS Code SecretStorage. Never use your Catalog access token.`,
      password: true,
      ignoreFocusOut: true,
    });
    check();
    if (token === undefined) return;
    const credential = packageCredential({
      origin,
      type: family.type,
      scheme: scheme === "Basic" ? "basic" : "bearer",
      ...(username !== undefined ? { username } : {}),
      token,
    });
    await this.credentials.save(account, credential);
    check();
    void vscode.window.showInformationMessage(
      "Package credential saved. Each private acquisition requires explicit selection.",
    );
  }
  async forgetAll() {
    if (
      (await vscode.window.showWarningMessage(
        "Forget all package source credentials on this host? Close other Catalog windows first. Catalog, MCP and Agent login remain separate. This does not revoke source credentials or cancel an acquisition that already read them.",
        { modal: true },
        "Forget package credentials",
      )) !== "Forget package credentials"
    )
      return;
    for (;;) {
      const result = await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Forgetting package credentials",
          cancellable: true,
        },
        async (_, token) =>
          this.credentials.forgetAll(() => token.isCancellationRequested),
      );
      if (!result.remaining) {
        void vscode.window.showInformationMessage(
          `Forgot ${result.deleted} package credential entries in this batch.`,
        );
        return;
      }
      if (
        (await vscode.window.showWarningMessage(
          `${result.remaining} package credentials remain; ${result.failed} deletions failed. Continue with another batch of at most 50?`,
          "Continue",
          "Stop",
        )) !== "Continue"
      )
        return;
    }
  }
}
