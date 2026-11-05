// Plugin preparation stays local. The API still inspects an explicit HTTPS archive.
import AxeBuilder from "@axe-core/playwright";
import { readFile, writeFile } from "node:fs/promises";
import { test, expect, login } from "./fixtures";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";
import { doubleText } from "./text-layout";

for (const base of ["root", "subpath"] as const) {
  test(`${base}: prepare Plugin files from fields without writes or endpoint calls`, async ({
    page,
    catalog,
  }, testInfo) => {
    await login(page, catalog, "local-contributor", "/resources/new");
    const writes: string[] = [];
    const outside: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (
        ["http:", "https:"].includes(url.protocol) &&
        url.origin !== catalog.origin
      )
        outside.push(url.href);
      if (
        request.method() === "POST" &&
        url.pathname.includes("/_catalog/api/v1/")
      )
        writes.push(url.pathname);
    });
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("combobox", { name: "Kind", exact: true })
      .selectOption("plugin");
    await page
      .getByRole("textbox", {
        name: "Canonical resource identifier",
        exact: true,
      })
      .fill(`urn:air:catalog.example.com:guided:plugin-fields-${base}`);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Version label", exact: true })
      .fill("1.0.0");
    const input = page.getByRole("combobox", {
      name: "Plugin input",
      exact: true,
    });
    await input.selectOption("prepare");
    await page
      .getByRole("textbox", { name: "Plugin name", exact: true })
      .fill("review-tools");
    await page
      .getByRole("button", {
        name: "Use Catalog version 1.0.0 in manifest",
        exact: true,
      })
      .click();
    await page
      .getByRole("textbox", {
        name: "Plugin description (optional)",
        exact: true,
      })
      .fill("Prepare a release review.");
    await page
      .getByRole("textbox", { name: "Plugin license (optional)", exact: true })
      .fill("Apache-2.0");
    await page
      .getByRole("button", {
        name: "Preview and download Plugin files",
        exact: true,
      })
      .click();
    const files = page.getByRole("region", { name: "Prepared Plugin files" });
    await expect(files.locator("summary")).toHaveText(["plugin.json"]);
    await openDisclosure(page, "plugin.json");
    const downloaded = page.waitForEvent("download");
    await files
      .getByRole("link", { name: "Download plugin.json", exact: true })
      .click();
    const download = await downloaded;
    expect(download.suggestedFilename()).toBe("plugin.json");
    expect(
      JSON.parse(await readFile(required(await download.path()), "utf8")),
    ).toEqual({
      $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
      name: "review-tools",
      version: "1.0.0",
      description: "Prepare a release review.",
      license: "Apache-2.0",
    });
    await page
      .getByRole("button", { name: "Hide prepared files", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Add Skill component", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Skill name", exact: true })
      .fill("review-notes");
    await page
      .getByRole("textbox", { name: "Skill description", exact: true })
      .fill("Prepare release review notes.");
    await page
      .getByRole("textbox", {
        name: "Skill instructions (Markdown)",
        exact: true,
      })
      .fill("# Review\n\nRead the release notes.");
    await page
      .getByRole("button", { name: "Add MCP server", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Server 1 name", exact: true })
      .fill("documentation");
    await page
      .getByRole("textbox", { name: "Command", exact: true })
      .fill("./bin/docs");
    await page
      .getByRole("button", { name: "Add argument", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Argument 1", exact: true })
      .fill("two words");
    await page
      .getByRole("button", { name: "Add argument", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Argument 2", exact: true })
      .fill("${PLUGIN_ROOT}/index");
    await page
      .getByRole("button", { name: "Edit Plugin MCP JSON", exact: true })
      .click();
    const mcp = page.getByRole("textbox", {
      name: "Plugin MCP JSON",
      exact: true,
    });
    expect(JSON.parse(await mcp.inputValue())).toEqual({
      $schema: "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
      mcpServers: {
        documentation: {
          type: "stdio",
          command: "./bin/docs",
          args: ["two words", "${PLUGIN_ROOT}/index"],
        },
      },
    });
    await mcp.fill("{invalid");
    await page
      .getByRole("button", { name: "Use Plugin MCP fields", exact: true })
      .click();
    await expect(mcp).toHaveValue("{invalid");
    await mcp.fill(
      '{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"documentation":{"type":"streamable-http","url":"https://example.test/mcp"}}}',
    );
    await expect(
      page.getByRole("textbox", { name: "MCP endpoint URL", exact: true }),
    ).toHaveValue("https://example.test/mcp");
    await page
      .getByRole("button", { name: "Add MCP server", exact: true })
      .click();
    const second = page.getByRole("group", {
      name: "MCP server 2",
      exact: true,
    });
    await second
      .getByRole("textbox", { name: "Command", exact: true })
      .fill("second-tool");
    const secondName = second.getByRole("textbox", {
      name: "Server 2 name",
      exact: true,
    });
    await secondName.fill("1");
    await secondName.press("2");
    await expect(secondName).toHaveValue("12");
    await expect(secondName).toBeFocused();
    await expect(
      second.getByRole("textbox", { name: "Command", exact: true }),
    ).toHaveValue("second-tool");
    page.once("dialog", (dialog) => dialog.accept());
    await second
      .getByRole("button", { name: "Remove MCP server 2", exact: true })
      .click();
    await page
      .getByRole("button", {
        name: "Preview and download Plugin files",
        exact: true,
      })
      .click();
    await expect(files.locator("summary")).toHaveText([
      "plugin.json",
      "mcp.json",
      "skills/review-notes/SKILL.md",
    ]);
    await expect(files).toContainText(
      "zip -r ../review-tools.zip plugin.json mcp.json skills",
    );
    await expect(files).toContainText(
      "tar -czf ../review-tools.tar.gz plugin.json mcp.json skills",
    );
    const prepared = await files.locator("details").evaluateAll((details) =>
      details.map((item) => ({
        path: item.querySelector("summary")!.textContent,
        content: item.querySelector("pre")!.textContent,
      })),
    );
    await writeFile(
      testInfo.outputPath("prepared-files.json"),
      JSON.stringify(prepared),
    );
    if (base === "root") {
      await page
        .getByRole("heading", { name: "Authoring", exact: true })
        .focus();
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({
        path: testInfo.outputPath("plugin-field-authoring.png"),
        fullPage: true,
      });
    }
    // Advanced YAML remains downloadable even when safe guided parsing is unavailable.
    const skill = page.getByRole("group", {
      name: "Skill component",
      exact: true,
    });
    await skill
      .getByRole("button", { name: "Edit component SKILL.md", exact: true })
      .click();
    const skillText = skill.getByRole("textbox", {
      name: "Component SKILL.md",
      exact: true,
    });
    const originalSkill = await skillText.inputValue();
    const advancedSkill =
      "---\nname: review-notes\ndescription: |\n  Review the release notes.\n  Summarize the change.\n---\n\n# Review\n\nRead the supplied notes.\n";
    await skillText.fill(advancedSkill);
    const skillDownload = page.waitForEvent("download");
    await skill
      .getByRole("link", { name: "Download SKILL.md", exact: true })
      .click();
    const actualSkill = await readFile(
      required(await (await skillDownload).path()),
      "utf8",
    );
    expect(actualSkill).toBe(advancedSkill);
    for (const [label, filename] of [
      ["Plugin manifest", "plugin.json"],
      ["Plugin MCP", "mcp.json"],
    ] as const) {
      const group = page.getByRole("group", { name: label, exact: true });
      await group
        .getByRole("button", { name: `Edit ${label} JSON`, exact: true })
        .click();
      const pending = page.waitForEvent("download");
      await group
        .getByRole("link", { name: `Download ${filename}`, exact: true })
        .click();
      const bytes = await readFile(
        required(await (await pending).path()),
        "utf8",
      );
      expect(bytes).toBe(
        required(prepared.find((item) => item.path === filename)).content,
      );
      await group
        .getByRole("button", { name: `Use ${label} fields`, exact: true })
        .click();
    }
    await writeFile(
      testInfo.outputPath("advanced-files.json"),
      JSON.stringify(
        prepared.map((item) =>
          item.path?.endsWith("SKILL.md")
            ? { ...item, content: actualSkill }
            : item,
        ),
      ),
    );
    await skillText.fill(originalSkill);
    await skill
      .getByRole("button", { name: "Use Skill fields", exact: true })
      .click();
    // Next/Back remounts the representation editors but must keep discard protection.
    await page
      .getByRole("button", { name: "Add representation", exact: true })
      .click();
    const extra = page.getByRole("group", {
      name: /content 2$/,
      exact: true,
    });
    await extra
      .getByRole("combobox", { name: "Plugin input", exact: true })
      .selectOption("prepare");
    await extra
      .getByRole("textbox", { name: "Plugin name", exact: true })
      .fill("second-draft");
    await page
      .getByRole("button", { name: "Back: Basics", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    page.once("dialog", (dialog) => dialog.dismiss());
    await page
      .getByRole("button", { name: "Remove representation 2", exact: true })
      .click();
    await expect(
      extra.getByRole("textbox", { name: "Plugin name", exact: true }),
    ).toHaveValue("second-draft");
    page.once("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("button", { name: "Remove representation 2", exact: true })
      .click();
    await expect(extra).toHaveCount(0);
    await expect(input).toBeFocused();
    await page
      .getByRole("button", { name: "Save content draft", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText("Package and host");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("alert")).toBeFocused();
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("button", {
        name: "Continue with archive inputs",
        exact: true,
      })
      .click();
    await page
      .getByRole("textbox", { name: "Plugin archive URL", exact: true })
      .fill("https://example.test/artifacts/current");
    const checksum = "sha256:" + "a".repeat(64);
    await page
      .getByRole("textbox", {
        name: "Expected archive checksum (optional)",
        exact: true,
      })
      .fill(checksum);
    await page
      .getByRole("combobox", { name: "Plugin archive format", exact: true })
      .selectOption("application/agent-plugins+gzip");
    await input.selectOption("prepare");
    await expect(
      page.getByRole("textbox", { name: "Plugin name", exact: true }),
    ).toHaveValue("review-tools");
    await input.selectOption("archive");
    await expect(
      page.getByRole("textbox", {
        name: "Expected archive checksum (optional)",
        exact: true,
      }),
    ).toHaveValue(checksum);
    await page
      .getByRole("textbox", { name: "Plugin archive URL", exact: true })
      .fill("https://example.test/artifacts/replacement");
    await expect(
      page.getByRole("textbox", {
        name: "Expected archive checksum (optional)",
        exact: true,
      }),
    ).toHaveValue(checksum);
    expect(writes).toEqual([]);
    expect(outside).toEqual([]);
  });
}
