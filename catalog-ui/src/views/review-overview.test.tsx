// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import {
  cleanup,
  render,
  screen,
  within,
  fireEvent,
} from "@testing-library/react";
import { workspace, reply } from "../../tests/unit/fixtures";
import {
  reviewFixture,
  publicationFixture,
} from "../../tests/unit/overview-fixtures";
import ReviewOverview from "./ReviewOverview";
import PublicationOverview from "./PublicationOverview";

afterEach(cleanup);

it("uses one zero-based scale for unequal daily series", async () => {
  const f = workspace(),
    value = reviewFixture();
  value.decisions.value = 1;
  value.days[29]!.changesRequired = 0;
  value.days[29]!.rejected = 0;
  f.get.mockResolvedValue(reply(value));
  render(<ReviewOverview {...f.props} />);
  const chart = await screen.findByRole("img");
  expect(
    chart
      .querySelectorAll(".review-submitted-bar")
      .item(29)
      .getAttribute("height"),
  ).toBe("120");
  expect(
    chart
      .querySelectorAll(".review-decided-bar")
      .item(29)
      .getAttribute("height"),
  ).toBe("40");
  expect(chart.querySelector("line")?.getAttribute("y1")).toBe("130");
});

it("places each day's bars and date label in the same time slot", async () => {
  const f = workspace();
  f.get.mockResolvedValue(reply(reviewFixture()));
  render(<ReviewOverview {...f.props} />);
  const chart = await screen.findByRole("img");
  const submitted = chart.querySelectorAll(".review-submitted-bar");
  const decided = chart.querySelectorAll(".review-decided-bar");
  for (let index = 0; index < 30; index++) {
    const left = Number(submitted[index]!.getAttribute("x"));
    const right = Number(decided[index]!.getAttribute("x")) + 8;
    expect((left + right) / 2).toBe((index + 0.5) * 25);
    expect(left).toBeGreaterThan(index * 25);
    expect(right).toBeLessThan((index + 1) * 25);
  }
  const ticks = document.querySelectorAll<HTMLTimeElement>(
    ".review-date-axis time",
  );
  expect([...ticks].map((tick) => tick.dateTime)).toEqual([
    "2026-09-26",
    "2026-10-03",
    "2026-10-10",
    "2026-10-17",
    "2026-10-25",
  ]);
  for (const [tick, index] of [...ticks].map(
    (tick, ordinal) => [tick, [0, 7, 14, 21, 29][ordinal]!] as const,
  ))
    expect(parseFloat(tick.style.left)).toBeCloseTo(((index + 0.5) / 30) * 100);
});

it("shows review counts, exact queue links, a common chart scale and complete tables", async () => {
  const f = workspace();
  f.get.mockResolvedValue(reply(reviewFixture()));
  render(<ReviewOverview {...f.props} />);
  expect(await screen.findByText("Median review time")).toBeTruthy();
  expect(
    screen
      .getByRole("link", {
        name: "Approved, unpublished",
      })
      .getAttribute("href"),
  ).toBe("/unit/reviews?state=approved&unreleased=true");
  expect(
    screen.getByRole("link", { name: "Awaiting review" }).getAttribute("href"),
  ).toBe("/unit/reviews?state=submitted");
  const chart = screen.getByRole("img");
  expect(chart.getAttribute("viewBox")).toBe("0 0 750 140");
  expect(chart.querySelectorAll("rect")).toHaveLength(60);
  expect(chart.querySelectorAll(".review-submitted-bar")).toHaveLength(30);
  expect(screen.queryByRole("table")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Daily breakdown" }));
  expect(screen.queryByRole("img")).toBeNull();
  expect(
    screen
      .getByRole("table", {
        name: "Daily submissions and decision outcomes in UTC",
      })
      .querySelectorAll("tbody tr"),
  ).toHaveLength(30);
  const age = screen.getByRole("region", { name: "Waiting time" });
  expect(within(age).getAllByRole("listitem")).toHaveLength(3);
  expect(within(age).queryByRole("table")).toBeNull();
  expect(
    Array.from(
      age.querySelectorAll<HTMLElement>(".overview-track span"),
      (span) => span.style.width,
    ),
  ).toEqual(["100%", "100%", "0%"]);
  fireEvent.click(screen.getByRole("button", { name: "Chart" }));
  expect(screen.queryByRole("table")).toBeNull();
  expect(screen.getByRole("img")).toBeTruthy();
  expect(f.get).toHaveBeenCalledTimes(1);
});

it.each(["submissions", "decisions", "pending"] as const)(
  "omits incomplete %s distributions without hiding complete independent measures",
  async (limited) => {
    const f = workspace(),
      value = reviewFixture();
    if (limited === "submissions") {
      value.submissions = { value: 10000, limited: true };
      for (const day of value.days) delete day.submitted;
    }
    if (limited === "decisions") {
      value.decisions = { value: 10000, limited: true };
      delete value.medianSeconds;
      value.medianUnavailable = "limited";
      for (const day of value.days) {
        delete day.approved;
        delete day.changesRequired;
        delete day.rejected;
      }
    }
    if (limited === "pending") {
      value.pending = { value: 10000, limited: true };
      delete value.pendingAge;
      value.pendingAgeUnavailable = "limited";
    }
    f.get.mockResolvedValue(reply(value));
    render(<ReviewOverview {...f.props} />);
    await screen.findByText("Oldest review:", { exact: false });
    if (limited !== "pending") {
      expect(screen.queryByRole("img")).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Daily breakdown" }));
      expect(screen.getAllByText("Unavailable").length).toBeGreaterThan(0);
    } else
      expect(
        screen.getByText(/Age breakdown unavailable: more than 10,000/),
      ).toBeTruthy();
    if (limited === "submissions")
      expect(screen.getByText("Median review time")).toBeTruthy();
    if (limited === "decisions")
      expect(
        screen.getByText(/Review time unavailable: more than 10,000/),
      ).toBeTruthy();
  },
);

it("reports inconsistent durations instead of replacing them with zero", async () => {
  const f = workspace(),
    value = reviewFixture();
  delete value.pendingAge;
  value.pendingAgeUnavailable = "invalid_timestamps";
  delete value.medianSeconds;
  value.medianUnavailable = "invalid_timestamps";
  f.get.mockResolvedValue(reply(value));
  render(<ReviewOverview {...f.props} />);
  await screen.findByText(/Review time unavailable: inconsistent timestamps/);
  expect(
    screen.getByText(/Age breakdown unavailable: inconsistent timestamps/),
  ).toBeTruthy();
});

it("shows complete empty populations and zero-duration decisions without inventing trends", async () => {
  const f = workspace(),
    value = reviewFixture();
  value.pending = { value: 0, limited: false };
  delete value.oldestPendingAt;
  value.pendingAge = { under24Hours: 0, under7Days: 0, atLeast7Days: 0 };
  value.submissions = { value: 0, limited: false };
  value.decisions = { value: 0, limited: false };
  delete value.medianSeconds;
  for (const day of value.days) {
    day.submitted = 0;
    day.approved = 0;
    day.changesRequired = 0;
    day.rejected = 0;
  }
  f.get.mockResolvedValue(reply(value));
  const view = render(<ReviewOverview {...f.props} />);
  await screen.findByText("No decisions in this period.");
  expect(screen.queryByRole("img")).toBeNull();
  expect(screen.queryByRole("region", { name: "Waiting time" })).toBeNull();
  view.unmount();
  value.decisions.value = 1;
  value.days[29]!.approved = 1;
  value.medianSeconds = 0;
  render(<ReviewOverview {...f.props} />);
  expect(await screen.findByText("Less than 1 second")).toBeTruthy();
});

it("keeps private read failures explicit and supports a fresh retry", async () => {
  const f = workspace();
  f.get
    .mockRejectedValueOnce(new Error("controlled failure"))
    .mockResolvedValue(reply(reviewFixture()));
  render(<ReviewOverview {...f.props} />);
  await screen.findByRole("alert");
  expect(screen.queryByRole("img")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("img");
  expect(f.get).toHaveBeenCalledTimes(2);
});

it("shows host modes, recorded metadata and known mismatches without a health claim", async () => {
  const f = workspace();
  f.get.mockResolvedValue(reply(publicationFixture()));
  render(<PublicationOverview {...f.props} />);
  const table = await screen.findByRole("table");
  expect(table.querySelectorAll("tbody tr")).toHaveLength(3);
  expect(screen.getByText("Generation 3")).toBeTruthy();
  expect(
    screen.getByText(
      "Audience differs; ARD profile differs; AI Catalog profile differs",
    ),
  ).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "catalog.example" }).getAttribute("href"),
  ).toBe("/unit/publication?host=catalog.example&audience=authenticated");
  expect(screen.queryByRole("link", { name: "disabled.example" })).toBeNull();
  expect(screen.getAllByText("Disabled").length).toBeGreaterThan(0);
  expect(screen.queryByText(/does not confirm document validity/)).toBeNull();
  expect(screen.getByText("Published: Public")).toBeTruthy();
});

it("handles empty host inventories, absent activation times and failed reads", async () => {
  const f = workspace(),
    value = publicationFixture();
  value.hosts = [];
  f.get.mockResolvedValue(reply(value));
  const view = render(<PublicationOverview {...f.props} />);
  await screen.findByText(/No publication hosts are configured/);
  view.unmount();
  const next = publicationFixture();
  delete next.hosts[2]!.active!.activatedAt;
  next.hosts[2]!.mismatches = [];
  next.hosts[2]!.mode = "anonymous";
  f.get.mockResolvedValue(reply(next));
  const second = render(<PublicationOverview {...f.props} />);
  await screen.findByText("Generation 3");
  second.unmount();
  f.get.mockRejectedValue(new Error("controlled host read"));
  render(<PublicationOverview {...f.props} />);
  await screen.findByRole("alert");
  expect(screen.queryByRole("table")).toBeNull();
});
