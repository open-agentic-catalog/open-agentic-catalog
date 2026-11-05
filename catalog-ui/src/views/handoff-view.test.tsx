// @vitest-environment jsdom
// Handoff views keep missing delivery, archive evidence and external provenance distinct.
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import {
  ExternalHandoff,
  LocalHandoff,
  LocalAssessment,
  RepresentationHandoff,
} from "./Handoff";
import type { RecordDetail, SearchResult } from "../api/client";
import type { Representation } from "./handoff";
import { workspace } from "../../tests/unit/fixtures";

afterEach(cleanup);

it.each([false, true])(
  "exposes already-loaded decoded metadata without an exact-byte export (compact=%s)",
  (compact) => {
    const { props, get } = workspace();
    const data = JSON.parse(
      '{"name":"<script>not executable</script>","large":9007199254740993}',
    ) as Record<string, unknown>;
    const view = render(
      <RepresentationHandoff
        {...props}
        compact={compact}
        item={{ ...item, data }}
      />,
    );
    const disclosure = screen
      .getByText("View decoded metadata")
      .closest("details")!;
    expect(disclosure.open).toBe(false);
    fireEvent.click(screen.getByText("View decoded metadata"));
    expect(disclosure.open).toBe(true);
    expect(disclosure.textContent).toContain("not original bytes");
    expect(disclosure.textContent).toContain("rounded");
    expect(
      within(disclosure).getByRole("group", { name: "JSON document" })
        .textContent,
    ).toContain("<script>not executable</script>");
    expect(view.container.querySelector("script")).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Copy|Export|Prepare retained/ }),
    ).toBeNull();
    expect(get).not.toHaveBeenCalled();
  },
);
const item: Representation = {
  id: "rep",
  role: "primary",
  mediaType: "application/json",
  profileId: "",
  contentDigest: "",
  size: 0,
  validationState: "valid",
};

it("shows resource references and disclosed delivery evidence without prefetching retained documents", () => {
  const { props, get } = workspace();
  const record = {
    resource: {
      canonicalIdentifier: "urn:air:example.test:agent:demo",
      kind: "agent",
    },
    version: { version: "1.0.0" },
    release: { id: "release" },
    listingRevision: { id: "listing" },
    versionRevision: {
      id: "revision",
      representations: [
        { ...item, artifactId: "a".repeat(64), url: "javascript:alert(1)" },
      ],
    },
  } as unknown as RecordDetail;
  const view = render(
    <LocalHandoff
      record={record}
      client={props.client}
      onFailure={props.onFailure}
    />,
  );
  const evidence = screen
    .getByText("Representation evidence")
    .closest("details");
  expect(evidence).toHaveProperty("open", false);
  fireEvent.click(screen.getByText("Representation evidence"));
  expect(evidence).toHaveProperty("open", true);
  for (const label of [
    "Resource identifier",
    "Resource version",
    "Metadata document URL",
  ])
    expect(screen.getByRole("button", { name: `Copy ${label}` })).toBeTruthy();
  expect(view.container.querySelector('a[href^="javascript:"]')).toBeNull();
  expect(screen.getAllByText("Not supplied")).toHaveLength(2);
  expect(screen.getByText("Representation document")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
});

it.each([
  ["agent", "Agent metadata"],
  ["mcp_server", "MCP document"],
  ["skill", "complete Skill tree"],
  ["plugin", "complete Plugin archive"],
] as const)(
  "explains the %s metadata handoff without inventing delivery details",
  (kind, guidance) => {
    const { props, get } = workspace();
    const record = {
      resource: { kind, canonicalIdentifier: "urn:air:example.test:kind:demo" },
      version: { version: "1" },
      release: { id: "release" },
      listingRevision: { id: "listing" },
      versionRevision: { id: "revision", representations: [] },
    } as unknown as RecordDetail;
    render(<LocalHandoff {...props} record={record} compact />);
    expect(screen.getByText(new RegExp(guidance))).toBeTruthy();
    expect(
      screen.getByRole("heading", {
        name: "Use this resource",
        level: 3,
      }),
    ).toBeTruthy();
    expect(screen.getByText(/No representation delivery details/)).toBeTruthy();
    expect(
      screen.queryByRole("button", {
        name: "Prepare retained document download",
      }),
    ).toBeNull();
    expect(get).not.toHaveBeenCalled();
  },
);

it.each(["inline", "missing", "skill", "plugin"])(
  "explains %s delivery without inventing a download",
  (kind) => {
    const { props, get } = workspace();
    const rep = {
      ...item,
      profileId: kind === "inline" ? "a2a-agent-card-1.0.1-3303592" : "known",
      contentDigest: "sha256:digest",
      ...(kind === "inline"
        ? { data: { name: "inline" } }
        : kind === "skill"
          ? { package: { mode: "external_reference_only" } }
          : kind === "plugin"
            ? { plugin: {}, url: "https://archive.test/plugin.zip" }
            : {}),
    } as Representation;
    render(<RepresentationHandoff {...props} item={rep} />);
    expect(
      screen.queryByRole("button", {
        name: "Prepare retained document download",
      }),
    ).toBeNull();
    expect(
      screen.getByText(
        kind === "inline"
          ? /Formatted JSON/
          : kind === "missing"
            ? /No retained document/
            : /archive is not stored/,
      ),
    ).toBeTruthy();
    if (kind === "plugin")
      expect(
        screen.getByRole("button", { name: "Copy External archive URL" }),
      ).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  },
);

it("keeps sparse indexed and transient live references separate from local pins and documents", () => {
  const base = {
    identifier: "urn:air:external.test:agent:demo",
    type: "indexed",
    versionPresent: false,
    indexed: { normalizedEntryDigest: "entry-digest" },
  } as SearchResult;
  const { indexed: _indexed, version: _version, ...liveBase } = base;
  const view = render(<ExternalHandoff item={base} />);
  expect(screen.getByText(/No version supplied/)).toBeTruthy();
  expect(
    screen.getByText(/does not include representation delivery/),
  ).toBeTruthy();
  view.rerender(
    <ExternalHandoff
      item={{
        ...liveBase,
        type: "live",
        versionPresent: true,
        version: "",
        live: {
          sourceId: "source",
          source: "https://source.test",
          entry: { url: "javascript:literal" },
        },
      }}
    />,
  );
  expect(
    screen.getByRole("button", { name: "Copy Declared version" }),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Copy Live source" })).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Copy External metadata URL" }),
  ).toBeTruthy();
  expect(view.container.querySelector("a")).toBeNull();
  view.rerender(
    <ExternalHandoff
      item={{
        ...liveBase,
        type: "live",
        versionPresent: true,
        live: {
          sourceId: "s",
          source: "https://source.test",
          entry: { url: "x".repeat(8193) },
        },
      }}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "Copy External metadata URL" }),
  ).toBeNull();
});

it("shows declared provider and delivery without copying private authentication fields", () => {
  const { props } = workspace();
  render(
    <RepresentationHandoff
      item={{
        ...item,
        profileId: "a2a-agent-card-1.0.1-3303592",
        data: {
          provider: { organization: "<b>Provider</b>" },
          supportedInterfaces: [{ url: "https://agent.test" }],
          private: "secret-sentinel",
        },
      }}
      client={props.client}
      onFailure={props.onFailure}
    />,
  );
  expect(screen.getByText("Declared remote interface").textContent).toBe(
    "Declared remote interface",
  );
  expect(screen.getByText("<b>Provider</b>").tagName).toBe("DD");
  expect(screen.queryByText("secret-sentinel")).toBeNull();
});

it("keeps multiple representations, native versions and digest scopes independent", () => {
  const { props, get } = workspace();
  const reps: Representation[] = [
    {
      ...item,
      id: "agent",
      mediaType: "application/a2a+json",
      profileId: "a2a-agent-card-1.0.1-3303592",
      contentDigest: "sha256:agent",
      data: {
        version: "agent-2",
        supportedInterfaces: [
          { url: "https://agent.test", protocolVersion: "1.0" },
        ],
      },
    },
    {
      ...item,
      id: "skill",
      role: "distribution",
      mediaType: "application/agent-skills+md",
      profileId: "agent-skills-69ef37e",
      contentDigest: "sha256:archive",
      package: {
        structureState: "passed",
        rootDirectory: "skills/read",
        fileCount: 3,
      },
    },
  ];
  render(
    <>
      {reps.map((rep) => (
        <RepresentationHandoff key={rep.id} {...props} item={rep} />
      ))}
    </>,
  );
  const agent = within(
    screen.getByRole("heading", { name: "primary: Agent interfaces" })
      .parentElement!,
  );
  const skill = within(
    screen.getByRole("heading", {
      name: "distribution: Skill package and source",
    }).parentElement!,
  );
  expect(agent.getByText("agent-2")).toBeTruthy();
  expect(
    agent.getByRole("button", { name: "Copy Endpoint 1 protocol version" }),
  ).toBeTruthy();
  expect(skill.queryByText("agent-2")).toBeNull();
  expect(skill.getByText("skills/read")).toBeTruthy();
  expect(skill.getByText("3")).toBeTruthy();
  fireEvent.click(agent.getByText("Representation evidence"));
  fireEvent.click(skill.getByText("Representation evidence"));
  expect(agent.getByText("application/a2a+json")).toBeTruthy();
  expect(agent.getByText("Representation document")).toBeTruthy();
  expect(agent.getByText("sha256:agent")).toBeTruthy();
  expect(skill.getByText("application/agent-skills+md")).toBeTruthy();
  expect(skill.getByText("Inspected archive")).toBeTruthy();
  expect(skill.getByText("sha256:archive")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
});

it("keeps completed or blocked validation separate from every reported trust state and unassessed rights", () => {
  const evidence = {
    state: "complete" as const,
    trust: { state: "not_assessed" as const },
    legalRights: { state: "not_assessed" as const },
  };
  const view = render(<LocalAssessment evidence={evidence} compact />);
  const assessment = within(
    screen.getByRole("region", { name: "Resource assessment" }),
  );
  expect(
    assessment.getByRole("heading", {
      level: 3,
      name: "Validation and assessments",
    }),
  ).toBeTruthy();
  expect(assessment.getByText("Completed")).toBeTruthy();
  expect(assessment.getAllByText("Not assessed")).toHaveLength(2);
  expect(
    assessment.getByText(/not proof of ownership or security/),
  ).toBeTruthy();
  for (const [state, label] of [
    ["unsupported", "Unsupported"],
    ["unverified", "Unverified"],
    ["verified", "Reported verified"],
    ["failed", "Failed"],
    ["expired", "Expired"],
  ] as const) {
    view.rerender(
      <LocalAssessment
        evidence={{ ...evidence, state: "blocked", trust: { state } }}
      />,
    );
    expect(assessment.getByRole("heading", { level: 2 })).toBeTruthy();
    expect(assessment.getByText("Blocked")).toBeTruthy();
    expect(assessment.getByText(label)).toBeTruthy();
    expect(assessment.getByText("Not assessed")).toBeTruthy();
    expect(assessment.queryByText("Completed")).toBeNull();
  }
});

it("keeps each decoded representation independent and uses the inert plain renderer for large data", () => {
  const { props, get } = workspace();
  const large = "x".repeat(128 * 1024 + 1);
  const reps = [
    {
      ...item,
      id: "first",
      data: { distinct: "First decoded representation" },
    },
    {
      ...item,
      id: "second",
      data: { distinct: "Second decoded representation", large },
    },
  ];
  const view = render(
    <>
      {reps.map((rep) => (
        <RepresentationHandoff key={rep.id} {...props} item={rep} compact />
      ))}
    </>,
  );
  const summaries = screen.getAllByText("View decoded metadata");
  expect(summaries).toHaveLength(2);
  const first = summaries[0]!.closest("details")!;
  const second = summaries[1]!.closest("details")!;
  fireEvent.click(summaries[1]!);
  expect(first.open).toBe(false);
  expect(second.open).toBe(true);
  expect(second.textContent).toContain("Second decoded representation");
  expect(second.textContent).not.toContain("First decoded representation");
  expect(
    within(second).getByRole("group", { name: "Text document" }).textContent,
  ).toContain(large);
  expect(
    within(second).queryByRole("button", { name: "Format JSON" }),
  ).toBeNull();
  expect(second.querySelector(".syntax-string")).toBeNull();
  expect(view.container.querySelector("script")).toBeNull();
  expect(get).not.toHaveBeenCalled();
});

it("explains independent content admission at a retained download without making a read", () => {
  const { props, get } = workspace();
  render(
    <RepresentationHandoff
      {...props}
      item={{ ...item, artifactId: "a".repeat(64) }}
    />,
  );
  expect(
    screen.getByText(/currently authorized content reference/),
  ).toBeTruthy();
  expect(screen.getByText(/catalog.read scope/)).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Prepare retained document download" }),
  ).toHaveProperty("disabled", false);
  expect(get).not.toHaveBeenCalled();
});

it.each(["allowed", "denied", "scope", "pending", "failed"])(
  "keeps the %s namespace hint separate from actual artifact admission",
  (mode) => {
    const { props, get } = workspace();
    const refresh = vi.fn();
    const state = {
      access: {
        ...props.access,
        permissions:
          mode === "allowed" || mode === "scope"
            ? ["resource.read_content" as const]
            : [],
        namespaceGrants: [],
        scopeEnforcement: mode === "scope",
        scopes: [],
      },
      busy: mode === "pending",
      error: mode === "failed" ? "unavailable" : "",
      refresh,
    };
    render(
      <RepresentationHandoff
        {...props}
        item={{ ...item, artifactId: "a".repeat(64) }}
        permissionState={state}
        namespaceId="ns-1"
      />,
    );
    const prepare = screen.getByRole("button", {
      name: "Prepare retained document download",
    });
    expect(prepare).toHaveProperty("disabled", false);
    expect(
      screen.getByText(/currently authorized content reference/),
    ).toBeTruthy();
    if (mode === "pending")
      expect(screen.getByRole("status").textContent).toContain(
        "Checking this namespace",
      );
    else if (mode === "failed") {
      fireEvent.click(
        screen.getByRole("button", { name: "Retry content permission hints" }),
      );
      expect(refresh).toHaveBeenCalledOnce();
    } else
      expect(
        screen.getByText(
          mode === "allowed"
            ? /hint includes content/
            : /hint does not include content/,
        ),
      ).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  },
);

it.each([
  "/docs/",
  "https://manual.example.test/private",
  "",
  "javascript:bad",
])(
  "uses only the configured documentation policy for missing package delivery (%s)",
  (documentationUrl) => {
    const { props, get } = workspace();
    const record = {
      resource: {
        kind: "skill",
        canonicalIdentifier: "urn:air:example.test:skill:one",
      },
      version: { version: "1" },
      listingRevision: {},
      versionRevision: { representations: [] },
    } as unknown as RecordDetail;
    render(
      <LocalHandoff
        {...props}
        record={record}
        documentationUrl={documentationUrl}
      />,
    );
    if (
      documentationUrl === "/docs/" ||
      documentationUrl.startsWith("https://")
    ) {
      const link = screen.getByRole("link", { name: /opens in a new tab/ });
      expect(link.getAttribute("href")).toBe(
        documentationUrl === "/docs/"
          ? "/docs/guides/resource-authoring/#skill-retain-a-standalone-markdown-document"
          : documentationUrl,
      );
      expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    } else expect(screen.queryByRole("link")).toBeNull();
    expect(get).not.toHaveBeenCalled();
  },
);
