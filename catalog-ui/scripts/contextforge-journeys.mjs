// Native peer browser checks in the ContextForge supervisor's owned fixture.
import { chromium, expect } from "@playwright/test";
import { createHash, X509Certificate } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { resolve, join, dirname, basename } from "node:path";
import { fileURLToPath } from "node:url";

const [directory, address] = process.argv.slice(2);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
if (
  !directory ||
  dirname(resolve(directory)) !==
    join(root, "output/interoperability/contextforge") ||
  !/^catalog-contextforge-[0-9a-f]{12}$/.test(basename(directory)) ||
  !/^https:\/\/127\.0\.0\.1:\d+\/v1\/admin\/$/.test(address ?? "")
)
  throw new Error("Select the owned fixture and its loopback Admin UI");
const owner = JSON.parse(await readFile(join(directory, "owner.json"), "utf8"));
if (
  owner.directory !== resolve(directory) ||
  owner.project !== basename(directory)
)
  throw new Error("Fixture ownership does not match");
const privateDirectory = join(directory, "private");
const login = JSON.parse(
  await readFile(join(privateDirectory, "login.json"), "utf8"),
);
const fixtureSecrets = JSON.parse(
  await readFile(join(privateDirectory, "secrets.json"), "utf8"),
);
const peerCredentials = JSON.parse(
  await readFile(
    join(privateDirectory, "client-state/contextforge-connections.json"),
    "utf8",
  ),
);
const knownSecrets = [
  login.password,
  ...Object.values(fixtureSecrets.tokens),
  ...Object.values(fixtureSecrets).filter((value) => typeof value === "string"),
  peerCredentials.admin,
  ...Object.values(peerCredentials.connections).map(
    (connection) => connection.token,
  ),
];
const assertSecretFree = async (table) => {
  const text = await table.innerText();
  for (const value of knownSecrets)
    if (value)
      expect(
        text.includes(value),
        "the table must omit known fixture credentials",
      ).toBe(false);
};
const certificate = new X509Certificate(
  await readFile(join(privateDirectory, "frontend/server.pem")),
);
const spki = createHash("sha256")
  .update(certificate.publicKey.export({ type: "spki", format: "der" }))
  .digest("base64");
const origin = new URL(address).origin;
const outcome = {};
const diagnostics = {};
let browser;
let stage = "private-certificate";
try {
  // A separate browser first proves that system trust alone rejects this leaf.
  browser = await chromium.launch({ args: ["--no-proxy-server"] });
  const untrusted = await browser.newPage();
  let refused = false;
  try {
    await untrusted.goto(address, { timeout: 10000 });
  } catch (error) {
    diagnostics.certificateFailure =
      String(error).match(/net::ERR_[A-Z_]+/)?.[0] ?? "unclassified";
    refused = String(error).includes("ERR_CERT_AUTHORITY_INVALID");
  }
  expect(refused).toBe(true);
  await browser.close();
  browser = await chromium.launch({
    args: [
      `--ignore-certificate-errors-spki-list=${spki}`,
      "--no-proxy-server",
    ],
  });
  const context = await browser.newContext({
    viewport: { width: 1920, height: 1200 },
    serviceWorkers: "block",
    acceptDownloads: false,
  });
  const external = [];
  await context.route("**/*", async (route) => {
    if (new URL(route.request().url()).origin !== origin) {
      external.push("unapproved origin");
      await route.abort();
    } else await route.continue();
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", () => errors.push("browser exception"));
  stage = "login";
  await page.goto(address + "login");
  await page.getByLabel(/Email\s+address/).fill(login.email);
  await page.getByLabel("Password", { exact: true }).fill(login.password);
  await page.getByRole("button", { name: /Sign in/i }).click();
  await expect(page.getByTestId("servers-tab")).toBeVisible();
  outcome.login = true;
  stage = "servers";
  await page.getByTestId("servers-tab").click();
  for (const name of ["native-a", "native-b", "native-everything"])
    await expect(
      page.locator("#servers-table").getByText(name, { exact: true }),
    ).toBeVisible();
  const text = await page.locator("body").innerText();
  expect(text).not.toContain(login.password);
  expect(/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/.test(text)).toBe(
    false,
  );
  await assertSecretFree(page.locator("#servers-table"));
  await page.screenshot({
    path: join(directory, "admin-ui.png"),
    fullPage: false,
  });
  outcome.servers = true;
  stage = "gateways";
  await page.getByTestId("gateways-tab").click();
  for (const name of ["native-a", "native-b", "native-everything"])
    await expect(
      page.locator("#gateways-table").getByRole("cell", { name, exact: true }),
    ).toBeVisible();
  const gatewayText = await page.locator("body").innerText();
  expect(gatewayText).not.toContain(login.password);
  expect(
    /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/.test(gatewayText),
  ).toBe(false);
  await assertSecretFree(page.locator("#gateways-table"));
  expect(
    await page.locator("#gateways-table").evaluate((table) => {
      const clip = table.parentElement.getBoundingClientRect();
      return [...table.querySelectorAll("th,td")].every((cell) => {
        const rect = cell.getBoundingClientRect();
        return (
          rect.left >= clip.left &&
          rect.right <=
            Math.min(clip.right, table.ownerDocument.defaultView.innerWidth)
        );
      });
    }),
    "all registration columns fit without clipping",
  ).toBe(true);
  await page
    .locator("#gateways-table")
    .screenshot({ path: join(directory, "upstream-registrations.png") });
  outcome.gateways = true;
  stage = "logout";
  page.on("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Logout|Log out|Sign out/i }).click();
  await expect(page.getByLabel(/Email\s+address/)).toBeVisible();
  // Revisit the protected alias that receives the native /admin cookie.
  // A login form alone does not prove that logout removed the session.
  await page.goto(origin + "/admin/");
  await expect(page.getByLabel(/Email\s+address/)).toBeVisible();
  await expect(page.getByTestId("servers-tab")).toHaveCount(0);
  outcome.logout = true;
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
  outcome.localRequestsOnly = true;
  await writeFile(
    join(directory, "browser.json"),
    JSON.stringify({ status: "passed", ...outcome }),
    { mode: 0o600 },
  );
  console.log(JSON.stringify(outcome));
} catch (error) {
  await writeFile(
    join(directory, "browser.json"),
    JSON.stringify({
      status: "failed",
      stage,
      type: error.constructor.name,
      completed: outcome,
      diagnostics,
    }),
    { mode: 0o600 },
  );
  process.exitCode = 1;
} finally {
  await browser?.close();
}
