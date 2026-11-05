import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { object, text as jsonText } from "./json";

test("Skill upload rejects an empty file and preserves inspected archive metadata after reload", async ({
  page,
  catalog,
  context,
}) => {
  const directory = await mkdtemp(join(tmpdir(), "ui-skill-upload-"));
  try {
    const markdown =
      "---\nname: browser-skill\ndescription: Review synthetic browser evidence.\nlicense: Apache-2.0\n---\n\n# Review\n\nRead evidence. Never execute it.\n";
    await mkdir(join(directory, "browser-skill"));
    await writeFile(join(directory, "browser-skill/SKILL.md"), markdown);
    // Archive only the explicitly owned synthetic tree; no host/project files.
    const archive = execFileSync(
      "tar",
      ["--format=ustar", "-czf", "-", "-C", directory, "browser-skill"],
      { timeout: 5000, maxBuffer: 65536 },
    );
    const resource = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        kind: "skill",
        canonicalIdentifier:
          "urn:air:catalog.example.com:demo:browser-skill-package",
      },
    );
    await login(
      page,
      catalog,
      "local-contributor",
      `/resources/${resource.data.id}`,
    );
    await page.getByRole("link", { name: "Add version", exact: true }).click();
    let versionWrites = 0;
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().endsWith(`/resources/${resource.data.id}/versions`)
      )
        versionWrites++;
    });
    await page
      .getByRole("textbox", { name: "Version label", exact: true })
      .fill("1.0.0");
    await page
      .getByRole("combobox", { name: "Content source", exact: true })
      .selectOption("package");
    await expect(
      page.getByText(/Choose a non-empty archive up to 16 MiB/),
    ).toBeVisible();
    await expect(
      page.getByText(/A discovery snapshot also needs/),
    ).toBeVisible();
    const prerequisite = page.getByText(
      /Supply at least one immutable Git or OCI reference for the complete package/,
    );
    await expect(prerequisite).toBeVisible();
    const upload = page.getByLabel("Skill archive", { exact: true });
    await upload.setInputFiles({
      name: "empty.tgz",
      mimeType: "application/gzip",
      buffer: Buffer.alloc(0),
    });
    await expect(page.getByRole("alert")).toContainText(
      "Choose a non-empty file",
    );
    await upload.setInputFiles({
      name: "browser-skill.tgz",
      mimeType: "application/gzip",
      buffer: archive,
    });
    await page
      .getByText("Advanced representation settings", { exact: true })
      .click();
    await expect(
      page.getByRole("textbox", { name: "Media type", exact: true }),
    ).toHaveValue("application/agent-skills+gzip");
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    const missingHandoff = page.getByRole("alert");
    await expect(missingHandoff).toContainText(
      "Supply at least one immutable Git or OCI reference.",
    );
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await missingHandoff
      .getByRole("link", {
        name: "Supply at least one immutable Git or OCI reference.",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("button", {
        name: "Edit immutable handoff as JSON",
        exact: true,
      }),
    ).toBeFocused();
    await expect(upload).toHaveValue(/browser-skill\.tgz$/);
    expect(versionWrites).toBe(0);
    await page.getByLabel("Include Git handoff", { exact: true }).check();
    await page
      .getByRole("textbox", { name: "Git repository", exact: true })
      .fill("https://github.com/example/skills");
    await page
      .getByRole("textbox", { name: "Git commit", exact: true })
      .fill("a".repeat(40));
    await page
      .getByRole("textbox", { name: "Git directory", exact: true })
      .fill("browser-skill");
    await page.getByLabel("Include OCI handoff", { exact: true }).check();
    await page
      .getByRole("textbox", { name: "OCI digest reference", exact: true })
      .fill("registry.example.com/skills@sha256:" + "b".repeat(64));
    await page
      .getByRole("button", {
        name: "Edit immutable handoff as JSON",
        exact: true,
      })
      .click();
    await expect(prerequisite).toBeVisible();
    await expect(
      page.getByRole("textbox", {
        name: "Immutable handoff JSON",
        exact: true,
      }),
    ).toHaveValue(
      JSON.stringify({
        git: {
          repository: "https://github.com/example/skills",
          commit: "a".repeat(40),
          directory: "browser-skill",
        },
        oci: {
          reference: "registry.example.com/skills@sha256:" + "b".repeat(64),
        },
      }),
    );
    const created = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith(`/resources/${resource.data.id}/versions`),
    );
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(
      "Draft saved. Version 1.0.0.",
    );
    expect(versionWrites).toBe(1);
    const createdRevision = jsonText(
      object(object(await (await created).json()).revision).id,
    );
    await page
      .getByRole("link", { name: "Open saved version", exact: true })
      .click();
    await page
      .getByRole("link", { name: "Inspect current revision and validation" })
      .click();
    await page.reload();
    await expect(
      page.getByRole("heading", {
        name: "primary: application/agent-skills+gzip",
        exact: true,
      }),
    ).toBeVisible();
    // Package bytes are deliberately not retained; only inspected metadata and
    // the immutable handoff survive. No network retrieval/execution is needed.
    await expect(
      page.getByRole("button", { name: "Read retained document" }),
    ).toHaveCount(0);
    const inspection = page.getByRole("region", {
      name: "Skill package inspection",
      exact: true,
    });
    await expect(inspection).toContainText("Passed");
    await expect(inspection).toContainText(
      "The complete archive is not stored here",
    );
    await expect(inspection.getByRole("link")).toHaveCount(0);
    await context.grantPermissions(["clipboard-read", "clipboard-write"], {
      origin: catalog.origin,
    });
    const inspectionReads: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "GET") inspectionReads.push(request.url());
    });
    for (const [label, literal] of [
      ["Git repository", "https://github.com/example/skills"],
      ["Git commit", "a".repeat(40)],
      ["Git directory", "browser-skill"],
      [
        "OCI digest reference",
        "registry.example.com/skills@sha256:" + "b".repeat(64),
      ],
    ]) {
      await inspection
        .getByRole("button", { name: `Copy ${label}`, exact: true })
        .click();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
        literal,
      );
    }
    await inspection.getByText(/^File inventory/).click();
    await expect(inspection).toContainText("browser-skill/SKILL.md");
    expect(inspectionReads).toEqual([]);
    await page.getByText("Representation metadata", { exact: true }).click();
    const text = await page.locator("main").innerText();
    expect(text).toContain("browser-skill");
    expect(text).toContain("https://github.com/example/skills");
    expect(text).toContain("a".repeat(40));
    expect(text).toContain(
      "registry.example.com/skills@sha256:" + "b".repeat(64),
    );
    expect(text).not.toContain(archive.toString("base64"));
    expect(
      await readFile(join(directory, "browser-skill/SKILL.md"), "utf8"),
    ).toBe(markdown);
    // Reuse the real inspected tree above; API calls prepare governed Reader handoff prerequisites.
    const listing = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/catalog-listings",
      {
        namespaceId: catalog.receipt.namespaceId,
        resourceId: resource.data.id,
      },
    );
    const revision = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${listing.data.id}/revisions`,
      {
        versionRevisionId: createdRevision,
        publisherId: catalog.receipt.publisherId,
        displayName: "Browser handoff Skill",
        description:
          "A governed synthetic Skill for safe reference navigation.",
        tags: ["browser-handoff"],
        // Complete inspected trees with Git handoff have no public archive URL;
        // authenticated private discovery preserves that publication boundary.
        visibility: "private",
        documentationUrl: "https://docs.example.test/skill",
      },
      listing.etag,
    );
    const submission = await catalog.admin<{ id: string }>(
      "POST",
      `/api/v1/catalog-listings/${listing.data.id}/submissions`,
      {},
    );
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${submission.data.id}:approve`,
      { reason: "Independent browser handoff fixture review" },
      submission.etag,
      "local-reviewer",
    );
    const published = await catalog.admin<{ release: { id: string } }>(
      "POST",
      `/api/v1/catalog-listings/${listing.data.id}:publish`,
      { submissionId: submission.data.id },
      revision.etag,
    );
    await signOut(page);
    await login(
      page,
      catalog,
      "local-reader",
      `/catalog/${published.data.release.id}`,
    );
    const handoff = page.getByRole("region", { name: "Resource handoff" });
    // Current Skill normalized facts do not project frontmatter license. Do not
    // invent one from Listing metadata or parse retained Markdown in the UI.
    await expect(
      handoff.getByText("Declared native license", { exact: true }),
    ).toHaveCount(0);
    for (const [label, value] of [
      ["Package structure", "passed"],
      ["Skill root", "browser-skill"],
      ["Inspected files", "1"],
    ] as const) {
      await expect(
        handoff
          .locator("dt")
          .filter({ hasText: new RegExp(`^${label}$`) })
          .locator("+ dd"),
      ).toHaveText(value);
    }
    await expect(
      handoff.getByRole("button", {
        name: "Prepare retained document download",
      }),
    ).toHaveCount(0);
    await handoff.getByText("Representation evidence", { exact: true }).click();
    await expect(handoff.locator("details")).toContainText("Inspected archive");
    await expect(handoff.locator("details")).toContainText(
      "sha256:" + createHash("sha256").update(archive).digest("hex"),
    );
    const git = page.getByRole("link", {
      name: "Open Git repository",
      exact: true,
    });
    await expect(git).toHaveAttribute(
      "href",
      "https://github.com/example/skills",
    );
    await expect(git).toHaveAttribute("target", "_blank");
    await expect(git).toHaveAttribute("rel", "noopener noreferrer");
    await expect(git).toHaveAttribute("referrerpolicy", "no-referrer");
    await expect(
      page.getByRole("button", { name: "Copy Git repository", exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("main")
        .getByRole("link", { name: "Documentation", exact: true }),
    ).toHaveAttribute("href", "https://docs.example.test/skill");
    await expect(
      page.getByRole("button", { name: "Copy Documentation", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Open Git commit", exact: true }),
    ).toHaveCount(0);
    // No external destination is followed: attributes and lack of automatic traffic are verified by the network ledger.
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("add a primary Skill document for discovery without losing the complete archive or handoff", async ({
  page,
  catalog,
}) => {
  const directory = await mkdtemp(join(tmpdir(), "ui-skill-discovery-"));
  try {
    const markdown =
      "---\nname: review-notes\ndescription: Review change evidence.\n---\n\n# Review\nRead references/checks.md.\n";
    await mkdir(join(directory, "review-notes/references"), {
      recursive: true,
    });
    await writeFile(join(directory, "review-notes/SKILL.md"), markdown);
    await writeFile(
      join(directory, "review-notes/references/checks.md"),
      "# Checks\nInspect the supplied evidence.\n",
    );
    const archive = execFileSync(
      "tar",
      ["--format=ustar", "-czf", "-", "-C", directory, "review-notes"],
      { timeout: 5000, maxBuffer: 65536 },
    );
    const resource = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        kind: "skill",
        canonicalIdentifier:
          "urn:air:catalog.example.com:guided:skill-package-document",
      },
    );
    await login(
      page,
      catalog,
      "local-contributor",
      `/versions/new?resource=${resource.data.id}`,
    );
    await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
    await page
      .getByRole("combobox", { name: "Content source", exact: true })
      .selectOption("package");
    await page.getByLabel("Skill archive", { exact: true }).setInputFiles({
      name: "review-notes.tgz",
      mimeType: "application/gzip",
      buffer: archive,
    });
    await page.getByLabel("Include Git handoff", { exact: true }).check();
    await page
      .getByLabel("Git repository", { exact: true })
      .fill("https://git.example.test/skills");
    await page.getByLabel("Git commit", { exact: true }).fill("a".repeat(40));
    await page
      .getByLabel("Git directory", { exact: true })
      .fill("skills/review-notes");
    await page
      .getByRole("button", {
        name: "Add primary SKILL.md for discovery",
        exact: true,
      })
      .click();
    const packageRow = page.getByRole("group", {
      name: /content 1$/,
      exact: true,
    });
    await expect(
      packageRow.getByLabel("Skill archive", { exact: true }),
    ).toHaveValue(/review-notes\.tgz$/u);
    await expect(
      packageRow.getByLabel("Git commit", { exact: true }),
    ).toHaveValue("a".repeat(40));
    await page.getByLabel("Skill name", { exact: true }).fill("review-notes");
    await page
      .getByLabel("Skill description", { exact: true })
      .fill("Review change evidence.");
    await page
      .getByLabel("Skill instructions (Markdown)", { exact: true })
      .fill("# Review\nRead references/checks.md.\n");
    const saved = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith(`/resources/${resource.data.id}/versions`),
    );
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await confirmCommand(page);
    const response = await saved;
    expect(response.status()).toBe(201);
    const bundle = object(await response.json());
    expect(object(bundle.evidence).state).toBe("complete");
    const representations = object(bundle.revision).representations;
    expect(representations).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          role: "distribution",
          mediaType: "application/agent-skills+gzip",
          contentDigest: `sha256:${createHash("sha256").update(archive).digest("hex")}`,
          validationState: "valid",
          handoff: {
            git: {
              repository: "https://git.example.test/skills",
              commit: "a".repeat(40),
              directory: "skills/review-notes",
            },
          },
        }),
        expect.objectContaining({
          role: "primary",
          mediaType: "application/agent-skills+md",
          validationState: "valid",
        }),
      ]),
    );
    expect(representations).toHaveLength(2);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
