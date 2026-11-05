// Rendered controls and bounded display fixtures supplement the real workflow journeys.
import type { Locator, Page } from "@playwright/test";
import { required } from "../required";
import { array, object, text } from "./json";
import { test, expect, login } from "./fixtures";
import { tabTo, visibleFocus } from "./keyboard";
import { doubleText, expectNaturalText } from "./text-layout";
import { fillAgentFields } from "./authoring-fields";
import { openDisclosure } from "./editor-disclosure";

async function bounds(value: Locator) {
  await expect(value).toBeVisible();
  const box = await value.boundingBox();
  if (!box) throw new Error("Expected a rendered control or container");
  return box;
}

/** Every line stays inside its available container, with all supplied text retained. */
async function containedText(value: Locator, container: Locator = value) {
  await expect(value).toBeVisible();
  const available = await bounds(container);
  const lines = await value.evaluate((node) => {
    const range = document.createRange();
    range.selectNodeContents(node);
    return Array.from(range.getClientRects(), (rect) => ({
      left: rect.left,
      right: rect.right,
      top: Math.round(rect.top),
    }));
  });
  expect(new Set(lines.map((line) => line.top)).size).toBeGreaterThan(1);
  for (const line of lines) {
    expect(line.left).toBeGreaterThanOrEqual(available.x - 1);
    expect(line.right).toBeLessThanOrEqual(available.x + available.width + 1);
  }
}

async function reflow(page: Page, width: number) {
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
    "The complete document fits the viewport",
  ).toBeLessThanOrEqual(width);
}

async function appearance(page: Page, choice: string) {
  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: choice, exact: true }).check();
  await page.keyboard.press("Escape");
}

async function expectNativeActions(rows: Locator) {
  for (const row of await rows.all()) {
    if ((await row.locator(":scope > button").count()) === 0) continue;
    const layout = await row.evaluate((node) => {
      const rect = (element: Element) => {
        const b = element.getBoundingClientRect();
        return { x: b.x, y: b.y, right: b.right, bottom: b.bottom };
      };
      const controls = Array.from(node.querySelectorAll("input,select"))
        .filter((control) => control.getBoundingClientRect().width > 0)
        .map(rect);
      const notes = Array.from(
        node.querySelectorAll(".workspace-field-notes,.field-error"),
      )
        .filter((note) => note.getBoundingClientRect().height > 0)
        .map(rect);
      return {
        action: rect(node.querySelector(":scope > button")!),
        controls,
        notes,
      };
    });
    expect(layout.controls.length).toBeGreaterThan(0);
    const right = Math.max(...layout.controls.map((control) => control.right));
    const bottom = Math.max(
      ...layout.controls.map((control) => control.bottom),
    );
    if (layout.action.x >= right + 1) {
      expect(Math.abs(layout.action.bottom - bottom)).toBeLessThanOrEqual(2);
      for (const note of layout.notes)
        expect(note.y).toBeGreaterThanOrEqual(bottom - 2);
    } else {
      const last = Math.max(bottom, ...layout.notes.map((note) => note.bottom));
      expect(layout.action.y - last).toBeGreaterThanOrEqual(7);
      expect(layout.action.y - last).toBeLessThanOrEqual(24);
    }
  }
}

for (const base of ["root", "subpath"] as const) {
  test(`${base}: expanded Ratings and feedback keep readable action and panel gaps`, async ({
    page,
    catalog,
  }) => {
    const item = required(
      catalog.receipt.items.find((entry) => entry.releaseId),
    );
    const writes: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (
        path.includes("/_catalog/api/v1/") &&
        !["GET", "HEAD"].includes(request.method()) &&
        !path.endsWith("/ratings/read")
      )
        writes.push(path);
    });
    await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
    const action = page.getByRole("button", {
      name: "Ratings for this version",
      exact: true,
    });
    await action.click();
    const panel = page.getByRole("region", {
      name: "Ratings for this version",
      exact: true,
    });
    const feedback = page.getByRole("region", {
      name: "Private item feedback",
      exact: true,
    });
    await feedback.locator(":scope > details > summary").click();
    await expect(
      feedback.getByRole("button", { name: "Submit feedback", exact: true }),
    ).toBeVisible();
    const gaps = async () => {
      const actionBox = await bounds(action);
      const panelBox = await bounds(panel);
      const feedbackBox = await bounds(feedback);
      expect(
        panelBox.y - actionBox.y - actionBox.height,
      ).toBeGreaterThanOrEqual(12);
      expect(panelBox.y - actionBox.y - actionBox.height).toBeLessThanOrEqual(
        24,
      );
      expect(
        feedbackBox.y - panelBox.y - panelBox.height,
      ).toBeGreaterThanOrEqual(16);
      expect(feedbackBox.y - panelBox.y - panelBox.height).toBeLessThanOrEqual(
        32,
      );
    };
    for (const width of [1440, 700, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      await gaps();
      await reflow(page, width);
    }
    await appearance(page, "Dark");
    await doubleText(page);
    await gaps();
    await reflow(page, 320);
    await action.click();
    await expect(panel).toHaveCount(0);
    await expect(feedback.locator(":scope > details")).toHaveAttribute("open");
    expect(writes).toEqual([]);
  });
}

test("subpath: bounded long metadata wraps in full records, previews and listing tags", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items.find((entry) => entry.releaseId));
  const release = required(item.releaseId);
  const name = "R".repeat(160);
  const description = "D".repeat(300);
  const tag = "W".repeat(80);
  let revision = "";
  // Replace display fields only in genuine owned reads; this does not publish metadata.
  await page.route(
    `**/_catalog/api/v1/catalog/resources/${release}`,
    async (route) => {
      const response = await route.fetch();
      const payload = object(await response.json());
      const listing = object(payload.listingRevision);
      revision = text(listing.id);
      await route.fulfill({
        response,
        json: {
          ...payload,
          listingRevision: {
            ...listing,
            displayName: name,
            description,
            tags: [tag],
          },
        },
      });
    },
  );
  await login(page, catalog, "local-reader", `/catalog/${release}`);
  const record = page.locator(".full-record");
  const ratings = page.getByRole("button", {
    name: "Ratings for this version",
    exact: true,
  });
  const feedback = page.getByRole("region", {
    name: "Private item feedback",
    exact: true,
  });
  const cardBox = await bounds(record);
  const ratingBox = await bounds(ratings);
  const feedbackBox = await bounds(feedback);
  expect(ratingBox.y - cardBox.y - cardBox.height).toBeGreaterThanOrEqual(16);
  expect(ratingBox.y - cardBox.y - cardBox.height).toBeLessThanOrEqual(40);
  expect(feedbackBox.y - ratingBox.y - ratingBox.height).toBeGreaterThanOrEqual(
    16,
  );
  const title = record.getByRole("heading", { name, exact: true });
  const prose = record.locator(".detail-description");
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(title).toHaveText(name);
    await expect(prose).toHaveText(description);
    await containedText(title);
    await containedText(prose);
    await expectNaturalText(title, record, "wrapped");
    await expectNaturalText(prose, record, "wrapped");
    await containedText(
      record.getByRole("button", { name: `Filter by tag ${tag}`, exact: true }),
      record.locator(":scope > .tags"),
    );
    await reflow(page, width);
  }
  expect(revision).not.toBe("");
  await page.route(
    `**/_catalog/api/v1/listing-revisions/${revision}`,
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({
        response,
        json: { ...object(await response.json()), tags: [tag] },
      });
    },
  );
  await page.goto(
    `${catalog.origin}${catalog.basePath}/listing-revisions/${revision}`,
  );
  const tags = page.getByLabel("Tags", { exact: true });
  const badge = tags.getByText(tag, { exact: true });
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(badge).toHaveText(tag);
    await containedText(badge, tags);
    await reflow(page, width);
  }
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    const response = await route.fetch();
    const payload = object(await response.json());
    const items = array(payload.items);
    const first = object(required(items[0]));
    await route.fulfill({
      response,
      json: {
        ...payload,
        items: [
          { ...first, displayName: name, description, tags: [tag] },
          ...items.slice(1),
        ],
      },
    });
  });
  await page.goto(`${catalog.origin}${catalog.basePath}/catalog`);
  await page.getByRole("button", { name, exact: true }).click();
  const preview = page.getByRole("dialog");
  const previewText = preview.locator(".detail-description");
  await expect(previewText).toHaveText(description);
  await containedText(preview.getByRole("heading", { name, exact: true }));
  await containedText(previewText);
  await expectNaturalText(
    previewText,
    preview.locator(".preview-content"),
    "wrapped",
  );
  await containedText(
    preview.getByRole("button", { name: `Filter by tag ${tag}`, exact: true }),
    preview.locator(".preview-content > .tags"),
  );
  await reflow(page, 390);
  await page.keyboard.press("Escape");
});

test("source choices align native checkboxes and long indexed identifiers remain readable", async ({
  page,
  catalog,
}) => {
  const sourceId = "00000000-0000-7000-8000-000000000001";
  const identifier = `urn:air:peer.invalid:agents:${"unbrokenlongidentifier".repeat(15)}`;
  const source = {
    id: sourceId,
    tenantId: "00000000-0000-7000-8000-000000000001",
    displayName: "Configured rendering peer with a longer display name",
    endpoint: "https://peer.invalid/ard/",
    status: "active",
    profileId: "ard",
    retrievalMode: "hybrid",
    updateMode: "manual_review",
    scope: { kinds: ["agent"] },
    live: { enabled: true },
    nesting: {},
    referrals: {},
    revision: 7,
    trustVerificationPolicy: "operator-ans",
    lastCompleteSnapshotId: "00000000-0000-7000-8000-000000000003",
  };
  // Controlled source/observation projections qualify rendering, not peer ingestion.
  await page.route("**/_catalog/api/v1/federation/sources?**", (route) =>
    route.fulfill({ json: { items: [source] } }),
  );
  await page.route(
    `**/_catalog/api/v1/federation/sources/${sourceId}`,
    (route) => route.fulfill({ json: source }),
  );
  await page.route("**/_catalog/api/v1/observations?**", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: "00000000-0000-7000-8000-000000000002",
            sourceId,
            sourceKey: "helper",
            identifier,
            versionPresent: false,
            mediaType: "application/a2a-agent-card+json",
            completeness: "complete",
            freshness: "current",
            compatibility: {
              structural: "valid",
              support: "supported",
              profileId: "ard",
            },
            rawEntryAvailable: true,
            exactEntryDigest: `sha256:${"a".repeat(64)}`,
          },
        ],
      },
    }),
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, catalog, "local-source", "/federation-search");
  await page
    .getByRole("button", { name: "Choose configured sources", exact: true })
    .click();
  const choice = page.locator(".source-choice");
  const checkbox = choice.getByRole("checkbox");
  await expect(checkbox).toBeVisible();
  const geometry = await choice.evaluate((label) => {
    const control = label.querySelector("input")!;
    const copy = label.querySelector("span")!;
    const box = control.getBoundingClientRect();
    const range = document.createRange();
    range.selectNodeContents(copy);
    const line = range.getClientRects()[0]!;
    return {
      width: box.width,
      height: box.height,
      labelHeight: label.getBoundingClientRect().height,
      centerDifference: Math.abs(
        box.y + box.height / 2 - line.y - line.height / 2,
      ),
    };
  });
  expect(geometry.width).toBeGreaterThanOrEqual(16);
  expect(geometry.height).toBeLessThanOrEqual(24);
  expect(Math.abs(geometry.width - geometry.height)).toBeLessThanOrEqual(1);
  expect(geometry.centerDifference).toBeLessThanOrEqual(3);
  expect(geometry.labelHeight).toBeGreaterThanOrEqual(40);
  await tabTo(page, checkbox);
  await page.keyboard.press("Space");
  await expect(checkbox).toBeChecked();
  await expect(
    page.getByRole("textbox", {
      name: "Source IDs (optional)",
      exact: true,
    }),
  ).toHaveValue(sourceId);
  // Return to the original draft before leaving this screen.
  await page.keyboard.press("Space");
  await expect(checkbox).not.toBeChecked();
  await page.goto(`${catalog.origin}${catalog.basePath}/sources/${sourceId}`);
  await page
    .getByRole("button", { name: "ANS registration", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Choose indexed entry", exact: true })
    .click();
  const heading = page.getByRole("heading", { name: identifier, exact: true });
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(heading).toHaveText(identifier);
    await containedText(heading);
    await reflow(page, width);
  }
});

test("listing value actions align with their controls and retain literal multiline input", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items.find((entry) => entry.releaseId));
  await login(page, catalog, "local-admin", `/listings/${item.listingId}`);
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  const keep = page.getByRole("checkbox", {
    name: "Keep the current version revision",
    exact: true,
  });
  await expect(keep).toBeChecked();
  const glyph = await bounds(keep);
  expect(glyph.width).toBeGreaterThanOrEqual(16);
  expect(glyph.height).toBeLessThanOrEqual(24);
  expect(Math.abs(glyph.width - glyph.height)).toBeLessThanOrEqual(1);
  expect(
    await keep
      .locator("..")
      .evaluate((label) => label.getBoundingClientRect().height),
  ).toBeGreaterThanOrEqual(40);
  await tabTo(page, keep);
  await page.keyboard.press("Space");
  await expect(keep).not.toBeChecked();
  await page.keyboard.press("Space");
  await expect(keep).toBeChecked();
  const values = page.getByRole("group", { name: "Tags", exact: true });
  const field = values.getByRole("textbox", {
    name: "Tag 1",
    exact: true,
  });
  const row = values.locator(".value-row").filter({
    has: page.getByRole("textbox", { name: "Tag 1", exact: true }),
  });
  const remove = row.getByRole("button", {
    name: "Remove tag 1",
    exact: true,
  });
  const control = await bounds(field);
  const action = await bounds(remove);
  expect(action.y).toBeGreaterThanOrEqual(control.y - 2);
  expect(action.y + action.height).toBeLessThanOrEqual(
    control.y + control.height + 2,
  );
  const original = await field.inputValue();
  const literal = "alpha, beta\n  preserved whitespace  ";
  await field.fill(literal);
  await expect(field).toHaveValue(literal);
  await field.fill(original);
  await page.setViewportSize({ width: 390, height: 844 });
  const help = await bounds(row.locator(".field-hint").last());
  const stacked = await bounds(remove);
  expect(stacked.y - help.y - help.height).toBeGreaterThanOrEqual(0);
  expect(stacked.y - help.y - help.height).toBeLessThanOrEqual(16);
  await reflow(page, 390);
  const count = await values.getByRole("textbox").count();
  await values.getByRole("button", { name: "Add tag", exact: true }).click();
  const added = values.getByRole("textbox", {
    name: `Tag ${count + 1}`,
    exact: true,
  });
  await expect(added).toBeFocused();
  await values
    .getByRole("button", {
      name: `Remove tag ${count + 1}`,
      exact: true,
    })
    .click();
  await expect(values.getByRole("textbox")).toHaveCount(count);
  await expect(
    values.getByRole("textbox", { name: `Tag ${count}`, exact: true }),
  ).toBeFocused();
  await page.setViewportSize({ width: 701, height: 1000 });
  await doubleText(page);
  for (const width of [701, 601, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    if (width === 701) {
      await page.evaluate(() =>
        window.scrollTo(0, document.documentElement.scrollHeight / 2),
      );
      await page.getByLabel("Account", { exact: true }).focus();
      await page.keyboard.press("Tab");
      const firstLink = page
        .getByRole("navigation", { name: "Workspace", exact: true })
        .getByRole("link", { name: "Catalog", exact: true });
      await expect(firstLink).toBeFocused();
      await expect
        .poll(
          () =>
            firstLink.evaluate((link) => {
              const box = link.getBoundingClientRect();
              const painted = document.elementFromPoint(
                box.left + box.width / 2,
                box.top + box.height / 2,
              );
              return painted === link || link.contains(painted);
            }),
          {
            message:
              "Keyboard focus exposes the sidebar link below the sticky header",
          },
        )
        .toBe(true);
      const link = await bounds(firstLink);
      const header = await bounds(page.locator(".topbar"));
      expect(link.y).toBeGreaterThanOrEqual(header.y + header.height - 1);
      const violations = await page.evaluate(() => {
        const fits = (inner: DOMRect, outer: DOMRect) =>
          inner.left >= outer.left - 1 &&
          inner.right <= outer.right + 1 &&
          inner.top >= outer.top - 1 &&
          inner.bottom <= outer.bottom + 1;
        const header = document
          .querySelector(".topbar")!
          .getBoundingClientRect();
        const sidebar = document
          .querySelector(".sidebar")!
          .getBoundingClientRect();
        return {
          header: Array.from(
            document.querySelectorAll(".topbar > *,.header-actions > *"),
          )
            .filter(
              (node) =>
                node.getClientRects().length &&
                !fits(node.getBoundingClientRect(), header),
            )
            .map((node) => node.className),
          links: Array.from(document.querySelectorAll(".sidebar nav a"))
            .filter((node) => {
              const box = node.getBoundingClientRect();
              const lines = Array.from(node.childNodes)
                .filter(
                  (child) =>
                    child.nodeType === Node.TEXT_NODE &&
                    child.textContent?.trim(),
                )
                .flatMap((child) => {
                  const range = document.createRange();
                  range.selectNodeContents(child);
                  return Array.from(range.getClientRects());
                });
              return (
                box.right > sidebar.right + 1 ||
                lines.some((line) => !fits(line, box))
              );
            })
            .map((node) => node.textContent),
          groups: Array.from(document.querySelectorAll(".sidebar .nav-label"))
            .filter((node) => {
              const range = document.createRange();
              range.selectNodeContents(node);
              return !fits(
                range.getBoundingClientRect(),
                node.getBoundingClientRect(),
              );
            })
            .map((node) => node.textContent),
        };
      });
      expect(
        violations,
        "Header controls and sidebar text stay in their containers",
      ).toEqual({ header: [], links: [], groups: [] });
    }

    const editor = await field.evaluate((node) => {
      if (!(node instanceof HTMLTextAreaElement))
        throw new Error("Expected a value editor");
      const style = getComputedStyle(node);
      const canvas = document.createElement("canvas").getContext("2d")!;
      canvas.font = style.font;
      return {
        available:
          node.clientWidth -
          parseFloat(style.paddingLeft) -
          parseFloat(style.paddingRight),
        ordinaryValueWidth: canvas.measureText(node.value).width,
      };
    });
    expect(
      editor.available,
      "An ordinary short value stays usable beside its Remove action",
    ).toBeGreaterThanOrEqual(editor.ordinaryValueWidth);
    await reflow(page, width);
  }
});

test("density remains consistent across mobile detail widths and field boundaries remain visible in both themes", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items.find((entry) => entry.releaseId));
  await login(
    page,
    catalog,
    "local-admin",
    `/catalog/${required(item.releaseId)}`,
  );
  const record = page.locator(".full-record");
  await expect(record).toBeVisible();
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.endsWith("/api/v1/namespaces")
    )
      writes.push(request.method());
  });
  const paddings: number[] = [];
  for (const density of ["Compact", "Comfortable"]) {
    await appearance(page, density);
    const widths: number[] = [];
    for (const width of [701, 700]) {
      await page.setViewportSize({ width, height: 1000 });
      widths.push(
        await record.evaluate((node) =>
          parseFloat(getComputedStyle(node).paddingLeft),
        ),
      );
      await reflow(page, width);
    }
    expect(widths[0]).toBe(widths[1]);
    paddings.push(required(widths[0]));
  }
  expect(required(paddings[0])).toBeLessThan(required(paddings[1]));
  const agent = required(
    catalog.receipt.items.find((entry) => entry.key === "order-agent"),
  );
  const mcp = required(
    catalog.receipt.items.find((entry) => entry.key === "order-mcp"),
  );
  for (const theme of ["Light", "Dark"]) {
    await appearance(page, theme);
    for (const path of [
      "resources/new",
      `versions/new?resource=${agent.resourceId}`,
      `versions/new?resource=${mcp.resourceId}`,
      "namespaces/new",
    ]) {
      await page.goto(`${catalog.origin}${catalog.basePath}/${path}`);
      // Native checkboxes paint their own indicator, rather than a CSS text-field border.
      const controls = page
        .locator(
          'main .workspace-field :is(input:not([type="checkbox"]):not([type="radio"]), select, textarea)',
        )
        .filter({ visible: true });
      await expect(controls.first()).toBeVisible();
      const contrast = await controls.evaluateAll((nodes) => {
        function luminance(color: string) {
          const channels = color
            .match(/\d+(?:\.\d+)?/g)
            ?.slice(0, 3)
            .map((value) => {
              const channel = Number(value) / 255;
              return channel <= 0.04045
                ? channel / 12.92
                : ((channel + 0.055) / 1.055) ** 2.4;
            });
          if (!channels || channels.length !== 3)
            throw new Error("Expected an opaque rendered color");
          return channels.reduce(
            (sum, channel, index) =>
              sum + channel * ([0.2126, 0.7152, 0.0722][index] ?? 0),
            0,
          );
        }
        return nodes.map((node) => {
          const control = getComputedStyle(node);
          const panel = getComputedStyle(node.closest(".workspace-panel")!);
          const border = luminance(control.borderTopColor);
          const surrounding = luminance(panel.backgroundColor);
          return {
            label:
              node instanceof HTMLInputElement ||
              node instanceof HTMLTextAreaElement ||
              node instanceof HTMLSelectElement
                ? node.labels?.[0]?.textContent
                : "",
            width: parseFloat(control.borderTopWidth),
            ratio:
              (Math.max(border, surrounding) + 0.05) /
              (Math.min(border, surrounding) + 0.05),
          };
        });
      });
      for (const control of contrast) {
        expect(control.width, `${theme}: ${control.label}`).toBeGreaterThan(0);
        expect(
          control.ratio,
          `${theme}: ${control.label} separates from its panel`,
        ).toBeGreaterThanOrEqual(3);
      }
    }
    const name = page.getByRole("textbox", {
      name: "Display name",
      exact: true,
    });
    const slug = page.getByRole("textbox", { name: "Slug", exact: true });
    const description = page.getByRole("textbox", {
      name: "Description",
      exact: true,
    });
    const submit = page.getByRole("button", {
      name: "Create namespace",
      exact: true,
    });
    await tabTo(page, submit);
    await page.keyboard.press("Enter");
    await expect(name).toBeFocused();
    expect(await name.evaluate((node) => node.matches(":invalid"))).toBe(true);
    await visibleFocus(name);
    const before = await bounds(name);
    await name.fill("Prepared namespace");
    await slug.fill("prepared-namespace");
    await description.fill("Retained while the command is cancelled.");
    await submit.click();
    const dialog = page.getByRole("dialog", {
      name: "Create namespace",
      exact: true,
    });
    await expect(dialog).toBeVisible();
    for (const control of [name, slug, description]) {
      await expect(control).toBeDisabled();
      expect(
        await control.evaluate((node) => getComputedStyle(node).visibility),
      ).toBe("visible");
    }
    const disabled = await bounds(name);
    expect(disabled.width).toBe(before.width);
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(name).toBeEnabled();
    await expect(name).toHaveValue("Prepared namespace");
    await expect(slug).toHaveValue("prepared-namespace");
    await expect(description).toHaveValue(
      "Retained while the command is cancelled.",
    );
    await name.fill("");
    await slug.fill("");
    await description.fill("");
  }
  expect(writes).toEqual([]);
});

/** Include painted pseudo-elements without requiring a particular selection mechanism. */
async function paintedState(control: Locator) {
  return control.evaluate((node) =>
    [undefined, "::before", "::after"].map((pseudo) => {
      const style = getComputedStyle(node, pseudo);
      return {
        background: style.backgroundColor,
        color: style.color,
        border: style.border,
        outline: style.outline,
        shadow: style.boxShadow,
      };
    }),
  );
}

test("forced colors preserve painted selection separately from keyboard focus", async ({
  page,
  catalog,
}) => {
  await page.emulateMedia({ forcedColors: "active", reducedMotion: "reduce" });
  await login(page, catalog);
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  expect(
    await page.evaluate(() => matchMedia("(forced-colors: active)").matches),
  ).toBe(true);
  await page.mouse.move(0, 0);
  const account = page.getByLabel("Account", { exact: true });
  const controls = [
    page
      .getByRole("group", { name: "Resource kinds", exact: true })
      .getByRole("button", { name: "Agents", exact: true }),
    page.getByRole("button", { name: "Table", exact: true }),
    page.getByRole("button", { name: "Visibility: Tenant", exact: true }),
  ];
  for (const control of controls) {
    await expect(control).toHaveAttribute("aria-pressed", "false");
    await account.focus();
    const before = await paintedState(control);
    await tabTo(page, control);
    await page.keyboard.press("Enter");
    await expect(control).toHaveAttribute("aria-pressed", "true");
    await visibleFocus(control);
    await account.focus();
    expect(
      await paintedState(control),
      "Selection remains painted after focus leaves",
    ).not.toEqual(before);
  }
  await expect(
    page.getByRole("button", { name: "Cards", exact: true }),
  ).toHaveAttribute("aria-pressed", "false");
  await expect(
    page.getByRole("button", {
      name: "Visibility: Any visibility",
      exact: true,
    }),
  ).toHaveAttribute("aria-pressed", "false");
});

for (const base of ["root", "subpath"] as const) {
  for (const kind of ["agent", "mcp_server"] as const) {
    test(`${base}: ${kind} repeat actions align with native fields and wrap with readable spacing`, async ({
      page,
      catalog,
    }) => {
      let writes = 0;
      page.on("request", (request) => {
        if (
          request.method() === "POST" &&
          request.url().includes("/_catalog/api/v1/")
        )
          writes++;
      });
      await login(page, catalog, "local-contributor", "/resources/new");
      await page
        .getByRole("combobox", { name: "Namespace", exact: true })
        .selectOption(catalog.receipt.namespaceId);
      await page
        .getByRole("combobox", { name: "Kind", exact: true })
        .selectOption(kind);
      await page
        .getByLabel("Canonical resource identifier", { exact: true })
        .fill(
          `urn:air:catalog.example.com:spacing:${kind.replaceAll("_", "-")}-${base}`,
        );
      await page
        .getByRole("button", { name: "Next: Content", exact: true })
        .click();
      await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
      if (kind === "agent") {
        await fillAgentFields(page);
        await page
          .getByRole("button", { name: "Add input format", exact: true })
          .click();
        await page
          .getByRole("combobox", { name: "Input format 1", exact: true })
          .selectOption("custom");
        await page
          .getByLabel("Custom input format 1", { exact: true })
          .fill("application/vnd.orders+json");
        await page
          .getByRole("button", { name: "Add ability 1 tag", exact: true })
          .click();
        await page
          .getByLabel("Ability 1 tag 2", { exact: true })
          .fill("support");
      } else {
        await page
          .getByRole("button", { name: "Add endpoint", exact: true })
          .click();
        await expect(
          page.getByText(
            /^This input is not recognized as the selected native format/,
          ),
        ).toHaveCount(0);
        await page
          .getByLabel("MCP server name", { exact: true })
          .fill("com.example/documentation");
        await page
          .getByLabel("MCP description", { exact: true })
          .fill("Search documentation.");
        await page
          .getByLabel("Endpoint 1 URL", { exact: true })
          .fill("https://mcp.example.test/documentation");
        await openDisclosure(page, "Endpoint 1 protocol versions (optional)");
        await page
          .getByRole("button", {
            name: "Add endpoint 1 protocol version",
            exact: true,
          })
          .click();
        await page
          .getByLabel("Endpoint 1 protocol version 1", { exact: true })
          .selectOption("2025-11-25");
      }
      const rows = page.locator(
        kind === "agent" ? ".agent-value-row" : ".mcp-value-row",
      );
      for (const width of [1920, 1440, 700, 390, 320]) {
        await page.setViewportSize({ width, height: 1000 });
        await expectNativeActions(rows);
        await reflow(page, width);
      }
      if (kind === "mcp_server")
        await page
          .getByLabel("Endpoint 1 protocol version 1", { exact: true })
          .selectOption("__custom__");
      const value = page.getByLabel(
        kind === "agent"
          ? "Custom input format 1"
          : "Endpoint 1 custom protocol version 1",
        { exact: true },
      );
      const retained =
        kind === "mcp_server" ? "vendor-protocol" : await value.inputValue();
      await value.fill("");
      await value.press("Tab");
      await expect(value).toHaveAttribute("aria-invalid", "true");
      await appearance(page, "Dark");
      await doubleText(page);
      await expectNativeActions(rows);
      await reflow(page, 320);
      await value.fill(retained);
      await expect(value).not.toHaveAttribute("aria-invalid");
      if (kind === "agent") {
        await expect(
          page.getByLabel("Custom input format 1", { exact: true }),
        ).toHaveValue("application/vnd.orders+json");
        await page
          .getByRole("button", { name: "Remove input format 2", exact: true })
          .click();
        await expect(
          page.getByRole("combobox", { name: "Input format 1", exact: true }),
        ).toBeFocused();
        await expect(
          page.getByLabel("Ability 1 tag 2", { exact: true }),
        ).toHaveValue("support");
      } else {
        await page
          .getByRole("button", {
            name: "Remove endpoint 1 protocol version 1",
            exact: true,
          })
          .click();
        await expect(
          page.getByRole("button", {
            name: "Add endpoint 1 protocol version",
            exact: true,
          }),
        ).toBeFocused();
        await expect(
          page.getByLabel("Endpoint 1 URL", { exact: true }),
        ).toHaveValue("https://mcp.example.test/documentation");
      }
      expect(writes).toBe(0);
    });
  }
}
