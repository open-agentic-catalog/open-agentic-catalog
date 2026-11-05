// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { PublicationContents } from "./PublicationContents";

afterEach(cleanup);
const registry = {
  identifier: "urn:air:catalog.example:registry:catalog",
  displayName: "Catalog Registry",
  type: "application/ai-registry+json",
  metadata: { catalogReleaseId: "registry:self" },
};
const profiles = {
  ardProfile: "ard-0.91-aa3e598",
  aiCatalogProfile: "ai-catalog-1.0-b062278",
};
const count = (name: string) =>
  screen.getByText(name).nextElementSibling?.textContent;

it.each(["ARD", "AI Catalog"])(
  "sorts the complete %s inventory before paging and keeps the other document independent",
  async (name) => {
    const entries = Array.from({ length: 28 }, (_, index) => ({
      identifier: `urn:example:entry-${28 - index}`,
      displayName: `Entry ${28 - index}`,
      type: index % 2 ? "application/json" : "text/plain",
      ...(index === 0 || index === 2
        ? {}
        : { version: index === 1 ? "" : `2.${28 - index}` }),
    }));
    const original = structuredClone(entries);
    render(
      <PublicationContents
        snapshot={{
          ...profiles,
          ard: { entries },
          aiCatalog: {
            specVersion: "1.0",
            host: { displayName: "Catalog" },
            entries,
          },
        }}
      />,
    );
    const inventory = within(
      screen.getByRole("region", { name: "Included discovery entries" }),
    );
    const ardToggle = inventory.getByText("Inspect ARD entries (28)");
    const aiToggle = inventory.getByText("Inspect AI Catalog entries (28)");
    const toggle = name === "ARD" ? ardToggle : aiToggle;
    fireEvent.click(ardToggle);
    fireEvent.click(aiToggle);
    const table = await inventory.findByRole<HTMLTableElement>("table", {
      name: `${name} included entries`,
    });
    const other = await inventory.findByRole<HTMLTableElement>("table", {
      name: `${name === "ARD" ? "AI Catalog" : "ARD"} included entries`,
    });
    // Confirm accessible cells once, then read native table order after each
    // change without repeating computed-style checks for every cell.
    expect(within(table).getAllByRole("cell")).toHaveLength(75);
    expect(within(other).getAllByRole("cell")).toHaveLength(75);
    const identifiers = (target = table) =>
      Array.from(target.rows)
        .slice(1)
        .map((row) => row.cells.item(0)?.textContent);
    const pager = within(
      inventory.getByRole("group", {
        name: `${name} included entries pagination`,
      }),
    );
    const select = (column: string, direction = "ascending") =>
      fireEvent.click(
        within(table).getByRole("button", {
          name: `Sort by ${column}, ${direction}`,
        }),
      );
    expect(identifiers()).toEqual(
      entries.slice(0, 25).map((entry) => entry.identifier),
    );
    fireEvent.click(pager.getByRole("button", { name: "Next page" }));
    expect(identifiers()).toHaveLength(3);
    select("identifier");
    expect(identifiers()).toEqual(
      Array.from({ length: 25 }, (_, i) => `urn:example:entry-${i + 1}`),
    );
    expect(pager.queryByRole("button", { name: "First page" })).toBeNull();
    expect(identifiers(other)).toEqual(
      entries.slice(0, 25).map((entry) => entry.identifier),
    );
    fireEvent.click(pager.getByRole("button", { name: "Next page" }));
    expect(identifiers()).toEqual([
      "urn:example:entry-26",
      "urn:example:entry-27",
      "urn:example:entry-28",
    ]);
    select("identifier", "descending");
    expect(identifiers()).toEqual(
      entries.slice(0, 25).map((entry) => entry.identifier),
    );
    select("version");
    expect(identifiers()).toEqual([
      "urn:example:entry-27",
      ...Array.from({ length: 24 }, (_, i) => `urn:example:entry-${i + 1}`),
    ]);
    expect(within(table).getByText("(empty)")).toBeTruthy();
    fireEvent.click(pager.getByRole("button", { name: "Next page" }));
    expect(identifiers()).toEqual([
      "urn:example:entry-25",
      "urn:example:entry-28",
      "urn:example:entry-26",
    ]);
    expect(within(table).getAllByText("No version supplied")).toHaveLength(2);
    select("version", "descending");
    expect(identifiers().slice(0, 4)).toEqual([
      "urn:example:entry-28",
      "urn:example:entry-26",
      "urn:example:entry-25",
      "urn:example:entry-24",
    ]);
    const json = Array.from(
      { length: 14 },
      (_, i) => `urn:example:entry-${27 - i * 2}`,
    );
    const text = Array.from(
      { length: 14 },
      (_, i) => `urn:example:entry-${28 - i * 2}`,
    );
    select("media type");
    expect(identifiers()).toEqual([...json, ...text].slice(0, 25));
    select("media type", "descending");
    expect(identifiers()).toEqual([...text, ...json].slice(0, 25));
    expect(
      within(table)
        .getByRole("columnheader", { name: "Media type" })
        .getAttribute("aria-sort"),
    ).toBe("descending");
    expect(identifiers(other)).toEqual(
      entries.slice(0, 25).map((entry) => entry.identifier),
    );
    fireEvent.click(pager.getByRole("button", { name: "Next page" }));
    expect(identifiers()).toEqual([
      "urn:example:entry-5",
      "urn:example:entry-3",
      "urn:example:entry-1",
    ]);
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(
        inventory.queryByRole("table", { name: `${name} included entries` }),
      ).toBeNull(),
    );
    expect(identifiers(other)).toEqual(
      entries.slice(0, 25).map((entry) => entry.identifier),
    );
    fireEvent.click(toggle);
    const reopened = await inventory.findByRole<HTMLTableElement>("table", {
      name: `${name} included entries`,
    });
    expect(identifiers(reopened)).toEqual([
      "urn:example:entry-5",
      "urn:example:entry-3",
      "urn:example:entry-1",
    ]);
    expect(
      within(reopened)
        .getByRole("columnheader", { name: "Media type" })
        .getAttribute("aria-sort"),
    ).toBe("descending");
    expect(
      within(
        inventory.getByRole("group", {
          name: `${name} included entries pagination`,
        }),
      ).getByRole("button", { name: "First page" }),
    ).toBeTruthy();
    expect(entries).toEqual(original);
  },
);

it("pages each retained document independently without rendering every row", async () => {
  const entries = Array.from({ length: 26 }, (_, index) => ({
    identifier: `urn:example:entry-${index}`,
    displayName: `Entry ${index}`,
    type: "application/json",
  }));
  render(
    <PublicationContents
      snapshot={{
        ...profiles,
        ard: { entries },
        aiCatalog: {
          specVersion: "1.0",
          host: { displayName: "Test Catalog" },
          entries,
        },
      }}
    />,
  );
  fireEvent.click(screen.getByText("Inspect ARD entries (26)"));
  fireEvent.click(screen.getByText("Inspect AI Catalog entries (26)"));
  const ard = await screen.findByRole("table", {
    name: "ARD included entries",
  });
  const ai = screen.getByRole("table", { name: "AI Catalog included entries" });
  expect(within(ard).getAllByRole("row")).toHaveLength(26);
  const pager = screen.getByRole("group", {
    name: "ARD included entries pagination",
  });
  fireEvent.click(within(pager).getByRole("button", { name: "Next page" }));
  expect(within(ard).getAllByRole("row")).toHaveLength(2);
  expect(within(ard).getByText("urn:example:entry-25")).toBeTruthy();
  expect(within(ai).getAllByRole("row")).toHaveLength(26);
  fireEvent.click(within(pager).getByRole("button", { name: "First page" }));
  expect(within(ard).getAllByRole("row")).toHaveLength(26);
  const aiPager = screen.getByRole("group", {
    name: "AI Catalog included entries pagination",
  });
  fireEvent.click(within(aiPager).getByRole("button", { name: "Next page" }));
  expect(within(ai).getAllByRole("row")).toHaveLength(2);
});

it("distinguishes an ARD Registry-only snapshot from zero resource entries", async () => {
  render(
    <PublicationContents
      snapshot={{
        ...profiles,
        ard: { entries: [registry] },
        aiCatalog: {
          specVersion: "1.0",
          host: { displayName: "Catalog" },
          entries: [],
        },
      }}
    />,
  );
  expect(count("ARD Registry self-entries")).toBe("1");
  expect(count("ARD resource entries")).toBe("0");
  expect(count("AI Catalog resource entries")).toBe("0");
  expect(screen.queryByRole("table")).toBeNull();
  fireEvent.click(screen.getByText("Inspect ARD entries (1)"));
  const ard = await screen.findByRole("table", {
    name: "ARD included entries",
  });
  expect(within(ard).getByText(registry.identifier)).toBeTruthy();
  expect(within(ard).getByText("No version supplied")).toBeTruthy();
  fireEvent.click(screen.getByText("Inspect AI Catalog entries (0)"));
  await screen.findByText("No AI Catalog entries in this document.");
  fireEvent.click(screen.getByText("Inspect ARD entries (1)"));
  await waitFor(() =>
    expect(
      screen.queryByRole("table", { name: "ARD included entries" }),
    ).toBeNull(),
  );
});

it("shows exact identifier/version rows and differing profile populations without uniqueness claims", async () => {
  const identifier =
    "urn:air:example.com:agents:" + "long-identity-".repeat(30);
  const entry = {
    identifier,
    displayName: "Two versions",
    type: "application/a2a+json",
  };
  render(
    <PublicationContents
      snapshot={{
        ...profiles,
        ard: {
          entries: [
            registry,
            { ...entry, version: "1.0" },
            { ...entry, version: "2.0" },
          ],
        },
        aiCatalog: {
          specVersion: "1.0",
          host: { displayName: "Catalog" },
          entries: [
            { ...entry, version: "" },
            {
              identifier: "urn:air:example.com:tenant:only-ai",
              type: "application/a2a+json",
              version: "3.0",
            },
          ],
        },
      }}
    />,
  );
  expect(count("ARD resource entries")).toBe("2");
  expect(count("AI Catalog resource entries")).toBe("2");
  expect(screen.getByText(/not unique Resources/)).toBeTruthy();
  fireEvent.click(screen.getByText("Inspect ARD entries (3)"));
  fireEvent.click(screen.getByText("Inspect AI Catalog entries (2)"));
  const ard = await screen.findByRole("table", {
    name: "ARD included entries",
  });
  expect(within(ard).getAllByText(identifier)).toHaveLength(2);
  expect(within(ard).getByText("1.0")).toBeTruthy();
  expect(within(ard).getByText("2.0")).toBeTruthy();
  const ai = screen.getByRole("table", { name: "AI Catalog included entries" });
  expect(within(ai).getByText("(empty)")).toBeTruthy();
  expect(within(ai).getByText("3.0")).toBeTruthy();
});
