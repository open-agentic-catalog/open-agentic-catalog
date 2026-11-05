// Actual downloads are packed and matched byte for byte to the immutable HTTPS fixture.
import { test, expect, login, confirmCommand } from "./fixtures";
import { required } from "../required";
import { array, object, text } from "./json";
import {
  downloadPluginFiles,
  fillPluginFields,
} from "./authoring-plugin-fields";

test.use({ demoArchives: true, authoredPlugin: true });
test("Plugin tutorial downloads pass real archive admission with their exact files", async ({
  page,
  catalog,
}, testInfo) => {
  const archive = required(catalog.generatedPlugin);
  const writes: string[] = [];
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== catalog.origin
    )
      outside.push(url.origin);
    if (
      request.method() === "POST" &&
      url.pathname.includes("/_catalog/api/v1/")
    )
      writes.push(url.pathname);
  });
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("plugin");
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill("urn:air:demo.catalog.example.com:plugins:review-tools-tutorial");
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
  await fillPluginFields(page);
  await page.evaluate(() => {
    (document.activeElement as HTMLElement).blur();
    window.scrollTo(0, 0);
  });
  await page.screenshot({
    path: testInfo.outputPath("plugin-tutorial-fields.png"),
    fullPage: true,
  });
  const documents = await downloadPluginFiles(page);
  await archive.verifyDownloads(documents);
  expect(writes).toEqual([]);
  expect(outside).toEqual([]);
  await page
    .getByRole("button", { name: "Continue with archive inputs", exact: true })
    .click();
  await page
    .getByLabel("Plugin archive URL", { exact: true })
    .fill(archive.url);
  await page
    .getByLabel("Expected archive checksum (optional)", { exact: true })
    .fill(archive.digest);
  await page.getByLabel("Include Listing details", { exact: true }).check();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Publisher", exact: true })
    .selectOption(catalog.receipt.publisherId);
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/resources\/[^/]+\/versions$/.test(new URL(response.url()).pathname),
  );
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await confirmCommand(page);
  const response = await saved;
  expect(response.status()).toBe(201);
  const bundle = object(await response.json());
  expect(object(bundle.evidence).state).toBe("complete");
  await expect(
    page.getByRole("heading", { name: "Listing draft saved", exact: true }),
  ).toBeVisible();
  const retained = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${text(object(bundle.revision).id)}`,
  );
  const representation = object(
    required(array(object(retained.data.revision).representations)[0]),
  );
  expect(representation.contentDigest).toBe(archive.digest);
  expect(representation.size).toBe(archive.size);
  const plugin = object(representation.plugin);
  expect(
    array(plugin.documents)
      .map((value) => {
        const item = object(value);
        return { path: item.path, content: item.content };
      })
      .sort((a, b) => text(a.path).localeCompare(text(b.path))),
  ).toEqual([...documents].sort((a, b) => a.path.localeCompare(b.path)));
  expect(array(plugin.components)).toHaveLength(2);
  expect(writes).toHaveLength(4);
  expect(outside).toEqual([]);
});
