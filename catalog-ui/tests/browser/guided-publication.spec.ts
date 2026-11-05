// The documented role handoffs start with fields and finish with actual discovery bytes.
import { writeFile } from "node:fs/promises";
import { test, expect, login, signOut, confirmCommand } from "./fixtures";
import {
  fillAgentFields,
  fillHostedMCPFields,
  fillSkillFields,
} from "./authoring-fields";
import {
  downloadPluginFiles,
  fillPluginFields,
} from "./authoring-plugin-fields";
import { object, array, text } from "./json";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";

const journeys = [
  { kind: "agent", label: "Agent", segment: "agents", document: "Agent Card" },
  {
    kind: "mcp_server",
    label: "MCP Server",
    segment: "mcp-servers",
    document: "MCP Server Card",
  },
  {
    kind: "skill",
    label: "Skill",
    segment: "skills",
    document: "Skill document",
  },
  {
    kind: "plugin",
    label: "Plugin",
    segment: "plugins",
    document: "Plugin package",
  },
] as const;

for (const journey of journeys) {
  test.describe(journey.label, () => {
    test.use({
      demoArchives: journey.kind === "plugin",
      authoredPlugin: journey.kind === "plugin",
    });
    test(`guided ${journey.label} tutorial requests changes, preserves exact evidence, publishes and activates discovery`, async ({
      page,
      catalog,
    }, testInfo) => {
      const identifier = `urn:air:demo.catalog.example.com:${journey.segment}:browser-publication-demo`;
      await login(page, catalog, "local-contributor", "/resources/new");
      await page
        .getByRole("combobox", { name: "Namespace", exact: true })
        .selectOption(catalog.receipt.namespaceId);
      const outside: string[] = [];
      page.on("request", (request) => {
        const url = new URL(request.url());
        if (
          ["http:", "https:"].includes(url.protocol) &&
          url.origin !== catalog.origin
        )
          outside.push(url.origin);
      });
      await page
        .getByRole("combobox", { name: "Kind", exact: true })
        .selectOption(journey.kind);
      await page
        .getByLabel("Canonical resource identifier", { exact: true })
        .fill(identifier);
      await page
        .getByRole("button", { name: "Next: Content", exact: true })
        .click();
      await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
      if (journey.kind === "plugin") {
        await fillPluginFields(page);
        const documents = await downloadPluginFiles(page);
        const archive = required(catalog.generatedPlugin);
        await archive.verifyDownloads(documents);
        await page
          .getByRole("button", {
            name: "Continue with archive inputs",
            exact: true,
          })
          .click();
        await page
          .getByLabel("Plugin archive URL", { exact: true })
          .fill(archive.url);
        await page
          .getByLabel("Expected archive checksum (optional)", { exact: true })
          .fill(archive.digest);
        await expect(
          page.getByLabel("Plugin archive URL", { exact: true }),
        ).toHaveValue(archive.url);
        await expect(
          page.getByLabel("Expected archive checksum (optional)", {
            exact: true,
          }),
        ).toHaveValue(archive.digest);
      } else {
        await {
          agent: fillAgentFields,
          mcp_server: fillHostedMCPFields,
          skill: fillSkillFields,
        }[journey.kind](page);
      }
      // These pages intentionally retain form input after readback. Accept only
      // the actual account handoff confirmation; any other dialog fails the test.
      page.on("dialog", async (dialog) => {
        expect(dialog.type()).toBe("confirm");
        expect(dialog.message()).toBe(
          "Sign out and discard unsaved input or an unresolved command? A request already sent may still complete.",
        );
        await dialog.accept();
      });
      await page.getByLabel("Include Listing details", { exact: true }).check();
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      await page
        .getByRole("combobox", { name: "Publisher", exact: true })
        .selectOption(catalog.receipt.publisherId);
      await page
        .getByRole("textbox", { name: "Display name", exact: true })
        .fill(`Browser publication tutorial ${journey.label}`);
      await page
        .getByRole("textbox", { name: "Description", exact: true })
        .fill(
          `${journey.label} metadata for the browser publication tutorial.`,
        );
      await page
        .getByRole("combobox", { name: "Visibility", exact: true })
        .selectOption("public");
      await page
        .getByRole("textbox", {
          name: "Representative queries (one per line)",
          exact: true,
        })
        .fill(
          "Find the browser publication tutorial Agent\nFind setup metadata for the browser publication tutorial",
        );
      await openDisclosure(page, "Links, license and relationships");
      await page
        .getByRole("textbox", { name: "License", exact: true })
        .fill("Apache-2.0");
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("link", { name: "Open saved Listing", exact: true })
        .click();
      const listingURL = page.url();
      const listingId = required(
        new URL(listingURL).pathname.split("/").at(-1),
      );
      await page
        .getByRole("button", { name: "Submit for review", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("link", { name: "Open review submission", exact: true })
        .click();
      const firstSubmissionURL = page.url();
      const firstId = required(
        new URL(firstSubmissionURL).pathname.split("/").at(-1),
      );
      await page
        .getByRole("button", {
          name: "Inspect submitted evidence",
          exact: true,
        })
        .click();
      await expect(
        page.getByRole("button", {
          name: "Record review decision",
          exact: true,
        }),
      ).toHaveCount(0);
      const firstEvidence = await catalog.admin(
        "GET",
        `/api/v1/review-submissions/${firstId}/evidence`,
      );
      await signOut(page);
      await login(page, catalog, "local-reviewer", `/reviews/${firstId}`);
      await page
        .getByRole("button", {
          name: "Inspect submitted evidence",
          exact: true,
        })
        .click();
      await page
        .getByRole("combobox", { name: "Decision", exact: true })
        .selectOption("request-changes");
      await page
        .getByRole("textbox", { name: "Reason", exact: true })
        .fill("State that this metadata was independently reviewed.");
      await page
        .getByRole("button", { name: "Record review decision", exact: true })
        .click();
      await confirmCommand(page);
      await expect(page.getByRole("status")).toContainText(
        "Review changes required.",
      );
      await signOut(page);
      await login(page, catalog, "local-contributor", `/listings/${listingId}`);
      await page
        .getByRole("button", { name: "New listing revision", exact: true })
        .click();
      await expect(
        page.getByRole("checkbox", {
          name: "Keep the current version revision",
          exact: true,
        }),
      ).toBeChecked();
      await page
        .getByRole("textbox", { name: "Description", exact: true })
        .fill(
          `${journey.label} metadata for the browser publication tutorial, independently reviewed.`,
        );
      await page
        .getByRole("button", { name: "Create listing revision", exact: true })
        .click();
      await confirmCommand(page);
      await page.reload();
      await page
        .getByRole("button", { name: "Submit for review", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("link", { name: "Open review submission", exact: true })
        .click();
      const secondId = required(new URL(page.url()).pathname.split("/").at(-1));
      expect(secondId).not.toBe(firstId);
      const retainedFirst = await catalog.admin(
        "GET",
        `/api/v1/review-submissions/${firstId}/evidence`,
      );
      expect(retainedFirst.data).toEqual(firstEvidence.data);
      await signOut(page);
      await login(page, catalog, "local-reviewer", `/reviews/${secondId}`);
      await page
        .getByRole("button", {
          name: "Inspect submitted evidence",
          exact: true,
        })
        .click();
      await page
        .getByRole("combobox", { name: "Decision", exact: true })
        .selectOption("approve");
      await page
        .getByRole("textbox", { name: "Reason", exact: true })
        .fill(
          `Independently reviewed the corrected browser tutorial listing and exact ${journey.document}.`,
        );
      await page
        .getByRole("button", { name: "Record review decision", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("button", {
          name: "Reload review and next steps",
          exact: true,
        })
        .click();
      await expect(
        page.getByRole("heading", { name: "Review approved", exact: true }),
      ).toBeVisible();
      await page.evaluate(() => {
        (document.activeElement as HTMLElement).blur();
        window.scrollTo(0, 0);
      });
      await page.screenshot({
        path: testInfo.outputPath("guided-review-approved.png"),
        fullPage: true,
      });
      await signOut(page);
      await login(page, catalog, "local-publisher", `/reviews/${secondId}`);
      await page
        .getByRole("button", { name: "Publish approved listing", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("link", { name: "Open published release", exact: true })
        .click();
      const releaseId = required(
        new URL(page.url()).pathname.split("/").at(-1),
      );
      await signOut(page);
      await login(
        page,
        catalog,
        "local-template-publication-admin",
        "/publication",
      );
      await page
        .getByRole("combobox", {
          name: "Configured publication hosts",
          exact: true,
        })
        .selectOption("127.0.0.1");
      await page
        .getByRole("button", { name: "Open host", exact: true })
        .click();
      await page
        .getByRole("button", { name: "Build candidate snapshot", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("link", { name: "Inspect candidate", exact: true })
        .click();
      const snapshotId = required(
        new URL(page.url()).pathname.split("/").at(-1),
      );
      await page
        .getByRole("textbox", { name: "Reason", exact: true })
        .fill("Activate independently reviewed browser tutorial metadata.");
      await page
        .getByRole("button", { name: "Activate candidate", exact: true })
        .click();
      await confirmCommand(page);
      await page
        .getByRole("button", { name: "Reload snapshot state", exact: true })
        .click();
      await expect(
        page.getByRole("heading", { name: /Generation .*: active/ }),
      ).toBeVisible();
      const snapshot = await catalog.admin(
        "GET",
        `/api/v1/publication/snapshots/${snapshotId}?host=127.0.0.1`,
      );
      for (const field of ["ard", "aiCatalog"]) {
        const entries = array(object(snapshot.data[field]).entries);
        expect(
          entries.some(
            (entry) =>
              object(entry).identifier === identifier &&
              object(entry).version === "1.0.0",
          ),
        ).toBe(true);
      }
      for (const [field, document] of [
        ["ard", "ard"],
        ["aiCatalog", "ai-catalog"],
      ] as const) {
        const served = await catalog.admin(
          "GET",
          `/.well-known/${document}.json`,
        );
        expect(served.data).toEqual(snapshot.data[field]);
      }
      await signOut(page);
      await login(page, catalog, "local-reader", `/catalog/${releaseId}`);
      await expect(
        page.getByRole("heading", {
          name: `Browser publication tutorial ${journey.label}`,
          exact: true,
        }),
      ).toBeVisible();
      await expect(
        page
          .getByText(
            `${journey.label} metadata for the browser publication tutorial, independently reviewed.`,
            { exact: true },
          )
          .first(),
      ).toBeVisible();
      const listing = await catalog.admin(
        "GET",
        `/api/v1/catalog-listings/${listingId}`,
      );
      expect(text(listing.data.currentReleaseId)).toBe(releaseId);
      const record = await catalog.admin(
        "GET",
        `/api/v1/catalog/resources/${releaseId}`,
      );
      expect(object(record.data.resource).kind).toBe(journey.kind);
      expect(outside).toEqual([]);
      await page.goto(`${catalog.origin}${catalog.basePath}/catalog`);
      await page
        .getByRole("searchbox", { name: "Search Catalog", exact: true })
        .fill(`Browser publication tutorial ${journey.label}`);
      await page.getByRole("button", { name: "Search", exact: true }).click();
      await page
        .getByRole("button", {
          name: `Browser publication tutorial ${journey.label}`,
          exact: true,
        })
        .click();
      await page
        .getByRole("link", { name: "Open full record", exact: true })
        .click();
      await expect(page).toHaveURL(
        `${catalog.origin}${catalog.basePath}/catalog/${releaseId}`,
      );
      await expect(
        page.getByRole("heading", {
          name: `Browser publication tutorial ${journey.label}`,
          exact: true,
        }),
      ).toBeVisible();
      await page.reload();
      await expect(
        page.getByRole("heading", {
          name: `Browser publication tutorial ${journey.label}`,
          exact: true,
        }),
      ).toBeVisible();
      expect(outside).toEqual([]);
      await page.screenshot({
        path: testInfo.outputPath("guided-catalog-record.png"),
        fullPage: true,
      });
      const references = testInfo.outputPath("created-record.json");
      await writeFile(
        references,
        JSON.stringify({
          kind: journey.kind,
          identifier,
          listingId,
          releaseId,
          snapshotId,
        }),
      );
      await testInfo.attach("created-record", {
        contentType: "application/json",
        path: references,
      });
    });
  });
}
