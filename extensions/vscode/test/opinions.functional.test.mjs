import test from "node:test";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import assert from "node:assert/strict";
import { createServer } from "node:http";
const { Session, Opinions, connections } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const id = "00000000-0000-7000-8000-000000000001";
const resourceId = "00000000-0000-7000-8000-000000000002";
const versionRevisionId = "00000000-0000-7000-8000-000000000003";
test("real HTTP opinion commands accept 201, retain private text, enforce ETags and recover an uncertain committed write", async (t) => {
  const calls = [],
    records = new Map(),
    receipts = new Map();
  let lostAccess = false,
    dropReply = false,
    refuseReplay = false;
  const server = createServer(async (req, res) => {
    const reply = (status, body, revision) => {
      res.writeHead(status, {
        "content-type": "application/json",
        ...(revision ? { etag: `"rev-${revision}"` } : {}),
      });
      res.end(JSON.stringify(body));
    };
    if (req.headers.authorization !== "Bearer owned-http-token")
      return reply(401, {});
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : {};
    calls.push({
      path: req.url,
      method: req.method,
      body,
      raw,
      headers: req.headers,
    });
    if (req.url === "/api/v1/catalog/resources/" + id)
      return lostAccess
        ? reply(404, {})
        : reply(200, {
            resource: {
              id: resourceId,
              kind: "agent",
              canonicalIdentifier: "urn:air:owned.test:team:agent",
            },
            versionRevision: { id: versionRevisionId },
            version: { version: "1" },
            release: { id },
          });
    if (req.url === "/api/v1/ratings/read")
      return reply(200, {
        count: 0,
        ...(records.get("ratings") ? { own: records.get("ratings") } : {}),
      });
    const family = req.url.includes("/ratings") ? "ratings" : "feedback",
      current = records.get(family);
    if (req.method === "GET")
      return req.url.includes("?")
        ? reply(200, { items: current ? [current] : [] })
        : current
          ? reply(200, current, current.revision)
          : reply(404, {});
    const key = req.headers["idempotency-key"];
    assert.ok(key);
    if (refuseReplay && receipts.has(key)) return reply(403, {});
    if (receipts.has(key))
      return reply(200, receipts.get(key), receipts.get(key).revision);
    if (current && req.headers["if-match"] !== `"rev-${current.revision}"`)
      return reply(412, {});
    const revision = (current?.revision ?? 0) + 1;
    const value = {
      ...current,
      ...body,
      id,
      revision,
      state: req.url.endsWith(":withdraw")
        ? "withdrawn"
        : family === "ratings"
          ? "active"
          : "open",
    };
    if (value.state === "withdrawn") {
      delete value.comment;
      delete value.response;
      value.score = 0;
    }
    records.set(family, value);
    receipts.set(key, { id, revision });
    if (dropReply) {
      dropReply = false;
      return req.socket.destroy();
    }
    reply(current ? 200 : 201, { id, revision }, revision);
  });
  await listenFixture(server);
  t.after(() => {
    server.closeAllConnections();
    return new Promise((done) => server.close(done));
  });
  const saved = new Map(),
    session = new Session(
      {
        get: async (k) => saved.get(k),
        store: async (k, v) => saved.set(k, v),
        delete: async (k) => saved.delete(k),
      },
      "owned-http",
      async () => ({ fetch, close: async () => {} }),
    );
  t.after(() => session.dispose());
  await session.select(
    connections([
      {
        id: "http",
        label: "Owned HTTP",
        mode: "control",
        auth: "manual",
        origin: `http://127.0.0.1:${server.address().port}`,
        allowLoopbackHTTP: true,
      },
    ])[0],
  );
  await session.manual("owned-http-token");
  const api = new Opinions(session),
    item = {
      origin: "local",
      releaseId: id,
      identifier: "urn:air:owned.test:team:agent",
      kind: "agent",
      version: "1",
    };
  let context = await api.rating(item);
  await api.submitRating(context, 4);
  assert.equal(records.get("ratings").consent, false);
  context = await api.rating(item);
  await api.submitRating(context, 5, true);
  assert.equal(records.get("ratings").consent, true);
  await assert.rejects(
    api.submitRating(context, 2),
    (error) => error.status === 412,
  );
  assert.equal(api.hasPending(), false);
  const feedback = await api.feedbackContext(item);
  assert.deepEqual(feedback, { resourceId, releaseId: id, versionRevisionId });
  dropReply = true;
  await assert.rejects(
    api.submitFeedback(feedback, "problem", "Private HTTP sentinel"),
  );
  assert.equal(api.hasPending(), true);
  const first = calls.at(-1);
  assert.deepEqual(first.body, {
    category: "problem",
    comment: "Private HTTP sentinel",
    resourceId,
    releaseId: id,
    versionRevisionId,
  });
  const originalRevision = records.get("feedback").revision;
  const replayStart = calls.length;
  refuseReplay = true;
  await assert.rejects(api.recover(), {
    code: "access_refused",
    status: 403,
  });
  assert.equal(
    api.hasPending(),
    true,
    "a later refusal cannot settle a lost reply",
  );
  const refused = calls.at(-1);
  assert.equal(
    refused.headers["idempotency-key"],
    first.headers["idempotency-key"],
  );
  assert.equal(refused.raw, first.raw);
  assert.equal(refused.method, first.method);
  assert.equal(refused.path, first.path);
  assert.equal(records.get("feedback").revision, originalRevision);
  await assert.rejects(
    api.submitFeedback(
      feedback,
      "suggestion",
      "Must not replace an uncertain command",
    ),
    { code: "opinion_outcome_uncertain" },
  );
  assert.equal(
    calls.length,
    replayStart + 1,
    "no implicit retry or replacement write",
  );
  refuseReplay = false;
  await api.recover();
  const repeat = calls.at(-1);
  assert.equal(
    repeat.headers["idempotency-key"],
    first.headers["idempotency-key"],
  );
  assert.equal(repeat.raw, first.raw);
  assert.equal(repeat.method, first.method);
  assert.equal(repeat.path, first.path);
  assert.deepEqual(repeat.body, first.body);
  assert.equal(records.get("feedback").revision, originalRevision);
  assert.equal(calls.length, replayStart + 2);
  assert.equal(api.hasPending(), false);
  records.get("feedback").state = "addressed";
  records.get("feedback").response = "Private response";
  const read = await api.get("feedback", id);
  assert.equal(read.value.response, "Private response");
  await api.submitFeedback({}, "documentation", "Reopen feedback", {
    id,
    etag: read.etag,
  });
  assert.deepEqual(calls.at(-1).body, {
    category: "documentation",
    comment: "Reopen feedback",
  });
  lostAccess = true;
  const marker = calls.length;
  for (const family of ["ratings", "feedback"]) {
    assert.equal((await api.page(family)).items.length, 1);
    const own = await api.get(family, id);
    await api.withdraw(family, own);
    assert.equal(records.get(family).state, "withdrawn");
  }
  assert.ok(
    calls
      .slice(marker)
      .every((value) => !value.path.includes("catalog/resources")),
    "Own reads and withdrawal make no resource request",
  );
  assert.ok(
    [...receipts.values()].every(
      (value) => !JSON.stringify(value).includes("Private"),
    ),
    "Receipts retain only command identity/revision",
  );
  assert.ok(
    [...saved.values()].every(
      (value) => !value.includes("Private HTTP sentinel"),
    ),
    "Private text never enters credential storage",
  );
});
