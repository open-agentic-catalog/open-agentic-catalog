// The same visible Plugin fields and actual downloads serve both browser journeys.
import { readFile } from "node:fs/promises";
import type { Page } from "@playwright/test";
import { expect } from "@playwright/test";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";

export async function fillPluginFields(page: Page) {
  await page
    .getByRole("combobox", { name: "Plugin input", exact: true })
    .selectOption("prepare");
  await page.getByLabel("Plugin name", { exact: true }).fill("review-tools");
  await page
    .getByRole("button", {
      name: "Use Catalog version 1.0.0 in manifest",
      exact: true,
    })
    .click();
  await page
    .getByLabel("Plugin description (optional)", { exact: true })
    .fill("Prepare a release review.");
  await page
    .getByLabel("Plugin license (optional)", { exact: true })
    .fill("Apache-2.0");
  await page
    .getByRole("button", { name: "Add Skill component", exact: true })
    .click();
  await page.getByLabel("Skill name", { exact: true }).fill("review-notes");
  await page
    .getByLabel("Skill description", { exact: true })
    .fill("Prepare release review notes.");
  await page
    .getByLabel("Skill instructions (Markdown)", { exact: true })
    .fill("# Review\n\nRead the release notes.");
  await page
    .getByRole("button", { name: "Add MCP server", exact: true })
    .click();
  await page.getByLabel("Server 1 name", { exact: true }).fill("documentation");
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toBe(
      "Change transport and replace this server's transport fields?",
    );
    await dialog.accept();
  });
  await page
    .getByRole("combobox", { name: "Server 1 transport", exact: true })
    .selectOption("streamable-http");
  await page
    .getByLabel("MCP endpoint URL", { exact: true })
    .fill("https://mcp.example.test/documentation");
}

export async function downloadPluginFiles(page: Page) {
  await page
    .getByRole("button", {
      name: "Preview and download Plugin files",
      exact: true,
    })
    .click();
  const region = page.getByRole("region", { name: "Prepared Plugin files" });
  const documents: { path: string; content: string }[] = [];
  for (const path of [
    "plugin.json",
    "mcp.json",
    "skills/review-notes/SKILL.md",
  ]) {
    await openDisclosure(page, path);
    const downloading = page.waitForEvent("download");
    await region
      .getByRole("link", {
        name: `Download ${path.split("/").at(-1)}`,
        exact: true,
      })
      .click();
    documents.push({
      path,
      content: await readFile(
        required(await (await downloading).path()),
        "utf8",
      ),
    });
  }
  expect(documents.map((item) => item.path)).toEqual([
    "plugin.json",
    "mcp.json",
    "skills/review-notes/SKILL.md",
  ]);
  return documents;
}
