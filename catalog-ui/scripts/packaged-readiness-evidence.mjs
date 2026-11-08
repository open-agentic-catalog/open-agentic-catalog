// Failure-only deployment evidence contains fixed categories and numeric ports,
// never engine output, logs, labels, configuration, response bodies or URLs.
const services = ["api", "ui-browser"];
const errors = [
  "timeout",
  "aborted",
  "connection_refused",
  "connection_reset",
  "name_resolution",
  "tls",
  "unclassified",
];
const port = (value) =>
  Number.isInteger(value) && value > 0 && value <= 65535 ? value : undefined;
const elapsed = (value) =>
  Number.isFinite(value) && value >= 0
    ? Math.min(2147483647, Math.floor(value))
    : undefined;

function containerEvidence(text, project, service) {
  if (typeof text !== "string" || text.length > 65536)
    return { status: "invalid" };
  let items;
  try {
    const value = text.trim();
    items =
      value === ""
        ? []
        : value.startsWith("[")
          ? JSON.parse(value)
          : value.split("\n").map((line) => JSON.parse(line));
  } catch {
    return { status: "invalid" };
  }
  if (!Array.isArray(items)) return { status: "invalid" };
  if (items.length === 0) return { status: "missing" };
  const item = items[0];
  if (
    items.length !== 1 ||
    !item ||
    item.Project !== project ||
    item.Service !== service ||
    typeof item.ID !== "string" ||
    !/^[a-f0-9]{12,64}$/.test(item.ID) ||
    ![
      "created",
      "restarting",
      "running",
      "removing",
      "paused",
      "exited",
      "dead",
    ].includes(item.State) ||
    ![undefined, "", "starting", "healthy", "unhealthy"].includes(item.Health)
  )
    return { status: "invalid" };
  return {
    status: "available",
    id: item.ID,
    state: item.State,
    health: item.Health || "none",
  };
}

function publishedEvidence(text) {
  if (typeof text !== "string" || text.length > 128)
    return { status: "invalid" };
  const match = /^127\.0\.0\.1:(\d{1,5})$/.exec(text.trim());
  const publishedPort = match ? port(Number(match[1])) : undefined;
  return publishedPort
    ? {
        status: "available",
        host: "loopback",
        port: publishedPort,
        containerPort: 8080,
      }
    : { status: "invalid" };
}

/** Read only the selected owned service, separately from the unchanged readiness budget. */
export async function collectReadinessEvidence({
  project,
  service,
  attemptedPort,
  phase,
  listener,
  run,
  now = () => performance.now(),
  interrupted = () => false,
}) {
  if (
    typeof project !== "string" ||
    !/^[a-z0-9][a-z0-9_-]{0,95}$/.test(project) ||
    !services.includes(service)
  )
    throw new Error("Invalid owned readiness diagnostic target");
  const started = now();
  const commands = [
    ["ps", "--all", "--format", "json", service],
    ["port", service, "8080"],
    service === "ui-browser"
      ? ["exec", "-T", service, "/usr/local/bin/catalog-ui", "--probe"]
      : [
          "exec",
          "-T",
          service,
          "/usr/local/bin/catalogctl",
          "probe",
          "--url",
          "http://127.0.0.1:8080/readyz",
        ],
  ];
  const read = async (args, projectValue, failure = "unavailable") => {
    if (interrupted()) return { status: "interrupted" };
    const remaining = 10000 - (now() - started);
    if (remaining <= 0) return { status: "budget_exhausted" };
    try {
      const text = await run(args, Math.min(3000, remaining));
      if (interrupted()) return { status: "interrupted" };
      if (now() - started >= 10000) return { status: "budget_exhausted" };
      return projectValue(text);
    } catch {
      return {
        status: interrupted()
          ? "interrupted"
          : now() - started >= 10000
            ? "budget_exhausted"
            : failure,
      };
    }
  };
  const container = await read(commands[0], (text) =>
    containerEvidence(text, project, service),
  );
  // Always ask for the current mapping; the failed attempt can target an old port.
  const published = await read(commands[1], publishedEvidence);
  const internalProbe = await read(
    commands[2],
    () => ({ status: "ready" }),
    "failed",
  );
  return {
    service,
    phase: ["ui_restart", "listener"].includes(phase) ? phase : "other",
    interrupted: interrupted(),
    elapsedMs: elapsed(now() - started),
    attemptedPort: port(attemptedPort),
    listener: {
      interrupted: listener?.interrupted === true,
      elapsedMs: elapsed(listener?.elapsedMs),
      lastStatus:
        Number.isInteger(listener?.lastStatus) &&
        listener.lastStatus >= 100 &&
        listener.lastStatus <= 599
          ? listener.lastStatus
          : undefined,
      lastError: errors.includes(listener?.lastError)
        ? listener.lastError
        : undefined,
    },
    container,
    published,
    internalProbe,
  };
}
