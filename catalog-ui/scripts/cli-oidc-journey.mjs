// The rebuilt CLI image acquires credentials from the owned real issuer.
// Private tokens stay in this invocation's mounted directory, never in reports.
import { mkdir, readFile, writeFile, stat } from "node:fs/promises";
import { join } from "node:path";
import { userInfo } from "node:os";
import { expect } from "@playwright/test";
import { localPorts } from "./browser-fixture.mjs";

export async function cliOIDCJourney({
  command,
  browser,
  image,
  directory,
  certificate,
  issuer,
  apiOrigin,
  username,
  password,
  subject,
  project,
  onPhase,
  loginOnly = false,
}) {
  const privateFiles = join(directory, "cli-auth");
  await mkdir(privateFiles, { mode: 0o700 });
  const { uid, gid } = userInfo();
  const container = project + "-cli-auth";
  const base = [
    "docker",
    "run",
    "--rm",
    "--name",
    container,
    "--network",
    "host",
    "--add-host",
    `${new URL(issuer).hostname}:127.0.0.1`,
    "--add-host",
    `${new URL(apiOrigin).hostname}:127.0.0.1`,
    "--user",
    `${uid}:${gid}`,
    "--read-only",
    "--cap-drop=ALL",
    "--security-opt=no-new-privileges:true",
    "--mount",
    `type=bind,src=${privateFiles},dst=/credentials`,
    "--mount",
    `type=bind,src=${certificate},dst=/issuer.pem,readonly`,
    "--entrypoint",
    "/usr/local/bin/catalogctl",
    image,
  ];
  const file = "/credentials/login.json";
  const context = await browser.newContext();
  let pending;
  let page;
  let callbackOrigin;
  try {
    // Use the fixture's browser-safe private range, independent of host ephemeral-port settings.
    const [callbackPort] = await localPorts();
    onPhase("cli-authorization-start");
    let announce;
    const announced = new Promise((resolve) => {
      announce = resolve;
    });
    pending = command(
      [
        ...base,
        "auth",
        "login",
        "--file",
        file,
        "--issuer",
        issuer,
        "--client-id",
        "catalog-cli",
        "--base-url",
        apiOrigin,
        "--issuer-ca-file",
        "/issuer.pem",
        ...(apiOrigin.startsWith("https:") ? ["--ca-file", "/issuer.pem"] : []),
        ...(loginOnly ? [] : ["--scope", "offline_access"]),
        "--listen",
        "127.0.0.1:" + callbackPort,
        "--timeout",
        "2m",
      ],
      135,
      false,
      (diagnostic) => {
        const found = diagnostic.match(
          /Open this URL in your browser:\n([^\n]+)\n/,
        );
        if (found) announce(found[1]);
      },
    );
    // Observe failure immediately, including a failure before URL output.
    const finished = pending.then(
      (result) => ({ result }),
      () => ({ failed: true }),
    );
    const first = await Promise.race([
      announced.then((url) => ({ url })),
      finished,
    ]);
    if (!first.url) throw new Error("CLI did not start browser authorization");
    const authorization = new URL(first.url);
    const callback = new URL(authorization.searchParams.get("redirect_uri"));
    callbackOrigin = callback.origin;
    if (
      authorization.origin !== new URL(issuer).origin ||
      callback.hostname !== "127.0.0.1" ||
      callback.port !== String(callbackPort) ||
      callback.pathname !== "/oauth/callback"
    )
      throw new Error("CLI selected an unexpected OAuth destination");
    await context.route("**/*", async (route) => {
      const origin = new URL(route.request().url()).origin;
      if (origin === authorization.origin || origin === callback.origin)
        await route.continue();
      else await route.abort();
    });
    page = await context.newPage();
    page.on("response", (response) => {
      if (new URL(response.url()).origin === callback.origin)
        console.log(`CLI loopback response status: ${response.status()}`);
    });
    onPhase("cli-provider-form");
    await page.goto(first.url);
    await page.getByLabel("Username or email").fill(username);
    await page.getByLabel("Password", { exact: true }).fill(password);
    onPhase("cli-loopback-callback");
    await page.getByRole("button", { name: "Sign In", exact: true }).click();
    await expect(
      page.getByText("Login response received. Return to the terminal.", {
        exact: true,
      }),
    ).toBeVisible();
    const outcome = await finished;
    if (outcome.failed || !JSON.parse(outcome.result).loggedIn)
      throw new Error("CLI human login failed");
    pending = undefined;
    const path = join(privateFiles, "login.json");
    onPhase("cli-private-credentials");
    const original = JSON.parse(await readFile(path, "utf8"));
    if (
      original.subject !== subject ||
      original.apiOrigin !== apiOrigin ||
      original.issuer !== issuer ||
      (!loginOnly && !original.refreshToken) ||
      ((await stat(path)).mode & 0o777) !== 0o600
    )
      throw new Error("CLI credentials have an invalid binding or file mode");
    const remote = [
      ...base,
      "remote",
      "capabilities",
      "--base-url",
      apiOrigin,
      "--login-file",
      file,
      "--issuer-ca-file",
      "/issuer.pem",
      ...(apiOrigin.startsWith("https:") ? ["--ca-file", "/issuer.pem"] : []),
    ];
    onPhase("cli-remote-read");
    JSON.parse(await command(remote));
    if (loginOnly) {
      const status = JSON.parse(
        await command([...base, "auth", "status", "--file", file]),
      );
      expect(status.loggedIn).toBe(true);
      expect(status.expired).toBe(false);
      const logout = JSON.parse(
        await command([...base, "auth", "logout", "--file", file]),
      );
      expect(logout.loggedIn).toBe(false);
      console.log(
        "Local password CLI PKCE login, private credentials, TLS API read and logout passed",
      );
      return;
    }
    // Expire the synthetic cache entry to exercise a real issuer refresh.
    await writeFile(
      path,
      JSON.stringify({ ...original, expiresAt: "2000-01-01T00:00:00Z" }),
      { mode: 0o600 },
    );
    onPhase("cli-real-refresh");
    JSON.parse(await command(remote));
    const renewed = JSON.parse(await readFile(path, "utf8"));
    if (
      renewed.subject !== subject ||
      renewed.membershipId !== original.membershipId ||
      new Date(renewed.expiresAt) <= new Date()
    )
      throw new Error("CLI refresh changed identity or did not renew access");
    const connection = [
      "--base-url",
      apiOrigin,
      "--login-file",
      file,
      "--issuer-ca-file",
      "/issuer.pem",
    ];
    const invoke = async (args) =>
      JSON.parse(await command([...base, ...args, ...connection]));
    const expireLogin = async () => {
      const current = JSON.parse(await readFile(path, "utf8"));
      await writeFile(
        path,
        JSON.stringify({ ...current, expiresAt: "2000-01-01T00:00:00Z" }),
        { mode: 0o600 },
      );
    };
    onPhase("cli-draft-export-preview");
    await invoke([
      "export",
      "preview",
      "--query",
      "search",
      "--kind",
      "agent",
      "--output-dir",
      "/credentials/selection",
    ]);
    const selection = JSON.parse(
      await readFile(join(privateFiles, "selection/selection.json"), "utf8"),
    );
    expect(selection.complete).toBe(true);
    expect(selection.items).toHaveLength(1);
    const selected = selection.items[0];
    const record = await invoke([
      "remote",
      "record",
      "--id",
      selected.releaseId,
    ]);
    onPhase("cli-draft-export-refresh");
    await expireLogin();
    await invoke([
      "export",
      "--format",
      "draft",
      "--release-id",
      selected.releaseId,
      "--output-dir",
      "/credentials/export",
    ]);
    const draft = JSON.parse(
      await readFile(join(privateFiles, "export/draft.json"), "utf8"),
    );
    expect(draft.kind).toBe("agent");
    expect(draft.listing).toBeTruthy();
    onPhase("cli-draft-export-batch");
    const batch = await invoke([
      "export",
      "batch",
      "--format",
      "draft",
      "--selection-file",
      "/credentials/selection/selection.json",
      "--output-dir",
      "/credentials/batch",
    ]);
    expect(batch.complete).toBe(true);
    expect(batch.items).toHaveLength(1);
    expect(batch.items[0].outcome).toBe("complete");
    expect(
      await readFile(
        join(privateFiles, "batch", selected.releaseId, "draft.json"),
        "utf8",
      ),
    ).toBe(await readFile(join(privateFiles, "export/draft.json"), "utf8"));
    const imported = {
      ...draft,
      canonicalIdentifier: "custom:cli-oidc-draft-transfer",
    };
    await writeFile(
      join(privateFiles, "import.json"),
      JSON.stringify(imported),
      { mode: 0o600 },
    );
    const destination = [
      "--file",
      "/credentials/import.json",
      "--namespace-id",
      record.data.resource.namespaceId,
      "--content-only",
      "--state-file",
      "/credentials/import/state.json",
    ];
    onPhase("cli-draft-import-refresh");
    await expireLogin();
    const applied = await invoke(["import", "apply", ...destination]);
    expect(applied.disposition).toBe("complete");
    expect(applied.items[0].steps).toHaveLength(2);
    onPhase("cli-draft-import-resume");
    const resumed = await invoke(["import", "resume", ...destination]);
    expect(resumed).toEqual(applied);
    onPhase("cli-draft-exact-readback");
    await invoke([
      "export",
      "--format",
      "draft",
      "--version-revision-id",
      applied.items[0].steps[1].ack.revisionId,
      "--output-dir",
      "/credentials/readback",
    ]);
    const readback = JSON.parse(
      await readFile(join(privateFiles, "readback/draft.json"), "utf8"),
    );
    expect(readback.canonicalIdentifier).toBe(imported.canonicalIdentifier);
    expect(readback.version).toBe(imported.version);
    expect(readback.representations).toHaveLength(
      imported.representations.length,
    );
    for (let index = 0; index < imported.representations.length; index++) {
      const expected = imported.representations[index];
      const actual = readback.representations[index];
      expect({ ...actual, input: undefined }).toEqual({
        ...expected,
        input: undefined,
      });
      if (expected.input.mode === "data") {
        expect(actual.input.mode).toBe("data");
        // Object admission can normalize whitespace and key order. Codec and
        // CLI regressions separately check exact unknown numeric tokens.
        expect(JSON.parse(actual.input.text)).toEqual(
          JSON.parse(expected.input.text),
        );
      } else expect(actual.input).toEqual(expected.input);
    }
    expect(readback.listing).toBeUndefined();
    onPhase("cli-status-logout");
    const status = JSON.parse(
      await command([...base, "auth", "status", "--file", file]),
    );
    if (!status.loggedIn || !status.canRefresh || status.expired)
      throw new Error("CLI local status failed");
    const logout = JSON.parse(
      await command([...base, "auth", "logout", "--file", file]),
    );
    if (logout.loggedIn) throw new Error("CLI local logout failed");
    console.log(
      "Packaged CLI public-client PKCE login, explicit remote access, draft import/export, real refresh and local logout passed",
    );
  } catch (error) {
    // Playwright errors can contain the authorization URL. Emit only known
    // transport categories, never the error text or a URL.
    const categories = [
      "timeout",
      "err_name_not_resolved",
      "err_connection_reset",
      "err_connection_refused",
      "err_connection_closed",
      "err_network_changed",
      "err_ssl_protocol_error",
      "err_timed_out",
      "err_too_many_redirects",
      "err_invalid_argument",
      "err_failed",
      "err_cert_authority_invalid",
      "err_aborted",
      "err_blocked_by_client",
      "err_empty_response",
      "has been closed",
      "protocol error (page.navigate)",
    ].filter((value) => String(error?.message).toLowerCase().includes(value));
    if (categories.length)
      console.error(`CLI browser failure: ${categories.join(",")}`);
    if (page) {
      const current = new URL(page.url());
      const destination =
        current.origin === callbackOrigin
          ? "loopback"
          : current.origin === new URL(issuer).origin
            ? "issuer"
            : "other";
      console.error(`CLI browser stopped at the ${destination} origin`);
      const protocol = ["http:", "https:", "about:", "chrome-error:"].includes(
        current.protocol,
      )
        ? current.protocol
        : "other";
      console.error(`CLI browser protocol category: ${protocol}`);
      if (await page.getByRole("button", { name: "Yes", exact: true }).count())
        console.error("CLI provider requests additional consent");
    }
    throw error;
  } finally {
    await context.close();
    if (pending) await pending.catch(() => {});
    const remaining = (
      await command(
        ["docker", "ps", "--all", "--quiet", "--filter", `name=^${container}$`],
        30,
        true,
      )
    ).trim();
    if (remaining)
      await command(["docker", "rm", "--force", container], 30, true);
  }
}
