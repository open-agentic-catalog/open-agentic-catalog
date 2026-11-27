// Pure admission of the owned Grafana dashboard's actual request/result pair.
function milliseconds(value) {
  if (typeof value === "string" && /^[0-9]+$/.test(value))
    value = Number(value);
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0
    ? value
    : undefined;
}

function validRefId(value) {
  // RefIds are opaque identifiers; do not invent a new alphabet for Grafana.
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.trim() === value &&
    ![...value].some(
      (character) =>
        character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127,
    )
  );
}

function positiveFrame(frame, refId, from, to) {
  const fields = frame?.schema?.fields;
  const columns = frame?.data?.values;
  if (
    frame?.schema?.refId !== refId ||
    !Array.isArray(fields) ||
    !fields.length ||
    !Array.isArray(columns) ||
    columns.length !== fields.length ||
    columns.some(
      (column) =>
        !Array.isArray(column) ||
        !column.length ||
        column.length !== columns[0].length,
    )
  )
    return false;
  const times = columns[fields.findIndex((field) => field?.type === "time")];
  if (!times) return false;
  return times.some((time, row) => {
    if (!Number.isSafeInteger(time) || time < from || time > to) return false;
    return fields.some((field, index) => {
      const value = columns[index][row];
      return field?.type === "number" && Number.isFinite(value) && value > 0;
    });
  });
}

export function hasPositiveHeapQuery(exchange, role, window) {
  const request = exchange?.request;
  const results = exchange?.body?.results;
  const from = milliseconds(window?.[0]);
  const to = milliseconds(window?.[1]);
  if (
    !["api", "worker", "All"].includes(role) ||
    !Array.isArray(window) ||
    window.length !== 2 ||
    from === undefined ||
    to === undefined ||
    to <= from ||
    milliseconds(request?.from) !== from ||
    milliseconds(request?.to) !== to ||
    !Array.isArray(request?.queries) ||
    !results ||
    typeof results !== "object" ||
    Array.isArray(results)
  )
    return false;
  const seen = new Set();
  for (const query of request.queries) {
    if (!validRefId(query?.refId) || seen.has(query.refId)) return false;
    seen.add(query.refId);
  }
  return request.queries.some((query) => {
    // The panel returns the leading heap series. Its availability and up
    // filters also mention the metric or role and can return positive values.
    const heap =
      typeof query.expr === "string" &&
      /^[\s(]*open_agentic_catalog_go_heap_bytes\s*\{([^}]*)\}/.exec(
        query.expr,
      );
    const labels = heap && heap[1].split(",").map((label) => label.trim());
    if (
      query.datasource?.uid !== "catalog-prometheus" ||
      !labels ||
      !labels.includes('catalog_deployment="local"') ||
      !labels.includes(
        `catalog_role=~"${role === "All" ? "api|worker" : role}"`,
      ) ||
      !Object.hasOwn(results, query.refId)
    )
      return false;
    // A different query's successful frames can never qualify this heap query.
    const result = results[query.refId];
    return (
      result?.status === 200 &&
      !result.error &&
      Array.isArray(result.frames) &&
      result.frames.some((frame) => positiveFrame(frame, query.refId, from, to))
    );
  });
}
