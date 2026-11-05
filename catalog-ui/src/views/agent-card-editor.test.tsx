// @vitest-environment jsdom
// Controlled editor units never start a Catalog or contact an advertised endpoint.
import { useState } from "react";
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import {
  RepresentationEditor,
  newRepresentation,
  representationInput,
  type RepresentationDraft,
} from "./representations";
import { nativeStarters } from "./native-starters";
import { screenLifecycle, change, click } from "../../tests/unit/screens";

screenLifecycle("/unit/resources/new");
const sample = JSON.parse(nativeStarters("agent")[0]!.content) as Record<
  string,
  unknown
>;

async function editor(content = "", version = "1.0.0") {
  const changed = vi.fn();
  let latest: RepresentationDraft;
  function Harness() {
    const [value, setValue] = useState({
      ...newRepresentation("agent"),
      content,
      editorID: "guided",
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
          kind="agent"
          index={0}
          value={value}
          version={label}
          onChange={(next) => {
            changed(next);
            setValue(next);
          }}
        />
      </>
    );
  }
  const view = render(<Harness />);
  if (
    content &&
    !Object.hasOwn(JSON.parse(content) as object, "supportedInterfaces")
  )
    await waitFor(() =>
      expect(
        screen.getByText("Advanced Agent Card JSON").closest("details")!.open,
      ).toBe(true),
    );
  else await screen.findByLabelText("Agent name");
  return {
    ...view,
    changed,
    value: () => latest!,
    card: () => JSON.parse(latest!.content) as Record<string, unknown>,
  };
}

function advanced(open: boolean) {
  const details = screen
    .getByText("Advanced Agent Card JSON")
    .closest("details")!;
  act(() => {
    details.open = open;
    fireEvent(details, new Event("toggle"));
  });
}

it("generates an unsigned Card from ordinary fields without version duplication or a mutation on mount", async () => {
  const test = await editor();
  expect(test.changed).not.toHaveBeenCalled();
  expect(test.value().content).toBe("");
  change("Agent name", "Order Agent");
  change("Agent description", "Explain an order without executing it.");
  change("Interface 1 HTTPS URL", "https://agents.example.test/order");
  change("Input format 1", "application/json");
  change("Output format 1", "text/plain");
  change("Ability 1 name", "Explain order status");
  click("Use suggested ability ID: explain-order-status");
  change("Ability 1 description", "Explain the order and expected next step.");
  change("Ability 1 tag 1", "orders, status");
  expect(representationInput(test.value()).data).toEqual({
    name: "Order Agent",
    description: "Explain an order without executing it.",
    version: "1.0.0",
    capabilities: {},
    supportedInterfaces: [
      {
        url: "https://agents.example.test/order",
        protocolBinding: "JSONRPC",
        protocolVersion: "1.0",
      },
    ],
    defaultInputModes: ["application/json"],
    defaultOutputModes: ["text/plain"],
    skills: [
      {
        id: "explain-order-status",
        name: "Explain order status",
        description: "Explain the order and expected next step.",
        tags: ["orders, status"],
      },
    ],
  });
  change("Catalog version", "\u0085 release 2026 Q4 \u0085");
  await waitFor(() => expect(test.card().version).toBe("release 2026 Q4"));
  expect(
    (test.card().supportedInterfaces as { protocolVersion: string }[])[0]!
      .protocolVersion,
  ).toBe("1.0");
  expect(test.container.querySelector("script")).toBeNull();
});

it("edits repeated interfaces, abilities, literal tags, custom media and only explicitly selected capabilities", async () => {
  const test = await editor();
  click("Add interface");
  change("Interface 1 HTTPS URL", "https://example.test/first");
  change("Interface 2 HTTPS URL", "https://example.test/second");
  change("Interface 2 protocol binding", "GRPC");
  change("Interface 2 A2A protocol version", "0.3");
  click("Add ability");
  change("Ability 1 name", "Original");
  change("Ability 1 ID", "original");
  change("Ability 2 name", "日本語");
  change("Ability 2 ID", "custom ability");
  change("Ability 2 description", "Second ability");
  click("Add ability 1 tag");
  change("Ability 1 tag 1", "first");
  change("Ability 1 tag 2", "comma, literal");
  click("Remove ability 1 tag 1");
  expect(screen.getByLabelText("Ability 1 tag 1")).toHaveProperty(
    "value",
    "comma, literal",
  );
  expect(document.activeElement).toBe(screen.getByLabelText("Ability 1 tag 1"));
  click("Add input format");
  change("Input format 2", "custom");
  change("Custom input format 2", "image/png");
  click("Add input format");
  change("Input format 3", "application/json");
  click("Remove input format 1");
  expect(document.activeElement).toBe(screen.getByLabelText("Input format 1"));
  expect(test.card().defaultInputModes).toEqual([
    "image/png",
    "application/json",
  ]);
  click("Add output format");
  change("Output format 2", "application/json");
  for (const name of [
    "Streaming responses",
    "Push notifications",
    "Authenticated extended Card",
  ])
    fireEvent.click(screen.getByLabelText(name));
  expect(test.card().capabilities).toEqual({
    streaming: true,
    pushNotifications: true,
    extendedAgentCard: true,
  });
  fireEvent.click(screen.getByLabelText("Streaming responses"));
  expect(test.card().capabilities).toHaveProperty("streaming", false);
  click("Remove interface 1");
  click("Remove ability 1");
  expect(test.card().supportedInterfaces).toEqual([
    {
      url: "https://example.test/second",
      protocolBinding: "GRPC",
      protocolVersion: "0.3",
    },
  ]);
  expect(test.card().skills).toEqual([
    {
      id: "custom ability",
      name: "日本語",
      description: "Second ability",
      tags: [""],
    },
  ]);
});

it.each(["interface", "ability"])(
  "focuses the added %s input",
  async (kind) => {
    await editor();
    click(`Add ${kind}`);
    expect(document.activeElement).toBe(
      screen.getByLabelText(
        kind === "interface" ? "Interface 2 HTTPS URL" : "Ability 2 name",
      ),
    );
  },
);

it.each(["interface", "ability"])(
  "focuses a remaining %s input after removal",
  async (kind) => {
    await editor();
    click(`Add ${kind}`);
    const remove = screen.getByRole("button", {
      name: `Remove ${kind} 2`,
    });
    remove.focus();
    fireEvent.click(remove);
    expect(document.activeElement).toBe(
      screen.getByLabelText(
        kind === "interface" ? "Interface 1 HTTPS URL" : "Ability 1 name",
      ),
    );
  },
);

it("keeps a document in Advanced when a row change exceeds the guided input limit", async () => {
  const limit = 2 * 1024 * 1024;
  const card = { ...sample, description: "" };
  card.description = "x".repeat(limit - JSON.stringify(card).length - 1);
  const content = JSON.stringify(card);
  expect(content.length).toBeLessThan(limit);
  const test = await editor(content);
  click("Add interface");
  expect(screen.queryByLabelText("Agent name")).toBeNull();
  expect(test.value().content.length).toBeGreaterThan(limit);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    test.value().content,
  );
  expect(test.card().supportedInterfaces).toHaveLength(2);
  expect(test.card().description).toBe(card.description);
  const retained = test.value().content;
  change("Catalog version", "2.0.0");
  expect(test.value().content).toBe(retained);
});

it("retains generated Agent content when a later version change follows the input limit", async () => {
  const test = await editor();
  change("Agent name", "Generated Agent");
  change("Agent description", "x".repeat(2 * 1024 * 1024));
  const retained = test.value().content;
  expect(screen.queryByLabelText("Agent name")).toBeNull();
  change("Catalog version", "2.0.0");
  expect(test.value().content).toBe(retained);
});

it("preserves imported versions, untouched security and extensions, ordered interfaces and ability overrides through guided edits", async () => {
  const card = {
    ...sample,
    version: "imported",
    provider: { organization: "Kept", url: "https://example.test" },
    capabilities: {
      extensions: [
        { uri: "https://example.test/ext", params: { exact: 0.25 } },
      ],
    },
    supportedInterfaces: [
      {
        url: "https://example.test/first",
        protocolBinding: "GRPC",
        protocolVersion: "1.0",
        tenant: "kept",
      },
      {
        url: "https://example.test/second",
        protocolBinding: "JSONRPC",
        protocolVersion: "1.0",
      },
    ],
    skills: [
      {
        id: "first",
        name: "First",
        description: "Kept",
        tags: ["literal, comma"],
        examples: ["Example"],
        inputModes: ["application/json"],
        securityRequirements: [],
      },
    ],
    securitySchemes: {
      bearer: { httpAuthSecurityScheme: { scheme: "Bearer" } },
    },
    securityRequirements: [{ schemes: {} }],
  };
  const text = JSON.stringify(card, null, 4);
  const test = await editor(text, "new-version");
  expect(test.value().content).toBe(text);
  change("Agent name", "Changed");
  expect(test.card()).toEqual({ ...card, name: "Changed" });
  change("Catalog version", "later");
  expect(test.card().version).toBe("imported");
  click("Use Version label in this Card");
  expect(test.card()).toEqual({ ...card, name: "Changed", version: "later" });
  change("Catalog version", "linked");
  await waitFor(() => expect(test.card().version).toBe("linked"));
  advanced(true);
  expect(
    screen.getByLabelText("Standards document (JSON)").closest("fieldset")!
      .disabled,
  ).toBe(false);
  const changedRaw = JSON.stringify({
    ...test.card(),
    version: "external",
    description: "Advanced edit",
  });
  change("Standards document (JSON)", changedRaw);
  advanced(false);
  expect(screen.getByLabelText("Agent description")).toHaveProperty(
    "value",
    "Advanced edit",
  );
  change("Catalog version", "do-not-overwrite");
  expect(test.value().content).toBe(changedRaw);
});

it("keeps signed fields read-only until explicit unsigned replacement and preserves advanced metadata", async () => {
  const card = {
    ...sample,
    signatures: [{ protected: "external", signature: "external" }],
    provider: { organization: "Kept", url: "https://example.test" },
  };
  const original = JSON.stringify(card);
  const test = await editor(original);
  expect(
    screen.getByLabelText("Agent name").closest("fieldset")!.disabled,
  ).toBe(true);
  fireEvent.change(screen.getByLabelText("Agent name"), {
    target: { value: "Forbidden generated edit" },
  });
  change("Catalog version", "2.0.0");
  expect(test.value().content).toBe(original);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "readOnly",
    true,
  );
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValue(true);
  click("Create unsigned copy");
  expect(test.value().content).toBe(original);
  advanced(true);
  advanced(false);
  expect(test.changed).not.toHaveBeenCalled();
  click("Create unsigned copy");
  const { signatures: _signatures, ...unsigned } = card;
  expect(test.card()).toEqual(unsigned);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "readOnly",
    false,
  );
  expect(confirm).toHaveBeenCalledTimes(2);
  change("Agent name", "Unsigned changed");
  expect(test.card()).toEqual({ ...unsigned, name: "Unsigned changed" });
  expect(test.card()).not.toHaveProperty("signatures");
});

it("requires unsigned replacement for signed Cards that use Advanced-only field names", async () => {
  const card = {
    name: "Alias Card",
    capabilities: {},
    supported_interfaces: [
      {
        url: "https://example.test/a2a",
        protocol_binding: "JSONRPC",
        protocol_version: "1.0",
      },
    ],
    signatures: [{ protected: "external", signature: "external" }],
  };
  const test = await editor(JSON.stringify(card));
  const raw = screen.getByLabelText("Standards document (JSON)");
  await waitFor(() => expect(raw).toHaveProperty("readOnly", true));
  expect(screen.queryByLabelText("Agent name")).toBeNull();
  vi.spyOn(window, "confirm").mockReturnValue(true);
  click("Create unsigned copy");
  const { signatures: _signatures, ...unsigned } = card;
  expect(test.card()).toEqual(unsigned);
  await waitFor(() => expect(raw).toHaveProperty("readOnly", false));
  expect(screen.queryByLabelText("Agent name")).toBeNull();
});

it("shows field-specific errors and clears native validity after corrections", async () => {
  await editor();
  const name = screen.getByLabelText("Agent name") as HTMLInputElement;
  fireEvent.blur(name);
  expect(name.getAttribute("aria-invalid")).toBe("true");
  expect(name.validationMessage).toBe("Enter a value.");
  expect(
    document.getElementById(
      name.getAttribute("aria-describedby")!.split(" ")[0]!,
    ),
  ).toHaveProperty("textContent", "Enter a value.");
  fireEvent.invalid(screen.getByLabelText("Agent description"));
  expect(
    screen.getByLabelText("Agent description").getAttribute("aria-invalid"),
  ).toBe("true");
  change("Agent name", "Corrected");
  expect(name.validationMessage).toBe("");
  expect(name.getAttribute("aria-invalid")).toBeNull();
  const url = screen.getByLabelText(
    "Interface 1 HTTPS URL",
  ) as HTMLInputElement;
  change("Interface 1 HTTPS URL", "http://example.test");
  fireEvent.blur(url);
  expect(url.validationMessage).toContain("absolute HTTPS");
  change("Interface 1 HTTPS URL", "https://example.test/a2a");
  expect(url.validationMessage).toBe("");
});

it("imports exact file text into Advanced, restores focus and populates guided fields without changing the imported version", async () => {
  const test = await editor();
  const content = JSON.stringify({ ...sample, version: "external" }, null, 4);
  const bytes = new TextEncoder().encode(content);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "agent.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  expect(test.value().documentStaged).toBe(true);
  click("Use loaded document");
  await waitFor(() =>
    expect(screen.getByLabelText("Standards document (JSON)")).toBe(
      document.activeElement,
    ),
  );
  expect(test.value().content).toBe(content);
  expect(test.value().documentStaged).toBeUndefined();
  advanced(false);
  await waitFor(() =>
    expect(screen.getByLabelText("Agent name")).toHaveProperty(
      "value",
      sample.name,
    ),
  );
  change("Catalog version", "do not overwrite");
  expect(test.value().content).toBe(content);
});

it("keeps a byte-identical imported Card independent from later Catalog version edits until explicitly relinked", async () => {
  const test = await editor();
  for (const [label, value] of [
    ["Agent name", "Order Agent"],
    ["Agent description", "Explain orders"],
    ["Interface 1 HTTPS URL", "https://example.test/orders"],
    ["Ability 1 name", "Explain"],
    ["Ability 1 ID", "explain"],
    ["Ability 1 description", "Explain an order"],
    ["Ability 1 tag 1", "orders"],
  ])
    change(label!, value!);
  const original = test.value().content;
  const bytes = new TextEncoder().encode(original);
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "same-card.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  click("Use loaded document");
  await waitFor(() =>
    expect(screen.getByLabelText("Standards document (JSON)")).toBe(
      document.activeElement,
    ),
  );
  change("Catalog version", "2.0.0");
  expect(test.value().content).toBe(original);
  advanced(false);
  click("Use Version label in this Card");
  expect(test.card().version).toBe("2.0.0");
  change("Catalog version", "3.0.0");
  await waitFor(() => expect(test.card().version).toBe("3.0.0"));
});

it("keeps invalid Advanced text and imported empty collections without replacing them with new defaults", async () => {
  const test = await editor(
    JSON.stringify({
      capabilities: {},
      supportedInterfaces: [],
      skills: [],
      defaultInputModes: [],
      defaultOutputModes: [],
    }),
    "",
  );
  expect(test.changed).not.toHaveBeenCalled();
  change("Agent name", "Imported partial");
  expect(test.card()).toEqual({
    name: "Imported partial",
    capabilities: {},
    supportedInterfaces: [],
    skills: [],
    defaultInputModes: [],
    defaultOutputModes: [],
  });
  change("Interface 1 protocol binding", "GRPC");
  change("Interface 1 A2A protocol version", "1.0");
  change("Ability 1 ID", "first");
  change("Input format 1", "application/json");
  change("Output format 1", "text/plain");
  expect(test.card().supportedInterfaces).toEqual([
    { protocolBinding: "GRPC", protocolVersion: "1.0" },
  ]);
  expect(test.card().skills).toEqual([{ id: "first" }]);
  expect(
    screen.getByRole("button", { name: "Use Version label in this Card" }),
  ).toHaveProperty("disabled", true);
  advanced(true);
  const raw = screen.getByLabelText("Standards document (JSON)");
  const invalid = '{"name":"first","name":"second"}';
  fireEvent.change(raw, { target: { value: invalid } });
  await waitFor(() => expect(screen.queryByLabelText("Agent name")).toBeNull());
  expect(test.value().content).toBe(invalid);
  advanced(false);
  expect(test.value().content).toBe(invalid);
  fireEvent.change(raw, { target: { value: JSON.stringify(sample) } });
  advanced(false);
  await screen.findByLabelText("Agent name");
  expect(test.card()).toEqual(sample);
});

it("moves complete interfaces with their custom inputs and errors without changing extensions or the imported version", async () => {
  const first = {
    url: "https://agents.example.test/one",
    protocolBinding: "vendor-binding",
    protocolVersion: "bad-version",
    tenant: "routing-one",
    extension: { keep: true },
  };
  const second = {
    url: "https://agents.example.test/two",
    protocolBinding: "GRPC",
    protocolVersion: "0.3",
    tenant: "routing-two",
  };
  const test = await editor(
    JSON.stringify({
      ...sample,
      version: "imported",
      supportedInterfaces: [first, second],
    }),
  );
  const protocol = screen.getByLabelText(
    "Interface 1 custom A2A protocol version",
  );
  fireEvent.blur(protocol);
  expect(protocol.getAttribute("aria-invalid")).toBe("true");
  expect(
    screen.getByRole("button", { name: "Move interface 1 up" }),
  ).toHaveProperty("disabled", true);
  click("Move interface 1 down");
  expect(test.card().supportedInterfaces).toEqual([second, first]);
  expect(test.card().version).toBe("imported");
  expect(screen.getByLabelText("Interface 2 custom A2A protocol version")).toBe(
    protocol,
  );
  expect(protocol.getAttribute("aria-invalid")).toBe("true");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Interface 2 HTTPS URL"),
  );
  expect(
    screen.getByRole("button", { name: "Move interface 2 down" }),
  ).toHaveProperty("disabled", true);
  change("Interface 2 custom A2A protocol version", "4.2");
  click("Move interface 2 up");
  expect(test.card().supportedInterfaces).toEqual([
    { ...first, protocolVersion: "4.2" },
    second,
  ]);
  change("Interface 1 protocol binding", "HTTP+JSON");
  expect(test.card().supportedInterfaces).toEqual([
    { ...first, protocolVersion: "4.2", protocolBinding: "HTTP+JSON" },
    second,
  ]);
});

it("keeps focus on a moved interface when identical objects leave the document bytes unchanged", async () => {
  const item = {
    url: "https://agents.example.test/duplicate",
    protocolBinding: "JSONRPC",
    protocolVersion: "1.0",
  };
  const original =
    JSON.stringify(
      { ...sample, supportedInterfaces: [item, { ...item }] },
      null,
      2,
    ) + "\n";
  const test = await editor(original);
  const firstURL = screen.getByLabelText("Interface 1 HTTPS URL");
  const move = screen.getByRole("button", { name: "Move interface 1 down" });
  move.focus();
  fireEvent.click(move);
  expect(test.value().content).toBe(original);
  expect(screen.getByLabelText("Interface 2 HTTPS URL")).toBe(firstURL);
  expect(document.activeElement).toBe(firstURL);
});
