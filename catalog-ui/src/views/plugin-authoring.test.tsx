// @vitest-environment jsdom
// These units use controlled memory drafts. No binary, database or server is required.
import { useState } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { screenLifecycle, change, click } from "../../tests/unit/screens";
import PluginAuthoring from "./PluginAuthoring";
import {
  newRepresentation,
  RepresentationList,
  RepresentationEditor,
  representationInput,
  type RepresentationDraft,
} from "./representations";
import {
  jsonText,
  newPluginPreparation,
  pluginFiles,
  pluginManifest,
  pluginMCP,
} from "./plugin-files";
import { pluginStarterFiles } from "./plugin-starters";

screenLifecycle("/unit/versions/new");
const create = vi.fn((_blob: Blob) => "blob:plugin-file");
const revoke = vi.fn();
beforeEach(() => {
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    },
  );
});
function editor(initial: Partial<RepresentationDraft> = {}, version?: string) {
  let latest: RepresentationDraft;
  const changed = vi.fn();
  function Harness() {
    const [value, setValue] = useState({
      ...newRepresentation("plugin"),
      editorID: "plugin",
      ...initial,
    });
    latest = value;
    return (
      <PluginAuthoring
        value={value}
        onChange={(next) => {
          changed(next);
          setValue(next);
        }}
        version={version}
      />
    );
  }
  return {
    ...render(<Harness />),
    value: () => latest!,
    changed,
    prep: () => latest!.pluginPreparation!,
    manifest: () => pluginManifest(latest!.pluginPreparation!.manifest),
    mcp: () => pluginMCP(latest!.pluginPreparation!.mcp),
  };
}

it("identifies missing Plugin and command fields and retains preparation after correction", () => {
  const test = editor();
  change("Plugin input", "prepare");
  const name = screen.getByLabelText("Plugin name") as HTMLInputElement;
  expect(name.required).toBe(true);
  expect(name.hasAttribute("aria-invalid")).toBe(false);
  fireEvent.blur(name);
  expect(name.getAttribute("aria-invalid")).toBe("true");
  expect(name.validationMessage).toBe("Enter plugin name.");
  change("Plugin name", "review-tools");
  expect(name.hasAttribute("aria-invalid")).toBe(false);
  click("Add MCP server");
  const serverName = screen.getByLabelText("Server 1 name") as HTMLInputElement;
  change("Server 1 name", "");
  fireEvent.invalid(serverName);
  expect(serverName.getAttribute("aria-invalid")).toBe("true");
  expect(serverName.validationMessage).toBe("Enter server 1 name.");
  change("Server 1 name", "server");
  expect(serverName.hasAttribute("aria-invalid")).toBe(false);
  const command = screen.getByLabelText("Command") as HTMLInputElement;
  fireEvent.invalid(command);
  expect(command.getAttribute("aria-invalid")).toBe("true");
  change("Command", "python");
  expect(command.validity.valid).toBe(true);
  expect(command.hasAttribute("aria-invalid")).toBe(false);
  expect(test.manifest().name).toBe("review-tools");
  expect(test.mcp().mcpServers.server).toMatchObject({
    type: "stdio",
    command: "python",
  });
});
function prepare() {
  change("Plugin input", "prepare");
}
function rename(label: string, value: string) {
  fireEvent.change(screen.getByRole("textbox", { name: label }), {
    target: { value },
  });
}

it("focuses archive inputs before typing and does not steal checksum focus on the next frame", () => {
  const frames: FrameRequestCallback[] = [];
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    frames.push(callback);
    return frames.length;
  });
  const test = editor();
  prepare();
  click("Continue with archive inputs");
  const url = screen.getByRole("textbox", { name: "Plugin archive URL" });
  expect(document.activeElement).toBe(url);
  rename("Plugin archive URL", "https://example.test/archive.zip");
  const checksum = screen.getByRole("textbox", {
    name: "Expected archive checksum (optional)",
  });
  checksum.focus();
  act(() => {
    for (const frame of frames) frame(0);
  });
  expect(document.activeElement).toBe(checksum);
  const digest = "sha256:" + "a".repeat(64);
  rename("Expected archive checksum (optional)", digest);
  expect(representationInput(test.value())).toMatchObject({
    url: "https://example.test/archive.zip",
    expectedDigest: digest,
  });
});

it("keeps URL, checksum and format through preparation and returns only archive inputs", () => {
  const checksum = "sha256:" + "a".repeat(64);
  const test = editor({
    url: "https://example.test/archive",
    expectedDigest: checksum,
  });
  expect(test.changed).not.toHaveBeenCalled();
  change("Plugin archive format", "application/agent-plugins+gzip");
  expect(test.value().mediaType).toBe("application/agent-plugins+gzip");
  expect(test.value().expectedDigest).toBe(checksum);
  prepare();
  expect(() => representationInput(test.value())).toThrow("Package and host");
  rename("Plugin name", "my-plugin");
  rename("Plugin version (optional)", "release-1");
  rename("Plugin description (optional)", "A useful Plugin");
  rename("Plugin license (optional)", "Apache-2.0");
  rename("Plugin license (optional)", "");
  click("Preview and download Plugin files");
  expect(
    screen
      .getByRole("link", { name: "Download plugin.json" })
      .getAttribute("download"),
  ).toBe("plugin.json");
  expect(
    screen.getByRole("region", { name: "Prepared Plugin files" }).textContent,
  ).toContain("zip -r ../my-plugin.zip plugin.json");
  click("Hide prepared files");
  expect(
    screen.queryByRole("region", { name: "Prepared Plugin files" }),
  ).toBeNull();
  click("Continue with archive inputs");
  expect(test.value().expectedDigest).toBe(checksum);
  expect(test.value().url).toBe("https://example.test/archive");
  expect(representationInput(test.value())).toEqual({
    role: "primary",
    mediaType: "application/agent-plugins+gzip",
    url: "https://example.test/archive",
    expectedDigest: checksum,
  });
  rename("Plugin archive URL", "https://example.test/new-archive");
  expect(test.value().expectedDigest).toBe(checksum);
  rename("Expected archive checksum (optional)", "");
  expect(representationInput(test.value())).not.toHaveProperty(
    "expectedDigest",
  );
  prepare();
  expect(test.manifest()).toMatchObject({
    name: "my-plugin",
    version: "release-1",
    description: "A useful Plugin",
  });
  expect(test.manifest()).not.toHaveProperty("license");
});

it("keeps raw manifest text and advanced values, and downloads only safe paths", () => {
  const test = editor({}, "\u0085 2.0.0 \u0085");
  prepare();
  click("Preview and download Plugin files");
  expect(screen.getByRole("alert").textContent).toContain("Plugin name");
  click("Edit Plugin manifest JSON");
  rename("Plugin manifest JSON", "{invalid");
  expect(test.prep().manifest).toBe("{invalid");
  click("Use Plugin manifest fields");
  expect(
    (
      screen.getByRole("textbox", {
        name: "Plugin manifest JSON",
      }) as HTMLInputElement
    ).value,
  ).toBe("{invalid");
  const text = jsonText({
    ...pluginManifest(newPluginPreparation().manifest),
    name: "tool",
    author: { name: "Original author" },
    keywords: ["one", "two"],
    extensions: { "org.example.test": { value: 0.5 } },
  });
  rename("Plugin manifest JSON", text);
  rename("Plugin description (optional)", "Updated");
  click("Use Catalog version 2.0.0 in manifest");
  expect(test.manifest()).toMatchObject({
    author: { name: "Original author" },
    keywords: ["one", "two"],
    version: "2.0.0",
    extensions: { "org.example.test": { value: 0.5 } },
  });
  expect(pluginFiles(test.prep())).toHaveLength(1);
  rename("Plugin name", "../unsafe");
  expect(screen.getByRole("alert").textContent).toContain("Plugin name");
  expect(
    screen.queryByRole("link", { name: "Download plugin.json" }),
  ).toBeNull();
  test.unmount();
  expect(revoke).toHaveBeenCalled();
});

it("generates named MCP rows with literal ordered arguments and non-secret optional fields", () => {
  const test = editor();
  prepare();
  rename("Plugin name", "tools");
  click("Add MCP server");
  rename("Server 1 name", "first");
  rename("Command", "./bin/tool");
  click("Add argument");
  rename("Argument 1", "two words");
  click("Add argument");
  rename("Argument 2", "${PLUGIN_ROOT}/asset");
  expect(test.mcp().mcpServers.first!.args).toEqual([
    "two words",
    "${PLUGIN_ROOT}/asset",
  ]);
  click("Remove argument 1");
  expect(test.mcp().mcpServers.first!.args).toEqual(["${PLUGIN_ROOT}/asset"]);
  rename("Working directory (optional)", "${PLUGIN_DATA}/cache");
  click("Add Environment variable");
  rename("Environment variable 1 name", "PUBLIC_SETTING");
  rename("Environment variable 1 value", "${USER_SETTING}");
  click("Add Environment variable");
  click("Add Environment variable");
  expect(screen.getByRole("alert").textContent).toContain(
    "Complete the unnamed",
  );
  rename("Environment variable 2 name", "PUBLIC_SETTING");
  expect(screen.getByRole("alert").textContent).toContain("different name");
  rename("Environment variable 2 name", "OTHER");
  rename("Environment variable 2 value", "text");
  click("Remove Environment variable 2");
  expect(test.mcp().mcpServers.first).toMatchObject({
    cwd: "${PLUGIN_DATA}/cache",
    env: { PUBLIC_SETTING: "${USER_SETTING}" },
  });
  rename("Working directory (optional)", "");
  expect(test.mcp().mcpServers.first).not.toHaveProperty("cwd");
});

it("keeps MCP names distinct and explicitly confirms transport changes and removal", () => {
  const test = editor();
  prepare();
  rename("Plugin name", "tools");
  click("Add MCP server");
  rename("Server 1 name", "first");
  rename("Command", "tool");
  click("Add MCP server");
  click("Add MCP server");
  expect(Object.keys(test.mcp().mcpServers)).toEqual([
    "first",
    "server",
    "server-2",
  ]);
  rename("Server 3 name", "first");
  expect(screen.getByRole("alert").textContent).toContain(
    "different name for each MCP",
  );
  rename("Server 3 name", "third");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  change("Server 3 transport", "sse");
  expect(test.mcp().mcpServers.third!.type).toBe("stdio");
  confirm.mockReturnValue(true);
  change("Server 3 transport", "sse");
  rename("MCP endpoint URL", "http://localhost:3000/events");
  click("Add Header");
  rename("Header 1 name", "X-Client");
  rename("Header 1 value", "${CLIENT_ID}");
  expect(test.mcp().mcpServers.third).toEqual({
    type: "sse",
    url: "http://localhost:3000/events",
    headers: { "X-Client": "${CLIENT_ID}" },
  });
  change("Server 3 transport", "streamable-http");
  rename("MCP endpoint URL", "https://example.test/mcp");
  expect(test.mcp().mcpServers.third!.type).toBe("streamable-http");
  change("Server 3 transport", "stdio");
  const third = screen.getByRole("group", { name: "MCP server 3" });
  fireEvent.change(within(third).getByRole("textbox", { name: "Command" }), {
    target: { value: "tool" },
  });
  confirm.mockReturnValue(false);
  click("Remove MCP server 3");
  expect(test.mcp().mcpServers).toHaveProperty("third");
  confirm.mockReturnValue(true);
  click("Remove MCP server 3");
  expect(test.mcp().mcpServers).not.toHaveProperty("third");
  confirm.mockReturnValue(false);
  click("Remove MCP configuration");
  expect(test.prep().mcp).not.toBe("");
  confirm.mockReturnValue(true);
  click("Remove MCP configuration");
  expect(test.prep().mcp).toBe("");
});

it("preserves invalid or unsupported MCP JSON and all explicitly edited optional values", () => {
  const test = editor({ mediaType: "application/custom-plugin" });
  expect(
    screen.getByRole("option", { name: "application/custom-plugin" }),
  ).toBeTruthy();
  prepare();
  click("Edit Plugin MCP JSON");
  rename("Plugin MCP JSON", '{"mcpServers":{}}');
  click("Use Plugin MCP fields");
  expect(
    (
      screen.getByRole("textbox", {
        name: "Plugin MCP JSON",
      }) as HTMLInputElement
    ).value,
  ).toBe('{"mcpServers":{}}');
  rename("Plugin MCP JSON", jsonText(pluginMCP("")));
  click("Add MCP server");
  rename("Command", "tool");
  expect(test.mcp().mcpServers.server).toEqual({
    type: "stdio",
    command: "tool",
  });
});

it("reuses Skill fields without a handoff and preserves Advanced component text", async () => {
  const test = editor();
  prepare();
  rename("Plugin name", "skills");
  click("Add Skill component");
  await screen.findByRole("textbox", { name: "Skill name" });
  change("Skill name", "prepare-release");
  change("Skill description", "Prepare a release checklist.");
  change("Skill instructions (Markdown)", "# Release\n\nReview the changes.");
  expect(screen.queryByText("Skill package handoff")).toBeNull();
  expect(pluginFiles(test.prep())[1]!.path).toBe(
    "skills/prepare-release/SKILL.md",
  );
  click("Edit component SKILL.md");
  const raw = pluginStarterFiles[2]!.content + "\nExact text.\n";
  rename("Component SKILL.md", raw);
  expect(test.prep().skills[0]!.content).toBe(raw);
  click("Use Skill fields");
  expect(
    (
      screen.getByRole("textbox", {
        name: "Skill name",
      }) as HTMLInputElement
    ).value,
  ).toBe("prepare-release");
  click("Edit component SKILL.md");
  rename("Component SKILL.md", "invalid frontmatter");
  click("Use Skill fields");
  click("Open Advanced SKILL.md");
  expect(
    (
      screen.getByRole("textbox", {
        name: "Component SKILL.md",
      }) as HTMLInputElement
    ).value,
  ).toBe("invalid frontmatter");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Remove Skill component");
  expect(test.prep().skills).toHaveLength(1);
  confirm.mockReturnValue(true);
  click("Remove Skill component");
  expect(test.prep().skills).toEqual([]);
});

it("replaces only prepared files when an example is explicitly confirmed", () => {
  const test = editor({
    url: "https://example.test/existing",
    expectedDigest: "sha256:" + "b".repeat(64),
  });
  prepare();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Use Plugin example");
  expect(test.prep().skills).toEqual([]);
  confirm.mockReturnValue(true);
  click("Use Plugin example");
  expect(pluginFiles(test.prep())).toEqual(pluginStarterFiles);
  expect(test.value().url).toBe("https://example.test/existing");
  expect(test.value().expectedDigest).toBe("sha256:" + "b".repeat(64));
  click("Preview and download Plugin files");
  expect(screen.getAllByRole("link", { name: /^Download / })).toHaveLength(3);
  change("Plugin input", "archive");
  act(() => {
    document.getElementById("plugin-url")!.focus();
  });
  expect(document.activeElement).toBe(
    screen.getByRole("textbox", { name: "Plugin archive URL" }),
  );
});

it("edits one of two Skill components and safely opens incomplete Advanced MCP server fields", async () => {
  const prep = newPluginPreparation();
  prep.preparing = true;
  prep.skills = [
    { id: "first", content: pluginStarterFiles[2]!.content },
    { id: "second", content: pluginStarterFiles[2]!.content },
  ];
  prep.mcp = jsonText({
    ...pluginMCP(""),
    mcpServers: {
      missingCommand: { type: "stdio" },
      missingURL: { type: "sse" },
    },
  });
  const test = editor({ pluginPreparation: prep });
  await screen.findAllByRole("textbox", { name: "Skill name" });
  const skills = screen.getAllByRole("group", { name: "Skill component" });
  fireEvent.change(
    within(skills[1]!).getByRole("textbox", { name: "Skill name" }),
    { target: { value: "second-skill" } },
  );
  expect(test.prep().skills[0]!.content).toBe(prep.skills[0]!.content);
  expect(test.prep().skills[1]!.content).toContain('name: "second-skill"');
  expect(screen.getByRole("textbox", { name: "Command" })).toHaveProperty(
    "value",
    "",
  );
  expect(
    screen.getByRole("textbox", { name: "MCP endpoint URL" }),
  ).toHaveProperty("value", "");
});

it("keeps keyboard focus on the new Plugin input after adding or removing a representation", async () => {
  function Rows() {
    const [value, setValue] = useState([newRepresentation("plugin")]);
    return (
      <RepresentationList kind="plugin" value={value} onChange={setValue} />
    );
  }
  render(<Rows />);
  await screen.findByRole("combobox", { name: "Plugin input" });
  click("Add representation");
  const inputs = await screen.findAllByRole("combobox", {
    name: "Plugin input",
  });
  expect(document.activeElement).toBe(inputs[1]);
  click("Remove representation 2");
  expect(document.activeElement).toBe(inputs[0]);
});

it("preserves the active row while authors type integer-like server and environment names", () => {
  const prep = newPluginPreparation();
  prep.preparing = true;
  prep.mcp = jsonText({
    ...pluginMCP(""),
    mcpServers: {
      first: { type: "stdio", command: "first-tool" },
      second: {
        type: "stdio",
        command: "second-tool",
        env: { FIRST: "one", SECOND: "two" },
      },
    },
  });
  const test = editor({ pluginPreparation: prep });
  const name = screen.getByRole("textbox", { name: "Server 2 name" });
  name.focus();
  fireEvent.change(name, { target: { value: "1" } });
  expect(document.activeElement).toBe(name);
  expect(name).toHaveProperty("value", "1");
  expect(test.mcp().mcpServers["1"]!.command).toBe("second-tool");
  expect(screen.getByRole("textbox", { name: "Server 1 name" })).toHaveProperty(
    "value",
    "first",
  );
  fireEvent.change(name, { target: { value: "12" } });
  expect(name).toHaveProperty("value", "12");
  const row = screen.getByRole("group", { name: "MCP server 2" });
  const envName = within(row).getByRole("textbox", {
    name: "Environment variable 2 name",
  });
  envName.focus();
  fireEvent.change(envName, { target: { value: "1" } });
  expect(document.activeElement).toBe(envName);
  expect(envName).toHaveProperty("value", "1");
  expect(test.mcp().mcpServers["12"]!.env).toEqual({
    FIRST: "one",
    "1": "two",
  });
});

it("protects prepared Plugin files when an unchanged representation is remounted", async () => {
  const preparation = {
    ...newPluginPreparation(),
    preparing: true,
    manifest: pluginStarterFiles[0]!.content,
  };
  const remove = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <RepresentationEditor
      kind="plugin"
      index={1}
      value={{ ...newRepresentation("plugin"), pluginPreparation: preparation }}
      onChange={vi.fn()}
      onRemove={remove}
    />,
  );
  await screen.findByRole("combobox", { name: "Plugin input" });
  click("Remove representation 2");
  expect(confirm).toHaveBeenCalledOnce();
  expect(remove).not.toHaveBeenCalled();
  confirm.mockReturnValue(true);
  click("Remove representation 2");
  expect(remove).toHaveBeenCalledOnce();
});

it("downloads exact Advanced files when Skill YAML cannot use guided fields", async () => {
  const yaml =
    "---\nname: review-notes\ndescription: |\n  Review the release notes.\n  Summarize the change.\n---\n\n# Review\n\nRead the supplied notes.\n";
  const preparation = {
    ...newPluginPreparation(),
    preparing: true,
    manifest: pluginStarterFiles[0]!.content,
    skills: [{ id: "advanced", content: yaml }],
  };
  const test = editor({ pluginPreparation: preparation });
  await screen.findByRole("button", { name: "Open Advanced SKILL.md" });
  click("Open Advanced SKILL.md");
  expect(
    screen.getByRole("link", { name: "Download SKILL.md" }),
  ).toHaveProperty("download", "SKILL.md");
  click("Edit Plugin manifest JSON");
  expect(
    screen.getByRole("link", { name: "Download plugin.json" }),
  ).toHaveProperty("download", "plugin.json");
  click("Edit Plugin MCP JSON");
  const mcp = jsonText(pluginMCP(""));
  rename("Plugin MCP JSON", mcp);
  expect(
    screen.getByRole("link", { name: "Download mcp.json" }),
  ).toHaveProperty("download", "mcp.json");
  const texts = await Promise.all(
    create.mock.calls.map(
      (args) =>
        new Promise<string>((resolve) => {
          const reader = new FileReader();
          reader.onload = () => resolve(reader.result as string);
          reader.readAsText(args[0] as Blob);
        }),
    ),
  );
  expect(texts).toContain(yaml);
  expect(texts).toContain(preparation.manifest);
  expect(texts).toContain(mcp);
  expect(test.prep().skills[0]!.content).toBe(yaml);
  expect(() => representationInput(test.value())).toThrow("Package and host");
});

it("identifies retained Plugin documents and files that require the original archive", () => {
  const test = editor({
    pluginPreparation: {
      ...newPluginPreparation(),
      sourceFiles: [
        { path: "plugin.json", retained: true },
        { path: "scripts/tool.py", retained: false },
      ],
    },
  });
  const summary = screen.getByText("Original Plugin files");
  fireEvent.click(summary);
  expect(screen.getByText("plugin.json")).toBeTruthy();
  expect(screen.getByText("scripts/tool.py")).toBeTruthy();
  const inventory = summary.closest("details")!;
  expect(
    within(inventory).getByText("plugin.json").closest("li")!.textContent,
  ).toContain("Retained document");
  expect(
    within(inventory).getByText("scripts/tool.py").closest("li")!.textContent,
  ).toContain("Original package required");
  expect(test.changed).not.toHaveBeenCalled();
});
