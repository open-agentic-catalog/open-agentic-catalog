import { object, array } from "./json";
import { test, expect, login, confirmCommand } from "./fixtures";
import { agentDocument } from "./published-fixture";
import { openDisclosure, revealControl } from "./editor-disclosure";

test("anonymous document URLs respect egress policy and can be corrected with an inline revision", async ({
  page,
  catalog,
  network,
}) => {
  const item = catalog.receipt.items.find((i) => i.key === "draft-agent")!;
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${item.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await openDisclosure(page, "Advanced Agent Card JSON");
  await expect(
    page.getByRole("textbox", { name: "Version label", exact: true }),
  ).toHaveAccessibleDescription(
    /up to 128 characters.*Match any version declared/,
  );
  await page
    .getByText("Advanced representation settings", { exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Media type", exact: true }),
  ).toHaveAccessibleDescription(/supported media type for this resource kind/);
  await expect(
    page.getByRole("textbox", { name: "Profile ID (optional)", exact: true }),
  ).toHaveAccessibleDescription(/does not choose another validator/);
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("3.0.0");
  await page
    .getByRole("combobox", { name: "Content source", exact: true })
    .selectOption("url");
  // Representation URLs have no private-network credential/profile selector.
  // Loopback must fail closed before a connection; no public test host needed.
  for (const url of [
    "http://example.test/card",
    "https://user:secret@example.test/card",
    "https://example.test/card#fragment",
  ]) {
    await page
      .getByRole("textbox", { name: "Document URL", exact: true })
      .fill(url);
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "credential-free HTTPS",
    );
    await expect(page.getByRole("dialog")).toHaveCount(0);
  }
  await page
    .getByRole("textbox", { name: "Document URL", exact: true })
    .fill("https://127.0.0.1/agent.json");
  network.expectFailure(
    "POST",
    `/_catalog/api/v1/resources/${item.resourceId}/versions`,
    503,
  );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
    "DEPENDENCY_UNAVAILABLE",
  );
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Back to form", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("textbox", { name: "Version label", exact: true }),
  ).toHaveValue("3.0.0");
  // A 503 is conservatively uncertain, so changing the pending command is
  // forbidden. Inspect the actual owned collection before abandoning it.
  const versions = await catalog.admin<{ items: { version: string }[] }>(
    "GET",
    `/api/v1/resources/${item.resourceId}/versions`,
  );
  expect(versions.data.items.map((v) => v.version)).not.toContain("3.0.0");
  page.once("dialog", (dialog) => dialog.accept());
  await page.reload();
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("3.0.0");
  await page
    .getByRole("combobox", { name: "Content source", exact: true })
    .selectOption("data");
  await openDisclosure(page, "Advanced Agent Card JSON");
  for (const [invalid, message] of [
    ["{", "Enter valid JSON"],
    ["[]", "Enter a JSON object"],
    [
      '{"number":9007199254740992}',
      "Whole numbers must be between -9007199254740991 and 9007199254740991.",
    ],
    ["[".repeat(66) + "0" + "]".repeat(66), "nesting exceeds"],
  ] as const) {
    await page
      .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
      .fill(invalid);
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(message);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  }
  await page
    .getByText("Advanced representation settings", { exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Profile ID (optional)", exact: true })
    .fill("a2a-agent-card-1.0.1-3303592");
  const large = {
    ...agentDocument,
    version: "3.0.0",
    description:
      "a".repeat(129 * 1024) + "<script data-ui-injection>unsafe</script>",
  };
  await page
    .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
    .fill(JSON.stringify(large));
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/resources/${item.resourceId}/versions`),
  );
  await confirmCommand(page);
  const saveResponse = await saved;
  expect(
    object(
      array(object(saveResponse.request().postDataJSON()).representations)[0],
    ).profileId,
  ).toBe("a2a-agent-card-1.0.1-3303592");
  expect(saveResponse.ok()).toBe(true);
  expect(object(object(await saveResponse.json()).evidence).state).toBe(
    "complete",
  );
  await expect(page.getByRole("status")).toContainText(
    "Draft saved. Version 3.0.0.",
  );
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Version 3.0.0", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("link", {
      name: "Inspect current revision and validation",
      exact: true,
    })
    .click();
  await page
    .getByRole("button", { name: "Read retained document", exact: true })
    .click();
  const retained = page
    .getByLabel("Text document", { exact: true })
    .filter({ visible: true });
  await expect(retained).toBeVisible();
  expect(JSON.parse(await retained.innerText())).toEqual(large);
  await expect(page.locator("script[data-ui-injection]")).toHaveCount(0);
  await expect(retained.locator(".syntax-string")).toHaveCount(0);
});

test("multiple representations preserve distinct documents after reload", async ({
  page,
  catalog,
  context,
}) => {
  const item = catalog.receipt.items.find((i) => i.key === "draft-agent")!;
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${item.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await openDisclosure(page, "Advanced Agent Card JSON");
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("2.0.0");
  await page
    .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
    .fill(JSON.stringify({ ...agentDocument, version: "2.0.0" }));
  await page
    .getByRole("button", { name: "Add representation", exact: true })
    .click();
  const secondary = page.getByRole("group", {
    name: /content 2$/,
    exact: true,
  });
  await secondary
    .getByText("Advanced representation settings", { exact: true })
    .click();
  await page
    .getByRole("group", { name: /content 1$/, exact: true })
    .getByText("Advanced representation settings", { exact: true })
    .click();
  await secondary
    .getByRole("combobox", { name: "Representation role", exact: true })
    .selectOption("documentation");
  await secondary
    .getByRole("textbox", { name: "Media type", exact: true })
    .fill("text/markdown");
  await secondary
    .getByRole("combobox", { name: "Content source", exact: true })
    .selectOption("content");
  const text =
    "# Supplementary evidence\n\n<script data-ui-injection>throw new Error('unsafe')</script>";
  await secondary
    .getByRole("textbox", { name: "Text content", exact: true })
    .fill(text);
  const firstRole = page
    .getByRole("combobox", { name: "Representation role", exact: true })
    .first();
  for (const [first, second] of [
    ["documentation", "documentation"],
    ["primary", "primary"],
  ] as const) {
    await firstRole.selectOption(first);
    await secondary
      .getByRole("combobox", { name: "Representation role", exact: true })
      .selectOption(second);
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("exactly one primary");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  }
  await secondary
    .getByRole("combobox", { name: "Representation role", exact: true })
    .selectOption("documentation");
  await page
    .getByRole("button", { name: "Add representation", exact: true })
    .click();
  await page
    .getByRole("group", { name: /content 3$/, exact: true })
    .getByRole("button", { name: "Remove representation 3", exact: true })
    .click();
  await expect(
    page.getByRole("group", { name: /content 3$/, exact: true }),
  ).toHaveCount(0);

  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await confirmCommand(page);
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Inspect current revision and validation" })
    .click();
  await page.reload();
  const sections = page.locator("div.workspace-panel");
  const primary = sections
    .filter({
      has: page.getByRole("heading", {
        name: "primary: application/a2a-agent-card+json",
        exact: true,
      }),
    })
    .last();
  const documentation = sections
    .filter({
      has: page.getByRole("heading", {
        name: "documentation: text/markdown",
        exact: true,
      }),
    })
    .last();
  await primary
    .getByRole("button", { name: "Read retained document", exact: true })
    .click();
  await expect(
    primary.getByRole("button", { name: "Copy document text", exact: true }),
  ).toHaveCount(0);
  await expect(primary.locator("pre").filter({ visible: true })).toContainText(
    '"version": "2.0.0"',
  );
  const exactReply = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.url().includes("/artifacts/") &&
      response.url().endsWith("/content"),
  );
  await documentation
    .getByRole("button", { name: "Read retained document", exact: true })
    .click();
  const rawDocument = await (await exactReply).text();
  expect(rawDocument).toBe(text);
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await documentation
    .getByRole("button", { name: "Copy document text", exact: true })
    .click();
  await expect(
    documentation.getByText("Copied.", { exact: true }),
  ).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    rawDocument,
  );

  await expect(
    documentation.locator("pre").filter({ visible: true }),
  ).toHaveText(text);
  await expect(page.locator("[data-ui-injection]")).toHaveCount(0);
});

test("representation drafts retain staged documents and keyboard focus until removal is confirmed", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (entry) => entry.key === "draft-agent",
  )!;
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${item.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await openDisclosure(page, "Advanced Agent Card JSON");
  const editorURL = page.url();
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() !== "GET" &&
      request.url().includes("/_catalog/api/v1/")
    )
      writes.push(request.url());
  });
  const first = page.getByRole("group", {
    name: /content 1$/,
    exact: true,
  });
  const primaryText = '{"name":"Staged primary document"}';
  await revealControl(first.getByLabel("Document file", { exact: true }));
  await first.getByLabel("Document file", { exact: true }).setInputFiles({
    name: "primary-card.json",
    mimeType: "application/json",
    buffer: Buffer.from(primaryText),
  });
  await expect(
    first.getByRole("button", { name: "Use loaded document", exact: true }),
  ).toBeVisible();
  await expect(
    first.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    }),
  ).toHaveValue("");
  // A selected but unapplied file is the only draft at this point.
  const stay = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("beforeunload");
    await dialog.dismiss();
  });
  await page
    .getByRole("link", { name: "Back to resource", exact: true })
    .click();
  await stay;
  await expect(page).toHaveURL(editorURL);
  await expect(
    first.getByText("primary-card.json", { exact: true }),
  ).toBeVisible();
  await first
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await expect(
    first.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    }),
  ).toHaveValue(primaryText);

  const add = page.getByRole("button", {
    name: "Add representation",
    exact: true,
  });
  await add.focus();
  await page.keyboard.press("Enter");
  const row = page.getByRole("group", {
    name: /content 2$/,
    exact: true,
  });
  const rowID = await row.getAttribute("id");
  await expect(
    row.getByRole("combobox", { name: "Content source", exact: true }),
  ).toBeFocused();
  const text = row.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
    includeHidden: true,
  });
  await revealControl(text);
  await openDisclosure(page, "Advanced Agent Card JSON");
  const remove = row.getByRole("button", {
    name: "Remove representation 2",
    exact: true,
  });
  await text.fill('{"name":"Unsaved secondary document"}');
  const rejectInput = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("confirm");
    expect(dialog.message()).toContain("unsaved input and selected document");
    await dialog.dismiss();
  });
  await remove.click();
  await rejectInput;
  await expect(row).toHaveAttribute("id", rowID!);
  await expect(text).toHaveValue('{"name":"Unsaved secondary document"}');

  await text.fill("");
  const secondaryText = '{"name":"Staged secondary document","literal":"café"}';
  await revealControl(row.getByLabel("Document file", { exact: true }));
  await row.getByLabel("Document file", { exact: true }).setInputFiles({
    name: "secondary-card.json",
    mimeType: "application/json",
    buffer: Buffer.from(secondaryText),
  });
  await expect(
    row.getByRole("button", { name: "Use loaded document", exact: true }),
  ).toBeVisible();
  const rejectFile = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("confirm");
    await dialog.dismiss();
  });
  await remove.click();
  await rejectFile;
  await expect(row).toHaveAttribute("id", rowID!);
  await expect(
    row.getByText("secondary-card.json", { exact: true }),
  ).toBeVisible();
  await expect(text).toHaveValue("");
  await row
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await expect(text).toHaveValue(secondaryText);
  const acceptRemoval = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("confirm");
    await dialog.accept();
  });
  await remove.click();
  await acceptRemoval;
  await expect(row).toHaveCount(0);
  await expect(
    first.getByRole("combobox", { name: "Content source", exact: true }),
  ).toBeFocused();
  await expect(
    first.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    }),
  ).toHaveValue(primaryText);
  await expect(
    page.getByRole("button", { name: /^Remove representation/ }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Version label", exact: true }),
  ).toHaveValue("");
  expect(writes).toEqual([]);
});

test("malformed Skill archives retain validation findings without executing uploaded content", async ({
  page,
  catalog,
}) => {
  const resource = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: catalog.receipt.namespaceId,
      kind: "skill",
      canonicalIdentifier: "urn:air:catalog.example.com:demo:malformed-skill",
    },
  );
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${resource.data.id}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("1.0.0");
  await page
    .getByRole("combobox", { name: "Content source", exact: true })
    .selectOption("package");
  await page.getByLabel("Skill archive", { exact: true }).setInputFiles({
    name: "invalid.tgz",
    mimeType: "application/gzip",
    buffer: Buffer.from("not an archive"),
  });
  await page
    .getByRole("button", {
      name: "Edit immutable handoff as JSON",
      exact: true,
    })
    .click();
  await page
    .getByRole("textbox", { name: "Immutable handoff JSON", exact: true })
    .fill(
      JSON.stringify({
        git: {
          repository: "https://github.com/example/skills",
          commit: "a".repeat(40),
          directory: "skill",
        },
      }),
    );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Draft saved, corrections needed.",
  );
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Inspect current revision and validation" })
    .click();
  await page.reload();
  await page
    .getByText("Validation findings and evidence", { exact: true })
    .click();
  await expect(page.locator("pre").filter({ visible: true })).toContainText(
    "SKILL_PACKAGE_ARCHIVE_INVALID",
  );
  await expect(
    page.getByRole("button", { name: "Read retained document", exact: true }),
  ).toHaveCount(0);
});
