// Test-only accounting: retain fixed outcome categories, never private URLs,
// query strings, response bodies, headers or transport exception text.
export function networkObserver(origin, basePath = "") {
  const expected = [];
  const unexpected = [];
  const assets = [];
  const transports = [];
  let truncated = false;
  const selected = (url) => {
    const value = new URL(url);
    return value.origin === origin ? value.pathname : undefined;
  };
  const assetEvent = (method, url, event, details = {}) => {
    if (method !== "GET") return;
    const path = selected(url);
    if (!path?.startsWith(basePath + "/assets/")) return;
    const name = path.slice((basePath + "/assets/").length);
    const match = /^(Service|DiscoveryWorkspace)-[A-Za-z0-9_-]+\.js$/u.exec(
      name,
    );
    if (!match) return;
    if (assets.length === 32) {
      truncated = true;
      return;
    }
    assets.push({
      asset: match[1] === "Service" ? "service" : "discovery",
      event,
      ...details,
    });
  };
  // Consume declared failures for this UI origin and retain only fixed diagnostic categories.
  function observe(method, url, status) {
    if (status) assetEvent(method, url, "response", { status });
    const path = selected(url);
    if (!path || (status > 0 && status < 400)) return;
    // This endpoint deliberately probes whether an anonymous browser has a
    // session, including after sign-out. Other 401s require explicit evidence.
    if (
      method === "GET" &&
      path === basePath + "/auth/session" &&
      status === 401
    )
      return;
    const match = expected.find(
      (item) =>
        item.method === method &&
        item.path === path &&
        item.status === status &&
        item.count > 0,
    );
    if (match) match.count--;
    else if (unexpected.length < 100)
      unexpected.push(
        `Unexpected UI ${status ? "HTTP " + status : "transport failure"} (${method})`,
      );
  }
  return {
    request(method, url) {
      assetEvent(method, url, "request");
    },
    finished(method, url) {
      assetEvent(method, url, "finished");
    },
    diagnostics() {
      return {
        assets: assets.map((event) => ({ ...event })),
        transports: transports.map((event) => ({ ...event })),
        truncated,
      };
    },
    expectFailure(method, path, status, count = 1) {
      if (
        !/^(GET|POST|PUT|PATCH|DELETE|HEAD)$/.test(method) ||
        typeof path !== "string" ||
        !path.startsWith("/") ||
        /[?#\r\n]/.test(path) ||
        !Number.isInteger(status) ||
        (status !== 0 && (status < 400 || status > 599)) ||
        !Number.isInteger(count) ||
        count < 1 ||
        count > 100 ||
        expected.length >= 100
      )
        throw new Error("Invalid expected network failure");
      expected.push({ method, path: basePath + path, status, count });
    },
    observe,
    failure(method, url, reason) {
      assetEvent(method, url, "failed", {
        reason:
          new Map([
            ["net::ERR_ABORTED", "aborted"],
            ["net::ERR_CONNECTION_REFUSED", "connection-refused"],
            ["net::ERR_CONNECTION_RESET", "connection-reset"],
            ["net::ERR_NETWORK_CHANGED", "network-changed"],
          ]).get(reason) ?? "other",
      });
      const path = selected(url);
      if (method === "GET" && path && reason !== "net::ERR_ABORTED") {
        if (transports.length === 32) truncated = true;
        else
          transports.push({
            family: path.startsWith(basePath + "/_catalog/api/")
              ? "api"
              : path.startsWith(basePath + "/assets/")
                ? "asset"
                : "document",
            reason:
              new Map([
                ["net::ERR_CONNECTION_REFUSED", "connection-refused"],
                ["net::ERR_CONNECTION_RESET", "connection-reset"],
                ["net::ERR_NETWORK_CHANGED", "network-changed"],
              ]).get(reason) ?? "other",
          });
      }
      // Superseded fetches and navigation cancel requests intentionally. Do
      // not treat ERR_FAILED, connection refusal or reset as cancellation.
      if (reason !== "net::ERR_ABORTED") observe(method, url, 0);
    },
    results() {
      return [
        ...unexpected,
        ...expected
          .filter((item) => item.count)
          .map(
            (item) =>
              `Expected UI failure not observed (${item.method}, ${item.status}, remaining ${item.count})`,
          ),
      ];
    },
  };
}
