// Real UI/API journeys validate generated YAML through the pinned backend parser.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import { object, array, text } from "./json";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";
import { doubleText } from "./text-layout";
for (const base of ["root", "subpath"] as const) {
  test(`${base}: author a Skill from fields with optional YAML and no external traffic`, async ({
    page,
    catalog,
  }, testInfo) => {
    if (base === "subpath") await page.emulateMedia({ colorScheme: "dark" });
    await login(page, catalog, "local-contributor", "/resources/new");
    const outside: string[] = [];
    const writes: { path: string; body: unknown }[] = [];
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
        writes.push({ path: url.pathname, body: request.postDataJSON() });
    });
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("combobox", { name: "Kind", exact: true })
      .selectOption("skill");
    await page
      .getByRole("textbox", {
        name: "Canonical resource identifier",
        exact: true,
      })
      .fill(`urn:air:catalog.example.com:guided:skill-fields-${base}`);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Version label", exact: true })
      .fill("release Q4");
    await expect(
      page.getByRole("combobox", { name: "Content source", exact: true }),
    ).toHaveValue("content");
    await page
      .getByRole("textbox", { name: "Skill name", exact: true })
      .fill("review-notes");
    const description =
      'Review a change: "before" and "after". 日本語 😀\nUse when a reviewer needs notes.\u0085Keep Unicode. Show \\uD800 literally.';
    await page
      .getByRole("textbox", { name: "Skill description", exact: true })
      .fill(description);
    const body = "# Review notes\n\nRead the evidence.\nList the checks.\n";
    await page
      .getByRole("textbox", {
        name: "Skill instructions (Markdown)",
        exact: true,
      })
      .fill(body);
    await openDisclosure(page, "Optional Skill fields");
    await page
      .getByRole("textbox", { name: "Skill license (optional)", exact: true })
      .fill("Apache-2.0");
    await page
      .getByRole("textbox", {
        name: "Skill compatibility (optional)",
        exact: true,
      })
      .fill("Requires git.");
    await page
      .getByRole("textbox", {
        name: "Skill allowed tools (optional)",
        exact: true,
      })
      .fill("Read Bash(git:*)");
    await page
      .getByRole("button", { name: "Add Skill metadata", exact: true })
      .press("Enter");
    await expect(
      page.getByRole("textbox", { name: "Skill metadata 1 key", exact: true }),
    ).toBeFocused();
    await page
      .getByRole("textbox", { name: "Skill metadata 1 key", exact: true })
      .fill("version");
    await page
      .getByRole("textbox", { name: "Skill metadata 1 value", exact: true })
      .fill("1.0");
    // Clearing compatibility omits it instead of generating an invalid empty scalar.
    await page
      .getByRole("textbox", {
        name: "Skill compatibility (optional)",
        exact: true,
      })
      .fill("");
    const expected =
      [
        "---",
        'name: "review-notes"',
        String.raw`description: "Review a change: \"before\" and \"after\". 日本語 😀\nUse when a reviewer needs notes.\u0085Keep Unicode. Show \\uD800 literally."`,
        'license: "Apache-2.0"',
        'allowed-tools: "Read Bash(git:*)"',
        "metadata:",
        '  "version": "1.0"',
        "---",
        "",
      ].join("\n") + body;
    await openDisclosure(page, "Advanced SKILL.md");
    await expect(
      page.getByRole("textbox", { name: "Text content", exact: true }),
    ).toHaveValue(expected);
    await page.getByText("Advanced SKILL.md", { exact: true }).click();
    await expect(
      page.getByRole("textbox", { name: "Skill name", exact: true }),
    ).toHaveValue("review-notes");
    await expect(
      page
        .getByRole("region", { name: "Native input guidance" })
        .getByRole("link"),
    ).toHaveAttribute("href", /^https:\/\/fixture\.invalid\//u);
    expect(writes).toEqual([]);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    if (base === "root") {
      const optional = page.getByText("Optional Skill fields", { exact: true });
      if ((await optional.locator("..").getAttribute("open")) !== null)
        await optional.click();
      await page
        .getByRole("textbox", { name: "Skill description", exact: true })
        .fill("Prepare short notes for a change review.");
      await page
        .getByRole("heading", { name: "Authoring", exact: true })
        .focus();
      await page.evaluate(() => window.scrollTo(0, 0));
      await testInfo.attach("skill-field-authoring", {
        body: await page.screenshot({
          path: testInfo.outputPath("skill-field-authoring.png"),
          fullPage: true,
        }),
        contentType: "image/png",
      });
      await page
        .getByRole("textbox", { name: "Skill description", exact: true })
        .fill(description);
      await page.setViewportSize({ width: 390, height: 844 });
      await doubleText(page);
      await expect(
        page.getByRole("textbox", {
          name: "Skill instructions (Markdown)",
          exact: true,
        }),
      ).toBeVisible();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
    const saved = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        /\/resources\/[^/]+\/versions$/u.test(new URL(response.url()).pathname),
    );
    await page
      .getByRole("button", { name: "Save content draft", exact: true })
      .click();
    await confirmCommand(page);
    const response = await saved;
    expect(response.status()).toBe(201);
    const bundle = object(await response.json());
    expect(object(bundle.evidence).state).toBe("complete");
    const revision = object(bundle.revision);
    const stored = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${text(revision.id)}`,
    );
    const representation = object(
      required(array(object(stored.data.revision).representations)[0]),
    );
    expect(representation).toMatchObject({
      role: "primary",
      mediaType: "application/agent-skills+md",
      validationState: "valid",
    });
    expect(object(representation.normalizedFacts)).toEqual({
      name: "review-notes",
      description,
    });
    const artifactId = text(representation.artifactId);
    const artifactResponse = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${artifactId}/content`,
      { headers: { "X-Catalog-Request": "1" } },
    );
    expect(artifactResponse.status()).toBe(200);
    expect(await artifactResponse.text()).toBe(expected);
    expect(writes).toHaveLength(2);
    expect(outside).toEqual([]);
  });
}
