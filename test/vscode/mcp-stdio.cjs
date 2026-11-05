#!/usr/bin/env node
// Owned protocol fixture. Only the real pinned npm/npx runner executes it.
const { createInterface } = require("node:readline");
const { appendFileSync } = require("node:fs");
if (
  process.versions.node !== "24.22.1" ||
  process.env.OWNED_MCP_TOKEN !== "owned-stdio-secret" ||
  process.env.CATALOG_MCP_SENTINEL_SECRET
)
  process.exit(2);
appendFileSync(process.env.OWNED_MCP_SENTINEL, "started\n");
createInterface({ input: process.stdin }).on("line", (line) => {
  const request = JSON.parse(line);
  if (request.id === undefined) return;
  let result;
  if (request.method === "initialize")
    result = {
      protocolVersion: "2025-11-25",
      capabilities: { tools: {} },
      serverInfo: { name: "owned-stdio", version: "1.0.0" },
    };
  else if (request.method === "tools/list")
    result = {
      tools: [
        {
          name: "owned_stdio_echo",
          description: "Harmless owned stdio echo",
          inputSchema: { type: "object", properties: {} },
          annotations: { readOnlyHint: true },
        },
      ],
    };
  else if (request.method === "tools/call") {
    appendFileSync(process.env.OWNED_MCP_SENTINEL, "called\n");
    result = {
      content: [{ type: "text", text: "owned stdio native invocation" }],
    };
  } else return;
  process.stdout.write(
    JSON.stringify({ jsonrpc: "2.0", id: request.id, result }) + "\n",
  );
});
