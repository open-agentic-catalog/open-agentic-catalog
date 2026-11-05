import { expect, it } from "vitest";
import { pluginStarterFiles } from "./plugin-starters";
import {
  jsonText,
  newPluginPreparation,
  pluginEndpointError,
  pluginFiles,
  pluginManifest,
  pluginMCP,
  pluginMCPSchema,
  pluginPackaging,
  pluginServerError,
} from "./plugin-files";

it("prepares a minimal Plugin and exact optional components without converting source text", () => {
  const prep = newPluginPreparation();
  expect(prep.preparing).toBe(false);
  expect(() => pluginFiles(prep)).toThrow("Plugin name");
  prep.manifest = jsonText({
    ...pluginManifest(prep.manifest),
    name: "my.plugin",
    version: "release-1",
    author: { name: "An author" },
    extensions: { "org.example.test": { value: 0.5 } },
  });
  const files = pluginFiles(prep);
  expect(files).toEqual([{ path: "plugin.json", content: prep.manifest }]);
  expect(pluginPackaging("my.plugin", files)).toBe(
    "zip -r ../my.plugin.zip plugin.json\ntar -czf ../my.plugin.tar.gz plugin.json",
  );
  prep.manifest = pluginStarterFiles[0]!.content;
  prep.mcp = pluginStarterFiles[1]!.content;
  prep.skills = [{ id: "one", content: pluginStarterFiles[2]!.content }];
  expect(pluginFiles(prep)).toEqual(pluginStarterFiles);
  prep.skills[0]!.content = prep.skills[0]!.content.replace(
    "---\n\n#",
    'metadata:\n  version: "1"\n---\n\n#',
  );
  expect(pluginFiles(prep)[2]!.content).toContain("metadata:");
  expect(pluginPackaging("release-workflow", pluginFiles(prep))).toContain(
    "plugin.json mcp.json skills",
  );
  prep.skills.push({ ...prep.skills[0]!, id: "two" });
  expect(() => pluginFiles(prep)).toThrow("different name");
  prep.skills = [{ id: "bad", content: "not frontmatter" }];
  expect(() => pluginFiles(prep)).toThrow();
  prep.skills = [
    {
      id: "bad",
      content: '---\nname: "../bad"\ndescription: "A description"\n---\nBody\n',
    },
  ];
  expect(() => pluginFiles(prep)).toThrow("lowercase");
  prep.skills = [{ id: "empty", content: "" }];
  expect(() => pluginFiles(prep)).toThrow();
});

it.each(["", "a--b", "a..b", "-bad", "Bad", "a".repeat(65)])(
  "refuses unsafe Plugin path %s",
  (name) => {
    const prep = newPluginPreparation();
    prep.manifest = jsonText({ ...pluginManifest(prep.manifest), name });
    expect(() => pluginFiles(prep)).toThrow("Plugin name");
  },
);

it("recognizes only editable shapes and preserves advanced fields", () => {
  const prep = newPluginPreparation();
  for (const patch of [
    { $schema: "wrong" },
    { name: 1 },
    { version: null },
    { license: [] },
    { description: {} },
  ])
    expect(() =>
      pluginManifest(jsonText({ ...pluginManifest(prep.manifest), ...patch })),
    ).toThrow("pinned Plugin");
  expect(() => pluginManifest('{"name":"one","name":"two"}')).toThrow();
  expect(pluginMCP("").mcpServers).toEqual({});
  for (const data of [
    { $schema: "card", mcpServers: {} },
    { $schema: pluginMCPSchema, mcpServers: [] },
  ])
    expect(() => pluginMCP(jsonText(data))).toThrow("named mcpServers");
  for (const server of [
    null,
    { type: "wrong" },
    { type: "stdio", command: 1 },
    { type: "stdio", args: "bad" },
    { type: "stdio", args: [1] },
    { type: "stdio", env: [] },
    { type: "sse", headers: { a: 1 } },
  ])
    expect(() =>
      pluginMCP(
        jsonText({ $schema: pluginMCPSchema, mcpServers: { test: server } }),
      ),
    ).toThrow("Advanced MCP JSON");
  const config = {
    $schema: pluginMCPSchema,
    mcpServers: {
      one: {
        type: "stdio",
        command: "tool",
        args: ["two words", "${PLUGIN_ROOT}/asset"],
        env: { VALUE: "${USER_VALUE}" },
        cwd: "./a/../b",
      },
      two: {
        type: "sse",
        url: "http://[0:0:0:0:0:0:0:1]/events",
        headers: { "X-Value": "${VALUE}" },
      },
    },
  };
  expect(pluginMCP(jsonText(config))).toEqual(config);
  prep.manifest = jsonText({ ...pluginManifest(prep.manifest), name: "test" });
  prep.mcp = jsonText(config);
  expect(pluginFiles(prep)[1]!.content).toBe(prep.mcp);
  expect(pluginPackaging("test", pluginFiles(prep))).toContain(
    "plugin.json mcp.json",
  );
  prep.mcp = jsonText({
    ...config,
    mcpServers: { "": { type: "stdio", command: "tool" } },
  });
  expect(() => pluginFiles(prep)).toThrow("name for each");
  prep.mcp = jsonText({
    ...config,
    mcpServers: { test: { type: "stdio", command: "bad command" } },
  });
  expect(() => pluginFiles(prep)).toThrow("test: Enter one executable");
});

it.each([
  "https://example.test/mcp",
  "http://localhost:3000/mcp",
  "http://127.0.0.1/mcp",
  "http://127.255.255.255/mcp",
  "http://[::1]/mcp",
  "http://[::1%25lo0]/mcp",
  "HTTP://localhost/mcp",
  "HTTPS://EXAMPLE.TEST/mcp",
  "http://[::ffff:127.0.0.1]/mcp",
  "http://[0:0:0:0:0:0:0:1]/mcp",
])("accepts the Plugin metadata URL %s without a request", (url) =>
  expect(pluginEndpointError(url)).toBe(""),
);
it.each([
  "",
  "not a url",
  "http://example.test/mcp",
  "http://127.1/mcp",
  "http://2130706433/mcp",
  "http://127.00.0.1/mcp",
  "http://127.256.0.1/mcp",
  "http://LOCALHOST/mcp",
  "https://user:password@example.test/mcp",
  "https://example.test/#part",
  "https://example.test/with space",
  "https://example.test\\evil/mcp",
  "ftp://example.test/mcp",
  "http://[::2]/mcp",
  "http://[::2%25lo0]/mcp",
])("refuses the Plugin metadata URL %s", (url) =>
  expect(pluginEndpointError(url)).not.toBe(""),
);

it("checks executable containment, reserved variables, working directories and safe headers", () => {
  for (const command of ["tool", "./tool", "./a/../tool", "./a/tool"])
    expect(pluginServerError({ type: "stdio", command })).toBe("");
  for (const command of [
    undefined,
    "",
    "bad command",
    "bad\u0085command",
    "${COMMAND}",
    "../tool",
    "./../tool",
    "./a:b",
    "./bad\\tool",
    "./bad\0tool",
    "/bin/tool",
  ])
    expect(pluginServerError({ type: "stdio", command })).toContain(
      "executable",
    );
  for (const env of [{ PLUGIN_ROOT: "x" }, { PLUGIN_DATA: "x" }])
    expect(
      pluginServerError({ type: "stdio", command: "tool", env }),
    ).toContain("Do not set");
  for (const cwd of [
    undefined,
    "",
    "./",
    "./a/../b",
    "${PLUGIN_ROOT}",
    "${PLUGIN_DATA}/a",
  ])
    expect(pluginServerError({ type: "stdio", command: "tool", cwd })).toBe("");
  for (const cwd of [
    "../bad",
    "./../bad",
    "${PLUGIN_ROOT}/../bad",
    "${PLUGIN_DATA}/${VALUE}",
    "./a:b",
    "./bad\npath",
  ])
    expect(
      pluginServerError({ type: "stdio", command: "tool", cwd }),
    ).toContain("working directory");
  expect(pluginServerError({ type: "sse", url: "http://127.0.0.1/mcp" })).toBe(
    "",
  );
  expect(pluginServerError({ type: "sse" })).toContain("HTTPS");
  expect(
    pluginServerError({ type: "sse", url: "http://remote/mcp" }),
  ).toContain("HTTPS");
  for (const headers of [
    { Authorization: "secret" },
    { Cookie: "x" },
    { "Proxy-Authorization": "x" },
    { "X-Duplicate": "x", "x-duplicate": "y" },
    { "Bad Header": "x" },
    { "X-Value": "\r\nInjected: x" },
  ])
    expect(
      pluginServerError({
        type: "streamable-http",
        url: "https://example.test/mcp",
        headers,
      }),
    ).toContain("header");
  expect(
    pluginServerError({
      type: "sse",
      url: "https://example.test/mcp",
      headers: { "X-Value": "${VALUE}\tvalid 日本語" },
    }),
  ).toBe("");
  expect(
    pluginServerError({ type: "stdio", command: "tool", unknown: "keep" }),
  ).toContain("unsupported");
});
