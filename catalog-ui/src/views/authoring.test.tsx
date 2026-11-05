import { required } from "../../tests/required";
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ActionForm, revealInvalidControl } from "./authoring";
import { Field, Pager, ReadState, confirmDiscard } from "./workspace";
import {
  newRepresentation,
  packageBytes,
  packageMediaType,
  parseObject,
  representationInput,
  replaceRepresentation,
} from "./representations";
import { auditFilters, auditRange, auditActions } from "./Audit";
import { toggleWebhookEvent, webhookChoices } from "./webhook-events";
import { catalogRoles } from "./Members";
import contract from "../../../api/openapi/control-v1.yaml?raw";
import { ScopeInput } from "./Federation";
import { sourceScope, topologyInput } from "./federation-inputs";
import { listingQueries } from "./Listings";
import { hasCatalogRecord } from "./Reviews";
import { RepresentationContent } from "./Versions";
import type { WorkspaceProps } from "./workspace";
import {
  relationshipDraft,
  relationshipInputs,
  listingVersionRevision,
  ListingMetadata,
  submittedListingMatches,
} from "./listing-metadata";

describe("authoring inputs", () => {
  it("associates federation scope guidance with its actual text control", () => {
    const html = renderToStaticMarkup(
      <ScopeInput
        label="Scope tags"
        hint="Exact tags, one per row."
        value={["support", "demo"]}
        onChange={vi.fn()}
      />,
    );
    expect(html).toMatch(/<textarea[^>]*aria-describedby="[^"]+-hint"/);
    expect(html).toContain(">support</textarea>");
    expect(html).toContain(">demo</textarea>");
    expect(html).toContain("Exact tags, one per row.");
  });
  it("keeps field help separate from the accessible label and associates the hint", () => {
    const html = renderToStaticMarkup(
      <Field
        label="Subject"
        hint="Exact identity identifier"
        help="Ask your identity administrator"
      >
        <input aria-describedby="existing" />
      </Field>,
    );
    expect(html).toMatch(
      /<label[^>]*><span>Subject<\/span><input[^>]*aria-describedby="existing [^"]+-hint"/,
    );
    expect(html).toContain('aria-expanded="false"');
    expect(html).toMatch(/<p[^>]*hidden=""/);
    expect(html).toContain("About subject");
  });
  it("captures bounded audit presets deterministically without leaking custom dates", () => {
    const draft = {
      actor: "",
      action: "resource.created",
      requestId: "",
      targetType: "",
      targetId: "",
      occurredFrom: "bad",
      occurredBefore: "bad",
    };
    const now = Date.parse("2026-01-10T12:00:00Z");
    for (const [range, hours] of [
      ["hour", 1],
      ["day", 24],
      ["week", 168],
    ] as const) {
      expect(auditRange(draft, range, now)).toEqual({
        action: draft.action,
        occurredFrom: new Date(now - hours * 3600000).toISOString(),
        occurredBefore: new Date(now).toISOString(),
      });
    }
    expect(() => auditRange(draft, "invalid", now)).toThrow("valid time range");
    expect(() => auditRange(draft, "custom", now)).toThrow();
  });
  it("preserves advanced webhook subscriptions when toggling common choices", () => {
    expect(
      toggleWebhookEvent(
        "resource.created, source.paused\nresource.created",
        "*",
        true,
      ),
    ).toBe("resource.created\nsource.paused\n*");
    expect(toggleWebhookEvent("*\nsource.paused", "*", false)).toBe(
      "source.paused",
    );
    expect(toggleWebhookEvent("", "*", false)).toBe("");
    expect(toggleWebhookEvent("*", "*", true)).toBe("*");
  });
  it("offers only contract-defined activity, webhook and role choices", () => {
    const audit = required(contract.split("    AuditEvent:\n")[1]).split(
      "    AuditDetailsV1:\n",
    )[0];
    for (const [value] of auditActions)
      expect(audit).toContain("- " + value + "\n");
    const events = required(contract.split("    WebhookEventType:\n")[1]).split(
      "    WebhookEventSelector:\n",
    )[0];
    for (const [value] of webhookChoices)
      expect(events).toContain(value === "*" ? "- '*'" : "- " + value + "\n");
    expect(Object.keys(catalogRoles)).toHaveLength(8);
  });
  it("offers retained inline JSON and artifact reads without prefetching content", () => {
    const props = {
      client: {},
      runtime: {},
      access: {},
      id: "revision",
      onFailure: vi.fn(),
    } as unknown as WorkspaceProps;
    const base = {
      id: "representation",
      role: "primary" as const,
      mediaType: "application/json",
      profileId: "",
      contentDigest: "sha256:" + "a".repeat(64),
      size: 2,
      validationState: "valid" as const,
    };
    for (const storage of [
      { data: {} },
      { data: { private: "not-displayed-before-click" } },
      { artifactId: "a".repeat(64) },
    ] as const) {
      const html = renderToStaticMarkup(
        <RepresentationContent {...props} item={{ ...base, ...storage }} />,
      );
      expect(html).toContain("Read");
      expect(html).toContain("retained document");
      expect(html).not.toContain("No retained document");
      expect(html).not.toContain("not-displayed-before-click");
    }
    const external = renderToStaticMarkup(
      <RepresentationContent {...props} item={base} />,
    );
    expect(external).toContain("No retained document");
    expect(external).not.toContain("<button");
  });
  it("links only current discoverable releases to Catalog records", () => {
    for (const state of [
      "published",
      "deprecated",
      "retired",
      "superseded",
      "revoked",
      "unknown",
    ]) {
      expect(
        hasCatalogRecord(
          { id: "release", state },
          { currentReleaseId: "release" },
        ),
      ).toBe(["published", "deprecated"].includes(state));
      expect(
        hasCatalogRecord(
          { id: "release", state },
          { currentReleaseId: "other" },
        ),
      ).toBe(false);
      expect(hasCatalogRecord({ id: "release", state }, {})).toBe(false);
    }
  });
  it("binds readable review metadata to every submitted listing identity", () => {
    const submission = {
      listingRevisionId: "submitted",
      listingId: "listing",
      versionRevisionId: "version",
    };
    const listing = {
      id: "submitted",
      listingId: "listing",
      versionRevisionId: "version",
    };
    expect(submittedListingMatches(submission, listing)).toBe(true);
    for (const key of ["id", "listingId", "versionRevisionId"] as const)
      expect(
        submittedListingMatches(submission, { ...listing, [key]: "different" }),
      ).toBe(false);
  });
  it("keeps the current content revision for metadata-only edits and requires explicit replacements", () => {
    expect(listingVersionRevision(true, "current", "different")).toBe(
      "current",
    );
    expect(listingVersionRevision(false, "current", "different")).toBe(
      "different",
    );
    for (const [keep, current, selected] of [
      [true, undefined, "different"],
      [false, "current", ""],
      [false, undefined, ""],
    ] as const)
      expect(() => listingVersionRevision(keep, current, selected)).toThrow(
        /Select a version revision/,
      );
  });
  it("serializes all supported relationship types without editor IDs or empty versions", () => {
    for (const type of [
      "depends_on",
      "recommended_with",
      "supersedes",
      "bundled_with",
    ] as const) {
      const draft = relationshipDraft({
        type,
        targetIdentifier: " urn:air:example.com:agents:one ",
        targetVersion: type === "supersedes" ? " 1.0 " : " ",
      });
      expect(relationshipInputs([draft])).toEqual([
        {
          type,
          targetIdentifier: "urn:air:example.com:agents:one",
          ...(type === "supersedes" ? { targetVersion: "1.0" } : {}),
        },
      ] as const);
      expect(
        required(relationshipInputs([{ ...draft, targetVersion: " 1.0 " }])[0])
          .targetVersion,
      ).toBe("1.0");
      expect(draft.targetVersion).toBe(type === "supersedes" ? " 1.0 " : " ");
    }
  });
  it("rejects missing, duplicate and over-limit relationships without silently dropping input", () => {
    const good = relationshipDraft({
      type: "depends_on",
      targetIdentifier: "urn:air:example.com:agents:one",
    });
    for (const rows of [
      [relationshipDraft()],
      [good, { ...good, editorID: "other" }],
      [{ ...good, targetIdentifier: "x".repeat(513) }],
      [{ ...good, targetVersion: "x".repeat(129) }],
      Array.from({ length: 65 }, () => good),
    ])
      expect(() => relationshipInputs(rows)).toThrow();
    expect(relationshipInputs([])).toEqual([]);
    expect(
      relationshipInputs(
        Array.from({ length: 64 }, (_, i) => ({
          ...good,
          targetIdentifier: `item-${i}`,
        })),
      ),
    ).toHaveLength(64);
  });
  it("requires an exact version when serializing supersedes", () => {
    const draft = relationshipDraft({
      type: "supersedes",
      targetIdentifier: "urn:air:example.com:agents:one",
    });
    expect(() => relationshipInputs([draft])).toThrow(/exact target version/);
    expect(() =>
      relationshipInputs([{ ...draft, targetVersion: " " }]),
    ).toThrow(/exact target version/);
  });

  it("counts relationship identities in Unicode code points", () => {
    const targetIdentifier = "🙂".repeat(512);
    const targetVersion = "🙂".repeat(128);
    const draft = relationshipDraft({
      type: "depends_on",
      targetIdentifier,
      targetVersion,
    });
    expect(relationshipInputs([draft])).toEqual([
      { type: "depends_on", targetIdentifier, targetVersion },
    ]);
    expect(() =>
      relationshipInputs([
        { ...draft, targetIdentifier: targetIdentifier + "x" },
      ]),
    ).toThrow();
    expect(() =>
      relationshipInputs([{ ...draft, targetVersion: targetVersion + "x" }]),
    ).toThrow();
  });
  it("shows listing review metadata without turning untrusted strings into HTML or active links", () => {
    const markup = renderToStaticMarkup(
      <ListingMetadata
        revision={{
          id: "revision",
          tenantId: "tenant",
          listingId: "listing",
          revisionNumber: 3,
          versionRevisionId: "version",
          displayName: "<script>alert(1)</script>",
          description: "Line one\nLine two",
          publisherId: "publisher",
          visibility: "private",
          digest: "digest",
          createdBy: "author",
          createdAt: "2026-09-09T00:00:00Z",
          tags: ["example"],
          categories: ["<b>tools</b>", "agents"],
          representativeQueries: ["What is this?"],
          documentationUrl: "javascript:alert(1)",
          relationships: [
            {
              type: "depends_on",
              targetIdentifier: "urn:air:example.com:agents:one",
              targetVersion: "1.0",
            },
          ],
        }}
      />,
    );
    expect(markup).toContain("&lt;script&gt;");
    expect(markup).not.toContain("<script>");
    expect(markup).not.toContain("href=");
    expect(markup).toContain("&lt;b&gt;tools&lt;/b&gt;, agents");
    expect(markup).not.toContain("<b>");
    expect(markup).toContain("Revision 3");
    expect(markup).toContain("Depends on");
    expect(markup).toContain("What is this?");
  });
  it("keeps asynchronous upload results bound to their original editor", () => {
    const first = newRepresentation("skill");
    const uploading = newRepresentation("skill");
    const third = newRepresentation("skill");
    expect(
      new Set([first.editorID, uploading.editorID, third.editorID]).size,
    ).toBe(3);
    const finished = { ...uploading, packageData: "upload-result" };
    // The first row was removed while the second row was reading a file.
    expect(
      replaceRepresentation([uploading, third], uploading.editorID, finished),
    ).toEqual([finished, third]);
    // Removing the uploading row makes a late completion a no-op.
    expect(
      replaceRepresentation([first, third], uploading.editorID, finished),
    ).toEqual([first, third]);
    expect(
      representationInput({ ...first, content: "# Skill" }),
    ).not.toHaveProperty("editorID");
  });
  it("selects the supported media type from the selected Skill archive", () => {
    expect(packageMediaType("skill.zip")).toBe("application/agent-skills+zip");
    for (const name of ["skill.tar.gz", "SKILL.TGZ"])
      expect(packageMediaType(name)).toBe("application/agent-skills+gzip");
  });
  it("checks public listing query requirements before submitting metadata", () => {
    expect(listingQueries("", "private")).toEqual([]);
    expect(
      listingQueries(" first question \n second question", "public"),
    ).toEqual(["first question", "second question"]);
    expect(() => listingQueries("one\none", "public")).toThrow(/two to five/);
    expect(() => listingQueries("1\n2\n3\n4\n5\n6", "public")).toThrow();
  });
  it("keeps federation scope and topology explicit", () => {
    expect(sourceScope({ kinds: ["agent"], tags: ["docs"] })).toEqual({
      kinds: ["agent"],
      tags: ["docs"],
    });
    for (const value of [
      {},
      [],
      null,
      { kinds: ["unsupported"] },
      { tags: [] },
      { tags: [2] },
      { unexpected: ["x"] },
    ])
      expect(() => sourceScope(value)).toThrow();
    expect(topologyInput('{"referrals":{"enabled":false}}')).toEqual({
      referrals: { enabled: false },
    });
    expect(() => topologyInput('{"url":"https://example.com"}')).toThrow(
      /only/,
    );
    expect(() => topologyInput('{"referrals":{},"referrals":{}}')).toThrow();
  });
  it("uses the implemented kind-specific media types", () => {
    expect(newRepresentation("agent").mediaType).toBe(
      "application/a2a-agent-card+json",
    );
    expect(newRepresentation("mcp_server").mediaType).toBe(
      "application/mcp-server-card+json",
    );
    const skill = newRepresentation("skill");
    expect(representationInput({ ...skill, content: "# Skill" })).toEqual({
      role: "primary",
      mediaType: "application/agent-skills+md",
      content: "# Skill",
    });
  });
  it("preserves extension values but rejects duplicate and ambiguous JSON", () => {
    const data = {
      extra: [{ key: "value", nested: { a: true } }, null, 1e10, "a:b"],
      empty: {},
      text: '\\"{}[]',
    };
    expect(parseObject(JSON.stringify(data))).toEqual(data);
    for (const source of [
      '{"a":1,"a":2}',
      '{"a":1,"\\u0061":2}',
      '{"a":[{"x":1,"x":2}]}',
      "[]",
      "null",
      "true",
      "{} {}",
      "{",
      '{"extension":9007199254740993}',
      '{"extension":-9007199254740993}',
      '{"extension":[1e400]}',
    ])
      expect(() => parseObject(source)).toThrow();
    expect(parseObject('{"extension":9007199254740991}')).toEqual({
      extension: Number.MAX_SAFE_INTEGER,
    });
    expect(() =>
      parseObject('{"a":' + "[".repeat(65) + "0" + "]".repeat(65) + "}"),
    ).toThrow(/nesting/);
    expect(() => parseObject(" ".repeat(2 * 1024 * 1024 + 1))).toThrow(/limit/);
  });
  it("selects only the chosen input source and rejects credential-bearing URLs", () => {
    const base = {
      ...newRepresentation("agent"),
      content: '{"name":"test"}',
      url: "https://example.com/card",
    };
    expect(representationInput(base)).not.toHaveProperty("url");
    expect(representationInput({ ...base, mode: "url" })).not.toHaveProperty(
      "data",
    );
    for (const url of [
      "http://example.com/card",
      "https://user:pass@example.com/card",
      "https://example.com/card#fragment",
      "invalid",
    ])
      expect(() =>
        representationInput({ ...base, mode: "url", url }),
      ).toThrow();
    expect(() =>
      representationInput({ ...base, mode: "package", packageData: "" }),
    ).toThrow(/package/);
    expect(
      representationInput({
        ...newRepresentation("skill"),
        mode: "package",
        mediaType: " application/agent-skills+zip ",
        profileId: " skill-profile ",
        packageData: "AQ==",
        handoff: JSON.stringify({
          oci: {
            reference: "registry.example.com/skills@sha256:" + "a".repeat(64),
          },
        }),
      }),
    ).toEqual({
      role: "primary",
      mediaType: "application/agent-skills+zip",
      profileId: "skill-profile",
      packageData: "AQ==",
      handoff: {
        oci: {
          reference: "registry.example.com/skills@sha256:" + "a".repeat(64),
        },
      },
    });
    expect(() =>
      representationInput({ ...base, mode: "content", content: " " }),
    ).toThrow(/content/);
  });
  it("bounds file reads before allocation and preserves the exact package bytes", async () => {
    expect(
      await packageBytes(
        new File([new Uint8Array([0, 1, 128, 255])], "skill.zip"),
      ),
    ).toBe("AAGA/w==");
    await expect(packageBytes(new File([], "empty.zip"))).rejects.toThrow();
    const read = vi.fn();
    await expect(
      packageBytes({
        size: 16 * 1024 * 1024 + 1,
        arrayBuffer: read,
      } as unknown as File),
    ).rejects.toThrow();
    expect(read).not.toHaveBeenCalled();
  });
});

describe("workflow controls", () => {
  it("opens every enclosing disclosure before native invalid-field focus", () => {
    const outer = { open: false, parentElement: null };
    const inner = {
      open: false,
      parentElement: { closest: vi.fn(() => outer) },
    };
    const control = { closest: vi.fn(() => inner) };
    revealInvalidControl(control as unknown as Element);
    expect(inner.open).toBe(true);
    expect(outer.open).toBe(true);
    expect(control.closest).toHaveBeenCalledWith("details");
    expect(inner.parentElement.closest).toHaveBeenCalledWith("details");
    expect(() =>
      revealInvalidControl({ closest: () => null } as unknown as Element),
    ).not.toThrow();
  });
  it("never submits the surrounding form from a picker pager or failed read", () => {
    const pager = renderToStaticMarkup(
      <Pager cursor="first" nextCursor="next" onPage={() => {}} />,
    );
    expect(pager.match(/type="button"/g)).toHaveLength(2);
    const error = renderToStaticMarkup(
      <ReadState busy={false} error="Unavailable" refresh={() => {}} />,
    );
    expect(error).toContain('type="button"');
  });
  it("allows selecting a namespace while preventing an unauthorized mutation", () => {
    const html = renderToStaticMarkup(
      <ActionForm
        title="Create"
        description="Create a record"
        allowed={false}
        onFailure={() => {}}
        prepare={() => async () => "done"}
      >
        <input aria-label="Namespace" />
      </ActionForm>,
    );
    expect(html).toContain('type="submit" disabled=""');
    expect(html).not.toContain('<fieldset class="workspace-fields" disabled');
  });
  it("confirms in-page dismissal only when an editor reports unsaved work", () => {
    const confirm = vi.fn(() => false);
    const dispatchEvent = vi.fn(() => true);
    vi.stubGlobal("window", { confirm, dispatchEvent });
    try {
      expect(confirmDiscard()).toBe(true);
      expect(confirm).not.toHaveBeenCalled();
      dispatchEvent.mockReturnValue(false);
      expect(confirmDiscard()).toBe(false);
      expect(confirm).toHaveBeenCalledOnce();
    } finally {
      vi.unstubAllGlobals();
    }
  });
  it("keeps audit target pairs and half-open ranges valid", () => {
    const base = {
      actor: "",
      action: "",
      requestId: "",
      targetType: "",
      targetId: "",
      occurredFrom: "",
      occurredBefore: "",
    };
    expect(auditFilters(base)).toEqual({});
    expect(() => auditFilters({ ...base, targetType: "resource" })).toThrow(
      /both/,
    );
    expect(() =>
      auditFilters({
        ...base,
        occurredFrom: "2026-09-09T12:00:00Z",
        occurredBefore: "2026-09-09T12:00:00Z",
      }),
    ).toThrow(/follow/);
    expect(
      auditFilters({
        ...base,
        occurredFrom: "2026-09-09T11:00:00Z",
        occurredBefore: "2026-09-09T12:00:00Z",
      }),
    ).toEqual({
      occurredFrom: "2026-09-09T11:00:00.000Z",
      occurredBefore: "2026-09-09T12:00:00.000Z",
    });
  });
});

it("normalizes representative query whitespace, deduplicates normalized values and enforces private Unicode/count bounds", () => {
  expect(listingQueries("🙂\t🙂 🙂\n🙂 🙂 🙂", "private")).toEqual([
    "🙂 🙂 🙂",
  ]);
  expect(listingQueries("🙂".repeat(240), "private")).toEqual([
    "🙂".repeat(240),
  ]);
  for (const value of [
    "🙂🙂",
    "🙂".repeat(241),
    "a\0b",
    "one\ntwo\nthree\nfour\nfive\nsix",
  ])
    expect(() => listingQueries(value, "private")).toThrow();
});
