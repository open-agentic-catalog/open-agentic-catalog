import { expect, it } from "vitest";
import { reviewFilters, reviewQueueURL } from "./review-queue-state";

it.each([
  ["", { listingId: "", state: "submitted" }],
  ["?listing=listing-a", { listingId: "listing-a", state: "" }],
  ["?state=", { listingId: "", state: "" }],
  [
    "?listing=listing-a&state=approved",
    { listingId: "listing-a", state: "approved" },
  ],
  ["?state=APPROVED", { listingId: "", state: "APPROVED" }],
  [
    "?listing=&cursor=private&other=value",
    { listingId: "", state: "submitted" },
  ],
] as const)(
  "reads only applied review criteria from %s",
  (search, expected) => {
    expect(reviewFilters(search)).toEqual(expected);
  },
);

it("writes exact queue criteria without persisting a cursor or a draft", () => {
  expect(
    reviewQueueURL("/unit", { listingId: "listing:a", state: "approved" }),
  ).toBe("/unit/reviews?state=approved&listing=listing%3Aa");
  expect(reviewQueueURL("", { listingId: "", state: "" })).toBe(
    "/reviews?state=",
  );
});

it.each(["true", "false", "", "other"])(
  "retains the unreleased URL criterion %j for explicit API validation",
  (unreleased) => {
    expect(reviewFilters(`?state=approved&unreleased=${unreleased}`)).toEqual({
      listingId: "",
      state: "approved",
      unreleased,
    });
    expect(
      reviewQueueURL("/unit", { listingId: "", state: "approved", unreleased }),
    ).toBe(`/unit/reviews?state=approved&unreleased=${unreleased}`);
  },
);

it("does not silently widen duplicate unreleased criteria", () => {
  expect(
    reviewFilters("?state=approved&unreleased=true&unreleased=false")
      .unreleased,
  ).toBe("unsupported");
});

it("round-trips only the applied exact namespace, preserving invalid URLs for refusal", () => {
  const namespaceId = "aaaaaaaa-0000-7000-8000-000000000010";
  expect(reviewFilters(`?namespace=${namespaceId}&state=approved`)).toEqual({
    listingId: "",
    state: "approved",
    namespaceId,
  });
  expect(
    reviewQueueURL("/unit", {
      listingId: "listing-a",
      state: "approved",
      namespaceId,
      unreleased: "true",
    }),
  ).toBe(
    `/unit/reviews?state=approved&unreleased=true&listing=listing-a&namespace=${namespaceId}`,
  );
  for (const value of ["", "bad", "unsupported"]) {
    expect(reviewFilters(`?namespace=${value}`).namespaceId).toBe(value);
  }
  expect(
    reviewFilters(`?namespace=${namespaceId}&namespace=${namespaceId}`)
      .namespaceId,
  ).toBe("unsupported");
  expect(reviewFilters(`?namespace=${namespaceId}&cursor=private`).state).toBe(
    "submitted",
  );
});
