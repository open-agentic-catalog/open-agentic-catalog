import test from "node:test";
import assert from "node:assert/strict";
const { Opinions, ClientError, feedbackText, localRelease } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const id = "00000000-0000-7000-8000-000000000001";
const item = {
  origin: "local",
  releaseId: id,
  identifier: "urn:air:owned.test:team:agent",
  kind: "agent",
  version: "1",
  versionPresent: true,
};
const opinion = {
  id,
  state: "active",
  revision: 2,
  consent: true,
  score: 4,
  target: { origin: "local" },
  releaseId: id,
};
function fixture(now, wallNow = () => 0) {
  const calls = [];
  let mode = "control",
    binding = "one",
    failure,
    own;
  const session = {
    connection: () => ({ mode }),
    guard: () => {
      const captured = binding;
      return () => {
        if (captured !== binding) throw new ClientError("connection_changed");
      };
    },
    consumerBinding: () => binding,
    async json(path, options) {
      calls.push({ path, options });
      if (path.startsWith("/api/v1/catalog/resources/"))
        return {
          resource: { id, canonicalIdentifier: item.identifier, kind: "agent" },
          versionRevision: { id },
          version: { version: "1" },
          release: { id },
        };
      if (path === "/api/v1/ratings/read") return { count: own ? 1 : 0, own };
      return { items: own ? [own] : [], nextCursor: "private-cursor" };
    },
    async read(path, options, accepted) {
      calls.push({ path, options, accepted });
      if (failure) throw failure;
      const body = options ? { id, revision: 3 } : own;
      return {
        headers: new Headers({ etag: '"rev-2"' }),
        text: () => JSON.stringify(body),
      };
    },
  };
  return {
    api: new Opinions(session, now, wallNow),
    calls,
    session,
    fail: (value) => {
      failure = value;
    },
    own: (value) => {
      own = value;
    },
    mode: (value) => {
      mode = value;
    },
    account: (value) => {
      binding = value;
    },
  };
}
test("personal ratings use fresh exact targets, private defaults and existing revision ETags", async () => {
  const f = fixture();
  let context = await f.api.rating(item);
  assert.equal(context.target.resourceId, id);
  assert.equal(context.target.versionRevisionId, id);
  await f.api.submitRating(context, 5);
  let call = f.calls.at(-1);
  assert.deepEqual(JSON.parse(call.options.body), {
    target: context.target,
    releaseId: id,
    score: 5,
    consent: false,
  });
  assert.equal(call.options.headers["If-Match"], undefined);
  assert.match(call.options.headers["Idempotency-Key"], /^[a-f0-9-]{36}$/u);
  assert.deepEqual(call.accepted, [200, 201]);
  f.own(opinion);
  context = await f.api.rating(item);
  await f.api.submitRating(context, 3);
  call = f.calls.at(-1);
  assert.equal(call.options.headers["If-Match"], '"rev-2"');
  assert.equal(JSON.parse(call.options.body).consent, true);
  await f.api.submitRating(context, 2, false);
  assert.equal(JSON.parse(f.calls.at(-1).options.body).consent, false);
  f.own({ ...opinion, state: "withdrawn", score: 0 });
  context = await f.api.rating(item);
  await f.api.submitRating(context, 4);
  assert.equal(JSON.parse(f.calls.at(-1).options.body).consent, false);
  for (const score of [0, 6, 1.5, "3"])
    await assert.rejects(f.api.submitRating(context, score), /invalid_rating/u);
  await assert.rejects(
    f.api.submitRating(context, 1, "yes"),
    /invalid_rating/u,
  );
});
test("own reads and withdrawal do not inspect resources or require submission authority", async () => {
  for (const family of ["ratings", "feedback"]) {
    const f = fixture();
    f.own(
      family === "ratings"
        ? opinion
        : {
            id,
            revision: 2,
            state: "addressed",
            category: "documentation",
            comment: "Private",
            response: "Administrator response",
          },
    );
    const page = await f.api.page(family, "cursor");
    assert.equal(page.next, "private-cursor");
    assert.equal(page.items.length, 1);
    const current = await f.api.get(family, id);
    if (family === "feedback")
      assert.equal(current.value.response, "Administrator response");
    await f.api.withdraw(family, current);
    assert.equal(f.calls.at(-1).path, `/api/v1/${family}/${id}:withdraw`);
    assert.equal(f.calls.at(-1).options.headers["If-Match"], '"rev-2"');
    assert.equal(f.calls.at(-1).options.body, "{}");
    assert.ok(
      f.calls.every((call) => !call.path.includes("catalog/resources")),
    );
  }
});
test("private feedback keeps immutable context, bounded Unicode and edit ETags", async () => {
  const f = fixture(),
    context = await f.api.feedbackContext(item);
  assert.deepEqual(context, {
    resourceId: id,
    releaseId: id,
    versionRevisionId: id,
  });
  const text = "😀".repeat(4000);
  await f.api.submitFeedback(context, "problem", text);
  assert.equal(f.calls.at(-1).options.method, "POST");
  assert.deepEqual(JSON.parse(f.calls.at(-1).options.body), {
    ...context,
    category: "problem",
    comment: text,
  });
  await f.api.submitFeedback(
    { resourceId: "must not change" },
    "suggestion",
    "Reopen",
    { id, etag: '"rev-7"' },
  );
  assert.equal(f.calls.at(-1).options.method, "PATCH");
  assert.deepEqual(JSON.parse(f.calls.at(-1).options.body), {
    category: "suggestion",
    comment: "Reopen",
  });
  assert.equal(f.calls.at(-1).options.headers["If-Match"], '"rev-7"');
  for (const value of ["", " ", "x\0", "\ud800", "😀".repeat(4001)])
    assert.throws(() => feedbackText(value));
  for (const category of ["admin", "", null])
    await assert.rejects(
      f.api.submitFeedback(context, category, "Text"),
      /invalid_feedback/u,
    );
});
test("uncertain outcomes require explicit identical replay; denied/concurrent writes can be refreshed", async () => {
  const f = fixture();
  f.fail(new ClientError("network_error"));
  await assert.rejects(
    f.api.submitFeedback({ resourceId: id }, "other", "Private sentinel"),
  );
  assert.equal(f.api.hasPending(), true);
  const original = f.calls.at(-1);
  await assert.rejects(
    f.api.submitFeedback({ resourceId: id }, "other", "A second command"),
    /opinion_outcome_uncertain/u,
  );
  assert.equal(f.calls.at(-1), original, "No implicit mutation retry");
  f.fail(undefined);
  await f.api.recover();
  assert.deepEqual(
    f.calls.at(-1),
    original,
    "Same body, key, method and precondition",
  );
  assert.equal(f.api.hasPending(), false);
  await assert.rejects(f.api.recover(), /no_pending_opinion/u);
  for (const status of [401, 403, 409, 412, 422]) {
    f.fail(new ClientError("refused", status));
    await assert.rejects(
      f.api.submitFeedback({ resourceId: id }, "other", "Private"),
    );
    assert.equal(f.api.hasPending(), false);
  }
  f.fail(new ClientError("timeout"));
  await assert.rejects(
    f.api.submitFeedback({ resourceId: id }, "other", "Private"),
  );
  f.account("two");
  assert.equal(f.api.hasPending(), false);
  f.mode("published");
  await assert.rejects(f.api.page("ratings"), /control_mode_required/u);
});
test("opinion parsers refuse malformed server data and non-local submissions", async () => {
  const f = fixture();
  for (const value of [
    { ...item, origin: "indexed" },
    { ...item, releaseId: undefined },
    { ...item, kind: "unknown" },
  ])
    assert.throws(() => localRelease(value), /local_release_required/u);
  for (const value of [
    { ...opinion, id: "bad" },
    { ...opinion, revision: 0 },
    { ...opinion, state: "bad" },
    { ...opinion, consent: "yes" },
    { ...opinion, score: 6 },
  ]) {
    f.own(value);
    await assert.rejects(f.api.page("ratings"));
  }
  f.own({ id, revision: 1, state: "open", category: "bad" });
  await assert.rejects(f.api.page("feedback"));
  f.session.json = async () => ({ items: Array(26).fill(opinion) });
  await assert.rejects(f.api.page("ratings"));
  f.session.json = async () => ({
    resource: { id, canonicalIdentifier: "changed", kind: "agent" },
    versionRevision: { id },
    version: { version: "1" },
    release: { id },
  });
  await assert.rejects(f.api.rating(item), /selection_changed/u);
  await assert.rejects(f.api.feedbackContext(item), /selection_changed/u);
  f.api.clear();
  assert.equal(f.api.hasPending(), false);
});

test("uncertain replay stays exact after refusals, expires monotonically, and blocks parallel writes", async () => {
  let now = 0;
  const f = fixture(() => now);
  f.fail(new ClientError("timeout"));
  await assert.rejects(
    f.api.submitFeedback({ resourceId: id }, "other", "Private"),
  );
  const original = f.calls.at(-1);
  f.fail(new ClientError("forbidden", 403));
  await assert.rejects(f.api.recover());
  assert.equal(
    f.api.hasPending(),
    true,
    "A failed replay cannot establish the original outcome",
  );
  assert.deepEqual(f.calls.at(-1), original);
  now = 600001;
  const count = f.calls.length;
  await assert.rejects(f.api.recover(), /opinion_recovery_expired/u);
  assert.equal(
    f.calls.length,
    count,
    "Never replay after the local recovery budget",
  );
  f.api.clear();
  f.fail(undefined);
  let finish;
  f.session.read = async () =>
    new Promise((resolve) => {
      finish = () =>
        resolve({ text: () => JSON.stringify({ id, revision: 1 }) });
    });
  const pending = f.api.submitFeedback(
    { resourceId: id },
    "other",
    "Single flight",
  );
  await assert.rejects(f.api.recover(), /opinion_command_in_progress/u);
  await assert.rejects(
    f.api.submitFeedback({}, "other", "Parallel"),
    /opinion_command_in_progress/u,
  );
  finish();
  await pending;
  assert.equal(f.api.hasPending(), false);
});

test("uncertain replay expires across suspension and refuses backwards clocks without contact", async () => {
  for (const step of ["suspended", "wall-backwards", "monotonic-backwards"]) {
    let mono = 100,
      wall = 1000000;
    const f = fixture(
      () => mono,
      () => wall,
    );
    f.fail(new ClientError("timeout"));
    await assert.rejects(
      f.api.submitFeedback({ resourceId: id }, "other", "Private"),
    );
    const count = f.calls.length;
    if (step === "suspended") {
      mono++;
      wall += 24 * 60 * 60 * 1000;
    } else if (step === "wall-backwards") wall--;
    else mono--;
    f.fail(undefined);
    await assert.rejects(f.api.recover(), /opinion_recovery_expired/u);
    assert.equal(
      f.calls.length,
      count,
      "An expired key must never reach the API again",
    );
    assert.equal(
      f.api.hasPending(),
      true,
      "Keep the pending outcome for deliberate list inspection/forgetting",
    );
  }
});
