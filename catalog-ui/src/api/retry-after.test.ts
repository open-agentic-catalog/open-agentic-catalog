import { describe, expect, it } from "vitest";
import { retryAfter } from "./retry-after";

const now = Date.UTC(2026, 8, 26, 12);
describe("bounded HTTP Retry-After advice", () => {
  it.each([
    ["1", 1000],
    ["0".repeat(127) + "1", 1000],
    ["00030", 30000],
    [" \t30\t ", 30000],
    ["86400", 86400000],
    ["Sat, 26 Sep 2026 12:00:30 GMT", 30000],
    ["Saturday, 26-Sep-26 12:00:30 GMT", 30000],
    ["Sat Sep 26 12:00:30 2026", 30000],
    ["Sat, 26 Sep 2026 12:00:60 GMT", 60000],
    ["Sun, 27 Sep 2026 12:00:00 GMT", 86400000],
  ])("accepts %s at its original UTC receipt time", (value, delay) => {
    expect(retryAfter(value, now)).toBe(now + delay);
  });
  it.each([
    null,
    "",
    " ",
    "0",
    "-1",
    "+1",
    "1.5",
    "1e2",
    "1, 2",
    "NaN",
    "Infinity",
    "9007199254740992",
    "86401",
    "0".repeat(128) + "1",
    "1\n",
    "1\r",
    "1\x00",
    "2026-09-26T12:00:30Z",
    "Sat, 26 Sep 2026 12:00:30 UTC",
    "sat, 26 Sep 2026 12:00:30 GMT",
    "Sat, 26 Sep 2026 12:00:30 GMT, 1",
    "Sat, 26 Sep 2026 12:00:00 GMT",
    "Sat, 26 Sep 2026 11:59:59 GMT",
    "Sun, 27 Sep 2026 12:00:01 GMT",
    "Sat, 31 Sep 2026 12:00:30 GMT",
    "Sat, 26 Xxx 2026 12:00:30 GMT",
    "Sat, 00 Sep 2026 12:00:30 GMT",
    "Sat, 26 Sep 2026 24:00:30 GMT",
    "Sat, 26 Sep 2026 12:60:30 GMT",
    "Sat, 26 Sep 2026 12:00:61 GMT",
    "Saturday, 26-Sep-76 12:00:30 GMT",
  ])("ignores invalid, expired or excessive advice: %s", (value) => {
    expect(retryAfter(value, now)).toBeUndefined();
  });
  it("accepts the single-digit asctime day and validates leap years", () => {
    expect(
      retryAfter("Sun Sep  6 12:00:30 2026", Date.UTC(2026, 8, 6, 12)),
    ).toBe(Date.UTC(2026, 8, 6, 12, 0, 30));
    expect(
      retryAfter("Tue, 29 Feb 2028 00:00:00 GMT", Date.UTC(2028, 1, 28)),
    ).toBe(Date.UTC(2028, 1, 29));
    expect(
      retryAfter("Sun, 29 Feb 2026 00:00:00 GMT", Date.UTC(2026, 1, 28)),
    ).toBeUndefined();
  });
  it("interprets RFC850 years more than 50 years ahead as the past century", () => {
    // A near-midnight current-century date remains usable; distant dates do not.
    expect(
      retryAfter(
        "Saturday, 01-Jan-00 00:00:00 GMT",
        Date.UTC(1999, 11, 31, 23, 59, 59),
      ),
    ).toBe(Date.UTC(2000, 0, 1));
    const old = Date.UTC(2076, 0, 1) - 1000;
    expect(retryAfter("Wednesday, 01-Jan-76 00:00:00 GMT", old)).toBe(
      old + 1000,
    );
    expect(
      retryAfter("Wednesday, 01-Jan-76 00:00:00 GMT", Date.UTC(2025, 11, 31)),
    ).toBeUndefined();
    expect(
      retryAfter("Wednesday, 01-Jan-76 00:00:00 GMT", Date.UTC(2026, 0, 1)),
    ).toBeUndefined(); // 50 years is still above the independent 24-hour cap.
  });
  it("normalizes a leap second through midnight and preserves subsecond receipt bounds", () => {
    expect(
      retryAfter(
        "Thu, 31 Dec 2026 23:59:60 GMT",
        Date.UTC(2026, 11, 31, 23, 59, 59),
      ),
    ).toBe(Date.UTC(2027, 0, 1));
    const fractionalReceipt = now + 500;
    expect(retryAfter("1", fractionalReceipt)).toBe(now + 1500);
    expect(retryAfter("86400", fractionalReceipt)).toBe(
      fractionalReceipt + 86400000,
    );
    expect(
      retryAfter("Sat, 26 Sep 2026 12:00:00 GMT", fractionalReceipt),
    ).toBeUndefined();
    expect(retryAfter("Sun, 27 Sep 2026 12:00:00 GMT", fractionalReceipt)).toBe(
      now + 86400000,
    );
    expect(
      retryAfter("Sun, 27 Sep 2026 12:00:01 GMT", fractionalReceipt),
    ).toBeUndefined();
  });
});
