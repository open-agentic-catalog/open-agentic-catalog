// Test-only bounded CDP observations. Never retain exception text, console
// arguments, request URLs/headers/bodies or private rendered content.
exports.rendererErrors = () => {
  const owned = new Set(),
    sessions = new Set(),
    slots = new Map(),
    requests = new Map();
  let ownedOverflow = false;
  const key = (session, kind, id) => JSON.stringify([session, kind, id]);
  const slot = (id) => {
    if (!slots.has(id)) {
      if (slots.size >= 256) {
        const discard = [...slots.keys()].find((value) => !owned.has(value));
        if (discard !== undefined) slots.delete(discard);
        else {
          if (owned.has(id)) ownedOverflow = true;
          return;
        }
      }
      slots.set(id, {});
    }
    return slots.get(id);
  };
  const record = (session, scope, id, kind) => {
    if (id === undefined) return;
    const identity = key(session, scope, id);
    if (sessions.has(session)) {
      if (owned.size >= 512 && !owned.has(identity)) {
        ownedOverflow = true;
        return;
      }
      owned.add(identity);
    }
    const counts = slot(identity);
    if (!counts) return;
    if (Object.values(counts).reduce((a, b) => a + b, 0) >= 256)
      counts.overflow = 1;
    else counts[kind] = (counts[kind] ?? 0) + 1;
  };
  const asset = (value) => {
    try {
      const url = new URL(value);
      return (
        ["https:", "file:", "vscode-webview:"].includes(url.protocol) &&
        /\/(?:extensions\/vscode|[^/]*open-agentic-catalog[^/]*)\/media\//u.test(
          url.pathname,
        ) &&
        /\/(?:catalog(?:-calibration-missing)?\.(?:js|css|svg|png)|a2a\.js|catalog-preview\.png|agent-conversation\.png|mcp-selection-preview\.png|my-tools-installed\.png)$/u.test(
          url.pathname,
        )
      );
    } catch {
      return false;
    }
  };
  return {
    ownSession(session) {
      if (sessions.size >= 256 && !sessions.has(session)) {
        ownedOverflow = true;
        return;
      }
      sessions.add(session);
      for (const identity of slots.keys())
        if (JSON.parse(identity)[0] === session) {
          if (owned.size >= 512 && !owned.has(identity)) {
            ownedOverflow = true;
            return;
          }
          owned.add(identity);
        }
    },
    own(session, context, frame) {
      for (const [scope, id] of [
        ["context", context],
        ["frame", frame],
      ]) {
        if (id === undefined) continue;
        const identity = key(session, scope, id);
        if (owned.size >= 512 && !owned.has(identity)) {
          ownedOverflow = true;
          return;
        }
        if (!slots.has(identity) && slots.size >= 256) ownedOverflow = true;
        owned.add(identity);
        slot(identity);
      }
    },
    observe(message) {
      const { method, params = {}, sessionId } = message;
      if (method === "Runtime.exceptionThrown")
        record(
          sessionId,
          "context",
          params.exceptionDetails?.executionContextId,
          "exception",
        );
      else if (
        method === "Runtime.consoleAPICalled" &&
        ["error", "assert"].includes(params.type)
      )
        record(sessionId, "context", params.executionContextId, "console");
      else if (
        method === "Network.requestWillBeSent" &&
        asset(params.request?.url)
      ) {
        if (requests.size >= 128)
          record(sessionId, "frame", params.frameId, "overflow");
        else
          requests.set(key(sessionId, "request", params.requestId), {
            session: sessionId,
            frame: params.frameId,
          });
      } else if (
        [
          "Network.loadingFailed",
          "Network.responseReceived",
          "Network.loadingFinished",
        ].includes(method)
      ) {
        const id = key(sessionId, "request", params.requestId),
          request = requests.get(id);
        if (!request) return;
        if (
          (method === "Network.loadingFailed" && !params.canceled) ||
          (method === "Network.responseReceived" &&
            params.response?.status >= 400)
        ) {
          record(request.session, "frame", request.frame, "asset");
          requests.delete(id);
        } else if (method !== "Network.responseReceived") requests.delete(id);
      }
    },
    counts() {
      const result = {};
      for (const identity of owned)
        for (const [kind, count] of Object.entries(slots.get(identity) ?? {}))
          result[kind] = kind === "overflow" ? 1 : (result[kind] ?? 0) + count;
      if (ownedOverflow) result.overflow = 1;
      return result;
    },
  };
};
