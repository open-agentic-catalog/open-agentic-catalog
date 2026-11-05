const months = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(" ");
const weekday = "(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)";
const clock = "(\\d{2}):(\\d{2}):(\\d{2})";
const imf = new RegExp(
  `^${weekday}, (\\d{2}) ([A-Z][a-z]{2}) (\\d{4}) ${clock} GMT$`,
);
const obsolete = new RegExp(
  `^(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday), (\\d{2})-([A-Z][a-z]{2})-(\\d{2}) ${clock} GMT$`,
);
const asctime = new RegExp(
  `^${weekday} ([A-Z][a-z]{2}) ( \\d|\\d{2}) ${clock} (\\d{4})$`,
);

/** Parse the three HTTP-date receive forms explicitly in UTC, without accepting
 * JavaScript's extra date formats or calendar overflow. */
function httpDate(value: string, receivedAt: number): number | undefined {
  // Each reviewed expression has exactly six mandatory captures on a match.
  type DateFields = [string, string, string, string, string, string, string];
  let fields = imf.exec(value) as DateFields | null;
  let shortYear = false;
  if (!fields) {
    fields = obsolete.exec(value) as DateFields | null;
    shortYear = fields !== null;
  }
  if (!fields) {
    const old = asctime.exec(value) as DateFields | null;
    if (!old) return undefined;
    fields = [old[0], old[2], old[1], old[6], old[3], old[4], old[5]];
  }
  const day = Number(fields[1]);
  const month = months.indexOf(fields[2]);
  let year = Number(fields[3]);
  const hour = Number(fields[4]);
  const minute = Number(fields[5]);
  const second = Number(fields[6]);
  if (month < 0 || day < 1 || hour > 23 || minute > 59 || second > 60)
    return undefined;
  const date = new Date(0);
  if (shortYear) {
    const horizon = new Date(receivedAt);
    horizon.setUTCFullYear(horizon.getUTCFullYear() + 50);
    year += Math.floor((horizon.getUTCFullYear() - year) / 100) * 100;
    date.setUTCFullYear(year, month, day);
    date.setUTCHours(hour, minute, second);
    if (date.getTime() > horizon.getTime()) year -= 100;
  }
  date.setUTCFullYear(year, month, day);
  date.setUTCHours(hour, minute, Math.min(second, 59), 0);
  if (date.getUTCMonth() !== month || date.getUTCDate() !== day)
    return undefined;
  // Accept the HTTP leap-second spelling without needing a leap-second table.
  return date.getTime() + (second === 60 ? 1000 : 0);
}

/** A bounded advisory time, never an instruction to schedule another request. */
export function retryAfter(
  value: string | null,
  receivedAt: number,
): number | undefined {
  if (value === null || value.length > 128) return undefined;
  const text = value.replace(/^[ \t]+|[ \t]+$/g, "");
  const when = /^\d+$/.test(text)
    ? receivedAt + Number(text) * 1000
    : httpDate(text, receivedAt);
  return when !== undefined &&
    Number.isSafeInteger(when) &&
    when > receivedAt &&
    when - receivedAt <= 86_400_000
    ? when
    : undefined;
}
