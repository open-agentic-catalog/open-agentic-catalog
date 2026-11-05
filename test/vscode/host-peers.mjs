import { createServer } from "node:http";
import { createServer as secureServer } from "node:https";
import { readFile } from "node:fs/promises";
import { createHash, randomBytes } from "node:crypto";
import { a2aPeer } from "./a2a-peer.mjs";
import { listenFixture } from "./listen.mjs";
import {
  generateKeyPair,
  exportJWK,
  SignJWT,
  jwtVerify,
} from "../../extensions/vscode/node_modules/jose/dist/webapi/index.js";

export async function peers(options = {}) {
  const agent = await a2aPeer(undefined, process.env.CATALOG_VSCODE_RUN_DIR);
  const { publicKey, privateKey } = await generateKeyPair("RS256");
  const jwk = {
    ...(await exportJWK(publicKey)),
    kid: "owned-key",
    alg: "RS256",
  };
  const codes = new Map();
  const stats = {
    authorization: 0,
    tokens: 0,
    manifests: 0,
    privateReads: 0,
    wrongCode: 0,
  };
  let origin, documentOrigin;
  const inspection = {
    scenario: "legacy",
    manifests: 0,
    holdDocument: false,
    queries: 0,
    documents: 0,
    abortedDocuments: 0,
    targetRequests: 0,
  };
  const longIdentifier =
    "urn:air:owned.example:team:" +
    "x".repeat(512 - "urn:air:owned.example:team:".length);
  const card = () => ({
    $schema:
      "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
    name: "io.example/consumer-replay",
    description: "Owned native cancellation fixture",
    version: "1.0.0",
    remotes: [{ type: "streamable-http", url: origin + "/owned-mcp" }],
  });
  const entries = () =>
    options.runnerDocument
      ? [
          {
            identifier: "urn:air:owned.example:team:runners",
            version: "1.0.0",
            type: "application/vnd.mcp.server+json",
            data: options.runnerDocument,
          },
        ]
      : inspection.scenario === "agent"
        ? [
            {
              identifier: "urn:air:owned.example:team:peer-agent",
              version: "1.0.0",
              type: "application/a2a-agent-card+json",
              data: {
                ...agent.card,
                supportedInterfaces: [
                  {
                    url: agent.origin + "/owned",
                    protocolBinding: "JSONRPC",
                    protocolVersion: "1.0",
                  },
                ],
              },
            },
          ]
        : inspection.scenario === "large"
          ? [undefined, "", "🙂".repeat(128), "\n".repeat(128)].map(
              (version, index) => ({
                identifier: longIdentifier,
                ...(version === undefined ? {} : { version }),
                type:
                  index === 2
                    ? "application/a2a-agent-card+json"
                    : "application/mcp-server-card+json",
                displayName: `Owned exact version ${index}`,
                data:
                  index === 2
                    ? {
                        name: "Owned inspection Agent",
                        description: "Owned",
                        version: "1.0.0",
                        supportedInterfaces: [
                          {
                            url: origin + "/uncontacted-agent",
                            protocolBinding: "JSONRPC",
                            protocolVersion: "1.0",
                          },
                        ],
                        capabilities: {},
                        defaultInputModes: ["text/plain"],
                        defaultOutputModes: ["text/plain"],
                        skills: [
                          {
                            id: "owned",
                            name: "Owned",
                            description: "Owned",
                            tags: [],
                          },
                        ],
                      }
                    : card(),
              }),
            )
          : [
              {
                identifier: "urn:air:owned.example:team:consumer",
                version: "1.0.0",
                type: "application/mcp-server-card+json",
                url: documentOrigin + "/native-card",
              },
            ];
  const reply = (response, value, status = 200) => {
    response.writeHead(status, { "content-type": "application/json" });
    response.end(JSON.stringify(value));
  };
  const serve = async (request, response) => {
    try {
      const url = new URL(request.url, origin);
      if (url.pathname === "/owned-runner-stats" && options.runnerStatus)
        return reply(response, options.runnerStatus());
      if (url.pathname === "/owned-test/inspection") {
        if (request.method === "POST") {
          let body = "";
          for await (const part of request) {
            body += part;
            if (body.length > 4096) return reply(response, {}, 413);
          }
          const value = JSON.parse(body);
          if (
            !["legacy", "large", "peer", "agent"].includes(value.scenario) ||
            typeof value.holdDocument !== "boolean" ||
            (value.scenario === "peer" && !documentOrigin)
          )
            return reply(response, {}, 400);
          inspection.scenario = value.scenario;
          inspection.holdDocument = value.holdDocument;
        }
        return reply(response, {
          ...inspection,
          agentRequests: agent.seen.length,
        });
      }
      if (url.pathname === "/native-card") {
        inspection.documents++;
        if (request.headers.authorization || request.headers.cookie)
          return reply(response, {}, 403);
        if (inspection.holdDocument) {
          response.setHeader("content-type", "application/json");
          response.write("{");
          response.once("close", () => {
            inspection.abortedDocuments++;
          });
          return;
        }
        return reply(response, card());
      }
      if (["/ard/v1/agents", "/ard/v1/search"].includes(url.pathname)) {
        if (
          request.headers.authorization &&
          request.headers.authorization !== "Bearer public-reader"
        )
          return reply(response, {}, 403);
        inspection.queries++;
        let body = "";
        for await (const part of request) {
          body += part;
          if (body.length > 4096) return reply(response, {}, 413);
        }
        const query = body ? JSON.parse(body) : {};
        return reply(
          response,
          url.pathname.endsWith("/search")
            ? {
                results: entries(),
                ...(query.pageToken
                  ? {}
                  : { pageToken: "owned-inspection-continuation" }),
              }
            : { items: entries(), total: entries().length },
        );
      }
      if (url.pathname === "/owned-mcp") {
        inspection.targetRequests++;
        if (request.headers.authorization || request.headers.cookie)
          return reply(response, {}, 403);
        if (request.method !== "POST") {
          response.writeHead(405);
          return response.end();
        }
        let body = "";
        for await (const part of request) {
          body += part;
          if (body.length > 8192) return reply(response, {}, 413);
        }
        const message = JSON.parse(body);
        if (message.id === undefined) {
          response.writeHead(202);
          return response.end();
        }
        const result =
          message.method === "initialize"
            ? {
                protocolVersion: message.params.protocolVersion,
                capabilities: { tools: {} },
                serverInfo: {
                  name: "Owned cancellation target",
                  version: "1.0.0",
                },
              }
            : message.method === "tools/list"
              ? {
                  tools: [
                    {
                      name: "owned_cancel_echo",
                      description: "Owned inert tool",
                      inputSchema: { type: "object", properties: {} },
                    },
                  ],
                }
              : {};
        return reply(response, { jsonrpc: "2.0", id: message.id, result });
      }
      if (url.pathname === "/.well-known/openid-configuration")
        return reply(response, {
          issuer: origin,
          authorization_endpoint: origin + "/authorize",
          token_endpoint: origin + "/token",
          jwks_uri: origin + "/keys",
          response_types_supported: ["code"],
          code_challenge_methods_supported: ["S256"],
        });
      if (url.pathname === "/keys") return reply(response, { keys: [jwk] });
      if (url.pathname === "/owned-browser-entry") {
        const authorization = url.searchParams.get("authorization");
        if (
          authorization &&
          (new URL(authorization).origin !== origin ||
            new URL(authorization).pathname !== "/authorize")
        )
          return reply(response, {}, 400);
        const escape = (value) =>
          value
            .replaceAll("&", "&amp;")
            .replaceAll('"', "&quot;")
            .replaceAll("<", "&lt;");
        response.writeHead(200, {
          "content-type": "text/html; charset=utf-8",
          "cache-control": "no-store",
          "content-security-policy":
            "default-src 'none'; base-uri 'none'; frame-ancestors 'none'",
        });
        return response.end(
          `<html><head><title>Owned Catalog browser login</title></head><body id="owned-browser-login"><h1>Owned Catalog browser login</h1>${authorization ? `<a id="complete-oidc" href="${escape(authorization)}">Complete sign-in in Catalog</a>` : "Waiting for the editor's explicit sign-in."}</body></html>`,
        );
      }
      if (url.pathname === "/authorize") {
        stats.authorization++;
        if (
          url.searchParams.get("client_id") !== "owned-extension" ||
          url.searchParams.get("code_challenge_method") !== "S256"
        )
          return reply(response, {}, 400);
        const code = randomBytes(16).toString("hex");
        codes.set(code, Object.fromEntries(url.searchParams));
        const target = new URL(url.searchParams.get("redirect_uri"));
        target.searchParams.set("state", url.searchParams.get("state"));
        target.searchParams.set("code", code);
        target.searchParams.set("iss", origin);
        response.writeHead(302, { location: target.href });
        return response.end();
      }
      if (url.pathname === "/token") {
        stats.tokens++;
        let bytes = "";
        for await (const chunk of request) {
          bytes += chunk;
          if (bytes.length > 8192) return reply(response, {}, 413);
        }
        const form = new URLSearchParams(bytes);
        const code = codes.get(form.get("code"));
        codes.delete(form.get("code"));
        if (
          !code ||
          createHash("sha256")
            .update(form.get("code_verifier") ?? "")
            .digest("base64url") !== code.code_challenge ||
          form.get("redirect_uri") !== code.redirect_uri ||
          form.get("client_id") !== "owned-extension"
        ) {
          stats.wrongCode++;
          return reply(response, {}, 400);
        }
        const sign = (audience) =>
          new SignJWT(
            audience === "owned-extension" ? { nonce: code.nonce } : {},
          )
            .setProtectedHeader({ alg: "RS256", kid: jwk.kid })
            .setIssuer(origin)
            .setAudience(audience)
            .setSubject("oidc-reader")
            .setIssuedAt()
            .setExpirationTime("1h")
            .sign(privateKey);
        return reply(response, {
          access_token: await sign("catalog"),
          id_token: await sign("owned-extension"),
          token_type: "Bearer",
          expires_in: 3600,
        });
      }
      if (url.pathname === "/.well-known/ard.json") {
        if (inspection.scenario === "legacy") stats.manifests++;
        else inspection.manifests++;
        if (
          request.headers.authorization &&
          request.headers.authorization !== "Bearer public-reader"
        )
          return reply(response, {}, 403);
        return reply(response, {
          entries: [{ identifier: "owned-public-entry" }],
        });
      }
      stats.privateReads++;
      const token = request.headers.authorization?.replace(/^Bearer /u, "");
      if (!token) return reply(response, {}, 401);
      await jwtVerify(token, publicKey, {
        issuer: origin,
        audience: "catalog",
      });
      if (url.pathname === "/api/v1/me")
        return reply(response, {
          tenantId: "00000000-0000-7000-8000-000000000001",
          realmId: "00000000-0000-7000-8000-000000000002",
          membershipId: "00000000-0000-7000-8000-000000000003",
          subject: "oidc-reader",
          permissions: ["catalog.read"],
          availablePermissions: ["catalog.read"],
        });
      if (url.pathname === "/api/v1/capabilities")
        return reply(response, {
          resourceKinds: ["agent", "mcp_server", "skill", "plugin"],
        });
      return reply(response, {}, 404);
    } catch {
      reply(response, {}, 401);
    }
  };
  const server = createServer(serve);
  let documents;
  const listeners = [server];
  try {
    if (process.env.CATALOG_VSCODE_DOCUMENT_KEY) {
      documents = secureServer(
        {
          cert: await readFile(process.env.CATALOG_VSCODE_PUBLIC_CA),
          key: await readFile(process.env.CATALOG_VSCODE_DOCUMENT_KEY),
        },
        serve,
      );
      listeners.push(documents);
    }
    for (const listener of listeners) await listenFixture(listener);
  } catch (error) {
    for (const listener of listeners) {
      listener.closeAllConnections();
      listener.close();
    }
    await agent.close();
    throw error;
  }
  origin = `http://127.0.0.1:${server.address().port}`;
  if (documents)
    documentOrigin = `https://127.0.0.1:${documents.address().port}`;
  return {
    origin,
    stats,
    close: async () => {
      try {
        await Promise.all(
          listeners.map((listener) => {
            listener.closeAllConnections();
            return new Promise((resolve, reject) =>
              listener.close((error) => (error ? reject(error) : resolve())),
            );
          }),
        );
      } finally {
        await agent.close();
      }
    },
  };
}
