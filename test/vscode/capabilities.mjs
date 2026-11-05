import assert from "node:assert/strict";
const { Session, Browser, Opinions, connections, transport } = await import(
  process.env.CATALOG_VSCODE_LIB
);

/** Composed Catalog, real HTTP, real permission/lifecycle/idempotency owners. */
export async function qualifyCapabilities(origin) {
  const sessions = [];
  const connect = async (token) => {
    const data = new Map();
    const session = new Session(
      {
        get: async (k) => data.get(k),
        store: async (k, v) => data.set(k, v),
        delete: async (k) => data.delete(k),
      },
      "owned-capabilities",
      transport,
    );
    sessions.push(session);
    await session.select(
      connections([
        {
          id: "capabilities",
          label: "Owned capabilities",
          origin,
          mode: "control",
          auth: "manual",
          allowLoopbackHTTP: true,
        },
      ])[0],
    );
    await session.manual(token);
    await session.test();
    return session;
  };
  let browser;
  try {
    const session = await connect("reader"),
      admin = await connect("admin"),
      other = await connect("other");
    browser = new Browser(
      session,
      { get: () => undefined, update: async () => {} },
      "owned-capabilities",
    );
    const combined = await browser.search({
      origins: ["local", "indexed"],
      kinds: ["agent", "mcp_server"],
      tags: ["owned"],
      categories: ["tools"],
      limit: 1,
    });
    assert.equal(combined.items.length, 1);
    assert.ok(combined.next);
    const next = await browser.search(browser.state.query, true);
    assert.equal(next.items.length, 1);
    assert.notEqual(combined.items[0].key, next.items[0].key);
    const missing = await browser.search({
      origins: ["local", "indexed"],
      kinds: ["agent"],
      categories: ["tools", "missing"],
    });
    assert.equal(missing.items.length, 0);
    const page = await browser.search({ kind: "agent" });
    const detail = await browser.select(page.items[0].key);
    const opinions = new Opinions(session);
    let context = await opinions.rating(detail.item);
    const created = await opinions.submitRating(context, 4);
    let current = await opinions.get("ratings", created.id);
    assert.equal(current.value.consent, false);
    context = await opinions.rating(detail.item);
    await opinions.submitRating(context, 5, true);
    await assert.rejects(
      opinions.submitRating(context, 1),
      (error) => error.status === 412,
    );
    current = await opinions.get("ratings", created.id);
    assert.equal(current.value.score, 5);
    assert.equal(current.value.consent, true);
    const outsider = new Opinions(other);
    await assert.rejects(
      outsider.get("ratings", created.id),
      (error) => error.status === 404,
    );
    assert.ok(
      (await opinions.page("ratings")).items.some((v) => v.id === created.id),
    );
    await opinions.withdraw("ratings", current);
    context = await opinions.rating(detail.item);
    await opinions.submitRating(context, 3);
    current = await opinions.get("ratings", created.id);
    assert.equal(current.value.consent, false);
    await opinions.withdraw("ratings", current);
    const feedbackContext = await opinions.feedbackContext(detail.item);
    const submitted = await opinions.submitFeedback(
      feedbackContext,
      "documentation",
      "Private composed Catalog feedback",
    );
    current = await opinions.get("feedback", submitted.id);
    assert.equal(current.value.comment, "Private composed Catalog feedback");
    const adminRead = await admin.read(`/api/v1/feedback/${submitted.id}`);
    await admin.read(`/api/v1/feedback/${submitted.id}:address`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "Idempotency-Key": "vscode-feedback-response",
        "If-Match": adminRead.headers.get("etag"),
      },
      body: JSON.stringify({
        response: "Administrator response from owned fixture",
      }),
    });
    current = await opinions.get("feedback", submitted.id);
    assert.equal(current.value.state, "addressed");
    assert.equal(
      current.value.response,
      "Administrator response from owned fixture",
    );
    await assert.rejects(
      outsider.get("feedback", submitted.id),
      (error) => error.status === 404,
    );
    assert.ok(
      (await opinions.page("feedback")).items.some(
        (v) => v.id === submitted.id,
      ),
    );
    await opinions.submitFeedback({}, "suggestion", "Reopened feedback", {
      id: submitted.id,
      etag: current.etag,
    });
    const stale = current;
    current = await opinions.get("feedback", submitted.id);
    assert.equal(current.value.response, undefined);
    assert.equal(current.value.state, "open");
    await assert.rejects(
      opinions.withdraw("feedback", stale),
      (error) => error.status === 412,
    );
    await opinions.withdraw("feedback", current);
    current = await opinions.get("feedback", submitted.id);
    assert.equal(current.value.state, "withdrawn");
    assert.equal(current.value.comment, undefined);
    console.log(
      "Real composed Catalog: combined filters/paging/all-categories, rating sharing/edit/withdraw/concurrency, private feedback/administrator response/reopen/withdraw and cross-tenant denial passed",
    );
  } finally {
    browser?.dispose();
    for (const session of sessions) await session.dispose();
  }
}
