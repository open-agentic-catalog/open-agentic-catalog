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
