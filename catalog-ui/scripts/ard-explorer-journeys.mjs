// Native UI assertions use independent sealed publication and fixture evidence.
import { expect } from "@playwright/test";

export async function browse(page, origin, fixture, exchanges, count) {
  expect(page.url()).toBe(origin);
  const outcomes = [];
  const combobox = page.getByRole("combobox").first();
  const connect = async (target) => {
    const before = exchanges.length;
    await combobox.fill(target);
    await page.getByRole("button", { name: "Connect", exact: true }).click();
    await expect
      .poll(
        () =>
          exchanges
            .slice(before)
            .filter((x) => x.url.endsWith("/agents?pageSize=1")).length,
      )
      .toBe(1);
    return exchanges.slice(before);
  };
  const tab = async (name) =>
    page.getByRole("tab", { name, exact: true }).click();
  const submit = async (button, path) => {
    const before = exchanges.length;
    await page.getByRole("button", { name: button, exact: true }).click();
    await expect
      .poll(
        () =>
          exchanges
            .slice(before)
            .filter((x) => new URL(x.url).pathname.endsWith(path)).length,
      )
      .toBe(1);
    return exchanges
      .slice(before)
      .find((x) => new URL(x.url).pathname.endsWith(path));
  };
  const search = async (text) => {
    await page
      .getByRole("textbox", { name: "query.text (required)", exact: true })
      .fill(text);
    return submit("Search", "/search");
  };
  const passed = (name, disposition, evidence) =>
    outcomes.push({ name, disposition, status: "pass", evidence });
  const real = fixture.catalog;
  const expected = fixture.entries;
  const ids = fixture.resources.map((x) => x.identifier);
  const firstConnect = await connect(real + "/.well-known/ard.json");
  const advertised = fixture.manifest.entries.find(
    (entry) => entry.type === "application/ai-registry+json",
  ).url;
  await expect(
    page.getByText(`Connected to ${advertised}`, { exact: true }),
  ).toBeVisible();
  expect(firstConnect[0].url).toBe(real + "/.well-known/ard.json");
  expect(firstConnect.every((x) => x.native.status === 200)).toBe(true);
  passed("manifest-connect", "supported", { requests: firstConnect.length });
  const direct = await connect(real + "/ard/v1");
  expect(direct).toHaveLength(3);
  expect(direct.every((x) => x.native.status === 200)).toBe(true);
  passed("registry-connect", "supported", { requests: 3 });
  await tab("Search");
  await page
    .getByRole("spinbutton", { name: "pageSize", exact: true })
    .fill("1");
  const pages = [];
  let exchange = await search("explorer");
  for (let index = 0; index < 3; index += 1) {
    expect(exchange.native.status).toBe(200);
    const body = JSON.parse(exchange.native.body);
    expect(body.results).toHaveLength(1);
    const result = body.results[0];
    expect(ids).toContain(result.identifier);
    const sealed = expected.find((x) => x.identifier === result.identifier);
    for (const [key, value] of Object.entries(sealed))
      expect(result[key]).toEqual(value);
    expect(
      Number.isInteger(result.score) &&
        result.score >= 0 &&
        result.score <= 100,
    ).toBe(true);
    expect(result.source).toBe(real + "/ard/v1/");
    await expect(
      page.getByText(result.identifier, { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(sealed.displayName, { exact: true }),
    ).toBeVisible();
    await expect(page.getByText(result.type, { exact: true })).toBeVisible();
    await expect(
      page.getByText(`score ${result.score}`, { exact: true }),
    ).toBeVisible();
    await expect(page.getByText(result.source, { exact: true })).toBeVisible();
    pages.push({
      identifier: result.identifier,
      score: result.score,
      token: body.pageToken ?? null,
    });
    if (index === 0)
      await page.screenshot({
        path: "/state/catalog-search.png",
        fullPage: true,
      });
    const next = page.getByRole("button", { name: "Next page", exact: false });
    if (index < 2) {
      expect(typeof body.pageToken).toBe("string");
      exchange = await submit(await next.innerText(), "/search");
    } else {
      expect(body.pageToken ?? null).toBeNull();
      await expect(next).toBeDisabled();
    }
  }
  expect(new Set(pages.map((x) => x.identifier)).size).toBe(3);
  expect(new Set(pages.slice(0, 2).map((x) => x.token)).size).toBe(2);
  passed("search-pagination", "supported", { pages });
  // Retain a real first-page token, then change its bound filter through the UI.
  const tokenPage = await search("explorer");
  const token = JSON.parse(tokenPage.native.body).pageToken;
  expect(typeof token).toBe("string");
  await page
    .getByRole("textbox", { name: "Filter 1 key", exact: true })
    .fill("type");
  const type = "application/mcp-server-card+json";
  await page
    .getByRole("textbox", { name: "Filter 1 values", exact: true })
    .fill(type);
  const refused = await submit(
    await page
      .getByRole("button", { name: "Next page", exact: false })
      .innerText(),
    "/search",
  );
  expect(JSON.parse(refused.body).pageToken).toBe(token);
  expect(refused.native.status).toBe(400);
  await expect(page.getByText(/^HTTP 400/)).toBeVisible();
  passed("changed-filter-token", "supported", { status: 400 });
  const matched = await search("explorer");
  const mcp = fixture.resources.find((x) => x.kind === "mcp_server");
  expect(
    JSON.parse(matched.native.body).results.map((x) => x.identifier),
  ).toEqual([mcp.identifier]);
  await expect(page.getByText(mcp.identifier, { exact: true })).toBeVisible();
  passed("filter-match", "supported", { identifier: mcp.identifier });
  await page
    .getByRole("textbox", { name: "Filter 1 values", exact: true })
    .fill("application/x-no-match");
  const empty = await search("explorer");
  expect(JSON.parse(empty.native.body).results).toEqual([]);
  await expect(
    page.getByText("No direct results matched this query.", { exact: true }),
  ).toBeVisible();
  passed("filter-empty", "supported", { results: 0 });
  await tab("Explore");
  await page
    .getByRole("textbox", { name: "query.text (optional)", exact: true })
    .fill("explorer");
  await page.getByRole("textbox", { name: /facet fields/ }).fill("type");
  const explored = await submit("Explore", "/explore");
  const buckets = JSON.parse(explored.native.body).facets.type.buckets;
  expect(buckets).toHaveLength(3);
  expect(buckets.every((x) => x.count === 1)).toBe(true);
  expect(new Set(buckets.map((x) => x.value))).toEqual(
    new Set(expected.map((x) => x.type)),
  );
  for (const bucket of buckets)
    await expect(page.getByText(bucket.value, { exact: true })).toBeVisible();
  await expect(page.getByText("1", { exact: true })).toHaveCount(3);
  passed("explore-counts", "supported", { buckets });
  await tab("Agents");
  await page
    .getByRole("spinbutton", { name: "pageSize", exact: true })
    .fill("1");
  const listed = await submit("List", "/agents");
  const list = JSON.parse(listed.native.body);
  expect(list.items).toHaveLength(1);
  expect(fixture.manifest.entries.map((x) => x.identifier)).toContain(
    list.items[0].identifier,
  );
  await expect(
    page.getByText(list.items[0].identifier, { exact: true }),
  ).toBeVisible();
  passed("list-first-page", "supported", {
    identifier: list.items[0].identifier,
  });
  expect(typeof list.pageToken).toBe("string");
  expect(await page.getByRole("button", { name: /Next page/ }).count()).toBe(0);
  passed("list-continuation", "client-limitation", {
    receivedToken: true,
    nextControl: false,
  });
  await page.screenshot({ path: "/state/catalog-list.png", fullPage: true });
  // The second host belongs to the same owned Catalog but requires authentication.
  const privateCalls = await connect("http://localhost:8080/ard/v1");
  expect(privateCalls).toHaveLength(3);
  expect(
    privateCalls.every((x) => x.native.status === 401 && !x.authorization),
  ).toBe(true);
  await expect(page.getByText(/POST \/search did not answer/)).toBeVisible();
  passed("private-authentication", "client-limitation", {
    statuses: privateCalls.map((x) => x.native.status),
    authorization: false,
  });
  await connect(fixture.origin);
  await tab("Search");
  for (const [query, label] of [
    ["sparse-unfamiliar", "sparse-unfamiliar"],
    ["malformed-envelope", "malformed-envelope"],
    ["http-400", "http-400"],
    ["http-401", "http-401"],
    ["http-503", "http-503"],
    ["requires-context", "query-context"],
    ["referral", "full-referral-connect"],
  ]) {
    const item = await search(query);
    const body = JSON.parse(item.native.body);
    if (query === "sparse-unfamiliar") {
      expect(body.results).toHaveLength(3);
      for (const result of body.results)
        await expect(
          page.getByText(result.identifier, { exact: true }),
        ).toBeVisible();
      await expect(
        page.getByText("Unfamiliar media type", { exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText("Valid neighbor", { exact: true }),
      ).toBeVisible();
      passed(label, "supported", { results: 3 });
    } else if (query === "malformed-envelope") {
      expect(item.native.status).toBe(200);
      await expect(
        page.getByText(/Response has no 'results' array/),
      ).toBeVisible();
      passed(label, "supported", { status: 200, rejectedEnvelope: true });
    } else if (query === "requires-context") {
      expect(JSON.parse(item.body).queryContext).toBeUndefined();
      expect(item.native.status).toBe(400);
      await expect(page.getByText(/^HTTP 400/)).toBeVisible();
      passed(label, "client-limitation", { status: 400, queryContext: false });
    } else if (query === "referral") {
      await expect(
        page.getByText("Valid neighbor", { exact: true }),
      ).toBeVisible();
      const before = exchanges.length;
      await page
        .getByRole("button", { name: "Connect to this registry", exact: true })
        .click();
      await expect(
        page.getByText(/POST \/search did not answer/),
      ).toBeVisible();
      await expect.poll(() => exchanges.slice(before).length).toBe(3);
      const requests = exchanges.slice(before);
      expect(requests[0].url).toBe(
        fixture.origin + "/referral-target/search/search",
      );
      expect(requests.every((x) => x.native.status === 404)).toBe(true);
      passed(label, "client-limitation", {
        statuses: [404, 404, 404],
        searchURL: requests[0].url,
      });
    } else {
      const status = Number(query.slice(5));
      expect(item.outerStatus).toBe(200);
      expect(item.native.status).toBe(status);
      await expect(page.getByText(new RegExp(`^HTTP ${status}`))).toBeVisible();
      passed(label, "supported", { outerStatus: 200, status });
    }
  }
  // No process listens on the reserved loopback port; do not select an external host.
  await connect(fixture.unavailable);
  await tab("Search");
  const unavailable = await search("unavailable");
  expect(unavailable.outerStatus).toBe(200);
  expect(unavailable.native.ok).toBe(false);
  await expect(page.getByText("Request failed", { exact: true })).toBeVisible();
  passed("target-unavailable", "supported", {
    outerStatus: 200,
    transportFailed: true,
  });
  expect(count()).toBeLessThanOrEqual(50);
  return { outcomes, proxyOperations: count(), exchanges };
}
