import { openDisclosure } from "./editor-disclosure";
import { test, expect, login, confirmCommand } from "./fixtures";
import { object, text } from "./json";

test("keyboard completion supports consecutive group additions and assignment grants", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/access-groups/new");
  await page
    .getByLabel("Group name", { exact: true })
    .fill("Command completion group");
  await page
    .getByLabel("Metadata change reason", { exact: true })
    .fill("Create local group");
  await page.getByRole("button", { name: "Create group", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByLabel("Command summary", { exact: true }),
  ).toContainText("Command completion group");
  await dialog.getByRole("button", { name: "Confirm", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("status")).toBeFocused();
  await page
    .getByRole("link", { name: "Open saved group", exact: true })
    .click();
  const groupID = new URL(page.url()).pathname.split("/").at(-1)!;
  const membershipPage = object(
    (await catalog.admin("GET", "/api/v1/memberships?limit=25")).data,
  );
  const members = (membershipPage.items as unknown[])
    .map(object)
    .filter((item) => item.state === "active")
    .slice(0, 2);
  expect(members).toHaveLength(2);
  const writes: { key: string; body: unknown }[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      /\/(members|assignments)$/.test(new URL(request.url()).pathname)
    )
      writes.push({
        key: request.headers()["idempotency-key"]!,
        body: request.postDataJSON(),
      });
  });
  await openDisclosure(page, "Add group member");
  const add = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", { name: "Add group member", exact: true }),
  });
  for (let i = 0; i < 2; i++) {
    await add
      .getByLabel("Membership ID", { exact: true })
      .fill(text(members[i]!.id));
    await add.getByLabel("Reason", { exact: true }).fill(`Add member ${i}`);
    await add
      .getByRole("button", { name: "Add group member", exact: true })
      .click();
    await confirmCommand(page);
    await expect(add.getByRole("status")).toContainText("Group member added.");
    await expect(add.getByRole("status")).toBeFocused();
    if (!i) {
      await add
        .getByRole("button", { name: "Add another group member", exact: true })
        .click();
      await expect(
        add.getByLabel("Membership ID", { exact: true }),
      ).toHaveValue("");
      await expect(add.getByRole("status")).toContainText(
        "Group member added.",
      );
    }
  }
  const storedMembers = object(
    (
      await catalog.admin(
        "GET",
        `/api/v1/access/groups/${groupID}/members?limit=25`,
      )
    ).data,
  );
  expect(storedMembers.items).toHaveLength(2);
  await page.goto(`${catalog.origin}${catalog.basePath}/access-assignments`);
  const roles = object(
    (await catalog.admin("GET", "/api/v1/access/roles?limit=25")).data,
  );
  const role = (roles.items as unknown[])
    .map(object)
    .find((item) => item.builtin === true && item.key === "reader")!;
  expect(role).toBeTruthy();
  await openDisclosure(page, "Grant access");
  const grant = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Grant access assignment",
      exact: true,
    }),
  });
  await grant
    .getByLabel("Role definition ID", { exact: true })
    .fill(text(role.id));
  for (let i = 0; i < 2; i++) {
    await grant
      .getByLabel("Member or group ID", { exact: true })
      .fill(text(members[i]!.id));
    // Switching type clears only the operand, without sending a command.
    const before = writes.length;
    const target = grant.getByRole("combobox", {
      name: "Assignment target",
      exact: true,
    });
    await target.focus();
    await page.keyboard.press("End");
    await expect(target).toHaveValue("groupId");
    await expect(
      grant.getByLabel("Member or group ID", { exact: true }),
    ).toHaveValue("");
    await target.focus();
    await page.keyboard.press("Home");
    await expect(target).toHaveValue("membershipId");
    await expect(
      grant.getByLabel("Role definition ID", { exact: true }),
    ).toHaveValue(text(role.id));
    expect(writes).toHaveLength(before);
    await grant
      .getByLabel("Member or group ID", { exact: true })
      .fill(text(members[i]!.id));
    await grant
      .getByLabel("Reason", { exact: true })
      .fill(`Exact namespace grant ${i}`);
    await grant
      .getByRole("combobox", { name: "Assignment scope", exact: true })
      .selectOption("namespace");
    await grant
      .getByLabel("Namespace ID", { exact: true })
      .fill(catalog.receipt.namespaceId);
    await grant
      .getByRole("button", { name: "Grant access assignment", exact: true })
      .click();
    await confirmCommand(page);
    await expect(grant.getByRole("status")).toContainText("Access assigned.");
    if (!i)
      await grant
        .getByRole("button", {
          name: "Grant another access assignment",
          exact: true,
        })
        .click();
  }
  expect(writes).toHaveLength(4);
  expect(new Set(writes.map((write) => write.key)).size).toBe(4);
});
