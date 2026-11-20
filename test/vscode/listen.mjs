import { createServer } from "node:http";

// Fixture startup only. Fetch/Chromium reserve ports through 10080; a host's
// ephemeral range can include them. Keep the accepted loopback listener bound.
export const fixturePortAllowed = (port) =>
  Number.isInteger(port) && port >= 16384 && port <= 65535;

export async function listenFixture(server) {
  for (let attempt = 0; attempt < 32; attempt++) {
    await new Promise((resolve, reject) => {
      const failed = (error) => {
        server.off("error", failed);
        reject(error);
      };
      server.once("error", failed);
      try {
        server.listen(0, "127.0.0.1", () => {
          server.off("error", failed);
          resolve();
        });
      } catch (error) {
        failed(error);
      }
    });
    if (fixturePortAllowed(server.address()?.port)) return;
    await new Promise((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
  throw new Error(
    "No Fetch-compatible loopback fixture port within startup bound",
  );
}

// Editor fixtures deny outbound requests. Node hands CONNECT sockets to this
// listener, so a peer disconnect after the refusal also belongs to the fixture.
export function createBlockedProxy() {
  const server = createServer((_, response) => {
    response.writeHead(403);
    response.end();
  });
  server.on("connect", (_, socket) => {
    socket.once("error", (error) => {
      if (error.code !== "ECONNRESET" && error.code !== "EPIPE") throw error;
    });
    socket.end("HTTP/1.1 403 Forbidden\r\n\r\n");
  });
  return server;
}
