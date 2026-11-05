// @vitest-environment jsdom
import { expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { components } from "../api/schema";
import { artworkIRI } from "./artwork";
import { listingRevision } from "../../tests/unit/responses";
import { screenLifecycle, stamp } from "../../tests/unit/screens";
import {
  ListingRevisionReview,
  listingRevisionChanges,
} from "./ListingRevisionReview";

screenLifecycle("/unit/listings");
type Input = components["schemas"]["CreateListingRevision"];
const base = {
  versionRevisionId: "version-1",
  displayName: "Saved title",
  description: "Saved description",
  publisherId: "publisher-1",
  visibility: "private",
} satisfies Input;
const savedBase = {
  ...listingRevision,
  digest: "sha256:listing",
  createdBy: "author",
  createdAt: stamp,
};
const saved = {
  ...savedBase,
  ...base,
  documentationUrl: "https://docs.example.test/old",
  supportUrl: "https://support.example.test/",
  termsUrl: "https://terms.example.test/",
  license: "Old license",
  tags: ["saved,tag"],
  categories: ["Saved category"],
  representativeQueries: ["Old query"],
  relationships: [{ type: "depends_on" as const, targetIdentifier: "urn:old" }],
  extensions: {
    opaque: { kept: "a,b 🙂", removed: true },
    artwork: { icon: "https://images.example.test/old.png" },
  },
};

it("compares every prepared field including removed links, changed references and opaque arrays", () => {
  const proposed: Input = {
    ...base,
    versionRevisionId: "version-2",
    publisherId: "publisher-2",
    displayName: "新しい, name 🙂",
    description: "First line\nSecond line",
    visibility: "public",
    tags: ["one,two", "🙂"],
    categories: ["新しい"],
    representativeQueries: ["a,b?", "🙂"],
    relationships: [],
    extensions: { opaque: { kept: "a,b 🙂", array: ["b", "a"] } },
  };
  const changes = listingRevisionChanges(saved, proposed);
  expect(changes.map((change) => change.field).sort()).toEqual(
    [
      "versionRevisionId",
      "publisherId",
      "displayName",
      "description",
      "visibility",
      "tags",
      "categories",
      "representativeQueries",
      "documentationUrl",
      "supportUrl",
      "termsUrl",
      "license",
      "relationships",
      "extensions",
    ].sort(),
  );
  expect(changes.find((change) => change.field === "tags")).toMatchObject({
    before: ["saved,tag"],
    after: ["one,two", "🙂"],
  });
  render(<ListingRevisionReview changes={changes} proposed={proposed} />);
  const review = screen.getByRole("region", {
    name: "Prepared listing changes",
  });
  expect(
    within(review).getByRole("region", { name: "Removed documentationUrl" })
      .textContent,
  ).toContain("Absent");
  expect(
    within(review).getByRole("region", { name: "Changed publisherId" })
      .textContent,
  ).toContain("publisher-1");
  expect(
    within(review).getByRole("region", { name: "Changed versionRevisionId" })
      .textContent,
  ).toContain("version-2");
  const disclosure = screen.getByText(
    "Review relationships, extensions and artwork",
  );
  expect(disclosure.closest("details")).not.toHaveProperty("open", true);
  fireEvent.click(disclosure);
  const extensions = within(review).getByRole("region", {
    name: "Changed extensions",
  });
  expect(extensions.textContent).toContain("images.example.test/old.png");
  expect(extensions.textContent).toContain("a,b 🙂");
  expect(extensions.textContent).toContain("removed");
  fireEvent.click(screen.getByText("Preview proposed listing metadata"));
  expect(
    screen.getByRole("heading", { name: proposed.displayName }),
  ).toBeTruthy();
  expect(screen.queryByText(/^Revision \d/)).toBeNull();
  expect(within(review).queryByRole("img")).toBeNull();
  expect(within(review).queryByRole("link")).toBeNull();
});

it("marks a first revision as additions and keeps unrelated server fields out", () => {
  const changes = listingRevisionChanges(null, base);
  expect(changes.every((change) => change.change === "Added")).toBe(true);
  expect(changes.some((change) => change.field === "versionRevisionId")).toBe(
    true,
  );
  expect(
    listingRevisionChanges(
      { ...savedBase, ...base },
      {
        ...base,
        tags: [],
        categories: [],
        representativeQueries: [],
        relationships: [],
        extensions: {},
      },
    ),
  ).toEqual([]);
});

it("reports advanced-only changes without hiding the unchanged basic metadata", () => {
  const proposed = { ...base, extensions: { opaque: ["b", "a"] } };
  render(
    <ListingRevisionReview
      changes={listingRevisionChanges({ ...savedBase, ...base }, proposed)}
      proposed={proposed}
    />,
  );
  expect(screen.getByText("No basic field changes.")).toBeTruthy();
  fireEvent.click(
    screen.getByText("Review relationships, extensions and artwork"),
  );
  expect(
    screen.getByRole("region", { name: "Changed extensions" }),
  ).toBeTruthy();
});

it("treats object key order as equal, preserves array order, and refuses excessively deep comparisons", () => {
  const before = {
    ...savedBase,
    ...base,
    extensions: { opaque: { b: 2, a: 1 }, array: ["a", "b"] },
  };
  expect(
    listingRevisionChanges(before, {
      ...base,
      extensions: { array: ["a", "b"], opaque: { a: 1, b: 2 } },
    }),
  ).toEqual([]);
  expect(
    listingRevisionChanges(before, {
      ...base,
      extensions: { array: ["b", "a"], opaque: { a: 1, b: 2 } },
    }),
  ).toMatchObject([{ field: "extensions", change: "Changed" }]);
  let deep: Record<string, unknown> = {};
  for (let i = 0; i < 70; i++) deep = { child: deep };
  expect(() =>
    listingRevisionChanges(null, { ...base, extensions: deep }),
  ).toThrow();
});

it("shows both no-change messages and keeps adversarial metadata inert", () => {
  const proposed = {
    ...base,
    displayName: "<script>hostile()</script>",
    extensions: { url: "javascript:hostile()" },
  };
  render(<ListingRevisionReview changes={[]} proposed={proposed} />);
  expect(screen.getByText("No basic field changes.")).toBeTruthy();
  fireEvent.click(
    screen.getByText("Review relationships, extensions and artwork"),
  );
  expect(
    screen.getByText("No relationship, extension or artwork changes."),
  ).toBeTruthy();
  fireEvent.click(screen.getByText("Preview proposed listing metadata"));
  expect(
    screen.getByRole("heading", { name: proposed.displayName }),
  ).toBeTruthy();
  expect(screen.queryByRole("link")).toBeNull();
});

it("reviews real artwork descriptor changes as inert saved evidence and proposed sealing input", () => {
  const before: components["schemas"]["ListingRevision"] = {
    ...savedBase,
    ...base,
    extensions: {
      [artworkIRI]: {
        schemaVersion: 1,
        icon: {
          url: "https://images.example.test/old.png",
          alt: "Old icon",
          rights: { basis: "owned", attribution: "Saved artist" },
          digest: "sha256:" + "a".repeat(64),
          mediaType: "image/png",
          size: 68,
          width: 1,
          height: 1,
          rightsAssessment: "not_assessed",
        },
      },
    },
  };
  const after: Input = {
    ...base,
    extensions: {
      [artworkIRI]: {
        schemaVersion: 1,
        icon: {
          url: "https://images.example.test/new.png",
          alt: "New icon 🙂",
          rights: { basis: "permission", attribution: "New artist" },
        },
      },
    },
  };
  const changes = listingRevisionChanges(before, after);
  expect(changes).toMatchObject([
    { field: "extensions", before: before.extensions, after: after.extensions },
  ]);
  render(<ListingRevisionReview changes={changes} proposed={after} />);
  fireEvent.click(
    screen.getByText("Review relationships, extensions and artwork"),
  );
  const change = screen.getByRole("region", { name: "Changed extensions" });
  for (const text of [
    "old.png",
    "new.png",
    "New icon 🙂",
    "Saved artist",
    "New artist",
    "not_assessed",
  ])
    expect(change.textContent).toContain(text);
  expect(screen.queryByRole("img")).toBeNull();
  expect(screen.queryByRole("link")).toBeNull();
});
