// Observe categories only: application messages, arguments, stacks and URLs remain private.
export function browserErrors() {
  const counts = { console: 0, page: 0 };
  const record = (kind) => {
    counts[kind] = Math.min(counts[kind] + 1, 1000);
  };
  return {
    observe(context, resourceOrigins = []) {
      const ledgerOrigins = new Set(
        resourceOrigins.map((origin) => new URL(origin).origin),
      );
      const pages = new Map();
      const observe = (page) => {
        if (pages.has(page)) return;
        const error = () => record("page");
        const consoleError = (message) => {
          if (message.type() !== "error") return;
          // Chromium reports failed HTTP/transport requests separately. Their
          // exact outcomes remain mandatory in the network ledger, including
          // deliberate failures. Arbitrary application console errors still fail.
          const ledgerOwnsLocation = () => {
            try {
              const url = message.location().url;
              return (
                typeof url === "string" &&
                url.length <= 8192 &&
                ledgerOrigins.has(new URL(url).origin)
              );
            } catch {
              return false;
            }
          };
          if (
            message.args().length === 0 &&
            /^Failed to load resource: (?:the server responded with a status of [45]\d{2} \([^\r\n)]{0,64}\)|net::ERR_[A-Z_]{1,64})$/.test(
              message.text(),
            ) &&
            ledgerOwnsLocation()
          )
            return;
          record("console");
        };
        const close = () => {
          page.off("pageerror", error);
          page.off("console", consoleError);
          page.off("close", close);
          pages.delete(page);
        };
        pages.set(page, close);
        page.on("pageerror", error);
        page.on("console", consoleError);
        page.once("close", close);
      };
      const close = () => {
        context.off("page", observe);
        context.off("close", close);
        for (const stop of [...pages.values()]) stop();
      };
      context.on("page", observe);
      context.once("close", close);
      for (const page of context.pages()) observe(page);
      return close;
    },
    results: () =>
      Object.entries(counts)
        .filter(([, count]) => count > 0)
        .map(([category, count]) => ({ category, count })),
  };
}

/** Classify a failure location without retaining dynamic paths, realms or private URLs. */
export function browserFailureLocation(value) {
  let url;
  try {
    if (typeof value !== "string" || value.length > 8192)
      return { protocol: "other", route: "other" };
    url = new URL(value);
  } catch {
    return { protocol: "other", route: "other" };
  }
  const parts = url.pathname.split("/").filter(Boolean);
  const first = parts[0] === "browser" ? parts[1] : parts[0];
  const known = [
    "catalog",
    "resources",
    "versions",
    "version-revisions",
    "listings",
    "listing-revisions",
    "releases",
    "sources",
    "jobs",
    "feedback",
    "my-feedback",
    "saved-items",
    "my-work",
    "overview",
    "access",
    "members",
    "publishers",
    "namespaces",
    "webhooks",
    "settings",
    "troubleshooting",
    "auth",
    "docs",
  ];
  return {
    protocol: ["http:", "https:", "about:", "chrome-error:"].includes(
      url.protocol,
    )
      ? url.protocol
      : "other",
    route: known.includes(first)
      ? first
      : first === "realms"
        ? "provider"
        : "other",
  };
}
