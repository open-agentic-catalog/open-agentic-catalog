import { agentDocument as agent } from "./published-fixture";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { object, text } from "./json";
import { openDisclosure } from "./editor-disclosure";

const skill =
  "---\nname: browser-skill\ndescription: Inspect synthetic browser test evidence.\nlicense: Apache-2.0\n---\n\n# Instructions\n\nRead the supplied evidence. Do not execute anything.\n";

// Each case owns a fresh Catalog. The writes below go through the actual UI,
// host, API and worker; seed-demo supplies only the namespace and Publisher.
for (const kind of ["agent", "mcp_server", "skill"] as const) {
  test(`${kind} authoring preserves immutable revisions and submits a listing`, async ({
    page,
    catalog,
  }) => {
    const original =
      kind === "agent"
        ? JSON.stringify({
            ...agent,
            description:
              "<svg data-ui-injection onload=\"throw new Error('rendered-untrusted-metadata')\"></svg>",
          })
        : kind === "mcp_server"
          ? JSON.stringify({
              name: "io.example/browser-server",
              description: "Inspect synthetic browser evidence.",
              version: "1.0.0",
              remotes: [
                {
                  type: "streamable-http",
                  url: "https://mcp.example.com/browser",
                },
              ],
            })
          : skill;
    const identifier = `urn:air:catalog.example.com:demo:browser-${kind.replaceAll("_", "-")}`;
    await login(page, catalog, "local-contributor", "/resources/new");
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("textbox", { name: "Canonical resource identifier" })
      .fill(identifier);
    await page
      .getByRole("combobox", { name: "Kind", exact: true })
      .selectOption(kind);
    await page
      .getByRole("button", { name: "Create resource", exact: true })
      .click();
    await confirmCommand(page);
    await page.getByRole("link", { name: "Open created resource" }).click();
    const resourceURL = page.url();
    await expect(
      page.getByRole("heading", { name: identifier, exact: true }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Add version", exact: true }).click();
    if (kind === "agent")
      await openDisclosure(page, "Advanced Agent Card JSON");
    if (kind === "mcp_server") {
      await page
        .getByRole("combobox", { name: "MCP document", exact: true })
        .selectOption("registry");
      await openDisclosure(page, "Advanced MCP JSON");
    }
    if (kind === "skill") await openDisclosure(page, "Advanced SKILL.md");
    await page.getByRole("textbox", { name: "Version label" }).fill("1.0.0");
    const document = page.getByRole("textbox", {
      name: kind === "skill" ? "Text content" : "Standards document (JSON)",
      exact: true,
    });
    if (kind === "agent") {
      await document.fill('{"name":"one","name":"two"}');
      await page
        .getByRole("button", { name: "Create version", exact: true })
        .click();
      await expect(page.getByRole("alert")).toContainText(
        "Duplicate JSON object keys",
      );
      await expect(page.getByRole("alert")).toBeFocused();
      await page
        .getByRole("link", {
          name: "Duplicate JSON object keys are not allowed.",
        })
        .click();
      await expect(document).toBeFocused();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(
        page.getByRole("textbox", { name: "Version label" }),
      ).toHaveValue("1.0.0");
    }
    await document.fill(original);
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(
      "Draft saved. Version 1.0.0.",
    );
    await page.getByRole("link", { name: "Open saved version" }).click();
    const versionURL = page.url();
    await page
      .getByRole("link", { name: "Inspect current revision and validation" })
      .click();
    const firstRevisionURL = page.url();
    await page.getByRole("button", { name: "Read retained document" }).click();
    const retained = page.locator("pre").filter({ visible: true });
    if (kind === "skill") await expect(retained).toHaveText(original);
    else
      expect(JSON.parse(await retained.innerText())).toEqual(
        JSON.parse(original),
      );
    await expect(page.locator("[data-ui-injection]")).toHaveCount(0);
    const viewer = retained.locator("..");
    const initialText = await retained.textContent();
    const format = viewer.getByRole("button", {
      name: "Format JSON",
      exact: true,
    });
    if (kind === "skill") {
      await expect(format).toHaveCount(0);
      await expect(retained).toHaveAttribute("aria-label", "Text document");
    } else {
      // Inline API objects start indented; artifact text uses original bytes.
      await expect(format).toHaveAttribute("aria-pressed", "true");
      await expect(retained.locator(".syntax-key").first()).toBeVisible();
      await format.focus();
      await page.keyboard.press("Enter");
      await expect(format).toHaveAttribute("aria-pressed", "false");
      expect(await retained.textContent()).toBe(
        JSON.stringify(JSON.parse(initialText!)),
      );
      await page.keyboard.press("Enter");
      await expect(format).toHaveAttribute("aria-pressed", "true");
      expect(await retained.textContent()).toBe(initialText);
    }
    const wrap = viewer.getByRole("button", {
      name: "Wrap lines",
      exact: true,
    });
    await wrap.click();
    await expect(wrap).toHaveAttribute("aria-pressed", "false");
    await expect(retained).toHaveAttribute("data-wrap", "false");
    expect(await retained.textContent()).toBe(initialText);
    await wrap.click();
    await expect(retained).toHaveAttribute("data-wrap", "true");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page
      .getByRole("button", { name: "Revalidate revision", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(
      "Validation: complete.",
    );
    await page.goto(versionURL);
    await page
      .getByRole("button", { name: "New revision", exact: true })
      .click();
    if (kind === "agent")
      await openDisclosure(page, "Advanced Agent Card JSON");
    if (kind === "mcp_server") {
      await page
        .getByRole("combobox", { name: "MCP document", exact: true })
        .selectOption("registry");
      await openDisclosure(page, "Advanced MCP JSON");
    }
    if (kind === "skill") await openDisclosure(page, "Advanced SKILL.md");
    await expect(
      page.getByRole("textbox", { name: "Version label" }),
    ).toHaveAttribute("readonly", "");
    const updated =
      kind === "skill"
        ? original + "\nSecond revision instructions.\n"
        : JSON.stringify({
            ...JSON.parse(original),
            description: "Second revision description.",
          });
    await document.fill(updated);
    await page
      .getByRole("button", { name: "Create version revision", exact: true })
      .click();
    const updatedReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "POST" &&
        new URL(reply.url()).pathname.endsWith("/revisions") &&
        reply.url().includes("/resource-versions/"),
    );
    await confirmCommand(page);
    const savedVersion = object(
      object(await (await updatedReply).json()).version,
    );
    await expect(
      page.getByRole("link", {
        name: "Create new Version from this revision",
        exact: true,
      }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/versions/new?resource=${text(savedVersion.resourceId)}&sourceRevision=${text(savedVersion.candidateRevisionId)}`,
    );
    await page.getByRole("link", { name: "Open saved version" }).click();
    await expect(
      page.getByRole("link", { name: "Revision 1", exact: true }),
    ).toHaveAttribute("href", new URL(firstRevisionURL).pathname);
    await expect(
      page.getByRole("link", { name: "Revision 2", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("link", { name: "Inspect current revision and validation" })
      .click();
    const revisionID = new URL(page.url()).pathname.split("/").at(-1)!;
    await page.getByRole("button", { name: "Read retained document" }).click();
    await expect(retained).toContainText("Second revision");
    await page.goto(firstRevisionURL);
    await page.getByRole("button", { name: "Read retained document" }).click();
    if (kind === "skill") await expect(retained).toHaveText(original);
    else
      expect(JSON.parse(await retained.innerText())).toEqual(
        JSON.parse(original),
      );

    await page.goto(resourceURL);
    await page.getByRole("button", { name: "Listings", exact: true }).click();
    await page
      .getByRole("button", { name: "Create listing", exact: true })
      .click();
    await confirmCommand(page);
    await page
      .getByRole("link", { name: "Open existing listing", exact: true })
      .click();
    await page.getByRole("button", { name: "New listing revision" }).click();
    await page
      .getByRole("combobox", { name: "Resource version", exact: true })
      .selectOption({ label: "1.0.0" });
    await page
      .getByRole("combobox", { name: "Version revision", exact: true })
      .selectOption(revisionID);
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill(`Browser ${kind}`);
    await page
      .getByRole("textbox", { name: "Description", exact: true })
      .fill("Synthetic browser authoring test.");
    await page
      .getByRole("combobox", { name: "Publisher", exact: true })
      .selectOption(catalog.receipt.publisherId);
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    await confirmCommand(page);
    await expect(
      page.getByRole("status").filter({ hasText: "Listing revision saved." }),
    ).toBeFocused();
    await expect(
      page.getByRole("link", {
        name: "Inspect candidate revision",
        exact: true,
      }),
    ).toHaveAttribute("href", /\/listing-revisions\/[0-9a-f-]+$/);
    await page
      .getByRole("button", { name: "Submit for review", exact: true })
      .click();
    await confirmCommand(page);
    await page.getByRole("link", { name: "Open review submission" }).click();
    await expect(
      page.getByRole("heading", {
        name: "Awaiting independent review",
        exact: true,
      }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Inspect submitted evidence" })
      .click();
    await expect(
      page.getByText("Use a different sign-in identity from the submitter.", {
        exact: false,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Record review decision", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("combobox", { name: "Decision", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByText(
        "The review.decide permission is required in this namespace.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Reviewer decisions", exact: true }),
    ).toContainText("No reviewer decision recorded.");
    if (kind === "agent") {
      const reviewURL = page.url();
      const submissionId = new URL(reviewURL).pathname.split("/").at(-1)!;
      const submitted = await catalog.admin<{
        listingRevisionId: string;
        versionRevisionId: string;
        submittedBy: string;
      }>("GET", `/api/v1/review-submissions/${submissionId}`);
      expect(submitted.data.versionRevisionId).toBe(revisionID);
      await signOut(page);
      await login(page, catalog, "local-reviewer", `/reviews/${submissionId}`);
      await page
        .getByRole("button", {
          name: "Inspect submitted evidence",
          exact: true,
        })
        .click();
      await page
        .getByRole("textbox", { name: "Reason", exact: true })
        .fill("Independently inspect the submitted Agent revision.");
      await page
        .getByRole("button", { name: "Record review decision", exact: true })
        .click();
      await confirmCommand(page);
      await expect(
        page.getByRole("status").filter({ hasText: /^Review approved\./ }),
      ).toBeVisible();
      const approved = await catalog.admin<{
        state: string;
        listingRevisionId: string;
        versionRevisionId: string;
        submittedBy: string;
      }>("GET", `/api/v1/review-submissions/${submissionId}`);
      expect(approved.data.state).toBe("approved");
      expect(approved.data.listingRevisionId).toBe(
        submitted.data.listingRevisionId,
      );
      expect(approved.data.versionRevisionId).toBe(revisionID);
      expect(approved.data.submittedBy).toBe(submitted.data.submittedBy);
    }
  });
}
