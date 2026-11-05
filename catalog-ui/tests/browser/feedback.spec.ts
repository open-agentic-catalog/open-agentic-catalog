import { revealControl } from "./editor-disclosure";
import { required } from "../required";
// Independent reader and moderator journeys use the owned Catalog, not response mocks.
import {
  test,
  expect,
  login,
  confirmCommand,
  signOut,
  openAccount,
} from "./fixtures";
import AxeBuilder from "@axe-core/playwright";
import { expectNaturalText } from "./text-layout";

test("private item feedback survives publication context and supports minimal moderator triage", async ({
  page,
  catalog,
  context,
}) => {
  const item = required(catalog.receipt.items[0]);
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  await page.getByText("Item feedback", { exact: true }).click();
  await expect(
    page.getByText("New feedback refers to this exact release.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByText(/Do not include credentials/)).toBeVisible();
  await page
    .getByRole("combobox", { name: "Category", exact: true })
    .selectOption("documentation");
  await page
    .getByRole("textbox", { name: "Feedback comment", exact: true })
    .fill("Please clarify this example. <script>not executable</script>");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.getByText("Item feedback", { exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "Feedback comment", exact: true }),
  ).toHaveValue("Please clarify this example. <script>not executable</script>");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Refresh feedback", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Feedback comment", exact: true }),
  ).toHaveValue("Please clarify this example. <script>not executable</script>");
  await page
    .getByRole("button", { name: "Submit feedback", exact: true })
    .click();
  await confirmCommand(page);
  await expect(
    page
      .getByText(
        "Please clarify this example. <script>not executable</script>",
        { exact: true },
      )
      .first(),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Feedback inbox", exact: true }),
  ).toHaveCount(0);
  await signOut(page);
  const memberships = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const moderator = required(
    memberships.data.items.find((member) => member.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Private feedback moderator only",
      permissions: ["feedback.moderate"],
      reason: "Independently qualify moderation without resource reads",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: moderator.id,
    roleDefinitionId: role.data.id,
    reason: "Minimal moderator browser qualification",
  });
  await catalog.admin("POST", "/api/v1/feedback", {
    resourceId: item.resourceId,
    category: "suggestion",
    comment: "Independent second feedback row",
  });
  const resourceReads: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/_catalog/api/v1/resources")
      resourceReads.push(request.url());
  });
  await login(page, catalog, "local-no-role", "/feedback");
  await revealControl(page.getByLabel("Resource ID filter (optional)"));
  await expect(page.getByLabel("Resource ID filter (optional)")).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Resource and versions", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Original release", exact: true }),
  ).toHaveCount(0);
  const firstRow = page.locator("article").filter({
    has: page.getByText(
      "Please clarify this example. <script>not executable</script>",
      { exact: true },
    ),
  });
  const secondRow = page.locator("article").filter({
    has: page.getByText("Independent second feedback row", { exact: true }),
  });
  const caseDisclosure = firstRow.locator("xpath=ancestor::details[1]");
  const caseSummary = caseDisclosure.locator(":scope > summary");
  await revealControl(caseSummary.locator("time"));
  await expect(caseDisclosure).toHaveJSProperty("open", false);
  await caseSummary.click();
  const references = firstRow.getByText("Identifiers and revision", {
    exact: true,
  });
  await revealControl(references);
  await expect(references).toBeVisible();
  await expect(references.locator("..")).toHaveJSProperty("open", false);
  await references.click();
  await expect(references.locator("..")).toHaveJSProperty("open", true);
  await revealControl(
    secondRow.getByText("Respond or manage feedback", { exact: true }),
  );
  await expect(
    firstRow.getByRole("button", { name: "Copy feedback ID", exact: true }),
  ).toBeVisible();
  await expect(
    firstRow.getByRole("button", {
      name: "Copy feedback revision",
      exact: true,
    }),
  ).toBeVisible();
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await firstRow
    .getByRole("button", { name: "Copy feedback ID", exact: true })
    .click();
  await expect(firstRow.getByText("Copied.", { exact: true })).toBeVisible();
  const copiedID = await page.evaluate(() => navigator.clipboard.readText());
  expect(copiedID).toMatch(/^[0-9a-f-]{36}$/);
  await expect(
    firstRow.locator("code").filter({ hasText: copiedID }),
  ).toHaveCount(1);
  await revealControl(
    secondRow.getByText("Respond or manage feedback", { exact: true }),
  );
  await secondRow
    .getByText("Respond or manage feedback", { exact: true })
    .click();
  await secondRow
    .getByRole("textbox", {
      name: "Moderator response (optional)",
      exact: true,
    })
    .fill("Keep second response draft");
  await revealControl(
    firstRow.getByText("Respond or manage feedback", { exact: true }),
  );
  await firstRow
    .getByText("Respond or manage feedback", { exact: true })
    .click();
  await firstRow
    .getByRole("textbox", {
      name: "Moderator response (optional)",
      exact: true,
    })
    .fill("The example is now clearer.");
  await firstRow
    .getByRole("button", { name: "Address feedback", exact: true })
    .click();
  await confirmCommand(page);
  await expect(
    page.getByText("The example is now clearer.", { exact: true }).first(),
  ).toBeVisible();
  await expect(
    secondRow.getByRole("textbox", {
      name: "Moderator response (optional)",
      exact: true,
    }),
  ).toHaveValue("Keep second response draft");
  page.once("dialog", (dialog) => dialog.dismiss());
  await revealControl(
    secondRow.getByText("Respond or manage feedback", { exact: true }),
  );
  await secondRow
    .getByText("Respond or manage feedback", { exact: true })
    .click();
  await expect(
    secondRow.getByRole("textbox", {
      name: "Moderator response (optional)",
      exact: true,
    }),
  ).toHaveValue("Keep second response draft");
  page.once("dialog", (dialog) => dialog.accept());
  await revealControl(
    secondRow.getByText("Respond or manage feedback", { exact: true }),
  );
  await secondRow
    .getByText("Respond or manage feedback", { exact: true })
    .click();
  await revealControl(
    secondRow.getByText("Respond or manage feedback", { exact: true }),
  );
  await secondRow
    .getByText("Respond or manage feedback", { exact: true })
    .click();
  await expect(
    secondRow.getByRole("textbox", {
      name: "Moderator response (optional)",
      exact: true,
    }),
  ).toHaveValue("");
  expect(resourceReads).toEqual([]);
  await signOut(page);
  const release = await catalog.admin(
    "GET",
    "/api/v1/listing-releases/" + item.releaseId,
  );
  await catalog.admin(
    "POST",
    "/api/v1/listing-releases/" + item.releaseId + ":retire",
    { reason: "Owned feedback retirement test" },
    release.etag,
  );
  await login(page, catalog, "local-reader", `/resources/${item.resourceId}`);
  await page.getByText("Item feedback", { exact: true }).click();
  await expect(
    page.getByText("The example is now clearer.", { exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await revealControl(
    page.getByText("Edit or withdraw your feedback", { exact: true }),
  );
  await page
    .getByText("Edit or withdraw your feedback", { exact: true })
    .click();
  await page
    .getByRole("button", { name: "Withdraw feedback", exact: true })
    .click();
  await confirmCommand(page);
  await revealControl(
    page.getByText("Comment and response have been cleared.").first(),
  );
  await expect(
    page.getByText("Comment and response have been cleared."),
  ).toBeVisible();
  await expect(
    page.getByText("The example is now clearer.", { exact: true }),
  ).toHaveCount(0);
});

test("stale feedback edits preserve an administrator response and recover on refresh", async ({
  page,
  catalog,
  network,
}) => {
  const resourceId = required(catalog.receipt.items[0]).resourceId;
  await login(page, catalog, "local-reader", `/resources/${resourceId}`);
  await page.getByText("Item feedback", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Feedback comment", exact: true })
    .fill("Original private report");
  await page
    .getByRole("button", { name: "Submit feedback", exact: true })
    .click();
  await confirmCommand(page);
  await revealControl(
    page.getByText("Edit or withdraw your feedback", { exact: true }),
  );
  await page
    .getByText("Edit or withdraw your feedback", { exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Feedback comment", exact: true })
    .last()
    .fill("A stale draft");
  const inbox = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    `/api/v1/feedback/inbox?resourceId=${resourceId}`,
  );
  const path = "/api/v1/feedback/" + required(inbox.data.items[0]).id;
  const current = await catalog.admin("GET", path);
  await catalog.admin(
    "POST",
    path + ":address",
    { response: "New administrator response" },
    current.etag,
  );
  network.expectFailure("PATCH", "/_catalog" + path, 412);
  await page
    .getByRole("button", { name: "Save feedback changes", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
    "PRECONDITION_FAILED",
  );
  await page.getByRole("button", { name: "Back to form", exact: true }).click();
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Refresh feedback", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Feedback comment", exact: true }).last(),
  ).toHaveValue("A stale draft");
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Refresh feedback", exact: true })
    .click();
  await expect(
    page.getByText("New administrator response", { exact: true }),
  ).toBeVisible();
  await revealControl(
    page.getByText("Edit or withdraw your feedback", { exact: true }),
  );
  await page
    .getByText("Edit or withdraw your feedback", { exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Feedback comment", exact: true }).last(),
  ).toHaveValue("Original private report");
  await page.setViewportSize({ width: 390, height: 844 });
  const accessibility = await new AxeBuilder({ page })
    .include('[aria-label="Your item feedback"]')
    .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
  expect(accessibility.incomplete).toEqual([]);
  await signOut(page);
  await expect(
    page.getByText("New administrator response", { exact: true }),
  ).toHaveCount(0);
});

test("editing addressed feedback and administrator reopen remove preserve state and clear private text", async ({
  page,
  catalog,
}) => {
  const resourceId = required(catalog.receipt.items[0]).resourceId;
  const created = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/feedback",
    {
      resourceId,
      category: "documentation",
      comment: "Original owned feedback",
    },
  );
  const path = `/api/v1/feedback/${created.data.id}`;
  await catalog.admin(
    "POST",
    path + ":address",
    { response: "Previous administrator response" },
    created.etag,
  );
  await login(page, catalog, "local-admin", "/feedback");
  await page
    .getByRole("combobox", { name: "Resource filter", exact: true })
    .selectOption(resourceId);
  await page
    .getByRole("combobox", { name: "Feedback category", exact: true })
    .selectOption("documentation");
  await page
    .getByRole("combobox", { name: "Feedback state", exact: true })
    .selectOption("addressed");
  await revealControl(
    page.getByText("Edit or withdraw your feedback", { exact: true }),
  );
  await page
    .getByText("Edit or withdraw your feedback", { exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Feedback comment", exact: true })
    .fill("Corrected owned feedback");
  await page
    .getByRole("button", { name: "Save feedback changes", exact: true })
    .click();
  await confirmCommand(page);
  await expect(
    page.getByText("No feedback matches these filters.", { exact: true }),
  ).toBeVisible();
  const edited = await catalog.admin<{
    state: string;
    comment: string;
    response?: string;
  }>("GET", path);
  expect(edited.data).toMatchObject({
    state: "open",
    comment: "Corrected owned feedback",
  });
  expect(edited.data.response || "").toBe("");
  await page
    .getByRole("combobox", { name: "Feedback state", exact: true })
    .selectOption("");
  await revealControl(
    page.getByText("Respond or manage feedback", { exact: true }),
  );
  await page.getByText("Respond or manage feedback", { exact: true }).click();
  await page
    .getByRole("button", { name: "Address feedback", exact: true })
    .click();
  await confirmCommand(page);
  await revealControl(
    page.getByText("Respond or manage feedback", { exact: true }),
  );
  await page.getByText("Respond or manage feedback", { exact: true }).click();
  await page
    .getByRole("button", { name: "Reopen feedback", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "clear its current moderator response",
  );
  await confirmCommand(page);
  await expect(page.locator("article .workspace-status")).toHaveText("Open");
  await expect(page.locator("article .workspace-status")).toHaveAttribute(
    "data-tone",
    "info",
  );
  await revealControl(
    page.getByText("Respond or manage feedback", { exact: true }),
  );
  await page.getByText("Respond or manage feedback", { exact: true }).click();
  await page
    .getByRole("button", { name: "Remove feedback", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "This feedback cannot be reopened",
  );
  await expect(page.getByRole("dialog")).toContainText(
    "Its identifying context and audit records remain",
  );
  await confirmCommand(page);
  await revealControl(
    page.getByText("Comment and response have been cleared.").first(),
  );
  await expect(
    page.getByText("Comment and response have been cleared.", { exact: true }),
  ).toBeVisible();
  const removed = await catalog.admin<{
    state: string;
    comment?: string;
    response?: string;
  }>("GET", path);
  expect(removed.data.state).toBe("removed");
  expect(removed.data.comment || "").toBe("");
  expect(removed.data.response || "").toBe("");
  await page.reload();
  await expect(
    page.getByText("Corrected owned feedback", { exact: true }),
  ).toHaveCount(0);
});

test("own-only feedback remains reachable and withdrawable after resource-read revocation", async ({
  page,
  catalog,
}) => {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const membership = required(
    members.data.items.find((item) => item.subject === "local-no-role"),
  );
  const ownRole = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Own feedback only",
      permissions: ["feedback.own"],
      reason: "Private feedback regression",
    },
  );
  const readRole = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Temporary resource reader",
      permissions: ["resource.read", "namespace.read"],
      reason: "Readable creation context",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: membership.id,
    roleDefinitionId: ownRole.data.id,
    reason: "Keep own submissions available",
  });
  const grant = await catalog.admin<{ id: string; revision: number }>(
    "POST",
    "/api/v1/access/assignments",
    {
      membershipId: membership.id,
      roleDefinitionId: readRole.data.id,
      reason: "Temporary resource context",
    },
  );
  const resource = required(catalog.receipt.items[0]);
  await login(
    page,
    catalog,
    "local-no-role",
    `/resources/${resource.resourceId}`,
  );
  await page.getByText("Item feedback", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Feedback comment", exact: true })
    .fill("Retained own report after resource revocation");
  await page
    .getByRole("button", { name: "Submit feedback", exact: true })
    .click();
  await confirmCommand(page);
  const current = await catalog.admin<{
    items: { id: string; state: string; revision: number; grantedAt: string }[];
  }>(
    "GET",
    `/api/v1/access/assignments?roleDefinitionId=${readRole.data.id}&limit=25`,
  );
  expect(current.data.items).toHaveLength(1);
  expect(current.data.items[0]).toMatchObject({
    id: grant.data.id,
    state: "active",
    revision: grant.data.revision,
  });
  await catalog.admin(
    "POST",
    `/api/v1/access/assignments/${grant.data.id}:revoke`,
    { reason: "Revoke resource context without withdrawing own feedback" },
    `"rev-${grant.data.revision}"`,
  );
  const resourceRequests: string[] = [];
  const feedbackRequests: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path === "/_catalog/api/v1/resources")
      resourceRequests.push(request.url());
    if (path.startsWith("/_catalog/api/v1/feedback"))
      feedbackRequests.push(request.url());
  });
  await page.goto(catalog.origin + "/my-feedback");
  await expect(
    page.getByRole("heading", { name: "My feedback", exact: true }),
  ).toBeVisible();
  await revealControl(
    page
      .getByText("Retained own report after resource revocation", {
        exact: true,
      })
      .first(),
  );
  await expect(
    page
      .getByText("Retained own report after resource revocation", {
        exact: true,
      })
      .first(),
  ).toBeVisible();
  await openAccount(page);
  await expect(
    page.getByRole("link", { name: "My feedback", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Feedback inbox", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Resource and versions", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Submit feedback", exact: true }),
  ).toHaveCount(0);
  expect(resourceRequests).toEqual([]);
  const submissions = page.getByRole("region", {
    name: "Your submissions",
    exact: true,
  });
  await expect(
    submissions.getByRole("heading", { name: "Your submissions", exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/Do not include credentials/)).toBeVisible();
  await expect(
    submissions.getByText(/To send feedback, open a resource/),
  ).toBeVisible();
  await expect(
    submissions.getByText(/existing submissions remain available/),
  ).toBeVisible();
  await expect(
    submissions.getByText(/New feedback (is about|refers)/),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 1440, height: 900 });
  await expectNaturalText(
    page.locator(".page-heading p"),
    page.locator(".page-heading"),
    1,
  );
  const contentWidth = await submissions.evaluate((node) => {
    const style = getComputedStyle(node);
    return (
      node.clientWidth -
      parseFloat(style.paddingLeft) -
      parseFloat(style.paddingRight)
    );
  });
  const guidance = submissions.getByText(/To send feedback, open a resource/);
  const paragraph = await guidance.boundingBox();
  expect(paragraph!.width).toBeGreaterThanOrEqual(contentWidth - 2);
  expect(
    await guidance.evaluate((node) => getComputedStyle(node).whiteSpace),
  ).toBe("normal");
  await expect(guidance.locator("br")).toHaveCount(0);
  const help = submissions
    .locator("details")
    .filter({ has: page.getByText("Feedback access", { exact: true }) });
  await expect(help).toHaveJSProperty("open", false);
  const reads = feedbackRequests.length;
  await help.locator("summary").focus();
  await page.keyboard.press("Enter");
  await expect(help).toHaveJSProperty("open", true);
  await expect(
    help.getByText(/Sending or editing feedback also requires/),
  ).toBeVisible();
  expect(feedbackRequests).toHaveLength(reads);
  expect(resourceRequests).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  const accessibility = await new AxeBuilder({ page })
    .include('[aria-label="Your submissions"]')
    .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
  expect(accessibility.incomplete).toEqual([]);
  await revealControl(
    page.getByText("Edit or withdraw your feedback", { exact: true }),
  );
  await page
    .getByText("Edit or withdraw your feedback", { exact: true })
    .click();
  await page
    .getByRole("button", { name: "Withdraw feedback", exact: true })
    .click();
  await confirmCommand(page);
  await revealControl(
    page.getByText("Comment and response have been cleared.").first(),
  );
  await expect(
    page.getByText("Comment and response have been cleared."),
  ).toBeVisible();
});
