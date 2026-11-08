// Panel decisions expose bounded metadata and actionable recovery without host effects.
import test from "node:test";
import assert from "node:assert/strict";

const { documentAction, guidance, projection } = await import(
  process.env.CATALOG_VSCODE_LIB
);

const registry = {
  name: "io.example/unit-notices",
  description: "A native MCP declaration for a consent preview.",
  version: "1.0.0",
  packages: [
    {
      registryType: "npm",
      identifier: "@example/first",
      version: "1.2.3",
      transport: { type: "stdio" },
    },
    {
      registryType: "npm",
      identifier: "@example/second",
      version: "4.5.6",
      transport: { type: "stdio" },
    },
    {
      registryType: "pypi",
      identifier: "example-unit-server",
      version: "1.2.3",
      transport: { type: "stdio" },
    },
  ],
};

test("credential-write contention does not offer another sign-in or write retry", () => {
  for (const error of ["credential_in_use", "credential_update_pending"]) {
    const result = guidance("verified", true, 4, false, error);
    assert.equal(result.phase, "failed");
    assert.deepEqual(result.actions, ["help"]);
    assert.match(
      result.message,
      /another Catalog window|Another Catalog window/u,
    );
    assert.match(result.message, /Recover Catalog Login/u);
  }
  const signing = guidance("signing_in", true, 4, false);
  assert.equal(signing.phase, "signing_in");
  assert.deepEqual(signing.actions, ["connect", "help"]);
  assert.match(signing.message, /cancel its progress notification/u);
});

test("read cancellation and timeout guidance preserve explicit recovery choices", () => {
  for (const error of ["request_canceled", "aborted", "cancelled"]) {
    const result = guidance("verified", true, 4, false, error);
    assert.equal(result.phase, "failed");
    assert.deepEqual(result.actions, ["restart", "help"]);
    assert.match(result.message, /canceled/u);
  }
  for (const error of ["timeout", "request_timeout", "deadline_exceeded"]) {
    const result = guidance("verified", true, 4, false, error);
    assert.equal(result.phase, "failed");
    assert.deepEqual(result.actions, ["settings", "restart", "help"]);
    assert.match(result.message, /timed out/u);
  }
  const unknown = guidance("verified", true, 4, false, "private-read-detail");
  assert.deepEqual(unknown.actions, ["settings", "restart", "help"]);
  assert.doesNotMatch(unknown.message, /private-read-detail/u);
});

test("MCP action notices name distinct required runners without installing them", () => {
  const result = documentAction(
    {
      label: "Registry server.json",
      mediaType: "application/vnd.mcp.server+json",
      value: registry,
    },
    "mcp_server",
  );
  assert.deepEqual(result, {
    action: "mcp",
    notice:
      "Requires npx on this execution host. Requires uvx on this execution host.",
  });
  assert.deepEqual(
    documentAction(
      { label: "Skill document", mediaType: "application/vnd.mcp.server+json" },
      "skill",
    ),
    {},
  );
});

test("native action admission measures encoded bytes when a size hint is absent", () => {
  const ref = {
    label: "Registry server.json",
    mediaType: "application/vnd.mcp.server+json",
    value: { ...registry, "x-notes": "Small forward-compatible metadata." },
  };
  assert.equal(documentAction(ref, "mcp_server").action, "mcp");
  const oversized = {
    ...ref,
    value: { ...ref.value, "x-notes": "😀".repeat(262144) },
  };
  assert.ok(JSON.stringify(oversized.value).length < 1024 ** 2);
  assert.ok(Buffer.byteLength(JSON.stringify(oversized.value)) > 1024 ** 2);
  const result = documentAction(oversized, "mcp_server");
  assert.equal(result.action, undefined);
  assert.match(
    result.reason,
    /native consumption accepts documents up to 1 MiB/u,
  );
});

test("detail projection exposes indexed package and artwork choices, not private payloads", () => {
  const item = {
    key: "local:unit-skill",
    identifier: "urn:air:example.com:team:unit-skill",
    kind: "skill",
    name: "Unit Skill",
    description: "A visible description.",
    tags: ["owned"],
    origin: "local",
    versionPresent: true,
    version: "",
    references: [],
    raw: { secret: "private-record-sentinel" },
    artworkTarget: { listingRevisionId: "private-target-sentinel" },
    artwork: {
      descriptor: {
        icon: { url: "https://private-artwork-sentinel.invalid/icon.png" },
        screenshots: [
          { url: "https://private-artwork-sentinel.invalid/one.png" },
        ],
      },
    },
  };
  const browser = {
    state: {
      scope: 3,
      busy: false,
      query: { text: "owned" },
      canNext: false,
      bookmarks: [],
      detail: {
        item,
        references: [],
        facts: [],
        documents: [{ label: "Skill", content: "private-document-sentinel" }],
        packages: [{ source: "private-package-sentinel" }, {}],
      },
    },
  };
  let view = projection(browser, "control", 7, "verified");
  assert.equal(view.canBrowse, true);
  assert.equal(view.detail.item.version, "");
  assert.equal(view.detail.bookmark, true);
  assert.deepEqual(view.detail.packages, [
    { index: 0, label: "Review and install complete Skill", type: "skill" },
    { index: 1, label: "Review and install complete Skill", type: "skill" },
  ]);
  assert.deepEqual(view.detail.artwork, [
    { index: 0, label: "Load icon from Catalog" },
    { index: 1, label: "Load screenshot 1 from Catalog" },
  ]);
  assert.deepEqual(view.detail.documents, [
    { label: "Skill", exact: true, readable: true },
  ]);
  assert.doesNotMatch(JSON.stringify(view), /private-[a-z-]+-sentinel/u);

  item.kind = "plugin";
  view = projection(browser, "control", 8, "signed_out");
  assert.equal(view.canBrowse, false);
  assert.deepEqual(view.detail.packages, [
    { index: 0, label: "Review and install Plugin disabled", type: "plugin" },
    { index: 1, label: "Review and install Plugin disabled", type: "plugin" },
  ]);
  item.kind = "mcp_server";
  item.origin = "live";
  delete item.artworkTarget;
  view = projection(browser, "published", 9, "anonymous");
  assert.equal(view.detail.bookmark, false);
  assert.deepEqual(view.detail.packages, []);
  assert.deepEqual(view.detail.artwork, []);
});
