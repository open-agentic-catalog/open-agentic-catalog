import { openDisclosure } from "./editor-disclosure";
import { test, expect, login, confirmCommand } from "./fixtures";

test("namespace grant persists and duplicate rejection preserves the editable scope", async ({
  page,
  catalog,
  network,
}) => {
  await login(page, catalog, "local-admin", "/members");
  await openDisclosure(page, "Add Catalog member");
  const form = page
    .locator("section.workspace-panel")
    .filter({
      has: page.getByRole("heading", {
        name: /^(Add Catalog member|Grant a built-in role)$/,
      }),
    })
    .last();
  await form
    .getByRole("textbox", { name: "Subject", exact: true })
    .fill("browser-scoped-member");
  await form
    .getByRole("combobox", { name: "Catalog role", exact: true })
    .selectOption("contributor");
  await form
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("namespace");
  await form
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await form
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Scoped contributor for browser test");
  await form
    .getByRole("button", {
      name: /^(Add Catalog member|Grant a built-in role)$/,
    })
    .click();
  await confirmCommand(page);
  await page
    .getByRole("link", { name: "Open updated membership", exact: true })
    .click();
  await page.reload();
  await openDisclosure(page, "Grant a built-in role");
  const assignments = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Direct role assignments",
      exact: true,
    }),
  });
  await expect(assignments).toContainText("contributor");
  await expect(assignments).toContainText(
    `Namespace ${catalog.receipt.namespaceId}`,
  );
  await expect(assignments).toContainText("active");
  const attribution = page.getByText("Assignment attribution", { exact: true });
  await attribution.click();
  await expect(attribution.locator("..").locator("pre")).toContainText(
    "Scoped contributor for browser test",
  );
  await form
    .getByRole("combobox", { name: "Catalog role", exact: true })
    .selectOption("contributor");
  await form
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("namespace");
  await form
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await form
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Duplicate grant must not lose my input");
  network.expectFailure("POST", "/_catalog/api/v1/role-assignments", 409);
  await form
    .getByRole("button", {
      name: /^(Add Catalog member|Grant a built-in role)$/,
    })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
    "CONFLICT",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Back to form", exact: true })
    .click();
  await expect(
    form.getByRole("combobox", { name: "Namespace", exact: true }),
  ).toHaveValue(catalog.receipt.namespaceId);
  await expect(
    form.getByRole("textbox", { name: "Reason", exact: true }),
  ).toHaveValue("Duplicate grant must not lose my input");
});
