import { expect, test } from "vitest";
import { PersonalInput, PersonalItem, PersonalResolution } from "./responses";
import { personalFacts } from "./PersonalSummary";

const id = "00000000-0000-7000-8000-000000000001";
const follow = { type: "follow" as const, label: "My watch", listingId: id };
const observed = { releaseId: id, revision: 2 };
test("only the explicit follow command carries the displayed release and revision", () => {
  expect(PersonalInput(follow)).toBe(true);
  expect(PersonalInput({ ...follow, observed })).toBe(true);
  expect(PersonalInput({ ...follow, listingId: "some-listing" })).toBe(false);
  expect(
    PersonalInput({ ...follow, observed: { ...observed, revision: 0 } }),
  ).toBe(false);
  expect(PersonalInput({ ...follow, search: { text: "mixed" } })).toBe(false);
  const item = {
    ...follow,
    id,
    revision: 1,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };
  expect(PersonalItem(item)).toBe(true);
  expect(PersonalItem({ ...item, observed })).toBe(false);
  expect(PersonalItem({ ...item, acknowledged: observed })).toBe(false);
  expect(personalFacts(follow)).toContainEqual({
    label: "Listing ID",
    value: id,
  });
});
test("an unavailable follow never renders a change or cached target", () => {
  expect(PersonalResolution({ status: "unavailable" })).toBe(true);
  for (const change of ["changed", "unchanged", "unacknowledged"]) {
    expect(
      PersonalResolution({ status: "available", current: observed, change }),
    ).toBe(true);
    expect(PersonalResolution({ status: "unavailable", change })).toBe(false);
  }
  for (const bad of [
    { status: "unavailable", current: observed },
    { status: "available", current: observed },
    { status: "available", current: observed, change: ["changed"] },
    {
      status: "available",
      current: { ...observed, releaseId: "some-release" },
      change: "changed",
    },
    {
      status: "available",
      current: { ...observed, label: "unexpected" },
      change: "changed",
    },
  ])
    expect(PersonalResolution(bad)).toBe(false);
});
