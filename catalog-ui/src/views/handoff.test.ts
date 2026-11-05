// Reference projection tests distinguish declared delivery facts from unsupported or missing metadata.
import { expect, it } from "vitest";
import {
  authenticationSummary,
  object,
  representationReferences,
  representationDisplay,
  representationGuide,
} from "./handoff";
import type { Representation } from "./handoff";

const representation: Representation = {
  id: "representation-1",
  role: "primary",
  mediaType: "application/a2a-agent-card+json",
  profileId: "a2a-agent-card-1.0.1-3303592",
  size: 0,
  contentDigest: "sha256:abc",
  validationState: "valid",
};

it("projects exact pins, document URLs and declared endpoints without converting them into commands", () => {
  expect(
    representationReferences({
      ...representation,
      url: "javascript:alert(1)",
      handoff: {
        git: {
          repository: "https://git.test/repo",
          commit: "a".repeat(40),
          directory: "skills/review",
        },
        oci: { reference: "registry.test/review@sha256:" + "b".repeat(64) },
      },
      data: {
        supportedInterfaces: [
          { url: "https://runtime.test", protocolBinding: "JSONRPC" },
        ],
        securitySchemes: { auth: {} },
      },
    }),
  ).toEqual([
    { label: "Metadata document URL", value: "javascript:alert(1)" },
    { label: "Git repository", value: "https://git.test/repo" },
    { label: "Git commit", value: "a".repeat(40) },
    { label: "Git directory", value: "skills/review" },
    {
      label: "OCI digest reference",
      value: "registry.test/review@sha256:" + "b".repeat(64),
    },
    { label: "Declared endpoint 1", value: "https://runtime.test" },
    { label: "Endpoint 1 transport", value: "JSONRPC" },
  ]);
  expect(
    representationReferences({
      ...representation,
      profileId: "unknown",
      data: { supportedInterfaces: [{ url: "https://not-inferred.test" }] },
    }),
  ).toEqual([]);
  expect(
    representationReferences({
      ...representation,
      url: "x".repeat(8193),
      data: { supportedInterfaces: [null, { url: 5 }] },
    }),
  ).toEqual([]);
  expect(object(null)).toEqual({});
  expect(object([])).toEqual({});
  expect(object("text")).toEqual({});
  expect(
    representationReferences({
      ...representation,
      package: { state: "inspected" },
      url: "https://packages.test/skill.zip",
    }),
  ).toEqual([
    { label: "External archive URL", value: "https://packages.test/skill.zip" },
  ]);
});

it("bounds native endpoint projection and explains auth declarations without leaking values", () => {
  for (const profileId of [
    "mcp-registry-server-1.8.1-f52dc85",
    "mcp-server-card-v1-experimental-526201b",
  ]) {
    const item = {
      ...representation,
      profileId,
      data: {
        remotes: Array.from({ length: 17 }, () => ({
          url: "https://mcp.test",
          type: "streamable-http",
          headers: [{ name: "Authorization", value: "private-value" }],
        })),
      },
    };
    const fields = representationReferences(item);
    expect(fields).toHaveLength(33);
    expect(fields[0]).toEqual({
      label: "Declared endpoint 1",
      value: "https://mcp.test",
    });
    expect(fields[1]).toEqual({
      label: "Endpoint 1 transport",
      value: "streamable-http",
    });
    expect(fields.at(-1)?.value).toContain("First 16");
    expect(JSON.stringify(fields)).not.toContain("private-value");
    expect(authenticationSummary(item)).toContain("Connection headers");
    expect(
      authenticationSummary({ ...item, data: { remotes: [{}] } }),
    ).toContain("not summarized");
  }
  for (const data of [{ securitySchemes: {} }, { securityRequirements: [] }])
    expect(authenticationSummary({ ...representation, data })).toContain(
      "Agent Card",
    );
  expect(authenticationSummary({ ...representation, data: {} })).toContain(
    "not summarized",
  );
  expect(authenticationSummary(representation)).toBe("");
  expect(
    authenticationSummary({
      ...representation,
      profileId: "private",
      data: {},
    }),
  ).toBe("");
});

it("summarizes only declared delivery and bounded attribution from recognized loaded metadata", () => {
  const mixed = representationDisplay({
    ...representation,
    handoff: {
      git: { repository: "https://git.test/repo", commit: "a".repeat(40) },
      oci: { reference: "registry.test/repo@sha256:" + "a".repeat(64) },
    },
    data: {
      supportedInterfaces: [{}, { url: "https://agent.test" }],
      provider: { organization: "<b>Provider</b>" },
      securitySchemes: { auth: { private: "sentinel" } },
    },
  });
  expect(mixed).toEqual({
    delivery: ["Git source", "OCI distribution", "Declared remote interface"],
    attribution: [{ label: "Declared provider", value: "<b>Provider</b>" }],
  });
  expect(
    representationDisplay({
      ...representation,
      url: "https://metadata.test",
      data: { provider: { organization: "𐐀".repeat(257) } },
    }),
  ).toEqual({
    delivery: [],
    attribution: [
      {
        label: "Declared provider",
        value: "See complete representation metadata for the declaration.",
      },
    ],
  });
  expect(
    representationDisplay({
      ...representation,
      profileId: "unknown",
      url: "https://runtime.test",
      data: {
        provider: { organization: "unknown" },
        packages: [{ registryType: "oci" }],
      },
    }),
  ).toEqual({ delivery: [], attribution: [] });
  expect(
    representationDisplay({
      ...representation,
      profileId: "mcp-registry-server-1.8.1-f52dc85",
      data: {
        remotes: [
          {},
          {
            url: "https://mcp.test",
            headers: [{ name: "Authorization", value: "private" }],
          },
        ],
        packages: [{ registryType: "npm" }, { registryType: "oci" }],
      },
    }),
  ).toEqual({
    delivery: ["Declared remote interface", "OCI distribution"],
    attribution: [],
  });
  expect(
    representationDisplay({
      ...representation,
      profileId: "mcp-server-card-v1-experimental-526201b",
      // Packages are not a declared field of the experimental Server Card.
      data: { remotes: [], packages: [{ registryType: "oci" }] },
    }),
  ).toEqual({ delivery: [], attribution: [] });
  expect(
    representationDisplay({
      ...representation,
      profileId: "agent-skills-69ef37e",
      normalizedFacts: {
        license: "Apache-2.0",
        author: "not an admitted projection",
      },
    }),
  ).toEqual({
    delivery: [],
    attribution: [{ label: "Declared native license", value: "Apache-2.0" }],
  });
});
it("reads only the root retained Plugin manifest, preserving conflicts and malformed evidence", () => {
  const plugin = {
    rootDirectory: "package",
    expandedSize: 1,
    assessments: {
      malware: "not_assessed" as const,
      secrets: "not_assessed" as const,
      license: "not_assessed" as const,
      sbom: "not_assessed" as const,
      provenance: "not_assessed" as const,
    },
    files: [],
    documents: [
      {
        path: "package/plugin.json",
        content: JSON.stringify({
          author: { name: "Author" },
          license: "MIT",
          private: "sentinel",
        }),
      },
    ],
  };
  expect(
    representationDisplay({
      ...representation,
      profileId: "agent-plugins-1.0.0-1fc1b627",
      plugin,
    }),
  ).toEqual({
    delivery: ["Inspected Plugin archive"],
    attribution: [
      { label: "Declared author", value: "Author" },
      { label: "Declared native license", value: "MIT" },
    ],
  });
  for (const content of [
    "{",
    JSON.stringify({ author: { name: 8 }, license: null }),
    JSON.stringify({ author: { name: "" }, license: "" }),
  ]) {
    expect(
      representationDisplay({
        ...representation,
        profileId: "agent-plugins-1.0.0-1fc1b627",
        plugin: {
          ...plugin,
          rootDirectory: ".",
          documents: [{ path: "plugin.json", content }],
        },
      }),
    ).toEqual({ delivery: ["Inspected Plugin archive"], attribution: [] });
  }
  expect(
    representationDisplay({
      ...representation,
      profileId: "agent-plugins-1.0.0-1fc1b627",
      plugin: { ...plugin, rootDirectory: "", documents: [] },
    }),
  ).toEqual({ delivery: ["Inspected Plugin archive"], attribution: [] });
});

it("does not coerce opaque or oversized Plugin documents into declared attribution", () => {
  const base = {
    ...representation,
    profileId: "agent-plugins-1.0.0-1fc1b627",
    plugin: {
      rootDirectory: "",
      expandedSize: 1,
      files: [],
      assessments: {
        malware: "not_assessed" as const,
        secrets: "not_assessed" as const,
        license: "not_assessed" as const,
        sbom: "not_assessed" as const,
        provenance: "not_assessed" as const,
      },
      documents: [{ path: "plugin.json", content: " ".repeat(500001) }],
    },
  };
  expect(representationDisplay(base)).toEqual({
    delivery: ["Inspected Plugin archive"],
    attribution: [],
  });
  const decoded: unknown = JSON.parse(
    JSON.stringify({
      ...base,
      plugin: { ...base.plugin, rootDirectory: null },
    }),
  );
  // The runtime response boundary deliberately preserves opaque native evidence;
  // the projection must narrow the fields it actually consumes independently.
  expect(representationDisplay(decoded as Representation)).toEqual({
    delivery: ["Inspected Plugin archive"],
    attribution: [],
  });
  const opaque: unknown = JSON.parse(
    JSON.stringify({
      ...base,
      plugin: {
        ...base.plugin,
        documents: [
          {
            path: "plugin.json",
            content: { author: { name: "not a JSON document" } },
          },
        ],
      },
    }),
  );
  expect(representationDisplay(opaque as Representation)).toEqual({
    delivery: ["Inspected Plugin archive"],
    attribution: [],
  });
});

it("keeps Agent protocol versions separate from the declared Agent version", () => {
  const item = {
    ...representation,
    data: {
      version: "agent-2",
      supportedInterfaces: [
        {
          url: "https://agent.test",
          protocolBinding: "JSONRPC",
          protocolVersion: "1.0",
        },
      ],
    },
  };
  expect(representationGuide(item).facts).toEqual([
    { label: "Declared Agent version", value: "agent-2" },
  ]);
  expect(representationReferences(item)).toEqual([
    { label: "Declared endpoint 1", value: "https://agent.test" },
    { label: "Endpoint 1 transport", value: "JSONRPC" },
    { label: "Endpoint 1 protocol version", value: "1.0" },
  ]);
});

it("projects Registry package identity and transport without inputs or commands, and never treats Server Cards as packages", () => {
  const data = {
    version: "server-1",
    packages: [
      {
        registryType: "npm",
        identifier: "@example/server",
        version: "package-2",
        transport: { type: "stdio" },
        environmentVariables: [{ value: "secret-sentinel" }],
        runtimeArguments: ["do-not-run"],
      },
    ],
  };
  const registry = representationGuide({
    ...representation,
    profileId: "mcp-registry-server-1.8.1-f52dc85",
    data,
  });
  expect(registry.title).toBe("MCP transports and packages");
  expect(registry.facts).toEqual([
    { label: "Declared MCP server version", value: "server-1" },
    { label: "Package 1 registry", value: "npm" },
    { label: "Package 1 identifier", value: "@example/server" },
    { label: "Package 1 version", value: "package-2" },
    { label: "Package 1 transport", value: "stdio" },
  ]);
  const card = representationGuide({
    ...representation,
    profileId: "mcp-server-card-v1-experimental-526201b",
    data,
  });
  expect(card.title).toBe("MCP Server metadata");
  expect(card.facts).toEqual([
    { label: "Declared MCP server version", value: "server-1" },
  ]);
  expect(JSON.stringify([registry, card])).not.toMatch(
    /secret-sentinel|do-not-run/,
  );
});

it.each([
  { remotes: undefined },
  { remotes: [] },
  { remotes: [{ url: "" }] },
  { remotes: [{ url: 3 }] },
])(
  "does not claim a remote connection from a sparse Server Card ($remotes)",
  ({ remotes }) => {
    const result = representationGuide({
      ...representation,
      profileId: "mcp-server-card-v1-experimental-526201b",
      data: { remotes },
    });
    expect(result.title).toBe("MCP Server metadata");
    expect(result.guidance).toContain("declares no remote connection");
    expect(
      representationReferences({
        ...representation,
        profileId: "mcp-server-card-v1-experimental-526201b",
        data: { remotes },
      }),
    ).toEqual([]);
  },
);

it("names a declared remote connection without inventing an endpoint", () => {
  const item = {
    ...representation,
    profileId: "mcp-server-card-v1-experimental-526201b",
    data: {
      remotes: [
        { url: "https://mcp.example.test/mcp", type: "streamable-http" },
      ],
    },
  };
  expect(representationGuide(item).title).toBe("MCP remote connections");
  expect(representationReferences(item)).toEqual([
    { label: "Declared endpoint 1", value: "https://mcp.example.test/mcp" },
    { label: "Endpoint 1 transport", value: "streamable-http" },
  ]);
});

it("only reports a Skill inventory after successful structural inspection", () => {
  const item = { ...representation, profileId: "agent-skills-69ef37e" };
  expect(
    representationGuide({
      ...item,
      package: {
        structureState: "passed",
        rootDirectory: "read",
        fileCount: 3,
      },
    }).facts,
  ).toEqual([
    { label: "Package structure", value: "passed" },
    { label: "Skill root", value: "read" },
    { label: "Inspected files", value: "3" },
  ]);
  expect(
    representationGuide({
      ...item,
      package: {
        structureState: "failed",
        rootDirectory: "partial",
        fileCount: 3,
      },
    }).facts,
  ).toEqual([{ label: "Package structure", value: "failed" }]);
  for (const fileCount of [-1, 0.5, "3", null, Infinity])
    expect(
      representationGuide({
        ...item,
        package: { structureState: "passed", fileCount },
      }).facts,
    ).toEqual([{ label: "Package structure", value: "passed" }]);
  expect(
    representationGuide({ ...item, package: { structureState: "unknown" } })
      .facts,
  ).toEqual([]);
});

it("bounds and narrows Plugin components and Registry package summaries without trusting extension fields", () => {
  const plugin = {
    rootDirectory: "",
    components: [
      {
        kind: "skill",
        name: "<script>inert</script>",
        path: "skills/read/SKILL.md",
      },
      { kind: "mcp_server", name: "tools", path: "mcp.json" },
      { kind: "unknown", name: "not inferred" },
      null,
    ],
  };
  // Opaque optional metadata remains unknown until the projection narrows each field.
  const item = {
    ...representation,
    profileId: "agent-plugins-1.0.0-1fc1b627",
    plugin,
  } as unknown as Representation;
  expect(representationGuide(item).facts).toEqual([
    { label: "Plugin root", value: "." },
    { label: "Skill component 1", value: "<script>inert</script>" },
    { label: "Component 1 path", value: "skills/read/SKILL.md" },
    { label: "MCP Server component 2", value: "tools" },
    { label: "Component 2 path", value: "mcp.json" },
  ]);
  for (const [profileId, data] of [
    [
      "agent-plugins-1.0.0-1fc1b627",
      {
        plugin: {
          components: Array.from({ length: 17 }, () => ({
            kind: "skill",
            name: "x".repeat(257),
          })),
        },
      },
    ],
    [
      "mcp-registry-server-1.8.1-f52dc85",
      {
        data: {
          packages: Array.from({ length: 17 }, () => ({
            identifier: "x".repeat(257),
          })),
        },
      },
    ],
  ] as const) {
    const guide = representationGuide({
      ...representation,
      profileId,
      ...data,
    } as Representation);
    expect(guide.facts).toHaveLength(17);
    expect(guide.facts[0]?.value).toBe(
      "See complete representation metadata for the declaration.",
    );
    expect(guide.facts.at(-1)?.value).toContain("First 16 shown");
  }
  for (const profileId of [
    "unknown",
    "agent-skills-69ef37e",
    "agent-plugins-1.0.0-1fc1b627",
    "mcp-registry-server-1.8.1-f52dc85",
    "mcp-server-card-v1-experimental-526201b",
    representation.profileId,
  ]) {
    const guide = representationGuide({
      ...representation,
      profileId,
      data: {
        version: {},
        packages: [null, { identifier: "", transport: [] }],
      },
    });
    expect(guide.facts).toEqual([]);
  }
});

it("preserves exactly 256 Unicode code points and replaces longer facts without mutating evidence", () => {
  const exact = "🧭".repeat(256);
  const excessive = exact + "x";
  for (const [value, expected] of [
    [exact, exact],
    [excessive, "See complete representation metadata for the declaration."],
  ]) {
    const data = Object.freeze({
      version: value,
      provider: { organization: value },
    });
    const item = { ...representation, data };
    expect(representationGuide(item).facts).toEqual([
      { label: "Declared Agent version", value: expected },
    ]);
    expect(representationDisplay(item).attribution).toEqual([
      { label: "Declared provider", value: expected },
    ]);
    expect(item.data.version).toBe(value);
    expect(item.data.provider.organization).toBe(value);
  }
});

it("preserves package order at the exact list limit and keeps overflow in retained evidence", () => {
  const packages = Array.from({ length: 17 }, (_, index) => ({
    identifier: `package-${index + 1}`,
  }));
  for (const count of [16, 17]) {
    const data = { packages: packages.slice(0, count) };
    const guide = representationGuide({
      ...representation,
      profileId: "mcp-registry-server-1.8.1-f52dc85",
      data,
    });
    expect(guide.facts.slice(0, 16)).toEqual(
      Array.from({ length: 16 }, (_, index) => ({
        label: `Package ${index + 1} identifier`,
        value: `package-${index + 1}`,
      })),
    );
    expect(guide.facts.slice(16)).toEqual(
      count === 16
        ? []
        : [
            {
              label: "Package limit",
              value:
                "First 16 shown; inspect the retained representation for the complete list.",
            },
          ],
    );
    expect(data.packages).toHaveLength(count);
    expect(data.packages.at(-1)?.identifier).toBe(`package-${count}`);
  }
});
