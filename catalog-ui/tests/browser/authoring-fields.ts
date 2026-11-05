// Literal tutorial inputs go through visible controls, without native JSON injection.
import type { Page } from "@playwright/test";

export async function fillAgentFields(page: Page) {
  await page
    .getByLabel("Agent name", { exact: true })
    .fill("Order status Agent");
  await page
    .getByLabel("Agent description", { exact: true })
    .fill("Explain an order and its next step.");
  await page
    .getByLabel("Interface 1 HTTPS URL", { exact: true })
    .fill("https://agents.example.test/orders");
  await page
    .getByRole("combobox", { name: "Input format 1", exact: true })
    .selectOption({ label: "JSON" });
  await page
    .getByLabel("Ability 1 name", { exact: true })
    .fill("Explain order status");
  await page
    .getByRole("button", {
      name: "Use suggested ability ID: explain-order-status",
      exact: true,
    })
    .click();
  await page
    .getByLabel("Ability 1 description", { exact: true })
    .fill("Explain order status without changing the order.");
  await page.getByLabel("Ability 1 tag 1", { exact: true }).fill("orders");
}

export async function fillHostedMCPFields(page: Page) {
  await page
    .getByLabel("MCP server name", { exact: true })
    .fill("com.example/documentation");
  await page
    .getByLabel("MCP display title (optional)", { exact: true })
    .fill("Documentation search");
  await page
    .getByLabel("MCP description", { exact: true })
    .fill("Search documentation without changing it.");
  await page.getByRole("button", { name: "Add endpoint", exact: true }).click();
  await page
    .getByLabel("Endpoint 1 URL", { exact: true })
    .fill("https://mcp.example.test/documentation");
}

export async function fillSkillFields(page: Page) {
  await page.getByLabel("Skill name", { exact: true }).fill("review-notes");
  await page
    .getByLabel("Skill description", { exact: true })
    .fill("Prepare release review notes.");
  await page
    .getByLabel("Skill instructions (Markdown)", { exact: true })
    .fill(
      "Read the release notes. Summarize the changes and list open questions.",
    );
}
