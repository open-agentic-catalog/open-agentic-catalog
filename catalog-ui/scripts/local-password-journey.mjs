/* global document, innerWidth */
// Selected by the packaged runner. Exercise the actual documented launcher;
// provider credentials and private HTTP payloads never enter reports or traces.
import {
  createHash,
  randomBytes,
  randomUUID,
  X509Certificate,
} from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { join } from "node:path";
import { request } from "node:https";
import { chromium, expect } from "@playwright/test";
import { cliOIDCJourney } from "./cli-oidc-journey.mjs";

const ui = "https://browse-a.test:9443";
const apiOrigin = "https://catalog-a.test:8443";
const provider = "https://identity.test:8447";
const issuer = provider + "/realms/catalog";
const allowed = new Set([ui, apiOrigin, provider]);

/** Read only an owned HTTPS origin, with verified private trust and no redirects. */
async function apiStatus(ca, token) {
  return new Promise((resolve, reject) => {
    const req = request(
      apiOrigin + "/api/v1/me",
      {
        ca,
        timeout: 5000,
        headers: { Authorization: "Bearer " + token },
        lookup: (_host, options, done) =>
          options.all
            ? done(null, [{ address: "127.0.0.1", family: 4 }])
            : done(null, "127.0.0.1", 4),
      },
      (response) => {
        response.resume();
        resolve(response.statusCode);
      },
    );
    req.on("error", reject);
    req.on("timeout", () => req.destroy(new Error("Owned API read timed out")));
    req.end();
  });
}

/** Capture only prepared input hashes in memory, never credential contents. */
async function inputHashes(directory) {
  const files = await readdir(directory, {
    recursive: true,
    withFileTypes: true,
  });
  const result = {};
  for (const file of files) {
    if (!file.isFile()) continue;
    const path = join(file.parentPath, file.name);
    const name = path.slice(directory.length + 1);
    if (name.startsWith("cli-auth/")) continue;
    result[name] = createHash("sha256")
      .update(await readFile(path))
      .digest("hex");
  }
  return result;
}

export async function localPasswordJourney({
  command,
  root,
  privateDirectory,
  image,
  uiImage,
  onBrowser,
  onCleanup = () => {},
  isInterrupted = () => false,
}) {
  const outputRoot = join(root, "output/playwright/local-passwords");
  await mkdir(outputRoot, { recursive: true });
  const output = await mkdtemp(join(outputRoot, "run-"));
  const directory = join(privateDirectory, "passwords");
  const launcher = ["python3", "examples/tutorials/local-oidc/local_auth.py"];
  const makeAuth = [
    "make",
    "CATALOG_ENV_FILE=/dev/null",
    "LOCAL_AUTH_DIRECTORY=" + directory,
  ];
  const startup = [
    ...makeAuth,
    "local-auth-up",
    "IMAGE=" + image,
    "UI_IMAGE=" + uiImage,
  ];
  let phase = "build",
    browser,
    compose,
    project,
    frontendImage;
  let passed = false,
    cleaned = false,
    engineChecked = false;
  const checks = [];
  const stage = (value) => {
    phase = value;
    console.log("Local passwords: " + value);
  };
  const checked = (value) => checks.push(value);
  async function newPage() {
    const context = await browser.newContext({
      viewport: { width: 1280, height: 900 },
    });
    await context.route("**/*", (route) =>
      allowed.has(new URL(route.request().url()).origin)
        ? route.continue()
        : route.abort(),
    );
    return context.newPage();
  }
  async function login(page, username, password, accepted = true) {
    await page.goto(ui + "/");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await page.locator("#username").fill(username);
    await page.locator("#password").fill(password);
    await page.locator("#kc-login").click();
    if (accepted)
      await expect(
        page.getByRole("navigation", { name: "Workspace" }),
      ).toBeVisible();
  }
  async function product(page, path, method = "GET", body, etag) {
    return page.evaluate(
      async ({ path, method, body, etag, key }) => {
        const response = await fetch("/_catalog/api/v1/" + path, {
          method,
          headers: {
            "X-Catalog-Request": "1",
            "Content-Type": "application/json",
            ...(method !== "GET" ? { "Idempotency-Key": key } : {}),
            ...(etag ? { "If-Match": etag } : {}),
          },
          ...(body ? { body: JSON.stringify(body) } : {}),
        });
        const text = await response.text();
        const value = text ? JSON.parse(text) : undefined;
        return {
          status: response.status,
          etag: response.headers.get("ETag"),
          value: value?.data ?? value,
        };
      },
      { path, method, body, etag, key: randomUUID() },
    );
  }
  async function grant(admin, subject) {
    await admin.goto(ui + "/members");
    await admin
      .locator("summary")
      .filter({ hasText: "Add Catalog member" })
      .click();
    const form = admin.locator("form").filter({
      has: admin.getByRole("button", {
        name: "Add Catalog member",
        exact: true,
      }),
    });
    await form.getByLabel("Subject", { exact: true }).fill(subject);
    await form
      .getByRole("combobox", { name: /^Identity type/ })
      .selectOption("human");
    await form
      .getByRole("combobox", { name: /^Catalog role/ })
      .selectOption("reader");
    await form
      .getByRole("combobox", { name: /^Assignment scope/ })
      .selectOption("tenant");
    await form
      .getByRole("textbox", { name: /^Reason/ })
      .fill("Local password workflow Reader grant");
    await form
      .getByRole("button", { name: "Add Catalog member", exact: true })
      .click();
    await admin
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(
      admin.getByText("Role granted.", { exact: true }),
    ).toBeVisible();
  }
  async function launchBrowser() {
    const pins = [];
    for (const name of ["identity", "catalog-a", "browse-a"]) {
      const cert = new X509Certificate(
        await readFile(join(directory, "trust", name + ".pem")),
      );
      pins.push(
        createHash("sha256")
          .update(cert.publicKey.export({ type: "spki", format: "der" }))
          .digest("base64"),
      );
    }
    browser = await chromium.launch({
      // The packaged supervisor owns cancellation and resource cleanup.
      handleSIGINT: false,
      handleSIGTERM: false,
      args: [
        "--ignore-certificate-errors-spki-list=" + pins.join(","),
        "--host-resolver-rules=MAP identity.test 127.0.0.1, MAP browse-a.test 127.0.0.1, MAP catalog-a.test 127.0.0.1",
        "--no-proxy-server",
      ],
    });
    onBrowser(browser);
  }
  try {
    await command([
      "python3",
      "-c",
      "import os,sys;sys.path.insert(0,'examples/tutorials/local-oidc');import local_auth;local_auth.local_engine(os.environ.copy())",
    ]);
    engineChecked = true;
    await command(
      [
        "make",
        "CATALOG_ENV_FILE=/dev/null",
        "container-build",
        "IMAGE=" + image,
      ],
      300,
    );
    await command(
      [
        "make",
        "CATALOG_ENV_FILE=/dev/null",
        "ui-container-build",
        "UI_IMAGE=" + uiImage,
      ],
      300,
    );
    stage("launcher-startup");
    await command(startup, 300);
    const values = await readFile(join(directory, "compose.env"), "utf8");
    project = values.match(
      /^COMPOSE_PROJECT_NAME="(catalog-docs-[a-f0-9]{8})"$/m,
    )?.[1];
    if (!project) throw new Error("Generated project is invalid");
    compose = [
      "docker",
      "compose",
      "--project-name",
      project,
      "--env-file",
      join(directory, "compose.env"),
      "--file",
      join(directory, "compose.single.json"),
    ];
    const configuration = JSON.parse(
      await readFile(join(directory, "compose.single.json"), "utf8"),
    );
    frontendImage = configuration.services.frontend.image;
    const hashes = await inputHashes(directory);
    const accounts = JSON.parse(
      await readFile(join(directory, "accounts.json"), "utf8"),
    );
    let adminPassword = await readFile(
      join(directory, "catalog-admin-initial-password"),
      "utf8",
    );
    const readerPassword = await readFile(
      join(directory, "catalog-reader-initial-password"),
      "utf8",
    );
    const ca = await readFile(join(directory, "trust/ca.pem"));
    await launchBrowser();
    checked("launcher-readiness");
    stage("regular-sign-in");
    const landing = await newPage();
    await landing.goto(ui + "/");
    await expect(
      landing.getByText("Use your account to access the Catalog."),
    ).toBeVisible();
    await expect(
      landing.getByText(
        /Local development identities|These are local shortcuts/,
      ),
    ).toHaveCount(0);
    await expect(
      landing.getByRole("searchbox", { name: "Find a sign-in identity" }),
    ).toHaveCount(0);
    await landing.screenshot({ path: join(output, "regular-sign-in.png") });
    await landing.setViewportSize({ width: 320, height: 900 });
    await landing.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    expect(
      await landing.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await landing.getByRole("button", { name: "Sign in", exact: true }).focus();
    await landing.keyboard.press("Enter");
    await expect(landing.locator("#username")).toBeVisible();
    await landing.setViewportSize({ width: 1280, height: 900 });
    await landing.screenshot({
      path: join(output, "local-keycloak-sign-in.png"),
    });
    checked("sign-in-copy-keyboard-layout");
    stage("bundled-password-guide");
    const help = await newPage();
    await help.goto(ui + "/getting-started");
    const guideOpened = help.context().waitForEvent("page");
    await help
      .getByRole("link", { name: /Set up local users and passwords/ })
      .click();
    const guide = await guideOpened;
    await expect(
      guide.getByRole("heading", {
        name: "Sign in with Keycloak",
        exact: true,
      }),
    ).toBeVisible();
    for (const name of ["regular-sign-in", "local-keycloak-sign-in"]) {
      const image = guide.locator('img[src$="/' + name + '.png"]');
      await expect(image).toHaveCount(1);
      await image.scrollIntoViewIfNeeded();
      await expect
        .poll(() =>
          image.evaluate((node) => node.complete && node.naturalWidth > 0),
        )
        .toBe(true);
    }
    await guide.close();
    checked("bundled-guide-and-screenshots");
    stage("no-development-fallback");
    const anonymous = await newPage();
    await anonymous.goto(ui + "/");
    const options = await anonymous.evaluate(async () =>
      (
        await fetch("/auth/options", { headers: { "X-Catalog-Request": "1" } })
      ).json(),
    );
    expect(options).toEqual({ mode: "oidc", users: [] });
    const aliasStatus = await anonymous.evaluate(
      async () =>
        (
          await fetch("/auth/login", {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Catalog-Request": "1",
            },
            body: JSON.stringify({ alias: "reader" }),
          })
        ).status,
    );
    expect(aliasStatus).toBe(400);
    expect(await apiStatus(ca, "local-admin")).toBe(401);
    checked("no-development-fallback");
    stage("wrong-password-and-unprovisioned-user");
    await login(
      anonymous,
      "catalog-admin",
      randomBytes(24).toString("hex"),
      false,
    );
    await expect(
      anonymous.getByText("Invalid username or password.", { exact: true }),
    ).toBeVisible();
    stage("unprovisioned-user");
    const refused = await newPage();
    await login(refused, "catalog-reader", readerPassword, false);
    await expect(refused.getByRole("alert")).toHaveText(
      "The Catalog did not accept this account. Ask an administrator to check your access.",
    );
    expect(
      (await refused.context().cookies(ui)).some(
        (cookie) => cookie.name === "catalog-a-session",
      ),
    ).toBe(false);
    checked("wrong-password-and-unprovisioned-refusal");
    const admin = await newPage();
    await login(admin, "catalog-admin", adminPassword);
    expect((await product(admin, "me")).value.subject).toBe(
      accounts["catalog-admin"],
    );
    stage("reader-grant");
    const namespace = await product(admin, "namespaces", "POST", {
      slug: "password-workflow",
      displayName: "Password workflow",
    });
    expect(namespace.status).toBe(201);
    const resource = await product(admin, "resources", "POST", {
      namespaceId: namespace.value.id,
      kind: "agent",
      canonicalIdentifier: "custom:password-workflow-control",
    });
    expect(resource.status).toBe(201);
    const listing = await product(admin, "catalog-listings", "POST", {
      namespaceId: namespace.value.id,
      resourceId: resource.value.id,
    });
    expect(listing.status).toBe(201);
    stage("reader-members-ui");
    await grant(admin, accounts["catalog-reader"]);
    const reader = await newPage();
    await login(reader, "catalog-reader", readerPassword);
    const access = await product(reader, "me");
    expect(access.status).toBe(200);
    expect(access.value.subject).toBe(accounts["catalog-reader"]);
    expect(access.value.permissions).toContain("resource.read");
    expect(access.value.permissions).toContain("ratings.own");
    expect(access.value.permissions).not.toContain("resource.create");
    expect(access.value.permissions).not.toContain("release.publish");
    expect((await product(reader, "resources?limit=1")).status).toBe(200);
    expect(
      (
        await product(reader, "resources", "POST", {
          kind: "agent",
          namespaceId: namespace.value.id,
          canonicalIdentifier: "custom:reader-denied",
        })
      ).status,
    ).toBe(403);
    stage("reader-publication-refusal");
    expect(
      (
        await product(
          reader,
          "catalog-listings/" + listing.value.id + ":publish",
          "POST",
          {
            submissionId: "00000000-0000-7000-8000-000000000098",
          },
          '"rev-1"',
        )
      ).status,
    ).toBe(403);
    await reader.getByTitle("Account", { exact: true }).click();
    await reader.getByRole("link", { name: "My access", exact: true }).click();
    await expect(
      reader.getByText(accounts["catalog-reader"], { exact: true }).first(),
    ).toBeVisible();
    checked("exact-reader-permissions-and-my-access");
    stage("self-service-password-change");
    const account = await newPage();
    await account.goto(issuer + "/account/");
    await account.locator("#username").fill("catalog-admin");
    await account.locator("#password").fill(adminPassword);
    await account.locator("#kc-login").click();
    await account
      .getByRole("button", { name: "Account security", exact: true })
      .click();
    await account
      .getByRole("link", { name: "Signing in", exact: true })
      .click();
    await account.getByRole("button", { name: "Update", exact: true }).click();
    await account.locator("#password-new, #password").first().waitFor();
    if (await account.locator("#password").isVisible()) {
      await account.locator("#password").fill(adminPassword);
      await account.locator("#kc-login").click();
    }
    const newAdminPassword = randomBytes(32).toString("base64url");
    await account.locator("#password-new").fill(newAdminPassword);
    await account.locator("#password-confirm").fill(newAdminPassword);
    await account.getByRole("button", { name: "Submit", exact: true }).click();
    await expect(
      account.getByRole("heading", { name: "Signing in", exact: true }),
    ).toBeVisible();
    const staleAdmin = await newPage();
    await login(staleAdmin, "catalog-admin", adminPassword, false);
    await expect(
      staleAdmin.getByText("Invalid username or password.", { exact: true }),
    ).toBeVisible();
    adminPassword = newAdminPassword;
    checked("self-service-password-change-and-stale-initial-password");
    stage("provider-account-administration");
    const providerAdmin = await newPage();
    let providerToken;
    providerAdmin.on("request", (request) => {
      if (request.url().startsWith(provider + "/admin/realms/")) {
        const value = request.headers().authorization;
        if (value?.startsWith("Bearer ")) providerToken = value;
      }
    });
    await providerAdmin.goto(
      provider + "/admin/master/console/#/catalog/users",
    );
    await providerAdmin.locator("#username").fill("bootstrap-admin");
    await providerAdmin
      .locator("#password")
      .fill(await readFile(join(directory, "bootstrap-password"), "utf8"));
    await providerAdmin.locator("#kc-login").click();
    await expect.poll(() => Boolean(providerToken)).toBe(true);
    async function providerCommand(path, method, body) {
      return providerAdmin.evaluate(
        async ({ path, method, body, token }) => {
          const response = await fetch("/admin/realms/catalog/" + path, {
            method,
            headers: {
              Authorization: token,
              "Content-Type": "application/json",
            },
            ...(body ? { body: JSON.stringify(body) } : {}),
          });
          return {
            status: response.status,
            location: response.headers.get("Location"),
          };
        },
        { path, method, body, token: providerToken },
      );
    }
    const extraPassword = randomBytes(32).toString("base64url");
    const extra = await providerCommand("users", "POST", {
      username: "catalog-extra",
      enabled: true,
      email: "extra@example.invalid",
      emailVerified: true,
      firstName: "Catalog",
      lastName: "Extra",
      credentials: [
        { type: "password", value: extraPassword, temporary: false },
      ],
    });
    expect(extra.status).toBe(201);
    const extraSubject = new URL(extra.location).pathname.split("/").at(-1);
    expect(extraSubject).toMatch(/^[a-f0-9-]{36}$/);
    await grant(admin, extraSubject);
    const additional = await newPage();
    await login(additional, "catalog-extra", extraPassword);
    expect((await product(additional, "me")).value.subject).toBe(extraSubject);
    checked("additional-user-without-restart");
    stage("password-reset-and-disable");
    const changedPassword = randomBytes(32).toString("base64url");
    expect(
      (
        await providerCommand(
          "users/" + accounts["catalog-reader"] + "/reset-password",
          "PUT",
          { type: "password", value: changedPassword, temporary: false },
        )
      ).status,
    ).toBe(204);
    const oldPassword = await newPage();
    await login(oldPassword, "catalog-reader", readerPassword, false);
    await expect(
      oldPassword.getByText("Invalid username or password.", { exact: true }),
    ).toBeVisible();
    const changed = await newPage();
    await login(changed, "catalog-reader", changedPassword);
    expect(
      (
        await providerCommand("users/" + accounts["catalog-reader"], "PUT", {
          enabled: false,
        })
      ).status,
    ).toBe(204);
    const disabled = await newPage();
    await login(disabled, "catalog-reader", changedPassword, false);
    await expect(disabled.getByText(/Account (?:is )?disabled/)).toBeVisible();
    expect((await product(reader, "me")).status).toBe(200);
    expect(
      (
        await providerCommand("users/" + accounts["catalog-reader"], "PUT", {
          enabled: true,
        })
      ).status,
    ).toBe(204);
    checked("provider-reset-disable-and-issued-token-boundary");
    stage("catalog-suspension");
    const members = await product(admin, "memberships?limit=100");
    const member = members.value.items.find(
      (item) => item.subject === accounts["catalog-reader"],
    );
    expect(member).toBeTruthy();
    const detail = await product(admin, "memberships/" + member.id);
    expect(
      (
        await product(
          admin,
          "memberships/" + member.id + ":suspend",
          "POST",
          { reason: "Local password suspension check" },
          detail.etag,
        )
      ).status,
    ).toBe(200);
    expect((await product(reader, "me")).status).toBe(401);
    const assignments = await product(
      admin,
      "role-assignments?membershipId=" + member.id + "&limit=100",
    );
    expect(assignments.value.items).toHaveLength(1);
    const assignment = assignments.value.items[0];
    expect(
      (
        await product(
          admin,
          "role-assignments/" + assignment.id + ":revoke",
          "POST",
          {
            reason: "Verify restart preserves revoked grants",
          },
          '"rev-' + assignment.revision + '"',
        )
      ).status,
    ).toBe(200);
    checked("suspension-denies-existing-session");
    stage("local-logout-and-provider-sso");
    await additional.getByTitle("Account", { exact: true }).click();
    await additional
      .getByRole("button", { name: "Sign out", exact: true })
      .click();
    await expect(
      additional.getByRole("button", { name: "Sign in", exact: true }),
    ).toBeVisible();
    expect(
      (await additional.context().cookies(ui)).some(
        (cookie) => cookie.name === "catalog-a-session",
      ),
    ).toBe(false);
    await additional
      .getByRole("button", { name: "Sign in", exact: true })
      .click();
    await expect(
      additional.getByRole("navigation", { name: "Workspace" }),
    ).toBeVisible();
    expect((await product(additional, "me")).value.subject).toBe(extraSubject);
    checked("logout-clears-catalog-cookie-preserves-provider-sso");
    stage("issuer-outage");
    await command([...compose, "stop", "identity"]);
    expect((await product(additional, "me")).status).toBe(200);
    const outage = await newPage();
    await outage.goto(ui + "/");
    const [unavailable] = await Promise.all([
      outage.waitForResponse(
        (response) => new URL(response.url()).pathname === "/auth/login",
      ),
      outage.getByRole("button", { name: "Sign in", exact: true }).click(),
    ]);
    expect(unavailable.status()).toBe(503);
    await expect(outage.getByRole("alert")).toContainText(
      "Sign-in is temporarily unavailable.",
    );
    checked("issuer-outage-without-development-fallback");
    stage("retained-restart");
    await browser.close();
    await command([...makeAuth, "local-auth-down"]);
    await command(startup, 300);
    expect(await inputHashes(directory)).toEqual(hashes);
    await launchBrowser();
    const restarted = await newPage();
    await login(restarted, "catalog-extra", extraPassword);
    expect((await product(restarted, "me")).value.subject).toBe(extraSubject);
    const restartedAdmin = await newPage();
    await login(restartedAdmin, "catalog-admin", adminPassword);
    expect(
      (await product(restartedAdmin, "memberships/" + member.id)).value.state,
    ).toBe("suspended");
    const retainedGrants = await product(
      restartedAdmin,
      "role-assignments?membershipId=" + member.id + "&limit=100",
    );
    expect(
      retainedGrants.value.items.filter((item) => item.state === "active"),
    ).toHaveLength(0);
    expect(
      retainedGrants.value.items.some(
        (item) => item.id === assignment.id && item.state === "revoked",
      ),
    ).toBe(true);
    expect(
      (await product(restartedAdmin, "resources/" + resource.value.id)).status,
    ).toBe(200);
    const restartedReader = await newPage();
    await login(restartedReader, "catalog-reader", changedPassword, false);
    await expect(restartedReader.getByRole("alert")).toContainText(
      "The Catalog did not accept this account.",
    );
    checked("restart-retains-inputs-user-password-and-suspension");
    stage("certificate-renewal");
    await browser.close();
    await command([...launcher, "renew", "--directory", directory]);
    const renewed = await inputHashes(directory);
    for (const [path, hash] of Object.entries(hashes)) {
      if (path.startsWith("trust/")) expect(renewed[path]).not.toBe(hash);
      else expect(renewed[path]).toBe(hash);
    }
    await command(startup, 300);
    await launchBrowser();
    const renewedPage = await newPage();
    await login(renewedPage, "catalog-admin", adminPassword);
    expect((await product(renewedPage, "me")).value.subject).toBe(
      accounts["catalog-admin"],
    );
    checked("tls-renewal-preserves-accounts-keys-and-data");
    await cliOIDCJourney({
      command,
      browser,
      image,
      directory,
      certificate: join(directory, "trust/ca.pem"),
      issuer,
      apiOrigin,
      username: "catalog-admin",
      password: adminPassword,
      subject: accounts["catalog-admin"],
      project,
      loginOnly: true,
      onPhase: stage,
    });
    checked("human-cli-pkce-and-private-credentials");
    passed = true;
  } catch (error) {
    const mismatch = error?.matcherResult;
    if (
      Number.isInteger(mismatch?.expected) &&
      Number.isInteger(mismatch?.actual)
    )
      console.error(
        `Status/count assertion: expected ${mismatch.expected}, received ${mismatch.actual}`,
      );
    console.error(
      "Local password workflow stopped during " +
        phase +
        "; private payloads were not recorded",
    );
  } finally {
    onCleanup();
    try {
      await browser?.close();
    } catch {
      passed = false;
      console.error("Owned password browser cleanup failed");
    }
    async function removeOwned() {
      if (!engineChecked) {
        await rm(privateDirectory, { recursive: true, force: true });
        return;
      }
      // Startup can fail after preparing inputs. Recover only this invocation's
      // validated generated project for cleanup, never an ambient Compose name.
      if (!compose) {
        try {
          const values = await readFile(join(directory, "compose.env"), "utf8");
          project = values.match(
            /^COMPOSE_PROJECT_NAME="(catalog-docs-[a-f0-9]{8})"$/m,
          )?.[1];
          if (!project) throw new Error("Invalid owned project");
          compose = [
            "docker",
            "compose",
            "--project-name",
            project,
            "--env-file",
            join(directory, "compose.env"),
            "--file",
            join(directory, "compose.single.json"),
          ];
          frontendImage = JSON.parse(
            await readFile(join(directory, "compose.single.json"), "utf8"),
          ).services.frontend.image;
        } catch (error) {
          if (error.code !== "ENOENT") throw error;
        }
      }
      if (compose)
        await command(
          [...compose, "down", "--volumes", "--remove-orphans"],
          120,
          true,
        );
      if (project) {
        const filter = "label=com.docker.compose.project=" + project;
        expect(
          (
            await command(["docker", "ps", "-aq", "--filter", filter], 30, true)
          ).trim(),
        ).toBe("");
        for (const kind of ["network", "volume"])
          expect(
            (
              await command(
                ["docker", kind, "ls", "-q", "--filter", filter],
                30,
                true,
              )
            ).trim(),
          ).toBe("");
      }
      for (const tag of [image, uiImage, frontendImage].filter(Boolean)) {
        if (
          (
            await command(["docker", "image", "ls", "--quiet", tag], 20, true)
          ).trim()
        )
          await command(["docker", "image", "rm", tag], 30, true);
      }
      await rm(privateDirectory, { recursive: true, force: true });
    }
    try {
      await removeOwned();
      cleaned = true;
    } catch {
      console.error(
        "Inspect owned password fixture before cleanup: " + privateDirectory,
      );
    }
    await writeFile(
      join(output, "report.json"),
      JSON.stringify(
        {
          status: passed && cleaned && !isInterrupted() ? "passed" : "failed",
          phase,
          checks,
          cleanup: cleaned ? "passed" : "failed",
        },
        null,
        2,
      ) + "\n",
    );
    console.log(
      "Local password workflow report: " + join(output, "report.json"),
    );
  }
  if (!passed || !cleaned || isInterrupted())
    throw new Error("Local password workflow incomplete");
}
