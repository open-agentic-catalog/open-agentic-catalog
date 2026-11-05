// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
  newRepresentation,
  representationInput,
  RepresentationEditor,
} from "./representations";
import { RepresentationContent } from "./Versions";
import type { WorkspaceProps } from "./workspace";
import type { components } from "../api/schema";
import { sourceScope } from "./federation-inputs";

afterEach(cleanup);

it("authors a Plugin as an external archive with an optional verified checksum", async () => {
  function Editor() {
    const [value, setValue] = useState(newRepresentation("plugin"));
    return (
      <RepresentationEditor
        kind="plugin"
        index={0}
        value={value}
        onChange={setValue}
      />
    );
  }
  render(<Editor />);
  await screen.findByLabelText("Plugin archive URL");
  expect(screen.queryByRole("option", { name: "JSON document" })).toBeNull();
  expect(
    screen.queryByRole("option", { name: /Skill package and/ }),
  ).toBeNull();
  fireEvent.change(screen.getByLabelText("Plugin archive URL"), {
    target: { value: "https://example.com/tools.zip" },
  });
  const checksum = "sha256:" + "a".repeat(64);
  fireEvent.change(
    screen.getByLabelText("Expected archive checksum (optional)"),
    { target: { value: checksum } },
  );
  expect(
    (
      screen.getByLabelText(
        "Expected archive checksum (optional)",
      ) as HTMLInputElement
    ).value,
  ).toBe(checksum);
  const draft = {
    ...newRepresentation("plugin"),
    url: "https://example.com/tools.zip",
  };
  expect(representationInput(draft)).toEqual({
    role: "primary",
    mediaType: "application/agent-plugins+zip",
    url: draft.url,
  });
  expect(
    representationInput({ ...draft, expectedDigest: " " + checksum + " " }),
  ).toHaveProperty("expectedDigest", checksum);
  expect(
    representationInput({ ...draft, expectedDigest: " " }),
  ).not.toHaveProperty("expectedDigest");
  expect(() =>
    representationInput({ ...draft, expectedDigest: "incorrect" }),
  ).toThrow(/SHA-256/);
  expect(
    sourceScope({ kinds: ["agent", "mcp_server", "skill", "plugin"] }).kinds,
  ).toHaveLength(4);
});

it("shows inert Plugin evidence and a safe archive handoff without network reads", () => {
  const props = {
    client: {},
    runtime: {},
    access: {},
    id: "revision",
    onFailure: vi.fn(),
  } as unknown as WorkspaceProps;
  const item: components["schemas"]["Representation"] = {
    id: "representation",
    role: "primary",
    mediaType: "application/agent-plugins+zip",
    profileId: "agent-plugins-1.0.0-1fc1b627",
    url: "https://example.com/tools.zip",
    contentDigest: "sha256:" + "a".repeat(64),
    size: 500,
    validationState: "valid",
    plugin: {
      rootDirectory: "",
      expandedSize: 40,
      files: [
        {
          path: "plugin.json",
          size: 40,
          digest: "sha256:" + "b".repeat(64),
          executable: false,
        },
      ],
      documents: [
        { path: "plugin.json", content: '{"name":"<script>inert</script>"}' },
      ],
      components: [
        {
          kind: "skill",
          name: "read",
          path: "skills/read/SKILL.md",
          findings: [
            {
              code: "INVALID",
              severity: "error",
              message: "Skill name does not match",
            },
          ],
        },
        { kind: "mcp_server", name: "tools", path: "mcp.json" },
      ],
      assessments: {
        malware: "not_assessed",
        secrets: "not_assessed",
        license: "not_assessed",
        sbom: "not_assessed",
        provenance: "not_assessed",
      },
    },
  };
  const view = render(<RepresentationContent {...props} item={item} />);
  expect(
    screen
      .getByRole("link", { name: "Open external archive" })
      .getAttribute("href"),
  ).toBe(item.url);
  expect(screen.getByText("Skill name does not match")).toBeTruthy();
  expect(screen.getByText(/Components are not installed/)).toBeTruthy();
  expect(document.querySelector("script")).toBeNull();
  expect(screen.queryByText("Read retained document")).toBeNull();
  const { components: _components, ...withoutComponents } = item.plugin!;
  for (const components of [[], undefined]) {
    view.rerender(
      <RepresentationContent
        {...props}
        item={{
          ...item,
          url: "javascript:alert(1)",
          plugin: {
            ...withoutComponents,
            ...(components === undefined ? {} : { components }),
          },
        }}
      />,
    );
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("No portable components declared.")).toBeTruthy();
  }
});
