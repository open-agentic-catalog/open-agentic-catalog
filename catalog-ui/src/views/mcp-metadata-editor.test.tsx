// @vitest-environment jsdom
// Controlled metadata units use local state/files, never a binary, database or endpoint.
import { useState } from "react";
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { change, click, screenLifecycle } from "../../tests/unit/screens";
import {
  RepresentationEditor,
  newRepresentation,
  representationInput,
  type RepresentationDraft,
} from "./representations";
import { mcpFormats } from "./mcp-metadata";
import { nativeStarters } from "./native-starters";

screenLifecycle("/unit/resources/new");
const card = JSON.parse(nativeStarters("mcp_server")[0]!.content) as Record<
  string,
  unknown
>;
const registry = JSON.parse(nativeStarters("mcp_server")[2]!.content) as Record<
  string,
  unknown
>;

async function editor(
  content = "",
  format: "card" | "registry" | "private" = "card",
  version = "1.0.0",
) {
  const changed = vi.fn();
  let latest: RepresentationDraft;
  function Harness() {
    const [value, setValue] = useState<RepresentationDraft>({
      ...newRepresentation("mcp_server"),
      editorID: "mcp",
      content,
      mediaType:
        format === "private"
          ? "application/vnd.open-agentic-catalog.mcp-registration+json"
          : mcpFormats[format].media,
      role: "metadata" as const,
      profileId: "kept-profile",
    });
    const [label, setLabel] = useState(version);
    latest = value;
    return (
      <>
        <label>
          Catalog version
          <input
            value={label}
            onChange={(event) => setLabel(event.target.value)}
          />
        </label>
        <RepresentationEditor
          kind="mcp_server"
          index={0}
          value={value}
          version={label}
          onVersionChange={setLabel}
          onChange={(next) => {
            changed(next);
            setValue(next);
          }}
        />
      </>
    );
  }
  const view = render(<Harness />);
  await screen.findByLabelText("MCP document");
  return {
    ...view,
    changed,
    value: () => latest!,
    document: () => JSON.parse(latest!.content) as Record<string, unknown>,
  };
}
function advanced(open: boolean) {
  const details = screen.getByText("Advanced MCP JSON").closest("details")!;
  act(() => {
    details.open = open;
    fireEvent(details, new Event("toggle"));
  });
}
function open(summary: string) {
  const details = screen
    .getByText(summary, { exact: true })
    .closest("details")!;
  act(() => {
    details.open = true;
    fireEvent(details, new Event("toggle"));
  });
}
async function importDocument(content: string) {
  const bytes = new TextEncoder().encode(content);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "server.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  click("Use loaded document");
  await waitFor(() =>
    expect(document.activeElement).toBe(
      screen.getByLabelText("Standards document (JSON)"),
    ),
  );
}

it("generates the pinned experimental Card from fields without imports, extra defaults or a mutation on mount", async () => {
  const test = await editor();
  expect(test.changed).not.toHaveBeenCalled();
  expect(test.value().content).toBe("");
  expect(screen.getByLabelText("MCP document")).toHaveProperty("value", "card");
  change("MCP server name", "com.example/documentation");
  change("MCP description", "Search documentation.");
  change("MCP display title (optional)", "Documentation search");
  expect(representationInput(test.value())).toEqual({
    role: "metadata",
    profileId: "kept-profile",
    mediaType: "application/mcp-server-card+json",
    data: {
      $schema: mcpFormats.card.schema,
      name: "com.example/documentation",
      description: "Search documentation.",
      title: "Documentation search",
      version: "1.0.0",
    },
  });
  change("MCP display title (optional)", "");
  expect(test.document()).not.toHaveProperty("title");
  change("Catalog version", "\u0085 release 2026 Q4 \u0085");
  await waitFor(() => expect(test.document().version).toBe("release 2026 Q4"));
  expect(test.document()).not.toHaveProperty("remotes");
  expect(test.document()).not.toHaveProperty("packages");
});

it("edits ordered endpoints, transports, optional protocol versions and header requirements without secret fields", async () => {
  const test = await editor();
  click("Add endpoint");
  expect(document.activeElement).toBe(screen.getByLabelText("Endpoint 1 URL"));
  change("Endpoint 1 URL", "{baseUrl}/mcp");
  change("Endpoint 1 transport", "sse");
  open("Endpoint 1 protocol versions (optional)");
  click("Add endpoint 1 protocol version");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Endpoint 1 protocol version 1"),
  );
  change("Endpoint 1 protocol version 1", "2025-11-25");
  click("Add endpoint 1 protocol version");
  change("Endpoint 1 protocol version 2", "2026-07-28");
  click("Remove endpoint 1 protocol version 1");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Endpoint 1 protocol version 1"),
  );
  click("Remove endpoint 1 protocol version 1");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add endpoint 1 protocol version" }),
  );
  open("Endpoint 1 headers");
  click("Add header requirement");
  change("Endpoint 1 headers 1 name", "Authorization");
  change("Endpoint 1 headers 1 description (optional)", "Caller credential");
  for (const suffix of ["Required", "Secret"])
    fireEvent.click(screen.getByLabelText(`Endpoint 1 headers 1: ${suffix}`));
  click("Add header requirement");
  change("Endpoint 1 headers 2 name", "X-API-Key");
  change(
    "Endpoint 1 headers 2 description (optional)",
    "Remove this description",
  );
  change("Endpoint 1 headers 2 description (optional)", "");
  click("Remove endpoint 1 headers 1");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Endpoint 1 headers 1 name"),
  );
  expect(test.document().remotes).toEqual([
    {
      type: "sse",
      url: "{baseUrl}/mcp",
      supportedProtocolVersions: [],
      headers: [{ name: "X-API-Key", isRequired: true, isSecret: true }],
    },
  ]);
  click("Remove endpoint 1 headers 1");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add header requirement" }),
  );
  click("Add endpoint");
  change("Endpoint 2 URL", "https://example.test/mcp");
  click("Remove endpoint 1");
  expect(document.activeElement).toBe(screen.getByLabelText("Endpoint 1 URL"));
  expect(test.document().remotes).toEqual([
    { type: "streamable-http", url: "https://example.test/mcp" },
  ]);
  click("Remove endpoint 1");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add endpoint" }),
  );
  expect(test.document().remotes).toEqual([]);
  expect(screen.queryByLabelText(/password|token value/i)).toBeNull();
});

it("generates Registry packages with independent optional versions, local transport metadata and environment declarations", async () => {
  const test = await editor();
  change("MCP document", "registry");
  change("MCP server name", "com.example/documentation");
  change("MCP description", "Local package metadata.");
  expect(screen.getByText(/No distribution is declared/)).toBeTruthy();
  click("Add package");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Package 1 identifier"),
  );
  change("Package 1 identifier", "@example/documentation-mcp");
  expect(test.document().packages).toEqual([
    {
      registryType: "npm",
      identifier: "@example/documentation-mcp",
      transport: { type: "stdio" },
    },
  ]);
  change("Package 1 version (optional)", "2.3.4");
  change("Catalog version", "document-v2");
  await waitFor(() => expect(test.document().version).toBe("document-v2"));
  expect((test.document().packages as { version: string }[])[0]!.version).toBe(
    "2.3.4",
  );
  change("Package 1 version (optional)", "");
  change("Package 1 transport", "streamable-http");
  change("Package 1 transport URL or template", "http://localhost:{port}/mcp");
  change("Package 1 transport", "sse");
  change("Package 1 transport", "stdio");
  expect(
    (test.document().packages as { transport: object }[])[0]!.transport,
  ).toEqual({ type: "stdio", url: "http://localhost:{port}/mcp" });
  open("Package 1 environment");
  click("Add environment requirement");
  change("Package 1 environment 1 name", "API_KEY");
  change("Package 1 environment 1 description (optional)", "Supply at runtime");
  change("Package 1 registry", "other");
  change("Package 1 custom registry type", "internal-registry");
  expect(
    (test.document().packages as { registryType: string }[])[0]!.registryType,
  ).toBe("internal-registry");
  change("Package 1 registry", "mcpb");
  const checksum = screen.getByLabelText(
    "Package 1 SHA-256",
  ) as HTMLInputElement;
  expect(checksum.required).toBe(true);
  change("Package 1 SHA-256", "a".repeat(64));
  change("Package 1 registry", "oci");
  expect(checksum.required).toBe(false);
  change("Package 1 SHA-256", "");
  click("Add package");
  change("Package 2 identifier", "second");
  click("Remove package 1");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Package 1 identifier"),
  );
  expect(test.document().packages).toEqual([
    { registryType: "npm", identifier: "second", transport: { type: "stdio" } },
  ]);
  click("Remove package 1");
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add package" }),
  );
  expect(screen.getByText(/No distribution is declared/)).toBeTruthy();
});

it.each(["card", "registry"] as const)(
  "preserves advanced %s imports, ordered arrays and native versions through common edits",
  async (format) => {
    const original = {
      ...(format === "card" ? card : registry),
      version: "imported",
      remotes: [
        {
          type: "sse",
          url: "https://example.test/first",
          variables: { tenant: { value: "one" } },
          headers: [
            {
              name: "Authorization",
              value: "Bearer {token}",
              variables: { token: { isSecret: true } },
            },
          ],
        },
        { type: "streamable-http", url: "https://example.test/second" },
      ],
      repository: {
        url: "https://example.test/source",
        source: "custom",
        id: "keep",
        subfolder: "servers/docs",
      },
      _meta: {
        "com.example/extra": { maximum: 9007199254740991, decimal: 0.25 },
      },
    };
    const content = JSON.stringify(original, null, 4);
    const test = await editor(content, format, "outer");
    expect(test.value().content).toBe(content);
    expect(test.changed).not.toHaveBeenCalled();
    change("MCP server name", "com.example/changed");
    expect(test.document()).toEqual({
      ...original,
      name: "com.example/changed",
    });
    change("Catalog version", "do-not-overwrite");
    expect(test.document().version).toBe("imported");
    click("Use Version label in this MCP document");
    change("Catalog version", "linked");
    await waitFor(() => expect(test.document().version).toBe("linked"));
    advanced(true);
    const raw = screen.getByLabelText("Standards document (JSON)");
    const edited = JSON.stringify({
      ...test.document(),
      description: "Advanced description",
      version: "external",
    });
    fireEvent.change(raw, { target: { value: edited } });
    advanced(false);
    expect(screen.getByLabelText("MCP description")).toHaveProperty(
      "value",
      "Advanced description",
    );
    change("Catalog version", "still-independent");
    expect(test.value().content).toBe(edited);
    expect(screen.getByLabelText("Standards document (JSON)")).toBe(raw);
  },
);

it("preserves package runtime settings and transport headers when other package fields change", async () => {
  const original = {
    ...registry,
    packages: [
      {
        registryType: "custom",
        identifier: "docs",
        version: "existing",
        transport: {
          type: "sse",
          url: "http://localhost:{port}/mcp",
          headers: [{ name: "X-Client", value: "{name}" }],
          custom: "kept",
        },
        runtimeHint: "node",
        runtimeArguments: [
          { type: "named", name: "--client", value: "two words" },
        ],
        packageArguments: [{ type: "positional", value: "comma, literal" }],
        environmentVariables: [
          {
            name: "API_KEY",
            isSecret: true,
            choices: ["opaque"],
            value: "${API_KEY}",
          },
        ],
        registryBaseUrl: "https://registry.example.test",
      },
    ],
  };
  const test = await editor(JSON.stringify(original), "registry");
  change("Package 1 identifier", "changed");
  const expected = {
    ...original,
    packages: [{ ...original.packages[0], identifier: "changed" }],
  };
  expect(test.document()).toEqual(expected);
  change("Package 1 transport", "stdio");
  expect(test.document()).toEqual({
    ...expected,
    packages: [
      {
        ...expected.packages[0],
        transport: { ...expected.packages[0]!.transport, type: "stdio" },
      },
    ],
  });
});

it("requires explicit replacement when changing formats and clears a staged file without changing other row settings", async () => {
  const original = JSON.stringify(card);
  const test = await editor(original);
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  change("MCP document", "registry");
  expect(confirm).toHaveBeenCalledOnce();
  expect(test.value().content).toBe(original);
  expect(test.value().mediaType).toBe(mcpFormats.card.media);
  const bytes = new TextEncoder().encode(original);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "server-card.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  expect(test.value().documentStaged).toBe(true);
  confirm.mockReturnValue(true);
  change("MCP document", "registry");
  expect(test.value()).toMatchObject({
    content: "",
    mediaType: mcpFormats.registry.media,
    editorID: "mcp",
    profileId: "kept-profile",
    role: "metadata",
  });
  expect(test.value().documentStaged).toBeUndefined();
  expect(
    screen.queryByRole("button", { name: "Use loaded document" }),
  ).toBeNull();
  change("MCP server name", "com.example/new");
  expect(test.document().$schema).toBe(mcpFormats.registry.schema);
  change("MCP document", "card");
  expect(test.value().content).toBe("");
  change("MCP server name", "com.example/card");
  expect(test.document().$schema).toBe(mcpFormats.card.schema);
});

it("imports exact generated bytes without retaining an old automatic version link", async () => {
  const test = await editor();
  change("MCP server name", "com.example/generated");
  const content = test.value().content;
  await importDocument(content);
  expect(test.value().content).toBe(content);
  advanced(false);
  change("Catalog version", "2.0.0");
  expect(test.document().version).toBe("1.0.0");
  change("MCP description", "Edit an imported field.");
  change("Catalog version", "3.0.0");
  expect(test.document().version).toBe("1.0.0");
  click("Use Version label in this MCP document");
  expect(test.document().version).toBe("3.0.0");
  change("Catalog version", "4.0.0");
  await waitFor(() => expect(test.document().version).toBe("4.0.0"));
});

it("keeps malformed advanced text visible and preserves valid partial imports without applying new-item defaults", async () => {
  const test = await editor(
    JSON.stringify({ $schema: mcpFormats.card.schema, remotes: [{}] }),
    "card",
    "",
  );
  expect(test.changed).not.toHaveBeenCalled();
  expect(
    screen.getByRole("button", {
      name: "Use Version label in this MCP document",
    }),
  ).toHaveProperty("disabled", true);
  change("Endpoint 1 transport", "sse");
  expect(test.document()).toEqual({
    $schema: mcpFormats.card.schema,
    remotes: [{ type: "sse" }],
  });
  advanced(true);
  const raw = screen.getByLabelText("Standards document (JSON)");
  fireEvent.change(raw, { target: { value: '{"name":"one","name":"two"}' } });
  await waitFor(() =>
    expect(screen.queryByLabelText("MCP server name")).toBeNull(),
  );
  expect(test.value().content).toBe('{"name":"one","name":"two"}');
  advanced(false);
  expect(test.value().content).toBe('{"name":"one","name":"two"}');
  fireEvent.change(raw, { target: { value: JSON.stringify(card) } });
  advanced(false);
  expect(screen.getByLabelText("MCP server name")).toHaveProperty(
    "value",
    card.name,
  );
});

it("retains private MCP descriptors in Advanced while offering an explicit new native document", async () => {
  const content =
    '{"name":"Private","transport":"custom","extra":{"value":0.25}}';
  const test = await editor(content, "private");
  expect(screen.queryByLabelText("MCP server name")).toBeNull();
  expect(test.value().content).toBe(content);
  vi.spyOn(window, "confirm").mockReturnValue(true);
  change("MCP document", "registry");
  await screen.findByLabelText("MCP server name");
  expect(test.value().content).toBe("");
});

it("repairs incomplete Registry imports without inserting new-item defaults or changing the native version", async () => {
  const original = {
    name: "com.example/incomplete",
    description: "Metadata with incomplete distribution fields.",
    version: "imported",
    remotes: [{ type: "sse", url: "https://example.test/mcp", headers: [{}] }],
    packages: [{}, { transport: { type: "sse" } }],
  };
  const content = JSON.stringify(original);
  const test = await editor(content, "registry", "different");
  expect(test.changed).not.toHaveBeenCalled();
  expect(test.value().content).toBe(content);
  expect(screen.getByLabelText("Package 1 transport")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Package 1 identifier")).toHaveProperty(
    "value",
    "",
  );
  const missingURL = screen.getByLabelText(
    "Package 2 transport URL or template",
  ) as HTMLInputElement;
  expect(missingURL.value).toBe("");
  expect(missingURL.validationMessage).toBe("Enter an endpoint URL.");
  open("Endpoint 1 headers");
  change("Endpoint 1 headers 1 name", "X-Client");
  change("Package 1 custom registry type", "custom-registry");
  change("Package 1 identifier", "first");
  change("Package 1 transport", "stdio");
  change("Package 2 custom registry type", "pypi");
  change("Package 2 identifier", "second");
  change("Package 2 transport URL or template", "http://localhost:9000/sse");
  expect(test.document()).toEqual({
    ...original,
    remotes: [
      {
        ...original.remotes[0],
        headers: [{ name: "X-Client" }],
      },
    ],
    packages: [
      {
        registryType: "custom-registry",
        identifier: "first",
        transport: { type: "stdio" },
      },
      {
        registryType: "pypi",
        identifier: "second",
        transport: { type: "sse", url: "http://localhost:9000/sse" },
      },
    ],
  });
});

it("shows native and associated errors without truncating supplementary Unicode characters", async () => {
  const test = await editor();
  const name = screen.getByLabelText("MCP server name") as HTMLInputElement;
  fireEvent.blur(name);
  expect(name.getAttribute("aria-invalid")).toBe("true");
  expect(name.validationMessage).toBe("Enter a value.");
  change("MCP server name", "bad name");
  expect(name.validationMessage).toContain("namespace/server");
  change("MCP server name", "com.example/valid");
  expect(name.validationMessage).toBe("");
  const description = screen.getByLabelText(
    "MCP description",
  ) as HTMLTextAreaElement;
  fireEvent.invalid(description);
  change("MCP description", "🙂".repeat(101));
  expect(description.validationMessage).toContain("100");
  expect(test.document().description).toBe("🙂".repeat(101));
  change("MCP description", "🙂".repeat(100));
  expect(description.validationMessage).toBe("");
});

it("retains generated MCP content when a later version change follows the input limit", async () => {
  const test = await editor();
  change("MCP server name", "com.example/generated");
  change("MCP description", "x".repeat(2 * 1024 * 1024));
  const retained = test.value().content;
  expect(screen.queryByLabelText("MCP server name")).toBeNull();
  change("Catalog version", "2.0.0");
  expect(test.value().content).toBe(retained);
});

it("preserves the full document when generated formatting exceeds the guided input limit", async () => {
  const partial = { ...card, description: "" };
  partial.description = "x".repeat(
    2 * 1024 * 1024 - JSON.stringify(partial).length - 1,
  );
  const test = await editor(JSON.stringify(partial));
  click("Add endpoint");
  expect(screen.queryByLabelText("MCP server name")).toBeNull();
  expect(test.value().content.length).toBeGreaterThan(2 * 1024 * 1024);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    test.value().content,
  );
  expect(test.document().description).toBe(partial.description);
  const retained = test.value().content;
  change("Catalog version", "2.0.0");
  expect(test.value().content).toBe(retained);
});
