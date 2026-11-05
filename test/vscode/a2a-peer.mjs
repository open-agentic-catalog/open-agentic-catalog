// Owned HTTP(S) glue around the unchanged pinned upstream Hello World executor.
import { listenFixture } from "./listen.mjs";
import { build } from "../../extensions/vscode/node_modules/esbuild/lib/main.js";
import { AgentCard } from "../../extensions/vscode/node_modules/@a2a-js/sdk/dist/index.js";
import {
  DefaultRequestHandler,
  InMemoryTaskStore,
  JsonRpcTransportHandler,
  ServerCallContext,
} from "../../extensions/vscode/node_modules/@a2a-js/sdk/dist/server/index.js";
import { createServer as httpServer } from "node:http";
import { createServer as httpsServer } from "node:https";
import { writeFile, mkdtemp, rm, readFile, rename } from "node:fs/promises";
import { resolve, join, dirname } from "node:path";
import { pathToFileURL } from "node:url";
const root = resolve(import.meta.dirname, "../..");

export async function a2aPeer(tls, work = join(root, "output/vscode")) {
  const directory = await mkdtemp(join(work, "a2a-peer-"));
  await build({
    entryPoints: [resolve(import.meta.dirname, ".tools/a2a-sample.ts")],
    outfile: join(directory, "sample.mjs"),
    bundle: true,
    platform: "node",
    format: "esm",
    plugins: [
      {
        name: "unchanged-upstream-linkage",
        setup(b) {
          b.onResolve(
            { filter: /^\.\.\/\.\.\/\.\.\/(?:server\/)?index\.js$/ },
            ({ path }) => ({
              path: resolve(
                root,
                "extensions/vscode/node_modules/@a2a-js/sdk/dist",
                path.includes("server") ? "server/index.js" : "index.js",
              ),
              external: true,
            }),
          );
        },
      },
    ],
  });
  const { SampleAgentExecutor } = await import(
    pathToFileURL(join(directory, "sample.mjs")).href
  );
  const seen = [],
    tasks = new Map();
  let handler,
    closed = false;
  const serve = async (req, res) => {
    try {
      if (req.url === "/__owned_stats") {
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify(seen));
        return;
      }
      seen.push({ url: req.url, headers: req.headers });
      if (
        req.headers.authorization &&
        req.headers.authorization !== "Bearer owned-a2a-secret"
      ) {
        res.writeHead(401);
        res.end("private-refusal");
        return;
      }
      let body = "";
      for await (const chunk of req) {
        body += chunk;
        if (body.length > 65536) throw Error("large request");
      }
      const request = JSON.parse(body);
      seen.at(-1).request = request;
      if (req.url === "/owned") {
        const msg = request.params.message,
          prompt = msg?.parts[0]?.text;
        let task = tasks.get(request.params.id ?? msg?.taskId);
        if (request.method === "CancelTask") {
          if (task?.metadata?.refuse) {
            res.setHeader("content-type", "application/json");
            res.end(
              JSON.stringify({
                jsonrpc: "2.0",
                id: request.id,
                error: { code: -32002, message: "private refusal" },
              }),
            );
            return;
          }
          task.status.state = "TASK_STATE_CANCELED";
        } else if (msg) {
          const contextId = msg.contextId ?? crypto.randomUUID(),
            id = msg.taskId ?? crypto.randomUUID();
          task = {
            id,
            contextId,
            status: {
              state:
                prompt === "input"
                  ? "TASK_STATE_INPUT_REQUIRED"
                  : prompt === "continue"
                    ? "TASK_STATE_COMPLETED"
                    : "TASK_STATE_WORKING",
            },
            history: [msg],
            artifacts: [],
            metadata: { refuse: prompt === "refuse" },
          };
          tasks.set(id, task);
        }
        res.setHeader("content-type", "application/json");
        if (!task) {
          res.end(
            JSON.stringify({
              jsonrpc: "2.0",
              id: request.id,
              error: { code: -32001, message: "not found" },
            }),
          );
          return;
        }
        if (request.method === "SendStreamingMessage") {
          res.setHeader("content-type", "text/event-stream");
          const publish = (result) =>
            res.write(
              `data: ${JSON.stringify({ jsonrpc: "2.0", id: request.id, result })}\n\n`,
            );
          publish({ task });
          const update = (text, append, lastChunk) => ({
            artifactUpdate: {
              taskId: task.id,
              contextId: task.contextId,
              artifact: { artifactId: "answer", parts: [{ text }] },
              append,
              lastChunk,
            },
          });
          publish(update("one ", false, false));
          publish(update("two", true, true));
          task.status.state =
            prompt === "auth"
              ? "TASK_STATE_AUTH_REQUIRED"
              : "TASK_STATE_COMPLETED";
          publish({
            statusUpdate: {
              taskId: task.id,
              contextId: task.contextId,
              status: task.status,
            },
          });
          res.end();
          return;
        }
        res.end(
          JSON.stringify({
            jsonrpc: "2.0",
            id: request.id,
            result: msg ? { task } : task,
          }),
        );
        return;
      }
      const result = await handler.handle(
        request,
        new ServerCallContext({ requestedVersion: "1.0" }),
      );
      if (Symbol.asyncIterator in result) {
        res.setHeader("content-type", "text/event-stream");
        for await (const event of result) {
          if (res.destroyed) break;
          res.write(`data: ${JSON.stringify(event)}\n\n`);
        }
        res.end();
      } else {
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify(result));
      }
    } catch {
      if (!res.headersSent) res.writeHead(500);
      res.end();
    }
  };
  const server = tls ? httpsServer(tls, serve) : httpServer(serve);
  await listenFixture(server);
  const origin = `${tls ? "https" : "http"}://127.0.0.1:${server.address().port}`;
  const card = {
    name: "Owned Agent",
    description: "Unchanged upstream Hello World executor",
    version: "1.0.0",
    supportedInterfaces: [
      {
        url: origin + "/a2a",
        protocolBinding: "JSONRPC",
        protocolVersion: "1.0",
      },
    ],
    capabilities: { streaming: true, extensions: [] },
    defaultInputModes: ["text/plain"],
    defaultOutputModes: ["text/plain"],
    skills: [
      {
        id: "hello",
        name: "Hello World",
        description: "Deterministic local greeting",
        tags: ["owned"],
      },
    ],
  };
  handler = new JsonRpcTransportHandler(
    new DefaultRequestHandler(
      AgentCard.fromJSON(card),
      new InMemoryTaskStore(),
      new SampleAgentExecutor(),
    ),
  );
  return {
    origin,
    card,
    seen,
    async close() {
      if (closed) return;
      closed = true;
      server.closeAllConnections();
      await new Promise((r) => server.close(r));
      await rm(directory, { recursive: true, force: true });
    },
  };
}
if (process.argv[1] === import.meta.filename) {
  const peer = await a2aPeer(
    {
      cert: await readFile(process.argv[3]),
      key: await readFile(process.argv[4]),
    },
    dirname(process.argv[2]),
  );
  // Publish only complete JSON; the Go fixture polls for this file's existence.
  const ready = process.argv[2];
  const pending = `${ready}.pending`;
  await writeFile(pending, JSON.stringify({ origin: peer.origin }), {
    flag: "wx",
    mode: 0o600,
  });
  await rename(pending, ready);
  process.on("SIGTERM", () => {
    void peer.close().then(() => process.exit(0));
  });
}
