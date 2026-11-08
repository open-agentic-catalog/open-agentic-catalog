// Exact document comparison crosses the real authenticated artifact boundary on explicit inspection.
import { createHash } from "node:crypto";
import { test, expect, login } from "./fixtures";
import { array, object, text } from "./json";
import { required } from "../required";

test("subpath: compare selected retained documents without normalizing their exact text", async ({
  page,
  catalog,
}) => {
  const profileId = "agent-skills-69ef37e";
  const primary =
    '---\nname: comparison-primary\ndescription: "Read the primary Skill before use."\n---\n# Primary\n\nKeep this document unselected.\n';
  const frontmatter =
    '---\nname: comparison-notes\ndescription: "Compare the retained Skill notes without rewriting whitespace."\n---\n';
  const originals = [
    frontmatter + "# Exact retained notes\n\nRead the Skill before use.\n",
    frontmatter + "# Exact retained notes\n\nRead the Skill before use.  \n\n",
  ];
  const documents = originals.map((content) => {
    const bytes = Buffer.from(content, "utf8");
    return {
      content,
      bytes,
      id: createHash("sha256").update(bytes).digest("hex"),
      header: `sha-256=:${createHash("sha256").update(bytes).digest("base64")}:`,
    };
  });
  expect(required(documents[0]).id).not.toBe(required(documents[1]).id);
  const resource = await catalog.admin("POST", "/api/v1/resources", {
    namespaceId: catalog.receipt.namespaceId,
    kind: "skill",
    canonicalIdentifier:
      "urn:air:catalog.example.com:authoring:exact-comparison",
  });
  const resourceId = text(resource.data.id);
  // A valid primary plus separate retained documentation makes selection observable.
  const representations = (content: string) => [
    {
      role: "primary",
      mediaType: "application/agent-skills+md",
      profileId,
      content: primary,
    },
    {
      role: "documentation",
      mediaType: "application/agent-skills+md",
      profileId,
      content,
    },
  ];
  const before = await catalog.admin(
    "POST",
    `/api/v1/resources/${resourceId}/versions`,
    {
      version: "1.0.0",
      representations: representations(required(documents[0]).content),
    },
  );
  const versionId = text(object(before.data.version).id);
  const after = await catalog.admin(
    "POST",
    `/api/v1/resource-versions/${versionId}/revisions`,
    { representations: representations(required(documents[1]).content) },
    before.etag,
  );
  const bundles = [before, after];
  const revisionIds = bundles.map((bundle) =>
    text(object(bundle.data.revision).id),
  );
  for (let index = 0; index < bundles.length; index++) {
    const bundle = required(bundles[index]);
    const document = required(documents[index]);
    const evidence = object(bundle.data.evidence);
    const findingCodes = array(evidence.findings ?? []).map((finding) =>
      text(object(finding).code),
    );
    expect(
      evidence.state,
      `Native Skill fixture must be complete; findings: ${findingCodes.join(", ")}`,
    ).toBe("complete");
    expect(array(object(bundle.data.revision).representations)).toHaveLength(2);
    expect(
      object(required(array(object(bundle.data.revision).representations)[0])),
    ).toMatchObject({
      role: "primary",
      mediaType: "application/agent-skills+md",
      profileId,
      validationState: "valid",
    });
    expect(
      object(required(array(object(bundle.data.revision).representations)[1])),
    ).toMatchObject({
      role: "documentation",
      mediaType: "application/agent-skills+md",
      profileId,
      artifactId: document.id,
      contentDigest: `sha256:${document.id}`,
      size: document.bytes.byteLength,
      validationState: "valid",
    });
  }
  const historyPath = `/api/v1/resource-versions/${versionId}/revisions?limit=100`;
  const historyBefore = await catalog.admin("GET", historyPath);
  expect(historyBefore.data.nextCursor).toBeUndefined();
  expect(
    array(historyBefore.data.items).map((revision) =>
      text(object(revision).id),
    ),
  ).toEqual(revisionIds);
  const versionBefore = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${versionId}`,
  );
  const artifactReads: string[] = [];
  const revisionReads: string[] = [];
  const writes: string[] = [];
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== catalog.origin
    )
      outside.push(url.origin);
    if (!url.pathname.startsWith(`${catalog.basePath}/_catalog/api/v1/`))
      return;
    if (request.method() !== "GET")
      writes.push(request.method() + " " + url.pathname);
    else if (url.pathname.includes("/artifacts/"))
      artifactReads.push(url.pathname);
    else if (url.pathname.includes("/version-revisions/"))
      revisionReads.push(url.pathname);
  });
  await login(page, catalog, "local-contributor", `/versions/${versionId}`);
  const comparison = page.getByRole("region", {
    name: "Revision comparison",
    exact: true,
  });
  await comparison
    .getByRole("button", { name: "Choose revisions to compare", exact: true })
    .click();
  await comparison
    .getByRole("combobox", { name: "Before revision", exact: true })
    .selectOption(required(revisionIds[0]));
  await comparison
    .getByRole("combobox", { name: "After revision", exact: true })
    .selectOption(required(revisionIds[1]));
  expect(revisionReads).toEqual([]);
  expect(artifactReads).toEqual([]);
  await comparison
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  const compared = page.getByLabel("Compared revisions", { exact: true });
  for (const id of revisionIds) await expect(compared).toContainText(id);
  const selections = page.getByRole("region", {
    name: "Document comparison",
    exact: true,
  });
  for (const [index, side] of ["Before", "After"].entries()) {
    const select = selections.getByRole("combobox", {
      name: `${side} representation`,
      exact: true,
    });
    await expect(
      select.getByRole("option", {
        name: `documentation / application/agent-skills+md / sha256:${required(documents[index]).id}`,
        exact: true,
      }),
    ).toHaveAttribute("value", "1");
    await select.selectOption("1");
  }
  expect(revisionReads).toEqual(
    revisionIds.map(
      (id) => `${catalog.basePath}/_catalog/api/v1/version-revisions/${id}`,
    ),
  );
  expect(artifactReads).toEqual([]);
  await expect(
    selections.getByRole("group", { name: "Text document", exact: true }),
  ).toHaveCount(0);
  expect(writes).toEqual([]);
  await selections
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(selections).toContainText(
    "Exact retained documents differ, including any whitespace or numeric token changes.",
  );
  for (const [index, side] of ["Before", "After"].entries()) {
    const panel = selections
      .getByRole("heading", { name: `${side} document`, exact: true })
      .locator("..");
    const original = panel.getByRole("group", {
      name: "Text document",
      exact: true,
    });
    // toHaveText normalizes whitespace; textContent must retain every original byte-decoded character.
    await expect(original).toBeVisible();
    expect(await original.textContent()).toBe(
      required(documents[index]).content,
    );
    await expect(
      panel.getByRole("button", { name: "Format JSON", exact: true }),
    ).toHaveCount(0);
  }
  expect(artifactReads).toEqual(
    documents.map(
      (document) =>
        `${catalog.basePath}/_catalog/api/v1/artifacts/${document.id}/content`,
    ),
  );
  expect(writes).toEqual([]);
  expect(outside).toEqual([]);

  // Separate authenticated reads verify retained bytes and wire digests independently of the UI display.
  for (const document of documents) {
    const response = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${document.id}/content`,
      { headers: { "X-Catalog-Request": "1" } },
    );
    expect(response.status()).toBe(200);
    expect(response.headers()["content-digest"]).toBe(document.header);
    expect(await response.body()).toEqual(document.bytes);
  }
  for (const bundle of bundles) {
    const retained = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${text(object(bundle.data.revision).id)}`,
    );
    expect(retained.data.revision).toEqual(bundle.data.revision);
  }
  const versionAfter = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${versionId}`,
  );
  expect(versionAfter.data).toEqual(versionBefore.data);
  expect(versionAfter.etag).toBe(versionBefore.etag);
  expect((await catalog.admin("GET", historyPath)).data).toEqual(
    historyBefore.data,
  );
  expect(writes).toEqual([]);
  expect(outside).toEqual([]);
});
